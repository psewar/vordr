"""Texts the integration writes itself, from its translation files.

The hub sends codes and numbers only. What the frontend shows - entity states, attribute values, repairs - HA
translates into each reader's language from translations/*.json. What has to be put into words here, in the backend,
comes from the same files: event messages in every language the integration ships (an automation picks one), and
the update entity's latest_version and release summary in HA's system language, like entity names.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from homeassistant.core import HomeAssistant
from homeassistant.helpers.translation import async_get_translations

from .const import CODES, DOMAIN, REASONS

# Keys of the problem texts: repair issue "problem_<key>" (title, description) and the values of the problem sensor's
# next_step attribute. A reason that comes with a result code (check_failed, a failed run) uses the code, since what
# to do depends on it; a code this integration does not know yet gets "unknown".
# A host that does not allow reboots through Vördr gets "reboot by hand" instead of "reboot in the window or with the
# button" - there is no button for it.
MANUAL_REBOOT = {"reboot_pending": "reboot_pending_manual", "ok_reboot_required": "ok_reboot_required_manual"}
TEXT_KEYS: tuple[str, ...] = (
    *(r for r in REASONS if r != "check_failed"),
    *(c for c in CODES if c != "ok"),
    "unknown",
    *MANUAL_REBOOT.values(),
)

SUMMARY_MAX = 250  # HA cuts the release summary at 255 characters


def can_reboot(host: dict[str, Any]) -> bool:
    """Whether the hub offers a reboot for this host (agent can, local policy allows)."""
    return bool((host.get("capabilities") or {}).get("reboot"))


def problem_key(problem: dict[str, Any], reboot: bool = True) -> str:
    """The text key of a problem: its first reason, or the result code behind it; reboot: the host can be rebooted
    through Vördr."""
    key = str(problem.get("reason", ""))
    if key == "check_failed":
        key = str(problem.get("check_failed_code", ""))
    key = key if key in TEXT_KEYS else "unknown"
    return key if reboot else MANUAL_REBOOT.get(key, key)


PACKAGES_NAMED = 5  # packages a problem text names; "…" when there are more

# Reach of a vulnerable finding whose fix nothing on the host can take now (ADR-0129): it makes no problem, so the
# problem text does not name it. A finding without reach (a hub before ADR-0129) is named.
WAITING_REACH = frozenset({"held", "no_fix", "too_new"})


def package_list(host: dict[str, Any] | None, malicious: bool) -> str:
    """The packages behind a package problem, "chalk 5.6.1 (npm-global)": the malicious ones, or the vulnerable ones
    with something to do. "-" when there are none (the text names it anyway, a placeholder must not be missing)."""
    findings = ((host or {}).get("packages") or {}).get("findings") or []
    named = [
        f"{f.get('name')} {f.get('version')} ({f.get('location')})"
        for f in findings
        if bool(f.get("malicious")) == malicious and (malicious or f.get("reach") not in WAITING_REACH)
    ]
    if not named:
        return "-"
    more = ", …" if len(named) > PACKAGES_NAMED else ""
    return ", ".join(named[:PACKAGES_NAMED]) + more


def placeholders(
    node: str, problem: dict[str, Any], limits: dict[str, Any] | None, host: dict[str, Any] | None = None
) -> dict[str, str]:
    """Everything a problem text may name. All of them always: a text that names one we left out would not render.
    host: the host view, for the packages a package problem names."""
    lim = limits or {}
    code = problem.get("check_failed_code") if problem.get("reason") == "check_failed" else problem.get("reason")
    return {
        "node": node,
        "code": str(code or "-"),
        "offline_minutes": str(lim.get("offline_after_minutes", "?")),
        "stale_days": str(lim.get("stale_after_days", "?")),
        "reboot_days": str(lim.get("reboot_pending_after_days", "?")),
        "no_report_hours": str(lim.get("no_report_after_hours", "?")),
        "packages": package_list(host, problem.get("reason") == "malicious_package"),
    }


class Texts:
    """The translations of every shipped language, loaded once at setup."""

    def __init__(self, hass: HomeAssistant) -> None:
        self._hass = hass
        self._strings: dict[str, dict[str, str]] = {}  # language -> "component.vordr.<category>.<key>" -> text

    @property
    def languages(self) -> list[str]:
        return sorted(self._strings)

    async def async_load(self) -> None:
        folder = Path(__file__).with_name("translations")
        languages = await self._hass.async_add_executor_job(lambda: sorted(f.stem for f in folder.glob("*.json")))
        for lang in languages:
            strings: dict[str, str] = {}
            for category in ("issues", "texts"):
                strings.update(await async_get_translations(self._hass, lang, category, [DOMAIN]))
            self._strings[lang] = strings

    def system_language(self) -> str:
        """HA's language if shipped, else English - as HA picks the entity names, so that both match."""
        lang = self._hass.config.language
        return lang if lang in self._strings else "en"

    def _get(self, lang: str, key: str) -> str:
        return self._strings.get(lang, {}).get(f"component.{DOMAIN}.{key}", "")

    def messages(self, key: str, values: dict[str, str]) -> dict[str, str]:
        """The repair text of a problem key in every shipped language - what a push says."""
        return {lang: self._get(lang, f"issues.problem_{key}.description").format(**values) for lang in self.languages}

    def latest_version(self, pending: int, security: int) -> str:
        return self._get(self.system_language(), "texts.latest_version").format(pending=pending, security=security)

    def packages_installed(self, count: int) -> str:
        return self._get(self.system_language(), "texts.packages_installed").format(count=count)

    def packages_latest(self, outdated: int, ready: int) -> str:
        return self._get(self.system_language(), "texts.packages_latest").format(outdated=outdated, ready=ready)

    def release_summary(self, packages: list[dict[str, Any]], pending: int) -> str | None:
        """Pending packages "name old → new", within what HA shows; "…" when more are pending than listed. Security
        updates and major version jumps (the hub's `major`, e.g. 1.4.2 → 2.0.1) are marked, and so are fixes taken at
        the shorter minimum age (ADR-0111)."""
        lang = self.system_language()
        keys = ("security", "major", "waiting", "flagged", "fix")
        markers = {key: self._get(lang, f"texts.{key}") for key in keys}
        out = ""
        for pkg in packages:
            name, old, new = pkg.get("name", ""), pkg.get("from", ""), pkg.get("to", "")
            line = f"{name} {old} → {new}" if old else f"{name} → {new}"  # no old version: a new dependency
            marked = [text for key, text in markers.items() if pkg.get(key)]
            if marked:
                line += f" ({', '.join(marked)})"
            if len(out) + len(line) + 2 > SUMMARY_MAX:
                return out + ", …"
            out = f"{out}, {line}" if out else line
        if out and pending > len(packages):
            out += ", …"
        return out or None

    def images_installed(self, count: int) -> str:
        return self._get(self.system_language(), "texts.images_installed").format(count=count)

    def images_latest(self, outdated: int, ready: int) -> str:
        return self._get(self.system_language(), "texts.images_latest").format(outdated=outdated, ready=ready)

    def images_fixable(self, fixable: int) -> str:
        return self._get(self.system_language(), "texts.images_fixable").format(fixable=fixable)

    def images_vulnerable(self, findings: list[dict[str, Any]], newer_known: bool = False) -> str:
        """Known vulnerabilities inside images (ADR-0120): "stack/db 4 (2 fixable, 1 without fix)", the fixable ones
        first as the hub sends them. Images that were not matched are left out here; their code is in the
        attributes. With newer_known (a hub that says per image whether a newer image is there, ADR-0130) the fixable
        count says which: "2 fixable, newer image ready" or "2 fixed in the distribution, no newer image yet"."""
        lang = self.system_language()
        out = self._get(lang, "texts.images_vulnerable")
        for i, f in enumerate(findings):
            name = str(f.get("image", "")).removeprefix("docker.io/").removeprefix("library/")
            counts = []
            if f.get("fixable"):
                key = "vuln_fixable"
                if newer_known:
                    key = "vuln_fixable_newer" if f.get("newer") else "vuln_fixable_waiting"
                counts.append(self._get(lang, f"texts.{key}").format(n=f["fixable"]))
            if f.get("unfixed"):
                counts.append(self._get(lang, "texts.vuln_unfixed").format(n=f["unfixed"]))
            line = f"{name} {f.get('tag', '')} ({', '.join(counts)})"
            sep = " " if i == 0 else ", "
            if len(out) + len(sep) + len(line) > SUMMARY_MAX - 3:
                return out + ", …"
            out += sep + line
        return out

    def images_summary(self, updates: list[dict[str, Any]], by_button: bool = False) -> str:
        """Outdated container images (ADR-0113): "stack/db 3 (rebuilt)", "stack/db 3 → 4 (new major version)",
        "app 1.2 → 1.3 (too new)", and beside a new major version the smaller step (ADR-0131): "stack/web 2.2.0 →
        3.1.0 (new major version, or 2.4.0 in the same major version)". Led by the note that Vördr only reports them,
        or - where the host allows Compose services (ADR-0119) - that the install button takes their rebuilds and newer
        tags stay manual. Images of Docker Hub's official library are named without their registry."""
        lang = self.system_language()
        lead = "images_by_button" if by_button else "images_report_only"
        text = {key: self._get(lang, f"texts.{key}") for key in ("rebuilt", "major", "waiting", "same_major")}
        text["images_report_only"] = self._get(lang, f"texts.{lead}")
        lines: list[str] = []
        for u in updates:
            name = str(u.get("image", "")).removeprefix("docker.io/").removeprefix("library/")
            tag = u.get("tag", "")
            if u.get("rebuilt"):
                marks = [text["rebuilt"]] + ([] if u.get("rebuilt_ready") else [text["waiting"]])
                lines.append(f"{name} {tag} ({', '.join(marks)})")
            if u.get("latest"):
                to = u.get("candidate") or u.get("latest")
                marks = ([text["major"]] if u.get("major") else []) + ([] if u.get("candidate") else [text["waiting"]])
                if u.get("same_major"):
                    marks.append(text["same_major"].format(tag=u["same_major"]))
                lines.append(f"{name} {tag} → {to}" + (f" ({', '.join(marks)})" if marks else ""))
        out = text["images_report_only"]
        for i, line in enumerate(lines):
            sep = " " if i == 0 else ", "
            if len(out) + len(sep) + len(line) > SUMMARY_MAX - 3:
                return out + ", …"
            out += sep + line
        return out
