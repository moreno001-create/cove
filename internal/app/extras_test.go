package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReceiptGuess(t *testing.T) {
	store, total := receiptGuess("TRADER JOE'S #123\nBANANAS 1.99\nSUBTOTAL 41.20\nTAX 2.10\nTOTAL $43.30\nVISA 43.30")
	if store != "TRADER JOE'S #123" || total != 43.30 {
		t.Errorf("%q %v", store, total)
	}
}

func TestRecipeFromAPage(t *testing.T) {
	page := `<html><head><script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},
		{"@type":["Recipe"],"name":"Banana Bread","recipeYield":["8","8 slices"],"totalTime":"PT1H10M",
		"recipeIngredient":["3 ripe bananas","2 cups flour","1 tsp baking soda"],
		"recipeInstructions":[{"@type":"HowToStep","text":"Mash the bananas."},{"@type":"HowToStep","text":"Mix &amp; bake at 350°F."}]}]}</script></head></html>`
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, page) }))
	defer site.Close()
	old := recipeClient
	recipeClient = http.DefaultClient
	defer func() { recipeClient = old }()
	p := newPeople(t, nil, "alice")
	var rec recipe
	if c := p.as("alice", "POST", "/api/recipes/import", map[string]string{"url": site.URL + "/banana"}, &rec); c != 200 {
		t.Fatalf("import: %d", c)
	}
	if rec.Title != "Banana Bread" || rec.Serves != 8 || rec.Minutes != 70 || len(rec.Ingredients) != 3 || !strings.Contains(rec.Steps, "2. Mix & bake at 350°F.") || rec.Link == "" {
		t.Errorf("recipe: %+v", rec)
	}
	if c := p.as("alice", "POST", "/api/recipes/import", map[string]string{"url": "javascript:alert(1)"}, nil); c != 400 {
		t.Errorf("a non-web link: %d", c)
	}
}

func TestRewardsShop(t *testing.T) {
	p := newPeople(t, nil, "mom", "sam")
	p.s.db.Exec(`UPDATE users SET kid = 1 WHERE name = 'sam'`)
	var ch, rw map[string]any
	p.as("mom", "POST", "/api/hh/chore", chore{Title: "Dishes", Who: []string{"sam"}, Repeat: "daily", Points: 5}, &ch)
	if c := p.as("sam", "POST", "/api/hh/reward", reward{Title: "Free candy", Points: 1}, nil); c != 403 {
		t.Errorf("a kid made a reward: %d", c)
	}
	p.as("mom", "POST", "/api/hh/reward", reward{Title: "30 min screen time", Points: 4, Emoji: "📱"}, &rw)
	if c := p.as("sam", "POST", fmt.Sprintf("/api/rewards/%d/claim", int64(rw["id"].(float64))), nil, nil); c != 400 {
		t.Errorf("claimed without points: %d", c)
	}
	p.as("sam", "POST", fmt.Sprintf("/api/chores/%d/done", int64(ch["id"].(float64))), nil, nil)
	if c := p.as("sam", "POST", fmt.Sprintf("/api/rewards/%d/claim", int64(rw["id"].(float64))), nil, nil); c != 201 {
		t.Fatalf("claim: %d", c)
	}
	var shop struct {
		Mine   int
		Claims []claim
	}
	p.as("sam", "GET", "/api/rewards", nil, &shop)
	if shop.Mine != 1 || len(shop.Claims) != 1 || shop.Claims[0].Status != "waiting" {
		t.Errorf("after claiming: %+v", shop)
	}
	if c := p.as("sam", "POST", fmt.Sprintf("/api/reward-claims/%d", shop.Claims[0].ID), map[string]bool{"yes": true}, nil); c != 403 {
		t.Errorf("a kid said yes to themselves: %d", c)
	}
	p.as("mom", "POST", fmt.Sprintf("/api/reward-claims/%d", shop.Claims[0].ID), map[string]bool{"yes": false}, nil)
	p.as("sam", "GET", "/api/rewards", nil, &shop)
	if shop.Mine != 5 || shop.Claims[0].Status != "no" {
		t.Errorf("after a no, the points come back: %+v", shop)
	}
	var n int
	p.s.db.QueryRow(`SELECT count(*) FROM notifications WHERE user = 'mom' AND text LIKE 'Sam wants a reward%'`).Scan(&n)
	if n != 1 {
		t.Error("mom didn't hear about the claim")
	}
}

func TestUpkeepAndWifi(t *testing.T) {
	x := upkeep{Title: "Oil change", EveryDays: 180, EveryMiles: 5000, History: []upkeepDone{{Date: "2026-01-10", Miles: 40000}, {Date: "2026-05-02", Miles: 44100}}}
	x.clean()
	if day, miles := x.due(); day != "2026-10-29" || miles != 49100 {
		t.Errorf("due %s at %d", day, miles)
	}
	if got := wifiQR(`Home;Guest`, `p@ss:"1"`, ""); got != `WIFI:T:WPA;S:Home\;Guest;P:p@ss\:\"1\";;` {
		t.Errorf("QR text %q", got)
	}
	p := newPeople(t, nil, "alice")
	var n struct{ ID int64 }
	if c := p.as("alice", "POST", "/api/fridge", fridgeInput{Kind: "wifi", Text: "Guest", Items: []fridgeItem{{Text: "secret123"}, {Text: "extra"}}}, &n); c != 201 {
		t.Fatalf("wifi card: %d", c)
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/fridge/%d/wifi", p.srv.URL, n.ID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.cookie["alice"]})
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("QR image: %v %v", err, res)
	}
}

func TestTakeoutImport(t *testing.T) {
	p := newPeople(t, nil, "alice")
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	pic := exifJPEG(t, "2020:01:01 10:00:00", 0, 0) // EXIF says 2020, but Google's sidecar wins
	w1, _ := zw.Create("Takeout/Google Photos/Trip/IMG_1.JPG")
	w1.Write(pic)
	w2, _ := zw.Create("Takeout/Google Photos/Trip/IMG_1.JPG.supplemental-metadata.json")
	ts := time.Date(2019, 7, 4, 12, 0, 0, 0, time.UTC).Unix()
	fmt.Fprintf(w2, `{"title":"IMG_1.JPG","photoTakenTime":{"timestamp":"%d"},"geoData":{"latitude":20.8,"longitude":-156.3}}`, ts)
	w3, _ := zw.Create("Takeout/Google Photos/Trip/notes.txt")
	io.WriteString(w3, "not a photo")
	zw.Close()
	p.s.writeFile("alice", "/takeout.zip", bytes.NewReader(zb.Bytes()), false, time.Time{})
	added, skipped, err := p.s.importZip(user{Name: "alice"}, "/takeout.zip")
	if err != nil || added != 1 || skipped != 0 {
		t.Fatalf("added %d skipped %d: %v", added, skipped, err)
	}
	var photos []photoMeta
	p.as("alice", "GET", "/api/photos", nil, &photos)
	if len(photos) != 1 || photos[0].Path != "/Photos/2019/07/IMG_1.JPG" || photos[0].Taken != ts || photos[0].Lat != 20.8 {
		t.Errorf("photos: %+v", photos)
	}
	if added, skipped, _ = p.s.importZip(user{Name: "alice"}, "/takeout.zip"); added != 0 || skipped != 1 {
		t.Errorf("again: added %d skipped %d", added, skipped)
	}
}

func TestWhoIsHome(t *testing.T) {
	got := whoIsHome(map[string]haState{
		"person.bob":    {State: "not_home", Attributes: map[string]any{"friendly_name": "Bob"}, LastChanged: "2026-11-09T08:00:00.123456+00:00"},
		"person.alice":  {State: "home", Attributes: map[string]any{"friendly_name": "Alice"}},
		"light.kitchen": {State: "on"},
	})
	if len(got) != 2 || got[0].Name != "Alice" || got[1].State != "not_home" || got[1].Since == 0 {
		t.Errorf("%+v", got)
	}
}
