package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
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
