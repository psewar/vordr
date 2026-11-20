---
status: accepted
date: 2026-11-19
decision-makers: kay (2026-11-19, asked that the entities get their categories; accepted with the merge); proposed by Claude
---

# Entities carry a category by their role

## Context and Problem Statement

Home Assistant sorts a device's entities by their entity category. An entity without one is in the device page's main
sections and in automatically generated dashboards. `diagnostic` entities show how a device or its connection is,
`config` entities change how it works. Both get their own sections on the device page and stay out of generated
dashboards.

The integration gave a category to two hub entities only, "Connection" and "Vulnerability data". Every host device
showed "Connected", "Last report", "Last backup" and the button "Check now" beside its updates. The update entities had
no category of their own either, so Home Assistant's update platform picked one: `config` where the entity can
install, `diagnostic` where it cannot. A host's "System packages" were under "Configuration" on a host that may install
and under "Diagnostic" on one that may not. They moved between the two when the host's policy changed, and they were
missing from generated dashboards, although they are what Vördr is for.

## Decision Drivers

* The device page and generated dashboards first show what a host's maintenance is about.
* The categories mean what Home Assistant says they mean: `diagnostic` shows a state and changes nothing, `config`
  changes how something works.
* An entity stays in one section. It does not move when a capability or the host's policy changes.
* Entity IDs, states, attributes and automations stay as they are.

## Considered Options

* a category by role, with the update entities of a host's maintenance set as primary
* a category by role, with the update entities left to Home Assistant's choice
* no categories, as before

## Decision Outcome

Chosen: a category by role, with the update entities of a host's maintenance set as primary.

| Role | Category | Entities (English names) |
|---|---|---|
| What a host's maintenance is about | none | the updates "System packages", "Packages <location>" and "Container images"; "Update problem", "Reboot required"; "Last run", "Pending updates", "Security updates", "Vulnerable packages", "Malicious packages", "Outdated packages", "Outdated images"; the buttons "Install security updates", "Reboot" and "Install <app>"; on the hub device its counts of hosts and "Pending approvals"; on the device "Vördr Home Assistant" "Updates ready" and "Last run" |
| How Vördr sees a host or the hub | `diagnostic` | per host "Connected", "Last report" and "Last backup"; on the hub device "Hub" (its connection), "Connection" and "Vulnerability data" |
| How Vördr itself works | `config` | the button "Check now" (it refreshes Vördr's view and changes nothing on the host); the update "Agent" (Vördr's own software), also on a host that cannot install it; the switch "Pause Home Assistant updates" |

The update entities set their category themselves instead of leaving it to the update platform, so that none moves
with a capability. Home Assistant writes an entity's category into its registry each time the integration loads, so an
existing installation takes the new sections on the next start without edits.

Rejected:

* **Home Assistant's choice for the update entities,** because "System packages" would sit under "Configuration" or
  "Diagnostic" depending on the host's policy, move when it changes, and stay out of generated dashboards.
* **No categories,** because every host device mixes how Vördr sees the host with what the host needs, and generated
  dashboards show every connection and timestamp entity of every host.

### Consequences

* Good, because a host's device page and generated dashboards lead with updates, problems and reboots.
* Good, because no entity changes its section when a capability or a policy changes.
* Bad, because a generated dashboard no longer shows "Connected", "Last report", "Last backup", "Check now", "Agent",
  the hub's connection or the pause switch. Whoever uses them there adds them to a dashboard of their own; dashboards
  built by hand keep them.
* Bad, because the system, package and container updates differ from Home Assistant's default for update entities,
  which treats an installable update like device firmware, under "Configuration".

### Confirmation

`ha/tests/test_entities.py`: the category of every entity of a host, the hub and the device "Vördr Home Assistant" in
the entity registry, for a host that may install and one that may not, and that no entity is left undecided
(`test_entity_categories_by_role`); existing registry entries that take the new category on load under the same entity
ID (`test_existing_entities_take_their_category`).
