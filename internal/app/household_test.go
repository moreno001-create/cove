package app

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMealsChoresKids(t *testing.T) {
	p := newPeople(t, nil, "alice", "sam")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'alice'`)
	// a kid's account: no money, no records, no public links, chores are read-only
	if c := p.as("alice", "PUT", "/api/admin/users/sam/kid", map[string]bool{"kid": true}, nil); c != 200 {
		t.Fatalf("making sam a kid: %d", c)
	}
	if c := p.as("sam", "GET", "/api/hh/bill", nil, nil); c != 403 {
		t.Errorf("a kid saw the bills: %d", c)
	}
	if c := p.as("sam", "POST", "/api/shares", map[string]string{"path": "/"}, nil); c != 403 {
		t.Errorf("a kid made a public link: %d", c)
	}
	if c := p.as("sam", "POST", "/api/hh/chore", chore{Title: "Nothing"}, nil); c != 403 {
		t.Errorf("a kid made a chore: %d", c)
	}

	// a recipe, a meal, and the week's ingredients onto the fridge
	var rc map[string]any
	if c := p.as("alice", "POST", "/api/hh/recipe", recipe{Title: "Tacos", Ingredients: []string{"Tortillas", "Beans", "Cheese"}}, &rc); c != 201 {
		t.Fatalf("recipe: %d", c)
	}
	id := int64(rc["id"].(float64))
	today := time.Now().Format("2006-01-02")
	if c := p.as("alice", "POST", "/api/hh/meal", meal{Date: today, Slot: "dinner", Recipe: id}, nil); c != 201 {
		t.Fatalf("meal: %d", c)
	}
	p.as("alice", "POST", "/api/shopping", map[string][]string{"items": {"cheese", "Milk"}}, nil)
	var added struct{ Added int }
	p.as("alice", "POST", "/api/meals/shopping", map[string]string{"from": today, "to": today}, &added)
	if added.Added != 2 { // cheese is on it already
		t.Errorf("added %d to the shopping list, want 2 (tortillas, beans)", added.Added)
	}
	var notes []fridgeNote
	p.as("sam", "GET", "/api/fridge", nil, &notes)
	if len(notes) != 1 || len(notes[0].Items) != 4 || notes[0].Text != "Shopping" {
		t.Errorf("shopping list: %+v", notes)
	}
	var meals []map[string]any
	p.as("sam", "GET", "/api/hh/meal?from="+today+"&to="+today, nil, &meals)
	if len(meals) != 1 {
		t.Errorf("meals today: %v", meals)
	}

	// a daily chore for sam with points and allowance; sam ticks it, alice pays
	var ch map[string]any
	p.as("alice", "POST", "/api/hh/chore", chore{Title: "Feed the cat", Who: []string{"sam"}, Repeat: "daily", Points: 2, Cents: 50}, &ch)
	cid := int64(ch["id"].(float64))
	if c := p.as("sam", "POST", fmt.Sprintf("/api/chores/%d/done", cid), nil, nil); c != 201 {
		t.Fatalf("sam ticking the chore: %d", c)
	}
	var board struct {
		Chores []choreView
		People []choreScore
	}
	p.as("alice", "GET", "/api/chores", nil, &board)
	if len(board.Chores) != 1 || board.Chores[0].DoneBy != "sam" || !board.Chores[0].DueToday {
		t.Errorf("chores: %+v", board.Chores)
	}
	p.as("alice", "POST", "/api/allowance", map[string]any{"user": "sam", "cents": 20}, nil)
	p.as("alice", "GET", "/api/chores", nil, &board)
	for _, x := range board.People {
		if x.Name == "sam" && (x.Week != 2 || x.Earned != 50 || x.Balance != 30 || x.Streak != 1 || !x.Kid) {
			t.Errorf("sam's score: %+v", x)
		}
	}
	if c := p.as("sam", "POST", "/api/allowance", map[string]any{"user": "sam", "cents": 1000}, nil); c != 403 {
		t.Errorf("a kid paid themselves: %d", c)
	}
	// rotating: two people take turns by day
	c := chore{Who: []string{"a", "b"}, Rotate: true, Repeat: "daily"}
	d := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	if c.whoOn(d)[0] == c.whoOn(d.AddDate(0, 0, 1))[0] {
		t.Error("the turn didn't change the next day")
	}
	w := chore{Repeat: "weekly:1"}
	if !w.dueOn(d) || w.dueOn(d.AddDate(0, 0, 1)) { // Nov 9 2026 is a Monday
		t.Error("a Monday chore")
	}
}

func TestBudgetRecordsTrips(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	month := time.Now().Format("2006-01")
	p.as("alice", "POST", "/api/hh/bill", bill{Name: "Power", Amount: 120, Day: 15, Payer: "alice", Category: "Utilities"}, nil)
	p.as("alice", "POST", "/api/hh/bill", bill{Name: "Car insurance", Amount: 600, Repeat: "yearly", Month: int(time.Now().Month())%12 + 1, Day: 1}, nil)
	p.as("alice", "POST", "/api/hh/expense", expense{Title: "Groceries", Amount: 90, PaidBy: "alice", Split: []string{"alice", "bob"}, Category: "Food"}, nil)
	p.as("bob", "POST", "/api/hh/expense", expense{Title: "Pizza", Amount: 30, PaidBy: "bob", Split: []string{"alice", "bob"}, Category: "Food"}, nil)
	var b struct {
		Bills      []map[string]any
		Spent      float64
		ByCategory map[string]float64
		Settle     []struct {
			From, To string
			Amount   float64
		}
	}
	p.as("bob", "GET", "/api/budget?month="+month, nil, &b)
	if len(b.Bills) != 1 || b.Bills[0]["isPaid"] != false {
		t.Errorf("bills this month (insurance is next month): %v", b.Bills)
	}
	if b.Spent != 120 || b.ByCategory["Food"] != 120 || b.ByCategory["Utilities"] != 120 {
		t.Errorf("spent %v by category %v", b.Spent, b.ByCategory)
	}
	if len(b.Settle) != 1 || b.Settle[0].From != "bob" || b.Settle[0].To != "alice" || b.Settle[0].Amount != 30 {
		t.Errorf("settling up: %+v", b.Settle)
	}
	// paying the bill
	bl := b.Bills[0]
	bl["paid"] = []string{month}
	p.as("alice", "PUT", fmt.Sprintf("/api/hh/bill/%d", int64(bl["id"].(float64))), bl, nil)
	p.as("bob", "GET", "/api/budget?month="+month, nil, &b)
	if b.Bills[0]["isPaid"] != true {
		t.Error("the bill isn't paid")
	}

	// a private record only its owner sees; a warranty reminder
	soon := time.Now().AddDate(0, 0, 14).Format("2006-01-02")
	p.as("alice", "POST", "/api/hh/record", map[string]any{"name": "Passport", "person": "alice", "expires": soon, "private": true}, nil)
	var recs []map[string]any
	p.as("bob", "GET", "/api/hh/record", nil, &recs)
	if len(recs) != 0 {
		t.Errorf("bob sees alice's private record: %v", recs)
	}
	at := time.Now().AddDate(0, 0, 0)
	day := time.Date(at.Year(), at.Month(), at.Day(), 9, 0, 0, 0, time.UTC)
	p.s.householdReminders(day.Add(-time.Minute), day.Add(time.Minute))
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'alice' AND text LIKE 'Passport: expires in 14 days'`).Scan(&n)
	if n != 1 {
		t.Error("no reminder that the passport expires in 14 days")
	}

	// a trip with bob: he hears about it, sees it, ticks packing
	var tr map[string]any
	p.as("alice", "POST", "/api/hh/trip", trip{Name: "Maui", Start: soon, End: soon, People: []string{"bob"},
		Plan: []tripStop{{Date: soon, Time: "10:00", Title: "Flight", Ref: "ABC123"}}, Packing: []packItem{{Text: "Sunscreen"}}}, &tr)
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'bob' AND text LIKE '%trip: Maui'`).Scan(&n)
	if n != 1 {
		t.Error("bob didn't hear about the trip")
	}
	tr["packing"] = []packItem{{Text: "Sunscreen", Done: true}}
	if c := p.as("bob", "PUT", fmt.Sprintf("/api/hh/trip/%d", int64(tr["id"].(float64))), tr, nil); c != 200 {
		t.Errorf("bob ticking the packing list: %d", c)
	}
	if c := p.as("bob", "POST", "/api/hh/trip", trip{Name: "No dates"}, nil); c != 400 {
		t.Errorf("a trip without dates: %d", c)
	}
}

func TestPollsCountdownsChat(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	var poll struct{ ID int64 }
	p.as("alice", "POST", "/api/fridge", fridgeInput{Kind: "poll", Text: "Friday dinner?", Items: []fridgeItem{{Text: "Pizza"}, {Text: "Tacos"}}}, &poll)
	p.as("bob", "POST", fmt.Sprintf("/api/fridge/%d/vote", poll.ID), map[string]int{"option": 1}, nil)
	p.as("alice", "POST", fmt.Sprintf("/api/fridge/%d/vote", poll.ID), map[string]int{"option": 1}, nil)
	var items []fridgeItem
	p.as("alice", "POST", fmt.Sprintf("/api/fridge/%d/vote", poll.ID), map[string]int{"option": 0}, &items) // changes her mind
	if len(items[0].Votes) != 1 || len(items[1].Votes) != 1 || items[1].Votes[0] != "bob" {
		t.Errorf("votes: %+v", items)
	}
	// the author's edit keeps the votes
	p.as("alice", "PUT", fmt.Sprintf("/api/fridge/%d", poll.ID), fridgeInput{Kind: "poll", Text: "Friday dinner!", Items: []fridgeItem{{Text: "Pizza"}, {Text: "Tacos"}, {Text: "Sushi"}}}, nil)
	var notes []fridgeNote
	p.as("bob", "GET", "/api/fridge", nil, &notes)
	if len(notes[0].Items) != 3 || len(notes[0].Items[1].Votes) != 1 {
		t.Errorf("after the edit: %+v", notes[0].Items)
	}
	day := time.Now().AddDate(0, 0, 10).Format("2006-01-02")
	if c := p.as("alice", "POST", "/api/fridge", fridgeInput{Kind: "countdown", Text: "Beach trip", Date: day}, nil); c != 201 {
		t.Fatalf("countdown: %d", c)
	}
	p.as("bob", "GET", "/api/fridge", nil, &notes)
	found := false
	for _, n := range notes {
		found = found || n.Kind == "countdown" && n.Date == day && n.Expires > time.Now().AddDate(0, 0, 10).Unix()
	}
	if !found {
		t.Errorf("countdown: %+v", notes)
	}

	// chat: the Family room, a private room, unread counts
	var rooms []chatRoom
	p.as("alice", "GET", "/api/chat", nil, &rooms)
	if len(rooms) != 1 || rooms[0].Name != "Family" {
		t.Fatalf("rooms: %+v", rooms)
	}
	p.as("alice", "POST", fmt.Sprintf("/api/chat/%d/messages", rooms[0].ID), map[string]string{"text": "Dinner at 6"}, nil)
	var room chatRoom
	p.as("alice", "POST", "/api/chat", map[string]any{"name": "Just me", "members": []string{"alice"}}, &room)
	p.as("bob", "GET", "/api/chat", nil, &rooms)
	if len(rooms) != 1 || rooms[0].Unread != 1 || rooms[0].Last != "Dinner at 6" {
		t.Errorf("bob's rooms: %+v", rooms)
	}
	if c := p.as("bob", "GET", fmt.Sprintf("/api/chat/%d/messages", room.ID), nil, nil); c != 404 {
		t.Errorf("bob read a room he isn't in: %d", c)
	}
	var msgs struct{ Messages []chatMessage }
	p.as("bob", "GET", fmt.Sprintf("/api/chat/%d/messages", rooms[0].ID), nil, &msgs)
	p.as("bob", "GET", "/api/chat", nil, &rooms)
	if len(msgs.Messages) != 1 || msgs.Messages[0].Who != "Alice" || rooms[0].Unread != 0 {
		t.Errorf("messages %+v, unread %d", msgs.Messages, rooms[0].Unread)
	}
	if c := p.as("bob", "DELETE", fmt.Sprintf("/api/chat-messages/%d", msgs.Messages[0].ID), nil, nil); c != 403 {
		t.Errorf("bob deleted alice's message: %d", c)
	}
}

// exifJPEG makes a JPEG with EXIF: when it was taken, and where.
func exifJPEG(t *testing.T, when string, lat, lon float64) []byte {
	var img bytes.Buffer
	jpeg.Encode(&img, image.NewGray(image.Rect(0, 0, 8, 8)), nil)
	bo := binary.LittleEndian
	var tf bytes.Buffer
	tf.WriteString("II")
	binary.Write(&tf, bo, uint16(42))
	binary.Write(&tf, bo, uint32(8))
	// IFD0 at 8: 2 entries (Exif pointer, GPS pointer)
	ifd0 := 8
	exifIFD := ifd0 + 2 + 2*12 + 4
	exifData := exifIFD + 2 + 12 + 4
	gpsIFD := exifData + 20
	gpsData := gpsIFD + 2 + 4*12 + 4
	entry := func(tag, typ uint16, count, value uint32) {
		binary.Write(&tf, bo, tag)
		binary.Write(&tf, bo, typ)
		binary.Write(&tf, bo, count)
		binary.Write(&tf, bo, value)
	}
	binary.Write(&tf, bo, uint16(2))
	entry(0x8769, 4, 1, uint32(exifIFD))
	entry(0x8825, 4, 1, uint32(gpsIFD))
	binary.Write(&tf, bo, uint32(0))
	binary.Write(&tf, bo, uint16(1))
	entry(0x9003, 2, 20, uint32(exifData))
	binary.Write(&tf, bo, uint32(0))
	tf.WriteString(when + "\x00")
	binary.Write(&tf, bo, uint16(4))
	entry(1, 2, 2, uint32('N'))
	entry(2, 5, 3, uint32(gpsData))
	entry(3, 2, 2, uint32('W'))
	entry(4, 5, 3, uint32(gpsData+24))
	binary.Write(&tf, bo, uint32(0))
	for _, v := range []float64{lat, lon} {
		deg := uint32(v)
		min := uint32((v - float64(deg)) * 60 * 1000)
		for _, r := range [][2]uint32{{deg, 1}, {min, 1000}, {0, 1}} {
			binary.Write(&tf, bo, r[0])
			binary.Write(&tf, bo, r[1])
		}
	}
	app1 := append([]byte("Exif\x00\x00"), tf.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)}
	out = append(out, app1...)
	return append(out, img.Bytes()[2:]...)
}

func TestPhotosAlbumsScan(t *testing.T) {
	p := newPeople(t, nil, "alice", "bob")
	pic := exifJPEG(t, "2024:07:04 18:30:00", 21.5, 157.75)
	tm, lat, lon := exifInfo(bytes.NewReader(pic))
	if tm.Format("2006-01-02 15:04") != "2024-07-04 18:30" || lat < 21.49 || lat > 21.51 || lon > -157.74 || lon < -157.76 {
		t.Fatalf("EXIF read %v %v %v", tm, lat, lon)
	}
	p.s.writeFile("alice", "/Photos/2024/beach.jpg", bytes.NewReader(pic), false, time.Time{})
	p.s.writeFile("alice", "/Photos/new.png", strings.NewReader("not really"), false, time.Time{})
	var photos []photoMeta
	p.as("alice", "GET", "/api/photos", nil, &photos)
	if len(photos) != 2 || photos[1].Name != "beach.jpg" || photos[1].Lat == 0 {
		t.Fatalf("photos: %+v", photos)
	}
	var have []bool
	p.as("alice", "POST", "/api/photos/have", map[string]any{"files": []map[string]any{{"name": "BEACH.jpg", "size": len(pic)}, {"name": "beach.jpg", "size": 1}}}, &have)
	if !have[0] || have[1] {
		t.Errorf("backed up already: %v", have)
	}
	// a shared album: bob adds his own photo, sees alice's
	var al struct{ ID int64 }
	p.as("alice", "POST", "/api/albums", map[string]any{"name": "Summer", "shared": true}, &al)
	p.as("alice", "POST", fmt.Sprintf("/api/albums/%d/items", al.ID), map[string][]string{"paths": {"/Photos/2024/beach.jpg", "/nope.jpg"}}, nil)
	p.s.writeFile("bob", "/me.jpg", bytes.NewReader(pic), false, time.Time{})
	p.as("bob", "POST", fmt.Sprintf("/api/albums/%d/items", al.ID), map[string][]string{"paths": {"/me.jpg"}}, nil)
	var items struct{ Items []albumItem }
	p.as("bob", "GET", fmt.Sprintf("/api/albums/%d", al.ID), nil, &items)
	if len(items.Items) != 2 {
		t.Fatalf("album: %+v", items)
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/albums/%d/photo?owner=alice&path=/Photos/2024/beach.jpg", p.srv.URL, al.ID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["bob"]})
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Errorf("bob seeing alice's album photo: %v", res.StatusCode)
	}
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/albums/%d/photo?owner=alice&path=/Photos/new.png", p.srv.URL, al.ID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["bob"]})
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 404 {
		t.Errorf("bob saw a photo not in the album: %d", res.StatusCode)
	}

	// scanning: two pages become a PDF
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i := 0; i < 2; i++ {
		var page bytes.Buffer
		img := image.NewRGBA(image.Rect(0, 0, 85, 110))
		for x := 0; x < 85; x++ {
			img.Set(x, 50, color.Black)
		}
		jpeg.Encode(&page, img, nil)
		fw, _ := mw.CreateFormFile("page", fmt.Sprintf("p%d.jpg", i))
		fw.Write(page.Bytes())
	}
	mw.Close()
	req, _ = http.NewRequest("POST", p.srv.URL+"/api/scan?path=/Scans/Receipt", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Cove", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["alice"]})
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 201 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("scan: %d %s", res.StatusCode, b)
	}
	pdf, err := p.s.data.ReadFile("files/alice/Scans/Receipt.pdf")
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || bytes.Count(pdf, []byte("/Type /Page ")) != 2 || !bytes.Contains(pdf, []byte("/Count 2")) {
		t.Errorf("the PDF: %v %q", err, pdf[:min(200, len(pdf))])
	}
}

// fakeAI is an OpenAI-compatible server: embeddings from words, and answers that repeat what they were told.
func fakeAI(t *testing.T, seen *string) *httptest.Server {
	vec := func(s string) []float32 {
		v := make([]float32, 8)
		for _, w := range strings.Fields(strings.ToLower(s)) {
			for i, group := range [][]string{{"insurance", "coverage", "policy"}, {"car", "vehicle", "auto"}, {"pizza"}, {"dog", "vet"}} {
				for _, k := range group {
					if strings.Contains(w, k) {
						v[i] += 1
					}
				}
			}
		}
		v[7] += 0.01
		return v
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"chat-model"},{"id":"embed-model"}]}`)
		case "/v1/embeddings":
			var in struct{ Input []string }
			json.NewDecoder(r.Body).Decode(&in)
			var out struct {
				Data []map[string]any `json:"data"`
			}
			for i, s := range in.Input {
				out.Data = append(out.Data, map[string]any{"index": i, "embedding": vec(s)})
			}
			json.NewEncoder(w).Encode(out)
		case "/v1/chat/completions":
			var in struct {
				Messages []chatTurn
				Stream   bool
			}
			json.NewDecoder(r.Body).Decode(&in)
			*seen = in.Messages[0].Content
			w.Header().Set("Content-Type", "text/event-stream")
			for _, piece := range []string{"<think>hmm</think>", "The vet ", "is Tuesday."} {
				b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": piece}}}})
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestAskCoveAndSmartSearch(t *testing.T) {
	var seen string
	ai := fakeAI(t, &seen)
	defer ai.Close()
	p := newPeople(t, nil, "alice", "sam")
	p.s.db.Exec(`UPDATE users SET admin = 1 WHERE name = 'alice'`)
	if c := p.as("alice", "POST", "/api/ask", map[string]string{"question": "hi"}, nil); c != 503 {
		t.Errorf("asking before it's set up: %d", c)
	}
	if c := p.as("sam", "PUT", "/api/admin/ai", aiConfig{URL: ai.URL}, nil); c != 403 {
		t.Errorf("sam set up the AI: %d", c)
	}
	var models struct{ Models []string }
	p.as("alice", "GET", "/api/admin/ai/models?url="+ai.URL, nil, &models)
	if len(models.Models) != 2 {
		t.Errorf("models: %v", models)
	}
	p.as("alice", "PUT", "/api/admin/ai", aiConfig{URL: ai.URL, Model: "chat-model", Embed: "embed-model"}, nil)
	if c := p.s.aiConfig(); c.URL != ai.URL+"/v1" || !c.on() {
		t.Fatalf("config: %+v", c)
	}
	p.s.writeFile("alice", "/Papers/Auto policy.txt", strings.NewReader("Your car insurance policy number is 55-123. It renews in March."), false, time.Time{})
	p.s.writeFile("alice", "/Papers/Menu.txt", strings.NewReader("Pizza pizza pizza"), false, time.Time{})
	p.s.indexUser("alice", 0)
	p.s.embedPending("alice", time.Minute)
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM embeddings WHERE owner = 'alice'`).Scan(&n)
	if n != 2 {
		t.Fatalf("embeddings: %d", n)
	}
	// "car insurance" finds the policy by meaning, though no file is named that
	var found struct {
		Files   []fileHit
		Related []fileHit
	}
	p.as("alice", "GET", "/api/search?q=vehicle+coverage", nil, &found)
	if len(found.Related) == 0 || found.Related[0].Name != "Auto policy.txt" {
		t.Errorf("smart search: %+v", found.Related)
	}

	// asking: the model hears about the calendar and the document, and the answer streams without its thinking
	p.as("alice", "POST", "/api/hh/chore", chore{Title: "Walk the dog", Repeat: "daily"}, nil)
	req, _ := http.NewRequest("POST", p.srv.URL+"/api/ask", strings.NewReader(`{"question":"what is my car insurance policy number?"}`))
	req.Header.Set("X-Cove", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["alice"]})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	stream := string(b)
	if !strings.Contains(stream, `"sources":[{"path":"/Papers/Auto policy.txt"`) || !strings.Contains(stream, `"text":"The vet "`) ||
		strings.Contains(stream, "hmm") || !strings.Contains(stream, `"done":true`) {
		t.Errorf("stream: %s", stream)
	}
	if !strings.Contains(seen, "- /Papers/Menu.txt (text") || !strings.Contains(seen, "Begins: Pizza pizza pizza") || !strings.Contains(seen, "files (2 in all)") {
		t.Errorf("the model wasn't told what files there are: %s", seen)
	}
	if !strings.Contains(seen, "55-123") || !strings.Contains(seen, "Walk the dog") || !strings.Contains(seen, "Alice") {
		t.Errorf("what the model was told: %s", seen)
	}
	// a kid's question doesn't carry the household's money or records
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name = 'sam'`)
	p.as("alice", "POST", "/api/hh/bill", bill{Name: "Mortgage", Amount: 2000, Day: 1}, nil)
	req, _ = http.NewRequest("POST", p.srv.URL+"/api/ask", strings.NewReader(`{"question":"what bills are there?"}`))
	req.Header.Set("X-Cove", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["sam"]})
	res, _ = http.DefaultClient.Do(req)
	io.ReadAll(res.Body)
	if strings.Contains(seen, "Mortgage") {
		t.Error("a kid's question was told about the bills")
	}
}

func TestChunks(t *testing.T) {
	text := strings.Repeat("word ", 1000)
	c := chunks(text)
	if len(c) < 5 || len(c[0]) > 1000 {
		t.Errorf("%d chunks, first %d long", len(c), len(c[0]))
	}
}
