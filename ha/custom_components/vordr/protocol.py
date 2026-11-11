"""The /v1/ha protocol of vordr-hub (design/ha-integration.md, section 5): keys, the signed hello, message types.

Pure functions only; the connection lives in client.py.
"""

from __future__ import annotations

import base64
import hashlib
import re
from typing import Any, Final

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

PROTOCOL: Final = 2  # 2: codes and limits instead of German texts (H5)
ROLE: Final = "ha"
PATH: Final = "/v1/ha"
NONCE_SIZE: Final = 32
HEARTBEAT_INTERVAL: Final = 30.0
IDLE_TIMEOUT: Final = 90.0  # the hub drops a client that stays silent this long; so do we
HELLO_TIMEOUT: Final = 10.0
MAX_MESSAGE: Final = 4 << 20
# Separates hello signatures from anything else the key signs, exactly as internal/wire/ha.go.
HELLO_CONTEXT: Final = b"vordr/ha-hello/v1\x00"
NAME_PATTERN: Final = re.compile(r"^[a-z0-9][a-z0-9.-]{0,62}$")

# Message types.
T_CHALLENGE: Final = "challenge"
T_HELLO: Final = "hello"
T_STATUS: Final = "status"
T_HEARTBEAT: Final = "heartbeat"
T_SNAPSHOT: Final = "snapshot"
T_HOST: Final = "host"
T_HOST_REMOVED: Final = "host_removed"
T_HUB: Final = "hub"
T_COMMAND: Final = "command"
T_COMMAND_RESULT: Final = "command_result"

# Registration states and reasons of the status message.
STATE_PENDING: Final = "pending_approval"
STATE_APPROVED: Final = "approved"
STATE_REJECTED: Final = "rejected"
REASON_PROTOCOL_MISMATCH: Final = "protocol_mismatch"


def new_private_key() -> str:
    """A fresh Ed25519 key, as the base64 of its 32-byte seed (what the config entry stores)."""
    return base64.b64encode(Ed25519PrivateKey.generate().private_bytes_raw()).decode()


def load_private_key(seed_b64: str) -> Ed25519PrivateKey:
    """Load a key stored by new_private_key."""
    return Ed25519PrivateKey.from_private_bytes(base64.b64decode(seed_b64))


def public_key(key: Ed25519PrivateKey) -> bytes:
    """The raw 32-byte public key."""
    return key.public_key().public_bytes_raw()


def fingerprint(pub: bytes) -> str:
    """Hex SHA-256 of the raw public key, as `vordr-hub clients` shows it."""
    return hashlib.sha256(pub).hexdigest()


def hello(nonce: bytes, name: str, key: Ed25519PrivateKey, version: str) -> dict[str, Any]:
    """The signed hello answering a challenge."""
    pub = public_key(key)
    signed = HELLO_CONTEXT + nonce + b"\x00" + name.encode() + b"\x00" + pub
    return {
        "t": T_HELLO,
        "role": ROLE,
        "name": name,
        "public_key": base64.b64encode(pub).decode(),
        "signature": base64.b64encode(key.sign(signed)).decode(),
        "integration_version": version,
        "protocol": PROTOCOL,
    }


def entity_part(node: str) -> str:
    """The node as it appears in entity ids (vordr_<id>_<object>), as the hub's MQTT side derives it."""
    return node.replace("-", "_").replace(".", "_")
