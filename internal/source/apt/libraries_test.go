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
	"github.com/psewar/vordr/internal/source"
)

const ownNS = "pid:[4026531836]"

// proc is one process in a fake /proc: its PID namespace and root (links), the files it maps ("" is anonymous memory),
// its state in stat, its cgroup and its name. Empty ns, root, state, cgroup or comm leave that link or file out;
// noMaps leaves out the map, which a kernel thread has (empty) and a gone process has not.
type proc struct {
	ns, root, state string
	cgroup, comm    string
	maps            []string
	noMaps          bool
}

// running: a process of a login session, named "proc".
func running(maps ...string) proc {
	return proc{ns: ownNS, root: "/", state: "S", cgroup: "0::/user.slice/user-0.slice/session-1.scope\n", comm: "proc\n",
		maps: maps}
}

// service: a process of a systemd service.
func service(unit string, maps ...string) proc {
	p := running(maps...)
	p.cgroup, p.comm = "0::/system.slice/"+unit+"\n", strings.TrimSuffix(unit, ".service")+"\n"
	return p
}

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
		if p.cgroup != "" {
			write(t, filepath.Join(dir, "cgroup"), p.cgroup)
		}
		if p.comm != "" {
			write(t, filepath.Join(dir, "comm"), p.comm)
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
			"163": service("systemd-logind.service", "/usr/lib/systemd/systemd-logind (deleted)", libcReplaced),
		})
		r := a.Reboot(context.Background(), inContainer)
		if !r.Required || r.Reason != protocol.RebootLibraries || !slices.Contains(r.Checked, librarySignal) ||
			!slices.Equal(r.Services, []string{"systemd-logind.service"}) {
			t.Fatalf("container %v, replaced libc: %+v", inContainer, r)
		}
	}
}

// ADR-0201: each process on a replaced library is named by its service - the innermost one, a user's unit too - or by
// its name where it belongs to none; each name once, sorted. A process on current files is not named.
func TestRebootLibrariesNamesTheServices(t *testing.T) {
	a, _ := newApt(t, nil, true)
	pid1 := running(libcReplaced)
	pid1.cgroup, pid1.comm = "0::/init.scope\n", "systemd\n"
	getty := running(libcReplaced)
	getty.cgroup = "0::/system.slice/system-getty.slice/getty@tty1.service\n"
	syncthing := running(libcReplaced)
	syncthing.cgroup = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/syncthing.service\n"
	shell := running(libcReplaced)
	shell.comm = "bash\n"
	fakeProc(t, a, map[string]proc{
		"1":   pid1,
		"156": service("cron.service", "/usr/sbin/cron", libcReplaced),
		"157": service("cron.service", "/usr/sbin/cron", libcReplaced), // a second process of the same service
		"166": getty,
		"200": service("nginx.service", "/usr/sbin/nginx", libc), // current files: nothing to restart
		"900": shell,
		"901": syncthing,
	})
	r := a.Reboot(context.Background(), true)
	want := []string{"bash", "cron.service", "getty@tty1.service", "syncthing.service", "systemd"}
	if !r.Required || r.Reason != protocol.RebootLibraries || !slices.Equal(r.Services, want) {
		t.Fatalf("services: %+v, want %v", r, want)
	}
}

// A process whose map showed a replaced library but which ended before it could be named runs no old code any more.
func TestRebootLibrariesAProcessEndedBeforeItsName(t *testing.T) {
	a, _ := newApt(t, nil, true)
	gone := running(libcReplaced)
	gone.cgroup, gone.comm = "", ""
	fakeProc(t, a, map[string]proc{"1": running(libc), "700": gone})
	r := a.Reboot(context.Background(), true)
	if r.Required || r.Services != nil || !slices.Equal(r.Checked, []string{librarySignal}) {
		t.Fatalf("named a process that ended: %+v", r)
	}
}

func TestRebootLibrariesNamesAtMostTheBound(t *testing.T) {
	a, _ := newApt(t, nil, true)
	procs := map[string]proc{}
	for i := range source.MaxRebootServices + 8 {
		procs[fmt.Sprint(1000+i)] = service(fmt.Sprintf("s%02d.service", i), libcReplaced)
	}
	fakeProc(t, a, procs)
	r := a.Reboot(context.Background(), true)
	if len(r.Services) != source.MaxRebootServices || r.Services[0] != "s00.service" || !r.Required {
		t.Fatalf("bound: %d services, first %q", len(r.Services), r.Services[0])
	}
}

func TestServiceOf(t *testing.T) {
	for cgroup, want := range map[string]string{
		"0::/system.slice/cron.service\n":                                          "cron.service",
		"0::/system.slice/system-getty.slice/getty@tty1.service\n":                 "getty@tty1.service",
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/foo.service\n": "foo.service",
		"0::/user.slice/user-1000.slice/user@1000.service/init.scope\n":            "user@1000.service",
		"0::/user.slice/user-0.slice/session-3.scope\n":                            "",
		"0::/init.scope\n": "",
		"12:pids:/system.slice/cron.service\n1:name=systemd:/system.slice/cron.service\n": "cron.service", // cgroup v1
		"1:name=systemd:/\n0::/system.slice/cron.service\n":                               "cron.service", // hybrid
		"12:pids:/system.slice/cron.service\n":                                            "",             // not systemd's
		"":                                                                                "",
	} {
		if got := serviceOf(cgroup); got != want {
			t.Errorf("serviceOf(%q) = %q, want %q", cgroup, got, want)
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
	if !r.Required || r.Reason != protocol.RebootKernel || !slices.Equal(r.Checked, []string{librarySignal, "kernel"}) ||
		!slices.Equal(r.Services, []string{"proc"}) { // named under the kernel too: restarting them is still due
		t.Fatalf("newer kernel, libraries replaced: %+v", r)
	}
}
