"""Private Unix IPC bound to an exact supervisor-launched process session.

The bearer value inherited by an adapter is not sufficient on its own.  Its
sealed claims name the Linux peer UID/PID, process start time, executable
measurement, and supervisor session.  Each frame is authenticated with that
capability and consumes a nonce once.  A copied capability therefore fails
closed for a sibling process and for PID reuse.
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import os
import secrets
import socket
import sqlite3
import stat
import struct
import time
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError, model_validator

from agentnet.bindings.tools import CANONICAL_TOOL_NAMES, CanonicalToolName
from agentnet.errors import AuthenticationError, ReplayError, ValidationError
from agentnet.security.signatures import b64url_decode, b64url_encode, canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


CanonicalIPCMethod = CanonicalToolName
LocalBindingMechanism = Literal["direct_ipc", "mcp"]
ProcessBindingMode = Literal["exact", "direct_child"]
Handler = Callable[["IPCSessionClaims", dict[str, Any]], Awaitable[dict[str, Any]]]


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
        return self


class ProcessProbe(Protocol):
    def __call__(self, pid: int) -> tuple[str, str]: ...


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
            "capability_id": claims.capability_id,
            "peer_pid": claims.pid,
            "peer_uid": claims.uid,
            "process_measurement": claims.process_measurement,
            "process_start_time": claims.process_start_time,
            "root_key_id": self._root_key_id,
            "schema": "agentnet.ipc.replay-context.v1",
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
        peer_uid: int,
        peer_pid: int,
        process_start_time: str,
        process_measurement: str,
    ) -> dict[str, Any]:
        _claims, request = self.verify_context(
            frame,
            peer_uid=peer_uid,
            peer_pid=peer_pid,
            process_start_time=process_start_time,
            process_measurement=process_measurement,
        )
        return request

    def verify_context(
        self,
        frame: dict[str, Any],
        *,
        peer_uid: int,
        peer_pid: int,
        process_start_time: str,
        process_measurement: str,
        parent_pid: int | None = None,
        parent_process_start_time: str | None = None,
        parent_process_measurement: str | None = None,
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
                peer_uid,
                peer_pid,
                process_start_time,
                process_measurement,
                frame["session_id"],
            )
        else:
            actual_binding = (
                peer_uid,
                parent_pid,
                parent_process_start_time,
                parent_process_measurement,
                frame["session_id"],
            )
        expected_binding = (
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
            and process_measurement != claims.child_process_measurement
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
    """Read Linux PID-reuse evidence and hash the running executable."""

    try:
        stat_value = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
        suffix = stat_value[stat_value.rindex(")") + 2 :].split()
        # Fields after comm begin at stat field 3; starttime is field 22.
        start_time = suffix[19]
        digest = hashlib.sha256()
        with Path(f"/proc/{pid}/exe").open("rb") as executable:
            for chunk in iter(lambda: executable.read(1024 * 1024), b""):
                digest.update(chunk)
    except (OSError, ValueError, IndexError) as exc:
        raise AuthenticationError("IPC process identity could not be measured") from exc
    return start_time, f"sha256:{digest.hexdigest()}"


def linux_process_parent(pid: int) -> int:
    """Return the exact Linux parent PID without trusting caller-supplied ancestry."""

    try:
        stat_value = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
        suffix = stat_value[stat_value.rindex(")") + 2 :].split()
        parent_pid = int(suffix[1])
    except (OSError, ValueError, IndexError) as exc:
        raise AuthenticationError("IPC process ancestry could not be measured") from exc
    if parent_pid <= 0:
        raise AuthenticationError("IPC process ancestry is invalid")
    return parent_pid


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
        process_probe: ProcessProbe = linux_process_probe,
    ) -> None:
        self.path = path
        self._verifier = IPCSessionVerifier(
            capability_root,
            replay_store=replay_store,
            clock=clock,
        )
        self.handler = handler
        self.max_frame = max_frame
        self.process_probe = process_probe
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
            if sock is None or not hasattr(socket, "SO_PEERCRED"):
                raise AuthenticationError("platform peer credentials unavailable")
            pid, uid, _gid = struct.unpack("3i", sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            start_time, measurement = self.process_probe(pid)
            parent_pid = linux_process_parent(pid)
            parent_start_time, parent_measurement = self.process_probe(parent_pid)
            prefix = await reader.readexactly(4)
            length = int.from_bytes(prefix, "big")
            if length < 2 or length > self.max_frame:
                raise ValidationError("IPC frame length rejected")
            raw_message = await reader.readexactly(length)
            message = json.loads(raw_message)
            if not isinstance(message, dict) or canonical_json(message) != raw_message:
                raise ValidationError("IPC frame must use exact canonical JSON")
            claims, request = self._verifier.verify_context(
                message,
                peer_uid=uid,
                peer_pid=pid,
                process_start_time=start_time,
                process_measurement=measurement,
                parent_pid=parent_pid,
                parent_process_start_time=parent_start_time,
                parent_process_measurement=parent_measurement,
            )
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
