package app

import (
	"bytes"
	"encoding/base64"
	"net/url"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/skip2/go-qrcode"
	htmltemplate "html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
)

// Siri and Android shortcuts (Ask Cove with an app password), the shopping list by aisle with the usual items and a
// store mode, dinner ideas, the babysitter page, family guests (grandparents), school, a family video call,
// checking and restoring the off-site backup, and the week on paper.

const batch4Schema = `
CREATE TABLE IF NOT EXISTS shopping_history (
	item TEXT PRIMARY KEY, -- lower case
	shown TEXT NOT NULL,
	times INTEGER NOT NULL,
	last INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sitter_links (
	token TEXT PRIMARY KEY,
	created_by TEXT NOT NULL,
	label TEXT NOT NULL,
	created INTEGER NOT NULL,
	expires INTEGER NOT NULL
);`

func qrPNG(text string) (string, error) {
	png, err := qrcode.Encode(text, qrcode.Medium, 360)
	return base64.StdEncoding.EncodeToString(png), err
}

// --- shortcuts: "Hey Siri, ask Cove…"

// shortcut answers a question (or does what it says) for Siri, Android or any app: Basic auth with the user name and
// an app password, the question as the body (plain text, or JSON {"text": …}); the answer is plain text to speak.
func (s *Server) shortcut(w http.ResponseWriter, r *http.Request) {
	u, ok := s.davUser(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cove"`)
		http.Error(w, "Sign in with your Cove user name and an app password (Settings → Sync apps).", http.StatusUnauthorized)
		return
	}
	s.db.QueryRow(`SELECT admin, kid, guest FROM users WHERE name = ?`, u.Name).Scan(&u.Admin, &u.Kid, &u.Guest)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8000))
	q := strings.TrimSpace(string(body))
	var in struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(body, &in) == nil && in.Text != "" {
		q = in.Text
	}
	if q == "" {
		q = r.URL.Query().Get("q")
	}
	if q == "" {
		http.Error(w, "Say something to Cove.", http.StatusBadRequest)
		return
	}
	b, _ := json.Marshal(map[string]string{"question": q + "\n(Answer in one or two short spoken sentences, no lists or formatting.)"})
	req := httptest.NewRequest("POST", "/api/ask", bytes.NewReader(b)).WithContext(r.Context())
	rec := httptest.NewRecorder()
	s.ask(rec, req, u)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if rec.Code >= 300 {
		w.WriteHeader(rec.Code)
		io.WriteString(w, errorOf(rec.Body.Bytes()))
		return
	}
	var text, actions []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct{ Text, Action, Error string }
		json.Unmarshal([]byte(data), &ev)
		switch {
		case ev.Action != "":
			actions = append(actions, ev.Action)
		case ev.Text != "":
			text = append(text, ev.Text)
		case ev.Error != "":
			text = append(text, "Sorry: "+ev.Error)
		}
	}
	answer := strings.TrimSpace(strings.Join(text, ""))
	if answer == "" {
		answer = strings.Join(actions, " ")
	}
	io.WriteString(w, strings.NewReplacer("**", "", "*", "", "#", "").Replace(answer))
}

// --- the shopping list, by aisle

var aisles = []struct {
	name  string
	words []string
}{
	{"Produce", []string{"apple", "banana", "berr", "lettuce", "spinach", "tomato", "onion", "garlic", "potato", "carrot", "pepper", "cucumber", "avocado", "lemon", "lime", "orange", "grape", "fruit", "salad", "herb", "cilantro", "basil", "mushroom", "broccoli", "celery", "zucchini", "corn", "kale", "ginger"}},
	{"Bakery", []string{"bread", "bagel", "bun", "roll", "tortilla", "croissant", "muffin", "cake", "pita"}},
	{"Dairy & eggs", []string{"milk", "cheese", "yogurt", "yoghurt", "butter", "cream", "egg", "sour cream", "cottage"}},
	{"Meat & fish", []string{"chicken", "beef", "pork", "turkey", "bacon", "sausage", "ham", "salmon", "fish", "shrimp", "tuna", "steak", "ground", "lamb"}},
	{"Frozen", []string{"frozen", "ice cream", "pizza", "fries", "peas"}},
	{"Pantry", []string{"rice", "pasta", "flour", "sugar", "salt", "oil", "vinegar", "sauce", "beans", "soup", "cereal", "oat", "coffee", "tea", "spice", "honey", "jam", "peanut", "nuts", "cracker", "chips", "snack", "can", "broth", "syrup", "baking"}},
	{"Drinks", []string{"water", "juice", "soda", "beer", "wine", "sparkling", "kombucha"}},
	{"Household", []string{"paper", "towel", "tissue", "toilet", "soap", "detergent", "dish", "trash bag", "foil", "wrap", "sponge", "batter", "bulb", "cleaner", "bleach"}},
	{"Personal care", []string{"shampoo", "toothpaste", "toothbrush", "deodorant", "razor", "lotion", "sunscreen", "floss", "diaper", "wipes", "medicine", "vitamin", "band"}},
}

func aisleOf(item string) string {
	l := strings.ToLower(item)
	for _, a := range aisles {
		for _, w := range a.words {
			if strings.Contains(l, w) {
				return a.name
			}
		}
	}
	return "Other"
}

// shoppingList is the fridge's shopping list (making none): its id and items.
func (s *Server) shoppingList() (int64, []fridgeItem) {
	var id int64
	var items string
	s.db.QueryRow(`SELECT id, items FROM fridge WHERE kind = 'list' AND lower(trim(text)) IN ('shopping', 'shopping list', 'groceries')
		AND (expires = 0 OR expires > ?) ORDER BY created DESC LIMIT 1`, time.Now().Unix()).Scan(&id, &items)
	var list []fridgeItem
	json.Unmarshal([]byte(items), &list)
	return id, list
}

// remember counts what goes on the shopping list, for "the usual".
func (s *Server) remember(items []string) {
	for _, it := range items {
		if it = clip(it, 200); it != "" {
			s.db.Exec(`INSERT INTO shopping_history (item, shown, times, last) VALUES (?, ?, 1, ?)
				ON CONFLICT(item) DO UPDATE SET times = times + 1, last = excluded.last, shown = excluded.shown`, strings.ToLower(it), it, time.Now().Unix())
		}
	}
}

func (s *Server) shopping(w http.ResponseWriter, r *http.Request, u user) {
	id, list := s.shoppingList()
	type row struct {
		fridgeItem
		Index int    `json:"index"`
		Aisle string `json:"aisle"`
	}
	rows := []row{}
	on := map[string]bool{}
	for i, it := range list {
		rows = append(rows, row{it, i, aisleOf(it.Text)})
		if !it.Done {
			on[strings.ToLower(it.Text)] = true
		}
	}
	order := map[string]int{}
	for i, a := range aisles {
		order[a.name] = i
	}
	order["Other"] = len(aisles)
	sort.SliceStable(rows, func(i, j int) bool { return order[rows[i].Aisle] < order[rows[j].Aisle] })
	usual := []string{}
	if res, err := s.db.Query(`SELECT item, shown FROM shopping_history WHERE times >= 2 ORDER BY times DESC, last DESC LIMIT 40`); err == nil {
		for res.Next() {
			var k, shown string
			if res.Scan(&k, &shown) == nil && !on[k] && len(usual) < 24 {
				usual = append(usual, shown)
			}
		}
		res.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "items": rows, "usual": usual})
}

// shoppingDone clears what was bought from the list, and puts it in the pantry (one more of each it knows).
func (s *Server) shoppingDone(w http.ResponseWriter, r *http.Request, u user) {
	id, list := s.shoppingList()
	if id == 0 {
		writeJSON(w, http.StatusOK, map[string]int{"bought": 0, "stocked": 0})
		return
	}
	var left []fridgeItem
	var bought []string
	for _, it := range list {
		if it.Done {
			bought = append(bought, it.Text)
		} else {
			left = append(left, it)
		}
	}
	if left == nil {
		left = []fridgeItem{}
	}
	b, _ := json.Marshal(left)
	s.db.Exec(`UPDATE fridge SET items = ? WHERE id = ?`, string(b), id)
	stocked := 0
	for _, p := range hhItems[pantryItem](s, u, "pantry") {
		for _, name := range bought {
			n := strings.ToLower(name)
			if strings.Contains(n, strings.ToLower(p.V.Name)) || strings.Contains(strings.ToLower(p.V.Name), n) {
				p.V.Qty++
				data, _ := json.Marshal(p.V)
				s.db.Exec(`UPDATE hh SET data = ?, updated = ? WHERE id = ?`, data, time.Now().Unix(), p.ID)
				stocked++
				break
			}
		}
	}
	s.changed("fridge", s.everyone()...)
	s.changed("household", s.everyone()...)
	writeJSON(w, http.StatusOK, map[string]int{"bought": len(bought), "stocked": stocked})
}

// --- dinner ideas

func (s *Server) suggestMeals(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSON(r, &in); err != nil || !validDate(in.From) || !validDate(in.To) {
		jsonError(w, http.StatusBadRequest, "Which days?")
		return
	}
	var b strings.Builder
	b.WriteString("Recipes we have:\n")
	for _, rc := range hhItems[recipe](s, u, "recipe") {
		fmt.Fprintf(&b, "- %s: %s\n", rc.V.Title, strings.Join(rc.V.Ingredients, ", "))
	}
	b.WriteString("\nIn the pantry:\n")
	for _, p := range hhItems[pantryItem](s, u, "pantry") {
		if p.V.Qty > 0 {
			fmt.Fprintf(&b, "- %s\n", p.V.Name)
		}
	}
	b.WriteString("\nDinners already planned:\n")
	taken := map[string]bool{}
	for _, m := range hhItems[meal](s, u, "meal") {
		if m.V.Slot == "dinner" && m.V.Date >= in.From && m.V.Date <= in.To {
			taken[m.V.Date] = true
			fmt.Fprintf(&b, "- %s\n", m.V.Date)
		}
	}
	var days []string
	for d, _ := time.Parse("2006-01-02", in.From); d.Format("2006-01-02") <= in.To && len(days) < 14; d = d.AddDate(0, 0, 1) {
		if !taken[d.Format("2006-01-02")] {
			days = append(days, d.Format("2006-01-02")+" ("+d.Weekday().String()+")")
		}
	}
	if len(days) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ideas": []any{}})
		return
	}
	fmt.Fprintf(&b, "\nPlan dinner for: %s", strings.Join(days, ", "))
	var got struct {
		Ideas []struct {
			Date    string   `json:"date"`
			Dinner  string   `json:"dinner"`
			Recipe  bool     `json:"recipe"` // one of our recipes
			Missing []string `json:"missing"`
			Why     string   `json:"why"`
		} `json:"ideas"`
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	err := s.aiJSON(ctx, `You plan a family's dinners. Prefer their own recipes, use what's in the pantry, vary the week, keep weeknights easy.
Give {"ideas": [{"date": "YYYY-MM-DD", "dinner": the recipe's exact name or a simple idea, "recipe": true if it's one of their recipes,
"missing": ingredients to buy (not in the pantry), "why": a few words}]}, one per day asked.`, b.String(), &got)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	recipes := map[string]int64{}
	for _, rc := range hhItems[recipe](s, u, "recipe") {
		recipes[strings.ToLower(rc.V.Title)] = rc.ID
	}
	type idea struct {
		Date     string   `json:"date"`
		Dinner   string   `json:"dinner"`
		RecipeID int64    `json:"recipeId"`
		Missing  []string `json:"missing"`
		Why      string   `json:"why"`
	}
	out := []idea{}
	for _, i := range got.Ideas {
		if !validDate(i.Date) || i.Date < in.From || i.Date > in.To || taken[i.Date] || clip(i.Dinner, 200) == "" {
			continue
		}
		out = append(out, idea{i.Date, clip(i.Dinner, 200), recipes[strings.ToLower(strings.TrimSpace(i.Dinner))], trimList(i.Missing, 20), clip(i.Why, 200)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ideas": out})
}

// --- the babysitter page

type sitterInfo struct {
	Address  string         `json:"address"`
	Contacts []sitterPerson `json:"contacts"` // parents, neighbors, grandparents
	Kids     []sitterKid    `json:"kids"`
	Doctor   string         `json:"doctor"`
	Rules    string         `json:"rules"`
	Notes    string         `json:"notes"`
	Wifi     bool           `json:"wifi"` // show the fridge's guest Wi-Fi card
}

type sitterPerson struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Who   string `json:"who"` // Mom, neighbor…
}

type sitterKid struct {
	Name      string `json:"name"`
	Age       string `json:"age"`
	Allergies string `json:"allergies"`
	Bedtime   string `json:"bedtime"`
	Notes     string `json:"notes"`
}

func (x *sitterInfo) clean() error {
	x.Address, x.Doctor, x.Rules, x.Notes = clip(x.Address, 300), clip(x.Doctor, 300), clip(x.Rules, 4000), clip(x.Notes, 4000)
	c := []sitterPerson{}
	for _, p := range x.Contacts {
		if p.Name = clip(p.Name, 100); p.Name != "" && len(c) < 20 {
			p.Phone, p.Who = clip(p.Phone, 40), clip(p.Who, 60)
			c = append(c, p)
		}
	}
	x.Contacts = c
	k := []sitterKid{}
	for _, p := range x.Kids {
		if p.Name = clip(p.Name, 100); p.Name != "" && len(k) < 12 {
			p.Age, p.Allergies, p.Bedtime, p.Notes = clip(p.Age, 20), clip(p.Allergies, 300), clip(p.Bedtime, 40), clip(p.Notes, 1000)
			k = append(k, p)
		}
	}
	x.Kids = k
	return nil
}
func (x *sitterInfo) label() string { return "Babysitter page" }

func (s *Server) sitterRecord() (hhRow, sitterInfo, bool) {
	rows := s.hhRows("sitter")
	if len(rows) == 0 {
		return hhRow{}, sitterInfo{}, false
	}
	var x sitterInfo
	json.Unmarshal(rows[len(rows)-1].Data, &x)
	return rows[len(rows)-1], x, true
}

func (s *Server) sitterLinks(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT token, label, created, expires, created_by FROM sitter_links WHERE expires > ? ORDER BY created DESC`, time.Now().Unix())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type link struct {
		Token   string `json:"token"`
		Label   string `json:"label"`
		Created int64  `json:"created"`
		Expires int64  `json:"expires"`
		By      string `json:"by"`
		URL     string `json:"url"`
	}
	out := []link{}
	for rows.Next() {
		var l link
		if rows.Scan(&l.Token, &l.Label, &l.Created, &l.Expires, &l.By) == nil {
			l.URL = s.cfg.URL + "/sitter/" + l.Token
			out = append(out, l)
		}
	}
	rows.Close()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) makeSitterLink(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid || u.Guest {
		jsonError(w, http.StatusForbidden, "A grown-up makes the babysitter's link.")
		return
	}
	var in struct {
		Label string `json:"label"`
		Hours int    `json:"hours"`
	}
	readJSON(r, &in)
	if in.Hours <= 0 || in.Hours > 24*14 {
		in.Hours = 24
	}
	if _, _, ok := s.sitterRecord(); !ok {
		jsonError(w, http.StatusBadRequest, "Fill in the babysitter page first.")
		return
	}
	tok := randomToken(18)
	exp := time.Now().Add(time.Duration(in.Hours) * time.Hour).Unix()
	s.db.Exec(`INSERT INTO sitter_links (token, created_by, label, created, expires) VALUES (?, ?, ?, ?, ?)`, tok, u.Name, clip(in.Label, 80), time.Now().Unix(), exp)
	writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "url": s.cfg.URL + "/sitter/" + tok, "expires": exp})
}

func (s *Server) endSitterLink(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid || u.Guest {
		jsonError(w, http.StatusForbidden, "A grown-up ends the link.")
		return
	}
	s.db.Exec(`DELETE FROM sitter_links WHERE token = ?`, r.PathValue("token"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var sitterPage = htmltemplate.Must(htmltemplate.New("sitter").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">
<title>For the babysitter</title><style>
body{font:16px/1.5 system-ui,-apple-system,sans-serif;margin:0;background:#f5f6f8;color:#1b2130}
main{max-width:640px;margin:0 auto;padding:20px 16px 60px}h1{font-size:24px;margin:8px 0 2px}h2{font-size:15px;text-transform:uppercase;letter-spacing:.05em;color:#687082;margin:24px 0 8px}
.card{background:#fff;border-radius:12px;padding:14px 16px;box-shadow:0 1px 3px rgba(0,0,0,.08);margin-bottom:10px}
a.tel{display:inline-block;margin-top:4px;padding:6px 12px;border-radius:8px;background:#2563eb;color:#fff;text-decoration:none;font-weight:600}
.warn{background:#fff1d6;color:#7a4a00;border-radius:8px;padding:6px 10px;margin-top:6px}.muted{color:#687082;font-size:14px}pre{white-space:pre-wrap;font:inherit;margin:0}
.e{background:#fee4e2;color:#a11;border-radius:12px;padding:14px 16px;font-weight:600}img{width:180px;height:180px;image-rendering:pixelated}
@media (prefers-color-scheme:dark){body{background:#0f1218;color:#e7eaf0}.card{background:#171b23}.muted,h2{color:#9aa3b2}}
</style></head><body><main>
<h1>Thank you for looking after us!</h1><p class="muted">This page works until {{.Until}}.</p>
<div class="e">Emergency: call 911 first.</div>
{{if .Info.Address}}<h2>Address</h2><div class="card">{{.Info.Address}}</div>{{end}}
{{if .Info.Contacts}}<h2>Who to call</h2>{{range .Info.Contacts}}<div class="card"><b>{{.Name}}</b>{{if .Who}} <span class="muted">· {{.Who}}</span>{{end}}{{if .Phone}}<br><a class="tel" href="tel:{{.Phone}}">Call {{.Phone}}</a>{{end}}</div>{{end}}{{end}}
{{if .Info.Kids}}<h2>The kids</h2>{{range .Info.Kids}}<div class="card"><b>{{.Name}}</b>{{if .Age}} <span class="muted">· {{.Age}}</span>{{end}}
{{if .Allergies}}<div class="warn">Allergies: {{.Allergies}}</div>{{end}}{{if .Bedtime}}<div>Bedtime: {{.Bedtime}}</div>{{end}}{{if .Notes}}<pre class="muted">{{.Notes}}</pre>{{end}}</div>{{end}}{{end}}
{{if .Info.Rules}}<h2>House rules</h2><div class="card"><pre>{{.Info.Rules}}</pre></div>{{end}}
{{if .Info.Doctor}}<h2>Doctor</h2><div class="card"><pre>{{.Info.Doctor}}</pre></div>{{end}}
{{if .Wifi}}<h2>Wi-Fi</h2><div class="card"><b>{{.Wifi.Name}}</b><br>Password: <b>{{.Wifi.Pass}}</b><br><img src="{{.QR}}" alt="Scan to join"></div>{{end}}
{{if .Info.Notes}}<h2>Good to know</h2><div class="card"><pre>{{.Info.Notes}}</pre></div>{{end}}
</main></body></html>`))

// sitterView is the babysitter's page: no sign-in, only what's written for them, until the link runs out.
func (s *Server) sitterView(w http.ResponseWriter, r *http.Request) {
	var exp int64
	if s.db.QueryRow(`SELECT expires FROM sitter_links WHERE token = ?`, r.PathValue("token")).Scan(&exp) != nil || exp < time.Now().Unix() {
		http.Error(w, "This link has ended. Ask the family for a new one.", http.StatusNotFound)
		return
	}
	_, info, ok := s.sitterRecord()
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Info  sitterInfo
		Until string
		Wifi  *struct{ Name, Pass string }
		QR    htmltemplate.URL
	}{Info: info, Until: time.Unix(exp, 0).In(time.Local).Format("Monday, January 2 at 3:04 PM")}
	if info.Wifi {
		var id int64
		var name, items string
		if s.db.QueryRow(`SELECT id, text, items FROM fridge WHERE kind = 'wifi' ORDER BY created DESC LIMIT 1`).Scan(&id, &name, &items) == nil {
			var list []fridgeItem
			json.Unmarshal([]byte(items), &list)
			pass := ""
			if len(list) > 0 {
				pass = list[0].Text
			}
			data.Wifi = &struct{ Name, Pass string }{name, pass}
			if png, err := qrPNG(wifiQR(name, pass, "")); err == nil {
				data.QR = htmltemplate.URL("data:image/png;base64," + png)
			}
		}
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	sitterPage.Execute(w, data)
}

// --- school

type homework struct {
	Kid     string `json:"kid"`
	Subject string `json:"subject"`
	Title   string `json:"title"`
	Due     string `json:"due"`
	Done    bool   `json:"done"`
	Notes   string `json:"notes"`
}

func (h *homework) clean() error {
	h.Kid, h.Subject, h.Title, h.Notes = clip(h.Kid, 64), clip(h.Subject, 60), clip(h.Title, 200), clip(h.Notes, 2000)
	if h.Title == "" {
		return errors.New("What's the homework?")
	}
	return optDate(&h.Due)
}
func (h *homework) label() string { return h.Title }

// homeworkReminders: at 5 in the afternoon the day before it's due, to the kid and whoever wrote it down.
func (s *Server) homeworkReminders(from, to time.Time) {
	for _, h := range hhItems[homework](s, user{}, "homework") {
		if h.V.Done || h.V.Due == "" {
			continue
		}
		for _, who := range []string{h.V.Kid, h.Owner} {
			if who == "" {
				continue
			}
			d, err := time.ParseInLocation("2006-01-02", h.V.Due, s.location(who))
			if err != nil {
				continue
			}
			at := d.AddDate(0, 0, -1).Add(17 * time.Hour)
			if at.After(from) && !at.After(to) && s.once(fmt.Sprintf("hw|%d|%s", h.ID, who), 2*24*time.Hour) {
				s.notify(who, fmt.Sprintf("Due tomorrow: %s %s", h.V.Subject, h.V.Title), "#/school")
			}
		}
	}
}

// --- a family video call (MiroTalk, or any video call address)

func (s *Server) callInfo(w http.ResponseWriter, r *http.Request, u user) {
	base := s.setting("call.url")
	room := s.setting("call.room")
	if room == "" {
		room = "cove-" + strings.ToLower(randomToken(9))
		s.setSetting("call.room", room)
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": base, "room": room})
}

func (s *Server) startCall(w http.ResponseWriter, r *http.Request, u user) {
	base := s.setting("call.url")
	if base == "" {
		jsonError(w, http.StatusBadRequest, "An admin sets the video call's address first (Admin → Home connections).")
		return
	}
	room := s.setting("call.room")
	link := base + "/join?room=" + room
	for _, o := range everyoneBut(s.db, u.Name) {
		s.notify(o, displayName(u)+" started a family video call: tap to join", "")
		go s.pushTo(o, displayName(u)+" is calling the family", link)
	}
	writeJSON(w, http.StatusOK, map[string]string{"link": link + "&name=" + urlQuery(displayName(u))})
}

func urlQuery(s string) string { return url.QueryEscape(s) }

func (s *Server) putCall(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		URL string `json:"url"`
	}
	readJSON(r, &in)
	u2, err := cleanURL(in.URL)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setSetting("call.url", u2)
	writeJSON(w, http.StatusOK, map[string]string{"url": u2})
}

// --- the off-site backup: checking it, and bringing back a file

func (s *Server) checkOffsite(ctx context.Context) error {
	c := s.offsiteConfig()
	if c.Repo == "" {
		return errors.New("off-site backups aren't set up")
	}
	_, err := s.restic(ctx, c, "check", "--read-data-subset=1/20")
	s.setSettingJSON("offsite.check", map[string]any{"at": time.Now().Unix(), "ok": err == nil, "error": errString(err)})
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) runCheck(w http.ResponseWriter, r *http.Request, u user) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		if err := s.checkOffsite(ctx); err != nil {
			s.notify(u.Name, "The off-site backup check found a problem: "+firstLine(err.Error()), "#/admin")
		} else {
			s.notify(u.Name, "The off-site backup checked out: it can be read back.", "#/admin")
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

type snapshotView struct {
	ID   string `json:"id"`
	Time string `json:"time"`
}

func (s *Server) offsiteSnapshots(w http.ResponseWriter, r *http.Request, u user) {
	c := s.offsiteConfig()
	if c.Repo == "" {
		writeJSON(w, http.StatusOK, []snapshotView{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := s.restic(ctx, c, "snapshots", "--tag", "cove", "--json")
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	var snaps []struct {
		ID   string `json:"short_id"`
		Time string `json:"time"`
	}
	json.Unmarshal(out, &snaps)
	list := []snapshotView{}
	for i := len(snaps) - 1; i >= 0; i-- {
		list = append(list, snapshotView{snaps[i].ID, snaps[i].Time})
	}
	writeJSON(w, http.StatusOK, list)
}

// restoreFile brings one of u's files (or folders) back from an off-site snapshot, next to where it was, named
// "name (from DATE)".
func (s *Server) restoreFromBackup(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Snapshot string `json:"snapshot"`
		Path     string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil || in.Snapshot == "" || strings.ContainsAny(in.Snapshot, " /\\.") {
		jsonError(w, http.StatusBadRequest, "Which backup, and which file?")
		return
	}
	c := s.offsiteConfig()
	p := cleanRel(in.Path)
	if c.Repo == "" || p == "." {
		jsonError(w, http.StatusBadRequest, "Pick a file (and off-site backups need to be set up).")
		return
	}
	tmp, err := os.MkdirTemp(s.cfg.Data, ".cove-restore-")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	src := s.cfg.Data + "/files/" + u.Name + "/" + p
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	if _, err := s.restic(ctx, c, "restore", in.Snapshot, "--target", tmp, "--include", src); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	got := tmp + src
	if _, err := os.Stat(got); err != nil {
		jsonError(w, http.StatusNotFound, "That file isn't in that backup.")
		return
	}
	ext := path.Ext(p)
	dest := strings.TrimSuffix(p, ext) + " (from " + time.Now().Format("Jan 2") + " backup)" + ext
	dest = strings.TrimPrefix(s.freeName(u.Name, "/"+dest), "/")
	if err := os.Rename(got, s.cfg.Data+"/files/"+u.Name+"/"+dest); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.forgetUsage(u.Name)
	s.changed("files", s.fileWatchers(u.Name)...)
	writeJSON(w, http.StatusOK, map[string]string{"path": "/" + dest})
}

// --- guests: grandparents and others who see some of the family's things

// guestAllowed are the parts of Cove a family guest uses: the calendar (shared with them), shared albums, the
// fridge, chat, notifications and their settings.
var guestAllowed = []string{"/api/me", "/api/home", "/api/live", "/api/calendars", "/api/events", "/api/fridge", "/api/chat", "/api/albums",
	"/api/notifications", "/api/settings", "/api/push", "/api/users", "/api/chat-photos", "/api/freebusy"}

func guestMay(r *http.Request) bool {
	p := r.URL.Path
	ok := slices.ContainsFunc(guestAllowed, func(a string) bool { return p == a || strings.HasPrefix(p, a+"/") || strings.HasPrefix(p, a+"?") })
	if !ok {
		return false
	}
	if r.Method == http.MethodGet {
		return true
	}
	// changes: only chat, votes and ticks on the fridge, their notifications and settings, push
	return strings.HasPrefix(p, "/api/chat") || strings.HasSuffix(p, "/vote") || (strings.HasPrefix(p, "/api/fridge/") && r.Method == http.MethodPut) ||
		strings.HasPrefix(p, "/api/notifications") || strings.HasPrefix(p, "/api/settings") || strings.HasPrefix(p, "/api/push")
}

func (s *Server) adminSetGuest(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Guest bool `json:"guest"`
	}
	readJSON(r, &in)
	if r.PathValue("name") == u.Name {
		jsonError(w, http.StatusBadRequest, "You can't make your own account a guest's.")
		return
	}
	res, _ := s.db.Exec(`UPDATE users SET guest = ?, kid = CASE WHEN ? THEN 0 ELSE kid END WHERE name = ?`, in.Guest, in.Guest, r.PathValue("name"))
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "There's no such person.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
