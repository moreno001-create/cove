package app

import (
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Sharing page: everything you share and everything shared with you, in one list, each with who can see it
// and how to stop. Household things (the Fridge, chat, the lists) aren't here: they're the household's, and the
// household's members are on that page.

type shareEntry struct {
	Kind  string `json:"kind"`  // file, folder, calendar, list, album, link
	Name  string `json:"name"`  // what it is
	Path  string `json:"path"`  // for files and folders
	With  string `json:"with"`  // who: a person, a group, "anyone with the link", a household
	Edit  bool   `json:"edit"`  // they can change it
	Stop  string `json:"stop"`  // the API to call (DELETE) to stop sharing it ("" if it can't be stopped here)
	Since int64  `json:"since"` // when it was shared
	Note  string `json:"note"`  // "until Dec 3", "with a password", "drop box"
}

func (s *Server) sharingOverview(w http.ResponseWriter, r *http.Request, u user) {
	mine, withMe := []shareEntry{}, []shareEntry{}
	kindOfPath := func(owner, p string) string {
		if info, err := s.data.Stat(s.rel(owner, p)); err == nil && info.IsDir() {
			return "folder"
		}
		return "file"
	}
	nameOf := func(p string) string {
		if p == "." || p == "" {
			return "All files"
		}
		return path.Base(p)
	}
	// links
	if rows, err := s.db.Query(`SELECT token, path, password != '', expires, upload, created FROM shares WHERE user = ? ORDER BY created DESC`, u.Name); err == nil {
		type link struct {
			token, p string
			pw       bool
			exp      int64
			mode     int
			created  int64
		}
		var links []link
		for rows.Next() {
			var l link
			if rows.Scan(&l.token, &l.p, &l.pw, &l.exp, &l.mode, &l.created) == nil {
				links = append(links, l)
			}
		}
		rows.Close()
		for _, l := range links {
			var notes []string
			if l.mode >= 0 && l.mode < len(shareModes) && shareModes[l.mode] != "view" {
				notes = append(notes, map[string]string{"upload": "people can add files", "drop": "drop box", "edit": "people can edit"}[shareModes[l.mode]])
			}
			if l.pw {
				notes = append(notes, "with a password")
			}
			if l.exp > 0 {
				notes = append(notes, "until "+time.Unix(l.exp, 0).Format("Jan 2"))
			}
			mine = append(mine, shareEntry{Kind: "link", Name: nameOf(l.p), Path: "/" + strings.TrimPrefix(l.p, "/"), With: "Anyone with the link",
				Edit: l.mode > 0, Stop: "/api/shares/" + l.token, Since: l.created, Note: strings.Join(notes, ", ")})
		}
	}
	// people shares, both ways (group shares show once, as the group)
	if rows, err := s.db.Query(`SELECT id, owner, path, target, edit, created, via FROM user_shares WHERE owner = ? OR target = ? ORDER BY created DESC`, u.Name, u.Name); err == nil {
		type us struct {
			id               int64
			owner, p, target string
			edit             bool
			created, via     int64
		}
		var all []us
		for rows.Next() {
			var x us
			if rows.Scan(&x.id, &x.owner, &x.p, &x.target, &x.edit, &x.created, &x.via) == nil {
				all = append(all, x)
			}
		}
		rows.Close()
		for _, x := range all {
			if x.owner == u.Name {
				if x.via == 0 {
					mine = append(mine, shareEntry{Kind: kindOfPath(x.owner, x.p), Name: nameOf(x.p), Path: "/" + x.p, With: s.displayOf(x.target), Edit: x.edit,
						Stop: "/api/user-shares/" + itoa(x.id), Since: x.created})
				}
			} else {
				withMe = append(withMe, shareEntry{Kind: kindOfPath(x.owner, x.p), Name: nameOf(x.p), Path: "/" + x.p, With: s.displayOf(x.owner), Edit: x.edit, Since: x.created})
			}
		}
	}
	if rows, err := s.db.Query(`SELECT id, kind, ref, grp, edit, created FROM group_shares WHERE owner = ? ORDER BY created DESC`, u.Name); err == nil {
		for rows.Next() {
			var id, created int64
			var kind, ref, grp string
			var edit bool
			if rows.Scan(&id, &kind, &ref, &grp, &edit, &created) == nil {
				name := nameOf(ref)
				if kind == "calendar" {
					s.db.QueryRow(`SELECT name FROM calendars WHERE user = ? AND id = ?`, u.Name, ref).Scan(&name)
				}
				mine = append(mine, shareEntry{Kind: map[bool]string{true: "calendar", false: "folder"}[kind == "calendar"], Name: name, Path: "/" + ref,
					With: "the group “" + grp + "”", Edit: edit, Stop: "/api/group-shares/" + itoa(id), Since: created})
			}
		}
		rows.Close()
	}
	// calendars and task lists, both ways
	if rows, err := s.db.Query(`SELECT s.id, s.owner, s.target, s.edit, s.created, c.name, s.via FROM calendar_shares s JOIN calendars c ON c.user = s.owner AND c.id = s.cal
		WHERE s.owner = ? OR s.target = ? ORDER BY s.created DESC`, u.Name, u.Name); err == nil {
		for rows.Next() {
			var id, created, via int64
			var owner, target, name string
			var edit bool
			if rows.Scan(&id, &owner, &target, &edit, &created, &name, &via) != nil {
				continue
			}
			if owner == u.Name {
				if via == 0 {
					mine = append(mine, shareEntry{Kind: "calendar", Name: name, With: target, Edit: edit, Stop: "/api/calendar-shares/" + itoa(id), Since: created})
				}
			} else {
				withMe = append(withMe, shareEntry{Kind: "calendar", Name: name, With: owner, Edit: edit, Since: created})
			}
		}
		rows.Close()
	}
	// albums shared with a household
	if rows, err := s.db.Query(`SELECT a.id, a.name, a.created, coalesce(h.name, '') FROM albums a LEFT JOIN households h ON h.id = a.household
		WHERE a.owner = ? AND a.shared = 1`, u.Name); err == nil {
		for rows.Next() {
			var id, created int64
			var name, home string
			if rows.Scan(&id, &name, &created, &home) == nil {
				mine = append(mine, shareEntry{Kind: "album", Name: name, With: "everyone in " + firstNonEmpty(home, "the household"), Edit: true, Since: created,
					Note: "they can add their photos"})
			}
		}
		rows.Close()
	}
	for i := range mine {
		if mine[i].Kind == "calendar" {
			mine[i].With = s.displayOf(mine[i].With)
		}
	}
	for i := range withMe {
		if withMe[i].Kind == "calendar" {
			withMe[i].With = s.displayOf(withMe[i].With)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mine": mine, "withMe": withMe})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// sharedIn says, for the things right inside a folder of the owner's, who each is shared with ("Bob, Ann, link").
func (s *Server) sharedIn(owner, dir string) map[string]string {
	who := map[string][]string{}
	child := func(p string) (string, bool) {
		p = strings.TrimPrefix(p, "/")
		parent := path.Dir(p)
		if dir == "." || dir == "" {
			return p, parent == "."
		}
		return path.Base(p), parent == dir
	}
	add := func(p, w string) {
		if name, ok := child(p); ok && name != "" && !slices.Contains(who[name], w) {
			who[name] = append(who[name], w)
		}
	}
	if rows, err := s.db.Query(`SELECT path, target FROM user_shares WHERE owner = ? AND via = 0`, owner); err == nil {
		var pairs [][2]string
		for rows.Next() {
			var p, t string
			if rows.Scan(&p, &t) == nil {
				pairs = append(pairs, [2]string{p, t})
			}
		}
		rows.Close()
		for _, x := range pairs {
			add(x[0], s.displayOf(x[1]))
		}
	}
	if rows, err := s.db.Query(`SELECT ref, grp FROM group_shares WHERE owner = ? AND kind = 'file'`, owner); err == nil {
		for rows.Next() {
			var p, g string
			if rows.Scan(&p, &g) == nil {
				add(p, "group "+g)
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT path FROM shares WHERE user = ? AND (expires = 0 OR expires > ?)`, owner, time.Now().Unix()); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				add(p, "link")
			}
		}
		rows.Close()
	}
	out := map[string]string{}
	for k, v := range who {
		out[k] = strings.Join(v, ", ")
	}
	return out
}
