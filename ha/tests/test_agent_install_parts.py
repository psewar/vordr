"""The pure parts of the install over SSH (ADR-0161): the check of the hub's release, how the host's answers are read,
the addresses, the inputs, the scripts' syntax, the texts, and that the integration's constants are the hub's and the
agent's."""

from __future__ import annotations

import base64
import json
from pathlib import Path
import re
import shlex
import shutil
import string
import subprocess
import sys
from typing import Any

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from homeassistant.core import HomeAssistant
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr import agent_install as ai
from custom_components.vordr.agent_install import LOGIN_FORM_ERRORS
from custom_components.vordr.client import CODE_HUB_WITHOUT_DOWNLOAD

from .conftest import FakeHub, make_release, wait_until
from .systemd_run_standin import expand

ROOT = Path(__file__).parents[2]
TRANSLATIONS = ROOT / "custom_components" / "vordr" / "translations"
LANGS = {f.stem: json.loads(f.read_text(encoding="utf-8")) for f in TRANSLATIONS.glob("*.json")}
OPERATOR = Ed25519PrivateKey.generate()
KEYS = [OPERATOR.public_key().public_bytes_raw()]
AGENT = b"#!/bin/sh\necho stand-in\n"
FILE = "vordr-agent-linux-amd64"


def path(file: str = FILE, prefix: str = "/v1/agent-download/", tail: str = "") -> str:
    return f"{prefix}{file}?token={'a1' * 24}{tail}"


def answer(**over: Any) -> dict[str, Any]:
    """An agent_download_result as the hub sends it, for linux/amd64."""
    release = make_release(OPERATOR, {("linux", "amd64"): AGENT, ("linux", "arm64"): AGENT})
    a = {
        "t": "agent_download_result",
        "request_id": "r1",
        "code": "ok",
        "version": "1.4.0",
        "manifest": base64.b64encode(release.manifest).decode(),
        "manifest_sig": base64.b64encode(release.sig).decode(),
        "hub_key": base64.b64encode(release.hub_key).decode(),
        "expires_at": "2030-01-01T00:15:00Z",
        "downloads": [{"file": FILE, "path": path()}],
    }
    a.update(over)
    return a


def signed(manifest: dict[str, Any] | bytes, signer: Ed25519PrivateKey = OPERATOR) -> dict[str, str]:
    raw = manifest if isinstance(manifest, bytes) else json.dumps(manifest).encode()
    return {
        "manifest": base64.b64encode(raw).decode(),
        "manifest_sig": base64.b64encode(signer.sign(ai.MANIFEST_CONTEXT + raw)).decode(),
    }


BINARY = {"os": "linux", "arch": "amd64", "file": "vordr-agent-linux-amd64", "sha256": "ab" * 32, "size": 10}


def test_release_is_taken_only_from_the_signed_manifest() -> None:
    code, release = ai.check_release(answer(), KEYS, "amd64")
    assert code == "ok" and release is not None
    m = json.loads(base64.b64decode(answer()["manifest"]))["binaries"][0]
    assert (release.file, release.sha256, release.size, release.version) == (m["file"], m["sha256"], m["size"], "1.4.0")
    assert release.path.startswith("/v1/agent-download/vordr-agent-linux-amd64?token=")
    # The answer's own version and anything outside the manifest do not count.
    code, release = ai.check_release(answer(version="9.9.9"), KEYS, "amd64")
    assert release is not None and release.version == "1.4.0"


@pytest.mark.parametrize(
    ("over", "code"),
    [
        ({"manifest": "not base64!"}, "manifest_invalid"),
        ({"manifest_sig": None}, "manifest_invalid"),
        (
            signed({"version": "1.4.0", "binaries": [BINARY]}, Ed25519PrivateKey.generate()),
            "manifest_signature_invalid",
        ),
        ({"manifest_sig": base64.b64encode(b"x" * 63).decode()}, "manifest_signature_invalid"),
        (signed(b"not json"), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [BINARY], "extra": 1}), "manifest_invalid"),
        (signed({"version": "1.4.0\n", "binaries": [BINARY]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": []}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "size": 0}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "size": (128 << 20) + 1}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "size": True}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "size": 10.0}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "sha256": "AB" * 32}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "file": "../vordr-agent"}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "extra": 1}]}), "manifest_invalid"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "arch": "arm64"}]}), "no_binary_for_platform"),
        (signed({"version": "1.4.0", "binaries": [{**BINARY, "os": "windows"}]}), "no_binary_for_platform"),
        ({"downloads": []}, "download_invalid"),
        ({"downloads": "x"}, "download_invalid"),
        ({"downloads": [{"file": FILE, "path": path()}] * 2}, "download_invalid"),
        ({"downloads": [{"file": FILE, "path": path(prefix="/v1/agent/")}]}, "download_invalid"),
        ({"downloads": [{"file": FILE, "path": path(file="vordr-agent-linux-arm64")}]}, "download_invalid"),
        ({"downloads": [{"file": FILE, "path": path(tail="\n")}]}, "download_invalid"),
        ({"downloads": [{"file": FILE, "path": path(tail="&x=1")}]}, "download_invalid"),
        ({"downloads": [{"file": FILE, "path": path(tail="\nnext line")}]}, "download_invalid"),
        ({"hub_key": "AAAA"}, "download_invalid"),
        ({"hub_key": None}, "download_invalid"),
        # Characters outside ASCII: refused with the code, not raised.
        ({"manifest": "é"}, "manifest_invalid"),
        ({"manifest_sig": base64.b64encode(b"s" * 64).decode() + "​"}, "manifest_invalid"),
        ({"hub_key": base64.b64encode(b"k" * 32).decode() + "​"}, "download_invalid"),
    ],
)
def test_release_refusals(over: dict[str, Any], code: str) -> None:
    assert ai.check_release(answer(**over), KEYS, "amd64") == (code, None)


@pytest.mark.parametrize(
    ("version", "newest", "code"),
    [
        ("1.4.0", "", "ok"),
        ("2026-11-17-dev", "", "ok"),  # nothing taken before: a hand build too
        ("1.4.0", "1.4.0", "ok"),  # the same release on another host
        ("1.4.1", "1.4.0", "ok"),
        ("1.10.0", "1.9.9", "ok"),
        ("2.0.0", "1.99.99", "ok"),
        ("1.3.9", "1.4.0", "release_too_old"),
        ("1.9.0", "1.10.0", "release_too_old"),
        ("0.9.0", "1.0.0", "release_too_old"),
        ("1.5.0-dev", "1.4.0", "release_too_old"),  # as an agent on a release refuses a hand build
    ],
)
def test_release_age(version: str, newest: str, code: str) -> None:
    assert ai.check_release_age(version, newest) == code


def test_stored_release() -> None:
    assert ai.stored_release({"version": "1.4.0"}) == "1.4.0"
    for data in (None, {}, {"version": 140}, {"version": "1.4"}, {"version": "01.4.0"}, ["1.4.0"]):
        assert ai.stored_release(data) == ""
    assert ai.release_store_key("abc") == "vordr.agent_release.abc"


def test_release_with_the_backup_key() -> None:
    backup = Ed25519PrivateKey.generate()
    keys = ai.OperatorKeys(
        base64.b64encode(KEYS[0]).decode(), base64.b64encode(backup.public_key().public_bytes_raw()).decode()
    )
    over = signed({"version": "1.4.0", "binaries": [BINARY]}, backup)
    downloads = [{"file": BINARY["file"], "path": f"/v1/agent-download/{BINARY['file']}?token={'b2' * 16}"}]
    assert ai.check_release(answer(**over, downloads=downloads), keys.raw(), "amd64")[0] == "ok"
    assert ai.check_release(answer(**over, downloads=downloads), KEYS, "amd64")[0] == "manifest_signature_invalid"


@pytest.mark.parametrize(
    ("ha_url", "agent", "download"),
    [
        ("wss://hub.example:8443/v1/ha", "wss://hub.example:8443/v1/tunnel", "https://hub.example:8443/p?token=1"),
        ("wss://192.0.2.10:8443/v1/ha", "wss://192.0.2.10:8443/v1/tunnel", "https://192.0.2.10:8443/p?token=1"),
        (
            "wss://[2001:db8::1]:8443/v1/ha",
            "wss://[2001:db8::1]:8443/v1/tunnel",
            "https://[2001:db8::1]:8443/p?token=1",
        ),
    ],
)
def test_addresses_from_the_integrations(ha_url: str, agent: str, download: str) -> None:
    """The agent's tunnel and the download at the same host and port as the integration's /v1/ha."""
    assert ai.agent_hub_url(ha_url) == agent
    assert ai.download_url(ha_url, "/p?token=1") == download


def test_the_download_url_needs_no_quoting() -> None:
    """The URL the host's curl reads from its config file between quotes: only plain characters."""
    for ha_url in ("wss://hub.example:8443/v1/ha", "wss://192.0.2.10/v1/ha", "wss://[2001:db8::1]:8443/v1/ha"):
        assert ai.DOWNLOAD_URL_PATTERN.fullmatch(ai.download_url(ha_url, path()))
    good = ai.download_url("wss://hub.example:8443/v1/ha", path())
    for bad in (good + '"', good + "\\", good + " x", good.replace("https", "http"), good + "\n"):
        assert not ai.DOWNLOAD_URL_PATTERN.fullmatch(bad), bad


def test_addresses_without_a_port() -> None:
    from yarl import URL

    agent = URL(ai.agent_hub_url("wss://hub.example/v1/ha"))
    assert (agent.scheme, agent.host, agent.port, agent.path) == ("wss", "hub.example", 443, "/v1/tunnel")
    download = URL(ai.download_url("wss://hub.example/v1/ha", "/p?token=1"))
    assert (download.scheme, download.host, download.port, download.path) == ("https", "hub.example", 443, "/p")


def facts(**over: str) -> str:
    base = {
        "os": "Linux",
        "machine": "x86_64",
        "uid": "1000",
        "systemd": "yes",
        "apt-get": "yes",
        "curl": "yes",
        "wget": "no",
        "sha256sum": "yes",
        "base64": "yes",
        "mktemp": "yes",
        "timeout": "yes",
        "systemd-run": "yes",
        "sudo": "yes",
    }
    base.update(over)
    return "".join(f"{k}={v}\n" for k, v in base.items() if v != "-")


@pytest.mark.parametrize(
    ("over", "code", "names"),
    [
        ({}, "ok", {}),
        ({"uid": "0", "sudo": "no"}, "ok", {}),
        ({"curl": "no", "wget": "yes"}, "ok", {}),
        ({"os": "-"}, "probe_failed", {}),
        ({"uid": "-"}, "probe_failed", {}),
        ({"os": "Darwin", "machine": "arm64"}, "not_linux", {"os": "Darwin"}),
        ({"os": "MINGW64_NT-10.0;`x`"}, "not_linux", {"os": "MINGW64_NT-10.0x"}),
        ({"machine": "i686"}, "unknown_arch", {"machine": "i686"}),
        ({"machine": "riscv64", "systemd": "no"}, "unknown_arch", {"machine": "riscv64"}),
        ({"systemd": "no", "apt-get": "no"}, "no_systemd", {}),
        ({"apt-get": "no", "curl": "no"}, "no_apt", {}),
        ({"curl": "no"}, "no_download_tool", {}),
        ({"sha256sum": "no", "mktemp": "no"}, "missing_tools", {"tools": "sha256sum, mktemp"}),
        ({"systemd-run": "-"}, "missing_tools", {"tools": "systemd-run"}),
        ({"timeout": "no"}, "missing_tools", {"tools": "timeout"}),
        ({"sudo": "no"}, "no_sudo", {}),
    ],
)
def test_facts_in_the_order_a_person_fixes_them(over: dict[str, str], code: str, names: dict[str, str]) -> None:
    got, got_names, f = ai.check_facts(facts(**over) + "noise without a field\n")
    assert (got, got_names) == (code, names)
    assert (f is not None) is (code == "ok")


@pytest.mark.parametrize(("machine", "arch"), [("x86_64", "amd64"), ("aarch64", "arm64"), ("armv7l", "arm")])
def test_architectures(machine: str, arch: str) -> None:
    assert ai.check_facts(facts(machine=machine))[2] == ai.Facts(arch=arch, root=False)


def test_every_architecture_is_one_the_build_makes() -> None:
    """build.sh builds the agent for linux/amd64, arm64 and arm (GOARM=7): the manifest names exactly those."""
    build = (ROOT / "build.sh").read_text(encoding="utf-8")
    agent_loop = next(line for line in build.splitlines() if line.startswith("for t in linux/amd64 linux/arm64"))
    assert set(ai.ARCHES.values()) == set(re.findall(r"linux/(\w+)", agent_loop))
    assert "GOARM=7" in build


@pytest.mark.parametrize(
    ("stdout", "code", "names"),
    [
        (
            "step=agent exit=0\nresult=installed node=web-01 fingerprint=" + "ab" * 32,
            "agent_installed",
            {"node": "web-01", "fingerprint": "ab" * 32, "version": "1.4.0"},
        ),
        ("step=agent exit=0\nresult=installed node=Web fingerprint=" + "ab" * 32, "agent_no_result", {"exit": "0"}),
        ("step=agent exit=0\nresult=installed node=web-01 fingerprint=abc", "agent_no_result", {"exit": "0"}),
        ("step=agent exit=2\nresult=refused code=config_differs", "agent_config_differs", {"stage": "-"}),
        ("step=agent exit=2\nresult=refused code=hub_unreachable stage=dns", "agent_hub_unreachable", {"stage": "dns"}),
        ("step=agent exit=2\nresult=refused code=not_root", "agent_refused", {"code": "not_root"}),
        ("step=agent exit=2\nresult=refused code=Not-A-Code", "agent_no_result", {"exit": "2"}),
        ("step=agent exit=1\nresult=failed code=install_failed", "agent_install_failed", {}),
        ("step=agent exit=1\nresult=failed code=later_code", "agent_failed", {"code": "later_code"}),
        ("step=agent exit=1\nthe agent says something in prose", "agent_no_result", {"exit": "1"}),
        ("step=agent exit=127", "agent_no_result", {"exit": "127"}),
        ("step=verify code=hash_mismatch", "hash_mismatch", {"tool": "-", "exit": "-", "http": "-", "reason": "-"}),
        (
            "step=download code=download_failed tool=curl exit=7",
            "download_failed",
            {"tool": "curl", "exit": "7", "http": "-", "reason": "-"},
        ),
        (
            "step=download code=download_too_large tool=wget size=1536",
            "download_too_large",
            {"tool": "wget", "exit": "-", "http": "-", "reason": "-"},
        ),
        ("step=temp code=no_such_code", "install_no_result", {}),
        ("", "install_no_result", {}),
        ("sh: 1: syntax error", "install_no_result", {}),
    ],
)
def test_outcome_is_read_from_fields_only(stdout: str, code: str, names: dict[str, str]) -> None:
    got = ai.outcome(stdout, "1.4.0")
    assert (got.code, got.names) == (code, names)
    assert got.installed is (code == "agent_installed")


def test_install_command_quotes_every_value_and_keeps_secrets_off_it() -> None:
    release = ai.Release(
        "1.4.0", "vordr-agent-linux-amd64", "ab" * 32, 10, "/v1/agent-download/x?token=" + "c" * 48, "k"
    )
    keys = ai.OperatorKeys("op'key", "")
    plain = ai.install_command(release, "wss://hub.example:8443/v1/tunnel", "web-01", keys, True, ai.Sudo.NONE)
    # A transient service of its own, outside the login session; the script as sh -c.
    service = "systemd-run --pipe --wait --collect --quiet '--description=vordr agent install' -- sh -c "
    assert plain.startswith(service) and "'op'\"'\"'key'" in plain and "token" not in plain.split("vordr-install")[1]
    assert ai.install_command(release, "h", "", keys, False, ai.Sudo.NOPASSWD).startswith(f"sudo -k -n -- {service}")
    assert ai.install_command(release, "h", "", keys, False, ai.Sudo.PASSWORD).startswith(
        f"sudo -k -S -p '' -- {service}"
    )
    # The CA path only with a CA, the node only when given (both as their own argument, possibly empty).
    assert plain.endswith(f"web-01 'op'\"'\"'key' '' {ai.CA_PATH}")
    assert ai.install_command(release, "h", "", keys, False, ai.Sudo.NONE).endswith("'' 'op'\"'\"'key' '' ''")
    assert ai.install_input("https://u", "PEM", "pw") == f"pw\nhttps://u\n{base64.b64encode(b'PEM').decode()}\n"
    assert ai.install_input("https://u", "", "") == "https://u\n\n"


@pytest.mark.skipif(shutil.which("sh") is None, reason="no POSIX shell")
@pytest.mark.parametrize("script", ["PROBE_SCRIPT", "INSTALL_SCRIPT"])
def test_scripts_are_posix_shell(script: str) -> None:
    """Both scripts parse in the shell Debian runs them in (dash, as sh)."""
    run = subprocess.run(["sh", "-n", "-c", getattr(ai, script)], capture_output=True, text=True, check=False)
    assert run.returncode == 0, run.stderr


def test_the_systemd_run_stand_in_expands_as_systemd_does() -> None:
    """The fake host's systemd-run replaces what systemd replaces in a service's command line, so that an install that
    loses part of its script under systemd fails in the tests too."""
    env = {"PATH": "/usr/bin"}
    assert expand(["sum=${sum%% *}", "${reason:-none}", "${PATH:+set}", "a$$b", "${PATH}x"], env) == [
        "sum=",
        "none",
        "set",
        "a$b",
        "/usr/binx",
    ]
    assert expand(["$PATH", "$NOTSET", "x$PATH", "$(id)", "$1", "${", "%s"], env) == [
        "/usr/bin",
        "x$PATH",
        "$(id)",
        "$1",
        "${",
        "%s",
    ]


@pytest.mark.parametrize("with_ca", [True, False])
@pytest.mark.parametrize("sudo", [ai.Sudo.NONE, ai.Sudo.NOPASSWD, ai.Sudo.PASSWORD])
def test_the_install_reaches_the_host_as_it_is_written(with_ca: bool, sudo: str) -> None:
    """systemd-run hands the script and every value on as written: neither holds anything systemd replaces. A script
    with ${sum%% *} lost it, and every install ended in hash_mismatch."""
    release = ai.Release("1.4.0", FILE, "ab" * 32, 4096, path(), base64.b64encode(b"k" * 32).decode())
    keys = ai.OperatorKeys(base64.b64encode(b"o" * 32).decode(), base64.b64encode(b"b" * 32).decode())
    argv = shlex.split(ai.install_command(release, "wss://hub.example:8443/v1/tunnel", "web-01", keys, with_ca, sudo))
    started = argv[argv.index("--", argv.index("systemd-run")) + 1 :]
    assert started[:3] == ["sh", "-c", ai.INSTALL_SCRIPT]
    assert expand(started, {"PATH": "/usr/bin"}) == started


@pytest.mark.skipif(shutil.which("sh") is None, reason="no POSIX shell")
@pytest.mark.parametrize(
    "url",
    [
        'https://127.0.0.1:9/v1/agent-download/f?token=ab"c',  # would end the quoted value in curl's config file
        "https://127.0.0.1:9/v1/agent-download/f?token=ab\\c",
        "https://127.0.0.1:9/v1/agent-download/f?token=a b",
        "https://127.0.0.1:9/v1/agent-download/f?token=a$b",
        "http://127.0.0.1:9/v1/agent-download/f?token=ab",
        "https://127.0.0.1:9/v1/other",
    ],
)
def test_the_install_script_takes_only_a_plain_download_url(url: str) -> None:
    """The host refuses a URL with a character curl's config file would need quoted, before it creates anything."""
    args = ["sh", "-c", ai.INSTALL_SCRIPT, "vordr-install", "ab" * 32, "10", "h", "k", "", "o", "", ""]
    run = subprocess.run(args, input=f"{url}\n\n", capture_output=True, text=True, check=False, timeout=30)
    assert (run.returncode, run.stdout) == (2, "step=input code=bad_input\n")


@pytest.mark.parametrize(
    ("values", "errors"),
    [
        ({"host": "web-01.example", "port": 22, "username": "root", "node": ""}, {}),
        ({"host": "2001:db8::1", "port": 2222.0, "username": "admin_1", "node": "web-01"}, {}),
        ({"host": "web 01", "port": 22, "username": "root"}, {"host": "invalid_host"}),
        ({"host": "-web", "port": 22, "username": "root"}, {"host": "invalid_host"}),
        ({"host": "we\nb", "port": 22, "username": "root"}, {"host": "invalid_host"}),
        ({"host": " web-01 ", "port": 22, "username": " root "}, {}),  # spaces around are dropped
        ({"host": "web", "port": 0, "username": "root"}, {"port": "invalid_port"}),
        ({"host": "web", "port": 22.5, "username": "root"}, {"port": "invalid_port"}),
        ({"host": "web", "port": 22, "username": "root user"}, {"username": "invalid_username"}),
        ({"host": "web", "port": 22, "username": "root", "node": "Web"}, {"node": "invalid_node"}),
        ({"host": "web", "port": 22, "username": "root", "node": "we\nb"}, {"node": "invalid_node"}),
    ],
)
def test_target(values: dict[str, Any], errors: dict[str, str]) -> None:
    target, got = ai.check_target(values)
    assert got == errors
    assert (target is not None) is (not errors)


def test_operator_keys_from_the_options() -> None:
    good = base64.b64encode(KEYS[0]).decode()
    assert ai.operator_keys({}) is None
    assert ai.operator_keys({"operator_keys": "x"}) is None
    assert ai.operator_keys({"operator_keys": {"operator_key": ""}}) is None
    assert ai.operator_keys({"operator_keys": {"operator_key": "AAAA"}}) is None
    assert ai.operator_keys({"operator_keys": {"operator_key": good}}) == ai.OperatorKeys(good, "")
    assert ai.operator_keys({"operator_keys": {"operator_key": good}}).raw() == KEYS


def test_credentials_are_dropped() -> None:
    creds, errors = ai.check_credentials({"password": "p", "sudo_password": "s"})
    assert errors == {} and creds is not None and creds.for_sudo() == "s"
    creds.clear()
    assert vars(creds) == {"password": "", "ssh_key": "", "ssh_key_passphrase": "", "sudo_password": ""}
    creds, _ = ai.check_credentials({"password": "p"})
    assert creds is not None and creds.for_sudo() == "p"


# --- the client


async def test_the_client_asks_only_a_hub_that_offers_the_download(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """An older hub ends the connection on a message it does not know: without the feature nothing is sent."""
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    client = entry.runtime_data.client
    await wait_until(lambda: entry.runtime_data.connected)
    assert client.features == frozenset() and not client.offers_agent_download
    assert await client.agent_download("linux", "amd64") == {"code": CODE_HUB_WITHOUT_DOWNLOAD}
    assert fake_hub.download_requests == []
    fake_hub.features = ["agent_download", "something_later"]
    fake_hub.release = make_release(OPERATOR, {("linux", "amd64"): AGENT})
    await fake_hub.drop_connections()
    await wait_until(lambda: client.offers_agent_download)
    got = await client.agent_download("linux", "amd64")
    assert got["code"] == "ok" and [(r["os"], r["arch"]) for r in fake_hub.download_requests] == [("linux", "amd64")]
    # The answer goes to who asked, not to the entities: the coordinator never sees it.
    assert "downloads" not in json.dumps(entry.runtime_data.data.hub)


# --- the constants are the hub's and the agent's


def test_constants_match_the_go_code() -> None:
    wire = (ROOT / "internal/wire/wire.go").read_text(encoding="utf-8")
    assert re.search(r'TunnelPath\s+=\s+"' + re.escape(ai.TUNNEL_PATH) + '"', wire)
    manifest = (ROOT / "internal/protocol/manifest.go").read_text(encoding="utf-8")
    assert f'manifestContext = "{ai.MANIFEST_CONTEXT.decode()[:-1]}\\x00"' in manifest
    assert f"MaxBinarySize = {ai.MAX_BINARY_SIZE >> 20} << 20" in manifest
    assert "fileNamePattern = regexp.MustCompile(`" + ai.FILE_PATTERN.pattern + "`)" in manifest
    assert "versionPattern  = regexp.MustCompile(`" + ai.VERSION_PATTERN.pattern + "`)" in manifest
    assert "releasePattern = regexp.MustCompile(`" + ai.RELEASE_PATTERN.pattern + "`)" in manifest


def test_the_integration_loads_without_the_ssh_library() -> None:
    """Only the install item imports asyncssh (ADR-0161): the integration, its config flow and its platforms load
    without it."""
    modules = ["", ".config_flow", ".sensor", ".binary_sensor", ".update", ".button", ".diagnostics", ".repairs"]
    imports = ", ".join(f"custom_components.vordr{m}" for m in modules)
    code = f"import sys; import {imports}; print('asyncssh' in sys.modules)"
    run = subprocess.run([sys.executable, "-c", code], cwd=ROOT, capture_output=True, text=True, check=False)
    assert (run.returncode, run.stdout.strip()) == (0, "False"), run.stderr[-2000:]


# --- texts


def fields_of(text: str) -> set[str]:
    return {f for _, f, _, _ in string.Formatter().parse(text) if f}


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_every_end_has_its_text_with_exactly_its_values(lang: str) -> None:
    """Every way "Add host" ends has its text in config_subentries.host.abort, with exactly the values it names, and
    every error of its forms its text; none of them is left in the options (ADR-0185)."""
    host = LANGS[lang]["config_subentries"]["host"]
    assert set(ai.ABORTS) == set(host["abort"])
    for code, names in ai.ABORTS.items():
        assert fields_of(host["abort"][code]) == set(names), (lang, code)
    assert set(ai.FORM_ERRORS) == set(host["error"])
    assert set(LOGIN_FORM_ERRORS) <= set(ai.FORM_ERRORS)
    assert not set(LOGIN_FORM_ERRORS) & set(ai.ABORTS.keys() - {"ssh_lost", "ssh_timeout"})
    # Every way it can end that is not success says what happened to the host.
    assert "`{fingerprint}`" in host["abort"]["agent_installed"]
    options = LANGS[lang]["options"]
    assert set(ai.OPERATOR_KEY_ERRORS) <= set(options["error"])
    assert not set(options["error"]) & set(ai.FORM_ERRORS) and "abort" not in options and "progress" not in options


@pytest.mark.parametrize("lang", sorted(LANGS))
def test_install_steps_have_their_texts(lang: str) -> None:
    """The button and every step of "Add host" have their texts; the options menu keeps the operator keys and offers no
    install."""
    host = LANGS[lang]["config_subentries"]["host"]
    assert host["initiate_flow"]["user"] and host["entry_type"]
    steps = host["step"]
    options = LANGS[lang]["options"]["step"]
    assert set(options["init"]["menu_options"]) == {"ha_updates", "operator_keys", "ai_explanations"}
    forms = [
        (options["operator_keys"], {"operator_key", "backup_operator_key"}),
        (steps["user"], {"host", "port", "username", "node"}),
        (steps["login"], {"password", "ssh_key", "ssh_key_passphrase", "sudo_password"}),
    ]
    for step, keys in forms:
        assert set(step["data"]) == keys == set(step["data_description"]), (lang, step["title"])
    assert set(steps) == {"user", "host_key", "login"}
    assert fields_of(steps["user"]["description"]) == {"client"}
    assert "`vordr-hub client-role {client} --role admin`" in steps["user"]["description"]
    assert fields_of(steps["host_key"]["description"]) == {"host", "port", "algorithm", "fingerprint"}
    assert set(steps["host_key"]["menu_options"]) == {"login", "host_key_rejected"}
    assert fields_of(steps["login"]["description"]) == {"host", "username"}
    assert set(host["progress"]) == {"install"} and fields_of(host["progress"]["install"]) == {"host"}
    assert "Linux" in steps["user"]["description"]
    # Without an operator key, "Add host" points to where it is entered: the options item, by its own title.
    assert options["operator_keys"]["title"] in host["abort"]["install_needs_operator_key"]
