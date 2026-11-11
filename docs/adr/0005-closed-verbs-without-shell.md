---
status: accepted
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# Closed set of verbs, no shell

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28, proposal),
`design/architecture.md` sections 2 and 6.

## Context and Problem Statement

The hub sends jobs that act as root on the hosts. What may a job ask for?

## Decision Drivers

* Whoever takes over the hub must not be able to bring commands or packages onto the hosts.
* Every job must be fully checkable.

## Considered Options

* general remote execution (as in central patch servers)
* a fixed list of verbs with fixed parameters

## Decision Outcome

Chosen option: a fixed list. The verbs are `check`, `install` (scope `security`, `all` or `app:<id>`), `reboot`,
`agent_update` and `cancel`. What is not on the list does not exist.

* No shell, no file transfer, no configuration changes from outside.
* Package names never come from the hub. `app:<id>` refers to an entry the agent reports itself from its
  `agent.yaml`.
* The agent checks every job strictly: unknown fields are `malformed`, a package name as scope is refused.

### Consequences

* Good, because a compromised hub can only trigger allowed runs.
* Bad, because every new capability needs a new verb in protocol, agent, hub and integration.

### Confirmation

* `VerifyJob` in `internal/protocol/job.go`; tests `TestVerifyJobRejects`, `TestParseScope`.
* Mutation probe when it was built: "package name allowed as scope" is caught.
