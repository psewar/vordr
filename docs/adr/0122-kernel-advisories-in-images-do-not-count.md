---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Yes, build it", after the first matched image showed thousands of kernel advisories); proposed by Claude
---

# The kernel's advisories in images do not count

Refines [ADR-0120](0120-known-vulnerabilities-inside-container-images.md) (known vulnerabilities inside container
images) in one point: which packages of an image count. Everything else there stays.

## Context and Problem Statement

The hub matches the packages inside each image against the OSV dump of the image's distribution release, by source
package (ADR-0120). A Debian image with build tools shows thousands of advisories, and one of its source packages
carries almost all of them: `linux-libc-dev`, source package `linux`, which OSV matches with thousands of advisories on
its own. The kernel gets several hundred CVEs a month.

A container never runs a kernel from its image. It runs on the host's kernel, which the host's own package updates
cover. `linux-libc-dev` only holds the kernel's header files for compiling userland programs, and images that carry a
kernel package do not boot it. The kernel's advisories therefore do not apply to the image, and they bury the few
hundred that do. Every image with a compiler toolchain carries the headers.

Image scanners draw the same line: Grype, for one, leaves out matches of kernel header packages through the kernel's
source package unless asked for them.

## Decision Drivers

* An image's findings should be those that a newer image could fix and that apply to what the container runs.
* Nothing is dropped silently: what is left out is shown with its count.
* The rule must not catch userland packages that only have `linux` in their name (`util-linux`, `linux-pam`).
* The kernel of the host stays covered where it is installed: by the host's system packages.

## Considered Options

1. Leave it: count the kernel's advisories like any other.
2. Leave out the kernel's source packages in images, and show their count per image apart.
3. Leave them out without a trace.
4. Count only advisories Debian or Alpine rate above a given urgency.

## Decision Outcome

Chosen: option 2.

* **Which packages:** the kernel's source packages as the OSV dumps name them:
  * Debian: `linux` and its versioned sources `linux-<version>` (`linux-6.12`, `linux-5.10`, `linux-2.6.24`): digits
    separated by dots, at least two parts. Binary packages such as `linux-libc-dev`, `linux-headers-*` and
    `linux-image-*` name one of them as their source.
  * Alpine: `linux-lts` and `linux-grsec`, the only kernel aports with entries.
  * Not the kernel: `util-linux`, `linux-pam`, `linux-base`, `linux-atm`, `linux-latest-*` and any other name.
* **Matching:** these packages are looked up as before, but their advisories go to a count of their own, `kernel`, per
  image, fixed or not; advisories the distribution rates "unimportant" count nowhere, as before. A CVE that another
  package of the same image also has counts there, as usual.
* **View:** `kernel` per image in `vulnerable`. An image with only kernel advisories is listed with them, and counts
  neither as fixable nor as unfixed. Home Assistant shows `kernel` in the attribute; the summary and the entity's state
  leave it out.
* **Unchanged:** the host's own system packages, and pip and npm packages, are not affected.

Rejected:

* **Option 1,** because thousands of advisories that cannot apply hide the ones that do, and a newer image cannot
  change the host's kernel.
* **Option 3,** because a filter nobody can see makes the counts hard to trust and a wrong rule hard to notice.
* **Option 4,** because urgency is not the question: a kernel advisory rated high still does not apply inside a
  container, and a userland one rated low still does.

### Consequences

* Good, because the counts of an image are about what its containers run: an image with build tools falls from
  thousands of findings to a few hundred.
* Good, because the kernel's count stays visible per image.
* Bad, because a new kernel package name in a dump would be counted again until the rule names it. The list is short
  and comes from the dumps; a test pins it.
* Neutral: a container started with access to the host's kernel sources or modules is still matched by the host's own
  packages, not by the image.

### Confirmation

* `internal/hub`:
  * `TestKernelFindingsAreLeftOut`: in a Debian and an Alpine image, the kernel's advisories go to `kernel` (fixed,
    unfixed, versioned source), "unimportant" ones nowhere; a CVE another package has too counts there; `util-linux`,
    `linux-base` and `linux-pam` count as before; an image with only kernel advisories is listed and counts neither as
    fixable nor as unfixed.
  * `TestKernelSource`: the names that are the kernel and those that only look like it.
* `ha/tests`: `test_container_images_kernel_advisories_apart`: `kernel` in the attribute, not in the summary, the entity
  stays off.
* A live check: an image with build tools shows its kernel advisories as `kernel`, and the others apart.
