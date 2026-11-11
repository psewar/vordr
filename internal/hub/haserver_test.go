package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Home Assistant may only offer what the hub will do: for every combination of announced verbs and policy, a
// capability is set exactly when Resolve accepts the command.
func TestCapabilitiesMatchResolve(t *testing.T) {
	f := newViewFixture(t)
	all := []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall, protocol.VerbReboot, protocol.VerbAgentUpdate}
	apps := []wire.AppInfo{{ID: "gitlab", Name: "GitLab"}, {ID: "other", Name: "Other"}, {ID: "../x", Name: "Bad"}}
	policies := []wire.PolicySummary{
		{},
		{AllowedScopes: []string{"security"}, DefaultScope: "security"},
		{AllowedScopes: []string{"all"}, DefaultScope: "all"},
		{AllowedScopes: []string{"security", "all", "app:gitlab", "app:../x"}, DefaultScope: "all", RebootAllowed: true,
			AgentUpdate: true, Apps: apps},
		{DefaultScope: "security"}, // default without allowed scopes: what a broken agent might announce
		{RebootAllowed: true, AgentUpdate: true, Apps: apps},
	}
	n := 0
	for mask := 0; mask < 1<<len(all); mask++ {
		var verbs []protocol.Verb
		for i, v := range all {
			if mask&(1<<i) != 0 {
				verbs = append(verbs, v)
			}
		}
		for pi, p := range policies {
			node := fmt.Sprintf("h%d-%d", mask, pi)
			f.host(t, node, verbs, p, true)
			e, _ := f.srv.Reg.Get(node)
			c := capabilities(e)
			ok := func(cmd string) bool { _, _, code := f.srv.Resolve(node, cmd); return code == AdminOK }
			check := func(cmd string, offered bool) {
				if offered != ok(cmd) {
					t.Errorf("%s %v %+v: %q offered=%v, resolve ok=%v", node, verbs, p, cmd, offered, ok(cmd))
				}
			}
			check("check", c.Check)
			check("install", c.Install)
			// install_security has no button of its own when it is the default (the update entity installs it).
			check("install_security", c.InstallSecurity || (c.Install && c.DefaultScope == "security"))
			check("reboot", c.Reboot)
			check("agent_update", c.AgentUpdate)
			for _, a := range apps {
				check("install_app:"+a.ID, slices.Contains(c.Apps, a))
			}
			if c.Install != (c.DefaultScope != "") {
				t.Errorf("%s: default scope %q with install=%v", node, c.DefaultScope, c.Install)
			}
			if c.InstallSecurity && c.DefaultScope == "security" {
				t.Errorf("%s: a second button for what the update entity installs", node)
			}
			n++
		}
	}
	if n != 96 {
		t.Fatalf("combinations: %d", n)
	}
}

func haHello(t *testing.T, name string) (wire.HAHello, string) {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	return wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: name, PublicKey: pub, IntegrationVersion: "0.1",
		Protocol: wire.HAProtocol}, identity.Fingerprint(pub)
}

func TestClientRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	r, _ := OpenClientRegistry(path)
	now := time.Now().UTC()
	h, fp := haHello(t, "bravo")
	if st, reason, err := r.Admit(h, fp, now); err != nil || st != wire.StatePendingApproval || reason != wire.ReasonNone {
		t.Fatalf("new client: %s %s %v", st, reason, err)
	}
	other, otherFP := haHello(t, "bravo")
	if st, reason, _ := r.Admit(other, otherFP, now); st != wire.StateRejected || reason != wire.ReasonKeyMismatch {
		t.Fatalf("other key: %s %s", st, reason)
	}
	if code := r.Decide("bravo", wire.StateApproved, otherFP, now); code != AdminFingerprintMismatch {
		t.Fatalf("approve with the other key's fingerprint: %s", code)
	}
	if code := r.Decide("bravo", wire.StateApproved, fp, now); code != AdminOK {
		t.Fatalf("approve: %s", code)
	}
	r2, err := OpenClientRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := r2.Get("bravo"); !ok || c.State != wire.StateApproved || c.IntegrationVersion != "0.1" {
		t.Fatalf("after reload: %+v", c)
	}
	if code := r2.Decide("bravo", wire.StateRejected, "", now); code != AdminOK {
		t.Fatalf("reject: %s", code)
	}
	if st, reason, _ := r2.Admit(h, fp, now); st != wire.StateRejected || reason != wire.ReasonRejected {
		t.Fatalf("rejected client: %s %s", st, reason)
	}
	if code := r2.Forget("bravo"); code != AdminOK {
		t.Fatalf("forget: %s", code)
	}
	if st, _, _ := r2.Admit(other, otherFP, now); st != wire.StatePendingApproval {
		t.Fatalf("new key after forget: %s", st)
	}
	for i := 1; i < maxPendingClients; i++ {
		h, fp := haHello(t, fmt.Sprintf("c%d", i))
		r2.Admit(h, fp, now)
	}
	extra, extraFP := haHello(t, "one-too-many")
	if st, reason, _ := r2.Admit(extra, extraFP, now); st != wire.StateRejected || reason != wire.ReasonTooManyPending {
		t.Fatalf("pending limit: %s %s", st, reason)
	}
	if _, ok := r2.Get("one-too-many"); ok {
		t.Fatal("refused client stored")
	}
}

// Clients and hosts are separate: a client name is no host and the reverse, and HA clients cannot be approved
// through the host commands.
func TestClientAdminIsSeparateFromHosts(t *testing.T) {
	f := newViewFixture(t)
	hostFP := f.host(t, "same", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	h, fp := haHello(t, "same")
	f.srv.Clients.Admit(h, fp, f.now)
	if r := f.srv.Admin(AdminRequest{Cmd: "approve", Node: "same", Fingerprint: fp}); r.Code != AdminFingerprintMismatch {
		t.Fatalf("host approve with the client's fingerprint: %s", r.Code)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "approve-client", Client: "same", Fingerprint: hostFP}); r.Code != AdminFingerprintMismatch {
		t.Fatalf("client approve with the host's fingerprint: %s", r.Code)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "approve-client", Client: "same", Fingerprint: fp}); r.Code != AdminOK {
		t.Fatalf("client approve: %s", r.Code)
	}
	if e, _ := f.srv.Reg.Get("same"); e.State != wire.StatePendingApproval {
		t.Fatalf("approving the client changed the host: %s", e.State)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "approve-client", Client: "same", Fingerprint: fp}); r.Code != AdminAlready {
		t.Fatalf("approve twice: %s", r.Code)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "forget-client", Client: "nobody"}); r.Code != AdminUnknownNode {
		t.Fatalf("forget unknown: %s", r.Code)
	}
	r := f.srv.Admin(AdminRequest{Cmd: "clients"})
	if len(r.Clients) != 1 || r.Clients[0].Connected || r.Clients[0].State != wire.StateApproved {
		t.Fatalf("clients: %+v", r.Clients)
	}
}

// A command is decided on the client register as it is now: a session of a key that is no longer (or not yet)
// approved gets nothing started.
func TestCommandNeedsTheApprovedKeyNow(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	cmd := wire.Command{T: wire.TCommand, RequestID: "r1", Node: "h", Command: "check"}
	if res := f.srv.haCommand(&haSession{name: "bravo", fingerprint: fp}, cmd); res.Code != string(AdminClientNotApproved) {
		t.Fatalf("pending client: %+v", res)
	}
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	if res := f.srv.haCommand(&haSession{name: "bravo", fingerprint: "SHA256:old-key"}, cmd); res.Code != string(AdminClientNotApproved) {
		t.Fatalf("session of another key: %+v", res)
	}
	if res := f.srv.haCommand(&haSession{name: "bravo", fingerprint: fp}, cmd); res.Code != string(AdminNotConnected) || res.RequestID != "r1" {
		t.Fatalf("approved client, host not connected: %+v", res)
	}
	if e, _ := f.srv.Reg.Get("h"); e.LastRun != nil {
		t.Fatalf("a refused command recorded a run: %+v", e.LastRun)
	}
}
