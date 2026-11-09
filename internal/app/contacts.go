package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	gowebdav "github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

// Contacts are vCards in address books (each user starts with "Contacts"), shown in the app and synced to phones
// and Macs over CardDAV: /dav/card/<user>/ is the user, /dav/card/<user>/books/<id>/ an address book, and <name>.vcf
// in it a contact.

const contactsSchema = `
CREATE TABLE IF NOT EXISTS addressbooks (
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	PRIMARY KEY (user, id)
);
CREATE TABLE IF NOT EXISTS cards (
	user TEXT NOT NULL,
	book TEXT NOT NULL,
	name TEXT NOT NULL,
	vcf TEXT NOT NULL,
	etag TEXT NOT NULL,
	modified INTEGER NOT NULL,
	PRIMARY KEY (user, book, name),
	FOREIGN KEY (user, book) REFERENCES addressbooks(user, id) ON DELETE CASCADE
);`

type addressBook struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) addressBooks(user string) ([]addressBook, error) {
	s.db.Exec(`INSERT OR IGNORE INTO addressbooks (user, id, name) SELECT ?, 'contacts', 'Contacts'
		WHERE NOT EXISTS (SELECT 1 FROM addressbooks WHERE user = ?)`, user, user)
	rows, err := s.db.Query(`SELECT id, name FROM addressbooks WHERE user = ? ORDER BY rowid`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []addressBook
	for rows.Next() {
		var b addressBook
		if err := rows.Scan(&b.ID, &b.Name); err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

// saveCard stores a contact, as CardDAV and the app both write them, and returns its ETag.
func (s *Server) saveCard(user, book, name string, card vcard.Card) (string, error) {
	if card.Value(vcard.FieldFormattedName) == "" {
		return "", errors.New("a contact needs a name (FN)")
	}
	if card.Value(vcard.FieldVersion) == "" {
		card.SetValue(vcard.FieldVersion, "3.0")
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return "", err
	}
	if buf.Len() > 2<<20 {
		return "", errors.New("it's too big (over 2 MB)")
	}
	etag := hashHex(buf.String())[:20]
	defer s.changed("contacts", user)
	_, err := s.db.Exec(`INSERT INTO cards (user, book, name, vcf, etag, modified) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user, book, name) DO UPDATE SET vcf = excluded.vcf, etag = excluded.etag, modified = excluded.modified`,
		user, book, name, buf.String(), etag, time.Now().Unix())
	return etag, err
}

func decodeCard(vcf string) (vcard.Card, error) {
	return vcard.NewDecoder(strings.NewReader(vcf)).Decode()
}

// --- the app's side

type labeled struct {
	Label string `json:"label"` // home, work, mobile…
	Value string `json:"value"`
}

type contact struct {
	ID       string    `json:"id"` // book/name
	Name     string    `json:"name"`
	Given    string    `json:"given"`
	Family   string    `json:"family"`
	Org      string    `json:"org"`
	Title    string    `json:"title"`
	Phones   []labeled `json:"phones"`
	Emails   []labeled `json:"emails"`
	Address  string    `json:"address"` // one line per part: street, city…
	Birthday string    `json:"birthday"`
	Notes    string    `json:"notes"`
	Groups   []string  `json:"groups"`          // CATEGORIES
	Photo    string    `json:"photo,omitempty"` // a data: URL
}

func labelOf(f *vcard.Field) string {
	for _, t := range f.Params.Types() {
		t = strings.ToLower(t)
		switch t {
		case "cell", "mobile", "iphone":
			return "mobile"
		case "home", "work", "fax", "main", "other":
			return t
		}
	}
	return ""
}

func asContact(id string, c vcard.Card) contact {
	out := contact{ID: id, Name: c.Value(vcard.FieldFormattedName), Org: strings.TrimSuffix(c.Value(vcard.FieldOrganization), ";"),
		Title: c.Value(vcard.FieldTitle), Notes: c.Value(vcard.FieldNote), Birthday: c.Value(vcard.FieldBirthday),
		Phones: []labeled{}, Emails: []labeled{}, Groups: []string{}}
	for _, f := range c[vcard.FieldCategories] {
		for _, g := range strings.Split(f.Value, ",") {
			if g = strings.TrimSpace(g); g != "" {
				out.Groups = append(out.Groups, g)
			}
		}
	}
	if n := c.Name(); n != nil {
		out.Given, out.Family = n.GivenName, n.FamilyName
	}
	for _, f := range c[vcard.FieldTelephone] {
		out.Phones = append(out.Phones, labeled{labelOf(f), f.Value})
	}
	for _, f := range c[vcard.FieldEmail] {
		out.Emails = append(out.Emails, labeled{labelOf(f), f.Value})
	}
	if a := c.Address(); a != nil {
		var parts []string
		for _, p := range []string{a.StreetAddress, strings.TrimSpace(a.PostalCode + " " + a.Locality), a.Region, a.Country} {
			if strings.TrimSpace(p) != "" {
				parts = append(parts, strings.TrimSpace(p))
			}
		}
		out.Address = strings.Join(parts, "\n")
	}
	if f := c.Get(vcard.FieldPhoto); f != nil && len(f.Value) < 400_000 {
		v := strings.ReplaceAll(f.Value, " ", "")
		if strings.HasPrefix(v, "data:image/") {
			out.Photo = v
		} else if _, err := base64.StdEncoding.DecodeString(v); err == nil && v != "" {
			kind := strings.ToLower(f.Params.Get("TYPE"))
			if kind == "" || strings.Contains(kind, "jpeg") || strings.Contains(kind, "jpg") {
				kind = "jpeg"
			}
			out.Photo = "data:image/" + kind + ";base64," + v
		}
	}
	return out
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request, u user) {
	if _, err := s.addressBooks(u.Name); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, err := s.db.Query(`SELECT book, name, vcf FROM cards WHERE user = ?`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []contact{}
	for rows.Next() {
		var book, name, vcf string
		if rows.Scan(&book, &name, &vcf) != nil {
			continue
		}
		if c, err := decodeCard(vcf); err == nil {
			list = append(list, asContact(book+"/"+name, c))
		}
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(sortName(list[i])) < strings.ToLower(sortName(list[j])) })
	writeJSON(w, http.StatusOK, list)
}

func sortName(c contact) string {
	if c.Given != "" || c.Family != "" {
		return c.Given + " " + c.Family
	}
	return c.Name
}

// apply writes the app's fields into a card, keeping what the app doesn't show (photos, other fields).
func (in contact) apply(c vcard.Card) error {
	in.Given, in.Family, in.Name = strings.TrimSpace(in.Given), strings.TrimSpace(in.Family), strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = strings.TrimSpace(in.Given + " " + in.Family)
	}
	if in.Name == "" {
		in.Name = strings.TrimSpace(in.Org)
	}
	if in.Name == "" {
		return errors.New("give the contact a name")
	}
	c.SetValue(vcard.FieldFormattedName, in.Name)
	c.SetName(&vcard.Name{GivenName: in.Given, FamilyName: in.Family})
	set := func(field, v string) {
		if v = strings.TrimSpace(v); v == "" {
			delete(c, field)
		} else {
			c.SetValue(field, v)
		}
	}
	set(vcard.FieldOrganization, in.Org)
	set(vcard.FieldTitle, in.Title)
	set(vcard.FieldNote, in.Notes)
	set(vcard.FieldBirthday, in.Birthday)
	list := func(field string, items []labeled) {
		delete(c, field)
		for _, it := range items {
			if v := strings.TrimSpace(it.Value); v != "" {
				f := &vcard.Field{Value: v, Params: vcard.Params{}}
				switch it.Label {
				case "mobile":
					f.Params.Set(vcard.ParamType, vcard.TypeCell)
				case "home", "work", "fax", "other":
					f.Params.Set(vcard.ParamType, strings.ToUpper(it.Label))
				}
				c.Add(field, f)
			}
		}
	}
	if in.Groups != nil {
		var gs []string
		for _, g := range in.Groups {
			if g = strings.TrimSpace(strings.ReplaceAll(g, ",", " ")); g != "" {
				gs = append(gs, g)
			}
		}
		set(vcard.FieldCategories, strings.Join(gs, ","))
	}
	list(vcard.FieldTelephone, in.Phones)
	list(vcard.FieldEmail, in.Emails)
	if lines := strings.Split(strings.TrimSpace(in.Address), "\n"); strings.TrimSpace(in.Address) != "" {
		a := &vcard.Address{Field: &vcard.Field{Params: vcard.Params{}}}
		a.StreetAddress = strings.TrimSpace(lines[0])
		if len(lines) > 1 {
			a.Locality = strings.TrimSpace(lines[1])
		}
		if len(lines) > 2 {
			a.Region = strings.TrimSpace(lines[2])
		}
		if len(lines) > 3 {
			a.Country = strings.TrimSpace(strings.Join(lines[3:], " "))
		}
		delete(c, vcard.FieldAddress)
		c.AddAddress(a)
	} else {
		delete(c, vcard.FieldAddress)
	}
	c.SetValue(vcard.FieldRevision, time.Now().UTC().Format("20060102T150405Z"))
	return nil
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request, u user) {
	var in contact
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	books, err := s.addressBooks(u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "3.0")
	uid := strings.ToLower(randomToken(16))
	c.SetValue(vcard.FieldUID, uid)
	if err := in.apply(c); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := uid + ".vcf"
	if _, err := s.saveCard(u.Name, books[0].ID, name, c); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logActivity(u.Name, u, "added the contact “"+c.Value(vcard.FieldFormattedName)+"”", undoStep{Kind: "trash-card", Cal: books[0].ID, Name: name})
	writeJSON(w, http.StatusCreated, asContact(books[0].ID+"/"+name, c))
}

func (s *Server) loadCard(user, book, name string) (vcard.Card, error) {
	var vcf string
	if err := s.db.QueryRow(`SELECT vcf FROM cards WHERE user = ? AND book = ? AND name = ?`, user, book, name).Scan(&vcf); err != nil {
		return nil, err
	}
	return decodeCard(vcf)
}

func (s *Server) updateContact(w http.ResponseWriter, r *http.Request, u user) {
	book, name := r.PathValue("book"), r.PathValue("name")
	var in contact
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	before := s.cardText(u.Name, book, name)
	c, err := s.loadCard(u.Name, book, name)
	if err != nil {
		jsonError(w, http.StatusNotFound, "That contact isn't there any more.")
		return
	}
	defer s.logActivity(u.Name, u, "changed the contact “"+c.Value(vcard.FieldFormattedName)+"”", undoStep{Kind: "put-card", Cal: book, Name: name, Data: before})
	if err := in.apply(c); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.saveCard(u.Name, book, name, c); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, asContact(book+"/"+name, c))
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request, u user) {
	if err := s.trashCard(u.Name, r.PathValue("book"), r.PathValue("name")); err != nil {
		jsonError(w, http.StatusNotFound, "That contact isn't there any more.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- CardDAV

type cardBackend struct {
	s *Server
	u user
}

func (b *cardBackend) home() string { return "/dav/card/" + b.u.Name + "/books/" }

func (b *cardBackend) CurrentUserPrincipal(context.Context) (string, error) {
	return "/dav/card/" + b.u.Name + "/", nil
}

func (b *cardBackend) AddressBookHomeSetPath(context.Context) (string, error) { return b.home(), nil }

func (b *cardBackend) split(p string) (book, name string, err error) {
	rest, ok := strings.CutPrefix(p, b.home())
	if !ok {
		return "", "", gowebdav.NewHTTPError(http.StatusForbidden, errors.New("that's another user's"))
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) > 2 || !davName.MatchString(parts[0]) || strings.Contains(parts[0], "~") || (len(parts) == 2 && !davName.MatchString(parts[1])) {
		return "", "", gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such address book"))
	}
	if len(parts) == 2 {
		name = parts[1]
	}
	return parts[0], name, nil
}

func (b *cardBackend) asBook(a addressBook) carddav.AddressBook {
	return carddav.AddressBook{Path: b.home() + a.ID + "/", Name: a.Name, MaxResourceSize: 2 << 20}
}

func (b *cardBackend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	list, err := b.s.addressBooks(b.u.Name)
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressBook, 0, len(list))
	for _, a := range list {
		out = append(out, b.asBook(a))
	}
	return out, nil
}

func (b *cardBackend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	id, _, err := b.split(p)
	if err != nil {
		return nil, err
	}
	var a addressBook
	if err := b.s.db.QueryRow(`SELECT id, name FROM addressbooks WHERE user = ? AND id = ?`, b.u.Name, id).Scan(&a.ID, &a.Name); err != nil {
		return nil, gowebdav.NewHTTPError(http.StatusNotFound, err)
	}
	ab := b.asBook(a)
	return &ab, nil
}

func (b *cardBackend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	id, _, err := b.split(ab.Path)
	if err != nil {
		return err
	}
	name := ab.Name
	if name == "" {
		name = id
	}
	_, err = b.s.db.Exec(`INSERT INTO addressbooks (user, id, name) VALUES (?, ?, ?)`, b.u.Name, id, name)
	return err
}

func (b *cardBackend) DeleteAddressBook(ctx context.Context, p string) error {
	id, _, err := b.split(p)
	if err != nil {
		return err
	}
	_, err = b.s.db.Exec(`DELETE FROM addressbooks WHERE user = ? AND id = ?`, b.u.Name, id)
	return err
}

func (b *cardBackend) object(book, name, vcf, etag string, mod int64) (*carddav.AddressObject, error) {
	c, err := decodeCard(vcf)
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: b.home() + book + "/" + name, ModTime: time.Unix(mod, 0), ContentLength: int64(len(vcf)),
		ETag: etag, Card: c}, nil
}

func (b *cardBackend) GetAddressObject(ctx context.Context, p string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	book, name, err := b.split(p)
	if err != nil || name == "" {
		return nil, gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such contact"))
	}
	var vcf, etag string
	var mod int64
	err = b.s.db.QueryRow(`SELECT vcf, etag, modified FROM cards WHERE user = ? AND book = ? AND name = ?`, b.u.Name, book, name).Scan(&vcf, &etag, &mod)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gowebdav.NewHTTPError(http.StatusNotFound, err)
	} else if err != nil {
		return nil, err
	}
	return b.object(book, name, vcf, etag, mod)
}

func (b *cardBackend) ListAddressObjects(ctx context.Context, p string, _ *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	book, _, err := b.split(p)
	if err != nil {
		return nil, err
	}
	rows, err := b.s.db.Query(`SELECT name, vcf, etag, modified FROM cards WHERE user = ? AND book = ?`, b.u.Name, book)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []carddav.AddressObject
	for rows.Next() {
		var name, vcf, etag string
		var mod int64
		if err := rows.Scan(&name, &vcf, &etag, &mod); err != nil {
			return nil, err
		}
		if o, err := b.object(book, name, vcf, etag, mod); err == nil {
			out = append(out, *o)
		}
	}
	return out, rows.Err()
}

func (b *cardBackend) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	all, err := b.ListAddressObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	if query != nil && len(query.PropFilters) == 0 {
		// No conditions match every contact (the library's filter matches none).
		if query.Limit > 0 && query.Limit < len(all) {
			all = all[:query.Limit]
		}
		return all, nil
	}
	return carddav.Filter(query, all)
}

func (b *cardBackend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	book, name, err := b.split(p)
	if err != nil || name == "" {
		return nil, gowebdav.NewHTTPError(http.StatusForbidden, errors.New("contacts go inside an address book"))
	}
	var exists int
	if b.s.db.QueryRow(`SELECT 1 FROM addressbooks WHERE user = ? AND id = ?`, b.u.Name, book).Scan(&exists) != nil {
		return nil, gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such address book"))
	}
	var current string
	had := b.s.db.QueryRow(`SELECT etag FROM cards WHERE user = ? AND book = ? AND name = ?`, b.u.Name, book, name).Scan(&current) == nil
	if opts != nil {
		if opts.IfNoneMatch.IsSet() && had {
			if m, _ := opts.IfNoneMatch.MatchETag(current); m || opts.IfNoneMatch.IsWildcard() {
				return nil, gowebdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("it's already there"))
			}
		}
		if opts.IfMatch.IsSet() {
			if m, _ := opts.IfMatch.MatchETag(current); !had || (!m && !opts.IfMatch.IsWildcard()) {
				return nil, gowebdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("it changed meanwhile"))
			}
		}
	}
	if _, err := b.s.saveCard(b.u.Name, book, name, card); err != nil {
		return nil, gowebdav.NewHTTPError(http.StatusBadRequest, err)
	}
	return b.GetAddressObject(ctx, p, nil)
}

func (b *cardBackend) DeleteAddressObject(ctx context.Context, p string) error {
	book, name, err := b.split(p)
	if err != nil || name == "" {
		return gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such contact"))
	}
	if b.s.trashCard(b.u.Name, book, name) != nil {
		return gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such contact"))
	}
	return nil
}
