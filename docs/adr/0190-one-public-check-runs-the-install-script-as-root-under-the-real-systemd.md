---
status: proposed
date: 2026-11-18
decision-makers: proposed by Claude (2026-11-18), for the maintainer to accept or reject
---

# One public check runs the install script as root under the real systemd

## Context and Problem Statement

"Add host" runs its install script on the new host as root, as a transient service:
`systemd-run --pipe --wait --collect ... -- sh -c <script> ...`
([ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md)). systemd expands a
service's command line before the shell sees it: each `${...}` becomes the service's variable or nothing. The script
held `${sum%% *}`, so on every host the computed hash was empty and the install ended with `hash_mismatch`. The tests
did not show it: their SSH host plays `systemd-run` with a stand-in that ran the command unchanged.

The stand-in now imitates the expansion, but an imitation differs from systemd wherever nobody thought of something.
Running the script under the real `systemd-run` needs a systemd manager: a person's own (`systemd-run --user`), where
one runs, or the system's, which needs root. GitHub's hosted runners have systemd and password-less sudo, but no user
manager for the runner's account.

Where does the install script meet the real systemd before it reaches a host?

## Decision Drivers

* The script that reaches hosts is tested under the systemd that runs it there, also in the public checks.
* A person's machine runs nothing as root unless the person asks for it.
* A green check means the test ran: a run that asked for it may not skip it silently.
* Nothing is installed: the test checks the script, not the agent.

## Considered Options

1. The stand-in only.
2. The user's own systemd manager only; skipped where there is none (the public checks).
3. As 2, and in the public checks the system's systemd as root through `sudo -n`, switched on by `VORDR_TEST_SUDO=1`
   in the workflow.
4. A container with systemd in the public checks.

## Decision Outcome

Chosen option: **3**.

* `ha/tests/test_install_under_systemd.py` runs `install_command` locally, against the fake hub's HTTPS download with
  its CA, with a stand-in for the agent binary that only checks its arguments.
* With `VORDR_TEST_SUDO=1` it runs as root through `sudo -k -n` in the system's systemd; without, in the user's
  manager through a `systemd-run --user` shim on `PATH`; without either it is skipped with the reason.
* Asked for with `VORDR_TEST_SUDO=1`, a missing precondition (no password-less sudo, no running system manager) fails
  the tests instead of skipping them.
* `tools/github-actions/checks.yml` sets `VORDR_TEST_SUDO=1` for the integration's step: the runner is a machine of
  its own for this run, with sudo.
* As root, only the install script and the agent stand-in run; the hub's CA goes into the test's own folder
  (`CA_PATH` set for the test), never to `/etc`.

### Consequences

* Good, because the public checks run the script as hosts do: as root, under the system's `systemd-run`.
* Good, because a developer with a user manager (WSL with systemd, a desktop session) runs it in the local gate without
  root.
* Good, because a public run that should have tested it cannot be green without having done so.
* Bad, because the public checks run part of a test as root. The runner is discarded after the run, and what runs as
  root is the repository's own script and an echoing stand-in.
* Bad, because the root path runs for the first time on GitHub, after a merge: the user-manager path runs in the
  local gate, the root path only there.
* Neutral: option 4 would test a systemd in a container, which differs from a host's in its own ways, and needs an
  image to keep current.

### Confirmation

* The test with the script from before ADR-0161's fix (`sum=${sum%% *}`) fails with `hash_mismatch` in the user's
  manager; with the fix it passes, and a served binary of the right size and another content ends with
  `hash_mismatch` and nothing run.
* With `VORDR_TEST_SUDO=1` on a machine where `sudo -k -n` does not run, the tests fail and are not skipped.
* `AGENTS.md`, "Running the checks", names the test, the variable and this ADR.

## More Information

* [ADR-0137](0137-checks-run-in-github-actions.md): the checks run in GitHub Actions.
* [ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md): the install over SSH.
