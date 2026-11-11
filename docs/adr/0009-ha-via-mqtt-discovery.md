---
status: superseded by ADR-0023
date: 2026-10-28
decision-makers: kay; proposed by Claude ("agent without MQTT")
---

# Home Assistant via MQTT discovery from the hub, agents without MQTT

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28, proposal),
`design/architecture.md` sections 2, 8 and 14.

**Superseded by [ADR-0023](0023-own-ha-integration.md).** The part "agents without MQTT" still holds there.

## Context and Problem Statement

Hosts should appear in Home Assistant as devices with entities. In an earlier agent project every agent publishes
its entities itself via MQTT discovery.

## Decision Drivers

* A compromised host must not be able to write to HA.
* Little new code; HA discovery via MQTT is proven.

## Considered Options

* every agent speaks MQTT (as in the earlier agent)
* only the hub speaks MQTT

## Decision Outcome

Chosen option: only the hub publishes, with its own MQTT account. Agents have no MQTT access and only need a route to
the hub. Commands from HA arrive on `vordr/host/<node>/cmd`; the hub checks them against verbs and policy before
signing.

### Consequences

* Good, because a compromised host can only fake its own state.
* Bad (see ADR-0023), because the devices sat under "MQTT"; an MQTT `update` entity cannot show
  "unknown"; commands over MQTT were not authenticated end to end. That would have needed a broker ACL before
  installing, and an ACL affects every account on the broker.

### Confirmation

Not applicable; superseded. The code was removed with [ADR-0026](0026-remove-mqtt.md).
