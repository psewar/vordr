---
status: accepted
date: 2026-10-30
decision-makers: kay (approval of the expansion on 2026-10-30)
---

# The hub in a container of its own

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: the operator's notes (analysis and expansion plan of
2026-10-30), the operator's project notes.

Supersedes [ADR-0019](0019-site-decision.md).

## Context and Problem Statement

When the hub runs on a shared host that Vördr itself updates, an install there can restart the hub in the middle of
its own run; the hub only catches that through its waiting time.

## Decision Drivers

* The hub should not be updated in the middle of its own run.
* Moving must not change any `agent.yaml`.

## Considered Options

* stay on the shared host
* a small container of its own

## Decision Outcome

Chosen option: a container of its own (1 core, 512 MB, 4 GB, unprivileged, SSH off). The hub moves with the same key
and state. Its own DNS name from ADR-0019 moves with it, so no agent changes. The old installation is stopped and
disabled; its files stay as the way back.

### Consequences

* Good, because an install on the shared host no longer touches the hub.
* Good, because the hub container is itself a managed host and can serve as a canary
  ([ADR-0058](0058-site-decision.md)).
* Bad, because certificate and PBS token cannot renew there ([ADR-0055](0055-files-copied-no-secret-access.md)).
* Bad, because a new container needs its own backup before the backup check lets it install.

### Confirmation

After a move all agents and HA reconnect without a change on their side; the move can be reversed.
