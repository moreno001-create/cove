package app

import (
	"bufio"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// Notes are Markdown files in the "Notes" folder (plain files, so they sync and back up like the rest), with
// checklists and pins.

const notesFolder = "Notes"

const notesSchema = `
CREATE TABLE IF NOT EXISTS pins (
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	path TEXT NOT NULL,
	PRIMARY KEY (user, path)
);`

type note struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
	Mod     int64  `json:"mod"`
	Pinned  bool   `json:"pinned"`
	Done    int    `json:"done"`  // checklist items ticked
	Total   int    `json:"total"` // checklist items
}

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request, u user) {
	s.data.MkdirAll(s.rel(u.Name, notesFolder), 0o755)
	pinned := map[string]bool{}
	if rows, err := s.db.Query(`SELECT path FROM pins WHERE user = ?`, u.Name); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				pinned[p] = true
			}
		}
		rows.Close()
	}
	list := []note{}
	root := s.rel(u.Name, notesFolder)
	fs.WalkDir(s.data.FS(), root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || hidden(d.Name()) {
			return nil
		}
		ext := strings.ToLower(path.Ext(d.Name()))
		if ext != ".md" && ext != ".txt" && ext != ".markdown" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel := notesFolder + strings.TrimPrefix(p, root)
		n := note{Path: "/" + rel, Mod: info.ModTime().UnixMilli(), Pinned: pinned[rel], Title: strings.TrimSuffix(d.Name(), ext)}
		if f, err := s.data.Open(p); err == nil {
			sc := bufio.NewScanner(f)
			var body []string
			for sc.Scan() && len(body) < 200 {
				line := strings.TrimSpace(sc.Text())
				switch {
				case strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "* [ ]"):
					n.Total++
				case strings.HasPrefix(strings.ToLower(line), "- [x]") || strings.HasPrefix(strings.ToLower(line), "* [x]"):
					n.Total++
					n.Done++
				}
				if line != "" && !strings.HasPrefix(line, "#") && len(strings.Join(body, " ")) < 160 {
					body = append(body, strings.TrimLeft(line, "-*> []xX"))
				}
			}
			f.Close()
			n.Excerpt = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "", "~~", "").Replace(strings.Join(body, " ")))
			if len(n.Excerpt) > 160 {
				n.Excerpt = n.Excerpt[:160] + "…"
			}
		}
		list = append(list, n)
		return nil
	})
	sort.Slice(list, func(i, j int) bool {
		if list[i].Pinned != list[j].Pinned {
			return list[i].Pinned
		}
		return list[i].Mod > list[j].Mod
	})
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createNote(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Title    string `json:"title"`
		Template string `json:"template"`
	}
	readJSON(r, &in)
	title := strings.TrimSpace(strings.NewReplacer("/", "-", "\\", "-").Replace(in.Title))
	if title == "" || title == "." || title == ".." {
		title = "Note " + time.Now().Format("2006-01-02 15.04")
	}
	final, err := s.writeFile(u.Name, notesFolder+"/"+title+".md", strings.NewReader(noteTemplate(in.Template, title)), false, time.Time{})
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": "/" + final})
}

func (s *Server) pinNote(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Path   string `json:"path"`
		Pinned bool   `json:"pinned"`
	}
	readJSON(r, &in)
	p := cleanRel(in.Path)
	if in.Pinned {
		s.db.Exec(`INSERT OR IGNORE INTO pins (user, path) VALUES (?, ?)`, u.Name, p)
	} else {
		s.db.Exec(`DELETE FROM pins WHERE user = ? AND path = ?`, u.Name, p)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
