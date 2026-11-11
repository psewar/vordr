---
status: accepted
date: 2026-11-04
decision-makers: kay (2026-11-04, after a refused "update all" outside the window; accepted once released); proposed by Claude
---

# The install button in Home Assistant installs at once, also outside the window

Supersedes [ADR-0087](0087-runs-outside-the-window-with-a-reason.md). It changes one point there:
the plain install buttons in Home Assistant no longer keep to the window. Everything else in ADR-0087 still holds and
is restated below.

## Context and Problem Statement

Under ADR-0087 an install outside the host's window needs a reason, given through a separate action
(`vordr.install_outside_window`, `vordr.press_outside_window`). The plain buttons keep to the window: the agent
refuses them with `rejected_outside_window`.

The operator pressed "update all" in Home Assistant outside the window:

* Every Linux host refused its install with `rejected_outside_window`. The entities stayed lit, and the operator
  could not tell from the button why nothing happened.
* Before that, the hub had each guest backed up on the backup server right before the install
  ([ADR-0096](0096-fresh-backup-right-before-the-install.md)). The window check comes only at the agent, after the
  backup. The backups were made for installs that never ran.
* Pressing the button is already the person's decision to install now. The action with a reason asks for the same
  decision a second time, and few people know it exists.

## Decision Drivers

* A person who presses install means now. The window is for what nobody started: the automatic runs.
* What the hub can know before it acts, it checks before it acts ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md),
  design 2): no backup for an install that the window stops anyway.
* A run outside the window stays visible as such, with a reason, in the run, the agent log and the event log.
* No agent change: every agent that takes a reason (ADR-0087) keeps working unchanged.
* Reboots stay where they are: a reboot button has no confirmation dialog, a mis-tap takes a host down.

## Considered Options

1. Leave it as in ADR-0087; only check the window at the hub before the backup.
2. The hub gives every install asked for in Home Assistant without a reason a fixed reason of its own when the host's
   window is closed, so the install runs at once. The hub also refuses, before any backup, a job the window would stop.
3. As 2, but the integration sends the reason, always.
4. The agent drops `install.window` for jobs from Home Assistant.

## Decision Outcome

Chosen: option 2, since hub 0.23.0.

* **Hub:** for a Home Assistant `command` with verb `install` (system packages, security updates, an app, a pip/npm
  location or package, a dependency refresh) and without a reason, the hub reads the install window and time zone the
  agent announces (`policy.window`, `policy.tz`). When the window is closed now and the agent takes reasons
  (`policy.outside_window`), the job carries `outside_window: "install started in Home Assistant outside the window"`
  (`hub.ButtonReason`). A reason the person gives stays as it is. A command that waits behind a running run gets the
  reason when it starts, not when it was asked for.
* **Hub, before the backup:** an install job without a reason whose window is closed now is refused with
  `outside_window` before it is sent and before its guest is backed up. This holds for every way a job starts (Home
  Assistant, the admin socket, the queue). It is reached only for an agent that cannot take a reason, or for
  `vordr-hub install` outside the window.
* **Where the hub cannot read the window** (no window announced, an unknown time zone), it adds nothing and leaves the
  decision to the agent, as before.
* **Unchanged from ADR-0087:**
  * the job field `outside_window` and its rules (`protocol.ValidReason`), signed with the job;
  * the agent lets a job with a reason past `install.window` and `reboot.window` and logs the reason; everything else
    still applies: allowed scopes, `packages.update`, `reboot.allowed`, `install.min_interval_h`, one run at a time,
    the backup check;
  * agents announce it in their hello; an older agent gets no reason;
  * the actions with a reason stay, for a person who wants to say why;
  * automatic runs (`install.auto: hub`) never carry a reason and stay inside the window.
* **Reboots** keep to `reboot.window`. Outside it they still need `vordr.press_outside_window` with a reason.

Rejected:

* **Option 1,** because the button would still do nothing outside the window: the refusal only comes earlier.
* **Option 3,** because the integration does not know the window. It would mark every install as outside the window,
  also inside it, and an older integration would still send none.
* **Option 4,** because the agent would have to tell where a job comes from, and a job's origin is the hub's
  knowledge, not the agent's. The agent's rule stays simple: a reason passes the window.

### Consequences

* Good, because "install" and "update all" in Home Assistant do what they say, at any time.
* Good, because no guest is backed up for an install the window stops.
* Good, because a run outside the window still names why: the button's reason in the run, the agent log and the event
  log.
* Bad, because a press at the wrong moment now installs at once, also in the middle of the day while a host is in
  use. The backup before the install and the health checks still apply; a reboot is never part of it.
* Bad, because the hub's reading of the window can differ from the agent's at its edges (clock skew, a minute before
  the end). Then the agent refuses as before (`rejected_outside_window`).
* Neutral: the threat model of ADR-0087 does not change. A hub could already send a reason with any install.

### Confirmation

* `internal/hub`: `TestInstallButtonReason` (closed and open window, a person's reason, a reboot, an agent without
  reasons, no window, an unknown time zone, another zone than UTC) and `TestNoBackupForAnInstallTheWindowStops` (no
  run, no backup, refused before the backup check; inside the window the install waits for its backup).
* `internal/agent`: `TestOutsideWindowFromHomeAssistant` (HA client, real hub and agent): the plain install button
  outside the window runs, `last_run.outside_window` is the button's reason; a person's reason stays.
* A live check: a guest's install button in Home Assistant pressed outside the window, with no reason given. The
  hub's `command` and `job_sent` events and the run carry the button's reason. The guest is backed up right before the
  install (`install_awaits_backup`, then `pre_install_backup` ok), and the run ends `ok` with its health checks. The
  refusal before the backup (`outside_window`) shows live only with an agent that cannot take reasons.
