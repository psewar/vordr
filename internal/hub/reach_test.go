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

// ADR-0129: where the fix of a vulnerable package is. The OSV testdata: debug 4.3.0 (npm) is fixed in 4.3.1, Requests
// 2.31.0 (PyPI) in 2.32.0.
func TestFindingReach(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	p := newTestPackages(t, t.TempDir(), o, []string{"PyPI", "npm"}, clk)
	if !p.Refresh(context.Background(), versions.PyPI) || !p.Refresh(context.Background(), versions.Npm) {
		t.Fatal("no OSV data")
	}
	npmDB, pypiDB := p.dbs[versions.Npm], p.dbs[versions.PyPI]
	cli := inventory.Package{Name: "some-cli", Version: "1.0.0", Direct: true}
	debug := inventory.Package{Name: "debug", Version: "4.3.0", Via: "some-cli"}
	npmLoc := func(update, deps bool, pkgs ...inventory.Package) inventory.Location {
		return inventory.Location{ID: "npm-global", Kind: inventory.KindNpmGlobal, Ecosystem: versions.Npm, Update: update,
			Deps: deps, Packages: pkgs}
	}
	pin := inventory.Hold{By: "some-cli@1.0.0", Range: "4.3.0"}
	readyCLI := cli
	readyCLI.Candidate = "1.1.0"
	with := func(p inventory.Package, holds ...inventory.Hold) inventory.Package { p.Holds = holds; return p }
	requests := inventory.Package{Name: "Requests", Version: "2.31.0", Direct: true}
	cand := func(p inventory.Package, candidate, latest string) inventory.Package {
		p.Candidate, p.Latest = candidate, latest
		return p
	}
	venv := func(update bool) inventory.Location {
		return inventory.Location{ID: "venv-app", Kind: inventory.KindVenv, Ecosystem: versions.PyPI, Update: update}
	}
	for _, c := range []struct {
		name   string
		loc    inventory.Location
		pkg    inventory.Package
		fixed  string
		want   string
		heldBy []inventory.Hold
	}{
		{"nothing holds it, Vördr may not", npmLoc(false, false, cli), debug, "4.3.1", wire.ReachByHand, nil},
		{"pinned below the fix", npmLoc(true, false, cli), with(debug, pin), "4.3.1", wire.ReachHeld, []inventory.Hold{pin}},
		{"pinned, the package it came with has an update", npmLoc(true, false, readyCLI), with(debug, pin), "4.3.1",
			wire.ReachUpdateVia, []inventory.Hold{pin}},
		{"pinned, an update ready, but Vördr may not", npmLoc(false, false, readyCLI), with(debug, pin), "4.3.1",
			wire.ReachByHand, []inventory.Hold{pin}},
		// A package of the tree pins it; the top-level package it came with has an update ready (a new release of a
		// global Node-RED brings new pins along).
		{"pinned deeper, the package it came with has an update", npmLoc(true, false, readyCLI),
			with(debug, inventory.Hold{By: "inner@2.0.0", Range: "4.3.0"}), "4.3.1", wire.ReachUpdateVia,
			[]inventory.Hold{{By: "inner@2.0.0", Range: "4.3.0"}}},
		{"pinned deeper, nothing above it has an update", npmLoc(true, false, cli),
			with(debug, inventory.Hold{By: "inner@2.0.0", Range: "4.3.0"}), "4.3.1", wire.ReachHeld,
			[]inventory.Hold{{By: "inner@2.0.0", Range: "4.3.0"}}},
		{"bundled", npmLoc(true, true, cli), with(debug, inventory.Hold{By: "some-cli@1.0.0", Bundled: true}), "4.3.1",
			wire.ReachHeld, []inventory.Hold{{By: "some-cli@1.0.0", Bundled: true}}},
		{"in range, refreshable", npmLoc(true, true, cli), with(debug, inventory.Hold{By: "x@1.0.0", Range: "^4.3.0"}),
			"4.3.1", wire.ReachRefresh, nil},
		{"in range, not refreshable", npmLoc(true, false, cli), with(debug, inventory.Hold{By: "x@1.0.0", Range: "^4.3.0"}),
			"4.3.1", wire.ReachByHand, nil},
		{"a range that does not read holds nothing", npmLoc(false, false, cli),
			with(debug, inventory.Hold{By: "x@1.0.0", Range: "latest"}), "4.3.1", wire.ReachByHand, nil},
		{"no fixed release", npmLoc(true, true, cli), debug, "", wire.ReachNoFix, nil},
		{"direct, the candidate fixes it", venv(true), cand(requests, "2.32.3", "2.32.4"), "2.32.0", wire.ReachUpdate, nil},
		{"direct, the candidate fixes it, Vördr may not", venv(false), cand(requests, "2.32.3", "2.32.4"), "2.32.0",
			wire.ReachByHand, nil},
		{"direct, the fix is too new", venv(true), cand(requests, "", "2.32.4"), "2.32.0", wire.ReachTooNew, nil},
		{"direct, the candidate stops below the fix", venv(true), cand(requests, "2.31.5", "2.32.4"), "2.32.0",
			wire.ReachTooNew, nil},
		{"direct, nothing known about newer releases", venv(true), requests, "2.32.0", wire.ReachByHand, nil},
		{"direct, held by the project's lock", venv(true), with(cand(requests, "2.32.3", "2.32.4"),
			inventory.Hold{By: "uv.lock", Range: "==2.31.0"}), "2.32.0", wire.ReachHeld,
			[]inventory.Hold{{By: "uv.lock", Range: "==2.31.0"}}},
		{"a lock pin above the fix holds nothing", venv(true), with(cand(requests, "2.32.3", ""),
			inventory.Hold{By: "uv.lock", Range: "==2.32.1"}), "2.32.0", wire.ReachUpdate, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := npmDB
			if c.loc.Ecosystem == versions.PyPI {
				db = pypiDB
			}
			got, held := reach(c.loc, c.pkg, c.fixed, db)
			if got != c.want || !slices.Equal(held, c.heldBy) {
				t.Errorf("reach %s %v, want %s %v", got, held, c.want, c.heldBy)
			}
		})
	}
	// A direct package whose security candidate fixes it (ADR-0111) takes it by update as well.
	sec := cand(requests, "", "2.32.4")
	sec.SecurityCandidate = "2.32.4"
	if got, _ := reach(venv(true), sec, "2.32.0", pypiDB); got != wire.ReachUpdate {
		t.Errorf("security candidate: %s", got)
	}
	for r, want := range map[string]bool{wire.ReachUpdate: true, wire.ReachUpdateVia: true, wire.ReachRefresh: true,
		wire.ReachByHand: true, wire.ReachHeld: false, wire.ReachNoFix: false, wire.ReachTooNew: false} {
		if wire.Actionable(r) != want {
			t.Errorf("Actionable(%s)", r)
		}
	}
}

// ADR-0129: only findings with something to do make the problem; the others are counted as waiting and keep their
// reach and what holds them.
func TestWaitingFindingsMakeNoProblem(t *testing.T) {
	f := newViewFixture(t)
	o := newOSVServer(t)
	f.srv.Packages = newTestPackages(t, t.TempDir(), o, []string{"PyPI", "npm"}, &clock{t: f.now})
	ctx := context.Background()
	f.srv.Packages.Refresh(ctx, versions.PyPI)
	f.srv.Packages.Refresh(ctx, versions.Npm)
	f.host(t, "echo", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReport("echo", aptReport(f.now, 0, 0, nil))

	inv := testInventory(f.now)
	inv.Locations[0].Packages = slices.DeleteFunc(inv.Locations[0].Packages, func(p inventory.Package) bool {
		return p.Name == "chalk" // the malware: a problem whatever holds it
	})
	inv.Locations[0].Packages[0].Holds = []inventory.Hold{{By: "some-cli@1.0.0", Range: "4.3.0"}}
	inv.Locations[1].Packages[0].Holds = []inventory.Hold{{By: "uv.lock", Range: "==2.31.0"}}
	_ = f.srv.Packages.SetInventory("echo", inv)
	v := f.view(t, "echo", f.now)
	pv := v.Packages
	if v.Problem != nil || pv == nil || pv.Vulnerable == nil || *pv.Vulnerable != 2 || pv.Waiting == nil || *pv.Waiting != 2 {
		t.Fatalf("all waiting: problem %+v, packages %+v", v.Problem, pv)
	}
	for _, fv := range pv.Findings {
		if fv.Reach != wire.ReachHeld || len(fv.HeldBy) != 1 {
			t.Errorf("finding %s: %s %v", fv.Name, fv.Reach, fv.HeldBy)
		}
	}

	// One of them with something to do: the problem is back.
	inv.Locations[1].Packages[0].Holds = nil
	_ = f.srv.Packages.SetInventory("echo", inv)
	v = f.view(t, "echo", f.now)
	if v.Problem == nil || !slices.Equal(v.Problem.Reasons, []string{ReasonVulnerablePackage}) || *v.Packages.Waiting != 1 {
		t.Fatalf("one actionable: problem %+v, waiting %v", v.Problem, v.Packages.Waiting)
	}
}
