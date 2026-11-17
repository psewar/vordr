---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for an easy setup through Home Assistant that reaches the device once over SSH and keeps no SSH credentials; this command is its first step); proposed by Claude
---

# The Linux agent installs itself from one binary and one command

## Context and Problem Statement

Putting the agent on a Linux host takes the binary, the `deploy/` folder (`install-agent.sh`, two systemd units,
`rollback.sh`) and an `agent.yaml` written by hand with the hub's address, the hub key and the operator keys. The only
example policy (`agent.example.yaml`) shows a full policy with placeholders and does not serve as a start. Windows has
`vordr-agent install-service`, which sets the agent up from the binary alone; Linux has nothing alike.

Setting a host up through Home Assistant, which reaches the host once over SSH and keeps no credentials, needs exactly
one command that can run there unattended: it must check everything before it changes anything, and tell a program
how it ended without that program reading prose.

## Decision Drivers

* One binary and one command on the host; nothing else to bring along.
* Safe to run unattended: every check before the first change, and a refusal changes nothing; an interrupt or a
  reader that goes away does not leave the host half installed without saying so.
* Nothing another user controls reaches what runs as root: not the environment of the caller, not the place of the
  installer's files, not the CA the agent will trust.
* The outcome as fixed codes ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)): exit code and a last line a
  program reads, never only prose.
* The local policy stays closed by default and has the last word
  ([ADR-0006](0006-local-policy-decides.md), [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)): the command
  must not widen it, and must not overwrite a policy the operator has changed.
* One installer: `install-agent.sh` is tested and in use; a second implementation would drift from it.
* No new trust: the approval at the hub ([ADR-0010](0010-administration-only-local.md)) and the signed releases
  ([ADR-0032](0032-self-update-signed-by-operator.md)) stay as they are.

## Considered Options

1. `vordr-agent install` writes a minimal `agent.yaml` from its flags and runs `install-agent.sh`, embedded in the
   binary.
2. The same command, with the install written anew in Go, as `install-service` does on Windows.
3. A bootstrap script fetched from the hub or the repository (`curl … | sh`) that brings the `deploy/` folder.
4. Policy templates to choose at install time (only check, updates by button, …).

## Decision Outcome

Chosen option: **1**.

* **Command:** `vordr-agent install --hub <wss-url> --hub-key <base64> [--node <name>] [--ca-file <path>]
  [--operator-key <base64> [--backup-operator-key <base64>]] [--replace-config "<reason>"]`, as root. The flags are
  named after the keys they fill (`hub.url`, `hub.public_key`, `node`, `hub.ca_file`, `agent_update.public_key`,
  `agent_update.backup_public_key`); `--replace-config` is the option of `install-agent.sh` and `install-service`.
* **Generated `agent.yaml`:** the node (default: the host name in lower case, as the agent's own default, but
  written out), the hub section and, only with `--operator-key`, `agent_update` with `enabled: true` and the key(s).
  Nothing else: the strict defaults apply, the agent checks and reports, installs no updates and never reboots.
  Widening the policy stays the operator's edit of `agent.yaml` on the host. A relative `--ca-file` becomes absolute.
  Giving the operator key is the operator's statement that this host takes the releases signed with it; without the
  flag self-update stays off, as before.
* **Checks, in this order, before anything changes:** the command line; Linux; root; systemd running; `apt-get`
  present (looked up in the system's folders only); the generated `agent.yaml` passes the same validation as
  `check-config` (each issue names the flag behind it); a `--ca-file` that only root may change: the file and every
  folder above it belong to root and nobody else may write to them (`ca_file_unsafe` otherwise, naming
  `/etc/vordr/hub-ca.pem`, where Home Assistant's install over SSH puts the CA too); the agent trusts that CA for the
  hub at every start, so a CA in a home folder or under `/tmp` could be swapped or be gone after a reboot; an
  installed `/etc/vordr/agent.yaml` that differs is replaced only with `--replace-config` (the difference is shown);
  the binary that runs is still the file at its path; the hub answers (the stages of `hub-check`).
* **Install:** the embedded `install-agent.sh`, both units and `rollback.sh` (package `deploy`, `//go:embed`) and the
  generated `agent.yaml` go into a private folder (0700) under `/run`, removed again in every case. Not under
  `$TMPDIR`: root's environment may come from another user (`su` without `-`, `sudo -E`), whose folder would let that
  user swap the script before `bash` reads it; `/run` itself must be root's and not writable by others
  (`temp_dir_unsafe` otherwise). `bash` runs the script from there with the binary that runs and that `agent.yaml`;
  `--replace-config` passes through. The script gets a fixed environment, `PATH` of the system's folders, `LANG=C` and
  `LC_ALL=C`, and nothing of the caller's (`BASH_ENV`, `SHELLOPTS` with `PS4`, a `PATH` with relative folders would
  run the caller's code as root). Its output is passed on and dropped once the reader is gone, so that a closed pipe
  does not end the script at its next line. The binary is not copied into the folder: a temporary folder may be
  mounted `noexec`, and whoever could swap the binary at its path could have swapped it before root started it. That
  is why the way by hand compares the binary's SHA-256 with the signed manifest before it runs as root
  (`deploy/README.md`); a flag that has the running binary check its own hash would prove nothing, as a binary that is
  not the operator's would skip it.
* **Interrupts:** SIGINT, SIGTERM and SIGHUP reach `install` instead of ending it without a last line. Before the
  script starts, `install` stops and refuses (`interrupted`, exit 2). While it runs, `install` passes the signal on
  and waits: the script ends once its running command has (its trap), with its exit saying whether it had changed
  anything, and `install` reports `interrupted` as refused (2) or failed (1) accordingly.
* **Outcome:** exit 0 installed, 2 refused with nothing changed, 1 failed after the first change. The last line is
  `result=installed node=<node> fingerprint=<hex>`, `result=refused code=<code>[ stage=<stage>]` or
  `result=failed code=<code>`. Codes: `usage`, `not_linux`, `not_root`, `no_systemd`, `no_apt`, `config_invalid`,
  `ca_file_unsafe`, `config_differs`, `hub_unreachable` (with the stage that failed), `prepare_failed`,
  `temp_dir_unsafe`, `installer_refused`; `install_failed`, `identity_unreadable`; `interrupted` in either class. The
  fingerprint comes from the identity the started service made, read without creating one. Before it, the script
  prints the approval command for the hub as before.
* **`install-agent.sh` says whether it changed anything:** exit 2 for a refusal and for any command that fails before
  the first change, exit 1 after it (a trap on errors keeps that true for unexpected failures, and the same trap on
  SIGINT, SIGTERM and SIGHUP for an interrupt). Exit 1 used to stand for both, so a caller could not tell a refusal
  from a half-done install without reading its text.
* **Windows:** `install` refuses with `not_linux` and names `install-service`, which stays the way there.
* **Not part of this decision:** a token that admits the host at the hub without comparing fingerprints, and getting
  the binary onto the host. Both are steps of their own; the approval at the hub stays as in ADR-0010.

Option 2 would make two installers for the same host to keep in step; the script is also used by hand and for proxy
instances. Option 3 runs an unverified script as root, which ADR-0032 rules out for updates, and still needs a policy
written by hand. Option 4 is left for later: a template is a policy decision of its own, and the closed default needs
none.

### Consequences

* Good, because a host needs only the binary and one command, and a program running it reads the outcome from the exit
  code and the last line.
* Good, because a refusal changes nothing and says why, also for a policy the operator has widened on the host: the
  command does not reset it.
* Good, because the install itself is the one that is already tested, now with an exit code that says whether the host
  changed.
* Bad, because `hub-check` runs twice (once in the command, once in the script) and the hub key is not proven: the hub
  does not sign its challenge yet, so a wrong `--hub-key` shows only when the first job is rejected.
* Bad, because the generated `agent.yaml` allows nothing beyond checks; whoever wants updates by button edits it on the
  host, as before.
* Bad, because a hub CA has to be in a place only root may change before `install` takes it; the refusal says where
  and how.
* Bad, because the script no longer sees the caller's environment: a proxy that `hub-check` would need from the
  caller's `https_proxy` is not used, as the agent's service does not see it either.
* Neutral: only hosts with apt and systemd; the script's other refusals (e.g. a proxy instance whose policy the new
  binary rejects) come back as `installer_refused`.

### Confirmation

* `cmd/vordr-agent/install_agent_test.go`: `TestInstallTakesItsFlags`, `TestInstallUsageProblems`,
  `TestInstallPolicy` (minimal and full `agent.yaml`, strict defaults, `check-config` accepts it),
  `TestInstallRefusesAndChangesNothing` (every refusal code, also a CA file under a folder of others, writable by
  group, in a folder others may write and of another user, and a private folder's place writable by others, of
  another user or a symbolic link; nothing under the system paths changes, no private folder left, the hub asked
  only after the host's checks), `TestInstallSetsUpTheHost`, `TestInstallReplacesTheConfigWithAReason`,
  `TestInstallFailureAfterAChange`, `TestInstallUnexpectedFailureAfterAChange`, `TestInstallWithoutIdentity`,
  `TestInstallTakesACAFileOnlyRootMayChange`, `TestInstallGivesTheInstallerAFixedEnvironment` (`BASH_ENV`, `ENV`,
  `SHELLOPTS` with `PS4` and another variable of the caller do not reach the script and run nothing),
  `TestRealHostIgnoresTheCallersEnvironment` (`/run` and the system's `PATH` whatever `TMPDIR` and `PATH` say),
  `TestInstallInterruptedBeforeTheInstaller`, `TestInstallInterruptedWhileTheInstallerRuns` (before and after the
  first change) and `TestInstallGoesOnWhenItsReaderIsGone`, against the stand-ins of `deploy/deploytest`.
* `deploy/install_agent_test.go`: `TestInstallRefusalsExitTwoAndChangeNothing`,
  `TestInstallFailureAfterAChangeExitsOne`, `TestInstallUnexpectedFailureAfterAChangeExitsOne` (a command that fails
  after the first change ends the script through its trap with exit 1), and
  `TestEmbeddedAgentFilesAreWhatTheScriptNeeds` (every file the script takes from next to itself is embedded).
* A mutation probe takes back each of these protections on its own (the private folder's place and its check, the
  fixed environment and `PATH`, the trap's exit code, the signal passed on, the trap on signals, the check before the
  script, the output passed on, the CA file's check); a test fails for every one.
* `internal/identity/identity_test.go`: `TestLoadNeverCreates`.
