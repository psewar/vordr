package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The Home Assistant endpoint /v1/ha (design/ha-integration.md, section 5). A client proves its key like an agent
// and is admitted only by the operator at the hub. Until then it gets its status and nothing else. Once approved it
// gets a snapshot, then every change, and may ask for commands, which take the same way as MQTT's: Resolve, a job
// signed by the hub, the agent's own checks.

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// maxCommandLen bounds the command string; the longest valid one is install_app:<id>.
const maxCommandLen = 80

// A command with a reason to run outside the window (ADR-0087): AdminBadReason, the reason is blank, too long or
// holds control characters; AdminOutsideWindowUnsupported, the host's agent does not take such jobs yet (older than
// the ADR, it would refuse the job as invalid).
const (
	AdminBadReason                AdminCode = "bad_reason"
	AdminOutsideWindowUnsupported AdminCode = "outside_window_unsupported"
)

// haPushInterval: changes that happen without an event (offline grace, stale age) reach a client within this time.
const haPushInterval = 30 * time.Second

type haSession struct {
	name        string
	fingerprint string
	conn        *wire.Conn
	dirty       chan struct{}
	done        chan struct{}

	mu       sync.Mutex // held while sending views, so that they leave in order
	approved bool
	synced   bool              // the snapshot went out; from then on only changes
	hosts    map[string]string // node -> HostView last sent, as JSON
	hub      string            // HubView last sent, as JSON
}

func (hs *haSession) markDirty() {
	select {
	case hs.dirty <- struct{}{}:
	default:
	}
}

func (s *Server) serveHA(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has answered the request already
	}
	conn := wire.NewConn(ws)
	defer conn.Close()

	sess, ok := s.loginHA(conn, r.RemoteAddr)
	if !ok {
		return
	}
	defer s.dropHA(sess)
	go s.pushHA(sess)
	s.loopHA(sess)
}

// loginHA runs challenge and hello. Pending and approved clients stay connected; only approved ones see anything.
func (s *Server) loginHA(conn *wire.Conn, remote string) (*haSession, bool) {
	nonce := make([]byte, wire.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, false
	}
	if conn.Send(wire.Challenge{T: wire.TChallenge, Nonce: nonce}) != nil {
		return nil, false
	}
	t, raw, err := conn.Receive(wire.HelloTimeout)
	if err != nil {
		return nil, false
	}
	var h wire.HAHello
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	refuse := func(reason wire.StatusReason) (*haSession, bool) {
		s.Events.Log("", "ha_hello_refused", map[string]any{"reason": reason, "remote": remote})
		_ = conn.Send(wire.Status{T: wire.TStatus, State: wire.StateRejected, Reason: reason})
		return nil, false
	}
	if t != wire.THello || dec.Decode(&h) != nil || h.Role != wire.RoleHA || !nodePattern.MatchString(h.Name) ||
		len(h.PublicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(h.PublicKey, wire.HAHelloSigned(nonce, h.Name, h.PublicKey), h.Signature) {
		return refuse(wire.ReasonBadHello)
	}
	if h.Protocol != wire.HAProtocol {
		// Before the register: a client that cannot speak this protocol is not one to approve.
		return refuse(wire.ReasonProtocolMismatch)
	}
	fp := identity.Fingerprint(h.PublicKey)
	state, reason, err := s.Clients.Admit(h, fp, s.Now())
	if err != nil {
		s.Log.Printf("client register: %v", err)
		return nil, false
	}
	s.Events.Log("", "ha_hello", map[string]any{"client": h.Name, "state": state, "reason": reason, "fingerprint": fp,
		"integration_version": h.IntegrationVersion, "remote": remote})
	if conn.Send(wire.Status{T: wire.TStatus, State: state, Reason: reason, Fingerprint: fp}) != nil ||
		state == wire.StateRejected {
		return nil, false
	}
	sess := &haSession{name: h.Name, fingerprint: fp, conn: conn, dirty: make(chan struct{}, 1), done: make(chan struct{}),
		approved: state == wire.StateApproved, hosts: map[string]string{}}
	s.mu.Lock()
	if old := s.haSessions[h.Name]; old != nil {
		old.conn.Close() // the client reconnected; the old connection is dead or stale
	}
	s.haSessions[h.Name] = sess
	s.mu.Unlock()
	sess.markDirty()
	return sess, true
}

func (s *Server) dropHA(sess *haSession) {
	close(sess.done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.haSessions[sess.name] == sess {
		delete(s.haSessions, sess.name)
		s.Events.Log("", "ha_disconnected", map[string]any{"client": sess.name})
	}
}

// loopHA reads until the client goes quiet for IdleTimeout or sends something it must not.
func (s *Server) loopHA(sess *haSession) {
	for {
		t, raw, err := sess.conn.Receive(wire.IdleTimeout)
		if err != nil {
			return
		}
		switch t {
		case wire.THeartbeat:
			if sess.conn.Send(wire.Heartbeat{T: wire.THeartbeat}) != nil {
				return
			}
		case wire.TCommand:
			var c wire.Command
			if json.Unmarshal(raw, &c) != nil || !requestIDPattern.MatchString(c.RequestID) ||
				!nodePattern.MatchString(c.Node) || len(c.Command) > maxCommandLen {
				s.Events.Log("", "ha_protocol_violation", map[string]any{"client": sess.name, "type": t})
				return
			}
			if sess.conn.Send(s.haCommand(sess, c)) != nil {
				return
			}
		default:
			s.Events.Log("", "ha_protocol_violation", map[string]any{"client": sess.name, "type": t})
			return
		}
	}
}

// haCommand answers a command at once: ok means the job is signed and sent, waits for the running run, or is that
// run already (job_id then is the running one); its result follows in the host's view.
func (s *Server) haCommand(sess *haSession, c wire.Command) wire.CommandResult {
	res := wire.CommandResult{T: wire.TCommandResult, RequestID: c.RequestID}
	fields := map[string]any{"client": sess.name, "command": c.Command, "request_id": c.RequestID}
	refused := func(code AdminCode) wire.CommandResult {
		fields["code"] = code
		s.Events.Log(c.Node, "command_refused", fields)
		res.Code = string(code)
		return res
	}
	// Decided on the register as it is now, not as it was at login.
	client, ok := s.Clients.Get(sess.name)
	if !ok || client.State != wire.StateApproved || client.Fingerprint != sess.fingerprint {
		return refused(AdminClientNotApproved)
	}
	verb, scope, code := s.Resolve(c.Node, c.Command)
	if code != AdminOK {
		return refused(code)
	}
	if !RoleAllows(client.Role, verb) {
		fields["role"] = EffectiveRole(client.Role)
		return refused(AdminRoleForbids)
	}
	if c.Reason != "" {
		e, _ := s.Reg.Get(c.Node) // Resolve found it
		switch {
		case verb != protocol.VerbInstall && verb != protocol.VerbReboot:
			return refused(AdminBadRequest) // check and agent updates have no window
		case !protocol.ValidReason(c.Reason):
			return refused(AdminBadReason)
		case !e.Policy.OutsideWindow:
			return refused(AdminOutsideWindowUnsupported)
		}
	}
	// The install button installs at once (ADR-0109): outside the window the install carries ButtonReason.
	reason := s.haReason(c.Node, verb, c.Reason)
	if reason != "" {
		fields["outside_window"] = reason
	}
	if run, ok := s.runningRun(c.Node); ok && run.Verb == verb && run.Scope == scope {
		// The same command as the running run (a second tap, a second person): it is being done. Queued, it would run
		// again afterwards for nothing or be refused (a second reboot, rejected_rate_limited).
		fields["job_id"] = run.JobID
		s.Events.Log(c.Node, "command_already_running", fields)
		res.Code, res.JobID = string(AdminOK), run.JobID
		return res
	}
	if s.windowRefuses(c.Node, protocol.Job{Verb: verb, OutsideWindow: reason}) {
		// Closed window, and the agent takes no reason (older than ADR-0087): it would refuse the install. Said here,
		// before the backup check and before a guest is backed up for nothing.
		return refused(AdminOutsideWindow)
	}
	if s.intervalRefuses(c.Node, protocol.Job{Verb: verb, Scope: scope}) {
		// The agent's minimum interval has not passed (ADR-0110): the host view says when it has.
		return refused(AdminRateLimited)
	}
	if verb == protocol.VerbInstall && s.Backups != nil {
		// An install changes the host: only with a state to go back to. Asked of PBS now, not from a schedule.
		ctx, cancel := context.WithTimeout(context.Background(), backupFetchTimeout)
		code, b := s.backupGate(ctx, c.Node)
		cancel()
		fields["backup"] = b.State
		if code != AdminOK {
			s.changedHA() // the host's backup view may have moved with this fresh look
			return refused(code)
		}
	}
	by := ByHA(sess.name)
	start := func() (*SentJob, AdminCode) {
		if verb == protocol.VerbAgentUpdate {
			// The button: this host now, whatever its ring - with the current release, as in the rollout.
			return s.StartAgentUpdate(c.Node, by)
		}
		return s.startJob(c.Node, s.haJob(c.Node, verb, scope, c.Reason), by)
	}
	if s.running(c.Node) {
		// Another run on this host is still going: the command waits for it (queue.go). The backup check above holds
		// for it; the wait is minutes. The queue keeps only the person's own reason, so that one given later still
		// replaces the button's.
		q := queuedJob{verb: verb, scope: scope, by: by, outsideWindow: c.Reason, start: start}
		if code := s.enqueue(c.Node, q); code != AdminOK {
			return refused(code)
		}
		fields["queued"] = s.Queued(c.Node)
		s.Events.Log(c.Node, "command_queued", fields)
		s.changedHA()
		res.Code = string(AdminOK)
		return res
	}
	job, code := start()
	if code != AdminOK {
		return refused(code)
	}
	fields["job_id"] = job.ID
	s.Events.Log(c.Node, "command", fields)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout(verb))
		defer cancel()
		s.WaitJob(ctx, job)
	}()
	res.Code, res.JobID = string(AdminOK), job.ID
	return res
}

// haJob is the job a Home Assistant command becomes. It is decided when the job goes out, since a command that waited
// in the queue may start later: the reason outside the window (ADR-0109), and the fixes the agent may take at its
// shorter minimum age (ADR-0111).
func (s *Server) haJob(node string, verb protocol.Verb, scope, given string) protocol.Job {
	job := protocol.Job{ID: newJobID(), Verb: verb, Scope: scope, OutsideWindow: s.haReason(node, verb, given)}
	if verb == protocol.VerbInstall && s.Packages != nil {
		job.SecurityFix = s.Packages.SecurityFixes(node, scope)
	}
	return job
}

// pushHA sends views to the client on every change and every haPushInterval.
func (s *Server) pushHA(sess *haSession) {
	t := time.NewTicker(haPushInterval)
	defer t.Stop()
	for {
		select {
		case <-sess.done:
			return
		case <-sess.dirty:
		case <-t.C:
		}
		if s.syncHA(sess) != nil {
			sess.conn.Close() // the read loop ends and drops the session
			return
		}
	}
}

// syncHA sends the snapshot once after approval, afterwards what changed since.
func (s *Server) syncHA(sess *haSession) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.approved {
		return nil
	}
	now := s.Now()
	entries := s.Reg.List()
	hub := s.HubView(entries)
	views := map[string]wire.HostView{}
	var nodes []string
	for _, e := range entries {
		if e.State == wire.StateApproved {
			views[e.Node] = s.HostView(e, now)
			nodes = append(nodes, e.Node)
		}
	}
	if !sess.synced {
		snap := wire.Snapshot{T: wire.TSnapshot, Hub: hub, Hosts: []wire.HostView{}}
		for _, n := range nodes {
			snap.Hosts = append(snap.Hosts, views[n])
		}
		if err := sess.conn.Send(snap); err != nil {
			return err
		}
		sess.hosts = map[string]string{}
		for n, v := range views {
			sess.hosts[n] = mustJSON(v)
		}
		sess.hub, sess.synced = mustJSON(hub), true
		return nil
	}
	for _, n := range nodes {
		j := mustJSON(views[n])
		if sess.hosts[n] == j {
			continue
		}
		if err := sess.conn.Send(wire.HostMsg{T: wire.THost, Host: views[n]}); err != nil {
			return err
		}
		sess.hosts[n] = j
	}
	var gone []string
	for n := range sess.hosts {
		if _, ok := views[n]; !ok {
			gone = append(gone, n)
		}
	}
	sort.Strings(gone)
	for _, n := range gone {
		if err := sess.conn.Send(wire.HostRemoved{T: wire.THostRemoved, Node: n}); err != nil {
			return err
		}
		delete(sess.hosts, n)
	}
	if j := mustJSON(hub); j != sess.hub {
		if err := sess.conn.Send(wire.HubMsg{T: wire.THub, Hub: hub}); err != nil {
			return err
		}
		sess.hub = j
	}
	return nil
}

// notifyClient tells a connected client about a decision: approval starts the snapshot, rejection and forgetting
// end the connection.
func (s *Server) notifyClient(name string) {
	s.mu.Lock()
	sess := s.haSessions[name]
	s.mu.Unlock()
	if sess == nil {
		return
	}
	c, ok := s.Clients.Get(name)
	switch {
	case !ok || c.Fingerprint != sess.fingerprint:
		sess.conn.Close() // forgotten
	case c.State == wire.StateRejected:
		_ = sess.conn.Send(wire.Status{T: wire.TStatus, State: c.State, Reason: wire.ReasonRejected, Fingerprint: c.Fingerprint})
		sess.conn.Close()
	case c.State == wire.StateApproved:
		sess.mu.Lock()
		// Status before the snapshot: sent under sess.mu, which syncHA needs as well.
		err := sess.conn.Send(wire.Status{T: wire.TStatus, State: c.State, Fingerprint: c.Fingerprint})
		sess.approved, sess.synced = err == nil, false
		sess.mu.Unlock()
		sess.markDirty()
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only the wire types of this module are marshalled
	}
	return string(b)
}

// changedHA marks every client's views as outdated.
func (s *Server) changedHA() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, hs := range s.haSessions {
		hs.markDirty()
	}
}
