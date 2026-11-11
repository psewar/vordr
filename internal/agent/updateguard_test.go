package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type guardFixture struct {
	state, exe string
	restarts   int
	restartErr error
}

// newGuardFixture: an update to job u1 pending since the swap, the new agent in place and the previous one kept.
func newGuardFixture(t *testing.T, markerJob string, oldKept bool) *guardFixture {
	t.Helper()
	dir := t.TempDir()
	f := &guardFixture{state: t.TempDir(), exe: filepath.Join(dir, "vordr-agent")}
	if err := os.WriteFile(f.exe, []byte("new agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if oldKept {
		if err := os.WriteFile(f.exe+".old", []byte("old agent"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := updateMarker{JobID: markerJob, From: "0.6.0", To: "0.7.0", At: time.Now().UTC()}
	if err := writeJSON(markerFile(f.state), m); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *guardFixture) guard(window time.Duration) Guard {
	g := NewGuard(f.state, f.exe, "u1", func(context.Context) error { f.restarts++; return f.restartErr },
		log.New(io.Discard, "", 0))
	g.Window, g.Poll = window, time.Millisecond
	return g
}

func (f *guardFixture) marker(t *testing.T) updateMarker {
	t.Helper()
	var m updateMarker
	raw, err := os.ReadFile(markerFile(f.state))
	if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// readFile: the content, or the error in angle brackets (the self-update tests' read, on every platform).
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// The new agent hangs: no confirmation, no rollback of its own, no failed unit. After the window the guard puts the
// previous binary back, says why in the marker and has the service manager start the agent again.
func TestGuardRollsBackAHungUpdate(t *testing.T) {
	f := newGuardFixture(t, "u1", true)
	if out := f.guard(0).Watch(context.Background()); out != GuardRolledBack {
		t.Fatalf("outcome %s", out)
	}
	if readFile(t, f.exe) != "old agent" || f.restarts != 1 {
		t.Fatalf("exe %q, restarts %d", readFile(t, f.exe), f.restarts)
	}
	if m := f.marker(t); m.JobID != "u1" || !strings.Contains(m.RolledBack, "update guard put 0.6.0 back") {
		t.Fatalf("marker %+v", m)
	}
}

// Before the window ends the guard only watches: when the new agent confirms (marker gone), it ends and touches nothing.
func TestGuardEndsWhenTheUpdateSettles(t *testing.T) {
	f := newGuardFixture(t, "u1", true)
	done := make(chan GuardOutcome)
	go func() { done <- f.guard(time.Hour).Watch(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	select {
	case out := <-done:
		t.Fatalf("ended while the update was pending: %s", out)
	default:
	}
	if err := os.Remove(markerFile(f.state)); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if out != GuardSettled {
			t.Fatalf("outcome %s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guard still watching after the marker went")
	}
	if readFile(t, f.exe) != "new agent" || readFile(t, f.exe+".old") != "old agent" || f.restarts != 0 {
		t.Fatalf("touched: exe %q, old %q, restarts %d", readFile(t, f.exe), readFile(t, f.exe+".old"), f.restarts)
	}
}

// The agent confirms by removing the marker, whenever the guard looks at it. On Windows a look that kept the file open
// without FILE_SHARE_DELETE would make the removal fail, and the guard would later roll back a confirmed agent.
// The guard here looks without pause; the deadline read uses readShared for the same reason.
func TestGuardDoesNotBlockTheConfirmation(t *testing.T) {
	for i := range 200 {
		f := newGuardFixture(t, "u1", true)
		g := f.guard(time.Hour)
		g.Poll = 0
		done := make(chan GuardOutcome)
		go func() { done <- g.Watch(context.Background()) }()
		if err := os.Remove(markerFile(f.state)); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
		if out := <-done; out != GuardSettled {
			t.Fatalf("outcome %d: %s", i, out)
		}
	}
	f := newGuardFixture(t, "u1", true)
	done := make(chan []byte)
	go func() { // the deadline read, many times over
		var raw []byte
		for range 2000 {
			raw, _ = readShared(markerFile(f.state))
		}
		done <- raw
	}()
	time.Sleep(time.Millisecond)
	if err := os.Remove(markerFile(f.state)); err != nil {
		t.Fatalf("remove during readShared: %v", err)
	}
	<-done
}

// What the guard leaves alone, even after the window.
func TestGuardLeavesWhatIsNotItsToDo(t *testing.T) {
	cases := map[string]struct {
		markerJob string
		oldKept   bool
		want      GuardOutcome
		exe       string
	}{
		// A later update; this guard's own was settled long ago.
		"another update": {"u2", true, GuardOtherJob, "new agent"},
		// The new agent's own rollback, rollback.sh or rollback.cmd was faster; the agent that is back reports.
		"already put back": {"u1", false, GuardAlreadyBack, "new agent"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newGuardFixture(t, c.markerJob, c.oldKept)
			if out := f.guard(0).Watch(context.Background()); out != c.want || readFile(t, f.exe) != c.exe ||
				f.restarts != 0 {
				t.Fatalf("outcome %s, exe %q, restarts %d", out, readFile(t, f.exe), f.restarts)
			}
		})
	}
	f := newGuardFixture(t, "u1", true)
	if err := os.WriteFile(markerFile(f.state), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := f.guard(0).Watch(context.Background()); out != GuardUnreadable || readFile(t, f.exe) != "new agent" {
		t.Fatalf("unreadable marker: %s, exe %q", out, readFile(t, f.exe))
	}
}

// The Windows service stops cleanly for its restart and asks the guard to start it again (ADR-0103): the guard starts
// it once per request and goes on watching the update; a start that fails leaves the update to the end of the window.
func TestGuardAnswersAPlannedRestart(t *testing.T) {
	f := newGuardFixture(t, "u1", true)
	requests := make(chan struct{}, 1)
	starts := make(chan struct{}, 4)
	g := f.guard(time.Hour)
	g.Requests = requests
	g.Start = func(context.Context) error { starts <- struct{}{}; return nil }
	done := make(chan GuardOutcome)
	go func() { done <- g.Watch(context.Background()) }()
	requests <- struct{}{}
	select {
	case <-starts:
	case <-time.After(5 * time.Second):
		t.Fatal("no start after the request")
	}
	select {
	case out := <-done:
		t.Fatalf("ended after the start, the update is still pending: %s", out)
	case <-starts:
		t.Fatal("started twice for one request")
	case <-time.After(20 * time.Millisecond):
	}
	if err := os.Remove(markerFile(f.state)); err != nil { // the new agent confirms
		t.Fatal(err)
	}
	if out := <-done; out != GuardSettled || f.restarts != 0 || readFile(t, f.exe) != "new agent" {
		t.Fatalf("outcome %s, restarts %d, exe %q", out, f.restarts, readFile(t, f.exe))
	}

	// The start fails: the guard keeps the update pending and puts the previous binary back at the end.
	f = newGuardFixture(t, "u1", true)
	g = f.guard(30 * time.Millisecond)
	g.Requests = requests
	failed := 0
	g.Start = func(context.Context) error { failed++; return errors.New("service not stopped") }
	requests <- struct{}{}
	if out := g.Watch(context.Background()); out != GuardRolledBack || failed != 1 || readFile(t, f.exe) != "old agent" {
		t.Fatalf("after a failed start: %s, starts %d, exe %q", out, failed, readFile(t, f.exe))
	}
}

// A restart that fails is reported as such; the previous binary is back in place all the same.
func TestGuardReportsAFailedRestart(t *testing.T) {
	f := newGuardFixture(t, "u1", true)
	f.restartErr = errors.New("systemctl: connection refused")
	if out := f.guard(0).Watch(context.Background()); out != GuardFailed || readFile(t, f.exe) != "old agent" {
		t.Fatalf("outcome %s, exe %q", out, readFile(t, f.exe))
	}
}

func TestGuardStopsWithItsContext(t *testing.T) {
	f := newGuardFixture(t, "u1", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := f.guard(time.Hour).Watch(ctx); out != GuardStopped || readFile(t, f.exe) != "new agent" {
		t.Fatalf("outcome %s, exe %q", out, readFile(t, f.exe))
	}
}
