package mail

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// Calendar invitations (RFC 5545): the part of a message that says what
// the meeting is, and the reply the organizer expects. Parsing is
// deliberately small: a VEVENT's fields, its organizer and attendees, and
// whether it updates or cancels the series. No lookup and no network.

// CalendarAttendee is one invited person and how they answered.
type CalendarAttendee struct {
	Address
	// Status is PARTSTAT: NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED.
	Status string
	// Organizer marks the event's organizer.
	Organizer bool
}

// CalendarInvite is a message's meeting invitation.
type CalendarInvite struct {
	Method    string
	UID       string
	Sequence  int
	Summary   string
	Start     time.Time
	End       time.Time
	AllDay    bool
	Location  string
	Organizer Address
	Attendees []CalendarAttendee
	Canceled  bool
	// Floating says the times carry no zone: RFC 5545's floating time,
	// which is the reader's own wall clock wherever they are. All-day
	// events are not floating: theirs is a date, not a time.
	Floating bool
	// StartRaw and EndRaw are the original property values, so a reply
	// names the same instant and zone.
	StartRaw string
	EndRaw   string
	Raw      string
}

// ParseCalendar reads the first VEVENT of an iCalendar stream.
func ParseCalendar(raw string) (*CalendarInvite, error) {
	lines := unfoldCalendar(raw)
	method := ""
	inEvent := false
	event := &CalendarInvite{}
	for _, line := range lines {
		upper := strings.ToUpper(line)
		switch upper {
		case "BEGIN:VEVENT":
			inEvent = true
			continue
		case "END:VEVENT":
			if inEvent {
				event.Raw = raw
				event.Method = method
				if event.Canceled || strings.EqualFold(method, "CANCEL") {
					event.Canceled = true
				}
				if event.UID == "" && event.Summary == "" {
					return nil, errors.New("mail: the invitation has no event")
				}
				return event, nil
			}
		case "BEGIN:VCALENDAR", "END:VCALENDAR":
			continue
		}
		key, params, value := calendarProperty(line)
		if key == "" {
			continue
		}
		switch key {
		case "METHOD":
			method = strings.ToUpper(value)
		case "UID":
			if inEvent {
				event.UID = value
			}
		case "SEQUENCE":
			if inEvent {
				_, _ = fmt.Sscanf(value, "%d", &event.Sequence)
			}
		case "SUMMARY":
			if inEvent {
				event.Summary = calendarText(value)
			}
		case "LOCATION":
			if inEvent {
				event.Location = calendarText(value)
			}
		case "STATUS":
			if inEvent && strings.EqualFold(value, "CANCELLED") {
				event.Canceled = true
			}
		case "DTSTART":
			if inEvent {
				event.StartRaw = line
				event.Start, event.AllDay = calendarTime(value, params)
				event.Floating = calendarFloating(value, params)
			}
		case "DTEND":
			if inEvent {
				event.EndRaw = line
				event.End, _ = calendarTime(value, params)
			}
		case "ORGANIZER":
			if inEvent {
				event.Organizer = calendarAddress(value, params).Address
			}
		case "ATTENDEE":
			if inEvent {
				addr := calendarAddress(value, params)
				addr.Status = strings.ToUpper(params["PARTSTAT"])
				event.Attendees = append(event.Attendees, addr)
			}
		}
	}
	return nil, errors.New("mail: the invitation has no event")
}

// unfoldCalendar splits continuation lines, which start with a space or a
// tab, back into the properties they belong to.
func unfoldCalendar(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += line[1:]
			continue
		}
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// calendarProperty splits "NAME;PARAM=value:content".
func calendarProperty(line string) (key string, params map[string]string, value string) {
	inQuote := false
	colon := -1
	for i, r := range line {
		if r == '"' {
			inQuote = !inQuote
		}
		if r == ':' && !inQuote {
			colon = i
			break
		}
	}
	if colon < 0 {
		return "", nil, ""
	}
	head, value := line[:colon], line[colon+1:]
	parts := splitCalendarParams(head)
	key = strings.ToUpper(parts[0])
	params = map[string]string{}
	for _, part := range parts[1:] {
		if k, v, ok := strings.Cut(part, "="); ok {
			params[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
	}
	return key, params, value
}

func splitCalendarParams(head string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for _, r := range head {
		if r == '"' {
			inQuote = !inQuote
		}
		if r == ';' && !inQuote {
			out = append(out, b.String())
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	out = append(out, b.String())
	return out
}

// calendarText unescapes the text values of an event.
func calendarText(value string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(value)
}

// calendarAddress parses a mailto: property with an optional CN.
func calendarAddress(value string, params map[string]string) CalendarAttendee {
	email := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(email), "mailto:") {
		email = email[len("mailto:"):]
	}
	addr := CalendarAttendee{Address: Address{Email: email}}
	addr.Name = params["CN"]
	if addr.Name == "" {
		if parsed, err := mail.ParseAddress(email); err == nil {
			addr.Name, addr.Email = parsed.Name, parsed.Address
		}
	}
	return addr
}

// calendarTime reads a date or date-time, in UTC when it ends in Z and in
// its TZID otherwise.
func calendarTime(value string, params map[string]string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(params["VALUE"], "DATE") || len(value) == 8 {
		t, err := time.Parse("20060102", value)
		return t, err == nil
	}
	layout := "20060102T150405"
	if strings.HasSuffix(value, "Z") {
		t, _ := time.Parse(layout+"Z", value)
		return t, false
	}
	loc := time.UTC
	if tz := params["TZID"]; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	t, _ := time.ParseInLocation(layout, value, loc)
	return t, false
}

// calendarFloating reports a date-time with neither a zone nor a "Z":
// RFC 5545's floating time.
func calendarFloating(value string, params map[string]string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(params["VALUE"], "DATE") || len(value) == 8 || strings.HasSuffix(value, "Z") {
		return false
	}
	return strings.TrimSpace(params["TZID"]) == ""
}

// CalendarReply builds the iMIP reply an organizer can read: a
// METHOD:REPLY calendar with the user's PARTSTAT.
func CalendarReply(invite *CalendarInvite, from Address, status string) string {
	if invite == nil {
		return ""
	}
	status = strings.ToUpper(status)
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Shu//EN\r\nMETHOD:REPLY\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	writeCalendar := func(name, value string) {
		if value != "" {
			b.WriteString(name + ":" + value + "\r\n")
		}
	}
	writeCalendar("UID", invite.UID)
	writeCalendar("SEQUENCE", fmt.Sprint(invite.Sequence))
	writeCalendar("SUMMARY", calendarEscape(invite.Summary))
	if invite.StartRaw != "" {
		b.WriteString(invite.StartRaw + "\r\n")
	}
	if invite.EndRaw != "" {
		b.WriteString(invite.EndRaw + "\r\n")
	}
	if invite.Organizer.Email != "" {
		b.WriteString("ORGANIZER:mailto:" + invite.Organizer.Email + "\r\n")
	}
	b.WriteString("ATTENDEE;PARTSTAT=" + status + ":mailto:" + from.Email + "\r\n")
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

func calendarEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`)
	return r.Replace(s)
}

// CalendarResponseText is the human part of an invitation reply.
func CalendarResponseText(status string) string {
	switch strings.ToUpper(status) {
	case "ACCEPTED":
		return "I accepted the invitation."
	case "TENTATIVE":
		return "I may be able to attend."
	case "DECLINED":
		return "I cannot attend."
	}
	return ""
}
