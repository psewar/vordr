---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# Device name `Vördr <node>`

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/ha-integration.md` section 11, decision 2.

## Context and Problem Statement

A host can appear twice in HA's device list: once from Vördr, once from other integrations that already know it.

## Decision Drivers

* Tell devices with the same name apart.

## Considered Options

* host name as device name
* "Vördr <node>"

## Decision Outcome

Chosen option: "Vördr <node>". The operator confirmed it for the own integration, because HA's device list is
global. Entity IDs are not affected ([ADR-0017](0017-entity-ids-with-prefix.md)).

### Consequences

* Good, because devices are unique in the device list.
* Bad, because an entity name that repeats the prefix shows it twice (the agent entity is therefore just called
  "Agent").

### Confirmation

Device names in the hub's device view (`internal/hub/view.go`) and in the HA tests.
