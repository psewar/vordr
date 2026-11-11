package apt

import (
	"strings"

	"github.com/psewar/vordr/internal/source"
)

// ParseSimulation reads the output of "apt-get -s full-upgrade". The lines it uses look like
//
//	Inst base-files [13ubuntu1] (13ubuntu1.1 Ubuntu:24.04/noble-updates [amd64])
//	Inst openssl [3.0.15-1~deb12u1] (3.0.17-1~deb12u2 Debian:12.12/stable, Debian-Security:12/stable-security [amd64]) []
//	Inst newdep (1.0-1 Debian:12.12/stable [amd64])
//	Remv oldpkg [2.0-1]
//
// An "Inst" line it cannot take apart still counts as a pending package (with its name only), so nothing pending
// gets lost because of an unexpected format.
func ParseSimulation(out []byte) (pkgs []source.Package, removals []string) {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "Inst "):
			pkgs = append(pkgs, parseInst(strings.TrimPrefix(line, "Inst ")))
		case strings.HasPrefix(line, "Remv "):
			if f := strings.Fields(strings.TrimPrefix(line, "Remv ")); len(f) > 0 {
				removals = append(removals, f[0])
			}
		}
	}
	return pkgs, removals
}

func parseInst(rest string) source.Package {
	name, rest, _ := strings.Cut(strings.TrimSpace(rest), " ")
	p := source.Package{Name: name}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "[") {
		if end := strings.IndexByte(rest, ']'); end > 0 {
			p.From = rest[1:end]
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	if !strings.HasPrefix(rest, "(") {
		return p
	}
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return p
	}
	candidate := rest[1:end] // "<version> <origin>[, <origin> ...] [<arch>]"
	ver, origins, _ := strings.Cut(candidate, " ")
	p.To = ver
	if i := strings.LastIndex(origins, " ["); i >= 0 && strings.HasSuffix(origins, "]") {
		origins = origins[:i]
	}
	for _, o := range strings.Split(origins, ", ") {
		if o = strings.TrimSpace(o); o != "" {
			p.Origins = append(p.Origins, o)
			if securityOrigin(o) {
				p.Security = true
			}
		}
	}
	return p
}

// splitOrigin takes apart one origin as apt prints it (pkgCache::VerIterator::RelStr): "<label>:<version>/<archive>",
// or "<label>:<archive>" when the archive's Release file has no Version (Raspbian, a vendor's repository), or
// "<label>:<site>" without an archive.
func splitOrigin(o string) (label, version, suite string) {
	label, rest, _ := strings.Cut(o, ":")
	if v, s, ok := strings.Cut(rest, "/"); ok {
		return label, v, s
	}
	return label, "", rest
}

// securityOrigin: from a security archive, e.g. Debian-Security:12/stable-security or Ubuntu:24.04/noble-security.
func securityOrigin(o string) bool {
	label, _, suite := splitOrigin(o)
	return strings.HasSuffix(suite, "-security") || strings.HasSuffix(label, "-Security")
}

// distributionLabels: the archives of the distributions themselves. A vendor's repository (Proxmox, Docker, the
// Raspberry Pi archive) names itself differently, even when its suite is "stable".
var distributionLabels = map[string]bool{"Debian": true, "Ubuntu": true}

// releaseOrigin: the host's own release (VERSION_ID), e.g. Debian:12.15/oldstable on Debian 12 (point releases
// included) or Ubuntu:24.04/noble on 24.04. Not its pockets -updates, -backports or -proposed, nor
// proposed-updates (their suites carry a dash), and not another release - unattended-upgrades matches the codename.
// An archive without a Version (Raspbian, Debian testing) cannot be matched, and none is taken without a release.
func releaseOrigin(o, release string) bool {
	label, version, suite := splitOrigin(o)
	return release != "" && distributionLabels[label] && suite != "" && !strings.Contains(suite, "-") &&
		(version == release || strings.HasPrefix(version, release+"."))
}

// SecurityRunAllows: a package a security run may change - its candidate comes from a security archive or from the
// host's release (ADR-0116), as unattended-upgrades allows by default. release is the host's VERSION_ID. An "Inst"
// line without origins is not.
func SecurityRunAllows(p source.Package, release string) bool {
	for _, o := range p.Origins {
		if securityOrigin(o) || releaseOrigin(o, release) {
			return true
		}
	}
	return false
}
