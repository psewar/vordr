package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/hub"
)

var (
	daysBack = regexp.MustCompile(`^([1-9][0-9]{0,3})d$`)
	dateOnly = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	number   = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)
)

// eventFilter turns the options of "vordr-hub events" into the filter; false: one of them cannot be read. since is a
// duration back from now (90m, 24h, 7d), a date (midnight local time) or a time in RFC 3339.
func eventFilter(node, event, since, limit string, now time.Time) (hub.EventFilter, bool) {
	f := hub.EventFilter{Node: node, Event: event}
	if limit != "" {
		if !number.MatchString(limit) {
			return f, false
		}
		f.Limit, _ = strconv.Atoi(limit) // checked above: 1 to 99999
	}
	switch {
	case since == "":
	case daysBack.MatchString(since):
		n, _ := strconv.Atoi(daysBack.FindStringSubmatch(since)[1]) // checked above
		f.Since = now.AddDate(0, 0, -n)
	case dateOnly.MatchString(since):
		t, err := time.ParseInLocation(time.DateOnly, since, now.Location())
		if err != nil {
			return f, false // the shape of a date, not a real one (2026-02-30)
		}
		f.Since = t
	case strings.Contains(since, "T"):
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return f, false
		}
		f.Since = t
	default:
		d, err := time.ParseDuration(since)
		if err != nil || d <= 0 {
			return f, false
		}
		f.Since = now.Add(-d)
	}
	return f, true
}

// printEvents writes one line per event: local time, event, node, then the other fields sorted by name.
func printEvents(w io.Writer, lines []json.RawMessage, unreadable int, loc *time.Location) {
	if len(lines) == 0 {
		fmt.Fprintln(w, "no events")
	}
	for _, raw := range lines {
		var rec map[string]any
		if json.Unmarshal(raw, &rec) != nil {
			continue // the hub sent only lines it could read
		}
		at, _ := rec["time"].(string)
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			at = t.In(loc).Format(time.DateTime)
		}
		event, _ := rec["event"].(string)
		node, _ := rec["node"].(string)
		if node == "" {
			node = "-"
		}
		var keys []string
		for k := range rec {
			if k != "time" && k != "event" && k != "node" {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		parts := []string{at, event, node}
		for _, k := range keys {
			parts = append(parts, k+"="+fieldText(rec[k]))
		}
		fmt.Fprintln(w, strings.Join(parts, "  "))
	}
	if unreadable > 0 {
		fmt.Fprintf(w, "(%d line(s) of the log could not be read: one being written right then, or damaged)\n", unreadable)
	}
}

// fieldText: a string as it is, quoted when it has spaces; anything else as JSON.
func fieldText(v any) string {
	if s, ok := v.(string); ok {
		if strings.ContainsAny(s, " \t\n\"") {
			return strconv.Quote(s)
		}
		return s
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}
