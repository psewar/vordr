package hub

import "github.com/psewar/vordr/internal/protocol"

// Roles of Home Assistant clients. The host's local policy has the last word anyway; the role limits what a taken-over
// Home Assistant could make the hub sign. Home Assistant, with its add-ons and custom integrations, is a large attack
// surface, so it gets only what it needs.
const (
	RoleObserve = "observe" // views only, no commands
	RoleOperate = "operate" // check, install, agent update - the default
	RoleAdmin   = "admin"   // also reboot
)

// AdminRoleForbids: the client's role does not allow the command. AdminUnknownRole: no such role.
const (
	AdminRoleForbids AdminCode = "role_forbids"
	AdminUnknownRole AdminCode = "unknown_role"
)

// ValidRole reports whether r names a role.
func ValidRole(r string) bool { return r == RoleObserve || r == RoleOperate || r == RoleAdmin }

// EffectiveRole is the role a client has: operate when none was set (clients admitted before roles existed).
func EffectiveRole(r string) string {
	if r == "" {
		return RoleOperate
	}
	return r
}

// RoleAllows reports whether a client with role may start verb.
func RoleAllows(role string, verb protocol.Verb) bool {
	switch EffectiveRole(role) {
	case RoleAdmin:
		return true
	case RoleOperate:
		return verb == protocol.VerbCheck || verb == protocol.VerbInstall || verb == protocol.VerbAgentUpdate
	}
	return false
}

// SetRole gives a client a role.
func (r *ClientRegistry) SetRole(name, role string) AdminCode {
	if !ValidRole(role) {
		return AdminUnknownRole
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.clients[name]
	switch {
	case c == nil:
		return AdminUnknownNode
	case EffectiveRole(c.Role) == role:
		return AdminAlready
	}
	old := c.Role
	c.Role = role
	if err := r.save(); err != nil {
		c.Role = old
		return AdminStoreFailed
	}
	return AdminOK
}
