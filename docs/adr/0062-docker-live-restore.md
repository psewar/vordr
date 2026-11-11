---
status: rejected
date: 2026-10-30
decision-makers: kay; proposed and examined by Claude
---

# Docker `live-restore` to keep containers running during Docker updates

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: the operator's notes (expansion plan of 2026-10-30).

## Context and Problem Statement

Docker updates stop running containers ([ADR-0061](0061-restart-containers-after-install.md)). Docker's `live-restore`
keeps containers running while the daemon restarts.

## Decision Drivers

* Containers should survive a Docker update.

## Considered Options

* switch on `live-restore` on hosts with Docker
* let the agent restart containers after the install

## Decision Outcome

Rejected. Docker only supports `live-restore` across patch versions. The pending updates were minor jumps. The
agent restarts containers instead.

### Consequences

* Good, because no setting that does not help for the updates that actually come.
* Bad, because containers still stop briefly during a Docker update.

### Confirmation

Not applicable.
