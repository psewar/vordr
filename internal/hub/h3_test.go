package hub

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

func TestRebootPendingAndNoReport(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	reboot := &source.Reboot{Required: true, Reason: protocol.RebootKernel, Checked: []string{"kernel"}}
	f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, reboot))
	e, _ := f.srv.Reg.Get("h")
	if e.RebootSince == nil || !e.RebootSince.Equal(f.now) {
		t.Fatalf("reboot since: %v", e.RebootSince)
	}
	// A later report still needing it keeps the first time; one without an answering signal changes nothing.
	f.srv.Reg.SetReport("h", aptReport(f.now.Add(time.Hour), 0, 0, reboot))
	f.srv.Reg.SetReport("h", aptReport(f.now.Add(2*time.Hour), 0, 0, &source.Reboot{Checked: []string{}}))
	e, _ = f.srv.Reg.Get("h")
	if !e.RebootSince.Equal(f.now) {
		t.Fatalf("reboot since moved: %v", e.RebootSince)
	}

	f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, reboot)) // last report back at f.now
	if v := f.view(t, "h", f.now.Add(6*24*time.Hour)); v.Reboot.Since == nil ||
		(v.Problem != nil && slices.Contains(v.Problem.Reasons, ReasonRebootPending)) {
		t.Fatalf("6 days: %+v %+v", v.Reboot, v.Problem)
	}
	if v := f.view(t, "h", f.now.Add(8*24*time.Hour)); v.Problem == nil || !slices.Contains(v.Problem.Reasons, ReasonRebootPending) {
		t.Fatalf("8 days: %+v", v.Problem)
	}

	f.srv.Reg.SetReport("h", aptReport(f.now, 0, 0, &source.Reboot{Required: false, Checked: []string{"kernel"}}))
	if e, _ := f.srv.Reg.Get("h"); e.RebootSince != nil {
		t.Fatal("rebooted host still counted")
	}
	// Not connected in this fixture: no_report needs the host online, offline covers the rest.
	if v := f.view(t, "h", f.now.Add(30*time.Hour)); v.Problem != nil && slices.Contains(v.Problem.Reasons, ReasonNoReport) {
		t.Fatalf("no_report for a host that is offline: %+v", v.Problem)
	}
}

func TestParseConfigLimitsAndRemovedMQTT(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cert, []byte("x"), 0o600)
	os.WriteFile(key, []byte("x"), 0o600)
	base := "tls_cert: " + cert + "\ntls_key: " + key + "\n"
	cfg, issues := ParseConfig([]byte(base))
	if len(issues) > 0 || cfg.RebootPendingAfterDays != 7 || cfg.NoReportAfterHours != 26 {
		t.Fatalf("defaults: %+v %v", cfg, issues)
	}
	if _, issues := ParseConfig([]byte(base + "no_report_after_hours: 3\n")); len(issues) != 1 {
		t.Fatalf("below the check interval: %v", issues)
	}
	// An old hub.yaml with MQTT is told what to do instead of failing on an unknown key.
	_, issues = ParseConfig([]byte(base + "mqtt:\n  url: mqtts://broker:8883\n"))
	if len(issues) != 1 || !strings.Contains(issues[0], "/v1/ha") {
		t.Fatalf("mqtt section: %v", issues)
	}
}
