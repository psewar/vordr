---
status: accepted
date: 2026-10-30
decision-makers: kay (2026-10-30, "Mesh Empfehlung passt für mich"); proposed by Claude
---

# A mesh Wi-Fi keeps its vendor self-update; Vördr reports and backs up

## Context and Problem Statement

A mesh Wi-Fi's master can update its firmware by itself on its own schedule; the nodes may have it off and still run
the same version, because the master brings the nodes along. The proxy agent
never installs on appliances by itself ([ADR-0089](0089-appliances-through-a-proxy-agent.md)). For hosts, Vördr
switched unattended-upgrades off so that one mechanism installs
([ADR-0080](0080-security-updates-everywhere-unattended-upgrades-off.md)). Should the mesh follow that and switch its
self-update off?

## Decision Drivers

* Security fixes for the routers should arrive quickly.
* One installer per device; no two mechanisms competing.
* A failed update must be noticed and must not lose the configuration.

## Considered Options

* Keep the master's self-update; Vördr reports.
* Switch it off; updates only by button through the proxy.
* Switch it on for every unit.

## Decision Outcome

Chosen: keep the master's self-update (the operator, 2026-10-30).

* Vördr does not install on the mesh. Its proxy instance per unit reports the firmware, backs up the configuration
  daily ([ADR-0090](0090-appliance-installs-need-a-fresh-config-backup.md)), and raises a problem when a unit's version
  differs from the others or an offered update stays pending for 7 days.
* The driver reports `self_update: on` as a fact, so HA shows who installs.
* This is not the double operation ADR-0080 avoids: there two installers competed for one lock; here only the vendor
  installs.

Rejected: switching it off, because Vördr never installs on appliances by itself, so fixes would wait for someone to
press a button. Switching it on for every unit, because the master already brings the nodes along.

### Consequences

* Good, because router fixes arrive within a day, without anyone pressing a button.
* Good, because the configuration backup is at most a day old when the vendor updates.
* Bad, because a failed update in the vendor's own schedule takes the Wi-Fi down until someone notices.
* Bad, because there is no backup taken right before the vendor's own update, only the daily one.

### Confirmation

The proxy's mesh instances report `self_update` and the firmware per unit; a test that a differing unit or a pending
offer older than 7 days raises a problem.
