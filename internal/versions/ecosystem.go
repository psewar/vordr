package versions

import (
	"regexp"
	"strings"
)

// Ecosystem names as OSV writes them.
type Ecosystem string

const (
	PyPI Ecosystem = "PyPI"
	Npm  Ecosystem = "npm"
	// Debian and Alpine: OSV writes them with the release ("Debian:12", "Alpine:v3.20"); Base strips it.
	Debian Ecosystem = "Debian"
	Alpine Ecosystem = "Alpine"
	// OCI: container images (ADR-0113). Not an OSV ecosystem and not one this package orders: tags are compared by
	// their form in package images.
	OCI Ecosystem = "oci"
)

// Known reports whether e is an ecosystem this package can order.
func (e Ecosystem) Known() bool { return e == PyPI || e == Npm }

// Base is the ecosystem without the release OSV appends after ":" ("Debian:13" is Debian, "PyPI" stays PyPI).
// Versions are ordered by the rules of the package manager, not per release: where those changed between releases
// (apk-tools 2 before Alpine v3.23, 3 since), Compare gives no order rather than pick one.
func Base(e Ecosystem) Ecosystem {
	if i := strings.IndexByte(string(e), ':'); i >= 0 {
		return e[:i]
	}
	return e
}

// Compare orders two versions of the ecosystem. ok is false when either does not parse, or (Alpine) when apk-tools 2
// and 3 order the two differently: then there is no order, and the caller must not guess one.
func Compare(e Ecosystem, a, b string) (c int, ok bool) {
	switch {
	case e == PyPI:
		x, okX := ParsePEP440(a)
		y, okY := ParsePEP440(b)
		if okX && okY {
			return ComparePEP440(x, y), true
		}
	case e == Npm:
		x, okX := ParseSemver(a)
		y, okY := ParseSemver(b)
		if okX && okY {
			return CompareSemver(x, y), true
		}
	case Base(e) == Debian:
		x, okX := ParseDpkg(a)
		y, okY := ParseDpkg(b)
		if okX && okY {
			return CompareDpkg(x, y), true
		}
	case Base(e) == Alpine:
		x, okX := ParseApk(a)
		y, okY := ParseApk(b)
		if okX && okY && !apkVersionsDisagree(x, y) {
			return CompareApk(x, y), true
		}
	}
	return 0, false
}

// Valid reports whether v is a version of the ecosystem.
func Valid(e Ecosystem, v string) bool {
	switch {
	case e == PyPI:
		_, ok := ParsePEP440(v)
		return ok
	case e == Npm:
		_, ok := ParseSemver(v)
		return ok
	case Base(e) == Debian:
		_, ok := ParseDpkg(v)
		return ok
	case Base(e) == Alpine:
		_, ok := ParseApk(v)
		return ok
	}
	return false
}

// Prerelease reports whether v is a pre-release (PyPI: a, b, rc, dev; npm: anything after "-"). Neither pip nor npm
// installs one unless asked for it by version. Debian and Alpine have none: apt and apk install whatever version the
// release carries, "1.0~rc1" or "1.0_rc1" included. ok is false when v does not parse.
func Prerelease(e Ecosystem, v string) (pre bool, ok bool) {
	switch {
	case e == PyPI:
		p, ok := ParsePEP440(v)
		return ok && p.IsPrerelease(), ok
	case e == Npm:
		s, ok := ParseSemver(v)
		return ok && s.IsPrerelease(), ok
	case Base(e) == Debian, Base(e) == Alpine:
		return false, Valid(e, v)
	}
	return false, false
}

var pypiSeparators = regexp.MustCompile(`[-_.]+`)

// NormalizeName returns the name under which the ecosystem treats a package as the same: PyPI ignores case and
// treats runs of "-", "_" and "." alike (PEP 503: "Foo.Bar" is "foo-bar"); npm names are exact.
func NormalizeName(e Ecosystem, name string) string {
	if e == PyPI {
		return pypiSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	}
	return strings.TrimSpace(name)
}
