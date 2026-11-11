---
status: accepted
date: 2026-11-07
decision-makers: kay (2026-11-07, "alles", after the entity stood on for images that had nothing to install); proposed by Claude
---

# The image entity needs attention only when a newer image exists

Refines [ADR-0120](0120-known-vulnerabilities-inside-container-images.md) (known vulnerabilities inside container
images) in one point: what turns the entity "Container images" on. Its operator decision 3 made an advisory that a
newer package version fixes ("a newer image fixes it") mark the entity as needing attention. This ADR replaces that
point with the rule of [ADR-0106](0106-update-entity-on-only-when-vordr-would-install.md): the entity is on only when
there is something to install. Everything else in ADR-0120 stays.

## Context and Problem Statement

ADR-0120 counts an image's advisories in two groups: "fixable" when the distribution has published a fixed package,
"no fix yet" otherwise. It assumed that a fixed package means a newer image is there to pull. That holds only after the
image's publisher has rebuilt it. Until then the running image is the newest one its tag has, and no install, by button
or by hand, can change it.

An image tagged `latest` with fixable advisories and no rebuild keeps its entity on "update available" for days, with
no install button and no candidate; so does an image with fixable advisories whose newest tag is the running one. That
is the noise ADR-0106 removed from the other update entities: a badge that is lit without
anything to do teaches the operator to stop looking.

## Decision Drivers

* "On" means the same on every update entity: there is something to install (ADR-0106).
* Nothing gets lost: the counts and advisory IDs stay visible.
* The view says as a fact whether a newer image exists, instead of the integration working it out from two lists.
* An older hub keeps working with the new integration, and the other way round.

## Considered Options

1. Leave it: fixable advisories turn the entity on.
2. The entity is on only while an image has a candidate (a rebuild or a newer tag at least `min_age_days` old). The hub
   says per image whether a candidate exists (`newer`) and counts the images with fixable advisories and a candidate
   (`fixable_newer`); the summary words each image accordingly.
3. The same, but fixable advisories raise a problem of the host instead.

## Decision Outcome

Chosen: option 2.

* **Hub:** per image in `vulnerable`, `newer` when the image has a candidate. In the view, `fixable_newer`: of the images
  counted in `fixable`, those with a candidate. It is nil while `fixable` is, and while the images were never compared
  with their registries; whether a newer image exists is unknown then, not "none". Images with a fixable advisory and a
  candidate come first in the list.
* **Home Assistant:**
  * The entity "Container images" is on only while `ready` is above zero. Without a candidate it shows
    `latest_version = installed_version`, also with fixable advisories.
  * With a candidate, `latest_version` names the outdated and ready images, and how many of the images with fixable
    advisories a newer image is there for ("1 fixable by a newer image").
  * The summary words each image's fixable count: "2 fixable, newer image ready" or "2 fixed in the distribution, no
    newer image yet". Advisories without a fix are listed as before.
  * The attributes keep `fixable`, `unfixed` and the advisory IDs, and add `fixable_newer` and `newer`.
* **Older parts:** a hub before this change sends no `fixable_newer`. The new integration then still turns the entity on
  only with a candidate and words the counts as before. An older integration with the new hub ignores the new fields
  and behaves as before.
* Unchanged: findings stay no problem of the host (ADR-0120, decision 3); the counting rules of ADR-0120 and
  [ADR-0122](0122-kernel-advisories-in-images-do-not-count.md).

Rejected:

* **Option 1,** because an entity that stays on while nothing can be installed is the noise ADR-0106 removed elsewhere,
  and it hides the images where an install would help.
* **Option 3,** because ADR-0120 already rejected findings as a problem of the host: base images almost always carry
  advisories, and hosts would stay red for good.

### Consequences

* Good, because the update badge counts an image only when there is something to pull; for images like those above,
  the entities go off at once.
* Good, because the summary separates "install now" from "wait for the publisher", which is what the operator decides
  on.
* Bad, because an image whose publisher never rebuilds stays quietly vulnerable; the summary and the attributes still
  show it, but the badge does not. Replacing such an image is the operator's decision.
* Neutral: `newer` says that a newer image exists, not that it contains the fixed package. That is known only after
  the pull, when the agent reads the new image's packages.

### Confirmation

* `internal/hub`: `TestImageFindingsSayWhetherANewerImageIsThere`: only an image with a candidate counts in
  `fixable_newer` and has `newer`; a newer tag that is too new does not; it comes first; never compared gives nil.
* `ha/tests`: `test_container_images_with_vulnerabilities`: fixable advisories without a candidate leave the entity off
  with an older hub and with the new one; the summary words both cases; on with a candidate, with `fixable_newer` in
  `latest_version`.
