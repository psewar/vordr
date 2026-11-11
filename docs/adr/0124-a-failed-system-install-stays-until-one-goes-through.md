---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Ja, bauen", after the check of a point noted on 2026-11-05 showed it still open); proposed by Claude
---

# A failed system install stays until one goes through

## Context and Problem Statement

The hub keeps one last run per host and derives the host's problem from it: a result code with `problem` (design 7.3)
sets the problem sensor in Home Assistant. The automatic rings use the same problem: later rings and the rollout of a
new agent release wait while a canary has a problem
([ADR-0058](0058-site-decision.md),
[ADR-0107](0107-ring-delay-per-update-ended-by-a-clean-canary-run.md)).

Every job's final result replaces the last run: a check, an install of pip or npm packages, of container images
([ADR-0119](0119-update-container-images-by-button.md)) and an agent update as well as an install of the system
packages. So a failed install of the system packages disappears with the next job of any kind. On a canary:

1. The automatic install in the window fails with `failed_health`.
2. Later someone presses "Check now", or the image button.
3. The problem sensor turns off without anyone having looked.
4. After `delay_hours`, the later rings get the very updates the canary failed with.

The point had been noted earlier and was checked again: still open. With updates by button for packages and
images, more jobs run on canaries than before, so it became more likely.

## Decision Drivers

* A failed install must stay visible until something shows it is resolved.
* The rings exist to stop at a failing canary; nothing but a successful install should let them go on.
* A check or an update of another kind says nothing about whether the system packages install now.
* No change to agent or protocol; the hub has every result already.

## Considered Options

1. Leave it: the last run decides.
2. Keep the last failed system install apart from the last run; it stays a reason of the problem until a system install
   with at least its scope goes through.
3. The same, cleared only by the operator (a command or a button).
4. Let jobs of other kinds not replace the last run.

## Decision Outcome

Chosen: option 2.

* **What is kept:** an install with scope `security` or `all` that ends with a code with `problem` becomes the host's
  failed install, beside its last run, in the register (survives a restart).
* **What clears it:** an install with scope `security` or `all` that goes through (`ok`, `ok_reboot_required`), changed
  packages or not, when its scope covers the failed one's: `all` clears any, `security` only a failed `security`
  install. A later failed system install replaces it.
* **What leaves it:** every other job (check, image, pip/npm, agent update, reboot, backup), a refusal without
  `problem` (outside the window, busy), and a run still going.
* **View:** its code stays among the reasons of the problem, once, as long as it is kept; the sensor "Last run" shows
  the latest job as before. The automatic rings and the agent rollout follow the problem, so a canary's kept failure
  holds them.
* To clear it after fixing the cause by hand, the operator installs again (the button): a run that goes through, even one
  with nothing left to install, clears it.

Rejected:

* **Option 1,** because a failure that vanishes with the next check defeats the canaries.
* **Option 3,** because the install button already is that action, and it proves the fix instead of only asserting it;
  a separate acknowledgement would need a new command or protocol message.
* **Option 4,** because the last run is what the sensor "Last run" shows, and a check or image update must appear there.

### Consequences

* Good, because a canary's failure holds the rings until its system install goes through.
* Good, because the problem sensor in Home Assistant stays on for any host's failed system install until it is resolved.
* Bad, because Home Assistant names the failed install only by its code; its detail and log stay in the agent's run log
  until a later change shows them.
* Bad, because a host whose failure cannot be fixed now keeps its problem until it can; that is the purpose.
* Neutral: a security install that goes through does not clear a failed install of `all`: the updates outside the
  security archives may still fail.

### Confirmation

* `internal/hub`:
  * `TestFailedInstallStays`: kept after a check, an image, package and agent update, a refusal, a run still going and
    a narrower install that went through, and after a restart of the hub; cleared by an install of `all` that went
    through, and a failed `security` install by a `security` install that went through; named once.
  * `TestAutoInstallHeldByAFailedCanaryInstall`: a check on the failed canary does not release the later rings; its
    install that goes through does.
  * `TestFailedInstallReasonIsACode`: the reason is the code, the last run the latest job.
* After the rollout: a canary whose install fails stays a problem in Home Assistant after a check.
