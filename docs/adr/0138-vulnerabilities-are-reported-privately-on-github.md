---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# Vulnerabilities are reported privately through GitHub

## Context and Problem Statement

Vördr installs updates and reboots hosts on signed orders. A flaw in signing, approval or the agent's policy is worth
reporting before it is public. `AGENTS.md` asked for private reports but named no way to send one.

## Decision Drivers

* A private channel that needs no address of the maintainer in the repository.
* Reporters know what to include and what happens next.

## Considered Options

* an e-mail address in the repository
* GitHub's private vulnerability reporting

## Decision Outcome

Chosen: GitHub's private vulnerability reporting, described in `SECURITY.md`. The operator turns it on for the public
repository when publishing. `AGENTS.md` points to `SECURITY.md`. Fixes are published together with an advisory.

Rejected:

* **An e-mail address,** because it would put a personal address into the repository and its spam.

### Consequences

* Good, because reports stay private until a fix exists, and the advisory is linked to the fix.
* Bad, because a reporter needs a GitHub account.

### Confirmation

`SECURITY.md` names the channel; the repository's security settings have private vulnerability reporting enabled.
