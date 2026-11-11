---
status: accepted
date: 2026-10-30
decision-makers: Claude (proposal); accepted by kay with the merge
---

# No code during install: npm without scripts, pip wheels only

## Context and Problem Statement

npm runs `preinstall`/`install`/`postinstall` scripts while installing; pip builds source packages with arbitrary code
(`setup.py`, build backends). Malicious npm packages almost always use install scripts. Should a Vördr update allow
that?

## Decision Drivers

* The agent runs as root or LocalSystem; on Linux pip and npm run as the owner of the location, but that is often a
  service account anyway.
* Some packages need their scripts (native modules) or only exist as source packages.

## Considered Options

1. Allow scripts and source packages, as pip and npm do by default.
2. Forbid them by default (`--ignore-scripts`, `--only-binary=:all:`), allow them explicitly per location
   (`packages.allow_scripts`, `packages.allow_sdist`).
3. Always forbid them.

## Decision Outcome

Chosen option: **option 2**. Planning (the dry run) always runs without scripts.

### Consequences

* Good, because an update runs no code from the package by default.
* Bad, because a location with a package that only exists as a source package cannot be updated as a whole
  (for example a tool published only as a source distribution); then per package or with `allow_sdist`.
* Bad, because packages that need scripts may be incomplete after an update without `allow_scripts`; the `health:`
  checks should show that ([ADR-0038](0038-health-checks-after-install.md)).

### Confirmation

`TestPipUpdate` ("sdist allowed"), `TestNpmUpdate` ("scripts allowed"); mutation probe "npm scripts run by default",
"source distributions by default".

## More Information

`design/architecture.md` 11.4.

Merged in German as ADR-0107 (status "proposed", accepted with the merge); later renumbered, translated and set to
accepted.
