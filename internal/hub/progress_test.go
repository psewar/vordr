package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// What an install run reports while it runs reaches the host's view; a broken tunnel does not end the run, and its
// result settles it even when nobody waits for it any more.
func TestRunProgressAndLateResult(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	if err := f.srv.Reg.SetRun("h", Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all",
		Phase: protocol.PhaseQueued, Started: f.now}); err != nil {
		t.Fatal(err)
	}
	sess := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	pct := func(n int) *int { return &n }

	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Phase: protocol.PhaseInstalling, Progress: pct(50), Item: "libc6"})
	run := f.view(t, "h", f.now).LastRun
	if run.State != "installing" || run.Progress == nil || *run.Progress != 50 || run.Item != "libc6" {
		t.Fatalf("while installing: %+v", run)
	}
	f.srv.deliver(sess, wire.JobStatus{JobID: "other", Phase: protocol.PhaseVerifying, Progress: pct(99)})
	if run := f.view(t, "h", f.now).LastRun; run.State != "installing" {
		t.Fatalf("a status of another job changed the run: %+v", run)
	}

	// The tunnel broke (WaitJob counted the run as interrupted); the agent reconnects and reports on.
	e, _ := f.srv.Reg.Get("h")
	broken := *e.LastRun
	fin := f.now.Add(time.Minute)
	broken.Code, broken.Phase, broken.Finished = protocol.CodeInterrupted, "", &fin
	if err := f.srv.Reg.SetRun("h", broken); err != nil {
		t.Fatal(err)
	}
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Phase: protocol.PhaseVerifying, Progress: pct(96)})
	if run := f.view(t, "h", f.now).LastRun; run.State != "verifying" || *run.Progress != 96 || run.Finished != nil {
		t.Fatalf("reporting on after a reconnect: %+v", run)
	}
	sum := &wire.RunSummary{PackageCount: 68, Packages: []string{"libc6"}, Seconds: 192, LogFile: "/var/lib/vordr/runs/j1.log"}
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Code: protocol.CodeOKRebootRequired, Summary: sum})
	run = f.view(t, "h", f.now).LastRun
	if run.State != "ok_reboot_required" || run.Progress != nil || run.Item != "" || run.Finished == nil ||
		run.Summary == nil || run.Summary.PackageCount != 68 || run.Summary.LogFile != sum.LogFile {
		t.Fatalf("late result: %+v", run)
	}
	// A settled run stays settled.
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Phase: protocol.PhaseInstalling, Progress: pct(10)})
	if run := f.view(t, "h", f.now).LastRun; run.State != "ok_reboot_required" {
		t.Fatalf("progress after the result: %+v", run)
	}
	if waitTimeout(protocol.VerbInstall) <= 24*time.Hour || waitTimeout(protocol.VerbCheck) != checkTimeout {
		t.Fatal("wait timeouts")
	}
}
