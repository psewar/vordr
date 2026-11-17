package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/wire"
)

// The hub signs its challenge and the agent checks hub.public_key with it, in hub-check and before every login
// (ADR-0170).

// otherKey is a valid hub key that is not the test hub's: agent.yaml with a key copied from the wrong place.
func otherKey(t *testing.T) string {
	t.Helper()
	id, _, err := identity.LoadOrCreateAt(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	return id.PublicB64()
}

// signingHub answers the tunnel with a challenge whose hub_sig comes from sign (nil: none, as a hub before ADR-0170)
// and reports every hello it gets, and the close code of every connection that ends without one.
type signingHub struct {
	*testHub
	hello  chan struct{}
	closed chan int
}

func startSigningHub(t *testing.T, sign func(key *identity.Identity, nonce, agentNonce []byte) []byte) *signingHub {
	t.Helper()
	key, _, err := identity.LoadOrCreateAt(filepath.Join(t.TempDir(), "hub.key"))
	if err != nil {
		t.Fatal(err)
	}
	s := &signingHub{hello: make(chan struct{}, 4), closed: make(chan int, 4)}
	up := websocket.Upgrader{}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentNonce, _ := wire.AgentNonce(r.Header)
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := wire.NewConn(ws)
		defer conn.Close()
		nonce := bytes.Repeat([]byte{1}, wire.NonceSize)
		ch := wire.Challenge{T: wire.TChallenge, Nonce: nonce}
		if sign != nil {
			ch.HubSig = sign(key, nonce, agentNonce)
		}
		if conn.Send(ch) != nil {
			return
		}
		typ, _, err := conn.Receive(5 * time.Second)
		if err != nil {
			select {
			case s.closed <- wire.CloseCode(err):
			default:
			}
			return
		}
		if typ == wire.THello {
			s.hello <- struct{}{}
			_ = conn.Send(wire.Status{T: wire.TStatus, State: wire.StatePendingApproval})
			for {
				if _, _, err := conn.Receive(5 * time.Second); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(ts.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	s.testHub = &testHub{key: key, caFile: caFile, ts: ts, url: "wss" + strings.TrimPrefix(ts.URL, "https") + wire.TunnelPath}
	return s
}

// signsUnless signs correctly until unsigned is set, then not at all: the same hub.url, first a hub that signs, then
// one that leaves the signature out.
func signsUnless(unsigned *atomic.Bool) func(key *identity.Identity, nonce, agentNonce []byte) []byte {
	return func(key *identity.Identity, nonce, agentNonce []byte) []byte {
		if unsigned.Load() {
			return nil
		}
		return key.Sign(wire.ChallengeSigned(nonce, agentNonce))
	}
}

// loginOnce runs one session until the hub has the hello, then ends it.
func loginOnce(t *testing.T, c *Client, s *signingHub) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan sessionEnd, 1)
	go func() { done <- c.session(ctx) }()
	select {
	case <-s.hello:
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	cancel()
	<-done
}

// refusals: the hub's agent_refused_hub_key events, as hub_key values.
func refusals(t *testing.T, h *testHub) []string {
	t.Helper()
	raw, _, err := h.events.Read(hub.EventFilter{Event: "agent_refused_hub_key"})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range raw {
		var e struct {
			HubKey string `json:"hub_key"`
			Remote string `json:"remote"`
		}
		if json.Unmarshal(r, &e) != nil || e.Remote == "" {
			t.Fatalf("event: %s", r)
		}
		out = append(out, e.HubKey)
	}
	return out
}

// hub-check against the real hub: the pinned key matches; another key fails at stage hub_key and the hub logs the
// refusal. Neither logs in.
func TestProbeChecksTheHubKey(t *testing.T) {
	h := startHub(t, 5)
	ctx := context.Background()
	c := h.client(t, "host-a", &fakeChecker{})
	if r := Probe(ctx, c.Cfg); r.Failed != "" || r.HubKey != wire.HubKeyMatches {
		t.Fatalf("pinned key: %+v", r)
	}
	wrong := c.Cfg
	wrong.Hub.PublicKey = otherKey(t)
	if r := Probe(ctx, wrong); r.Failed != StageHubKey || r.HubKey != wire.HubKeyMismatch {
		t.Fatalf("another key: %+v", r)
	}
	waitFor(t, "the refusal at the hub", func() bool { return len(refusals(t, h)) == 1 })
	if got := refusals(t, h); got[0] != string(wire.HubKeyMismatch) {
		t.Fatalf("refusals: %v", got)
	}
	if _, known := h.reg.Get("host-a"); known {
		t.Fatal("the probe logged in")
	}
	if _, err := os.Stat(hubSignedPath(c.Cfg.StateDir)); err == nil {
		t.Fatal("hub-check wrote to state_dir")
	}
}

// A hub before ADR-0170 does not sign: every stage passes and the key is reported as not checked.
func TestProbeWithAnOlderHub(t *testing.T) {
	o := startSigningHub(t, nil)
	if r := Probe(context.Background(), o.client(t, "host-a", &fakeChecker{}).Cfg); r.Failed != "" || r.HubKey != wire.HubKeyUnsigned {
		t.Fatalf("older hub: %+v", r)
	}
}

// A signature that is not the hub key's over this connection fails at stage hub_key: damaged on the way, or recorded
// on another connection and played back.
func TestProbeRefusesAForeignSignature(t *testing.T) {
	for name, sign := range map[string]func(key *identity.Identity, nonce, agentNonce []byte) []byte{
		"tampered": func(key *identity.Identity, nonce, agentNonce []byte) []byte {
			sig := key.Sign(wire.ChallengeSigned(nonce, agentNonce))
			sig[0] ^= 0x80
			return sig
		},
		"replayed": func(key *identity.Identity, nonce, _ []byte) []byte {
			recorded, _ := wire.NewChallengeNonce()
			return key.Sign(wire.ChallengeSigned(nonce, recorded))
		},
	} {
		s := startSigningHub(t, sign)
		if r := Probe(context.Background(), s.client(t, "host-a", &fakeChecker{}).Cfg); r.Failed != StageHubKey ||
			r.HubKey != wire.HubKeyMismatch {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

// syncBuffer collects the agent's log lines across goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// With the pinned key the agent logs in as before and holds the fact.
func TestLoginWithThePinnedHubKey(t *testing.T) {
	h := startHub(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := h.client(t, "host-a", &fakeChecker{})
	go c.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "host-a") == wire.StatePendingApproval })
	if got := c.hubKeyState(); got != wire.HubKeyMatches {
		t.Fatalf("hub key: %s", got)
	}
}

// The agent does not log in to a hub that signs with another key than hub.public_key: no hello, nothing about the
// host reaches it, the hub never hears of the host. It ends with the close code of the refusal, which the hub logs, and
// says why and asks again only after refusedRetry.
func TestNoLoginToAHubWithAnotherKey(t *testing.T) {
	h := startHub(t, 5)
	c := h.client(t, "host-a", &fakeChecker{})
	c.Cfg.Hub.PublicKey = otherKey(t)
	var logged syncBuffer
	c.Log = log.New(&logged, "", 0)
	// Bounded: an agent that logged in after all would hold the session until the context ends.
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	if end := c.session(sctx); end.hubKey != wire.HubKeyMismatch || end.established || end.err != nil {
		t.Fatalf("session: %+v", end)
	}
	if _, known := h.reg.Get("host-a"); known {
		t.Fatal("the agent sent its hello to a hub with another key")
	}
	waitFor(t, "the refusal at the hub", func() bool { return len(refusals(t, h)) == 1 })
	if got := refusals(t, h); got[0] != string(wire.HubKeyMismatch) {
		t.Fatalf("refusals: %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	waitFor(t, "the refusal in the log", func() bool { return strings.Contains(logged.String(), "hub_key mismatch") })
	cancel()
	if !strings.Contains(logged.String(), "asking again in "+refusedRetry.String()) {
		t.Errorf("log: %s", logged.String())
	}
	if _, known := h.reg.Get("host-a"); known {
		t.Fatal("registered after the refusal")
	}
}

// A damaged signature counts as another key: no hello, and the close code says so.
func TestNoLoginWithATamperedSignature(t *testing.T) {
	s := startSigningHub(t, func(key *identity.Identity, nonce, agentNonce []byte) []byte {
		sig := key.Sign(wire.ChallengeSigned(nonce, agentNonce))
		sig[len(sig)-1] ^= 0x01
		return sig
	})
	c := s.client(t, "host-a", &fakeChecker{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // bounded, as above
	defer cancel()
	if end := c.session(ctx); end.hubKey != wire.HubKeyMismatch {
		t.Fatalf("session: %+v", end)
	}
	select {
	case <-s.hello:
		t.Fatal("hello sent despite the damaged signature")
	case code := <-s.closed:
		if code != wire.CloseHubKeyMismatch {
			t.Fatalf("close code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection did not end")
	}
}

// A new agent with a hub before ADR-0170 works as before: it logs in, and the key counts as not checked - a fact,
// no refusal. Nothing is remembered: only a hub that signs is.
func TestLoginWithAnOlderHubThatDoesNotSign(t *testing.T) {
	o := startSigningHub(t, nil)
	c := o.client(t, "host-a", &fakeChecker{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	select {
	case <-o.hello:
	case <-time.After(5 * time.Second):
		t.Fatal("no hello to the older hub")
	}
	if got := c.hubKeyState(); got != wire.HubKeyUnsigned {
		t.Fatalf("hub key: %s", got)
	}
	if got := readHubSigned(c.Cfg); got != HubSignedNone {
		t.Fatalf("record after an unsigned challenge: %v", got)
	}
}

// Downgrade protection: once the hub at hub.url signed with hub.public_key, the agent remembers it in state_dir, also
// across a restart. An unsigned challenge from the same hub.url is then refused like another key: no hello, the close
// code of a downgrade, and hub-check fails at stage hub_key and says where the record is.
func TestNoLoginAfterTheHubStoppedSigning(t *testing.T) {
	var unsigned atomic.Bool
	s := startSigningHub(t, signsUnless(&unsigned))
	first := s.client(t, "host-a", &fakeChecker{})
	loginOnce(t, first, s)
	if got := readHubSigned(first.Cfg); got != HubSignedSeen {
		t.Fatalf("record after matches: %v", got)
	}

	unsigned.Store(true)
	// A restart: a new client from the same agent.yaml and state_dir.
	c := NewClient(first.Cfg, first.ID, "test", first.Facts, &fakeChecker{}, quiet)
	var logged syncBuffer
	c.Log = log.New(&logged, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // bounded, as above
	defer cancel()
	if end := c.session(ctx); end.hubKey != wire.HubKeyDowngrade || end.established || end.err != nil {
		t.Fatalf("session: %+v", end)
	}
	select {
	case <-s.hello:
		t.Fatal("hello sent to a hub that stopped signing")
	case code := <-s.closed:
		if code != wire.CloseHubKeyDowngrade {
			t.Fatalf("close code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection did not end")
	}
	if r := Probe(context.Background(), c.Cfg); r.Failed != StageHubKey || r.HubKey != wire.HubKeyDowngrade ||
		!strings.Contains(r.Detail, hubSignedFile) {
		t.Fatalf("hub-check: %+v", r)
	}
	rctx, rcancel := context.WithCancel(context.Background())
	go c.Run(rctx)
	waitFor(t, "the refusal in the log", func() bool { return strings.Contains(logged.String(), "hub_key downgrade") })
	rcancel()
	if !strings.Contains(logged.String(), "asking again in "+refusedRetry.String()) ||
		!strings.Contains(logged.String(), hubSignedPath(c.Cfg.StateDir)) {
		t.Errorf("log: %s", logged.String())
	}

	// The hub signs again: matches, and the agent logs in.
	unsigned.Store(false)
	loginOnce(t, c, s)
}

// agent.yaml pointed at another hub.url starts fresh: the record is about the hub that signed, and the new one may be
// older. Its unsigned challenge passes and leaves the record as it was.
func TestAnotherHubURLStartsFresh(t *testing.T) {
	signs := startSigningHub(t, signsUnless(new(atomic.Bool)))
	c := signs.client(t, "host-a", &fakeChecker{})
	loginOnce(t, c, signs)
	before, err := os.ReadFile(hubSignedPath(c.Cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	older := startSigningHub(t, nil)
	c.Cfg.Hub.URL, c.Cfg.Hub.CAFile = older.url, older.caFile
	if got := readHubSigned(c.Cfg); got != HubSignedNone {
		t.Fatalf("record for another hub.url: %v", got)
	}
	loginOnce(t, c, older)
	if got := c.hubKeyState(); got != wire.HubKeyUnsigned {
		t.Fatalf("hub key: %s", got)
	}
	if after, _ := os.ReadFile(hubSignedPath(c.Cfg.StateDir)); !bytes.Equal(after, before) {
		t.Fatalf("record changed by an unsigned challenge: %s", after)
	}
}
