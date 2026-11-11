---
status: accepted
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# Scope limits: what Vördr does not do, and who gets no agent

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: `design/architecture.md` section 1 (non-goals), the operator's
notes.

## Context and Problem Statement

An agent with root rights invites doing more than updates. And not every device can carry an agent.

## Decision Drivers

* Smallest attack surface ([ADR-0005](0005-closed-verbs-without-shell.md)).
* Do not duplicate what already exists.

## Considered Options

The sources name no alternatives; these are non-goals.

## Decision Outcome

* No general remote administration: no shell, no file transfer, no configuration management.
* No metrics monitoring.
* Devices with vendor firmware (such as routers, firewalls, NAS, printers, ESPHome devices) and Home Assistant OS get
  no agent.
  Their HA integrations report their update state.
* Kubernetes workloads: updates come as pull requests (e.g. Renovate) through GitOps.

### Consequences

* Good, because the agent stays small; very small devices need none ([ADR-0003](0003-go.md)).
* Bad, because devices without an agent have no "hangs" view. The planned HA blueprint for that
  (`design/architecture.md` 8.5) is not built yet.

### Confirmation

No automatic check.
