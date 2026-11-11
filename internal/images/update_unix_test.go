//go:build !windows

package images

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// fakeDocker is an engine with images, names and containers, answering the calls the agent makes as the Docker
// Engine API does. Writes change its state the way the engine would - deleting the last name of an image no container
// uses deletes the image; every call is recorded.
type fakeDocker struct {
	mu          sync.Mutex
	calls       []string           // "METHOD /path k=v&k=v", values decoded, keys sorted
	images      map[string]*fImage // id -> image
	tags        map[string]string  // "repo:tag" -> image id
	ctrs        []*fCtr
	pull        map[string]*fImage // "name@digest" -> the image a pull of it brings
	pullError   string             // a pull ends with this errorDetail
	unhealthy   map[string]bool    // containers created from this image id end up unhealthy
	crashing    map[string]bool    // containers created from this image id die and are restarted, again and again
	classic     bool               // the classic image store: no containerd data directory
	root        string
	beforeWrite func(call string) // every write, before it applies
}

type fImage struct {
	id          string
	repoDigests []string
	created     time.Time
	platform    Platform
	labels      map[string]string
}

type fCtr struct {
	id, name, ref, image, state, health string
	labels                              map[string]string
	started                             int // StartedAt, as a counter
	restarts                            int
}

func call(r *http.Request) string {
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+q.Get(k))
	}
	return strings.TrimSpace(r.Method + " " + r.URL.Path + " " + strings.Join(parts, "&"))
}

func (d *fakeDocker) resolve(ref string) *fImage {
	if img := d.images[ref]; img != nil {
		return img
	}
	if id, ok := d.tags[ref]; ok {
		return d.images[id]
	}
	for _, img := range d.images {
		if slices.Contains(img.repoDigests, ref) {
			return img
		}
	}
	return nil
}

func (d *fakeDocker) tagsOf(id string) []string {
	out := []string{}
	for t, i := range d.tags {
		if i == id {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func (d *fakeDocker) used(id string) bool {
	return slices.ContainsFunc(d.ctrs, func(c *fCtr) bool { return c.image == id })
}

func (d *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := call(r)
	d.calls = append(d.calls, c)
	if r.Method != http.MethodGet && d.beforeWrite != nil {
		d.beforeWrite(c)
	}
	p := r.URL.Path
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == http.MethodGet && p == "/containers/json":
		all := r.URL.Query().Get("all") == "1"
		list := []map[string]string{}
		for _, c := range d.ctrs {
			if all || c.state == "running" {
				list = append(list, map[string]string{"Id": c.id, "ImageID": c.image})
			}
		}
		send(list)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/containers/"), "/json")
		i := slices.IndexFunc(d.ctrs, func(c *fCtr) bool { return c.id == id })
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		c := d.ctrs[i]
		if d.crashing[c.image] && c.state == "running" { // it died since the last look and was started again
			c.restarts++
			c.started++
		}
		state := map[string]any{"Status": c.state, "StartedAt": fmt.Sprintf("2026-11-06T10:00:%02dZ", c.started%60)}
		if c.health != "" {
			state["Health"] = map[string]string{"Status": c.health}
		}
		send(map[string]any{"Id": c.id, "Name": "/" + c.name, "Image": c.image, "State": state,
			"RestartCount": c.restarts, "Config": map[string]any{"Image": c.ref, "Labels": c.labels}})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		img := d.resolve(strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/json"))
		if img == nil {
			http.NotFound(w, r)
			return
		}
		send(map[string]any{"Id": img.id, "RepoDigests": img.repoDigests, "RepoTags": d.tagsOf(img.id),
			"Created": img.created, "Os": img.platform.OS, "Architecture": img.platform.Architecture,
			"Variant": img.platform.Variant, "Config": map[string]any{"Labels": img.labels}})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/tag"):
		img := d.resolve(strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/tag"))
		if img == nil {
			http.NotFound(w, r)
			return
		}
		d.tags[r.URL.Query().Get("repo")+":"+r.URL.Query().Get("tag")] = img.id
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && p == "/images/create":
		key := r.URL.Query().Get("fromImage") + "@" + r.URL.Query().Get("tag")
		send(map[string]string{"status": "Pulling from " + key})
		img := d.pull[key]
		switch {
		case d.pullError != "":
			send(map[string]any{"errorDetail": map[string]string{"message": d.pullError}, "error": d.pullError})
		case img == nil || r.URL.Query().Get("platform") != img.platform.String():
			send(map[string]any{"errorDetail": map[string]string{"message": "manifest unknown"}})
		default:
			d.images[img.id] = img
			send(map[string]string{"status": "Downloaded newer image for " + key})
		}
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/images/"):
		ref := strings.TrimPrefix(p, "/images/")
		if id, ok := d.tags[ref]; ok {
			delete(d.tags, ref)
			if len(d.tagsOf(id)) == 0 && !d.used(id) { // the last name of an unused image: the image goes too
				delete(d.images, id)
			}
			send([]map[string]string{{"Untagged": ref}})
			return
		}
		img := d.images[ref]
		switch {
		case img == nil:
			http.NotFound(w, r)
		case len(d.tagsOf(ref)) > 0 || d.used(ref):
			w.WriteHeader(http.StatusConflict)
		default:
			delete(d.images, ref)
			send([]map[string]string{{"Deleted": ref}})
		}
	case r.Method == http.MethodGet && p == "/info":
		info := map[string]any{"DockerRootDir": d.root}
		if !d.classic {
			info["DriverStatus"] = [][]string{{"driver-type", "io.containerd.snapshotter.v1"}}
		}
		send(info)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// writes: the calls that change the engine.
func (d *fakeDocker) writes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

// fakeCompose answers "docker compose version" and "config --hash", and for "up" recreates the service's containers
// from the image its reference's tag names now - as Compose does without pulling.
type fakeCompose struct {
	d       *fakeDocker
	version string            // "": Compose is not installed
	hashes  map[string]string // "<project>/<service>" -> what config --hash says; default: the container's label
	failUps int               // so many ups fail first
	env     [][]string
	calls   [][]string
}

func (c *fakeCompose) Run(_ context.Context, env []string, name string, args ...string) runner.Result {
	c.calls = append(c.calls, append([]string{name}, args...))
	c.env = append(c.env, env)
	if name != "docker" || len(args) == 0 || args[0] != "compose" {
		return runner.Result{ExitCode: 127}
	}
	if slices.Equal(args, []string{"compose", "version", "--short"}) {
		if c.version == "" {
			return runner.Result{ExitCode: 1, Stderr: []byte("docker: unknown command: docker compose")}
		}
		return runner.Result{Stdout: []byte(c.version + "\n")}
	}
	project, service := args[slices.Index(args, "--project-name")+1], args[len(args)-1]
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	if slices.Contains(args, "config") {
		h, ok := c.hashes[project+"/"+service]
		if !ok {
			for _, ctr := range c.d.ctrs {
				if ctr.labels[composeProjectLabel] == project && ctr.labels[labelService] == service {
					h = ctr.labels[labelConfigHash]
				}
			}
		}
		return runner.Result{Stdout: []byte(service + " " + h + "\n")}
	}
	if c.failUps > 0 {
		c.failUps--
		return runner.Result{ExitCode: 1, Stderr: []byte("Error response from daemon: something")}
	}
	for _, ctr := range c.d.ctrs {
		if ctr.labels[composeProjectLabel] != project || ctr.labels[labelService] != service {
			continue
		}
		ref, _ := ParseRef(ctr.ref)
		ctr.image, ctr.state, ctr.restarts = c.d.tags[ref.Name()+":"+ref.Tag], "running", 0
		ctr.started++
		if ctr.health != "" {
			ctr.health = "healthy"
			if c.d.unhealthy[ctr.image] {
				ctr.health = "unhealthy"
			}
		}
	}
	return runner.Result{Stdout: []byte(" Container " + project + "-" + service + "-1 Recreated\n")}
}

func (c *fakeCompose) ups() [][]string {
	var out [][]string
	for _, a := range c.calls {
		if slices.Contains(a, "up") {
			out = append(out, a)
		}
	}
	return out
}

type updFixture struct {
	f      *fakeRegistry
	d      *fakeDocker
	c      *fakeCompose
	u      *Updater
	dir    string
	free   map[string]int64 // -1: cannot be read
	events []string         // journal and engine writes, in order
	mu     sync.Mutex
}

func newUpdFixture(t *testing.T) *updFixture {
	was := containerdRoot
	containerdRoot = t.TempDir()
	t.Cleanup(func() { containerdRoot = was })
	fx := &updFixture{f: newFakeRegistry(t), free: map[string]int64{}}
	fx.d = &fakeDocker{images: map[string]*fImage{}, tags: map[string]string{}, pull: map[string]*fImage{},
		unhealthy: map[string]bool{}, crashing: map[string]bool{}, root: "/var/lib/docker",
		beforeWrite: func(c string) { fx.note("write " + c) }}
	fx.c = &fakeCompose{d: fx.d, version: "2.29.7", hashes: map[string]string{}}
	fx.dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(fx.dir, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.u = &Updater{Engine: NewEngine(startEngine(t, fx.d)), Reg: &Registry{Client: fx.f.srv.Client()}, Compose: fx.c,
		Now: func() time.Time { return now }, MinAge: 7 * 24 * time.Hour, Settle: 500 * time.Millisecond,
		Stable: 20 * time.Millisecond, Every: 2 * time.Millisecond, ChangeBudget: time.Minute,
		Allowed: func(s Service) bool { return s.Project == "stack" },
		Free: func(p string) (uint64, bool) {
			if v, ok := fx.free[p]; ok {
				return uint64(max(v, 0)), v >= 0
			}
			return 1 << 40, true
		}}
	return fx
}

func (fx *updFixture) note(e string) {
	fx.mu.Lock()
	fx.events = append(fx.events, e)
	fx.mu.Unlock()
}

func (fx *updFixture) labels(project, service string) map[string]string {
	return map[string]string{composeProjectLabel: project, labelService: service, labelWorkingDir: fx.dir,
		labelConfigFiles: filepath.Join(fx.dir, "compose.yaml"), labelConfigHash: "hash-" + service,
		labelOneoff: "False"}
}

// service starts a container of project/service running repo:tag built at old; with rebuilt set, the registry's tag
// points to a build of that time since, which a pull brings. It returns the old and the new image id.
func (fx *updFixture) service(project, service, repo, tag string, old, rebuilt time.Time) (string, string) {
	oldDigest, oldCfg := fx.f.push(repo, tag, []build{{amd64, old, "old " + repo}}, false)
	id, name := oldCfg[amd64.String()], fx.f.host()+"/"+repo
	fx.d.images[id] = &fImage{id: id, repoDigests: []string{name + "@" + oldDigest}, created: old, platform: amd64}
	fx.d.tags[name+":"+tag] = id
	fx.d.ctrs = append(fx.d.ctrs, &fCtr{id: strings.Repeat(fmt.Sprintf("%x", len(fx.d.ctrs)+10), 64)[:64],
		name: project + "-" + service + "-1", ref: name + ":" + tag, image: id, state: "running",
		labels: fx.labels(project, service)})
	if rebuilt.IsZero() {
		return id, ""
	}
	newDigest, newCfg := fx.f.push(repo, tag, []build{{amd64, rebuilt, "new " + repo}}, false)
	nid := newCfg[amd64.String()]
	fx.d.pull[name+"@"+newDigest] = &fImage{id: nid, repoDigests: []string{name + "@" + newDigest}, created: rebuilt,
		platform: amd64}
	return id, nid
}

func (fx *updFixture) run(scope string) UpdateResult {
	return fx.runWith(context.Background(), scope, nil)
}

func (fx *updFixture) runWith(ctx context.Context, scope string, health func(context.Context) string) UpdateResult {
	sc, ok := protocol.ParseScope(scope)
	if !ok {
		panic(scope)
	}
	return fx.u.Update(ctx, sc, UpdateHooks{Health: health, BeforeChange: func() error {
		fx.note("journal")
		return nil
	}})
}

func (fx *updFixture) ctr(name string) *fCtr {
	for _, c := range fx.d.ctrs {
		if c.name == name {
			return c
		}
	}
	return nil
}

func index(list []string, prefix string) int {
	return slices.IndexFunc(list, func(s string) bool { return strings.HasPrefix(s, prefix) })
}

// The way there: the build the registry names now, pulled by its digest for the host's platform, the running image
// kept under a tag of its own, the Compose file's tag moved, the service recreated without pull or build, against the
// agent's own engine. The journal comes before the first change, the pull before it changes nothing that runs; the way
// back of an update before goes once this one runs steadily.
func TestImageUpdateMovesToTheRebuild(t *testing.T) {
	fx := newUpdFixture(t)
	oldID, newID := fx.service("stack", "web", "org/web", "10", days(30), days(10))
	older := "sha256:" + strings.Repeat("7", 64) // an update before left its way back; nothing else holds it
	repo := fx.f.host() + "/org/web"
	fx.d.images[older] = &fImage{id: older, platform: amd64}
	fx.d.tags[repo+":vordr-previous.stack.web"] = older

	res := fx.run("image:stack/web")
	if res.Code != protocol.CodeOK || !slices.Equal(res.Changed, []string{"stack/web"}) {
		t.Fatalf("result %+v", res)
	}
	if c := fx.ctr("stack-web-1"); c.image != newID || c.state != "running" {
		t.Fatalf("container %+v, want image %s", c, newID)
	}
	if fx.d.tags[repo+":10"] != newID || fx.d.tags[repo+":vordr-previous.stack.web"] != oldID {
		t.Fatalf("tags %v", fx.d.tags)
	}
	if _, kept := fx.d.images[older]; kept {
		t.Error("the way back of the update before is still there")
	}
	ev := fx.events
	pull, journal := index(ev, "write POST /images/create"), index(ev, "journal")
	keep := index(ev, "write POST /images/"+oldID+"/tag")
	if pull < 0 || journal < pull || keep < journal || slices.Index(ev[journal+1:], "journal") >= 0 {
		t.Fatalf("order pull %d, journal %d, keep %d: %v", pull, journal, keep, ev)
	}
	newDigest := strings.TrimPrefix(fx.d.images[newID].repoDigests[0], repo+"@")
	want := []string{
		"POST /images/create fromImage=" + repo + "&platform=linux/amd64&tag=" + newDigest,
		"POST /images/" + oldID + "/tag repo=" + repo + "&tag=vordr-previous.stack.web",
		"POST /images/" + newID + "/tag repo=" + repo + "&tag=10",
		"DELETE /images/" + older,
	}
	if got := fx.d.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	base := []string{"docker", "compose", "--project-name", "stack", "--project-directory", fx.dir, "--file",
		filepath.Join(fx.dir, "compose.yaml")}
	ups := fx.c.ups()
	if len(ups) != 1 || !slices.Equal(ups[0], append(slices.Clone(base), "up", "--detach", "--no-deps", "--no-build",
		"--pull", "never", "web")) {
		t.Fatalf("compose %q", ups)
	}
	if !slices.ContainsFunc(fx.c.calls, func(a []string) bool {
		return slices.Equal(a, append(slices.Clone(base), "config", "--hash", "web"))
	}) {
		t.Errorf("the configuration was not compared: %q", fx.c.calls)
	}
	for _, env := range fx.c.env {
		if !slices.Equal(env, []string{"DOCKER_HOST=unix://" + fx.u.Engine.Socket}) {
			t.Fatalf("Compose ran against %v, not the agent's engine", env)
		}
	}
	if !strings.Contains(string(res.Log), "Recreated") {
		t.Errorf("log %q", res.Log)
	}
	// Run again: the tag points to the running build now, nothing to do and nothing written.
	before := len(fx.d.writes())
	if res := fx.run("image:stack/web"); res.Code != protocol.CodeOK || len(res.Changed) != 0 ||
		len(fx.d.writes()) != before {
		t.Fatalf("second run %+v, writes %v", res, fx.d.writes()[before:])
	}
}

// A stopped helper of the same project on the same tag - a one-shot service that sets permissions before the service
// starts - does not hold the service back (ADR-0123): the service moves, the helper keeps its image by id and stays
// stopped, Compose recreates only the service, and the inventory offers it. Created and dead count as stopped too.
func TestImageUpdateStoppedHelperOfTheProject(t *testing.T) {
	for _, state := range []string{"exited", "created", "dead"} {
		t.Run(state, func(t *testing.T) {
			fx := newUpdFixture(t)
			oldID, newID := fx.service("stack", "web", "org/web", "10", days(30), days(10))
			helper := *fx.ctr("stack-web-1")
			helper.id, helper.name, helper.state = strings.Repeat("e", 64), "stack-fix-permissions-1", state
			helper.labels = fx.labels("stack", "fix-permissions")
			fx.d.ctrs = append(fx.d.ctrs, &helper)

			if why := fx.u.Engine.Movable(context.Background())(Service{"stack", "web"}); why != "" {
				t.Fatalf("not offered: %s", why)
			}
			res := fx.run("image:stack/web")
			if res.Code != protocol.CodeOK || !slices.Equal(res.Changed, []string{"stack/web"}) {
				t.Fatalf("result %+v", res)
			}
			if c := fx.ctr("stack-web-1"); c.image != newID {
				t.Errorf("service on %s, want %s", c.image, newID)
			}
			if h := fx.ctr("stack-fix-permissions-1"); h.image != oldID || h.state != state {
				t.Errorf("helper %+v, want image %s, %s", h, oldID, state)
			}
			if ups := fx.c.ups(); len(ups) != 1 || ups[0][len(ups[0])-1] != "web" || !slices.Contains(ups[0], "--no-deps") {
				t.Errorf("compose %q", ups)
			}
		})
	}
}

// Every refusal comes before the engine is written to (ADR-0119, confirmation): not allowed, pinned, not from Compose,
// built here, scaled, a shared tag, the project's files gone or not absolute, a configuration changed since, too new,
// no answer, too little or unknown space, no Compose.
func TestImageUpdateRefusesWithoutWriting(t *testing.T) {
	web := func(fx *updFixture) { fx.service("stack", "web", "org/web", "10", days(30), days(10)) }
	cases := map[string]struct {
		setup func(fx *updFixture)
		code  protocol.Code
		in    string // in the detail
	}{
		"not allowed": {func(fx *updFixture) {
			fx.service("data", "app", "org/app", "5", days(30), days(10))
			fx.ctr("data-app-1").labels[composeProjectLabel] = "data"
		}, protocol.CodeRejectedScopeNotAllowed, ""},
		"pinned": {func(fx *updFixture) {
			web(fx)
			c := fx.ctr("stack-web-1")
			c.ref += "@" + strings.TrimPrefix(fx.d.images[c.image].repoDigests[0], fx.f.host()+"/org/web@")
		}, protocol.CodeRejectedScopeNotAllowed, "pins a digest"},
		"not from Compose": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").labels = nil
		}, protocol.CodeRejectedScopeNotAllowed, "no container"},
		"a run container": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").labels[labelOneoff] = "True"
		}, protocol.CodeRejectedScopeNotAllowed, "no container"},
		"stopped": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").state = "exited"
		}, protocol.CodeRejectedScopeNotAllowed, "is exited"},
		"built here": {func(fx *updFixture) {
			old, _ := fx.service("stack", "web", "org/web", "10", days(30), days(10))
			fx.d.images[old].labels = map[string]string{composeProjectLabel: "stack"}
		}, protocol.CodeRejectedScopeNotAllowed, "built on this host"},
		"scaled": {func(fx *updFixture) {
			web(fx)
			second := *fx.ctr("stack-web-1")
			second.id, second.name = strings.Repeat("e", 64), "stack-web-2"
			fx.d.ctrs = append(fx.d.ctrs, &second)
		}, protocol.CodeRejectedScopeNotAllowed, "has 2 containers"},
		"shared tag": {func(fx *updFixture) {
			web(fx)
			other := *fx.ctr("stack-web-1")
			other.id, other.name, other.labels = strings.Repeat("e", 64), "standalone", nil // docker run, same tag
			fx.d.ctrs = append(fx.d.ctrs, &other)
		}, protocol.CodeRejectedScopeNotAllowed, "also used by the container standalone"},
		// ADR-0123: only a stopped container of the same project does not count.
		"shared tag stopped in another project": {func(fx *updFixture) {
			web(fx)
			other := *fx.ctr("stack-web-1")
			other.id, other.name, other.state = strings.Repeat("e", 64), "site-init-1", "exited"
			other.labels = fx.labels("site", "init")
			fx.d.ctrs = append(fx.d.ctrs, &other)
		}, protocol.CodeRejectedScopeNotAllowed, "also used by the container site-init-1"},
		"shared tag stopped one-off of the project": {func(fx *updFixture) {
			web(fx)
			other := *fx.ctr("stack-web-1")
			other.id, other.name, other.state = strings.Repeat("e", 64), "stack-web-run-1", "exited"
			other.labels = fx.labels("stack", "web")
			other.labels[labelOneoff] = "True" // docker compose run: no service of the project
			fx.d.ctrs = append(fx.d.ctrs, &other)
		}, protocol.CodeRejectedScopeNotAllowed, "also used by the container stack-web-run-1"},
		"shared tag running in the project": {func(fx *updFixture) {
			web(fx)
			other := *fx.ctr("stack-web-1")
			other.id, other.name, other.labels = strings.Repeat("e", 64), "stack-sidecar-1", fx.labels("stack", "sidecar")
			fx.d.ctrs = append(fx.d.ctrs, &other)
		}, protocol.CodeRejectedScopeNotAllowed, "also used by the container stack-sidecar-1"},
		"shared tag restarting in the project": {func(fx *updFixture) {
			web(fx)
			other := *fx.ctr("stack-web-1")
			other.id, other.name, other.state = strings.Repeat("e", 64), "stack-sidecar-1", "restarting"
			other.labels = fx.labels("stack", "sidecar")
			fx.d.ctrs = append(fx.d.ctrs, &other)
		}, protocol.CodeRejectedScopeNotAllowed, "also used by the container stack-sidecar-1"},
		"project file gone": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").labels[labelConfigFiles] = filepath.Join(fx.dir, "gone.yaml")
		}, protocol.CodeRejectedScopeNotAllowed, "gone.yaml is gone"},
		"relative project dir": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").labels[labelWorkingDir] = "srv/stack"
		}, protocol.CodeRejectedScopeNotAllowed, "is gone"},
		"option in the files label": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").labels[labelConfigFiles] = "--project-directory=/,-f"
		}, protocol.CodeRejectedScopeNotAllowed, "--project-directory=/ is gone"},
		"option in the env label": {func(fx *updFixture) {
			web(fx)
			fx.ctr("stack-web-1").labels[labelEnvFiles] = "--env-file=/etc/shadow"
		}, protocol.CodeRejectedScopeNotAllowed, "is gone"},
		"no config hash": {func(fx *updFixture) {
			web(fx)
			delete(fx.ctr("stack-web-1").labels, labelConfigHash)
		}, protocol.CodeRejectedScopeNotAllowed, "lacks the labels"},
		"configuration changed": {func(fx *updFixture) {
			web(fx)
			fx.c.hashes["stack/web"] = "hash-edited"
		}, protocol.CodeRejectedScopeNotAllowed, "changed since it was created"},
		"too new": {func(fx *updFixture) {
			fx.service("stack", "web", "org/web", "10", days(30), days(2))
		}, protocol.CodePrecheckVersionTooNew, "younger than 7 days"},
		"no answer": {func(fx *updFixture) {
			web(fx)
			fx.f.limited["org/web"] = true
		}, protocol.CodePrecheckRepoUnreachable, "no answer"},
		"too little space": {func(fx *updFixture) {
			fx.f.layers["org/web"] = []int64{300 << 20, 200 << 20}
			web(fx)
			fx.free[containerdRoot] = 2 << 30 // 500 MiB download needs about 2.2 GiB
		}, protocol.CodePrecheckDiskSpace, "MiB free"},
		"space unknown": {func(fx *updFixture) {
			web(fx)
			fx.free["/var/lib/docker"] = -1
		}, protocol.CodePrecheckDiskSpace, "cannot be read"},
		"no compose": {func(fx *updFixture) {
			web(fx)
			fx.c.version = ""
		}, protocol.CodeRejectedUnsupported, "not available"},
		"old compose": {func(fx *updFixture) {
			web(fx)
			fx.c.version = "2.21.0"
		}, protocol.CodeRejectedUnsupported, "older than 2.22"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newUpdFixture(t)
			tc.setup(fx)
			scope := "image:stack/web"
			if name == "not allowed" {
				scope = "image:data/app"
			}
			res := fx.run(scope)
			if res.Code != tc.code || !strings.Contains(res.Detail, tc.in) {
				t.Fatalf("got %s %q, want %s with %q", res.Code, res.Detail, tc.code, tc.in)
			}
			if w := fx.d.writes(); len(w) > 0 || slices.Contains(fx.events, "journal") || len(fx.c.ups()) > 0 {
				t.Fatalf("written: %v, events %v, compose %v", w, fx.events, fx.c.ups())
			}
		})
	}
	// Enough space for the same image: it goes through. With the classic image store only the engine's own
	// directory counts.
	for _, classic := range []bool{false, true} {
		fx := newUpdFixture(t)
		fx.f.layers["org/web"] = []int64{300 << 20, 200 << 20}
		fx.service("stack", "web", "org/web", "10", days(30), days(10))
		fx.free[containerdRoot] = 3 << 30
		if classic {
			fx.d.classic, fx.free[containerdRoot] = true, 1<<20
		}
		if res := fx.run("image:stack/web"); res.Code != protocol.CodeOK {
			t.Fatalf("with room (classic %v): %+v", classic, res)
		}
	}
}

// The way back: the service does not come back healthy, keeps dying, agent.yaml health fails, or Compose fails - the
// old image gets the tag again, the service is recreated from it, the keep tag goes back to where it was (to the way
// back of the update before, if there was one), the new image goes; the run ends failed_health
// (failed_package_manager for Compose) and says it went back.
func TestImageUpdateRollsBack(t *testing.T) {
	for name, tc := range map[string]struct {
		break_ func(fx *updFixture, newID string)
		older  bool // an update before left its way back
		health string
		code   protocol.Code
		in     string
	}{
		"service unhealthy": {func(fx *updFixture, newID string) { fx.d.unhealthy[newID] = true }, false, "",
			protocol.CodeFailedHealth, "unhealthy; rolled back"},
		"service keeps dying": {func(fx *updFixture, newID string) { fx.d.crashing[newID] = true }, true, "",
			protocol.CodeFailedHealth, "without a restart; rolled back"},
		"agent health": {func(*updFixture, string) {}, true, "docker web: exited", protocol.CodeFailedHealth,
			"health: docker web: exited; rolled back"},
		"compose fails once": {func(fx *updFixture, _ string) { fx.c.failUps = 1 }, false, "",
			protocol.CodeFailedPackageManager, "docker compose up failed; rolled back"},
		"compose fails always": {func(fx *updFixture, _ string) { fx.c.failUps = 2 }, false, "",
			protocol.CodeFailedPackageManager, "docker compose up failed; rolling back failed"},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newUpdFixture(t)
			oldID, newID := fx.service("stack", "web", "org/web", "10", days(30), days(10))
			fx.ctr("stack-web-1").health = "healthy"
			repo := fx.f.host() + "/org/web"
			older := "sha256:" + strings.Repeat("7", 64)
			if tc.older {
				fx.d.images[older] = &fImage{id: older, platform: amd64}
				fx.d.tags[repo+":vordr-previous.stack.web"] = older
			}
			tc.break_(fx, newID)
			res := fx.runWith(context.Background(), "image:stack/web",
				func(context.Context) string { return tc.health })
			if res.Code != tc.code || !strings.Contains(res.Detail, tc.in) || len(res.Changed) != 0 {
				t.Fatalf("got %+v", res)
			}
			if fx.d.tags[repo+":10"] != oldID {
				t.Fatalf("tag 10 on %s, want the old %s", fx.d.tags[repo+":10"], oldID)
			}
			keep, kept := fx.d.tags[repo+":vordr-previous.stack.web"]
			if tc.older != kept || tc.older && keep != older {
				t.Errorf("keep tag %q %v after going back, want the update before's: %v", keep, kept, tc.older)
			}
			if _, ok := fx.d.images[oldID]; !ok {
				t.Fatal("the old image is gone")
			}
			if name == "compose fails always" {
				return // Compose itself is broken: the container is where Compose left it
			}
			if c := fx.ctr("stack-web-1"); c.image != oldID || c.health != "healthy" {
				t.Fatalf("container after going back: %+v", c)
			}
			if _, kept := fx.d.images[newID]; kept {
				t.Error("the new image is still there")
			}
			if len(fx.c.ups()) != 2 {
				t.Errorf("compose up %d times, want 2", len(fx.c.ups()))
			}
		})
	}
}

// Once something changed, the run's own deadline and the agent stopping do not cut the way back: the health check
// fails with the run's context already cancelled, and the service still goes back.
func TestImageUpdateGoesBackAfterTheRunEnds(t *testing.T) {
	fx := newUpdFixture(t)
	oldID, _ := fx.service("stack", "web", "org/web", "10", days(30), days(10))
	ctx, cancel := context.WithCancel(context.Background())
	res := fx.runWith(ctx, "image:stack/web", func(context.Context) string {
		cancel() // the agent stops, or install.timeout_min is up
		return "http://127.0.0.1:3000: no answer"
	})
	if res.Code != protocol.CodeFailedHealth || !strings.HasSuffix(res.Detail, "; rolled back") {
		t.Fatalf("got %+v", res)
	}
	if c := fx.ctr("stack-web-1"); c.image != oldID || fx.d.tags[fx.f.host()+"/org/web:10"] != oldID {
		t.Fatalf("not back on the old image: %+v", c)
	}
	// A run that is already over starts no further service.
	fx2 := newUpdFixture(t)
	fx2.service("stack", "web", "org/web", "10", days(30), days(10))
	done, stop := context.WithCancel(context.Background())
	stop()
	if res := fx2.runWith(done, "images", nil); res.Code != protocol.CodeTimeout || len(fx2.d.writes()) != 0 {
		t.Fatalf("over before it began: %+v", res)
	}
}

// A pull that fails changes nothing: no journal, no tag.
func TestImageUpdatePullFails(t *testing.T) {
	fx := newUpdFixture(t)
	fx.service("stack", "web", "org/web", "10", days(30), days(10))
	fx.d.pullError = "toomanyrequests: rate limit"
	res := fx.run("image:stack/web")
	if res.Code != protocol.CodeFailedPackageManager || !strings.Contains(res.Detail, "toomanyrequests") {
		t.Fatalf("got %+v", res)
	}
	if w := fx.d.writes(); len(w) != 1 || !strings.HasPrefix(w[0], "POST /images/create") ||
		slices.Contains(fx.events, "journal") {
		t.Fatalf("writes %v, events %v", w, fx.events)
	}
}

// "images": every allowed service with a rebuild old enough; too new, not movable and not allowed ones stay, and the
// detail says which waited and why.
func TestImageUpdateAllAllowed(t *testing.T) {
	fx := newUpdFixture(t)
	_, newWeb := fx.service("stack", "web", "org/web", "10", days(30), days(10))
	oldDB, _ := fx.service("stack", "db", "org/db", "3", days(30), days(3))
	oldCache, _ := fx.service("stack", "cache", "org/cache", "3", days(30), days(10))
	fx.c.hashes["stack/cache"] = "hash-edited" // edited by hand since: not this button's business
	oldApp, _ := fx.service("data", "app", "org/app", "5", days(30), days(10))
	res := fx.run("images")
	if res.Code != protocol.CodeOK || !slices.Equal(res.Changed, []string{"stack/web"}) ||
		!strings.Contains(res.Detail, "updated: stack/web") || !strings.Contains(res.Detail, "too new: stack/db") ||
		!strings.Contains(res.Detail, "not updated: stack/cache: its Compose files") {
		t.Fatalf("result %+v", res)
	}
	if fx.ctr("stack-web-1").image != newWeb || fx.ctr("stack-db-1").image != oldDB ||
		fx.ctr("stack-cache-1").image != oldCache || fx.ctr("data-app-1").image != oldApp {
		t.Fatal("the wrong services moved")
	}
	// Nothing ready, and the registry did not answer for one: that is what the run says.
	fx2 := newUpdFixture(t)
	fx2.service("stack", "web", "org/web", "10", days(30), days(10))
	fx2.f.limited["org/web"] = true
	if res := fx2.run("images"); res.Code != protocol.CodePrecheckRepoUnreachable || len(fx2.d.writes()) != 0 {
		t.Fatalf("no answer: %+v", res)
	}
	// Nothing done for lack of space: the run says so with its code.
	fx3 := newUpdFixture(t)
	fx3.service("stack", "web", "org/web", "10", days(30), days(10))
	fx3.free["/var/lib/docker"] = 1 << 20
	if res := fx3.run("images"); res.Code != protocol.CodePrecheckDiskSpace || len(fx3.d.writes()) != 0 {
		t.Fatalf("no room: %+v", res)
	}
}

// The inventory offers only services the policy allows and the agent can move: the engine's own view, read once.
func TestMarkUpdatable(t *testing.T) {
	fx := newUpdFixture(t)
	fx.service("stack", "web", "org/web", "10", days(30), days(10))
	fx.service("stack", "db", "org/db", "3", days(30), time.Time{})
	fx.service("data", "app", "org/app", "5", days(30), time.Time{})
	pinned := fx.ctr("stack-db-1")
	pinned.ref += "@" + strings.TrimPrefix(fx.d.images[pinned.image].repoDigests[0], fx.f.host()+"/org/db@")
	ctx := context.Background()
	run, code := fx.u.Engine.Running(ctx)
	if code != "" {
		t.Fatal(code)
	}
	loc := Location(run, "", "/run/test.sock")
	MarkUpdatable(&loc, run, func(s Service) bool { return s.Project == "stack" }, fx.u.Engine.Movable(ctx))
	got := map[string][]string{}
	for _, p := range loc.Packages {
		got[strings.TrimPrefix(p.Name, fx.f.host()+"/")] = p.Updatable
	}
	want := map[string][]string{"org/web": {"stack/web"}, "org/db": nil, "org/app": nil}
	for name, w := range want {
		if !slices.Equal(got[name], w) {
			t.Errorf("%s: %v, want %v (pinned and not allowed ones are never offered)", name, got[name], w)
		}
	}
	// A second container on the same tag: web is no longer offered. No answer from the engine: nothing is.
	other := *fx.ctr("stack-web-1")
	other.id, other.name, other.labels = strings.Repeat("e", 64), "standalone", nil
	fx.d.ctrs = append(fx.d.ctrs, &other)
	loc = Location(run, "", "/run/test.sock")
	MarkUpdatable(&loc, run, func(Service) bool { return true }, fx.u.Engine.Movable(ctx))
	got = map[string][]string{}
	for _, p := range loc.Packages {
		got[strings.TrimPrefix(p.Name, fx.f.host()+"/")] = p.Updatable
	}
	if len(got["org/web"]) != 0 || !slices.Equal(got["org/app"], []string{"data/app"}) {
		t.Errorf("with a shared tag: %v", got)
	}
	loc = Location(run, "", "/run/test.sock")
	MarkUpdatable(&loc, run, func(Service) bool { return true }, nil)
	for _, p := range loc.Packages {
		if len(p.Updatable) > 0 {
			t.Errorf("without an answer from the engine, %s offered: %v", p.Name, p.Updatable)
		}
	}
	if len(fx.d.writes()) != 0 {
		t.Errorf("marking wrote: %v", fx.d.writes())
	}
}
