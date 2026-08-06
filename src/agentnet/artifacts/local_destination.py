"""No-follow, no-overwrite materialization inside one approved local root."""

from __future__ import annotations

import errno
import hashlib
import os
import secrets
import stat
from pathlib import Path

from agentnet.errors import ConflictError, ValidationError


_SHA256 = frozenset("0123456789abcdef")
_DEFAULT_MAX_BYTES = 16_777_216


class SafeDownloadDestination:
    """Publish verified bytes beneath one owner-private directory.

    The root's device and inode are pinned when the helper is created. Every
    operation reopens it without following links and walks each destination
    parent by directory descriptor, so path replacement cannot redirect a
    write. Publication uses a same-directory hard-link followed by unlink of
    the private temporary name; that gives atomic visibility and an atomic
    no-overwrite decision on platforms supported by AgentNet.
    """

    @staticmethod
    def _open_directory_no_symlinks(path: Path) -> int:
        flags = (
            os.O_RDONLY
            | getattr(os, "O_DIRECTORY", 0)
            | getattr(os, "O_NOFOLLOW", 0)
        )
        if path.anchor != os.path.sep:
            raise ValidationError("approved download root must be an absolute local path")
        descriptor = os.open(path.anchor, flags)
        try:
            for component in path.parts[1:]:
                child = os.open(component, flags, dir_fd=descriptor)
                os.close(descriptor)
                descriptor = child
            return descriptor
        except Exception:
            os.close(descriptor)
            raise

    def __init__(self, root: Path, *, max_bytes: int = _DEFAULT_MAX_BYTES) -> None:
        if type(max_bytes) is not int or not 0 <= max_bytes <= _DEFAULT_MAX_BYTES:
            raise ValidationError("download size boundary is invalid")
        if not isinstance(root, Path):
            raise ValidationError("download root must be a filesystem path")
        absolute = Path(os.path.abspath(os.fspath(root)))
        try:
            descriptor = self._open_directory_no_symlinks(absolute)
        except OSError as exc:
            raise ValidationError("approved download root is unavailable or unsafe") from exc
        try:
            root_stat = os.fstat(descriptor)
            self._require_private_directory(root_stat, label="approved download root")
        finally:
            os.close(descriptor)
        self.root = absolute
        self.max_bytes = max_bytes
        self._root_identity = (root_stat.st_dev, root_stat.st_ino)

    @staticmethod
    def _require_private_directory(value: os.stat_result, *, label: str) -> None:
        if not stat.S_ISDIR(value.st_mode):
            raise ValidationError(f"{label} must be a real directory")
        if hasattr(os, "geteuid") and value.st_uid != os.geteuid():
            raise ValidationError(f"{label} must be owned by the current user")
        mode = stat.S_IMODE(value.st_mode)
        if mode & 0o077 or mode & 0o700 != 0o700:
            raise ValidationError(f"{label} must be owner-private and owner-writable")

    def _relative_destination(self, destination: Path) -> Path:
        if not isinstance(destination, Path):
            raise ValidationError("download destination must be a filesystem path")
        if destination.is_absolute():
            try:
                relative = destination.relative_to(self.root)
            except ValueError as exc:
                raise ValidationError("download destination escapes the approved root") from exc
        else:
            relative = destination
        parts = relative.parts
        if not parts or any(part in {"", ".", ".."} for part in parts):
            raise ValidationError("download destination contains unsafe traversal")
        if len(parts[-1]) > 255 or any(ord(character) < 0x20 for character in parts[-1]):
            raise ValidationError("download destination name is invalid")
        return Path(*parts)

    def _open_path(self, relative: Path) -> list[int]:
        flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
        descriptors: list[int] = []
        try:
            root_fd = self._open_directory_no_symlinks(self.root)
            descriptors.append(root_fd)
            opened_root = os.fstat(root_fd)
            if (opened_root.st_dev, opened_root.st_ino) != self._root_identity:
                raise ValidationError("approved download root was replaced")
            self._require_private_directory(opened_root, label="approved download root")
            current = root_fd
            for component in relative.parts[:-1]:
                try:
                    current = os.open(component, flags, dir_fd=current)
                except OSError as exc:
                    raise ValidationError("download destination parent is unsafe") from exc
                descriptors.append(current)
                self._require_private_directory(
                    os.fstat(current), label="download destination parent"
                )
            return descriptors
        except Exception:
            for descriptor in reversed(descriptors):
                os.close(descriptor)
            raise

    def _path_still_names_parent(self, relative: Path, expected: os.stat_result) -> None:
        descriptors = self._open_path(relative)
        try:
            current = os.fstat(descriptors[-1])
            if (current.st_dev, current.st_ino) != (expected.st_dev, expected.st_ino):
                raise ValidationError("download destination parent was replaced")
        finally:
            for descriptor in reversed(descriptors):
                os.close(descriptor)

    @staticmethod
    def _entry_exists(parent_fd: int, name: str) -> bool:
        try:
            os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
        except FileNotFoundError:
            return False
        except OSError as exc:
            raise ValidationError("download destination cannot be inspected safely") from exc
        return True

    @staticmethod
    def _remove_exact_temporary(
        parent_fd: int,
        name: str,
        identity: tuple[int, int] | None,
    ) -> None:
        if identity is None:
            return
        try:
            current = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
        except FileNotFoundError:
            return
        except OSError:
            return
        if (current.st_dev, current.st_ino) != identity or not stat.S_ISREG(current.st_mode):
            return
        try:
            os.unlink(name, dir_fd=parent_fd)
        except OSError:
            return

    def verify_existing(
        self,
        *,
        destination: Path,
        expected_digest: str,
        expected_size: int,
    ) -> Path | None:
        """Return an exact prior materialization, or ``None`` when absent."""

        if (
            not isinstance(expected_digest, str)
            or len(expected_digest) != 64
            or any(character not in _SHA256 for character in expected_digest)
            or type(expected_size) is not int
            or not 0 <= expected_size <= self.max_bytes
        ):
            raise ValidationError("download verification boundary is invalid")
        relative = self._relative_destination(destination)
        descriptors = self._open_path(relative)
        parent_fd = descriptors[-1]
        parent_stat = os.fstat(parent_fd)
        descriptor: int | None = None
        try:
            if not self._entry_exists(parent_fd, relative.name):
                return None
            flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
            try:
                descriptor = os.open(relative.name, flags, dir_fd=parent_fd)
            except OSError as exc:
                raise ConflictError("recorded download destination is unsafe") from exc
            before = os.fstat(descriptor)
            if (
                not stat.S_ISREG(before.st_mode)
                or before.st_nlink != 1
                or before.st_size != expected_size
                or (hasattr(os, "geteuid") and before.st_uid != os.geteuid())
                or stat.S_IMODE(before.st_mode) != 0o600
            ):
                raise ConflictError("recorded download destination is not the verified file")
            digest = hashlib.sha256()
            remaining = expected_size
            while remaining:
                block = os.read(descriptor, min(65_536, remaining))
                if not block:
                    raise ConflictError("recorded download destination ended unexpectedly")
                digest.update(block)
                remaining -= len(block)
            if os.read(descriptor, 1):
                raise ConflictError("recorded download destination exceeds its manifest")
            after = os.fstat(descriptor)
            if (
                (after.st_dev, after.st_ino) != (before.st_dev, before.st_ino)
                or after.st_size != before.st_size
                or after.st_mtime_ns != before.st_mtime_ns
                or after.st_ctime_ns != before.st_ctime_ns
                or not secrets.compare_digest(digest.hexdigest(), expected_digest)
            ):
                raise ConflictError("recorded download destination failed integrity verification")
            self._path_still_names_parent(relative, parent_stat)
            named = os.stat(relative.name, dir_fd=parent_fd, follow_symlinks=False)
            if (named.st_dev, named.st_ino) != (before.st_dev, before.st_ino):
                raise ConflictError("recorded download destination was replaced")
            return self.root / relative
        finally:
            if descriptor is not None:
                os.close(descriptor)
            for parent_descriptor in reversed(descriptors):
                os.close(parent_descriptor)

    def write(self, *, destination: Path, content: bytes, expected_digest: str) -> Path:
        if not isinstance(content, bytes):
            raise ValidationError("download content must be exact bytes")
        if len(content) > self.max_bytes:
            raise ValidationError("download content size exceeds the approved boundary")
        if (
            not isinstance(expected_digest, str)
            or len(expected_digest) != 64
            or any(character not in _SHA256 for character in expected_digest)
        ):
            raise ValidationError("download expected digest is invalid")
        actual_digest = hashlib.sha256(content).hexdigest()
        if not secrets.compare_digest(actual_digest, expected_digest):
            raise ValidationError("download content digest mismatch")

        relative = self._relative_destination(destination)
        descriptors = self._open_path(relative)
        parent_fd = descriptors[-1]
        parent_stat = os.fstat(parent_fd)
        target_name = relative.name
        temporary_name = f".agentnet-{secrets.token_hex(16)}.tmp"
        temporary_identity: tuple[int, int] | None = None
        temporary_fd: int | None = None
        try:
            if self._entry_exists(parent_fd, target_name):
                raise ConflictError("download destination already exists")
            flags = os.O_RDWR | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
            try:
                temporary_fd = os.open(temporary_name, flags, 0o600, dir_fd=parent_fd)
            except OSError as exc:
                raise ConflictError("download temporary destination is unavailable") from exc
            os.fchmod(temporary_fd, 0o600)
            created = os.fstat(temporary_fd)
            if not stat.S_ISREG(created.st_mode) or created.st_nlink != 1:
                raise ValidationError("download temporary destination is unsafe")
            temporary_identity = (created.st_dev, created.st_ino)

            remaining = memoryview(content)
            while remaining:
                written = os.write(temporary_fd, remaining)
                if written <= 0:
                    raise OSError("download temporary write made no progress")
                remaining = remaining[written:]
            os.fsync(temporary_fd)
            after_write = os.fstat(temporary_fd)
            if (
                (after_write.st_dev, after_write.st_ino) != temporary_identity
                or after_write.st_size != len(content)
                or not stat.S_ISREG(after_write.st_mode)
            ):
                raise ValidationError("download temporary destination changed during write")
            os.lseek(temporary_fd, 0, os.SEEK_SET)
            digest = hashlib.sha256()
            remaining_size = len(content)
            while remaining_size:
                block = os.read(temporary_fd, min(65_536, remaining_size))
                if not block:
                    raise ValidationError("download temporary destination ended unexpectedly")
                digest.update(block)
                remaining_size -= len(block)
            if not secrets.compare_digest(digest.hexdigest(), expected_digest):
                raise ValidationError("download temporary destination digest mismatch")
            os.close(temporary_fd)
            temporary_fd = None

            self._path_still_names_parent(relative, parent_stat)
            if self._entry_exists(parent_fd, target_name):
                raise ConflictError("download destination already exists")
            try:
                os.link(
                    temporary_name,
                    target_name,
                    src_dir_fd=parent_fd,
                    dst_dir_fd=parent_fd,
                    follow_symlinks=False,
                )
            except FileExistsError as exc:
                raise ConflictError("download destination already exists") from exc
            except OSError as exc:
                if exc.errno == errno.EEXIST:
                    raise ConflictError("download destination already exists") from exc
                raise ValidationError("download destination cannot be published safely") from exc
            os.fsync(parent_fd)
            self._remove_exact_temporary(parent_fd, temporary_name, temporary_identity)
            if self._entry_exists(parent_fd, temporary_name):
                raise ValidationError("download temporary destination cleanup failed")
            os.fsync(parent_fd)
            self._path_still_names_parent(relative, parent_stat)
            final = os.stat(target_name, dir_fd=parent_fd, follow_symlinks=False)
            if (
                (final.st_dev, final.st_ino) != temporary_identity
                or not stat.S_ISREG(final.st_mode)
                or final.st_nlink != 1
                or final.st_size != len(content)
                or stat.S_IMODE(final.st_mode) != 0o600
            ):
                raise ValidationError("published download destination is not the verified file")
            self._path_still_names_parent(relative, parent_stat)
            return self.root / relative
        finally:
            if temporary_fd is not None:
                os.close(temporary_fd)
            self._remove_exact_temporary(parent_fd, temporary_name, temporary_identity)
            for descriptor in reversed(descriptors):
                os.close(descriptor)


__all__ = ["SafeDownloadDestination"]
