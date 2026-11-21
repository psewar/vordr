package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// Holding a release a host offers only in another branch (ADR-0208).

const (
	releaseOther = "pfSense (other branch)"
	day          = 24 * time.Hour
)

// branchReport: a firewall's report at at - the release to offered only in another branch, kept back, and as many
// add-on offers as addons, which an install takes.
func branchReport(at time.Time, to string, addons int) wire.Report {
	src := source.Report{Source: "pfsense", State: protocol.SourceUpToDate, CheckedAt: at}
	if to != "" {
		src.Packages = append(src.Packages, source.Package{Name: releaseOther, From: "1.2.3", To: to, KeptBack: true})
		src.KeptBack, src.KeptBackWhy = 1, protocol.KeptBackOtherBranch
	}
	for i := range addons {
		src.Packages = append(src.Packages, source.Package{Name: "package pfSense-pkg-" + string(rune('a'+i)), From: "1",
			To: "2"})
	}
	if src.Pending = len(src.Packages); src.Pending > 0 {
		src.State = protocol.SourceUpdatesAvailable
	}
	return wire.Report{T: wire.TReport, CheckedAt: at, Sources: []source.Report{src}}
}

func stale(v wire.HostView) bool {
	return v.Problem != nil && slices.Contains(v.Problem.Reasons, ReasonStale)
}

func firewallFixture(t *testing.T) *viewFixture {
	t.Helper()
	f := newViewFixture(t)
	all := wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all"}
	f.host(t, "fw", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, all, true)
	return f
}

// The view names the release in another branch; a hold of exactly it ends the stale problem while it is all that is
// pending, and the release stays pending and kept back. Only what the host offers can be held.
func TestHoldRelease(t *testing.T) {
	f := firewallFixture(t)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now.Add(-8*day), "1.3.0", 0))
	v := f.view(t, "fw", f.now)
	if !stale(v) || v.OtherBranch != "1.3.0" || v.HeldRelease != "" {
		t.Fatalf("before: stale %v, other branch %q, held %q", stale(v), v.OtherBranch, v.HeldRelease)
	}
	if code := f.srv.Reg.HoldRelease("fw", "1.2.9", "ha:test", f.now); code != AdminNotOffered {
		t.Fatalf("another version: %s", code)
	}
	if code := f.srv.Reg.HoldRelease("fw", "1.3.0", "ha:test", f.now); code != AdminOK {
		t.Fatalf("hold: %s", code)
	}
	v = f.view(t, "fw", f.now)
	if stale(v) || v.HeldRelease != "1.3.0" || *v.Pending != 1 || *v.KeptBack != 1 || v.OtherBranch != "1.3.0" {
		t.Fatalf("held: stale %v, held %q, pending %d, kept back %d", stale(v), v.HeldRelease, *v.Pending, *v.KeptBack)
	}
	if code := f.srv.Reg.HoldRelease("fw", "1.3.0", "ha:test", f.now); code != AdminAlready {
		t.Fatalf("again: %s", code)
	}
	e, _ := f.srv.Reg.Get("fw")
	if h := e.HeldRelease; h == nil || h.Source != "pfsense" || h.Name != releaseOther || h.By != "ha:test" ||
		!h.At.Equal(f.now) {
		t.Fatalf("hold: %+v", e.HeldRelease)
	}
	// It survives a restart of the hub: the register keeps it.
	reg, err := OpenRegistry(f.srv.Reg.path)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := reg.Get("fw"); e.HeldRelease == nil || e.HeldRelease.To != "1.3.0" || e.PendingSince != nil {
		t.Fatalf("after a restart: %+v %v", e.HeldRelease, e.PendingSince)
	}

	// Refused: an unknown host, a host that waits for approval, a host whose check failed.
	if code := f.srv.Reg.HoldRelease("nobody", "1.3.0", "ha:test", f.now); code != AdminUnknownNode {
		t.Fatalf("unknown: %s", code)
	}
	f.host(t, "waiting", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	if code := f.srv.Reg.HoldRelease("waiting", "1.3.0", "ha:test", f.now); code != AdminNotApproved {
		t.Fatalf("waiting: %s", code)
	}
	f.host(t, "failed", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	failed := branchReport(f.now, "1.3.0", 0)
	failed.Sources[0].State, failed.Sources[0].Code = protocol.SourceCheckFailed, protocol.CodePrecheckRepoUnreachable
	_ = f.srv.Reg.SetReport("failed", failed)
	if code := f.srv.Reg.HoldRelease("failed", "1.3.0", "ha:test", f.now); code != AdminNotOffered {
		t.Fatalf("failed check: %s", code)
	}
}

// The hold follows the host's reports: the same release keeps it, other pending updates count as before, and once the
// host no longer offers exactly that release - a newer one, or nothing pending - the hold ends and what is pending
// counts from then on. A failed check changes nothing.
func TestHoldReleaseFollowsTheReports(t *testing.T) {
	f := firewallFixture(t)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now.Add(-8*day), "1.3.0", 0))
	_ = f.srv.Reg.HoldRelease("fw", "1.3.0", "ha:test", f.now)

	f.now = f.now.Add(day)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now, "1.3.0", 0))
	if e, _ := f.srv.Reg.Get("fw"); e.HeldRelease == nil || e.PendingSince != nil {
		t.Fatalf("the same release: %+v %v", e.HeldRelease, e.PendingSince)
	}
	failed := branchReport(f.now, "", 0)
	failed.Sources[0].State, failed.Sources[0].Code = protocol.SourceCheckFailed, protocol.CodePrecheckRepoUnreachable
	_ = f.srv.Reg.SetReport("fw", failed)
	if e, _ := f.srv.Reg.Get("fw"); e.HeldRelease == nil {
		t.Fatal("a failed check ended the hold")
	}
	// An add-on update besides: it is pending and counts, the hold stays.
	f.now = f.now.Add(day)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now, "1.3.0", 1))
	if e, _ := f.srv.Reg.Get("fw"); e.HeldRelease == nil || e.PendingSince == nil || !e.PendingSince.Equal(f.now) {
		t.Fatalf("with an add-on: %+v %v", e.HeldRelease, e.PendingSince)
	}
	f.now = f.now.Add(day)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now, "1.3.0", 0))
	if e, _ := f.srv.Reg.Get("fw"); e.HeldRelease == nil || e.PendingSince != nil {
		t.Fatalf("add-on installed: %+v %v", e.HeldRelease, e.PendingSince)
	}
	// A newer release in the other branch: the hold ends, the new release counts from its first report.
	f.now = f.now.Add(day)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now, "1.3.1", 0))
	e, _ := f.srv.Reg.Get("fw")
	if e.HeldRelease != nil || e.PendingSince == nil || !e.PendingSince.Equal(f.now) {
		t.Fatalf("a newer release: %+v %v", e.HeldRelease, e.PendingSince)
	}
	if v := f.view(t, "fw", f.now.Add(8*day)); !stale(v) || v.HeldRelease != "" || v.OtherBranch != "1.3.1" {
		t.Fatalf("a newer release, 8 days on: stale %v, held %q", stale(v), v.HeldRelease)
	}
	// Nothing pending (the branch switched and the release installed): the hold ends.
	_ = f.srv.Reg.HoldRelease("fw", "1.3.1", "ha:test", f.now)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now, "", 0))
	if e, _ := f.srv.Reg.Get("fw"); e.HeldRelease != nil || e.PendingSince != nil {
		t.Fatalf("nothing pending: %+v %v", e.HeldRelease, e.PendingSince)
	}
}

// The hub logs a hold a report ends; a report that keeps it logs nothing about it.
func TestHoldReleaseEndLogged(t *testing.T) {
	f := firewallFixture(t)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now, "1.3.0", 0))
	_ = f.srv.Reg.HoldRelease("fw", "1.3.0", "ha:test", f.now)
	e, _ := f.srv.Reg.Get("fw")
	f.srv.takeReport("fw", e, branchReport(f.now, "1.3.0", 0))
	if got := eventLines(t, f, "release_hold_ended"); len(got) != 0 {
		t.Fatalf("kept, but logged: %v", got)
	}
	e, _ = f.srv.Reg.Get("fw")
	f.srv.takeReport("fw", e, branchReport(f.now, "1.3.1", 0))
	if got := eventLines(t, f, "release_hold_ended"); len(got) != 1 || got[0]["to"] != "1.3.0" || got[0]["node"] != "fw" {
		t.Fatalf("event: %v", got)
	}
}

// `vordr-hub unhold` ends a hold: the release counts as pending again, from now; without a hold it says so.
func TestUnhold(t *testing.T) {
	f := firewallFixture(t)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now.Add(-8*day), "1.3.0", 0))
	_ = f.srv.Reg.HoldRelease("fw", "1.3.0", "ha:test", f.now)
	f.now = f.now.Add(time.Hour)
	if r := f.srv.Admin(AdminRequest{Cmd: "unhold", Node: "fw"}); r.Code != AdminOK {
		t.Fatalf("unhold: %s", r.Code)
	}
	e, _ := f.srv.Reg.Get("fw")
	if e.HeldRelease != nil || e.PendingSince == nil || !e.PendingSince.Equal(f.now) {
		t.Fatalf("after unhold: %+v %v", e.HeldRelease, e.PendingSince)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "unhold", Node: "fw"}); r.Code != AdminAlready {
		t.Fatalf("again: %s", r.Code)
	}
	if r := f.srv.Admin(AdminRequest{Cmd: "unhold", Node: "nobody"}); r.Code != AdminUnknownNode {
		t.Fatalf("unknown: %s", r.Code)
	}
	if got := eventLines(t, f, "unhold"); len(got) != 3 || got[0]["code"] != "ok" {
		t.Fatalf("events: %v", got)
	}
}

// From Home Assistant: a client with role operate or admin holds; observe may not; the version must be given and be
// text within its bound; a client that is not approved is refused. Each answer is in the event log with the client.
func TestHoldReleaseFromHomeAssistant(t *testing.T) {
	f := firewallFixture(t)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now.Add(-8*day), "1.3.0", 0))
	ask := func(sess *haSession, node, to string) string {
		return f.srv.haHoldRelease(sess, wire.HoldRelease{T: wire.THoldRelease, RequestID: "r1", Node: node, To: to}).Code
	}
	observe := f.haClient(t, "viewer", RoleObserve)
	operate := f.haClient(t, "ha", RoleOperate)
	h, pendingFP := haHello(t, "pending")
	f.srv.Clients.Admit(h, pendingFP, f.now)
	for _, c := range []struct {
		name string
		sess *haSession
		node string
		to   string
		want AdminCode
	}{
		{"observe", observe, "fw", "1.3.0", AdminRoleForbids},
		{"pending client", &haSession{name: "pending", fingerprint: pendingFP}, "fw", "1.3.0", AdminClientNotApproved},
		{"no version", operate, "fw", "", AdminBadRequest},
		{"a version with a line break", operate, "fw", "1.3.0\n", AdminBadRequest},
		{"not offered", operate, "fw", "2.0.0", AdminNotOffered},
		{"unknown host", operate, "nobody", "1.3.0", AdminUnknownNode},
	} {
		if got := ask(c.sess, c.node, c.to); got != string(c.want) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got := eventLines(t, f, "release_hold_refused"); len(got) != 6 || got[0]["client"] != "viewer" ||
		got[0]["role"] != RoleObserve {
		t.Fatalf("refusals: %v", got)
	}
	if got := ask(operate, "fw", "1.3.0"); got != string(AdminOK) {
		t.Fatalf("hold: %s", got)
	}
	if e, _ := f.srv.Reg.Get("fw"); e.HeldRelease == nil || e.HeldRelease.By != ByHA("ha") {
		t.Fatalf("hold: %+v", e.HeldRelease)
	}
	got := eventLines(t, f, "release_hold")
	if len(got) != 1 || got[0]["client"] != "ha" || got[0]["to"] != "1.3.0" || got[0]["code"] != "ok" {
		t.Fatalf("event: %v", got)
	}
	if v := f.view(t, "fw", f.now); stale(v) || v.HeldRelease != "1.3.0" {
		t.Fatalf("view: stale %v, held %q", stale(v), v.HeldRelease)
	}
}

// Over the HA protocol: an operate client sees the feature and gets its answer; a malformed request ends the
// connection as a protocol violation.
func TestHoldReleaseOverTheHAProtocol(t *testing.T) {
	f := firewallFixture(t)
	_ = f.srv.Reg.SetReport("fw", branchReport(f.now.Add(-8*day), "1.3.0", 0))
	priv := adminClient(t, f, "ha-operate", RoleOperate)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())

	c, st := dialHA(t, ts.URL, "ha-operate", priv)
	if st.State != wire.StateApproved || !slices.Contains(st.Features, wire.FeatureHoldRelease) {
		t.Fatalf("status: %+v", st)
	}
	if err := c.ws.WriteJSON(wire.HoldRelease{T: wire.THoldRelease, RequestID: "hold-1", Node: "fw", To: "1.3.0"}); err != nil {
		t.Fatal(err)
	}
	got := c.collect(wire.THoldReleaseResult, wire.THost)
	var res wire.HoldReleaseResult
	if json.Unmarshal(got[wire.THoldReleaseResult], &res) != nil || res.Code != string(AdminOK) || res.RequestID != "hold-1" {
		t.Fatalf("result: %s", got[wire.THoldReleaseResult])
	}
	var hv wire.HostMsg
	if json.Unmarshal(got[wire.THost], &hv) != nil || hv.Host.HeldRelease != "1.3.0" {
		t.Fatalf("host: %s", got[wire.THost])
	}

	if err := c.ws.WriteJSON(wire.HoldRelease{T: wire.THoldRelease, RequestID: "hold-2", Node: "Not A Node",
		To: "1.3.0"}); err != nil {
		t.Fatal(err)
	}
	if c.next(wire.THoldReleaseResult, &res) {
		t.Fatalf("a malformed request was answered: %+v", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(eventLines(t, f, "ha_protocol_violation")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no protocol violation in the event log")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
