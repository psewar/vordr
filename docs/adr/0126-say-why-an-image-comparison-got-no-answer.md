---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Alles machen", after images failed their comparison every day and the reason had to be worked out from outside); proposed by Claude
---

# Say why an image comparison got no answer

Refines [ADR-0113](0113-report-outdated-container-images.md) (report outdated container images) in what a failed
comparison reports. Everything else there stays.

## Context and Problem Statement

When the agent cannot finish comparing an image with its registry, the image counts in the location's
`outdated_failed`. Nothing more is reported: not which image, not why.

Finding out which images failed and why takes reading the registry code, listing the images on the hosts and asking
the registries again from outside. The reasons can call for opposite reactions: a tag list longer than the agent
reads, which fails every day until a limit changes ([ADR-0121](0121-read-tag-lists-of-up-to-60000-tags.md)), or a
run's share of manifest downloads used up on the first run, which resolves by itself in the next runs. The agent
knows which in each case; it only does not say.

## Decision Drivers

* A component reports its state as a fact, not as a count someone has to explain from outside.
* The reason comes from the answer the agent got, never from an error text.
* No new requests to the registries: the reason is what the run already learned.
* Older agents and hubs keep working.

## Considered Options

1. Leave it: the count only.
2. Log the reason in the agent's journal.
3. Report the reason per image in the inventory, and show it in the hub's view and in Home Assistant.

## Decision Outcome

Chosen: option 3.

* **Reasons**, each a distinct answer of the registry client, so no text is read:
  * `rate_limited`: the registry answered 429;
  * `request_budget`: the run's 300 requests were used up before the question;
  * `manifest_share`: the run's manifest downloads for this registry were used up;
  * `tag_list_too_long`: more pages, tags or bytes than the agent reads;
  * `no_answer`: anything else, such as the network, a server error, a reply that does not parse, or the run's time
    running out.
* **Agent:** each image that counts in `outdated_failed` carries `compare_failed` in the inventory, the reason of its
  first question without an answer. Between comparisons it comes from the agent's kept answers like the rest.
* **Hub:** the container view lists them as `compare_failed` (image, tag, containers, reason), at most 100, beside the
  count. A failed comparison stays no problem of the host, as before.
* **Home Assistant:** the entity "Container images" shows the list as an attribute, not recorded.

Rejected:

* **Option 1,** because the count alone needed an investigation to act on.
* **Option 2,** because the journal is on the host, and the operator looks at Home Assistant.

### Consequences

* Good, because the reaction follows from the reason: wait for a share or budget, look at a limit for a list that is
  too long, look at the network for no answer.
* Good, because it costs no request.
* Bad, because an image reports only its first reason, though a later question could have failed for another.
* Neutral: an agent before this change reports only the count; the hub then shows no list.

### Confirmation

* `internal/images`:
  * `TestCompareFailedReasons`: a used-up manifest share, a tag list too long and a reply that does not parse each give
    their reason, and a later run that gets its answer clears it;
  * `TestOutdatedAgainstAFakeRegistry`: the rate-limited image says `rate_limited`, every other image nothing;
  * `TestOutdatedBudget`: every image the budget left says `request_budget`;
  * `TestTagListsAreBounded`, `TestLongTagListsAreRead`: lists past the bounds answer `tag_list_too_long`.
* `internal/agent`: `TestImagesComparedOnTheirOwnSchedule`: the reason comes along from the kept answers.
* `internal/hub`: `TestContainersView`: the list with image, tag, containers and reason beside the count; still no
  problem of the host (`TestContainersInHostView`).
* `ha/tests`: `test_container_images_compare_failed`: the attribute, not recorded.
