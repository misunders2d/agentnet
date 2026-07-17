"""Persistent peer-bound MCP bootstrap over a protected Windows named pipe."""

from __future__ import annotations

import asyncio
import json
import os
import threading
from collections.abc import Awaitable, Callable
from typing import Any

from agentnet.bindings.ipc import (
    AcceptedProcessPeer,
    WindowsNamedPipeIPCServer,
    accepted_windows_pipe_peer,
)
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import canonical_json
from agentnet.windows_security import (
    private_security_attributes,
    require_private_kernel_handle,
)


PeerBinder = Callable[[AcceptedProcessPeer], Any]
BoundHandler = Callable[
    [Any, AcceptedProcessPeer, dict[str, Any]],
    Awaitable[dict[str, Any]],
]


class WindowsMCPBootstrapServer:
    """Bind persistent MCP calls to exact named-pipe client and parent process."""

    def __init__(
        self,
        path: str,
        *,
        bind_peer: PeerBinder,
        handler: BoundHandler,
        generation: str,
        assurance: str,
        max_frame: int = 1_048_576,
    ) -> None:
        if not isinstance(path, str) or not path.startswith(r"\\.\pipe\agentnet-mcp-"):
            raise ValidationError("Windows MCP named-pipe locator is invalid")
        self.path = path
        self.bind_peer = bind_peer
        self.handler = handler
        self.generation = generation
        self.assurance = assurance
        self.max_frame = max_frame
        self.last_request_fields: frozenset[str] | None = None
        self._loop: asyncio.AbstractEventLoop | None = None
        self._stop = threading.Event()
        self._ready = threading.Event()
        self._server_thread: threading.Thread | None = None
        self._client_threads: set[threading.Thread] = set()
        self._handles: set[Any] = set()
        self._guard = threading.Lock()
        self._startup_error: BaseException | None = None

    @staticmethod
    def _imports():
        return WindowsNamedPipeIPCServer._imports()

    def _create_pipe(self):
        _pywintypes, _win32con, _win32file, win32pipe, _winerror = self._imports()
        reject_remote = getattr(win32pipe, "PIPE_REJECT_REMOTE_CLIENTS", 0x00000008)
        handle = win32pipe.CreateNamedPipe(
            self.path,
            win32pipe.PIPE_ACCESS_DUPLEX,
            win32pipe.PIPE_TYPE_BYTE
            | win32pipe.PIPE_READMODE_BYTE
            | win32pipe.PIPE_WAIT
            | reject_remote,
            win32pipe.PIPE_UNLIMITED_INSTANCES,
            self.max_frame + 4,
            self.max_frame + 4,
            5_000,
            private_security_attributes(),
        )
        require_private_kernel_handle(handle, label=self.path)
        return handle

    @staticmethod
    def _read_exact(handle, length: int) -> bytes:
        return WindowsNamedPipeIPCServer._read_exact(handle, length)

    def _read_frame(self, handle) -> dict[str, Any]:
        length = int.from_bytes(self._read_exact(handle, 4), "big")
        if length < 2 or length > self.max_frame:
            raise ValidationError("MCP bootstrap frame length rejected")
        raw = self._read_exact(handle, length)
        try:
            value = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValidationError("MCP bootstrap frame is invalid") from exc
        if not isinstance(value, dict) or canonical_json(value) != raw:
            raise ValidationError("MCP bootstrap frame must use exact canonical JSON")
        return value

    @staticmethod
    def _write_frame(handle, value: dict[str, Any]) -> None:
        WindowsNamedPipeIPCServer._write_frame(handle, value)

    def _serve_client(self, handle) -> None:
        _pywintypes, _win32con, _win32file, win32pipe, _winerror = self._imports()
        try:
            peer = accepted_windows_pipe_peer(handle)
            bound = self.bind_peer(peer)
            self._write_frame(
                handle,
                {
                    "assurance": self.assurance,
                    "generation": self.generation,
                    "ok": True,
                    "schema": "agentnet.mcp.bootstrap-accepted.v1",
                },
            )
            while not self._stop.is_set():
                request = self._read_frame(handle)
                self.last_request_fields = frozenset(request)
                loop = self._loop
                if loop is None or loop.is_closed():
                    raise AuthenticationError("Windows MCP event loop is unavailable")
                response = asyncio.run_coroutine_threadsafe(
                    self.handler(bound, peer, request),
                    loop,
                ).result(timeout=30)
                self._write_frame(handle, response)
        except Exception as exc:
            if not self._stop.is_set():
                try:
                    self._write_frame(
                        handle,
                        {"error": getattr(exc, "code", "invalid_request")},
                    )
                except Exception:
                    pass
        finally:
            try:
                win32pipe.DisconnectNamedPipe(handle)
            except Exception:
                pass
            try:
                handle.Close()
            except Exception:
                pass
            with self._guard:
                self._handles.discard(handle)
                self._client_threads.discard(threading.current_thread())

    def _serve(self) -> None:
        pywintypes, _win32con, _win32file, win32pipe, winerror = self._imports()
        try:
            while not self._stop.is_set():
                handle = self._create_pipe()
                with self._guard:
                    self._handles.add(handle)
                self._ready.set()
                try:
                    win32pipe.ConnectNamedPipe(handle, None)
                except pywintypes.error as exc:
                    if getattr(exc, "winerror", None) != winerror.ERROR_PIPE_CONNECTED:
                        raise
                if self._stop.is_set():
                    handle.Close()
                    with self._guard:
                        self._handles.discard(handle)
                    break
                thread = threading.Thread(
                    target=self._serve_client,
                    args=(handle,),
                    name="agentnet-windows-mcp-client",
                    daemon=True,
                )
                with self._guard:
                    self._client_threads.add(thread)
                thread.start()
        except Exception as exc:
            if not self._stop.is_set():
                self._startup_error = exc
            self._ready.set()

    async def start(self) -> None:
        if os.name != "nt":
            raise AuthenticationError("Windows MCP bootstrap requires Windows")
        self._loop = asyncio.get_running_loop()
        self._stop.clear()
        self._ready.clear()
        self._server_thread = threading.Thread(
            target=self._serve,
            name="agentnet-windows-mcp-accept",
            daemon=True,
        )
        self._server_thread.start()
        ready = await asyncio.to_thread(self._ready.wait, 10)
        if not ready or self._startup_error is not None:
            await self.close()
            raise AuthenticationError("Windows MCP bootstrap failed to start") from self._startup_error

    def _unblock_accept(self) -> None:
        _pywintypes, win32con, win32file, _win32pipe, _winerror = self._imports()
        try:
            handle = win32file.CreateFile(
                self.path,
                win32con.GENERIC_READ | win32con.GENERIC_WRITE,
                0,
                None,
                win32con.OPEN_EXISTING,
                0,
                None,
            )
            handle.Close()
        except Exception:
            pass

    async def close(self) -> None:
        self._stop.set()
        await asyncio.to_thread(self._unblock_accept)
        thread = self._server_thread
        if thread is not None:
            await asyncio.to_thread(thread.join, 10)
        with self._guard:
            handles = tuple(self._handles)
            clients = tuple(self._client_threads)
        for handle in handles:
            try:
                handle.Close()
            except Exception:
                pass
        for client in clients:
            await asyncio.to_thread(client.join, 2)
        self._server_thread = None
        self._loop = None


__all__ = ["WindowsMCPBootstrapServer"]
