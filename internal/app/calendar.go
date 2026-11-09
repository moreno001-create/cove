package app

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// --- events

// occurrence is one showing of an event in the calendar view. Times are RFC 3339, or dates (YYYY-MM-DD, the end
// exclusive) for all-day events.
type occurrence struct {
	ID       string `json:"id"` // calendar/name: what to edit
	Cal      string `json:"cal"`
	Title    string `json:"title"`
	Location string `json:"location"`
	Notes    string `json:"notes"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"allDay"`
	Repeat   string `json:"repeat"` // "", DAILY, WEEKLY, MONTHLY, YEARLY, or "custom"
	// SeriesStart and SeriesEnd are the first occurrence's times (what editing a repeating event changes).
	SeriesStart string  `json:"seriesStart"`
	SeriesEnd   string  `json:"seriesEnd"`
	Changed     bool    `json:"changed,omitempty"`   // an occurrence changed on its own (edited elsewhere)
	ReadOnly    bool    `json:"readOnly,omitempty"`  // in a calendar shared to view
	Reminders   []int   `json:"reminders,omitempty"` // minutes before the start
	Invite      *invite `json:"invite,omitempty"`    // an invitation to someone else's event
	// Instance is which time of a repeating event this is (its original start), for changing only this one.
	Instance string `json:"instance,omitempty"`
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, u user) {
	q := r.URL.Query()
	loc, err := time.LoadLocation(q.Get("tz"))
	if err != nil || q.Get("tz") == "" {
		loc = time.UTC
	}
	from, err1 := time.ParseInLocation("2006-01-02", q.Get("from"), loc)
	to, err2 := time.ParseInLocation("2006-01-02", q.Get("to"), loc)
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > 400*24*time.Hour {
		jsonError(w, http.StatusBadRequest, "from and to are dates (YYYY-MM-DD), up to about a year apart.")
		return
	}
	writeJSON(w, http.StatusOK, s.eventsFor(u, loc, from, to.AddDate(0, 0, 1)))
}

// eventsFor is everything on a user's calendar between from and to: their calendars, those shared with them,
// invitations they haven't declined, and birthdays from their contacts.
func (s *Server) eventsFor(u user, loc *time.Location, from, to time.Time) []occurrence {
	out := []occurrence{}
	cals, _ := s.calendars(u.Name)
	for _, c := range cals {
		if c.Kind == "events" {
			out = s.calendarOccurrences(out, c, loc, from, to)
		}
	}
	out = s.inviteOccurrences(out, u, loc, from, to)
	out = s.birthdays(out, u, loc, from, to)
	out = s.subscriptionOccurrences(out, u, loc, from, to)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

func (s *Server) calendarOccurrences(out []occurrence, c calendarInfo, loc *time.Location, from, to time.Time) []occurrence {
	rows, err := s.db.Query(`SELECT name, ics FROM events WHERE user = ? AND cal = ?`, c.Owner, c.id)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, ics string
		if rows.Scan(&name, &ics) != nil {
			continue
		}
		obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
		if err != nil {
			continue
		}
		for _, o := range expand(obj, loc, from, to) {
			o.ID, o.Cal, o.ReadOnly = c.ID+"/"+name, c.ID, !c.Edit
			out = append(out, o)
		}
	}
	return out
}

// propTime reads a date or date-time, falling back to reading it as local time when its time zone has a name Go
// doesn't know (Outlook's Windows names).
func propTime(p *ical.Prop, loc *time.Location) (t time.Time, allDay bool, err error) {
	allDay = p.ValueType() == ical.ValueDate || len(p.Value) == 8
	t, err = p.DateTime(loc)
	if err != nil {
		layout := "20060102T150405"
		if allDay {
			layout = "20060102"
		}
		t, err = time.ParseInLocation(layout, strings.TrimSuffix(p.Value, "Z"), loc)
	}
	return t, allDay, err
}

// eventTimes is an event's start and end (DTEND, or DTSTART plus DURATION, or a day for all-day events).
func eventTimes(ev ical.Event, loc *time.Location) (start, end time.Time, allDay bool, err error) {
	sp := ev.Props.Get(ical.PropDateTimeStart)
	if sp == nil {
		return start, end, false, errors.New("no start")
	}
	if start, allDay, err = propTime(sp, loc); err != nil {
		return
	}
	end = start
	if allDay {
		end = start.AddDate(0, 0, 1)
	}
	if ep := ev.Props.Get(ical.PropDateTimeEnd); ep != nil {
		if e, _, err := propTime(ep, loc); err == nil && !e.Before(start) {
			end = e
		}
	} else if dp := ev.Props.Get(ical.PropDuration); dp != nil {
		if d, err := dp.Duration(); err == nil && d >= 0 {
			end = start.Add(d)
		}
	}
	return start, end, allDay, nil
}

func formatTime(t time.Time, allDay bool) string {
	if allDay {
		return t.Format("2006-01-02")
	}
	return t.Format(time.RFC3339)
}

var simpleRule = regexp.MustCompile(`^FREQ=(DAILY|WEEKLY|MONTHLY|YEARLY)$`)

func repeatOf(ev ical.Event) string {
	p := ev.Props.Get(ical.PropRecurrenceRule)
	if p == nil {
		return ""
	}
	if m := simpleRule.FindStringSubmatch(strings.ToUpper(p.Value)); m != nil {
		return m[1]
	}
	return "custom"
}

func cancelled(ev ical.Event) bool {
	st, _ := ev.Props.Text(ical.PropStatus)
	return strings.EqualFold(st, "CANCELLED")
}

// expand lists an object's occurrences between from and to: the event, its repeats (less excluded dates and
// occurrences changed on their own), and the changed occurrences.
func expand(obj *ical.Calendar, loc *time.Location, from, to time.Time) []occurrence {
	var masters []ical.Event
	overrides := map[int64]ical.Event{}
	for _, ev := range obj.Events() {
		if rp := ev.Props.Get(ical.PropRecurrenceID); rp != nil {
			if t, _, err := propTime(rp, loc); err == nil {
				overrides[t.Unix()] = ev
			}
			continue
		}
		masters = append(masters, ev)
	}
	overlaps := func(start, end time.Time) bool {
		if end.Equal(start) {
			return !start.Before(from) && start.Before(to)
		}
		return start.Before(to) && end.After(from)
	}
	var out []occurrence
	add := func(ev ical.Event, start, end time.Time, allDay bool, series ical.Event, changed bool, instance string) {
		o := occurrence{Start: formatTime(start, allDay), End: formatTime(end, allDay), AllDay: allDay, Changed: changed, Instance: instance}
		o.Title, _ = ev.Props.Text(ical.PropSummary)
		o.Location, _ = ev.Props.Text(ical.PropLocation)
		o.Notes, _ = ev.Props.Text(ical.PropDescription)
		o.Repeat = repeatOf(series)
		o.Reminders = alarmsOf(ev)
		if ss, se, sAllDay, err := eventTimes(series, loc); err == nil {
			o.SeriesStart, o.SeriesEnd = formatTime(ss, sAllDay), formatTime(se, sAllDay)
		}
		out = append(out, o)
	}
	for _, ev := range masters {
		if cancelled(ev) {
			continue
		}
		start, end, allDay, err := eventTimes(ev, loc)
		if err != nil {
			continue
		}
		set, err := ev.RecurrenceSet(loc)
		if err != nil || set == nil {
			if overlaps(start, end) {
				add(ev, start, end, allDay, ev, false, "")
			}
			continue
		}
		dur := end.Sub(start)
		for _, t := range set.Between(from.Add(-dur-time.Second), to, true) {
			if _, ok := overrides[t.Unix()]; ok {
				continue
			}
			if allDay { // repeats keep to the date, across clock changes
				t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
			}
			e := t.Add(dur)
			if allDay {
				e = t.AddDate(0, 0, int(dur.Hours()/24+0.5))
			}
			if overlaps(t, e) {
				add(ev, t, e, allDay, ev, false, formatTime(t, allDay))
			}
		}
	}
	for _, ev := range overrides {
		if cancelled(ev) {
			continue
		}
		if start, end, allDay, err := eventTimes(ev, loc); err == nil && overlaps(start, end) {
			series := ev
			if len(masters) > 0 {
				series = masters[0]
			}
			rid, ridAllDay, _ := propTime(ev.Props.Get(ical.PropRecurrenceID), loc)
			add(ev, start, end, allDay, series, true, formatTime(rid, ridAllDay))
		}
	}
	return out
}

// eventInput is what the app sends to make or change an event. Changing a repeating event changes all of it.
type eventInput struct {
	Cal      string `json:"cal"`
	Title    string `json:"title"`
	Location string `json:"location"`
	Notes    string `json:"notes"`
	AllDay   bool   `json:"allDay"`
	Start    string `json:"start"` // RFC 3339, or YYYY-MM-DD for all-day events
	End      string `json:"end"`   // likewise; all-day ends are exclusive
	Repeat   string `json:"repeat"`
	TZ       string `json:"tz"` // the browser's time zone, so repeats keep their local time
	// Reminder is minutes before the start (0: at the start, -1: none); left out, the event's alarms stay as they are.
	Reminder *int `json:"reminder"`
	// Invite is who in the household is invited (user names); left out, invitations stay as they are.
	Invite *[]string `json:"invite"`
	// Scope "this" changes only the occurrence at Instance of a repeating event.
	Scope    string `json:"scope"`
	Instance string `json:"instance"`
}

// alarmsOf lists an event's reminders, in minutes before its start (alarms relative to the start).
func alarmsOf(ev ical.Event) []int {
	var out []int
	for _, a := range ev.Children {
		if a.Name != ical.CompAlarm {
			continue
		}
		p := a.Props.Get(ical.PropTrigger)
		if p == nil || p.ValueType() == ical.ValueDateTime || strings.EqualFold(p.Params.Get("RELATED"), "END") {
			continue
		}
		if d, err := p.Duration(); err == nil && d <= 0 {
			out = append(out, int(-d/time.Minute))
		}
	}
	return out
}

func setReminder(ev *ical.Event, minutes int, title string) {
	kept := ev.Children[:0]
	for _, c := range ev.Children {
		if c.Name != ical.CompAlarm {
			kept = append(kept, c)
		}
	}
	ev.Children = kept
	if minutes < 0 {
		return
	}
	a := ical.NewComponent(ical.CompAlarm)
	a.Props.SetText(ical.PropAction, "DISPLAY")
	a.Props.SetText(ical.PropDescription, title)
	t := ical.NewProp(ical.PropTrigger)
	t.Value = "-PT" + strconv.Itoa(minutes) + "M"
	if minutes == 0 {
		t.Value = "PT0S"
	}
	a.Props.Set(t)
	ev.Children = append(ev.Children, a)
}

// apply writes the input into an event, keeping anything else it has (alarms, attendees, a custom repeat).
func (in eventInput) apply(ev *ical.Event) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		in.Title = "New event"
	}
	loc, err := time.LoadLocation(in.TZ)
	if err != nil || in.TZ == "" || in.TZ == "Local" {
		loc = time.UTC
	}
	var start, end time.Time
	if in.AllDay {
		start, err = time.Parse("2006-01-02", in.Start)
		if err != nil {
			return errors.New("the start date isn't a date")
		}
		end, err = time.Parse("2006-01-02", in.End)
		if err != nil || !end.After(start) {
			end = start.AddDate(0, 0, 1)
		}
		ev.Props.SetDate(ical.PropDateTimeStart, start)
		ev.Props.SetDate(ical.PropDateTimeEnd, end)
	} else {
		start, err = time.Parse(time.RFC3339, in.Start)
		if err != nil {
			return errors.New("the start isn't a time")
		}
		end, err = time.Parse(time.RFC3339, in.End)
		if err != nil || end.Before(start) {
			end = start.Add(time.Hour)
		}
		ev.Props.SetDateTime(ical.PropDateTimeStart, start.In(loc))
		ev.Props.SetDateTime(ical.PropDateTimeEnd, end.In(loc))
	}
	ev.Props.Del(ical.PropDuration)
	setText(ev, ical.PropSummary, in.Title)
	setText(ev, ical.PropLocation, strings.TrimSpace(in.Location))
	setText(ev, ical.PropDescription, strings.TrimSpace(in.Notes))
	switch in.Repeat {
	case "custom":
		// kept as it is
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
		p := ical.NewProp(ical.PropRecurrenceRule)
		p.Value = "FREQ=" + in.Repeat
		ev.Props.Set(p)
	case "":
		ev.Props.Del(ical.PropRecurrenceRule)
		ev.Props.Del(ical.PropExceptionDates)
	default:
		return errors.New("repeat is DAILY, WEEKLY, MONTHLY, YEARLY or empty")
	}
	if in.Reminder != nil {
		setReminder(ev, *in.Reminder, in.Title)
	}
	now := time.Now().UTC()
	ev.Props.SetDateTime(ical.PropDateTimeStamp, now)
	ev.Props.SetDateTime(ical.PropLastModified, now)
	return nil
}

func setText(ev *ical.Event, name, v string) {
	if v == "" {
		ev.Props.Del(name)
	} else {
		ev.Props.SetText(name, v)
	}
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request, u user) {
	var in eventInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.writable(u.Name, in.Cal)
	if err != nil || c.Kind != "events" {
		jsonError(w, http.StatusBadRequest, "Pick a calendar you can add to.")
		return
	}
	uid := strings.ToLower(randomToken(18)) + "@cove"
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
	if err := in.apply(ev); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	obj := ical.NewCalendar()
	obj.Children = append(obj.Children, ev.Component)
	name := strings.TrimSuffix(uid, "@cove") + ".ics"
	s.setInvites(u, c, name, ev, in.Invite)
	if _, err := s.saveObject(c.Owner, c.id, name, obj); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.calendarActivity(u, c, "added “"+strings.TrimSpace(in.Title)+"” to")
	s.logActivity(c.Owner, u, "added “"+strings.TrimSpace(in.Title)+"” to "+c.Name, undoStep{Kind: "trash-object", Cal: c.id, Name: name})
	writeJSON(w, http.StatusCreated, map[string]string{"id": c.ID + "/" + name})
}

func (s *Server) loadObject(user, cal, name string) (*ical.Calendar, error) {
	var ics string
	if err := s.db.QueryRow(`SELECT ics FROM events WHERE user = ? AND cal = ? AND name = ?`, user, cal, name).Scan(&ics); err != nil {
		return nil, err
	}
	return ical.NewDecoder(strings.NewReader(ics)).Decode()
}

func (s *Server) updateEvent(w http.ResponseWriter, r *http.Request, u user) {
	cal, name := r.PathValue("cal"), r.PathValue("name")
	var in eventInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := s.writable(u.Name, cal)
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	before := s.objectText(from.Owner, from.id, name)
	obj, err := s.loadObject(from.Owner, from.id, name)
	if err != nil {
		jsonError(w, http.StatusNotFound, "That event isn't there any more.")
		return
	}
	var master *ical.Event
	if in.Scope == "this" && in.Instance != "" {
		if master, err = overrideFor(obj, in.Instance); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		in.Repeat, in.Invite, in.Cal = "", nil, cal // one occurrence doesn't repeat, and stays in the series' calendar
	} else {
		for _, ev := range obj.Events() {
			if ev.Props.Get(ical.PropRecurrenceID) == nil {
				master = &ev
				break
			}
		}
	}
	if master == nil {
		jsonError(w, http.StatusBadRequest, "That event can't be changed here.")
		return
	}
	if err := in.apply(master); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	seq := 0
	if p := master.Props.Get(ical.PropSequence); p != nil {
		seq, _ = p.Int()
	}
	sp := ical.NewProp(ical.PropSequence)
	sp.Value = strconv.Itoa(seq + 1)
	master.Props.Set(sp)
	if in.Cal == "" {
		in.Cal = cal
	}
	to, err := s.writable(u.Name, in.Cal)
	if err != nil || to.Kind != from.Kind {
		jsonError(w, http.StatusBadRequest, "Pick a calendar you can add to.")
		return
	}
	if to.Owner != from.Owner || to.id != from.id { // invitations follow it to another calendar
		s.db.Exec(`UPDATE invites SET owner = ?, cal = ? WHERE owner = ? AND cal = ? AND name = ?`, to.Owner, to.id, from.Owner, from.id, name)
	}
	s.setInvites(u, to, name, master, in.Invite)
	if _, err := s.saveObject(to.Owner, to.id, name, obj); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if to.Owner != from.Owner || to.id != from.id { // moved to another calendar
		s.db.Exec(`DELETE FROM events WHERE user = ? AND cal = ? AND name = ?`, from.Owner, from.id, name)
	}
	s.calendarActivity(u, to, "changed “"+strings.TrimSpace(in.Title)+"” in")
	undo := undoStep{Kind: "put-object", Cal: from.id, Name: name, Data: before}
	if to.Owner != from.Owner || to.id != from.id {
		undo = undoStep{} // moved between calendars: not undone here
	}
	s.logActivity(to.Owner, u, "changed “"+strings.TrimSpace(in.Title)+"” in "+to.Name, undo)
	writeJSON(w, http.StatusOK, map[string]string{"id": to.ID + "/" + name})
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.writable(u.Name, r.PathValue("cal"))
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	if inst := r.URL.Query().Get("instance"); r.URL.Query().Get("scope") == "this" && inst != "" {
		before := s.objectText(c.Owner, c.id, r.PathValue("name"))
		obj, err := s.loadObject(c.Owner, c.id, r.PathValue("name"))
		if err == nil {
			err = skipInstance(obj, inst)
		}
		defer s.logActivity(c.Owner, u, "skipped one time of an event in "+c.Name, undoStep{Kind: "put-object", Cal: c.id, Name: r.PathValue("name"), Data: before})
		if err == nil {
			_, err = s.saveObject(c.Owner, c.id, r.PathValue("name"), obj)
		}
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.calendarActivity(u, c, "skipped one time of an event in")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := s.trashObject(u, c, r.PathValue("name"), "event"); err != nil {
		jsonError(w, http.StatusNotFound, "That event isn't there any more.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
