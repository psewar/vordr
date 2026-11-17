---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for a machine-readable list and show at the hub and for the stability question to be settled); proposed by Claude
---

# The hub's command line gives programs a versioned host view

## Context and Problem Statement

`vordr-hub list` prints one line per host for people: state, fingerprint, online, the counts of the last report.
Whatever else an operator's script needs (the last run and its result, the problem, since when a reboot or updates
are pending, the agent version and since when it runs, the policy) it can only get by reading the hub's register
`registry.json` directly or by parsing the table. The register is the hub's private store
([ADR-0011](0011-state-in-files.md)): its layout changes without notice, it lacks everything the hub computes (the
problem, online, what the install button would take), and reading it goes around the admin socket that decides who may
read ([ADR-0010](0010-administration-only-local.md)). A table made for people breaks scripts with every new column.

Two questions: what does the machine-readable view contain, and does it promise anything to the scripts that read it?

## Decision Drivers

* One place computes a host's state: the view Home Assistant gets (`internal/hub/view.go`); no second calculation.
* No second format to maintain beside the views that exist.
* A script must be able to tell when the layout changed under it, instead of misreading it without a word.
* Results travel as fixed codes, not texts ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)); the exit status
  says what happened.
* Only through the admin socket; nothing secret in the output.

## Considered Options

Content:

* **A:** the existing admin line `NodeView`, widened by what the register knows about the host, plus the host view of
  Home Assistant (`wire.HostView`) for an approved host.
* **B:** a schema of its own for the command line.
* **C:** the register entry as it is stored.

Promise:

* **1:** a stable interface with a version number in the document.
* **2:** "for people and scripts, may change": no promise.
* **3:** stable without a version number.

## Decision Outcome

Chosen options: **A** and **1**.

* **Commands:** `vordr-hub list --json` and `vordr-hub show <node> [--json]`. `show` is `list` narrowed to one host;
  without `--json` it prints the host in full for people. `--json` with any other verb is refused as a wrong command
  line.
* **Document:** both print one JSON document: `format`, `code`, `ha_protocol`, `hub_version`, `at` (when the views
  were computed, UTC) and `nodes`, a list sorted by name that is never null. The admin socket sends it in the answer of
  `list` and `show`; the command line prints it as it came.
* **One host:** `node`, `state`, `fingerprint`, `online`, `last_seen`, `agent_version`, `agent_since`, `os`, `facts`,
  `verbs`, `policy`, `first_seen`, `decided_at`, `pending_since`, `last_install`, the counts of the last report
  (`pending`, `security`, `reboot_required`, `report_at`) and `view`: the `wire.HostView` of an approved host exactly
  as Home Assistant gets it (problem, last run, reboot, updates, backup, packages, queue). A host that waits or was
  rejected has no `view`.
* **Version:** `format` is 1. Within a format, fields are only added. A field that goes away, is renamed or changes
  its type or meaning raises `format`. `view` follows the Home Assistant protocol instead: its layout changes
  incompatibly only together with `ha_protocol` ([ADR-0027](0027-protocol-2-codes-and-translations.md) is the last
  such change). A program checks both numbers and ignores fields it does not know. A field that is absent or null is
  unknown or does not apply.
* **Codes and exit status:** 0 `ok`; 1 the hub refused, and the document says why in `code` (`unknown_node` for a host
  the register does not know, `bad_request`), or `hub.yaml` has issues; 2 no usable answer: a wrong command line,
  `hub.yaml` not found, the admin socket does not answer, or the hub that runs is older than the command and its
  answer has no document (the binary was replaced, the service not yet restarted). With 2, stdout stays empty.
* **Not an interface:** the text of `list` and `show` for people; it may change with any release.
* **Nothing secret:** no public key (the fingerprint names it), no token, no signature. The register's bookkeeping for
  automatic installs (`pending_updates`, `clean_install_*`) and the raw last report stay out: they are the hub's
  internals, and promising them would freeze the register's layout.

Option B would be a third description of a host beside the register and the view, and it would drift from what Home
Assistant shows. Option C promises the register's layout to every script and still lacks what the hub computes.
Option 2 leaves the scripts where they are with `registry.json`: they break silently. Option 3 gives them no way to
notice the break.

### Consequences

* Good, because scripts read the state the hub computes, through the socket, and can rely on a number that tells them
  when the layout changed.
* Good, because what the command line shows of an approved host is what Home Assistant shows; there is nothing to
  keep in step.
* Bad, because the view's fields are now promised to two readers: a field Home Assistant no longer needs stays in the
  view until `ha_protocol` changes.
* Bad, because every `list` computes the full view of every approved host, and the answer carries all of it; the
  socket is local, and Home Assistant gets the same views on every change anyway.
* Neutral: the answer of `list` on the admin socket moved from `nodes` into `hosts`; the socket itself is no interface,
  and the command line and the hub are one binary.

### Confirmation

* `internal/hub/admin_hosts_test.go`: `TestListAnswersTheHostDocument` (an approved, a waiting and a rejected host:
  the document's head, every host's fields, the view only for the approved one and equal to the view Home Assistant
  gets, no field that names a key, token or secret and no public key in any encoding), `TestListWithoutHosts`
  (`nodes` is `[]`), `TestShowOneHost` (each host, `unknown_node`, `bad_request`, the document also when refused).
* `cmd/vordr-hub/admin_test.go`: `TestParseArgsJSON` (`--json` only with `list` and `show`),
  `TestListAndShowOverTheSocket` (documents and texts over a real admin socket, exit status 0, 1 and 2),
  `TestOlderHubGivesNoHostView` (exit 2 and nothing on stdout against a hub without the document).
* `ha/tests/test_e2e_real_hub.py`: the real hub binary's `list --json` while the agent waits, and `show --json` with
  the job Home Assistant started.
