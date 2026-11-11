package hub

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// securityInventory: an npm location whose direct packages have security candidates (ADR-0111), with the real OSV
// testdata: chalk 5.6.1 and debug 4.4.2 are malware, chalk 5.6.2 is clean, axios 1.14.1 is malware.
func securityInventory(at time.Time, shorter int) wire.Inventory {
	outdated := at.Add(-time.Hour)
	return wire.Inventory{T: wire.TInventory, CheckedAt: at, OutdatedAt: &outdated, MinAgeDays: 7,
		MinAgeDaysSecurity: shorter, Locations: []inventory.Location{
			{ID: "npm-global", Kind: inventory.KindNpmGlobal, Ecosystem: versions.Npm, Path: "/usr/lib/node_modules",
				Update: true, Packages: []inventory.Package{
					// installed with a finding, the security candidate clean: a fix
					{Name: "chalk", Version: "5.6.1", Direct: true, Latest: "5.6.2", SecurityCandidate: "5.6.2"},
					// installed clean, the security candidate is malware: no fix
					{Name: "axios", Version: "1.14.0", Direct: true, Latest: "1.14.1", SecurityCandidate: "1.14.1"},
					// installed with a finding (GHSA-gxpj-cx7g-858c), the security candidate is malware: no fix
					{Name: "debug", Version: "4.3.0", Direct: true, Latest: "4.4.2", SecurityCandidate: "4.4.2"},
					// nothing to fix: the normal minimum age holds
					{Name: "some-cli", Version: "1.0.0", Direct: true, Latest: "1.0.2", SecurityCandidate: "1.0.2"},
					// a dependency with a finding
					{Name: "debug", Version: "4.4.2", Via: "some-cli"},
					{Name: "ms", Version: "2.1.3", Via: "some-cli"},
				}},
		}}
}

// The hub decides from OSV which security candidate is a fix and names the packages for the job: the direct ones
// whose update fixes a finding, for a dependency refresh the dependencies with findings. An agent that announces no
// shorter age gets none.
func TestSecurityFixesFromOSV(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Now()}
	p := newTestPackages(t, t.TempDir(), o, []string{"npm"}, clk)
	p.Refresh(context.Background(), versions.Npm)
	if err := p.SetInventory("h", securityInventory(clk.now(), 2)); err != nil {
		t.Fatal(err)
	}
	v := p.View("h")
	fixes := map[string]string{}
	for _, u := range v.Updates {
		fixes[u.Name] = u.Fix
	}
	if fixes["chalk"] != "5.6.2" || fixes["axios"] != "" || fixes["debug"] != "" || fixes["some-cli"] != "" ||
		v.MinAgeDaysSecurity != 2 {
		t.Fatalf("fixes %v, view %+v", fixes, v)
	}
	if v.Updates[0].Name != "chalk" {
		t.Errorf("a fix is ready and comes first: %+v", v.Updates)
	}
	for scope, want := range map[string][]string{
		"pkg:npm-global":          {"chalk"},
		"pkg:npm-global/chalk":    {"chalk"},
		"pkg:npm-global/axios":    nil,
		"pkg:npm-global/debug":    nil,
		"deps:npm-global":         {"debug"},
		"pkg:venv-app":            nil,
		"all":                     nil,
		"pkg:npm-global/some-cli": nil,
	} {
		if got := p.SecurityFixes("h", scope); !slices.Equal(got, want) {
			t.Errorf("SecurityFixes(%s) = %v, want %v", scope, got, want)
		}
	}
	if !p.Updatable("h", "npm-global", "chalk") || p.Updatable("h", "npm-global", "axios") ||
		p.Updatable("h", "npm-global", "debug") {
		t.Error("a fix is updatable without a normal candidate, a malware candidate is not")
	}
	// An agent before ADR-0111: no shorter age, so no fixes in its jobs, and its packages wait as before.
	_ = p.SetInventory("old", securityInventory(clk.now(), 0))
	if got := p.SecurityFixes("old", "pkg:npm-global"); got != nil || p.Updatable("old", "npm-global", "chalk") {
		t.Errorf("older agent: %v", got)
	}
}

// The job a Home Assistant command becomes carries the fixes, decided when it goes out.
func TestHAJobCarriesTheFixes(t *testing.T) {
	f := newViewFixture(t)
	o := newOSVServer(t)
	clk := &clock{t: f.now}
	p := newTestPackages(t, t.TempDir(), o, []string{"npm"}, clk)
	p.Refresh(context.Background(), versions.Npm)
	f.srv.Packages = p
	f.host(t, "h", []protocol.Verb{protocol.VerbInstall}, wire.PolicySummary{AllowedScopes: []string{"all"}}, true)
	_ = p.SetInventory("h", securityInventory(clk.now(), 2))
	if j := f.srv.haJob("h", protocol.VerbInstall, "pkg:npm-global", ""); !slices.Equal(j.SecurityFix, []string{"chalk"}) {
		t.Fatalf("pkg job: %+v", j)
	}
	if j := f.srv.haJob("h", protocol.VerbInstall, "all", ""); j.SecurityFix != nil {
		t.Fatalf("system job: %+v", j)
	}
	if j := f.srv.haJob("h", protocol.VerbReboot, "", ""); j.SecurityFix != nil {
		t.Fatalf("reboot: %+v", j)
	}
	// What the hub signs passes the agent's check of the job.
	j := f.srv.haJob("h", protocol.VerbInstall, "pkg:npm-global", "")
	j.Node, j.IssuedAt, j.ExpiresAt = "h", f.now, f.now.Add(time.Minute)
	sj, err := protocol.SignJob(j, f.srv.Key.Sign)
	if err != nil {
		t.Fatal(err)
	}
	if got, why := protocol.VerifyJob(sj, f.srv.Key.Public(), "h", f.now, protocol.NewReplayGuard()); why != protocol.InvalidNone ||
		!slices.Equal(got.SecurityFix, []string{"chalk"}) {
		t.Fatalf("agent's check: %q %+v", why, got)
	}
}
