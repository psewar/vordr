//go:build windows

package winupdate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// refuseLines stands in for the line runner, which only the install script uses: the live test must never get there.
type refuseLines struct{ t *testing.T }

func (r refuseLines) Lines(context.Context, func(string), string, ...string) runner.Result {
	r.t.Fatal("the live test ran the install script")
	return runner.Result{}
}

// TestWinUpdateLive asks the real Windows Update Agent of this host what it offers, with the search script only: it
// downloads and installs nothing. It talks to Windows Update and takes a while, so it only runs with
// VORDR_WINUPDATE_LIVE=1.
func TestWinUpdateLive(t *testing.T) {
	if os.Getenv("VORDR_WINUPDATE_LIVE") != "1" {
		t.Skip("VORDR_WINUPDATE_LIVE=1 not set: this test asks the real Windows Update")
	}
	u := New()
	u.Lines = refuseLines{t}
	if u.Detect() != source.Present {
		t.Fatalf("no PowerShell at %q", u.PowerShell)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	start := time.Now()
	rep := u.Check(ctx)
	t.Logf("search with %s took %s: %s, %d pending, %d security, facts %v", u.PowerShell,
		time.Since(start).Round(time.Second), rep.State, rep.Pending, rep.Security, rep.Facts)
	for _, p := range rep.Packages {
		t.Logf("  %s security=%v %s", p.Name, p.Security, p.To)
	}
	if rep.State == protocol.SourceCheckFailed || rep.Facts[FactSearch] != SearchOK {
		t.Fatalf("search failed: %s %q", rep.Code, rep.Detail)
	}
}
