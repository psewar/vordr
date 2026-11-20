"""Entities, ids, unknown values, availability, commands, events and repairs - against the fake hub."""

from __future__ import annotations

from datetime import timedelta
import json
from typing import Any

from homeassistant.components.recorder.db_schema import MAX_STATE_ATTRS_BYTES, StateAttributes
from homeassistant.const import (
    EVENT_STATE_CHANGED,
    STATE_OFF,
    STATE_ON,
    STATE_UNAVAILABLE,
    STATE_UNKNOWN,
    EntityCategory,
)
from homeassistant.core import Event, HomeAssistant
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers import device_registry as dr, entity_registry as er, issue_registry as ir
from homeassistant.helpers.json import json_bytes
from homeassistant.util import dt as dt_util
from homeassistant.util.json import json_loads
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry, async_capture_events
import voluptuous as vol

from custom_components.vordr.const import DOMAIN, EVENT_PROBLEM, EVENT_RUN_FINISHED
from custom_components.vordr.coordinator import local_time
from custom_components.vordr.diagnostics import async_get_config_entry_diagnostics, hub_address

from .conftest import FakeHub, host_view, wait_until

HOST_IDS = [
    "binary_sensor.vordr_golf_online",
    "binary_sensor.vordr_golf_problem",
    "binary_sensor.vordr_golf_reboot_required",
    "sensor.vordr_golf_last_report",
    "sensor.vordr_golf_last_run",
    "sensor.vordr_golf_pending_updates",
    "sensor.vordr_golf_security_updates",
    "update.vordr_golf_system",
    "button.vordr_golf_check",
]
HUB_IDS = [
    "binary_sensor.vordr_hub_online",
    "sensor.vordr_hub_hosts_online",
    "sensor.vordr_hub_hosts_with_problem",
    "sensor.vordr_hub_hosts_with_updates",
    "sensor.vordr_hub_hosts_with_security_updates",
    "sensor.vordr_hub_hosts_needing_reboot",
    "sensor.vordr_hub_pending_approvals",
    "sensor.vordr_hub_connection",
]


async def setup(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()


async def test_entity_ids_as_mqtt_had_them(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = host_view("golf")
    fake_hub.hosts["charlie-long-name"] = host_view("charlie-long-name")
    await setup(hass, entry)
    for eid in [*HOST_IDS, *HUB_IDS, "update.vordr_charlie_long_name_system"]:
        assert hass.states.get(eid) is not None, eid
    reg = dr.async_get(hass)
    dev = reg.async_get_device_by_identifier((DOMAIN, "host:golf"), entry.entry_id)
    hub = reg.async_get_device_by_identifier((DOMAIN, "hub"), entry.entry_id)
    assert dev is not None and dev.name == "Vördr golf" and dev.sw_version == "1.0"
    assert dev.via_device_id == hub.id
    assert hub.sw_version == "test"  # the hub's version arrives with its view, after the device was created
    assert hub.name == "Vördr hub"  # translated, in HA's system language (en in tests)
    assert hass.states.get("binary_sensor.vordr_hub_online").state == STATE_ON
    assert hass.states.get("sensor.vordr_hub_connection").state == "approved"


async def test_values_and_unknowns(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = host_view("golf")
    await setup(hass, entry)
    upd = hass.states.get("update.vordr_golf_system")
    assert upd.state == STATE_ON
    assert upd.attributes["installed_version"] == "TestOS 1"
    assert upd.attributes["latest_version"] == "2 pending (1 security)"
    assert upd.attributes["release_summary"] == "a 1 → 2 (security), b 1 → 2"
    assert upd.attributes["supported_features"] == 0  # no install capability: no install bit
    assert hass.states.get("sensor.vordr_golf_pending_updates").state == "2"
    assert hass.states.get("binary_sensor.vordr_golf_reboot_required").state == STATE_UNKNOWN
    assert hass.states.get("binary_sensor.vordr_golf_problem").state == STATE_OFF

    # A failed check: pending and latest version are unknown, never "up to date".
    await fake_hub.push_host(
        host_view(
            "golf",
            pending=None,
            security=None,
            system={"installed_version": "TestOS 1"},
            reboot={"required": True, "reason": "kernel", "checked": ["kernel"]},
            problem={
                "reason": "check_failed",
                "reasons": ["check_failed"],
                "check_failed_code": "precheck_repo_unreachable",
            },
        )
    )
    await wait_until(lambda: hass.states.get("update.vordr_golf_system").state == STATE_UNKNOWN)
    assert hass.states.get("sensor.vordr_golf_pending_updates").state == STATE_UNKNOWN
    assert hass.states.get("binary_sensor.vordr_golf_reboot_required").state == STATE_ON
    prob = hass.states.get("binary_sensor.vordr_golf_problem")
    assert prob.state == STATE_ON and prob.attributes["check_failed_code"] == "precheck_repo_unreachable"
    # Codes only; the frontend translates them. What to do depends on the code behind check_failed.
    assert prob.attributes["reason"] == "check_failed" and prob.attributes["next_step"] == "precheck_repo_unreachable"
    assert hass.states.get("update.vordr_golf_system").attributes["release_summary"] is None
    assert hass.states.get("sensor.vordr_hub_hosts_with_problem").state == "1"


async def test_the_hub_counts_the_hosts_to_update(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0188: the hub device counts the hosts with updates (system, images, pip/npm), with security updates and
    with a reboot due, names them, and lists apart the hosts for which it cannot be said (no report yet, a failed
    system check); a comparison the host does not make (images or packages not compared) is no unknown. It follows
    every push."""
    current = {"pending": 0, "security": 0}
    fake_hub.hosts["alpha"] = host_view("alpha")  # 2 pending, 1 security
    fake_hub.hosts["bravo"] = host_view("bravo", **current, containers={"outdated": 0}, packages={"outdated": 0})
    fake_hub.hosts["charlie"] = host_view(
        "charlie",
        **current,
        containers={"outdated": 3},
        packages={"outdated": 1},
        reboot={"required": True, "reason": "kernel", "checked": ["kernel"], "since": "2026-11-10T00:00:00Z"},
    )
    fake_hub.hosts["delta"] = host_view("delta", pending=None, security=None)  # the check failed
    fake_hub.hosts["echo"] = host_view("echo", last_report=None, pending=None, security=None)  # no report yet
    fake_hub.hosts["foxtrot"] = host_view("foxtrot", pending=None, security=None, system=None)  # no package source
    # images and packages that the host does not compare: no count, and nothing unknown about it
    fake_hub.hosts["golf"] = host_view("golf", **current, containers={"outdated": None}, packages={"outdated": None})
    # an inventory before the first report: its outdated images count, the rest cannot be said yet
    fake_hub.hosts["hotel"] = host_view(
        "hotel", last_report=None, pending=None, security=None, containers={"outdated": 2}
    )
    await setup(hass, entry)

    updates = hass.states.get("sensor.vordr_hub_hosts_with_updates")
    assert updates.state == "3"
    assert updates.attributes["hosts"] == [
        {"node": "alpha", "system": 2},
        {"node": "charlie", "images": 3, "packages": 1},
        {"node": "hotel", "images": 2},
    ]
    assert updates.attributes["unknown"] == ["delta", "echo"]
    security = hass.states.get("sensor.vordr_hub_hosts_with_security_updates")
    assert security.state == "1" and security.attributes["hosts"] == [{"node": "alpha", "security": 1}]
    assert security.attributes["unknown"] == ["delta", "echo", "hotel"]
    reboot = hass.states.get("sensor.vordr_hub_hosts_needing_reboot")
    assert reboot.state == "1" and reboot.attributes["hosts"] == [{"node": "charlie", "since": "2026-11-10T00:00:00Z"}]
    assert reboot.attributes["unknown"] == ["echo", "hotel"]

    await fake_hub.push_host(host_view("alpha", **current))
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_hosts_with_security_updates").state == "0")
    assert hass.states.get("sensor.vordr_hub_hosts_with_updates").state == "2"


async def test_problem_shows_the_failed_install(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0124: a kept failed install is a reason by its code, and the problem sensor carries the run itself, with its
    detail and the run log on the host, while the sensor "Last run" shows the later check; not recorded."""
    failed = {
        "state": "failed_health",
        "job_id": "j1",
        "verb": "install",
        "scope": "all",
        "by": "auto",
        "started": "2023-11-14T04:00:00Z",
        "finished": "2023-11-14T04:05:00Z",
        "log_excerpt": "health: docker app-db not running",
        "summary": {"package_count": 3, "log_file": "/var/lib/vordr/runs/j1.log"},
    }
    fake_hub.hosts["golf"] = host_view(
        "golf",
        last_run={"state": "ok", "job_id": "j2", "verb": "check", "started": "2023-11-14T08:00:00Z"},
        failed_install=failed,
        problem={"reason": "failed_health", "reasons": ["failed_health"]},
    )
    await setup(hass, entry)
    prob = hass.states.get("binary_sensor.vordr_golf_problem")
    assert prob.state == STATE_ON and prob.attributes["reason"] == "failed_health"
    assert prob.attributes["failed_install"] == {
        "job_id": "j1",
        "scope": "all",
        "state": "failed_health",
        "by": "auto",
        "started": "2023-11-14T04:00:00Z",
        "finished": "2023-11-14T04:05:00Z",
        "log_excerpt": "health: docker app-db not running",
        "log_file": "/var/lib/vordr/runs/j1.log",
    }
    assert "failed_install" in prob.state_info["unrecorded_attributes"]
    assert hass.states.get("sensor.vordr_golf_last_run").state == "ok"


async def test_up_to_date_and_install_feature(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    caps = {
        "check": True,
        "install": True,
        "install_security": True,
        "reboot": True,
        "agent_update": False,
        "default_scope": "all",
        "apps": [{"id": "gitlab", "name": "GitLab"}],
    }
    fake_hub.hosts["h"] = host_view(
        "h",
        pending=0,
        security=0,
        capabilities=caps,
        system={"installed_version": "TestOS 1"},
    )
    await setup(hass, entry)
    upd = hass.states.get("update.vordr_h_system")
    assert upd.state == STATE_OFF and upd.attributes["supported_features"] & 1
    for eid in ("button.vordr_h_install_security", "button.vordr_h_reboot", "button.vordr_h_install_app_gitlab"):
        assert hass.states.get(eid) is not None, eid
    assert hass.states.get("button.vordr_h_agent_update") is None
    await fake_hub.push_host(host_view("h", capabilities=caps))  # now with pending updates
    await wait_until(lambda: hass.states.get("update.vordr_h_system").state == STATE_ON)
    await hass.services.async_call("update", "install", {"entity_id": "update.vordr_h_system"}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "install" and fake_hub.commands[-1]["node"] == "h"
    await hass.services.async_call("button", "press", {"entity_id": "button.vordr_h_install_app_gitlab"}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "install_app:gitlab"

    # Capability withdrawn by the host: the button goes, once the hub says so.
    await fake_hub.push_host(host_view("h", capabilities={**caps, "reboot": False}))
    await wait_until(lambda: hass.states.get("button.vordr_h_reboot") is None)
    assert er.async_get(hass).async_get("button.vordr_h_reboot") is None


async def test_install_progress(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """An install run shows in the update entity: in progress with the agent's percentage, then done."""
    caps = {"check": True, "install": True, "install_security": False, "reboot": False, "agent_update": False}
    fake_hub.hosts["h"] = host_view("h", capabilities={**caps, "default_scope": "all"})
    await setup(hass, entry)
    assert hass.states.get("update.vordr_h_system").attributes["supported_features"] == 1 | 4  # INSTALL | PROGRESS
    run = {"job_id": "j1", "verb": "install", "scope": "all", "by": "ha:bravo", "started": "2026-10-29T08:00:00Z"}
    installing = {**run, "state": "installing", "progress": 50, "item": "libc6:amd64"}
    await fake_hub.push_host(host_view("h", capabilities=caps, last_run=installing, queued=["check"]))
    await wait_until(lambda: hass.states.get("update.vordr_h_system").attributes["in_progress"])
    upd = hass.states.get("update.vordr_h_system")
    assert upd.attributes["update_percentage"] == 50
    last = hass.states.get("sensor.vordr_h_last_run")
    assert last.state == "installing" and last.attributes["item"] == "libc6:amd64" and last.attributes["progress"] == 50
    assert last.attributes["queued"] == ["check"]  # waiting at the hub until this run has ended

    runs = async_capture_events(hass, EVENT_RUN_FINISHED)
    summary = {
        "package_count": 68,
        "packages": ["libc6", "tzdata"],
        "seconds": 192,
        "log_file": "/var/lib/vordr/runs/j1.log",
        "outdated_services": ["dbus.service"],
    }
    done = {**run, "state": "ok_reboot_required", "summary": summary}
    reboot = {
        "required": True,
        "reason": "kernel",
        "checked": ["kernel"],
        "kernel_running": "6.8.0-136-generic",
        "kernel_newest": "6.8.0-147-generic",
        "services": ["dbus.service"],  # ADR-0201: still named under the kernel
    }
    await fake_hub.push_host(host_view("h", capabilities=caps, pending=0, security=0, last_run=done, reboot=reboot))
    await wait_until(lambda: not hass.states.get("update.vordr_h_system").attributes["in_progress"])
    upd = hass.states.get("update.vordr_h_system")
    assert upd.state == STATE_OFF and upd.attributes["update_percentage"] is None
    last = hass.states.get("sensor.vordr_h_last_run").attributes
    assert "progress" not in last
    # What the run did, on the sensor and in the event; the kernels with the reboot need.
    assert last["package_count"] == 68 and last["log_file"] == "/var/lib/vordr/runs/j1.log"
    assert last["outdated_services"] == ["dbus.service"]
    rr = hass.states.get("binary_sensor.vordr_h_reboot_required").attributes
    assert rr["kernel_running"] == "6.8.0-136-generic" and rr["kernel_newest"] == "6.8.0-147-generic"
    assert rr["services"] == ["dbus.service"]
    await wait_until(lambda: len(runs) == 1)
    assert runs[0].data["summary"] == summary
    # Who started it, on the sensor and in the event.
    assert last["by"] == "ha:bravo" and runs[0].data["by"] == "ha:bravo"
    # This host does not allow reboots through Vördr: the next step is a reboot by hand, not "with the button".
    assert "Von Hand neu starten" in runs[0].data["message"]["de"]


async def test_agent_update_entity(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """With a release at the hub every host gets "Vördr agent": installed against released version; installing asks
    the hub for the update now (it signs the job with the operator's release)."""
    caps = {"check": True, "install": False, "install_security": False, "reboot": False, "agent_update": True}
    fake_hub.hosts["h"] = host_view("h", capabilities=caps, agent_release="0.7.0")
    fake_hub.hosts["plain"] = host_view("plain")
    await setup(hass, entry)
    agent = hass.states.get("update.vordr_h_agent")
    assert agent.state == STATE_ON and agent.attributes["installed_version"] == "1.0"
    assert agent.attributes["latest_version"] == "0.7.0" and agent.attributes["supported_features"] == 1 | 4
    assert hass.states.get("update.vordr_plain_agent") is None  # the hub hands out no release for it
    assert hass.states.get("button.vordr_h_agent_update") is None  # the update entity replaces the button
    await hass.services.async_call("update", "install", {"entity_id": "update.vordr_h_agent"}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "agent_update" and fake_hub.commands[-1]["node"] == "h"
    running = {
        "job_id": "u1",
        "verb": "agent_update",
        "started": "2026-10-29T12:00:00Z",
        "state": "downloading",
        "progress": 10,
    }
    await fake_hub.push_host(host_view("h", capabilities=caps, agent_release="0.7.0", last_run=running))
    await wait_until(lambda: hass.states.get("update.vordr_h_agent").attributes["in_progress"])
    assert hass.states.get("update.vordr_h_agent").attributes["update_percentage"] == 10


async def test_agent_update_entity_without_a_binary(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """A release without a binary for the host's platform is not offered there (ADR-0102): the entity stays, but
    shows no update that would never come."""
    caps = {"check": True, "install": False, "install_security": False, "reboot": False, "agent_update": True}
    fake_hub.hosts["h"] = host_view("h", capabilities=caps, agent_release="0.7.0", agent_release_no_binary=True)
    await setup(hass, entry)
    agent = hass.states.get("update.vordr_h_agent")
    assert agent.state == STATE_OFF and agent.attributes["latest_version"] == "1.0"


async def test_reboot_hint_follows_the_capability(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    caps = {"check": True, "install": False, "install_security": False, "reboot": False, "agent_update": False}
    pending = {"reason": "reboot_pending", "reasons": ["reboot_pending"]}
    fake_hub.hosts["h"] = host_view("h", capabilities=caps, problem=pending)
    await setup(hass, entry)
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "problem_h")
    assert issue is not None and issue.translation_key == "problem_reboot_pending_manual"
    assert hass.states.get("binary_sensor.vordr_h_problem").attributes["next_step"] == "reboot_pending_manual"
    await fake_hub.push_host(host_view("h", capabilities={**caps, "reboot": True}, problem=pending))
    await wait_until(
        lambda: hass.states.get("binary_sensor.vordr_h_problem").attributes["next_step"] == "reboot_pending"
    )
    # The repair follows too, although the problem itself did not change.
    assert ir.async_get(hass).async_get_issue(DOMAIN, "problem_h").translation_key == "problem_reboot_pending"


async def test_stale_hint_names_a_way_that_exists(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0197: updates pending too long that the install button would not take get "install by hand" - with the
    reason - instead of "by hand or with the button". The repair and the problem sensor follow what the host lets Vördr
    install, without a new problem event: the problem itself stays the same."""
    none = {"check": True, "install": False, "install_security": False, "reboot": False, "agent_update": False}
    stale = {"reason": "stale", "reasons": ["stale"]}
    problems = async_capture_events(hass, EVENT_PROBLEM)
    fake_hub.hosts["h"] = host_view("h", capabilities=none, problem=stale, installable=0)
    await setup(hass, entry)
    reg = ir.async_get(hass)
    assert reg.async_get_issue(DOMAIN, "problem_h").translation_key == "problem_stale_manual"
    assert hass.states.get("binary_sensor.vordr_h_problem").attributes["next_step"] == "stale_manual"
    assert len(problems) == 1

    # Only security updates through Vördr, and none of the pending ones is one: the policy holds them.
    security = {**none, "install": True, "default_scope": "security"}
    await fake_hub.push_host(host_view("h", capabilities=security, problem=stale, installable=0, security=0))
    await wait_until(lambda: reg.async_get_issue(DOMAIN, "problem_h").translation_key == "problem_stale_policy")
    assert hass.states.get("binary_sensor.vordr_h_problem").attributes["next_step"] == "stale_policy"
    only_button = {**none, "install_security": True}
    await fake_hub.push_host(host_view("h", capabilities=only_button, problem=stale, installable=0, security=0))
    await hass.async_block_till_done()
    assert reg.async_get_issue(DOMAIN, "problem_h").translation_key == "problem_stale_policy"

    # The button takes some of them: the repair can install them (ADR-0203); the sensor names the button again.
    await fake_hub.push_host(host_view("h", capabilities=security, problem=stale, installable=1))
    await wait_until(lambda: reg.async_get_issue(DOMAIN, "problem_h").translation_key == "stale_install")
    assert reg.async_get_issue(DOMAIN, "problem_h").is_fixable
    assert hass.states.get("binary_sensor.vordr_h_problem").attributes["next_step"] == "stale"
    # A hub without the count: the text names the button, and there is no fix.
    await fake_hub.push_host(host_view("h", capabilities=security, problem=stale))
    await wait_until(lambda: reg.async_get_issue(DOMAIN, "problem_h").translation_key == "problem_stale")
    assert not reg.async_get_issue(DOMAIN, "problem_h").is_fixable
    assert len(problems) == 1


async def test_install_waits_for_a_backup(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """The hub's backup check shows as "last backup" per host, and its refusal says what is missing."""
    caps = {
        "check": True,
        "install": True,
        "install_security": False,
        "reboot": False,
        "agent_update": False,
        "default_scope": "all",
    }
    old = {"group": "vm/301", "last": "2023-11-14T22:13:20+00:00", "max_age_hours": 26, "state": "stale"}
    fake_hub.hosts["golf"] = host_view("golf", capabilities=caps, backup=old)
    fake_hub.hosts["charlie"] = host_view(
        "charlie", backup={"max_age_hours": 26, "state": "exempt", "exempt": "physical host"}
    )
    await setup(hass, entry)
    sensor = hass.states.get("sensor.vordr_golf_last_backup")
    assert sensor.state == "2023-11-14T22:13:20+00:00"
    assert sensor.attributes["state"] == "stale" and sensor.attributes["group"] == "vm/301"
    assert hass.states.get("sensor.vordr_charlie_last_backup") is None  # no backup by decision: nothing to show

    fake_hub.command_code = "backup_stale"
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call("update", "install", {"entity_id": "update.vordr_golf_system"}, blocking=True)
    assert err.value.translation_key == "command_refused_backup_stale"
    assert err.value.translation_placeholders["group"] == "vm/301"
    assert err.value.translation_placeholders["max"] == "26"
    assert int(err.value.translation_placeholders["age"]) > 26
    fake_hub.command_code = "backup_unreachable"
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call("update", "install", {"entity_id": "update.vordr_golf_system"}, blocking=True)
    assert err.value.translation_key == "command_refused_backup_unreachable"


async def test_backup_host_refusal_names_its_state(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """A guest backed up by its hypervisor right before the install: the refusal names the hypervisor and the state
    that keeps its agent from the backup (ADR-0199); an older hub's single code keeps its text."""
    caps = {"check": True, "install": True, "install_security": False, "reboot": False, "agent_update": False}
    fresh = {"group": "ct/302", "max_age_hours": 26, "state": "ok", "before_install": "hotel"}
    fake_hub.hosts["golf"] = host_view("golf", capabilities={**caps, "default_scope": "all"}, backup=fresh)
    await setup(hass, entry)
    for code in (
        "backup_host_offline",
        "backup_host_unknown",
        "backup_host_not_approved",
        "backup_host_no_guest_backup",
        "backup_host_unavailable",
    ):
        fake_hub.command_code = code
        with pytest.raises(HomeAssistantError) as err:
            await hass.services.async_call(
                "update", "install", {"entity_id": "update.vordr_golf_system"}, blocking=True
            )
        assert err.value.translation_key == f"command_refused_{code}"
        assert err.value.translation_placeholders["hypervisor"] == "hotel"
        assert err.value.translation_placeholders["node"] == "golf"


async def test_package_entities_wait_for_a_report(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    fake_hub.hosts["new"] = host_view("new", last_report=None, pending=None, security=None, system=None)
    await setup(hass, entry)
    assert hass.states.get("binary_sensor.vordr_new_online") is not None
    assert hass.states.get("update.vordr_new_system") is None
    await fake_hub.push_host(host_view("new"))
    await wait_until(lambda: hass.states.get("update.vordr_new_system") is not None)
    # A report without any package source (e.g. a host with only apps) removes them again.
    await fake_hub.push_host(host_view("new", system=None, pending=0, security=0))
    await wait_until(lambda: hass.states.get("update.vordr_new_system") is None)
    assert hass.states.get("sensor.vordr_new_pending_updates") is None


async def test_command_refused_is_an_error(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = host_view("golf")
    await setup(hass, entry)
    await hass.services.async_call("button", "press", {"entity_id": "button.vordr_golf_check"}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "check"
    fake_hub.command_code = "rejected_rate_limited"
    with pytest.raises(HomeAssistantError):
        await hass.services.async_call("button", "press", {"entity_id": "button.vordr_golf_check"}, blocking=True)


async def test_unavailable_without_connection(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = host_view("golf")
    await setup(hass, entry)
    await fake_hub.shut_down()
    await wait_until(lambda: hass.states.get("sensor.vordr_golf_last_run").state == STATE_UNAVAILABLE)
    assert hass.states.get("update.vordr_golf_system").state == STATE_UNAVAILABLE
    assert hass.states.get("binary_sensor.vordr_hub_online").state == STATE_OFF
    assert hass.states.get("sensor.vordr_hub_connection").state in ("connecting", "unreachable")


async def test_sleeping_pc_is_away_not_a_problem(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """A PC that announced its sleep (hub 0.15.0): not connected, with power, and no problem."""
    fake_hub.hosts["host-a"] = host_view("host-a", online=False, power="sleeping")
    fake_hub.hosts["golf"] = host_view("golf")
    await setup(hass, entry)
    online = hass.states.get("binary_sensor.vordr_host_a_online")
    assert online.state == STATE_OFF and online.attributes["power"] == "sleeping"
    assert hass.states.get("binary_sensor.vordr_host_a_problem").state == STATE_OFF
    assert "power" not in hass.states.get("binary_sensor.vordr_golf_online").attributes


async def test_problem_events_and_repairs(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = host_view("golf")
    problems = async_capture_events(hass, EVENT_PROBLEM)
    runs = async_capture_events(hass, EVENT_RUN_FINISHED)
    await setup(hass, entry)
    assert problems == []  # no problem at start: nothing to tell
    await fake_hub.push_host(host_view("golf", problem={"reason": "stale", "reasons": ["stale"]}))
    await wait_until(lambda: len(problems) == 1)
    assert problems[0].data == {
        "node": "golf",
        "active": True,
        "reason": "stale",
        "reasons": ["stale"],
        "check_failed_code": None,
        # The repair text in every shipped language, with the hub's limit: an automation picks its language.
        "message": {
            "de": "Updates liegen seit über 7 Tagen.\n\n"
            "Nächster Schritt: Updates installieren, von Hand oder per Knopf.",
            "en": "Updates have been pending for more than 7 days.\n\n"
            "Next step: Install the updates, by hand or with the button.",
        },
    }
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "problem_golf")
    assert issue is not None and issue.translation_key == "problem_stale"
    assert issue.translation_placeholders["node"] == "golf" and issue.translation_placeholders["stale_days"] == "7"

    await fake_hub.push_host(
        host_view(
            "golf",
            last_run={"state": "queued", "job_id": "j1", "verb": "install", "started": "2026-10-29T00:00:00Z"},
        )
    )
    await wait_until(lambda: len(problems) == 2)
    assert not problems[1].data["active"] and problems[1].data["message"] == {}
    assert ir.async_get(hass).async_get_issue(DOMAIN, "problem_golf") is None
    await fake_hub.push_host(
        host_view(
            "golf",
            last_run={"state": "ok", "job_id": "j1", "verb": "install", "started": "2026-10-29T00:00:00Z"},
        )
    )
    await wait_until(lambda: len(runs) == 1)
    assert runs[0].data == {
        "node": "golf",
        "job_id": "j1",
        "verb": "install",
        "scope": "",
        "by": "",  # a hub before 0.12 does not say who started it
        "state": "ok",
        "message": {},  # plain ok: nothing to say
        "summary": {},
    }
    # A failed run: problem and run event name the code's text; the repair switches to it.
    await fake_hub.push_host(
        host_view(
            "golf",
            last_run={"state": "failed_package_manager", "job_id": "j2", "verb": "install", "started": "2026-10-29"},
            problem={"reason": "failed_package_manager", "reasons": ["failed_package_manager"]},
        )
    )
    await wait_until(lambda: len(runs) == 2 and len(problems) == 3)
    assert runs[1].data["message"]["de"] == (
        "Paketverwaltung gescheitert.\n\nNächster Schritt: Auszug im letzten Lauf lesen."
    )
    assert problems[2].data["message"] == runs[1].data["message"]
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "problem_golf")
    assert issue is not None and issue.translation_key == "problem_failed_package_manager"


def packages_view(malicious: int | None, vulnerable: int | None, findings: list[dict[str, Any]]) -> dict[str, Any]:
    """A PackagesView as the hub sends it for a Node-RED folder."""
    return {
        "checked_at": "2023-11-14T10:00:00Z",
        "total": 150,
        "locations": [
            {"id": "node-red", "kind": "npm-dir", "ecosystem": "npm", "path": "/srv/app/.node-red", "packages": 150}
        ],
        "vulnerable": vulnerable,
        "malicious": malicious,
        "findings": findings,
    }


CHALK = {
    "location": "node-red",
    "name": "chalk",
    "version": "5.6.1",
    "malicious": True,
    "advisories": ["MAL-2025-46969"],
}
DEBUG = {
    "location": "node-red",
    "name": "debug",
    "version": "4.3.0",
    "via": "node-red-contrib-x",
    "advisories": ["GHSA-gxpj-cx7g-858c"],
    "severity": "LOW",
    "fixed_in": "4.3.1",
}


async def test_package_findings(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """pip/npm findings: sensors per host, the hub's OSV data, and a problem that names the packages."""
    fake_hub.hosts["echo"] = host_view("echo")  # an agent that sends no inventory: no package sensors
    fake_hub.hub["osv"] = [
        {"ecosystem": "PyPI", "state": "ok", "checked": "2023-11-14T06:00:00Z", "advisories": 26000},
        {
            "ecosystem": "npm",
            "state": "ok",
            "checked": "2023-11-14T05:00:00Z",
            "advisories": 230000,
            "malicious": 220000,
        },
    ]
    problems = async_capture_events(hass, EVENT_PROBLEM)
    await setup(hass, entry)
    assert hass.states.get("sensor.vordr_echo_malicious_packages") is None
    data = hass.states.get("sensor.vordr_hub_vulnerability_data")
    assert data.state == "2023-11-14T05:00:00+00:00"  # the older of the two
    assert data.attributes["ecosystems"]["npm"] == {"state": "ok", "advisories": 230000, "malicious": 220000}
    assert "kev" not in data.attributes  # the hub has no kev section (ADR-0168)

    # Inventory sent, but the hub has no data for npm: unknown, not zero.
    await fake_hub.push_host(host_view("echo", packages=packages_view(None, None, [])))
    await wait_until(lambda: hass.states.get("sensor.vordr_echo_malicious_packages") is not None)
    assert hass.states.get("sensor.vordr_echo_malicious_packages").state == STATE_UNKNOWN
    vuln = hass.states.get("sensor.vordr_echo_vulnerable_packages")
    assert vuln.state == STATE_UNKNOWN and vuln.attributes["packages_total"] == 150
    assert vuln.attributes["locations"][0]["path"] == "/srv/app/.node-red"

    reasons = {"reason": "malicious_package", "reasons": ["malicious_package", "vulnerable_package"]}
    await fake_hub.push_host(host_view("echo", packages=packages_view(1, 1, [CHALK, DEBUG]), problem=reasons))
    await wait_until(lambda: hass.states.get("sensor.vordr_echo_malicious_packages").state == "1")
    mal = hass.states.get("sensor.vordr_echo_malicious_packages")
    assert mal.attributes["findings"] == [
        {"location": "node-red", "name": "chalk", "version": "5.6.1", "advisories": ["MAL-2025-46969"]}
    ]
    vuln = hass.states.get("sensor.vordr_echo_vulnerable_packages")
    assert vuln.state == "1" and vuln.attributes["findings"][0]["fixed_in"] == "4.3.1"
    prob = hass.states.get("binary_sensor.vordr_echo_problem")
    assert prob.state == STATE_ON and prob.attributes["next_step"] == "malicious_package"
    await wait_until(lambda: len(problems) == 1)
    message = problems[0].data["message"]["de"]
    assert message.startswith("Ein installiertes Paket ist laut OSV Schadsoftware: chalk 5.6.1 (node-red).\n\n")
    assert "Nächster Schritt: Paket entfernen" in message
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "problem_echo")
    assert issue.translation_key == "problem_malicious_package"
    assert issue.translation_placeholders["packages"] == "chalk 5.6.1 (node-red)"

    # Another malicious version turns up: same reasons, but a new problem worth telling.
    other = {**CHALK, "name": "lib-a", "version": "1.2.1", "advisories": ["MAL-0000-0001"]}
    await fake_hub.push_host(host_view("echo", packages=packages_view(2, 1, [CHALK, other, DEBUG]), problem=reasons))
    await wait_until(lambda: len(problems) == 2)
    assert "chalk 5.6.1 (node-red), lib-a 1.2.1 (node-red)" in problems[1].data["message"]["en"]


async def test_findings_say_where_their_fix_is(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0129: the sensor shows reach, held_by and how many wait; the repair names only findings with something to
    do."""
    held = {**DEBUG, "reach": "held", "held_by": [{"by": "node-red-contrib-x@1.0.0", "range": "4.3.0"}]}
    refresh = {**DEBUG, "name": "lib-b", "version": "1.0.7", "fixed_in": "1.0.8", "reach": "refresh"}
    del refresh["via"]
    view = {**packages_view(0, 2, [held, refresh]), "waiting": 1}
    problem = {"reason": "vulnerable_package", "reasons": ["vulnerable_package"]}
    fake_hub.hosts["echo"] = host_view("echo", packages=view, problem=problem)
    await setup(hass, entry)
    vuln = hass.states.get("sensor.vordr_echo_vulnerable_packages")
    assert vuln.state == "2" and vuln.attributes["waiting"] == 1
    assert vuln.attributes["findings"][0]["reach"] == "held"
    assert vuln.attributes["findings"][0]["held_by"] == [{"by": "node-red-contrib-x@1.0.0", "range": "4.3.0"}]
    assert vuln.attributes["findings"][1]["reach"] == "refresh"
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "problem_echo")
    assert issue.translation_placeholders["packages"] == "lib-b 1.0.7 (node-red)"


async def test_findings_known_exploited(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0168: the KEV marks of each finding and the host's count reach the sensors, the catalog's state the
    vulnerability data. A host view without them (the hub has no KEV catalog) shows none of it."""
    kev = {"known_exploited": True, "kev_cves": ["CVE-2017-16137"], "kev_added": "2026-11-09", "kev_ransomware": True}
    exploited = {**DEBUG, **kev}
    other = {**DEBUG, "name": "lib-b", "version": "1.0.7", "fixed_in": "1.0.8"}
    malware = {**CHALK, "known_exploited": True, "kev_cves": ["CVE-2025-59144"], "kev_added": "2025-09-10"}
    fake_hub.hosts["echo"] = host_view(
        "echo", packages={**packages_view(1, 2, [malware, exploited, other]), "known_exploited": 2}
    )
    fake_hub.hosts["foxtrot"] = host_view("foxtrot", packages=packages_view(1, 1, [CHALK, DEBUG]))
    fake_hub.hub["kev"] = {
        "state": "stale",
        "code": "download_failed",
        "checked": "2026-11-10T06:00:00Z",
        "released": "2026-11-09T18:00:00.123Z",
        "catalog_version": "2026.11.09",
        "entries": 1781,
    }
    await setup(hass, entry)

    vuln = hass.states.get("sensor.vordr_echo_vulnerable_packages")
    assert vuln.state == "2" and vuln.attributes["known_exploited"] == 2
    assert vuln.attributes["findings"][0] == exploited
    assert "known_exploited" not in vuln.attributes["findings"][1]
    mal = hass.states.get("sensor.vordr_echo_malicious_packages")
    assert mal.attributes["findings"][0]["kev_cves"] == ["CVE-2025-59144"]
    assert "kev_ransomware" not in mal.attributes["findings"][0]  # false is left out like every empty field
    assert "known_exploited" not in mal.attributes  # the count is on the vulnerable packages, once per host
    data = hass.states.get("sensor.vordr_hub_vulnerability_data")
    assert data.attributes["kev"] == fake_hub.hub["kev"]

    plain = hass.states.get("sensor.vordr_foxtrot_vulnerable_packages")
    assert "known_exploited" not in plain.attributes
    assert not any(k.startswith(("known_", "kev_")) for k in plain.attributes["findings"][0])


async def test_outdated_packages(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """Newer releases per package; unknown while the agent does not ask the registries."""
    fake_hub.hosts["echo"] = host_view("echo", packages={**packages_view(0, 0, []), "outdated": None})
    await setup(hass, entry)
    assert hass.states.get("sensor.vordr_echo_outdated_packages").state == STATE_UNKNOWN
    updates = [
        {
            "location": "node-red",
            "name": "node-red-contrib-x",
            "installed": "1.0.3",
            "latest": "1.1.0",
            "candidate": "1.1.0",
        },
        {
            "location": "npm-global",
            "name": "example-app",
            "installed": "1.4.0",
            "latest": "2.0.1",
            "major": True,
            "latest_at": "2023-11-12T10:00:00Z",
        },
    ]
    view = {
        **packages_view(0, 0, []),
        "outdated": 2,
        "outdated_at": "2023-11-14T06:00:00Z",
        "min_age_days": 7,
        "updates": updates,
    }
    await fake_hub.push_host(host_view("echo", packages=view))
    await wait_until(lambda: hass.states.get("sensor.vordr_echo_outdated_packages").state == "2")
    attrs = hass.states.get("sensor.vordr_echo_outdated_packages").attributes
    assert attrs["min_age_days"] == 7 and attrs["updates"][1] == updates[1]
    assert hass.states.get("binary_sensor.vordr_echo_problem").state == STATE_OFF  # outdated is no problem


async def test_package_lists_stay_out_of_the_recorder(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """Full lists (MaxFindings, MaxUpdates) are bigger than the recorder's limit: they stay in the state, the recorder
    stores the other attributes without them (ADR-0101)."""
    findings = [
        {
            **DEBUG,
            "name": f"package-{i:02}",
            "advisories": [f"GHSA-{i:04}-{n:04}-abcd" for n in range(6)],
            "aliases": [f"CVE-2026-{1000 * n + i}" for n in range(1, 7)],
            "severity": "HIGH",
        }
        for i in range(50)
    ]
    updates = [
        {
            "location": "npm-global",
            "name": f"node-red-contrib-package-{i:03}",
            "installed": "1.0.0",
            "latest": "2.0.0",
            "candidate": "1.9.0",
            "latest_at": "2023-11-12T10:00:00Z",
            "major": True,
        }
        for i in range(100)
    ]
    view = {
        **packages_view(0, 50, findings),
        "outdated": 100,
        "outdated_at": "2023-11-14T06:00:00Z",
        "min_age_days": 7,
        "updates": updates,
    }
    fake_hub.hosts["echo"] = host_view("echo", packages=view)
    await setup(hass, entry)
    for entity_id, lists in (
        ("sensor.vordr_echo_vulnerable_packages", {"findings", "locations"}),
        ("sensor.vordr_echo_outdated_packages", {"updates"}),
    ):
        state = hass.states.get(entity_id)
        assert len(json_bytes(dict(state.attributes))) > MAX_STATE_ATTRS_BYTES  # the full lists are in the state
        event = Event(EVENT_STATE_CHANGED, {"entity_id": entity_id, "old_state": None, "new_state": state})
        recorded = json_loads(StateAttributes.shared_attrs_bytes_from_event(event, None))
        assert recorded and not lists & recorded.keys(), recorded  # stored, without the lists
    malicious = hass.states.get("sensor.vordr_echo_malicious_packages")
    assert "findings" in malicious.state_info["unrecorded_attributes"]


async def test_package_update_entity_and_service(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """One update entity per location the host lets HA update; install = the whole location, the service one package."""
    updates = [
        {"location": "npm-global", "name": "example-app", "installed": "1.4.0", "latest": "2.0.1", "major": True},
        {
            "location": "npm-global",
            "name": "@example/nodes",
            "installed": "1.4.0",
            "latest": "2.0.1",
            "candidate": "2.0.1",
        },
        {
            "location": "npm-global",
            "name": "lib-a",
            "installed": "1.2.0",
            "latest": "1.2.1",
            "candidate": "1.2.1",
            "candidate_flagged": ["MAL-0000-0001"],
        },
        {"location": "node-red", "name": "x", "installed": "1.0.0", "latest": "1.1.0", "candidate": "1.1.0"},
    ]
    view = {
        **packages_view(0, 0, []),
        "locations": [
            {
                "id": "npm-global",
                "kind": "npm-global",
                "ecosystem": "npm",
                "path": "/usr/lib/node_modules",
                "packages": 500,
                "outdated": 3,
            },
            {
                "id": "node-red",
                "kind": "npm-dir",
                "ecosystem": "npm",
                "path": "/srv/app/.node-red",
                "packages": 150,
                "outdated": 1,
            },
        ],
        "outdated": 4,
        "updates": updates,
    }
    caps = {**host_view("echo")["capabilities"], "package_updates": ["npm-global"]}
    fake_hub.hosts["echo"] = host_view("echo", packages=view, capabilities=caps)
    await setup(hass, entry)
    assert hass.states.get("update.vordr_echo_packages_node_red") is None  # not updatable: no entity
    upd = hass.states.get("update.vordr_echo_packages_npm_global")
    assert upd.state == STATE_ON and upd.attributes["friendly_name"] == "Vördr echo Packages npm-global"
    assert upd.attributes["installed_version"] == "500 packages"
    assert upd.attributes["latest_version"] == "3 outdated, 1 ready"
    assert upd.attributes["release_summary"] == (
        "@example/nodes 1.4.0 → 2.0.1, example-app 1.4.0 → 2.0.1 (new major version, too new), "
        "lib-a 1.2.0 → 1.2.1 (flagged by OSV)"
    )
    await hass.services.async_call("update", "install", {"entity_id": upd.entity_id}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "install_pkg:npm-global" and fake_hub.commands[-1]["node"] == "echo"
    await hass.services.async_call(
        DOMAIN, "update_package", {"entity_id": upd.entity_id, "package": "@example/nodes"}, blocking=True
    )
    assert fake_hub.commands[-1]["command"] == "install_pkg:npm-global/@example/nodes"
    fake_hub.command_code = "not_allowed"
    with pytest.raises(HomeAssistantError):
        await hass.services.async_call(
            DOMAIN, "update_package", {"entity_id": upd.entity_id, "package": "lib-a"}, blocking=True
        )


async def test_refresh_dependencies(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """vordr.refresh_dependencies (ADR-0104): only for a location the hub offers it for; nothing goes out otherwise.
    With a reason it may run outside the window."""
    location = {"id": "node-red", "kind": "npm-dir", "ecosystem": "npm", "path": "/n", "packages": 150, "outdated": 0}
    view = {**packages_view(0, 0, []), "locations": [location], "outdated": 0, "updates": []}
    caps = {**host_view("echo")["capabilities"], "package_updates": ["node-red"], "outside_window": True}
    fake_hub.hosts["echo"] = host_view("echo", packages=view, capabilities=caps)
    await setup(hass, entry)
    eid = "update.vordr_echo_packages_node_red"
    sent_before = len(fake_hub.commands)
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call(DOMAIN, "refresh_dependencies", {"entity_id": eid}, blocking=True)
    assert err.value.translation_key == "refresh_dependencies_unsupported" and len(fake_hub.commands) == sent_before

    await fake_hub.push_host(
        host_view("echo", packages=view, capabilities={**caps, "dependency_refresh": ["node-red"]})
    )
    await wait_until(
        lambda: entry.runtime_data.data.hosts["echo"]["capabilities"].get("dependency_refresh") == ["node-red"]
    )
    await hass.services.async_call(DOMAIN, "refresh_dependencies", {"entity_id": eid}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "refresh_deps:node-red" and "reason" not in fake_hub.commands[-1]
    await hass.services.async_call(
        DOMAIN, "refresh_dependencies", {"entity_id": eid, "reason": "tonight"}, blocking=True
    )
    sent = fake_hub.commands[-1]
    assert (sent["command"], sent["reason"]) == ("refresh_deps:node-red", "tonight")


async def test_outside_window_actions(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """Install, press or update a package outside the host's window, with a reason (ADR-0087): only where the hub
    offers it, never for what has no window, never with a reason the hub would refuse."""
    caps = {
        **host_view("h")["capabilities"],
        "install": True,
        "install_security": True,
        "reboot": True,
        "agent_update": True,
        "default_scope": "all",
        "package_updates": ["npm-global"],
        "outside_window": True,
    }
    location = {
        "id": "npm-global",
        "kind": "npm-global",
        "ecosystem": "npm",
        "path": "/n",
        "packages": 1,
        "outdated": 0,
    }
    pkgs = {**packages_view(0, 0, []), "locations": [location], "outdated": 0, "updates": []}
    fake_hub.hosts["h"] = host_view("h", capabilities=caps, packages=pkgs, agent_release="1.1")
    await setup(hass, entry)

    async def call(service: str, data: dict[str, Any]) -> dict[str, Any]:
        await hass.services.async_call(DOMAIN, service, data, blocking=True)
        return fake_hub.commands[-1]

    system, npm = "update.vordr_h_system", "update.vordr_h_packages_npm_global"
    sent = await call("install_outside_window", {"entity_id": system, "reason": "  away tomorrow "})
    assert (sent["command"], sent["reason"]) == ("install", "away tomorrow")
    sent = await call("install_outside_window", {"entity_id": npm, "reason": "now"})
    assert (sent["command"], sent["reason"]) == ("install_pkg:npm-global", "now")
    sent = await call("update_package", {"entity_id": npm, "package": "lib-a", "reason": "now"})
    assert (sent["command"], sent["reason"]) == ("install_pkg:npm-global/lib-a", "now")
    sent = await call("press_outside_window", {"entity_id": "button.vordr_h_reboot", "reason": "kernel update"})
    assert (sent["command"], sent["reason"]) == ("reboot", "kernel update")
    await hass.services.async_call("button", "press", {"entity_id": "button.vordr_h_install_security"}, blocking=True)
    assert "reason" not in fake_hub.commands[-1]  # outside the window the hub gives the button its reason (ADR-0109)

    sent_before = len(fake_hub.commands)
    for service, eid in (
        ("press_outside_window", "button.vordr_h_check"),
        ("install_outside_window", "update.vordr_h_agent"),
    ):
        with pytest.raises(HomeAssistantError) as err:
            await call(service, {"entity_id": eid, "reason": "now"})
        assert err.value.translation_key == "outside_window_no_window"
    for bad in ("", "   ", "one\ntwo", "x" * 201, "ä" * 101):
        with pytest.raises(vol.Invalid):
            await call("install_outside_window", {"entity_id": system, "reason": bad})
    assert len(fake_hub.commands) == sent_before

    # An older hub or agent: not offered, nothing goes out; a hub that refuses for an older agent says the same.
    await fake_hub.push_host(host_view("h", capabilities={**caps, "outside_window": False}, packages=pkgs))
    await wait_until(lambda: not entry.runtime_data.data.hosts["h"]["capabilities"]["outside_window"])
    with pytest.raises(HomeAssistantError) as err:
        await call("install_outside_window", {"entity_id": system, "reason": "now"})
    assert err.value.translation_key == "outside_window_unsupported" and len(fake_hub.commands) == sent_before
    await fake_hub.push_host(host_view("h", capabilities=caps, packages=pkgs))
    await wait_until(lambda: entry.runtime_data.data.hosts["h"]["capabilities"]["outside_window"])
    fake_hub.command_code = "outside_window_unsupported"
    with pytest.raises(HomeAssistantError) as err:
        await call("install_outside_window", {"entity_id": system, "reason": "now"})
    assert err.value.translation_key == "outside_window_unsupported"
    # The hub refuses an install the closed window would stop, for an agent that takes no reason (ADR-0109).
    fake_hub.command_code = "outside_window"
    press = {"entity_id": "button.vordr_h_install_security"}
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call("button", "press", press, blocking=True)
    assert err.value.translation_key == "command_refused_outside_window"
    assert err.value.translation_placeholders == {"node": "h"}
    # The hub refuses an install the agent's minimum interval would stop, and the view says when (ADR-0110).
    nxt = dt_util.utcnow().replace(microsecond=0) + timedelta(minutes=5)
    await fake_hub.push_host(host_view("h", capabilities=caps, packages=pkgs, next_install=nxt.isoformat()))
    await wait_until(lambda: entry.runtime_data.data.hosts["h"].get("next_install"))
    fake_hub.command_code = "rate_limited"
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call("button", "press", press, blocking=True)
    assert err.value.translation_key == "command_refused_rate_limited"
    assert err.value.translation_placeholders == {"node": "h", "next": local_time(nxt)}


REBOOT_CAPS = {**host_view("h")["capabilities"], "reboot": True, "install_security": True, "outside_window": True}
WORKDAYS = {"days": ["mon", "tue", "wed", "thu", "fri"], "start": "06:00", "end": "16:00", "tz": "Europe/Paris"}


async def test_reboot_button_waits_for_the_reboot_window(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0187: while the host's reboot window is closed, the reboot button sends nothing and says from when the reboot
    runs; with reboot.window_only a reason does not help either, without it the action with a reason still goes out.
    The button shows the window and the time. Once the time has come, and from a hub that does not say, it sends."""
    nxt = dt_util.utcnow().replace(microsecond=0) + timedelta(hours=3)
    only = {**WORKDAYS, "window_only": True}
    view = {"capabilities": REBOOT_CAPS, "reboot_window": only, "next_reboot": nxt.isoformat()}
    fake_hub.hosts["h"] = host_view("h", **view)
    await setup(hass, entry)
    attrs = hass.states.get("button.vordr_h_reboot").attributes
    assert attrs["next_reboot"] == nxt.isoformat() and attrs["window_only"] is True
    assert attrs["reboot_window"] == {"days": WORKDAYS["days"], "start": "06:00", "end": "16:00", "tz": "Europe/Paris"}
    assert "next_reboot" not in hass.states.get("button.vordr_h_check").attributes

    press = {"entity_id": "button.vordr_h_reboot"}
    shown = {"node": "h", "next": local_time(nxt)}
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call("button", "press", press, blocking=True)
    assert err.value.translation_key == "reboot_window_only" and err.value.translation_placeholders == shown
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call(DOMAIN, "press_outside_window", {**press, "reason": "now"}, blocking=True)
    assert err.value.translation_key == "reboot_window_only" and err.value.translation_placeholders == shown
    assert fake_hub.commands == []
    # The other buttons have no reboot window.
    await hass.services.async_call("button", "press", {"entity_id": "button.vordr_h_install_security"}, blocking=True)
    assert [c["command"] for c in fake_hub.commands] == ["install_security"]

    # Without window_only the button still waits - it carries no reason (ADR-0109) - but a reason opens the window.
    await fake_hub.push_host(host_view("h", **{**view, "reboot_window": {**WORKDAYS, "window_only": False}}))
    await wait_until(lambda: hass.states.get("button.vordr_h_reboot").attributes["window_only"] is False)
    with pytest.raises(HomeAssistantError) as err:
        await hass.services.async_call("button", "press", press, blocking=True)
    assert err.value.translation_key == "reboot_window_closed" and err.value.translation_placeholders == shown
    assert len(fake_hub.commands) == 1
    await hass.services.async_call(DOMAIN, "press_outside_window", {**press, "reason": "now"}, blocking=True)
    assert (fake_hub.commands[-1]["command"], fake_hub.commands[-1]["reason"]) == ("reboot", "now")

    # The window has opened since the hub's last view: the time lies behind, the button sends.
    past = dt_util.utcnow().replace(microsecond=0) - timedelta(seconds=5)
    await fake_hub.push_host(host_view("h", **{**view, "next_reboot": past.isoformat()}))
    await wait_until(lambda: hass.states.get("button.vordr_h_reboot").attributes["next_reboot"] == past.isoformat())
    await hass.services.async_call("button", "press", press, blocking=True)
    assert fake_hub.commands[-1]["command"] == "reboot" and "reason" not in fake_hub.commands[-1]

    # Inside the window the hub sends no time; a hub or agent before ADR-0187 sends neither: as before.
    for over in ({"reboot_window": only}, {}):
        await fake_hub.push_host(host_view("h", capabilities=REBOOT_CAPS, **over))
        await wait_until(lambda: "next_reboot" not in (hass.states.get("button.vordr_h_reboot").attributes))
        sent = len(fake_hub.commands)
        await hass.services.async_call("button", "press", press, blocking=True)
        assert len(fake_hub.commands) == sent + 1 and fake_hub.commands[-1]["command"] == "reboot"
    await wait_until(lambda: "reboot_window" not in hass.states.get("button.vordr_h_reboot").attributes)


def test_local_time_names_the_day_only_when_it_is_not_today() -> None:
    now = dt_util.now()
    assert local_time(None) == "?"
    assert local_time(now) == now.strftime("%H:%M")
    other = now + timedelta(days=2)
    assert local_time(other) == other.strftime("%d.%m. %H:%M")


async def test_forgotten_host_disappears(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["gone"] = host_view("gone")
    fake_hub.hosts["stays"] = host_view("stays")
    await setup(hass, entry)
    await fake_hub.remove_host("gone")
    devices = dr.async_get(hass)
    await wait_until(lambda: devices.async_get_device_by_identifier((DOMAIN, "host:gone"), entry.entry_id) is None)
    await hass.async_block_till_done()
    assert hass.states.get("binary_sensor.vordr_gone_online") is None
    assert er.async_get(hass).async_get("binary_sensor.vordr_gone_online") is None
    assert hass.states.get("binary_sensor.vordr_stays_online") is not None


def every_entity_view(node: str, may_install: bool) -> dict[str, Any]:
    """A host with every entity the integration makes: backup, pip/npm location, container images, an app, an agent
    release. may_install False takes the install capabilities away, so that the update platform would pick
    diagnostic for the update entities instead of config."""
    caps = {
        **host_view(node)["capabilities"],
        "install": may_install,
        "install_security": True,
        "reboot": True,
        "agent_update": may_install,
        "default_scope": "all",
        "package_updates": ["npm-global"] if may_install else [],
        "apps": [{"id": "gitlab", "name": "GitLab"}],
    }
    location = {"id": "npm-global", "kind": "npm-global", "ecosystem": "npm", "path": "/n", "packages": 1}
    return host_view(
        node,
        capabilities=caps,
        agent_release="1.1",
        backup={"state": "ok", "last": "2026-10-28T23:00:00Z", "group": "ct/302", "max_age_hours": 26},
        packages={**packages_view(0, 0, []), "locations": [location], "outdated": 0, "updates": []},
        containers={"checked_at": "2026-10-29T00:00:00Z", "images": 1, "outdated": 0, "ready": 0},
    )


DIAGNOSTIC, CONFIG = EntityCategory.DIAGNOSTIC, EntityCategory.CONFIG
HOST_CATEGORIES = {
    # what a host's maintenance is about: no category
    "update.{n}_system": None,
    "update.{n}_containers": None,
    "binary_sensor.{n}_problem": None,
    "binary_sensor.{n}_reboot_required": None,
    "sensor.{n}_last_run": None,
    "sensor.{n}_pending_updates": None,
    "sensor.{n}_security_updates": None,
    "sensor.{n}_vulnerable_packages": None,
    "sensor.{n}_malicious_packages": None,
    "sensor.{n}_outdated_packages": None,
    "sensor.{n}_outdated_images": None,
    "button.{n}_install_security": None,
    "button.{n}_reboot": None,
    "button.{n}_install_app_gitlab": None,
    # how Vördr sees the host
    "binary_sensor.{n}_online": DIAGNOSTIC,
    "sensor.{n}_last_report": DIAGNOSTIC,
    "sensor.{n}_last_backup": DIAGNOSTIC,
    # how Vördr itself works
    "button.{n}_check": CONFIG,
    "update.{n}_agent": CONFIG,
}
OTHER_CATEGORIES = {
    "sensor.vordr_hub_hosts_online": None,
    "sensor.vordr_hub_hosts_with_problem": None,
    "sensor.vordr_hub_hosts_with_updates": None,
    "sensor.vordr_hub_hosts_with_security_updates": None,
    "sensor.vordr_hub_hosts_needing_reboot": None,
    "sensor.vordr_hub_pending_approvals": None,
    "sensor.vordr_ha_updates_ready": None,
    "sensor.vordr_ha_updates_last_run": None,
    "binary_sensor.vordr_hub_online": DIAGNOSTIC,
    "sensor.vordr_hub_connection": DIAGNOSTIC,
    "sensor.vordr_hub_vulnerability_data": DIAGNOSTIC,
    "switch.vordr_ha_updates_paused": CONFIG,
}


async def test_entity_categories_by_role(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0202: primary what a host's maintenance is about, diagnostic how Vördr sees a host or the hub, config how
    Vördr itself works. The update entities keep their category whether the host may install or not."""
    fake_hub.hosts["full"] = every_entity_view("full", may_install=True)
    fake_hub.hosts["bare"] = every_entity_view("bare", may_install=False)
    await setup(hass, entry)
    reg = er.async_get(hass)
    want = {eid.format(n="vordr_full"): cat for eid, cat in HOST_CATEGORIES.items()}
    want |= {eid.format(n="vordr_bare"): cat for eid, cat in HOST_CATEGORIES.items()}
    want |= OTHER_CATEGORIES
    want["update.vordr_full_packages_npm_global"] = None
    for eid, cat in want.items():
        e = reg.async_get(eid)
        assert e is not None, eid
        assert e.entity_category == cat, eid
    # every entity of the integration is named above: a new one gets its category decided, not by chance
    made = {e.entity_id for e in er.async_entries_for_config_entry(reg, entry.entry_id)}
    assert made == set(want), made ^ set(want)
    assert hass.states.get("update.vordr_full_system").attributes["supported_features"] & 1  # may install
    assert not hass.states.get("update.vordr_bare_system").attributes["supported_features"] & 1


async def test_existing_entities_take_their_category(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """An installation from before ADR-0202 has its entities registered without a category - or, for the update
    entities, with the one the update platform picked. They take the new one on load, under the same entity id."""
    entry.add_to_hass(hass)
    reg = er.async_get(hass)
    old = {
        "sensor.vordr_golf_last_report": ("sensor", "golf:last_report", None),
        "button.vordr_golf_check": ("button", "golf:check", None),
        "update.vordr_golf_system": ("update", "golf:system", CONFIG),
    }
    for eid, (domain, uid, cat) in old.items():
        e = reg.async_get_or_create(
            domain, DOMAIN, uid, suggested_object_id=eid.split(".")[1], config_entry=entry, entity_category=cat
        )
        assert e.entity_id == eid and e.entity_category == cat
    fake_hub.hosts["golf"] = host_view("golf", capabilities={**host_view("golf")["capabilities"], "install": True})
    await setup(hass, entry)
    for eid, cat in {
        "sensor.vordr_golf_last_report": DIAGNOSTIC,
        "button.vordr_golf_check": CONFIG,
        "update.vordr_golf_system": None,
    }.items():
        e = reg.async_get(eid)
        assert e is not None and e.entity_category == cat, (eid, e and e.entity_category)
    assert len(er.async_entries_for_config_entry(reg, entry.entry_id)) == len(HOST_IDS) + len(OTHER_CATEGORIES)


async def test_diagnostics_hide_the_key(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    await setup(hass, entry)
    diag: dict[str, Any] = await async_get_config_entry_diagnostics(hass, entry)
    assert diag["entry"]["private_key"] == "**REDACTED**"
    assert diag["connection"] == "approved" and len(diag["fingerprint"]) == 64


async def test_diagnostics_name_no_site(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0189: the download names no host, address, path, image repository, location id, log or free text, and
    keeps what a diagnosis needs: the structure, counts, codes, versions, package names. Host names and location ids
    become stable placeholders, also inside texts and in any case, but not inside longer names ("vordr-agent" keeps its
    "agent"), and field names stay even where a host is named like one."""
    run = {
        "job_id": "j1",
        "verb": "install",
        "state": "failed",
        "by": "ha:homeassistant",
        "reason": "bad_parameters",  # a code
        "outside_window": "before the backup to db.example.org",  # free text
        "log_excerpt": "connect to 192.0.2.10 (WEB-01) failed",
        "summary": {"log_file": "/var/log/vordr/web-01.log", "package_count": 3},
    }
    location = "pip-user-someone"
    fake_hub.hosts["web-01"] = host_view(
        "web-01",
        last_run=run,
        problem={"reason": "stale", "reasons": ["stale"]},
        packages={
            "outdated": 0,
            "locations": [{"id": location, "ecosystem": "PyPI", "path": "/home/someone/app/.venv", "packages": 3}],
            "findings": [{"location": location, "name": "requests", "version": "2.0.0", "severity": "high"}],
        },
        containers={"outdated": 1, "updates": [{"image": "registry.example.org/team/app", "tag": "1.2"}]},
        backup={"state": "exempt", "exempt": "copied to nas.example.org by hand"},
    )
    fake_hub.hosts["agent"] = host_view(
        "agent", device={"name": "Vördr agent", "model": "vordr-agent test box", "agent_version": "1.0"}
    )
    fake_hub.hosts["system"] = host_view("system", backup={"state": "ok", "before_install": "pve-x"})
    fake_hub.hub["waiting"] = [{"node": "new-box", "fingerprint": "ab" * 32, "os": "linux"}]
    await setup(hass, entry)
    diag: dict[str, Any] = await async_get_config_entry_diagnostics(hass, entry)
    dumped = json.dumps(diag)
    for site in (
        "web-01",
        "WEB-01",
        "new-box",
        "someone",
        "registry.example.org",
        "192.0.2.10",
        "example.org",
        "pve-x",
    ):
        assert site not in dumped, site
    assert "127.0.0.1" not in dumped and "homeassistant" not in dumped

    # agent, new-box, system, web-01: in the order of the names
    assert sorted(diag["hosts"]) == ["host-1", "host-3", "host-4"] and diag["hub"]["waiting"][0]["node"] == "host-2"
    web = diag["hosts"]["host-4"]
    assert web["node"] == "host-4" and web["device"]["name"] == "Vördr host-4" and web["pending"] == 2
    assert web["last_run"]["by"] == "ha:client" and web["last_run"]["state"] == "failed"
    assert web["last_run"]["reason"] == "bad_parameters"
    assert web["last_run"]["outside_window"] == web["last_run"]["log_excerpt"] == "**REDACTED**"
    assert web["last_run"]["summary"] == {"log_file": "**REDACTED**", "package_count": 3}
    assert web["packages"]["locations"][0] == {
        "id": "location-1",
        "ecosystem": "PyPI",
        "path": "**REDACTED**",
        "packages": 3,
    }
    assert web["packages"]["findings"][0] == {
        "location": "location-1",
        "name": "requests",
        "version": "2.0.0",
        "severity": "high",
    }
    assert web["containers"]["updates"][0] == {"image": "**REDACTED**", "tag": "1.2"}
    assert web["backup"] == {"state": "exempt", "exempt": "**REDACTED**"}
    agent = diag["hosts"]["host-1"]
    assert agent["device"] == {"name": "Vördr host-1", "model": "vordr-agent test box", "agent_version": "1.0"}
    named_like_a_field = diag["hosts"]["host-3"]
    assert named_like_a_field["node"] == "host-3" and "system" in named_like_a_field and "pending" in named_like_a_field
    assert named_like_a_field["backup"] == {"state": "ok", "before_install": "**REDACTED**"}  # no host of the hub
    assert diag["hub"]["waiting"][0]["fingerprint"] == "ab" * 32
    assert diag["entry"]["url"] == f"wss://hub.invalid:{fake_hub.port}/v1/ha" and diag["entry"]["name"] == "client"
    assert diag["entry"]["private_key"] == diag["entry"]["ca_pem"] == "**REDACTED**"


def test_the_hub_address_keeps_no_credentials() -> None:
    assert hub_address("wss://someone:pw@hub.example.org:8443/v1/ha") == "wss://hub.invalid:8443/v1/ha"
    assert hub_address("") == hub_address(None) == "**REDACTED**"


async def test_system_update_only_when_installable(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0106: the system entity is an update only when the install button would install something. The counts
    stay; a hub that does not send installable keeps the old behaviour."""
    caps = {**host_view("golf")["capabilities"], "install": True, "default_scope": "security"}
    fake_hub.hosts["golf"] = host_view("golf", pending=41, security=0, installable=0, capabilities=caps)
    fake_hub.hosts["hotel"] = host_view("hotel", pending=42, security=1, installable=1, capabilities=caps)
    fake_hub.hosts["india"] = host_view("india")  # a hub before ADR-0106: no installable
    await setup(hass, entry)
    golf = hass.states.get("update.vordr_golf_system")
    assert golf.state == STATE_OFF and golf.attributes["latest_version"] == "TestOS 1"
    assert hass.states.get("sensor.vordr_golf_pending_updates").state == "41"
    hotel = hass.states.get("update.vordr_hotel_system")
    assert hotel.state == STATE_ON and hotel.attributes["latest_version"] == "42 pending (1 security)"
    assert hass.states.get("update.vordr_india_system").state == STATE_ON


async def test_packages_update_only_when_ready(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0106: a pip/npm location whose newer releases are all too new or flagged is no update yet; the summary still
    names them."""

    def location(updates: list[dict[str, Any]]) -> dict[str, Any]:
        loc = {"id": "npm-global", "kind": "npm-global", "ecosystem": "npm", "path": "/usr/lib/node_modules"}
        return {
            **packages_view(0, 0, []),
            "locations": [{**loc, "packages": 3, "outdated": len(updates)}],
            "outdated": len(updates),
            "updates": updates,
        }

    caps = {**host_view("echo")["capabilities"], "package_updates": ["npm-global"]}
    too_new = {"location": "npm-global", "name": "example-cli", "installed": "2.1.0", "latest": "2.2.1"}
    flagged = {**too_new, "name": "lib-a", "candidate": "1.2.1", "candidate_flagged": ["MAL-0000-0001"]}
    fake_hub.hosts["echo"] = host_view("echo", packages=location([too_new]), capabilities=caps)
    fake_hub.hosts["foxtrot"] = host_view("foxtrot", packages=location([flagged]), capabilities=caps)
    await setup(hass, entry)
    echo = hass.states.get("update.vordr_echo_packages_npm_global")
    assert echo.state == STATE_OFF and echo.attributes["latest_version"] == "3 packages"
    assert echo.attributes["release_summary"] == "example-cli 2.1.0 → 2.2.1 (too new)"
    assert hass.states.get("update.vordr_foxtrot_packages_npm_global").state == STATE_OFF


async def test_security_fix_counts_as_ready(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0111: a release younger than the minimum age that fixes an advisory (the hub's `fix`) is an update now, and
    the summary names it as a fix - instead of "too new"."""
    caps = {**host_view("golf")["capabilities"], "package_updates": ["npm-global"]}
    loc = {"id": "npm-global", "kind": "npm-global", "ecosystem": "npm", "path": "/usr/lib/node_modules"}
    fix = {"location": "npm-global", "name": "lib-d", "installed": "2.8.0", "latest": "2.9.1", "fix": "2.9.1"}
    pkgs = {**packages_view(0, 0, []), "locations": [{**loc, "packages": 3, "outdated": 1}], "outdated": 1}
    pkgs["updates"] = [fix]
    fake_hub.hosts["golf"] = host_view("golf", packages=pkgs, capabilities=caps)
    await setup(hass, entry)
    state = hass.states.get("update.vordr_golf_packages_npm_global")
    assert state.state == STATE_ON and state.attributes["latest_version"] == "1 outdated, 1 ready"
    assert state.attributes["release_summary"] == "lib-d 2.8.0 → 2.9.1 (security fix, shorter minimum age)"
