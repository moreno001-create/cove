package app

import (
	"errors"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// Changing or deleting one time a repeating event happens ("only this one"): a change becomes an override (a VEVENT
// with RECURRENCE-ID naming the time it replaces), a deletion an EXDATE on the series. Phones read both.

// instanceProp makes a property (RECURRENCE-ID or EXDATE) naming one occurrence, written the way the series' DTSTART
// is (a date, or a time in its time zone), as clients match them literally.
func instanceProp(name string, master *ical.Component, instance string) (*ical.Prop, error) {
	start := master.Props.Get(ical.PropDateTimeStart)
	if start == nil {
		return nil, errors.New("the event has no start")
	}
	p := ical.NewProp(name)
	if start.ValueType() == ical.ValueDate || len(start.Value) == 8 {
		d, err := time.Parse("2006-01-02", instance[:min(10, len(instance))])
		if err != nil {
			return nil, err
		}
		p.SetDate(d)
		return p, nil
	}
	t, err := time.Parse(time.RFC3339, instance)
	if err != nil {
		return nil, errors.New("the occurrence isn't a time")
	}
	if strings.HasSuffix(start.Value, "Z") {
		p.SetDateTime(t.UTC())
	} else if tz := start.Params.Get(ical.PropTimezoneID); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, err
		}
		p.SetDateTime(t.In(loc))
	} else { // floating time
		p.SetValueType(ical.ValueDateTime)
		p.Value = t.Format("20060102T150405")
	}
	return p, nil
}

// sameInstance says whether a RECURRENCE-ID names the occurrence at instance.
func sameInstance(rid *ical.Prop, instance string) bool {
	t, allDay, err := propTime(rid, time.UTC)
	if err != nil {
		return false
	}
	if allDay {
		return strings.HasPrefix(instance, t.Format("2006-01-02"))
	}
	want, err := time.Parse(time.RFC3339, instance)
	return err == nil && want.Equal(t)
}

func masterOf(obj *ical.Calendar) *ical.Component {
	for _, c := range obj.Children {
		if c.Name == ical.CompEvent && c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
	}
	return nil
}

// overrideFor finds the override for one occurrence, making it (a copy of the series) if there's none yet.
func overrideFor(obj *ical.Calendar, instance string) (*ical.Event, error) {
	master := masterOf(obj)
	if master == nil {
		return nil, errors.New("that event can't be changed here")
	}
	for _, c := range obj.Children {
		if rid := c.Props.Get(ical.PropRecurrenceID); c.Name == ical.CompEvent && rid != nil && sameInstance(rid, instance) {
			return &ical.Event{Component: c}, nil
		}
	}
	ov := ical.NewComponent(ical.CompEvent)
	for name, props := range master.Props {
		switch name {
		case ical.PropRecurrenceRule, ical.PropExceptionDates, ical.PropRecurrenceDates:
			continue
		}
		for _, p := range props {
			cp := p
			cp.Params = ical.Params{}
			for k, v := range p.Params {
				cp.Params[k] = append([]string(nil), v...)
			}
			ov.Props[name] = append(ov.Props[name], cp)
		}
	}
	ov.Children = append(ov.Children, master.Children...)
	rid, err := instanceProp(ical.PropRecurrenceID, master, instance)
	if err != nil {
		return nil, err
	}
	ov.Props.Set(rid)
	obj.Children = append(obj.Children, ov)
	return &ical.Event{Component: ov}, nil
}

// skipInstance deletes one occurrence: an EXDATE on the series, and its override (if any) gone.
func skipInstance(obj *ical.Calendar, instance string) error {
	master := masterOf(obj)
	if master == nil {
		return errors.New("that event can't be changed here")
	}
	ex, err := instanceProp(ical.PropExceptionDates, master, instance)
	if err != nil {
		return err
	}
	master.Props.Add(ex)
	kept := obj.Children[:0]
	for _, c := range obj.Children {
		if rid := c.Props.Get(ical.PropRecurrenceID); c.Name == ical.CompEvent && rid != nil && sameInstance(rid, instance) {
			continue
		}
		kept = append(kept, c)
	}
	obj.Children = kept
	return nil
}
