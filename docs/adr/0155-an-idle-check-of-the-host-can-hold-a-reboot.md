---
status: accepted
date: 2026-11-16
decision-makers: kay (2026-11-16, asked to harden the reboot against a compromised Home Assistant); proposed by Claude
---

# An idle check of the host can hold a reboot

Adds a precondition to the reboot of [ADR-0041](0041-reboot-with-window-and-proof.md).

## Context and Problem Statement

The agent reboots only when nothing else of its own runs (no install, no check). It does not know the host's own work.
A backup server interrupts its running backups, verify jobs and garbage collection when it reboots; a database host may
be in the middle of a long migration. Such work has no fixed time a window could leave out, and a reason from Home
Assistant opens the window anyway (ADR-0087). Whoever asks for the reboot, the operator by mistake or someone who took
over Home Assistant, the host should be able to say "not now".

Only the host knows what "busy" means for it, and the answer differs from product to product.

## Decision Drivers

* The host's local policy has the last word; Home Assistant and the hub cannot switch the check off.
* Vördr does not learn every product's notion of "busy"; the host brings its own check.
* A check that cannot answer must not let the reboot through.
* Results travel as fixed codes; the check's own words go along only for a human.

## Considered Options

1. `reboot.idle_check` in `agent.yaml`: a command the agent runs right before the reboot; exit status 0 means idle.
2. Built-in checks per product (backup server, database), switched on in `agent.yaml`.
3. Leave it to the window and to the person who presses the button.

## Decision Outcome

Chosen option: **1**.

* **Policy:** `reboot.idle_check` is the command and its arguments as a list, run without a shell; the command is an
  absolute path. check-config refuses an empty entry, more than 16 entries, a relative path, and a command that is not
  an executable file.
* **When:** after the agent has found that the reboot is needed, under its run lock, right before it writes the reboot
  marker. A reboot that is not needed does not run the check.
* **Contract:** exit status 0: idle, the reboot goes on. Any other status: busy; the agent refuses with the new result
  code `rejected_not_idle`, and the first non-empty line of the check's output (at most 200 bytes) is the detail. A
  check that is missing, cannot start or does not finish within 30 seconds cannot say that the host is idle: also
  `rejected_not_idle`, with that as the detail.
* **No problem:** like `rejected_busy`, the code does not set the problem sensor; a reboot that stays held for days is
  caught by `reboot_pending` (ADR-0014). Home Assistant knows the code in all languages; its next step: wait until the
  host's own tasks are done, then reboot again.
* **First use:** a backup server whose check reports running tasks. The check is site configuration, not part of the
  repository; any product plugs in by bringing a command that keeps the contract.
* **Installs** do not run the check. Whether an install should wait for it as well is a decision of its own.

Option 2 makes Vördr follow the command-line interfaces of products it does not own, and still leaves out the next one.
Option 3 is what exists, and it does not hold against a caller that is not to be trusted.

### Consequences

* Good, because a host with work of its own can refuse a reboot at a bad moment, whoever asks.
* Good, because the check is the host's own and needs no change in Vördr for another product.
* Bad, because a broken check holds every reboot of that host until someone fixes it; the refusal names it.
* Neutral: the check runs as the agent's user (root), like everything the agent does; it comes from the same
  `agent.yaml` that already decides what the agent may do.

### Confirmation

* `internal/agent/reboot_test.go`: `TestRebootWaitsForIdle` (a busy check refuses with its line as the detail and
  leaves no marker; once idle, the host reboots), `TestRebootRefusals` (case "not idle"), and `TestIdleCheckCommand`
  with real scripts (exit status 0, another status with and without output, arguments, a missing command, a command
  that hangs).
* `internal/policy/policy_test.go`: `TestIssues` (relative, missing, empty entry, too many entries) and
  `TestRebootWindowOnlyAndIdleCheck` (taken with its arguments; not executable and a folder refused).
* `internal/protocol/protocol_test.go` (`TestCodes`): `rejected_not_idle` does not set the problem.
