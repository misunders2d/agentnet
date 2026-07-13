"""Live recovery/integrity checks for the self-hosted artifact filesystem."""

from __future__ import annotations

import hashlib
import os
import re
import stat
import time
from pathlib import Path
from typing import Any

from agentnet.security.signatures import canonical_digest
from agentnet.storage.backend import StoreBackend


_OBJECT_KEY = re.compile(r"^[a-f0-9]{32}$")
_VERSION = re.compile(r"^[a-f0-9]{64}$")


def _object_path(root: Path, *, namespace: str, object_key: str, version: str) -> Path:
    return root / namespace / object_key[:2] / object_key / version


def _verified_file_digest(path: Path) -> str | None:
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError:
        return None
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o077:
            return None
        digest = hashlib.sha256()
        for chunk in iter(lambda: os.read(descriptor, 1_048_576), b""):
            digest.update(chunk)
        after = os.fstat(descriptor)
        if (metadata.st_dev, metadata.st_ino, metadata.st_size) != (
            after.st_dev,
            after.st_ino,
            after.st_size,
        ):
            return None
        return digest.hexdigest()
    finally:
        os.close(descriptor)


def probe_filesystem_artifact_recovery(
    store: StoreBackend,
    root: Path,
    *,
    instance_id: str,
    scan_limit: int,
    record_observation: bool = False,
) -> dict[str, Any]:
    """Cross-check every authoritative manifest against immutable bytes.

    A bounded scan that cannot cover the full manifest set reports degraded;
    it never treats a partial sample as recovery evidence.
    """

    if scan_limit < 1:
        raise ValueError("artifact recovery scan limit must be positive")
    root_fingerprint = canonical_digest({"artifact_root": str(root.resolve(strict=False))})
    try:
        root_metadata = root.lstat()
        if not stat.S_ISDIR(root_metadata.st_mode) or root_metadata.st_mode & 0o077:
            raise PermissionError("artifact root must be an owner-only directory")
        count_row = store.fetch_one("SELECT COUNT(*) AS count FROM artifact_manifests")
        manifest_count = int(count_row["count"]) if count_row else 0
        rows = store.fetch_all(
            """SELECT artifact_id,object_key,object_version,ciphertext_digest,state
                 FROM artifact_manifests ORDER BY artifact_id LIMIT ?""",
            (scan_limit,),
        )
    except Exception as exc:
        return {
            "ready": False,
            "backend": "filesystem",
            "reason": type(exc).__name__,
            "root_fingerprint": root_fingerprint,
        }

    verified = 0
    missing = 0
    corrupt = 0
    for row in rows:
        object_key = row["object_key"]
        version = row["object_version"]
        if not _OBJECT_KEY.fullmatch(object_key) or not _VERSION.fullmatch(version):
            corrupt += 1
            continue
        if row["state"] == "released":
            candidates = (_object_path(root, namespace="released", object_key=object_key, version=version),)
        elif row["state"] == "release_pending":
            candidates = (
                _object_path(root, namespace="released", object_key=object_key, version=version),
                _object_path(root, namespace="quarantine", object_key=object_key, version=version),
            )
        else:
            candidates = (_object_path(root, namespace="quarantine", object_key=object_key, version=version),)
        digests = [_verified_file_digest(candidate) for candidate in candidates]
        actual = next((digest for digest in digests if digest is not None), None)
        if actual is None:
            missing += 1
        elif actual != version or actual != row["ciphertext_digest"]:
            corrupt += 1
        else:
            verified += 1

    complete = manifest_count <= scan_limit and len(rows) == manifest_count
    ready = complete and missing == 0 and corrupt == 0 and verified == manifest_count
    status = {
        "ready": ready,
        "backend": "durable_shared_filesystem",
        "manifest_count": manifest_count,
        "verified_count": verified,
        "missing_count": missing,
        "corrupt_count": corrupt,
        "complete_scan": complete,
        "root_fingerprint": root_fingerprint,
        "ha_claimed": False,
    }
    if record_observation and store.backend_name == "postgresql":
        now = int(time.time())
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO artifact_recovery_observations(
                       instance_id,checked_at,manifest_count,verified_count,missing_count,
                       corrupt_count,root_fingerprint,status
                   ) VALUES(?,?,?,?,?,?,?,?)
                   ON CONFLICT(instance_id) DO UPDATE SET
                       checked_at=excluded.checked_at,
                       manifest_count=excluded.manifest_count,
                       verified_count=excluded.verified_count,
                       missing_count=excluded.missing_count,
                       corrupt_count=excluded.corrupt_count,
                       root_fingerprint=excluded.root_fingerprint,
                       status=excluded.status""",
                (
                    instance_id,
                    now,
                    manifest_count,
                    verified,
                    missing,
                    corrupt,
                    root_fingerprint,
                    "ready" if ready else "degraded",
                ),
            )
    return status


__all__ = ["probe_filesystem_artifact_recovery"]
