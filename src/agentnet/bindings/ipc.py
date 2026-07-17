"""Private Unix IPC bound to an exact supervisor-launched process session.

The bearer value inherited by an adapter is not sufficient on its own.  Its
sealed claims name the Linux peer UID/PID, process start time, executable
measurement, and supervisor session.  Each frame is authenticated with that
capability and consumes a nonce once.  A copied capability therefore fails
closed for a sibling process and for PID reuse.
"""

from __future__ import annotations

import asyncio
import ctypes
import hashlib
import hmac
import json
import os
import secrets
import socket
import sqlite3
import stat
import struct
import threading
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError, model_validator

from agentnet.bindings.tools import CANONICAL_TOOL_NAMES, CanonicalToolName
from agentnet.errors import AuthenticationError, ReplayError, ValidationError
from agentnet.host import HostPlatform, host_platform
from agentnet.host_security import HostProcessIdentity, measure_process_identity
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


CanonicalIPCMethod = CanonicalToolName
LocalBindingMechanism = Literal["direct_ipc", "mcp"]
ProcessBindingMode = Literal["exact", "direct_child"]
Handler = Callable[["IPCSessionClaims", dict[str, Any]], Awaitable[dict[str, Any]]]


@dataclass(frozen=True, slots=True)
class AcceptedProcessPeer:
    """Server-derived peer and direct-parent process identity."""

    platform: HostPlatform
    account_id: str
    pid: int
    uid: int
    process_start_time: str
    process_measurement: str
    parent_pid: int
    parent_account_id: str
    parent_process_start_time: str
    parent_process_measurement: str


class IPCSessionClaims(BaseModel):
    """Exact claims sealed by a per-supervisor-launch capability root."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_id: str = Field(alias="schema", pattern=r"^agentnet\.ipc\.session\.v1$")
    capability_id: str = Field(min_length=24, max_length=128, pattern=r"^[A-Za-z0-9_-]+$")
    harness_id: str = Field(min_length=1, max_length=256)
    credential_id: str = Field(min_length=1, max_length=256)
    credential_epoch: int = Field(ge=1)
    binding: LocalBindingMechanism = "direct_ipc"
    process_binding: ProcessBindingMode = "exact"
    child_process_measurement: str | None = Field(
        default=None,
        pattern=r"^sha256:[0-9a-f]{64}$",
    )
    allowed_methods: tuple[CanonicalIPCMethod, ...] = Field(
        min_length=1,
        max_length=len(CANONICAL_TOOL_NAMES),
    )
    platform: HostPlatform
    account_id: str = Field(min_length=4, max_length=256)
    uid: int = Field(ge=0)
    pid: int = Field(gt=0)
    process_start_time: str = Field(pattern=r"^[0-9]{1,128}$")
    process_measurement: str = Field(pattern=r"^sha256:[0-9a-f]{64}$")
    session_id: str = Field(min_length=16, max_length=256, pattern=r"^[A-Za-z0-9_-]+$")
    issued_at: int = Field(ge=0)
    expires_at: int = Field(gt=0)

    @model_validator(mode="after")
    def bounded_lifetime(self) -> "IPCSessionClaims":
        if self.expires_at <= self.issued_at:
            raise ValueError("IPC capability expiry must follow issuance")
        if self.expires_at - self.issued_at > 3600:
            raise ValueError("IPC capability lifetime exceeds one hour")
        if len(set(self.allowed_methods)) != len(self.allowed_methods):
            raise ValueError("IPC capability methods must be unique")
        if (self.process_binding == "direct_child") != (
            self.child_process_measurement is not None
        ):
            raise ValueError("direct-child IPC binding requires one exact child measurement")
        if self.binding == "direct_ipc" and self.process_binding != "exact":
            raise ValueError("direct IPC must bind the exact harness process")
        if self.platform in {"linux", "macos"}:
            if self.account_id != f"uid:{self.uid}":
                raise ValueError("POSIX IPC account must match its peer UID")
        elif self.uid != 0 or not self.account_id.startswith("sid:S-"):
            raise ValueError("Windows IPC account must use a process-token SID")
        return self


class ProcessIdentityProbe(Protocol):
    def __call__(self, pid: int) -> HostProcessIdentity: ...


def _require_root(root: bytes) -> None:
    if not isinstance(root, bytes) or len(root) < 32:
        raise ValidationError("IPC capability root must contain at least 256 bits")


def _capability_mac(root: bytes, payload: bytes) -> bytes:
    return hmac.digest(root, b"AgentNet-IPC-CAPABILITY\x00" + payload, "sha256")


def mint_inherited_session_capability(root: bytes, claims: IPCSessionClaims) -> str:
    """Seal exact launch claims into the opaque value inherited by one child."""

    _require_root(root)
    payload = canonical_json(claims.model_dump(mode="json", by_alias=True))
    return f"{b64url_encode(payload)}.{b64url_encode(_capability_mac(root, payload))}"


def _open_inherited_session_capability(root: bytes, token: str) -> IPCSessionClaims:
    _require_root(root)
    if not isinstance(token, str) or token.count(".") != 1:
        raise AuthenticationError("IPC capability encoding rejected")
    payload_part, mac_part = token.split(".", 1)
    try:
        payload = b64url_decode(payload_part)
        supplied_mac = b64url_decode(mac_part)
    except ValidationError as exc:
        raise AuthenticationError("IPC capability encoding rejected") from exc
    if b64url_encode(payload) != payload_part or b64url_encode(supplied_mac) != mac_part:
        raise AuthenticationError("IPC capability encoding rejected")
    if not secrets.compare_digest(supplied_mac, _capability_mac(root, payload)):
        raise AuthenticationError("IPC capability rejected")
    try:
        value = json.loads(payload)
        if not isinstance(value, dict):
            raise ValueError
        if canonical_json(value) != payload:
            raise ValueError
        return IPCSessionClaims.model_validate(value)
    except (ValueError, json.JSONDecodeError, PydanticValidationError) as exc:
        raise AuthenticationError("IPC capability claims rejected") from exc


def _frame_authenticator(token: str, *, session_id: str, nonce: str, request: dict[str, Any]) -> str:
    value = {"nonce": nonce, "request": request, "session_id": session_id}
    preimage = b"AgentNet-IPC-FRAME\x00" + canonical_json(value)
    return b64url_encode(hmac.digest(token.encode("ascii"), preimage, "sha256"))


def build_ipc_frame(
    capability: str,
    *,
    session_id: str,
    nonce: str,
    request: dict[str, Any],
) -> dict[str, Any]:
    """Build the fixed-schema frame emitted by a credential-free adapter."""

    if not isinstance(request, dict):
        raise ValidationError("IPC request must be an object")
    return {
        "authenticator": _frame_authenticator(
            capability,
            session_id=session_id,
            nonce=nonce,
            request=request,
        ),
        "capability": capability,
        "nonce": nonce,
        "request": request,
        "session_id": session_id,
    }


class IPCSessionVerifier:
    """Verify exact process/session binding and consume per-frame nonces."""

    def __init__(
        self,
        root: bytes,
        *,
        replay_store: StoreBackend,
        clock: Callable[[], int] | None = None,
    ) -> None:
        _require_root(root)
        self._root = bytes(root)
        self._root_key_id = hashlib.sha256(
            hmac.digest(self._root, b"AgentNet-IPC-REPLAY-ROOT-ID\x00", "sha256")
        ).hexdigest()
        self._replay_store = replay_store
        self._clock = clock or (lambda: int(time.time()))

    def _consume_replay_fence(self, claims: IPCSessionClaims, nonce: str, *, now: int) -> None:
        context = {
            "account_id": claims.account_id,
            "capability_id": claims.capability_id,
            "peer_pid": claims.pid,
            "peer_uid": claims.uid,
            "platform": claims.platform,
            "process_measurement": claims.process_measurement,
            "process_start_time": claims.process_start_time,
            "root_key_id": self._root_key_id,
            "schema": "agentnet.ipc.replay-context.v2",
            "session_id": claims.session_id,
        }
        context_digest = canonical_digest(context)
        nonce_hash = hashlib.sha256(nonce.encode("utf-8")).hexdigest()
        with self._replay_store.transaction() as connection:
            connection.execute("DELETE FROM ipc_replay_fences WHERE expires_at<=?", (now,))
            try:
                connection.execute(
                    """INSERT INTO ipc_replay_fences(
                        context_digest,root_key_id,capability_id,peer_uid,peer_pid,
                        process_start_time,process_measurement,session_id,nonce_hash,
                        consumed_at,expires_at
                    ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                    (
                        context_digest,
                        self._root_key_id,
                        claims.capability_id,
                        claims.uid,
                        claims.pid,
                        claims.process_start_time,
                        claims.process_measurement,
                        claims.session_id,
                        nonce_hash,
                        now,
                        claims.expires_at,
                    ),
                )
            except Exception as exc:
                if isinstance(exc, sqlite3.IntegrityError) or exc.__class__.__name__ in {
                    "IntegrityError",
                    "UniqueViolation",
                }:
                    raise ReplayError("IPC frame nonce was already consumed") from exc
                raise

    def verify(
        self,
        frame: dict[str, Any],
        *,
        peer: AcceptedProcessPeer,
    ) -> dict[str, Any]:
        _claims, request = self.verify_context(frame, peer=peer)
        return request

    def verify_context(
        self,
        frame: dict[str, Any],
        *,
        peer: AcceptedProcessPeer,
    ) -> tuple[IPCSessionClaims, dict[str, Any]]:
        if not isinstance(frame, dict) or set(frame) != {
            "authenticator",
            "capability",
            "nonce",
            "request",
            "session_id",
        }:
            raise ValidationError("IPC frame schema rejected")
        if not all(isinstance(frame[field], str) for field in ("authenticator", "capability", "nonce", "session_id")):
            raise ValidationError("IPC frame scalar types rejected")
        if not isinstance(frame["request"], dict):
            raise ValidationError("IPC request must be an object")
        nonce = frame["nonce"]
        if not 24 <= len(nonce) <= 256 or any(character not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" for character in nonce):
            raise ValidationError("IPC nonce length outside profile")

        claims = _open_inherited_session_capability(self._root, frame["capability"])
        now = self._clock()
        if claims.issued_at > now or claims.expires_at <= now:
            raise AuthenticationError("IPC capability is outside its validity window")
        if claims.process_binding == "exact":
            actual_binding = (
                peer.platform,
                peer.account_id,
                peer.uid,
                peer.pid,
                peer.process_start_time,
                peer.process_measurement,
                frame["session_id"],
            )
        else:
            if peer.account_id != peer.parent_account_id:
                raise AuthenticationError("IPC child and parent accounts differ")
            actual_binding = (
                peer.platform,
                peer.parent_account_id,
                peer.uid,
                peer.parent_pid,
                peer.parent_process_start_time,
                peer.parent_process_measurement,
                frame["session_id"],
            )
        expected_binding = (
            claims.platform,
            claims.account_id,
            claims.uid,
            claims.pid,
            claims.process_start_time,
            claims.process_measurement,
            claims.session_id,
        )
        if actual_binding != expected_binding:
            raise AuthenticationError("IPC process/session binding mismatch")
        if (
            claims.process_binding == "direct_child"
            and peer.process_measurement != claims.child_process_measurement
        ):
            raise AuthenticationError("IPC child executable measurement mismatch")
        expected_authenticator = _frame_authenticator(
            frame["capability"],
            session_id=frame["session_id"],
            nonce=nonce,
            request=frame["request"],
        )
        if not secrets.compare_digest(frame["authenticator"], expected_authenticator):
            raise AuthenticationError("IPC frame authenticator rejected")

        self._consume_replay_fence(claims, nonce, now=now)
        return claims, frame["request"]


def linux_process_probe(pid: int) -> tuple[str, str]:
    """Compatibility name for cross-platform PID-reuse and executable proof."""

    identity = measure_process_identity(pid)
    return identity.start_time, identity.executable_measurement


def linux_process_parent(pid: int) -> int:
    """Compatibility name for cross-platform server-measured ancestry."""

    parent_pid = measure_process_identity(pid).parent_pid
    if parent_pid <= 0:
        raise AuthenticationError("IPC process ancestry is invalid")
    return parent_pid


def _macos_peer_ids(sock: socket.socket) -> tuple[int, int]:
    """Read effective peer PID and UID from one accepted Darwin Unix socket."""

    try:
        raw_pid = sock.getsockopt(0, 0x002, struct.calcsize("i"))  # SOL_LOCAL/LOCAL_PEERPID
        if not isinstance(raw_pid, bytes) or len(raw_pid) != struct.calcsize("i"):
            raise OSError("LOCAL_PEERPID returned an invalid payload")
        pid = struct.unpack("i", raw_pid)[0]
        libc = ctypes.CDLL(None, use_errno=True)
        getpeereid = libc.getpeereid
        getpeereid.argtypes = [
            ctypes.c_int,
            ctypes.POINTER(ctypes.c_uint),
            ctypes.POINTER(ctypes.c_uint),
        ]
        getpeereid.restype = ctypes.c_int
        uid = ctypes.c_uint()
        gid = ctypes.c_uint()
        if getpeereid(sock.fileno(), ctypes.byref(uid), ctypes.byref(gid)) != 0:
            errno_value = ctypes.get_errno()
            raise OSError(errno_value, os.strerror(errno_value))
    except (AttributeError, OSError, TypeError, struct.error) as exc:
        raise AuthenticationError("macOS Unix peer credentials unavailable") from exc
    if pid <= 0:
        raise AuthenticationError("macOS Unix peer PID is invalid")
    return pid, int(uid.value)


def accepted_windows_pipe_peer(
    handle: Any,
    *,
    process_identity_probe: ProcessIdentityProbe = measure_process_identity,
) -> AcceptedProcessPeer:
    """Derive Windows peer authority from connected named-pipe client PID."""

    try:
        import win32pipe
    except ImportError as exc:  # pragma: no cover - Windows dependency gate
        raise AuthenticationError("pywin32 named-pipe identity is unavailable") from exc
    try:
        pid = int(win32pipe.GetNamedPipeClientProcessId(handle))
        peer = process_identity_probe(pid)
        parent = process_identity_probe(peer.parent_pid)
    except Exception as exc:
        raise AuthenticationError("Windows named-pipe client process identity rejected") from exc
    if (
        peer.platform != "windows"
        or parent.platform != "windows"
        or not peer.account_id.startswith("sid:S-")
        or not parent.account_id.startswith("sid:S-")
    ):
        raise AuthenticationError("Windows named-pipe client process identity rejected")
    return AcceptedProcessPeer(
        platform="windows",
        account_id=peer.account_id,
        pid=peer.pid,
        uid=0,
        process_start_time=peer.start_time,
        process_measurement=peer.executable_measurement,
        parent_pid=parent.pid,
        parent_account_id=parent.account_id,
        parent_process_start_time=parent.start_time,
        parent_process_measurement=parent.executable_measurement,
    )


def accepted_unix_socket_peer(
    sock: socket.socket,
    *,
    platform_name: HostPlatform | None = None,
    process_identity_probe: ProcessIdentityProbe = measure_process_identity,
) -> AcceptedProcessPeer:
    """Derive peer authority only from accepted-socket and host process facts."""

    platform_value = host_platform() if platform_name is None else platform_name
    if platform_value == "linux":
        if not hasattr(socket, "SO_PEERCRED"):
            raise AuthenticationError("Linux Unix peer credentials unavailable")
        try:
            pid, uid, _gid = struct.unpack(
                "3i",
                sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12),
            )
        except (OSError, TypeError, struct.error) as exc:
            raise AuthenticationError("Linux Unix peer credentials unavailable") from exc
    elif platform_value == "macos":
        pid, uid = _macos_peer_ids(sock)
    else:
        raise AuthenticationError("Unix peer credentials are unavailable on Windows")

    peer = process_identity_probe(pid)
    if peer.platform != platform_value or peer.account_id != f"uid:{uid}":
        raise AuthenticationError("Unix peer process account does not match socket credentials")
    parent = process_identity_probe(peer.parent_pid)
    if parent.platform != platform_value:
        raise AuthenticationError("Unix peer parent platform changed during measurement")
    return AcceptedProcessPeer(
        platform=platform_value,
        account_id=peer.account_id,
        pid=peer.pid,
        uid=uid,
        process_start_time=peer.start_time,
        process_measurement=peer.executable_measurement,
        parent_pid=parent.pid,
        parent_account_id=parent.account_id,
        parent_process_start_time=parent.start_time,
        parent_process_measurement=parent.executable_measurement,
    )


class WindowsNamedPipeIPCServer:
    """Private byte-stream named pipe with server-derived client identity."""

    def __init__(
        self,
        path: str,
        *,
        capability_root: bytes,
        replay_store: StoreBackend,
        handler: Handler,
        max_frame: int = 1_048_576,
        clock: Callable[[], int] | None = None,
        process_identity_probe: ProcessIdentityProbe = measure_process_identity,
    ) -> None:
        if not isinstance(path, str) or not path.startswith(r"\\.\pipe\agentnet-"):
            raise ValidationError("Windows named-pipe locator is invalid")
        self.path = path
        self._verifier = IPCSessionVerifier(
            capability_root,
            replay_store=replay_store,
            clock=clock,
        )
        self.handler = handler
        self.max_frame = max_frame
        self.process_identity_probe = process_identity_probe
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
        try:
            import pywintypes
            import win32con
            import win32file
            import win32pipe
            import winerror
        except ImportError as exc:  # pragma: no cover - Windows dependency gate
            raise AuthenticationError("pywin32 named-pipe support is unavailable") from exc
        return pywintypes, win32con, win32file, win32pipe, winerror

    def _create_pipe(self):
        _pywintypes, _win32con, _win32file, win32pipe, _winerror = self._imports()
        from agentnet.windows_security import (
            private_security_attributes,
            require_private_kernel_handle,
        )

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
        _pywintypes, _win32con, win32file, _win32pipe, _winerror = WindowsNamedPipeIPCServer._imports()
        value = bytearray()
        while len(value) < length:
            _status, chunk = win32file.ReadFile(handle, length - len(value))
            if not chunk:
                raise ValidationError("Windows named-pipe frame ended early")
            value.extend(chunk)
        return bytes(value)

    @staticmethod
    def _write_frame(handle, value: dict[str, Any]) -> None:
        _pywintypes, _win32con, win32file, _win32pipe, _winerror = WindowsNamedPipeIPCServer._imports()
        body = canonical_json(value)
        win32file.WriteFile(handle, len(body).to_bytes(4, "big") + body)
        win32file.FlushFileBuffers(handle)

    def _accepted_peer(self, handle) -> AcceptedProcessPeer:
        return accepted_windows_pipe_peer(
            handle,
            process_identity_probe=self.process_identity_probe,
        )

    def _serve_client(self, handle) -> None:
        _pywintypes, _win32con, _win32file, win32pipe, _winerror = self._imports()
        try:
            peer = self._accepted_peer(handle)
            length = int.from_bytes(self._read_exact(handle, 4), "big")
            if length < 2 or length > self.max_frame:
                raise ValidationError("IPC frame length rejected")
            raw = self._read_exact(handle, length)
            message = json.loads(raw)
            if not isinstance(message, dict) or canonical_json(message) != raw:
                raise ValidationError("IPC frame must use exact canonical JSON")
            claims, request = self._verifier.verify_context(message, peer=peer)
            loop = self._loop
            if loop is None or loop.is_closed():
                raise AuthenticationError("Windows named-pipe event loop is unavailable")
            result = asyncio.run_coroutine_threadsafe(
                self.handler(claims, request), loop
            ).result(timeout=30)
            self._write_frame(handle, result)
        except Exception as exc:
            try:
                self._write_frame(handle, {"error": getattr(exc, "code", "invalid_request")})
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
                    try:
                        handle.Close()
                    finally:
                        with self._guard:
                            self._handles.discard(handle)
                    break
                thread = threading.Thread(
                    target=self._serve_client,
                    args=(handle,),
                    name="agentnet-windows-pipe-client",
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
        if host_platform() != "windows":
            raise AuthenticationError("Windows named-pipe server requires Windows")
        if self._server_thread is not None:
            raise AuthenticationError("Windows named-pipe server is already started")
        self._loop = asyncio.get_running_loop()
        self._stop.clear()
        self._ready.clear()
        self._startup_error = None
        self._server_thread = threading.Thread(
            target=self._serve,
            name="agentnet-windows-pipe-accept",
            daemon=True,
        )
        self._server_thread.start()
        ready = await asyncio.to_thread(self._ready.wait, 10)
        if not ready or self._startup_error is not None:
            await self.close()
            if self._startup_error is not None:
                raise AuthenticationError("Windows named-pipe server failed to start") from self._startup_error
            raise AuthenticationError("Windows named-pipe server startup timed out")

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


class UnixIPCServer:
    def __init__(
        self,
        path: Path,
        *,
        capability_root: bytes,
        replay_store: StoreBackend,
        handler: Handler,
        max_frame: int = 1_048_576,
        clock: Callable[[], int] | None = None,
        process_identity_probe: ProcessIdentityProbe = measure_process_identity,
    ) -> None:
        self.path = path
        self._verifier = IPCSessionVerifier(
            capability_root,
            replay_store=replay_store,
            clock=clock,
        )
        self.handler = handler
        self.max_frame = max_frame
        self.process_identity_probe = process_identity_probe
        self._server: asyncio.AbstractServer | None = None
        self._socket_identity: tuple[int, int] | None = None

    async def start(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        parent = self.path.parent.lstat()
        if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid() or parent.st_mode & 0o077:
            raise AuthenticationError("IPC directory ownership or mode rejected")
        if os.path.lexists(self.path):
            raise AuthenticationError("IPC path replacement or stale socket detected")
        self._server = await asyncio.start_unix_server(self._handle, path=self.path)
        os.chmod(self.path, 0o600)
        created = self.path.stat()
        self._socket_identity = (created.st_dev, created.st_ino)

    async def close(self) -> None:
        if self._server:
            self._server.close()
            await self._server.wait_closed()
        if os.path.lexists(self.path) and self.path.is_socket() and self._socket_identity is not None:
            current = self.path.stat()
            if (current.st_dev, current.st_ino) == self._socket_identity:
                self.path.unlink()
        self._socket_identity = None

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            sock = writer.get_extra_info("socket")
            if sock is None:
                raise AuthenticationError("platform peer credentials unavailable")
            peer = accepted_unix_socket_peer(
                sock,
                process_identity_probe=self.process_identity_probe,
            )
            prefix = await reader.readexactly(4)
            length = int.from_bytes(prefix, "big")
            if length < 2 or length > self.max_frame:
                raise ValidationError("IPC frame length rejected")
            raw_message = await reader.readexactly(length)
            message = json.loads(raw_message)
            if not isinstance(message, dict) or canonical_json(message) != raw_message:
                raise ValidationError("IPC frame must use exact canonical JSON")
            claims, request = self._verifier.verify_context(message, peer=peer)
            response = await self.handler(claims, request)
            body = canonical_json(response)
            writer.write(len(body).to_bytes(4, "big") + body)
            await writer.drain()
        except Exception as exc:
            body = canonical_json({"error": getattr(exc, "code", "invalid_request")})
            writer.write(len(body).to_bytes(4, "big") + body)
            await writer.drain()
        finally:
            writer.close()
            await writer.wait_closed()
