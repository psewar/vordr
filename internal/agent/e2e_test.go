package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

var quiet = log.New(io.Discard, "", 0)

type fakeChecker struct {
	mu sync.Mutex
	n  int
}

func (f *fakeChecker) Check(context.Context) wire.Report {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return wire.Report{T: wire.TReport, CheckedAt: time.Now().UTC(),
		Sources: []source.Report{{Source: "apt", State: protocol.SourceUpdatesAvailable, Pending: 3, Security: 1}},
		Reboot:  &source.Reboot{Checked: []string{"kernel"}}}
}

func (f *fakeChecker) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

type testHub struct {
	srv    *hub.Server
	reg    *hub.Registry
	key    *identity.Identity
	url    string
	caFile string
	ts     *httptest.Server
}

func startHub(t *testing.T, maxPending int) *testHub {
	t.Helper()
	return startHubWith(t, maxPending, nil)
}

// startHubWith: the same, with the server's listener wrapped (wrap may be nil).
func startHubWith(t *testing.T, maxPending int, wrap func(net.Listener) net.Listener) *testHub {
	t.Helper()
	dir := t.TempDir()
	key, _, err := identity.LoadOrCreateAt(filepath.Join(dir, "hub.key"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := hub.OpenRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := hub.OpenEvents(filepath.Join(dir, "events.jsonl"), time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	clients, err := hub.OpenClientRegistry(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := hub.NewServer(reg, clients, events, key, maxPending, quiet)
	ts := httptest.NewUnstartedServer(srv.Handler())
	if wrap != nil {
		ts.Listener = wrap(ts.Listener)
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &testHub{srv: srv, reg: reg, key: key, caFile: caFile, ts: ts,
		url: "wss" + strings.TrimPrefix(ts.URL, "https") + wire.TunnelPath}
}

func (h *testHub) client(t *testing.T, node string, checker Checker) *Client {
	t.Helper()
	dir := t.TempDir()
	cfg, issues := policy.Parse([]byte("node: " + node + "\nstate_dir: " + dir + "\nhub: {url: '" + h.url +
		"', public_key: " + h.key.PublicB64() + ", ca_file: '" + h.caFile + "'}\n"))
	if len(issues) > 0 {
		t.Fatalf("agent config: %v", issues)
	}
	id, _, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(cfg, id, "test", facts.Host{OS: "TestOS"}, checker, quiet)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func state(reg *hub.Registry, node string) wire.NodeState {
	e, _ := reg.Get(node)
	return e.State
}

// A PC about to sleep says so over the tunnel; the hub keeps it until the PC is heard from again.
func TestPowerNoticeOverTheTunnel(t *testing.T) {
	h := startHub(t, 5)
	checker := &fakeChecker{}
	c := h.client(t, "pc", checker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if c.Power(wire.PowerSleeping) {
		t.Fatal("power notice without a session")
	}
	go c.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "pc") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "pc", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { return checker.calls() == 1 })

	if !c.Power(wire.PowerSleeping) {
		t.Fatal("power notice not sent")
	}
	waitFor(t, "hub knows it sleeps", func() bool { e, _ := h.reg.Get("pc"); return e.Power == wire.PowerSleeping })
	c.SetCheckMinInterval(0)
	jctx, jcancel := context.WithTimeout(ctx, 5*time.Second)
	defer jcancel()
	if _, code := h.srv.SendJob(jctx, "pc", protocol.VerbCheck, ""); code != hub.AdminOK {
		t.Fatalf("check: %s", code)
	}
	waitFor(t, "awake after its report", func() bool { e, _ := h.reg.Get("pc"); return e.Power == "" })
}

func TestEndToEndApprovalReportAndCheckJob(t *testing.T) {
	h := startHub(t, 5)
	checker := &fakeChecker{}
	c := h.client(t, "host-a", checker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, "pending registration", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
	if checker.calls() != 0 {
		t.Fatal("agent checked before it was approved")
	}
	if _, code := h.srv.SendJob(ctx, "host-a", protocol.VerbCheck, ""); code != hub.AdminNotApproved {
		t.Fatalf("job to pending host: %s", code)
	}
	if code := h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "host-a", Fingerprint: strings.Repeat("0", 64)}).Code; code != hub.AdminFingerprintMismatch {
		t.Fatalf("approve with wrong fingerprint: %s", code)
	}
	if code := h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "host-a", Fingerprint: c.ID.Fingerprint()}).Code; code != hub.AdminOK {
		t.Fatalf("approve: %s", code)
	}
	waitFor(t, "first report after approval", func() bool {
		e, _ := h.reg.Get("host-a")
		return e.LastReport != nil && checker.calls() == 1
	})

	jctx, jcancel := context.WithTimeout(ctx, 5*time.Second)
	defer jcancel()
	st, code := h.srv.SendJob(jctx, "host-a", protocol.VerbCheck, "")
	if code != hub.AdminOK || st.Code != protocol.CodeRejectedRateLimited {
		t.Fatalf("check right after the first one: %s / %+v, want rejected_rate_limited", code, st)
	}
	c.SetCheckMinInterval(0)
	st, code = h.srv.SendJob(jctx, "host-a", protocol.VerbCheck, "")
	if code != hub.AdminOK || st.Code != protocol.CodeOK || checker.calls() != 2 {
		t.Fatalf("check job: %s / %+v, checks %d", code, st, checker.calls())
	}
	if _, code := h.srv.SendJob(jctx, "host-a", protocol.VerbInstall, "security"); code != hub.AdminVerbUnsupported {
		t.Fatalf("install to an agent that cannot install: %s", code)
	}

	nodes := h.srv.Admin(hub.AdminRequest{Cmd: "list"}).Nodes
	if len(nodes) != 1 || !nodes[0].Online || nodes[0].Pending == nil || *nodes[0].Pending != 3 ||
		nodes[0].Reboot == nil || *nodes[0].Reboot || nodes[0].OS != "TestOS" {
		t.Fatalf("list: %+v", nodes)
	}

	// A second agent with another key under the same name is refused; the registered key stays.
	other := h.client(t, "host-a", &fakeChecker{})
	if end := other.session(ctx); end.refused != wire.ReasonKeyMismatch {
		t.Fatalf("same node, other key: %+v", end)
	}
	if e, _ := h.reg.Get("host-a"); e.Fingerprint != c.ID.Fingerprint() || e.State != wire.StateApproved {
		t.Fatalf("register changed by an impostor: %+v", e)
	}

	// Rejecting closes the connection and keeps the host out.
	if code := h.srv.Admin(hub.AdminRequest{Cmd: "reject", Node: "host-a"}).Code; code != hub.AdminOK {
		t.Fatalf("reject: %s", code)
	}
	waitFor(t, "connection closed after reject", func() bool { online, _ := h.srv.Online("host-a"); return !online })
	if end := c.session(ctx); end.refused != wire.ReasonRejected {
		t.Fatalf("rejected host reconnecting: %+v", end)
	}
	// Forgetting lets it start over as pending (Run itself waits refusedRetry after a refusal, so connect directly).
	if code := h.srv.Admin(hub.AdminRequest{Cmd: "forget", Node: "host-a"}).Code; code != hub.AdminOK {
		t.Fatalf("forget: %s", code)
	}
	go c.session(ctx)
	waitFor(t, "pending again after forget", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
}

func TestProbeStages(t *testing.T) {
	h := startHub(t, 5)
	ctx := context.Background()
	c := h.client(t, "host-a", &fakeChecker{})
	if r := Probe(ctx, c.Cfg); r.Failed != "" || len(r.Chain) == 0 || len(r.Addresses) == 0 {
		t.Fatalf("reachable hub: %+v", r)
	}
	if _, known := h.reg.Get("host-a"); known {
		t.Fatal("the probe logged in")
	}

	// httptest gives every server the same built-in certificate, so a second test hub would not do: a CA of our own.
	wrongCA := c.Cfg
	wrongCA.Hub.CAFile = foreignCA(t)
	wrongPath := c.Cfg
	wrongPath.Hub.URL = strings.TrimSuffix(c.Cfg.Hub.URL, wire.TunnelPath) + "/nope"
	closed := c.Cfg
	closed.Hub.URL = "wss://127.0.0.1:1" + wire.TunnelPath
	unknown := c.Cfg
	unknown.Hub.URL = "wss://vordr-hub.invalid" + wire.TunnelPath
	noKey := c.Cfg
	noKey.Hub.PublicKey = ""
	for name, tc := range map[string]struct {
		cfg  policy.Config
		want ProbeStage
	}{
		"untrusted certificate": {wrongCA, StageTLS},
		"wrong path":            {wrongPath, StageWebSocket},
		"nothing listening":     {closed, StageConnect},
		"name does not resolve": {unknown, StageDNS},
		"no hub key":            {noKey, StageConfig},
	} {
		if r := Probe(ctx, tc.cfg); r.Failed != tc.want {
			t.Errorf("%s: %+v, want stage %s", name, r, tc.want)
		}
	}
}

// foreignCA writes a self-signed certificate that has nothing to do with the test hub's.
func foreignCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "foreign test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "foreign.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTooManyPending(t *testing.T) {
	h := startHub(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := h.client(t, "host-a", &fakeChecker{})
	go first.Run(ctx)
	waitFor(t, "first pending", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
	if end := h.client(t, "host-b", &fakeChecker{}).session(ctx); end.refused != wire.ReasonTooManyPending {
		t.Fatalf("second unknown host: %+v", end)
	}
	if _, known := h.reg.Get("host-b"); known {
		t.Fatal("refused host was stored")
	}
}

func dialRaw(t *testing.T, h *testHub, header http.Header) (*wire.Conn, *http.Response, error) {
	t.Helper()
	tlsCfg, err := TLSConfig(h.caFile)
	if err != nil {
		t.Fatal(err)
	}
	d := websocket.Dialer{TLSClientConfig: tlsCfg}
	ws, resp, err := d.Dial(h.url, header)
	if err != nil {
		return nil, resp, err
	}
	return wire.NewConn(ws), resp, nil
}

func TestHubRefusesBrowserOrigin(t *testing.T) {
	h := startHub(t, 5)
	_, resp, err := dialRaw(t, h, http.Header{"Origin": {"https://evil.example"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("browser origin accepted: err=%v resp=%v", err, resp)
	}
}

func TestHubRefusesBadHelloSignature(t *testing.T) {
	h := startHub(t, 5)
	conn, _, err := dialRaw(t, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := conn.Receive(time.Second); err != nil {
		t.Fatal(err)
	}
	id, _, _ := identity.LoadOrCreate(t.TempDir())
	// Signed over a nonce of our choosing instead of the hub's challenge: a replayed or forged proof.
	hello := wire.Hello{T: wire.THello, Node: "host-x", PublicKey: id.Public(),
		Signature: id.Sign(wire.HelloSigned(make([]byte, wire.NonceSize), "host-x", id.Public()))}
	if err := conn.Send(hello); err != nil {
		t.Fatal(err)
	}
	typ, raw, err := conn.Receive(time.Second)
	if err != nil || typ != wire.TStatus || !strings.Contains(string(raw), string(wire.ReasonBadHello)) {
		t.Fatalf("got %q %s %v", typ, raw, err)
	}
	if _, known := h.reg.Get("host-x"); known {
		t.Fatal("host with a bad hello was stored")
	}
}

func TestHandleJob(t *testing.T) {
	h := startHub(t, 5)
	checker := &fakeChecker{}
	c := h.client(t, "host-a", checker)
	foreign, _, _ := identity.LoadOrCreate(t.TempDir())
	var sent []any
	send := func(v any) error { sent = append(sent, v); return nil }
	last := func() wire.JobStatus {
		st, _ := sent[len(sent)-1].(wire.JobStatus)
		return st
	}
	now := time.Now()
	job := func(id string, verb protocol.Verb, scope string) protocol.Job {
		return protocol.Job{ID: id, Node: "host-a", Verb: verb, Scope: scope, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	}
	signed := func(j protocol.Job, by *identity.Identity) protocol.SignedJob {
		sj, err := protocol.SignJob(j, by.Sign)
		if err != nil {
			t.Fatal(err)
		}
		return sj
	}

	c.handleJob(context.Background(), signed(job("j1", protocol.VerbCheck, ""), foreign), send)
	if st := last(); st.Code != protocol.CodeRejectedInvalidJob || st.Reason != protocol.InvalidBadSignature || st.JobID != "j1" {
		t.Fatalf("job from a foreign key: %+v", st)
	}
	c.handleJob(context.Background(), signed(job("j2", protocol.VerbInstall, "security"), h.key), send)
	if st := last(); st.Code != protocol.CodeRejectedUnsupported {
		t.Fatalf("install: %+v", st)
	}
	c.handleJob(context.Background(), signed(job("j3", protocol.VerbCheck, ""), h.key), send)
	if st := last(); st.Code != protocol.CodeOK || checker.calls() != 1 {
		t.Fatalf("check: %+v, checks %d", st, checker.calls())
	}
	if _, isReport := sent[len(sent)-2].(wire.Report); !isReport {
		t.Fatalf("no report before the result: %#v", sent[len(sent)-2])
	}
	c.handleJob(context.Background(), signed(job("j3", protocol.VerbCheck, ""), h.key), send)
	if st := last(); st.Code != protocol.CodeRejectedInvalidJob || st.Reason != protocol.InvalidReplayed {
		t.Fatalf("replayed job: %+v", st)
	}
}
