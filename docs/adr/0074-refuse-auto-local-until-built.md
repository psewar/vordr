---
status: accepted
date: 2026-10-30
decision-makers: kay ("yes, refuse it")
---

# `install.auto: local` is refused until it is built

Extends [ADR-0036](0036-automatic-install-in-rings.md).

## Context and Problem Statement

The design (10.3) describes `install.auto: local`: the agent triggers runs in its own window, even without the hub.
Only `hub` was built ([ADR-0036](0036-automatic-install-in-rings.md)). The policy still accepted `local`, but nothing
acts on it: only the hub's planner reads the value, and only for `hub` (`internal/hub/auto.go`). A host configured with
`local` would never install automatically, and nothing would say so. This came up during the ADR backfill.

## Decision Drivers

* A configuration must do what it says; no silent switch-off (the operator's rule: exceptions only explicitly).
* Report state instead of leaving someone to guess why nothing happens.
* No deployed host used `local` when this was decided.

## Considered Options

* build `local` now
* refuse `local` in the policy until it is built
* leave it as it is

## Decision Outcome

Chosen option: refuse it (the operator, 2026-10-30). The policy reports `install.auto: "local" is not built yet -
nothing would start a run; use hub or off`. Allowed values are `off` and `hub`. Building `local` later needs a new ADR
that lifts this one.

### Consequences

* Good, because a policy that would never act cannot be deployed unnoticed: `check-config` reports it, and the agent
  unit does not start with it (`check-config` runs as `ExecStartPre`).
* Bad, because an `agent.yaml` with `local` now stops the agent at start, and a self-update fails its probe on such a
  host (the probe runs `check-config` on the host's own `agent.yaml`). No host used `local` at the time.

### Confirmation

`TestAutoLocalIsRefused` in `internal/policy`: exactly one issue, on `install.auto`.
