---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for setup to become simpler and allowed the proposals to be built; accepted with the merge); proposed by Claude
---

# The config flow keeps its key for a hub and client name until the client is set up

Refines how the integration holds its own key ([ADR-0023](0023-own-ha-integration.md),
`design/ha-integration.md` section 4) while it waits for the approval at the hub
([ADR-0010](0010-administration-only-local.md)). The hub and the protocol stay as they are.

## Context and Problem Statement

The hub fixes a client's key at its first hello and keeps the waiting request without a time limit. Another key under
the same name is refused with `key_mismatch` until the operator forgets the client (`vordr-hub forget-client`); at
most five clients may wait.

The config flow created a fresh key in every run and held it only in memory until the entry existed. When the run ended
before the approval — the hour of waiting ran out, the dialog was closed, Home Assistant restarted — the next run came
with a new key, and the hub refused it with `key_mismatch`. The text on the timeout said the opposite: "Start again;
the hub keeps the request". The ways out were to forget the client at the hub or to choose another name, which leaves
the old request among the five.

How does a second attempt continue the request the hub keeps, and what does the user read when that is not possible?

## Decision Drivers

* Setting up again continues what the operator sees at the hub: the fingerprint shown first stays valid.
* The private key is kept nowhere less protected than the config entry that holds it later.
* A real mismatch (the name is known with a key this Home Assistant does not have) says what to do and what it touches.
* No change at the hub: a key stays fixed at the first hello, as it does for hosts.

## Considered Options

1. Keep the waiting key in Home Assistant's storage, per hub and client name, until the entry exists.
2. Wait in the flow without a time limit.
3. Keep a new key per run and change only the text: forget the client at the hub, or choose another name.
4. Let the hub replace the key of a waiting client on a new hello.

## Decision Outcome

Chosen option: **1**, with texts that tell the truth for the case it cannot cover.

* **Where:** a storage file of the integration (`vordr.pending_keys` in Home Assistant's `.storage`, written with mode
  0600), mapping the hub (host and port, the entry's unique ID) and the client name to the key.
* **When:** the key is written before the first hello reaches the hub. Every later run for the same hub and name takes
  it. When the entry is created, the key moves into it and the file forgets every key of that hub.
* **Refusals keep it:** after `key_mismatch` or a rejection, the key stays. Once the operator forgets the client at the
  hub, the same key starts a new request, and the fingerprint the operator compares is the one shown before.
* **Texts:** the waiting step says the dialog may be closed; the timeout says to add the integration again with the
  same name. `key_mismatch` names the client, this Home Assistant's fingerprint, `vordr-hub clients` to see the known
  one, and `vordr-hub forget-client <name>` — only if the name belongs to no other Home Assistant, otherwise another
  name.

Option 2 still loses the key with a closed dialog or a restart. Option 3 is honest but leaves the user one more step at
the hub for an attempt that only took too long, and fills the five places. Option 4 weakens a property the hub keeps
for hosts and clients alike: whoever reaches the hub could swap the key of a waiting client between the operator's
comparison and the approval.

### Consequences

* Good, because a timeout, a closed dialog and a restart no longer cost the request; the operator approves once, also
  after Home Assistant gave up waiting.
* Good, because nothing changes at the hub or in the protocol.
* Bad, because until the entry exists a private key lives in a second file. It has the same protection as the config
  entries (both in `.storage`, this one 0600), and it is not part of the diagnostics.
* Bad, because a key of an attempt given up for good (another name, a hub never set up) stays in that file. It admits
  nothing unless the operator approves its fingerprint.
* Neutral: removing the entry still removes its key. Setting the hub up again afterwards meets `key_mismatch` with the
  text that names the way out.

### Confirmation

`ha/tests/test_config_flow.py`: a closed dialog, a timeout and a restart continue with the same key until the approval,
and the file is empty once the entry exists; one key per hub and name; a real key mismatch is refused with the client
name, the fingerprint and `vordr-hub forget-client <name>` in every language, and after forgetting the same key waits
again; every refusal text uses only what the flow passes.
