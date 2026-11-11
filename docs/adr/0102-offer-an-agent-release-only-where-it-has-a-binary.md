---
status: accepted
date: 2026-11-02
decision-makers: kay (2026-11-02, accepted); proposed by Claude
---

# Offer an agent release only to hosts it has a binary for

Refines the rollout of [ADR-0032](0032-self-update-signed-by-operator.md). The canary rule of
[ADR-0058](0058-site-decision.md) stays; this says how it reads when a release cannot
reach a canary.

## Context and Problem Statement

An agent release is a signed manifest that names one binary per platform (`os` and `arch`). The operator decides
which platforms a release contains. A release made for a test or a drill often has one platform only, and a site
may not build every platform the agent supports.

The hub offers every release to every host that takes agent updates. An agent whose platform is missing refuses with
`rejected_unsupported`. That is safe, but:

* the hub asks again after `retry_after_hours`, and every refusal is a failed run in the host's history;
* Home Assistant shows an update for that host that can never be installed;
* a canary that cannot run the release never reaches its version, so the other hosts wait for ever, with nothing
  that says why.

The hub knows each host's architecture from its hello, but not which build of the agent it runs. The `os_id` fact is
the distribution on Linux, not the build.

How does the hub keep a release away from hosts it has no binary for?

## Decision Drivers

* A host is not asked for something it can only refuse.
* Home Assistant shows only updates that can come.
* A missing platform must not stop the rollout for the platforms that are in the release.
* The hub decides from facts the agent reports, not from guesses
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md): state as a field, not read from a refusal).
* Agents that do not report the new fact keep working as before.

## Considered Options

1. Leave it to the agent: offer the release everywhere, the agent refuses (as before).
2. The hub derives the build from the facts it already has: `windows` when `os_id` says so, `linux` otherwise.
3. The agent reports its build as a fact (`goos`, beside `arch`). The hub offers a release only where the manifest has a
   binary for that pair, says so in the host view, and counts only the canaries it has a binary for.
4. Require every release to contain every platform.

## Decision Outcome

Chosen: option 3, since hub 0.20.0 and agent 0.11.0; every agent from 0.11.0 on reports `goos`.

* The agent reports `goos` (its `runtime.GOOS`) in the host facts. A host is known only when both `goos` and `arch`
  are; a device behind a target has neither.
* The rollout skips a host whose known platform has no binary in the release. "Install" in Home Assistant refuses with
  the code `no_binary_for_platform` before a job is sent.
* The host view carries `agent_release_no_binary`. The integration then shows the installed version as the latest, so
  the update entity stays but shows no update.
* Canary rule: a canary the release has no binary for neither tries it nor holds the others back. Every other canary
  must run the release `canary_clean_hours` without a problem, and at least one canary must have run it; if none could,
  the second ring stays closed.
* A host that does not report its platform (an agent before 0.11.0) is offered the release as before. Its agent still
  checks the manifest itself and refuses with `rejected_unsupported`.

Rejected:

* **Option 1,** because of the repeated failed runs, the update in Home Assistant that never comes, and the second ring
  that waits for ever without a reason anyone can see.
* **Option 2,** because it guesses a fact the agent knows exactly. It breaks for any platform beside these two, and
  the agent's own check would still be the only source of truth.
* **Option 4,** because the hub cannot know which platforms a site has, a drill release would have to be built for all
  of them, and a site that runs no Windows hosts has no reason to build Windows binaries.

### Consequences

* Good, because hosts are asked only for releases they can install, and their history stays free of refusals that
  were certain beforehand.
* Good, because Home Assistant shows an agent update only where one can come.
* Good, because a release without one platform still reaches the others after its canaries.
* Bad, because a host on a platform the canaries do not share can get a release no canary of its platform tried.
  That was already so before: the rings were never per platform.
* Bad, because the gain needs agents that report `goos`. Until every agent runs 0.11.0 or later, the old behaviour
  stays for the others.
* Neutral: the agent keeps its own check. The hub's check saves a round trip but is not a security boundary; the
  manifest's signature and the agent's choice of binary are.

### Confirmation

* `TestReleaseOnlyWhereItHasABinary` (hub): a release with one platform is due for the matching canary only, the
  install refuses with `no_binary_for_platform`, the view sets `agent_release_no_binary`. The other hosts are due once
  the matching canary ran it 25 hours, and not when no canary could run it.
* `TestRolloutRings` keeps passing with hosts that do not report a platform.
* `TestPlatformNeedsOSAndArch` (facts): a platform is known only with both values.
* `test_agent_update_entity_without_a_binary` (integration): the update entity stays off with the flag set.
* A live check: for a release with all of a site's platforms, the host view marks no host as
  `agent_release_no_binary`.
