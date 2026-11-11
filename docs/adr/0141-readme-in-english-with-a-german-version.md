---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# The README is English, with a German version beside it

## Context and Problem Statement

`AGENTS.md` made `README.md`, the integration's README and the design documents German. On GitHub the README is the
first page every visitor reads, also those who do not read German; HACS shows it as the integration's description.
Translating all design documents (well over two thousand lines) is a separate, larger task.

## Decision Drivers

* Visitors understand what Vördr does and how to start, in English.
* German readers keep their version; both say the same.
* No translation work that holds up the publication.

## Considered Options

* keep the README German
* README in English, German version in `README.de.md`
* translate every document

## Decision Outcome

Chosen: `README.md` in English, `README.de.md` in German, linked to each other and changed together. The integration's
README, `deploy/README.md` and the design documents stay German for now; the English README says so where it links
them. ADRs, code and commit messages stay English as before.

Rejected:

* **Keep it German,** because most visitors of a public repository would not get past the first page.
* **Translate everything now,** because it would delay the publication for documents few visitors read first.

### Consequences

* Good, because the entry page reaches everyone.
* Bad, because two READMEs must be kept in step, and setup instructions are still German only.

### Confirmation

`README.md` and `README.de.md` link to each other; a change to one changes the other in the same pull request
(`AGENTS.md`).
