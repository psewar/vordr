---
status: accepted
date: 2026-10-30
decision-makers: Claude (proposal); accepted by kay with the merge
---

# Read the inventory from metadata, start no package manager

## Context and Problem Statement

How does the agent learn which pip and npm packages are installed?

## Decision Drivers

* The agent runs as root or LocalSystem; whatever it starts runs with these rights.
* No parsing of output in changing formats (project principle: state instead of error text, `AGENTS.md`).
* Foreign user profiles and environments without a matching interpreter should be readable too.

## Considered Options

1. Run `pip list --format=json` and `npm ls -g --json` per location.
2. Read the files the package managers leave behind: `*.dist-info/METADATA`, `*.egg-info`, `package.json` under
   `node_modules`.

## Decision Outcome

Chosen option: **option 2**. The inventory starts nothing and asks no one.

### Consequences

* Good, because no foreign code runs while checking; thousands of packages take about 3 s.
* Good, because it also reads a Linux venv on a Windows drive (the Windows agent cannot update it and says so,
  [ADR-0069](0069-package-updates-by-button-only.md)).
* Bad, because the agent must know the rules for "direct" (installed for itself) on its own (`package.json`,
  `Requires-Dist` without extras).
* Bad, because names and versions from foreign files are not trustworthy; only registry-valid ones go out (they start
  with a letter or digit, so they can never count as an option of a command later).

### Confirmation

Tests in `internal/inventory` (`TestVenv`, `TestNpmGlobal`, `TestNodeRedFolder`, `TestLinkedPackageNotDescended`).

## More Information

`design/architecture.md` 11.2.

Merged in German as ADR-0102 (status "proposed", accepted with the merge); renumbered, translated and set to accepted
later.
