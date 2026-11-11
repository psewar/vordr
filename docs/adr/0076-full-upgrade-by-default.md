---
status: accepted
date: 2026-10-30
decision-makers: kay ("yes, build it this way")
---

# `full-upgrade` is the default for scope `all`

Supersedes [ADR-0031](0031-install-mode-full-upgrade.md).

## Context and Problem Statement

For scope `all` the agent used `upgrade`, except on Proxmox hosts. `upgrade` never installs new packages, so updates
that need new dependencies stay pending: a new kernel, `linux-firmware`, desktop packages. ADR-0031 made
`full-upgrade` selectable per host, but not the default. In practice every Linux host already had
`install.mode: full-upgrade` in its `agent.yaml`, set by the deploy tool with the operator's OK. A new host without the
line would silently fall back to `upgrade`.

## Decision Drivers

* "All updates" should install all updates.
* No run removes packages unnoticed; that guard exists for both modes
  ([ADR-0042](0042-replaced-packages-are-no-removal.md)).
* A default should match what every host uses, so a forgotten line changes nothing.

## Considered Options

* keep `upgrade` as the default, `full-upgrade` per host
* `full-upgrade` as the default, `upgrade` per host

## Decision Outcome

Chosen option: `full-upgrade` as the default (the operator, 2026-10-30). Without `install.mode` scope `all` runs
`apt-get full-upgrade` on every host; `install.mode: upgrade` still chooses `upgrade`. The agent no longer asks dpkg
whether it runs on Proxmox; Proxmox gets `full-upgrade` like every other host. The fact `install_mode` reports the
default, `full-upgrade`.

### Consequences

* Good, because new hosts get kernels and firmware without an extra line, and existing hosts do not change.
* Good, because two `dpkg-query` calls per check are gone.
* Bad, because `full-upgrade` may install new packages a host did not have; the run summary lists them.
* Bad, because agents before this release still use `upgrade` without the line; the deploy tool keeps writing
  `mode: full-upgrade` until every agent has this release.

### Confirmation

`TestInstallAll` (default runs `full-upgrade`), `TestInstallModeFromPolicy` (`install.mode: upgrade` wins),
`TestInstallModeDefault` (fact `full-upgrade`, no `dpkg-query`).
