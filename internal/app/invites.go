package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// Inviting people in the household to an event: it shows on their calendar (marked as an invitation) until they
// decline, and the organizer sees who's coming. In the iCalendar data they're ATTENDEEs (with X-COVE-USER naming the
// Cove user), so phones show the guest list too.

const invitesSchema = `
CREATE TABLE IF NOT EXISTS invites (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	cal TEXT NOT NULL,
	name TEXT NOT NULL,
	target TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	status TEXT NOT NULL,
	created INTEGER NOT NULL,
	UNIQUE (owner, cal, name, target)
);`

type invite struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"` // NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED
	Organizer string `json:"organizer"`
}

type guest struct {
	User   string `json:"user"`
	Who    string `json:"who"`
	Status string `json:"status"`
}

// setInvites makes the event's invitations what want says (nil: leave them), and writes them into it as ATTENDEEs.
func (s *Server) setInvites(actor user, c calendarInfo, name string, ev *ical.Event, want *[]string) {
	if want == nil {
		return
	}
	title, _ := ev.Props.Text(ical.PropSummary)
	keep := map[string]bool{}
	for _, t := range *want {
		if t == "" || t == c.Owner || keep[t] {
			continue
		}
		var ok int
		if s.db.QueryRow(`SELECT 1 FROM users WHERE name = ?`, t).Scan(&ok) != nil {
			continue
		}
		keep[t] = true
		res, err := s.db.Exec(`INSERT OR IGNORE INTO invites (owner, cal, name, target, status, created) VALUES (?, ?, ?, ?, 'NEEDS-ACTION', ?)`,
			c.Owner, c.id, name, t, time.Now().Unix())
		if n, _ := res.RowsAffected(); err == nil && n == 1 {
			s.notify(t, fmt.Sprintf("%s invited you to “%s”", displayName(actor), title), "#/calendar")
		}
	}
	rows, err := s.db.Query(`SELECT target FROM invites WHERE owner = ? AND cal = ? AND name = ?`, c.Owner, c.id, name)
	if err == nil {
		var gone []string
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil && !keep[t] {
				gone = append(gone, t)
			}
		}
		rows.Close()
		for _, t := range gone {
			s.db.Exec(`DELETE FROM invites WHERE owner = ? AND cal = ? AND name = ? AND target = ?`, c.Owner, c.id, name, t)
		}
	}
	s.writeAttendees(c.Owner, c.id, name, ev)
}

func (s *Server) guests(owner, cal, name string) []guest {
	rows, err := s.db.Query(`SELECT i.target, coalesce(nullif(u.display, ''), u.name), i.status FROM invites i JOIN users u ON u.name = i.target
		WHERE i.owner = ? AND i.cal = ? AND i.name = ? ORDER BY i.created`, owner, cal, name)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []guest
	for rows.Next() {
		var g guest
		if rows.Scan(&g.User, &g.Who, &g.Status) == nil {
			out = append(out, g)
		}
	}
	return out
}

func (s *Server) writeAttendees(owner, cal, name string, ev *ical.Event) {
	ev.Props.Del(ical.PropAttendee)
	ev.Props.Del(ical.PropOrganizer)
	gs := s.guests(owner, cal, name)
	if len(gs) == 0 {
		return
	}
	address := func(u string) string {
		var email string
		s.db.QueryRow(`SELECT email FROM users WHERE name = ?`, u).Scan(&email)
		if email == "" {
			email = u + "@cove.invalid"
		}
		return "mailto:" + email
	}
	org := ical.NewProp(ical.PropOrganizer)
	org.Value = address(owner)
	org.Params.Set(ical.ParamCommonName, s.displayOf(owner))
	ev.Props.Set(org)
	for _, g := range gs {
		a := ical.NewProp(ical.PropAttendee)
		a.Value = address(g.User)
		a.Params.Set(ical.ParamCommonName, g.Who)
		a.Params.Set(ical.ParamParticipationStatus, g.Status)
		a.Params.Set("X-COVE-USER", g.User)
		ev.Props.Add(a)
	}
}

// inviteOccurrences adds the events a user is invited to (and hasn't declined).
func (s *Server) inviteOccurrences(out []occurrence, u user, loc *time.Location, from, to time.Time) []occurrence {
	rows, err := s.db.Query(`SELECT i.id, i.owner, i.cal, i.name, i.status, e.ics FROM invites i
		JOIN events e ON e.user = i.owner AND e.cal = i.cal AND e.name = i.name WHERE i.target = ? AND i.status != 'DECLINED'`, u.Name)
	if err != nil {
		return out
	}
	type rec struct {
		inv        invite
		owner, ics string
	}
	var recs []rec
	for rows.Next() {
		var x rec
		var cal, name string
		if rows.Scan(&x.inv.ID, &x.owner, &cal, &name, &x.inv.Status, &x.ics) == nil {
			recs = append(recs, x)
		}
	}
	rows.Close() // the database has one connection: no other query while these rows are open
	for _, x := range recs {
		inv := x.inv
		obj, err := ical.NewDecoder(strings.NewReader(x.ics)).Decode()
		if err != nil {
			continue
		}
		inv.Organizer = s.displayOf(x.owner)
		for _, o := range expand(obj, loc, from, to) {
			i := inv
			o.ID, o.Cal, o.ReadOnly, o.Invite = fmt.Sprintf("~invites/%d", inv.ID), "~invites", true, &i
			out = append(out, o)
		}
	}
	return out
}

func (s *Server) eventGuests(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.calendar(u.Name, r.PathValue("cal"))
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	gs := s.guests(c.Owner, c.id, r.PathValue("name"))
	if gs == nil {
		gs = []guest{}
	}
	writeJSON(w, http.StatusOK, gs)
}

// answerInvite records yes, maybe or no, in Cove and in the organizer's event.
func (s *Server) answerInvite(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Status string `json:"status"`
	}
	readJSON(r, &in)
	switch in.Status {
	case "ACCEPTED", "TENTATIVE", "DECLINED":
	default:
		jsonError(w, http.StatusBadRequest, "status is ACCEPTED, TENTATIVE or DECLINED")
		return
	}
	var owner, cal, name string
	if err := s.db.QueryRow(`SELECT owner, cal, name FROM invites WHERE id = ? AND target = ?`, r.PathValue("id"), u.Name).Scan(&owner, &cal, &name); err != nil {
		jsonError(w, http.StatusNotFound, "That invitation isn't there any more.")
		return
	}
	s.db.Exec(`UPDATE invites SET status = ? WHERE id = ?`, in.Status, r.PathValue("id"))
	if obj, err := s.loadObject(owner, cal, name); err == nil {
		title := ""
		for _, ev := range obj.Events() {
			s.writeAttendees(owner, cal, name, &ev)
			if title == "" {
				title, _ = ev.Props.Text(ical.PropSummary)
			}
		}
		s.saveObject(owner, cal, name, obj)
		verb := map[string]string{"ACCEPTED": "is coming to", "TENTATIVE": "might come to", "DECLINED": "can't come to"}[in.Status]
		s.notify(owner, fmt.Sprintf("%s %s “%s”", displayName(u), verb, title), "#/calendar")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
