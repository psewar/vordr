"""Diagnostics download: connection, versions, hosts - never the key."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.diagnostics import async_redact_data

from .const import CONF_CA_PEM, CONF_PRIVATE_KEY, VERSION

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant

    from . import VordrConfigEntry

REDACT = {CONF_PRIVATE_KEY, CONF_CA_PEM}


async def async_get_config_entry_diagnostics(hass: HomeAssistant, entry: VordrConfigEntry) -> dict[str, Any]:
    coordinator = entry.runtime_data
    data = coordinator.data
    return {
        "entry": async_redact_data(dict(entry.data), REDACT),
        "integration_version": VERSION,
        "fingerprint": coordinator.client.fingerprint,
        "connection": data.connection.value,
        "reason": data.reason,
        "synced": data.synced,
        "hub": data.hub,
        "hosts": data.hosts,
    }
