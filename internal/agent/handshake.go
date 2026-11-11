package agent

import (
	"slices"

	"github.com/psewar/vordr/internal/wire"
)

// The run handshake (ADR-0114): the hello names the jobs this agent carries out and the results it holds, and the
// hub acknowledges every final status it stored. The run journal and the reboot marker count a result as reported
// only on that ack. The self-update keeps its own rule: the marker goes once the result is sent on the first approved
// session, because the update guard takes its removal as the confirmation (ADR-0098).

// setRunning marks a job as being carried out, or no longer.
func (c *Client) setRunning(jobID string, on bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.running == nil {
		c.running = map[string]bool{}
	}
	if on {
		c.running[jobID] = true
	} else {
		delete(c.running, jobID)
	}
}

func (c *Client) setRebooting(on bool) {
	c.stateMu.Lock()
	c.rebooting = on
	c.stateMu.Unlock()
}

// answer sends a final status that has no journal: over the session that brought the job, else over the current one.
// With neither it waits in memory for the next approved session (flushUnsent) and is named in its hello: that hello
// may already have named the job as running, and the hub waits for its result. Kept first, then
// tried, so a session that comes up meanwhile finds it either way; a second copy changes nothing at the hub.
func (c *Client) answer(send func(any) error, st wire.JobStatus) {
	if send(st) == nil {
		return
	}
	c.stateMu.Lock()
	if c.unsent == nil {
		c.unsent = map[string]wire.JobStatus{}
	}
	c.unsent[st.JobID] = st
	c.stateMu.Unlock()
	if c.sendNow(st) == nil {
		c.dropUnsent(st.JobID)
	}
}

func (c *Client) dropUnsent(jobID string) {
	c.stateMu.Lock()
	delete(c.unsent, jobID)
	c.stateMu.Unlock()
}

// flushUnsent sends the final statuses no session took, on an approved session right after it became the way out.
func (c *Client) flushUnsent() {
	c.stateMu.Lock()
	pending := make([]wire.JobStatus, 0, len(c.unsent))
	for _, st := range c.unsent {
		pending = append(pending, st)
	}
	c.stateMu.Unlock()
	for _, st := range pending {
		if c.sendNow(st) == nil {
			c.dropUnsent(st.JobID)
		}
	}
}

// runState is what the hello says about this agent's runs.
func (c *Client) runState() wire.Runs {
	r := wire.Runs{Running: []string{}, Unacknowledged: []string{}}
	c.stateMu.Lock()
	for id := range c.running {
		r.Running = append(r.Running, id)
	}
	for id := range c.unsent {
		r.Unacknowledged = append(r.Unacknowledged, id)
	}
	update, rebooting := c.update, c.rebooting
	c.stateMu.Unlock()
	if id := c.unacknowledgedJournal(); id != "" {
		r.Unacknowledged = append(r.Unacknowledged, id)
	}
	if update != nil {
		r.Unacknowledged = append(r.Unacknowledged, update.JobID) // reported on this session (reportUpdate)
	}
	if m, ok := c.readRebootMarker(); ok && !m.Reported {
		if rebooting {
			r.Running = append(r.Running, m.JobID) // the host has not gone down yet
		} else {
			r.Unacknowledged = append(r.Unacknowledged, m.JobID)
		}
	}
	slices.Sort(r.Running)
	r.Running = slices.Compact(r.Running)
	slices.Sort(r.Unacknowledged)
	r.Unacknowledged = slices.Compact(r.Unacknowledged)
	return r
}

// acknowledged: the hub stored the final status of a job.
func (c *Client) acknowledged(jobID string) {
	c.acknowledgeJournal(jobID)
	c.acknowledgeReboot(jobID)
}

// acknowledgeReboot marks the reboot marker's result as reported once the hub acknowledged it.
func (c *Client) acknowledgeReboot(jobID string) {
	m, ok := c.readRebootMarker()
	if !ok || m.JobID != jobID || m.Reported {
		return
	}
	m.Reported = true
	if err := writeJSON(c.rebootPath(), m); err != nil {
		c.Log.Printf("reboot marker: %v", err)
	}
}
