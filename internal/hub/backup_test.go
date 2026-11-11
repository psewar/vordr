package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// fakePBS answers the groups call of a datastore; down makes it fail, token is the one it accepts.
type fakePBS struct {
	srv   *httptest.Server
	down  atomic.Bool
	calls atomic.Int32
	last  map[string]int64 // "vm/301" -> epoch
}

func newFakePBS(t *testing.T, last map[string]int64) *fakePBS {
	t.Helper()
	f := &fakePBS{last: last}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/api2/json/admin/datastore/pbs-store/groups" ||
			r.Header.Get("Authorization") != "PBSAPIToken=vordr@pbs!hub:secret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var parts []string
		for g, at := range f.last {
			typ, id, _ := strings.Cut(g, "/")
			parts = append(parts, fmt.Sprintf(`{"backup-type":%q,"backup-id":%q,"last-backup":%d}`, typ, id, at))
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(parts, ","))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePBS) fingerprint() string {
	sum := sha256.Sum256(f.srv.Certificate().Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var pairs []string
	for i := 0; i < len(h); i += 2 {
		pairs = append(pairs, h[i:i+2])
	}
	return strings.Join(pairs, ":")
}

func backupConfig(t *testing.T, f *fakePBS) BackupConfig {
	t.Helper()
	token := filepath.Join(t.TempDir(), "pbs-token")
	if err := os.WriteFile(token, []byte("vordr@pbs!hub:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return BackupConfig{PBSURL: f.srv.URL, Datastore: "pbs-store", Fingerprint: f.fingerprint(), TokenFile: token,
		MaxAgeHours: 26, Guests: []BackupGuest{{Node: "golf", Group: "vm/301"}, {Node: "foxtrot", Group: "ct/302"},
			{Node: "charlie", Without: "physical host, no guest backup"}, {Node: "new", Group: "vm/999"}}}
}

func TestBackupGate(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	f := newFakePBS(t, map[string]int64{"vm/301": now.Add(-time.Hour).Unix(), "ct/302": now.Add(-30 * time.Hour).Unix()})
	b, err := NewBackups(backupConfig(t, f), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := []struct {
		node  string
		code  AdminCode
		state string
	}{
		{"golf", AdminOK, BackupOK},
		{"foxtrot", AdminBackupStale, BackupStale},
		{"charlie", AdminOK, BackupExempt},
		{"new", AdminBackupMissing, BackupMissing},
		{"unknown", AdminBackupUnmapped, BackupUnmapped},
	}
	for _, c := range cases {
		code, v := b.Gate(ctx, c.node, nil)
		if code != c.code || v.State != c.state {
			t.Errorf("%s: %s %+v, want %s %s", c.node, code, v, c.code, c.state)
		}
	}
	if v := b.View("charlie", now, nil); v.Exempt != "physical host, no guest backup" {
		t.Errorf("exempt reason: %+v", v)
	}
	if v := b.View("golf", now, nil); v.Last == nil || !v.Last.Equal(now.Add(-time.Hour)) || v.Group != "vm/301" {
		t.Errorf("golf view: %+v", v)
	}

	// PBS down: a fetch from minutes ago still counts, one older than the grace does not.
	f.down.Store(true)
	now = now.Add(5 * time.Minute)
	if code, _ := b.Gate(ctx, "golf", nil); code != AdminOK {
		t.Errorf("PBS briefly down, recent fetch: %s", code)
	}
	now = now.Add(backupCacheGrace)
	if code, v := b.Gate(ctx, "golf", nil); code != AdminBackupUnreachable || v.State != BackupUnreachable || b.Failure() == "" {
		t.Errorf("PBS down too long: %s %+v %q", code, v, b.Failure())
	}
}

// A command to install is refused before any job goes out when the host's backup is not fresh; with a fresh one it
// goes on to the next check (here: the host is not connected). A check needs no backup.
func TestInstallWaitsForABackup(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "golf", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall},
		wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all"}, true)
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	pbs := newFakePBS(t, map[string]int64{"vm/301": f.now.Add(-30 * time.Hour).Unix()})
	b, err := NewBackups(backupConfig(t, pbs), func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Backups = b
	sess := &haSession{name: "bravo", fingerprint: fp}
	install := wire.Command{T: wire.TCommand, RequestID: "r1", Node: "golf", Command: "install"}
	if res := f.srv.haCommand(sess, install); res.Code != string(AdminBackupStale) {
		t.Fatalf("install with a 30 h old backup: %+v", res)
	}
	if v := f.view(t, "golf", f.now).Backup; v == nil || v.State != BackupStale || v.MaxAgeHours != 26 {
		t.Fatalf("view: %+v", v)
	}
	check := wire.Command{T: wire.TCommand, RequestID: "r2", Node: "golf", Command: "check"}
	if res := f.srv.haCommand(sess, check); res.Code != string(AdminNotConnected) {
		t.Fatalf("a check waited for a backup: %+v", res)
	}
	pbs.last["vm/301"] = f.now.Add(-time.Hour).Unix()
	if res := f.srv.haCommand(sess, install); res.Code != string(AdminNotConnected) {
		t.Fatalf("install with a fresh backup: %+v", res)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastRun != nil {
		t.Fatalf("a refused install recorded a run: %+v", e.LastRun)
	}
	// The same decision for the operator, without a job.
	if r := f.srv.Admin(AdminRequest{Cmd: "backup", Node: "golf"}); r.Code != AdminOK || r.Backup.State != BackupOK {
		t.Fatalf("admin backup: %+v", r)
	}
	f.srv.Backups = nil
	if r := f.srv.Admin(AdminRequest{Cmd: "backup", Node: "golf"}); r.Code != AdminBackupCheckOff {
		t.Fatalf("admin backup without a check: %+v", r)
	}
}

// Only the pinned certificate counts; another PBS (or a man in the middle) is not asked.
func TestBackupPinsTheCertificate(t *testing.T) {
	f := newFakePBS(t, map[string]int64{"vm/301": time.Now().Unix()})
	cfg := backupConfig(t, f)
	cfg.Fingerprint = strings.Repeat("AB:", 31) + "AB"
	b, err := NewBackups(cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("refresh against a foreign certificate: %v", err)
	}
}

func TestBackupConfigIssues(t *testing.T) {
	f := newFakePBS(t, nil)
	good := backupConfig(t, f)
	if issues := good.check(func(string) bool { return true }); len(issues) != 0 {
		t.Fatalf("good config: %v", issues)
	}
	bad := good
	bad.PBSURL, bad.Fingerprint, bad.MaxAgeHours = "http://pbs/x", "nope", 0
	bad.Guests = []BackupGuest{{Node: "a", Group: "vm/1", Without: "both"}, {Node: "b"}, {Node: "c", Group: "lxc/7"},
		{Node: "a", Group: "vm/2"}, {Node: "d", Group: "vm/3", ConfigBackup: true},
		{Node: "e", Without: "none", ConfigBackup: true}}
	issues := strings.Join(bad.check(func(string) bool { return false }), "\n")
	for _, want := range []string{"pbs_url", "fingerprint", "token_file", "max_age_hours", "guests[0]: give exactly one",
		"guests[1]: give exactly one", "guests[2].group", "guests[3].node", "guests[4]: give exactly one",
		"guests[5]: give exactly one"} {
		if !strings.Contains(issues, want) {
			t.Errorf("missing issue %q in:\n%s", want, issues)
		}
	}

	appliance := good
	appliance.Guests = append(slices.Clone(good.Guests), BackupGuest{Node: "nas", ConfigBackup: true})
	if issues := appliance.check(func(string) bool { return true }); len(issues) != 0 {
		t.Fatalf("config_backup alone: %v", issues)
	}

	fresh := good
	fresh.BeforeInstall = &BeforeInstall{Hypervisor: "india"}
	if issues := fresh.check(func(string) bool { return true }); len(issues) != 0 {
		t.Fatalf("before_install: %v", issues)
	}
	for hv, want := range map[string]string{"": "not a node name", "-x": "not a node name",
		"golf": "would back up itself"} { // golf is vm/301 in the config
		fresh.BeforeInstall = &BeforeInstall{Hypervisor: hv}
		if issues := strings.Join(fresh.check(func(string) bool { return true }), "\n"); !strings.Contains(issues, want) {
			t.Errorf("hypervisor %q: %q, want %q", hv, issues, want)
		}
	}
}

// With before_install a guest of the hypervisor gets a fresh backup first (ADR-0096): the age of its last backup no
// longer stops the install, but PBS must answer. Hosts without a vm/ or ct/ group keep the age check.
func TestBackupBeforeInstallGate(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	f := newFakePBS(t, map[string]int64{"vm/301": now.Add(-time.Hour).Unix(), "ct/302": now.Add(-30 * time.Hour).Unix(),
		"host/pve": now.Add(-30 * time.Hour).Unix()})
	cfg := backupConfig(t, f)
	cfg.Guests = append(cfg.Guests, BackupGuest{Node: "pve", Group: "host/pve"})
	cfg.BeforeInstall = &BeforeInstall{Hypervisor: "india"}
	b, err := NewBackups(cfg, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for node, want := range map[string]struct {
		fresh string
		code  AdminCode
		state string
	}{
		"foxtrot": {"ct/302", AdminOK, BackupStale},    // 30 h old, but backed up right before
		"new":     {"vm/999", AdminOK, BackupMissing},  // its first backup comes before its first install
		"golf":    {"vm/301", AdminOK, BackupOK},       // fresh anyway
		"pve":     {"", AdminBackupStale, BackupStale}, // host/ group: vzdump cannot make it, the age check stays
		"charlie": {"", AdminOK, BackupExempt},         // without
		"unknown": {"", AdminBackupUnmapped, BackupUnmapped},
	} {
		code, v := b.Gate(ctx, node, nil)
		hv := ""
		if want.fresh != "" {
			hv = "india"
		}
		if b.FreshFor(node) != want.fresh || code != want.code || v.State != want.state || v.BeforeInstall != hv {
			t.Errorf("%s: fresh %q, %s, %+v; want %+v", node, b.FreshFor(node), code, v, want)
		}
	}
	f.down.Store(true)
	now = now.Add(backupCacheGrace + time.Minute)
	if code, _ := b.Gate(ctx, "foxtrot", nil); code != AdminBackupUnreachable {
		t.Errorf("PBS down: %s - the fresh backup could not be checked", code)
	}
}

// An appliance with config_backup (ADR-0090) is decided on the backup time its proxy agent reports, with the same
// max_age_hours and codes; PBS is not asked, so PBS being down neither blocks nor passes it. Before_install never
// applies: there is no guest to back up.
func TestBackupConfigBackupGate(t *testing.T) {
	now := time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)
	f := newFakePBS(t, map[string]int64{"vm/301": now.Add(-time.Hour).Unix()})
	cfg := backupConfig(t, f)
	cfg.Guests = append(cfg.Guests, BackupGuest{Node: "nas", ConfigBackup: true})
	cfg.BeforeInstall = &BeforeInstall{Hypervisor: "india"}
	b, err := NewBackups(cfg, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f.down.Store(true) // never fetched: a PBS guest would be unreachable
	ctx := context.Background()
	at := func(age time.Duration) *time.Time { a := now.Add(-age); return &a }
	for name, c := range map[string]struct {
		configAt *time.Time
		code     AdminCode
		state    string
	}{
		"fresh":      {at(2 * time.Hour), AdminOK, BackupOK},
		"at the age": {at(26 * time.Hour), AdminOK, BackupOK},
		"stale":      {at(27 * time.Hour), AdminBackupStale, BackupStale},
		"none":       {nil, AdminBackupMissing, BackupMissing},
	} {
		calls := f.calls.Load()
		code, v := b.Gate(ctx, "nas", c.configAt)
		if code != c.code || v.State != c.state || !v.ConfigBackup || v.Group != "" || v.BeforeInstall != "" ||
			v.Last != c.configAt {
			t.Errorf("%s: %s %+v, want %s %s", name, code, v, c.code, c.state)
		}
		if f.calls.Load() != calls {
			t.Errorf("%s: the gate asked PBS for an appliance", name)
		}
	}
	if b.FreshFor("nas") != "" {
		t.Error("an appliance would get a guest backup before its install")
	}
	// A reported time does not stand in for a PBS guest, nor for a host without an entry.
	if code, v := b.Gate(ctx, "golf", at(time.Hour)); code != AdminBackupUnreachable || v.ConfigBackup {
		t.Errorf("PBS guest with a reported config backup: %s %+v", code, v)
	}
	if code, _ := b.Gate(ctx, "unknown", at(time.Hour)); code != AdminBackupUnmapped {
		t.Errorf("unmapped host with a reported config backup: %s", code)
	}
}

func TestReportedConfigBackup(t *testing.T) {
	rep := func(sources ...source.Report) *wire.Report { return &wire.Report{Sources: sources} }
	firmware := source.Report{Source: "synology-dsm", Facts: map[string]string{source.FactConfigBackupAt: "2023-11-14T21:13:20Z"}}
	want := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	got := ReportedConfigBackup(rep(firmware, source.Report{Source: source.ConfigBackup,
		Facts: map[string]string{source.FactConfigBackupAt: "2023-11-14T17:13:20-05:00"}}))
	if got == nil || !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("reported: %v, want %v", got, want)
	}
	for name, r := range map[string]*wire.Report{
		"no report":     nil,
		"other source":  rep(firmware), // the fact counts only in the config-backup entry
		"no export yet": rep(source.Report{Source: source.ConfigBackup, Facts: map[string]string{}}),
		"not RFC 3339": rep(source.Report{Source: source.ConfigBackup,
			Facts: map[string]string{source.FactConfigBackupAt: "14.11.2023"}}),
		"nil facts": rep(source.Report{Source: source.ConfigBackup}),
	} {
		if got := ReportedConfigBackup(r); got != nil {
			t.Errorf("%s: %v, want none", name, got)
		}
	}
}

// End to end in the hub: the host view and the install gate take the time from the host's last report.
func TestConfigBackupFromTheReport(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "nas", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall},
		wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all"}, true)
	pbs := newFakePBS(t, nil)
	cfg := backupConfig(t, pbs)
	cfg.Guests = append(cfg.Guests, BackupGuest{Node: "nas", ConfigBackup: true})
	b, err := NewBackups(cfg, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Backups = b
	if v := f.view(t, "nas", f.now).Backup; v == nil || v.State != BackupMissing || !v.ConfigBackup {
		t.Fatalf("before any report: %+v", v)
	}
	if code, _ := f.srv.backupGate(context.Background(), "nas"); code != AdminBackupMissing {
		t.Fatalf("gate before any report: %s", code)
	}
	at := f.now.Add(-3 * time.Hour).UTC().Truncate(time.Second) // RFC 3339 as the agent writes it: whole seconds
	if err := f.srv.Reg.SetReport("nas", wire.Report{T: wire.TReport, CheckedAt: f.now, Sources: []source.Report{{
		Source: source.ConfigBackup, State: protocol.SourceUpToDate, CheckedAt: f.now,
		Facts: map[string]string{source.FactConfigBackupAt: at.Format(time.RFC3339)}}}}); err != nil {
		t.Fatal(err)
	}
	if v := f.view(t, "nas", f.now).Backup; v == nil || v.State != BackupOK || v.Last == nil || !v.Last.Equal(at) {
		t.Fatalf("after the report: %+v", v)
	}
	if code, _ := f.srv.backupGate(context.Background(), "nas"); code != AdminOK {
		t.Fatalf("gate after the report: %s", code)
	}
}

// The hub restarted while an install waited for its backup: nobody sends it any more, so the run ends - nothing
// was installed. Other runs stay as they are.
func TestOrphanedBackupEndsOnStart(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "g", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetRun("g", Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all", Phase: protocol.PhaseBackingUp,
		Started: f.now})
	_ = f.srv.Reg.SetRun("h", Run{JobID: "j2", Verb: protocol.VerbInstall, Scope: "all", Phase: protocol.PhaseInstalling,
		Started: f.now})
	srv := NewServer(f.srv.Reg, f.srv.Clients, f.srv.Events, f.srv.Key, 5, f.srv.Log)
	if e, _ := srv.Reg.Get("g"); e.LastRun.Code != protocol.CodeInterrupted || e.LastRun.Phase != "" ||
		!strings.Contains(e.LastRun.Detail, "nothing was installed") {
		t.Fatalf("orphaned install: %+v", e.LastRun)
	}
	if e, _ := srv.Reg.Get("h"); e.LastRun.Code != "" || e.LastRun.Phase != protocol.PhaseInstalling {
		t.Fatalf("a running install was touched: %+v", e.LastRun)
	}
}

// While the hypervisor backs up a guest, a command for the hypervisor itself waits and starts afterwards.
func TestHypervisorCommandWaitsForABackup(t *testing.T) {
	f, sess := queueFixture(t)
	fin := f.now
	_ = f.srv.Reg.SetRun("h", Run{JobID: "j1", Verb: protocol.VerbCheck, Code: protocol.CodeOK, Started: f.now,
		Finished: &fin})
	f.srv.setBackingUp("h", true)
	res := f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r1", Node: "h", Command: "install"})
	if res.Code != string(AdminOK) || res.JobID != "" || !slices.Equal(f.srv.Queued("h"), []string{"install:all"}) {
		t.Fatalf("command during a backup: %+v, queued %v", res, f.srv.Queued("h"))
	}
	f.srv.setBackingUp("h", false)
	if f.srv.running("h") {
		t.Fatal("still running after the backup")
	}
}
