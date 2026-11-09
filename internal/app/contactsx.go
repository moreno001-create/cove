package app

import (
	"net/http"
	"sort"
	"strings"

	"github.com/emersion/go-vcard"
)

// Contact groups are vCard CATEGORIES (phones show them too). Duplicates (the same name, email or phone, as two
// imports tend to leave) can be merged into one.

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func (s *Server) contactDuplicates(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT book, name, vcf FROM cards WHERE user = ?`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var all []contact
	for rows.Next() {
		var book, name, vcf string
		if rows.Scan(&book, &name, &vcf) != nil {
			continue
		}
		if c, err := decodeCard(vcf); err == nil {
			p := asContact(book+"/"+name, c)
			p.Photo = ""
			all = append(all, p)
		}
	}
	rows.Close()
	// union-find over shared keys
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	seen := map[string]int{}
	for i, p := range all {
		keys := []string{"n:" + strings.ToLower(strings.TrimSpace(p.Name))}
		for _, e := range p.Emails {
			keys = append(keys, "e:"+strings.ToLower(strings.TrimSpace(e.Value)))
		}
		for _, t := range p.Phones {
			if d := digits(t.Value); len(d) >= 7 {
				keys = append(keys, "t:"+d[len(d)-7:])
			}
		}
		for _, k := range keys {
			if j, ok := seen[k]; ok {
				parent[find(i)] = find(j)
			} else {
				seen[k] = i
			}
		}
	}
	groups := map[int][]contact{}
	for i, p := range all {
		groups[find(i)] = append(groups[find(i)], p)
	}
	out := [][]contact{}
	for _, g := range groups {
		if len(g) > 1 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i][0].Name) < strings.ToLower(out[j][0].Name) })
	writeJSON(w, http.StatusOK, out)
}

// mergeContacts folds the others into the first: their phones, emails and groups added, empty fields filled, notes
// joined. The others go to the trash.
func (s *Server) mergeContacts(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		IDs []string `json:"ids"`
	}
	readJSON(r, &in)
	if len(in.IDs) < 2 {
		jsonError(w, http.StatusBadRequest, "Pick at least two contacts.")
		return
	}
	book, name, _ := strings.Cut(in.IDs[0], "/")
	keep, err := s.loadCard(u.Name, book, name)
	if err != nil {
		jsonError(w, http.StatusNotFound, "That contact isn't there any more.")
		return
	}
	has := func(field, v string) bool {
		for _, f := range keep[field] {
			if strings.EqualFold(strings.TrimSpace(f.Value), strings.TrimSpace(v)) || (field == vcard.FieldTelephone && digits(f.Value) == digits(v)) {
				return true
			}
		}
		return false
	}
	for _, id := range in.IDs[1:] {
		b, n, _ := strings.Cut(id, "/")
		other, err := s.loadCard(u.Name, b, n)
		if err != nil {
			continue
		}
		for _, field := range []string{vcard.FieldTelephone, vcard.FieldEmail, vcard.FieldCategories, vcard.FieldURL, vcard.FieldAddress} {
			for _, f := range other[field] {
				if !has(field, f.Value) {
					keep.Add(field, f)
				}
			}
		}
		for _, field := range []string{vcard.FieldOrganization, vcard.FieldTitle, vcard.FieldBirthday, vcard.FieldPhoto, vcard.FieldName} {
			if keep.Get(field) == nil && other.Get(field) != nil {
				keep[field] = other[field]
			}
		}
		if note := other.Value(vcard.FieldNote); note != "" && !strings.Contains(keep.Value(vcard.FieldNote), note) {
			keep.SetValue(vcard.FieldNote, strings.TrimSpace(keep.Value(vcard.FieldNote)+"\n"+note))
		}
		s.trashCard(u.Name, b, n)
	}
	if _, err := s.saveCard(u.Name, book, name, keep); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, asContact(in.IDs[0], keep))
}
