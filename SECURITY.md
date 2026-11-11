# Security policy

Vördr installs updates and reboots hosts on signed orders, so flaws in signing, host approval, the agent's local
policy or its verbs matter. Please report them privately.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: on the repository's **Security** tab, choose **Report a vulnerability**
([ADR-0138](docs/adr/0138-vulnerabilities-are-reported-privately-on-github.md)). Please do not open a public issue.

Helpful to include:

- the component (`vordr-agent`, `vordr-hub`, `vordr-release`, the Home Assistant integration) and its version,
- what an attacker needs (network position, an approved client, access to the hub, …) and what they gain,
- steps or a test that shows it.

You get an answer as soon as the maintainer has looked at it. A fix is published together with a security advisory
that credits you, unless you prefer otherwise.

## Supported versions

Fixes go into the latest release of the integration and the current agent and hub. Older versions are not patched;
update instead.

## Scope

The design and its trust boundaries are described in `design/architecture.md` (section 5, the threat model, in German)
and in the ADRs in `docs/adr/`. Findings in third-party software that Vördr only reports on (packages, images, firmware) belong to their
projects.
