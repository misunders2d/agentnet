"""Credential-free real child used by composed Unix IPC tests."""

from __future__ import annotations

import json
import socket
import sys
from typing import Any

from agentnet.bindings.ipc import build_ipc_frame
from agentnet.security.signatures import canonical_json


def _read_exact(connection: socket.socket, length: int) -> bytes:
    value = bytearray()
    while len(value) < length:
        chunk = connection.recv(length - len(value))
        if not chunk:
            raise RuntimeError("IPC server closed before its complete response")
        value.extend(chunk)
    return bytes(value)


def _roundtrip(instruction: dict[str, Any]) -> dict[str, Any]:
    frame = build_ipc_frame(
        instruction["capability"],
        session_id=instruction["session_id"],
        nonce=instruction["nonce"],
        request=instruction["request"],
    )
    encoded = canonical_json(frame)
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.connect(instruction["socket_path"])
        connection.sendall(len(encoded).to_bytes(4, "big") + encoded)
        length = int.from_bytes(_read_exact(connection, 4), "big")
        return json.loads(_read_exact(connection, length))


for raw_line in sys.stdin:
    try:
        value = json.loads(raw_line)
        if not isinstance(value, dict):
            raise ValueError("instruction is not an object")
        result = _roundtrip(value)
    except Exception as exc:  # pragma: no cover - failure text is parent evidence
        result = {"child_error": type(exc).__name__}
    sys.stdout.write(json.dumps(result, separators=(",", ":"), sort_keys=True) + "\n")
    sys.stdout.flush()
