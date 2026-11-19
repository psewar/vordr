package hub

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// The bounds of what an agent sends after its hello (ADR-0195): reports, inventories and job status messages, and its
// vet requests (ADR-0196). As for the hello (ADR-0191, hello.go), each message type is bounded in one place, here,
// before anything of it is stored, computed with, logged or shown in Home Assistant: boundReport, boundInventory,
// boundJobStatus, boundVet. What an earlier hub stored is bounded the same way when the hub loads it (OpenRegistry,
// NewPackages).
//
// A value out of its bound costs the narrowest thing that keeps the rest meaningful: the field, the list or map it
// belongs to (taken whole or not at all, as ADR-0191 takes lists), or the list entry, each set to what an agent sends
// that does not report it. An excerpt of a tool's output is cleaned instead: it is for people, and an empty one means
// something else (handshake.go). Where the rest would mislead without it - a count, a state or a code that decides
// what Home Assistant shows, the source a count belongs to, the job a status belongs to - the hub refuses the whole
// message and keeps what it had. The codes are the hello's (dropTooLong, dropCharacters, dropTooMany, dropValue). Only
// for an approved host, logBounds names what was not taken, rate-limited per host and event.
//
// Nesting is bounded by the wire types themselves: the hub decodes into them and keeps nothing else, and they hold no
// recursive type, no interface and no raw JSON (TestEveryFieldOfTheMessagesIsBounded).

// The bounds, each far above what agents send (TestTheHubTakesWhatAgentsReport); ADR-0195 says how each was chosen.
// Where the agent caps a list itself, the hub takes exactly that cap: protocol.MaxInventoryLocations,
// inventory.MaxPackages, inventory.MaxOSPackages, wire.MaxSummaryPackages, policy.MaxMinAgeDays.
const (
	maxKeyLen        = 64        // a state, code, phase, reason, signal, fact key, source name, kind; today's have 30 at most
	maxLocationIDLen = 64        // a location's id: agents make them up to 32, an id is never longer than this
	maxNameLen       = 1024      // a package or image name, a version or tag, a digest, a range, a container or service
	maxPathLen       = 4096      // a path on the host: PATH_MAX on Linux, far above Windows' usual 260
	maxExcerptLen    = 16 << 10  // a log excerpt: the agent keeps the last 800 bytes of a tool's output
	maxFactValueLen  = 16 << 10  // a fact of a source; the longest is a list of package ids
	maxKernelLen     = 255       // kernel_running, kernel_newest: a kernel release has at most 64
	maxCount         = 1_000_000 // a count of packages or updates, a run's seconds
	maxSources       = 64        // sources of a report: an agent sends one to three
	maxReportList    = 10_000    // pending packages or removals of one source
	maxOrigins       = 64        // origins of one pending package
	maxFacts         = 64        // facts of one source
	maxSignals       = 32        // reboot.checked: the signals that answered
	maxHolds         = 32        // holds of one package: the agent reports at most two (ADR-0129)
	maxNames         = 1024      // a reboot's packages, an image's containers or services, a run's services or packages
	maxProgress      = 100       // percent
)

var (
	// keyPattern: a state, code, phase, reason, signal or fact key - what a translation or a decision looks up. A newer
	// agent's value is kept and means nothing to this hub, as ADR-0191 keeps a newer verb.
	keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// kindPattern: a source's name or a location's kind (config-backup, synology-dsm, npm-global).
	kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	// ecosystemPattern: PyPI, npm, oci, and an OSV ecosystem with its release (Debian:13).
	ecosystemPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]*$`)
	// locationIDPattern: what the agent makes an id of - lowercase letters, digits, "_" and "-", starting with a letter or
	// a digit. A command names a location only by an id a scope takes (protocol.ValidAppID, UpdatableLocations).
	locationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// outOfBounds is what the bounds of one message did not take as sent. Keys are the fields' names in the message, a list
// entry's as "sources[].packages": the key names the field, not which entry.
type outOfBounds struct {
	dropped map[string]string // each field dropped, to why
	cleaned map[string]string // each excerpt cleaned or cut, to why
	refused map[string]string // the field that made the hub refuse the whole message, to why; at most one
}

func newOutOfBounds() *outOfBounds {
	return &outOfBounds{dropped: map[string]string{}, cleaned: map[string]string{}, refused: map[string]string{}}
}

// drop: field is out of its bound, for why, and drop takes it out. Nothing and false when why is "".
func (o *outOfBounds) drop(field, why string, drop func()) bool {
	if why == "" {
		return false
	}
	if _, seen := o.dropped[field]; !seen {
		o.dropped[field] = why
	}
	drop()
	return true
}

// clean: the excerpt field is not taken as sent, for why; set keeps it cleaned. Nothing when why is "".
func (o *outOfBounds) clean(field, why string, set func()) {
	if why == "" {
		return
	}
	if _, seen := o.cleaned[field]; !seen {
		o.cleaned[field] = why
	}
	set()
}

// refuse: the whole message is refused for field, for why. Nothing and false when why is "".
func (o *outOfBounds) refuse(field, why string) bool {
	if why == "" {
		return false
	}
	if len(o.refused) == 0 {
		o.refused[field] = why
	}
	return true
}

// taken: the message is kept, whole or with fields dropped or cleaned.
func (o *outOfBounds) taken() bool { return len(o.refused) == 0 }

// whole: the message is kept as sent.
func (o *outOfBounds) whole() bool { return o.taken() && len(o.dropped) == 0 && len(o.cleaned) == 0 }

// notTaken: every field refused, dropped or cleaned, to why; nil for a message taken whole.
func (o *outOfBounds) notTaken() map[string]string {
	if o.whole() {
		return nil
	}
	out := maps.Clone(o.dropped)
	maps.Copy(out, o.cleaned)
	maps.Copy(out, o.refused)
	return out
}

// dropsEntry: a drop the caller carries out - it leaves the entry the field belongs to out.
func dropsEntry() {}

// ReportNotTaken is what the hub would not take as sent of a report - each field it would refuse the report for, drop
// or clean, to why; nil when it takes the report whole. r is not changed. For the tests of the sources: what they
// report reaches the hub whole (ADR-0195).
func ReportNotTaken(r wire.Report) map[string]string {
	raw, _ := json.Marshal(r) // a copy as the hub receives it: boundReport changes what it bounds
	var sent wire.Report
	_ = json.Unmarshal(raw, &sent)
	return boundReport(&sent).notTaken()
}

// boundReport bounds a report (wire.Report) before anything of it is kept.
func boundReport(r *wire.Report) *outOfBounds {
	o := newOutOfBounds()
	// What the counts and states belong to: without one, the pending updates and the problem would be wrong.
	if o.refuse("sources", entriesBound(len(r.Sources), maxSources)) {
		return o
	}
	for _, s := range r.Sources {
		switch {
		case o.refuse("sources[].source", nameBound(s.Source, maxKeyLen, kindPattern.MatchString)),
			o.refuse("sources[].state", keyBound(string(s.State))),
			// check_failed without its code would show no problem (HostView), with another code the wrong one.
			o.refuse("sources[].code", optional(string(s.Code), keyBound)),
			o.refuse("sources[].pending", countBound(s.Pending)),
			o.refuse("sources[].security", countBound(s.Security)),
			o.refuse("sources[].kept_back", countBound(s.KeptBack)):
			return o
		}
	}
	for i := range r.Sources {
		s := &r.Sources[i]
		// Without its list a source's count stands for its whole pending set, as from a source that reports none.
		o.drop("sources[].packages", listBound(s.Packages, maxReportList, pendingBound), func() { s.Packages = nil })
		o.drop("sources[].removals", listBound(s.Removals, maxReportList, nameText), func() { s.Removals = nil })
		o.drop("sources[].facts", factsBound(s.Facts), func() { s.Facts = nil })
		cleaned, why := cleanExcerpt(s.Detail)
		o.clean("sources[].detail", why, func() { s.Detail = cleaned })
	}
	if rb := r.Reboot; rb != nil {
		// The signals say whether required means anything: without them the need for a reboot is unknown.
		if o.drop("reboot.checked", listBound(rb.Checked, maxSignals, keyBound), func() { r.Reboot = nil }) {
			return o
		}
		o.drop("reboot.reason", optional(string(rb.Reason), keyBound), func() { rb.Reason = "" })
		o.drop("reboot.packages", listBound(rb.Packages, maxNames, nameText), func() { rb.Packages = nil })
		o.drop("reboot.kernel_running", textBound(rb.Running, maxKernelLen), func() { rb.Running = "" })
		o.drop("reboot.kernel_newest", textBound(rb.Newest, maxKernelLen), func() { rb.Newest = "" })
		o.drop("reboot.services", listBound(rb.Services, source.MaxRebootServices, nameText),
			func() { rb.Services = nil })
	}
	return o
}

// pendingBound: one pending package - names and versions as text, its origins.
func pendingBound(p source.Package) string {
	for _, s := range []string{p.Name, p.From, p.To} {
		if why := nameText(s); why != "" {
			return why
		}
	}
	return listBound(p.Origins, maxOrigins, nameText)
}

// factsBound: the facts of a source - at most maxFacts, each key a key, each value text.
func factsBound(f map[string]string) string {
	if len(f) > maxFacts {
		return dropTooMany
	}
	for _, k := range slices.Sorted(maps.Keys(f)) {
		if why := keyBound(k); why != "" {
			return why
		}
		if why := textBound(f[k], maxFactValueLen); why != "" {
			return why
		}
	}
	return ""
}

// boundInventory bounds an inventory (wire.Inventory) before anything of it is kept.
func boundInventory(inv *wire.Inventory) *outOfBounds {
	o := newOutOfBounds()
	// The agent reports no more (Inventory, agent); the hub keeps the first of an older or another one's, and the event
	// says that the rest is missing.
	o.drop("locations", entriesBound(len(inv.Locations), protocol.MaxInventoryLocations),
		func() { inv.Locations = inv.Locations[:protocol.MaxInventoryLocations] })
	// Without its minimum ages the hub sends no fix at the shorter age (ADR-0111), as to an agent before them.
	o.drop("min_age_days", rangeBound(inv.MinAgeDays, policy.MaxMinAgeDays), func() { inv.MinAgeDays = 0 })
	o.drop("min_age_days_security", rangeBound(inv.MinAgeDaysSecurity, policy.MaxMinAgeDays),
		func() { inv.MinAgeDaysSecurity = 0 })
	kept := inv.Locations[:0]
	for _, l := range inv.Locations {
		if boundLocation(&l, o) {
			kept = append(kept, l)
		}
	}
	inv.Locations = kept
	return o
}

// boundLocation bounds one location of an inventory; false: it is dropped as a whole - what it is, what it is matched
// with, whether its list is complete, or its list itself is out of bounds.
func boundLocation(l *inventory.Location, o *outOfBounds) bool {
	switch {
	case o.drop("locations[].id", nameBound(l.ID, maxLocationIDLen, locationIDPattern.MatchString), dropsEntry),
		o.drop("locations[].kind", nameBound(l.Kind, maxKeyLen, kindPattern.MatchString), dropsEntry),
		o.drop("locations[].ecosystem", nameBound(string(l.Ecosystem), maxKeyLen, ecosystemPattern.MatchString),
			dropsEntry),
		// The code says the list is incomplete: without it the location would look complete.
		o.drop("locations[].code", optional(l.Code, keyBound), dropsEntry),
		o.drop("locations[].packages", entriesBound(len(l.Packages), inventory.MaxPackages), dropsEntry):
		return false
	}
	o.drop("locations[].path", textBound(l.Path, maxPathLen), func() { l.Path = "" })
	o.drop("locations[].skipped", countBound(l.Skipped), func() { l.Skipped = 0 })
	o.drop("locations[].outdated_failed", countBound(l.OutdatedFailed), func() { l.OutdatedFailed = 0 })
	o.drop("locations[].not_in_registry", countBound(l.NotInRegistry), func() { l.NotInRegistry = 0 })
	kept := l.Packages[:0]
	for _, p := range l.Packages {
		if boundPackage(&p, o) {
			kept = append(kept, p)
		}
	}
	l.Packages = kept
	return true
}

// boundPackage bounds one package of a location; false: it is dropped - its name or version, what it is matched by,
// is out of bounds. Every other field is dropped on its own; what it says is then unknown to the hub, as from an agent
// that does not report it: no newer release, no hold, not read inside.
func boundPackage(p *inventory.Package, o *outOfBounds) bool {
	if o.drop("locations[].packages[].name", nameText(p.Name), dropsEntry) ||
		o.drop("locations[].packages[].version", nameText(p.Version), dropsEntry) {
		return false
	}
	for _, f := range []struct {
		field string
		v     *string
	}{
		{"via", &p.Via}, {"latest", &p.Latest}, {"candidate", &p.Candidate},
		{"security_candidate", &p.SecurityCandidate}, {"digest", &p.Digest}, {"rebuilt", &p.Rebuilt},
		{"same_major", &p.SameMajor},
	} {
		o.drop("locations[].packages[]."+f.field, nameText(*f.v), func() { *f.v = "" })
	}
	o.drop("locations[].packages[].compare_failed", optional(p.CompareFailed, keyBound), func() { p.CompareFailed = "" })
	o.drop("locations[].packages[].holds", listBound(p.Holds, maxHolds, holdBound), func() { p.Holds = nil })
	o.drop("locations[].packages[].containers", listBound(p.Containers, maxNames, nameText),
		func() { p.Containers = nil })
	// Fewer services offer fewer buttons; the agent checks its policy again.
	o.drop("locations[].packages[].updatable", listBound(p.Updatable, maxNames, serviceBound),
		func() { p.Updatable = nil })
	if p.OS != nil {
		// Taken whole or not at all: a shortened list of packages would match as the image's whole contents.
		o.drop("locations[].packages[].os", imageOSBound(*p.OS), func() { p.OS = nil })
	}
	return true
}

// holdBound: what holds a package and its range, as text.
func holdBound(h inventory.Hold) string {
	if why := nameText(h.By); why != "" {
		return why
	}
	return nameText(h.Range)
}

// serviceBound: a Compose service as a scope names it, <project>/<service> (ADR-0119).
func serviceBound(s string) string {
	return nameBound(s, maxNameLen, func(s string) bool {
		sc, ok := protocol.ParseScope("image:" + s)
		return ok && sc.Kind == protocol.ScopeImage
	})
}

// imageOSBound: what an image carries inside - its release and code, and its packages as text.
func imageOSBound(os inventory.ImageOS) string {
	for _, s := range []string{os.ID, os.Version} {
		if why := nameText(s); why != "" {
			return why
		}
	}
	if why := optional(os.Code, keyBound); why != "" {
		return why
	}
	return listBound(os.Packages, inventory.MaxOSPackages, func(p inventory.OSPackage) string {
		for _, s := range []string{p.Name, p.Version, p.Source, p.SourceVersion} {
			if why := nameText(s); why != "" {
				return why
			}
		}
		return ""
	})
}

// boundJobStatus bounds a job status message (wire.JobStatus) before anything of it is kept or logged.
func boundJobStatus(st *wire.JobStatus) *outOfBounds {
	o := newOutOfBounds()
	// The job it belongs to and its result: without them it would settle another run, or leave its own open. An agent
	// answers a job whose id it could not read with none (protocol.UntrustedJobID).
	if o.refuse("job_id", optional(st.JobID, jobIDBound)) || o.refuse("code", optional(string(st.Code), keyBound)) {
		return o
	}
	// A status without its phase changes nothing; one without its reason still settles its run.
	o.drop("phase", optional(string(st.Phase), keyBound), func() { st.Phase = "" })
	o.drop("reason", optional(string(st.Reason), keyBound), func() { st.Reason = "" })
	// Cleaned, never emptied: an interrupted run's own detail must not read as the hub's guess (reconcileRuns).
	cleaned, why := cleanExcerpt(st.Detail)
	o.clean("detail", why, func() { st.Detail = cleaned })
	if st.Progress != nil {
		o.drop("progress", rangeBound(*st.Progress, maxProgress), func() { st.Progress = nil })
	}
	o.drop("item", nameText(st.Item), func() { st.Item = "" })
	s := st.Summary
	if s == nil {
		return o
	}
	// The count decides whether the run ends a ring delay (ADR-0107): without a summary it ends none.
	if o.drop("summary.package_count", countBound(s.PackageCount), func() { st.Summary = nil }) ||
		o.drop("summary.seconds", countBound(s.Seconds), func() { st.Summary = nil }) {
		return o
	}
	o.drop("summary.packages", listBound(s.Packages, wire.MaxSummaryPackages, nameText), func() { s.Packages = nil })
	o.drop("summary.log_file", textBound(s.LogFile, maxPathLen), func() { s.LogFile = "" })
	o.drop("summary.outdated_services", listBound(s.OutdatedServices, maxNames, nameText),
		func() { s.OutdatedServices = nil })
	o.drop("summary.replaced", listBound(s.Replaced, maxNames, nameText), func() { s.Replaced = nil })
	o.drop("summary.restarted", listBound(s.Restarted, maxNames, nameText), func() { s.Restarted = nil })
	return o
}

// boundVet bounds a vet request (wire.Vet, design 11.4) before the hub vouches for anything of it or logs it. It is
// taken whole or refused whole, never trimmed (ADR-0196): with an entry left out the hub would sign a statement about
// less than the agent installs. The hub then answers that it cannot vouch (protocol.VetOutOfBounds), and the agent
// stops the update.
func boundVet(req *wire.Vet) *outOfBounds {
	o := newOutOfBounds()
	switch {
	// The job the statement is bound to: always one, as the agent's job carries it.
	case o.refuse("job_id", jobIDBound(req.JobID)),
		// What the hub can match: an ecosystem it orders (pip, npm). A newer one is no plan this hub can vouch for.
		o.refuse("ecosystem", ecosystemBound(req.Ecosystem)),
		// The server ends the connection beyond it, as before; here for every caller of boundVet.
		o.refuse("packages", entriesBound(len(req.Packages), wire.MaxVetPackages)):
		return o
	}
	for _, p := range req.Packages {
		switch {
		// The bound the agent plans within too (protocol.MaxVetEntryLen), as long as ADR-0195's names.
		case o.refuse("packages[].name", textBound(p.Name, protocol.MaxVetEntryLen)),
			o.refuse("packages[].version", textBound(p.Version, protocol.MaxVetEntryLen)),
			// What the hub fills in: a request that brings its own is no plan the agent made.
			o.refuse("packages[].flagged", setByTheHub(len(p.Flagged) > 0)),
			o.refuse("packages[].malicious", setByTheHub(p.Malicious)):
			return o
		}
	}
	return o
}

// ecosystemBound: why e is no ecosystem the hub can vet, "" when it is - one whose versions it orders and matches with
// OSV (versions.Ecosystem.Known): dropValue for another, dropTooLong for one longer than a key.
func ecosystemBound(e string) string {
	switch {
	case len(e) > maxKeyLen:
		return dropTooLong
	case !versions.Ecosystem(e).Known():
		return dropValue
	}
	return ""
}

// setByTheHub: dropValue for a field of a vet request the hub fills in itself, when the request sets it.
func setByTheHub(set bool) string {
	if set {
		return dropValue
	}
	return ""
}

// VetNotTaken is what the hub would refuse a vet request for - the field, to why; nil when it takes the request. For
// the tests of the agent's plans: what it asks reaches the hub whole (ADR-0196).
func VetNotTaken(req wire.Vet) map[string]string {
	return boundVet(&req).notTaken()
}

func keyBound(k string) string { return nameBound(k, maxKeyLen, keyPattern.MatchString) }
func nameText(s string) string { return textBound(s, maxNameLen) }
func countBound(n int) string  { return rangeBound(n, maxCount) }

// entriesBound: dropTooMany for a list of n entries when it is longer than max, "" when it is not.
func entriesBound(n, max int) string {
	if n > max {
		return dropTooMany
	}
	return ""
}

// rangeBound: why n is not from 0 to max, "" when it is.
func rangeBound(n, max int) string {
	if n < 0 || n > max {
		return dropValue
	}
	return ""
}

// excerptControl: a control character an excerpt does not keep - all but the line breaks and tabs of a tool's output.
// An escape sequence then reaches no terminal.
func excerptControl(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }

// cleanExcerpt is a log excerpt as the hub keeps it, and why it differs from s ("" when it does not): every control
// character but CR, LF and tab, and every byte that is not UTF-8, becomes U+FFFD (dropCharacters); beyond maxExcerptLen
// bytes only its end is kept, as the agent keeps the end of a tool's output (dropTooLong). A text that was not empty
// never becomes empty.
func cleanExcerpt(s string) (string, string) {
	why := ""
	if len(s) > maxExcerptLen {
		s, why = tailOf(s, maxExcerptLen), dropTooLong
	}
	if !utf8.ValidString(s) || strings.ContainsFunc(s, excerptControl) {
		s = strings.Map(func(r rune) rune {
			if excerptControl(r) {
				return utf8.RuneError
			}
			return r
		}, strings.ToValidUTF8(s, string(utf8.RuneError)))
		if len(s) > maxExcerptLen { // U+FFFD has three bytes
			s = tailOf(s, maxExcerptLen)
		}
		if why == "" {
			why = dropCharacters
		}
	}
	return s, why
}

// tailOf: the last max bytes of s at most, starting at a whole character.
func tailOf(s string, max int) string {
	s = s[len(s)-max:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// boundsEventEvery: per host and event, the hub logs what it did not take of a message at most once in this time; the
// next event counts the messages left out of the log in between (unlogged). A host cannot write the event log full with
// messages out of bounds, the first one shows at once, and a harmless drop does not hide a refusal.
const boundsEventEvery = time.Hour

// boundsLog: when logBounds last logged one event for one host, and how many it left out of the log since.
type boundsLog struct {
	at       time.Time
	unlogged int
}

// logBounds logs what the bounds did not take of an approved host's message of kind (report, inventory, job_status,
// vet), with the key's fingerprint: <kind>_refused names the field that refused it, <kind>_fields_dropped each field it
// dropped (fields) and each excerpt it cleaned (cleaned). Nothing for a message taken whole; at most one event per host
// and event in boundsEventEvery.
func (s *Server) logBounds(node, fingerprint, kind string, o *outOfBounds) {
	if o.whole() {
		return
	}
	event := kind + "_fields_dropped"
	fields := map[string]any{"fingerprint": fingerprint}
	switch {
	case !o.taken():
		event, fields["fields"] = kind+"_refused", o.refused
	default:
		if len(o.dropped) > 0 {
			fields["fields"] = o.dropped
		}
		if len(o.cleaned) > 0 {
			fields["cleaned"] = o.cleaned
		}
	}
	now := s.Now()
	s.mu.Lock()
	if s.boundsLogged == nil {
		s.boundsLogged = map[string]map[string]*boundsLog{}
	}
	if s.boundsLogged[node] == nil {
		s.boundsLogged[node] = map[string]*boundsLog{}
	}
	last := s.boundsLogged[node][event]
	if last != nil && !now.Before(last.at) && now.Sub(last.at) < boundsEventEvery {
		last.unlogged++
		s.mu.Unlock()
		return
	}
	if last != nil && last.unlogged > 0 {
		fields["unlogged"] = last.unlogged
	}
	s.boundsLogged[node][event] = &boundsLog{at: now}
	s.mu.Unlock()
	s.Events.Log(node, event, fields)
}

// forgetBounds: a host the operator forgot starts afresh - its name, approved again, inherits no silence.
func (s *Server) forgetBounds(node string) {
	s.mu.Lock()
	delete(s.boundsLogged, node)
	s.mu.Unlock()
}
