package images

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/protocol"
)

// Labels Compose writes on every container it creates (ADR-0119): the project and service, where the project's files
// are, and the hash of the service's configuration. A container without them was not created by Compose and is never
// updated.
const (
	labelService     = "com.docker.compose.service"
	labelWorkingDir  = "com.docker.compose.project.working_dir"
	labelConfigFiles = "com.docker.compose.project.config_files"
	labelEnvFiles    = "com.docker.compose.project.environment_file"
	labelConfigHash  = "com.docker.compose.config-hash"
	labelOneoff      = "com.docker.compose.oneoff" // "True": a container of "docker compose run", not of the service
)

// Service names a Compose service as its containers' labels do.
type Service struct {
	Project string
	Service string
}

// Name is "<project>/<service>", as install.allowed_scopes and the scope image:<project>/<service> name it.
func (s Service) Name() string { return s.Project + "/" + s.Service }

// service reads the container's Compose labels; false for a container Compose did not create for a service, or whose
// names are none Compose accepts.
func (d containerDoc) service() (Service, bool) {
	l := d.Config.Labels
	s := Service{Project: l[composeProjectLabel], Service: l[labelService]}
	if l[labelOneoff] == "True" || !protocol.ComposeProjectPattern.MatchString(s.Project) ||
		!protocol.ComposeServicePattern.MatchString(s.Service) {
		return Service{}, false
	}
	return s, true
}

// Container is one container on the host, running or not, as the engine records it.
type Container struct {
	ID           string
	Name         string
	Reference    string // as the container was created with (Config.Image)
	ImageID      string
	State        string // running, exited, restarting, ...
	Health       string // healthy, unhealthy, starting; "" without a health check
	StartedAt    string
	RestartCount int
	// Service: the Compose service it belongs to; Compose is false for a container Compose did not create for one.
	Service Service
	Compose bool
	// WorkingDir, ConfigFiles and EnvFiles: the project's directory and files, as Compose recorded them when it created
	// the container; ConfigHash: the hash of the service's configuration then. The agent runs Compose with exactly these
	// files, and only while the configuration still hashes the same.
	WorkingDir  string
	ConfigFiles []string
	EnvFiles    []string
	ConfigHash  string
}

// Containers: every container on the host, running or not, in the order of their names. ok is false when the engine
// gave no usable answer.
func (e Engine) Containers(ctx context.Context) ([]Container, bool) {
	var list []struct {
		ID string `json:"Id"`
	}
	if e.get(ctx, "/containers/json?all=1", &list) != engineOK {
		return nil, false
	}
	var out []Container
	for _, c := range list {
		if !containerID.MatchString(c.ID) {
			continue
		}
		var d containerDoc
		switch e.get(ctx, "/containers/"+c.ID+"/json", &d) {
		case engineGone:
			continue
		case engineFailed:
			return nil, false
		}
		l := d.Config.Labels
		ct := Container{ID: c.ID, Name: strings.TrimPrefix(d.Name, "/"), Reference: d.Config.Image, ImageID: d.Image,
			State: d.State.Status, StartedAt: d.State.StartedAt, RestartCount: d.RestartCount}
		if d.State.Health != nil {
			ct.Health = d.State.Health.Status
		}
		if s, ok := d.service(); ok {
			ct.Service, ct.Compose = s, true
			ct.WorkingDir, ct.ConfigHash = l[labelWorkingDir], l[labelConfigHash]
			ct.ConfigFiles, ct.EnvFiles = splitLabel(l[labelConfigFiles]), splitLabel(l[labelEnvFiles])
		}
		out = append(out, ct)
	}
	slices.SortFunc(out, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return out, true
}

// splitLabel: Compose joins several files with commas.
func splitLabel(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Image is an image the engine has, by id or name. found is false when it has none of that name, ok false when it
// gave no usable answer.
func (e Engine) Image(ctx context.Context, ref string) (img ImageState, found, ok bool) {
	d, ans := e.inspectImage(ctx, ref)
	switch ans {
	case engineGone:
		return ImageState{}, false, true
	case engineFailed:
		return ImageState{}, false, false
	}
	return ImageState{ID: d.ID, RepoTags: d.RepoTags, RepoDigests: d.RepoDigests}, true, true
}

// ImageState: an image's id and the names it carries.
type ImageState struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
}

// InUse reports whether a container, running or not, was created from the image; ok is false without an answer.
func (e Engine) InUse(ctx context.Context, id string) (used, ok bool) {
	var list []struct {
		ImageID string `json:"ImageID"`
	}
	if e.get(ctx, "/containers/json?all=1", &list) != engineOK {
		return false, false
	}
	for _, c := range list {
		if c.ImageID == id {
			return true, true
		}
	}
	return false, true
}

// Tag gives the image (id or name) the name repo:tag; a name another image carried moves to this one.
func (e Engine) Tag(ctx context.Context, image, repo, tag string) bool {
	q := url.Values{"repo": {repo}, "tag": {tag}}
	return e.write(ctx, http.MethodPost, "/images/"+url.PathEscape(image)+"/tag?"+q.Encode()) == http.StatusCreated
}

// Untag takes the name repo:tag off its image. Callers make sure another name holds the image: the engine deletes an
// image whose last name goes and that no container uses.
func (e Engine) Untag(ctx context.Context, repo, tag string) bool {
	return e.write(ctx, http.MethodDelete, "/images/"+url.PathEscape(repo+":"+tag)+"?noprune=1") == http.StatusOK
}

// Remove deletes an image by id. Callers check first that no container uses it and no name holds it: the engine
// would refuse, or with several names only take one off.
func (e Engine) Remove(ctx context.Context, id string) bool {
	return e.write(ctx, http.MethodDelete, "/images/"+url.PathEscape(id)) == http.StatusOK
}

// write sends a request that changes the engine and returns its status code (0: no answer).
func (e Engine) write(ctx context.Context, method, path string) int {
	req, err := http.NewRequestWithContext(ctx, method, "http://engine"+path, nil)
	if err != nil {
		return 0
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxEngineDoc))
	return resp.StatusCode
}

// maxPullLine bounds one progress line of a pull.
const maxPullLine = 1 << 20

// Pull fetches name@digest for the platform, exactly that build, and returns the image's id once the engine has it.
// The engine reports a failed pull inside its answer (status 200, then a line with errorDetail); what it says goes into
// detail for people, and whether the image is there afterwards decides.
func (e Engine) Pull(ctx context.Context, name, digest string, p Platform) (id, detail string, ok bool) {
	q := url.Values{"fromImage": {name}, "tag": {digest}, "platform": {p.String()}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://engine/images/create?"+q.Encode(), nil)
	if err != nil {
		return "", "", false
	}
	client := *e.Client
	client.Timeout = 0 // a pull takes as long as the download; ctx bounds it
	resp, err := client.Do(req)
	if err != nil {
		return "", "the engine did not answer the pull", false
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxPullLine)
	for sc.Scan() {
		var line struct {
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.ErrorDetail != nil {
			detail = line.ErrorDetail.Message
		}
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && detail == "" {
		detail = "the engine refused the pull: HTTP " + resp.Status
	}
	img, found, answered := e.Image(ctx, name+"@"+digest)
	if !answered || !found {
		if detail == "" {
			detail = "the image is not there after the pull"
		}
		return "", detail, false
	}
	return img.ID, "", true
}

// DataDirs: where the engine keeps images - its root directory and, with the containerd image store, containerd's
// where it is.
func (e Engine) DataDirs(ctx context.Context) ([]string, bool) {
	var info struct {
		DockerRootDir string     `json:"DockerRootDir"`
		DriverStatus  [][]string `json:"DriverStatus"`
	}
	if e.get(ctx, "/info", &info) != engineOK || !filepath.IsAbs(info.DockerRootDir) {
		return nil, false
	}
	dirs := []string{info.DockerRootDir}
	for _, kv := range info.DriverStatus {
		if len(kv) == 2 && kv[0] == "driver-type" && strings.HasPrefix(kv[1], "io.containerd.snapshotter") {
			if fi, err := os.Stat(containerdRoot); err == nil && fi.IsDir() {
				dirs = append(dirs, containerdRoot)
			}
		}
	}
	return dirs, true
}

// containerdRoot: where containerd keeps content and snapshots when the engine uses its image store (a variable for
// tests).
var containerdRoot = "/var/lib/containerd"
