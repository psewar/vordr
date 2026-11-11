---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# PowerShell 7 for the Windows service

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: `design/architecture.md` 11.1 ("PowerShell 7 ist Pflicht
für den Dienst"), the operator's notes.

## Context and Problem Statement

Right after `install-service` the agent cannot query winget as LocalSystem. As LocalSystem,
`Microsoft.WinGet.Client` refuses Windows PowerShell 5.1 ("This cmdlet is not supported in Windows PowerShell"); with
PowerShell 7 it works.

## Decision Drivers

* The service runs as LocalSystem ([ADR-0015](0015-windows-service-localsystem.md)).
* The state should be reported, not a module error.
* Setup in one step.

## Considered Options

The sources name no alternative to PowerShell 7.

## Decision Outcome

* The agent uses PowerShell 7 for all users (`%ProgramFiles%\PowerShell\7\pwsh.exe`) when present, otherwise Windows
  PowerShell 5.1.
* As LocalSystem without PowerShell 7 it reports the fact `winget_client: needs_powershell_7` with the next step
  before calling anything, and `install` refuses with `rejected_unsupported`.
* `install-service` installs PowerShell 7 for all users as MSI (`--installer-type wix`). The default msix package
  failed machine-wide with 0x80070005 and removed the user's Store version on the way.

### Consequences

* Good, because the service works on a fresh PC after one command.
* Bad, because one more component on every Windows PC.

### Confirmation

`TestServiceNeedsPowerShell7`; a live check: the script runs as SYSTEM with PowerShell 7 on a Windows host.
