package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// asIn makes a request as someone, looking at one household.
func (p *people) asIn(who string, home int64, method, path string, body any, out any) int {
	p.t.Helper()
	return p.asWith(who, map[string]string{"X-Cove-Household": fmt.Sprint(home)}, method, path, body, out)
}

func (p *people) asWith(who string, hdr map[string]string, method, path string, body any, out any) int {
	p.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(mustJSON(body))
	}
	req, _ := http.NewRequest(method, p.srv.URL+path, rd)
	req.Header.Set("X-Cove", "1")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie[who]})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		decodeJSON(res.Body, out)
	}
	return res.StatusCode
}

func TestTwoHouseholds(t *testing.T) {
	p := newPeople(t, nil, "mom", "dad", "jones", "grandma", "newbie")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'mom'`)
	morenos := p.home()
	// the test helper put everyone in the first household: set up two households properly through the admin API
	p.s.db.Exec(`DELETE FROM household_members WHERE user IN ('jones', 'grandma', 'newbie')`)
	var made struct{ ID int64 }
	if c := p.as("dad", "POST", "/api/admin/households", map[string]string{"name": "The Joneses"}, nil); c != 403 {
		t.Errorf("a non-admin made a household: %d", c)
	}
	p.as("mom", "POST", "/api/admin/households", map[string]string{"name": "The Joneses"}, &made)
	joneses := made.ID
	p.as("mom", "POST", fmt.Sprintf("/api/admin/households/%d/members", joneses), map[string]any{"user": "jones", "in": true}, nil)
	p.as("mom", "POST", fmt.Sprintf("/api/admin/households/%d/members", joneses), map[string]any{"user": "grandma", "in": true}, nil)
	p.as("mom", "POST", fmt.Sprintf("/api/admin/households/%d/members", morenos), map[string]any{"user": "grandma", "in": true}, nil) // in both

	// each household's fridge, lists, chat and albums stay its own
	p.as("mom", "POST", "/api/fridge", fridgeInput{Kind: "note", Text: "Moreno secret"}, nil)
	p.as("mom", "POST", "/api/hh/recipe", recipe{Title: "Moreno tacos"}, nil)
	p.as("mom", "POST", "/api/hh/bill", bill{Name: "Moreno mortgage", Amount: 2000, Day: 1}, nil)
	p.as("mom", "POST", "/api/shopping", map[string][]string{"items": {"Moreno milk"}}, nil)
	p.as("mom", "POST", "/api/albums", map[string]any{"name": "Moreno summer", "shared": true}, nil)
	var rooms []chatRoom
	p.as("mom", "GET", "/api/chat", nil, &rooms)
	p.as("mom", "POST", fmt.Sprintf("/api/chat/%d/messages", rooms[0].ID), map[string]string{"text": "Moreno dinner at 6"}, nil)

	var notes []fridgeNote
	p.as("jones", "GET", "/api/fridge", nil, &notes)
	if len(notes) != 0 {
		t.Errorf("the Joneses see the Morenos' fridge: %+v", notes)
	}
	var recipes, bills []map[string]any
	p.as("jones", "GET", "/api/hh/recipe", nil, &recipes)
	p.as("jones", "GET", "/api/hh/bill", nil, &bills)
	if len(recipes)+len(bills) != 0 {
		t.Errorf("the Joneses see the Morenos' lists: %v %v", recipes, bills)
	}
	var jRooms []chatRoom
	p.as("jones", "GET", "/api/chat", nil, &jRooms)
	if len(jRooms) != 1 || jRooms[0].Last != "" || jRooms[0].ID == rooms[0].ID {
		t.Errorf("the Joneses' chat: %+v", jRooms)
	}
	if c := p.as("jones", "GET", fmt.Sprintf("/api/chat/%d/messages", rooms[0].ID), nil, nil); c != 404 {
		t.Errorf("jones read the Morenos' chat: %d", c)
	}
	var albums []album
	p.as("jones", "GET", "/api/albums", nil, &albums)
	if len(albums) != 0 {
		t.Errorf("the Joneses see the Morenos' shared album: %+v", albums)
	}
	var shop struct{ Items []struct{ Text string } }
	p.as("jones", "GET", "/api/shopping", nil, &shop)
	if len(shop.Items) != 0 {
		t.Errorf("the Joneses' shopping list: %+v", shop)
	}
	// someone asking for the other household by its number just gets their own
	p.asIn("jones", morenos, "GET", "/api/fridge", nil, &notes)
	if len(notes) != 0 {
		t.Errorf("jones reached the Morenos' fridge by asking: %+v", notes)
	}
	// and can't change a record of theirs by its id
	if c := p.as("jones", "PUT", "/api/hh/recipe/1", recipe{Title: "Hacked"}, nil); c != 404 {
		t.Errorf("jones changed a Moreno recipe: %d", c)
	}
	// the Joneses' things land in their own household
	p.as("jones", "POST", "/api/fridge", fridgeInput{Kind: "note", Text: "Jones note"}, nil)
	p.as("mom", "GET", "/api/fridge", nil, &notes)
	if len(notes) != 2 || notes[0].Text != "Moreno secret" || notes[1].Text != "Shopping" { // the note and the shopping list
		t.Errorf("the Morenos' fridge: %+v", notes)
	}

	// grandma is in both: she switches
	var me struct {
		Homes     []household
		Household int64
	}
	p.as("grandma", "GET", "/api/me", nil, &me)
	if len(me.Homes) != 2 {
		t.Fatalf("grandma's households: %+v", me.Homes)
	}
	p.asIn("grandma", morenos, "GET", "/api/fridge", nil, &notes)
	if len(notes) != 2 || notes[0].Text != "Moreno secret" {
		t.Errorf("grandma at the Morenos': %+v", notes)
	}
	p.asIn("grandma", joneses, "GET", "/api/fridge", nil, &notes)
	if len(notes) != 1 || notes[0].Text != "Jones note" {
		t.Errorf("grandma at the Joneses': %+v", notes)
	}
	var who []person
	p.asIn("grandma", joneses, "GET", "/api/users?household=1", nil, &who)
	if len(who) != 1 || who[0].Name != "jones" {
		t.Errorf("people in the Joneses' household: %+v", who)
	}

	// someone new isn't in any household: their household pages say so, and the admins hear about them
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM household_members WHERE user = 'newbie'`).Scan(&n)
	if n != 0 {
		t.Error("newbie is in a household")
	}
	if c := p.as("newbie", "POST", "/api/fridge", fridgeInput{Kind: "note", Text: "hi"}, nil); c == 201 {
		var home int64
		p.s.db.QueryRow(`SELECT household FROM fridge WHERE text = 'hi'`).Scan(&home)
		if home != 0 {
			t.Errorf("newbie's note landed in household %d", home)
		}
	}
	p.as("newbie", "GET", "/api/fridge", nil, &notes)
	for _, x := range notes {
		if x.Text != "hi" {
			t.Errorf("newbie sees %q", x.Text)
		}
	}
	if c := p.as("newbie", "GET", "/api/shopping", nil, nil); c != 409 {
		t.Errorf("newbie's shopping: %d", c)
	}

	// an empty household can go; one with people can't
	if c := p.as("mom", "DELETE", fmt.Sprintf("/api/admin/households/%d", joneses), nil, nil); c != 409 {
		t.Errorf("deleted a household with people: %d", c)
	}
	// an empty one can, even after someone opened its chat
	var empty struct{ ID int64 }
	p.as("mom", "POST", "/api/admin/households", map[string]string{"name": "Spare"}, &empty)
	p.as("mom", "POST", fmt.Sprintf("/api/admin/households/%d/members", empty.ID), map[string]any{"user": "mom", "in": true}, nil)
	p.asIn("mom", empty.ID, "GET", "/api/chat", nil, nil)
	p.as("mom", "POST", fmt.Sprintf("/api/admin/households/%d/members", empty.ID), map[string]any{"user": "mom", "in": false}, nil)
	if c := p.as("mom", "DELETE", fmt.Sprintf("/api/admin/households/%d", empty.ID), nil, nil); c != 200 {
		t.Errorf("removing an empty household: %d", c)
	}
}

func TestSharingOverview(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	p.s.writeFile("alice", "/Taxes/2026.pdf", strings.NewReader("x"), false, nil0())
	p.as("alice", "POST", "/api/user-shares", map[string]any{"path": "/Taxes", "user": "bob", "edit": true}, nil)
	p.as("alice", "POST", "/api/shares", map[string]any{"path": "/Taxes/2026.pdf"}, nil)
	var out struct{ Mine, WithMe []shareEntry }
	p.as("alice", "GET", "/api/sharing", nil, &out)
	kinds := map[string]shareEntry{}
	for _, e := range out.Mine {
		kinds[e.Kind] = e
	}
	if kinds["folder"].With != "Bob" || !kinds["folder"].Edit || kinds["link"].With != "Anyone with the link" || kinds["folder"].Stop == "" || kinds["link"].Stop == "" {
		t.Errorf("alice's sharing: %+v", out.Mine)
	}
	p.as("bob", "GET", "/api/sharing", nil, &out)
	if len(out.WithMe) != 1 || out.WithMe[0].With != "Alice" || out.WithMe[0].Name != "Taxes" {
		t.Errorf("shared with bob: %+v", out.WithMe)
	}
	// stopping from the overview works
	p.as("alice", "GET", "/api/sharing", nil, &out)
	for _, e := range out.Mine {
		if c := p.as("alice", "DELETE", e.Stop, nil, nil); c != 200 {
			t.Errorf("stopping %s: %d", e.Stop, c)
		}
	}
	p.as("alice", "GET", "/api/sharing", nil, &out)
	if len(out.Mine) != 0 {
		t.Errorf("still shared: %+v", out.Mine)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func decodeJSON(r io.Reader, out any) { json.NewDecoder(r).Decode(out) }

func nil0() time.Time { return time.Time{} }
