package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/wire"
)

// GuestBackups carries out backup jobs on the hypervisor (ADR-0096): vzdump of one guest of this host to the Proxmox
// Backup Server storage of agent.yaml guest_backup, right before that guest installs. It only adds a backup - it
// never prunes (--remove 0), restores or deletes - and backs up a guest at most once per guestBackupMinGap.
type GuestBackups struct {
	Storage string
	Timeout time.Duration
	// Exists reports whether this host has the guest (kind vm or ct, Proxmox id).
	Exists func(kind, id string) bool
	// Vzdump runs vzdump with these arguments.
	Vzdump func(ctx context.Context, args ...string) runner.Result

	mu   sync.Mutex
	last map[string]time.Time // guest -> start of its last backup by this agent
}

const (
	// guestBackupMinGap: a hub that asks again and again cannot keep a guest frozen or the backup server busy. The hub
	// itself uses a backup younger than its own reuse window (longer than this) instead of asking for a new one.
	guestBackupMinGap = 10 * time.Minute
	pveDir            = "/etc/pve"
)

// NewGuestBackups returns the backups this agent may make, or nil and why not. No guest_backup in agent.yaml: nil and
// "" - the agent simply does not offer the verb.
func NewGuestBackups(cfg policy.Config) (*GuestBackups, string) {
	b := cfg.GuestBackup
	switch {
	case b == nil:
		return nil, ""
	case runtime.GOOS != "linux":
		return nil, "not Linux"
	case os.Geteuid() != 0:
		return nil, "not root (vzdump needs it)"
	case !policy.PBSStorage(b.Storage):
		return nil, "no Proxmox Backup Server storage " + b.Storage + " in " + policy.PVEStorageConfig
	}
	vzdump, err := exec.LookPath("vzdump")
	if err != nil {
		return nil, "no vzdump: not a Proxmox VE host"
	}
	return &GuestBackups{Storage: b.Storage, Timeout: time.Duration(b.TimeoutMin) * time.Minute,
		Exists: func(kind, id string) bool {
			dir := "lxc"
			if kind == "vm" {
				dir = "qemu-server"
			}
			// pmxcfs links /etc/pve/lxc and /etc/pve/qemu-server to this node's guests only.
			_, err := os.Stat(filepath.Join(pveDir, dir, id+".conf"))
			return err == nil
		},
		Vzdump: vzdumpRunner(vzdump)}, ""
}

// umaskScript runs vzdump with umask 022, as Proxmox's own scheduler does. vzdump creates its temporary directory
// and the guest's pct.conf in it with the umask it inherits, and the agent's unit has UMask=0077. For an
// unprivileged container proxmox-backup-client reads pct.conf as the container's mapped root (uid 100000); under
// 0077 it gets "Permission denied" and the backup fails. Go can set the umask only for the whole process, not for
// one child, so this fixed line sets it and execs vzdump. The arguments arrive as "$0" and "$@" and never become part
// of the script.
const umaskScript = `umask 022 && exec "$0" "$@"`

// vzdumpRunner starts the vzdump at path through umaskScript.
func vzdumpRunner(path string) func(ctx context.Context, args ...string) runner.Result {
	run := runner.Exec{}
	return func(ctx context.Context, args ...string) runner.Result {
		return run.Run(ctx, nil, "/bin/sh", append([]string{"-c", umaskScript, path}, args...)...)
	}
}

// vzdumpArgs: snapshot mode (vzdump falls back to suspend where the storage cannot take snapshots; it never stops a
// guest), no pruning, a lock wait within the job's time, and notes that name the job on the backup server.
func (g *GuestBackups) vzdumpArgs(id, jobID string) []string {
	lockwait := max(1, int(g.Timeout.Minutes())/2)
	return []string{id, "--storage", g.Storage, "--mode", "snapshot", "--remove", "0",
		"--lockwait", strconv.Itoa(lockwait), "--notes-template", "vordr: before install, job " + jobID}
}

// backupGuest carries out a backup job. Like an install it holds the agent's lock: no install on the hypervisor
// while one of its guests is being backed up. A stopping agent waits for the backup (WaitRuns).
func (c *Client) backupGuest(j protocol.Job, send func(any) error) {
	answer := func(st wire.JobStatus) {
		st.T, st.JobID = wire.TJobStatus, j.ID
		c.Log.Printf("job %s backup %s: %s %s", j.ID, j.Guest, st.Code, st.Detail)
		c.answer(send, st) // over a newer session, or kept for the next one
	}
	g := c.Backups
	if g == nil {
		answer(wire.JobStatus{Code: protocol.CodeRejectedUnsupported})
		return
	}
	kind, id, _ := protocol.ParseGuest(j.Guest) // VerifyJob accepts backup only with a valid guest
	if !g.Exists(kind, id) {
		answer(wire.JobStatus{Code: protocol.CodeFailedBackup, Detail: j.Guest + " is not a guest of this host"})
		return
	}
	g.mu.Lock()
	last, seen := g.last[j.Guest]
	g.mu.Unlock()
	if seen && c.Now().Sub(last) < guestBackupMinGap {
		answer(wire.JobStatus{Code: protocol.CodeRejectedRateLimited, Detail: "backed up at " + last.Format(time.RFC3339)})
		return
	}
	if !c.checkMu.TryLock() {
		answer(wire.JobStatus{Code: protocol.CodeRejectedBusy})
		return
	}
	defer c.checkMu.Unlock()
	c.runs.Add(1)
	defer c.runs.Done()
	start := c.Now()
	g.mu.Lock()
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	g.last[j.Guest] = start
	g.mu.Unlock()

	// Not cut short when the agent stops: a backup killed half way leaves the backup server a broken snapshot.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.root()), g.Timeout)
	defer cancel()
	res := g.Vzdump(ctx, g.vzdumpArgs(id, j.ID)...)
	summary := &wire.RunSummary{Seconds: int(c.Now().Sub(start).Seconds())}
	summary.LogFile = c.writeRunLog(j.ID, append(append([]byte{}, res.Stdout...), res.Stderr...))
	st := wire.JobStatus{Code: protocol.CodeOK, Summary: summary}
	switch {
	case ctx.Err() != nil:
		st.Code, st.Detail = protocol.CodeTimeout, "vzdump did not finish within "+g.Timeout.String()
	case !res.Ran():
		st.Code, st.Detail = protocol.CodeFailedBackup, "vzdump: "+res.Err.Error()
	case res.ExitCode != 0:
		st.Code, st.Detail = protocol.CodeFailedBackup, "vzdump exit "+strconv.Itoa(res.ExitCode)+": "+lastLine(res.Stderr)
	}
	if st.Code != protocol.CodeOK && summary.LogFile != "" {
		// vzdump's last line ("job errors") rarely says why; its whole output is in the run log on the hypervisor.
		st.Detail += " (log " + summary.LogFile + ")"
	}
	answer(st)
}

// lastLine is the last non-empty line of out, for a human reading the result; nothing decides on it.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
