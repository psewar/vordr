---
status: superseded by ADR-0087
date: 2026-10-29
decision-makers: kay ("yes, build it")
---

# Reboot on request, only inside the window, with proof

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 10.2 ("Neustart"), the operator's
project notes.

**Superseded by [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)** in one point: a reboot with a reason may
go outside `reboot.window`. Everything else still holds there.

## Context and Problem Statement

After kernel and firmware updates hosts need a reboot. Without the agent this needs SSH. The verb `reboot` existed
in protocol, hub, integration and design, but the agent did not carry it out.

## Decision Drivers

* No reboot that is not needed, not allowed or outside the window.
* No reboot loops.
* "Rebooted" must be proven, not assumed from a lost connection.

## Considered Options

The sources name no alternative.

## Decision Outcome

* Only with `reboot.allowed`, inside `reboot.window`, only when the host really needs it (same signals as the report:
  `reboot-required`, running versus newest kernel), only when nothing else runs, at most once per hour.
* Before: `reboot.json` with the boot ID, then the phase `rebooting`; the hub therefore waits for the agent instead of
  counting an interruption.
* After the start the agent proves the reboot with a new boot ID and checks that no reboot is needed any more. Only
  then `ok`; otherwise `failed_verification` with the reason.
* The agent only announces the verb where it is allowed (Linux, root, `reboot.allowed`). In HA the client role must be
  `admin` ([ADR-0039](0039-ha-client-roles.md)).
* Never automatic. HA offers a push "reboot now" after `ok_reboot_required`.

### Consequences

* Good, because reboots go through HA with the same rules as installs.
* Bad, because containers (LXC) gain nothing from it; they share the host kernel.
* Bad, because which hosts allow it is decided host by host.

### Confirmation

`TestRebootAndProof`, `TestRebootProofFails`, `TestRebootRefusals`; a live check: a reboot through HA comes back with
a new boot ID and the new kernel, without an alarm.
