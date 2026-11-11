---
status: accepted
date: 2026-10-30
decision-makers: kay (2026-10-30, "ADR-0091 finde ich so gut"); proposed by Claude
---

# Firmware for devices without internet: the proxy fetches it, otherwise a short, narrow opening

## Context and Problem Statement

Some appliances are deliberately cut off from the internet (for example IoT devices that a firewall keeps off the
internet). The operator, 2026-10-30: such devices need no internet themselves; for an update either the
agent downloads the firmware for them, or a script grants internet access briefly.

Appliances are served by the proxy agent ([ADR-0089](0089-appliances-through-a-proxy-agent.md)), which already holds a
fresh configuration backup before every install ([ADR-0090](0090-appliance-installs-need-a-fresh-config-backup.md)).
Design: `design/proxy-agent.md` 4.6.

## Decision Drivers

* A device without internet stays without it; an update must not reopen it for good.
* The proxy must not gain write access to the firewall's rules, aliases or settings; it already holds valuable
  credentials.
* The vendor's signature or checksum is checked before firmware reaches a device.
* What is opened, is closed again by itself, also when the proxy crashes mid-run.

## Considered Options

* Give such devices internet permanently.
* The proxy edits the firewall (alias membership or rules) through a firewall account.
* The proxy fetches the firmware and offers it to the device locally; where a device can only download itself, a
  runtime table on the firewall, filled only through a forced SSH command with an allowlist and an expiry.

## Decision Outcome

Chosen: the third option, in this order (the operator, 2026-10-30). The printer joins the devices without internet: it
does not need it, printing happens locally (the operator).

1. **Local delivery first.** The proxy downloads the firmware from the vendor, checks signature or checksum, and offers
   it where the device accepts a file or a local repository (for example a BMC with a local vendor repository, or a
   printer or NAS by file upload). The device never sees the internet.
2. **Short opening otherwise** (for a device that can only download its update itself):
   * The firewall has a runtime table `vordr_update_window` and a rule above the block for devices without internet:
     addresses in the table may reach only their vendor's update hosts (one FQDN alias per vendor).
   * The proxy runs one forced command over SSH on the firewall (`command=` on its key). The command accepts only
     addresses from an allowlist stored on the firewall, adds them to the table, and removes them after at most
     30 minutes by itself. Runtime tables are not part of the saved configuration.
   * The opening belongs to the run: after backup and prechecks, closed after verification; every run first checks
     that the table is empty. Event and reason go into the log.

Rejected: permanent internet, because it defeats the block for the whole lifetime of the device. Editing the firewall
through an account, because the privileges for aliases or rules cover every alias and rule, so a compromised proxy
could open anything, including the management network.

### Consequences

* Good, because blocked devices stay blocked except for minutes, and then only towards their vendor.
* Good, because the proxy gains one narrow capability instead of firewall access.
* Bad, because the firewall gets a small script, an allowlist and a key that must be kept with its configuration.
* Bad, because FQDN aliases resolve vendor hosts by DNS; a vendor using broad CDNs widens the opening.
* Bad, because local delivery needs a download path per vendor and egress from the proxy to vendor download hosts.

### Confirmation

Tests of the forced command: it refuses an address not on its list, and an entry is gone after the expiry without any
further call. A run that finds a non-empty table refuses to start. In a live opening the device reaches only the
vendor's hosts (firewall log).
