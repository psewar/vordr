---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, asked to build it, "113 und 114 umsetzen"); proposed by Claude (work kay asked for on 2026-11-04)
---

# Report outdated container images: same tag rebuilt, or a newer tag of the same form

Works out the first step of [ADR-0081](0081-container-images-own-source.md) (own source for container images, first
only reporting) for the part that needs no OSV data: which images are outdated.

## Context and Problem Statement

Hosts with Docker run services whose images Vördr does not see. apt updates the host, not the packages inside an
image. Two kinds of news matter:

* **The same tag, rebuilt.** A tag like `postgres:16` or `nginx:stable` moves to a new image, usually with a patched
  base. For security this is the common case.
* **A newer release.** A service pinned to `1.4.2` has `1.4.3` or `1.5.0` out.

Neither shows anywhere today. ADR-0081 decided on Vördr's own source and on reporting first. It left open how
"outdated" is decided, where the data comes from, and what the host may ask on the network.

## Decision Drivers

* Read only what the container engine itself records; start no CLI and parse no text
  ([ADR-0066](0066-inventory-from-metadata.md), [ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* The same minimum age as for pip and npm ([ADR-0068](0068-minimum-age-for-target-versions.md)): a release younger
  than `min_age_days` is not offered. It also explains a delay.
* Nothing new on the network without the operator's consent. Asking registries is new traffic from each host.
* No credentials in the agent for this.
* Reporting must not suggest that Vördr would install the update ([ADR-0106](0106-update-entity-on-only-when-vordr-would-install.md)).

## Considered Options

1. **Engine API and registry:**
   * The agent reads the running containers and their images through the local Docker Engine API socket, `GET` only.
   * It asks each image's registry, anonymously, for the current digest of the same tag and for its tag list.
2. **The `docker` CLI** (`docker ps`, `docker image inspect`) and parsing its output.
3. **A third-party scanner** (Watchtower in monitor mode, Diun, updates2mqtt), reporting to the hub.
4. **The hub asks the registries** for all hosts.

## Decision Outcome

Chosen: option 1, report only.

* **Inventory:** the running containers, each with its image reference (registry, repository, tag), the digest it was
  pulled by (`RepoDigests`) and the image's creation time. Stopped containers are left out. An image without a repo
  digest (built locally) is reported as such and never compared.
* **Outdated: the same tag rebuilt.** The registry's current manifest digest for the tag (a `HEAD` request, which
  registries do not count as a pull) differs from the local repo digest, and the new image is at least
  `min_age_days` old (its configuration's `created`).
* **Outdated: a newer tag of the same form.**
  * The tags of the repository that have the form of the running tag: the same number of numeric parts and the same
    suffix, so `1.4.2-cuda` only ever compares with `x.y.z-cuda`.
  * Pre-releases do not count. `latest` and other tags without a number have no newer form and are only checked for
    a rebuild.
  * The newest of them that is at least `min_age_days` old is the candidate, and a change of the first number is
    marked as a major version.
* **Registry access:**
  * The anonymous token flow the registry announces (`WWW-Authenticate: Bearer`), for Docker Hub, GHCR, Quay and any
    registry that follows the distribution specification.
  * No credentials. A private image counts as "not in the registry", like a private npm package.
  * A bound on lookups per check, as for pip and npm.
* **Policy:** `packages.containers: true` in agent.yaml turns it on per host. The default is **off**, because it is
  new network traffic from the host.
* **Hub and Home Assistant:**
  * A new location kind `container` with its own ecosystem (`oci`). The hub keeps it like any other inventory, with no
    OSV matching yet.
  * Home Assistant shows one update entity per host, "Container images", with the list and no install. The entity is
    on while a candidate exists, and its summary says that Vördr only reports.
* **Not part of this decision:**
  * Updating an image by button. That needs a policy per Compose project, a health check and a way back, and gets
    its own ADR.
  * Vulnerabilities inside images (ADR-0081, OSV for Debian/Alpine/Ubuntu).

Decided while building it (2026-11-05), where the proposal left the point open:

* **The hub announces it.** The agent sends the location only to a hub that announces the feature `containers`
  ([ADR-0067](0067-hub-announces-features.md)). An older hub would keep the images among the pip/npm packages: their
  outdated count would include them, and with no OSV data for `oci` the counts of findings would turn unknown.
* **Shown apart.** The hub leaves the location out of the package view and shows it as `containers` in the host
  view. Outdated images are no problem of the host.
* **Same switches as pip and npm, a schedule of its own.** The registries are asked at most every `outdated_hours`,
  and only with `packages.outdated`. `packages.containers` needs `packages.inventory` and Linux; `check-config`
  refuses it otherwise, and the location id `containers` is reserved for it. A check that could not read the engine,
  or whose hub did not keep images yet, compares nothing and does not hold the next comparison back. The location
  says when its images were compared (`compared_at`); until then the hub reports outdated images as unknown, not none.
* **What a rebuild is.** A different digest of the tag is not enough: an index also changes when only another platform
  or an attestation is added. A rebuild is another configuration for the host's platform, whatever its creation time:
  reproducible builds (distroless, ko, Bazel, Nix) keep a fixed one across rebuilds. The running image's configuration
  is its image id with the engine's classic store. With the containerd store the id is the pulled digest, so the
  configuration behind it is read from the registry; when the registry no longer has that digest, the tag counts as
  rebuilt. A reference that pins a digest is not checked for a rebuild.
* **Same form, refined.** A number of six or more digits is a date or a build number (`20250108`), not a version, and
  compares only with another such number: `alpine:3` does not find the date tag `20250108` newer.
* **Age.** A creation time before 2013 (reproducible builds write 1970) means the age is unknown: the image is
  reported, rebuilt or with a newer tag, but never a candidate.
* **Answers.**
  * 404, and 401 or 403 to a fresh anonymous token, count as not in the registry. A 401 to a token from an earlier
    request gets one fresh token first: Docker Hub's expire after five minutes.
  * A 404 on the running tag (deleted by retention) still reads the tag list. Only when that is closed too, or the
    tag has no form, does the image count as not in the registry.
  * 429, server errors and network faults count as failed.
  * Only a token realm over `https` is followed, and a tag list's next page only on the same host.
* **Downloads that count as pulls.** `HEAD` requests do not count against Docker Hub's anonymous pull limit, but
  reading a changed image's configuration takes manifest downloads, and those do. The limit is shared with the host's
  own pulls, often with every host behind the same public address. Two things keep them few:
  * The agent keeps the configuration and creation time it read per digest and platform in its state directory
    (`image-cache.json`, up to 4096 entries, the least recently used dropped). A digest never changes its content, so
    neither later runs nor a restarted agent download the same manifests again.
  * A run downloads at most 20 manifests per registry. What is left counts as failed and comes in the next run.
* **Bounds.** 300 requests per run, tokens included. A tag list of more than 20 pages of 1000 (at most 1 MiB each)
  counts as no answer, and only valid tags are kept, at most 20000 per repository. The run keeps at most 50000 tags
  for further images of the same repository. At most 5 newer tags are read per image, newest first, stopping at the
  first one that is old enough.
* **Engine.** The Docker Engine socket at its default path. Another engine with the same API (Podman's compatible
  socket) would plug in there. A container that disappears between the list and its inspection is left out.

Rejected:

* **Option 2,** because ADR-0066 reads metadata instead of tool output, and the CLI's format is text.
* **Option 3,** because it brings another service with its own updates and access to the socket, and its own view.
  ADR-0081 already rejected updates2mqtt for the same reasons.
* **Option 4,** because the hub would need to know every host's images and the registries' answers per platform. The
  host knows its own platform and already pulls from these registries.

### Consequences

* Good, because rebuilt base images, which mostly carry security fixes, become visible in Home Assistant.
* Good, because the minimum age and the major-version mark work as for pip and npm.
* Bad, because each host with it turned on asks registries once per check interval, about one request per image plus
  the tag lists. `HEAD` requests do not count against Docker Hub's anonymous pull limit, but reading a changed image
  takes manifest downloads, which do. They share the limit with the host's own pulls and with other hosts behind the
  same address. The cache on disk and the bound per run keep them few, but they are not zero.
* Bad, because the tag-form rule cannot know every scheme. Tags like `2026.10.1` or `v1.2` follow it, but a project
  that encodes the base in the tag (`1.2-bookworm` vs `1.2-trixie`) is compared only within the same suffix.
* Bad, because it is a protocol change: a new location kind. The hub must be updated first, then agents, then the
  integration.

### Confirmation

* `internal/images`:
  * `TestOutdatedAgainstAFakeRegistry`, against a registry that wants an anonymous token for every repository. It
    covers:
    * a rebuilt tag, and newer tags of the same form with the minimum age (candidate and latest);
    * a tag list over several pages, and a multi-platform index read for the host's platform;
    * an index change without a new image, with the classic and with the containerd image store;
    * a reproducible rebuild with a fixed creation time, and a running tag deleted by retention;
    * a private and a rate-limited repository, and a locally built image that is never asked about;
    * `HEAD` for unchanged tags, and one token per repository.
  * `TestManifestDownloadsAreKeptAndBounded`: a restarted agent downloads no manifest again, and a run stays within its
    manifest downloads.
  * `TestExpiredTokenIsRenewed`, `TestTagListsAreBounded` (page size, tags per repository, tags held per run).
  * `TestOutdatedBudget`, `TestParseChallenge`, `TestNextPageStaysOnTheHost`, `TestParseRef`, `TestNewerTags` (date
    tags included).
  * `TestEngineRunning` and `TestEngineStates` against a fake Engine API socket that refuses everything but `GET`,
    with a container that disappears between the list and its inspection.
* `internal/policy`: `TestContainersOffByDefault`, `TestContainersRefused` (Windows, inventory off, id taken).
* `internal/agent`:
  * `TestInventoryWithContainers`: only to a hub that keeps images, and off by default.
  * `TestImagesComparedOnTheirOwnSchedule`: an engine that does not answer delays nothing.
  * `TestInventoryToAHubThatAcceptsIt`: a hub with packages announces the feature.
* `internal/hub`: `TestContainersView` (apart from the package counts, unknown until the images were compared) and
  `TestContainersInHostView`.
* `ha/tests/test_containers.py`: the entity without install, on only with a candidate, the summary, and the list out
  of the recorder; `test_texts.py` for the texts in both languages.
* After the rollout: a live check on a host with a rebuilt tag.
