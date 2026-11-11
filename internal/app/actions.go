package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// Ask Cove can do things, not only answer: the model is offered tools (add to the shopping list, put an event on the
// calendar, add a task, a note on the fridge, plan a meal, a chore, spending, switch a light), and Cove carries
// them out as the person asking, with that person's rights. Each one is reported back ("Added Milk to the
// shopping list"), so nothing happens unseen.

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func (s *Server) tools(u user) []tool {
	t := func(name, desc string, params map[string]any) tool {
		return tool{"function", toolFunction{name, desc, params}}
	}
	list := []tool{
		t("add_to_shopping_list", "Add things to the household's shopping list on the fridge.",
			obj([]string{"items"}, map[string]any{"items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}})),
		t("add_event", "Put an event on the user's calendar.", obj([]string{"title", "date"}, map[string]any{
			"title": str("what it is"), "date": str("YYYY-MM-DD"), "time": str("HH:MM, 24-hour; leave out for an all-day event"),
			"minutes": map[string]any{"type": "integer", "description": "how long, default 60"}, "location": str("where")})),
		t("add_task", "Add a to-do to the user's task list.", obj([]string{"title"}, map[string]any{
			"title": str("the task"), "due": str("YYYY-MM-DD, optional"), "for": str("the person it's for, optional")})),
		t("put_on_fridge", "Put a note on the family fridge (the notice board everyone sees).", obj([]string{"text"}, map[string]any{"text": str("the note")})),
		t("post_to_family_chat", "Send a message to the family chat, as the user (\"on my way home, there in 20 minutes\").", obj([]string{"text"}, map[string]any{"text": str("the message")})),
		t("plan_meal", "Plan a meal on the meal planner.", obj([]string{"date", "what"}, map[string]any{
			"date": str("YYYY-MM-DD"), "meal": str("breakfast, lunch or dinner (default dinner)"), "what": str("the dish, or a saved recipe's name")})),
	}
	if !u.Kid {
		list = append(list,
			t("add_chore", "Add a household chore.", obj([]string{"title"}, map[string]any{
				"title": str("the chore"), "who": str("who does it, optional"), "repeat": str("daily, weekdays, weekly or once (default weekly)")})),
			t("add_expense", "Write down money spent.", obj([]string{"what", "amount"}, map[string]any{
				"what": str("what it was"), "amount": map[string]any{"type": "number"}, "category": str("Food, Home, Car…")})))
	}
	if c := s.haConfig(); c.Token != "" && len(c.Entities) > 0 {
		var names []string
		for _, e := range c.Entities {
			names = append(names, e.ID)
		}
		list = append(list, t("switch_device", "Control something at home through Home Assistant. Devices: "+strings.Join(names, ", "),
			obj([]string{"device", "action"}, map[string]any{"device": str("the entity id, like light.kitchen"), "action": str("on, off, toggle, lock, unlock, open or close")})))
	}
	return list
}

// invoke runs one of Cove's own handlers as u, with body as the JSON it gets.
func (s *Server) invoke(h func(http.ResponseWriter, *http.Request, user), u user, method, target string, body any) (int, []byte) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, target, bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h(rec, req, u)
	return rec.Code, rec.Body.Bytes()
}

func errorOf(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal(b, &e)
	if e.Error == "" {
		return "that didn't work"
	}
	return e.Error
}

// person finds a household member by name or display name ("" if nobody matches).
func (s *Server) person(home int64, name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" || name == "me" {
		return ""
	}
	for _, n := range s.members(home) {
		if strings.ToLower(n) == name || strings.ToLower(s.displayOf(n)) == name {
			return n
		}
	}
	return ""
}

func (s *Server) firstCalendar(u user, kind string) string {
	cals, _ := s.calendars(u.Name)
	for _, c := range cals {
		if c.Kind == kind && c.Owner == u.Name && !c.Shared && !c.Virtual && c.Subscription == nil {
			return c.ID
		}
	}
	return ""
}

// act carries out one tool call and says what happened.
func (s *Server) act(ctx context.Context, u user, name, args string) string {
	var a map[string]any
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "Couldn't read what to do: " + err.Error()
	}
	get := func(k string) string { v, _ := a[k].(string); return strings.TrimSpace(v) }
	loc := s.location(u.Name)
	switch name {
	case "add_to_shopping_list":
		var items []string
		if list, ok := a["items"].([]any); ok {
			for _, x := range list {
				if t, ok := x.(string); ok {
					items = append(items, t)
				}
			}
		}
		n, err := s.addToShopping(u, items)
		if err != nil {
			return "Couldn't add to the shopping list: " + err.Error()
		}
		if n == 0 {
			return "Those are on the shopping list already."
		}
		return fmt.Sprintf("Added %s to the shopping list.", strings.Join(items, ", "))
	case "add_event":
		cal := s.firstCalendar(u, "events")
		day, err := time.ParseInLocation("2006-01-02", get("date"), loc)
		if err != nil || cal == "" {
			return "Couldn't add the event: which day?"
		}
		in := eventInput{Cal: cal, Title: get("title"), Location: get("location"), TZ: loc.String()}
		when := day.Format("Mon Jan 2")
		if t := get("time"); clockTime.MatchString(t) {
			start, _ := time.ParseInLocation("2006-01-02 15:04", get("date")+" "+t, loc)
			mins, _ := a["minutes"].(float64)
			if mins <= 0 {
				mins = 60
			}
			in.Start, in.End = start.Format(time.RFC3339), start.Add(time.Duration(mins)*time.Minute).Format(time.RFC3339)
			when += " at " + start.Format("3:04 PM")
		} else {
			in.AllDay, in.Start, in.End = true, day.Format("2006-01-02"), day.AddDate(0, 0, 1).Format("2006-01-02")
		}
		if code, b := s.invoke(s.createEvent, u, "POST", "/api/events", in); code >= 300 {
			return "Couldn't add the event: " + errorOf(b)
		}
		return fmt.Sprintf("Put “%s” on your calendar, %s.", in.Title, when)
	case "add_task":
		list := s.firstCalendar(u, "tasks")
		in := map[string]any{"list": list, "title": get("title"), "tz": loc.String()}
		if d := get("due"); validDate(d) {
			in["due"] = d
		}
		if who := s.person(u.Home, get("for")); who != "" {
			in["assignee"] = who
		}
		note := ""
		code, b := s.invoke(s.createTask, u, "POST", "/api/tasks", in)
		if code >= 300 && in["assignee"] != nil { // not a list they share: add it anyway, unassigned
			delete(in, "assignee")
			code, b = s.invoke(s.createTask, u, "POST", "/api/tasks", in)
			note = fmt.Sprintf(" (not assigned to %s: share the list with them first)", get("for"))
		}
		if code >= 300 {
			return "Couldn't add the task: " + errorOf(b)
		}
		return fmt.Sprintf("Added the task “%s”.%s", get("title"), note)
	case "put_on_fridge":
		if code, b := s.invoke(s.addFridge, u, "POST", "/api/fridge", fridgeInput{Kind: "note", Text: get("text"), Color: "yellow"}); code >= 300 {
			return "Couldn't put it on the fridge: " + errorOf(b)
		}
		return "Put a note on the fridge: “" + firstLine(get("text")) + "”."
	case "post_to_family_chat":
		if err := s.postToFamily(u, get("text")); err != nil {
			return "Couldn't post it: " + err.Error()
		}
		return "Told the family: “" + firstLine(get("text")) + "”."
	case "plan_meal":
		m := meal{Date: get("date"), Slot: strings.ToLower(get("meal")), Text: get("what")}
		for _, r := range hhItems[recipe](s, u, "recipe") {
			if strings.EqualFold(r.V.Title, m.Text) {
				m.Recipe, m.Text = r.ID, ""
			}
		}
		return s.hhAct(u, "meal", m, fmt.Sprintf("Planned %s for %s on %s.", get("what"), orDefault(m.Slot, "dinner"), m.Date))
	case "add_chore":
		c := chore{Title: get("title"), Repeat: get("repeat"), Points: 1}
		if c.Repeat == "weekly" || c.Repeat == "" {
			c.Repeat = fmt.Sprintf("weekly:%d", int(time.Now().In(loc).Weekday()))
		}
		if who := s.person(u.Home, get("who")); who != "" {
			c.Who = []string{who}
		}
		return s.hhAct(u, "chore", c, "Added the chore “"+c.Title+"”.")
	case "add_expense":
		amount, _ := a["amount"].(float64)
		e := expense{Title: get("what"), Amount: amount, Category: get("category"), PaidBy: u.Name, Date: time.Now().In(loc).Format("2006-01-02")}
		return s.hhAct(u, "expense", e, fmt.Sprintf("Wrote down %s, $%.2f.", e.Title, e.Amount))
	case "switch_device":
		if err := s.haAction(ctx, u, get("device"), get("action")); err != nil {
			return "Couldn't do that: " + err.Error()
		}
		return fmt.Sprintf("Done: %s %s.", get("device"), get("action"))
	}
	return "Cove can't do that."
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// hhAct saves a household record as u (through the same checks as the app).
func (s *Server) hhAct(u user, kind string, v any, done string) string {
	req := httptest.NewRequest("POST", "/api/hh/"+kind, nil)
	req.SetPathValue("kind", kind)
	b, _ := json.Marshal(v)
	req.Body = httpBody(b)
	rec := httptest.NewRecorder()
	s.saveHH(rec, req, u)
	if rec.Code >= 300 {
		return "Couldn't: " + errorOf(rec.Body.Bytes())
	}
	return done
}

type bodyReader struct{ *bytes.Reader }

func (bodyReader) Close() error { return nil }

func httpBody(b []byte) bodyReader { return bodyReader{bytes.NewReader(b)} }
