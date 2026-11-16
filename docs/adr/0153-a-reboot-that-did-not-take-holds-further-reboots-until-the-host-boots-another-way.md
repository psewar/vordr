---
status: accepted
date: 2026-11-16
decision-makers: kay (2026-11-16, asked to harden the reboot against a compromised Home Assistant); proposed by Claude
---

# A reboot that did not take holds further reboots until the host boots another way

Refines [ADR-0041](0041-reboot-with-window-and-proof.md) and [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)
in one point: the limit of one reboot per hour.

## Context and Problem Statement

An approved Home Assistant with the role `admin` can ask the hub to reboot a host; so can whoever takes over that Home
Assistant or the hub. The agent bounds this: it reboots only with `reboot.allowed`, only when the host itself reports
that it needs a reboot (running versus newest kernel, `reboot-required`), and at most once per hour. After the boot it
proves the reboot: a new boot ID and no further need, otherwise `failed_verification`.

One case is not bounded well. If a reboot does not clear the need, for example because the boot loader still starts
the old kernel, or because the host did not go down at all, the need stays. A caller that keeps asking then gets a
reboot every hour, for as long as it asks: the hourly limit turns into a reboot loop at a slower pace. Each of these
reboots also fails its proof, so nothing is gained by them.

## Decision Drivers

* A compromised caller must not be able to reboot a host again and again.
* A reboot that did not help is no routine matter: someone has to look at the host.
* The block must lift on its own once that has happened, without a command that the same caller could send.
* One fact that every Linux host has, not a list of reasons per source.

## Considered Options

1. After a reboot whose proof failed, no further reboot through Vördr while the host still runs the boot in which the
   failure was found; any boot after it lifts the block.
2. Block until the need changes (another newest kernel, other packages in `reboot-required.pkgs`).
3. Keep the hourly limit and make it longer (a day).
4. Block until a local command on the host clears it.

## Decision Outcome

Chosen option: **1**.

* **Marker:** the agent that proves the reboot after the boot writes into its reboot marker (`reboot.json`) that the
  reboot failed and the boot ID it found that in (`failed`, `failed_boot`), before it sends the result.
* **Refusal:** a reboot job is refused with the new result code `rejected_last_reboot_failed` while the host's boot ID
  is that boot, or cannot be read. The check comes after `reboot.allowed` and the window, before the hourly limit; the
  detail names the time of the reboot that did not take.
* **Lifted by a boot:** once the host has booted another way (a person, a power cut), its boot ID differs and the
  marker is history; a needed reboot goes through again, with all other checks.
* **Problem:** the code sets the host's problem sensor, like the `failed_verification` before it. Home Assistant knows
  it in all languages; its next step: reboot the host another way once and look why the last reboot did not help.
* A reboot whose `systemctl reboot` itself failed changes nothing on the host and keeps its old meaning
  (`failed_verification`, no block).

Option 2 needs a fingerprint of the need for every source (kernel versions, package lists, Windows Update keys) and
still lets a loop run if the need changes while the cause stays. Option 3 only slows the loop down. Option 4 asks for a
visit to the host as well, but without a fact that shows it happened; a boot is that fact.

### Consequences

* Good, because a caller can cause at most one reboot that does not help; after that the host waits for a person.
* Good, because the block lifts by itself after the next boot, whoever caused it.
* Bad, because a host whose reboot failed for a passing reason (the boot loader fixed in the meantime) needs one reboot
  by hand before Vördr reboots it again.
* Bad, because an integration or hub older than this change does not know the code: the integration's "Last run" shows
  no state, and an older hub counts it as a problem anyway (unknown codes are). Hub and integration go out before the
  agents.

### Confirmation

* `internal/agent/reboot_test.go` (`TestRebootAfterFailedProofIsHeld`): after a proof that fails because the host still
  needs a reboot, and after one that fails because the boot ID stayed the same, a reboot two hours later is refused with
  `rejected_last_reboot_failed` and nothing reboots; after a boot another way, the needed reboot goes through.
* `internal/protocol/protocol_test.go` (`TestCodes`): 32 codes, and `rejected_last_reboot_failed` sets the problem.
* `ha/tests/test_texts.py` checks that every language has the code's texts.
