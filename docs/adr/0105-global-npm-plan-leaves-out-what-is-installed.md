---
status: accepted
date: 2026-11-03
decision-makers: kay (2026-11-03, chose this fix over leaving it); proposed by Claude
---

# The plan of a global npm update leaves out versions the location already has

Refines [ADR-0070](0070-hub-vets-and-signs-the-update-plan.md) (the hub vets the plan) for global npm locations.

## Context and Problem Statement

Before a package update the hub vets the plan, and one flagged entry stops the run (ADR-0070). The plan is meant to be
what the update brings that is not installed yet, by name and version (design 11.4). In an npm folder with a lock file
the agent gets that by comparing the old and the new lock.

A global npm location has no lock file. npm resolves the updated package's whole tree into an empty folder, and every
entry of that tree counted as planned, also versions that are already installed in the location.

An update of a globally installed package could be refused with `precheck_version_flagged` although every flagged
version in its plan was already installed in the same location, under another global package. The update would have
brought none of them anew and could even remove other flagged versions. As it stood, no update of that package could
ever pass while another package of the location carried those versions.

## Decision Drivers

* The vet keeps new code with known findings out (ADR-0070); it must not miss anything new.
* Code that is already on the host is no new risk from the update. Its findings are reported anyway.
* The same rule for every kind of location: what was not there before.
* No new safeguard to switch off; nothing the operator has to decide per update.

## Considered Options

1. Leave it: the whole resolved tree is the plan.
2. Compare the resolved tree with what the location has installed, by name and version: the plan is what it does not
   have yet.
3. Allow flagged entries in the plan when the update leaves fewer findings than before.

## Decision Outcome

Chosen: option 2. For a global npm location, the "before" of the plan is what the location reports as installed, by
name and version; a version that is already there, under this package or another, is left out. Everything else is
vetted as before, and one flagged new entry still stops the run.

Rejected:

* **Option 1,** because it refuses updates for code they would not bring, and keeps a location on more findings than
  the update would leave.
* **Option 3,** because it would let new flagged code in whenever it replaces enough old flagged code. The vet's rule
  stays simple: nothing new with a known finding.

### Consequences

* Good, because the plan of a global update means the same as in an npm folder with a lock file.
* Good, because the vetted plan gets smaller; in the case that showed the problem, by about half.
* Bad, because a flagged version already installed elsewhere in the location can now arrive under the updated package
  as well. That is a second copy of code the host already runs, not a new one, and its finding stays reported.
* Neutral: the check after the install is unchanged, every target at its version.

### Confirmation

* `TestNpmUpdate`: a global update whose resolved tree holds a flagged version the location already has vets only the
  new entries and installs; a flagged new entry (malware in the tree) still stops the run. Without the change the test
  fails.
* Dry run with the new binary (`vordr-agent packages --plan npm-global/<package>`) on such a location: the plan leaves
  out the versions the location already has, the flagged ones among them.
