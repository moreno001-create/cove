package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Backups: Homeport backs up Cove's data folder while Cove runs, and a database copied mid-write can be damaged. So
// every night Cove writes a clean copy of its database (VACUUM INTO) to backups/, keeping a week of them: restoring
// one of those is always safe. And each person can download everything of theirs as one zip.

func (s *Server) snapshotEvery(d time.Duration) {
	wait := time.Minute // not in the middle of starting up
	for {
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
		s.bg.Add(1)
		if err := s.snapshot(time.Now()); err != nil {
			log.Printf("database snapshot: %v", err)
		}
		s.bg.Done()
		wait = d
	}
}

// snapshot writes today's copy of the database, if there isn't one yet, and drops copies older than a week.
func (s *Server) snapshot(now time.Time) error {
	if err := s.data.MkdirAll("backups", 0o750); err != nil {
		return err
	}
	name := "backups/cove-" + now.Format("2006-01-02") + ".db"
	if _, err := s.data.Stat(name); err == nil {
		return nil
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, s.cfg.Data+"/"+name); err != nil {
		return err
	}
	entries, _ := fs.ReadDir(s.data.FS(), "backups")
	var old []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cove-") && strings.HasSuffix(e.Name(), ".db") {
			old = append(old, e.Name())
		}
	}
	sort.Strings(old)
	for len(old) > 7 {
		s.data.Remove("backups/" + old[0])
		old = old[1:]
	}
	return nil
}

// exportAll is everything of a user's as one zip: their files (with notes), calendars and task lists as .ics, and
// contacts as .vcf.
func (s *Server) exportAll(w http.ResponseWriter, r *http.Request, u user) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape("Cove "+u.Name+" "+time.Now().Format("2006-01-02")+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	root := "files/" + u.Name
	fsys := s.data.FS()
	fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root || hidden(d.Name()) || !d.Type().IsRegular() && !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = "Files/" + strings.TrimPrefix(p, root+"/")
		if d.IsDir() {
			hdr.Name += "/"
			zw.CreateHeader(hdr)
			return nil
		}
		hdr.Method = zip.Deflate
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := fsys.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(out, f)
		return err
	})
	own, _ := s.ownCalendars(u.Name)
	for _, c := range own {
		rec := &bytesRecorder{header: http.Header{}}
		req, _ := http.NewRequest("GET", "/", nil)
		req.SetPathValue("id", c.ID)
		s.exportCalendar(rec, req, u)
		folder := "Calendars/"
		if c.Kind == "tasks" {
			folder = "Task lists/"
		}
		if f, err := zw.Create(folder + safeName(c.Name) + ".ics"); err == nil {
			f.Write(rec.buf.Bytes())
		}
	}
	rec := &bytesRecorder{header: http.Header{}}
	s.exportContacts(rec, &http.Request{}, u)
	if f, err := zw.Create("Contacts.vcf"); err == nil {
		f.Write(rec.buf.Bytes())
	}
	if f, err := zw.Create("README.txt"); err == nil {
		fmt.Fprintf(f, "Everything %s had in Cove on %s.\r\n\r\nFiles: as they were (notes are in Files/Notes).\r\nCalendars and Task lists: .ics files any calendar app imports.\r\nContacts.vcf: imports into any contacts app.\r\n", u.Name, time.Now().Format("January 2, 2006"))
	}
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, s)
}

// bytesRecorder captures a handler's answer, to put it in the zip.
type bytesRecorder struct {
	header http.Header
	buf    bytes.Buffer
}

func (b *bytesRecorder) Header() http.Header         { return b.header }
func (b *bytesRecorder) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bytesRecorder) WriteHeader(int)             {}
