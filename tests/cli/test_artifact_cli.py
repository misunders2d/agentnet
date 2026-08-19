from __future__ import annotations

import hashlib
import json
import os
import stat
from argparse import Namespace
from pathlib import Path

import httpx
import pytest

from agentnet.cli import (
    _prepare_artifact_output,
    _read_artifact_file,
    _write_artifact_output,
    command_artifact_download,
    command_artifact_upload,
)
from agentnet.client import MAX_ARTIFACT_BYTES


def test_artifact_input_reader_preserves_exact_binary_bytes(tmp_path: Path) -> None:
    source = tmp_path / "payload.bin"
    content = b"\x00\xffbinary\ncontent"
    source.write_bytes(content)

    normalized, observed = _read_artifact_file(source)

    assert normalized == source.absolute()
    assert observed == content


@pytest.mark.parametrize("kind", ["symlink", "directory", "fifo", "oversize"])
def test_artifact_input_reader_rejects_unsafe_or_oversize_paths(
    tmp_path: Path,
    kind: str,
) -> None:
    source = tmp_path / "payload.bin"
    if kind == "symlink":
        target = tmp_path / "target.bin"
        target.write_bytes(b"secret")
        source.symlink_to(target)
    elif kind == "directory":
        source.mkdir()
    elif kind == "fifo":
        os.mkfifo(source)
    else:
        with source.open("wb") as stream:
            stream.truncate(MAX_ARTIFACT_BYTES + 1)

    with pytest.raises(SystemExit):
        _read_artifact_file(source)


def test_artifact_input_reader_fails_closed_without_no_follow(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    source = tmp_path / "payload.bin"
    source.write_bytes(b"content")
    monkeypatch.delattr(os, "O_NOFOLLOW")

    with pytest.raises(SystemExit, match="requires operating-system flag O_NOFOLLOW"):
        _read_artifact_file(source)


def test_artifact_input_reader_rejects_file_changed_during_read(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    source = tmp_path / "payload.bin"
    source.write_bytes(b"first")
    real_read = os.read
    changed = False

    def racing_read(descriptor: int, size: int) -> bytes:
        nonlocal changed
        chunk = real_read(descriptor, size)
        if chunk and not changed:
            changed = True
            with source.open("ab") as stream:
                stream.write(b"-changed")
        return chunk

    monkeypatch.setattr(os, "read", racing_read)
    with pytest.raises(SystemExit, match="changed while it was read"):
        _read_artifact_file(source)


def test_artifact_output_is_exclusive_private_and_exact(tmp_path: Path) -> None:
    output = tmp_path / "download.bin"
    normalized, name, directory = _prepare_artifact_output(output)
    try:
        _write_artifact_output(
            directory=directory,
            name=name,
            content=b"\x00download\xff",
        )
    finally:
        os.close(directory)

    assert normalized == output.absolute()
    assert output.read_bytes() == b"\x00download\xff"
    assert stat.S_IMODE(output.stat().st_mode) == 0o600
    with pytest.raises(SystemExit, match="refusing to overwrite"):
        _prepare_artifact_output(output)


def test_artifact_output_removes_its_partial_inode_on_write_failure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    output = tmp_path / "download.bin"
    _normalized, name, directory = _prepare_artifact_output(output)
    monkeypatch.setattr(os, "write", lambda _descriptor, _content: 0)
    try:
        with pytest.raises(SystemExit, match="could not be committed"):
            _write_artifact_output(
                directory=directory,
                name=name,
                content=b"partial",
            )
    finally:
        os.close(directory)

    assert not output.exists()


def test_artifact_output_rejects_shared_directory_and_symlink(
    tmp_path: Path,
) -> None:
    shared = tmp_path / "shared"
    shared.mkdir(mode=0o777)
    shared.chmod(0o777)
    with pytest.raises(SystemExit, match="not group/world writable"):
        _prepare_artifact_output(shared / "output.bin")

    target = tmp_path / "target.bin"
    target.write_bytes(b"existing")
    link = tmp_path / "output-link.bin"
    link.symlink_to(target)
    with pytest.raises(SystemExit, match="refusing to overwrite"):
        _prepare_artifact_output(link)


class _UploadClient:
    def __init__(self) -> None:
        self.calls: list[tuple[str, object]] = []
        self.closed = False

    def reserve_artifact(self, **kwargs: object) -> httpx.Response:
        self.calls.append(("reserve", kwargs))
        return httpx.Response(
            201,
            json={
                "reservation_id": "reservation-1",
                "object_key": "private-object-key",
                "state": "upload_reserved",
            },
        )

    def upload_artifact_bytes(self, **kwargs: object) -> httpx.Response:
        self.calls.append(("upload", kwargs))
        return httpx.Response(
            200,
            json={
                "version": "a" * 64,
                "object_key": "private-object-key",
                "state": "object_verified",
            },
        )

    def promote_artifact(self, **kwargs: object) -> httpx.Response:
        self.calls.append(("promote", kwargs))
        return httpx.Response(
            201,
            json={
                "artifact_id": "artifact-1",
                "state": "quarantined",
                "provenance": {"provenance_digest": "b" * 64},
            },
        )

    def close(self) -> None:
        self.closed = True


class _DownloadClient:
    def __init__(self, content: bytes) -> None:
        self.content = content
        self.calls: list[tuple[str, int]] = []
        self.closed = False

    def download_artifact(self, *, artifact_id: str, ttl_seconds: int) -> httpx.Response:
        self.calls.append((artifact_id, ttl_seconds))
        return httpx.Response(
            200,
            content=self.content,
            headers={"Content-Type": "application/octet-stream"},
        )

    def close(self) -> None:
        self.closed = True


def test_artifact_upload_command_stops_at_quarantine_and_redacts_private_storage(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    source = tmp_path / "payload.bin"
    content = b"\x00artifact\xff"
    source.write_bytes(content)
    client = _UploadClient()
    monkeypatch.setattr(
        "agentnet.cli.helpers._load_identity_client",
        lambda _path: (client, object(), object()),
    )
    args = Namespace(
        path=str(source),
        identity="identity.json",
        idempotency_key="artifact-command-key-0001",
        media_type="application/octet-stream",
        origin="operator-selected-input",
        classification="C1",
        ttl_seconds=3600,
        optional_attachment=False,
    )

    assert command_artifact_upload(args) == 0

    raw_output = capsys.readouterr().out
    output = json.loads(raw_output)
    assert output == {
        "artifact_id": "artifact-1",
        "classification": "C1",
        "media_type": "application/octet-stream",
        "plaintext_digest": hashlib.sha256(content).hexdigest(),
        "provenance": {"provenance_digest": "b" * 64},
        "released": False,
        "required_attachment": True,
        "reservation_id": "reservation-1",
        "scanner_state": "pending",
        "size": len(content),
        "state": "quarantined",
    }
    assert "private-object-key" not in raw_output
    assert [name for name, _kwargs in client.calls] == ["reserve", "upload", "promote"]
    assert client.calls[2][1]["provenance"] == {"origin": "operator-selected-input"}
    assert client.closed is True


def test_artifact_download_command_rejects_bad_content_type_without_writing(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class BadContentTypeClient(_DownloadClient):
        def download_artifact(
            self,
            *,
            artifact_id: str,
            ttl_seconds: int,
        ) -> httpx.Response:
            self.calls.append((artifact_id, ttl_seconds))
            return httpx.Response(
                200,
                content=b"not accepted",
                headers={"Content-Type": "text/plain"},
            )

    client = BadContentTypeClient(b"")
    monkeypatch.setattr(
        "agentnet.cli.helpers._load_identity_client",
        lambda _path: (client, object(), object()),
    )
    output_path = tmp_path / "download.bin"
    args = Namespace(
        artifact_id="artifact-1",
        output=str(output_path),
        identity="identity.json",
        ttl_seconds=60,
    )

    with pytest.raises(SystemExit, match="invalid content type"):
        command_artifact_download(args)

    assert not output_path.exists()
    assert client.closed is True


def test_artifact_download_command_writes_private_file_without_capability_output(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    content = b"\x00released\xff"
    client = _DownloadClient(content)
    monkeypatch.setattr(
        "agentnet.cli.helpers._load_identity_client",
        lambda _path: (client, object(), object()),
    )
    output_path = tmp_path / "download.bin"
    args = Namespace(
        artifact_id="artifact-1",
        output=str(output_path),
        identity="identity.json",
        ttl_seconds=60,
    )

    assert command_artifact_download(args) == 0

    output = json.loads(capsys.readouterr().out)
    assert output["artifact_id"] == "artifact-1"
    assert output["output"] == str(output_path.absolute())
    assert output["size"] == len(content)
    assert output_path.read_bytes() == content
    assert stat.S_IMODE(output_path.stat().st_mode) == 0o600
    assert client.calls == [("artifact-1", 60)]
    assert client.closed is True
