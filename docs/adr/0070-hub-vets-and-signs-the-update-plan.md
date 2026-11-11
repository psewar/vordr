---
status: accepted
date: 2026-10-30
decision-makers: Claude (proposal, from kay's requirement "never install a version that OSV knows"); accepted by kay with the merge
---

# The hub vets the whole update plan and signs its answer

## Context and Problem Statement

The operator: an update must never install a version that OSV knows. But the OSV data lives at the hub
([ADR-0064](0064-osv-data-local-at-the-hub.md)), and only pip and npm know which dependencies an update brings along.
How do both come together without anyone between agent and hub being able to wave a flagged version through?

## Decision Drivers

* Malicious versions often sit in a dependency, not in the package itself (`debug` 4.4.2 in the tree of many
  packages).
* Package names and versions never come from the hub; the agent decides.
* Jobs are signed because they act as root (`design/architecture.md` 4); the same applies to an approval.

## Considered Options

1. Check only the target package (the hub already knows the candidate) and let dependencies resolve.
2. The hub sends the list of flagged versions with the job.
3. The agent builds the whole plan without installing and has the hub check it; the hub answers with a signed
   statement; exactly this plan is installed.

## Decision Outcome

Chosen option: **option 3**. Message `vet` with the plan (name and version of every entry that would be added), answer
`vet_result` signed with the hub key (context `vordr/vet/v1`, bound to job and host, at most 5 min old). The agent only
accepts it if it names every entry of the plan and no other. Then it installs exactly the plan: pip `--no-deps` with
all versions pinned, npm with the same `--before`.

### Consequences

* Good, because even a malicious dependency stops the update before anything is installed (`precheck_version_flagged`).
* Good, because without data, with stale data or without an answer there is no update (`precheck_vetting_unavailable`).
* Good, because the hub only answers for a running `pkg:` job of that host.
* Bad, because an update needs the hub; without it nothing works, not even locally.
* Bad, because for npm the agent relies on the same `--before` limit giving the same resolution.

### Confirmation

`TestVetSignature`, `TestVetAndUpdatable`, `TestPipUpdate`, `TestNpmUpdate`, end to end `TestPackageUpdateThroughTheHub`
(debug 4.4.2 in the plan → refused, clean plan → ok); mutation probe 79/79.

## Pros and Cons of the Options

* **Option 1:** simple; dependencies unchecked.
* **Option 2:** the hub would have to know all versions of all possible dependencies in advance.
* **Option 3:** checks exactly what is installed; one more round trip through the tunnel.

## More Information

`design/architecture.md` 11.4.

Merged in German as ADR-0106 (status "proposed", accepted with the merge); later renumbered, translated and set to
accepted.
