//go:build windows

package agent

import (
	"testing"
	"time"
)

// The zone the agent announces has the same offset as the Windows clock, now and half a year from now (the other
// side of daylight saving).
func TestHostZoneMatchesTheClock(t *testing.T) {
	name := hostZone()
	if name == "" {
		t.Skip("this host reports no time zone (unknown ID or daylight saving switched off)")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("hostZone %q does not load: %v", name, err)
	}
	for _, at := range []time.Time{time.Now(), time.Now().AddDate(0, 6, 0)} {
		_, want := at.In(time.Local).Zone()
		_, got := at.In(loc).Zone()
		if got != want {
			t.Errorf("%s: %s has offset %d s, the Windows clock %d s", at.Format(time.DateOnly), name, got, want)
		}
	}
}
