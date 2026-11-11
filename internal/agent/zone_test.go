package agent

import (
	"testing"
	"time"
	_ "time/tzdata" // the hub loads the zone with the embedded data; the test checks against the same
)

func TestIanaZone(t *testing.T) {
	for _, c := range []struct {
		id   string
		off  bool
		want string
	}{
		{"Eastern Standard Time", false, "America/New_York"},
		{"UTC", false, "Etc/UTC"},
		{"Pacific Standard Time", false, "America/Los_Angeles"},
		{"Eastern Standard Time", true, ""}, // daylight saving switched off: the clock no longer follows the zone
		{"No Such Standard Time", false, ""},
		{"", false, ""},
	} {
		if got := ianaZone(c.id, c.off); got != c.want {
			t.Errorf("ianaZone(%q, %v) = %q, want %q", c.id, c.off, got, c.want)
		}
	}
}

// Every name in the table must load where the hub plans with it, or the hub would refuse to plan the window.
func TestWindowsZonesLoad(t *testing.T) {
	if len(windowsZones) < 100 {
		t.Fatalf("table has %d zones, expected the whole CLDR list", len(windowsZones))
	}
	for id, name := range windowsZones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("%s -> %s: %v", id, name, err)
		}
	}
}
