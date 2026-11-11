---
status: accepted
date: 2026-11-07
decision-makers: kay (2026-11-07, "alles", after images were offered only a new major version); proposed by Claude
---

# Name the newest tag of the running major version beside a new one

Refines [ADR-0113](0113-report-outdated-container-images.md) (report outdated container images) in what a comparison
reports about newer tags: beside a candidate that is a new major version, the newest tag of the running major version
that is old enough. Everything else there stays; the install button still takes only rebuilds
([ADR-0119](0119-update-container-images-by-button.md)).

## Context and Problem Statement

ADR-0113 reports, per image, the newest tag of the same form (`latest`) and the newest one at least `min_age_days`
old (`candidate`), and marks a change of the first number as a major version. When the newer major version is old
enough, it is the candidate, and nothing is said about the running major version.

For example, a database image on `2.3.0` is offered `3.1.0`, a new major version that needs a data migration; that
the running major version has moved on to `2.5.1` with fixes is not reported. A minor step is what an operator usually
takes first: no migration, no breaking changes, and most of the fixes. Finding it meant reading the registry by hand.

## Decision Drivers

* The comparison reports what the operator can act on, also the safer step.
* The same minimum age and the same bounds on requests as for the candidate (ADR-0113, ADR-0121).
* No new kind of action: a new tag still needs the Compose file changed, by the operator.
* Older hubs and integrations keep working.

## Considered Options

1. Leave it: only `latest` and `candidate`.
2. A second field `same_major`: when the candidate is a new major version, or `latest` is one and no tag was old
   enough, the newest tag of the running major version that is at least `min_age_days` old.
3. Make the candidate stay in the running major version, and report a new major version only as `latest`.

## Decision Outcome

Chosen: option 2.

* **Agent:** after the walk for the candidate, when it found a new major version (as `latest` with no candidate, or as
  the candidate), it walks the newer tags with the running first number, newest first, at most 5 as for the candidate,
  and takes the first one at least `min_age_days` old as `same_major`. A candidate within the running major version
  needs no second walk. A question without an answer in this walk counts like any other (ADR-0126). Between
  comparisons the value comes from the agent's kept answers.
* **Hub:** passes `same_major` on in the container view. An image with only `same_major` (the new major version too
  new) counts as ready: there is something old enough to install.
* **Home Assistant:** the summary names it beside the new major version, "app 2.3.0 → 3.1.0 (new major version,
  or 2.5.1 in the same major version)", and the attribute `updates` carries it.

Rejected:

* **Option 1,** because the safer step had to be found by hand, and the offered step was the riskier one.
* **Option 3,** because a new major version that is old enough is also worth knowing, and it hides the newest
  release behind the older one.

### Consequences

* Good, because the step without a migration is in view next to the one with it.
* Good, because a new major version that is still too new no longer hides an old enough step within the running one:
  the image counts as ready.
* Bad, because the second walk costs requests and manifest downloads for images with a new major version, at most 5
  tags each. The agent's cache keeps what it read per digest, so later runs ask again only for new tags.
* Neutral: a hub or integration before this change ignores the field.

### Confirmation

* `internal/images`: `TestSameMajorCandidate`: a candidate in a new major version with the step within the running one;
  the newest of both too new; a candidate already within the running major version (no second walk result); all five
  tags of the first walk too new; no old enough tag in the running major version; a later run clears it.
* `internal/agent`: `TestImagesComparedOnTheirOwnSchedule`: `same_major` comes from the kept answers.
* `internal/hub`: `TestSameMajorInContainersView`: passed on, and alone makes an image ready.
* `ha/tests`: `test_container_images_same_major`: the summary and the attribute.
