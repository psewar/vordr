package versions

import "strings"

// Apk is a parsed Alpine package version, digits{.digits}[letter]{_suffix[digits]}[-rdigits], ordered as apk-tools
// orders it (src/version.c, apk_version_compare).
type Apk struct {
	tokens []apkToken
}

// apkToken is one part of a version.
type apkToken struct {
	kind   apkKind
	text   string // the digits or the letter as written
	suffix int    // apkSuffix: its rank in apkSuffixRank
}

// apkKind in apk's order. Where two versions agree up to a point and then go on with different kinds, the one going on
// with the earlier kind is the greater, unless that is a pre-release suffix: 1.0_rc1 < 1.0 < 1.0-r0 < 1.0_p1 < 1.0a
// < 1.0.1.
type apkKind int

const (
	apkFirstNumber  apkKind = iota
	apkNumber               // after "."
	apkLetter               // a single lowercase letter after the numbers
	apkSuffix               // "_alpha" and so on
	apkSuffixNumber         // "_alpha2"
	apkRevision             // "-r2"
	apkEnd
)

// apkSuffixRank: below zero a pre-release, which sorts before the version without suffix (rank 0).
var apkSuffixRank = map[string]int{"alpha": -4, "beta": -3, "pre": -2, "rc": -1, "cvs": 1, "svn": 2, "git": 3, "hg": 4,
	"p": 5}

// apkMaxDigits: apk-tools 2 refuses numbers of 18 digits or more, apk-tools 3 silently wraps them above 64 bits.
const apkMaxDigits = 17

// ParseApk reads a version. ok is false for anything apk refuses, and also for what only one of apk-tools 2 (Alpine
// before v3.23) and 3 accepts, since OSV names the release but the order must hold in all of them: apk-tools 3's
// commit hash ("1.0~1234abcd"), apk-tools 2's digits after the letter ("1.0a1"), numbers of 18 or more digits.
func ParseApk(s string) (Apk, bool) {
	s = strings.TrimSpace(s)
	n := digitsEnd(s, 0)
	if n == 0 || n > apkMaxDigits {
		return Apk{}, false
	}
	v := Apk{tokens: []apkToken{{kind: apkFirstNumber, text: s[:n]}}}
	// number adds the digits from s[i] on as a token of the kind; false when there are none or too many.
	number := func(kind apkKind, i int) (int, bool) {
		e := digitsEnd(s, i)
		v.tokens = append(v.tokens, apkToken{kind: kind, text: s[i:e]})
		return e, e > i && e-i <= apkMaxDigits
	}
	// Each part must come after the ones apk allows before it: one letter after the numbers, suffixes after that,
	// the revision last.
	for i, ok := n, true; i < len(s); {
		switch last := v.tokens[len(v.tokens)-1].kind; {
		case s[i] >= 'a' && s[i] <= 'z' && last <= apkNumber:
			v.tokens = append(v.tokens, apkToken{kind: apkLetter, text: s[i : i+1]})
			i++
		case s[i] == '.' && last <= apkNumber:
			i, ok = number(apkNumber, i+1)
		case s[i] == '_' && last <= apkSuffixNumber:
			e := i + 1
			for e < len(s) && s[e] >= 'a' && s[e] <= 'z' {
				e++
			}
			var rank int
			if rank, ok = apkSuffixRank[s[i+1:e]]; ok {
				v.tokens = append(v.tokens, apkToken{kind: apkSuffix, suffix: rank})
			}
			i = e
			if ok && i < len(s) && isDigit(s[i]) {
				i, ok = number(apkSuffixNumber, i)
			}
		case strings.HasPrefix(s[i:], "-r") && last < apkRevision:
			i, ok = number(apkRevision, i+2)
		default:
			ok = false
		}
		if !ok {
			return Apk{}, false
		}
	}
	return v, true
}

func (v Apk) token(i int) apkToken {
	if i < len(v.tokens) {
		return v.tokens[i]
	}
	return apkToken{kind: apkEnd}
}

// CompareApk orders a and b as apk-tools 3 does: -1, 0 or 1. Part by part: numbers as numbers, except that two
// numbers after a "." compare as strings when either starts with "0" (1.01 < 1.1, 8.2.0015 < 8.2.002); the letter;
// the suffix by rank and then its number; the revision as a number.
func CompareApk(a, b Apk) int {
	for i := 0; ; i++ {
		x, y := a.token(i), b.token(i)
		if x.kind != y.kind {
			switch {
			case x.kind == apkSuffix && x.suffix < 0:
				return -1
			case y.kind == apkSuffix && y.suffix < 0:
				return 1
			}
			return cmpInt(int(y.kind), int(x.kind)) // going on with the earlier kind is greater
		}
		if x.kind == apkEnd {
			return 0
		}
		if c := apkTokenCompare(x, y); c != 0 {
			return c
		}
	}
}

func apkTokenCompare(x, y apkToken) int {
	switch x.kind {
	case apkNumber:
		if x.text[0] == '0' || y.text[0] == '0' {
			return strings.Compare(x.text, y.text)
		}
		return cmpDigits(x.text, y.text)
	case apkFirstNumber, apkSuffixNumber, apkRevision:
		return cmpDigits(trimZeros(x.text), trimZeros(y.text))
	case apkSuffix:
		return cmpInt(x.suffix, y.suffix)
	default: // apkLetter
		return strings.Compare(x.text, y.text)
	}
}

// apkVersionsDisagree reports whether apk-tools 2 orders a and b differently from CompareApk (apk-tools 3). That
// happens only where the first difference is two numbers after a "." that both start with "0": apk-tools 2 sorts the
// one with fewer leading zeros higher and then compares the rest as a number (8.2.0015 > 8.2.002, 1.0 > 1.00).
func apkVersionsDisagree(a, b Apk) bool {
	for i := 0; ; i++ {
		x, y := a.token(i), b.token(i)
		if x.kind != y.kind || x.kind == apkEnd {
			return false
		}
		c := apkTokenCompare(x, y)
		if c == 0 {
			continue
		}
		if x.kind != apkNumber || x.text[0] != '0' || y.text[0] != '0' {
			return false
		}
		zx := len(x.text) - len(strings.TrimLeft(x.text, "0"))
		zy := len(y.text) - len(strings.TrimLeft(y.text, "0"))
		apk2 := cmpInt(zy, zx)
		if apk2 == 0 {
			apk2 = cmpDigits(x.text[zx:], y.text[zy:])
		}
		return apk2 != c
	}
}
