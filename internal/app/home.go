package app

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// The home page: what's coming up this week, tasks due, recent documents, what was shared lately, and starred
// files and folders.

const homeSchema = `
CREATE TABLE IF NOT EXISTS favorites (
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	share INTEGER NOT NULL,
	created INTEGER NOT NULL,
	PRIMARY KEY (user, owner, path)
);`

type favorite struct {
	fileItem
	Path  string `json:"path"`  // as the app reaches it
	Share int64  `json:"share"` // 0: the user's own
	Owner string `json:"owner"`
}

func (s *Server) favoritesOf(u user) []favorite {
	rows, err := s.db.Query(`SELECT owner, path, share FROM favorites WHERE user = ? ORDER BY created`, u.Name)
	if err != nil {
		return nil
	}
	type rec struct {
		owner, p string
		share    int64
	}
	var recs []rec
	for rows.Next() {
		var x rec
		if rows.Scan(&x.owner, &x.p, &x.share) == nil {
			recs = append(recs, x)
		}
	}
	rows.Close()
	out := []favorite{}
	for _, x := range recs {
		a := access{owner: u.Name, base: ".", edit: true}
		if x.share != 0 {
			fake, _ := http.NewRequest("GET", "/?share="+strconv.FormatInt(x.share, 10), nil)
			var err error
			if a, err = s.accessFor(u, fake); err != nil || a.owner != x.owner {
				continue
			}
		}
		info, err := s.data.Stat(s.rel(x.owner, x.p))
		if err != nil {
			continue
		}
		out = append(out, favorite{item(info), a.shown(x.p), x.share, x.owner})
	}
	return out
}

func (s *Server) listFavorites(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, s.favoritesOf(u))
}

func (s *Server) addFavorite(w http.ResponseWriter, r *http.Request, u user, a access) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.path(in.Path)
	if _, err := s.data.Stat(s.rel(a.owner, p)); err != nil {
		fileError(w, err)
		return
	}
	s.db.Exec(`INSERT OR IGNORE INTO favorites (user, owner, path, share, created) VALUES (?, ?, ?, ?, ?)`, u.Name, a.owner, p, a.share, time.Now().Unix())
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// removeFavorite works for reading too (view-only shares can be starred), so it isn't behind files' write check.
func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request, u user) {
	a, err := s.accessFor(u, r)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	s.db.Exec(`DELETE FROM favorites WHERE user = ? AND owner = ? AND path = ?`, u.Name, a.owner, a.path(r.URL.Query().Get("path")))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) starFavorite(w http.ResponseWriter, r *http.Request, u user) {
	a, err := s.accessFor(u, r)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	s.addFavorite(w, r, u, a)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, u user) {
	loc, err := time.LoadLocation(r.URL.Query().Get("tz"))
	if err != nil || r.URL.Query().Get("tz") == "" {
		loc = s.location(u.Name)
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	events := []occurrence{}
	for _, o := range s.eventsFor(u, loc, today, today.AddDate(0, 0, 8)) {
		if o.AllDay || occStart(o, loc).Add(time.Hour).After(now) || o.End > now.Format(time.RFC3339) {
			events = append(events, o)
		}
		if len(events) == 12 {
			break
		}
	}
	tasks := []task{}
	soon := today.AddDate(0, 0, 8).Format("2006-01-02")
	for _, t := range s.tasksFor(u.Name, loc, "") {
		if !t.Done && t.Due != "" && t.Due < soon && len(tasks) < 10 {
			tasks = append(tasks, t)
		}
	}
	shared := []userShare{}
	rows, err := s.db.Query(`SELECT s.id, s.owner, coalesce(nullif(o.display, ''), o.name), s.path, s.edit, s.created
		FROM user_shares s JOIN users o ON o.name = s.owner WHERE s.target = ? ORDER BY s.created DESC LIMIT 5`, u.Name)
	if err == nil {
		for rows.Next() {
			var sh userShare
			if rows.Scan(&sh.ID, &sh.Owner, &sh.Who, &sh.Path, &sh.Edit, &sh.Created) == nil {
				if info, err := s.data.Stat(s.rel(sh.Owner, sh.Path)); err == nil {
					sh.Name, sh.Dir, sh.Kind = path.Base(sh.Path), info.IsDir(), kindOf(path.Base(sh.Path), info.IsDir())
					sh.Path = "/" + sh.Path
					shared = append(shared, sh)
				}
			}
		}
		rows.Close()
	}
	var unread int
	s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = ? AND read = 0`, u.Name).Scan(&unread)
	writeJSON(w, http.StatusOK, map[string]any{
		"greeting":  greeting(now),
		"events":    events,
		"tasks":     tasks,
		"recent":    s.recentFor(u, 6),
		"shared":    shared,
		"favorites": s.favoritesOf(u),
		"fridge":    s.fridgeNotes(u, 6),
		"unread":    unread,
		"chat":      s.chatUnread(u),
	})
}

func greeting(t time.Time) string {
	switch h := t.Hour(); {
	case h < 5:
		return "Good night"
	case h < 12:
		return "Good morning"
	case h < 18:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}

var _ = strings.TrimSpace
