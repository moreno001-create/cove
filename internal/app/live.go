package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Live updates: each open Cove keeps an event stream (/api/live), and is told when something it shows changed
// ("files", "calendar", "tasks", "contacts", "notes", "notifications"), so it redraws without a reload. Phones that
// added Cove to their home screen also get notifications through Web Push, with no extra app.

type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan string]bool
}

var live = &hub{subs: map[string]map[chan string]bool{}}

// changed tells users' open Coves that topic changed.
func (s *Server) changed(topic string, users ...string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	seen := map[string]bool{}
	for _, u := range users {
		if seen[u] {
			continue
		}
		seen[u] = true
		for ch := range live.subs[u] {
			select {
			case ch <- topic:
			default: // a slow client misses one; it catches up on the next
			}
		}
	}
}

// fileWatchers are the owner and everyone a file or folder of theirs is shared with.
func (s *Server) fileWatchers(owner string) []string {
	out := []string{owner}
	if rows, err := s.db.Query(`SELECT DISTINCT target FROM user_shares WHERE owner = ?`, owner); err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil {
				out = append(out, t)
			}
		}
		rows.Close()
	}
	return out
}

// calendarWatchers are the owner of a calendar or list and everyone it's shared with.
func (s *Server) calendarWatchers(owner, cal string) []string {
	out := []string{owner}
	if rows, err := s.db.Query(`SELECT target FROM calendar_shares WHERE owner = ? AND cal = ?`, owner, cal); err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil {
				out = append(out, t)
			}
		}
		rows.Close()
	}
	return out
}

func (s *Server) liveStream(w http.ResponseWriter, r *http.Request, u user) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming isn't supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan string, 16)
	live.mu.Lock()
	if live.subs[u.Name] == nil {
		live.subs[u.Name] = map[chan string]bool{}
	}
	live.subs[u.Name][ch] = true
	live.mu.Unlock()
	defer func() {
		live.mu.Lock()
		delete(live.subs[u.Name], ch)
		live.mu.Unlock()
	}()
	fmt.Fprint(w, "retry: 5000\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case t := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", t)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// --- Web Push

const pushSchema = `
CREATE TABLE IF NOT EXISTS push_subs (
	endpoint TEXT PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	p256dh TEXT NOT NULL,
	auth TEXT NOT NULL,
	created INTEGER NOT NULL
);`

type vapidKeys struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

// vapid is the server's Web Push key pair, made the first time.
func (s *Server) vapid() (vapidKeys, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var k vapidKeys
	if b, err := s.data.ReadFile("vapid.json"); err == nil && json.Unmarshal(b, &k) == nil && k.Public != "" {
		return k, nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return k, err
	}
	k = vapidKeys{priv, pub}
	b, _ := json.Marshal(k)
	return k, s.data.WriteFile("vapid.json", b, 0o600)
}

func (s *Server) pushKey(w http.ResponseWriter, r *http.Request, u user) {
	k, err := s.vapid()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM push_subs WHERE user = ?`, u.Name).Scan(&n)
	writeJSON(w, http.StatusOK, map[string]any{"key": k.Public, "devices": n})
}

func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request, u user) {
	var sub webpush.Subscription
	if err := readJSON(r, &sub); err != nil || !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		jsonError(w, http.StatusBadRequest, "That isn't a push subscription.")
		return
	}
	if _, err := s.db.Exec(`INSERT INTO push_subs (endpoint, user, p256dh, auth, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET user = excluded.user, p256dh = excluded.p256dh, auth = excluded.auth`,
		sub.Endpoint, u.Name, sub.Keys.P256dh, sub.Keys.Auth, time.Now().Unix()); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	readJSON(r, &in)
	s.db.Exec(`DELETE FROM push_subs WHERE endpoint = ? AND user = ?`, in.Endpoint, u.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pushTo sends a notification to every device the user turned push on for, dropping ones that are gone.
// pushHTTP reaches push services (Apple's, Google's, Mozilla's), on the internet only (a test hook).
var pushHTTP = pushClient

func (s *Server) pushTo(user, text, link string) {
	rows, err := s.db.Query(`SELECT endpoint, p256dh, auth FROM push_subs WHERE user = ?`, user)
	if err != nil {
		return
	}
	var subs []webpush.Subscription
	for rows.Next() {
		var sub webpush.Subscription
		if rows.Scan(&sub.Endpoint, &sub.Keys.P256dh, &sub.Keys.Auth) == nil {
			subs = append(subs, sub)
		}
	}
	rows.Close()
	if len(subs) == 0 {
		return
	}
	k, err := s.vapid()
	if err != nil {
		return
	}
	msg, _ := json.Marshal(map[string]string{"title": "Cove", "body": text, "link": "/" + link})
	subscriber := "cove@" + strings.TrimPrefix(strings.TrimPrefix(s.cfg.URL, "https://"), "http://")
	for _, sub := range subs {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		res, err := webpush.SendNotificationWithContext(ctx, msg, &sub, &webpush.Options{Subscriber: subscriber,
			VAPIDPublicKey: k.Public, VAPIDPrivateKey: k.Private, TTL: 24 * 3600, HTTPClient: pushHTTP})
		cancel()
		if err != nil {
			log.Printf("push to %s: %v", user, err)
			continue
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
			s.db.Exec(`DELETE FROM push_subs WHERE endpoint = ?`, sub.Endpoint)
		}
	}
}
