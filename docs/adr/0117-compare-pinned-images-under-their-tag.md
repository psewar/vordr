---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, asked to build it); proposed by Claude after turning on ADR-0113
---

# Compare pinned images under their tag

## Context and Problem Statement

[ADR-0113](0113-report-outdated-container-images.md) reports container images whose tag was rebuilt, or that have a
newer tag of the same form. A container started from a reference that pins a digest (`app@sha256:…`) has no tag
in its reference. ADR-0113 therefore never compared it: "A reference that pins a digest is not checked for a rebuild",
and without a tag there is no form either.

Pinning is a common, deliberate practice for important services, because it keeps the container on a known build;
often every image of such a service is pinned (application, database, cache, proxy). These are exactly the services
where a missed security build hurts most. Until now the agent only lists them.

The engine still knows the tag such an image was pulled under: `RepoTags` of the image, e.g. `app:stable` for
`app@sha256:…`.

## Decision Drivers

* Pinned images of important services are reported like any other.
* The pin is respected: Vördr only reports (ADR-0113). What it reports is that the tag has moved on, and the operator
  decides whether to move the pin.
* No new access: `RepoTags` comes with the image the agent already reads. No protocol change.
* No guess: only a tag the running image itself carries counts.

## Considered Options

1. **The image's own tag on the host** (`RepoTags`, same repository).
2. **The tag from the Compose file** behind the container (`image: app:stable@sha256:…`). This reads files, needs
   Compose's merge rules, and finds nothing for containers started without Compose.
3. **A tag per image in agent.yaml.** Needs upkeep.
4. **Leave pinned images unchecked.**

## Decision Outcome

Chosen: option 1.

* **Which tag:**
  * A reference with a tag (`app:2.3.0@sha256:…`) is compared under that tag.
  * A reference that pins only a digest is compared under the tag the running image carries on the host for the same
    repository. When there are several, a tag with a version form wins over a word (`v3.1` over `latest`, so newer
    tags of its form can be found), then the first in alphabetical order.
  * A tag the host has since moved to another image is no longer the running image's tag, so it does not count.
  * Without such a tag the image is listed, never asked about, as before.
* **What is compared:** the same as for an unpinned image.
  * Has the tag moved on? This is the platform-exact rebuild check. For a pin it means the pin is behind its tag.
  * Are there newer tags of the same form?
* **References with tag and digest** are now checked for a rebuild too. ADR-0113 skipped that check for every pinned
  reference.
* **The inventory entry:**
  * `version` is the tag. The digest stays in `digest`, and the hub shows the entry under its tag (until now a pin
    showed its digest there).
  * Two references to one image under one tag (`postgres:16` and `postgres@sha256:…` of the same build) are one entry,
    with the containers of both.

### Consequences

* Good: pinned images of important services are compared and reported.
* Good: no new access, no new key, no protocol change.
* Bad: "rebuilt" now also means "the pin is behind its tag". The entry shows the tag and the pinned digest, so both
  meanings read the same: a newer build exists.
* Bad: a pin whose image lost its tag on the host stays unchecked. That happens when the host pulled the tag again for
  another image while the container still runs the pinned one.
* Neutral: entries of pinned images change their `version` once, from the digest to the tag. The agent's kept answers
  miss them once, and they are compared at the next run.

### Confirmation

* `internal/images/pinned_test.go`:
  * `TestPinnedReferenceIsComparedUnderItsTag`:
    * a pin with a tag on the host is compared: the tag moved on, a newer tag exists;
    * a pin without a tag is listed, with no request;
    * a reference with tag and digest is checked for a rebuild.
  * `TestRunningTag`: the reference's own tag first; then the image's tags of the same repository, a version form
    before a word, then alphabetical order; other repositories' tags do not count.
  * `TestLocationMergesReferencesToOneImage`: one entry with the containers of both references.
* Mutation probes. Each of these turns a named test red:
  * `RepoTags` ignored;
  * the pin not compared;
  * another repository's tag accepted;
  * entries not merged;
  * `version` left as the digest;
  * the tags not sorted.
