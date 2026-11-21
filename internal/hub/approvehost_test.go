package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Approving a waiting host from Home Assistant (ADR-0192).

// approvalFixture: a hub that lets Home Assistant approve hosts, a host waiting for approval, and an approved client
// with role admin.
func approvalFixture(t *testing.T) (*viewFixture, *haSession, string) {
	t.Helper()
	f := newViewFixture(t)
	f.srv.HAApprovesHosts = true
	fp := f.host(t, "new-host", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	return f, f.haClient(t, "ha", RoleAdmin), fp
}

func (f *viewFixture) approveHost(sess *haSession, node, fingerprint string) wire.ApproveHostResult {
	return f.srv.haApproveHost(sess, wire.ApproveHost{T: wire.TApproveHost, RequestID: "r1", Node: node,
		Fingerprint: fingerprint})
}

func TestApproveHostFromHomeAssistant(t *testing.T) {
	f, sess, fp := approvalFixture(t)
	res := f.approveHost(sess, "new-host", fp)
	if res != (wire.ApproveHostResult{T: wire.TApproveHostResult, RequestID: "r1", Code: string(AdminOK)}) {
		t.Fatalf("result: %+v", res)
	}
	e, _ := f.srv.Reg.Get("new-host")
	if e.State != wire.StateApproved || e.DecidedAt == nil || !e.DecidedAt.Equal(f.now) {
		t.Fatalf("host: %s %v", e.State, e.DecidedAt)
	}
	// The event log names the client, as `vordr-hub approve` logs the approval without one.
	got := eventLines(t, f, "approve")
	if len(got) != 1 || got[0]["node"] != "new-host" || got[0]["client"] != "ha" || got[0]["code"] != "ok" {
		t.Fatalf("event: %v", got)
	}
	// The hub's view no longer lists it as waiting, and Home Assistant gets the host instead.
	if v := f.srv.HubView(f.srv.Reg.List()); len(v.Waiting) != 0 {
		t.Fatalf("still waiting: %+v", v.Waiting)
	}
	if res := f.approveHost(sess, "new-host", fp); res.Code != string(AdminNotWaiting) {
		t.Fatalf("approved twice: %+v", res)
	}
	if got := eventLines(t, f, "approve_refused"); len(got) != 1 || got[0]["code"] != string(AdminNotWaiting) {
		t.Fatalf("refusal: %v", got)
	}
}

// Every refusal has its code and its line in the event log, and leaves the host as it was: a rejection at the hub is
// never undone from Home Assistant.
func TestApproveHostRefusals(t *testing.T) {
	f, admin, fp := approvalFixture(t)
	other := strings.Repeat("ab", 32)
	approvedFP := f.host(t, "known", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	rejectedFP := f.host(t, "refused", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	if code := f.srv.Reg.Decide("refused", wire.StateRejected, "", f.now); code != AdminOK {
		t.Fatal(code)
	}
	operate := f.haClient(t, "operator", RoleOperate)
	observe := f.haClient(t, "viewer", RoleObserve)
	unset := f.haClient(t, "unset", "")
	h, pendingFP := haHello(t, "pending")
	f.srv.Clients.Admit(h, pendingFP, f.now)
	cases := []struct {
		name        string
		sess        *haSession
		node, fp    string
		off         bool
		want        AdminCode
		state, host string
	}{
		{"switch off", admin, "new-host", fp, true, AdminHAApprovalOff, "", "new-host"},
		{"pending client", &haSession{name: "pending", fingerprint: pendingFP}, "new-host", fp, false,
			AdminClientNotApproved, "", "new-host"},
		{"pending client, switch off", &haSession{name: "pending", fingerprint: pendingFP}, "new-host", fp, true,
			AdminClientNotApproved, "", "new-host"},
		{"session of another key", &haSession{name: "ha", fingerprint: "SHA256:old-key"}, "new-host", fp, false,
			AdminClientNotApproved, "", "new-host"},
		{"unknown client", &haSession{name: "nobody", fingerprint: "x"}, "new-host", fp, false,
			AdminClientNotApproved, "", "new-host"},
		{"role operate", operate, "new-host", fp, false, AdminRoleForbids, "operate", "new-host"},
		{"role observe", observe, "new-host", fp, false, AdminRoleForbids, "observe", "new-host"},
		{"no role set", unset, "new-host", fp, false, AdminRoleForbids, "operate", "new-host"},
		{"unknown node", admin, "nobody-here", fp, false, AdminUnknownNode, "", "new-host"},
		{"approved host", admin, "known", approvedFP, false, AdminNotWaiting, "", "known"},
		{"rejected host", admin, "refused", rejectedFP, false, AdminNotWaiting, "", "refused"},
		{"another key", admin, "new-host", other, false, AdminFingerprintMismatch, "", "new-host"},
		{"upper case", admin, "new-host", strings.ToUpper(fp), false, AdminBadRequest, "", "new-host"},
		{"short", admin, "new-host", fp[:62], false, AdminBadRequest, "", "new-host"},
		{"empty", admin, "new-host", "", false, AdminBadRequest, "", "new-host"},
		{"with spaces", admin, "new-host", " " + fp, false, AdminBadRequest, "", "new-host"},
	}
	for _, c := range cases {
		before, _ := f.srv.Reg.Get(c.host)
		f.srv.HAApprovesHosts = !c.off
		res := f.approveHost(c.sess, c.node, c.fp)
		f.srv.HAApprovesHosts = true
		if res.Code != string(c.want) || res.RequestID != "r1" || res.T != wire.TApproveHostResult {
			t.Errorf("%s: %+v, want %s", c.name, res, c.want)
		}
		if after, _ := f.srv.Reg.Get(c.host); after.State != before.State || after.DecidedAt != before.DecidedAt {
			t.Errorf("%s: %s changed from %s to %s", c.name, c.host, before.State, after.State)
		}
		lines := eventLines(t, f, "approve_refused")
		if len(lines) == 0 {
			t.Fatalf("%s: nothing logged", c.name)
		}
		last := lines[len(lines)-1]
		if last["code"] != string(c.want) || last["client"] != c.sess.name || last["node"] != c.node {
			t.Errorf("%s: logged %v", c.name, last)
		}
		if c.state != "" && last["role"] != c.state {
			t.Errorf("%s: role logged %v", c.name, last["role"])
		}
	}
	if got := eventLines(t, f, "approve"); len(got) != 0 {
		t.Fatalf("approved: %v", got)
	}
	if e, _ := f.srv.Reg.Get("refused"); e.State != wire.StateRejected {
		t.Fatalf("rejection undone: %s", e.State)
	}

	// The register cannot be written: nothing changes, and the code says so.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.srv.Reg.path = filepath.Join(blocker, "registry.json")
	if res := f.approveHost(admin, "new-host", fp); res.Code != string(AdminStoreFailed) {
		t.Fatalf("store: %+v", res)
	}
	if e, _ := f.srv.Reg.Get("new-host"); e.State != wire.StatePendingApproval || e.DecidedAt != nil {
		t.Fatalf("after a failed store: %s %v", e.State, e.DecidedAt)
	}
}

// The register decides under its lock: of many requests at once, exactly one approves, the others find the host no
// longer waiting.
func TestApproveWaitingIsOneStep(t *testing.T) {
	f := newViewFixture(t)
	fp := f.host(t, "new-host", nil, wire.PolicySummary{}, false)
	codes := make([]AdminCode, 16)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = f.srv.Reg.ApproveWaiting("new-host", fp, f.now)
		}()
	}
	wg.Wait()
	if n := len(slices.DeleteFunc(slices.Clone(codes), func(c AdminCode) bool { return c != AdminOK })); n != 1 {
		t.Fatalf("approved %d times: %v", n, codes)
	}
	for _, c := range codes {
		if c != AdminOK && c != AdminNotWaiting {
			t.Fatalf("code %s", c)
		}
	}
}

// The feature is named only with the switch on, to an approved client with its key and role admin.
func TestApproveHostFeature(t *testing.T) {
	f, admin, _ := approvalFixture(t)
	operate := f.haClient(t, "operator", RoleOperate)
	h, pendingFP := haHello(t, "pending")
	f.srv.Clients.Admit(h, pendingFP, f.now)
	both := []string{wire.FeatureAgentDownload, wire.FeatureApproveHost, wire.FeatureHoldRelease}
	hold := []string{wire.FeatureAgentDownload, wire.FeatureHoldRelease} // ADR-0208: operate and admin
	only := []string{wire.FeatureAgentDownload}
	for _, c := range []struct {
		name, client, fp string
		on               bool
		want             []string
	}{
		{"admin", "ha", admin.fingerprint, true, both},
		{"admin, switch off", "ha", admin.fingerprint, false, hold},
		{"operate", "operator", operate.fingerprint, true, hold},
		{"pending", "pending", pendingFP, true, only},
		{"another key", "ha", "SHA256:old-key", true, only},
		{"unknown", "nobody", "x", true, only},
	} {
		f.srv.HAApprovesHosts = c.on
		if got := f.srv.haFeatures(c.client, c.fp); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// hub.yaml ha_approves_hosts: off unless set, only a boolean, and in the example file, off, which passes as it stands.
func TestHAApprovesHostsConfig(t *testing.T) {
	base := "tls_cert: x\ntls_key: y\n"
	for _, c := range []struct {
		yaml string
		want bool
	}{{"", false}, {"ha_approves_hosts: false\n", false}, {"ha_approves_hosts: true\n", true}} {
		cfg, issues := ParseConfig([]byte(base + c.yaml))
		if cfg.HAApprovesHosts != c.want || slices.ContainsFunc(issues, func(i string) bool {
			return strings.HasPrefix(i, "hub.yaml") || strings.Contains(i, "ha_approves_hosts")
		}) {
			t.Errorf("%q: %v %v", c.yaml, cfg.HAApprovesHosts, issues)
		}
	}
	if _, issues := ParseConfig([]byte(base + "ha_approves_hosts: sometimes\n")); !slices.ContainsFunc(issues,
		func(i string) bool { return strings.HasPrefix(i, "hub.yaml") }) {
		t.Errorf("a value that is no boolean passed: %v", issues)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "hub.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\nha_approves_hosts: false") || !strings.Contains(string(raw), "ADR-0192") {
		t.Fatal("the example does not show ha_approves_hosts, off, with its ADR")
	}
	cfg, issues := ParseConfig(raw)
	if cfg.HAApprovesHosts || slices.ContainsFunc(issues, func(i string) bool { return strings.HasPrefix(i, "hub.yaml") }) {
		t.Fatalf("example: %v %v", cfg.HAApprovesHosts, issues)
	}
}

// haConn is a Home Assistant client on the real /v1/ha endpoint.
type haConn struct {
	t  *testing.T
	ws *websocket.Conn
}

// dialHA logs in as name with priv and returns the connection and the first status.
func dialHA(t *testing.T, url, name string, priv ed25519.PrivateKey) (*haConn, wire.Status) {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http")+wire.HAPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	var ch wire.Challenge
	if err := ws.ReadJSON(&ch); err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	hello := wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: name, PublicKey: pub, IntegrationVersion: "0.1",
		Protocol: wire.HAProtocol, Signature: ed25519.Sign(priv, wire.HAHelloSigned(ch.Nonce, name, pub))}
	var st wire.Status
	if err := ws.WriteJSON(hello); err != nil || ws.ReadJSON(&st) != nil {
		t.Fatalf("login: %v", err)
	}
	return &haConn{t: t, ws: ws}, st
}

// next reads until a message of type typ and decodes it into v; false: the connection ended.
func (c *haConn) next(typ string, v any) bool {
	c.t.Helper()
	for {
		c.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := c.ws.ReadMessage()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			c.t.Fatalf("no %s within 5 s", typ)
		}
		if err != nil {
			return false
		}
		var tt wire.Type
		if json.Unmarshal(data, &tt) == nil && tt.T == typ {
			if err := json.Unmarshal(data, v); err != nil {
				c.t.Fatal(err)
			}
			return true
		}
	}
}

// collect reads until a message of each of the types has come, in any order, and returns the first of each; other
// messages are skipped. The connection ending or 5 s without them fail the test.
func (c *haConn) collect(types ...string) map[string][]byte {
	c.t.Helper()
	got := map[string][]byte{}
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < len(types) {
		c.ws.SetReadDeadline(deadline)
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.t.Fatalf("got %d of %v: %v", len(got), types, err)
		}
		var tt wire.Type
		if json.Unmarshal(data, &tt) == nil && slices.Contains(types, tt.T) && got[tt.T] == nil {
			got[tt.T] = data
		}
	}
	return got
}

// quiet proves that the hub sent nothing to this client up to now: the hub answers a heartbeat after everything it
// sent before, so the first message after it must be that answer (views it pushes on its own, snapshot and hub, are
// skipped).
func (c *haConn) quiet(what string) {
	c.t.Helper()
	if err := c.ws.WriteJSON(wire.Heartbeat{T: wire.THeartbeat}); err != nil {
		c.t.Fatal(err)
	}
	for {
		c.ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.t.Fatalf("%s: %v", what, err)
		}
		var tt wire.Type
		json.Unmarshal(data, &tt)
		switch tt.T {
		case wire.THeartbeat:
			return
		case wire.TSnapshot, wire.THub, wire.THost, wire.THostRemoved:
		default:
			c.t.Fatalf("%s: the hub sent %s", what, data)
		}
	}
}

// adminClient registers name as an approved client with role and returns its key.
func adminClient(t *testing.T, f *viewFixture, name, role string) ed25519.PrivateKey {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fp := identity.Fingerprint(pub)
	h := wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: name, PublicKey: pub, Protocol: wire.HAProtocol}
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide(name, wire.StateApproved, fp, f.now)
	f.srv.Clients.SetRole(name, role)
	return priv
}

// Over /v1/ha: the status names the feature for an admin client, a role change sends the status again with the
// feature gone or back, approve_host approves the waiting host, and a malformed request ends the connection.
func TestApproveHostOverTheHAProtocol(t *testing.T) {
	f, _, fp := approvalFixture(t)
	priv := adminClient(t, f, "ha-admin", RoleAdmin)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())

	c, st := dialHA(t, ts.URL, "ha-admin", priv)
	if st.State != wire.StateApproved || !slices.Contains(st.Features, wire.FeatureApproveHost) {
		t.Fatalf("status: %+v", st)
	}
	for _, step := range []struct {
		role string
		has  bool
	}{{RoleOperate, false}, {RoleAdmin, true}} {
		if r := f.srv.Admin(AdminRequest{Cmd: "client-role", Client: "ha-admin", Role: step.role}); r.Code != AdminOK {
			t.Fatalf("client-role %s: %s", step.role, r.Code)
		}
		var again wire.Status
		if !c.next(wire.TStatus, &again) || again.State != wire.StateApproved ||
			slices.Contains(again.Features, wire.FeatureApproveHost) != step.has ||
			!slices.Contains(again.Features, wire.FeatureAgentDownload) {
			t.Fatalf("status after the role %s: %+v", step.role, again)
		}
	}

	if err := c.ws.WriteJSON(wire.ApproveHost{T: wire.TApproveHost, RequestID: "fix-1", Node: "new-host",
		Fingerprint: fp}); err != nil {
		t.Fatal(err)
	}
	// The answer comes from the read loop, the host's view from the push: in either order.
	got := c.collect(wire.TApproveHostResult, wire.THost)
	var res wire.ApproveHostResult
	if json.Unmarshal(got[wire.TApproveHostResult], &res) != nil || res.Code != string(AdminOK) || res.RequestID != "fix-1" {
		t.Fatalf("result: %s", got[wire.TApproveHostResult])
	}
	if e, _ := f.srv.Reg.Get("new-host"); e.State != wire.StateApproved {
		t.Fatalf("host: %s", e.State)
	}
	var hv wire.HostMsg
	if json.Unmarshal(got[wire.THost], &hv) != nil || hv.Host.Node != "new-host" {
		t.Fatalf("the approved host did not come: %s", got[wire.THost])
	}

	if err := c.ws.WriteJSON(wire.ApproveHost{T: wire.TApproveHost, RequestID: "fix-2", Node: "Not A Node",
		Fingerprint: fp}); err != nil {
		t.Fatal(err)
	}
	if c.next(wire.TApproveHostResult, &res) {
		t.Fatalf("a malformed request was answered: %+v", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(eventLines(t, f, "ha_protocol_violation")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no protocol violation in the event log")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A waiting client that has role admin already gets the feature with the status that approves it.
func TestApproveHostFeatureComesWithTheClientsApproval(t *testing.T) {
	f, _, _ := approvalFixture(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fp := identity.Fingerprint(pub)
	f.srv.Clients.Admit(wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: "waiting-ha", PublicKey: pub,
		Protocol: wire.HAProtocol}, fp, f.now)
	if code := f.srv.Clients.SetRole("waiting-ha", RoleAdmin); code != AdminOK {
		t.Fatal(code)
	}
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())
	c, st := dialHA(t, ts.URL, "waiting-ha", priv)
	if st.State != wire.StatePendingApproval || slices.Contains(st.Features, wire.FeatureApproveHost) {
		t.Fatalf("status while waiting: %+v", st)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "approve-client", Client: "waiting-ha", Fingerprint: fp}); r.Code != AdminOK {
		t.Fatalf("approve-client: %s", r.Code)
	}
	var again wire.Status
	if !c.next(wire.TStatus, &again) || again.State != wire.StateApproved ||
		!slices.Equal(again.Features, []string{wire.FeatureAgentDownload, wire.FeatureApproveHost, wire.FeatureHoldRelease}) {
		t.Fatalf("status with the approval: %+v", again)
	}
}

// pendingAgent logs a new host in over a real tunnel and returns its end of the connection once the hub keeps its
// session, with the fingerprint of its key; the host waits for approval.
func pendingAgent(t *testing.T, f *viewFixture, ts *httptest.Server, node string) (*wire.Conn, string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	conn := dialTunnel(t, ts)
	_, raw, err := conn.Receive(5 * time.Second)
	var ch wire.Challenge
	if err != nil || json.Unmarshal(raw, &ch) != nil {
		t.Fatalf("challenge: %v", err)
	}
	h := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "1.0",
		Verbs: []protocol.Verb{protocol.VerbCheck}, Signature: ed25519.Sign(priv, wire.HelloSigned(ch.Nonce, node, pub))}
	if err := conn.Send(h); err != nil {
		t.Fatal(err)
	}
	_, raw, err = conn.Receive(5 * time.Second)
	var st wire.Status
	if err != nil || json.Unmarshal(raw, &st) != nil || st.State != wire.StatePendingApproval {
		t.Fatalf("status: %v %+v", err, st)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if online, _ := f.srv.Online(node); online {
			return conn, identity.Fingerprint(pub)
		}
		if time.Now().After(deadline) {
			t.Fatal("the hub did not keep the session")
		}
	}
}

// Approved from Home Assistant, a host that is connected while it waits gets its status at once, as after
// `vordr-hub approve`.
func TestApproveHostTellsTheWaitingAgent(t *testing.T) {
	f, admin, _ := approvalFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())
	agent, fp := pendingAgent(t, f, ts, "fresh-host")
	if res := f.approveHost(admin, "fresh-host", fp); res.Code != string(AdminOK) {
		t.Fatalf("result: %+v", res)
	}
	for {
		typ, raw, err := agent.Receive(5 * time.Second)
		if err != nil {
			t.Fatalf("the agent got no status: %v", err)
		}
		if typ != wire.TStatus {
			continue
		}
		var st wire.Status
		if json.Unmarshal(raw, &st) != nil || st.State != wire.StateApproved || st.Fingerprint != fp {
			t.Fatalf("status: %s", raw)
		}
		return
	}
}

// resendStatus sends only to a session that is approved, with its key, as the register has it now: a waiting client,
// one rejected or approved again with another key since it logged in, gets nothing from it.
func TestResendStatusOnlyToAnApprovedSessionWithItsKey(t *testing.T) {
	f, _, _ := approvalFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.srv.Clients.Admit(wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: "waiting-ha", PublicKey: pub,
		Protocol: wire.HAProtocol}, identity.Fingerprint(pub), f.now)
	f.srv.Clients.SetRole("waiting-ha", RoleAdmin)
	waiting, st := dialHA(t, ts.URL, "waiting-ha", priv)
	if st.State != wire.StatePendingApproval {
		t.Fatalf("status: %+v", st)
	}
	f.srv.resendStatus("waiting-ha")
	waiting.quiet("waiting client")

	rejected, st := dialHA(t, ts.URL, "rejected-ha", adminClient(t, f, "rejected-ha", RoleAdmin))
	if st.State != wire.StateApproved {
		t.Fatalf("status: %+v", st)
	}
	f.srv.Clients.Decide("rejected-ha", wire.StateRejected, "", f.now) // the register only: no notifyClient
	f.srv.resendStatus("rejected-ha")
	rejected.quiet("rejected client")

	replaced, st := dialHA(t, ts.URL, "replaced-ha", adminClient(t, f, "replaced-ha", RoleAdmin))
	if st.State != wire.StateApproved {
		t.Fatalf("status: %+v", st)
	}
	f.srv.Clients.Forget("replaced-ha")
	adminClient(t, f, "replaced-ha", RoleAdmin) // the same name, approved again with another key
	f.srv.resendStatus("replaced-ha")
	replaced.quiet("session of the old key")

	approved, st := dialHA(t, ts.URL, "approved-ha", adminClient(t, f, "approved-ha", RoleAdmin))
	if st.State != wire.StateApproved {
		t.Fatalf("status: %+v", st)
	}
	f.srv.resendStatus("approved-ha")
	var again wire.Status
	if !approved.next(wire.TStatus, &again) || !slices.Contains(again.Features, wire.FeatureApproveHost) {
		t.Fatalf("the approved session got no status: %+v", again)
	}
}

// Without the switch the status does not name the feature, also for an admin; a role change sends it again all the
// same, without it.
func TestApproveHostFeatureNeedsTheSwitch(t *testing.T) {
	f := newViewFixture(t)
	priv := adminClient(t, f, "ha-admin", RoleAdmin)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())
	c, st := dialHA(t, ts.URL, "ha-admin", priv)
	hold := []string{wire.FeatureAgentDownload, wire.FeatureHoldRelease} // no approve_host without the switch
	if st.State != wire.StateApproved || !slices.Equal(st.Features, hold) {
		t.Fatalf("status: %+v", st)
	}
	f.srv.Admin(AdminRequest{Cmd: "client-role", Client: "ha-admin", Role: RoleOperate})
	var again wire.Status
	if !c.next(wire.TStatus, &again) || !slices.Equal(again.Features, hold) {
		t.Fatalf("status after the role change: %+v", again)
	}
}

// A client that is not approved gets one answer to approve_host, client_not_approved, and the connection ends: a
// pending client cannot write the event log full with requests.
func TestApproveHostFromAPendingClientEndsTheConnection(t *testing.T) {
	f, _, fp := approvalFixture(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())
	c, st := dialHA(t, ts.URL, "pending", priv)
	if st.State != wire.StatePendingApproval || slices.Contains(st.Features, wire.FeatureApproveHost) {
		t.Fatalf("status: %+v", st)
	}
	req := wire.ApproveHost{T: wire.TApproveHost, RequestID: "r0", Node: "new-host", Fingerprint: fp}
	if err := c.ws.WriteJSON(req); err != nil {
		t.Fatal(err)
	}
	var res wire.ApproveHostResult
	if !c.next(wire.TApproveHostResult, &res) || res.Code != string(AdminClientNotApproved) || res.RequestID != "r0" {
		t.Fatalf("answer: %+v", res)
	}
	if c.next(wire.TApproveHostResult, &res) {
		t.Fatalf("the connection stayed open: %+v", res)
	}
	if e, _ := f.srv.Reg.Get("new-host"); e.State != wire.StatePendingApproval {
		t.Fatalf("host: %s", e.State)
	}
	if got := eventLines(t, f, "approve_refused"); len(got) != 1 || got[0]["client"] != "pending" {
		t.Fatalf("refusals logged: %v", got)
	}
}
