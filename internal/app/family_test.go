package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWishesStayASurprise(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob", "carol")
	var mine, idea map[string]any
	p.as("bob", "POST", "/api/hh/wish", wish{For: "bob", Title: "Bike"}, &mine)
	p.as("alice", "POST", "/api/hh/wish", wish{For: "bob", Title: "Helmet (surprise)"}, &idea)
	// carol claims the bike; bob mustn't know, and can't see alice's idea
	mine["claimedBy"] = "carol"
	p.as("carol", "PUT", fmt.Sprintf("/api/hh/wish/%d", int64(mine["id"].(float64))), mine, nil)
	var bobSees []map[string]any
	p.as("bob", "GET", "/api/hh/wish", nil, &bobSees)
	if len(bobSees) != 1 || bobSees[0]["title"] != "Bike" || bobSees[0]["claimedBy"] != nil {
		t.Errorf("bob sees %v", bobSees)
	}
	if c := p.as("bob", "DELETE", fmt.Sprintf("/api/hh/wish/%d", int64(idea["id"].(float64))), nil, nil); c != 404 {
		t.Errorf("bob deleted the surprise: %d", c)
	}
	// bob edits his wish: carol's claim stays
	bobSees[0]["title"] = "Red bike"
	p.as("bob", "PUT", fmt.Sprintf("/api/hh/wish/%d", int64(mine["id"].(float64))), bobSees[0], nil)
	var aliceSees []map[string]any
	p.as("alice", "GET", "/api/hh/wish", nil, &aliceSees)
	claimed := ""
	for _, w := range aliceSees {
		if w["title"] == "Red bike" {
			claimed, _ = w["claimedBy"].(string)
		}
	}
	if len(aliceSees) != 2 || claimed != "carol" {
		t.Errorf("alice sees %v", aliceSees)
	}
	// alice can't take carol's claim over
	for _, w := range aliceSees {
		if w["title"] == "Red bike" {
			w["claimedBy"] = "alice"
			p.as("alice", "PUT", fmt.Sprintf("/api/hh/wish/%d", int64(w["id"].(float64))), w, nil)
		}
	}
	p.as("alice", "GET", "/api/hh/wish", nil, &aliceSees)
	for _, w := range aliceSees {
		if w["title"] == "Red bike" && w["claimedBy"] != "carol" {
			t.Errorf("claim taken over: %v", w)
		}
	}
	if c := p.as("bob", "POST", "/api/hh/wish", wish{For: "bob", Title: "x", Link: "javascript:alert(1)"}, &mine); c != 201 || mine["link"] != "" {
		t.Errorf("a javascript: link was kept: %v", mine)
	}
}

func TestMedsPantryBriefing(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	loc := p.s.location("alice")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	var m map[string]any
	if c := p.as("alice", "POST", "/api/hh/med", med{Who: "bob", Name: "Amoxicillin", Dose: "5 ml", Times: []string{"08:00", "20:00", "nope"}}, &m); c != 201 {
		t.Fatalf("med: %d", c)
	}
	var doses struct{ Doses []dose }
	p.as("bob", "GET", "/api/meds/today", nil, &doses)
	if len(doses.Doses) != 2 {
		t.Fatalf("doses: %+v", doses)
	}
	p.as("bob", "POST", fmt.Sprintf("/api/meds/%d/taken", int64(m["id"].(float64))), map[string]any{"day": today, "time": "08:00", "taken": true}, nil)
	p.as("alice", "GET", "/api/meds/today", nil, &doses)
	if !doses.Doses[0].Taken || doses.Doses[0].By != "Bob" || doses.Doses[1].Taken {
		t.Errorf("after bob took it: %+v", doses.Doses)
	}
	// the reminder at 8 goes to bob (it's his medicine) and to alice (who added it)
	day := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, loc)
	p.s.familyReminders(day.Add(-time.Minute), day.Add(time.Minute))
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE text = 'Time for Amoxicillin (5 ml)'`).Scan(&n)
	if n != 2 {
		t.Errorf("%d medicine reminders, want 2", n)
	}

	// the pantry: using the last but one puts it on the shopping list, once
	var pi map[string]any
	p.as("alice", "POST", "/api/hh/pantry", pantryItem{Name: "Coffee", Qty: 2, Min: 1, Barcode: "0123-4567-89"}, &pi)
	if pi["barcode"] != "0123456789" {
		t.Errorf("barcode: %v", pi["barcode"])
	}
	var used struct {
		Qty    float64
		Listed bool
	}
	p.as("bob", "POST", fmt.Sprintf("/api/pantry/%d/use", int64(pi["id"].(float64))), map[string]float64{"delta": -1}, &used)
	if used.Qty != 1 || !used.Listed {
		t.Errorf("used: %+v", used)
	}
	p.as("bob", "POST", fmt.Sprintf("/api/pantry/%d/use", int64(pi["id"].(float64))), map[string]float64{"delta": -1}, &used)
	if used.Listed {
		t.Error("listed twice")
	}
	var lookup map[string]any
	p.as("bob", "GET", "/api/pantry/lookup?code=0123456789", nil, &lookup)
	if lookup["name"] != "Coffee" {
		t.Errorf("lookup of a known barcode: %v", lookup)
	}

	// the briefing: today's chore, dinner, the medicine
	p.as("alice", "POST", "/api/hh/chore", chore{Title: "Water plants", Who: []string{"bob"}, Repeat: "daily"}, nil)
	p.as("alice", "POST", "/api/hh/meal", meal{Date: today, Slot: "dinner", Text: "Soup"}, nil)
	var br struct{ Text string }
	p.as("bob", "GET", "/api/briefing", nil, &br)
	for _, want := range []string{"Water plants", "Dinner: Soup", "2 doses"} {
		if !strings.Contains(br.Text, want) {
			t.Errorf("briefing %q is missing %q", br.Text, want)
		}
	}
	var st userSettings
	p.as("bob", "PUT", "/api/settings", userSettings{Briefing: "07:30"}, &st)
	if st.Briefing != "07:30" {
		t.Fatalf("settings: %+v", st)
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), 7, 30, 0, 0, p.s.location("bob"))
	p.s.sendBriefings(at.Add(-time.Minute), at.Add(time.Minute))
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'bob' AND text LIKE 'Good morning!%Water plants%'`).Scan(&n)
	if n != 1 {
		t.Error("no morning briefing")
	}
}

// toolAI is an OpenAI-compatible server that, asked anything, calls the tools it's told to, then answers.
func toolAI(t *testing.T, calls string) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var heard []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []map[string]any `json:"messages"`
			Tools    []tool           `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "text/event-stream")
		last := in.Messages[len(in.Messages)-1]
		if last["role"] == "tool" {
			mu.Lock()
			for _, m := range in.Messages {
				if m["role"] == "tool" {
					heard = append(heard, m["content"].(string))
				}
			}
			mu.Unlock()
			b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": "All done."}}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
			return
		}
		// the tool call, in two pieces like real servers send it
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[`+calls+`]}}]}`)
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	return srv, &heard
}

func TestAskCoveActions(t *testing.T) {
	day := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	ai, heard := toolAI(t, `{"index":0,"id":"a","function":{"name":"add_to_shopping_list","arguments":"{\"items\":[\"Milk\","}},`+
		`{"index":0,"function":{"arguments":"\"Eggs\"]}"}},`+
		`{"index":1,"id":"b","function":{"name":"add_event","arguments":"{\"title\":\"Dentist\",\"date\":\"`+day+`\",\"time\":\"15:00\"}"}},`+
		`{"index":2,"id":"c","function":{"name":"add_task","arguments":"{\"title\":\"Call grandma\",\"for\":\"Bob\"}"}},`+
		`{"index":3,"id":"d","function":{"name":"add_expense","arguments":"{\"what\":\"Pizza\",\"amount\":30}"}}`)
	defer ai.Close()
	p := newPeople(t, nil, "alice", "bob")
	p.s.setSetting("x", "y")
	p.s.db.Exec(`INSERT INTO ai_settings (key, value) VALUES ('url', ?), ('model', 'm')`, ai.URL+"/v1")
	ask := func(who string) string {
		req, _ := http.NewRequest("POST", p.srv.URL+"/api/ask", strings.NewReader(`{"question":"do the things"}`))
		req.Header.Set("X-Cove", "1")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie[who]})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	stream := ask("alice")
	for _, want := range []string{"Added Milk, Eggs to the shopping list.", "Put “Dentist” on your calendar", "at 3:00 PM", "Added the task “Call grandma”.", "Wrote down Pizza, $30.00.", "All done."} {
		if !strings.Contains(stream, want) {
			t.Errorf("the answer is missing %q: %s", want, stream)
		}
	}
	if len(*heard) != 4 {
		t.Errorf("the model heard back %d results", len(*heard))
	}
	if !strings.Contains(stream, "not assigned to Bob: share the list with them first") {
		t.Error("didn't say why the task isn't bob's")
	}
	var evs []occurrence
	p.as("alice", "GET", "/api/events?from="+day+"&to="+time.Now().AddDate(0, 0, 5).Format("2006-01-02")+"&tz=UTC", nil, &evs)
	if len(evs) != 1 || evs[0].Title != "Dentist" {
		t.Errorf("events: %+v", evs)
	}
	// a kid asking for spending: not done
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name = 'bob'`)
	if s := ask("bob"); !strings.Contains(s, "Couldn't: That's for grown-ups.") {
		t.Errorf("a kid's expense: %s", s)
	}
}

func TestHomeAssistant(t *testing.T) {
	var mu sync.Mutex
	var called []string
	ha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/api/states":
			io.WriteString(w, `[{"entity_id":"light.kitchen","state":"off","attributes":{"friendly_name":"Kitchen"}},
				{"entity_id":"lock.front","state":"locked","attributes":{"friendly_name":"Front door"}},
				{"entity_id":"sensor.solar","state":"2.5","attributes":{"unit_of_measurement":"kW"}},
				{"entity_id":"sensor.batt","state":"81","attributes":{"unit_of_measurement":"%"}},
				{"entity_id":"weather.home","state":"sunny","attributes":{"temperature":72,"temperature_unit":"°F"}}]`)
		case strings.HasPrefix(r.URL.Path, "/api/services/"):
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			called = append(called, r.URL.Path+" "+string(b))
			mu.Unlock()
			io.WriteString(w, "[]")
		}
	}))
	defer ha.Close()
	p := newPeople(t, nil, "alice", "kid")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'alice'`)
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name = 'kid'`)
	cfg := haConfig{URL: ha.URL, Token: "tok", Entities: []haEntity{{ID: "light.kitchen", Home: true}, {ID: "lock.front"}}, Solar: "sensor.solar", Battery: "sensor.batt", Weather: "weather.home"}
	if c := p.as("kid", "PUT", "/api/admin/ha", cfg, nil); c != 403 {
		t.Errorf("a kid set up Home Assistant: %d", c)
	}
	var got struct {
		Config   haConfig
		TokenSet bool
	}
	p.as("alice", "PUT", "/api/admin/ha", cfg, &got)
	if got.Config.Token != "" || !got.TokenSet {
		t.Errorf("the token came back: %+v", got)
	}
	var d struct {
		Devices []deviceView
		Energy  energyView
		Weather weatherView
	}
	p.as("kid", "GET", "/api/devices", nil, &d)
	if len(d.Devices) != 2 || d.Devices[0].Name != "Kitchen" || !d.Devices[0].Can || d.Devices[1].Can || *d.Energy.Solar != 2500 || *d.Energy.Battery != 81 || d.Weather.Temp != 72 {
		t.Errorf("devices: %+v", d)
	}
	if c := p.as("kid", "POST", "/api/devices", map[string]string{"id": "lock.front", "action": "unlock"}, nil); c != 400 {
		t.Errorf("a kid unlocked the door: %d", c)
	}
	if c := p.as("kid", "POST", "/api/devices", map[string]string{"id": "light.kitchen", "action": "on"}, nil); c != 200 {
		t.Errorf("the kitchen light: %d", c)
	}
	if c := p.as("alice", "POST", "/api/devices", map[string]string{"id": "switch.not_shown", "action": "on"}, nil); c != 400 {
		t.Errorf("switched something Cove doesn't show: %d", c)
	}
	if len(called) != 1 || !strings.Contains(called[0], "/api/services/light/turn_on") {
		t.Errorf("calls: %v", called)
	}
}

func TestJournalIsPrivate(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'bob'`) // not even an admin
	var j map[string]any
	if c := p.as("alice", "POST", "/api/hh/journal", journal{Text: "Dear diary", Photos: []string{"/x.jpg"}}, &j); c != 201 || j["private"] != true {
		t.Fatalf("entry: %d %v", c, j)
	}
	var list []map[string]any
	p.as("bob", "GET", "/api/hh/journal", nil, &list)
	if len(list) != 0 {
		t.Errorf("bob sees alice's journal: %v", list)
	}
	id := int64(j["id"].(float64))
	if c := p.as("bob", "PUT", fmt.Sprintf("/api/hh/journal/%d", id), journal{Text: "edited"}, nil); c != 404 {
		t.Errorf("bob changed it: %d", c)
	}
	if c := p.as("bob", "GET", fmt.Sprintf("/api/journal/%d/photo?path=/x.jpg", id), nil, nil); c != 404 {
		t.Errorf("bob saw a photo in it: %d", c)
	}
	p.as("alice", "GET", "/api/hh/journal", nil, &list)
	if len(list) != 1 {
		t.Errorf("alice doesn't see her own entry: %v", list)
	}
	// entries written before the journal became personal are made private
	p.s.db.Exec(`UPDATE hh SET private = 0 WHERE kind = 'journal'`)
	migrateHousehold(p.s.db)
	p.as("bob", "GET", "/api/hh/journal", nil, &list)
	if len(list) != 0 {
		t.Errorf("an old entry stayed shared: %v", list)
	}
}
