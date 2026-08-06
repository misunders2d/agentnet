from __future__ import annotations

import hashlib
import os
from pathlib import Path

import pytest

from agentnet.artifacts.local_destination import SafeDownloadDestination
from agentnet.errors import ConflictError, ValidationError


def _writer(root: Path) -> SafeDownloadDestination:
    root.mkdir(mode=0o700)
    root.chmod(0o700)
    return SafeDownloadDestination(root)


def test_safe_destination_materializes_verified_bytes_inside_approved_root(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    destination = root / "reports" / "result.txt"
    (root / "reports").mkdir(parents=True, mode=0o700)
    root.chmod(0o700)
    (root / "reports").chmod(0o700)
    writer = SafeDownloadDestination(root)
    content = b"verified artifact bytes"

    written = writer.write(
        destination=destination,
        content=content,
        expected_digest=hashlib.sha256(content).hexdigest(),
    )

    assert written == destination
    assert written.read_bytes() == content
    assert written.stat().st_mode & 0o777 == 0o600


@pytest.mark.parametrize(
    "relative",
    (
        Path("../escape.txt"),
        Path("reports/../../escape.txt"),
        Path("."),
    ),
)
def test_safe_destination_rejects_traversal_and_root_escape(
    tmp_path: Path,
    relative: Path,
) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)

    with pytest.raises(ValidationError, match="destination"):
        writer.write(
            destination=root / relative,
            content=b"no",
            expected_digest=hashlib.sha256(b"no").hexdigest(),
        )

    assert not (tmp_path / "escape.txt").exists()


def test_safe_destination_rejects_symlink_root(tmp_path: Path) -> None:
    real = tmp_path / "real"
    real.mkdir(mode=0o700)
    root = tmp_path / "downloads"
    root.symlink_to(real, target_is_directory=True)

    with pytest.raises(ValidationError, match="root"):
        SafeDownloadDestination(root)


def test_safe_destination_rejects_symlink_in_root_ancestor(tmp_path: Path) -> None:
    real_parent = tmp_path / "real-parent"
    real_parent.mkdir(mode=0o700)
    (real_parent / "downloads").mkdir(mode=0o700)
    linked_parent = tmp_path / "linked-parent"
    linked_parent.symlink_to(real_parent, target_is_directory=True)

    with pytest.raises(ValidationError, match="root"):
        SafeDownloadDestination(linked_parent / "downloads")


def test_safe_destination_rejects_symlink_parent_and_target(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)
    outside = tmp_path / "outside"
    outside.mkdir(mode=0o700)
    (root / "linked").symlink_to(outside, target_is_directory=True)

    with pytest.raises(ValidationError, match="destination"):
        writer.write(
            destination=root / "linked" / "escaped.txt",
            content=b"no",
            expected_digest=hashlib.sha256(b"no").hexdigest(),
        )

    target = root / "target.txt"
    target.symlink_to(outside / "target.txt")
    with pytest.raises(ConflictError, match="exists"):
        writer.write(
            destination=target,
            content=b"no",
            expected_digest=hashlib.sha256(b"no").hexdigest(),
        )
    assert not (outside / "target.txt").exists()


def test_safe_destination_detects_approved_root_replacement(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)
    original = tmp_path / "original-downloads"
    root.rename(original)
    root.mkdir(mode=0o700)

    with pytest.raises(ValidationError, match="root"):
        writer.write(
            destination=root / "result.txt",
            content=b"no",
            expected_digest=hashlib.sha256(b"no").hexdigest(),
        )

    assert not (root / "result.txt").exists()
    assert not (original / "result.txt").exists()


def test_safe_destination_never_overwrites_an_existing_entry(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)
    destination = root / "result.txt"
    destination.write_bytes(b"existing")
    destination.chmod(0o600)

    with pytest.raises(ConflictError, match="exists"):
        writer.write(
            destination=destination,
            content=b"replacement",
            expected_digest=hashlib.sha256(b"replacement").hexdigest(),
        )

    assert destination.read_bytes() == b"existing"


@pytest.mark.parametrize("entry_kind", ("directory", "fifo"))
def test_safe_destination_rejects_existing_non_regular_entries(
    tmp_path: Path,
    entry_kind: str,
) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)
    destination = root / "result"
    if entry_kind == "directory":
        destination.mkdir(mode=0o700)
    else:
        os.mkfifo(destination, mode=0o600)

    with pytest.raises(ConflictError, match="exists"):
        writer.write(
            destination=destination,
            content=b"replacement",
            expected_digest=hashlib.sha256(b"replacement").hexdigest(),
        )


def test_safe_destination_digest_mismatch_leaves_no_file_or_temporary(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)
    destination = root / "result.txt"

    with pytest.raises(ValidationError, match="digest"):
        writer.write(
            destination=destination,
            content=b"actual",
            expected_digest=hashlib.sha256(b"other").hexdigest(),
        )

    assert not destination.exists()
    assert tuple(root.iterdir()) == ()


def test_safe_destination_rejects_non_private_parent(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    writer = _writer(root)
    shared = root / "shared"
    shared.mkdir(mode=0o755)

    with pytest.raises(ValidationError, match="private"):
        writer.write(
            destination=shared / "result.txt",
            content=b"no",
            expected_digest=hashlib.sha256(b"no").hexdigest(),
        )


def test_safe_destination_rejects_oversized_content_before_writing(tmp_path: Path) -> None:
    root = tmp_path / "downloads"
    root.mkdir(mode=0o700)
    writer = SafeDownloadDestination(root, max_bytes=4)

    with pytest.raises(ValidationError, match="size"):
        writer.write(
            destination=root / "result.txt",
            content=b"12345",
            expected_digest=hashlib.sha256(b"12345").hexdigest(),
        )

    assert os.listdir(root) == []
