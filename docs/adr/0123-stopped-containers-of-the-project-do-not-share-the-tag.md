---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Yes, build it", after the first service chosen for updates by button turned out to be held back by its own one-shot helper); proposed by Claude
---

# Stopped containers of the same Compose project do not share the tag

Refines [ADR-0119](0119-update-container-images-by-button.md) (update container images by button) in one point: when
a tag counts as shared. Everything else there stays.

## Context and Problem Statement

ADR-0119 moves a Compose service to the rebuild of its tag by moving the tag to the new image and having Compose
recreate the service. A tag that another container also uses stays manual: the tag would move under that container
too, and after a rollback two services on one tag could end up on different images.

A service chosen for updates by button could never be offered. Its project, as published by the service's own
authors, has a one-shot helper: a second service on the same image that only sets the permissions of the volumes and
exits (`restart: "no"`), which the main service waits for (`depends_on`). The helper's stopped container uses the same
tag, so the agent refused the main service with "the tag is also used by the container …". The pattern is common:
migrations, permission fixes and setup steps run as a stopped container of the same image beside the service.

A stopped container does not change when a tag moves. It keeps its image by id; `docker start` starts that image
again. It takes the new image only when Compose recreates it, which happens when the project is brought up again: for a
container of the same project, that is what an update of the project means.

## Decision Drivers

* The rule of ADR-0119 protects running services that would silently change or end up apart.
* A one-shot helper next to the service is a common pattern and should not block the service.
* The operator's Compose files stay as they are: the agent does not edit them, and they often come from upstream.
* No new setting: the rule follows from the containers' state and labels.

## Considered Options

1. Keep the rule: every container on the tag counts.
2. Leave out stopped containers of the same Compose project.
3. Leave out every stopped container, of any project or none.
4. Let the operator name containers to leave out, in `agent.yaml`.

## Decision Outcome

Chosen: option 2.

* A container that is stopped (`exited`, `created` or `dead`) and belongs to a service of the same Compose project
  does not count as sharing the tag.
* Every other container on the tag still holds the service back: running, `paused` and `restarting` ones in the same
  project, stopped ones of another project, a stopped `docker compose run` container (it belongs to no service), and
  those without Compose labels.
* Nothing else changes in the run. Compose recreates only the service (`--no-deps`), so the helper keeps its image; the
  cleanup after a later update leaves an image alone while any container, stopped ones included, still uses it.

Rejected:

* **Option 1,** because such a service would stay manual for a container that nothing changes.
* **Option 3,** because a stopped container of another project, or one started by hand, belongs to something the
  service's update is not about; whether its next recreation should take a new image is not this update's question.
* **Option 4,** because the state and labels tell it already, and a list in the policy would have to be kept in step
  with the Compose files.

### Consequences

* Good, because services with a one-shot helper can be updated by button.
* Good, because a running second service on the tag still keeps both manual.
* Bad, because the update does not run the helper again (`--no-deps`), and its container stays on the old image until
  the project is next brought up. For a step that only had to run once, such as setting permissions, that is fine. A
  helper that must run before every new version, such as a database migration, would be skipped. The operator allows
  each service by name (ADR-0119), so such a service is simply not allowed; and if the service does not come up
  healthy without it, the run goes back to the old image.
* Neutral: the old image stays on disk while the helper's container holds it.

### Confirmation

* `internal/images`:
  * `TestImageUpdateStoppedHelperOfTheProject`: with a helper `exited`, `created` or `dead` in the same project, the
    service is offered and moves, the helper keeps its image and state, and Compose recreates only the service.
  * `TestImageUpdateRefusesWithoutWriting`: a stopped container of another project, a stopped `docker compose run`
    container of the same project, and a running or restarting one of the same project still hold the service back,
    before anything is written.
* A live check: a service with such a helper is offered in Home Assistant and moves by button.
