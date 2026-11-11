package versions

import (
	"regexp"
	"strings"
)

// Semver is a parsed npm version (https://semver.org, as node-semver reads it: a leading "v" or "=" is allowed).
type Semver struct {
	Major, Minor, Patch string // digits only, without leading zeros; compared by length, then text (no overflow)
	Pre                 []string
}

var semverPattern = regexp.MustCompile(`^[=v]*([0-9]+)\.([0-9]+)\.([0-9]+)` +
	`(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// ParseSemver reads a version. ok is false for anything that is not one (a range, a tag, "latest").
func ParseSemver(s string) (Semver, bool) {
	m := semverPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Semver{}, false
	}
	v := Semver{Major: trimZeros(m[1]), Minor: trimZeros(m[2]), Patch: trimZeros(m[3])}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, true // build metadata (after "+") does not take part in the order
}

func trimZeros(d string) string {
	d = strings.TrimLeft(d, "0")
	if d == "" {
		return "0"
	}
	return d
}

// IsPrerelease: npm installs those only when asked for them by name.
func (v Semver) IsPrerelease() bool { return len(v.Pre) > 0 }

// CompareSemver orders a and b: -1, 0 or 1. A pre-release sorts before its release; numeric identifiers sort as
// numbers and before alphanumeric ones; more identifiers sort after fewer when the rest is equal.
func CompareSemver(a, b Semver) int {
	for _, p := range [][2]string{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c := cmpDigits(p[0], p[1]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < min(len(a.Pre), len(b.Pre)); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, yn := allDigits(x), allDigits(y)
		var c int
		switch {
		case xn && yn:
			c = cmpDigits(trimZeros(x), trimZeros(y))
		case xn:
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(a.Pre), len(b.Pre))
}

// cmpDigits compares two numbers written without leading zeros, of any length.
func cmpDigits(a, b string) int {
	if c := cmpInt(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
