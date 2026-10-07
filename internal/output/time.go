package output

import (
	"fmt"
	"time"
)

// now is time.Now, replaceable in tests.
var now = time.Now

// When is an API timestamp for a person: "3 Oct 2026, 10:30 (1 day ago)", in
// local time. Anything unparseable comes back as it was; nil is a dash.
func When(iso *string) string {
	t, ok := parse(iso)
	if !ok {
		return Or(iso)
	}
	return fmt.Sprintf("%s (%s)", t.Local().Format("2 Jan 2006, 15:04"), relative(t))
}

// Ago is a timestamp as a short relative time, for tables: "2 days ago".
func Ago(iso *string) string {
	t, ok := parse(iso)
	if !ok {
		return Or(iso)
	}
	return relative(t)
}

func parse(iso *string) (time.Time, bool) {
	if iso == nil || *iso == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, *iso)
	return t, err == nil
}

func relative(t time.Time) string {
	d := now().Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		s = plural(int(d/time.Minute), "min")
	case d < 24*time.Hour:
		s = plural(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		s = plural(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		s = plural(int(d/(30*24*time.Hour)), "month")
	default:
		s = plural(int(d/(365*24*time.Hour)), "year")
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

func plural(n int, unit string) string {
	if n == 1 || unit == "min" {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
