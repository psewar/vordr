package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// pad makes every test event about 100 bytes, so a limit of a few hundred bytes rotates every few lines.
var pad = strings.Repeat("x", 40)

// rotating opens an event log in a fresh folder with rotation and a clock the test moves.
func rotating(t *testing.T, maxBytes int64, keep int) (*Events, *clock, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	clk := &clock{t: time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)}
	e, err := OpenEvents(path, clk.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.SetRotation(maxBytes, keep)
	return e, clk, path
}

type logLine struct {
	Event string `json:"event"`
	Node  string `json:"node"`
	N     int    `json:"n"`
	W     int    `json:"w"`
	Code  string `json:"code"`
	File  string `json:"file"`
}

// onDisk reads every file of the log as Read orders them - events.jsonl.<highest> first, events.jsonl last - and
// returns their names and lines.
func onDisk(t *testing.T, path string) ([]string, []logLine) {
	t.Helper()
	e := &Events{path: path}
	nums, err := e.generations()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := len(nums) - 1; i >= 0; i-- {
		names = append(names, filepath.Base(e.generation(nums[i])))
	}
	if _, err := os.Stat(path); err == nil {
		names = append(names, filepath.Base(path))
	}
	var lines []logLine
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(bytes.NewReader(raw))
		for sc.Scan() {
			var l logLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				t.Fatalf("%s: %q: %v", name, sc.Text(), err)
			}
			lines = append(lines, l)
		}
	}
	return names, lines
}

func ns(lines []logLine) []int {
	var out []int
	for _, l := range lines {
		if l.Event == "e" {
			out = append(out, l.N)
		}
	}
	return out
}

// fileNames: events.jsonl.<full> down to events.jsonl.1, then events.jsonl.
func fileNames(full int) []string {
	var out []string
	for n := full; n >= 1; n-- {
		out = append(out, fmt.Sprintf("events.jsonl.%d", n))
	}
	return append(out, "events.jsonl")
}

func span(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func readAll(t *testing.T, e *Events, f EventFilter) []logLine {
	t.Helper()
	raw, unreadable, err := e.Read(f)
	if err != nil || unreadable != 0 {
		t.Fatalf("read %+v: %v, %d unreadable", f, err, unreadable)
	}
	var out []logLine
	for _, r := range raw {
		var l logLine
		_ = json.Unmarshal(r, &l)
		out = append(out, l)
	}
	return out
}

func logN(e *Events, from, to int) {
	for i := from; i <= to; i++ {
		e.Log("golf", "e", map[string]any{"n": i, "pad": pad})
	}
}

// At the limit the full file becomes events.jsonl.1 and the new one starts with event_log_rotated; no file passes the
// limit, and every line is there once, in order, on disk and for Read.
func TestEventLogRotatesAtTheLimit(t *testing.T) {
	e, _, path := rotating(t, 1000, 5)
	logN(e, 0, 29)
	names, lines := onDisk(t, path)
	if len(names) < 4 || !slices.Equal(names, fileNames(len(names)-1)) {
		t.Fatalf("files %v", names)
	}
	for _, name := range names {
		if info, _ := os.Stat(filepath.Join(filepath.Dir(path), name)); info.Size() > 1000 {
			t.Errorf("%s: %d bytes, limit 1000", name, info.Size())
		}
	}
	if got := ns(lines); !slices.Equal(got, span(0, 29)) {
		t.Fatalf("lines on disk %v", got)
	}
	raw, _ := os.ReadFile(path)
	var head map[string]any
	_ = json.Unmarshal(bytes.SplitN(raw, []byte("\n"), 2)[0], &head)
	if head["event"] != "event_log_rotated" || head["previous"] != "events.jsonl.1" || head["keep"] != 5.0 ||
		head["previous_size"].(float64) <= 0 || head["previous_size"].(float64) > 1000 {
		t.Fatalf("first line of the new file: %v", head)
	}
	if got := ns(readAll(t, e, EventFilter{Limit: MaxEventLimit})); !slices.Equal(got, span(0, 29)) {
		t.Fatalf("read %v", got)
	}
	// Without rotation nothing moves, whatever the size.
	plain, _, plainPath := rotating(t, 0, 5)
	logN(plain, 0, 29)
	if names, _ := onDisk(t, plainPath); !slices.Equal(names, []string{"events.jsonl"}) {
		t.Fatalf("without rotation: %v", names)
	}
}

// keep full files stay; the oldest goes. A smaller keep later drops the files beyond it with the next rotation.
func TestEventLogKeepsItsNumberOfFiles(t *testing.T) {
	e, _, path := rotating(t, 1000, 2)
	logN(e, 0, 59)
	names, lines := onDisk(t, path)
	if !slices.Equal(names, []string{"events.jsonl.2", "events.jsonl.1", "events.jsonl"}) {
		t.Fatalf("files %v", names)
	}
	kept := ns(lines)
	if len(kept) == 0 || kept[len(kept)-1] != 59 || kept[0] == 0 || !slices.Equal(kept, span(kept[0], 59)) {
		t.Fatalf("kept %v: want the newest lines without a gap, the oldest gone", kept)
	}
	if got := ns(readAll(t, e, EventFilter{Limit: MaxEventLimit})); !slices.Equal(got, kept) {
		t.Fatalf("read %v, on disk %v", got, kept)
	}

	e.SetRotation(1000, 1)
	logN(e, 60, 75)
	if names, _ := onDisk(t, path); !slices.Equal(names, []string{"events.jsonl.1", "events.jsonl"}) {
		t.Fatalf("keep 1: files %v", names)
	}
}

// Read goes across the files: newest first, older ones only while they can add to the answer, and the answer is the
// same as filtering every line on disk.
func TestReadAcrossFiles(t *testing.T) {
	e, clk, path := rotating(t, 600, 20)
	for i := 0; i < 60; i++ {
		node := map[bool]string{true: "golf", false: "hotel"}[i%3 != 0]
		e.Log(node, "e", map[string]any{"n": i, "pad": pad})
		clk.add(time.Minute)
	}
	names, _ := onDisk(t, path)
	if len(names) < 8 {
		t.Fatalf("only %v", names)
	}
	start := time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	for _, f := range []EventFilter{
		{Limit: MaxEventLimit},
		{Limit: 1}, {Limit: 3}, {Limit: 7}, {Limit: 25},
		{Node: "hotel", Limit: 4},
		{Node: "golf"},
		{Event: "event_log_rotated", Limit: MaxEventLimit},
		{Since: start.Add(37 * time.Minute)},
		{Since: start.Add(5 * time.Minute), Node: "hotel", Limit: MaxEventLimit},
		{Since: start.Add(90 * time.Minute)},
	} {
		want := filterOnDisk(t, path, f)
		got := readAll(t, e, f)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%+v:\n read %v\n want %v", f, got, want)
		}
	}
}

// filterOnDisk is Read done the slow way: every line of every file, filtered, the newest limit of them.
func filterOnDisk(t *testing.T, path string, f EventFilter) []logLine {
	t.Helper()
	limit := f.Limit
	if limit == 0 {
		limit = DefaultEventLimit
	}
	names, _ := onDisk(t, path)
	var out []logLine
	for _, name := range names {
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			var head struct {
				Time  time.Time `json:"time"`
				Event string    `json:"event"`
				Node  string    `json:"node"`
			}
			var l logLine
			_ = json.Unmarshal(line, &head)
			_ = json.Unmarshal(line, &l)
			if f.Node != "" && head.Node != f.Node || f.Event != "" && head.Event != f.Event ||
				!f.Since.IsZero() && head.Time.Before(f.Since) {
				continue
			}
			out = append(out, l)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Writers and readers at the same time, across many rotations: every line lands exactly once, each writer's lines in
// its order, and a read never sees a line twice or out of order.
func TestEventLogConcurrentWritesAndReads(t *testing.T) {
	e, _, path := rotating(t, 2000, 1000)
	const writers, each = 8, 200
	var wg sync.WaitGroup
	done := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readErr)
		for {
			select {
			case <-done:
				return
			default:
			}
			raw, _, err := e.Read(EventFilter{Limit: MaxEventLimit})
			if err != nil {
				readErr <- err
				return
			}
			last := map[int]int{}
			for _, r := range raw {
				var l logLine
				_ = json.Unmarshal(r, &l)
				if l.Event != "e" {
					continue
				}
				if prev, seen := last[l.W]; seen && l.N <= prev {
					readErr <- fmt.Errorf("writer %d: %d after %d", l.W, l.N, prev)
					return
				}
				last[l.W] = l.N
			}
		}
	}()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for n := 0; n < each; n++ {
				e.Log("", "e", map[string]any{"w": w, "n": n, "pad": pad})
			}
		}(w)
	}
	wg.Wait()
	close(done)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	names, lines := onDisk(t, path)
	if len(names) < 10 {
		t.Fatalf("too few rotations to tell: %v", names)
	}
	got := map[int][]int{}
	for _, l := range lines {
		if l.Event == "e" {
			got[l.W] = append(got[l.W], l.N)
		}
	}
	for w := 0; w < writers; w++ {
		if !slices.Equal(got[w], span(0, each-1)) {
			t.Errorf("writer %d: %d lines, want %d in order", w, len(got[w]), each)
		}
	}
}

// A hub that stops in the middle of a rotation finds the files in order on its next start and loses nothing.
func TestEventLogAfterAStopInTheMiddle(t *testing.T) {
	t.Run("between moving the full file and opening the new one", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		e, _ := OpenEvents(path, time.Now, nil)
		logN(e, 0, 4)
		e.Close()
		if err := os.Rename(path, path+".1"); err != nil {
			t.Fatal(err)
		}
		e2, err := OpenEvents(path, time.Now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer e2.Close()
		e2.SetRotation(1000, 5)
		logN(e2, 5, 9)
		if got := ns(readAll(t, e2, EventFilter{Limit: MaxEventLimit})); !slices.Equal(got, span(0, 9)) {
			t.Fatalf("read %v", got)
		}
		if names, lines := onDisk(t, path); !slices.Equal(names, []string{"events.jsonl.1", "events.jsonl"}) ||
			!slices.Equal(ns(lines), span(0, 9)) {
			t.Fatalf("files %v, lines %v", names, ns(lines))
		}
	})
	t.Run("between moving two full files", func(t *testing.T) {
		// .2 was moved to .3, .1 not yet to .2, the current file not yet to .1.
		path := filepath.Join(t.TempDir(), "events.jsonl")
		for _, part := range []struct {
			file     string
			from, to int
		}{{".3", 0, 4}, {".1", 5, 9}, {"", 10, 14}} {
			e, _ := OpenEvents(path+part.file, time.Now, nil)
			logN(e, part.from, part.to)
			e.Close()
		}
		e, err := OpenEvents(path, time.Now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Close()
		e.SetRotation(300, 3) // the current file is past it already: the next line rotates
		if got := ns(readAll(t, e, EventFilter{Limit: MaxEventLimit})); !slices.Equal(got, span(0, 14)) {
			t.Fatalf("read before the rotation %v", got)
		}
		logN(e, 15, 15)
		names, lines := onDisk(t, path)
		if !slices.Equal(names, []string{"events.jsonl.3", "events.jsonl.2", "events.jsonl.1", "events.jsonl"}) ||
			!slices.Equal(ns(lines), span(0, 15)) {
			t.Fatalf("the gap is filled, nothing dropped: files %v, lines %v", names, ns(lines))
		}
	})
}

// A rotation never overwrites: something at the next place stops it with target_exists, recorded as an event; the hub
// keeps writing to its file, tries again after a minute, and goes on once the place is free.
func TestEventLogRotationNeverOverwrites(t *testing.T) {
	e, clk, path := rotating(t, 500, 5)
	foreign := path + ".2"
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "keep-me"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logN(e, 0, 19) // the first rotation goes to .1; the second would move .1 to .2
	failures := func() []logLine {
		return readAll(t, e, EventFilter{Event: "event_log_rotation_failed", Limit: MaxEventLimit})
	}
	if f := failures(); len(f) != 1 || f[0].Code != RotationTargetExists || f[0].File != "events.jsonl.2" {
		t.Fatalf("failures %+v", f)
	}
	clk.add(30 * time.Second)
	logN(e, 20, 24)
	if f := failures(); len(f) != 1 {
		t.Fatalf("tried again within the minute: %+v", f)
	}
	clk.add(31 * time.Second)
	logN(e, 25, 25)
	if f := failures(); len(f) != 2 {
		t.Fatalf("not tried again after the minute: %+v", f)
	}
	if raw, err := os.ReadFile(filepath.Join(foreign, "keep-me")); err != nil || string(raw) != "x" {
		t.Fatalf("the foreign folder was touched: %q %v", raw, err)
	}
	if _, lines := onDisk(t, path); !slices.Equal(ns(lines), span(0, 25)) {
		t.Fatalf("lines %v", ns(lines))
	}

	os.RemoveAll(foreign)
	clk.add(time.Minute)
	logN(e, 26, 26)
	names, lines := onDisk(t, path)
	if !slices.Equal(names, []string{"events.jsonl.2", "events.jsonl.1", "events.jsonl"}) ||
		!slices.Equal(ns(lines), span(0, 26)) {
		t.Fatalf("after the place is free: files %v, lines %v", names, ns(lines))
	}
}

// When the new file cannot be opened, the lines go to the service log, Read still answers from the full files, and the
// next line opens the file once it can.
func TestEventLogWhenTheNewFileCannotBeOpened(t *testing.T) {
	e, _, path := rotating(t, 500, 5)
	var service bytes.Buffer
	e.echo = log.New(&service, "", 0)
	broken := true
	e.open = func(p string) (*os.File, error) {
		if broken {
			return nil, errors.New("no space left on device")
		}
		return openAppend(p)
	}
	logN(e, 0, 7) // rotates, and the new file does not open
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current file: %v", err)
	}
	read := ns(readAll(t, e, EventFilter{Limit: MaxEventLimit}))
	if len(read) == 0 || read[0] != 0 || !slices.Equal(read, span(0, read[len(read)-1])) || read[len(read)-1] == 7 {
		t.Fatalf("read without a current file: %v", read)
	}
	for i := read[len(read)-1] + 1; i <= 7; i++ {
		if !strings.Contains(service.String(), fmt.Sprintf(`"n":%d,`, i)) {
			t.Errorf("line %d neither in the file nor in the service log", i)
		}
	}
	if strings.Count(service.String(), "events go only to the service log") != 1 {
		t.Errorf("service log:\n%s", service.String())
	}
	broken = false
	logN(e, 8, 9)
	_, lines := onDisk(t, path)
	if got := ns(lines); !slices.Equal(got[len(got)-2:], []int{8, 9}) {
		t.Fatalf("after the file opens again: %v", got)
	}
}

// hub.yaml event_log: off without the section, defaults with an empty one, ranges checked, unknown keys refused; the
// example's section is accepted.
func TestParseConfigEventLog(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	os.WriteFile(cert, []byte("x"), 0o600)
	base := "tls_cert: " + cert + "\ntls_key: " + cert + "\n"
	for _, c := range []struct {
		yaml   string
		want   *EventLogConfig
		issues []string
	}{
		{"", nil, nil},
		{"event_log: {}\n", &EventLogConfig{MaxSizeMB: 32, Keep: 8}, nil},
		{"event_log: {max_size_mb: 64, keep: 3}\n", &EventLogConfig{MaxSizeMB: 64, Keep: 3}, nil},
		{"event_log: {max_size_mb: 1024, keep: 100}\n", &EventLogConfig{MaxSizeMB: 1024, Keep: 100}, nil},
		{"event_log: {max_size_mb: -1, keep: 101}\n", nil,
			[]string{"event_log.max_size_mb: must be between 1 and 1024", "event_log.keep: must be between 1 and 100"}},
		{"event_log: {max_size_mb: 1025}\n", nil, []string{"event_log.max_size_mb: must be between 1 and 1024"}},
		{"event_log: {max_size: 3}\n", nil, []string{"field max_size not found"}},
	} {
		cfg, issues := ParseConfig([]byte(base + c.yaml))
		if len(issues) != len(c.issues) {
			t.Errorf("%q: issues %v, want %v", c.yaml, issues, c.issues)
			continue
		}
		for i, want := range c.issues {
			if !strings.Contains(issues[i], want) {
				t.Errorf("%q: issue %q, want %q", c.yaml, issues[i], want)
			}
		}
		if c.issues == nil && fmt.Sprint(cfg.EventLog) != fmt.Sprint(c.want) {
			t.Errorf("%q: %+v, want %+v", c.yaml, cfg.EventLog, c.want)
		}
	}
	raw, err := os.ReadFile("../../hub.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, issues := ParseConfig(raw)
	for _, i := range issues {
		if strings.HasPrefix(i, "event_log") || strings.HasPrefix(i, "hub.yaml") {
			t.Errorf("example: %s", i)
		}
	}
	if cfg.EventLog == nil || *cfg.EventLog != (EventLogConfig{MaxSizeMB: 32, Keep: 8}) || cfg.EventLog.MaxBytes() != 32<<20 {
		t.Fatalf("example: %+v", cfg.EventLog)
	}
}
