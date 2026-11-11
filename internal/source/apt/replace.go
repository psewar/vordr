package apt

import (
	"context"
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/source"
)

// replacements sorts what the simulation removes. A package is no loss when a package of the same run takes its
// place: it declares Replaces plus Breaks or Conflicts on it (Debian policy 7.6.2, replacing whole packages). That is
// how distributions rename or split packages; refusing such a run would hold back every other update of the host.
// Anything else is a real removal, and the run is refused.
//
// replaced: "old -> new" per replaced package; real: the removals without such a successor. When apt-cache cannot
// say, every removal counts as real.
func (a *Apt) replacements(ctx context.Context, plan []source.Package, removals []string) (replaced, real []string) {
	if len(removals) == 0 {
		return nil, nil
	}
	var names []string
	for _, p := range plan {
		if !slices.Contains(names, p.Name) {
			names = append(names, p.Name)
		}
	}
	stanzas := map[string]map[string][]string{} // package -> field -> package names
	if len(names) > 0 {
		res := a.Run.Run(ctx, env, "apt-cache", append([]string{"show", "--no-all-versions"}, names...)...)
		if res.Ran() && res.ExitCode == 0 {
			stanzas = parseRelations(res.Stdout)
		}
	}
	for _, r := range removals {
		r = bareName(r)
		by := ""
		for _, n := range names {
			rel := stanzas[bareName(n)]
			if slices.Contains(rel["Replaces"], r) &&
				(slices.Contains(rel["Breaks"], r) || slices.Contains(rel["Conflicts"], r)) {
				by = bareName(n)
				break
			}
		}
		if by == "" {
			real = append(real, r)
		} else {
			replaced = append(replaced, r+" -> "+by)
		}
	}
	return replaced, real
}

// parseRelations reads apt-cache show stanzas: per package the names in Replaces, Breaks and Conflicts (versions,
// architectures and alternatives dropped - a name is what the removal check compares).
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
			case "Replaces", "Breaks", "Conflicts":
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
