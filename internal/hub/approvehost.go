package hub

import (
	"regexp"

	"github.com/psewar/vordr/internal/wire"
)

// Approving a waiting host from Home Assistant (ADR-0192). Off unless hub.yaml sets ha_approves_hosts; then a client
// that is approved with role admin is told so in its status (FeatureApproveHost) and may send approve_host. The hub
// approves only a node that waits for approval, with the fingerprint it holds for the node's key, decided on its
// registers as they are at that moment. Rejecting and forgetting hosts, and everything about clients, stay at the
// command line (admin.go).

// Codes of approve_host besides ok, client_not_approved, role_forbids, unknown_node, fingerprint_mismatch,
// bad_request and store_failed.
const (
	// AdminHAApprovalOff: hub.yaml does not let Home Assistant approve hosts (ha_approves_hosts).
	AdminHAApprovalOff AdminCode = "ha_approval_off"
	// AdminNotWaiting: the node is approved or rejected already; a rejection is undone only at the hub.
	AdminNotWaiting AdminCode = "not_waiting"
)

// hostFingerprintPattern: a host key's fingerprint as the hub and `vordr-agent identity` show it, hex SHA-256.
var hostFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// haFeatures: what the hub accepts from this client beyond the base protocol, in every status it sends it, as the
// client register has it now. agent_download for every client (the role is decided per request, ADR-0167);
// approve_host only while hub.yaml allows it and the client is approved with its key and role admin (ADR-0192).
func (s *Server) haFeatures(name, fingerprint string) []string {
	features := []string{wire.FeatureAgentDownload}
	if s.HAApprovesHosts {
		c, ok := s.Clients.Get(name)
		if ok && c.State == wire.StateApproved && c.Fingerprint == fingerprint && RoleAllowsHostApproval(c.Role) {
			features = append(features, wire.FeatureApproveHost)
		}
	}
	return features
}

// resendStatus sends a connected, approved client its status again, with the features as they are now: after a role
// change approve_host comes or goes (ADR-0192). The views it holds stay as they are. The client register is read
// under sess.mu, as by loginHA and notifyClient: whichever of them sends last names the state as it is then. Lock
// order: sess.mu, then the client register's; nothing takes sess.mu while it holds that.
func (s *Server) resendStatus(name string) {
	s.mu.Lock()
	sess := s.haSessions[name]
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.approved {
		return // pending, or its approval is on the way (notifyClient), which reads the role when it sends
	}
	c, ok := s.Clients.Get(name)
	if !ok || c.Fingerprint != sess.fingerprint || c.State != wire.StateApproved {
		return // forgotten, approved again with another key, or rejected: notifyClient tells it what it needs to know
	}
	if sess.conn.Send(wire.Status{T: wire.TStatus, State: c.State, Fingerprint: c.Fingerprint,
		Features: s.haFeatures(name, sess.fingerprint)}) != nil {
		sess.conn.Close() // the read loop ends and drops the session
	}
}

// haApproveHost answers approve_host. The client's approval comes first: a client that is not approved gets that
// code whatever else holds, and the connection ends (loopHA), so that a pending client cannot write the event log
// full. Then the switch, the role and the fingerprint's form; the node and its key are decided under the register's
// lock (Registry.ApproveWaiting). On ok the host gets its status as after `vordr-hub approve`.
func (s *Server) haApproveHost(sess *haSession, a wire.ApproveHost) wire.ApproveHostResult {
	res := wire.ApproveHostResult{T: wire.TApproveHostResult, RequestID: a.RequestID}
	fields := map[string]any{"client": sess.name, "request_id": a.RequestID}
	refused := func(code AdminCode) wire.ApproveHostResult {
		fields["code"] = code
		s.Events.Log(a.Node, "approve_refused", fields)
		res.Code = string(code)
		return res
	}
	client, ok := s.approvedClient(sess)
	switch {
	case !ok:
		return refused(AdminClientNotApproved)
	case !s.HAApprovesHosts:
		return refused(AdminHAApprovalOff)
	case !RoleAllowsHostApproval(client.Role):
		fields["role"] = EffectiveRole(client.Role)
		return refused(AdminRoleForbids)
	case !hostFingerprintPattern.MatchString(a.Fingerprint):
		return refused(AdminBadRequest)
	}
	if code := s.Reg.ApproveWaiting(a.Node, a.Fingerprint, s.Now()); code != AdminOK {
		return refused(code)
	}
	fields["code"] = AdminOK
	s.Events.Log(a.Node, "approve", fields)
	s.Notify(a.Node)
	s.changed(a.Node)
	res.Code = string(AdminOK)
	return res
}
