---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, asked for the follow-up that bounds vet requests; accepted with the merge); proposed by Claude
---

# The hub refuses a vet request out of bounds whole

Extends [ADR-0195](0195-the-hub-bounds-reports-inventories-and-the-home-assistant-hello.md), which bounds what an agent
sends after its hello but left the `vet` request open ("its `job_id` and `ecosystem` reach the event log as sent"). It
uses ADR-0195's codes, events and rate limit; ADR-0195 stays as decided.

## Context and Problem Statement

Before a pip or npm update changes anything, the agent sends its whole plan - every name and version, dependencies
included - in a `vet` request (`job_id`, `ecosystem`, `packages` of `name`, `version`), and the hub answers with a
statement it signs ([ADR-0070](0070-hub-vets-and-signs-the-update-plan.md), design 11.4). The agent installs only a
plan the statement covers entry by entry. The hub bounded the request only by its number of entries
(`wire.MaxVetPackages`, beyond it a protocol violation); its job id, ecosystem, names and versions went as sent into
the event `vet`, and into the statement it signs - also for a host that is not approved.

The statement is the hub vouching for the update. Which bounds does a vet request get, and what does a request out of
them cost?

## Decision Drivers

* The hub never vouches for less than the agent installs: what it signs covers the plan as sent, or nothing.
* An agent whose plan is not vouched for stops its update, fail-closed, as it does for missing OSV data.
* Nothing of a request is logged before it is bounded; the log names a refusal with a fixed code
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)), and a host cannot write it full.
* The real agent's plans, pip and npm, are taken as they are, and the agent never asks for what the hub refuses.

## Considered Options

1. Trim: drop the entries out of bounds and vet the rest, as ADR-0195 drops what it can.
2. Refuse the whole request, and answer with a signed statement that vouches for nothing.
3. Treat a request out of bounds as a protocol violation and end the connection.

## Decision Outcome

Chosen option: **2**.

* **Whole or nothing:** `boundVet` (`internal/hub/bounds.go`) runs first. Any field out of bounds refuses the whole
  request; nothing of it is matched, signed as vetted, or logged as vetted.
* **The answer:** a signed statement with `unavailable` set to the new fixed code `vet_out_of_bounds`
  (`protocol.VetOutOfBounds`), no ecosystem and no packages, bound to the node and to the request's job id - unless the
  job id itself is out of bounds, then to none, and the agent's wait runs out. The agent stops the update with
  `precheck_vetting_unavailable`, its detail naming the code. The connection stays; more than `wire.MaxVetPackages`
  entries, an unreadable message and a hub without OSV data end it as before.
* **The bounds:**

  | Field | Bound | Code beyond |
  |---|---|---|
  | `job_id` | a job id (1 to 64 letters, digits, `-`), never empty: the agent asks only for its running job | `too_long`, `characters` |
  | `ecosystem` | one the hub can vet: PyPI, npm (`versions.Ecosystem.Known`) | `value`; `too_long` beyond 64 |
  | `packages` | up to `wire.MaxVetPackages` (5,000) | `too_many` (and a protocol violation at the server) |
  | `packages[].name`, `packages[].version` | text up to `protocol.MaxVetEntryLen` (1,024) bytes, as ADR-0195's names | `too_long`, `characters` |
  | `packages[].flagged`, `packages[].malicious` | empty and false: the hub fills them in | `value` |

  A sender that sets `flagged` or `malicious` is refused rather than ignored: no plan the agent makes carries them,
  and a request that does is not one to vouch for.
* **The agent never asks for what the hub refuses:** a version with a control character stays on the host when the
  inventory is read, as an entry it skips, so a refresh of dependencies never offers one; a planned entry whose
  version has white space around it, a control character or more than `protocol.MaxVetEntryLen` bytes stops the
  update with the agent's own `failed_package_manager` ("planned an entry that is no package") before the hub is asked.
  `vet_out_of_bounds` thus means another agent, or one with a fault, not a plan of a package manager.
* **A host that is not approved** has no run: its request in bounds gets the signed `no_running_update`, and nothing
  is logged, as for every other message of such a host.
* **Events:** `vet_refused` names the refusing field with its code (`fields`) and the key's `fingerprint`, through
  ADR-0195's rate limit - at most one per host in an hour, the next counting the others (`unlogged`) - and only for an
  approved host. It carries no text of the request. The event `vet` logs only a request in bounds of an approved host.

Option 1 would sign a statement about a part of the plan; even though the agent checks that the statement covers every
entry, the hub must not depend on that to never vouch for less than is installed. Option 3 ends the session of an agent
that may be in the middle of other work, and says nothing about why; the signed refusal gives the running job its
answer at once and with a code.

### Consequences

* Good, because the hub vouches only for a plan as the agent sent it, and a request out of bounds stops the update
  fail-closed, at once, with a fixed code in the run's detail.
* Good, because the event log gets no text of a vet request that is out of bounds, at most one refusal per host and
  hour, and nothing from a host that is not approved.
* Good, because the reflection test now walks `wire.Vet` and `protocol.VetPackage` too: a new field fails until it has
  its bound.
* Bad, because one entry out of bounds stops the whole update, also for the entries that are fine; the host's
  operator sees `precheck_vetting_unavailable` (or, for an entry the agent itself does not take,
  `failed_package_manager`) and the event or the detail names it.
* Bad, because a request whose job id is out of bounds gets an answer the agent cannot match: it waits for its timeout.
  The real agent always sends its job's id, which passed the job's own check.
* Neutral: a package version with a control character other than those the agent refused before - a form feed, an
  escape, a delete - is now skipped in the inventory, and counted in `skipped`.

### Confirmation

* `internal/hub/vet_bounds_test.go`: `TestTheHubBoundsEveryFieldOfAVetRequest` (at the bounds taken, one step beyond
  in each field refused whole, the request left as sent), `TestTheHubTakesWhatAgentsAskToVet` (pip's and npm's longest
  forms, a tree of 5,000), `TestAVetRequestOutOfBoundsIsRefusedWhole` (the signed refusal with no ecosystem and no
  packages, no job id echoed out of bounds, `vet_refused` with `unlogged`, no text of a refused request in the log,
  `vet` only in bounds), `TestAHostNotApprovedHasNoRunToVet`, `TestARefusedVetRequestKeepsTheConnection` (over a real
  tunnel; 5,001 entries still end it).
* `internal/hub/bounds_test.go` (`TestEveryFieldOfTheMessagesIsBounded`) walks the vet request with every other
  message.
* `internal/inventory`: `TestAVersionWithAControlCharacterStaysOnTheHost`, `TestAPlannedEntryTheHubWouldRefuseIsNoPackage`
  (pip and npm stop before the hub is asked) and `TestARefreshOffersOnlyWhatTheHubTakes` (external package: a refresh
  of dependencies asks with `hub.VetNotTaken` nil).
* `internal/agent/vet_bounds_e2e_test.go`: `TestAVetRequestOutOfBoundsStopsTheUpdate` (over a real tunnel, the agent's
  refresh of a location holding an entry the hub refuses stops with `precheck_vetting_unavailable`),
  `TestAnAgentDoesNotAskWithAPlanTheHubWouldRefuse`, `TestTheRealAgentsPipPlanFitsTheHubsBounds`;
  `internal/agent/pkgupdate_test.go` (`TestPackageUpdateThroughTheHub`) for npm plans, both with no `vet_refused`.
* The pull request's mutation probe removes each bound once; the tests fail for each.

## More Information

* [ADR-0195](0195-the-hub-bounds-reports-inventories-and-the-home-assistant-hello.md): the bounds of what follows the
  hello, the events and their rate limit.
* [ADR-0070](0070-hub-vets-and-signs-the-update-plan.md): the hub vets and signs the update plan.
* Design: [design/architecture.md](../../design/architecture.md) (6.2, 11.4).
