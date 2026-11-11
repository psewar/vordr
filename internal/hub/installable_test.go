package hub

import (
	"testing"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// ADR-0106: installable is what the install button would take under the host's policy.
func TestInstallableFollowsTheInstallButton(t *testing.T) {
	install := []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}
	cases := []struct {
		name  string
		verbs []protocol.Verb
		pol   wire.PolicySummary
		want  int
	}{
		{"default scope all", install, wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all"}, 5},
		{"default scope security", install, wire.PolicySummary{AllowedScopes: []string{"security"}, DefaultScope: "security"}, 2},
		{"only the security button", install, wire.PolicySummary{AllowedScopes: []string{"security"}, DefaultScope: "all"}, 2},
		{"no install button", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, 0},
		{"install not allowed", install, wire.PolicySummary{DefaultScope: "all"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newViewFixture(t)
			f.host(t, "h", c.verbs, c.pol, true)
			_ = f.srv.Reg.SetReport("h", aptReport(f.now, 5, 2, nil))
			v := f.view(t, "h", f.now)
			if v.Installable == nil || *v.Installable != c.want {
				t.Fatalf("installable %v, want %d", v.Installable, c.want)
			}
		})
	}
	// Unknown exactly when pending is: a failed check.
	f := newViewFixture(t)
	f.host(t, "h", install, cases[0].pol, true)
	failed := aptReport(f.now, 0, 0, nil)
	failed.Sources[0].State, failed.Sources[0].Code = protocol.SourceCheckFailed, protocol.CodePrecheckRepoUnreachable
	_ = f.srv.Reg.SetReport("h", failed)
	if v := f.view(t, "h", f.now); v.Installable != nil || v.Pending != nil {
		t.Fatalf("failed check: installable %v pending %v", v.Installable, v.Pending)
	}
}
