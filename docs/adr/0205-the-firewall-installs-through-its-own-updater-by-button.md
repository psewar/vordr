---
status: proposed
date: 2026-11-20
decision-makers: proposed by Claude (2026-11-20) after kay asked that the firewall's pending updates become installable from Home Assistant; for the maintainer to accept or reject
---

# The firewall installs through its own updater, by button

Builds on [ADR-0204](0204-the-firewall-reports-what-its-own-updater-offers.md) (what the firewall offers) and is the
first appliance install of the proxy agent ([ADR-0089](0089-appliances-through-a-proxy-agent.md); design
`proxy-agent.md` 4.2, 4.5, step 5 of its plan).

## Context and Problem Statement

Vördr reads the firewall through a proxy instance and an unprivileged forced command; a root job on the firewall works
a checked queue ([ADR-0093](0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)). Installing is not
built: `check-config` refuses `install` with `target:`. The firewall's updates are therefore installed by hand in
pfSense's interface, and its `stale` repair can only say so
([ADR-0197](0197-the-stale-repair-names-only-a-way-that-exists.md)).

The design already says how an appliance install goes: by button, never automatically, with a fresh configuration
backup before ([ADR-0090](0090-appliance-installs-need-a-fresh-config-backup.md)) and a verification after; the reboot
belongs to the update. What is open for the firewall is the way the root job installs, and how the run survives the
firewall's own reboot, which takes the network behind it down for minutes.

## Decision Drivers

* The vendor's updater does the install, as in pfSense's own interface; Vördr chooses no packages and no sources.
* The proxy gets no privilege on the firewall: the root job checks every request again (ADR-0093).
* No install without a fresh, checked configuration backup (ADR-0090).
* Only by button, inside the window or outside it with a reason
  ([ADR-0087](0087-runs-outside-the-window-with-a-reason.md)); never automatic for appliances.
* The result reaches Home Assistant also when the firewall reboots and the connection drops meanwhile.

## Considered Options

* a queued request that the root job runs with `pfSense-upgrade`: the release of the configured branch, or one add-on
  package from its offers
* `pkg upgrade` through the root job
* the proxy logs in as an administrator and starts the update in pfSense's interface
* keep installing by hand

## Decision Outcome

Proposed: a queued request run with `pfSense-upgrade`.

* **Forced command:** `install release <job>` and `install addon <name> <job>` queue a request; `install status <job>`
  answers `install_state` (`queued`, `running`, `ok`, `failed`), the exit code and the times. The job id is checked
  for its form, the name against the root job's own current `addon_offer` lines; anything else is refused with a code.
* **Root job:** takes one request at a time under its lock. Before it acts it checks again that the offer still exists
  and that no other upgrade runs. It runs `pfSense-upgrade -y` for the release, `pfSense-upgrade -y -i <name>` for an
  add-on, with the log in its own state directory, and keeps the state across the reboot that the updater may start.
  It never passes words of the request to a command line beyond the checked name.
* **Agent (proxy instance):** offers `install` for a pfSense target only where the instance's `agent.yaml` allows it
  (`install.allowed_scopes`, closed by default; `install.auto` stays refused for targets). Before queueing: a
  configuration backup younger than `config_backup.max_age_h` (taken right before, as ADR-0090 says), the firewall
  answering, the offer present. Phases `installing`, then `rebooting` while the firewall does not answer, then verify
  within `timeout_min`: the new release or add-on version in the facts, else `failed_verification`.
* **Hub:** unchanged in kind; the backup check takes the reported `config_backup_at`
  (`backup.guests` entry `config_backup: true`, ADR-0090).
* **Branch:** switching to another release branch stays the operator's step in pfSense's interface (ADR-0204 reports
  it); Vördr installs only within the configured branch.

Rejected:

* **`pkg upgrade`,** because it installs a set of packages the vendor never released together and bypasses the
  updater's own checks (boot environment, kernel, vital packages).
* **An administrator login of the proxy,** because the proxy would hold full control of the firewall, which ADR-0093
  deliberately avoided.
* **By hand only,** because the firewall would stay the one host whose updates Home Assistant can name but not start.

### Consequences

* Good, because the firewall's release and add-ons can be installed from Home Assistant, the vendor's way, with a
  backup before and a check after.
* Good, because the proxy still holds no privilege on the firewall.
* Bad, because a release install reboots the firewall: the network behind it, possibly with Home Assistant and the
  hub, is down for minutes, and the run's progress shows only once it is back. The text of the button and the repair
  says so.
* Bad, because root runs the vendor's updater on a request from an unprivileged user; the request carries no command,
  but it is a boundary to keep small.
* Bad, because a hub or proxy taken over could start a firewall update where the instance allows install - as with
  hosts; the threat model gets that line.

### Confirmation

* `deploy/pfsense/` tests with a stubbed `pfSense-upgrade`: a request with a malformed job id or an unknown add-on is
  refused, an offer that vanished is refused at run time, one request at a time, the state survives a simulated reboot.
* `internal/target` and `internal/agent`: no install without a fresh backup, the phases and the verification, the
  refusal of `install` without the instance's policy, and of `install.auto` for targets.
* Before the first install on a production firewall: a release install and an add-on install on a test firewall (a
  virtual machine with the same release), including the reboot and the verification after it.
