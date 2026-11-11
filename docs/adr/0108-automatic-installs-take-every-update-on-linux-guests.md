---
status: accepted
date: 2026-11-04
decision-makers: kay (2026-11-04, chose this among the variants against notification fatigue, after ADR-0106 and ADR-0107); proposed by Claude
---

# Automatic installs take every pending update on Linux guests, not only security updates

Supersedes [ADR-0088](0088-hypervisor-updates-by-button-only.md). It changes one point there:
the scope of automatic runs on Linux guests. Everything else in ADR-0088 still holds and is restated below.

## Context and Problem Statement

Under ADR-0080 and ADR-0088, automatic runs take the scope `security` only. Vördr counts an update as a security
update when it comes from a distribution's security archive (a suite ending in `-security`). In practice:

* Updates from vendor repositories never carry that label (for example a container runtime's plugins, a VPN client, a
  CLI tool, the browser of a kiosk). Neither do the updates of a distribution's point release. Such updates wait for a
  button, and Home Assistant shows them every day.
* Some of them fix security issues (for example a browser patch release). They wait precisely because the label is
  missing.
* A host whose policy installs only security updates collects the rest.

So "security only" both kept the update badge lit and left security fixes from vendor repositories uninstalled.

## Decision Drivers

* Security fixes arrive whatever repository they come from.
* Nobody has to press a button for routine updates; the badge shows what really waits for a person.
* The same safeguards as today: window, rings, backup before the install, health checks, a failed run stops the
  others.
* The host that carries every guest is still changed only when a person chooses it.
* No automatic reboot.

## Considered Options

1. Keep `security` only.
2. Automatic scope `all` on every Linux host whose local policy already allows `all`, except the hypervisor.
3. `security`, plus an operator-maintained list of vendor repositories whose updates count as security updates.
4. Classify every pending update by advisory data (a CVE lookup per package and version).

## Decision Outcome

Chosen: option 2.

* `install.auto_scope: all` on the Linux guests whose `install.allowed_scopes` contain `all`. A host whose policy
  allows only `security` keeps `security`; the agent refuses an `auto_scope` outside its allowed scopes anyway.
* The hypervisor keeps its updates by button only and gets no reboot through Vördr (as ADR-0088).
* Unchanged:
  * the host's window in host time;
  * rings as in [ADR-0107](0107-ring-delay-per-update-ended-by-a-clean-canary-run.md);
  * the backup right before a guest's install ([ADR-0096](0096-fresh-backup-right-before-the-install.md));
  * health checks after the run;
  * reboots are never automatic (Home Assistant shows the need);
  * the deploy tool switches unattended-upgrades off in the same step (ADR-0080);
  * Windows PCs have no automatic installs.
* The button in Home Assistant installs what it installed before.

Rejected:

* **Option 1,** because of the context above: a lit badge every day, and vendor fixes that never arrive.
* **Option 3,** because the list would need care forever, a vendor's other updates are no riskier than its fixes, and
  point-release updates would still wait.
* **Option 4,** because there is no reliable per-package advisory feed for every vendor repository, and it would be a
  large build for a question the rings and the backup already answer.

### Consequences

* Good, because updates from every repository reach the guests within a window or two, security fixes among them.
* Good, because the system entities of the guests are mostly `off` after the window. What remains waits for a person:
  the hypervisor, Windows, package locations by button.
* Bad, because more changes per window. Concretely:
  * an upstream version can change behaviour;
  * an update of the container runtime restarts every container on its host
    ([ADR-0062](0062-docker-live-restore.md) rejected live-restore);
  * a kiosk's browser runs the new version only after its next restart.
* Bad, because a vendor repository can ship a new major version. Vördr flags major jumps only for sources with vendor
  versions (winget), not for apt, so such a jump can arrive in an automatic window. Safeguards: the backup, the health
  checks, and `apt-mark hold` on a host for a package that must not move.
* Bad, because a failure is noticed afterwards, not while someone watches. The problem alarm and the AI explanation
  ([ADR-0045](0045-site-decision.md)) report it; the backup is the way back.

### Confirmation

* The operator's deploy tool writes `auto_scope: all` for those hosts and `security` for the others. `check-config`
  on each host accepts it. The hub's events show `auto_install_started` with scope `all`.
* After the first window: the guests' system entities are `off` or name what failed.
* The deploy tool derives the automatic scope from each host's `allowed_scopes`: `all` where they contain `all`,
  `security` where only `security` is allowed. `check-config` accepts each file, and the hub lists the hosts with
  `hub/all`.
