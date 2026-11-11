package versions

import "testing"

// strictlyIncreasing checks that every version sorts after the one before it, and that the order is antisymmetric.
func strictlyIncreasing(t *testing.T, eco Ecosystem, list []string) {
	t.Helper()
	for i := 0; i+1 < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			c, ok := Compare(eco, list[i], list[j])
			if !ok || c != -1 {
				t.Errorf("%s: %q < %q: got %d (ok %v)", eco, list[i], list[j], c, ok)
			}
			if c, _ := Compare(eco, list[j], list[i]); c != 1 {
				t.Errorf("%s: %q > %q: got %d", eco, list[j], list[i], c)
			}
		}
	}
}

func TestPEP440Order(t *testing.T) {
	// The example of the specification, "Summary of permitted suffixes and relative ordering".
	strictlyIncreasing(t, PyPI, []string{
		"1.dev0", "1.0.dev456", "1.0a1", "1.0a2.dev456", "1.0a12.dev456", "1.0a12", "1.0b1.dev456", "1.0b2",
		"1.0b2.post345.dev456", "1.0b2.post345", "1.0rc1.dev456", "1.0rc1", "1.0", "1.0+abc.5", "1.0+abc.7", "1.0+5",
		"1.0.post456.dev34", "1.0.post456", "1.0.15", "1.1.dev1",
	})
	// Event versions from real OSV entries (October 2026), in order: zodb3 PYSEC-2009-10, skia-python
	// GHSA-2mhw-8qcg-gr96, ocrodjvu GHSA-5pjj-7m4p-wfh2, pyblade GHSA-23jg-5f8m-gw8c, sglang GHSA-36m8-w8qf-g76p.
	strictlyIncreasing(t, PyPI, []string{"3.8", "3.8.1b9", "3.8.2", "3.8.3", "3.9a0", "3.9.0b5", "3.9.0c1", "3.9.0rc2",
		"3.9.0"})
	strictlyIncreasing(t, PyPI, []string{"116.0b2", "144.0", "144.0.post0", "144.0.post1"})
	strictlyIncreasing(t, PyPI, []string{"0.4.6", "0.4.6-1", "0.4.6.post2", "0.4.7"})
	strictlyIncreasing(t, PyPI, []string{"0.1.8-alpha", "0.1.8a1", "0.1.8", "0.1.9", "0.2.0-alpha", "0.2.0a1", "0.2.0"})
	strictlyIncreasing(t, PyPI, []string{"0.4.1.post6", "0.4.1.post7", "0.4.2", "0.4.10", "0.5.12"})
	strictlyIncreasing(t, PyPI, []string{"2015.01.25", "2015.01.30.1", "2021.12.17", "2021.12.18"})
	strictlyIncreasing(t, PyPI, []string{"2.0", "1!0.1", "1!1.0", "2!0"}) // epochs first
}

func TestPEP440Equal(t *testing.T) {
	for _, p := range [][2]string{
		{"1.0", "1.0.0"}, {"1", "1.0.0.0"}, {"1.0a", "1.0a0"}, {"1.0-1", "1.0.post1"}, {"1.0.post", "1.0.post0"},
		{"1.0RC1", "1.0rc1"}, {"1.0c1", "1.0rc1"}, {"1.0pre1", "1.0rc1"}, {"1.0preview1", "1.0rc1"},
		{"1.0alpha1", "1.0a1"}, {"1.0beta2", "1.0b2"}, {"1.0-r4", "1.0.post4"}, {"1.0rev4", "1.0.post4"},
		{"v1.0", "1.0"}, {"1.0.DEV1", "1.0.dev1"}, {"1.0+ABC", "1.0+abc"}, {"1.0+a-b_c", "1.0+a.b.c"},
		{" 1.0 ", "1.0"}, {"0!1.0", "1.0"}, {"1.0+007", "1.0+7"},
	} {
		if c, ok := Compare(PyPI, p[0], p[1]); !ok || c != 0 {
			t.Errorf("%q == %q: got %d (ok %v)", p[0], p[1], c, ok)
		}
	}
}

func TestPEP440Invalid(t *testing.T) {
	// Legacy versions pip no longer accepts; OSV lists can still name them, matched by exact string only.
	for _, v := range []string{"", "latest", "2004d", "1.0-final", "1.0.x", "1..0", "1.0+", "1.0+a..b", "dev",
		"1.0a1b2", "99999999999999999999.0"} {
		if _, ok := ParsePEP440(v); ok {
			t.Errorf("%q parsed", v)
		}
		if _, ok := Compare(PyPI, v, "1.0"); ok {
			t.Errorf("%q compared", v)
		}
	}
}

func TestPEP440Prerelease(t *testing.T) {
	for v, want := range map[string]bool{"1.0": false, "1.0a1": true, "1.0.dev0": true, "1.0.post1": false,
		"1.0rc1": true, "1.0.post1.dev0": true, "1.0+local": false} {
		if got, ok := Prerelease(PyPI, v); !ok || got != want {
			t.Errorf("%q prerelease: got %v (ok %v)", v, got, ok)
		}
	}
}

func TestSemverOrder(t *testing.T) {
	// The example of semver.org, section 11.
	strictlyIncreasing(t, Npm, []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "2.0.0", "2.1.0", "2.1.1"})
	// Event versions from real OSV entries: payload GHSA-238x-w2j9-gwwr, h3 GHSA-26f5-8h2x-34xh, strapi-admin
	// GHSA-23fp-fmrv-f5px, MAL-2022-250 ("0.0.1-security" is npm's placeholder after removing malware).
	strictlyIncreasing(t, Npm, []string{"3.99.0", "4.0.0-canary.0", "4.0.0-canary.9", "4.0.0-canary.33",
		"4.0.0-canary.34", "4.0.0"})
	strictlyIncreasing(t, Npm, []string{"2.0.0-alpha.1", "2.0.0-beta.0", "2.0.0", "2.0.1-rc.8", "2.0.1-rc.9",
		"2.0.1-rc.15", "2.0.1"})
	strictlyIncreasing(t, Npm, []string{"2.0.0", "3.0.0-beta.18", "3.0.0-beta.18.3", "3.0.0-beta.18.4", "3.0.0"})
	strictlyIncreasing(t, Npm, []string{"0.0.1-security", "0.0.1", "1.9.9", "99.9.8"})
	strictlyIncreasing(t, Npm, []string{"9.0.0", "10.0.0", "99999999999999999999.0.0", "100000000000000000000.0.0"})
}

func TestSemverEqualAndInvalid(t *testing.T) {
	for _, p := range [][2]string{{"1.2.3", "v1.2.3"}, {"1.2.3", "=1.2.3"}, {"1.2.3+build.7", "1.2.3"},
		{"1.2.3-rc.1+b", "1.2.3-rc.1"}, {"01.2.3", "1.2.3"}, {"1.2.3-rc.01", "1.2.3-rc.1"}} {
		if c, ok := Compare(Npm, p[0], p[1]); !ok || c != 0 {
			t.Errorf("%q == %q: got %d (ok %v)", p[0], p[1], c, ok)
		}
	}
	for _, v := range []string{"", "1.0", "1", "^1.2.3", "~1.2.3", ">=1.0.0", "latest", "1.2.3-", "1.2.3-a..b",
		"1.2.3.4", "x.1.2"} {
		if _, ok := ParseSemver(v); ok {
			t.Errorf("%q parsed", v)
		}
	}
	if pre, ok := Prerelease(Npm, "0.0.1-security"); !ok || !pre {
		t.Error("0.0.1-security is a pre-release")
	}
}

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{"Requests": "requests", "zope.interface": "zope-interface",
		"Foo__Bar-.baz": "foo-bar-baz", "ruamel.yaml.clib": "ruamel-yaml-clib"} {
		if got := NormalizeName(PyPI, in); got != want {
			t.Errorf("PyPI %q: got %q", in, got)
		}
	}
	if got := NormalizeName(Npm, "@Scope/Name"); got != "@Scope/Name" {
		t.Errorf("npm names are exact: %q", got)
	}
}
