package versions

import "testing"

// pair: a compared with b gives c (-1, 0, 1).
type pair struct {
	a string
	c int
	b string
}

// checkPairs checks every pair in both directions (antisymmetry).
func checkPairs(t *testing.T, eco Ecosystem, pairs []pair) {
	t.Helper()
	for _, p := range pairs {
		if c, ok := Compare(eco, p.a, p.b); !ok || c != p.c {
			t.Errorf("%s: %q vs %q: got %d (ok %v), want %d", eco, p.a, p.b, c, ok, p.c)
		}
		if c, ok := Compare(eco, p.b, p.a); !ok || c != -p.c {
			t.Errorf("%s: %q vs %q: got %d (ok %v), want %d", eco, p.b, p.a, c, ok, -p.c)
		}
	}
}

func TestDpkgReference(t *testing.T) {
	// dpkg's own cases (scripts/t/Dpkg_Version.t), except "-0.6.5", which dpkg itself refuses.
	checkPairs(t, Debian, []pair{
		{"1.0-1", -1, "2.0-2"}, {"2.2~rc-4", -1, "2.2-1"}, {"1.0000-1", 0, "1.0-1"}, {"1", 0, "0:1"},
		{"0", 0, "0:0-0"}, {"2:2.5", 1, "1:7.5"}, {"1:0foo", 1, "0foo"}, {"0:0foo", 0, "0foo"}, {"0foo", 0, "0foo"},
		{"0foo-0", 0, "0foo"}, {"0foo", 1, "0fo"}, {"0foo-0", -1, "0foo+"}, {"0foo~1", -1, "0foo"},
		{"0foo~foo+Bar", -1, "0foo~foo+bar"}, {"0foo~~", -1, "0foo~"}, {"1~", -1, "1"},
		{"12345+that-really-is-some-ver-0", -1, "12345+that-really-is-some-ver-10"}, {"0foo-0", -1, "0foo-01"},
		{"0foo.bar", 1, "0foobar"}, {"0foo.bar", 1, "0foo1bar"}, {"0foo.bar", 1, "0foo0bar"},
		{"0foo1bar-1", -1, "0foobar-1"}, {"0foo2.0", 1, "0foo2"}, {"0foo2.0.0", -1, "0foo2.10.0"},
		{"0foo2.0", -1, "0foo2.0.0"}, {"0foo2.0", -1, "0foo2.10"}, {"0foo2.1", -1, "0foo2.10"}, {"1.09", 0, "1.9"},
		{"1.0.8+nmu1", 1, "1.0.8"}, {"3.11", 1, "3.10+nmu1"}, {"0.9j-20080306-4", 1, "0.9i-20070324-2"},
		{"1.2.0~b7-1", 1, "1.2.0~b6-1"}, {"1.011-1", 1, "1.06-2"}, {"0.0.9+dfsg1-1", 1, "0.0.8+dfsg1-3"},
		{"4.6.99+svn6582-1", 1, "4.6.99+svn6496-1"}, {"53", 1, "52"}, {"0.9.9~pre122-1", 1, "0.9.9~pre111-1"},
		{"2:2.3.2-2+lenny2", 1, "2:2.3.2-2"}, {"1:3.8.1-1", 1, "3.8.GA-1"}, {"1.0.1+gpl-1", 1, "1.0.1-2"},
		{"1a", -1, "1000a"},
	})
	// lib/dpkg/t/t-version.c, written as strings.
	checkPairs(t, Debian, []pair{{"0-0", 0, "0-0"}, {"0-00", 0, "00-0"}, {"1:2-3", 0, "1:2-3"},
		{"0-0", -1, "1:0-0"}, {"0-a", -1, "0-b"}, {"1-1", -1, "2-1"}, {"1-1", -1, "1-2"}})
}

func TestDpkgOrder(t *testing.T) {
	// Debian Policy 5.6.12: "~~" < "~~a" < "~" < the empty part < "a"; letters before all other characters, upper
	// case before lower case (ASCII), "+" before ".".
	strictlyIncreasing(t, Debian, []string{"1.0~~", "1.0~~a", "1.0~", "1.0~a", "1.0", "1.0A", "1.0Z", "1.0a",
		"1.0a+", "1.0z", "1.0+", "1.0+b1", "1.0.", "1.0.1"})
	// Security updates as Debian numbers them (bookworm), and backports with "~".
	strictlyIncreasing(t, Debian, []string{"1.0-1", "1.0-1+deb12u1", "1.0-1+deb12u2", "1.0-1+deb12u10", "1.0-2~bpo12+1",
		"1.0-2"})
	strictlyIncreasing(t, Debian, []string{"2.36-9+deb12u9", "2.36-9+deb12u10", "2.36-9+deb12u11"})
	strictlyIncreasing(t, Debian, []string{"3.0.15-1~deb12u1", "3.0.15-1", "3.0.17-1~deb12u2", "3.0.17-1"})
	strictlyIncreasing(t, Debian, []string{"1.0~rc1", "1.0~rc2", "1.0", "1.0+b1", "1.0.1~rc1"})
	strictlyIncreasing(t, Debian, []string{"1.0a+dfsg-1", "1.0+dfsg-1"}) // ASCII would put "+" (43) before "a" (97)
	// The epoch outweighs everything after it.
	strictlyIncreasing(t, Debian, []string{"2.0", "99.9-99", "1:0.1", "1:0.2", "2:0~rc1", "2147483647:0"})
	// Numbers of any length, compared as numbers: no overflow, leading zeros ignored.
	strictlyIncreasing(t, Debian, []string{"2.9", "2.10", "2.99999999999999999999", "2.100000000000000000000",
		"2.100000000000000000001", "2.1000000000000000000000000000000"})
	strictlyIncreasing(t, Debian, []string{"1.0-9", "1.0-10", "1.0-18446744073709551615", "1.0-18446744073709551616",
		"1.0-99999999999999999999999"})
	checkPairs(t, Debian, []pair{
		{"1.0", 0, "1.0-0"},  // no revision orders like "0" (dpkg: an absent revision is "")
		{"1.0", 0, "1.0-00"}, // and so like any run of zeros
		{"1.0", -1, "1.0-1"}, {"1.0", -1, "1.0-a"}, {"1.0", 1, "1.0-~"},
		{"0:1.0-1", 0, "1.0-1"}, {"00:1.0", 0, "1.0"}, {" 1.0-1 ", 0, "1.0-1"},
		{"1.00000000000000000000000000001", 0, "1.1"}, {"1.0-1-2", 1, "1.0-1"},
		{"1.0.", 0, "1.0.0"},        // an empty digit run is 0
		{"1:1.0:2-1", 1, "1:1.0-1"}, // ":" in the upstream version, allowed with an epoch
	})
}

func TestDpkgInvalid(t *testing.T) {
	for _, v := range []string{"", " ", "a", "a1.0", "~1", ".1", "+1", "1.0-", "1.0-1-", ":1.0", "1:", "a:1.0",
		"-1:1.0", "1.0:1", "1:-1", "1: 1.0", "2147483648:1.0", "99999999999999999999999:1.0", "1.0 1", "1.0_1",
		"1.0@", "1.0-1:2", "1.0-1_1", "1.0-1@", "1.0-a b", "1.0-1~bpo12+1!", "1.ä", "1.0\t1", "1.0/1"} {
		if Valid(Debian, v) {
			t.Errorf("%q valid", v)
		}
		if _, ok := Compare(Debian, v, "1.0"); ok {
			t.Errorf("%q compared", v)
		}
	}
	for _, v := range []string{"0", "1.0", "1:1.0", "2147483647:1.0", "1.0-1", "1.0-1-2", "1:1.0:2-1",
		"1.0+dfsg~rc1-0.1~a", "2024a-0+deb12u1", "1.0~-1", "1.0-~"} {
		if !Valid(Debian, v) {
			t.Errorf("%q invalid", v)
		}
	}
}

func TestDistributionEcosystems(t *testing.T) {
	for in, want := range map[Ecosystem]Ecosystem{"Debian:13": Debian, "Debian:12": Debian, "Debian": Debian,
		"Alpine:v3.23": Alpine, "Alpine:v3.20": Alpine, "PyPI": PyPI, "npm": Npm, "": "", "Debian:": Debian,
		":x": ""} {
		if got := Base(in); got != want {
			t.Errorf("Base(%q): got %q, want %q", in, got, want)
		}
	}
	for _, eco := range []Ecosystem{"Debian:13", "Debian:12", Debian} {
		if c, ok := Compare(eco, "1:0.1", "2.0"); !ok || c != 1 {
			t.Errorf("%s: 1:0.1 > 2.0: got %d (ok %v)", eco, c, ok)
		}
		if !Valid(eco, "2.36-9+deb12u10") || Valid(eco, "1.0_rc1-r0") {
			t.Errorf("%s: Valid uses dpkg", eco)
		}
		if pre, ok := Prerelease(eco, "1.0~rc1-1"); !ok || pre {
			t.Errorf("%s: 1.0~rc1-1: got pre %v (ok %v)", eco, pre, ok)
		}
		if _, ok := Prerelease(eco, "x"); ok {
			t.Errorf("%s: x has no pre-release state", eco)
		}
	}
	for _, eco := range []Ecosystem{"Alpine:v3.23", "Alpine:v3.20", Alpine} {
		if c, ok := Compare(eco, "1.2_rc1-r0", "1.2-r0"); !ok || c != -1 {
			t.Errorf("%s: 1.2_rc1-r0 < 1.2-r0: got %d (ok %v)", eco, c, ok)
		}
		if !Valid(eco, "1.2.3-r1") || Valid(eco, "1:1.2.3") {
			t.Errorf("%s: Valid uses apk", eco)
		}
		if pre, ok := Prerelease(eco, "1.2_rc1-r0"); !ok || pre {
			t.Errorf("%s: 1.2_rc1-r0: got pre %v (ok %v)", eco, pre, ok)
		}
	}
	// A release suffix does not make other ecosystems known, and unknown ones have no order.
	for _, eco := range []Ecosystem{"Ubuntu:22.04", "PyPI:x", "debian:12", "alpine"} {
		if _, ok := Compare(eco, "1.0", "1.0"); ok || Valid(eco, "1.0") {
			t.Errorf("%s: ordered", eco)
		}
	}
}
