---
status: accepted
date: 2026-11-15
decision-makers: kay (2026-11-15, asked for a winget installer whose hash does not match to get its own result code); proposed by Claude
---

# An installer that does not match its hash gets its own result code

## Context and Problem Statement

winget reports each package of an install run with its status (`PSInstallResultStatus`) and an extended error code.
The agent maps the statuses `DownloadError` and `CatalogError` to `precheck_repo_unreachable`
([ADR-0007](0007-fixed-codes-instead-of-error-texts.md): fixed codes, no error texts). Home Assistant then says that
the package source is unreachable, and its next step is to check the source and the network.

One `DownloadError` is not about reaching the source at all. With the extended error code `0x8A150011`
(`APPINSTALLER_CLI_ERROR_INSTALLER_HASH_MISMATCH`), winget downloaded the installer, but the file does not match the
SHA-256 hash that the package's manifest states, so winget refuses to run it. Packages that publish nightly or snapshot
builds under the same address do this regularly: the publisher replaces the file before the manifest catches up. It can
also mean that the file was swapped on the way or at its source.

The code `precheck_repo_unreachable` hides this. People check a network that works, and press "Install again"
([ADR-0148](0148-a-kept-failed-install-is-a-repair-that-installs-again.md)), which fails the same way as long as the
manifest is not corrected. Whether the file was replaced on purpose is a question someone has to look at, as with a
version that OSV flags.

## Decision Drivers

* Every outcome a caller has to tell apart has its own code; the agent decides on numbers and states, never on text.
* The text in Home Assistant should point to what helps: wait for a corrected manifest, or hold the package, and look
  at the file's origin if its publisher did not replace it.
* A file that is not the one its source vouches for is worth a look, like a flagged version.
* An older integration or hub must keep working with an agent that sends the new code.

## Considered Options

* A new result code `precheck_hash_mismatch` that the winget source returns when the extended error code is
  `0x8A150011`, whatever status winget files it under.
* Keep `precheck_repo_unreachable` and only extend its text in Home Assistant ("or the installer's hash does not match").
* Map the case to `failed_verification`.

## Decision Outcome

Chosen option: the new code `precheck_hash_mismatch`.

* **Agent:** `statusCode` in `internal/source/winget/install.go` returns `precheck_hash_mismatch` for any failed
  package whose extended error code is `0x8A150011` (the signed 32-bit value winget reports, `-1978335215`), before it
  looks at the status. `DownloadError` and `CatalogError` with any other code stay `precheck_repo_unreachable`.
* **Like the other precheck codes:** nothing of the package was installed. If another package of the same run went
  through, the run as a whole is `failed_package_manager`, as before, and the excerpt names the package and the code.
* **Problem:** the code sets the host's problem sensor, so a failed install keeps its "Install again" repair until a
  run goes through.
* **Home Assistant:** the integration knows the code (enum sensor "Last run", problem reasons, next step, repair text)
  in all languages. Its next step: wait for a corrected package manifest or hold the package (`winget pin add`); if the
  publisher did not replace the file, check where it comes from. A pinned package does not count as pending, so the
  next run no longer tries it.
* **Generic name:** the code names the case, not the package manager. winget is the first implementation; another
  source plugs in by mapping its own hash failure to the same code.

The text-only option was rejected: one code would still stand for two different causes, and an automation could not
tell them apart. `failed_verification` was rejected: in Vördr it means that a run reported success but the check
afterwards disagrees, and here nothing was installed.

### Consequences

* Good, because Home Assistant names the cause and the way out instead of sending people to check their network.
* Good, because a possibly swapped file shows as its own problem that someone looks at.
* Neutral: winget still decides that the hash does not match; Vördr only reports it.
* Bad, because an integration older than 0.36.0 does not know the code: its sensor "Last run" shows no state and the
  repair text falls back to "unknown". Integration and agent should therefore be released together, the integration
  first.

### Confirmation

* `internal/source/winget/winget_test.go` (`TestInstallResults`): `DownloadError` with `0x8A150011` and nothing else
  upgraded gives `precheck_hash_mismatch`; next to an upgraded package it gives `failed_package_manager`; a
  `DownloadError` with another extended code and a `CatalogError` stay `precheck_repo_unreachable`.
* `internal/protocol/protocol_test.go` (`TestCodes`): 30 codes, and `precheck_hash_mismatch` sets the problem sensor.
* `ha/tests/test_texts.py` checks that every language has the code's texts and one wording per code.
