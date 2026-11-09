package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
)

// Moving in and out: calendars and task lists take .ics files (an export from iCloud, Google or Outlook) and give
// them back; contacts take and give .vcf files. Importing again doesn't make copies: what's there already (the same
// UID) is updated, and contacts without one are matched by name and email or phone.

type importResult struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"` // not something this calendar or list holds
}

func (s *Server) importCalendar(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.writable(u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	data, err := ical.NewDecoder(io.LimitReader(r.Body, 50<<20)).Decode()
	if err != nil {
		jsonError(w, http.StatusBadRequest, "That isn't an iCalendar (.ics) file: "+err.Error())
		return
	}
	want := ical.CompEvent
	if c.Kind == "tasks" {
		want = ical.CompToDo
	}
	// Existing objects by UID, so importing twice doesn't duplicate.
	existing := map[string]struct{ name, ics string }{}
	rows, err := s.db.Query(`SELECT name, ics FROM events WHERE user = ? AND cal = ?`, c.Owner, c.id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var name, ics string
		if rows.Scan(&name, &ics) != nil {
			continue
		}
		if obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode(); err == nil {
			for _, ch := range obj.Children {
				if uid, _ := ch.Props.Text(ical.PropUID); uid != "" {
					existing[uid] = struct{ name, ics string }{name, ics}
					break
				}
			}
		}
	}
	rows.Close()

	zones := map[string]*ical.Component{}
	groups := map[string][]*ical.Component{}
	var order []string
	var res importResult
	for _, ch := range data.Children {
		switch ch.Name {
		case ical.CompTimezone:
			if id, _ := ch.Props.Text(ical.PropTimezoneID); id != "" {
				zones[id] = ch
			}
		case want:
			uid, _ := ch.Props.Text(ical.PropUID)
			if uid == "" {
				uid = strings.ToLower(randomToken(18)) + "@cove"
				ch.Props.SetText(ical.PropUID, uid)
			}
			if _, ok := groups[uid]; !ok {
				order = append(order, uid)
			}
			groups[uid] = append(groups[uid], ch)
		default:
			res.Skipped++
		}
	}
	for _, uid := range order {
		obj := ical.NewCalendar()
		obj.Props.SetText(ical.PropProductID, "-//Cove//EN")
		obj.Props.SetText(ical.PropVersion, "2.0")
		used := map[string]bool{}
		for _, ch := range groups[uid] {
			for _, props := range ch.Props {
				for _, p := range props {
					if tz := p.Params.Get(ical.PropTimezoneID); tz != "" {
						used[tz] = true
					}
				}
			}
		}
		for tz := range used {
			if z, ok := zones[tz]; ok {
				obj.Children = append(obj.Children, z)
			}
		}
		obj.Children = append(obj.Children, groups[uid]...)
		name := hashHex(uid)[:24] + ".ics"
		if old, ok := existing[uid]; ok {
			name = old.name
			var buf bytes.Buffer
			if ical.NewEncoder(&buf).Encode(obj) == nil && buf.String() == old.ics {
				res.Unchanged++
				continue
			}
		}
		if _, err := s.saveObject(c.Owner, c.id, name, obj); err != nil {
			res.Skipped++
			continue
		}
		if _, ok := existing[uid]; ok {
			res.Updated++
		} else {
			res.Added++
		}
	}
	if res.Added+res.Updated > 0 {
		s.calendarActivity(u, c, fmt.Sprintf("imported %d into", res.Added+res.Updated))
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) exportCalendar(w http.ResponseWriter, r *http.Request, u user) {
	c, err := s.calendar(u.Name, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, "-//Cove//EN")
	out.Props.SetText(ical.PropVersion, "2.0")
	out.Props.SetText("X-WR-CALNAME", c.Name)
	zones := map[string]bool{}
	rows, err := s.db.Query(`SELECT ics FROM events WHERE user = ? AND cal = ? ORDER BY name`, c.Owner, c.id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ics string
		if rows.Scan(&ics) != nil {
			continue
		}
		obj, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
		if err != nil {
			continue
		}
		for _, ch := range obj.Children {
			if ch.Name == ical.CompTimezone {
				id, _ := ch.Props.Text(ical.PropTimezoneID)
				if zones[id] {
					continue
				}
				zones[id] = true
			}
			out.Children = append(out.Children, ch)
		}
	}
	var buf bytes.Buffer
	if len(out.Children) > 0 {
		if err := ical.NewEncoder(&buf).Encode(out); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else { // the encoder wants at least one component; an empty calendar is still a valid file
		buf.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Cove//EN\r\nEND:VCALENDAR\r\n")
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(c.Name+".ics"))
	w.Write(buf.Bytes())
}

// cardKey matches contacts without a UID: the name and the first email or phone.
func cardKey(c vcard.Card) string {
	k := strings.ToLower(strings.TrimSpace(c.Value(vcard.FieldFormattedName)))
	if e := c.Value(vcard.FieldEmail); e != "" {
		return k + "|" + strings.ToLower(e)
	}
	return k + "|" + strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, c.Value(vcard.FieldTelephone))
}

func (s *Server) importContacts(w http.ResponseWriter, r *http.Request, u user) {
	books, err := s.addressBooks(u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	book := books[0].ID
	byUID, byKey := map[string]string{}, map[string]string{}
	vcfs := map[string]string{}
	rows, err := s.db.Query(`SELECT name, vcf FROM cards WHERE user = ? AND book = ?`, u.Name, book)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var name, vcf string
		if rows.Scan(&name, &vcf) != nil {
			continue
		}
		vcfs[name] = vcf
		if c, err := decodeCard(vcf); err == nil {
			if uid := c.Value(vcard.FieldUID); uid != "" {
				byUID[uid] = name
			}
			byKey[cardKey(c)] = name
		}
	}
	rows.Close()
	dec := vcard.NewDecoder(io.LimitReader(r.Body, 50<<20))
	var res importResult
	for {
		card, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if res.Added+res.Updated+res.Unchanged == 0 {
				jsonError(w, http.StatusBadRequest, "That isn't a contacts (.vcf) file: "+err.Error())
				return
			}
			break
		}
		if card.Value(vcard.FieldFormattedName) == "" {
			if n := card.Name(); n != nil && strings.TrimSpace(n.GivenName+" "+n.FamilyName) != "" {
				card.SetValue(vcard.FieldFormattedName, strings.TrimSpace(n.GivenName+" "+n.FamilyName))
			} else if o := card.Value(vcard.FieldOrganization); o != "" {
				card.SetValue(vcard.FieldFormattedName, strings.TrimSuffix(o, ";"))
			} else {
				res.Skipped++
				continue
			}
		}
		uid := card.Value(vcard.FieldUID)
		name, ok := byUID[uid]
		if uid == "" || !ok {
			name, ok = byKey[cardKey(card)]
		}
		if !ok {
			if uid == "" {
				uid = strings.ToLower(randomToken(16))
				card.SetValue(vcard.FieldUID, uid)
			}
			name = hashHex(uid)[:24] + ".vcf"
		}
		if ok {
			var buf bytes.Buffer
			if vcard.NewEncoder(&buf).Encode(card) == nil && buf.String() == vcfs[name] {
				res.Unchanged++
				continue
			}
		}
		if _, err := s.saveCard(u.Name, book, name, card); err != nil {
			res.Skipped++
			continue
		}
		if ok {
			res.Updated++
		} else {
			res.Added++
			byUID[uid], byKey[cardKey(card)] = name, name
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) exportContacts(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := s.db.Query(`SELECT vcf FROM cards WHERE user = ? ORDER BY name`, u.Name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"Contacts.vcf\"")
	for rows.Next() {
		var vcf string
		if rows.Scan(&vcf) == nil {
			io.WriteString(w, vcf)
		}
	}
}
