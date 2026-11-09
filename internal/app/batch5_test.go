package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// visionAI describes every picture as a beach, and answers chat with tool calls as told.
func visionAI(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string `json:"model"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "eyes" && !bytes.Contains(in.Messages[0].Content, []byte("data:image/jpeg;base64,")) {
			http.Error(w, "no picture", 400)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"Two kids building a sandcastle on a sunny beach.\nbeach, sand, summer, ocean"}}]}`)
	}))
}

func TestPhotosByWhatsInThem(t *testing.T) {
	ai := visionAI(t)
	defer ai.Close()
	p := newPeople(t, nil, "alice")
	p.s.db.Exec(`INSERT INTO ai_settings (key, value) VALUES ('url', ?), ('model', 'm'), ('vision', 'eyes')`, ai.URL+"/v1")
	p.s.writeFile("alice", "/Photos/beach.jpg", bytes.NewReader(exifJPEG(t, "2025:07:04 12:00:00", 0, 0)), false, time.Time{})
	if n := p.s.describePending("alice", time.Minute); n != 1 {
		t.Fatalf("described %d", n)
	}
	if n := p.s.describePending("alice", time.Minute); n != 0 {
		t.Errorf("described again: %d", n)
	}
	var list []photoMeta
	p.as("alice", "GET", "/api/photos?q=sandcast", nil, &list)
	if len(list) != 1 || !strings.Contains(list[0].Caption, "sandcastle") {
		t.Errorf("by what's in it: %+v", list)
	}
	p.as("alice", "GET", "/api/photos?q=snow", nil, &list)
	if len(list) != 0 {
		t.Errorf("snow: %+v", list)
	}
	var found struct{ Photos []photoMeta }
	p.as("alice", "GET", "/api/search?q=beach+summer", nil, &found)
	if len(found.Photos) != 1 {
		t.Errorf("search: %+v", found)
	}
}

func TestGoalsAndRides(t *testing.T) {
	p := newPeople(t, nil, "mom", "sam")
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name = 'sam'`)
	var g map[string]any
	if c := p.as("sam", "POST", "/api/hh/goal", goal{Kid: "sam", Title: "New bike", Target: 12000, Saved: 999999}, &g); c != 201 {
		t.Fatalf("goal: %d", c)
	}
	id := int64(g["id"].(float64))
	g["saved"] = 50000 // a kid can't add money by editing
	p.as("sam", "PUT", fmt.Sprintf("/api/hh/goal/%d", id), g, &g)
	if c := p.as("sam", "POST", fmt.Sprintf("/api/goals/%d/add", id), map[string]int{"cents": 100}, nil); c != 403 {
		t.Errorf("a kid added money: %d", c)
	}
	var saved struct{ Saved int }
	p.as("mom", "POST", fmt.Sprintf("/api/goals/%d/add", id), map[string]int{"cents": 4500}, &saved)
	// a kid's new goal starts at nothing saved, and editing can't change that: only a grown-up's money counts
	if saved.Saved != 4500 {
		t.Errorf("saved %d", saved.Saved)
	}
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'sam'`).Scan(&n)
	if n != 1 {
		t.Errorf("sam heard %d times", n)
	}

	// a ride: the driver hears about it, and is reminded the evening before and an hour before
	day := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	p.as("sam", "POST", "/api/hh/ride", ride{Date: day, Time: "16:30", What: "Soccer practice", Driver: "mom", Riders: []string{"Sam", "Leo"}}, nil)
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'mom' AND text LIKE 'Sam put you down to drive%'`).Scan(&n)
	if n != 1 {
		t.Error("mom wasn't told she's driving")
	}
	loc := p.s.location("mom")
	d, _ := time.ParseInLocation("2006-01-02", day, loc)
	for _, at := range []time.Time{d.Add(-5 * time.Hour), d.Add(15*time.Hour + 30*time.Minute)} {
		p.s.rideReminders(at.Add(-time.Minute), at.Add(time.Minute))
	}
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'mom' AND text LIKE 'You drive Soccer practice%(Sam, Leo)'`).Scan(&n)
	if n != 2 {
		t.Errorf("%d ride reminders, want 2", n)
	}
}

func TestOnMyWayHome(t *testing.T) {
	ai, _ := toolAI(t, `{"index":0,"id":"a","function":{"name":"post_to_family_chat","arguments":"{\"text\":\"On my way home, there in 20 minutes\"}"}}`)
	defer ai.Close()
	p := newPeople(t, nil, "mom", "dad")
	p.s.db.Exec(`INSERT INTO ai_settings (key, value) VALUES ('url', ?), ('model', 'm')`, ai.URL+"/v1")
	var pw struct{ Password string }
	p.as("dad", "POST", "/api/app-passwords", map[string]string{"name": "Siri"}, &pw)
	req, _ := http.NewRequest("POST", p.srv.URL+"/shortcut", strings.NewReader("tell the family I'm on my way home"))
	req.SetBasicAuth("dad", pw.Password)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("shortcut: %v %v", err, res)
	}
	var rooms []chatRoom
	p.as("mom", "GET", "/api/chat", nil, &rooms)
	if len(rooms) != 1 || rooms[0].Last != "On my way home, there in 20 minutes" || rooms[0].Unread != 1 {
		t.Errorf("mom's chat: %+v", rooms)
	}
}
