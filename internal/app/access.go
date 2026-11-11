package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Files can be shared with other people in the household: a folder or file of one user's appears under "Shared with
// me" for another, to view or to change. Every file request says where it is with ?share=<id> (none: the user's own
// folder), and access works out whose folder that is, where in it, and whether they may change things.

const accessSchema = `
CREATE TABLE IF NOT EXISTS user_shares (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	path TEXT NOT NULL,
	target TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	edit INTEGER NOT NULL,
	created INTEGER NOT NULL,
	UNIQUE (owner, path, target)
);
CREATE TABLE IF NOT EXISTS recent (
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	share INTEGER NOT NULL,
	at INTEGER NOT NULL,
	PRIMARY KEY (user, owner, path)
);`

type access struct {
	owner string // whose folder
	base  string // the shared file or folder in it ("." for the user's own folder)
	file  bool   // a single shared file
	edit  bool
	share int64 // 0 for the user's own folder
}

var errReadOnly = errors.New("this is shared with you to view, not to change")

// path is where p (a path the app sends, "/" being the top of what it sees) is in the owner's folder.
func (a access) path(p string) string {
	if a.file {
		return a.base
	}
	r := cleanRel(p)
	if a.base == "." {
		return r
	}
	if r == "." {
		return a.base
	}
	return a.base + "/" + r
}

// inside says whether p is inside what the request reaches, not its top: something that can be made, moved or deleted.
func (a access) inside(p string) bool { return !a.file && p != a.base && p != "." }

// shown is the reverse: what the app sees for a path in the owner's folder.
func (a access) shown(p string) string {
	if a.base == "." {
		return "/" + strings.TrimPrefix(p, ".")
	}
	if a.file || p == a.base {
		return "/"
	}
	return "/" + strings.TrimPrefix(p, a.base+"/")
}

func (s *Server) accessFor(u user, r *http.Request) (access, error) {
	raw := r.URL.Query().Get("share")
	if raw == "" || raw == "0" {
		return access{owner: u.Name, base: ".", edit: true}, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return access{}, errors.New("share isn't a number")
	}
	a := access{share: id}
	if err := s.db.QueryRow(`SELECT owner, path, edit FROM user_shares WHERE id = ? AND target = ?`, id, u.Name).
		Scan(&a.owner, &a.base, &a.edit); err != nil {
		return a, errors.New("that isn't shared with you any more")
	}
	info, err := s.data.Stat(s.rel(a.owner, a.base))
	if err != nil {
		return a, errors.New("what was shared with you isn't there any more")
	}
	a.file = !info.IsDir()
	return a, nil
}

// files wraps a file API handler with where the request's files are.
func (s *Server) files(h func(http.ResponseWriter, *http.Request, user, access)) func(http.ResponseWriter, *http.Request, user) {
	return func(w http.ResponseWriter, r *http.Request, u user) {
		a, err := s.accessFor(u, r)
		if err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !a.edit {
			jsonError(w, http.StatusForbidden, errReadOnly.Error())
			return
		}
		h(w, r, u, a)
	}
}

// --- sharing with people

type person struct {
	Name    string `json:"name"`
	Display string `json:"display"`
}

// listUsers is everyone who has signed in to Cove (the household), to share with.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, u user) {
	q, args := `SELECT name, display FROM users WHERE name != ? ORDER BY lower(coalesce(nullif(display, ''), name))`, []any{u.Name}
	if r.URL.Query().Get("household") != "" { // people in the household being looked at (for chores, trips, rides…)
		q, args = `SELECT u.name, u.display FROM users u JOIN household_members m ON m.user = u.name WHERE m.household = ? AND u.name != ?
			ORDER BY lower(coalesce(nullif(u.display, ''), u.name))`, []any{u.Home, u.Name}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []person{}
	for rows.Next() {
		var p person
		if rows.Scan(&p.Name, &p.Display) == nil {
			list = append(list, p)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

type userShare struct {
	ID      int64  `json:"id"`
	Owner   string `json:"owner"`
	Who     string `json:"who"` // the other person's name to show
	User    string `json:"user"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	Dir     bool   `json:"dir"`
	Kind    string `json:"kind"`
	Edit    bool   `json:"edit"`
	Created int64  `json:"created"`
}

func (s *Server) listUserShares(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT s.id, s.target, coalesce(nullif(t.display, ''), t.name), s.path, s.edit, s.created
		FROM user_shares s JOIN users t ON t.name = s.target WHERE s.owner = ? AND s.path = ? ORDER BY s.created`,
		u.Name, cleanRel(r.URL.Query().Get("path")))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []userShare{}
	for rows.Next() {
		var sh userShare
		if rows.Scan(&sh.ID, &sh.User, &sh.Who, &sh.Path, &sh.Edit, &sh.Created) == nil {
			sh.Owner, sh.Path = u.Name, "/"+sh.Path
			list = append(list, sh)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createUserShare(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Path string `json:"path"`
		User string `json:"user"`
		Edit bool   `json:"edit"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := cleanRel(in.Path)
	if p == "." || in.User == u.Name {
		jsonError(w, http.StatusBadRequest, "Share a file or folder with someone else.")
		return
	}
	if _, err := s.data.Stat(s.rel(u.Name, p)); err != nil {
		fileError(w, err)
		return
	}
	var exists int
	if s.db.QueryRow(`SELECT 1 FROM users WHERE name = ?`, in.User).Scan(&exists) != nil {
		jsonError(w, http.StatusBadRequest, "There's no such person in Cove (they need to have signed in once).")
		return
	}
	var had int
	s.db.QueryRow(`SELECT count(*) FROM user_shares WHERE owner = ? AND path = ? AND target = ?`, u.Name, p, in.User).Scan(&had)
	_, err := s.db.Exec(`INSERT INTO user_shares (owner, path, target, edit, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(owner, path, target) DO UPDATE SET edit = excluded.edit`, u.Name, p, in.User, in.Edit, time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if had == 0 {
		var id int64
		s.db.QueryRow(`SELECT id FROM user_shares WHERE owner = ? AND path = ? AND target = ?`, u.Name, p, in.User).Scan(&id)
		s.notify(in.User, fmt.Sprintf("%s shared “%s” with you", displayName(u), path.Base(p)), fmt.Sprintf("#/shared/%d/", id))
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) deleteUserShare(w http.ResponseWriter, r *http.Request, u user) {
	// the owner stops sharing, or the person removes it from their list
	res, err := s.db.Exec(`DELETE FROM user_shares WHERE id = ? AND (owner = ? OR target = ?)`, r.PathValue("id"), u.Name, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "That isn't shared any more.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) sharedWithMe(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT s.id, s.owner, coalesce(nullif(o.display, ''), o.name), s.path, s.edit, s.created
		FROM user_shares s JOIN users o ON o.name = s.owner WHERE s.target = ? ORDER BY s.created DESC`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []userShare{}
	for rows.Next() {
		var sh userShare
		if rows.Scan(&sh.ID, &sh.Owner, &sh.Who, &sh.Path, &sh.Edit, &sh.Created) != nil {
			continue
		}
		info, err := s.data.Stat(s.rel(sh.Owner, sh.Path))
		if err != nil {
			continue
		}
		sh.User, sh.Name, sh.Dir, sh.Kind = u.Name, path.Base(sh.Path), info.IsDir(), kindOf(path.Base(sh.Path), info.IsDir())
		sh.Path = "/" + sh.Path
		list = append(list, sh)
	}
	writeJSON(w, http.StatusOK, list)
}

// followPath keeps people's shares, versions and recent files with a file or folder that moved (to == "": deleted).
func (s *Server) followPath(owner, from, to string) {
	n := len(from) + 1
	if to == "" {
		s.db.Exec(`DELETE FROM user_shares WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`, owner, from, n, from+"/")
		s.db.Exec(`DELETE FROM recent WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`, owner, from, n, from+"/")
		s.db.Exec(`DELETE FROM favorites WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`, owner, from, n, from+"/")
		s.db.Exec(`DELETE FROM group_shares WHERE owner = ? AND kind = 'file' AND (ref = ? OR substr(ref, 1, ?) = ?)`, owner, from, n, from+"/")
		s.db.Exec(`DELETE FROM pins WHERE user = ? AND (path = ? OR substr(path, 1, ?) = ?)`, owner, from, n, from+"/")
		return
	}
	s.db.Exec(`UPDATE OR REPLACE user_shares SET path = ? || substr(path, ?) WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, n, owner, from, n, from+"/")
	s.db.Exec(`UPDATE OR REPLACE recent SET path = ? || substr(path, ?) WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, n, owner, from, n, from+"/")
	s.db.Exec(`UPDATE OR REPLACE favorites SET path = ? || substr(path, ?) WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, n, owner, from, n, from+"/")
	s.db.Exec(`UPDATE OR REPLACE group_shares SET ref = ? || substr(ref, ?) WHERE owner = ? AND kind = 'file' AND (ref = ? OR substr(ref, 1, ?) = ?)`,
		to, n, owner, from, n, from+"/")
	s.db.Exec(`UPDATE comments SET path = ? || substr(path, ?) WHERE owner = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, n, owner, from, n, from+"/")
	s.db.Exec(`UPDATE OR REPLACE pins SET path = ? || substr(path, ?) WHERE user = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, n, owner, from, n, from+"/")
	if info, err := s.data.Stat(s.rel(owner, to)); err == nil && !info.IsDir() {
		s.data.MkdirAll("versions/"+owner, 0o750)
		s.data.Rename(s.versionDir(owner, from), s.versionDir(owner, to))
	}
}

// --- versions: replacing a file keeps the old one (at most one every 10 minutes while a document is being edited,
// the newest 30, files under 512 MB).

func (s *Server) versionDir(owner, p string) string {
	return "versions/" + owner + "/" + fileID(owner, p)
}

// keepVersion moves the file at p aside as a version before it's replaced (force: even if a version is a few minutes
// old). It reports false if it left the file in place.
func (s *Server) keepVersion(owner, p string, force bool) bool {
	name := s.rel(owner, p)
	info, err := s.data.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 || info.Size() == 0 {
		return false
	}
	dir := s.versionDir(owner, p)
	list := s.versions(owner, p)
	if !force && len(list) > 0 && time.Since(time.Unix(0, list[0].ID)) < 10*time.Minute {
		return false // a version from a few minutes ago is enough
	}
	if s.data.MkdirAll(dir, 0o750) != nil {
		return false
	}
	if s.data.Rename(name, dir+"/"+strconv.FormatInt(info.ModTime().UnixNano(), 10)) != nil {
		return false
	}
	for _, old := range list[min(len(list), 29):] {
		s.data.Remove(dir + "/" + strconv.FormatInt(old.ID, 10))
	}
	return true
}

type version struct {
	ID   int64 `json:"id,string"` // when it was saved, in nanoseconds (a string: too big for JavaScript's numbers)
	Size int64 `json:"size"`
	Mod  int64 `json:"mod"` // milliseconds
}

func (s *Server) versions(owner, p string) []version {
	f, err := s.data.Open(s.versionDir(owner, p))
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(-1)
	var list []version
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue
		}
		if info, err := e.Info(); err == nil {
			list = append(list, version{ID: id, Size: info.Size(), Mod: id / 1e6})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return list
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request, u user, a access) {
	list := s.versions(a.owner, a.path(r.URL.Query().Get("path")))
	if list == nil {
		list = []version{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) versionContent(w http.ResponseWriter, r *http.Request, u user, a access) {
	p := a.path(r.URL.Query().Get("path"))
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "id is a version's number")
		return
	}
	f, err := s.data.Open(s.versionDir(a.owner, p) + "/" + strconv.FormatInt(id, 10))
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	stamp := time.Unix(0, id).Format("2006-01-02 15.04")
	ext := path.Ext(p)
	name := strings.TrimSuffix(path.Base(p), ext) + " (" + stamp + ")" + ext
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	io.Copy(w, f)
}

func (s *Server) restoreVersion(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path string `json:"path"`
		ID   int64  `json:"id,string"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.path(in.Path)
	src := s.versionDir(a.owner, p) + "/" + strconv.FormatInt(in.ID, 10)
	f, err := s.data.Open(src)
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	s.keepVersion(a.owner, p, true) // what's there now becomes a version too
	_, err = s.writeFile(a.owner, p, f, true, time.Time{})
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- recent files

func (s *Server) touchRecent(u user, a access, p string) {
	s.db.Exec(`INSERT INTO recent (user, owner, path, share, at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user, owner, path) DO UPDATE SET share = excluded.share, at = excluded.at`, u.Name, a.owner, p, a.share, time.Now().Unix())
	s.db.Exec(`DELETE FROM recent WHERE user = ? AND at < (SELECT at FROM recent WHERE user = ? ORDER BY at DESC LIMIT 1 OFFSET 60)`, u.Name, u.Name)
}

type recentEntry struct {
	fileItem
	Path   string `json:"path"`  // as the app reaches it (inside the share, if it's someone else's)
	Share  int64  `json:"share"` // 0: the user's own
	Owner  string `json:"owner"`
	Opened int64  `json:"opened"`
}

func (s *Server) listRecent(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, s.recentFor(u, 60))
}

func (s *Server) recentFor(u user, limit int) []recentEntry {
	list := []recentEntry{}
	rows, err := s.db.Query(`SELECT owner, path, share, at FROM recent WHERE user = ? ORDER BY at DESC LIMIT 60`, u.Name)
	if err != nil {
		return list
	}
	type rec struct {
		owner, p string
		share    int64
		at       int64
	}
	var recs []rec
	for rows.Next() {
		var x rec
		if rows.Scan(&x.owner, &x.p, &x.share, &x.at) == nil {
			recs = append(recs, x)
		}
	}
	rows.Close()
	for _, x := range recs {
		if len(list) == limit {
			break
		}
		a := access{owner: u.Name, base: ".", edit: true}
		if x.share != 0 {
			fake, _ := http.NewRequest("GET", "/?share="+strconv.FormatInt(x.share, 10), nil)
			var err error
			if a, err = s.accessFor(u, fake); err != nil || a.owner != x.owner {
				continue
			}
			if !a.file && x.p != a.base && !strings.HasPrefix(x.p, a.base+"/") {
				continue
			}
		}
		info, err := s.data.Stat(s.rel(x.owner, x.p))
		if err != nil || info.IsDir() {
			continue
		}
		list = append(list, recentEntry{item(info), a.shown(x.p), x.share, x.owner, x.at * 1000})
	}
	return list
}
