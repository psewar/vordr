---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Yes, raise it", after images failed their comparison in every run); proposed by Claude, who also chose the value
---

# Read tag lists of up to 60000 tags

Refines [ADR-0113](0113-report-outdated-container-images.md) (report outdated container images) in one point: the
bound on a repository's tag list. Everything else there stays.

## Context and Problem Statement

To find a newer tag of the same form, the agent reads the repository's whole tag list. ADR-0113 bounds it at 20 pages
of 1000 tags. A longer list counts as no answer, because the newest tags could be on a page that was not read, and the
image is counted as `outdated_failed`.

Widely used repositories go past that bound: some publish an image for every commit of their main branch and reach
20000 to 30000 tags in more than 20 pages. Their comparison fails in every run, and a host misses newer releases of
the same form that have been out for weeks. Such a failure is not a passing fault that the next run fixes. It is a
lasting blind spot, and the inventory shows only a count, not which image.

One such list grows by about 500 tags a month: sorted by last change, its thousandth tag was two months old.

## Decision Drivers

* An image that fails its comparison every day is a blind spot, not a fault.
* An incomplete list stays no answer: a partial list could hide the newest tag.
* The bound protects the run's request budget (300 per run, ADR-0113) and the agent's memory. A tag list page is not
  a manifest download, so it does not count against Docker Hub's pull limit.
* Room for years of growth, so that the same repositories do not fail again soon.

## Considered Options

1. Keep 20 pages.
2. 60 pages of 1000, so at most 60000 tags per repository, and a run keeps up to 120000 tags for further images of
   the same repository.
3. No bound on the pages, only the run's request budget.
4. Read only part of the list, from the running tag on (`last=` in the distribution API).
5. Ask a registry's own API that sorts tags by date.

## Decision Outcome

Chosen: option 2.

* A tag list is read in at most 60 pages of 1000 tags. A list with more than 60000 valid tags still counts as no
  answer.
* A run keeps up to 120000 tags of lists for further images of the same repository, two of the longest lists.
* Unchanged: 1000 tags and at most 1 MiB per page, 300 requests per run, 20 manifest downloads per registry and run.

Rejected:

* **Option 1,** because such repositories would stay blind for good, and more will follow: publishing every commit
  as a tag is common.
* **Option 3,** because one repository could then take the whole budget of the run, and the images after it would
  fail instead.
* **Option 4,** because the registries list tags in lexical order, not in version order. A newer release can sort
  before the running tag (`3.10` before `3.6`), and the answer would be wrong rather than missing.
* **Option 5,** because it is not part of the distribution API: each registry has its own, if any, and the agent asks
  every registry the same way (ADR-0113).

### Consequences

* Good, because such repositories are compared again, and a list of 30000 tags has room for about five years at
  500 tags a month.
* Bad, because a list of 60 pages takes a fifth of a run's 300 requests. Several such repositories on one host can use
  up the budget; what is left counts as failed until the next run.
* Bad, because a run can hold more tags: one list being read and 120000 kept, under 10 MB.
* Bad, because every page is a request: a long list takes tens of seconds, within the run's 10 minutes.
* Neutral: which image failed, and why, is still reported only as a count. Reporting a reason per image would be a
  separate decision.

### Confirmation

* `internal/images`: `TestLongTagListsAreRead`: lists of under 30000 and of 60000 tags in pages of 1000 are read
  whole, 60001 is no answer, and a list of 60000 is read once for two images of the same repository.
  `TestTagListsAreBounded` keeps the bounds by page size and number of tags.
* Mutation probes: 20 pages, 59 pages, `>=` at the bound, and the earlier 50000 kept tags each fail the test.
* A live check: the container entities of hosts that run such images no longer count them as failed.
