package apt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// fakeRunner answers by the joined command line; unknown commands "cannot run".
type fakeRunner struct {
	answers map[string]runner.Result
	calls   []string
}

func (f *fakeRunner) Run(_ context.Context, _ []string, name string, args ...string) runner.Result {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	if r, ok := f.answers[cmd]; ok {
		return r
	}
	return runner.Result{Err: errors.New("not in fake: " + cmd)}
}

func ok(stdout string) runner.Result { return runner.Result{Stdout: []byte(stdout)} }

func exit(code int, stderr string) runner.Result {
	return runner.Result{ExitCode: code, Stderr: []byte(stderr)}
}

const debianSim = `NOTE: This is only a simulation!
Inst openssl [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian:12.12/stable, Debian-Security:12/stable-security [amd64]) []
Inst libssl3 [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian-Security:12/stable-security [amd64])
Inst tzdata [2024a-0+deb12u1] (2025b-0+deb12u1 Debian:12.12/stable-updates [all])
Inst pve-manager [8.4.1] (8.4.14 Proxmox:bookworm/bookworm [amd64])
Inst newdep (1.0-1 Debian:12.12/stable [amd64])
Inst weird-line-without-candidate
Remv obsolete-pkg [2.0-1]
Conf openssl (3.0.17-1~deb12u2 Debian:12.12/stable, Debian-Security:12/stable-security [amd64])
`

func base() map[string]runner.Result {
	return map[string]runner.Result{
		"apt-get --version": ok("apt 2.6.1 (amd64)\nSupported modules:\n"),
		"apt-config shell UU APT::Periodic::Unattended-Upgrade": ok("UU='1'\n"),
		"apt-get update -q --error-on=any":                      ok(""),
		"apt-get -s -q full-upgrade":                            ok(debianSim),
	}
}

func newApt(t *testing.T, answers map[string]runner.Result, root bool) (*Apt, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	f := &fakeRunner{answers: answers}
	a := &Apt{
		Run:      f,
		LookPath: exec.LookPath,
		IsRoot:   root,
		Now:      func() time.Time { return time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC) },
		Paths: Paths{
			ListsDir:       filepath.Join(dir, "lists"),
			ListsLock:      filepath.Join(dir, "lists", "lock"),
			RebootRequired: filepath.Join(dir, "reboot-required"),
			RebootNotifier: filepath.Join(dir, "notify-reboot-required"),
			BootDir:        filepath.Join(dir, "boot"),
			OSRelease:      filepath.Join(dir, "osrelease"),
		},
	}
	return a, f
}

func TestParseSimulationUbuntu(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ubuntu-full-upgrade.txt"))
	if err != nil {
		t.Fatal(err)
	}
	pkgs, removals := ParseSimulation(raw)
	if len(pkgs) != 10 || len(removals) != 0 {
		t.Fatalf("%d packages, %d removals; want 10, 0", len(pkgs), len(removals))
	}
	first := pkgs[0]
	if first.Name != "base-files" || first.From != "13ubuntu1" || first.To != "13ubuntu1.1" ||
		!slices.Equal(first.Origins, []string{"Ubuntu:24.04/noble-updates"}) || first.Security {
		t.Fatalf("first package: %+v", first)
	}
	var security []string
	for _, p := range pkgs {
		if p.Name == "" || p.To == "" || len(p.Origins) == 0 {
			t.Errorf("incomplete: %+v", p)
		}
		if p.Security {
			security = append(security, p.Name)
		}
		if p.Name == "libaudit1" && (p.From != "1:3.1.0-1build1" || p.To != "1:3.1.0-1ubuntu0.1") {
			t.Errorf("epoch: %+v", p)
		}
	}
	if !slices.Equal(security, []string{"libssl3t64", "openssl"}) {
		t.Errorf("security: %v", security)
	}
}

func TestParseSimulationDebian(t *testing.T) {
	pkgs, removals := ParseSimulation([]byte(debianSim))
	if len(pkgs) != 6 || !slices.Equal(removals, []string{"obsolete-pkg"}) {
		t.Fatalf("%d packages, removals %v", len(pkgs), removals)
	}
	want := map[string]struct {
		from, to string
		security bool
	}{
		"openssl":     {"3.0.15-1~deb12u1", "3.0.17-1~deb12u2", true},
		"libssl3":     {"3.0.15-1~deb12u1", "3.0.17-1~deb12u2", true},
		"tzdata":      {"2024a-0+deb12u1", "2025b-0+deb12u1", false},
		"pve-manager": {"8.4.1", "8.4.14", false},
		"newdep":      {"", "1.0-1", false},
	}
	for _, p := range pkgs {
		w, known := want[p.Name]
		if !known {
			if p.Name != "weird-line-without-candidate" {
				t.Errorf("unexpected %+v", p)
			}
			continue
		}
		if p.From != w.from || p.To != w.to || p.Security != w.security {
			t.Errorf("%s: %+v, want %+v", p.Name, p, w)
		}
	}
	if o := pkgs[0].Origins; !slices.Equal(o, []string{"Debian:12.12/stable", "Debian-Security:12/stable-security"}) {
		t.Errorf("origins %v", o)
	}
}

func TestCheckUpdatesAvailable(t *testing.T) {
	a, f := newApt(t, base(), true)
	rep := a.Check(context.Background(), true)
	if rep.State != protocol.SourceUpdatesAvailable || rep.Pending != 6 || rep.Security != 2 ||
		!slices.Equal(rep.Removals, []string{"obsolete-pkg"}) {
		t.Fatalf("report: %+v", rep)
	}
	if rep.Facts["lists_refresh"] != RefreshDone || rep.Facts["install_mode"] != "full-upgrade" ||
		rep.Facts["unattended_upgrades"] != "enabled" || rep.Facts["apt_version"] != "2.6.1" {
		t.Fatalf("facts: %v", rep.Facts)
	}
	if !slices.Contains(f.calls, "apt-get update -q --error-on=any") {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestCheckUpToDate(t *testing.T) {
	ans := base()
	ans["apt-get -s -q full-upgrade"] = ok("Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove.\n")
	a, _ := newApt(t, ans, true)
	if rep := a.Check(context.Background(), true); rep.State != protocol.SourceUpToDate || rep.Pending != 0 {
		t.Fatalf("report: %+v", rep)
	}
}

func TestCheckRepoUnreachable(t *testing.T) {
	ans := base()
	ans["apt-get update -q --error-on=any"] = exit(100, "E: Failed to fetch http://deb.example/dists/x/InRelease")
	a, f := newApt(t, ans, true)
	rep := a.Check(context.Background(), true)
	if rep.State != protocol.SourceCheckFailed || rep.Code != protocol.CodePrecheckRepoUnreachable ||
		!strings.Contains(rep.Detail, "Failed to fetch") || rep.Facts["lists_refresh"] != RefreshFailed {
		t.Fatalf("report: %+v", rep)
	}
	if slices.Contains(f.calls, "apt-get -s -q full-upgrade") {
		t.Fatal("simulated on stale lists after a failed refresh")
	}
}

func TestCheckClassifiesFailedSimulation(t *testing.T) {
	for name, c := range map[string]struct {
		audit runner.Result
		want  protocol.Code
	}{
		"package db broken": {ok("The following packages are only half configured:\n libfoo\n"), protocol.CodePrecheckPackageDBBroken},
		"other failure":     {ok(""), protocol.CodeFailedPackageManager},
	} {
		t.Run(name, func(t *testing.T) {
			ans := base()
			ans["apt-get -s -q full-upgrade"] = exit(100, "E: Unmet dependencies.")
			ans["dpkg --audit"] = c.audit
			a, _ := newApt(t, ans, true)
			if rep := a.Check(context.Background(), true); rep.Code != c.want || rep.State != protocol.SourceCheckFailed {
				t.Fatalf("report: %+v", rep)
			}
		})
	}
}

func TestCheckWithoutRootSkipsRefresh(t *testing.T) {
	a, f := newApt(t, base(), false)
	rep := a.Check(context.Background(), true)
	if rep.Facts["lists_refresh"] != RefreshNotRoot || rep.State != protocol.SourceUpdatesAvailable {
		t.Fatalf("report: %+v", rep)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "apt-get update") {
			t.Fatalf("refreshed without root: %v", f.calls)
		}
	}
}

func TestOldAptGetsNoErrorOn(t *testing.T) {
	ans := base()
	ans["apt-get --version"] = ok("apt 2.0.10 (amd64)\n")
	ans["apt-get update -q"] = ok("")
	a, f := newApt(t, ans, true)
	a.Check(context.Background(), true)
	if !slices.Contains(f.calls, "apt-get update -q") || slices.Contains(f.calls, "apt-get update -q --error-on=any") {
		t.Fatalf("calls: %v", f.calls)
	}
}

// Every host gets full-upgrade for scope all unless its policy says otherwise; dpkg is not asked (ADR-0076).
func TestInstallModeDefault(t *testing.T) {
	a, f := newApt(t, base(), true)
	if m := a.Check(context.Background(), false).Facts["install_mode"]; m != "full-upgrade" ||
		slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "dpkg-query") }) {
		t.Fatalf("install_mode %q, calls %v", m, f.calls)
	}
}

// The fact install_mode is the mode scope all runs, so install.mode of the policy wins over the default (ADR-0095).
func TestInstallModeFactFollowsPolicy(t *testing.T) {
	a, _ := newApt(t, base(), true)
	a.PolicyMode = "upgrade"
	if m := a.Check(context.Background(), false).Facts["install_mode"]; m != "upgrade" {
		t.Fatalf("install_mode %q, want upgrade from the policy", m)
	}
}

// With install.mode upgrade, what only full-upgrade would take is marked kept back and counted (ADR-0127): here
// pve-manager and the new dependency newdep, which "upgrade" leaves. Still pending, the security count unchanged.
// Other modes ask nothing more; a failing second simulation marks nothing.
func TestCheckMarksKeptBack(t *testing.T) {
	const upgradeSim = `NOTE: This is only a simulation!
Inst openssl [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian:12.12/stable, Debian-Security:12/stable-security [amd64]) []
Inst libssl3 [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian-Security:12/stable-security [amd64])
Inst tzdata [2024a-0+deb12u1] (2025b-0+deb12u1 Debian:12.12/stable-updates [all])
Inst weird-line-without-candidate
`
	answers := base()
	answers["apt-get -s -q upgrade"] = ok(upgradeSim)
	a, _ := newApt(t, answers, true)
	a.PolicyMode = "upgrade"
	rep := a.Check(context.Background(), false)
	var kept []string
	for _, p := range rep.Packages {
		if p.KeptBack {
			kept = append(kept, p.Name)
		}
	}
	if !slices.Equal(kept, []string{"pve-manager", "newdep"}) || rep.KeptBack != 2 || rep.Pending != 6 || rep.Security != 2 {
		t.Fatalf("kept back %v (%d), pending %d, security %d", kept, rep.KeptBack, rep.Pending, rep.Security)
	}

	full, f := newApt(t, answers, true)
	if rep := full.Check(context.Background(), false); rep.KeptBack != 0 || slices.Contains(f.calls, "apt-get -s -q upgrade") {
		t.Fatalf("full-upgrade: kept back %d, calls %v", rep.KeptBack, f.calls)
	}

	broken := base()
	broken["apt-get -s -q upgrade"] = exit(100, "E: something")
	b, _ := newApt(t, broken, true)
	b.PolicyMode = "upgrade"
	if rep := b.Check(context.Background(), false); rep.KeptBack != 0 || rep.Pending != 6 ||
		rep.State != protocol.SourceUpdatesAvailable {
		t.Fatalf("second simulation failed: %+v", rep)
	}
}

func TestVersionAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"2.1.16": true, "2.1.15": false, "2.2.4": true, "3.2.0": true,
		"2.0.10": false, "2.7.14build2": true, "": false, "x.y": false} {
		if got := versionAtLeast(v, 2, 1, 16); got != want {
			t.Errorf("versionAtLeast(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	a, _ := newApt(t, base(), true)
	a.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if a.Detect() != source.Absent {
		t.Fatal("missing apt-get not reported as absent")
	}
	a.LookPath = func(string) (string, error) { return "/usr/bin/apt-get", nil }
	if a.Detect() != source.Present {
		t.Fatal("apt-get not reported as present")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRebootKernel(t *testing.T) {
	ans := map[string]runner.Result{
		"dpkg --compare-versions 6.8.12-10-pve gt 6.8.12-4-pve": exit(0, ""),
		"dpkg --compare-versions 6.8.12-4-pve gt 6.8.12-10-pve": exit(1, ""),
	}
	a, _ := newApt(t, ans, true)
	write(t, a.Paths.OSRelease, "6.8.12-4-pve\n")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.8.12-4-pve"), "")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.8.12-10-pve"), "")

	r := a.Reboot(context.Background(), false)
	if !r.Required || r.Reason != protocol.RebootKernel || r.Newest != "6.8.12-10-pve" || !slices.Contains(r.Checked, "kernel") {
		t.Fatalf("reboot: %+v", r)
	}
	if r := a.Reboot(context.Background(), true); r.Required || slices.Contains(r.Checked, "kernel") {
		t.Fatalf("container compared kernels: %+v", r)
	}
}

func TestKernelFlavor(t *testing.T) {
	for release, want := range map[string]string{
		"6.8.12-4-pve":        "pve",
		"6.1.0-26-amd64":      "amd64",
		"6.12.30+deb13-amd64": "amd64", // Debian 13 names its kernels so; same flavor as bookworm's -amd64
		"6.10.11+bpo-amd64":   "amd64",
		"6.1.0-26-rt-amd64":   "amd64", // GRUB boots the newest of both anyway
		"6.8.0-45-generic":    "generic",
		"5.15.0-1050-raspi":   "raspi",
		"6.6.20+rpt-rpi-2712": "2712",
		"6.6.20+rpt-rpi-v8":   "v8",
		"6.1.21-v8+":          "v8+",
		"6.1.21+":             "+",
		"6.8.0":               "",
	} {
		if got := kernelFlavor(release); got != want {
			t.Errorf("kernelFlavor(%q) = %q, want %q", release, got, want)
		}
	}
}

// Raspberry Pi OS installs the kernels of several board generations side by side. Only the running flavor counts - the
// fake dpkg has no answer for v8, so a comparison with it would leave the kernel unchecked.
func TestRebootKernelFlavor(t *testing.T) {
	ans := map[string]runner.Result{
		"dpkg --compare-versions 6.6.20+rpt-rpi-2712 gt 6.6.10+rpt-rpi-2712": exit(0, ""),
	}
	a, _ := newApt(t, ans, true)
	for _, k := range []string{"6.6.10+rpt-rpi-2712", "6.6.10+rpt-rpi-v8", "6.6.20+rpt-rpi-2712", "6.6.20+rpt-rpi-v8"} {
		write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-"+k), "")
	}

	write(t, a.Paths.OSRelease, "6.6.20+rpt-rpi-2712\n") // after the reboot
	r := a.Reboot(context.Background(), false)
	if r.Required || r.Newest != "6.6.20+rpt-rpi-2712" || !slices.Contains(r.Checked, "kernel") {
		t.Fatalf("after the reboot: %+v", r)
	}

	write(t, a.Paths.OSRelease, "6.6.10+rpt-rpi-2712\n") // before it
	r = a.Reboot(context.Background(), false)
	if !r.Required || r.Reason != protocol.RebootKernel || r.Newest != "6.6.20+rpt-rpi-2712" {
		t.Fatalf("before the reboot: %+v", r)
	}

	write(t, a.Paths.OSRelease, "6.6.20+rpt-rpi-v7\n") // no installed kernel of the running flavor: no verdict
	if r := a.Reboot(context.Background(), false); r.Required || slices.Contains(r.Checked, "kernel") {
		t.Fatalf("decided without a kernel of the running flavor: %+v", r)
	}
}

func TestRebootKernelUnknownComparison(t *testing.T) {
	a, _ := newApt(t, map[string]runner.Result{}, true) // dpkg cannot run
	write(t, a.Paths.OSRelease, "6.1.0-26-amd64\n")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.1.0-26-amd64"), "")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.1.0-28-amd64"), "")
	if r := a.Reboot(context.Background(), false); r.Required || slices.Contains(r.Checked, "kernel") {
		t.Fatalf("decided without an answer from dpkg: %+v", r)
	}
}

func TestRebootRequiredFile(t *testing.T) {
	a, _ := newApt(t, nil, true)
	write(t, a.Paths.RebootRequired, "*** System restart required ***\n")
	write(t, a.Paths.RebootRequired+".pkgs", "libc6\nlinux-image-6.8.0-45-generic\nlibc6\n")
	r := a.Reboot(context.Background(), true)
	if !r.Required || r.Reason != protocol.RebootKernel ||
		!slices.Equal(r.Packages, []string{"libc6", "linux-image-6.8.0-45-generic"}) {
		t.Fatalf("reboot: %+v", r)
	}
}
