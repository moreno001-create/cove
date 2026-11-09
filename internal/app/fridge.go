package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The fridge: the household's notice board. Anyone puts up a note, a photo or a list; everyone sees it (and can tick
// a list); it can come down by itself after some days. Whoever put something up (or an admin) takes it down.

const fridgeSchema = `
CREATE TABLE IF NOT EXISTS fridge (
	id INTEGER PRIMARY KEY,
	author TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	kind TEXT NOT NULL, -- note, list or photo
	text TEXT NOT NULL,
	color TEXT NOT NULL,
	items TEXT NOT NULL DEFAULT '[]',
	photo TEXT NOT NULL DEFAULT '',
	pinned INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL,
	expires INTEGER NOT NULL DEFAULT 0
);`

var fridgeColors = map[string]bool{"yellow": true, "pink": true, "blue": true, "green": true, "orange": true, "purple": true, "white": true}

type fridgeItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
	By   string `json:"by,omitempty"` // who ticked it
	// Votes are who picked this answer, in a poll.
	Votes []string `json:"votes,omitempty"`
}

type fridgeNote struct {
	ID      int64        `json:"id"`
	Author  string       `json:"author"`
	Who     string       `json:"who"`
	Kind    string       `json:"kind"`
	Text    string       `json:"text"`
	Color   string       `json:"color"`
	Items   []fridgeItem `json:"items"`
	Date    string       `json:"date"` // a countdown's day
	Photo   bool         `json:"photo"`
	Pinned  bool         `json:"pinned"`
	Created int64        `json:"created"`
	Expires int64        `json:"expires"`
	Mine    bool         `json:"mine"`
}

func (s *Server) fridgeNotes(u user, limit int) []fridgeNote {
	rows, err := s.db.Query(`SELECT f.id, f.author, coalesce(nullif(x.display, ''), f.author), f.kind, f.text, f.color, f.items, f.date, f.photo != '', f.pinned, f.created, f.expires
		FROM fridge f LEFT JOIN users x ON x.name = f.author WHERE f.expires = 0 OR f.expires > ? ORDER BY f.pinned DESC, f.created DESC LIMIT ?`,
		time.Now().Unix(), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []fridgeNote{}
	for rows.Next() {
		var n fridgeNote
		var items string
		if rows.Scan(&n.ID, &n.Author, &n.Who, &n.Kind, &n.Text, &n.Color, &items, &n.Date, &n.Photo, &n.Pinned, &n.Created, &n.Expires) == nil {
			json.Unmarshal([]byte(items), &n.Items)
			if n.Items == nil {
				n.Items = []fridgeItem{}
			}
			n.Mine = n.Author == u.Name || u.Admin
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) listFridge(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, s.fridgeNotes(u, 200))
}

func everyoneBut(db *sql.DB, name string) []string {
	var out []string
	if rows, err := db.Query(`SELECT name FROM users WHERE name != ?`, name); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				out = append(out, n)
			}
		}
		rows.Close()
	}
	return out
}

// putUp tells the household something went on the fridge (at most every few minutes per person).
func (s *Server) putUp(u user, what, detail string) {
	others := everyoneBut(s.db, u.Name)
	if s.once("fridge|"+u.Name, 5*time.Minute) {
		for _, o := range others {
			s.notify(o, fmt.Sprintf("%s put %s on the fridge%s", displayName(u), what, detail), "#/fridge")
		}
	}
	s.changed("fridge", append(others, u.Name)...)
}

type fridgeInput struct {
	Kind        string       `json:"kind"`
	Text        string       `json:"text"`
	Color       string       `json:"color"`
	Items       []fridgeItem `json:"items"`
	Date        string       `json:"date"`
	Pinned      bool         `json:"pinned"`
	ExpiresDays int          `json:"expiresDays"`
}

func (in *fridgeInput) clean() {
	in.Text = strings.TrimSpace(in.Text)
	if len(in.Text) > 4000 {
		in.Text = in.Text[:4000]
	}
	if !fridgeColors[in.Color] {
		in.Color = "yellow"
	}
	items := in.Items[:0]
	for _, it := range in.Items {
		if it.Text = strings.TrimSpace(it.Text); it.Text != "" && len(items) < 100 {
			items = append(items, it)
		}
	}
	in.Items = items
}

func (s *Server) addFridge(w http.ResponseWriter, r *http.Request, u user) {
	var in fridgeInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.clean()
	switch in.Kind {
	case "list", "note":
	case "poll":
		if in.Text == "" || len(in.Items) < 2 {
			jsonError(w, http.StatusBadRequest, "A poll needs a question and at least two answers.")
			return
		}
		for i := range in.Items {
			in.Items[i].Done, in.Items[i].By, in.Items[i].Votes = false, "", nil
		}
	case "countdown":
		if in.Text == "" || !validDate(in.Date) {
			jsonError(w, http.StatusBadRequest, "A countdown needs a name and a day.")
			return
		}
		if in.ExpiresDays == 0 { // comes down the day after
			d, _ := time.ParseInLocation("2006-01-02", in.Date, s.location(u.Name))
			in.ExpiresDays = max(1, int(time.Until(d.AddDate(0, 0, 1)).Hours()/24)+1)
		}
	default:
		in.Kind = "note"
	}
	if in.Kind != "countdown" {
		in.Date = ""
	}
	if in.Text == "" && len(in.Items) == 0 {
		jsonError(w, http.StatusBadRequest, "Write something first.")
		return
	}
	items, _ := json.Marshal(in.Items)
	var expires int64
	if in.ExpiresDays > 0 {
		expires = time.Now().AddDate(0, 0, in.ExpiresDays).Unix()
	}
	res, err := s.db.Exec(`INSERT INTO fridge (author, kind, text, color, items, date, pinned, created, expires) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Name, in.Kind, in.Text, in.Color, string(items), in.Date, in.Pinned, time.Now().Unix(), expires)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	what := map[string]string{"note": "a note", "list": "a list", "poll": "a poll", "countdown": "a countdown"}[in.Kind]
	detail := ""
	if t := firstLine(in.Text); t != "" {
		detail = ": “" + t + "”"
	}
	s.putUp(u, what, detail)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}

// addFridgePhoto puts a photo up (the body is the picture; ?caption=, ?expiresDays=).
func (s *Server) addFridgePhoto(w http.ResponseWriter, r *http.Request, u user) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 30<<20))
	if err != nil || len(b) == 0 {
		jsonError(w, http.StatusBadRequest, "The photo didn't arrive.")
		return
	}
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}[http.DetectContentType(b)]
	if ext == "" {
		jsonError(w, http.StatusBadRequest, "That isn't a JPEG, PNG, GIF or WebP picture.")
		return
	}
	s.data.MkdirAll("fridge", 0o750)
	name := "fridge/" + randomToken(12) + ext
	if err := s.data.WriteFile(name, b, 0o640); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var expires int64
	var days int
	fmt.Sscan(r.URL.Query().Get("expiresDays"), &days)
	if days > 0 {
		expires = time.Now().AddDate(0, 0, days).Unix()
	}
	caption := strings.TrimSpace(r.URL.Query().Get("caption"))
	res, err := s.db.Exec(`INSERT INTO fridge (author, kind, text, color, photo, created, expires) VALUES (?, 'photo', ?, 'white', ?, ?, ?)`,
		u.Name, caption, name, time.Now().Unix(), expires)
	if err != nil {
		s.data.Remove(name)
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	detail := ""
	if caption != "" {
		detail = ": “" + firstLine(caption) + "”"
	}
	s.putUp(u, "a photo", detail)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) fridgePhoto(w http.ResponseWriter, r *http.Request, u user) {
	var name string
	if s.db.QueryRow(`SELECT photo FROM fridge WHERE id = ?`, r.PathValue("id")).Scan(&name) != nil || name == "" {
		http.NotFound(w, r)
		return
	}
	s.serveThumb(w, r, name)
}

// updateFridge changes a note: anyone can tick a list or add to it; the text, color, pin and end date are the
// author's (or an admin's).
func (s *Server) updateFridge(w http.ResponseWriter, r *http.Request, u user) {
	var in fridgeInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.clean()
	var author, text, color, kind, oldItems, date string
	var pinned bool
	var expires int64
	id := r.PathValue("id")
	if s.db.QueryRow(`SELECT author, text, color, pinned, expires, kind, items, date FROM fridge WHERE id = ?`, id).Scan(&author, &text, &color, &pinned, &expires, &kind, &oldItems, &date) != nil {
		jsonError(w, http.StatusNotFound, "That isn't on the fridge any more.")
		return
	}
	if kind == "poll" { // answers keep their votes (voting is its own call); only the author changes them
		var old []fridgeItem
		json.Unmarshal([]byte(oldItems), &old)
		votes := map[string][]string{}
		for _, it := range old {
			votes[it.Text] = it.Votes
		}
		if author != u.Name && !u.Admin {
			in.Items = old
		}
		for i := range in.Items {
			in.Items[i].Done, in.Items[i].By, in.Items[i].Votes = false, "", votes[in.Items[i].Text]
		}
	}
	for i := range in.Items {
		if in.Items[i].Done && in.Items[i].By == "" {
			in.Items[i].By = displayName(u)
		}
		if !in.Items[i].Done {
			in.Items[i].By = ""
		}
	}
	items, _ := json.Marshal(in.Items)
	if author == u.Name || u.Admin {
		text, color, pinned = in.Text, in.Color, in.Pinned
		if kind == "countdown" && validDate(in.Date) {
			date = in.Date
		}
		if in.ExpiresDays > 0 {
			expires = time.Now().AddDate(0, 0, in.ExpiresDays).Unix()
		} else if in.ExpiresDays < 0 {
			expires = 0
		}
	}
	s.db.Exec(`UPDATE fridge SET text = ?, color = ?, items = ?, date = ?, pinned = ?, expires = ? WHERE id = ?`, text, color, string(items), date, pinned, expires, id)
	s.changed("fridge", append(everyoneBut(s.db, u.Name), u.Name)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// voteFridge picks an answer in a poll (one each; picking it again takes the vote back).
func (s *Server) voteFridge(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Option int `json:"option"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var items, kind string
	if s.db.QueryRow(`SELECT items, kind FROM fridge WHERE id = ?`, r.PathValue("id")).Scan(&items, &kind) != nil || kind != "poll" {
		jsonError(w, http.StatusNotFound, "That poll isn't on the fridge any more.")
		return
	}
	var list []fridgeItem
	json.Unmarshal([]byte(items), &list)
	if in.Option < 0 || in.Option >= len(list) {
		jsonError(w, http.StatusBadRequest, "There's no such answer.")
		return
	}
	had := slices.Contains(list[in.Option].Votes, u.Name)
	for i := range list {
		list[i].Votes = slices.DeleteFunc(list[i].Votes, func(v string) bool { return v == u.Name })
	}
	if !had {
		list[in.Option].Votes = append(list[in.Option].Votes, u.Name)
	}
	b, _ := json.Marshal(list)
	s.db.Exec(`UPDATE fridge SET items = ? WHERE id = ?`, string(b), r.PathValue("id"))
	s.changed("fridge", s.everyone()...)
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) removeFridge(w http.ResponseWriter, r *http.Request, u user) {
	var author, photo string
	if s.db.QueryRow(`SELECT author, photo FROM fridge WHERE id = ?`, r.PathValue("id")).Scan(&author, &photo) != nil {
		jsonError(w, http.StatusNotFound, "That isn't on the fridge any more.")
		return
	}
	if author != u.Name && !u.Admin {
		jsonError(w, http.StatusForbidden, "Only whoever put it up (or an admin) can take it down.")
		return
	}
	s.db.Exec(`DELETE FROM fridge WHERE id = ?`, r.PathValue("id"))
	if photo != "" {
		s.data.Remove(photo)
	}
	s.changed("fridge", append(everyoneBut(s.db, u.Name), u.Name)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// dropExpiredFridge takes down what has passed its end date.
func (s *Server) dropExpiredFridge(now time.Time) {
	rows, err := s.db.Query(`SELECT id, photo FROM fridge WHERE expires > 0 AND expires < ?`, now.Unix())
	if err != nil {
		return
	}
	type gone struct {
		id    int64
		photo string
	}
	var list []gone
	for rows.Next() {
		var g gone
		if rows.Scan(&g.id, &g.photo) == nil {
			list = append(list, g)
		}
	}
	rows.Close()
	for _, g := range list {
		s.db.Exec(`DELETE FROM fridge WHERE id = ?`, g.id)
		if g.photo != "" {
			s.data.Remove(g.photo)
		}
	}
}
