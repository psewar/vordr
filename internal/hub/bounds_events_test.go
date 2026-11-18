package hub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// agentSession logs node in over a real tunnel with a hello in bounds and returns the connection and the key's
// fingerprint; with approve the operator approves the host.
func agentSession(t *testing.T, f *viewFixture, ts *httptest.Server, node string, approve bool) (*wire.Conn, string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	conn := dialTunnel(t, ts)
	var ch wire.Challenge
	if _, raw, err := conn.Receive(5 * time.Second); err != nil || json.Unmarshal(raw, &ch) != nil {
		t.Fatalf("challenge: %v", err)
	}
	if err := conn.Send(wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "0.48.0",
		Signature: ed25519.Sign(priv, wire.HelloSigned(ch.Nonce, node, pub))}); err != nil {
		t.Fatal(err)
	}
	var st wire.Status
	if _, raw, err := conn.Receive(5 * time.Second); err != nil || json.Unmarshal(raw, &st) != nil ||
		st.State != wire.StatePendingApproval {
		t.Fatalf("%s: %+v %v", node, st, err)
	}
	fp := identity.Fingerprint(pub)
	if approve {
		if code := f.srv.Reg.Decide(node, wire.StateApproved, fp, f.now); code != AdminOK {
			t.Fatal(code)
		}
	}
	return conn, fp
}

// sendHandled sends msgs on conn, then a heartbeat, and waits for the hub's answer to it: the hub has then handled
// every message sent before.
func sendHandled(t *testing.T, conn *wire.Conn, msgs ...any) {
	t.Helper()
	for _, m := range append(msgs, wire.Heartbeat{T: wire.THeartbeat}) {
		if err := conn.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	for {
		typ, _, err := conn.Receive(5 * time.Second)
		if err != nil {
			t.Fatalf("no heartbeat: %v", err)
		}
		if typ == wire.THeartbeat {
			return
		}
	}
}

// eventsOf are the events of name logged under node (a host or a Home Assistant client), oldest first.
func eventsOf(t *testing.T, f *viewFixture, name, node string) []map[string]any {
	t.Helper()
	var found []map[string]any
	for _, e := range eventLines(t, f, name) {
		if e["node"] == node || e["client"] == node {
			found = append(found, e)
		}
	}
	return found
}

// eventOf is the one event of name logged under node; it fails unless there is exactly one.
func eventOf(t *testing.T, f *viewFixture, name, node string) map[string]any {
	t.Helper()
	found := eventsOf(t, f, name, node)
	if len(found) != 1 {
		t.Fatalf("%s of %s: %v", name, node, found)
	}
	return found[0]
}

// Over a real tunnel (ADR-0195): what the hub does not take of an approved host's report, inventory or job status is
// logged with the key's fingerprint, at most once an hour per host and event - a drop does not hide a refusal -; the
// next event counts the messages left out of the log. A refused message changes nothing the hub keeps. A pending
// host's messages log nothing, and messages in bounds cause no event.
func TestMessagesOutOfBoundsAreLoggedOncePerHourAndHost(t *testing.T) {
	f := newViewFixture(t)
	pk, err := NewPackages(t.TempDir(), nil, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Packages = pk
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	alpha, fp := agentSession(t, f, ts, "alpha", true)
	other, otherFP := agentSession(t, f, ts, "other", true)
	pending, _ := agentSession(t, f, ts, "pending", false)

	// A report with its detail in ANSI colours is kept with the colours cleaned out.
	rep := aptReport(f.now, 3, 1, nil)
	rep.Sources[0].Detail = "E: \x1b[31mfailed\x1b[0m"
	sendHandled(t, alpha, rep)
	if e, _ := f.srv.Reg.Get("alpha"); e.LastReport == nil ||
		e.LastReport.Sources[0].Detail != "E: \uFFFD[31mfailed\uFFFD[0m" || e.LastReport.Sources[0].Pending != 3 {
		t.Fatalf("stored: %+v", e.LastReport)
	}
	// A report the hub refuses keeps the one before. Within the same hour it is logged all the same - refusals and
	// drops count apart -, a second one is not; another host has an hour of its own.
	bad := aptReport(f.now, 5, 0, nil)
	bad.Sources[0].Source = "Apt"
	sendHandled(t, alpha, bad, rep, bad)
	if e, _ := f.srv.Reg.Get("alpha"); e.LastReport.Sources[0].Pending != 3 {
		t.Fatalf("a refused report was stored: %+v", e.LastReport)
	}
	sendHandled(t, other, bad)
	if e, _ := f.srv.Reg.Get("other"); e.LastReport != nil {
		t.Fatalf("a refused report was stored: %+v", e.LastReport)
	}
	sendHandled(t, pending, bad, rep)
	f.now = f.now.Add(time.Hour)
	sendHandled(t, alpha, bad, aptReport(f.now, 0, 0, nil))

	if e := eventOf(t, f, "report_fields_dropped", "alpha"); e["fingerprint"] != fp || e["unlogged"] != nil ||
		e["fields"] != nil || !reflect.DeepEqual(e["cleaned"], map[string]any{"sources[].detail": "characters"}) {
		t.Errorf("dropped: %v", e)
	}
	refused := eventsOf(t, f, "report_refused", "alpha")
	if len(refused) != 2 || refused[0]["unlogged"] != nil || refused[1]["unlogged"] != float64(1) {
		t.Fatalf("refused: %v", refused)
	}
	for _, e := range refused {
		if e["fingerprint"] != fp || !reflect.DeepEqual(e["fields"], map[string]any{"sources[].source": "characters"}) {
			t.Errorf("refused: %v", e)
		}
	}
	if e := eventOf(t, f, "report_refused", "other"); e["fingerprint"] != otherFP || e["unlogged"] != nil {
		t.Errorf("other: %v", e)
	}
	if n := len(eventLines(t, f, "report_refused")) + len(eventLines(t, f, "report_fields_dropped")); n != 4 {
		t.Errorf("%d report events", n)
	}
	if e, _ := f.srv.Reg.Get("alpha"); e.LastReport.Sources[0].Pending != 0 {
		t.Fatalf("the report in bounds was not stored: %+v", e.LastReport)
	}

	// An inventory with more locations than the bound keeps the first ones, with an event; one with a location out of
	// bounds is kept without it - not logged within the hour.
	sendHandled(t, alpha, wire.Inventory{T: wire.TInventory, CheckedAt: f.now, Locations: manyOf(1025,
		inventory.Location{ID: "venv", Kind: inventory.KindVenv, Ecosystem: versions.PyPI, Packages: []inventory.Package{}})})
	if v := pk.View("alpha"); v == nil || len(v.Locations) != protocol.MaxInventoryLocations {
		t.Fatalf("the first locations were not kept: %+v", v)
	}
	sendHandled(t, alpha, wire.Inventory{T: wire.TInventory, CheckedAt: f.now, Locations: []inventory.Location{
		{ID: "Bad Id", Kind: inventory.KindVenv, Ecosystem: versions.PyPI, Packages: []inventory.Package{}},
		{ID: "venv", Kind: inventory.KindVenv, Ecosystem: versions.PyPI, Path: "/opt/venv",
			Packages: []inventory.Package{{Name: "requests", Version: "2.32.3"}}}}})
	if v := pk.View("alpha"); v == nil || len(v.Locations) != 1 || v.Locations[0].ID != "venv" || v.Total != 1 {
		t.Fatalf("inventory: %+v", v)
	}
	if e := eventOf(t, f, "inventory_fields_dropped", "alpha"); e["fingerprint"] != fp ||
		!reflect.DeepEqual(e["fields"], map[string]any{"locations": "too_many"}) {
		t.Errorf("inventory: %v", e)
	}
	if n := len(eventLines(t, f, "inventory_refused")); n != 0 {
		t.Errorf("%d inventories refused", n)
	}

	// Job status messages: the approved host's are logged, the pending host's are not.
	sendHandled(t, alpha, wire.JobStatus{T: wire.TJobStatus, JobID: "j1", Phase: protocol.PhaseInstalling,
		Item: "pkg\x1b[2J"}, wire.JobStatus{T: wire.TJobStatus, JobID: "../j1", Code: protocol.CodeOK},
		wire.JobStatus{T: wire.TJobStatus, JobID: "../j3", Code: protocol.CodeOK})
	sendHandled(t, pending, wire.JobStatus{T: wire.TJobStatus, JobID: "../j2", Code: protocol.CodeOK},
		wire.JobStatus{T: wire.TJobStatus, JobID: "j2", Phase: protocol.PhaseInstalling, Item: "pkg\x1b[2J"})
	if e := eventOf(t, f, "job_status_fields_dropped", "alpha"); e["fingerprint"] != fp ||
		!reflect.DeepEqual(e["fields"], map[string]any{"item": "characters"}) {
		t.Errorf("job status: %v", e)
	}
	if e := eventOf(t, f, "job_status_refused", "alpha"); e["fingerprint"] != fp ||
		!reflect.DeepEqual(e["fields"], map[string]any{"job_id": "characters"}) {
		t.Errorf("job status refused: %v", e)
	}
	for _, e := range eventLines(t, f, "job_status") {
		if strings.HasPrefix(e["job_id"].(string), "../") {
			t.Errorf("a refused job status was delivered: %v", e)
		}
	}
	for _, name := range []string{"report_fields_dropped", "report_refused", "inventory_fields_dropped",
		"inventory_refused", "job_status_fields_dropped", "job_status_refused"} {
		for _, e := range eventLines(t, f, name) {
			if e["node"] == "pending" {
				t.Errorf("logged for a pending host: %v", e)
			}
		}
	}
	for _, node := range []string{"alpha", "other", "pending"} {
		if online, _ := f.srv.Online(node); !online {
			t.Errorf("%s: a message out of bounds ended the connection", node)
		}
	}
}

// A host the operator forgets starts afresh (ADR-0195): approved again under its name, its first message out of bounds
// is logged at once, not counted into the hour of the host that had the name before.
func TestAForgottenHostInheritsNoSilence(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	bad := aptReport(f.now, 5, 0, nil)
	bad.Sources[0].Source = "Apt"
	conn, first := agentSession(t, f, ts, "alpha", true)
	sendHandled(t, conn, bad)
	if code := f.srv.Admin(AdminRequest{Cmd: "forget", Node: "alpha"}).Code; code != AdminOK {
		t.Fatal(code)
	}
	conn, second := agentSession(t, f, ts, "alpha", true)
	sendHandled(t, conn, bad)
	refused := eventsOf(t, f, "report_refused", "alpha")
	if len(refused) != 2 || refused[0]["fingerprint"] != first || refused[1]["fingerprint"] != second ||
		refused[1]["unlogged"] != nil {
		t.Fatalf("refused: %v", refused)
	}
}

// What an earlier hub stored unbounded is bounded when the hub loads it (ADR-0195): a stored report the hub would
// refuse is not shown - unknown until the host reports again -, one with a field out of bounds is kept without it, the
// same for inventories; one line each in the service log, no event.
func TestWhatAnEarlierHubStoredIsBoundedOnLoad(t *testing.T) {
	dir := t.TempDir()
	reg, err := OpenRegistry(filepath.Join(dir, "r.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, node := range []string{"refused", "cleaned", "whole"} {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		fp := identity.Fingerprint(pub)
		if _, _, err := reg.Admit(wire.Hello{Node: node, PublicKey: pub}, fp, now, 5); err != nil ||
			reg.Decide(node, wire.StateApproved, fp, now) != AdminOK {
			t.Fatal(err)
		}
	}
	refused := aptReport(now, 3, 1, nil)
	refused.Sources[0].Source = "Apt\x1b[2J"
	cleaned := aptReport(now, 3, 1, nil)
	cleaned.Sources[0].Detail = "\x1b[31mE:\x1b[0m failed"
	cleaned.Reboot = &source.Reboot{Required: true, Running: "6.1\n", Checked: []string{"kernel"}}
	for node, rep := range map[string]wire.Report{"refused": refused, "cleaned": cleaned, "whole": aptReport(now, 3, 1, nil)} {
		if err := reg.SetReport(node, rep); err != nil { // as an earlier hub stored it, unbounded
			t.Fatal(err)
		}
	}
	pk, err := NewPackages(dir, nil, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := pk.SetInventory("cleaned", wire.Inventory{T: wire.TInventory, CheckedAt: now,
		Locations: append([]inventory.Location{{ID: "Bad Id", Kind: inventory.KindVenv, Ecosystem: versions.PyPI}},
			manyOf(1100, inventory.Location{ID: "venv", Kind: inventory.KindVenv, Ecosystem: versions.PyPI,
				Packages: []inventory.Package{{Name: "requests", Version: "2.32.3"}}})...)}); err != nil {
		t.Fatal(err)
	}

	var logged bytes.Buffer
	logger := log.New(&logged, "", 0)
	reg, err = OpenRegistry(filepath.Join(dir, "r.json"))
	if err != nil {
		t.Fatal(err)
	}
	events, _ := OpenEvents(filepath.Join(dir, "e.jsonl"), time.Now, nil)
	t.Cleanup(func() { events.Close() })
	key, _, _ := identity.LoadOrCreateAt(filepath.Join(dir, "hub.key"))
	clients, _ := OpenClientRegistry(filepath.Join(dir, "c.json"))
	srv := NewServer(reg, clients, events, key, 5, logger)
	if pk, err = NewPackages(dir, nil, time.Now, logger); err != nil {
		t.Fatal(err)
	}
	srv.Packages = pk

	if e, _ := reg.Get("refused"); e.LastReport != nil {
		t.Errorf("a refused stored report is shown: %+v", e.LastReport)
	}
	if v := srv.HostView(mustGet(t, reg, "refused"), now); v.LastReport != nil || v.Pending != nil {
		t.Errorf("view of a refused stored report: %+v", v)
	}
	e, _ := reg.Get("cleaned")
	if e.LastReport == nil || e.LastReport.Sources[0].Detail != "\uFFFD[31mE:\uFFFD[0m failed" ||
		e.LastReport.Reboot == nil || e.LastReport.Reboot.Running != "" || e.LastReport.Sources[0].Pending != 3 {
		t.Errorf("cleaned: %+v", e.LastReport)
	}
	if w, _ := reg.Get("whole"); w.LastReport == nil || w.LastReport.Sources[0].Pending != 3 {
		t.Errorf("whole: %+v", w.LastReport)
	}
	if v := pk.View("cleaned"); v == nil || len(v.Locations) != protocol.MaxInventoryLocations-1 {
		t.Errorf("stored inventory: %+v", v)
	}
	for _, want := range []string{"stored report of refused out of bounds, kept false",
		"stored report of cleaned out of bounds, kept true", "stored inventory of cleaned out of bounds, kept without"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("service log without %q:\n%s", want, logged.String())
		}
	}
	if strings.Contains(logged.String(), "whole") {
		t.Errorf("a stored report in bounds was logged:\n%s", logged.String())
	}
	raw, _, err := events.Read(EventFilter{Limit: MaxEventLimit})
	if err != nil || len(raw) != 0 {
		t.Errorf("events on load: %d %v", len(raw), err)
	}
}

func mustGet(t *testing.T, reg *Registry, node string) Entry {
	t.Helper()
	e, ok := reg.Get(node)
	if !ok {
		t.Fatalf("no %s", node)
	}
	return e
}

// haLogin logs a Home Assistant client in with priv (a new key when nil) on the real /v1/ha endpoint with a hello in
// bounds that change may alter (signed afterwards unless it signs itself), and returns the hub's status and the key's
// fingerprint.
func haLogin(t *testing.T, ts *httptest.Server, name string, priv ed25519.PrivateKey,
	change func(*wire.HAHello)) (wire.Status, string) {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+wire.HAPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	var ch wire.Challenge
	if err := ws.ReadJSON(&ch); err != nil {
		t.Fatal(err)
	}
	if priv == nil {
		_, priv, _ = ed25519.GenerateKey(rand.Reader)
	}
	pub := priv.Public().(ed25519.PublicKey)
	h := wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: name, PublicKey: pub, IntegrationVersion: "0.49.0",
		Protocol: wire.HAProtocol}
	if change != nil {
		change(&h)
	}
	if h.Signature == nil {
		h.Signature = ed25519.Sign(priv, wire.HAHelloSigned(ch.Nonce, h.Name, h.PublicKey))
	}
	var st wire.Status
	if err := ws.WriteJSON(h); err != nil || ws.ReadJSON(&st) != nil {
		t.Fatalf("%s: %v", name, err)
	}
	ws.Close()
	return st, identity.Fingerprint(pub)
}

// The hello of a Home Assistant client (ADR-0195): its identity out of bounds is refused with bad_hello, as before,
// and nothing of it is kept. integration_version out of bounds is dropped and the client connects; once it is
// approved, one event per hello names the drop with the key's fingerprint - not while it waits, and not for a hello the
// client register refuses.
func TestTheHubBoundsTheHelloOfAHomeAssistantClient(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	version := func(v string) func(*wire.HAHello) { return func(h *wire.HAHello) { h.IntegrationVersion = v } }
	for name, v := range map[string]string{"ha-release": "0.49.0", "ha-test": "test", "ha-none": "",
		"ha-at-the-bound": "1." + strings.Repeat("0", 62), "ha-candidate": "1.2.3-rc.1+build.7"} {
		st, fp := haLogin(t, ts, name, nil, version(v))
		if c, ok := f.srv.Clients.Get(name); st.State != wire.StatePendingApproval || !ok || c.IntegrationVersion != v {
			t.Errorf("%s: %+v %+v", name, st, c)
		}
		f.srv.Clients.Decide(name, wire.StateApproved, fp, f.now) // at most five wait for approval
	}
	dropped := map[string]struct{ version, why string }{
		"ha-too-long":   {"1." + strings.Repeat("0", 63), dropTooLong},
		"ha-megabyte":   {strings.Repeat("9", 1<<20), dropTooLong},
		"ha-line-break": {"0.49.0\n", dropCharacters},
		"ha-space":      {"0.49 beta", dropCharacters},
		"ha-path":       {"../0.49", dropCharacters},
		"ha-escape":     {"\x1b[2J", dropCharacters},
	}
	for name, c := range dropped {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		st, fp := haLogin(t, ts, name, priv, version(c.version))
		if cl, ok := f.srv.Clients.Get(name); st.State != wire.StatePendingApproval || !ok || cl.IntegrationVersion != "" {
			t.Errorf("%s: %+v %+v", name, st, cl)
		}
		if got := eventsOf(t, f, "ha_hello_fields_dropped", name); len(got) != 0 {
			t.Errorf("%s: logged while it waits: %v", name, got)
		}
		f.srv.Clients.Decide(name, wire.StateApproved, fp, f.now)
		if st, _ := haLogin(t, ts, name, priv, version(c.version)); st.State != wire.StateApproved {
			t.Fatalf("%s approved: %+v", name, st)
		}
		if e := eventOf(t, f, "ha_hello_fields_dropped", name); e["fingerprint"] != fp ||
			!reflect.DeepEqual(e["fields"], map[string]any{"integration_version": c.why}) {
			t.Errorf("%s: %v", name, e)
		}
		for _, e := range eventsOf(t, f, "ha_hello", name) {
			if e["integration_version"] != "" {
				t.Errorf("%s: the hello event logs the dropped version: %v", name, e)
			}
		}
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	for name, c := range map[string]struct {
		client string
		change func(*wire.HAHello)
	}{
		"a role other than ha":   {"ha-role", func(h *wire.HAHello) { h.Role = "admin" }},
		"a name too long":        {strings.Repeat("b", 64), nil},
		"a name of a megabyte":   {strings.Repeat("c", 1<<20), nil},
		"a name in capitals":     {"HA-Capital", nil},
		"a key too long":         {"ha-key", func(h *wire.HAHello) { h.PublicKey = append(h.PublicKey, 0) }},
		"another key's proof":    {"ha-proof", func(h *wire.HAHello) { h.PublicKey = other }},
		"a signature too long":   {"ha-sig", func(h *wire.HAHello) { h.Signature = make([]byte, 1<<20) }},
		"a bad version as well":  {"ha-Both", version("\x1b[2J")},
		"another type of hello":  {"ha-type", func(h *wire.HAHello) { h.T = wire.TReport }},
		"another type, and more": {"ha-type-2", func(h *wire.HAHello) { h.T = strings.Repeat("x", 1<<20) }},
	} {
		st, _ := haLogin(t, ts, c.client, nil, c.change)
		if _, stored := f.srv.Clients.Get(c.client); st.State != wire.StateRejected || st.Reason != wire.ReasonBadHello ||
			stored {
			t.Errorf("%s: %+v, stored %v", name, st, stored)
		}
	}
	// Another key under a known name: the register refuses it, and its version out of bounds is not logged under the
	// name of a client that is not its own.
	if st, _ := haLogin(t, ts, "ha-release", nil, version("\x1b[2J")); st.Reason != wire.ReasonKeyMismatch {
		t.Fatalf("another key: %+v", st)
	}
	if n := len(eventLines(t, f, "ha_hello_fields_dropped")); n != len(dropped) {
		t.Errorf("%d drops logged, want %d", n, len(dropped))
	}
}
