package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// More of family life: wish lists (what's bought stays a surprise), medicines with dose reminders and health
// records (vaccines, checkups, the vet), the pantry (scan a barcode; running low puts it on the shopping list),
// a family journal, "on this day" memories, and a morning briefing.

const familySchema = `
CREATE TABLE IF NOT EXISTS med_taken (
	med INTEGER NOT NULL,
	day TEXT NOT NULL,
	time TEXT NOT NULL,
	by TEXT NOT NULL,
	at INTEGER NOT NULL,
	PRIMARY KEY (med, day, time)
);`

func migrateFamily(db *sql.DB) {
	db.Exec(`ALTER TABLE user_settings ADD COLUMN briefing TEXT NOT NULL DEFAULT ''`)
}

// --- wish lists

type wish struct {
	For       string  `json:"for"` // whose wish (a user name)
	Title     string  `json:"title"`
	Link      string  `json:"link"`
	Price     float64 `json:"price"`
	Notes     string  `json:"notes"`
	ClaimedBy string  `json:"claimedBy"` // who's getting it (never shown to For)
}

func (x *wish) clean() error {
	x.For, x.Title, x.Link, x.Notes, x.ClaimedBy = clip(x.For, 64), clip(x.Title, 200), safeLink(x.Link), clip(x.Notes, 2000), clip(x.ClaimedBy, 64)
	if x.Title == "" {
		return fmt.Errorf("What's the wish?")
	}
	return nil
}
func (x *wish) label() string { return x.Title }

// safeLink keeps web links only (no javascript: and the like).
func safeLink(s string) string {
	s = clip(s, 1000)
	if s == "" {
		return ""
	}
	if p, err := url.Parse(s); err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return ""
	}
	return s
}

// wishView hides what's a surprise: ideas others wrote down for you, and who's getting what.
func wishView(u user, row hhRow, m map[string]any) bool {
	if m["for"] == u.Name {
		if row.Owner != u.Name {
			return false
		}
		delete(m, "claimedBy")
	}
	return true
}

// wishMerge keeps the surprise when the person changes their own wish, and lets others only claim for themselves.
func wishMerge(u user, old, next []byte) []byte {
	var o, n wish
	json.Unmarshal(old, &o)
	json.Unmarshal(next, &n)
	switch {
	case u.Name == o.For:
		n.ClaimedBy, n.For = o.ClaimedBy, o.For
	case o.ClaimedBy != "" && o.ClaimedBy != u.Name:
		n.ClaimedBy = o.ClaimedBy // someone else is getting it: only they let it go
	case n.ClaimedBy != "" && n.ClaimedBy != u.Name:
		n.ClaimedBy = o.ClaimedBy // claim it for yourself only
	}
	b, _ := json.Marshal(n)
	return b
}

// --- medicines and health

type med struct {
	Who    string   `json:"who"` // a user name, or a pet's name
	Name   string   `json:"name"`
	Dose   string   `json:"dose"`
	Times  []string `json:"times"` // "08:00"
	Days   string   `json:"days"`  // daily, weekdays, or "mon,thu"
	Start  string   `json:"start"`
	End    string   `json:"end"`
	Notes  string   `json:"notes"`
	Paused bool     `json:"paused"`
}

var clockTime = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (m *med) clean() error {
	m.Who, m.Name, m.Dose, m.Notes, m.Days = clip(m.Who, 64), clip(m.Name, 200), clip(m.Dose, 100), clip(m.Notes, 2000), clip(m.Days, 40)
	if m.Name == "" {
		return fmt.Errorf("Which medicine?")
	}
	times := []string{}
	for _, t := range m.Times {
		if t = strings.TrimSpace(t); clockTime.MatchString(t) && !slices.Contains(times, t) && len(times) < 12 {
			times = append(times, t)
		}
	}
	slices.Sort(times)
	m.Times = times
	if m.Days == "" {
		m.Days = "daily"
	}
	for _, d := range []*string{&m.Start, &m.End} {
		if err := optDate(d); err != nil {
			return err
		}
	}
	return nil
}
func (m *med) label() string { return m.Name }

func (m *med) on(day time.Time) bool {
	d := day.Format("2006-01-02")
	if m.Paused || m.Start != "" && d < m.Start || m.End != "" && d > m.End {
		return false
	}
	switch m.Days {
	case "daily":
		return true
	case "weekdays":
		return day.Weekday() >= time.Monday && day.Weekday() <= time.Friday
	}
	return strings.Contains(strings.ToLower(m.Days), strings.ToLower(day.Weekday().String()[:3]))
}

type healthRecord struct {
	Who      string `json:"who"`
	Type     string `json:"type"` // vaccine, checkup, dentist, eyes, vet, other
	Title    string `json:"title"`
	Date     string `json:"date"`
	Next     string `json:"next"` // when it's due again
	Provider string `json:"provider"`
	Notes    string `json:"notes"`
}

func (h *healthRecord) clean() error {
	h.Who, h.Type, h.Title, h.Provider, h.Notes = clip(h.Who, 64), clip(h.Type, 20), clip(h.Title, 200), clip(h.Provider, 200), clip(h.Notes, 4000)
	if h.Title == "" {
		return fmt.Errorf("What was it?")
	}
	for _, d := range []*string{&h.Date, &h.Next} {
		if err := optDate(d); err != nil {
			return err
		}
	}
	return nil
}
func (h *healthRecord) label() string { return h.Title }

type dose struct {
	Med   int64  `json:"med"`
	Who   string `json:"who"`
	Name  string `json:"name"`
	Dose  string `json:"dose"`
	Time  string `json:"time"`
	Taken bool   `json:"taken"`
	By    string `json:"by"`
}

func (s *Server) dosesOn(u user, day time.Time) []dose {
	out := []dose{}
	d := day.Format("2006-01-02")
	for _, m := range hhItems[med](s, u, "med") {
		if !m.V.on(day) {
			continue
		}
		for _, t := range m.V.Times {
			x := dose{Med: m.ID, Who: m.V.Who, Name: m.V.Name, Dose: m.V.Dose, Time: t}
			if s.db.QueryRow(`SELECT by FROM med_taken WHERE med = ? AND day = ? AND time = ?`, m.ID, d, t).Scan(&x.By) == nil {
				x.Taken = true
			}
			out = append(out, x)
		}
	}
	slices.SortFunc(out, func(a, b dose) int { return strings.Compare(a.Time+a.Who, b.Time+b.Who) })
	return out
}

func (s *Server) medsToday(w http.ResponseWriter, r *http.Request, u user) {
	now := time.Now().In(s.location(u.Name))
	writeJSON(w, http.StatusOK, map[string]any{"day": now.Format("2006-01-02"), "doses": s.dosesOn(u, now)})
}

func (s *Server) medTaken(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Day   string `json:"day"`
		Time  string `json:"time"`
		Taken bool   `json:"taken"`
	}
	if err := readJSON(r, &in); err != nil || !validDate(in.Day) || !clockTime.MatchString(in.Time) {
		jsonError(w, http.StatusBadRequest, "Which day and time?")
		return
	}
	row, ok := s.hhRow("med", r.PathValue("id"))
	if !ok || row.Private && row.Owner != u.Name {
		jsonError(w, http.StatusNotFound, "That medicine is gone.")
		return
	}
	if in.Taken {
		s.db.Exec(`INSERT OR REPLACE INTO med_taken (med, day, time, by, at) VALUES (?, ?, ?, ?, ?)`, row.ID, in.Day, in.Time, displayName(u), time.Now().Unix())
	} else {
		s.db.Exec(`DELETE FROM med_taken WHERE med = ? AND day = ? AND time = ?`, row.ID, in.Day, in.Time)
	}
	s.hhChanged(u, row)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- the pantry

type pantryItem struct {
	Name    string  `json:"name"`
	Brand   string  `json:"brand"`
	Barcode string  `json:"barcode"`
	Qty     float64 `json:"qty"`
	Unit    string  `json:"unit"`
	Min     float64 `json:"min"` // at or below this, it goes on the shopping list
	Where   string  `json:"where"`
	Expires string  `json:"expires"`
}

func (p *pantryItem) clean() error {
	p.Name, p.Brand, p.Unit, p.Where = clip(p.Name, 200), clip(p.Brand, 100), clip(p.Unit, 20), clip(p.Where, 60)
	p.Barcode = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, p.Barcode)
	if p.Name == "" {
		return fmt.Errorf("What is it?")
	}
	p.Qty, p.Min = max(p.Qty, 0), max(p.Min, 0)
	return optDate(&p.Expires)
}
func (p *pantryItem) label() string { return p.Name }

// pantryUse changes how much there is (−1 when one is used up); running low puts it on the shopping list.
func (s *Server) pantryUse(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Delta float64 `json:"delta"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, ok := s.hhRow("pantry", r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "That's gone from the pantry.")
		return
	}
	var p pantryItem
	json.Unmarshal(row.Data, &p)
	before := p.Qty
	p.Qty = max(0, p.Qty+in.Delta)
	b, _ := json.Marshal(p)
	s.db.Exec(`UPDATE hh SET data = ?, updated = ? WHERE id = ?`, b, time.Now().Unix(), row.ID)
	listed := false
	if in.Delta < 0 && before > p.Min && p.Qty <= p.Min {
		if n, _ := s.addToShopping(u, []string{p.Name}); n > 0 {
			listed = true
		}
	}
	s.hhChanged(u, row)
	writeJSON(w, http.StatusOK, map[string]any{"qty": p.Qty, "listed": listed})
}

// pantryLookup finds a product's name by its barcode in Open Food Facts (the one outside site this asks).
func (s *Server) pantryLookup(w http.ResponseWriter, r *http.Request, u user) {
	code := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, r.URL.Query().Get("code"))
	if len(code) < 6 || len(code) > 14 {
		jsonError(w, http.StatusBadRequest, "That isn't a barcode number.")
		return
	}
	for _, it := range hhItems[pantryItem](s, u, "pantry") { // known here already
		if it.V.Barcode == code {
			writeJSON(w, http.StatusOK, map[string]any{"name": it.V.Name, "brand": it.V.Brand, "id": it.ID})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://world.openfoodfacts.org/api/v2/product/"+code+".json?fields=product_name,brands,quantity", nil)
	req.Header.Set("User-Agent", "Cove household app")
	res, err := connectHTTP.Do(req)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "Couldn't look the barcode up: "+err.Error())
		return
	}
	defer res.Body.Close()
	var out struct {
		Status  int `json:"status"`
		Product struct {
			Name     string `json:"product_name"`
			Brands   string `json:"brands"`
			Quantity string `json:"quantity"`
		} `json:"product"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out)
	if out.Status != 1 || out.Product.Name == "" {
		writeJSON(w, http.StatusOK, map[string]any{"name": "", "brand": ""})
		return
	}
	brand, _, _ := strings.Cut(out.Product.Brands, ",")
	writeJSON(w, http.StatusOK, map[string]any{"name": out.Product.Name, "brand": strings.TrimSpace(brand), "quantity": out.Product.Quantity})
}

// --- the family journal and memories

type journal struct {
	Date   string   `json:"date"`
	Text   string   `json:"text"`
	Photos []string `json:"photos"` // in the author's files
}

func (j *journal) clean() error {
	j.Text, j.Photos = clip(j.Text, 20000), trimList(j.Photos, 20)
	if !validDate(j.Date) {
		j.Date = time.Now().Format("2006-01-02")
	}
	if j.Text == "" && len(j.Photos) == 0 {
		return fmt.Errorf("Write something first.")
	}
	return nil
}
func (j *journal) label() string { return firstLine(j.Text) }

// journalPhoto shows a photo in a journal entry, from its author's files.
func (s *Server) journalPhoto(w http.ResponseWriter, r *http.Request, u user) {
	row, ok := s.hhRow("journal", r.PathValue("id"))
	if !ok || row.Private && row.Owner != u.Name {
		http.NotFound(w, r)
		return
	}
	var j journal
	json.Unmarshal(row.Data, &j)
	p := r.URL.Query().Get("path")
	if !slices.Contains(j.Photos, p) {
		http.NotFound(w, r)
		return
	}
	name := s.rel(row.Owner, p)
	if r.URL.Query().Get("w") != "" {
		s.serveThumb(w, r, name)
		return
	}
	s.serveFile(w, r, name, false)
}

func (s *Server) memories(w http.ResponseWriter, r *http.Request, u user) {
	loc := s.location(u.Name)
	now := time.Now().In(loc)
	md := now.Format("01-02")
	type memory struct {
		photoMeta
		Years int `json:"years"`
	}
	photos := []memory{}
	if rows, err := s.db.Query(`SELECT path, taken FROM photo_meta WHERE owner = ? ORDER BY taken DESC`, u.Name); err == nil {
		for rows.Next() {
			var m memory
			if rows.Scan(&m.Path, &m.Taken) != nil {
				continue
			}
			t := time.Unix(m.Taken, 0).In(loc)
			if t.Format("01-02") == md && t.Year() < now.Year() && len(photos) < 40 {
				m.Name, m.Kind, m.Owner, m.Years = path.Base(m.Path), kindOf(m.Path, false), u.Name, now.Year()-t.Year()
				photos = append(photos, m)
			}
		}
		rows.Close()
	}
	entries := []map[string]any{}
	for _, j := range hhItems[journal](s, u, "journal") {
		if strings.HasSuffix(j.V.Date, "-"+strings.ReplaceAll(md, "-", "-")) && j.V.Date[:4] < now.Format("2006") {
			entries = append(entries, s.hhJSON(j.hhRow))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"photos": photos, "journal": entries})
}

// --- the morning briefing

// briefing is the day ahead for one person, in a few lines.
func (s *Server) briefing(u user, day time.Time) string {
	loc := s.location(u.Name)
	var parts []string
	var evs []string
	for _, o := range s.eventsFor(u, loc, day, day.AddDate(0, 0, 1)) {
		if o.AllDay {
			evs = append(evs, o.Title)
		} else {
			evs = append(evs, occStart(o, loc).In(loc).Format("3:04")+" "+o.Title)
		}
	}
	if len(evs) > 0 {
		parts = append(parts, "📅 "+strings.Join(evs, ", "))
	}
	d := day.Format("2006-01-02")
	recipes := map[int64]string{}
	for _, r := range hhItems[recipe](s, u, "recipe") {
		recipes[r.ID] = r.V.Title
	}
	for _, m := range hhItems[meal](s, u, "meal") {
		if m.V.Date == d && m.V.Slot == "dinner" {
			what := m.V.Text
			if t, ok := recipes[m.V.Recipe]; ok {
				what = t
			}
			parts = append(parts, "🍽 Dinner: "+what)
		}
	}
	var chores []string
	for _, c := range hhItems[chore](s, u, "chore") {
		if c.V.dueOn(day) && slices.Contains(c.V.whoOn(day), u.Name) {
			chores = append(chores, c.V.Title)
		}
	}
	if len(chores) > 0 {
		parts = append(parts, "🧹 "+strings.Join(chores, ", "))
	}
	var tasks []string
	for _, t := range s.tasksFor(u.Name, loc, "") {
		if !t.Done && t.Due != "" && t.Due[:min(10, len(t.Due))] <= d && (t.Assignee == "" || t.Assignee == u.Name) && len(tasks) < 5 {
			tasks = append(tasks, t.Title)
		}
	}
	if len(tasks) > 0 {
		parts = append(parts, "✅ "+strings.Join(tasks, ", "))
	}
	meds := 0
	for _, x := range s.dosesOn(u, day) {
		if x.Who == u.Name || x.Who == "" {
			meds++
		}
	}
	if meds > 0 {
		parts = append(parts, fmt.Sprintf("💊 %d doses today", meds))
	}
	if !u.Kid {
		for _, b := range hhItems[bill](s, u, "bill") {
			due, period := b.V.due(time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, loc))
			if !due.IsZero() && !slices.Contains(b.V.Paid, period) && !b.V.Autopay && !due.Before(day) && due.Before(day.AddDate(0, 0, 4)) {
				parts = append(parts, fmt.Sprintf("💵 %s $%.2f due %s", b.V.Name, b.V.Amount, due.Format("Mon")))
			}
		}
	}
	for _, n := range s.fridgeNotes(u, 50) {
		if n.Kind == "countdown" {
			if t, err := time.ParseInLocation("2006-01-02", n.Date, loc); err == nil {
				if days := int(t.Sub(day).Hours() / 24); days >= 0 && days <= 14 {
					parts = append(parts, fmt.Sprintf("⏳ %s in %d days", n.Text, days))
				}
			}
		}
	}
	if s.haConfig().Weather != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if states, err := s.haStates(ctx); err == nil {
			if wv := s.weather(states); wv != nil {
				parts = append([]string{fmt.Sprintf("🌤 %s, %.0f%s", strings.ReplaceAll(wv.State, "partlycloudy", "partly cloudy"), wv.Temp, wv.Unit)}, parts...)
			}
		}
		cancel()
	}
	if len(parts) == 0 {
		return "Nothing on today. Enjoy!"
	}
	return strings.Join(parts, "\n")
}

func (s *Server) sendBriefings(from, to time.Time) {
	rows, err := s.db.Query(`SELECT u.name, u.display, u.admin, u.kid, s.briefing FROM users u JOIN user_settings s ON s.user = u.name WHERE s.briefing != ''`)
	if err != nil {
		return
	}
	type who struct {
		u    user
		when string
	}
	var list []who
	for rows.Next() {
		var x who
		if rows.Scan(&x.u.Name, &x.u.Display, &x.u.Admin, &x.u.Kid, &x.when) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		loc := s.location(x.u.Name)
		h, m, ok := strings.Cut(x.when, ":")
		hh, err1 := strconv.Atoi(h)
		mm, err2 := strconv.Atoi(m)
		if !ok || err1 != nil || err2 != nil {
			continue
		}
		local := to.In(loc)
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		at := day.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
		if at.After(from) && !at.After(to) && s.once("brief|"+x.u.Name+"|"+day.Format("2006-01-02"), 20*time.Hour) {
			s.notify(x.u.Name, "Good morning! Today:\n"+s.briefing(x.u, day), "#/home")
		}
	}
}

func (s *Server) briefingNow(w http.ResponseWriter, r *http.Request, u user) {
	loc := s.location(u.Name)
	now := time.Now().In(loc)
	writeJSON(w, http.StatusOK, map[string]string{"text": s.briefing(u, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc))})
}

// medicine and health reminders (called with the other household reminders)
func (s *Server) familyReminders(from, to time.Time) {
	admin := user{}
	users := map[string]bool{}
	for _, n := range s.everyone() {
		users[n] = true
	}
	for _, m := range hhItems[med](s, admin, "med") {
		who := m.Owner
		if users[m.V.Who] {
			who = m.V.Who
		}
		loc := s.location(who)
		local := to.In(loc)
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		if !m.V.on(day) {
			continue
		}
		for _, t := range m.V.Times {
			hh, _ := strconv.Atoi(t[:2])
			mm, _ := strconv.Atoi(t[3:])
			at := day.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
			if at.After(from) && !at.After(to) && s.once(fmt.Sprintf("med|%d|%s|%s", m.ID, day.Format("2006-01-02"), t), 20*time.Hour) {
				whose := "" // a pet's or someone else's
				if m.V.Who != "" && m.V.Who != who {
					whose = m.V.Who + "'s "
				}
				text := fmt.Sprintf("Time for %s%s", whose, m.V.Name)
				if m.V.Dose != "" {
					text += " (" + m.V.Dose + ")"
				}
				s.notify(who, text, "#/health")
				if who != m.Owner {
					s.notify(m.Owner, text, "#/health")
				}
			}
		}
	}
	for _, h := range hhItems[healthRecord](s, admin, "health") {
		if h.V.Next == "" {
			continue
		}
		for _, before := range []int{14, 0} {
			d, err := time.ParseInLocation("2006-01-02", h.V.Next, s.location(h.Owner))
			if err != nil {
				continue
			}
			at := d.AddDate(0, 0, -before).Add(9 * time.Hour)
			if at.After(from) && !at.After(to) && s.once(fmt.Sprintf("health|%d|%s|%d", h.ID, h.V.Next, before), 3*24*time.Hour) {
				who := h.V.Who
				if users[who] {
					who = s.displayOf(who)
				}
				when := "today"
				if before > 0 {
					when = fmt.Sprintf("in %d days", before)
				}
				s.notify(h.Owner, fmt.Sprintf("%s: %s is due %s", who, h.V.Title, when), "#/health")
			}
		}
	}
	for _, p := range hhItems[pantryItem](s, admin, "pantry") {
		if p.V.Expires == "" {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", p.V.Expires, s.location(p.Owner))
		if err == nil && d.AddDate(0, 0, -2).Add(9*time.Hour).After(from) && !d.AddDate(0, 0, -2).Add(9*time.Hour).After(to) &&
			s.once(fmt.Sprintf("pantry|%d|%s", p.ID, p.V.Expires), 3*24*time.Hour) {
			s.notify(p.Owner, fmt.Sprintf("Use it soon: %s goes off %s", p.V.Name, d.Format("Monday")), "#/pantry")
		}
	}
	s.sendBriefings(from, to)
}
