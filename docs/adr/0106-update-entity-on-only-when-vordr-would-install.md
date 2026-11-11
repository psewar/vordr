---
status: accepted
date: 2026-11-04
decision-makers: kay (2026-11-04, chose this among the variants against notification fatigue; accepted once released); proposed by Claude
---

# An update entity is on only when Vördr would install something

Refines the update entities of [ADR-0023](0023-own-ha-integration.md) (`design/architecture.md` 8.3 and 11.4).

## Context and Problem Statement

The update entity "system packages" of a host was `on` whenever anything was pending, and the entity of a pip/npm
location whenever a direct package had a newer release. Home Assistant counts every such entity in its update badge.

Several of them stood for updates that no one could install through Vördr:

* a host whose policy allows only the scope `security`, while many other updates were pending; its install button
  installs security updates only, so there was nothing to press for;
* a pip location whose only newer release was younger than the minimum age
  ([ADR-0068](0068-minimum-age-for-target-versions.md)), shown as "1 outdated, 0 ready".

An update badge that is always lit teaches the operator to stop looking at it, and the updates that do wait for a
button drown in it.

## Decision Drivers

* The badge means "there is something to do here".
* What the install button would install is the hub's knowledge (the host's policy and the scope it resolves); the hub
  reports it as a fact instead of the integration working it out
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md), state as a fact).
* Nothing gets lost: what waits stays visible, and a backlog that nobody installs still raises a problem.
* A hub without the new field keeps working with the new integration.

## Considered Options

1. Leave it: `on` whenever anything is pending.
2. The hub reports per host how many pending updates the install button would install (`installable`); the system
   entity is `on` only when that is above zero, the package entity only when a candidate is ready.
3. The same, but the integration derives the number from the capabilities and the counts.
4. Rely on "Skip" in Home Assistant.

## Decision Outcome

Chosen: option 2, since integration 0.20.0 and hub 0.22.0.

* `HostView.installable`: the pending updates the install button would take under the host's policy. The default
  scope `all` takes everything pending, `security` the security updates. Without an install button but with the
  button for security updates, it is the security updates; with neither, zero. Unknown (nil) exactly when `pending` is.
* The system entity shows `latest_version = installed_version` (so `off`) when `installable` is zero; otherwise it
  words the pending set as before. Without the field (an older hub) it behaves as before.
* The entity of a pip/npm location is `off` when none of its outdated packages has a ready candidate (all too new or
  flagged by OSV).
* Unchanged: the sensors for pending and security updates, the release summary, the agent entity, and the problem
  `stale`, which still counts every pending update from `pending_since`. A host on which updates pile up without being
  installable is reported after `stale_after_days`.

Rejected:

* **Option 1,** because a badge that never goes out is noise, and it hides the updates that do need a button.
* **Option 3,** because the integration would repeat the hub's rules for scopes; a later rule (another scope, an app)
  would have to be built twice.
* **Option 4,** because "Skip" in Home Assistant is work for the operator again, and it only lasts until the pending
  set changes (design 8.3).

### Consequences

* Good, because the update badge counts only what an install would change, at once, before anything is installed.
* Good, because the counts stay where they were (sensors, attributes); only the meaning of `on` changes.
* Bad, because a host that Vördr only monitors, without an install button, no longer shows its pending updates as an
  update entity. The sensors and the problem `stale` still do.
* Bad, because a host whose policy keeps updates out on purpose is only noticed through `stale`; that alarm comes
  after `stale_after_days` and asks for a decision about the policy.

### Confirmation

* `TestInstallableFollowsTheInstallButton` (hub): `all`, `security`, only the security button, no install button, an
  unknown pending count.
* `test_system_update_only_when_installable` and `test_packages_update_only_when_ready` (integration): `off` with
  nothing installable or ready, `on` otherwise, and the old behaviour without the field.
* A live check: the entity of a pip location with "1 outdated, 0 ready" is `off`; the system entity of a host whose
  policy allows only `security` is `off` while its other updates stay in the sensors; the system entities that stay
  `on` belong to hosts whose install button takes their pending updates.
