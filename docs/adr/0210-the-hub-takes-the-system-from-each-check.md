---
status: accepted
date: 2026-11-21
decision-makers: kay (asked for a pull request on the gap on 2026-11-21, "Ja, PR bauen"; merged the implementation on 2026-11-21; accepted on 2026-11-21, "Ja, PR bauen"); proposed by Claude
---

# The hub takes the system from each check

## Context and Problem Statement

The hello carries the host's facts (`facts.Host`), among them `os` and `os_version`. The hub shows `os` as the host's
system: the update entity's `installed_version`, the device's model in Home Assistant, `vordr-hub list` and `show`.

A host agent reads its facts when it starts, and a new system usually comes with a reboot that restarts it. A proxy
instance ([ADR-0089](0089-appliances-through-a-proxy-agent.md)) reads the device's product and firmware once, at its
start. A release install on the device, through Vördr
([ADR-0205](0205-the-firewall-installs-through-its-own-updater-by-button.md)) or in the device's own interface,
changes the firmware without restarting the instance. The hub keeps showing the old release as installed until the
instance restarts. A device that did not answer at the instance's start shows the driver's name until the next start.

How does the hub's view of the system follow the device?

## Decision Drivers

* The system shown is the one the last check found.
* No change to the protocol's messages: an older hub refuses a field it does not know.
* The hub keeps no knowledge of a driver's own facts.
* Values from the agent are bounded before the hub keeps them
  ([ADR-0191](0191-the-hub-bounds-every-field-of-an-agents-hello-before-it-keeps-it.md),
  [ADR-0195](0195-the-hub-bounds-reports-inventories-and-the-home-assistant-hello.md)).

## Considered Options

* A source names the system among its report's facts at every check, and the hub takes it for the host.
* The proxy instance restarts, or logs in again, after an install that changed the firmware, so that a new hello
  carries the new facts.
* A new message or report field for changed facts.
* The hub reads the system from the driver's own facts, such as `release_installed`.

## Decision Outcome

Chosen: a source names the system among its report's facts, and the hub takes it for the host.

* Two facts of a source report, in the form of `facts.Host`: `os` (the system as it names itself, e.g. product and
  firmware) and `os_version` (`source.FactOS`, `source.FactOSVersion`). A proxy instance's source names both at every
  check the device answered, as its hello does.
* On a report of an approved host, the first source that was checked (state other than `check_failed`) and names both
  sets the host's `os` and `os_version`, each within the bound of the hello's facts. The view, `vordr-hub list` and
  `show` follow. A report without them leaves what the hello said; the next hello reads the device again.
* Nothing in the hub decides on `os` or `os_version`; they are shown.
* An older hub keeps the two as facts of the source and shows the hello's system as before. An older agent names
  neither.

Rejected:
* **Restart or log in again:** a restart to carry a fact, and a release installed in the device's own interface would
  still show the old system until the next start.
* **A new message or field:** a protocol change with a feature to announce it, for what the report's bounded facts
  already carry.
* **The driver's own facts:** the hub would have to know each driver's keys and how they make a system name.

### Consequences

* Good, because the installed system follows the device with the report after an install, and after an update by hand
  at the next check.
* Good, because a device that did not answer at the instance's start gets its system with the first check it answers.
* Good, because messages, features and older hubs stay as they are.
* Bad, because the system now comes from the hello and from reports; the newer one stands until the next hello.

### Confirmation

* `TestTargetCheckNamesTheSystem` in `internal/agent`: the source report names `os` and `os_version` from the device
  as the hello does, and neither when the check failed.
* `TestReportTakesTheSystem` in `internal/hub`: a report whose source names both changes the host's facts and the
  view's `installed_version`; a failed source, a value beyond the bound or a report without them changes nothing.
