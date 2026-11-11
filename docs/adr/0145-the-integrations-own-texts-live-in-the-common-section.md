---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to fix the findings of hassfest; accepted with the merge); proposed by Claude
---

# The integration's own texts live in the `common` section, and its translations pass hassfest

## Context and Problem Statement

Since [ADR-0027](0027-protocol-2-codes-and-translations.md) the integration writes every text from its translation
files. Most of them belong to sections Home Assistant translates itself (`entity`, `issues`, `services`, `config`). A
few the integration writes itself, in the system language: the summary of an update entity such as "12 pending
(3 security)", the package list, the counts of container images. They sat in a section `texts` the integration had
invented.

hassfest, Home Assistant's own check, which the HACS default list requires to pass
([ADR-0144](0144-the-integration-lives-at-the-repository-root.md)), accepts only the sections Home Assistant knows and
refuses `texts`. It also refuses any text that looks like an HTML tag (`<` followed by a letter, up to `>`), which hit
two notations of the form `wss://<host>[:port]` and `<project>/<service>`.

## Decision Drivers

* hassfest passes without changes to Home Assistant or ignored checks.
* The texts keep their placeholders (`{pending}`, `{security}`) and stay in every language's file.
* A rule Home Assistant applies is checked in the repository before the public CI sees it.

## Considered Options

* the section `common`
* the section `exceptions`
* the section `selector`
* keep `texts`

## Decision Outcome

Chosen: `common`. hassfest accepts it for every integration as plain keys with texts, placeholders allowed; it is the
section for texts that belong to no entity, issue or flow. The integration loads it at setup as it loaded `texts`
before, and the keys stay the same.

* Notations that name a value use capitals instead of angle brackets: `wss://HOST[:PORT]/v1/ha`, `PROJECT/SERVICE`.
* `ha/tests/test_texts.py` checks for every language what hassfest checks for strings.json and en.json: only known
  sections, no text that looks like an HTML tag, keys of `common` in lower case.

Rejected:

* **`exceptions`,** because it is for the messages of errors raised with a translation key, and each text would need a
  `message` level.
* **`selector`,** because it is for the options of selectors and needs an `options` level.
* **Keep `texts`,** because hassfest refuses it, and with it the HACS default list.

### Consequences

* Good, because the translation files pass hassfest, and the test shows a breach before a pull request is merged.
* Bad, because the test copies hassfest's list of sections and its HTML rule; when Home Assistant changes them, the
  daily hassfest run in CI shows it first, and the copy is updated then.

### Confirmation

`ha/tests/test_texts.py` (`test_texts_pass_home_assistants_checks`), and the hassfest job of the `hacs` workflow on the
public repository.
