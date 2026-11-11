---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# On Windows, winget first; Windows Update later

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: the operator's notes (analysis of 2026-10-30),
`design/architecture.md` 11.1.

## Context and Problem Statement

The draft planned Windows Update (`winupdate`) as the Windows source. The programs on a PC are not covered by it.

## Decision Drivers

* On Windows, programs are what stays outdated.
* Windows Update already runs by itself.

## Considered Options

* Windows Update first (`winupdate`)
* winget first (programs)

## Decision Outcome

Chosen option: winget first (the operator: "Windows agent, preferably winget"). `winupdate` comes later.

### Consequences

* Good, because programs such as Node.js or backup clients show up in HA and update by button.
* Bad, because Windows system updates are not reported by Vördr yet. The analysis suggests at least reporting
  them; the agent already reads the reboot keys of Windows Update.

### Confirmation

Not applicable.

## More Information

How winget is queried: [ADR-0050](0050-winget-via-powershell-module.md).
