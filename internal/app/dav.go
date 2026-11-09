package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
	xwebdav "golang.org/x/net/webdav"
)

// dav serves sync apps, which sign in with the user name and an app password:
//
//	/dav/files/  the user's files over WebDAV (Finder, Windows, rclone, phone file apps)
//	/dav/cal/    their calendars and task lists over CalDAV (iPhone Calendar and Reminders, Mac, Thunderbird, DAVx⁵)
//	/dav/card/   their contacts over CardDAV
func (s *Server) dav() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.davUser(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="Cove", charset="UTF-8"`)
			http.Error(w, "Sign in with your user name and an app password (Cove → Settings → Sync apps).", http.StatusUnauthorized)
			return
		}
		switch p := r.URL.Path; {
		case p == "/dav/files" || strings.HasPrefix(p, "/dav/files/"):
			h := &xwebdav.Handler{Prefix: "/dav/files", FileSystem: userFS{s, u.Name}, LockSystem: s.lockSystem(u.Name)}
			h.ServeHTTP(w, r)
		case p == "/dav/cal" || strings.HasPrefix(p, "/dav/cal/"):
			(&caldav.Handler{Backend: &calBackend{s: s, u: u}, Prefix: "/dav/cal"}).ServeHTTP(w, r)
		case p == "/dav/card" || strings.HasPrefix(p, "/dav/card/"):
			(&carddav.Handler{Backend: &cardBackend{s: s, u: u}, Prefix: "/dav/card"}).ServeHTTP(w, r)
		default:
			http.Error(w, "Files are at /dav/files/, calendars and tasks at /dav/cal/, contacts at /dav/card/.", http.StatusNotFound)
		}
	})
}

func (s *Server) lockSystem(user string) xwebdav.LockSystem {
	s.mu.Lock()
	defer s.mu.Unlock()
	ls, ok := s.locks[user]
	if !ok {
		ls = xwebdav.NewMemLS()
		s.locks[user] = ls
	}
	return ls
}

// userFS is a user's folder for WebDAV. Deleting moves to the trash, as in the app.
type userFS struct {
	s    *Server
	user string
}

func (f userFS) Mkdir(_ context.Context, name string, _ os.FileMode) error {
	return f.s.data.Mkdir(f.s.rel(f.user, name), 0o755)
}

func (f userFS) OpenFile(_ context.Context, name string, flag int, _ os.FileMode) (xwebdav.File, error) {
	return f.s.data.OpenFile(f.s.rel(f.user, name), flag, 0o644)
}

func (f userFS) RemoveAll(_ context.Context, name string) error {
	return f.s.trash(f.user, cleanRel(name))
}

func (f userFS) Rename(_ context.Context, oldName, newName string) error {
	return f.s.move(f.user, cleanRel(oldName), cleanRel(newName))
}

func (f userFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	return f.s.data.Stat(f.s.rel(f.user, name))
}

// saveObject stores a calendar object, as CalDAV and the app both write them, and returns its ETag.
func (s *Server) saveObject(user, cal, name string, data *ical.Calendar) (string, error) {
	uid := ""
	n := 0
	for _, c := range data.Children {
		if c.Name != ical.CompEvent && c.Name != ical.CompToDo {
			continue
		}
		n++
		id, _ := c.Props.Text(ical.PropUID)
		if uid == "" {
			uid = id
		} else if id != uid {
			return "", errors.New("one object holds one event (with its changed occurrences): the UIDs differ")
		}
	}
	if n == 0 || uid == "" {
		return "", errors.New("there's no event (with a UID) in it")
	}
	if data.Props.Get(ical.PropProductID) == nil {
		data.Props.SetText(ical.PropProductID, "-//Cove//EN")
	}
	if data.Props.Get(ical.PropVersion) == nil {
		data.Props.SetText(ical.PropVersion, "2.0")
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(data); err != nil {
		return "", fmt.Errorf("it can't be written as iCalendar: %w", err)
	}
	ics := buf.String()
	if len(ics) > 4<<20 {
		return "", errors.New("it's too big (over 4 MB)")
	}
	etag := hashHex(ics)[:20]
	_, err := s.db.Exec(`INSERT INTO events (user, cal, name, ics, etag, modified) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user, cal, name) DO UPDATE SET ics = excluded.ics, etag = excluded.etag, modified = excluded.modified`,
		user, cal, name, ics, etag, time.Now().Unix())
	watchers := s.calendarWatchers(user, cal)
	s.changed("calendar", watchers...)
	s.changed("tasks", watchers...)
	return etag, err
}
