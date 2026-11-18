---
status: proposed
date: 2026-11-18
decision-makers: proposed by Claude (2026-11-18), for the maintainer to accept or reject
---

# The hub device counts the hosts with updates and a reboot due

## Context and Problem Statement

Vördr is meant to keep a whole network up to date. Home Assistant shows each host as a device with its own update
entities and counters, and the hub device has counts over all hosts: connected hosts, hosts with a problem, pending
approvals. Whether the network as a whole is up to date, which hosts still have something to install, and which wait
for a reboot, could only be read by looking at every host, or with a template over all update entities that has to
know their names and treat "unknown" right.

A failed check makes a host's counters "unknown", never "up to date"
([ADR-0101](0101-package-lists-stay-out-of-the-recorder.md) keeps lists out of the history; the host's own sensors already
keep unknown apart). A count over all hosts must not lose that: a host that could not be checked is not up to date.

How does Home Assistant show whether all hosts are up to date, and which are not?

## Decision Drivers

* One look, and one condition for an automation or a dashboard, answers "is everything up to date?".
* An unknown host is not counted as up to date, and is not hidden either.
* The same fields as the host's own sensors, so the counts never disagree with them.
* No new data on the wire: older hubs and agents keep working.

## Considered Options

1. Nothing new: templates over the host entities, written by each user.
2. Counts on the hub device, computed in Home Assistant from the host views it already has: hosts with updates, with
   security updates, with a reboot due; each with the list of hosts, and a separate list of hosts that cannot be said.
3. The same counts computed by the hub and sent in its own view.

## Decision Outcome

Chosen option: **2**.

* `sensor.vordr_hub_hosts_with_updates`: the hosts with system updates (`pending` of their package source), outdated
  container images (`containers.outdated`) or outdated pip/npm packages (`packages.outdated`); attribute `hosts`, per
  host the kinds above zero (`system`, `images`, `packages`).
* `sensor.vordr_hub_hosts_with_security_updates`: the hosts with `security` above zero; attribute `hosts` with the count.
* `sensor.vordr_hub_hosts_needing_reboot`: the hosts whose reboot is required; attribute `hosts` with `since`.
* Attribute `unknown`: the hosts for which it cannot be said: no report yet, or the system check failed (`pending`
  absent while the host has a package source). They are not counted, unless they have something else to count (an
  inventory with outdated images comes before the first report). For the reboot count, only a host without a report
  is unknown; a reporting host without a reboot signal (a container) has no reboot to wait for. A host without a
  package source has no system or security updates.
* Images and pip/npm packages count where the host compares them. While it does not (switched off in its policy, no
  container engine), the hub sends no count; that is a fact about the host, not a failed check, so it makes no
  host unknown.
* The lists are not recorded (ADR-0101); the history keeps the counts.

### Consequences

* Good, because "0 and an empty `unknown`" is the one condition for "every host was checked and has nothing to update".
* Good, because the counts read the same fields as the host's own sensors and follow every push without a new message.
* Good, because nothing changes for the hub or the agents.
* Bad, because outdated pip/npm packages and container images that stay manual count as updates, also when no install
  button would take them yet; the attribute says per host which kind it is, so a template can leave a kind out.
* Neutral: option 3 would give other clients of the hub the same counts, but needs a hub release for what Home
  Assistant can already compute.

### Confirmation

* `ha/tests/test_entities.py::test_the_hub_counts_the_hosts_to_update`: eight hosts (updates of each kind, security
  updates, a reboot due, a failed check, no report yet, no package source, images and packages not compared, an
  inventory before the first report) give the counts, the lists and `unknown`; a push of a host that became current
  changes them.
* `ha/tests/test_texts.py` checks the names in every language.

## More Information

* Design: [design/architecture.md](../../design/architecture.md), the entity table of the hub device.
* [ADR-0101](0101-package-lists-stay-out-of-the-recorder.md): lists stay out of the recorder.
