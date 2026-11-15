---
status: accepted
date: 2026-11-15
decision-makers: kay (2026-11-15, asked to correct the pin hint in the texts of precheck_hash_mismatch); proposed by Claude
---

# The hash mismatch next step does not recommend a winget pin

Refines [ADR-0151](0151-an-installer-that-does-not-match-its-hash-gets-its-own-result-code.md) in one point: the next
step that Home Assistant shows for `precheck_hash_mismatch`. Everything else in ADR-0151 stays.

## Context and Problem Statement

ADR-0151 gave the code the next step "wait for a corrected package manifest or hold the package (`winget pin add`); if
the publisher did not replace the file, check where it comes from". It assumed that a pinned package no longer counts
as pending for the agent.

winget keeps pins in each user's own settings: `pinning.db` in the user's App Installer data. The
`Microsoft.WinGet.Client` module, through which the agent asks winget ([ADR-0050](0050-winget-via-powershell-module.md)),
has no command for pins. The Windows agent runs as a service under LocalSystem. A pin that an operator sets with
`winget pin add` in their own shell therefore lands in their profile, not in the one the agent asks from. The text
recommended a step that does not do what it promises.

## Decision Drivers

* A text in Home Assistant only recommends what works as written.
* The next step stays short and points to the cause: a manifest that has not caught up, or a file that may have been
  swapped.

## Considered Options

* Keep the pin tip.
* Recommend a pin in the agent's account (LocalSystem), for example through a one-time task.
* Leave the pin out of the next step.

## Decision Outcome

Chosen option: leave the pin out. The next step reads, in all languages: wait for a corrected package manifest; if the
publisher did not replace the file, check where it comes from. The design table (7.3) says the same.

Keeping the tip was rejected, because it does not reach the agent. A pin in the agent's account was rejected for this
text: Vördr has no tool for it, and whether the module honours a pin set that way has not been checked. That question
belongs to ADR-0050 and [ADR-0052](0052-major-version-jumps.md), which state that Vördr respects pins, and to
`deploy/README.md`. This ADR does not change them.

### Consequences

* Good, because the text no longer sends people to a step without effect.
* Neutral: a package that should not be updated can still be left out by uninstalling it or by waiting; a working way
  to hold a winget package for the agent is an open question of its own.
* Bad, because ADR-0050, ADR-0052 and `deploy/README.md` keep the pin statement until that question is answered.

### Confirmation

* `ha/tests/test_texts.py`: every language has the texts of every code, and the repair text contains the next step.
* The texts in `strings.json`, `translations/en.json` and `translations/de.json` and the design table no longer mention
  `winget pin` for `precheck_hash_mismatch`.
