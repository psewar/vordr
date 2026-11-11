package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"strings"
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

// A Home Assistant client as the integration will be one: it speaks /v1/ha over TLS with its own key.

type haMsg struct {
	t   string
	raw []byte
}

type haTestClient struct {
	t    *testing.T
	ws   *websocket.Conn
	msgs chan haMsg
	fp   string
}

func newHAKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func dialWS(t *testing.T, h *testHub, path string) *websocket.Conn {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(h.ts.Certificate())
	d := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: pool}, HandshakeTimeout: 5 * time.Second}
	ws, _, err := d.Dial("wss"+strings.TrimPrefix(h.ts.URL, "https")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// dialHA connects to path, answers the challenge with a signed hello (changed by mutate after signing, if given)
// and returns the client with the hub's status answer.
func dialHA(t *testing.T, h *testHub, path, name string, priv ed25519.PrivateKey, mutate func(*wire.HAHello)) (*haTestClient, wire.Status) {
	t.Helper()
	ws := dialWS(t, h, path)
	c := &haTestClient{t: t, ws: ws, msgs: make(chan haMsg, 100)}
	var ch wire.Challenge
	if err := ws.ReadJSON(&ch); err != nil || ch.T != wire.TChallenge {
		t.Fatalf("challenge: %+v %v", ch, err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	hello := wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: name, PublicKey: pub, IntegrationVersion: "test",
		Protocol: wire.HAProtocol, Signature: ed25519.Sign(priv, wire.HAHelloSigned(ch.Nonce, name, pub))}
	if mutate != nil {
		mutate(&hello)
	}
	if err := ws.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var st wire.Status
	if err := ws.ReadJSON(&st); err != nil || st.T != wire.TStatus {
		t.Fatalf("status: %+v %v", st, err)
	}
	c.fp = identity.Fingerprint(pub)
	go func() {
		defer close(c.msgs)
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var tt wire.Type
			_ = json.Unmarshal(data, &tt)
			c.msgs <- haMsg{tt.T, data}
		}
	}()
	return c, st
}

// until skips messages until one of type want satisfies cond, and decodes it into v.
func (c *haTestClient) until(want string, v any, cond func() bool) {
	c.t.Helper()
	c.within(5*time.Second, want, v, cond)
}

// within is until with its own time limit (live runs against a real package manager take minutes).
func (c *haTestClient) within(d time.Duration, want string, v any, cond func() bool) {
	c.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case m, open := <-c.msgs:
			if !open {
				c.t.Fatalf("connection ended while waiting for %s", want)
			}
			if m.t != want {
				continue
			}
			if err := json.Unmarshal(m.raw, v); err != nil {
				c.t.Fatal(err)
			}
			if cond == nil || cond() {
				return
			}
		case <-timeout:
			c.t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// quiet fails if anything but a heartbeat arrives within d.
func (c *haTestClient) quiet(d time.Duration) {
	c.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case m, open := <-c.msgs:
			if open && m.t != wire.THeartbeat {
				c.t.Fatalf("unexpected %s: %s", m.t, m.raw)
			}
		case <-timeout:
			return
		}
	}
}

// closed waits for the hub to end the connection; status messages before are returned.
func (c *haTestClient) closed() []wire.Status {
	c.t.Helper()
	var sts []wire.Status
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, open := <-c.msgs:
			if !open {
				return sts
			}
			if m.t == wire.TStatus {
				var st wire.Status
				_ = json.Unmarshal(m.raw, &st)
				sts = append(sts, st)
			}
		case <-timeout:
			c.t.Fatal("hub kept the connection open")
		}
	}
}

func (c *haTestClient) command(id, node, cmd string) wire.CommandResult {
	c.t.Helper()
	return c.commandWithReason(id, node, cmd, "")
}

// commandWithReason asks for cmd outside the host's window, with reason (ADR-0087).
func (c *haTestClient) commandWithReason(id, node, cmd, reason string) wire.CommandResult {
	c.t.Helper()
	if err := c.ws.WriteJSON(wire.Command{T: wire.TCommand, RequestID: id, Node: node, Command: cmd,
		Reason: reason}); err != nil {
		c.t.Fatal(err)
	}
	var res wire.CommandResult
	c.until(wire.TCommandResult, &res, func() bool { return res.RequestID == id })
	return res
}

func TestHomeAssistantClientEndToEnd(t *testing.T) {
	h := startHub(t, 5)
	checker := &fakeChecker{}
	agent := h.client(t, "host-a", checker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "host-a", Fingerprint: agent.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { return checker.calls() == 1 })
	agent.SetCheckMinInterval(0)

	// Not approved yet: the client gets its status and nothing else, and may not command.
	priv := newHAKey(t)
	ha, st := dialHA(t, h, wire.HAPath, "bravo", priv, nil)
	if st.State != wire.StatePendingApproval || st.Fingerprint != ha.fp {
		t.Fatalf("status of a new client: %+v", st)
	}
	if res := ha.command("r1", "host-a", "check"); res.Code != string(hub.AdminClientNotApproved) || res.JobID != "" {
		t.Fatalf("command of a pending client: %+v", res)
	}
	other := h.client(t, "host-b", &fakeChecker{}) // a change the hub announces to approved clients
	go other.Run(ctx)
	waitFor(t, "host-b pending", func() bool { return state(h.reg, "host-b") == wire.StatePendingApproval })
	ha.quiet(300 * time.Millisecond)
	if e, _ := h.reg.Get("host-a"); e.LastRun != nil {
		t.Fatalf("pending client started a run: %+v", e.LastRun)
	}

	if r := h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: "SHA256:wrong"}); r.Code != hub.AdminFingerprintMismatch {
		t.Fatalf("approve with a wrong fingerprint: %s", r.Code)
	}
	if r := h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp}); r.Code != hub.AdminOK {
		t.Fatalf("approve: %s", r.Code)
	}
	var approved wire.Status
	ha.until(wire.TStatus, &approved, func() bool { return approved.State == wire.StateApproved })
	var snap wire.Snapshot
	ha.until(wire.TSnapshot, &snap, nil)
	if len(snap.Hosts) != 1 || snap.Hosts[0].Node != "host-a" || !snap.Hosts[0].Online || snap.Hosts[0].Pending == nil ||
		*snap.Hosts[0].Pending != 3 || !snap.Hosts[0].Capabilities.Check || snap.Hosts[0].Capabilities.Reboot ||
		snap.Hosts[0].Device.Name != "Vördr host-a" || snap.Hub.Protocol != wire.HAProtocol ||
		len(snap.Hub.Waiting) != 1 || snap.Hub.Waiting[0].Node != "host-b" {
		t.Fatalf("snapshot: %+v", snap)
	}
	if r := h.srv.Admin(hub.AdminRequest{Cmd: "clients"}); len(r.Clients) != 1 || !r.Clients[0].Connected ||
		r.Clients[0].State != wire.StateApproved {
		t.Fatalf("clients: %+v", r.Clients)
	}

	// A command: answered at once with the job id, the result follows in the host's view.
	res := ha.command("r2", "host-a", "check")
	if res.Code != string(hub.AdminOK) || res.JobID == "" {
		t.Fatalf("check: %+v", res)
	}
	var hv wire.HostMsg
	ha.until(wire.THost, &hv, func() bool {
		r := hv.Host.LastRun
		return r != nil && r.JobID == res.JobID && r.State == string(protocol.CodeOK)
	})
	if checker.calls() != 2 {
		t.Fatalf("checks run: %d", checker.calls())
	}
	for _, c := range []struct{ node, cmd, code string }{
		{"host-a", "reboot", string(hub.AdminVerbUnsupported)},
		{"host-a", "install", string(hub.AdminVerbUnsupported)},
		{"host-a", "rm -rf /", string(hub.AdminBadRequest)},
		{"nobody", "check", string(hub.AdminUnknownNode)},
	} {
		if r := ha.command("r-"+c.cmd[:2], c.node, c.cmd); r.Code != c.code || r.JobID != "" {
			t.Errorf("%s %q: %+v, want %s", c.node, c.cmd, r, c.code)
		}
	}

	// Pushes: an approved host appears and leaves the waiting list, a forgotten one disappears.
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "host-b", Fingerprint: other.ID.Fingerprint()})
	var hb wire.HostMsg
	ha.until(wire.THost, &hb, func() bool { return hb.Host.Node == "host-b" })
	var hubMsg wire.HubMsg
	ha.until(wire.THub, &hubMsg, func() bool { return len(hubMsg.Hub.Waiting) == 0 })
	h.srv.Admin(hub.AdminRequest{Cmd: "forget", Node: "host-a"})
	var gone wire.HostRemoved
	ha.until(wire.THostRemoved, &gone, func() bool { return gone.Node == "host-a" })

	// Revoked: the client is told and disconnected; its key stays refused.
	if r := h.srv.Admin(hub.AdminRequest{Cmd: "reject-client", Client: "bravo"}); r.Code != hub.AdminOK {
		t.Fatalf("reject: %s", r.Code)
	}
	if sts := ha.closed(); len(sts) != 1 || sts[0].State != wire.StateRejected {
		t.Fatalf("status before the hub closed: %+v", sts)
	}
	if _, st := dialHA(t, h, wire.HAPath, "bravo", priv, nil); st.State != wire.StateRejected || st.Reason != wire.ReasonRejected {
		t.Fatalf("rejected client again: %+v", st)
	}
}

// A run outside the host's window (ADR-0087): Home Assistant names a reason, the hub signs it into the job and keeps
// it with the run, the agent runs it. The plain install button gets the button's reason from the hub (ADR-0109). A
// reason for a check, an unreadable one, or one for an agent that does not announce it, the hub refuses.
func TestOutsideWindowFromHomeAssistant(t *testing.T) {
	// The hub reads the window in the zone the agent announces; in one process that is this process's own.
	oldZone := zoneName
	zoneName = func() string { return "Local" }
	defer func() { zoneName = oldZone }()
	h := startHub(t, 5)
	checker := &fakeChecker{}
	agent := h.client(t, "host-a", checker)
	closed := closedWindow()
	agent.Cfg.Install = policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all", Window: &closed,
		TimeoutMin: 60} // no minimum gap: two installs follow each other here
	agent.Installer = &fakeInstaller{result: source.InstallResult{Code: protocol.CodeOK}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "host-a", Fingerprint: agent.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { return checker.calls() == 1 })

	ha, _ := dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil)
	h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp})
	var snap wire.Snapshot
	ha.until(wire.TSnapshot, &snap, nil)
	if len(snap.Hosts) != 1 || !snap.Hosts[0].Capabilities.OutsideWindow || !snap.Hosts[0].Capabilities.Install {
		t.Fatalf("capabilities: %+v", snap.Hosts)
	}
	finished := func(res wire.CommandResult) wire.RunView {
		t.Helper()
		if res.Code != string(hub.AdminOK) || res.JobID == "" {
			t.Fatalf("command: %+v", res)
		}
		var hv wire.HostMsg
		ha.until(wire.THost, &hv, func() bool {
			r := hv.Host.LastRun
			return r != nil && r.JobID == res.JobID && r.Finished != nil
		})
		return *hv.Host.LastRun
	}
	if r := finished(ha.command("r1", "host-a", "install")); r.State != string(protocol.CodeOK) ||
		r.OutsideWindow != hub.ButtonReason {
		t.Fatalf("install button without a reason: %+v", r)
	}
	const reason = "ops: tomorrow is no good"
	if r := finished(ha.commandWithReason("r2", "host-a", "install", reason)); r.State != string(protocol.CodeOK) ||
		r.OutsideWindow != reason {
		t.Fatalf("with a reason: %+v", r)
	}
	for _, c := range []struct{ cmd, reason, code string }{
		{"check", reason, string(hub.AdminBadRequest)},
		{"install", "ops\nrm -rf /", string(hub.AdminBadReason)},
		{"install", strings.Repeat("x", protocol.MaxReasonLen+1), string(hub.AdminBadReason)},
	} {
		if r := ha.commandWithReason("r-"+c.cmd, "host-a", c.cmd, c.reason); r.Code != c.code || r.JobID != "" {
			t.Errorf("%s with %q: %+v, want %s", c.cmd, c.reason, r, c.code)
		}
	}
}

// The agent's minimum interval reaches the hub (ADR-0110): after an install that changed packages, a second one from
// Home Assistant is refused by the hub at once (rate_limited) - no job, no second run - and the view says when the
// next is possible.
func TestRateLimitFromHomeAssistant(t *testing.T) {
	h := startHub(t, 5)
	checker := &fakeChecker{}
	agent := h.client(t, "host-a", checker)
	agent.Cfg.Install = policy.Install{AllowedScopes: []string{"all"}, DefaultScope: "all", MinIntervalH: 6,
		TimeoutMin: 60}
	inst := &fakeInstaller{result: source.InstallResult{Code: protocol.CodeOK}, change: true}
	agent.Installer = inst
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "host-a", Fingerprint: agent.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { return checker.calls() == 1 })

	ha, _ := dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil)
	h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp})
	var snap wire.Snapshot
	ha.until(wire.TSnapshot, &snap, nil)
	if len(snap.Hosts) != 1 || snap.Hosts[0].NextInstall != nil {
		t.Fatalf("before any run: %+v", snap.Hosts)
	}
	res := ha.command("r1", "host-a", "install")
	if res.Code != string(hub.AdminOK) || res.JobID == "" {
		t.Fatalf("first install: %+v", res)
	}
	var hv wire.HostMsg
	ha.until(wire.THost, &hv, func() bool {
		r := hv.Host.LastRun
		return r != nil && r.JobID == res.JobID && r.Finished != nil && hv.Host.NextInstall != nil
	})
	if hv.Host.LastRun.State != string(protocol.CodeOK) {
		t.Fatalf("first run: %+v", hv.Host.LastRun)
	}
	if left := time.Until(*hv.Host.NextInstall); left < 5*time.Hour || left > 6*time.Hour {
		t.Fatalf("next install in %s, want about 6 h", left)
	}
	if r := ha.command("r2", "host-a", "install"); r.Code != string(hub.AdminRateLimited) || r.JobID != "" {
		t.Fatalf("second install within the interval: %+v", r)
	}
	if e, _ := h.reg.Get("host-a"); e.LastRun == nil || e.LastRun.JobID != res.JobID {
		t.Fatalf("a refused install started a run: %+v", e.LastRun)
	}
}

func TestHomeAssistantClientRefusals(t *testing.T) {
	h := startHub(t, 5)
	cases := map[string]struct {
		path   string
		mutate func(*wire.HAHello)
		reason wire.StatusReason
	}{
		"bad signature":   {wire.HAPath, func(m *wire.HAHello) { m.Signature[0] ^= 1 }, wire.ReasonBadHello},
		"agent role":      {wire.HAPath, func(m *wire.HAHello) { m.Role = "agent" }, wire.ReasonBadHello},
		"bad name":        {wire.HAPath, func(m *wire.HAHello) { m.Name = "Folk Wang" }, wire.ReasonBadHello},
		"other protocol":  {wire.HAPath, func(m *wire.HAHello) { m.Protocol = wire.HAProtocol + 1 }, wire.ReasonProtocolMismatch},
		"on agent tunnel": {wire.TunnelPath, nil, wire.ReasonBadHello},
	}
	for name, c := range cases {
		cl, st := dialHA(t, h, c.path, "bravo", newHAKey(t), c.mutate)
		if st.State != wire.StateRejected || st.Reason != c.reason {
			t.Errorf("%s: %+v", name, st)
			continue
		}
		cl.closed()
	}
	if r := h.srv.Admin(hub.AdminRequest{Cmd: "clients"}); len(r.Clients) != 0 {
		t.Fatalf("refused hellos registered clients: %+v", r.Clients)
	}

	// A valid agent hello on the HA path is no HA hello.
	ws := dialWS(t, h, wire.HAPath)
	var ch wire.Challenge
	if err := ws.ReadJSON(&ch); err != nil {
		t.Fatal(err)
	}
	id, _, _ := identity.LoadOrCreate(t.TempDir())
	if err := ws.WriteJSON(wire.Hello{T: wire.THello, Node: "host-x", PublicKey: id.Public(), AgentVersion: "test",
		Signature: id.Sign(wire.HelloSigned(ch.Nonce, "host-x", id.Public()))}); err != nil {
		t.Fatal(err)
	}
	var st wire.Status
	if err := ws.ReadJSON(&st); err != nil || st.State != wire.StateRejected || st.Reason != wire.ReasonBadHello {
		t.Fatalf("agent hello on the HA path: %+v %v", st, err)
	}
	if _, known := h.reg.Get("host-x"); known {
		t.Fatal("agent hello on the HA path registered a host")
	}

	// The same name with another key, and one pending client too many.
	dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil)
	if _, st := dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil); st.Reason != wire.ReasonKeyMismatch {
		t.Fatalf("other key: %+v", st)
	}
	for i := 0; i < 4; i++ {
		dialHA(t, h, wire.HAPath, "test-"+string(rune('a'+i)), newHAKey(t), nil)
	}
	if _, st := dialHA(t, h, wire.HAPath, "test-z", newHAKey(t), nil); st.Reason != wire.ReasonTooManyPending {
		t.Fatalf("sixth pending client: %+v", st)
	}
}

// A command the hub cannot answer cleanly ends the connection instead of being guessed at.
func TestMalformedCommandEndsConnection(t *testing.T) {
	h := startHub(t, 5)
	for i, c := range []wire.Command{
		{RequestID: "", Node: "host-a", Command: "check"},
		{RequestID: strings.Repeat("r", 65), Node: "host-a", Command: "check"},
		{RequestID: "r1", Node: "../etc", Command: "check"},
		{RequestID: "r1", Node: "host-a", Command: "install_app:" + strings.Repeat("x", 80)},
	} {
		name := "ha-" + string(rune('a'+i))
		ha, _ := dialHA(t, h, wire.HAPath, name, newHAKey(t), nil)
		h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: name, Fingerprint: ha.fp})
		var snap wire.Snapshot
		ha.until(wire.TSnapshot, &snap, nil)
		c.T = wire.TCommand
		if err := ha.ws.WriteJSON(c); err != nil {
			t.Fatal(err)
		}
		ha.closed()
	}
}

func TestForgottenClientIsDisconnected(t *testing.T) {
	h := startHub(t, 5)
	ha, _ := dialHA(t, h, wire.HAPath, "bravo", newHAKey(t), nil)
	h.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp})
	var snap wire.Snapshot
	ha.until(wire.TSnapshot, &snap, nil)
	if len(snap.Hosts) != 0 || snap.Hub.Waiting == nil {
		t.Fatalf("empty snapshot: %+v", snap)
	}
	if r := h.srv.Admin(hub.AdminRequest{Cmd: "forget-client", Client: "bravo"}); r.Code != hub.AdminOK {
		t.Fatal(r.Code)
	}
	ha.closed()
}
