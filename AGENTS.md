# Repository guidance

How Vördr is developed. This applies to every contributor, human or coding agent. This file is the single source;
tool-specific files such as `CLAUDE.md` only import it.

## Workflow

- Work on a topic branch and open a pull request against `main`. One topic per pull request; nothing is pushed to
  `main` directly.
- The maintainer reviews and merges. Do not merge your own pull request.
- `./build.sh` and, for the Home Assistant integration, its tests and ruff are the gate: they must pass before you
  push. On GitHub the same checks run for every push and pull request
  ([ADR-0137](docs/adr/0137-checks-run-in-github-actions.md)).
- Work happens in the operator's repository; the public repository receives checked snapshots, and an accepted pull
  request there is carried over with its author named
  ([ADR-0140](docs/adr/0140-work-stays-private-the-public-repository-gets-snapshots.md)).
- Contributions are under the MIT license of the repository (`LICENSE`,
  [ADR-0135](docs/adr/0135-mit-license.md)).
- Commit subjects say what changes, in English, and name the ADR where there is one, e.g.
  `Update guard rolls back a new agent that hangs (ADR-0098)`.
- The pull request description says what changes and why, names its ADRs, says how it was tested, and gives the
  rollout order when hub, agent and integration change together.
- Releases are built and signed by the operator after the merge, with `vordr-release` on the operator's machine
  (agent manifests: [ADR-0032](docs/adr/0032-self-update-signed-by-operator.md),
  [ADR-0139](docs/adr/0139-the-release-signer-is-part-of-the-repository.md)). The integration ships as `vordr.zip`
  on a GitHub release tagged with its version ([ADR-0136](docs/adr/0136-integration-via-hacs-as-a-zip-release.md)).
  A pull request prepares them; see "What a change brings along".

## Public data

The repository is site-neutral ([ADR-0099](docs/adr/0099-the-repository-is-site-neutral-checked-by-the-build.md)):
nothing in it may point at the infrastructure of whoever runs it.

- No host names, addresses, domains, people's names in running text, or paths outside the repository: not in code,
  comments, tests, examples, documentation, commit messages or pull request texts.
- Examples and tests use placeholders: `*.example`, `*.invalid`, `*.test`, `example.org`, and the documentation ranges
  `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`.
- A decision is worded so it fits other sites. Name a product only as a supported or first implementation, and say
  where another one would plug in.
- `tools/check_neutral.py` runs in `./build.sh`. It reads the site's own terms from outside the repository
  (`~/.config/vordr/site-terms.txt` or the file `VORDR_SITE_TERMS` names). Check commit messages and pull request
  texts with `python3 tools/check_neutral.py --text <file>`. The check does not replace reading your added lines.
- The text describes the design and how it is checked, not what happened at one site: no dated runs, no "seen on the
  host", no results of a pilot, no counts of one installation, no internal reports as sources. Test data is synthetic,
  not a renamed capture of a real machine. `check_neutral.py` stops the usual wording
  ([ADR-0134](docs/adr/0134-the-neutral-check-also-stops-operations-wording.md)).
- A decision about one site's own operation gets `scope: site` in its ADR front matter and is published only as a stub
  ([ADR-0133](docs/adr/0133-site-decisions-stay-with-the-site.md)).
- The repository is published as a new repository with one start commit
  ([ADR-0100](docs/adr/0100-publish-as-a-new-repository-with-one-start-commit.md)); `tools/export_public.py`
  builds that tree and each later snapshot and runs the checks on it.

## What a change brings along

A pull request is complete when it contains, as far as they apply:

- **Tests.** A behaviour change comes with tests; a bug fix with a test that fails without the fix. Go tests live next
  to the code (`*_test.go`); the integration lives in `custom_components/vordr/` at the root, where HACS looks for it,
  and its tests are in `ha/tests/` ([ADR-0144](docs/adr/0144-the-integration-lives-at-the-repository-root.md)). A
  refusal is tested as well as the accepted case.
- **Version.**
  - Agent and hub get their version at build time: `./build.sh <X.Y.Z>`. Agents only take a release newer than the one
    they run.
  - The integration's version is `VERSION` in `custom_components/vordr/const.py` and `version` in
    `custom_components/vordr/manifest.json`, kept in step.
  - Patch for fixes, minor for new behaviour while below 1.0.
- **Documentation.**
  - The design documents in `design/` describe the new behaviour. They link to the ADRs instead of repeating the
    reasons.
  - `README.md`, `ha/README.md` and `deploy/README.md` change where setup or use changes.
  - Every document changes in English and German in the same pull request (`name.md` and `name.de.md`, see
    "Language and names").
- **ADR** for every decision (see "Architecture decisions are recorded as ADRs").
- **Configuration.**
  - A new key goes into the example file (`agent.example.yaml`, `hub.example.yaml`) and into the validation that
    `check-config` runs; the policy tests check `agent.example.yaml`.
  - The agent's local policy stays closed by default: a new verb or scope does nothing until `agent.yaml` allows it.
  - An existing configuration keeps working without edits, or the change is marked breaking.
- **Texts in Home Assistant.** The hub sends codes, the integration has the texts in
  `custom_components/vordr/translations/en.json` and `custom_components/vordr/translations/de.json`, and in
  `custom_components/vordr/strings.json` (equal to `en.json`). `ha/tests/test_texts.py` checks that every file has
  every text.

## Running the checks

```bash
./build.sh                        # ADR index, site-neutral check, documents in both languages, gofmt, go vet
                                  # (Linux and Windows), go test (-race where gcc exists), binaries in dist/
ruff check custom_components ha && ruff format --check custom_components ha && python -m pytest -q
                                  # integration, from the root (see ha/README.md for the venv)
python3 docs/adr/check_index.py --write                              # regenerate the ADR index
python3 tools/check_neutral.py --text <file>                         # commit message or pull request text
```

- `ha/tests/test_e2e_real_hub.py` runs the real hub and agent from `dist/` and is skipped without them: run
  `./build.sh` first.
- Platform code lives in `*_windows.go` with a `*_linux.go` or `*_other.go` counterpart. File locking and the service
  manager only show on Windows. Build the test binary there with `GOOS=windows go test -c ./internal/<package>` and run
  it on a Windows machine from the package folder.

## Language and names

- Code, identifiers, comments, log and error messages, commit messages, pull request texts and ADRs are English.
- Every document exists in English as `name.md` and in German as `name.de.md` beside it, with the same outline; both
  change together ([ADR-0143](docs/adr/0143-every-document-in-english-and-german.md)). Each version links the other
  under its title and links the documents of its own language. `tools/check_docs.py` in `./build.sh` checks the pairs,
  their outline and every link. ADRs, this file and `CLAUDE.md` are English only.
- German texts use Swiss spelling (`ss`, never `ß`).
- Texts users see in Home Assistant come from the translations, never from the hub.

## Tools check their own preconditions

Scripts and commands take the caller's intent and do everything needed for it, including the checks. They verify
their preconditions before acting and refuse with the state and the next step instead of acting halfway. What can be
derived is derived instead of asked for. Examples:

- `vordr-agent install-service` refuses before any change.
- `deploy/install-agent.sh` checks the configuration before it replaces anything.
- `tools/check_neutral.py` stops without a term list and says how to go on.

A rule that only lives in documentation is the weakest protection; when the same mistake happens twice, the check
belongs in the tool. Exceptions are explicit options with a stated reason, never a silent switch.

## Explicit control flow: errors are not a control signal

- **Report state as a fact.** A component that knows something about itself reports it as a return value, status or
  field. Results travel as fixed codes, not texts
  ([ADR-0007](docs/adr/0007-fixed-codes-instead-of-error-texts.md)).
- **Check first, then act.** Check existence, format, capability and availability before the operation instead of
  attempting it and catching the failure.
- **Errors and exceptions only for real faults** that cannot be checked beforehand or whose check would race: network,
  file system, other processes, OS locks. Go returns them as values; Python catches them only at that boundary.
- **Never parse error text** to decide anything. When cases must be distinguished, they get a code, a field or a
  status.
- Where there is no other way, a comment says why.

## Security

Report vulnerabilities privately as `SECURITY.md` describes, not in a public issue
([ADR-0138](docs/adr/0138-vulnerabilities-are-reported-privately-on-github.md)). Changes to the signing of jobs and
releases, to the approval of hosts, to the agent's local policy or to its verbs need an ADR, and tests for the refused
case as well as the accepted one.

## Architecture decisions are recorded as ADRs

Every architecture or design decision is an ADR in `docs/adr/` (MADR 4.0, English; rules in
[ADR-0072](docs/adr/0072-decisions-as-adrs-checked-by-the-build.md)): technology and protocol choices, interfaces,
security boundaries, data stores, modes of operation, and deliberately rejected options (status `rejected`).

- A pull request that makes or changes a decision brings its ADR (`docs/adr/NNNN-title-in-kebab-case.md`, next free
  number) and names it in its description.
- A changed decision is a new ADR that supersedes the old one. Accepted ADRs are not rewritten; the old one only
  changes its status to `superseded by ADR-NNNN`. Typos, links and site details
  ([ADR-0099](docs/adr/0099-the-repository-is-site-neutral-checked-by-the-build.md)) may be corrected.
- Decisions made earlier without an ADR get one afterwards, marked "Backfilled on …, decided on …" with its source.
- Regenerate the index with `python3 docs/adr/check_index.py --write`; `./build.sh` checks it.
