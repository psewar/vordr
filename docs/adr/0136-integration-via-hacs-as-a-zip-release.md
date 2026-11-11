---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# The integration ships through HACS as a zip release of the public repository

Supersedes [ADR-0025](0025-distribution-via-deploy-tool.md).

## Context and Problem Statement

ADR-0025 shipped the integration with the operator's own deploy tool because the repository was private, and named
HACS as the way once it becomes public. With publication, other operators need a way to install and update the
integration that Home Assistant shows as an update.

HACS expects the integration in `custom_components/<domain>/` at the root of the repository, or a zip file attached to
each GitHub release. Here the integration lives in `ha/custom_components/vordr/`, next to its tests, apart from the Go
code.

## Decision Drivers

* Home Assistant shows new versions of the integration and installs them.
* The repository layout stays: tests, tools and ADRs refer to `ha/`.
* A release ships exactly the integration's files, with the version the manifest names.

## Considered Options

* move the integration to `custom_components/vordr/` at the root
* a zip release: each GitHub release carries `vordr.zip` with the integration's files

## Decision Outcome

Chosen: a zip release.

* `hacs.json` in the root names the integration, `zip_release: true` and `filename: vordr.zip`.
* A GitHub release is tagged `vX.Y.Z` with the integration's version from `manifest.json`. The release workflow
  (`.github/workflows/release.yml` in the published tree, kept in `tools/github-actions/` here as
  [ADR-0137](0137-checks-run-in-github-actions.md) explains) refuses a tag that differs from the manifest, packs the
  content of `ha/custom_components/vordr/` into `vordr.zip` and attaches it.
* `manifest.json` names the issue tracker and the code owner, as HACS and Home Assistant's checks expect.
* Agent and hub binaries are not part of a release: every operator signs agent releases with their own key
  ([ADR-0139](0139-the-release-signer-is-part-of-the-repository.md)).
* Copying `ha/custom_components/vordr/` into the configuration by hand stays possible, as does a deploy tool of one's
  own.

Rejected:

* **Move the integration to the root,** because it would separate it from its tests and change paths in tools and
  documents, for no gain over a zip.

### Consequences

* Good, because HACS installs and updates the integration, and Home Assistant shows the update.
* Good, because a release cannot ship a version that differs from the manifest.
* Bad, because release tags follow the integration's version, while agent and hub count on their own.

### Confirmation

The release workflow fails on a tag that differs from `manifest.json`. A release carries `vordr.zip` with
`manifest.json` at its top level.
