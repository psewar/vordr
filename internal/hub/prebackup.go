package hub

import (
	"context"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// A fresh backup right before the install (ADR-0096, design 10.2). Before an install of a Proxmox guest the hub has
// the hypervisor's agent back the guest up, checks on PBS that the backup is there, and only then sends the install:
// the way back is minutes old instead of up to a day. The hub gets no right to write anything for this - PBS is only
// read, as for the age check; the backup is a closed verb of the hypervisor's agent, allowed by its own agent.yaml
// (guest_backup), which only ever adds a backup.

const (
	// preBackupTimeout bounds the wait of one install for its backup: other installs' backups before it (one at a
	// time), a run of the hypervisor itself, the backup.
	preBackupTimeout = 5 * time.Hour
	// backupReuse: a backup of the guest this young is used instead of a new one - the scheduled job ran just before,
	// or the same install is tried again. Longer than the agent's minimum gap per guest, so the hub never meets it.
	backupReuse = 15 * time.Minute
	// backupTries: a hypervisor busy with a short check of its own is asked again this often.
	backupTries = 3
	// AdminBackupHostUnavailable: the hypervisor's agent, which makes the backup, is not there (unknown, not
	// approved, not connected, or without guest_backup in its agent.yaml).
	AdminBackupHostUnavailable AdminCode = "backup_host_unavailable"
)

// idlePoll: how often a backup that waits for the hypervisor's own run looks again.
var idlePoll = 5 * time.Second

// pendingInstall: an install whose run waits in phase backing_up; WaitJob sends job once group is backed up.
type pendingInstall struct {
	job   protocol.Job
	by    string
	group string
}

// backupGate is the backup decision before an install on node (Home Assistant, automatic installs, vordr-hub
// backup): the age check of ADR-0030, or - for a guest that gets a fresh backup first - whether that backup can be
// made now. A refusal comes before the run starts. An appliance's configuration backup counts as its last report
// names it (ADR-0090).
func (s *Server) backupGate(ctx context.Context, node string) (AdminCode, *wire.BackupView) {
	var configAt *time.Time
	if e, ok := s.Reg.Get(node); ok {
		configAt = ReportedConfigBackup(e.LastReport)
	}
	code, v := s.Backups.Gate(ctx, node, configAt)
	if code == AdminOK && v.BeforeInstall != "" {
		if _, c := s.sendable(v.BeforeInstall, protocol.VerbBackup); c != AdminOK {
			code = AdminBackupHostUnavailable
		}
	}
	return code, v
}

// awaitBackup starts the run of an install that waits for its guest's backup: phase backing_up, nothing sent yet.
func (s *Server) awaitBackup(node string, job protocol.Job, by, group string) *SentJob {
	run := Run{JobID: job.ID, Verb: job.Verb, Scope: job.Scope, By: by, OutsideWindow: job.OutsideWindow,
		Phase: protocol.PhaseBackingUp, Started: s.Now()}
	s.Events.Log(node, "install_awaits_backup", map[string]any{"job_id": job.ID, "scope": job.Scope, "by": by,
		"group": group})
	s.storeRun(node, run)
	return &SentJob{ID: job.ID, node: node, run: run, pending: &pendingInstall{job: job, by: by, group: group}}
}

// backupFirst makes the backup the install waits for, then sends the install and turns j into the sent job. false:
// no fresh backup came about, or the install could not go out; st is the run's result then, and nothing was
// installed.
func (s *Server) backupFirst(ctx context.Context, j *SentJob) (wire.JobStatus, bool) {
	p := j.pending
	refused := func(code protocol.Code, when string) (wire.JobStatus, bool) {
		s.Events.Log(j.node, "pre_install_refused", map[string]any{"job_id": j.ID, "code": code, "when": when})
		return wire.JobStatus{JobID: j.ID, Code: code,
			Detail: "the install waited for its guest's backup; " + when + " the host would no longer take it"}, false
	}
	bctx, cancel := context.WithTimeout(ctx, preBackupTimeout)
	at, why, early := s.freshBackup(bctx, j.node, p.group, j.ID, func() protocol.Code { return s.stillWanted(j.node, p) })
	cancel()
	if early != "" {
		return refused(early, "before the backup")
	}
	fields := map[string]any{"job_id": j.ID, "group": p.group}
	if why != "" {
		fields["code"], fields["detail"] = protocol.CodeFailedBackup, why
		s.Events.Log(j.node, "pre_install_backup", fields)
		return wire.JobStatus{JobID: j.ID, Code: protocol.CodeFailedBackup, Detail: why}, false
	}
	fields["code"], fields["backup"] = protocol.CodeOK, at.UTC().Format(time.RFC3339)
	s.Events.Log(j.node, "pre_install_backup", fields)
	if code := s.stillWanted(j.node, p); code != "" {
		return refused(code, "after the backup")
	}
	sent, code := s.sendJob(j.node, p.job, p.by, j.run.Started)
	if code != AdminOK {
		// The backup is there, but the host is not (offline, its agent restarting): nothing was installed.
		return wire.JobStatus{JobID: j.ID, Code: protocol.CodeInterrupted,
			Detail: "backed up, but the install could not be sent: " + string(code)}, false
	}
	*j = *sent
	return wire.JobStatus{}, true
}

// stillWanted decides again what startJob decided when the run began, once the install's turn for the backup has come
// and once more before it goes out: other guests' backups and its own take time, and the host's window can close
// meanwhile. An install from Home Assistant without a reason of the person's then gets the button's reason, as it would
// have had at the start (ADR-0109); any other install without a reason, and one the minimum interval keeps out
// (ADR-0110), ends with the code the agent would answer, and nothing is sent. "" while the install is still wanted.
func (s *Server) stillWanted(node string, p *pendingInstall) protocol.Code {
	if p.job.OutsideWindow == "" && strings.HasPrefix(p.by, byHAPrefix) {
		p.job.OutsideWindow = s.haReason(node, p.job.Verb, "")
	}
	switch {
	case s.windowRefuses(node, p.job):
		return protocol.CodeRejectedOutsideWindow
	case s.intervalRefuses(node, p.job):
		return protocol.CodeRejectedRateLimited
	}
	return ""
}

// freshBackup has the hypervisor back up group for node's install and returns when PBS has the backup; why says
// what failed. The backup counts only when the hypervisor's agent reports it done and PBS has a backup of the group
// from no earlier than the moment the hub asked for it. One backup at a time: the hypervisor's agent runs one job at
// a time, and vzdump keeps one lock per host anyway. ready runs once the turn has come, before anything is backed
// up; a code from it ends the wait there (early), without a backup.
func (s *Server) freshBackup(ctx context.Context, node, group, jobID string, ready func() protocol.Code) (time.Time,
	string, protocol.Code) {
	select {
	case s.backupSem <- struct{}{}:
	case <-ctx.Done():
		return time.Time{}, "no turn for the backup in time: other backups before it took too long", ""
	}
	defer func() { <-s.backupSem }()
	if code := ready(); code != "" {
		return time.Time{}, "", code
	}
	asked := s.Now()
	if at, why := s.Backups.Since(ctx, group, asked.Add(-backupReuse)); why == "" {
		return at, "", "" // made minutes ago: the scheduled backup, or this install tried again
	}
	if why := s.askBackup(ctx, group, node, jobID); why != "" {
		return time.Time{}, why, ""
	}
	at, why := s.Backups.Since(ctx, group, asked.Add(-protocol.MaxClockSkew))
	return at, why, ""
}

// askBackup has the hypervisor back up group, waiting for a run of its own to end first; "" when its agent reports
// the backup done.
func (s *Server) askBackup(ctx context.Context, group, node, jobID string) string {
	hv := s.Backups.Hypervisor()
	for try := 1; ; try++ {
		if !s.waitIdle(ctx, hv) {
			return hv + " was busy with a run of its own until the wait ended"
		}
		st, code := s.backupJob(ctx, hv, group, node, jobID)
		switch {
		case code != AdminOK:
			return hv + ": " + string(code)
		case st.Code == protocol.CodeRejectedBusy && try < backupTries:
			// A check of the hypervisor's own got the agent's lock first; it is over in moments.
			select {
			case <-ctx.Done():
				return hv + " stayed busy"
			case <-time.After(idlePoll):
			}
		case st.Code != protocol.CodeOK:
			return strings.TrimSpace(hv + ": " + string(st.Code) + " " + st.Detail)
		default:
			return ""
		}
	}
}

// waitIdle waits until the hypervisor has no run of its own going (an install by button, an agent update).
func (s *Server) waitIdle(ctx context.Context, hv string) bool {
	for s.running(hv) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(idlePoll):
		}
	}
	return true
}

// backupJob sends one backup job to the hypervisor and waits for its result. It is no run of the hypervisor's own:
// its outcome belongs to the install it is made for, so the hypervisor's last run stays its own. While it goes, a
// command for the hypervisor waits (running) and starts afterwards.
func (s *Server) backupJob(ctx context.Context, hv, group, node, jobID string) (wire.JobStatus, AdminCode) {
	sess, code := s.sendable(hv, protocol.VerbBackup)
	if code != AdminOK {
		return wire.JobStatus{}, code
	}
	s.setBackingUp(hv, true)
	defer func() {
		s.setBackingUp(hv, false)
		s.dispatchNext(hv)
	}()
	job, ch, code := s.signAndSend(sess, hv, protocol.Job{ID: newJobID(), Verb: protocol.VerbBackup, Guest: group})
	if code != AdminOK {
		return wire.JobStatus{}, code
	}
	s.Events.Log(hv, "job_sent", map[string]any{"job_id": job.ID, "verb": job.Verb, "guest": group, "for": node,
		"for_job": jobID})
	select {
	case st, open := <-ch:
		if !open {
			return wire.JobStatus{}, AdminNotConnected // the hypervisor's connection ended during the backup
		}
		return st, AdminOK
	case <-ctx.Done():
		sess.mu.Lock()
		delete(sess.waiters, job.ID)
		sess.mu.Unlock()
		return wire.JobStatus{}, AdminTimeout
	}
}

func (s *Server) setBackingUp(hv string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.backingUp[hv] = true
	} else {
		delete(s.backingUp, hv)
	}
}

func (s *Server) backingUpOn(node string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backingUp[node]
}

// endOrphanedBackups ends installs that a previous hub left waiting for their backup. Nobody sends them any more and
// their host's agent never got them: nothing was installed.
func (s *Server) endOrphanedBackups() {
	for _, e := range s.Reg.List() {
		r := e.LastRun
		if r == nil || r.Code != "" || r.Phase != protocol.PhaseBackingUp {
			continue
		}
		fin := s.Now()
		run := *r
		run.Phase, run.Code, run.Finished = "", protocol.CodeInterrupted, &fin
		run.Detail = "the hub restarted while the install waited for its backup; nothing was installed"
		if err := s.Reg.SetRun(e.Node, run); err != nil {
			s.Log.Printf("register: %v", err)
		}
		s.Events.Log(e.Node, "pre_install_backup", map[string]any{"job_id": r.JobID, "code": protocol.CodeInterrupted})
	}
}
