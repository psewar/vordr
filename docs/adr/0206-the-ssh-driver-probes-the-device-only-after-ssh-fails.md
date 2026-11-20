---
status: accepted
date: 2026-11-20
decision-makers: kay (2026-11-20, "Ja, PR bauen" on the proposal to probe only after ssh fails); proposed by Claude
---

# The SSH driver probes the device only after ssh fails

Changes when the SSH driver of [ADR-0094](0094-ssh-drivers-use-the-system-openssh-client.md) establishes the way and
the host key. The system's OpenSSH client, its options, the pinned host key and the codes stay as ADR-0094 decided.

## Context and Problem Statement

ADR-0094 proves two facts before every `ssh`: a bare TCP connection shows that the address answers, and `ssh-keyscan`
shows that the device offers the pinned host key. Only then does `ssh` run, so that its exit 255 can only mean the
login. Every call of the forced command therefore opens three connections, and two of them end before a login.

A login protection on the device, such as sshguard on pfSense, counts a connection that ends before a login as an
attack. Once enough of them come within its detection time, it blocks the address for a while, and longer each time
it happens again. An install asks the forced command for its status every 15 seconds (`targetPoll`), so an install of a
few minutes is enough to block the proxy agent in the middle of it. The `ssh` that meets the block ends with 255 after
both probes have passed, and the driver reports `failed_target_auth`: a refused login, with a repair that sends people
to the service account although nothing is wrong with it.

How does the driver keep its codes decided on facts without tripping the device's login protection?

## Decision Drivers

* A call that goes through must look like one ordinary login to the device: nothing a login protection counts.
* Codes decided on facts, never on OpenSSH's text ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* A device without the pinned host key never sees a login (ADR-0094).
* No setting on the device just for Vördr: an exception in the login protection is the site's choice, not a
  precondition.

## Considered Options

* Run `ssh` first and probe only after it ends with 255.
* Keep the probe before each call, but remember its result for some minutes.
* One SSH connection shared by the calls of a job (OpenSSH connection multiplexing).
* Ask for the install status less often.
* An exception for the agent's address in the device's login protection.

## Decision Outcome

Chosen: run `ssh` first and probe only after it ends with 255.

* A call opens one connection, `ssh`'s own. OpenSSH compares the device's host key with the known_hosts file in the
  key exchange, before user authentication (RFC 4253, RFC 4252). The file holds only the pinned key and
  `StrictHostKeyChecking=yes` stays, so a device with another key ends the connection before the client key is
  offered.
* When `ssh` ends with 255, the driver establishes why, in the order of ADR-0094:
  * The address does not answer: `precheck_repo_unreachable`. A login protection that blocks the agent's address
    looks the same, and that is what it is: the device is not reachable for the agent.
  * `ssh-keyscan` does not show the pinned key: `failed_target_hostkey`.
  * Otherwise: `failed_target_auth`, with `ssh`'s text in the detail for people.
* Any other exit of `ssh` is the forced command's refusal, as before.

Rejected:
* **Remember the probe's result:** still two counted connections per interval, and a stale result could name the
  login for what is in fact a new host key or a block.
* **One shared connection:** a control socket and a master process that outlives the call, more state in the
  instance, and still a probe at the start of each job.
* **Ask less often:** fewer calls only delay the block; a long release install still reaches it.
* **An exception in the login protection:** it exempts the address from brute-force protection entirely and is a
  setting on each device. A site may still choose it; the driver must work without it.

### Consequences

* Good, because a call that goes through is one login; the login protection counts nothing in normal operation.
* Good, because a block by the login protection shows as `precheck_repo_unreachable`, not as a refused login.
* Good, because the time window between `ssh-keyscan` and the login that ADR-0094 accepted is gone: `ssh` checks the
  key itself on the connection it logs in on.
* Bad, because the private key is written to its 0600 file also for a call to a device with another key. It is
  removed after the call as before, and `ssh` does not offer it before the host key matched.
* Bad, because a failed call costs up to two more connections that end before a login. Failures are rare, and each
  one is a real fault.
* Bad, as in ADR-0094: a network failure during `ssh` followed by a probe that passes counts as `failed_target_auth`.

### Confirmation

* `TestPfsenseOneConnectionPerCall` in `internal/target`: twenty calls (check and install status) open no bare
  connection and run no `ssh-keyscan`. It fails with the probe before each call.
* `TestPfsenseCodes`: every code comes from the probe after `ssh` ended with 255, which runs once; a refusal of the
  forced command runs no probe; a device with another host key never receives the client key; an address that stops
  answering is `precheck_repo_unreachable`.
* The stand-in for `ssh` compares the device's key with known_hosts before it takes the client key, as OpenSSH does.
