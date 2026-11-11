package apt

import (
	"context"
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/source"
)

// replacements sorts what the simulation removes (ADR-0146). A package is no loss when the run takes its place:
//
//   - declared: a package of the run declares Replaces plus Breaks or Conflicts on it (Debian policy 7.6.2, replacing
//     whole packages). That is how distributions rename or split packages.
//   - superseded: an old library the run leaves behind (see superseded), as when a library moves to a new soname.
//
// Refusing such a run would hold back every other update of the host. Anything else is a real removal, and the run is
// refused.
//
// replaced: "old -> new" per replaced package; real: the removals without such a successor. When apt-cache, apt-mark
// or dpkg-query cannot say, every removal counts as real.
func (a *Apt) replacements(ctx context.Context, plan []source.Package, removals []string) (replaced, real []string) {
	if len(removals) == 0 {
		return nil, nil
	}
	var names []string
	for _, p := range plan {
		if n := bareName(p.Name); !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	stanzas := map[string]map[string][]string{} // package -> field -> package names
	if len(names) > 0 {
		res := a.Run.Run(ctx, env, "apt-cache", append([]string{"show", "--no-all-versions"}, names...)...)
		if res.Ran() && res.ExitCode == 0 {
			stanzas = parseRelations(res.Stdout)
		}
	}
	var open []string // removals without a declared successor
	declared := map[string]string{}
	for _, r := range removals {
		r = bareName(r)
		for _, n := range names {
			rel := stanzas[n]
			if slices.Contains(rel["Replaces"], r) &&
				(slices.Contains(rel["Breaks"], r) || slices.Contains(rel["Conflicts"], r)) {
				declared[r] = n
				break
			}
		}
		if declared[r] == "" {
			open = append(open, r)
		}
	}
	left := a.superseded(ctx, open, names, stanzas)
	for _, r := range removals {
		r = bareName(r)
		by := declared[r]
		if by == "" {
			by = left[r]
		}
		if by == "" {
			real = append(real, r)
		} else {
			replaced = append(replaced, r+" -> "+by)
		}
	}
	return replaced, real
}

// superseded finds the old libraries a run leaves behind: when a library moves to a new soname (libexample1 ->
// libexample2), the packages built from the same source move with it, and apt removes the old library where it was tied
// to one of them. Such a removal is no loss when all three hold:
//
//   - nobody asked for it: the package was installed automatically, as a dependency (apt-mark showauto). A package a
//     person installed may serve software apt does not know of;
//   - its successor is in the run: a package of the run comes from the same source package (dpkg-query source:Package
//     of the installed one, Source of the new one);
//   - nothing installed needs it any more: every installed package that depends on it (Depends, Pre-Depends) is part
//     of the run, and its new version no longer depends on it.
//
// Returns removed package -> its successor; a removal that misses one of the three, or that a tool cannot answer for,
// is not in it.
func (a *Apt) superseded(ctx context.Context, removals, names []string, stanzas map[string]map[string][]string) map[string]string {
	out := map[string]string{}
	if len(removals) == 0 || len(names) == 0 {
		return out
	}
	auto := a.Run.Run(ctx, env, "apt-mark", append([]string{"showauto"}, removals...)...)
	srcs := a.Run.Run(ctx, env, "dpkg-query", append([]string{"-W", "-f", "${Package} ${source:Package}\\n"}, removals...)...)
	if !auto.Ran() || auto.ExitCode != 0 || !srcs.Ran() || srcs.ExitCode != 0 {
		return out
	}
	automatic := map[string]bool{}
	for _, line := range strings.Split(string(auto.Stdout), "\n") {
		if n := bareName(line); n != "" {
			automatic[n] = true
		}
	}
	source := map[string]string{} // installed removal -> its source package
	for _, line := range strings.Split(string(srcs.Stdout), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			source[bareName(f[0])] = f[1]
		}
	}
	for _, r := range removals {
		if !automatic[r] || source[r] == "" {
			continue
		}
		successor := ""
		for _, n := range names {
			if n != r && sourceOf(n, stanzas[n]) == source[r] {
				successor = n
				break
			}
		}
		if successor == "" {
			continue
		}
		rdep := a.Run.Run(ctx, env, "apt-cache", "rdepends", "--installed", "--no-suggests", "--no-recommends",
			"--no-enhances", "--no-conflicts", "--no-breaks", "--no-replaces", r)
		if !rdep.Ran() || rdep.ExitCode != 0 {
			continue
		}
		needed := false
		for _, d := range reverseDepends(rdep.Stdout) {
			rel, upgraded := stanzas[d]
			if d == r {
				continue
			}
			if !upgraded || !slices.Contains(names, d) ||
				slices.Contains(rel["Depends"], r) || slices.Contains(rel["Pre-Depends"], r) {
				needed = true
				break
			}
		}
		if !needed {
			out[r] = successor
		}
	}
	return out
}

// sourceOf: the source package a stanza names (Source: name [(version)]), else the package's own name.
func sourceOf(name string, rel map[string][]string) string {
	if s := rel["Source"]; len(s) > 0 {
		return s[0]
	}
	return name
}

// reverseDepends reads `apt-cache rdepends` output: the names after "Reverse Depends:", alternatives ("|") included.
func reverseDepends(out []byte) []string {
	var names []string
	listing := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "Reverse Depends:"):
			listing = true
		case listing && strings.TrimSpace(line) != "":
			if n := bareName(strings.TrimPrefix(strings.TrimSpace(line), "|")); !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names
}

// parseRelations reads apt-cache show stanzas: per package the names in Replaces, Breaks, Conflicts, Depends and
// Pre-Depends, and its Source (versions, architectures and alternatives dropped - a name is what the removal check
// compares).
func parseRelations(out []byte) map[string]map[string][]string {
	all := map[string]map[string][]string{}
	pkg, field := "", ""
	fields := map[string][]string{}
	flush := func() {
		if pkg != "" {
			all[pkg] = fields
		}
		pkg, field, fields = "", "", map[string][]string{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"): // continuation of the field before
			if field != "" {
				fields[field] = append(fields[field], relationNames(line)...)
			}
		default:
			key, value, _ := strings.Cut(line, ":")
			field = ""
			switch key {
			case "Package":
				pkg = bareName(value)
			case "Replaces", "Breaks", "Conflicts", "Depends", "Pre-Depends", "Source":
				field = key
				fields[key] = append(fields[key], relationNames(value)...)
			}
		}
	}
	flush()
	return all
}

// relationNames: "filemgr (<< 1.5), foo:any | bar" -> filemgr, foo, bar.
func relationNames(list string) []string {
	var out []string
	for _, item := range strings.Split(list, ",") {
		for _, alt := range strings.Split(item, "|") {
			if f := strings.Fields(strings.ReplaceAll(alt, "(", " (")); len(f) > 0 {
				out = append(out, bareName(f[0]))
			}
		}
	}
	return out
}

// bareName drops an architecture qualifier: "filemgr:arm64" -> "filemgr".
func bareName(n string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(n), ":")
	return name
}
