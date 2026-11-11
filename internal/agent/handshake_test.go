package agent

import (
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// What the hello names (ADR-0114): jobs being carried out, the journal's result until the hub acknowledged it, a
// self-update to report, and a reboot - as running until the host went down, then as a result to report.
func TestRunStateNamesRunsAndHeldResults(t *testing.T) {
	c := installClient(t, nil, allowAll)
	if r := c.runState(); len(r.Running) != 0 || len(r.Unacknowledged) != 0 {
		t.Fatalf("nothing yet: %+v", r)
	}
	c.setRunning("j-check", true)
	fin := time.Now().UTC()
	if err := writeJournal(c.journalPath(), runJournal{JobID: "j-install", Started: fin, Code: protocol.CodeOK,
		Finished: &fin}); err != nil {
		t.Fatal(err)
	}
	c.update = &updateMarker{JobID: "j-update"}
	if err := writeJSON(c.rebootPath(), rebootMarker{JobID: "j-reboot", At: fin}); err != nil {
		t.Fatal(err)
	}
	c.setRebooting(true)
	r := c.runState()
	if !slices.Equal(r.Running, []string{"j-check", "j-reboot"}) ||
		!slices.Equal(r.Unacknowledged, []string{"j-install", "j-update"}) {
		t.Fatalf("while the reboot is pending: %+v", r)
	}
	var none outbox
	c.reportReboot(none.send)
	if len(none.sent) != 0 {
		t.Fatalf("reported its own reboot before the host went down: %+v", none.sent)
	}

	c.setRebooting(false) // the agent after the boot
	c.setRunning("j-check", false)
	c.acknowledged("j-install")
	r = c.runState()
	if len(r.Running) != 0 || !slices.Equal(r.Unacknowledged, []string{"j-reboot", "j-update"}) {
		t.Fatalf("after the boot: %+v", r)
	}
	c.acknowledged("j-reboot")
	c.update = nil
	if r := c.runState(); len(r.Unacknowledged) != 0 {
		t.Fatalf("after the acks: %+v", r)
	}
	if jr, _ := c.readJournal(); !jr.Acknowledged {
		t.Fatalf("journal: %+v", jr)
	}
}

// A final status without a journal that finds no session (it finished between a hello that named the job as running
// and the new session) is kept, named in the next hello, and sent on the next approved session.
func TestAnswerWithoutASessionGoesOutOnTheNextOne(t *testing.T) {
	c := installClient(t, nil, allowAll)
	broken := func(any) error { return errNoSession }
	c.answer(broken, wire.JobStatus{T: wire.TJobStatus, JobID: "j-check", Code: protocol.CodeOK})
	if r := c.runState(); !slices.Equal(r.Unacknowledged, []string{"j-check"}) {
		t.Fatalf("hello: %+v", r)
	}
	var next outbox
	c.approvedSession(next.send)
	if fin := next.final(); fin.JobID != "j-check" || fin.Code != protocol.CodeOK {
		t.Fatalf("on the next session: %+v", next.sent)
	}
	if r := c.runState(); len(r.Unacknowledged) != 0 {
		t.Fatalf("kept after it went out: %+v", r)
	}
	var direct outbox
	c.answer(direct.send, wire.JobStatus{T: wire.TJobStatus, JobID: "j2", Code: protocol.CodeOK})
	if r := c.runState(); len(direct.sent) != 1 || len(r.Unacknowledged) != 0 {
		t.Fatalf("sent at once: %+v, %+v", direct.sent, r)
	}
}
