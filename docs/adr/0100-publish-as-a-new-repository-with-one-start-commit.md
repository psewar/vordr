---
status: accepted
date: 2026-11-01
decision-makers: kay (2026-11-01, chose the option); proposed by Claude
---

# Publish as a new repository with one start commit

Refines [ADR-0082](0082-repo-stays-private-for-now.md): the repository stays private until the operator publishes;
this decides what a publication contains. The content rule is
[ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md).

## Context and Problem Statement

ADR-0099 makes the working tree site-neutral and keeps it so. The history does not follow: earlier commits hold the old
test data, examples, ADR texts and file names, and commit messages and merge commits name hosts and the operator's
tools. ADR-0082 already expected "a cleaned copy rather than this history".

How does the history reach the public, if at all?

## Decision Drivers

* Nothing published allows conclusions about the operator's systems, the history included.
* What is published can be checked completely, by the same check as the working tree.
* The private history stays as it is: it is the record of how decisions came about.
* Little effort at the moment of publishing.

## Considered Options

* a new repository whose history starts with one commit of the site-neutral tree
* the existing repository with its history rewritten (`git filter-repo` with replacements for texts and messages)

## Decision Outcome

Chosen: a new repository with one start commit (the operator, 2026-11-01).

* The public repository starts with the site-neutral tree of `main` at the time of publishing, as one commit. Before
  that commit, `build.sh` runs green, which includes `tools/check_neutral.py`, and the commit message itself passes
  `tools/check_neutral.py --text`.
* Its location is the module path in `go.mod`; the integration's documentation link points there.
* The private repository keeps its full history and stays where it is. It is not mirrored to the public one.
* Whether later work happens in the public repository or is carried over from the private one is decided when it is
  published, together with the license (ADR-0082).

Rejected:

* **Rewriting the history,** because a replacement list catches only the terms it knows: an old test fixture, a binary,
  a merge commit or a message phrased differently can slip through, and with hundreds of commits nobody can show that
  none did. It also changes every commit hash of the private repository, or needs a second, rewritten copy that then
  has to be checked as a whole.

### Consequences

* Good, because exactly what the check has seen is published.
* Good, because the private history, with all its context, stays intact.
* Bad, because the public repository has no history: who changed what before the start commit, and why, is only in the
  ADRs and the design documents.
* Bad, because the start commit holds everything at once; contributors cannot follow earlier changes.

### Confirmation

When publishing: `build.sh` green on the tree to be published, `tools/check_neutral.py --text` clean on the commit
message, and the public repository has exactly one commit.
