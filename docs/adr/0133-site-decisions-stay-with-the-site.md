---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# Decisions about one site's operation stay with that site; the public tree carries a stub

Refines [ADR-0100](0100-publish-as-a-new-repository-with-one-start-commit.md). Builds on
[ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md).

## Context and Problem Statement

Most ADRs decide how Vördr is built: a protocol, a check, a boundary. A few decide how one installation is run: which
hosts join and when, which host is the canary, the quiet hours of its notifications, the review rules of its private
Git hosting, the automation of its home automation system, the order its private release tools enforce. Their decision
*is* the site's configuration. ADR-0099 allows redacting site details in place, but only while the decision and its
reasons stay; for these ADRs a neutral wording would change what was decided.

How are such ADRs kept as a record without publishing them?

## Decision Drivers

* Nothing published allows conclusions about the operator's systems.
* The private log stays complete: what was decided and why.
* Numbers and links stay valid in the published tree; the index check passes there too.
* The published tree is exactly what the checks saw (ADR-0100).

## Considered Options

* neutral wording in place
* supersede each with a neutral ADR
* move them out of the repository into the operator's own notes
* mark them `scope: site`; the private repository keeps them, the published tree replaces each with a stub

## Decision Outcome

Chosen: mark them `scope: site`; the published tree carries a stub.

* An ADR whose decision is the configuration or operation of one installation gets `scope: site` in its front matter.
  The first ones: 0016, 0019, 0028, 0045, 0046, 0047, 0049, 0057, 0058, 0078.
* `docs/adr/check_index.py` accepts `scope: site` and no other value.
* `tools/export_public.py` builds the published tree: each `scope: site` ADR becomes `NNNN-site-decision.md` with the
  same number, status and date, a neutral title, and the supersede lines the index check needs; links to it are
  rewritten; then the index is regenerated and every check runs on the result.
* The operations-log rules of [ADR-0134](0134-the-neutral-check-also-stops-operations-wording.md) do not apply to a
  `scope: site` ADR, because it is never published as written. Site terms and the generic rules still apply.
* A decision that is about the product goes into a normal ADR, even where one site prompted it.

Rejected:

* **Neutral wording in place,** because it would change what was decided, which ADR-0099 does not allow.
* **Supersede each,** because the new ADRs would repeat nothing but the fact that a site decided something.
* **Move them out,** because the private log would lose part of its record and links would break.

### Consequences

* Good, because the private log stays whole, and the published log has no gaps.
* Good, because the stub step is a tool, run the same way at every publication, followed by the same checks.
* Bad, because a public reader sees that a decision exists but not what it was.
* Bad, because one more front matter field must be set right; a site decision without it would be published as
  written. The operations-log rules of ADR-0134 catch the usual wording of such ADRs.

### Confirmation

`check_index.py` rejects any other `scope` value. `tools/export_public.py` on a commit replaces each `scope: site` ADR
with a stub; `check_index.py` and `check_neutral.py` run green on the exported tree.
