---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked whether AI tasks could judge how safe an update is or how quickly it should be done, with the AI part optional; accepted with the merge); proposed by Claude
---

# Optional AI explanations of failed runs go through Home Assistant's AI Task and only advise

Brings into the integration what one site does with an automation of its own
([ADR-0045](0045-site-decision.md), a site decision). Bounded by the decision "An AI may not change
maintenance windows, minimum ages, ring delays or scopes" (rejected, recorded in its own ADR): nothing here lets an AI
decide.

## Context and Problem Statement

The question was whether AI tasks could judge how safe an update is or how quickly it should be done, with the AI
part optional. How urgent a fix is comes from data: known exploitation, the CVSS vector, the rating of the source. An
AI's opinion on it can neither be checked nor defended against a crafted changelog. Where a model does help is text:
a failed run ends with a code and a log excerpt that a person has to read and understand. Home Assistant offers AI
Task (`ai_task.generate_data`), with which a person chooses a model, local or a cloud service, once for all
integrations.

What may an AI do in Vördr, where does it run, what goes to it, and how is its answer kept from deciding anything?

## Decision Drivers

* Optional and off by default: without a choice nothing changes and nothing is sent.
* Advice only: windows, minimum ages, scopes, the local policy, buttons and repairs stay deterministic.
* The hub stays without AI: no provider settings, no API keys, no traffic to a provider. It sends codes
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)); texts are Home Assistant's side
  ([ADR-0027](0027-protocol-2-codes-and-translations.md)).
* As little data as the explanation needs, the site's names taken out, and only after a confirmation.
* Log excerpts come from the host and from package sources and may carry instructions (prompt injection): an answer
  must not be able to act, and free text must not decide anything.
* A model may invent a cause: what is shown must be backed by the log.
* Every outcome a fixed code, no exception as a signal.

## Considered Options

1. In the integration, through Home Assistant's action `ai_task.generate_data`, advisory, for failed runs only.
2. In the hub, with provider settings of its own.
3. Through a conversation agent (`conversation.process`).
4. An AI that judges urgency or safety and changes order, windows or delays.
5. No AI in the product; each site writes its own automation, as in ADR-0045.

## Decision Outcome

Chosen option: **1**.

* **Switch:** the item "AI explanations" of the options menu (key `ai_explanations`): an AI Task entity and the
  confirmation that package names and log excerpts of failed runs go to its provider. Empty: off. The form refuses an
  entity without the confirmation, one that does not exist or is unavailable, and one that cannot generate data.
  Stored options that the form would refuse (edited by hand) send nothing (`not_configured`, logged once).
* **The call:** through the action, so that nothing of `ai_task` is imported and the model gets no tools: the action
  takes no `llm_api`. A structure with four fields: `explanation` (text, in Home Assistant's language),
  `likely_cause` (select from a fixed list), `next_steps` (select, several, from a fixed list), `quote` (text).
* **Which runs:** a run that ends (`vordr_run_finished`) with a failure whose log carries the cause:
  `precheck_disk_space`, `precheck_package_db_broken`, `precheck_repo_unreachable`, `precheck_would_remove_packages`,
  `failed_package_manager`, `failed_verification`, `failed_health`, `failed_windows_update`, `failed_target_auth`,
  `failed_config_backup`, `failed_backup`, `timeout`, `aborted`, `interrupted`. Never `ok`, a refusal (`rejected_*`),
  a decision of Vördr's rules (`precheck_version_flagged`, `precheck_version_too_new`, `precheck_vetting_unavailable`,
  `precheck_not_from_registry`), nor a security signal (`precheck_hash_mismatch`, `failed_target_tls`,
  `failed_target_hostkey`): an AI does not explain those away. Each result code is in one of the two sets.
* **Checked first, without a call:** the options, an excerpt to explain, the entity there and available, its feature
  to generate data, the action there.
* **What is sent:** the result code, the verb, the package names of the run's summary (at most 50) and the end of the
  log excerpt (at most 2000 characters). Replaced before, by placeholders: the hub's URL and host (`<hub>`); the run's
  host and every other host the hub knows, each with the domain after it (`<host>`, `<other-host>`); names in the
  hub's domain and the server of any URL, with user and password (`<server>`); every IPv4 and IPv6 address (`<ip>`).
  Replaced first, shortened then, so that no cut name slips through. Other words go as they are, for example a
  package source named without a URL; the confirmation says so. The facts stand between two marker lines, the
  instructions say they are data, and a marker inside them is taken out.
* **The answer is checked:** each field there with its type and each select value in its list, else
  `invalid_answer`. The quote must be part of the excerpt *as it was sent*, character for character (runs of white
  space count as one), 8 to 300 characters, else `unverified`, and nothing of the answer is shown. Only `ok` carries
  the fields; the explanation is put on one line and shortened to 600 characters.
* **Bounded:** at most one call per run (by node and job id), one question at a time, 60 seconds each. A text sent
  again takes the earlier `ok` answer (the last 32 are kept): the same failure on several hosts reads the same once
  the names are replaced. A timeout or whatever the provider raises is `no_answer`, caught at that boundary only.
* **The result is advice:** the event `vordr_ai_explanation` `{node, job_id, state, code, entity_id}`, with `ok` also
  `likely_cause`, `next_steps`, `explanation`, `quote`. It fires for every run of the set, also while off
  (`disabled`), so that an automation never waits for an answer that does not come. The host's sensor "Last run" shows
  `ai_code`, `ai_entity_id` and with `ok` the fields for the run it shows, out of the recorder
  ([ADR-0101](0101-package-lists-stay-out-of-the-recorder.md)); nothing while off. Nothing else changes: no
  window, minimum age, scope, policy, button, repair or command. Only the select fields may drive what is displayed;
  the free text is shown as AI text and never read by the integration.
* **Codes:** `ok`, `unverified`, `invalid_answer`, `no_answer`, `no_excerpt`, `unsupported`, `entity_missing`,
  `not_configured`, `disabled`.

Option 2 puts provider settings and API keys into the hub and opens a path from it to a provider, while Home
Assistant already holds both, and the text is display like the translations. Option 3 answers in free text that
would have to be parsed. Option 4 is the decision rejected in its own ADR: urgency comes from data, and an AI that
holds or hurries updates could be steered by a crafted changelog. Option 5 leaves every site to rebuild the
replacements and the quote check.

### Consequences

* Good, because a failed run can come with a short explanation and a cause and steps from fixed lists, with whatever
  model the person chose.
* Good, because the answer cannot act: it reaches an event and sensor attributes, the select fields are from fixed
  lists, and the quote check catches an invented quote.
* Good, because the hub, the agents and every existing configuration stay as they are; while off everything works as
  before, apart from the event saying `disabled`.
* Bad, because log excerpts and package names leave Home Assistant once switched on; the replacements take out the
  names Vördr knows and addresses, not every word that may point at a site.
* Bad, because the quote check does not stop a crafted excerpt: whoever controls the log can make the model quote it.
  That is why nothing acts on the answer and the free text is marked as AI text.
* Bad, because a model that misses the structure or the quote often makes the item useless; the codes say which.
* Neutral: release notes, a daily limit and a push of the explanation are later changes.

### Confirmation

`ha/tests/test_ai_explain.py`, against a played AI Task entity and action, no provider: off by default nothing is sent
and the event says `disabled`; on, one call per failed run and none for `ok`, a refusal, a security signal or the same
run again; names and addresses replaced in what is sent; an `ok` answer shown; a quote that is not part of what was
sent (also one from the text before the replacements, or from the instructions) is `unverified` without any field;
a missing, unavailable or unsupported entity, a missing action, a timeout, an error of the provider and malformed
answers give their codes; with an answer that suggests steps, no state, attribute, repair or option changes besides
the event and the sensor's `ai_*` attributes, and no command is sent; every result code is placed.
`ha/tests/test_texts.py`: every code, cause, step and error has its text in every language.
