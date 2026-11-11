---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Ja, bauen", together with ADR-0129 after the check of two package problems that Home Assistant could not resolve); proposed by Claude
---

# Check npm tarball origins and let npm 12 fetch them

Refines [ADR-0070](0070-hub-vets-and-signs-the-update-plan.md) (the hub vets the update plan) for npm: besides the
hub's vet, the agent checks where every planned package comes from. Everything else in the pip/npm update (design
11.4) stays.

## Context and Problem Statement

npm 12 refuses to fetch "remote" dependencies, those that point to a tarball URL instead of a version, unless
`allow-remote` allows it (default `none`). It also refuses registry tarballs on some paths: when a global package
such as Node-RED pulls `npm` itself through one of its packages, npm 12.2.1 and 12.2.2 both stop the resolution with
`EALLOWREMOTE` for `https://registry.npmjs.org/npm/-/npm-<version>.tgz`, from the registry the host is configured
with.
npm's own source says that its parser takes such a spec for a remote one; it guards one code path against that, but not
the one the resolution takes.

Every npm update and dependency refresh of Vördr starts with that resolution (`npm install --package-lock-only`,
`npm update --package-lock-only`). On a host with npm 12, an update of a global Node-RED fails with
`failed_package_manager`, and so does every update whose tree contains `npm`. With the plan Vördr makes for such an
update, npm 11 resolves it, npm 12.2.1 and 12.2.2 refuse it, and both resolve it with `--allow-remote=all`. npm 11
accepts the option without effect.

What `allow-remote=none` protects against is a dependency that names a URL: code from a host that is not the registry,
outside the registry's checks, and outside what OSV knows by name and version. Vördr's plan check identifies entries by
name and version only (ADR-0070), so such a dependency must not slip through either.

## Decision Drivers

* An update of a global Node-RED, or of anything that brings `npm` along, works again on hosts with npm 12.
* No code from outside the configured registry enters a host through a Vördr update.
* The check does not depend on an npm version or on a bug being fixed; it works on npm 11 too.
* The agent knows where the plan's packages come from as a fact: the lock file npm writes for the plan.

## Considered Options

1. Wait for npm to fix it.
2. Pass `--allow-remote=all` and nothing else.
3. Pass `--allow-remote=all`, and check that every entry new in the plan comes from the configured registry.
4. Pass `--allow-remote=root`.

## Decision Outcome

Chosen: option 3.

* **npm gets `--allow-remote=all`** in every call of an update and a dependency refresh: the resolution of the plan,
  the install and the update in place.
* **The agent checks the plan's origin.** After npm wrote the plan's lock file, every entry of the plan (what the
  update brings that is not installed yet, by name and version) must either have no `resolved` URL (an entry bundled in
  its parent) or one on the configured registry: the same scheme and host, and a path below the registry's. Anything
  else, a URL of another host, git or a file, ends the run before the hub is asked and before anything is installed:
  `precheck_not_from_registry`, with the entries named in the detail. The code sets the problem sensor: someone has to
  look at where the dependency points.
* **The install takes the vetted plan as before:** the same `--before`, so npm resolves the same tree. That npm resolves
  it the same way is the assumption the update already rests on (design 11.4).

Rejected:

* **Option 1,** because fixes for findings can wait in exactly such an update, and an update that fails on every host
  with npm 12 until npm changes is no update path. npm 12.2.2 still fails.
* **Option 2,** because it gives up the protection npm 12 added, with nothing in its place.
* **Option 4,** because `root` allows only URLs named in the project's own `package.json`; the misread registry tarball
  is named nowhere, so the resolution fails as before.

### Consequences

* Good, because global Node-RED updates and refreshes in trees with `npm` work again on npm 12.
* Good, because a dependency from outside the registry stops the update on every npm version, not only on 12, and with
  its own code instead of a package manager error.
* Bad, because a site that deliberately installs a package from a URL cannot update that location through Vördr; it
  updates it by hand. Vördr never vetted such a package by name and version anyway.
* Bad, because it is a new result code: the hub and the integration must be released before the agent, as for every
  such code.
* Neutral: on npm 11 the option changes nothing.

### Confirmation

* `internal/inventory`:
  * `TestNpmCallsAllowRegistryTarballs`: the resolution, the install and the dependency refresh pass
    `--allow-remote=all`;
  * `TestNpmPlanFromAnotherHostIsRefused`: a plan entry resolved from another host, from git or from a file ends the
    update with `precheck_not_from_registry` before the vet and before any install; entries without `resolved`,
    entries already installed and entries on the registry pass;
  * the same for a dependency refresh.
* `internal/protocol`: the code is in the table with `problem`, and Home Assistant has its texts (`ha/tests/test_texts.py`).
