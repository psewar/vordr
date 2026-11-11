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
	rel := parseRelations([]byte("Package: a\nConflicts: x,\n y\nReplaces: x\n\nPackage: b:arm64\nBreaks: z\n"))
	if !slices.Equal(rel["a"]["Conflicts"], []string{"x", "y"}) || !slices.Equal(rel["b"]["Breaks"], []string{"z"}) {
		t.Fatalf("stanzas: %v", rel)
	}
}
