package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// ADR-0107: the ring delay counts per pending update, and a clean canary install ends it for what was pending before.

func (f *viewFixture) canaryRun(t *testing.T, started time.Time, scope string, code protocol.Code) {
	t.Helper()
	f.canaryRunWith(t, started, scope, code, &wire.RunSummary{PackageCount: 3})
}

// canaryRunWith: the same with the summary the agent sent; nil for a run that changed nothing (no journal).
func (f *viewFixture) canaryRunWith(t *testing.T, started time.Time, scope string, code protocol.Code,
	summary *wire.RunSummary) {
	t.Helper()
	fin := started.Add(5 * time.Minute)
	run := Run{JobID: "j-" + started.Format("150405"), Verb: protocol.VerbInstall, Scope: scope, By: ByAuto, Code: code,
		Started: started, Finished: &fin, Summary: summary}
	if err := f.srv.Reg.SetRun("canary", run); err != nil {
		t.Fatal(err)
	}
}

// canaryLeft: the canary's report after its run - the apt packages still pending, as aptReport names them (pkgc and
// later are no security updates there). A package pending before keeps its first-seen time.
func (f *viewFixture) canaryLeft(t *testing.T, at time.Time, security []string, other ...string) {
	t.Helper()
	pkgs := []source.Package{}
	for _, n := range security {
		pkgs = append(pkgs, source.Package{Name: n, From: "1", To: "2", Security: true})
	}
	for _, n := range other {
		pkgs = append(pkgs, source.Package{Name: n, From: "1", To: "2"})
	}
	st := protocol.SourceUpToDate
	if len(pkgs) > 0 {
		st = protocol.SourceUpdatesAvailable
	}
	rep := wire.Report{T: wire.TReport, CheckedAt: at, Sources: []source.Report{{Source: "apt", State: st,
		Pending: len(pkgs), Security: len(security), Packages: pkgs}}}
	if err := f.srv.Reg.SetReport("canary", rep); err != nil {
		t.Fatal(err)
	}
}

// ADR-0118: a canary's clean run meets only what it did not leave behind. Its security run installs pkga and holds
// back pkgb (ADR-0116); pkgc to pkge are outside its scope. Until the canary reports after the run, nothing counts.
func TestCanaryRunMeetsOnlyWhatItInstalled(t *testing.T) {
	f := newAutoFixture(t, func(p *wire.PolicySummary) {
		p.AllowedScopes, p.DefaultScope, p.AutoScope = []string{"security", "all"}, "all", "all"
	})
	f.now = f.now.Add(20 * time.Minute)
	f.canaryRun(t, f.now, "security", protocol.CodeOK)
	f.now = f.now.Add(10 * time.Minute)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("before the canary reported after its run: %q", r)
	}
	f.canaryLeft(t, f.now, []string{"pkgb"}, "pkgc", "pkgd", "pkge")
	e, _ := f.srv.Reg.Get("other") // scope all: pkga to pkge
	if _, r := f.srv.autoDue(e, f.now); r != autoNotReady {
		t.Fatalf("scope all after a canary security run that held back pkgb: %q", r)
	}
	e.Policy.AutoScope = "security" // pkga and pkgb
	if _, r := f.srv.autoDue(e, f.now); r != autoNotReady {
		t.Fatalf("pkgb was held back at the canary: %q", r)
	}
	// The canary installs pkgb as well, with everything else: all met.
	f.canaryRun(t, f.now, "all", protocol.CodeOK)
	f.canaryLeft(t, f.now.Add(5*time.Minute), nil)
	f.now = f.now.Add(10 * time.Minute)
	if e, _ = f.srv.Reg.Get("other"); func() string { _, r := f.srv.autoDue(e, f.now); return r }() != autoReady {
		t.Fatal("after the canary installed everything")
	}
}

// With two canaries an update is met when one of them installed it.
func TestSoakedWithTwoCanaries(t *testing.T) {
	now := time.Date(2026, 11, 5, 2, 30, 0, 0, time.UTC)
	u := []PendingUpdate{{Source: "apt", Name: "a", Security: true, Since: now.Add(-time.Hour)}}
	held := canaryRun{at: now.Add(-30 * time.Minute), left: map[updateName]bool{{"apt", "a"}: true}}
	took := canaryRun{at: now.Add(-20 * time.Minute), left: map[updateName]bool{}}
	early := canaryRun{at: now.Add(-2 * time.Hour), left: map[updateName]bool{}}
	if soaked(u, "security", []canaryRun{held}, now, 24*time.Hour) {
		t.Error("held back at the only canary")
	}
	if !soaked(u, "security", []canaryRun{held, took}, now, 24*time.Hour) {
		t.Error("installed at the second canary")
	}
	if soaked(u, "security", []canaryRun{early}, now, 24*time.Hour) {
		t.Error("a run before the update was seen")
	}
	if !soaked(u, "security", []canaryRun{held}, now.Add(24*time.Hour), 24*time.Hour) {
		t.Error("after the delay")
	}
}

// A canary's run counts once a complete report received after it ended is there; everything in that report is left
// behind, whenever it was first seen. Before it, or with an incomplete one, the run counts for
// nothing - also when the canary had nothing pending before.
func TestLastCleanRun(t *testing.T) {
	at := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	end, before, after := at.Add(5*time.Minute), at.Add(-time.Hour), at.Add(6*time.Minute)
	e := Entry{CleanInstallAt: &at, CleanInstallEnd: &end, PendingAt: &before}
	if _, ok := lastCleanRun(e); ok {
		t.Error("counted before the canary reported after the run")
	}
	e.PendingAt = &after
	e.PendingUpdates = []PendingUpdate{
		{Source: "apt", Name: "old", Since: before},
		{Source: "apt", Name: "new", Since: after}, // reached the canary only after the run
		{Source: "winget", Since: before},
	}
	r, ok := lastCleanRun(e)
	if !ok || !r.at.Equal(at) || !r.left[updateName{"apt", "old"}] || !r.left[updateName{"apt", "new"}] ||
		!r.left[updateName{"winget", ""}] {
		t.Errorf("after the report: %v %+v", ok, r)
	}
	e.CleanInstallEnd = nil // a register from before ADR-0118
	if _, ok := lastCleanRun(e); ok {
		t.Error("counted without knowing when the run ended")
	}
}

// The review's case: other reports a security fix the canary had not reported yet; the canary's run holds it back,
// and its report after the run is the first to name it. It has not met a canary.
func TestHeldBackUpdateFirstReportedAfterTheRun(t *testing.T) {
	f := newAutoFixture(t) // 02:30: both report pkga to pkge, pkga and pkgb security
	f.now = f.now.Add(5 * time.Minute)
	rep := aptReport(f.now, 5, 2, nil)
	rep.Sources[0].Packages = append(rep.Sources[0].Packages, source.Package{Name: "pkgz", From: "1", To: "2", Security: true})
	rep.Sources[0].Pending, rep.Sources[0].Security = 6, 3
	_ = f.srv.Reg.SetReport("other", rep) // 02:35: pkgz is new on other
	f.now = f.now.Add(5 * time.Minute)
	f.canaryRun(t, f.now, "security", protocol.CodeOK) // 02:40, installs pkga and pkgb
	f.canaryLeft(t, f.now.Add(10*time.Minute), []string{"pkgz"}, "pkgc", "pkgd", "pkge")
	f.now = f.now.Add(15 * time.Minute)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("pkgz held back at the canary, first reported after its run: %q", r)
	}
	// Nothing pending at the canary before its run, and no report after it yet: nothing is met.
	f = newAutoFixture(t)
	f.canaryLeft(t, f.now, nil)
	f.now = f.now.Add(10 * time.Minute)
	f.canaryRun(t, f.now, "security", protocol.CodeOK)
	f.now = f.now.Add(10 * time.Minute)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("a run without a report after it: %q", r)
	}
}

// A canary install that found nothing to install ends no delay: nothing of the scope pending, or
// install.mode upgrade leaving a new kernel pending every night - its ok met none of the updates.
func TestAutoInstallNoOpCanaryRunEndsNoDelay(t *testing.T) {
	f := newAutoFixture(t)
	f.now = f.now.Add(20 * time.Minute)
	f.canaryRunWith(t, f.now, "security", protocol.CodeOK, nil)
	f.canaryRunWith(t, f.now, "all", protocol.CodeOK, &wire.RunSummary{PackageCount: 0})
	f.now = f.now.Add(10 * time.Minute)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("a canary run that installed nothing released the others: %q", r)
	}
	if e, _ := f.srv.Reg.Get("canary"); e.CleanInstallAt != nil {
		t.Fatalf("a run without changes counted as a clean install: %v", e.CleanInstallAt)
	}
}

// New updates count from the hub's receive time when the agent's clock lags: a board back after
// three days off, reporting before its clock is synced, must not have its new updates count as three days old.
func TestFirstSeenNotBeforeTheHubReceivedIt(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	lagging := aptReport(f.now.Add(-72*time.Hour), 2, 1, &source.Reboot{Required: true, Checked: []string{"kernel"}})
	_ = f.srv.Reg.SetReportAt("h", lagging, f.now)
	e, _ := f.srv.Reg.Get("h")
	if len(e.PendingUpdates) != 2 || !e.PendingUpdates[0].Since.Equal(f.now) || !e.PendingSince.Equal(f.now) ||
		!e.RebootSince.Equal(f.now) {
		t.Fatalf("lagging clock: %+v since %v reboot %v", e.PendingUpdates, e.PendingSince, e.RebootSince)
	}
	// A clock ahead of the hub's only makes the wait longer: its time stays.
	f = newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReportAt("h", aptReport(f.now.Add(time.Hour), 2, 1, nil), f.now)
	if e, _ := f.srv.Reg.Get("h"); !e.PendingUpdates[0].Since.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("clock ahead: %+v", e.PendingUpdates)
	}
}

// A clean canary install that started after the updates were first seen makes the later ring due in the same window;
// one that started before, a package run, or a failed one does not.
func TestAutoInstallCleanCanaryRunEndsTheDelay(t *testing.T) {
	f := newAutoFixture(t) // 02:30: both hosts report their updates
	f.canaryRun(t, f.now.Add(-time.Hour), "security", protocol.CodeOK)
	f.now = f.now.Add(20 * time.Minute)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("canary run started before the updates were seen: %q", r)
	}
	f.canaryRun(t, f.now, "pkg:npm-global", protocol.CodeOK)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("a package run is no system install: %q", r)
	}
	f.canaryRun(t, f.now, "security", protocol.CodeOKRebootRequired)
	f.canaryLeft(t, f.now.Add(6*time.Minute), nil, "pkgc", "pkgd", "pkge") // installed pkga and pkgb
	f.now = f.now.Add(10 * time.Minute)                                    // 03:00, the canary is done
	if scope, r := f.due(t, "other"); r != autoReady || scope != "security" {
		t.Fatalf("after a clean canary run in the same window: %q %q", scope, r)
	}
	f.canaryRun(t, f.now, "security", protocol.CodeFailedPackageManager)
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("a failed canary run stops the others: %q", r)
	}
	if e, _ := f.srv.Reg.Get("canary"); e.CleanInstallAt == nil || !e.CleanInstallAt.Equal(f.now.Add(-10*time.Minute)) {
		t.Fatalf("clean install kept: %v", e.CleanInstallAt)
	}
}

// On a host with an old backlog, a new update keeps the host waiting: pending_since alone would have let it through
// before any canary met it.
func TestAutoInstallNewUpdateWaitsOnABacklogHost(t *testing.T) {
	f := newAutoFixture(t)
	f.now = f.now.Add(24 * time.Hour) // the next night: the old updates are past the delay
	for _, n := range []string{"canary", "other"} {
		_ = f.srv.Reg.SetReport(n, aptReport(f.now, 6, 3, nil)) // pkgc is now a security update, pkgf is new
	}
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("a new security update on a backlog host: %q", r)
	}
	if e, _ := f.srv.Reg.Get("other"); e.PendingSince == nil || f.now.Sub(*e.PendingSince) < 24*time.Hour {
		t.Fatalf("pending_since still counts from the first update: %v", e.PendingSince)
	}
	f.canaryRun(t, f.now, "security", protocol.CodeOK)
	f.canaryLeft(t, f.now.Add(5*time.Minute), nil, "pkgd", "pkge", "pkgf") // installed pkga to pkgc
	f.now = f.now.Add(10 * time.Minute)
	if _, r := f.due(t, "other"); r != autoReady {
		t.Fatalf("after the canary met it: %q", r)
	}
	// With scope all, a new non-security update counts as well.
	f = newAutoFixture(t)
	f.now = f.now.Add(24 * time.Hour)
	_ = f.srv.Reg.SetReport("other", aptReport(f.now, 6, 2, nil))
	if _, r := f.due(t, "other"); r != autoReady {
		t.Fatalf("scope security: the new update is no security update: %q", r)
	}
	e, _ := f.srv.Reg.Get("other")
	e.Policy.AllowedScopes, e.Policy.DefaultScope, e.Policy.AutoScope = []string{"security", "all"}, "all", "all"
	if _, r := f.srv.autoDue(e, f.now); r != autoNotReady {
		t.Fatalf("scope all: the new update has not met a canary: %q", r)
	}
}

// Times survive reports, a new target version is a new update, a failed report changes nothing, nothing pending
// clears the list. A source without a package list is one update.
func TestPendingUpdatesKeepTheirFirstSeen(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	t0 := f.now
	_ = f.srv.Reg.SetReport("h", aptReport(t0, 2, 1, nil))
	t1 := t0.Add(6 * time.Hour)
	rep := aptReport(t1, 2, 1, nil)
	rep.Sources[0].Packages[1].To = "3" // pkgb has a newer version now
	rep.Sources = append(rep.Sources, source.Report{Source: "winget", State: protocol.SourceUpdatesAvailable,
		Pending: 4})
	_ = f.srv.Reg.SetReport("h", rep)
	e, _ := f.srv.Reg.Get("h")
	want := []PendingUpdate{
		{Source: "apt", Name: "pkga", To: "2", Security: true, Since: t0},
		{Source: "apt", Name: "pkgb", To: "3", Since: t1},
		{Source: "winget", Since: t1},
	}
	if len(e.PendingUpdates) != len(want) {
		t.Fatalf("pending updates: %+v", e.PendingUpdates)
	}
	for i, u := range want {
		if got := e.PendingUpdates[i]; got.Source != u.Source || got.Name != u.Name || got.To != u.To ||
			got.Security != u.Security || !got.Since.Equal(u.Since) {
			t.Fatalf("pending update %d: %+v, want %+v", i, got, u)
		}
	}
	failed := aptReport(t1.Add(time.Hour), 0, 0, nil)
	failed.Sources[0].State, failed.Sources[0].Code = protocol.SourceCheckFailed, protocol.CodePrecheckRepoUnreachable
	_ = f.srv.Reg.SetReport("h", failed)
	if e, _ = f.srv.Reg.Get("h"); len(e.PendingUpdates) != 3 {
		t.Fatalf("a failed report changed the list: %+v", e.PendingUpdates)
	}
	_ = f.srv.Reg.SetReport("h", aptReport(t1.Add(2*time.Hour), 0, 0, nil))
	if e, _ = f.srv.Reg.Get("h"); e.PendingUpdates != nil || e.PendingSince != nil {
		t.Fatalf("nothing pending: %+v %v", e.PendingUpdates, e.PendingSince)
	}
}

// A host the hub has no list of pending updates for (no complete report since the upgrade) is not soaked.
func TestSoakedNeedsAList(t *testing.T) {
	now := time.Date(2026, 11, 4, 2, 30, 0, 0, time.UTC)
	if soaked(nil, "security", []canaryRun{{at: now}}, now, time.Hour) {
		t.Fatal("no list")
	}
	if soaked([]PendingUpdate{{Source: "apt", Name: "a", Since: now.Add(-48 * time.Hour)}}, "security", nil, now,
		time.Hour) {
		t.Fatal("no security update in the list, scope security")
	}
}
