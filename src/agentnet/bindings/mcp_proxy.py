"""Credential-free stdio MCP proxy over peer-bound bootstrap IPC."""

from __future__ import annotations

import json
import os
import socket
import stat
import threading
import time
from pathlib import Path
from typing import Any, Protocol

from agentnet.bindings.mcp import create_mcp_binding
from agentnet.bindings.mcp_bootstrap import MCP_BOOTSTRAP_ASSURANCE
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import canonical_json


MAX_LOCATOR_BYTES = 4096
MAX_RESPONSE_BYTES = 1_048_576
LOCATOR_NAME = "mcp-bootstrap-locator.json"


class _PipeConnection(Protocol):
    def sendall(self, payload: bytes) -> None: ...
    def recv(self, length: int) -> bytes: ...
    def close(self) -> None: ...


class _WindowsPipeConnection:
    def __init__(self, endpoint: str) -> None:
        try:
            import win32con
            import win32file
            import win32pipe
        except ImportError as exc:
            raise AuthenticationError("Windows MCP named-pipe support is unavailable") from exc
        if not endpoint.startswith(r"\\.\pipe\agentnet-mcp-"):
            raise AuthenticationError("Windows MCP named-pipe locator is invalid")
        win32pipe.WaitNamedPipe(endpoint, 10_000)
        self._win32file = win32file
        self._handle = win32file.CreateFile(
            endpoint,
            win32con.GENERIC_READ | win32con.GENERIC_WRITE,
            0,
            None,
            win32con.OPEN_EXISTING,
            0,
            None,
        )

    def sendall(self, payload: bytes) -> None:
        self._win32file.WriteFile(self._handle, payload)
        self._win32file.FlushFileBuffers(self._handle)

    def recv(self, length: int) -> bytes:
        _status, payload = self._win32file.ReadFile(self._handle, length)
        return bytes(payload)

    def close(self) -> None:
        handle, self._handle = self._handle, None
        if handle is not None:
            handle.Close()


def _parse_locator(raw: bytes) -> tuple[str, str]:
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise AuthenticationError("local MCP bootstrap locator is invalid") from exc
    if (
        not isinstance(value, dict)
        or set(value) != {"generation", "schema", "socket_path"}
        or value["schema"] != "agentnet.mcp.bootstrap-locator.v1"
        or not isinstance(value["socket_path"], str)
        or not value["socket_path"]
        or not isinstance(value["generation"], str)
        or not 24 <= len(value["generation"]) <= 128
        or canonical_json(value) != raw
    ):
        raise AuthenticationError("local MCP bootstrap locator schema rejected")
    return value["socket_path"], value["generation"]


def read_bootstrap_locator(*, timeout_seconds: float = 10.0) -> tuple[str, str]:
    """Read an owner-only nonsecret socket locator from the private runtime state."""

    state_raw = os.environ.get("AGENTNET_STATE_DIR")
    if not state_raw:
        raise AuthenticationError("local MCP runtime state is absent")
    state_dir = Path(state_raw)
    if os.name == "nt":
        from agentnet.windows_security import read_private_file, require_private_path

        require_private_path(state_dir, directory=True)
        locator = state_dir / LOCATOR_NAME
        deadline = time.monotonic() + timeout_seconds
        while True:
            if not locator.exists():
                if time.monotonic() >= deadline:
                    raise AuthenticationError("local MCP bootstrap locator was not published") from None
                time.sleep(0.01)
                continue
            return _parse_locator(read_private_file(locator, max_bytes=MAX_LOCATOR_BYTES))
    try:
        state = state_dir.lstat()
    except OSError as exc:
        raise AuthenticationError("local MCP runtime state is unavailable") from exc
    if (
        state_dir.is_symlink()
        or not stat.S_ISDIR(state.st_mode)
        or state.st_uid != os.geteuid()
        or state.st_mode & 0o077
    ):
        raise AuthenticationError("local MCP runtime state ownership or mode rejected")
    locator = state_dir / LOCATOR_NAME
    deadline = time.monotonic() + timeout_seconds
    while True:
        try:
            descriptor = os.open(locator, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        except FileNotFoundError:
            if time.monotonic() >= deadline:
                raise AuthenticationError("local MCP bootstrap locator was not published") from None
            time.sleep(0.01)
            continue
        except OSError as exc:
            raise AuthenticationError("local MCP bootstrap locator is unavailable") from exc
        try:
            metadata = os.fstat(descriptor)
            if (
                not stat.S_ISREG(metadata.st_mode)
                or metadata.st_uid != os.geteuid()
                or metadata.st_nlink != 1
                or metadata.st_mode & 0o077
                or not 2 <= metadata.st_size <= MAX_LOCATOR_BYTES
            ):
                raise AuthenticationError("local MCP bootstrap locator metadata rejected")
            raw = os.read(descriptor, MAX_LOCATOR_BYTES + 1)
            if len(raw) != metadata.st_size:
                raise AuthenticationError("local MCP bootstrap locator changed during read")
        finally:
            os.close(descriptor)
        return _parse_locator(raw)


class PeerBoundRemoteDispatcher:
    """Canonical dispatcher whose actor is derived by the bootstrap server."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._connection: _PipeConnection | None = None
        self._generation: str | None = None
        self._connect()

    def _connect(self) -> None:
        socket_path, generation = read_bootstrap_locator()
        if os.name == "nt":
            connection: _PipeConnection = _WindowsPipeConnection(socket_path)
        else:
            path = Path(socket_path)
            try:
                metadata = path.stat()
            except OSError as exc:
                raise AuthenticationError("local MCP bootstrap socket is unavailable") from exc
            if (
                not stat.S_ISSOCK(metadata.st_mode)
                or metadata.st_uid != os.geteuid()
                or metadata.st_mode & 0o077
            ):
                raise AuthenticationError("local MCP bootstrap socket ownership or mode rejected")
            unix_connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            unix_connection.settimeout(10.0)
            unix_connection.connect(socket_path)
            connection = unix_connection
        self._connection = connection
        try:
            accepted = self._receive_frame()
        except (OSError, ValidationError) as exc:
            self._invalidate()
            raise AuthenticationError("local MCP bootstrap transport is unavailable") from exc
        if accepted != {
            "assurance": MCP_BOOTSTRAP_ASSURANCE,
            "generation": generation,
            "ok": True,
            "schema": "agentnet.mcp.bootstrap-accepted.v1",
        }:
            self._invalidate()
            raise AuthenticationError("local MCP bootstrap peer was rejected")
        self._generation = generation

    def _invalidate(self) -> None:
        connection, self._connection = self._connection, None
        self._generation = None
        if connection is not None:
            connection.close()

    @staticmethod
    def _receive_exact(connection: socket.socket, length: int) -> bytes:
        result = bytearray()
        while len(result) < length:
            chunk = connection.recv(length - len(result))
            if not chunk:
                raise ValidationError("local MCP bootstrap response ended early")
            result.extend(chunk)
        return bytes(result)

    def _receive_frame(self) -> dict[str, Any]:
        connection = self._connection
        if connection is None:
            raise ValidationError("local MCP bootstrap connection is absent")
        length = int.from_bytes(self._receive_exact(connection, 4), "big")
        if length < 2 or length > MAX_RESPONSE_BYTES:
            raise ValidationError("local MCP bootstrap response length is invalid")
        raw = self._receive_exact(connection, length)
        try:
            response = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValidationError("local MCP bootstrap response is invalid") from exc
        if not isinstance(response, dict) or canonical_json(response) != raw:
            raise ValidationError("local MCP bootstrap response is not canonical")
        return response

    def call(self, method: str, arguments: dict[str, Any]) -> Any:
        payload = canonical_json({"arguments": arguments, "method": method})
        with self._lock:
            if self._connection is None:
                self._connect()
            connection = self._connection
            assert connection is not None
            try:
                connection.sendall(len(payload).to_bytes(4, "big") + payload)
                response = self._receive_frame()
            except (OSError, ValidationError) as exc:
                self._invalidate()
                raise AuthenticationError(
                    "local MCP call outcome is unknown after transport failure; retry a new call"
                ) from exc
        if response.get("ok") is not True or set(response) != {"ok", "result"}:
            self._invalidate()
            raise AuthenticationError("local MCP bootstrap call was rejected")
        return response["result"]


def main() -> None:
    # The ordinary extension verifies this live module inode through procfs in
    # addition to the interpreter measurement and exact ``python -m`` argv.
    module_descriptor = os.open(
        Path(__file__).resolve(), os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    )
    try:
        dispatcher = PeerBoundRemoteDispatcher()
        create_mcp_binding(dispatcher).run(transport="stdio")
    finally:
        os.close(module_descriptor)


if __name__ == "__main__":
    main()
