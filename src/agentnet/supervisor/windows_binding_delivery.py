"""One-time process-bound Pi capability delivery over a private Windows pipe."""

from __future__ import annotations

import os
import secrets
import threading
from typing import Any

from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.host_security import HostProcessIdentity, measure_process_identity
from agentnet.windows_security import (
    private_security_attributes,
    require_private_kernel_handle,
)


class WindowsBindingDelivery:
    """Deliver one capability only to one exact server-measured Windows process."""

    def __init__(self, *, timeout_seconds: float = 30.0) -> None:
        if os.name != "nt":
            raise GateBlocked("G05", "Windows binding delivery requires Windows")
        self.endpoint = rf"\\.\pipe\agentnet-binding-{secrets.token_urlsafe(24)}"
        self.timeout_seconds = timeout_seconds
        self._stop = threading.Event()
        self._ready = threading.Event()
        self._published = threading.Event()
        self._delivered = threading.Event()
        self._guard = threading.Lock()
        self._thread: threading.Thread | None = None
        self._handle: Any = None
        self._payload: bytes | None = None
        self._expected: HostProcessIdentity | None = None
        self._error: BaseException | None = None

    @staticmethod
    def _imports():
        try:
            import pywintypes
            import win32con
            import win32file
            import win32pipe
            import winerror
        except ImportError as exc:  # pragma: no cover - Windows dependency gate
            raise GateBlocked("G05", "pywin32 binding delivery is unavailable") from exc
        return pywintypes, win32con, win32file, win32pipe, winerror

    def _create_pipe(self):
        _pywintypes, _win32con, _win32file, win32pipe, _winerror = self._imports()
        reject_remote = getattr(win32pipe, "PIPE_REJECT_REMOTE_CLIENTS", 0x00000008)
        handle = win32pipe.CreateNamedPipe(
            self.endpoint,
            win32pipe.PIPE_ACCESS_OUTBOUND,
            win32pipe.PIPE_TYPE_BYTE
            | win32pipe.PIPE_READMODE_BYTE
            | win32pipe.PIPE_WAIT
            | reject_remote,
            win32pipe.PIPE_UNLIMITED_INSTANCES,
            65_540,
            0,
            5_000,
            private_security_attributes(),
        )
        require_private_kernel_handle(handle, label=self.endpoint)
        return handle

    @staticmethod
    def _same_process(actual: HostProcessIdentity, expected: HostProcessIdentity) -> bool:
        return (
            actual.platform,
            actual.account_id,
            actual.pid,
            actual.parent_pid,
            actual.start_time,
            actual.executable_path,
            actual.executable_measurement,
        ) == (
            expected.platform,
            expected.account_id,
            expected.pid,
            expected.parent_pid,
            expected.start_time,
            expected.executable_path,
            expected.executable_measurement,
        )

    def _serve(self) -> None:
        pywintypes, _win32con, win32file, win32pipe, winerror = self._imports()
        try:
            while not self._stop.is_set() and not self._delivered.is_set():
                handle = self._create_pipe()
                with self._guard:
                    self._handle = handle
                self._ready.set()
                try:
                    win32pipe.ConnectNamedPipe(handle, None)
                except pywintypes.error as exc:
                    if getattr(exc, "winerror", None) != winerror.ERROR_PIPE_CONNECTED:
                        raise
                if self._stop.is_set():
                    break
                if not self._published.wait(self.timeout_seconds):
                    raise GateBlocked("G05", "Windows binding publication timed out")
                expected = self._expected
                payload = self._payload
                if expected is None or payload is None:
                    raise GateBlocked("G05", "Windows binding publication is incomplete")
                pid = int(win32pipe.GetNamedPipeClientProcessId(handle))
                actual = measure_process_identity(pid)
                if not self._same_process(actual, expected):
                    win32pipe.DisconnectNamedPipe(handle)
                    handle.Close()
                    with self._guard:
                        self._handle = None
                    continue
                win32file.WriteFile(handle, len(payload).to_bytes(4, "big") + payload)
                win32file.FlushFileBuffers(handle)
                self._delivered.set()
                break
        except Exception as exc:
            if not self._stop.is_set():
                self._error = exc
            self._ready.set()
        finally:
            with self._guard:
                handle, self._handle = self._handle, None
            if handle is not None:
                try:
                    win32pipe.DisconnectNamedPipe(handle)
                except Exception:
                    pass
                try:
                    handle.Close()
                except Exception:
                    pass

    def start(self) -> None:
        if self._thread is not None:
            raise GateBlocked("G05", "Windows binding delivery is already started")
        self._thread = threading.Thread(
            target=self._serve,
            name="agentnet-windows-binding-delivery",
            daemon=True,
        )
        self._thread.start()
        if not self._ready.wait(10) or self._error is not None:
            self.close()
            raise GateBlocked("G05", "Windows binding delivery failed to start") from self._error

    def publish(self, payload: bytes, *, expected: HostProcessIdentity) -> None:
        if not isinstance(payload, bytes) or not 2 <= len(payload) <= 65_536:
            raise GateBlocked("G05", "Windows binding capability response is oversized")
        if expected.platform != "windows" or not expected.account_id.startswith("sid:S-"):
            raise AuthenticationError("Windows binding target identity is invalid")
        if self._published.is_set():
            raise GateBlocked("G05", "Windows binding capability was already published")
        self._payload = bytes(payload)
        self._expected = expected
        self._published.set()

    def _unblock(self) -> None:
        _pywintypes, win32con, win32file, _win32pipe, _winerror = self._imports()
        try:
            handle = win32file.CreateFile(
                self.endpoint,
                win32con.GENERIC_READ,
                0,
                None,
                win32con.OPEN_EXISTING,
                0,
                None,
            )
            handle.Close()
        except Exception:
            pass

    def close(self) -> None:
        self._stop.set()
        self._unblock()
        thread = self._thread
        if thread is not None and thread is not threading.current_thread():
            thread.join(timeout=10)
        with self._guard:
            handle, self._handle = self._handle, None
        if handle is not None:
            try:
                handle.Close()
            except Exception:
                pass
        self._thread = None


__all__ = ["WindowsBindingDelivery"]
