---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# The hub stays a service of its own and keeps the signing key

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/ha-integration.md` section 2, the operator's notes
("plan the integration").

## Context and Problem Statement

With an own HA integration it was tempting to build the whole hub inside Home Assistant.

## Decision Drivers

* Whoever takes over HA must not be able to sign jobs for all hosts.
* Agents should need one route only: the one to the hub.
* An HA restart (for example after an update) should not disconnect the agents.
* Approvals stay outside HA.

## Considered Options

* A: integration as a client of the hub
* B: hub as part of HA

## Decision Outcome

Chosen option: A. The signing key stays in the hub; HA can only ask.

### Consequences

* Good, because a compromised HA can only do what the hub and the host policies allow
  ([ADR-0039](0039-ha-client-roles.md) limits it further).
* Good, because HA restarts only disconnect the integration.
* Bad, because there are two parts to run (endpoint in the hub, and the integration).

### Confirmation

Threat model in `design/architecture.md` 5 and `design/ha-integration.md` 4; mutation probe on the permission checks
(18 of 18 caught).

## Pros and Cons of the Options

| | A (chosen) | B |
|---|---|---|
| Signing key | stays in the hub | would sit in HA |
| Reachability | agents only reach the hub | every network would need a route to HA |
| HA restart | disconnects only the integration | disconnects all tunnels |
| Approval of new hosts | at the hub | in HA |
| Effort | endpoint + integration | rebuild the hub in Python |
