package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// vetAtTheBounds is a vet request with every bounded field at its bound: the longest job id, the most entries, the
// longest name and version.
func vetAtTheBounds() wire.Vet {
	return wire.Vet{T: wire.TVet, JobID: strings.Repeat("a", 64), Ecosystem: string(versions.PyPI),
		Packages: withFirst(wire.MaxVetPackages, protocol.VetPackage{Name: "requests", Version: "2.32.4"},
			protocol.VetPackage{Name: textOf(1024), Version: textOf(1024)})}
}

// copyVet is a deep copy of a vet request, its bytes as they are.
func copyVet(v wire.Vet) wire.Vet {
	out := v
	out.Packages = nil
	for _, p := range v.Packages {
		p.Flagged = append([]string(nil), p.Flagged...)
		out.Packages = append(out.Packages, p)
	}
	return out
}

// A vet request is taken whole or refused whole (ADR-0196): at its bounds it is taken; one step beyond in any field -
// one entry of thousands, a field the hub fills in itself set by the sender - refuses all of it, never a part, and
// leaves the request as it came.
func TestTheHubBoundsEveryFieldOfAVetRequest(t *testing.T) {
	type c = boundCase[wire.Vet]
	refused := func(field, why string, change func(*wire.Vet)) c { return c{field, why, change, nil} }
	cases := map[string]c{
		"no job id":         refused("job_id", dropCharacters, func(v *wire.Vet) { v.JobID = "" }),
		"a job id too long": refused("job_id", dropTooLong, func(v *wire.Vet) { v.JobID += "a" }),
		"a job id a path":   refused("job_id", dropCharacters, func(v *wire.Vet) { v.JobID = "../j1" }),
		"a distribution":    refused("ecosystem", dropValue, func(v *wire.Vet) { v.Ecosystem = "Debian:12" }),
		"an ecosystem in other case": refused("ecosystem", dropValue,
			func(v *wire.Vet) { v.Ecosystem = "pypi" }),
		"no ecosystem": refused("ecosystem", dropValue, func(v *wire.Vet) { v.Ecosystem = "" }),
		"an ecosystem of a megabyte": refused("ecosystem", dropTooLong,
			func(v *wire.Vet) { v.Ecosystem = strings.Repeat("x", 1<<20) }),
		"5001 entries": refused("packages", dropTooMany, func(v *wire.Vet) {
			v.Packages = append(v.Packages, protocol.VetPackage{Name: "x", Version: "1"})
		}),
		"a name too long": refused("packages[].name", dropTooLong, func(v *wire.Vet) { v.Packages[0].Name += "x" }),
		"a name with an escape": refused("packages[].name", dropCharacters,
			func(v *wire.Vet) { v.Packages[4999].Name = "debug\x1b[2J" }),
		"a version too long": refused("packages[].version", dropTooLong,
			func(v *wire.Vet) { v.Packages[0].Version += "x" }),
		"a version with a line break": refused("packages[].version", dropCharacters,
			func(v *wire.Vet) { v.Packages[9].Version = "4.4.3\n" }),
		"a version not UTF-8": refused("packages[].version", dropCharacters,
			func(v *wire.Vet) { v.Packages[2].Version = "1.0\xff" }),
		"flagged by the sender": refused("packages[].flagged", dropValue,
			func(v *wire.Vet) { v.Packages[3].Flagged = []string{"MAL-0000-1"} }),
		"malicious by the sender": refused("packages[].malicious", dropValue,
			func(v *wire.Vet) { v.Packages[3].Malicious = true }),
	}
	checkBounds(t, vetAtTheBounds, boundVet, cases)
	for name, c := range cases {
		m := vetAtTheBounds()
		c.change(&m)
		sent := copyVet(m)
		if boundVet(&m); !reflect.DeepEqual(m, sent) {
			t.Errorf("%s: the refused request changed", name)
		}
	}
}

// What agents ask to vet is taken (ADR-0196): pip and npm plans with PEP 440's and semver's longest forms, npm's
// longest scoped names, a whole tree of the most entries a plan has, and the dependencies a refresh offers.
func TestTheHubTakesWhatAgentsAskToVet(t *testing.T) {
	tree := make([]protocol.VetPackage, wire.MaxVetPackages)
	for i := range tree {
		tree[i] = protocol.VetPackage{Name: fmt.Sprintf("@%s/%s-%d", strings.Repeat("s", 100), strings.Repeat("n", 100), i),
			Version: "1.0.0-beta.1+build." + strings.Repeat("7", 45)}
	}
	for name, req := range map[string]wire.Vet{
		"a pip plan": {T: wire.TVet, JobID: "0123456789abcdef01234567", Ecosystem: "PyPI", Packages: []protocol.VetPackage{
			{Name: "requests", Version: "2.32.4"}, {Name: "urllib3", Version: "2.5.0"},
			{Name: "zope.interface", Version: "1!7.2.post1.dev3+local.7"}, {Name: "Django", Version: "5.2rc1"}}},
		"an npm plan": {T: wire.TVet, JobID: "0123456789abcdef01234567", Ecosystem: "npm",
			Packages: []protocol.VetPackage{{Name: "@example/nodes", Version: "2.0.1"}, {Name: "debug", Version: "4.4.3"}}},
		"a whole tree":           {T: wire.TVet, JobID: "fedcba9876543210fedcba98", Ecosystem: "npm", Packages: tree},
		"nothing new to vet":     {T: wire.TVet, JobID: "fedcba9876543210fedcba98", Ecosystem: "npm", Packages: []protocol.VetPackage{}},
		"a job of the hub's own": {T: wire.TVet, JobID: "j-1", Ecosystem: "PyPI", Packages: []protocol.VetPackage{{Name: "pip", Version: "25.1"}}},
	} {
		if miss := VetNotTaken(req); miss != nil {
			t.Errorf("%s: refused for %v", name, miss)
		}
	}
}

// A request out of bounds gets a signed answer that vouches for nothing (protocol.VetOutOfBounds), however clean the
// rest of its plan; the answer echoes no job id that is out of bounds. Only for an approved host the event vet_refused
// names the field, with the key's fingerprint, at most once an hour; neither the vet event nor any event carries the
// text of a refused request.
func TestAVetRequestOutOfBoundsIsRefusedWhole(t *testing.T) {
	f := newViewFixture(t)
	clk := &clock{t: f.now}
	p := newTestPackages(t, t.TempDir(), newOSVServer(t), []string{"npm"}, clk)
	f.srv.Packages = p
	if !p.Refresh(context.Background(), versions.Npm) {
		t.Fatal("no OSV data")
	}
	fp := f.host(t, "echo", []protocol.Verb{protocol.VerbInstall}, wire.PolicySummary{}, true)
	f.host(t, "waiting", []protocol.Verb{protocol.VerbInstall}, wire.PolicySummary{}, false)
	for _, node := range []string{"echo", "waiting"} {
		f.srv.storeRun(node, Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "pkg:npm-global", Started: f.now})
	}
	verify := func(r wire.VetResult, node, jobID string) protocol.Vet {
		t.Helper()
		v, ok := protocol.VerifyVet(r.Vet, f.srv.Key.Public(), node, jobID, f.now)
		if !ok || r.JobID != jobID {
			t.Fatalf("no valid answer for %q: %+v", jobID, r)
		}
		return v
	}
	plan := []protocol.VetPackage{{Name: "debug", Version: "4.4.2"}, {Name: "ms", Version: "2.1.3"}}
	if v := verify(f.srv.vet("echo", wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: plan}), "echo",
		"j1"); v.Unavailable != "" || !v.Covers(plan) || !v.Packages[0].Malicious {
		t.Fatalf("in bounds: %+v", v)
	}
	withBad := append(append([]protocol.VetPackage{}, plan...), protocol.VetPackage{Name: "left-pad\x1b[31m",
		Version: "1.3.0"})
	for _, req := range []wire.Vet{
		{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: withBad},
		{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: []protocol.VetPackage{{Name: "ms", Version: "2.1.3",
			Flagged: []string{"none-at-all"}}}},
		{T: wire.TVet, JobID: "j1", Ecosystem: "Maven", Packages: plan},
	} {
		if v := verify(f.srv.vet("echo", req), "echo", "j1"); v.Unavailable != protocol.VetOutOfBounds ||
			len(v.Packages) != 0 || v.Ecosystem != "" || v.Covers(req.Packages) {
			t.Errorf("%+v: %+v", req, v)
		}
	}
	if v := verify(f.srv.vet("echo", wire.Vet{T: wire.TVet, JobID: "../" + strings.Repeat("j", 100), Ecosystem: "npm",
		Packages: plan}), "echo", ""); v.Unavailable != protocol.VetOutOfBounds {
		t.Errorf("job id out of bounds: %+v", v)
	}
	if v := verify(f.srv.vet("waiting", wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: withBad}),
		"waiting", "j1"); v.Unavailable != protocol.VetOutOfBounds {
		t.Errorf("waiting host: %+v", v)
	}
	f.now = f.now.Add(time.Hour)
	verify(f.srv.vet("echo", wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: withBad}), "echo", "j1")

	refused := eventsOf(t, f, "vet_refused", "echo")
	if len(refused) != 2 || refused[0]["fingerprint"] != fp || refused[0]["unlogged"] != nil ||
		!reflect.DeepEqual(refused[0]["fields"], map[string]any{"packages[].name": "characters"}) ||
		refused[1]["unlogged"] != float64(3) {
		t.Fatalf("vet_refused: %v", refused)
	}
	if got := eventsOf(t, f, "vet_refused", "waiting"); len(got) != 0 {
		t.Errorf("logged for a waiting host: %v", got)
	}
	if vets := eventLines(t, f, "vet"); len(vets) != 1 || vets[0]["job_id"] != "j1" || vets[0]["ecosystem"] != "npm" ||
		vets[0]["packages"] != float64(2) {
		t.Errorf("vet events: %v", vets)
	}
	raw, _, err := f.srv.Events.Read(EventFilter{Limit: MaxEventLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range raw {
		if s := string(l); strings.Contains(s, "left-pad") || strings.Contains(s, "jjjj") || strings.Contains(s, "Maven") ||
			strings.Contains(s, "none-at-all") {
			t.Errorf("the text of a refused request in the event log: %s", s)
		}
	}
}

// A host that is not approved has no run (ADR-0196): its request in bounds gets the signed no_running_update - even
// with a run of that id in the register - and logs nothing, as none of its messages does.
func TestAHostNotApprovedHasNoRunToVet(t *testing.T) {
	f := newViewFixture(t)
	clk := &clock{t: f.now}
	p := newTestPackages(t, t.TempDir(), newOSVServer(t), []string{"npm"}, clk)
	f.srv.Packages = p
	if !p.Refresh(context.Background(), versions.Npm) {
		t.Fatal("no OSV data")
	}
	f.host(t, "waiting", []protocol.Verb{protocol.VerbInstall}, wire.PolicySummary{}, false)
	f.srv.storeRun("waiting", Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "pkg:npm-global", Started: f.now})
	plan := []protocol.VetPackage{{Name: "debug", Version: "4.4.2"}}
	for range 3 {
		r := f.srv.vet("waiting", wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: plan})
		if v, ok := protocol.VerifyVet(r.Vet, f.srv.Key.Public(), "waiting", "j1", f.now); !ok ||
			v.Unavailable != "no_running_update" || len(v.Packages) != 0 {
			t.Fatalf("waiting host: %+v %v", v, ok)
		}
	}
	for _, event := range []string{"vet", "vet_refused"} {
		if got := eventLines(t, f, event); len(got) != 0 {
			t.Errorf("%s logged for a waiting host: %v", event, got)
		}
	}
	// Approved, the same request is vetted and logged.
	f.srv.Reg.Decide("waiting", wire.StateApproved, mustGet(t, f.srv.Reg, "waiting").Fingerprint, f.now)
	r := f.srv.vet("waiting", wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: plan})
	if v, ok := protocol.VerifyVet(r.Vet, f.srv.Key.Public(), "waiting", "j1", f.now); !ok || v.Unavailable != "" ||
		!v.Packages[0].Malicious || len(eventLines(t, f, "vet")) != 1 {
		t.Fatalf("approved: %+v", v)
	}
}

// Over a real tunnel: a vet request out of bounds is answered, and the connection stays (ADR-0196); one with more
// entries than wire.MaxVetPackages still ends it as a protocol violation, as before.
func TestARefusedVetRequestKeepsTheConnection(t *testing.T) {
	f := newViewFixture(t)
	f.srv.Packages = newTestPackages(t, t.TempDir(), newOSVServer(t), []string{"npm"}, &clock{t: f.now})
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	conn, _ := agentSession(t, f, ts, "alpha", true)
	if err := conn.Send(wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm",
		Packages: []protocol.VetPackage{{Name: "debug", Version: "4.4.3\n"}}}); err != nil {
		t.Fatal(err)
	}
	for {
		typ, raw, err := conn.Receive(5 * time.Second)
		if err != nil {
			t.Fatalf("no answer: %v", err)
		}
		if typ != wire.TVetResult {
			continue
		}
		var r wire.VetResult
		if json.Unmarshal(raw, &r) != nil {
			t.Fatal("unreadable answer")
		}
		if v, ok := protocol.VerifyVet(r.Vet, f.srv.Key.Public(), "alpha", "j1", f.now); !ok ||
			v.Unavailable != protocol.VetOutOfBounds {
			t.Fatalf("answer: %+v", v)
		}
		break
	}
	sendHandled(t, conn) // the hub answers the next message on the same connection
	if online, _ := f.srv.Online("alpha"); !online {
		t.Fatal("a refused vet request ended the connection")
	}
	if err := conn.Send(wire.Vet{T: wire.TVet, JobID: "j2", Ecosystem: "npm",
		Packages: manyOf(wire.MaxVetPackages+1, protocol.VetPackage{Name: "x", Version: "1"})}); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := conn.Receive(5 * time.Second); err != nil {
			break // the hub ended the connection
		}
	}
	if e := eventOf(t, f, "protocol_violation", "alpha"); e["type"] != wire.TVet {
		t.Errorf("protocol violation: %v", e)
	}
}
