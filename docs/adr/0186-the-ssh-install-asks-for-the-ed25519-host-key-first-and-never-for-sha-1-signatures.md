---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, accepted: where an SSH server offers only ssh-rsa, deploy/install-agent.sh sets up the agent); proposed by Claude
---

# The SSH install asks for the ED25519 host key first and never for SHA-1 signatures

Refines [ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md) on one point:
which of a host's keys the person is shown and compares. Everything else about the host key stays: fetched once
without logging in, shown with its SHA256 fingerprint, confirmed by the person, then the only key the login accepts,
nothing written to a known_hosts file.

## Context and Problem Statement

An SSH server usually holds several host keys (OpenSSH creates ED25519, ECDSA and RSA keys), and which one it presents
depends on the client's list of host key algorithms: the first one in the client's order that the server has. The
install fetches the key with asyncssh, whose default list starts with RSA (`rsa-sha2-256`, then `rsa-sha2-512`,
`ssh-rsa`, ... and ED25519 further down). So the dialog showed the RSA key of a host that also has an ED25519 key, while
its text told the person to compare with `/etc/ssh/ssh_host_ed25519_key.pub`. The fingerprints then differ although
nobody sits between, and a person who learns that differing fingerprints are "normal" stops comparing, which is the
one protection of this step.

The same default list also allows `ssh-rsa`, RSA signatures over SHA-1.

Which host key types does the install ask for, in which order, and how does the person know which file to compare with?

## Decision Drivers

* The key shown and the file named for the comparison belong together, for every host.
* The person compares the key a current OpenSSH prefers itself: ED25519.
* No signature scheme the install would not choose today (SHA-1) is asked for.
* Hosts the agent supports keep working: the agent needs systemd 236 or newer
  ([ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md)), from 2017; OpenSSH
  has offered RSA with SHA-2 (`rsa-sha2-256`, `rsa-sha2-512`) since 7.2, from 2016.
* The login accepts exactly the confirmed key, so it must negotiate the same type.

## Considered Options

1. Keep asyncssh's default order and name only the ED25519 file in the text.
2. Keep asyncssh's default order and name the file of the type shown.
3. Ask for ED25519, then ECDSA, then RSA with SHA-2 (`rsa-sha2-512`, `rsa-sha2-256`); not `ssh-rsa`, not certificates
   or security-key host keys; and name the file of the type shown.
4. Ask for ED25519 only.

## Decision Outcome

Chosen option: **3**.

* `ssh.HOST_KEY_ALGS` is the list, in that order: `ssh-ed25519`, `ecdsa-sha2-nistp256`, `ecdsa-sha2-nistp384`,
  `ecdsa-sha2-nistp521`, `rsa-sha2-512`, `rsa-sha2-256`. Both connections use it, fetching the key and the login, so
  the login negotiates the type the person confirmed.
* The dialog names the host's file of the type shown (`ssh-keygen -lf /etc/ssh/{key_file}`): `ssh_host_ed25519_key.pub`,
  `ssh_host_ecdsa_key.pub` or `ssh_host_rsa_key.pub`.
* A host that offers none of these types ends at the first step with `ssh_failed`, whose text now names the types
  the install accepts.
* The list is what the client asks for. asyncssh then checks the server's signature against the key, and for an RSA
  key it would also take a signature of the `ssh-rsa` kind; a server that answers `rsa-sha2-*` with that breaks the
  protocol. What protects the step is the pinned key: a signature of any kind still needs its private key.

### Consequences

* Good, because the key shown and the file named match on every host, so a differing fingerprint means something again.
* Good, because a host with an ED25519 key is compared on ED25519, as OpenSSH itself prefers.
* Good, because no host key signature over SHA-1 is asked for: a server that speaks the protocol signs with SHA-2.
* Bad, because a host whose SSH server offers only `ssh-rsa` (OpenSSH before 7.2, or one configured that way) cannot be
  added from Home Assistant; it gets the agent the other way, `deploy/install-agent.sh`, or a newer SSH server.
* Bad, because a host that only has a certificate or a security-key host key cannot be added this way either; both are
  rare on servers, and the person could not compare them with a plain file.
* Neutral: option 1 was the state before; option 2 would keep RSA first, so the person compares an RSA key on most hosts
  although they hold an ED25519 one; option 4 would shut out hosts with only ECDSA or RSA keys for no gain.

### Confirmation

* `ha/tests/test_agent_install.py`: a server with an RSA and an ED25519 key shows the ED25519 one, the dialog names
  `ssh_host_ed25519_key.pub`, and the install with that key succeeds
  (`test_the_ed25519_key_is_asked_for_first`; it fails with asyncssh's own list); a server with an RSA key only
  shows it and names `ssh_host_rsa_key.pub` (`test_a_host_with_an_rsa_key_only`); a server with a DSA key only ends
  at the first step with `ssh_failed`, nothing sent (`test_a_host_without_a_key_type_the_install_asks_for`).
* `ha/tests/test_agent_install_parts.py`: the list starts with ED25519, holds no `ssh-rsa` and no certificate type,
  both connections use it, every type has its file (`test_the_host_key_types_and_their_files`), and every language's
  host key text has `ssh-keygen -lf /etc/ssh/{key_file}`.

## More Information

* [ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md): the install over SSH.
* [ADR-0185](0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md): where it starts.
* Design: [design/ha-integration.md](../../design/ha-integration.md), the host key step of "Add host".
