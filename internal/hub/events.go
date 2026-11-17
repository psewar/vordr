package hub

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Events is the event log (events.jsonl): one JSON object per line, only ever appended. With a size limit
// (SetRotation, hub.yaml event_log, ADR-0172) the hub starts a new file once the current one would pass it: the full
// file becomes events.jsonl.1, the older ones move one number up, and what goes past keep is deleted. Reading goes
// across all of them, oldest first.
type Events struct {
	mu   sync.Mutex
	f    *os.File // the current file; nil while it could not be opened again after a rotation
	size int64    // of the current file
	path string
	now  func() time.Time
	echo *log.Logger // also to the service log; may be nil
	// maxSize 0: no rotation (no event_log section).
	maxSize int64
	keep    int
	// retryAt: after a rotation that did not go through, the next attempt waits until then.
	retryAt time.Time
	// quietUntil: while the current file cannot be opened, the service log hears about it once per rotationRetry.
	quietUntil time.Time
	open       func(path string) (*os.File, error) // the current file, for appending; tests replace it
}

// rotationRetry: how long a rotation that did not go through waits before the next attempt; meanwhile the hub keeps
// writing to the file it has, and every line also goes to the service log.
const rotationRetry = time.Minute

// Codes of a rotation that did not go through (event event_log_rotation_failed, field code).
const (
	RotationCheckFailed  = "check_failed"  // the state folder or a file in it could not be looked at
	RotationTargetExists = "target_exists" // something is already where the next file goes; nothing is overwritten
	RotationRemoveFailed = "remove_failed" // the oldest file could not be deleted
	RotationRenameFailed = "rename_failed"
	RotationReopenFailed = "reopen_failed" // the new current file could not be opened
)

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
}

// OpenEvents opens the current file for appending, creating it when it is missing - also after a hub that stopped
// between moving the full file away and opening the new one: everything is in events.jsonl.1 then.
func OpenEvents(path string, now func() time.Time, echo *log.Logger) (*Events, error) {
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Events{f: f, size: info.Size(), path: path, now: now, echo: echo, open: openAppend}, nil
}

// SetRotation sets the size limit of the current file and how many full files are kept; maxBytes 0 switches rotation
// off. Files beyond keep from an earlier, larger setting go with the next rotation.
func (e *Events) SetRotation(maxBytes int64, keep int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.maxSize, e.keep = maxBytes, max(keep, 1)
}

// Log appends one event. A failed write goes to the service log; the event log never stops the hub. Rotation happens
// here, under the same lock as every write, so no line is lost to it or written twice.
func (e *Events) Log(node, event string, fields map[string]any) {
	line := e.record(node, event, fields)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rotateIfDue(len(line))
	e.append(line)
}

// append writes one line to the file and the service log; the caller holds mu.
func (e *Events) append(line []byte) {
	e.write(line)
	if e.echo != nil {
		e.echo.Printf("%s", line[:len(line)-1])
	}
}

func (e *Events) record(node, event string, fields map[string]any) []byte {
	rec := map[string]any{"time": e.now().UTC().Format(time.RFC3339), "event": event}
	if node != "" {
		rec["node"] = node
	}
	for k, v := range fields {
		rec[k] = v
	}
	line, _ := json.Marshal(rec)
	return append(line, '\n')
}

// write appends one line to the current file; the caller holds mu. Without a current file (a rotation could not open
// the new one) it tries to open it again first, for every line: the line is in the service log either way.
func (e *Events) write(line []byte) {
	if e.f == nil {
		if err := e.reopen(); err != nil {
			if e.echo != nil && !e.now().Before(e.quietUntil) {
				e.quietUntil = e.now().Add(rotationRetry)
				e.echo.Printf("event log: %v - events go only to the service log until it opens", err)
			}
			return
		}
	}
	n, err := e.f.Write(line)
	e.size += int64(n)
	if err != nil && e.echo != nil {
		e.echo.Printf("event log: %v", err)
	}
}

// reopen opens the current file again; the caller holds mu.
func (e *Events) reopen() error {
	f, err := e.open(e.path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	e.f, e.size = f, info.Size()
	return nil
}

// rotateIfDue starts a new file when the line would take the current one past the limit; the caller holds mu. The
// outcome is itself an event: event_log_rotated as the first line of the new file, or event_log_rotation_failed with
// its code in the file the hub keeps writing to.
func (e *Events) rotateIfDue(next int) {
	if e.maxSize == 0 || e.f == nil || e.size == 0 || e.size+int64(next) <= e.maxSize || e.now().Before(e.retryAt) {
		return
	}
	full := e.size
	if code, file := e.rotate(); code != "" {
		e.retryAt = e.now().Add(rotationRetry)
		e.append(e.record("", "event_log_rotation_failed", map[string]any{"code": code, "file": filepath.Base(file)}))
		return
	}
	e.append(e.record("", "event_log_rotated", map[string]any{"previous": filepath.Base(e.generation(1)),
		"previous_size": full, "keep": e.keep}))
}

// rotate makes the current file events.jsonl.1; the caller holds mu. Every step checks first that its target is free
// and stops the rotation where it is otherwise, so a file is never overwritten, and a hub that stops between two steps
// leaves the files in their order: a higher number is older, the current file is the newest. A gap in the numbers
// (left by such a stop) is filled by the next rotation instead of costing a file.
func (e *Events) rotate() (code, file string) {
	nums, err := e.generations()
	if err != nil {
		return RotationCheckFailed, filepath.Dir(e.path)
	}
	// Past keep goes anyway; deleting it first saves moving it (after keep was made smaller).
	for i := len(nums) - 1; i >= 0 && nums[i] > e.keep; i-- {
		if err := os.Remove(e.generation(nums[i])); err != nil {
			return RotationRemoveFailed, e.generation(nums[i])
		}
		nums = nums[:i]
	}
	// Room at 1: the files below the first free number move one up, the highest first.
	free := 1
	for slices.Contains(nums, free) {
		free++
	}
	for n := free - 1; n >= 1; n-- {
		if code, file := e.move(e.generation(n), e.generation(n+1)); code != "" {
			return code, file
		}
	}
	if free > e.keep {
		if err := os.Remove(e.generation(free)); err != nil {
			return RotationRemoveFailed, e.generation(free)
		}
	}
	first := e.generation(1)
	if code, file := freeAt(first); code != "" {
		return code, file
	}
	// Closed before it moves: Windows does not rename a file that is open. Nobody else writes, the caller holds mu.
	_ = e.f.Close()
	e.f = nil
	if err := os.Rename(e.path, first); err != nil {
		_ = e.reopen() // still the current file: keep appending to it; write tries again if this fails
		return RotationRenameFailed, e.path
	}
	if err := e.reopen(); err != nil {
		return RotationReopenFailed, e.path
	}
	return "", ""
}

// move renames from to to, after checking that nothing is at to.
func (e *Events) move(from, to string) (code, file string) {
	if code, file := freeAt(to); code != "" {
		return code, file
	}
	if err := os.Rename(from, to); err != nil {
		return RotationRenameFailed, from
	}
	return "", ""
}

// freeAt: "" when nothing is at path, else the rotation code that stops it.
func freeAt(path string) (code, file string) {
	empty, err := absent(path)
	switch {
	case err != nil:
		return RotationCheckFailed, path
	case !empty:
		return RotationTargetExists, path
	}
	return "", ""
}

// absent reports whether nothing is at path; a file, a folder or a link all count as something.
func absent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func (e *Events) generation(n int) string { return e.path + "." + strconv.Itoa(n) }

// generationSuffix: events.jsonl.<n>; other names beside it (events.jsonl.bak) are not the hub's.
var generationSuffix = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)

// generations lists the numbers of the full files that exist, ascending (newest first).
func (e *Events) generations() ([]int, error) {
	entries, err := os.ReadDir(filepath.Dir(e.path))
	if err != nil {
		return nil, err
	}
	var nums []int
	prefix := filepath.Base(e.path) + "."
	for _, de := range entries {
		suffix, ok := strings.CutPrefix(de.Name(), prefix)
		if ok && generationSuffix.MatchString(suffix) && de.Type().IsRegular() {
			n, _ := strconv.Atoi(suffix) // checked above: 1 to 9999
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	return nums, nil
}

func (e *Events) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return nil
	}
	return e.f.Close()
}

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

// Read returns the newest events that match f, oldest first, and how many lines it could not read. It opens the
// current file and the full ones under the log's lock, which a rotation also holds, so it reads one consistent set of
// files; the reading itself runs without the lock, and the hub keeps writing. It reads the newest file first and an
// older one only while that can still add to the answer. A line still being written when Read reaches the end is
// incomplete then and counts as unreadable; nothing else in the files it reads is skipped.
func (e *Events) Read(f EventFilter) ([]json.RawMessage, int, error) {
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultEventLimit
	case limit > MaxEventLimit:
		limit = MaxEventLimit
	}
	files, err := e.openAll()
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	var chunks [][]json.RawMessage // newest file first
	found, unreadable := 0, 0
	for i := len(files) - 1; i >= 0; i-- {
		lines, bad, first, err := scanEvents(files[i], f, limit)
		if err != nil {
			return nil, 0, err
		}
		chunks, found, unreadable = append(chunks, lines), found+len(lines), unreadable+bad
		// Older files hold older events: enough found, or this file already starts before since.
		if found >= limit || !f.Since.IsZero() && !first.IsZero() && first.Before(f.Since) {
			break
		}
	}
	var out []json.RawMessage
	for i := len(chunks) - 1; i >= 0; i-- {
		out = append(out, chunks[i]...)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, unreadable, nil
}

// openAll opens the full files, oldest first, and the current file last, under the lock.
func (e *Events) openAll() ([]*os.File, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	nums, err := e.generations()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(nums)+1)
	for i := len(nums) - 1; i >= 0; i-- {
		paths = append(paths, e.generation(nums[i]))
	}
	current := true
	if e.f == nil { // a rotation could not open the new current file: there may be none yet
		empty, err := absent(e.path)
		if err != nil {
			return nil, err
		}
		current = !empty
	}
	if current {
		paths = append(paths, e.path)
	}
	var files []*os.File
	for _, p := range paths {
		file, err := os.Open(p)
		if err != nil {
			for _, open := range files {
				open.Close()
			}
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// scanEvents reads one file: the newest limit lines that match f, how many lines were no event, and the time of its
// first event.
func scanEvents(file *os.File, f EventFilter, limit int) ([]json.RawMessage, int, time.Time, error) {
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	var out []json.RawMessage
	var first time.Time
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
		if first.IsZero() {
			first = head.Time
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
	return out, unreadable, first, sc.Err()
}
