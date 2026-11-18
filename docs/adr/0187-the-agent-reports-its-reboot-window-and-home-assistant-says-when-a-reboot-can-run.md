---
status: proposed
date: 2026-11-18
decision-makers: kay (2026-11-17, asked for it after a reboot refused outside the window); proposed by Claude
---

# The agent reports its reboot window, and Home Assistant says when a reboot can run

Refines [ADR-0154](0154-a-host-can-keep-reboots-inside-its-window-even-with-a-reason.md) and resolves its bad
consequence: Home Assistant offered the reboot outside the window, and only the result said no. The agent's own rules
for reboots ([ADR-0041](0041-reboot-with-window-and-proof.md), ADR-0154) do not change.

## Context and Problem Statement

A reboot through Vördr runs only inside `reboot.window` of the host's `agent.yaml`. Outside it the agent refuses with
`rejected_outside_window`. A reason (`outside_window`, [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)) opens
the window, except on a host with `reboot.window_only: true` (ADR-0154). The reboot button and the fix of the reboot
repair ([ADR-0132](0132-a-needed-reboot-is-a-repair-from-the-first-report.md)) carry no reason: the hub gives its
button reason only to installs, reboots keep to their window
([ADR-0109](0109-the-install-button-installs-at-once.md)). ADR-0154's context says otherwise; for reboots ADR-0109
holds. So a closed reboot window holds the button and the repair on every host that has one, and `window_only` decides
only about the action with a reason (`vordr.press_outside_window`).

Home Assistant does not know the window. It shows the reboot button and the repair's fix at any time; outside the
window the reboot is sent, the agent refuses it, and the person sees a refused run. Neither the button nor the refusal
says when the reboot would run, nor whether a reason would help.

How does Home Assistant learn the reboot window, and what does it do with it?

## Decision Drivers

* The agent's local policy has the last word ([ADR-0006](0006-local-policy-decides.md)); Home Assistant and the hub
  only learn about it.
* State travels as facts, fields and codes, not as texts to be read
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)); a refusal is not the way to learn a window.
* What can be checked before acting is checked before acting: no reboot is sent that the window refuses for sure.
* Hub, agent and integration can be updated in any order; an older part keeps today's behaviour.
* The pattern exists: the minimum interval reaches Home Assistant as `next_install`
  ([ADR-0110](0110-the-hub-checks-the-minimum-interval-before-the-backup.md)).

## Considered Options

1. The agent reports `reboot.window` and `reboot.window_only` in its hello, only to a hub that announces it takes them;
   the hub passes them on in the host view and computes `next_reboot`, when the closed window opens next; the
   integration checks it before it sends a reboot.
2. As 1, but the agent sends the window to every hub, and the hub is updated first.
3. The hub refuses a reboot without a reason while the reported window is closed, with a code of its own, as it does
   for installs whose agent takes no reason (ADR-0109).
4. Home Assistant hides the reboot button, or makes it unavailable, while the window is closed.
5. Leave the reboot to be refused, and only word the refusal better.

## Decision Outcome

Chosen option: **1**.

* **Agent:** the hello's policy carries `reboot_window` (days, start, end) and `reboot_window_only`, with `tz`, the
  host's time zone, also when the host has no install window. Only with `reboot.allowed` and a `reboot.window`: without
  either there is no window to tell. Only to a hub whose challenge names the feature `reboot_window`; any other hub
  gets the hello as before.
* **Hub:**
  * announces `reboot_window` in its challenge, before the hello that carries it, like `ack`
    ([ADR-0114](0114-agent-names-its-run-and-the-hub-acknowledges-results.md),
    [ADR-0067](0067-hub-announces-features.md));
  * puts `reboot_window` (`days`, `start`, `end`, `tz`, `window_only`) into the host view where it offers the reboot
    (capability `reboot`);
  * takes the reported window only within bounds: at most seven days, each once, a window `check-config` would take,
    and a zone name of at most 64 characters that the hub's time zone data knows. Otherwise the host counts as one
    that reports no window, and the event `reboot_window_refused` names why (`days`, `window` or `tz`). A window
    without a zone (the host does not name its own) is taken and passed on without a next opening;
  * computes `next_reboot` while the window is closed: the first minute at which the agent's own window check says
    yes, found by trying each minute in turn, for up to 15 days, in the host's zone. So it holds over midnight, on
    the window's days, and on the nights daylight saving time skips or repeats an hour - also for a window that ends
    in the repeated hour, or a weekly one the skipped hour takes away for a week. In UTC, absent while the window is
    open, without a reported window, and without a zone;
  * sets `next_reboot` with and without `window_only`: the button and the repair carry no reason either way;
  * refuses no reboot because of it: the agent decides, and the hub's own view and `vordr-hub show` only report.
* **Home Assistant:**
  * the reboot button and the reboot repair's fix look at `next_reboot` first. While it lies ahead, nothing is sent.
    The button raises an error, the repair aborts, both with a text that names the time in Home Assistant's local time
    and, on a host with `window_only`, that a reason does not open the window; otherwise it points to the action with
    a reason. The repair checks when its dialog opens and again at confirmation;
  * the action `vordr.press_outside_window` holds a reboot only on a host with `window_only`; elsewhere the reason
    opens the window, as before;
  * the reboot button shows `reboot_window`, `window_only` and `next_reboot` as attributes;
  * the notification blueprint `reboot_offer.yaml` ([ADR-0173](0173-notifications-are-shipped-as-blueprints-the-user-imports.md))
    does not press the button on a tap while the window is closed and says from when the reboot runs.
* **Older parts:** an older agent or hub sends neither field; an older integration ignores them. Each then behaves as
  before: the agent refuses outside the window, and the result says so.

Option 2 makes the order of updates matter: an older hub reads the hello strictly and refuses one with a field it does
not know, so the host would stay away. Option 3 places a third check of the same rule at the hub, and an older
integration would word the new code as the install case (`outside_window`, "the agent is too old"). Option 4 would
make the button come and go twice a day, take the fix from the repair, and still not say when. Option 5 leaves the
person to learn the window by being refused.

### Consequences

* Good, because the person learns before anything is sent that the reboot cannot run now, from when it can, and
  whether a reason would help - instead of a refused run.
* Good, because the facts come from the host's own `agent.yaml`, and the moment is the one the agent's own check uses.
* Good, because nothing changes on hosts without a reboot window, and every part can be updated first.
* Bad, because Home Assistant decides on the hub's last view: around the moment the window closes, a reboot can still
  be sent and be refused by the agent, as before. A difference between the clocks of Home Assistant and the hub moves
  the moment by that difference.
* Bad, because the hub computes the moment with its own time zone data; if the host's differs (a zone rule that
  changed), the two can disagree by the change. The agent still decides.
* Neutral: every approved Home Assistant client sees the reboot window. It is no secret: the window bounds when a reboot
  can run, and a client that sends reboots learns it from the agent's refusals anyway.
* Neutral: the minute-by-minute search costs at most 15 days of minutes for each host whose window is closed, each
  time the hub computes the host's view - a few milliseconds; in exchange it needs no rule of its own for the nights
  the clock changes. The bounds on the reported window keep each step small.

### Confirmation

* `internal/policy/policy_test.go` (`TestWindowOpens`): later the same day, the next morning, after the weekend, a
  week later, over midnight, in another zone, a start that daylight saving time skips and one it repeats, a window
  over midnight that ends in the repeated hour, a weekly window the skipped hour takes away - always the first
  minute `Contains` says yes. `TestWindowOpensIsTheFirstMinuteTheWindowSaysYes` compares `Opens` with a
  minute-by-minute search over an own reading of the window around each change of 2026 and weekly through the year,
  in four zones (forward and back, by an hour and by half an hour, north and south).
* `internal/agent/summary_test.go` (`TestSummaryReportsTheRebootWindow`): the window, `window_only` and the zone are
  reported; not without `reboot.allowed`, not without a window, not to an older hub.
* `internal/agent/handshake_e2e_test.go` (`TestAnOlderHubGetsNoRebootWindow`): a hub that does not announce the feature
  gets no reboot window in the hello.
* `internal/hub/rebootwindow_test.go` (`TestNextRebootFromTheAgentsWindow`): inside the window nothing, outside the next
  opening, with and without `window_only`, across midnight, in the host's zone; nothing where the reboot is not offered,
  from an older agent, without a zone, with an unknown zone or an invalid window.
  `TestTheHubTakesOnlyABoundedRebootWindow` and `TestAHelloKeepsOnlyABoundedRebootWindow`: eight days, a day twice,
  a hundred thousand days, an invalid window, a zone too long, unknown or a path are not taken, with their code in
  the event log; a bounded window and one without a zone are.
* `internal/agent/ha_e2e_test.go` (`TestRebootWindowReachesHomeAssistant`): agent and hub together, the view Home
  Assistant gets.
* `ha/tests/test_entities.py` (`test_reboot_button_waits_for_the_reboot_window`), `ha/tests/test_repairs.py`
  (`test_fix_waits_for_the_reboot_window`), `ha/tests/test_texts.py` (`test_reboot_window_texts`),
  `ha/tests/test_blueprints.py` (`test_reboot_offer_says_when_the_reboot_window_opens`): nothing sent while the window
  is closed, the text with the time in every language, the action with a reason held only with `window_only`, sent once
  the time has come and from a hub that does not say.

## More Information

* [ADR-0041](0041-reboot-with-window-and-proof.md): the reboot window.
* [ADR-0154](0154-a-host-can-keep-reboots-inside-its-window-even-with-a-reason.md): `reboot.window_only`.
* [ADR-0110](0110-the-hub-checks-the-minimum-interval-before-the-backup.md): `next_install`, the same kind of fact.
* Design: [design/architecture.md](../../design/architecture.md) (6.2, 10.2),
  [design/ha-integration.md](../../design/ha-integration.md) (6a, 6f).
