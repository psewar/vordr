package hub

import (
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// Containers is the host's container location as Home Assistant sees it (ADR-0113): the images of its running
// containers, what their registries say, the known vulnerabilities inside them (ADR-0120) and which of their Compose
// services may be moved to a rebuild by button (ADR-0119). nil when the host's last inventory has no such location.
func (p *Packages) Containers(node string) *wire.ContainersView {
	p.mu.Lock()
	defer p.mu.Unlock()
	inv := p.inv[node]
	if inv == nil {
		return nil
	}
	i := slices.IndexFunc(inv.Locations, func(l inventory.Location) bool { return l.Kind == inventory.KindContainer })
	if i < 0 {
		return nil
	}
	loc := inv.Locations[i]
	v := &wire.ContainersView{CheckedAt: inv.CheckedAt, Code: loc.Code, Images: len(loc.Packages),
		OutdatedFailed: loc.OutdatedFailed, NotInRegistry: loc.NotInRegistry}
	outdated, ready, installable := 0, 0, 0
	var updates []wire.ImageUpdateView
	for _, img := range loc.Packages {
		if img.Digest == "" {
			v.Local++
		}
		if img.CompareFailed != "" && len(v.CompareFailed) < wire.MaxUpdates { // why no answer (ADR-0126)
			v.CompareFailed = append(v.CompareFailed, wire.ImageCompareFailedView{Image: img.Name, Tag: img.Version,
				Containers: img.Containers, Code: img.CompareFailed})
		}
		if img.Rebuilt == "" && img.Latest == "" {
			continue
		}
		u := wire.ImageUpdateView{Image: img.Name, Tag: img.Version, Containers: img.Containers,
			Rebuilt: img.Rebuilt != "", RebuiltAt: img.RebuiltAt, RebuiltReady: img.RebuiltCandidate,
			Latest: img.Latest, Candidate: img.Candidate, LatestAt: img.LatestAt, Major: img.Major,
			SameMajor: img.SameMajor, Services: img.Updatable}
		outdated++
		if imageReady(u) {
			ready++
		}
		if u.RebuiltReady && len(u.Services) > 0 { // what the install button takes (ADR-0119)
			installable++
		}
		updates = append(updates, u)
	}
	if loc.ComparedAt != nil { // the images' own time: a pip/npm run says nothing about them
		v.Outdated, v.Ready, v.ComparedAt, v.MinAgeDays = &outdated, &ready, loc.ComparedAt, inv.MinAgeDays
		v.Installable = &installable
		slices.SortStableFunc(updates, func(a, b wire.ImageUpdateView) int {
			ra, rb := imageReady(a), imageReady(b)
			switch {
			case ra && !rb:
				return -1
			case rb && !ra:
				return 1
			}
			return strings.Compare(a.Image+"\x00"+a.Tag, b.Image+"\x00"+b.Tag)
		})
		if len(updates) > wire.MaxUpdates {
			updates = updates[:wire.MaxUpdates]
		}
		v.Updates = updates
	}
	if p.ImageContentsOn() {
		p.imageFindings(node, loc, v)
	}
	return v
}

// imageFindings fills in the known vulnerabilities inside the images (ADR-0120) from the results matching kept: per
// image its counts, or why it was not matched, and whether a newer image is there to install (ADR-0130). The counts
// stay nil while no image was matched. Called with p.mu held.
func (p *Packages) imageFindings(node string, loc inventory.Location, v *wire.ContainersView) {
	results := p.images[node]
	matched, fixable, unfixed, fixableNewer := 0, 0, 0, 0
	var list []wire.ImageFindingsView
	for _, img := range loc.Packages {
		f := wire.ImageFindingsView{Image: img.Name, Tag: img.Version, Containers: img.Containers,
			Newer: loc.ComparedAt != nil && hasCandidate(img)}
		r, code := releaseOf(img.OS)
		m, ok := results[imageKey(img)]
		switch {
		case code != "":
			f.Code = code
		case !p.distributionOn(versions.Base(r)) || !ok || m.Release != r:
			f.Release, f.Code = string(r), ImageNoData
		default:
			matched++
			f.Release, f.Fixable, f.Unfixed, f.Kernel = string(r), len(m.Fixable), len(m.Unfixed), m.Kernel
			f.IDs = append(slices.Clone(m.Fixable), m.Unfixed...)
			if len(f.IDs) > wire.MaxFindingIDs {
				f.IDs = f.IDs[:wire.MaxFindingIDs]
			}
			if f.Fixable > 0 {
				fixable++
				if f.Newer {
					fixableNewer++
				}
			}
			if f.Unfixed > 0 {
				unfixed++
			}
			if f.Fixable == 0 && f.Unfixed == 0 && f.Kernel == 0 {
				continue // nothing known against it: not listed; one with only kernel advisories is (ADR-0122)
			}
		}
		list = append(list, f)
	}
	if matched > 0 {
		v.Fixable, v.Unfixed = &fixable, &unfixed
		if loc.ComparedAt != nil { // never compared: whether a newer image exists is unknown, not "none"
			v.FixableNewer = &fixableNewer
		}
	}
	slices.SortStableFunc(list, func(a, b wire.ImageFindingsView) int {
		an, bn := a.Fixable > 0 && a.Newer, b.Fixable > 0 && b.Newer
		switch {
		case an && !bn: // what an install would bring first (ADR-0130)
			return -1
		case bn && !an:
			return 1
		case a.Fixable > 0 && b.Fixable == 0:
			return -1
		case b.Fixable > 0 && a.Fixable == 0:
			return 1
		case a.Code == "" && b.Code != "":
			return -1
		case b.Code == "" && a.Code != "":
			return 1
		}
		return strings.Compare(a.Image+"\x00"+a.Tag, b.Image+"\x00"+b.Tag)
	})
	if len(list) > wire.MaxUpdates {
		list = list[:wire.MaxUpdates]
	}
	v.Vulnerable = list
}

// imageReady: an image with a candidate - a rebuild or a newer tag at least min_age_days old, also one within the
// running major version beside a newer major one (ADR-0131).
func imageReady(u wire.ImageUpdateView) bool {
	return u.RebuiltReady || u.Candidate != "" || u.SameMajor != ""
}

// hasCandidate: the same for an image of the inventory - a newer image is there to install (ADR-0130).
func hasCandidate(img inventory.Package) bool {
	return img.RebuiltCandidate || img.Candidate != "" || img.SameMajor != ""
}

// UpdatableServices: the Compose services of a host whose image may be moved to a rebuild by button (ADR-0119), as
// its agent reported them - its policy allows them, and the image is one it can move. Sorted; nil without any.
func (p *Packages) UpdatableServices(node string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	inv := p.inv[node]
	if inv == nil {
		return nil
	}
	var out []string
	for _, l := range inv.Locations {
		if l.Kind != inventory.KindContainer {
			continue
		}
		for _, img := range l.Packages {
			out = append(out, img.Updatable...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
