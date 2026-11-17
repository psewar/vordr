package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/psewar/vordr/deploy/deploytest"
	"github.com/psewar/vordr/internal/agent"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
)

const testHub = "wss://hub.example:8443/v1/tunnel"

// testKey is a base64 Ed25519 public key, a different one for each b.
func testKey(b byte) string {
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

// argsOf reads a command line of install's own flags as main does.
func argsOf(t *testing.T, cmdline ...string) installArgs {
	t.Helper()
	var a installArgs
	for i := 0; i < len(cmdline); i++ {
		if !a.takeFlag(cmdline, &i) {
			t.Fatalf("%s is not a flag of install", cmdline[i])
		}
	}
	return a
}

func baseArgs(t *testing.T, more ...string) installArgs {
	t.Helper()
	return argsOf(t, append([]string{"--hub", testHub, "--hub-key", testKey(1)}, more...)...)
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

// Both spellings of each flag; a base64 value keeps its "=" in the inline one.
func TestInstallTakesItsFlags(t *testing.T) {
	a := argsOf(t, "--hub="+testHub, "--hub-key", testKey(1), "--node=web-01", "--ca-file", "ca.pem",
		"--operator-key="+testKey(2), "--backup-operator-key", testKey(3))
	if a.hub != testHub || a.hubKey != testKey(1) || a.node != "web-01" || a.caFile != "ca.pem" ||
		a.operatorKey != testKey(2) || a.backupKey != testKey(3) || a.usageProblem() != "" {
		t.Fatalf("%+v: %s", a, a.usageProblem())
	}
	var b installArgs
	i := 0
	if b.takeFlag([]string{"--config", "x"}, &i) || i != 0 {
		t.Fatal("--config taken as a flag of install")
	}
}

// A command line install cannot take is refused before it looks at the host, naming what is wrong.
func TestInstallUsageProblems(t *testing.T) {
	for want, a := range map[string]installArgs{
		"--hub and --hub-key are required":           argsOf(t, "--hub", testHub),
		"--hub is given twice":                       baseArgs(t, "--hub", testHub),
		"--node needs a value":                       baseArgs(t, "--node"),
		"--node is empty":                            baseArgs(t, "--node="),
		"--backup-operator-key needs --operator-key": baseArgs(t, "--backup-operator-key", testKey(3)),
		"--config does not apply":                    func() installArgs { a := baseArgs(t); a.config = true; return a }(),
		"--replace-config needs a reason":            func() installArgs { a := baseArgs(t); a.replace, a.reason = true, " "; return a }(),
		"unexpected-word does not belong to install": func() installArgs { a := baseArgs(t); a.note("unexpected-word does not belong to install"); return a }(),
	} {
		if got := a.usageProblem(); !strings.Contains(got, want) {
			t.Errorf("%+v: %q, want %q", a, got, want)
		}
	}
}

// The generated agent.yaml holds the node, the hub and, with an operator key, self-update; everything else is left out,
// so that the strict defaults apply (ADR-0006), and check-config accepts it as it is.
func TestInstallPolicy(t *testing.T) {
	host := func() (string, error) { return "Host-A", nil }
	t.Run("minimal", func(t *testing.T) {
		a := baseArgs(t)
		raw, cfg, issues, err := a.policy(host)
		if err != nil || len(issues) > 0 {
			t.Fatalf("%v %v", issues, err)
		}
		text := string(raw)
		if !strings.HasPrefix(text, generatedHeader) || !strings.Contains(text, "\nnode: host-a\n") {
			t.Fatalf("not the header and the host name in lower case:\n%s", text)
		}
		for _, key := range []string{"install:", "reboot:", "agent_update:", "ca_file:", "apps:", "health:", "packages:"} {
			if strings.Contains(text, key) {
				t.Errorf("%s written although no flag asks for it:\n%s", key, text)
			}
		}
		if cfg.Node != "host-a" || cfg.Hub.URL != testHub || cfg.Hub.PublicKey != testKey(1) || cfg.Hub.CAFile != "" {
			t.Errorf("hub section read back as %+v, node %q", cfg.Hub, cfg.Node)
		}
		in := cfg.Install
		if len(in.AllowedScopes) > 0 || in.DefaultScope != "" || in.Auto != policy.AutoOff || cfg.Reboot.Allowed ||
			cfg.AgentUpdate.Enabled || len(cfg.Apps) > 0 || len(cfg.Health) > 0 || cfg.Target != nil || cfg.GuestBackup != nil {
			t.Errorf("not the strict defaults: %+v", cfg)
		}
		path := filepath.Join(t.TempDir(), "agent.yaml")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if code := checkConfig(path); code != 0 {
			t.Errorf("check-config: exit %d", code)
		}
	})
	t.Run("everything", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.WriteFile("hub-ca.pem", []byte("-----BEGIN CERTIFICATE-----\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		a := baseArgs(t, "--node", "web-01.example", "--ca-file", "hub-ca.pem", "--operator-key", testKey(2),
			"--backup-operator-key", testKey(3))
		_, cfg, issues, err := a.policy(host)
		if err != nil || len(issues) > 0 {
			t.Fatalf("%v %v", issues, err)
		}
		wantCA, _ := filepath.Abs("hub-ca.pem")
		u := cfg.AgentUpdate
		if cfg.Node != "web-01.example" || cfg.Hub.CAFile != wantCA || !filepath.IsAbs(cfg.Hub.CAFile) ||
			!u.Enabled || u.PublicKey != testKey(2) || u.BackupPublicKey != testKey(3) {
			t.Errorf("read back as node %q, hub %+v, agent_update %+v", cfg.Node, cfg.Hub, u)
		}
		if len(cfg.Install.AllowedScopes) > 0 || cfg.Reboot.Allowed {
			t.Errorf("an operator key widened the policy: %+v %+v", cfg.Install, cfg.Reboot)
		}
	})
}

// sandboxRun: install against a deploytest sandbox, on a host that passes every check unless a test changes it.
type sandboxRun struct {
	s           *deploytest.Sandbox
	h           installHost
	out         *bytes.Buffer
	tmp         string
	sig         chan os.Signal // the interrupts that reach install
	probes      int
	privateMode fs.FileMode // the private folder's mode, seen while it existed
}

func newSandboxRun(t *testing.T) *sandboxRun {
	t.Helper()
	s := deploytest.New(t)
	r := &sandboxRun{s: s, out: &bytes.Buffer{}, tmp: filepath.Join(s.Dir, "tmp"), sig: make(chan os.Signal, 4)}
	if err := os.MkdirAll(r.tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	// The sandbox's files belong to whoever runs the test: it plays root for them.
	r.h = installHost{goos: "linux", euid: 0, owner: os.Getuid(), root: s.Root, path: s.Path(), tempDir: r.tmp,
		signals:  r.sig,
		hostname: func() (string, error) { return "Host-A", nil },
		self:     func() (string, error) { return s.Bin, nil },
		probe: func(context.Context, policy.Config) agent.ProbeResult {
			r.probes++
			return agent.ProbeResult{Addresses: []string{"192.0.2.10:8443"}}
		},
		files: func(dir string) error {
			if fi, err := os.Stat(dir); err == nil {
				r.privateMode = fi.Mode().Perm()
			}
			return s.WriteFiles(dir)
		},
		out: r.out, errOut: r.out}
	return r
}

// identity gives the host the key pair its agent makes on first start, and the stand-in binary its fingerprint.
func (r *sandboxRun) identity(t *testing.T) string {
	t.Helper()
	id, _, err := identity.LoadOrCreate(filepath.Join(r.s.Root, policy.DefaultStateDir))
	if err != nil {
		t.Fatal(err)
	}
	r.s.Fingerprint(id.Fingerprint())
	return id.Fingerprint()
}

func (r *sandboxRun) tmpLeft(t *testing.T) []string {
	t.Helper()
	left, err := os.ReadDir(r.tmp)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	return names
}

// caFile writes a hub CA file hub-ca.pem with mode into dir and returns its path.
func caFile(t *testing.T, dir string, mode fs.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "hub-ca.pem")
	if err := os.WriteFile(p, []byte("-----BEGIN CERTIFICATE-----\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// mkdir makes dir with exactly mode.
func mkdir(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

// waitFor waits until cond holds, at most 10 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("not within 10 s: %s", what)
		}
	}
}

// Every refusal comes before any change (exit 2, result=refused with its code as the last line): nothing under the
// system paths changes, systemctl only lists units, no private folder is left, and the hub is asked only once every
// check of the host has passed.
func TestInstallRefusesAndChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, r *sandboxRun, a *installArgs)
		line   string
		probes int
		says   string
	}{
		{"usage", func(t *testing.T, r *sandboxRun, a *installArgs) { *a = argsOf(t, "--hub", testHub) },
			"result=refused code=usage", 0, "--hub and --hub-key are required"},
		{"windows", func(t *testing.T, r *sandboxRun, a *installArgs) { r.h.goos = "windows" },
			"result=refused code=not_linux", 0, "vordr-agent install-service --config <agent.yaml>"},
		{"another system", func(t *testing.T, r *sandboxRun, a *installArgs) { r.h.goos = "freebsd" },
			"result=refused code=not_linux", 0, "this is freebsd"},
		{"not root", func(t *testing.T, r *sandboxRun, a *installArgs) { r.h.euid = 1000 },
			"result=refused code=not_root", 0, "run it as root"},
		{"no systemd", func(t *testing.T, r *sandboxRun, a *installArgs) {
			if err := os.Remove(filepath.Join(r.s.Root, "/run/systemd/system")); err != nil {
				t.Fatal(err)
			}
		}, "result=refused code=no_systemd", 0, "systemd is not running"},
		{"no apt", func(t *testing.T, r *sandboxRun, a *installArgs) { r.s.Remove("apt-get"); r.h.path = r.s.Stubs },
			"result=refused code=no_apt", 0, "no apt-get"},
		{"invalid hub key", func(t *testing.T, r *sandboxRun, a *installArgs) { a.hubKey = "not-a-key" },
			"result=refused code=config_invalid", 0, "--hub-key (hub.public_key)"},
		{"hub key of another length", func(t *testing.T, r *sandboxRun, a *installArgs) {
			a.hubKey = base64.StdEncoding.EncodeToString(make([]byte, 16))
		}, "result=refused code=config_invalid", 0, "--hub-key (hub.public_key)"},
		{"not a wss url", func(t *testing.T, r *sandboxRun, a *installArgs) { a.hub = "https://hub.example:8443/v1/tunnel" },
			"result=refused code=config_invalid", 0, "--hub (hub.url)"},
		{"host name is no node name", func(t *testing.T, r *sandboxRun, a *installArgs) {
			r.h.hostname = func() (string, error) { return "web_01", nil }
		}, "result=refused code=config_invalid", 0, "--node (node, here from the host name)"},
		{"invalid node", func(t *testing.T, r *sandboxRun, a *installArgs) { *a = baseArgs(t, "--node", "Web 01") },
			"result=refused code=config_invalid", 0, "--node (node)"},
		{"ca file missing", func(t *testing.T, r *sandboxRun, a *installArgs) {
			*a = baseArgs(t, "--ca-file", filepath.Join(r.s.Dir, "missing.pem"))
		}, "result=refused code=config_invalid", 0, "--ca-file (hub.ca_file)"},
		{"backup key is the operator key", func(t *testing.T, r *sandboxRun, a *installArgs) {
			*a = baseArgs(t, "--operator-key", testKey(2), "--backup-operator-key", testKey(2))
		}, "result=refused code=config_invalid", 0, "--backup-operator-key (agent_update.backup_public_key)"},
		{"installed agent.yaml differs", func(t *testing.T, r *sandboxRun, a *installArgs) {
			if err := os.WriteFile(filepath.Join(r.s.Root, policy.DefaultConfigPath), []byte("node: host-b\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "result=refused code=config_differs", 0, "-   1  node: host-b"},
		{"binary unknown", func(t *testing.T, r *sandboxRun, a *installArgs) {
			r.h.self = func() (string, error) { return "", errors.New("replaced since this run started") }
		}, "result=refused code=prepare_failed", 0, "replaced since this run started"},
		{"hub unreachable", func(t *testing.T, r *sandboxRun, a *installArgs) {
			r.h.probe = func(context.Context, policy.Config) agent.ProbeResult {
				r.probes++
				return agent.ProbeResult{Failed: agent.StageConnect, Detail: "connection refused"}
			}
		}, "result=refused code=hub_unreachable stage=connect", 1, "failed at stage connect"},
		{"no bash", func(t *testing.T, r *sandboxRun, a *installArgs) { r.h.path = r.s.Stubs },
			"result=refused code=prepare_failed", 1, "no bash"},
		{"ca file under a folder of others", func(t *testing.T, r *sandboxRun, a *installArgs) {
			// The test's folder lies under the system's temporary folder, which is not the test user's.
			*a = baseArgs(t, "--ca-file", caFile(t, r.s.Dir, 0o644))
		}, "result=refused code=ca_file_unsafe", 0, "then --ca-file /etc/vordr/hub-ca.pem"},
		{"ca file group writable", func(t *testing.T, r *sandboxRun, a *installArgs) {
			*a = baseArgs(t, "--ca-file", caFile(t, filepath.Join(r.s.Root, "/etc/vordr"), 0o664))
		}, "result=refused code=ca_file_unsafe", 0, "may be written by group or others"},
		{"ca file in a folder others may write", func(t *testing.T, r *sandboxRun, a *installArgs) {
			dir := filepath.Join(r.s.Root, "/etc/vordr/open")
			mkdir(t, dir, 0o777)
			*a = baseArgs(t, "--ca-file", caFile(t, dir, 0o644))
		}, "result=refused code=ca_file_unsafe", 0, "/etc/vordr/open may be written by group or others"},
		{"ca file of another user", func(t *testing.T, r *sandboxRun, a *installArgs) {
			*a = baseArgs(t, "--ca-file", caFile(t, filepath.Join(r.s.Root, "/etc/vordr"), 0o644))
			r.h.owner = os.Getuid() + 1
		}, "result=refused code=ca_file_unsafe", 0, "hub-ca.pem belongs to uid"},
		{"private folder's place writable by others", func(t *testing.T, r *sandboxRun, a *installArgs) {
			mkdir(t, r.tmp, 0o777)
		}, "result=refused code=temp_dir_unsafe", 1, "may be written by others"},
		{"private folder's place of another user", func(t *testing.T, r *sandboxRun, a *installArgs) {
			r.h.owner = os.Getuid() + 1
		}, "result=refused code=temp_dir_unsafe", 1, "belongs to uid"},
		{"private folder's place a symbolic link", func(t *testing.T, r *sandboxRun, a *installArgs) {
			link := filepath.Join(r.s.Dir, "tmp-link")
			if err := os.Symlink(r.tmp, link); err != nil {
				t.Fatal(err)
			}
			r.h.tempDir = link
		}, "result=refused code=temp_dir_unsafe", 1, "is no folder"},
		{"installer refuses", func(t *testing.T, r *sandboxRun, a *installArgs) { r.s.Instance("fw-a", "node: fw-a\nBROKEN\n") },
			"result=refused code=installer_refused", 1, "proxy instance fw-a"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newSandboxRun(t)
			a := baseArgs(t)
			c.setup(t, r, &a)
			before := r.s.Tree()
			code := install(r.h, a)
			out := r.out.String()
			if code != 2 || lastLine(out) != c.line || !strings.Contains(out, c.says) {
				t.Fatalf("exit %d, want 2 and last line %q saying %q:\n%s", code, c.line, c.says, out)
			}
			if after := r.s.Tree(); !slices.Equal(before, after) {
				t.Errorf("changed although refused:\nbefore %v\nafter  %v", before, after)
			}
			for _, call := range r.s.Calls() {
				if !strings.HasPrefix(call, "list-units") {
					t.Errorf("systemctl %q although nothing may change", call)
				}
			}
			if left := r.tmpLeft(t); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
			if r.probes != c.probes {
				t.Errorf("hub asked %d times, want %d", r.probes, c.probes)
			}
		})
	}
}

// The whole install against the stand-ins: the generated agent.yaml and the running binary in place, the service
// enabled and started, the private folder (0700) gone, and the fingerprint of the host's identity in the last line.
func TestInstallSetsUpTheHost(t *testing.T) {
	r := newSandboxRun(t)
	fp := r.identity(t)
	a := baseArgs(t, "--operator-key", testKey(2))
	code := install(r.h, a)
	out := r.out.String()
	if want := "result=installed node=host-a fingerprint=" + fp; code != 0 || lastLine(out) != want {
		t.Fatalf("exit %d, want 0 and last line %q:\n%s", code, want, out)
	}
	if !strings.Contains(out, "vordr-hub approve host-a --fingerprint "+fp) {
		t.Errorf("no approval command for the hub:\n%s", out)
	}
	raw, _, _, _ := a.policy(r.h.hostname)
	if got := r.s.Policy(); got != string(raw) {
		t.Errorf("installed agent.yaml:\n%s\nwant:\n%s", got, raw)
	}
	bin, _ := os.ReadFile(r.s.Bin)
	if got, _ := os.ReadFile(filepath.Join(r.s.Root, "/usr/local/bin/vordr-agent")); string(got) != string(bin) {
		t.Error("the installed binary is not the one that ran install")
	}
	calls := r.s.Calls()
	for _, want := range []string{"daemon-reload", "enable --quiet vordr-agent.service", "restart vordr-agent.service"} {
		if deploytest.IndexOf(calls, want) < 0 {
			t.Errorf("no systemctl %s:\n%s", want, strings.Join(calls, "\n"))
		}
	}
	if r.privateMode != 0o700 {
		t.Errorf("private folder mode %v, want 0700", r.privateMode)
	}
	if left := r.tmpLeft(t); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}

	// The same command again keeps the same agent.yaml: no reason needed, no backup.
	r.out.Reset()
	if code := install(r.h, a); code != 0 || lastLine(r.out.String()) != "result=installed node=host-a fingerprint="+fp {
		t.Fatalf("second run: exit %d:\n%s", code, r.out.String())
	}
	if backups, _ := filepath.Glob(filepath.Join(r.s.Root, "/etc/vordr/agent.yaml.bak-*")); len(backups) > 0 {
		t.Errorf("backup %v of an unchanged agent.yaml", backups)
	}
}

// A different agent.yaml is replaced only with a reason, which goes through to install-agent.sh: the old one is kept.
func TestInstallReplacesTheConfigWithAReason(t *testing.T) {
	r := newSandboxRun(t)
	fp := r.identity(t)
	installed := filepath.Join(r.s.Root, policy.DefaultConfigPath)
	if err := os.WriteFile(installed, []byte("node: host-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := baseArgs(t)
	a.replace, a.reason = true, "set up anew"
	if code := install(r.h, a); code != 0 || lastLine(r.out.String()) != "result=installed node=host-a fingerprint="+fp {
		t.Fatalf("exit %d:\n%s", code, r.out.String())
	}
	backups, _ := filepath.Glob(installed + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("backups %v, want one", backups)
	}
	if old, _ := os.ReadFile(backups[0]); string(old) != "node: host-b\n" {
		t.Errorf("backup holds %q", old)
	}
	if !strings.Contains(r.s.Policy(), "node: host-a") {
		t.Errorf("not replaced:\n%s", r.s.Policy())
	}
}

// Once install-agent.sh has begun to change the host, a failure is exit 1, result=failed, and the private folder is
// gone all the same.
func TestInstallFailureAfterAChange(t *testing.T) {
	r := newSandboxRun(t)
	r.identity(t)
	r.s.Down("vordr-agent.service")
	code := install(r.h, baseArgs(t))
	out := r.out.String()
	if code != 1 || lastLine(out) != "result=failed code=install_failed" || !strings.Contains(out, "did not stay up") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if left := r.tmpLeft(t); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// Installed, but without an identity to read there is no fingerprint to report: exit 1, not a made-up result.
func TestInstallWithoutIdentity(t *testing.T) {
	r := newSandboxRun(t)
	code := install(r.h, baseArgs(t))
	out := r.out.String()
	if code != 1 || lastLine(out) != "result=failed code=identity_unreadable" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A command of the installer that fails unexpectedly after the first change (not one of its checks) is exit 1 and
// result=failed through the installer's trap, never "refused, nothing changed" for a host that is half installed.
func TestInstallUnexpectedFailureAfterAChange(t *testing.T) {
	r := newSandboxRun(t)
	r.identity(t)
	r.s.Fail("daemon-reload")
	code := install(r.h, baseArgs(t))
	out := r.out.String()
	if code != 1 || lastLine(out) != "result=failed code=install_failed" {
		t.Fatalf("exit %d, want 1 and result=failed code=install_failed:\n%s", code, out)
	}
	if !r.s.Installed() {
		t.Fatal("daemon-reload failed before the binary was installed; the test no longer fails after a change")
	}
	if left := r.tmpLeft(t); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// A CA file in a place only root may change is taken as it is and named in agent.yaml.
func TestInstallTakesACAFileOnlyRootMayChange(t *testing.T) {
	r := newSandboxRun(t)
	fp := r.identity(t)
	ca := caFile(t, filepath.Join(r.s.Root, "/etc/vordr"), 0o644)
	if code := install(r.h, baseArgs(t, "--ca-file", ca)); code != 0 ||
		lastLine(r.out.String()) != "result=installed node=host-a fingerprint="+fp {
		t.Fatalf("exit %d:\n%s", code, r.out.String())
	}
	if !strings.Contains(r.s.Policy(), "ca_file: "+ca+"\n") {
		t.Errorf("agent.yaml does not name the CA file:\n%s", r.s.Policy())
	}
}

// Nothing of the caller's environment reaches the installer, which runs as root: not BASH_ENV or ENV (a file bash
// reads first), SHELLOPTS and PS4 (xtrace with a command in its prompt), nor anything else; its PATH is install's own.
func TestInstallGivesTheInstallerAFixedEnvironment(t *testing.T) {
	r := newSandboxRun(t)
	r.identity(t)
	ran := filepath.Join(r.s.Dir, "caller-code-ran")
	rc := filepath.Join(r.s.Dir, "caller-rc")
	if err := os.WriteFile(rc, []byte("touch "+ran+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", rc)
	t.Setenv("ENV", rc)
	t.Setenv("SHELLOPTS", "xtrace")
	t.Setenv("PS4", "$(touch "+ran+")")
	t.Setenv("VORDR_FROM_THE_CALLER", "1")
	env := filepath.Join(r.s.Dir, "installer-env")
	r.s.Stub("id", "env > "+env+"\necho 0")
	if code := install(r.h, baseArgs(t)); code != 0 {
		t.Fatalf("exit %d:\n%s", code, r.out.String())
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the caller's BASH_ENV, SHELLOPTS or PS4 ran code in the installer")
	}
	raw, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for _, name := range []string{"BASH_ENV", "ENV", "SHELLOPTS", "PS4", "VORDR_FROM_THE_CALLER"} {
		for _, l := range lines {
			if strings.HasPrefix(l, name+"=") {
				t.Errorf("%s reached the installer", l)
			}
		}
	}
	for _, want := range []string{"PATH=" + r.h.path, "LANG=C", "LC_ALL=C"} {
		if !slices.Contains(lines, want) {
			t.Errorf("%s not in the installer's environment:\n%s", want, raw)
		}
	}
}

// realHost takes nothing of the caller's environment for where things are: the private folder goes to /run and
// commands are looked up in the system's folders, whatever TMPDIR and PATH say.
func TestRealHostIgnoresTheCallersEnvironment(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("PATH", "::.:"+t.TempDir())
	h := realHost()
	signal.Reset(append(slices.Clone(interrupts), syscall.SIGPIPE)...) // realHost took them for install
	if h.tempDir != "/run" || h.path != systemPath || h.owner != 0 || h.signals == nil {
		t.Fatalf("tempDir %q, path %q, owner %d, signals %v", h.tempDir, h.path, h.owner, h.signals != nil)
	}
	for _, d := range filepath.SplitList(h.path) {
		if !filepath.IsAbs(d) {
			t.Errorf("relative folder %q in PATH", d)
		}
	}
}

// An interrupt that reaches install before the installer starts: refused, nothing changed, no private folder left.
func TestInstallInterruptedBeforeTheInstaller(t *testing.T) {
	r := newSandboxRun(t)
	r.h.probe = func(context.Context, policy.Config) agent.ProbeResult {
		r.probes++
		r.sig <- syscall.SIGTERM // while the hub is asked
		return agent.ProbeResult{Addresses: []string{"192.0.2.10:8443"}}
	}
	before := r.s.Tree()
	code := install(r.h, baseArgs(t))
	out := r.out.String()
	if code != 2 || lastLine(out) != "result=refused code=interrupted" {
		t.Fatalf("exit %d, want 2 and result=refused code=interrupted:\n%s", code, out)
	}
	if after := r.s.Tree(); !slices.Equal(before, after) {
		t.Errorf("changed although interrupted before the installer:\nbefore %v\nafter  %v", before, after)
	}
	if calls := r.s.Calls(); len(calls) > 0 {
		t.Errorf("systemctl %v although the installer never started", calls)
	}
	if left := r.tmpLeft(t); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// An interrupt while the installer runs goes on to it; install waits for it to end once its running command has, and
// tells by its exit code whether it had changed anything: refused before the first change, failed after it, both as
// "interrupted". The private folder is gone either way.
func TestInstallInterruptedWhileTheInstallerRuns(t *testing.T) {
	for _, c := range []struct {
		name, hold, line string
		exit             int
		installed        bool
	}{
		{"before the first change", "list-units", "result=refused code=interrupted", 2, false},
		{"after the first change", "daemon-reload", "result=failed code=interrupted", 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newSandboxRun(t)
			r.identity(t)
			r.s.Hold(c.hold)
			a := baseArgs(t)
			result := make(chan int, 1)
			go func() { result <- install(r.h, a) }()
			waitFor(t, "systemctl "+c.hold+" waits", func() bool { return r.s.Held(c.hold) })
			r.sig <- syscall.SIGTERM
			waitFor(t, "install took the signal", func() bool { return len(r.sig) == 0 })
			time.Sleep(100 * time.Millisecond) // the signal on its way to the installer
			r.s.Release(c.hold)
			var code int
			select {
			case code = <-result:
			case <-time.After(20 * time.Second):
				t.Fatal("install did not end")
			}
			out := r.out.String()
			if code != c.exit || lastLine(out) != c.line {
				t.Fatalf("exit %d, want %d and %q:\n%s", code, c.exit, c.line, out)
			}
			if r.s.Installed() != c.installed {
				t.Errorf("binary installed: %v, want %v", r.s.Installed(), c.installed)
			}
			if left := r.tmpLeft(t); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

// A reader of install's output that goes away (a closed pipe) does not end the install halfway.
func TestInstallGoesOnWhenItsReaderIsGone(t *testing.T) {
	r := newSandboxRun(t)
	r.identity(t)
	r.h.out, r.h.errOut = goneReader{}, goneReader{}
	if code := install(r.h, baseArgs(t)); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !r.s.Installed() || r.s.Policy() == "" {
		t.Fatal("not installed")
	}
}

// goneReader is the output of a reader that went away.
type goneReader struct{}

func (goneReader) Write([]byte) (int, error) { return 0, syscall.EPIPE }

// The binary that runs is the one to install: the path of this test binary, as it runs.
func TestRunningBinary(t *testing.T) {
	if _, err := os.Stat("/proc/self/exe"); err != nil {
		t.Skip("no /proc here")
	}
	p, err := runningBinary()
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if want, _ := filepath.EvalSymlinks(self); p != want {
		t.Errorf("%s, want %s", p, want)
	}
}
