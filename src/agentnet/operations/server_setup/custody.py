"""Host custody and bounded process primitives for server setup."""

from __future__ import annotations

import json
import os
import select
import signal
import stat
import subprocess
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping

if os.name == "posix":
    import grp
    import pwd

from agentnet import __version__
from agentnet.security.signatures import canonical_digest

from .models import ServerSetupError
from .preflight import _read_bounded_snapshot, _strict_json_bytes
from .systemd import _SYSTEM_PATH, _managed_service_runtime as _rendered_service_runtime

_MAX_UNIT_BYTES = 65_536




def _validate_account(account: pwd.struct_passwd, name: str, home: Path) -> None:
    shells = {"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"}
    try:
        primary_group = grp.getgrgid(account.pw_gid)
        effective_groups = set(os.getgrouplist(name, account.pw_gid))
        primary_peers = {
            candidate.pw_name
            for candidate in pwd.getpwall()
            if candidate.pw_gid == account.pw_gid
        }
    except (KeyError, OSError) as exc:
        raise ServerSetupError("identity_conflict", f"existing {name} account conflicts with fixed profile") from exc
    if (
        account.pw_uid == 0
        or account.pw_gid == 0
        or Path(account.pw_dir) != home
        or account.pw_shell not in shells
        or primary_group.gr_name != name
        or effective_groups != {account.pw_gid}
        or primary_peers != {name}
        or not set(primary_group.gr_mem) <= {name}
    ):
        raise ServerSetupError("identity_conflict", f"existing {name} account conflicts with fixed profile")


def _account_fact(name: str, home: Path) -> str:
    try:
        account = pwd.getpwnam(name)
    except KeyError:
        try:
            grp.getgrnam(name)
        except KeyError:
            return "create"
        raise ServerSetupError(
            "identity_conflict",
            f"existing {name} group conflicts with fixed profile",
        )
    _validate_account(account, name, home)
    return "already_satisfied"


def _atomic_write(path: Path, payload: bytes, *, mode: int, uid: int = 0, gid: int = 0) -> str:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.parent.is_symlink():
        raise ServerSetupError("unsafe_path", "managed AgentNet path is a symlink")
    try:
        existing = os.open(
            path,
            os.O_RDONLY | os.O_NONBLOCK | getattr(os, "O_NOFOLLOW", 0),
        )
    except FileNotFoundError:
        existing = None
    except OSError as exc:
        raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile") from exc
    if existing is not None:
        try:
            before = os.fstat(existing)
            if (
                not stat.S_ISREG(before.st_mode)
                or before.st_nlink != 1
                or before.st_size != len(payload)
                or stat.S_IMODE(before.st_mode) != mode
                or before.st_uid != uid
                or before.st_gid != gid
            ):
                raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile")
            current = os.read(existing, len(payload) + 1)
            after = os.fstat(existing)
            if (
                current == payload
                and after.st_dev == before.st_dev
                and after.st_ino == before.st_ino
                and after.st_size == before.st_size
                and after.st_mtime_ns == before.st_mtime_ns
                and after.st_ctime_ns == before.st_ctime_ns
            ):
                return "already_satisfied"
            raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile")
        finally:
            os.close(existing)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        os.fchown(descriptor, uid, gid)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        try:
            os.link(temporary, path, follow_symlinks=False)
        except FileExistsError as exc:
            raise ServerSetupError("managed_path_conflict", "managed AgentNet path conflicts with fixed profile") from exc
        temporary.unlink()
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise
    return "completed"


def _read_managed_exact(
    path: Path,
    *,
    uid: int,
    gid: int,
    mode: int,
    blocker: str,
    label: str,
    max_bytes: int = 65_536,
) -> bytes | None:
    """Read one root-owned managed file exactly, or report that it is absent."""

    try:
        descriptor = os.open(
            path,
            os.O_RDONLY
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
        )
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise ServerSetupError(blocker, f"{label} custody is unsafe") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_nlink != 1
            or before.st_uid != uid
            or before.st_gid != gid
            or stat.S_IMODE(before.st_mode) != mode
            or not 1 <= before.st_size <= max_bytes
        ):
            raise ServerSetupError(blocker, f"{label} custody is unsafe")
        payload = os.read(descriptor, before.st_size + 1)
        after = os.fstat(descriptor)
        if (
            len(payload) != before.st_size
            or after.st_dev != before.st_dev
            or after.st_ino != before.st_ino
            or after.st_size != before.st_size
            or after.st_mtime_ns != before.st_mtime_ns
            or after.st_ctime_ns != before.st_ctime_ns
        ):
            raise ServerSetupError(blocker, f"{label} changed during preflight")
        return payload
    finally:
        os.close(descriptor)


def _read_setup_marker(path: Path, *, uid: int, gid: int) -> bytes | None:
    return _read_managed_exact(
        path,
        uid=uid,
        gid=gid,
        mode=0o600,
        blocker="setup_marker_conflict",
        label="setup marker",
    )


def _read_managed_unit(path: Path, *, uid: int, gid: int, blocker: str) -> bytes | None:
    return _read_managed_exact(
        path,
        uid=uid,
        gid=gid,
        mode=0o644,
        blocker=blocker,
        label="managed AgentNet unit",
        max_bytes=_MAX_UNIT_BYTES,
    )


def _atomic_replace_exact(
    path: Path,
    *,
    expected: bytes,
    payload: bytes,
    mode: int,
    uid: int,
    gid: int,
    reader: Callable[[Path], bytes | None] | None = None,
    blocker: str = "setup_marker_conflict",
    label: str = "setup marker",
    result: str = "updated_same_request",
) -> str:
    def read(target: Path) -> bytes | None:
        if reader is not None:
            return reader(target)
        return _read_setup_marker(target, uid=uid, gid=gid)

    current = read(path)
    if current != expected:
        raise ServerSetupError(blocker, f"{label} changed before compare-and-swap")
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        os.fchown(descriptor, uid, gid)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        before_replace = path.lstat()
        if (
            not stat.S_ISREG(before_replace.st_mode)
            or before_replace.st_nlink != 1
            or before_replace.st_uid != uid
            or before_replace.st_gid != gid
            or stat.S_IMODE(before_replace.st_mode) != mode
            or read(path) != expected
        ):
            raise ServerSetupError(blocker, f"{label} changed before compare-and-swap")
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise
    return result


def _write_managed_unit(
    path: Path,
    payload: bytes,
    *,
    uid: int,
    gid: int,
    previous: bytes | None,
) -> str:
    """Install one managed unit, replacing exactly the journaled previous payload.

    Outside a journaled package upgrade this keeps the strict behaviour that a
    managed path holding unexpected content is a conflict, never an overwrite.
    """

    if previous is not None and previous != payload:
        current = _read_managed_unit(path, uid=uid, gid=gid, blocker="managed_path_conflict")
        if current == previous:
            return _atomic_replace_exact(
                path,
                expected=previous,
                payload=payload,
                mode=0o644,
                uid=uid,
                gid=gid,
                reader=lambda target: _read_managed_unit(
                    target,
                    uid=uid,
                    gid=gid,
                    blocker="managed_path_conflict",
                ),
                blocker="managed_path_conflict",
                label="managed AgentNet unit",
                result="updated_package_upgrade",
            )
    return _atomic_write(path, payload, mode=0o644, uid=uid, gid=gid)


def _remove_managed_unit_exact(
    path: Path,
    *,
    expected: bytes,
    uid: int,
    gid: int,
) -> None:
    """Remove one upgrade-created unit only while its exact bytes remain."""

    current = _read_managed_unit(
        path,
        uid=uid,
        gid=gid,
        blocker="setup_upgrade_conflict",
    )
    if current is None:
        return
    if current != expected:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "upgrade-created managed unit changed before rollback",
        )
    before = path.lstat()
    if (
        not stat.S_ISREG(before.st_mode)
        or before.st_nlink != 1
        or before.st_uid != uid
        or before.st_gid != gid
        or stat.S_IMODE(before.st_mode) != 0o644
        or _read_managed_unit(
            path,
            uid=uid,
            gid=gid,
            blocker="setup_upgrade_conflict",
        )
        != expected
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "upgrade-created managed unit changed before rollback",
        )
    path.unlink()
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _ensure_account(
    name: str,
    home: Path,
    *,
    useradd_executable: Path,
) -> pwd.struct_passwd:
    try:
        account = pwd.getpwnam(name)
    except KeyError:
        completed = subprocess.run(
            [
                str(useradd_executable),
                "--system",
                "--user-group",
                "--no-create-home",
                "--home-dir",
                str(home),
                "--shell",
                "/usr/sbin/nologin",
                name,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if completed.returncode != 0:
            raise ServerSetupError("identity_create_failed", f"failed to create dedicated {name} identity")
        account = pwd.getpwnam(name)
    _account_fact(name, home)
    return account


def _ensure_root_private_directory(path: Path, *, uid: int, gid: int, label: str) -> str:
    if path.exists() or path.is_symlink():
        try:
            metadata = path.lstat()
        except OSError as exc:
            raise ServerSetupError(f"{label}_conflict", f"{label} root is unavailable") from exc
        if (
            not stat.S_ISDIR(metadata.st_mode)
            or metadata.st_uid != uid
            or metadata.st_gid != gid
            or stat.S_IMODE(metadata.st_mode) != 0o700
        ):
            raise ServerSetupError(f"{label}_conflict", f"{label} root conflicts with fixed profile")
        return "already_satisfied"
    path.mkdir(parents=True, mode=0o700)
    os.chown(path, uid, gid)
    os.chmod(path, 0o700)
    return "completed"


def _ensure_private_root(path: Path, account: pwd.struct_passwd) -> str:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        metadata = None
    except OSError as exc:
        raise ServerSetupError("private_root_conflict", "private AgentNet root is unavailable") from exc
    if metadata is not None:
        if (
            not stat.S_ISDIR(metadata.st_mode)
            or metadata.st_uid != account.pw_uid
            or metadata.st_gid != account.pw_gid
            or stat.S_IMODE(metadata.st_mode) != 0o700
        ):
            raise ServerSetupError("private_root_conflict", "private AgentNet root conflicts with fixed profile")
        return "already_satisfied"
    path.mkdir(parents=True, mode=0o700)
    os.chown(path, account.pw_uid, account.pw_gid)
    os.chmod(path, 0o700)
    return "completed"


def _service_environment(
    base: Mapping[str, str],
    data: Path,
    uv_executable: Path,
    *,
    allowed_names: frozenset[str],
) -> dict[str, str]:
    reserved = {
        "PATH",
        "HOME",
        "XDG_STATE_HOME",
        "XDG_CACHE_HOME",
        "AGENTNET_NPM_RUNTIME_DIR",
        "AGENTNET_UV",
        "AGENTNET_PACKAGE_ROOT",
        "AGENTNET_NODE_EXECUTABLE",
    }
    supplied = set(base)
    if reserved & supplied:
        raise ServerSetupError("reserved_environment", "runtime environment overrides a setup-owned variable")
    if supplied != set(allowed_names):
        raise ServerSetupError("unexpected_environment", "runtime environment names do not match fixed request references")
    environment = {
        "PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "HOME": str(data),
        "XDG_STATE_HOME": str(data / ".local/state"),
        "XDG_CACHE_HOME": str(data / ".cache"),
        "AGENTNET_NPM_RUNTIME_DIR": str(data / "npm-runtime"),
        "AGENTNET_UV": str(uv_executable),
    }
    environment.update(base)
    return environment


def _drop_identity(account: pwd.struct_passwd):
    def apply() -> None:
        os.setgroups([])
        os.setgid(account.pw_gid)
        os.setuid(account.pw_uid)
    return apply


@dataclass(frozen=True)
class _BoundedCommandResult:
    returncode: int
    stdout: bytes
    stderr_present: bool


def _kill_product_process_tree(process: subprocess.Popen[bytes]) -> None:
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except OSError:
        if process.poll() is None:
            try:
                process.kill()
            except OSError:
                pass


def _run_bounded_product_process(
    account: pwd.struct_passwd,
    argv: list[str],
    *,
    environment: Mapping[str, str],
    stage: str,
) -> _BoundedCommandResult:
    try:
        process = subprocess.Popen(
            argv,
            env=dict(environment),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            preexec_fn=_drop_identity(account),
            start_new_session=True,
            text=False,
            bufsize=0,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise ServerSetupError("product_command_failed", f"{stage} could not start") from exc
    if process.stdout is None or process.stderr is None:  # pragma: no cover - Popen invariant
        _kill_product_process_tree(process)
        process.wait()
        raise ServerSetupError("invalid_product_evidence", f"{stage} returned invalid evidence streams")

    stdout = bytearray()
    stderr_bytes = 0
    streams = {
        process.stdout.fileno(): "stdout",
        process.stderr.fileno(): "stderr",
    }
    deadline = time.monotonic() + 300
    try:
        for descriptor in streams:
            os.set_blocking(descriptor, False)
        while streams:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(argv, 300)
            readable, _, _ = select.select(tuple(streams), (), (), min(remaining, 1.0))
            if not readable:
                if process.poll() is not None:
                    raise ServerSetupError(
                        "invalid_product_evidence",
                        f"{stage} left structured evidence streams open",
                    )
                continue
            for descriptor in readable:
                try:
                    chunk = os.read(descriptor, 65_536)
                except BlockingIOError:
                    continue
                if not chunk:
                    streams.pop(descriptor, None)
                    continue
                if streams[descriptor] == "stdout":
                    if len(stdout) + len(chunk) > 1_048_576:
                        raise ServerSetupError(
                            "invalid_product_evidence",
                            f"{stage} returned oversized structured evidence",
                        )
                    stdout.extend(chunk)
                else:
                    stderr_bytes += len(chunk)
                    if stderr_bytes > 65_536:
                        raise ServerSetupError(
                            "invalid_product_evidence",
                            f"{stage} returned oversized error evidence",
                        )
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise subprocess.TimeoutExpired(argv, 300)
        returncode = process.wait(timeout=remaining)
    except BaseException:
        _kill_product_process_tree(process)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        raise
    finally:
        process.stdout.close()
        process.stderr.close()
    return _BoundedCommandResult(
        returncode=returncode,
        stdout=bytes(stdout),
        stderr_present=stderr_bytes > 0,
    )


def _prepare_managed_service_runtime(
    account: pwd.struct_passwd,
    *,
    data_root: Path,
    node_executable: Path,
    agentnet_executable: Path,
    uv_executable: Path,
    stage: str,
) -> None:
    """Materialize the exact package runtime before bounded service startup."""

    runtime_root = _rendered_service_runtime(data_root, package_version=__version__)
    environment = {
        "PATH": _SYSTEM_PATH,
        "HOME": str(data_root),
        "LANG": "C.UTF-8",
        "XDG_STATE_HOME": str(data_root / ".local" / "state"),
        "XDG_CACHE_HOME": str(data_root / ".cache"),
        "AGENTNET_NPM_RUNTIME_DIR": str(runtime_root),
        "AGENTNET_UV": str(uv_executable),
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONPYCACHEPREFIX": str(runtime_root / "pycache"),
        "UV_NO_MODIFY_PATH": "1",
        "UV_PROJECT_ENVIRONMENT": str(runtime_root),
    }
    try:
        completed = _run_bounded_product_process(
            account,
            [str(node_executable), str(agentnet_executable), "--version"],
            environment=environment,
            stage=stage,
        )
    except subprocess.TimeoutExpired as exc:
        raise ServerSetupError(
            "service_runtime_prepare",
            f"{stage} timed out",
        ) from exc
    if (
        completed.returncode != 0
        or completed.stdout != f"agentnet {__version__}\n".encode()
    ):
        raise ServerSetupError(
            "service_runtime_prepare",
            f"{stage} did not materialize the exact package runtime",
        )
    _require_private_directory(
        runtime_root,
        account,
        blocker="service_runtime_prepare",
    )


def _run_as(
    account: pwd.struct_passwd,
    argv: list[str],
    *,
    environment: Mapping[str, str],
    stage: str,
    accepted_returncodes: frozenset[int] = frozenset({0}),
) -> dict[str, Any]:
    try:
        completed = _run_bounded_product_process(
            account,
            argv,
            environment=environment,
            stage=stage,
        )
    except subprocess.TimeoutExpired as exc:
        raise ServerSetupError("product_command_failed", f"{stage} timed out") from exc
    except (OSError, subprocess.SubprocessError) as exc:
        raise ServerSetupError("product_command_failed", f"{stage} could not start") from exc
    if completed.returncode not in accepted_returncodes:
        stderr_state = "stderr_present" if completed.stderr_present else "no_stderr"
        raise ServerSetupError(
            "product_command_failed",
            f"{stage} failed with exit status {completed.returncode} ({stderr_state})",
        )
    try:
        value = json.loads(completed.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ServerSetupError("invalid_product_evidence", f"{stage} returned invalid structured evidence") from exc
    if not isinstance(value, dict):
        raise ServerSetupError("invalid_product_evidence", f"{stage} returned invalid structured evidence")
    return value


def _private_entry_exists(
    path: Path,
    account: pwd.struct_passwd,
    *,
    expected: str,
    blocker: str,
) -> bool:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        return False
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private path is unavailable") from exc
    if expected == "file":
        valid_type = stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1
        expected_mode = 0o600
    elif expected == "directory":
        valid_type = stat.S_ISDIR(metadata.st_mode)
        expected_mode = 0o700
    else:  # pragma: no cover - internal invariant
        raise AssertionError(expected)
    if (
        stat.S_ISLNK(metadata.st_mode)
        or not valid_type
        or metadata.st_uid != account.pw_uid
        or metadata.st_gid != account.pw_gid
        or stat.S_IMODE(metadata.st_mode) != expected_mode
    ):
        raise ServerSetupError(blocker, "managed private path custody conflicts with fixed profile")
    return True


def _require_communication_only_artifact_absence(core_runtime: Path) -> None:
    forbidden = (
        core_runtime / "secrets" / "artifact.key",
        core_runtime / "artifacts",
    )
    if any(path.exists() or path.is_symlink() for path in forbidden):
        raise ServerSetupError(
            "core_conflict",
            "communication-only Core state contains forbidden artifact state",
        )


def _require_private_file(path: Path, account: pwd.struct_passwd, *, blocker: str) -> None:
    try:
        metadata = path.lstat()
        resolved = path.resolve(strict=True)
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private file is unavailable") from exc
    if (
        resolved != path
        or not stat.S_ISREG(metadata.st_mode)
        or metadata.st_nlink != 1
        or metadata.st_uid != account.pw_uid
        or metadata.st_gid != account.pw_gid
        or stat.S_IMODE(metadata.st_mode) != 0o600
    ):
        raise ServerSetupError(blocker, "managed private file custody conflicts with fixed profile")


def _read_private_managed_file(
    path: Path,
    account: pwd.struct_passwd,
    *,
    blocker: str,
    max_bytes: int,
) -> bytes:
    _require_private_file(path, account, blocker=blocker)
    try:
        descriptor = os.open(
            path,
            os.O_RDONLY
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
        )
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private file is unavailable") from exc
    try:
        before = os.fstat(descriptor)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_nlink != 1
            or before.st_uid != account.pw_uid
            or before.st_gid != account.pw_gid
            or stat.S_IMODE(before.st_mode) != 0o600
            or not 1 <= before.st_size <= max_bytes
        ):
            raise ServerSetupError(blocker, "managed private file size or custody conflicts with fixed profile")
        changed_message = "managed private file changed while being read"
        first = _read_bounded_snapshot(
            descriptor,
            before.st_size,
            blocker=blocker,
            message=changed_message,
        )
        middle = os.fstat(descriptor)
        second = _read_bounded_snapshot(
            descriptor,
            before.st_size,
            blocker=blocker,
            message=changed_message,
        )
        after = os.fstat(descriptor)
        current = path.lstat()
        if (
            len(first) != before.st_size
            or first != second
            or any(
                getattr(snapshot, field) != getattr(before, field)
                for snapshot in (middle, after)
                for field in (
                    "st_dev",
                    "st_ino",
                    "st_size",
                    "st_mtime_ns",
                    "st_ctime_ns",
                )
            )
            or current.st_dev != before.st_dev
            or current.st_ino != before.st_ino
        ):
            raise ServerSetupError(blocker, changed_message)
        return first
    finally:
        os.close(descriptor)


def _managed_config_digest(
    path: Path,
    account: pwd.struct_passwd,
    *,
    blocker: str,
    exclude_top_level: frozenset[str] = frozenset(),
) -> str:
    value = _strict_json_bytes(
        _read_private_managed_file(
            path,
            account,
            blocker=blocker,
            max_bytes=1_048_576,
        ),
        label="managed AgentNet configuration",
    )
    for key in exclude_top_level:
        value.pop(key, None)
    return canonical_digest(value)


def _require_private_directory(path: Path, account: pwd.struct_passwd, *, blocker: str) -> None:
    try:
        metadata = path.lstat()
        resolved = path.resolve(strict=True)
    except OSError as exc:
        raise ServerSetupError(blocker, "managed private directory is unavailable") from exc
    if (
        resolved != path
        or not stat.S_ISDIR(metadata.st_mode)
        or metadata.st_uid != account.pw_uid
        or metadata.st_gid != account.pw_gid
        or stat.S_IMODE(metadata.st_mode) != 0o700
    ):
        raise ServerSetupError(blocker, "managed private directory custody conflicts with fixed profile")


def _require_private_tree(
    root: Path,
    account: pwd.struct_passwd,
    *,
    blocker: str,
) -> None:
    _require_private_directory(root, account, blocker=blocker)
    pending = [root]
    records = 0
    while pending:
        directory = pending.pop()
        try:
            entries = tuple(os.scandir(directory))
        except OSError as exc:
            raise ServerSetupError(blocker, "managed private tree is unavailable") from exc
        for entry in entries:
            records += 1
            if records > 20_000:
                raise ServerSetupError(blocker, "managed private tree exceeds fixed custody bound")
            item = Path(entry.path)
            try:
                metadata = entry.stat(follow_symlinks=False)
            except OSError as exc:
                raise ServerSetupError(blocker, "managed private tree changed during validation") from exc
            if stat.S_ISDIR(metadata.st_mode):
                _require_private_directory(item, account, blocker=blocker)
                pending.append(item)
            elif stat.S_ISREG(metadata.st_mode):
                _require_private_file(item, account, blocker=blocker)
            else:
                raise ServerSetupError(blocker, "managed private tree contains an unsupported entry")
