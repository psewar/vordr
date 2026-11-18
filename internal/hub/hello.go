package hub

import (
	"crypto/ed25519"
	"regexp"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The bounds of an agent's hello (ADR-0191), checked here and nowhere else, before anything of the hello is stored,
// computed with, logged or shown in Home Assistant - also for a host that is not approved yet.
//
// Its identity - type, node name, key and the signature over the challenge - must hold, or the hub refuses the hello
// with bad_hello (helloIdentityHolds). Everything else describes the host: agent version, facts, verbs, runs, the
// policy summary. Each of these fields is taken whole or not at all: a field out of its bound is dropped - set to what
// an agent sends that does not report it, a fact to "unknown" as from an agent that could not find it out - and the
// event hello_fields_dropped names it with why. The host still connects. The reboot window has its own check and
// event, the first of these (ADR-0187, takeRebootWindow). Both events follow only once the register has taken the
// host (logDrops): a hello the register refuses names a node that is not its own.
//
// The hello of a Home Assistant client is bounded here as well, the same way (ADR-0195): haHelloIdentityHolds,
// boundHAHello. What an agent sends after its hello is bounded in bounds.go.

// Why a field of a hello is dropped: the fixed codes in the event hello_fields_dropped. A window that is dropped says
// why with the codes of the reboot window (rebootWindowBadDays, rebootWindowBadWindow).
const (
	dropTooLong    = "too_long"   // a text or a name longer than its bound
	dropCharacters = "characters" // a character the field does not take: control characters, or outside a name's set
	dropTooMany    = "too_many"   // a list with more entries than its bound
	dropValue      = "value"      // a number outside its range
)

// The bounds. They lie well above what any agent reports: the agent's own builder is checked against them
// (TestTheRealAgentsHelloFitsTheHubsBounds). Those agent.yaml decides - the number of apps and allowed scopes, an app's
// name, the days of a window - are protocol.MaxApps, protocol.MaxAllowedScopes, protocol.MaxAppNameLen and
// protocol.MaxWindowDays, which check-config refuses beyond as well (ADR-0195).
const (
	maxFactLen   = 255 // bytes of one fact
	maxVersion   = 64  // the bound of a signed manifest's version (protocol.ValidVersion)
	maxCodeLen   = 32  // a verb, install.auto
	maxScopeLen  = 384 // an entry of allowed_scopes, default_scope, auto_scope; the protocol's longest has 353 (pkg:)
	maxAppIDLen  = 32  // protocol.ValidAppID
	maxJobIDLen  = 64  // protocol.ValidJobID
	maxVerbs     = 16  // the closed set has six; a newer agent may name one this hub does not know
	maxRunIDs    = 64  // in each list of runs: the jobs an agent carries out now, the results it holds
	maxIntervalH = 168
)

var (
	// codePattern: a verb or install.auto. The hub acts only on the ones it knows; a newer agent's well-formed verb is
	// kept and offers nothing.
	codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// scopePattern: the characters of the scopes the protocol knows - security, all, app:<id>, image:<project>/<service>
	// with "*" as agent.yaml takes it, and pkg:<location>/<name> (protocol.PackageNamePattern: "@", "~"). The hub
	// compares scopes, it does not parse them here: a newer agent's scope is kept and offers nothing.
	scopePattern = regexp.MustCompile(`^[a-z][A-Za-z0-9:/_.*@~-]*$`)
	// zonePattern: the characters of an IANA time zone name ("America/Argentina/Buenos_Aires", "Etc/GMT+5").
	zonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._+/-]*$`)
)

// helloIdentityHolds: the hello names a valid node and a key, and proves the key over this connection's challenge.
func helloIdentityHolds(h wire.Hello, nonce []byte) bool {
	return nodePattern.MatchString(h.Node) && len(h.PublicKey) == ed25519.PublicKeySize &&
		ed25519.Verify(h.PublicKey, wire.HelloSigned(nonce, h.Node, h.PublicKey), h.Signature)
}

// haHelloIdentityHolds: the hello of a Home Assistant client (ADR-0195) has the role ha, names a valid client and a key,
// and proves the key over this connection's challenge. Out of bounds, the hub refuses it with bad_hello.
func haHelloIdentityHolds(h wire.HAHello, nonce []byte) bool {
	return h.Role == wire.RoleHA && nodePattern.MatchString(h.Name) && len(h.PublicKey) == ed25519.PublicKeySize &&
		ed25519.Verify(h.PublicKey, wire.HAHelloSigned(nonce, h.Name, h.PublicKey), h.Signature)
}

// boundHAHello drops each field of a Home Assistant client's hello whose identity holds that is out of its bound, and
// returns them as boundHello does. The protocol is compared as it is (protocol_mismatch); integration_version is
// empty or a version a signed manifest may carry, as the agent's (ADR-0195).
func boundHAHello(h *wire.HAHello) map[string]string {
	dropped := map[string]string{}
	if why := optional(h.IntegrationVersion, versionBound); why != "" {
		dropped["integration_version"], h.IntegrationVersion = why, ""
	}
	return dropped
}

// helloDrops is what takeHello did not take of a hello.
type helloDrops struct {
	rebootWindow string            // why the reboot window was dropped (ADR-0187); "" when it was taken
	fields       map[string]string // every other dropped field to why (boundHello)
}

// takeHello bounds a hello whose identity holds, before anything of it is kept: the reboot window first, as sent
// (ADR-0187), then every other field (boundHello, ADR-0191). It returns what it dropped, for logDrops.
func takeHello(h *wire.Hello) helloDrops {
	return helloDrops{rebootWindow: takeRebootWindow(h), fields: boundHello(h)}
}

// logDrops logs what takeHello dropped, with the key's fingerprint. Only for a hello the register took (pending or
// approved): one it refused - another key under a known name, a rejected host, no room for another pending one - names
// a node that is not its own, and its fields go nowhere.
func (s *Server) logDrops(node, fingerprint string, d helloDrops) {
	if d.rebootWindow != "" {
		s.Events.Log(node, "reboot_window_refused", map[string]any{"reason": d.rebootWindow, "fingerprint": fingerprint})
	}
	if len(d.fields) > 0 {
		s.Events.Log(node, "hello_fields_dropped", map[string]any{"fields": d.fields, "fingerprint": fingerprint})
	}
}

// boundHello drops each field of h that is out of its bound and returns them, the field's name in the hello to why.
// Empty: h is taken as sent.
func boundHello(h *wire.Hello) map[string]string {
	dropped := map[string]string{}
	check := func(field, why string, drop func()) {
		if why != "" {
			dropped[field] = why
			drop()
		}
	}
	check("agent_version", optional(h.AgentVersion, versionBound), func() { h.AgentVersion = "" })
	f := &h.Facts
	for _, fact := range []struct {
		field string
		v     *string
	}{
		{"facts.hostname", &f.Hostname}, {"facts.os", &f.OS}, {"facts.os_id", &f.OSID}, {"facts.os_version", &f.OSVersion},
		{"facts.goos", &f.GOOS}, {"facts.arch", &f.Arch}, {"facts.virtualization", &f.Virtualization},
		{"facts.container", &f.Container},
	} {
		// As from an agent that could not find the fact out: "unknown" never means no, while an empty container would
		// count as one (facts.Host.InContainer).
		check(fact.field, textBound(*fact.v, maxFactLen), func() { *fact.v = facts.Unknown })
	}
	check("verbs", listBound(h.Verbs, maxVerbs, func(v protocol.Verb) string { return codeBound(string(v)) }),
		func() { h.Verbs = nil })
	if r := h.Runs; r != nil {
		why := listBound(r.Running, maxRunIDs, jobIDBound)
		if why == "" {
			why = listBound(r.Unacknowledged, maxRunIDs, jobIDBound)
		}
		check("runs", why, func() { h.Runs = nil }) // as from an agent before ADR-0114: the hub keeps its guess
	}
	p := &h.Policy
	check("policy.allowed_scopes", listBound(p.AllowedScopes, protocol.MaxAllowedScopes, scopeBound),
		func() { p.AllowedScopes = nil })
	check("policy.default_scope", optional(p.DefaultScope, scopeBound), func() { p.DefaultScope = "" })
	check("policy.auto", optional(p.Auto, codeBound), func() { p.Auto = "" })
	check("policy.auto_scope", optional(p.AutoScope, scopeBound), func() { p.AutoScope = "" })
	check("policy.apps", listBound(p.Apps, protocol.MaxApps, appBound), func() { p.Apps = nil })
	if p.Window != nil {
		check("policy.window", windowRefusal(*p.Window), func() { p.Window = nil })
	}
	check("policy.tz", optional(p.TZ, zoneBound), func() { p.TZ = "" })
	if p.MinIntervalH < 0 || p.MinIntervalH > maxIntervalH {
		check("policy.min_interval_h", dropValue, func() { p.MinIntervalH = 0 }) // as from an agent before ADR-0110
	}
	return dropped
}

// textBound: why s is no text of at most max bytes, "" when it is. Text is valid UTF-8 without control characters
// (protocol.PlainText; encoding/json already turns invalid UTF-8 into U+FFFD).
func textBound(s string, max int) string {
	switch {
	case len(s) > max:
		return dropTooLong
	case !protocol.PlainText(s):
		return dropCharacters
	}
	return ""
}

// nameBound: why s is no name of at most max bytes that valid takes, "" when it is.
func nameBound(s string, max int, valid func(string) bool) string {
	switch {
	case len(s) > max:
		return dropTooLong
	case !valid(s):
		return dropCharacters
	}
	return ""
}

// optional: an empty field is one the agent does not report, and in bounds; anything else is bounded by bound.
func optional(s string, bound func(string) string) string {
	if s == "" {
		return ""
	}
	return bound(s)
}

// listBound: why list is out of bounds - more than max entries, or the first entry each refuses -, "" when it is not.
func listBound[T any](list []T, max int, each func(T) string) string {
	if len(list) > max {
		return dropTooMany
	}
	for _, v := range list {
		if why := each(v); why != "" {
			return why
		}
	}
	return ""
}

func versionBound(v string) string { return nameBound(v, maxVersion, protocol.ValidVersion) }
func codeBound(c string) string    { return nameBound(c, maxCodeLen, codePattern.MatchString) }
func scopeBound(s string) string   { return nameBound(s, maxScopeLen, scopePattern.MatchString) }
func zoneBound(z string) string    { return nameBound(z, maxZoneLen, zonePattern.MatchString) }
func jobIDBound(id string) string  { return nameBound(id, maxJobIDLen, protocol.ValidJobID) }

// appBound: an app's id as agent.yaml takes it, its name as text.
func appBound(a wire.AppInfo) string {
	if why := nameBound(a.ID, maxAppIDLen, protocol.ValidAppID); why != "" {
		return why
	}
	return textBound(a.Name, protocol.MaxAppNameLen)
}
