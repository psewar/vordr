---
status: accepted
date: 2026-10-30
decision-makers: kay (detect app containers), Claude (patterns, ids, update rule)
---

# Detect pip and npm packages inside MSIX app containers

## Context and Problem Statement

Desktop apps from the Microsoft Store, for example PowerShell, are MSIX apps. Whatever a process of such an app writes
to `%APPDATA%` or `%LOCALAPPDATA%` lands physically in the app's container,
`C:\Users\<user>\AppData\Local\Packages\<package family>\LocalCache\{Roaming|Local}`. Only processes of the same app see
it at the usual path. The Vördr service runs as LocalSystem outside every app, so `pip install --user` or
`npm install -g` run from inside such an app stays invisible to it.

Packages installed this way, vulnerable ones included, are missing from what the service reports. A dry run started
inside the app sees the merged view and counts more packages and more findings than the service. The operator first had
such a container entered in `agent.yaml` by hand, then asked for it to be detected in general.

## Decision Drivers

* The agent derives what it can derive; a manual entry per host and app gets forgotten (ADR-0065: `agent.yaml` is
  for project folders, not for places every Windows PC has).
* Code in such a container runs for the user like any other code: a vulnerable package there is a real finding.
* No new kind of location and no new protocol field, so the hub and Home Assistant need no change.

## Considered Options

1. Leave it to `packages.locations` in `agent.yaml`, per host and app.
2. Detect the containers of all MSIX apps as well-known places: `Roaming\npm\node_modules`,
   `Roaming\Python\*\site-packages`, and the `pip --user` folder of Python from the Store
   (`PythonSoftwareFoundation.Python.*\LocalCache\local-packages\*\site-packages`).
3. Read the inventory inside each user's session or app context (impersonation).

## Decision Outcome

Chosen option: **2**.
* **Ids** name the app and the user, like the place outside the container: `appname-pip-user-python312-bob`,
  `powershell-npm-global-alice`, `store-pip-user-python312-bob`. The app is the name part of the package family,
  without the publisher prefix.
* **Configured beats detected.** A path that `agent.yaml` also names is read once, under the configured id; Windows
  compares the two paths ignoring case.
* **Updates: the kind decides, as everywhere.** A container's global npm folder is updatable once
  `packages.update` names its id. `pip-site` stays read-only (ADR-0065, design 11.4). A container is just the user's npm
  prefix seen through an app. A separate "never in containers" rule would be a hidden special case, and
  `packages.update` already requires an explicit opt-in per location.

### Consequences

* Good, because what is installed from inside an MSIX app shows up without any `agent.yaml` entry, on every Windows
  host.
* Good, because the hub, the protocol and the integration stay as they are.
* Bad, because an agent run **inside** an MSIX app (for example `vordr-agent packages` started from a shell inside such
  an app) sees the app's files twice: merged into `AppData\Roaming` and again in the container. The service, the only
  thing that reports, runs outside every app. Diagnostic runs therefore start from a shell outside every app.
* Bad, because a host that configured the path by hand reports the configured id; when the entry is removed, the id
  changes to the detected one.
* Neutral: uninstalling an app deletes its container, and its findings disappear with it.

### Confirmation

* Tests `TestWindowsPlaces` (patterns and ids, any OS), `TestAppName`, and `TestWindowsContainersDetected` (Windows
  only: real folders, configured path written in other letter case).
* Mutation probe: 8 of 8 mutations caught.
* A run of `vordr-agent packages` on a Windows PC, started outside every app as the service sees the disk, shows:
  * without a manual `agent.yaml` entry: the container's packages under `appname-pip-user-python312-bob`, and
    `pip-user-python312-bob` with only the packages really installed there;
  * with the entry: the same total, the container read once under the configured id;
  * the same run from a shell inside the app: the container's packages counted twice, as stated under Consequences.

## Pros and Cons of the Options

### 1. `agent.yaml` per host

* Good, because nothing changes in the agent.
* Bad, because it has to be known and repeated per host and app, and it was forgotten exactly where it mattered.

### 2. Detect the containers

* Good, because it covers every MSIX app and every user, with one glob per place.
* Bad, because it relies on how Windows redirects AppData for packaged desktop apps (`LocalCache` below the package
  folder); should that layout change, the places find nothing, and nothing breaks.

### 3. Impersonation

* Good, because it would see exactly what each app sees.
* Bad, because it needs user tokens and an app context per app, which is much more code for the same files.

## More Information

`design/architecture.md` 11.2; ADR-0065 (scope of package locations), ADR-0066 (inventory from metadata).
