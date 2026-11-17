---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for an easy setup from Home Assistant); proposed by Claude; kay (2026-11-17, merged)
---

# Home Assistant gets one-time downloads of the signed agent release

Refines [ADR-0032](0032-self-update-signed-by-operator.md) (who may fetch a release from the hub),
[ADR-0039](0039-ha-client-roles.md) (what role `admin` may do) and [ADR-0023](0023-own-ha-integration.md) (a new
message on `/v1/ha`). Applies [ADR-0067](0067-hub-announces-features.md) and
[ADR-0007](0007-fixed-codes-instead-of-error-texts.md).

## Context and Problem Statement

A host is to be set up from Home Assistant: during the setup, Home Assistant reaches the new device once over SSH,
with credentials it does not keep, and runs the agent's installer there (`vordr-agent install`). For that the new host
needs the agent binary. The hub already holds the operator's signed release and serves its binaries, but only to an
agent that holds the token of an `agent_update` job (ADR-0032), and a host without an agent has no job. Copying the
binary by hand is the step the setup is meant to remove.

What may Home Assistant get from the hub to install a host, and how does the new host fetch the binary, so that
neither a compromised Home Assistant nor a compromised hub can do more than today?

## Decision Drivers

* The binary that runs as root on the new host is the operator's. Its authenticity rests on the operator key, never on
  the hub (ADR-0032); the hub must not be the one that tells Home Assistant which key to trust.
* A compromised Home Assistant must not turn the hub into a file server, and must learn nothing that lets it do more on
  the hosts that exist.
* The hub has nothing to do with SSH or the device's credentials and never sees them.
* Outcomes travel as fixed codes and statuses, not as texts (ADR-0007).
* What exists is used where it fits: the release folder, the manifest's list of files, the form of download tokens.

## Considered Options

1. A request on `/v1/ha` for role `admin`; the answer carries the signed manifest, the hub's public key and per binary
   a path with a one-time token on a download path of its own.
2. An open download of the current release, without a token.
3. The self-update path and its job tokens for Home Assistant as well.
4. Home Assistant or the new host fetches the binary from somewhere else than the hub.
5. As option 1, and the hub also hands out the operator key from `hub.yaml`, so Home Assistant needs nothing from the
   person.

## Decision Outcome

Chosen option: **1**.

* **Request** `agent_download {request_id, os, arch}`: `os` and `arch` name the new host's platform as the manifest
  does (`linux`/`amd64`, `arm64`, `arm`; `windows`/`amd64`), both or neither; neither asks for every binary. Home
  Assistant sends it only to a hub whose `status` names the feature `agent_download` (ADR-0067).
* **Who:** an approved client with role `admin`, decided on the client register at the moment of the request. Others
  get `client_not_approved` or `role_forbids`. What sets up a new host goes with the highest role; a grant of its own
  beside the role is not needed, because the download admits no host and signs nothing (threat comparison below). A
  client that is not approved gets its one `client_not_approved`, and the hub ends the connection: a pending client
  has nothing to ask for, and must not write a line into the event log per message.
* **Answer** `agent_download_result`: `request_id`, `code`, and with `ok`: `version`, `manifest` and `manifest_sig`
  (the exact bytes of `manifest.json` and `manifest.sig`, base64), `hub_key` (the public key that signs every job,
  base64 as `hub.public_key` in `agent.yaml`), `expires_at`, and `downloads: [{file, path}]`, one per binary asked for.
  Platform, hash and size of a binary are only in the manifest: Home Assistant takes them from there after it checked
  the signature with the operator key the person gave it. **The hub does not send an operator key.**
* **Codes** besides `ok`: `client_not_approved`, `role_forbids`, `bad_request` (only one of `os`/`arch`, or not of the
  form `[a-z0-9]{1,16}`), `no_release` (no `agent_update` section in `hub.yaml`, no release, or one its keys did not
  sign), `no_binary_for_platform`, `release_incomplete` (a binary the manifest names is missing in the release folder,
  has another size, or other bytes than the manifest's SHA-256), `too_many_downloads`. A refusal issues nothing.
* **Token:** 24 random bytes in hex, one per binary, bound to the file and to its hash in the manifest, and to the
  client and the key it was issued to; single use (the download that starts uses it); valid for 15 minutes, the window
  of the job tokens. Tokens live in the hub's memory only: a restart ends them, and Home Assistant asks again. Like a
  request, a download is decided on the client register as it is then: once the client is no longer approved with that
  key and role `admin` (demoted, rejected, forgotten, approved again with another key), its open tokens are refused.
* **Download:** `GET /v1/agent-download/<file>?token=<t>` on the hub's listener, beside the self-update path
  `/v1/agent/`. It serves only a file the current manifest names, with the hash the token was issued for, from the
  release folder, as `application/octet-stream` with its length. Before it uses the token, the hub checks the file's
  size and SHA-256 against the manifest: other bytes would use the token for an install that fails at Home Assistant's
  own check. It reads each binary's hash once per size and time of change, not for every request. Each refusal has a
  fixed status and its code as the body; a refusal does not use the token:

  | Code | Status | When |
  |---|---|---|
  | `bad_method` | 405 | not a GET |
  | `unknown_token` | 403 | no token, a malformed one, one never issued, or one that expired over an hour ago |
  | `token_expired` | 410 | the 15 minutes have passed |
  | `token_used` | 409 | its download has started already |
  | `wrong_file` | 404 | the token opens another file |
  | `client_revoked` | 403 | the client the token was issued to is no longer approved with that key and role `admin` |
  | `release_changed` | 410 | the current release no longer names the file with the hash the token was issued for |
  | `binary_unreadable` | 500 | the file cannot be read or has another size than the manifest says |
  | `binary_mismatch` | 500 | the file's bytes are not the manifest's SHA-256 (replaced or damaged) |

* **Write window:** a download may take as long as its binary needs at 64 KiB/s, plus one minute; a reader slower than
  that is cut off (`complete: false`), so that it holds no goroutine and no open file beyond it. The self-update path
  `/v1/agent/` gets the same window.
* **Limits:** at most 8 tokens exist at a time, and one client holds at most 4 of them; one new host needs one binary.
  A token counts from its issue until it expires, used or not, so at most 8 downloads start per 15 minutes, 4 of them
  for one client; a request is answered in full or refused with `too_many_downloads`, and the event names the bound
  (`hub` or `client`).
* **Event log,** never with a token: `agent_download_issued` (client, request, version, platform, `downloads` with
  `file` and `download_id`, `expires_at`), `agent_downloaded` (client, file, version, `download_id`, remote address,
  `bytes`, `complete`), `agent_download_refused` (for a request: client and code; for a download: remote address,
  file, `download_id`, code and status). `download_id` is the first 16 hex digits of the token's SHA-256: it ties issue
  and download together and cannot be turned back into the token. Refusals of the download path are logged in two
  budgets:
  * a token the hub issued: every refusal, up to 20 per token, with `client`, `version` and `refusal` (its number for
    that token); the 20th says `more_not_logged`. Strangers cannot push these out, and whoever saw one path cannot
    write the log full with it;
  * anything else (no, a malformed or an unknown token, not a GET), from anyone who reaches the port: at most 10 a
    minute; the next one logged carries `suppressed`, the number left out.
* **Self-update unchanged:** job tokens and `/v1/agent/` stay as they are, but for the write window. The two kinds of
  token do not open each other's path.

Option 2 lets anyone who reaches the port pull the release at any time, unbounded and unattributed; the binaries are no
secret, but there would be no decision of a known client behind a download, and nothing to bound. Option 3 opens the
whole release for any number of downloads for 15 minutes: a path seen in the new host's process list, its shell
history or a proxy log would stay usable, and changing those tokens would change the agents' self-update, which works.
Option 4: the repository publishes no binaries (ADR-0139, ADR-0144); the operator builds and signs, and the hub is where
the signed release already is. Option 5 takes the trust anchor from the component it is meant to check: a compromised
hub would hand out its own key with its own manifest, and its code would run as root on the new host.

### Threat comparison

| Who | Today | With this decision |
|---|---|---|
| A compromised Home Assistant with role `admin` | jobs within every host's policy, reboots | in addition: the manifest and its signature, the hub's public key, and at most 4 downloads of release binaries per 15 minutes (its share of the hub's 8). All of it is public or signed: the binaries are what every agent gets. It admits no host (approval stays at the hub, ADR-0010), has nothing signed, and reads no file outside the release. That Home Assistant can run code of its own on a device it reaches over SSH does not come from this download. |
| A compromised Home Assistant with role `operate` or `observe` | its role's commands | nothing more: `role_forbids` |
| A compromised hub | jobs within the policies; no code on agents (ADR-0032) | can withhold or swap the binary and the manifest, but Home Assistant checks the manifest with the operator key it has from the person, and the binary against the manifest's hash: a swapped binary does not run. It can hand out a wrong `hub_key`; the new agent then refuses the jobs of the real hub, and the key it trusts is no stronger than the hub's own, which signs jobs anyway. |
| Someone who sees a download path | – | one download of a binary that is no secret, within 15 minutes and before the new host fetches it; the host then gets `token_used` and Home Assistant asks again. The thief's download and the host's refusal are both logged, whatever strangers send. After use or expiry: nothing but at most 20 refusal lines for that path. |
| A stranger at the port | no binary without a job token | the same; the requests are refused as `unknown_token` and logged at a bounded rate |
| A client at `/v1/ha` that is not approved | its status, nothing else | the same: one `client_not_approved` per connection, then the hub ends it |

### Consequences

* Good, because a new host needs nothing copied by hand: Home Assistant fetches the release where the operator put it.
* Good, because the operator key keeps its role: the hub cannot put code on a host, a new one included.
* Good, because a leaked path is worth one download for 15 minutes, and every issue, download and refusal of an
  issued token names its client or address in the event log; strangers cannot push those refusals out.
* Good, because a binary in the release folder that is not the one signed is found by the hub, before a token is used
  for it.
* Bad, because Home Assistant needs the operator's public key from the person, one more input when it is set up;
  without it, the integration cannot check the manifest and must not install.
* Bad, because one client with role `admin` that keeps asking holds its share of 4 tokens for as long as it keeps
  asking, not only for 15 minutes; two such clients hold the hub's 8 and hold back every other client as long. The
  share stops one client, not two.
* Bad, because a download that breaks off, or is cut off as too slow, has used its token; Home Assistant asks again
  with one message. A link slower than 64 KiB/s cannot fetch a binary at all.
* Bad, because the hub reads a binary's hash again only when its size or time of change changes: a binary replaced by
  another of the same size and time of change is served until then. Home Assistant's own check still refuses it.
* Neutral: the new host must reach the hub at the address Home Assistant uses and trust its certificate (ADR-0020).
* Neutral: the integration uses the request in a later step; until then nothing sends it.
* Neutral: a pending client that sends `command` still gets `client_not_approved` and one event line per message, as
  before this decision; ending its connection the same way is left to a decision of its own.

### Confirmation

* `internal/hub/agentdownload_test.go`:
  * `TestAgentDownloadForAnAdmin`: the manifest and signature byte for byte as in the release folder, verified with the
    operator key; `hub_key` is the key that signs jobs; `expires_at` 15 minutes ahead; one download for one platform,
    one per binary without a platform; the event names the downloads, the log holds no token.
  * `TestAgentDownloadRefusals`: `operate`, `observe`, no role, a session of another key, an unknown and a pending
    client, malformed platforms, no release, no `agent_update` section, no binary for the platform, a binary missing,
    of another size, or of the same size with other bytes; nothing issued, each refusal logged.
  * `TestAgentDownloadServesTheExactBytesOnce`, `TestAgentDownloadTokenRefusals` (every status of the table, the token
    unused after `wrong_file`, `binary_unreadable` and `binary_mismatch`, a token expired over an hour ago unknown
    without a new issue), `TestAgentDownloadAndSelfUpdateTokensStayApart`, `TestAgentDownloadCap` (the hub's bound and
    the client's share, named in the event), `TestAgentDownloadOneClientCannotStarveAnother`,
    `TestAgentDownloadRefusalLogIsBounded`, `TestAgentDownloadRefusalsOfIssuedTokensAreLogged` (after strangers filled
    their budget; the cap per token), `TestAgentDownloadTokensFollowTheClient` (demoted, rejected, approved again with
    another key: `client_revoked`, the token unused), `TestAgentDownloadFromAPendingClientEndsTheConnection`,
    `TestDownloadsCutOffASlowReader` (both download paths), and `TestAgentDownloadOverTheHAProtocol` (the feature in
    the status, request and download over `/v1/ha`, a malformed request ends the connection).
* Each check of the hub was taken out once: the role, single use, the file binding, the expiry, the bound, used tokens
  in the bound, the hash, the token in the log, the feature, the log rate, the missing and the unreadable binary; and
  after the security review the client's share, the separate budget and cap of issued tokens, the hash of the bytes
  at issue and download, forgetting old tokens on lookup, the client check at download, ending a pending client's
  connection and the write window on both paths. A test fails for each.

## More Information

* Design: [architecture.md](../../design/architecture.md) 5, 10.4 and 12;
  [ha-integration.md](../../design/ha-integration.md) 4, 5 and 5b (how the integration is to use the answer).
