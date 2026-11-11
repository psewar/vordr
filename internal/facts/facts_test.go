package facts

import "testing"

func TestParseOSRelease(t *testing.T) {
	rel := parseOSRelease([]byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\n# comment\n"))
	if rel["PRETTY_NAME"] != "Debian GNU/Linux 13 (trixie)" || rel["ID"] != "debian" || rel["VERSION_ID"] != "13" {
		t.Fatalf("%v", rel)
	}
}

func TestPlatformNeedsOSAndArch(t *testing.T) {
	for _, c := range []struct {
		h    Host
		want bool
	}{
		{Host{GOOS: "linux", Arch: "arm64"}, true},
		{Host{Arch: "amd64"}, false},                  // an agent that does not report goos
		{Host{GOOS: Unknown, Arch: "Model 7"}, false}, // a device behind a target: arch is its model
		{Host{GOOS: "windows", Arch: Unknown}, false},
	} {
		if _, _, ok := c.h.Platform(); ok != c.want {
			t.Errorf("%+v: %v, want %v", c.h, ok, c.want)
		}
	}
}

func TestInContainer(t *testing.T) {
	for c, want := range map[string]bool{"none": false, Unknown: false, "lxc": true, "wsl": true} {
		if got := (Host{Container: c}).InContainer(); got != want {
			t.Errorf("%q: %v, want %v", c, got, want)
		}
	}
}
