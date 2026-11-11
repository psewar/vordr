---
status: proposed
date: 2026-11-04
decision-makers: kay (2026-11-04, "und die Verbesserung umsetzen", after an install refused for the minimum interval had its guest backed up first); proposed by Claude
---

# The hub checks the minimum interval before it has a guest backed up

Extends [ADR-0109](0109-the-install-button-installs-at-once.md), which does the same for the window, to the agent's
minimum interval between installs (`install.min_interval_h`). Changes nothing in the agent's own check.

## Context and Problem Statement

The agent refuses an install of the system packages or an app with `rejected_rate_limited` while its last run that
changed packages started less than `install.min_interval_h` ago (default 6). It counts from its run journal, so a
pip/npm run or a dependency refresh counts as well (design 10.2).

The hub does not know either value. It asks for the install anyway, and for a guest that gets a fresh backup right
before its install ([ADR-0096](0096-fresh-backup-right-before-the-install.md)), that backup comes first. For example,
a dependency refresh, then an install minutes later: the hub goes through the backup step for the guest and sends the
install, and the agent refuses it with `rejected_rate_limited`. A backup from minutes before can be reused; with a
longer gap the hub makes a new one for nothing. Home Assistant only learns the reason from the finished run.

Automatic installs have the same gap. The hub makes one attempt per host and window. If that attempt meets the
interval, the backup is made, the agent refuses, and the window is used up.

## Decision Drivers

* What the hub can know before it acts, it checks before it acts (design 2, ADR-0109): no backup for an install the
  agent will refuse.
* The agent states its state as facts. The hub does not work it out from codes or texts
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* The agent's check stays the last word. The hub's check only comes earlier.
* Home Assistant can say when the next install is possible.

## Considered Options

1. Leave it.
2. The agent announces `min_interval_h` in its hello and reports when its last counting run started: in the hello and
   with the final status of every run that wrote its journal. The hub keeps that time, refuses earlier, and shows when
   the next install is possible.
3. The hub derives the last run itself from the jobs it sent and their codes.
4. The minimum interval is also set in `hub.yaml`.

## Decision Outcome

Chosen: option 2.

* **Agent:** `policy.min_interval_h` in the hello. `last_install` in the hello: the start of the run in its journal,
  if there is one. `install_started` with the final status of a run that wrote its journal: that run's start. These
  are the same times the agent's own check uses.
* **Hub:** the register keeps `last_install` per host. A hello sets it, and a final status that carries
  `install_started` moves it. While `last_install` plus `min_interval_h` lies ahead, an install whose scope is not
  `pkg:` or `deps:` is refused with `rate_limited`. That is the same rule the agent applies. The refusal comes before
  the job is sent and before its guest is backed up, on every path (Home Assistant, the admin socket, the queue).
  Home Assistant gets it before the backup check.
* **Automatic installs:** a host that would be refused is not due yet. Its one attempt per window stays for later in
  the window.
* **View:** `HostView.next_install` says when the agent takes an install again, while that lies ahead.
* **Home Assistant:** the refusal says when: "not installed - the last run was less than the host's minimum interval
  ago; possible again from HH:MM".
* **An agent without the fields** (older than this decision) announces `min_interval_h` as 0. The hub then checks
  nothing, and the agent refuses as before.
* **Rollout:** the hub reads the hello strictly, so it must be updated before any agent that sends the new fields.

Rejected:

* **Option 1,** because of the wasted backups and the used-up automatic attempt.
* **Option 3,** because the hub would have to know which codes mean that the run got as far as the journal. That is
  the agent's knowledge, and it would break with the next code.
* **Option 4,** because the interval would live in two places and drift. The agent's policy decides.

### Consequences

* Good, because a refused install no longer costs a backup, and the refusal names the time.
* Good, because an automatic attempt is no longer used up by the interval.
* Bad, because it is a protocol change: hub first, then agents, and the integration for the text.
* Bad, because the hub's view of the time can be a little behind the agent's. A run that started while the
  connection was down only reaches the hub with its status or the next hello. Until then the agent refuses as before.
* Neutral: the agent's check and its default are unchanged.

### Confirmation

* `internal/hub`: `TestNextInstallFromTheAgentsInterval` (within and after the interval, an agent without the field,
  no run yet), `TestRateLimitedRefusedBeforeBackup` (no run, no backup, refused before the backup check, `pkg:` runs not
  refused, the view's `next_install`), `TestAutoInstallWaitsForTheInterval`, and the register taking `last_install`
  from hello and status.
* `internal/agent`: the summary announces `min_interval_h`, the hello carries `last_install`, the journal's final
  status carries `install_started`. `TestRateLimitFromHomeAssistant` uses a real hub, agent and HA client: a second
  install within the interval is refused by the hub at once, and no second run starts.
* `ha/tests`: the refusal names the time.
