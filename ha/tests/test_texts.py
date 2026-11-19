"""Texts: every language complete and consistent, problem keys, and the language each surface uses."""

from __future__ import annotations

from datetime import timedelta
import json
from pathlib import Path
import re
import string
from typing import Any

from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr
from homeassistant.helpers.selector import SelectSelector
from homeassistant.util import dt as dt_util
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr import agent_install as ai, ai_explain as ax, ha_updates as hu, ha_updates_installer as hi
from custom_components.vordr.const import CODES, DOMAIN, REASONS
from custom_components.vordr.coordinator import BACKUP_REFUSALS, reboot_held, refusal, waiting_hosts
from custom_components.vordr.repairs import (
    APPROVE_REFUSALS,
    CONF_FINGERPRINT,
    FORM_DIFFERS,
    FORM_INVALID,
    InstallAgainFlow,
    RebootFlow,
    approve_refusal,
)
from custom_components.vordr.texts import TEXT_KEYS, Texts, placeholders, problem_key, reboot_key, reboot_names

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
    # A repair has a title and exactly one of description and fix_flow (gen_issues_schema): a fixable repair shows
    # its text in the fix dialog, not as a description.
    for key, issue in data["issues"].items():
        assert "title" in issue and set(issue) <= {"title", "short_title", "description", "fix_flow"}, (key, issue)
        assert ("description" in issue) != ("fix_flow" in issue), key
    # No text starts or ends with a space, and no placeholder is in single quotes (translation_value_validator).
    assert [path for path, text in texts(data) if text != text.strip() or re.search(r"'\{\w+\}'", text)] == []


# The parts of a flow's texts (gen_data_entry_schema); a subentry flow also needs the name of its kind and the label of
# its button, and has no title of its own (subentry_flow=True, flow_title=REMOVED).
FLOW_KEYS = {"flow_title", "step", "error", "abort", "progress", "create_entry"}
STEP_KEYS = {"title", "description", "data", "data_description", "menu_options", "menu_option_descriptions", "submit"}


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_flow_texts_follow_home_assistants_schema(lang: str) -> None:
    """The config flow, the options flow and every subentry flow ("Add host", ADR-0185) as hassfest checks them: only
    known parts, each step only known keys, and a data_description only for a field that has a label."""
    data = LANGS[lang]
    flows = {"config": data["config"], "options": data["options"]}
    for kind, flow in data["config_subentries"].items():
        assert {"entry_type", "initiate_flow", "step"} <= set(flow) <= FLOW_KEYS | {"entry_type", "initiate_flow"}, kind
        assert flow["entry_type"] and flow["initiate_flow"]["user"], kind
        flows[f"config_subentries.{kind}"] = flow
    assert set(data["config_subentries"]) == {"host"}
    for name, flow in flows.items():
        assert set(flow) - {"title", "entry_type", "initiate_flow"} <= FLOW_KEYS, name
        for step_id, step in flow["step"].items():
            assert set(step) <= STEP_KEYS, (name, step_id)
            assert set(step.get("data_description", {})) <= set(step.get("data", {})), (name, step_id)


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


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_repair_refusals_have_their_texts(lang: str) -> None:
    """Each code a repair flow says why for has its text in the flow's dialog, under the key the buttons use, and the
    text needs no placeholder the refusal does not fill; any other code shows as command_refused. No refusal text is
    left unused."""
    for flow, issue in ((RebootFlow, "reboot_required"), (InstallAgainFlow, "install_again")):
        abort = LANGS[lang]["issues"][issue]["fix_flow"]["abort"]
        used = set()
        for code in (*flow.refusals, "some_future_code"):
            key, names = refusal("n", None, code)
            fields = {field for _, field, _, _ in string.Formatter().parse(abort[key]) if field}
            assert "node" in fields and fields <= set(names), (issue, code, fields)
            used.add(key)
        assert used == {k for k in abort if k.startswith("command_refused")}, issue


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_backup_refusals_have_their_texts(lang: str) -> None:
    """Each refusal of the hub's backup check has its error text for the buttons, with no placeholder the refusal does
    not fill; the hypervisor's states each have their own (ADR-0199)."""
    exceptions = LANGS[lang]["exceptions"]
    for code in BACKUP_REFUSALS:
        key, names = refusal("n", None, code)
        fields = {field for _, field, _, _ in string.Formatter().parse(exceptions[key]["message"]) if field}
        assert "node" in fields and fields <= set(names), (code, fields)
        if code.startswith("backup_host_"):
            assert "hypervisor" in fields, code


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_reboot_window_texts(lang: str) -> None:
    """ADR-0187: the reboot button's error and the reboot repair's abort have a text for each key reboot_held gives,
    rendered with exactly the placeholders it fills: the host and the time. Only the text for window_only says that a
    reason does not help; the other points to the action with a reason."""
    data = LANGS[lang]
    later = (dt_util.utcnow() + timedelta(hours=2)).isoformat()
    window = {"days": ["mon"], "start": "06:00", "end": "16:00", "tz": "Europe/Paris"}
    seen = set()
    for only in (True, False):
        host = {"next_reboot": later, "reboot_window": {**window, "window_only": only}}
        held = reboot_held("n", host)
        assert held is not None
        key, names = held
        seen.add(key)
        assert (reboot_held("n", host, reason=True) is None) is not only
        for text in (data["exceptions"][key]["message"], data["issues"]["reboot_required"]["fix_flow"]["abort"][key]):
            assert {f for _, f, _, _ in string.Formatter().parse(text) if f} == set(names) == {"node", "next"}
            assert ("reboot.window_only" in text) is only
    assert seen == {"reboot_window_only", "reboot_window_closed"}
    assert reboot_held("n", {}) is None and reboot_held("n", None) is None


REBOOT_FIXABLE = ("reboot_required", "reboot_required_kernel", "reboot_required_libraries")
REBOOT_MANUAL = (
    "reboot_required_manual",
    "reboot_required_manual_kernel",
    "reboot_required_manual_libraries",
    "reboot_required_manual_windows_update",
)


def fields(text: str) -> set[str]:
    return {f for _, f, _, _ in string.Formatter().parse(text) if f}


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_reboot_texts_per_reason(lang: str) -> None:
    """ADR-0201: each reboot text has the general one's title; a fixable one has the general one's refusals in its
    dialog and names only what the dialog fills (the host and reboot_names), a manual one also since. Each names what
    its reason is about. These are all the reboot texts there are."""
    issues = LANGS[lang]["issues"]
    shown = {"node", *reboot_names(None)}
    for key in REBOOT_FIXABLE:
        flow = issues[key]["fix_flow"]
        assert issues[key]["title"] == issues["reboot_required"]["title"], key
        assert flow["abort"] == issues["reboot_required"]["fix_flow"]["abort"], key
        assert fields(flow["step"]["confirm"]["description"]) <= shown, key
    for key in REBOOT_MANUAL:
        assert issues[key]["title"] == issues["reboot_required_manual"]["title"], key
        assert fields(issues[key]["description"]) <= shown | {"since"}, key
    for key in (*REBOOT_FIXABLE, *REBOOT_MANUAL):
        text = issues[key].get("description") or issues[key]["fix_flow"]["step"]["confirm"]["description"]
        assert "node" in fields(text), key
        assert ("services" in fields(text)) is key.endswith("_libraries"), key
        assert ({"kernel_running", "kernel_newest"} <= fields(text)) is key.endswith("_kernel"), key
        assert ("Windows Update" in text) is key.endswith("_windows_update"), key
    assert {k for k in issues if k.startswith("reboot_required")} == {*REBOOT_FIXABLE, *REBOOT_MANUAL}


def test_reboot_key_follows_the_reason() -> None:
    """ADR-0201: the reason's own text only where the view has what it names; the general one otherwise, also for a
    hub or agent before ADR-0201. Fixable follows the capability; Windows Update has only a text by hand."""
    can, cannot = {"reboot": True}, {"reboot": False}
    lib = {"required": True, "reason": "libraries", "services": ["cron.service", "systemd"]}
    kernel = {
        "required": True,
        "reason": "kernel",
        "kernel_running": "6.1.0-35-amd64",
        "kernel_newest": "6.1.0-37-amd64",
    }
    for caps, reboot, key in (
        (can, lib, "reboot_required_libraries"),
        (cannot, lib, "reboot_required_manual_libraries"),
        (can, {**lib, "services": []}, "reboot_required"),
        (can, kernel, "reboot_required_kernel"),
        (cannot, kernel, "reboot_required_manual_kernel"),
        (can, {**kernel, "kernel_newest": "6.1.0-35-amd64"}, "reboot_required"),  # the file says kernel, kernels agree
        (can, {"required": True, "reason": "kernel"}, "reboot_required"),  # a container: no kernels
        (cannot, {"required": True, "reason": "windows_update"}, "reboot_required_manual_windows_update"),
        (can, {"required": True, "reason": "windows_update"}, "reboot_required"),
        (cannot, {"required": True, "reason": "other"}, "reboot_required_manual"),
        (cannot, {"required": True}, "reboot_required_manual"),
    ):
        assert reboot_key({"capabilities": caps, "reboot": reboot}) == key, (caps, reboot)
    assert reboot_key(None) == "reboot_required_manual"
    assert reboot_names({"reboot": lib}) == {
        "services": "cron.service, systemd",
        "kernel_running": "-",
        "kernel_newest": "-",
    }
    assert reboot_names({"reboot": kernel})["kernel_newest"] == "6.1.0-37-amd64"


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_waiting_host_text(lang: str) -> None:
    """ADR-0157: the repair of a waiting host renders with exactly what the integration passes, shows fingerprint,
    system and command as code, and asks for the comparison with the host's own identity before the command."""
    text = LANGS[lang]["issues"]["waiting_host"]
    names = waiting_hosts({"waiting": [{"node": "n1", "fingerprint": "ab" * 32, "os": "TestOS 1"}]})["n1"]
    fields = {field for _, field, _, _ in string.Formatter().parse(text["description"]) if field}
    assert fields == set(names)
    assert "{node}" in text["title"]
    description = text["description"]
    for code in ("`{fingerprint}`", "`{os}`", "`{command}`", "`vordr-agent identity`", "`vordr-hub reject {node}`"):
        assert code in description, code
    assert description.index("vordr-agent identity") < description.index("{command}")
    assert names["command"] in description.format(**names)


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_waiting_host_approve_texts(lang: str) -> None:
    """ADR-0192: the fixable repair of a waiting host has the same title, and its fix dialog renders with exactly what
    the integration passes, asks for the host's own `vordr-agent identity` before it names the command, and says how to
    reject. Each refusal of the form and every way the fix ends has its text, rendered with what the flow fills; the
    form's errors need no placeholder."""
    data = LANGS[lang]["issues"]
    issue = data["waiting_host_approve"]
    assert issue["title"] == data["waiting_host"]["title"]
    names = waiting_hosts({"waiting": [{"node": "n1", "fingerprint": "ab" * 32, "os": "TestOS 1"}]})["n1"]
    step = issue["fix_flow"]["step"]["confirm"]
    description = step["description"]
    assert fields_of(description) == set(names) and fields_of(step["title"]) == {"node"}
    for code in ("`{fingerprint}`", "`{os}`", "`{command}`", "`vordr-agent identity`", "`vordr-hub reject {node}`"):
        assert code in description, code
    assert description.index("vordr-agent identity") < description.index("{command}")
    assert set(step["data"]) == set(step["data_description"]) == {CONF_FINGERPRINT}
    errors = issue["fix_flow"]["error"]
    assert set(errors) == {FORM_INVALID, FORM_DIFFERS} and not any(fields_of(t) for t in errors.values())
    assert "vordr-hub reject" in errors[FORM_DIFFERS]
    abort = issue["fix_flow"]["abort"]
    reasons = {"no_longer_waiting", "approval_not_offered"}
    for code in (*APPROVE_REFUSALS, "some_future_code", "hub_not_connected", "hub_without_approval"):
        key, shown = approve_refusal("n1", code)
        reasons.add(key)
        assert fields_of(abort[key]) <= set(shown), (key, shown)
    assert set(abort) == reasons
    assert all(fields_of(abort[k]) <= {"node"} for k in ("no_longer_waiting", "approval_not_offered"))
    assert fields_of(abort["approve_refused"]) == {"node", "code"}


def test_waiting_host_command_is_the_hubs() -> None:
    """The command the repair shows is the one vordr-hub's usage names for admitting a host."""
    usage = (BASE.parents[1] / "cmd" / "vordr-hub" / "main.go").read_text(encoding="utf-8")
    assert "approve <node> --fingerprint <fp>" in usage
    names = waiting_hosts({"waiting": [{"node": "n1", "fingerprint": "ab" * 32, "os": ""}]})["n1"]
    assert names["command"] == f"vordr-hub approve n1 --fingerprint {'ab' * 32}"


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_ha_updates_texts(lang: str) -> None:
    """ADR-0158: every reason code of Home Assistant's updates has its own text (the sensor's attribute names, which
    the frontend shows in the reader's language), and so has every error of the options."""
    data = LANGS[lang]
    attrs = data["entity"]["sensor"]["ha_updates_ready"]["state_attributes"]
    assert set(attrs) == {*hu.CODES, "min_age_days", "min_age_days_firmware", "updates"}
    names = [attrs[code]["name"] for code in hu.CODES]
    assert all(names) and len(set(names)) == len(names), names
    assert set(data["options"]["error"]) == {
        *hu.ERRORS,
        hu.ERR_AUTO_INSTALL_OTHER,
        *ai.OPERATOR_KEY_ERRORS,
        *ax.FORM_ERRORS,
    }
    assert data["device"]["home_assistant"]["name"]


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_ai_explanation_texts(lang: str) -> None:
    """ADR-0166: every code of an AI explanation, every likely cause and every next step has its own text on the
    last-run sensor, every new attribute a name, and every error of the item its text. The fixed lists in the texts are
    exactly the ones the answer may choose from."""
    data = LANGS[lang]
    attrs = data["entity"]["sensor"]["last_run"]["state_attributes"]
    assert set(ax.ATTRIBUTES) <= set(attrs) and all(attrs[a]["name"] for a in ax.ATTRIBUTES)
    for attr, values in (("ai_code", ax.CODES), ("ai_likely_cause", ax.CAUSES), ("ai_next_steps", ax.NEXT_STEPS)):
        states = attrs[attr]["state"]
        assert set(states) == set(values), (lang, attr)
        assert all(states.values()) and len(set(states.values())) == len(states), (lang, attr)
    assert set(ax.FORM_ERRORS) <= set(data["options"]["error"])
    step = data["options"]["step"]["ai_explanations"]
    assert step["title"] and "vordr_ai_explanation" in step["description"]
    assert set(step["data"]) == set(step["data_description"]) == {ax.CONF_ENTITY, ax.CONF_DATA_ACK}


async def test_ai_explanations_form_has_its_texts(hass: HomeAssistant) -> None:
    """The item as the integration builds it: each field has its label and description in every language."""
    entry = MockConfigEntry(domain=DOMAIN, data={})
    entry.add_to_hass(hass)
    menu = await hass.config_entries.options.async_init(entry.entry_id)
    form = await hass.config_entries.options.async_configure(menu["flow_id"], {"next_step_id": "ai_explanations"})
    fields = {str(k) for k in form["data_schema"].schema}
    for lang, data in LANGS.items():
        step = data["options"]["step"]["ai_explanations"]
        assert set(step["data"]) == fields == set(step["data_description"]), lang
        assert data["options"]["step"]["init"]["menu_options"]["ai_explanations"], lang


def fields_of(text: str) -> set[str]:
    return {field for _, field, _, _ in string.Formatter().parse(text) if field}


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_ha_updates_install_texts(lang: str) -> None:
    """ADR-0162 – ADR-0165, ADR-0181, ADR-0182: every phase and result of the last-run sensor, every state of the
    installer and every kind of backup (also none, for firmware) has its text; the pause switch has its name; each kept
    result has its repair, whose texts use only the names the integration fills, with a text for every reason its fix
    may not install now; and the failed backup has its own."""
    data = LANGS[lang]
    run = data["entity"]["sensor"]["ha_updates_last_run"]
    assert set(run["state"]) == {*hu.PHASES, *hu.RESULTS}
    assert all(run["state"].values())
    assert set(run["state_attributes"]["installer"]["state"]) == set(hi.INSTALLER_STATES)
    assert set(run["state_attributes"]["backup"]["state"]) == set(hu.BACKUPS)
    assert data["entity"]["switch"]["ha_updates_paused"]["name"]
    names = set(hi.failure_names("update.example", None))
    for result in hu.KEPT:
        issue = data["issues"][f"ha_update_{result}"]
        confirm = issue["fix_flow"]["step"]["confirm"]
        for text in (issue["title"], confirm["title"], confirm["description"]):
            assert fields_of(text) <= names, (result, text)
        assert {"entity_id", "to_version", "at"} <= fields_of(confirm["description"]), result
        assert set(issue["fix_flow"]["abort"]) == {*hi.FIX_REFUSALS, "entry_not_loaded"}
        assert all(fields_of(t) <= {"entity_id"} for t in issue["fix_flow"]["abort"].values())
    backup = data["issues"][hi.ISSUE_BACKUP]
    assert fields_of(backup["description"]) == {"at"} and not fields_of(backup["title"])


async def test_ha_updates_form_has_its_texts(hass: HomeAssistant) -> None:
    """The options menu and form as the integration builds them: every menu item, field and choice has its text in
    every language, and the choices are exactly the ones the form offers."""
    entry = MockConfigEntry(domain=DOMAIN, data={})
    entry.add_to_hass(hass)
    menu = await hass.config_entries.options.async_init(entry.entry_id)
    form = await hass.config_entries.options.async_configure(menu["flow_id"], {"next_step_id": "ha_updates"})
    fields = {str(k): v for k, v in form["data_schema"].schema.items()}
    for lang, data in LANGS.items():
        options = data["options"]
        assert set(options["step"]["init"]["menu_options"]) == set(menu["menu_options"]), lang
        step = options["step"]["ha_updates"]
        assert set(step["data"]) == set(fields) == set(step["data_description"]), lang
        assert step["title"] and step["description"], lang
        # The fixed parts of the minimum age come from the rules (ADR-0179, ADR-0180), in every language.
        assert set(re.findall(r"\{(\w+)\}", step["description"])) == set(form["description_placeholders"]), lang
        for field, selector in fields.items():
            key = selector.config.get("translation_key") if isinstance(selector, SelectSelector) else None
            if key:
                assert set(data["selector"][key]["options"]) == set(selector.config["options"]), (lang, field)
    assert {k for k, s in fields.items() if isinstance(s, SelectSelector)} == {"kinds", "days"}
    assert form["description_placeholders"] == {
        "firmware_days": str(hu.FIRMWARE_MIN_AGE_DAYS),
        "patch_days": str(hu.PATCH_MIN_AGE_DAYS),
    }


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_swiss_spelling(lang: str) -> None:
    """German texts use Swiss spelling: ss, never the sharp s (AGENTS.md)."""
    assert "\N{LATIN SMALL LETTER SHARP S}" not in json.dumps(LANGS[lang], ensure_ascii=False)


def test_problem_key() -> None:
    assert problem_key({"reason": "stale"}) == "stale"
    assert problem_key({"reason": "check_failed", "check_failed_code": "timeout"}) == "timeout"
    assert problem_key({"reason": "check_failed"}) == "unknown"
    assert problem_key({"reason": "failed_verification"}) == "failed_verification"
    assert problem_key({"reason": "some_future_code"}) == "unknown"
    assert problem_key({"reason": "ok"}) == "unknown"


def test_stale_key_follows_what_vordr_installs() -> None:
    """ADR-0197: "with the button" only where the button takes some of the pending updates, or where the hub does not
    say; otherwise the policy or the host's lack of installs through Vördr."""
    stale = {"reason": "stale", "reasons": ["stale"]}
    none = {"install": False, "install_security": False}
    assert problem_key(stale) == "stale"  # no host: as before
    assert problem_key(stale, True, {"capabilities": none}) == "stale"  # a hub without installable
    assert problem_key(stale, True, {"capabilities": none, "installable": None}) == "stale"
    assert problem_key(stale, True, {"capabilities": {"install": True}, "installable": 2}) == "stale"
    assert problem_key(stale, True, {"capabilities": {"install": True}, "installable": 0}) == "stale_policy"
    assert problem_key(stale, True, {"capabilities": {"install_security": True}, "installable": 0}) == "stale_policy"
    assert problem_key(stale, True, {"capabilities": none, "installable": 0}) == "stale_manual"
    assert problem_key(stale, False, {"installable": 0}) == "stale_manual"  # no reboot through Vördr changes nothing
    assert problem_key(stale, True, {"capabilities": none, "installable": False}) == "stale"  # not a count
    # Only the reason "stale" itself; another first reason keeps its text.
    both = {"reason": "offline", "reasons": ["offline", "stale"]}
    assert problem_key(both, True, {"capabilities": none, "installable": 0}) == "offline"


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
