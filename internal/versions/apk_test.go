package versions

import "testing"

func TestApkReference(t *testing.T) {
	// apk-tools' own cases (test/unit/version.data); none of them depends on the apk-tools release.
	checkPairs(t, Alpine, []pair{
		{"0.1.0_alpha", 0, "0.1.0_alpha"}, {"0.1.0_alpha2", 1, "0.1.0_alpha"}, {"1.0.4-r3", -1, "1.0.4-r4"},
		{"1.1", 1, "1.1_alpha1"}, {"1.3_alpha", -1, "1.3_alpha2"}, {"0.8_beta1", -1, "0.8_beta2"},
		{"0.8_beta4", -1, "4.8-r1"}, {"2.3.0b-r1", -1, "2.3.0b-r2"}, {"0.12.3", 1, "0.12"},
		{"0.13_beta5", 1, "0.9.12"}, {"0.0.16", -1, "0.6"}, {"2.1.15-r3", 1, "1.2.11"}, {"1.2.12.1", -1, "1.2.13"},
		{"0.2.0-r1", 0, "0.2.0-r1"}, {"3.1_p16", -1, "3.1_p17"}, {"1.06-r6", -1, "006"}, {"006", 1, "1.0.0"},
		{"1.2.2-r1", 1, "1.2.2"}, {"20050718-r1", -1, "20050718-r2"}, {"2.01.01_alpha10", 1, "0.94"},
		{"1.02", -1, "1.03-r1"}, {"4.2.52_p2-r1", -1, "4.2.52_p4-r2"}, {"1.02.07", -1, "1.02.10-r1"},
		{"2.0.2", 1, "1.1a"}, {"1.1a", -1, "1.3a"}, {"0.15.1b", -1, "1.0.1"}, {"0.15.1b-r2", 1, "0.15.1b"},
		{"3.9_pre20060124", 1, "0.01"}, {"0.01", -1, "0.06"}, {"2.9.11_pre20051101-r3", 1, "2.9.11_pre20051101"},
		{"1.0.0_pre20191002222144-r0", -1, "1.0.0_pre20210530193627-r0"}, {"0.1.0_alpha_pre2", -1, "0.1.0_alpha"},
		{"6.0_pre1", -1, "6.0"}, {"6.1_p1", 1, "6.1"}, {"1.0_p10-r0", 1, "1.0_p9-r0"}, {"1.3_pre1-r1", -1, "1.3.2"},
		{"0.20070118", -1, "0.20070207_rc1"}, {"0.10.0", -1, "0.10.1_rc1"}, {"4.20", 1, "0.20070118"},
		{"2.1.9.200602141850", 1, "1.6"},
	})
}

func TestApkOrder(t *testing.T) {
	// Suffixes in apk's order around the plain version, the revision, the letter and a further number: pre-release
	// suffixes before the plain version, the revision after it, then the other suffixes, the letter, more numbers.
	strictlyIncreasing(t, Alpine, []string{"1.0_alpha", "1.0_alpha1", "1.0_alpha2_p1", "1.0_alpha10", "1.0_beta",
		"1.0_beta2", "1.0_pre", "1.0_pre3", "1.0_rc", "1.0_rc1", "1.0_rc1-r1", "1.0", "1.0-r0", "1.0-r1", "1.0-r10",
		"1.0_cvs", "1.0_svn", "1.0_git", "1.0_git20240101", "1.0_hg", "1.0_p", "1.0_p1", "1.0_p1-r1", "1.0_p2", "1.0a",
		"1.0a-r1", "1.0b", "1.0.1"})
	checkPairs(t, Alpine, []pair{{"1.2.3-r0", -1, "1.2.3-r1"}, {"1.2_rc1", -1, "1.2"}, {"1.2", -1, "1.2_p1"},
		{"1.2", -1, "1.2a"}, {"1.0_alpha", -1, "1.0_beta"}, {"2.10", 1, "2.9"}, {"1.2", -1, "1.2.0"},
		{"1.2", -1, "1.2-r0"}, {"1.2_rc1-r5", -1, "1.2-r0"}, {"1.2_p1-r0", 1, "1.2-r9"}})
	// Numbers as numbers, the first one with leading zeros too; the revision and suffix numbers as well.
	strictlyIncreasing(t, Alpine, []string{"1.9", "1.10", "1.99", "1.100", "1.9999999999999999", "1.10000000000000000",
		"1.99999999999999999", "9", "10", "99999999999999999"})
	strictlyIncreasing(t, Alpine, []string{"1.0-r9", "1.0-r10", "1.0-r99999999999999999"})
	strictlyIncreasing(t, Alpine, []string{"1.0_p9", "1.0_p10", "1.0_p99999999999999999"})
	// Numbers after a "." that start with "0", where apk-tools 2 and 3 agree.
	strictlyIncreasing(t, Alpine, []string{"1.001", "1.01", "1.02", "1.1", "1.10"})
	strictlyIncreasing(t, Alpine, []string{"2024.01.05", "2024.01.10", "2024.02.01", "2024.10.01"})
	checkPairs(t, Alpine, []pair{{"1.0", -1, "1.01"}, {"1.0", -1, "1.1"}, {"1.00", -1, "1.01"},
		{"01.2", 0, "1.2"}, {"1.2-r0", 0, "1.2-r00"}, {"1.2_p01", 0, "1.2_p1"}, {" 1.2-r0 ", 0, "1.2-r0"},
		{"1.2.3a_p1-r2", 0, "1.2.3a_p1-r2"}})
}

func TestApkReleasesDisagree(t *testing.T) {
	// apk-tools 3 (Alpine v3.23 on) compares numbers after a "." that both start with "0" as strings; apk-tools 2
	// sorts the one with fewer leading zeros higher, then compares the rest as a number. Where that changes the order,
	// Compare gives none; CompareApk is apk-tools 3 (8.2.0015 < 8.2.002 is in its test/unit/version.data).
	for _, p := range []pair{{"8.2.0015", -1, "8.2.002"}, {"1.0", -1, "1.00"}, {"1.05", 1, "1.010"},
		{"1.0", -1, "1.001"}, {"1.2.0_p1", -1, "1.2.00"}} {
		x, okX := ParseApk(p.a)
		y, okY := ParseApk(p.b)
		if !okX || !okY {
			t.Errorf("%q, %q: parsed %v %v", p.a, p.b, okX, okY)
			continue
		}
		if c := CompareApk(x, y); c != p.c {
			t.Errorf("CompareApk(%q, %q): got %d, want %d", p.a, p.b, c, p.c)
		}
		if c := CompareApk(y, x); c != -p.c {
			t.Errorf("CompareApk(%q, %q): got %d, want %d", p.b, p.a, c, -p.c)
		}
		if _, ok := Compare(Alpine, p.a, p.b); ok {
			t.Errorf("%q, %q: ordered although apk-tools 2 and 3 disagree", p.a, p.b)
		}
		if _, ok := Compare(Alpine, p.b, p.a); ok {
			t.Errorf("%q, %q: ordered although apk-tools 2 and 3 disagree", p.b, p.a)
		}
	}
}

func TestApkInvalid(t *testing.T) {
	// The invalid versions of apk-tools' test/unit/version.data, then what only one apk-tools release accepts (the
	// commit hash, digits after the letter, 18 digits and more), then other garbage.
	for _, v := range []string{"", "0.1bc", "0.1bc1", "0.1a1", "0.1a.1", "0.1_pre2~", "0.1_pre2~1234xbcd",
		"0.1_pre2~1234abcd_pre1", "0.1_pre2-r1~1234xbcd", "0.1_foobar", "0.1_foobar1", "0.1-pre1.1", "0.1-r",
		"0.1-r2_pre1", "0.1-r2_p3_pre1", "0.1-r2-r3", "0.1-r2.1", ".1", "a", "_pre1", "-r1", "0.1_", "0.1_-r0",
		"0.1__alpha", "0.1_1_alpha",
		"0.1_pre2~1234abcd", "1.0~1234abcd", "1.2a3", "1.100000000000000000", "100000000000000000",
		"1.0_p100000000000000000", "1.0-r100000000000000000",
		" ", "1.0A", "1.0_RC1", "1.0_alpha.1", "1..0", "1.", "1.0.", "v1.0", "1.0 -r0", "1.0-r1a", "1.0-R1", "1.0+1",
		"1:1.0", "1.0-1", "1.ä", "1.0_p1a"} {
		if Valid(Alpine, v) {
			t.Errorf("%q valid", v)
		}
		if _, ok := Compare(Alpine, v, "1.0"); ok {
			t.Errorf("%q compared", v)
		}
	}
	for _, v := range []string{"0.1_pre2", "0.1_p1_pre2", "0.1_alpha1_pre2", "0.1_git20240101_pre1", "1.2", "006",
		"1.0_rc-r0", "1.2.3a_p1-r2", "1.0_p_p", "99999999999999999", "1.0-r0"} {
		if !Valid(Alpine, v) {
			t.Errorf("%q invalid", v)
		}
	}
}
