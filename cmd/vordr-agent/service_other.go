//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/psewar/vordr/internal/runner"
)

// agentUnit: the host agent's unit (deploy/systemd/vordr-agent.service).
const agentUnit = "vordr-agent.service"

// restartAgent, for the update guard: systemd starts the binary now in place. A hung agent would hold up a stop for
// TimeoutStopSec (30 min), so its main process gets SIGKILL first, as a crash would end it; a unit that has failed in
// the meantime is reset, so that the start is not refused.
func restartAgent(ctx context.Context) error {
	r := runner.Exec{}
	res := r.Run(ctx, nil, "systemctl", "show", "--property=MainPID", "--value", agentUnit)
	if !res.Ran() || res.ExitCode != 0 {
		return fmt.Errorf("systemctl show %s: %s", agentUnit, cmdOutput(res))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		return fmt.Errorf("main PID of %s: %q", agentUnit, res.Stdout)
	}
	if pid > 0 { // 0: no main process (failed, or waiting to restart); kill(0) would hit the guard's own group
		// ESRCH: the process ended between show and kill - nothing left to end; no way to ask without that race.
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("SIGKILL to the agent (%d): %w", pid, err)
		}
	}
	for _, args := range [][]string{{"reset-failed", agentUnit}, {"restart", "--no-block", agentUnit}} {
		if res := r.Run(ctx, nil, "systemctl", args...); !res.Ran() || res.ExitCode != 0 {
			return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), cmdOutput(res))
		}
	}
	return nil
}

func cmdOutput(res runner.Result) string {
	if !res.Ran() {
		return res.Err.Error()
	}
	return strings.TrimSpace(string(res.Stderr))
}

// On Linux systemd runs the agent (deploy/install-agent.sh); these exist only on Windows.

func asService() bool { return false }

func runService(string) int { return 2 }

func serviceLog(string) io.Writer { return os.Stderr }

func installService(string, string) int {
	fmt.Fprintln(os.Stderr, "install-service is for Windows; on Linux: deploy/install-agent.sh")
	return 2
}

func uninstallService() int {
	fmt.Fprintln(os.Stderr, "uninstall-service is for Windows; on Linux: systemctl disable --now vordr-agent")
	return 2
}

// plannedRestarts: systemd restarts the agent after a self-update and counts no failure, so the guard has none to
// answer (ADR-0103 is about the Windows service).
func plannedRestarts(context.Context, string) (<-chan struct{}, func(context.Context) error, error) {
	return nil, nil, nil
}
