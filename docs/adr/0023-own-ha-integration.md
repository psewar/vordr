---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# Own HA integration as an approved client over `/v1/ha` instead of MQTT

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/ha-integration.md` sections 1 and 3–8, the
operator's project notes ("plan the integration", approval of 2026-10-29).

Supersedes [ADR-0009](0009-ha-via-mqtt-discovery.md).

## Context and Problem Statement

HA over MQTT discovery has three flaws. The devices sit under "MQTT" instead of an own integration. An MQTT `update`
entity cannot show "unknown". Commands over MQTT are not authenticated end to end; that needs a broker ACL, and an ACL
affects every account on the broker.

## Decision Drivers

* Authenticated commands from HA without a broker ACL.
* "Unknown" as a real state.
* Entity IDs stay ([ADR-0017](0017-entity-ids-with-prefix.md)).

## Considered Options

* stay with MQTT and add a broker ACL
* own integration as a client of the hub

## Decision Outcome

Chosen option: own integration `custom_components/vordr`.

* It creates its own Ed25519 key pair and, like a host, is approved only at the hub
  (`vordr-hub approve-client <name> --fingerprint <fp>`).
* It connects to `/v1/ha` over WebSocket (same port and certificate as the tunnel), receives state as push and sends
  commands from a fixed list over the same connection.
* Agents still speak no MQTT.

### Consequences

* Good, because the hub needs no MQTT account; no MQTT account can trigger anything.
* Good, because `latest_version = None` shows "unknown".
* Bad, because the integration is a piece of Python that must keep up with HA versions.
* Bad, because display names follow HA's system language; under MQTT they were always German. Whether they should be
  fixed to German is left open.

### Confirmation

`test_real_hub_and_agent` (real binaries against the integration), `TestClientRegistry`,
`TestCommandNeedsTheApprovedKeyNow`; mutation probe 18 of 18; `test_entity_ids_as_mqtt_had_them` (the switch keeps
all IDs, `design/ha-integration.md` 12).
