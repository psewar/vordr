package agent

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// testPBS answers the groups call of datastore "store" with the newest backup per group.
type testPBS struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last map[string]time.Time
}

func newTestPBS(t *testing.T, last map[string]time.Time) *testPBS {
	t.Helper()
	p := &testPBS{last: last}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/admin/datastore/store/groups" || r.Header.Get("Authorization") != "PBSAPIToken=t@pbs!hub:s" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		var parts []string
		for g, at := range p.last {
			typ, id, _ := strings.Cut(g, "/")
			parts = append(parts, fmt.Sprintf(`{"backup-type":%q,"backup-id":%q,"last-backup":%d}`, typ, id, at.Unix()))
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(parts, ","))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *testPBS) set(group string, at time.Time) {
	p.mu.Lock()
	p.last[group] = at
	p.mu.Unlock()
}

// config: guest "guest" is ct/302 and gets a fresh backup from "hv" before it installs.
func (p *testPBS) config(t *testing.T) hub.BackupConfig {
	t.Helper()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("t@pbs!hub:s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(p.srv.Certificate().Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var pairs []string
	for i := 0; i < len(h); i += 2 {
		pairs = append(pairs, h[i:i+2])
	}
	return hub.BackupConfig{PBSURL: p.srv.URL, Datastore: "store", Fingerprint: strings.Join(pairs, ":"), TokenFile: token,
		MaxAgeHours: 26, Guests: []hub.BackupGuest{{Node: "guest", Group: "ct/302"}, {Node: "hv", Without: "the hypervisor"}},
		BeforeInstall: &hub.BeforeInstall{Hypervisor: "hv"}}
}

// countingInstaller installs nothing and counts how often it was asked (from the agent's goroutine).
type countingInstaller struct{ calls atomic.Int32 }

func (c *countingInstaller) Install(context.Context, source.InstallRequest, time.Duration, func(source.Progress)) source.InstallResult {
	c.calls.Add(1)
	return source.InstallResult{Code: protocol.CodeOK}
}

func (c *countingInstaller) RebootRequired(context.Context) bool { return false }

// backupFixture: a hub with the backup check before installs, PBS, the hypervisor's agent "hv" and its guest's agent
// "guest", both approved and connected. vzdump does what the test sets.
type backupFixture struct {
	hub     *testHub
	pbs     *testPBS
	inst    *countingInstaller
	vzdumps atomic.Int32
	mu      sync.Mutex
	vzdump  func() runner.Result // what one vzdump run does (setVzdump)
}

func (f *backupFixture) setVzdump(do func() runner.Result) {
	f.mu.Lock()
	f.vzdump = do
	f.mu.Unlock()
}

func newBackupFixture(t *testing.T, last map[string]time.Time, withHypervisor bool) *backupFixture {
	t.Helper()
	return newBackupFixtureAt(t, last, withHypervisor, time.Now, nil)
}

// newBackupFixtureAt: the same with one clock for hub, PBS check and agents, and an install window for the guest.
func newBackupFixtureAt(t *testing.T, last map[string]time.Time, withHypervisor bool, now func() time.Time,
	window *policy.Window) *backupFixture {
	t.Helper()
	f := &backupFixture{hub: startHub(t, 5), pbs: newTestPBS(t, last), inst: &countingInstaller{}}
	f.hub.srv.Now = now
	b, err := hub.NewBackups(f.pbs.config(t), now)
	if err != nil {
		t.Fatal(err)
	}
	f.hub.srv.Backups = b
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clients := []*Client{}
	if withHypervisor {
		hv := f.hub.client(t, "hv", &fakeChecker{})
		hv.Backups = &GuestBackups{Storage: "pbs", Timeout: time.Hour,
			Exists: func(kind, id string) bool { return kind+"/"+id == "ct/302" },
			Vzdump: func(context.Context, ...string) runner.Result {
				f.vzdumps.Add(1)
				f.mu.Lock()
				do := f.vzdump
				f.mu.Unlock()
				return do()
			}}
		clients = append(clients, hv)
	}
	guest := f.hub.client(t, "guest", &fakeChecker{})
	guest.Cfg.Install = policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all", Auto: policy.AutoOff,
		MinIntervalH: 6, TimeoutMin: 60, Window: window}
	guest.Installer = f.inst
	clients = append(clients, guest)
	for _, c := range clients {
		c.Now = now
		go c.Run(ctx)
		node := c.Cfg.Node
		waitFor(t, node+" pending", func() bool { return state(f.hub.reg, node) == wire.StatePendingApproval })
		f.hub.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: node, Fingerprint: c.ID.Fingerprint()})
		waitFor(t, node+" online", func() bool { online, _ := f.hub.srv.Online(node); return online })
	}
	return f
}

// install starts an install on the guest the way Home Assistant's button does (backup check, then the job) and waits
// for its end.
func (f *backupFixture) install(t *testing.T) (hub.AdminResponse, wire.JobStatus) {
	t.Helper()
	gate := f.hub.srv.Admin(hub.AdminRequest{Cmd: "backup", Node: "guest"})
	job, code := f.hub.srv.StartJob("guest", protocol.VerbInstall, "all", hub.ByAdmin)
	if code != hub.AdminOK {
		t.Fatalf("start: %s", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ := f.hub.srv.WaitJob(ctx, job)
	return gate, st
}

func (f *backupFixture) run(node string) *hub.Run {
	e, _ := f.hub.reg.Get(node)
	return e.LastRun
}

// The install of a guest waits in phase backing_up until the hypervisor backed it up and PBS has the backup - however
// old the last one was. The backup is no run of the hypervisor's own.
func TestInstallWaitsForAFreshBackup(t *testing.T) {
	hold := make(chan struct{})
	f := newBackupFixture(t, map[string]time.Time{"ct/302": time.Now().Add(-30 * time.Hour)}, true)
	f.setVzdump(func() runner.Result {
		<-hold
		f.pbs.set("ct/302", time.Now())
		return runner.Result{Stderr: []byte("INFO: Finished Backup of VM 302\n")}
	})
	gate := f.hub.srv.Admin(hub.AdminRequest{Cmd: "backup", Node: "guest"})
	if gate.Code != hub.AdminOK || gate.Backup.BeforeInstall != "hv" || gate.Backup.State != hub.BackupStale {
		t.Fatalf("a 30 h old backup does not stop a guest that is backed up first: %+v %+v", gate, gate.Backup)
	}
	job, code := f.hub.srv.StartJob("guest", protocol.VerbInstall, "all", hub.ByAdmin)
	if code != hub.AdminOK {
		t.Fatalf("start: %s", code)
	}
	done := make(chan wire.JobStatus, 1)
	go func() {
		st, _ := f.hub.srv.WaitJob(context.Background(), job)
		done <- st
	}()
	waitFor(t, "vzdump", func() bool { return f.vzdumps.Load() == 1 })
	if r := f.run("guest"); r == nil || r.Phase != protocol.PhaseBackingUp || r.JobID != job.ID || f.inst.calls.Load() != 0 {
		t.Fatalf("during the backup: run %+v, installs %d", r, f.inst.calls.Load())
	}
	if r := f.run("hv"); r != nil {
		t.Fatalf("the backup became the hypervisor's run: %+v", r)
	}
	close(hold)
	select {
	case st := <-done:
		if st.Code != protocol.CodeOK || st.JobID != job.ID || f.inst.calls.Load() != 1 {
			t.Fatalf("after the backup: %+v, installs %d", st, f.inst.calls.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the install did not end")
	}
	if r := f.run("guest"); r.Code != protocol.CodeOK || r.JobID != job.ID {
		t.Fatalf("guest run: %+v", r)
	}
}

// A backup made minutes ago (the scheduled job, or the same install again) is used; no new one is made.
func TestRecentBackupIsUsed(t *testing.T) {
	f := newBackupFixture(t, map[string]time.Time{"ct/302": time.Now().Add(-5 * time.Minute)}, true)
	f.setVzdump(func() runner.Result { return runner.Result{} })
	if _, st := f.install(t); st.Code != protocol.CodeOK || f.vzdumps.Load() != 0 || f.inst.calls.Load() != 1 {
		t.Fatalf("result %+v, vzdump %d, installs %d", st, f.vzdumps.Load(), f.inst.calls.Load())
	}
}

// Without a fresh backup nothing is installed, and the run says why: vzdump failed, or PBS does not have the backup
// the hypervisor reported.
func TestNoFreshBackupNoInstall(t *testing.T) {
	for name, tc := range map[string]struct {
		vzdump func(p *testPBS) runner.Result
		says   string
	}{
		"vzdump failed": {func(*testPBS) runner.Result {
			return runner.Result{ExitCode: 255, Stderr: []byte("ERROR: Backup of VM 302 failed - no space left on device\n")}
		}, "no space left"},
		"not on PBS": {func(*testPBS) runner.Result { return runner.Result{} }, "PBS has no backup of ct/302"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBackupFixture(t, map[string]time.Time{"ct/302": time.Now().Add(-30 * time.Hour)}, true)
			f.setVzdump(func() runner.Result { return tc.vzdump(f.pbs) })
			_, st := f.install(t)
			if st.Code != protocol.CodeFailedBackup || !strings.Contains(st.Detail, tc.says) || f.inst.calls.Load() != 0 {
				t.Fatalf("result %+v, installs %d", st, f.inst.calls.Load())
			}
			if r := f.run("guest"); r.Code != protocol.CodeFailedBackup || r.Phase != "" || !protocol.CodeFailedBackup.Problem() {
				t.Fatalf("guest run: %+v", r)
			}
		})
	}
}

// Without the hypervisor's agent no backup can be made: the check refuses the install before it starts, and an
// install that got past it (it waited in the queue meanwhile) ends without installing.
func TestHypervisorAway(t *testing.T) {
	f := newBackupFixture(t, map[string]time.Time{"ct/302": time.Now().Add(-time.Hour)}, false)
	gate, st := f.install(t)
	if gate.Code != hub.AdminBackupHostUnavailable {
		t.Fatalf("check: %+v", gate)
	}
	if st.Code != protocol.CodeFailedBackup || !strings.Contains(st.Detail, "hv: unknown_node") || f.inst.calls.Load() != 0 {
		t.Fatalf("result %+v", st)
	}
	if !slices.Contains(protocol.AllCodes(), st.Code) {
		t.Fatal("code not in the table")
	}
}
