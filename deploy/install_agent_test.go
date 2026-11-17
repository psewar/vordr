package deploy_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/psewar/vordr/deploy"
	"github.com/psewar/vordr/deploy/deploytest"
)

// The instances share the agent's binary: once the new agent stays up, the running ones are restarted onto it and
// must come back.
func TestInstallRestartsTheRunningInstances(t *testing.T) {
	s := deploytest.New(t)
	s.Instance("nas-a", "node: nas-a\n")
	s.Instance("fw-a", "node: fw-a\n")
	out, code := s.Run()
	if code != 0 || !strings.Contains(out, "restarted onto the new binary: vordr-agent@nas-a.service vordr-agent@fw-a.service") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	calls := s.Calls()
	agent := deploytest.IndexOf(calls, "restart vordr-agent.service")
	instances := deploytest.IndexOf(calls, "try-restart vordr-agent@nas-a.service vordr-agent@fw-a.service")
	if agent < 0 || instances < agent {
		t.Fatalf("instances restarted before the agent, or not at all:\n%s", strings.Join(calls, "\n"))
	}
	for _, u := range []string{"vordr-agent@nas-a.service", "vordr-agent@fw-a.service"} {
		if deploytest.IndexOf(calls[instances:], "is-active --quiet "+u) < 0 {
			t.Errorf("%s not checked after its restart", u)
		}
	}
	if !s.Installed() {
		t.Fatal("binary not installed")
	}
}

// Every check runs before anything changes: an instance whose policy the new binary refuses, or that has none, stops
// the install before the binary or the agent's policy is replaced, with exit 2.
func TestInstallRefusesWhileAnInstancePolicyFails(t *testing.T) {
	for name, policy := range map[string]string{"refused": "node: fw-a\nBROKEN\n", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			s := deploytest.New(t)
			s.Instance("nas-a", "node: nas-a\n")
			s.Instance("fw-a", policy)
			out, code := s.Run()
			if code != 2 || !strings.Contains(out, "proxy instance fw-a:") || !strings.Contains(out, "nothing installed") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if s.Installed() {
				t.Error("binary installed although an instance's policy fails")
			}
			if s.Policy() != "" {
				t.Error("agent.yaml installed although an instance's policy fails")
			}
			for _, c := range s.Calls() {
				if strings.Contains(c, "restart") {
					t.Errorf("%q although nothing may change", c)
				}
			}
		})
	}
}

// A refusal says by its exit code that nothing changed (2), so that vordr-agent install can report it as a fact
// (ADR-0169): every check, and a command that fails before the first change.
func TestInstallRefusalsExitTwoAndChangeNothing(t *testing.T) {
	for name, setup := range map[string]func(*deploytest.Sandbox) []string{
		"not root": func(s *deploytest.Sandbox) []string { s.Stub("id", "echo 1000"); return nil },
		"no systemd": func(s *deploytest.Sandbox) []string {
			must(t, os.Remove(filepath.Join(s.Root, "/run/systemd/system")))
			return nil
		},
		"policy has issues": func(s *deploytest.Sandbox) []string {
			must(t, os.WriteFile(s.Cfg, []byte("BROKEN\n"), 0o644))
			return nil
		},
		"hub not reachable": func(s *deploytest.Sandbox) []string {
			raw, err := os.ReadFile(s.Bin)
			must(t, err)
			must(t, os.WriteFile(s.Bin, []byte(strings.Replace(string(raw), `echo "hub reachable"`, "exit 1", 1)), 0o755))
			return nil
		},
		"policy differs without reason": func(s *deploytest.Sandbox) []string {
			must(t, os.WriteFile(filepath.Join(s.Root, "/etc/vordr/agent.yaml"), []byte("node: host-b\n"), 0o600))
			return nil
		},
		"replace without reason": func(*deploytest.Sandbox) []string { return []string{"--replace-config", " "} },
		// realpath fails: a command, not a check, that ends the script before the first change
		"binary missing": func(s *deploytest.Sandbox) []string {
			s.Bin = filepath.Join(s.Dir, "missing", "vordr-agent")
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := deploytest.New(t)
			args := setup(s)
			before := s.Tree()
			out, code := s.Run(args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			if after := s.Tree(); !slices.Equal(before, after) {
				t.Errorf("changed although refused:\nbefore %v\nafter  %v", before, after)
			}
			for _, c := range s.Calls() {
				if !strings.HasPrefix(c, "list-units") {
					t.Errorf("systemctl %q although nothing may change", c)
				}
			}
		})
	}
}

// After the first change a failure exits 1: the host is no longer as before, the output says where.
func TestInstallFailureAfterAChangeExitsOne(t *testing.T) {
	s := deploytest.New(t)
	s.Down("vordr-agent.service")
	out, code := s.Run()
	if code != 1 || !strings.Contains(out, "the service did not stay up") {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if !s.Installed() {
		t.Fatal("the failure came before the binary was installed; the test no longer fails after a change")
	}
}

// A command that fails unexpectedly after the first change (not one of the script's checks, which call fail) ends the
// script through its trap with exit 1 too: the host is no longer as before.
func TestInstallUnexpectedFailureAfterAChangeExitsOne(t *testing.T) {
	s := deploytest.New(t)
	s.Fail("daemon-reload")
	out, code := s.Run()
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if !s.Installed() {
		t.Fatal("daemon-reload failed before the binary was installed; the test no longer fails after a change")
	}
}

// An instance that does not stay up with the new binary fails the install and is named.
func TestInstallFailsWhenAnInstanceDoesNotComeBack(t *testing.T) {
	s := deploytest.New(t)
	s.Instance("nas-a", "node: nas-a\n")
	s.Instance("fw-a", "node: fw-a\n")
	s.Down("vordr-agent@fw-a.service")
	out, code := s.Run()
	if code != 1 || !strings.Contains(out, "proxy instance vordr-agent@fw-a.service did not come back") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A host without proxy instances restarts nothing but its agent.
func TestInstallWithoutInstances(t *testing.T) {
	s := deploytest.New(t)
	out, code := s.Run()
	if code != 0 || strings.Contains(out, "proxy instances") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, c := range s.Calls() {
		if strings.HasPrefix(c, "try-restart") {
			t.Fatalf("%q on a host without instances", c)
		}
	}
	if !s.Installed() {
		t.Fatal("binary not installed")
	}
}

// The binary carries every file install-agent.sh takes from next to itself, and writes them as in this folder.
func TestEmbeddedAgentFilesAreWhatTheScriptNeeds(t *testing.T) {
	raw, err := os.ReadFile("install-agent.sh")
	must(t, err)
	var embedded []string
	for _, f := range deploy.AgentFiles {
		embedded = append(embedded, f.Name)
	}
	needs := regexp.MustCompile(`"\$here/([^"]+)"`).FindAllStringSubmatch(string(raw), -1)
	if len(needs) == 0 {
		t.Fatal(`install-agent.sh names no "$here/..." file any more; update the test with the script`)
	}
	for _, m := range needs {
		if !slices.Contains(embedded, m[1]) {
			t.Errorf("install-agent.sh needs %s, which deploy.AgentFiles does not carry", m[1])
		}
	}
	dir := t.TempDir()
	must(t, deploy.WriteAgentFiles(dir))
	for _, f := range deploy.AgentFiles {
		want, err := os.ReadFile(filepath.FromSlash(f.Name))
		must(t, err)
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s: not the repository's file (%v)", f.Name, err)
		}
		if fi, err := os.Stat(path); err == nil && os.PathSeparator == '/' && fi.Mode().Perm() != f.Mode {
			t.Errorf("%s: mode %v, want %v", f.Name, fi.Mode().Perm(), f.Mode)
		}
	}
	// The files are new in the folder: a second write does not overwrite them.
	if err := deploy.WriteAgentFiles(dir); err == nil {
		t.Error("WriteAgentFiles wrote over files that were already there")
	}
}

// hub-check's exit code decides (ADR-0170): a hub that does not sign with hub.public_key (3) and an unreachable hub (1)
// each stop the install before anything changes (exit 2, ADR-0169), with their own reason; an older hub that does not
// sign at all passes hub-check (0) and the install goes on.
func TestInstallRefusesByHubCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		exit int
		says string
	}{
		"another hub key": {3, "does not sign with hub.public_key"},
		"not reachable":   {1, "the hub is not reachable from here"},
	} {
		t.Run(name, func(t *testing.T) {
			s := deploytest.New(t)
			s.HubCheck(tc.exit)
			before := s.Tree()
			out, code := s.Run()
			if code != 2 || !strings.Contains(out, tc.says) || !strings.Contains(out, "nothing installed") {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			if s.Installed() {
				t.Error("binary installed although hub-check refused")
			}
			if s.Policy() != "" {
				t.Error("agent.yaml installed although hub-check refused")
			}
			if after := s.Tree(); !slices.Equal(before, after) {
				t.Errorf("changed although refused:\nbefore %v\nafter  %v", before, after)
			}
			for _, c := range s.Calls() {
				if !strings.HasPrefix(c, "list-units") {
					t.Errorf("systemctl %q although nothing may change", c)
				}
			}
		})
	}
	s := deploytest.New(t)
	s.HubCheck(0)
	if out, code := s.Run(); code != 0 || !s.Installed() {
		t.Fatalf("hub-check passed, exit %d:\n%s", code, out)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
