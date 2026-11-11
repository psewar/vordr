package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// tunnelAgent logs an approved host in over a real tunnel and returns its end of the connection.
func tunnelAgent(t *testing.T, f *viewFixture, ts *httptest.Server, node string, verbs ...protocol.Verb) *wire.Conn {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fp := identity.Fingerprint(pub)
	h := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "1.0", Verbs: verbs}
	if _, _, err := f.srv.Reg.Admit(h, fp, f.now, 5); err != nil {
		t.Fatal(err)
	}
	if code := f.srv.Reg.Decide(node, wire.StateApproved, fp, f.now); code != AdminOK {
		t.Fatal(code)
	}
	conn := dialTunnel(t, ts)
	_, raw, err := conn.Receive(5 * time.Second)
	var ch wire.Challenge
	if err != nil || json.Unmarshal(raw, &ch) != nil {
		t.Fatalf("challenge: %v", err)
	}
	h.Signature = ed25519.Sign(priv, wire.HelloSigned(ch.Nonce, node, pub))
	if err := conn.Send(h); err != nil {
		t.Fatal(err)
	}
	_, raw, err = conn.Receive(5 * time.Second)
	var st wire.Status
	if err != nil || json.Unmarshal(raw, &st) != nil || st.State != wire.StateApproved {
		t.Fatalf("status: %v %+v", err, st)
	}
	return conn
}

func dialTunnel(t *testing.T, ts *httptest.Server) *wire.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+wire.TunnelPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := wire.NewConn(ws)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// holdSaves makes the register's next save stop in its sync, as on a slow disk, until the returned release is called;
// started is closed once that save is in its sync.
func holdSaves(t *testing.T, f *viewFixture) (arm func(), started <-chan struct{}, release func()) {
	t.Helper()
	var armed atomic.Bool
	in := make(chan struct{})
	out := make(chan struct{})
	var once sync.Once
	old := syncFile
	syncFile = func(file *os.File) error {
		if armed.Load() && strings.HasSuffix(file.Name(), "r.json.tmp") {
			once.Do(func() {
				close(in)
				<-out
			})
		}
		return old(file)
	}
	release = sync.OnceFunc(func() { close(out) })
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.srv.Close(ctx) // nothing of the hub uses syncFile any more when it is put back
		syncFile = old
	})
	return func() { armed.Store(true) }, in, release
}

// A stopping hub (or a test that removes the hub's directory next) must not end while a session's message is still
// being stored: Close waits for it (ADR-0142).
func TestCloseWaitsForTheSaveASessionStarted(t *testing.T) {
	f := newViewFixture(t)
	arm, started, release := holdSaves(t, f)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	agent := tunnelAgent(t, f, ts, "h", protocol.VerbCheck)
	arm()
	if err := agent.Send(wire.Power{T: wire.TPower, State: wire.PowerSleeping}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the power notice was not stored")
	}
	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- f.srv.Close(ctx)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while the session's save was still in its sync")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	select {
	case done := <-closed:
		if !done {
			t.Fatal("Close: the work did not end")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the save")
	}
	if e, _ := f.srv.Reg.Get("h"); e.Power != wire.PowerSleeping {
		t.Fatalf("the power notice is not in the register: %+v", e)
	}
	if online, _ := f.srv.Online("h"); online {
		t.Fatal("the session is still there after Close")
	}
}

// A job whose result is still out when the hub stops: the wait ends with hub_stopping and the run stays open for the
// agent's next hello, instead of Close sitting out the wait or recording a result nobody knows.
func TestCloseEndsTheWaitForAJobWithoutARecord(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	tunnelAgent(t, f, ts, "h", protocol.VerbCheck)
	job, code := f.srv.StartJob("h", protocol.VerbCheck, "", ByAdmin)
	if code != AdminOK {
		t.Fatalf("start: %s", code)
	}
	waited := make(chan AdminCode, 1)
	go func() {
		_, c := f.srv.WaitJob(context.Background(), job) // no deadline of its own: only Close can end it
		waited <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !f.srv.Close(ctx) {
		t.Fatal("Close: the work did not end")
	}
	select {
	case c := <-waited:
		if c != AdminHubStopping {
			t.Fatalf("wait ended with %s", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait for the job did not end with Close")
	}
	if e, _ := f.srv.Reg.Get("h"); e.LastRun == nil || e.LastRun.JobID != job.ID || e.LastRun.Code != "" ||
		e.LastRun.Finished != nil {
		t.Fatalf("the run is not left open: %+v", e.LastRun)
	}
}

// After Close the hub serves no new connection and starts no new work.
func TestAfterCloseNothingNewStarts(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !f.srv.Close(ctx) {
		t.Fatal("Close of an idle hub did not end")
	}
	if f.srv.spawn(func() { t.Error("work started after Close") }) {
		t.Fatal("spawn accepted work after Close")
	}
	conn := dialTunnel(t, ts)
	if _, _, err := conn.Receive(2 * time.Second); err == nil {
		t.Fatal("a connection after Close got a challenge")
	}
	if !f.srv.Close(ctx) {
		t.Fatal("a second Close did not end")
	}
}
