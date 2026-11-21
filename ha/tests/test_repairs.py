"""The reboot repair (ADR-0132): from the first report, fixable where the hub offers the reboot, gone once the host no
longer needs one; one repair for the long-pending reboot instead of two. The install-again repair (ADR-0148): a kept
failed install is fixable where the hub offers the install, and gone with the failure. The stale repair (ADR-0203):
updates pending too long that a button takes are fixable, and the fix sends that button's command. Each says why the
hub refused its command, as the buttons do. The waiting-host repair (ADR-0157): a host waiting for approval names the
command for the hub and goes once the hub decided; where the hub lets this Home Assistant approve hosts, its fix
approves the host with the fingerprint read on the host (ADR-0192)."""

from __future__ import annotations

from datetime import timedelta
from typing import Any

from homeassistant.components.repairs import repairs_flow_manager
from homeassistant.const import STATE_ON
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType
from homeassistant.helpers import issue_registry as ir
from homeassistant.setup import async_setup_component
from homeassistant.util import dt as dt_util
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr import client as client_module
from custom_components.vordr.const import DOMAIN
from custom_components.vordr.coordinator import local_time
from custom_components.vordr.repairs import APPROVE_REFUSALS, ApproveHostFlow

from .conftest import FakeHub, host_view, wait_until

CAN = {"check": True, "install": False, "install_security": False, "reboot": True, "agent_update": False}
CANNOT = {**CAN, "reboot": False}
NEEDED = {
    "required": True,
    "reason": "kernel",
    "packages": ["linux-image"],
    "checked": ["kernel"],
    "since": "2023-11-14T22:13:20Z",
}
NOT_NEEDED = {"required": False, "reason": "", "packages": [], "checked": ["kernel"]}
PENDING = {"reason": "reboot_pending", "reasons": ["reboot_pending"]}
INSTALLS = {**CAN, "install": True}
FAILED = {
    "state": "precheck_would_remove_packages",
    "job_id": "f1",
    "verb": "install",
    "scope": "all",
    "started": "2023-11-14T22:00:00Z",
    "finished": "2023-11-14T22:01:00Z",
    "log_excerpt": "the upgrade would remove:\n libexample1",
}
KEPT = {"reason": "precheck_would_remove_packages", "reasons": ["precheck_would_remove_packages"]}


async def setup(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    assert await async_setup_component(hass, "repairs", {})
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()


def issue(hass: HomeAssistant, issue_id: str) -> ir.IssueEntry | None:
    return ir.async_get(hass).async_get_issue(DOMAIN, issue_id)


async def start_fix(hass: HomeAssistant, issue_id: str) -> dict[str, Any]:
    manager = repairs_flow_manager(hass)
    assert manager is not None
    result = await manager.async_init(DOMAIN, data={"issue_id": issue_id})
    assert result["type"] is FlowResultType.FORM and result["step_id"] == "confirm"
    return result


async def confirm(hass: HomeAssistant, flow: dict[str, Any]) -> dict[str, Any]:
    manager = repairs_flow_manager(hass)
    assert manager is not None
    return await manager.async_configure(flow["flow_id"], {})


async def test_repair_from_the_first_report(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED)
    fake_hub.hosts["m"] = host_view("m", capabilities=CANNOT, reboot=NEEDED)
    fake_hub.hosts["u"] = host_view("u", capabilities=CAN)  # unknown: no signal answered
    fake_hub.hosts["n"] = host_view("n", capabilities=CAN, reboot=NOT_NEEDED)
    await setup(hass, entry)
    fixable = issue(hass, "reboot_h")
    assert fixable is not None and fixable.is_fixable and fixable.translation_key == "reboot_required"
    assert fixable.severity == ir.IssueSeverity.WARNING and fixable.translation_placeholders["node"] == "h"
    manual = issue(hass, "reboot_m")
    assert manual is not None and not manual.is_fixable and manual.translation_key == "reboot_required_manual"
    assert manual.translation_placeholders["since"] not in ("", "?")
    assert issue(hass, "reboot_u") is None and issue(hass, "reboot_n") is None
    assert issue(hass, "problem_h") is None  # needing a reboot is not yet a problem

    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NOT_NEEDED))
    await wait_until(lambda: issue(hass, "reboot_h") is None)
    await fake_hub.remove_host("m")
    await wait_until(lambda: issue(hass, "reboot_m") is None)


async def test_capability_decides_whether_it_can_be_fixed(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CANNOT, reboot=NEEDED)
    await setup(hass, entry)
    assert not issue(hass, "reboot_h").is_fixable
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED))
    await wait_until(lambda: issue(hass, "reboot_h").is_fixable)


LIBRARIES = {
    "required": True,
    "reason": "libraries",
    "checked": ["libraries"],
    "services": ["cron.service", "systemd"],
    "since": "2023-11-14T22:13:20Z",
}
KERNEL = {**NEEDED, "kernel_running": "6.1.0-35-amd64", "kernel_newest": "6.1.0-37-amd64"}


async def test_repair_names_its_reason(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0201: the reboot repair says why where the host's view has what that text names - the services still on
    replaced libraries, the two kernels, Windows Update - fixable or by hand; otherwise the general text. Its dialog
    names the same; a change of the services or the reason makes the repair again with them."""
    windows = {**NEEDED, "reason": "windows_update", "packages": [], "checked": ["windows_update"]}
    fake_hub.hosts["l"] = host_view("l", capabilities=CAN, reboot=LIBRARIES)
    fake_hub.hosts["lm"] = host_view("lm", capabilities=CANNOT, reboot=LIBRARIES)
    fake_hub.hosts["k"] = host_view("k", capabilities=CAN, reboot=KERNEL)
    fake_hub.hosts["km"] = host_view("km", capabilities=CANNOT, reboot=KERNEL)
    fake_hub.hosts["w"] = host_view("w", capabilities=CANNOT, reboot=windows)
    fake_hub.hosts["old"] = host_view("old", capabilities=CAN, reboot={**LIBRARIES, "services": []})  # an older agent
    await setup(hass, entry)
    for node, key in (
        ("l", "reboot_required_libraries"),
        ("lm", "reboot_required_manual_libraries"),
        ("k", "reboot_required_kernel"),
        ("km", "reboot_required_manual_kernel"),
        ("w", "reboot_required_manual_windows_update"),
        ("old", "reboot_required"),
    ):
        assert issue(hass, f"reboot_{node}").translation_key == key, node
    names = issue(hass, "reboot_lm").translation_placeholders
    assert names["services"] == "cron.service, systemd" and names["since"] not in ("", "?")
    names = issue(hass, "reboot_km").translation_placeholders
    assert names["kernel_running"] == "6.1.0-35-amd64" and names["kernel_newest"] == "6.1.0-37-amd64"

    await fake_hub.push_host(host_view("l", capabilities=CAN, reboot={**LIBRARIES, "services": ["dbus.service"]}))
    await wait_until(lambda: issue(hass, "reboot_l").translation_placeholders["services"] == "dbus.service")
    flow = await start_fix(hass, "reboot_l")
    assert flow["description_placeholders"] == {
        "node": "l",
        "services": "dbus.service",
        "kernel_running": "-",
        "kernel_newest": "-",
    }
    await fake_hub.push_host(host_view("l", capabilities=CAN, reboot=KERNEL))
    await wait_until(lambda: issue(hass, "reboot_l").translation_key == "reboot_required_kernel")


async def test_long_pending_reboot_is_one_repair(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Once the hub calls it a problem (ADR-0014), the reboot repair turns into an error and replaces the problem's
    repair; the problem sensor stays. A problem with another reason keeps its own repair."""
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED, problem=PENDING)
    await setup(hass, entry)
    assert issue(hass, "reboot_h").severity == ir.IssueSeverity.ERROR
    assert issue(hass, "problem_h") is None
    assert hass.states.get("binary_sensor.vordr_h_problem").state == STATE_ON

    both = {"reason": "stale", "reasons": ["stale", "reboot_pending"]}
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, problem=both))
    await wait_until(lambda: issue(hass, "problem_h") is not None)
    assert issue(hass, "reboot_h").severity == ir.IssueSeverity.ERROR


async def test_long_pending_without_a_reboot_report_keeps_the_problem_repair(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Without a definite "reboot required" there is no reboot repair to carry it, so the problem's repair stays."""
    fake_hub.hosts["h"] = host_view("h", capabilities=CANNOT, problem=PENDING)
    await setup(hass, entry)
    assert issue(hass, "reboot_h") is None
    assert issue(hass, "problem_h").translation_key == "problem_reboot_pending_manual"


async def test_fix_reboots_and_waits_for_the_run(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED)
    await setup(hass, entry)
    result = await confirm(hass, await start_fix(hass, "reboot_h"))
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert fake_hub.commands[-1]["command"] == "reboot" and fake_hub.commands[-1]["node"] == "h"
    assert issue(hass, "reboot_h") is None

    # The host still reports the reboot while it goes down: the repair does not come straight back.
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, pending=3))
    await wait_until(lambda: entry.runtime_data.data.hosts["h"]["pending"] == 3)
    await hass.async_block_till_done()
    assert issue(hass, "reboot_h") is None

    # The reboot ran, and the host still needs one (e.g. a newer kernel came meanwhile): the repair is back.
    ran = {"job_id": "r1", "verb": "reboot", "state": "ok", "started": "2023-11-15T01:00:00Z"}
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, last_run=ran))
    await wait_until(lambda: issue(hass, "reboot_h") is not None)


async def test_fix_refused_by_the_hub(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """A refusal says why where the reboot dialog has the button's text for the code, any other names its code. The
    window, the minimum interval and the backup check hold only installs: a reboot names those as a code too."""
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED)
    await setup(hass, entry)
    for code, reason, shown in (
        ("role_forbids", "command_refused_role_forbids", {"node": "h"}),
        ("queue_full", "command_refused", {"node": "h", "code": "queue_full"}),
        ("rate_limited", "command_refused", {"node": "h", "code": "rate_limited"}),
    ):
        fake_hub.command_code = code
        result = await confirm(hass, await start_fix(hass, "reboot_h"))
        assert result["type"] is FlowResultType.ABORT and result["reason"] == reason, code
        assert result["description_placeholders"] == shown, code
        assert issue(hass, "reboot_h") is not None  # an abort leaves the repair


async def test_fix_checks_again_at_confirmation(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """The repair may be older than the host's state: each change in between has its own reason, and no command
    goes out."""
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED)
    await setup(hass, entry)
    sent = len(fake_hub.commands)

    flow = await start_fix(hass, "reboot_h")
    await fake_hub.push_host(host_view("h", capabilities=CANNOT, reboot=NEEDED))
    await wait_until(lambda: not issue(hass, "reboot_h").is_fixable)
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "reboot_not_offered"

    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED))
    await wait_until(lambda: issue(hass, "reboot_h").is_fixable)
    flow = await start_fix(hass, "reboot_h")
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NOT_NEEDED))
    await wait_until(lambda: issue(hass, "reboot_h") is None)
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_longer_needed"
    assert len(fake_hub.commands) == sent


async def test_fix_waits_for_the_reboot_window(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0187: the fix sends the button's reboot, without a reason. While the host's reboot window is closed it sends
    nothing and says from when the reboot runs - when the dialog opens, and again at confirmation for a window that
    closed in between. The text names reboot.window_only where the host sets it. Once the time has come it reboots."""
    nxt = dt_util.utcnow().replace(microsecond=0) + timedelta(hours=10)
    window = {"days": ["mon", "tue", "wed", "thu", "fri"], "start": "06:00", "end": "16:00", "tz": "Europe/Paris"}
    closed = {"capabilities": CAN, "reboot": NEEDED, "next_reboot": nxt.isoformat()}
    fake_hub.hosts["h"] = host_view("h", **closed, reboot_window={**window, "window_only": True})
    await setup(hass, entry)
    manager = repairs_flow_manager(hass)
    assert manager is not None
    shown = {"node": "h", "next": local_time(nxt)}

    result = await manager.async_init(DOMAIN, data={"issue_id": "reboot_h"})
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "reboot_window_only"
    assert result["description_placeholders"] == shown
    await fake_hub.push_host(host_view("h", **closed, reboot_window={**window, "window_only": False}))
    await wait_until(lambda: not entry.runtime_data.data.hosts["h"]["reboot_window"]["window_only"])
    result = await manager.async_init(DOMAIN, data={"issue_id": "reboot_h"})
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "reboot_window_closed"
    assert result["description_placeholders"] == shown
    assert fake_hub.commands == [] and issue(hass, "reboot_h") is not None  # an abort leaves the repair

    # Opened inside the window, confirmed after it closed.
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, reboot_window=window))
    await wait_until(lambda: "next_reboot" not in entry.runtime_data.data.hosts["h"])
    flow = await start_fix(hass, "reboot_h")
    await fake_hub.push_host(host_view("h", **closed, reboot_window=window))
    await wait_until(lambda: "next_reboot" in entry.runtime_data.data.hosts["h"])
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "reboot_window_closed"
    assert result["description_placeholders"] == shown and fake_hub.commands == []

    # The time has come since the hub's last view: the fix reboots.
    past = dt_util.utcnow().replace(microsecond=0) - timedelta(seconds=5)
    await fake_hub.push_host(host_view("h", **{**closed, "next_reboot": past.isoformat()}, reboot_window=window))
    await wait_until(lambda: entry.runtime_data.data.hosts["h"]["next_reboot"] == past.isoformat())
    result = await confirm(hass, await start_fix(hass, "reboot_h"))
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert [c["command"] for c in fake_hub.commands] == ["reboot"] and "reason" not in fake_hub.commands[0]


async def test_kept_failed_install_is_one_fixable_repair(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """A problem that is nothing but the kept failed install shows as the install-again repair, which names its code
    and the run's detail; without the install offered the problem keeps its own repair, and another reason keeps it
    too."""
    fake_hub.hosts["h"] = host_view("h", capabilities=INSTALLS, problem=KEPT, failed_install=FAILED)
    fake_hub.hosts["m"] = host_view("m", capabilities=CAN, problem=KEPT, failed_install=FAILED)
    await setup(hass, entry)
    again = issue(hass, "install_again_h")
    assert again is not None and again.is_fixable and again.translation_key == "install_again"
    assert again.translation_placeholders == {
        "node": "h",
        "code": "precheck_would_remove_packages",
        "detail": "the upgrade would remove: libexample1",
    }
    assert issue(hass, "problem_h") is None
    assert hass.states.get("binary_sensor.vordr_h_problem").state == STATE_ON  # the sensor stays
    assert issue(hass, "install_again_m") is None
    assert issue(hass, "problem_m").translation_key == "problem_precheck_would_remove_packages"

    both = {"reason": "stale", "reasons": ["stale", "precheck_would_remove_packages"]}
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS, problem=both, failed_install=FAILED))
    await wait_until(lambda: issue(hass, "problem_h") is not None)
    assert issue(hass, "install_again_h") is not None


async def test_fix_installs_again_and_goes_with_the_failure(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=INSTALLS, problem=KEPT, failed_install=FAILED)
    await setup(hass, entry)
    flow = await start_fix(hass, "install_again_h")
    # The fix dialog carries the repair's text (hassfest allows a description or a fix flow): it names the failure.
    assert flow["description_placeholders"] == {
        "node": "h",
        "code": "precheck_would_remove_packages",
        "detail": "the upgrade would remove: libexample1",
    }
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert fake_hub.commands[-1]["command"] == "install" and fake_hub.commands[-1]["node"] == "h"
    # The run went through: the hub no longer keeps the failure, and no repair is left for it.
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS))
    await wait_until(lambda: issue(hass, "install_again_h") is None)
    assert issue(hass, "problem_h") is None


async def test_install_again_checks_again_at_confirmation(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Each change between opening and confirming has its own reason, and no command goes out."""
    fake_hub.hosts["h"] = host_view("h", capabilities=INSTALLS, problem=KEPT, failed_install=FAILED)
    await setup(hass, entry)
    sent = len(fake_hub.commands)

    flow = await start_fix(hass, "install_again_h")
    await fake_hub.push_host(host_view("h", capabilities=CAN, problem=KEPT, failed_install=FAILED))
    await wait_until(lambda: issue(hass, "install_again_h") is None and issue(hass, "problem_h") is not None)
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "install_not_offered"

    await fake_hub.push_host(host_view("h", capabilities=INSTALLS, problem=KEPT, failed_install=FAILED))
    await wait_until(lambda: issue(hass, "install_again_h") is not None and issue(hass, "problem_h") is None)
    flow = await start_fix(hass, "install_again_h")
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS))
    await wait_until(lambda: issue(hass, "install_again_h") is None)
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_longer_needed"
    assert len(fake_hub.commands) == sent


async def test_install_again_refused_says_why(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """A refusal by the hub says why, as the install button does: the minimum interval from when it works again
    (ADR-0110), the closed window for an agent that takes no reason (ADR-0109), the backup check what is missing
    (ADR-0096). Any other code is named as it is. Every abort leaves the repair."""
    nxt = dt_util.utcnow().replace(microsecond=0) + timedelta(hours=18)
    old = {"group": "vm/301", "last": "2023-11-14T22:13:20+00:00", "max_age_hours": 26, "state": "stale"}
    view = {"capabilities": INSTALLS, "problem": KEPT, "failed_install": FAILED, "next_install": nxt.isoformat()}
    fake_hub.hosts["h"] = host_view("h", **view, backup=old)
    await setup(hass, entry)

    fake_hub.command_code = "rate_limited"
    result = await confirm(hass, await start_fix(hass, "install_again_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused_rate_limited"
    assert result["description_placeholders"] == {"node": "h", "next": local_time(nxt)}
    assert local_time(nxt) != "?"
    assert fake_hub.commands[-1]["command"] == "install"
    assert issue(hass, "install_again_h") is not None

    fake_hub.command_code = "outside_window"
    result = await confirm(hass, await start_fix(hass, "install_again_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused_outside_window"
    assert result["description_placeholders"] == {"node": "h"}

    fake_hub.command_code = "backup_stale"
    result = await confirm(hass, await start_fix(hass, "install_again_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused_backup_stale"
    shown = result["description_placeholders"]
    assert shown["group"] == "vm/301" and shown["max"] == "26" and int(shown["age"]) > 26

    for code in ("queue_full", "outside_window_unsupported"):  # the latter comes only with a reason
        fake_hub.command_code = code
        result = await confirm(hass, await start_fix(hass, "install_again_h"))
        assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused", code
        assert result["description_placeholders"] == {"node": "h", "code": code}
    assert issue(hass, "install_again_h") is not None


STALE = {"reason": "stale", "reasons": ["stale"]}
SECURITY_BUTTON = {**CANNOT, "install_security": True}


async def test_stale_repair_installs_what_the_button_takes(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0203: updates pending too long that the install button takes make a fixable repair. Its dialog names how
    many of them the button takes, its fix sends the button's install, and the repair goes with the problem. The
    problem sensor keeps the stale text."""
    fake_hub.hosts["h"] = host_view("h", capabilities=INSTALLS, problem=STALE, installable=2, pending=3)
    await setup(hass, entry)
    stale = issue(hass, "problem_h")
    assert stale is not None and stale.is_fixable and stale.translation_key == "stale_install"
    assert stale.translation_placeholders == {"node": "h", "installable": "2", "pending": "3"}
    assert hass.states.get("binary_sensor.vordr_h_problem").attributes["next_step"] == "stale"
    flow = await start_fix(hass, "problem_h")
    assert flow["description_placeholders"] == {"node": "h", "installable": "2", "pending": "3"}
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert fake_hub.commands[-1]["command"] == "install" and fake_hub.commands[-1]["node"] == "h"
    assert "reason" not in fake_hub.commands[-1]  # as the button: the hub gives the reason outside the window
    # The run installed them: the problem ends, and with it the repair.
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS, pending=0, security=0, installable=0))
    await wait_until(lambda: issue(hass, "problem_h") is None)


async def test_stale_repair_sends_the_security_button_where_it_is_the_only_one(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Without the install button the hub counts the security updates as installable (ADR-0106): the fix sends the
    command of the button for security updates."""
    fake_hub.hosts["h"] = host_view("h", capabilities=SECURITY_BUTTON, problem=STALE, installable=1)
    await setup(hass, entry)
    assert issue(hass, "problem_h").is_fixable
    result = await confirm(hass, await start_fix(hass, "problem_h"))
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert fake_hub.commands[-1]["command"] == "install_security" and fake_hub.commands[-1]["node"] == "h"


async def test_stale_repair_without_a_fix(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """No fix where no button takes the pending updates (the texts of ADR-0197), where the hub sends no count (an older
    hub), where the host offers no install button at all, and where the problem has another reason besides stale:
    installing would not end it."""
    other = {"reason": "stale", "reasons": ["stale", "offline"]}
    fake_hub.hosts["policy"] = host_view("policy", capabilities=INSTALLS, problem=STALE, installable=0)
    fake_hub.hosts["older"] = host_view("older", capabilities=INSTALLS, problem=STALE)
    fake_hub.hosts["none"] = host_view("none", capabilities=CANNOT, problem=STALE, installable=2)
    fake_hub.hosts["both"] = host_view("both", capabilities=INSTALLS, problem=other, installable=2)
    await setup(hass, entry)
    for node, key in (
        ("policy", "problem_stale_policy"),
        ("older", "problem_stale"),
        ("none", "problem_stale"),
        ("both", "problem_stale"),
    ):
        found = issue(hass, f"problem_{node}")
        assert found is not None and found.translation_key == key and not found.is_fixable, node


async def test_stale_install_checks_again_at_confirmation(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Each change between opening and confirming has its own reason, and no command goes out: the button takes none
    of the updates any more, or the problem is gone."""
    fake_hub.hosts["h"] = host_view("h", capabilities=INSTALLS, problem=STALE, installable=2)
    await setup(hass, entry)
    sent = len(fake_hub.commands)

    flow = await start_fix(hass, "problem_h")
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS, problem=STALE, installable=0))
    await wait_until(lambda: issue(hass, "problem_h").translation_key == "problem_stale_policy")
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "install_not_offered"

    await fake_hub.push_host(host_view("h", capabilities=INSTALLS, problem=STALE, installable=2))
    await wait_until(lambda: issue(hass, "problem_h").is_fixable)
    flow = await start_fix(hass, "problem_h")
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS))
    await wait_until(lambda: issue(hass, "problem_h") is None)
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_longer_needed"
    assert len(fake_hub.commands) == sent


async def test_stale_install_refused_says_why(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """A refusal by the hub says why, as the install button does; any other code is named as it is. Every abort leaves
    the repair and its fix."""
    nxt = dt_util.utcnow().replace(microsecond=0) + timedelta(hours=5)
    view = {"capabilities": INSTALLS, "problem": STALE, "installable": 2, "next_install": nxt.isoformat()}
    fake_hub.hosts["h"] = host_view("h", **view)
    await setup(hass, entry)

    fake_hub.command_code = "rate_limited"
    result = await confirm(hass, await start_fix(hass, "problem_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused_rate_limited"
    assert result["description_placeholders"] == {"node": "h", "next": local_time(nxt)}

    fake_hub.command_code = "queue_full"
    result = await confirm(hass, await start_fix(hass, "problem_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused"
    assert result["description_placeholders"] == {"node": "h", "code": "queue_full"}
    assert [c["command"] for c in fake_hub.commands[-2:]] == ["install", "install"]
    assert issue(hass, "problem_h").is_fixable


# ADR-0208: every pending update is a release the firewall offers only in another branch.
BRANCH = {
    "capabilities": INSTALLS,
    "problem": STALE,
    "installable": 0,
    "pending": 1,
    "security": 0,
    "kept_back": 1,
    "kept_back_why": "other_branch",
    "other_branch": "1.3.0",
}


async def test_hold_repair_holds_the_release(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0208: where the hub offers the hold, the repair of a release in another branch is fixable; its dialog names
    the release, its fix asks the hub to hold exactly it, and the repair goes with the problem. The problem sensor keeps
    the step of ADR-0207; the pending count names the held release."""
    fake_hub.features = ["hold_release"]
    fake_hub.hosts["fw"] = host_view("fw", **BRANCH)
    await setup(hass, entry)
    found = issue(hass, "problem_fw")
    assert found is not None and found.is_fixable and found.translation_key == "hold_release"
    assert found.translation_placeholders == {"node": "fw", "release": "1.3.0"}
    assert hass.states.get("binary_sensor.vordr_fw_problem").attributes["next_step"] == "stale_other_branch"
    flow = await start_fix(hass, "problem_fw")
    assert flow["description_placeholders"] == {"node": "fw", "release": "1.3.0"}
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert [(r["node"], r["to"]) for r in fake_hub.hold_requests] == [("fw", "1.3.0")]
    await wait_until(lambda: issue(hass, "problem_fw") is None)
    pending = hass.states.get("sensor.vordr_fw_pending_updates")
    assert pending.state == "1" and pending.attributes["held_release"] == "1.3.0"


async def test_hold_repair_needs_the_feature_and_the_release(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """No fix while the hub's status does not name hold_release (an older hub, role observe) - it follows the status at
    once - and none where the hub names no release (an older hub, more than one) or the problem is more than that."""
    fake_hub.hosts["fw"] = host_view("fw", **BRANCH)
    older = {k: v for k, v in BRANCH.items() if k != "other_branch"}
    fake_hub.hosts["older"] = host_view("older", **older)
    fake_hub.hosts["both"] = host_view(
        "both", **{**BRANCH, "problem": {"reason": "stale", "reasons": ["stale", "offline"]}}
    )
    await setup(hass, entry)
    found = issue(hass, "problem_fw")
    assert found is not None and not found.is_fixable and found.translation_key == "problem_stale_other_branch"
    await fake_hub.set_features(["hold_release"])
    await wait_until(lambda: issue(hass, "problem_fw").is_fixable)
    assert issue(hass, "problem_fw").translation_key == "hold_release"
    for node in ("older", "both"):
        assert not issue(hass, f"problem_{node}").is_fixable, node
    await fake_hub.set_features([])
    await wait_until(lambda: not issue(hass, "problem_fw").is_fixable)
    assert issue(hass, "problem_fw").translation_key == "problem_stale_other_branch"
    assert fake_hub.hold_requests == []


async def test_hold_checks_again_and_says_why(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """A newer release between opening and confirming: nothing is sent. A refusal by the hub says why; any other code
    is named as it is. Every abort leaves the repair and its fix."""
    fake_hub.features = ["hold_release"]
    fake_hub.hosts["fw"] = host_view("fw", **BRANCH)
    await setup(hass, entry)
    flow = await start_fix(hass, "problem_fw")
    await fake_hub.push_host(host_view("fw", **{**BRANCH, "other_branch": "1.3.1"}))
    await wait_until(lambda: issue(hass, "problem_fw").translation_placeholders["release"] == "1.3.1")
    result = await confirm(hass, flow)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_longer_needed"
    assert fake_hub.hold_requests == []

    names = {"node": "fw", "release": "1.3.1"}
    fake_hub.hold_code = "not_offered"
    result = await confirm(hass, await start_fix(hass, "problem_fw"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "hold_not_offered"
    assert result["description_placeholders"] == names
    fake_hub.hold_code = "store_failed"
    result = await confirm(hass, await start_fix(hass, "problem_fw"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "hold_refused"
    assert result["description_placeholders"] == {**names, "code": "store_failed"}
    assert len(fake_hub.hold_requests) == 2 and issue(hass, "problem_fw").is_fixable


FP_NEW = "ab" * 32
FP_OTHER = "cd" * 32


def waiting(node: str, fp: str, system: str = "TestOS 1") -> dict[str, str]:
    """A WaitingHost as the hub sends it in its own view."""
    return {"node": node, "fingerprint": fp, "os": system}


async def test_a_waiting_host_is_a_repair_with_the_approval_command(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0157: a warning per host waiting for approval, with the node, the fingerprint the hub saw and the command
    for the hub. Not fixable while the hub does not name approve_host (ADR-0192). None for an approved host, none for an
    entry a command could not carry as it is; the system the host names for itself is shown as one short line of
    code."""
    fake_hub.hosts["h"] = host_view("h")
    fake_hub.hub["waiting"] = [
        waiting("new-host", FP_NEW, "Debian GNU/Linux 13 (trixie)"),
        waiting("odd", FP_OTHER, "x`[look](https://example.org)`\n second line " + "y" * 80),
        waiting("Bad Name", FP_NEW),  # no host name
        waiting("short", "ab" * 8),  # no fingerprint as the hub shows one
        waiting("upper", FP_NEW.upper()),
        waiting("bare", FP_NEW, ""),
    ]
    await setup(hass, entry)
    new = issue(hass, "waiting_new-host")
    assert new is not None and not new.is_fixable and new.severity == ir.IssueSeverity.WARNING
    assert new.translation_key == "waiting_host"
    assert new.translation_placeholders == {
        "node": "new-host",
        "fingerprint": FP_NEW,
        "os": "Debian GNU/Linux 13 (trixie)",
        "command": f"vordr-hub approve new-host --fingerprint {FP_NEW}",
    }
    odd = issue(hass, "waiting_odd")
    assert odd is not None and odd.translation_placeholders["os"].startswith("x'[look](https://example.org)' second")
    assert "`" not in odd.translation_placeholders["os"] and len(odd.translation_placeholders["os"]) == 64
    assert issue(hass, "waiting_bare").translation_placeholders["os"] == "?"
    for node in ("h", "Bad Name", "short", "upper"):
        assert issue(hass, f"waiting_{node}") is None, node
    # The sensor keeps listing every waiting host as the hub sent it.
    assert hass.states.get("sensor.vordr_hub_pending_approvals").state == "6"


async def test_the_waiting_repair_goes_on_approval_and_on_removal(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW), waiting("other", FP_OTHER)]
    await setup(hass, entry)
    assert issue(hass, "waiting_new-host") is not None and issue(hass, "waiting_other") is not None
    # Approved at the hub: it leaves the waiting list and comes as a host.
    await fake_hub.push_hub(waiting=[waiting("other", FP_OTHER)])
    await fake_hub.push_host(host_view("new-host"))
    # Two messages, two conditions: the repair goes with the hub's, the host's entities come with the host's.
    await wait_until(
        lambda: (
            issue(hass, "waiting_new-host") is None
            and hass.states.get("binary_sensor.vordr_new_host_online") is not None
        )
    )
    assert issue(hass, "waiting_other") is not None
    # Rejected or forgotten at the hub: it leaves the waiting list and does not come as a host.
    await fake_hub.push_hub(waiting=[])
    await wait_until(lambda: issue(hass, "waiting_other") is None)


async def test_the_waiting_repair_follows_the_snapshot_after_a_reconnect(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Decided at the hub while Home Assistant was not connected: the snapshot on the next connection settles it."""
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW)]
    await setup(hass, entry)
    assert issue(hass, "waiting_new-host") is not None
    fake_hub.hub["waiting"] = [waiting("later", FP_OTHER)]
    await fake_hub.drop_connections()
    await wait_until(lambda: issue(hass, "waiting_new-host") is None and issue(hass, "waiting_later") is not None)


# --- approving a waiting host from its repair (ADR-0192)

APPROVES = ["agent_download", "approve_host"]


def fixable_waiting(hass: HomeAssistant, node: str) -> bool:
    found = issue(hass, f"waiting_{node}")
    return found is not None and found.is_fixable and found.translation_key == "waiting_host_approve"


async def approve_with(hass: HomeAssistant, flow: dict[str, Any], fingerprint: str) -> dict[str, Any]:
    manager = repairs_flow_manager(hass)
    assert manager is not None
    return await manager.async_configure(flow["flow_id"], {"fingerprint": fingerprint})


async def test_the_waiting_repair_is_fixable_only_while_the_hub_names_the_feature(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Without approve_host the repair is ADR-0157's, not fixable. Once the hub's status names it (a role change at
    the hub), the repair turns fixable with its own text at once, without waiting for a change of the hub's view; the
    names stay. When the hub stops naming it, the repair is not fixable any more."""
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW)]
    await setup(hass, entry)
    plain = issue(hass, "waiting_new-host")
    assert plain is not None and not plain.is_fixable and plain.translation_key == "waiting_host" and plain.data is None
    names = plain.translation_placeholders

    await fake_hub.set_features(APPROVES)
    await wait_until(lambda: fixable_waiting(hass, "new-host"))
    fixable = issue(hass, "waiting_new-host")
    assert fixable.data == {"kind": "approve_host", "entry_id": entry.entry_id, "node": "new-host"}
    assert fixable.translation_placeholders == names and fixable.severity == ir.IssueSeverity.WARNING
    assert fake_hub.approve_requests == []

    await fake_hub.set_features(["agent_download"])
    await wait_until(lambda: not issue(hass, "waiting_new-host").is_fixable)
    assert issue(hass, "waiting_new-host").translation_key == "waiting_host"
    assert issue(hass, "waiting_new-host").data is None


async def test_the_fix_asks_for_the_hosts_fingerprint(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """The fix refuses in the form, sending nothing, anything that is no fingerprint and a fingerprint other than the
    one the hub saw. Spaces and upper case do not matter: the matching one goes to the hub, the host is approved and the
    repair goes."""
    fake_hub.features = APPROVES
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW, "Debian GNU/Linux 13 (trixie)")]
    await setup(hass, entry)
    assert fixable_waiting(hass, "new-host")
    flow = await start_fix(hass, "waiting_new-host")
    assert flow["description_placeholders"] == issue(hass, "waiting_new-host").translation_placeholders
    assert [str(k) for k in flow["data_schema"].schema] == ["fingerprint"]
    for given, error in (
        ("not a fingerprint", "invalid_fingerprint"),
        (FP_NEW[:-1], "invalid_fingerprint"),
        (FP_NEW[:-1] + "g", "invalid_fingerprint"),
        (f"SHA256:{FP_NEW}", "invalid_fingerprint"),
        (FP_OTHER, "fingerprint_differs"),
        (FP_OTHER.upper(), "fingerprint_differs"),
    ):
        result = await approve_with(hass, flow, given)
        assert result["type"] is FlowResultType.FORM and result["errors"] == {"fingerprint": error}, given
    assert fake_hub.approve_requests == []
    assert issue(hass, "waiting_new-host") is not None

    spaced = " ".join(FP_NEW[i : i + 8] for i in range(0, 64, 8)).upper()
    result = await approve_with(hass, flow, f"  {spaced}\n")
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert [(r["node"], r["fingerprint"]) for r in fake_hub.approve_requests] == [("new-host", FP_NEW)]
    await wait_until(lambda: issue(hass, "waiting_new-host") is None)


async def test_the_fix_says_why_the_hub_refused(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Each code the hub answers has its own text; any other is named as it is. A hub that does not answer in time
    has its own text too. Every refusal leaves the repair."""
    monkeypatch.setattr(client_module, "COMMAND_TIMEOUT", 0.3)
    fake_hub.features = APPROVES
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW)]
    await setup(hass, entry)
    hub_codes = sorted(APPROVE_REFUSALS - {"no_answer"})
    assert set(hub_codes) == {
        "ha_approval_off",
        "client_not_approved",
        "role_forbids",
        "unknown_node",
        "not_waiting",
        "fingerprint_mismatch",
        "bad_request",
        "store_failed",
    }
    for code in hub_codes:
        fake_hub.approve_code = code
        result = await approve_with(hass, await start_fix(hass, "waiting_new-host"), FP_NEW)
        assert result["type"] is FlowResultType.ABORT and result["reason"] == f"approve_{code}", code
        assert result["description_placeholders"] == {"node": "new-host"}
    fake_hub.approve_code = "some_future_code"
    result = await approve_with(hass, await start_fix(hass, "waiting_new-host"), FP_NEW)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "approve_refused"
    assert result["description_placeholders"] == {"node": "new-host", "code": "some_future_code"}
    fake_hub.approve_code = None  # no answer
    result = await approve_with(hass, await start_fix(hass, "waiting_new-host"), FP_NEW)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "approve_no_answer"
    assert len(fake_hub.approve_requests) == len(hub_codes) + 2
    assert fixable_waiting(hass, "new-host")


async def test_the_fix_checks_again_before_it_sends(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """The dialog may be older than the hub's state: a host decided meanwhile, a hub that no longer lets Home Assistant
    approve, a connection that ended - each its own reason, and nothing is sent."""
    fake_hub.features = APPROVES
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW), waiting("other", FP_OTHER)]
    await setup(hass, entry)

    flow = await start_fix(hass, "waiting_new-host")
    await fake_hub.push_hub(waiting=[waiting("other", FP_OTHER)])
    await wait_until(lambda: issue(hass, "waiting_new-host") is None)
    result = await approve_with(hass, flow, FP_NEW)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_longer_waiting"
    assert result["description_placeholders"] == {"node": "new-host"}

    flow = await start_fix(hass, "waiting_other")
    await fake_hub.set_features(["agent_download"])
    await wait_until(lambda: not entry.runtime_data.client.offers_host_approval)
    result = await approve_with(hass, flow, FP_OTHER)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "approval_not_offered"

    await fake_hub.set_features(APPROVES)
    await wait_until(lambda: fixable_waiting(hass, "other"))
    flow = await start_fix(hass, "waiting_other")
    await fake_hub.shut_down()
    await wait_until(lambda: not entry.runtime_data.connected)
    result = await approve_with(hass, flow, FP_OTHER)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "hub_not_connected"
    assert fake_hub.approve_requests == []
    # Not connected, the hub names nothing: the repair is not fixable until it does again.
    assert not issue(hass, "waiting_other").is_fixable


async def test_the_fix_of_a_host_that_no_longer_waits_ends_at_once(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Opened for a host the hub no longer lists (the repair was older): it says so instead of asking."""
    fake_hub.features = APPROVES
    fake_hub.hub["waiting"] = [waiting("new-host", FP_NEW)]
    await setup(hass, entry)
    flow = ApproveHostFlow(entry.entry_id, "gone-host")
    flow.hass = hass
    result = await flow.async_step_init()
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_longer_waiting"
    flow = ApproveHostFlow("no-such-entry", "new-host")
    flow.hass = hass
    result = await flow.async_step_init()
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "hub_not_connected"
    assert fake_hub.approve_requests == []
