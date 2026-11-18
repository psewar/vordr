package apt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// What apt reports reaches the hub whole (ADR-0195): its report in each case these tests play - Debian's and Ubuntu's
// simulations, up to date, a refresh that failed with apt's own lines, a simulation that failed, kept back with
// install.mode upgrade - and its need for a reboot from the kernel and from reboot-required.
func TestTheHubTakesWhatAptReports(t *testing.T) {
	ubuntu, err := os.ReadFile(filepath.Join("testdata", "ubuntu-full-upgrade.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var fetchErr strings.Builder
	for i := range 30 {
		fetchErr.WriteString("Err:" + string(rune('1'+i%9)) + " http://deb.example/debian bookworm/main amd64 Packages\n" +
			"  Temporary failure resolving 'deb.example'\n")
	}
	fetchErr.WriteString("E: Some index files failed to download. They have been ignored, or old ones used instead.\n")
	with := func(change func(map[string]runner.Result)) map[string]runner.Result {
		a := base()
		change(a)
		return a
	}
	for name, c := range map[string]struct {
		answers map[string]runner.Result
		mode    string
	}{
		"Debian": {base(), ""},
		"Ubuntu": {with(func(a map[string]runner.Result) { a["apt-get -s -q full-upgrade"] = ok(string(ubuntu)) }), ""},
		"up to date": {with(func(a map[string]runner.Result) {
			a["apt-get -s -q full-upgrade"] = ok("Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove.\n")
		}), ""},
		"a refresh that failed": {with(func(a map[string]runner.Result) {
			a["apt-get update -q --error-on=any"] = exit(100, fetchErr.String())
		}), ""},
		"a simulation that failed": {with(func(a map[string]runner.Result) {
			a["apt-get -s -q full-upgrade"] = exit(100, "E: Unmet dependencies. Try 'apt --fix-broken install'.\n"+
				" libfoo : Depends: libbar (>= 2.0) but 1.9-1 is to be installed\n")
			a["dpkg --audit"] = ok("The following packages are only half configured:\n libfoo\n")
		}), ""},
		"kept back": {with(func(a map[string]runner.Result) {
			a["apt-get -s -q upgrade"] = ok("Inst libssl3 [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian-Security:12/stable-security [amd64])\n")
		}), "upgrade"},
	} {
		a, _ := newApt(t, c.answers, true)
		a.PolicyMode = c.mode
		rep := wire.Report{T: wire.TReport, CheckedAt: a.Now(), Sources: []source.Report{a.Check(context.Background(), true)}}
		if miss := hub.ReportNotTaken(rep); miss != nil {
			t.Errorf("%s: the hub does not take %v", name, miss)
		}
	}

	a, _ := newApt(t, map[string]runner.Result{
		"dpkg --compare-versions 6.8.12-10-pve gt 6.8.12-4-pve": exit(0, ""),
		"dpkg --compare-versions 6.8.12-4-pve gt 6.8.12-10-pve": exit(1, ""),
	}, true)
	write(t, a.Paths.OSRelease, "6.8.12-4-pve\n")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.8.12-4-pve"), "")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.8.12-10-pve"), "")
	write(t, a.Paths.RebootRequired, "*** System restart required ***\n")
	write(t, a.Paths.RebootRequired+".pkgs", "libc6\nlinux-image-6.8.0-45-generic\n")
	r := a.Reboot(context.Background(), false)
	if !r.Required || len(r.Checked) != 2 {
		t.Fatalf("reboot: %+v", r)
	}
	if miss := hub.ReportNotTaken(wire.Report{T: wire.TReport, CheckedAt: a.Now(), Reboot: &r}); miss != nil {
		t.Errorf("reboot: the hub does not take %v", miss)
	}
}
