"""The update entity "Container images" (ADR-0113): reported, and rebuilds installed by button where the host allows
Compose services (ADR-0119); on only while the button would take something (ADR-0150), the sensor "Outdated images"
counts every outdated image."""

from __future__ import annotations

from typing import Any

from homeassistant.components.update import UpdateEntityFeature
from homeassistant.const import STATE_OFF, STATE_ON, STATE_UNKNOWN
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers import entity_registry as er
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr.const import DOMAIN

from .conftest import FakeHub, host_view, wait_until
from .test_entities import setup

DB = {
    "image": "docker.io/stack/db",
    "tag": "3",
    "containers": ["db"],
    "rebuilt": True,
    "rebuilt_at": "2023-11-10T00:00:00Z",
    "rebuilt_ready": True,
    "latest": "5",
    "candidate": "4",
    "major": True,
}
APP = {"image": "ghcr.io/org/app", "tag": "1.4.2", "latest": "1.4.3", "latest_at": "2023-11-13T00:00:00Z"}


def containers_view(updates: list[dict[str, Any]], ready: int) -> dict[str, Any]:
    return {
        "checked_at": "2023-11-14T06:00:00Z",
        "images": 4,
        "local": 1,
        "outdated": len(updates),
        "ready": ready,
        "compared_at": "2023-11-14T06:00:00Z",
        "min_age_days": 7,
        "not_in_registry": 1,
        "updates": updates,
    }


async def test_container_images_entity(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """Without allowed services the entity only reports: no install, the summary says so, and it stays off also with a
    candidate (ADR-0150) - the sensor "Outdated images" counts the images instead."""
    eid, sensor = "update.vordr_echo_containers", "sensor.vordr_echo_outdated_images"
    fake_hub.hosts["echo"] = host_view("echo", containers=containers_view([APP], 0))
    fake_hub.hosts["golf"] = host_view("golf")  # reports no images: no entity, no sensor
    await setup(hass, entry)
    state = hass.states.get(eid)
    assert state is not None and state.state == STATE_OFF  # only a tag that is too new
    assert state.attributes["installed_version"] == "4 images" and state.attributes["latest_version"] == "4 images"
    assert (
        state.attributes["release_summary"]
        == "Only reported, Vördr does not update images: ghcr.io/org/app 1.4.2 → 1.4.3 (too new)"
    )
    assert state.attributes["supported_features"] == UpdateEntityFeature(0)
    assert hass.states.get(sensor).state == "1" and hass.states.get(sensor).attributes["ready"] == 0
    reg = er.async_get(hass)
    assert reg.async_get_entity_id("update", "vordr", "golf:containers") is None
    assert reg.async_get_entity_id("sensor", "vordr", "golf:outdated_images") is None

    await fake_hub.push_host(host_view("echo", containers=containers_view([DB, APP], 1)))
    await wait_until(lambda: hass.states.get(sensor).state == "2")
    state = hass.states.get(eid)
    assert state.state == STATE_OFF and state.attributes["latest_version"] == "4 images"  # nothing for a button
    assert state.attributes["release_summary"] == (
        "Only reported, Vördr does not update images: stack/db 3 (rebuilt), stack/db 3 → 4 (new major version), "
        "ghcr.io/org/app 1.4.2 → 1.4.3 (too new)"
    )
    attrs = state.attributes
    assert attrs["updates"][0]["containers"] == ["db"] and attrs["not_in_registry"] == 1 and attrs["local"] == 1
    assert "updates" in state.state_info["unrecorded_attributes"]
    attrs = hass.states.get(sensor).attributes
    assert attrs["ready"] == 1 and "installable" not in attrs  # a hub before installable does not send it
    assert attrs["updates"][0]["candidate"] == "4" and attrs["updates"][1]["image"] == "ghcr.io/org/app"
    assert "updates" in hass.states.get(sensor).state_info["unrecorded_attributes"]


async def test_container_images_without_registry_questions(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """packages.outdated off: the images are listed, nothing is compared - the entity stays off, never "unknown"; the
    sensor "Outdated images" is unknown, not zero."""
    view = {**containers_view([], 0), "outdated": None, "ready": None, "updates": None}
    fake_hub.hosts["echo"] = host_view("echo", containers=view)
    await setup(hass, entry)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF and state.attributes["release_summary"] is None
    sensor = hass.states.get("sensor.vordr_echo_outdated_images")
    assert sensor.state == STATE_UNKNOWN and sensor.attributes["updates"] == [] and "ready" not in sensor.attributes


async def test_container_images_same_major(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0131: beside a new major version the summary names the smaller step within the running one, also when the
    new major version is too new; the attribute carries it. Without it the line stays as before."""
    web = {
        "image": "docker.io/stack/web",
        "tag": "2.2.0",
        "latest": "3.1.0",
        "candidate": "3.1.0",
        "major": True,
        "same_major": "2.4.0",
    }
    db = {"image": "docker.io/stack/db", "tag": "5.5", "latest": "6.2", "major": True, "same_major": "5.10"}
    cache = {"image": "docker.io/library/cache", "tag": "7", "latest": "8", "major": True}
    fake_hub.hosts["echo"] = host_view("echo", containers=containers_view([web, db], 2))
    await setup(hass, entry)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF  # newer tags stay manual (ADR-0150)
    assert hass.states.get("sensor.vordr_echo_outdated_images").attributes["ready"] == 2
    assert state.attributes["release_summary"] == (
        "Only reported, Vördr does not update images: "
        "stack/web 2.2.0 → 3.1.0 (new major version, or 2.4.0 in the same major version), "
        "stack/db 5.5 → 6.2 (new major version, too new, or 5.10 in the same major version)"
    )
    assert state.attributes["updates"][0]["same_major"] == "2.4.0"

    # Without a step within the running major version: as before.
    await fake_hub.push_host(host_view("echo", containers=containers_view([cache], 0)))
    cache_only = "Only reported, Vördr does not update images: cache 7 → 8 (new major version, too new)"
    eid = "update.vordr_echo_containers"
    await wait_until(lambda: hass.states.get(eid).attributes["release_summary"] == cache_only)
    state = hass.states.get(eid)
    assert state.state == STATE_OFF and "same_major" not in state.attributes["updates"][0]


VULNERABLE_DB = {
    "image": "docker.io/stack/db",
    "tag": "4",
    "containers": ["db"],
    "release": "Debian:13",
    "fixable": 2,
    "unfixed": 1,
    "ids": ["CVE-2023-3", "CVE-2025-1", "CVE-2025-2"],
}
UNFIXED_NGINX = {"image": "docker.io/library/nginx", "tag": "alpine", "release": "Alpine:v3.23", "unfixed": 1}
NOT_MATCHED = {"image": "docker.io/library/webapp", "tag": "local", "code": "unsupported_distribution"}


async def test_container_images_with_vulnerabilities(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0120 with ADR-0130: known vulnerabilities alone never turn the entity on, only a newer image to install does;
    the summary says per image whether one is ready. Images that were not matched carry their code in the attributes,
    not in the summary."""
    view = {
        **containers_view([], 0),
        "fixable": 0,
        "unfixed": 1,
        "vulnerable": [UNFIXED_NGINX, NOT_MATCHED],
    }
    fake_hub.hosts["echo"] = host_view("echo", containers=view)
    await setup(hass, entry)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF  # nothing can be done about an advisory without a fix
    assert state.attributes["release_summary"] == "Known vulnerabilities: nginx alpine (1 without fix)"
    assert state.attributes["vulnerable"][1] == NOT_MATCHED and state.attributes["fixable"] == 0

    # A hub before ADR-0130: fixable, but it does not say whether a newer image is there - off, plain counts.
    view = {**view, "fixable": 1, "unfixed": 2, "vulnerable": [VULNERABLE_DB, UNFIXED_NGINX, NOT_MATCHED]}
    await fake_hub.push_host(host_view("echo", containers=view))
    old_hub = "Known vulnerabilities: stack/db 4 (2 fixable, 1 without fix), nginx alpine (1 without fix)"
    await wait_until(lambda: hass.states.get("update.vordr_echo_containers").attributes["release_summary"] == old_hub)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF and state.attributes["latest_version"] == "4 images"
    assert state.attributes["vulnerable"][0]["ids"] == VULNERABLE_DB["ids"]
    assert "vulnerable" in state.state_info["unrecorded_attributes"]

    # Fixed in the distribution, but the publisher has not rebuilt the image yet: off, and the summary says so.
    view = {**view, "fixable_newer": 0}
    await fake_hub.push_host(host_view("echo", containers=view))
    waiting = (
        "Known vulnerabilities: stack/db 4 (2 fixed in the distribution, no newer image yet, 1 without fix), "
        "nginx alpine (1 without fix)"
    )
    await wait_until(lambda: hass.states.get("update.vordr_echo_containers").attributes["release_summary"] == waiting)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF and state.attributes["fixable_newer"] == 0
    assert "newer" not in state.attributes["vulnerable"][0]

    # A rebuild old enough is there, but of a service the host does not allow: still off (ADR-0150), the summary and
    # the sensor "Outdated images" say what is there.
    db_newer = {**VULNERABLE_DB, "newer": True}
    view = {
        **containers_view([DB], 1),
        "fixable": 1,
        "fixable_newer": 1,
        "unfixed": 1,
        "vulnerable": [db_newer],
        "installable": 0,
    }
    await fake_hub.push_host(host_view("echo", containers=view))
    await wait_until(lambda: hass.states.get("sensor.vordr_echo_outdated_images").state == "1")
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF and state.attributes["release_summary"].endswith("1 without fix)")
    assert hass.states.get("sensor.vordr_echo_outdated_images").attributes["installable"] == 0

    # The host allows the service, so the button takes the rebuild: on, with how many of the images it fixes.
    caps = {**host_view("echo")["capabilities"], "install": True, "image_updates": ["stack/db"]}
    view = {**view, "updates": [{**DB, "services": ["stack/db"]}], "installable": 1}
    await fake_hub.push_host(host_view("echo", containers=view, capabilities=caps))
    await wait_until(lambda: hass.states.get("update.vordr_echo_containers").state == STATE_ON)
    attrs = hass.states.get("update.vordr_echo_containers").attributes
    assert attrs["latest_version"] == "1 outdated, 1 old enough, 1 fixable by a newer image"
    assert attrs["release_summary"].endswith(
        "Known vulnerabilities: stack/db 4 (2 fixable, newer image ready, 1 without fix)"
    )
    assert attrs["vulnerable"][0]["newer"] is True and attrs["fixable_newer"] == 1


async def test_container_images_kernel_advisories_apart(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """ADR-0122: the kernel's own advisories in an image are shown in the attribute with their count, but neither turn
    the entity on nor appear in the summary."""
    db = {**VULNERABLE_DB, "fixable": 0, "kernel": 10}
    headers_only = {
        "image": "ghcr.io/org/builder",
        "tag": "latest",
        "release": "Debian:13",
        "fixable": 0,
        "unfixed": 0,
        "kernel": 3000,
    }
    view = {**containers_view([], 0), "fixable": 0, "unfixed": 1, "vulnerable": [db, headers_only]}
    fake_hub.hosts["echo"] = host_view("echo", containers=view)
    await setup(hass, entry)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.state == STATE_OFF
    assert state.attributes["release_summary"] == "Known vulnerabilities: stack/db 4 (1 without fix)"
    assert state.attributes["vulnerable"][0]["kernel"] == 10
    assert state.attributes["vulnerable"][1] == {
        "image": "ghcr.io/org/builder",
        "tag": "latest",
        "release": "Debian:13",
        "kernel": 3000,
    }


async def test_container_images_compare_failed(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0126: the images whose comparison got no answer are listed with why, beside the count; not recorded."""
    view = {
        **containers_view([], 0),
        "outdated_failed": 2,
        "compare_failed": [
            {"image": "docker.io/stack/logs", "tag": "2.1.0", "containers": ["logs"], "code": "tag_list_too_long"},
            {"image": "docker.io/org/chat", "tag": "latest", "code": "manifest_share"},
        ],
    }
    fake_hub.hosts["echo"] = host_view("echo", containers=view)
    await setup(hass, entry)
    state = hass.states.get("update.vordr_echo_containers")
    assert state.attributes["outdated_failed"] == 2
    assert state.attributes["compare_failed"] == [
        {"image": "docker.io/stack/logs", "tag": "2.1.0", "containers": ["logs"], "code": "tag_list_too_long"},
        {"image": "docker.io/org/chat", "tag": "latest", "code": "manifest_share"},
    ]
    assert "compare_failed" in state.state_info["unrecorded_attributes"]


async def test_container_images_by_button(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    """ADR-0119: with Compose services the host allows, the entity installs - every allowed service with a rebuild
    old enough - and vordr.update_image does it for one; a service the hub does not offer sends nothing. A run of
    images shows its progress here, not on the system packages."""
    eid, system = "update.vordr_echo_containers", "update.vordr_echo_system"
    caps = {**host_view("echo")["capabilities"], "install": True, "outside_window": True, "image_updates": ["stack/db"]}
    db = {**DB, "services": ["stack/db"]}
    fake_hub.hosts["echo"] = host_view("echo", containers=containers_view([db, APP], 1), capabilities=caps)
    await setup(hass, entry)
    state = hass.states.get(eid)
    # a hub before installable: the allowed services decide, as for the button
    assert state.state == STATE_ON and state.attributes["latest_version"] == "2 outdated, 1 old enough"
    assert state.attributes["supported_features"] == UpdateEntityFeature.INSTALL | UpdateEntityFeature.PROGRESS
    assert state.attributes["release_summary"].startswith(
        "The install button takes rebuilds of the allowed services; newer tags stay manual: stack/db 3 (rebuilt)"
    )
    assert state.attributes["updates"][0]["services"] == ["stack/db"]

    await hass.services.async_call("update", "install", {"entity_id": eid}, blocking=True)
    assert (fake_hub.commands[-1]["node"], fake_hub.commands[-1]["command"]) == ("echo", "install_images")
    await hass.services.async_call(DOMAIN, "update_image", {"entity_id": eid, "service": "stack/db"}, blocking=True)
    assert fake_hub.commands[-1]["command"] == "install_image:stack/db" and "reason" not in fake_hub.commands[-1]
    await hass.services.async_call(
        DOMAIN, "update_image", {"entity_id": eid, "service": "stack/db", "reason": "now"}, blocking=True
    )
    assert (fake_hub.commands[-1]["command"], fake_hub.commands[-1]["reason"]) == ("install_image:stack/db", "now")
    await hass.services.async_call(DOMAIN, "install_outside_window", {"entity_id": eid, "reason": "now"}, blocking=True)
    assert (fake_hub.commands[-1]["command"], fake_hub.commands[-1]["reason"]) == ("install_images", "now")
    sent = len(fake_hub.commands)
    for service in ("stack/web", "stack/db --pull always", "other/app"):
        with pytest.raises(HomeAssistantError) as err:
            await hass.services.async_call(
                DOMAIN, "update_image", {"entity_id": eid, "service": service}, blocking=True
            )
        assert err.value.translation_key == "update_image_not_allowed"
    assert len(fake_hub.commands) == sent
    fake_hub.command_code = "not_allowed"
    with pytest.raises(HomeAssistantError):
        await hass.services.async_call("update", "install", {"entity_id": eid}, blocking=True)

    run = {"verb": "install", "scope": "image:stack/db", "state": "installing", "progress": 40, "job_id": "j1"}
    await fake_hub.push_host(
        host_view("echo", containers=containers_view([db, APP], 1), capabilities=caps, last_run=run)
    )
    await wait_until(lambda: hass.states.get(eid).attributes["in_progress"])
    assert hass.states.get(eid).attributes["update_percentage"] == 40
    assert not hass.states.get(system).attributes["in_progress"]
    # ... and a run of the system packages shows there, not here.
    await fake_hub.push_host(
        host_view("echo", containers=containers_view([db, APP], 1), capabilities=caps, last_run={**run, "scope": "all"})
    )
    await wait_until(lambda: hass.states.get(system).attributes["in_progress"])
    assert not hass.states.get(eid).attributes["in_progress"]

    # The hub says the button would take nothing now (only a newer tag, or no rebuild of an allowed service): no button,
    # and off (ADR-0150) - Home Assistant would list it as an update that cannot be installed. The sensor counts it.
    await fake_hub.push_host(
        host_view("echo", containers={**containers_view([db, APP], 1), "installable": 0}, capabilities=caps)
    )
    await wait_until(lambda: hass.states.get(eid).attributes["supported_features"] == 0)
    state = hass.states.get(eid)
    assert state.state == STATE_OFF and state.attributes["latest_version"] == "4 images"
    assert state.attributes["release_summary"].startswith("The install button takes rebuilds of the allowed services")
    sensor = hass.states.get("sensor.vordr_echo_outdated_images").attributes
    assert (sensor["ready"], sensor["installable"]) == (1, 0)
    await fake_hub.push_host(
        host_view("echo", containers={**containers_view([db, APP], 1), "installable": 1}, capabilities=caps)
    )
    await wait_until(lambda: hass.states.get(eid).attributes["supported_features"] != 0)
    assert hass.states.get(eid).state == STATE_ON

    # Without allowed services the entity only reports, and stays off.
    await fake_hub.push_host(
        host_view("echo", containers=containers_view([DB], 1), capabilities={**caps, "image_updates": []})
    )
    await wait_until(lambda: hass.states.get(eid).attributes["supported_features"] == 0)
    state = hass.states.get(eid)
    assert state.state == STATE_OFF
    assert state.attributes["release_summary"].startswith("Only reported, Vördr does not update images:")
