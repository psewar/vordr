---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# The agent starts containers again after an install

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: the operator's notes (expansion plan of 2026-10-30), the
operator's project notes.

## Context and Problem Statement

A docker-ce update can leave a container stopped although it has the restart policy `unless-stopped`. Services that
depend on it stay down until someone starts it by hand. Vördr reports `ok`.

## Decision Drivers

* A container that ran before the install and should run must run after it.
* Do not touch containers someone stopped on purpose.

## Considered Options

* Docker `live-restore` ([ADR-0062](0062-docker-live-restore.md), rejected)
* hold Docker packages back (left out on purpose earlier)
* the agent starts the containers again

## Decision Outcome

Chosen option: the agent starts them again. Before an install it notes which containers with a restart policy
(`always`, `unless-stopped`) are running. Afterwards it waits until the Docker daemon answers and starts those that do
not run, before the health checks ([ADR-0038](0038-health-checks-after-install.md)). The run summary lists them
(`restarted`). Containers without a restart policy and those restarting are left alone.

### Consequences

* Good, because a Docker update no longer takes services down silently.
* Bad, because a new protocol field: hub 0.17.0 before agent 0.8.3 ([ADR-0049](0049-site-decision.md)).

### Confirmation

`TestContainersRunningWithRestartPolicy`, `TestContainersRestore`, `TestContainersRestoreWithoutDaemon`,
`TestInstallStartsContainersAgain`; a counter-check when it was built.
