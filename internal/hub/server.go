package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// JobLifetime: how long a signed job stays valid (protocol.MaxJobLifetime is the upper bound agents accept).
const JobLifetime = 2 * time.Minute

var nodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

// Server serves the tunnel endpoint for agents, the Home Assistant endpoint and administration requests.
type Server struct {
	Reg        *Registry
	Clients    *ClientRegistry // Home Assistant clients
	Events     *Events
	Key        *identity.Identity // the hub key; signs every job
	MaxPending int
	Limits     Limits
	Version    string // shown in Home Assistant
	Now        func() time.Time
	Log        *log.Logger
	// Backups: the backup check before installs; nil when hub.yaml has none.
	Backups *Backups
	// Releases: agent releases and their rollout; nil when hub.yaml has no agent_update section.
	Releases *Releases
	// AutoInstall: install runs the hub starts itself (install.auto: hub); nil without an auto_install section.
	AutoInstall *AutoInstall
	// Packages: the hosts' pip/npm inventories and the OSV data they are matched against; nil: the hub does not
	// announce FeatureInventory, and agents send none.
	Packages *Packages

	mu         sync.Mutex
	sessions   map[string]*session
	haSessions map[string]*haSession
	lastSeen   map[string]time.Time // when a host was last connected (in memory)
	// successors: jobs whose agent announced its own restart, waiting for the agent that starts next to report.
	successors map[string]chan wire.JobStatus
	// queues: commands waiting for the host's running run (queue.go); dispatching: a host whose next one is starting.
	queues      map[string][]queuedJob
	dispatching map[string]bool
	// backupSem: one backup before an install at a time (prebackup.go); backingUp: the hypervisor works on one now.
	backupSem chan struct{}
	backingUp map[string]bool
	// claims: per host, the session whose hello named the jobs the agent runs or reports (ADR-0114).
	claims   map[string]*session
	started  time.Time
	upgrader websocket.Upgrader
	// conns: the tunnel and Home Assistant connections being served; work counts them and the goroutines the server
	// started (spawn), and Close waits for it; closing: Close has begun, nothing new starts (close.go, ADR-0142).
	// conns and closing under mu. closed ends when Close begins (endClosed); the waits for jobs listen to it.
	conns     map[*wire.Conn]struct{}
	work      sync.WaitGroup
	closing   bool
	closed    context.Context
	endClosed context.CancelFunc
}

type session struct {
	node     string
	conn     *wire.Conn
	mu       sync.Mutex
	lastSeen time.Time
	waiters  map[string]chan wire.JobStatus
	// acks: the agent named its runs in the hello and takes an ack for every final status (ADR-0114).
	acks bool
	// claimed: the jobs its hello named as running or as results it holds; claimedRunning: those named as running
	// whose result has not come yet. Both under Server.mu.
	claimed        []string
	claimedRunning []string
}

// NewServer starts with the default limits of hub.yaml; the caller may set Limits and Version before serving. An
// install that a previous hub left waiting for its backup ends here (endOrphanedBackups).
func NewServer(reg *Registry, clients *ClientRegistry, events *Events, key *identity.Identity, maxPending int,
	logger *log.Logger) *Server {
	s := &Server{
		Reg: reg, Clients: clients, Events: events, Key: key, MaxPending: maxPending, Now: time.Now, Log: logger,
		Limits: Limits{StaleAfter: defaultStaleDays * 24 * time.Hour, OfflineAfter: defaultOfflineMin * time.Minute,
			RebootAfter: defaultRebootDays * 24 * time.Hour, NoReportAfter: defaultNoReportH * time.Hour},
		Version: "dev", sessions: map[string]*session{}, haSessions: map[string]*haSession{},
		lastSeen: map[string]time.Time{}, successors: map[string]chan wire.JobStatus{},
		queues: map[string][]queuedJob{}, dispatching: map[string]bool{}, started: time.Now(),
		backupSem: make(chan struct{}, 1), backingUp: map[string]bool{}, claims: map[string]*session{},
		conns: map[*wire.Conn]struct{}{},
		upgrader: websocket.Upgrader{
			HandshakeTimeout: 10 * time.Second,
			// Agents and Home Assistant send no Origin header. Browsers always do: refusing them stops cross-site
			// WebSocket hijacking.
			CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
		},
	}
	s.closed, s.endClosed = context.WithCancel(context.Background())
	s.endOrphanedBackups()
	return s
}

// Handler serves only the tunnel path and the Home Assistant path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(wire.TunnelPath, s.serveTunnel)
	mux.HandleFunc(wire.HAPath, s.serveHA)
	if s.Releases != nil {
		mux.Handle(AgentReleasePath, s.Releases)
	}
	return mux
}

func (s *Server) serveTunnel(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has answered the request already
	}
	conn := wire.NewConn(ws)
	defer conn.Close()
	if !s.open(conn) {
		return // the hub is stopping; the agent connects again later
	}
	defer s.release(conn)

	sess, ok := s.login(conn, r.RemoteAddr)
	if !ok {
		return
	}
	defer s.drop(sess)
	s.loop(sess)
}

// login runs challenge and hello and answers with the registration state. Only pending and approved hosts stay.
func (s *Server) login(conn *wire.Conn, remote string) (*session, bool) {
	nonce := make([]byte, wire.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, false
	}
	// The hello comes before the status: what it may carry beyond the base protocol is announced here (ADR-0114).
	if conn.Send(wire.Challenge{T: wire.TChallenge, Nonce: nonce, Features: []string{wire.FeatureAck}}) != nil {
		return nil, false
	}
	t, raw, err := conn.Receive(wire.HelloTimeout)
	if err != nil {
		return nil, false
	}
	var h wire.Hello
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if t != wire.THello || dec.Decode(&h) != nil || !nodePattern.MatchString(h.Node) ||
		len(h.PublicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(h.PublicKey, wire.HelloSigned(nonce, h.Node, h.PublicKey), h.Signature) {
		s.Events.Log("", "hello_refused", map[string]any{"reason": wire.ReasonBadHello, "remote": remote})
		_ = conn.Send(wire.Status{T: wire.TStatus, State: wire.StateRejected, Reason: wire.ReasonBadHello})
		return nil, false
	}
	fp := identity.Fingerprint(h.PublicKey)
	state, reason, err := s.Reg.Admit(h, fp, s.Now(), s.MaxPending)
	if err != nil {
		s.Log.Printf("register: %v", err)
		return nil, false
	}
	s.Events.Log(h.Node, "hello", map[string]any{"state": state, "reason": reason, "fingerprint": fp,
		"agent_version": h.AgentVersion, "remote": remote})
	sess := &session{node: h.Node, conn: conn, lastSeen: s.Now(), waiters: map[string]chan wire.JobStatus{},
		acks: h.Runs != nil}
	if state == wire.StateApproved && h.Runs != nil {
		// Before any job can reach the host: what the agent says about its runs replaces the hub's guess.
		s.claim(sess, *h.Runs)
		s.reconcileRuns(h.Node, *h.Runs)
	}
	if conn.Send(wire.Status{T: wire.TStatus, State: state, Reason: reason, Fingerprint: fp, Features: s.features()}) != nil ||
		state == wire.StateRejected {
		s.unclaim(sess)
		return nil, false
	}
	s.mu.Lock()
	if old := s.sessions[h.Node]; old != nil {
		old.conn.Close() // the agent reconnected; the old connection is dead or stale
	}
	s.sessions[h.Node] = sess
	s.mu.Unlock()
	s.changed(h.Node)
	return sess, true
}

// features: what this hub accepts beyond the base protocol, told to every agent in its status.
func (s *Server) features() []string {
	f := []string{wire.FeatureAck}
	if s.Packages == nil {
		return f
	}
	f = append(f, wire.FeatureInventory, wire.FeatureContainers)
	if s.Packages.OSVOn() {
		f = append(f, wire.FeatureVet)
	}
	if s.Packages.ImageContentsOn() {
		f = append(f, wire.FeatureImageContents) // a distribution's dump in osv.ecosystems (ADR-0120)
	}
	return f
}

// vet answers an agent's plan of a pip/npm update (design 11.4) with a statement signed like a job. Only for a run
// of that host that is an update of packages or a refresh of dependencies (ADR-0104: there it also names which
// installed dependencies have findings); anything else gets "no_running_update" - the agent then refuses.
func (s *Server) vet(node string, req wire.Vet) wire.VetResult {
	v := protocol.Vet{JobID: req.JobID, Node: node, Ecosystem: req.Ecosystem, IssuedAt: s.Now().UTC(),
		Packages: []protocol.VetPackage{}}
	run, ok := s.currentRun(node, req.JobID)
	switch {
	case !ok || run.Verb != protocol.VerbInstall || run.Code != "" ||
		!strings.HasPrefix(run.Scope, "pkg:") && !strings.HasPrefix(run.Scope, "deps:"):
		v.Unavailable = "no_running_update"
	default:
		v.Packages, v.Unavailable = s.Packages.Vet(versions.Ecosystem(req.Ecosystem), req.Packages)
	}
	flagged := 0
	for _, p := range v.Packages {
		if len(p.Flagged) > 0 {
			flagged++
		}
	}
	s.Events.Log(node, "vet", map[string]any{"job_id": req.JobID, "ecosystem": req.Ecosystem,
		"packages": len(req.Packages), "flagged": flagged, "unavailable": v.Unavailable})
	sv, err := protocol.SignVet(v, s.Key.Sign)
	if err != nil {
		s.Log.Printf("vet: %v", err)
	}
	return wire.VetResult{T: wire.TVetResult, JobID: req.JobID, Vet: sv}
}

// inventoryFields: what the event log keeps of an inventory - counts, never the package list.
func inventoryFields(v *wire.PackagesView) map[string]any {
	if v == nil {
		return nil
	}
	f := map[string]any{"locations": len(v.Locations), "packages": v.Total}
	if v.Vulnerable != nil {
		f["vulnerable"], f["malicious"] = *v.Vulnerable, *v.Malicious
	}
	return f
}

// changed tells every Home Assistant client that a host's view may have changed (they compare and send the difference).
func (s *Server) changed(string) {
	s.changedHA()
}

// ViewsChanged: something every view shows has changed (e.g. the backup check refreshed); clients get new views.
func (s *Server) ViewsChanged() { s.changedHA() }

func (s *Server) drop(sess *session) {
	s.mu.Lock()
	if s.sessions[sess.node] == sess {
		delete(s.sessions, sess.node)
		s.lastSeen[sess.node] = s.Now()
		s.Events.Log(sess.node, "disconnected", nil)
	}
	if s.claims[sess.node] == sess {
		delete(s.claims, sess.node) // what it said about its runs ends with it
	}
	s.mu.Unlock()
	s.changed(sess.node)
	sess.mu.Lock()
	for id, ch := range sess.waiters {
		close(ch)
		delete(sess.waiters, id)
	}
	sess.mu.Unlock()
}

// loop reads until the agent goes quiet for IdleTimeout or sends something it must not.
func (s *Server) loop(sess *session) {
	for {
		t, raw, err := sess.conn.Receive(wire.IdleTimeout)
		if err != nil {
			return
		}
		sess.mu.Lock()
		sess.lastSeen = s.Now()
		sess.mu.Unlock()
		if t != wire.TPower {
			s.awake(sess.node) // a host that sends anything else is up, whatever it said before
		}
		switch t {
		case wire.TPower:
			var p wire.Power
			if json.Unmarshal(raw, &p) != nil || !wire.ValidPower(p.State) {
				s.Events.Log(sess.node, "protocol_violation", map[string]any{"type": t})
				return
			}
			if err := s.Reg.SetPower(sess.node, p.State, s.Now()); err != nil {
				s.Log.Printf("register: %v", err)
			}
			s.Events.Log(sess.node, "power", map[string]any{"state": p.State})
			s.changed(sess.node)
		case wire.THeartbeat:
			// The answer lets the agent tell a quiet hub from a dead connection.
			if sess.conn.Send(wire.Heartbeat{T: wire.THeartbeat}) != nil {
				return
			}
		case wire.TReport:
			var rep wire.Report
			if json.Unmarshal(raw, &rep) != nil {
				s.Events.Log(sess.node, "protocol_violation", map[string]any{"type": t})
				return
			}
			if e, ok := s.Reg.Get(sess.node); !ok || e.State != wire.StateApproved {
				continue // pending hosts do not report; ignore rather than store
			}
			if err := s.Reg.SetReportAt(sess.node, rep, s.Now()); err != nil {
				s.Log.Printf("register: %v", err)
			}
			s.Events.Log(sess.node, "report", reportFields(rep))
			s.changed(sess.node)
		case wire.TJobStatus:
			var st wire.JobStatus
			if json.Unmarshal(raw, &st) != nil {
				s.Events.Log(sess.node, "protocol_violation", map[string]any{"type": t})
				return
			}
			s.deliver(sess, st)
		case wire.TVet:
			var req wire.Vet
			if s.Packages == nil || !s.Packages.OSVOn() || json.Unmarshal(raw, &req) != nil ||
				len(req.Packages) > wire.MaxVetPackages {
				s.Events.Log(sess.node, "protocol_violation", map[string]any{"type": t})
				return // without OSV data the hub never announced FeatureVet
			}
			if sess.conn.Send(s.vet(sess.node, req)) != nil {
				return
			}
		case wire.TInventory:
			var inv wire.Inventory
			if s.Packages == nil || json.Unmarshal(raw, &inv) != nil {
				s.Events.Log(sess.node, "protocol_violation", map[string]any{"type": t})
				return // without Packages the hub never announced the feature
			}
			if e, ok := s.Reg.Get(sess.node); !ok || e.State != wire.StateApproved {
				continue
			}
			if err := s.Packages.SetInventory(sess.node, inv); err != nil {
				s.Log.Printf("packages: %v", err)
			}
			s.Events.Log(sess.node, "inventory", inventoryFields(s.Packages.View(sess.node)))
			s.changed(sess.node)
		default:
			s.Events.Log(sess.node, "protocol_violation", map[string]any{"type": t})
			return
		}
	}
}

// awake clears a host's announced sleep or shutdown once it is heard from again over the same connection (a short
// sleep the connection survived); after a reconnect its hello does it.
func (s *Server) awake(node string) {
	if e, ok := s.Reg.Get(node); !ok || e.Power == "" {
		return
	}
	if err := s.Reg.SetPower(node, "", s.Now()); err != nil {
		s.Log.Printf("register: %v", err)
	}
	s.changed(node)
}

func reportFields(rep wire.Report) map[string]any {
	f := map[string]any{}
	for _, src := range rep.Sources {
		f[src.Source] = map[string]any{"state": src.State, "code": src.Code, "pending": src.Pending, "security": src.Security}
	}
	if rep.Reboot != nil {
		f["reboot_required"] = rep.Reboot.Required
	}
	return f
}

// deliver hands a final job status to the request waiting for it. Phases and progress of the host's current run go
// into its last run, so Home Assistant sees them. A final status nobody waits for any more - the agent reconnected
// and reports a run the hub had to count as interrupted - still settles that run. An agent that takes acks gets one
// once the status is stored (ADR-0114), also for a second copy, which changes nothing else.
func (s *Server) deliver(sess *session, st wire.JobStatus) {
	fields := map[string]any{"job_id": st.JobID, "phase": st.Phase, "code": st.Code}
	if st.Reason != "" {
		fields["reason"] = st.Reason
	}
	if !st.Final() {
		s.progress(sess.node, st)
		return // progress is not an event worth keeping
	}
	sess.mu.Lock()
	ch := sess.waiters[st.JobID]
	delete(sess.waiters, st.JobID)
	sess.mu.Unlock()
	if s.claimSettled(sess.node, st.JobID) {
		defer s.dispatchNext(sess.node) // it waits for the host's own run too, if one is left
	}
	if ch == nil && s.settledAlready(sess.node, st) {
		// The agent sent the result again: the ack to it was lost, or it came from a journal before ADR-0114.
		s.Events.Log(sess.node, "job_status_again", map[string]any{"job_id": st.JobID, "code": st.Code})
		s.acknowledge(sess, st.JobID)
		return
	}
	s.Events.Log(sess.node, "job_status", fields)
	// Stored before the ack: the agent forgets the result once it has the ack. Not stored, no ack - the agent sends
	// it again on its next session.
	stored := true
	if st.InstallStarted != nil {
		// The run wrote the agent's journal: the agent's minimum interval counts from its start (ADR-0110).
		if err := s.Reg.SetLastInstall(sess.node, *st.InstallStarted); err != nil {
			s.Log.Printf("register: %v", err)
			stored = false
		}
	}
	settled := false
	if run, ok := s.currentRun(sess.node, st.JobID); ok {
		finished := s.Now()
		run.Phase, run.Code, run.Reason, run.Detail, run.Finished = "", st.Code, st.Reason, st.Detail, &finished
		run.Summary = st.Summary
		run.Progress, run.Item = nil, ""
		if err := s.Reg.SetRun(sess.node, run); err != nil {
			s.Log.Printf("register: %v", err)
			stored = false
		}
		s.changed(sess.node)
		settled = true
	}
	if stored {
		s.acknowledge(sess, st.JobID)
	}
	if ch != nil {
		ch <- st // buffered for exactly this one final status; WaitJob records it with what it knows of the job
		return
	}
	s.mu.Lock()
	succ := s.successors[st.JobID]
	s.mu.Unlock()
	if succ != nil {
		select {
		case succ <- st:
		default: // buffered for one final status; a second one changes nothing
		}
		return // the WaitJob that waits for the successor hands on to the queue when it returns
	}
	if settled {
		// Nobody waits for this run any more - the hub restarted during it, or the tunnel broke and the run went on.
		// Commands queued behind it would otherwise wait until the next command for the host.
		s.dispatchNext(sess.node)
	}
}

// progress records the phase, percent and package of the host's current run.
func (s *Server) progress(node string, st wire.JobStatus) {
	run, ok := s.currentRun(node, st.JobID)
	if !ok || st.Phase == "" {
		return
	}
	if run.Code == protocol.CodeInterrupted {
		run.Code, run.Finished = "", nil // the tunnel broke, the run did not: the agent reports on
	}
	if run.Phase == st.Phase && run.Item == st.Item && equalPct(run.Progress, st.Progress) {
		return
	}
	run.Phase, run.Progress, run.Item = st.Phase, st.Progress, st.Item
	s.storeRun(node, run)
}

// currentRun is the host's last run if the status belongs to it and it is not settled yet (running, or counted as
// interrupted because the tunnel broke).
func (s *Server) currentRun(node, jobID string) (Run, bool) {
	e, ok := s.Reg.Get(node)
	if !ok || e.LastRun == nil || e.LastRun.JobID != jobID {
		return Run{}, false
	}
	if c := e.LastRun.Code; c != "" && c != protocol.CodeInterrupted {
		return Run{}, false
	}
	return *e.LastRun, true
}

func equalPct(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

// Notify sends the current registration state to a connected host, e.g. right after approval. A rejected host's
// connection is closed.
func (s *Server) Notify(node string) {
	e, ok := s.Reg.Get(node)
	s.mu.Lock()
	sess := s.sessions[node]
	s.mu.Unlock()
	if sess == nil {
		return
	}
	if !ok {
		sess.conn.Close() // forgotten
		return
	}
	reason := wire.ReasonNone
	if e.State == wire.StateRejected {
		reason = wire.ReasonRejected
	}
	// Every status carries the features: an agent takes them from the last one it got.
	_ = sess.conn.Send(wire.Status{T: wire.TStatus, State: e.State, Reason: reason, Fingerprint: e.Fingerprint,
		Features: s.features()})
	if e.State == wire.StateRejected {
		sess.conn.Close()
	}
}

// Online reports whether a host is connected and when it last sent something.
func (s *Server) Online(node string) (bool, time.Time) {
	s.mu.Lock()
	sess := s.sessions[node]
	s.mu.Unlock()
	if sess == nil {
		return false, time.Time{}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return true, sess.lastSeen
}

// DisconnectedSince: when a host that is not connected was last connected. Hosts not seen since the hub started
// count from the hub's start, so a hub restart does not report every host offline at once.
func (s *Server) DisconnectedSince(node string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.lastSeen[node]; ok {
		return t
	}
	return s.started
}

// SendJob signs a job for an approved, connected host, sends it and waits for its final status.
func (s *Server) SendJob(ctx context.Context, node string, verb protocol.Verb, scope string) (wire.JobStatus, AdminCode) {
	job, code := s.StartJob(node, verb, scope, ByAdmin)
	if code != AdminOK {
		return wire.JobStatus{}, code
	}
	return s.WaitJob(ctx, job)
}

// SentJob is a job on its way: StartJob sent it, WaitJob collects its result. An install that waits for a fresh
// backup of its guest (prebackup.go) is not sent yet: WaitJob makes the backup, then sends it.
type SentJob struct {
	ID      string
	node    string
	sess    *session
	run     Run
	ch      chan wire.JobStatus
	pending *pendingInstall
}

// StartJob signs a job for an approved, connected host and sends it. The hub checks what it can know before it
// asks (connected, approved, verb announced by the agent); the agent checks again. Every started job needs a
// WaitJob, which records its outcome.
func (s *Server) StartJob(node string, verb protocol.Verb, scope, by string) (*SentJob, AdminCode) {
	return s.startJob(node, protocol.Job{ID: newJobID(), Verb: verb, Scope: scope}, by)
}

// Who started a job, kept with its run and in job_sent: a Home Assistant client ("ha:<name>"), the admin socket, or
// the hub's own agent rollout.
const (
	ByAdmin   = "admin"
	ByRollout = "rollout"
)

// byHAPrefix starts every ByHA value.
const byHAPrefix = "ha:"

// ByHA names a Home Assistant client as the one who started a job.
func ByHA(client string) string { return byHAPrefix + client }

// startJob completes job (node, times) and sends it; job carries the verb and its parameters, by who asked for it.
// An install of a guest that gets a fresh backup first (ADR-0096) starts its run in phase backing_up instead; WaitJob
// sends it once the backup is there.
func (s *Server) startJob(node string, job protocol.Job, by string) (*SentJob, AdminCode) {
	if _, code := s.sendable(node, job.Verb); code != AdminOK {
		return nil, code
	}
	if s.windowRefuses(node, job) {
		// The agent would refuse it outside its window; said before a guest is backed up for it (ADR-0109).
		return nil, AdminOutsideWindow
	}
	if s.intervalRefuses(node, job) {
		// The same for the agent's minimum interval since its last run that changed packages (ADR-0110).
		return nil, AdminRateLimited
	}
	if job.Verb == protocol.VerbInstall && s.Backups != nil {
		if group := s.Backups.FreshFor(node); group != "" {
			return s.awaitBackup(node, job, by, group), AdminOK
		}
	}
	return s.sendJob(node, job, by, time.Time{})
}

// sendable: what the hub can know before it signs a job - the host is known, approved, announced the verb and is
// connected now. Returns its session.
func (s *Server) sendable(node string, verb protocol.Verb) (*session, AdminCode) {
	e, ok := s.Reg.Get(node)
	switch {
	case !ok:
		return nil, AdminUnknownNode
	case e.State != wire.StateApproved:
		return nil, AdminNotApproved
	case !slices.Contains(e.Verbs, verb):
		return nil, AdminVerbUnsupported
	}
	s.mu.Lock()
	sess := s.sessions[node]
	s.mu.Unlock()
	if sess == nil {
		return nil, AdminNotConnected
	}
	return sess, AdminOK
}

// signAndSend signs job for node and sends it over sess; the returned channel gets its final status.
func (s *Server) signAndSend(sess *session, node string, job protocol.Job) (protocol.Job, chan wire.JobStatus, AdminCode) {
	now := s.Now()
	job.Node, job.IssuedAt, job.ExpiresAt = node, now, now.Add(JobLifetime)
	sj, err := protocol.SignJob(job, s.Key.Sign)
	if err != nil {
		return job, nil, AdminSendFailed
	}
	ch := make(chan wire.JobStatus, 1)
	sess.mu.Lock()
	sess.waiters[job.ID] = ch
	sess.mu.Unlock()
	if err := sess.conn.Send(wire.JobMsg{T: wire.TJob, Job: sj}); err != nil {
		sess.mu.Lock()
		delete(sess.waiters, job.ID)
		sess.mu.Unlock()
		return job, nil, AdminSendFailed
	}
	return job, ch, AdminOK
}

// sendJob sends job and records it as the host's run. started: when the run began, if before now (an install that
// waited for its backup keeps the moment it was asked for); zero means now.
func (s *Server) sendJob(node string, job protocol.Job, by string, started time.Time) (*SentJob, AdminCode) {
	sess, code := s.sendable(node, job.Verb)
	if code != AdminOK {
		return nil, code
	}
	job, ch, code := s.signAndSend(sess, node, job)
	if code != AdminOK {
		return nil, code
	}
	fields := map[string]any{"job_id": job.ID, "verb": job.Verb, "scope": job.Scope, "by": by}
	if job.OutsideWindow != "" {
		fields["outside_window"] = job.OutsideWindow
	}
	if len(job.SecurityFix) > 0 {
		fields["security_fix"] = job.SecurityFix // the shorter minimum age for these (ADR-0111)
	}
	s.Events.Log(node, "job_sent", fields)
	if started.IsZero() {
		started = job.IssuedAt
	}
	run := Run{JobID: job.ID, Verb: job.Verb, Scope: job.Scope, By: by, OutsideWindow: job.OutsideWindow,
		Phase: protocol.PhaseQueued, Started: started}
	s.storeRun(node, run)
	return &SentJob{ID: job.ID, node: node, sess: sess, run: run, ch: ch}, AdminOK
}

// WaitJob waits for the final status of a started job and records it as the host's last run. When it returns, the
// host's next waiting command (queue.go) gets its turn.
func (s *Server) WaitJob(ctx context.Context, j *SentJob) (wire.JobStatus, AdminCode) {
	defer s.dispatchNext(j.node)
	// A stopping hub ends the wait and records nothing: the run stays as it is (ADR-0142).
	ctx, stop := s.untilClose(ctx)
	defer stop()
	if j.pending != nil {
		// The install waits for its guest's fresh backup (prebackup.go); without one it ends here, never sent.
		if st, ok := s.backupFirst(ctx, j); !ok {
			if s.stopping() {
				return wire.JobStatus{JobID: j.ID}, AdminHubStopping
			}
			s.finishRun(j, st)
			return st, AdminOK
		}
	}
	var st wire.JobStatus
	code := AdminOK
	select {
	case got, open := <-j.ch:
		st = got
		if !open && s.stopping() {
			return wire.JobStatus{JobID: j.ID}, AdminHubStopping // Close ended the session, not the agent
		}
		if !open {
			if done, ok := s.settledRun(j.node, j.ID); ok {
				// Its result came over the agent's new session already (ADR-0114); never overwrite it.
				return done, AdminOK
			}
		}
		if !open && s.restarting(j.node, j.ID) {
			return s.awaitSuccessor(ctx, j, "job_awaits_restart")
		}
		if !open && s.claimedOpen(j.node, j.ID) {
			// The agent's new session named the job before the old one ended here (ADR-0114).
			return s.awaitSuccessor(ctx, j, "job_continues")
		}
		if !open {
			// The connection ended before the result: from the hub's view the run was interrupted.
			st, code = wire.JobStatus{JobID: j.ID, Code: protocol.CodeInterrupted}, AdminNotConnected
		}
	case <-ctx.Done():
		j.sess.mu.Lock()
		delete(j.sess.waiters, j.ID)
		j.sess.mu.Unlock()
		if s.stopping() {
			return wire.JobStatus{JobID: j.ID}, AdminHubStopping
		}
		st, code = wire.JobStatus{JobID: j.ID, Code: protocol.CodeTimeout, Detail: "no result from the agent in time"}, AdminTimeout
	}
	s.finishRun(j, st)
	return st, code
}

// restarting: the host's run is still this job, and the agent's last word was that it restarts itself.
func (s *Server) restarting(node, jobID string) bool {
	run, ok := s.currentRun(node, jobID)
	return ok && run.Code == "" && run.Phase == protocol.PhaseRebooting
}

// awaitSuccessor: the end of the job's connection is expected, and the run goes on. Either the agent reported that it
// restarts itself (agent_update swaps its binary and exits; event job_awaits_restart): the agent that starts next
// reports the result - the new one confirms, or the old one, put back, reports the failed update. Or the agent's
// next session named the job as running or reported (job_continues, ADR-0114). deliver records the result. Only if
// none comes within the job's wait the run counts as timed out.
func (s *Server) awaitSuccessor(ctx context.Context, j *SentJob, event string) (wire.JobStatus, AdminCode) {
	ch := make(chan wire.JobStatus, 1)
	s.mu.Lock()
	s.successors[j.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.successors, j.ID)
		s.mu.Unlock()
	}()
	s.Events.Log(j.node, event, map[string]any{"job_id": j.ID})
	// The result came before this wait listened.
	if e, ok := s.Reg.Get(j.node); ok && e.LastRun != nil && e.LastRun.JobID == j.ID && e.LastRun.Code != "" {
		r := e.LastRun
		return wire.JobStatus{JobID: j.ID, Code: r.Code, Reason: r.Reason, Detail: r.Detail, Summary: r.Summary}, AdminOK
	}
	select {
	case st := <-ch:
		return st, AdminOK
	case <-ctx.Done():
		if s.stopping() {
			return wire.JobStatus{JobID: j.ID}, AdminHubStopping // the run stays open (ADR-0142)
		}
		st := wire.JobStatus{JobID: j.ID, Code: protocol.CodeTimeout,
			Detail: "the agent announced its restart and no agent reported back in time"}
		s.finishRun(j, st)
		return st, AdminTimeout
	}
}

// finishRun records a job's final status as the host's last run.
func (s *Server) finishRun(j *SentJob, st wire.JobStatus) {
	finished := s.Now()
	run := j.run
	run.Phase, run.Code, run.Reason, run.Detail, run.Finished = "", st.Code, st.Reason, st.Detail, &finished
	run.Summary = st.Summary
	s.storeRun(j.node, run)
}

func (s *Server) storeRun(node string, run Run) {
	if err := s.Reg.SetRun(node, run); err != nil {
		s.Log.Printf("register: %v", err)
	}
	s.changed(node)
}

func newJobID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
