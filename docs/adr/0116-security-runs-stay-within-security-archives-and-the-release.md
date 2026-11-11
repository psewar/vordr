---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, asked to build option 1 and chose the allowed sources); proposed by Claude after an analysis of the apt install run
---

# Security runs stay within the security archives and the host's release

## Context and Problem Statement

An install with scope `security` takes the packages whose candidate comes from a security archive and runs
`apt-get install --only-upgrade <names>` (design 10.2). apt resolves their dependencies from every configured source.

When a security fix needs a newer dependency, apt takes it from wherever it is newest: the distribution's `-updates`
or `-backports` pocket, or a vendor's repository. A host that allows only `security`, and the Home Assistant button
"Sicherheitsupdates" on any host, then get more than security fixes. An analysis of the install run found it.

## Decision Drivers

* "Security only" means the security fixes, plus what they need from the host's own release.
* The same rule as unattended-upgrades by default, so a host behaves as its operator expects from Debian and Ubuntu.
* Decided from facts: apt's simulation (the origins it prints for each planned package) and the host's
  `/etc/os-release`, not from tool texts.
* A fix that is held back stays visible as pending, and the run says why.

## Considered Options

1. **Simulate per update, as unattended-upgrades does:** keep an update only when the plan with it changes nothing
   from another source.
2. **`apt-get -t <security suite>`:** raises the security suite's priority. apt still takes a dependency from
   elsewhere when the security suite lacks it, so it guarantees nothing.
3. **A temporary apt preferences file** that pins every other source below 0 for the run. It changes apt's
   configuration while the run lasts, also for anybody else using apt then, and an agent stopped in between would leave
   the pin behind.
4. **Leave it as it is.**

Which sources a dependency may come from:

* A. The security archives and the host's release itself (unattended-upgrades' default).
* B. Only the security archives.

## Decision Outcome

Chosen: option 1 with A.

* **Candidates as before:** packages whose candidate comes from a security archive.
* **One simulation in the usual case.**
  * First all candidates together.
  * When that plan reaches further, one at a time in the order of the full simulation. An update is kept when the
    plan with the ones kept so far and it stays within the allowed sources; otherwise it is held back.
  * With one candidate, its first simulation decides.
* **Allowed:** a planned package one of whose candidate's origins is either
  * a security archive: a suite ending in `-security`, or a label ending in `-Security`; or
  * the host's release. All three must hold:
    * the label is `Debian` or `Ubuntu`;
    * the suite has no dash, e.g. `stable`, `oldstable`, `resolute`;
    * the version is the host's `VERSION_ID` from `/etc/os-release`, or one of its point releases: `12.15` on
      Debian 12. unattended-upgrades matches the codename the same way.
* **Not allowed:**
  * the pockets `-updates`, `-backports`, `-proposed` and `proposed-updates`;
  * another release, even one configured on the host;
  * every vendor repository (for example Proxmox, Docker, the Raspberry Pi archive), also when its suite is `stable`;
  * an `Inst` line without an origin.
* **Unknown release:** when the host's release is unknown, only the security archives count.
* **Both of apt's formats** (`pkgCache::VerIterator::RelStr`):
  * `<label>:<version>/<archive>`;
  * `<label>:<archive>` for an archive whose Release file has no `Version`. Such an archive is never matched to the
    host's release. That covers Raspbian and Debian testing, which have no security archive of their own anyway.
    Raspberry Pi OS on 64 bit uses Debian's archives.
* **Held back:** the updates stay pending.
  * The run ends `ok`, and its detail names each held-back update with the packages it would need and their origins.
  * When it holds back everything, or what it kept plans nothing, it downloads nothing and writes no journal.
* **Out of time:** when the time runs out while the run chooses (one simulation per update), it ends `timeout`, not
  `failed_package_manager`.
* **Unchanged:** removals (ADR-0042), and the scopes `all` and `app`.

B was not chosen. A fix that needs a new dependency from the release, or the release's newer version of one, would
stay pending for good on a host that allows only `security`, because such a host never installs anything else.

How `apt-cache policy` names sources, and how they count:

| Source | Counts as |
|---|---|
| `Debian-Security:12/oldstable-security`, `Debian-Security:13/stable-security` | security |
| `Debian:12.15/oldstable`, `Debian:13.7/stable` | release |
| `Debian:12-updates/oldstable-updates` and its 13 counterpart | not allowed |
| `Proxmox Debian Repository:stable`, `Raspberry Pi Foundation:stable`, other vendor repositories | not allowed |

### Consequences

* Good: a security-only host changes only security fixes and what they need from its release, as unattended-upgrades
  would.
* Good: what was held back, and why, is in the run's result in Home Assistant.
* Bad: a held-back fix stays pending, and the check still counts it as a security update, which it is.
  * Automatic installs with scope `security` then try once per window and change nothing.
  * That lasts until the dependency reaches an allowed source, or someone installs with scope `all`.
* Bad: a canary's security run that installs something counts as a clean install for every update pending before it
  (ADR-0107). That includes updates it held back.
  * The same already held for the updates outside the scope.
  * Where canaries install with `auto_scope: all`, only the button for security updates on a canary reaches this
    case.
* Bad: more simulations when the joint plan reaches further: one per candidate, each well under a second.
* Neutral: the labels are a fixed list. A distribution under another label counts its release as a vendor's, so its
  fixes with dependencies from the release are held back. That is stricter, the safe way round.

### Confirmation

* `internal/source/apt/sources_test.go` (`TestSecurityRunAllows`):
  * every source above in apt's formats: security archives, also a mirror without `Version`;
  * the host's release with point releases, and a package in both the release and `-updates`;
  * not allowed: another release, the pockets, backports, vendor repositories (`stable`, no version, or the host's
    version), and archives without `Version`;
  * an unknown release, and lines without origin or suite.
* `internal/source/apt/install_test.go`:
  * `TestSecurityRunHoldsBackWhatNeedsOtherSources`: one fix with a new dependency from the release goes through;
    one needing `-updates` is held back, named, and never downloaded.
  * `TestSecurityRunHoldsBackEverything`: no download, no journal, `ok` with the reason, one simulation.
  * `TestSecurityRunKeepsNothingToDoButSaysWhatItHeldBack`: the kept update plans nothing; the detail is still there.
  * `TestSecurityRunTimesOutWhileChoosing`: `timeout`, no journal.
  * `TestHeldDetailIsBounded`: the detail's bounds.
* `internal/agent/checker_release_test.go`: the agent hands the host's release to apt; unknown stays empty.
* 15 mutation probes, each reverting one decision and turning a named test red:
  * the release never allowed, or everything allowed;
  * no label check, no pocket rule, no version match, no point-release bound, no guard for an unknown release;
  * the format without a slash misread;
  * the joint plan taken unchecked, or a candidate kept despite another source;
  * the detail lost on each of the three `ok` paths;
  * a timeout reported as a package manager failure;
  * the release not handed to apt.
