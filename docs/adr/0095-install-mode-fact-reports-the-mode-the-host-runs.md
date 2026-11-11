---
status: accepted
date: 2026-10-31
decision-makers: kay (2026-10-30, "beides": fix the misleading fact; 2026-10-31, "Ja, Fix starten"); proposed by Claude
---

# The fact `install_mode` reports the mode the host runs

Refines [ADR-0076](0076-full-upgrade-by-default.md). Its decision, `full-upgrade` as the default for scope `all`,
stands; only its sentence "The fact `install_mode` reports the default, `full-upgrade`" changes.

## Context and Problem Statement

The apt source reports the fact `install_mode` with every check. Since ADR-0076 it is the constant default,
`full-upgrade`. The run itself takes `install.mode` from the host's `agent.yaml` when it is set. A host with
`install.mode: upgrade` therefore runs `apt-get upgrade` but reports `full-upgrade`: the fact says something the host
does not do. Found while checking the facts after "update all"; it shows only on a host that sets
`install.mode: upgrade`.

## Decision Drivers

* A fact states what the host does, not a default someone must combine with the policy in their head.
* The fact and the run come from one place, so they cannot drift apart again.
* No new field in the protocol for one value.

## Considered Options

* Keep the fact as the default (ADR-0076) and document that `install.mode` overrides it.
* Report the mode scope `all` runs: `install.mode` of the policy, else `full-upgrade`.
* Drop the fact; the policy summary the agent sends at hello carries `install.mode`.

## Decision Outcome

Chosen: report the mode scope `all` runs (the operator asked for the fix, 2026-10-30). The agent passes `install.mode`
of its policy to the apt source (`agent.System(host, refresh, mode)`, field `apt.Apt.PolicyMode`). One function,
`runMode`, gives the mode for both the check's fact and the install run's command.

Rejected: documenting the gap, because a fact that needs a footnote is the misleading display this fixes. Dropping the
fact, because `vordr-agent check` and the last report the hub keeps already carry the source's facts, and the policy
summary would need a new field for the same value.

### Consequences

* Good, because `install_mode` on a host with `install.mode: upgrade` now says `upgrade`.
* Good, because the fact and the run share `runMode`; a change of the default changes both.
* Bad, because agents before this release keep reporting `full-upgrade` whatever their policy says, until they update.
* Neutral: on a host that sets `install.mode: full-upgrade` or no mode, the value stays `full-upgrade`.

### Confirmation

`TestInstallModeDefault` (no `install.mode`: fact `full-upgrade`), `TestInstallModeFactFollowsPolicy`
(`install.mode: upgrade`: fact `upgrade`), `TestSystemCheckerPassesPolicyMode` (the agent hands the policy's mode to
apt), `TestInstallModeFromPolicy` (the run uses the same mode).
