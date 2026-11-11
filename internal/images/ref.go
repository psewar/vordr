// Package images reads which container images a host runs and asks their registries whether they are outdated
// (ADR-0113, design 11.5): the running containers come from the container engine's API on the local socket (GET
// only, no CLI), the registries are asked anonymously through the distribution API. It only reports; nothing here
// pulls, starts or changes a container.
package images

import (
	"regexp"
	"strings"
)

// Ref is an image reference as a container was created with: postgres:16, ghcr.io/org/app:1.2@sha256:...
type Ref struct {
	Registry   string // docker.io, ghcr.io, registry.example:5000
	Repository string // library/postgres, org/app
	Tag        string // "latest" when the reference names neither a tag nor a digest
	Digest     string // set when the reference pins a digest: then the tag cannot move under the container
}

// DockerHub is the registry of references without one; its API answers at dockerHubAPI.
const (
	DockerHub    = "docker.io"
	dockerHubAPI = "registry-1.docker.io"
)

var (
	pathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	tagPattern    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	bareID        = regexp.MustCompile(`^(?:sha256:)?[0-9a-f]{64}$`)
	hostPattern   = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]{1,5})?$`)
)

// ParseRef reads a reference the way the engine resolves it. ok is false for anything that is not one - an image
// id, an empty string - and then nothing about it is asked anywhere.
func ParseRef(s string) (Ref, bool) {
	if bareID.MatchString(s) { // a container created from an image id names no repository
		return Ref{}, false
	}
	var r Ref
	name := s
	if at := strings.IndexByte(s, '@'); at >= 0 {
		name, r.Digest = s[:at], s[at+1:]
		if !digestPattern.MatchString(r.Digest) {
			return Ref{}, false
		}
	}
	if slash, colon := strings.LastIndexByte(name, '/'), strings.LastIndexByte(name, ':'); colon > slash {
		name, r.Tag = name[:colon], name[colon+1:]
		if !tagPattern.MatchString(r.Tag) {
			return Ref{}, false
		}
	}
	if name == "" || len(name) > 255 {
		return Ref{}, false
	}
	first, rest, hasSlash := strings.Cut(name, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if !hostPattern.MatchString(first) {
			return Ref{}, false
		}
		r.Registry, r.Repository = first, rest
	} else {
		r.Registry, r.Repository = DockerHub, name
	}
	if r.Registry == "index.docker.io" {
		r.Registry = DockerHub
	}
	if r.Registry == DockerHub && !strings.Contains(r.Repository, "/") {
		r.Repository = "library/" + r.Repository
	}
	for _, c := range strings.Split(r.Repository, "/") {
		if !pathComponent.MatchString(c) {
			return Ref{}, false
		}
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r, true
}

// Name is the repository with its registry, as the inventory lists the image: docker.io/library/postgres.
func (r Ref) Name() string { return r.Registry + "/" + r.Repository }

// apiHost is where the registry's distribution API answers.
func (r Ref) apiHost() string {
	if r.Registry == DockerHub {
		return dockerHubAPI
	}
	return r.Registry
}
