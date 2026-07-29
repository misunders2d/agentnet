"""Strict package-owned reset for the fixed ordinary Linux server-agent profile.

Reset removes exactly deployment state created by
``agentnet server-agent setup --apply`` and nothing else. Permanent root-only
setup lock/root remain as coordination state so reset and a new setup cannot
lock different inodes. External prerequisites — PostgreSQL and its cluster data,
Node.js, uv, reverse proxy, TLS material, and operator configuration — are never
owned by this package and are always retained. Operation is idempotent: rerunning
on an already reset host proves deployment absence instead of failing, and any
path that is not exactly one allowlisted package entry fails closed rather than
being deleted.
"""

from __future__ import annotations

import os
import pwd
import shutil
import stat
import subprocess
from pathlib import Path
from typing import Any

from agentnet import __version__
from agentnet.operations.server_setup import (
    APPROVAL_DATA,
    APPROVAL_ENV,
    APPROVAL_UNIT,
    CORE_DATA,
    CORE_ENV,
    CORE_UNIT,
    SECRET_ROOT,
    SETUP_MARKER,
    SETUP_ROOT,
    SETUP_RUNTIME_ROOT,
    SETUP_UPGRADE_JOURNAL,
    ServerSetupError,
    SetupLayout,
    _SYSTEM_PATH,
    _SYSTEMCTL_TIMEOUT_SECONDS,
    _ensure_root_private_directory,
    _resolve_host_tool,
)


class ServerSetupResetError(RuntimeError):
    """Fail-closed reset blocker safe to show in redacted operator evidence."""

    def __init__(self, blocker: str, message: str) -> None:
        super().__init__(message)
        self.blocker = blocker


# Every path this package creates, and nothing else.  Secret files are named
# exactly because /etc/agentnet-secrets is a directory an operator can also see.
_MANAGED_SECRET_FILES = (CORE_ENV.name, APPROVAL_ENV.name)
_MANAGED_SETUP_ENTRIES = frozenset(
    {
        "setup.lock",
        SETUP_MARKER.name,
        SETUP_RUNTIME_ROOT.name,
        SETUP_UPGRADE_JOURNAL.name,
    }
)
_RETAINED_EXTERNAL_PREREQUISITES = (
    "postgresql_cluster_and_data",
    "node_and_uv_runtimes",
    "reverse_proxy_and_tls_material",
    "operator_owned_host_configuration",
)


def _require_absent_or_owned_directory(
    path: Path,
    *,
    label: str,
    uid: int,
    gid: int,
    mode: int = 0o700,
) -> bool:
    """Report whether one managed root has exact setup custody."""

    if not os.path.lexists(path):
        return False
    metadata = path.lstat()
    if (
        not stat.S_ISDIR(metadata.st_mode)
        or metadata.st_uid != uid
        or metadata.st_gid != gid
        or stat.S_IMODE(metadata.st_mode) != mode
    ):
        raise ServerSetupResetError(
            "reset_custody",
            f"managed {label} root custody does not match package-owned state",
        )
    return True


def _require_absent_or_owned_file(
    path: Path,
    *,
    label: str,
    uid: int,
    gid: int,
    mode: int,
) -> bool:
    if not os.path.lexists(path):
        return False
    metadata = path.lstat()
    if (
        not stat.S_ISREG(metadata.st_mode)
        or metadata.st_nlink != 1
        or metadata.st_uid != uid
        or metadata.st_gid != gid
        or stat.S_IMODE(metadata.st_mode) != mode
    ):
        raise ServerSetupResetError(
            "reset_custody",
            f"managed {label} custody does not match package-owned state",
        )
    return True


def _require_only_managed_secrets(secret_root: Path) -> None:
    """Refuse to remove a secret root holding anything this package did not create."""

    try:
        entries = sorted(entry.name for entry in os.scandir(secret_root))
    except OSError as exc:
        raise ServerSetupResetError("reset_custody", "managed secret root is not inspectable") from exc
    unexpected = [name for name in entries if name not in _MANAGED_SECRET_FILES]
    if unexpected:
        raise ServerSetupResetError(
            "reset_allowlist",
            "managed secret root holds state this package does not own; reset refuses to remove it",
        )


def _require_only_managed_setup_entries(entries: set[str]) -> None:
    unexpected = sorted(entries - _MANAGED_SETUP_ENTRIES)
    if unexpected:
        raise ServerSetupResetError(
            "reset_allowlist",
            "managed setup root holds state this package does not own; reset refuses to remove it",
        )


def _remove_tree(path: Path, *, label: str) -> None:
    try:
        shutil.rmtree(path)
    except OSError as exc:
        raise ServerSetupResetError("reset_failed", f"managed {label} root could not be removed") from exc


def _remove_file(path: Path, *, label: str) -> None:
    try:
        path.unlink(missing_ok=True)
    except OSError as exc:
        raise ServerSetupResetError("reset_failed", f"managed {label} could not be removed") from exc


def _acquire_idle_setup_lock(
    lock_path: Path,
    *,
    uid: int,
    gid: int,
) -> tuple[int, bool]:
    """Create/validate and lock package custody before any reset inventory."""

    try:
        import fcntl as posix_fcntl
    except ModuleNotFoundError as exc:  # pragma: no cover - POSIX-only profile
        raise ServerSetupResetError("unsupported_host", "server setup reset requires POSIX file locking") from exc
    try:
        _ensure_root_private_directory(
            lock_path.parent,
            uid=uid,
            gid=gid,
            label="setup_lock",
        )
    except ServerSetupError as exc:
        raise ServerSetupResetError(
            "reset_custody",
            "AgentNet setup lock root custody is unsafe",
        ) from exc
    existed_before = os.path.lexists(lock_path)
    try:
        descriptor = os.open(
            lock_path,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
            0o600,
        )
    except OSError as exc:
        raise ServerSetupResetError("reset_custody", "AgentNet setup lock custody is unsafe") from exc
    metadata = os.fstat(descriptor)
    if (
        not stat.S_ISREG(metadata.st_mode)
        or metadata.st_nlink != 1
        or metadata.st_uid != uid
        or metadata.st_gid != gid
        or stat.S_IMODE(metadata.st_mode) != 0o600
    ):
        os.close(descriptor)
        raise ServerSetupResetError(
            "reset_custody",
            "AgentNet setup lock custody conflicts with the fixed profile",
        )
    try:
        posix_fcntl.flock(descriptor, posix_fcntl.LOCK_EX | posix_fcntl.LOCK_NB)
    except BlockingIOError as exc:
        os.close(descriptor)
        raise ServerSetupResetError("setup_locked", "another AgentNet server setup is active") from exc
    except OSError as exc:
        os.close(descriptor)
        raise ServerSetupResetError("reset_custody", "AgentNet setup lock is not usable") from exc
    return descriptor, existed_before


def _run_systemctl_reset(systemctl_executable: Path, arguments: list[str]) -> int:
    try:
        completed = subprocess.run(
            [str(systemctl_executable), *arguments],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupResetError(
            "reset_service_stop",
            "managed AgentNet unit could not be stopped before reset",
        ) from exc
    return int(completed.returncode)


def _stop_managed_units(systemctl_executable: Path) -> list[str]:
    """Stop and disable managed units, then prove no managed process remains active."""

    stopped: list[str] = []
    for unit in (CORE_UNIT, APPROVAL_UNIT):
        _run_systemctl_reset(systemctl_executable, ["disable", "--now", unit])
        active_status = _run_systemctl_reset(systemctl_executable, ["is-active", "--quiet", unit])
        if active_status not in {3, 4}:
            raise ServerSetupResetError(
                "reset_service_stop",
                "managed AgentNet unit did not prove inactive before reset",
            )
        _run_systemctl_reset(systemctl_executable, ["reset-failed", unit])
        stopped.append(unit)
    return stopped


def reset_server_setup(
    *,
    layout: SetupLayout = SetupLayout(),
    retain_external_prerequisites: bool,
    _allow_test_layout: bool = False,
) -> dict[str, Any]:
    """Remove exactly the package-owned server-agent state and prove its absence.

    ``retain_external_prerequisites`` must be ``True``: this package installs no
    external prerequisite and therefore must never remove one.  The dedicated
    service identities are retained on purpose — deleting a system account whose
    UID may still own files elsewhere is destructive beyond what this package can
    prove it owns — and are reported honestly instead of being silently dropped.
    """

    if not retain_external_prerequisites:
        raise ServerSetupResetError(
            "external_prerequisite_ownership",
            "AgentNet does not own PostgreSQL, Node.js, uv, the reverse proxy, or TLS material and must not remove them",
        )
    if layout.root != Path("/") and not _allow_test_layout:
        raise ServerSetupResetError("test_layout", "reset requires the real host layout")
    if not _allow_test_layout and os.geteuid() != 0:
        raise ServerSetupResetError("privilege_required", "server setup reset requires root")

    unit_paths = {CORE_UNIT: layout.core_unit, APPROVAL_UNIT: layout.approval_unit}
    directory_roots = {
        "core_state": layout.host(CORE_DATA),
        "approval_state": layout.host(APPROVAL_DATA),
        "setup_runtime": layout.host(SETUP_RUNTIME_ROOT),
    }
    setup_files = {
        "setup_marker": layout.host(SETUP_MARKER),
        "upgrade_journal": layout.host(SETUP_UPGRADE_JOURNAL),
    }
    secret_root = layout.host(SECRET_ROOT)
    root_uid = 0 if layout.root == Path("/") else os.geteuid()
    root_gid = 0 if layout.root == Path("/") else os.getegid()
    lock_descriptor, lock_preexisted = _acquire_idle_setup_lock(
        layout.lock,
        uid=root_uid,
        gid=root_gid,
    )
    try:
        try:
            setup_entries = {
                entry.name for entry in os.scandir(layout.lock.parent)
            }
        except OSError as exc:
            raise ServerSetupResetError(
                "reset_custody",
                "AgentNet setup lock root is not inspectable",
            ) from exc
        _require_only_managed_setup_entries(setup_entries)
        setup_owned_state = bool(setup_entries - {layout.lock.name})
        state_exists = setup_owned_state or any(
            os.path.lexists(path)
            for path in (
                *unit_paths.values(),
                directory_roots["core_state"],
                directory_roots["approval_state"],
                secret_root,
            )
        )
        if state_exists and not lock_preexisted:
            raise ServerSetupResetError(
                "reset_custody",
                "managed state exists without a pre-existing package-owned setup lock",
            )
        if layout.root == Path("/"):
            directory_owners: dict[str, tuple[int, int]] = {}
            for label, account_name in (
                ("core_state", "agentnet"),
                ("approval_state", "agentnet-approval"),
            ):
                if not os.path.lexists(directory_roots[label]):
                    directory_owners[label] = (root_uid, root_gid)
                    continue
                try:
                    identity = pwd.getpwnam(account_name)
                except KeyError as exc:
                    raise ServerSetupResetError(
                        "reset_custody",
                        "managed state exists without its locked service identity",
                    ) from exc
                directory_owners[label] = (identity.pw_uid, identity.pw_gid)
            directory_owners["setup_runtime"] = (root_uid, root_gid)
        else:
            directory_owners = {
                label: (root_uid, root_gid) for label in directory_roots
            }

        # Inventory only after taking the setup lock.  This prevents a concurrent
        # setup from changing package custody between validation and deletion.
        present_units = {
            unit: path
            for unit, path in unit_paths.items()
            if _require_absent_or_owned_file(
                path,
                label=unit,
                uid=root_uid,
                gid=root_gid,
                mode=0o644,
            )
        }
        present_directories = {
            label: path
            for label, path in directory_roots.items()
            if _require_absent_or_owned_directory(
                path,
                label=label,
                uid=directory_owners[label][0],
                gid=directory_owners[label][1],
            )
        }
        present_setup_files = {
            label: path
            for label, path in setup_files.items()
            if _require_absent_or_owned_file(
                path,
                label=label,
                uid=root_uid,
                gid=root_gid,
                mode=0o600,
            )
        }
        secret_root_present = _require_absent_or_owned_directory(
            secret_root,
            label="secret",
            uid=root_uid,
            gid=root_gid,
        )
        if secret_root_present:
            _require_only_managed_secrets(secret_root)
        present = state_exists

        if present_directories and not shutil.rmtree.avoids_symlink_attacks:
            raise ServerSetupResetError(
                "unsupported_host",
                "server setup reset requires symlink-attack-resistant recursive removal",
            )

        stopped: list[str] = []
        systemctl_executable: Path | None = None
        if layout.root == Path("/"):
            systemctl_executable = _resolve_host_tool("systemctl")
            if present:
                stopped = _stop_managed_units(systemctl_executable)

        for unit, path in present_units.items():
            _remove_file(path, label=unit)
        for label, path in present_setup_files.items():
            _remove_file(path, label=label)
        if secret_root_present:
            for name in _MANAGED_SECRET_FILES:
                secret_path = secret_root / name
                if _require_absent_or_owned_file(
                    secret_path,
                    label=f"secret:{name}",
                    uid=root_uid,
                    gid=root_gid,
                    mode=0o600,
                ):
                    _remove_file(secret_path, label=f"secret:{name}")
            # rmdir, not rmtree: anything that appeared after the allowlist check
            # keeps the root and fails closed instead of being removed.
            try:
                secret_root.rmdir()
            except OSError as exc:
                raise ServerSetupResetError(
                    "reset_allowlist",
                    "managed secret root is not empty after removing package-owned secrets",
                ) from exc
        for label, path in present_directories.items():
            _remove_tree(path, label=label)
        if systemctl_executable is not None:
            # Always reload, including an exact retry after a lost/failing prior
            # response.  Unit files may already be absent while systemd still
            # holds their old fragments in memory.
            if _run_systemctl_reset(systemctl_executable, ["daemon-reload"]) != 0:
                raise ServerSetupResetError(
                    "reset_service_stop",
                    "systemd could not reload after managed unit removal",
                )
    finally:
        os.close(lock_descriptor)

    remaining = sorted(
        str(path)
        for path in (
            *unit_paths.values(),
            *directory_roots.values(),
            *setup_files.values(),
            secret_root,
        )
        if os.path.lexists(path)
    )
    if remaining:
        raise ServerSetupResetError("reset_incomplete", "package-owned server state is still present after reset")

    return {
        "schema": "agentnet.server-setup.reset-evidence.v1",
        "state": "reset" if present else "already_absent",
        "package_version": __version__,
        "external_prerequisites": "retained",
        "retained_external_prerequisites": list(_RETAINED_EXTERNAL_PREREQUISITES),
        "retained_service_identities": ["agentnet", "agentnet-approval"],
        "removed_units": sorted(present_units),
        "stopped_units": sorted(stopped),
        "absence_proven_paths": sorted(
            str(path)
            for path in (
                *unit_paths.values(),
                *directory_roots.values(),
                *setup_files.values(),
                secret_root,
            )
        ),
        "authority_granted": False,
        "identity_enrolled": False,
        "production_durability_proven": False,
        "next": "rerun agentnet server-agent setup --request ... to provision a fresh deployment",
    }


__all__ = ["ServerSetupResetError", "reset_server_setup"]
