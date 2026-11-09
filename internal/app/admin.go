package app

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Admins (Homeport's "admins" group) see how much space each person uses, can set limits, and can see and end every
// link and share.

// migrateUsers adds what older databases lack.
func migrateUsers(db *sql.DB) {
	db.Exec(`ALTER TABLE users ADD COLUMN admin INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN quota INTEGER NOT NULL DEFAULT 0`) // bytes, 0: no limit
	db.Exec(`ALTER TABLE users ADD COLUMN seen INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE shares ADD COLUMN downloads INTEGER NOT NULL DEFAULT 0`)
}

type usage struct {
	bytes int64
	files int
	at    time.Time
}

var usageCache = struct {
	sync.Mutex
	m map[string]usage
}{m: map[string]usage{}}

// usageOf is the space a user's files take (with their versions and trash), worked out at most every two minutes.
func (s *Server) usageOf(user string) usage {
	usageCache.Lock()
	u, ok := usageCache.m[user]
	usageCache.Unlock()
	if ok && time.Since(u.at) < 2*time.Minute {
		return u
	}
	u = usage{at: time.Now()}
	for _, dir := range []string{"files/", "versions/", "trash/"} {
		fs.WalkDir(s.data.FS(), dir+user, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !hidden(d.Name()) { // not uploads still arriving
				if info, err := d.Info(); err == nil {
					u.bytes += info.Size()
					if dir == "files/" {
						u.files++
					}
				}
			}
			return nil
		})
	}
	usageCache.Lock()
	usageCache.m[user] = u
	usageCache.Unlock()
	return u
}

func (s *Server) forgetUsage(user string) {
	usageCache.Lock()
	delete(usageCache.m, user)
	usageCache.Unlock()
}

// checkQuota says whether adding size bytes to owner's files stays within their limit.
func (s *Server) checkQuota(owner string, size int64) error {
	var quota int64
	s.db.QueryRow(`SELECT quota FROM users WHERE name = ?`, owner).Scan(&quota)
	if quota <= 0 {
		return nil
	}
	if used := s.usageOf(owner).bytes; used+size > quota {
		return fmt.Errorf("that would go over the storage limit (%s of %s used)", humanBytes(used), humanBytes(quota))
	}
	return nil
}

func humanBytes(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if f < 1024 {
			return fmt.Sprintf("%.3g %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.3g TB", f)
}

func (s *Server) admin(h func(http.ResponseWriter, *http.Request, user)) func(http.ResponseWriter, *http.Request, user) {
	return func(w http.ResponseWriter, r *http.Request, u user) {
		if !u.Admin {
			jsonError(w, http.StatusForbidden, "That's for admins.")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT name, display, email, admin, kid, quota, created, seen FROM users ORDER BY lower(coalesce(nullif(display, ''), name))`)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type entry struct {
		Name    string `json:"name"`
		Display string `json:"display"`
		Email   string `json:"email"`
		Admin   bool   `json:"admin"`
		Kid     bool   `json:"kid"`
		Quota   int64  `json:"quota"`
		Used    int64  `json:"used"`
		Files   int    `json:"files"`
		Created int64  `json:"created"`
		Seen    int64  `json:"seen"`
	}
	var list []entry
	for rows.Next() {
		var e entry
		if rows.Scan(&e.Name, &e.Display, &e.Email, &e.Admin, &e.Kid, &e.Quota, &e.Created, &e.Seen) == nil {
			list = append(list, e)
		}
	}
	rows.Close()
	for i := range list {
		us := s.usageOf(list[i].Name)
		list[i].Used, list[i].Files = us.bytes, us.files
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) adminSetQuota(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Quota int64 `json:"quota"` // bytes, 0: none
	}
	if err := readJSON(r, &in); err != nil || in.Quota < 0 {
		jsonError(w, http.StatusBadRequest, "quota is a number of bytes (0: no limit)")
		return
	}
	res, err := s.db.Exec(`UPDATE users SET quota = ? WHERE name = ?`, in.Quota, r.PathValue("name"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "There's no such person.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminSetKid makes an account a kid's (or not).
func (s *Server) adminSetKid(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Kid bool `json:"kid"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Kid && r.PathValue("name") == u.Name {
		jsonError(w, http.StatusBadRequest, "You can't make your own account a kid's.")
		return
	}
	res, _ := s.db.Exec(`UPDATE users SET kid = ? WHERE name = ?`, in.Kid, r.PathValue("name"))
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "There's no such person.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminShares lists every link, people share and shared calendar.
func (s *Server) adminShares(w http.ResponseWriter, r *http.Request, u user) {
	links := []share{}
	if rows, err := s.db.Query(`SELECT ` + shareColumns + ` FROM shares ORDER BY created DESC`); err == nil {
		for rows.Next() {
			if sh, err := s.scanShare(rows); err == nil {
				sh.Owner = sh.User
				links = append(links, sh)
			}
		}
		rows.Close()
	}
	type personShare struct {
		Kind  string `json:"kind"` // file or calendar
		ID    int64  `json:"id"`
		Owner string `json:"owner"`
		With  string `json:"with"`
		What  string `json:"what"`
		Edit  bool   `json:"edit"`
	}
	people := []personShare{}
	if rows, err := s.db.Query(`SELECT id, owner, target, path, edit FROM user_shares ORDER BY created DESC`); err == nil {
		for rows.Next() {
			var p personShare
			if rows.Scan(&p.ID, &p.Owner, &p.With, &p.What, &p.Edit) == nil {
				p.Kind, p.What = "file", "/"+p.What
				people = append(people, p)
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT s.id, s.owner, s.target, c.name, s.edit FROM calendar_shares s JOIN calendars c ON c.user = s.owner AND c.id = s.cal ORDER BY s.created DESC`); err == nil {
		for rows.Next() {
			var p personShare
			if rows.Scan(&p.ID, &p.Owner, &p.With, &p.What, &p.Edit) == nil {
				p.Kind = "calendar"
				people = append(people, p)
			}
		}
		rows.Close()
	}
	sort.SliceStable(people, func(i, j int) bool { return strings.ToLower(people[i].Owner) < strings.ToLower(people[j].Owner) })
	writeJSON(w, http.StatusOK, map[string]any{"links": links, "people": people})
}

func (s *Server) adminEndLink(w http.ResponseWriter, r *http.Request, u user) {
	s.db.Exec(`DELETE FROM shares WHERE token = ?`, r.PathValue("token"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminEndShare(w http.ResponseWriter, r *http.Request, u user) {
	table := "user_shares"
	if r.PathValue("kind") == "calendar" {
		table = "calendar_shares"
	}
	s.db.Exec(`DELETE FROM `+table+` WHERE id = ?`, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
