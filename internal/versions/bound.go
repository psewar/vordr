package versions

import (
	"strconv"
	"strings"
)

// Bound is the upper end of a dependency range (ADR-0129): the highest version the range admits. None: it has no upper
// end and holds nothing back.
type Bound struct {
	Version   string // in the ecosystem's order
	Inclusive bool   // Version itself is admitted
	None      bool
}

// UpperBound reads the upper end of a dependency range as its ecosystem writes it: npm's ranges (node-semver) and
// PEP 440 specifiers. ok is false for anything it cannot read - a tag, a URL, git, an alias, a range it does not know;
// a caller then takes it as holding nothing.
func UpperBound(eco Ecosystem, rng string) (Bound, bool) {
	switch eco {
	case Npm:
		return npmUpper(rng)
	case PyPI:
		return pepUpper(rng)
	}
	return Bound{}, false
}

// Admits reports whether a range with this upper end admits some version at or above v. ok is false when the two cannot
// be ordered.
func (b Bound) Admits(eco Ecosystem, v string) (admits, ok bool) {
	if b.None {
		return true, true
	}
	c, ok := Compare(eco, b.Version, v)
	if !ok {
		return false, false
	}
	return c > 0 || c == 0 && b.Inclusive, true
}

// Tighter reports whether a ends below b. Two ends that cannot be ordered are not tighter.
func Tighter(eco Ecosystem, a, b Bound) bool {
	switch {
	case a.None:
		return false
	case b.None:
		return true
	}
	c, ok := Compare(eco, a.Version, b.Version)
	if !ok {
		return false
	}
	return c < 0 || c == 0 && !a.Inclusive && b.Inclusive
}

// --- npm ------------------------------------------------------------------------------------------------------------

// npmUpper: a union ("||") admits what its loosest part admits; a part is a hyphen range or comparators that all hold.
func npmUpper(rng string) (Bound, bool) {
	var out Bound
	for i, alt := range strings.Split(rng, "||") {
		b, ok := npmAltUpper(strings.TrimSpace(alt))
		if !ok {
			return Bound{}, false
		}
		if b.None {
			return Bound{None: true}, true
		}
		if i == 0 || Tighter(Npm, out, b) {
			out = b
		}
	}
	return out, true
}

var npmOperators = []string{"<=", ">=", "~>", "<", ">", "=", "^", "~"}

func npmAltUpper(alt string) (Bound, bool) {
	if alt == "" || alt == "*" || alt == "x" || alt == "X" {
		return Bound{None: true}, true
	}
	if _, upper, ok := strings.Cut(alt, " - "); ok { // hyphen range: a full upper end is inclusive, a partial one runs up to the next
		p, ok := parsePartial(strings.TrimSpace(upper))
		if !ok {
			return Bound{}, false
		}
		return p.atMost()
	}
	fields := strings.Fields(alt)
	var comps []string
	for i := 0; i < len(fields); i++ {
		if isNpmOperator(fields[i]) && i+1 < len(fields) { // node-semver allows ">= 1.2.3"
			comps = append(comps, fields[i]+fields[i+1])
			i++
			continue
		}
		comps = append(comps, fields[i])
	}
	out := Bound{None: true}
	for _, c := range comps {
		b, ok := npmComparatorUpper(c)
		if !ok {
			return Bound{}, false
		}
		if Tighter(Npm, b, out) {
			out = b
		}
	}
	return out, true
}

func isNpmOperator(s string) bool {
	for _, o := range npmOperators {
		if s == o {
			return true
		}
	}
	return false
}

func npmComparatorUpper(c string) (Bound, bool) {
	op := ""
	for _, o := range npmOperators {
		if strings.HasPrefix(c, o) {
			op, c = o, c[len(o):]
			break
		}
	}
	p, ok := parsePartial(strings.TrimSpace(c))
	if !ok {
		return Bound{}, false
	}
	switch op {
	case ">", ">=":
		return Bound{None: true}, true
	case "<":
		if p.n == 0 {
			return Bound{}, false // "<*" admits nothing: not a range anyone writes
		}
		return Bound{Version: p.floor()}, true
	case "<=", "=", "":
		return p.atMost()
	case "^":
		switch {
		case p.n == 0:
			return Bound{None: true}, true
		case p.nums[0] > 0 || p.n == 1:
			return p.next(0)
		case p.nums[1] > 0 || p.n == 2:
			return p.next(1)
		}
		return p.next(2)
	}
	// "~" and "~>": the minor version may not change, or the major when only that is given
	switch p.n {
	case 0:
		return Bound{None: true}, true
	case 1:
		return p.next(0)
	}
	return p.next(1)
}

// partial is an npm version that may stop early or end in a wildcard: "1", "1.2", "1.2.x", "*".
type partial struct {
	nums [3]uint64
	n    int    // how many numbers are given
	pre  string // "-beta.1", only with all three numbers
}

func parsePartial(s string) (partial, bool) {
	s = strings.TrimLeft(s, "v=")
	if s == "" {
		return partial{}, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i] // build metadata takes no part in the order
	}
	var p partial
	if i := strings.IndexByte(s, '-'); i >= 0 {
		p.pre, s = s[i:], s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return partial{}, false
	}
	wild := false
	for _, part := range parts {
		if part == "x" || part == "X" || part == "*" {
			wild = true
			continue
		}
		if wild || part == "" {
			return partial{}, false // a number after a wildcard
		}
		v, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return partial{}, false // not a number: a tag, a URL, an alias
		}
		p.nums[p.n] = v
		p.n++
	}
	if p.pre != "" && p.n < 3 {
		return partial{}, false
	}
	return p, true
}

func (p partial) floor() string {
	return strconv.FormatUint(p.nums[0], 10) + "." + strconv.FormatUint(p.nums[1], 10) + "." +
		strconv.FormatUint(p.nums[2], 10) + p.pre
}

// atMost: the range "<= p": a full version is its own upper end, a partial one runs up to the next.
func (p partial) atMost() (Bound, bool) {
	switch p.n {
	case 0:
		return Bound{None: true}, true
	case 3:
		return Bound{Version: p.floor(), Inclusive: true}, true
	}
	return p.next(p.n - 1)
}

// next: below the version that raises number i by one and zeroes what follows.
func (p partial) next(i int) (Bound, bool) {
	if p.nums[i] == ^uint64(0) {
		return Bound{}, false
	}
	q := partial{n: 3}
	copy(q.nums[:i], p.nums[:i])
	q.nums[i] = p.nums[i] + 1
	return Bound{Version: q.floor()}, true
}

// --- PEP 440 --------------------------------------------------------------------------------------------------------

// pepUpper: comma-separated clauses that all hold; Requires-Dist may put them in parentheses.
func pepUpper(spec string) (Bound, bool) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "(") && strings.HasSuffix(spec, ")") {
		spec = strings.TrimSpace(spec[1 : len(spec)-1])
	}
	out := Bound{None: true}
	if spec == "" {
		return out, true
	}
	for _, clause := range strings.Split(spec, ",") {
		b, ok := pepClauseUpper(strings.TrimSpace(clause))
		if !ok {
			return Bound{}, false
		}
		if Tighter(PyPI, b, out) {
			out = b
		}
	}
	return out, true
}

var pepOperators = []string{"===", "==", "!=", "~=", "<=", ">=", "<", ">"}

func pepClauseUpper(c string) (Bound, bool) {
	for _, op := range pepOperators {
		if !strings.HasPrefix(c, op) {
			continue
		}
		v := strings.TrimSpace(c[len(op):])
		if op == "==" && strings.HasSuffix(v, ".*") { // a prefix match runs up to the next release of the prefix
			p, ok := ParsePEP440(strings.TrimSuffix(v, ".*"))
			if !ok {
				return Bound{}, false
			}
			return pepNext(p, len(p.Release)-1), true
		}
		p, ok := ParsePEP440(v)
		if !ok {
			return Bound{}, false
		}
		switch op {
		case ">", ">=", "!=":
			return Bound{None: true}, true
		case "~=":
			if len(p.Release) < 2 {
				return Bound{}, false // the specification requires two numbers
			}
			return pepNext(p, len(p.Release)-2), true
		case "<":
			return Bound{Version: v}, true
		}
		return Bound{Version: v, Inclusive: true}, true // "==", "===", "<="
	}
	return Bound{}, false
}

// pepNext: below the release that raises number i of p by one and drops what follows.
func pepNext(p PEP440, i int) Bound {
	parts := make([]string, 0, i+1)
	for j := 0; j < i; j++ {
		parts = append(parts, strconv.FormatUint(p.Release[j], 10))
	}
	parts = append(parts, strconv.FormatUint(p.Release[i]+1, 10))
	v := strings.Join(parts, ".")
	if p.Epoch > 0 {
		v = strconv.FormatUint(p.Epoch, 10) + "!" + v
	}
	return Bound{Version: v}
}
