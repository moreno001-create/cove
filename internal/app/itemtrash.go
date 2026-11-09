package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
)

// Deleted events, tasks and contacts go to their owner's trash for 30 days, like files, whether they were deleted in
// Cove or on a phone.

const itemTrashSchema = `
CREATE TABLE IF NOT EXISTS trash_items (
	id TEXT PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	kind TEXT NOT NULL, -- event, task or contact
	title TEXT NOT NULL,
	cal TEXT NOT NULL, -- the calendar, list or address book it was in
	name TEXT NOT NULL,
	data TEXT NOT NULL,
	deleted INTEGER NOT NULL
);`

// trashObject deletes an event or task (kind) into the calendar owner's trash.
func (s *Server) trashObject(actor user, c calendarInfo, name, kind string) error {
	_, err := s.trashObjectID(actor, c, name, kind)
	return err
}

func (s *Server) trashObjectID(actor user, c calendarInfo, name, kind string) (string, error) {
	var ics string
	if err := s.db.QueryRow(`SELECT ics FROM events WHERE user = ? AND cal = ? AND name = ?`, c.Owner, c.id, name).Scan(&ics); err != nil {
		return "", err
	}
	title := ""
	if obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode(); err == nil {
		for _, ch := range obj.Children {
			if ch.Name == ical.CompEvent || ch.Name == ical.CompToDo {
				title, _ = ch.Props.Text(ical.PropSummary)
				if ch.Name == ical.CompToDo {
					kind = "task"
				}
				break
			}
		}
	}
	tid := randomToken(9)
	if _, err := s.db.Exec(`INSERT INTO trash_items (id, user, kind, title, cal, name, data, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tid, c.Owner, kind, title, c.id, name, ics, time.Now().Unix()); err != nil {
		return "", err
	}
	s.db.Exec(`DELETE FROM events WHERE user = ? AND cal = ? AND name = ?`, c.Owner, c.id, name)
	s.db.Exec(`DELETE FROM invites WHERE owner = ? AND cal = ? AND name = ?`, c.Owner, c.id, name)
	if title == "" {
		title = "(no title)"
	}
	s.calendarActivity(actor, c, "deleted “"+title+"” from")
	s.logActivity(c.Owner, actor, "deleted “"+title+"” from "+c.Name, undoStep{Kind: "restore-item", TrashID: tid})
	return tid, nil
}

func (s *Server) trashCard(user, book, name string) error {
	_, err := s.trashCardID(user, book, name)
	return err
}

func (s *Server) trashCardID(user, book, name string) (string, error) {
	var vcf string
	if err := s.db.QueryRow(`SELECT vcf FROM cards WHERE user = ? AND book = ? AND name = ?`, user, book, name).Scan(&vcf); err != nil {
		return "", err
	}
	title := name
	if c, err := decodeCard(vcf); err == nil {
		title = c.Value(vcard.FieldFormattedName)
	}
	tid := randomToken(9)
	if _, err := s.db.Exec(`INSERT INTO trash_items (id, user, kind, title, cal, name, data, deleted) VALUES (?, ?, 'contact', ?, ?, ?, ?, ?)`,
		tid, user, title, book, name, vcf, time.Now().Unix()); err != nil {
		return "", err
	}
	_, err := s.db.Exec(`DELETE FROM cards WHERE user = ? AND book = ? AND name = ?`, user, book, name)
	s.logActivity(user, user2(user), "deleted the contact “"+title+"”", undoStep{Kind: "restore-item", TrashID: tid})
	s.changed("contacts", user)
	return tid, err
}

func user2(name string) user { return user{Name: name} }

type trashedItem struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Where   string `json:"where"`
	Deleted int64  `json:"deleted"`
}

func (s *Server) listTrashItems(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT t.id, t.kind, t.title, coalesce(c.name, b.name, ''), t.deleted FROM trash_items t
		LEFT JOIN calendars c ON t.kind != 'contact' AND c.user = t.user AND c.id = t.cal
		LEFT JOIN addressbooks b ON t.kind = 'contact' AND b.user = t.user AND b.id = t.cal
		WHERE t.user = ? ORDER BY t.deleted DESC, t.rowid DESC`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	list := []trashedItem{}
	for rows.Next() {
		var t trashedItem
		if rows.Scan(&t.ID, &t.Kind, &t.Title, &t.Where, &t.Deleted) == nil {
			list = append(list, t)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) restoreTrashItem(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		ID string `json:"id"`
	}
	readJSON(r, &in)
	var kind, cal, name, data string
	if err := s.db.QueryRow(`SELECT kind, cal, name, data FROM trash_items WHERE id = ? AND user = ?`, in.ID, u.Name).Scan(&kind, &cal, &name, &data); err != nil {
		jsonError(w, http.StatusNotFound, "That isn't in the trash any more.")
		return
	}
	var err error
	if kind == "contact" {
		err = s.restoreCard(u.Name, cal, name, data)
	} else {
		err = s.restoreObject(u.Name, kind, cal, name, data)
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.db.Exec(`DELETE FROM trash_items WHERE id = ?`, in.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// restoreObject puts an event or task back in its calendar (or the first one of its kind, if that's gone), under a
// new name if its old one was taken meanwhile.
func (s *Server) restoreObject(user, kind, cal, name, ics string) error {
	want := "events"
	if kind == "task" {
		want = "tasks"
	}
	var k string
	if s.db.QueryRow(`SELECT kind FROM calendars WHERE user = ? AND id = ?`, user, cal).Scan(&k) != nil || k != want {
		list, _ := s.ownCalendars(user)
		cal = ""
		for _, c := range list {
			if c.Kind == want {
				cal = c.id
				break
			}
		}
		if cal == "" {
			return errors.New("there's no calendar to put it back in")
		}
	}
	var x int
	if s.db.QueryRow(`SELECT 1 FROM events WHERE user = ? AND cal = ? AND name = ?`, user, cal, name).Scan(&x) == nil {
		name = strings.ToLower(randomToken(12)) + ".ics"
	}
	obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		return err
	}
	_, err = s.saveObject(user, cal, name, obj)
	return err
}

func (s *Server) restoreCard(user, book, name, vcf string) error {
	books, err := s.addressBooks(user)
	if err != nil || len(books) == 0 {
		return errors.New("there's no address book to put it back in")
	}
	var x int
	if s.db.QueryRow(`SELECT 1 FROM addressbooks WHERE user = ? AND id = ?`, user, book).Scan(&x) != nil {
		book = books[0].ID
	}
	if s.db.QueryRow(`SELECT 1 FROM cards WHERE user = ? AND book = ? AND name = ?`, user, book, name).Scan(&x) == nil {
		name = strings.ToLower(randomToken(12)) + ".vcf"
	}
	c, err := decodeCard(vcf)
	if err != nil {
		return err
	}
	_, err = s.saveCard(user, book, name, c)
	return err
}

func (s *Server) deleteTrashItems(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	readJSON(r, &in)
	var err error
	if in.All {
		_, err = s.db.Exec(`DELETE FROM trash_items WHERE user = ?`, u.Name)
	} else {
		for _, id := range in.IDs {
			if _, err = s.db.Exec(`DELETE FROM trash_items WHERE id = ? AND user = ?`, id, u.Name); err != nil {
				break
			}
		}
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- birthdays from contacts, as a calendar of its own (only in Cove: phones make theirs from the same contacts)

var birthdayPattern = regexp.MustCompile(`^(\d{4}|--)-?(\d{2})-?(\d{2})`)

func (s *Server) birthdays(out []occurrence, u user, loc *time.Location, from, to time.Time) []occurrence {
	rows, err := s.db.Query(`SELECT name, vcf FROM cards WHERE user = ?`, u.Name)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, vcf string
		if rows.Scan(&name, &vcf) != nil {
			continue
		}
		c, err := decodeCard(vcf)
		if err != nil {
			continue
		}
		m := birthdayPattern.FindStringSubmatch(c.Value(vcard.FieldBirthday))
		if m == nil {
			continue
		}
		year := 0
		if m[1] != "--" {
			year, _ = strconv.Atoi(m[1])
			if year == 1604 { // Apple's "no year"
				year = 0
			}
		}
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		who := c.Value(vcard.FieldFormattedName)
		for y := from.Year(); y <= to.Year(); y++ {
			d := time.Date(y, time.Month(month), day, 0, 0, 0, 0, loc)
			if d.Month() != time.Month(month) || d.Before(from.AddDate(0, 0, -1)) || !d.Before(to) || (year > 0 && y < year) {
				continue
			}
			title := who + "'s birthday"
			if year > 0 {
				title += fmt.Sprintf(" (%d)", y-year)
			}
			out = append(out, occurrence{ID: "~birthdays/" + name, Cal: "~birthdays", Title: title, AllDay: true, ReadOnly: true,
				Start: d.Format("2006-01-02"), End: d.AddDate(0, 0, 1).Format("2006-01-02"), Repeat: "YEARLY"})
		}
	}
	return out
}

var _ = sql.ErrNoRows
