---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked to extend Vördr towards an easier installation and to build the proposals); proposed by Claude; kay (2026-11-17, merged)
---

# The hub signs its challenge, and the agent checks the hub key before it logs in

Refines [ADR-0002](0002-own-project-following-an-earlier-agent.md) (login by challenge, jobs signed with the hub key
that every agent pins). The new field follows the compatibility rules of
[ADR-0067](0067-hub-announces-features.md) and [ADR-0114](0114-agent-names-its-run-and-the-hub-acknowledges-results.md).

## Context and Problem Statement

Every agent pins the hub's public key in `agent.yaml` (`hub.public_key`) and checks every job against it. The login
itself proves only the agent's key: the hub's challenge is a bare nonce. So:

* An `agent.yaml` with a wrong `hub.public_key` (copied from the wrong place, the key of another hub, a hub key that
  was replaced) installs without complaint. The host appears at the hub, and only its first job fails, as
  `rejected_invalid_job`.
* `vordr-agent hub-check`, which `install-agent.sh` and `install-service` run before they change anything, checks
  name, connection, TLS, tunnel and challenge, but cannot tell whether the key is right.
* The agent sends its hello and its reports (facts, policy, package lists) to whatever answers at `hub.url` with a
  trusted certificate, without knowing whether it holds the key the agent pins.

## Decision Drivers

* A wrong hub key is found by the tool that sets the host up, before it changes anything (tools check their own
  preconditions).
* The finding is a fixed code, not a text to parse ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* Older agents and older hubs keep working; hub and agents are not updated in lockstep
  ([ADR-0049](0049-site-decision.md)).
* A signature made for one connection must not count on another.
* Once an agent has seen its hub sign, leaving the signature out must not make an impostor pass as an older hub.
* An agent that refuses the hub tells it so as a fixed code; the hub does not have to guess from a dropped connection.
* No new key and no new secret: the key is the one the agents already pin.

## Considered Options

1. The hub signs its nonce with a purpose string.
2. The hub signs its nonce and a nonce the agent sends in the WebSocket upgrade request.
3. The hub signs its nonce and keying material exported from the TLS session (channel binding).
4. The hub signs its status after the hello.
5. A new first message from the agent that carries its nonce.

## Decision Outcome

Chosen option: **2**.

* **Upgrade request:** the agent sends the header `Vordr-Challenge-Nonce`, 32 random bytes in base64, new for every
  connection (`hub-check` and every login).
* **Challenge:** with a usable header the challenge carries `hub_sig`, the hub key's Ed25519 signature over
  `vordr/challenge/v1\0`, the hub's nonce and the agent's nonce. The context string keeps it apart from what else the
  hub key signs (`vordr/job/v1`, `vordr/vet/v1`). Without the header, or with one that is not 32 bytes in base64, the
  challenge is as before.
* **Result in the agent,** a fixed code `hub_key`:
  * `matches`: signed with `hub.public_key` over this connection's nonces;
  * `mismatch`: signed, but not so: another key, a damaged signature, or one recorded on another connection;
  * `unsigned`: no signature, a hub before this decision;
  * `downgrade`: no signature, from a hub that this agent has seen sign before (next point).
* **Downgrade protection:** the first `matches` at login is written to `state_dir/hub-signed.json`, with the `hub.url`
  and `hub.public_key` it was seen with. While `agent.yaml` names that same pair, an unsigned challenge is
  `downgrade` and refused like `mismatch`: whoever answers may simply have left the signature out. Without a record,
  or with one for another `hub.url` or `hub.public_key`, `unsigned` passes as before. A record that exists but cannot
  be read counts as seen; the next `matches` replaces it. `hub-check` reads the record and never writes it.
* **`hub-check`:** a new last stage `hub_key`. `matches` passes; `unsigned` passes with the line "not checked";
  `mismatch` and `downgrade` fail at stage `hub_key`, the latter naming the record and how a hub put back on purpose is
  let in again. Exit codes: 0 ok, 1 a stage before `hub_key` failed (not reachable), 2 `agent.yaml` not usable, 3 the
  hub does not sign with `hub.public_key`, or no longer signs.
* **`install-agent.sh` and `install-service`** act on the exit code: 3 refuses before any change, with its own reason
  and the next step (`vordr-hub key` on the hub); 1 refuses as before. A hub that does not sign, and never did for this
  host, passes: an older hub must not stop an installation.
* **Agent run:** the agent checks the challenge before its hello. With `mismatch` or `downgrade` it sends nothing about
  its host, logs the code and asks again after the wait it keeps after a refusal by the hub (10 min). With `unsigned`
  it logs in as before; the session's log line names the fact (`hub_key unsigned`), as it names `hub_key matches`.
* **The refusal is reported as a fact:** instead of just dropping the connection, the agent (and `hub-check`) ends it
  with a WebSocket close code from the private range, `4001` for `mismatch` and `4002` for `downgrade`. The hub logs
  the event `agent_refused_hub_key` with `hub_key` and the remote address, and nothing about a host, since none was
  sent. Hosts that go offline after a new hub key, or a proxy that drops the header, show at the hub, not only in the
  log of each host.
* **Scope:** the agent tunnel only. Home Assistant does not know the hub key; its challenge on `/v1/ha` stays as it is.
* **Compatibility:**
  * An older agent sends no header and gets the challenge as before; it would ignore the field anyway, since it reads
    the challenge leniently.
  * A new agent with an older hub gets `unsigned` and works as before; it is a reported state, not an error.
  * Rollout: hub first, as always (ADR-0049); the other order is safe as well.

Why not the others:

* **Option 1** proves only that the hub key signed that nonce at some time. A challenge recorded once fits every later
  connection, so `matches` would say nothing about who answers now.
* **Option 3** also stops a relay that passes the messages on live, but ties the check to TLS ending in the hub process:
  a proxy that terminates TLS in front of the hub would read as `mismatch`. A relay that gets through TLS gains nothing
  the job signatures do not already stop: jobs are signed, bound to the node and expire within minutes.
* **Option 4** needs a hello: `hub-check` would register the host at the hub as pending, and the agent would have sent
  its facts already.
* **Option 5:** an older hub drops a connection on a message it does not know. The agent would first have to learn
  whether it may send it, a round trip more for what a header does.

For the downgrade protection:

* **Remembered per `hub.url` and `hub.public_key`** (chosen). `agent.yaml` is the trust anchor and only the host's
  administrator changes it. Pointing it at another URL or key says "another hub", which may be older; carrying the
  record over would take that hub offline for no reason. An attacker who can change `agent.yaml` has won already.
* **Remembered once for all hubs:** after one signing hub, every hub would have to sign. Moving a host to another,
  older hub would need the record removed by hand, and the refusal would look like an attack.
* **Not remembered, only a switch in `agent.yaml`** (`hub.require_signed_challenge`): protects also the first
  contact, but only where someone sets it, and it does not exist for the hosts already installed. It can be added
  later on top of the record, for example set by an installer that knows its hub signs.

### Consequences

* Good, because a wrong `hub.public_key` stops the installation on Linux and Windows with its own reason, instead of
  showing at the first job.
* Good, because the agent sends nothing about its host to a hub that signs with another key, nor, once it has seen
  its hub sign, to whatever answers at the same `hub.url` without a signature.
* Good, because a refusal reaches the hub as a fixed close code and an event, not as a connection that just ends.
* Good, because older agents and older hubs need no change.
* Bad, because as long as older hubs exist, `unsigned` has to pass until the agent has seen its hub sign. The very
  first contact is not protected: someone who gets through TLS before the agent has ever logged in to a signing hub
  (at installation, with `hub-check` and the first login; after `hub.url` or `hub.public_key` changed; after
  `state_dir` was emptied) can strip the signature and look like an older hub. The agent then sends its hello and
  reports there; jobs keep their own signatures. A later decision can refuse an unsigned challenge once no older hubs
  are left, or add a switch for installers that know their hub signs.
* Bad, because a hub put back to a version before this decision, or a proxy in front of the hub that drops the header
  `Vordr-Challenge-Nonce`, is refused as `downgrade` by every agent that saw the hub sign. Its log, `hub-check` and the
  hub's event name it; removing `state_dir/hub-signed.json` lets an older hub in again on purpose.
* Bad, because a host whose hub key changed goes offline at the hub instead of reporting and refusing every job; its
  log names `hub_key mismatch`, and the hub logs `agent_refused_hub_key`. A changed hub key already means changing
  every `agent.yaml` (ADR-0002).
* Neutral: the hub makes one Ed25519 signature per connection, also before it knows who connects. What it signs starts
  with the context string and its own random nonce, so it can never be taken for a job or a vet answer.
* Neutral: anyone who reaches the tunnel can make the hub write one `agent_refused_hub_key` line per connection, as
  they can write one `hello_refused` line with a bad hello; each costs them a TLS connection. Neither is throttled.

### Confirmation

* `internal/wire/challenge_test.go`: the three codes; a damaged, short, replayed (other agent nonce) signature, one
  over another hub nonce and one with another context are `mismatch`; a missing or unusable header is no nonce; an
  older agent reads the signed challenge; `mismatch` and `downgrade` are refused, each with its close code, which
  reads back to it, and no other close code does.
* `internal/wire/conn_test.go`: a close code reaches the other side; a connection that just ends is no refusal.
* `internal/hub/challenge_test.go`: the hub signs with its key for the agent's nonce, a new signature per connection,
  and does not sign without a usable nonce; close codes 4001 and 4002 are logged as `agent_refused_hub_key` with the
  address and no node, other closes are not.
* `internal/agent/hubsigned_test.go`: no record on a fresh install; the record holds for its `hub.url` and
  `hub.public_key` only; an unreadable one counts as seen; only `unsigned` turns into `downgrade`.
* `internal/agent/hubkey_e2e_test.go`: `Probe` against the real hub with the pinned key (`matches`) and another key
  (stage `hub_key`, the refusal logged at the hub, nothing written to `state_dir`), without logging in; against an
  older hub (`unsigned`, passes); against a damaged or replayed signature (stage `hub_key`). The run logs in with the
  pinned key, and with an older hub (no record written); with another key or a damaged signature it sends no hello,
  ends with close code 4001 and the hub never hears of the host. After a `matches`, a restarted agent refuses the same
  `hub.url` without a signature (`downgrade`, close code 4002, `hub-check` fails at stage `hub_key` naming the record)
  and logs in again once the hub signs; another `hub.url` starts fresh and leaves the record as it was.
* `cmd/vordr-agent/hubcheck_test.go`: the exit codes 0, 1 and 3 from the finding (3 also for `downgrade`, with its
  own hint), `hub-check` against the real hub with its key (0) and another key (3), and `hubCheckRefusal`, which
  `install-service` refuses with for every exit code but 0.
* `deploy/install_agent_test.go` (`TestInstallRefusesByHubCheck`): exit 3 and exit 1 each refuse with their own reason
  before anything changes; exit 0 installs.

## More Information

`design/architecture.md` 3, 4, 6.2, 6.3 and 10.1; `deploy/README.md`.
