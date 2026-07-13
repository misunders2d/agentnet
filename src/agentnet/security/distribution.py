"""Fail-closed local distribution lifecycle for threshold-verified releases.

This module provides the local installer boundary.  It does not claim that a
release was signed by independently administered production roots; callers
must supply the configured update root to :func:`verify_distribution_release`.
"""

from __future__ import annotations

import hashlib
import json
import os
import platform
import re
import secrets
import shutil
import stat
import fcntl
import subprocess
import sys
from contextlib import contextmanager
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agentnet.errors import ConflictError, GateBlocked, ValidationError
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.security.update import (
    UpdateArtifact,
    UpdateManifest,
    UpdateTrustRoot,
    UpdateVerificationState,
    verify_threshold_manifest,
)


_VERIFIED_DISTRIBUTION_SEAL = object()
_RELEASE_ID = re.compile(r"^[1-9][0-9]*-[0-9]+\.[0-9]+\.[0-9]+$")
_STATE_SCHEMA = "agentnet.distribution.state.v1"
_UNINSTALL_TOMBSTONE_PREFIX = ".agentnet-distribution-uninstall-"
MAX_HEALTH_CHECK_SECONDS = 60
_HEALTH_SUPERVISOR_CLEANUP_SECONDS = 5
_HEALTH_SUPERVISOR_SCRIPT = r"""
import ctypes
import os
import signal
import sys
import time

PR_SET_CHILD_SUBREAPER = 36
health_fd = int(sys.argv[1])
helper_fd = int(sys.argv[2])
timeout_seconds = int(sys.argv[3])
working_directory = sys.argv[4]
health_argv = tuple(sys.argv[5:])
stop_requested = False


def request_stop(_signum, _frame):
    global stop_requested
    stop_requested = True


def reap_exited_children():
    while True:
        try:
            child_pid, _status = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return
        if child_pid == 0:
            return


def direct_children():
    path = "/proc/self/task/{}/children".format(os.getpid())
    try:
        value = open(path, "r", encoding="ascii").read().strip()
    except (FileNotFoundError, OSError):
        return None
    if not value:
        return ()
    try:
        return tuple(int(item) for item in value.split())
    except ValueError:
        return None


def kill_and_reap_all_children():
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        reap_exited_children()
        children = direct_children()
        if children == ():
            try:
                child_pid, _status = os.waitpid(-1, os.WNOHANG)
            except ChildProcessError:
                return True
            if child_pid == 0:
                time.sleep(0.01)
            continue
        if children is None:
            return False
        for child_pid in children:
            try:
                os.kill(child_pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            except OSError:
                return False
        time.sleep(0.01)
    reap_exited_children()
    children = direct_children()
    if children != ():
        return False
    try:
        os.waitpid(-1, os.WNOHANG)
    except ChildProcessError:
        return True
    return False


libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
    os._exit(70)
signal.signal(signal.SIGTERM, request_stop)
signal.signal(signal.SIGINT, request_stop)
try:
    os.close(helper_fd)
except OSError:
    pass

health_pid = os.fork()
if health_pid == 0:
    try:
        os.chdir(working_directory)
        os.setsid()
        os.set_inheritable(health_fd, True)
        os.execve(
            "/proc/self/fd/{}".format(health_fd),
            health_argv,
            {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
        )
    except BaseException:
        os._exit(127)

try:
    os.close(health_fd)
except OSError:
    pass
deadline = time.monotonic() + timeout_seconds
health_status = None
while health_status is None:
    try:
        waited_pid, waited_status = os.waitpid(health_pid, os.WNOHANG)
    except ChildProcessError:
        waited_pid, waited_status = health_pid, 1 << 8
    if waited_pid == health_pid:
        health_status = waited_status
        break
    if stop_requested or time.monotonic() >= deadline:
        try:
            os.kill(health_pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            _waited_pid, health_status = os.waitpid(health_pid, 0)
        except ChildProcessError:
            health_status = 1 << 8
        break
    time.sleep(0.01)

clean = kill_and_reap_all_children()
healthy = (
    clean
    and not stop_requested
    and os.WIFEXITED(health_status)
    and os.WEXITSTATUS(health_status) == 0
)
os._exit(0 if healthy else (1 if clean else 71))
"""


@dataclass(frozen=True, slots=True)
class HealthCheckCommand:
    """Exact, digest-bound subprocess health check; ``{bundle}`` is substituted once."""

    argv: tuple[str, ...]
    executable_sha256: str

    def open_validated_executable(self) -> int:
        if (
            not self.argv
            or not Path(self.argv[0]).is_absolute()
            or self.argv.count("{bundle}") != 1
            or not re.fullmatch(r"[0-9a-f]{64}", self.executable_sha256)
        ):
            raise ValidationError("health-check command is not exact")
        executable = Path(self.argv[0]).resolve()
        if not executable.is_file():
            raise ValidationError("health-check executable is absent")
        descriptor = os.open(executable, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode):
            os.close(descriptor)
            raise ValidationError("health-check executable is not regular")
        digest_state = hashlib.sha256()
        while chunk := os.read(descriptor, 1024 * 1024):
            digest_state.update(chunk)
        os.lseek(descriptor, 0, os.SEEK_SET)
        digest = digest_state.hexdigest()
        if not secrets.compare_digest(digest, self.executable_sha256):
            os.close(descriptor)
            raise GateBlocked("distribution_health", "health-check executable digest changed")
        return descriptor


@dataclass(frozen=True, slots=True)
class VerifiedDistributionRelease:
    manifest: UpdateManifest
    manifest_digest: str
    observed_state: UpdateVerificationState
    verified_at: int
    _seal: object


def verify_distribution_release(
    manifest: Mapping[str, Any],
    signatures: Sequence[Mapping[str, str]],
    trusted_update_root: UpdateTrustRoot,
    *,
    state: UpdateVerificationState,
    now: int,
) -> VerifiedDistributionRelease:
    """Threshold-verify metadata and bind the anti-rollback observation."""

    parsed = verify_threshold_manifest(
        manifest,
        signatures,
        trusted_update_root,
        state=state,
        now=now,
    )
    signed_value = parsed.model_dump(mode="json", by_alias=True)
    digest = canonical_digest(signed_value)
    if parsed.release_sequence > state.highest_seen_sequence:
        observed = UpdateVerificationState(
            installed_version=state.installed_version,
            installed_sequence=state.installed_sequence,
            highest_seen_version=parsed.version,
            highest_seen_sequence=parsed.release_sequence,
            highest_seen_manifest_digest=digest,
            last_advance_at=now,
        )
    else:
        observed = state
    return VerifiedDistributionRelease(
        manifest=parsed,
        manifest_digest=digest,
        observed_state=observed,
        verified_at=now,
        _seal=_VERIFIED_DISTRIBUTION_SEAL,
    )


def _canonical_architecture(value: str | None = None) -> str:
    candidate = (value or platform.machine()).casefold()
    aliases = {
        "x86_64": "x86_64",
        "amd64": "x86_64",
        "aarch64": "aarch64",
        "arm64": "aarch64",
    }
    architecture = aliases.get(candidate)
    if architecture is None:
        raise GateBlocked("distribution_platform", "release architecture is unsupported")
    return architecture


def _fsync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _secure_directory(path: Path, *, create: bool = False) -> None:
    if create and not os.path.lexists(path):
        path.mkdir(mode=0o700)
    try:
        info = path.lstat()
    except FileNotFoundError as exc:
        raise GateBlocked("distribution_path", "distribution directory is absent") from exc
    if (
        stat.S_ISLNK(info.st_mode)
        or not stat.S_ISDIR(info.st_mode)
        or info.st_uid != os.geteuid()
        or info.st_mode & 0o077
    ):
        raise GateBlocked(
            "distribution_path",
            "distribution directory must be an owner-only real directory",
        )


def _reject_symlink_ancestors(path: Path) -> None:
    """Refuse resolving an install boundary through any existing symlink."""

    current = path.absolute()
    for candidate in (current, *current.parents):
        if not os.path.lexists(candidate):
            continue
        if stat.S_ISLNK(candidate.lstat().st_mode):
            raise GateBlocked(
                "distribution_path",
                "distribution path cannot traverse a symlink",
            )


def _atomic_json(path: Path, value: Mapping[str, Any], *, mode: int = 0o600) -> None:
    payload = canonical_json(dict(value)) + b"\n"
    temporary = path.parent / f".{path.name}.{secrets.token_hex(12)}.tmp"
    descriptor = os.open(
        temporary,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
        mode,
    )
    try:
        view = memoryview(payload)
        while view:
            written = os.write(descriptor, view)
            if written <= 0:
                raise OSError("distribution state write made no progress")
            view = view[written:]
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    os.replace(temporary, path)
    os.chmod(path, mode)
    _fsync_directory(path.parent)


class DistributionInstaller:
    """Atomic release store with exact rollback and cleanup boundaries."""

    def __init__(
        self,
        install_root: Path,
        *,
        trusted_update_root: UpdateTrustRoot,
        bootstrap_state: UpdateVerificationState,
        health_check_timeout_seconds: int = 30,
        architecture: str | None = None,
        cleanup_allowlist: Sequence[Path] = (),
    ) -> None:
        self.root = install_root.absolute()
        if not isinstance(trusted_update_root, UpdateTrustRoot):
            raise ValidationError("typed update trust root is required")
        if not isinstance(bootstrap_state, UpdateVerificationState):
            raise ValidationError("typed bootstrap update state is required")
        self.trusted_update_root = trusted_update_root
        self.bootstrap_state = bootstrap_state
        root_digest = hashlib.sha256(os.fsencode(self.root)).hexdigest()[:16]
        self.trust_state_path = self.root.parent / f".{self.root.name}.{root_digest}.trust-state.json"
        if (
            not isinstance(health_check_timeout_seconds, int)
            or isinstance(health_check_timeout_seconds, bool)
            or not 1 <= health_check_timeout_seconds <= MAX_HEALTH_CHECK_SECONDS
        ):
            raise ValidationError("health-check timeout is outside the bounded profile")
        self.health_check_timeout_seconds = health_check_timeout_seconds
        self.architecture = _canonical_architecture(architecture)
        self.cleanup_allowlist = frozenset(path.absolute() for path in cleanup_allowlist)

    @property
    def releases(self) -> Path:
        return self.root / "releases"

    @property
    def state_path(self) -> Path:
        return self.trust_state_path

    @property
    def lock_path(self) -> Path:
        return self.trust_state_path.with_name(f".{self.trust_state_path.name}.lock")

    def _prepare_lock_boundary(self) -> None:
        _reject_symlink_ancestors(self.trust_state_path.parent)
        _secure_directory(self.trust_state_path.parent)

    def _unresolved_tombstones(self) -> tuple[Path, ...]:
        _secure_directory(self.root.parent)
        prefix = self._tombstone_prefix()
        return tuple(
            entry
            for entry in self.root.parent.iterdir()
            if entry.name.startswith(prefix)
        )

    def _tombstone_prefix(self) -> str:
        root_digest = hashlib.sha256(os.fsencode(self.root)).hexdigest()[:16]
        return f"{_UNINSTALL_TOMBSTONE_PREFIX}{root_digest}-"

    def _prepare(self) -> None:
        _reject_symlink_ancestors(self.root)
        unresolved = self._unresolved_tombstones()
        if unresolved:
            raise GateBlocked(
                "distribution_cleanup",
                "an unresolved uninstall tombstone blocks installation",
            )
        if not os.path.lexists(self.root):
            self.root.mkdir(mode=0o700)
        _secure_directory(self.root)
        if not os.path.lexists(self.releases):
            self.releases.mkdir(mode=0o700)
        _secure_directory(self.releases)

    @contextmanager
    def _state_lock(self):
        """Serialize verification and the exact state CAS across processes."""

        self._prepare_lock_boundary()
        descriptor = os.open(
            self.lock_path,
            os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0),
            0o600,
        )
        try:
            info = os.fstat(descriptor)
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
                raise GateBlocked("distribution_state", "distribution lock is not owner-only")
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            yield
        finally:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            finally:
                os.close(descriptor)

    def _select_artifact(self, release: VerifiedDistributionRelease) -> UpdateArtifact:
        selected = [
            artifact
            for artifact in release.manifest.artifacts
            if artifact.platform == "linux" and artifact.architecture == self.architecture
        ]
        if len(selected) != 1:
            raise GateBlocked(
                "distribution_platform",
                "signed release has no unique artifact for this platform",
            )
        return selected[0]

    def _run_health_check(self, command: HealthCheckCommand, bundle: Path) -> bool:
        if not isinstance(command, HealthCheckCommand):
            raise ValidationError("typed health-check command is required")
        health_descriptor = command.open_validated_executable()
        helper_executable = Path(sys.executable).resolve()
        helper_descriptor = os.open(
            helper_executable,
            os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0),
        )
        helper_info = os.fstat(helper_descriptor)
        if not stat.S_ISREG(helper_info.st_mode):
            os.close(health_descriptor)
            os.close(helper_descriptor)
            raise GateBlocked(
                "distribution_health",
                "health supervisor interpreter is not regular",
            )
        argv = tuple(str(bundle) if item == "{bundle}" else item for item in command.argv)
        supervisor_argv = (
            str(helper_executable),
            "-I",
            "-c",
            _HEALTH_SUPERVISOR_SCRIPT,
            str(health_descriptor),
            str(helper_descriptor),
            str(self.health_check_timeout_seconds),
            str(bundle.parent),
            *argv,
        )
        pinned_helper = f"/proc/self/fd/{helper_descriptor}"
        try:
            process = subprocess.Popen(
                supervisor_argv,
                executable=pinned_helper,
                pass_fds=(health_descriptor, helper_descriptor),
                cwd=bundle.parent,
                env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
            )
        finally:
            os.close(health_descriptor)
            os.close(helper_descriptor)
        try:
            return process.wait(
                timeout=(
                    self.health_check_timeout_seconds
                    + _HEALTH_SUPERVISOR_CLEANUP_SECONDS
                    + 2
                )
            ) == 0
        except subprocess.TimeoutExpired:
            process.terminate()
            try:
                process.wait(timeout=_HEALTH_SUPERVISOR_CLEANUP_SECONDS + 1)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=2)
            return False

    @staticmethod
    def _verify_source(source: Path, artifact: UpdateArtifact) -> str:
        try:
            info = source.lstat()
        except FileNotFoundError as exc:
            raise GateBlocked("distribution_artifact", "release artifact is absent") from exc
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
            raise GateBlocked("distribution_artifact", "release artifact must be a regular non-symlink file")
        if info.st_size != artifact.size:
            raise GateBlocked("distribution_artifact", "release artifact size does not match signed metadata")
        digest = hashlib.sha256()
        descriptor = os.open(source, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        try:
            while chunk := os.read(descriptor, 1024 * 1024):
                digest.update(chunk)
            final = os.fstat(descriptor)
            if not stat.S_ISREG(final.st_mode) or final.st_size != info.st_size:
                raise GateBlocked("distribution_artifact", "release artifact changed while being read")
        finally:
            os.close(descriptor)
        value = digest.hexdigest()
        if not secrets.compare_digest(value, artifact.sha256):
            raise GateBlocked("distribution_artifact", "release artifact digest does not match signed metadata")
        return value

    @staticmethod
    def _default_state() -> dict[str, Any]:
        raise AssertionError("bootstrap state is instance-specific")

    def _bootstrap_distribution_state(self) -> dict[str, Any]:
        return {
            "schema": _STATE_SCHEMA,
            "active_release": None,
            "previous_release": None,
            "failed_releases": [],
            "verification_state": self.bootstrap_state.model_dump(mode="json"),
        }

    def _load_state(self) -> tuple[dict[str, Any], str]:
        if not os.path.lexists(self.state_path):
            value = self._bootstrap_distribution_state()
            return value, "absent"
        info = self.state_path.lstat()
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise GateBlocked("distribution_state", "distribution state is not an owner-only regular file")
        try:
            value = json.loads(self.state_path.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, json.JSONDecodeError) as exc:
            raise GateBlocked("distribution_state", "distribution state is unreadable") from exc
        if (
            not isinstance(value, dict)
            or set(value) != set(self._bootstrap_distribution_state())
            or value.get("schema") != _STATE_SCHEMA
            or not isinstance(value.get("failed_releases"), list)
        ):
            raise GateBlocked("distribution_state", "distribution state schema is invalid")
        for key in ("active_release", "previous_release"):
            release_id = value.get(key)
            if release_id is not None and (
                not isinstance(release_id, str) or not _RELEASE_ID.fullmatch(release_id)
            ):
                raise GateBlocked("distribution_state", "distribution release identifier is invalid")
        verification_state = value.get("verification_state")
        if verification_state is not None:
            try:
                UpdateVerificationState.model_validate(verification_state)
            except Exception as exc:
                raise GateBlocked(
                    "distribution_state",
                    "distribution verification state is invalid",
                ) from exc
        token = hashlib.sha256(canonical_json(value) + b"\n").hexdigest()
        return value, token

    def _write_state_cas(self, value: Mapping[str, Any], expected_token: str) -> None:
        """Replace state only if its exact canonical predecessor is unchanged."""

        if os.path.lexists(self.state_path):
            current, token = self._load_state()
            del current
            if token != expected_token:
                raise ConflictError("distribution anti-rollback state changed concurrently")
        elif expected_token != "absent":
            raise ConflictError("distribution anti-rollback state disappeared concurrently")
        _atomic_json(self.state_path, value)

    @staticmethod
    def _release_id(manifest: UpdateManifest) -> str:
        value = f"{manifest.release_sequence}-{manifest.version}"
        if not _RELEASE_ID.fullmatch(value):
            raise ValidationError("signed release identifier is invalid")
        return value

    def _stage_release(
        self,
        *,
        source: Path,
        release: VerifiedDistributionRelease,
        artifact: UpdateArtifact,
        artifact_digest: str,
    ) -> tuple[Path, bool]:
        release_id = self._release_id(release.manifest)
        final = self.releases / release_id
        bundle = final / "bundle.whl"
        metadata = final / "release.json"
        expected_metadata = {
            "schema": "agentnet.distribution.release.v1",
            "release_id": release_id,
            "version": release.manifest.version,
            "release_sequence": release.manifest.release_sequence,
            "manifest_digest": release.manifest_digest,
            "artifact_sha256": artifact_digest,
            "artifact_size": artifact.size,
            "architecture": artifact.architecture,
        }
        if os.path.lexists(final):
            _secure_directory(final)
            if set(item.name for item in final.iterdir()) != {"bundle.whl", "release.json"}:
                raise ConflictError("existing release directory contains unexpected entries")
            if bundle.is_symlink() or metadata.is_symlink():
                raise ConflictError("existing release contains a symlink")
            for immutable_file in (bundle, metadata):
                info = immutable_file.lstat()
                if (
                    not stat.S_ISREG(info.st_mode)
                    or info.st_uid != os.geteuid()
                    or info.st_mode & 0o777 != 0o400
                ):
                    raise ConflictError("existing release file is not immutable owner-readable state")
            try:
                existing = json.loads(metadata.read_text(encoding="utf-8"))
            except (OSError, UnicodeError, json.JSONDecodeError) as exc:
                raise ConflictError("existing release metadata is unreadable") from exc
            if existing != expected_metadata or self._verify_source(bundle, artifact) != artifact_digest:
                raise ConflictError("existing release identifier has different immutable bytes")
            return bundle, True

        temporary = self.releases / f".{release_id}.{secrets.token_hex(12)}.tmp"
        temporary.mkdir(mode=0o700)
        try:
            target = temporary / "bundle.whl"
            source_fd = os.open(source, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
            target_fd = os.open(
                target,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
                0o600,
            )
            try:
                copied_digest = hashlib.sha256()
                copied_size = 0
                while chunk := os.read(source_fd, 1024 * 1024):
                    copied_digest.update(chunk)
                    copied_size += len(chunk)
                    view = memoryview(chunk)
                    while view:
                        written = os.write(target_fd, view)
                        if written <= 0:
                            raise OSError("release artifact write made no progress")
                        view = view[written:]
                source_info = os.fstat(source_fd)
                if (
                    not stat.S_ISREG(source_info.st_mode)
                    or source_info.st_size != artifact.size
                    or copied_size != artifact.size
                    or not secrets.compare_digest(copied_digest.hexdigest(), artifact_digest)
                ):
                    raise GateBlocked(
                        "distribution_artifact",
                        "release artifact changed during atomic staging",
                    )
                os.fsync(target_fd)
            finally:
                os.close(source_fd)
                os.close(target_fd)
            os.chmod(target, 0o400)
            _atomic_json(temporary / "release.json", expected_metadata, mode=0o400)
            _fsync_directory(temporary)
            os.chmod(temporary, 0o500)
            os.replace(temporary, final)
            _fsync_directory(self.releases)
        except Exception:
            if os.path.lexists(temporary):
                os.chmod(temporary, 0o700)
                shutil.rmtree(temporary)
            raise
        return bundle, False

    def install(
        self,
        manifest: Mapping[str, Any],
        signatures: Sequence[Mapping[str, str]],
        source: Path,
        *,
        now: int,
        health_check: HealthCheckCommand,
    ) -> dict[str, Any]:
        """Verify and install against the exact persisted anti-rollback state.

        Verification is intentionally inside the serialized installer boundary;
        a previously verified object from a stale caller snapshot is never an
        install credential.
        """

        with self._state_lock():
            self._prepare()
            state, state_token = self._load_state()
            persisted = UpdateVerificationState.model_validate(state["verification_state"])
            release = verify_distribution_release(
                manifest,
                signatures,
                self.trusted_update_root,
                state=persisted,
                now=now,
            )
            artifact = self._select_artifact(release)
            digest = self._verify_source(source, artifact)
            bundle, duplicate_bytes = self._stage_release(
                source=source,
                release=release,
                artifact=artifact,
                artifact_digest=digest,
            )
            release_id = self._release_id(release.manifest)
            if state["active_release"] == release_id:
                return {
                    "state": "active",
                    "release_id": release_id,
                    "duplicate": True,
                    "bundle": str(bundle),
                    "verification_state": persisted.model_dump(mode="json"),
                }
            healthy = self._run_health_check(health_check, bundle)
            if healthy:
                try:
                    self._stage_release(
                        source=source,
                        release=release,
                        artifact=artifact,
                        artifact_digest=digest,
                    )
                except (GateBlocked, ConflictError, OSError):
                    healthy = False
            if not healthy:
                state["failed_releases"] = sorted(set([*state["failed_releases"], release_id]))
                state["verification_state"] = release.observed_state.model_dump(mode="json")
                self._write_state_cas(state, state_token)
                raise GateBlocked(
                    "distribution_health",
                    "candidate failed health verification; prior release remains active",
                )
            previous = state["active_release"]
            installed_state = UpdateVerificationState(
                installed_version=release.manifest.version,
                installed_sequence=release.manifest.release_sequence,
                highest_seen_version=release.observed_state.highest_seen_version,
                highest_seen_sequence=release.observed_state.highest_seen_sequence,
                highest_seen_manifest_digest=release.observed_state.highest_seen_manifest_digest,
                last_advance_at=release.observed_state.last_advance_at,
            )
            state.update(
                {
                    "active_release": release_id,
                    "previous_release": previous,
                    "failed_releases": [
                        item for item in state["failed_releases"] if item != release_id
                    ],
                    "verification_state": installed_state.model_dump(mode="json"),
                }
            )
            self._write_state_cas(state, state_token)
            return {
                "state": "active",
                "release_id": release_id,
                "previous_release": previous,
                "duplicate": duplicate_bytes,
                "bundle": str(bundle),
                "verification_state": installed_state.model_dump(mode="json"),
            }

    def uninstall(self, *, cleanup_paths: Sequence[Path] = ()) -> dict[str, Any]:
        """Serialize ordinary uninstall with verification, staging, and health."""

        with self._state_lock():
            return self._uninstall_locked(cleanup_paths=cleanup_paths)

    def recover_uninstall(self) -> dict[str, Any]:
        """Retry deletion of one exact quarantined install root."""

        with self._state_lock():
            tombstones = self._unresolved_tombstones()
            if not tombstones:
                return {
                    "state": "uninstalled" if not os.path.lexists(self.root) else "not_pending",
                    "deleted": [],
                    "residual": [],
                    "secure_erase_guaranteed": False,
                }
            if len(tombstones) != 1 or os.path.lexists(self.root):
                return {
                    "state": "residual",
                    "deleted": [],
                    "residual": [str(path) for path in tombstones],
                    "secure_erase_guaranteed": False,
                }
            tombstone = tombstones[0]
            try:
                _secure_directory(tombstone)
                entries = set(tombstone.iterdir())
                if any(entry.name != "releases" for entry in entries):
                    raise GateBlocked("distribution_cleanup", "tombstone contains unexpected entries")
                releases = tombstone / "releases"
                if os.path.lexists(releases):
                    _secure_directory(releases)
                    for release in releases.iterdir():
                        _secure_directory(release)
                        if not _RELEASE_ID.fullmatch(release.name):
                            raise GateBlocked("distribution_cleanup", "tombstone release identifier is invalid")
                        names = {entry.name for entry in release.iterdir()}
                        if not names <= {"bundle.whl", "release.json"}:
                            raise GateBlocked("distribution_cleanup", "tombstone release contains unexpected entries")
                        for entry in release.iterdir():
                            info = entry.lstat()
                            if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
                                raise GateBlocked("distribution_cleanup", "tombstone release entry is unsafe")
                    for release in tuple(releases.iterdir()):
                        os.chmod(release, 0o700)
                        for entry in tuple(release.iterdir()):
                            entry.unlink()
                        release.rmdir()
                    releases.rmdir()
                trust_state, trust_token = self._load_state()
                previous = trust_state["active_release"]
                trust_state["active_release"] = None
                trust_state["previous_release"] = previous
                self._write_state_cas(trust_state, trust_token)
                tombstone.rmdir()
                _fsync_directory(tombstone.parent)
            except Exception:
                return {
                    "state": "residual",
                    "deleted": [],
                    "residual": [str(tombstone)] if os.path.lexists(tombstone) else [],
                    "secure_erase_guaranteed": False,
                }
            return {
                "state": "uninstalled",
                "deleted": [str(tombstone)],
                "residual": [],
                "secure_erase_guaranteed": False,
            }

    def _uninstall_locked(self, *, cleanup_paths: Sequence[Path] = ()) -> dict[str, Any]:
        """Delete an exact allowlisted tree through pinned no-follow dirfds.

        The operation preflights every entry and owner before mutation.  Open
        directory descriptors pin the inspected objects; exact inode checks
        immediately precede every top-level unlink/rmdir.  No secure-erasure
        property is implied by logical deletion.
        """

        requested = tuple(path.absolute() for path in cleanup_paths)
        if any(path not in self.cleanup_allowlist for path in requested):
            raise ValidationError("cleanup path is outside the explicit extension allowlist")
        if any(path == self.root or self.root in path.parents for path in requested):
            raise ValidationError("cleanup allowlist cannot overlap the install root")
        unresolved = self._unresolved_tombstones()
        if unresolved:
            return {
                "state": "residual",
                "deleted": [],
                "residual": [str(path) for path in unresolved],
                "secure_erase_guaranteed": False,
            }
        trust_state, trust_token = self._load_state()

        residual: list[str] = []
        cleanup_handles: list[tuple[int, str, os.stat_result, Path]] = []
        root_fd: int | None = None
        root_parent_fd: int | None = None
        root_info: os.stat_result | None = None
        releases_info: os.stat_result | None = None
        release_snapshots: dict[str, tuple[os.stat_result, dict[str, os.stat_result]]] = {}
        try:
            for path in requested:
                if not os.path.lexists(path):
                    continue
                parent_fd: int | None = None
                try:
                    _reject_symlink_ancestors(path.parent)
                    _secure_directory(path.parent)
                    parent_fd = os.open(
                        path.parent,
                        os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                    )
                    info = os.stat(path.name, dir_fd=parent_fd, follow_symlinks=False)
                    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
                        raise OSError("allowlisted path is not an owner-owned regular file")
                    cleanup_handles.append((parent_fd, path.name, info, path))
                except (OSError, GateBlocked):
                    if parent_fd is not None:
                        os.close(parent_fd)
                    residual.append(str(path))

            if os.path.lexists(self.root):
                _reject_symlink_ancestors(self.root)
                _secure_directory(self.root)
                _secure_directory(self.root.parent)
                root_parent_fd = os.open(
                    self.root.parent,
                    os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                )
                root_fd = os.open(
                    self.root.name,
                    os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                    dir_fd=root_parent_fd,
                )
                root_info = os.fstat(root_fd)
                root_names = set(os.listdir(root_fd))
                unexpected = root_names - {"releases"}
                residual.extend(str(self.root / name) for name in sorted(unexpected))
                if "releases" in root_names:
                    info = os.stat("releases", dir_fd=root_fd, follow_symlinks=False)
                    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid():
                        residual.append(str(self.releases))
                    else:
                        releases_info = info
                        releases_fd = os.open(
                            "releases",
                            os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                            dir_fd=root_fd,
                        )
                        try:
                            for release_name in os.listdir(releases_fd):
                                if not _RELEASE_ID.fullmatch(release_name):
                                    residual.append(str(self.releases / release_name))
                                    continue
                                release_fd = os.open(
                                    release_name,
                                    os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                                    dir_fd=releases_fd,
                                )
                                try:
                                    release_info = os.fstat(release_fd)
                                    names = set(os.listdir(release_fd))
                                    if names != {"bundle.whl", "release.json"}:
                                        residual.append(str(self.releases / release_name))
                                    for name in names:
                                        child = os.stat(name, dir_fd=release_fd, follow_symlinks=False)
                                        if not stat.S_ISREG(child.st_mode) or child.st_uid != os.geteuid():
                                            residual.append(str(self.releases / release_name / name))
                                    release_snapshots[release_name] = (
                                        release_info,
                                        {
                                            name: os.stat(name, dir_fd=release_fd, follow_symlinks=False)
                                            for name in names
                                        },
                                    )
                                finally:
                                    os.close(release_fd)
                        finally:
                            os.close(releases_fd)
        except (OSError, GateBlocked):
            residual.append(str(self.root))

        if residual:
            for descriptor, *_ in cleanup_handles:
                os.close(descriptor)
            if root_fd is not None:
                os.close(root_fd)
            if root_parent_fd is not None:
                os.close(root_parent_fd)
            return {
                "state": "refused",
                "deleted": [],
                "residual": sorted(set(residual)),
                "secure_erase_guaranteed": False,
            }

        deleted: list[str] = []
        tombstone_name: str | None = None
        tombstone_path: Path | None = None
        trust_state_updated = False
        try:
            if root_fd is not None and root_parent_fd is not None and root_info is not None:
                current_root = os.stat(self.root.name, dir_fd=root_parent_fd, follow_symlinks=False)
                if (current_root.st_dev, current_root.st_ino) != (root_info.st_dev, root_info.st_ino):
                    raise ConflictError("install root changed after preflight")
                tombstone_name = f"{self._tombstone_prefix()}{secrets.token_hex(16)}"
                tombstone_path = self.root.parent / tombstone_name
                os.rename(
                    self.root.name,
                    tombstone_name,
                    src_dir_fd=root_parent_fd,
                    dst_dir_fd=root_parent_fd,
                )
                moved_root = os.stat(tombstone_name, dir_fd=root_parent_fd, follow_symlinks=False)
                if (moved_root.st_dev, moved_root.st_ino) != (root_info.st_dev, root_info.st_ino):
                    raise ConflictError("install root quarantine identity mismatch")
                os.fsync(root_parent_fd)
            for parent_fd, name, expected, path in cleanup_handles:
                current = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
                if (current.st_dev, current.st_ino, current.st_mode) != (
                    expected.st_dev,
                    expected.st_ino,
                    expected.st_mode,
                ):
                    raise ConflictError("allowlisted cleanup path changed after preflight")
                tombstone = f".agentnet-uninstall-{secrets.token_hex(16)}"
                os.rename(
                    name,
                    tombstone,
                    src_dir_fd=parent_fd,
                    dst_dir_fd=parent_fd,
                )
                moved = os.stat(tombstone, dir_fd=parent_fd, follow_symlinks=False)
                if (moved.st_dev, moved.st_ino, moved.st_mode) != (
                    expected.st_dev,
                    expected.st_ino,
                    expected.st_mode,
                ):
                    if name not in os.listdir(parent_fd):
                        os.rename(
                            tombstone,
                            name,
                            src_dir_fd=parent_fd,
                            dst_dir_fd=parent_fd,
                        )
                    raise ConflictError("allowlisted cleanup path raced atomic isolation")
                os.unlink(tombstone, dir_fd=parent_fd)
                os.fsync(parent_fd)
                deleted.append(str(path))

            if root_fd is not None and root_parent_fd is not None and root_info is not None:
                if "releases" in os.listdir(root_fd):
                    releases_fd = os.open(
                        "releases",
                        os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                        dir_fd=root_fd,
                    )
                    try:
                        if releases_info is None or (
                            os.fstat(releases_fd).st_dev,
                            os.fstat(releases_fd).st_ino,
                        ) != (releases_info.st_dev, releases_info.st_ino):
                            raise ConflictError("release root changed after preflight")
                        if set(os.listdir(releases_fd)) != set(release_snapshots):
                            raise ConflictError("release set changed after preflight")
                        for release_name in os.listdir(releases_fd):
                            release_fd = os.open(
                                release_name,
                                os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                                dir_fd=releases_fd,
                            )
                            try:
                                expected_dir, expected_children = release_snapshots[release_name]
                                current_dir = os.fstat(release_fd)
                                if (current_dir.st_dev, current_dir.st_ino) != (
                                    expected_dir.st_dev,
                                    expected_dir.st_ino,
                                ) or set(os.listdir(release_fd)) != set(expected_children):
                                    raise ConflictError("release directory changed after preflight")
                                for name, expected_child in expected_children.items():
                                    current_child = os.stat(
                                        name, dir_fd=release_fd, follow_symlinks=False
                                    )
                                    if (current_child.st_dev, current_child.st_ino, current_child.st_mode) != (
                                        expected_child.st_dev,
                                        expected_child.st_ino,
                                        expected_child.st_mode,
                                    ):
                                        raise ConflictError("release file changed after preflight")
                                os.fchmod(release_fd, 0o700)
                                for name in expected_children:
                                    os.unlink(name, dir_fd=release_fd)
                                    deleted.append(str(self.releases / release_name / name))
                                os.fsync(release_fd)
                            finally:
                                os.close(release_fd)
                            os.rmdir(release_name, dir_fd=releases_fd)
                            deleted.append(str(self.releases / release_name))
                        os.fsync(releases_fd)
                    finally:
                        os.close(releases_fd)
                    os.rmdir("releases", dir_fd=root_fd)
                    deleted.append(str(self.releases))
                os.fsync(root_fd)
                assert tombstone_name is not None
                previous = trust_state["active_release"]
                trust_state["active_release"] = None
                trust_state["previous_release"] = previous
                self._write_state_cas(trust_state, trust_token)
                trust_state_updated = True
                current_root = os.stat(tombstone_name, dir_fd=root_parent_fd, follow_symlinks=False)
                if (current_root.st_dev, current_root.st_ino) != (root_info.st_dev, root_info.st_ino):
                    raise ConflictError("install root tombstone changed during cleanup")
                os.rmdir(tombstone_name, dir_fd=root_parent_fd)
                os.fsync(root_parent_fd)
                deleted.append(str(self.root))
            if not trust_state_updated:
                previous = trust_state["active_release"]
                trust_state["active_release"] = None
                trust_state["previous_release"] = previous
                self._write_state_cas(trust_state, trust_token)
        except Exception:
            residual_paths = [str(path) for path in requested if os.path.lexists(path)]
            if tombstone_path is not None and os.path.lexists(tombstone_path):
                residual_paths.append(str(tombstone_path))
            elif os.path.lexists(self.root):
                residual_paths.append(str(self.root))
            return {
                "state": "residual",
                "deleted": deleted,
                "residual": sorted(set(residual_paths)),
                "secure_erase_guaranteed": False,
            }
        finally:
            for descriptor, *_ in cleanup_handles:
                os.close(descriptor)
            if root_fd is not None:
                os.close(root_fd)
            if root_parent_fd is not None:
                os.close(root_parent_fd)
        return {
            "state": "uninstalled",
            "deleted": deleted,
            "residual": [],
            "secure_erase_guaranteed": False,
        }


__all__ = [
    "DistributionInstaller",
    "HealthCheckCommand",
    "VerifiedDistributionRelease",
    "verify_distribution_release",
]
