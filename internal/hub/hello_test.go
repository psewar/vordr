package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// textOf is n bytes of text with spaces and a two-byte letter.
func textOf(n int) string {
	s := strings.Repeat("Aü ", n/4) // 4 bytes each
	return s + strings.Repeat("x", n-len(s))
}

// helloAtTheBounds is a hello with every bounded field at its bound: the longest texts and names, the most entries.
func helloAtTheBounds() wire.Hello {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	h := wire.Hello{T: wire.THello, Node: strings.Repeat("n", 63), PublicKey: pub, Signature: make([]byte, 64),
		AgentVersion: "1." + strings.Repeat("0", 62),
		Facts: facts.Host{Hostname: textOf(255), OS: textOf(255), OSID: textOf(255), OSVersion: textOf(255),
			GOOS: textOf(255), Arch: textOf(255), Virtualization: textOf(255), Container: textOf(255)},
		Runs: &wire.Runs{},
		Policy: wire.PolicySummary{DefaultScope: scopeOf(999), Auto: "a" + strings.Repeat("b", 31), AutoScope: "all",
			RebootAllowed: true, AgentUpdate: true, Window: &wire.WindowInfo{Start: "22:00", End: "05:00"},
			TZ: "A" + strings.Repeat("b", 63), OutsideWindow: true, MinIntervalH: 168}}
	for i := range 16 {
		h.Verbs = append(h.Verbs, protocol.Verb(fmt.Sprintf("v%031d", i)))
	}
	for i := range 64 {
		h.Runs.Running = append(h.Runs.Running, fmt.Sprintf("%064x", i))
		h.Runs.Unacknowledged = append(h.Runs.Unacknowledged, fmt.Sprintf("U%063d", i))
		h.Policy.Apps = append(h.Policy.Apps, wire.AppInfo{ID: fmt.Sprintf("a%031d", i), Name: textOf(255)})
		h.Policy.Window.Days = append(h.Policy.Window.Days, weekDays[i%7]) // each day more than once, as check-config takes
	}
	for i := range 128 {
		h.Policy.AllowedScopes = append(h.Policy.AllowedScopes, scopeOf(i))
	}
	return h
}

var weekDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// scopeOf is a scope of 384 characters with every character a scope may have: pkg:<location>/<@scope/name>.
func scopeOf(i int) string {
	s := fmt.Sprintf("pkg:a%031d/@Scope.name_~*-%d/", i, i)
	return s + strings.Repeat("n", 384-len(s))
}

// copyHello is a deep copy, as the hello would arrive again.
func copyHello(t *testing.T, h wire.Hello) wire.Hello {
	t.Helper()
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var out wire.Hello
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func manyOf[T any](n int, v T) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// Every bounded field of a hello (ADR-0191): at its bound it is taken as sent; one step beyond, it alone is dropped -
// set to what an agent sends that does not report it, a fact to "unknown" - and named with its code; every other field
// stays as sent.
func TestTheHubBoundsEveryFieldOfAHello(t *testing.T) {
	at := helloAtTheBounds()
	h := copyHello(t, at)
	if dropped := boundHello(&h); len(dropped) != 0 || !reflect.DeepEqual(h, copyHello(t, at)) {
		t.Fatalf("a hello at the bounds: dropped %v", dropped)
	}

	type outOfBounds struct {
		change func(*wire.Hello) // takes the field out of its bound
		field  string
		why    string
		drop   func(*wire.Hello) // what the field is after the hub dropped it
	}
	cases := map[string]outOfBounds{}
	add := func(name, field, why string, change, drop func(*wire.Hello)) {
		cases[name] = outOfBounds{change, field, why, drop}
	}
	noVersion := func(h *wire.Hello) { h.AgentVersion = "" }
	add("agent version too long", "agent_version", dropTooLong, func(h *wire.Hello) { h.AgentVersion += "0" }, noVersion)
	add("agent version with a space", "agent_version", dropCharacters,
		func(h *wire.Hello) { h.AgentVersion = "1.0 beta" }, noVersion)
	add("agent version a path", "agent_version", dropCharacters,
		func(h *wire.Hello) { h.AgentVersion = "../1.0" }, noVersion)
	for name, fact := range map[string]func(*wire.Hello) *string{
		"hostname":       func(h *wire.Hello) *string { return &h.Facts.Hostname },
		"os":             func(h *wire.Hello) *string { return &h.Facts.OS },
		"os_id":          func(h *wire.Hello) *string { return &h.Facts.OSID },
		"os_version":     func(h *wire.Hello) *string { return &h.Facts.OSVersion },
		"goos":           func(h *wire.Hello) *string { return &h.Facts.GOOS },
		"arch":           func(h *wire.Hello) *string { return &h.Facts.Arch },
		"virtualization": func(h *wire.Hello) *string { return &h.Facts.Virtualization },
		"container":      func(h *wire.Hello) *string { return &h.Facts.Container },
	} {
		unknown := func(h *wire.Hello) { *fact(h) = facts.Unknown }
		add("fact "+name+" too long", "facts."+name, dropTooLong, func(h *wire.Hello) { *fact(h) += "x" }, unknown)
		add("fact "+name+" a megabyte", "facts."+name, dropTooLong,
			func(h *wire.Hello) { *fact(h) = strings.Repeat("x", 1<<20) }, unknown)
		add("fact "+name+" with a line break", "facts."+name, dropCharacters,
			func(h *wire.Hello) { *fact(h) = "Debian\nGNU/Linux" }, unknown)
		add("fact "+name+" with a NUL", "facts."+name, dropCharacters, func(h *wire.Hello) { *fact(h) = "x\x00" }, unknown)
		add("fact "+name+" not UTF-8", "facts."+name, dropCharacters, func(h *wire.Hello) { *fact(h) = "x\xff" }, unknown)
	}
	noVerbs := func(h *wire.Hello) { h.Verbs = nil }
	add("seventeen verbs", "verbs", dropTooMany, func(h *wire.Hello) { h.Verbs = append(h.Verbs, "check") }, noVerbs)
	add("a hundred thousand verbs", "verbs", dropTooMany,
		func(h *wire.Hello) { h.Verbs = manyOf[protocol.Verb](100000, "check") }, noVerbs)
	add("a verb too long", "verbs", dropTooLong, func(h *wire.Hello) { h.Verbs[3] += "x" }, noVerbs)
	add("a verb in capitals", "verbs", dropCharacters, func(h *wire.Hello) { h.Verbs[0] = "Check" }, noVerbs)
	add("an empty verb", "verbs", dropCharacters, func(h *wire.Hello) { h.Verbs[15] = "" }, noVerbs)
	noRuns := func(h *wire.Hello) { h.Runs = nil }
	add("65 running", "runs", dropTooMany, func(h *wire.Hello) { h.Runs.Running = append(h.Runs.Running, "j") }, noRuns)
	add("65 unacknowledged", "runs", dropTooMany,
		func(h *wire.Hello) { h.Runs.Unacknowledged = append(h.Runs.Unacknowledged, "j") }, noRuns)
	add("a running job id too long", "runs", dropTooLong, func(h *wire.Hello) { h.Runs.Running[1] += "0" }, noRuns)
	add("an unacknowledged job id a path", "runs", dropCharacters,
		func(h *wire.Hello) { h.Runs.Unacknowledged[2] = "../job" }, noRuns)
	noScopes := func(h *wire.Hello) { h.Policy.AllowedScopes = nil }
	add("129 scopes", "policy.allowed_scopes", dropTooMany,
		func(h *wire.Hello) { h.Policy.AllowedScopes = append(h.Policy.AllowedScopes, "all") }, noScopes)
	add("a scope too long", "policy.allowed_scopes", dropTooLong,
		func(h *wire.Hello) { h.Policy.AllowedScopes[0] += "s" }, noScopes)
	add("a scope with a space", "policy.allowed_scopes", dropCharacters,
		func(h *wire.Hello) { h.Policy.AllowedScopes[5] = "app:git lab" }, noScopes)
	add("an empty scope", "policy.allowed_scopes", dropCharacters,
		func(h *wire.Hello) { h.Policy.AllowedScopes[127] = "" }, noScopes)
	add("default scope too long", "policy.default_scope", dropTooLong,
		func(h *wire.Hello) { h.Policy.DefaultScope += "s" }, func(h *wire.Hello) { h.Policy.DefaultScope = "" })
	add("default scope in capitals", "policy.default_scope", dropCharacters,
		func(h *wire.Hello) { h.Policy.DefaultScope = "Security" }, func(h *wire.Hello) { h.Policy.DefaultScope = "" })
	add("auto too long", "policy.auto", dropTooLong,
		func(h *wire.Hello) { h.Policy.Auto += "c" }, func(h *wire.Hello) { h.Policy.Auto = "" })
	add("auto with a dash", "policy.auto", dropCharacters,
		func(h *wire.Hello) { h.Policy.Auto = "-hub" }, func(h *wire.Hello) { h.Policy.Auto = "" })
	add("auto scope too long", "policy.auto_scope", dropTooLong,
		func(h *wire.Hello) { h.Policy.AutoScope = "a" + strings.Repeat("l", 384) },
		func(h *wire.Hello) { h.Policy.AutoScope = "" })
	add("auto scope with a line break", "policy.auto_scope", dropCharacters,
		func(h *wire.Hello) { h.Policy.AutoScope = "all\n" }, func(h *wire.Hello) { h.Policy.AutoScope = "" })
	noApps := func(h *wire.Hello) { h.Policy.Apps = nil }
	add("65 apps", "policy.apps", dropTooMany,
		func(h *wire.Hello) { h.Policy.Apps = append(h.Policy.Apps, wire.AppInfo{ID: "x", Name: "X"}) }, noApps)
	add("an app id too long", "policy.apps", dropTooLong, func(h *wire.Hello) { h.Policy.Apps[0].ID += "x" }, noApps)
	add("an app id in capitals", "policy.apps", dropCharacters,
		func(h *wire.Hello) { h.Policy.Apps[9].ID = "GitLab" }, noApps)
	add("an app name too long", "policy.apps", dropTooLong, func(h *wire.Hello) { h.Policy.Apps[63].Name += "x" }, noApps)
	add("an app name with a control character", "policy.apps", dropCharacters,
		func(h *wire.Hello) { h.Policy.Apps[1].Name = "Git\u0085Lab" }, noApps)
	// check-config takes it (it only asks for a name); the hub does not (ADR-0191, "Bad").
	add("an app name with a tab", "policy.apps", dropCharacters,
		func(h *wire.Hello) { h.Policy.Apps[2].Name = "Git\tLab" }, noApps)
	noWindow := func(h *wire.Hello) { h.Policy.Window = nil }
	add("an install window of 65 entries", "policy.window", rebootWindowBadDays,
		func(h *wire.Hello) { h.Policy.Window.Days = append(h.Policy.Window.Days, "mon") }, noWindow)
	add("an install window of a hundred thousand days", "policy.window", rebootWindowBadDays,
		func(h *wire.Hello) { h.Policy.Window.Days = manyOf(100000, "mon") }, noWindow)
	add("an install window on no day", "policy.window", rebootWindowBadWindow,
		func(h *wire.Hello) { h.Policy.Window.Days = []string{"someday"} }, noWindow)
	add("an install window from a megabyte", "policy.window", rebootWindowBadWindow,
		func(h *wire.Hello) { h.Policy.Window.Start = strings.Repeat("2", 1<<20) }, noWindow)
	noZone := func(h *wire.Hello) { h.Policy.TZ = "" }
	add("a zone too long", "policy.tz", dropTooLong, func(h *wire.Hello) { h.Policy.TZ += "c" }, noZone)
	add("a zone with a space", "policy.tz", dropCharacters,
		func(h *wire.Hello) { h.Policy.TZ = "Europe/Some Town" }, noZone)
	add("a zone that is a path", "policy.tz", dropCharacters, func(h *wire.Hello) { h.Policy.TZ = "../etc/x" }, noZone)
	noInterval := func(h *wire.Hello) { h.Policy.MinIntervalH = 0 }
	add("an interval of 169 hours", "policy.min_interval_h", dropValue,
		func(h *wire.Hello) { h.Policy.MinIntervalH = 169 }, noInterval)
	add("a negative interval", "policy.min_interval_h", dropValue,
		func(h *wire.Hello) { h.Policy.MinIntervalH = -1 }, noInterval)
	add("an interval of a lifetime", "policy.min_interval_h", dropValue,
		func(h *wire.Hello) { h.Policy.MinIntervalH = 1 << 62 }, noInterval)

	for name, c := range cases {
		h := copyHello(t, at)
		c.change(&h)
		dropped := boundHello(&h)
		if want := map[string]string{c.field: c.why}; !reflect.DeepEqual(dropped, want) {
			t.Errorf("%s: dropped %v, want %v", name, dropped, want)
			continue
		}
		want := copyHello(t, at)
		c.drop(&want)
		if !reflect.DeepEqual(h, want) {
			t.Errorf("%s: the hello after the drop is not the one with only %s dropped", name, c.field)
		}
	}
}

// helloPaths lists the fields of a hello - or of any message - by their JSON names, as "policy.window.days"; the
// fields of a list's entries as "policy.apps[].name", of a map's values as "by_key{}.name". kinds says what
// each holds: a time counts as reflect.Struct, a pointer as what it points to - also in a list or a map.
func helloPaths(t reflect.Type, path string, kinds map[string]reflect.Kind) {
	deref := func(t reflect.Type) reflect.Type {
		if t.Kind() == reflect.Pointer {
			return t.Elem()
		}
		return t
	}
	isStruct := func(t reflect.Type) bool { return t.Kind() == reflect.Struct && t != reflect.TypeFor[time.Time]() }
	for i := range t.NumField() {
		sf := t.Field(i)
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		p := strings.TrimPrefix(path+"."+name, ".")
		ft := deref(sf.Type)
		switch {
		case ft == reflect.TypeFor[time.Time]():
			kinds[p] = reflect.Struct
		case ft.Kind() == reflect.Struct:
			helloPaths(ft, p, kinds)
		case ft.Kind() == reflect.Slice && isStruct(deref(ft.Elem())):
			kinds[p] = reflect.Slice
			helloPaths(deref(ft.Elem()), p+"[]", kinds)
		case ft.Kind() == reflect.Map && isStruct(deref(ft.Elem())):
			kinds[p] = reflect.Map
			helloPaths(deref(ft.Elem()), p+"{}", kinds)
		default:
			kinds[p] = ft.Kind()
		}
	}
}

// oversize sets the field target below v far beyond any bound: a megabyte of text or bytes, a hundred thousand
// entries, a number of 2^40. On the way it creates what is absent, and a list's first entry.
func oversize(v reflect.Value, path, target string) {
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		p := strings.TrimPrefix(path+"."+name, ".")
		fv := v.Field(i)
		switch {
		case p == target:
			if fv.Kind() == reflect.Pointer { // a number that may be absent, as a job's progress
				fv.Set(reflect.New(fv.Type().Elem()))
				fv = fv.Elem()
			}
			switch fv.Kind() {
			case reflect.String:
				fv.SetString(strings.Repeat("x", 1<<20))
			case reflect.Int:
				fv.SetInt(1 << 40)
			case reflect.Bool: // a switch the sender must not set, as malicious in a vet request
				fv.SetBool(true)
			case reflect.Slice:
				n := 100000
				if fv.Type().Elem().Kind() == reflect.Uint8 {
					n = 1 << 20
				}
				fv.Set(reflect.MakeSlice(fv.Type(), n, n))
			case reflect.Map: // a hundred thousand entries, as a source's facts
				m := reflect.MakeMapWithSize(fv.Type(), 100000)
				for i := range 100000 {
					m.SetMapIndex(reflect.ValueOf(fmt.Sprintf("k%d", i)).Convert(fv.Type().Key()),
						reflect.Zero(fv.Type().Elem()))
				}
				fv.Set(m)
			}
		case strings.HasPrefix(target, p+"[]."):
			if fv.Len() == 0 {
				fv.Set(reflect.MakeSlice(fv.Type(), 1, 1))
			}
			ev := fv.Index(0)
			if ev.Kind() == reflect.Pointer { // a list of pointers to entries
				if ev.IsNil() {
					ev.Set(reflect.New(ev.Type().Elem()))
				}
				ev = ev.Elem()
			}
			oversize(ev, p+"[]", target)
		case strings.HasPrefix(target, p+"{}."):
			// A map's values are not addressable: one value is built, oversized and put in under the key "k".
			mt := fv.Type()
			if fv.IsNil() {
				fv.Set(reflect.MakeMap(mt))
			}
			et, ptr := mt.Elem(), mt.Elem().Kind() == reflect.Pointer
			if ptr {
				et = et.Elem()
			}
			ev := reflect.New(et).Elem()
			oversize(ev, p+"{}", target)
			if ptr {
				ev = ev.Addr()
			}
			fv.SetMapIndex(reflect.ValueOf("k").Convert(mt.Key()), ev)
		case strings.HasPrefix(target, p+"."):
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			oversize(fv, p, target)
		}
	}
}

// Every field of a hello is bounded - also one added later, which this test finds by itself: far beyond any bound, a
// field of the identity is for login to refuse, a switch or a time is bounded by its type, and any other field is not
// taken (boundHello, or the reboot window's check of ADR-0187). A new field fails here until it has its bound.
func TestEveryFieldOfAHelloIsBounded(t *testing.T) {
	identityFields := []string{"t", "node", "public_key", "signature"} // helloIdentityHolds, at login
	byType := []string{"last_install", "policy.reboot_allowed", "policy.agent_update", "policy.reboot_window_only",
		"policy.outside_window"}
	kinds := map[string]reflect.Kind{}
	helloPaths(reflect.TypeFor[wire.Hello](), "", kinds)
	for _, p := range append(append([]string{}, identityFields...), byType...) {
		if _, ok := kinds[p]; !ok {
			t.Errorf("%s: no such field of the hello", p)
		}
	}
	for _, p := range byType {
		if k := kinds[p]; k != reflect.Bool && k != reflect.Struct {
			t.Errorf("%s holds a %s: not bounded by its type", p, k)
		}
	}
	// The base is taken whole: a reboot window with valid days and a zone this hub knows, so that only the oversized
	// field can make the hub drop something.
	base := helloAtTheBounds()
	base.Policy.TZ = "UTC"
	base.Policy.RebootWindow = &wire.WindowInfo{Days: weekDays, Start: "03:00", End: "04:00"}
	if b := copyHello(t, base); rebootWindowRefusal(b.Policy) != "" || len(boundHello(&b)) != 0 {
		t.Fatal("the base hello is not taken whole")
	}
	checked := 0
	for p := range kinds {
		if slices.Contains(identityFields, p) || slices.Contains(byType, p) {
			continue
		}
		h := copyHello(t, base)
		oversize(reflect.ValueOf(&h).Elem(), "", p)
		if rebootWindowRefusal(h.Policy) == "" && len(boundHello(&h)) == 0 {
			t.Errorf("%s: far beyond any bound, and taken", p)
		}
		checked++
	}
	if checked < 25 {
		t.Fatalf("only %d fields checked: %v", checked, kinds)
	}
}

// What agents send today, and what agents before a field send, is taken as sent.
func TestTheHubTakesWhatAgentsSend(t *testing.T) {
	plain := func(h *wire.Hello) {}
	for name, change := range map[string]func(*wire.Hello){
		"at the bounds":      plain,
		"a release":          func(h *wire.Hello) { h.AgentVersion = "0.48.0" },
		"a pull request":     func(h *wire.Hello) { h.AgentVersion = "0.0.0-pr" },
		"a hand build":       func(h *wire.Hello) { h.AgentVersion = "dev" },
		"a candidate":        func(h *wire.Hello) { h.AgentVersion = "1.2.3-rc.1+build.7" },
		"no version":         func(h *wire.Hello) { h.AgentVersion = "" },
		"no facts":           func(h *wire.Hello) { h.Facts = facts.Host{} },
		"unknown facts":      func(h *wire.Hello) { h.Facts = facts.Host{OS: facts.Unknown, Arch: facts.Unknown} },
		"a device's facts":   func(h *wire.Hello) { h.Facts = facts.Host{OS: "Appliance OS 2.7.2-RELEASE (amd64)", Arch: "Model 7"} },
		"Windows' facts":     func(h *wire.Hello) { h.Facts.OS, h.Facts.OSVersion = "Windows 11 Pro 24H2", "10.0.26100.1000" },
		"the verbs of today": func(h *wire.Hello) { h.Verbs = []protocol.Verb{"check", "install", "reboot", "agent_update", "backup"} },
		"no verbs":           func(h *wire.Hello) { h.Verbs = nil },
		"no runs":            func(h *wire.Hello) { h.Runs = nil },
		"nothing running":    func(h *wire.Hello) { h.Runs = &wire.Runs{Running: []string{}, Unacknowledged: []string{}} },
		"an empty policy":    func(h *wire.Hello) { h.Policy = wire.PolicySummary{} },
		"scopes of agent.yaml": func(h *wire.Hello) {
			h.Policy.AllowedScopes = []string{"security", "all", "app:gitlab", "image:stack/*", "image:*/web.1"}
		},
		"the scopes of the protocol": func(h *wire.Hello) {
			h.Policy.AllowedScopes = []string{"pkg:node-red", "pkg:global-npm/@scope/name.js~1", "deps:venv_1", "images"}
		},
		"a day twice": func(h *wire.Hello) {
			h.Policy.Window = &wire.WindowInfo{Days: []string{"mon", "mon"}, Start: "02:00", End: "05:00"}
		},
		"eight entries": func(h *wire.Hello) {
			h.Policy.Window.Days = append(append([]string{}, weekDays...), "mon")
		},
		"an app with umlauts": func(h *wire.Hello) { h.Policy.Apps = []wire.AppInfo{{ID: "notes", Name: "Notizen für alle"}} },
		"no install window":   func(h *wire.Hello) { h.Policy.Window = nil },
		"a window over midnight": func(h *wire.Hello) {
			h.Policy.Window = &wire.WindowInfo{Days: []string{"sat"}, Start: "23:30", End: "00:30"}
		},
		"no interval": func(h *wire.Hello) { h.Policy.MinIntervalH = 0 },
		"one hour":    func(h *wire.Hello) { h.Policy.MinIntervalH = 1 },
		"no zone":     func(h *wire.Hello) { h.Policy.TZ = "" },
		"UTC":         func(h *wire.Hello) { h.Policy.TZ = "UTC" },
		"Local":       func(h *wire.Hello) { h.Policy.TZ = "Local" },
		"a long zone": func(h *wire.Hello) { h.Policy.TZ = "America/Argentina/Buenos_Aires" },
		"a dash":      func(h *wire.Hello) { h.Policy.TZ = "America/Port-au-Prince" },
		"a sign":      func(h *wire.Hello) { h.Policy.TZ = "Etc/GMT+5" },
	} {
		h := helloAtTheBounds()
		change(&h)
		sent := copyHello(t, h)
		if dropped := boundHello(&h); len(dropped) != 0 || !reflect.DeepEqual(h, sent) {
			t.Errorf("%s: dropped %v", name, dropped)
		}
	}
}

// loginOverTunnel sends h over a real tunnel connection, signed with priv for its challenge unless it carries a
// signature already, and returns the status the hub answers with.
func loginOverTunnel(t *testing.T, ts *httptest.Server, h wire.Hello, priv ed25519.PrivateKey) wire.Status {
	t.Helper()
	conn := dialTunnel(t, ts)
	_, raw, err := conn.Receive(5 * time.Second)
	var ch wire.Challenge
	if err != nil || json.Unmarshal(raw, &ch) != nil {
		t.Fatalf("challenge: %v", err)
	}
	if h.Signature == nil {
		h.Signature = ed25519.Sign(priv, wire.HelloSigned(ch.Nonce, h.Node, h.PublicKey))
	}
	if err := conn.Send(h); err != nil {
		t.Fatal(err)
	}
	_, raw, err = conn.Receive(5 * time.Second)
	var st wire.Status
	if err != nil || json.Unmarshal(raw, &st) != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// Over a real tunnel (ADR-0191): a hello with descriptive fields out of bounds connects without them - stored, shown to
// Home Assistant and logged without them - and one event names them; a hello in bounds causes none.
func TestAHelloWithFieldsOutOfBoundsConnectsWithoutThem(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	login := func(node string, change func(*wire.Hello)) string {
		t.Helper()
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		h := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "0.48.0",
			Facts: facts.Host{OS: "TestOS 1", Arch: "amd64", GOOS: "linux"},
			Verbs: []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall},
			Policy: wire.PolicySummary{AllowedScopes: []string{"security", "app:notes"}, DefaultScope: "security",
				Auto: "off", Apps: []wire.AppInfo{{ID: "notes", Name: "Notes"}}, MinIntervalH: 6}}
		change(&h)
		if st := loginOverTunnel(t, ts, h, priv); st.State != wire.StatePendingApproval {
			t.Fatalf("%s: %+v", node, st)
		}
		if code := f.srv.Reg.Decide(node, wire.StateApproved, identity.Fingerprint(pub), f.now); code != AdminOK {
			t.Fatal(code)
		}
		return identity.Fingerprint(pub)
	}
	login("bounded", func(*wire.Hello) {})
	fp := login("oversized", func(h *wire.Hello) {
		h.AgentVersion = strings.Repeat("9", 1<<20)
		h.Facts.OS = strings.Repeat("x", 1<<20)
		h.Policy.Apps = manyOf(10000, wire.AppInfo{ID: "notes", Name: "Notes"})
		h.Policy.MinIntervalH = 1 << 40
	})

	kept, _ := f.srv.Reg.Get("bounded")
	if kept.Facts.OS != "TestOS 1" || kept.AgentVersion != "0.48.0" || len(kept.Policy.Apps) != 1 ||
		kept.Policy.MinIntervalH != 6 {
		t.Fatalf("bounded: %+v", kept)
	}
	e, _ := f.srv.Reg.Get("oversized")
	if e.Facts.OS != facts.Unknown || e.AgentVersion != "" || e.Policy.Apps != nil || e.Policy.MinIntervalH != 0 ||
		e.Facts.Arch != "amd64" || !reflect.DeepEqual(e.Policy.AllowedScopes, []string{"security", "app:notes"}) ||
		len(e.Verbs) != 2 {
		t.Fatalf("oversized, stored: %+v", e)
	}
	v := f.view(t, "oversized", f.now)
	if v.Device.Model != facts.Unknown || v.Device.AgentVersion != "" || len(v.Capabilities.Apps) != 0 ||
		v.Capabilities.DefaultScope != "security" || v.NextInstall != nil {
		t.Fatalf("oversized, view: %+v", v)
	}
	got := eventLines(t, f, "hello_fields_dropped")
	if len(got) != 1 || got[0]["node"] != "oversized" || got[0]["fingerprint"] != fp ||
		!reflect.DeepEqual(got[0]["fields"], map[string]any{"agent_version": "too_long", "facts.os": "too_long",
			"policy.apps": "too_many", "policy.min_interval_h": "value"}) {
		t.Fatalf("events: %v", got)
	}
	for _, h := range eventLines(t, f, "hello") {
		if h["node"] == "oversized" && h["agent_version"] != "" {
			t.Fatalf("the hello event logs the dropped version: %v", h)
		}
	}
}

// A host waiting for approval shows in Home Assistant with its facts bounded too.
func TestAWaitingHostShowsOnlyBoundedFacts(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := wire.Hello{T: wire.THello, Node: "waiting", PublicKey: pub, AgentVersion: "0.48.0",
		Facts: facts.Host{OS: "TestOS\u001b[2J"}}
	if st := loginOverTunnel(t, ts, h, priv); st.State != wire.StatePendingApproval {
		t.Fatalf("%+v", st)
	}
	hv := f.srv.HubView(f.srv.Reg.List())
	if len(hv.Waiting) != 1 || hv.Waiting[0].OS != facts.Unknown {
		t.Fatalf("waiting: %+v", hv.Waiting)
	}
}

// What the hub drops is logged, with the key's fingerprint, only for a hello the register takes (ADR-0191). One it
// refuses - another key under a known name, a host the operator rejected, no room for another waiting one - names a
// node that is not its own: no drops are logged under that name.
func TestARefusedHelloLogsNoDrops(t *testing.T) {
	f := newViewFixture(t) // at most 5 hosts wait for approval
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	login := func(node string, want wire.NodeState, reason wire.StatusReason) string {
		t.Helper()
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		if node == "rejected" {
			// The operator rejected the host with this key.
			if _, _, err := f.srv.Reg.Admit(wire.Hello{Node: node, PublicKey: pub}, identity.Fingerprint(pub), f.now,
				5); err != nil || f.srv.Reg.Decide(node, wire.StateRejected, "", f.now) != AdminOK {
				t.Fatal(err)
			}
		}
		h := wire.Hello{T: wire.THello, Node: node, PublicKey: pub, AgentVersion: "0.48.0",
			Facts: facts.Host{OS: strings.Repeat("x", 1000)},
			Policy: wire.PolicySummary{RebootAllowed: true,
				RebootWindow: &wire.WindowInfo{Days: manyOf(65, "mon"), Start: "03:00", End: "04:00"}}}
		if st := loginOverTunnel(t, ts, h, priv); st.State != want || st.Reason != reason {
			t.Fatalf("%s: %+v", node, st)
		}
		return identity.Fingerprint(pub)
	}
	fp := login("first", wire.StatePendingApproval, wire.ReasonNone)
	f.host(t, "known", nil, wire.PolicySummary{}, true)
	login("known", wire.StateRejected, wire.ReasonKeyMismatch)
	login("rejected", wire.StateRejected, wire.ReasonRejected)
	for i := range 4 {
		f.host(t, fmt.Sprintf("waiting-%d", i), nil, wire.PolicySummary{}, false)
	}
	login("newcomer", wire.StateRejected, wire.ReasonTooManyPending)

	for event, want := range map[string]map[string]any{
		"hello_fields_dropped":  {"fields": map[string]any{"facts.os": "too_long"}},
		"reboot_window_refused": {"reason": "days"},
	} {
		got := eventLines(t, f, event)
		if len(got) != 1 || got[0]["node"] != "first" || got[0]["fingerprint"] != fp {
			t.Fatalf("%s: %v", event, got)
		}
		for k, v := range want {
			if !reflect.DeepEqual(got[0][k], v) {
				t.Fatalf("%s: %v", event, got)
			}
		}
	}
}

// The identity of a hello is no field to drop: out of its bound, the hello is refused with bad_hello, as before, and
// nothing of it is stored. At the bound it is taken.
func TestAHelloWithItsIdentityOutOfBoundsIsRefused(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	for name, c := range map[string]struct {
		node   string
		change func(h *wire.Hello, priv ed25519.PrivateKey)
		want   wire.NodeState
	}{
		"a node name at the bound":  {strings.Repeat("a", 63), nil, wire.StatePendingApproval},
		"a node name too long":      {strings.Repeat("b", 64), nil, wire.StateRejected},
		"a node name of a megabyte": {strings.Repeat("c", 1<<20), nil, wire.StateRejected},
		"a node name in capitals":   {"Host-D", nil, wire.StateRejected},
		"a key too long": {"host-e", func(h *wire.Hello, _ ed25519.PrivateKey) {
			h.PublicKey = append(h.PublicKey, 0)
		}, wire.StateRejected},
		"a key of a megabyte": {"host-f", func(h *wire.Hello, _ ed25519.PrivateKey) {
			h.PublicKey = make([]byte, 1<<20)
		}, wire.StateRejected},
		"a signature too long": {"host-g", func(h *wire.Hello, priv ed25519.PrivateKey) {
			h.Signature = append(ed25519.Sign(priv, []byte("x")), make([]byte, 1<<20)...)
		}, wire.StateRejected},
		"another type": {"host-h", func(h *wire.Hello, _ ed25519.PrivateKey) { h.T = wire.TReport }, wire.StateRejected},
	} {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		h := wire.Hello{T: wire.THello, Node: c.node, PublicKey: pub, AgentVersion: "0.48.0"}
		if c.change != nil {
			c.change(&h, priv)
		}
		st := loginOverTunnel(t, ts, h, priv) // signed over the challenge, the name and the key as sent
		_, stored := f.srv.Reg.Get(c.node)
		switch {
		case st.State != c.want:
			t.Errorf("%s: %+v", name, st)
		case c.want == wire.StateRejected && (st.Reason != wire.ReasonBadHello || stored):
			t.Errorf("%s: %+v, stored %v", name, st, stored)
		case c.want != wire.StateRejected && !stored:
			t.Errorf("%s: not stored", name)
		}
	}
	if n := len(eventLines(t, f, "hello_refused")); n != 7 {
		t.Fatalf("%d refusals logged, want 7", n)
	}
	if n := len(eventLines(t, f, "hello_fields_dropped")); n != 0 {
		t.Fatalf("a refused hello had fields dropped: %d", n)
	}
}
