package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// A decision that comes while a host logs in - after the hub admitted its hello, before it registered the session -
// finds no session to tell (Notify). Here the hub's hello event waits (the event log's lock is held, as on a slow disk)
// while the host is approved or rejected, as the approve and reject commands do it. The agent must still get the new
// status right after the pending one, not stay pending until its next connection.
func TestADecisionDuringTheLoginReachesTheAgent(t *testing.T) {
	for _, decided := range []wire.NodeState{wire.StateApproved, wire.StateRejected} {
		t.Run(string(decided), func(t *testing.T) {
			f := newViewFixture(t)
			ts := httptest.NewServer(f.srv.Handler())
			t.Cleanup(ts.Close)
			const node = "h"
			pub, priv, _ := ed25519.GenerateKey(rand.Reader)
			conn := dialTunnel(t, ts)
			_, raw, err := conn.Receive(5 * time.Second)
			var ch wire.Challenge
			if err != nil || json.Unmarshal(raw, &ch) != nil {
				t.Fatalf("challenge: %v", err)
			}

			f.srv.Events.mu.Lock() // the login stops at its hello event: admitted, no status sent, no session registered
			unlock := sync.OnceFunc(f.srv.Events.mu.Unlock)
			defer unlock()
			h := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "1.0",
				Verbs:     []protocol.Verb{protocol.VerbCheck},
				Signature: ed25519.Sign(priv, wire.HelloSigned(ch.Nonce, node, pub))}
			if err := conn.Send(h); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				if e, ok := f.srv.Reg.Get(node); ok && e.State == wire.StatePendingApproval {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the hub did not admit the hello")
				}
			}
			if code := f.srv.Reg.Decide(node, decided, identity.Fingerprint(pub), f.now); code != AdminOK {
				t.Fatal(code)
			}
			f.srv.Notify(node) // as the approve and reject commands do; the session is not registered yet
			unlock()

			var got []wire.NodeState
			for len(got) < 2 {
				typ, raw, err := conn.Receive(3 * time.Second)
				if err != nil {
					t.Fatalf("statuses %v, then: %v", got, err)
				}
				var st wire.Status
				if typ == wire.TStatus && json.Unmarshal(raw, &st) == nil {
					got = append(got, st.State)
				}
			}
			if got[0] != wire.StatePendingApproval || got[1] != decided {
				t.Fatalf("statuses: %v", got)
			}
			if decided == wire.StateRejected {
				if _, _, err := conn.Receive(3 * time.Second); err == nil {
					t.Fatal("the connection of a rejected host stays open")
				}
			}
		})
	}
}
