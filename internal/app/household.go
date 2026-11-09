package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The household's own records, kept the same way: recipes and the meal plan, chores, home records (appliances,
// warranties, passports), bills and shared expenses, and trips. Everyone at home sees them (a home record can be
// private); kids don't see money or records, and can't change chores.

const householdSchema = `
CREATE TABLE IF NOT EXISTS hh (
	id INTEGER PRIMARY KEY,
	kind TEXT NOT NULL,
	owner TEXT NOT NULL,
	private INTEGER NOT NULL DEFAULT 0,
	data TEXT NOT NULL,
	created INTEGER NOT NULL,
	updated INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS hh_kind ON hh(kind);
CREATE TABLE IF NOT EXISTS chore_done (
	id INTEGER PRIMARY KEY,
	chore INTEGER NOT NULL,
	user TEXT NOT NULL,
	at INTEGER NOT NULL,
	points INTEGER NOT NULL,
	cents INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chore_done_at ON chore_done(at);
CREATE TABLE IF NOT EXISTS payouts (
	id INTEGER PRIMARY KEY,
	user TEXT NOT NULL,
	cents INTEGER NOT NULL,
	note TEXT NOT NULL,
	by TEXT NOT NULL,
	at INTEGER NOT NULL
);`

func migrateHousehold(db interface {
	Exec(string, ...any) (sql.Result, error)
}) {
	db.Exec(`ALTER TABLE users ADD COLUMN kid INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE fridge ADD COLUMN date TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE search_files ADD COLUMN embedded INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`UPDATE hh SET private = 1 WHERE kind = 'journal'`) // the journal is personal now
}

type cleaner interface {
	clean() error
	label() string
}

type hhKind struct {
	decode     func([]byte) (any, string, error)
	adultsOnly bool // kids don't see it
	adultsEdit bool // kids see it but don't change it
	privateOK  bool
	private    bool // always private: only its writer ever sees it (the journal)
	// view can hide a record from u or change what u sees of it; merge combines a change with what was there.
	view  func(u user, row hhRow, m map[string]any) bool
	merge func(u user, old, next []byte) []byte
}

func decodeAs[T any, P interface {
	*T
	cleaner
}](raw []byte) (any, string, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, "", fmt.Errorf("that didn't read: %w", err)
	}
	p := P(&v)
	if err := p.clean(); err != nil {
		return nil, "", err
	}
	return v, p.label(), nil
}

var hhKinds = map[string]hhKind{
	"recipe":  {decode: decodeAs[recipe]},
	"meal":    {decode: decodeAs[meal]},
	"chore":   {decode: decodeAs[chore], adultsEdit: true},
	"record":  {decode: decodeAs[record], adultsOnly: true, privateOK: true},
	"bill":    {decode: decodeAs[bill], adultsOnly: true},
	"expense": {decode: decodeAs[expense], adultsOnly: true},
	"trip":    {decode: decodeAs[trip]},
	"wish":    {decode: decodeAs[wish], view: wishView, merge: wishMerge},
	"med":     {decode: decodeAs[med], privateOK: true},
	"health":  {decode: decodeAs[healthRecord], privateOK: true},
	"pantry":  {decode: decodeAs[pantryItem]},
	"journal": {decode: decodeAs[journal], private: true},
	"reward":  {decode: decodeAs[reward], adultsEdit: true},
	"upkeep":  {decode: decodeAs[upkeep]},
}

// --- the kinds

func trimList(in []string, n int) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && len(out) < n {
			out = append(out, s)
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func optDate(s *string) error {
	if *s = strings.TrimSpace(*s); *s != "" && !validDate(*s) {
		return fmt.Errorf("%q isn't a date (YYYY-MM-DD)", *s)
	}
	return nil
}

type recipe struct {
	Title       string   `json:"title"`
	Serves      int      `json:"serves"`
	Minutes     int      `json:"minutes"`
	Ingredients []string `json:"ingredients"`
	Steps       string   `json:"steps"`
	Tags        []string `json:"tags"`
	Link        string   `json:"link"`
}

func (r *recipe) clean() error {
	r.Title, r.Steps, r.Link = clip(r.Title, 200), clip(r.Steps, 20000), safeLink(r.Link)
	r.Ingredients, r.Tags = trimList(r.Ingredients, 100), trimList(r.Tags, 20)
	if r.Title == "" {
		return fmt.Errorf("A recipe needs a name.")
	}
	return nil
}
func (r *recipe) label() string { return r.Title }

type meal struct {
	Date   string `json:"date"`
	Slot   string `json:"slot"` // breakfast, lunch, dinner, snack
	Recipe int64  `json:"recipe"`
	Text   string `json:"text"` // when it isn't a recipe: "Leftovers", "Pizza night"
	Cook   string `json:"cook"`
}

func (m *meal) clean() error {
	m.Text, m.Cook = clip(m.Text, 200), clip(m.Cook, 64)
	if !validDate(m.Date) {
		return fmt.Errorf("A meal needs a day.")
	}
	if !slices.Contains([]string{"breakfast", "lunch", "dinner", "snack"}, m.Slot) {
		m.Slot = "dinner"
	}
	if m.Recipe == 0 && m.Text == "" {
		return fmt.Errorf("Pick a recipe or write what's for %s.", m.Slot)
	}
	return nil
}
func (m *meal) label() string { return m.Text }

type chore struct {
	Title  string   `json:"title"`
	Who    []string `json:"who"`    // empty: anyone
	Rotate bool     `json:"rotate"` // take turns (by day for daily chores, by week otherwise)
	Repeat string   `json:"repeat"` // daily, weekdays, weekly:0-6 (Sunday 0), monthly:1-31, once
	Points int      `json:"points"`
	Cents  int      `json:"cents"` // allowance for doing it
	Paused bool     `json:"paused"`
}

func (c *chore) clean() error {
	c.Title, c.Who = clip(c.Title, 200), trimList(c.Who, 20)
	if c.Title == "" {
		return fmt.Errorf("A chore needs a name.")
	}
	kind, n, _ := strings.Cut(c.Repeat, ":")
	d, err := strconv.Atoi(n)
	switch {
	case c.Repeat == "daily" || c.Repeat == "weekdays" || c.Repeat == "once":
	case kind == "weekly" && err == nil && d >= 0 && d <= 6:
	case kind == "monthly" && err == nil && d >= 1 && d <= 31:
	default:
		c.Repeat = "weekly:6"
	}
	c.Points, c.Cents = min(max(c.Points, 0), 1000), min(max(c.Cents, 0), 100000)
	return nil
}
func (c *chore) label() string { return c.Title }

// dueOn says whether the chore comes up on day.
func (c *chore) dueOn(day time.Time) bool {
	if c.Paused {
		return false
	}
	kind, n, _ := strings.Cut(c.Repeat, ":")
	d, _ := strconv.Atoi(n)
	switch kind {
	case "daily", "once":
		return true
	case "weekdays":
		return day.Weekday() >= time.Monday && day.Weekday() <= time.Friday
	case "weekly":
		return int(day.Weekday()) == d
	case "monthly":
		last := time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, day.Location()).Day()
		return day.Day() == min(d, last)
	}
	return false
}

// whoOn is who the chore is for on day ("" for anyone).
func (c *chore) whoOn(day time.Time) []string {
	if !c.Rotate || len(c.Who) < 2 {
		return c.Who
	}
	n := day.Unix() / 86400
	if c.Repeat != "daily" && c.Repeat != "weekdays" {
		n = (n + 3) / 7 // weeks starting Monday
	}
	return []string{c.Who[int(n)%len(c.Who)]}
}

type record struct {
	Name     string   `json:"name"`
	Category string   `json:"category"` // appliance, electronics, document, vehicle, home, other
	Person   string   `json:"person"`   // whose (a passport)
	Brand    string   `json:"brand"`
	Model    string   `json:"model"`
	Serial   string   `json:"serial"`
	Where    string   `json:"where"` // where it was bought, or where it's kept
	Bought   string   `json:"bought"`
	Price    float64  `json:"price"`
	Warranty string   `json:"warranty"` // warranty ends
	Expires  string   `json:"expires"`  // passports, licences, insurance
	Files    []string `json:"files"`    // receipts and manuals in the owner's files
	Notes    string   `json:"notes"`
}

func (r *record) clean() error {
	for _, p := range []*string{&r.Name, &r.Category, &r.Person, &r.Brand, &r.Model, &r.Serial, &r.Where} {
		*p = clip(*p, 200)
	}
	r.Notes, r.Files = clip(r.Notes, 10000), trimList(r.Files, 30)
	if r.Name == "" {
		return fmt.Errorf("Give it a name.")
	}
	for _, d := range []*string{&r.Bought, &r.Warranty, &r.Expires} {
		if err := optDate(d); err != nil {
			return err
		}
	}
	return nil
}
func (r *record) label() string { return r.Name }

type bill struct {
	Name     string   `json:"name"`
	Amount   float64  `json:"amount"`
	Repeat   string   `json:"repeat"` // monthly, yearly, once
	Day      int      `json:"day"`    // of the month
	Month    int      `json:"month"`  // yearly bills
	Date     string   `json:"date"`   // once
	Payer    string   `json:"payer"`
	Category string   `json:"category"`
	Autopay  bool     `json:"autopay"`
	Paid     []string `json:"paid"` // periods paid: 2026-11 (monthly), 2026 (yearly), once
	Link     string   `json:"link"`
}

func (b *bill) clean() error {
	b.Name, b.Payer, b.Category, b.Link = clip(b.Name, 200), clip(b.Payer, 64), clip(b.Category, 64), safeLink(b.Link)
	b.Paid = trimList(b.Paid, 600)
	if b.Name == "" {
		return fmt.Errorf("A bill needs a name.")
	}
	switch b.Repeat {
	case "yearly":
		b.Month = min(max(b.Month, 1), 12)
	case "once":
		if !validDate(b.Date) {
			return fmt.Errorf("When is it due?")
		}
	default:
		b.Repeat = "monthly"
	}
	b.Day = min(max(b.Day, 1), 31)
	return nil
}
func (b *bill) label() string { return b.Name }

// due is when the bill is due in the month (zero time: not that month), and the period it pays.
func (b *bill) due(month time.Time) (time.Time, string) {
	day := func(d int) time.Time {
		last := time.Date(month.Year(), month.Month()+1, 0, 0, 0, 0, 0, month.Location()).Day()
		return time.Date(month.Year(), month.Month(), min(d, last), 0, 0, 0, 0, month.Location())
	}
	switch b.Repeat {
	case "yearly":
		if int(month.Month()) == b.Month {
			return day(b.Day), strconv.Itoa(month.Year())
		}
	case "once":
		if d, err := time.ParseInLocation("2006-01-02", b.Date, month.Location()); err == nil && d.Year() == month.Year() && d.Month() == month.Month() {
			return d, "once"
		}
	default:
		return day(b.Day), month.Format("2006-01")
	}
	return time.Time{}, ""
}

type expense struct {
	Title    string   `json:"title"`
	Amount   float64  `json:"amount"`
	Date     string   `json:"date"`
	PaidBy   string   `json:"paidBy"`
	Split    []string `json:"split"` // shared equally between them (empty: not shared)
	Category string   `json:"category"`
	Settle   bool     `json:"settle"`  // paying someone back: PaidBy gave Amount to Split[0]
	Receipt  string   `json:"receipt"` // its photo, in the owner's files
}

func (e *expense) clean() error {
	e.Title, e.PaidBy, e.Category, e.Split, e.Receipt = clip(e.Title, 200), clip(e.PaidBy, 64), clip(e.Category, 64), trimList(e.Split, 20), clip(e.Receipt, 500)
	if e.Amount <= 0 {
		return fmt.Errorf("How much was it?")
	}
	if !validDate(e.Date) {
		e.Date = time.Now().Format("2006-01-02")
	}
	if e.Title == "" && !e.Settle {
		return fmt.Errorf("What was it for?")
	}
	if e.Settle && len(e.Split) != 1 {
		return fmt.Errorf("Paying back is to one person.")
	}
	return nil
}
func (e *expense) label() string { return e.Title }

type tripStop struct {
	Date  string `json:"date"`
	Time  string `json:"time"`
	Title string `json:"title"`
	Where string `json:"where"`
	Ref   string `json:"ref"` // booking number
	Notes string `json:"notes"`
}

type packItem struct {
	Text string `json:"text"`
	Who  string `json:"who"`
	Done bool   `json:"done"`
}

type trip struct {
	Name    string     `json:"name"`
	Where   string     `json:"where"`
	Start   string     `json:"start"`
	End     string     `json:"end"`
	People  []string   `json:"people"`
	Plan    []tripStop `json:"plan"`
	Packing []packItem `json:"packing"`
	Files   []string   `json:"files"`
	Notes   string     `json:"notes"`
	Event   string     `json:"event"` // the calendar event made for it, "cal/name"
}

func (t *trip) clean() error {
	t.Name, t.Where, t.Notes, t.Event = clip(t.Name, 200), clip(t.Where, 200), clip(t.Notes, 20000), clip(t.Event, 300)
	t.People, t.Files = trimList(t.People, 30), trimList(t.Files, 50)
	if t.Name == "" {
		return fmt.Errorf("A trip needs a name.")
	}
	if !validDate(t.Start) {
		return fmt.Errorf("When does it start?")
	}
	if !validDate(t.End) || t.End < t.Start {
		t.End = t.Start
	}
	plan := []tripStop{}
	for _, p := range t.Plan {
		p.Title, p.Where, p.Ref, p.Notes, p.Time = clip(p.Title, 200), clip(p.Where, 200), clip(p.Ref, 100), clip(p.Notes, 2000), clip(p.Time, 5)
		if p.Title != "" && validDate(p.Date) && len(plan) < 300 {
			plan = append(plan, p)
		}
	}
	sort.SliceStable(plan, func(i, j int) bool { return plan[i].Date+plan[i].Time < plan[j].Date+plan[j].Time })
	t.Plan = plan
	pack := []packItem{}
	for _, p := range t.Packing {
		if p.Text = clip(p.Text, 200); p.Text != "" && len(pack) < 500 {
			p.Who = clip(p.Who, 64)
			pack = append(pack, p)
		}
	}
	t.Packing = pack
	return nil
}
func (t *trip) label() string { return t.Name }

// --- storage

type hhRow struct {
	ID      int64
	Kind    string
	Owner   string
	Private bool
	Data    []byte
	Created int64
	Updated int64
}

func (s *Server) hhRows(kind string) []hhRow {
	rows, err := s.db.Query(`SELECT id, kind, owner, private, data, created, updated FROM hh WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []hhRow
	for rows.Next() {
		var r hhRow
		if rows.Scan(&r.ID, &r.Kind, &r.Owner, &r.Private, &r.Data, &r.Created, &r.Updated) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) hhRow(kind string, id string) (hhRow, bool) {
	var r hhRow
	err := s.db.QueryRow(`SELECT id, kind, owner, private, data, created, updated FROM hh WHERE kind = ? AND id = ?`, kind, id).
		Scan(&r.ID, &r.Kind, &r.Owner, &r.Private, &r.Data, &r.Created, &r.Updated)
	return r, err == nil
}

// hhItems are the kind's records u sees, decoded.
func hhItems[T any](s *Server, u user, kind string) []struct {
	hhRow
	V T
} {
	var out []struct {
		hhRow
		V T
	}
	for _, r := range s.hhRows(kind) {
		if r.Private && r.Owner != u.Name && u.Name != "" { // no name: Cove itself (reminders)
			continue
		}
		var v T
		if json.Unmarshal(r.Data, &v) == nil {
			out = append(out, struct {
				hhRow
				V T
			}{r, v})
		}
	}
	return out
}

func (s *Server) hhJSON(r hhRow) map[string]any {
	m := map[string]any{}
	json.Unmarshal(r.Data, &m)
	m["id"], m["owner"], m["who"], m["private"], m["created"], m["updated"] = r.ID, r.Owner, s.displayOf(r.Owner), r.Private, r.Created, r.Updated
	return m
}

func (s *Server) hhKindFor(w http.ResponseWriter, r *http.Request, u user) (string, hhKind, bool) {
	name := r.PathValue("kind")
	k, ok := hhKinds[name]
	if !ok {
		jsonError(w, http.StatusNotFound, "There's no such list.")
		return "", k, false
	}
	if k.adultsOnly && u.Kid {
		jsonError(w, http.StatusForbidden, "That's for grown-ups.")
		return "", k, false
	}
	return name, k, true
}

// everyone is every user's name.
func (s *Server) everyone() []string {
	return everyoneBut(s.db, "")
}

func (s *Server) listHH(w http.ResponseWriter, r *http.Request, u user) {
	kind, k, ok := s.hhKindFor(w, r, u)
	if !ok {
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	out := []map[string]any{}
	for _, row := range s.hhRows(kind) {
		if row.Private && row.Owner != u.Name {
			continue
		}
		m := s.hhJSON(row)
		if d, ok := m["date"].(string); ok && (from != "" && d < from || to != "" && d > to) {
			continue
		}
		if k.view != nil && !k.view(u, row, m) {
			continue
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

type hhInput struct {
	Private bool `json:"private"`
}

func (s *Server) saveHH(w http.ResponseWriter, r *http.Request, u user) {
	kind, k, ok := s.hhKindFor(w, r, u)
	if !ok {
		return
	}
	if k.adultsEdit && u.Kid {
		jsonError(w, http.StatusForbidden, "Ask a grown-up to change chores.")
		return
	}
	raw, err := readBody(r, 2<<20)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	v, label, err := k.decode(raw)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var in hhInput
	json.Unmarshal(raw, &in)
	in.Private = in.Private && k.privateOK || k.private
	data, _ := json.Marshal(v)
	now := time.Now().Unix()
	var row hhRow
	if id := r.PathValue("id"); id != "" {
		old, found := s.hhRow(kind, id)
		if !found || old.Private && old.Owner != u.Name || !visibleTo(k, u, old) {
			jsonError(w, http.StatusNotFound, "That's gone.")
			return
		}
		if u.Kid && old.Owner != u.Name {
			jsonError(w, http.StatusForbidden, "Only whoever made it can change it.")
			return
		}
		if old.Owner != u.Name {
			in.Private = old.Private || k.private
		}
		if k.merge != nil {
			data = k.merge(u, old.Data, data)
		}
		s.db.Exec(`UPDATE hh SET data = ?, private = ?, updated = ? WHERE id = ?`, data, in.Private, now, old.ID)
		row = old
		row.Data, row.Private, row.Updated = data, in.Private, now
	} else {
		res, err := s.db.Exec(`INSERT INTO hh (kind, owner, private, data, created, updated) VALUES (?, ?, ?, ?, ?, ?)`, kind, u.Name, in.Private, data, now, now)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		row.ID, _ = res.LastInsertId()
		row.Kind, row.Owner, row.Private, row.Data, row.Created, row.Updated = kind, u.Name, in.Private, data, now, now
		s.hhAdded(u, kind, v, label)
	}
	s.hhChanged(u, row)
	m := s.hhJSON(row)
	if k.view != nil {
		k.view(u, row, m)
	}
	writeJSON(w, map[bool]int{true: http.StatusOK, false: http.StatusCreated}[r.PathValue("id") != ""], m)
}

// hidden2 says whether u may see the record at all, by the kind's own rule.
func visibleTo(k hhKind, u user, row hhRow) bool {
	if k.view == nil {
		return true
	}
	m := map[string]any{}
	json.Unmarshal(row.Data, &m)
	return k.view(u, row, m)
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	b := make([]byte, 0, 4096)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buf)
		b = append(b, buf[:n]...)
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("that's too big")
		}
		if err != nil {
			return b, nil
		}
	}
}

// hhAdded tells the people a new chore or trip is for.
func (s *Server) hhAdded(u user, kind string, v any, label string) {
	var people []string
	var text, link string
	switch x := v.(type) {
	case chore:
		people, text, link = x.Who, fmt.Sprintf("%s gave you a chore: %s", displayName(u), label), "#/chores"
	case trip:
		people, text, link = x.People, fmt.Sprintf("%s added you to a trip: %s", displayName(u), label), "#/trips"
	case wish:
		if x.For != "" && x.For != u.Name { // ideas for someone stay a surprise; tell the others
			for _, p := range everyoneBut(s.db, u.Name) {
				if p != x.For {
					people = append(people, p)
				}
			}
			text, link = fmt.Sprintf("%s added a gift idea for %s", displayName(u), s.displayOf(x.For)), "#/wishes"
		}
	}
	for _, p := range people {
		if p != u.Name {
			s.notify(p, text, link)
		}
	}
}

func (s *Server) hhChanged(u user, row hhRow) {
	if row.Private {
		s.changed("household", u.Name)
		return
	}
	s.changed("household", s.everyone()...)
}

func (s *Server) deleteHH(w http.ResponseWriter, r *http.Request, u user) {
	kind, k, ok := s.hhKindFor(w, r, u)
	if !ok {
		return
	}
	row, found := s.hhRow(kind, r.PathValue("id"))
	if !found || row.Private && row.Owner != u.Name || !visibleTo(k, u, row) {
		jsonError(w, http.StatusNotFound, "That's gone.")
		return
	}
	if (k.adultsEdit || row.Owner != u.Name) && u.Kid {
		jsonError(w, http.StatusForbidden, "Only whoever made it can remove it.")
		return
	}
	s.db.Exec(`DELETE FROM hh WHERE id = ?`, row.ID)
	if kind == "chore" {
		s.db.Exec(`DELETE FROM chore_done WHERE chore = ?`, row.ID)
	}
	if kind == "recipe" { // meals that pointed at it keep its name
		for _, m := range hhItems[meal](s, u, "meal") {
			if m.V.Recipe == row.ID {
				var rc recipe
				json.Unmarshal(row.Data, &rc)
				m.V.Recipe, m.V.Text = 0, rc.Title
				data, _ := json.Marshal(m.V)
				s.db.Exec(`UPDATE hh SET data = ? WHERE id = ?`, data, m.ID)
			}
		}
	}
	s.hhChanged(u, row)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- meals: the week's ingredients onto the fridge's shopping list

func (s *Server) mealsToShopping(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		From    string  `json:"from"`
		To      string  `json:"to"`
		Recipes []int64 `json:"recipes"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	want := map[int64]bool{}
	for _, id := range in.Recipes {
		want[id] = true
	}
	if in.From != "" {
		for _, m := range hhItems[meal](s, u, "meal") {
			if m.V.Recipe != 0 && m.V.Date >= in.From && (in.To == "" || m.V.Date <= in.To) {
				want[m.V.Recipe] = true
			}
		}
	}
	var ingredients []string
	for _, rc := range hhItems[recipe](s, u, "recipe") {
		if want[rc.ID] {
			ingredients = append(ingredients, rc.V.Ingredients...)
		}
	}
	if len(ingredients) == 0 {
		jsonError(w, http.StatusBadRequest, "Those meals have no ingredients written down.")
		return
	}
	added, err := s.addToShopping(u, ingredients)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": added})
}

// addToShopping adds what's not on it yet to the fridge's shopping list (making one if there's none).
func (s *Server) addToShopping(u user, things []string) (int, error) {
	var id int64
	var items string
	err := s.db.QueryRow(`SELECT id, items FROM fridge WHERE kind = 'list' AND lower(trim(text)) IN ('shopping', 'shopping list', 'groceries')
		AND (expires = 0 OR expires > ?) ORDER BY created DESC LIMIT 1`, time.Now().Unix()).Scan(&id, &items)
	var list []fridgeItem
	json.Unmarshal([]byte(items), &list)
	have := map[string]bool{}
	for _, it := range list {
		if !it.Done {
			have[strings.ToLower(it.Text)] = true
		}
	}
	added := 0
	for _, t := range things {
		if t = clip(t, 200); t != "" && !have[strings.ToLower(t)] && len(list) < 100 {
			list = append(list, fridgeItem{Text: t})
			have[strings.ToLower(t)] = true
			added++
		}
	}
	b, _ := json.Marshal(list)
	if err != nil {
		if _, err := s.db.Exec(`INSERT INTO fridge (author, kind, text, color, items, created) VALUES (?, 'list', 'Shopping', 'green', ?, ?)`, u.Name, b, time.Now().Unix()); err != nil {
			return 0, err
		}
	} else if added > 0 {
		s.db.Exec(`UPDATE fridge SET items = ? WHERE id = ?`, b, id)
	}
	if added > 0 {
		s.putUp(u, fmt.Sprintf("%d things on the shopping list", added), "")
	}
	return added, nil
}

// --- chores: who does what today, ticking them off, points and allowance

type choreView struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	For      []string `json:"for"` // today
	Repeat   string   `json:"repeat"`
	Points   int      `json:"points"`
	Cents    int      `json:"cents"`
	DueToday bool     `json:"dueToday"`
	DoneBy   string   `json:"doneBy"` // today (or this week for weekly chores, ever for one-off)
	DoneID   int64    `json:"doneId"`
	Chore    chore    `json:"chore"`
	Owner    string   `json:"owner"`
}

type choreScore struct {
	Name     string `json:"name"`
	Who      string `json:"who"`
	Kid      bool   `json:"kid"`
	Week     int    `json:"week"`     // points this week
	Earned   int    `json:"earned"`   // cents, all time
	Paid     int    `json:"paid"`     // cents, all time
	Balance  int    `json:"balance"`  // still owed
	Streak   int    `json:"streak"`   // days in a row with a chore done
	WeekDone int    `json:"weekDone"` // chores done this week
}

func startOfWeek(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

func (s *Server) choreBoard(w http.ResponseWriter, r *http.Request, u user) {
	loc := s.location(u.Name)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	week := startOfWeek(now)
	type done struct {
		id, chore int64
		user      string
		at        int64
		points    int
		cents     int
	}
	var all []done
	if rows, err := s.db.Query(`SELECT id, chore, user, at, points, cents FROM chore_done ORDER BY at`); err == nil {
		for rows.Next() {
			var d done
			if rows.Scan(&d.id, &d.chore, &d.user, &d.at, &d.points, &d.cents) == nil {
				all = append(all, d)
			}
		}
		rows.Close()
	}
	chores := []choreView{}
	for _, c := range hhItems[chore](s, u, "chore") {
		v := choreView{ID: c.ID, Title: c.V.Title, For: c.V.whoOn(today), Repeat: c.V.Repeat, Points: c.V.Points, Cents: c.V.Cents,
			DueToday: c.V.dueOn(today), Chore: c.V, Owner: c.Owner}
		since := today
		switch {
		case c.V.Repeat == "once":
			since = time.Unix(0, 0)
		case strings.HasPrefix(c.V.Repeat, "weekly"):
			since = week
		case strings.HasPrefix(c.V.Repeat, "monthly"):
			since = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
		}
		var doneAt int64
		for _, d := range all {
			if d.chore == c.ID && d.at >= since.Unix() {
				v.DoneBy, v.DoneID, doneAt = d.user, d.id, d.at
			}
		}
		if c.V.Repeat == "once" && v.DoneBy != "" && time.Since(time.Unix(doneAt, 0)) > 7*24*time.Hour {
			continue // done long ago
		}
		chores = append(chores, v)
	}
	people := []choreScore{}
	if rows, err := s.db.Query(`SELECT name, coalesce(nullif(display, ''), name), kid FROM users ORDER BY kid DESC, lower(coalesce(nullif(display, ''), name))`); err == nil {
		for rows.Next() {
			var p choreScore
			if rows.Scan(&p.Name, &p.Who, &p.Kid) == nil {
				people = append(people, p)
			}
		}
		rows.Close()
	}
	paid := map[string]int{}
	if rows, err := s.db.Query(`SELECT user, sum(cents) FROM payouts GROUP BY user`); err == nil {
		for rows.Next() {
			var n string
			var c int
			if rows.Scan(&n, &c) == nil {
				paid[n] = c
			}
		}
		rows.Close()
	}
	for i := range people {
		p := &people[i]
		days := map[string]bool{}
		for _, d := range all {
			if d.user != p.Name {
				continue
			}
			p.Earned += d.cents
			days[time.Unix(d.at, 0).In(loc).Format("2006-01-02")] = true
			if d.at >= week.Unix() {
				p.Week += d.points
				p.WeekDone++
			}
		}
		for d := today; days[d.Format("2006-01-02")] || (d.Equal(today) && days[d.AddDate(0, 0, -1).Format("2006-01-02")]); d = d.AddDate(0, 0, -1) {
			if days[d.Format("2006-01-02")] {
				p.Streak++
			}
		}
		p.Paid = paid[p.Name]
		p.Balance = p.Earned - p.Paid
	}
	type recent struct {
		ID     int64  `json:"id"`
		Chore  string `json:"chore"`
		Who    string `json:"who"`
		User   string `json:"user"`
		At     int64  `json:"at"`
		Points int    `json:"points"`
	}
	titles := map[int64]string{}
	for _, c := range chores {
		titles[c.ID] = c.Title
	}
	rec := []recent{}
	for i := len(all) - 1; i >= 0 && len(rec) < 30; i-- {
		d := all[i]
		if t, ok := titles[d.chore]; ok {
			rec = append(rec, recent{d.id, t, s.displayOf(d.user), d.user, d.at, d.points})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chores": chores, "people": people, "recent": rec})
}

func (s *Server) choreDone(w http.ResponseWriter, r *http.Request, u user) {
	row, found := s.hhRow("chore", r.PathValue("id"))
	if !found {
		jsonError(w, http.StatusNotFound, "That chore is gone.")
		return
	}
	var c chore
	json.Unmarshal(row.Data, &c)
	var in struct {
		Who string `json:"who"` // a grown-up ticks it for someone
	}
	readJSON(r, &in)
	who := u.Name
	if in.Who != "" && in.Who != u.Name {
		if u.Kid {
			jsonError(w, http.StatusForbidden, "Tick off your own chores.")
			return
		}
		who = in.Who
	}
	loc := s.location(u.Name)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if c.Paused {
		jsonError(w, http.StatusBadRequest, "That chore is paused.")
		return
	}
	if forToday := c.whoOn(today); u.Kid && len(forToday) > 0 && !slices.Contains(forToday, u.Name) {
		jsonError(w, http.StatusForbidden, "That one isn't yours today.")
		return
	}
	// once each time it comes up: today, this week, this month, or ever for a one-off
	since := today
	switch {
	case c.Repeat == "once":
		since = time.Unix(0, 0)
	case strings.HasPrefix(c.Repeat, "weekly"):
		since = startOfWeek(now)
	case strings.HasPrefix(c.Repeat, "monthly"):
		since = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	}
	var done int
	s.db.QueryRow(`SELECT count(*) FROM chore_done WHERE chore = ? AND at >= ?`, row.ID, since.Unix()).Scan(&done)
	if done > 0 {
		jsonError(w, http.StatusConflict, "That's done already.")
		return
	}
	res, err := s.db.Exec(`INSERT INTO chore_done (chore, user, at, points, cents) VALUES (?, ?, ?, ?, ?)`, row.ID, who, time.Now().Unix(), c.Points, c.Cents)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	if row.Owner != who && row.Owner != u.Name {
		msg := fmt.Sprintf("%s did a chore: %s", s.displayOf(who), c.Title)
		if c.Points > 0 {
			msg += fmt.Sprintf(" (+%d)", c.Points)
		}
		s.notify(row.Owner, msg, "#/chores")
	}
	s.changed("household", s.everyone()...)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) choreUndo(w http.ResponseWriter, r *http.Request, u user) {
	var who string
	if s.db.QueryRow(`SELECT user FROM chore_done WHERE id = ?`, r.PathValue("id")).Scan(&who) != nil {
		jsonError(w, http.StatusNotFound, "That's not ticked.")
		return
	}
	if who != u.Name && u.Kid {
		jsonError(w, http.StatusForbidden, "Only your own.")
		return
	}
	s.db.Exec(`DELETE FROM chore_done WHERE id = ?`, r.PathValue("id"))
	s.changed("household", s.everyone()...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) payAllowance(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid {
		jsonError(w, http.StatusForbidden, "That's for grown-ups.")
		return
	}
	var in struct {
		User  string `json:"user"`
		Cents int    `json:"cents"`
		Note  string `json:"note"`
	}
	if err := readJSON(r, &in); err != nil || in.Cents == 0 || in.User == "" {
		jsonError(w, http.StatusBadRequest, "Who, and how much?")
		return
	}
	s.db.Exec(`INSERT INTO payouts (user, cents, note, by, at) VALUES (?, ?, ?, ?, ?)`, in.User, in.Cents, clip(in.Note, 200), u.Name, time.Now().Unix())
	if in.User != u.Name && in.Cents > 0 {
		s.notify(in.User, fmt.Sprintf("%s paid you %s allowance", displayName(u), money(in.Cents)), "#/chores")
	}
	s.changed("household", s.everyone()...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func money(cents int) string {
	return fmt.Sprintf("$%d.%02d", cents/100, abs(cents%100))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// --- budget: the month's bills and spending, and who owes whom

func (s *Server) budget(w http.ResponseWriter, r *http.Request, u user) {
	if u.Kid {
		jsonError(w, http.StatusForbidden, "That's for grown-ups.")
		return
	}
	loc := s.location(u.Name)
	month, err := time.ParseInLocation("2006-01", r.URL.Query().Get("month"), loc)
	if err != nil {
		now := time.Now().In(loc)
		month = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	}
	key := month.Format("2006-01")
	bills := []map[string]any{}
	var billTotal, billPaid float64
	for _, b := range hhItems[bill](s, u, "bill") {
		due, period := b.V.due(month)
		if due.IsZero() {
			continue
		}
		m := s.hhJSON(b.hhRow)
		m["dueDate"], m["period"], m["isPaid"] = due.Format("2006-01-02"), period, slices.Contains(b.V.Paid, period) || b.V.Autopay
		billTotal += b.V.Amount
		if m["isPaid"].(bool) {
			billPaid += b.V.Amount
		}
		bills = append(bills, m)
	}
	sort.Slice(bills, func(i, j int) bool { return bills[i]["dueDate"].(string) < bills[j]["dueDate"].(string) })
	expenses := []map[string]any{}
	byCat := map[string]float64{}
	var spent float64
	owe := map[string]float64{} // + is owed money, - owes
	for _, e := range hhItems[expense](s, u, "expense") {
		x := e.V
		switch {
		case x.Settle:
			owe[x.PaidBy] += x.Amount
			owe[x.Split[0]] -= x.Amount
		case len(x.Split) > 0:
			owe[x.PaidBy] += x.Amount
			for _, p := range x.Split {
				owe[p] -= x.Amount / float64(len(x.Split))
			}
		}
		if strings.HasPrefix(x.Date, key) {
			expenses = append(expenses, s.hhJSON(e.hhRow))
			if !x.Settle {
				spent += x.Amount
				cat := x.Category
				if cat == "" {
					cat = "Other"
				}
				byCat[cat] += x.Amount
			}
		}
	}
	for _, b := range bills {
		cat, _ := b["category"].(string)
		if cat == "" {
			cat = "Bills"
		}
		byCat[cat] += b["amount"].(float64)
	}
	sort.Slice(expenses, func(i, j int) bool { return expenses[i]["date"].(string) > expenses[j]["date"].(string) })
	// who pays whom to settle up: biggest debtor pays biggest creditor, again and again
	type settle struct {
		From   string  `json:"from"`
		To     string  `json:"to"`
		Amount float64 `json:"amount"`
	}
	var settles []settle
	type bal struct {
		n string
		v float64
	}
	var ups, downs []bal
	for n, v := range owe {
		if v > 0.004 {
			ups = append(ups, bal{n, v})
		} else if v < -0.004 {
			downs = append(downs, bal{n, -v})
		}
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].v > ups[j].v || ups[i].v == ups[j].v && ups[i].n < ups[j].n })
	sort.Slice(downs, func(i, j int) bool {
		return downs[i].v > downs[j].v || downs[i].v == downs[j].v && downs[i].n < downs[j].n
	})
	for i, j := 0, 0; i < len(downs) && j < len(ups); {
		a := min(downs[i].v, ups[j].v)
		settles = append(settles, settle{downs[i].n, ups[j].n, float64(int(a*100+0.5)) / 100})
		downs[i].v -= a
		ups[j].v -= a
		if downs[i].v < 0.005 {
			i++
		}
		if ups[j].v < 0.005 {
			j++
		}
	}
	if settles == nil {
		settles = []settle{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": key, "bills": bills, "expenses": expenses, "byCategory": byCat,
		"billTotal": billTotal, "billPaid": billPaid, "spent": spent, "settle": settles})
}

// --- reminders: warranties and documents running out, bills coming due, chores, packing for a trip

func (s *Server) householdReminders(from, to time.Time) {
	at9 := func(user, date string, daysBefore int) time.Time {
		d, err := time.ParseInLocation("2006-01-02", date, s.location(user))
		if err != nil {
			return time.Time{}
		}
		return d.AddDate(0, 0, -daysBefore).Add(9 * time.Hour)
	}
	in := func(t time.Time) bool { return t.After(from) && !t.After(to) }
	admin := user{Admin: true}
	for _, rc := range hhItems[record](s, admin, "record") {
		for _, x := range []struct{ date, what string }{{rc.V.Warranty, "warranty ends"}, {rc.V.Expires, "expires"}} {
			if x.date == "" {
				continue
			}
			for _, before := range []int{60, 14, 0} {
				if in(at9(rc.Owner, x.date, before)) && s.once(fmt.Sprintf("rec|%d|%s|%s|%d", rc.ID, x.what, x.date, before), 3*24*time.Hour) {
					when := "today"
					if before > 0 {
						when = fmt.Sprintf("in %d days", before)
					}
					s.notify(rc.Owner, fmt.Sprintf("%s: %s %s", rc.V.Name, x.what, when), "#/records")
				}
			}
		}
	}
	for _, b := range hhItems[bill](s, admin, "bill") {
		if b.V.Autopay {
			continue
		}
		who := b.V.Payer
		if who == "" {
			who = b.Owner
		}
		loc := s.location(who)
		for _, m := range []time.Time{from.In(loc), from.In(loc).AddDate(0, 1, 0)} {
			due, period := b.V.due(time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, loc))
			if due.IsZero() || slices.Contains(b.V.Paid, period) {
				continue
			}
			for _, before := range []int{3, 0} {
				if in(due.AddDate(0, 0, -before).Add(9*time.Hour)) && s.once(fmt.Sprintf("bill|%d|%s|%d", b.ID, period, before), 3*24*time.Hour) {
					when := "today"
					if before > 0 {
						when = due.Format("Monday")
					}
					s.notify(who, fmt.Sprintf("Bill due %s: %s, $%.2f", when, b.V.Name, b.V.Amount), "#/budget")
				}
			}
		}
	}
	for _, t := range hhItems[trip](s, admin, "trip") {
		for _, p := range append([]string{t.Owner}, t.V.People...) {
			if in(at9(p, t.V.Start, 2)) && s.once(fmt.Sprintf("trip|%d|%s", t.ID, p), 5*24*time.Hour) {
				s.notify(p, fmt.Sprintf("%s starts in 2 days. Time to pack!", t.V.Name), "#/trips")
			}
		}
	}
	s.familyReminders(from, to)
	s.upkeepReminders(from, to)
	// chores still to do at 4 in the afternoon
	for _, name := range s.everyone() {
		loc := s.location(name)
		day := to.In(loc)
		day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		if !in(day.Add(16 * time.Hour)) {
			continue
		}
		var left []string
		for _, c := range hhItems[chore](s, admin, "chore") {
			if c.V.Repeat != "daily" && c.V.Repeat != "weekdays" || !c.V.dueOn(day) || !slices.Contains(c.V.whoOn(day), name) {
				continue
			}
			var n int
			s.db.QueryRow(`SELECT count(*) FROM chore_done WHERE chore = ? AND at >= ?`, c.ID, day.Unix()).Scan(&n)
			if n == 0 {
				left = append(left, c.V.Title)
			}
		}
		if len(left) > 0 && s.once("chores|"+name+"|"+day.Format("2006-01-02"), 20*time.Hour) {
			s.notify(name, "Chores left today: "+strings.Join(left, ", "), "#/chores")
		}
	}
}
