---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# winget through Microsoft's PowerShell module, not the CLI table

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: `design/architecture.md` 11.1.

## Context and Problem Statement

The agent must read which programs winget would update and install them. The table of `winget upgrade` is translated
and cut to the console width.

## Decision Drivers

* Fixed states instead of parsing texts ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* Respect pins set with `winget pin`.

## Considered Options

* parse the output of `winget upgrade`
* Microsoft's PowerShell module `Microsoft.WinGet.Client` (same COM interface as winget)

## Decision Outcome

Chosen option: the PowerShell module.

* Check: `Get-WinGetPackage`, the ones with `IsUpdateAvailable` (ID, installed and newest version, source). Pinned
  packages do not count.
* Install: `Update-WinGetPackage -Mode Silent` per package, one after another, in one PowerShell process. Each returns
  a JSON line with winget's own status (`PSInstallResultStatus`), the installer's exit code and `RebootRequired`. A
  fixed table turns these into Vördr codes; afterwards the agent checks against a freshly read list.
* Prechecks: Windows Installer busy (mutex `Global\_MSIExecute`) → `rejected_busy`; less than 2 GiB free →
  `precheck_disk_space`.
* A missing module makes the check `check_failed` with the fact `winget_client: missing`; the source does not vanish
  silently.

### Consequences

* Good, because objects and status values replace text parsing.
* Bad, because the module must be installed for all users and needs PowerShell 7 under LocalSystem
  ([ADR-0053](0053-powershell-7-for-the-service.md)).
* Bad, because winget has no security classification; scope `security` finds nothing.

### Confirmation

`TestCheck`, `TestCheckClientMissing`, `TestInstallResults`, `TestInstallPrechecks`; `TestWingetLive` runs the whole way
HA → hub → agent → winget on a Windows host.
