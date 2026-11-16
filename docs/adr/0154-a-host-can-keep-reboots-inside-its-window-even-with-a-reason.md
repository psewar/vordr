---
status: accepted
date: 2026-11-16
decision-makers: kay (2026-11-16, asked to harden the reboot against a compromised Home Assistant); proposed by Claude
---

# A host can keep reboots inside its window even with a reason

Refines [ADR-0087](0087-runs-outside-the-window-with-a-reason.md) for reboots, as an option of the host's local policy.

## Context and Problem Statement

Since ADR-0087 a job with a reason (`outside_window`) gets past `install.window` and `reboot.window`: a person can move
an install or a reboot to a better moment from Home Assistant. The hub gives every button press in Home Assistant such a
reason ("started in Home Assistant"), so the reboot button works at any time.

For most hosts that is the point. For some, the moment of a reboot matters more than the convenience: a server whose
reboot interrupts work that is hard to repeat, or one that other systems depend on during the day. On such a host the
window should hold, whoever asks: the operator, the hub's automation, or someone who took over Home Assistant. Today
the window is no bound against a caller that sends a reason.

## Decision Drivers

* The host's local policy has the last word (ADR-0006); Home Assistant and the hub cannot widen it.
* Existing configurations keep their behaviour without edits.
* No new code where an existing one says the same thing.

## Considered Options

1. A key `reboot.window_only` in `agent.yaml`: with `true`, a reason does not open `reboot.window`.
2. Withdraw ADR-0087 for reboots altogether: reboots only inside the window, on every host.
3. Let the hub stop adding its button reason for reboots.

## Decision Outcome

Chosen option: **1**.

* **Policy:** `reboot.window_only: true` makes the agent ignore a job's reason for the reboot window. Outside the window
  it refuses with `rejected_outside_window`; the detail says that `reboot.window_only` keeps the window shut. Inside the
  window nothing changes. Default `false`: ADR-0087 as before.
* **check-config:** `reboot.window_only` needs `reboot.window`.
* **Installs** are not affected; `install.window` keeps ADR-0087.
* **Home Assistant** learns of it from the result: the reboot button and the reboot repair stay, and outside the window
  the run ends with `rejected_outside_window` and its known text.

Option 2 takes away from every host what most of them want. Option 3 moves the decision to the hub, which is one of the
parties the option guards against, and leaves the reason of `vordr.press_outside_window` open.

### Consequences

* Good, because a host decides for itself that a reboot never comes at a bad moment, also when its caller is not to be
  trusted.
* Good, because nothing changes on hosts that do not set it.
* Bad, because Home Assistant offers the reboot outside the window and only the result says no; the window is not shown
  ahead.

### Confirmation

* `internal/agent/reboot_test.go` (`TestRebootWindowOnly`): with `window_only`, a job with a reason outside the window is
  refused with `rejected_outside_window` and nothing reboots; inside the window it reboots.
  `TestRebootOutsideWindowWithReason` still reboots without the key.
* `internal/policy/policy_test.go` (`TestIssues`, `TestRebootWindowOnlyAndIdleCheck`): `window_only` without a window is
  refused, with one it is taken.
