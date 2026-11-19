package apt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

const ownNS = "pid:[4026531836]"

// proc is one process in a fake /proc: its PID namespace and root (links), the files it maps ("" is anonymous memory)
// and its state in stat. Empty ns, root or state leave that link or file out; noMaps leaves out the map, which a
// kernel thread has (empty) and a gone process has not.
type proc struct {
	ns, root, state string
	maps            []string
	noMaps          bool
}

func running(maps ...string) proc { return proc{ns: ownNS, root: "/", state: "S", maps: maps} }

func link(t *testing.T, path, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// fakeProc builds a /proc under a.Paths.Proc: the agent's own namespace and the given processes.
func fakeProc(t *testing.T, a *Apt, procs map[string]proc) {
	t.Helper()
	link(t, filepath.Join(a.Paths.Proc, "self", "ns", "pid"), ownNS)
	write(t, filepath.Join(a.Paths.Proc, "uptime"), "1.0 1.0\n") // not a process
	for pid, p := range procs {
		dir := filepath.Join(a.Paths.Proc, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if p.ns != "" {
			link(t, filepath.Join(dir, "ns", "pid"), p.ns)
		}
		if p.root != "" {
			link(t, filepath.Join(dir, "root"), p.root)
		}
		if p.state != "" {
			write(t, filepath.Join(dir, "stat"), pid+" (an (odd) name) "+p.state+" 1 1 1 0\n")
		}
		if !p.noMaps {
			var b strings.Builder
			for i, path := range p.maps {
				fmt.Fprintf(&b, "7f%010x-7f%010x r-xp 00000000 08:01 %-8d                   %s\n", i<<12, (i+1)<<12, 1000+i, path)
			}
			write(t, filepath.Join(dir, "maps"), b.String())
		}
	}
}

const (
	libc         = "/usr/lib/x86_64-linux-gnu/libc.so.6"
	libcReplaced = libc + " (deleted)"
)

// A container runs no kernel of its own, so until ADR-0200 nothing answered there: now its processes do.
func TestRebootLibrariesAnswerInContainers(t *testing.T) {
	a, _ := newApt(t, nil, true)
	fakeProc(t, a, map[string]proc{
		"1":   running("/usr/lib/systemd/systemd", libc, "", "[stack]"),
		"2":   {ns: ownNS, root: "/", state: "S"}, // a kernel thread maps nothing
		"117": running("/usr/sbin/cron", libc),
	})
	r := a.Reboot(context.Background(), true)
	if r.Required || !slices.Equal(r.Checked, []string{librarySignal}) {
		t.Fatalf("nothing replaced: %+v", r)
	}
}

func TestRebootLibrariesReplaced(t *testing.T) {
	for _, inContainer := range []bool{true, false} {
		a, _ := newApt(t, nil, true)
		fakeProc(t, a, map[string]proc{
			"1":   running("/usr/lib/systemd/systemd", libc),
			"163": running("/usr/lib/systemd/systemd-logind (deleted)", libcReplaced),
		})
		r := a.Reboot(context.Background(), inContainer)
		if !r.Required || r.Reason != protocol.RebootLibraries || !slices.Contains(r.Checked, librarySignal) {
			t.Fatalf("container %v, replaced libc: %+v", inContainer, r)
		}
	}
}

func TestReplacedPackageFile(t *testing.T) {
	line := func(path string) string { return "7f2a1d200000-7f2a1d228000 r-xp 00028000 08:01 1838290    " + path }
	for path, want := range map[string]bool{
		libcReplaced:                                      true,
		"/usr/sbin/cron (deleted)":                        true,
		"/usr/libexec/polkitd (deleted)":                  true,
		"/lib/x86_64-linux-gnu/libpam.so.0 (deleted)":     true,
		"/usr/lib/x86_64-linux-gnu/odd name.so (deleted)": true, // a path may hold spaces
		libc:                                   false, // the current file
		"/usr/local/bin/vordr-agent (deleted)": false, // not the package manager's
		"/usr/lib/locale/locale-archive (deleted)": false, // data, not code
		"/memfd:doublemapper (deleted)":            false,
		"/dev/shm/sem.x (deleted)":                 false,
		"/tmp/ffiXXXXXX (deleted)":                 false,
		"/home/user/app/lib.so (deleted)":          false,
		"[stack]":                                  false,
		"":                                         false, // anonymous memory
	} {
		if got := replacedPackageFile(line(path)); got != want {
			t.Errorf("%q: %v, want %v", path, got, want)
		}
	}
	if replacedPackageFile("7f2a1d200000-7f2a1d228000 r-xp 00028000 08:01") {
		t.Error("a short line counted")
	}
}

// The processes of a container on this host, and a process under another root, map files that are not this host's.
func TestRebootLibrariesOnlyOwnNamespaceAndRoot(t *testing.T) {
	a, _ := newApt(t, nil, true)
	fakeProc(t, a, map[string]proc{
		"1":    running(libc),
		"4100": {ns: "pid:[4026532412]", root: "/", state: "S", maps: []string{libcReplaced}},
		"4200": {ns: ownNS, root: "/var/lib/machines/build", state: "S", maps: []string{libcReplaced}},
	})
	r := a.Reboot(context.Background(), false)
	if r.Required || !slices.Contains(r.Checked, librarySignal) {
		t.Fatalf("counted another namespace or root: %+v", r)
	}
}

// A zombie has no links and no map left, a gone process not even its stat: neither runs code, neither stops the answer.
func TestRebootLibrariesEndedProcesses(t *testing.T) {
	a, _ := newApt(t, nil, true)
	fakeProc(t, a, map[string]proc{
		"1":   running(libc),
		"300": {state: "Z", noMaps: true},
		"301": {noMaps: true},
	})
	r := a.Reboot(context.Background(), true)
	if r.Required || !slices.Equal(r.Checked, []string{librarySignal}) {
		t.Fatalf("ended processes: %+v", r)
	}
}

// "No" is said only when every running process was seen: one whose map cannot be read leaves the need unknown.
func TestRebootLibrariesUnreadableIsNoAnswer(t *testing.T) {
	a, _ := newApt(t, nil, true)
	fakeProc(t, a, map[string]proc{
		"1":   running(libc),
		"500": {ns: ownNS, root: "/", state: "S", noMaps: true},
	})
	if r := a.Reboot(context.Background(), true); r.Required || slices.Contains(r.Checked, librarySignal) {
		t.Fatalf("answered without seeing every process: %+v", r)
	}
}

func TestRebootLibrariesNeedRootAndProc(t *testing.T) {
	a, _ := newApt(t, nil, false)
	fakeProc(t, a, map[string]proc{"163": running(libcReplaced)})
	if r := a.Reboot(context.Background(), true); r.Required || slices.Contains(r.Checked, librarySignal) {
		t.Fatalf("answered without root: %+v", r)
	}
	a, _ = newApt(t, nil, true) // no /proc
	if r := a.Reboot(context.Background(), true); r.Required || slices.Contains(r.Checked, librarySignal) {
		t.Fatalf("answered without /proc: %+v", r)
	}
}

// The weightiest reason wins: kernel before libraries before other.
func TestRebootLibrariesReason(t *testing.T) {
	replaced := map[string]proc{"163": running(libcReplaced)}

	a, _ := newApt(t, nil, true)
	fakeProc(t, a, replaced)
	write(t, a.Paths.RebootRequired, "*** System restart required ***\n")
	write(t, a.Paths.RebootRequired+".pkgs", "libc6\n")
	r := a.Reboot(context.Background(), true)
	if !r.Required || r.Reason != protocol.RebootLibraries || !slices.Equal(r.Packages, []string{"libc6"}) ||
		!slices.Equal(r.Checked, []string{"reboot_required_file", librarySignal}) {
		t.Fatalf("file says other, libraries replaced: %+v", r)
	}

	a, _ = newApt(t, nil, true)
	fakeProc(t, a, replaced)
	write(t, a.Paths.RebootRequired, "*** System restart required ***\n")
	write(t, a.Paths.RebootRequired+".pkgs", "linux-image-6.8.0-45-generic\n")
	if r := a.Reboot(context.Background(), true); r.Reason != protocol.RebootKernel {
		t.Fatalf("file says kernel, libraries replaced: %+v", r)
	}

	a, _ = newApt(t, map[string]runner.Result{
		"dpkg --compare-versions 6.8.12-10-pve gt 6.8.12-4-pve": exit(0, ""),
		"dpkg --compare-versions 6.8.12-4-pve gt 6.8.12-10-pve": exit(1, ""),
	}, true)
	fakeProc(t, a, replaced)
	write(t, a.Paths.OSRelease, "6.8.12-4-pve\n")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.8.12-4-pve"), "")
	write(t, filepath.Join(a.Paths.BootDir, "vmlinuz-6.8.12-10-pve"), "")
	r = a.Reboot(context.Background(), false)
	if !r.Required || r.Reason != protocol.RebootKernel || !slices.Equal(r.Checked, []string{librarySignal, "kernel"}) {
		t.Fatalf("newer kernel, libraries replaced: %+v", r)
	}
}
