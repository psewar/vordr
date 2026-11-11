package hub

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// A failed install of the system packages stays the host's problem after later jobs of other kinds (ADR-0124): a
// check, an image, package or agent update (also a failed one), a refusal, a run still going, and a narrower install
// that goes through leave it, and it survives a restart of the hub. An install of the same or a wider scope that goes through clears it.
// The reason is named once, also while the failed run is the last one.
func TestFailedInstallStays(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReport("h", aptReport(f.now, 2, 1, nil))
	n := 0
	run := func(verb protocol.Verb, scope string, code protocol.Code) {
		t.Helper()
		n++
		r := Run{JobID: fmt.Sprintf("j%d", n), Verb: verb, Scope: scope, Code: code, Started: f.now}
		if code != "" {
			fin := f.now
			r.Finished = &fin
		}
		if err := f.srv.Reg.SetRun("h", r); err != nil {
			t.Fatal(err)
		}
	}
	reasons := func(code protocol.Code) int {
		t.Helper()
		v := f.view(t, "h", f.now)
		if v.Problem == nil {
			return 0
		}
		k := 0
		for _, r := range v.Problem.Reasons {
			if r == string(code) {
				k++
			}
		}
		return k
	}

	run(protocol.VerbInstall, "all", protocol.CodeFailedHealth)
	if k := reasons(protocol.CodeFailedHealth); k != 1 {
		t.Fatalf("the failed install as the last run: named %d times", k)
	}
	for _, step := range []struct {
		name  string
		verb  protocol.Verb
		scope string
		code  protocol.Code
	}{
		{"check", protocol.VerbCheck, "", protocol.CodeOK},
		{"image update", protocol.VerbInstall, "images", protocol.CodeOK},
		{"failed image update", protocol.VerbInstall, "images", protocol.CodeFailedHealth}, // not a system install: j1 stays kept
		{"package update", protocol.VerbInstall, "pkg:services", protocol.CodeOK},
		{"agent update", protocol.VerbAgentUpdate, "", protocol.CodeOK},
		{"refusal", protocol.VerbInstall, "all", protocol.CodeRejectedOutsideWindow},
		{"run still going", protocol.VerbInstall, "all", ""},
		{"narrower install that went through", protocol.VerbInstall, "security", protocol.CodeOK},
	} {
		run(step.verb, step.scope, step.code)
		if k := reasons(protocol.CodeFailedHealth); k != 1 {
			t.Fatalf("after a %s: failed_health named %d times", step.name, k)
		}
	}
	reg, err := OpenRegistry(f.srv.Reg.path)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := reg.Get("h"); e.FailedInstall == nil || e.FailedInstall.JobID != "j1" ||
		e.FailedInstall.Code != protocol.CodeFailedHealth {
		t.Fatalf("after a restart: %+v", e.FailedInstall)
	}
	run(protocol.VerbInstall, "all", protocol.CodeOKRebootRequired)
	if v := f.view(t, "h", f.now); v.Problem != nil {
		t.Fatalf("an install of all went through, still a problem: %+v", v.Problem)
	}

	// A failed security install: a security install that goes through clears it.
	run(protocol.VerbInstall, "security", protocol.CodePrecheckRepoUnreachable)
	run(protocol.VerbCheck, "", protocol.CodeOK)
	if k := reasons(protocol.CodePrecheckRepoUnreachable); k != 1 {
		t.Fatalf("the failed security install after a check: named %d times", k)
	}
	run(protocol.VerbInstall, "security", protocol.CodeOK)
	if e, _ := f.srv.Reg.Get("h"); e.FailedInstall != nil {
		t.Fatalf("a security install went through, still kept: %+v", e.FailedInstall)
	}
}

// A canary's failed install holds the later rings also after a later check on the canary, until an install of the
// canary goes through again (ADR-0124).
func TestAutoInstallHeldByAFailedCanaryInstall(t *testing.T) {
	f := newAutoFixture(t)
	f.now = f.now.Add(24 * time.Hour) // the updates have been pending delay_hours
	for _, n := range []string{"canary", "other"} {
		_ = f.srv.Reg.SetReport(n, aptReport(f.now, 5, 2, nil))
	}
	fin := f.now
	set := func(id string, verb protocol.Verb, scope string, code protocol.Code) {
		t.Helper()
		if err := f.srv.Reg.SetRun("canary", Run{JobID: id, Verb: verb, Scope: scope, Code: code, Started: f.now,
			Finished: &fin}); err != nil {
			t.Fatal(err)
		}
	}
	set("j1", protocol.VerbInstall, "security", protocol.CodeFailedHealth)
	set("j2", protocol.VerbCheck, "", protocol.CodeOK)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("other after a check on the failed canary: %q", r)
	}
	set("j3", protocol.VerbInstall, "security", protocol.CodeOK)
	if _, r := f.due(t, "other"); r != autoReady {
		t.Fatalf("other once the canary's install went through: %q", r)
	}
}

// The view names a kept failure by its code among the reasons, and shows the run itself beside the last run, with
// its detail and summary, as long as it is kept; the last run is the latest job.
func TestFailedInstallReasonIsACode(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, nil))
	fin := f.now
	_ = f.srv.Reg.SetRun("h", Run{JobID: "a", Verb: protocol.VerbInstall, Scope: "all", Code: protocol.CodeTimeout,
		Detail: "apt-get ran past install.timeout_min", Started: f.now, Finished: &fin, By: "auto",
		Summary: &wire.RunSummary{PackageCount: 3, LogFile: "/var/lib/vordr/runs/a.log"}})
	_ = f.srv.Reg.SetRun("h", Run{JobID: "b", Verb: protocol.VerbCheck, Code: protocol.CodeOK, Started: f.now,
		Finished: &fin})
	v := f.view(t, "h", f.now)
	if v.Problem == nil || !slices.Equal(v.Problem.Reasons, []string{string(protocol.CodeTimeout)}) ||
		v.LastRun == nil || v.LastRun.JobID != "b" {
		t.Fatalf("problem %+v, last run %+v", v.Problem, v.LastRun)
	}
	fi := v.FailedInstall
	if fi == nil || fi.JobID != "a" || fi.State != string(protocol.CodeTimeout) || fi.Scope != "all" || fi.By != "auto" ||
		fi.LogExcerpt != "apt-get ran past install.timeout_min" || fi.Finished == nil || fi.Summary == nil ||
		fi.Summary.LogFile != "/var/lib/vordr/runs/a.log" {
		t.Fatalf("failed install: %+v", fi)
	}
	_ = f.srv.Reg.SetRun("h", Run{JobID: "c", Verb: protocol.VerbInstall, Scope: "all", Code: protocol.CodeOK,
		Started: f.now, Finished: &fin})
	if v := f.view(t, "h", f.now); v.FailedInstall != nil || v.Problem != nil {
		t.Fatalf("cleared, still shown: %+v %+v", v.FailedInstall, v.Problem)
	}
}
