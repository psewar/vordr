---
status: accepted
date: 2026-10-29
decision-makers: kay ("build install, then test")
---

# A run does not belong to its connection, and dpkg is never stopped halfway

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 6.1 (`cancel`) and 10.2
("Festgelegt im Bau").

## Context and Problem Statement

A package run takes minutes. Meanwhile the tunnel can break, the hub can restart or the service can be stopped. A dpkg
run stopped halfway leaves a half-updated system.

## Decision Drivers

* No half-updated system.
* The result of a run is never lost.

## Considered Options

The sources name no alternative.

## Decision Outcome

* The agent writes a run journal (`run.json`) before it touches a package. Progress and result go over the current
  connection, otherwise from the journal over the next one. A late result replaces the provisional `interrupted` at
  the hub.
* dpkg is never stopped halfway. `cancel` only works until the installation starts. Stopping the service waits for
  the run (`KillMode=mixed`, `TimeoutStopSec=30min`). `install.timeout_min` applies until the installation starts; the
  installation itself ends with `timeout` after twice that time at the latest.
* Download and installation are separate (`apt-get -d`, then install); package runs use `ionice -c2 -n7`.

### Consequences

* Good, because a hub restart during a run costs no result.
* Bad, because a hanging run cannot be cancelled once the installation has started.

### Confirmation

`TestInstallRun`, `TestInstallResultReachesTheHubLater`; a field test on a host shows the result in HA.
