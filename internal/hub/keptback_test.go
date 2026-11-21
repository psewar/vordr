package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// keptBackReport: pending updates of which the last kept are left by install.mode upgrade (ADR-0127).
func keptBackReport(at time.Time, pending, security, kept int) wire.Report {
	rep := aptReport(at, pending, security, nil)
	for i := pending - kept; i < pending; i++ {
		rep.Sources[0].Packages[i].KeptBack = true
	}
	rep.Sources[0].KeptBack = kept
	return rep
}

// What install.mode upgrade leaves pending is no update the install button of scope all would take: installable counts
// it out, the view names it, the pending and security counts stay; the security button is not affected (ADR-0127).
func TestKeptBackIsNotInstallable(t *testing.T) {
	f := newViewFixture(t)
	all := wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all"}
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, all, true)
	_ = f.srv.Reg.SetReport("h", keptBackReport(f.now, 3, 1, 2))
	v := f.view(t, "h", f.now)
	if v.Pending == nil || *v.Pending != 3 || *v.Security != 1 || v.Installable == nil || *v.Installable != 1 ||
		v.KeptBack == nil || *v.KeptBack != 2 {
		t.Fatalf("pending %v security %v installable %v kept back %v", v.Pending, v.Security, v.Installable, v.KeptBack)
	}
	_ = f.srv.Reg.SetReport("h", keptBackReport(f.now, 2, 0, 2))
	if v := f.view(t, "h", f.now); *v.Installable != 0 || *v.Pending != 2 {
		t.Fatalf("only kept back: installable %d, pending %d", *v.Installable, *v.Pending)
	}
	_ = f.srv.Reg.SetReport("h", aptReport(f.now, 2, 0, nil))
	if v := f.view(t, "h", f.now); *v.Installable != 2 || v.KeptBack != nil {
		t.Fatalf("none kept back: installable %d, kept back %v", *v.Installable, v.KeptBack)
	}

	sec := wire.PolicySummary{AllowedScopes: []string{"security"}, DefaultScope: "security"}
	f.host(t, "s", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, sec, true)
	_ = f.srv.Reg.SetReport("s", keptBackReport(f.now, 2, 2, 2)) // a security run installs by name: kept back or not
	if v := f.view(t, "s", f.now); *v.Installable != 2 {
		t.Fatalf("security: installable %d", *v.Installable)
	}
}

// The view names why the host holds its updates back where every source that holds some back names the same reason, so
// that Home Assistant names the one step; a source that holds none back does not count. Where one names none (an agent
// before ADR-0207) or two name different ones, the view names none (ADR-0207).
func TestKeptBackWhy(t *testing.T) {
	f := newViewFixture(t)
	all := wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all"}
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, all, true)
	const none, mode, branch = protocol.KeptBackNone, protocol.KeptBackInstallMode, protocol.KeptBackOtherBranch
	for name, c := range map[string]struct {
		apt, device protocol.KeptBackReason
		deviceKept  int
		want        protocol.KeptBackReason
	}{
		"install mode":                           {mode, none, 0, mode},
		"another branch":                         {branch, none, 0, branch},
		"an agent before ADR-0207":               {none, none, 0, none},
		"two reasons":                            {mode, branch, 1, none},
		"the same reason twice":                  {branch, branch, 1, branch},
		"one source names none":                  {mode, none, 1, none},
		"a source that holds none back":          {branch, mode, 0, branch},
		"only the second source holds some back": {none, branch, 1, none},
	} {
		rep := keptBackReport(f.now, 3, 0, 2)
		rep.Sources[0].KeptBackWhy = c.apt
		dev := source.Report{Source: "device", State: protocol.SourceUpToDate, KeptBackWhy: c.device, CheckedAt: f.now}
		if c.deviceKept > 0 {
			dev.State, dev.Pending, dev.KeptBack = protocol.SourceUpdatesAvailable, 1, 1
			dev.Packages = []source.Package{{Name: "release", From: "1", To: "2", KeptBack: true}}
		}
		rep.Sources = append(rep.Sources, dev)
		_ = f.srv.Reg.SetReport("h", rep)
		if v := f.view(t, "h", f.now); v.KeptBack == nil || *v.KeptBack != 2+c.deviceKept || v.KeptBackWhy != c.want {
			t.Errorf("%s: kept back %v, why %q, want %q", name, v.KeptBack, v.KeptBackWhy, c.want)
		}
	}
	// Nothing held back: no count, no reason - also where a source names one anyway.
	rep := aptReport(f.now, 2, 0, nil)
	rep.Sources[0].KeptBackWhy = protocol.KeptBackInstallMode
	_ = f.srv.Reg.SetReport("h", rep)
	if v := f.view(t, "h", f.now); v.KeptBack != nil || v.KeptBackWhy != protocol.KeptBackNone {
		t.Fatalf("none kept back: %v %q", v.KeptBack, v.KeptBackWhy)
	}
}

// The automatic install of scope all does not run for updates install.mode upgrade leaves; with one it takes, it does.
func TestAutoInstallSkipsKeptBack(t *testing.T) {
	f := newAutoFixture(t, func(p *wire.PolicySummary) {
		p.AllowedScopes, p.DefaultScope, p.AutoScope = []string{"security", "all"}, "all", "all"
	})
	_ = f.srv.Reg.SetReport("canary", keptBackReport(f.now, 2, 0, 2))
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatalf("only kept back: %q", r)
	}
	_ = f.srv.Reg.SetReport("canary", keptBackReport(f.now, 3, 0, 2))
	if scope, r := f.due(t, "canary"); r != autoReady || scope != "all" {
		t.Fatalf("one it takes: %q %q", scope, r)
	}
}
