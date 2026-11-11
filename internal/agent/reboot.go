package agent

import (
	"context"
	"encoding/json"
	"errors"
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
// when the host really needs it and nothing else runs. Before the reboot the agent notes the boot ID; the agent that
// starts afterwards proves with it that the host did reboot, and checks that no reboot is needed any more.
type Rebooter struct {
	BootID   func() string                   // changes with every boot
	Reboot   func(ctx context.Context) error // asks the system to reboot
	Required func(ctx context.Context) bool  // the same signals the report uses (reboot-required file, kernel)
}

const (
	bootIDPath = "/proc/sys/kernel/random/boot_id"
	// rebootMinGap: one reboot per hour at most - a host that keeps needing one must not reboot in a loop.
	rebootMinGap = time.Hour
)

// NewRebooter returns the rebooter for this host, or nil where the agent may not reboot it: reboot.allowed is false,
// not Linux, not root, or no installer (it knows whether a reboot is needed).
func NewRebooter(cfg policy.Config, inst Installer) *Rebooter {
	if !cfg.Reboot.Allowed || runtime.GOOS != "linux" || os.Geteuid() != 0 || inst == nil {
		return nil
	}
	run := runner.Exec{}
	return &Rebooter{
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
}

// rebootMarker: written before the reboot; Reported once the hub acknowledged the result the agent after the boot
// sent (with a hub before ADR-0114: once it was sent). It stays afterwards for the minimum gap.
type rebootMarker struct {
	JobID    string    `json:"job_id"`
	BootID   string    `json:"boot_id"`
	At       time.Time `json:"at"`
	Reported bool      `json:"reported"`
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
	switch {
	case !rb.Allowed:
		answer(wire.JobStatus{Code: protocol.CodeRejectedNotAllowed})
		return
	case r == nil:
		answer(wire.JobStatus{Code: protocol.CodeRejectedUnsupported})
		return
	case c.windowClosed(rb.Window, j):
		answer(wire.JobStatus{Code: protocol.CodeRejectedOutsideWindow})
		return
	}
	if m, ok := c.readRebootMarker(); ok && c.Now().Sub(m.At) < rebootMinGap {
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
	m := rebootMarker{JobID: j.ID, BootID: r.BootID(), At: c.Now()}
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
