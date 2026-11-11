package app

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestShortcut(t *testing.T) {
	ai, _ := toolAI(t, `{"index":0,"id":"a","function":{"name":"add_to_shopping_list","arguments":"{\"items\":[\"Milk\"]}"}}`)
	defer ai.Close()
	p := newPeople(t, nil, "alice")
	p.s.db.Exec(`INSERT INTO ai_settings (key, value) VALUES ('url', ?), ('model', 'm')`, ai.URL+"/v1")
	var pw struct{ Password string }
	p.as("alice", "POST", "/api/app-passwords", map[string]string{"name": "iPhone Siri"}, &pw)
	say := func(user, pass, text string) (int, string) {
		req, _ := http.NewRequest("POST", p.srv.URL+"/shortcut", strings.NewReader(text))
		req.SetBasicAuth(user, pass)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if c, _ := say("alice", "wrong", "add milk"); c != 401 {
		t.Errorf("a wrong password: %d", c)
	}
	c, answer := say("alice", pw.Password, "add milk")
	if c != 200 || answer != "All done." {
		t.Errorf("%d %q", c, answer)
	}
	var notes []fridgeNote
	p.as("alice", "GET", "/api/fridge", nil, &notes)
	if len(notes) != 1 || notes[0].Items[0].Text != "Milk" {
		t.Errorf("the shopping list: %+v", notes)
	}
}

func TestShoppingByAisle(t *testing.T) {
	p := newPeople(t, nil, "alice")
	for i := 0; i < 2; i++ { // twice: they become "the usual"
		p.as("alice", "POST", "/api/shopping", map[string][]string{"items": {"Bananas", "Paper towels", "Whole milk"}}, nil)
		var l struct {
			Items []struct {
				fridgeItem
				Index int
			}
		}
		p.as("alice", "GET", "/api/shopping", nil, &l)
		id, list := p.s.shoppingList(p.home())
		for j := range list {
			list[j].Done = true
		}
		p.as("alice", "PUT", fmt.Sprintf("/api/fridge/%d", id), fridgeInput{Kind: "list", Text: "Shopping", Items: list}, nil)
		if i == 0 {
			p.as("alice", "POST", "/api/hh/pantry", pantryItem{Name: "Milk", Qty: 0, Min: 1}, nil)
		}
		var done struct{ Bought, Stocked int }
		p.as("alice", "POST", "/api/shopping/done", nil, &done)
		if done.Bought != 3 || done.Stocked != 1 {
			t.Errorf("done shopping: %+v", done)
		}
	}
	p.as("alice", "POST", "/api/shopping", map[string][]string{"items": {"Cheddar cheese", "Bananas"}}, nil)
	var l struct {
		Items []struct {
			Text  string
			Aisle string
		}
		Usual []string
	}
	p.as("alice", "GET", "/api/shopping", nil, &l)
	if len(l.Items) != 2 || l.Items[0].Aisle != "Produce" || l.Items[1].Aisle != "Dairy & eggs" {
		t.Errorf("by aisle: %+v", l.Items)
	}
	if strings.Join(l.Usual, ",") != "Paper towels,Whole milk" && strings.Join(l.Usual, ",") != "Whole milk,Paper towels" {
		t.Errorf("the usual (not what's on the list): %v", l.Usual)
	}
	var milk []map[string]any
	p.as("alice", "GET", "/api/hh/pantry", nil, &milk)
	if milk[0]["qty"].(float64) != 2 {
		t.Errorf("the pantry's milk: %v", milk)
	}
}

func TestSitterPage(t *testing.T) {
	p := newPeople(t, nil, "mom", "kid")
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name = 'kid'`)
	if c := p.as("mom", "POST", "/api/sitter-links", map[string]any{"hours": 5}, nil); c != 400 {
		t.Errorf("a link before the page: %d", c)
	}
	p.as("mom", "POST", "/api/hh/sitter", sitterInfo{Address: "12 Elm St", Contacts: []sitterPerson{{Name: "Mom", Phone: "555-0100", Who: "Mom"}},
		Kids: []sitterKid{{Name: "Sam <script>", Allergies: "Peanuts", Bedtime: "8:00"}}, Rules: "No screens after 7", Wifi: true}, nil)
	p.as("mom", "POST", "/api/fridge", fridgeInput{Kind: "wifi", Text: "Guest", Items: []fridgeItem{{Text: "pw123"}}}, nil)
	if c := p.as("kid", "POST", "/api/sitter-links", nil, nil); c != 403 {
		t.Errorf("a kid made a link: %d", c)
	}
	var link struct{ Token, URL string }
	p.as("mom", "POST", "/api/sitter-links", map[string]any{"hours": 5, "label": "Saturday"}, &link)
	res, err := http.Get(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	page := string(b)
	for _, want := range []string{"12 Elm St", "tel:555-0100", "Allergies: Peanuts", "Sam &lt;script&gt;", "No screens after 7", "pw123", "data:image/png;base64,"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "<script>") {
		t.Error("the page has a script in it")
	}
	p.s.db.Exec(`UPDATE sitter_links SET expires = ?`, time.Now().Add(-time.Minute).Unix())
	if res, _ := http.Get(link.URL); res.StatusCode != 404 {
		t.Errorf("an ended link: %d", res.StatusCode)
	}
}

func TestGuests(t *testing.T) {
	p := newPeople(t, nil, "mom", "grandma")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'mom'`)
	p.as("mom", "PUT", "/api/admin/users/grandma/guest", map[string]bool{"guest": true}, nil)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/fridge", 200}, {"GET", "/api/chat", 200}, {"GET", "/api/calendars", 200}, {"GET", "/api/albums", 200},
		{"GET", "/api/files?path=/", 403}, {"GET", "/api/hh/recipe", 403}, {"GET", "/api/budget", 403}, {"GET", "/api/chores", 403},
		{"POST", "/api/fridge", 403}, {"POST", "/api/ask", 403}, {"GET", "/api/photos", 403},
	} {
		if got := p.as("grandma", c.method, c.path, map[string]string{"text": "hi"}, nil); got != c.want {
			t.Errorf("grandma %s %s: %d, want %d", c.method, c.path, got, c.want)
		}
	}
	var rooms []chatRoom
	p.as("grandma", "GET", "/api/chat", nil, &rooms)
	if c := p.as("grandma", "POST", fmt.Sprintf("/api/chat/%d/messages", rooms[0].ID), map[string]string{"text": "Hello dears"}, nil); c != 201 {
		t.Errorf("grandma chatting: %d", c)
	}
}

func TestHomeworkReminder(t *testing.T) {
	p := newPeople(t, nil, "mom", "sam")
	due := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	p.as("mom", "POST", "/api/hh/homework", homework{Kid: "sam", Subject: "Math", Title: "Fractions sheet", Due: due}, nil)
	d, _ := time.ParseInLocation("2006-01-02", due, p.s.location("sam"))
	at := d.AddDate(0, 0, -1).Add(17 * time.Hour)
	p.s.homeworkReminders(at.Add(-time.Minute), at.Add(time.Minute))
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE text = 'Due tomorrow: Math Fractions sheet'`).Scan(&n)
	if n != 2 {
		t.Errorf("%d reminders, want sam's and mom's", n)
	}
}
