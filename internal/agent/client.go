package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// Installer carries out install jobs on this host. A client without one cannot install and does not announce it.
type Installer interface {
	Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
		progress func(source.Progress)) source.InstallResult
	RebootRequired(ctx context.Context) bool
}

// progressGap: at most one progress message per gap within a phase; a new phase is sent at once.
const progressGap = time.Second

const (
	DefaultCheckInterval    = 6 * time.Hour
	DefaultCheckMinInterval = 5 * time.Minute // design 6.1: check at most every 5 minutes
	backoffMin              = time.Second
	backoffMax              = 30 * time.Second
	// refusedRetry: after a refusal (rejected, key mismatch, hub full) the agent asks again rarely; the operator
	// decides at the hub, hammering it changes nothing.
	refusedRetry = 10 * time.Minute
)

type Client struct {
	Cfg     policy.Config
	ID      *identity.Identity
	Version string
	Facts   facts.Host
	Checker Checker
	// Installer: nil when this host cannot install (no apt, not root); then install is not announced.
	Installer Installer
	// Updater: nil when this agent cannot replace itself (NewSelfUpdate says why); then agent_update is not announced.
	Updater *SelfUpdate
	// Health: the checks of agent.yaml health, run after an install run that changed packages; nil when none.
	Health *Health
	// Containers: starts containers again that ran before an install run and do not after it (a Docker upgrade
	// restarts the daemon); nil on a host without Docker.
	Containers *Containers
	// Rebooter: nil when this agent may not reboot its host (NewRebooter); then reboot is not announced.
	Rebooter *Rebooter
	// Inventory reads the packages of pip, pipx and npm; sent after each check to a hub that accepts it. nil: off
	// (packages.inventory: false). containers: the hub keeps container images (wire.FeatureContainers, ADR-0113).
	Inventory func(ctx context.Context, w Wants) wire.Inventory
	// Packages: pip/npm updates by button (packages.update); nil when agent.yaml allows none.
	Packages *PackageUpdates
	// Images: Compose services moved to the rebuild of their tag by button (image:<project>/<service> in
	// install.allowed_scopes, ADR-0119); nil when agent.yaml allows none.
	Images ImageUpdater
	// Backups: backups of this hypervisor's guests before they install (guest_backup, ADR-0096); nil elsewhere, then
	// backup is not announced.
	Backups *GuestBackups
	Log     *log.Logger
	Now     func() time.Time

	CheckInterval time.Duration

	guard       *protocol.ReplayGuard
	checkMu     sync.Mutex // one apt operation at a time: a check or an install
	stateMu     sync.Mutex
	lastCheck   time.Time
	minGap      time.Duration
	lastInstall time.Time       // start of the last install run that changed packages (journal)
	send        func(any) error // the current approved session, for runs that outlive a session
	rootCtx     context.Context
	runs        sync.WaitGroup // install runs in progress; WaitRuns waits for them
	journalMu   sync.Mutex
	update      *updateMarker             // a self-update to confirm or report (settleUpdate)
	confirmed   chan struct{}             // closed when this agent, freshly installed, reached an approved session
	features    []string                  // what the hub of the current session announced (wire.Feature*)
	running     map[string]bool           // jobs being carried out now, named in the hello (ADR-0114)
	rebooting   bool                      // this agent asked for the host's reboot; it reports the reboot after the boot
	unsent      map[string]wire.JobStatus // final statuses without a journal that no session took (answer)
}

// setFeatures keeps what the hub announced in its status; a hub that announces nothing gets no newer messages.
func (c *Client) setFeatures(f []string) {
	c.stateMu.Lock()
	c.features = f
	c.stateMu.Unlock()
}

// hubAccepts reports whether the current hub announced a feature.
func (c *Client) hubAccepts(feature string) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return slices.Contains(c.features, feature)
}

func NewClient(cfg policy.Config, id *identity.Identity, version string, host facts.Host, checker Checker, logger *log.Logger) *Client {
	return &Client{Cfg: cfg, ID: id, Version: version, Facts: host, Checker: checker, Log: logger, Now: time.Now,
		CheckInterval: DefaultCheckInterval, guard: protocol.NewReplayGuard(), minGap: DefaultCheckMinInterval,
		confirmed: make(chan struct{}), running: map[string]bool{}}
}

// SetCheckMinInterval changes the minimum gap between two checks (tests).
func (c *Client) SetCheckMinInterval(d time.Duration) {
	c.stateMu.Lock()
	c.minGap = d
	c.stateMu.Unlock()
}

// Run keeps the tunnel up until ctx ends. An install run still in progress then is not stopped: WaitRuns waits for it.
func (c *Client) Run(ctx context.Context) {
	c.stateMu.Lock()
	c.rootCtx = ctx
	c.stateMu.Unlock()
	c.settleJournal()
	c.settleUpdate()
	backoff := backoffMin
	for ctx.Err() == nil {
		end := c.session(ctx)
		wait := backoff
		switch {
		case end.refused != wire.ReasonNone:
			c.Log.Printf("hub refused this agent: %s - asking again in %s", end.refused, refusedRetry)
			wait = refusedRetry
		case end.established:
			backoff, wait = backoffMin, backoffMin
		default:
			backoff = min(backoff*2, backoffMax)
		}
		if end.err != nil && ctx.Err() == nil {
			c.Log.Printf("tunnel: %v - reconnecting in %s", end.err, wait)
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

type sessionEnd struct {
	established bool              // the hub accepted the hello (pending or approved)
	refused     wire.StatusReason // the hub refused the hello
	err         error
}

func (c *Client) session(ctx context.Context) sessionEnd {
	tlsCfg, err := TLSConfig(c.Cfg.Hub.CAFile)
	if err != nil {
		return sessionEnd{err: err}
	}
	dialer := websocket.Dialer{TLSClientConfig: tlsCfg, HandshakeTimeout: 15 * time.Second}
	ws, _, err := dialer.DialContext(ctx, c.Cfg.Hub.URL, nil)
	if err != nil {
		return sessionEnd{err: err}
	}
	conn := wire.NewConn(ws)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-sctx.Done(); conn.Close() }()

	status, err := c.login(conn)
	if err != nil {
		return sessionEnd{err: err}
	}
	if status.State == wire.StateRejected {
		return sessionEnd{refused: status.Reason}
	}
	c.Log.Printf("hub: %s (fingerprint %s)", status.State, status.Fingerprint)
	c.setFeatures(status.Features)
	approved := status.State == wire.StateApproved
	defer c.setSender(nil)

	go c.heartbeats(sctx, conn)
	if approved {
		c.approvedSession(conn.Send)
		go c.checkAndReport(sctx, conn)
	}
	ticker := time.NewTicker(c.CheckInterval)
	defer ticker.Stop()
	msgs := make(chan received)
	go receive(sctx, conn, msgs)
	for {
		select {
		case <-ctx.Done():
			return sessionEnd{established: true}
		case <-ticker.C:
			if approved {
				go c.checkAndReport(sctx, conn)
			}
		case m := <-msgs:
			if m.err != nil {
				return sessionEnd{established: true, err: m.err}
			}
			switch m.t {
			case wire.THeartbeat:
			case wire.TStatus:
				var st wire.Status
				if json.Unmarshal(m.raw, &st) != nil {
					return sessionEnd{established: true, err: errors.New("bad status message")}
				}
				c.Log.Printf("hub: %s", st.State)
				c.setFeatures(st.Features)
				if st.State == wire.StateRejected {
					return sessionEnd{refused: st.Reason}
				}
				if st.State == wire.StateApproved && !approved {
					approved = true
					c.approvedSession(conn.Send)
					go c.checkAndReport(sctx, conn)
				}
			case wire.TJob:
				var jm wire.JobMsg
				if json.Unmarshal(m.raw, &jm) != nil {
					return sessionEnd{established: true, err: errors.New("bad job message")}
				}
				c.runs.Add(1) // here, before Run can return and WaitRuns start
				go func() {
					defer c.runs.Done()
					c.handleJob(sctx, jm.Job, conn.Send)
				}()
			case wire.TAck:
				var a wire.Ack
				if json.Unmarshal(m.raw, &a) != nil || a.JobID == "" {
					return sessionEnd{established: true, err: errors.New("bad ack")}
				}
				c.acknowledged(a.JobID)
			case wire.TVetResult:
				var vr wire.VetResult
				if json.Unmarshal(m.raw, &vr) != nil {
					return sessionEnd{established: true, err: errors.New("bad vet result")}
				}
				if c.Packages != nil {
					c.Packages.deliver(vr) // the waiting run checks signature, job and node itself
				}
			default:
				return sessionEnd{established: true, err: errors.New("unexpected message " + m.t)}
			}
		}
	}
}

type received struct {
	t   string
	raw []byte
	err error
}

// receive forwards messages until the connection fails. The hub answers every heartbeat, so silence for
// IdleTimeout means the connection is dead.
func receive(ctx context.Context, conn *wire.Conn, out chan<- received) {
	for {
		t, raw, err := conn.Receive(wire.IdleTimeout)
		select {
		case out <- received{t, raw, err}:
		case <-ctx.Done():
			return // the session ended; nobody reads any more
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) login(conn *wire.Conn) (wire.Status, error) {
	t, raw, err := conn.Receive(wire.HelloTimeout)
	if err != nil {
		return wire.Status{}, err
	}
	var ch wire.Challenge
	if t != wire.TChallenge || json.Unmarshal(raw, &ch) != nil || len(ch.Nonce) != wire.NonceSize {
		return wire.Status{}, errors.New("no valid challenge from the hub")
	}
	pub := c.ID.Public()
	hello := wire.Hello{T: wire.THello, Node: c.Cfg.Node, PublicKey: pub,
		Signature: c.ID.Sign(wire.HelloSigned(ch.Nonce, c.Cfg.Node, pub)), AgentVersion: c.Version,
		Facts: c.Facts, Verbs: c.Verbs(), Policy: Summary(c.Cfg), LastInstall: c.lastInstallAt()}
	if slices.Contains(ch.Features, wire.FeatureAck) {
		// Only to a hub that said so: an older one refuses a hello with a field it does not know.
		runs := c.runState()
		hello.Runs = &runs
	}
	if err := conn.Send(hello); err != nil {
		return wire.Status{}, err
	}
	t, raw, err = conn.Receive(wire.HelloTimeout)
	if err != nil {
		return wire.Status{}, err
	}
	var st wire.Status
	if t != wire.TStatus || json.Unmarshal(raw, &st) != nil {
		return wire.Status{}, errors.New("no status after hello")
	}
	return st, nil
}

func (c *Client) heartbeats(ctx context.Context, conn *wire.Conn) {
	t := time.NewTicker(wire.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if conn.Send(wire.Heartbeat{T: wire.THeartbeat}) != nil {
				return
			}
		}
	}
}

// messageSender is the tunnel connection, or a stand-in in tests.
type messageSender interface {
	Send(v any) error
}

// checkAndReport runs one check and sends the report. The result code says whether the check ran; what it found
// (including a failed source) is in the report. Rate-limited also after a reconnect right after a check: the hub
// keeps the last report.
func (c *Client) checkAndReport(ctx context.Context, out messageSender) protocol.Code {
	if !c.checkMu.TryLock() {
		return protocol.CodeRejectedBusy
	}
	defer c.checkMu.Unlock()
	c.stateMu.Lock()
	recent := !c.lastCheck.IsZero() && c.Now().Sub(c.lastCheck) < c.minGap
	c.stateMu.Unlock()
	if recent {
		return protocol.CodeRejectedRateLimited
	}
	c.checkLocked(ctx, out)
	return protocol.CodeOK
}

// checkLocked checks and reports; the caller holds checkMu.
func (c *Client) checkLocked(ctx context.Context, out messageSender) {
	rep := c.Checker.Check(ctx)
	c.stateMu.Lock()
	c.lastCheck = c.Now()
	c.stateMu.Unlock()
	if err := out.Send(rep); err != nil {
		c.Log.Printf("report not sent: %v", err)
		return
	}
	if c.Inventory != nil && c.hubAccepts(wire.FeatureInventory) {
		if err := out.Send(c.Inventory(ctx, Wants{Containers: c.hubAccepts(wire.FeatureContainers),
			ImageContents: c.hubAccepts(wire.FeatureImageContents)})); err != nil {
			c.Log.Printf("inventory not sent: %v", err)
		}
	}
}

// handleJob verifies a job against the pinned hub key and carries it out within the local policy.
func (c *Client) handleJob(ctx context.Context, sj protocol.SignedJob, send func(any) error) {
	j, reason := protocol.VerifyJob(sj, c.Cfg.HubKey(), c.Cfg.Node, c.Now(), c.guard)
	if reason != protocol.InvalidNone {
		c.Log.Printf("job rejected: %s", reason)
		_ = send(wire.JobStatus{T: wire.TJobStatus, JobID: protocol.UntrustedJobID(sj.Payload),
			Code: protocol.CodeRejectedInvalidJob, Reason: reason})
		return
	}
	// From here until its final status is sent the job is running, and a hello says so (ADR-0114).
	c.setRunning(j.ID, true)
	defer c.setRunning(j.ID, false)
	switch j.Verb {
	case protocol.VerbCheck:
		code := c.checkAndReport(ctx, sender(send))
		c.Log.Printf("job %s check: %s", j.ID, code)
		st := wire.JobStatus{T: wire.TJobStatus, JobID: j.ID, Code: code}
		c.answer(send, st) // over a newer session, or kept for the next one
	case protocol.VerbInstall:
		c.install(j, send)
	case protocol.VerbAgentUpdate:
		c.agentUpdate(j, send)
	case protocol.VerbReboot:
		c.rebootHost(j, send)
	case protocol.VerbBackup:
		c.backupGuest(j, send)
	default:
		c.Log.Printf("job %s %s: %s", j.ID, j.Verb, protocol.CodeRejectedUnsupported)
		_ = send(wire.JobStatus{T: wire.TJobStatus, JobID: j.ID, Code: protocol.CodeRejectedUnsupported})
	}
}

// Verbs this agent can carry out on this host; announced in the hello.
func (c *Client) Verbs() []protocol.Verb {
	verbs := []protocol.Verb{protocol.VerbCheck}
	if c.Installer != nil || c.Packages != nil || c.Images != nil {
		verbs = append(verbs, protocol.VerbInstall)
	}
	if c.Rebooter != nil {
		verbs = append(verbs, protocol.VerbReboot)
	}
	if c.Updater != nil {
		verbs = append(verbs, protocol.VerbAgentUpdate)
	}
	if c.Backups != nil {
		verbs = append(verbs, protocol.VerbBackup)
	}
	return verbs
}

// WaitRuns waits for install runs still in progress - after Run ended, before the process exits: dpkg is never
// left half way (the unit stops only the agent itself, KillMode=mixed).
func (c *Client) WaitRuns() { c.runs.Wait() }

// install carries out an install job within the local policy (design 6.1 and 10.2). Once it changes packages it no
// longer belongs to the session that brought it: a broken tunnel must not stop a package manager half way. Progress
// and result then go out over whatever session is current, and a result that found none goes out from the run
// journal later. A job refused before that is answered where it came from.
func (c *Client) install(j protocol.Job, send func(any) error) {
	code, detail, summary, journaled := c.installRun(j)
	c.Log.Printf("job %s install %s: %s", j.ID, j.Scope, code)
	if journaled {
		c.finishJournal(code, detail, summary)
		return
	}
	st := wire.JobStatus{T: wire.TJobStatus, JobID: j.ID, Code: code, Detail: detail}
	c.answer(send, st) // over a newer session, or kept for the next one
}

// windowClosed reports whether w keeps job j out now. A job with a reason (a person asked for it outside the window,
// ADR-0087) is let through, and its reason goes into the log.
func (c *Client) windowClosed(w *policy.Window, j protocol.Job) bool {
	if w == nil || w.Contains(c.Now().In(time.Local)) {
		return false
	}
	if j.OutsideWindow == "" {
		return true
	}
	c.Log.Printf("job %s %s outside the window, reason: %q", j.ID, j.Verb, j.OutsideWindow)
	return false
}

// installRun checks the policy, runs the installer and reports what it found afterwards. journaled: the run got as
// far as the journal, so its result goes through it.
func (c *Client) installRun(j protocol.Job) (code protocol.Code, detail string, summary *wire.RunSummary,
	journaled bool) {
	in := c.Cfg.Install
	scope, _ := protocol.ParseScope(j.Scope) // VerifyJob accepts install only with a valid scope
	if scope.Kind == protocol.ScopePkg || scope.Kind == protocol.ScopeDeps {
		return c.packageRun(j, scope) // pip/npm: its own policy (packages.update) and its own checks
	}
	if scope.Images() {
		return c.imageRun(j, scope) // container images: the policy's image entries and their own checks (ADR-0119)
	}
	switch {
	case c.Installer == nil:
		return protocol.CodeRejectedUnsupported, "", nil, false
	case len(in.AllowedScopes) == 0:
		return protocol.CodeRejectedNotAllowed, "", nil, false
	case !slices.Contains(in.AllowedScopes, j.Scope):
		return protocol.CodeRejectedScopeNotAllowed, "", nil, false
	case c.windowClosed(in.Window, j):
		return protocol.CodeRejectedOutsideWindow, "", nil, false
	}
	c.stateMu.Lock()
	last := c.lastInstall
	c.stateMu.Unlock()
	if !last.IsZero() && c.Now().Sub(last) < time.Duration(in.MinIntervalH)*time.Hour {
		return protocol.CodeRejectedRateLimited, "", nil, false
	}
	if !c.checkMu.TryLock() {
		return protocol.CodeRejectedBusy, "", nil, false
	}
	defer c.checkMu.Unlock()

	timeout := time.Duration(in.TimeoutMin) * time.Minute
	root := c.root()
	ctx, cancel := context.WithTimeout(root, timeout)
	defer cancel()
	var running []string // containers to bring back if the run stops them
	if c.Containers != nil {
		running = c.Containers.Running(ctx)
	}
	req := source.InstallRequest{Scope: scope, Package: c.appPackage(scope), Mode: in.Mode}
	req.BeforeChange = func() error {
		err := c.startJournal(j, running) // kept for the next start, should this run be cut short (ADR-0125)
		journaled = err == nil
		return err
	}
	start := c.Now()
	res := c.Installer.Install(ctx, req, 2*timeout, c.progressSender(j.ID))
	code, detail = res.Code, res.Detail
	after := context.WithoutCancel(root)
	switch {
	case root.Err() != nil && code != protocol.CodeOK:
		code = protocol.CodeInterrupted // the agent is stopping; whatever the run hit, it was cut short
	case code == protocol.CodeOK && c.Installer.RebootRequired(after):
		code = protocol.CodeOKRebootRequired
	}
	// Packages changed: containers that ran before run again, even if the run itself failed half way.
	var restarted []string
	if journaled && c.Containers != nil && root.Err() == nil {
		var failed []string
		restarted, failed = c.Containers.Restore(after, running)
		if len(restarted) > 0 {
			c.Log.Printf("job %s: containers that did not come back by themselves, started again: %v", j.ID, restarted)
		}
		if len(failed) > 0 {
			c.Log.Printf("job %s: containers that ran before and could not be started again: %v", j.ID, failed)
		}
	}
	// Packages changed and the run went through: does the host still do its job (agent.yaml health)?
	if journaled && c.Health != nil && (code == protocol.CodeOK || code == protocol.CodeOKRebootRequired) {
		c.progressSender(j.ID)(phaseAt(protocol.PhaseVerifying, 97))
		if failed := c.Health.Settle(after); failed != "" {
			c.Log.Printf("job %s: health after the run: %s", j.ID, failed)
			code, detail = protocol.CodeFailedHealth, "health: "+failed
		}
	}
	if journaled {
		summary = &wire.RunSummary{PackageCount: len(res.Packages), Seconds: int(c.Now().Sub(start).Seconds()),
			Packages: res.Packages[:min(len(res.Packages), wire.MaxSummaryPackages)], OutdatedServices: res.OutdatedServices,
			Replaced: res.Replaced, Restarted: restarted}
		summary.LogFile = c.writeRunLog(j.ID, res.Log)
		if len(res.Replaced) > 0 {
			c.Log.Printf("job %s: packages replaced by the run: %v", j.ID, res.Replaced)
		}
		c.Log.Printf("job %s install %s: %d packages in %s, log %s", j.ID, j.Scope, summary.PackageCount,
			time.Duration(summary.Seconds)*time.Second, summary.LogFile)
		if len(res.OutdatedServices) > 0 {
			c.Log.Printf("job %s: services still on replaced libraries: %v", j.ID, res.OutdatedServices)
		}
	}
	// What is pending now, so that Home Assistant's counts follow the run before its result arrives.
	c.checkLocked(after, sender(c.sendNow))
	return code, detail, summary, journaled
}

// runLogsKept: run logs on the host beyond these, oldest first, are removed.
const runLogsKept = 10

// writeRunLog keeps the package manager's output of a run in <state_dir>/runs/<job>.log; "" when there is none or it
// could not be written (the run's result does not depend on it).
func (c *Client) writeRunLog(jobID string, log []byte) string {
	if len(log) == 0 {
		return ""
	}
	dir := filepath.Join(c.Cfg.StateDir, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.Log.Printf("run log: %v", err)
		return ""
	}
	path := filepath.Join(dir, jobID+".log")
	if err := os.WriteFile(path, log, 0o600); err != nil {
		c.Log.Printf("run log: %v", err)
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) > runLogsKept {
		slices.SortFunc(entries, func(a, b os.DirEntry) int {
			ia, _ := a.Info()
			ib, _ := b.Info()
			return ia.ModTime().Compare(ib.ModTime())
		})
		for _, e := range entries[:len(entries)-runLogsKept] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return path
}

func (c *Client) appPackage(scope protocol.Scope) string {
	for _, a := range c.Cfg.Apps {
		if scope.Kind == protocol.ScopeApp && a.ID == scope.App {
			return a.Package
		}
	}
	return ""
}

// progressSender forwards install progress: a new phase at once, otherwise at most once per progressGap.
func (c *Client) progressSender(jobID string) func(source.Progress) {
	var last source.Progress
	var sentAt time.Time
	return func(p source.Progress) {
		now := c.Now()
		if p == last || (p.Phase == last.Phase && now.Sub(sentAt) < progressGap) {
			return
		}
		if p.Phase != last.Phase {
			c.Log.Printf("job %s: %s", jobID, p.Phase) // the host's own record of where a run got to
		}
		last, sentAt = p, now
		pct := p.Percent
		_ = c.sendNow(wire.JobStatus{T: wire.TJobStatus, JobID: jobID, Phase: p.Phase, Progress: &pct, Item: p.Item})
	}
}

// approvedSession makes an approved session the way out for runs and sends a run result the hub has not had yet.
func (c *Client) approvedSession(send func(any) error) {
	c.setSender(send)
	c.flushUnsent()
	c.reportJournal()
	c.reportUpdate(send)
	c.reportReboot(send)
}

func (c *Client) setSender(send func(any) error) {
	c.stateMu.Lock()
	c.send = send
	c.stateMu.Unlock()
}

// errNoSession: no approved session right now; the run journal keeps the result.
var errNoSession = errors.New("no approved session")

func (c *Client) sendNow(v any) error {
	c.stateMu.Lock()
	send := c.send
	c.stateMu.Unlock()
	if send == nil {
		return errNoSession
	}
	return send(v)
}

// Power tells the hub that this host is about to sleep or shut down (wire.PowerSleeping, wire.PowerShutdown), over
// the current approved session. false: there is none, the hub will see the host go offline instead.
func (c *Client) Power(state string) bool {
	return c.sendNow(wire.Power{T: wire.TPower, State: state}) == nil
}

func (c *Client) root() context.Context {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.rootCtx == nil {
		return context.Background()
	}
	return c.rootCtx
}

// sender adapts a send function to messageSender.
type sender func(any) error

func (s sender) Send(v any) error { return s(v) }

// Summary is what the local policy allows, as announced to the hub.
func Summary(cfg policy.Config) wire.PolicySummary {
	s := wire.PolicySummary{AllowedScopes: append([]string{}, cfg.Install.AllowedScopes...),
		DefaultScope: cfg.Install.DefaultScope, Auto: cfg.Install.Auto, AutoScope: cfg.Install.AutoScope,
		RebootAllowed: cfg.Reboot.Allowed,
		AgentUpdate:   cfg.AgentUpdate.Enabled, OutsideWindow: true, MinIntervalH: cfg.Install.MinIntervalH}
	for _, a := range cfg.Apps {
		s.Apps = append(s.Apps, wire.AppInfo{ID: a.ID, Name: a.Name})
	}
	if w := cfg.Install.Window; w != nil {
		s.Window = &wire.WindowInfo{Days: append([]string{}, w.Days...), Start: w.Start, End: w.End}
		s.TZ = zoneName()
	}
	return s
}

// zoneName is the host's time zone as an IANA name ("America/New_York"), the zone install.window is meant in; "" when
// the host does not say. hostZone reads it per platform (zone_windows.go, zone_other.go).
var zoneName = hostZone

// TLSConfig trusts the system roots, or only the CAs in caFile when it is set (internal PKI).
func TLSConfig(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("hub.ca_file holds no PEM certificate")
	}
	cfg.RootCAs = pool
	return cfg, nil
}
