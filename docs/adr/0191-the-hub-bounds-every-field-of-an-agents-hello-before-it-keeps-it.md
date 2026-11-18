---
status: proposed
date: 2026-11-18
decision-makers: proposed by Claude (2026-11-18), for the maintainer to accept or reject
---

# The hub bounds every field of an agent's hello before it keeps it

Extends the bounded reboot window of
[ADR-0187](0187-the-agent-reports-its-reboot-window-and-home-assistant-says-when-a-reboot-can-run.md) to the whole
hello. ADR-0187's check and its event `reboot_window_refused` stay: they are the first instance of this rule. One of
its bounds changes: a window's days are taken as `check-config` takes them - day names, a day repeated too - with at
most 64 entries, instead of at most seven, each once, which refused windows `check-config` accepts.

## Context and Problem Statement

An agent's hello carries its identity - node name, public key, the signature over the hub's challenge - and a
description of the host: the agent version, eight facts (host name, OS, OS id and version, build, architecture,
virtualization, container), the verbs it can carry out, the jobs it runs and the results it holds
([ADR-0114](0114-agent-names-its-run-and-the-hub-acknowledges-results.md)), and the summary of its local policy
(allowed scopes, default and automatic scope, `install.auto`, apps, install window, time zone, minimum interval,
switches).

The hub refuses a hello whose identity is malformed with `bad_hello`. Of the description, only the reboot window is
bounded (ADR-0187). Every other field is bounded only by the size of one message, 4 MiB, and goes into the register as
sent - also for a host that is not approved yet. From there it reaches:

* the event log (`agent_version` in the event `hello`);
* the views Home Assistant gets: the device's model from the OS, the hosts waiting for approval with their OS, the app
  names, `next_install` computed from the minimum interval;
* `vordr-hub list` and `vordr-hub show`;
* the hub's own decisions: verbs, scopes, install window and time zone, minimum interval, the platform an agent
  release must fit.

An unapproved or hostile agent can thus make the hub store and carry megabytes per host, put control characters into
what Home Assistant and the command line show, and make the hub compute with values no agent sends: a minimum interval
whose hours overflow a duration, an install window of a hundred thousand days.

Which bounds does the hub set on a hello, and what does it do with one that goes beyond them?

## Decision Drivers

* Nothing of a hello is stored, computed with or shown before it is bounded - also for hosts that are not approved.
* No existing agent loses its connection or a field it reports today: what `check-config` takes, the hub takes, unless
  it lies far beyond what anyone writes; the bounds must hold everything the real agent sends.
* State is told as fixed codes and fields ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)): the operator sees
  which field was not taken and why.
* A newer agent keeps working with this hub: a verb or scope the hub does not know yet is no fault
  ([ADR-0067](0067-hub-announces-features.md)).
* The checks are in one place, so that a new field gets its bound beside the others.

## Considered Options

1. Refuse a hello with any field out of bounds (`bad_hello`).
2. Identity strict: refused as before. Every descriptive field bounded on its own: one out of bounds is dropped, with an
   event that names it; the host connects.
3. A smaller message limit for the hello (for example 64 KiB) and no bounds per field.
4. Shorten a text or list that is too long to its bound instead of dropping it.
5. Bound only sizes (length, count), not characters.

## Decision Outcome

Chosen option: **2**.

* **Identity:** the type `hello`, a node name as `agent.yaml` takes it (`^[a-z0-9][a-z0-9.-]{0,62}$`), a public key of
  32 bytes, and a signature that verifies over the challenge, the name and the key (only one of 64 bytes can). A hello
  that fails is refused with `bad_hello` and the event `hello_refused`, as before; nothing of it is stored.
* **Description:** once the identity holds, and before anything is stored, every other field is bounded. Each is taken
  whole or not at all. A field out of its bound is dropped: set to what an agent sends that does not report it - empty,
  none, absent, 0 -, and a fact to `unknown`, what an agent sends for a fact it could not find out (`unknown` never
  means no; an empty `container` would count as a container). The host connects with the rest.
* **The events:** `hello_fields_dropped` names every dropped field of the hello with its code (`fields`: field to code),
  `reboot_window_refused` (ADR-0187) why the reboot window was dropped; both with the key's `fingerprint`. They are
  logged only once the register has taken the host, pending or approved. A hello the register refuses - another key
  under a known name, a rejected host, no room for another pending one - names a node that is not its own: nothing of
  it is kept, and nothing is logged under that name but the event `hello` with its refusal.
* **Order:** the reboot window is checked first, on the hello as sent (ADR-0187); then the other fields, the zone among
  them.
* **The bounds:**

  | Field | Bound | Dropped to |
  |---|---|---|
  | `agent_version` | empty, or a version a signed manifest may carry: up to 64 letters, digits, `.`, `+`, `-`, starting with a letter or digit | empty |
  | `facts.hostname`, `facts.os`, `facts.os_id`, `facts.os_version`, `facts.goos`, `facts.arch`, `facts.virtualization`, `facts.container` | each up to 255 bytes of UTF-8 without control characters | `unknown` |
  | `verbs` | up to 16, each up to 32 of `a`-`z`, `0`-`9`, `_`, starting with a letter | none |
  | `runs` | up to 64 ids in each of its two lists, each a job id (1 to 64 letters, digits, `-`) | absent |
  | `policy.allowed_scopes` | up to 128, each up to 384 of `A`-`Z`, `a`-`z`, `0`-`9`, `:/_.*@~-`, starting with a lowercase letter: the characters of every scope the protocol knows, the longest of which (`pkg:<location>/<@scope/name>`) has 353 | none |
  | `policy.default_scope`, `policy.auto_scope` | empty, or a scope as above | empty |
  | `policy.auto` | empty, or as a verb | empty |
  | `policy.apps` | up to 64; the id as `agent.yaml` takes it, the name as a fact | none |
  | `policy.window` | the window `check-config` takes - day names, a day repeated too, start and end `HH:MM` and not equal - with at most 64 entries in its days | absent |
  | `policy.tz` | empty, or up to 64 of `A`-`Z`, `a`-`z`, `0`-`9`, `._+/-`, starting with a letter | empty |
  | `policy.min_interval_h` | 0 to 168 | 0 |
  | `policy.reboot_window`, `policy.reboot_window_only` | ADR-0187, its days bounded as those of `policy.window` | absent |
  | the switches, `last_install` | their JSON type | - |

* **Codes:** `too_long` (a text or name longer than its bound), `too_many` (a list with more entries), `characters` (a
  control character, or one outside a name's set), `value` (a number outside its range); for the install window the
  codes of the reboot window: `days` (more than 64 entries) and `window` (a window `check-config` refuses).
* **Characters, not known values:** a verb, a scope and `install.auto` are bounded by their characters, not by the
  values this hub knows. The hub acts only on values it knows; a newer agent's verb or scope is kept and offers nothing
  until a hub knows it.
* **What a dropped field means:** what it means for an agent that does not report it.
  * Without `goos` or `arch` the hub does not know the host's platform, as for an agent before `goos` (design 10.4).
  * Without the install window the hub plans no automatic install for the host, and it cannot tell that the window is
    closed: an install from Home Assistant goes out without the button's reason and the agent refuses it outside its
    window, and for a guest the hub orders the backup before it
    ([ADR-0109](0109-the-install-button-installs-at-once.md), [ADR-0096](0096-fresh-backup-right-before-the-install.md)).
  * Without the minimum interval only the agent checks it
    ([ADR-0110](0110-the-hub-checks-the-minimum-interval-before-the-backup.md)).
  * Without `runs` the hub keeps its guess about the host's run and sends no acknowledgements, as for an agent before
    ADR-0114.
* **Room:** every bound lies well above what agents send. The agent's own builder of the hello is checked against the
  bounds over a real tunnel: with the facts and the time zone of the machine the test runs on, `agent.example.yaml` and
  a configuration with every part of the policy - app ids and image scopes as long as `check-config` takes them, both
  windows with a day repeated, the install window with eight entries -, the versions `build.sh` gives, every verb, jobs
  running and results held.

Option 1 takes the whole host away for one field: a 65th app or an unusual OS name would end its checks, its installs
and the agent update that could put it right. Option 3 ends the connection without a code - the agent sees a closed
connection, not a reason - and still lets through what fits: control characters, an overflowing interval, a window of
thousands of days fit in 64 KiB; the bounds per field are needed anyway, and the 4 MiB limit stays as the outer one.
Option 4 makes a shortened app list or name look like the agent's own and hides that something was cut; a shortened
scope list can offer other buttons than the host's policy. A whole field dropped is recognizable, and the event says
so. Option 5 lets control characters and terminal escape sequences reach Home Assistant and the command line.

### Consequences

* Good, because what a hello brings is bounded to a few tens of KiB per host before the register keeps it, whether the
  host is approved or not, and Home Assistant and `vordr-hub` show only text without control characters.
* Good, because a field out of bounds costs only that field, and the event names it with a fixed code and the key.
* Good, because existing agents keep every field, and a newer agent's verbs and scopes are kept.
* Bad, because `check-config` does not know every bound. These pass it, and the hub drops the field, with the event:
  * more than 64 apps, an app name over 255 bytes, or more than 128 allowed scopes;
  * an app name with a control character - a tab, a line break -, where `check-config` only asks for a name: the hub
    drops the whole app list, and Home Assistant offers no app button for the host;
  * more than 64 entries in the days of a window: the hub drops that window. Without the install window, as above, the
    host gets no automatic installs, and installs from Home Assistant go out while the window is closed and are refused
    by the agent, for a guest after a needless backup. Without the reboot window, Home Assistant does not say when a
    reboot runs (ADR-0187).
* Bad, because a hello of up to 4 MiB is still read whole before it is bounded: an unauthenticated connection can make
  the hub parse that much once, as before.
* Neutral: what the register holds from an earlier hub stays until the host's next hello replaces it;
  `vordr-hub forget` removes a host that does not come back.
* Neutral: reports, inventories, job status messages and the hello of a Home Assistant client are other surfaces, with
  bounds of their own or none; this decision does not cover them.

### Confirmation

* `internal/hub/hello_test.go`:
  * `TestEveryFieldOfAHelloIsBounded` finds every field of the hello by itself, a field added later too. From a hello
    the hub takes whole, each field set far beyond any bound is refused as identity, bounded by its type (a switch, a
    time), or not taken. A new field fails there until it has its bound.
  * `TestTheHubBoundsEveryFieldOfAHello`: a hello with every field at its bound is taken as sent. One step beyond, a
    megabyte, a hundred thousand entries, control characters, invalid UTF-8, a path, capitals, a number out of range:
    that field alone is dropped, to the value of an agent that does not report it, with its code.
  * `TestTheHubTakesWhatAgentsSend`: the versions `build.sh` gives, no version, the facts of a device and of Windows, the
    verbs of today, no runs, an empty policy, the scopes of `agent.yaml` and of the protocol (`@`, `~`), a day twice
    and eight entries in a window, umlauts, zones with `_`, `-` and `+`.
  * `TestAHelloWithFieldsOutOfBoundsConnectsWithoutThem`: over a real tunnel, a hello with a megabyte of version and OS,
    ten thousand apps and an overflowing interval connects; the register, the host view and the event `hello` have the
    fields dropped, one event `hello_fields_dropped` names them with the fingerprint; a hello in bounds causes none.
  * `TestARefusedHelloLogsNoDrops`: the same oversized hello from another key under a known name, from a rejected host
    and with no room for another pending one logs neither `hello_fields_dropped` nor `reboot_window_refused`; from a
    host the register takes, both, with its fingerprint.
  * `TestAWaitingHostShowsOnlyBoundedFacts`: a host waiting for approval reaches Home Assistant without the escape
    sequence in its OS.
  * `TestAHelloWithItsIdentityOutOfBoundsIsRefused`: a node name at its bound is taken; one character more, a
    megabyte, capitals, a key or a signature too long, another type are refused with `bad_hello`, and nothing is
    stored.
* `internal/agent/hello_bounds_e2e_test.go` (`TestTheRealAgentsHelloFitsTheHubsBounds`): the real agent's hello, as
  under "Room", reaches the hub over a real tunnel with every field as sent, and neither `hello_fields_dropped` nor
  `reboot_window_refused` is logged.
* `internal/hub/rebootwindow_test.go`: ADR-0187's tests run on the window check the install window now shares: a day
  twice, eight entries and 64 entries are taken, 65 and a hundred thousand are not.
* The pull request's mutation probe removes each check once; the tests fail for each.

## More Information

* [ADR-0187](0187-the-agent-reports-its-reboot-window-and-home-assistant-says-when-a-reboot-can-run.md): the reboot
  window, the first bounded field.
* [ADR-0007](0007-fixed-codes-instead-of-error-texts.md): fixed codes.
* [ADR-0114](0114-agent-names-its-run-and-the-hub-acknowledges-results.md): `runs`.
* [ADR-0110](0110-the-hub-checks-the-minimum-interval-before-the-backup.md): the minimum interval at the hub.
* Design: [design/architecture.md](../../design/architecture.md) (6.2, 10.2, 12).
