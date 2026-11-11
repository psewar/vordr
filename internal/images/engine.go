package images

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// DefaultSocket is where the Docker Engine answers. Another engine with the same API (Podman's compatible socket)
// would plug in here.
const DefaultSocket = "/var/run/docker.sock"

// Engine reads the running containers through the engine's HTTP API on its unix socket, GET only (ADR-0113): what
// the engine records, no CLI output.
type Engine struct {
	Socket string
	Client *http.Client
}

// NewEngine returns an engine reader for the socket.
func NewEngine(socket string) Engine {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return Engine{Socket: socket, Client: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}}
}

// Running is one image the host runs, with the containers that run it.
type Running struct {
	Reference   string    // as the containers were created with (Config.Image)
	ImageID     string    // sha256:...
	RepoDigests []string  // repository@sha256:... the image was pulled by; none: built locally (classic image store)
	Created     time.Time // the image configuration's creation time
	Platform    Platform
	Containers  []string
	// RepoTags: repository:tag names the image carries on this host. A reference that pins only a digest is compared
	// under the tag of the same repository (ADR-0117).
	RepoTags []string
	// BuiltLocally: built on this host, so no registry has anything to compare it with. Without repo digests (the
	// classic image store), or Compose built it (its labels in the image configuration): the containerd image store
	// gives local builds a repo digest too (ADR-0115).
	BuiltLocally bool
	// ContainerID: one container that runs the image (the first listed). Its package database tells what the image
	// carries inside (ADR-0120).
	ContainerID string
	// Services: the Compose services ("<project>/<service>") of the containers that run the image, as their labels
	// name them (ADR-0119); none for containers Compose did not create.
	Services []string
}

// composeProjectLabel: Compose writes it into the configuration of every image it builds (and the service and its
// version next to it). A pulled image has it only if its publisher built it with Compose and pushed it - then it is
// left uncompared, the safe way round: nothing is asked or reported for it.
const composeProjectLabel = "com.docker.compose.project"

// builtLocally: no repo digest (classic store), or Compose built it (ADR-0115).
func builtLocally(repoDigests []string, labels map[string]string) bool {
	return len(repoDigests) == 0 || labels[composeProjectLabel] != ""
}

// Platform of an image; a registry's index is read for the same one.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

// Engine states: why the running images could not be read. Empty: read.
const (
	CodeNoEngine          = "no_engine"          // no engine socket on this host
	CodeEngineUnreachable = "engine_unreachable" // the socket is there, the engine did not answer as expected
)

// maxEngineDoc bounds one answer of the engine; a host with hundreds of containers stays far below.
const maxEngineDoc = 16 << 20

var (
	containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageID     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Running lists the images of the running containers. code says why the list is missing or incomplete.
func (e Engine) Running(ctx context.Context) ([]Running, string) {
	if fi, err := os.Stat(e.Socket); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return nil, CodeNoEngine
	}
	var list []struct {
		ID string `json:"Id"`
	}
	if e.get(ctx, "/containers/json", &list) != engineOK { // running ones only: the engine's default
		return nil, CodeEngineUnreachable
	}
	byImage := map[string]*Running{}
	var order []string
	for _, c := range list {
		if !containerID.MatchString(c.ID) {
			continue
		}
		var ctr containerDoc
		switch e.get(ctx, "/containers/"+c.ID+"/json", &ctr) {
		case engineGone: // removed between the list and now: no longer running
			continue
		case engineFailed:
			return nil, CodeEngineUnreachable
		}
		if !imageID.MatchString(ctr.Image) {
			continue
		}
		key := ctr.Config.Image + "\x00" + ctr.Image
		r := byImage[key]
		if r == nil {
			img, ans := e.inspectImage(ctx, ctr.Image)
			switch ans {
			case engineGone: // the container went away with its image in between
				continue
			case engineFailed:
				return nil, CodeEngineUnreachable
			}
			r = &Running{Reference: ctr.Config.Image, ImageID: ctr.Image, RepoDigests: img.RepoDigests,
				Created: img.Created, Platform: img.platform(), RepoTags: img.RepoTags,
				BuiltLocally: builtLocally(img.RepoDigests, img.Config.Labels), ContainerID: c.ID}
			byImage[key] = r
			order = append(order, key)
		}
		r.Containers = append(r.Containers, strings.TrimPrefix(ctr.Name, "/"))
		if s, ok := ctr.service(); ok {
			r.Services = append(r.Services, s.Name())
		}
	}
	out := make([]Running, 0, len(order))
	for _, k := range order {
		r := byImage[k]
		slices.Sort(r.Containers)
		slices.Sort(r.Services)
		r.Services = slices.Compact(r.Services)
		out = append(out, *r)
	}
	return out, ""
}

// containerDoc is what the agent reads of a container's inspection.
type containerDoc struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Image        string `json:"Image"`
	RestartCount int    `json:"RestartCount"`
	Config       struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		StartedAt string `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

// imageDoc is what the agent reads of an image's inspection.
type imageDoc struct {
	ID           string    `json:"Id"`
	RepoDigests  []string  `json:"RepoDigests"`
	RepoTags     []string  `json:"RepoTags"`
	Created      time.Time `json:"Created"`
	Os           string    `json:"Os"`
	Architecture string    `json:"Architecture"`
	Variant      string    `json:"Variant"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (d imageDoc) platform() Platform {
	return Platform{OS: d.Os, Architecture: d.Architecture, Variant: d.Variant}
}

// inspectImage reads an image by id or name.
func (e Engine) inspectImage(ctx context.Context, ref string) (imageDoc, engineAnswer) {
	var img imageDoc
	return img, e.get(ctx, "/images/"+url.PathEscape(ref)+"/json", &img)
}

// get reads one answer of the engine into v.
func (e Engine) get(ctx context.Context, path string, v any) engineAnswer {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine"+path, nil)
	if err != nil {
		return engineFailed
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return engineFailed
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxEngineDoc))
		return engineGone
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxEngineDoc))
		return engineFailed
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxEngineDoc)).Decode(v) != nil {
		return engineFailed
	}
	return engineOK
}

// engineAnswer is the state of one engine read.
type engineAnswer int

const (
	engineOK     engineAnswer = iota
	engineGone                // 404: the container or image no longer exists
	engineFailed              // no usable answer
)

// Tag is the tag ref is compared under: its own, or for a reference that pins only a digest, the tag the image
// carries on this host for the same repository (ADR-0117). Of several, a tag with a version form wins over a word
// (v3.1 over latest: newer tags of its form can be found), then the first in alphabetical order. "" when there is
// none: then the image is listed, not compared. A tag the host moved to a newer image since is no longer the running
// image's, so it does not count.
func (r Running) Tag(ref Ref) string {
	if ref.Tag != "" {
		return ref.Tag
	}
	var tags []string
	for _, rt := range r.RepoTags {
		if got, ok := ParseRef(rt); ok && got.Name() == ref.Name() && got.Digest == "" && got.Tag != "" {
			tags = append(tags, got.Tag)
		}
	}
	if len(tags) == 0 {
		return ""
	}
	slices.SortFunc(tags, func(a, b string) int {
		_, fa := parseForm(a)
		_, fb := parseForm(b)
		switch {
		case fa && !fb:
			return -1
		case fb && !fa:
			return 1
		}
		return strings.Compare(a, b)
	})
	return tags[0]
}

// RepoDigest returns the digest the image was pulled by from ref's repository; "" when it has none from there, or
// was built on this host: then nothing is compared and no registry is asked about it.
func (r Running) RepoDigest(ref Ref) string {
	if r.BuiltLocally {
		return ""
	}
	for _, rd := range r.RepoDigests {
		name, digest, ok := strings.Cut(rd, "@")
		if !ok || !digestPattern.MatchString(digest) {
			continue
		}
		if got, ok := ParseRef(name); ok && got.Name() == ref.Name() {
			return digest
		}
	}
	return ""
}

func (p Platform) String() string {
	s := fmt.Sprintf("%s/%s", p.OS, p.Architecture)
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}
