---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# Remove MQTT from the code after the switch

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/ha-integration.md` section 11, decision 4.

## Context and Problem Statement

After the switch to the own integration ([ADR-0023](0023-own-ha-integration.md)) HA only spoke over `/v1/ha`. The MQTT
part of the hub was unused.

## Decision Drivers

* No dead code with its own attack surface and dependency.

## Considered Options

* keep MQTT as a second path
* remove MQTT completely

## Decision Outcome

Chosen option: remove it completely. `internal/hub/ha.go`, `mqtt.go` and the paho dependency are gone. If an old
`hub.yaml` still has an `mqtt` section, `check-config` says what to do instead.

### Consequences

* Good, because the hub binary shrank from 8.5 to 8.1 MB, and no MQTT account or password has to be kept any more.
* Bad, because there is no way back to MQTT without new code.

### Confirmation

`TestParseConfigLimitsAndRemovedMQTT`; the former MQTT checks now run against the views (`view_test.go`); mutation
probe after the removal 18 of 18.
