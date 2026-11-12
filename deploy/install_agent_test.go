package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// systemPaths are the fixed paths install-agent.sh writes to or reads from; the sandbox points each into its root.
var systemPaths = []string{"/run/systemd/system", "/etc/systemd/system", "/etc/vordr", "/usr/local/bin",
	"/usr/local/lib/vordr", "/var/lib/vordr"}

// sandbox is one copy of install-agent.sh with its files next to it, its system paths under root, and stand-ins for
// systemctl, journalctl, logger, id, apt-get and sleep first in PATH. The stand-in systemctl logs every call, lists
// the units in the file "running" and calls a unit inactive when it is listed in "down".
type sandbox struct {
	t                     *testing.T
	dir, root, script     string
	bin, cfg, stubs, logf string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash here; the install scripts are tested where bash exists")
	}
	d := t.TempDir()
	s := &sandbox{t: t, dir: d, root: filepath.Join(d, "root"), stubs: filepath.Join(d, "stubs"),
		logf: filepath.Join(d, "systemctl.log")}
	for _, p := range systemPaths {
		must(t, os.MkdirAll(filepath.Join(s.root, p), 0o755))
	}
	must(t, os.MkdirAll(filepath.Join(s.root, "/etc/vordr/targets"), 0o700))
	must(t, os.MkdirAll(s.stubs, 0o755))
	write(t, filepath.Join(s.stubs, "systemctl"), `#!/bin/sh
echo "$*" >> "`+s.logf+`"
case "$1" in
list-units) cat "`+d+`/running" 2>/dev/null; exit 0 ;;
is-active) for u; do :; done; grep -qxF "$u" "`+d+`/down" 2>/dev/null && exit 3; exit 0 ;;
esac
exit 0
`, 0o755)
	for name, body := range map[string]string{"journalctl": "exit 0", "logger": "exit 0", "apt-get": "exit 0",
		"sleep": "exit 0", "id": "echo 0"} {
		write(t, filepath.Join(s.stubs, name), "#!/bin/sh\n"+body+"\n", 0o755)
	}
	// The new agent binary: its policy check fails for a missing file or one that says BROKEN.
	s.bin = filepath.Join(d, "vordr-agent")
	write(t, s.bin, `#!/bin/sh
case "$1" in
version) echo 9.9.9 ;;
check-config) [ -f "$3" ] || { echo "$3: no such file"; exit 1; }
  if grep -q BROKEN "$3"; then echo "$3: broken"; exit 1; fi; echo "ok $3" ;;
hub-check) echo "hub reachable" ;;
identity) printf 'node host-a\nfingerprint 0123\n' ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`, 0o755)
	s.cfg = filepath.Join(d, "agent.yaml")
	write(t, s.cfg, "node: host-a\n", 0o644)

	deploy := filepath.Join(d, "deploy")
	must(t, os.MkdirAll(filepath.Join(deploy, "systemd"), 0o755))
	for _, f := range []string{"systemd/vordr-agent.service", "systemd/vordr-agent-rollback.service", "rollback.sh"} {
		raw, err := os.ReadFile(f)
		must(t, err)
		write(t, filepath.Join(deploy, f), string(raw), 0o644)
	}
	raw, err := os.ReadFile("install-agent.sh")
	must(t, err)
	script := string(raw)
	for _, p := range systemPaths {
		// A path missing from the script fails the test, so a renamed path cannot point the test at the real system.
		if !strings.Contains(script, p) {
			t.Fatalf("install-agent.sh no longer names %s; update the test with the script", p)
		}
		script = strings.ReplaceAll(script, p, filepath.Join(s.root, p))
	}
	s.script = filepath.Join(deploy, "install-agent.sh")
	write(t, s.script, script, 0o755)
	return s
}

// instance makes a proxy instance run, with the given policy file content ("" for no policy file).
func (s *sandbox) instance(name, policy string) {
	s.t.Helper()
	f, err := os.OpenFile(filepath.Join(s.dir, "running"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	must(s.t, err)
	_, err = f.WriteString("vordr-agent@" + name + ".service loaded active running Proxy agent for " + name + "\n")
	must(s.t, err)
	must(s.t, f.Close())
	if policy != "" {
		write(s.t, filepath.Join(s.root, "/etc/vordr/targets", name+".yaml"), policy, 0o600)
	}
}

func (s *sandbox) run() (string, int) {
	s.t.Helper()
	c := exec.Command("bash", s.script, s.bin, s.cfg)
	c.Env = []string{"PATH=" + s.stubs + ":/usr/bin:/bin"}
	out, _ := c.CombinedOutput()
	return string(out), c.ProcessState.ExitCode()
}

func (s *sandbox) calls() []string {
	raw, _ := os.ReadFile(s.logf)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func indexOf(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
}

func (s *sandbox) installed() bool {
	_, err := os.Stat(filepath.Join(s.root, "/usr/local/bin/vordr-agent"))
	return err == nil
}

// The instances share the agent's binary: once the new agent stays up, the running ones are restarted onto it and
// must come back.
func TestInstallRestartsTheRunningInstances(t *testing.T) {
	s := newSandbox(t)
	s.instance("nas-a", "node: nas-a\n")
	s.instance("fw-a", "node: fw-a\n")
	out, code := s.run()
	if code != 0 || !strings.Contains(out, "restarted onto the new binary: vordr-agent@nas-a.service vordr-agent@fw-a.service") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	calls := s.calls()
	agent := indexOf(calls, "restart vordr-agent.service")
	instances := indexOf(calls, "try-restart vordr-agent@nas-a.service vordr-agent@fw-a.service")
	if agent < 0 || instances < agent {
		t.Fatalf("instances restarted before the agent, or not at all:\n%s", strings.Join(calls, "\n"))
	}
	for _, u := range []string{"vordr-agent@nas-a.service", "vordr-agent@fw-a.service"} {
		if indexOf(calls[instances:], "is-active --quiet "+u) < 0 {
			t.Errorf("%s not checked after its restart", u)
		}
	}
	if !s.installed() {
		t.Fatal("binary not installed")
	}
}

// Every check runs before anything changes: an instance whose policy the new binary refuses, or that has none, stops
// the install before the binary or the agent's policy is replaced.
func TestInstallRefusesWhileAnInstancePolicyFails(t *testing.T) {
	for name, policy := range map[string]string{"refused": "node: fw-a\nBROKEN\n", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			s.instance("nas-a", "node: nas-a\n")
			s.instance("fw-a", policy)
			out, code := s.run()
			if code == 0 || !strings.Contains(out, "proxy instance fw-a:") || !strings.Contains(out, "nothing installed") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if s.installed() {
				t.Error("binary installed although an instance's policy fails")
			}
			if _, err := os.Stat(filepath.Join(s.root, "/etc/vordr/agent.yaml")); err == nil {
				t.Error("agent.yaml installed although an instance's policy fails")
			}
			for _, c := range s.calls() {
				if strings.Contains(c, "restart") {
					t.Errorf("%q although nothing may change", c)
				}
			}
		})
	}
}

// An instance that does not stay up with the new binary fails the install and is named.
func TestInstallFailsWhenAnInstanceDoesNotComeBack(t *testing.T) {
	s := newSandbox(t)
	s.instance("nas-a", "node: nas-a\n")
	s.instance("fw-a", "node: fw-a\n")
	write(t, filepath.Join(s.dir, "down"), "vordr-agent@fw-a.service\n", 0o644)
	out, code := s.run()
	if code == 0 || !strings.Contains(out, "proxy instance vordr-agent@fw-a.service did not come back") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A host without proxy instances restarts nothing but its agent.
func TestInstallWithoutInstances(t *testing.T) {
	s := newSandbox(t)
	out, code := s.run()
	if code != 0 || strings.Contains(out, "proxy instances") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, c := range s.calls() {
		if strings.HasPrefix(c, "try-restart") {
			t.Fatalf("%q on a host without instances", c)
		}
	}
	if !s.installed() {
		t.Fatal("binary not installed")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(content), mode))
	must(t, os.Chmod(path, mode))
}
