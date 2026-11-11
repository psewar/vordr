package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventFilterOptions(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2023, 11, 15, 3, 0, 0, 0, newYork)
	for _, c := range []struct {
		since, limit string
		want         time.Time
		wantLimit    int
		ok           bool
	}{
		{"", "", time.Time{}, 0, true},
		{"90m", "", now.Add(-90 * time.Minute), 0, true},
		{"24h", "200", now.Add(-24 * time.Hour), 200, true},
		{"7d", "", now.AddDate(0, 0, -7), 0, true},
		{"2023-11-14", "", time.Date(2023, 11, 14, 0, 0, 0, 0, newYork), 0, true},
		{"2023-11-14T22:13:20Z", "", time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC), 0, true},
		{"2023-02-30", "", time.Time{}, 0, false},
		{"-5m", "", time.Time{}, 0, false},
		{"0d", "", time.Time{}, 0, false},
		{"yesterday", "", time.Time{}, 0, false},
		{"", "0", time.Time{}, 0, false},
		{"", "-3", time.Time{}, 0, false},
		{"", "ten", time.Time{}, 0, false},
	} {
		f, ok := eventFilter("golf", "job_sent", c.since, c.limit, now)
		if ok != c.ok {
			t.Errorf("since %q limit %q: ok %v, want %v", c.since, c.limit, ok, c.ok)
			continue
		}
		if ok && (!f.Since.Equal(c.want) || f.Limit != c.wantLimit || f.Node != "golf" || f.Event != "job_sent") {
			t.Errorf("since %q limit %q: %+v", c.since, c.limit, f)
		}
	}
}

func TestPrintEvents(t *testing.T) {
	lines := []json.RawMessage{
		json.RawMessage(`{"time":"2023-11-14T22:13:20Z","event":"job_sent","node":"golf","job_id":"j1","scope":"all","by":"ha:bravo"}`),
		json.RawMessage(`{"time":"2023-11-14T22:14:00Z","event":"hub_started","paths":["/v1/tunnel","/v1/ha"],"detail":"two words"}`),
	}
	var b bytes.Buffer
	printEvents(&b, lines, 1, time.UTC)
	got := strings.Split(strings.TrimSpace(b.String()), "\n")
	want := []string{
		"2023-11-14 22:13:20  job_sent  golf  by=ha:bravo  job_id=j1  scope=all",
		`2023-11-14 22:14:00  hub_started  -  detail="two words"  paths=["/v1/tunnel","/v1/ha"]`,
		"(1 line(s) of the log could not be read: one being written right then, or damaged)",
	}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: %q, want %q", i, got[i], want[i])
		}
	}
	b.Reset()
	printEvents(&b, nil, 0, time.UTC)
	if b.String() != "no events\n" {
		t.Errorf("empty: %q", b.String())
	}
}
