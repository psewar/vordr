package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/wire"
)

func hello(t *testing.T, node string) (wire.Hello, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return wire.Hello{Node: node, PublicKey: pub, AgentVersion: "1"}, identity.Fingerprint(pub)
}

func TestRegistryAdmitAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	reg, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	h, fp := hello(t, "host-a")
	if st, r, err := reg.Admit(h, fp, now, 5); st != wire.StatePendingApproval || r != wire.ReasonNone || err != nil {
		t.Fatalf("new host: %s %s %v", st, r, err)
	}
	other, otherFP := hello(t, "host-a")
	if st, r, _ := reg.Admit(other, otherFP, now, 5); st != wire.StateRejected || r != wire.ReasonKeyMismatch {
		t.Fatalf("other key: %s %s", st, r)
	}
	if code := reg.Decide("host-a", wire.StateApproved, otherFP, now); code != AdminFingerprintMismatch {
		t.Fatalf("approve with the impostor's fingerprint: %s", code)
	}
	if code := reg.Decide("host-a", wire.StateApproved, fp, now); code != AdminOK {
		t.Fatalf("approve: %s", code)
	}
	if code := reg.Decide("host-a", wire.StateApproved, fp, now); code != AdminAlready {
		t.Fatalf("approve twice: %s", code)
	}
	if code := reg.Decide("nobody", wire.StateRejected, "", now); code != AdminUnknownNode {
		t.Fatalf("unknown: %s", code)
	}

	again, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := again.Get("host-a"); !ok || e.State != wire.StateApproved || e.Fingerprint != fp {
		t.Fatalf("after reload: %+v", e)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("register mode %v", info.Mode().Perm())
	}
}

func TestRegistryRejectedStaysRejected(t *testing.T) {
	reg, _ := OpenRegistry(filepath.Join(t.TempDir(), "r.json"))
	h, fp := hello(t, "host-a")
	reg.Admit(h, fp, time.Now(), 5)
	reg.Decide("host-a", wire.StateRejected, "", time.Now())
	if st, r, _ := reg.Admit(h, fp, time.Now(), 5); st != wire.StateRejected || r != wire.ReasonRejected {
		t.Fatalf("rejected host: %s %s", st, r)
	}
}

func TestParseConfig(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cert, []byte("x"), 0o600)
	os.WriteFile(key, []byte("x"), 0o600)
	cfg, issues := ParseConfig([]byte("tls_cert: " + cert + "\ntls_key: " + key + "\n"))
	if len(issues) > 0 || cfg.Listen != ":8443" || cfg.MaxPending != 20 {
		t.Fatalf("%+v %v", cfg, issues)
	}
	if _, issues := ParseConfig([]byte("listen: ':1'\n")); len(issues) != 2 {
		t.Fatalf("without TLS: %v", issues)
	}
	if _, issues := ParseConfig([]byte("tls_crt: x\n")); len(issues) != 1 {
		t.Fatalf("typo: %v", issues)
	}
}

func TestAdminSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "admin.sock")
	reg, _ := OpenRegistry(filepath.Join(dir, "r.json"))
	events, _ := OpenEvents(filepath.Join(dir, "e.jsonl"), time.Now, nil)
	defer events.Close()
	key, _, _ := identity.LoadOrCreateAt(filepath.Join(dir, "hub.key"))
	clients, _ := OpenClientRegistry(filepath.Join(dir, "c.json"))
	srv := NewServer(reg, clients, events, key, 5, log.New(io.Discard, "", 0))
	h, fp := hello(t, "host-a")
	reg.Admit(h, fp, time.Now(), 5)

	ln, err := ListenAdmin(path)
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeAdmin(ln)
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", info.Mode().Perm())
	}
	if _, err := ListenAdmin(path); err == nil {
		t.Fatal("second hub took over a live socket")
	}
	resp, err := AdminCall(path, AdminRequest{Cmd: "approve", Node: "host-a", Fingerprint: fp}, time.Second)
	if err != nil || resp.Code != AdminOK {
		t.Fatalf("approve over the socket: %+v %v", resp, err)
	}
	resp, _ = AdminCall(path, AdminRequest{Cmd: "list"}, time.Second)
	if len(resp.Nodes) != 1 || resp.Nodes[0].State != wire.StateApproved || resp.Nodes[0].Online || resp.Nodes[0].Reboot != nil {
		t.Fatalf("list: %+v", resp)
	}
	if resp, _ := AdminCall(path, AdminRequest{Cmd: "shell"}, time.Second); resp.Code != AdminBadRequest {
		t.Fatalf("unknown command: %+v", resp)
	}
	ln.Close()

	// A socket file left behind by a dead hub is replaced.
	stale, err := net.Listen("unix", filepath.Join(dir, "stale.sock"))
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	ln2, err := ListenAdmin(filepath.Join(dir, "stale.sock"))
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	ln2.Close()
}
