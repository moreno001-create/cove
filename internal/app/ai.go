package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// Ask Cove: questions answered from the household's own things (calendar, tasks, the fridge, chores, meals, bills,
// trips, records and documents) by a model on the home network, an OpenAI-compatible server such as LM Studio or
// Ollama. Nothing goes to the internet. Documents are also turned into embeddings, so search finds things by
// meaning ("car insurance" finds the policy that never says "car").

const aiSchema = `
CREATE TABLE IF NOT EXISTS ai_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS embeddings (
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	chunk INTEGER NOT NULL,
	text TEXT NOT NULL,
	vec BLOB NOT NULL,
	PRIMARY KEY (owner, path, chunk)
);`

type aiConfig struct {
	URL    string `json:"url"`    // http://host:1234/v1
	Model  string `json:"model"`  // for answers
	Embed  string `json:"embed"`  // for smart search ("" turns it off)
	Vision string `json:"vision"` // a model that sees pictures, for finding photos by what's in them ("" off)
}

func (s *Server) aiConfig() aiConfig {
	c := aiConfig{URL: envOr("COVE_AI_URL", ""), Model: envOr("COVE_AI_MODEL", ""), Embed: envOr("COVE_AI_EMBED_MODEL", "")}
	rows, err := s.db.Query(`SELECT key, value FROM ai_settings`)
	if err != nil {
		return c
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			switch k {
			case "url":
				c.URL = v
			case "model":
				c.Model = v
			case "embed":
				c.Embed = v
			case "vision":
				c.Vision = v
			}
		}
	}
	c.URL = strings.TrimSuffix(c.URL, "/")
	return c
}

func (c aiConfig) on() bool { return c.URL != "" && c.Model != "" }

var aiHTTP = &http.Client{Timeout: 10 * time.Minute}

func (s *Server) aiPost(ctx context.Context, c aiConfig, endpoint string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := aiHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the AI at %s can't be reached: %w", c.URL, err)
	}
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		res.Body.Close()
		return nil, fmt.Errorf("the AI answered %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return res, nil
}

// embed turns texts into vectors (query: a question rather than a document; some models want to know).
func (s *Server) embed(ctx context.Context, c aiConfig, texts []string, query bool) ([][]float32, error) {
	if c.URL == "" || c.Embed == "" {
		return nil, errors.New("smart search isn't set up")
	}
	in := make([]string, len(texts))
	for i, t := range texts {
		if strings.Contains(strings.ToLower(c.Embed), "nomic") {
			if query {
				t = "search_query: " + t
			} else {
				t = "search_document: " + t
			}
		}
		in[i] = t
	}
	res, err := s.aiPost(ctx, c, "/embeddings", map[string]any{"model": c.Embed, "input": in})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = normalize(d.Embedding)
		}
	}
	for _, v := range vecs {
		if v == nil {
			return nil, errors.New("the AI left some texts out")
		}
	}
	return vecs, nil
}

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n = math.Sqrt(n); n > 0 {
		for i := range v {
			v[i] = float32(float64(v[i]) / n)
		}
	}
	return v
}

func vecBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func dot(a []float32, b []byte) float64 {
	if len(b) != 4*len(a) {
		return -1
	}
	var d float64
	for i, x := range a {
		d += float64(x) * float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
	}
	return d
}

// chunks splits a document into pieces of about 1000 characters that overlap a little, at most 40.
func chunks(text string) []string {
	text = strings.Join(strings.Fields(text), " ")
	var out []string
	for len(text) > 0 && len(out) < 40 {
		n := min(len(text), 1000)
		if n < len(text) {
			if sp := strings.LastIndexByte(text[:n], ' '); sp > 600 {
				n = sp
			}
		}
		out = append(out, text[:n])
		if n == len(text) {
			break
		}
		back := min(150, n/4)
		text = text[n-back:]
	}
	return out
}

// embedPending makes embeddings for the user's documents whose text changed, for at most budget.
func (s *Server) embedPending(owner string, budget time.Duration) {
	c := s.aiConfig()
	if c.URL == "" || c.Embed == "" {
		return
	}
	start := time.Now()
	var paths []string
	if rows, err := s.db.Query(`SELECT path FROM search_files WHERE owner = ? AND embedded = 0`, owner); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				paths = append(paths, p)
			}
		}
		rows.Close()
	}
	for _, p := range paths {
		if time.Since(start) > budget {
			return
		}
		var body string
		s.db.QueryRow(`SELECT body FROM search_text WHERE owner = ? AND path = ?`, owner, p).Scan(&body)
		parts := chunks(body)
		if len(parts) > 0 {
			parts[0] = path.Base(p) + ": " + parts[0]
		}
		var vecs [][]float32
		for i := 0; i < len(parts); i += 16 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			v, err := s.embed(ctx, c, parts[i:min(i+16, len(parts))], false)
			cancel()
			if err != nil {
				return // try again next time
			}
			vecs = append(vecs, v...)
		}
		s.db.Exec(`DELETE FROM embeddings WHERE owner = ? AND path = ?`, owner, p)
		for i, v := range vecs {
			s.db.Exec(`INSERT INTO embeddings (owner, path, chunk, text, vec) VALUES (?, ?, ?, ?, ?)`, owner, p, i, parts[i], vecBytes(v))
		}
		s.db.Exec(`UPDATE search_files SET embedded = 1 WHERE owner = ? AND path = ?`, owner, p)
	}
}

type smartHit struct {
	Path  string
	Text  string
	Score float64
}

// smartSearch finds the parts of u's documents closest in meaning to q (the best part of each document).
func (s *Server) smartSearch(ctx context.Context, owner, q string, n int) ([]smartHit, error) {
	c := s.aiConfig()
	v, err := s.embed(ctx, c, []string{q}, true)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT path, text, vec FROM embeddings WHERE owner = ?`, owner)
	if err != nil {
		return nil, err
	}
	best := map[string]smartHit{}
	for rows.Next() {
		var h smartHit
		var vec []byte
		if rows.Scan(&h.Path, &h.Text, &vec) != nil {
			continue
		}
		if h.Score = dot(v[0], vec); h.Score > best[h.Path].Score {
			best[h.Path] = h
		}
	}
	rows.Close()
	out := make([]smartHit, 0, len(best))
	for _, h := range best {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// --- the admin's settings

func (s *Server) getAI(w http.ResponseWriter, r *http.Request, u user) {
	c := s.aiConfig()
	var docs, done int
	s.db.QueryRow(`SELECT count(*), coalesce(sum(embedded), 0) FROM search_files`).Scan(&docs, &done)
	var photos, described int
	s.db.QueryRow(`SELECT count(*) FROM photo_meta`).Scan(&photos)
	s.db.QueryRow(`SELECT count(*) FROM photo_described`).Scan(&described)
	writeJSON(w, http.StatusOK, map[string]any{"config": c, "documents": docs, "embedded": done, "ocr": ocrAvailable(), "photos": photos, "described": described})
}

func (s *Server) putAI(w http.ResponseWriter, r *http.Request, u user) {
	var in aiConfig
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.URL = strings.TrimSuffix(strings.TrimSpace(in.URL), "/")
	if in.URL != "" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		jsonError(w, http.StatusBadRequest, "The address starts with http:// (LM Studio's is like http://192.168.1.20:1234/v1).")
		return
	}
	if in.URL != "" && !strings.HasSuffix(in.URL, "/v1") {
		in.URL += "/v1"
	}
	old := s.aiConfig()
	for k, v := range map[string]string{"url": in.URL, "model": strings.TrimSpace(in.Model), "embed": strings.TrimSpace(in.Embed), "vision": strings.TrimSpace(in.Vision)} {
		s.db.Exec(`INSERT INTO ai_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v)
	}
	if old.Embed != in.Embed || old.URL != in.URL { // different vectors: make them again
		s.db.Exec(`DELETE FROM embeddings`)
		s.db.Exec(`UPDATE search_files SET embedded = 0`)
	}
	s.getAI(w, r, u)
}

// aiModels lists what the AI server has, to choose from.
func (s *Server) aiModels(w http.ResponseWriter, r *http.Request, u user) {
	base := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("url")), "/")
	if base == "" {
		base = s.aiConfig().URL
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		jsonError(w, http.StatusBadRequest, "Type the AI server's address first.")
		return
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	res, err := aiHTTP.Do(req)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "Can't reach "+base+": "+err.Error())
		return
	}
	defer res.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil {
		jsonError(w, http.StatusBadGateway, "That doesn't look like an OpenAI-compatible server (LM Studio, Ollama).")
		return
	}
	ids := []string{}
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": base, "models": ids})
}

// --- asking

type chatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type source struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// householdContext is what Cove knows that might answer the question, as plain text for the model.
func (s *Server) householdContext(ctx context.Context, u user, q, docPath string) (string, []source) {
	loc := s.location(u.Name)
	now := time.Now().In(loc)
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s. You're talking with %s.\n", now.Format("Monday, January 2, 2006, 3:04 PM (MST)"), displayName(u))
	var people []string
	for _, p := range s.everyone() {
		people = append(people, s.displayOf(p))
	}
	fmt.Fprintf(&b, "People at home in Cove: %s.\n", strings.Join(people, ", "))
	var sources []source
	if docPath != "" {
		p := "/" + cleanRel(docPath)
		var body string
		if s.db.QueryRow(`SELECT body FROM search_text WHERE owner = ? AND path = ?`, u.Name, strings.TrimPrefix(p, "/")).Scan(&body) != nil || body == "" {
			if f, err := s.data.Open(s.rel(u.Name, p)); err == nil {
				if info, err := f.Stat(); err == nil {
					body = extractText(path.Base(p), f, info.Size())
				}
				f.Close()
			}
		}
		if len(body) > 40000 {
			body = body[:40000] + "\n[…the rest is cut off]"
		}
		fmt.Fprintf(&b, "\n## The document %q\n%s\n", path.Base(p), body)
		return b.String(), []source{{p, path.Base(p)}}
	}
	b.WriteString("\n## Calendar (the last week and the next two months)\n")
	for i, o := range s.eventsFor(u, loc, now.AddDate(0, 0, -7), now.AddDate(0, 2, 0)) {
		if i >= 120 {
			break
		}
		fmt.Fprintf(&b, "- %s: %s", whenText(o, loc), o.Title)
		if o.Location != "" {
			fmt.Fprintf(&b, " (at %s)", o.Location)
		}
		if o.Notes != "" {
			fmt.Fprintf(&b, ", notes: %s", firstLine(o.Notes))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n## Tasks not done\n")
	for i, t := range s.tasksFor(u.Name, loc, "") {
		if t.Done || i > 150 {
			continue
		}
		fmt.Fprintf(&b, "- %s", t.Title)
		if t.Due != "" {
			fmt.Fprintf(&b, " (due %s)", t.Due)
		}
		if t.Assignee != "" {
			fmt.Fprintf(&b, " for %s", s.displayOf(t.Assignee))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n## On the fridge\n")
	for _, n := range s.fridgeNotes(u, 40) {
		fmt.Fprintf(&b, "- %s from %s: %s", n.Kind, n.Who, strings.ReplaceAll(n.Text, "\n", " / "))
		if n.Date != "" {
			fmt.Fprintf(&b, " (on %s)", n.Date)
		}
		for _, it := range n.Items {
			mark := ""
			if it.Done {
				mark = " ✓"
			}
			if len(it.Votes) > 0 {
				mark = fmt.Sprintf(" (%d votes)", len(it.Votes))
			}
			fmt.Fprintf(&b, "; %s%s", it.Text, mark)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n## Meals planned\n")
	recipes := map[int64]recipe{}
	for _, r := range hhItems[recipe](s, u, "recipe") {
		recipes[r.ID] = r.V
	}
	for _, m := range hhItems[meal](s, u, "meal") {
		if m.V.Date >= now.AddDate(0, 0, -3).Format("2006-01-02") && m.V.Date <= now.AddDate(0, 0, 14).Format("2006-01-02") {
			what := m.V.Text
			if r, ok := recipes[m.V.Recipe]; ok {
				what = r.Title
			}
			fmt.Fprintf(&b, "- %s %s: %s\n", m.V.Date, m.V.Slot, what)
		}
	}
	if len(recipes) > 0 {
		var names []string
		for _, r := range recipes {
			names = append(names, r.Title)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "Recipes saved: %s\n", strings.Join(names, ", "))
	}
	b.WriteString("\n## Chores\n")
	for _, c := range hhItems[chore](s, u, "chore") {
		who := "anyone"
		if w := c.V.whoOn(now); len(w) > 0 {
			who = strings.Join(w, ", ")
		}
		fmt.Fprintf(&b, "- %s (%s, %s today, %d points)\n", c.V.Title, c.V.Repeat, who, c.V.Points)
	}
	for _, t := range hhItems[trip](s, u, "trip") {
		if t.V.End < now.AddDate(0, 0, -7).Format("2006-01-02") {
			continue
		}
		fmt.Fprintf(&b, "\n## Trip: %s to %s, %s to %s\n", t.V.Name, t.V.Where, t.V.Start, t.V.End)
		for _, p := range t.V.Plan {
			fmt.Fprintf(&b, "- %s %s %s %s %s\n", p.Date, p.Time, p.Title, p.Where, p.Ref)
		}
	}
	b.WriteString("\n## Medicines and health\n")
	for _, d := range s.dosesOn(u, now) {
		fmt.Fprintf(&b, "- %s %s %s at %s today, %s\n", d.Who, d.Name, d.Dose, d.Time, map[bool]string{true: "taken by " + d.By, false: "not taken yet"}[d.Taken])
	}
	for _, h := range hhItems[healthRecord](s, u, "health") {
		fmt.Fprintf(&b, "- %s: %s (%s) on %s, next due %s, %s\n", h.V.Who, h.V.Title, h.V.Type, h.V.Date, h.V.Next, h.V.Provider)
	}
	b.WriteString("\n## Pantry\n")
	for _, p := range hhItems[pantryItem](s, u, "pantry") {
		fmt.Fprintf(&b, "- %s: %g %s", p.V.Name, p.V.Qty, p.V.Unit)
		if p.V.Qty <= p.V.Min {
			b.WriteString(" (running low)")
		}
		if p.V.Expires != "" {
			fmt.Fprintf(&b, ", use by %s", p.V.Expires)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n## Your own wish list\n")
	for _, x := range hhItems[wish](s, u, "wish") {
		if x.V.For == u.Name && x.Owner == u.Name {
			fmt.Fprintf(&b, "- %s\n", x.V.Title)
		}
	}
	if c := s.haConfig(); c.Token != "" && len(c.Entities) > 0 {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if devs, states, err := s.devices(hctx, u); err == nil {
			b.WriteString("\n## At home now (Home Assistant)\n")
			for _, d := range devs {
				fmt.Fprintf(&b, "- %s (%s): %s%s\n", d.Name, d.ID, d.State, d.Unit)
			}
			if e := s.energy(states); e != nil {
				for _, kv := range []struct {
					k    string
					v    *float64
					unit string
				}{{"Solar now", e.Solar, " W"}, {"Solar today", e.Today, " kWh"}, {"Battery", e.Battery, "%"}, {"House using", e.Load, " W"}, {"Grid", e.Grid, " W (+ from, − to)"}} {
					if kv.v != nil {
						fmt.Fprintf(&b, "- %s: %.1f%s\n", kv.k, *kv.v, kv.unit)
					}
				}
			}
			if wv := s.weather(states); wv != nil {
				fmt.Fprintf(&b, "- Weather: %s, %.0f%s\n", wv.State, wv.Temp, wv.Unit)
			}
		}
		cancel()
	}
	if !u.Kid {
		b.WriteString("\n## Home records\n")
		for _, r := range hhItems[record](s, u, "record") {
			fmt.Fprintf(&b, "- %s", r.V.Name)
			for _, kv := range [][2]string{{"for", r.V.Person}, {"brand", r.V.Brand}, {"model", r.V.Model}, {"serial", r.V.Serial}, {"bought", r.V.Bought}, {"warranty until", r.V.Warranty}, {"expires", r.V.Expires}, {"notes", firstLine(r.V.Notes)}} {
				if kv[1] != "" {
					fmt.Fprintf(&b, ", %s %s", kv[0], kv[1])
				}
			}
			b.WriteByte('\n')
		}
		b.WriteString("\n## Bills\n")
		for _, bl := range hhItems[bill](s, u, "bill") {
			fmt.Fprintf(&b, "- %s: $%.2f %s (day %d), paid: %s\n", bl.V.Name, bl.V.Amount, bl.V.Repeat, bl.V.Day, strings.Join(bl.V.Paid, ", "))
		}
		b.WriteString("\n## Spending this month and last\n")
		since := now.AddDate(0, -1, -now.Day()+1).Format("2006-01-02")
		for _, e := range hhItems[expense](s, u, "expense") {
			if e.V.Date >= since {
				fmt.Fprintf(&b, "- %s %s $%.2f paid by %s (%s)\n", e.V.Date, e.V.Title, e.V.Amount, e.V.PaidBy, e.V.Category)
			}
		}
	}
	// the files themselves: what's there, recently opened first (and, for a small library, how each begins)
	s.indexUser(u.Name, 2*time.Second)
	b.WriteString(s.filesOverview(u))
	// documents: by meaning, and by words
	seen := map[string]bool{}
	docs := 0
	if hits, err := s.smartSearch(ctx, u.Name, q, 6); err == nil {
		for _, h := range hits {
			if h.Score < 0.45 {
				continue
			}
			if docs == 0 {
				b.WriteString("\n## From documents\n")
			}
			seen[h.Path] = true
			docs++
			sources = append(sources, source{"/" + h.Path, path.Base(h.Path)})
			fmt.Fprintf(&b, "### %s\n%s\n", h.Path, h.Text)
		}
	}
	s.indexUser(u.Name, 2*time.Second)
	if fq := anyWords(q); fq != "" {
		if rows, err := s.db.Query(`SELECT path, snippet(search_text, 2, '', '', '…', 60) FROM search_text WHERE search_text MATCH ? AND owner = ? ORDER BY rank LIMIT 4`,
			fq, u.Name); err == nil {
			for rows.Next() {
				var p, snip string
				if rows.Scan(&p, &snip) == nil && !seen[p] {
					if docs == 0 {
						b.WriteString("\n## From documents\n")
					}
					docs++
					seen[p] = true
					sources = append(sources, source{"/" + p, path.Base(p)})
					fmt.Fprintf(&b, "### %s\n%s\n", p, snip)
				}
			}
			rows.Close()
		}
	}
	text := b.String()
	if len(text) > 60000 {
		text = text[:60000]
	}
	return text, sources
}

// stopWords are left out of a question's word search.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be by can could did do does for from has have how i in is it its me my of on or our
		should so than that the their them there these they this to us was we were what whats what's when where which who whom why will with would you your
		much many any some tell show find give get`) {
		stopWords[w] = true
	}
}

// anyWords is a full-text query for documents with any of the question's words (best matches first).
func anyWords(q string) string {
	var parts []string
	for _, w := range strings.Fields(ftsQuery(q)) {
		if bare := strings.ToLower(strings.Trim(w, `"*`)); len(bare) > 2 && !stopWords[bare] {
			parts = append(parts, w)
		}
	}
	return strings.Join(parts, " OR ")
}

// filesOverview lists u's files for the model: the recently opened, then the rest (up to 300), each with its
// folder, size and date; when there are only a few documents, the start of each one's text too.
func (s *Server) filesOverview(u user) string {
	var b strings.Builder
	root := "files/" + u.Name
	type f struct {
		path string
		info fs.FileInfo
	}
	var list []f
	total := 0
	fs.WalkDir(s.data.FS(), root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if hidden(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		total++
		if len(list) < 300 {
			if info, err := d.Info(); err == nil {
				list = append(list, f{strings.TrimPrefix(p, root+"/"), info})
			}
		}
		return nil
	})
	fmt.Fprintf(&b, "\n## %s's files (%d in all)\n", displayName(u), total)
	if total == 0 {
		b.WriteString("None yet.\n")
		return b.String()
	}
	recent := map[string]bool{}
	for _, r := range s.recentFor(u, 15) {
		if r.Owner == u.Name {
			recent[strings.TrimPrefix(r.Path, "/")] = true
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if recent[list[i].path] != recent[list[j].path] {
			return recent[list[i].path]
		}
		return list[i].info.ModTime().After(list[j].info.ModTime())
	})
	docs := 0
	for _, x := range list {
		if indexable(x.path) {
			docs++
		}
	}
	for _, x := range list {
		fmt.Fprintf(&b, "- /%s (%s, %s, changed %s", x.path, kindOf(x.path, false), fmtBytes(x.info.Size()), x.info.ModTime().Format("Jan 2, 2006"))
		if recent[x.path] {
			b.WriteString(", opened lately")
		}
		b.WriteString(")\n")
		if docs <= 12 && indexable(x.path) {
			var body string
			s.db.QueryRow(`SELECT body FROM search_text WHERE owner = ? AND path = ?`, u.Name, x.path).Scan(&body)
			if body = strings.Join(strings.Fields(body), " "); body != "" {
				fmt.Fprintf(&b, "  Begins: %s\n", firstWords(body, 600))
			} else {
				b.WriteString("  (no text in it yet)\n")
			}
		}
	}
	if total > len(list) {
		fmt.Fprintf(&b, "…and %d more.\n", total-len(list))
	}
	return b.String()
}

func whenText(o occurrence, loc *time.Location) string {
	start := occStart(o, loc)
	if o.AllDay {
		return start.Format("Mon Jan 2") + " (all day)"
	}
	return start.In(loc).Format("Mon Jan 2, 3:04 PM")
}

// ask answers a question as it's written: server-sent events of {"sources":…}, then {"text": "…"} pieces, then
// {"done": true} (or {"error": "…"}).
func (s *Server) ask(w http.ResponseWriter, r *http.Request, u user) {
	c := s.aiConfig()
	if !c.on() {
		jsonError(w, http.StatusServiceUnavailable, "Ask Cove isn't set up yet: an admin picks the home AI server in Admin → AI.")
		return
	}
	var in struct {
		Question string     `json:"question"`
		Path     string     `json:"path"` // ask about this document
		History  []chatTurn `json:"history"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Question) == "" {
		jsonError(w, http.StatusBadRequest, "Ask something first.")
		return
	}
	known, sources := s.householdContext(r.Context(), u, in.Question, in.Path)
	if sources == nil {
		sources = []source{}
	}
	system := "You are Cove, the helper inside a family's home app. Answer from what Cove knows below; if it isn't there, say you " +
		"don't see it in Cove (don't make things up), and you may add general knowledge clearly marked as such. Be brief, " +
		"warm and practical. Use the people's names. Dates are relative to Now. When you use a document, name it. " +
		"When asked to add, plan, put up, remind, switch or write something down, use the tools to do it, then say in one line what you did."
	if in.Path != "" {
		system = "You are Cove, the helper inside a family's home app. Answer about the document below; be brief and clear."
	}
	system += "\n\n# What Cove knows\n" + known
	msgs := []map[string]any{{"role": "system", "content": system}}
	for _, h := range in.History {
		if (h.Role == "user" || h.Role == "assistant") && len(msgs) < 20 {
			msgs = append(msgs, map[string]any{"role": h.Role, "content": clip(h.Content, 8000)})
		}
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": clip(in.Question, 8000)})
	var tools []tool
	if in.Path == "" {
		tools = s.tools(u)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	started := false
	send := func(v any) {
		started = true
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	// up to three rounds: the model may call tools, hear what happened, then answer
	for round := 0; round < 3; round++ {
		body := map[string]any{"model": c.Model, "messages": msgs, "stream": true, "temperature": 0.3}
		if len(tools) > 0 && round < 2 {
			body["tools"] = tools
		}
		res, err := s.aiPost(r.Context(), c, "/chat/completions", body)
		if err != nil {
			if !started {
				jsonError(w, http.StatusBadGateway, err.Error())
			} else {
				send(map[string]string{"error": err.Error()})
			}
			return
		}
		if round == 0 {
			send(map[string]any{"sources": sources})
		}
		calls, text, err := s.streamAnswer(res, send)
		res.Body.Close()
		if err != nil {
			send(map[string]string{"error": err.Error()})
			return
		}
		if len(calls) == 0 {
			break
		}
		assistant := map[string]any{"role": "assistant", "content": text, "tool_calls": calls}
		msgs = append(msgs, assistant)
		for _, call := range calls {
			result := s.act(r.Context(), u, call.Function.Name, call.Function.Arguments)
			send(map[string]string{"action": result})
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": result})
		}
	}
	send(map[string]bool{"done": true})
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamAnswer passes the model's words on as they come, and gathers any tool calls (they arrive in pieces).
func (s *Server) streamAnswer(res *http.Response, send func(any)) ([]toolCall, string, error) {
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	thinking := false
	var calls []toolCall
	var text strings.Builder
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if line == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || len(ev.Choices) == 0 {
			continue
		}
		d := ev.Choices[0].Delta
		for _, tc := range d.ToolCalls {
			for len(calls) <= tc.Index {
				calls = append(calls, toolCall{Type: "function"})
			}
			c := &calls[tc.Index]
			if tc.ID != "" {
				c.ID = tc.ID
			}
			c.Function.Name += tc.Function.Name
			c.Function.Arguments += tc.Function.Arguments
		}
		piece := d.Content
		// some models think out loud between <think> tags first: leave that out
		if strings.Contains(piece, "<think>") {
			thinking = true
		}
		if thinking {
			if _, after, ok := strings.Cut(piece, "</think>"); ok {
				thinking, piece = false, after
			} else {
				continue
			}
		}
		if piece != "" {
			text.WriteString(piece)
			send(map[string]string{"text": piece})
		}
	}
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	return calls, text.String(), sc.Err()
}
