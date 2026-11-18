---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, accepted the recommendation to bound the surfaces ADR-0191 left open and to make check-config refuse what the hub drops; accepted with the merge); proposed by Claude
---

# The hub bounds reports, inventories and the Home Assistant hello

Extends [ADR-0191](0191-the-hub-bounds-every-field-of-an-agents-hello-before-it-keeps-it.md), which bounds an
agent's hello, to the surfaces it left open, and closes the gap it names as "Bad": `check-config` now refuses what the
hub would drop from the hello. ADR-0191 stays as decided; its codes and its way of dropping a field are reused here.

## Context and Problem Statement

After its hello an approved agent sends reports (sources, pending packages, facts, an excerpt of a tool's output, the
reboot need), inventories (locations of pip, npm and container images with their packages, holds, the packages inside
images) and job status messages (phase, progress, result, detail, a run's summary). A Home Assistant client sends a
hello of its own. All of it was bounded only by the size of one message, 4 MiB, and went as sent into the register,
the inventories on disk, the event log, Home Assistant and `vordr-hub show`. A broken or hostile agent could put
megabytes into a field, escape sequences into what people read, nonsense counts into decisions (a negative pending
count, a run summary that ends a ring delay), and write the event log full.

ADR-0191 also left `check-config` taking an `agent.yaml` whose apps, allowed scopes or window days the hub then drops
from the hello.

## Decision Drivers

* Nothing of these messages is stored, computed with, logged or shown before it is bounded - also what an earlier hub
  stored.
* What agents send today is taken whole, also from a developer machine with tens of thousands of npm packages, Windows
  paths and long image references; the bounds lie an order of magnitude or more above it, or at the agent's own cap.
* A value out of bounds costs as little as possible, but never leaves something that misleads.
* State is told as fixed codes ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)), and a host cannot write the
  event log full.
* `check-config` and the hub cannot drift apart; a new field fails a test until it has its bound.

## Considered Options

1. Refuse a whole message for any field out of bounds.
2. Bound every field in one place per message type; drop the narrowest part that keeps the rest meaningful, refuse
   the message only where the rest would mislead; log drops and refusals rate-limited.
3. Smaller size limits per message type, no bounds per field.
4. Repair what is out of bounds: cut long lists and texts, replace control characters.
5. For `check-config`: keep it as it is (ADR-0191's "Bad"), or give it bounds of its own beside the hub's.

## Decision Outcome

Chosen option: **2**, with option 4 for excerpts and for the number of locations only, and `check-config` refuses
with the hub's own bounds.

* **One place per message type:** `boundReport`, `boundInventory`, `boundJobStatus` (`internal/hub/bounds.go`) and
  `boundHAHello` (`internal/hub/hello.go`) run on the decoded message before anything else.
* **What a value out of bounds costs:** the field, the list or map it belongs to (taken whole or not at all, as
  ADR-0191 takes lists), or the list entry - each set to what an agent sends that does not report it. Where the rest
  would mislead without it, the hub refuses the whole message and keeps what it had: the names, states, codes and
  counts of a report's sources, the job id and result code of a job status.
* **Two repairs:**
  * An excerpt of a tool's output (`detail` of a source and of a job status) is cleaned, never dropped: every control
    character but CR, LF and tab, and every byte that is not UTF-8, becomes U+FFFD; of a longer one its end is kept,
    as the agent keeps the end of a tool's output. It is for people, colours of a tool's output are common, and an
    empty detail means something else: the hub's own guess at a run lost at a disconnect, which an interrupted run's
    own detail must not turn into.
  * Of an inventory with more locations than the bound, the first are kept and the rest dropped. The agent reports no
    more (it logs that it found more); a glob of `agent.yaml` may match hundreds of project folders, and refusing the
    inventory would lose every finding of the host.
* **Characters, not known values:** keys (states, codes, phases, reasons, signals, fact keys) are bounded by their
  characters; a newer agent's value is kept and means nothing to this hub, as ADR-0191 keeps a newer verb. Text is valid
  UTF-8 without control characters.
* **Location ids** are bounded by what the agent makes of them, not by what a scope takes: up to 64, while agents make
  ids of up to 32. A location with a longer id is kept and shown, but offers no update by button - the command would
  name a scope the hub refuses.
* **Nesting** is bounded by the wire types: the hub decodes into them and keeps nothing else, and they hold no
  interface, raw JSON or recursive type; an inventory nests deepest, 8 levels.
* **The bounds** (a key: up to 64 of `a`-`z`, `0`-`9`, `_`, starting with a letter; text: as above; an excerpt: up to
  16 KiB, cleaned as above):

  | Field | Bound | Out of bounds |
  |---|---|---|
  | report `sources` | up to 64 | report refused |
  | `sources[].source` | 1 to 64 of `a`-`z`, `0`-`9`, `_`, `-`, starting with a letter | report refused |
  | `sources[].state`, `sources[].code` | a key; the code may be empty | report refused |
  | `sources[].pending`, `security`, `kept_back` | 0 to 1,000,000 | report refused |
  | `sources[].packages` | up to 10,000; name, from, to text up to 1,024 bytes; up to 64 origins of text up to 1,024 bytes | the list dropped: the count stands for the whole pending set, as from a source that lists none |
  | `sources[].removals` | up to 10,000 texts up to 1,024 bytes | dropped |
  | `sources[].facts` | up to 64; a key to text up to 16 KiB | dropped |
  | `sources[].detail` | an excerpt | cleaned |
  | `reboot.checked` | up to 32 keys | the reboot need dropped: unknown |
  | `reboot.reason`; `reboot.packages`; `kernel_running`, `kernel_newest` | empty or a key; up to 1,024 texts up to 1,024 bytes; text up to 255 bytes | dropped |
  | inventory `locations` | up to 1,024 (`protocol.MaxInventoryLocations`, the agent's cap) | the first 1,024 kept, the rest dropped |
  | `min_age_days`, `min_age_days_security` | 0 to 90, `check-config`'s range | dropped (0): no fix at the shorter age ([ADR-0111](0111-shorter-minimum-age-for-security-fixes.md)) |
  | `locations[].id` | 1 to 64 of `a`-`z`, `0`-`9`, `_`, `-`, starting with a letter or digit | the location dropped |
  | `locations[].kind`, `ecosystem`, `code`, `packages` | 1 to 64 of `a`-`z`, `0`-`9`, `_`, `-`, starting with a letter; 1 to 64 of letters, digits, `._:-`, starting with a letter; empty or a key; up to 20,000 entries | the location dropped - a code says the list is incomplete, without it the location would look complete |
  | `locations[].path` | text up to 4,096 bytes | dropped |
  | `locations[].skipped`, `outdated_failed`, `not_in_registry` | 0 to 1,000,000 | dropped (0) |
  | `…packages[].name`, `version` | text up to 1,024 bytes | the package dropped |
  | `…packages[].via`, `latest`, `candidate`, `security_candidate`, `digest`, `rebuilt`, `same_major`; `compare_failed` | text up to 1,024 bytes; empty or a key | dropped |
  | `…packages[].holds` | up to 32; by and range text up to 1,024 bytes | dropped |
  | `…packages[].containers`; `updatable` | up to 1,024 texts up to 1,024 bytes; up to 1,024 Compose services as a scope names them | dropped |
  | `…packages[].os` | id and version text up to 1,024 bytes, code empty or a key, up to 5,000 packages of text up to 1,024 bytes | dropped: not read |
  | job status `job_id`, `code` | empty or a job id; empty or a key | refused |
  | `phase`, `reason`; `progress`; `item` | empty or a key; 0 to 100; text up to 1,024 bytes | dropped |
  | `detail` | an excerpt | cleaned |
  | `summary.package_count`, `summary.seconds` | 0 to 1,000,000 | the summary dropped: the run ends no ring delay ([ADR-0107](0107-ring-delay-per-update-ended-by-a-clean-canary-run.md)) |
  | `summary.packages`; `log_file`; `outdated_services`, `replaced`, `restarted` | up to 100 texts up to 1,024 bytes; text up to 4,096 bytes; up to 1,024 texts up to 1,024 bytes | dropped |
  | Home Assistant hello `t`, `role`, `name`, `public_key`, `signature` | as before | refused, `bad_hello` |
  | `protocol` | the hub's | refused, `protocol_mismatch` |
  | `integration_version` | empty, or a version a signed manifest may carry, as the agent's (ADR-0191) | dropped |

* **How the bounds were chosen:** a key: today's longest has 30 characters. Text of a name: npm names have up to 214
  characters, an image reference with its registry up to 255, a tag 128, a SHA-512 digest 135; 1,024 is four times the
  longest of these. A location id: agents cut theirs to 32. A path: `PATH_MAX` on Linux; Windows paths stay far below.
  An excerpt: the agent keeps the last 800 bytes of a tool's output, 16 KiB is twenty times that; a fact holds at most a
  list of package ids. Counts: a host has thousands of packages, a run lasts at most a day (86,400 s). Sources: an
  agent sends one to three. Pending packages: a release upgrade of a desktop leaves a few thousand. Origins, facts,
  signals, holds: agents send about three, ten, three and at most two. Lists of names (the packages a reboot is for,
  containers, services): tens. Locations: `agent.yaml` names up to 50 patterns, each of which may match many project
  folders. Where the agent caps a list itself, the hub takes exactly that cap, which no agent exceeds:
  `protocol.MaxInventoryLocations` per inventory, `inventory.MaxPackages` per location (the agent cuts beyond and says
  so with `too_many_packages`), `inventory.MaxOSPackages` per image, `wire.MaxSummaryPackages` per run; the minimum
  ages take `check-config`'s range.
* **Codes** are ADR-0191's: `too_long`, `too_many`, `characters`, `value`.
* **Events,** only for an approved host and with the key's `fingerprint`: `report_fields_dropped`,
  `inventory_fields_dropped`, `job_status_fields_dropped` name each dropped field with its code (`fields`, the field's
  key as `sources[].packages`) and each excerpt cleaned (`cleaned`); `report_refused` and `job_status_refused` name the
  field that refused the message. At most one of each event per host in an hour - a drop does not hide a refusal -; the
  next one counts the messages left out of the log in between (`unlogged`). A host the operator forgets starts afresh.
  A pending host's reports and inventories are not read; its job status messages are bounded and logged nowhere.
  `ha_hello_fields_dropped` (`client`, `fields`, `fingerprint`) follows only for an approved client, so that a waiting
  one cannot double the log with every reconnect.
* **What an earlier hub stored** - the last report in the register, the inventories on disk - is bounded the same way
  when the hub loads it. A stored report the hub refuses is not shown: unknown until the host reports again. Each host
  whose stored message was not taken whole gets one line in the service log, no event.
* **`check-config`** refuses what the hub would drop from the hello: more than 64 apps, an app name over 255 bytes or
  with a control character, more than 128 allowed scopes, more than 64 entries in the days of a window. Both read
  these bounds from `internal/protocol` (`MaxApps`, `MaxAppNameLen`, `MaxAllowedScopes`, `MaxWindowDays`,
  `PlainText`).

Option 1 would take a host's whole report away for one odd character in an excerpt, and its inventory for one package.
Option 3 ends the connection without a code and still lets control characters and nonsense counts through. Option 4
in general makes a cut list or text look like the agent's own, and a cut package list would match as complete; it is
taken only where its alternative is worse - an excerpt is already a cut-out and only read, and the locations beyond the
bound are named by the event. A `check-config` that knows other bounds than the hub, or none, lets a configuration pass
whose apps and buttons the hub then drops.

### Consequences

* Good, because everything an agent sends after its hello, a Home Assistant client's hello, and what an earlier hub
  stored is bounded before the hub keeps or shows it, and Home Assistant and `vordr-hub` show no control characters
  but the line breaks of an excerpt.
* Good, because a value out of bounds costs the field or entry, a message is refused only where the rest would mislead,
  and every drop and refusal names its field and code - at most one line per host, event and hour.
* Good, because `check-config` and the hub read the same bounds, and a test runs an `agent.yaml` at all of them through
  a real tunnel.
* Bad, because this is **breaking** for an `agent.yaml` beyond these bounds, which the agent took until now:
  `check-config`, `install-agent.sh` and the agent's start refuse it. An agent update onto such a configuration does not
  happen: before the swap the running agent has the new binary check `agent.yaml`, the update ends with
  `failed_verification`, and nothing is swapped; the operator shortens the configuration and updates again.
* Bad, because a refused report keeps the host's last one: Home Assistant shows the older state until `no_report` says
  the host has not reported, and only the event log says why.
* Bad, because a dropped package or location lowers the counts Home Assistant shows, the locations beyond 1,024 are
  missing from them, and a dropped `compare_failed` or `digest` makes an image look answered or built on the host;
  only the event log (and for the locations the agent's log) names it. A location with an id over 32 offers no update
  by button.
* Bad, because within an hour only the first message's fields are named, the others only counted, and a message of up
  to 4 MiB is still parsed whole before it is bounded.
* Neutral: the runs an earlier hub stored (their detail and summary) are not bounded on load; the next run replaces
  them.
* Neutral: `vet` is not covered; its plan is matched and only counted, but its `job_id` and `ecosystem` reach the event
  log as sent.

### Confirmation

* `internal/hub/bounds_test.go`:
  * `TestTheHubBoundsEveryFieldOfAReport`, `…OfAnInventory`, `…OfAJobStatus`: a message with every field at its bound
    is taken as sent; one step beyond - a megabyte, one entry more, control characters, invalid UTF-8, ANSI colours,
    capitals, a number out of range - refuses it, drops or cleans that field alone, with its code.
  * `TestTheHubTakesWhatAgentsReport`: Debian, Windows, an appliance, a release upgrade, a developer machine with 300
    projects and a location at the agent's cap with npm's longest names, Windows paths, image references, SHA-512
    digests, an image at its cap of packages, excerpts with CRLF and tabs, location ids up to 64, a refused job without
    an id.
  * `TestEveryFieldOfTheMessagesIsBounded` finds every field of the four messages by itself, a field added later too,
    also behind a pointer in a list or a map; far beyond any bound each is refused, dropped, cleaned, or bounded by its
    type. It fails for a type that holds an interface, raw JSON or itself, and for nesting deeper than 8.
* `internal/hub/bounds_more_test.go`: `TestAnExcerptIsCleanedNeverEmptied`, `TestAnInterruptedRunKeepsItsCleanedDetail`
  (no `run_lost` for an agent's own cleaned detail), `TestALongLocationIDIsKeptButOffersNoButton`,
  `TestTheFieldWalkReachesPointersAndMaps`.
* `internal/hub/bounds_events_test.go`: over a real tunnel, `TestMessagesOutOfBoundsAreLoggedOncePerHourAndHost`
  (drops and cleaned excerpts kept, refusals keep the last message, the first locations kept, one event per hour, host
  and event with `unlogged`, none for a pending host or a message in bounds); `TestAForgottenHostInheritsNoSilence`;
  `TestWhatAnEarlierHubStoredIsBoundedOnLoad`; `TestTheHubBoundsTheHelloOfAHomeAssistantClient` (versions taken,
  dropped, logged only once the client is approved, identity refused with `bad_hello`, nothing logged for a hello the
  register refuses).
* What the sources report, in the cases their own tests play, reaches the hub whole (`hub.ReportNotTaken`):
  `TestTheHubTakesWhatAptReports`, `TestTheHubTakesWhatWingetReports`, `TestTheHubTakesWhatWindowsUpdateReports`, and
  every report of the appliance checker's tests (`internal/agent/target_test.go`, with the drivers' statuses as stubs).
* `internal/agent/message_bounds_e2e_test.go` (`TestTheRealAgentsMessagesFitTheHubsBounds`): the real agent's
  inventory reader on npm's longest names and a nested hold, and an install run's progress and result reach the hub
  without a drop or refusal; its report is a stand-in. `internal/agent/inventory_cap_test.go`
  (`TestTheAgentReportsNoMoreLocationsThanTheHubKeeps`). `internal/agent/hello_bounds_e2e_test.go`: an `agent.yaml` at
  every bound `check-config` shares with the hub reaches it whole. `internal/inventory/inventory_test.go`
  (`TestWindowsPlaces`): a long Windows profile folder gives an id of 32.
* `internal/policy/bounds_test.go` (`TestCheckConfigRefusesWhatTheHubDrops`): at each bound `agent.yaml` passes, one
  step beyond it is refused with the field's issue.
* The pull request's mutation probe removes each bound once; the tests fail for each.

## More Information

* [ADR-0191](0191-the-hub-bounds-every-field-of-an-agents-hello-before-it-keeps-it.md): the hello's bounds and codes.
* [ADR-0007](0007-fixed-codes-instead-of-error-texts.md): fixed codes.
* Design: [design/architecture.md](../../design/architecture.md) (6.2, 9, 12),
  [design/ha-integration.md](../../design/ha-integration.md) (5a).
