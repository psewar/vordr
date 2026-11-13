---
status: accepted
date: 2026-11-13
decision-makers: kay (2026-11-13, asked to check and fix the image entities that Home Assistant lists as "not installable updates", and to merge and release the fix); proposed by Claude
---

# The image entity is on only when its install button would take something

Refines [ADR-0130](0130-image-entity-needs-attention-only-with-a-newer-image.md) and
[ADR-0119](0119-update-container-images-by-button.md) in one point: what turns the update entity "Container images"
on. ADR-0130 turns it on while an image has a candidate (`ready` above zero); ADR-0119 keeps it on for what stays
manual. This ADR applies the rule of [ADR-0106](0106-update-entity-on-only-when-vordr-would-install.md) to it without
an exception: on only when the install button would take something. Everything else in both ADRs stays.

## Context and Problem Statement

`ready` counts every outdated image with a candidate at least `min_age_days` old: a rebuild under its tag, a newer
tag, or the smaller step within the running major version
([ADR-0131](0131-name-the-newest-tag-of-the-running-major-version.md)). The install button takes only a part of that:
rebuilds of the Compose services the host allows (ADR-0119), which the hub counts as `installable`. A newer tag needs
the Compose file changed; a service the host does not allow, and every image of a host without allowed services,
stays the operator's.

So the entity stands on without an install button whenever the only candidates are manual ones: a newer tag, a
rebuild of a service the host does not allow, or any candidate on a host that only reports its images. Home
Assistant's update overview lists such an entity apart, as an update that cannot be installed, and it stays there until
someone edits a Compose file or changes the host's policy. ADR-0106 removed exactly this from the other update
entities: a lit update without anything to press teaches the operator to stop looking.

## Decision Drivers

* "On" means the same on every update entity: the install button would install something (ADR-0106).
* Nothing gets lost: the images that stay manual remain counted, and an automation or a dashboard can still use them.
* The hub already reports the fact (`installable`); the integration does not work it out from the lists
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md), state as a fact).
* An older hub keeps working with the new integration.

## Considered Options

1. Leave it: on while `ready` is above zero, with or without an install button.
2. On only while the install button would take something; a new sensor "Outdated images" per host counts every
   outdated image, with `ready`, `installable` and the list.
3. As option 2, without the sensor: the images that stay manual are only in the attributes of the entity, which is
   off.
4. Offer the install button for newer tags as well, so that every candidate can be installed.

## Decision Outcome

Chosen: option 2.

* **Home Assistant:**
  * `update.vordr_<id>_containers` is on only while its install button is offered and `ready` is above zero. The
    button is offered as before (ADR-0119): the host allows Compose services, and the hub counts something the button
    would take (`installable` above zero). Otherwise `latest_version = installed_version`, also when images have a
    candidate.
  * The release summary and the attributes stay as they are, also while the entity is off.
  * New sensor `sensor.vordr_<id>_outdated_images` "Outdated images", for every host that reports its images: the state
    is `outdated`, unknown while the images were never compared with their registries; attributes `ready`,
    `installable` (where the hub sends it), `compared_at`, `min_age_days`, and `updates`, which stays out of the
    recorder ([ADR-0101](0101-package-lists-stay-out-of-the-recorder.md)). It is the images' counterpart of the sensor
    "Outdated packages".
* **Hub and agent:** unchanged; `installable` exists since ADR-0119.
* **Older parts:** a hub that sends no `installable` leaves the decision to the allowed services, as it already does
  for the button: with allowed services the entity turns on with a candidate as before, without them it stays off. An
  older integration with this hub behaves as before.

Rejected:

* **Option 1,** because Home Assistant shows such an entity as an update that cannot be installed, for as long as the
  manual step is not done; that is the noise ADR-0106 removed elsewhere.
* **Option 3,** because the attributes of an entity that is off are where nobody looks, and a count is what a
  dashboard or an automation needs; the pip/npm packages have their sensor for the same reason.
* **Option 4,** because a newer tag means editing the Compose file, which ADR-0119 leaves to the operator: a new
  version can change configuration, volumes or migrations, and a rollback cannot always undo that.

### Consequences

* Good, because every update Home Assistant lists for the images is one its button can install.
* Good, because the images to move by hand get a count of their own, for dashboards and automations.
* Bad, because a newer tag no longer appears among Home Assistant's updates; whoever relied on that watches the
  sensor instead.
* Bad, because the entity of a host that only reports its images is never on; a dashboard shows that host's images
  through the sensor.

### Confirmation

* `ha/tests/test_containers.py`:
  * `test_container_images_entity`: without allowed services the entity stays off, also with a candidate, and the
    sensor counts the images; no sensor for a host that reports none.
  * `test_container_images_by_button`: off with `installable` 0, on with 1; with a hub that sends no `installable`, on
    with allowed services and off without them.
  * `test_container_images_with_vulnerabilities`: a rebuild old enough of a service the host does not allow leaves the
    entity off; once the service is allowed, it is on.
  * `test_container_images_without_registry_questions`: the sensor is unknown, not zero, while nothing was compared.
