package inventory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
)

// A version with a control character - one the old check let through as well as the line breaks and tabs it caught -
// stays on the host as a skipped entry: it would make the hub refuse a vet request that names it (ADR-0196), and a
// refresh of dependencies offers every installed one.
func TestAVersionWithAControlCharacterStaysOnTheHost(t *testing.T) {
	nm := filepath.Join(t.TempDir(), "node_modules")
	bad := []string{"1.0.0\v", "1.0.0\f", "1.0.0\x1b[2J", "1.0.0\x7f", "1.0.0\x00", "1.0.0\u0085", "1.0.0\u009b"}
	for i, v := range append([]string{"4.4.2"}, bad...) {
		raw, _ := json.Marshal(map[string]string{"name": "pkg" + string(rune('a'+i)), "version": v})
		write(t, filepath.Join(nm, "pkg"+string(rune('a'+i)), "package.json"), string(raw))
	}
	if loc := read("npm-global", KindNpmGlobal, nm); len(loc.Packages) != 1 || loc.Packages[0].Version != "4.4.2" ||
		loc.Skipped != len(bad) {
		t.Errorf("npm: %v, skipped %d", pkgs(loc), loc.Skipped)
	}
	venv := filepath.Join(t.TempDir(), "venv")
	write(t, filepath.Join(venv, "pyvenv.cfg"), "")
	site := filepath.Join(venv, "lib", "python3.11", "site-packages")
	distInfo(t, site, "requests", "2.31.0")
	write(t, filepath.Join(site, "evil-1.0.dist-info", "METADATA"), "Metadata-Version: 2.1\nName: evil\nVersion: 1.0\x1b0\n")
	if loc := read("venv", KindVenv, venv); len(loc.Packages) != 1 || loc.Skipped != 1 {
		t.Errorf("pip: %v, skipped %d", pkgs(loc), loc.Skipped)
	}
}

// A planned entry the hub would refuse is no package to the agent (ADR-0196): pip's and npm's plans with a version that
// has white space around it (the reading of versions trims it), a control character, or more than the hub takes stop
// the update with the agent's own code before the hub is asked.
func TestAPlannedEntryTheHubWouldRefuseIsNoPackage(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	long := "5.0.1-" + strings.Repeat("a", protocol.MaxVetEntryLen)
	for name, c := range map[string]struct {
		eco     versions.Ecosystem
		version string
	}{
		"pip, a space after":             {versions.PyPI, "2.32.3 "},
		"npm, a space before":            {versions.Npm, " 5.0.1"},
		"pip, a form feed after":         {versions.PyPI, "2.32.3\f"},
		"pip, a line break before":       {versions.PyPI, "\n2.32.3"},
		"pip, an escape in it":           {versions.PyPI, "2.32.3\x1b"},
		"npm, a form feed after":         {versions.Npm, "5.0.1\f"},
		"npm, a tab before":              {versions.Npm, "\t5.0.1"},
		"npm, a vertical tab after":      {versions.Npm, "5.0.1\v"},
		"npm, longer than the hub takes": {versions.Npm, long},
	} {
		asked := false
		vet := func(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
			asked = true
			return vetAll(nil)(ctx, eco, plan)
		}
		var loc Location
		var pm *fakePM
		target := "requests"
		if c.eco == versions.PyPI {
			var site string
			loc, site = venvLocation(t)
			pm = &fakePM{t: t, site: site, plan: map[string]string{"requests": c.version}}
		} else {
			var nm string
			loc, nm = npmGlobal(t)
			pm = &fakePM{t: t, nm: nm, plan: map[string]string{"@example/nodes": c.version}}
			target = "@example/nodes"
		}
		u := newUpdater(t, pm, vet)
		res := u.Update(context.Background(), loc, target, func() error { t.Fatal("journal started"); return nil })
		if res.Code != protocol.CodeFailedPackageManager || !strings.Contains(res.Detail, "planned an entry that is no package") ||
			asked {
			t.Errorf("%s: %s %q, the hub asked: %v", name, res.Code, res.Detail, asked)
		}
	}
	// The same entries without what the hub refuses go on to the hub.
	loc, site := venvLocation(t)
	asked := false
	u := newUpdater(t, &fakePM{t: t, site: site, plan: map[string]string{"requests": "2.32.3"}},
		func(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
			asked = true
			return vetAll(nil)(ctx, eco, plan)
		})
	u.Update(context.Background(), loc, "requests", func() error { return nil })
	if !asked {
		t.Error("a plan in bounds was not vetted")
	}
}
