"""Texts: every language complete and consistent, problem keys, and the language each surface uses."""

from __future__ import annotations

import json
from pathlib import Path
import re
from typing import Any

from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr.const import CODES, DOMAIN, REASONS
from custom_components.vordr.texts import TEXT_KEYS, Texts, placeholders, problem_key

from .conftest import FakeHub, host_view, wait_until

BASE = Path(__file__).parents[2] / "custom_components" / "vordr"
LANGS = {f.stem: json.loads(f.read_text(encoding="utf-8")) for f in (BASE / "translations").glob("*.json")}


def keys(obj: Any, prefix: str = "") -> set[str]:
    if not isinstance(obj, dict):
        return {prefix}
    return set().union(*(keys(v, f"{prefix}.{k}") for k, v in obj.items()))


def test_strings_json_is_the_english_file() -> None:
    assert (BASE / "strings.json").read_text(encoding="utf-8") == (BASE / "translations" / "en.json").read_text(
        encoding="utf-8"
    )


def test_every_language_has_every_text() -> None:
    assert set(LANGS) >= {"de", "en"}
    for lang, data in LANGS.items():
        assert keys(data) == keys(LANGS["en"]), lang


# What hassfest, Home Assistant's own check, allows in strings.json and translations/en.json
# (script/hassfest/translations.py): only the sections Home Assistant knows, and no text that looks like an HTML tag.
# Checked here for every language, so a finding shows before the HACS default list's checks run (ADR-0145).
HA_SECTIONS = {
    "title", "config", "config_subentries", "options", "preview_features", "selector", "device_automation",
    "system_health", "config_panel", "application_credentials", "issues", "entity_component", "device", "entity",
    "exceptions", "services", "conditions", "triggers", "conversation", "common",
}  # fmt: skip
HTML_LIKE = re.compile(r"<[a-z].*?>", re.IGNORECASE)


def texts(obj: Any, prefix: str = "") -> list[tuple[str, str]]:
    if isinstance(obj, dict):
        return [t for k, v in obj.items() for t in texts(v, f"{prefix}.{k}" if prefix else k)]
    return [(prefix, obj)] if isinstance(obj, str) else []


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_texts_pass_home_assistants_checks(lang: str) -> None:
    data = LANGS[lang]
    assert set(data) <= HA_SECTIONS, sorted(set(data) - HA_SECTIONS)
    assert [path for path, text in texts(data) if HTML_LIKE.search(text)] == []
    assert all(re.fullmatch(r"[a-z0-9_]+", key) for key in data["common"]), sorted(data["common"])


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_problem_texts_complete_and_consistent(lang: str) -> None:
    data = LANGS[lang]
    attrs = data["entity"]["binary_sensor"]["problem"]["state_attributes"]
    run_states = data["entity"]["sensor"]["last_run"]["state"]
    codes = [c for c in CODES if c != "ok"]
    assert set(attrs["next_step"]["state"]) == set(TEXT_KEYS)
    assert set(attrs["reason"]["state"]) == {*REASONS, *codes}
    assert set(attrs["check_failed_code"]["state"]) == set(codes)
    values = placeholders("n1", {"reason": "x"}, {"stale_after_days": 7})
    for key in TEXT_KEYS:
        issue = data["issues"][f"problem_{key}"]
        description = issue["description"].format(**values)  # renders with what the integration always passes
        assert attrs["next_step"]["state"][key] in description, (key, description)
        assert "{node}" in issue["title"]
    for code in codes:  # one wording per code, wherever it shows
        assert attrs["reason"]["state"][code] == run_states[code] == attrs["check_failed_code"]["state"][code]
        assert data["issues"][f"problem_{code}"]["description"].startswith(run_states[code] + ".")
    assert "{pending}" in data["common"]["latest_version"] and "{security}" in data["common"]["latest_version"]


def test_problem_key() -> None:
    assert problem_key({"reason": "stale"}) == "stale"
    assert problem_key({"reason": "check_failed", "check_failed_code": "timeout"}) == "timeout"
    assert problem_key({"reason": "check_failed"}) == "unknown"
    assert problem_key({"reason": "failed_verification"}) == "failed_verification"
    assert problem_key({"reason": "some_future_code"}) == "unknown"
    assert problem_key({"reason": "ok"}) == "unknown"


async def test_release_summary_fits(hass: HomeAssistant) -> None:
    texts = Texts(hass)
    await texts.async_load()
    many = [{"name": f"package-{i:02d}", "from": "1.0", "to": "1.1", "security": i < 2} for i in range(30)]
    summary = texts.release_summary(many, 30)
    assert summary is not None and len(summary) <= 255 and summary.endswith(", …")
    assert summary.startswith("package-00 1.0 → 1.1 (security), package-01 1.0 → 1.1 (security), package-02 1.0")
    # All listed, but more pending than the hub sent: say so.
    assert texts.release_summary(many[:2], 5) == "package-00 1.0 → 1.1 (security), package-01 1.0 → 1.1 (security), …"
    assert texts.release_summary([{"name": "dep", "to": "2"}], 1) == "dep → 2"  # new dependency, no old version
    # A major jump the hub marks (e.g. from winget), also together with security.
    app = {"name": "Contoso.ExampleTool.LTS", "from": "1.4.2", "to": "2.0.1", "major": True}
    assert texts.release_summary([app], 1) == "Contoso.ExampleTool.LTS 1.4.2 → 2.0.1 (new major version)"
    both = {"name": "x", "from": "1", "to": "2", "security": True, "major": True}
    assert texts.release_summary([both], 1) == "x 1 → 2 (security, new major version)"


@pytest.mark.parametrize(
    ("lang", "package", "packages", "image", "images"),
    [("en", "package", "packages", "image", "images"), ("de", "Paket", "Pakete", "Image", "Images")],
)
async def test_counts_name_one_in_the_singular(
    hass: HomeAssistant, lang: str, package: str, packages: str, image: str, images: str
) -> None:
    """Exactly one takes the singular text ("1 image", not "1 images"); every other count, also 0, the plural."""
    await hass.config.async_update(language=lang)
    texts = Texts(hass)
    await texts.async_load()
    assert [texts.packages_installed(n) for n in (0, 1, 2)] == [f"0 {packages}", f"1 {package}", f"2 {packages}"]
    assert [texts.images_installed(n) for n in (0, 1, 2)] == [f"0 {images}", f"1 {image}", f"2 {images}"]


async def test_system_language_words_the_backend(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """German HA: device name and update texts in German; events still carry every language."""
    await hass.config.async_update(language="de")
    fake_hub.hosts["golf"] = host_view("golf")
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()
    upd = hass.states.get("update.vordr_golf_system")
    assert upd.attributes["latest_version"] == "2 ausstehend (1 Sicherheit)"
    assert upd.attributes["release_summary"] == "a 1 → 2 (Sicherheit), b 1 → 2"
    hub = dr.async_get(hass).async_get_device_by_identifier((DOMAIN, "hub"), entry.entry_id)
    assert hub is not None and hub.name == "Vördr-Leitstelle"
    assert set(entry.runtime_data.texts.messages("stale", placeholders("n", {}, None))) == set(LANGS)
