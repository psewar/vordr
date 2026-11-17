---
status: rejected
date: 2026-11-17
decision-makers: kay (2026-11-17, rejection confirmed; asked on 2026-11-17 whether an AI should help judge how safe or how urgent an update is); proposed by Claude
---

# An AI may not change maintenance windows, minimum ages, ring delays or scopes

Refines [ADR-0045](0045-site-decision.md), which lets an AI explain a failed run and nothing more.
This ADR records the option that was weighed and rejected, so it is not proposed again by accident.

## Context and Problem Statement

An optional AI part for Vördr was asked for: to judge how safe an update is and how fast it should go. The tempting
version lets the AI act on its judgement: install sooner than the minimum age, wait longer than the ring delay, widen
or narrow what a run covers, open or close a maintenance window.

The settings in question are the ones that decide when and what is installed: the maintenance window and reboot
window ([ADR-0087](0087-runs-outside-the-window-with-a-reason.md),
[ADR-0154](0154-a-host-can-keep-reboots-inside-its-window-even-with-a-reason.md)), the minimum age of a target version
([ADR-0068](0068-minimum-age-for-target-versions.md), [ADR-0111](0111-shorter-minimum-age-for-security-fixes.md)), the
ring delay ([ADR-0036](0036-automatic-install-in-rings.md), [ADR-0107](0107-ring-delay-per-update-ended-by-a-clean-canary-run.md))
and the scope of a run (security only or all, held packages).

## Decision Drivers

* The agent's local policy has the last word ([ADR-0006](0006-local-policy-decides.md)), and decisions rest on
  deterministic data that a person can check.
* The text an AI reads is controlled in part by whoever publishes a release: changelogs, release notes and log
  excerpts can carry instructions. A change of a window or an age driven by such text lets a publisher steer when a
  site installs.
* A wrong "wait" holds a security fix back; a wrong "hurry" skips the canaries.
* The AI part is optional. A site without it, or with it switched off, must behave the same as before.
* Results travel as fixed codes, and text is never parsed to decide anything
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).

## Considered Options

* The AI sets or changes windows, minimum ages, ring delays and scopes, within bounds.
* The AI proposes a change that a person confirms.
* The AI changes none of these; it only advises, and everything it says is shown as advice.

## Decision Outcome

Rejected option: letting an AI change maintenance windows, minimum ages, ring delays or scopes, whether directly or as a
proposal that becomes a setting.

Why: how urgent an update is comes better from data that can be checked (a catalogue of known exploited
vulnerabilities, a severity vector, the origin of the update), which gives the same answer every time and cannot be
swayed by the text of a release. An AI judgement of urgency is not checkable and can be manipulated through a
changelog. "Slower" is as dangerous as "faster": a forged breaking-change note would hold a security fix back.
Bounds do not fix this, because the bounds are the same settings.

What stays: an AI, where a site chooses to use one, may advise (explain a failed run, summarise release notes, name a
likely breaking change) through Home Assistant, as fixed codes with a quoted proof, and a person decides. Anything it
suggests is at most a button that a person presses; then the policy, the window, the backup check and the role apply
as always. A faster path for one kind of update, if wanted, is a deterministic rule at the hub with its own ADR.

### Consequences

* Good, because installing sooner or later never depends on text that an attacker may influence.
* Good, because sites without an AI behave exactly as before, and the AI cannot widen what an agent may do.
* Neutral: urgency is improved by deterministic signals instead (see the proposal for an urgency code and the examined,
  rejected waiver for known exploitation).
* Bad, because a case that only an AI would have recognised (a subtle breaking change) still needs a person to read the
  advice and act.

### Confirmation

* Tests of the integration's AI part: an injected instruction in the input does not change any setting; the output
  accepts only values from a fixed list and a proof that appears literally in the input, otherwise the code is
  `unverified` and the step is `none`.
* A review rule: the AI code has no path to the configuration of windows, minimum ages, ring delays, scope or roles,
  and starts no run.
