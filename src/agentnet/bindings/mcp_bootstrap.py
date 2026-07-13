"""Peer-credential bootstrap transport for harness-launched MCP proxies.

The socket path is only a locator.  It carries no authority: the ordinary
extension derives the connecting proxy PID with ``SO_PEERCRED`` and resolves
its direct parent from procfs before selecting a supervisor-registered launch.
"""

from __future__ import annotations

import asyncio
import json
import os
import socket
import stat
import struct
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agentnet.bindings.ipc import linux_process_parent, linux_process_probe
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import canonical_json


@dataclass(frozen=True, slots=True)
class UnixProcessPeer:
    pid: int
    uid: int
    process_start_time: str
    process_measurement: str
    parent_pid: int
    parent_process_start_time: str
    parent_process_measurement: str


PeerBinder = Callable[[UnixProcessPeer], Any]
BoundHandler = Callable[[Any, UnixProcessPeer, dict[str, Any]], Awaitable[dict[str, Any]]]


class UnixMCPBootstrapServer:
    """Bind one MCP connection to a server-derived registered harness launch."""

    def __init__(
        self,
        path: Path,
        *,
        bind_peer: PeerBinder,
        handler: BoundHandler,
        generation: str,
        max_frame: int = 1_048_576,
    ) -> None:
        self.path = path
        self.bind_peer = bind_peer
        self.handler = handler
        self.generation = generation
        self.max_frame = max_frame
        self._server: asyncio.AbstractServer | None = None
        self._socket_identity: tuple[int, int] | None = None
        self._clients: set[asyncio.StreamWriter] = set()
        self.last_request_fields: frozenset[str] | None = None

    async def start(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        parent = self.path.parent.lstat()
        if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.geteuid() or parent.st_mode & 0o077:
            raise AuthenticationError("MCP bootstrap directory ownership or mode rejected")
        if os.path.lexists(self.path):
            raise AuthenticationError("MCP bootstrap path replacement or stale socket detected")
        self._server = await asyncio.start_unix_server(self._handle, path=self.path)
        os.chmod(self.path, 0o600)
        created = self.path.stat()
        self._socket_identity = (created.st_dev, created.st_ino)

    async def close(self) -> None:
        clients = tuple(self._clients)
        for writer in clients:
            writer.close()
            writer.transport.abort()
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None
        if clients:
            await asyncio.sleep(0)
        if os.path.lexists(self.path) and self.path.is_socket() and self._socket_identity is not None:
            current = self.path.stat()
            if (current.st_dev, current.st_ino) == self._socket_identity:
                self.path.unlink()
        self._socket_identity = None

    async def _read_frame(self, reader: asyncio.StreamReader) -> dict[str, Any]:
        prefix = await reader.readexactly(4)
        length = int.from_bytes(prefix, "big")
        if length < 2 or length > self.max_frame:
            raise ValidationError("MCP bootstrap frame length rejected")
        raw = await reader.readexactly(length)
        try:
            value = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValidationError("MCP bootstrap frame is invalid") from exc
        if not isinstance(value, dict) or canonical_json(value) != raw:
            raise ValidationError("MCP bootstrap frame must use exact canonical JSON")
        return value

    @staticmethod
    async def _write_frame(writer: asyncio.StreamWriter, value: dict[str, Any]) -> None:
        body = canonical_json(value)
        writer.write(len(body).to_bytes(4, "big") + body)
        await writer.drain()

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        self._clients.add(writer)
        try:
            sock = writer.get_extra_info("socket")
            if sock is None or not hasattr(socket, "SO_PEERCRED"):
                raise AuthenticationError("platform peer credentials unavailable")
            pid, uid, _gid = struct.unpack(
                "3i", sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12)
            )
            process_start_time, process_measurement = linux_process_probe(pid)
            parent_pid = linux_process_parent(pid)
            parent_start_time, parent_measurement = linux_process_probe(parent_pid)
            peer = UnixProcessPeer(
                pid=pid,
                uid=uid,
                process_start_time=process_start_time,
                process_measurement=process_measurement,
                parent_pid=parent_pid,
                parent_process_start_time=parent_start_time,
                parent_process_measurement=parent_measurement,
            )
            bound = self.bind_peer(peer)
            await self._write_frame(
                writer,
                {
                    "assurance": "same_uid_peercred_direct_parent_module",
                    "generation": self.generation,
                    "ok": True,
                    "schema": "agentnet.mcp.bootstrap-accepted.v1",
                },
            )
            while True:
                try:
                    request = await self._read_frame(reader)
                except asyncio.IncompleteReadError:
                    break
                self.last_request_fields = frozenset(request)
                await self._write_frame(writer, await self.handler(bound, peer, request))
        except Exception as exc:
            try:
                await self._write_frame(
                    writer,
                    {"error": getattr(exc, "code", "invalid_request")},
                )
            except (BrokenPipeError, ConnectionError):
                pass
        finally:
            self._clients.discard(writer)
            writer.close()
            try:
                await asyncio.wait_for(writer.wait_closed(), timeout=0.5)
            except (OSError, TimeoutError):
                writer.transport.abort()


__all__ = ["UnixMCPBootstrapServer", "UnixProcessPeer"]
