package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
)

// Smaller helpers that make the rest faster to use: a receipt photo becomes spending, a scanned letter gets sorted,
// a recipe comes in from its web page, who's home (Home Assistant), the kids' rewards shop, home and car
// maintenance, a guest Wi-Fi card for the fridge, and photos brought in from a Google Takeout or iCloud export.

const extrasSchema = `
CREATE TABLE IF NOT EXISTS reward_claims (
	id INTEGER PRIMARY KEY,
	reward INTEGER NOT NULL,
	title TEXT NOT NULL,
	user TEXT NOT NULL,
	points INTEGER NOT NULL,
	at INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'waiting', -- waiting, yes, no
	by TEXT NOT NULL DEFAULT ''
);`

// aiJSON asks the home AI for an answer as JSON, into out.
func (s *Server) aiJSON(ctx context.Context, system, text string, out any) error {
	c := s.aiConfig()
	if !c.on() {
		return errors.New("the home AI isn't set up (Admin → AI)")
	}
	res, err := s.aiPost(ctx, c, "/chat/completions", map[string]any{"model": c.Model, "temperature": 0, "stream": false,
		"messages": []map[string]string{{"role": "system", "content": system + " Answer with one JSON object and nothing else."}, {"role": "user", "content": text}}})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&r); err != nil || len(r.Choices) == 0 {
		return errors.New("the AI didn't answer")
	}
	body := r.Choices[0].Message.Content
	if _, after, ok := strings.Cut(body, "</think>"); ok {
		body = after
	}
	i, j := strings.Index(body, "{"), strings.LastIndex(body, "}")
	if i < 0 || j < i {
		return errors.New("the AI's answer wasn't JSON")
	}
	return json.Unmarshal([]byte(body[i:j+1]), out)
}

// readImageText reads the words in an uploaded picture or PDF (a temporary file).
func readImageText(name string, b []byte) string {
	f, err := os.CreateTemp("", "cove-read-*"+path.Ext(name))
	if err != nil {
		return ""
	}
	defer os.Remove(f.Name())
	f.Write(b)
	f.Close()
	if strings.EqualFold(path.Ext(name), ".pdf") {
		if t := pdfToolText(f.Name()); meaningful(t) {
			return t
		}
		return ocrPDF(f.Name())
	}
	return ocrImage(f.Name())
}

var moneyRe = regexp.MustCompile(`(?i)(total|amount due|balance due|grand total|to pay)[^0-9\n]{0,20}\$?\s*([0-9]{1,6}[.,][0-9]{2})`)

// receiptGuess is what can be told from a receipt's text without the AI: the total, and the first line as the store.
func receiptGuess(text string) (store string, total float64) {
	for _, line := range strings.Split(text, "\n") {
		if l := strings.TrimSpace(line); len(l) > 2 {
			store = clip(l, 60)
			break
		}
	}
	for _, m := range moneyRe.FindAllStringSubmatch(text, -1) {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", "."), 64); err == nil && v > total {
			total = v
		}
	}
	return
}

// readReceipt keeps a receipt's photo in Receipts/YEAR and reads the store, date, total and kind of spending.
func (s *Server) readReceipt(w http.ResponseWriter, r *http.Request, u user) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 30<<20))
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "application/pdf": ".pdf"}[http.DetectContentType(b)]
	if err != nil || ext == "" {
		jsonError(w, http.StatusBadRequest, "That isn't a photo (JPEG, PNG, WebP) or a PDF.")
		return
	}
	now := time.Now().In(s.location(u.Name))
	p, err := s.writeFile(u.Name, fmt.Sprintf("/Receipts/%d/Receipt %s%s", now.Year(), now.Format("2006-01-02 15.04"), ext), bytes.NewReader(b), false, time.Time{})
	if err != nil {
		jsonError(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	text := readImageText(p, b)
	out := struct {
		Path     string  `json:"path"`
		Store    string  `json:"store"`
		Date     string  `json:"date"`
		Amount   float64 `json:"amount"`
		Category string  `json:"category"`
		Read     bool    `json:"read"` // the words could be read at all
		AI       bool    `json:"ai"`
	}{Path: p, Date: now.Format("2006-01-02"), Read: meaningful(text)}
	out.Store, out.Amount = receiptGuess(text)
	if out.Read && s.aiConfig().on() {
		var got struct {
			Store, Date, Category string
			Total                 float64
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		err := s.aiJSON(ctx, `You read shop receipts. Give {"store": the shop's name, "date": "YYYY-MM-DD" or "", "total": the amount paid as a number, "category": one of Food, Household, Eating out, Car, Health, Kids, Clothes, Home, Fun, Other}.`, clip(text, 8000), &got)
		cancel()
		if err == nil {
			out.AI = true
			if got.Store != "" {
				out.Store = clip(got.Store, 100)
			}
			if validDate(got.Date) {
				out.Date = got.Date
			}
			if got.Total > 0 {
				out.Amount = got.Total
			}
			out.Category = clip(got.Category, 40)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// sortLetter reads a scanned letter or bill (a file in u's own files) and suggests where it goes and what to do.
func (s *Server) sortLetter(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := "/" + cleanRel(in.Path)
	b, err := s.data.ReadFile(s.rel(u.Name, p))
	if err != nil {
		fileError(w, err)
		return
	}
	var text string
	s.db.QueryRow(`SELECT body FROM search_text WHERE owner = ? AND path = ?`, u.Name, strings.TrimPrefix(p, "/")).Scan(&text)
	if !meaningful(text) {
		if kind := kindOf(p, false); kind == "image" || kind == "pdf" {
			text = readImageText(p, b)
		} else {
			text = extractText(path.Base(p), bytes.NewReader(b), int64(len(b)))
		}
	}
	if !meaningful(text) {
		jsonError(w, http.StatusBadRequest, "Cove couldn't read any words in it.")
		return
	}
	var got struct {
		Kind    string  `json:"kind"`
		From    string  `json:"from"`
		Title   string  `json:"title"`
		Summary string  `json:"summary"`
		Due     string  `json:"due"`
		Amount  float64 `json:"amount"`
		Folder  string  `json:"folder"`
		Name    string  `json:"name"`
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	err = s.aiJSON(ctx, `You sort a family's paper mail. From the letter's text, give {"kind": bill, statement, insurance, medical, school, tax, bank, letter or ad,
"from": who sent it, "title": a short title, "summary": one or two plain sentences on what it says and anything to do, "due": "YYYY-MM-DD" when something must be paid or done by (else ""),
"amount": the amount to pay (else 0), "folder": a folder path like "Mail/Bills/Electric" or "Mail/Medical", "name": a file name without extension like "2026-11 Electric bill"}.`,
		clip(text, 12000), &got)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	got.Folder = "/" + cleanRel(got.Folder)
	if got.Folder == "/." || got.Folder == "/" {
		got.Folder = "/Mail"
	}
	got.Name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, clip(got.Name, 80))
	if !validDate(got.Due) {
		got.Due = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "ext": path.Ext(p), "suggestion": got})
}

// --- recipes from a web page

var jsonLDRe = regexp.MustCompile(`(?is)<script[^>]+application/ld\+json[^>]*>(.*?)</script>`)
var tagRe = regexp.MustCompile(`(?s)<[^>]*>`)

func importRecipePage(page []byte) (recipe, bool) {
	var out recipe
	for _, m := range jsonLDRe.FindAllSubmatch(page, -1) {
		var any_ any
		if json.Unmarshal(m[1], &any_) != nil {
			continue
		}
		if rec := findRecipe(any_); rec != nil {
			out.Title = str2(rec["name"])
			for _, i := range list2(rec["recipeIngredient"]) {
				out.Ingredients = append(out.Ingredients, html.UnescapeString(str2(i)))
			}
			var steps []string
			for _, st := range list2(rec["recipeInstructions"]) {
				switch v := st.(type) {
				case string:
					steps = append(steps, v)
				case map[string]any:
					if t := str2(v["text"]); t != "" {
						steps = append(steps, t)
					}
					for _, sub := range list2(v["itemListElement"]) {
						if m, ok := sub.(map[string]any); ok {
							steps = append(steps, str2(m["text"]))
						}
					}
				}
			}
			for i, st := range steps {
				steps[i] = fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(st, ""))))
			}
			out.Steps = strings.Join(steps, "\n")
			if y := list2(rec["recipeYield"]); len(y) > 0 {
				fmt.Sscan(str2(y[0]), &out.Serves)
			}
			out.Minutes = isoMinutes(str2(rec["totalTime"]))
			if out.Minutes == 0 {
				out.Minutes = isoMinutes(str2(rec["cookTime"])) + isoMinutes(str2(rec["prepTime"]))
			}
			for _, k := range list2(rec["recipeCategory"]) {
				out.Tags = append(out.Tags, str2(k))
			}
			return out, out.Title != "" && len(out.Ingredients) > 0
		}
	}
	return out, false
}

func findRecipe(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		for _, t := range list2(x["@type"]) {
			if str2(t) == "Recipe" {
				return x
			}
		}
		if g, ok := x["@graph"]; ok {
			return findRecipe(g)
		}
	case []any:
		for _, e := range x {
			if r := findRecipe(e); r != nil {
				return r
			}
		}
	}
	return nil
}

func list2(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case nil:
		return nil
	}
	return []any{v}
}

func str2(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

var isoDur = regexp.MustCompile(`PT?(?:(\d+)H)?(?:(\d+)M)?`)

func isoMinutes(s string) int {
	m := isoDur.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	return h*60 + mins
}

// recipeClient fetches recipe pages (a test hook): from the internet only.
var recipeClient = outsideClient

func (s *Server) importRecipe(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &in); err != nil || safeLink(in.URL) == "" {
		jsonError(w, http.StatusBadRequest, "Paste the recipe's web address (https://…).")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Cove household app; recipe import)")
	res, err := recipeClient.Do(req)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "Couldn't open that page: "+err.Error())
		return
	}
	defer res.Body.Close()
	page, _ := io.ReadAll(io.LimitReader(res.Body, 5<<20))
	rec, ok := importRecipePage(page)
	if !ok && s.aiConfig().on() { // no recipe data in the page: ask the AI to read it
		text := strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`).ReplaceAllString(string(page), ""), " "))), " ")
		var got struct {
			Title       string   `json:"title"`
			Serves      int      `json:"serves"`
			Minutes     int      `json:"minutes"`
			Ingredients []string `json:"ingredients"`
			Steps       []string `json:"steps"`
		}
		ctx2, cancel2 := context.WithTimeout(r.Context(), 2*time.Minute)
		err := s.aiJSON(ctx2, `You copy recipes from web pages. Give {"title", "serves" (number), "minutes" (number), "ingredients" (list of strings), "steps" (list of strings)}.`, clip(text, 15000), &got)
		cancel2()
		if err == nil && got.Title != "" {
			rec = recipe{Title: got.Title, Serves: got.Serves, Minutes: got.Minutes, Ingredients: got.Ingredients}
			for i, st := range got.Steps {
				rec.Steps += fmt.Sprintf("%d. %s\n", i+1, st)
			}
			ok = true
		}
	}
	if !ok {
		jsonError(w, http.StatusBadRequest, "Cove couldn't find a recipe on that page.")
		return
	}
	rec.Link = in.URL
	rec.clean()
	writeJSON(w, http.StatusOK, rec)
}

// --- who's home (Home Assistant's people)

type homePerson struct {
	Name  string `json:"name"`
	State string `json:"state"` // home, not_home, or a zone's name
	Since int64  `json:"since"`
}

func whoIsHome(states map[string]haState) []homePerson {
	out := []homePerson{}
	for id, st := range states {
		if !strings.HasPrefix(id, "person.") {
			continue
		}
		p := homePerson{State: st.State}
		p.Name, _ = st.Attributes["friendly_name"].(string)
		if p.Name == "" {
			p.Name = strings.TrimPrefix(id, "person.")
		}
		if t, err := time.Parse(time.RFC3339Nano, st.LastChanged); err == nil {
			p.Since = t.Unix()
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b homePerson) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// --- the rewards shop: chore points spent on treats a grown-up says yes to

type reward struct {
	Title  string `json:"title"`
	Points int    `json:"points"`
	Emoji  string `json:"emoji"`
}

func (x *reward) clean() error {
	x.Title, x.Emoji = clip(x.Title, 100), clip(x.Emoji, 16)
	if x.Title == "" || x.Points <= 0 {
		return errors.New("A reward needs a name and a price in points.")
	}
	return nil
}
func (x *reward) label() string { return x.Title }

// points is how many chore points someone has to spend: all earned, less what's spent or waiting for a yes.
// (points are earned and spent in one household)
func (s *Server) points(home int64, name string) int {
	var earned, spent int
	s.db.QueryRow(`SELECT coalesce(sum(d.points), 0) FROM chore_done d JOIN hh h ON h.id = d.chore WHERE d.user = ? AND h.household = ?`, name, home).Scan(&earned)
	s.db.QueryRow(`SELECT coalesce(sum(points), 0) FROM reward_claims WHERE user = ? AND household = ? AND status != 'no'`, name, home).Scan(&spent)
	return earned - spent
}

type claim struct {
	ID     int64  `json:"id"`
	Reward int64  `json:"reward"`
	Title  string `json:"title"`
	User   string `json:"user"`
	Who    string `json:"who"`
	Points int    `json:"points"`
	At     int64  `json:"at"`
	Status string `json:"status"`
	By     string `json:"by"`
}

func (s *Server) rewardsShop(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT id, reward, title, user, points, at, status, by FROM reward_claims WHERE household = ? ORDER BY at DESC LIMIT 100`, u.Home)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	claims := []claim{}
	for rows.Next() {
		var c claim
		if rows.Scan(&c.ID, &c.Reward, &c.Title, &c.User, &c.Points, &c.At, &c.Status, &c.By) == nil && (!u.Kid || c.User == u.Name) {
			claims = append(claims, c)
		}
	}
	rows.Close()
	for i := range claims {
		claims[i].Who = s.displayOf(claims[i].User)
	}
	balances := map[string]int{}
	for _, n := range s.members(u.Home) {
		if !u.Kid || n == u.Name {
			balances[n] = s.points(u.Home, n)
		}
	}
	list := []map[string]any{}
	for _, x := range hhItems[reward](s, u, "reward") {
		list = append(list, s.hhJSON(x.hhRow))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rewards": list, "claims": claims, "points": balances, "mine": s.points(u.Home, u.Name)})
}

func (s *Server) claimReward(w http.ResponseWriter, r *http.Request, u user) {
	row, ok := s.homeRow(u, "reward", r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "That reward is gone.")
		return
	}
	var x reward
	json.Unmarshal(row.Data, &x)
	if have := s.points(u.Home, u.Name); have < x.Points {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("That's %d points; you have %d.", x.Points, have))
		return
	}
	s.db.Exec(`INSERT INTO reward_claims (household, reward, title, user, points, at) VALUES (?, ?, ?, ?, ?, ?)`, u.Home, row.ID, x.Title, u.Name, x.Points, time.Now().Unix())
	for _, a := range s.members(u.Home) {
		if a != u.Name && !s.isKid(a) {
			s.notify(a, fmt.Sprintf("%s wants a reward: %s (%d points)", displayName(u), x.Title, x.Points), "#/chores")
		}
	}
	s.changed("household", s.members(u.Home)...)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) isKid(name string) bool {
	var k bool
	s.db.QueryRow(`SELECT kid FROM users WHERE name = ?`, name).Scan(&k)
	return k
}

func (s *Server) answerClaim(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid {
		jsonError(w, http.StatusForbidden, "A grown-up says yes or no.")
		return
	}
	var in struct {
		Yes bool `json:"yes"`
	}
	readJSON(r, &in)
	var who, title, status string
	if s.db.QueryRow(`SELECT user, title, status FROM reward_claims WHERE id = ? AND household = ?`, r.PathValue("id"), u.Home).Scan(&who, &title, &status) != nil || status != "waiting" {
		jsonError(w, http.StatusNotFound, "That's been answered already.")
		return
	}
	answer := map[bool]string{true: "yes", false: "no"}[in.Yes]
	s.db.Exec(`UPDATE reward_claims SET status = ?, by = ? WHERE id = ?`, answer, displayName(u), r.PathValue("id"))
	if in.Yes {
		s.notify(who, fmt.Sprintf("%s said yes: %s 🎉", displayName(u), title), "#/chores")
	} else {
		s.notify(who, fmt.Sprintf("%s said not this time: %s (your points are back)", displayName(u), title), "#/chores")
	}
	s.changed("household", s.members(u.Home)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- home and car maintenance

type upkeepDone struct {
	Date  string `json:"date"`
	Miles int    `json:"miles"`
	Note  string `json:"note"`
	By    string `json:"by"`
}

type upkeep struct {
	Title      string       `json:"title"`
	Area       string       `json:"area"` // home, car, yard, other
	EveryDays  int          `json:"everyDays"`
	EveryMiles int          `json:"everyMiles"`
	Miles      int          `json:"miles"` // the car's mileage now (as last written down)
	Notes      string       `json:"notes"`
	History    []upkeepDone `json:"history"`
}

func (x *upkeep) clean() error {
	x.Title, x.Area, x.Notes = clip(x.Title, 200), clip(x.Area, 20), clip(x.Notes, 4000)
	if x.Title == "" {
		return errors.New("What needs doing?")
	}
	x.EveryDays, x.EveryMiles, x.Miles = max(x.EveryDays, 0), max(x.EveryMiles, 0), max(x.Miles, 0)
	h := []upkeepDone{}
	for _, d := range x.History {
		if validDate(d.Date) && len(h) < 200 {
			d.Note, d.By = clip(d.Note, 500), clip(d.By, 64)
			h = append(h, d)
		}
	}
	slices.SortFunc(h, func(a, b upkeepDone) int { return strings.Compare(b.Date, a.Date) })
	x.History = h
	return nil
}
func (x *upkeep) label() string { return x.Title }

// due is when it's next due by date ("" without a date rule or a first time), and the mileage it's due at (0: none).
func (x *upkeep) due() (string, int) {
	if len(x.History) == 0 {
		return "", 0
	}
	last := x.History[0]
	var day string
	if x.EveryDays > 0 {
		if t, err := time.Parse("2006-01-02", last.Date); err == nil {
			day = t.AddDate(0, 0, x.EveryDays).Format("2006-01-02")
		}
	}
	miles := 0
	if x.EveryMiles > 0 && last.Miles > 0 {
		miles = last.Miles + x.EveryMiles
	}
	return day, miles
}

func (s *Server) upkeepReminders(from, to time.Time) {
	for _, x := range hhItems[upkeep](s, user{}, "upkeep") {
		day, _ := x.V.due()
		if day == "" {
			continue
		}
		for _, before := range []int{7, 0} {
			d, err := time.ParseInLocation("2006-01-02", day, s.location(x.Owner))
			if err != nil {
				continue
			}
			at := d.AddDate(0, 0, -before).Add(9 * time.Hour)
			if at.After(from) && !at.After(to) && s.once(fmt.Sprintf("upkeep|%d|%s|%d", x.ID, day, before), 3*24*time.Hour) {
				when := "today"
				if before > 0 {
					when = "next week"
				}
				s.notify(x.Owner, fmt.Sprintf("Due %s: %s", when, x.V.Title), "#/upkeep")
			}
		}
	}
}

// --- the guest Wi-Fi card

func wifiQR(ssid, password, security string) string {
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `:`, `\:`, `"`, `\"`)
	if security == "" {
		security = "WPA"
	}
	if password == "" {
		security = "nopass"
	}
	return fmt.Sprintf("WIFI:T:%s;S:%s;P:%s;;", security, esc.Replace(ssid), esc.Replace(password))
}

// fridgeWifi draws the QR code guests scan to join (a fridge note of kind wifi: the text is the network's name,
// the first item its password).
func (s *Server) fridgeWifi(w http.ResponseWriter, r *http.Request, u user) {
	var kind, text, items string
	if s.db.QueryRow(`SELECT kind, text, items FROM fridge WHERE id = ? AND household = ?`, r.PathValue("id"), u.Home).Scan(&kind, &text, &items) != nil || kind != "wifi" {
		http.NotFound(w, r)
		return
	}
	var list []fridgeItem
	json.Unmarshal([]byte(items), &list)
	pass := ""
	if len(list) > 0 {
		pass = list[0].Text
	}
	png, err := qrcode.Encode(wifiQR(text, pass, ""), qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(png)
}

// --- photos from Google Takeout or an iCloud export

type takeoutMeta struct {
	Title     string `json:"title"`
	PhotoTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
	Geo struct {
		Lat float64 `json:"latitude"`
		Lon float64 `json:"longitude"`
	} `json:"geoData"`
}

// importPhotos unpacks the photos and videos in a zip (in u's files) into Photos/YEAR/MONTH, dated by Google's
// sidecar files (photoTakenTime, geoData) or the picture's own EXIF; it runs in the background and tells u when done.
func (s *Server) importPhotos(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil || !strings.HasSuffix(strings.ToLower(in.Path), ".zip") {
		jsonError(w, http.StatusBadRequest, "Pick the .zip from Google Takeout or iCloud.")
		return
	}
	p := "/" + cleanRel(in.Path)
	if _, err := s.data.Stat(s.rel(u.Name, p)); err != nil {
		fileError(w, err)
		return
	}
	go func() {
		n, skipped, err := s.importZip(u, p)
		if err != nil {
			s.notify(u.Name, "Bringing in photos didn't work: "+err.Error(), "#/photos")
			return
		}
		s.notify(u.Name, fmt.Sprintf("Brought in %d photos and videos (%d were there already)", n, skipped), "#/photos")
		s.changed("photos", u.Name)
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) importZip(u user, p string) (added, skipped int, err error) {
	f, err := s.data.Open(s.rel(u.Name, p))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	info, _ := f.Stat()
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return 0, 0, errors.New("that isn't a zip file")
	}
	metas := map[string]takeoutMeta{} // by the media file's path in the zip
	for _, zf := range zr.File {
		if !strings.HasSuffix(strings.ToLower(zf.Name), ".json") || zf.UncompressedSize64 > 1<<20 {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		var m takeoutMeta
		if json.NewDecoder(rc).Decode(&m) == nil && m.PhotoTime.Timestamp != "" {
			dir := path.Dir(zf.Name)
			metas[path.Join(dir, m.Title)] = m
			// Google sometimes shortens the sidecar's name ("IMG_1234.JPG.supplemental-metadata.json")
			base := strings.TrimSuffix(path.Base(zf.Name), ".json")
			if i := strings.Index(strings.ToLower(base), ".supplemental"); i > 0 {
				base = base[:i]
			}
			metas[path.Join(dir, base)] = m
		}
		rc.Close()
	}
	have := map[string]bool{}
	for _, ph := range s.photosOf(u.Name) {
		have[strings.ToLower(ph.Name)+"|"+strconv.FormatInt(ph.Size, 10)] = true
	}
	for _, zf := range zr.File {
		name := path.Base(zf.Name)
		kind := kindOf(name, false)
		if zf.FileInfo().IsDir() || (kind != "image" && kind != "video") || strings.HasPrefix(name, ".") || strings.Contains(zf.Name, "__MACOSX") {
			continue
		}
		if have[strings.ToLower(name)+"|"+strconv.FormatUint(zf.UncompressedSize64, 10)] {
			skipped++
			continue
		}
		if err := s.checkQuota(u.Name, int64(zf.UncompressedSize64)); err != nil {
			return added, skipped, err
		}
		taken := zf.Modified
		var lat, lon float64
		if m, ok := metas[zf.Name]; ok {
			if sec, err := strconv.ParseInt(m.PhotoTime.Timestamp, 10, 64); err == nil && sec > 0 {
				taken = time.Unix(sec, 0)
			}
			lat, lon = m.Geo.Lat, m.Geo.Lon
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		var head bytes.Buffer
		body := io.TeeReader(io.LimitReader(rc, 256<<10), &head)
		buf, _ := io.ReadAll(body)
		if t, la, lo := exifInfo(bytes.NewReader(buf)); !t.IsZero() {
			if _, ok := metas[zf.Name]; !ok {
				taken = t
			}
			if lat == 0 && lon == 0 {
				lat, lon = la, lo
			}
		}
		final, err := s.writeFile(u.Name, fmt.Sprintf("/Photos/%d/%02d/%s", taken.Year(), int(taken.Month()), name), io.MultiReader(bytes.NewReader(buf), rc), false, taken)
		rc.Close()
		if err != nil {
			continue
		}
		added++
		if lat != 0 || lon != 0 {
			if st, err := s.data.Stat(s.rel(u.Name, final)); err == nil {
				s.db.Exec(`INSERT INTO photo_meta (owner, path, mod, size, taken, lat, lon) VALUES (?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(owner, path) DO UPDATE SET mod = excluded.mod, size = excluded.size, taken = excluded.taken, lat = excluded.lat, lon = excluded.lon`,
					u.Name, final, st.ModTime().UnixNano(), st.Size(), taken.Unix(), lat, lon)
			}
		}
	}
	return added, skipped, nil
}

// fileLetter moves a sorted letter into its folder (made if needed) under its new name.
func (s *Server) fileLetter(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Path   string `json:"path"`
		Folder string `json:"folder"`
		Name   string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	from := cleanRel(in.Path)
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, in.Name))
	if name == "" {
		name = strings.TrimSuffix(path.Base(from), path.Ext(from))
	}
	dir := cleanRel(in.Folder)
	to := path.Join(dir, name+path.Ext(from))
	if err := s.data.MkdirAll(s.rel(u.Name, dir), 0o755); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if from != to {
		if _, err := s.data.Stat(s.rel(u.Name, to)); err == nil {
			to = strings.TrimPrefix(s.freeName(u.Name, "/"+to), "/")
		}
		if err := s.move(u.Name, from, to); err != nil {
			fileError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": "/" + to})
}
