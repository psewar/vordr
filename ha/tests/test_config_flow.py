"""Config flow: validation, probe stages, waiting for approval, the kept key, refusals."""

from __future__ import annotations

import json
from pathlib import Path
import socket
import string
from typing import Any

from homeassistant import config_entries
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType
import pytest

from custom_components.vordr import config_flow, protocol as p
from custom_components.vordr.const import CONF_CA_PEM, CONF_NAME, CONF_URL, DOMAIN

from .conftest import FakeHub, self_signed, wait_until

LANGS = {
    f.stem: json.loads(f.read_text(encoding="utf-8"))
    for f in (Path(__file__).parents[2] / "custom_components" / "vordr" / "translations").glob("*.json")
}


async def start(hass: HomeAssistant, url: str, name: str = "homeassistant", ca: str = "") -> dict:
    result = await hass.config_entries.flow.async_init(DOMAIN, context={"source": config_entries.SOURCE_USER})
    assert result["type"] is FlowResultType.FORM
    return await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_URL: url, CONF_NAME: name, CONF_CA_PEM: ca}
    )


async def test_input_is_checked(hass: HomeAssistant) -> None:
    result = await start(hass, "https://hub.example/v1/ha", name="Home Assistant")
    assert result["errors"] == {CONF_URL: "invalid_url", CONF_NAME: "invalid_name"}
    result = await start(hass, "wss://hub.example:8443/v1/ha", ca="no certificate")
    assert result["errors"] == {CONF_CA_PEM: "invalid_ca"}


async def test_probe_names_the_failing_stage(hass: HomeAssistant, fake_hub: FakeHub, socket_enabled: None) -> None:
    with socket.socket() as s:  # a port nobody listens on
        s.bind(("127.0.0.1", 0))
        free = s.getsockname()[1]
    result = await start(hass, f"wss://127.0.0.1:{free}/v1/ha")
    assert result["errors"] == {"base": "probe_connect"}
    # The same flow again, as a user correcting the form: a wrong CA, then the right one.
    other_ca, _ = self_signed()
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_URL: fake_hub.url, CONF_NAME: "homeassistant", CONF_CA_PEM: other_ca}
    )
    assert result["errors"] == {"base": "probe_tls"}
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {CONF_URL: fake_hub.url, CONF_NAME: "homeassistant", CONF_CA_PEM: fake_hub.cert_pem}
    )
    assert result["type"] is FlowResultType.SHOW_PROGRESS  # every stage passed
    # The user closes the dialog while it waits for approval: the flow goes, and with it the waiting connection.
    # Left open, it outlives the test and the fake hub, and is collected unclosed in some later test.
    await wait_until(lambda: any(name == "homeassistant" for name, _ in fake_hub.conns))
    hass.config_entries.flow.async_abort(result["flow_id"])
    await hass.async_block_till_done()
    await wait_until(lambda: not fake_hub.conns, describe=lambda: fake_hub.conns)


async def test_wait_for_approval_then_entry(hass: HomeAssistant, fake_hub: FakeHub) -> None:
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    fp = result["description_placeholders"]["fingerprint"]
    assert result["description_placeholders"]["command"] == f"vordr-hub approve-client homeassistant --fingerprint {fp}"
    await wait_until(lambda: "homeassistant" in fake_hub.clients)
    assert fake_hub.clients["homeassistant"]["fp"] == fp
    await fake_hub.approve("homeassistant")
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert result["data"][CONF_URL] == fake_hub.url
    key = p.load_private_key(result["data"]["private_key"])
    assert p.fingerprint(p.public_key(key)) == fp
    await hass.async_block_till_done()
    await wait_until(
        lambda: (
            hass.states.get("binary_sensor.vordr_hub_online") is not None
            and hass.states.get("binary_sensor.vordr_hub_online").state == "on"
        )
    )
    # Close the client's connection before the fake hub goes away (keeps the hub's teardown clean).
    assert await hass.config_entries.async_unload(result["result"].entry_id)


async def test_setting_up_again_continues_the_waiting_request(
    hass: HomeAssistant, fake_hub: FakeHub, hass_storage: dict[str, Any], monkeypatch: pytest.MonkeyPatch
) -> None:
    """The hub keeps a waiting request with the key of its first hello. A closed dialog, the hour running out or a
    restart of Home Assistant must not cost that request: setting up again for the same hub and name uses the same
    key, and the operator's approval of the fingerprint shown first lets it in (ADR-0156)."""
    monkeypatch.setattr(config_flow, "APPROVAL_TIMEOUT", 0.3)
    hub = f"127.0.0.1:{fake_hub.port}"
    # 1. The user closes the dialog while it waits.
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    fp = result["description_placeholders"]["fingerprint"]
    await wait_until(lambda: "homeassistant" in fake_hub.clients)
    hass.config_entries.flow.async_abort(result["flow_id"])
    await hass.async_block_till_done()
    await wait_until(lambda: not fake_hub.conns, describe=lambda: fake_hub.conns)
    key = hass_storage[config_flow.PENDING_KEYS_STORE]["data"][hub]["homeassistant"]
    assert p.fingerprint(p.public_key(p.load_private_key(key))) == fp
    # 2. Again, and nobody decides within the time.
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    assert result["description_placeholders"]["fingerprint"] == fp
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "approval_timeout"
    assert fake_hub.clients["homeassistant"] == {"fp": fp, "state": "pending_approval"}  # no key_mismatch
    # 3. After a restart of Home Assistant (the key comes from the storage), the operator approves.
    hass.data.pop(config_flow.PENDING_KEYS_STORE)
    monkeypatch.setattr(config_flow, "APPROVAL_TIMEOUT", 60.0)
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    assert result["description_placeholders"]["fingerprint"] == fp
    await wait_until(lambda: any(name == "homeassistant" for name, _ in fake_hub.conns))
    await fake_hub.approve("homeassistant")
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert result["data"]["private_key"] == key
    # From now on the entry holds the key, and only the entry.
    assert hass_storage[config_flow.PENDING_KEYS_STORE]["data"] == {}
    await hass.async_block_till_done()
    assert await hass.config_entries.async_unload(result["result"].entry_id)


async def test_a_key_per_hub_and_client_name(hass: HomeAssistant) -> None:
    keys = config_flow.PendingKeys(hass)
    first = await keys.key("hub-a.example:8443", "homeassistant")
    assert await keys.key("hub-a.example:8443", "homeassistant") == first
    other_name = await keys.key("hub-a.example:8443", "test-ha")
    other_hub = await keys.key("hub-b.example:8443", "homeassistant")
    assert len({first, other_name, other_hub}) == 3
    await keys.set_up("hub-a.example:8443")  # the entry of hub-a holds its key now
    assert await keys.key("hub-a.example:8443", "homeassistant") not in (first, other_name)
    assert await keys.key("hub-b.example:8443", "homeassistant") == other_hub


async def test_name_taken_by_another_key(hass: HomeAssistant, fake_hub: FakeHub) -> None:
    """A real key mismatch: the hub knows the name with a key this Home Assistant does not have. Refused, and the text
    names the client, this Home Assistant's fingerprint and the way out at the hub."""
    fake_hub.clients["homeassistant"] = {"fp": "0" * 64, "state": "approved"}
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    fp = result["description_placeholders"]["fingerprint"]
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "rejected_key_mismatch"
    assert result["description_placeholders"] == {"name": "homeassistant", "fingerprint": fp}
    for lang, data in LANGS.items():
        text = data["config"]["abort"]["rejected_key_mismatch"].format(**result["description_placeholders"])
        assert "`vordr-hub forget-client homeassistant`" in text and fp in text, lang
    # The operator forgets the old client at the hub: the next attempt waits with the same key.
    fake_hub.clients.pop("homeassistant")
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    assert result["description_placeholders"]["fingerprint"] == fp
    await wait_until(lambda: any(name == "homeassistant" for name, _ in fake_hub.conns))
    assert fake_hub.clients["homeassistant"] == {"fp": fp, "state": "pending_approval"}
    hass.config_entries.flow.async_abort(result["flow_id"])
    await hass.async_block_till_done()
    await wait_until(lambda: not fake_hub.conns, describe=lambda: fake_hub.conns)


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_refusal_texts_use_what_the_flow_passes(lang: str) -> None:
    """Every refusal of the flow gets the client name and the fingerprint, and its text needs nothing else."""
    for reason, text in LANGS[lang]["config"]["abort"].items():
        fields = {field for _, field, _, _ in string.Formatter().parse(text) if field}
        assert fields <= {"name", "fingerprint"}, (reason, fields)
    progress = LANGS[lang]["config"]["progress"]["approval"]
    assert {field for _, field, _, _ in string.Formatter().parse(progress) if field} == {"command", "fingerprint"}


async def test_protocol_mismatch(hass: HomeAssistant, fake_hub: FakeHub) -> None:
    fake_hub.protocol = p.PROTOCOL + 1
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "rejected_protocol_mismatch"
