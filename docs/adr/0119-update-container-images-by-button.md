---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, took the recommendation on all three open questions); proposed by Claude, as ADR-0081 and ADR-0113 left it for an ADR of its own
---

# Update container images by button

## Context and Problem Statement

Vördr reports outdated container images ([ADR-0113](0113-report-outdated-container-images.md),
[ADR-0117](0117-compare-pinned-images-under-their-tag.md)) and does nothing else. The operator then updates by hand
on each host: `docker compose pull` and `docker compose up -d`. [ADR-0081](0081-container-images-own-source.md) put
updating by button off to an ADR of its own: "Updating a running service is risky; reporting is not."

For system packages the agent already has what makes an update safe:

* a local policy that allows each scope;
* a backup check before the run;
* the run journal;
* health checks after;
* fixed codes.

An image update needs the same, plus two things packages do not: the container must be recreated, and there must be a
way back to the old image.

## Decision Drivers

* Closed by default: nothing is updated unless the host's agent.yaml names the service.
* The same safeguards as for packages: the backup check (ADR-0096 for guests), the minimum age (only candidates), the
  journal, health after the run, fixed codes.
* A way back that needs no registry: the old image stays on the host until the new one has proved itself.
* No new remote access. The agent already runs as root next to the engine; what changes is that it now writes, so the
  local policy must allow it.
* The Compose file stays the operator's: the agent does not edit it.

## Considered Options

1. **Compose, per service:** for a container with Compose labels (`com.docker.compose.project`,
   `.project.working_dir`, `.project.config_files`, `.service`):
   * keep the running image under a local tag `vordr-previous`;
   * `docker compose pull <service>`, then `docker compose up -d --no-deps <service>`;
   * health check: the container is `running`, and `healthy` if it has a health check, within a timeout;
   * on failure, tag the old image back and `up -d` again.
2. **Engine API directly:** pull, then create a container with the old one's configuration and swap them. The
   container would drift from its Compose file, and the next `docker compose up` would undo or confuse it.
3. **A third-party updater** (Watchtower and similar): no local policy, no backup check, no canaries, no codes;
   updates2mqtt was rejected for the same reasons (ADR-0081).
4. **Keep reporting only.**

## Decision Outcome

Chosen: option 1, with these limits:

* **Only Compose services with a tag.** A pinned reference (`@sha256:…`) is not updated: moving a pin means editing
  the Compose file, and that stays the operator's. A container without Compose labels is not updated.
* **Allowed per service in agent.yaml:** a new scope `image:<project>/<service>` in `install.allowed_scopes` (patterns
  allowed). No service is allowed by default.
* **Only candidates:** the new image must be at least `min_age_days` old, as for packages (ADR-0113's
  `candidate`, `rebuilt_candidate`).
* **Before the run:**
  * the hub's backup check for the host, as for every install;
  * the agent's journal is written before `up -d`, as before dpkg.
* **After:**
  * the service's health, as above;
  * then agent.yaml `health` (ADR-0045);
  * on failure, back to the previous image, ending `failed_health` with `rolled_back` in the detail.
* **Home Assistant:** the container entity gets an install button only when the host allows at least one of its
  services.
* **Out of scope:** automatic image updates (`install.auto`); a later ADR can add them once button runs have shown
  how they behave.

### Decided by the operator (2026-11-05)

1. **The agent may write to the engine, narrowly fenced.**
   * Only for services agent.yaml names (`image:<project>/<service>`); none by default.
   * Only candidates.
   * Only the fixed Compose commands above: the hub's job names the allowed service and nothing else, no image, tag
     or argument.
   * The first services allowed are non-critical ones (for example monitoring). Central services stay manual.
2. **The way back keeps one generation.**
   * The previous image keeps its local tag until that service's next successful update.
   * Before the pull the agent checks the free space for the new image beside the kept one, as apt's run does
     (design 10.2), and refuses with `precheck_disk_space` when it is short.
3. **Pins are not moved.** A pinned service stays the operator's. Vördr reports that the pin is behind its tag
   (ADR-0117), and the operator moves it by hand, with a backup.

Built after [ADR-0120](0120-known-vulnerabilities-inside-container-images.md), which only reads.

Decided while building it (2026-11-06):

* **Only a rebuild of the tag** (the operator, 2026-11-06). `docker compose pull` brings only what the Compose file's
  tag points to now. A newer tag (ADR-0113's `candidate`, 1.2 -> 1.3) would mean editing the Compose file, which stays
  the operator's, as a pin does. What can be installed is ADR-0113's `rebuilt_candidate`; Home Assistant still shows
  newer tags and says that they stay manual.
* **The entity's button updates every allowed service with a rebuild old enough, in one run** (the operator,
  2026-11-06).
  * Scope `images`: the agent picks the services from its own policy; the job names none.
  * It goes service by service. A service that cannot be moved, whose rebuild is too new or whose registry gave no
    answer stays as it is and is named, with the reason, in the detail. A failure after a change is rolled back and
    ends the run.
  * `image:<project>/<service>` does one service; Home Assistant's action `vordr.update_image` sends it.
* **Pulled by digest, not by `docker compose pull`.**
  * At the time of the run the agent asks the registry what the tag points to, checks that build's age, and pulls
    exactly that digest for the platform it checked, through the engine API.
  * It then gives that image the Compose file's tag and runs
    `docker compose up --detach --no-deps --no-build --pull never <service>` with the project's files from the labels,
    against the agent's own engine (`DOCKER_HOST`), whatever Docker context root has.
  * The effect is that of `compose pull` for the tag, without a gap between judging a build and pulling one that has
    moved on since; and Compose can neither pull nor build, whatever the service's `pull_policy` says.
  * This needs Compose 2.22 or newer (`up --pull`); with an older one or none the run is `rejected_unsupported`.
* **Only the image changes, never the configuration.** Before anything changes, `docker compose config --hash
  <service>` must give the hash the running container was created with (its label). Otherwise the project's files or
  their variables changed since, `up` would apply edits nobody checked, and going back would bring back only the image;
  the service stays manual until someone brings it up by hand. With only `PATH` and `DOCKER_HOST` set, the hash matches
  for a running Compose service whose files and variables are unchanged.
* **What stays manual besides a newer tag or a pin:** a service with more than one container (Compose would scale it
  to its file); a tag another container also uses (moving it would change that one too); a container without Compose
  labels or of `docker compose run`; an image built on the host; a project whose directory or files are gone or not
  absolute. The agent reports per image only the services it can move (`updatable`), from the engine's view; the hub
  offers only those. The configuration check comes at the run.
* **The way back is tagged per service:** `<repository>:vordr-previous.<project>.<service>`. Project and service names
  are bounded to 56 characters each, so the tag fits.
* **The journal is written after the pull, before the first tag changes.** The pull adds an image and changes nothing
  that runs; a failed pull leaves no journal.
* **Once something changed, it is finished or undone.** What follows the journal for a service - recreating, waiting,
  going back - runs on a budget of its own (30 minutes), apart from `install.timeout_min` and from the agent stopping,
  as the apt run does for dpkg. A run whose time is up starts no further service.
* **Healthy means running steadily.** The service's container runs the new image, `healthy` where it has a health
  check, and then runs for 30 seconds without a restart, within 3 minutes; then agent.yaml `health`. A container that
  dies after a few seconds and is restarted by its policy - a rebuild that needs a new setting - never gets there.
  Otherwise it goes back and ends `failed_health` with "rolled back" in the detail; a failed `docker compose up` ends
  `failed_package_manager`, also rolled back.
* **One generation:**
  * After a successful update the agent removes the image of the update before, once no name and no container holds
    it.
  * After a rollback the keep tag goes back to where it was (to the update before's way back, if there was one), and
    the new image is removed.
  * The keep tag leaves the old image only once that image carries the Compose file's tag again: the engine deletes an
    image whose last name goes.
* **Free space:** four times the download (the compressed layers as the registry lists them; unpacked they take about
  three times as much) plus 256 MB, on the engine's data directory and, with the containerd image store, on
  containerd's where it exists. Too little, or a free space that cannot be read: `precheck_disk_space`.
* **Few counted downloads.** The run reads the inventory's cache of the registries' answers, which also keeps each
  build's size, and does not write it; a build the inventory knows costs no manifest download before the pull.
* **No new codes.** A service the agent cannot move is `rejected_scope_not_allowed`, with the reason in the detail.
* **Policy:**
  * `image:` entries need `packages.containers` and `packages.outdated`. Patterns allow only `*`.
  * `default_scope` stays for the system packages: it is required only when `allowed_scopes` names system packages
    or apps, and it is never an image entry. `images` is a job scope, not an entry.
  * `check-config` names the services whose rebuilds are installed by button.
* **The minimum interval does not hold image updates back,** as for pip and npm (ADR-0110); a run that changed
  something counts as an install run.
* **Home Assistant:** the hub counts the images whose rebuild the button would take (`installable`: a rebuild old
  enough, and a service allowed and movable). The entity offers the button only then, as the system entity does
  (ADR-0106); it still turns on for what stays manual, as before.

### Consequences

* Good: image updates with the same safeguards as packages, from the same place.
* Bad: the agent writes to the engine, a new power on every host that allows it. The local policy holds it back, and
  nothing is allowed by default.
* Bad: the way back keeps one old image per updated service on disk.
* Bad: a service whose new image needs a changed Compose file (new variables, a migration) fails its health check and
  goes back. The operator must then update it by hand.

### Confirmation

With the implementation, at least:

* refusal without the scope in agent.yaml, and for a pinned or non-Compose container;
* refusal when the free space does not hold the new image beside the kept one;
* the way back on a failed health check;
* the journal written before the swap;
* no `POST` to the engine without an allowed scope (a fake engine records the methods);
* nothing from the job besides the service's name reaches a command line.
