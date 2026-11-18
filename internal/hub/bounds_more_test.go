package hub

import (
	"io"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// An excerpt is cleaned, never emptied (ADR-0195): ANSI colours and other control characters but CR, LF and tab become
// U+FFFD, bytes that are no UTF-8 too; of a longer one its end is kept, starting at a whole character.
func TestAnExcerptIsCleanedNeverEmptied(t *testing.T) {
	for in, want := range map[string]struct{ out, why string }{
		"":                                   {"", ""},
		"E: failed\r\n\tbecause":             {"E: failed\r\n\tbecause", ""},
		"\x1b[1;31mE:\x1b[0m failed":         {"�[1;31mE:�[0m failed", dropCharacters},
		"\x1b]0;title\x07\x00\u0085\u009b2J": {"�]0;title����2J", dropCharacters},
		"f\xffailed":                         {"f�ailed", dropCharacters},
		"\x1b":                               {"�", dropCharacters},
	} {
		if out, why := cleanExcerpt(in); out != want.out || why != want.why {
			t.Errorf("%q: %q %q, want %q %q", in, out, why, want.out, want.why)
		}
	}
	for name, in := range map[string]string{
		"umlauts":     strings.Repeat("Ö", 10000),            // two bytes each, cut inside one
		"escapes":     strings.Repeat("\x1b", maxExcerptLen), // each grows to three bytes
		"a long line": strings.Repeat("x", 1<<20) + "the end",
	} {
		out, why := cleanExcerpt(in)
		if out == "" || len(out) > maxExcerptLen || !utf8.ValidString(out) || why == "" ||
			strings.ContainsFunc(out, excerptControl) {
			t.Errorf("%s: %d bytes, %q", name, len(out), why)
		}
	}
	if out, _ := cleanExcerpt(strings.Repeat("x", 1<<20) + "the end"); !strings.HasSuffix(out, "the end") {
		t.Error("not the end of the excerpt")
	}
}

// An agent's own detail of an interrupted run stays its own (ADR-0195): cleaned, it is never empty, so the hub does not
// take it for its own guess at a disconnect and logs no run_lost when the agent's next hello names neither (handshake).
func TestAnInterruptedRunKeepsItsCleanedDetail(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbInstall}, wire.PolicySummary{}, true)
	st := wire.JobStatus{T: wire.TJobStatus, JobID: "j1", Code: protocol.CodeInterrupted,
		Detail: "\x1b[31mthe run was cut short\x1b[0m"}
	if o := boundJobStatus(&st); !o.taken() || st.Detail == "" {
		t.Fatalf("detail %q, not taken %v", st.Detail, o.notTaken())
	}
	fin := f.now
	run := Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all", Code: st.Code, Detail: st.Detail, Started: f.now,
		Finished: &fin}
	if err := f.srv.Reg.SetRun("h", run); err != nil {
		t.Fatal(err)
	}
	f.srv.reconcileRuns("h", wire.Runs{})
	if e, _ := f.srv.Reg.Get("h"); e.LastRun.Detail != "�[31mthe run was cut short�[0m" ||
		len(eventLines(t, f, "run_lost")) != 0 {
		t.Fatalf("run %+v, run_lost %v", e.LastRun, eventLines(t, f, "run_lost"))
	}
}

// A location id longer than a scope takes - up to 64, as an agent might make one - is kept and shown, but offers no
// update by button: Resolve would refuse the command (ADR-0195).
func TestALongLocationIDIsKeptButOffersNoButton(t *testing.T) {
	pk, err := NewPackages(t.TempDir(), &OSVConfig{Ecosystems: []string{"npm"}}, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	long := "npm-global-firstname-lastname-example-domain-001"
	inv := wire.Inventory{T: wire.TInventory, CheckedAt: time.Now(), Locations: []inventory.Location{
		{ID: long, Kind: inventory.KindNpmGlobal, Ecosystem: versions.Npm, Update: true, Deps: true,
			Packages: []inventory.Package{{Name: "npm", Version: "10.9.2", Direct: true}}},
		{ID: "node-red", Kind: inventory.KindNpmDir, Ecosystem: versions.Npm, Update: true, Deps: true,
			Packages: []inventory.Package{{Name: "node-red", Version: "4.0.9", Direct: true}}}}}
	if o := boundInventory(&inv); !o.whole() {
		t.Fatalf("not taken: %v", o.notTaken())
	}
	if err := pk.SetInventory("h", inv); err != nil {
		t.Fatal(err)
	}
	if v := pk.View("h"); v == nil || len(v.Locations) != 2 || v.Locations[0].ID != long {
		t.Fatalf("view: %+v", v)
	}
	if u, r := pk.UpdatableLocations("h"), pk.RefreshableLocations("h"); !reflect.DeepEqual(u, []string{"node-red"}) ||
		!reflect.DeepEqual(r, []string{"node-red"}) {
		t.Fatalf("updatable %v, refreshable %v", u, r)
	}
}

// ReportNotTaken, which the sources' tests use, names what the hub would not take of a report, and leaves the report as
// it was.
func TestReportNotTakenNamesWhatTheHubDrops(t *testing.T) {
	r := aptReport(time.Now(), 2, 1, nil)
	r.Sources[0].Detail, r.Sources[0].Packages[1].To = "\x1b[31mE\x1b[0m", "2\n"
	got := ReportNotTaken(r)
	if want := map[string]string{"sources[].detail": dropCharacters, "sources[].packages": dropCharacters}; !reflect.DeepEqual(got, want) {
		t.Fatalf("not taken: %v", got)
	}
	if r.Sources[0].Detail != "\x1b[31mE\x1b[0m" || len(r.Sources[0].Packages) != 2 {
		t.Fatalf("the report changed: %+v", r.Sources[0])
	}
	r.Sources[0].Source = "Apt"
	if got := ReportNotTaken(r); !reflect.DeepEqual(got, map[string]string{"sources[].source": dropCharacters}) {
		t.Fatalf("refused: %v", got)
	}
	if got := ReportNotTaken(aptReport(time.Now(), 2, 1, nil)); got != nil {
		t.Fatalf("a report in bounds: %v", got)
	}
}

// walkProbe holds what the wire types do not have yet: a list of pointers, maps of entries and of pointers to them.
type walkProbe struct {
	List []*struct {
		Name string `json:"name"`
	} `json:"list"`
	ByKey map[string]struct {
		Value int `json:"value"`
	} `json:"by_key"`
	ByPtr map[string]*struct {
		Text string `json:"text"`
	} `json:"by_ptr"`
}

// The field walk of the reflection tests reaches the fields of entries behind a pointer in a list and in a map, so a
// later wire type with them is checked field by field too.
func TestTheFieldWalkReachesPointersAndMaps(t *testing.T) {
	kinds := map[string]reflect.Kind{}
	helloPaths(reflect.TypeFor[walkProbe](), "", kinds)
	if want := map[string]reflect.Kind{"list": reflect.Slice, "list[].name": reflect.String, "by_key": reflect.Map,
		"by_key{}.value": reflect.Int, "by_ptr": reflect.Map, "by_ptr{}.text": reflect.String}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("paths: %v", kinds)
	}
	var v walkProbe
	for _, p := range []string{"list[].name", "by_key{}.value", "by_ptr{}.text"} {
		oversize(reflect.ValueOf(&v).Elem(), "", p)
	}
	if len(v.List) != 1 || len(v.List[0].Name) != 1<<20 || v.ByKey["k"].Value != 1<<40 || len(v.ByPtr["k"].Text) != 1<<20 {
		t.Fatalf("oversized: %d %d %v", len(v.List), v.ByKey["k"].Value, v.ByPtr["k"] != nil)
	}
}
