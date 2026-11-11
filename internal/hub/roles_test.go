package hub

import (
	"testing"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// What a client may start follows its role; operate (the default) may not reboot, observe may start nothing.
func TestClientRoles(t *testing.T) {
	for _, tc := range []struct {
		role string
		verb protocol.Verb
		ok   bool
	}{
		{"", protocol.VerbCheck, true}, {"", protocol.VerbInstall, true}, {"", protocol.VerbAgentUpdate, true},
		{"", protocol.VerbReboot, false}, {RoleOperate, protocol.VerbReboot, false}, {RoleAdmin, protocol.VerbReboot, true},
		{RoleObserve, protocol.VerbCheck, false}, {RoleObserve, protocol.VerbInstall, false},
	} {
		if got := RoleAllows(tc.role, tc.verb); got != tc.ok {
			t.Errorf("RoleAllows(%q, %s) = %v", tc.role, tc.verb, got)
		}
	}

	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	sess := &haSession{name: "bravo", fingerprint: fp}
	cmd := wire.Command{T: wire.TCommand, RequestID: "r1", Node: "h", Command: "check"}
	if res := f.srv.haCommand(sess, cmd); res.Code != string(AdminNotConnected) {
		t.Fatalf("operate may check (host not connected): %+v", res)
	}
	if code := f.srv.Clients.SetRole("bravo", RoleObserve); code != AdminOK {
		t.Fatalf("set role: %s", code)
	}
	if res := f.srv.haCommand(sess, cmd); res.Code != string(AdminRoleForbids) {
		t.Fatalf("observe may not check: %+v", res)
	}
	if c, _ := f.srv.Clients.Get("bravo"); c.Role != RoleObserve || f.srv.clientViews()[0].Role != RoleObserve {
		t.Fatalf("role not kept: %+v", c)
	}
	if f.srv.Clients.SetRole("bravo", "root") != AdminUnknownRole || f.srv.Clients.SetRole("nobody", RoleAdmin) != AdminUnknownNode ||
		f.srv.Clients.SetRole("bravo", RoleObserve) != AdminAlready {
		t.Fatal("SetRole refusals")
	}
	if e, _ := f.srv.Reg.Get("h"); e.LastRun != nil {
		t.Fatalf("a refused command recorded a run: %+v", e.LastRun)
	}
}
