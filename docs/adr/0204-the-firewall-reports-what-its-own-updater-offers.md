---
status: proposed
date: 2026-11-20
decision-makers: proposed by Claude (2026-11-20) after kay asked that the firewall's pending updates become installable from Home Assistant; for the maintainer to accept or reject
---

# The firewall reports what its own updater offers

Would supersede [ADR-0198](0198-the-firewall-root-job-names-only-real-package-updates-with-their-versions.md) once
accepted: its blank-separated offers with both versions stay, what counts as an offer changes.

## Context and Problem Statement

The firewall's root job counts every package that the local catalog has in a newer version (status `<` of
`pkg version`, ADR-0198), and the pfSense driver offers each as `package <name>`. pfSense's own updater,
`pfSense-upgrade`, works differently:

* It installs only together with a release: when the meta package `pfSense` or a core package (`pfSense-base`,
  `pfSense-kernel-*`) of the configured branch is newer. Otherwise it says "Your packages are up to date" and installs
  nothing, even while other packages of the branch's repository are newer.
* Such newer packages (the PHP runtime, the VPN daemons, the DNS resolver, the CA bundle and the like) come with the
  next release, or as dependencies when an add-on package (`pfSense-pkg-*`) is updated in the package manager.
* `pfSense-upgrade -C` names a newer release in another release branch, such as a new release line. Switching the
  branch is the operator's step in pfSense's own interface.

So a firewall whose updater has nothing to install is pending for Vördr and turns `stale` after `stale_after_days`
([ADR-0014](0014-stale-after-seven-days.md)); no button and no repair can end that the vendor's way. Installing those
packages with `pkg upgrade` would bypass the updater and leave a mix of packages the vendor never released together.
And a newer release in another branch, the update the operator would actually make, is reported nowhere.

## Decision Drivers

* A pending update on an appliance is one its vendor's updater or package manager would install
  ([ADR-0089](0089-appliances-through-a-proxy-agent.md): the driver reports the device's own view).
* Each pending update has a way to install it, so that `stale` stays a decision for the operator, not noise.
* A newer release in another branch reaches the operator, with the step it needs.
* The root job stays as it is in kind: fixed commands, its own state, read hourly ([ADR-0093](0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)).

## Considered Options

* count what the updater and the package manager offer: the release of the configured branch, updates of installed
  add-on packages, and a newer release in another branch as its own offer
* keep counting every newer package of the catalog (ADR-0198)
* count only the release of the configured branch

## Decision Outcome

Proposed: what the updater and the package manager offer.

* The root job writes `updates_format=3` with:
  * `release_installed` and `release_offered`: the newest version of the meta and core packages, installed and in the
    configured branch's catalog - the comparison `pfSense-upgrade -c` makes;
  * `release_other`: the newest release in another release branch, as `pfSense-upgrade -C -U` names it, read once a
    day (it may fetch the other branches' catalogs from the vendor; the configured branch's catalog stays pfSense's
    own job), empty without one;
  * `addon_offer=<name> <installed> <offered>`: one line per installed add-on package (`pfSense-pkg-*`) with status
    `<`, blank-separated as in ADR-0198.
* The driver offers the release of the configured branch as the firmware (from installed to offered), and each add-on
  offer as `package <name>`. A newer release in another branch is an offer `release <version> (other branch)` that no
  button installs; the integration's text names the step: switch the branch in pfSense's update settings.
* Other newer packages of the catalog are no offers. The root job keeps their count as the fact
  `packages_behind_release`, so that it stays visible, without making the firewall pending.
* Without `updates_format` 3 (an earlier root job) the driver reads the facts as ADR-0198 describes.

### Consequences

* Good, because the firewall is pending exactly when its vendor offers something, and each offer has a way: the
  release and add-ons by the updater (ADR-0205 proposes the button), another branch by the operator.
* Good, because the new release line, the update that matters, shows in Home Assistant.
* Bad, because newer packages of the branch that the vendor ships only with the next release no longer count; a
  security fix among them waits for that release, as it does in pfSense's own interface.
* Bad, because reading other branches may fetch catalogs from the vendor once a day, where today the root job reads
  only the local catalog.

### Confirmation

* `deploy/pfsense/` tests with a stubbed `pkg` and `pfSense-upgrade`: a newer core package gives `release_offered`, a
  newer dependency alone gives none, an older add-on gives its `addon_offer` line, another branch gives
  `release_other`; the `version` call returns them.
* `internal/target`: the offers from format 3, the refusal of malformed lines (as ADR-0198), and format 2 read as
  before.
* Before the first rollout, the root job's facts on a firewall are compared with `pfSense-upgrade -c`, `-C` and the
  package manager's list of installed packages.
