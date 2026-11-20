---
status: accepted
date: 2026-11-20
decision-makers: kay (2026-11-20, accepted with ADR-0204: "annehmen und umsetzen"); proposed by Claude (2026-11-20) after kay asked that the firewall's pending updates become installable from Home Assistant
---

# The firewall installs through its own updater, by button

Builds on [ADR-0204](0204-the-firewall-reports-what-its-own-updater-offers.md) (what the firewall offers) and is the
first appliance install of the proxy agent ([ADR-0089](0089-appliances-through-a-proxy-agent.md); design
`proxy-agent.md` 4.2, 4.5, step 5 of its plan).

## Context and Problem Statement

Vördr reads the firewall through a proxy instance and an unprivileged forced command; a root job on the firewall works
a checked queue ([ADR-0093](0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)). Installing was not
built: `check-config` refused `install` with `target:`. The firewall's updates were therefore installed by hand in
pfSense's interface, and its `stale` repair could only say so
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

Chosen: a queued request run with `pfSense-upgrade`.

* **Forced command:** `install release <id>` and `install addon <name> <id>` queue a request; `install status <id>`
  answers the root job's state of that id (`running`, `ok`, `failed`, `refused`, `rebooted`, with a code, the exit code
  and the times), `queued` while its request waits, else `unknown`. The id (8 to 32 hex digits) and an add-on's name
  (`pfSense-pkg-` and at most 36 more letters, digits or `._+-`) are checked for their form, so that a request stays
  within the 100 bytes the root job reads; anything else is refused with a code.
* **Root job:** runs at most one install per run, under its cron lock. Before it acts it checks again that no other run
  of the updater holds its lock (`busy`) and that the offer still exists (`not_offered`): the configured branch's
  release, or a newer catalog version of the installed add-on. It runs `pfSense-upgrade -y` for the release,
  `pfSense-upgrade -y -i <name>` for an add-on, with the log in its own state directory, and reads the update facts
  again afterwards. An install left running by an earlier run is `rebooted` after a reboot, `failed` with
  `interrupted` in the same boot, and stays `running` while the updater still holds its lock (a later boot stage).
  It never passes words of the request to a command line beyond the checked name.
* **Agent (proxy instance):** offers `install` for a pfSense target only where the instance's `agent.yaml` allows it
  (`install.allowed_scopes: [all]`, closed by default; `install.auto` stays refused for targets). Before queueing: a
  configuration backup taken and stored right before (ADR-0090), the firewall answering, the offer present. The plan
  is the release alone, else each add-on; kept-back offers never. Phases `installing`, then `rebooting` while the
  firewall does not answer, then verify within `timeout_min`: facts read after the install's end that no longer offer
  it, else `failed_verification`. The existing codes carry the outcome (`failed_config_backup`, `rejected_busy`,
  `failed_package_manager`, `timeout`, `failed_verification`), so the integration needs no new text.
* **Hub:** unchanged; the backup check takes the reported `config_backup_at` (`backup.guests` entry
  `config_backup: true`, ADR-0090), and kept-back offers stay out of `installable` (ADR-0204).
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
  hub, is down for minutes, and the run's progress shows only once it is back.
* Bad, because root runs the vendor's updater on a request from an unprivileged user; the request carries no command,
  but it is a boundary to keep small.
* Bad, because a hub or proxy taken over could start a firewall update where the instance allows install - as with
  hosts; the threat model has that line.

### Confirmation

* `deploy/pfsense/scripts_test.go` with a stubbed `pfSense-upgrade` and `sysctl`: malformed ids and names refused by
  the forced command (`TestForcedCommandRefuses`); release and add-on installs (`TestRootJobInstallsTheRelease`,
  `TestRootJobInstallsAnAddOn`); an offer gone, the updater's lock held, requests past the forced command, two in one
  run (`TestRootJobRefusesAnInstall`); failures (`TestRootJobReportsAFailedInstall`); the end after a reboot, in the
  same boot and during a later boot stage (`TestRootJobNotesTheEndOfAnInterruptedInstall`).
* `internal/target/pfsense_test.go`: `TestPfsenseQueue`, `TestPfsenseInstallStatus`.
* `internal/agent/target_install_test.go`: the plan, the backup before, the device's end, the reboot, the
  verification; `internal/policy/target_test.go`: install only for `pfsense`, only scope `all`, never `install.auto`.
* Before the first install on a production firewall: a release install and an add-on install on a test firewall (a
  virtual machine with the same release), including the reboot and the verification after it.
