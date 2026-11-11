---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# pip and npm updates only by button, never automatically

## Context and Problem Statement

Vördr installs system updates automatically on request (`install.auto: hub`, canaries, security updates only). Should
outdated pip and npm packages be updated automatically as well?

## Decision Drivers

* Malicious new versions are the most common supply-chain attack; an automaton would spread them within the window.
* Language packages have no security classification like `*-security` in apt; "security only" does not exist.
* An update in a service venv or in Node-RED can break the service; someone should be watching.

## Considered Options

1. Automatically like apt, in rings.
2. Only by button in Home Assistant, per location or per package, with the safeguards of
   [ADR-0068](0068-minimum-age-for-target-versions.md), [ADR-0070](0070-hub-vets-and-signs-the-update-plan.md) and
   [ADR-0071](0071-no-code-during-install.md).
3. Not at all, only report.

## Decision Outcome

Chosen option: **option 2** (the operator, 2026-10-30). There is no path to an automatic package update:
`install.auto` only knows `security` and `all`, the hub never starts `pkg:` jobs itself, and `packages.update` in
`agent.yaml` names the locations that may be updated at all (default: none).

### Consequences

* Good, because every package update has a trigger (`by: ha:<client>`) that saw it.
* Good, because with `packages.update` the host itself decides which locations HA may touch.
* Bad, because outdated packages stay until someone presses; HA shows them on the sensor "Veraltete Pakete" for that.

### Confirmation

`Resolve` only signs `install_pkg:` for locations with `update`; `TestPackageRunGates`; the auto-install planner knows
no `pkg:` scope (`internal/hub/auto.go`).

## More Information

`design/architecture.md` 11.4.

Merged in German as ADR-0105; later renumbered and translated.
