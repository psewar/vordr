---
status: superseded by ADR-0087
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# The local policy on the host has the last word

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28, proposal),
`design/architecture.md` sections 2 and 9.

**Superseded by [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)** in one point: a person's run with a
reason may go outside the window, also when it comes through the hub. Everything else still holds there.

## Context and Problem Statement

Who decides what may happen on a host: scope, window, reboot, self-update?

## Decision Drivers

* A compromised hub must not bypass windows or a reboot ban.
* Whoever looks after a host should see and change its rules there.

## Considered Options

* rules centrally at the hub
* rules locally in `agent.yaml` on each host

## Decision Outcome

Chosen option: locally. What a host allows is in its own `agent.yaml`. The hub can only ask.

* A missing key means the stricter value: no reboot, no automatic install, no self-update, only `check`.
* The agent refuses unknown keys. `vordr-agent check-config` checks the file before the service starts.
* The agent reports a summary of its policy in the hello. The HA buttons come from it; the hub checks commands against
  it in advance, and the agent checks again itself.
* Exceptions only locally and with a reason (`--outside-window "<reason>"`).

### Consequences

* Good, because a compromised hub can only do what every host allows anyway.
* Bad, because changing a policy means replacing the file on the host (`install-agent.sh --replace-config "<reason>"`).
* Bad, because older agents refuse a new key, which slows down rollouts (for example `backup_public_key` only from
  agent 0.7.3).

### Confirmation

`internal/policy`: `TestEmptyFileIsStrict`, `TestIssues`; `TestInstallFollowsThePolicy`; `TestCapabilitiesMatchResolve`
(buttons only where verb and policy match, 96 combinations).
