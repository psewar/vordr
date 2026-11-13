"""The update entity "Systempakete" of a host."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.update import UpdateEntity, UpdateEntityFeature
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers import config_validation as cv, entity_platform
import voluptuous as vol

from .const import DOMAIN, IMAGE_UPDATE_KEYS, PHASES
from .coordinator import VordrCoordinator, valid_reason
from .entity import HostEntities, VordrHostEntity, track_host_entities
from .texts import SUMMARY_MAX

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

    from . import VordrConfigEntry

PLATFORM = "update"
SERVICE_UPDATE_PACKAGE = "update_package"
SERVICE_INSTALL_OUTSIDE_WINDOW = "install_outside_window"
SERVICE_REFRESH_DEPENDENCIES = "refresh_dependencies"
SERVICE_UPDATE_IMAGE = "update_image"


async def async_setup_entry(
    hass: HomeAssistant, entry: VordrConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    coordinator = entry.runtime_data

    def wanted(node: str, host: dict[str, Any]) -> HostEntities:
        factories: dict[str, Any] = {}
        if host.get("agent_release"):  # the hub hands out agent releases
            factories["agent"] = lambda: HostAgent(coordinator, node)
        # pip/npm: one entity per location the host lets Home Assistant update (design 11.4)
        for loc in (host.get("capabilities") or {}).get("package_updates") or []:
            factories[f"packages_{loc}"] = lambda loc=loc: HostPackages(coordinator, node, loc)
        # container images (ADR-0113), for hosts that report them; rebuilds installed by button where allowed (ADR-0119)
        if isinstance(host.get("containers"), dict):
            factories["containers"] = lambda: HostContainers(coordinator, node)
        if host.get("last_report") is None:
            return factories, {"system"}
        if host.get("system") is not None:
            factories["system"] = lambda: HostSystem(coordinator, node)
        return factories, set()

    track_host_entities(coordinator, entry, PLATFORM, wanted, async_add_entities)
    platform = entity_platform.async_get_current_platform()
    # A reason asks for it outside the host's window (ADR-0087); without one the window holds.
    platform.async_register_entity_service(
        SERVICE_UPDATE_PACKAGE,
        {vol.Required("package"): cv.string, vol.Optional("reason"): valid_reason},
        "async_update_package",
    )
    platform.async_register_entity_service(
        SERVICE_INSTALL_OUTSIDE_WINDOW, {vol.Required("reason"): valid_reason}, "async_install_outside_window"
    )
    platform.async_register_entity_service(
        SERVICE_REFRESH_DEPENDENCIES, {vol.Optional("reason"): valid_reason}, "async_refresh_dependencies"
    )
    platform.async_register_entity_service(
        SERVICE_UPDATE_IMAGE,
        {vol.Required("service"): cv.string, vol.Optional("reason"): valid_reason},
        "async_update_image",
    )


class HostSystem(VordrHostEntity, UpdateEntity):
    """A package set has no single version: installed_version names the system, latest_version the pending set
    (design 8.3), worded here in HA's system language. An unknown latest version (failed check) is shown as unknown,
    not as up to date. The entity is on only when the install button would install something (ADR-0106): what the
    host's policy keeps out stays in the counts and the summary."""

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "system", PLATFORM)

    @property
    def _system(self) -> dict[str, Any]:
        return self.host.get("system") or {}

    @property
    def installed_version(self) -> str | None:
        return self._system.get("installed_version")

    @property
    def latest_version(self) -> str | None:
        pending, security = self.host.get("pending"), self.host.get("security")
        if not isinstance(pending, int):
            return None  # the check failed
        # installable: the hub's count of what the install button takes; a hub before ADR-0106 does not send it
        if pending == 0 or self.host.get("installable") == 0:
            return self.installed_version
        return self.coordinator.texts.latest_version(pending, security if isinstance(security, int) else 0)

    @property
    def release_summary(self) -> str | None:
        pending = self.host.get("pending")
        if not isinstance(pending, int) or pending == 0:
            return None
        return self.coordinator.texts.release_summary(self._system.get("packages") or [], pending)

    @property
    def supported_features(self) -> UpdateEntityFeature:
        caps = self.host.get("capabilities") or {}
        if caps.get("install"):
            return UpdateEntityFeature.INSTALL | UpdateEntityFeature.PROGRESS
        return UpdateEntityFeature(0)

    @property
    def in_progress(self) -> bool:
        run = self.host.get("last_run") or {}
        mine = not _other_entity_scope(str(run.get("scope", "")))
        return run.get("verb") == "install" and mine and run.get("state") in PHASES

    @property
    def update_percentage(self) -> int | None:
        """Percent of the whole run (prechecks, download, installation, verification) as the agent reports it."""
        progress = (self.host.get("last_run") or {}).get("progress")
        return progress if self.in_progress and isinstance(progress, int) else None

    def version_is_newer(self, latest_version: str, installed_version: str) -> bool:
        # Free text on both sides (system name, "n pending"): only equality means anything.
        return latest_version != installed_version

    async def async_install(self, version: str | None, backup: bool, **kwargs: Any) -> None:
        code = await self.coordinator.command(self.node, "install")
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    async def async_install_outside_window(self, reason: str) -> None:
        """Service vordr.install_outside_window: what the install button does, also outside the window."""
        await self.coordinator.command_outside_window(self.node, "install", reason)


def _ready(update: dict[str, Any]) -> bool:
    """An install would take this package now: a fix at the shorter minimum age (ADR-0111), or a candidate OSV does
    not flag."""
    return bool(update.get("fix")) or bool(update.get("candidate") and not update.get("candidate_flagged"))


def _image_scope(scope: str) -> bool:
    """A run that updates container images (ADR-0119): one Compose service, or all the host allows."""
    return scope == "images" or scope.startswith("image:")


def _other_entity_scope(scope: str) -> bool:
    """A run that belongs to another entity than the system packages: pip/npm, dependencies, container images."""
    return scope.startswith(("pkg:", "deps:")) or _image_scope(scope)


class HostPackages(VordrHostEntity, UpdateEntity):
    """The pip/npm packages of one location (design 11.4). Installing updates every package there to its candidate -
    public for at least the host's minimum age, the whole plan checked against OSV by the hub; the service
    vordr.update_package does it for one. What waits (too new) or is flagged is named in the summary; with nothing
    ready the entity is off (ADR-0106)."""

    def __init__(self, coordinator: VordrCoordinator, node: str, location: str) -> None:
        super().__init__(coordinator, node, f"packages_{location}", PLATFORM)
        self._attr_translation_key = "packages"
        self._attr_translation_placeholders = {"location": location}
        self._location = location

    def _loc(self) -> dict[str, Any]:
        for loc in (self.host.get("packages") or {}).get("locations") or []:
            if loc.get("id") == self._location:
                return loc
        return {}

    def _updates(self) -> list[dict[str, Any]]:
        updates = (self.host.get("packages") or {}).get("updates") or []
        return [u for u in updates if u.get("location") == self._location]

    @property
    def installed_version(self) -> str | None:
        count = self._loc().get("packages")
        return self.coordinator.texts.packages_installed(count) if isinstance(count, int) else None

    @property
    def latest_version(self) -> str | None:
        outdated = self._loc().get("outdated") or 0
        ready = sum(1 for u in self._updates() if _ready(u))
        if not outdated or not ready:  # nothing an install would take now: all too new or flagged
            return self.installed_version
        return self.coordinator.texts.packages_latest(outdated, ready)

    @property
    def release_summary(self) -> str | None:
        lines = [
            {
                "name": u.get("name"),
                "from": u.get("installed"),
                # a fix at the shorter minimum age goes before the candidate (ADR-0111)
                "to": u.get("fix") or u.get("candidate") or u.get("latest"),
                "major": u.get("major"),
                "waiting": not (u.get("fix") or u.get("candidate")),
                "flagged": bool(u.get("candidate_flagged")) and not u.get("fix"),
                "fix": bool(u.get("fix")),
            }
            for u in self._updates()
        ]
        lines.sort(key=lambda line: line["waiting"] or line["flagged"])  # what an update takes now first
        return self.coordinator.texts.release_summary(lines, self._loc().get("outdated") or 0)

    @property
    def supported_features(self) -> UpdateEntityFeature:
        caps = self.host.get("capabilities") or {}
        if self._location in (caps.get("package_updates") or []):
            return UpdateEntityFeature.INSTALL | UpdateEntityFeature.PROGRESS
        return UpdateEntityFeature(0)

    @property
    def in_progress(self) -> bool:
        run = self.host.get("last_run") or {}
        scope = str(run.get("scope", ""))
        loc = self._location
        mine = scope in (f"pkg:{loc}", f"deps:{loc}") or scope.startswith(f"pkg:{loc}/")
        return run.get("verb") == "install" and mine and run.get("state") in PHASES

    @property
    def update_percentage(self) -> int | None:
        progress = (self.host.get("last_run") or {}).get("progress")
        return progress if self.in_progress and isinstance(progress, int) else None

    def version_is_newer(self, latest_version: str, installed_version: str) -> bool:
        return latest_version != installed_version  # free text on both sides

    async def async_install(self, version: str | None, backup: bool, **kwargs: Any) -> None:
        code = await self.coordinator.command(self.node, f"install_pkg:{self._location}")
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    async def async_update_package(self, package: str, reason: str = "") -> None:
        """Service vordr.update_package: one package of this location; the hub and the agent check the name."""
        command = f"install_pkg:{self._location}/{package}"
        if reason:
            await self.coordinator.command_outside_window(self.node, command, reason)
            return
        code = await self.coordinator.command(self.node, command)
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    async def async_install_outside_window(self, reason: str) -> None:
        """Service vordr.install_outside_window: every package of this location, also outside the window."""
        await self.coordinator.command_outside_window(self.node, f"install_pkg:{self._location}", reason)

    async def async_refresh_dependencies(self, reason: str = "") -> None:
        """Service vordr.refresh_dependencies (ADR-0104): the dependencies of this location that have findings; the hub
        names them, the agent moves each within the ranges its dependents allow."""
        caps = self.host.get("capabilities") or {}
        if self._location not in (caps.get("dependency_refresh") or []):
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="refresh_dependencies_unsupported",
                translation_placeholders={"node": self.node, "location": self._location},
            )
        command = f"refresh_deps:{self._location}"
        if reason:
            await self.coordinator.command_outside_window(self.node, command, reason)
            return
        code = await self.coordinator.command(self.node, command)
        if code != "ok":
            raise self.coordinator.refused(self.node, code)


class HostContainers(VordrHostEntity, UpdateEntity):
    """The images of the host's running containers (ADR-0113): rebuilt under their tag, or with a newer tag of the
    same form, and known vulnerabilities inside them (ADR-0120). The summary says per image whether a newer image is
    ready (ADR-0130).

    Where the host's agent.yaml allows Compose services (ADR-0119), the install button moves every allowed service
    with a rebuild old enough to it, and vordr.update_image does it for one. The entity is on only while that button
    would take something (ADR-0150), as the other update entities (ADR-0106). A newer tag needs the Compose file
    changed, and a service the host does not allow stays the operator's: both leave the entity off and are counted by
    the sensor "Outdated images", as are hosts without allowed services, whose entity only reports. Known
    vulnerabilities alone never turn it on: an advisory fixed in the distribution waits for its publisher to rebuild
    the image."""

    _unrecorded_attributes = frozenset({"updates", "vulnerable", "compare_failed"})  # can be long (ADR-0101)

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "containers", PLATFORM)

    def _view(self) -> dict[str, Any]:
        view = self.host.get("containers")
        return view if isinstance(view, dict) else {}

    @property
    def installed_version(self) -> str | None:
        count = self._view().get("images")
        return self.coordinator.texts.images_installed(count) if isinstance(count, int) else None

    @property
    def latest_version(self) -> str | None:
        view = self._view()
        outdated, ready = view.get("outdated"), view.get("ready")
        if not (self._button() and isinstance(outdated, int) and isinstance(ready, int) and ready):
            # nothing the button would take now (ADR-0150): what stays manual is in the sensor "Outdated images";
            # known vulnerabilities alone neither, until a newer image is there (ADR-0130)
            return self.installed_version
        parts = [self.coordinator.texts.images_latest(outdated, ready)]
        fixable_newer = view.get("fixable_newer")  # a hub before ADR-0130 does not tell: then not named here
        if isinstance(fixable_newer, int) and fixable_newer:
            parts.append(self.coordinator.texts.images_fixable(fixable_newer))
        return ", ".join(parts)

    @property
    def release_summary(self) -> str | None:
        view = self._view()
        updates = view.get("updates") or []
        findings = [f for f in view.get("vulnerable") or [] if f.get("fixable") or f.get("unfixed")]
        parts = []
        if updates:
            parts.append(self.coordinator.texts.images_summary(updates, by_button=bool(self._services())))
        if findings:
            # "newer" per image only from a hub that reports fixable_newer (ADR-0130); else the plain counts
            newer_known = isinstance(view.get("fixable_newer"), int)
            parts.append(self.coordinator.texts.images_vulnerable(findings, newer_known=newer_known))
        if not parts:
            return None
        text = " ".join(parts)
        return text if len(text) <= SUMMARY_MAX else text[: SUMMARY_MAX - 1] + "…"

    def _services(self) -> list[str]:
        """The Compose services the host lets Home Assistant update (ADR-0119)."""
        return list((self.host.get("capabilities") or {}).get("image_updates") or [])

    def _button(self) -> bool:
        """Whether the install button would take something: a rebuild old enough of an allowed service, as the hub
        counts it (installable). A hub before "installable" does not say; then the allowed services decide."""
        installable = self._view().get("installable")
        return bool(self._services()) and (not isinstance(installable, int) or installable > 0)

    @property
    def supported_features(self) -> UpdateEntityFeature:
        if self._button():
            return UpdateEntityFeature.INSTALL | UpdateEntityFeature.PROGRESS
        return UpdateEntityFeature(0)  # only reported, or nothing the button would take now

    @property
    def in_progress(self) -> bool:
        run = self.host.get("last_run") or {}
        mine = _image_scope(str(run.get("scope", "")))
        return run.get("verb") == "install" and mine and run.get("state") in PHASES

    @property
    def update_percentage(self) -> int | None:
        progress = (self.host.get("last_run") or {}).get("progress")
        return progress if self.in_progress and isinstance(progress, int) else None

    async def async_install(self, version: str | None, backup: bool, **kwargs: Any) -> None:
        code = await self.coordinator.command(self.node, "install_images")
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    async def async_install_outside_window(self, reason: str) -> None:
        """Service vordr.install_outside_window: every allowed service with a rebuild, also outside the window."""
        await self.coordinator.command_outside_window(self.node, "install_images", reason)

    async def async_update_image(self, service: str, reason: str = "") -> None:
        """Service vordr.update_image: one Compose service, "<project>/<service>"; the hub and the agent check it."""
        if service not in self._services():
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="update_image_not_allowed",
                translation_placeholders={"node": self.node, "service": service},
            )
        command = f"install_image:{service}"
        if reason:
            await self.coordinator.command_outside_window(self.node, command, reason)
            return
        code = await self.coordinator.command(self.node, command)
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        view = self._view()
        keys = IMAGE_UPDATE_KEYS
        attrs: dict[str, Any] = {"updates": [{k: u[k] for k in keys if u.get(k)} for u in view.get("updates") or []]}
        for key in ("code", "local", "not_in_registry", "outdated_failed", "compared_at", "min_age_days"):
            if view.get(key):
                attrs[key] = view[key]
        if view.get("compare_failed"):  # ADR-0126: the images behind outdated_failed, each with why
            ckeys = ("image", "tag", "containers", "code")
            attrs["compare_failed"] = [{k: f[k] for k in ckeys if f.get(k)} for f in view["compare_failed"]]
        if "vulnerable" in view or isinstance(view.get("fixable"), int):  # ADR-0120: the hub matches image contents
            # kernel: the kernel's own advisories, left out of the counts (ADR-0122)
            # newer: a newer image is there to install (ADR-0130)
            fkeys = ("image", "tag", "containers", "release", "fixable", "unfixed", "ids", "kernel", "newer", "code")
            attrs["vulnerable"] = [{k: f[k] for k in fkeys if f.get(k)} for f in view.get("vulnerable") or []]
            for key in ("fixable", "unfixed", "fixable_newer"):
                if isinstance(view.get(key), int):
                    attrs[key] = view[key]
        return attrs

    def version_is_newer(self, latest_version: str, installed_version: str) -> bool:
        return latest_version != installed_version  # free text on both sides


class HostAgent(VordrHostEntity, UpdateEntity):
    """The Vördr agent of a host against the release the hub hands out. The hub rolls a release out by itself
    (canaries first); installing here asks for it now. Only the operator's signed release can be installed."""

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "agent", PLATFORM)

    @property
    def installed_version(self) -> str | None:
        return (self.host.get("device") or {}).get("agent_version") or None

    @property
    def latest_version(self) -> str | None:
        if self.host.get("agent_release_no_binary"):  # the release has no binary for this host (ADR-0102)
            return self.installed_version
        return self.host.get("agent_release") or self.installed_version

    @property
    def supported_features(self) -> UpdateEntityFeature:
        caps = self.host.get("capabilities") or {}
        if caps.get("agent_update"):
            return UpdateEntityFeature.INSTALL | UpdateEntityFeature.PROGRESS
        return UpdateEntityFeature(0)

    @property
    def in_progress(self) -> bool:
        run = self.host.get("last_run") or {}
        return run.get("verb") == "agent_update" and run.get("state") in PHASES

    @property
    def update_percentage(self) -> int | None:
        progress = (self.host.get("last_run") or {}).get("progress")
        return progress if self.in_progress and isinstance(progress, int) else None

    def version_is_newer(self, latest_version: str, installed_version: str) -> bool:
        # The release is what the operator signed - also an older one, to go back: any other version counts.
        return latest_version != installed_version

    async def async_install(self, version: str | None, backup: bool, **kwargs: Any) -> None:
        code = await self.coordinator.command(self.node, "agent_update")
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    async def async_install_outside_window(self, reason: str) -> None:
        """An agent update has no window: the install button does it at any time."""
        raise HomeAssistantError(
            translation_domain=DOMAIN,
            translation_key="outside_window_no_window",
            translation_placeholders={"node": self.node},
        )
