---
status: accepted
date: 2026-10-28
decision-makers: kay
---

# Own update agent instead of PatchMon

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28),
`design/architecture.md` section 1.

## Context and Problem Statement

The operator wants to keep all computers and services up to date and see in Home Assistant when an update fails or
hangs. HA only knows updates for devices that bring their own integration. Linux servers, VMs, containers, Raspberry
Pis and Windows PCs are missing. The `update` entity has no "failed" state.

## Decision Drivers

* A compromised central service must not mean root on every host.
* Integration with Home Assistant.
* Every failure as a stored state, not a passing message.

## Considered Options

* PatchMon
* updates2mqtt
* LNXlink
* HASS.Agent
* own agent with a hub

## Decision Outcome

Chosen option: own agent with a hub.

The operator explicitly rejected PatchMon: central root remote execution on all hosts plus an SSH terminal in the
browser is too much attack surface. A compromised server means root everywhere. There is no HA integration. The other
tools only cover part of the problem.

### Consequences

* Good, because the security boundaries are our own ([ADR-0002](0002-own-project-following-an-earlier-agent.md),
  [ADR-0005](0005-closed-verbs-without-shell.md), [ADR-0006](0006-local-policy-decides.md)).
* Good, because Home Assistant stays the only user interface.
* Bad, because agent, hub and integration must be built, run and kept up to date by us.

### Confirmation

The threat model (`design/architecture.md` section 5) states what a compromised hub cannot do: bring commands or code
onto hosts.

## Pros and Cons of the Options

| Option | Why not |
|---|---|
| PatchMon | root remote execution on all hosts, SSH terminal in the browser, no HA integration |
| updates2mqtt | Docker containers only |
| LNXlink | counts pending updates, but runs nothing and reports no failures |
| HASS.Agent | Windows Update sensor only in the old version; whether the current fork has it is unknown |
