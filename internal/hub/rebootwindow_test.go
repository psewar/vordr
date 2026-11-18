package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// rebootPolicy: reboots allowed, reboot.window mon-fri 06:00-16:00 in zone, as an agent after ADR-0187 reports it.
func rebootPolicy(zone string, windowOnly bool) wire.PolicySummary {
	return wire.PolicySummary{RebootAllowed: true, OutsideWindow: true, TZ: zone, RebootWindowOnly: windowOnly,
		RebootWindow: &wire.WindowInfo{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "06:00", End: "16:00"}}
}

// The host view says when the reboot window opens (ADR-0187): absent inside it, the next opening outside it - the
// next morning, after the weekend, past midnight, in the host's own zone - with or without window_only, since the reboot
// button carries no reason either way (ADR-0109). Absent where the hub offers no reboot, where the agent reports no
// window, and where the hub cannot read the zone; the window itself is passed on wherever the reboot is offered.
func TestNextRebootFromTheAgentsWindow(t *testing.T) {
	f := newViewFixture(t)
	reboot := []protocol.Verb{protocol.VerbCheck, protocol.VerbReboot}
	f.host(t, "pbs", reboot, rebootPolicy("Europe/Paris", true), true)
	f.host(t, "reason", reboot, rebootPolicy("Europe/Paris", false), true)
	f.host(t, "newyork", reboot, rebootPolicy("America/New_York", true), true)
	night := rebootPolicy("Europe/Paris", true)
	night.RebootWindow = &wire.WindowInfo{Days: []string{"sat"}, Start: "23:00", End: "02:00"}
	f.host(t, "night", reboot, night, true)
	notAllowed := rebootPolicy("Europe/Paris", true)
	notAllowed.RebootAllowed = false
	f.host(t, "notallowed", reboot, notAllowed, true)
	f.host(t, "noverb", []protocol.Verb{protocol.VerbCheck}, rebootPolicy("Europe/Paris", true), true)
	f.host(t, "older", reboot, wire.PolicySummary{RebootAllowed: true, OutsideWindow: true}, true)
	f.host(t, "nozone", reboot, rebootPolicy("", true), true)
	f.host(t, "badzone", reboot, rebootPolicy("Mars/Olympus_Mons", true), true)
	badWindow := rebootPolicy("Europe/Paris", true)
	badWindow.RebootWindow = &wire.WindowInfo{Days: []string{"mon"}, Start: "6 am", End: "16:00"}
	f.host(t, "badwindow", reboot, badWindow, true)

	utc := func(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.UTC) }
	wed10, wed20 := utc(18, 9, 0), utc(18, 19, 0) // 2026-11-18 is a Wednesday; Paris is UTC+1 in November
	for _, c := range []struct {
		node   string
		at     time.Time
		next   *time.Time
		window bool
	}{
		{"pbs", wed10, nil, true},
		{"pbs", wed20, ptr(utc(19, 5, 0)), true},               // Thursday 06:00 in Paris
		{"pbs", utc(20, 15, 0), ptr(utc(23, 5, 0)), true},      // Friday 16:00: Monday 06:00
		{"pbs", utc(19, 4, 59), ptr(utc(19, 5, 0)), true},      // a minute before it opens
		{"reason", wed20, ptr(utc(19, 5, 0)), true},            // the button has no reason: the same moment
		{"newyork", wed20, nil, true},                          // 14:00 in New York
		{"newyork", utc(18, 22, 0), ptr(utc(19, 11, 0)), true}, // 17:00 there: Thursday 06:00 there
		{"night", utc(22, 2, 0), ptr(utc(28, 22, 0)), true},    // Sunday 03:00: Saturday 23:00
		{"night", utc(21, 23, 0), nil, true},                   // Sunday 00:00, still Saturday's window
		{"notallowed", wed20, nil, false},
		{"noverb", wed20, nil, false},
		{"older", wed20, nil, false},
		{"nozone", wed20, nil, true},
		{"badzone", wed20, nil, true},
		{"badwindow", wed20, nil, true},
	} {
		v := f.view(t, c.node, c.at)
		if (v.NextReboot == nil) != (c.next == nil) || c.next != nil && !v.NextReboot.Equal(*c.next) {
			t.Errorf("%s at %s: next reboot %v, want %v", c.node, c.at.Format("Mon 15:04"), v.NextReboot, c.next)
		}
		if (v.RebootWindow != nil) != c.window {
			t.Errorf("%s: reboot window %+v", c.node, v.RebootWindow)
		}
	}

	v := f.view(t, "pbs", wed20)
	if w := v.RebootWindow; w.Start != "06:00" || w.End != "16:00" || len(w.Days) != 5 || w.TZ != "Europe/Paris" ||
		!w.WindowOnly {
		t.Fatalf("window: %+v", w)
	}
	if w := f.view(t, "reason", wed20).RebootWindow; w.WindowOnly {
		t.Fatalf("without window_only: %+v", w)
	}
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"next_reboot":"2026-11-19T05:00:00Z"`) ||
		!strings.Contains(string(raw), `"reboot_window":{"days":["mon","tue","wed","thu","fri"],"start":"06:00",`+
			`"end":"16:00","tz":"Europe/Paris","window_only":true}`) {
		t.Fatalf("view as Home Assistant gets it: %s", raw)
	}
	raw, _ = json.Marshal(f.view(t, "older", wed20))
	if strings.Contains(string(raw), "next_reboot") || strings.Contains(string(raw), "reboot_window") {
		t.Fatalf("an older agent's view: %s", raw)
	}
}

// The hub takes a reported reboot window only within bounds (ADR-0187): a window check-config would take - a day
// repeated too (ADR-0191) - with at most 64 entries in its days, and a zone name of at most 64 characters that the hub
// knows. A window without a zone is taken. Anything else has its fixed code.
func TestTheHubTakesOnlyABoundedRebootWindow(t *testing.T) {
	every := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	with := func(zone string, days []string, start string) wire.PolicySummary {
		p := rebootPolicy(zone, true)
		p.RebootWindow = &wire.WindowInfo{Days: days, Start: start, End: "16:00"}
		return p
	}
	many, different := make([]string, 100000), make([]string, 100000)
	for i := range many {
		many[i], different[i] = every[i%7], "day"+strconv.Itoa(i)
	}
	atTheBound, beyond := many[:64], many[:65]
	for name, c := range map[string]struct {
		p    wire.PolicySummary
		want string
	}{
		"workdays":           {rebootPolicy("Europe/Paris", true), ""},
		"every day":          {with("America/New_York", every, "06:00"), ""},
		"no zone":            {with("", every, "06:00"), ""},
		"no window at all":   {wire.PolicySummary{RebootAllowed: true, TZ: strings.Repeat("x", 1000)}, ""},
		"eight days":         {with("Europe/Paris", append(append([]string{}, every...), "mon"), "06:00"), ""},
		"a day twice":        {with("Europe/Paris", []string{"mon", "mon"}, "06:00"), ""},
		"64 entries":         {with("Europe/Paris", atTheBound, "06:00"), ""},
		"65 entries":         {with("Europe/Paris", beyond, "06:00"), "days"},
		"a hundred thousand": {with("Europe/Paris", many, "06:00"), "days"},
		"all different":      {with("Europe/Paris", different, "06:00"), "days"},
		"no day":             {with("Europe/Paris", nil, "06:00"), "window"},
		"no day name":        {with("Europe/Paris", []string{"someday"}, "06:00"), "window"},
		"no start":           {with("Europe/Paris", every, "6 am"), "window"},
		"start is end":       {with("Europe/Paris", every, "16:00"), "window"},
		"zone too long":      {with(strings.Repeat("A", 65), every, "06:00"), "tz"},
		"unknown zone":       {with("Mars/Olympus_Mons", every, "06:00"), "tz"},
		"a path":             {with("../../etc/passwd", every, "06:00"), "tz"},
	} {
		if got := rebootWindowRefusal(c.p); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// At the hello (ADR-0187): the challenge announces reboot_window; a bounded window is kept and reaches the view, an
// unbounded one is not kept, the host counts as one that reports none, and the event log says why.
func TestAHelloKeepsOnlyABoundedRebootWindow(t *testing.T) {
	f := newViewFixture(t)
	f.now = time.Date(2026, 11, 18, 19, 0, 0, 0, time.UTC) // Wednesday 20:00 in Paris: the window is closed
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	login := func(node string, pol wire.PolicySummary) {
		t.Helper()
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		conn := dialTunnel(t, ts)
		_, raw, err := conn.Receive(5 * time.Second)
		var ch wire.Challenge
		if err != nil || json.Unmarshal(raw, &ch) != nil || !slices.Contains(ch.Features, wire.FeatureRebootWindow) {
			t.Fatalf("challenge: %v %+v", err, ch.Features)
		}
		h := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "1.0", Policy: pol,
			Verbs:     []protocol.Verb{protocol.VerbCheck, protocol.VerbReboot},
			Signature: ed25519.Sign(priv, wire.HelloSigned(ch.Nonce, node, pub))}
		if err := conn.Send(h); err != nil {
			t.Fatal(err)
		}
		_, raw, err = conn.Receive(5 * time.Second)
		var st wire.Status
		if err != nil || json.Unmarshal(raw, &st) != nil || st.State != wire.StatePendingApproval {
			t.Fatalf("status: %v %+v", err, st)
		}
		if code := f.srv.Reg.Decide(node, wire.StateApproved, identity.Fingerprint(pub), f.now); code != AdminOK {
			t.Fatal(code)
		}
	}
	login("kept", rebootPolicy("Europe/Paris", true))
	huge := rebootPolicy("Europe/Paris", true)
	huge.RebootWindow.Days = make([]string, 100000)
	for i := range huge.RebootWindow.Days {
		huge.RebootWindow.Days[i] = "mon"
	}
	login("huge", huge)
	login("badzone", rebootPolicy("Mars/Olympus_Mons", true))

	if e, _ := f.srv.Reg.Get("kept"); e.Policy.RebootWindow == nil || !e.Policy.RebootWindowOnly {
		t.Fatalf("kept: %+v", e.Policy)
	}
	if v := f.view(t, "kept", f.now); v.RebootWindow == nil || v.NextReboot == nil {
		t.Fatalf("kept, view: %+v %v", v.RebootWindow, v.NextReboot)
	}
	for _, node := range []string{"huge", "badzone"} {
		e, _ := f.srv.Reg.Get(node)
		if e.Policy.RebootWindow != nil || e.Policy.RebootWindowOnly || !e.Policy.RebootAllowed {
			t.Fatalf("%s: %+v", node, e.Policy)
		}
		if v := f.view(t, node, f.now); v.RebootWindow != nil || v.NextReboot != nil || !v.Capabilities.Reboot {
			t.Fatalf("%s, view: %+v %v", node, v.RebootWindow, v.NextReboot)
		}
	}
	got := map[string]string{}
	for _, e := range eventLines(t, f, "reboot_window_refused") {
		got[e["node"].(string)] = e["reason"].(string)
	}
	if len(got) != 2 || got["huge"] != "days" || got["badzone"] != "tz" {
		t.Fatalf("events: %v", got)
	}
}
