---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# One repair issue per host problem

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/ha-integration.md` section 11, decision 1, and 6a.

## Context and Problem Statement

A problem shows on `binary_sensor.vordr_<id>_problem`. Should HA also show it under *Repairs*?

## Decision Drivers

* Problems should appear where one looks for things to do in HA.

## Considered Options

* entity and event only
* an additional repair issue

## Decision Outcome

Chosen option: an additional repair issue (`problem_<node>`) with reason and next step. It disappears as soon as the
problem is gone. Connection issues get their own: `protocol_mismatch`, `client_rejected`. The sources give no reason
beyond the proposal.

### Consequences

* Good, because problems are visible without an own dashboard.
* Bad, because every issue needs text in every language ([ADR-0027](0027-protocol-2-codes-and-translations.md)).

### Confirmation

`test_problem_events_and_repairs`, `test_problem_texts_complete_and_consistent`.
