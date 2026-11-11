---
status: accepted
date: 2026-11-01
decision-makers: kay (2026-11-01, asked for a site-neutral repository; approved redacting the accepted ADRs, titles and file names included, in the same PR; accepted); proposed by Claude
---

# The repository is site-neutral, checked by the build

Extends [ADR-0082](0082-repo-stays-private-for-now.md), which keeps the repository private until a review, and the
rule on accepted ADRs in [ADR-0072](0072-decisions-as-adrs-checked-by-the-build.md) and `docs/adr/README.md`. Both stay
in force; this adds a standing rule, its check and one more kind of allowed change to an accepted ADR.

## Context and Problem Statement

The operator is considering publishing Vördr. The repository was written for one site:

* Tests, examples, design notes and ADRs use the site's own host names, addresses, domain, guest numbers, device
  models and firmware versions, which software runs on which host, the operator's name, paths of the operator's tools
  outside the repository, and measurements of the own installation: well over a hundred files with known site terms,
  plus addresses, an SSH host key, a certificate fingerprint and paths found by hand. A reader learns the layout of the
  site; a stranger finds examples that do not fit their own.
* Some texts read as if a product were the only way, where the decision is general and the product is only the first
  implementation.
* Cleaning once does not last: every new test or note adds the next host name. ADR-0082 already noted that every new
  mention adds to the work before a publication.
* Accepted ADRs are not rewritten; only typos and links may be fixed. 75 of the 98 ADRs contain site terms, 33 of them
  more than the operator's name, some in their title and file name.

How does the repository become and stay free of site details, and what may happen to accepted ADRs that contain them?

## Decision Drivers

* Nothing in code, documents, test data or examples allows conclusions about the operator's systems.
* A stranger with other infrastructure can follow every document and example.
* A check that matters belongs in the tool, not in a text ([ADR-0072](0072-decisions-as-adrs-checked-by-the-build.md)).
* The list of what must not appear must not itself give away what it protects.
* What was decided, and why, stays traceable in every ADR.
* No behaviour change: this is about text, examples and test data.

## Considered Options

The rule:

* clean once before publishing, no standing rule
* a standing rule, checked by `build.sh`

The site terms the check needs:

* a list in the repository
* hashes of the terms in the repository
* a keyed hash (HMAC) in the repository, the key outside
* the list outside the repository, generic rules inside

Accepted ADRs with site details:

* leave them; publish a cleaned copy only
* supersede each with a neutral ADR
* allow redaction in place

## Decision Outcome

Chosen: a standing rule checked by `build.sh`, with the site terms outside the repository and generic rules inside,
and redaction in place for accepted ADRs.

* **Neutral content.** No host names, addresses, domains, guest numbers, device models or firmware of single devices,
  counts or roles of the operator's hosts, software per host, measurements or data of the own installation, paths
  outside the repository, and no operator's name in running text ("the operator"). Examples and tests use placeholders:
  `host-a` or single words such as `alpha`, `example.org`, addresses from 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24
  (RFC 5737) and 2001:db8::/32 (RFC 3849), synthetic package lists, keys and fingerprints. A measurement stays where it
  supports a design statement, without host, date or installation. `decision-makers:` keeps the name: it records
  authorship, not infrastructure.
* **Products as implementations.** A decision is written for the general case. A product is named as the supported or
  first implementation ("first implementation: Proxmox VE with Proxmox Backup Server"), with where another system would
  have to fit in, and without claiming a plug-in interface that does not exist; the pattern is
  [ADR-0096](0096-fresh-backup-right-before-the-install.md).
* **Site data stays with the site.** A list that belongs to one site ships as an example; the site keeps its own copy
  with its operation tools. First case: the device list of the pfSense forced command (`deploy/pfsense/vordr-gna-allow`).
* **The check.** `tools/check_neutral.py`, run by `build.sh` right after the ADR check:
  * generic rules in the code: private and carrier-grade NAT addresses (RFC 1918, RFC 6598), unique-local, link-local
    and global IPv6 outside the documentation prefix, host names in URLs and in `server_name` that are neither
    placeholders (`*.example`, `*.invalid`, `*.test`, `example.org`) nor one of a short list of public sites, and WSL
    mount paths;
  * site terms from a list outside the repository, `~/.config/vordr/site-terms.txt` or the file `$VORDR_SITE_TERMS`
    names: whole words, or regular expressions with `re:`. A term marked `person:` may stand in the
    `decision-makers:` line of an ADR, which records authorship, and nowhere else;
  * not matched: the module path from `go.mod`, which is the publishing location. The public OSV records in
    `internal/osv/testdata`, kept verbatim with their reference links, get the site terms only;
  * without a list the build stops and says how to go on: write one (an empty file is fine), or set
    `VORDR_SITE_TERMS="none: <reason>"`, which runs the generic rules alone and prints the reason on every run. There is
    no silent way around it;
  * `--text <file>` checks a commit message or a PR description the same way.
* **Accepted ADRs.** Site details in an accepted ADR may be redacted in place, like typos and links: replaced by
  neutral words or placeholders, also in the title and the file name, which the index and every link then follow. The
  number, status, date, `decision-makers`, the decision and its reasons stay. A change that would alter what was
  decided or why is a new ADR, as before.

Rejected:

* **Clean once,** because the next test brings the next host name, and the review ADR-0082 asks for would never be
  finished.
* **A list in the repository,** because it would publish exactly what it protects.
* **Hashes in the repository,** because host names are short and often drawn from a small, guessable set. Hashing a
  list of candidates finds them again in seconds, salted or not, as the salt would be public too.
* **HMAC with the key outside,** because it needs a file outside the repository just like the list and adds a key to
  keep, without protecting more: whoever holds the key can test names as well.
* **Leave accepted ADRs, publish a cleaned copy,** because two versions of the same ADRs would drift apart, and the
  check could not run on the repository the operator works in.
* **Supersede each ADR,** because some 75 new ADRs would repeat decisions that did not change, and the old ones would
  still carry the details.

### Consequences

* Good, because examples fit any site, and a reader learns the design, not the operator's network.
* Good, because a private address, a real domain, an out-of-repository path or a known site term stops the build, in
  code, tests and documents alike.
* Good, because the protected terms stay with the site; the repository carries only generic rules.
* Bad, because the check knows only the terms on the list. A new host name not yet on it, and site details that are not
  words (a count of hosts, a measurement, a version tied to one device), still need review.
* Bad, because a build without a site list stops until someone writes one or states a reason, one more step for a
  stranger's first build.
* Bad, because redacted ADRs read less concretely than they were decided; the operator's own notes keep the concrete
  version.
* Bad, because the history still holds every detail. How it is published is decided in
  [ADR-0100](0100-publish-as-a-new-repository-with-one-start-commit.md).
* Neutral: established component names, such as the pfSense forced command `vordr-gna` with its user and files, stay,
  even where a neutral name would be chosen today. Renaming them would change the deployment, and the operator chose to
  keep them (2026-11-01); the name reads as a component name.

### Confirmation

* `build.sh` runs `tools/check_neutral.py`. Counter-checks: a private address, a host name that is no placeholder in a
  URL, a site term and a missing list each stop it; `VORDR_SITE_TERMS="none: <reason>"` runs and names the reason.
* The site's own search for known terms, kept with its operation tools, finds nothing in the repository.
* Review still judges what no list can: counts, measurements, versions tied to a device.
