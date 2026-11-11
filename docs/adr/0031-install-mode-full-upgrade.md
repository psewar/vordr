---
status: superseded by ADR-0076
date: 2026-10-29
decision-makers: kay
---

# `install.mode`: `full-upgrade` selectable per host, not the default

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 10.2 ("Umfang `all`"), the
operator's project notes.

**Superseded by [ADR-0076](0076-full-upgrade-by-default.md).**

## Context and Problem Statement

With `upgrade`, "all updates" leaves packages behind that need new dependencies (on Ubuntu typically kernel and
`linux-firmware`). `upgrade` leaves them, `full-upgrade` does not. On Proxmox hosts the agent already used
`full-upgrade`.

## Decision Drivers

* "All updates" should install all updates.
* No run may remove packages unnoticed.

## Considered Options

* `full-upgrade` as the default for everyone
* option `install.mode` per host, default unchanged

## Decision Outcome

Chosen option: option `install.mode` (`upgrade` or `full-upgrade`) for scope `all`. Without it, the default stays
(Proxmox `full-upgrade`, otherwise `upgrade`). `full-upgrade` as the default was deliberately left out. A run that
would remove packages is refused in both modes ([ADR-0042](0042-replaced-packages-are-no-removal.md) covers the
exception).

### Consequences

* Good, because a host with `full-upgrade` also installs packages such as `linux-firmware`.
* Bad, because the choice sits in every `agent.yaml` separately.

### Confirmation

The policy refuses an unknown mode; apt tests for `install.mode`.
