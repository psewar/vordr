// Package deploytest runs deploy/install-agent.sh against stand-ins, for the tests of the script and of vordr-agent
// install (ADR-0169): the script's system paths point into a temporary root, stand-ins for systemctl, journalctl,
// logger, id, apt-get and sleep come first in PATH, and a stand-in plays the new agent binary. Nothing outside the
// test's temporary folder is touched.
package deploytest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/psewar/vordr/deploy"
)

// SystemPaths are the fixed paths install-agent.sh writes to or reads from; the sandbox points each into its root.
var SystemPaths = []string{"/run/systemd/system", "/etc/systemd/system", "/etc/vordr", "/usr/local/bin",
	"/usr/local/lib/vordr", "/var/lib/vordr"}

// Sandbox is one copy of install-agent.sh with its files next to it, its system paths under Root, and the stand-ins.
// The stand-in systemctl logs every call, lists the units in the file "running", calls a unit inactive when it is
// listed in "down" (Down), fails a subcommand listed in "fail" (Fail) and waits in a subcommand while Hold holds it.
// The stand-in agent binary (Bin) answers version, check-config (it refuses a missing file and one that says BROKEN),
// hub-check (it ends with the exit code HubCheck sets, 0 by default) and identity (node host-a, the fingerprint in
// Fingerprint).
type Sandbox struct {
	t *testing.T
	// Dir: the test's folder; Root: where the system paths point; Stubs: the stand-ins, first in PATH.
	Dir, Root, Stubs string
	// Script: install-agent.sh with its files next to it; Bin and Cfg: the agent binary and agent.yaml it is given.
	Script, Bin, Cfg string
	logf             string
}

// New builds a sandbox; it skips the test where there is no bash.
func New(t *testing.T) *Sandbox {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash here; the install scripts are tested where bash exists")
	}
	d := t.TempDir()
	s := &Sandbox{t: t, Dir: d, Root: filepath.Join(d, "root"), Stubs: filepath.Join(d, "stubs"),
		logf: filepath.Join(d, "systemctl.log")}
	for _, p := range SystemPaths {
		must(t, os.MkdirAll(filepath.Join(s.Root, p), 0o755))
	}
	must(t, os.MkdirAll(filepath.Join(s.Root, "/etc/vordr/targets"), 0o700))
	must(t, os.MkdirAll(s.Stubs, 0o755))
	s.Stub("systemctl", `echo "$*" >> "`+s.logf+`"
if [ -e "`+d+`/hold-$1" ]; then
  : > "`+d+`/held-$1"
  while [ -e "`+d+`/hold-$1" ]; do /usr/bin/sleep 0.02; done
fi
grep -qxF "$1" "`+d+`/fail" 2>/dev/null && exit 1
case "$1" in
list-units) cat "`+d+`/running" 2>/dev/null; exit 0 ;;
is-active) for u; do :; done; grep -qxF "$u" "`+d+`/down" 2>/dev/null && exit 3; exit 0 ;;
esac
exit 0`)
	for name, body := range map[string]string{"journalctl": "exit 0", "logger": "exit 0", "apt-get": "exit 0",
		"sleep": "exit 0", "id": "echo 0"} {
		s.Stub(name, body)
	}
	s.Bin = filepath.Join(d, "vordr-agent")
	s.Fingerprint("0123")
	s.Cfg = filepath.Join(d, "agent.yaml")
	write(t, s.Cfg, "node: host-a\n", 0o644)

	dir := filepath.Join(d, "deploy")
	must(t, os.MkdirAll(dir, 0o755))
	must(t, s.WriteFiles(dir))
	s.Script = filepath.Join(dir, "install-agent.sh")
	return s
}

// Fingerprint sets what the stand-in agent binary prints as its fingerprint.
func (s *Sandbox) Fingerprint(fp string) {
	write(s.t, s.Bin, `#!/bin/sh
case "$1" in
version) echo 9.9.9 ;;
check-config) [ -f "$3" ] || { echo "$3: no such file"; exit 1; }
  if grep -q BROKEN "$3"; then echo "$3: broken"; exit 1; fi; echo "ok $3" ;;
hub-check) echo "hub reachable"; [ ! -f "`+s.Dir+`/hubcheck" ] || exit "$(cat "`+s.Dir+`/hubcheck")" ;;
identity) printf 'node host-a\nfingerprint `+fp+`\n' ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`, 0o755)
}

// HubCheck makes the stand-in hub-check end with this exit code, after it has printed its line.
func (s *Sandbox) HubCheck(code int) {
	s.t.Helper()
	write(s.t, filepath.Join(s.Dir, "hubcheck"), strconv.Itoa(code)+"\n", 0o644)
}

// WriteFiles writes deploy.AgentFiles into dir as deploy.WriteAgentFiles does, with the system paths of
// install-agent.sh pointed into Root. A path the script no longer names fails the test, so that a renamed path cannot
// point the script at the real system.
func (s *Sandbox) WriteFiles(dir string) error {
	for _, f := range deploy.AgentFiles {
		raw, err := deploy.AgentFS.ReadFile(f.Name)
		if err != nil {
			return err
		}
		if f.Name == "install-agent.sh" {
			script := string(raw)
			for _, p := range SystemPaths {
				if !strings.Contains(script, p) {
					s.t.Fatalf("install-agent.sh no longer names %s; update deploytest with the script", p)
				}
				script = strings.ReplaceAll(script, p, filepath.Join(s.Root, p))
			}
			raw = []byte(script)
		}
		if err := deploy.WriteNew(filepath.Join(dir, filepath.FromSlash(f.Name)), raw, f.Mode); err != nil {
			return err
		}
	}
	return nil
}

// Stub replaces (or adds) the stand-in for a command.
func (s *Sandbox) Stub(name, body string) {
	s.t.Helper()
	write(s.t, filepath.Join(s.Stubs, name), "#!/bin/sh\n"+body+"\n", 0o755)
}

// Remove takes a stand-in away, so that the command is missing from PATH.
func (s *Sandbox) Remove(name string) {
	s.t.Helper()
	must(s.t, os.Remove(filepath.Join(s.Stubs, name)))
}

// Path is the PATH the script runs with: the stand-ins first, then only the system folders for sh, bash and coreutils.
func (s *Sandbox) Path() string { return s.Stubs + ":/usr/bin:/bin" }

// Instance makes a proxy instance run, with the given policy file content ("" for no policy file).
func (s *Sandbox) Instance(name, policy string) {
	s.t.Helper()
	f, err := os.OpenFile(filepath.Join(s.Dir, "running"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	must(s.t, err)
	_, err = f.WriteString("vordr-agent@" + name + ".service loaded active running Proxy agent for " + name + "\n")
	must(s.t, err)
	must(s.t, f.Close())
	if policy != "" {
		write(s.t, filepath.Join(s.Root, "/etc/vordr/targets", name+".yaml"), policy, 0o600)
	}
}

// Down makes the stand-in systemctl call a unit inactive.
func (s *Sandbox) Down(unit string) {
	s.t.Helper()
	f, err := os.OpenFile(filepath.Join(s.Dir, "down"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	must(s.t, err)
	_, err = f.WriteString(unit + "\n")
	must(s.t, err)
	must(s.t, f.Close())
}

// Fail makes the stand-in systemctl fail (exit 1) in a subcommand, e.g. daemon-reload: a command that fails
// unexpectedly, not a check of the script.
func (s *Sandbox) Fail(subcommand string) {
	s.t.Helper()
	f, err := os.OpenFile(filepath.Join(s.Dir, "fail"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	must(s.t, err)
	_, err = f.WriteString(subcommand + "\n")
	must(s.t, err)
	must(s.t, f.Close())
}

// Hold makes the stand-in systemctl wait in a subcommand until Release; Held reports whether it got there.
func (s *Sandbox) Hold(subcommand string) {
	s.t.Helper()
	write(s.t, filepath.Join(s.Dir, "hold-"+subcommand), "", 0o644)
}

// Held reports whether the stand-in systemctl waits (or waited) in the subcommand Hold holds.
func (s *Sandbox) Held(subcommand string) bool {
	_, err := os.Stat(filepath.Join(s.Dir, "held-"+subcommand))
	return err == nil
}

// Release lets the stand-in systemctl go on from a subcommand Hold held.
func (s *Sandbox) Release(subcommand string) {
	s.t.Helper()
	must(s.t, os.Remove(filepath.Join(s.Dir, "hold-"+subcommand)))
}

// Run runs the script with Bin, Cfg and args; it returns the output and the exit code.
func (s *Sandbox) Run(args ...string) (string, int) {
	s.t.Helper()
	c := exec.Command("bash", append([]string{s.Script, s.Bin, s.Cfg}, args...)...)
	c.Env = []string{"PATH=" + s.Path()}
	out, _ := c.CombinedOutput()
	return string(out), c.ProcessState.ExitCode()
}

// Calls are the stand-in systemctl's calls, oldest first; none: an empty list.
func (s *Sandbox) Calls() []string {
	raw, _ := os.ReadFile(s.logf)
	if strings.TrimSpace(string(raw)) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// Installed reports whether the agent binary is in place.
func (s *Sandbox) Installed() bool { return s.exists("/usr/local/bin/vordr-agent") }

// Policy is the installed agent.yaml; "" when there is none.
func (s *Sandbox) Policy() string {
	raw, _ := os.ReadFile(filepath.Join(s.Root, "/etc/vordr/agent.yaml"))
	return string(raw)
}

// Tree lists every file and folder under Root with its size and mode, to compare before and after a run that must not
// change anything.
func (s *Sandbox) Tree() []string {
	s.t.Helper()
	var out []string
	must(s.t, filepath.WalkDir(s.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.Root, path)
		raw := ""
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			raw = string(b)
		}
		out = append(out, rel+" "+fi.Mode().String()+" "+raw)
		return nil
	}))
	return out
}

func (s *Sandbox) exists(p string) bool {
	_, err := os.Stat(filepath.Join(s.Root, p))
	return err == nil
}

// IndexOf is the position of want in lines, or -1.
func IndexOf(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
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
