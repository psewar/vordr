package versions

import "testing"

// ADR-0129: the upper end of a dependency range, and whether it admits a fix at or above a version.
func TestUpperBound(t *testing.T) {
	none := Bound{None: true}
	below := func(v string) Bound { return Bound{Version: v} }
	upTo := func(v string) Bound { return Bound{Version: v, Inclusive: true} }
	for _, c := range []struct {
		eco  Ecosystem
		rng  string
		want Bound
		ok   bool
	}{
		// npm: caret, tilde, partial and x-ranges as node-semver reads them
		{Npm, "^13.0.0", below("14.0.0"), true},
		{Npm, "^0.2.3", below("0.3.0"), true},
		{Npm, "^0.0.3", below("0.0.4"), true},
		{Npm, "^0.0", below("0.1.0"), true},
		{Npm, "^0.x", below("1.0.0"), true},
		{Npm, "^1.2.3-beta.1", below("2.0.0"), true},
		{Npm, "~1.20.5", below("1.21.0"), true},
		{Npm, "~1", below("2.0.0"), true},
		{Npm, "~>2.1", below("2.2.0"), true},
		{Npm, "1.20.8", upTo("1.20.8"), true},
		{Npm, "=2.1.0", upTo("2.1.0"), true},
		{Npm, "v2.1.0", upTo("2.1.0"), true},
		{Npm, "1.2.x", below("1.3.0"), true},
		{Npm, "1", below("2.0.0"), true},
		{Npm, "*", none, true},
		{Npm, "", none, true},
		{Npm, "x", none, true},
		{Npm, ">=1.0.0", none, true},
		{Npm, "> 1.0.0", none, true},
		{Npm, "<2.0.0", below("2.0.0"), true},
		{Npm, "<= 2.1", below("2.2.0"), true},
		{Npm, "<=2.1.0", upTo("2.1.0"), true},
		{Npm, ">=1.2.3 <1.5.0", below("1.5.0"), true},
		{Npm, ">=1.0.0 <=1.4.2 <1.9.0", upTo("1.4.2"), true},
		{Npm, "1.2.3 - 2.3.4", upTo("2.3.4"), true},
		{Npm, "1.2.3 - 2.3", below("2.4.0"), true},
		{Npm, "^1.0.0 || ^2.0.0", below("3.0.0"), true},
		{Npm, "^2.0.0 || ^1.0.0", below("3.0.0"), true},
		{Npm, "^1.0.0 || >=3", none, true},
		// npm: what is no range
		{Npm, "latest", Bound{}, false},
		{Npm, "npm:other@^1.0.0", Bound{}, false},
		{Npm, "git+https://example.org/a.git", Bound{}, false},
		{Npm, "file:../a", Bound{}, false},
		{Npm, "user/repo", Bound{}, false},
		{Npm, "workspace:*", Bound{}, false},
		{Npm, "^1.0.0 || next", Bound{}, false},
		{Npm, "1.x.2", Bound{}, false},
		{Npm, "<*", Bound{}, false},
		{Npm, "^18446744073709551615.0.0", Bound{}, false},
		// PEP 440
		{PyPI, "==12.3.0", upTo("12.3.0"), true},
		{PyPI, "(==12.3.0)", upTo("12.3.0"), true},
		{PyPI, "<7.0,>=4.5", below("7.0"), true},
		{PyPI, ">=2.14.0,<2.20.0", below("2.20.0"), true},
		{PyPI, "<= 2.1", upTo("2.1"), true},
		{PyPI, "==1.2.*", below("1.3"), true},
		{PyPI, "~=1.4.5", below("1.5"), true},
		{PyPI, "~=2.2", below("3"), true},
		{PyPI, "===1.0", upTo("1.0"), true},
		{PyPI, ">=1.0,!=1.3", none, true},
		{PyPI, "", none, true},
		{PyPI, "~=2", Bound{}, false},
		{PyPI, "==foo", Bound{}, false},
		{PyPI, "@ https://example.org/a.whl", Bound{}, false},
	} {
		got, ok := UpperBound(c.eco, c.rng)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("UpperBound(%s, %q) = %+v %v, want %+v %v", c.eco, c.rng, got, ok, c.want, c.ok)
		}
	}
}

func TestBoundAdmits(t *testing.T) {
	for _, c := range []struct {
		eco      Ecosystem
		rng, fix string
		want     bool
	}{
		{Npm, "1.20.8", "1.20.9", false},  // pinned below the fix
		{Npm, "~1.20.5", "1.20.9", true},  // the fix is in range
		{Npm, "^13.0.0", "14.0.2", false}, // fixed only in a new major
		{Npm, "^6.25.0", "6.29.1", true},
		{Npm, "<=2.1.0", "2.1.0", true},
		{Npm, "<2.1.0", "2.1.0", false},
		{Npm, "*", "99.0.0", true},
		{PyPI, "==12.3.0", "13.0.0", false},
		{PyPI, ">=2.14.0,<2.20.0", "2.24.0", false},
		{PyPI, "<7.0,>=4.5", "6.9.1", true},
		{PyPI, "==48.0.0", "50.0.0", false},
		{PyPI, "==50.0.1", "50.0.0", true}, // a pin above the fix admits a fixed version
	} {
		b, ok := UpperBound(c.eco, c.rng)
		if !ok {
			t.Fatalf("%q unreadable", c.rng)
		}
		if got, ok := b.Admits(c.eco, c.fix); !ok || got != c.want {
			t.Errorf("%q admits %s: %v %v, want %v", c.rng, c.fix, got, ok, c.want)
		}
	}
	if Tighter(Npm, Bound{Version: "2.0.0"}, Bound{Version: "2.0.0", Inclusive: true}) != true ||
		Tighter(Npm, Bound{None: true}, Bound{Version: "1.0.0"}) || !Tighter(Npm, Bound{Version: "1.0.0"}, Bound{None: true}) ||
		Tighter(Npm, Bound{Version: "x"}, Bound{Version: "1.0.0"}) {
		t.Error("Tighter")
	}
}
