"""Diagnostics download: connection, versions, hosts - never the key, and nothing that points at the site (ADR-0189).

People attach the download to public issues. The hub's address becomes hub.invalid; host names, this Home Assistant's
name at the hub and the ids of the pip/npm locations (which carry user names) become stable placeholders (host-1,
location-1, ... in the order of the names, client), so the structure stays readable. Paths on the hosts, image
repositories with their registry, log excerpts and log files, and free text (the reason of a run outside its window, the
reason a host is exempt from backups) are redacted. Counts, codes, versions, package names and fingerprints stay: they
are what a diagnosis needs. Home Assistant adds the repair issues to the download itself, with their ids, which name
the host; this module does not write that part.
"""

from __future__ import annotations

import re
from typing import TYPE_CHECKING, Any

from homeassistant.components.diagnostics import async_redact_data
from yarl import URL

from .const import CONF_CA_PEM, CONF_NAME, CONF_PRIVATE_KEY, CONF_URL, VERSION

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant

    from . import VordrConfigEntry

REDACT = {CONF_PRIVATE_KEY, CONF_CA_PEM}
REDACTED = "**REDACTED**"
HUB_HOST = "hub.invalid"
# Text fields whose value names the site or may hold anything: a path on a host, an image's repository with its
# registry, an excerpt of a run log, the log file on the host, the reason of a run outside its window, the reason a host
# is exempt from backups. Redacted where they hold text; a flag of the same name stays.
REDACT_TEXT = frozenset({"path", "image", "log_excerpt", "log_file", "outside_window", "exempt"})
# The hypervisor that backs a guest up before its install: a placeholder if it is one of the hosts, else redacted.
NAME_FIELDS = frozenset({"before_install"})
# Characters that make a name part of a longer one ("vordr-agent" does not hold the host "agent"). A dot ends a name, so
# "web-01.example.org" loses its "web-01".
NAME_CHARS = r"A-Za-z0-9_\-"


class Scrub:
    """Replaces names by their placeholders where they stand on their own in a value (case does not matter). As keys
    only the names in key_names (location ids): field names stay as they are, even where a host is named like one; the
    host list's own keys are renamed by the caller."""

    def __init__(self, names: dict[str, str], key_names: set[str] | frozenset[str] = frozenset()) -> None:
        self._names = {name.lower(): placeholder for name, placeholder in names.items()}
        self._keys = {name.lower() for name in key_names}
        longest_first = sorted(self._names, key=len, reverse=True)
        self._pattern = (
            re.compile(
                rf"(?<![{NAME_CHARS}])(?:{'|'.join(map(re.escape, longest_first))})(?![{NAME_CHARS}])", re.IGNORECASE
            )
            if longest_first
            else None
        )

    def text(self, value: str) -> str:
        if self._pattern is None:
            return value
        return self._pattern.sub(lambda m: self._names[m.group(0).lower()], value)

    def name(self, value: str) -> str:
        """The placeholder of a name, or the value itself."""
        return self._names.get(value.lower(), value)

    def key(self, key: Any) -> Any:
        return self.name(key) if isinstance(key, str) and key.lower() in self._keys else key

    def data(self, value: Any) -> Any:
        if isinstance(value, dict):
            out = {}
            for key, item in value.items():
                if key in REDACT_TEXT and isinstance(item, str) and item:
                    out[self.key(key)] = REDACTED
                elif key in NAME_FIELDS and isinstance(item, str) and item:
                    out[self.key(key)] = self._names.get(item.lower(), REDACTED)
                else:
                    out[self.key(key)] = self.data(item)
            return out
        if isinstance(value, list):
            return [self.data(item) for item in value]
        if isinstance(value, str):
            return self.text(value)
        return value


def _locations(hosts: dict[str, Any]) -> set[str]:
    """The ids of the hosts' pip/npm locations (pip-user-<user>, node-red-<user>, ...)."""
    ids = set()
    for host in hosts.values():
        packages = host.get("packages") if isinstance(host, dict) else None
        locations = packages.get("locations") if isinstance(packages, dict) else None
        for location in locations if isinstance(locations, list) else []:
            if isinstance(location, dict) and isinstance(location.get("id"), str) and location["id"]:
                ids.add(location["id"])
    return ids


def placeholders(hosts: dict[str, Any], hub: dict[str, Any], client: str) -> dict[str, str]:
    """host-1, host-2, ... for the hosts and those waiting for approval, location-1, ... for the pip/npm locations, in
    the order of their names; client for this Home Assistant's name at the hub."""
    waiting = hub.get("waiting") if isinstance(hub.get("waiting"), list) else []
    nodes = set(hosts) | {w["node"] for w in waiting if isinstance(w, dict) and isinstance(w.get("node"), str)}
    names = {node: f"host-{i}" for i, node in enumerate(sorted(n for n in nodes if n), start=1)}
    for i, location in enumerate(sorted(_locations(hosts) - set(names)), start=1):
        names[location] = f"location-{i}"
    if client:
        names[client] = "client"
    return names


def hub_address(url: Any) -> str:
    """The hub's address with hub.invalid as its host and without user or password: scheme, port and path stay."""
    if not isinstance(url, str) or not url:
        return REDACTED
    parsed = URL(url)
    return str(parsed.with_user(None).with_host(HUB_HOST)) if parsed.host else REDACTED


async def async_get_config_entry_diagnostics(hass: HomeAssistant, entry: VordrConfigEntry) -> dict[str, Any]:
    coordinator = entry.runtime_data
    data = coordinator.data
    hub = data.hub or {}
    scrub = Scrub(placeholders(data.hosts, hub, str(entry.data.get(CONF_NAME) or "")), _locations(data.hosts))
    entry_data = scrub.data(async_redact_data(dict(entry.data), REDACT))
    entry_data[CONF_URL] = hub_address(entry.data.get(CONF_URL))
    return {
        "entry": entry_data,
        "integration_version": VERSION,
        "fingerprint": coordinator.client.fingerprint,
        "connection": data.connection.value,
        "reason": data.reason,
        "synced": data.synced,
        "hub": scrub.data(hub),
        "hosts": {scrub.name(node): scrub.data(view) for node, view in data.hosts.items()},
    }
