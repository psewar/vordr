package agent

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

type fakeInstaller struct {
	result source.InstallResult
	reboot bool
	change bool // gets as far as changing packages (calls BeforeChange)
	steps  []source.Progress
	calls  int
	req    source.InstallRequest
}

func (f *fakeInstaller) Install(_ context.Context, req source.InstallRequest, _ time.Duration,
	progress func(source.Progress)) source.InstallResult {
	f.calls++
	f.req = req
	if f.change {
		if err := req.BeforeChange(); err != nil {
			return source.InstallResult{Code: protocol.CodeFailedPackageManager, Detail: err.Error()}
		}
	}
	for _, s := range f.steps {
		progress(s)
	}
	return f.result
}

func (f *fakeInstaller) RebootRequired(context.Context) bool { return f.reboot }

type outbox struct{ sent []any }

func (o *outbox) send(v any) error { o.sent = append(o.sent, v); return nil }

func (o *outbox) statuses() []wire.JobStatus {
	var out []wire.JobStatus
	for _, v := range o.sent {
		if st, ok := v.(wire.JobStatus); ok {
			out = append(out, st)
		}
	}
	return out
}

func (o *outbox) final() wire.JobStatus {
	st := o.statuses()
	if len(st) == 0 || !st[len(st)-1].Final() {
		return wire.JobStatus{}
	}
	return st[len(st)-1]
}

func installClient(t *testing.T, inst Installer, in policy.Install) *Client {
	t.Helper()
	if in.MinIntervalH == 0 {
		in.MinIntervalH, in.TimeoutMin = 6, 60
	}
	cfg := policy.Config{Node: "host-a", StateDir: t.TempDir(), Install: in}
	c := NewClient(cfg, nil, "test", facts.Host{}, &fakeChecker{}, log.New(io.Discard, "", 0))
	if inst != nil {
		c.Installer = inst
	}
	return c
}

func installJob(id, scope string) protocol.Job {
	return protocol.Job{ID: id, Node: "host-a", Verb: protocol.VerbInstall, Scope: scope}
}

var allowAll = policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all"}

// closedWindow opens in an hour, every day: closed now.
func closedWindow() policy.Window {
	now := time.Now().In(time.Local)
	return policy.Window{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"},
		Start: now.Add(time.Hour).Format("15:04"), End: now.Add(2 * time.Hour).Format("15:04")}
}

// A person asked for the run outside the window, with a reason (ADR-0087): it runs, and the reason is in the log.
func TestInstallOutsideWindowWithReason(t *testing.T) {
	closed := closedWindow()
	inst := &fakeInstaller{result: source.InstallResult{Code: protocol.CodeOK}}
	c := installClient(t, inst, policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all", Window: &closed})
	var logged bytes.Buffer
	c.Log = log.New(&logged, "", 0)
	j := installJob("j1", "all")
	j.OutsideWindow = "ops: tomorrow is no good"
	var out outbox
	c.install(j, out.send)
	if inst.calls != 1 || out.final().Code != protocol.CodeOK {
		t.Fatalf("installer calls %d, sent %+v", inst.calls, out.sent)
	}
	if !strings.Contains(logged.String(), `job j1 install outside the window, reason: "ops: tomorrow is no good"`) {
		t.Fatalf("log: %s", logged.String())
	}
}

func TestInstallFollowsThePolicy(t *testing.T) {
	if v := installClient(t, nil, allowAll).Verbs(); !slices.Equal(v, []protocol.Verb{protocol.VerbCheck}) {
		t.Fatalf("without an installer the agent announces %v", v)
	}
	now := time.Now().In(time.Local)
	later := policy.Window{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"},
		Start: now.Add(time.Hour).Format("15:04"), End: now.Add(2 * time.Hour).Format("15:04")}
	cases := map[string]struct {
		inst  Installer
		in    policy.Install
		scope string
		code  protocol.Code
	}{
		"no installer":      {nil, allowAll, "all", protocol.CodeRejectedUnsupported},
		"install forbidden": {&fakeInstaller{}, policy.Install{}, "all", protocol.CodeRejectedNotAllowed},
		"scope not allowed": {&fakeInstaller{}, allowAll, "security", protocol.CodeRejectedScopeNotAllowed},
		"window closed": {&fakeInstaller{}, policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all",
			Window: &later}, "all", protocol.CodeRejectedOutsideWindow},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := installClient(t, tc.inst, tc.in)
			var out outbox
			c.install(installJob("j1", tc.scope), out.send)
			if st := out.final(); st.Code != tc.code || st.JobID != "j1" {
				t.Fatalf("got %+v", out.sent)
			}
			if f, ok := tc.inst.(*fakeInstaller); ok && f.calls != 0 {
				t.Fatal("the installer ran despite the refusal")
			}
		})
	}
}

// A run that changes packages: progress over the current session (a new phase at once, within a phase at most
// once per gap), a fresh report, then the result - from the journal, which also holds the minimum interval.
func TestInstallRun(t *testing.T) {
	inst := &fakeInstaller{change: true, reboot: true,
		result: source.InstallResult{Code: protocol.CodeOK, Planned: 3, Packages: []string{"libc6", "tzdata", "bash"},
			Log: []byte("Setting up libc6 ...\n"), OutdatedServices: []string{"dbus.service"}},
		steps: []source.Progress{{Phase: protocol.PhasePrechecking}, {Phase: protocol.PhaseDownloading, Percent: 5},
			{Phase: protocol.PhaseDownloading, Percent: 6}, {Phase: protocol.PhaseInstalling, Percent: 35, Item: "libc6"}}}
	c := installClient(t, inst, allowAll)
	var session, bringer outbox
	c.approvedSession(session.send)
	c.install(installJob("j1", "all"), bringer.send)

	if len(bringer.sent) != 0 {
		t.Fatalf("a journaled run answered over the session that brought it: %+v", bringer.sent)
	}
	st := session.statuses()
	if len(st) != 4 || st[0].Phase != protocol.PhasePrechecking || st[1].Phase != protocol.PhaseDownloading ||
		st[2].Phase != protocol.PhaseInstalling || st[2].Item != "libc6" || *st[2].Progress != 35 {
		t.Fatalf("statuses: %+v", st)
	}
	fin := session.final()
	if fin.Code != protocol.CodeOKRebootRequired || fin.JobID != "j1" {
		t.Fatalf("result: %+v", fin)
	}
	// What the run did travels with the result; the whole output stays on the host.
	sum := fin.Summary
	if sum == nil || sum.PackageCount != 3 || !slices.Equal(sum.Packages, []string{"libc6", "tzdata", "bash"}) ||
		!slices.Equal(sum.OutdatedServices, []string{"dbus.service"}) ||
		sum.LogFile != filepath.Join(c.Cfg.StateDir, "runs", "j1.log") {
		t.Fatalf("summary: %+v", sum)
	}
	if raw, err := os.ReadFile(sum.LogFile); err != nil || string(raw) != "Setting up libc6 ...\n" {
		t.Fatalf("run log: %q %v", raw, err)
	}
	if _, isReport := session.sent[len(session.sent)-2].(wire.Report); !isReport {
		t.Fatalf("no report before the result: %#v", session.sent[len(session.sent)-2])
	}
	jr, ok := c.readJournal()
	if !ok || jr.Code != protocol.CodeOKRebootRequired || !jr.Acknowledged || jr.JobID != "j1" || jr.Summary == nil {
		t.Fatalf("journal: %+v %v", jr, ok)
	}

	var again outbox
	c.install(installJob("j2", "all"), again.send)
	if again.final().Code != protocol.CodeRejectedRateLimited || inst.calls != 1 {
		t.Fatalf("second run within min_interval_h: %+v, installer calls %d", again.sent, inst.calls)
	}
}

// The result outlives the session: without one it stays in the journal and goes out on the next approved session.
// A journal without a result at start means the run was interrupted.
func TestInstallResultReachesTheHubLater(t *testing.T) {
	c := installClient(t, &fakeInstaller{change: true, result: source.InstallResult{Code: protocol.CodeOK, Planned: 1}},
		allowAll)
	c.install(installJob("j1", "all"), func(any) error { return errNoSession })
	if jr, _ := c.readJournal(); jr.Code != protocol.CodeOK || jr.Acknowledged {
		t.Fatalf("journal without a session: %+v", jr)
	}
	var later outbox
	c.approvedSession(later.send)
	if fin := later.final(); fin.Code != protocol.CodeOK || fin.JobID != "j1" {
		t.Fatalf("on the next session: %+v", later.sent)
	}
	var third outbox
	c.approvedSession(third.send)
	if len(third.sent) != 0 {
		t.Fatalf("sent twice: %+v", third.sent)
	}

	// The agent stopped between journal and result.
	if err := writeJournal(c.journalPath(), runJournal{JobID: "j9", Verb: protocol.VerbInstall, Scope: "all",
		Started: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	restarted := installClient(t, &fakeInstaller{}, allowAll)
	restarted.Cfg.StateDir = c.Cfg.StateDir
	restarted.settleJournal()
	var first outbox
	restarted.approvedSession(first.send)
	if fin := first.final(); fin.Code != protocol.CodeInterrupted || fin.JobID != "j9" {
		t.Fatalf("interrupted run: %+v", first.sent)
	}
	var next outbox
	restarted.install(installJob("j10", "all"), next.send)
	if next.final().Code != protocol.CodeRejectedRateLimited {
		t.Fatalf("the interrupted run's start does not count for the interval: %+v", next.sent)
	}
}
