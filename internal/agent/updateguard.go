package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"
)

// The update guard (ADR-0098). The new agent's own rollback (confirmWindow) needs a new agent that runs, the unit's
// (rollback.sh, rollback.cmd) one that ends; a new agent that hangs does neither, and systemd or the service manager
// sees it running. So the agent being replaced starts a guard before the swap, from its own binary and outside the
// agent's unit or service (armGuard): it does not depend on the new binary and outlives the old process. If the update
// is still pending after guardWindow and the previous binary is still kept, the guard puts that back and has the
// service manager start it, ending the hung agent first. It ends as soon as the update is settled. On Windows it also
// starts the service again when the agent stops cleanly for its restart (ADR-0103).

// guardWindow: the new agent's own confirmWindow first, then its restart delays and the unit's rollback (rollback.cmd
// after up to 6 minutes) - the guard only acts where none of them did.
const guardWindow = confirmWindow + 5*time.Minute

// Guard watches one self-update, Job, from outside the new binary.
type Guard struct {
	StateDir string                      // where update.json lies
	Exe      string                      // the agent binary in place; the previous one is Exe.old
	Job      string                      // the agent_update job it watches
	Restart  func(context.Context) error // the service manager starts the binary now in place, ending a hung agent
	Log      *log.Logger
	Window   time.Duration // guardWindow
	Poll     time.Duration // how often it looks at the marker
	// Requests: the agent asks for its planned restart (Windows, ADR-0103); nil where the service manager restarts it.
	Requests <-chan struct{}
	// Start starts the service again once it has stopped, for a request.
	Start func(context.Context) error
}

// NewGuard returns the guard for job with the real windows.
func NewGuard(stateDir, exe, job string, restart func(context.Context) error, logger *log.Logger) Guard {
	return Guard{StateDir: stateDir, Exe: exe, Job: job, Restart: restart, Log: logger, Window: guardWindow,
		Poll: 10 * time.Second}
}

// GuardOutcome is what the guard found and did.
type GuardOutcome string

const (
	GuardSettled     GuardOutcome = "settled"           // the marker is gone: confirmed, or reported by the old agent
	GuardOtherJob    GuardOutcome = "other_update"      // the marker is another update's
	GuardUnreadable  GuardOutcome = "marker_unreadable" // nothing to go by; the next agent start removes it
	GuardAlreadyBack GuardOutcome = "already_back"      // no Exe.old any more: the new agent or the unit put it back
	GuardRolledBack  GuardOutcome = "rolled_back"
	GuardFailed      GuardOutcome = "failed" // putting back or the restart failed; the log says what
	GuardStopped     GuardOutcome = "stopped"
)

// Watch looks at the marker every Poll until the update is settled or Window has passed, then acts.
// While it waits, it only asks whether the marker exists: an open file would stop the agent on Windows from removing
// it (its confirmation) or replacing it. The marker stays this job's until it is removed, so the guard reads it once,
// at the end.
func (g Guard) Watch(ctx context.Context) GuardOutcome {
	path := markerFile(g.StateDir)
	for deadline := time.Now().Add(g.Window); time.Now().Before(deadline); {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return GuardSettled
		}
		select {
		case <-ctx.Done():
			return GuardStopped
		case <-g.Requests: // a nil channel never delivers
			g.startAgain(ctx)
		case <-time.After(g.Poll):
		}
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return GuardSettled
	}
	raw, err := readShared(path)
	if errors.Is(err, fs.ErrNotExist) {
		return GuardSettled // removed between the check and the read: a race no check can rule out
	}
	var m updateMarker
	if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	switch {
	case err != nil:
		g.Log.Printf("update marker %s: %v", path, err)
		return GuardUnreadable
	case m.JobID != g.Job:
		return GuardOtherJob
	}
	return g.rollBack(ctx, path, m)
}

// startAgain answers the agent's request for its planned restart. If the start fails, the update stays pending and
// the guard puts the previous binary back at the end of its window, as for a hung agent.
func (g Guard) startAgain(ctx context.Context) {
	if g.Start == nil {
		g.Log.Printf("job %s: restart asked for, but this guard cannot start the service", g.Job)
		return
	}
	if err := g.Start(ctx); err != nil {
		g.Log.Printf("job %s: planned restart: %v - the previous binary comes back at the end of the window", g.Job, err)
		return
	}
	g.Log.Printf("job %s: started the agent again after its planned stop", g.Job)
}

func (g Guard) rollBack(ctx context.Context, path string, m updateMarker) GuardOutcome {
	if _, err := os.Stat(g.Exe + ".old"); errors.Is(err, fs.ErrNotExist) {
		return GuardAlreadyBack // the agent that is back reports, once it reaches the hub
	}
	g.Log.Printf("update %s -> %s (job %s) not confirmed within %s - putting %s back", m.From, m.To, m.JobID,
		g.Window, m.From)
	if err := putBack(g.Exe); err != nil {
		g.Log.Printf("putting back %s.old: %v", g.Exe, err)
		return GuardFailed
	}
	m.RolledBack = fmt.Sprintf("hung: not confirmed within %s, the update guard put %s back", g.Window, m.From)
	if err := writeJSON(path, m); err != nil {
		g.Log.Printf("update marker: %v - the report will not say why", err)
	}
	if err := g.Restart(ctx); err != nil {
		g.Log.Printf("restart: %v - %s is back in place and runs from the next start", err, m.From)
		return GuardFailed
	}
	return GuardRolledBack
}
