---
status: accepted
date: 2026-10-28
decision-makers: kay
---

# Agent as root, hub as its own user with tight sandboxing

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: `deploy/README.md` section "Abschottung der Dienste".

## Context and Problem Statement

Both services need systemd units. How many rights do they get, and how tightly are they sandboxed?

## Decision Drivers

* apt needs root; the agent installs packages and replaces itself.
* No sandboxing that disturbs a package run halfway.
* The hub needs no rights on the system.

## Considered Options

The sources name no alternative to root for the agent.

## Decision Outcome

* **Hub:** own system user `vordr-hub`, no capabilities, read-only system, only its own state and runtime directory,
  system calls limited to `@system-service`. `systemd-analyze security`: 1.3.
* **Agent:** root, only restrictions that never disturb a package run (private `/tmp`, address families, realtime,
  architecture). `systemd-analyze security`: 8.5, on purpose.
* Neither unit starts with a broken configuration (`check-config` as `ExecStartPre`).

### Consequences

* Good, because a compromised hub has few rights on its own host.
* Bad, because the agent is root; its protection is the closed set of verbs, the local policy and the signatures
  ([ADR-0005](0005-closed-verbs-without-shell.md), [ADR-0006](0006-local-policy-decides.md)).

### Confirmation

`systemd-analyze verify` and `systemd-analyze security --offline` for both units; `install-hub.sh` runs
`check-config` as user `vordr-hub`.
