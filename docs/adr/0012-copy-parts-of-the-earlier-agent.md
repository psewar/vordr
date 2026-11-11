---
status: accepted
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# Copy parts of the earlier agent instead of a shared library

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: `design/architecture.md` section 14.

## Context and Problem Statement

Identity, tunnel, self-update and the Windows service already exist in an earlier agent project (Go). How do they get
into Vördr?

## Decision Drivers

* Both projects should release independently.

## Considered Options

* a shared Go module
* copy and adapt

## Decision Outcome

Chosen option: copy and adapt. A shared module only pays off once both grow in parallel. From the server side of the
earlier project (Python) the hub only takes patterns (register with approval, rollout with canary, event log), no code.

### Consequences

* Good, because releases do not depend on each other.
* Bad, because a fix in one project must be carried to the other by hand.

### Confirmation

No automatic check. The table in `design/architecture.md` 14 lists what came from where.
