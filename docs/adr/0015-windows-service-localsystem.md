---
status: accepted
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# Windows agent as a service under LocalSystem

Backfilled on 2026-10-30, decided on 2026-10-28 (draft), built on 2026-10-30. Sources: `design/architecture.md`
sections 11 and 14.

## Context and Problem Statement

On Windows the agent must install machine-wide and query Windows Update without anyone logged in.

## Decision Drivers

* Windows Update over COM requires LocalSystem.
* Machine-wide installers would ask for UAC without admin rights, and nobody answers.

## Considered Options

* service under LocalSystem
* agent in the user context

## Decision Outcome

Chosen option: service under LocalSystem, as with an earlier agent project on Windows.
`vordr-agent install-service` sets it up in one step: binary to `%ProgramFiles%\Vordr`, `agent.yaml` to
`%ProgramData%\vordr` (readable only by SYSTEM and administrators), automatic start, restart after a crash.

In the user context (foreground) only `app:<id>` remains; `all` ends with `rejected_unsupported` there. The agent
reports the context as the fact `install_context`.

### Consequences

* Good, because installing needs no dialog.
* Bad, because LocalSystem has no regular user profile (no Documents folder), and the WinGet module requires
  PowerShell 7 there ([ADR-0053](0053-powershell-7-for-the-service.md)).
* Bad, because self-update and reboot on Windows are not built yet.

### Confirmation

`TestInstallAllNeedsElevation`; a setup with `vordr-agent install-service` on a Windows host.
