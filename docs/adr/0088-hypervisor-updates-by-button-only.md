---
status: superseded by ADR-0108
date: 2026-10-30
decision-makers: kay (2026-10-30, "Yes, add [the hypervisor]" to "by button only, no reboot"); proposed by Claude
---

# The hypervisor gets updates by button only, and never a reboot through Vördr

Supersedes [ADR-0080](0080-security-updates-everywhere-unattended-upgrades-off.md) in one point: the hypervisor is
the exception to "automatic security updates on every Linux host". Everything else in it still holds and is restated
below.

## Context and Problem Statement

Every Linux guest may have the agent while the hypervisor that runs them does not. Then nothing reports the
hypervisor's pending updates, security updates included, and without unattended-upgrades nothing installs them
either.

ADR-0080 says every Linux host with Vördr gets security updates automatically in its window. The hypervisor carries
every guest: an upgrade of its virtualization stack restarts its management services and can affect all guests at once,
and PBS backs up the guests, not the host.

## Decision Drivers

* Pending security updates on the hypervisor must show up in Home Assistant like everywhere else.
* A change to the host that carries everything happens when a person chooses it, not in an automatic window.
* A reboot of the hypervisor takes every guest down; it stays a manual step.

## Considered Options

* Like every other Linux host (ADR-0080): automatic security updates in the window, after the canaries.
* Report and install by button only, no reboot through Vördr.
* Report only (no install verb at all).

## Decision Outcome

Chosen: report and install by button only, no reboot through Vördr (the operator, 2026-10-30).

* `install.auto: "off"`; scopes `security` and `all` (default `all`, `full-upgrade`), so Home Assistant offers both the
  install button and "Install security updates". Runs outside the window with a reason work as everywhere
  ([ADR-0087](0087-runs-outside-the-window-with-a-reason.md)).
* `reboot.allowed` is not set: the agent does not offer the verb. After a kernel update HA shows "reboot required"; the
  reboot is done by hand.
* Health checks after a run: the cluster file system, the API daemon, the web proxy and the statistics daemon.
* Backup check: the host is listed with `without` and the reason (PBS backs up the guests only).

What still holds from ADR-0080: every other Linux host gets security updates automatically, scope `security`, in its
window in host time, after the canaries, with backup and health checks; a host whose policy allows only `security`
installs security updates only; reboots are never automatic; the deploy tool switches unattended-upgrades off in the
same step as the automation and checks it with `apt-config`; Windows PCs have no automatic installs.

### Consequences

* Good, because the hypervisor's updates are visible in HA, with the same checks as everywhere when someone installs.
* Good, because nothing changes the host that carries all guests without a person deciding.
* Bad, because security updates on the hypervisor wait for someone to press the button. The "stale" problem after
  7 days is the reminder.
* Bad, because a hypervisor reboot remains a manual step outside Vördr.

### Confirmation

The operator's deploy tool keeps the hypervisor out of its automation list and gives it no reboot section; the hub view
shows `auto: off` and no reboot capability for it. In HA a live check shows the pending updates with their security
count, the install and security buttons, and no reboot button.
