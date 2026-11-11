package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// fakeHypervisor stands in for vzdump and the guests in /etc/pve.
type fakeHypervisor struct {
	guests  map[string]bool // "ct/302"
	calls   [][]string
	result  runner.Result
	hold    chan struct{} // closed when vzdump may end; nil: at once
	started chan struct{} // gets one value when vzdump starts; nil: not watched
}

func (h *fakeHypervisor) backups() *GuestBackups {
	return &GuestBackups{Storage: "pbs", Timeout: time.Hour,
		Exists: func(kind, id string) bool { return h.guests[kind+"/"+id] },
		Vzdump: func(ctx context.Context, args ...string) runner.Result {
			h.calls = append(h.calls, args)
			if h.started != nil {
				h.started <- struct{}{}
			}
			if h.hold != nil {
				<-h.hold
			}
			return h.result
		}}
}

func backupJob(id, guest string) protocol.Job {
	return protocol.Job{ID: id, Node: "host-a", Verb: protocol.VerbBackup, Guest: guest}
}

func backupClient(t *testing.T, h *fakeHypervisor) *Client {
	t.Helper()
	c := installClient(t, nil, allowAll)
	c.Backups = h.backups()
	return c
}

// One backup of one guest: vzdump to the configured storage, snapshot mode, nothing pruned, the job named in the notes.
func TestGuestBackup(t *testing.T) {
	h := &fakeHypervisor{guests: map[string]bool{"ct/302": true},
		result: runner.Result{Stderr: []byte("INFO: Backup job finished successfully\n")}}
	c := backupClient(t, h)
	if !slices.Contains(c.Verbs(), protocol.VerbBackup) {
		t.Fatalf("backup not announced: %v", c.Verbs())
	}
	var out outbox
	c.backupGuest(backupJob("b1", "ct/302"), out.send)
	fin := out.final()
	if fin.JobID != "b1" || fin.Code != protocol.CodeOK || fin.Summary == nil || fin.Summary.LogFile == "" {
		t.Fatalf("result: %+v", fin)
	}
	want := []string{"302", "--storage", "pbs", "--mode", "snapshot", "--remove", "0", "--lockwait", "30",
		"--notes-template", "vordr: before install, job b1"}
	if len(h.calls) != 1 || !slices.Equal(h.calls[0], want) {
		t.Fatalf("vzdump %v, want %v", h.calls, want)
	}
	if log, err := os.ReadFile(fin.Summary.LogFile); err != nil || !strings.Contains(string(log), "finished successfully") {
		t.Fatalf("run log: %q %v", log, err)
	}

	// The same guest again within the gap: refused, vzdump not started.
	var again outbox
	c.backupGuest(backupJob("b2", "ct/302"), again.send)
	if again.final().Code != protocol.CodeRejectedRateLimited || len(h.calls) != 1 {
		t.Fatalf("second backup: %+v, calls %d", again.final(), len(h.calls))
	}
	// After the gap it goes again.
	c.Now = func() time.Time { return time.Now().Add(guestBackupMinGap + time.Minute) }
	var later outbox
	c.backupGuest(backupJob("b3", "ct/302"), later.send)
	if later.final().Code != protocol.CodeOK || len(h.calls) != 2 {
		t.Fatalf("after the gap: %+v", later.final())
	}
}

// What the agent refuses before vzdump runs, and how a failed vzdump is reported.
func TestGuestBackupRefusals(t *testing.T) {
	h := &fakeHypervisor{guests: map[string]bool{"vm/301": true}}
	c := backupClient(t, h)

	var other outbox
	c.backupGuest(backupJob("b1", "ct/302"), other.send) // not on this host
	if fin := other.final(); fin.Code != protocol.CodeFailedBackup || !strings.Contains(fin.Detail, "ct/302") {
		t.Fatalf("foreign guest: %+v", fin)
	}

	c.checkMu.Lock() // an install or check runs
	var busy outbox
	c.backupGuest(backupJob("b2", "vm/301"), busy.send)
	c.checkMu.Unlock()
	if busy.final().Code != protocol.CodeRejectedBusy {
		t.Fatalf("busy: %+v", busy.final())
	}

	h.result = runner.Result{ExitCode: 255, Stderr: []byte("INFO: starting\nERROR: Backup of VM 301 failed - no space left\n")}
	var failed outbox
	c.backupGuest(backupJob("b3", "vm/301"), failed.send)
	if fin := failed.final(); fin.Code != protocol.CodeFailedBackup || !strings.Contains(fin.Detail, "no space left") {
		t.Fatalf("failed vzdump: %+v", fin)
	}

	h.result = runner.Result{Err: errors.New("exec: vzdump: not found")}
	c.Now = func() time.Time { return time.Now().Add(time.Hour) }
	var gone outbox
	c.backupGuest(backupJob("b4", "vm/301"), gone.send)
	if gone.final().Code != protocol.CodeFailedBackup {
		t.Fatalf("vzdump did not run: %+v", gone.final())
	}

	c.Backups = nil
	if slices.Contains(c.Verbs(), protocol.VerbBackup) {
		t.Fatal("backup announced without guest_backup")
	}
	var none outbox
	c.backupGuest(backupJob("b5", "vm/301"), none.send)
	if none.final().Code != protocol.CodeRejectedUnsupported {
		t.Fatalf("no backups here: %+v", none.final())
	}
}

// The backup holds the agent's lock: an install on the hypervisor waits for it, and a stopping agent waits too.
func TestGuestBackupHoldsTheLock(t *testing.T) {
	h := &fakeHypervisor{guests: map[string]bool{"ct/302": true}, hold: make(chan struct{}),
		started: make(chan struct{}, 1)}
	c := backupClient(t, h)
	done := make(chan struct{})
	var out outbox
	go func() {
		c.backupGuest(backupJob("b1", "ct/302"), out.send)
		close(done)
	}()
	<-h.started
	if c.checkMu.TryLock() {
		t.Fatal("the lock is free during the backup")
	}
	waited := make(chan struct{})
	go func() { c.WaitRuns(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("WaitRuns returned during the backup")
	case <-time.After(20 * time.Millisecond):
	}
	close(h.hold)
	<-done
	<-waited
	if out.final().Code != protocol.CodeOK {
		t.Fatalf("result: %+v", out.final())
	}
}
