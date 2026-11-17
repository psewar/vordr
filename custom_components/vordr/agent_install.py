"""Installing the agent on a new Linux host from Home Assistant, once over SSH (ADR-0161): the pure part.

Checks of what the person entered, the operator keys, the scripts that run on the host and how their output is read,
the check of the signed release the hub hands out. No Home Assistant and no SSH call here: the connection is in ssh.py,
the steps in agent_installer.py and config_flow.py.

What runs on the host reports its state as `key=value` lines of its own, and `vordr-agent install` ends with its
defined last line (`result=installed node=<n> fingerprint=<hex>`, `result=refused code=<c>`, `result=failed code=<c>`);
nothing here reads prose.
"""

from __future__ import annotations

import base64
import binascii
from dataclasses import dataclass, field
import json
import re
import shlex
from typing import Any, Final

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from yarl import URL

from . import protocol as p

OK: Final = "ok"  # every step's code when it went through (ssh.OK too)

# --- options item "Operator keys" (not secret: public keys)

OPTIONS_KEY: Final = "operator_keys"
CONF_OPERATOR_KEY: Final = "operator_key"
CONF_BACKUP_OPERATOR_KEY: Final = "backup_operator_key"

# --- the install form; the secrets are entered only after the host key is confirmed and never stored

CONF_HOST: Final = "host"
CONF_PORT: Final = "port"
CONF_USERNAME: Final = "username"
CONF_NODE: Final = "node"
CONF_PASSWORD: Final = "password"
CONF_SSH_KEY: Final = "ssh_key"
CONF_SSH_KEY_PASSPHRASE: Final = "ssh_key_passphrase"
CONF_SUDO_PASSWORD: Final = "sudo_password"
SECRET_FIELDS: Final = (CONF_PASSWORD, CONF_SSH_KEY, CONF_SSH_KEY_PASSPHRASE, CONF_SUDO_PASSWORD)

DEFAULT_PORT: Final = 22
DEFAULT_USERNAME: Final = "root"

# Errors the forms show beside a field or at the top; every one has its text in options.error.
FORM_ERRORS: Final = (
    "invalid_operator_key",
    "invalid_backup_operator_key",
    "backup_without_operator_key",
    "backup_equals_operator_key",
    "invalid_host",
    "invalid_port",
    "invalid_username",
    "invalid_node",
    "credentials_missing",
    "ssh_key_invalid",
    "sudo_password_invalid",
    "ssh_unreachable",
    "ssh_timeout",
    "ssh_failed",
    "ssh_no_host_key",
    "ssh_auth_failed",
    "ssh_lost",
    "sudo_failed",
    "sudo_needs_password",
)

# What the login step shows in its own form, where the person can correct it; anything else ends the flow.
LOGIN_FORM_ERRORS: Final = {
    "ssh_key_invalid": CONF_SSH_KEY,
    "ssh_auth_failed": "base",
    "ssh_unreachable": "base",
    "ssh_timeout": "base",
    "ssh_failed": "base",
    "ssh_lost": "base",
    "sudo_failed": CONF_SUDO_PASSWORD,
    "sudo_needs_password": CONF_SUDO_PASSWORD,
}

# Where things are on the host. Module constants, read when a command is built, so that the tests can point them into
# a sandbox.
SYSTEMD_DIR = "/run/systemd/system"  # as vordr-agent install checks for systemd
CA_PATH = "/etc/vordr/hub-ca.pem"  # the hub's own CA for the agent (--ca-file); it has to stay where it is

# The agent's tunnel on the hub: the same host and port as /v1/ha (internal/wire/wire.go TunnelPath).
TUNNEL_PATH: Final = "/v1/tunnel"
DOWNLOAD_PREFIX: Final = "/v1/agent-download/"
# Exactly as internal/protocol/manifest.go: the context of the operator's signature over a manifest.
MANIFEST_CONTEXT: Final = b"vordr/agent-manifest/v1\x00"
MAX_BINARY_SIZE: Final = 128 << 20

# uname -m to the platform names of the manifest (build.sh builds linux/amd64, arm64 and arm with GOARM=7).
ARCHES: Final = {
    "x86_64": "amd64",
    "amd64": "amd64",
    "aarch64": "arm64",
    "arm64": "arm64",
    "armv7l": "arm",
    "armv7": "arm",
    "armv8l": "arm",  # 32-bit userland on a 64-bit core runs the GOARM=7 build
}

# A host name, an IPv4 address, or an IPv6 address without brackets.
HOST_PATTERN = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$|^[0-9A-Fa-f:.]{2,45}$")
USERNAME_PATTERN = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._-]{0,31}$")
VERSION_PATTERN = re.compile(r"^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$")
FILE_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
DOWNLOAD_PATH_PATTERN = re.compile(r"^/v1/agent-download/([A-Za-z0-9][A-Za-z0-9._-]{0,127})\?token=[0-9a-f]{32,128}$")
# The whole download URL as it goes to the host: only characters that need no quoting in curl's config file.
DOWNLOAD_URL_PATTERN = re.compile(
    r"^https://(?:[A-Za-z0-9.-]{1,253}|\[[0-9A-Fa-f:.]{2,45}\])(?::[0-9]{1,5})?"
    r"/v1/agent-download/[A-Za-z0-9][A-Za-z0-9._-]{0,127}\?token=[0-9a-f]{32,128}$"
)
# A published release X.Y.Z, exactly as internal/protocol releasePattern; hand builds never compare.
RELEASE_PATTERN = re.compile(r"^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$")
SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
FINGERPRINT_PATTERN = re.compile(r"^[0-9a-f]{64}$")
CODE_PATTERN = re.compile(r"^[a-z][a-z0-9_]{0,47}$")
FIELD_PATTERN = re.compile(r"^([a-z][a-z0-9_-]{0,31})=(\S{0,200})$")
SHOWN_PATTERN = re.compile(r"[^A-Za-z0-9._ -]")


def shown(value: str, limit: int = 32) -> str:
    """Something the host or the hub said, made safe to show as code in a text: one short line of plain characters."""
    return SHOWN_PATTERN.sub("", value)[:limit] or "?"


# --- operator keys


def public_key(text: Any) -> bytes | None:
    """A base64 Ed25519 public key as vordr-release prints it; None for anything else (also for a character outside
    ASCII, such as an invisible one pasted with it)."""
    if not isinstance(text, str) or not text.isascii():
        return None
    try:
        raw = base64.b64decode(text.strip(), validate=True)
    except binascii.Error:  # malformed base64 cannot be told apart without decoding it
        return None
    return raw if len(raw) == 32 else None


def check_operator_keys(user_input: dict[str, Any]) -> tuple[dict[str, str], dict[str, str]]:
    """The options of the item "Operator keys" as they are stored, and the errors by field. Both empty: no key, the
    install item explains it cannot run."""
    key = str(user_input.get(CONF_OPERATOR_KEY) or "").strip()
    backup = str(user_input.get(CONF_BACKUP_OPERATOR_KEY) or "").strip()
    errors: dict[str, str] = {}
    if key and public_key(key) is None:
        errors[CONF_OPERATOR_KEY] = "invalid_operator_key"
    if backup and public_key(backup) is None:
        errors[CONF_BACKUP_OPERATOR_KEY] = "invalid_backup_operator_key"
    elif backup and not key:
        errors[CONF_BACKUP_OPERATOR_KEY] = "backup_without_operator_key"
    elif backup and public_key(backup) == public_key(key):
        errors[CONF_BACKUP_OPERATOR_KEY] = "backup_equals_operator_key"
    if errors:
        return {}, errors
    return {CONF_OPERATOR_KEY: key, CONF_BACKUP_OPERATOR_KEY: backup}, {}


@dataclass(frozen=True)
class OperatorKeys:
    """The operator key and its backup as the person entered them in Home Assistant, never from the hub."""

    key: str
    backup: str = ""

    def raw(self) -> list[bytes]:
        return [k for k in (public_key(self.key), public_key(self.backup)) if k is not None]


def operator_keys(options: dict[str, Any]) -> OperatorKeys | None:
    """The stored operator keys; None when there is none that is valid (the install does not run then)."""
    stored = options.get(OPTIONS_KEY)
    if not isinstance(stored, dict):
        return None
    clean, errors = check_operator_keys(stored)
    if errors or not clean.get(CONF_OPERATOR_KEY):
        return None
    return OperatorKeys(clean[CONF_OPERATOR_KEY], clean[CONF_BACKUP_OPERATOR_KEY])


# --- the target host


@dataclass(frozen=True)
class Target:
    """Where to install, without any secret."""

    host: str
    port: int
    username: str
    node: str  # "" = the host's own name, as vordr-agent install defaults it


def check_target(user_input: dict[str, Any]) -> tuple[Target | None, dict[str, str]]:
    """The first form: host, port, user, node."""
    host = str(user_input.get(CONF_HOST) or "").strip()
    username = str(user_input.get(CONF_USERNAME) or "").strip()
    node = str(user_input.get(CONF_NODE) or "").strip()
    port = user_input.get(CONF_PORT, DEFAULT_PORT)
    errors: dict[str, str] = {}
    if not HOST_PATTERN.fullmatch(host):
        errors[CONF_HOST] = "invalid_host"
    if not isinstance(port, int | float) or not float(port).is_integer() or not 1 <= port <= 65535:
        errors[CONF_PORT] = "invalid_port"
    if not USERNAME_PATTERN.fullmatch(username):
        errors[CONF_USERNAME] = "invalid_username"
    if node and not p.NAME_PATTERN.fullmatch(node):
        errors[CONF_NODE] = "invalid_node"
    if errors:
        return None, errors
    return Target(host, int(port), username, node), {}


@dataclass
class Credentials:
    """What logs in and what sudo needs: only in the flow's memory, dropped with clear()."""

    password: str = ""
    ssh_key: str = ""
    ssh_key_passphrase: str = ""
    sudo_password: str = ""

    def clear(self) -> None:
        self.password = self.ssh_key = self.ssh_key_passphrase = self.sudo_password = ""

    def for_sudo(self) -> str:
        """The password sudo gets: the one given for it, else the login password; "" when there is none."""
        return self.sudo_password or self.password


def check_credentials(user_input: dict[str, Any]) -> tuple[Credentials | None, dict[str, str]]:
    """The second form, after the host key was confirmed: a password or a pasted private key, its passphrase, the
    sudo password."""
    creds = Credentials(
        password=str(user_input.get(CONF_PASSWORD) or ""),
        ssh_key=str(user_input.get(CONF_SSH_KEY) or "").strip(),
        ssh_key_passphrase=str(user_input.get(CONF_SSH_KEY_PASSPHRASE) or ""),
        sudo_password=str(user_input.get(CONF_SUDO_PASSWORD) or ""),
    )
    errors: dict[str, str] = {}
    if not creds.password and not creds.ssh_key:
        errors["base"] = "credentials_missing"
    if "\n" in creds.sudo_password or "\r" in creds.sudo_password:
        errors[CONF_SUDO_PASSWORD] = "sudo_password_invalid"  # sudo reads one line
    if errors:
        creds.clear()
        return None, errors
    return creds, {}


# --- what the host says about itself

# Run as the login user, before anything changes. $1: where systemd shows it runs. Each line key=value.
PROBE_SCRIPT: Final = """exec </dev/null
printf 'os=%s\\n' "$(uname -s 2>/dev/null | tr -d ' ')"
printf 'machine=%s\\n' "$(uname -m 2>/dev/null | tr -d ' ')"
printf 'uid=%s\\n' "$(id -u 2>/dev/null)"
if [ -d "$1" ]; then echo systemd=yes; else echo systemd=no; fi
for t in apt-get curl wget sha256sum base64 mktemp timeout systemd-run sudo; do
  if command -v "$t" >/dev/null 2>&1; then echo "$t=yes"; else echo "$t=no"; fi
done
"""

# timeout bounds wget's whole download; systemd-run runs the install outside the login session (INSTALL_SCRIPT).
TOOLS: Final = ("sha256sum", "base64", "mktemp", "timeout", "systemd-run")


def probe_command() -> str:
    return shlex.join(["sh", "-c", PROBE_SCRIPT, "vordr-probe", SYSTEMD_DIR])


def fields(text: str) -> dict[str, str]:
    """The key=value lines of our scripts' output (one per line); anything else is left out."""
    out: dict[str, str] = {}
    for line in text.splitlines():
        if m := FIELD_PATTERN.fullmatch(line.strip()):
            out[m.group(1)] = m.group(2)
    return out


def line_fields(line: str) -> dict[str, str] | None:
    """One line of space-separated key=value fields (our own or vordr-agent install's last line); None when any part is
    no such field."""
    out: dict[str, str] = {}
    for part in line.split():
        m = FIELD_PATTERN.fullmatch(part)
        if m is None:
            return None
        out[m.group(1)] = m.group(2)
    return out or None


@dataclass(frozen=True)
class Facts:
    """The host as the probe found it."""

    arch: str  # as the manifest names it
    root: bool  # logged in as root: no sudo needed


def check_facts(output: str) -> tuple[str, dict[str, str], Facts | None]:
    """The probe's output: a refusal code with its placeholders, or the facts. In the order a person fixes them: the
    system, the platform, systemd, apt, the tools, root or sudo."""
    f = fields(output)
    if "uid" not in f or "os" not in f:
        return "probe_failed", {}, None
    if f["os"] != "Linux":
        return "not_linux", {"os": shown(f["os"])}, None
    arch = ARCHES.get(f.get("machine", ""))
    if arch is None:
        return "unknown_arch", {"machine": shown(f.get("machine", ""))}, None
    if f.get("systemd") != "yes":
        return "no_systemd", {}, None
    if f.get("apt-get") != "yes":
        return "no_apt", {}, None
    if f.get("curl") != "yes" and f.get("wget") != "yes":
        return "no_download_tool", {}, None
    if missing := [t for t in TOOLS if f.get(t) != "yes"]:
        return "missing_tools", {"tools": ", ".join(missing)}, None
    root = f["uid"] == "0"
    if not root and f.get("sudo") != "yes":
        return "no_sudo", {}, None
    return "ok", {}, Facts(arch=arch, root=root)


# --- sudo, for a login that is not root


class Sudo:
    """How the install becomes root."""

    NONE = "none"  # logged in as root
    NOPASSWD = "nopasswd"  # sudo without a password
    PASSWORD = "password"  # sudo with the password on its stdin


# -k: ignore cached credentials, so that whether a password is asked is the same in the check and in the install.
SUDO_NOPASSWD_CHECK: Final = "sudo -k -n true"
SUDO_PASSWORD_CHECK: Final = "sudo -k -S -p '' true"


def as_root(command: str, sudo: str) -> str:
    """The command as root: as it is, through sudo without a password, or through sudo reading its password from the
    first line of stdin (never on the command line, which sudo logs)."""
    if sudo == Sudo.NOPASSWD:
        return "sudo -k -n -- " + command
    if sudo == Sudo.PASSWORD:
        return "sudo -k -S -p '' -- " + command
    return command


# --- the release from the hub


@dataclass(frozen=True)
class Release:
    """What the install takes from the hub's answer, every part of it checked: the binary's name, hash and size from
    the operator-signed manifest only."""

    version: str
    file: str
    sha256: str
    size: int
    path: str  # with its single-use token: never logged, never stored
    hub_key: str


def signed_by(raw: bytes, sig: bytes, keys: list[bytes]) -> bool:
    """One of the operator keys signed exactly these bytes, under the manifest's context."""
    signed = MANIFEST_CONTEXT + raw
    return len(sig) == 64 and any(_verifies(k, sig, signed) for k in keys)


def parse_manifest(raw: bytes) -> dict[str, Any] | None:
    """The signed manifest, if it has the form internal/protocol VerifyManifest takes; None for any doubt."""
    try:
        m = json.loads(raw)
    except ValueError:  # no JSON: only decoding tells
        return None
    if not isinstance(m, dict) or set(m) - {"version", "issued", "binaries"}:
        return None
    version, binaries = m.get("version"), m.get("binaries")
    if not isinstance(version, str) or not VERSION_PATTERN.fullmatch(version):
        return None
    if not isinstance(binaries, list) or not binaries:
        return None
    for b in binaries:
        if not (
            isinstance(b, dict)
            and not set(b) - {"os", "arch", "file", "sha256", "size"}
            and isinstance(b.get("os"), str)
            and b["os"]
            and isinstance(b.get("arch"), str)
            and b["arch"]
            and isinstance(b.get("file"), str)
            and FILE_PATTERN.fullmatch(b["file"])
            and isinstance(b.get("sha256"), str)
            and SHA256_PATTERN.fullmatch(b["sha256"])
            and type(b.get("size")) is int
            and 0 < b["size"] <= MAX_BINARY_SIZE
        ):
            return None
    return m


def _verifies(key: bytes, sig: bytes, signed: bytes) -> bool:
    try:
        Ed25519PublicKey.from_public_bytes(key).verify(sig, signed)
    except InvalidSignature:  # the signature check answers only by raising
        return False
    return True


def _b64(value: Any) -> bytes | None:
    if not isinstance(value, str) or not value.isascii():
        return None
    try:
        return base64.b64decode(value, validate=True)
    except binascii.Error:  # malformed base64 cannot be told apart without decoding it
        return None


def check_release(answer: dict[str, Any], keys: list[bytes], arch: str) -> tuple[str, Release | None]:
    """The hub's agent_download_result with code ok, checked before anything reaches the host: the operator's signature
    over the manifest with the keys entered in Home Assistant, the binary for linux/arch from that manifest, its
    download path and the hub key."""
    raw, sig = _b64(answer.get("manifest")), _b64(answer.get("manifest_sig"))
    if raw is None or sig is None:
        return "manifest_invalid", None
    if not signed_by(raw, sig, keys):
        return "manifest_signature_invalid", None
    m = parse_manifest(raw)
    if m is None:
        return "manifest_invalid", None
    binary = next((b for b in m["binaries"] if b["os"] == "linux" and b["arch"] == arch), None)
    if binary is None:
        return "no_binary_for_platform", None
    downloads = answer.get("downloads")
    paths = [
        d.get("path")
        for d in (downloads if isinstance(downloads, list) else [])
        if isinstance(d, dict) and d.get("file") == binary["file"] and isinstance(d.get("path"), str)
    ]
    match = DOWNLOAD_PATH_PATTERN.fullmatch(paths[0]) if len(paths) == 1 else None
    if match is None or match.group(1) != binary["file"]:
        return "download_invalid", None
    hub_key = public_key(answer.get("hub_key"))
    if hub_key is None:
        return "download_invalid", None
    return "ok", Release(
        m["version"], binary["file"], binary["sha256"], binary["size"], paths[0], base64.b64encode(hub_key).decode()
    )


# --- the newest release seen: an old release, validly signed, is not taken again


def release_store_key(entry_id: str) -> str:
    """The Store of the newest agent release one config entry's installs took (.storage/vordr.agent_release.<id>)."""
    return f"vordr.agent_release.{entry_id}"


def newer_release(a: str, b: str) -> bool:
    """Release a is newer than release b; False unless both are X.Y.Z (internal/protocol NewerRelease)."""
    pa, pb = RELEASE_PATTERN.fullmatch(a), RELEASE_PATTERN.fullmatch(b)
    if pa is None or pb is None:
        return False
    return tuple(map(int, pa.groups())) > tuple(map(int, pb.groups()))


def stored_release(data: Any) -> str:
    """The newest release kept in the Store; "" when there is none or it is not X.Y.Z."""
    version = data.get("version") if isinstance(data, dict) else None
    return version if isinstance(version, str) and RELEASE_PATTERN.fullmatch(version) else ""


def check_release_age(version: str, newest: str) -> str:
    """ok, or release_too_old. Every manifest the operator signed stays valid, so once an install took release X.Y.Z,
    the next one takes that release or a newer one, as an agent on a release takes only newer ones: a hub that was taken
    over cannot hand new hosts an old release with a known weakness. Going back means publishing the old code under a
    new version."""
    if not newest or version == newest or newer_release(version, newest):
        return OK
    return "release_too_old"


def agent_hub_url(ha_url: str) -> str:
    """The agent's address of the hub: host and port of the integration's /v1/ha, path /v1/tunnel."""
    u = URL(ha_url)
    return str(URL.build(scheme="wss", host=u.host or "", port=u.port, path=TUNNEL_PATH))


def download_url(ha_url: str, path: str) -> str:
    """The binary's single-use download: https at the hub's host and port, followed by the path from the answer."""
    u = URL(ha_url)
    return str(URL.build(scheme="https", host=u.host or "", port=u.port)).rstrip("/") + path


# --- the install on the host, as root

# $1 sha256, $2 size, $3 hub (agent URL), $4 hub key, $5 node or "", $6 operator key, $7 backup key or "", $8 CA path
# or "". stdin: the download URL, then the hub's CA as base64 or an empty line. The URL carries the single-use token:
# it goes from stdin into a file in the private folder that curl (-K) or wget (-i) reads, never on a command line, which
# every user of the host sees in ps and sudo logs. Everything happens in a private temporary folder that goes in every
# case. The download is bounded by the signed size (curl --max-filesize, and ulimit -f for both tools) and in time
# (curl --max-time, timeout for wget). The binary runs only once its size and SHA-256 match the signed manifest. Its own
# lines are key=value; after the agent ran, the line "step=agent exit=<n>" and then the agent's last line.
INSTALL_SCRIPT: Final = """umask 077
sha=$1 size=$2 hub=$3 hubkey=$4 node=$5 opkey=$6 backup=$7 capath=$8
IFS= read -r url || url=
IFS= read -r ca || ca=
exec </dev/null
say() { printf '%s\\n' "$*"; }
case $url in
  *[!][A-Za-z0-9._:/?=-]*) say 'step=input code=bad_input'; exit 2 ;;
  https://*/v1/agent-download/*'?token='*) ;;
  *) say 'step=input code=bad_input'; exit 2 ;;
esac
d=$(mktemp -d 2>/dev/null) && [ -d "$d" ] || { say 'step=temp code=temp_failed'; exit 2; }
trap 'rm -rf "$d"' EXIT
trap 'exit 1' HUP INT TERM PIPE
printf '#!/bin/sh\\nexit 0\\n' > "$d/x" && chmod 700 "$d/x" && "$d/x" 2>/dev/null || {
  say 'step=temp code=temp_noexec'; exit 2; }
cacert=
if [ -n "$ca" ]; then
  printf '%s' "$ca" | base64 -d > "$d/ca.pem" 2>/dev/null && [ -s "$d/ca.pem" ] || {
    say 'step=ca code=ca_failed'; exit 2; }
  cacert=$d/ca.pem
fi
f=$d/vordr-agent
blocks=$((size / 512 + 2))
fsize() { if [ -f "$f" ]; then wc -c < "$f" | tr -d ' '; else echo 0; fi; }
big() { say "step=download code=download_too_large tool=$1 size=$(fsize)"; exit 2; }
if command -v curl >/dev/null 2>&1; then
  printf 'url = "%s"\\n' "$url" > "$d/curl.cfg" || { say 'step=temp code=temp_failed'; exit 2; }
  if [ -n "$cacert" ]; then
    http=$( (ulimit -f "$blocks" && exec curl -sS --proto =https --max-time 900 --max-filesize "$size" \\
      --cacert "$cacert" -o "$f" -w '%{http_code}' -K "$d/curl.cfg") 2>/dev/null)
  else
    http=$( (ulimit -f "$blocks" && exec curl -sS --proto =https --max-time 900 --max-filesize "$size" \\
      -o "$f" -w '%{http_code}' -K "$d/curl.cfg") 2>/dev/null)
  fi
  rc=$?
  if [ "$rc" = 63 ] || [ "$(fsize)" -gt "$size" ]; then big curl; fi
  [ "$rc" = 0 ] || { say "step=download code=download_failed tool=curl exit=$rc"; exit 2; }
  if [ "$http" != 200 ]; then
    reason=$(head -c 32 "$f" 2>/dev/null | tr -cd 'a-z_')
    say "step=download code=download_refused http=$http reason=${reason:-none}"; exit 2
  fi
else
  printf '%s\\n' "$url" > "$d/url" || { say 'step=temp code=temp_failed'; exit 2; }
  if [ -n "$cacert" ]; then
    (ulimit -f "$blocks" && exec timeout 900 wget -q --https-only --timeout=60 --ca-certificate="$cacert" \\
      -O "$f" -i "$d/url") 2>/dev/null
  else
    (ulimit -f "$blocks" && exec timeout 900 wget -q --https-only --timeout=60 -O "$f" -i "$d/url") 2>/dev/null
  fi
  rc=$?
  if [ "$(fsize)" -gt "$size" ]; then big wget; fi
  [ "$rc" = 0 ] || { say "step=download code=download_failed tool=wget exit=$rc"; exit 2; }
fi
got=$(fsize)
[ "$got" = "$size" ] || { say "step=verify code=size_mismatch size=$got"; exit 2; }
sum=$(sha256sum < "$f") && sum=${sum%% *}
[ "$sum" = "$sha" ] || { say 'step=verify code=hash_mismatch'; exit 2; }
chmod 700 "$f"
caarg= cadir= canew= dirnew=
if [ -n "$cacert" ]; then
  if [ -e "$capath" ]; then
    a=$(sha256sum < "$cacert") && b=$(sha256sum < "$capath") || { say 'step=ca code=ca_failed'; exit 2; }
    [ "$a" = "$b" ] || { say 'step=ca code=ca_differs'; exit 2; }
  else
    cadir=${capath%/*}
    if [ ! -d "$cadir" ]; then
      mkdir -m 755 "$cadir" || { say 'step=ca code=ca_failed'; exit 2; }
      dirnew=yes
    fi
    cp "$cacert" "$capath" && chmod 644 "$capath" || {
      rm -f "$capath"; [ "$dirnew" != yes ] || rmdir "$cadir"; say 'step=ca code=ca_failed'; exit 2; }
    canew=yes
  fi
  caarg=$capath
fi
set -- install --hub "$hub" --hub-key "$hubkey" --operator-key "$opkey"
[ -z "$node" ] || set -- "$@" --node "$node"
[ -z "$backup" ] || set -- "$@" --backup-operator-key "$backup"
[ -z "$caarg" ] || set -- "$@" --ca-file "$caarg"
"$f" "$@" > "$d/out" 2> "$d/err"
rc=$?
if [ "$rc" = 2 ] && [ "$canew" = yes ]; then
  rm -f "$capath"
  [ "$dirnew" != yes ] || rmdir "$cadir" 2>/dev/null
fi
say "step=agent exit=$rc"
tail -n 1 "$d/out"
exit "$rc"
"""


# The install runs as a transient service of its own (systemd-run, as root), not in the login's session: closing the
# connection, or logind ending the session with KillUserProcesses=yes, does not stop it halfway; the host finishes on
# its own. --pipe passes stdin and stdout through, --wait returns when it ended, --collect removes the unit even after
# a failure. systemd-run is part of systemd, which the probe requires (--pipe since systemd 235, --collect since 236).
SYSTEMD_RUN: Final = ("systemd-run", "--pipe", "--wait", "--collect", "--quiet", "--description=vordr agent install")


def install_command(release: Release, hub_url: str, node: str, keys: OperatorKeys, with_ca: bool, sudo: str) -> str:
    """The command that installs, as root. Every value is checked before and quoted; the secret parts go on stdin."""
    args = [
        *SYSTEMD_RUN,
        "--",
        "sh",
        "-c",
        INSTALL_SCRIPT,
        "vordr-install",
        release.sha256,
        str(release.size),
        hub_url,
        release.hub_key,
        node,
        keys.key,
        keys.backup,
        CA_PATH if with_ca else "",
    ]
    return as_root(shlex.join(args), sudo)


def install_input(url: str, ca_pem: str, sudo_password: str = "") -> str:
    """stdin of the install: sudo's password if it reads one, the download URL, the hub's CA as base64 or nothing."""
    ca = base64.b64encode(ca_pem.encode()).decode() if ca_pem else ""
    first = f"{sudo_password}\n" if sudo_password else ""
    return f"{first}{url}\n{ca}\n"


# --- the outcome

# vordr-agent install's codes (ADR-0156 of the agent) that need a next step of their own; any other shows with its code.
AGENT_REFUSALS: Final = ("config_invalid", "config_differs", "hub_unreachable", "installer_refused", "prepare_failed")
AGENT_FAILURES: Final = ("install_failed", "identity_unreadable")
# What the install script stops for on its own, before the agent runs.
SCRIPT_CODES: Final = (
    "bad_input",
    "temp_failed",
    "temp_noexec",
    "ca_failed",
    "ca_differs",
    "download_failed",
    "download_refused",
    "download_too_large",
    "size_mismatch",
    "hash_mismatch",
)


@dataclass(frozen=True)
class Outcome:
    """How the install ended: a code that has its own text, and the values the text names."""

    code: str
    names: dict[str, str] = field(default_factory=dict)

    @property
    def installed(self) -> bool:
        return self.code == "agent_installed"


def outcome(stdout: str, version: str) -> Outcome:
    """The install script's output: its own stop, or the agent's last line after "step=agent exit=<n>"."""
    lines = [line.strip() for line in stdout.splitlines() if line.strip()]
    agent_at = next((i for i, line in enumerate(lines) if line.startswith("step=agent ")), None)
    if agent_at is None:
        own = (line_fields(lines[-1]) if lines else None) or {}
        code = own.get("code", "")
        if code not in SCRIPT_CODES:
            return Outcome("install_no_result")
        return Outcome(code, {k: shown(own.get(k, "-")) for k in ("tool", "exit", "http", "reason")})
    exit_code = shown((line_fields(lines[agent_at]) or {}).get("exit", ""), 4)
    last = (line_fields(lines[agent_at + 1]) if agent_at + 1 < len(lines) else None) or {}
    result = last.get("result")
    if result == "installed":
        node, fp = last.get("node", ""), last.get("fingerprint", "")
        if p.NAME_PATTERN.fullmatch(node) and FINGERPRINT_PATTERN.fullmatch(fp):
            return Outcome("agent_installed", {"node": node, "fingerprint": fp, "version": shown(version)})
        return Outcome("agent_no_result", {"exit": exit_code})
    code = last.get("code", "")
    if result not in ("refused", "failed") or not CODE_PATTERN.fullmatch(code):
        return Outcome("agent_no_result", {"exit": exit_code})
    if result == "refused":
        if code in AGENT_REFUSALS:
            return Outcome(f"agent_{code}", {"stage": shown(last.get("stage", "-"))})
        return Outcome("agent_refused", {"code": code})
    if code in AGENT_FAILURES:
        return Outcome(f"agent_{code}")
    return Outcome("agent_failed", {"code": code})


# Every way the install item can end, each with its text in options.abort. With the values its text names.
HUB_CODES: Final = (
    "role_forbids",
    "client_not_approved",
    "no_release",
    "no_binary_for_platform",
    "release_incomplete",
    "too_many_downloads",
    "bad_request",
)
ABORTS: Final = {
    # before anything is entered
    "entry_not_loaded": (),
    "install_needs_operator_key": (),
    "install_needs_ssh_library": (),
    "hub_not_connected": (),
    "hub_without_download": (),
    # the dialog was closed while logging in: nothing more happens (nobody sees this end)
    "install_cancelled": (),
    # a host that prints more than Home Assistant reads, at any step
    "host_output_too_long": (),
    # the host key
    "host_key_rejected": ("host",),
    "ssh_host_key_changed": ("host",),
    # the probe
    "probe_failed": (),
    "not_linux": ("os",),
    "unknown_arch": ("machine",),
    "no_systemd": (),
    "no_apt": (),
    "no_download_tool": (),
    "missing_tools": ("tools",),
    "no_sudo": (),
    # the hub's answer (no_binary_for_platform: the hub's code or the checked manifest's)
    **dict.fromkeys(HUB_CODES, ()),
    "role_forbids": ("client",),
    "hub_refused": ("code",),
    "no_answer": (),
    "manifest_invalid": (),
    "manifest_signature_invalid": (),
    "release_too_old": ("version", "newest"),
    "download_invalid": (),
    # on the host
    "bad_input": (),
    "temp_failed": (),
    "temp_noexec": (),
    "ca_failed": (),
    "ca_differs": ("ca_path",),
    "download_failed": ("tool", "exit"),
    "download_refused": ("http", "reason"),
    "download_too_large": ("tool",),
    "size_mismatch": (),
    "hash_mismatch": (),
    "ssh_lost": (),
    "ssh_timeout": (),
    "install_no_result": (),
    # vordr-agent install
    "agent_installed": ("node", "fingerprint", "version"),
    "agent_config_invalid": (),
    "agent_config_differs": (),
    "agent_hub_unreachable": ("stage", "hub"),
    "agent_installer_refused": (),
    "agent_prepare_failed": (),
    "agent_refused": ("code",),
    "agent_install_failed": (),
    "agent_identity_unreadable": (),
    "agent_failed": ("code",),
    "agent_no_result": ("exit",),
}
