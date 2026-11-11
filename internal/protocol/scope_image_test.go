package protocol

import (
	"strings"
	"testing"
)

// ADR-0119: an image scope names a Compose service with names Compose accepts, never a pattern, a path or an option.
func TestImageScopes(t *testing.T) {
	for in, want := range map[string]Scope{
		"images":                    {Kind: ScopeImages},
		"image:stack/web":           {Kind: ScopeImage, Project: "stack", Service: "web"},
		"image:my_proj-1/Web.Api_2": {Kind: ScopeImage, Project: "my_proj-1", Service: "Web.Api_2"},
		"image:0/x":                 {Kind: ScopeImage, Project: "0", Service: "x"},
		"image:" + strings.Repeat("p", 56) + "/" + strings.Repeat("s", 56): {Kind: ScopeImage,
			Project: strings.Repeat("p", 56), Service: strings.Repeat("s", 56)},
	} {
		got, ok := ParseScope(in)
		if !ok || got != want || got.String() != in || !got.Images() {
			t.Errorf("ParseScope(%q) = %+v %v, String %q", in, got, ok, got.String())
		}
	}
	for _, in := range []string{"image:", "image:x", "image:/s", "image:p/", "image:P/s", "image:-p/s", "image:p/-s",
		"image:p/s/t", "image:p/*", "image:*/s", "image:p/s;rm", "image:p/s x", "image:p/s\n", "image:../s",
		"image:p/..x", "image:" + strings.Repeat("p", 57) + "/s", "image:p/" + strings.Repeat("s", 57), "images:",
		"image", "Images"} {
		if sc, ok := ParseScope(in); ok {
			t.Errorf("ParseScope(%q) accepted: %+v", in, sc)
		}
	}
	for _, s := range []string{"security", "all", "app:a", "pkg:l", "deps:l"} {
		if sc, _ := ParseScope(s); sc.Images() {
			t.Errorf("%q counts as an image scope", s)
		}
	}
}

// An install job with an image scope has valid parameters; one with a pattern, or one that names packages for the
// shorter minimum age (only pip/npm and dependencies do, ADR-0111), does not.
func TestImageScopeParameters(t *testing.T) {
	for scope, ok := range map[string]bool{"image:stack/web": true, "images": true,
		"image:stack/*": false} {
		j := Job{ID: "j1", Node: "host-a", Verb: VerbInstall, Scope: scope}
		if got := validParameters(j); got != ok {
			t.Errorf("install %q: valid %v, want %v", scope, got, ok)
		}
	}
	j := Job{ID: "j1", Node: "host-a", Verb: VerbInstall, Scope: "images", SecurityFix: []string{"web"}}
	if validParameters(j) {
		t.Error("an image scope with security_fix passed")
	}
	if validParameters(Job{ID: "j1", Verb: VerbReboot, Scope: "images"}) {
		t.Error("a reboot with an image scope passed")
	}
}
