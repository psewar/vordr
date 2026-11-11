---
status: accepted
date: 2026-10-29
decision-makers: kay ("implement the proposals")
---

# Roles for Home Assistant clients: observe, operate, admin

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: a security review in the operator's notes,
`design/architecture.md` 12.

## Context and Problem Statement

The host policy has the last word anyway. But Home Assistant, with HACS and add-ons, has a large attack surface. A
compromised HA should not be able to have the hub sign everything a host would accept.

## Decision Drivers

* Limit what a compromised HA can trigger.
* Existing clients keep working.

## Considered Options

The sources name no alternative.

## Decision Outcome

Three roles per client:

* `observe`: views only.
* `operate`: check, install, agent update. This is the default, also for clients approved before.
* `admin`: additionally reboot.

The hub refuses a command the role does not allow with `role_forbids`; `vordr-hub client-role <name> --role <role>`
sets the role.

### Consequences

* Good, because reboots need an explicit decision per client.
* Bad, because HA needs `admin` to offer reboots, and a compromised HA with `admin` can reboot hosts that allow it.

### Confirmation

`TestClientRoles` (role table, refusal leaves no run).
