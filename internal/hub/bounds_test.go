package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

var boundsAt = time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)

// keyOf is a key of n characters: a state, code, phase or fact key as long as the hub takes it.
func keyOf(n int) string { return "k" + strings.Repeat("y", n-1) }

// excerptOf is n bytes of a tool's output: lines, a tab, CRLF and LF, umlauts.
func excerptOf(n int) string {
	s := strings.Repeat("E: Fehler\tbeim Öffnen\r\n", n/26) // 26 bytes each
	return s + strings.Repeat("x", n-len(s))
}

// withFirst is n entries of v, the first of them first.
func withFirst[T any](n int, v, first T) []T {
	out := manyOf(n, v)
	out[0] = first
	return out
}

// reportAtTheBounds is a report with every bounded field at its bound: the most sources and entries, the longest
// texts and keys, the largest counts.
func reportAtTheBounds() wire.Report {
	r := wire.Report{T: wire.TReport, CheckedAt: boundsAt, Reboot: &source.Reboot{Required: true,
		Reason: protocol.RebootReason(keyOf(64)), Packages: withFirst(1024, "linux-image", textOf(1024)),
		Running: textOf(255), Newest: textOf(255), Checked: manyOf(32, keyOf(64))}}
	for i := range 64 {
		r.Sources = append(r.Sources, source.Report{Source: fmt.Sprintf("src-%d", i), State: protocol.SourceUpToDate,
			CheckedAt: boundsAt})
	}
	s := &r.Sources[0]
	s.Source, s.State, s.Code = "a"+strings.Repeat("-", 63), protocol.SourceState(keyOf(64)), protocol.Code(keyOf(64))
	s.Pending, s.Security, s.KeptBack = 1_000_000, 1_000_000, 1_000_000
	s.Packages = withFirst(10000, source.Package{Name: "pkg", From: "1", To: "2"}, source.Package{Name: textOf(1024),
		From: textOf(1024), To: textOf(1024), Origins: withFirst(64, "Debian:12/stable", textOf(1024)), Security: true,
		Major: true, KeptBack: true})
	s.Removals = withFirst(10000, "old", textOf(1024))
	s.Facts = map[string]string{}
	for i := range 64 {
		s.Facts[fmt.Sprintf("f%063d", i)] = "on"
	}
	s.Facts["f"+strings.Repeat("0", 63)] = textOf(16 << 10)
	s.Detail = excerptOf(16 << 10)
	return r
}

// inventoryAtTheBounds is an inventory with every bounded field at its bound: the most locations, a location with the
// most packages, a package with every field at its bound, an image with the most packages inside.
func inventoryAtTheBounds() wire.Inventory {
	at := boundsAt
	inv := wire.Inventory{T: wire.TInventory, CheckedAt: at, OutdatedAt: &at, MinAgeDays: 90, MinAgeDaysSecurity: 90}
	for i := range 1024 {
		inv.Locations = append(inv.Locations, inventory.Location{ID: fmt.Sprintf("loc-%d", i), Kind: inventory.KindVenv,
			Ecosystem: versions.PyPI, Path: "/opt/app", Packages: []inventory.Package{{Name: "pkg", Version: "1.0"}}})
	}
	l := &inv.Locations[0]
	l.ID, l.Kind, l.Ecosystem = "l"+strings.Repeat("0", 63), "k"+strings.Repeat("-", 63), versions.Ecosystem("D"+
		strings.Repeat(":", 63))
	l.Path, l.Code, l.Update, l.Deps, l.ComparedAt = textOf(4096), keyOf(64), true, true, &at
	l.Skipped, l.OutdatedFailed, l.NotInRegistry = 1_000_000, 1_000_000, 1_000_000
	service := strings.Repeat("p", 56) + "/" + strings.Repeat("S", 56)
	l.Packages = withFirst(20000, inventory.Package{Name: "pkg", Version: "1.0"}, inventory.Package{
		Name: textOf(1024), Version: textOf(1024), Direct: true, Via: textOf(1024), Latest: textOf(1024),
		Candidate: textOf(1024), SecurityCandidate: textOf(1024), LatestAt: &at, Major: true, Digest: textOf(1024),
		Rebuilt: textOf(1024), RebuiltAt: &at, RebuiltCandidate: true, SameMajor: textOf(1024), CompareFailed: keyOf(64),
		Holds: withFirst(32, inventory.Hold{By: "a@1", Range: "^1"}, inventory.Hold{By: textOf(1024),
			Range: textOf(1024), Bundled: true}),
		Containers: withFirst(1024, "web-1", textOf(1024)), Updatable: withFirst(1024, "stack/web", service),
		OS: &inventory.ImageOS{ID: textOf(1024), Version: textOf(1024), Code: keyOf(64),
			Packages: withFirst(5000, inventory.OSPackage{Name: "libc6", Version: "2.36-9"}, inventory.OSPackage{
				Name: textOf(1024), Version: textOf(1024), Source: textOf(1024), SourceVersion: textOf(1024)})}})
	return inv
}

// jobStatusAtTheBounds is a job status with every bounded field at its bound.
func jobStatusAtTheBounds() wire.JobStatus {
	at, pct := boundsAt, 100
	return wire.JobStatus{T: wire.TJobStatus, JobID: strings.Repeat("a", 64), Phase: protocol.Phase(keyOf(64)),
		Code: protocol.Code(keyOf(64)), Reason: protocol.InvalidReason(keyOf(64)), Detail: excerptOf(16 << 10),
		Progress: &pct, Item: textOf(1024), InstallStarted: &at,
		Summary: &wire.RunSummary{PackageCount: 1_000_000, Seconds: 1_000_000, LogFile: textOf(4096),
			Packages: withFirst(100, "pkg", textOf(1024)), OutdatedServices: withFirst(1024, "a.service", textOf(1024)),
			Replaced: withFirst(1024, "a -> b", textOf(1024)), Restarted: withFirst(1024, "web-1", textOf(1024))}}
}

// boundCase takes one field of a message at the bounds one step beyond: the hub refuses the message for field (drop
// nil), or drops or cleans field and the message is then the one at the bounds with drop applied.
type boundCase[M any] struct {
	field, why string
	change     func(*M)
	drop       func(*M)
}

// checkBounds: a message at the bounds is taken as built; each case's change makes the hub refuse it, or drop or clean
// that field alone, with the case's code - every other field stays as built.
func checkBounds[M any](t *testing.T, build func() M, bound func(*M) *outOfBounds, cases map[string]boundCase[M]) {
	t.Helper()
	m := build()
	if o := bound(&m); !o.whole() || !reflect.DeepEqual(m, build()) {
		t.Fatalf("at the bounds: not taken %v", o.notTaken())
	}
	for name, c := range cases {
		m := build()
		c.change(&m)
		o := bound(&m)
		want := map[string]string{c.field: c.why}
		switch {
		case c.drop == nil && !reflect.DeepEqual(o.refused, want):
			t.Errorf("%s: not taken %v; want refused %v", name, o.notTaken(), want)
		case c.drop == nil:
		case !o.taken() || !reflect.DeepEqual(o.notTaken(), want):
			t.Errorf("%s: refused %v, not taken %v; want %v", name, o.refused, o.notTaken(), want)
		default:
			w := build()
			c.drop(&w)
			if !reflect.DeepEqual(m, w) {
				t.Errorf("%s: the message after the drop is not the one with only %s dropped", name, c.field)
			}
		}
	}
}

// Every bounded field of a report (ADR-0195): at its bound it is taken as sent. One step beyond, a source's identity,
// state or counts refuse the report; every other field alone is dropped, to what an agent sends that does not report
// it, and an excerpt is cleaned: its control characters become U+FFFD, of a longer one the end is kept.
func TestTheHubBoundsEveryFieldOfAReport(t *testing.T) {
	type c = boundCase[wire.Report]
	src := func(r *wire.Report) *source.Report { return &r.Sources[0] }
	refused := func(field, why string, change func(*wire.Report)) c { return c{field, why, change, nil} }
	noPackages := func(r *wire.Report) { src(r).Packages = nil }
	noRemovals := func(r *wire.Report) { src(r).Removals = nil }
	noFacts := func(r *wire.Report) { src(r).Facts = nil }
	detail := func(d string) func(r *wire.Report) { return func(r *wire.Report) { src(r).Detail = d } }
	noReboot := func(r *wire.Report) { r.Reboot = nil }
	checkBounds(t, reportAtTheBounds, boundReport, map[string]c{
		"65 sources": refused("sources", dropTooMany, func(r *wire.Report) {
			r.Sources = append(r.Sources, source.Report{Source: "apt", State: protocol.SourceUpToDate})
		}),
		"a source name too long":    refused("sources[].source", dropTooLong, func(r *wire.Report) { src(r).Source += "-" }),
		"a source name in capitals": refused("sources[].source", dropCharacters, func(r *wire.Report) { r.Sources[5].Source = "Apt" }),
		"no source name":            refused("sources[].source", dropCharacters, func(r *wire.Report) { r.Sources[63].Source = "" }),
		"a state too long":          refused("sources[].state", dropTooLong, func(r *wire.Report) { src(r).State += "y" }),
		"a state with a space":      refused("sources[].state", dropCharacters, func(r *wire.Report) { r.Sources[1].State = "up to date" }),
		"a code with a dash":        refused("sources[].code", dropCharacters, func(r *wire.Report) { src(r).Code = "check-failed" }),
		"a code with an escape":     refused("sources[].code", dropCharacters, func(r *wire.Report) { src(r).Code = "x\x1b[2J" }),
		"a million and one pending": refused("sources[].pending", dropValue, func(r *wire.Report) { src(r).Pending++ }),
		"negative security":         refused("sources[].security", dropValue, func(r *wire.Report) { r.Sources[2].Security = -1 }),
		"kept back 2^40":            refused("sources[].kept_back", dropValue, func(r *wire.Report) { src(r).KeptBack = 1 << 40 }),
		"10001 packages": {"sources[].packages", dropTooMany, func(r *wire.Report) {
			src(r).Packages = append(src(r).Packages, source.Package{Name: "x"})
		}, noPackages},
		"a package name too long": {"sources[].packages", dropTooLong,
			func(r *wire.Report) { src(r).Packages[0].Name += "x" }, noPackages},
		"a version with an escape": {"sources[].packages", dropCharacters,
			func(r *wire.Report) { src(r).Packages[3].To = "1.0\x1b[2J" }, noPackages},
		"65 origins": {"sources[].packages", dropTooMany,
			func(r *wire.Report) { src(r).Packages[0].Origins = append(src(r).Packages[0].Origins, "x") }, noPackages},
		"an origin not UTF-8": {"sources[].packages", dropCharacters,
			func(r *wire.Report) { src(r).Packages[0].Origins[9] = "x\xff" }, noPackages},
		"10001 removals": {"sources[].removals", dropTooMany,
			func(r *wire.Report) { src(r).Removals = append(src(r).Removals, "x") }, noRemovals},
		"a removal with a NUL": {"sources[].removals", dropCharacters,
			func(r *wire.Report) { src(r).Removals[5] = "x\x00" }, noRemovals},
		"65 facts": {"sources[].facts", dropTooMany, func(r *wire.Report) { src(r).Facts["more"] = "1" }, noFacts},
		"a fact key in capitals": {"sources[].facts", dropCharacters, func(r *wire.Report) {
			delete(src(r).Facts, fmt.Sprintf("f%063d", 5))
			src(r).Facts["Apt_version"] = "2.6"
		}, noFacts},
		"a fact key too long": {"sources[].facts", dropTooLong, func(r *wire.Report) {
			delete(src(r).Facts, fmt.Sprintf("f%063d", 5))
			src(r).Facts[keyOf(65)] = "2.6"
		}, noFacts},
		"a fact value too long": {"sources[].facts", dropTooLong,
			func(r *wire.Report) { src(r).Facts["f"+strings.Repeat("0", 63)] += "x" }, noFacts},
		"a fact value with a line break": {"sources[].facts", dropCharacters,
			func(r *wire.Report) { src(r).Facts[fmt.Sprintf("f%063d", 7)] = "on\noff" }, noFacts},
		"a detail too long: its end is kept": {"sources[].detail", dropTooLong, func(r *wire.Report) { src(r).Detail += "x" },
			detail(excerptOf(16 << 10)[1:] + "x")},
		"a detail in ANSI colours": {"sources[].detail", dropCharacters,
			detail("\x1b[1;31mE:\x1b[0m failed"), detail("�[1;31mE:�[0m failed")},
		"a detail with a NUL": {"sources[].detail", dropCharacters, detail("E: failed\x00"), detail("E: failed�")},
		"a detail with a C1 control": {"sources[].detail", dropCharacters, detail("E: failed\u0085"),
			detail("E: failed�")},
		"a detail not UTF-8": {"sources[].detail", dropCharacters, detail("E: f\xffiled"), detail("E: f�iled")},
		"33 signals": {"reboot.checked", dropTooMany,
			func(r *wire.Report) { r.Reboot.Checked = append(r.Reboot.Checked, "kernel") }, noReboot},
		"a signal with a dash": {"reboot.checked", dropCharacters,
			func(r *wire.Report) { r.Reboot.Checked[1] = "reboot-required" }, noReboot},
		"a reason in capitals": {"reboot.reason", dropCharacters,
			func(r *wire.Report) { r.Reboot.Reason = "Kernel" }, func(r *wire.Report) { r.Reboot.Reason = "" }},
		"1025 packages to reboot for": {"reboot.packages", dropTooMany,
			func(r *wire.Report) { r.Reboot.Packages = append(r.Reboot.Packages, "x") },
			func(r *wire.Report) { r.Reboot.Packages = nil }},
		"a kernel too long": {"reboot.kernel_running", dropTooLong,
			func(r *wire.Report) { r.Reboot.Running += "x" }, func(r *wire.Report) { r.Reboot.Running = "" }},
		"a kernel with a line break": {"reboot.kernel_newest", dropCharacters,
			func(r *wire.Report) { r.Reboot.Newest = "6.1\n6.2" }, func(r *wire.Report) { r.Reboot.Newest = "" }},
	})
}

// Every bounded field of an inventory (ADR-0195): at its bound it is taken as sent. One step beyond, the locations
// beyond the bound are dropped; a location whose identity, code or list is out of bounds is dropped as a whole, a
// package whose name or version is out of bounds too; every other field alone is dropped.
func TestTheHubBoundsEveryFieldOfAnInventory(t *testing.T) {
	type c = boundCase[wire.Inventory]
	loc := func(inv *wire.Inventory) *inventory.Location { return &inv.Locations[0] }
	pkg := func(inv *wire.Inventory) *inventory.Package { return &inv.Locations[0].Packages[0] }
	without := func(i int) func(*wire.Inventory) {
		return func(inv *wire.Inventory) { inv.Locations = slices.Delete(inv.Locations, i, i+1) }
	}
	withoutPackage := func(i int) func(*wire.Inventory) {
		return func(inv *wire.Inventory) { loc(inv).Packages = slices.Delete(loc(inv).Packages, i, i+1) }
	}
	field := func(set func(*inventory.Package)) func(*wire.Inventory) {
		return func(inv *wire.Inventory) { set(pkg(inv)) }
	}
	noHolds := field(func(p *inventory.Package) { p.Holds = nil })
	noContainers := field(func(p *inventory.Package) { p.Containers = nil })
	noServices := field(func(p *inventory.Package) { p.Updatable = nil })
	noOS := field(func(p *inventory.Package) { p.OS = nil })
	cases := map[string]c{
		"1025 locations: the first 1024 are kept": {"locations", dropTooMany, func(inv *wire.Inventory) {
			inv.Locations = append(inv.Locations, inventory.Location{ID: "the-1025th", Kind: inventory.KindVenv,
				Ecosystem: versions.PyPI, Packages: []inventory.Package{}})
		}, func(*wire.Inventory) {}},
		"two thousand locations": {"locations", dropTooMany, func(inv *wire.Inventory) {
			inv.Locations = append(inv.Locations, manyOf(976, inv.Locations[1])...)
		}, func(*wire.Inventory) {}},
		"a location id with a dot and capitals": {"locations[].id", dropCharacters,
			func(inv *wire.Inventory) { inv.Locations[6].ID = "npm-global-First.Last" }, without(6)},
		"min_age_days 91": {"min_age_days", dropValue, func(inv *wire.Inventory) { inv.MinAgeDays = 91 },
			func(inv *wire.Inventory) { inv.MinAgeDays = 0 }},
		"negative min_age_days": {"min_age_days", dropValue, func(inv *wire.Inventory) { inv.MinAgeDays = -1 },
			func(inv *wire.Inventory) { inv.MinAgeDays = 0 }},
		"min_age_days_security 91": {"min_age_days_security", dropValue,
			func(inv *wire.Inventory) { inv.MinAgeDaysSecurity = 91 }, func(inv *wire.Inventory) { inv.MinAgeDaysSecurity = 0 }},
		"a location id too long": {"locations[].id", dropTooLong, func(inv *wire.Inventory) { loc(inv).ID += "0" }, without(0)},
		"a location id in capitals": {"locations[].id", dropCharacters,
			func(inv *wire.Inventory) { inv.Locations[7].ID = "Node-RED" }, without(7)},
		"a kind too long": {"locations[].kind", dropTooLong, func(inv *wire.Inventory) { loc(inv).Kind += "-" }, without(0)},
		"a kind with a space": {"locations[].kind", dropCharacters,
			func(inv *wire.Inventory) { inv.Locations[9].Kind = "npm global" }, without(9)},
		"an ecosystem with a slash": {"locations[].ecosystem", dropCharacters,
			func(inv *wire.Inventory) { inv.Locations[3].Ecosystem = "PyPI/x" }, without(3)},
		"no ecosystem": {"locations[].ecosystem", dropCharacters,
			func(inv *wire.Inventory) { inv.Locations[3].Ecosystem = "" }, without(3)},
		"a code with a dash": {"locations[].code", dropCharacters,
			func(inv *wire.Inventory) { inv.Locations[4].Code = "too-many" }, without(4)},
		"20001 packages": {"locations[].packages", dropTooMany, func(inv *wire.Inventory) {
			loc(inv).Packages = append(loc(inv).Packages, inventory.Package{Name: "x", Version: "1"})
		}, without(0)},
		"a path too long": {"locations[].path", dropTooLong, func(inv *wire.Inventory) { loc(inv).Path += "x" },
			func(inv *wire.Inventory) { loc(inv).Path = "" }},
		"a path with a tab": {"locations[].path", dropCharacters, func(inv *wire.Inventory) { inv.Locations[2].Path = "/a\tb" },
			func(inv *wire.Inventory) { inv.Locations[2].Path = "" }},
		"skipped a million and one": {"locations[].skipped", dropValue, func(inv *wire.Inventory) { loc(inv).Skipped++ },
			func(inv *wire.Inventory) { loc(inv).Skipped = 0 }},
		"negative outdated_failed": {"locations[].outdated_failed", dropValue,
			func(inv *wire.Inventory) { loc(inv).OutdatedFailed = -1 }, func(inv *wire.Inventory) { loc(inv).OutdatedFailed = 0 }},
		"not_in_registry 2^40": {"locations[].not_in_registry", dropValue,
			func(inv *wire.Inventory) { loc(inv).NotInRegistry = 1 << 40 }, func(inv *wire.Inventory) { loc(inv).NotInRegistry = 0 }},
		"a name too long": {"locations[].packages[].name", dropTooLong, field(func(p *inventory.Package) { p.Name += "x" }),
			withoutPackage(0)},
		"a name with an escape": {"locations[].packages[].name", dropCharacters,
			func(inv *wire.Inventory) { loc(inv).Packages[11].Name = "pkg\x1b[2J" }, withoutPackage(11)},
		"a version not UTF-8": {"locations[].packages[].version", dropCharacters,
			func(inv *wire.Inventory) { loc(inv).Packages[12].Version = "1.0\xff" }, withoutPackage(12)},
		"compare_failed in capitals": {"locations[].packages[].compare_failed", dropCharacters,
			field(func(p *inventory.Package) { p.CompareFailed = "No_answer" }),
			field(func(p *inventory.Package) { p.CompareFailed = "" })},
		"33 holds": {"locations[].packages[].holds", dropTooMany,
			field(func(p *inventory.Package) { p.Holds = append(p.Holds, inventory.Hold{By: "x"}) }), noHolds},
		"a range with an escape": {"locations[].packages[].holds", dropCharacters,
			field(func(p *inventory.Package) { p.Holds[3].Range = "^1\x1b" }), noHolds},
		"a holder too long": {"locations[].packages[].holds", dropTooLong,
			field(func(p *inventory.Package) { p.Holds[0].By += "x" }), noHolds},
		"1025 containers": {"locations[].packages[].containers", dropTooMany,
			field(func(p *inventory.Package) { p.Containers = append(p.Containers, "x") }), noContainers},
		"a container name too long": {"locations[].packages[].containers", dropTooLong,
			field(func(p *inventory.Package) { p.Containers[0] += "x" }), noContainers},
		"1025 services": {"locations[].packages[].updatable", dropTooMany,
			field(func(p *inventory.Package) { p.Updatable = append(p.Updatable, "a/b") }), noServices},
		"a service without its project": {"locations[].packages[].updatable", dropCharacters,
			field(func(p *inventory.Package) { p.Updatable[4] = "web" }), noServices},
		"a service pattern": {"locations[].packages[].updatable", dropCharacters,
			field(func(p *inventory.Package) { p.Updatable[5] = "stack/*" }), noServices},
		"a project too long": {"locations[].packages[].updatable", dropCharacters,
			field(func(p *inventory.Package) { p.Updatable[0] = "p" + p.Updatable[0] }), noServices},
		"5001 packages in an image": {"locations[].packages[].os", dropTooMany, field(func(p *inventory.Package) {
			p.OS.Packages = append(p.OS.Packages, inventory.OSPackage{Name: "x"})
		}), noOS},
		"an image's release too long": {"locations[].packages[].os", dropTooLong,
			field(func(p *inventory.Package) { p.OS.Version += "x" }), noOS},
		"an image's code with a dash": {"locations[].packages[].os", dropCharacters,
			field(func(p *inventory.Package) { p.OS.Code = "no-package-db" }), noOS},
		"a source package with a NUL": {"locations[].packages[].os", dropCharacters,
			field(func(p *inventory.Package) { p.OS.Packages[8].Source = "glibc\x00" }), noOS},
	}
	for _, name := range []string{"via", "latest", "candidate", "security_candidate", "digest", "rebuilt", "same_major"} {
		get := func(p *inventory.Package) *string {
			return map[string]*string{"via": &p.Via, "latest": &p.Latest, "candidate": &p.Candidate,
				"security_candidate": &p.SecurityCandidate, "digest": &p.Digest, "rebuilt": &p.Rebuilt,
				"same_major": &p.SameMajor}[name]
		}
		dropped := field(func(p *inventory.Package) { *get(p) = "" })
		cases[name+" too long"] = c{"locations[].packages[]." + name, dropTooLong,
			field(func(p *inventory.Package) { *get(p) += "x" }), dropped}
		cases[name+" with a line break"] = c{"locations[].packages[]." + name, dropCharacters,
			field(func(p *inventory.Package) { *get(p) = "1.0\n2.0" }), dropped}
	}
	checkBounds(t, inventoryAtTheBounds, boundInventory, cases)
}

// Every bounded field of a job status (ADR-0195): at its bound it is taken as sent. One step beyond, its job id or
// result code refuse it; every other field alone is dropped, a summary whose counts are out of range as a whole.
func TestTheHubBoundsEveryFieldOfAJobStatus(t *testing.T) {
	type c = boundCase[wire.JobStatus]
	refused := func(field, why string, change func(*wire.JobStatus)) c { return c{field, why, change, nil} }
	sum := func(set func(*wire.RunSummary)) func(*wire.JobStatus) {
		return func(st *wire.JobStatus) { set(st.Summary) }
	}
	noSummary := func(st *wire.JobStatus) { st.Summary = nil }
	checkBounds(t, jobStatusAtTheBounds, boundJobStatus, map[string]c{
		"a job id too long":   refused("job_id", dropTooLong, func(st *wire.JobStatus) { st.JobID += "a" }),
		"a job id a path":     refused("job_id", dropCharacters, func(st *wire.JobStatus) { st.JobID = "../job" }),
		"a code too long":     refused("code", dropTooLong, func(st *wire.JobStatus) { st.Code += "y" }),
		"a code in capitals":  refused("code", dropCharacters, func(st *wire.JobStatus) { st.Code = "OK" }),
		"a code with a space": refused("code", dropCharacters, func(st *wire.JobStatus) { st.Code = "ok " }),
		"a phase with a dash": {"phase", dropCharacters, func(st *wire.JobStatus) { st.Phase = "backing-up" },
			func(st *wire.JobStatus) { st.Phase = "" }},
		"a reason too long": {"reason", dropTooLong, func(st *wire.JobStatus) { st.Reason += "y" },
			func(st *wire.JobStatus) { st.Reason = "" }},
		"a detail too long: its end is kept": {"detail", dropTooLong, func(st *wire.JobStatus) { st.Detail += "x" },
			func(st *wire.JobStatus) { st.Detail = excerptOf(16 << 10)[1:] + "x" }},
		"a detail with a terminal title": {"detail", dropCharacters,
			func(st *wire.JobStatus) { st.Detail = "\x1b]0;title\x07" },
			func(st *wire.JobStatus) { st.Detail = "�]0;title�" }},
		"a detail in ANSI colours": {"detail", dropCharacters,
			func(st *wire.JobStatus) { st.Detail = "\x1b[32mSetting up\x1b[0m libssl3\r\n\tdone" },
			func(st *wire.JobStatus) { st.Detail = "�[32mSetting up�[0m libssl3\r\n\tdone" }},
		"progress 101": {"progress", dropValue, func(st *wire.JobStatus) { *st.Progress = 101 },
			func(st *wire.JobStatus) { st.Progress = nil }},
		"negative progress": {"progress", dropValue, func(st *wire.JobStatus) { *st.Progress = -1 },
			func(st *wire.JobStatus) { st.Progress = nil }},
		"an item with a line break": {"item", dropCharacters, func(st *wire.JobStatus) { st.Item = "a\nb" },
			func(st *wire.JobStatus) { st.Item = "" }},
		"a million and one packages": {"summary.package_count", dropValue,
			sum(func(s *wire.RunSummary) { s.PackageCount++ }), noSummary},
		"negative seconds": {"summary.seconds", dropValue, sum(func(s *wire.RunSummary) { s.Seconds = -1 }), noSummary},
		"101 packages in the summary": {"summary.packages", dropTooMany,
			sum(func(s *wire.RunSummary) { s.Packages = append(s.Packages, "x") }),
			sum(func(s *wire.RunSummary) { s.Packages = nil })},
		"a package with a NUL": {"summary.packages", dropCharacters,
			sum(func(s *wire.RunSummary) { s.Packages[1] = "x\x00" }), sum(func(s *wire.RunSummary) { s.Packages = nil })},
		"a log file too long": {"summary.log_file", dropTooLong, sum(func(s *wire.RunSummary) { s.LogFile += "x" }),
			sum(func(s *wire.RunSummary) { s.LogFile = "" })},
		"1025 outdated services": {"summary.outdated_services", dropTooMany,
			sum(func(s *wire.RunSummary) { s.OutdatedServices = append(s.OutdatedServices, "x") }),
			sum(func(s *wire.RunSummary) { s.OutdatedServices = nil })},
		"a replaced package with an escape": {"summary.replaced", dropCharacters,
			sum(func(s *wire.RunSummary) { s.Replaced[2] = "a\x1b -> b" }), sum(func(s *wire.RunSummary) { s.Replaced = nil })},
		"a restarted container too long": {"summary.restarted", dropTooLong,
			sum(func(s *wire.RunSummary) { s.Restarted[0] += "x" }), sum(func(s *wire.RunSummary) { s.Restarted = nil })},
	})
}

// What agents send - Debian, Windows, an appliance, a developer machine with tens of thousands of npm packages,
// Windows paths, long image references and digests, multi-line excerpts - is taken as sent (ADR-0195).
func TestTheHubTakesWhatAgentsReport(t *testing.T) {
	at := boundsAt
	aptExcerpt := strings.Repeat("W: Some index files failed to download.\n", 19) + "E: Sub-process returned an error code"
	report := func(sources ...source.Report) wire.Report {
		return wire.Report{T: wire.TReport, CheckedAt: at, Sources: sources}
	}
	ids := make([]string, 150)
	for i := range ids {
		ids[i] = fmt.Sprintf("Vendor%d.Product.Edition", i)
	}
	big := make([]source.Package, 3000) // a desktop after a release upgrade
	for i := range big {
		big[i] = source.Package{Name: fmt.Sprintf("lib%d-dev", i), From: "1:2.3.4~rc1-1+deb12u1", To: "1:2.3.5-1+deb13u1",
			Origins: []string{"Debian:13.2/stable"}}
	}
	for name, r := range map[string]wire.Report{
		"nothing to report": report(),
		"Debian": {T: wire.TReport, CheckedAt: at, Sources: []source.Report{{Source: "apt",
			State: protocol.SourceUpdatesAvailable, Pending: 3, Security: 1, KeptBack: 1, CheckedAt: at,
			Packages: []source.Package{{Name: "libssl3", From: "3.0.15-1~deb12u1", To: "3.0.17-1~deb12u2",
				Origins: []string{"Debian:12.11/stable-security", "Debian-Security:12/stable-security"}, Security: true},
				{Name: "linux-image-amd64", From: "6.1.140-1", To: "6.1.147-1", KeptBack: true}, {Name: "new-dep", To: "1.0-1"}},
			Removals: []string{"old-lib"},
			Facts: map[string]string{"apt_version": "2.6.1", "install_mode": "full-upgrade", "unattended_upgrades": "on",
				"lists_refresh": "done", "lists_updated_at": at.Format(time.RFC3339)}}},
			Reboot: &source.Reboot{Required: true, Reason: protocol.RebootKernel, Packages: []string{"linux-image-6.1.0-37-amd64"},
				Running: "6.1.0-35-amd64", Newest: "6.1.0-37-amd64", Checked: []string{"kernel", "reboot_required_file"}}},
		"a check that failed": report(source.Report{Source: "apt", State: protocol.SourceCheckFailed,
			Code: protocol.CodePrecheckRepoUnreachable, Detail: aptExcerpt, CheckedAt: at}),
		"a release upgrade": report(source.Report{Source: "apt", State: protocol.SourceUpdatesAvailable, Pending: 3000,
			Packages: big, CheckedAt: at}),
		"Windows": report(source.Report{Source: "winupdate", State: protocol.SourceCheckFailed,
			Code: protocol.CodeFailedWindowsUpdate, CheckedAt: at, Detail: "hresult 0x8024402C (COMException: \r\n\tat X)",
			Facts: map[string]string{"install_context": "service", "windows_update_search": "failed",
				"windows_update_hresult": "0x8024402C", "windows_update_agent": "1023.807.2072.0",
				"windows_update_feature_upgrade": "1", "windows_update_awaiting_reboot": "0"}},
			source.Report{Source: "winget", State: protocol.SourceUpdatesAvailable, Pending: 1, CheckedAt: at,
				Packages: []source.Package{{Name: "Contoso.ExampleTool.LTS", From: "1.2.3", To: "2.0.0", Major: true}},
				Facts: map[string]string{"winget_version": "v1.11.430", "winget_client": "1.11.430",
					"winget_unknown_version": strings.Join(ids, ", ")}}),
		"an appliance": report(source.Report{Source: "pfsense", State: protocol.SourceUpdatesAvailable, Pending: 1,
			CheckedAt: at, Packages: []source.Package{{Name: "pfSense", From: "2.7.2-RELEASE", To: "2.8.0-RELEASE"}},
			Facts: map[string]string{"updates_checked_at": "2026-11-18T02:00:00Z", "base_installed": "2.7.2-RELEASE",
				"self_update": "off", "model": "Netgate 4200"}},
			source.Report{Source: "config-backup", State: protocol.SourceUpToDate, CheckedAt: at,
				Facts: map[string]string{source.FactConfigBackupAt: at.Format(time.RFC3339),
					source.FactConfigBackupSH: strings.Repeat("ab", 32)}}),
		"a container": {T: wire.TReport, CheckedAt: at, Sources: []source.Report{{Source: "apt",
			State: protocol.SourceUpToDate, CheckedAt: at}}, Reboot: &source.Reboot{Checked: []string{}}},
	} {
		sent, _ := json.Marshal(r)
		if o := boundReport(&r); !o.whole() {
			t.Errorf("%s: refused %v, dropped %v", name, o.refused, o.dropped)
		} else if got, _ := json.Marshal(r); string(got) != string(sent) {
			t.Errorf("%s: changed", name)
		}
	}

	digest := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	scope := strings.Repeat("s", 100) // the longest npm names: a scope of 100, a name of 214
	dev := wire.Inventory{T: wire.TInventory, CheckedAt: at, OutdatedAt: &at, MinAgeDays: 7, MinAgeDaysSecurity: 2}
	for i := range 300 { // project folders under a pattern of agent.yaml
		dev.Locations = append(dev.Locations, inventory.Location{ID: fmt.Sprintf("projects-app-%d", i),
			Kind: inventory.KindNpmDir, Ecosystem: versions.Npm, Update: true, Deps: true,
			Path:     `C:\Users\developer.name\source\repos\` + strings.Repeat("deeply-nested-folder\\", 8) + "app",
			Packages: []inventory.Package{{Name: "left-pad", Version: "1.3.0", Direct: true}}})
	}
	huge := make([]inventory.Package, inventory.MaxPackages)
	for i := range huge {
		huge[i] = inventory.Package{Name: fmt.Sprintf("@%s/%s-%d", scope, strings.Repeat("n", 200), i),
			Version: "1.0.0-beta.1+build." + strings.Repeat("7", 40), Via: "@angular/core",
			Holds: []inventory.Hold{{By: "@angular/core@17.3.12", Range: ">=1.2.3 <2.0.0 || ^3.0.0-rc.1"},
				{By: "bundle@1.0.0", Bundled: true}}}
	}
	dev.Locations = append(dev.Locations, inventory.Location{ID: "monorepo", Kind: inventory.KindNpmDir,
		Ecosystem: versions.Npm, Path: "/home/developer/src/monorepo", Code: inventory.CodeTooManyPackages, Skipped: 12,
		Packages: huge}, inventory.Location{ID: "pipx-tool", Kind: inventory.KindVenv, Ecosystem: versions.PyPI,
		Path: "/home/developer/.local/share/pipx/venvs/tool", OutdatedFailed: 2, NotInRegistry: 1,
		Packages: []inventory.Package{{Name: "zope.interface", Version: "1!7.2.post1+local.7", Direct: true,
			Latest: "8.0", Candidate: "7.3", SecurityCandidate: "7.2.1", LatestAt: &at, Major: true}}})
	osPackages := make([]inventory.OSPackage, inventory.MaxOSPackages)
	for i := range osPackages {
		osPackages[i] = inventory.OSPackage{Name: fmt.Sprintf("libfoo%d", i), Version: "1:2.3.4~rc1-1+deb12u1",
			Source: "foo", SourceVersion: "1:2.3.4~rc1-1+deb12u1"}
	}
	images := inventory.Location{ID: "containers", Kind: inventory.KindContainer, Ecosystem: versions.OCI,
		Path: "/var/run/docker.sock", ComparedAt: &at, Packages: []inventory.Package{
			{Name: "registry.example:5000/team/some-service-with-a-long-name", Version: "v1.2.3-alpine3.20", Digest: digest,
				Containers: []string{"stack-web-1", "stack-web-2"}, Updatable: []string{"stack/web"}, Rebuilt: digest,
				RebuiltAt: &at, RebuiltCandidate: true, Latest: "v1.3.0-alpine3.20", SameMajor: "v1.2.9-alpine3.20",
				OS: &inventory.ImageOS{ID: "debian", Version: "12", Code: inventory.CodeTooManyPackages, Packages: osPackages}},
			{Name: "ghcr.io/org/app", Version: "", Digest: "sha512:" + strings.Repeat("0123456789abcdef", 8),
				CompareFailed: inventory.CompareRateLimited},
			{Name: "localhost/app", Version: "dev", OS: &inventory.ImageOS{Code: inventory.CodeNoPackageDB}}}}
	for name, inv := range map[string]wire.Inventory{
		"none":                   {T: wire.TInventory, CheckedAt: at, Locations: []inventory.Location{}},
		"a developer machine":    dev,
		"images":                 {T: wire.TInventory, CheckedAt: at, Locations: []inventory.Location{images}},
		"an unreadable location": {T: wire.TInventory, CheckedAt: at, Locations: []inventory.Location{{ID: "node-red", Kind: inventory.KindNpmDir, Ecosystem: versions.Npm, Path: "/srv/node-red", Code: inventory.CodeUnreadable, Packages: []inventory.Package{}}}},
		// Ids longer than a scope takes: the hub keeps them up to 64 (ADR-0195), offering no button for them.
		"a long id": {T: wire.TInventory, CheckedAt: at, Locations: []inventory.Location{{
			ID: "npm-global-firstname-lastname-example-domain-001", Kind: inventory.KindNpmGlobal, Ecosystem: versions.Npm,
			Path: `C:\Users\firstname.lastname.EXAMPLE-DOMAIN.001\AppData\Roaming\npm\node_modules`, Update: true,
			Packages: []inventory.Package{{Name: "npm", Version: "10.9.2", Direct: true}}}, {
			ID: "a" + strings.Repeat("-", 62) + "z", Kind: inventory.KindVenv, Ecosystem: versions.PyPI, Path: "/opt/x",
			Packages: []inventory.Package{}}}},
	} {
		sent, _ := json.Marshal(inv)
		if o := boundInventory(&inv); !o.whole() {
			t.Errorf("%s: refused %v, dropped %v", name, o.refused, o.dropped)
		} else if got, _ := json.Marshal(inv); string(got) != string(sent) {
			t.Errorf("%s: changed", name)
		}
	}

	pct := 42
	summary := &wire.RunSummary{PackageCount: 2500, Seconds: 1800, LogFile: `C:\ProgramData\Vordr\runs\2026-11-18T030000Z.log`,
		Packages: manyOf(wire.MaxSummaryPackages, "libreoffice-core-nogui"), OutdatedServices: manyOf(40, "systemd-logind.service"),
		Replaced: []string{"libssl3 -> libssl3t64"}, Restarted: []string{"stack-web-1"}}
	for name, st := range map[string]wire.JobStatus{
		"a phase": {T: wire.TJobStatus, JobID: "0123456789abcdef01234567", Phase: protocol.PhaseDownloading, Progress: &pct,
			Item: "libreoffice-core-nogui"},
		"a result": {T: wire.TJobStatus, JobID: "0123456789abcdef01234567", Code: protocol.CodeOKRebootRequired,
			Detail: aptExcerpt, Summary: summary, InstallStarted: &at},
		"a refused job without its id": {T: wire.TJobStatus, Code: protocol.CodeRejectedInvalidJob,
			Reason: protocol.InvalidBadSignature},
		"winget's failure": {T: wire.TJobStatus, JobID: "fedcba9876543210fedcba98", Code: protocol.CodeFailedPackageManager,
			Detail: "InstallerErrorCode: 0x8A150011\r\n\tInstaller hash does not match.\r\n"},
		"a run of the hub's own": {T: wire.TJobStatus, JobID: "j-1", Code: protocol.CodeOK, Detail: "rebooted"},
	} {
		sent, _ := json.Marshal(st)
		if o := boundJobStatus(&st); !o.whole() {
			t.Errorf("%s: refused %v, dropped %v", name, o.refused, o.dropped)
		} else if got, _ := json.Marshal(st); string(got) != string(sent) {
			t.Errorf("%s: changed", name)
		}
	}
}

// nestingOf is how deeply the JSON of a value of type typ nests. The type must hold no interface, no raw JSON and no
// type within itself: each would let a message nest without bound.
func nestingOf(t *testing.T, typ reflect.Type, seen []reflect.Type) int {
	switch {
	case typ == reflect.TypeFor[json.RawMessage]() || typ.Kind() == reflect.Interface:
		t.Errorf("%v holds %v: nesting without bound", seen, typ)
		return 0
	case typ.Kind() == reflect.Pointer:
		return nestingOf(t, typ.Elem(), seen)
	case typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8, typ == reflect.TypeFor[time.Time]():
		return 0 // text in JSON
	case typ.Kind() == reflect.Slice, typ.Kind() == reflect.Map:
		return 1 + nestingOf(t, typ.Elem(), seen)
	case typ.Kind() == reflect.Struct:
		if slices.Contains(seen, typ) {
			t.Errorf("%v holds itself: nesting without bound", typ)
			return 0
		}
		deepest := 0
		for i := range typ.NumField() {
			deepest = max(deepest, nestingOf(t, typ.Field(i).Type, append(seen, typ)))
		}
		return 1 + deepest
	}
	return 0
}

// Every field of a report, an inventory, a job status and a Home Assistant client's hello is bounded - also one added
// later, which this test finds by itself: far beyond any bound, a field the hub does not bound by its type (a switch,
// a time) or by the dispatch (the type t) is not taken as sent. A new field fails here until it has its bound. Their
// nesting is bounded by the types: no message nests deeper than an inventory's 8 levels.
func TestEveryFieldOfTheMessagesIsBounded(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nonce := make([]byte, wire.NonceSize)
	haHelloAt := func() wire.HAHello {
		return wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: strings.Repeat("n", 63), PublicKey: pub,
			Signature:          ed25519.Sign(priv, wire.HAHelloSigned(nonce, strings.Repeat("n", 63), pub)),
			IntegrationVersion: "1." + strings.Repeat("0", 62), Protocol: wire.HAProtocol}
	}
	for name, m := range map[string]struct {
		typ    reflect.Type
		byType []string                 // bounded by the dispatch (t) or by their type (a switch, a time)
		whole  func(target string) bool // the message at the bounds with target oversized is taken whole
	}{
		"report": {reflect.TypeFor[wire.Report](), []string{"t", "checked_at", "sources[].checked_at",
			"sources[].packages[].security", "sources[].packages[].major", "sources[].packages[].kept_back",
			"reboot.required"}, func(target string) bool {
			r := reportAtTheBounds()
			oversize(reflect.ValueOf(&r).Elem(), "", target)
			return boundReport(&r).whole()
		}},
		"inventory": {reflect.TypeFor[wire.Inventory](), []string{"t", "checked_at", "outdated_at", "locations[].update",
			"locations[].deps", "locations[].compared_at", "locations[].packages[].direct", "locations[].packages[].latest_at",
			"locations[].packages[].major", "locations[].packages[].rebuilt_at", "locations[].packages[].rebuilt_candidate",
			"locations[].packages[].holds[].bundled"}, func(target string) bool {
			inv := inventoryAtTheBounds()
			oversize(reflect.ValueOf(&inv).Elem(), "", target)
			return boundInventory(&inv).whole()
		}},
		"job status": {reflect.TypeFor[wire.JobStatus](), []string{"t", "install_started"}, func(target string) bool {
			st := jobStatusAtTheBounds()
			oversize(reflect.ValueOf(&st).Elem(), "", target)
			return boundJobStatus(&st).whole()
		}},
		// Its identity holds or the hello is refused with bad_hello, its protocol is the hub's or it is refused with
		// protocol_mismatch, and every other field is boundHAHello's.
		"Home Assistant hello": {reflect.TypeFor[wire.HAHello](), []string{"t"}, func(target string) bool {
			h := haHelloAt()
			oversize(reflect.ValueOf(&h).Elem(), "", target)
			return haHelloIdentityHolds(h, nonce) && h.Protocol == wire.HAProtocol && len(boundHAHello(&h)) == 0
		}},
	} {
		kinds := map[string]reflect.Kind{}
		helloPaths(m.typ, "", kinds)
		for _, p := range m.byType {
			if k, ok := kinds[p]; !ok || k != reflect.Bool && k != reflect.Struct && p != "t" {
				t.Errorf("%s: %s is no switch or time of the message (%v)", name, p, k)
			}
		}
		if !m.whole("") {
			t.Fatalf("%s: the message at the bounds is not taken whole", name)
		}
		checked := 0
		for p := range kinds {
			if slices.Contains(m.byType, p) {
				continue
			}
			if m.whole(p) {
				t.Errorf("%s: %s far beyond any bound, and taken", name, p)
			}
			checked++
		}
		if depth := nestingOf(t, m.typ, nil); depth > 8 {
			t.Errorf("%s nests %d levels", name, depth)
		}
		t.Logf("%s: %d fields checked", name, checked)
	}
	if depth := nestingOf(t, reflect.TypeFor[wire.Inventory](), nil); depth != 8 {
		t.Errorf("an inventory nests %d levels, the bound says 8", depth)
	}
}
