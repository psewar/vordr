"""Installing the agent on a new Linux host over SSH from the button "Add host" (ADR-0161, ADR-0185), end to end
against a fake hub and a fake host behind a real SSH server whose commands run in a sandbox (fake_ssh_host.py). The
integration's scripts run for real there: the download, the size and hash check, the cleanup. The flow is started as
Home Assistant's frontend starts it: as a subentry flow of the hub's entry."""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import re
from typing import Any

import asyncssh
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from homeassistant.config_entries import SOURCE_USER, ConfigEntryState
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType, InvalidData
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr import agent_install as ai, config_flow, ssh
from custom_components.vordr.diagnostics import async_get_config_entry_diagnostics

from .conftest import FakeHub, make_release, wait_until
from .fake_ssh_host import AGENT, FINGERPRINT, FakeHost, installed

# curl as an older version that does not stop at --max-filesize while it downloads (curl before 8.4 checks only a
# Content-Length): the file size limit has to stop it.
CURL_WITHOUT_MAX_FILESIZE = """#!/bin/sh
skip=
for a do
  shift
  if [ -n "$skip" ]; then skip=; continue; fi
  if [ "$a" = --max-filesize ]; then skip=1; continue; fi
  set -- "$@" "$a"
done
exec REAL "$@"
"""
# curl whose connections all go to PORT on this machine: port 1 refuses (no connection, as behind a firewall that
# rejects), the SSH server's port accepts and then speaks no TLS (a connection, but no hub).
CURL_TO_PORT = """#!/bin/sh
exec REAL --connect-to ::127.0.0.1:PORT "$@"
"""
# wget the same way: the URL file it is given (-i) points to PORT on this machine.
WGET_TO_PORT = """#!/bin/sh
prev=
for a do
  if [ "$prev" = -i ]; then printf 'https://127.0.0.1:PORT/v1/agent-download/x\\n' > "$a"; fi
  prev=$a
done
exec REAL "$@"
"""

OTHER = {"other_item": {"kept": True}}  # another menu item's options, kept by every save
OPERATOR = Ed25519PrivateKey.generate()
BACKUP = Ed25519PrivateKey.generate()
PASSWORD = "login-Secret-4711"
SUDO_PASSWORD = "sudo-Secret-0815"
PASSPHRASE = "key-Passphrase-42"


def pub(key: Ed25519PrivateKey) -> str:
    return base64.b64encode(key.public_key().public_bytes_raw()).decode()


@pytest.fixture
async def ready(
    hass: HomeAssistant,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    entry: MockConfigEntry,
    monkeypatch: pytest.MonkeyPatch,
    caplog: pytest.LogCaptureFixture,
) -> MockConfigEntry:
    """A hub that hands out a release signed with the operator key the person entered, an admin user with sudo on
    the host, and every log line captured."""
    caplog.set_level(logging.DEBUG)
    monkeypatch.setattr(ai, "CA_PATH", str(fake_host.ca_path))
    monkeypatch.setattr(ai, "SYSTEMD_DIR", str(fake_host.systemd))
    fake_hub.features = ["agent_download"]
    fake_hub.release = make_release(
        OPERATOR,
        {("linux", "amd64"): AGENT, ("linux", "arm64"): AGENT, ("linux", "arm"): AGENT, ("windows", "amd64"): b"MZ"},
    )
    fake_host.users = {"admin": PASSWORD}
    fake_host.set(sudo_password=SUDO_PASSWORD)
    entry.add_to_hass(hass)
    hass.config_entries.async_update_entry(entry, options={**OTHER, ai.OPTIONS_KEY: {"operator_key": pub(OPERATOR)}})
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected and entry.runtime_data.client.offers_agent_download)
    return entry


async def add_host(hass: HomeAssistant, entry: MockConfigEntry) -> dict[str, Any]:
    """Press "Add host" on the integration's page: the frontend starts a subentry flow of the hub's entry."""
    return await hass.config_entries.subentries.async_init(
        (entry.entry_id, config_flow.HOST_SUBENTRY), context={"source": SOURCE_USER}
    )


def handler(hass: HomeAssistant, flow_id: str) -> config_flow.AddHostFlow:
    """The flow object behind a running "Add host"."""
    return hass.config_entries.subentries._progress[flow_id]


async def to_host_key(hass: HomeAssistant, entry: MockConfigEntry, host: FakeHost, **target: Any) -> dict[str, Any]:
    """From the button to the host key: host, port, user, node."""
    result = await add_host(hass, entry)
    assert result["type"] is FlowResultType.FORM and result["step_id"] == "user"
    assert result["description_placeholders"] == {"client": entry.data["name"]}
    values = {"host": "127.0.0.1", "port": host.port, "username": "admin", "node": "web-01", **target}
    return await hass.config_entries.subentries.async_configure(result["flow_id"], values)


async def log_in(hass: HomeAssistant, result: dict[str, Any], **creds: str) -> dict[str, Any]:
    """Confirm the host key, then enter the credentials."""
    flow = hass.config_entries.subentries
    assert result["type"] is FlowResultType.MENU and result["step_id"] == "host_key"
    result = await flow.async_configure(result["flow_id"], {"next_step_id": "login"})
    assert result["type"] is FlowResultType.FORM and result["step_id"] == "login"
    return await flow.async_configure(result["flow_id"], creds)


async def finish(hass: HomeAssistant, result: dict[str, Any]) -> dict[str, Any]:
    """Wait for the install's progress and take its end."""
    assert result["type"] is FlowResultType.SHOW_PROGRESS, result
    await hass.async_block_till_done()
    result = await hass.config_entries.subentries.async_configure(result["flow_id"])
    assert result["type"] is FlowResultType.ABORT, result
    return result


async def install(hass: HomeAssistant, entry: MockConfigEntry, host: FakeHost, **creds: str) -> dict[str, Any]:
    result = await to_host_key(hass, entry, host)
    return await finish(hass, await log_in(hass, result, **(creds or {"password": PASSWORD})))


async def no_secret_left(
    hass: HomeAssistant,
    entry: MockConfigEntry,
    fake_hub: FakeHub,
    caplog: pytest.LogCaptureFixture,
    hass_storage: dict[str, Any],
    *more: str,
) -> None:
    """Neither the credentials nor a download token are in the logs, the entry, its options, the diagnostics or
    Home Assistant's storage."""
    found = {
        "logs": caplog.text,
        "entry": json.dumps(entry.as_dict(), default=str),
        "diagnostics": json.dumps(await async_get_config_entry_diagnostics(hass, entry), default=str),
        "storage": json.dumps(hass_storage, default=str),
    }
    for secret in (PASSWORD, SUDO_PASSWORD, PASSPHRASE, *fake_hub.tokens, "agent-download/vordr-agent", *more):
        for where, text in found.items():
            assert secret not in text, (where, secret[:12])


def assert_nothing_ran(host: FakeHost, fake_hub: FakeHub) -> None:
    """Nothing reached the host beyond the probe: no install, no agent, nothing left in its temporary folder."""
    assert not any("vordr-install" in c for c in host.commands)
    assert host.file("agent-args") is None
    assert host.leftovers() == []
    assert not host.ca_path.exists()
    assert not any(t["used"] for t in fake_hub.tokens.values())


TOKEN = re.compile(r"token=[0-9a-f]{32,}")  # a download token, not the word in the install script's own check


def outputs(monkeypatch: pytest.MonkeyPatch) -> list[str]:
    """What each command on the host printed, as Home Assistant read it."""
    seen: list[str] = []
    real_run = ssh.run

    async def spy(conn: Any, command: str, stdin: str = "", timeout: float | None = None) -> ssh.Ran:
        ran = await real_run(conn, command, stdin, timeout)
        seen.append(ran.stdout)
        return ran

    monkeypatch.setattr(ssh, "run", spy)
    return seen


# --- the operator keys


async def test_operator_keys_are_checked_and_stored(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    """The item "Operator keys": public keys, checked, stored beside the other items' options; empty removes them."""
    entry.add_to_hass(hass)
    hass.config_entries.async_update_entry(entry, options=OTHER)
    flow = hass.config_entries.options

    async def save(values: dict[str, str]) -> dict[str, Any]:
        result = await flow.async_init(entry.entry_id)
        assert result["menu_options"] == ["ha_updates", "operator_keys", "ai_explanations"]
        result = await flow.async_configure(result["flow_id"], {"next_step_id": "operator_keys"})
        return await flow.async_configure(result["flow_id"], values)

    bad = {"operator_key": "not base64!"}
    assert (await save(bad))["errors"] == {"operator_key": "invalid_operator_key"}
    short = {"operator_key": base64.b64encode(b"x" * 31).decode()}
    assert (await save(short))["errors"] == {"operator_key": "invalid_operator_key"}
    alone = {"operator_key": "", "backup_operator_key": pub(BACKUP)}
    assert (await save(alone))["errors"] == {"backup_operator_key": "backup_without_operator_key"}
    same = {"operator_key": pub(OPERATOR), "backup_operator_key": pub(OPERATOR)}
    assert (await save(same))["errors"] == {"backup_operator_key": "backup_equals_operator_key"}
    broken = {"operator_key": pub(OPERATOR), "backup_operator_key": "AAAA"}
    assert (await save(broken))["errors"] == {"backup_operator_key": "invalid_backup_operator_key"}
    # An invisible character pasted with the key (zero-width space): a form error, not an unknown error.
    invisible = {"operator_key": pub(OPERATOR) + "​", "backup_operator_key": "​" + pub(BACKUP)}
    assert (await save(invisible))["errors"] == {
        "operator_key": "invalid_operator_key",
        "backup_operator_key": "invalid_backup_operator_key",
    }
    assert entry.options == OTHER

    result = await save({"operator_key": f" {pub(OPERATOR)} ", "backup_operator_key": pub(BACKUP)})
    assert result["type"] is FlowResultType.CREATE_ENTRY
    keys = {"operator_key": pub(OPERATOR), "backup_operator_key": pub(BACKUP)}
    assert entry.options == {**OTHER, "operator_keys": keys}
    # Opened again, the form shows them.
    result = await flow.async_init(entry.entry_id)
    result = await flow.async_configure(result["flow_id"], {"next_step_id": "operator_keys"})
    shown = {str(k): k.default() for k in result["data_schema"].schema}
    assert shown == keys
    flow.async_abort(result["flow_id"])
    # Empty: no key, "Add host" explains.
    await save({"operator_key": "", "backup_operator_key": ""})
    assert entry.options == OTHER


# --- where the install starts (ADR-0185)


async def test_the_options_menu_no_longer_offers_the_install(hass: HomeAssistant, ready: MockConfigEntry) -> None:
    """The options are the integration's settings: their menu offers no install, and its old step is gone."""
    flow = hass.config_entries.options
    result = await flow.async_init(ready.entry_id)
    assert result["type"] is FlowResultType.MENU and result["menu_options"] == config_flow.OPTIONS_MENU
    assert config_flow.OPTIONS_MENU == ["ha_updates", "operator_keys", "ai_explanations"]
    with pytest.raises(InvalidData):
        await flow.async_configure(result["flow_id"], {"next_step_id": "install_agent"})
    flow.async_abort(result["flow_id"])
    assert not [name for name in vars(config_flow.VordrOptionsFlow) if name.startswith("async_step_install")]


async def test_the_button_add_host_is_registered(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    """Every entry offers the subentry type behind the button "Add host", loaded or not; it has no reconfigure, since
    nothing of it is ever stored."""
    entry.add_to_hass(hass)
    assert entry.supported_subentry_types == {config_flow.HOST_SUBENTRY: {"supports_reconfigure": False}}
    assert config_flow.VordrConfigFlow.async_get_supported_subentry_types(entry) == {
        config_flow.HOST_SUBENTRY: config_flow.AddHostFlow
    }
    assert config_flow.HOST_SUBENTRY == "host"


# --- before anything is entered


@pytest.mark.parametrize("missing", ["entry_loaded", "operator_key", "feature", "connection", "ssh_library"])
async def test_install_explains_why_it_cannot_run(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    monkeypatch: pytest.MonkeyPatch,
    missing: str,
) -> None:
    """The entry not running, without the operator key, without a hub that hands out the release, without the hub, or
    without the SSH library: pressing "Add host" says why, each with its own code, before anything is asked; nothing is
    stored and no flow is left."""
    if missing == "ssh_library":
        monkeypatch.setattr(config_flow, "SSH_LIBRARY", "vordr_no_such_ssh_library")
        reason = "install_needs_ssh_library"
    elif missing == "entry_loaded":
        assert await hass.config_entries.async_unload(ready.entry_id)
        assert ready.state is ConfigEntryState.NOT_LOADED
        reason = "entry_not_loaded"
    elif missing == "operator_key":
        hass.config_entries.async_update_entry(ready, options=OTHER)
        reason = "install_needs_operator_key"
    elif missing == "feature":
        fake_hub.features = []
        await fake_hub.drop_connections()
        await wait_until(lambda: ready.runtime_data.connected and not ready.runtime_data.client.features)
        reason = "hub_without_download"
    else:
        await fake_hub.shut_down()
        await wait_until(lambda: not ready.runtime_data.connected)
        reason = "hub_not_connected"
    result = await add_host(hass, ready)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == reason
    assert not result["description_placeholders"]
    assert fake_host.logins == [] and fake_host.commands == []
    assert fake_hub.download_requests == []
    assert ready.subentries == {} and hass.config_entries.subentries.async_progress() == []


# --- the whole way


@pytest.mark.parametrize("tool", ["curl", "wget"])
async def test_install_with_password_and_sudo(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    caplog: pytest.LogCaptureFixture,
    hass_storage: dict[str, Any],
    tool: str,
) -> None:
    """A user with sudo and a password: the host key is shown and confirmed, the release checked, the binary
    downloaded on the host, checked and run as root, outside the login session, with the values for the hub; the
    outcome names the fingerprint and the approval. Nothing secret is left anywhere, no process on the host had the
    download token or a password among its arguments, and the host keeps nothing but the agent and the CA."""
    if not fake_host.has(tool):
        pytest.skip(f"no {tool} on this machine")
    fake_host.without("wget" if tool == "curl" else "curl")
    fake_host.record_argv()
    result = await to_host_key(hass, ready, fake_host)
    names = result["description_placeholders"]
    assert names == {
        "host": "127.0.0.1",
        "port": str(fake_host.port),
        "algorithm": "ssh-ed25519",
        "fingerprint": fake_host.host_key.get_fingerprint("sha256"),
        "key_file": "ssh_host_ed25519_key.pub",
    }
    assert fake_host.logins == []  # the host key came without logging in
    flow_handler = handler(hass, result["flow_id"])
    held = flow_handler._install
    result = await log_in(hass, result, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    await hass.async_block_till_done()
    # The install closed the connection and dropped sudo's password as it ended, before anyone opens the result: a
    # browser that went away leaves nothing open.
    assert held._conn is None and held._sudo_password == ""
    assert flow_handler._install is held
    result = await finish(hass, result)
    assert result["reason"] == "agent_installed"
    assert result["description_placeholders"] == {"node": "web-01", "fingerprint": FINGERPRINT, "version": "1.4.0"}

    # The hub was asked for this host's platform, and its one download used.
    assert [(r["os"], r["arch"]) for r in fake_hub.download_requests] == [("linux", "amd64")]
    assert [t["used"] for t in fake_hub.tokens.values()] == [True]
    # The agent ran as root with the hub's address for agents, the hub's key, the person's operator key, the node and
    # the hub's CA, which stays on the host for the agent.
    hub_key = base64.b64encode(fake_hub.release.hub_key).decode()
    assert fake_host.file("agent-args").split("\n")[:-1] == [
        "install",
        "--hub",
        f"wss://127.0.0.1:{fake_hub.port}/v1/tunnel",
        "--hub-key",
        hub_key,
        "--operator-key",
        pub(OPERATOR),
        "--node",
        "web-01",
        "--ca-file",
        str(fake_host.ca_path),
    ]
    assert fake_host.file("agent-uid") == "0\n"
    assert fake_host.ca_path.read_text(encoding="utf-8") == fake_hub.cert_pem
    assert fake_host.leftovers() == []
    # The probe ran as the user; sudo's password and the download went on stdin, never on a command line. The install
    # ran as a transient service of its own (systemd-run), not in the login session.
    probe, nopasswd, check, run = fake_host.commands
    assert probe.startswith("sh -c ") and nopasswd == "sudo -k -n true" and check == "sudo -k -S -p '' true"
    assert run.startswith("sudo -k -S -p '' -- systemd-run --pipe --wait --collect --quiet ")
    assert fake_host.file("systemd-run-args").splitlines() == [
        "--pipe",
        "--wait",
        "--collect",
        "--quiet",
        "--description=vordr agent install",
    ]
    assert fake_host.stdins[3].startswith(f"{SUDO_PASSWORD}\nhttps://127.0.0.1:{fake_hub.port}/v1/agent-download/")
    for command in fake_host.commands:
        assert PASSWORD not in command and SUDO_PASSWORD not in command
        assert not any(token in command for token in fake_hub.tokens)
    # Neither did any process the scripts started: the download tool read the URL from a file in the private folder.
    argv = fake_host.argv()
    assert re.search(rf"^{tool} ", argv, re.M) and re.search(r"^sha256sum", argv, re.M)  # the recording works
    # The download is bounded in size and time: curl by its own options, wget by timeout; both give up on a connection
    # to the hub after 20 s.
    size = len(fake_hub.release.served["vordr-agent-linux-amd64"])
    if tool == "curl":
        assert re.search(rf"^curl .*--connect-timeout 20 --max-time 900 --max-filesize {size} ", argv, re.M), argv
    else:
        assert re.search(r"^timeout 900 wget .*--tries=1 .*--connect-timeout=20 ", argv, re.M), argv
    assert TOKEN.search(argv) is None and not any(token in argv for token in fake_hub.tokens)
    assert f"127.0.0.1:{fake_hub.port}/v1/agent-download" not in argv
    assert PASSWORD not in argv and SUDO_PASSWORD not in argv
    # Nothing secret anywhere, and the flow dropped what it held.
    await no_secret_left(hass, ready, fake_hub, caplog, hass_storage)
    assert flow_handler._install is None and held._conn is None and held._sudo_password == ""
    assert ready.options == {**OTHER, "operator_keys": {"operator_key": pub(OPERATOR)}}
    assert_nothing_stored(hass, ready, fake_host, hass_storage, "web-01")


def assert_nothing_stored(
    hass: HomeAssistant, entry: MockConfigEntry, host: FakeHost, hass_storage: dict[str, Any], node: str
) -> None:
    """ "Add host" ended, also after a successful install, with an abort that stores nothing (ADR-0185): no subentry in
    the entry or in what Home Assistant writes of it, no other entry, no flow left, and nothing of the host (its node,
    its host key, the agent's fingerprint) in Home Assistant's storage; the hub's registry is the one list of hosts."""
    assert entry.subentries == {} and entry.as_dict()["subentries"] == []
    assert hass.config_entries.async_entries() == [entry]
    assert hass.config_entries.subentries.async_progress() == []
    stored = json.dumps(hass_storage, default=str)
    for value in (node, host.host_key.get_fingerprint("sha256"), FINGERPRINT, '"subentry_type"'):
        assert value not in stored, value


async def test_install_as_root_with_a_key(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    caplog: pytest.LogCaptureFixture,
    hass_storage: dict[str, Any],
) -> None:
    """root with a private key that has a passphrase, an ARM host, the host's own name as the node, the hub's CA
    already there: no sudo, no --node, the CA kept as it is. A release signed with the backup key is taken."""
    key = asyncssh.generate_private_key("ssh-ed25519")
    pem = key.export_private_key(passphrase=PASSPHRASE).decode()
    fake_host.keys = {"root": [key]}
    fake_host.set(uid="0", machine="aarch64", agent_out=installed("arm-1"))
    fake_host.ca_path.parent.mkdir()
    fake_host.ca_path.write_text(fake_hub.cert_pem, encoding="utf-8")
    hass.config_entries.async_update_entry(
        ready, options={ai.OPTIONS_KEY: {"operator_key": pub(OPERATOR), "backup_operator_key": pub(BACKUP)}}
    )
    fake_hub.release = make_release(BACKUP, {("linux", "arm64"): AGENT})
    result = await to_host_key(hass, ready, fake_host, username="root", node="")
    result = await finish(hass, await log_in(hass, result, ssh_key=pem, ssh_key_passphrase=PASSPHRASE))
    assert result["reason"] == "agent_installed"
    assert result["description_placeholders"]["node"] == "arm-1"
    assert [(r["os"], r["arch"]) for r in fake_hub.download_requests] == [("linux", "arm64")]
    args = fake_host.file("agent-args").split("\n")
    assert "--node" not in args and args[args.index("--backup-operator-key") + 1] == pub(BACKUP)
    assert fake_host.commands[1].startswith("systemd-run --pipe ")  # as root: no sudo
    assert fake_host.ca_path.read_text(encoding="utf-8") == fake_hub.cert_pem
    assert fake_host.leftovers() == []
    assert ("root", "key") in fake_host.logins and ("root", "password") not in fake_host.logins
    pem_line = pem.splitlines()[2]
    await no_secret_left(hass, ready, fake_hub, caplog, hass_storage, pem_line)
    assert_nothing_stored(hass, ready, fake_host, hass_storage, "arm-1")


# --- the host key


async def test_host_key_not_confirmed(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """The person says the keys differ: the flow ends, nothing was sent, not even a login attempt."""
    result = await to_host_key(hass, ready, fake_host)
    assert result["type"] is FlowResultType.MENU and result["menu_options"] == ["login", "host_key_rejected"]
    result = await hass.config_entries.subentries.async_configure(
        result["flow_id"], {"next_step_id": "host_key_rejected"}
    )
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "host_key_rejected"
    assert result["description_placeholders"] == {"host": "127.0.0.1"}
    assert fake_host.logins == [] and fake_host.commands == []
    assert fake_hub.download_requests == []


async def test_host_key_changed_after_it_was_confirmed(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """Another host key at the login than the one confirmed: refused before any password is sent."""
    result = await to_host_key(hass, ready, fake_host)
    fake_host.new_host_key()
    result = await log_in(hass, result, password=PASSWORD)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "ssh_host_key_changed"
    assert not any(method == "password" for _, method in fake_host.logins)
    assert fake_host.commands == [] and fake_hub.download_requests == []


async def test_the_ed25519_key_is_asked_for_first(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """A host with an RSA and an ED25519 key shows the ED25519 one, with the file to compare it with, and the login
    takes that key (ADR-0186). asyncssh's own list would ask for RSA first."""
    fake_host.present(asyncssh.generate_private_key("ssh-rsa"), fake_host.host_key)
    result = await to_host_key(hass, ready, fake_host)
    names = result["description_placeholders"]
    assert (names["algorithm"], names["key_file"]) == ("ssh-ed25519", "ssh_host_ed25519_key.pub")
    assert names["fingerprint"] == fake_host.host_key.get_fingerprint("sha256")
    result = await finish(hass, await log_in(hass, result, password=PASSWORD, sudo_password=SUDO_PASSWORD))
    assert result["reason"] == "agent_installed"


async def test_a_host_with_an_rsa_key_only(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """A host with only an RSA key shows it (signed with SHA-2: ssh-rsa is not asked for) and names its file."""
    rsa = asyncssh.generate_private_key("ssh-rsa")
    fake_host.present(rsa)
    result = await to_host_key(hass, ready, fake_host)
    names = result["description_placeholders"]
    assert (names["algorithm"], names["key_file"]) == ("ssh-rsa", "ssh_host_rsa_key.pub")
    assert names["fingerprint"] == rsa.get_fingerprint("sha256")
    result = await finish(hass, await log_in(hass, result, password=PASSWORD, sudo_password=SUDO_PASSWORD))
    assert result["reason"] == "agent_installed"


async def test_a_host_without_a_key_type_the_install_asks_for(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """A host that offers none of the types (here a DSA key only) is refused at the first step with ssh_failed: no
    key shown, no login, nothing asked of the hub (ADR-0186)."""
    fake_host.present(asyncssh.generate_private_key("ssh-dss"))
    result = await to_host_key(hass, ready, fake_host)
    assert result["type"] is FlowResultType.FORM and result["step_id"] == "user"
    assert result["errors"] == {"base": "ssh_failed"}
    assert fake_host.logins == [] and fake_host.commands == [] and fake_hub.download_requests == []


# --- the login


async def test_wrong_password_then_the_right_one(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    caplog: pytest.LogCaptureFixture,
    hass_storage: dict[str, Any],
) -> None:
    """A wrong password is a form error the person corrects; the secret fields are never prefilled."""
    result = await log_in(hass, await to_host_key(hass, ready, fake_host), password="wrong-Secret-1")
    assert result["type"] is FlowResultType.FORM and result["errors"] == {"base": "ssh_auth_failed"}
    defaults = [k.default() for k in result["data_schema"].schema if callable(getattr(k, "default", None))]
    assert defaults == []
    assert fake_host.commands == []
    result = await hass.config_entries.subentries.async_configure(
        result["flow_id"], {"password": PASSWORD, "sudo_password": SUDO_PASSWORD}
    )
    result = await finish(hass, result)
    assert result["reason"] == "agent_installed"
    await no_secret_left(hass, ready, fake_hub, caplog, hass_storage, "wrong-Secret-1")


@pytest.mark.parametrize(
    ("setup", "creds", "errors"),
    [
        ("sudo_denied", {"password": PASSWORD}, {"sudo_password": "sudo_failed"}),
        ("sudo_wrong", {"password": PASSWORD, "sudo_password": "not-it"}, {"sudo_password": "sudo_failed"}),
        ("key_login", {}, {"sudo_password": "sudo_needs_password"}),
        ("none", {"ssh_key": "-----BEGIN NOTHING-----"}, {"ssh_key": "ssh_key_invalid"}),
        ("none", {"ssh_key_passphrase": "x"}, {"base": "credentials_missing"}),
        ("none", {"password": PASSWORD, "sudo_password": "two\nlines"}, {"sudo_password": "sudo_password_invalid"}),
    ],
)
async def test_login_refusals_the_person_can_correct(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    setup: str,
    creds: dict[str, str],
    errors: dict[str, str],
) -> None:
    if setup == "sudo_denied":
        fake_host.set(sudo_denied="")
    if setup == "key_login":
        key = asyncssh.generate_private_key("ssh-ed25519")
        fake_host.keys = {"admin": [key]}
        creds = {"ssh_key": key.export_private_key().decode()}
    result = await log_in(hass, await to_host_key(hass, ready, fake_host), **creds)
    assert result["type"] is FlowResultType.FORM and result["errors"] == errors
    assert_nothing_ran(fake_host, fake_hub)
    assert fake_hub.download_requests == []
    if setup == "sudo_wrong":
        assert fake_host.stdins[2] == "not-it\n"  # sudo's check read the password from stdin


@pytest.mark.parametrize("password", [PASSWORD, "wrong-Secret-1"])
async def test_the_login_step_empties_the_credentials(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_host: FakeHost,
    monkeypatch: pytest.MonkeyPatch,
    password: str,
) -> None:
    """The credentials the login form made are emptied as soon as the login answered, whether the flow goes on to the
    install or back to the form: none of them waits in the step for the garbage collector (ADR-0161)."""
    made: list[ai.Credentials] = []
    real = ai.check_credentials

    def spy(user_input: dict[str, Any]) -> tuple[ai.Credentials | None, dict[str, str]]:
        creds, errors = real(user_input)
        if creds is not None:
            made.append(creds)
        return creds, errors

    monkeypatch.setattr(ai, "check_credentials", spy)
    result = await to_host_key(hass, ready, fake_host)
    result = await log_in(hass, result, password=password, sudo_password=SUDO_PASSWORD)
    assert len(made) == 1 and made[0] == ai.Credentials()
    if password == PASSWORD:
        assert (await finish(hass, result))["reason"] == "agent_installed"
    else:
        assert result["type"] is FlowResultType.FORM and result["errors"] == {"base": "ssh_auth_failed"}


async def test_no_sudo_ends_the_flow(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    fake_host.without("sudo")
    result = await log_in(hass, await to_host_key(hass, ready, fake_host), password=PASSWORD)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "no_sudo"
    assert_nothing_ran(fake_host, fake_hub)


# --- what the host is


@pytest.mark.parametrize(
    ("change", "reason", "names"),
    [
        ({"os": "FreeBSD"}, "not_linux", {"os": "FreeBSD"}),
        ({"machine": "i686"}, "unknown_arch", {"machine": "i686"}),
        ({"machine": "armv6l"}, "unknown_arch", {"machine": "armv6l"}),
        ({"no": "systemd"}, "no_systemd", {}),
        ({"without": "apt-get"}, "no_apt", {}),
        ({"without": "curl wget"}, "no_download_tool", {}),
        ({"without": "sha256sum"}, "missing_tools", {"tools": "sha256sum"}),
        ({"without": "systemd-run timeout"}, "missing_tools", {"tools": "timeout, systemd-run"}),
    ],
)
async def test_probe_refusals(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    change: dict[str, str],
    reason: str,
    names: dict[str, str],
) -> None:
    """A host the agent does not run on ends the flow before the hub is asked and before anything changes."""
    if "without" in change:
        fake_host.without(*change["without"].split())
    elif "no" in change:
        fake_host.systemd.rmdir()
    else:
        fake_host.set(**change)
    result = await log_in(hass, await to_host_key(hass, ready, fake_host), password=PASSWORD)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == reason
    assert result["description_placeholders"] == names
    assert len(fake_host.commands) == 1  # the probe, nothing else
    assert fake_hub.download_requests == []
    assert_nothing_ran(fake_host, fake_hub)


# --- what the hub says


@pytest.mark.parametrize(
    ("code", "reason", "names"),
    [
        ("role_forbids", "role_forbids", {"client": "homeassistant"}),
        ("no_release", "no_release", {}),
        ("too_many_downloads", "too_many_downloads", {}),
        ("release_incomplete", "release_incomplete", {}),
        ("some_future_code", "hub_refused", {"code": "some_future_code"}),
    ],
)
async def test_hub_refusals(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    code: str,
    reason: str,
    names: dict[str, str],
) -> None:
    fake_hub.download_code = code
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == reason and result["description_placeholders"] == names
    assert len(fake_hub.download_requests) == 1
    assert_nothing_ran(fake_host, fake_hub)


@pytest.mark.parametrize("change", ["other_signer", "tampered_manifest", "no_platform"])
async def test_release_checked_before_anything_reaches_the_host(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost, change: str
) -> None:
    """A release not signed with the operator key the person entered, one changed after signing, or one without this
    platform: refused in Home Assistant, the download unused, nothing run on the host."""
    if change == "other_signer":
        fake_hub.release = make_release(Ed25519PrivateKey.generate(), {("linux", "amd64"): AGENT})
        reason = "manifest_signature_invalid"
    elif change == "tampered_manifest":
        release = make_release(OPERATOR, {("linux", "amd64"): AGENT})
        release.manifest = release.manifest.replace(b'"size": ', b'"size": 1')
        fake_hub.release = release
        reason = "manifest_signature_invalid"
    else:
        fake_hub.release = make_release(OPERATOR, {("linux", "arm64"): AGENT})
        reason = "no_binary_for_platform"
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == reason
    assert_nothing_ran(fake_host, fake_hub)


async def test_an_older_release_than_one_taken_before_is_refused(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    hass_storage: dict[str, Any],
) -> None:
    """Every release the operator signed stays valid. Once an install took 1.4.0, a hub that hands out 1.3.0 (or a hand
    build) again is refused before anything reaches the host; 1.4.0 itself and newer ones are taken, and the newest is
    kept until the entry is deleted."""
    store = ai.release_store_key(ready.entry_id)

    async def again(version: str) -> dict[str, Any]:
        fake_hub.release = make_release(OPERATOR, {("linux", "amd64"): AGENT}, version=version)
        fake_hub.tokens.clear()
        fake_host.unset("agent_args")
        return await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)

    assert (await again("1.4.0"))["reason"] == "agent_installed"
    assert hass_storage[store]["data"] == {"version": "1.4.0"}
    for old in ("1.3.0", "1.3.9", "1.5.0-dev"):
        result = await again(old)
        assert result["reason"] == "release_too_old"
        assert result["description_placeholders"] == {"version": old, "newest": "1.4.0"}
        assert fake_host.file("agent-args") is None and not any(t["used"] for t in fake_hub.tokens.values())
    assert sum("vordr-install" in c for c in fake_host.commands) == 1
    assert (await again("1.4.0"))["reason"] == "agent_installed"
    assert (await again("1.10.0"))["reason"] == "agent_installed"
    assert hass_storage[store]["data"] == {"version": "1.10.0"}
    assert (await again("1.9.0"))["reason"] == "release_too_old"
    assert await hass.config_entries.async_remove(ready.entry_id)
    assert store not in hass_storage


# --- on the host


@pytest.mark.parametrize(
    ("served", "reason"),
    [("longer", "download_too_large"), ("shorter", "size_mismatch"), ("other", "hash_mismatch")],
)
async def test_binary_checked_on_the_host_before_it_runs(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost, served: str, reason: str
) -> None:
    """The hub serves other bytes than the signed manifest names: the host stops a longer download, compares size and
    SHA-256 and runs nothing; the binary goes with the temporary folder, the CA is not put in place."""
    file = "vordr-agent-linux-amd64"
    good = fake_hub.release.served[file]
    fake_hub.release.served[file] = {
        "longer": good + b"\n# more\n",
        "shorter": good[:-8],
        "other": good.replace(b"tests", b"TESTS"),
    }[served]
    assert served != "other" or len(fake_hub.release.served[file]) == len(good)
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == reason
    assert fake_host.file("agent-args") is None
    assert fake_host.leftovers() == []
    assert not fake_host.ca_path.exists()


async def test_download_refused_by_the_hub(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    fake_hub.download_refusal = (409, "token_used")
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == "download_refused"
    assert result["description_placeholders"] == {"http": "409", "reason": "token_used"}
    assert fake_host.file("agent-args") is None and fake_host.leftovers() == []


async def test_a_host_that_gets_no_connection_to_the_hub(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """curl gets no connection to the hub (a firewall between them): the download stops with its own code and the
    hub's address instead of a general failure, and nothing runs."""
    if not fake_host.has("curl"):
        pytest.skip("no curl on this machine")
    real = (fake_host.bin / "curl").resolve()
    fake_host.replace("curl", CURL_TO_PORT.replace("REAL", str(real)).replace("PORT", "1"))
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == "download_unreachable"
    assert result["description_placeholders"] == {
        "tool": "curl",
        "exit": "7",
        "hub": f"wss://127.0.0.1:{fake_hub.port}/v1/tunnel",
    }
    assert fake_host.file("agent-args") is None and fake_host.leftovers() == []


@pytest.mark.parametrize("target", ["refused", "no_tls"])
async def test_wget_failures_stay_download_failed(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost, target: str
) -> None:
    """The real wget exits 4 both for a refused connection and for a port that speaks no TLS: it cannot say which, so
    both stay download_failed with the exit code, and nothing runs."""
    if not fake_host.has("wget"):
        pytest.skip("no wget on this machine")
    fake_host.without("curl")
    port = "1" if target == "refused" else str(fake_host.port)
    real = (fake_host.bin / "wget").resolve()
    fake_host.replace("wget", WGET_TO_PORT.replace("REAL", str(real)).replace("PORT", port))
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == "download_failed"
    assert result["description_placeholders"] == {"tool": "wget", "exit": "4"}
    assert fake_host.file("agent-args") is None and fake_host.leftovers() == []


async def test_a_connection_to_something_else_stays_a_download_failure(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """curl reaches the address, but what answers speaks no TLS: a connection was made, so it is no firewall case."""
    if not fake_host.has("curl"):
        pytest.skip("no curl on this machine")
    real = (fake_host.bin / "curl").resolve()
    fake_host.replace("curl", CURL_TO_PORT.replace("REAL", str(real)).replace("PORT", str(fake_host.port)))
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == "download_failed"
    assert result["description_placeholders"]["tool"] == "curl"
    assert fake_host.file("agent-args") is None and fake_host.leftovers() == []


@pytest.mark.parametrize("tool", ["curl", "old_curl", "wget"])
async def test_download_bounded_by_the_signed_size(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    monkeypatch: pytest.MonkeyPatch,
    tool: str,
) -> None:
    """A hub that sends 20 MiB more than the signed size: the host stops the download at about that size (curl refuses
    a longer answer; a file size limit stops an older curl and wget) instead of filling its disk, and runs nothing."""
    name = "wget" if tool == "wget" else "curl"
    if not fake_host.has(name):
        pytest.skip(f"no {name} on this machine")
    fake_host.without("curl" if name == "wget" else "wget")
    if tool == "old_curl":
        real = (fake_host.bin / "curl").resolve()
        fake_host.replace("curl", CURL_WITHOUT_MAX_FILESIZE.replace("REAL", str(real)))
    seen = outputs(monkeypatch)
    file = "vordr-agent-linux-amd64"
    good = fake_hub.release.served[file]
    fake_hub.release.served[file] = good + b"\0" * (20 << 20)
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == "download_too_large"
    assert result["description_placeholders"] == {"tool": name}
    stop = ai.line_fields(seen[-1].strip().splitlines()[-1]) or {}
    assert stop["code"] == "download_too_large" and int(stop["size"]) <= len(good) + 1024, stop
    if tool == "curl":
        assert stop["size"] == "0"  # curl saw the hub's Content-Length and did not start
    assert fake_host.file("agent-args") is None and fake_host.leftovers() == []
    assert not fake_host.ca_path.exists()


async def test_another_ca_on_the_host(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """A hub CA file of another content is there: compared, not overwritten, nothing run."""
    fake_host.ca_path.parent.mkdir()
    fake_host.ca_path.write_text("another CA\n", encoding="utf-8")
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == "ca_differs"
    assert result["description_placeholders"] == {"ca_path": str(fake_host.ca_path)}
    assert fake_host.ca_path.read_text(encoding="utf-8") == "another CA\n"
    assert fake_host.file("agent-args") is None and fake_host.leftovers() == []


@pytest.mark.parametrize(
    ("exit_code", "out", "reason", "names", "ca_kept"),
    [
        ("2", "prose\nresult=refused code=config_differs\n", "agent_config_differs", {}, False),
        ("2", "result=refused code=hub_unreachable stage=tls\n", "agent_hub_unreachable", {"stage": "tls"}, False),
        ("2", "result=refused code=not_root\n", "agent_refused", {"code": "not_root"}, False),
        ("1", "result=failed code=install_failed\n", "agent_install_failed", {}, True),
        ("1", "result=failed code=something_new\n", "agent_failed", {"code": "something_new"}, True),
        ("1", "result=failed code=identity_unreadable\n", "agent_identity_unreadable", {}, True),
        ("2", "usage: vordr-agent <command>\n", "agent_no_result", {"exit": "2"}, False),
    ],
)
async def test_agent_refused_or_failed(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    exit_code: str,
    out: str,
    reason: str,
    names: dict[str, str],
    ca_kept: bool,
) -> None:
    """vordr-agent install's own refusal or failure shows with its code and the next step. After a refusal (exit 2,
    nothing changed) the CA the install put there goes again; after a failure it stays for the agent.yaml that may
    name it. The temporary folder goes in every case."""
    fake_host.set(agent_exit=exit_code, agent_out=out)
    result = await install(hass, ready, fake_host, password=PASSWORD, sudo_password=SUDO_PASSWORD)
    assert result["reason"] == reason
    if reason == "agent_hub_unreachable":
        names = {**names, "hub": f"wss://127.0.0.1:{fake_hub.port}/v1/tunnel"}
    assert result["description_placeholders"] == names
    assert fake_host.file("agent-args") is not None
    assert fake_host.ca_path.exists() is ca_kept
    assert fake_host.ca_path.parent.exists() is ca_kept  # a folder the install made goes with its CA
    assert fake_host.leftovers() == []


async def test_closing_the_dialog_during_the_install(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    caplog: pytest.LogCaptureFixture,
    hass_storage: dict[str, Any],
) -> None:
    """The person closes the dialog while the agent runs: the flow drops the connection and every secret at once; the
    host finishes on its own and removes its temporary folder."""
    fake_host.set(agent_hold="")
    result = await log_in(
        hass, await to_host_key(hass, ready, fake_host), password=PASSWORD, sudo_password=SUDO_PASSWORD
    )
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    held = handler(hass, result["flow_id"])._install
    await wait_until(lambda: fake_host.file("agent-args") is not None, timeout=20)
    hass.config_entries.subentries.async_abort(result["flow_id"])
    await hass.async_block_till_done()
    assert held._conn is None and held._sudo_password == ""
    fake_host.unset("agent_hold")
    await wait_until(lambda: fake_host.leftovers() == [], timeout=20, describe=fake_host.leftovers)
    await no_secret_left(hass, ready, fake_hub, caplog, hass_storage)


async def test_closing_the_dialog_during_the_login(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """The person closes the dialog while the login still probes the host (e.g. having noticed the wrong host): the
    connection is closed at once, the hub is not asked and nothing runs on the host. The step that was waiting ends
    as an abort, which Home Assistant takes from a flow that is gone."""
    fake_host.replace(
        "uname",
        '#!/bin/sh\nwhile [ -e "$SIM/probe-hold" ]; do sleep 0.05; done\n'
        'case $1 in -s) cat "$SIM/os" ;; -m) cat "$SIM/machine" ;; *) cat "$SIM/os" ;; esac\n',
    )
    fake_host.set(probe_hold="")
    flow = hass.config_entries.subentries
    result = await to_host_key(hass, ready, fake_host)
    flow_id = result["flow_id"]
    result = await flow.async_configure(flow_id, {"next_step_id": "login"})
    held = flow._progress[flow_id]._install
    pending = asyncio.ensure_future(
        flow.async_configure(flow_id, {"password": PASSWORD, "sudo_password": SUDO_PASSWORD})
    )
    try:
        await wait_until(lambda: len(fake_host.commands) == 1, timeout=20)  # the probe runs and waits on the host
        flow.async_abort(flow_id)
        await wait_until(pending.done, timeout=10)  # the login ended while the probe still waits
    finally:
        fake_host.unset("probe_hold")
    result = pending.result()
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "install_cancelled"
    assert held.ended and held._conn is None and held._sudo_password == ""
    await hass.async_block_till_done()
    await asyncio.sleep(0.5)  # time for an install that should not start
    assert fake_hub.download_requests == []
    assert len(fake_host.commands) == 1  # the probe, nothing else
    assert_nothing_ran(fake_host, fake_hub)


async def test_nothing_runs_on_the_host_once_the_install_ended(
    hass: HomeAssistant,
    ready: MockConfigEntry,
    fake_hub: FakeHub,
    fake_host: FakeHost,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The install ends (clear()) while the hub answers, before its task is cancelled: it checks once more before
    anything runs on the host, and stops there."""
    result = await to_host_key(hass, ready, fake_host)
    held = handler(hass, result["flow_id"])._install
    client = ready.runtime_data.client
    real = client.agent_download

    async def answer_then_end(os_name: str, arch: str) -> dict[str, Any]:
        got = await real(os_name, arch)
        held.clear()
        return got

    monkeypatch.setattr(client, "agent_download", answer_then_end)
    result = await finish(hass, await log_in(hass, result, password=PASSWORD, sudo_password=SUDO_PASSWORD))
    assert result["reason"] == "install_cancelled"
    assert len(fake_hub.download_requests) == 1
    assert_nothing_ran(fake_host, fake_hub)


async def test_a_host_that_prints_too_much(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost
) -> None:
    """A host that answers the probe with a flood (a confirmed host key does not make a host trustworthy): Home
    Assistant reads at most ssh.MAX_OUTPUT characters, closes the command and ends the flow with its own code."""
    fake_host.replace("uname", "#!/bin/sh\nhead -c 4000000 /dev/zero | tr '\\0' a\n")
    result = await log_in(hass, await to_host_key(hass, ready, fake_host), password=PASSWORD)
    assert result["type"] is FlowResultType.ABORT and result["reason"] == "host_output_too_long"
    assert len(fake_host.commands) == 1 and fake_hub.download_requests == []
    assert_nothing_ran(fake_host, fake_hub)


async def test_run_reads_a_bounded_output(
    hass: HomeAssistant, fake_host: FakeHost, monkeypatch: pytest.MonkeyPatch
) -> None:
    """ssh.run reads exactly ssh.MAX_OUTPUT characters whole; one more ends the command with host_output_too_long.
    stderr is not read at all: a flood there changes nothing."""
    stderr_targets: list[Any] = []
    create_process = asyncssh.SSHClientConnection.create_process

    async def spy(self: asyncssh.SSHClientConnection, *args: Any, **kwargs: Any) -> Any:
        stderr_targets.append(kwargs.get("stderr"))
        return await create_process(self, *args, **kwargs)

    monkeypatch.setattr(asyncssh.SSHClientConnection, "create_process", spy)
    fake_host.users = {"admin": PASSWORD}
    code, key = await ssh.host_key("127.0.0.1", fake_host.port)
    assert code == ssh.OK and key is not None
    code, conn = await ssh.connect("127.0.0.1", fake_host.port, "admin", key, PASSWORD, None)
    assert code == ssh.OK and conn is not None
    try:
        n = ssh.MAX_OUTPUT
        ran = await ssh.run(conn, f"head -c {n} /dev/zero | tr '\\0' a; exit 3", timeout=30)
        assert (ran.code, ran.exit_status, ran.stdout) == (ssh.OK, 3, "a" * n)
        ran = await ssh.run(conn, f"head -c {n + 1} /dev/zero | tr '\\0' a", timeout=30)
        assert ran == ssh.Ran(ssh.OUTPUT_TOO_LONG)
        ran = await ssh.run(conn, "head -c 4000000 /dev/zero >&2; echo done", stdin="x\n", timeout=30)
        assert (ran.code, ran.exit_status, ran.stdout) == (ssh.OK, 0, "done\n")
        ran = await ssh.run(conn, "sleep 2", timeout=0.5)
        assert ran == ssh.Ran(ssh.TIMEOUT)
    finally:
        await ssh.close(conn)
    assert stderr_targets == [asyncssh.DEVNULL] * 4


@pytest.mark.parametrize("login", ["password", "key"])
async def test_the_connection_keeps_no_credentials(
    hass: HomeAssistant, ready: MockConfigEntry, fake_hub: FakeHub, fake_host: FakeHost, login: str
) -> None:
    """While the install runs, neither the connection nor its options nor its client hold the login password or the
    private key: asyncssh got them once, when it asked (ssh._Login)."""
    fake_host.set(agent_hold="")
    key = asyncssh.generate_private_key("ssh-ed25519")
    if login == "key":
        fake_host.keys = {"admin": [key]}
        creds = {"ssh_key": key.export_private_key().decode(), "sudo_password": SUDO_PASSWORD}
    else:
        creds = {"password": PASSWORD, "sudo_password": SUDO_PASSWORD}
    result = await log_in(hass, await to_host_key(hass, ready, fake_host), **creds)
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    held = handler(hass, result["flow_id"])._install
    await wait_until(lambda: fake_host.file("agent-args") is not None, timeout=20)
    conn = held._conn
    assert conn is not None and ("admin", login) in fake_host.logins  # logged in with exactly that
    found = [
        (where, name)
        for where, obj in (("connection", conn), ("options", conn._options), ("client", conn._owner))
        for name, value in vars(obj).items()
        for v in (value if isinstance(value, list | tuple) else [value])
        if (isinstance(v, str) and v == PASSWORD) or getattr(v, "public_data", None) == key.public_data
    ]
    assert found == []
    fake_host.unset("agent_hold")
    assert (await finish(hass, result))["reason"] == "agent_installed"
