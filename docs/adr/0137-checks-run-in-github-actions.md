---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# The checks run in GitHub Actions on the public repository

Adds to [ADR-0072](0072-decisions-as-adrs-checked-by-the-build.md), which made `build.sh` the gate.

## Context and Problem Statement

`build.sh` and the integration's tests are the gate every change passes, run by whoever makes the change. On a public
repository, pull requests come from people the maintainer does not know; their result should be visible before
anyone reads the change.

## Decision Drivers

* The same checks as locally, nothing extra to keep in step.
* No secrets in the workflow: pull requests from forks run it too.
* The site's own term list stays with the site (ADR-0099).

## Considered Options

* no CI; contributors run the checks themselves
* GitHub Actions running `build.sh` and the integration's checks

## Decision Outcome

Chosen: GitHub Actions (`.github/workflows/checks.yml`) on every push and pull request.

* One job: Go from `go.mod`, Python as the integration's tests need it, then `./build.sh` with
  `VORDR_SITE_TERMS="none: <reason>"` (the generic rules; the site list stays with the operator), then ruff and
  pytest in `ha/` against the pinned test requirements.
* Permissions read-only; no secrets; actions pinned to a major version.
* The private repository keeps running the same checks locally, with the site list.
* In the private repository the workflow files live in `tools/github-actions/`, because a Git hosting with its own
  Actions runner would run anything in `.github/workflows/` there; `tools/export_public.py` puts them into
  `.github/workflows/` of the published tree.

### Consequences

* Good, because every pull request shows whether it passes, also for strangers.
* Bad, because the public run cannot check site terms; they are checked where the list is, before export
  ([ADR-0133](0133-site-decisions-stay-with-the-site.md)).

### Confirmation

The workflow runs `build.sh` and the integration's checks; a red step fails the run. The exported tree has the
workflows in `.github/workflows/`, the private repository has none there.
