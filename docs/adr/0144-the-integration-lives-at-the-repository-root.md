---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, chose to move the folder to the root over a repository of its own; accepted with the merge); proposed by Claude
---

# The integration lives at the repository root, where HACS looks for it

Supersedes [ADR-0136](0136-integration-via-hacs-as-a-zip-release.md). Its zip release stays and is restated below;
what changes is where the integration lives.

## Context and Problem Statement

ADR-0136 kept the integration in `ha/custom_components/vordr/` and relied on a zip attached to each release, assuming
HACS would take the zip whatever the layout. It does not. HACS checks the repository tree at the release tag before it
downloads anything: an integration needs a directory under `custom_components/` at the root of the repository, and
HACS reads `manifest.json` from there. `zip_release` only changes the download. With the integration under `ha/`, HACS
refuses the repository as a custom repository ("Repository structure … is not compliant"), so the install path the
README describes does not work.

The HACS default list, which Home Assistant offers as its Marketplace, also asks for one integration per repository,
the HACS action and hassfest passing without ignored checks, full releases, a description, topics and issues.

## Decision Drivers

* HACS accepts the repository as a custom repository and could list it as a default.
* One repository and one snapshot flow: changes that touch hub codes and the integration's texts stay one pull request,
  and the end-to-end test keeps running the real hub and agent from the same build.
* A release ships exactly the integration's files, with the version the manifest names.

## Considered Options

* move the integration to `custom_components/vordr/` at the root of this repository
* a repository of its own for the integration
* `content_in_root: true` in `hacs.json`
* keep the layout and the zip release (ADR-0136)

## Decision Outcome

Chosen: the integration moves to `custom_components/vordr/` at the root of this repository.

* `custom_components/` holds only `vordr/` (and the package file the tests import through), so HACS finds exactly one
  integration.
* The tests and their pinned requirements stay in `ha/` (`ha/tests/`, `ha/requirements_test.txt`). `ruff.toml` and
  `pytest.ini` move to the root, so editors and both tools see the integration and its tests under one configuration;
  the checks run from the root.
* The zip release of ADR-0136 stays: `hacs.json` in the root says `zip_release: true` and `filename: vordr.zip`; a
  release is tagged `vX.Y.Z` with the integration's version; the release workflow refuses a tag that differs from
  `manifest.json` and attaches the content of `custom_components/vordr/` as `vordr.zip`. Agent and hub binaries are not
  part of a release ([ADR-0139](0139-the-release-signer-is-part-of-the-repository.md)). Copying the folder by hand
  stays possible.
* A GitHub workflow runs the HACS action (category integration, nothing ignored) and hassfest on every push and pull
  request and once a day.

Rejected:

* **A repository of its own,** because it adds a second public repository and snapshot flow, splits every change that
  adds a hub code and its texts into two pull requests, and moves the end-to-end test away from the binaries it runs.
  It would only pay off if the integration needed releases or issues apart from Vördr.
* **`content_in_root`,** because it expects `manifest.json` at the root of the repository, among the Go code.
* **Keep the layout,** because HACS refuses it.

### Consequences

* Good, because HACS installs and updates the integration from a custom repository, and the repository can be proposed
  for the default list.
* Good, because HACS's and Home Assistant's own checks run in CI and show when either changes its rules.
* Bad, because the integration's code and its tests sit in two folders.
* Bad, because release tags follow the integration's version, while agent and hub count on their own, as before; a
  GitHub release for hub or agent would become HACS's latest version and is not made.
* Bad, because HACS shows the README of the whole repository as the integration's description.

### Confirmation

The `hacs` workflow (HACS action and hassfest) passes on the public repository. The release workflow fails on a tag
that differs from `manifest.json`, and a release carries `vordr.zip` with `manifest.json` at its top level. The
integration's tests import it from `custom_components/vordr/` at the root.
