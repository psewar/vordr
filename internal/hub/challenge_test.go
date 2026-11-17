package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/wire"
)

// challengeFor opens the tunnel with the given upgrade header and returns the hub's challenge.
func challengeFor(t *testing.T, ts *httptest.Server, header http.Header) wire.Challenge {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+wire.TunnelPath, header)
	if err != nil {
		t.Fatal(err)
	}
	conn := wire.NewConn(ws)
	defer conn.Close()
	typ, raw, err := conn.Receive(5 * time.Second)
	var ch wire.Challenge
	if err != nil || typ != wire.TChallenge || json.Unmarshal(raw, &ch) != nil {
		t.Fatalf("challenge: %q %s %v", typ, raw, err)
	}
	return ch
}

// The hub signs its challenge with its job key over its nonce and the agent's (ADR-0170); every connection gets a
// signature of its own.
func TestHubSignsTheChallengeForTheAgentsNonce(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	nonce, header := wire.NewChallengeNonce()
	ch := challengeFor(t, ts, header)
	if got := ch.CheckHubKey(nonce, f.srv.Key.Public()); got != wire.HubKeyMatches {
		t.Fatalf("hub key: %s (%+v)", got, ch)
	}
	again := challengeFor(t, ts, header)
	if string(again.Nonce) == string(ch.Nonce) || string(again.HubSig) == string(ch.HubSig) {
		t.Fatal("two connections, the same challenge")
	}
}

// An agent before ADR-0170 sends no nonce, or one the hub cannot use: the challenge stays as before, unsigned.
func TestHubDoesNotSignWithoutTheAgentsNonce(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	for name, header := range map[string]http.Header{
		"older agent": nil,
		"not base64":  {wire.ChallengeNonceHeader: {"%%%"}},
		"too short":   {wire.ChallengeNonceHeader: {"AAAA"}},
	} {
		ch := challengeFor(t, ts, header)
		if len(ch.HubSig) != 0 || len(ch.Nonce) != wire.NonceSize {
			t.Errorf("%s: %+v", name, ch)
		}
	}
}

// An agent that refuses the hub's key ends with a close code instead of a hello (ADR-0170): the hub logs that as an
// event with the address and no node, so hosts going offline over a new hub key show at the hub. A connection that
// ends otherwise logs nothing.
func TestHubLogsAnAgentThatRefusesItsKey(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	refusals := func() []map[string]any {
		raw, _, err := f.srv.Events.Read(EventFilter{Event: "agent_refused_hub_key"})
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		for _, r := range raw {
			var e map[string]any
			_ = json.Unmarshal(r, &e)
			out = append(out, e)
		}
		return out
	}
	endWith := func(code int) {
		_, header := wire.NewChallengeNonce()
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+wire.TunnelPath, header)
		if err != nil {
			t.Fatal(err)
		}
		conn := wire.NewConn(ws)
		if typ, _, err := conn.Receive(5 * time.Second); err != nil || typ != wire.TChallenge {
			t.Fatalf("challenge: %q %v", typ, err)
		}
		_ = conn.CloseWith(code)
	}
	for i, tc := range []struct {
		code int
		want wire.HubKeyCheck
	}{{wire.CloseHubKeyMismatch, wire.HubKeyMismatch}, {wire.CloseHubKeyDowngrade, wire.HubKeyDowngrade}} {
		endWith(tc.code)
		deadline := time.Now().Add(5 * time.Second)
		for len(refusals()) <= i && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		got := refusals()
		if len(got) != i+1 {
			t.Fatalf("close code %d: events %v", tc.code, got)
		}
		remote, _ := got[i]["remote"].(string)
		if got[i]["hub_key"] != string(tc.want) || !strings.HasPrefix(remote, "127.0.0.1:") || got[i]["node"] != nil {
			t.Errorf("close code %d: event %v", tc.code, got[i])
		}
	}
	endWith(websocket.CloseNormalClosure)
	endWith(4003)
	time.Sleep(200 * time.Millisecond) // the hub has read both closes by now; logged, they would show
	if got := refusals(); len(got) != 2 {
		t.Fatalf("a close without a refusal code was logged: %v", got)
	}
}
