---
status: accepted
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# Administration and approval only locally at the hub

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: `design/architecture.md` sections 2, 10.1 and 12.

## Context and Problem Statement

Hosts must be approved, rejected and forgotten. Where does that happen, and who may do it?

## Decision Drivers

* Smallest attack surface on the hub.
* Whoever takes over HA must not be able to admit hosts.

## Considered Options

* web interface on the hub
* approval in Home Assistant
* command line on the hub over a local Unix socket

## Decision Outcome

Chosen option: command line on the hub. `vordr-hub approve <node> --fingerprint <fp>` after comparing with
`vordr-agent identity` on the host. The socket has mode 0600 inside a 0700 directory. The hub has no web interface and
refuses browser connections (with `Origin`). HA cannot admit hosts. The same applies to HA clients (`approve-client`,
[ADR-0023](0023-own-ha-integration.md)).

### Consequences

* Good, because admitting a host requires access to the hub host.
* Bad, because every approval is manual work at the hub (a tool that compares the fingerprints can do it).

### Confirmation

`TestAdminSocket`, `TestHubRefusesBrowserOrigin`, `TestTooManyPending`; mutation probe when it was built ("approval
without fingerprint", "browser origin allowed" caught).
