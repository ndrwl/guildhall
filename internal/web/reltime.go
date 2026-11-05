package web

import (
	"strconv"
	"time"
)

// timeView is one time on the task list, a <time> element: its instant as RFC 3339 (datetime), the exact time in the
// server's zone (title, shown on hover or a long press) and its text, relative to when the page was read.
type timeView struct {
	ISO, Title, Text string
}

// exactTimeLayout is a time's exact form on the task list, such as "Thu 5 Nov 2026, 14:52:10 GMT".
const exactTimeLayout = "Mon 2 Jan 2006, 15:04:05 MST"

// relTime is t relative to now as the task list says it: "now" under a minute, and for a time in the future, which
// a clock skew can give; then "6 min ago", "2 h ago", "1 day ago" or "3 days ago" up to 30 days, and past that the
// date in loc, "4 Oct", with its year when it is not now's.
func relTime(t, now time.Time, loc *time.Location) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	}
	if days := int(d / (24 * time.Hour)); days == 1 {
		return "1 day ago"
	} else if days <= 30 {
		return strconv.Itoa(days) + " days ago"
	}
	if t.In(loc).Year() != now.In(loc).Year() {
		return t.In(loc).Format("2 Jan 2006")
	}
	return t.In(loc).Format("2 Jan")
}

// timeAt is t as one <time> of the task list, read at now.
func (s *Server) timeAt(t, now time.Time) timeView {
	return timeView{ISO: t.UTC().Format(time.RFC3339), Title: t.In(s.o.Location).Format(exactTimeLayout), Text: relTime(t, now, s.o.Location)}
}
