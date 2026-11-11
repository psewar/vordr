---
status: superseded by ADR-0088
date: 2026-10-30
decision-makers: kay ("1. Go")
---

# Automatic security updates on every Linux host, unattended-upgrades off

Supersedes [ADR-0058](0058-site-decision.md).

**Superseded by [ADR-0088](0088-hypervisor-updates-by-button-only.md)** in one point: the hypervisor gets updates by
button only. Everything else still holds there.

## Context and Problem Statement

ADR-0058 introduced automatic security updates with canaries; some hosts stayed on the button. The agents report
`unattended_upgrades: enabled` where it is on, among others on hosts that are on the button. On such hosts two
mechanisms install updates. Only Vördr checks the backup before and the services after a run, uses canaries and shows the result
in HA; both compete for the same lock (`rejected_busy`). Design 16.5 had left this open.

## Decision Drivers

* One mechanism per host.
* Every automatic install with backup check, health checks and canaries.
* A security-relevant host under continuous load should get security updates automatically, but checked. It is no
  canary for the same reason.
* Vördr itself switches nothing on a host (design 10.3); the operator's deploy tool does.

## Considered Options

* keep both
* unattended-upgrades only on hosts without Vördr automation
* Vördr automation on every Linux host, unattended-upgrades off

## Decision Outcome

Chosen option: Vördr on every Linux host (the operator, 2026-10-30).

* The canaries stay those of ADR-0058.
* All other Linux hosts follow after the canaries, now including the hosts that had stayed on the button: updates
  pending for 24 h, while every canary is free of problems. Scope `security`, inside the host's window in host time,
  reboots never automatic. A host whose policy allows only `security` keeps installing security updates only, also by
  button.
* The deploy tool switches unattended-upgrades off in the same step as the automation: a file in `/etc/apt/apt.conf.d`
  read after the distribution's files sets `APT::Periodic::Unattended-Upgrade "0"`. The tool checks the result with
  `apt-config`; the package stays, deleting the file undoes it.
* Windows hosts have no automatic installs (winget knows no security classification).

### Consequences

* Good, because every automatic update runs with the same safeguards and shows up in HA.
* Bad, because security updates on these hosts now depend on the hub; while it is down nothing installs (HA shows the
  hub offline).
* Bad, because packages from repositories Vördr does not count as security sources (for example Docker's own) are
  only installed with scope `all`. Whether unattended-upgrades took them before was not checked.

### Confirmation

The deploy tool stops unless `apt-config` reports `UU='0'` afterwards. The ring rollout tool checks that the canaries
are free of problems, that every host understands `auto_scope` (agent 0.8.2 or newer) and that the agent version it
installs has run on every canary for 24 h. The agents report `unattended_upgrades` in every check.
