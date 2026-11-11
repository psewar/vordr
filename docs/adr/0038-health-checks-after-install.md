---
status: accepted
date: 2026-10-29
decision-makers: kay ("implement the proposals")
---

# Health checks after an install (`health:`), result `failed_health`

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 7.3 and 10.2, the operator's
project notes (2026-10-30: "yes, set it up").

## Context and Problem Statement

A run that ends `ok` can still leave a host without its job. On a host whose main service runs in Docker, a Docker
update restarts that service; if it does not come back, nobody notices until the service is missed.

## Decision Drivers

* "ok" should mean that the host still does its job.
* Names from the configuration become command arguments; they must be safe.

## Considered Options

The sources name no alternative.

## Decision Outcome

`health:` in `agent.yaml` lists what must run after an update: `systemd` (unit active), `docker` (container running,
and healthy if it has a health check) or `http` (GET with 2xx/3xx within 10 s; no certificate check, local services).
One kind per entry, names checked by a pattern without options or shell, at most 20 entries.

After a run that changed packages and ends `ok` or `ok_reboot_required`, the agent repeats the checks every 15 s for up
to 3 minutes. If one still fails, the run ends with `failed_health` (a problem), naming the failed check.

### Consequences

* Good, because a service that does not come back is a problem in HA right away.
* Bad, because a new container on a host means updating its `agent.yaml`.

### Confirmation

`TestHealthWaitsForServicesToComeBack`, `TestHealthNamesWhatFails`, `TestInstallEndsFailedHealth`; the deploy tool
refuses to set up a check that already fails before the update.

## More Information

A Docker update can leave a container stopped although it has a restart policy; without health checks Vördr then
reports `ok`. See also [ADR-0061](0061-restart-containers-after-install.md).
