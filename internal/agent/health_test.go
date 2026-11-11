package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// scriptedRunner answers systemctl and docker from a table; each call takes the next answer for that command.
type scriptedRunner struct{ answers map[string][]string }

func (r *scriptedRunner) Run(_ context.Context, _ []string, name string, args ...string) runner.Result {
	key := name + " " + args[len(args)-1]
	out := r.answers[key]
	if len(out) == 0 {
		return runner.Result{ExitCode: 1}
	}
	a := out[0]
	if len(out) > 1 {
		r.answers[key] = out[1:]
	}
	return runner.Result{Stdout: []byte(a + "\n")}
}

func testHealth(checks []policy.Check, r *scriptedRunner, status int) *Health {
	return &Health{Checks: checks, Run: r, Wait: 50 * time.Millisecond, Every: time.Millisecond,
		Get: func(context.Context, string) (int, error) {
			if status == 0 {
				return 0, errors.New("connection refused")
			}
			return status, nil
		}}
}

var appChecks = []policy.Check{{Systemd: "app-watchdog.timer"}, {Docker: "web"}, {HTTP: "https://127.0.0.1:8443/api/version"}}

// A container that is still starting after the upgrade gets time; once healthy, all is well.
func TestHealthWaitsForServicesToComeBack(t *testing.T) {
	r := &scriptedRunner{answers: map[string][]string{
		"systemctl app-watchdog.timer": {"active"},
		"docker web":                   {"running starting", "running starting", "running healthy"},
	}}
	if failed := testHealth(appChecks, r, 200).Settle(context.Background()); failed != "" {
		t.Fatalf("all came back: %q", failed)
	}
}

// What still fails when the wait is up is named, check by check.
func TestHealthNamesWhatFails(t *testing.T) {
	r := &scriptedRunner{answers: map[string][]string{
		"systemctl app-watchdog.timer": {"failed"},
		"docker web":                   {"exited "},
	}}
	failed := testHealth(appChecks, r, 503).Settle(context.Background())
	for _, want := range []string{"systemd app-watchdog.timer: failed", "docker web: exited",
		"http https://127.0.0.1:8443/api/version: HTTP 503"} {
		if !strings.Contains(failed, want) {
			t.Fatalf("%q missing in %q", want, failed)
		}
	}
	r = &scriptedRunner{answers: map[string][]string{}}
	failed = testHealth([]policy.Check{{Docker: "gone"}, {HTTP: "http://127.0.0.1:1/"}}, r, 0).Settle(context.Background())
	if !strings.Contains(failed, "docker gone: not found") || !strings.Contains(failed, "no answer") {
		t.Fatalf("missing container, no answer: %q", failed)
	}
}

// A run that went through but leaves a check failing ends failed_health, with the check in the result.
func TestInstallEndsFailedHealth(t *testing.T) {
	inst := &fakeInstaller{change: true, result: source.InstallResult{Code: protocol.CodeOK, Planned: 1, Packages: []string{"docker-ce"}}}
	c := installClient(t, inst, allowAll)
	c.Health = testHealth([]policy.Check{{Docker: "web"}}, &scriptedRunner{answers: map[string][]string{
		"docker web": {"restarting "}}}, 200)
	var session outbox
	c.approvedSession(session.send)
	c.install(installJob("j1", "all"), session.send)
	fin := session.final()
	if fin.Code != protocol.CodeFailedHealth || !strings.Contains(fin.Detail, "docker web: restarting") ||
		fin.Summary == nil || fin.Summary.PackageCount != 1 {
		t.Fatalf("result: %+v", fin)
	}
	// Without health checks, or when they pass, the result stays ok.
	inst2 := &fakeInstaller{change: true, result: source.InstallResult{Code: protocol.CodeOK, Planned: 1}}
	c2 := installClient(t, inst2, allowAll)
	c2.Health = testHealth([]policy.Check{{Docker: "web"}}, &scriptedRunner{answers: map[string][]string{
		"docker web": {"running healthy"}}}, 200)
	var s2 outbox
	c2.approvedSession(s2.send)
	c2.install(installJob("j2", "all"), s2.send)
	if s2.final().Code != protocol.CodeOK {
		t.Fatalf("checks pass: %+v", s2.final())
	}
}
