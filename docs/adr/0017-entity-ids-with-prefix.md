---
status: accepted
date: 2026-10-28
decision-makers: kay
---

# Entity IDs with the prefix `vordr_`, unchanged across every rebuild

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's notes (deviation from draft
v0.2), `design/ha-integration.md` sections 6 and 6a.

## Context and Problem Statement

The draft planned `update.<id>_system`. Other integrations often name their entities after the same host name.

## Decision Drivers

* No collision with entities of other integrations.
* Dashboards, automations and the auto-update blueprint depend on the IDs.

## Considered Options

* `*.<id>_<object>` (draft v0.2)
* `*.vordr_<id>_<object>`

## Decision Outcome

Chosen option: `*.vordr_<id>_<object>`, English, set explicitly (`<id>` is the host name with `_` instead of `-` and
`.`). When moving from MQTT to the own integration ([ADR-0023](0023-own-ha-integration.md)) the IDs deliberately stayed
the same; the integration sets them itself before adding the entity.

### Consequences

* Good, because there are no collisions, and the switch kept all IDs.
* Bad, because the integration must set the IDs itself instead of leaving them to HA. That only works while the ID is
  free.

### Confirmation

`test_entity_ids_as_mqtt_had_them` in `ha/tests/test_entities.py`.
