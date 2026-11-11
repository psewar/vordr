//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// vzdump runs with umask 022 whatever the agent's own is (UMask=0077 in its unit). Under 0077 the backup of an
// unprivileged container fails: proxmox-backup-client, as the container's mapped root, cannot read the pct.conf that
// vzdump has written. The arguments reach vzdump unchanged; none becomes shell.
func TestVzdumpRunsWithUmask022(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "vzdump")
	script := "#!/bin/sh\numask\nfor a in \"$@\"; do echo \"arg:$a\"; done\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o077) // as under the agent's unit
	defer syscall.Umask(old)
	args := []string{"301", "--notes-template", "vordr: before install, job b1", `$(touch x); "q" 'q'`}
	res := vzdumpRunner(fake)(context.Background(), args...)
	if !res.Ran() || res.ExitCode != 0 {
		t.Fatalf("ran %v, exit %d, stderr %q", res.Err, res.ExitCode, res.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	want := []string{"0022"}
	for _, a := range args {
		want = append(want, "arg:"+a)
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("vzdump saw\n%q\nwant\n%q", lines, want)
	}
}
