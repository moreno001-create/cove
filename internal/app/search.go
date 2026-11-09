package app

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/xml"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-ical"
)

// One search for everything: file and folder names, the words inside documents (text, Markdown, Word, Excel,
// PowerPoint, OpenDocument, and PDFs whose text can be read), events, tasks and contacts. Document text is kept in a
// full-text index, brought up to date in the background and just before each search.

const searchSchema = `
CREATE TABLE IF NOT EXISTS search_files (
	owner TEXT NOT NULL,
	path TEXT NOT NULL,
	mod INTEGER NOT NULL,
	size INTEGER NOT NULL,
	PRIMARY KEY (owner, path)
);
CREATE VIRTUAL TABLE IF NOT EXISTS search_text USING fts5(owner UNINDEXED, path UNINDEXED, body, tokenize='unicode61 remove_diacritics 2');`

const maxIndexed = 40 << 20 // bigger files are found by name only

func indexable(name string) bool {
	switch kindOf(name, false) {
	case "text", "code", "doc", "sheet", "slides", "pdf":
		return true
	case "image": // words in photos of papers, with OCR
		ext := strings.ToLower(path.Ext(name))
		return ocrAvailable() && (ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".tif" || ext == ".tiff")
	}
	return false
}

// extractText reads the words in a document (up to about a megabyte of them).
func extractText(name string, r io.ReaderAt, size int64) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	var parts []string
	switch ext {
	case "docx", "xlsx", "pptx", "odt", "ods", "odp":
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return ""
		}
		for _, f := range zr.File {
			n := f.Name
			if n == "word/document.xml" || n == "xl/sharedStrings.xml" || n == "content.xml" ||
				(strings.HasPrefix(n, "ppt/slides/slide") && strings.HasSuffix(n, ".xml")) {
				if rc, err := f.Open(); err == nil {
					parts = append(parts, xmlText(io.LimitReader(rc, 20<<20)))
					rc.Close()
				}
			}
		}
	case "pdf":
		parts = append(parts, pdfText(io.NewSectionReader(r, 0, size)))
	default:
		b, _ := io.ReadAll(io.LimitReader(io.NewSectionReader(r, 0, size), 1<<20))
		if !utf8.Valid(b) && !utf8.Valid(b[:max(0, len(b)-3)]) {
			return ""
		}
		parts = append(parts, string(b))
	}
	text := strings.Join(parts, "\n")
	if len(text) > 1<<20 {
		text = text[:1<<20]
	}
	return text
}

// xmlText is an XML document's text, with a space between elements.
func xmlText(r io.Reader) string {
	var b strings.Builder
	d := xml.NewDecoder(r)
	for b.Len() < 1<<20 {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local == "p" || t.Name.Local == "si" || t.Name.Local == "tr" {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

var (
	pdfStream = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	pdfString = regexp.MustCompile(`\((?:\\.|[^\\)])*\)\s*(?:Tj|'|")|\[(?:[^\]]*)\]\s*TJ`)
	pdfInner  = regexp.MustCompile(`\((?:\\.|[^\\)])*\)`)
)

// pdfText is a best effort at a PDF's words: the text drawn with simple fonts in its (usually compressed) content
// streams. PDFs that are pictures of pages, or use fonts with their own encodings, give little or nothing.
func pdfText(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, maxIndexed))
	var b strings.Builder
	for _, m := range pdfStream.FindAllSubmatch(raw, -1) {
		data := m[1]
		if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
			if out, err := io.ReadAll(io.LimitReader(zr, 8<<20)); err == nil || len(out) > 0 {
				data = out
			}
		}
		for _, op := range pdfString.FindAll(data, -1) {
			for _, s := range pdfInner.FindAll(op, -1) {
				b.WriteString(pdfUnescape(s[1 : len(s)-1]))
			}
			b.WriteByte(' ')
		}
		if b.Len() > 1<<20 {
			break
		}
	}
	return b.String()
}

func pdfUnescape(s []byte) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			if c >= 32 || c == '\n' {
				out = append(out, c)
			}
			continue
		}
		i++
		switch s[i] {
		case 'n', 'r':
			out = append(out, ' ')
		case '(', ')', '\\':
			out = append(out, s[i])
		}
	}
	if !utf8.Valid(out) { // Latin-1
		r := make([]rune, len(out))
		for i, c := range out {
			r[i] = rune(c)
		}
		return string(r)
	}
	return string(out)
}

// indexUser brings a user's document index up to date, for at most budget (0: no limit). It reports whether it
// got through everything (only then are deleted files dropped from the index).
func (s *Server) indexUser(owner string, budget time.Duration) bool {
	start := time.Now()
	root := "files/" + owner
	seen := map[string]bool{}
	complete := true
	known := map[string][2]int64{}
	rows, err := s.db.Query(`SELECT path, mod, size FROM search_files WHERE owner = ?`, owner)
	if err != nil {
		return false
	}
	for rows.Next() {
		var p string
		var mod, size int64
		if rows.Scan(&p, &mod, &size) == nil {
			known[p] = [2]int64{mod, size}
		}
	}
	rows.Close()
	fs.WalkDir(s.data.FS(), root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if hidden(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !indexable(d.Name()) {
			return nil
		}
		rel := strings.TrimPrefix(p, root+"/")
		seen[rel] = true
		info, err := d.Info()
		if err != nil || info.Size() > maxIndexed {
			return nil
		}
		if k, ok := known[rel]; ok && k[0] == info.ModTime().UnixNano() && k[1] == info.Size() {
			return nil
		}
		if budget > 0 && time.Since(start) > budget {
			complete = false
			return fs.SkipAll
		}
		// OCR is slow: only in the background run, not while someone waits for a search
		var text string
		switch kindOf(d.Name(), false) {
		case "image":
			if budget > 0 || info.Size() > 30<<20 {
				return nil
			}
			text = ocrImage(s.osPath(p))
		case "pdf":
			if text = pdfToolText(s.osPath(p)); !meaningful(text) {
				f, err := s.data.Open(p)
				if err != nil {
					return nil
				}
				text = extractText(d.Name(), f, info.Size())
				f.Close()
			}
			if !meaningful(text) && ocrAvailable() {
				if budget > 0 {
					return nil
				}
				text = ocrPDF(s.osPath(p))
			}
		default:
			f, err := s.data.Open(p)
			if err != nil {
				return nil
			}
			text = extractText(d.Name(), f, info.Size())
			f.Close()
		}
		s.db.Exec(`DELETE FROM search_text WHERE owner = ? AND path = ?`, owner, rel)
		if strings.TrimSpace(text) != "" {
			s.db.Exec(`INSERT INTO search_text (owner, path, body) VALUES (?, ?, ?)`, owner, rel, text)
		}
		s.db.Exec(`INSERT INTO search_files (owner, path, mod, size) VALUES (?, ?, ?, ?)
			ON CONFLICT(owner, path) DO UPDATE SET mod = excluded.mod, size = excluded.size, embedded = 0`, owner, rel, info.ModTime().UnixNano(), info.Size())
		return nil
	})
	if complete {
		for p := range known {
			if !seen[p] {
				s.db.Exec(`DELETE FROM search_text WHERE owner = ? AND path = ?`, owner, p)
				s.db.Exec(`DELETE FROM search_files WHERE owner = ? AND path = ?`, owner, p)
				s.db.Exec(`DELETE FROM embeddings WHERE owner = ? AND path = ?`, owner, p)
			}
		}
	}
	return complete
}

func (s *Server) indexEvery(d time.Duration) {
	first := true
	for {
		if first {
			time.Sleep(time.Minute) // soon after starting, then every d
			first = false
		} else {
			time.Sleep(d)
		}
		rows, err := s.db.Query(`SELECT name FROM users`)
		if err != nil {
			continue
		}
		var users []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				users = append(users, n)
			}
		}
		rows.Close()
		for _, u := range users {
			if !s.indexUser(u, 0) {
				log.Printf("indexing %s's documents didn't finish", u)
			}
			s.embedPending(u, 15*time.Minute)
		}
	}
}

func firstWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if i := strings.LastIndexByte(s[:n], ' '); i > n/2 {
		n = i
	}
	return s[:n] + "…"
}

// ftsQuery turns what someone typed into a full-text query: every word, each as a prefix.
func ftsQuery(q string) string {
	var parts []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return !(r == '\'' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 127)
	}) {
		parts = append(parts, `"`+strings.ReplaceAll(w, `"`, ``)+`"*`)
	}
	return strings.Join(parts, " ")
}

type fileHit struct {
	fileItem
	Path    string `json:"path"`
	Snippet string `json:"snippet,omitempty"` // matched words between \x01 and \x02
}

type eventHit struct {
	ID     string `json:"id"`
	Cal    string `json:"cal"`
	Title  string `json:"title"`
	Start  string `json:"start"`
	AllDay bool   `json:"allDay"`
	Where  string `json:"where"`
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, u user) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	lq := strings.ToLower(q)
	out := map[string]any{"files": []fileHit{}, "events": []eventHit{}, "tasks": []task{}, "contacts": []contact{}}
	if q == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	loc, err := time.LoadLocation(r.URL.Query().Get("tz"))
	if err != nil {
		loc = time.UTC
	}
	// Names
	root := s.rel(u.Name, ".")
	files := []fileHit{}
	byPath := map[string]bool{}
	deadline := time.Now().Add(4 * time.Second)
	fs.WalkDir(s.data.FS(), root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if hidden(d.Name()) || len(files) >= 100 || time.Now().After(deadline) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), lq) {
			if info, err := d.Info(); err == nil {
				rel := strings.TrimPrefix(p, root)
				files = append(files, fileHit{fileItem: item(info), Path: rel})
				byPath[rel] = true
			}
		}
		return nil
	})
	// Words inside documents
	s.indexUser(u.Name, 2*time.Second)
	if fq := ftsQuery(q); fq != "" {
		rows, err := s.db.Query(`SELECT path, snippet(search_text, 2, char(1), char(2), '…', 14) FROM search_text
			WHERE search_text MATCH ? AND owner = ? ORDER BY rank LIMIT 60`, fq, u.Name)
		if err == nil {
			for rows.Next() {
				var p, snip string
				if rows.Scan(&p, &snip) != nil || byPath["/"+p] {
					continue
				}
				if info, err := s.data.Stat(s.rel(u.Name, p)); err == nil {
					files = append(files, fileHit{fileItem: item(info), Path: "/" + p, Snippet: snip})
				}
			}
			rows.Close()
		}
	}
	out["files"] = files
	// by meaning (when the home AI makes embeddings)
	related := []fileHit{}
	if s.aiConfig().Embed != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		hits, _ := s.smartSearch(ctx, u.Name, q, 12)
		cancel()
		for _, h := range hits {
			if h.Score < 0.5 || byPath["/"+h.Path] || len(related) >= 8 {
				continue
			}
			dup := false
			for _, f := range files {
				dup = dup || f.Path == "/"+h.Path
			}
			if info, err := s.data.Stat(s.rel(u.Name, h.Path)); err == nil && !dup {
				related = append(related, fileHit{fileItem: item(info), Path: "/" + h.Path, Snippet: firstWords(h.Text, 200)})
			}
		}
	}
	out["related"] = related
	// Events (each once, at its first time)
	events := []eventHit{}
	cals, _ := s.calendars(u.Name)
	for _, c := range cals {
		if c.Kind != "events" {
			continue
		}
		rows, err := s.db.Query(`SELECT name, ics FROM events WHERE user = ? AND cal = ?`, c.Owner, c.id)
		if err != nil {
			continue
		}
		for rows.Next() {
			var name, ics string
			if rows.Scan(&name, &ics) != nil || !strings.Contains(strings.ToLower(ics), lq) {
				continue
			}
			obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
			if err != nil {
				continue
			}
			for _, ev := range obj.Events() {
				title, _ := ev.Props.Text(ical.PropSummary)
				where, _ := ev.Props.Text(ical.PropLocation)
				notes, _ := ev.Props.Text(ical.PropDescription)
				if !strings.Contains(strings.ToLower(title+"\n"+where+"\n"+notes), lq) || ev.Props.Get(ical.PropRecurrenceID) != nil {
					continue
				}
				if start, _, allDay, err := eventTimes(ev, loc); err == nil {
					events = append(events, eventHit{c.ID + "/" + name, c.ID, title, formatTime(start, allDay), allDay, where})
				}
				break
			}
		}
		rows.Close()
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Start > events[j].Start })
	if len(events) > 30 {
		events = events[:30]
	}
	out["events"] = events
	tasks := []task{}
	for _, t := range s.tasksFor(u.Name, loc, "") {
		if strings.Contains(strings.ToLower(t.Title+"\n"+t.Notes), lq) && len(tasks) < 30 {
			tasks = append(tasks, t)
		}
	}
	out["tasks"] = tasks
	people := []contact{}
	if rows, err := s.db.Query(`SELECT book, name, vcf FROM cards WHERE user = ?`, u.Name); err == nil {
		for rows.Next() {
			var book, name, vcf string
			if rows.Scan(&book, &name, &vcf) != nil || !strings.Contains(strings.ToLower(vcf), lq) {
				continue
			}
			if c, err := decodeCard(vcf); err == nil {
				p := asContact(book+"/"+name, c)
				hay := strings.ToLower(strings.Join([]string{p.Name, p.Org, p.Notes, p.Address, p.Title}, "\n"))
				for _, x := range append(p.Phones, p.Emails...) {
					hay += "\n" + strings.ToLower(x.Value)
				}
				if strings.Contains(hay, lq) {
					p.Photo = ""
					people = append(people, p)
				}
			}
		}
		rows.Close()
	}
	out["contacts"] = people
	writeJSON(w, http.StatusOK, out)
}
