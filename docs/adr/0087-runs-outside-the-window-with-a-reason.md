---
status: superseded by ADR-0109
date: 2026-10-30
decision-makers: kay (2026-10-30); proposed by Claude
---

# A person may start a run outside the window, with a reason

Supersedes [ADR-0006](0006-local-policy-decides.md) and [ADR-0041](0041-reboot-with-window-and-proof.md) in one
point: the window. Everything else in them still holds and is restated below.

## Context and Problem Statement

Until now every install and reboot job had to fall inside the host's window (`install.window`, `reboot.window`). The
agent refused anything else with `rejected_outside_window`, the HA button included. ADR-0006 allowed exceptions only
on the host itself (`--outside-window "<reason>"`), and that command was never built.

The operator (2026-10-30): "I want to be able to trigger updates from Home Assistant outside the normal window. I may
know that later won't work and that now is a better time."

## Decision Drivers

* The person who looks after the hosts decides when they change, not only a window set weeks earlier.
* An exception is explicit and has a reason that stays visible (design 2.6), never a silent switch.
* Automatic runs keep to the window.
* A compromised hub should gain as little as possible.

## Considered Options

* **Switch per host in `agent.yaml`**, off by default: only hosts that opt in take such jobs. ADR-0006 would hold
  unchanged.
* **Every host, the window binds only automatic runs and the plain buttons**: a job that carries a reason may run
  outside the window, on any host with an agent that knows it.
* For the trigger: the plain button working outside the window with an automatic reason ("pressed by X"), or a
  separate action with a required reason.

## Decision Outcome

Chosen: every host, through a separate action with a required reason (the operator's choice of both options,
2026-10-30), for system updates, pip/npm packages and reboots.

* **Job:** `install` and `reboot` may carry `outside_window`, the reason: not blank, at most 200 bytes of UTF-8, no
  control characters (`protocol.ValidReason`). Every other verb with it is an invalid job. The hub signs it with the
  job like every other parameter.
* **Agent:** a job with a reason passes `install.window` (also for `pkg:`) and `reboot.window`. The agent writes the
  reason into its log. Everything else still applies: allowed scopes, `packages.update`, `reboot.allowed`, a reboot
  only when needed, `install.min_interval_h`, one run at a time, the backup check at the hub.
* **Announcement:** the agent says so in its hello (`policy.outside_window`). An older agent decodes jobs strictly
  and would refuse the unknown field as `rejected_invalid_job`, so the hub sends a reason only to an agent that
  announces it and otherwise answers `outside_window_unsupported`.
* **Hub:** a Home Assistant `command` may carry `reason`. The hub checks it (`bad_reason`; `bad_request` for `check`
  and `agent_update`), keeps it with the run (`last_run.outside_window`) and logs it in `command`, `job_sent` and
  `queued_job_started`. A command that waits behind a running run takes the reason if it is asked for again with one.
  Roles stay: a reboot still needs `admin`.
* **Home Assistant:** the plain buttons and the install button keep to the window. Outside it: the actions
  `vordr.install_outside_window` (update entities: system packages or a pip/npm location),
  `vordr.press_outside_window` (buttons: security updates, apps, reboot) and `vordr.update_package` with the optional
  field `reason`. The reason is required where it is the point of the action. The integration offers them only where
  the hub reports `capabilities.outside_window`.
* **Automatic runs** (`install.auto: hub`) never carry a reason. They stay inside the window.

What still holds from ADR-0006: the local policy decides everything except the window for such a run. A missing key
means the stricter value, unknown keys are refused, the agent announces a summary of its policy, the hub checks
commands against it in advance and the agent checks again.

What still holds from ADR-0041: a reboot only with `reboot.allowed`, only when the host really needs it, at most once
per hour, only when nothing else runs; `reboot.json` with the boot ID before it, proof by a new boot ID and no further
reboot needed after it; never automatic.

### Consequences

* Good, because the operator can move an update or reboot to a better moment from Home Assistant, without SSH.
* Good, because the reason stays with the run, in the agent log and in the hub's event log.
* Good, because automatic runs and the plain buttons still keep to the window.
* Bad, because the threat model changes: a compromised hub (or someone with an approved HA client) can now start
  allowed installs and allowed reboots at any time, on every host with a new agent. It still cannot choose packages,
  widen scopes, reboot a host that does not allow it, or run more often than `install.min_interval_h`.
* Bad, because it is a protocol change: hub first, then agents; and integration 0.14.0.
* Neutral: `--outside-window` on the host is still not built.

### Confirmation

* `internal/protocol`: `TestOutsideWindowReason` (verbs, length, blank, control characters).
* `internal/agent`: `TestInstallOutsideWindowWithReason`, `TestRebootOutsideWindowWithReason`, `TestPackageRunGates`
  (closed window without and with a reason), `TestOutsideWindowFromHomeAssistant` (HA client, real hub and agent:
  refused without a reason, `ok` with one, the reason in `last_run`).
* `internal/hub`: `TestOutsideWindowCommands` (older agent, capability, queue takes the reason, refusals).
* `ha/tests`: `test_outside_window_actions`.
