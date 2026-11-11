---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# The release signer is part of the repository

Refines [ADR-0032](0032-self-update-signed-by-operator.md) and
[ADR-0035](0035-backup-operator-key.md).

## Context and Problem Statement

Agents update themselves only with a release whose manifest the operator key in their own `agent.yaml` signed
(ADR-0032). The repository contained the manifest format and its check, but the tool that creates the key and signs a
release was part of the operator's private tools. Others could run the agent but not publish an agent release for
their own hosts.

## Decision Drivers

* Every operator can create a key and sign releases for their own hosts.
* The key is never on the hub, and an existing key is never replaced: every agent trusts exactly this key.
* A release is signed only when it is complete and its version is right.

## Considered Options

* document the manifest format and leave the tool to each operator
* a small command in the repository: `vordr-release`

## Decision Outcome

Chosen: `cmd/vordr-release`, built by `build.sh` for Linux and Windows, run on the operator's machine.

* `keygen <key-file>` creates an Ed25519 key (a base64 seed, mode 0600) and prints the public key; it refuses when the
  file exists.
* `public-key <key-file>` prints the public key for `agent_update.public_key`.
* `sign <key-file> <X.Y.Z> <dist-dir> <out-dir>` signs every agent build in `dist-dir` as release X.Y.Z, verifies the
  signature as an agent would and writes binaries, `manifest.sig` and `manifest.json` (last) into a new directory,
  ready for `<state_dir>/agent` on the hub. It refuses a version that is no release (ADR-0034), a key file readable by
  others, a non-empty target, and a build for its own platform that reports another version; without such a build,
  only `--skip-version-check "<reason>"` signs.
* Copying the release to the hub, and a backup of the key apart from it (ADR-0035), stay with the operator.

Rejected:

* **Document only,** because each operator would write the signing step anew, with its own mistakes.

### Consequences

* Good, because others can run the whole update path, self-update included.
* Good, because the tool checks what the agent will check, before anything is written.
* Bad, because the key file is as valuable as before: whoever holds it can sign agents for every host that trusts it.

### Confirmation

`cmd/vordr-release/main_test.go`: keygen never replaces a key; the public key matches; a signed manifest verifies with
the agent's own check; each refusal writes nothing; the version of the host's build is compared.
