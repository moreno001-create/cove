package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
)

// Photos found by what's in them (the home AI's vision model writes a short description of each), kids' savings
// goals, carpools and rides, and posting to the family chat from Ask Cove ("on my way home").

const batch5Schema = `
CREATE VIRTUAL TABLE IF NOT EXISTS photo_text USING fts5(owner UNINDEXED, path UNINDEXED, body, tokenize='unicode61 remove_diacritics 2');
CREATE TABLE IF NOT EXISTS photo_described (
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	mod INTEGER NOT NULL,
	PRIMARY KEY (owner, path)
);`

// --- describing photos

var describable = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// describe asks the vision model what's in a picture, in a sentence and some keywords.
func (s *Server) describe(ctx context.Context, c aiConfig, jpg []byte) (string, error) {
	res, err := s.aiPost(ctx, c, "/chat/completions", map[string]any{"model": c.Vision, "temperature": 0.2, "stream": false, "max_tokens": 200,
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "Describe this family photo for search, in one plain sentence, then a line of keywords: people (no names), " +
				"place, things, animals, activity, season, any text you can read. No guessing about who anyone is."},
			{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg)}},
		}}}})
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("the vision model didn't answer")
	}
	text := out.Choices[0].Message.Content
	if _, after, ok := strings.Cut(text, "</think>"); ok {
		text = after
	}
	return clip(strings.TrimSpace(text), 1000), nil
}

// describePending describes a user's photos that haven't been, newest first, for at most budget.
func (s *Server) describePending(owner string, budget time.Duration) int {
	c := s.aiConfig()
	if c.URL == "" || c.Vision == "" {
		return 0
	}
	start := time.Now()
	done := map[string]int64{}
	if rows, err := s.db.Query(`SELECT path, mod FROM photo_described WHERE owner = ?`, owner); err == nil {
		for rows.Next() {
			var p string
			var m int64
			if rows.Scan(&p, &m) == nil {
				done[p] = m
			}
		}
		rows.Close()
	}
	n := 0
	for _, ph := range s.photosOf(owner) {
		if time.Since(start) > budget {
			break
		}
		if ph.Kind != "image" || !describable[strings.ToLower(path.Ext(ph.Path))] {
			continue
		}
		info, err := s.data.Stat(s.rel(owner, ph.Path))
		if err != nil || done[ph.Path] == info.ModTime().UnixNano() {
			continue
		}
		thumbSlots <- struct{}{}
		jpg, err := s.makeThumb(s.rel(owner, ph.Path), 640)
		<-thumbSlots
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		text, err := s.describe(ctx, c, jpg)
		cancel()
		if err != nil {
			log.Printf("describing %s's %s: %v", owner, ph.Path, err)
			return n // the model isn't there: try again next time
		}
		s.db.Exec(`DELETE FROM photo_text WHERE owner = ? AND path = ?`, owner, ph.Path)
		s.db.Exec(`INSERT INTO photo_text (owner, path, body) VALUES (?, ?, ?)`, owner, ph.Path, text)
		s.db.Exec(`INSERT INTO photo_described (owner, path, mod) VALUES (?, ?, ?) ON CONFLICT(owner, path) DO UPDATE SET mod = excluded.mod`,
			owner, ph.Path, info.ModTime().UnixNano())
		n++
	}
	return n
}

// photoCaptions are the descriptions of u's photos, by path.
func (s *Server) photoCaptions(owner string) map[string]string {
	out := map[string]string{}
	if rows, err := s.db.Query(`SELECT path, body FROM photo_text WHERE owner = ?`, owner); err == nil {
		for rows.Next() {
			var p, b string
			if rows.Scan(&p, &b) == nil {
				out[p] = b
			}
		}
		rows.Close()
	}
	return out
}

// searchPhotos finds u's photos whose description has all the words (each word as a prefix).
func (s *Server) searchPhotos(owner, q string) map[string]bool {
	out := map[string]bool{}
	fq := ftsQuery(q)
	if fq == "" {
		return out
	}
	if rows, err := s.db.Query(`SELECT path FROM photo_text WHERE photo_text MATCH ? AND owner = ? ORDER BY rank LIMIT 500`, fq, owner); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				out[p] = true
			}
		}
		rows.Close()
	}
	return out
}

// --- savings goals

type goal struct {
	Kid    string `json:"kid"`
	Title  string `json:"title"`
	Emoji  string `json:"emoji"`
	Target int    `json:"target"` // cents
	Saved  int    `json:"saved"`  // cents
}

func (g *goal) clean() error {
	g.Kid, g.Title, g.Emoji = clip(g.Kid, 64), clip(g.Title, 120), clip(g.Emoji, 16)
	if g.Title == "" || g.Target <= 0 {
		return errors.New("What's it for, and how much does it cost?")
	}
	g.Saved = max(g.Saved, 0)
	return nil
}
func (g *goal) label() string { return g.Title }

// goalMerge: kids set up their own goals, but only grown-ups put money in.
func goalMerge(u user, old, next []byte) []byte {
	if !u.Kid {
		return next
	}
	var o, n goal
	json.Unmarshal(old, &o)
	json.Unmarshal(next, &n)
	n.Saved = o.Saved
	b, _ := json.Marshal(n)
	return b
}

// toGoal puts money toward a goal (from paying allowance, or a gift).
func (s *Server) toGoal(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid {
		jsonError(w, http.StatusForbidden, "A grown-up adds the money.")
		return
	}
	var in struct {
		Cents int `json:"cents"`
	}
	if err := readJSON(r, &in); err != nil || in.Cents == 0 {
		jsonError(w, http.StatusBadRequest, "How much?")
		return
	}
	row, ok := s.homeRow(u, "goal", r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "That goal is gone.")
		return
	}
	var g goal
	json.Unmarshal(row.Data, &g)
	before := g.Saved
	g.Saved = max(0, g.Saved+in.Cents)
	b, _ := json.Marshal(g)
	s.db.Exec(`UPDATE hh SET data = ?, updated = ? WHERE id = ?`, b, time.Now().Unix(), row.ID)
	if g.Kid != "" && g.Kid != u.Name && in.Cents > 0 {
		msg := fmt.Sprintf("%s added %s to your %s goal", displayName(u), money(in.Cents), g.Title)
		if before < g.Target && g.Saved >= g.Target {
			msg = fmt.Sprintf("🎉 You reached your goal: %s!", g.Title)
		}
		s.notify(g.Kid, msg, "#/chores")
	}
	s.hhChanged(u, row)
	writeJSON(w, http.StatusOK, map[string]int{"saved": g.Saved})
}

// --- carpools and rides

type ride struct {
	Date   string   `json:"date"`
	Time   string   `json:"time"`
	What   string   `json:"what"` // soccer practice
	Driver string   `json:"driver"`
	Riders []string `json:"riders"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Back   string   `json:"back"` // who drives home ("" the same, "none")
	Notes  string   `json:"notes"`
}

func (x *ride) clean() error {
	x.What, x.Driver, x.From, x.To, x.Back, x.Notes, x.Riders = clip(x.What, 200), clip(x.Driver, 100), clip(x.From, 200), clip(x.To, 200), clip(x.Back, 100), clip(x.Notes, 2000), trimList(x.Riders, 20)
	if x.What == "" || !validDate(x.Date) {
		return errors.New("What, and which day?")
	}
	if x.Time != "" && !clockTime.MatchString(x.Time) {
		x.Time = ""
	}
	return nil
}
func (x *ride) label() string { return x.What }

// rideReminders: the evening before (7 PM) and an hour before, to the driver (a household member).
func (s *Server) rideReminders(from, to time.Time) {
	users := map[string]bool{}
	for _, n := range s.everyone() {
		users[n] = true
	}
	for _, x := range hhItems[ride](s, user{}, "ride") {
		for _, who := range []string{x.V.Driver, x.V.Back} {
			if !users[who] {
				continue
			}
			loc := s.location(who)
			d, err := time.ParseInLocation("2006-01-02", x.V.Date, loc)
			if err != nil {
				continue
			}
			at := []time.Time{d.Add(-5 * time.Hour)} // 7 PM the day before
			if x.V.Time != "" {
				t, _ := time.ParseInLocation("2006-01-02 15:04", x.V.Date+" "+x.V.Time, loc)
				at = append(at, t.Add(-time.Hour))
			}
			for i, a := range at {
				if a.After(from) && !a.After(to) && s.once(fmt.Sprintf("ride|%d|%s|%d", x.ID, who, i), 2*24*time.Hour) {
					riders := strings.Join(x.V.Riders, ", ")
					when := "tomorrow"
					if i == 1 {
						when = "in an hour"
					}
					role := "drive"
					if who == x.V.Back && who != x.V.Driver {
						role = "drive home from"
					}
					s.notify(who, fmt.Sprintf("You %s %s %s%s", role, x.V.What, when, map[bool]string{true: " (" + riders + ")", false: ""}[riders != ""]), "#/rides")
				}
			}
		}
	}
}

// --- posting to the family chat (Ask Cove's tool: "on my way home")

func (s *Server) postToFamily(u user, text string) error {
	rooms := s.chatRooms(u)
	i := slices.IndexFunc(rooms, func(r chatRoom) bool { return len(r.Members) == 0 })
	if i < 0 {
		return errors.New("there's no family room")
	}
	b, _ := json.Marshal(map[string]string{"text": text})
	req, _ := http.NewRequest("POST", "/api/chat", strings.NewReader(string(b)))
	req.SetPathValue("id", fmt.Sprint(rooms[i].ID))
	rec := &statusRecorder{header: http.Header{}}
	s.postChat(rec, req, u)
	if rec.code >= 300 {
		return errors.New("couldn't post to the chat")
	}
	return nil
}

type statusRecorder struct {
	header http.Header
	code   int
}

func (r *statusRecorder) Header() http.Header         { return r.header }
func (r *statusRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *statusRecorder) WriteHeader(c int)           { r.code = c }
