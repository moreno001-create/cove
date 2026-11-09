package app

import (
	"database/sql"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// Sharing with a Homeport group ("family"): everyone in it gets the share, including people who join later. A group
// share is kept as people shares marked with it (via), brought up to date when someone signs in.

const groupsSchema = `
CREATE TABLE IF NOT EXISTS user_groups (
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	grp TEXT NOT NULL,
	PRIMARY KEY (user, grp)
);
CREATE TABLE IF NOT EXISTS group_shares (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	kind TEXT NOT NULL, -- file or calendar
	ref TEXT NOT NULL,  -- the path, or the calendar's id
	grp TEXT NOT NULL,
	edit INTEGER NOT NULL,
	created INTEGER NOT NULL,
	UNIQUE (owner, kind, ref, grp)
);
CREATE TABLE IF NOT EXISTS comments (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	author TEXT NOT NULL,
	text TEXT NOT NULL,
	created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_path ON comments(owner, path);`

func migrateGroups(db *sql.DB) {
	db.Exec(`ALTER TABLE user_shares ADD COLUMN via INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE calendar_shares ADD COLUMN via INTEGER NOT NULL DEFAULT 0`)
}

// setGroups records which Homeport groups a user is in (as signing in reported) and updates group shares.
func (s *Server) setGroups(user string, groups []string) {
	s.db.Exec(`DELETE FROM user_groups WHERE user = ?`, user)
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			s.db.Exec(`INSERT OR IGNORE INTO user_groups (user, grp) VALUES (?, ?)`, user, g)
		}
	}
	s.syncGroupShares()
}

type groupShare struct {
	ID    int64  `json:"id"`
	Owner string `json:"owner"`
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Group string `json:"group"`
	Edit  bool   `json:"edit"`
}

// syncGroupShares gives each group's members its shares, and takes them from people no longer in it.
func (s *Server) syncGroupShares() {
	rows, err := s.db.Query(`SELECT id, owner, kind, ref, grp, edit FROM group_shares`)
	if err != nil {
		return
	}
	var all []groupShare
	for rows.Next() {
		var g groupShare
		if rows.Scan(&g.ID, &g.Owner, &g.Kind, &g.Ref, &g.Group, &g.Edit) == nil {
			all = append(all, g)
		}
	}
	rows.Close()
	for _, g := range all {
		members := map[string]bool{}
		if mr, err := s.db.Query(`SELECT user FROM user_groups WHERE grp = ? AND user != ?`, g.Group, g.Owner); err == nil {
			for mr.Next() {
				var m string
				if mr.Scan(&m) == nil {
					members[m] = true
				}
			}
			mr.Close()
		}
		table, refCol := "user_shares", "path"
		if g.Kind == "calendar" {
			table, refCol = "calendar_shares", "cal"
		}
		var current []string
		if cr, err := s.db.Query(`SELECT target FROM `+table+` WHERE via = ?`, g.ID); err == nil {
			for cr.Next() {
				var t string
				if cr.Scan(&t) == nil {
					current = append(current, t)
				}
			}
			cr.Close()
		}
		for _, t := range current {
			if !members[t] {
				s.db.Exec(`DELETE FROM `+table+` WHERE via = ? AND target = ?`, g.ID, t)
			}
		}
		for m := range members {
			res, err := s.db.Exec(`INSERT OR IGNORE INTO `+table+` (owner, `+refCol+`, target, edit, created, via) VALUES (?, ?, ?, ?, ?, ?)`,
				g.Owner, g.Ref, m, g.Edit, time.Now().Unix(), g.ID)
			if n, _ := res.RowsAffected(); err == nil && n == 1 {
				what, link := "“"+path.Base(g.Ref)+"”", "#/shared"
				if g.Kind == "calendar" {
					var name string
					s.db.QueryRow(`SELECT name FROM calendars WHERE user = ? AND id = ?`, g.Owner, g.Ref).Scan(&name)
					what, link = "the calendar “"+name+"”", "#/calendar"
				}
				s.notify(m, fmt.Sprintf("%s shared %s with %s (you're in it)", s.displayOf(g.Owner), what, g.Group), link)
			}
		}
	}
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT grp, user FROM user_groups ORDER BY grp, user`)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type group struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	list := []group{}
	for rows.Next() {
		var g, m string
		if rows.Scan(&g, &m) != nil {
			continue
		}
		if len(list) == 0 || list[len(list)-1].Name != g {
			list = append(list, group{Name: g})
		}
		list[len(list)-1].Members = append(list[len(list)-1].Members, m)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) listGroupShares(w http.ResponseWriter, r *http.Request, u user) {
	q := r.URL.Query()
	ref := q.Get("ref")
	if q.Get("kind") == "file" {
		ref = cleanRel(ref)
	}
	rows, err := s.db.Query(`SELECT id, owner, kind, ref, grp, edit FROM group_shares WHERE owner = ? AND kind = ? AND ref = ?`, u.Name, q.Get("kind"), ref)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []groupShare{}
	for rows.Next() {
		var g groupShare
		if rows.Scan(&g.ID, &g.Owner, &g.Kind, &g.Ref, &g.Group, &g.Edit) == nil {
			list = append(list, g)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createGroupShare(w http.ResponseWriter, r *http.Request, u user) {
	var in groupShare
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch in.Kind {
	case "file":
		in.Ref = cleanRel(in.Ref)
		if _, err := s.data.Stat(s.rel(u.Name, in.Ref)); err != nil || in.Ref == "." {
			jsonError(w, http.StatusNotFound, "That isn't there.")
			return
		}
	case "calendar":
		var x int
		if s.db.QueryRow(`SELECT 1 FROM calendars WHERE user = ? AND id = ?`, u.Name, in.Ref).Scan(&x) != nil {
			jsonError(w, http.StatusNotFound, "Only its owner can share it.")
			return
		}
	default:
		jsonError(w, http.StatusBadRequest, "kind is file or calendar")
		return
	}
	if _, err := s.db.Exec(`INSERT INTO group_shares (owner, kind, ref, grp, edit, created) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, kind, ref, grp) DO UPDATE SET edit = excluded.edit`, u.Name, in.Kind, in.Ref, in.Group, in.Edit, time.Now().Unix()); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var id int64
	s.db.QueryRow(`SELECT id FROM group_shares WHERE owner = ? AND kind = ? AND ref = ? AND grp = ?`, u.Name, in.Kind, in.Ref, in.Group).Scan(&id)
	table := map[string]string{"file": "user_shares", "calendar": "calendar_shares"}[in.Kind]
	s.db.Exec(`UPDATE `+table+` SET edit = ? WHERE via = ?`, in.Edit, id)
	s.syncGroupShares()
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) deleteGroupShare(w http.ResponseWriter, r *http.Request, u user) {
	var kind string
	id := r.PathValue("id")
	if s.db.QueryRow(`SELECT kind FROM group_shares WHERE id = ? AND owner = ?`, id, u.Name).Scan(&kind) != nil {
		jsonError(w, http.StatusNotFound, "That isn't shared any more.")
		return
	}
	table := map[string]string{"file": "user_shares", "calendar": "calendar_shares"}[kind]
	s.db.Exec(`DELETE FROM `+table+` WHERE via = ?`, id)
	s.db.Exec(`DELETE FROM group_shares WHERE id = ?`, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- comments on files and folders; @name tells that person

type comment struct {
	ID      int64  `json:"id"`
	Author  string `json:"author"`
	Who     string `json:"who"`
	Text    string `json:"text"`
	Created int64  `json:"created"`
	Mine    bool   `json:"mine"`
}

var mention = regexp.MustCompile(`@([A-Za-z0-9][A-Za-z0-9._@-]*[A-Za-z0-9])`)

// commentAccess is where a comment request is; anyone who can see the file may comment, even on a view-only share.
func (s *Server) commentAccess(w http.ResponseWriter, r *http.Request, u user, p string) (access, string, bool) {
	a, err := s.accessFor(u, r)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return a, "", false
	}
	full := a.path(p)
	if _, err := s.data.Stat(s.rel(a.owner, full)); err != nil {
		jsonError(w, http.StatusNotFound, "That isn't there any more.")
		return a, "", false
	}
	return a, full, true
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request, u user) {
	a, p, ok := s.commentAccess(w, r, u, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	rows, err := s.db.Query(`SELECT c.id, c.author, coalesce(nullif(x.display, ''), c.author), c.text, c.created FROM comments c
		LEFT JOIN users x ON x.name = c.author WHERE c.owner = ? AND c.path = ? ORDER BY c.created, c.id`, a.owner, p)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []comment{}
	for rows.Next() {
		var c comment
		if rows.Scan(&c.ID, &c.Author, &c.Who, &c.Text, &c.Created) == nil {
			c.Mine = c.Author == u.Name
			list = append(list, c)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 5000 {
		jsonError(w, http.StatusBadRequest, "Write something (up to 5000 characters).")
		return
	}
	a, p, ok := s.commentAccess(w, r, u, in.Path)
	if !ok {
		return
	}
	// who to tell: the owner, everyone who commented before, and anyone mentioned
	tell := map[string]bool{a.owner: true}
	if rows, err := s.db.Query(`SELECT DISTINCT author FROM comments WHERE owner = ? AND path = ?`, a.owner, p); err == nil {
		for rows.Next() {
			var x string
			if rows.Scan(&x) == nil {
				tell[x] = true
			}
		}
		rows.Close()
	}
	mentioned := map[string]bool{}
	for _, m := range mention.FindAllStringSubmatch(in.Text, -1) {
		var x int
		if s.db.QueryRow(`SELECT 1 FROM users WHERE name = ?`, m[1]).Scan(&x) == nil {
			mentioned[m[1]] = true
		}
	}
	if _, err := s.db.Exec(`INSERT INTO comments (owner, path, author, text, created) VALUES (?, ?, ?, ?, ?)`, a.owner, p, u.Name, in.Text, time.Now().Unix()); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := path.Base(p)
	short := in.Text
	if len(short) > 80 {
		short = short[:80] + "…"
	}
	for t := range mentioned {
		if t != u.Name {
			s.notify(t, fmt.Sprintf("%s mentioned you on “%s”: %s", displayName(u), name, short), "#/home")
			delete(tell, t)
		}
	}
	for t := range tell {
		if t != u.Name {
			s.notify(t, fmt.Sprintf("%s commented on “%s”: %s", displayName(u), name, short), "#/home")
		}
	}
	s.changed("files", s.fileWatchers(a.owner)...)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request, u user) {
	res, _ := s.db.Exec(`DELETE FROM comments WHERE id = ? AND (author = ? OR owner = ?)`, r.PathValue("id"), u.Name, u.Name)
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "That comment isn't yours to delete.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
