//go:build windows

package agent

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/wire"
)

// TestWingetLive runs the whole way on a real Windows host: Home Assistant asks the hub to upgrade one app, the hub
// signs the job, the agent upgrades it with winget and Home Assistant sees the result and the shorter list. It
// changes the host, so it only runs when VORDR_WINGET_LIVE names the winget id of a package that has an upgrade
// pending and needs no elevation (for example jqlang.jq, with an older version installed for the test).
func TestWingetLive(t *testing.T) {
	pkg := os.Getenv("VORDR_WINGET_LIVE")
	if pkg == "" {
		t.Skip("VORDR_WINGET_LIVE not set: this test upgrades a real package")
	}
	h := startHub(t, 5)
	dir := t.TempDir()
	cfg, issues := policy.Parse([]byte("node: live-win\nstate_dir: '" + dir + "'\nhub: {url: '" + h.url +
		"', public_key: " + h.key.PublicB64() + ", ca_file: '" + h.caFile + "'}\n" +
		"apps: [{id: live, name: Live test, source: winget-package, package: '" + pkg + "'}]\n" +
		"install: {allowed_scopes: ['app:live'], default_scope: 'app:live', min_interval_h: 0, timeout_min: 20}\n"))
	if len(issues) > 0 {
		t.Fatalf("agent config: %v", issues)
	}
	id, _, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := facts.Collect(ctx, runner.Exec{})
	agent := NewClient(cfg, id, "live", host, System(host, true, cfg.Install.Mode), quiet)
	agent.Installer = SystemInstaller(host)
	if agent.Installer == nil {
		t.Fatal("no installer on this Windows host")
	}
	go agent.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "live-win") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "live-win", Fingerprint: agent.ID.Fingerprint()})

	ha, _ := dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil)
	h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp})
	listed := func(v wire.HostView) bool {
		return v.System != nil && slices.ContainsFunc(v.System.Packages, func(p wire.PackageView) bool { return p.Name == pkg })
	}
	var snap wire.Snapshot
	ha.until(wire.TSnapshot, &snap, nil)
	var v wire.HostView
	if len(snap.Hosts) == 1 {
		v = snap.Hosts[0]
	}
	if !listed(v) { // the agent's first report may come after the snapshot
		var hm wire.HostMsg
		ha.within(2*time.Minute, wire.THost, &hm, func() bool { return listed(hm.Host) })
		v = hm.Host
	}
	t.Logf("before: %s, %d pending, install offered: %v", v.System.InstalledVersion, *v.Pending, v.Capabilities.Install)

	res := ha.command("live-1", "live-win", "install_app:live")
	if res.Code != string(hub.AdminOK) || res.JobID == "" {
		t.Fatalf("install command: %+v", res)
	}
	var done wire.HostMsg
	ha.within(20*time.Minute, wire.THost, &done, func() bool {
		r := done.Host.LastRun
		return r != nil && r.JobID == res.JobID && r.Finished != nil
	})
	r := done.Host.LastRun
	t.Logf("run %s: %s, summary %+v", r.JobID, r.State, r.Summary)
	if r.State != string(protocol.CodeOK) && r.State != string(protocol.CodeOKRebootRequired) {
		t.Fatalf("run ended %s: %s", r.State, r.LogExcerpt)
	}
	if r.Summary == nil || !slices.Equal(r.Summary.Packages, []string{pkg}) {
		t.Fatalf("summary: %+v", r.Summary)
	}
	// The agent reports what is pending before it sends the result, so the finished run's view has the new list.
	if done.Host.Pending == nil || listed(done.Host) {
		t.Fatalf("still listed after the run: %+v", done.Host.System)
	}
	t.Logf("after: %d pending", *done.Host.Pending)
}
