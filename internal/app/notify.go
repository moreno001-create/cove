package app

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Notifications: what others did with things you share (a share with you, an event added to the family calendar,
// your document edited, files dropped into your link), invitations, and reminders for events and tasks. They show
// in Cove (and as browser notifications while it's open), and on phones through ntfy when a topic is set.

const notifySchema = `
CREATE TABLE IF NOT EXISTS notifications (
	id INTEGER PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	text TEXT NOT NULL,
	link TEXT NOT NULL,
	created INTEGER NOT NULL,
	read INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS notifications_user ON notifications(user, created);
CREATE TABLE IF NOT EXISTS user_settings (
	user TEXT PRIMARY KEY REFERENCES users(name) ON DELETE CASCADE,
	ntfy TEXT NOT NULL DEFAULT '',
	tz TEXT NOT NULL DEFAULT ''
);
-- What was sent already: reminders, and "edited" notices sent at most every half hour.
CREATE TABLE IF NOT EXISTS sent (
	key TEXT PRIMARY KEY,
	at INTEGER NOT NULL
);`

type userSettings struct {
	Ntfy string `json:"ntfy"` // an ntfy topic's address, https://ntfy.example.com/my-topic
	TZ   string `json:"tz"`
}

func (s *Server) settingsOf(user string) userSettings {
	var st userSettings
	s.db.QueryRow(`SELECT ntfy, tz FROM user_settings WHERE user = ?`, user).Scan(&st.Ntfy, &st.TZ)
	return st
}

func (s *Server) location(user string) *time.Location {
	if loc, err := time.LoadLocation(s.settingsOf(user).TZ); err == nil && s.settingsOf(user).TZ != "" {
		return loc
	}
	return time.UTC
}

// notify tells a user something, in Cove and on their phone.
func (s *Server) notify(user, text, link string) {
	if _, err := s.db.Exec(`INSERT INTO notifications (user, text, link, created) VALUES (?, ?, ?, ?)`, user, text, link, time.Now().Unix()); err != nil {
		log.Printf("notifying %s: %v", user, err)
		return
	}
	if topic := s.settingsOf(user).Ntfy; topic != "" {
		go s.pushNtfy(topic, text, link)
	}
	go s.pushTo(user, text, link)
	s.changed("notifications", user)
}

func (s *Server) pushNtfy(topic, text, link string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, topic, strings.NewReader(text))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "Cove")
	req.Header.Set("Tags", "ocean")
	if link != "" {
		req.Header.Set("Click", s.cfg.URL+"/"+link)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("ntfy answered %d", res.StatusCode)
	}
	return nil
}

// once reports whether key wasn't sent within the last d, and marks it sent.
func (s *Server) once(key string, d time.Duration) bool {
	var at int64
	if s.db.QueryRow(`SELECT at FROM sent WHERE key = ?`, key).Scan(&at) == nil && time.Since(time.Unix(at, 0)) < d {
		return false
	}
	s.db.Exec(`INSERT INTO sent (key, at) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET at = excluded.at`, key, time.Now().Unix())
	return true
}

func displayName(u user) string {
	if u.Display != "" {
		return u.Display
	}
	return u.Name
}

func (s *Server) displayOf(name string) string {
	var d string
	s.db.QueryRow(`SELECT display FROM users WHERE name = ?`, name).Scan(&d)
	if d == "" {
		return name
	}
	return d
}

// calendarActivity tells everyone with a shared calendar or list (but whoever did it) what changed: "Bob added
// “Picnic” to Family".
func (s *Server) calendarActivity(actor user, c calendarInfo, phrase string) {
	people := []string{c.Owner}
	rows, err := s.db.Query(`SELECT target FROM calendar_shares WHERE owner = ? AND cal = ?`, c.Owner, c.id)
	if err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil {
				people = append(people, t)
			}
		}
		rows.Close()
	}
	link := "#/calendar"
	if c.Kind == "tasks" {
		link = "#/tasks"
	}
	if len(people) == 1 && actor.Name == c.Owner {
		s.changed(strings.TrimPrefix(link, "#/"), c.Owner) // not shared: only the owner's other windows
		return
	}
	for _, p := range people {
		if p != actor.Name {
			s.notify(p, fmt.Sprintf("%s %s %s", displayName(actor), phrase, c.Name), link)
		}
	}
	s.changed(strings.TrimPrefix(link, "#/"), people...)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT id, text, link, created, read FROM notifications WHERE user = ? ORDER BY created DESC, id DESC LIMIT 50`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type note struct {
		ID      int64  `json:"id"`
		Text    string `json:"text"`
		Link    string `json:"link"`
		Created int64  `json:"created"`
		Read    bool   `json:"read"`
	}
	list := []note{}
	unread := 0
	for rows.Next() {
		var n note
		if rows.Scan(&n.ID, &n.Text, &n.Link, &n.Created, &n.Read) == nil {
			list = append(list, n)
			if !n.Read {
				unread++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "unread": unread})
}

func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request, u user) {
	s.db.Exec(`UPDATE notifications SET read = 1 WHERE user = ?`, u.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, s.settingsOf(u.Name))
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, u user) {
	var in userSettings
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Ntfy = strings.TrimSpace(in.Ntfy)
	if in.Ntfy != "" {
		p, err := url.Parse(in.Ntfy)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || strings.Trim(p.Path, "/") == "" {
			jsonError(w, http.StatusBadRequest, "The ntfy address is the server and a topic, like https://ntfy.example.com/cove-alice.")
			return
		}
	}
	if _, err := time.LoadLocation(in.TZ); err != nil {
		in.TZ = ""
	}
	if _, err := s.db.Exec(`INSERT INTO user_settings (user, ntfy, tz) VALUES (?, ?, ?)
		ON CONFLICT(user) DO UPDATE SET ntfy = excluded.ntfy, tz = CASE WHEN excluded.tz = '' THEN user_settings.tz ELSE excluded.tz END`,
		u.Name, in.Ntfy, in.TZ); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.settingsOf(u.Name))
}

func (s *Server) testNtfy(w http.ResponseWriter, r *http.Request, u user) {
	topic := s.settingsOf(u.Name).Ntfy
	if topic == "" {
		jsonError(w, http.StatusBadRequest, "Set an ntfy address first.")
		return
	}
	if err := s.pushNtfy(topic, "Cove notifications work on this device.", "#/settings"); err != nil {
		jsonError(w, http.StatusBadGateway, "Sending to ntfy didn't work: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- reminders: event alarms (VALARM, before the start) and tasks when they're due (9 in the morning for a date)

func (s *Server) remindEvery(d time.Duration) {
	last := time.Now().Add(-d)
	for {
		time.Sleep(d)
		now := time.Now()
		s.sendReminders(last, now)
		last = now
	}
}

// sendReminders sends the reminders due in (from, to].
func (s *Server) sendReminders(from, to time.Time) {
	rows, err := s.db.Query(`SELECT name FROM users`)
	if err != nil {
		return
	}
	var users []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			users = append(users, n)
		}
	}
	rows.Close()
	for _, u := range users {
		loc := s.location(u)
		cals, err := s.calendars(u)
		if err != nil {
			continue
		}
		for _, c := range cals {
			if c.Kind != "events" {
				continue
			}
			// alarms up to a week ahead of the start
			for _, o := range s.calendarOccurrences(nil, c, loc, from.Add(-time.Hour), to.Add(8*24*time.Hour)) {
				start := occStart(o, loc)
				for _, m := range o.Reminders {
					at := start.Add(-time.Duration(m) * time.Minute)
					if at.After(from) && !at.After(to) && s.once(fmt.Sprintf("r|%s|%s|%s|%d", u, o.ID, o.Start, m), 3*24*time.Hour) {
						s.notify(u, reminderText(o, start, loc), "#/calendar")
					}
				}
			}
		}
		for _, t := range s.tasksFor(u, loc, "") {
			if t.Done || t.Due == "" {
				continue
			}
			var at time.Time
			if len(t.Due) == 10 {
				d, _ := time.ParseInLocation("2006-01-02", t.Due, loc)
				at = d.Add(9 * time.Hour)
			} else {
				at, _ = time.Parse(time.RFC3339, t.Due)
			}
			if at.After(from) && !at.After(to) && s.once("t|"+u+"|"+t.ID+"|"+t.Due, 3*24*time.Hour) {
				s.notify(u, "Due: "+t.Title, "#/tasks")
			}
		}
	}
	s.householdReminders(from, to)
	s.db.Exec(`DELETE FROM sent WHERE at < ?`, to.Add(-4*24*time.Hour).Unix())
}

func occStart(o occurrence, loc *time.Location) time.Time {
	if o.AllDay {
		t, _ := time.ParseInLocation("2006-01-02", o.Start, loc)
		return t
	}
	t, _ := time.Parse(time.RFC3339, o.Start)
	return t
}

func reminderText(o occurrence, start time.Time, loc *time.Location) string {
	title := o.Title
	if title == "" {
		title = "(no title)"
	}
	if o.AllDay {
		return fmt.Sprintf("%s, %s", title, start.In(loc).Format("Monday, January 2"))
	}
	when := start.In(loc).Format("3:04 PM")
	if y, m, d := start.In(loc).Date(); y != time.Now().In(loc).Year() || m != time.Now().In(loc).Month() || d != time.Now().In(loc).Day() {
		when = start.In(loc).Format("Mon Jan 2, 3:04 PM")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s at %s", title, when)
	if o.Location != "" {
		fmt.Fprintf(&b, " (%s)", o.Location)
	}
	return b.String()
}
