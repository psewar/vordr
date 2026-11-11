package hub

import (
	"slices"
	"testing"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// ADR-0119: the hub offers what the agent reported as updatable - per image the Compose services its policy allows,
// for an image it can move - and resolves only those commands; the agent checks everything again.
func TestImageUpdateCommands(t *testing.T) {
	f := newViewFixture(t)
	clk := &clock{t: f.now}
	f.srv.Packages = newTestPackages(t, t.TempDir(), nil, nil, clk)
	install := []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}
	f.host(t, "echo", install, wire.PolicySummary{AllowedScopes: []string{"image:stack/*"}}, true)
	f.host(t, "foxtrot", install, wire.PolicySummary{}, true)
	f.host(t, "golf", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	at := f.now
	base := wire.Inventory{T: wire.TInventory, CheckedAt: at, OutdatedAt: &at, MinAgeDays: 7, Locations: []inventory.Location{}}
	inv := withContainers(base, at, &at)
	db := &inv.Locations[len(inv.Locations)-1].Packages[0]
	db.Updatable = []string{"stack/db", "stack/db-replica"}
	for _, node := range []string{"echo", "golf"} {
		if err := f.srv.Packages.SetInventory(node, inv); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.srv.Packages.SetInventory("foxtrot", withContainers(base, at, &at)); err != nil {
		t.Fatal(err)
	}

	v := f.view(t, "echo", f.now)
	if !slices.Equal(v.Capabilities.ImageUpdates, []string{"stack/db", "stack/db-replica"}) {
		t.Fatalf("capabilities %+v", v.Capabilities)
	}
	// The db image has a rebuild old enough and services to update; the app image only a newer tag, which stays manual.
	if c := v.Containers; c.Installable == nil || *c.Installable != 1 || *c.Ready != 1 {
		t.Fatalf("installable %v, ready %v", c.Installable, c.Ready)
	}
	if f := f.view(t, "foxtrot", f.now).Containers; f.Installable == nil || *f.Installable != 0 {
		t.Fatalf("foxtrot installable %v", f.Installable)
	}
	if u := v.Containers.Updates[0]; u.Image != "docker.io/stack/db" || !slices.Equal(u.Services, db.Updatable) {
		t.Fatalf("view of the image: %+v", u)
	}
	for _, c := range []struct {
		node, cmd string
		scope     string
		code      AdminCode
	}{
		{"echo", "install_images", "images", AdminOK},
		{"echo", "install_image:stack/db", "image:stack/db", AdminOK},
		{"echo", "install_image:stack/web", "", AdminNotAllowed},              // not reported as updatable
		{"echo", "install_image:stack/*", "", AdminBadRequest},                // a pattern is no service
		{"echo", "install_image:stack/db --pull always", "", AdminBadRequest}, // nothing but names
		{"echo", "install_image:", "", AdminBadRequest},
		{"foxtrot", "install_images", "", AdminNotAllowed},         // the agent offers no service
		{"foxtrot", "install_image:stack/db", "", AdminNotAllowed}, // ... not even one the other host has
		{"golf", "install_images", "", AdminVerbUnsupported},       // reported, but the agent installs nothing
		{"nobody", "install_images", "", AdminUnknownNode},
	} {
		verb, scope, code := f.srv.Resolve(c.node, c.cmd)
		if code != c.code || scope != c.scope || code == AdminOK && verb != protocol.VerbInstall {
			t.Errorf("%s %q: %s %q %s, want %q %s", c.node, c.cmd, verb, scope, code, c.scope, c.code)
		}
	}
	if v := f.view(t, "foxtrot", f.now); len(v.Capabilities.ImageUpdates) != 0 {
		t.Errorf("foxtrot offers %v", v.Capabilities.ImageUpdates)
	}
	if v := f.view(t, "golf", f.now); len(v.Capabilities.ImageUpdates) != 0 {
		t.Errorf("an agent without install offers %v", v.Capabilities.ImageUpdates)
	}
}
