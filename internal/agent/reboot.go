package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/wire"
)

// Rebooter restarts the host for a reboot job (design 6.1): only where agent.yaml allows it, inside reboot.window,
// when the host really needs it, nothing else runs and its idle check says it is idle. Before the reboot the agent notes
// the boot ID; the agent that starts afterwards proves with it that the host did reboot, and checks that no reboot is
// needed any more.
type Rebooter struct {
	BootID   func() string                   // changes with every boot
	Reboot   func(ctx context.Context) error // asks the system to reboot
	Required func(ctx context.Context) bool  // the same signals the report uses (reboot-required file, kernel)
	// Idle runs reboot.idle_check (ADR-0155): whether the host is idle, and if not, why. Nil: the host has no check.
	Idle func(ctx context.Context) (idle bool, why string)
}

const (
	bootIDPath = "/proc/sys/kernel/random/boot_id"
	// rebootMinGap: one reboot per hour at most - a host that keeps needing one must not reboot in a loop.
	rebootMinGap = time.Hour
	// idleWhyMax bounds the line of the idle check that travels along as the detail.
	idleWhyMax = 200
)

// idleCheckTimeout: a check that cannot say within it that the host is idle holds the reboot. A variable only so that
// the test of a check that hangs does not wait half a minute.
var idleCheckTimeout = 30 * time.Second

// NewRebooter returns the rebooter for this host, or nil where the agent may not reboot it: reboot.allowed is false,
// not Linux, not root, or no installer (it knows whether a reboot is needed).
func NewRebooter(cfg policy.Config, inst Installer) *Rebooter {
	if !cfg.Reboot.Allowed || runtime.GOOS != "linux" || os.Geteuid() != 0 || inst == nil {
		return nil
	}
	run := runner.Exec{}
	r := &Rebooter{
		BootID: func() string {
			raw, err := os.ReadFile(bootIDPath)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(raw))
		},
		Reboot: func(ctx context.Context) error {
			res := run.Run(ctx, nil, "systemctl", "reboot")
			if !res.Ran() {
				return res.Err
			}
			if res.ExitCode != 0 {
				return errors.New(strings.TrimSpace(string(res.Stderr)))
			}
			return nil
		},
		Required: inst.RebootRequired,
	}
	if argv := cfg.Reboot.IdleCheck; len(argv) > 0 {
		r.Idle = idleCheck(argv, run)
	}
	return r
}

// idleCheck runs reboot.idle_check (ADR-0155). Its exit status is its answer, by contract: 0 means idle, any other
// status means busy, and its first line says why. A check that is gone, cannot start or does not finish in time cannot
// say that the host is idle, so it holds the reboot as well, with that as the reason.
func idleCheck(argv []string, run runner.Runner) func(ctx context.Context) (bool, string) {
	return func(ctx context.Context) (bool, string) {
		// Checked first, so that a missing check is named as such (check-config checks it when the file is taken over).
		if fi, err := os.Stat(argv[0]); err != nil || !fi.Mode().IsRegular() {
			return false, "idle check " + argv[0] + " is missing"
		}
		ctx, cancel := context.WithTimeout(ctx, idleCheckTimeout)
		defer cancel()
		res := run.Run(ctx, nil, argv[0], argv[1:]...)
		switch {
		case ctx.Err() != nil:
			return false, fmt.Sprintf("idle check did not finish within %s", idleCheckTimeout)
		case !res.Ran():
			// Started but not run (permissions, format): only the attempt can tell; the text is for a human.
			return false, "idle check did not run: " + res.Err.Error()
		case res.ExitCode == 0:
			return true, ""
		}
		return false, idleWhy(res)
	}
}

// idleWhy is the first non-empty line the idle check printed (its output before its errors), for a human reading the
// result; nothing decides on it. Without one, its exit status.
func idleWhy(res runner.Result) string {
	for _, out := range [][]byte{res.Stdout, res.Stderr} {
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				if len(line) > idleWhyMax {
					line = strings.ToValidUTF8(line[:idleWhyMax], "")
				}
				return line
			}
		}
	}
	return fmt.Sprintf("idle check exited with status %d", res.ExitCode)
}

// rebootMarker: written before the reboot; Reported once the hub acknowledged the result the agent after the boot
// sent (with a hub before ADR-0114: once it was sent). It stays afterwards for the minimum gap. Failed: the agent after
// the boot found that the reboot did not take (ADR-0153); FailedBoot is the boot it found that in. While the host still
// runs that boot, no further reboot goes through Vördr.
type rebootMarker struct {
	JobID      string    `json:"job_id"`
	BootID     string    `json:"boot_id"`
	At         time.Time `json:"at"`
	Reported   bool      `json:"reported"`
	Failed     bool      `json:"failed,omitempty"`
	FailedBoot string    `json:"failed_boot,omitempty"`
}

// blocks reports whether this marker holds a new reboot (ADR-0153): its reboot did not take, and the host has not
// booted since - or its boot ID cannot be read, so that cannot be told.
func (m rebootMarker) blocks(bootID string) bool {
	return m.Failed && (bootID == "" || bootID == m.FailedBoot)
}

func (c *Client) rebootPath() string { return filepath.Join(c.Cfg.StateDir, "reboot.json") }

func (c *Client) readRebootMarker() (rebootMarker, bool) {
	var m rebootMarker
	raw, err := os.ReadFile(c.rebootPath())
	if errors.Is(err, fs.ErrNotExist) {
		return m, false
	}
	if err != nil || json.Unmarshal(raw, &m) != nil {
		c.Log.Printf("reboot marker unreadable, ignored: %v", err)
		return m, false
	}
	return m, true
}

// rebootHost carries out a reboot job.
func (c *Client) rebootHost(j protocol.Job, send func(any) error) {
	answer := func(st wire.JobStatus) {
		st.T, st.JobID = wire.TJobStatus, j.ID
		c.Log.Printf("job %s reboot: %s %s", j.ID, st.Code, st.Detail)
		c.answer(send, st) // over a newer session, or kept for the next one
	}
	rb, r := c.Cfg.Reboot, c.Rebooter
	asked := j
	if rb.WindowOnly {
		asked.OutsideWindow = "" // ADR-0154: on this host a reason does not open the reboot window
	}
	switch {
	case !rb.Allowed:
		answer(wire.JobStatus{Code: protocol.CodeRejectedNotAllowed})
		return
	case r == nil:
		answer(wire.JobStatus{Code: protocol.CodeRejectedUnsupported})
		return
	case c.windowClosed(rb.Window, asked):
		st := wire.JobStatus{Code: protocol.CodeRejectedOutsideWindow}
		if j.OutsideWindow != "" {
			st.Detail = "reboot.window_only: a reason does not open the reboot window"
		}
		answer(st)
		return
	}
	m, marked := c.readRebootMarker()
	switch {
	case marked && m.blocks(r.BootID()):
		answer(wire.JobStatus{Code: protocol.CodeRejectedLastRebootFailed,
			Detail: "the reboot of " + m.At.Format(time.RFC3339) + " did not take; reboot the host another way once"})
		return
	case marked && c.Now().Sub(m.At) < rebootMinGap:
		answer(wire.JobStatus{Code: protocol.CodeRejectedRateLimited, Detail: "rebooted at " + m.At.Format(time.RFC3339)})
		return
	}
	if !c.checkMu.TryLock() {
		answer(wire.JobStatus{Code: protocol.CodeRejectedBusy})
		return
	}
	ctx, cancel := context.WithTimeout(c.root(), time.Minute)
	defer cancel()
	if !r.Required(ctx) {
		c.checkMu.Unlock()
		answer(wire.JobStatus{Code: protocol.CodeOK, Detail: "no reboot needed"})
		return
	}
	if r.Idle != nil {
		// Asked last, right before the reboot and under the run lock: the host's own word that nothing of its own runs.
		if idle, why := r.Idle(ctx); !idle {
			c.checkMu.Unlock()
			answer(wire.JobStatus{Code: protocol.CodeRejectedNotIdle, Detail: why})
			return
		}
	}
	m = rebootMarker{JobID: j.ID, BootID: r.BootID(), At: c.Now()}
	if err := writeJSON(c.rebootPath(), m); err != nil {
		c.checkMu.Unlock()
		answer(wire.JobStatus{Code: protocol.CodeFailedVerification, Detail: "reboot marker: " + err.Error()})
		return
	}
	// The hub keeps the run open over the coming disconnect: the agent said it restarts (phase rebooting).
	c.progressSender(j.ID)(phaseAt(protocol.PhaseRebooting, 50))
	c.Log.Printf("job %s reboot: rebooting now (boot %s)", j.ID, m.BootID)
	// Until the host goes down a reconnect names the reboot as running, and nothing reports it as done.
	c.setRebooting(true)
	if err := r.Reboot(ctx); err != nil {
		c.setRebooting(false)
		_ = os.Remove(c.rebootPath())
		c.checkMu.Unlock()
		answer(wire.JobStatus{Code: protocol.CodeFailedVerification, Detail: "systemctl reboot: " + err.Error()})
	}
	// systemd stops the agent now; the agent after the boot reports (reportReboot). The lock stays taken until then.
}

// reportReboot sends the result of a reboot this agent's host went through, on the first approved session after it.
func (c *Client) reportReboot(send func(any) error) {
	m, ok := c.readRebootMarker()
	c.stateMu.Lock()
	rebooting := c.rebooting
	c.stateMu.Unlock()
	if !ok || m.Reported || rebooting {
		return // nothing to report, or this agent's own reboot has not happened yet
	}
	st := wire.JobStatus{T: wire.TJobStatus, JobID: m.JobID, Code: protocol.CodeOK, Detail: "rebooted"}
	now := ""
	if c.Rebooter != nil {
		now = c.Rebooter.BootID()
	}
	ctx, cancel := context.WithTimeout(c.root(), time.Minute)
	defer cancel()
	switch {
	case now == "" || now == m.BootID:
		st.Code, st.Detail = protocol.CodeFailedVerification, "the host did not reboot (same boot ID)"
	case c.Rebooter.Required(ctx):
		st.Code, st.Detail = protocol.CodeFailedVerification, "rebooted, but the host still needs a reboot"
	}
	if st.Code != protocol.CodeOK && !m.Failed {
		// Noted before it is sent: from now on this boot takes no further reboot through Vördr (ADR-0153).
		m.Failed, m.FailedBoot = true, now
		if err := writeJSON(c.rebootPath(), m); err != nil {
			c.Log.Printf("reboot marker: %v", err)
		}
	}
	if send(st) != nil {
		return // the next session reports it
	}
	c.Log.Printf("reboot %s reported: %s %s", m.JobID, st.Code, st.Detail)
	if !c.delivered(true) {
		return // the hub's ack marks it (acknowledgeReboot)
	}
	m.Reported = true
	if err := writeJSON(c.rebootPath(), m); err != nil {
		c.Log.Printf("reboot marker: %v", err)
	}
}
