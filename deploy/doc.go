// Package deploy is the home of the install scripts and of their tests, which run the scripts with bash against
// stand-ins for systemd and the agent binary, with the system paths pointed into a temporary directory
// (deploy/deploytest). Its only Go code embeds install-agent.sh and the files it installs, so that vordr-agent install
// sets up a Linux host from the binary alone (ADR-0169).
//
//   - install-agent.sh: installs or updates the agent as a systemd service, and the proxy instances that share its binary.
//   - install-hub.sh: installs or updates the hub.
//   - rollback.sh: puts the previous agent binary back when a self-update does not confirm.
package deploy
