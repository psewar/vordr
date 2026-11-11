"""The reboot repair (ADR-0132): from the first report, fixable where the hub offers the reboot, gone once the host no
longer needs one; one repair for the long-pending reboot instead of two. The install-again repair (ADR-0148): a kept
failed install is fixable where the hub offers the install, and gone with the failure."""

from __future__ import annotations

from typing import Any

from homeassistant.components.repairs import repairs_flow_manager
from homeassistant.const import STATE_ON
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType
from homeassistant.helpers import issue_registry as ir
from homeassistant.setup import async_setup_component
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr.const import DOMAIN

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
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED)
    await setup(hass, entry)
    fake_hub.command_code = "role_forbids"
    result = await confirm(hass, await start_fix(hass, "reboot_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused"
    assert result["description_placeholders"] == {"node": "h", "code": "role_forbids"}
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
    result = await confirm(hass, await start_fix(hass, "install_again_h"))
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert fake_hub.commands[-1]["command"] == "install" and fake_hub.commands[-1]["node"] == "h"
    # The run went through: the hub no longer keeps the failure, and no repair is left for it.
    await fake_hub.push_host(host_view("h", capabilities=INSTALLS))
    await wait_until(lambda: issue(hass, "install_again_h") is None)
    assert issue(hass, "problem_h") is None


async def test_install_again_checks_again_at_confirmation(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Each change between opening and confirming has its own reason, and no command goes out; a refusal of the hub
    leaves the repair."""
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

    await fake_hub.push_host(host_view("h", capabilities=INSTALLS, problem=KEPT, failed_install=FAILED))
    await wait_until(lambda: issue(hass, "install_again_h") is not None)
    fake_hub.command_code = "role_forbids"
    result = await confirm(hass, await start_fix(hass, "install_again_h"))
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "command_refused"
    assert result["description_placeholders"] == {"node": "h", "code": "role_forbids"}
    assert issue(hass, "install_again_h") is not None
