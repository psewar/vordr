---
status: accepted
date: 2026-10-30
decision-makers: kay ("then please build both"; "self-update works [in an earlier agent of the operator] without signing too")
---

# Self-update on Windows, without code signing

Extends [ADR-0032](0032-self-update-signed-by-operator.md).

## Context and Problem Statement

The self-update ([ADR-0032](0032-self-update-signed-by-operator.md)) was built for Linux only: systemd restarts the
agent after the swap, and a systemd unit puts the old binary back if the new one does not start. On Windows an agent
version reached a PC only through `install-service` by hand. The design planned Authenticode signing, because Smart
App Control could block a swapped binary. An earlier agent of the operator swaps its unsigned `.exe` on a Windows PC
without trouble.

## Decision Drivers

* A fixed agent reaches every host through the hub, Windows included.
* The authenticity of an agent binary rests on the operator's manifest signature and its hash, on every platform.
* No code-signing certificate unless something actually requires one.

## Considered Options

* keep updating Windows agents by hand with `install-service`
* self-update on Windows with Authenticode signing
* self-update on Windows without code signing, like the earlier agent

## Decision Outcome

Chosen option: self-update without code signing (the operator, 2026-10-30).

* Only as the Windows service, with `agent_update.enabled`; the agent checks manifest, hash and size and runs the new
  binary's `version` and `check-config` first, as on Linux.
* The swap renames the running `.exe` to `.old` and puts the new one in place. The agent then exits with code 3; the
  service's recovery action starts it again after 10 s.
* If the new agent reaches no approved session within 10 minutes, it moves itself aside as `.failed`, puts `.old` back
  and exits the same way.
* If the new agent does not start at all, the third recovery action runs `rollback.cmd` instead of a third restart. It
  puts `.old` back only while an update is pending and starts the service in any case. `install-service` writes it.
* The release tool also signs and publishes the Windows binary in the manifest.

### Consequences

* Good, because Windows PCs get agent fixes like every other host, through the canaries.
* Bad, because the tests cover the swap and the putting back, not the whole way through the hub.
* Bad, because if Smart App Control is switched on later, it may block the unsigned binary; then a certificate is
  needed after all.

### Confirmation

Native Windows tests: `TestSwapAndPutBackWithTheRunningBinary` swaps and puts back the running test binary
(with the Linux way of putting back, the test fails); `TestRollbackScriptPutsBackOnlyWhilePending` runs the generated
`rollback.cmd` in a folder with a space in its name.
