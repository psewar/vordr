---
status: accepted
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# Hub state in files, no database

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: `design/architecture.md` section 12.

## Context and Problem Statement

The hub must keep its register, approvals, runs and events across restarts.

## Decision Drivers

* Little to operate: no additional service.
* Proven in an earlier agent project.

## Considered Options

* a database
* files, as in the earlier project

## Decision Outcome

Chosen option: files. Host register `registry.json`, client register `clients.json`, event log `events.jsonl`
(append only), settings and rings in `hub.yaml`. The draft says "no database in phase 1".

### Consequences

* Good, because moving the hub means copying files ([ADR-0054](0054-hub-in-own-container.md): moved with the same state
  in 12 s).
* Good, because the event log is easy to copy off the host ([ADR-0060](0060-event-log-off-host.md)).
* Bad, because queries across many runs only go through the log.

### Confirmation

`TestRegistryAdmitAndPersist`; file mode 0600 in the mutation probe when it was built.

## More Information

Whether the hub stays without a database beyond phase 1 has not been decided explicitly.
