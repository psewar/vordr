---
status: rejected
date: 2026-10-30
decision-makers: kay ("4. Go")
---

# A snapshot of the guest before every install

## Context and Problem Statement

Before every install the hub requires a backup younger than 26 h ([ADR-0030](0030-backup-required.md)); going back
means a full restore. A Proxmox snapshot right before the run would make going back one click. But only guests on
qcow2 or a thin pool can take snapshots; raw files on directory storage cannot, so where most guests are raw files a
snapshot covers only a few. A thin pool close to full stays lower only through regular trimming; thin snapshots fill
it again.

## Decision Drivers

* A way back after a broken update.
* A full thin pool stops every guest on it.

## Considered Options

* snapshot before every install, where the storage allows it
* no snapshot; the backup check stays the way back

## Decision Outcome

Rejected for now (the operator, 2026-10-30). The backup check stays the way back. The failures so far (a replaced
package, a stopped database container) were fixed forward without going back. Revisit when a restore after a Vördr
update is actually needed, or when all guests sit on storage that can take snapshots.

### Consequences

* Good, because no risk for the thin pool and nothing to build.
* Bad, because going back stays a full restore from a backup up to 26 h old.

### Confirmation

Not applicable.
