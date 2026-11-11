// Package versions orders package versions the way their ecosystem does: PEP 440 for PyPI, semantic versioning for
// npm, dpkg's order for Debian and apk's for Alpine (the packages inside container images, ADR-0120). OSV ranges
// (design/architecture.md, sections 11.2 and 11.5) are only as good as this order, so a version that does not parse is
// reported as such and never guessed at.
package versions

import (
	"regexp"
	"strconv"
	"strings"
)

// PEP440 is a parsed PyPI version (https://packaging.python.org/en/latest/specifications/version-specifiers/).
type PEP440 struct {
	Epoch   uint64
	Release []uint64 // as written; missing parts compare as 0, so 1.0 == 1.0.0
	Pre     *prePart // a, b or rc with its number; nil: no pre-release
	Post    *uint64  // nil: no post-release
	Dev     *uint64  // nil: no dev release
	Local   []localPart
}

type prePart struct {
	Kind int // 0 a, 1 b, 2 rc
	N    uint64
}

// localPart: a segment of the local version label; numeric segments compare as numbers and sort after strings.
type localPart struct {
	Num   uint64
	Str   string
	IsNum bool
}

// The pattern of the specification (packaging.version.VERSION_PATTERN), case-insensitive, whole string.
var pep440Pattern = regexp.MustCompile(`(?i)^v?` +
	`(?:(?P<epoch>[0-9]+)!)?` +
	`(?P<release>[0-9]+(?:\.[0-9]+)*)` +
	`(?:[-_.]?(?P<pre_l>alpha|a|beta|b|preview|pre|c|rc)[-_.]?(?P<pre_n>[0-9]+)?)?` +
	`(?:(?:-(?P<post_n1>[0-9]+))|(?:[-_.]?(?P<post_l>post|rev|r)[-_.]?(?P<post_n2>[0-9]+)?))?` +
	`(?:[-_.]?(?P<dev_l>dev)[-_.]?(?P<dev_n>[0-9]+)?)?` +
	`(?:\+(?P<local>[a-z0-9]+(?:[-_.][a-z0-9]+)*))?$`)

var pepGroup = func() map[string]int {
	m := map[string]int{}
	for i, n := range pep440Pattern.SubexpNames() {
		if n != "" {
			m[n] = i
		}
	}
	return m
}()

// ParsePEP440 reads a version as pip does. ok is false for anything PEP 440 does not allow (legacy versions such
// as "2004d" or "1.0-final-1"); those can still be found by exact string in an OSV versions list.
func ParsePEP440(s string) (PEP440, bool) {
	m := pep440Pattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return PEP440{}, false
	}
	g := func(name string) string { return m[pepGroup[name]] }
	var v PEP440
	ok := true
	num := func(s string) uint64 {
		if s == "" {
			return 0 // an implicit number: "1.0a" is 1.0a0, "1.0.post" is 1.0.post0
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			ok = false // more than 64 bits: no version we can order safely
		}
		return n
	}
	if e := g("epoch"); e != "" {
		v.Epoch = num(e)
	}
	for _, part := range strings.Split(g("release"), ".") {
		v.Release = append(v.Release, num(part))
	}
	if l := strings.ToLower(g("pre_l")); l != "" {
		kind := 2 // c, rc, pre, preview
		switch l {
		case "a", "alpha":
			kind = 0
		case "b", "beta":
			kind = 1
		}
		v.Pre = &prePart{Kind: kind, N: num(g("pre_n"))}
	}
	switch {
	case g("post_n1") != "":
		n := num(g("post_n1"))
		v.Post = &n
	case g("post_l") != "":
		n := num(g("post_n2"))
		v.Post = &n
	}
	if g("dev_l") != "" {
		n := num(g("dev_n"))
		v.Dev = &n
	}
	if l := g("local"); l != "" {
		for _, seg := range strings.FieldsFunc(strings.ToLower(l), func(r rune) bool {
			return r == '-' || r == '_' || r == '.'
		}) {
			if n, err := strconv.ParseUint(seg, 10, 64); err == nil {
				v.Local = append(v.Local, localPart{Num: n, IsNum: true})
			} else {
				v.Local = append(v.Local, localPart{Str: seg})
			}
		}
	}
	return v, ok
}

// IsPrerelease: a, b, rc or dev - pip skips those unless asked for them.
func (v PEP440) IsPrerelease() bool { return v.Pre != nil || v.Dev != nil }

// Major is the first release number.
func (v PEP440) Major() uint64 { return v.Release[0] }

// ComparePEP440 orders a and b: -1, 0 or 1. The order of the specification: dev < pre < release < post, epochs
// first, local labels last (1.0 < 1.0+local).
func ComparePEP440(a, b PEP440) int {
	if c := cmpU(a.Epoch, b.Epoch); c != 0 {
		return c
	}
	for i := 0; i < max(len(a.Release), len(b.Release)); i++ {
		if c := cmpU(at(a.Release, i), at(b.Release, i)); c != 0 {
			return c
		}
	}
	if c := cmpInt(a.preRank(), b.preRank()); c != 0 {
		return c
	}
	if a.Pre != nil && b.Pre != nil {
		if c := cmpInt(a.Pre.Kind, b.Pre.Kind); c != 0 {
			return c
		}
		if c := cmpU(a.Pre.N, b.Pre.N); c != 0 {
			return c
		}
	}
	if c := cmpOpt(a.Post, b.Post, -1); c != 0 { // no post-release sorts before any
		return c
	}
	if c := cmpOpt(a.Dev, b.Dev, 1); c != 0 { // no dev release sorts after any
		return c
	}
	return compareLocal(a.Local, b.Local)
}

// preRank: a bare dev release (1.0.dev1) sorts before every pre-release of the same release, a final or post release
// after all of them.
func (v PEP440) preRank() int {
	switch {
	case v.Pre != nil:
		return 0
	case v.Post == nil && v.Dev != nil:
		return -1
	}
	return 1
}

// cmpOpt compares optional numbers; absent sorts as `absent` (-1: before every number, 1: after).
func cmpOpt(a, b *uint64, absent int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return absent
	case b == nil:
		return -absent
	}
	return cmpU(*a, *b)
}

func compareLocal(a, b []localPart) int {
	if len(a) == 0 || len(b) == 0 {
		return cmpInt(len(a), len(b)) // no local label sorts first
	}
	for i := 0; i < min(len(a), len(b)); i++ {
		x, y := a[i], b[i]
		switch {
		case x.IsNum && y.IsNum:
			if c := cmpU(x.Num, y.Num); c != 0 {
				return c
			}
		case x.IsNum != y.IsNum:
			if x.IsNum {
				return 1 // numbers after strings
			}
			return -1
		default:
			if c := strings.Compare(x.Str, y.Str); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func at(s []uint64, i int) uint64 {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func cmpU(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
