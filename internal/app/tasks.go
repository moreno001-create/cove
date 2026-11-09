package app

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Tasks are VTODOs in task lists (calendars of kind "tasks"): what iPhone Reminders and other CalDAV task apps sync.

type task struct {
	ID        string `json:"id"` // list/name
	List      string `json:"list"`
	Title     string `json:"title"`
	Notes     string `json:"notes"`
	Due       string `json:"due"` // YYYY-MM-DD, or RFC 3339 with a time
	Done      bool   `json:"done"`
	Completed int64  `json:"completed"` // unix seconds
	Priority  int    `json:"priority"`  // 1 high … 9 low, 0 none
	Created   int64  `json:"created"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
	UID       string `json:"uid"`
	Repeat    string `json:"repeat"`   // "", DAILY, WEEKLY, MONTHLY, YEARLY or custom
	Assignee  string `json:"assignee"` // a user name
	Parent    string `json:"parent"`   // the UID of the task this is a step of
}

// X-COVE-ASSIGNEE names who a task is for (someone the list is shared with).
const propAssignee = "X-COVE-ASSIGNEE"

func asTask(id, list string, c *ical.Component, loc *time.Location) task {
	t := task{ID: id, List: list}
	t.Title, _ = c.Props.Text(ical.PropSummary)
	t.Notes, _ = c.Props.Text(ical.PropDescription)
	if p := c.Props.Get(ical.PropDue); p != nil {
		if d, allDay, err := propTime(p, loc); err == nil {
			t.Due = formatTime(d, allDay)
		}
	}
	st, _ := c.Props.Text(ical.PropStatus)
	t.Done = strings.EqualFold(st, "COMPLETED")
	if p := c.Props.Get(ical.PropCompleted); p != nil {
		if d, err := p.DateTime(time.UTC); err == nil {
			t.Completed = d.Unix()
			t.Done = true
		}
	}
	if p := c.Props.Get(ical.PropPriority); p != nil {
		t.Priority, _ = p.Int()
	}
	if p := c.Props.Get(ical.PropCreated); p != nil {
		if d, err := p.DateTime(time.UTC); err == nil {
			t.Created = d.Unix()
		}
	}
	t.UID, _ = c.Props.Text(ical.PropUID)
	t.Repeat = repeatOf(ical.Event{Component: c})
	t.Assignee, _ = c.Props.Text(propAssignee)
	if p := c.Props.Get(ical.PropRelatedTo); p != nil && (p.Params.Get("RELTYPE") == "" || strings.EqualFold(p.Params.Get("RELTYPE"), "PARENT")) {
		t.Parent = p.Value
	}
	return t
}

func todoOf(obj *ical.Calendar) *ical.Component {
	for _, c := range obj.Children {
		if c.Name == ical.CompToDo && c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
	}
	return nil
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, u user) {
	loc, err := time.LoadLocation(r.URL.Query().Get("tz"))
	if err != nil {
		loc = time.UTC
	}
	writeJSON(w, http.StatusOK, s.tasksFor(u.Name, loc, r.URL.Query().Get("list")))
}

// tasksFor lists a user's tasks (in one list, or all): to do first, soonest due, then done.
func (s *Server) tasksFor(user string, loc *time.Location, only string) []task {
	out := []task{}
	cals, err := s.calendars(user)
	if err != nil {
		return out
	}
	for _, c := range cals {
		if c.Kind != "tasks" || (only != "" && c.ID != only) {
			continue
		}
		rows, err := s.db.Query(`SELECT name, ics FROM events WHERE user = ? AND cal = ?`, c.Owner, c.id)
		if err != nil {
			continue
		}
		for rows.Next() {
			var name, ics string
			if rows.Scan(&name, &ics) != nil {
				continue
			}
			obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
			if err != nil {
				continue
			}
			if todo := todoOf(obj); todo != nil {
				t := asTask(c.ID+"/"+name, c.ID, todo, loc)
				t.ReadOnly = !c.Edit
				out = append(out, t)
			}
		}
		rows.Close()
	}
	// To do first (soonest due, then oldest), then done (latest first).
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Done != b.Done {
			return !a.Done
		}
		if a.Done {
			return a.Completed > b.Completed
		}
		if (a.Due == "") != (b.Due == "") {
			return a.Due != ""
		}
		if a.Due != b.Due {
			return a.Due < b.Due
		}
		return a.Created < b.Created
	})
	return out
}

type taskInput struct {
	List     string `json:"list"`
	Title    string `json:"title"`
	Notes    string `json:"notes"`
	Due      string `json:"due"` // "", YYYY-MM-DD or RFC 3339
	Done     bool   `json:"done"`
	Priority int    `json:"priority"`
	TZ       string `json:"tz"`
	// Left out, these stay as they are.
	Repeat   *string `json:"repeat"`
	Assignee *string `json:"assignee"`
	Parent   *string `json:"parent"`
}

// apply writes the input into a task. Ticking off a repeating task moves it to its next due date instead (next is
// that date then).
func (in taskInput) apply(c *ical.Component) (next string) {
	ev := &ical.Event{Component: c} // for setText
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "New task"
	}
	setText(ev, ical.PropSummary, title)
	setText(ev, ical.PropDescription, strings.TrimSpace(in.Notes))
	c.Props.Del(ical.PropDue)
	if d, err := time.Parse("2006-01-02", in.Due); err == nil {
		c.Props.SetDate(ical.PropDue, d)
	} else if t, err := time.Parse(time.RFC3339, in.Due); err == nil {
		loc, err := time.LoadLocation(in.TZ)
		if err != nil || in.TZ == "" || in.TZ == "Local" {
			loc = time.UTC
		}
		c.Props.SetDateTime(ical.PropDue, t.In(loc))
	}
	if in.Priority > 0 && in.Priority <= 9 {
		p := ical.NewProp(ical.PropPriority)
		p.Value = strconv.Itoa(in.Priority)
		c.Props.Set(p)
	} else {
		c.Props.Del(ical.PropPriority)
	}
	if in.Repeat != nil {
		switch *in.Repeat {
		case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
			p := ical.NewProp(ical.PropRecurrenceRule)
			p.Value = "FREQ=" + *in.Repeat
			c.Props.Set(p)
		case "custom":
		default:
			c.Props.Del(ical.PropRecurrenceRule)
		}
	}
	if in.Assignee != nil {
		if *in.Assignee == "" {
			c.Props.Del(propAssignee)
		} else {
			c.Props.SetText(propAssignee, *in.Assignee)
		}
	}
	if in.Parent != nil {
		if *in.Parent == "" {
			c.Props.Del(ical.PropRelatedTo)
		} else {
			p := ical.NewProp(ical.PropRelatedTo)
			p.Value = *in.Parent
			c.Props.Set(p)
		}
	}
	now := time.Now().UTC()
	st, _ := c.Props.Text(ical.PropStatus)
	wasDone := strings.EqualFold(st, "COMPLETED")
	if in.Done && !wasDone && c.Props.Get(ical.PropRecurrenceRule) != nil && c.Props.Get(ical.PropDue) != nil {
		if d := nextDue(c); d != "" {
			c.Props.SetDateTime(ical.PropDateTimeStamp, now)
			c.Props.SetDateTime(ical.PropLastModified, now)
			return d
		}
	}
	if in.Done && !wasDone {
		c.Props.SetText(ical.PropStatus, "COMPLETED")
		c.Props.SetDateTime(ical.PropCompleted, now)
		p := ical.NewProp(ical.PropPercentComplete)
		p.Value = "100"
		c.Props.Set(p)
	} else if !in.Done {
		c.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		c.Props.Del(ical.PropCompleted)
		c.Props.Del(ical.PropPercentComplete)
	}
	c.Props.SetDateTime(ical.PropDateTimeStamp, now)
	c.Props.SetDateTime(ical.PropLastModified, now)
	return ""
}

// nextDue moves a repeating task's due date to the next time after the current one, and says what it is.
func nextDue(c *ical.Component) string {
	due := c.Props.Get(ical.PropDue)
	opt, err := c.Props.RecurrenceRule()
	if err != nil || opt == nil {
		return ""
	}
	at, allDay, err := propTime(due, time.UTC)
	if err != nil {
		return ""
	}
	if tz := due.Params.Get(ical.PropTimezoneID); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			at = at.In(loc)
		}
	}
	opt.Dtstart = at
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return ""
	}
	next := rule.After(at, false)
	if next.IsZero() { // the repeats are over: it's simply done
		return ""
	}
	if allDay {
		c.Props.SetDate(ical.PropDue, next)
		return next.Format("2006-01-02")
	}
	c.Props.SetDateTime(ical.PropDue, next)
	return next.Format(time.RFC3339)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request, u user) {
	var in taskInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.writable(u.Name, in.List)
	if err != nil || c.Kind != "tasks" {
		jsonError(w, http.StatusBadRequest, "Pick a list you can add to.")
		return
	}
	uid := strings.ToLower(randomToken(18)) + "@cove"
	todo := ical.NewComponent(ical.CompToDo)
	todo.Props.SetText(ical.PropUID, uid)
	todo.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
	if in.Assignee != nil && *in.Assignee != "" && !s.canSee(c, *in.Assignee) {
		jsonError(w, http.StatusBadRequest, "Share the list with them first.")
		return
	}
	in.apply(todo)
	obj := ical.NewCalendar()
	obj.Children = append(obj.Children, todo)
	name := strings.TrimSuffix(uid, "@cove") + ".ics"
	if _, err := s.saveObject(c.Owner, c.id, name, obj); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.calendarActivity(u, c, "added “"+strings.TrimSpace(in.Title)+"” to")
	s.logActivity(c.Owner, u, "added the task “"+strings.TrimSpace(in.Title)+"” to "+c.Name, undoStep{Kind: "trash-object", Cal: c.id, Name: name})
	if in.Assignee != nil && *in.Assignee != "" && *in.Assignee != u.Name {
		s.notify(*in.Assignee, fmt.Sprintf("%s gave you “%s” in %s", displayName(u), strings.TrimSpace(in.Title), c.Name), "#/tasks")
	}
	writeJSON(w, http.StatusCreated, asTask(c.ID+"/"+name, c.ID, todo, time.UTC))
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request, u user) {
	var in taskInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := s.writable(u.Name, r.PathValue("list"))
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	name := r.PathValue("name")
	beforeText := s.objectText(from.Owner, from.id, name)
	obj, err := s.loadObject(from.Owner, from.id, name)
	if err != nil {
		jsonError(w, http.StatusNotFound, "That task isn't there any more.")
		return
	}
	todo := todoOf(obj)
	if todo == nil {
		jsonError(w, http.StatusBadRequest, "That isn't a task.")
		return
	}
	st, _ := todo.Props.Text(ical.PropStatus)
	wasDone := strings.EqualFold(st, "COMPLETED")
	before, _ := todo.Props.Text(propAssignee)
	if in.Assignee != nil && *in.Assignee != "" && !s.canSee(from, *in.Assignee) {
		jsonError(w, http.StatusBadRequest, "Share the list with them first.")
		return
	}
	next := in.apply(todo)
	to := from
	if in.List != "" && in.List != from.ID {
		if to, err = s.writable(u.Name, in.List); err != nil || to.Kind != "tasks" {
			jsonError(w, http.StatusBadRequest, "Pick a list you can add to.")
			return
		}
	}
	if _, err := s.saveObject(to.Owner, to.id, name, obj); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if to.ID != from.ID {
		s.db.Exec(`DELETE FROM events WHERE user = ? AND cal = ? AND name = ?`, from.Owner, from.id, name)
	}
	if in.Done && !wasDone {
		s.calendarActivity(u, to, "checked off “"+strings.TrimSpace(in.Title)+"” in")
	}
	if in.Assignee != nil && *in.Assignee != before && *in.Assignee != "" && *in.Assignee != u.Name {
		s.notify(*in.Assignee, fmt.Sprintf("%s gave you “%s” in %s", displayName(u), strings.TrimSpace(in.Title), to.Name), "#/tasks")
	}
	undo := undoStep{Kind: "put-object", Cal: from.id, Name: name, Data: beforeText}
	if to.ID != from.ID {
		undo = undoStep{}
	}
	verb := "changed the task “"
	if in.Done && !wasDone {
		verb = "checked off “"
	}
	s.logActivity(to.Owner, u, verb+strings.TrimSpace(in.Title)+"” in "+to.Name, undo)
	_ = next
	writeJSON(w, http.StatusOK, asTask(to.ID+"/"+name, to.ID, todo, time.UTC))
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.writable(u.Name, r.PathValue("list"))
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := s.trashObject(u, c, r.PathValue("name"), "task"); err != nil {
		jsonError(w, http.StatusNotFound, "That task isn't there any more.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// canSee says whether a user can see a calendar or list: its owner, or someone it's shared with.
func (s *Server) canSee(c calendarInfo, user string) bool {
	for _, w := range s.calendarWatchers(c.Owner, c.id) {
		if w == user {
			return true
		}
	}
	return false
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.calendar(u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	out := []person{}
	for _, m := range s.calendarWatchers(c.Owner, c.id) {
		out = append(out, person{Name: m, Display: s.displayOf(m)})
	}
	writeJSON(w, http.StatusOK, out)
}
