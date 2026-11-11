package versions

import (
	"strconv"
	"strings"
)

// maxEpoch is dpkg's limit (INT_MAX); it refuses larger epochs as "too big".
const maxEpoch = "2147483647"

// Dpkg is a parsed Debian package version, [epoch:]upstream[-revision] (Debian Policy 5.6.12), read and ordered as
// dpkg does (lib/dpkg/parsehelp.c parseversion, lib/dpkg/version.c verrevcmp).
type Dpkg struct {
	Epoch    uint64 // 0 when not written
	Upstream string
	Revision string // after the last "-"; "" when not written, which dpkg orders like "0" (1.0 == 1.0-0)
}

// ParseDpkg reads a version as dpkg does. ok is false for anything dpkg refuses or only warns about: an empty or
// non-numeric epoch, one above dpkg's limit (INT_MAX), an empty upstream version or revision, an upstream version
// that does not start with a digit, a character the policy does not allow, embedded spaces.
func ParseDpkg(s string) (Dpkg, bool) {
	s = strings.TrimSpace(s)
	var v Dpkg
	// The first ":" ends the epoch, so a ":" in the upstream version is only possible with one, as the policy wants.
	if i := strings.IndexByte(s, ':'); i >= 0 {
		e := s[:i]
		if !allDigits(e) || cmpDigits(strings.TrimLeft(e, "0"), maxEpoch) > 0 {
			return Dpkg{}, false
		}
		n, _ := strconv.ParseUint(e, 10, 64) // cannot fail: digits only, at most maxEpoch
		v.Epoch, s = n, s[i+1:]
	}
	// The last "-" starts the revision, so a "-" in the upstream version is only possible with one.
	if i := strings.LastIndexByte(s, '-'); i >= 0 {
		v.Revision, s = s[i+1:], s[:i]
		if v.Revision == "" {
			return Dpkg{}, false
		}
	}
	v.Upstream = s
	if s == "" || !isDigit(s[0]) || !dpkgChars(s, ".+~-:") || !dpkgChars(v.Revision, ".+~") {
		return Dpkg{}, false
	}
	return v, true
}

// dpkgChars: only ASCII letters, digits and the given punctuation (dpkg's c_isalpha is ASCII only).
func dpkgChars(s, punct string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !isDigit(c) && !isLetter(c) && strings.IndexByte(punct, c) < 0 {
			return false
		}
	}
	return true
}

// CompareDpkg orders a and b: -1, 0 or 1. Epoch first, then upstream version, then revision.
func CompareDpkg(a, b Dpkg) int {
	if c := cmpU(a.Epoch, b.Epoch); c != 0 {
		return c
	}
	if c := verrevcmp(a.Upstream, b.Upstream); c != 0 {
		return c
	}
	return verrevcmp(a.Revision, b.Revision)
}

// verrevcmp is dpkg's comparison of an upstream version or a revision: alternately a run of non-digits, compared
// character by character by dpkgOrder, and a run of digits, compared as a number of any length.
func verrevcmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		// Equal weights mean the same character, so i and j move together and never past the end.
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			if c := cmpInt(dpkgOrder(a, i), dpkgOrder(b, j)); c != 0 {
				return c
			}
			i++
			j++
		}
		ei, ej := digitsEnd(a, i), digitsEnd(b, j)
		if c := cmpDigits(strings.TrimLeft(a[i:ei], "0"), strings.TrimLeft(b[j:ej], "0")); c != 0 {
			return c
		}
		i, j = ei, ej
	}
	return 0
}

// dpkgOrder is the weight of the character at i in a non-digit run: "~" sorts before everything, even the end of the
// run (1.0~rc1 < 1.0), then the end (a digit or the end of the string), then letters, then all other characters
// (1.0a < 1.0+b1).
func dpkgOrder(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	switch c := s[i]; {
	case isDigit(c):
		return 0
	case isLetter(c):
		return int(c)
	case c == '~':
		return -1
	default:
		return int(c) + 256
	}
}

// digitsEnd returns the index after the run of digits starting at i.
func digitsEnd(s string, i int) int {
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
