package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/target"
	"github.com/psewar/vordr/internal/wire"
)

type stubDriver struct {
	status    target.Status
	check     target.Result
	backup    target.Result
	exports   int
	exportLen int
}

func (s *stubDriver) Name() string { return "synology-dsm" }
func (s *stubDriver) Check(context.Context) (target.Status, target.Result) {
	return s.status, s.check
}
func (s *stubDriver) Backup(context.Context) ([]byte, string, target.Result) {
	if !s.backup.OK() {
		return nil, ".dss", s.backup
	}
	s.exports++
	return make([]byte, s.exportLen), ".dss", target.Result{}
}

func reportOf(t *testing.T, r wire.Report) (fw, bk source.Report) {
	t.Helper()
	if len(r.Sources) != 2 || r.Sources[1].Source != ConfigBackupSource || r.Reboot != nil {
		t.Fatalf("report: %+v", r)
	}
	return r.Sources[0], r.Sources[1]
}

// The report of an appliance: its firmware as one source, the freshness of its configuration backup as another;
// exports are renewed daily, not on every check, and a backup older than max_age_h is a problem (ADR-0090).
func TestTargetChecker(t *testing.T) {
	d := &stubDriver{exportLen: 2048, status: target.Status{Product: "Synology DSM", Model: "DS-TEST",
		Installed: "7.9.9-99999 Update 1", SelfUpdate: "on (hotfix-security)", Facts: map[string]string{"update_check": "hotfix"}}}
	now := time.Unix(1700000000, 0).UTC()
	clock := func() time.Time { return now }
	c := TargetChecker(d, target.Store{Dir: filepath.Join(t.TempDir(), "config-backups"), Keep: 5}, 26*time.Hour, clock)

	fw, bk := reportOf(t, c.Check(context.Background()))
	if fw.State != protocol.SourceUpToDate || fw.Source != "synology-dsm" || fw.Facts["self_update"] != "on (hotfix-security)" ||
		fw.Facts["model"] != "DS-TEST" || fw.Facts["update_check"] != "hotfix" {
		t.Fatalf("firmware: %+v", fw)
	}
	if bk.State != protocol.SourceUpToDate || bk.Facts["config_backup_at"] != "2023-11-14T22:13:20Z" || d.exports != 1 {
		t.Fatalf("first check exports: %+v, exports %d", bk, d.exports)
	}

	now = now.Add(20 * time.Hour) // still fresh: no new export
	if _, bk = reportOf(t, c.Check(context.Background())); d.exports != 1 || bk.State != protocol.SourceUpToDate {
		t.Fatalf("within a day: exports %d, %+v", d.exports, bk)
	}
	now = now.Add(5 * time.Hour) // 25 h: older than max_age less the margin, renewed
	if _, bk = reportOf(t, c.Check(context.Background())); d.exports != 2 || bk.Facts["config_backup_at"] != "2023-11-15T23:13:20Z" {
		t.Fatalf("after 25 h: exports %d, %+v", d.exports, bk)
	}

	d.status.Offers = []target.Offer{{Name: "DSM", Version: "DSM 7.9.9-99999 Update 2", Security: true},
		{Name: "DSM (new major version)", Version: target.UnknownVersion}}
	fw, _ = reportOf(t, c.Check(context.Background()))
	if fw.State != protocol.SourceUpdatesAvailable || fw.Pending != 2 || fw.Security != 1 || len(fw.Packages) != 2 ||
		fw.Packages[0].From != "7.9.9-99999 Update 1" || fw.Packages[1].To != target.UnknownVersion {
		t.Fatalf("offers: %+v", fw)
	}

	// The device refuses the login: the firmware entry says why, no export is tried, the last one still counts.
	d.check = target.Result{Code: protocol.CodeFailedTargetAuth, Detail: "DSM error 400"}
	now = now.Add(25 * time.Hour)
	fw, bk = reportOf(t, c.Check(context.Background()))
	if fw.State != protocol.SourceCheckFailed || fw.Code != protocol.CodeFailedTargetAuth || d.exports != 2 {
		t.Fatalf("refused login: %+v, exports %d", fw, d.exports)
	}
	if bk.State != protocol.SourceUpToDate || bk.Detail == "" {
		t.Fatalf("last export 25 h old is still within max_age: %+v", bk)
	}
	now = now.Add(2 * time.Hour) // 27 h: no export within max_age any more
	if _, bk = reportOf(t, c.Check(context.Background())); bk.State != protocol.SourceCheckFailed ||
		bk.Code != protocol.CodeFailedConfigBackup || bk.Facts["config_backup_at"] == "" {
		t.Fatalf("stale backup must be a problem: %+v", bk)
	}

	// The device answers again, but the export fails: still a problem, with the driver's reason.
	d.check = target.Result{}
	d.backup = target.Result{Code: protocol.CodeFailedConfigBackup, Detail: "export of 100 bytes is not xz"}
	if _, bk = reportOf(t, c.Check(context.Background())); bk.Code != protocol.CodeFailedConfigBackup ||
		bk.Detail != "export of 100 bytes is not xz" {
		t.Fatalf("failed export: %+v", bk)
	}
}

func TestTargetCheckerWithoutAnyBackup(t *testing.T) {
	d := &stubDriver{check: target.Result{Code: protocol.CodePrecheckRepoUnreachable, Detail: "connection refused"}}
	c := TargetChecker(d, target.Store{Dir: t.TempDir(), Keep: 5}, 26*time.Hour, time.Now)
	fw, bk := reportOf(t, c.Check(context.Background()))
	if fw.Code != protocol.CodePrecheckRepoUnreachable || bk.Code != protocol.CodeFailedConfigBackup ||
		bk.Facts["config_backup_at"] != "" || d.exports != 0 {
		t.Fatalf("unreachable, never backed up: %+v %+v", fw, bk)
	}
}

func TestTargetFacts(t *testing.T) {
	d := &stubDriver{status: target.Status{Product: "Synology DSM", Model: "DS-TEST", Installed: "7.9.9-99999 Update 1"}}
	if h := TargetFacts(context.Background(), "delta", d); h.OS != "Synology DSM 7.9.9-99999 Update 1" ||
		h.OSID != "synology-dsm" || h.Arch != "DS-TEST" || h.Hostname != "delta" || h.InContainer() {
		t.Fatalf("facts: %+v", h)
	}
	d.check = target.Result{Code: protocol.CodeFailedTargetAuth}
	if h := TargetFacts(context.Background(), "delta", d); h.OS != "synology-dsm" || h.OSVersion != "unknown" {
		t.Fatalf("facts without an answer: %+v", h)
	}
}
