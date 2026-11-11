//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/psewar/vordr/internal/runner"
)

// selfUpdatePlatform: on Linux only as root, under systemd (Restart=always starts the new binary after the swap), and
// with systemd-run for the update guard.
func selfUpdatePlatform() string {
	if os.Geteuid() != 0 {
		return "not running as root"
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return "no systemd-run for the update guard (ADR-0098)"
	}
	return ""
}

// restartExit ends the process; systemd starts the binary now in place.
func restartExit(string) { os.Exit(0) }

// nextPath is where the downloaded binary waits for its smoke test and the swap.
func nextPath(exe string) string { return exe + ".new" }

// putBack puts the previous binary back in place of the running one.
func putBack(exe string) error { return os.Rename(exe+".old", exe) }

// removeLeftovers: on Linux the swap leaves nothing behind but exe.old, the way back.
func removeLeftovers(string) {}

// readShared: on Linux an open file never stops another process from removing or replacing it.
func readShared(path string) ([]byte, error) { return os.ReadFile(path) }

// guardUnit: the transient unit the update guard runs in.
const guardUnit = "vordr-agent-update-guard.service"

// instanceUnits: the proxy instances (deploy/systemd/vordr-agent@.service), all on this agent's binary.
const instanceUnits = "vordr-agent@*.service"

// restartInstances has systemd restart the proxy instances that run, so that they run the binary now in place. A pattern
// that matches no running unit restarts nothing; --no-block: the instances start on their own time.
func restartInstances(ctx context.Context, run runner.Runner) error {
	res := run.Run(ctx, nil, "systemctl", "try-restart", "--no-block", instanceUnits)
	if !res.Ran() {
		return res.Err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("systemctl try-restart exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// armGuard starts the update guard for job as a transient unit of its own: outside vordr-agent.service, so it outlives
// this process and does not hang with the new binary. Type=exec: systemd-run returns only once the guard runs exe, which
// is still this binary before the swap. --collect: the unit goes away when the guard ends, however it ends.
func armGuard(ctx context.Context, run runner.Runner, exe, cfg, job string) error {
	res := run.Run(ctx, nil, "systemd-run", "--unit="+guardUnit, "--description=Vördr agent: update guard for job "+job,
		"--property=Type=exec", "--collect", "--quiet", exe, "update-guard", "--config", cfg, "--job", job, "--exe", exe)
	if !res.Ran() {
		return res.Err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("systemd-run exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
