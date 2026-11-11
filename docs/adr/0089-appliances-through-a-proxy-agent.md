---
status: accepted
date: 2026-10-30
decision-makers: kay (2026-10-30, idea and decisions); proposed by Claude
---

# Appliances through a proxy agent, not through the hub

## Context and Problem Statement

Once every machine that can run the agent has one, what is left are appliances with vendor firmware (for example
firewalls, NAS, mesh Wi-Fi, smart-home servers and gateways, BMCs, printers, switches). Nobody sees their updates in one
place; a firewall can have a new system release and package updates pending that nothing reports.

The operator asked whether they could run as "virtual agents" directly on the hub, each with its own way to check and
update (scripts, REST, SSH). Design: `design/stellvertreter.md`.

## Decision Drivers

* Appliances should appear in Home Assistant like hosts: pending updates, problems, buttons, events.
* The hub holds no credentials and can only ask; that is the main security boundary
  ([ADR-0006](0006-local-policy-decides.md), [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)). Vördr exists
  instead of a central server with remote execution ([ADR-0001](0001-own-agent-instead-of-patchmon.md)).
* As little new machinery as possible in the hub and the protocol.
* A firmware update of a firewall or a mesh Wi-Fi takes the whole network down.
* Some devices sit in networks the hub should not need a route into.

## Considered Options

* **Virtual agents in the hub:** the hub keeps the appliances' credentials and calls their APIs or SSH itself.
* **Home Assistant integrations:** many NAS, mesh and firewall products have integrations with update entities.
* **Proxy agent:** an isolated container runs one ordinary agent instance per appliance, each with its own identity,
  policy and credentials, and a closed driver (check, back up, install, verify) instead of a package source.
* **One agent with several devices:** like the proxy, but one connection carries many nodes (protocol change).

## Decision Outcome

Chosen: the proxy agent (the operator, 2026-10-30: "the container that is isolated and may only talk to the devices and
the hub sounds good").

* For the hub each appliance is an ordinary node; hub, protocol and integration stay as they are, except the backup
  check ([ADR-0090](0090-appliance-installs-need-a-fresh-config-backup.md)).
* The container talks only to the hub and to its listed devices (egress allowlist), has no SSH service, and runs one
  system user per instance. Credentials are files with mode 0600, written from the secret store, on dedicated service
  accounts per appliance.
* Drivers know only their fixed requests or commands; nothing comes from the hub.
* A proxy stands where it can reach its devices. A segment the hub should not reach gets its own proxy there; this is
  also the answer where devices are otherwise only reachable through a jump host (the operator).
* Appliances are never updated automatically. Installing is by button and only where the instance's policy allows it.
* First device classes: firewalls, smart-home gateways, NAS and every unit of a mesh Wi-Fi; a smart-home server only
  if it reports offered updates.

Rejected: virtual agents in the hub, because a compromised hub could then operate every appliance with admin rights,
which is exactly the central remote-execution server Vördr replaced. Home Assistant integrations for a firewall and
a mesh, because they give Home Assistant far more access to these systems than reporting needs; a firewall in
Home Assistant would be a risk (the operator). One agent with several devices, because it needs a protocol and hub
change that one instance per device avoids.

### Consequences

* Good, because the hub stays credential-free and the threat model for a compromised hub does not change.
* Good, because no protocol change is needed; drivers can come one by one.
* Good, because segmented networks need no route from the hub; a proxy inside them is enough.
* Bad, because the proxy container becomes valuable: whoever takes it can operate the appliances whose credentials it
  holds and read their configuration backups.
* Bad, because some devices know only admin accounts (for example some mesh systems, or a firewall's web interface),
  so even report-only instances hold admin credentials there.
* Bad, because instances share one binary, so self-update needs a small change.
* Bad, because some devices (for example a mesh Wi-Fi) allow one admin session: every check logs out an open browser
  session.

### Confirmation

Tests per driver against recorded appliance answers, a mutation probe, `check-config` refusing `target:` together with
`reboot`, `packages` or `install.auto: hub`, the container's egress rules checked after setup, and a live instance
reporting through the hub like any host.
