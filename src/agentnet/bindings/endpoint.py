"""Exact enrolled-endpoint binding and owner-private capability custody."""

from __future__ import annotations

import os
import secrets
import stat
import time
from dataclasses import dataclass
from hashlib import sha256
from hmac import compare_digest
from pathlib import Path
from typing import Any, Callable

from agentnet.core.capabilities import (
    ENDPOINT_CAPABILITY_ROOT_BYTES,
    endpoint_capability_root_name,
)
from agentnet.errors import AuthenticationError
from agentnet.host import host_platform
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.storage.backend import StoreBackend

_CAPABILITY_FILE_NAME = "capability-root.key"
_ROTATABLE_ENDPOINT_STATES = frozenset(
    {"enrolled", "access_ready", "restart_required", "connected"}
)


@dataclass(frozen=True, slots=True)
class EndpointBinding:
    domain_id: str
    principal_id: str
    harness_id: str
    harness_kind: str
    credential_id: str
    credential_epoch: int
    adapter_generation: int
    mailbox_cursor: int
    profile_key: str
    capability_root_path: Path
    process_measurement: str


def process_measurement_digest(process_measurement: str) -> str:
    """Normalize a measured process identity to the schema-v7 digest form."""

    if not process_measurement:
        raise AuthenticationError("endpoint process measurement is unavailable")
    candidate = process_measurement.removeprefix("sha256:")
    if len(candidate) == 64 and all(character in "0123456789abcdef" for character in candidate):
        return candidate
    return sha256(process_measurement.encode()).hexdigest()

def exact_process_measurement(
    *,
    platform: str,
    account_id: str,
    pid: int,
    start_time: str,
    executable_measurement: str,
) -> str:
    """Digest one exact process instance, including PID-reuse fencing facts."""

    fields = (platform, account_id, str(pid), start_time, executable_measurement)
    if any(not field for field in fields) or type(pid) is not int or pid <= 0:
        raise AuthenticationError("exact endpoint process measurement is unavailable")
    framed = b"".join(
        len(field.encode()).to_bytes(8, "big") + field.encode()
        for field in fields
    )
    return sha256(framed).hexdigest()


def _require_owner_private_real_directory(path: Path) -> None:
    if host_platform() == "windows":
        from agentnet.windows_security import require_private_path

        try:
            require_private_path(path, directory=True)
        except Exception as exc:
            raise AuthenticationError(
                "endpoint capability root must be an owner-private real directory"
            ) from exc
        return
    try:
        before = path.lstat()
        descriptor = os.open(
            path,
            os.O_RDONLY
            | getattr(os, "O_DIRECTORY", 0)
            | getattr(os, "O_NOFOLLOW", 0),
        )
    except OSError as exc:
        raise AuthenticationError(
            "endpoint capability root must be an owner-private real directory"
        ) from exc
    try:
        opened = os.fstat(descriptor)
        if (
            stat.S_ISLNK(before.st_mode)
            or not stat.S_ISDIR(before.st_mode)
            or not stat.S_ISDIR(opened.st_mode)
            or before.st_uid != os.geteuid()
            or opened.st_uid != os.geteuid()
            or before.st_mode & 0o077
            or opened.st_mode & 0o077
            or (before.st_dev, before.st_ino) != (opened.st_dev, opened.st_ino)
        ):
            raise AuthenticationError(
                "endpoint capability root must be an owner-private real directory"
            )
    finally:
        os.close(descriptor)


def _ensure_owner_private_real_directory(path: Path) -> None:
    if path.exists() or path.is_symlink():
        _require_owner_private_real_directory(path)
        return
    if host_platform() == "windows":
        from agentnet.windows_security import ensure_private_directory

        try:
            ensure_private_directory(path)
        except Exception as exc:
            raise AuthenticationError(
                "endpoint capability root could not be created privately"
            ) from exc
    else:
        try:
            path.mkdir(mode=0o700)
        except OSError as exc:
            raise AuthenticationError(
                "endpoint capability root could not be created privately"
            ) from exc
    _require_owner_private_real_directory(path)


def _read_owner_private_capability(path: Path) -> bytes:
    _require_owner_private_real_directory(path.parent)
    if host_platform() == "windows":
        from agentnet.windows_security import read_private_file

        try:
            value = read_private_file(path, max_bytes=ENDPOINT_CAPABILITY_ROOT_BYTES)
        except Exception as exc:
            raise AuthenticationError("endpoint capability root is unavailable") from exc
        if len(value) != ENDPOINT_CAPABILITY_ROOT_BYTES:
            raise AuthenticationError("endpoint capability root has invalid length")
        return value
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise AuthenticationError("endpoint capability root is unavailable") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_nlink != 1
            or before.st_mode & 0o077
            or before.st_size != ENDPOINT_CAPABILITY_ROOT_BYTES
        ):
            raise AuthenticationError(
                "endpoint capability root must be an owner-private regular file"
            )
        value = os.read(descriptor, ENDPOINT_CAPABILITY_ROOT_BYTES + 1)
        after = os.fstat(descriptor)
        if len(value) != ENDPOINT_CAPABILITY_ROOT_BYTES or (
            before.st_dev,
            before.st_ino,
            before.st_size,
            before.st_mtime_ns,
        ) != (
            after.st_dev,
            after.st_ino,
            after.st_size,
            after.st_mtime_ns,
        ):
            raise AuthenticationError("endpoint capability root changed while being read")
        return value
    finally:
        os.close(descriptor)


def _create_owner_private_capability(path: Path) -> bytes:
    value = secrets.token_bytes(ENDPOINT_CAPABILITY_ROOT_BYTES)
    if host_platform() == "windows":
        from agentnet.windows_security import write_private_file

        try:
            write_private_file(path, value)
        except Exception as exc:
            raise AuthenticationError("endpoint capability root could not be created") from exc
        return _read_owner_private_capability(path)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags, 0o600)
    except FileExistsError:
        return _read_owner_private_capability(path)
    except OSError as exc:
        raise AuthenticationError("endpoint capability root could not be created") from exc
    try:
        offset = 0
        while offset < len(value):
            written = os.write(descriptor, value[offset:])
            if written <= 0:
                raise AuthenticationError("endpoint capability root write made no progress")
            offset += written
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    return _read_owner_private_capability(path)


def _replace_owner_private_capability(path: Path) -> bytes:
    """Re-mint an uncommitted capability root left by an interrupted write.

    No committed digest can reference these bytes, so the exact owner-private
    inode is replaced rather than adopted.  A symlink, foreign owner, hard
    link, or non-regular file still fails closed.
    """

    _require_owner_private_real_directory(path.parent)
    if host_platform() == "windows":
        from agentnet.windows_security import require_private_path

        try:
            require_private_path(path, directory=False)
        except Exception as exc:
            raise AuthenticationError(
                "endpoint capability root must be an owner-private regular file"
            ) from exc
        path.unlink()
        return _create_owner_private_capability(path)
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise AuthenticationError(
            "uncommitted endpoint capability root is unavailable"
        ) from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
        ):
            raise AuthenticationError(
                "endpoint capability root must be an owner-private regular file"
            )
    finally:
        os.close(descriptor)
    os.unlink(path)
    return _create_owner_private_capability(path)


def _endpoint_directory(
    base: Path, *, domain_id: str, harness_id: str, adapter_generation: int
) -> Path:
    return base / "endpoints" / endpoint_capability_root_name(
        domain_id=domain_id,
        harness_id=harness_id,
        adapter_generation=adapter_generation,
    )


def endpoint_root(base: Path, binding: EndpointBinding) -> Path:
    """Return and validate the opaque private directory for one exact generation."""

    root = _endpoint_directory(
        base,
        domain_id=binding.domain_id,
        harness_id=binding.harness_id,
        adapter_generation=binding.adapter_generation,
    )
    if binding.capability_root_path != root / _CAPABILITY_FILE_NAME:
        raise AuthenticationError("endpoint capability root path does not match its exact binding")
    _require_owner_private_real_directory(root)
    return root


def read_capability_root(binding: EndpointBinding) -> bytes:
    """Read capability bytes only from their exact owner-private endpoint file."""

    return _read_owner_private_capability(binding.capability_root_path)


def read_capability_digest(binding: EndpointBinding) -> str:
    return sha256(read_capability_root(binding)).hexdigest()


class EndpointBindingRepository:
    """Load and rotate exact endpoint descriptors against current authority."""

    def __init__(
        self,
        store: StoreBackend,
        capability_base: Path,
        *,
        clock: Callable[[], int] | None = None,
    ) -> None:
        self.store = store
        self.capability_base = Path(capability_base).absolute()
        self._clock = clock or (lambda: int(time.time()))
        _ensure_owner_private_real_directory(self.capability_base)
        _ensure_owner_private_real_directory(self.capability_base / "endpoints")

    def _row(self, connection: Any, *, domain_id: str, harness_id: str) -> Any:
        row = connection.execute(
            """SELECT e.*,h.kind AS enrolled_harness_kind,h.domain_id AS enrolled_domain_id,
                      h.principal_id AS enrolled_principal_id
                 FROM endpoint_lifecycle e
                 JOIN harnesses h ON h.harness_id=e.harness_id
                WHERE e.domain_id=? AND e.harness_id=?""",
            (domain_id, harness_id),
        ).fetchone()
        if row is None:
            raise AuthenticationError("exact endpoint binding is unavailable")
        return row

    def _validated_credential(self, connection: Any, row: Any):
        if row["state"] not in _ROTATABLE_ENDPOINT_STATES:
            raise AuthenticationError("exact endpoint is not currently enrolled")
        if (
            row["domain_id"] != row["enrolled_domain_id"]
            or row["principal_id"] != row["enrolled_principal_id"]
            or row["harness_kind"] != row["enrolled_harness_kind"]
        ):
            raise AuthenticationError("endpoint lifecycle identity changed")
        credential = load_credential_binding_from_connection(
            connection, row["current_credential_id"]
        )
        credential.require_active(now=self._clock())
        if (
            credential.domain_id != row["domain_id"]
            or credential.harness_id != row["harness_id"]
            or credential.principal_id != row["principal_id"]
            or credential.credential_id != row["current_credential_id"]
            or credential.credential_epoch != credential.harness_credential_epoch
        ):
            raise AuthenticationError("endpoint current credential binding changed")
        return credential

    def _capability_path(self, row: Any) -> Path:
        _require_owner_private_real_directory(self.capability_base)
        _require_owner_private_real_directory(self.capability_base / "endpoints")
        directory = _endpoint_directory(
            self.capability_base,
            domain_id=row["domain_id"],
            harness_id=row["harness_id"],
            adapter_generation=int(row["adapter_generation"]),
        )
        _ensure_owner_private_real_directory(directory)
        return directory / _CAPABILITY_FILE_NAME

    def _materialize_capability(self, connection: Any, row: Any) -> Path:
        path = self._capability_path(row)
        expected_digest = row["capability_root_digest"]
        if expected_digest is None:
            if path.is_symlink():
                raise AuthenticationError(
                    "endpoint capability root must be an owner-private regular file"
                )
            value = (
                _replace_owner_private_capability(path)
                if path.exists()
                else _create_owner_private_capability(path)
            )
            digest = sha256(value).hexdigest()
            updated = connection.execute(
                """UPDATE endpoint_lifecycle
                      SET capability_root_digest=?,revision=revision+1,updated_at=?
                    WHERE domain_id=? AND harness_id=? AND adapter_generation=?
                      AND capability_root_digest IS NULL""",
                (
                    digest,
                    self._clock(),
                    row["domain_id"],
                    row["harness_id"],
                    row["adapter_generation"],
                ),
            )
            if updated.rowcount != 1:
                raise AuthenticationError("endpoint capability generation changed")
            expected_digest = digest
        actual_digest = sha256(_read_owner_private_capability(path)).hexdigest()
        if not compare_digest(actual_digest, str(expected_digest)):
            raise AuthenticationError("endpoint capability root digest changed")
        return path

    def load_current(self, *, domain_id: str, harness_id: str) -> EndpointBinding:
        if not domain_id or not harness_id:
            raise AuthenticationError("exact endpoint identity is required")
        with self.store.transaction() as connection:
            row = self._row(connection, domain_id=domain_id, harness_id=harness_id)
            credential = self._validated_credential(connection, row)
            if row["state"] != "connected":
                raise AuthenticationError("exact endpoint is not currently connected")
            if row["process_measurement"] is None:
                raise AuthenticationError("endpoint process measurement is unavailable")
            process_measurement = process_measurement_digest(row["process_measurement"])
            capability_path = self._materialize_capability(connection, row)
            return EndpointBinding(
                domain_id=row["domain_id"],
                principal_id=row["principal_id"],
                harness_id=row["harness_id"],
                harness_kind=row["harness_kind"],
                credential_id=credential.credential_id,
                credential_epoch=credential.credential_epoch,
                adapter_generation=int(row["adapter_generation"]),
                mailbox_cursor=int(row["mailbox_cursor"]),
                profile_key=row["profile_key"],
                capability_root_path=capability_path,
                process_measurement=process_measurement,
            )

    def rotate_generation(
        self,
        *,
        actor: VerifiedActor,
        expected_generation: int,
        process_measurement: str,
    ) -> EndpointBinding:
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
        ):
            raise AuthenticationError("endpoint generation rotation requires an exact human harness")
        measurement = process_measurement_digest(process_measurement)
        with self.store.transaction() as connection:
            row = self._row(
                connection,
                domain_id=actor.domain_id,
                harness_id=actor.harness_id,
            )
            credential = self._validated_credential(connection, row)
            if (
                row["principal_id"] != actor.principal_id
                or credential.credential_id != actor.credential_id
                or credential.credential_epoch != actor.credential_epoch
            ):
                raise AuthenticationError("endpoint rotation actor is stale")
            if int(row["adapter_generation"]) != expected_generation:
                raise AuthenticationError("endpoint adapter generation is stale")
            next_generation = expected_generation + 1
            next_row = dict(row)
            next_row["adapter_generation"] = next_generation
            next_row["capability_root_digest"] = None
            capability_path = self._capability_path(next_row)
            if capability_path.exists() or capability_path.is_symlink():
                raise AuthenticationError("next endpoint capability generation already exists")
            digest = sha256(_create_owner_private_capability(capability_path)).hexdigest()
            updated = connection.execute(
                """UPDATE endpoint_lifecycle
                      SET adapter_generation=?,process_measurement=?,capability_root_digest=?,
                          state='connected',state_reason='binding_generation_rotated',
                          revision=revision+1,updated_at=?
                    WHERE domain_id=? AND harness_id=? AND adapter_generation=?
                      AND current_credential_id=? AND revision=?""",
                (
                    next_generation,
                    measurement,
                    digest,
                    self._clock(),
                    actor.domain_id,
                    actor.harness_id,
                    expected_generation,
                    actor.credential_id,
                    row["revision"],
                ),
            )
            if updated.rowcount != 1:
                raise AuthenticationError("endpoint adapter generation changed during rotation")
        return self.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)

    def verify_current(
        self,
        binding: EndpointBinding,
        *,
        process_measurement: str | None = None,
    ) -> EndpointBinding:
        current = self.load_current(
            domain_id=binding.domain_id,
            harness_id=binding.harness_id,
        )
        if (
            current.domain_id != binding.domain_id
            or current.harness_id != binding.harness_id
            or current.principal_id != binding.principal_id
            or current.harness_kind != binding.harness_kind
            or current.profile_key != binding.profile_key
        ):
            raise AuthenticationError("exact endpoint identity changed")
        if (
            current.credential_id != binding.credential_id
            or current.credential_epoch != binding.credential_epoch
        ):
            raise AuthenticationError("endpoint credential epoch changed")
        if current.adapter_generation != binding.adapter_generation:
            raise AuthenticationError("endpoint adapter generation changed")
        if current.process_measurement != binding.process_measurement:
            raise AuthenticationError("endpoint process measurement changed")
        if current.capability_root_path != binding.capability_root_path:
            raise AuthenticationError("endpoint capability generation changed")
        if process_measurement is not None and not compare_digest(
            current.process_measurement,
            process_measurement_digest(process_measurement),
        ):
            raise AuthenticationError("endpoint process measurement changed")
        return current

    def require_current(
        self,
        binding: EndpointBinding,
        *,
        process_measurement: str | None = None,
    ) -> EndpointBinding:
        return self.verify_current(binding, process_measurement=process_measurement)


__all__ = [
    "EndpointBinding",
    "EndpointBindingRepository",
    "endpoint_root",
    "exact_process_measurement",
    "process_measurement_digest",
    "read_capability_digest",
    "read_capability_root",
]
