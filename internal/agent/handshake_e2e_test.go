package agent

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// The run handshake between agent and hub (ADR-0114), with a real hub and agent.

// gateInstaller changes packages, reports a phase, and finishes only when released.
type gateInstaller struct {
	started chan struct{}
	release chan struct{}
}

func newGateInstaller() *gateInstaller {
	return &gateInstaller{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateInstaller) Install(_ context.Context, req source.InstallRequest, _ time.Duration,
	progress func(source.Progress)) source.InstallResult {
	if err := req.BeforeChange(); err != nil {
		return source.InstallResult{Code: protocol.CodeFailedPackageManager, Detail: err.Error()}
	}
	progress(source.Progress{Phase: protocol.PhaseInstalling, Percent: 50})
	close(g.started)
	<-g.release
	return source.InstallResult{Code: protocol.CodeOK, Packages: []string{"libfoo"}}
}

func (g *gateInstaller) RebootRequired(context.Context) bool { return false }

// breaker records the hub's connections so a test can break every tunnel: httptest forgets a connection once the
// WebSocket upgrade hijacked it.
type breaker struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (b *breaker) Accept() (net.Conn, error) {
	c, err := b.Listener.Accept()
	if err == nil {
		b.mu.Lock()
		b.conns = append(b.conns, c)
		b.mu.Unlock()
	}
	return c, err
}

func (b *breaker) breakAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.Close()
	}
	b.conns = nil
}

func startBreakableHub(t *testing.T) (*testHub, *breaker) {
	t.Helper()
	var b *breaker
	h := startHubWith(t, 5, func(l net.Listener) net.Listener { b = &breaker{Listener: l}; return b })
	return h, b
}

// handshakeAgent: an approved, connected agent on host "golf" that may install, its first check done.
func handshakeAgent(t *testing.T, h *testHub, ctx context.Context, inst Installer) *Client {
	t.Helper()
	c := h.client(t, "golf", &fakeChecker{})
	c.Cfg.Install = policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all", Auto: policy.AutoOff,
		TimeoutMin: 60}
	c.Installer = inst
	go c.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "golf") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "golf", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "online", func() bool { online, _ := h.srv.Online("golf"); return online })
	// The check after the approval holds the agent's lock; an install sent meanwhile would be refused as busy.
	waitFor(t, "first report", func() bool { e, _ := h.reg.Get("golf"); return e.LastReport != nil })
	waitFor(t, "check done", func() bool {
		if !c.checkMu.TryLock() {
			return false
		}
		c.checkMu.Unlock()
		return true
	})
	return c
}

func started(t *testing.T, g *gateInstaller) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the install did not start")
	}
}

func lastRunOf(h *testHub, node string) hub.Run {
	e, _ := h.reg.Get(node)
	if e.LastRun == nil {
		return hub.Run{}
	}
	return *e.LastRun
}

func eventNames(t *testing.T, h *testHub, node string) []string {
	t.Helper()
	r := h.srv.Admin(hub.AdminRequest{Cmd: "events", Events: &hub.EventFilter{Node: node, Limit: hub.MaxEventLimit}})
	var names []string
	for _, raw := range r.Events {
		var e struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(raw, &e)
		names = append(names, e.Event)
	}
	return names
}

// reconnected breaks every tunnel and waits until the agent is back with a new hello.
func reconnected(t *testing.T, h *testHub, b *breaker, node string) {
	t.Helper()
	n := countEvents(eventNames(t, h, node), "hello")
	b.breakAll()
	waitFor(t, "a new hello", func() bool { return countEvents(eventNames(t, h, node), "hello") > n })
	waitFor(t, "online again", func() bool { online, _ := h.srv.Online(node); return online })
}

func countEvents(list []string, name string) int {
	n := 0
	for _, e := range list {
		if e == name {
			n++
		}
	}
	return n
}

func approvedHA(t *testing.T, h *testHub) *haTestClient {
	t.Helper()
	ha, _ := dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil)
	if r := h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp}); r.Code != hub.AdminOK {
		t.Fatalf("approve client: %s", r.Code)
	}
	var st wire.Status
	ha.until(wire.TStatus, &st, func() bool { return st.State == wire.StateApproved })
	return ha
}

// The tunnel breaks during an install. The hub records the run as interrupted, the agent's hello names it as
// running: it is running again, a command for the host waits for it, and the install's real result is kept.
func TestTunnelBreakDuringAnInstall(t *testing.T) {
	h, b := startBreakableHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst := newGateInstaller()
	c := handshakeAgent(t, h, ctx, inst)

	job, code := h.srv.StartJob("golf", protocol.VerbInstall, "all", hub.ByAdmin)
	if code != hub.AdminOK {
		t.Fatalf("start: %s", code)
	}
	go h.srv.WaitJob(context.Background(), job)
	started(t, inst)
	waitFor(t, "installing", func() bool { return lastRunOf(h, "golf").Phase == protocol.PhaseInstalling })

	reconnected(t, h, b, "golf")
	waitFor(t, "running again", func() bool { r := lastRunOf(h, "golf"); return r.JobID == job.ID && r.Code == "" })
	ha := approvedHA(t, h)
	if res := ha.command("r1", "golf", "check"); res.Code != string(hub.AdminOK) {
		t.Fatalf("check while the install runs: %+v", res)
	}
	if q := h.srv.Queued("golf"); !slices.Equal(q, []string{"check"}) {
		t.Fatalf("queued: %v", q)
	}

	close(inst.release)
	waitFor(t, "the real result", func() bool { return lastRunOf(h, "golf").Code == protocol.CodeOK })
	if r := lastRunOf(h, "golf"); r.JobID != job.ID || r.Summary == nil || r.Summary.PackageCount != 1 {
		t.Fatalf("run: %+v", r)
	}
	waitFor(t, "acknowledged", func() bool { jr, _ := c.readJournal(); return jr.Acknowledged })
	ev := eventNames(t, h, "golf")
	// run_continues: the hub saw the old connection end first and recorded interrupted, the hello set it right;
	// job_continues: the hello came first, and nothing was recorded.
	if countEvents(ev, "run_continues")+countEvents(ev, "job_continues") != 1 || slices.Contains(ev, "run_lost") {
		t.Fatalf("events: %v", ev)
	}
}

// The ack for a result is lost (the hub host lost power right after storing it). The agent sends the result again
// on its next session; the hub stores it once and acknowledges again.
func TestResultSentAgainIsStoredOnce(t *testing.T) {
	h, b := startBreakableHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst := newGateInstaller()
	close(inst.release)
	c := handshakeAgent(t, h, ctx, inst)
	job, _ := h.srv.StartJob("golf", protocol.VerbInstall, "all", hub.ByAdmin)
	if st, _ := h.srv.WaitJob(context.Background(), job); st.Code != protocol.CodeOK {
		t.Fatalf("install: %+v", st)
	}
	waitFor(t, "acknowledged", func() bool { jr, _ := c.readJournal(); return jr.Acknowledged })
	first := lastRunOf(h, "golf")

	c.journalMu.Lock()
	jr, _ := c.readJournal()
	jr.Acknowledged = false
	err := writeJournal(c.journalPath(), jr)
	c.journalMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reconnected(t, h, b, "golf")
	waitFor(t, "acknowledged again", func() bool { jr, _ := c.readJournal(); return jr.Acknowledged })
	if again := lastRunOf(h, "golf"); again.Code != protocol.CodeOK || !again.Finished.Equal(*first.Finished) {
		t.Fatalf("stored twice: %+v", again)
	}
	ev := eventNames(t, h, "golf")
	if countEvents(ev, "job_status") != 1 || countEvents(ev, "job_status_again") != 1 {
		t.Fatalf("events: %v", ev)
	}
}

// The agent finished its install while the hub was away, and the hub came back with the run still open (it
// restarted during it). The hello names the result as held: the run waits for it, and it arrives.
func TestResultHeldOverAHubRestart(t *testing.T) {
	h := startHub(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	inst := newGateInstaller()
	c := handshakeAgent(t, h, ctx, inst)
	job, _ := h.srv.StartJob("golf", protocol.VerbInstall, "all", hub.ByAdmin)
	started(t, inst)
	cancel() // the agent stops; its install goes on (WaitRuns) and finds no session for its result
	waitFor(t, "offline", func() bool { online, _ := h.srv.Online("golf"); return !online })
	close(inst.release)
	c.WaitRuns()
	if jr, _ := c.readJournal(); jr.Code != protocol.CodeOK || jr.Acknowledged {
		t.Fatalf("journal: %+v", jr)
	}
	open := lastRunOf(h, "golf")
	open.Code, open.Finished, open.Phase = "", nil, protocol.PhaseInstalling // as a restarted hub has it
	if err := h.reg.SetRun("golf", open); err != nil {
		t.Fatal(err)
	}

	again := h.client(t, "golf", &fakeChecker{})
	again.Cfg = c.Cfg
	again.ID = c.ID
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go again.Run(ctx2)
	waitFor(t, "the held result", func() bool { return lastRunOf(h, "golf").Code == protocol.CodeOK })
	if r := lastRunOf(h, "golf"); r.JobID != job.ID || r.Detail != "" {
		t.Fatalf("run: %+v", r)
	}
	waitFor(t, "acknowledged", func() bool { jr, _ := again.readJournal(); return jr.Acknowledged })
	if slices.Contains(eventNames(t, h, "golf"), "run_lost") {
		t.Fatal("the held result counted as lost")
	}
}

// The hub sent a job and the agent restarted before its journal (or a check's answer was lost): the hub's run is
// open, the hello names nothing, and the host is free at once.
func TestHelloWithoutTheRunFreesTheHost(t *testing.T) {
	h, b := startBreakableHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handshakeAgent(t, h, ctx, newGateInstaller())
	if err := h.reg.SetRun("golf", hub.Run{JobID: "gone", Verb: protocol.VerbInstall, Scope: "all",
		Phase: protocol.PhaseInstalling, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	reconnected(t, h, b, "golf")
	waitFor(t, "settled", func() bool { return lastRunOf(h, "golf").Code == protocol.CodeInterrupted })
	if r := lastRunOf(h, "golf"); r.Finished == nil || !strings.Contains(r.Detail, "no such run") {
		t.Fatalf("run: %+v", r)
	}
	ha := approvedHA(t, h)
	res := ha.command("r1", "golf", "check")
	if res.Code != string(hub.AdminOK) || len(h.srv.Queued("golf")) != 0 {
		t.Fatalf("the host is not free: %+v, queued %v", res, h.srv.Queued("golf"))
	}
	// The check ran at once; wait for its end, or the hub still writes its register while the test cleans up.
	waitFor(t, "the check's result", func() bool { r := lastRunOf(h, "golf"); return r.JobID == res.JobID && r.Code != "" })
}

// rawAgent logs in like an agent, with or without Runs in its hello, and returns its connection once approved.
func rawAgent(t *testing.T, h *testHub, node string, runs *wire.Runs) *wire.Conn {
	t.Helper()
	id, _, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return rawAgentAs(t, h, id, node, runs)
}

// rawAgentAs: the same with a given key, so a test can log the same agent in again.
func rawAgentAs(t *testing.T, h *testHub, id *identity.Identity, node string, runs *wire.Runs) *wire.Conn {
	t.Helper()
	login := func() (*wire.Conn, wire.Status) {
		conn, _, err := dialRaw(t, h, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, raw, err := conn.Receive(5 * time.Second)
		var ch wire.Challenge
		if err != nil || json.Unmarshal(raw, &ch) != nil {
			t.Fatalf("challenge: %v", err)
		}
		if !slices.Contains(ch.Features, wire.FeatureAck) {
			t.Fatalf("challenge does not announce ack: %+v", ch.Features)
		}
		pub := id.Public()
		hello := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, Signature: id.Sign(wire.HelloSigned(ch.Nonce, node, pub)),
			AgentVersion: "old", Verbs: []protocol.Verb{protocol.VerbCheck}, Runs: runs}
		if err := conn.Send(hello); err != nil {
			t.Fatal(err)
		}
		_, raw, err = conn.Receive(5 * time.Second)
		var st wire.Status
		if err != nil || json.Unmarshal(raw, &st) != nil {
			t.Fatalf("status: %v", err)
		}
		return conn, st
	}
	conn, st := login()
	if st.State == wire.StatePendingApproval {
		conn.Close()
		h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: node, Fingerprint: id.Fingerprint()})
		conn, st = login()
	}
	if st.State != wire.StateApproved {
		t.Fatalf("status: %+v", st)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// An agent before ADR-0114 logs in without runs while the hub has its run open: the hub keeps its guess, through the
// whole login.
func TestOldAgentLoginLeavesTheRunOpen(t *testing.T) {
	h := startHub(t, 5)
	id, _, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := rawAgentAs(t, h, id, "old-agent", nil)
	open := hub.Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all", Phase: protocol.PhaseInstalling,
		Started: time.Now()}
	if err := h.reg.SetRun("old-agent", open); err != nil {
		t.Fatal(err)
	}
	first.Close()
	rawAgentAs(t, h, id, "old-agent", nil)
	if r := lastRunOf(h, "old-agent"); r.JobID != "j1" || r.Code != "" {
		t.Fatalf("an old agent's hello settled the run: %+v", r)
	}
}

// An agent that named its runs gets an ack for each final status; an agent before ADR-0114 gets none.
func TestAckOnlyToAgentsThatNamedTheirRuns(t *testing.T) {
	h := startHub(t, 5)
	for _, tc := range []struct {
		node string
		runs *wire.Runs
	}{{"new-agent", &wire.Runs{Running: []string{}, Unacknowledged: []string{}}}, {"old-agent", nil}} {
		conn := rawAgent(t, h, tc.node, tc.runs)
		if err := conn.Send(wire.JobStatus{T: wire.TJobStatus, JobID: "j-" + tc.node, Code: protocol.CodeOK}); err != nil {
			t.Fatal(err)
		}
		if err := conn.Send(wire.Heartbeat{T: wire.THeartbeat}); err != nil {
			t.Fatal(err)
		}
		var got []string
		for {
			typ, raw, err := conn.Receive(5 * time.Second)
			if err != nil {
				t.Fatalf("%s: %v", tc.node, err)
			}
			if typ == wire.THeartbeat {
				break // the hub answers in order: an ack comes before this answer
			}
			var a wire.Ack
			_ = json.Unmarshal(raw, &a)
			got = append(got, typ+":"+a.JobID)
		}
		want := []string(nil)
		if tc.runs != nil {
			want = []string{"ack:j-" + tc.node}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s got %v, want %v", tc.node, got, want)
		}
	}
}

// oldHub is a hub before ADR-0114 as the agent meets it: its challenge announces nothing, it refuses a hello with a
// field it does not know, and it never acknowledges. With announce it does announce ack, and still sends none.
type oldHub struct {
	*testHub
	hello   chan map[string]json.RawMessage
	results chan wire.JobStatus
}

func startOldHub(t *testing.T, announce bool) *oldHub {
	t.Helper()
	key, _, err := identity.LoadOrCreateAt(filepath.Join(t.TempDir(), "hub.key"))
	if err != nil {
		t.Fatal(err)
	}
	o := &oldHub{hello: make(chan map[string]json.RawMessage, 4), results: make(chan wire.JobStatus, 4)}
	var features []string
	if announce {
		features = []string{wire.FeatureAck}
	}
	up := websocket.Upgrader{}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := wire.NewConn(ws)
		defer conn.Close()
		if conn.Send(wire.Challenge{T: wire.TChallenge, Nonce: make([]byte, wire.NonceSize), Features: features}) != nil {
			return
		}
		_, raw, err := conn.Receive(5 * time.Second)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &fields) != nil {
			return
		}
		o.hello <- fields
		if conn.Send(wire.Status{T: wire.TStatus, State: wire.StateApproved, Features: features}) != nil {
			return
		}
		for {
			typ, raw, err := conn.Receive(5 * time.Second)
			if err != nil {
				return
			}
			var st wire.JobStatus
			if typ == wire.TJobStatus && json.Unmarshal(raw, &st) == nil && st.Final() {
				o.results <- st
			}
			if typ == wire.THeartbeat {
				_ = conn.Send(wire.Heartbeat{T: wire.THeartbeat})
			}
		}
	}))
	t.Cleanup(ts.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	o.testHub = &testHub{key: key, caFile: caFile, ts: ts, url: "wss" + strings.TrimPrefix(ts.URL, "https") + wire.TunnelPath}
	return o
}

// A new agent and an older hub: the hello carries no runs, and a sent result counts as reported, as before. Against
// a hub that announces ack, the result stays unacknowledged until the ack comes.
func TestNewAgentWithAnOlderHub(t *testing.T) {
	for _, announce := range []bool{false, true} {
		o := startOldHub(t, announce)
		c := o.client(t, "golf", &fakeChecker{})
		fin := time.Now().UTC()
		if err := writeJournal(c.journalPath(), runJournal{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all",
			Started: fin.Add(-time.Minute), Code: protocol.CodeOK, Finished: &fin}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go c.Run(ctx)
		hello := <-o.hello
		if _, has := hello["runs"]; has != announce {
			t.Fatalf("announce %v: hello %s", announce, hello["runs"])
		}
		select {
		case st := <-o.results:
			if st.JobID != "j1" {
				t.Fatalf("result: %+v", st)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no result")
		}
		if !announce {
			waitFor(t, "reported", func() bool { jr, _ := c.readJournal(); return jr.Acknowledged })
		} else {
			time.Sleep(200 * time.Millisecond) // the journal is written right after the send
			if jr, _ := c.readJournal(); jr.Acknowledged {
				t.Fatal("acknowledged without an ack")
			}
			c.acknowledged("j1")
			if jr, _ := c.readJournal(); !jr.Acknowledged {
				t.Fatal("ack not taken")
			}
		}
		cancel()
		c.WaitRuns()
	}
}
