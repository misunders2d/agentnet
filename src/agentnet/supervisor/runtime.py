"""Lifecycle and recovery around documented native harness drivers."""

from __future__ import annotations

import json
import ctypes
import os
import stat
import sys
import threading
import time
from asyncio import CancelledError as AsyncCancelledError
from concurrent.futures import CancelledError as FutureCancelledError
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Any, Callable, Literal

import psutil

try:  # POSIX-only; capability methods below fail closed when unavailable.
    import fcntl
except ModuleNotFoundError:  # pragma: no cover - exercised on Windows CI
    fcntl = None  # type: ignore[assignment]

from agentnet.adapters.auth import HarnessAuthInjection
from agentnet.adapters.base import AdapterLaunchSpec, ExecutableProbe
from agentnet.adapters.native import (
    NativeHarnessDriver,
    NativeProtocolError,
    create_native_driver,
)
from agentnet.adapters.specs import detect_executable
from agentnet.bindings.ipc import linux_process_probe
from agentnet.bindings.mcp_bootstrap import MCP_BOOTSTRAP_ASSURANCE
from agentnet.errors import AuthorizationError, GateBlocked, ValidationError
from agentnet.host_security import measure_process_identity
from agentnet.security.signatures import canonical_json

if TYPE_CHECKING:
    from agentnet.supervisor.workers import CleanWorkerAdmission


class AdapterProcessError(RuntimeError):
    """A native background process failed without a foreground fallback."""


_MFD_ALLOW_SEALING = 0x0002
_F_ADD_SEALS = 1033
_F_SEAL_SEAL = 0x0001
_F_SEAL_SHRINK = 0x0002
_F_SEAL_GROW = 0x0004
_F_SEAL_WRITE = 0x0008


@dataclass(frozen=True, slots=True)
class RuntimeStatus:
    harness: str
    phase: Literal["new", "starting", "ready", "offline", "degraded", "stopped"]
    generation: int
    restart_count: int
    semantic_mode: str
    native_surface: str
    pinned_version: str
    version_match: bool
    clean_worker_admitted: bool
    last_heartbeat_at: int | None


@dataclass(frozen=True, slots=True)
class BackgroundTurnAuthorization:
    """Server-authorized exact event that may enter a clean worker."""

    decision_id: str
    harness_id: str
    event_id: str
    envelope_digest: str
    event_type: str
    classification: str
    policy_revision: int
    expires_at: int
    task_grant_id: str | None = None

    def __post_init__(self) -> None:
        identifiers = (self.decision_id, self.harness_id, self.event_id, self.event_type)
        if (
            any(not isinstance(value, str) or not 1 <= len(value) <= 256 for value in identifiers)
            or not isinstance(self.envelope_digest, str)
            or len(self.envelope_digest) != 64
            or any(character not in "0123456789abcdef" for character in self.envelope_digest)
            or self.classification not in {"C0", "C1", "C2", "C3"}
            or type(self.policy_revision) is not int
            or self.policy_revision < 1
            or type(self.expires_at) is not int
            or self.expires_at <= int(time.time())
            or (
                self.task_grant_id is not None
                and (not isinstance(self.task_grant_id, str) or not 1 <= len(self.task_grant_id) <= 256)
            )
            or self.event_type in {"task_assignment", "handoff"}
            and self.task_grant_id is None
        ):
            raise AuthorizationError("background turn authorization is invalid or expired")

    @classmethod
    def from_mapping(cls, value: dict[str, Any]) -> "BackgroundTurnAuthorization":
        if set(value) != {
            "decision_id",
            "harness_id",
            "event_id",
            "envelope_digest",
            "event_type",
            "classification",
            "policy_revision",
            "expires_at",
            "task_grant_id",
        }:
            raise AuthorizationError("background turn authorization schema is not exact")
        return cls(**value)


class BackgroundAdapterRuntime:
    """Own one private native harness session and recover it after death."""

    def __init__(
        self,
        spec: AdapterLaunchSpec,
        *,
        request_timeout_seconds: float = 2.0,
        heartbeat_interval_seconds: float = 1.0,
        max_restart_attempts: int = 3,
        auth: HarnessAuthInjection | None = None,
        clean_worker_admission: CleanWorkerAdmission | None = None,
        local_binding_issuer: Callable[[int, str], dict[str, Any]] | None = None,
    ) -> None:
        spec.validate()
        if not 0.05 <= request_timeout_seconds <= 60:
            raise ValueError("adapter request timeout is outside the bounded profile")
        if not 0.02 <= heartbeat_interval_seconds <= 60:
            raise ValueError("adapter heartbeat interval is outside the bounded profile")
        if not 0 <= max_restart_attempts <= 20:
            raise ValueError("adapter restart ceiling is outside the bounded profile")
        if spec.semantic_mode == "clean_worker":
            if auth is None or clean_worker_admission is None:
                raise GateBlocked("G03", "semantic native runtime lacks signed clean-worker admission")
        elif auth is not None or clean_worker_admission is not None:
            raise GateBlocked("G03", "authentication cannot be attached before clean-worker admission")
        self.spec = spec
        self.request_timeout_seconds = request_timeout_seconds
        self.heartbeat_interval_seconds = heartbeat_interval_seconds
        self.max_restart_attempts = max_restart_attempts
        self._auth = auth
        self._clean_worker_admission = clean_worker_admission
        self._local_binding_issuer = local_binding_issuer
        self._local_binding_socket_identity: tuple[int, int, str] | None = None
        self._local_binding_expires_at: int | None = None
        self._local_binding_next_renewal = 0.0
        self._local_binding_renewal_failures = 0
        self._local_binding_delivery: Any | None = None
        self._auth_materialized = False
        self._lock = threading.RLock()
        self._driver: NativeHarnessDriver | None = None
        self._phase: Literal["new", "starting", "ready", "offline", "degraded", "stopped"] = "new"
        self._generation = 0
        self._restart_count = 0
        self._restart_attempts_this_run = 0
        self._last_heartbeat_at: int | None = None
        self._probe: ExecutableProbe | None = None
        self._stop_event = threading.Event()
        self._monitor_thread: threading.Thread | None = None
        self._state_path = spec.state_dir / "runtime-state.json"
        self._load_content_free_state()

    def _load_content_free_state(self) -> None:
        try:
            value = json.loads(self._state_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return
        if not isinstance(value, dict) or value.get("session_id") != self.spec.session_id:
            return
        generation = value.get("generation", 0)
        restart_count = value.get("restart_count", 0)
        if type(generation) is int and generation >= 0:
            self._generation = generation
        if type(restart_count) is int and restart_count >= 0:
            self._restart_count = restart_count

    def _persist_content_free_state(self) -> None:
        value = {
            "schema": "agentnet.adapter-runtime-state.v2",
            "harness": self.spec.harness,
            "session_id": self.spec.session_id,
            "native_surface": self.spec.transport,
            "phase": self._phase,
            "generation": self._generation,
            "restart_count": self._restart_count,
            "semantic_mode": self.spec.semantic_mode,
            "pinned_version": self.spec.pinned_version,
            "version_match": bool(self._probe and self._probe.matches_pin),
            "clean_worker_admitted": self._clean_worker_admission is not None,
            "last_heartbeat_at": self._last_heartbeat_at,
        }
        temporary = self._state_path.with_suffix(".tmp")
        temporary.write_text(
            json.dumps(value, allow_nan=False, separators=(",", ":"), sort_keys=True),
            encoding="utf-8",
        )
        os.chmod(temporary, 0o600)
        os.replace(temporary, self._state_path)

    def _environment(
        self,
        *,
        local_binding_fd: int | None = None,
        local_binding_endpoint: str | None = None,
    ) -> dict[str, str]:
        """Complete allowlist; vendor/user credentials are never inherited."""

        environment = {
            "AGENTNET_BACKGROUND_SESSION_ID": self.spec.session_id,
            "AGENTNET_HARNESS_KIND": self.spec.harness,
            "AGENTNET_SEMANTIC_MODE": self.spec.semantic_mode,
            "AGENTNET_STATE_DIR": str(self.spec.state_dir),
            "CODEX_HOME": str(self.spec.state_dir / "codex"),
            "HOME": str(self.spec.home_dir),
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "NO_COLOR": "1",
            "PATH": "/usr/local/bin:/usr/bin:/bin",
            "PI_CODING_AGENT_DIR": str(self.spec.state_dir / "pi"),
            "PI_TELEMETRY": "0",
            "PYTHONNOUSERSITE": "1",
            "TMPDIR": str(self.spec.temp_dir),
            "XDG_CACHE_HOME": str(self.spec.state_dir / "xdg-cache"),
            "XDG_CONFIG_HOME": str(self.spec.state_dir / "xdg-config"),
            "XDG_DATA_HOME": str(self.spec.state_dir / "xdg-data"),
        }
        broker_enabled = (
            self._clean_worker_admission is not None
            and self._clean_worker_admission.broker_origin is not None
        )
        if self.spec.harness == "pi" and not broker_enabled:
            environment["PI_OFFLINE"] = "1"
        if self._auth is not None:
            environment.update(self._auth.environment_for(self.spec.harness))
        if local_binding_fd is not None and local_binding_endpoint is not None:
            raise GateBlocked("G05", "local binding has multiple capability locators")
        if local_binding_fd is not None:
            environment["AGENTNET_LOCAL_BINDING_FD"] = str(local_binding_fd)
        if local_binding_endpoint is not None:
            environment["AGENTNET_LOCAL_BINDING_ENDPOINT"] = local_binding_endpoint
        return environment

    @staticmethod
    def _binding_descriptors() -> tuple[int, int | None]:
        if sys.platform == "darwin":
            reader, writer = os.pipe()
            os.set_inheritable(reader, True)
            os.set_inheritable(writer, False)
            return reader, writer
        if sys.platform != "linux":
            raise GateBlocked("G05", "process-bound local binding requires a host adapter")
        if hasattr(os, "memfd_create"):
            return (
                os.memfd_create(
                    "agentnet-local-binding",
                    flags=getattr(os, "MFD_ALLOW_SEALING", _MFD_ALLOW_SEALING),
                ),
                None,
            )
        libc = ctypes.CDLL(None, use_errno=True)
        create = getattr(libc, "memfd_create", None)
        if create is None:
            raise GateBlocked("G05", "process-bound local binding requires Linux memfd")
        create.argtypes = [ctypes.c_char_p, ctypes.c_uint]
        create.restype = ctypes.c_int
        descriptor = int(create(b"agentnet-local-binding", _MFD_ALLOW_SEALING))
        if descriptor < 0:
            raise GateBlocked("G05", "process-bound local binding memfd creation failed")
        return descriptor, None

    def _mcp_locator_path(self) -> Path:
        return self.spec.state_dir / "mcp-bootstrap-locator.json"

    def _clear_mcp_locator(self) -> None:
        path = self._mcp_locator_path()
        if sys.platform == "win32":
            if not path.exists():
                return
            from agentnet.windows_security import require_private_path

            require_private_path(path, directory=False)
            path.unlink()
            return
        try:
            metadata = path.lstat()
        except FileNotFoundError:
            return
        if path.is_symlink() or not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid():
            raise GateBlocked("G05", "MCP bootstrap locator path is not an owner file")
        path.unlink()

    def _publish_mcp_locator(self, issued: dict[str, Any]) -> None:
        if (
            set(issued)
            != {
                "schema",
                "session_id",
                "harness_id",
                "credential_id",
                "credential_epoch",
                "expires_at",
                "bootstrap_socket_path",
                "bootstrap_generation",
                "assurance",
            }
            or issued.get("schema") != "agentnet.mcp.registered-launch.v1"
            or issued.get("session_id") != self.spec.session_id
            or issued.get("harness_id") != self.spec.harness_id
            or issued.get("assurance") != MCP_BOOTSTRAP_ASSURANCE
            or not isinstance(issued.get("bootstrap_socket_path"), str)
            or not isinstance(issued.get("bootstrap_generation"), str)
            or not 24 <= len(issued["bootstrap_generation"]) <= 128
            or type(issued.get("expires_at")) is not int
            or issued["expires_at"] <= int(time.time())
        ):
            raise GateBlocked("G05", "MCP launch registration response is invalid")
        socket_path = Path(issued["bootstrap_socket_path"])
        if sys.platform == "win32":
            if not issued["bootstrap_socket_path"].startswith(r"\\.\pipe\agentnet-mcp-"):
                raise GateBlocked("G05", "MCP bootstrap named-pipe locator rejected")
            socket_identity = (0, 0)
        else:
            try:
                socket_metadata = socket_path.lstat()
            except OSError as exc:
                raise GateBlocked("G05", "MCP bootstrap socket is unavailable after registration") from exc
            if (
                not stat.S_ISSOCK(socket_metadata.st_mode)
                or socket_metadata.st_uid != os.geteuid()
                or socket_metadata.st_mode & 0o077
            ):
                raise GateBlocked("G05", "MCP bootstrap socket ownership or mode rejected")
            socket_identity = (socket_metadata.st_dev, socket_metadata.st_ino)
        locator = canonical_json(
            {
                "generation": issued["bootstrap_generation"],
                "schema": "agentnet.mcp.bootstrap-locator.v1",
                "socket_path": issued["bootstrap_socket_path"],
            }
        )
        path = self._mcp_locator_path()
        temporary = path.with_name(f".{path.name}.{os.getpid()}.{threading.get_ident()}.tmp")
        try:
            if sys.platform == "win32":
                from agentnet.windows_security import write_private_file

                write_private_file(path, locator, force=True)
            else:
                flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
                descriptor = os.open(temporary, flags, 0o600)
                try:
                    os.write(descriptor, locator)
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
                os.replace(temporary, path)
                metadata = path.lstat()
                if path.is_symlink() or not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o077:
                    raise GateBlocked("G05", "MCP bootstrap locator publication failed closed")
            self._local_binding_socket_identity = (
                socket_identity[0],
                socket_identity[1],
                issued["bootstrap_generation"],
            )
            self._local_binding_expires_at = issued["expires_at"]
            remaining = issued["expires_at"] - int(time.time())
            self._local_binding_next_renewal = time.monotonic() + max(
                0.1, min(5.0, remaining / 3)
            )
            self._local_binding_renewal_failures = 0
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass

    def _activate_binding(
        self,
        descriptor: int | None,
        writer_descriptor: int | None,
        delivery: Any | None,
        pid: int,
        command: tuple[str, ...],
    ) -> None:
        if self._local_binding_issuer is None:
            return
        probe = self._probe
        if probe is None or probe.resolved_path is None:
            raise GateBlocked("G05", "local binding lacks its pinned executable probe")
        target_paths = {
            os.path.realpath(probe.resolved_path),
            probe.resolved_path,
        }
        wrapper_path = os.path.realpath(command[0])
        wrapped = wrapper_path not in target_paths
        deadline = time.monotonic() + self.request_timeout_seconds
        while True:
            try:
                identity = measure_process_identity(pid)
                executable = os.path.realpath(identity.executable_path)
                arguments = {
                    os.path.realpath(item)
                    for item in psutil.Process(pid).cmdline()
                    if os.path.isabs(item)
                }
            except Exception:
                executable = ""
                arguments = set()
            target_visible = bool(target_paths & arguments) or executable in target_paths
            wrapper_replaced = not wrapped or executable != wrapper_path
            if target_visible and wrapper_replaced:
                break
            if time.monotonic() >= deadline:
                raise GateBlocked("G05", "local binding child did not reach its pinned executable")
            time.sleep(0.01)
        issued = self._local_binding_issuer(pid, self.spec.session_id)
        if self.spec.harness == "pi":
            payload = canonical_json(issued)
            if delivery is not None:
                if descriptor is not None or writer_descriptor is not None:
                    raise GateBlocked("G05", "Pi binding delivery has conflicting transports")
                delivery.publish(payload, expected=measure_process_identity(pid))
                delivery.wait_delivered()
            elif descriptor is None:
                raise GateBlocked("G05", "Pi direct IPC binding descriptor is absent")
            elif writer_descriptor is None:
                self._seal_binding_descriptor(descriptor, payload)
            else:
                self._write_binding_pipe(writer_descriptor, payload)
        else:
            if descriptor is not None:
                raise GateBlocked("G05", "MCP binding must not inherit a descriptor")
            self._publish_mcp_locator(issued)

    def _maintain_mcp_binding(self) -> None:
        if self._local_binding_issuer is None or self.spec.harness not in {"claude", "codex"}:
            return
        driver = self._driver
        pid = driver.pid if driver is not None else None
        if pid is None or not driver.alive:
            raise GateBlocked("G05", "MCP harness is unavailable for launch renewal")
        identity = self._local_binding_socket_identity
        socket_changed = identity is None
        if identity is not None:
            if sys.platform == "win32":
                from agentnet.windows_security import read_private_file

                locator = json.loads(
                    read_private_file(
                        self._mcp_locator_path(),
                        max_bytes=4096,
                    )
                )
                socket_changed = locator.get("generation") != identity[2]
            else:
                locator = json.loads(self._mcp_locator_path().read_text(encoding="utf-8"))
                socket_path = Path(locator["socket_path"])
                try:
                    metadata = socket_path.lstat()
                except OSError:
                    socket_changed = True
                else:
                    socket_changed = (
                        metadata.st_dev,
                        metadata.st_ino,
                        locator.get("generation"),
                    ) != identity
        renewal_due = (
            time.monotonic() >= self._local_binding_next_renewal
            or self._local_binding_expires_at is None
            or self._local_binding_expires_at <= int(time.time()) + 5
        )
        if not socket_changed and not renewal_due:
            return
        issued = self._local_binding_issuer(pid, self.spec.session_id)
        self._publish_mcp_locator(issued)

    @staticmethod
    def _write_binding_pipe(descriptor: int, payload: bytes) -> None:
        if len(payload) > 65_536:
            raise GateBlocked("G05", "local binding capability response is oversized")
        view = memoryview(payload)
        while view:
            written = os.write(descriptor, view)
            if written <= 0:
                raise GateBlocked("G05", "local binding pipe write made no progress")
            view = view[written:]

    @staticmethod
    def _seal_binding_descriptor(descriptor: int, payload: bytes) -> None:
        if fcntl is None:
            raise GateBlocked("G05", "local binding descriptor sealing is unavailable")
        if len(payload) > 65_536:
            raise GateBlocked("G05", "local binding capability response is oversized")
        os.pwrite(descriptor, payload, 0)
        os.ftruncate(descriptor, len(payload))
        seals = _F_SEAL_SEAL | _F_SEAL_SHRINK | _F_SEAL_GROW | _F_SEAL_WRITE
        fcntl.fcntl(descriptor, getattr(fcntl, "F_ADD_SEALS", _F_ADD_SEALS), seals)


    def _resolved_command(self) -> tuple[str, ...]:
        probe = self._probe
        if probe is None or probe.resolved_path is None or not probe.matches_pin:
            raise GateBlocked("G01", "native background executable lacks a current pinned probe")
        command = (probe.resolved_path, *self.spec.arguments)
        if self._clean_worker_admission is not None:
            assert self._auth is not None
            self._clean_worker_admission.validate_runtime(
                self.spec,
                self._auth,
                probe.resolved_path,
            )
            return self._clean_worker_admission.wrap_command(command)
        return command

    def start(self) -> RuntimeStatus:
        with self._lock:
            if self._phase == "ready" and self._driver is not None and self._driver.alive:
                return self.status()
            self._probe = detect_executable(self.spec, timeout_seconds=self.request_timeout_seconds)
            if not self._probe.matches_pin:
                self._phase = "offline"
                self._persist_content_free_state()
                raise GateBlocked(
                    "G01",
                    f"{self.spec.harness} executable is absent or not pinned to {self.spec.pinned_version}",
                )
            self._stop_event.clear()
            self._restart_attempts_this_run = 0
            self._start_driver(recover=self._generation > 0, restart=False)
            if self._monitor_thread is None or not self._monitor_thread.is_alive():
                self._monitor_thread = threading.Thread(
                    target=self._monitor,
                    name=f"agentnet-native-monitor-{self.spec.harness}-{self.spec.session_id[:8]}",
                    daemon=True,
                )
                self._monitor_thread.start()
            return self.status()

    def _start_driver(self, *, recover: bool, restart: bool) -> None:
        if restart:
            if self._restart_attempts_this_run >= self.max_restart_attempts:
                self._phase = "degraded"
                self._persist_content_free_state()
                return
            self._restart_attempts_this_run += 1
            self._restart_count += 1
        self._phase = "starting"
        self._persist_content_free_state()
        driver = create_native_driver(self.spec)
        binding_descriptor: int | None = None
        binding_writer: int | None = None
        binding_delivery: Any | None = None
        try:
            command = self._resolved_command()
            if self._auth is not None and not self._auth_materialized:
                self._auth.materialize(self.spec)
                self._auth_materialized = True
            if self._local_binding_issuer is not None:
                if self.spec.harness == "pi":
                    previous_delivery, self._local_binding_delivery = (
                        self._local_binding_delivery,
                        None,
                    )
                    if previous_delivery is not None:
                        previous_delivery.close()
                    if sys.platform == "win32":
                        from agentnet.supervisor.windows_binding_delivery import (
                            WindowsBindingDelivery,
                        )

                        binding_delivery = WindowsBindingDelivery(
                            timeout_seconds=max(10.0, self.request_timeout_seconds * 5)
                        )
                        binding_delivery.start()
                        self._local_binding_delivery = binding_delivery
                    else:
                        binding_descriptor, binding_writer = self._binding_descriptors()
                else:
                    self._clear_mcp_locator()
            driver.start(
                command,
                environment=self._environment(
                    local_binding_fd=binding_descriptor,
                    local_binding_endpoint=(
                        binding_delivery.endpoint if binding_delivery is not None else None
                    ),
                ),
                recover=recover,
                timeout_seconds=self.request_timeout_seconds,
                inherited_fds=(binding_descriptor,) if binding_descriptor is not None else (),
                process_started=(
                    (
                        lambda pid: self._activate_binding(
                            binding_descriptor,
                            binding_writer,
                            binding_delivery,
                            pid,
                            command,
                        )
                    )
                    if self._local_binding_issuer is not None
                    else None
                ),
            )
        except GateBlocked:
            driver.stop()
            if binding_delivery is not None:
                binding_delivery.close()
                if self._local_binding_delivery is binding_delivery:
                    self._local_binding_delivery = None
            self._phase = "offline"
            self._persist_content_free_state()
            raise
        except (AsyncCancelledError, FutureCancelledError):
            driver.stop()
            if binding_delivery is not None:
                binding_delivery.close()
                if self._local_binding_delivery is binding_delivery:
                    self._local_binding_delivery = None
            self._phase = "offline"
            self._persist_content_free_state()
            raise
        except Exception as exc:
            driver.stop()
            if binding_delivery is not None:
                binding_delivery.close()
                if self._local_binding_delivery is binding_delivery:
                    self._local_binding_delivery = None
            self._phase = "offline"
            self._persist_content_free_state()
            raise AdapterProcessError(f"{self.spec.harness} native driver startup failed") from exc
        finally:
            if binding_descriptor is not None:
                try:
                    os.close(binding_descriptor)
                except OSError:
                    pass
            if binding_writer is not None:
                try:
                    os.close(binding_writer)
                except OSError:
                    pass
        self._driver = driver
        self._generation += 1
        self._phase = "ready"
        self._last_heartbeat_at = int(time.time())
        self._persist_content_free_state()

    def _monitor(self) -> None:
        while not self._stop_event.wait(self.heartbeat_interval_seconds):
            with self._lock:
                # The stop may race with acquisition of this lock after the
                # outer wait returned. Re-check it before any restart so a
                # failed one-shot process cannot resurrect itself.
                if self._stop_event.is_set() or self._phase == "stopped":
                    return
                driver = self._driver
                if driver is None or not driver.alive:
                    self._phase = "offline"
                    self._persist_content_free_state()
                    if driver is not None:
                        driver.stop()
                    try:
                        self._start_driver(recover=True, restart=True)
                    except AdapterProcessError:
                        continue
                elif self._phase in {"ready", "degraded"}:
                    try:
                        self._maintain_mcp_binding()
                    except Exception:
                        self._local_binding_renewal_failures += 1
                        self._phase = "degraded"
                        self._persist_content_free_state()
                        if self._local_binding_renewal_failures < 3:
                            continue
                        driver.stop()
                        try:
                            self._start_driver(recover=True, restart=True)
                        except (AdapterProcessError, GateBlocked):
                            continue
                        continue
                    self._phase = "ready"
                    self._last_heartbeat_at = int(time.time())
                    self._persist_content_free_state()

    def require_ready(self) -> None:
        with self._lock:
            if self._phase != "ready" or self._driver is None or not self._driver.alive:
                raise AdapterProcessError(f"{self.spec.harness} native background session is offline")

    def healthcheck(self) -> dict[str, Any]:
        with self._lock:
            self.require_ready()
            assert self._driver is not None
            try:
                result = self._driver.healthcheck(timeout_seconds=self.request_timeout_seconds)
            except NativeProtocolError as exc:
                self._phase = "offline"
                self._persist_content_free_state()
                raise AdapterProcessError("native background healthcheck failed") from exc
            self._last_heartbeat_at = int(time.time())
            self._persist_content_free_state()
            return result

    def _submit_authorized(self, prompt: str) -> dict[str, Any]:
        if self.spec.semantic_mode != "clean_worker":
            raise GateBlocked("G03", "semantic isolation is unavailable; adapter is deterministic-only")
        if not prompt:
            raise ValidationError("native background prompt cannot be empty")
        with self._lock:
            self.require_ready()
            assert self._driver is not None
            try:
                return asdict(
                    self._driver.submit(prompt, timeout_seconds=self.request_timeout_seconds)
                )
            except NativeProtocolError as exc:
                if not self._driver.alive or not self.spec.persistent_process:
                    self._phase = "offline"
                    if not self.spec.persistent_process:
                        self._stop_event.set()
                    self._persist_content_free_state()
                raise AdapterProcessError("native background turn failed") from exc

    def submit(self, prompt: str, *, explicit: bool = False) -> dict[str, Any]:
        """Human explicit-open compatibility path; never used by the daemon."""

        if not explicit:
            raise AuthorizationError("native background turns require an authorized trigger")
        return self._submit_authorized(prompt)

    def submit_background(
        self,
        prompt: str,
        *,
        authorization: BackgroundTurnAuthorization,
    ) -> dict[str, Any]:
        """Run a server-authorized eligible event without foreground injection."""

        if (
            authorization.harness_id != self.spec.harness_id
            or authorization.expires_at <= int(time.time())
        ):
            raise AuthorizationError("background turn authorization does not bind this runtime")
        return self._submit_authorized(prompt)

    @property
    def pid(self) -> int | None:
        with self._lock:
            return self._driver.pid if self._driver is not None else None

    def status(self) -> RuntimeStatus:
        with self._lock:
            return RuntimeStatus(
                harness=self.spec.harness,
                phase=self._phase,
                generation=self._generation,
                restart_count=self._restart_count,
                semantic_mode=self.spec.semantic_mode,
                native_surface=self.spec.transport,
                pinned_version=self.spec.pinned_version,
                version_match=bool(self._probe and self._probe.matches_pin),
                clean_worker_admitted=self._clean_worker_admission is not None,
                last_heartbeat_at=self._last_heartbeat_at,
            )

    def content_free_status(self) -> dict[str, Any]:
        return asdict(self.status())

    def stop(self, *, timeout_seconds: float = 2.0) -> None:
        del timeout_seconds
        self._stop_event.set()
        with self._lock:
            driver = self._driver
            self._phase = "stopped"
            self._persist_content_free_state()
        if driver is not None:
            driver.stop()
        delivery, self._local_binding_delivery = self._local_binding_delivery, None
        if delivery is not None:
            delivery.close()
        monitor = self._monitor_thread
        if monitor is not None and monitor is not threading.current_thread():
            monitor.join(timeout=2.0)

    def __enter__(self) -> "BackgroundAdapterRuntime":
        self.start()
        return self

    def __exit__(self, *_exc: object) -> None:
        self.stop()
