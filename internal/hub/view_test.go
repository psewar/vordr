package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

type viewFixture struct {
	srv *Server
	now time.Time
}

func newViewFixture(t *testing.T) *viewFixture {
	t.Helper()
	dir := t.TempDir()
	reg, _ := OpenRegistry(filepath.Join(dir, "r.json"))
	events, _ := OpenEvents(filepath.Join(dir, "e.jsonl"), time.Now, nil)
	t.Cleanup(func() { events.Close() })
	key, _, _ := identity.LoadOrCreateAt(filepath.Join(dir, "hub.key"))
	clients, _ := OpenClientRegistry(filepath.Join(dir, "c.json"))
	srv := NewServer(reg, clients, events, key, 5, log.New(io.Discard, "", 0))
	srv.Version = "test"
	f := &viewFixture{srv: srv, now: srv.started}
	srv.Now = func() time.Time { return f.now }
	return f
}

// host registers a host; approved unless approve is false. Returns its fingerprint.
func (f *viewFixture) host(t *testing.T, node string, verbs []protocol.Verb, policy wire.PolicySummary, approve bool) string {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	fp := identity.Fingerprint(pub)
	h := wire.Hello{Node: node, PublicKey: pub, AgentVersion: "1.0", Verbs: verbs, Policy: policy,
		Facts: facts.Host{OS: "TestOS 1", Virtualization: "lxc"}}
	if _, _, err := f.srv.Reg.Admit(h, fp, f.now, 5); err != nil {
		t.Fatal(err)
	}
	if approve {
		if code := f.srv.Reg.Decide(node, wire.StateApproved, fp, f.now); code != AdminOK {
			t.Fatal(code)
		}
	}
	return fp
}

func aptReport(at time.Time, pending, security int, reboot *source.Reboot) wire.Report {
	pkgs := []source.Package{}
	for i := 0; i < pending; i++ {
		pkgs = append(pkgs, source.Package{Name: "pkg" + string(rune('a'+i)), From: "1", To: "2", Security: i < security})
	}
	st := protocol.SourceUpToDate
	if pending > 0 {
		st = protocol.SourceUpdatesAvailable
	}
	return wire.Report{T: wire.TReport, CheckedAt: at, Reboot: reboot,
		Sources: []source.Report{{Source: "apt", State: st, Pending: pending, Security: security, Packages: pkgs}}}
}

func (f *viewFixture) view(t *testing.T, node string, at time.Time) wire.HostView {
	t.Helper()
	e, ok := f.srv.Reg.Get(node)
	if !ok {
		t.Fatalf("no host %s", node)
	}
	return f.srv.HostView(e, at)
}

// A PC that said it goes to sleep is away, not offline; once it is heard from again, the rule holds again.
func TestSleepingHostIsNotOffline(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "pc", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	f.srv.Reg.SetReport("pc", aptReport(f.now, 0, 0, nil))
	if err := f.srv.Reg.SetPower("pc", wire.PowerSleeping, f.now); err != nil {
		t.Fatal(err)
	}
	if v := f.view(t, "pc", f.now.Add(3*time.Hour)); v.Problem != nil || v.Power != wire.PowerSleeping || v.Online {
		t.Fatalf("sleeping: power %q, problem %+v", v.Power, v.Problem)
	}
	if e, _ := f.srv.Reg.Get("pc"); e.PowerSince == nil || !e.PowerSince.Equal(f.now) {
		t.Fatalf("power since: %+v", e.PowerSince)
	}
	f.srv.Reg.SetPower("pc", "", f.now)
	if v := f.view(t, "pc", f.now.Add(3*time.Hour)); v.Problem == nil || v.Problem.Reason != ReasonOffline || v.Power != "" {
		t.Fatalf("heard from again, then gone without notice: %+v", v.Problem)
	}
	if err := f.srv.Reg.SetPower("unknown", wire.PowerSleeping, f.now); err != nil {
		t.Fatalf("unknown host: %v", err)
	}
}

func TestProblemReasons(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	f.srv.Reg.SetReport("h", aptReport(f.now, 2, 0, nil))

	if v := f.view(t, "h", f.now.Add(9*time.Minute)); v.Problem != nil {
		t.Fatalf("offline within the grace time is no problem: %+v", v.Problem)
	}
	if v := f.view(t, "h", f.now.Add(11*time.Minute)); v.Problem == nil || v.Problem.Reason != ReasonOffline {
		t.Fatalf("offline beyond grace: %+v", v.Problem)
	}
	if v := f.view(t, "h", f.now.Add(8*24*time.Hour)); v.Problem == nil || !slices.Contains(v.Problem.Reasons, ReasonStale) {
		t.Fatalf("pending for 8 days: %+v", v.Problem)
	}

	failed := aptReport(f.now, 0, 0, nil)
	failed.Sources[0].State, failed.Sources[0].Code = protocol.SourceCheckFailed, protocol.CodePrecheckRepoUnreachable
	f.srv.Reg.SetReport("h", failed)
	v := f.view(t, "h", f.now)
	if v.Problem == nil || v.Problem.Reason != ReasonCheckFailed || v.Problem.CheckFailedCode != protocol.CodePrecheckRepoUnreachable ||
		v.Pending != nil || v.System == nil || len(v.System.Packages) != 0 {
		t.Fatalf("check failed: %+v %+v", v.Problem, v.System)
	}
	if e, _ := f.srv.Reg.Get("h"); e.PendingSince == nil {
		t.Fatal("a failed check reset the pending age")
	}

	fin := f.now
	f.srv.Reg.SetRun("h", Run{JobID: "j", Verb: protocol.VerbCheck, Code: protocol.CodeRejectedInvalidJob,
		Reason: protocol.InvalidBadSignature, Started: f.now, Finished: &fin})
	f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, nil))
	v = f.view(t, "h", f.now)
	if v.Problem == nil || v.Problem.Reason != string(protocol.CodeRejectedInvalidJob) || v.LastRun == nil ||
		v.LastRun.State != "rejected_invalid_job" || v.LastRun.Reason != protocol.InvalidBadSignature {
		t.Fatalf("run problem: %+v %+v", v.Problem, v.LastRun)
	}
	if e, _ := f.srv.Reg.Get("h"); e.PendingSince != nil {
		t.Fatal("nothing pending, but the age kept running")
	}
}

// Whatever the hub does not know stays unknown (nil) - never "up to date", never "no reboot".
func TestUnknownStaysUnknown(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	if v := f.view(t, "h", f.now); v.LastReport != nil || v.System != nil || v.Pending != nil || v.Reboot.Required != nil {
		t.Fatalf("never reported: %+v", v)
	}
	f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, &source.Reboot{Required: false, Checked: []string{}}))
	if v := f.view(t, "h", f.now); v.Reboot.Required != nil {
		t.Fatalf("no signal answered, yet the reboot need is known: %+v", v.Reboot)
	}
	f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, &source.Reboot{Required: false, Checked: []string{"kernel"},
		Running: "6.1.0-10-generic", Newest: "6.1.0-10-generic"}))
	v := f.view(t, "h", f.now)
	if v.Reboot.Required == nil || *v.Reboot.Required || v.System == nil || v.Pending == nil || *v.Pending != 0 ||
		len(v.System.Packages) != 0 {
		t.Fatalf("answered and up to date: %+v %+v", v.Reboot, v.System)
	}
	if v.Reboot.KernelRunning != "6.1.0-10-generic" || v.Reboot.KernelNewest != "6.1.0-10-generic" {
		t.Fatalf("kernels: %+v", v.Reboot)
	}
}

// The views carry codes and numbers, never text: every problem reason is a reason constant or a result code, and the
// pending packages arrive as data, capped at MaxPackages in report order.
func TestViewsCarryCodesOnly(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	f.srv.Reg.SetReport("h", aptReport(f.now, wire.MaxPackages+5, 3, nil))
	fin := f.now
	f.srv.Reg.SetRun("h", Run{JobID: "j", Verb: protocol.VerbInstall, Code: protocol.CodeFailedPackageManager,
		Started: f.now, Finished: &fin})
	v := f.view(t, "h", f.now.Add(8*24*time.Hour))
	known := []string{ReasonOffline, ReasonStale, ReasonCheckFailed, ReasonRebootPending, ReasonNoReport}
	if v.Problem == nil || len(v.Problem.Reasons) < 2 {
		t.Fatalf("expected a run problem and stale: %+v", v.Problem)
	}
	for _, r := range v.Problem.Reasons {
		if !slices.Contains(known, r) && !protocol.Code(r).Valid() {
			t.Errorf("reason %q is neither a reason nor a result code", r)
		}
	}
	pk := v.System.Packages
	if len(pk) != wire.MaxPackages || pk[0].Name != "pkga" || !pk[0].Security || pk[3].Security || pk[1].From != "1" ||
		pk[1].To != "2" || *v.Pending != wire.MaxPackages+5 || *v.Security != 3 || pk[0].Major {
		t.Fatalf("packages: %d %+v pending=%d security=%d", len(pk), pk[:4], *v.Pending, *v.Security)
	}

	// A major jump the source found reaches Home Assistant.
	win := aptReport(f.now, 1, 0, nil)
	win.Sources[0].Source, win.Sources[0].Packages[0].Major = "winget", true
	f.srv.Reg.SetReport("h", win)
	if v := f.view(t, "h", f.now); !v.System.Packages[0].Major {
		t.Fatalf("major jump lost: %+v", v.System.Packages)
	}
}

// A policy alone offers nothing an agent version cannot do.
func TestPolicyAloneOffersNothing(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{AllowedScopes: []string{"security", "all"},
		DefaultScope: "all", RebootAllowed: true, AgentUpdate: true, Apps: []wire.AppInfo{{ID: "gitlab", Name: "GitLab"}}}, true)
	c := f.view(t, "h", f.now).Capabilities
	if !c.Check || c.Install || c.InstallSecurity || c.Reboot || c.AgentUpdate || len(c.Apps) != 0 || c.DefaultScope != "" {
		t.Fatalf("capabilities: %+v", c)
	}
}

func TestHubViewListsWaitingHosts(t *testing.T) {
	f := newViewFixture(t)
	fp := f.host(t, "waiting", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	f.host(t, "approved", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	v := f.srv.HubView(f.srv.Reg.List())
	if len(v.Waiting) != 1 || v.Waiting[0].Node != "waiting" || v.Waiting[0].Fingerprint != fp || v.Protocol != wire.HAProtocol ||
		v.Version != "test" || v.HostsOnline != 0 {
		t.Fatalf("hub view: %+v", v)
	}
	// The thresholds the integration's texts name, in the units of hub.yaml (defaults of NewServer).
	if v.Limits != (wire.LimitsView{OfflineAfterMinutes: 10, StaleAfterDays: 7, RebootPendingAfterDays: 7, NoReportAfterHours: 26}) {
		t.Fatalf("limits: %+v", v.Limits)
	}
}

func TestResolveCommands(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "check-only", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	f.host(t, "full", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall, protocol.VerbReboot},
		wire.PolicySummary{AllowedScopes: []string{"security", "app:gitlab"}, DefaultScope: "security"}, true)
	f.host(t, "waiting", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	cases := []struct {
		node, cmd string
		verb      protocol.Verb
		scope     string
		code      AdminCode
	}{
		{"check-only", "check", protocol.VerbCheck, "", AdminOK},
		{"check-only", "install", "", "", AdminVerbUnsupported},
		{"check-only", "rm -rf /", "", "", AdminBadRequest},
		{"full", "install", protocol.VerbInstall, "security", AdminOK},
		{"full", "install_security", protocol.VerbInstall, "security", AdminOK},
		{"full", "install_app:gitlab", protocol.VerbInstall, "app:gitlab", AdminOK},
		{"full", "install_app:openssl", "", "", AdminNotAllowed},
		{"full", "install_app:../x", "", "", AdminBadRequest},
		{"full", "reboot", "", "", AdminNotAllowed},
		{"full", "agent_update", "", "", AdminVerbUnsupported},
		{"waiting", "check", "", "", AdminNotApproved},
		{"nobody", "check", "", "", AdminUnknownNode},
	}
	for _, c := range cases {
		verb, scope, code := f.srv.Resolve(c.node, c.cmd)
		if verb != c.verb || scope != c.scope || code != c.code {
			t.Errorf("%s %q: %q %q %s, want %q %q %s", c.node, c.cmd, verb, scope, code, c.verb, c.scope, c.code)
		}
	}
}
