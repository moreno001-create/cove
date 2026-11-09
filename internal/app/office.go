package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Documents open in Collabora Online, which reads and saves them through Cove's WOPI endpoints (/wopi/files/<id>).
// Each opening gets a signed token naming the user and the file, good for ten hours.

const officeTokenLife = 10 * time.Hour

type officeApps struct {
	fetched time.Time
	actions map[string]string // extension → Collabora's page for it (edit if it can, else view)
}

// officeActions reads Collabora's discovery (which file types it opens, and where), at most hourly.
func (s *Server) officeActions(ctx context.Context) (map[string]string, error) {
	if s.cfg.Collabora == "" {
		return nil, errors.New("documents are off: neither COVE_OFFICE_INTERNAL nor COVE_COLLABORA is set")
	}
	discovery := s.cfg.Collabora
	if s.cfg.OfficeInternal != "" {
		discovery = s.cfg.OfficeInternal
	}
	s.mu.Lock()
	if s.office != nil && time.Since(s.office.fetched) < time.Hour {
		a := s.office.actions
		s.mu.Unlock()
		return a, nil
	}
	s.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, discovery+"/hosting/discovery", nil)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach Collabora Online at %s: %w", discovery, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Collabora Online answered %d", res.StatusCode)
	}
	var d struct {
		Zones []struct {
			Apps []struct {
				Actions []struct {
					Name   string `xml:"name,attr"`
					Ext    string `xml:"ext,attr"`
					URLSrc string `xml:"urlsrc,attr"`
				} `xml:"action"`
			} `xml:"app"`
		} `xml:"net-zone"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&d); err != nil {
		return nil, fmt.Errorf("Collabora Online's discovery can't be read: %w", err)
	}
	actions := map[string]string{}
	for _, z := range d.Zones {
		for _, a := range z.Apps {
			for _, act := range a.Actions {
				if act.Ext == "" || act.URLSrc == "" || (act.Name != "edit" && act.Name != "view") {
					continue
				}
				if _, have := actions[act.Ext]; !have || act.Name == "edit" {
					actions[act.Ext] = act.URLSrc
				}
			}
		}
	}
	s.mu.Lock()
	s.office = &officeApps{fetched: time.Now(), actions: actions}
	s.mu.Unlock()
	return actions, nil
}

// Office files the app offers to open in Collabora (it opens more, like images and PDFs, which Cove shows itself).
var officeExts = map[string]bool{"doc": true, "docx": true, "odt": true, "rtf": true, "txt": false, "xls": true, "xlsx": true,
	"ods": true, "csv": true, "ppt": true, "pptx": true, "odp": true, "odg": true, "vsdx": true, "pages": true, "numbers": true, "key": true}

func (s *Server) officeTypes(w http.ResponseWriter, r *http.Request, u user) {
	s.writeOfficeTypes(w, r)
}

func (s *Server) writeOfficeTypes(w http.ResponseWriter, r *http.Request) {
	exts := []string{}
	if actions, err := s.officeActions(r.Context()); err == nil {
		for e := range actions {
			if officeExts[e] {
				exts = append(exts, e)
			}
		}
	} else if s.cfg.Collabora != "" {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exts": exts})
}

// officeToken lets Collabora read (and with Write, save) one file. Everyone opening the same file gets the same file
// id, so they edit it together.
type officeToken struct {
	Owner   string `json:"o"` // whose folder the file is in
	Path    string `json:"p"`
	User    string `json:"u"` // who's editing: a user, or "guest:<id>" for someone with a link
	Name    string `json:"n"` // a guest's name
	Write   bool   `json:"w"`
	Expires int64  `json:"e"` // unix milliseconds
	// What gave access, checked again on every request (so ending a share or a link ends editing too).
	Share int64  `json:"s,omitempty"` // a people share
	Link  string `json:"l,omitempty"` // a share link's token
}

func fileID(owner, p string) string { return hashHex(owner + "\x00" + p)[:32] }

func (s *Server) officeOpen(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.path(in.Path)
	s.openDocument(w, r, officeToken{Owner: a.owner, Path: p, User: u.Name, Write: a.edit, Share: a.share})
	s.touchRecent(u, a, p)
}

// openDocument answers with where Collabora shows the file and the token it reads it with.
func (s *Server) openDocument(w http.ResponseWriter, r *http.Request, tok officeToken) {
	info, err := s.data.Stat(s.rel(tok.Owner, tok.Path))
	if err != nil || info.IsDir() {
		jsonError(w, http.StatusNotFound, "That file isn't there any more.")
		return
	}
	actions, err := s.officeActions(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	src, ok := actions[strings.ToLower(strings.TrimPrefix(path.Ext(tok.Path), "."))]
	if !ok {
		jsonError(w, http.StatusBadRequest, "Collabora Online doesn't open this kind of file.")
		return
	}
	tok.Expires = time.Now().Add(officeTokenLife).UnixMilli()
	b, _ := json.Marshal(tok)
	access := s.sign(base64.RawURLEncoding.EncodeToString(b))
	wopiBase := s.cfg.URL
	if s.cfg.WOPIURL != "" {
		wopiBase = s.cfg.WOPIURL
	}
	wopiSrc := wopiBase + "/wopi/files/" + fileID(tok.Owner, tok.Path)
	sep := "?"
	if strings.Contains(src, "?") {
		sep = "&"
		if strings.HasSuffix(src, "?") || strings.HasSuffix(src, "&") {
			sep = ""
		}
	}
	extra := "&lang=en"
	if tok.Write && !strings.HasPrefix(tok.User, "guest:") {
		extra += "&revisionhistory=1" // Collabora's File → Revision History asks Cove to show its versions
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": src + sep + "WOPISrc=" + url.QueryEscape(wopiSrc) + extra,
		"token": access, "ttl": tok.Expires, "write": tok.Write})
}

// wopiFile checks a WOPI request's token against the file it asks for.
func (s *Server) wopiFile(w http.ResponseWriter, r *http.Request) (officeToken, user, bool) {
	var tok officeToken
	raw := r.URL.Query().Get("access_token")
	if raw == "" {
		raw = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	v, ok := s.verify(raw)
	if ok {
		b, err := base64.RawURLEncoding.DecodeString(v)
		ok = err == nil && json.Unmarshal(b, &tok) == nil
	}
	if !ok || tok.Expires < time.Now().UnixMilli() || fileID(tok.Owner, tok.Path) != r.PathValue("id") || !s.stillAllowed(tok) {
		http.Error(w, "invalid or expired access token", http.StatusUnauthorized)
		return tok, user{}, false
	}
	if strings.HasPrefix(tok.User, "guest:") {
		name := tok.Name
		if name == "" {
			name = "Guest"
		}
		return tok, user{Name: tok.User, Display: name + " (guest)"}, true
	}
	var u user
	if err := s.db.QueryRow(`SELECT name, display, email, admin FROM users WHERE name = ?`, tok.User).Scan(&u.Name, &u.Display, &u.Email, &u.Admin); err != nil {
		http.Error(w, "no such user", http.StatusUnauthorized)
		return tok, u, false
	}
	return tok, u, true
}

// stillAllowed says whether what opened the document still gives access: the link or share hasn't ended, and
// still lets the person change it when the token says so.
func (s *Server) stillAllowed(tok officeToken) bool {
	switch {
	case tok.Link != "":
		var mode int
		var expires int64
		if s.db.QueryRow(`SELECT upload, expires FROM shares WHERE token = ? AND user = ?`, tok.Link, tok.Owner).Scan(&mode, &expires) != nil {
			return false
		}
		return (expires == 0 || expires > time.Now().Unix()) && (!tok.Write || mode >= 0 && mode < len(shareModes) && shareModes[mode] == "edit")
	case strings.HasPrefix(tok.User, "guest:"):
		return false // guests always come through a link
	case tok.User == tok.Owner:
		return true
	case tok.Share != 0:
		var p string
		var edit bool
		if s.db.QueryRow(`SELECT path, edit FROM user_shares WHERE id = ? AND owner = ? AND target = ?`, tok.Share, tok.Owner, tok.User).Scan(&p, &edit) != nil {
			return false
		}
		return (tok.Path == p || strings.HasPrefix(tok.Path, strings.TrimSuffix(p, "/")+"/") || p == ".") && (!tok.Write || edit)
	}
	return false
}

func wopiTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// wopiInfo is CheckFileInfo: what the file is and what the user may do with it.
func (s *Server) wopiInfo(w http.ResponseWriter, r *http.Request) {
	tok, u, ok := s.wopiFile(w, r)
	if !ok {
		return
	}
	info, err := s.data.Stat(s.rel(tok.Owner, tok.Path))
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	display := u.Display
	if display == "" {
		display = u.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"BaseFileName":            path.Base(tok.Path),
		"Size":                    info.Size(),
		"OwnerId":                 tok.Owner,
		"UserId":                  u.Name,
		"UserFriendlyName":        display,
		"Version":                 fmt.Sprint(info.ModTime().UnixNano()),
		"LastModifiedTime":        wopiTime(info.ModTime()),
		"UserCanWrite":            tok.Write,
		"IsAdminUser":             u.Admin, // Collabora's admin-only notices (updates, server audit) go to Cove's admins only
		"UserCanNotWriteRelative": true,
		"SupportsUpdate":          true,
		"SupportsLocks":           false,
		"SupportsRename":          false,
		"PostMessageOrigin":       s.cfg.URL,
		"HideSaveOption":          false,
		"DisablePrint":            false,
	})
}

// wopiGet is GetFile.
func (s *Server) wopiGet(w http.ResponseWriter, r *http.Request) {
	tok, _, ok := s.wopiFile(w, r)
	if !ok {
		return
	}
	f, err := s.data.Open(s.rel(tok.Owner, tok.Path))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-WOPI-ItemVersion", fmt.Sprint(info.ModTime().UnixNano()))
	io.Copy(w, f)
}

// wopiPut is PutFile. Collabora names the version it started from; a file changed meanwhile (by a sync app, say) is
// refused, and Collabora asks the user what to do.
func (s *Server) wopiPut(w http.ResponseWriter, r *http.Request) {
	tok, _, ok := s.wopiFile(w, r)
	if !ok {
		return
	}
	if o := r.Header.Get("X-WOPI-Override"); o != "" && o != "PUT" {
		http.Error(w, "not supported", http.StatusNotImplemented)
		return
	}
	if !tok.Write {
		http.Error(w, "this document is open to view only", http.StatusUnauthorized)
		return
	}
	name := s.rel(tok.Owner, tok.Path)
	info, err := s.data.Stat(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if ts := r.Header.Get("X-COOL-WOPI-Timestamp"); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && !t.Equal(info.ModTime()) {
			writeJSON(w, http.StatusConflict, map[string]any{"COOLStatusCode": 1010})
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<30))
	if err != nil {
		http.Error(w, "the document didn't arrive", http.StatusBadRequest)
		return
	}
	if _, err := s.writeFile(tok.Owner, tok.Path, bytes.NewReader(body), true, time.Time{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	info, err = s.data.Stat(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tok.User != tok.Owner && s.once("edit|"+r.PathValue("id")+"|"+tok.User, 30*time.Minute) {
		who := tok.Name + " (guest)"
		if !strings.HasPrefix(tok.User, "guest:") {
			who = s.displayOf(tok.User)
		} else if tok.Name == "" {
			who = "A guest"
		}
		s.notify(tok.Owner, fmt.Sprintf("%s edited “%s”", who, path.Base(tok.Path)), "#/recent")
	}
	w.Header().Set("X-WOPI-ItemVersion", fmt.Sprint(info.ModTime().UnixNano()))
	writeJSON(w, http.StatusOK, map[string]string{"LastModifiedTime": wopiTime(info.ModTime())})
}

// wopiOther answers the WOPI operations Cove doesn't offer (locks, renames, Save As): CheckFileInfo says so, so
// Collabora doesn't ask.
func (s *Server) wopiOther(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.wopiFile(w, r); !ok {
		return
	}
	http.Error(w, "not supported", http.StatusNotImplemented)
}

// newDocument makes an empty document, spreadsheet or presentation, which Collabora opens as a new one.
func (s *Server) newDocument(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path     string `json:"path"` // the folder
		Kind     string `json:"kind"` // document, spreadsheet, presentation
		Name     string `json:"name"`
		Template string `json:"template"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	content, tplExt, fromTemplate := templateFile(in.Template, u)
	if fromTemplate {
		in.Kind = map[string]string{".docx": "document", ".xlsx": "spreadsheet"}[tplExt]
	}
	ext, ok := map[string]string{"document": ".docx", "spreadsheet": ".xlsx", "presentation": ".pptx"}[in.Kind]
	if !ok {
		jsonError(w, http.StatusBadRequest, "kind is document, spreadsheet or presentation.")
		return
	}
	name := strings.TrimSpace(path.Base(cleanRel(in.Name)))
	if name == "." || name == "" {
		name = map[string]string{"document": "Document", "spreadsheet": "Spreadsheet", "presentation": "Presentation"}[in.Kind]
	}
	if !strings.EqualFold(path.Ext(name), ext) {
		name += ext
	}
	dir := a.path(in.Path)
	if a.file {
		jsonError(w, http.StatusBadRequest, "New documents go in a folder.")
		return
	}
	final, err := s.writeFile(a.owner, path.Join(dir, name), bytes.NewReader(content), false, time.Time{})
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": a.shown(final)})
}
