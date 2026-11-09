package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	gowebdav "github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// Calendars hold events or tasks (a task list, Reminders on an iPhone). A calendar can be shared with others in the
// household, to view or to change: it then shows in their Cove and on their phones beside their own. Their app
// names it "<owner>~<id>"; the owner's own is just "<id>".

const calendarsSchema = `
CREATE TABLE IF NOT EXISTS calendar_shares (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	cal TEXT NOT NULL,
	target TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	edit INTEGER NOT NULL,
	created INTEGER NOT NULL,
	UNIQUE (owner, cal, target),
	FOREIGN KEY (owner, cal) REFERENCES calendars(user, id) ON DELETE CASCADE
);`

// migrateCalendars adds what older databases lack.
func migrateCalendars(db *sql.DB) {
	db.Exec(`ALTER TABLE calendars ADD COLUMN kind TEXT NOT NULL DEFAULT 'events'`) // fails harmlessly once it's there
}

type calendarInfo struct {
	ID     string `json:"id"` // what the user's app calls it: "<id>", or "<owner>~<id>" when shared with them
	Name   string `json:"name"`
	Color  string `json:"color"`
	Kind   string `json:"kind"` // events or tasks
	Owner  string `json:"owner"`
	Who    string `json:"who"`    // the owner's name to show, for shared ones
	Shared bool   `json:"shared"` // someone else's, shared with this user
	Edit   bool   `json:"edit"`
	// Virtual calendars are made up in the app: birthdays from contacts, and invitations.
	Virtual      bool          `json:"virtual,omitempty"`
	Subscription *subscription `json:"subscription,omitempty"` // a calendar fetched from another site
	id           string        // in the owner's calendars
}

var calendarColors = []string{"#3b82f6", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6", "#ec4899", "#14b8a6", "#64748b"}

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// calendars lists a user's calendars and task lists, then those shared with them. A user with none gets "Personal"
// and "Tasks".
func (s *Server) calendars(user string) ([]calendarInfo, error) {
	list, err := s.ownCalendars(user)
	if err != nil {
		return nil, err
	}
	for _, def := range []calendarInfo{{id: "personal", Name: "Personal", Kind: "events"}, {id: "tasks", Name: "Tasks", Kind: "tasks"}} {
		if !slices.ContainsFunc(list, func(c calendarInfo) bool { return c.Kind == def.Kind }) {
			color := calendarColors[0]
			if def.Kind == "tasks" {
				color = calendarColors[1]
			}
			s.db.Exec(`INSERT OR IGNORE INTO calendars (user, id, name, color, kind) VALUES (?, ?, ?, ?, ?)`, user, def.id, def.Name, color, def.Kind)
		}
	}
	if list, err = s.ownCalendars(user); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT c.user, c.id, c.name, c.color, c.kind, coalesce(nullif(o.display, ''), o.name), s.edit
		FROM calendar_shares s JOIN calendars c ON c.user = s.owner AND c.id = s.cal JOIN users o ON o.name = s.owner
		WHERE s.target = ? ORDER BY s.created`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c := calendarInfo{Shared: true}
		if err := rows.Scan(&c.Owner, &c.id, &c.Name, &c.Color, &c.Kind, &c.Who, &c.Edit); err != nil {
			return nil, err
		}
		c.ID = c.Owner + "~" + c.id
		list = append(list, c)
	}
	return list, rows.Err()
}

func (s *Server) ownCalendars(user string) ([]calendarInfo, error) {
	rows, err := s.db.Query(`SELECT id, name, color, kind FROM calendars WHERE user = ? ORDER BY rowid`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []calendarInfo
	for rows.Next() {
		c := calendarInfo{Owner: user, Edit: true}
		if err := rows.Scan(&c.id, &c.Name, &c.Color, &c.Kind); err != nil {
			return nil, err
		}
		c.ID = c.id
		list = append(list, c)
	}
	return list, rows.Err()
}

// calendar finds one of a user's calendars, their own or shared with them, by what their app calls it.
func (s *Server) calendar(user, ref string) (calendarInfo, error) {
	list, err := s.calendars(user)
	if err != nil {
		return calendarInfo{}, err
	}
	for _, c := range list {
		if c.ID == ref {
			return c, nil
		}
	}
	return calendarInfo{}, errors.New("there's no such calendar")
}

// writable is calendar for changing events in it.
func (s *Server) writable(user, ref string) (calendarInfo, error) {
	c, err := s.calendar(user, ref)
	if err == nil && !c.Edit {
		err = errors.New("that calendar is shared with you to view, not to change")
	}
	return c, err
}

func (s *Server) listCalendars(w http.ResponseWriter, r *http.Request, u user) {
	list, err := s.calendars(u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if k := r.URL.Query().Get("kind"); k != "" {
		list = slices.DeleteFunc(list, func(c calendarInfo) bool { return c.Kind != k })
	}
	if k := r.URL.Query().Get("kind"); k == "" || k == "events" {
		var n int
		s.db.QueryRow(`SELECT count(*) FROM invites WHERE target = ? AND status != 'DECLINED'`, u.Name).Scan(&n)
		if n > 0 {
			list = append(list, calendarInfo{ID: "~invites", Name: "Invitations", Color: "#8b5cf6", Kind: "events", Owner: u.Name, Virtual: true})
		}
		list = append(list, calendarInfo{ID: "~birthdays", Name: "Birthdays", Color: "#ec4899", Kind: "events", Owner: u.Name, Virtual: true})
		for _, sub := range s.subscriptionsOf(u.Name) {
			list = append(list, calendarInfo{ID: fmt.Sprintf("~sub:%d", sub.ID), Name: sub.Name, Color: sub.Color, Kind: "events", Owner: u.Name,
				Virtual: true, Subscription: &sub})
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createCalendar(w http.ResponseWriter, r *http.Request, u user) {
	var in calendarInfo
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		jsonError(w, http.StatusBadRequest, "Give it a name (up to 80 characters).")
		return
	}
	if in.Kind != "tasks" {
		in.Kind = "events"
	}
	if !colorPattern.MatchString(in.Color) {
		in.Color = calendarColors[len(in.Name)%len(calendarColors)]
	}
	in.id = strings.NewReplacer("_", "x", "-", "y").Replace(strings.ToLower(randomToken(8)))
	if _, err := s.db.Exec(`INSERT INTO calendars (user, id, name, color, kind) VALUES (?, ?, ?, ?, ?)`, u.Name, in.id, in.Name, in.Color, in.Kind); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in.ID, in.Owner, in.Edit = in.id, u.Name, true
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) updateCalendar(w http.ResponseWriter, r *http.Request, u user) {
	var in calendarInfo
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 || !colorPattern.MatchString(in.Color) {
		jsonError(w, http.StatusBadRequest, "It needs a name (up to 80 characters) and a color like #3b82f6.")
		return
	}
	res, err := s.db.Exec(`UPDATE calendars SET name = ?, color = ? WHERE user = ? AND id = ?`, in.Name, in.Color, u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "Only its owner can change that.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deleteCalendar deletes the user's own calendar (with its events), or takes one shared with them off their list.
func (s *Server) deleteCalendar(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.calendar(u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if c.Virtual {
		err = errors.New("that one comes from your contacts or invitations")
	} else if c.Shared {
		_, err = s.db.Exec(`DELETE FROM calendar_shares WHERE owner = ? AND cal = ? AND target = ?`, c.Owner, c.id, u.Name)
	} else {
		_, err = s.db.Exec(`DELETE FROM calendars WHERE user = ? AND id = ?`, u.Name, c.id)
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- sharing a calendar or task list

func (s *Server) listCalendarShares(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT s.id, s.target, coalesce(nullif(t.display, ''), t.name), s.edit FROM calendar_shares s
		JOIN users t ON t.name = s.target WHERE s.owner = ? AND s.cal = ? ORDER BY s.created`, u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type entry struct {
		ID   int64  `json:"id"`
		User string `json:"user"`
		Who  string `json:"who"`
		Edit bool   `json:"edit"`
	}
	list := []entry{}
	for rows.Next() {
		var e entry
		if rows.Scan(&e.ID, &e.User, &e.Who, &e.Edit) == nil {
			list = append(list, e)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// shareCalendar shares the user's calendar with one person, or with everyone who uses Cove ("*").
func (s *Server) shareCalendar(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		User string `json:"user"`
		Edit bool   `json:"edit"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	var kind string
	if s.db.QueryRow(`SELECT kind FROM calendars WHERE user = ? AND id = ?`, u.Name, id).Scan(&kind) != nil {
		jsonError(w, http.StatusNotFound, "Only its owner can share it.")
		return
	}
	targets := []string{in.User}
	if in.User == "*" {
		targets = nil
		rows, err := s.db.Query(`SELECT name FROM users WHERE name != ?`, u.Name)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				targets = append(targets, n)
			}
		}
		rows.Close()
	}
	var calName string
	s.db.QueryRow(`SELECT name FROM calendars WHERE user = ? AND id = ?`, u.Name, id).Scan(&calName)
	for _, t := range targets {
		if t == u.Name {
			continue
		}
		var had int
		s.db.QueryRow(`SELECT count(*) FROM calendar_shares WHERE owner = ? AND cal = ? AND target = ?`, u.Name, id, t).Scan(&had)
		if _, err := s.db.Exec(`INSERT INTO calendar_shares (owner, cal, target, edit, created) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(owner, cal, target) DO UPDATE SET edit = excluded.edit`, u.Name, id, t, in.Edit, time.Now().Unix()); err != nil {
			jsonError(w, http.StatusBadRequest, "There's no such person in Cove.")
			return
		}
		if had == 0 {
			what, link := "calendar", "#/calendar"
			if kind == "tasks" {
				what, link = "list", "#/tasks"
			}
			s.notify(t, fmt.Sprintf("%s shared the %s “%s” with you", displayName(u), what, calName), link)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]int{"shared": len(targets)})
}

func (s *Server) unshareCalendar(w http.ResponseWriter, r *http.Request, u user) {
	res, err := s.db.Exec(`DELETE FROM calendar_shares WHERE id = ? AND owner = ?`, r.PathValue("id"), u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, http.StatusNotFound, "That isn't shared any more.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- CalDAV: /dav/cal/<user>/ is the user, /dav/cal/<user>/calendars/<id>/ a calendar or task list (their own, or
// <owner>~<id> shared with them), and <name>.ics in it an event or task.

type calBackend struct {
	s *Server
	u user
}

var davName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@%+=~-]{0,200}$`)

func (b *calBackend) home() string { return "/dav/cal/" + b.u.Name + "/calendars/" }

func (b *calBackend) CurrentUserPrincipal(context.Context) (string, error) {
	return "/dav/cal/" + b.u.Name + "/", nil
}

func (b *calBackend) CalendarHomeSetPath(context.Context) (string, error) { return b.home(), nil }

// split turns a path into the calendar (as the user's app names it) and the event's name (if any).
func (b *calBackend) split(p string) (ref, name string, err error) {
	rest, ok := strings.CutPrefix(p, b.home())
	if !ok {
		return "", "", gowebdav.NewHTTPError(http.StatusForbidden, errors.New("that's another user's"))
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) > 2 || !davName.MatchString(parts[0]) || (len(parts) == 2 && !davName.MatchString(parts[1])) {
		return "", "", gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such calendar"))
	}
	if len(parts) == 2 {
		name = parts[1]
	}
	return parts[0], name, nil
}

func (b *calBackend) lookup(p string, write bool) (calendarInfo, string, error) {
	ref, name, err := b.split(p)
	if err != nil {
		return calendarInfo{}, "", err
	}
	c, err := b.s.calendar(b.u.Name, ref)
	if err != nil {
		return c, "", gowebdav.NewHTTPError(http.StatusNotFound, err)
	}
	if write && !c.Edit {
		return c, "", gowebdav.NewHTTPError(http.StatusForbidden, errors.New("shared to view, not to change"))
	}
	return c, name, nil
}

func (b *calBackend) asCalendar(c calendarInfo) caldav.Calendar {
	comps := []string{ical.CompEvent}
	name := c.Name
	if c.Kind == "tasks" {
		comps = []string{ical.CompToDo}
	}
	if c.Shared {
		name += " (" + c.Who + ")"
	}
	return caldav.Calendar{Path: b.home() + c.ID + "/", Name: name, MaxResourceSize: 4 << 20, SupportedComponentSet: comps}
}

func (b *calBackend) CreateCalendar(ctx context.Context, c *caldav.Calendar) error {
	id, _, err := b.split(c.Path)
	if err != nil {
		return err
	}
	if strings.Contains(id, "~") {
		return gowebdav.NewHTTPError(http.StatusForbidden, errors.New("calendar names can't have ~"))
	}
	name := c.Name
	if name == "" {
		name = id
	}
	kind := "events"
	if slices.Contains(c.SupportedComponentSet, ical.CompToDo) && !slices.Contains(c.SupportedComponentSet, ical.CompEvent) {
		kind = "tasks" // a Reminders list
	}
	_, err = b.s.db.Exec(`INSERT INTO calendars (user, id, name, color, kind) VALUES (?, ?, ?, ?, ?)`, b.u.Name, id, name, calendarColors[1], kind)
	return err
}

func (b *calBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	list, err := b.s.calendars(b.u.Name)
	if err != nil {
		return nil, err
	}
	out := make([]caldav.Calendar, 0, len(list))
	for _, c := range list {
		out = append(out, b.asCalendar(c))
	}
	return out, nil
}

func (b *calBackend) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	c, _, err := b.lookup(p, false)
	if err != nil {
		return nil, err
	}
	cal := b.asCalendar(c)
	return &cal, nil
}

func (b *calBackend) object(c calendarInfo, name, ics, etag string, mod int64) (*caldav.CalendarObject, error) {
	data, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		return nil, err
	}
	return &caldav.CalendarObject{Path: b.home() + c.ID + "/" + name, ModTime: time.Unix(mod, 0), ContentLength: int64(len(ics)),
		ETag: etag, Data: data}, nil
}

func (b *calBackend) GetCalendarObject(ctx context.Context, p string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	c, name, err := b.lookup(p, false)
	if err != nil || name == "" {
		return nil, gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such event"))
	}
	var ics, etag string
	var mod int64
	err = b.s.db.QueryRow(`SELECT ics, etag, modified FROM events WHERE user = ? AND cal = ? AND name = ?`, c.Owner, c.id, name).
		Scan(&ics, &etag, &mod)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gowebdav.NewHTTPError(http.StatusNotFound, err)
	} else if err != nil {
		return nil, err
	}
	return b.object(c, name, ics, etag, mod)
}

func (b *calBackend) ListCalendarObjects(ctx context.Context, p string, _ *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	c, _, err := b.lookup(p, false)
	if err != nil {
		return nil, err
	}
	rows, err := b.s.db.Query(`SELECT name, ics, etag, modified FROM events WHERE user = ? AND cal = ?`, c.Owner, c.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []caldav.CalendarObject
	for rows.Next() {
		var name, ics, etag string
		var mod int64
		if err := rows.Scan(&name, &ics, &etag, &mod); err != nil {
			return nil, err
		}
		if o, err := b.object(c, name, ics, etag, mod); err == nil {
			out = append(out, *o)
		}
	}
	return out, rows.Err()
}

func (b *calBackend) QueryCalendarObjects(ctx context.Context, p string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	all, err := b.ListCalendarObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	return caldav.Filter(query, all)
}

func (b *calBackend) PutCalendarObject(ctx context.Context, p string, data *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	c, name, err := b.lookup(p, true)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, gowebdav.NewHTTPError(http.StatusForbidden, errors.New("events go inside a calendar"))
	}
	var current string
	err = b.s.db.QueryRow(`SELECT etag FROM events WHERE user = ? AND cal = ? AND name = ?`, c.Owner, c.id, name).Scan(&current)
	exists := err == nil
	if opts != nil {
		if opts.IfNoneMatch.IsSet() && exists {
			if opts.IfNoneMatch.IsWildcard() {
				return nil, gowebdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("it's already there"))
			}
			if m, _ := opts.IfNoneMatch.MatchETag(current); m {
				return nil, gowebdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("it hasn't changed"))
			}
		}
		if opts.IfMatch.IsSet() {
			if !exists {
				return nil, gowebdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("it isn't there"))
			}
			if m, _ := opts.IfMatch.MatchETag(current); !m && !opts.IfMatch.IsWildcard() {
				return nil, gowebdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("it changed meanwhile"))
			}
		}
	}
	if _, err := b.s.saveObject(c.Owner, c.id, name, data); err != nil {
		return nil, gowebdav.NewHTTPError(http.StatusBadRequest, err)
	}
	for _, ch := range data.Children {
		if ch.Name == ical.CompEvent || ch.Name == ical.CompToDo {
			title, _ := ch.Props.Text(ical.PropSummary)
			verb := "added “"
			if exists {
				verb = "changed “"
			}
			b.s.calendarActivity(b.u, c, verb+title+"” in")
			break
		}
	}
	return b.GetCalendarObject(ctx, b.home()+c.ID+"/"+name, nil)
}

func (b *calBackend) DeleteCalendarObject(ctx context.Context, p string) error {
	c, name, err := b.lookup(p, true)
	if err != nil {
		return err
	}
	if name == "" || b.s.trashObject(b.u, c, name, "event") != nil {
		return gowebdav.NewHTTPError(http.StatusNotFound, errors.New("no such event"))
	}
	return nil
}
