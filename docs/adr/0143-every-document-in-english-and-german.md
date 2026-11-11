---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked that every document exist in German and English; accepted with the merge); proposed by Claude
---

# Every document exists in English and German

Supersedes [ADR-0141](0141-readme-in-english-with-a-german-version.md), which made only the README bilingual and left
the other documents German for now.

## Context and Problem Statement

Since ADR-0141 the README is English with a German version beside it. The design documents, the integration's README
and the setup guide stayed German, and the security policy was English only. A visitor of the public repository who
does not read German can start Vördr but cannot follow how it is built or set it up; a German reader gets the
security policy only in English. The operator wants every document in both languages.

## Decision Drivers

* Everyone can read every document that describes Vördr, its setup and its design, in English or in German.
* Links, references in the code and the ADRs keep working.
* Two versions of one document do not drift apart unnoticed.
* The ADRs and the instructions for contributors stay one source each.

## Considered Options

* translate every document, English under the existing name, German as `name.de.md`
* translate every document, German under the existing name, English as `name.en.md`
* translate the ADRs as well

## Decision Outcome

Chosen: every document exists as `name.md` in English and `name.de.md` in German, as ADR-0141 did for the README. The
German versions keep their history by a rename-only commit. The proxy agent's design moves from `stellvertreter.md` to
`proxy-agent.md` / `proxy-agent.de.md`, so that the English name of the file matches the term the ADRs use.

* Each version carries a language line under its title that links the other one, and links the documents of its own
  language.
* Both versions change together in the same pull request (`AGENTS.md`).
* English only: the ADRs (ADR-0072), `AGENTS.md` and `CLAUDE.md`. The ADRs are records of decisions in one wording;
  `AGENTS.md` is the single source of the development rules for people and coding agents alike, and a second language
  would be a second source.
* German texts use Swiss spelling (`ss`, never `ß`), as the German documents already do.
* Texts users see in Home Assistant were bilingual before and stay in the integration's translations.

Rejected:

* **German under the existing name,** because the README already set English as the default name, and code comments
  and ADRs, which are English, point at `design/*.md`.
* **Translate the ADRs,** because they are many, accepted ADRs are not rewritten, and two wordings of one decision
  invite the question which one holds.

### Consequences

* Good, because every reader finds the design, setup and security policy in their language.
* Good, because the English names stay, so links from code and ADRs land on the English version without edits,
  except the renamed proxy agent design.
* Bad, because every change to a document is written twice.
* Bad, because a check sees the shape of both versions, not whether they say the same; that stays with the review.

### Confirmation

`tools/check_docs.py` runs in `./build.sh` and finds the documents itself: every Markdown file except `docs/adr/`,
`AGENTS.md`, `CLAUDE.md` and hidden or generated folders. For each it requires the other language's version, the
language line in both, the same sequence of heading levels, the same number of code blocks and table rows, links that
resolve to a file and, with an anchor, to a heading, and links to the documents of the same language. Its tests
(`tools/check_docs_test.py`) cover each rule.
