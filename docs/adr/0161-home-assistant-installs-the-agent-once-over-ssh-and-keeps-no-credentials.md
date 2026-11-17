---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for an easy setup through Home Assistant that reaches the device once over SSH and stores the SSH data nowhere; accepted with the merge); proposed by Claude
---

# Home Assistant installs the agent on a new Linux host once over SSH and keeps no credentials

Builds on [ADR-0023](0023-own-ha-integration.md) (Home Assistant is an approved client of the hub),
[ADR-0039](0039-ha-client-roles.md) (its roles), [ADR-0010](0010-administration-only-local.md) (approval only at the
hub), [ADR-0032](0032-self-update-signed-by-operator.md) (agent releases signed by the operator) and
[ADR-0157](0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md) (a waiting host is a repair). It uses two
building blocks decided on their own: the agent's `vordr-agent install` (one binary and one command set a Linux host up,
with a last line for programs) and the hub's `agent_download` (a short-lived, single-use download of the signed agent
release for an approved client with role `admin`, only to a client whose hub announces the feature,
[ADR-0067](0067-hub-announces-features.md)).

## Context and Problem Statement

Setting a host up takes the agent binary for its processor, the hub's address and key, the operator key for the
agent's self-update, and root on the host. With `vordr-agent install` this is one command, but someone still has to
fetch the right binary, check it, copy it to the host, look up the hub key and type the command as root. The
maintainer asked for setting a host up from Home Assistant: Home Assistant reaches the device once, over SSH or
similar, and stores the SSH data nowhere; apart from that first connection, everything works as before.

How does Home Assistant set up a host, and what does it hold, for how long?

## Decision Drivers

* In steady state nothing changes at the trust boundaries: approval stays at the hub, jobs are signed by the hub,
  releases by the operator, the agent's local policy stays closed by default.
* Nothing of the SSH access is kept: not in the config entry, its options, Home Assistant's storage, the logs, the
  diagnostics or an exception.
* The binary that runs as root on the new host is the operator's signed release, checked against a key the person
  entered in Home Assistant, never one from the hub: a hub that was taken over must not be able to slip in a binary.
* The person confirms the host's SSH key before anything secret is sent.
* Every step checks its precondition first and ends with a fixed code that has a text and a next step
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)); failures of the network or the host become codes at one
  boundary.
* A host whose key the person confirmed is still not trusted with Home Assistant's memory, and other users on that
  host must not see the single-use download token.
* An install either does not start or runs to its end on the host, whatever happens to the dialog or the connection.
* Linux with systemd and apt first, as `vordr-agent install` supports.

## Considered Options

1. Home Assistant connects once over SSH with credentials entered in its options flow and held only in the flow's
   memory; it checks the release, has the host download and check the binary, and runs `vordr-agent install`.
2. Home Assistant only shows a one-line command that the person runs on the host as root.
3. A hypervisor's agent installs into its guests (guest agent or container exec), without SSH.
4. Home Assistant stores SSH credentials per host, to set it up again or repair it later.
5. The hub connects over SSH instead of Home Assistant.

## Decision Outcome

Chosen option: **1**, Linux only.

* **Operator keys** are an item of their own in the integration's options (`operator_keys`: the key and an optional
  backup, base64 Ed25519 as `vordr-release public-key` prints them). They are public and stored like any option.
  Without one, the install item says why it cannot run. They are never taken from the hub.
* **The install item** ("Install the agent on a Linux host (SSH)") is a flow of its own, with no options to store:
  1. *Before anything is entered:* the SSH library is installed, the entry runs, an operator key is stored, the hub is
     connected and its status names `agent_download`; otherwise it ends with a code.
  2. *Target:* host, SSH port, user, node name (empty: the host's own name, as `vordr-agent install` defaults it).
  3. *Host key:* fetched without logging in, shown with its algorithm and SHA256 fingerprint as `ssh-keygen -l`
     shows it. The person compares it with the host's own and confirms or stops. The login afterwards accepts exactly
     this key; nothing is written to a `known_hosts` file. A changed key at the login ends the flow before a password
     is sent.
  4. *Credentials, only now:* a password or a pasted private key (with its passphrase), and sudo's password (empty:
     the login password). A wrong login, a key that cannot be read or a sudo that refuses is shown in the form to
     correct; the secret fields are never prefilled.
  5. *Probe*, as the login user, reporting `key=value` facts: Linux, the processor mapped to the manifest's platform
     (`x86_64` → `amd64`, `aarch64` → `arm64`, `armv7l`/`armv8l` → `arm`), systemd, `apt-get`, `curl` or `wget`,
     `sha256sum`, `base64`, `mktemp`, `timeout`, `systemd-run`, root or `sudo`. Then how to become root:
     `sudo -k -n true`, else `sudo -k -S` with the password on stdin. `-k` makes the check and the install ask the
     same way.
  6. *The release:* `agent_download` for `linux/<arch>`. Home Assistant checks the operator's signature over
     `vordr/agent-manifest/v1\0` + manifest with the stored keys, reads the manifest as strictly as the agent does,
     and takes file, SHA-256 and size only from it. The answer's download path must open exactly that file; the hub key
     must be a key. Every manifest the operator ever signed stays valid, so the release must also not be older than the
     newest release an install of this entry took before (`release_too_old`): Home Assistant keeps that version
     (`.storage/vordr.agent_release.<entry_id>`, removed with the entry) and compares as an agent on a release does for
     its self-update (`NewerRelease`: X.Y.Z only, the same release again is taken, a hand build is refused once a
     release was taken).
  7. *On the host, as root, as a transient service of its own:* `systemd-run --pipe --wait --collect` runs the install
     outside the login's session, with stdin and stdout passed through. Closing the connection, or systemd-logind
     ending the session's processes (`KillUserProcesses=yes`), does not stop it halfway; it finishes on its own. In it:
     a private temporary folder (`mktemp -d`, umask 077, refused if nothing may run from it); the binary fetched with
     `curl` or `wget` over HTTPS from the hub's address (the integration's host and port), trusting the hub's own CA
     if the integration has one. The download URL, which carries the single-use token, comes on stdin and goes into a
     file in that folder, which the tool reads (`curl -K`, `wget -i`); it is never an argument, which every user of
     the host sees in `ps`. The download is bounded by the signed size (`curl --max-filesize`, and `ulimit -f` for both
     tools: `download_too_large`) and in time (`curl --max-time 900`, `timeout 900` for `wget`). Size and SHA-256
     are compared with the signed manifest before anything runs. The hub's CA goes to `/etc/vordr/hub-ca.pem` for the
     agent; one that is there already is compared, never overwritten, and one the install put there goes again if
     `vordr-agent install` refuses. Then `vordr-agent install --hub wss://<host>:<port>/v1/tunnel --hub-key <from the
     hub's answer> --operator-key <the stored key> [--backup-operator-key] [--node] [--ca-file]`, its output kept on
     the host, its last line read as fields. The temporary folder goes in every case, by a trap.
  8. *The end:* on success the agent's fingerprint and that the host now waits for approval at the hub, where the
     repair of ADR-0157 names the command. Otherwise the code with a text that says what changed on the host and what
     to do next.
* **What is read from the host:** only `key=value` fields, and of each command at most 64 KiB of stdout; stderr is not
  read. A host that prints more ends the flow with `host_output_too_long`.
* **What is held, and how long:** the credentials, the open connection and sudo's password live in the flow object
  only. The login password and the key are handed to the SSH library once, when it asks for them (a callable and the
  client object), and are not kept past the login, neither by the flow nor by the open connection's options. sudo's
  password and the download path (it carries a single-use token) go over stdin, never on a command line, which `ps`
  shows and sudo logs. Everything is dropped when the flow ends in any way: done, aborted, or the dialog closed.
  Closing it during the login closes the connection at once and nothing starts: the flow object is marked as ended,
  and the login and the install check that mark after connecting, before asking the hub and before running anything
  on the host (`install_cancelled`). Closing it during the install cancels the task and closes its connection; the
  host finishes on its own and removes its temporary folder. Python frees memory only once nothing refers to it;
  "dropped" means no reference is left.
* **SSH library:** `asyncssh`, required in `manifest.json` as a range (`>=2.24.0,<3`) that holds the version Home
  Assistant pins for its own `sftp_storage` integration, so that both install the same copy. It is imported only when
  the install item is opened, outside the event loop; without it the rest of the integration loads and the item says
  so (`install_needs_ssh_library`). It reads no `~/.ssh` files, uses no SSH agent and no default keys.

### Threat comparison

| Situation | Before | With this decision |
|---|---|---|
| Steady state | Home Assistant can only ask for allowed jobs (ADR-0039) | Unchanged: it keeps no SSH data; the host trusts the hub key and the operator key as after an install by hand; approval stays at the hub |
| While a person runs the flow (minutes) | – | Home Assistant holds root-capable access to this one host. A compromised Home Assistant, or code running in it, could run anything on that host at that moment, and could have read the credentials as they were entered. New, bounded to one host, the time of the flow, and a person starting it |
| A hub that was taken over | Signs jobs; can withhold a release, not forge one | Same: it can withhold the release or refuse; a binary it slips in fails the signature check in Home Assistant or the size and hash check on the host before it runs, and a longer one is stopped at the signed size. It cannot hand out an older release, validly signed but perhaps with a known weakness, once an install took a newer one (`release_too_old`); before the first install through Home Assistant it can, as at an install by hand. It hands out the hub key, which it holds anyway |
| A hostile or broken host, its key confirmed | – | It learns what any login to it shows: the login and sudo's password. It cannot fill Home Assistant's memory (64 KiB per command, stderr not read). The download token is single-use and only for this host's platform |
| Other users on the host | – | They do not see the download token: it is never an argument of a process. They see that an install runs |
| The dialog closed or the connection lost | – | During the login: nothing starts. During the install: the host finishes as a service of its own, also with `KillUserProcesses=yes`; the dialog does not show the result, the host's `systemctl status vordr-agent` does |
| Someone between Home Assistant and the host | – | Stopped by the person's comparison of the host key; the login accepts only the confirmed key |
| Someone between the host and the hub | – | Download over HTTPS with the hub's CA; whatever arrives, only the signed hash runs |
| Credentials used elsewhere too | – | They pass through Home Assistant once. The texts advise a key or a password for this purpose; nothing keeps them |

### Why not the others

* **Option 2, a one-line command:** no trust change at all, the person's own shell. But the signature check would
  have to happen on a bare host, where shell tools have no Ed25519 check, and the command would carry a single-use
  download path into the shell's history. It stays the way by hand (`deploy/README.md`) and may become a second item
  later: Home Assistant would check the release and show a command with the expected hash.
* **Option 3, through a hypervisor's agent:** works without credentials, but gives that agent a standing power to run
  commands as root in every guest, a new verb with its own policy, and covers only guests of supported hypervisors.
  A decision of its own if wanted.
* **Option 4, stored credentials:** turns Home Assistant into a standing store of root access to every host;
  exactly what the maintainer ruled out.
* **Option 5, the hub over SSH:** the hub is the most valuable target and today never holds credentials of hosts; it
  signs jobs and should not also log in as root.

### Consequences

* Good, because a new Linux host is set up from Home Assistant without a shell, with the same checks as by hand plus
  the signature check in Home Assistant, and ends in the same place: waiting for approval at the hub.
* Good, because nothing of the SSH access outlives the flow, and every way it can end has a code and a text.
* Bad, because during the flow Home Assistant holds root-capable access to the host (threat comparison above).
* Bad, because the integration gets a requirement, `asyncssh`, which Home Assistant installs when it loads the
  integration.
* Bad, because going back to an older agent release through Home Assistant means publishing its code under a new
  version, as for the agent's self-update; and because Home Assistant keeps one small store per entry for it.
* Bad, because the host needs `systemd-run` with `--pipe` and `--collect` (systemd 236 or newer) and `timeout`; both
  come with systemd and coreutils on the hosts this supports.
* Bad, because the agent's address of the hub is derived from the integration's (same host and port, path
  `/v1/tunnel`, as the hub serves both). Where agents reach the hub under another address, `vordr-agent install`
  refuses with `hub_unreachable` and the host is set up by hand.
* Bad, because only Linux with systemd and apt; Windows keeps `vordr-agent install-service`.
* Neutral: the operator key is one more value to enter in Home Assistant; it is public.

### Confirmation

`ha/tests/test_agent_install.py` drives the options flow against a fake hub and a real SSH server
(`ha/tests/fake_ssh_host.py`) whose commands run the integration's scripts in a sandbox with stand-ins for `uname`,
`id`, `sudo` and `apt-get`: success with a password and sudo (with `curl` and with `wget`), as root with a key that has a
passphrase and a release signed with the backup key; host key not confirmed (no login attempt) and changed after
confirming (no password sent); a wrong password corrected; sudo refused, wrong or asking for a password; no sudo;
not Linux, an unknown processor, no systemd, no apt, no download tool, a missing tool; the hub without the feature,
not connected, and refusing (`role_forbids` with the client's name, others, an unknown code); a release signed with
another key, changed after signing or without the platform (nothing reaches the host); a binary of another size or
hash (not run); a download refused by the hub; another CA on the host; `vordr-agent install` refused and failed with
the CA removed or kept; closing the dialog during the install. In every case the host's temporary folder is empty
afterwards, and no credential or token is in the logs, the entry, its options, the diagnostics or the storage.
The same tests cover what a security review of this decision found: closing the dialog during the login (the
connection closes at once, the hub is not asked, nothing runs on the host) and ending the install while the hub
answers (nothing runs on the host); a host that floods the probe (`host_output_too_long`) and `ssh.run` at exactly
64 KiB, one more, a flood on stderr and a timeout; an older release, a hand build and the same release after an
install took one, and the store going with the entry; a hub that sends 20 MiB more than signed, with `curl`, with a
`curl` that ignores `--max-filesize` and with `wget` (stopped at the signed size); every process's arguments on the
host recorded, without the token or a password; the install going through `systemd-run` and the probe requiring it;
an operator key and a hub answer with a character outside ASCII; the open connection holding neither the login
password nor the key; the integration loading without `asyncssh`, and the item saying so.
`ha/tests/test_agent_install_parts.py` checks the release check, the release age, the reading of facts and outcomes,
the addresses and the download URL, the quoting, the scripts' syntax and the script's refusal of a URL with a
character curl's config file would need quoted, the texts, and the constants against the Go code. A mutation probe
(the signature check, the size and hash check, the cleanup, the CA's removal, the host key pinning, the feature check,
secrets in a log, an option or a form default, and each of the review's fixes, among others) makes at least one of
these tests fail.
