---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, chose to extend the rule; accepted with the merge); proposed by Claude
---

# An old library the run supersedes is no removal

Supersedes [ADR-0042](0042-replaced-packages-are-no-removal.md). Its rule, the declared replacement, stays and is
restated below; this ADR adds a second case.

## Context and Problem Statement

The agent refuses a run whose simulation removes packages (`precheck_would_remove_packages`); a removal can take away
something the host needs. ADR-0042 excepted packages that a package of the same run replaces by declaration
(`Replaces` plus `Breaks` or `Conflicts`).

A library that moves to a new soname is not covered. Its new package (`libexample2`) is built from the same source as
the old one (`libexample1`) and does not declare `Replaces` on it, because the two can be installed side by side.
Usually the old library stays as an orphan. But when a package of the same source is tied to one library version, for
example a plugin package that the old library depends on with `=`, apt removes the old library as soon as that package
moves on. Every package that used the old library moves to the new one in the same run. Nothing is lost, yet the
agent refused the run and held back all other updates of the host, security updates included, until someone acted by
hand.

## Decision Drivers

* A removal that takes nothing away does not block the updates of a host.
* A removal that might take something away is still refused.
* The decision rests on what the package manager knows about the host, not on names or patterns.
* When a tool cannot answer, the run is refused, as before.

## Considered Options

* the superseded library: three facts the package manager can answer
* a soname pattern: the removed and the new package share a name up to a version suffix
* an explicit exception per run with a reason, as for runs outside the window
* keep ADR-0042 and handle such cases by hand

## Decision Outcome

Chosen: the superseded library, next to the declared replacement. A removal is no loss when one of these holds.

1. **Declared replacement** (from ADR-0042): a package of the same run declares `Replaces` plus `Breaks` or
   `Conflicts` on the removed package (Debian Policy 7.6.2).
2. **Superseded library**, when all three hold:
   * nobody asked for it: the package was installed automatically, as a dependency (`apt-mark showauto`). A package a
     person installed may serve software the package manager does not know of;
   * its successor is in the run: a package of the run is built from the same source package (`source:Package` of
     the installed package, `Source` of the new one);
   * nothing installed needs it any more: every installed package that depends on it (`Depends`, `Pre-Depends`) is
     part of the run, and its new version no longer depends on it.

Everything else is still refused. When `apt-cache`, `apt-mark` or `dpkg-query` cannot answer, every removal counts as
real. The run lists replacements of both kinds in its summary (`replaced`, "old -> new"); a refusal names the
replaced packages next to the real removals.

Rejected:

* **A soname pattern,** because names do not prove that nothing needs the old package; a pattern would let through a
  removal that breaks a package outside the run.
* **An explicit exception with a reason,** because the case is common and safe to recognize; asking a person each
  time adds work without adding a check.
* **Handle by hand,** because a refused run holds back every other update of the host, security updates included.

### Consequences

* Good, because soname moves no longer hold back the updates of a host.
* Good, because a library a person installed, or one that a package outside the run still needs, keeps the run
  refused.
* Bad, because the check runs three more commands for each run that removes packages.
* Bad, because software outside the package manager that links the old library directly is not seen; only packages
  installed as dependencies qualify, which narrows this to what the package manager installed for its own packages.

### Confirmation

`internal/source/apt/replace_test.go`: a run that leaves an old library behind goes ahead and names it with its
successor (`TestInstallSupersededLibrary`); the run is refused when a person installed the library, when no package
of the run comes from its source, when a package outside the run or the new version of one in it still depends on it,
and when a tool cannot answer (`TestInstallSupersededLibraryOnlyWhenAllThreeHold`). The declared replacement keeps its
tests (`TestInstallReplacedPackage`, `TestInstallRealRemovalsStillRefused`).
