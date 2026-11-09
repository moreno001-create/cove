package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// Subscribed calendars: holidays, a school's or a team's calendar, work. Cove fetches their .ics address every few
// hours; they show read-only beside the user's own (as "~sub:<id>").

const subscriptionsSchema = `
CREATE TABLE IF NOT EXISTS subscriptions (
	id INTEGER PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	name TEXT NOT NULL,
	color TEXT NOT NULL,
	url TEXT NOT NULL,
	fetched INTEGER NOT NULL DEFAULT 0,
	data TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT ''
);`

type subscription struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	URL     string `json:"url"`
	Fetched int64  `json:"fetched"`
	Error   string `json:"error"`
	data    string
}

// subClient fetches subscriptions from the internet, never from this machine or the home network (see guard.go).
var subClient = outsideClient

func subURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "webcal://") {
		raw = "https://" + strings.TrimPrefix(raw, "webcal://")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("give the calendar's web address (https://… or webcal://…)")
	}
	return u.String(), nil
}

// fetchSubscription refreshes one subscription's events.
func (s *Server) fetchSubscription(id int64) error {
	var raw string
	if err := s.db.QueryRow(`SELECT url FROM subscriptions WHERE id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	req.Header.Set("Accept", "text/calendar")
	res, err := subClient.Do(req)
	if err == nil && res.StatusCode != http.StatusOK {
		err = fmt.Errorf("it answered %d", res.StatusCode)
	}
	var body []byte
	if err == nil {
		body, err = io.ReadAll(io.LimitReader(res.Body, 20<<20))
		res.Body.Close()
	}
	if err == nil {
		_, err = ical.NewDecoder(strings.NewReader(string(body))).Decode()
		if err != nil {
			err = errors.New("that isn't a calendar (.ics)")
		}
	}
	if err != nil {
		s.db.Exec(`UPDATE subscriptions SET fetched = ?, error = ? WHERE id = ?`, time.Now().Unix(), err.Error(), id)
		return err
	}
	s.db.Exec(`UPDATE subscriptions SET fetched = ?, data = ?, error = '' WHERE id = ?`, time.Now().Unix(), string(body), id)
	return nil
}

func (s *Server) refreshSubscriptionsEvery(d time.Duration) {
	for {
		time.Sleep(time.Minute)
		rows, err := s.db.Query(`SELECT id FROM subscriptions WHERE fetched < ?`, time.Now().Add(-d).Unix())
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			s.fetchSubscription(id)
		}
	}
}

func (s *Server) subscriptionsOf(user string) []subscription {
	rows, err := s.db.Query(`SELECT id, name, color, url, fetched, error, data FROM subscriptions WHERE user = ? ORDER BY id`, user)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []subscription
	for rows.Next() {
		var sub subscription
		if rows.Scan(&sub.ID, &sub.Name, &sub.Color, &sub.URL, &sub.Fetched, &sub.Error, &sub.data) == nil {
			out = append(out, sub)
		}
	}
	return out
}

// subscriptionOccurrences adds the subscribed calendars' events.
func (s *Server) subscriptionOccurrences(out []occurrence, u user, loc *time.Location, from, to time.Time) []occurrence {
	for _, sub := range s.subscriptionsOf(u.Name) {
		if sub.data == "" {
			continue
		}
		data, err := ical.NewDecoder(strings.NewReader(sub.data)).Decode()
		if err != nil {
			continue
		}
		ref := fmt.Sprintf("~sub:%d", sub.ID)
		// one event (with its changed occurrences) at a time, as expand expects
		byUID := map[string]*ical.Calendar{}
		for _, ch := range data.Children {
			if ch.Name != ical.CompEvent {
				continue
			}
			uid, _ := ch.Props.Text(ical.PropUID)
			if byUID[uid] == nil {
				byUID[uid] = ical.NewCalendar()
			}
			byUID[uid].Children = append(byUID[uid].Children, ch)
		}
		for uid, obj := range byUID {
			for _, o := range expand(obj, loc, from, to) {
				o.ID, o.Cal, o.ReadOnly = ref+"/"+uid, ref, true
				out = append(out, o)
			}
		}
	}
	return out
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request, u user) {
	var in subscription
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	link, err := subURL(in.URL)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Name = strings.TrimSpace(in.Name); in.Name == "" {
		in.Name = "Subscribed"
	}
	if !colorPattern.MatchString(in.Color) {
		in.Color = calendarColors[7]
	}
	res, err := s.db.Exec(`INSERT INTO subscriptions (user, name, color, url) VALUES (?, ?, ?, ?)`, u.Name, in.Name, in.Color, link)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	if err := s.fetchSubscription(id); err != nil {
		s.db.Exec(`DELETE FROM subscriptions WHERE id = ?`, id)
		jsonError(w, http.StatusBadRequest, "That calendar couldn't be read: "+err.Error())
		return
	}
	s.changed("calendar", u.Name)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request, u user) {
	var in subscription
	readJSON(r, &in)
	if strings.TrimSpace(in.Name) == "" || !colorPattern.MatchString(in.Color) {
		jsonError(w, http.StatusBadRequest, "It needs a name and a color.")
		return
	}
	s.db.Exec(`UPDATE subscriptions SET name = ?, color = ? WHERE id = ? AND user = ?`, strings.TrimSpace(in.Name), in.Color, r.PathValue("id"), u.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request, u user) {
	s.db.Exec(`DELETE FROM subscriptions WHERE id = ? AND user = ?`, r.PathValue("id"), u.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) refreshSubscription(w http.ResponseWriter, r *http.Request, u user) {
	var id int64
	if s.db.QueryRow(`SELECT id FROM subscriptions WHERE id = ? AND user = ?`, r.PathValue("id"), u.Name).Scan(&id) != nil {
		jsonError(w, http.StatusNotFound, "There's no such subscription.")
		return
	}
	if err := s.fetchSubscription(id); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- free/busy: when someone is busy (from their calendars and accepted invitations), without what it is

func (s *Server) freeBusy(w http.ResponseWriter, r *http.Request, u user) {
	q := r.URL.Query()
	from, err1 := time.Parse(time.RFC3339, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339, q.Get("to"))
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > 40*24*time.Hour {
		jsonError(w, http.StatusBadRequest, "from and to are times, up to 40 days apart")
		return
	}
	type span struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	out := map[string][]span{}
	for _, name := range strings.Split(q.Get("users"), ",") {
		if name == "" {
			continue
		}
		var exists int
		if s.db.QueryRow(`SELECT 1 FROM users WHERE name = ?`, name).Scan(&exists) != nil {
			continue
		}
		busy := []span{}
		them := user{Name: name}
		for _, o := range s.eventsFor(them, time.UTC, from.Add(-24*time.Hour), to.Add(24*time.Hour)) {
			if o.AllDay || o.Cal == "~birthdays" || strings.HasPrefix(o.Cal, "~sub:") || (o.Invite != nil && o.Invite.Status != "ACCEPTED") {
				continue
			}
			st, en := occStart(o, time.UTC), occStart(occurrence{Start: o.End}, time.UTC)
			if st.Before(to) && en.After(from) {
				busy = append(busy, span{o.Start, o.End})
			}
		}
		out[name] = busy
	}
	writeJSON(w, http.StatusOK, out)
}
