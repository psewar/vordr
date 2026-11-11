package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/versions"
)

// Updating pip/npm packages by button (design 11.4). Supply-chain attacks come mostly as malicious new releases,
// of the package itself or of one of its dependencies. So an update:
//
//  1. takes for each package only its candidate (11.3: public for min_age_days), asked anew from the registry;
//  2. lets pip or npm resolve the whole plan without installing (pip --dry-run --report, npm --package-lock-only),
//     npm with --before <now - min_age> so that no dependency younger than that is chosen either;
//  3. checks every planned version's age itself (pip; npm's --before does it there);
//  4. has the hub vouch for the plan against OSV (signed, bound to the job) - one flagged entry stops everything;
//  5. installs exactly the vetted plan: pip with --no-deps and the plan's versions pinned, npm with the same --before;
//     without install scripts (npm) and without building source distributions (pip), unless agent.yaml allows it
//     for the location;
//  6. reads the location again: every package at its candidate, and pip check passes.
//
// Nothing is installed before step 5; a refusal leaves the location as it was.

// Vetter asks the hub about a plan; ok is false when no valid answer came.
type Vetter func(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool)

// Updater updates the packages of one location.
type Updater struct {
	Exec    CommandRunner
	Reg     Registries
	Now     func() time.Time
	MinAge  time.Duration
	Scripts bool // npm: allow install scripts
	Sdist   bool // pip: allow source distributions
	Vet     Vetter
	TempDir string // for the plan; default os.TempDir()
	// SecurityAge and SecurityFix (ADR-0111): the packages the hub named in the job as fixes of an OSV advisory take
	// their target at SecurityAge (min_age_days_security); every other entry of the plan keeps MinAge.
	SecurityAge time.Duration
	SecurityFix []string
}

// shortAges: for each package the hub named as a fix, by normalized name, the versions that may be younger than
// MinAge: an update's candidate, or in a dependency refresh the releases that replace an installed copy with a finding.
// Empty when the job names none or the agent has no shorter age.
type shortAges map[string][]string

// fixNamed: the hub named name as a fix, and the agent's policy has a shorter age for it.
func (u *Updater) fixNamed(eco versions.Ecosystem, name string) bool {
	if u.SecurityAge <= 0 || u.SecurityAge >= u.MinAge {
		return false
	}
	return slices.ContainsFunc(u.SecurityFix, func(n string) bool {
		return versions.NormalizeName(eco, n) == versions.NormalizeName(eco, name)
	})
}

// cutoff: the newest publish time an entry may have - MinAge, or SecurityAge for a fix in short.
func (u *Updater) cutoff(eco versions.Ecosystem, e protocol.VetPackage, short shortAges, now time.Time) time.Time {
	if slices.Contains(short[versions.NormalizeName(eco, e.Name)], e.Version) {
		return now.Add(-u.SecurityAge)
	}
	return now.Add(-u.MinAge)
}

// checkAges asks the registry about every entry of a plan: each must be public, not yanked, and older than its
// cutoff. pip resolves to the newest matching release, however young, so it is always checked here; npm only when a
// fix let its --before move to the shorter age, so that the fix brings no young dependency along (ADR-0111).
func (u *Updater) checkAges(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage,
	short shortAges) UpdateResult {
	now := u.Now()
	var young []string
	for _, e := range plan {
		info, err := u.Reg.Lookup(ctx, eco, e.Name)
		if err != nil {
			return refused(protocol.CodePrecheckRepoUnreachable, "%s: %v", e.Name, err)
		}
		i := slices.IndexFunc(info.Releases, func(r Release) bool {
			c, ok := versions.Compare(eco, r.Version, e.Version)
			return ok && c == 0
		})
		if i < 0 || info.Releases[i].Withdrawn || info.Releases[i].Published.After(u.cutoff(eco, e, short, now)) {
			young = append(young, e.Name+" "+e.Version)
		}
	}
	if len(young) > 0 {
		return refused(protocol.CodePrecheckVersionTooNew, "the plan needs releases younger than the minimum age "+
			"(or yanked): %s", strings.Join(young, ", "))
	}
	return UpdateResult{}
}

// UpdateResult is the outcome; Changed lists "name old -> new" of what the run installed.
type UpdateResult struct {
	Code    protocol.Code
	Detail  string
	Changed []string
	Log     []byte
	Plan    []protocol.VetPackage `json:",omitempty"` // Plan only: everything the update would install
}

func refused(code protocol.Code, format string, a ...any) UpdateResult {
	return UpdateResult{Code: code, Detail: fmt.Sprintf(format, a...)}
}

// target is a package to update, with the version the update installs.
type target struct {
	name, from, to string
}

// Update updates the direct packages of loc that have a candidate, or only name. before runs once the plan is
// vetted, before anything changes (the run journal); an error stops the run there. before nil: Plan.
func (u *Updater) Update(ctx context.Context, loc Location, name string, before func() error) UpdateResult {
	if !Updatable(loc.Kind) {
		return refused(protocol.CodeRejectedUnsupported, "%s locations are not updated by Vördr", loc.Kind)
	}
	var direct []Package
	for _, p := range loc.Packages {
		if p.Direct && (name == "" || versions.NormalizeName(loc.Ecosystem, p.Name) == versions.NormalizeName(loc.Ecosystem, name)) {
			direct = append(direct, p)
		}
	}
	if name != "" && len(direct) == 0 {
		return refused(protocol.CodeRejectedScopeNotAllowed, "%s is not installed for its own sake in %s", name, loc.ID)
	}
	now := u.Now()
	var targets []target
	var waiting []string
	short := shortAges{}
	for _, p := range direct {
		info, err := u.Reg.Lookup(ctx, loc.Ecosystem, p.Name)
		if err != nil {
			return refused(protocol.CodePrecheckRepoUnreachable, "%s: %v", p.Name, err)
		}
		age := u.MinAge
		if u.fixNamed(loc.Ecosystem, p.Name) {
			age = u.SecurityAge // a fix of an advisory, named by the hub (ADR-0111)
		}
		latest, candidate, latestAt := Pick(loc.Ecosystem, p.Version, info, now, age)
		if candidate != "" && age != u.MinAge {
			if _, normal, _ := Pick(loc.Ecosystem, p.Version, info, now, u.MinAge); normal != candidate {
				short[versions.NormalizeName(loc.Ecosystem, p.Name)] = []string{candidate}
			}
		}
		switch {
		case candidate != "":
			targets = append(targets, target{p.Name, p.Version, candidate})
		case latest != "":
			waiting = append(waiting, fmt.Sprintf("%s %s (public since %s)", p.Name, latest, latestAt.Format("2006-01-02")))
		}
	}
	if len(targets) == 0 {
		if len(waiting) > 0 {
			return refused(protocol.CodePrecheckVersionTooNew, "younger than the minimum age: %s", strings.Join(waiting, ", "))
		}
		return UpdateResult{Code: protocol.CodeOK, Detail: "nothing to update"}
	}
	if loc.Ecosystem == versions.Npm && len(short) > 0 && len(short) < len(targets) {
		// npm's --before holds for the whole resolution: at the fixes' shorter age, the other packages' new
		// dependencies would resolve as young as the fixes and then fail the minimum age, and the whole job with them.
		// So the fixes go first, on their own, at the shorter age; the rest follows at the minimum age, planned from
		// what the first part left (review of ADR-0111). pip resolves without a cutoff and checks every entry anyway.
		var fixes, rest []target
		for _, t := range targets {
			if _, ok := short[versions.NormalizeName(loc.Ecosystem, t.name)]; ok {
				fixes = append(fixes, t)
			} else {
				rest = append(rest, t)
			}
		}
		once := onlyOnce(before)
		return twoParts(u.updateTargets(ctx, loc, fixes, short, once, now), func() UpdateResult {
			return u.updateTargets(ctx, read(loc.ID, loc.Kind, loc.Path), rest, shortAges{}, once, u.Now())
		})
	}
	return u.updateTargets(ctx, loc, targets, short, before, now)
}

// onlyOnce: before for a run made of two parts; it runs before the first change only (the run journal). nil stays nil.
func onlyOnce(before func() error) func() error {
	if before == nil {
		return nil
	}
	done := false
	return func() error {
		if done {
			return nil
		}
		done = true
		return before()
	}
}

// twoParts runs second after first went well and reports both as one run: what both changed and planned. When the
// second part fails, its code is the run's, and its detail says that the first part was installed.
func twoParts(first UpdateResult, second func() UpdateResult) UpdateResult {
	if first.Code != protocol.CodeOK {
		return first
	}
	res := second()
	res.Changed = append(append([]string{}, first.Changed...), res.Changed...)
	res.Plan = append(append([]protocol.VetPackage{}, first.Plan...), res.Plan...)
	res.Log = append(append([]byte{}, first.Log...), res.Log...)
	switch {
	case res.Code != protocol.CodeOK && len(first.Changed) > 0:
		res.Detail = "the fixes went in first (" + strings.Join(first.Changed, ", ") + "), then: " + res.Detail
	case first.Detail != "" && res.Detail != "":
		res.Detail = first.Detail + "; " + res.Detail
	case first.Detail != "":
		res.Detail = first.Detail
	}
	return res
}

// updateTargets plans, vets, installs and verifies targets in loc; short as for checkAges.
func (u *Updater) updateTargets(ctx context.Context, loc Location, targets []target, short shortAges,
	before func() error, now time.Time) UpdateResult {
	tmp, err := os.MkdirTemp(u.TempDir, "vordr-plan-")
	if err != nil {
		return refused(protocol.CodePrecheckDiskSpace, "%v", err)
	}
	defer os.RemoveAll(tmp)
	if err := handTo(tmp, loc.Path); err != nil { // pip/npm run as the location's owner and write their plan here
		return refused(protocol.CodeFailedPackageManager, "temporary folder: %v", err)
	}

	var p planner
	if loc.Ecosystem == versions.PyPI {
		p = &pipPlanner{u: u, loc: loc, tmp: tmp, short: short}
	} else {
		p = newNpmPlanner(u, loc, tmp, short, now)
	}
	plan, res := p.plan(ctx, targets)
	if res.Code != "" {
		return res
	}
	if before == nil { // Plan: show what would be installed, change nothing
		return UpdateResult{Code: protocol.CodeOK, Changed: changes(targets), Plan: plan}
	}
	if res := u.vet(ctx, loc.Ecosystem, plan); res.Code != "" {
		return res
	}
	if err := before(); err != nil {
		return refused(protocol.CodeFailedPackageManager, "run journal: %v", err)
	}
	log, res := p.install(ctx, targets, plan)
	res.Log = log
	if res.Code != "" {
		return res
	}
	return u.verify(ctx, p, loc, targets, log)
}

// Plan goes as far as Update before vetting: candidates from the registry and the package manager's whole plan
// (dependencies included, pip's ages checked), without asking the hub and without changing anything. For the
// operator: "vordr-agent packages --plan <location>[/<name>]".
func (u *Updater) Plan(ctx context.Context, loc Location, name string) UpdateResult {
	return u.Update(ctx, loc, name, nil)
}

func changes(targets []target) []string {
	var out []string
	for _, t := range targets {
		out = append(out, t.name+" "+t.from+" -> "+t.to)
	}
	return out
}

// vet has the hub vouch for the plan, entry by entry.
func (u *Updater) vet(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage) UpdateResult {
	v, res := u.vetAnswer(ctx, eco, plan)
	if res.Code != "" {
		return res
	}
	if flagged := flaggedIn(v); len(flagged) > 0 {
		return refused(protocol.CodePrecheckVersionFlagged, "%s", strings.Join(flagged, "; "))
	}
	return UpdateResult{}
}

// vetAnswer asks the hub about entries and takes only a valid answer that covers each of them.
func (u *Updater) vetAnswer(ctx context.Context, eco versions.Ecosystem, entries []protocol.VetPackage) (protocol.Vet,
	UpdateResult) {
	if u.Vet == nil {
		return protocol.Vet{}, refused(protocol.CodePrecheckVettingUnavailable, "no hub to ask about the packages")
	}
	v, ok := u.Vet(ctx, eco, entries)
	switch {
	case !ok:
		return v, refused(protocol.CodePrecheckVettingUnavailable, "the hub gave no valid answer about the plan")
	case v.Unavailable != "":
		return v, refused(protocol.CodePrecheckVettingUnavailable, "the hub's OSV data for %s: %s", eco, v.Unavailable)
	case !v.Covers(entries):
		return v, refused(protocol.CodePrecheckVettingUnavailable, "the hub's answer does not cover the plan")
	}
	return v, UpdateResult{}
}

// flaggedIn: the entries of a vet answer that OSV knows, as "name version: advisories".
func flaggedIn(v protocol.Vet) []string {
	var flagged []string
	for _, p := range v.Packages {
		if len(p.Flagged) > 0 {
			flagged = append(flagged, fmt.Sprintf("%s %s: %s", p.Name, p.Version, strings.Join(p.Flagged, " ")))
		}
	}
	return flagged
}

// verify reads the location again: every target at its version, and the planner's own check.
func (u *Updater) verify(ctx context.Context, p planner, loc Location, targets []target, log []byte) UpdateResult {
	after := read(loc.ID, loc.Kind, loc.Path)
	have := map[string]string{}
	for _, pkg := range after.Packages {
		have[versions.NormalizeName(loc.Ecosystem, pkg.Name)+"\x00"+pkg.Version] = pkg.Version
	}
	var missing, changed []string
	for _, t := range targets {
		if _, ok := have[versions.NormalizeName(loc.Ecosystem, t.name)+"\x00"+t.to]; !ok {
			missing = append(missing, t.name+" "+t.to)
		}
		changed = append(changed, t.name+" "+t.from+" -> "+t.to)
	}
	res := UpdateResult{Code: protocol.CodeOK, Changed: changed, Log: log}
	if len(missing) > 0 {
		res.Code, res.Detail = protocol.CodeFailedVerification, "not installed afterwards: "+strings.Join(missing, ", ")
		return res
	}
	if detail, ok := p.check(ctx); !ok {
		res.Code, res.Detail = protocol.CodeFailedVerification, detail
	}
	return res
}

type planner interface {
	plan(ctx context.Context, targets []target) ([]protocol.VetPackage, UpdateResult)
	install(ctx context.Context, targets []target, plan []protocol.VetPackage) ([]byte, UpdateResult)
	check(ctx context.Context) (string, bool)
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 800 {
		s = "…" + s[len(s)-800:]
	}
	return s
}

// --- pip (virtual environments) -----------------------------------------------------------------------------------

type pipPlanner struct {
	u     *Updater
	loc   Location
	tmp   string
	short shortAges // fixes the hub named (ADR-0111)
}

func (p *pipPlanner) python() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(p.loc.Path, "Scripts", "python.exe")
	}
	return filepath.Join(p.loc.Path, "bin", "python")
}

func (p *pipPlanner) run(ctx context.Context, args ...string) runner.Result {
	base := []string{"-m", "pip"}
	return p.u.Exec.Run(ctx, Command{Dir: p.loc.Path, Owner: p.loc.Path, Name: p.python(), Args: append(base, args...),
		Env: []string{"PIP_NO_INPUT=1", "PIP_DISABLE_PIP_VERSION_CHECK=1"}})
}

// common: never ask, wheels only unless allowed, the configured index.
func (p *pipPlanner) common() []string {
	args := []string{"--no-input", "--index-url", strings.TrimSuffix(p.u.Reg.PyPI, "/") + "/simple"}
	if !p.u.Sdist {
		args = append(args, "--only-binary=:all:")
	}
	return args
}

func (p *pipPlanner) plan(ctx context.Context, targets []target) ([]protocol.VetPackage, UpdateResult) {
	if _, err := os.Stat(p.python()); err != nil {
		return nil, refused(protocol.CodeRejectedUnsupported, "no interpreter in %s", p.loc.Path)
	}
	report := filepath.Join(p.tmp, "report.json")
	args := append([]string{"install", "--dry-run", "--report", report, "--quiet", "--upgrade-strategy", "only-if-needed"},
		p.common()...)
	for _, t := range targets {
		args = append(args, t.name+"=="+t.to)
	}
	r := p.run(ctx, args...)
	if !r.Ran() || r.ExitCode != 0 {
		return nil, refused(protocol.CodeFailedPackageManager, "pip could not resolve the update (nothing changed): %s %v",
			tail(r.Stderr), r.Err)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		return nil, refused(protocol.CodeFailedPackageManager, "pip wrote no report: %v", err)
	}
	var rep struct {
		Install []struct {
			Metadata struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"metadata"`
		} `json:"install"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, refused(protocol.CodeFailedPackageManager, "pip report unreadable: %v", err)
	}
	var plan []protocol.VetPackage
	for _, i := range rep.Install {
		plan = append(plan, protocol.VetPackage{Name: i.Metadata.Name, Version: i.Metadata.Version})
	}
	for _, e := range plan {
		if !protocol.PackageNamePattern.MatchString(e.Name) || !versions.Valid(versions.PyPI, e.Version) {
			return nil, refused(protocol.CodeFailedPackageManager, "pip planned an entry that is no package: %q %q", e.Name, e.Version)
		}
	}
	// pip resolves to the newest matching release, however young; the minimum age is checked here, entry by entry.
	if res := p.u.checkAges(ctx, versions.PyPI, plan, p.short); res.Code != "" {
		return nil, res
	}
	return plan, UpdateResult{}
}

func (p *pipPlanner) install(ctx context.Context, _ []target, plan []protocol.VetPackage) ([]byte, UpdateResult) {
	args := append([]string{"install", "--no-deps"}, p.common()...) // exactly the vetted plan, nothing resolved anew
	for _, e := range plan {
		args = append(args, e.Name+"=="+e.Version)
	}
	r := p.run(ctx, args...)
	log := append(r.Stdout, r.Stderr...)
	if !r.Ran() || r.ExitCode != 0 {
		return log, refused(protocol.CodeFailedPackageManager, "pip exit %d: %s %v", r.ExitCode, tail(r.Stderr), r.Err)
	}
	return log, UpdateResult{}
}

func (p *pipPlanner) check(ctx context.Context) (string, bool) {
	r := p.run(ctx, "check")
	if !r.Ran() || r.ExitCode != 0 {
		return "pip check: " + tail(append(r.Stdout, r.Stderr...)), false
	}
	return "", true
}

// --- npm (global, Node-RED, projects) -----------------------------------------------------------------------------

type npmPlanner struct {
	u      *Updater
	loc    Location
	tmp    string
	cutoff time.Time
	young  bool      // cutoff is the shorter age: npm may have chosen releases younger than the minimum age
	short  shortAges // fixes the hub named (ADR-0111)
	// flagged: in a refresh of fixes, the installed copies with findings; only releases that take their place may be
	// as young as the shorter age (replacing).
	flagged map[[2]string]bool
}

// newNpmPlanner: npm resolves with --before the minimum age - or the shorter one when a fix is in the plan; then
// checkAges holds everything else in the plan to the minimum age (ADR-0111).
func newNpmPlanner(u *Updater, loc Location, tmp string, short shortAges, now time.Time) *npmPlanner {
	n := &npmPlanner{u: u, loc: loc, tmp: tmp, cutoff: now.Add(-u.MinAge), short: short}
	if len(short) > 0 {
		n.cutoff, n.young = now.Add(-u.SecurityAge), true
	}
	return n
}

// newDepsPlanner: a refresh of dependencies; fixes: all of them are fixes the hub named, and npm resolves at the
// shorter age. Which releases may be that young follows from the plan (replacing).
func newDepsPlanner(u *Updater, loc Location, tmp string, fixes bool, flagged map[[2]string]bool,
	now time.Time) *npmPlanner {
	n := &npmPlanner{u: u, loc: loc, tmp: tmp, cutoff: now.Add(-u.MinAge)}
	if fixes {
		n.cutoff, n.young, n.flagged = now.Add(-u.SecurityAge), true, flagged
	}
	return n
}

// checkPlanAges: when --before was the shorter age, each entry of the plan but those in short must still be as old as
// the minimum age.
func (n *npmPlanner) checkPlanAges(ctx context.Context, plan []protocol.VetPackage, short shortAges) UpdateResult {
	if !n.young {
		return UpdateResult{} // --before was the minimum age: npm chose nothing younger
	}
	return n.u.checkAges(ctx, versions.Npm, plan, short)
}

// replacing: in a refresh of fixes, the releases that may be as young as the shorter age - those npm put in the place
// (the same path in the lock) of an installed copy with a finding, for a dependency the hub named. "npm update <name>"
// moves every copy of the name: a copy without a finding, or one npm moves elsewhere, keeps the minimum age (review of
// ADR-0111).
func (n *npmPlanner) replacing(before, after map[string]protocol.VetPackage) shortAges {
	short := shortAges{}
	for path, e := range after {
		b, ok := before[path]
		if !ok || b.Name != e.Name || b.Version == e.Version || !n.flagged[[2]string{b.Name, b.Version}] ||
			!n.u.fixNamed(versions.Npm, e.Name) {
			continue
		}
		k := versions.NormalizeName(versions.Npm, e.Name)
		if !slices.Contains(short[k], e.Version) {
			short[k] = append(short[k], e.Version)
		}
	}
	return short
}

// npm returns how to start npm: under Windows node.exe with npm-cli.js (npm.cmd is a batch file), elsewhere npm.
func npmCommand() (string, []string, bool) {
	if runtime.GOOS != "windows" {
		p, err := exec.LookPath("npm")
		return p, nil, err == nil
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return "", nil, false
	}
	cli := filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
	_, err = os.Stat(cli)
	return node, []string{cli}, err == nil
}

// NpmCommand can be replaced in tests.
var NpmCommand = npmCommand

// DefaultNpmCommand is how the agent starts npm; tests that replace NpmCommand put it back with it.
var DefaultNpmCommand = npmCommand

// run starts npm with what every call of an update has in common. --allow-remote=all (ADR-0128): npm 12 takes some
// registry tarballs for remote ones (a bundled npm in a global package) and would refuse the plan; what the option
// gives up, checkOrigins checks for every entry of the plan. npm before 12 does not know the option and ignores it.
func (n *npmPlanner) run(ctx context.Context, dir string, args ...string) runner.Result {
	name, pre, _ := NpmCommand()
	common := []string{"--no-audit", "--no-fund", "--allow-remote=all", "--registry", n.u.Reg.Npm,
		"--before", n.cutoff.UTC().Format(time.RFC3339)}
	return n.u.Exec.Run(ctx, Command{Dir: dir, Owner: n.loc.Path, Name: name,
		Args: append(append(pre, args...), common...), Env: []string{"npm_config_update_notifier=false"}})
}

func specs(targets []target) []string {
	var out []string
	for _, t := range targets {
		out = append(out, t.name+"@"+t.to)
	}
	return out
}

type lockFile struct {
	Packages map[string]struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Link     bool   `json:"link"`
		Resolved string `json:"resolved"` // where npm fetches it from; none for an entry bundled in its parent
	} `json:"packages"`
}

// fromElsewhere: the entries of the lock file at path that are in plan (by name and version) and that npm resolved
// from anywhere but registry - another host, git, a file (ADR-0128). An entry without "resolved" came bundled in its
// parent's tarball, which the registry served. ok is false when the lock file is unreadable.
func fromElsewhere(path, registry string, plan []protocol.VetPackage) ([]string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var lf lockFile
	if json.Unmarshal(raw, &lf) != nil {
		return nil, false
	}
	planned := map[[2]string]bool{}
	for _, e := range plan {
		planned[[2]string{e.Name, e.Version}] = true
	}
	var out []string
	for p, e := range lf.Packages {
		i := strings.LastIndex(p, "node_modules/")
		if p == "" || e.Link || i < 0 {
			continue
		}
		name := e.Name
		if name == "" {
			name = p[i+len("node_modules/"):]
		}
		if !planned[[2]string{name, e.Version}] || e.Resolved == "" || onRegistry(e.Resolved, registry) {
			continue
		}
		if s := name + " " + e.Version + " from " + e.Resolved; !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, true
}

// onRegistry: resolved is a URL on the registry - the same scheme and host, and a path below the registry's.
func onRegistry(resolved, registry string) bool {
	r, err := url.Parse(resolved)
	if err != nil {
		return false // not even a URL: not the registry's
	}
	g, err := url.Parse(registry)
	if err != nil {
		return false
	}
	if r.Scheme != g.Scheme || !strings.EqualFold(r.Host, g.Host) || r.User != nil {
		return false
	}
	base := strings.TrimSuffix(g.Path, "/") + "/"
	return strings.HasPrefix(path.Clean(r.Path), base)
}

// checkOrigins ends the run when an entry of the plan does not come from the registry (ADR-0128): before the hub is
// asked and before anything is installed.
func (n *npmPlanner) checkOrigins(plan []protocol.VetPackage) UpdateResult {
	bad, ok := fromElsewhere(filepath.Join(n.tmp, "package-lock.json"), n.u.Reg.Npm, plan)
	if !ok {
		return refused(protocol.CodeFailedPackageManager, "npm wrote no readable lock file")
	}
	if len(bad) > 0 {
		return refused(protocol.CodePrecheckNotFromRegistry, "not from the registry %s: %s", n.u.Reg.Npm,
			strings.Join(bad, ", "))
	}
	return UpdateResult{}
}

// entries: name and version of every installed package a lock file lists, by path.
func entries(path string) (map[string]protocol.VetPackage, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var lf lockFile
	if json.Unmarshal(raw, &lf) != nil {
		return nil, false
	}
	out := map[string]protocol.VetPackage{}
	for p, e := range lf.Packages {
		i := strings.LastIndex(p, "node_modules/")
		if p == "" || e.Link || i < 0 {
			continue
		}
		name := e.Name
		if name == "" {
			name = p[i+len("node_modules/"):]
		}
		out[p] = protocol.VetPackage{Name: name, Version: e.Version}
	}
	return out, true
}

func (n *npmPlanner) plan(ctx context.Context, targets []target) ([]protocol.VetPackage, UpdateResult) {
	if _, _, ok := NpmCommand(); !ok {
		return nil, refused(protocol.CodeRejectedUnsupported, "npm not found")
	}
	var before map[string]protocol.VetPackage
	args := []string{"install", "--package-lock-only", "--ignore-scripts", "--json"}
	if n.loc.Kind == KindNpmGlobal {
		deps := map[string]string{}
		for _, t := range targets {
			deps[t.name] = t.to
		}
		raw, _ := json.Marshal(map[string]any{"name": "vordr-plan", "version": "0.0.0", "private": true, "dependencies": deps})
		if err := os.WriteFile(filepath.Join(n.tmp, "package.json"), raw, 0o644); err != nil {
			return nil, refused(protocol.CodePrecheckDiskSpace, "%v", err)
		}
		// No lock to compare with: what the location has installed is the "before" (ADR-0105). npm resolves the whole
		// tree anew, and a version already there, under this package or another, brings no new code.
		before = installedEntries(n.loc)
	} else {
		for _, f := range []string{"package.json", "package-lock.json"} {
			raw, err := os.ReadFile(filepath.Join(n.loc.Path, f))
			if err != nil {
				continue // a folder without a lock file: then everything resolved counts as planned
			}
			if err := os.WriteFile(filepath.Join(n.tmp, f), raw, 0o644); err != nil {
				return nil, refused(protocol.CodePrecheckDiskSpace, "%v", err)
			}
		}
		before, _ = entries(filepath.Join(n.tmp, "package-lock.json"))
		args = append(args, specs(targets)...)
	}
	after, res := n.resolve(ctx, args)
	if res.Code != "" {
		return nil, res
	}
	plan, res := newEntries(before, after)
	if res.Code != "" {
		return nil, res
	}
	if res := n.checkOrigins(plan); res.Code != "" {
		return nil, res
	}
	if res := n.checkPlanAges(ctx, plan, n.short); res.Code != "" {
		return nil, res
	}
	return plan, UpdateResult{}
}

// installedEntries: every package a location reports, by name and version, as lock entries would list them.
func installedEntries(loc Location) map[string]protocol.VetPackage {
	out := map[string]protocol.VetPackage{}
	for _, p := range loc.Packages {
		out[p.Name+"@"+p.Version] = protocol.VetPackage{Name: p.Name, Version: p.Version}
	}
	return out
}

// resolve has npm write the plan's lock file into the temporary folder, without installing anything.
func (n *npmPlanner) resolve(ctx context.Context, args []string) (map[string]protocol.VetPackage, UpdateResult) {
	r := n.run(ctx, n.tmp, append(args, "--prefix", n.tmp)...)
	if !r.Ran() || r.ExitCode != 0 {
		var out struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(r.Stdout, &out) // npm --json reports its error as data; no data: the general case below
		if out.Error.Code == "ETARGET" {   // no release satisfies a range before --before: a dependency too new
			return nil, refused(protocol.CodePrecheckVersionTooNew, "npm finds no release old enough for the plan: %s",
				tail(r.Stdout))
		}
		return nil, refused(protocol.CodeFailedPackageManager, "npm could not resolve the update (nothing changed): %s %v",
			tail(append(r.Stdout, r.Stderr...)), r.Err)
	}
	after, ok := entries(filepath.Join(n.tmp, "package-lock.json"))
	if !ok {
		return nil, refused(protocol.CodeFailedPackageManager, "npm wrote no lock file")
	}
	return after, UpdateResult{}
}

// newEntries: what the lock after an update has that the one before had not.
// The plan is what the update brings that is not installed already, by name and version - not by path: npm moves
// packages around in the tree, and a version that only moved is no new code (counted by path, an old version that
// moved would be flagged by the hub, and a good update refused).
func newEntries(before, after map[string]protocol.VetPackage) ([]protocol.VetPackage, UpdateResult) {
	installed := map[[2]string]bool{}
	for _, e := range before {
		installed[[2]string{e.Name, e.Version}] = true
	}
	seen := map[[2]string]bool{}
	var plan []protocol.VetPackage
	for _, e := range after {
		if installed[[2]string{e.Name, e.Version}] {
			continue // already there before the update
		}
		if !protocol.PackageNamePattern.MatchString(e.Name) || !versions.Valid(versions.Npm, e.Version) {
			return nil, refused(protocol.CodeFailedPackageManager, "npm planned an entry that is no package: %q %q", e.Name, e.Version)
		}
		if k := [2]string{e.Name, e.Version}; !seen[k] {
			seen[k] = true
			plan = append(plan, e)
		}
	}
	slices.SortFunc(plan, func(a, b protocol.VetPackage) int { return strings.Compare(a.Name+"@"+a.Version, b.Name+"@"+b.Version) })
	return plan, UpdateResult{}
}

// globalPrefix: npm's --prefix for a global node_modules (Linux <prefix>/lib/node_modules, Windows <prefix>\node_modules).
func (n *npmPlanner) globalPrefix() string {
	dir := filepath.Dir(n.loc.Path)
	if filepath.Base(dir) == "lib" {
		return filepath.Dir(dir)
	}
	return dir
}

func (n *npmPlanner) install(ctx context.Context, targets []target, _ []protocol.VetPackage) ([]byte, UpdateResult) {
	args := []string{"install"}
	dir := n.loc.Path
	if n.loc.Kind == KindNpmGlobal {
		args, dir = append(args, "--global", "--prefix", n.globalPrefix()), n.globalPrefix()
	} else {
		args = append(args, "--prefix", n.loc.Path)
	}
	if !n.u.Scripts {
		args = append(args, "--ignore-scripts")
	}
	r := n.run(ctx, dir, append(args, specs(targets)...)...)
	log := append(r.Stdout, r.Stderr...)
	if !r.Ran() || r.ExitCode != 0 {
		return log, refused(protocol.CodeFailedPackageManager, "npm exit %d: %s %v", r.ExitCode, tail(r.Stderr), r.Err)
	}
	return log, UpdateResult{}
}

func (n *npmPlanner) check(context.Context) (string, bool) { return "", true }

// --- dependencies with findings (ADR-0104) ---------------------------------------------------------------------------
//
// The packages a location has for their own sake move to their candidates (11.4); their dependencies move only with
// them. Refreshing moves the dependencies that have findings within the ranges their dependents declare, so far in npm
// folders with a lock file:
//
//  1. the hub names them: the agent offers every dependency installed there, the hub's signed vet flags them;
//  2. npm resolves "update <names>" into a copy of package.json and the lock, with the same --before as an update;
//  3. the hub vets the plan. A dependency whose newest allowed release still has a finding is left as it is, and the
//     plan is made once more without it; anything else flagged stops the run;
//  4. npm runs the same update in place (the same --before, so the same resolution), without install scripts unless
//     agent.yaml allows them for the location;
//  5. the location is read again: every entry of the plan installed.
//
// A dependency that no allowed release moves is named with the packages that pin it.

// Refreshable reports whether the dependencies of loc can be refreshed: an npm folder with a lock file.
func Refreshable(loc Location) bool {
	if loc.Kind != KindNpmDir {
		return false
	}
	_, err := os.Stat(filepath.Join(loc.Path, "package-lock.json"))
	return err == nil
}

// RefreshDeps refreshes the dependencies of loc that have findings. before as for Update.
func (u *Updater) RefreshDeps(ctx context.Context, loc Location, before func() error) UpdateResult {
	if !Refreshable(loc) {
		return refused(protocol.CodeRejectedUnsupported, "%s: dependencies are refreshed only in npm folders with a lock file",
			loc.ID)
	}
	var installed []protocol.VetPackage
	seen := map[[2]string]bool{}
	for _, p := range loc.Packages {
		if k := [2]string{p.Name, p.Version}; !p.Direct && !seen[k] {
			seen[k] = true
			installed = append(installed, protocol.VetPackage{Name: p.Name, Version: p.Version})
		}
	}
	if len(installed) == 0 {
		return UpdateResult{Code: protocol.CodeOK, Detail: "no dependencies here"}
	}
	v, res := u.vetAnswer(ctx, loc.Ecosystem, installed)
	if res.Code != "" {
		return res
	}
	var names []string
	for _, p := range v.Packages {
		if len(p.Flagged) > 0 && !slices.Contains(names, p.Name) {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return UpdateResult{Code: protocol.CodeOK, Detail: "no dependency here has a finding"}
	}
	slices.Sort(names)
	flagged := map[[2]string]bool{}
	for _, p := range v.Packages {
		if len(p.Flagged) > 0 {
			flagged[[2]string{p.Name, p.Version}] = true
		}
	}
	return u.refresh(ctx, loc, names, flagged, before)
}

// PlanDeps goes as far as RefreshDeps before vetting, for dependencies the operator names - without the hub, which
// would name them, and without changing anything ("vordr-agent packages --plan-deps <location>:<name>,...").
func (u *Updater) PlanDeps(ctx context.Context, loc Location, names []string) UpdateResult {
	if !Refreshable(loc) {
		return refused(protocol.CodeRejectedUnsupported, "%s: dependencies are refreshed only in npm folders with a lock file",
			loc.ID)
	}
	return u.refresh(ctx, loc, names, nil, nil)
}

// refresh moves names, the dependencies of loc with findings; flagged holds the installed copies (name, version) with
// findings, as the hub's vet said. Dependencies the hub named as fixes may move to a release as young as the shorter
// age (ADR-0111); npm's --before holds for the whole resolution, so they are refreshed first, on their own, and the
// other names after them at the minimum age.
func (u *Updater) refresh(ctx context.Context, loc Location, names []string, flagged map[[2]string]bool,
	before func() error) UpdateResult {
	for _, name := range names {
		if !protocol.PackageNamePattern.MatchString(name) {
			return refused(protocol.CodeRejectedScopeNotAllowed, "%q is no package name", name)
		}
	}
	if _, _, ok := NpmCommand(); !ok {
		return refused(protocol.CodeRejectedUnsupported, "npm not found")
	}
	var fixes, rest []string
	for _, name := range names {
		if u.fixNamed(loc.Ecosystem, name) {
			fixes = append(fixes, name)
		} else {
			rest = append(rest, name)
		}
	}
	switch {
	case len(fixes) == 0:
		return u.refreshNames(ctx, loc, rest, false, nil, before)
	case len(rest) == 0:
		return u.refreshNames(ctx, loc, fixes, true, flagged, before)
	}
	once := onlyOnce(before)
	return twoParts(u.refreshNames(ctx, loc, fixes, true, flagged, once), func() UpdateResult {
		return u.refreshNames(ctx, read(loc.ID, loc.Kind, loc.Path), rest, false, nil, once)
	})
}

// refreshNames refreshes names in one npm resolution; fixes: they are all fixes the hub named, resolved at the shorter
// age.
func (u *Updater) refreshNames(ctx context.Context, loc Location, names []string, fixes bool,
	flagged map[[2]string]bool, before func() error) UpdateResult {
	tmp, err := os.MkdirTemp(u.TempDir, "vordr-plan-")
	if err != nil {
		return refused(protocol.CodePrecheckDiskSpace, "%v", err)
	}
	defer os.RemoveAll(tmp)
	if err := handTo(tmp, loc.Path); err != nil { // npm runs as the location's owner and writes its plan here
		return refused(protocol.CodeFailedPackageManager, "temporary folder: %v", err)
	}
	n := newDepsPlanner(u, loc, tmp, fixes, flagged, u.Now())
	d, res := n.planDeps(ctx, names)
	if res.Code != "" {
		return res
	}
	if before == nil { // PlanDeps: show what would be installed, change nothing
		return UpdateResult{Code: protocol.CodeOK, Changed: d.moved, Plan: d.plan, Detail: strings.Join(d.kept, "; ")}
	}
	var stillFlagged []string // dependencies whose newest allowed release has a finding as well
	for round := 0; len(d.plan) > 0; round++ {
		v, res := u.vetAnswer(ctx, loc.Ecosystem, d.plan)
		if res.Code != "" {
			return res
		}
		var drop []string
		other := false
		for _, p := range v.Packages {
			switch {
			case len(p.Flagged) == 0:
			case slices.Contains(names, p.Name):
				if !slices.Contains(drop, p.Name) {
					drop = append(drop, p.Name)
				}
			default:
				other = true // something the update brings along: not one dependency to leave out
			}
		}
		if other || len(drop) > 0 && round > 0 {
			return refused(protocol.CodePrecheckVersionFlagged, "%s", strings.Join(flaggedIn(v), "; "))
		}
		if len(drop) == 0 {
			break
		}
		for _, name := range drop {
			stillFlagged = append(stillFlagged, name+": its newest allowed release has a finding as well")
		}
		names = slices.DeleteFunc(names, func(n string) bool { return slices.Contains(drop, n) })
		if len(names) == 0 {
			return UpdateResult{Code: protocol.CodeOK, Detail: "nothing to refresh: " + strings.Join(stillFlagged, "; ")}
		}
		if d, res = n.planDeps(ctx, names); res.Code != "" {
			return res
		}
	}
	notes := strings.Join(append(d.kept, stillFlagged...), "; ")
	if len(d.plan) == 0 {
		return UpdateResult{Code: protocol.CodeOK, Detail: "nothing to refresh: " + notes}
	}
	if err := before(); err != nil {
		return refused(protocol.CodeFailedPackageManager, "run journal: %v", err)
	}
	log, res := n.installDeps(ctx, names)
	res.Log = log
	if res.Code != "" {
		return res
	}
	have := map[[2]string]bool{}
	for _, p := range read(loc.ID, loc.Kind, loc.Path).Packages {
		have[[2]string{p.Name, p.Version}] = true
	}
	var missing []string
	for _, e := range d.plan {
		if !have[[2]string{e.Name, e.Version}] {
			missing = append(missing, e.Name+" "+e.Version)
		}
	}
	out := UpdateResult{Code: protocol.CodeOK, Detail: notes, Changed: d.moved, Log: log}
	if len(missing) > 0 {
		out.Code, out.Detail = protocol.CodeFailedVerification, "not installed afterwards: "+strings.Join(missing, ", ")
	}
	return out
}

// depsPlan is a refresh as npm resolves it: the new lock entries, and per dependency what moved or why not.
type depsPlan struct {
	plan  []protocol.VetPackage
	moved []string // "name old -> new"
	kept  []string // "name version: ... needs a new release of a, b"
}

func (n *npmPlanner) planDeps(ctx context.Context, names []string) (depsPlan, UpdateResult) {
	for _, f := range []string{"package.json", "package-lock.json"} {
		raw, err := os.ReadFile(filepath.Join(n.loc.Path, f))
		if err != nil {
			return depsPlan{}, refused(protocol.CodeFailedPackageManager, "%s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(n.tmp, f), raw, 0o644); err != nil {
			return depsPlan{}, refused(protocol.CodePrecheckDiskSpace, "%v", err)
		}
	}
	lock := filepath.Join(n.tmp, "package-lock.json")
	before, ok := entries(lock)
	if !ok {
		return depsPlan{}, refused(protocol.CodeFailedPackageManager, "package-lock.json is unreadable")
	}
	after, res := n.resolve(ctx, append([]string{"update", "--package-lock-only", "--ignore-scripts", "--json"}, names...))
	if res.Code != "" {
		return depsPlan{}, res
	}
	plan, res := newEntries(before, after)
	if res.Code != "" {
		return depsPlan{}, res
	}
	if res := n.checkOrigins(plan); res.Code != "" {
		return depsPlan{}, res
	}
	if res := n.checkPlanAges(ctx, plan, n.replacing(before, after)); res.Code != "" {
		return depsPlan{}, res
	}
	d := depsPlan{plan: plan}
	dependents := lockDependents(lock)
	for _, name := range names {
		was, now := versionsOf(before, name), versionsOf(after, name)
		if !slices.Equal(was, now) {
			d.moved = append(d.moved, name+" "+strings.Join(was, ", ")+" -> "+strings.Join(now, ", "))
			continue
		}
		k := name + " " + strings.Join(was, ", ") + ": no newer release that its dependents allow"
		if by := dependents[name]; len(by) > 0 {
			k += " - needs a new release of " + strings.Join(by, ", ")
		}
		d.kept = append(d.kept, k)
	}
	return d, UpdateResult{}
}

// installDeps runs the same update in place: with the plan's --before, so npm resolves it the same way.
func (n *npmPlanner) installDeps(ctx context.Context, names []string) ([]byte, UpdateResult) {
	args := []string{"update", "--prefix", n.loc.Path}
	if !n.u.Scripts {
		args = append(args, "--ignore-scripts")
	}
	r := n.run(ctx, n.loc.Path, append(args, names...)...)
	log := append(r.Stdout, r.Stderr...)
	if !r.Ran() || r.ExitCode != 0 {
		return log, refused(protocol.CodeFailedPackageManager, "npm exit %d: %s %v", r.ExitCode, tail(r.Stderr), r.Err)
	}
	return log, UpdateResult{}
}

// versionsOf: the versions of name in lock entries, sorted.
func versionsOf(entries map[string]protocol.VetPackage, name string) []string {
	var out []string
	for _, e := range entries {
		if e.Name == name && !slices.Contains(out, e.Version) {
			out = append(out, e.Version)
		}
	}
	slices.Sort(out)
	return out
}

// lockDependents: per package name, the installed packages whose dependencies in the lock file name it.
func lockDependents(path string) map[string][]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lf struct {
		Packages map[string]struct {
			Name                 string            `json:"name"`
			Dependencies         map[string]string `json:"dependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
		} `json:"packages"`
	}
	if json.Unmarshal(raw, &lf) != nil {
		return nil
	}
	out := map[string][]string{}
	for p, e := range lf.Packages {
		i := strings.LastIndex(p, "node_modules/")
		if i < 0 {
			continue // the folder itself: what it depends on is direct, never refreshed here
		}
		name := e.Name
		if name == "" {
			name = p[i+len("node_modules/"):]
		}
		for _, deps := range []map[string]string{e.Dependencies, e.OptionalDependencies, e.PeerDependencies} {
			for dep := range deps {
				if !slices.Contains(out[dep], name) {
					out[dep] = append(out[dep], name)
				}
			}
		}
	}
	for k := range out {
		slices.Sort(out[k])
	}
	return out
}
