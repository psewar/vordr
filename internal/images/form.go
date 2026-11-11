package images

import (
	"regexp"
	"slices"
	"strings"
)

// A tag's form (ADR-0113): an optional "v", numeric parts joined by dots, and a suffix starting with "-", "_" or
// "+". Only tags of the same form are compared: 1.4.2-gpu with x.y.z-gpu, 16 with 17, v1.2 with v1.3.
// Tags without a number (latest, stable, edge) have no form and are only checked for a rebuild.
type form struct {
	prefix string   // "" or "v"
	nums   []string // the numeric parts, as written
	suffix string   // "" or what follows the numbers, separator included
}

var (
	formPattern = regexp.MustCompile(`^(v?)([0-9]{1,18}(?:\.[0-9]{1,18})*)((?:[-_+].*)?)$`)
	// prereleaseWord: a suffix part that marks a pre-release; a variant such as -alpine or -gpu is none.
	prereleaseWord = regexp.MustCompile(`(?i)^(?:alpha|beta|rc|pre|preview|dev|snapshot|nightly|canary|unstable|test)[0-9]*$`)
)

// parseForm reads a tag's form; ok is false for a tag that has none.
func parseForm(tag string) (form, bool) {
	m := formPattern.FindStringSubmatch(tag)
	if m == nil {
		return form{}, false
	}
	return form{prefix: m[1], nums: strings.Split(m[2], "."), suffix: m[3]}, true
}

// same reports whether two forms may be compared: same prefix, as many numeric parts, the same suffix - and each
// number of the same kind: a number of longDigits or more digits is a date (20250108) or a build number, not a
// version, and compares only with another such number. Otherwise alpine:3 would find the date tag 20250108 newer.
func (f form) same(g form) bool {
	if f.prefix != g.prefix || len(f.nums) != len(g.nums) || f.suffix != g.suffix {
		return false
	}
	for i := range f.nums {
		if long(f.nums[i]) != long(g.nums[i]) {
			return false
		}
	}
	return true
}

// longDigits: from this many digits on (leading zeros not counted), a number is a date or build number.
const longDigits = 6

func long(n string) bool { return len(strings.TrimLeft(n, "0")) >= longDigits }

// prerelease reports whether the suffix marks a pre-release (1.2.0-rc1, 2.0-beta.2). Neither is ever a candidate,
// and a running pre-release is only checked for a rebuild.
func (f form) prerelease() bool {
	for _, part := range strings.FieldsFunc(f.suffix, func(r rune) bool { return r == '-' || r == '_' || r == '+' || r == '.' }) {
		if prereleaseWord.MatchString(part) {
			return true
		}
	}
	return false
}

// compare orders two forms that are the same (same), number by number; leading zeros do not count.
func (f form) compare(g form) int {
	for i := range f.nums {
		if c := compareNumber(f.nums[i], g.nums[i]); c != 0 {
			return c
		}
	}
	return 0
}

// major reports whether g changes the first number of f.
func (f form) major(g form) bool { return compareNumber(f.nums[0], g.nums[0]) != 0 }

// compareNumber orders decimal strings of any length without converting them (no overflow).
func compareNumber(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// newerTags returns the tags of the same form as current that are newer and no pre-release, newest first. Nil when
// current has no form or is a pre-release itself.
func newerTags(current string, tags []string) []string {
	cur, ok := parseForm(current)
	if !ok || cur.prerelease() {
		return nil
	}
	type tagForm struct {
		tag  string
		form form
	}
	var newer []tagForm
	for _, t := range tags {
		f, ok := parseForm(t)
		// the same suffix as a running tag that is no pre-release: none of these is one either
		if !ok || !f.same(cur) || f.compare(cur) <= 0 {
			continue
		}
		newer = append(newer, tagForm{t, f})
	}
	// newest first; equal numbers (01 and 1) keep the registry's order
	slices.SortStableFunc(newer, func(a, b tagForm) int { return b.form.compare(a.form) })
	out := make([]string, len(newer))
	for i, n := range newer {
		out[i] = n.tag
	}
	return out
}
