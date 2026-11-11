package hub

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// Events is the append-only event log (events.jsonl): one JSON object per line.
type Events struct {
	mu   sync.Mutex
	f    *os.File
	path string
	now  func() time.Time
	echo *log.Logger // also to the service log; may be nil
}

func OpenEvents(path string, now func() time.Time, echo *log.Logger) (*Events, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Events{f: f, path: path, now: now, echo: echo}, nil
}

// Log appends one event. A failed write goes to the service log; the event log never stops the hub.
func (e *Events) Log(node, event string, fields map[string]any) {
	rec := map[string]any{"time": e.now().UTC().Format(time.RFC3339), "event": event}
	if node != "" {
		rec["node"] = node
	}
	for k, v := range fields {
		rec[k] = v
	}
	line, _ := json.Marshal(rec)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.f.Write(append(line, '\n')); err != nil && e.echo != nil {
		e.echo.Printf("event log: %v", err)
	}
	if e.echo != nil {
		e.echo.Printf("%s", line)
	}
}

func (e *Events) Close() error { return e.f.Close() }

// EventFilter selects events for "vordr-hub events". Every field is optional.
type EventFilter struct {
	Node  string    `json:"node,omitempty"`
	Event string    `json:"event,omitempty"` // the event's name, exactly
	Since time.Time `json:"since,omitempty"`
	Limit int       `json:"limit,omitempty"` // the newest this many; 0: DefaultEventLimit, at most MaxEventLimit
}

const (
	DefaultEventLimit = 50
	MaxEventLimit     = 5000
	// maxEventLine: a line longer than this is no event the hub wrote (a run's detail is cut far below it).
	maxEventLine = 1 << 20
)

// Read returns the newest events that match f, oldest first, and how many lines it could not read. It does not hold
// the log's lock: the hub keeps writing while an operator reads. A line still being written when Read reaches the end
// is incomplete then and counts as unreadable; nothing else in the log is skipped.
func (e *Events) Read(f EventFilter) ([]json.RawMessage, int, error) {
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultEventLimit
	case limit > MaxEventLimit:
		limit = MaxEventLimit
	}
	file, err := os.Open(e.path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	var out []json.RawMessage
	unreadable := 0
	for sc.Scan() {
		var head struct {
			Time  time.Time `json:"time"`
			Event string    `json:"event"`
			Node  string    `json:"node"`
		}
		line := sc.Bytes()
		if json.Unmarshal(line, &head) != nil {
			unreadable++
			continue
		}
		if f.Node != "" && head.Node != f.Node || f.Event != "" && head.Event != f.Event ||
			!f.Since.IsZero() && head.Time.Before(f.Since) {
			continue
		}
		out = append(out, json.RawMessage(append([]byte{}, line...)))
		if len(out) > limit {
			out = out[1:]
		}
	}
	return out, unreadable, sc.Err()
}
