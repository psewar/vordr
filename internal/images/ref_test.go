package images

import (
	"slices"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	for in, want := range map[string]Ref{
		"postgres:16":                         {DockerHub, "library/postgres", "16", ""},
		"postgres":                            {DockerHub, "library/postgres", "latest", ""},
		"org/app:1.2":                         {DockerHub, "org/app", "1.2", ""},
		"index.docker.io/library/nginx:1.27":  {DockerHub, "library/nginx", "1.27", ""},
		"ghcr.io/org/app:1.4.2-gpu":           {"ghcr.io", "org/app", "1.4.2-gpu", ""},
		"registry.example:5000/team/svc:v1.2": {"registry.example:5000", "team/svc", "v1.2", ""},
		"localhost/app:dev":                   {"localhost", "app", "dev", ""},
		"ghcr.io/org/app@" + digest:           {"ghcr.io", "org/app", "", digest},
		"ghcr.io/org/app:1.2@" + digest:       {"ghcr.io", "org/app", "1.2", digest},
	} {
		got, ok := ParseRef(in)
		if !ok || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", digest, digest[7:], "Upper/Case:1", "app:bad tag", "app@sha256:short",
		"-x/app:1", "app::1", "registry.example:5000/:1"} {
		if r, ok := ParseRef(bad); ok {
			t.Errorf("ParseRef(%q) = %+v, want no reference", bad, r)
		}
	}
	if r, _ := ParseRef("postgres:16"); r.Name() != "docker.io/library/postgres" || r.apiHost() != dockerHubAPI {
		t.Errorf("name %q, api %q", r.Name(), r.apiHost())
	}
}

// Only tags of the same form compare (ADR-0113): as many numbers, the same prefix and suffix, no pre-release.
func TestNewerTags(t *testing.T) {
	tags := []string{"15", "16", "17", "18", "17-alpine", "18-rc1", "latest", "16.4", "9", "010"}
	if got := newerTags("16", tags); !slices.Equal(got, []string{"18", "17"}) {
		t.Errorf("16: %v", got) // "010" is 10: older; "18-rc1" has a suffix; 16.4 another form
	}
	variants := []string{"1.4.1-gpu", "1.4.3-gpu", "1.4.3", "1.5.0-beta1-gpu", "1.5.0-gpu-rc2",
		"1.4.10-gpu", "1.4.2-gpu"}
	if got := newerTags("1.4.2-gpu", variants); !slices.Equal(got, []string{"1.4.10-gpu", "1.4.3-gpu"}) {
		t.Errorf("gpu variant: %v", got)
	}
	if got := newerTags("v1.2", []string{"v1.3", "1.4", "v1.10", "v2.0"}); !slices.Equal(got, []string{"v2.0", "v1.10", "v1.3"}) {
		t.Errorf("v prefix: %v", got)
	}
	for _, none := range []string{"latest", "stable", "1.2.0-rc1", "2.0-beta.2"} {
		if got := newerTags(none, []string{"1.2.1", "2.1", "9.9", "1.3.0-rc1", "2.1-beta.2"}); got != nil {
			t.Errorf("%s has no newer form, got %v", none, got)
		}
	}
	// A date or build number is no version: alpine:3 does not find the date tag 20250108 newer, but dates compare.
	if got := newerTags("3", []string{"3", "4", "20250108", "3.21"}); !slices.Equal(got, []string{"4"}) {
		t.Errorf("3 with date tags: %v", got)
	}
	if got := newerTags("20250101", []string{"20250108", "3", "20241231"}); !slices.Equal(got, []string{"20250108"}) {
		t.Errorf("date tags: %v", got)
	}
	a, _ := parseForm("0.4.2")
	b, _ := parseForm("0.5.0")
	c, _ := parseForm("1.0.0")
	if a.major(b) || !a.major(c) {
		t.Error("major is a change of the first number")
	}
	if compareNumber("00012345678901234567890", "12345678901234567891") != -1 {
		t.Error("long numbers")
	}
}
