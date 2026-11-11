package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// "vordr-hub events" reads the log while the hub writes it: filters by host, event and time, keeps the newest, and
// counts a line it cannot read (the one being written) instead of failing.
func TestReadEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	at := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	e, err := OpenEvents(path, func() time.Time { return at }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for i, ev := range []struct{ node, event string }{
		{"golf", "job_sent"}, {"hotel", "job_sent"}, {"golf", "job_done"}, {"", "hub_started"}, {"golf", "job_sent"},
	} {
		at = time.Date(2026, 11, 4, 20, i, 0, 0, time.UTC)
		e.Log(ev.node, ev.event, map[string]any{"n": i})
	}
	// A line being written right now: incomplete.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"time":"2026-11-04T20:09:00Z","event":"job_s`)
	f.Close()

	ns := func(lines []json.RawMessage) []int {
		var out []int
		for _, l := range lines {
			var r struct{ N int }
			_ = json.Unmarshal(l, &r)
			out = append(out, r.N)
		}
		return out
	}
	for _, c := range []struct {
		name string
		f    EventFilter
		want []int
	}{
		{"all", EventFilter{}, []int{0, 1, 2, 3, 4}},
		{"one host", EventFilter{Node: "golf"}, []int{0, 2, 4}},
		{"one event", EventFilter{Event: "job_sent"}, []int{0, 1, 4}},
		{"both", EventFilter{Node: "golf", Event: "job_sent"}, []int{0, 4}},
		{"since", EventFilter{Since: time.Date(2026, 11, 4, 20, 2, 0, 0, time.UTC)}, []int{2, 3, 4}},
		{"newest two", EventFilter{Limit: 2}, []int{3, 4}},
		{"too many asked", EventFilter{Limit: MaxEventLimit + 1}, []int{0, 1, 2, 3, 4}},
		{"none", EventFilter{Node: "india"}, nil},
	} {
		lines, unreadable, err := e.Read(c.f)
		if err != nil || unreadable != 1 {
			t.Fatalf("%s: err %v, unreadable %d", c.name, err, unreadable)
		}
		if got := ns(lines); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	// Through the admin socket's handler, as the command line asks.
	srv := &Server{Events: e}
	resp := srv.Admin(AdminRequest{Cmd: "events", Events: &EventFilter{Node: "hotel"}})
	if resp.Code != AdminOK || len(resp.Events) != 1 || resp.Unreadable != 1 {
		t.Fatalf("admin events: %+v", resp)
	}
	if resp := srv.Admin(AdminRequest{Cmd: "events"}); resp.Code != AdminOK || len(resp.Events) != 5 {
		t.Fatalf("admin events without a filter: %d lines, %s", len(resp.Events), resp.Code)
	}
	os.Remove(path)
	if resp := srv.Admin(AdminRequest{Cmd: "events"}); resp.Code != AdminEventsUnreadable {
		t.Fatalf("log gone: %s", resp.Code)
	}
}
