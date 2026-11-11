//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rollback.cmd puts the previous binary back only while a self-update is pending, in a folder with a space in its
// name like %ProgramFiles%. The last line (sc start) is not run here: it would start the real service.
func TestRollbackScriptPutsBackOnlyWhilePending(t *testing.T) {
	for _, pending := range []bool{true, false} {
		dir := filepath.Join(t.TempDir(), "Program Files", "Vordr")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(dir, "vordr-agent.exe")
		marker := filepath.Join(dir, "state", "update.json")
		write := func(path, s string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write(exe, "new agent")
		write(exe+".old", "old agent")
		if pending {
			write(marker, "{}")
		}
		lines := strings.Split(strings.TrimSuffix(rollbackScript(exe, marker), "\r\n"), "\r\n")
		if last := lines[len(lines)-1]; last != "sc start "+serviceName+" >nul" {
			t.Fatalf("last line %q does not start the agent", last)
		}
		script := filepath.Join(dir, "rollback-test.cmd")
		write(script, strings.Join(lines[:len(lines)-1], "\r\n")+"\r\n")
		if out, err := exec.Command("cmd.exe", "/c", script).CombinedOutput(); err != nil {
			t.Fatalf("pending %v: %v %s", pending, err, out)
		}
		got, _ := os.ReadFile(exe)
		_, oldErr := os.Stat(exe + ".old")
		switch {
		case pending && (string(got) != "old agent" || oldErr == nil):
			t.Fatalf("pending: exe %q, .old still there: %v", got, oldErr == nil)
		case !pending && (string(got) != "new agent" || oldErr != nil):
			t.Fatalf("not pending: exe %q, .old gone: %v", got, oldErr != nil)
		}
	}
}
