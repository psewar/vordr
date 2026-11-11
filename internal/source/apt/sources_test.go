package apt

import "testing"

// What a security run may change (ADR-0116): a candidate from a security archive or from the host's own release. Not
// the release's pockets, not another release, not a vendor's repository - also when the vendor calls its suite
// "stable". The origins are in the formats apt prints (RelStr): "<label>:<version>/<archive>", or "<label>:<archive>"
// for an archive without Version.
func TestSecurityRunAllows(t *testing.T) {
	for _, c := range []struct {
		line, release string
		want          bool
	}{
		{"Inst a [1] (2 Debian-Security:12/oldstable-security [amd64])", "12", true},
		{"Inst a [1] (2 Debian-Security:13/stable-security [arm64])", "13", true},
		{"Inst a [1] (2 Ubuntu:24.04/noble-security [amd64])", "24.04", true},
		{"Inst a [1] (2 UbuntuESM:24.04/noble-infra-security [amd64])", "24.04", true},
		{"Inst a [1] (2 Debian-Security:12/oldstable-security [amd64])", "", true}, // security needs no release
		{"Inst a [1] (2 Debian:12.15/oldstable, Debian-Security:12/oldstable-security [amd64]) []", "12", true},
		{"Inst a (2 Debian:12.15/oldstable [amd64])", "12", true}, // the release with its point releases
		{"Inst a (2 Debian:13.7/stable [arm64])", "13", true},
		{"Inst a (2 Ubuntu:24.04/noble [amd64])", "24.04", true},
		{"Inst a (2 Debian:12.15/oldstable, Debian:12-updates/oldstable-updates [amd64])", "12", true}, // the release has it
		{"Inst a (2 Debian:13.7/stable [amd64])", "12", false},                                         // the next release, configured on a 12 host
		{"Inst a (2 Debian:12.15/oldstable [amd64])", "1", false},                                      // 12.15 is no point release of 1
		{"Inst a (2 Debian:12.15/oldstable [amd64])", "", false},                                       // release unknown: only security
		{"Inst a [1] (2 Debian:12-updates/oldstable-updates [amd64])", "12", false},
		{"Inst a [1] (2 Debian:13-updates/stable-updates [arm64])", "13", false},
		{"Inst a [1] (2 Ubuntu:24.04/noble-updates [amd64])", "24.04", false},
		{"Inst a [1] (2 Ubuntu:24.04/noble-proposed [amd64])", "24.04", false},
		{"Inst a [1] (2 Debian:12.15/proposed-updates [amd64])", "12", false},
		{"Inst a [1] (2 Debian Backports:bookworm-backports [amd64])", "12", false},
		{"Inst a [1] (2 Proxmox Debian Repository:stable [amd64])", "12", false}, // no Version
		{"Inst a [1] (2 Raspberry Pi Foundation:stable [arm64])", "13", false},   // no Version
		{"Inst a [1] (2 Docker CE:bookworm [amd64])", "12", false},               // no Version
		{"Inst a [1] (2 Raspbian:oldstable [armhf])", "12", false},               // no Version: cannot be matched
		{"Inst a [1] (2 Example Vendor:12/stable [amd64])", "12", false},         // a vendor with the host's version
		{"Inst a [1] (2 Debian:testing [amd64])", "", false},                     // no Version, release unknown
		{"Inst a [1] (2 Debian:testing [amd64])", "13", false},                   // no Version
		{"Inst a [1] (2 Ubuntu:noble-security [amd64])", "24.04", true},          // a mirror without Version
		{"Inst a [1] (2 Debian:localhost [amd64])", "12", false},                 // no archive: the site
		{"Inst a", "12", false}, // no candidate, no origin
	} {
		pkgs, _ := ParseSimulation([]byte(c.line + "\n"))
		if len(pkgs) != 1 {
			t.Fatalf("%q: %+v", c.line, pkgs)
		}
		if got := SecurityRunAllows(pkgs[0], c.release); got != c.want {
			t.Errorf("%q on release %q: %v, want %v (origins %q)", c.line, c.release, got, c.want, pkgs[0].Origins)
		}
	}
}
