package apt

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// A vendor's archive renames a package in an upgrade: filemgr-ng replaces filemgr.
const (
	renameSim = twoPending + "Inst desktop-common [1.0] (1.1 Example Vendor:stable [arm64]) []\n" +
		"Remv filemgr [1.4.0-1] []\n" +
		"Inst filemgr-ng (2.0 Example Vendor:stable [arm64])\n"
	renameShow = "Package: libc6\nVersion: 2.36-9+deb12u10\nBreaks: hurd (<< 1:0.9.git20220301-2), libtirpc1 (<< 0.2.3)\n\n" +
		"Package: tzdata\nVersion: 2025b-0+deb12u1\n\n" +
		"Package: desktop-common\nVersion: 1.1\nDepends: session-example, filemgr-ng\n\n" +
		"Package: filemgr-ng\nVersion: 2.0\nDepends: libc6 (>= 2.34),\n libgtk-3-0t64 (>= 3.21.5)\nBreaks: filemgr\nReplaces: filemgr\n\n"
	renameShowCmd = "apt-cache show --no-all-versions libc6 tzdata desktop-common filemgr-ng"
)

// A package the run replaces (Replaces plus Breaks) is no loss: the run goes ahead and says what it replaced.
func TestInstallReplacedPackage(t *testing.T) {
	f := installFake()
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(renameSim), ok("")}
	f.answers[renameShowCmd] = []runner.Result{ok(renameShow)}
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || !slices.Equal(res.Replaced, []string{"filemgr -> filemgr-ng"}) || rec.journals != 1 {
		t.Fatalf("result %+v, calls %v", res, f.calls)
	}
}

// Still refused: a removal nobody in the run replaces, Replaces without Breaks/Conflicts, or apt-cache cannot say.
func TestInstallRealRemovalsStillRefused(t *testing.T) {
	cases := map[string]struct {
		sim, show string
		want      string
	}{
		"replaced plus a real removal": {renameSim + "Remv oldpkg [1.0]\n", renameShow,
			"the upgrade would remove: oldpkg (replaced by the run: filemgr -> filemgr-ng)"},
		"replaces without breaks": {renameSim, strings.Replace(renameShow, "Breaks: filemgr\n", "", 1),
			"the upgrade would remove: filemgr"},
		"apt-cache cannot say": {renameSim, "", "the upgrade would remove: filemgr"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := installFake()
			f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(tc.sim)}
			if tc.show != "" {
				f.answers[renameShowCmd] = []runner.Result{ok(tc.show)}
			}
			var rec recorder
			res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
			if res.Code != protocol.CodePrecheckWouldRemove || res.Detail != tc.want || rec.journals != 0 {
				t.Fatalf("result %+v", res)
			}
		})
	}
}

func TestRelationNames(t *testing.T) {
	got := relationNames(" filemgr (<< 1.5), foo:any | bar,baz(>=1)")
	if !slices.Equal(got, []string{"filemgr", "foo", "bar", "baz"}) {
		t.Fatalf("names: %v", got)
	}
	rel := parseRelations([]byte("Package: a\nConflicts: x,\n y\nReplaces: x\n\nPackage: b:arm64\nBreaks: z\n" +
		"Source: bsrc (1.2-1)\nDepends: libz1 (>= 1), c | d\nPre-Depends: e\n"))
	if !slices.Equal(rel["a"]["Conflicts"], []string{"x", "y"}) || !slices.Equal(rel["b"]["Breaks"], []string{"z"}) {
		t.Fatalf("stanzas: %v", rel)
	}
	if sourceOf("b", rel["b"]) != "bsrc" || sourceOf("a", rel["a"]) != "a" ||
		!slices.Equal(rel["b"]["Depends"], []string{"libz1", "c", "d"}) || !slices.Equal(rel["b"]["Pre-Depends"], []string{"e"}) {
		t.Fatalf("source and depends: %v", rel["b"])
	}
}

// A library moves to a new soname (ADR-0146): libexample1 -> libexample2, both built from the source "example". The
// plugins package of the same source is tied to its library version, so apt removes libexample1 once the plugins move
// on; every package that used libexample1 moves to libexample2 in the same run, and nobody declares Replaces on it.
const (
	sonameSim = twoPending +
		"Inst libexample2 (2.0-1 Example Vendor:stable [arm64])\n" +
		"Inst example-tools [1.4-1] (1.5-1 Example Vendor:stable [arm64]) []\n" +
		"Remv libexample1 [1.9-1] [example-plugins:arm64 ]\n" +
		"Inst example-plugins [1.9-1] (2.0-1 Example Vendor:stable [arm64])\n"
	sonameShow = "Package: libc6\nVersion: 2.36-9+deb12u10\n\nPackage: tzdata\nVersion: 2025b-0+deb12u1\n\n" +
		"Package: libexample2\nVersion: 2.0-1\nSource: example\nDepends: libc6 (>= 2.36)\n\n" +
		"Package: example-tools\nVersion: 1.5-1\nDepends: libexample2 (>= 2.0), libc6\n\n" +
		"Package: example-plugins\nVersion: 2.0-1\nSource: example\nDepends: libexample2 (>= 2.0)\nBreaks: libexample0\n" +
		"Replaces: libexample0\n\n"
	sonameShowCmd = "apt-cache show --no-all-versions libc6 tzdata libexample2 example-tools example-plugins"
	sonameAutoCmd = "apt-mark showauto libexample1"
	sonameSrcCmd  = "dpkg-query -W -f ${Package} ${source:Package}\\n libexample1"
	sonameRdepCmd = "apt-cache rdepends --installed --no-suggests --no-recommends --no-enhances --no-conflicts " +
		"--no-breaks --no-replaces libexample1"
	sonameRdep = "libexample1\nReverse Depends:\n  example-plugins\n |example-tools\n  example-plugins\n"
)

func sonameFake(overrides map[string]*runner.Result) *seqRunner {
	f := installFake()
	f.answers["apt-get -s -q full-upgrade"] = []runner.Result{ok(sonameSim), ok("")}
	base := map[string]runner.Result{
		sonameShowCmd: ok(sonameShow), sonameAutoCmd: ok("libexample1\n"), sonameSrcCmd: ok("libexample1 example\n"),
		sonameRdepCmd: ok(sonameRdep),
	}
	for cmd, r := range base {
		if o, set := overrides[cmd]; set {
			if o == nil {
				continue // the tool gives no answer
			}
			r = *o
		}
		f.answers[cmd] = []runner.Result{r}
	}
	return f
}

// The old library the run leaves behind is no loss: the run goes ahead and names it with its successor.
func TestInstallSupersededLibrary(t *testing.T) {
	f := sonameFake(nil)
	var rec recorder
	res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
	if res.Code != protocol.CodeOK || !slices.Equal(res.Replaced, []string{"libexample1 -> libexample2"}) || rec.journals != 1 {
		t.Fatalf("result %+v, calls %v", res, f.calls)
	}
}

// Still refused when one of the three conditions is missing, or a tool cannot say.
func TestInstallSupersededLibraryOnlyWhenAllThreeHold(t *testing.T) {
	r := func(x runner.Result) *runner.Result { return &x }
	stillUses := strings.Replace(sonameShow, "Depends: libexample2 (>= 2.0), libc6", "Depends: libexample1, libc6", 1)
	cases := map[string]map[string]*runner.Result{
		"a person installed it":         {sonameAutoCmd: r(ok(""))},
		"no successor from its source":  {sonameSrcCmd: r(ok("libexample1 other-source\n"))},
		"a package outside the run":     {sonameRdepCmd: r(ok(sonameRdep + "  example-viewer\n"))},
		"the new version still uses it": {sonameShowCmd: r(ok(stillUses))},
		"apt-mark cannot say":           {sonameAutoCmd: nil},
		"dpkg-query fails":              {sonameSrcCmd: r(exit(1, "dpkg-query: no packages found matching libexample1"))},
		"rdepends cannot say":           {sonameRdepCmd: nil},
	}
	for name, overrides := range cases {
		t.Run(name, func(t *testing.T) {
			f := sonameFake(overrides)
			var rec recorder
			res := newInstaller(t, f).Install(context.Background(), rec.req(protocol.ScopeAll), time.Hour, rec.progress)
			if res.Code != protocol.CodePrecheckWouldRemove || res.Detail != "the upgrade would remove: libexample1" ||
				rec.journals != 0 {
				t.Fatalf("result %+v", res)
			}
		})
	}
}

func TestReverseDepends(t *testing.T) {
	got := reverseDepends([]byte("libx1\nReverse Depends:\n  a\n |b:arm64\n  a\n\n"))
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("reverse depends: %v", got)
	}
	if got := reverseDepends([]byte("libx1\n")); len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
}
