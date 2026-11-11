package protocol

import (
	"regexp"
	"strings"
)

// ScopeKind is what an install job covers.
type ScopeKind string

const (
	ScopeSecurity ScopeKind = "security"
	ScopeAll      ScopeKind = "all"
	ScopeApp      ScopeKind = "app"
	// ScopePkg: pip/npm packages of one location the agent itself reported (design 11.4): "pkg:<location>" updates
	// every package there that has a candidate, "pkg:<location>/<name>" one of them.
	ScopePkg ScopeKind = "pkg"
	// ScopeDeps: "deps:<location>" moves the dependencies there that have findings to the newest release their
	// dependents allow (ADR-0104). The hub's signed vet names them; the agent offers only what it has installed.
	ScopeDeps ScopeKind = "deps"
	// ScopeImage: "image:<project>/<service>" moves one Compose service to the rebuild of its tag (ADR-0119): the
	// image the tag points to now, once it is min_age_days old, with the old one kept as the way back. ScopeImages:
	// "images" does that for every service the agent's policy allows that has such a rebuild; the agent picks them, the
	// job names none.
	ScopeImage  ScopeKind = "image"
	ScopeImages ScopeKind = "images"
)

// Scope of an install job. App is the id of an entry in the agent's own app list, never a package name. Location and
// Package of a pkg scope name what the agent reported itself; it checks that they are its own, and it alone decides
// the version (its candidate), never the hub.
type Scope struct {
	Kind     ScopeKind
	App      string
	Location string
	Package  string // "" = every package of the location with a candidate
	// Project and Service of an image scope name a Compose service by its labels; nothing else of the job reaches the
	// engine or a command line.
	Project string
	Service string
}

var (
	appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	// PackageNamePattern: what PyPI (PEP 508) and npm (scoped too) accept as a name; nothing else becomes an argument.
	// It starts with a letter or digit, so that it can never be read as an option ("--index-url=...").
	PackageNamePattern = regexp.MustCompile(`^(?:@[A-Za-z0-9][A-Za-z0-9-._~]{0,99}/)?[A-Za-z0-9][A-Za-z0-9-._~]{0,213}$`)
	// ComposeProjectPattern and ComposeServicePattern: what Compose accepts as a project name (lowercase letters,
	// digits, "_" and "-") and a service name (also capitals and "."), each starting with a letter or digit, so that
	// neither can be read as an option. Bounded so that the tag that keeps the previous image,
	// "vordr-previous.<project>.<service>", stays within the 128 characters of a tag.
	ComposeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,55}$`)
	ComposeServicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,55}$`)
)

// ValidAppID reports whether id is a well-formed app id (lowercase, digits, "_" and "-", at most 32 characters).
// Location ids follow the same rules.
func ValidAppID(id string) bool { return appIDPattern.MatchString(id) }

// ParseScope reads "security", "all", "app:<id>", "pkg:<location>", "pkg:<location>/<name>", "deps:<location>",
// "image:<project>/<service>" or "images".
func ParseScope(s string) (Scope, bool) {
	switch {
	case s == string(ScopeSecurity):
		return Scope{Kind: ScopeSecurity}, true
	case s == string(ScopeAll):
		return Scope{Kind: ScopeAll}, true
	case s == string(ScopeImages):
		return Scope{Kind: ScopeImages}, true
	case strings.HasPrefix(s, "image:"):
		project, service, ok := strings.Cut(strings.TrimPrefix(s, "image:"), "/")
		if !ok || !ComposeProjectPattern.MatchString(project) || !ComposeServicePattern.MatchString(service) {
			return Scope{}, false
		}
		return Scope{Kind: ScopeImage, Project: project, Service: service}, true
	case strings.HasPrefix(s, "app:") && ValidAppID(strings.TrimPrefix(s, "app:")):
		return Scope{Kind: ScopeApp, App: strings.TrimPrefix(s, "app:")}, true
	case strings.HasPrefix(s, "pkg:"):
		loc, name, named := strings.Cut(strings.TrimPrefix(s, "pkg:"), "/")
		if !ValidAppID(loc) || named && !PackageNamePattern.MatchString(name) {
			return Scope{}, false
		}
		return Scope{Kind: ScopePkg, Location: loc, Package: name}, true
	case strings.HasPrefix(s, "deps:") && ValidAppID(strings.TrimPrefix(s, "deps:")):
		return Scope{Kind: ScopeDeps, Location: strings.TrimPrefix(s, "deps:")}, true
	}
	return Scope{}, false
}

func (s Scope) String() string {
	switch s.Kind {
	case ScopeApp:
		return "app:" + s.App
	case ScopePkg:
		if s.Package != "" {
			return "pkg:" + s.Location + "/" + s.Package
		}
		return "pkg:" + s.Location
	case ScopeDeps:
		return "deps:" + s.Location
	case ScopeImage:
		return "image:" + s.Project + "/" + s.Service
	}
	return string(s.Kind)
}

// Images reports whether the scope updates container images (ADR-0119) rather than packages.
func (s Scope) Images() bool { return s.Kind == ScopeImage || s.Kind == ScopeImages }
