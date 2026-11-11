---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, took the recommendation on all three open questions); proposed by Claude, the second half of ADR-0081's report-only source
---

# Known vulnerabilities inside container images

## Context and Problem Statement

[ADR-0081](0081-container-images-own-source.md) decided that the container source reports "images with a newer
version and known vulnerabilities in the packages of an image (OSV, like ADR-0064)".
[ADR-0113](0113-report-outdated-container-images.md) built the first half. An image can be the newest of its tag and
still carry a vulnerable OpenSSL from its base image, until its publisher rebuilds it.

The hub already matches pip and npm packages against OSV data it keeps locally
([ADR-0064](0064-osv-data-local-at-the-hub.md)). Images need two more things:

* the operating system packages inside each running image;
* OSV data for those distributions, and version comparison by their rules (dpkg, apk).

## Decision Drivers

* Read only, and no new kind of access: `GET` on the engine, as for the inventory (ADR-0113); no `exec`, no pull.
* The same matching at the hub as for pip and npm. The agent sends names and versions, never file contents.
* Bounded: package lists and OSV data sized for small hosts and a small hub.
* Report only, like ADR-0113.

## Considered Options

How the agent learns the packages inside a running image:

1. **The container's package database through the engine:** `GET /containers/{id}/archive?path=…` returns a single
   file as a tar. That is `/var/lib/dpkg/status` for Debian and Ubuntu, `/lib/apk/db/installed` for Alpine. It is read
   only, needs no `exec`, and reads the container as it runs, including packages installed after the image was built.
2. **The image's SBOM attestation from the registry:** many official images publish one (Docker Hub's attestation
   manifests). It needs no access to the container, but it misses images without attestation and packages installed at
   runtime.
3. **`docker exec` with `dpkg-query` or `apk info`:** an `exec` is a `POST` that runs code in the container.
   Rejected.
4. **A scanner such as Trivy or Grype** on each host: a large third-party binary, its own vulnerability database per
   host, and output to parse.

How the hub matches them:

* **A.** OSV data for `Debian`, `Ubuntu` and `Alpine`, downloaded and kept like PyPI's and npm's (ADR-0064), with
  version comparison by dpkg and apk rules in `internal/versions`.
* **B.** Only Debian security tracker data. That covers Debian-based images only.

## Decision Outcome

Chosen: option 1 with A.

* **Agent:**
  * per running image, one container's package database through the archive endpoint;
  * the distribution and its version from that container's `/etc/os-release`, read the same way;
  * name, version and source package per package, into the image's inventory entry.
  * Bounded: at most one read per image per comparison (on the `outdated_hours` schedule), and at most a few thousand
    packages per image.
* **Hub:**
  * OSV ecosystems `Debian:<n>`, `Ubuntu:<n>` and `Alpine:v<n>`, only those the inventories name;
  * matched on the source package for Debian and Ubuntu, as their advisories are written;
  * the counts per image (vulnerable, and fixed in a newer version) in the container entity, with the advisory IDs.
* **Unknown stays unknown:** an image without a readable package database (distroless, scratch, a shell-less image)
  reports its packages as unknown, not as zero.

### Decided by the operator (2026-11-05)

1. **OSV data at the hub: Debian and Alpine first.**
   * These are the ecosystems the operator's images use.
   * Ubuntu is added when an inventory names an Ubuntu-based image; the hub downloads only ecosystems some inventory
     names.
   * Before the first download the hub checks its free disk space for the ecosystem and refuses with a state of its
     own when it is short.
   * Matching reads one ecosystem at a time, so memory stays bounded on a small hub.
2. **The two files inside the running container** (option 1), not the registry's SBOM attestation. Coverage is far
   better: every image with a package database, and packages installed after the build. It costs no requests that
   registries count as pulls. And it reads package metadata, not the services' data.
3. **Findings only on the container entity, not as a problem of the host,** as for outdated images (ADR-0113).
   * The entity counts findings in two groups: "a newer image fixes it" and "no fix yet", with the advisory IDs.
   * "A newer image fixes it" is what marks the entity as needing attention, because it is what the operator can act
     on.
   * Base images almost always carry advisories without a fix. As a problem of the host they would keep many hosts red
     for good.

Built before [ADR-0119](0119-update-container-images-by-button.md): it only reads and needs no new rights.

Decided while building it (2026-11-05), from the real dumps:

* **Turned on by the hub's configuration:** `Debian` and/or `Alpine` in `osv.ecosystems`. The default stays PyPI and
  npm, so an existing hub downloads nothing new. With one of them the hub announces the feature `image_contents`, and
  only then do agents read inside their images.
* **Indexes on disk, one in memory at a time.**
  * Measured: a Debian release's index (about 57,000 advisories) takes about 60 MB in memory and under 1 MB on disk;
    Alpine's about 4 MB. Kept loaded like PyPI's and npm's, two Debian releases with Alpine would add about 180 MB, a
    large share of a small hub (for example 512 MB).
  * So the hub matches when an inventory arrives or new data comes in. It loads the release's index, matches its
    images, keeps the results and drops the index; one match runs at a time.
* **Findings count by CVE.** Debian publishes a CVE as DEBIAN-CVE and again in the DSA or DLA that fixed it, naming it
  in `upstream`. The hub counts CVEs: the id, its aliases and `upstream`. One CVE counts once, as fixable when any of
  its advisories names a fix; a DSA for several CVEs counts each; an advisory without a CVE counts under its own id.
* **"unimportant" does not count.** Debian rates a package's exposure per advisory (`urgency`). "unimportant" is its
  statement that the CVE is no security problem in its build. Counted, it would bury the real findings under "no fix
  yet". It holds for that package, whichever advisory names the CVE (a DSA does not bring it back). Alpine's data
  lists fixed CVEs only, so there all findings are fixable.
* **Source package and source version.** Debian and Alpine write their advisories for the source package. A binNMU's
  binary version (`+b1`) is not the source's, so the hub uses the source version where dpkg names one.
* **Version order:** dpkg's for Debian (`verrevcmp`, with its own reference cases), apk's for Alpine.
  * apk-tools 2 (Alpine up to 3.22) and 3 (from 3.23) order a few versions differently: numbers after a "." that both
    start with "0", e.g. `8.2.0015` and `8.2.002`.
  * Where the two disagree, the comparison gives no order. The advisory then counts as undecided, not as a finding,
    instead of choosing one generation's answer. The same holds when the advisory's own range bounds cannot be
    ordered against each other.
* **Each release stands on its own.** A dump is downloaded per distribution, but each release built from it has its
  own state:
  * A release the dump has no entries for (one just released) is a state, `no_entries`, not a failure; its images
    show `no_data`.
  * A release whose new data would shrink far below the old keeps its old index and shows `implausible_dump`
    (ADR-0085), while the other releases take the new dump. It is tried again after an hour; releases already built
    from that dump are not built again.
  * An index counts only for the dump (its ETag) and the index version it was built from. A release that was not
    needed while the dump changed, and is needed again, brings the dump at once instead of being matched with old
    data until the next refresh. An unreadable index is forgotten and built anew.
  * Building an index also waits for the match lock, so a build and a match never hold two indexes in memory.
* **Agent side:**
  * Without `packages.outdated`, images are not compared, but their contents are still read on the same schedule
    (`outdated_hours`) for a hub that asks.
  * An image started between two comparisons is read once and kept, not read again with every check.
  * Distroless images keep `status.d` files without a `Status` field; those count as installed.
  * When an inventory is too large for one message, the agent shortens by size, not by number of entries: first the
    package list of the image that carries the most (the image and its comparison stay, its code says
    `too_many_packages`, and the hub does not match it), then as before.
* **The data view lists only needed releases.** A release appears in the hub's data view only while some image runs
  it. Home Assistant's data time stays known on hubs without images.
* **Ubuntu is not built yet.**
  * OSV names its releases differently (`Ubuntu:24.04:LTS`, and Pro releases), and its dump is 807 MB against Debian's
    81 MB.
  * An Ubuntu-based image shows `unsupported_distribution`.
  * Adding Ubuntu, and whether the hub has room for it, is decided on its own.

### Consequences

* Good: the second half of ADR-0081, with the same matching and the same view as pip and npm.
* Good: no `exec`, no pull, no third-party scanner.
* Bad: more data at the hub, and two more version schemes to compare correctly.
* Bad: the reads look into running containers. They are reads of two named files, and the agent is root on the host
  anyway.

### Confirmation

With the implementation, at least:

* only `GET` to the engine (a fake engine records the methods);
* the dpkg and apk version comparisons, tested against their reference cases;
* source-package matching;
* an image without a package database reported as unknown;
* only ecosystems an inventory names are downloaded, and not when the disk is short;
* the two groups on the container entity, and no problem of the host.
