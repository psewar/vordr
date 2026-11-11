---
status: accepted
date: 2026-11-02
decision-makers: kay (2026-11-02, accepted); proposed by Claude
---

# Package lists stay in the state but out of the recorder

## Context and Problem Statement

The Home Assistant sensors for malicious, vulnerable and outdated packages carry lists as attributes:

* `findings`: up to `MaxFindings` (50) entries, sorted by the hub, most important first;
* `locations` and `undecided`: where the agent looked and what it could not decide;
* `updates`: up to `MaxUpdates` (100) entries.

Home Assistant's recorder stores a state's attributes only up to 16 KiB (`MAX_STATE_ATTRS_BYTES`). Above that it logs
a warning on every state change and stores none of the attributes. A host with many findings goes over the limit
(about 30 KB with 50 findings).

How do these sensors stay recordable without losing what automations and dashboards read?

## Decision Drivers

* What an automation, a template or a dashboard reads now stays complete.
* The history keeps what is worth comparing over time: the counts and when the host was checked.
* No warnings in the log.
* Neither hub nor protocol changes for a limit of one consumer.

## Considered Options

1. Trim the lists in the integration: the most important N entries, or as many as fit a byte budget, plus a count of
   the rest.
2. Lower `MaxFindings` and `MaxUpdates` in the hub.
3. Keep the lists in the state and leave them out of the recorder with the entity's `_unrecorded_attributes`.

## Decision Outcome

Chosen: option 3, since integration 0.17.1.

* The findings sensors leave `findings`, `locations` and `undecided` out of the recorder. The outdated packages sensor
  leaves out `updates`.
* The state keeps the lists as the hub sends them. The recorder stores the state (the count) and the other attributes:
  `checked_at` and `packages_total`, or `outdated_at` and `min_age_days`.

Rejected:

* **Option 1,** because the live state would lose entries that automations and dashboards could use. A byte budget
  depends on the length of names and advisory ids, so it would cut a different number of entries each time. And a
  recorded list is of little use: the counts show the trend, and the hub has the current list.
* **Option 2,** because it shrinks the list for every consumer of the host view to fit one consumer's limit, needs a
  hub release, and still guarantees nothing: entries vary in length.

### Consequences

* Good, because the recorder warnings stop and the history keeps the counts and the time of the last check.
* Good, because the live state stays complete.
* Good, because only the integration changes.
* Bad, because the history cannot say which packages were affected at an earlier time; only the current list is
  available.
* Bad, because the state machine and the frontend still carry up to about 30 KB per sensor. That is acceptable at the
  rate these sensors change: once per check of the host.
* Neutral: a new list attribute that can grow belongs in `_unrecorded_attributes` as well.

### Confirmation

`test_package_lists_stay_out_of_the_recorder` builds full lists (50 findings, 100 updates) that are bigger than the
recorder's limit. It runs the recorder's own attribute encoding and checks that the other attributes are stored
without the lists. Without the change the test fails. A live check: after a restart the recorder logs no more warnings
about these sensors.
