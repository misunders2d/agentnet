"""Windows private-state DACL enforcement.

Imported lazily so Linux and macOS never require pywin32.  Files are created
inside a directory whose protected DACL is already restricted to the current
user, LocalSystem, and Administrators; the final object receives the same DACL.
"""

from __future__ import annotations

import os
import stat
from pathlib import Path

from agentnet.errors import AuthenticationError


_FILE_ALL_ACCESS = 0x001F01FF
_FILE_GENERIC_READ = 0x00120089

_DANGEROUS_ALLOW_SIDS = frozenset(
    {
        "S-1-1-0",       # Everyone
        "S-1-5-7",       # Anonymous
        "S-1-5-11",      # Authenticated Users
        "S-1-5-32-545",  # Builtin Users
    }
)


def _api():
    if os.name != "nt":
        raise AuthenticationError("Windows DACL enforcement requested on a non-Windows host")
    try:
        import win32api
        import win32con
        import win32security
    except ImportError as exc:  # pragma: no cover - Windows-only dependency gate
        raise AuthenticationError("pywin32 is required for Windows private-state enforcement") from exc
    return win32api, win32con, win32security


def _sid_text(sid) -> str:
    _win32api, _win32con, win32security = _api()
    return str(win32security.ConvertSidToStringSid(sid))


def current_user_sid():
    win32api, win32con, win32security = _api()
    token = win32security.OpenProcessToken(win32api.GetCurrentProcess(), win32con.TOKEN_QUERY)
    try:
        return win32security.GetTokenInformation(token, win32security.TokenUser)[0]
    finally:
        token.Close()


def _private_dacl(*, directory: bool):
    _win32api, win32con, win32security = _api()
    dacl = win32security.ACL()
    inheritance = 0
    if directory:
        inheritance = win32con.CONTAINER_INHERIT_ACE | win32con.OBJECT_INHERIT_ACE
    for sid in (
        current_user_sid(),
        win32security.ConvertStringSidToSid("S-1-5-18"),
        win32security.ConvertStringSidToSid("S-1-5-32-544"),
    ):
        dacl.AddAccessAllowedAceEx(
            win32security.ACL_REVISION_DS,
            inheritance,
            _FILE_ALL_ACCESS,
            sid,
        )
    return dacl


def _reject_reparse(path: Path) -> None:
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise AuthenticationError(f"Windows private path is unavailable: {path}") from exc
    attributes = getattr(metadata, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    if path.is_symlink() or attributes & reparse:
        raise AuthenticationError(f"Windows private path cannot be a reparse point: {path}")


def private_security_attributes(*, directory: bool = False):
    """Build non-inheritable SECURITY_ATTRIBUTES with a protected private DACL."""

    try:
        import pywintypes
    except ImportError as exc:  # pragma: no cover - Windows-only dependency gate
        raise AuthenticationError("pywin32 is required for Windows private-state enforcement") from exc
    sa = pywintypes.SECURITY_ATTRIBUTES()
    sa.bInheritHandle = 0
    sa.SetSecurityDescriptorOwner(current_user_sid(), 0)
    sa.SetSecurityDescriptorDacl(1, _private_dacl(directory=directory), 0)
    return sa


def _require_private_descriptor(owner, dacl, *, label: str) -> None:
    _win32api, win32con, win32security = _api()
    if dacl is None:
        raise AuthenticationError(f"Windows private DACL is null: {label}")
    current = _sid_text(current_user_sid())
    if _sid_text(owner) != current:
        raise AuthenticationError(f"Windows private owner is not the current user: {label}")
    current_allowed = False
    for index in range(dacl.GetAceCount()):
        header, mask, sid = dacl.GetAce(index)
        ace_type = int(header[0])
        sid_value = _sid_text(sid)
        if ace_type == win32security.ACCESS_ALLOWED_ACE_TYPE:
            if sid_value in _DANGEROUS_ALLOW_SIDS:
                raise AuthenticationError(f"Windows private DACL grants a broad principal: {label}")
            if sid_value == current and mask & _FILE_GENERIC_READ:
                current_allowed = True
    if not current_allowed:
        raise AuthenticationError(f"Windows private DACL does not grant the current user: {label}")


def require_private_kernel_handle(handle, *, label: str) -> None:
    """Verify owner and DACL on a named-pipe/job kernel object."""

    _win32api, _win32con, win32security = _api()
    try:
        descriptor = win32security.GetSecurityInfo(
            handle,
            win32security.SE_KERNEL_OBJECT,
            win32security.OWNER_SECURITY_INFORMATION
            | win32security.DACL_SECURITY_INFORMATION,
        )
        owner = descriptor.GetSecurityDescriptorOwner()
        dacl = descriptor.GetSecurityDescriptorDacl()
    except Exception as exc:
        raise AuthenticationError(f"Windows private kernel security descriptor is unavailable: {label}") from exc
    _require_private_descriptor(owner, dacl, label=label)


def apply_private_dacl(path: Path, *, directory: bool) -> None:
    _win32api, _win32con, win32security = _api()
    _reject_reparse(path)
    flags = (
        win32security.DACL_SECURITY_INFORMATION
        | win32security.PROTECTED_DACL_SECURITY_INFORMATION
    )
    try:
        win32security.SetNamedSecurityInfo(
            str(path),
            win32security.SE_FILE_OBJECT,
            flags,
            None,
            None,
            _private_dacl(directory=directory),
            None,
        )
    except Exception as exc:
        raise AuthenticationError(f"Windows private DACL could not be applied: {path}") from exc


def require_private_path(path: Path, *, directory: bool) -> os.stat_result:
    _win32api, win32con, win32security = _api()
    _reject_reparse(path)
    try:
        metadata = path.stat()
        descriptor = win32security.GetNamedSecurityInfo(
            str(path),
            win32security.SE_FILE_OBJECT,
            win32security.OWNER_SECURITY_INFORMATION
            | win32security.DACL_SECURITY_INFORMATION,
        )
        owner = descriptor.GetSecurityDescriptorOwner()
        dacl = descriptor.GetSecurityDescriptorDacl()
    except Exception as exc:
        raise AuthenticationError(f"Windows private security descriptor is unavailable: {path}") from exc
    if directory != stat.S_ISDIR(metadata.st_mode) or dacl is None:
        raise AuthenticationError(f"Windows private path type or DACL is invalid: {path}")
    _require_private_descriptor(owner, dacl, label=str(path))
    return metadata


def ensure_private_directory(path: Path) -> None:
    path = path.absolute()
    existing = next((candidate for candidate in (path, *path.parents) if candidate.exists()), None)
    if existing is not None:
        for candidate in (existing, *existing.parents):
            _reject_reparse(candidate)
    path.mkdir(parents=True, exist_ok=True)
    apply_private_dacl(path, directory=True)
    require_private_path(path, directory=True)


def write_private_file(path: Path, content: bytes, *, force: bool = False) -> None:
    path = path.absolute()
    ensure_private_directory(path.parent)
    if path.exists():
        if not force:
            raise FileExistsError(path)
        require_private_path(path, directory=False)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{os.urandom(12).hex()}.tmp")
    descriptor: int | None = None
    installed = False
    try:
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_BINARY)
        view = memoryview(content)
        while view:
            written = os.write(descriptor, view)
            if written <= 0:
                raise OSError("Windows private-state write made no progress")
            view = view[written:]
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None
        apply_private_dacl(temporary, directory=False)
        require_private_path(temporary, directory=False)
        os.replace(temporary, path)
        installed = True
        apply_private_dacl(path, directory=False)
        require_private_path(path, directory=False)
    finally:
        if descriptor is not None:
            os.close(descriptor)
        if not installed:
            temporary.unlink(missing_ok=True)


def read_private_file(path: Path, *, max_bytes: int = 65_536) -> bytes:
    path = path.absolute()
    before = require_private_path(path, directory=False)
    if before.st_size > max_bytes:
        raise AuthenticationError(f"Windows private file exceeds its bounded size: {path}")
    descriptor = os.open(path, os.O_RDONLY | os.O_BINARY)
    try:
        opened = os.fstat(descriptor)
        content = bytearray()
        while True:
            chunk = os.read(descriptor, min(16_384, max_bytes + 1 - len(content)))
            if not chunk:
                break
            content.extend(chunk)
            if len(content) > max_bytes:
                raise AuthenticationError(f"Windows private file exceeds its bounded size: {path}")
        after = os.fstat(descriptor)
    finally:
        os.close(descriptor)
    identity = lambda value: (
        value.st_dev,
        value.st_ino,
        value.st_size,
        value.st_mtime_ns,
        value.st_ctime_ns,
    )
    if identity(before) != identity(opened) or identity(opened) != identity(after):
        raise AuthenticationError(f"Windows private file changed while read: {path}")
    return bytes(content)


__all__ = [
    "apply_private_dacl",
    "current_user_sid",
    "private_security_attributes",
    "ensure_private_directory",
    "read_private_file",
    "require_private_kernel_handle",
    "require_private_path",
    "write_private_file",
]
