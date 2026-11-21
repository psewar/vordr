package hub

import (
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Holding a release a host offers only in another branch of its updates (ADR-0208). The operator stays on the device's
// branch for now: the release stays pending and kept back, but no longer counts as pending too long, so that the stale
// problem ends. The hub holds exactly the release the host offers when it is asked, and ends the hold by itself once
// the host no longer offers exactly that one: a newer release in the other branch counts again from when it appears.
// Home Assistant holds through its repair's fix (hold_release, by role operate or admin); `vordr-hub unhold` ends a hold.

// AdminNotOffered: the host does not offer that release in another branch (any more), or its last report says nothing
// about what is pending. Also a code of hold_release besides ok, client_not_approved, role_forbids, unknown_node,
// not_approved, already_in_state, bad_request and store_failed.
const AdminNotOffered AdminCode = "not_offered"

// HeldRelease is a release in another branch the operator holds, as the host's report named it.
type HeldRelease struct {
	Source string    `json:"source"`
	Name   string    `json:"name"`
	To     string    `json:"to"`
	At     time.Time `json:"at"`
	By     string    `json:"by"` // who asked: ByHA(client)
}

// otherBranchOffers: the pending updates of a report that a source holds back because the device offers them only in
// another branch of its updates (ADR-0204, ADR-0207).
func otherBranchOffers(rep wire.Report) []PendingUpdate {
	var out []PendingUpdate
	for _, s := range rep.Sources {
		if s.KeptBackWhy != protocol.KeptBackOtherBranch {
			continue
		}
		for _, p := range s.Packages {
			if p.KeptBack {
				out = append(out, PendingUpdate{Source: s.Source, Name: p.Name, To: p.To})
			}
		}
	}
	return out
}

// offeredIn: the report offers exactly this release in another branch.
func (h *HeldRelease) offeredIn(rep wire.Report) bool {
	for _, o := range otherBranchOffers(rep) {
		if o.Source == h.Source && o.Name == h.Name && o.To == h.To {
			return true
		}
	}
	return false
}

// offeredRelease: the release in another branch at version to that the complete report rep offers.
func offeredRelease(rep *wire.Report, to string) (PendingUpdate, bool) {
	if rep == nil {
		return PendingUpdate{}, false
	}
	if _, known := pendingCount(*rep); !known {
		return PendingUpdate{}, false // a failed check says nothing about what is offered
	}
	for _, o := range otherBranchOffers(*rep) {
		if o.To == to {
			return o, true
		}
	}
	return PendingUpdate{}, false
}

// HoldRelease holds the release node offers only in another branch, at version to, for by (ADR-0208); with to empty it
// ends the node's hold. Decided on the host's last report under the register's lock: the hub holds only what the host
// offers now. While the held release is all that is pending, nothing counts as pending too long; once the hold ends
// that way, the release counts again from now.
func (r *Registry) HoldRelease(node, to, by string, now time.Time) AdminCode {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	switch {
	case e == nil:
		return AdminUnknownNode
	case e.State != wire.StateApproved:
		return AdminNotApproved
	}
	oldHeld, oldSince := e.HeldRelease, e.PendingSince
	if to == "" {
		if e.HeldRelease == nil {
			return AdminAlready
		}
		e.HeldRelease = nil
		if e.PendingSince == nil && e.LastReport != nil {
			if pending, known := pendingCount(*e.LastReport); known && pending > 0 {
				at := now
				e.PendingSince = &at
			}
		}
	} else {
		if e.HeldRelease != nil && e.HeldRelease.To == to {
			return AdminAlready
		}
		offer, ok := offeredRelease(e.LastReport, to)
		if !ok {
			return AdminNotOffered
		}
		e.HeldRelease = &HeldRelease{Source: offer.Source, Name: offer.Name, To: to, At: now, By: by}
		if pending, _ := pendingCount(*e.LastReport); pending == 1 {
			e.PendingSince = nil
		}
	}
	if err := r.save(); err != nil {
		e.HeldRelease, e.PendingSince = oldHeld, oldSince
		return AdminStoreFailed
	}
	return AdminOK
}

// takeReport stores a bounded report of node, whose entry was before before it, and logs a hold the report ended: the
// host no longer offers exactly the held release.
func (s *Server) takeReport(node string, before Entry, rep wire.Report) {
	if err := s.Reg.SetReportAt(node, rep, s.Now()); err != nil {
		s.Log.Printf("register: %v", err)
	}
	if held := before.HeldRelease; held != nil {
		if after, _ := s.Reg.Get(node); after.HeldRelease == nil {
			s.Events.Log(node, "release_hold_ended", map[string]any{"to": held.To})
		}
	}
}

// haHoldRelease answers hold_release. The client's approval comes first, as for approve_host: a client that is not
// approved gets that code, and the connection ends (loopHA). Then the role and the version's form; the host and what it
// offers are decided under the register's lock (Registry.HoldRelease). Ending a hold stays at the command line.
func (s *Server) haHoldRelease(sess *haSession, h wire.HoldRelease) wire.HoldReleaseResult {
	res := wire.HoldReleaseResult{T: wire.THoldReleaseResult, RequestID: h.RequestID}
	fields := map[string]any{"client": sess.name, "request_id": h.RequestID}
	refused := func(code AdminCode) wire.HoldReleaseResult {
		fields["code"] = code
		s.Events.Log(h.Node, "release_hold_refused", fields)
		res.Code = string(code)
		return res
	}
	client, ok := s.approvedClient(sess)
	switch {
	case !ok:
		return refused(AdminClientNotApproved)
	case !RoleAllowsReleaseHold(client.Role):
		fields["role"] = EffectiveRole(client.Role)
		return refused(AdminRoleForbids)
	case h.To == "" || nameText(h.To) != "":
		return refused(AdminBadRequest)
	}
	fields["to"] = h.To
	if code := s.Reg.HoldRelease(h.Node, h.To, ByHA(sess.name), s.Now()); code != AdminOK {
		return refused(code)
	}
	fields["code"] = AdminOK
	s.Events.Log(h.Node, "release_hold", fields)
	s.changed(h.Node)
	res.Code = string(AdminOK)
	return res
}
