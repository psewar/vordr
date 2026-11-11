package agent

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/images"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

// fakeImages stands for images.Updater: it reports what the agent handed it and returns a set result.
type fakeImages struct {
	change  bool // gets as far as a change: calls BeforeChange
	res     images.UpdateResult
	calls   int
	scope   protocol.Scope
	health  string // what the Health hook said
	journal string // the run journal right after BeforeChange
	path    string
}

func (f *fakeImages) Update(ctx context.Context, scope protocol.Scope, h images.UpdateHooks) images.UpdateResult {
	f.calls++
	f.scope = scope
	h.Progress(protocol.PhasePrechecking, 5)
	if f.change {
		if err := h.BeforeChange(); err != nil {
			return images.UpdateResult{Code: protocol.CodeFailedPackageManager, Detail: err.Error()}
		}
		raw, _ := os.ReadFile(f.path)
		f.journal = string(raw)
	}
	f.health = h.Health(ctx)
	return f.res
}

func imageClient(t *testing.T, scopes []string, f *fakeImages) *Client {
	t.Helper()
	cfg := policy.Config{Node: "host-a", StateDir: t.TempDir(),
		Install: policy.Install{AllowedScopes: scopes, MinIntervalH: 6, TimeoutMin: 60}}
	c := NewClient(cfg, nil, "test", facts.Host{}, &fakeChecker{}, quiet)
	if f != nil {
		f.path = c.journalPath()
		c.Images = f
	}
	return c
}

// ADR-0119: image scopes go to the image updater, under the policy's image entries, the window and the one-run lock;
// never to the system's installer.
func TestImageRunGates(t *testing.T) {
	c := imageClient(t, nil, nil)
	if v := c.Verbs(); !slices.Equal(v, []protocol.Verb{protocol.VerbCheck}) {
		t.Fatalf("verbs without anything to install: %v", v)
	}
	if code, _, _, _ := c.installRun(installJob("j1", "image:stack/web")); code != protocol.CodeRejectedNotAllowed {
		t.Errorf("no image entries: %s", code)
	}
	if NewImageUpdates(policy.Config{Packages: policy.Packages{Containers: true}}) != nil {
		t.Error("NewImageUpdates without image entries is not nil")
	}
	if NewImageUpdates(policy.Config{Install: policy.Install{AllowedScopes: []string{"image:stack/*"}}}) != nil {
		t.Error("NewImageUpdates without packages.containers is not nil")
	}
	if NewImageUpdates(policy.Config{Install: policy.Install{AllowedScopes: []string{"image:stack/*"}},
		Packages: policy.Packages{Containers: true}}) == nil {
		t.Error("NewImageUpdates with an image entry and containers is nil")
	}
	f := &fakeImages{res: images.UpdateResult{Code: protocol.CodeOK}}
	c = imageClient(t, []string{"image:stack/*"}, f)
	if v := c.Verbs(); !slices.Equal(v, []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}) {
		t.Fatalf("verbs with image updates only: %v", v)
	}
	inst := &fakeInstaller{}
	c.Installer = inst
	if code, _, _, _ := c.installRun(installJob("j1", "image:other/app")); code != protocol.CodeRejectedScopeNotAllowed ||
		f.calls != 0 {
		t.Errorf("a service the policy does not name: %s, updater called %d", code, f.calls)
	}
	for _, scope := range []string{"image:stack/web", "images"} {
		if code, _, _, _ := c.installRun(installJob("j1", scope)); code != protocol.CodeOK {
			t.Errorf("%s: %s", scope, code)
		}
	}
	if f.calls != 2 || f.scope.Kind != protocol.ScopeImages || inst.calls != 0 {
		t.Errorf("updater %d calls (last %+v), installer %d", f.calls, f.scope, inst.calls)
	}
	closed := closedWindow()
	c.Cfg.Install.Window = &closed
	if code, _, _, _ := c.installRun(installJob("j1", "images")); code != protocol.CodeRejectedOutsideWindow {
		t.Errorf("window closed: %s", code)
	}
	asked := installJob("j1", "images")
	asked.OutsideWindow = "ops: now"
	if code, _, _, _ := c.installRun(asked); code != protocol.CodeOK {
		t.Errorf("window closed, with a reason: %s", code)
	}
	c.Cfg.Install.Window = nil
	c.checkMu.Lock()
	code, _, _, _ := c.installRun(installJob("j1", "images"))
	c.checkMu.Unlock()
	if code != protocol.CodeRejectedBusy {
		t.Errorf("another run: %s", code)
	}
	// Agent-wide: the system's min_interval_h does not hold an image update back.
	c.lastInstall = c.Now()
	if code, _, _, _ := c.installRun(installJob("j1", "images")); code != protocol.CodeOK {
		t.Errorf("right after another install: %s", code)
	}
}

// A run that changes something is journaled before the change; its result, with the services and the log, goes
// through the journal. agent.yaml health is what the updater's Health hook runs.
func TestImageRunJournalsAndSummarizes(t *testing.T) {
	f := &fakeImages{change: true, res: images.UpdateResult{Code: protocol.CodeOK, Changed: []string{"stack/web"},
		Log: []byte("$ docker compose ... up\n Container stack-web-1 Recreated\n")}}
	c := imageClient(t, []string{"image:stack/*"}, f)
	c.Health = testHealth([]policy.Check{{Systemd: "web.service"}},
		&scriptedRunner{answers: map[string][]string{"systemctl web.service": {"failed"}}}, 0)
	var session, bringer outbox
	c.approvedSession(session.send)
	c.install(installJob("j1", "image:stack/web"), bringer.send)
	if len(bringer.sent) != 0 {
		t.Fatalf("a journaled run answered over the session that brought it: %+v", bringer.sent)
	}
	if !strings.Contains(f.journal, `"j1"`) {
		t.Fatalf("no journal when the updater changed something: %q", f.journal)
	}
	if !strings.Contains(f.health, "web.service: failed") {
		t.Errorf("health hook said %q", f.health)
	}
	fin := session.final()
	if fin.Code != protocol.CodeOK || fin.Summary == nil || fin.Summary.PackageCount != 1 ||
		!slices.Equal(fin.Summary.Packages, []string{"stack/web"}) || fin.Summary.LogFile == "" {
		t.Fatalf("final %+v summary %+v", fin, fin.Summary)
	}
	if raw, err := os.ReadFile(fin.Summary.LogFile); err != nil || !strings.Contains(string(raw), "Recreated") {
		t.Errorf("run log %q %v", raw, err)
	}
	if c.lastInstallAt() == nil {
		t.Error("an image run that changed something does not count as an install run")
	}

	// A refusal before any change is answered where it came from, with no journal.
	r := &fakeImages{res: images.UpdateResult{Code: protocol.CodePrecheckVersionTooNew, Detail: "too new"}}
	c = imageClient(t, []string{"image:stack/*"}, r)
	var out outbox
	c.install(installJob("j2", "image:stack/web"), out.send)
	if st := out.final(); st.Code != protocol.CodePrecheckVersionTooNew || st.Detail != "too new" || st.Summary != nil {
		t.Fatalf("refusal: %+v", out.sent)
	}
	if _, ok := c.readJournal(); ok {
		t.Error("a refusal left a journal")
	}
	if r.health != "" {
		t.Errorf("health without checks: %q", r.health)
	}
}
