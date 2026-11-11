---
status: superseded by ADR-0146
date: 2026-10-29
decision-makers: kay
---

# A package the run replaces is no removal

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 10.2 step 5.

## Context and Problem Statement

Distributions replace packages under a new name; Raspberry Pi OS, for example, replaced `pcmanfm` with `pcmanfm-pi`
(`Replaces` and `Breaks`). The upgrade would then remove the old package, so "install all" is refused with
`precheck_would_remove_packages`, and all pending updates of that host stay pending.

## Decision Drivers

* No run may remove packages unnoticed ([ADR-0031](0031-install-mode-full-upgrade.md)).
* Renames and splits by the distribution are normal and must not block every update.

## Considered Options

* allow removals in general
* use `upgrade` instead of `full-upgrade` on such a host (leaves packages behind, including the kernel and the desktop)
* accept a removal only when a package of the same run declares it replaces the removed one

## Decision Outcome

Chosen option: the declared replacement. A removal is no loss when a package of the same run declares `Replaces` plus
`Breaks` or `Conflicts` on the removed package (Debian Policy 7.6.2). One `apt-cache show` over the run's packages
decides. If it cannot tell, every removal counts as real. Everything else is still refused; the refusal lists replaced
packages next to real removals. The run lists its replacements in the summary (`replaced`).

### Consequences

* Good, because distribution renames no longer block updates.
* Bad, because the check depends on correct package metadata.

### Confirmation

`TestInstallReplacedPackage`, `TestInstallRealRemovalsStillRefused`, `TestRelationNames`; a live check: a run with a
replaced package ends `ok` and lists the replacement.
