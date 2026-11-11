---
status: accepted
date: 2026-10-30
decision-makers: Claude (proposal); accepted by kay with the merge
---

# New message types only after a feature the hub announces

## Context and Problem Statement

The agent gets a new message (`inventory`, later `vet`). An older hub drops the connection on a message type it does
not know. How does the agent know what it may send?

## Decision Drivers

* The operator's rule: report state instead of deriving it from errors; do not try and detect the drop.
* Updates of hub and agents do not run in lockstep (rings, canaries).
* The hub reads the `hello` strictly (unknown fields are refused).

## Considered Options

1. Only the order "hub before agent" (the release tool, [ADR-0049](0049-site-decision.md)).
2. The agent reads the hub's version and compares.
3. The hub lists its `features` in every `status`; the agent only sends a newer type when it is listed.

## Decision Outcome

Chosen option: **option 3**, in addition to option 1. Every `status` (also the one at approval) carries `features`.

### Consequences

* Good, because the order of updates no longer matters for the connection; a new agent at an old hub simply sends no
  inventory.
* Good, because a feature can depend on the setup (`vet` only with OSV data).
* Bad, because old agents read `status` leniently, so new fields are only free in this direction.

### Confirmation

Tests `TestInventoryToAHubThatAcceptsIt`, `TestNoInventoryToAnOlderHub`; mutation probe "approval status without
features".

## More Information

`design/architecture.md` 6.2.

Merged in German as ADR-0103 (status "proposed", accepted with the merge); later renumbered, translated and set to
accepted.
