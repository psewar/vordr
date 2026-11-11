//go:build windows

package agent

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/psewar/vordr/internal/runner"
)

// selfUpdatePlatform: on Windows only as the service (install-service). The update guard starts it again after the swap
// (ADR-0103), and the service's third recovery action (rollback.cmd) puts the old binary back if the new one does not
// start (ADR-0084).
func selfUpdatePlatform() string {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return "cannot tell whether running as a service: " + err.Error()
	}
	if !isService {
		return "not running as the Windows service (vordr-agent install-service)"
	}
	return ""
}

// restartExit ends the process with an error code: the service's recovery action (install-service: also for exits
// with an error) starts it again after 10 s, now with the new binary. The service counts that as a failure; it uses
// this only when the update guard cannot be asked (AskGuardToStart, ADR-0103).
func restartExit(string) { os.Exit(3) }

// restartInstances: proxy instances run on Linux only (design/stellvertreter.md); there are none to restart here.
func restartInstances(context.Context, runner.Runner) error { return nil }

// The planned restart of the Windows service (ADR-0103). The service manager counts every end with an error code as
// a failure, for a day (install-service: reset period 86400 s), and its third recovery action is rollback.cmd. A
// restart that ends with an error would use up that chain: the third self-update within a day rolled back a good new
// agent. So the agent stops cleanly instead, and the update guard, which runs outside the service anyway (ADR-0098),
// starts the service again. The request goes through an event that the guard creates and holds while it runs: an agent
// that can open it knows the guard is there.

// eventNamespace: guard and service both run as LocalSystem in session 0; tests run unelevated and use Local\.
var eventNamespace = `Global\`

// guardReadyWait: how long armGuard waits for the guard to be listening before it refuses the swap.
var guardReadyWait = 30 * time.Second

func guardEvent(job string) (*uint16, error) {
	return windows.UTF16PtrFromString(eventNamespace + "vordr-update-guard-" + job)
}

// openGuardEvent opens the event of job's guard. Opening is the only way to ask whether the event exists, and no
// answer could be checked beforehand without a race: an error here means "no guard of job listens".
func openGuardEvent(job string, access uint32) (windows.Handle, bool) {
	name, err := guardEvent(job)
	if err != nil {
		return 0, false
	}
	h, err := windows.OpenEvent(access, false, name)
	if err != nil {
		return 0, false
	}
	return h, true
}

// AskGuardToStart asks the update guard of job to start the service again once it has stopped. false: no guard of job
// listens; a clean stop would then leave the agent stopped.
func AskGuardToStart(job string) bool {
	h, ok := openGuardEvent(job, windows.EVENT_MODIFY_STATE)
	if !ok {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h) == nil
}

// GuardRequests creates the event of job and forwards each request on it until ctx ends. Only the update guard calls it.
func GuardRequests(ctx context.Context, job string) (<-chan struct{}, error) {
	name, err := guardEvent(job)
	if err != nil {
		return nil, err
	}
	// Auto-reset: one request, one start. A guard of the same job cannot run twice; CreateEvent would return the
	// existing event then, with ERROR_ALREADY_EXISTS as its error and a valid handle.
	h, err := windows.CreateEvent(nil, 0, 0, name)
	if h == 0 {
		return nil, err
	}
	out := make(chan struct{}, 1)
	go func() {
		defer windows.CloseHandle(h)
		for ctx.Err() == nil {
			ev, err := windows.WaitForSingleObject(h, 500)
			if err != nil {
				return
			}
			if ev == windows.WAIT_OBJECT_0 {
				select {
				case out <- struct{}{}:
				default: // one start is already asked for
				}
			}
		}
	}()
	return out, nil
}

// guardListens waits up to d for the guard of job to create its event.
func guardListens(job string, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(100 * time.Millisecond) {
		if h, ok := openGuardEvent(job, windows.SYNCHRONIZE); ok {
			windows.CloseHandle(h)
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// nextPath keeps the .exe extension, so the smoke test can start the new binary.
func nextPath(exe string) string { return strings.TrimSuffix(exe, ".exe") + ".new.exe" }

// putBack: a running .exe can be renamed but not overwritten. The running binary moves aside as exe.failed, the
// previous one back into place; removeLeftovers deletes exe.failed on the next start.
func putBack(exe string) error {
	if err := os.Rename(exe, exe+".failed"); err != nil {
		return err
	}
	if err := os.Rename(exe+".old", exe); err != nil {
		_ = os.Rename(exe+".failed", exe)
		return err
	}
	return nil
}

// removeLeftovers deletes the binary a rollback moved aside; it no longer runs once the old agent has started.
func removeLeftovers(exe string) {
	if _, err := os.Stat(exe + ".failed"); err == nil {
		_ = os.Remove(exe + ".failed")
	}
}

// readShared reads a file that another process may remove meanwhile: os.ReadFile opens it without FILE_SHARE_DELETE,
// and while it is open, the agent could not remove the update marker, its confirmation to the guard.
func readShared(path string) ([]byte, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}

// armGuard starts the update guard for job as a process of its own, detached from the service: the service manager
// does not end it with the agent, and it does not hang with the new binary. CreateProcess maps the image before it
// returns, so the guard runs exe as it is now, this binary before the swap; the swap may rename it while it runs.
// The swap waits until the guard listens for the planned restart: without it, the restart after the swap would count
// as a failure again (ADR-0103).
func armGuard(_ context.Context, _ runner.Runner, exe, cfg, job string) error {
	cmd := exec.Command(exe, "update-guard", "--config", cfg, "--job", job, "--exe", exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	if !guardListens(job, guardReadyWait) {
		return fmt.Errorf("the update guard does not listen after %s", guardReadyWait)
	}
	return nil
}
