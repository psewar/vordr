package source

import "testing"

func TestMajorJump(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"20.11.1", "22.2.0", true},      // a runtime's LTS line
		{"2.7.1000", "3.0.2000", true},   // a vendor's backup agent
		{"35.0.0", "35.0.1", false},      // a desktop client
		{"1.7.0", "1.7.1", false},        // a command-line tool
		{"0.40.8", "0.40.13", false},     // a tool with long patch series
		{"1:2.3-4", "1:3.0-1", true},     // epoch skipped
		{"2:1.0", "3:1.5", false},        // only the epoch changed
		{"2025.10.1", "2026.1.0", false}, // calendar version
		{"", "1.0", false},               // new dependency
		{"Unknown", "2.0", false},        // winget's unknown installed version
		{"v1.2", "v2.0", false},          // not a number first: no statement
	} {
		if got := MajorJump(c.from, c.to); got != c.want {
			t.Errorf("MajorJump(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
