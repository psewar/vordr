"""Config flow: address of the hub, own key, wait until the operator admits this client at the hub."""

from __future__ import annotations

import asyncio
import ssl
from typing import Any

from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.helpers.selector import TextSelector, TextSelectorConfig
import voluptuous as vol
from yarl import URL

from . import protocol as p
from .client import VordrClient, probe, ssl_context
from .const import CONF_CA_PEM, CONF_NAME, CONF_PRIVATE_KEY, CONF_URL, DEFAULT_NAME, DOMAIN, VERSION

APPROVAL_TIMEOUT = 3600.0


class VordrConfigFlow(ConfigFlow, domain=DOMAIN):
    """One entry per hub."""

    VERSION = 1

    def __init__(self) -> None:
        self._data: dict[str, Any] = {}
        self._fingerprint = ""
        self._task: asyncio.Task[Any] | None = None
        self._outcome = ""

    async def async_step_user(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        errors: dict[str, str] = {}
        if user_input is not None:
            url = user_input[CONF_URL].strip()
            name = user_input[CONF_NAME].strip()
            ca_pem = (user_input.get(CONF_CA_PEM) or "").strip()
            u = URL(url)
            if u.scheme != "wss" or not u.host or u.path != p.PATH:
                errors[CONF_URL] = "invalid_url"
            if not p.NAME_PATTERN.match(name):
                errors[CONF_NAME] = "invalid_name"
            if ca_pem and not errors:
                try:
                    await ssl_context(self.hass, ca_pem)
                except ssl.SSLError:  # only parsing tells whether a PEM holds a certificate
                    errors[CONF_CA_PEM] = "invalid_ca"
            if not errors:
                await self.async_set_unique_id(f"{u.host}:{u.port}")
                self._abort_if_unique_id_configured()
                if (stage := await probe(self.hass, url, ca_pem)) is not None:
                    errors["base"] = f"probe_{stage.value}"
            if not errors:
                key = p.new_private_key()
                self._data = {CONF_URL: url, CONF_NAME: name, CONF_CA_PEM: ca_pem, CONF_PRIVATE_KEY: key}
                self._fingerprint = p.fingerprint(p.public_key(p.load_private_key(key)))
                return await self.async_step_approval()
        schema = vol.Schema(
            {
                vol.Required(CONF_URL, default=(user_input or {}).get(CONF_URL, "wss://")): str,
                vol.Required(CONF_NAME, default=(user_input or {}).get(CONF_NAME, DEFAULT_NAME)): str,
                vol.Optional(CONF_CA_PEM, default=(user_input or {}).get(CONF_CA_PEM, "")): TextSelector(
                    TextSelectorConfig(multiline=True)
                ),
            }
        )
        return self.async_show_form(step_id="user", data_schema=schema, errors=errors)

    async def async_step_approval(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        """Show fingerprint and command while waiting for the operator."""
        if self._task is None:
            client = VordrClient(
                self.hass,
                self._data[CONF_URL],
                self._data[CONF_CA_PEM],
                self._data[CONF_NAME],
                p.load_private_key(self._data[CONF_PRIVATE_KEY]),
                VERSION,
                lambda _msg: None,
                lambda _state, _reason: None,
            )
            self._task = self.hass.async_create_task(client.wait_for_approval(APPROVAL_TIMEOUT))
        if not self._task.done():
            return self.async_show_progress(
                step_id="approval",
                progress_action="approval",
                progress_task=self._task,
                description_placeholders={
                    "fingerprint": self._fingerprint,
                    "command": f"vordr-hub approve-client {self._data[CONF_NAME]} --fingerprint {self._fingerprint}",
                },
            )
        status = self._task.result()
        if status is None:
            self._outcome = "unreachable"
        elif status.state == p.STATE_APPROVED:
            self._outcome = ""
        elif status.state == p.STATE_PENDING:
            self._outcome = "approval_timeout"
        else:
            self._outcome = f"rejected_{status.reason}" if status.reason in _REJECT_REASONS else "rejected"
        return self.async_show_progress_done(next_step_id="done" if not self._outcome else "failed")

    async def async_step_done(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        host = URL(self._data[CONF_URL]).host
        return self.async_create_entry(title=f"Vördr ({host})", data=self._data)

    async def async_step_failed(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        return self.async_abort(reason=self._outcome)


_REJECT_REASONS = {"key_mismatch", "too_many_pending", "rejected", "protocol_mismatch", "bad_hello"}
