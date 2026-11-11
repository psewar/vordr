package agent

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// runJournal is written before an install run touches a package and completed when the run ends (design 10.2,
// step 6). A journal without a result at start means agent or host stopped during the run: interrupted (7.3). A
// result the hub has not acknowledged is sent on every approved session until it does (ADR-0114).
type runJournal struct {
	JobID    string           `json:"job_id"`
	Verb     protocol.Verb    `json:"verb"`
	Scope    string           `json:"scope"`
	Started  time.Time        `json:"started"`
	Code     protocol.Code    `json:"code,omitempty"`
	Detail   string           `json:"detail,omitempty"`
	Summary  *wire.RunSummary `json:"summary,omitempty"`
	Finished *time.Time       `json:"finished,omitempty"`
	// Acknowledged: the hub has stored the result (its ack), or a hub before ADR-0114 took it into its socket. A
	// journal from before ADR-0114 ("reported") reads as not acknowledged and is sent once more.
	Acknowledged bool `json:"acknowledged"`
	// Containers: the containers with a restart policy that ran before a system install (ADR-0061). The run starts
	// them again when it ends; a run cut short leaves that to the agent's next start (ADR-0125).
	Containers []string `json:"containers,omitempty"`
}

// delivered: what a successful send of a result means - with a hub that acknowledges, nothing yet.
func (c *Client) delivered(sent bool) bool { return sent && !c.hubAccepts(wire.FeatureAck) }

func (c *Client) journalPath() string { return filepath.Join(c.Cfg.StateDir, "run.json") }

// status is the run's result as the hub gets it, with when the run started: install.min_interval_h counts from it
// (ADR-0110).
func (jr runJournal) status() wire.JobStatus {
	started := jr.Started
	return wire.JobStatus{T: wire.TJobStatus, JobID: jr.JobID, Code: jr.Code, Detail: jr.Detail, Summary: jr.Summary,
		InstallStarted: &started}
}

// lastInstallAt is the start of the run in the journal, as the hello announces it (ADR-0110); nil when there is none.
func (c *Client) lastInstallAt() *time.Time {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.lastInstall.IsZero() {
		return nil
	}
	at := c.lastInstall.UTC()
	return &at
}

// startJournal records a run that is about to change packages; its start counts for install.min_interval_h.
// containers: those to bring back should the run be cut short (ADR-0125); nil for runs that do not guard them.
func (c *Client) startJournal(j protocol.Job, containers []string) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	jr := runJournal{JobID: j.ID, Verb: j.Verb, Scope: j.Scope, Started: c.Now().UTC(), Containers: containers}
	if err := writeJournal(c.journalPath(), jr); err != nil {
		return err
	}
	c.stateMu.Lock()
	c.lastInstall = jr.Started
	c.stateMu.Unlock()
	return nil
}

// finishJournal records the result and sends it over the current session if there is one.
func (c *Client) finishJournal(code protocol.Code, detail string, summary *wire.RunSummary) {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	jr, ok := c.readJournal()
	if !ok {
		return
	}
	finished := c.Now().UTC()
	jr.Code, jr.Detail, jr.Summary, jr.Finished = code, detail, summary, &finished
	// The hub's ack waits for journalMu (acknowledged), so it lands after this write.
	jr.Acknowledged = c.delivered(c.sendNow(jr.status()) == nil)
	if err := writeJournal(c.journalPath(), jr); err != nil {
		c.Log.Printf("run journal: %v", err)
	}
}

// settleJournal runs once at start: the last run's start still counts for the minimum interval, and a run without a
// result was interrupted. The containers that ran before such a run and do not run now are started again first, as
// the run would have done when it ended (ADR-0125); the result names them. That waits for the Docker daemon at most
// as long as after a run.
func (c *Client) settleJournal() {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	jr, ok := c.readJournal()
	if !ok {
		return
	}
	c.stateMu.Lock()
	c.lastInstall = jr.Started
	c.stateMu.Unlock()
	if jr.Code != "" {
		return
	}
	finished := c.Now().UTC()
	jr.Code, jr.Finished, jr.Acknowledged = protocol.CodeInterrupted, &finished, false
	jr.Detail = "the agent or the host stopped during the run; check the package database"
	c.Log.Printf("run %s was interrupted", jr.JobID)
	if len(jr.Containers) > 0 && c.Containers != nil {
		started, failed := c.Containers.Restore(c.root(), jr.Containers)
		if len(started) > 0 {
			jr.Summary = &wire.RunSummary{Restarted: started}
			c.Log.Printf("run %s: containers that ran before it, started again: %v", jr.JobID, started)
		}
		if len(failed) > 0 {
			jr.Detail += "; containers that ran before it and could not be started again: " + strings.Join(failed, ", ")
			c.Log.Printf("run %s: containers that ran before it and could not be started again: %v", jr.JobID, failed)
		}
	}
	if err := writeJournal(c.journalPath(), jr); err != nil {
		c.Log.Printf("run journal: %v", err)
	}
}

// reportJournal sends a finished run's result the hub has not acknowledged yet.
func (c *Client) reportJournal() {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	jr, ok := c.readJournal()
	if !ok || jr.Code == "" || jr.Acknowledged {
		return
	}
	if !c.delivered(c.sendNow(jr.status()) == nil) {
		return // not sent, or the hub's ack follows
	}
	jr.Acknowledged = true
	if err := writeJournal(c.journalPath(), jr); err != nil {
		c.Log.Printf("run journal: %v", err)
	}
}

// acknowledgeJournal: the hub's ack for the run in the journal; an ack for any other job changes nothing.
func (c *Client) acknowledgeJournal(jobID string) {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	jr, ok := c.readJournal()
	if !ok || jr.JobID != jobID || jr.Code == "" || jr.Acknowledged {
		return
	}
	jr.Acknowledged = true
	if err := writeJournal(c.journalPath(), jr); err != nil {
		c.Log.Printf("run journal: %v", err)
	}
}

// unacknowledgedJournal: the job of a result in the journal the hub has not acknowledged; "" when there is none.
func (c *Client) unacknowledgedJournal() string {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	jr, ok := c.readJournal()
	if !ok || jr.Code == "" || jr.Acknowledged {
		return ""
	}
	return jr.JobID
}

// readJournal: no journal (no run has changed packages yet) is a normal state, not an error.
func (c *Client) readJournal() (runJournal, bool) {
	path := c.journalPath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return runJournal{}, false
	}
	raw, err := os.ReadFile(path)
	var jr runJournal
	if err == nil {
		err = json.Unmarshal(raw, &jr)
	}
	if err != nil {
		c.Log.Printf("run journal %s unreadable: %v", path, err)
		return runJournal{}, false
	}
	return jr, true
}

// writeJournal replaces the journal atomically: a crash leaves the old or the new one, never half of each.
func writeJournal(path string, jr runJournal) error {
	raw, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
