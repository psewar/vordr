package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 10, 30, 15, 4, 5, 0, time.UTC)

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Nothing installed yet: the given file is copied, with or without a reason.
func TestPlanConfigNew(t *testing.T) {
	dir := t.TempDir()
	given := writeFile(t, filepath.Join(dir, "given.yaml"), "node: a\n")
	for _, reason := range []string{"", "new policy"} {
		plan, err := planConfig(filepath.Join(dir, "installed.yaml"), given, reason, at)
		if err != nil || plan.action != configNew {
			t.Fatalf("reason %q: %+v, %v", reason, plan, err)
		}
	}
}

// The installed file itself (paths compare as on Windows, without case), or an identical copy: nothing to replace.
func TestPlanConfigSame(t *testing.T) {
	dir := t.TempDir()
	installed := writeFile(t, filepath.Join(dir, "installed.yaml"), "node: a\n")
	given := writeFile(t, filepath.Join(dir, "given.yaml"), "node: a\n")
	for _, g := range []string{installed, strings.ToUpper(installed), given} {
		plan, err := planConfig(installed, g, "", at)
		if err != nil || plan.action != configSame {
			t.Fatalf("%s: %+v, %v", g, plan, err)
		}
	}
}

// A different file without a reason is refused before anything changes, naming the state and the next step.
func TestPlanConfigDiffersRefused(t *testing.T) {
	dir := t.TempDir()
	installed := writeFile(t, filepath.Join(dir, "installed.yaml"), "node: a\n")
	given := writeFile(t, filepath.Join(dir, "given.yaml"), "node: b\n")
	_, err := planConfig(installed, given, "", at)
	if err == nil {
		t.Fatal("a different agent.yaml without a reason was accepted")
	}
	for _, want := range []string{installed, given, "differs", `--replace-config "<reason>"`, "nothing changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	if got, _ := os.ReadFile(installed); string(got) != "node: a\n" {
		t.Errorf("installed file changed: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("planning left files behind: %v", entries)
	}
}

// With a reason: kept as agent.yaml.bak-<time> as on Linux, and the difference listed.
func TestPlanConfigReplaced(t *testing.T) {
	dir := t.TempDir()
	installed := writeFile(t, filepath.Join(dir, "agent.yaml"), "node: a\ninstall:\n  auto: off\n")
	given := writeFile(t, filepath.Join(dir, "given.yaml"), "node: a\ninstall:\n  auto: hub\n")
	plan, err := planConfig(installed, given, "packages section for a project folder", at)
	if err != nil || plan.action != configReplace {
		t.Fatalf("%+v, %v", plan, err)
	}
	if want := installed + ".bak-20261030-150405"; plan.backup != want {
		t.Errorf("backup %s, want %s", plan.backup, want)
	}
	if want := []string{"-   3    auto: off", "+   3    auto: hub"}; !slices.Equal(plan.diff, want) {
		t.Errorf("diff %q, want %q", plan.diff, want)
	}
}

// An installed path that cannot be read is no "not installed": refused.
func TestPlanConfigUnreadable(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "agent.yaml")
	if err := os.Mkdir(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	given := writeFile(t, filepath.Join(dir, "given.yaml"), "node: a\n")
	if _, err := planConfig(installed, given, "reason", at); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("unreadable installed file: %v", err)
	}
}

func TestLineDiff(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want []string
	}{
		{"same", "a\nb\n", "a\nb\n", nil},
		{"changed line", "a\nb\nc\n", "a\nx\nc\n", []string{"-   2  b", "+   2  x"}},
		{"added and removed", "a\nb\n", "b\nc\n", []string{"-   1  a", "+   2  c"}},
		{"from nothing", "", "a\n", []string{"+   1  a"}},
		{"line endings", "a\r\nb\r\n", "a\nb\n", []string{"      (only line endings or the final newline differ)"}},
		{"final newline", "a", "a\n", []string{"      (only line endings or the final newline differ)"}},
	}
	for _, c := range cases {
		if got := lineDiff([]byte(c.a), []byte(c.b)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// trail records what the steps did, in order.
type trail []string

func (tr *trail) step(name string, fail, undo bool) step {
	st := step{what: name, do: func() error {
		*tr = append(*tr, "do "+name)
		if fail {
			return errors.New(name + " broke")
		}
		return nil
	}}
	if undo {
		st.undo = func() error { *tr = append(*tr, "undo "+name); return nil }
	}
	return st
}

func TestApplyAllSteps(t *testing.T) {
	var tr trail
	var out, errOut bytes.Buffer
	res := apply([]step{tr.step("a", false, true), tr.step("b", false, false)}, &out, &errOut)
	if res.failed != "" || !slices.Equal(tr, trail{"do a", "do b"}) {
		t.Fatalf("%+v, %v", res, tr)
	}
	if out.String() != "ok     a\nok     b\n" || errOut.Len() != 0 {
		t.Errorf("output %q / %q", out.String(), errOut.String())
	}
}

// A step fails after the stop: what ran is undone last first, the failed step included, nothing after it runs.
func TestApplyUndoesLastFirst(t *testing.T) {
	var tr trail
	var out, errOut bytes.Buffer
	res := apply([]step{
		tr.step("powershell", false, false),
		tr.step("stop", false, true),
		tr.step("binary", false, true),
		tr.step("agent.yaml", true, true),
		tr.step("start", false, true),
	}, &out, &errOut)
	want := trail{"do powershell", "do stop", "do binary", "do agent.yaml", "undo agent.yaml", "undo binary", "undo stop"}
	if !slices.Equal(tr, want) {
		t.Fatalf("trail %v, want %v", tr, want)
	}
	if res.failed != "agent.yaml" || res.err == nil || len(res.stuck) != 0 {
		t.Errorf("result %+v", res)
	}
	if !strings.Contains(errOut.String(), "failed agent.yaml: agent.yaml broke") ||
		!strings.Contains(out.String(), "undone stop") {
		t.Errorf("output %q / %q", out.String(), errOut.String())
	}
}

// An undo that fails is reported as still changed, and the undos before it still run.
func TestApplyGoesOnPastAFailedUndo(t *testing.T) {
	var tr trail
	var out, errOut bytes.Buffer
	stuck := tr.step("binary", false, false)
	stuck.undo = func() error { tr = append(tr, "undo binary"); return errors.New("in use") }
	res := apply([]step{tr.step("stop", false, true), stuck, tr.step("start", true, true)}, &out, &errOut)
	want := trail{"do stop", "do binary", "do start", "undo start", "undo binary", "undo stop"}
	if !slices.Equal(tr, want) {
		t.Fatalf("trail %v, want %v", tr, want)
	}
	if !slices.Equal(res.stuck, []string{"binary: in use"}) {
		t.Errorf("stuck %v", res.stuck)
	}
}

func TestLogLine(t *testing.T) {
	dir := t.TempDir()
	for _, msg := range []string{"first", "install-service: agent.yaml replaced (old one in x): reason"} {
		if err := logLine(dir, msg); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "agent.log"))
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[1], " install-service: agent.yaml replaced (old one in x): reason") {
		t.Errorf("agent.log %q", got)
	}
	if err := logLine(filepath.Join(dir, "missing"), "x"); err == nil {
		t.Error("a log line into a missing state_dir reported as written")
	}
}
