package apt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// seqRunner answers a command with the next of its results (the last one repeats) and streams status lines.
type seqRunner struct {
	answers map[string][]runner.Result
	lines   map[string][]string
	calls   []string
}

func (f *seqRunner) Run(_ context.Context, _ []string, name string, args ...string) runner.Result {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	rs, ok := f.answers[cmd]
	if !ok {
		return runner.Result{Err: errors.New("not in fake: " + cmd)}
	}
	if len(rs) > 1 {
		f.answers[cmd] = rs[1:]
	}
	return rs[0]
}

func (f *seqRunner) Stream(ctx context.Context, env []string, onStatus func(string), name string, args ...string) runner.Result {
	for _, l := range f.lines[strings.Join(append([]string{name}, args...), " ")] {
		onStatus(l)
	}
	return f.Run(ctx, env, name, args...)
}

const (
	twoPending = "Inst libc6 [2.36-9+deb12u9] (2.36-9+deb12u10 Debian:12.12/stable, Debian-Security:12/stable-security [amd64])\n" +
		"Inst tzdata [2024a-0+deb12u1] (2025b-0+deb12u1 Debian:12.12/stable-updates [all])\n"
	dlCmd  = "ionice -c 2 -n 7 apt-get -q -y -d -o APT::Status-Fd=3 full-upgrade"
	insCmd = "ionice -c 2 -n 7 apt-get -q -y -o APT::Status-Fd=3 -o Dpkg::Options::=--force-confdef " +
		"-o Dpkg::Options::=--force-confold full-upgrade"
)

func installFake() *seqRunner {
	one := func(r runner.Result) []runner.Result { return []runner.Result{r} }
	return &seqRunner{
		answers: map[string][]runner.Result{
			"dpkg --audit":                     one(ok("")),
			"apt-get --version":                one(ok("apt 2.6.1 (amd64)\n")),
			"apt-get update -q --error-on=any": one(ok("")),
			// The plan, then the verification: nothing left.
			"apt-get -s -q full-upgrade": {ok(twoPending), ok("")},
			"apt-get -qq --print-uris -y -d full-upgrade": one(ok(
				"'http://deb.debian.org/pool/libc6.deb' libc6_2.36-9+deb12u10_amd64.deb 2871234 SHA256:aa\n" +
					"'http://deb.debian.org/pool/tzdata.deb' tzdata_2025b-0+deb12u1_all.deb 260000 SHA256:bb\n")),
			dlCmd:  one(ok("Get:1 libc6\nGet:2 tzdata\n")),
			insCmd: one(ok("Setting up libc6 ...\nSetting up tzdata ...\n")),
			"needrestart -b": one(ok("NEEDRESTART-VER: 3.6\nNEEDRESTART-KSTA: 1\nNEEDRESTART-SVC: dbus.service\n" +
				"NEEDRESTART-SVC: systemd-logind.service\n")),
			"apt-get clean": one(ok("")),
		},
		lines: map[string][]string{
			dlCmd: {"dlstatus:1:0:Retrieving file 1 of 2", "dlstatus:2:50:Retrieving file 2 of 2"},
			insCmd: {"pmstatus:dpkg-exec:0:Running dpkg", "pmstatus:libc6:amd64:25:Unpacking libc6:amd64 (2.36-9+deb12u10)",
				"pmstatus:tzdata:75:Setting up tzdata (2025b-0+deb12u1)"},
		},
	}
}

func newInstaller(t *testing.T, f *seqRunner) *Apt {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"lists", "archives", "boot", "dpkg"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &Apt{
		Run:      f,
		Stream:   f,
		LookPath: func(string) (string, error) { return "/usr/bin/ionice", nil },
		IsRoot:   true,
		Now:      time.Now,
		Paths: Paths{ListsDir: filepath.Join(dir, "lists"), ListsLock: filepath.Join(dir, "lists", "lock"),
			DpkgLock: filepath.Join(dir, "dpkg", "lock-frontend"), ArchivesDir: filepath.Join(dir, "archives"),
			BootDir: filepath.Join(dir, "boot")},
		Release: "12", // the fixtures are Debian 12
	}
}

type recorder struct {
	steps    []source.Progress
	journals int
	mark     func() // also runs when the journal is written: puts it in line with the commands
}

func (r *recorder) req(scope protocol.ScopeKind) source.InstallRequest {
	return source.InstallRequest{Scope: protocol.Scope{Kind: scope}, BeforeChange: func() error {
		r.journals++
		if r.mark != nil {
			r.mark()
		}
		return nil
	}}
}

func (r *recorder) progress(p source.Progress) { r.steps = append(r.steps, p) }

func TestInstallAll(t *testing.T) {
	f := installFake()
	rec := recorder{mark: func() { f.calls = append(f.calls, "journal") }}
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || res.Planned != 2 || rec.journals != 1 {
		t.Fatalf("result %+v, journals %d, calls %v", res, rec.journals, f.calls)
	}
	// What the run did: the packages, the whole output for the run log, services still on old libraries; the
	// package cache is emptied afterwards.
	if !slices.Equal(res.Packages, []string{"libc6", "tzdata"}) ||
		string(res.Log) != "Get:1 libc6\nGet:2 tzdata\nSetting up libc6 ...\nSetting up tzdata ...\n" ||
		!slices.Equal(res.OutdatedServices, []string{"dbus.service", "systemd-logind.service"}) ||
		f.calls[len(f.calls)-1] != "apt-get clean" {
		t.Fatalf("summary: packages %v, log %q, services %v, last call %q", res.Packages, res.Log,
			res.OutdatedServices, f.calls[len(f.calls)-1])
	}
	// The journal is written after the download, which touches no package, and before the first change.
	dl, j, ins := slices.Index(f.calls, dlCmd), slices.Index(f.calls, "journal"), slices.Index(f.calls, insCmd)
	if dl < 0 || slices.Index(f.calls, "apt-get -qq --print-uris -y -d full-upgrade") > dl || j < dl || ins < j {
		t.Fatalf("order: %v", f.calls)
	}
	var phases []protocol.Phase
	last := -1
	for _, s := range rec.steps {
		if s.Percent < last {
			t.Fatalf("progress went back: %+v", rec.steps)
		}
		last = s.Percent
		if len(phases) == 0 || phases[len(phases)-1] != s.Phase {
			phases = append(phases, s.Phase)
		}
	}
	want := []protocol.Phase{protocol.PhasePrechecking, protocol.PhaseDownloading, protocol.PhaseInstalling, protocol.PhaseVerifying}
	if !slices.Equal(phases, want) || last != 100 {
		t.Fatalf("phases %v, last %d", phases, last)
	}
	// dlstatus 50 % lies halfway through the download share, pmstatus 25 % a quarter into the install share.
	if !slices.Contains(rec.steps, source.Progress{Phase: protocol.PhaseDownloading, Percent: 20}) ||
		!slices.Contains(rec.steps, source.Progress{Phase: protocol.PhaseInstalling, Percent: 50, Item: "libc6:amd64"}) {
		t.Fatalf("progress: %+v", rec.steps)
	}
}

func TestInstallPrechecksStopBeforeAnyChange(t *testing.T) {
	cases := map[string]struct {
		change func(*seqRunner)
		code   protocol.Code
	}{
		"database interrupted": {func(f *seqRunner) {
			f.answers["dpkg --audit"] = []runner.Result{ok("The following packages are only half configured:\n foo\n")}
		}, protocol.CodePrecheckPackageDBBroken},
		"source unreachable": {func(f *seqRunner) {
			f.answers["apt-get update -q --error-on=any"] = []runner.Result{exit(100, "E: Failed to fetch")}
		}, protocol.CodePrecheckRepoUnreachable},
		"would remove": {func(f *seqRunner) {
			f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(twoPending + "Remv oldpkg [1.0]\n")}
		}, protocol.CodePrecheckWouldRemove},
		"no space": {func(f *seqRunner) {
			f.answers["apt-get -qq --print-uris -y -d full-upgrade"] = []runner.Result{ok(
				"'http://x/huge.deb' huge.deb 999999999999999 SHA256:cc\n")}
		}, protocol.CodePrecheckDiskSpace},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := installFake()
			tc.change(f)
			var rec recorder
			res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
			if res.Code != tc.code || rec.journals != 0 || slices.Contains(f.calls, dlCmd) {
				t.Fatalf("result %+v, journals %d, calls %v", res, rec.journals, f.calls)
			}
		})
	}
}

// A download that fails changed nothing: no journal, so the run does not count for
// install.min_interval_h and the install window stays usable; nothing is installed.
func TestFailedDownloadWritesNoJournal(t *testing.T) {
	for name, res := range map[string]runner.Result{
		"mirror unreachable": exit(100, "E: Failed to fetch http://x/libc6.deb"),
	} {
		t.Run(name, func(t *testing.T) {
			f := installFake()
			f.answers[dlCmd] = []runner.Result{res}
			var rec recorder
			got := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
			if got.Code != protocol.CodePrecheckRepoUnreachable || rec.journals != 0 || slices.Contains(f.calls, insCmd) {
				t.Fatalf("result %+v, journals %d, calls %v", got, rec.journals, f.calls)
			}
		})
	}
}

func TestInstallFailures(t *testing.T) {
	f := installFake()
	f.answers[insCmd] = []runner.Result{exit(100, "E: Sub-process /usr/bin/dpkg returned an error code (1)")}
	f.lines[insCmd] = append(f.lines[insCmd], "pmerror:foo:amd64:80:installed post-installation script returned error exit status 1")
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
	if res.Code != protocol.CodeFailedPackageManager || !strings.Contains(res.Detail, "foo:amd64: installed post-installation") ||
		!strings.Contains(res.Detail, "exit code 100") {
		t.Fatalf("failed install: %+v", res)
	}

	f = installFake()
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(twoPending), ok("Inst tzdata [2024a] (2025b Debian:12/stable [all])\n")}
	res = newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
	if res.Code != protocol.CodeFailedVerification || res.Detail != "still pending: tzdata" {
		t.Fatalf("verification: %+v", res)
	}
}

// Security: only the packages a full simulation marks as from a security source, upgraded by name.
func TestInstallSecurityByName(t *testing.T) {
	f := installFake()
	target := "install --only-upgrade openssl libssl3"
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(debianSim)}
	f.answers["apt-get -s -q "+target] = []runner.Result{ok(
		"Inst openssl [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian-Security:12/stable-security [amd64])\n" +
			"Inst libssl3 [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian-Security:12/stable-security [amd64])\n"), ok("")}
	f.answers["apt-get -qq --print-uris -y -d "+target] = []runner.Result{ok("")}
	f.answers["ionice -c 2 -n 7 apt-get -q -y -d -o APT::Status-Fd=3 "+target] = []runner.Result{ok("")}
	f.answers["ionice -c 2 -n 7 apt-get -q -y -o APT::Status-Fd=3 -o Dpkg::Options::=--force-confdef "+
		"-o Dpkg::Options::=--force-confold "+target] = []runner.Result{ok("")}
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeSecurity), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || res.Planned != 2 {
		t.Fatalf("security: %+v, calls %v", res, f.calls)
	}

	// Nothing from a security source pending: done without touching anything.
	f = installFake()
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok("Inst tzdata [2024a] (2025b Debian:12/stable [all])\n")}
	res = newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeSecurity), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || res.Planned != 0 || slices.ContainsFunc(f.calls, func(c string) bool {
		return strings.Contains(c, "APT::Status-Fd")
	}) {
		t.Fatalf("nothing to do: %+v, calls %v", res, f.calls)
	}
}

const (
	opensslSec = "Inst openssl [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian-Security:12/stable-security [amd64])\n"
	curlSec    = "Inst curl [7.88.1-10+deb12u12] (7.88.1-10+deb12u14 Debian-Security:12/stable-security [amd64])\n"
	fromUpdate = "Inst libcurl-extra [1.0] (1.1 Debian:12/stable-updates [amd64])\n"
	newFromRel = "Inst libnew (1.0-1 Debian:12.12/stable [amd64])\n"
)

// A security run changes only what comes from the security archives or the distribution's release (ADR-0116).
// openssl's fix with a new dependency from the release goes through. curl's needs a package from -updates: held back,
// named in the result, never downloaded or installed.
func TestSecurityRunHoldsBackWhatNeedsOtherSources(t *testing.T) {
	f := installFake()
	both, one := "install --only-upgrade openssl curl", "install --only-upgrade openssl"
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(opensslSec + curlSec +
		"Inst tzdata [2024a-0+deb12u1] (2025b-0+deb12u1 Debian:12.12/stable-updates [all])\n")}
	// Together they reach -updates: once all at once, once when curl is tried after openssl.
	f.answers["apt-get -s -q "+both] = []runner.Result{ok(opensslSec + newFromRel + curlSec + fromUpdate)}
	f.answers["apt-get -s -q "+one] = []runner.Result{ok(opensslSec + newFromRel), ok("")} // plan, verification
	f.answers["apt-get -qq --print-uris -y -d "+one] = []runner.Result{ok("")}
	f.answers["ionice -c 2 -n 7 apt-get -q -y -d -o APT::Status-Fd=3 "+one] = []runner.Result{ok("")}
	f.answers["ionice -c 2 -n 7 apt-get -q -y -o APT::Status-Fd=3 -o Dpkg::Options::=--force-confdef "+
		"-o Dpkg::Options::=--force-confold "+one] = []runner.Result{ok("")}
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeSecurity), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || res.Planned != 2 || !slices.Equal(res.Packages, []string{"openssl", "libnew"}) {
		t.Fatalf("result %+v, calls %v", res, f.calls)
	}
	if !strings.Contains(res.Detail, "curl needs libcurl-extra 1.1 (Debian:12/stable-updates)") {
		t.Errorf("held back without saying why: %q", res.Detail)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "APT::Status-Fd") && strings.Contains(c, "curl") {
			t.Errorf("curl went into the run: %s", c)
		}
	}
}

// Nothing a security run may install on its own: no download, no journal, ok with the reason.
func TestSecurityRunHoldsBackEverything(t *testing.T) {
	f := installFake()
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(curlSec)}
	f.answers["apt-get -s -q install --only-upgrade curl"] = []runner.Result{ok(curlSec + fromUpdate)}
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeSecurity), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || res.Planned != 0 || rec.journals != 0 ||
		!strings.Contains(res.Detail, "curl needs libcurl-extra 1.1") || slices.ContainsFunc(f.calls, func(c string) bool {
		return strings.Contains(c, "APT::Status-Fd")
	}) {
		t.Fatalf("result %+v, journals %d, calls %v", res, rec.journals, f.calls)
	}
	if n := strings.Count(strings.Join(f.calls, "\n"), "install --only-upgrade curl"); n != 1 {
		t.Errorf("one candidate simulated %d times", n)
	}
}

// The third ok path: the kept update plans nothing (a kernel image --only-upgrade does not install), the other one is
// held back. Nothing is downloaded, and the result still says what was held back.
func TestSecurityRunKeepsNothingToDoButSaysWhatItHeldBack(t *testing.T) {
	f := installFake()
	kernel := "Inst linux-image-6.1.0-41-amd64 (6.1.158-1 Debian-Security:12/stable-security [amd64])\n"
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(kernel + curlSec)}
	f.answers["apt-get -s -q install --only-upgrade linux-image-6.1.0-41-amd64 curl"] =
		[]runner.Result{ok(curlSec + fromUpdate)}
	f.answers["apt-get -s -q install --only-upgrade linux-image-6.1.0-41-amd64"] = []runner.Result{ok("")}
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeSecurity), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || res.Planned != 0 || rec.journals != 0 ||
		!strings.Contains(res.Detail, "curl needs libcurl-extra 1.1") {
		t.Fatalf("result %+v, journals %d, calls %v", res, rec.journals, f.calls)
	}
}

// cancelOn ends the run's context when a command starts, as if install.timeout_min ran out right then.
type cancelOn struct {
	*seqRunner
	cmd    string
	cancel context.CancelFunc
}

func (c cancelOn) Run(ctx context.Context, env []string, name string, args ...string) runner.Result {
	if strings.Join(append([]string{name}, args...), " ") == c.cmd {
		c.cancel()
		return runner.Result{Err: context.Canceled}
	}
	return c.seqRunner.Run(ctx, env, name, args...)
}

// A security run may simulate once per update; when the time runs out while it chooses, the run says timeout, not
// that the package manager failed.
func TestSecurityRunTimesOutWhileChoosing(t *testing.T) {
	f := installFake()
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(opensslSec + curlSec)}
	f.answers["apt-get -s -q install --only-upgrade openssl curl"] = []runner.Result{ok(opensslSec + curlSec + fromUpdate)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newInstaller(t, f)
	a.Run = cancelOn{seqRunner: f, cmd: "apt-get -s -q install --only-upgrade openssl", cancel: cancel}
	var rec recorder
	res := a.Install(ctx, rec.req(protocol.ScopeSecurity), time.Hour, rec.progress)
	if res.Code != protocol.CodeTimeout || rec.journals != 0 {
		t.Fatalf("result %+v, journals %d, calls %v", res, rec.journals, f.calls)
	}
}

func TestHeldDetailIsBounded(t *testing.T) {
	var held []heldBack
	for i := range 12 {
		held = append(held, heldBack{name: fmt.Sprintf("p%d", i), outside: []string{"a", "b", "c", "d", "e"}})
	}
	d := heldDetail(held)
	if !strings.Contains(d, "p0 needs a, b, c and 2 more") || !strings.Contains(d, "p9 needs") ||
		strings.Contains(d, "p10") || !strings.HasSuffix(d, "and 2 more") || heldDetail(nil) != "" {
		t.Errorf("detail: %q", d)
	}
}

// install.mode names the command for scope all instead of the default full-upgrade (ADR-0076).
func TestInstallModeFromPolicy(t *testing.T) {
	f := installFake()
	for _, k := range []string{"apt-get -s -q full-upgrade", "apt-get -qq --print-uris -y -d full-upgrade", dlCmd, insCmd} {
		f.answers[strings.Replace(k, "full-upgrade", "upgrade", 1)] = f.answers[k]
		delete(f.answers, k)
	}
	var rec recorder
	req := rec.req(protocol.ScopeAll)
	req.Mode = "upgrade"
	res := newInstaller(t, f).Install(context.Background(), req, time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || slices.ContainsFunc(f.calls, func(c string) bool {
		return strings.Contains(c, "full-upgrade")
	}) {
		t.Fatalf("upgrade from the policy: %+v, calls %v", res, f.calls)
	}
}

func TestStatusLines(t *testing.T) {
	var got []source.Progress
	var errs []string
	say := func(ph protocol.Phase, pct int, item string) {
		got = append(got, source.Progress{Phase: ph, Percent: pct, Item: item})
	}
	h := statusLines(protocol.PhaseInstalling, 35, 95, say, &errs)
	for _, l := range []string{"pmstatus:libc6:amd64:50:Unpacking libc6:amd64", "pmstatus:tzdata:100:Installed tzdata",
		"pmerror:foo:7.5:error: something", "pmconffile:/etc/x:/etc/x.dpkg-new:'1 1 2 1'", "garbage"} {
		h(l)
	}
	want := []source.Progress{{Phase: protocol.PhaseInstalling, Percent: 65, Item: "libc6:amd64"},
		{Phase: protocol.PhaseInstalling, Percent: 95, Item: "tzdata"}}
	if !slices.Equal(got, want) || len(errs) != 1 || errs[0] != "foo: error: something" {
		t.Fatalf("got %+v, errors %v", got, errs)
	}
}
