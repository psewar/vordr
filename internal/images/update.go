package images

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// Updating an image by button (ADR-0119). The agent moves a Compose service to the rebuild of its tag: the build the
// tag points to now, once it is at least min_age_days old. It pulls exactly that build by its digest, keeps the
// running image under a tag of its own as the way back, gives the new build the Compose file's tag and lets Compose
// recreate the service, without pulling or building. When the service does not come back running steadily and
// healthy, the old image gets the tag again and Compose recreates the service once more. A newer tag (1.2 -> 1.3)
// would mean editing the Compose file, which stays the operator's, as a pinned digest does.

// whyNotUpdatable says why the agent cannot move the image of a running container; "" when it can: a tag without a
// pinned digest, pulled from its registry, not built on this host.
func (r Running) whyNotUpdatable() string {
	ref, ok := ParseRef(r.Reference)
	switch {
	case !ok:
		return "the container names no image reference"
	case ref.Digest != "":
		return "the reference pins a digest; moving a pin stays the operator's"
	case r.BuiltLocally:
		return "the image was built on this host"
	case r.RepoDigest(ref) == "":
		return "the image was not pulled from " + ref.Name()
	}
	return ""
}

// MarkUpdatable fills in, per image of the container location, the Compose services that run it, that allowed lets
// Home Assistant update and whose image the agent can move (movable says why not; nil: the engine gave no answer, and
// nothing is offered).
func MarkUpdatable(loc *inventory.Location, running []Running, allowed func(Service) bool,
	movable func(Service) string) {
	if movable == nil {
		return
	}
	at := map[string]int{}
	for i, p := range loc.Packages {
		at[Key(p.Name, p.Version, p.Digest)] = i
	}
	for _, r := range running {
		k, ok := KeyOf(r)
		i, listed := at[k]
		if !ok || !listed { // pinned and local images: movable says so per service
			continue
		}
		p := &loc.Packages[i]
		for _, name := range r.Services {
			if s, ok := ParseService(name); ok && allowed(s) && movable(s) == "" {
				p.Updatable = append(p.Updatable, name)
			}
		}
		slices.Sort(p.Updatable)
		p.Updatable = slices.Compact(p.Updatable)
	}
}

// Movable reads the containers once and returns what says why a service's image cannot be moved ("" when it can),
// for marking the inventory; nil when the engine gave no answer.
func (e Engine) Movable(ctx context.Context) func(Service) string {
	all, ok := e.Containers(ctx)
	if !ok {
		return nil
	}
	return func(s Service) string {
		_, why, ok := e.movable(ctx, s, all)
		if !ok {
			return "the container engine did not answer"
		}
		return why
	}
}

// ParseService reads "<project>/<service>".
func ParseService(name string) (Service, bool) {
	sc, ok := protocol.ParseScope("image:" + name)
	return Service{Project: sc.Project, Service: sc.Service}, ok
}

// target is a service with everything the update needs, read and checked before anything changes.
type target struct {
	s     Service
	ctr   Container // its one container
	ref   Ref
	image Running
}

// movable reads what moving the service's image needs, from the engine only, and says why it cannot be moved; "" when
// it can. ok is false when the engine gave no usable answer.
func (e Engine) movable(ctx context.Context, s Service, all []Container) (t target, why string, ok bool) {
	t.s = s
	var mine []Container
	for _, c := range all {
		if c.Compose && c.Service == s {
			mine = append(mine, c)
		}
	}
	switch {
	case len(mine) == 0:
		return t, "no container of the service runs", true
	case len(mine) > 1:
		return t, fmt.Sprintf("the service has %d containers; a scaled service stays manual", len(mine)), true
	case mine[0].State != "running":
		return t, "its container is " + mine[0].State, true
	}
	t.ctr = mine[0]
	if why := projectFiles(t.ctr); why != "" {
		return t, why, true
	}
	img, ans := e.inspectImage(ctx, t.ctr.ImageID)
	switch ans {
	case engineGone:
		return t, "its image is gone", true
	case engineFailed:
		return t, "", false
	}
	t.image = Running{Reference: t.ctr.Reference, ImageID: t.ctr.ImageID, RepoDigests: img.RepoDigests,
		Created: img.Created, Platform: img.platform(), RepoTags: img.RepoTags,
		BuiltLocally: builtLocally(img.RepoDigests, img.Config.Labels)}
	if why := t.image.whyNotUpdatable(); why != "" {
		return t, why, true
	}
	t.ref, _ = ParseRef(t.ctr.Reference)
	// The tag would move under every container created from it: a shared tag stays manual. A stopped container of the
	// same Compose project does not count (ADR-0123), such as a one-shot helper that sets permissions before the service
	// starts: it keeps its image by id, and takes the new one only when Compose recreates it with its project - what an
	// update of the project means. A container of "docker compose run" has no service, so it still counts.
	for _, c := range all {
		if stopped(c.State) && c.Service.Project == s.Project {
			continue
		}
		if r, ok := ParseRef(c.Reference); ok && c.ID != t.ctr.ID && r.Digest == "" && r.Name() == t.ref.Name() &&
			r.Tag == t.ref.Tag {
			return t, "the tag is also used by the container " + c.Name + "; moving it would change that one too", true
		}
	}
	return t, "", true
}

// stopped: the engine's states of a container whose process does not run and does not come back by itself. paused and
// restarting are running containers.
func stopped(state string) bool {
	return state == "exited" || state == "created" || state == "dead"
}

// projectFiles checks the project's directory and files as Compose recorded them: absolute, and still there.
func projectFiles(c Container) string {
	if c.WorkingDir == "" || len(c.ConfigFiles) == 0 || c.ConfigHash == "" {
		return "the container lacks the labels of its Compose project"
	}
	if fi, err := os.Stat(c.WorkingDir); !filepath.IsAbs(c.WorkingDir) || err != nil || !fi.IsDir() {
		return "the project directory " + c.WorkingDir + " is gone"
	}
	for _, f := range append(slices.Clone(c.ConfigFiles), c.EnvFiles...) {
		if fi, err := os.Stat(f); !filepath.IsAbs(f) || err != nil || !fi.Mode().IsRegular() {
			return "the project file " + f + " is gone"
		}
	}
	return ""
}

// RebuildState: what the registry says about a running image's tag at the moment of an update.
type RebuildState int

const (
	RebuildNone    RebuildState = iota // the tag points to the running build, or to nothing for this platform
	RebuildReady                       // another build, at least the minimum age old
	RebuildTooNew                      // another build, younger than the minimum age or of unknown age
	RebuildUnknown                     // the registry gave no usable answer
)

// Rebuild is the tag's build now.
type Rebuild struct {
	State   RebuildState
	Digest  string    // what the tag points to now
	Created time.Time // when that build was made
	Size    int64     // its download for the host's platform: layers and configuration
}

// Rebuild asks the registry now what the running image's tag points to. The inventory's answer can be hours old;
// an update takes only what the registry says at the moment of the run, and pulls exactly that digest.
func (r *Registry) Rebuild(ctx context.Context, run Running, ref Ref, now time.Time, minAge time.Duration) Rebuild {
	s := r.session(MaxRequests, now)
	old := run.RepoDigest(ref)
	d, ans := s.digestOf(ctx, ref, ref.Tag)
	switch {
	case ans.failed():
		return Rebuild{State: RebuildUnknown}
	case ans == answerNotFound || d == old: // the tag is gone, or still the running build
		return Rebuild{State: RebuildNone}
	}
	rebuilt, created, ans := s.rebuilt(ctx, ref, run, old, d)
	switch {
	case ans.failed():
		return Rebuild{State: RebuildUnknown}
	case !rebuilt:
		return Rebuild{State: RebuildNone}
	}
	// rebuilt read the new build through the cache, which keeps its size; an entry from before has none.
	size := int64(0)
	if c, ok := r.cached(d+" "+run.Platform.String(), now); ok {
		size = c.Size
	}
	if size == 0 {
		m, ans := s.platformManifest(ctx, ref, d, run.Platform)
		if size = m.size(); ans != answerOK || size == 0 {
			return Rebuild{State: RebuildUnknown}
		}
	}
	rb := Rebuild{State: RebuildReady, Digest: d, Created: created, Size: size}
	if !plausible(created) || now.Sub(created) < minAge {
		rb.State = RebuildTooNew
	}
	return rb
}

// KeepTag is the tag under which the running image of a service stays on the host as the way back: one per service.
func KeepTag(s Service) string { return "vordr-previous." + s.Project + "." + s.Service }

// Updater moves Compose services to the rebuild of their tag. It writes to the engine only for services Allowed
// names, only once every check passed, and only these steps: pull a build by its digest, put tags on images, take
// its own tag off, remove an image no name and no container holds, and run Compose with the fixed arguments of
// composeArgs.
type Updater struct {
	Engine  Engine
	Reg     *Registry
	Compose runner.Runner
	Now     func() time.Time
	MinAge  time.Duration
	Allowed func(Service) bool
	Free    func(path string) (uint64, bool) // FreeBytes; tests replace it
	// Settle: how long a recreated service may take to run steadily, and to be healthy where it has a health check;
	// Stable: how long its container must then run without a restart; Every: the pause between looks.
	Settle time.Duration
	Stable time.Duration
	Every  time.Duration
	// ChangeBudget bounds what follows the journal for one service - recreating, waiting, going back. It runs apart
	// from the run's own deadline and from the agent stopping: once something changed, it is finished or undone.
	ChangeBudget time.Duration
}

// UpdateHooks: what the agent adds to a run.
type UpdateHooks struct {
	BeforeChange func() error                     // once, before the first change: the run journal
	Health       func(ctx context.Context) string // agent.yaml health; "" when it passes
	Progress     func(protocol.Phase, int)
}

// UpdateResult of a run.
type UpdateResult struct {
	Code    protocol.Code
	Detail  string
	Changed []string // services moved to their rebuild, "<project>/<service>"
	Log     []byte   // what Compose said
}

const (
	// spaceFactor and spaceMargin: the free space an update needs beside the images on the host - the download, and
	// the unpacked layers, which take about three times as much again.
	spaceFactor = 4
	spaceMargin = 256 << 20
	// minCompose: the first Compose release with "up --pull" (major, minor).
	minComposeMajor, minComposeMinor = 2, 22
)

// outcome of one service.
type outcome int

const (
	outDone    outcome = iota // moved to its rebuild
	outNone                   // nothing to move to
	outTooNew                 // a rebuild, too new yet
	outUnknown                // the registry gave no answer
	outSkip                   // refused before any change: the service stays as it is, the code says why
	outStop                   // failed: the run ends with its code
)

// Update carries out an image scope: one service, or every service the policy allows that has a rebuild.
func (u *Updater) Update(ctx context.Context, scope protocol.Scope, h UpdateHooks) UpdateResult {
	if h.Progress == nil {
		h.Progress = func(protocol.Phase, int) {}
	}
	h.Progress(protocol.PhasePrechecking, 5)
	if ctx.Err() != nil {
		return UpdateResult{Code: protocol.CodeTimeout, Detail: "the run's time was up before it began"}
	}
	if code, detail := u.composeReady(ctx); code != "" {
		return UpdateResult{Code: code, Detail: detail}
	}
	var services []Service
	if scope.Kind == protocol.ScopeImage {
		s := Service{Project: scope.Project, Service: scope.Service}
		if !u.Allowed(s) {
			return UpdateResult{Code: protocol.CodeRejectedScopeNotAllowed}
		}
		services = []Service{s}
	} else {
		all, ok := u.Engine.Containers(ctx)
		if !ok {
			return UpdateResult{Code: protocol.CodeFailedPackageManager, Detail: "the container engine did not answer"}
		}
		services = u.candidates(all)
	}
	run := &updateRun{u: u, h: h}
	if scope.Kind == protocol.ScopeImage {
		out, code, detail := run.one(ctx, services[0])
		res := UpdateResult{Code: code, Detail: detail, Log: run.log}
		if out == outDone {
			res.Code, res.Changed = protocol.CodeOK, []string{services[0].Name()}
		}
		return res
	}
	res := UpdateResult{Code: protocol.CodeOK}
	var tooNew, unknown, skipped []string
	short := false // a service was refused for lack of space
	for i, s := range services {
		if ctx.Err() != nil {
			res.Code, res.Detail = protocol.CodeTimeout, "the run's time was up before "+s.Name()
			break
		}
		run.span = [2]int{5 + 90*i/len(services), 5 + 90*(i+1)/len(services)}
		out, code, detail := run.one(ctx, s)
		switch out {
		case outDone:
			res.Changed = append(res.Changed, s.Name())
		case outTooNew:
			tooNew = append(tooNew, s.Name())
		case outUnknown:
			unknown = append(unknown, s.Name())
		case outSkip:
			skipped = append(skipped, detail)
			short = short || code == protocol.CodePrecheckDiskSpace
		case outStop:
			res.Code, res.Detail = code, detail
		}
		if out == outStop {
			break
		}
	}
	res.Log = run.log
	if res.Code == protocol.CodeOK && len(res.Changed) == 0 {
		switch {
		case short:
			res.Code = protocol.CodePrecheckDiskSpace
		case len(unknown) > 0:
			res.Code = protocol.CodePrecheckRepoUnreachable
		}
	}
	var notes []string
	for _, n := range []struct {
		what  string
		names []string
	}{{"updated", res.Changed}, {"too new", tooNew}, {"no answer from the registry", unknown},
		{"not updated", skipped}} {
		if len(n.names) > 0 {
			notes = append(notes, n.what+": "+strings.Join(n.names, ", "))
		}
	}
	if res.Detail != "" {
		notes = append(notes, res.Detail)
	}
	res.Detail = strings.Join(notes, "; ")
	return res
}

// candidates: the services of the running containers the policy allows.
func (u *Updater) candidates(all []Container) []Service {
	var out []Service
	for _, c := range all {
		if c.Compose && c.State == "running" && u.Allowed(c.Service) && !slices.Contains(out, c.Service) {
			out = append(out, c.Service)
		}
	}
	slices.SortFunc(out, func(a, b Service) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

// env: Compose talks to the engine the agent reads and writes, whatever Docker context root has.
func (u *Updater) env() []string { return []string{"DOCKER_HOST=unix://" + u.Engine.Socket} }

// composeReady checks that Compose is there and new enough for "up --pull never".
func (u *Updater) composeReady(ctx context.Context) (protocol.Code, string) {
	res := u.Compose.Run(ctx, u.env(), "docker", "compose", "version", "--short")
	if !res.Ran() || res.ExitCode != 0 {
		return protocol.CodeRejectedUnsupported, "docker compose is not available on this host"
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		major, e1 := strconv.Atoi(parts[0])
		minor, e2 := strconv.Atoi(parts[1])
		if e1 == nil && e2 == nil && (major > minComposeMajor || major == minComposeMajor && minor >= minComposeMinor) {
			return "", ""
		}
	}
	return protocol.CodeRejectedUnsupported, fmt.Sprintf("docker compose %q is older than %d.%d", v, minComposeMajor,
		minComposeMinor)
}

// updateRun is the state of one run across its services.
type updateRun struct {
	u       *Updater
	h       UpdateHooks
	changed bool // the journal was written
	log     []byte
	span    [2]int // progress of the current service, from and to percent
}

func (r *updateRun) progress(phase protocol.Phase, part int) {
	r.h.Progress(phase, r.span[0]+(r.span[1]-r.span[0])*part/100)
}

// one moves one service. Every check comes before the first change; once something changed, the rest runs on its
// own budget, and a failure goes back.
func (r *updateRun) one(ctx context.Context, s Service) (outcome, protocol.Code, string) {
	u := r.u
	all, ok := u.Engine.Containers(ctx)
	if !ok {
		return outStop, protocol.CodeFailedPackageManager, "the container engine did not answer"
	}
	t, why, ok := u.Engine.movable(ctx, s, all)
	switch {
	case !ok:
		return outStop, protocol.CodeFailedPackageManager, "the container engine did not answer about " + s.Name()
	case why != "":
		return outSkip, protocol.CodeRejectedScopeNotAllowed, s.Name() + ": " + why
	}
	if why := r.drifted(ctx, t); why != "" {
		return outSkip, protocol.CodeRejectedScopeNotAllowed, s.Name() + ": " + why
	}
	rb := u.Reg.Rebuild(ctx, t.image, t.ref, u.Now(), u.MinAge)
	switch rb.State {
	case RebuildUnknown:
		return outUnknown, protocol.CodePrecheckRepoUnreachable, s.Name() + ": no answer from the registry about " +
			t.image.Reference
	case RebuildNone:
		return outNone, protocol.CodeOK, s.Name() + ": " + t.image.Reference + " has no rebuild"
	case RebuildTooNew:
		return outTooNew, protocol.CodePrecheckVersionTooNew, fmt.Sprintf("%s: the rebuild of %s from %s is younger "+
			"than %d days", s.Name(), t.image.Reference, rb.Created.UTC().Format(time.DateOnly), int(u.MinAge.Hours()/24))
	}
	if code, detail := u.space(ctx, rb.Size); code != "" {
		return outSkip, code, s.Name() + ": " + detail
	}
	r.progress(protocol.PhaseDownloading, 10)
	repo, pinned := t.ref.Name(), t.ref.Name()+"@"+rb.Digest
	newID, why, ok := u.Engine.Pull(ctx, repo, rb.Digest, t.image.Platform)
	if !ok {
		return outStop, protocol.CodeFailedPackageManager, "pull of " + pinned + ": " + why
	}
	// From here on something changes: finished or undone, also when the run's time is up or the agent stops.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), u.ChangeBudget)
	defer cancel()
	if !r.changed && r.h.BeforeChange != nil {
		if err := r.h.BeforeChange(); err != nil {
			return outStop, protocol.CodeFailedPackageManager, err.Error()
		}
	}
	r.changed = true
	oldID, keep := t.image.ImageID, KeepTag(s)
	img, found, ok := u.Engine.Image(wctx, repo+":"+keep)
	if !ok {
		return outStop, protocol.CodeFailedPackageManager, s.Name() + ": the container engine did not answer about " + keep
	}
	older := ""
	if found && img.ID != oldID {
		older = img.ID // the way back of the update before; it goes once this one has proved itself
	}
	if !u.Engine.Tag(wctx, oldID, repo, keep) {
		return outStop, protocol.CodeFailedPackageManager, s.Name() + ": the running image could not be kept as " + keep
	}
	if !u.Engine.Tag(wctx, newID, repo, t.ref.Tag) {
		r.restoreKeep(wctx, repo, keep, older)
		return outStop, protocol.CodeFailedPackageManager, s.Name() + ": the new build could not be tagged " + t.ref.Tag
	}
	r.progress(protocol.PhaseInstalling, 50)
	if !r.up(wctx, t) {
		back := r.rollback(wctx, t, oldID, newID, older)
		return outStop, protocol.CodeFailedPackageManager, s.Name() + ": docker compose up failed; " + back
	}
	r.progress(protocol.PhaseVerifying, 80)
	failed := u.settled(wctx, s, newID)
	if failed == "" && r.h.Health != nil {
		if why := r.h.Health(wctx); why != "" {
			failed = "health: " + why
		}
	}
	if failed != "" {
		back := r.rollback(wctx, t, oldID, newID, older)
		return outStop, protocol.CodeFailedHealth, s.Name() + ": " + failed + "; " + back
	}
	if older != "" {
		u.removeUnheld(wctx, older)
	}
	return outDone, protocol.CodeOK, ""
}

// composeArgs: Compose with the project's own name, directory and files, as its labels recorded them.
func composeArgs(t target) []string {
	args := []string{"compose", "--project-name", t.s.Project, "--project-directory", t.ctr.WorkingDir}
	for _, f := range t.ctr.ConfigFiles {
		args = append(args, "--file", f)
	}
	for _, f := range t.ctr.EnvFiles {
		args = append(args, "--env-file", f)
	}
	return args
}

// drifted says why Compose would not recreate the service as it runs now: its files or their variables changed since
// it was created (another configuration hash), so "up" would apply edits nobody checked; "" when it hashes the same.
func (r *updateRun) drifted(ctx context.Context, t target) string {
	res := r.u.Compose.Run(ctx, r.u.env(), "docker", append(composeArgs(t), "config", "--hash", t.s.Service)...)
	if !res.Ran() || res.ExitCode != 0 {
		return "docker compose cannot read the project's files"
	}
	if strings.TrimSpace(string(res.Stdout)) != t.s.Service+" "+t.ctr.ConfigHash {
		return "its Compose files or their variables changed since it was created; bring it up once by hand"
	}
	return ""
}

// space checks that the new build fits beside the images on the host; a free space that cannot be read does not.
func (u *Updater) space(ctx context.Context, size int64) (protocol.Code, string) {
	dirs, ok := u.Engine.DataDirs(ctx)
	if !ok {
		return protocol.CodePrecheckDiskSpace, "the container engine did not say where it keeps images"
	}
	need := uint64(size)*spaceFactor + spaceMargin
	for _, d := range dirs {
		free, known := u.Free(d)
		switch {
		case !known:
			return protocol.CodePrecheckDiskSpace, "the free space of " + d + " cannot be read"
		case free < need:
			return protocol.CodePrecheckDiskSpace, fmt.Sprintf("%s: %d MiB free, the new image needs about %d MiB",
				d, free>>20, need>>20)
		}
	}
	return "", ""
}

// up lets Compose recreate the service from its project's files: no dependencies, no build, no pull - the image is
// the one the agent just named with the tag.
func (r *updateRun) up(ctx context.Context, t target) bool {
	args := append(composeArgs(t), "up", "--detach", "--no-deps", "--no-build", "--pull", "never", t.s.Service)
	res := r.u.Compose.Run(ctx, r.u.env(), "docker", args...)
	r.log = append(r.log, fmt.Sprintf("$ docker %s\n", strings.Join(args, " "))...)
	r.log = append(append(r.log, res.Stdout...), res.Stderr...)
	return res.Ran() && res.ExitCode == 0
}

// settled waits until the service's container runs image id - healthy where it has a health check - and keeps running
// for Stable without a restart; "" then, else what it saw last. A container that starts and dies, as one that misses
// a new setting does, never gets there.
func (u *Updater) settled(ctx context.Context, s Service, id string) string {
	deadline := time.Now().Add(u.Settle) // wall time; Now is the clock the images' age is judged by
	var since time.Time
	seen := ""
	for {
		why, mark := u.look(ctx, s, id)
		now := time.Now()
		switch {
		case why != "":
			since, seen = time.Time{}, ""
		case mark != seen: // the first clean look, or it restarted since
			since, seen = now, mark
		case now.Sub(since) >= u.Stable:
			return ""
		}
		if !now.Before(deadline) || ctx.Err() != nil {
			if why == "" {
				why = fmt.Sprintf("it did not run %s without a restart", u.Stable)
			}
			return why
		}
		select {
		case <-ctx.Done():
		case <-time.After(u.Every):
		}
	}
}

// look: why the service's container is not (yet) running image id as it should, and a mark that changes with every
// start of it.
func (u *Updater) look(ctx context.Context, s Service, id string) (why, mark string) {
	all, ok := u.Engine.Containers(ctx)
	if !ok {
		return "the container engine did not answer", ""
	}
	for _, c := range all {
		if !c.Compose || c.Service != s || c.ImageID != id {
			continue
		}
		switch {
		case c.State != "running":
			return c.Name + ": " + c.State, ""
		case c.Health != "" && c.Health != "healthy":
			return c.Name + ": " + c.Health, ""
		}
		return "", c.ID + " " + c.StartedAt + " " + strconv.Itoa(c.RestartCount)
	}
	return "no container of the service runs the image", ""
}

// rollback gives the old image the tag again and lets Compose recreate the service once more; it says how that went.
// The keep tag stays on the old image until the old image carries the Compose file's tag again: taking off its only
// name could let the engine delete the way back.
func (r *updateRun) rollback(ctx context.Context, t target, oldID, newID, older string) string {
	u := r.u
	repo, keep := t.ref.Name(), KeepTag(t.s)
	if !u.Engine.Tag(ctx, oldID, repo, t.ref.Tag) {
		return "rolling back failed: the old image could not be tagged " + t.ref.Tag + "; it stays as " + keep
	}
	r.restoreKeep(ctx, repo, keep, older)
	if !r.up(ctx, t) {
		u.removeUnheld(ctx, newID)
		return "rolling back failed: docker compose up failed"
	}
	why := u.settled(ctx, t.s, oldID)
	u.removeUnheld(ctx, newID)
	if why != "" {
		return "rolled back, but " + why
	}
	return "rolled back"
}

// restoreKeep gives the way back of the update before its tag again, or takes the tag off when there was none.
func (r *updateRun) restoreKeep(ctx context.Context, repo, keep, older string) {
	if older != "" {
		r.u.Engine.Tag(ctx, older, repo, keep)
		return
	}
	r.u.Engine.Untag(ctx, repo, keep)
}

// removeUnheld removes an image that no name and no container holds any more.
func (u *Updater) removeUnheld(ctx context.Context, id string) {
	img, found, ok := u.Engine.Image(ctx, id)
	if !ok || !found || len(img.RepoTags) > 0 {
		return
	}
	if used, ok := u.Engine.InUse(ctx, id); ok && !used {
		u.Engine.Remove(ctx, id)
	}
}
