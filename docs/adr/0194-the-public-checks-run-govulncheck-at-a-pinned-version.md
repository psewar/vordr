---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, chose a step in the public checks over a run on the operator's machine); proposed by Claude
---

# The public checks run govulncheck at a pinned version

Extends [ADR-0137](0137-checks-run-in-github-actions.md) (the checks run in GitHub Actions) by one step.

## Context and Problem Statement

Hub, agent and the release signer are Go programs. `go vet` and the tests say nothing about known vulnerabilities in
the Go standard library of the toolchain or in the modules the code depends on. `govulncheck` (the Go team's tool,
`golang.org/x/vuln`) reports those whose vulnerable functions the code actually reaches, from the Go vulnerability
database. It needs that database over the network, while `./build.sh` runs offline (`GOPROXY=off`), and nothing ran it
so far.

Where and how does Vördr check its Go code for known vulnerabilities?

## Decision Drivers

* A known vulnerability that the code reaches is found without someone remembering to look.
* The local gate stays offline and reproducible.
* The tool that checks is a fixed version, not whatever is newest on the day.

## Considered Options

1. A step in the public checks (`tools/github-actions/checks.yml`): `go run golang.org/x/vuln/cmd/govulncheck@<pinned
   version> ./...`.
2. A step in `./build.sh`.
3. Run it by hand on the operator's machine before a release.
4. Rely on the hosting service's dependency alerts.

## Decision Outcome

Chosen option: **1**. The step runs after `build.sh` in the job `checks`, on every push and pull request of the public
repository, with the Go toolchain `go.mod` names. A finding that the code reaches fails the step and with it the
checks; modules that are only required, not reached, are listed without failing. The version is pinned in the
workflow and raised by hand, to a release that is some weeks old.

Option 2 would need the network in a gate that runs offline. Option 3 depends on someone remembering. Option 4 knows
the module list, not whether the code reaches the vulnerable function, and not the standard library of the toolchain.

### Consequences

* Good, because every snapshot is checked against the database of that day, also without a change of the code.
* Good, because only what the code reaches fails the checks.
* Bad, because a vulnerability published after a release can turn the checks red without a change; then the module or
  the toolchain in `go.mod` is raised before the next release.
* Bad, because the check runs only on the public repository, after a merge: a pull request is not checked before it.

### Confirmation

The step in `tools/github-actions/checks.yml` with a pinned version; `AGENTS.md` names it under "Running the checks".
