---
status: superseded by ADR-0204
date: 2026-11-19
decision-makers: kay (2026-11-19, asked that the firewall's packages count only real updates, with both versions; accepted with the merge); proposed by Claude
---

# The firewall's root job names only real package updates, with their versions

## Context and Problem Statement

On the pfSense firewall the root job writes the update facts that the forced command's `version` call returns
([ADR-0093](0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)). It listed every package whose
status in `pkg version -R` against the local catalog is not `=`. Besides `<` (the catalog has a newer version) that
took in `>` (the installed version is newer than the catalog's) and `?` (the package is not in the catalog). Neither
is an update that can be installed: such a package stayed listed for good and kept the firewall pending, and after
`stale_after_days` `stale` ([ADR-0014](0014-stale-after-seven-days.md)).

The list, `packages_outdated`, named each package as `<name>-<version>`, joined by commas, and the driver offered each
without a version. Home Assistant showed it as from the firmware's version to "unknown version", although pkg names
both versions of the package. A comma also separates the epoch of a FreeBSD package from its version
(`1.24.0_12,3`), so the list cannot always be split again.

The update facts are an interface between two parts that are rolled out apart: the root job is installed on the
firewall by its operator, the agent arrives as a signed release
([ADR-0032](0032-self-update-signed-by-operator.md)). For a while either may be newer than the other.

## Decision Drivers

* Only an update that can be installed counts as pending.
* Each package shows the version it has and the version it would get.
* An earlier agent keeps working against the new root job, and a new agent against the earlier root job.
* A package that pkg names as outdated never drops out of the report silently.
* The root job still reads only the local catalog (`-U`), and the forced command gets no new call.

## Considered Options

* new lines next to the earlier list, with a form number
* give `packages_outdated` the versions
* the agent asks the firewall for each package's versions through new calls of the forced command

## Decision Outcome

Chosen: new lines next to the earlier list, with a form number.

* The root job asks `pkg version -U -vRl'<'` and keeps only lines whose status is `<`; it checks the status itself
  again. pkg's line `<name>-<version>   <   needs updating (remote has <offered>)` gives the name and the installed
  version (split at the last `-`: pkg allows none in a version) and the offered version.
* The update facts get `updates_format=2` and one line `package_offer=<name> <installed> <offered>` per such package,
  three words without a comma between them. `packages_outdated_count` and `packages_outdated` keep their form and now
  hold only `<`.
* A `<` line whose versions the root job cannot read is still counted and listed, but gets no `package_offer` line.
* The root job reads the facts again at once when its file holds no `updates_format=2`, so the new form is there within
  a minute of the install, not only after the hour.
* The pfSense driver reads the `package_offer` lines when `updates_format` is 2 or higher: each is an offer
  `package <name>` from `<installed>` to `<offered>`. It refuses the answer whole (`rejected_unsupported`) when a line
  is not three words, when the number of lines is not `packages_outdated_count`, or when `updates_format` is not a
  number from 2 on. Without `updates_format` (an earlier root job) it reads `packages_outdated` as before: each package
  without a version.
* A driver's offer names the version it replaces (`From`) when it is not the firmware; empty means the firmware, whose
  installed version the device names. The Synology driver's offers stay as they are.
* A later form keeps the `package_offer` lines and `packages_outdated(_count)` as they are and adds what it needs under
  new keys, so that an agent that reads form 2 reads it too.

Rejected:

* **Giving `packages_outdated` the versions,** because an earlier agent would read the new values as package names,
  and because a comma cannot separate entries whose version has an epoch.
* **New calls of the forced command** (for example `pkg rquery` per package), because every call widens what the key
  may do on the firewall (ADR-0093), and pkg already compares the versions in the one call the root job makes.

### Consequences

* Good, because a package whose installed version is newer, or that is not in the catalog, no longer keeps the
  firewall pending.
* Good, because Home Assistant shows each package with its own name and both versions.
* Good, because the earlier list and its count stay: an earlier agent sees only real updates, still without versions.
* Good, because a change in pkg's output shows as a refused check with its reason, not as a package that is gone.
* Bad, because one line the root job cannot read refuses the whole check, the firmware's offer included, until the root
  job reads it again.
* Bad, because `packages_outdated` keeps its comma for earlier agents; from form 2 on the driver no longer reads it.
* Bad, because the name of a package in the report changes from `package <name>-<version>` to `package <name>`.

### Confirmation

`deploy/pfsense/scripts_test.go`: a stand-in pkg answers with `<`, `>`, `?` and `=`, a name with dashes and a version
with an epoch; the root job keeps only `<` and writes the form, the lines and the earlier list
(`TestRootJobUpdateFacts`), reads an earlier form again at once (`TestRootJobReplacesAnEarlierForm`), and counts a
`<` line it cannot read without writing a line for it (`TestRootJobCountsALineItCannotRead`).
`internal/target/pfsense_test.go`: offers with both versions (`TestPfsenseCheck`), an earlier root job
(`TestPfsenseEarlierRootJob`), the refusals and a later form (`TestPfsenseRefusesFactsItDoesNotRead`).
`internal/agent/target_test.go`: the report takes each offer's own `From` (`TestTargetCheckerPackageOffers`).
