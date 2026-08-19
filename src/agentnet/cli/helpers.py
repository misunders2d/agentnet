"""Common file I/O, security, identity, and HTTP helpers for the AgentNet CLI."""

from __future__ import annotations

import base64
import hashlib
import ipaddress
import json
import os
import secrets
import stat
from contextlib import contextmanager
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit
from uuid import uuid4

try:
    import fcntl
except ModuleNotFoundError:  # pragma: no cover - exercised on Windows CI
    fcntl = None  # type: ignore[assignment]

import httpx

from agentnet.client import MAX_ARTIFACT_BYTES, AgentNetClient
from agentnet.operations.config import (
    BackupSealKeyConfig,
    ExtensionConfig,
    RuntimeProfile,
)
from agentnet.operations.config_migration import load_config_json
from agentnet.operations.backup import (
    ManifestSeal,
    read_manifest_seal,
)
from agentnet.host import host_platform
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import P256KeyPair, canonical_json


def _load_config(path: Path) -> ExtensionConfig:
    if not path.exists():
        raise SystemExit(f"configuration not found: {path}")
    return load_config_json(path.read_text(encoding="utf-8"))


def _owner_only_directory(path: Path) -> None:
    if host_platform() == "windows":
        from agentnet.windows_security import ensure_private_directory

        try:
            ensure_private_directory(path)
        except Exception as exc:
            raise SystemExit(f"private state directory DACL is unsafe: {path}") from exc
        return
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink() or path.stat().st_mode & 0o077:
        raise SystemExit(f"private state directory must be owner-only and not a symlink: {path}")


def _write_owner_only(path: Path, content: bytes, *, force: bool = False) -> None:
    if host_platform() == "windows":
        from agentnet.windows_security import write_private_file

        try:
            write_private_file(path, content, force=force)
        except FileExistsError as exc:
            raise SystemExit(f"refusing to overwrite {path}; use --force where supported") from exc
        except Exception as exc:
            raise SystemExit(f"private state file DACL is unsafe: {path}") from exc
        return
    _owner_only_directory(path.parent)
    if os.path.lexists(path):
        if not force:
            raise SystemExit(f"refusing to overwrite {path}; use --force where supported")
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077:
            raise SystemExit(f"existing private state is unsafe: {path}")
        descriptor = os.open(path, os.O_WRONLY | os.O_TRUNC)
    else:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        remaining = memoryview(content)
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("private state write made no progress")
            remaining = remaining[written:]
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _write_owner_json(path: Path, value: dict[str, object], *, force: bool = False) -> None:
    _write_owner_only(
        path,
        json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n",
        force=force,
    )


def _write_private_config(
    path: Path,
    value: dict[str, object],
    *,
    force: bool = False,
    expected_content: bytes | None = None,
) -> None:
    """Atomically write a private config without following a raced link/reparse point.

    Config files commonly live in an ordinary 0755 project directory, unlike
    credential state.  The containing directory is therefore not required to
    be private, but the final directory and destination must be real objects
    owned by this process.  A directory descriptor pins the replacement seam.
    """

    if host_platform() == "windows":
        if expected_content is not None:
            current = _owner_only_file(path.absolute(), label="private state")
            if not secrets.compare_digest(current, expected_content):
                raise SystemExit(f"private state changed before replacement: {path}")
        _write_owner_only(
            path,
            json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n",
            force=force,
        )
        return

    parent = path.parent
    parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if parent.is_symlink() or not parent.is_dir() or parent.stat().st_uid != os.geteuid():
        raise SystemExit(f"configuration directory is unsafe: {parent}")
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    directory = os.open(parent, flags)
    temporary_name = f".{path.name}.tmp-{uuid4().hex}"
    descriptor: int | None = None
    installed = False
    try:
        existing: os.stat_result | None
        try:
            existing = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            existing = None
        if existing is not None:
            if not force:
                raise SystemExit(f"refusing to overwrite {path}; use --force")
            if not stat.S_ISREG(existing.st_mode) or existing.st_uid != os.geteuid():
                raise SystemExit(f"existing configuration is unsafe: {path}")
        if expected_content is not None:
            if existing is None:
                raise SystemExit(f"private state disappeared before replacement: {path}")
            existing_descriptor = os.open(
                path.name,
                os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0),
                dir_fd=directory,
            )
            try:
                opened = os.fstat(existing_descriptor)
                if (
                    not stat.S_ISREG(opened.st_mode)
                    or opened.st_uid != os.geteuid()
                    or opened.st_mode & 0o077
                    or opened.st_size > 65_536
                    or (opened.st_dev, opened.st_ino)
                    != (existing.st_dev, existing.st_ino)
                ):
                    raise SystemExit(f"existing private state is unsafe: {path}")
                current_content = bytearray()
                while True:
                    chunk = os.read(existing_descriptor, 16_384)
                    if not chunk:
                        break
                    current_content.extend(chunk)
                    if len(current_content) > 65_536:
                        raise SystemExit(f"existing private state is unsafe: {path}")
                after = os.fstat(existing_descriptor)
                if (
                    opened.st_dev,
                    opened.st_ino,
                    opened.st_mode,
                    opened.st_uid,
                    opened.st_size,
                    opened.st_mtime_ns,
                    opened.st_ctime_ns,
                ) != (
                    after.st_dev,
                    after.st_ino,
                    after.st_mode,
                    after.st_uid,
                    after.st_size,
                    after.st_mtime_ns,
                    after.st_ctime_ns,
                ) or not secrets.compare_digest(bytes(current_content), expected_content):
                    raise SystemExit(f"private state changed before replacement: {path}")
            finally:
                os.close(existing_descriptor)
        create_flags = (
            os.O_WRONLY
            | os.O_CREAT
            | os.O_EXCL
            | getattr(os, "O_NOFOLLOW", 0)
        )
        descriptor = os.open(temporary_name, create_flags, 0o600, dir_fd=directory)
        content = json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n"
        remaining = memoryview(content)
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("configuration write made no progress")
            remaining = remaining[written:]
        os.fchmod(descriptor, 0o600)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None

        try:
            current = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            current = None
        if existing is None:
            if current is not None:
                raise SystemExit(f"configuration destination raced during creation: {path}")
        elif current is None or (current.st_dev, current.st_ino) != (existing.st_dev, existing.st_ino):
            raise SystemExit(f"configuration destination raced during replacement: {path}")
        elif expected_content is not None and (
            current.st_mode,
            current.st_uid,
            current.st_size,
            current.st_mtime_ns,
            current.st_ctime_ns,
        ) != (
            after.st_mode,
            after.st_uid,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        ):
            raise SystemExit(f"private state changed before replacement: {path}")
        os.replace(
            temporary_name,
            path.name,
            src_dir_fd=directory,
            dst_dir_fd=directory,
        )
        installed = True
        os.fsync(directory)
    finally:
        if descriptor is not None:
            os.close(descriptor)
        if not installed:
            try:
                os.unlink(temporary_name, dir_fd=directory)
            except FileNotFoundError:
                pass
        os.close(directory)


def _read_json_object(path: Path, *, label: str) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit(f"{label} is not readable JSON: {path}") from exc
    if not isinstance(value, dict):
        raise SystemExit(f"{label} must be one JSON object: {path}")
    return value


def _owner_only_file(path: Path, *, label: str) -> bytes:
    if not path.is_absolute():
        raise SystemExit(f"{label} must be an owner-only absolute regular file")
    if host_platform() == "windows":
        from agentnet.windows_security import read_private_file

        try:
            return read_private_file(path, max_bytes=65_536)
        except Exception as exc:
            raise SystemExit(f"{label} must have a private current-user DACL") from exc
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise SystemExit(f"{label} must be an owner-only absolute regular file") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.geteuid()
            or before.st_mode & 0o077
            or before.st_size > 65_536
        ):
            raise SystemExit(f"{label} must be an owner-only bounded regular file")
        content = bytearray()
        while True:
            chunk = os.read(descriptor, 16_384)
            if not chunk:
                break
            content.extend(chunk)
        after = os.fstat(descriptor)
        if (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_uid,
            before.st_size,
            before.st_mtime_ns,
            before.st_ctime_ns,
        ) != (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_uid,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        ):
            raise SystemExit(f"{label} changed while it was read")
        return bytes(content)
    finally:
        os.close(descriptor)

@contextmanager
def _private_state_lock(path: Path):
    """Serialize one private state lifecycle across local processes."""

    path = path.absolute()
    lock_path = path.with_name(f".{path.name}.lock")
    if host_platform() == "windows":
        from agentnet.windows_security import (
            require_private_path,
            write_private_file,
        )

        try:
            write_private_file(lock_path, b"\0")
        except FileExistsError:
            pass
        try:
            before = require_private_path(lock_path, directory=False)
            descriptor = os.open(
                lock_path,
                os.O_RDWR | getattr(os, "O_BINARY", 0),
            )
            opened = os.fstat(descriptor)
            if (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino):
                os.close(descriptor)
                raise SystemExit(
                    f"communication scope state lock raced during open: {lock_path}"
                )
        except Exception as exc:
            raise SystemExit(
                f"communication scope state lock is unsafe: {lock_path}"
            ) from exc
    else:
        parent = path.parent
        parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        before_parent = os.stat(parent, follow_symlinks=False)
        if (
            not stat.S_ISDIR(before_parent.st_mode)
            or before_parent.st_uid != os.geteuid()
            or before_parent.st_mode & 0o022
        ):
            raise SystemExit(f"communication scope state lock directory is unsafe: {parent}")
        directory = os.open(
            parent,
            os.O_RDONLY
            | getattr(os, "O_DIRECTORY", 0)
            | getattr(os, "O_NOFOLLOW", 0),
        )
        opened_parent = os.fstat(directory)
        if (
            (opened_parent.st_dev, opened_parent.st_ino)
            != (before_parent.st_dev, before_parent.st_ino)
            or not stat.S_ISDIR(opened_parent.st_mode)
            or opened_parent.st_uid != os.geteuid()
            or opened_parent.st_mode & 0o022
        ):
            os.close(directory)
            raise SystemExit(f"communication scope state lock directory raced: {parent}")
        try:
            descriptor = os.open(
                lock_path.name,
                os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0),
                0o600,
                dir_fd=directory,
            )
            info = os.fstat(descriptor)
            if (
                not stat.S_ISREG(info.st_mode)
                or info.st_uid != os.geteuid()
                or info.st_mode & 0o077
            ):
                os.close(descriptor)
                raise SystemExit(
                    f"communication scope state lock is unsafe: {lock_path}"
                )
        finally:
            os.close(directory)
    locked = False
    try:
        if fcntl is not None:
            fcntl.flock(descriptor, fcntl.LOCK_EX)
        else:  # Windows byte-range lock; the verified file is process-private.
            import msvcrt

            if os.fstat(descriptor).st_size == 0:
                os.write(descriptor, b"\0")
                os.fsync(descriptor)
            os.lseek(descriptor, 0, os.SEEK_SET)
            getattr(msvcrt, "locking")(descriptor, getattr(msvcrt, "LK_LOCK"), 1)
        locked = True
        yield
    finally:
        try:
            if locked:
                if fcntl is not None:
                    fcntl.flock(descriptor, fcntl.LOCK_UN)
                else:
                    import msvcrt

                    os.lseek(descriptor, 0, os.SEEK_SET)
                    getattr(msvcrt, "locking")(
                        descriptor,
                        getattr(msvcrt, "LK_UNLCK"),
                        1,
                    )
        finally:
            os.close(descriptor)


def _canonical_server_origin(value: str) -> str:
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise SystemExit("server URL is invalid") from exc
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise SystemExit("server URL must be one canonical HTTP(S) origin without credentials")
    if parsed.scheme == "http":
        try:
            if not ipaddress.ip_address(parsed.hostname).is_loopback:
                raise SystemExit("plaintext server URL must be an explicit loopback address")
        except ValueError as exc:
            raise SystemExit("plaintext server URL must be an explicit loopback address") from exc
    host = f"[{parsed.hostname.lower()}]" if ":" in parsed.hostname else parsed.hostname.lower()
    default_port = 80 if parsed.scheme == "http" else 443
    rendered = f"{parsed.scheme}://{host}"
    if port not in {None, default_port}:
        rendered += f":{port}"
    if value.rstrip("/") != rendered:
        raise SystemExit("server URL must use canonical origin spelling")
    return rendered


def _public_json_request(
    *,
    server: str,
    method: str,
    path: str,
    body: dict[str, object],
    timeout: float = 10.0,
) -> dict[str, object]:
    origin = _canonical_server_origin(server)
    try:
        response = httpx.request(
            method,
            f"{origin}{path}",
            content=canonical_json(body),
            headers={"Content-Type": "application/json"},
            timeout=timeout,
            follow_redirects=False,
        )
    except httpx.HTTPError as exc:
        raise SystemExit(f"AgentNet server request failed: {type(exc).__name__}") from exc
    if response.status_code < 200 or response.status_code >= 300:
        raise SystemExit(f"AgentNet server rejected the request with HTTP {response.status_code}")
    try:
        value = response.json()
    except ValueError as exc:
        raise SystemExit("AgentNet server returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit("AgentNet server returned a non-object response")
    return value


def _load_identity_profile(path: Path) -> tuple[dict[str, object], VerifiedActor, P256KeyPair]:
    value = _read_json_object(path, label="AgentNet identity profile")
    if set(value) != {
        "schema",
        "server_base_url",
        "audience",
        "actor",
        "private_key_path",
    } or value.get("schema") != "agentnet.identity-profile.v1":
        raise SystemExit("AgentNet identity profile does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(value["actor"])
    except Exception as exc:
        raise SystemExit("AgentNet identity profile actor is invalid") from exc
    if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
        raise SystemExit("AgentNet owner commands require a verified human identity profile")
    key_path = Path(str(value["private_key_path"]))
    key = P256KeyPair.from_private_pem(
        _owner_only_file(key_path, label="AgentNet identity private key")
    )
    return value, actor, key


def _load_identity_client(path: Path) -> tuple[AgentNetClient, VerifiedActor, P256KeyPair]:
    value, actor, key = _load_identity_profile(path)
    client = AgentNetClient(
        base_url=_canonical_server_origin(str(value["server_base_url"])),
        key=key,
        domain_id=actor.domain_id,
        harness_id=actor.harness_id or "",
        credential_id=actor.credential_id or "",
        audience=str(value["audience"]),
    )
    return client, actor, key


def _remove_private_state(
    path: Path,
    *,
    label: str = "managed-server reauthorization state",
) -> None:
    if not os.path.lexists(path):
        return
    _owner_only_file(path, label=label)
    path.unlink()
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _identity_client_json_call(
    identity_path: Path,
    method: str,
    path: str,
    *,
    label: str,
    expected_status: int = 200,
    json_body: dict[str, object] | None = None,
) -> int:
    client, _actor, _key = _load_identity_client(identity_path)
    try:
        if json_body is None:
            response = client.request(method, path)
        else:
            response = client.request(method, path, json_body=json_body)
    finally:
        client.close()
    if response.status_code != expected_status:
        raise SystemExit(f"{label} was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def _validate_http_json_response(
    response: httpx.Response,
    *,
    label: str,
    statuses: frozenset[int] | None = None,
) -> dict[str, object]:
    if statuses is not None:
        if response.status_code not in statuses:
            raise SystemExit(f"{label} was rejected with HTTP {response.status_code}")
    elif response.status_code < 200 or response.status_code >= 300:
        raise SystemExit(f"{label} was rejected with HTTP {response.status_code}")
    try:
        value = response.json()
    except ValueError as exc:
        raise SystemExit(f"{label} returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit(f"{label} returned a non-object response")
    return value

