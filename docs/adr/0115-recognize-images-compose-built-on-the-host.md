---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, merged the implementation); proposed by Claude after turning on ADR-0113
---

# Recognize images Compose built on the host

## Context and Problem Statement

[ADR-0113](0113-report-outdated-container-images.md) reports outdated container images and never compares an image
built on the host: "An image without a repo digest (built locally) is reported as such and never compared."

That rule holds for Docker's classic image store, where only a pull or a push gives an image a repo digest. With the
containerd image store, the default of new Docker installations, every image has one. Its own index digest is
listed as `<name>@sha256:...`, also for an image built on the host.

On a host with the containerd store, services that Compose builds there have images with a repo digest. The agent
would take them as pulled from Docker Hub.

* **Its name goes out.** The agent asks Docker Hub anonymously for `library/<name>`, the name of the host's own build.
* **Wrongly classified.** Docker Hub answers "not found", so the image counts as "not in the registry" (like a private
  one) instead of "built locally".
* **Compared with somebody else's image.** A local build named like a public repository (`org/app`) would be compared
  with that repository. Its digest never matches, so the agent would report it as rebuilt for ever, and newer tags of
  a stranger's image as candidates.

## Decision Drivers

* No name of a local build leaves the host; nothing is compared with an image of another origin.
* Read the engine only, `GET` on its socket as before (ADR-0113). No files of the host, no CLI.
* When the signal is wrong, it errs on the safe side: an image left uncompared, never a false report.
* No new setting the operator has to keep up to date.

## Considered Options

1. **The labels Compose writes into an image it builds.** Compose puts `com.docker.compose.project`, `.service` and
   `.version` into the configuration of every image it builds. The engine returns them with the image
   (`Config.Labels`).
2. **The Compose file behind the container.** The container's labels name its Compose files
   (`com.docker.compose.project.config_files`) and service; the agent reads the files and looks for `build:`.
3. **A list in agent.yaml** (`packages.containers_local`), which the operator keeps.
4. **The image's manifests:** a pulled multi-platform image has platforms that were not pulled (`Available: false`),
   a local build has all of them.
5. **Ask the registry whether it knows the digest**, and count an unknown one as local.
6. **Leave it as it is.**

## Decision Outcome

Chosen: option 1. An image counts as built locally when it has no repo digest (classic store) or its configuration
has the label `com.docker.compose.project`. Such an image is reported, never compared, and no registry is asked about
it.

* On a host with both kinds, the label marks exactly the services whose Compose file has `build:`, and none of the
  containers that run pulled images.
* A pulled image has the label only if its publisher built it with Compose and pushed it. It is then left
  uncompared: nothing is reported for it, nothing is sent.
* Options 2 to 6, briefly:
  * Option 2 reads files the labels point to, needs Compose's merge rules (several files, overrides, `include`,
    `extends`) and finds nothing a plain `docker build` made either.
  * Option 3 needs upkeep. A new local build leaks until somebody adds it.
  * Option 4 fails for single-platform images, which are common among small projects.
  * Option 5 sends the name out first, which is what this decision avoids. A registry may also delete old digests, and
    the image would then turn "local".
  * Option 6 keeps all three faults.

### Consequences

* Good: local Compose builds stay on the host and are shown as built locally, without any setting.
* Good: no new access. The labels come with the image the agent already reads.
* Bad: an image built with plain `docker build` under the containerd store is still taken as pulled. It has no
  Compose labels. If that shows up, option 3 can be added for it, as an explicit list.
* Bad: a public image built with Compose is never compared. That is rare, and it is the safe way round.

### Confirmation

* `internal/images/engine_unix_test.go` (`TestEngineRunning`): an image with a repo digest and the Compose labels is
  `BuiltLocally` and has no digest in the inventory. A pulled image with other labels is not.
* `internal/images/registry_test.go` (`TestLocalBuildWithARepoDigestIsNeverAsked`): such an image causes no request
  and no token, even when a public repository of its name was rebuilt and has a newer tag.
* Mutation probes: dropping the label check, ignoring `BuiltLocally` in `RepoDigest`, not reading `Config.Labels` or
  not setting `BuiltLocally` each turn a named test red.
