---
status: rejected
date: 2026-10-30
decision-makers: kay ("yes, build it this way": do not hold Docker back)
---

# Hold Docker packages back during updates

## Context and Problem Statement

A docker-ce update left a database container stopped although it had a restart policy; the services using it had no
database until someone started it by hand. Holding the Docker packages back (`apt-mark hold`) had been left out on
purpose earlier and had stayed open since.

## Decision Drivers

* Docker updates carry security fixes (containerd, runc).
* A service must not stay down after an update.
* Nothing should stay pending for good without someone deciding it.

## Considered Options

* hold the Docker packages and update them by hand
* update them like every other package, with the agent restarting containers and checking health afterwards

## Decision Outcome

Rejected (the operator, 2026-10-30). Docker is updated like any other package. That failure is handled
elsewhere: the agent starts containers with a restart policy again after an install
([ADR-0061](0061-restart-containers-after-install.md)), and health checks report what still does not run
([ADR-0038](0038-health-checks-after-install.md)). Held packages would stay pending and turn `stale` after 7 days
([ADR-0014](0014-stale-after-seven-days.md)).

### Consequences

* Good, because Docker fixes arrive with the other updates, and a service that does not come back is a problem in HA.
* Bad, because containers still stop briefly during a Docker update.
* Note: where Docker comes from Docker's own repository (`docker-ce`), Vördr does not count that repository as a
  security source; those updates arrive with scope `all`, not with the automatic security updates.

### Confirmation

Not applicable; nothing is held. `TestInstallStartsContainersAgain` covers the restart.
