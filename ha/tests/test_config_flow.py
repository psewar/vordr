"""Config flow: validation, probe stages, waiting for approval, refusals."""

from __future__ import annotations

import socket

from homeassistant import config_entries
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType

from custom_components.vordr import protocol as p
from custom_components.vordr.const import CONF_CA_PEM, CONF_NAME, CONF_URL, DOMAIN

from .conftest import FakeHub, self_signed, wait_until


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


async def test_name_taken_by_another_key(hass: HomeAssistant, fake_hub: FakeHub) -> None:
    fake_hub.clients["homeassistant"] = {"fp": "0" * 64, "state": "approved"}
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "rejected_key_mismatch"


async def test_protocol_mismatch(hass: HomeAssistant, fake_hub: FakeHub) -> None:
    fake_hub.protocol = p.PROTOCOL + 1
    result = await start(hass, fake_hub.url, ca=fake_hub.cert_pem)
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "rejected_protocol_mismatch"
