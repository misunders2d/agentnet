"""Fail-closed composition for MCP and Pi's direct Unix IPC binding."""

from __future__ import annotations

import os
import secrets
import stat
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from mcp.server.fastmcp import FastMCP
import psutil

from agentnet.adapters.capabilities import ALL as ADAPTER_CAPABILITIES
from agentnet.bindings.endpoint import (
    EndpointBinding,
    EndpointBindingRepository,
    exact_process_measurement,
)
from agentnet.bindings.ipc import (
    IPCSessionClaims,
    UnixIPCServer,
    WindowsNamedPipeIPCServer,
    linux_process_parent,
    linux_process_probe,
    mint_inherited_session_capability,
)
from agentnet.bindings.mcp import create_mcp_binding
from agentnet.bindings.mcp_bootstrap import (
    MCP_BOOTSTRAP_ASSURANCE,
    UnixMCPBootstrapServer,
    UnixProcessPeer,
)
from agentnet.bindings.windows_mcp_bootstrap import WindowsMCPBootstrapServer
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES, CanonicalToolDispatcher
from agentnet.errors import AuthenticationError, AuthorizationError, GateBlocked, ValidationError
from agentnet.host import HostPlatform, host_platform
from agentnet.host_security import current_account_id, measure_process_identity
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import CredentialBinding, load_credential_binding
from agentnet.operations.config import LocalBindingConfig
from agentnet.operations.endpoint_lifecycle import (
    EndpointActivationState,
    EndpointLifecycleService,
)
from agentnet.storage.backend import StoreBackend


MAX_CAPABILITY_ROOT_BYTES = 4096


class LocalBindingCore:
    """Structural marker for the ordinary core used by canonical tools."""

    config: Any
    store: StoreBackend


@dataclass(frozen=True, slots=True)
class BoundHarnessSession:
    endpoint: EndpointBinding

    @property
    def harness_id(self) -> str:
        return self.endpoint.harness_id

    @property
    def credential_id(self) -> str:
        return self.endpoint.credential_id

    @property
    def credential_epoch(self) -> int:
        return self.endpoint.credential_epoch


@dataclass(frozen=True, slots=True)
class IssuedChildCapability:
    capability: str
    session_id: str
    harness_id: str
    credential_id: str
    credential_epoch: int
    expires_at: int
    socket_path: Path | str

    def redacted(self) -> dict[str, Any]:
        return {
            "schema": "agentnet.ipc.issued-child.v1",
            "session_id": self.session_id,
            "harness_id": self.harness_id,
            "credential_id": self.credential_id,
            "credential_epoch": self.credential_epoch,
            "expires_at": self.expires_at,
            "socket_path": str(self.socket_path),
        }


@dataclass(frozen=True, slots=True)
class RegisteredMCPLaunch:
    session_id: str
    harness_id: str
    credential_id: str
    credential_epoch: int
    expires_at: int
    bootstrap_socket_path: Path | str
    bootstrap_generation: str

    def redacted(self) -> dict[str, Any]:
        return {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": self.session_id,
            "harness_id": self.harness_id,
            "credential_id": self.credential_id,
            "credential_epoch": self.credential_epoch,
            "expires_at": self.expires_at,
            "bootstrap_socket_path": str(self.bootstrap_socket_path),
            "bootstrap_generation": self.bootstrap_generation,
            "assurance": MCP_BOOTSTRAP_ASSURANCE,
        }


@dataclass(slots=True)
class _MCPLaunchRecord:
    record_id: str
    session: BoundHarnessSession
    session_id: str
    platform: HostPlatform
    account_id: str
    uid: int
    parent_pid: int
    parent_process_start_time: str
    parent_process_measurement: str
    endpoint_process_measurement: str
    expires_at: int
    proxy_pid: int | None = None
    proxy_process_start_time: str | None = None
    proxy_process_measurement: str | None = None


@dataclass(frozen=True, slots=True)
class _BoundMCPPeer:
    record_id: str
    proxy_pid: int
    proxy_process_start_time: str
    proxy_process_measurement: str


def _posix_uid(account_id: str) -> int:
    if account_id.startswith("uid:"):
        try:
            uid = int(account_id.removeprefix("uid:"))
        except ValueError as exc:
            raise AuthenticationError("local binding process account is invalid") from exc
        if uid >= 0:
            return uid
    if account_id.startswith("sid:S-"):
        return 0
    raise AuthenticationError("local binding process account is invalid")


def _binding_actor(binding: CredentialBinding, *, now: int) -> VerifiedActor:
    binding.require_active(now=now)
    common = {
        "domain_id": binding.domain_id,
        "harness_id": binding.harness_id,
        "credential_id": binding.credential_id,
        "credential_epoch": binding.credential_epoch,
        "binding_assurance": binding.binding_assurance,
    }
    if binding.guest_id is not None:
        return VerifiedActor(kind=ActorKind.HOST_GUEST_HARNESS, guest_id=binding.guest_id, **common)
    if binding.principal_id is None:
        raise AuthenticationError("local binding credential lacks positive authority")
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        principal_id=binding.principal_id,
        **common,
    )


def _load_capability_root(path: Path) -> bytes:
    if host_platform() == "windows":
        from agentnet.windows_security import read_private_file

        try:
            value = read_private_file(path, max_bytes=MAX_CAPABILITY_ROOT_BYTES)
        except Exception as exc:
            raise GateBlocked("G05", "local IPC capability root is unavailable") from exc
        if len(value) < 32:
            raise GateBlocked("G05", "local IPC capability root must contain at least 256 bits")
        return value
    try:
        parent = path.parent.lstat()
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise GateBlocked("G05", "local IPC capability root is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            path.parent.is_symlink()
            or not stat.S_ISDIR(parent.st_mode)
            or parent.st_uid != os.geteuid()
            or parent.st_mode & 0o077
            or not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or metadata.st_size < 32
            or metadata.st_size > MAX_CAPABILITY_ROOT_BYTES
        ):
            raise GateBlocked("G05", "local IPC capability root must be an owner-only file")
        value = os.read(descriptor, MAX_CAPABILITY_ROOT_BYTES + 1)
        if len(value) != metadata.st_size:
            raise GateBlocked("G05", "local IPC capability root changed during bounded read")
        return value
    finally:
        os.close(descriptor)


class LocalBindingService:
    """Own the private socket, current-epoch actor resolution, and issuance."""

    def __init__(
        self,
        core: LocalBindingCore,
        *,
        config: LocalBindingConfig,
        socket_path: Path | str,
        mcp_bootstrap_socket_path: Path | str,
        capability_root: bytes,
        binding_repository: EndpointBindingRepository,
        clock: Any = None,
    ) -> None:
        self.core = core
        self.config = config
        self.socket_path = socket_path
        self.mcp_bootstrap_socket_path = mcp_bootstrap_socket_path
        self._capability_root = bytes(capability_root)
        self._binding_repository = binding_repository
        self._clock = clock or (lambda: int(time.time()))
        server_type = WindowsNamedPipeIPCServer if host_platform() == "windows" else UnixIPCServer
        self.server = server_type(
            socket_path,
            capability_root=self._capability_root,
            replay_store=core.store,
            handler=self._handle_ipc,
            max_frame=config.max_frame_bytes,
            clock=self._clock,
        )
        self._mcp_launches: dict[str, _MCPLaunchRecord] = {}
        self._issued_sessions: dict[str, BoundHarnessSession] = {}
        self._mcp_lock = threading.RLock()
        self.bootstrap_generation = secrets.token_urlsafe(24)
        self._proxy_measurement = linux_process_probe(os.getpid())[1]
        self._proxy_module = Path(__file__).with_name("mcp_proxy.py").resolve()
        self._proxy_module_identity = self._proxy_module.stat()
        if host_platform() == "windows":
            self.mcp_bootstrap_server = WindowsMCPBootstrapServer(
                str(mcp_bootstrap_socket_path),
                bind_peer=self._bind_mcp_peer,
                handler=self._handle_mcp_peer,
                generation=self.bootstrap_generation,
                assurance=MCP_BOOTSTRAP_ASSURANCE,
                max_frame=config.max_frame_bytes,
            )
        else:
            self.mcp_bootstrap_server = UnixMCPBootstrapServer(
                mcp_bootstrap_socket_path,
                bind_peer=self._bind_mcp_peer,
                handler=self._handle_mcp_peer,
                generation=self.bootstrap_generation,
                assurance=MCP_BOOTSTRAP_ASSURANCE,
                max_frame=config.max_frame_bytes,
            )

    def _current_session(self, harness_id: str) -> BoundHarnessSession:
        row = self.core.store.fetch_one(
            "SELECT domain_id FROM harnesses WHERE harness_id=?",
            (harness_id,),
        )
        if row is None or row["domain_id"] != self.core.config.domain_id:
            raise AuthenticationError("local binding exact endpoint domain is unavailable")
        endpoint = self._binding_repository.load_current(
            domain_id=row["domain_id"],
            harness_id=harness_id,
        )
        return BoundHarnessSession(endpoint=endpoint)

    def _activate_or_verify_measured_endpoint(
        self,
        *,
        actor: VerifiedActor | None,
        harness_id: str,
        platform: str,
        account_id: str,
        pid: int,
        start_time: str,
        executable_measurement: str,
    ) -> str:
        """Bind the endpoint to the exact process instance presenting itself."""

        lifecycle = EndpointLifecycleService(self.core.store, clock=self._clock)
        status = lifecycle.status(endpoint_id=harness_id)
        instance_digest = exact_process_measurement(
            platform=platform,
            account_id=account_id,
            pid=pid,
            start_time=start_time,
            executable_measurement=executable_measurement,
        )
        if status.state is EndpointActivationState.RESTART_REQUIRED:
            if actor is None or actor.harness_id != harness_id:
                raise AuthenticationError(
                    "explicit endpoint restart requires its verified harness actor"
                )
            status = lifecycle.record_user_restart(
                actor=actor,
                expected_generation=status.adapter_generation,
                process_measurement=instance_digest,
            )
        if status.state is not EndpointActivationState.CONNECTED:
            raise AuthenticationError("exact endpoint binding is not connected")
        if status.process_measurement != instance_digest:
            if actor is None or actor.harness_id != harness_id:
                raise AuthenticationError(
                    "endpoint process rebinding requires its verified harness actor"
                )
            status = lifecycle.record_process_reconnect(
                actor=actor,
                expected_generation=status.adapter_generation,
                process_measurement=instance_digest,
            )
            if status.process_measurement != instance_digest:
                raise AuthenticationError("endpoint process measurement changed")
        return instance_digest

    def _session_actor(
        self,
        session: BoundHarnessSession,
        *,
        process_measurement: str | None = None,
    ) -> VerifiedActor:
        endpoint = self._binding_repository.verify_current(
            session.endpoint,
            process_measurement=process_measurement,
        )
        binding = load_credential_binding(self.core.store, session.credential_id)
        if (
            binding.domain_id != endpoint.domain_id
            or binding.principal_id != endpoint.principal_id
            or binding.harness_id != session.harness_id
            or binding.credential_id != endpoint.credential_id
            or binding.credential_epoch != session.credential_epoch
            or binding.harness_credential_epoch != session.credential_epoch
        ):
            raise AuthenticationError("local binding exact endpoint credential is stale")
        return _binding_actor(binding, now=self._clock())

    def dispatcher_for_harness(self, harness_id: str, *, binding: str) -> CanonicalToolDispatcher:
        session = self._current_session(harness_id)
        row = self.core.store.fetch_one("SELECT kind FROM harnesses WHERE harness_id=?", (harness_id,))
        if row is None or row["kind"] not in ADAPTER_CAPABILITIES:
            raise AuthenticationError("local binding harness kind is unsupported")
        if ADAPTER_CAPABILITIES[row["kind"]].local_binding != binding:
            raise AuthorizationError("local binding mechanism does not match the enrolled harness kind")
        return CanonicalToolDispatcher(self.core, lambda: self._session_actor(session))

    def create_mcp_binding(self, harness_id: str) -> FastMCP:
        """Create an epoch-fenced MCP binding for a non-Pi enrolled harness."""

        return create_mcp_binding(self.dispatcher_for_harness(harness_id, binding="mcp"))

    def issue_child_capability(
        self,
        *,
        harness_id: str,
        pid: int,
        session_id: str,
        actor: VerifiedActor | None = None,
        expected_process_start_time: str | None = None,
        expected_process_measurement: str | None = None,
    ) -> IssuedChildCapability:
        """Measure a running harness and seal its exact local-binding process."""

        row = self.core.store.fetch_one(
            "SELECT kind FROM harnesses WHERE harness_id=?",
            (harness_id,),
        )
        if row is None or row["kind"] not in ADAPTER_CAPABILITIES:
            raise AuthenticationError("local binding harness kind is unsupported")
        binding = ADAPTER_CAPABILITIES[row["kind"]].local_binding
        if binding == "none":
            raise AuthorizationError("enrolled harness has no approved local binding")
        if binding != "direct_ipc":
            raise AuthorizationError("MCP harnesses require peer-credential launch registration")
        try:
            identity = measure_process_identity(pid)
        except AuthenticationError as exc:
            raise AuthenticationError("IPC child process is unavailable") from exc
        if identity.account_id != current_account_id():
            raise AuthenticationError("IPC child process owner is not the ordinary extension user")
        start_time = identity.start_time
        measurement = identity.executable_measurement
        if (
            expected_process_start_time is not None
            and expected_process_start_time != start_time
        ) or (
            expected_process_measurement is not None
            and expected_process_measurement != measurement
        ):
            raise AuthenticationError("local binding child changed before issuance")
        endpoint_measurement = self._activate_or_verify_measured_endpoint(
            actor=actor,
            harness_id=harness_id,
            platform=identity.platform,
            account_id=identity.account_id,
            pid=pid,
            start_time=start_time,
            executable_measurement=measurement,
        )
        session = self._current_session(harness_id)
        self._session_actor(session, process_measurement=endpoint_measurement)
        now = self._clock()
        binding_record = load_credential_binding(self.core.store, session.credential_id)
        expires_at = min(now + self.config.capability_ttl_seconds, binding_record.expires_at)
        if expires_at <= now:
            raise AuthenticationError("IPC child credential expires before capability issuance")
        claims = IPCSessionClaims(
            schema="agentnet.ipc.session.v1",
            capability_id=secrets.token_urlsafe(32),
            harness_id=session.harness_id,
            credential_id=session.credential_id,
            credential_epoch=session.credential_epoch,
            binding=binding,
            process_binding="exact",
            child_process_measurement=None,
            allowed_methods=CANONICAL_TOOL_NAMES,
            platform=identity.platform,
            account_id=identity.account_id,
            uid=_posix_uid(identity.account_id),
            pid=pid,
            process_start_time=start_time,
            process_measurement=measurement,
            session_id=session_id,
            issued_at=now,
            expires_at=expires_at,
        )
        with self._mcp_lock:
            existing_session = self._issued_sessions.get(session_id)
            if existing_session is not None and existing_session != session:
                raise AuthenticationError("IPC session is already bound to another exact endpoint")
            self._issued_sessions[session_id] = session
        return IssuedChildCapability(
            capability=mint_inherited_session_capability(self._capability_root, claims),
            session_id=session_id,
            harness_id=session.harness_id,
            credential_id=session.credential_id,
            credential_epoch=session.credential_epoch,
            expires_at=expires_at,
            socket_path=self.socket_path,
        )

    def _purge_mcp_launches(self) -> None:
        now = self._clock()
        stale: list[str] = []
        for record_id, record in self._mcp_launches.items():
            if record.expires_at <= now:
                stale.append(record_id)
                continue
            try:
                parent_identity = measure_process_identity(record.parent_pid)
            except AuthenticationError:
                stale.append(record_id)
                continue
            if (
                parent_identity.platform,
                parent_identity.account_id,
                parent_identity.start_time,
                parent_identity.executable_measurement,
            ) != (
                record.platform,
                record.account_id,
                record.parent_process_start_time,
                record.parent_process_measurement,
            ):
                stale.append(record_id)
        for record_id in stale:
            self._mcp_launches.pop(record_id, None)

    def register_mcp_launch(
        self,
        *,
        harness_id: str,
        pid: int,
        session_id: str,
        actor: VerifiedActor | None = None,
        expected_process_start_time: str | None = None,
        expected_process_measurement: str | None = None,
    ) -> RegisteredMCPLaunch:
        """Register one exact MCP harness parent; return no identity capability."""

        row = self.core.store.fetch_one(
            "SELECT kind FROM harnesses WHERE harness_id=?", (harness_id,)
        )
        if row is None or row["kind"] not in ADAPTER_CAPABILITIES:
            raise AuthenticationError("local binding harness kind is unsupported")
        if ADAPTER_CAPABILITIES[row["kind"]].local_binding != "mcp":
            raise AuthorizationError("enrolled harness does not use the MCP bootstrap binding")
        try:
            identity = measure_process_identity(pid)
        except AuthenticationError as exc:
            raise AuthenticationError("MCP harness process is unavailable") from exc
        if identity.account_id != current_account_id():
            raise AuthenticationError("MCP harness owner is not the ordinary extension user")
        start_time = identity.start_time
        measurement = identity.executable_measurement
        if (
            expected_process_start_time is not None
            and expected_process_start_time != start_time
        ) or (
            expected_process_measurement is not None
            and expected_process_measurement != measurement
        ):
            raise AuthenticationError("MCP harness changed before registration")
        endpoint_measurement = self._activate_or_verify_measured_endpoint(
            actor=actor,
            harness_id=harness_id,
            platform=identity.platform,
            account_id=identity.account_id,
            pid=pid,
            start_time=start_time,
            executable_measurement=measurement,
        )
        session = self._current_session(harness_id)
        self._session_actor(session, process_measurement=endpoint_measurement)
        binding = load_credential_binding(self.core.store, session.credential_id)
        now = self._clock()
        expires_at = min(now + self.config.capability_ttl_seconds, binding.expires_at)
        if expires_at <= now:
            raise AuthenticationError("MCP harness credential expires before registration")
        with self._mcp_lock:
            self._purge_mcp_launches()
            existing = next(
                (
                    candidate
                    for candidate in self._mcp_launches.values()
                    if (
                        candidate.session == session
                        and candidate.session_id == session_id
                        and candidate.platform == identity.platform
                        and candidate.account_id == identity.account_id
                        and candidate.uid == _posix_uid(identity.account_id)
                        and candidate.parent_pid == pid
                        and candidate.parent_process_start_time == start_time
                        and candidate.parent_process_measurement == measurement
                    )
                ),
                None,
            )
            if existing is not None:
                existing.expires_at = expires_at
                record = existing
            else:
                record = _MCPLaunchRecord(
                    record_id=secrets.token_urlsafe(32),
                    session=session,
                    session_id=session_id,
                    platform=identity.platform,
                    account_id=identity.account_id,
                    uid=_posix_uid(identity.account_id),
                    parent_pid=pid,
                    parent_process_start_time=start_time,
                    parent_process_measurement=measurement,
                    endpoint_process_measurement=endpoint_measurement,
                    expires_at=expires_at,
                )
            self._mcp_launches = {
                record_id: existing
                for record_id, existing in self._mcp_launches.items()
                if record_id == record.record_id
                or (
                    existing.session.harness_id != harness_id
                    and existing.session_id != session_id
                )
            }
            self._mcp_launches[record.record_id] = record
        return RegisteredMCPLaunch(
            session_id=session_id,
            harness_id=harness_id,
            credential_id=session.credential_id,
            credential_epoch=session.credential_epoch,
            expires_at=expires_at,
            bootstrap_socket_path=self.mcp_bootstrap_socket_path,
            bootstrap_generation=self.bootstrap_generation,
        )

    def register_or_issue_child(
        self,
        *,
        harness_id: str,
        pid: int,
        session_id: str,
        actor: VerifiedActor | None = None,
        expected_process_start_time: str | None = None,
        expected_process_measurement: str | None = None,
    ) -> IssuedChildCapability | RegisteredMCPLaunch:
        row = self.core.store.fetch_one(
            "SELECT kind FROM harnesses WHERE harness_id=?", (harness_id,)
        )
        if row is None or row["kind"] not in ADAPTER_CAPABILITIES:
            raise AuthenticationError("local binding harness kind is unsupported")
        if ADAPTER_CAPABILITIES[row["kind"]].local_binding == "mcp":
            return self.register_mcp_launch(
                harness_id=harness_id,
                pid=pid,
                session_id=session_id,
                actor=actor,
                expected_process_start_time=expected_process_start_time,
                expected_process_measurement=expected_process_measurement,
            )
        return self.issue_child_capability(
            harness_id=harness_id,
            pid=pid,
            session_id=session_id,
            actor=actor,
            expected_process_start_time=expected_process_start_time,
            expected_process_measurement=expected_process_measurement,
        )

    def _proxy_has_expected_module_open(self, pid: int) -> bool:
        expected = self._proxy_module_identity
        try:
            opened = psutil.Process(pid).open_files()
        except (OSError, psutil.Error):
            return False
        for candidate in opened:
            try:
                actual = Path(candidate.path).stat()
            except OSError:
                continue
            if (
                actual.st_dev == expected.st_dev
                and actual.st_ino == expected.st_ino
                and stat.S_ISREG(actual.st_mode)
            ):
                return True
        return False

    def _verify_proxy_identity(self, peer: UnixProcessPeer) -> None:
        if (
            peer.account_id != current_account_id()
            or peer.process_measurement != self._proxy_measurement
        ):
            raise AuthenticationError("MCP proxy executable identity rejected")
        try:
            arguments = tuple(psutil.Process(peer.pid).cmdline())
        except (OSError, psutil.Error) as exc:
            raise AuthenticationError("MCP proxy command identity is unavailable") from exc
        if arguments[1:] != ("-m", "agentnet.bindings.mcp_proxy"):
            raise AuthenticationError("MCP proxy module command identity rejected")
        if not self._proxy_has_expected_module_open(peer.pid):
            raise AuthenticationError("MCP proxy loaded-module identity rejected")

    def _bind_mcp_peer(self, peer: UnixProcessPeer) -> _BoundMCPPeer:
        self._verify_proxy_identity(peer)
        with self._mcp_lock:
            self._purge_mcp_launches()
            matches = [
                record
                for record in self._mcp_launches.values()
                if (
                    record.platform,
                    record.account_id,
                    record.uid,
                    record.parent_pid,
                    record.parent_process_start_time,
                    record.parent_process_measurement,
                )
                == (
                    peer.platform,
                    peer.parent_account_id,
                    peer.uid,
                    peer.parent_pid,
                    peer.parent_process_start_time,
                    peer.parent_process_measurement,
                )
            ]
            if len(matches) != 1:
                raise AuthenticationError("MCP proxy has no exact registered parent launch")
            record = matches[0]
            actual_proxy = (
                peer.pid,
                peer.process_start_time,
                peer.process_measurement,
            )
            expected_proxy = (
                record.proxy_pid,
                record.proxy_process_start_time,
                record.proxy_process_measurement,
            )
            if record.proxy_pid is None:
                (
                    record.proxy_pid,
                    record.proxy_process_start_time,
                    record.proxy_process_measurement,
                ) = actual_proxy
            elif actual_proxy != expected_proxy:
                raise AuthenticationError("MCP launch is already bound to a different proxy")
            self._session_actor(
                record.session,
                process_measurement=record.endpoint_process_measurement,
            )
            return _BoundMCPPeer(record.record_id, *actual_proxy)

    def _validate_bound_mcp_peer(
        self, bound: _BoundMCPPeer, peer: UnixProcessPeer
    ) -> _MCPLaunchRecord:
        with self._mcp_lock:
            self._purge_mcp_launches()
            record = self._mcp_launches.get(bound.record_id)
            if record is None:
                raise AuthenticationError("MCP registered launch is unavailable")
            if (
                peer.pid,
                peer.process_start_time,
                peer.process_measurement,
            ) != (
                bound.proxy_pid,
                bound.proxy_process_start_time,
                bound.proxy_process_measurement,
            ):
                raise AuthenticationError("MCP proxy process changed after bootstrap")
            if peer.parent_pid != record.parent_pid:
                self._mcp_launches.pop(record.record_id, None)
                raise AuthenticationError("MCP proxy parent exited or changed")
            parent_identity = measure_process_identity(record.parent_pid)
            if (
                parent_identity.platform,
                parent_identity.account_id,
                parent_identity.start_time,
                parent_identity.executable_measurement,
            ) != (
                record.platform,
                record.account_id,
                record.parent_process_start_time,
                record.parent_process_measurement,
            ):
                self._mcp_launches.pop(record.record_id, None)
                raise AuthenticationError("MCP harness PID was reused")
            self._verify_proxy_identity(peer)
            self._session_actor(
                record.session,
                process_measurement=record.endpoint_process_measurement,
            )
            return record

    async def _handle_mcp_peer(
        self,
        bound: _BoundMCPPeer,
        peer: UnixProcessPeer,
        request: dict[str, Any],
    ) -> dict[str, Any]:
        record = self._validate_bound_mcp_peer(bound, peer)
        if set(request) != {"arguments", "method"}:
            raise ValidationError("MCP bootstrap request schema is not exact")
        method = request.get("method")
        arguments = request.get("arguments")
        if method not in CANONICAL_TOOL_NAMES or not isinstance(arguments, dict):
            raise ValidationError("MCP bootstrap canonical tool request is invalid")
        actor = self._session_actor(
            record.session,
            process_measurement=record.endpoint_process_measurement,
        )
        result = CanonicalToolDispatcher(self.core, lambda: actor).call(method, arguments)
        return {"ok": True, "result": result}

    async def _handle_ipc(
        self,
        claims: IPCSessionClaims,
        request: dict[str, Any],
    ) -> dict[str, Any]:
        method = request.get("method") if isinstance(request, dict) else None
        if not isinstance(method, str) or method not in claims.allowed_methods:
            raise AuthorizationError("IPC method is outside the child capability")
        with self._mcp_lock:
            session = self._issued_sessions.get(claims.session_id)
        if session is None:
            raise AuthenticationError("IPC descriptor is not bound to a current exact endpoint")
        if (
            claims.harness_id != session.harness_id
            or claims.credential_id != session.credential_id
            or claims.credential_epoch != session.credential_epoch
        ):
            raise AuthenticationError("IPC descriptor crossed its exact endpoint binding")
        endpoint_measurement = self._activate_or_verify_measured_endpoint(
            actor=None,
            harness_id=claims.harness_id,
            platform=claims.platform,
            account_id=claims.account_id,
            pid=claims.pid,
            start_time=claims.process_start_time,
            executable_measurement=claims.process_measurement,
        )
        actor = self._session_actor(
            session,
            process_measurement=endpoint_measurement,
        )
        row = self.core.store.fetch_one(
            "SELECT kind FROM harnesses WHERE harness_id=?",
            (claims.harness_id,),
        )
        if row is None or row["kind"] not in ADAPTER_CAPABILITIES:
            raise AuthorizationError("local IPC harness binding is unavailable")
        expected_binding = ADAPTER_CAPABILITIES[row["kind"]].local_binding
        if claims.binding != expected_binding:
            raise AuthorizationError("local IPC mechanism crossed its enrolled harness binding")
        dispatcher = CanonicalToolDispatcher(self.core, lambda: actor)
        arguments = request.get("arguments")
        if not isinstance(arguments, dict) or set(request) != {"arguments", "method"}:
            raise ValidationError("IPC canonical tool request schema is not exact")
        return {"ok": True, "result": dispatcher.call(method, arguments)}

    async def start(self) -> None:
        await self.server.start()
        try:
            await self.mcp_bootstrap_server.start()
        except Exception:
            await self.server.close()
            raise

    async def close(self) -> None:
        await self.mcp_bootstrap_server.close()
        await self.server.close()
        with self._mcp_lock:
            self._mcp_launches.clear()
            self._issued_sessions.clear()


def _configured_path(data_dir: Path, configured: Path) -> Path:
    if configured.is_absolute():
        return configured
    if ".." in configured.parts:
        raise GateBlocked("G05", "local binding path escapes the data directory")
    return data_dir / configured


def _configured_unix_socket_path(data_dir: Path, configured: Path) -> Path:
    path = _configured_path(data_dir, configured)
    # Linux has 108 bytes including NUL; Darwin sockaddr_un.sun_path has 104.
    limit = 103 if host_platform() == "macos" else 107
    if len(os.fsencode(path)) > limit:
        raise GateBlocked("G05", "local binding Unix socket path exceeds the platform limit")
    return path


def _windows_pipe_path(kind: str) -> str:
    if kind not in {"direct", "mcp"}:
        raise GateBlocked("G05", "local binding pipe kind is invalid")
    return rf"\\.\pipe\agentnet-{kind}-{secrets.token_urlsafe(24)}"


def create_local_binding_service(core: LocalBindingCore) -> LocalBindingService:
    core.config.require_feature("local_bindings")
    configured = core.config.local_bindings
    if configured is None:
        raise GateBlocked("G05", "local binding configuration is absent")
    root_path = _configured_path(core.config.data_dir, configured.capability_root_path)
    if host_platform() == "windows":
        socket_path: Path | str = _windows_pipe_path("direct")
        mcp_bootstrap_socket_path: Path | str = _windows_pipe_path("mcp")
    else:
        socket_path = _configured_unix_socket_path(core.config.data_dir, configured.socket_path)
        mcp_bootstrap_socket_path = _configured_unix_socket_path(
            core.config.data_dir, configured.mcp_bootstrap_socket_path
        )
    return LocalBindingService(
        core,
        config=configured,
        socket_path=socket_path,
        mcp_bootstrap_socket_path=mcp_bootstrap_socket_path,
        capability_root=_load_capability_root(root_path),
        binding_repository=EndpointBindingRepository(core.store, root_path.parent),
    )


__all__ = [
    "BoundHarnessSession",
    "IssuedChildCapability",
    "RegisteredMCPLaunch",
    "LocalBindingService",
    "create_local_binding_service",
]
