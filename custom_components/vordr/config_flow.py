"""Config flow: address of the hub, own key, wait until the operator admits this client at the hub. Options flow: a
menu of the integration's settings: the rules for Home Assistant's own updates (ADR-0158) and whether Vördr installs
them (ADR-0162), the operator keys (ADR-0161), and the optional AI explanations of failed runs (ADR-0166). Subentry
flow "Add host": installing the agent on a new Linux host over SSH (ADR-0161), a button on the integration's page that
stores nothing (ADR-0185)."""

from __future__ import annotations

import asyncio
import importlib.util
import ssl
from types import ModuleType
from typing import TYPE_CHECKING, Any

from homeassistant.components.update import DOMAIN as UPDATE_DOMAIN
from homeassistant.config_entries import (
    ConfigEntry,
    ConfigFlow,
    ConfigFlowResult,
    ConfigSubentryFlow,
    OptionsFlow,
    SubentryFlowResult,
)
from homeassistant.core import HomeAssistant, callback
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.importlib import async_import_module
from homeassistant.helpers.selector import (
    BooleanSelector,
    EntitySelector,
    EntitySelectorConfig,
    NumberSelector,
    NumberSelectorConfig,
    NumberSelectorMode,
    SelectSelector,
    SelectSelectorConfig,
    SelectSelectorMode,
    TextSelector,
    TextSelectorConfig,
    TextSelectorType,
    TimeSelector,
)
from homeassistant.helpers.storage import Store
from homeassistant.util.hass_dict import HassKey
import voluptuous as vol
from yarl import URL

from . import agent_install as ai, ai_explain as ax, ha_updates as hu, protocol as p
from .client import VordrClient, probe, ssl_context
from .const import CONF_CA_PEM, CONF_NAME, CONF_PRIVATE_KEY, CONF_URL, DEFAULT_NAME, DOMAIN, VERSION

if TYPE_CHECKING:
    from .agent_installer import AgentInstall

APPROVAL_TIMEOUT = 3600.0
PENDING_KEYS_STORE = f"{DOMAIN}.pending_keys"


class PendingKeys:
    """The key of a client waiting for approval, per hub and client name, until the hub's entry exists (ADR-0156).

    The hub fixes a client's key at its first hello and keeps the waiting request; another key under the same name is
    `key_mismatch`. Kept here, adding the integration again after a timeout, a closed dialog or a restart of Home
    Assistant continues that request instead of starting one the hub refuses.
    """

    def __init__(self, hass: HomeAssistant) -> None:
        # private: mode 0600, like the config entries that hold the key once the client is set up.
        self._store: Store[dict[str, dict[str, str]]] = Store(hass, 1, PENDING_KEYS_STORE, private=True)
        self._keys: dict[str, dict[str, str]] | None = None
        self._lock = asyncio.Lock()

    async def _loaded(self) -> dict[str, dict[str, str]]:
        async with self._lock:
            if self._keys is None:
                self._keys = await self._store.async_load() or {}
            return self._keys

    async def key(self, hub: str, name: str) -> str:
        """The waiting key for this hub and client name; a new one, stored before the hub sees it, if there is none."""
        keys = await self._loaded()
        key = keys.get(hub, {}).get(name)
        if key is None:
            key = p.new_private_key()
            keys.setdefault(hub, {})[name] = key
            await self._store.async_save(keys)
        return key

    async def set_up(self, hub: str) -> None:
        """The hub's entry holds the key from now on; nothing of this hub waits here any more."""
        keys = await self._loaded()
        if hub in keys:
            del keys[hub]
            await self._store.async_save(keys)


_PENDING_KEYS: HassKey[PendingKeys] = HassKey(PENDING_KEYS_STORE)


def pending_keys(hass: HomeAssistant) -> PendingKeys:
    """The one PendingKeys of this Home Assistant."""
    if _PENDING_KEYS not in hass.data:
        hass.data[_PENDING_KEYS] = PendingKeys(hass)
    return hass.data[_PENDING_KEYS]


class VordrConfigFlow(ConfigFlow, domain=DOMAIN):
    """One entry per hub."""

    VERSION = 1

    @staticmethod
    @callback
    def async_get_options_flow(config_entry: ConfigEntry) -> VordrOptionsFlow:
        return VordrOptionsFlow()

    @classmethod
    @callback
    def async_get_supported_subentry_types(cls, config_entry: ConfigEntry) -> dict[str, type[ConfigSubentryFlow]]:
        """The button "Add host" on the integration's page (ADR-0185). Offered for every entry: whether it can run now
        is what its first step checks and says."""
        return {HOST_SUBENTRY: AddHostFlow}

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
            hub = f"{u.host}:{u.port}"
            if not errors:
                await self.async_set_unique_id(hub)
                self._abort_if_unique_id_configured()
                if (stage := await probe(self.hass, url, ca_pem)) is not None:
                    errors["base"] = f"probe_{stage.value}"
            if not errors:
                # The same key as any earlier attempt for this hub and name: the hub keeps that request (ADR-0156).
                key = await pending_keys(self.hass).key(hub, name)
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
        u = URL(self._data[CONF_URL])
        await pending_keys(self.hass).set_up(f"{u.host}:{u.port}")
        return self.async_create_entry(title=f"Vördr ({u.host})", data=self._data)

    async def async_step_failed(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        # Every refusal may name the client and this Home Assistant's fingerprint, e.g. to tell it from the key the
        # hub already knows under that name (key_mismatch).
        return self.async_abort(
            reason=self._outcome,
            description_placeholders={"name": self._data[CONF_NAME], "fingerprint": self._fingerprint},
        )


_REJECT_REASONS = {"key_mismatch", "too_many_pending", "rejected", "protocol_mismatch", "bad_hello"}

# The items of the options menu, the integration's settings; each keeps its options under its own key of the entry's
# options. Installing the agent is no setting: it is the button "Add host" (ADR-0185).
OPTIONS_MENU = [hu.OPTIONS_KEY, ai.OPTIONS_KEY, ax.OPTIONS_KEY]
# The subentry type behind the button "Add host". No subentry of it is ever stored: the hub's registry is the one list
# of hosts (ADR-0185).
HOST_SUBENTRY = "host"


def _secret(key: str, required: bool = False) -> tuple[vol.Marker, TextSelector]:
    """A field for a secret: never prefilled, so that it never travels back to the browser."""
    marker = vol.Required(key) if required else vol.Optional(key)
    return marker, TextSelector(TextSelectorConfig(type=TextSelectorType.PASSWORD))


SSH_LIBRARY = "asyncssh"


async def _agent_installer(hass: HomeAssistant) -> ModuleType | None:
    """The install over SSH, imported when "Add host" is opened, not when the integration loads (outside the event
    loop: it brings asyncssh). None when the SSH library is not installed: everything else of the integration works
    without it, and "Add host" says why it cannot run."""
    if await hass.async_add_executor_job(importlib.util.find_spec, SSH_LIBRARY) is None:
        return None
    return await async_import_module(hass, f"{__package__}.agent_installer")


class VordrOptionsFlow(OptionsFlow):
    """The integration's settings as a menu; only Home Assistant's administrators reach it. Saving one item keeps the
    options of the others."""

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        return self.async_show_menu(step_id="init", menu_options=OPTIONS_MENU)

    # --- operator keys (ADR-0161): what the agent release must be signed with, entered here, never taken from the hub

    async def async_step_operator_keys(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        errors: dict[str, str] = {}
        if user_input is not None:
            clean, errors = ai.check_operator_keys(user_input)
            if not errors:
                options = {k: v for k, v in self.config_entry.options.items() if k != ai.OPTIONS_KEY}
                if clean[ai.CONF_OPERATOR_KEY]:
                    options[ai.OPTIONS_KEY] = clean
                return self.async_create_entry(data=options)
        stored = self.config_entry.options.get(ai.OPTIONS_KEY)
        shown = user_input if user_input is not None else (stored if isinstance(stored, dict) else {})
        schema = vol.Schema(
            {
                vol.Optional(ai.CONF_OPERATOR_KEY, default=str(shown.get(ai.CONF_OPERATOR_KEY) or "")): str,
                vol.Optional(
                    ai.CONF_BACKUP_OPERATOR_KEY, default=str(shown.get(ai.CONF_BACKUP_OPERATOR_KEY) or "")
                ): str,
            }
        )
        return self.async_show_form(step_id=ai.OPTIONS_KEY, data_schema=schema, errors=errors)

    # --- AI explanations of failed runs (ADR-0166): optional, off while no entity is chosen

    async def async_step_ai_explanations(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        """An AI Task entity and the confirmation that package names and log excerpts of failed runs go to its
        provider. Empty turns it off; the entity must exist and generate data when it is saved."""
        errors: dict[str, str] = {}
        if user_input is not None:
            clean, errors = ax.check_options(user_input)
            if not errors and clean and (code := ax.entity_code(self.hass, clean[ax.CONF_ENTITY])) != ax.OK:
                errors = {ax.CONF_ENTITY: ax.FORM_ERROR_OF[code]}
            if not errors:
                options = {k: v for k, v in self.config_entry.options.items() if k != ax.OPTIONS_KEY}
                if clean:
                    options[ax.OPTIONS_KEY] = clean
                return self.async_create_entry(data=options)
        stored = self.config_entry.options.get(ax.OPTIONS_KEY)
        shown = user_input if user_input is not None else (stored if isinstance(stored, dict) else {})
        entity = shown.get(ax.CONF_ENTITY)
        schema = vol.Schema(
            {
                # A suggestion, not a default: an emptied field stays empty, and that turns it off.
                vol.Optional(ax.CONF_ENTITY, description={"suggested_value": entity or None}): EntitySelector(
                    EntitySelectorConfig(domain=ax.AI_TASK)
                ),
                vol.Required(ax.CONF_DATA_ACK, default=shown.get(ax.CONF_DATA_ACK) is True): BooleanSelector(),
            }
        )
        return self.async_show_form(step_id=ax.OPTIONS_KEY, data_schema=schema, errors=errors)

    async def async_step_ha_updates(self, user_input: dict[str, Any] | None = None) -> ConfigFlowResult:
        """Home Assistant's own updates (ADR-0158): what Vördr's rules allow, the window, the minimum age, and whether
        Vördr installs what they let through (ADR-0162). Nothing chosen, nothing allowed; installing is off until it is
        switched on, and only one entry of Vördr may install."""
        errors: dict[str, str] = {}
        if user_input is not None:
            clean, errors = hu.check_options(user_input)
            if not errors and clean[hu.CONF_AUTO_INSTALL] and self._another_installs():
                errors = {hu.CONF_AUTO_INSTALL: hu.ERR_AUTO_INSTALL_OTHER}
            if not errors:
                return self.async_create_entry(data={**self.config_entry.options, hu.OPTIONS_KEY: clean})
        shown = user_input if user_input is not None else self._current()
        return self.async_show_form(
            step_id="ha_updates",
            data_schema=self._schema(shown),
            errors=errors,
            # The fixed parts of the minimum age, from the rules themselves (ADR-0179, ADR-0180).
            description_placeholders={
                "firmware_days": str(hu.FIRMWARE_MIN_AGE_DAYS),
                "patch_days": str(hu.PATCH_MIN_AGE_DAYS),
            },
        )

    def _another_installs(self) -> bool:
        """Another entry of Vördr (one per hub), loaded or not, has installing switched on."""
        return any(
            (policy := hu.policy_from_options(e.options)) is not None and policy.auto_install
            for e in self.hass.config_entries.async_entries(DOMAIN)
            if e.entry_id != self.config_entry.entry_id
        )

    def _current(self) -> dict[str, Any]:
        """The stored options to show, or the closed defaults."""
        stored = self.config_entry.options.get(hu.OPTIONS_KEY)
        current: dict[str, Any] = {
            hu.CONF_KINDS: [],
            hu.CONF_ENTITIES: [],
            hu.CONF_DAYS: [],
            hu.CONF_START: f"{hu.CLOSED.start:%H:%M}",
            hu.CONF_END: f"{hu.CLOSED.end:%H:%M}",
            hu.CONF_MIN_AGE: hu.MIN_AGE_DEFAULT,
            hu.CONF_AUTO_INSTALL: False,
        }
        if isinstance(stored, dict):
            current.update({k: v for k, v in stored.items() if k in current})
        return current

    def _schema(self, shown: dict[str, Any]) -> vol.Schema:
        # Vördr's own update entities follow the hub's rules: not offered here, and the plan leaves them out anyway.
        own = sorted(
            e.entity_id
            for e in er.async_get(self.hass).entities.values()
            if e.platform == DOMAIN and e.domain == UPDATE_DOMAIN
        )

        def multi(key: str, options: tuple[str, ...]) -> SelectSelector:
            return SelectSelector(
                SelectSelectorConfig(
                    options=list(options), multiple=True, mode=SelectSelectorMode.LIST, translation_key=key
                )
            )

        return vol.Schema(
            {
                vol.Optional(hu.CONF_KINDS, default=shown.get(hu.CONF_KINDS, [])): multi("ha_update_kind", hu.KINDS),
                vol.Optional(hu.CONF_ENTITIES, default=shown.get(hu.CONF_ENTITIES, [])): EntitySelector(
                    EntitySelectorConfig(domain=UPDATE_DOMAIN, multiple=True, exclude_entities=own)
                ),
                vol.Optional(hu.CONF_DAYS, default=shown.get(hu.CONF_DAYS, [])): multi("weekday", hu.DAYS),
                vol.Required(hu.CONF_START, default=shown.get(hu.CONF_START)): TimeSelector(),
                vol.Required(hu.CONF_END, default=shown.get(hu.CONF_END)): TimeSelector(),
                vol.Required(hu.CONF_MIN_AGE, default=shown.get(hu.CONF_MIN_AGE)): NumberSelector(
                    NumberSelectorConfig(min=hu.MIN_AGE_MIN, max=hu.MIN_AGE_MAX, step=1, mode=NumberSelectorMode.BOX)
                ),
                vol.Required(hu.CONF_AUTO_INSTALL, default=shown.get(hu.CONF_AUTO_INSTALL, False)): BooleanSelector(),
            }
        )


class AddHostFlow(ConfigSubentryFlow):
    """The button "Add host": installs the agent on a new Linux host once over SSH (ADR-0161). Only Home Assistant's
    administrators reach it. It ends with an abort whose text says how it went, the fingerprint on success, and stores
    nothing, not even a subentry: the hub's registry stays the one list of hosts, and the new host waits there for
    approval (ADR-0185)."""

    def __init__(self) -> None:
        self._install: AgentInstall | None = None
        self._task: asyncio.Task[ai.Outcome] | None = None

    @callback
    def async_remove(self) -> None:
        """The flow ended in any way: done, aborted, or the dialog closed. Credentials, the connection and the download
        token go with it (ADR-0161); a login under way stops at once and starts nothing, a running install task is
        cancelled by the flow manager and closes its end."""
        if self._install is not None:
            self._install.clear()
        self._install = None

    async def async_step_user(self, user_input: dict[str, Any] | None = None) -> SubentryFlowResult:
        """Host, port, user and node: nothing secret yet. First what "Add host" needs, each missing piece with its own
        text; then the host key, fetched without logging in."""
        entry = self._get_entry()
        if self._install is None:
            if (installer := await _agent_installer(self.hass)) is None:
                return self.async_abort(reason="install_needs_ssh_library")
            self._install = installer.AgentInstall(self.hass, entry)
        if (reason := self._install.precondition()) is not None:
            return self.async_abort(reason=reason)
        errors: dict[str, str] = {}
        if user_input is not None:
            target, errors = ai.check_target(user_input)
            if target is not None:
                code = await self._install.fetch_host_key(target)
                if code == ai.OK:
                    return await self.async_step_host_key()
                errors["base"] = code
        shown = user_input or {}
        schema = vol.Schema(
            {
                vol.Required(ai.CONF_HOST, default=str(shown.get(ai.CONF_HOST, ""))): str,
                vol.Required(ai.CONF_PORT, default=shown.get(ai.CONF_PORT, ai.DEFAULT_PORT)): NumberSelector(
                    NumberSelectorConfig(min=1, max=65535, step=1, mode=NumberSelectorMode.BOX)
                ),
                vol.Required(ai.CONF_USERNAME, default=str(shown.get(ai.CONF_USERNAME, ai.DEFAULT_USERNAME))): str,
                vol.Optional(ai.CONF_NODE, default=str(shown.get(ai.CONF_NODE, ""))): str,
            }
        )
        return self.async_show_form(
            step_id="user",
            data_schema=schema,
            errors=errors,
            # The role the hub asks for: the hub does not tell a client its role, so the text names it (ADR-0185).
            description_placeholders={"client": entry.data[CONF_NAME]},
        )

    async def async_step_host_key(self, user_input: dict[str, Any] | None = None) -> SubentryFlowResult:
        """The host key as the server presented it: the person compares it with the host's own before anything secret
        is entered. Nothing is written to a known_hosts file; the login accepts only this key."""
        assert self._install is not None
        return self.async_show_menu(
            step_id="host_key",
            menu_options=["login", "host_key_rejected"],
            description_placeholders=self._install.host_key_names(),
        )

    async def async_step_host_key_rejected(self, user_input: dict[str, Any] | None = None) -> SubentryFlowResult:
        assert self._install is not None
        reason = "host_key_rejected"
        return self.async_abort(reason=reason, description_placeholders=self._install.names(reason))

    async def async_step_login(self, user_input: dict[str, Any] | None = None) -> SubentryFlowResult:
        """The credentials: used for this login and the install, kept nowhere."""
        install = self._install
        assert install is not None and install.target is not None
        errors: dict[str, str] = {}
        if user_input is not None:
            creds, errors = ai.check_credentials(user_input)
            if creds is not None:
                code, names = await install.login(creds)
                creds.clear()
                if install.ended:
                    # The dialog was closed while logging in (login answered install_cancelled): the flow is gone and
                    # its connection closed; nothing starts. Home Assistant takes an abort from a removed flow.
                    return self.async_abort(reason="install_cancelled")
                if code == ai.OK:
                    self._task = self.hass.async_create_task(install.install(), "vordr agent install")
                    return await self.async_step_install()
                if code not in ai.LOGIN_FORM_ERRORS:
                    return self.async_abort(reason=code, description_placeholders=install.names(code, names))
                errors[ai.LOGIN_FORM_ERRORS[code]] = code
        schema = vol.Schema(
            dict(
                [
                    _secret(ai.CONF_PASSWORD),
                    (vol.Optional(ai.CONF_SSH_KEY), TextSelector(TextSelectorConfig(multiline=True))),
                    _secret(ai.CONF_SSH_KEY_PASSPHRASE),
                    _secret(ai.CONF_SUDO_PASSWORD),
                ]
            )
        )
        return self.async_show_form(
            step_id="login",
            data_schema=schema,
            errors=errors,
            description_placeholders={"host": install.target.host, "username": install.target.username},
        )

    async def async_step_install(self, user_input: dict[str, Any] | None = None) -> SubentryFlowResult:
        assert self._task is not None and self._install is not None and self._install.target is not None
        if not self._task.done():
            return self.async_show_progress(
                step_id="install",
                progress_action="install",
                progress_task=self._task,
                description_placeholders={"host": self._install.target.host},
            )
        return self.async_show_progress_done(next_step_id="result")

    async def async_step_result(self, user_input: dict[str, Any] | None = None) -> SubentryFlowResult:
        """The end: on success the fingerprint and that the host waits for approval at the hub; otherwise the code's
        text with the next step. An abort, not a created subentry, also on success: nothing is stored (ADR-0185). Ends
        the flow, which drops what it held."""
        assert self._task is not None and self._install is not None
        outcome = self._task.result()
        return self.async_abort(
            reason=outcome.code, description_placeholders=self._install.names(outcome.code, outcome.names)
        )
