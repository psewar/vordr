// Package deploy holds no Go code: it is the home of the install scripts and of their tests, which run the scripts with
// bash against stand-ins for systemd and the agent binary, with the system paths pointed into a temporary directory.
//
//   - install-agent.sh: installs or updates the agent as a systemd service, and the proxy instances that share its binary.
//   - install-hub.sh: installs or updates the hub.
//   - rollback.sh: puts the previous agent binary back when a self-update does not confirm.
package deploy
