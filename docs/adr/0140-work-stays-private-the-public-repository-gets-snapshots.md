---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# Work stays in the private repository; the public one gets checked snapshots

Settles what [ADR-0100](0100-publish-as-a-new-repository-with-one-start-commit.md) left open: where work happens after
publishing.

## Context and Problem Statement

ADR-0100 publishes a new repository with one start commit and left open whether later work happens there or in the
private repository and is carried over. The private repository holds what the public one must not: decisions about
the operator's own site ([ADR-0133](0133-site-decisions-stay-with-the-site.md)), and the operator's review and
automation work with it.

## Decision Drivers

* Nothing reaches the public repository that the checks have not seen.
* The operator's way of working stays as it is.
* Others can report problems and propose changes.

## Considered Options

* work moves to the public repository
* work stays in the private repository; the public one gets exported snapshots

## Decision Outcome

Chosen: work stays private; the public repository gets snapshots.

* Each publication runs `tools/export_public.py` on a commit of `main` and replaces the content of the public
  repository with the exported tree in one commit. The tool runs the checks; its result is what is published.
* Issues and pull requests on GitHub are welcome. An accepted pull request is carried over into the private
  repository with the author named (`Co-authored-by`) and comes back with the next snapshot.
* Release tags of the integration ([ADR-0136](0136-integration-via-hacs-as-a-zip-release.md)) are set on snapshot
  commits.

Rejected:

* **Work moves to the public repository,** because the site's decisions and the operator's review would need a second
  place, and every push would be a publication without the export's checks.

### Consequences

* Good, because each snapshot passed the same checks as the start commit.
* Bad, because the public history is a series of snapshots; who changed what in between is in the ADRs only.
* Bad, because a pull request on GitHub is merged by carrying it over, not with the merge button.

### Confirmation

Every commit of the public repository is the result of `tools/export_public.py` on a commit of the private `main`.
