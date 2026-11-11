---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# winget: leave out packages with an unknown installed version

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: the operator's notes (expansion plan of 2026-10-30).

## Context and Problem Statement

"Update all" on a Windows PC updates several programs. One whose installed version winget reports as "Unknown" fails
with `CatalogError 0x8A15005F`, and HA shows a problem.

## Decision Drivers

* Behave like winget itself.
* No permanent problem for a package winget cannot compare.

## Considered Options

* count and update such packages (like `winget upgrade --include-unknown`)
* leave them out (like `winget upgrade` without the flag)

## Decision Outcome

Chosen option: leave them out. Packages with an unknown installed version do not count as pending and are not
updated. The fact `winget_unknown_version` lists them.

### Consequences

* Good, because no false problem.
* Bad, because such packages are never updated by Vördr; the fact shows which ones.

### Confirmation

`TestUnknownVersionIsLeftOut`.

## More Information

The same change fixed how a partial failure is named: if other packages of the run were updated, it is
`failed_package_manager`, no longer `precheck_repo_unreachable` ("nothing changed").
