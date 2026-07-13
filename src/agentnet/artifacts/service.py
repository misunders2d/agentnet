"""Provider-neutral local artifact implementation with staged acceptance."""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import stat
import time
from collections.abc import Callable, Mapping
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from uuid import uuid4

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from agentnet.artifacts.scanner import (
    ArtifactDerivationV1,
    ArtifactManifestProvenanceV1,
    ArtifactProvenanceV1,
    ArtifactScanAttestationV1,
    LocalPrefilter,
    ScannerTrustPolicy,
)
from agentnet.errors import AuthorizationError, ConflictError, IdempotencyConflict, ValidationError
from agentnet.authorization.policy import validate_actor_state
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.protocol.models import (
    Classification,
    EventEnvelope,
    ReleasedArtifactBinding,
)
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ProvenanceObjectType,
    ProvenanceOrigin,
    ProvenanceService,
    SinkSet,
)
from agentnet.security.signatures import canonical_digest, canonical_json, verify_signature
from agentnet.storage.sqlite import SQLiteStore


SAFE_KEY = re.compile(r"^[a-f0-9]{32}$")
SHA256_DIGEST = re.compile(r"^[a-f0-9]{64}$")
CANONICAL_MEDIA_TYPE = re.compile(
    r"^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$"
)
MAX_ARTIFACT_BYTES = 16_777_216


class FilesystemArtifactStore:
    """Immutable, fsync-backed self-hosted filesystem object store.

    This survives process/container restart on the mounted filesystem.  It does
    not claim host-loss replication or backup/restore evidence.
    """

    def __init__(self, root: Path, key_path: Path) -> None:
        self.root = root
        self.quarantine = root / "quarantine"
        self.released = root / "released"
        for directory in (self.root, self.quarantine, self.released):
            directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        if not key_path.exists():
            key_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            descriptor = os.open(key_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            try:
                os.write(descriptor, os.urandom(32))
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        if key_path.stat().st_mode & 0o077:
            raise PermissionError("artifact key must be owner-only")
        key = key_path.read_bytes()
        if len(key) != 32:
            raise ValidationError("artifact key must contain exactly 32 bytes")
        self._cipher = AESGCM(key)

    def _path(self, namespace: str, object_key: str, version: str) -> Path:
        if namespace not in {"quarantine", "released"}:
            raise ValidationError("invalid artifact namespace")
        if not SAFE_KEY.fullmatch(object_key) or not re.fullmatch(r"[a-f0-9]{64}", version):
            raise ValidationError("invalid immutable artifact address")
        base = self.quarantine if namespace == "quarantine" else self.released
        return base / object_key[:2] / object_key / version

    @staticmethod
    def _fsync_directory_chain(path: Path, *, stop: Path) -> None:
        current = path
        while True:
            descriptor = os.open(current, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
            if current == stop:
                return
            if stop not in current.parents:
                raise ValidationError("artifact directory escaped its durability root")
            current = current.parent

    def put_quarantine_bytes(self, object_key: str, plaintext: bytes, *, expected_digest: str) -> dict[str, Any]:
        if hashlib.sha256(plaintext).hexdigest() != expected_digest:
            raise ValidationError("artifact plaintext digest mismatch")
        nonce = os.urandom(12)
        ciphertext = nonce + self._cipher.encrypt(nonce, plaintext, f"artifact:{object_key}".encode("ascii"))
        version = hashlib.sha256(ciphertext).hexdigest()
        target = self._path("quarantine", object_key, version)
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        try:
            descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
        except FileExistsError:
            existing = self._read_exact(target)
            if not secrets.compare_digest(hashlib.sha256(existing).hexdigest(), version):
                raise ConflictError("immutable artifact version collision")
        else:
            try:
                remaining = memoryview(ciphertext)
                while remaining:
                    written = os.write(descriptor, remaining)
                    if written <= 0:
                        raise OSError("artifact write made no progress")
                    remaining = remaining[written:]
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
            self._fsync_directory_chain(target.parent, stop=self.root)
        return {"object_key": object_key, "version": version, "ciphertext_digest": version, "size": len(plaintext)}

    def _read_exact(self, path: Path) -> bytes:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        try:
            stat_before = os.fstat(descriptor)
            chunks: list[bytes] = []
            while True:
                chunk = os.read(descriptor, 1_048_576)
                if not chunk:
                    break
                chunks.append(chunk)
            stat_after = os.fstat(descriptor)
            if (stat_before.st_ino, stat_before.st_dev, stat_before.st_size) != (
                stat_after.st_ino,
                stat_after.st_dev,
                stat_after.st_size,
            ):
                raise ConflictError("artifact changed during same-descriptor read")
            return b"".join(chunks)
        finally:
            os.close(descriptor)

    def verify_quarantine(self, object_key: str, version: str) -> dict[str, Any]:
        ciphertext = self._read_exact(self._path("quarantine", object_key, version))
        actual = hashlib.sha256(ciphertext).hexdigest()
        if not secrets.compare_digest(actual, version):
            raise ConflictError("artifact ciphertext version mismatch")
        return {"ciphertext_digest": actual, "ciphertext_size": len(ciphertext)}

    def promote(self, object_key: str, version: str) -> None:
        source = self._path("quarantine", object_key, version)
        target = self._path("released", object_key, version)
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        if target.exists():
            if self._read_exact(target) != self._read_exact(source):
                raise ConflictError("released version differs from quarantine")
            return
        os.link(source, target, follow_symlinks=False)
        self._fsync_directory_chain(target.parent, stop=self.root)

    def read_plaintext(self, object_key: str, version: str, *, released: bool) -> bytes:
        namespace = "released" if released else "quarantine"
        ciphertext = self._read_exact(self._path(namespace, object_key, version))
        if not secrets.compare_digest(hashlib.sha256(ciphertext).hexdigest(), version):
            raise ConflictError("artifact ciphertext failed integrity check")
        nonce, body = ciphertext[:12], ciphertext[12:]
        try:
            return self._cipher.decrypt(nonce, body, f"artifact:{object_key}".encode("ascii"))
        except Exception as exc:
            raise ConflictError("artifact decryption authentication failed") from exc

    def delete_version(self, object_key: str, version: str) -> dict[str, int]:
        """Idempotently unlink an exact immutable version from both namespaces."""

        if not SAFE_KEY.fullmatch(object_key) or not SHA256_DIGEST.fullmatch(version):
            raise ValidationError("invalid immutable artifact address")
        deleted = 0
        for namespace in ("released", "quarantine"):
            base = self.released if namespace == "released" else self.quarantine
            descriptor: int | None = None
            try:
                descriptor = os.open(
                    base,
                    os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                )
                for component in (object_key[:2], object_key):
                    child = os.open(
                        component,
                        os.O_RDONLY
                        | getattr(os, "O_DIRECTORY", 0)
                        | getattr(os, "O_NOFOLLOW", 0),
                        dir_fd=descriptor,
                    )
                    os.close(descriptor)
                    descriptor = child
                info = os.stat(version, dir_fd=descriptor, follow_symlinks=False)
            except FileNotFoundError:
                continue
            finally:
                # The final leaf descriptor remains open for the guarded unlink
                # below, so only close here when traversal failed.
                if descriptor is not None and "info" not in locals():
                    os.close(descriptor)
                    descriptor = None
            try:
                if not stat.S_ISREG(info.st_mode):
                    raise ConflictError("artifact deletion target is not a regular immutable object")
                os.unlink(version, dir_fd=descriptor)
                os.fsync(descriptor)
                deleted += 1
            finally:
                if descriptor is not None:
                    os.close(descriptor)
                # Do not let one namespace's local variable satisfy the next
                # traversal's failure path.
                del info
        return {"deleted_links": deleted}

    def delete_object_versions(self, object_key: str) -> dict[str, int]:
        """Delete every immutable version in one reservation-private namespace.

        Reservation object keys are random and never shared, so this is the
        crash-recovery path for a write that reached disk before its version
        could be committed to the reservation row. Unexpected directory
        entries fail closed and leave quota charged.
        """

        if not SAFE_KEY.fullmatch(object_key):
            raise ValidationError("invalid immutable artifact address")
        versions: set[str] = set()
        for base in (self.quarantine, self.released):
            descriptor: int | None = None
            try:
                descriptor = os.open(
                    base / object_key[:2] / object_key,
                    os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                )
                for name in os.listdir(descriptor):
                    info = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
                    if not SHA256_DIGEST.fullmatch(name) or not stat.S_ISREG(info.st_mode):
                        raise ConflictError("artifact reservation contains an unexpected immutable object")
                    versions.add(name)
            except FileNotFoundError:
                continue
            finally:
                if descriptor is not None:
                    os.close(descriptor)
        deleted = 0
        for version in sorted(versions):
            deleted += self.delete_version(object_key, version)["deleted_links"]
        return {"deleted_links": deleted, "deleted_versions": len(versions)}


class ArtifactService:
    def __init__(
        self,
        store: SQLiteStore,
        objects: FilesystemArtifactStore,
        *,
        trusted_scanner_keys: Mapping[str, str] | None = None,
        scanner_policy: ScannerTrustPolicy | None = None,
        local_prefilter: LocalPrefilter | None = None,
        operations_policy: OperationsPolicy | None = None,
        outage_gate: OutageGate | None = None,
        provenance: ProvenanceService | None = None,
        clock: Callable[[], int] | None = None,
    ) -> None:
        self.store = store
        self.objects = objects
        self.trusted_scanner_keys = dict(trusted_scanner_keys or {})
        self.scanner_policy = scanner_policy or ScannerTrustPolicy()
        self.local_prefilter = local_prefilter or LocalPrefilter()
        self.operations_policy = operations_policy or OperationsPolicy()
        self.outage_gate = outage_gate
        self.provenance = provenance or ProvenanceService(store)
        if self.provenance.store is not store:
            raise ValueError("artifact and provenance services must share one transactional store")
        self.clock = clock or (lambda: int(time.time()))

    @staticmethod
    def _parse_stored_provenance(value: object) -> ArtifactManifestProvenanceV1:
        try:
            raw = str(value)
            parsed = ArtifactManifestProvenanceV1.parse_storage(raw)
        except Exception as exc:
            raise ConflictError("artifact manifest provenance is invalid") from exc
        if canonical_json(parsed.model_dump(mode="json")).decode("utf-8") != raw:
            raise ConflictError("artifact manifest provenance is not canonical")
        return parsed

    def _require_manifest_provenance(
        self,
        row: Any,
        *,
        connection: Any | None = None,
    ) -> ArtifactManifestProvenanceV1:
        """Resolve one manifest's mandatory ledger reference fail closed."""

        try:
            artifact_id = str(row["artifact_id"])
            domain_id = str(row["domain_id"])
            classification = Classification(str(row["classification"]))
            encrypted_digest = str(row["plaintext_digest_encrypted"])
            provenance_json = row["provenance_json"]
        except Exception as exc:
            raise ConflictError("artifact manifest lacks mandatory provenance fields") from exc
        parsed = self._parse_stored_provenance(provenance_json)
        reference = parsed.ledger_reference
        if reference.object_id != artifact_id:
            raise ConflictError("artifact manifest provenance names another artifact")
        try:
            plaintext_digest = self.store.cipher.decrypt_json(
                encrypted_digest,
                purpose=f"artifact-digest:{artifact_id}",
            )
        except Exception as exc:
            raise ConflictError("artifact manifest digest authentication failed") from exc
        if not isinstance(plaintext_digest, str) or not SHA256_DIGEST.fullmatch(plaintext_digest):
            raise ConflictError("artifact manifest plaintext digest is invalid")
        if connection is None:
            domain = self.store.fetch_one(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (domain_id,),
            )
        else:
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (domain_id,),
            ).fetchone()
        if domain is None:
            raise AuthorizationError("artifact provenance domain is unavailable")
        expected_policy_revision = int(domain["policy_revision"])
        if reference.policy_revision != expected_policy_revision:
            raise AuthorizationError("artifact provenance policy revision is stale")
        if connection is None:
            self.provenance.require_reference(
                reference,
                expected_domain_id=domain_id,
                expected_content_digest=plaintext_digest,
                expected_object_type=ProvenanceObjectType.ARTIFACT,
                expected_classification=classification,
                required_sinks=(),
                expected_policy_revision=expected_policy_revision,
            )
        else:
            self.provenance.require_reference_in_transaction(
                connection,
                reference,
                expected_domain_id=domain_id,
                expected_content_digest=plaintext_digest,
                expected_object_type=ProvenanceObjectType.ARTIFACT,
                expected_classification=classification,
                required_sinks=(),
                expected_policy_revision=expected_policy_revision,
            )
        return parsed

    def _scanner_key(self, scanner_id: str, key_epoch: int) -> str:
        key = self.trusted_scanner_keys.get(f"{scanner_id}:{key_epoch}")
        if key is None and key_epoch == 1:
            key = self.trusted_scanner_keys.get(scanner_id)
        if key is None:
            raise AuthorizationError("scanner key epoch is not currently trusted")
        return key

    def _validate_attestation_shape_and_time(
        self,
        attestation: object,
    ) -> ArtifactScanAttestationV1:
        parsed = ArtifactScanAttestationV1.parse_boundary(attestation)
        issued_at = parsed.issued_at
        expires_at = parsed.expires_at
        now = self.clock()
        if issued_at > now + self.scanner_policy.allowed_future_skew_seconds:
            raise AuthorizationError("scan attestation was issued in the future")
        if expires_at <= issued_at or now >= expires_at:
            raise AuthorizationError("scan attestation is expired")
        if now - issued_at > self.scanner_policy.max_attestation_age_seconds:
            raise AuthorizationError("scan attestation exceeds the current maximum age")
        self.scanner_policy.require_profile(parsed)
        return parsed

    def _decode_scan_attestation(self, artifact_id: str, stored: str) -> dict[str, Any]:
        try:
            decoded = self.store.cipher.decrypt_json(stored, purpose=f"artifact-scan:{artifact_id}")
        except Exception as exc:
            raise AuthorizationError("scan attestation storage authentication failed") from exc
        try:
            parsed = ArtifactScanAttestationV1.parse_boundary(decoded)
        except ValidationError as exc:
            raise AuthorizationError("scan attestation storage is invalid") from exc
        return parsed.model_dump(mode="json")

    def _require_fresh_scan(self, artifact_id: str) -> dict[str, Any]:
        row = self.store.fetch_one(
            """SELECT m.*,r.expected_digest
                 FROM artifact_manifests m
                 JOIN artifact_reservations r ON r.reservation_id=m.reservation_id
                WHERE m.artifact_id=?""",
            (artifact_id,),
        )
        if row is None or row["scanner_attestation_json"] is None:
            raise AuthorizationError("current scanner evidence is absent")
        self._require_manifest_provenance(row)
        attestation = self._decode_scan_attestation(artifact_id, row["scanner_attestation_json"])
        parsed = self._validate_attestation_shape_and_time(attestation)
        if attestation["artifact_id"] != artifact_id or attestation["result"] != "allow":
            raise AuthorizationError("scanner evidence does not authorize release")
        expected = {
            "classification": row["classification"],
            "ciphertext_digest": row["ciphertext_digest"],
            "object_key": row["object_key"],
            "object_version": row["object_version"],
            "plaintext_digest": row["expected_digest"],
        }
        if any(attestation.get(key) != value for key, value in expected.items()):
            raise AuthorizationError("scanner evidence no longer binds the exact artifact")
        domain = self.store.fetch_one(
            "SELECT policy_revision FROM domains WHERE domain_id=?",
            (row["domain_id"],),
        )
        if domain is None or int(domain["policy_revision"]) != int(attestation["policy_revision"]):
            raise AuthorizationError("scanner evidence policy revision is stale")
        scanner_key = self._scanner_key(parsed.scanner_id, parsed.scanner_key_epoch)
        signed_fields = parsed.signed_fields()
        verify_signature(scanner_key, "agentnet.artifact.attestation.v1", signed_fields, str(attestation["signature"]))
        verified = self.objects.verify_quarantine(row["object_key"], row["object_version"])
        if verified["ciphertext_digest"] != attestation["ciphertext_digest"]:
            raise ConflictError("scanner evidence ciphertext is no longer current")
        plaintext = self.objects.read_plaintext(row["object_key"], row["object_version"], released=False)
        if hashlib.sha256(plaintext).hexdigest() != attestation["plaintext_digest"]:
            raise ConflictError("scanner evidence plaintext is no longer current")
        return attestation

    def _hold_for_scan_failure(self, artifact_id: str, reason: str) -> None:
        with self.store.transaction() as connection:
            connection.execute(
                "UPDATE artifact_manifests SET state='held' WHERE artifact_id=? AND state IN ('scan_passed','release_pending','released')",
                (artifact_id,),
            )
            connection.execute(
                "UPDATE artifact_release_outbox SET last_error=?,updated_at=? WHERE artifact_id=? AND state='pending'",
                (reason, self.clock(), artifact_id),
            )
            self.store.append_audit(
                connection,
                {"action": "artifact.scan_held", "artifact_id": artifact_id, "reason": reason},
            )

    def _require_current_decision(
        self,
        connection: Any,
        *,
        decision_id: str,
        actor: VerifiedActor,
        action: str,
        resource_id: str,
        expected_context: Mapping[str, Any] | None = None,
    ) -> Any:
        decision = connection.execute(
            "SELECT * FROM policy_decisions WHERE decision_id=? AND allowed=1",
            (decision_id,),
        ).fetchone()
        if (
            decision is None
            or decision["action"] != action
            or decision["actor_json"] != canonical_json(actor.audit_view()).decode("utf-8")
            or json.loads(decision["resource_json"]) != {"id": resource_id}
        ):
            raise AuthorizationError("exact artifact policy decision is absent")
        decision_context = json.loads(decision["context_json"])
        if expected_context is not None and decision_context.get("request") != dict(expected_context):
            raise AuthorizationError("artifact decision context does not match exact operation")
        decision_time = int(decision["occurred_at"])
        now = int(time.time())
        if decision_time > now + 60 or decision_time < now - 300:
            raise AuthorizationError("artifact policy decision is stale")
        denial, current_revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(decision["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None or current_revision != int(decision["policy_revision"]):
            raise AuthorizationError("artifact actor or policy revision is no longer current")
        return decision

    def _artifact_limit(self, scope_type: str) -> int:
        if scope_type == "actor":
            return self.operations_policy.per_actor_artifact_bytes
        if scope_type == "domain":
            return self.operations_policy.per_domain_artifact_bytes
        raise ValueError("invalid artifact quota scope")

    def _charge_artifact_bytes_in_transaction(
        self,
        connection: Any,
        *,
        reservation_id: str,
        domain_id: str,
        actor_id: str,
        amount: int,
        now: int,
    ) -> None:
        for scope_type, scope_id in (("actor", actor_id), ("domain", domain_id)):
            limit = self._artifact_limit(scope_type)
            if amount > limit:
                raise AuthorizationError("artifact byte quota exceeded")
            updated = connection.execute(
                """INSERT INTO artifact_byte_accounts(
                       scope_type,scope_id,used_bytes,limit_bytes,updated_at
                   ) VALUES(?,?,?,?,?)
                   ON CONFLICT(scope_type,scope_id) DO UPDATE SET
                       used_bytes=artifact_byte_accounts.used_bytes+excluded.used_bytes,
                       limit_bytes=excluded.limit_bytes,
                       updated_at=excluded.updated_at
                    WHERE artifact_byte_accounts.used_bytes
                          <= excluded.limit_bytes-excluded.used_bytes""",
                (scope_type, scope_id, amount, limit, now),
            )
            if updated.rowcount != 1:
                # One conditional UPSERT is the cross-process fence on both
                # SQLite and PostgreSQL.  Do not disclose which scope fired or
                # its current usage to an authenticated caller.
                raise AuthorizationError("artifact byte quota exceeded")
        connection.execute(
            """INSERT INTO artifact_byte_charges(
                   reservation_id,domain_id,actor_id,charged_bytes,state,created_at,updated_at
               ) VALUES(?,?,?,?,'charged',?,?)""",
            (reservation_id, domain_id, actor_id, amount, now, now),
        )

    @staticmethod
    def _begin_artifact_byte_release_in_transaction(
        connection: Any,
        *,
        reservation_id: str,
        reason: str,
        now: int,
    ) -> Any:
        charge = connection.execute(
            "SELECT * FROM artifact_byte_charges WHERE reservation_id=?",
            (reservation_id,),
        ).fetchone()
        if charge is None:
            raise ConflictError("artifact byte charge is absent")
        if charge["state"] == "released":
            return charge
        if charge["state"] == "release_pending":
            if charge["release_reason"] != reason:
                raise ConflictError("artifact byte release reason changed")
            return charge
        updated = connection.execute(
            """UPDATE artifact_byte_charges
                  SET state='release_pending',release_reason=?,updated_at=?
                WHERE reservation_id=? AND state='charged'""",
            (reason, now, reservation_id),
        )
        if updated.rowcount != 1:
            raise ConflictError("artifact byte release raced with another lifecycle mutation")
        return connection.execute(
            "SELECT * FROM artifact_byte_charges WHERE reservation_id=?",
            (reservation_id,),
        ).fetchone()

    @staticmethod
    def _finalize_artifact_byte_release_in_transaction(
        connection: Any,
        *,
        reservation_id: str,
        now: int,
    ) -> bool:
        charge = connection.execute(
            "SELECT * FROM artifact_byte_charges WHERE reservation_id=?",
            (reservation_id,),
        ).fetchone()
        if charge is None:
            raise ConflictError("artifact byte charge is absent")
        if charge["state"] == "released":
            return False
        if charge["state"] != "release_pending":
            raise ConflictError("artifact byte charge is not pending release")
        amount = int(charge["charged_bytes"])
        for scope_type, scope_id in (
            ("actor", charge["actor_id"]),
            ("domain", charge["domain_id"]),
        ):
            updated = connection.execute(
                """UPDATE artifact_byte_accounts
                      SET used_bytes=used_bytes-?,updated_at=?
                    WHERE scope_type=? AND scope_id=? AND used_bytes>=?""",
                (amount, now, scope_type, scope_id, amount),
            )
            if updated.rowcount != 1:
                raise ConflictError("artifact byte account cannot release the exact charge")
        updated = connection.execute(
            """UPDATE artifact_byte_charges
                  SET state='released',released_at=?,updated_at=?
                WHERE reservation_id=? AND state='release_pending'""",
            (now, now, reservation_id),
        )
        if updated.rowcount != 1:
            raise ConflictError("artifact byte charge release lost its exact fence")
        return True

    def reconcile_quota_accounting(self) -> dict[str, int]:
        """Rebuild cumulative counters from the exact charge ledger atomically."""

        now = self.clock()
        with self.store.transaction() as connection:
            connection.execute("UPDATE artifact_byte_accounts SET used_bytes=0,updated_at=?", (now,))
            rows = connection.execute(
                """SELECT domain_id,actor_id,SUM(charged_bytes) AS used_bytes
                     FROM artifact_byte_charges
                    WHERE state IN ('charged','release_pending')
                    GROUP BY domain_id,actor_id"""
            ).fetchall()
            actor_count = 0
            domains: dict[str, int] = {}
            for row in rows:
                used = int(row["used_bytes"])
                connection.execute(
                    """INSERT INTO artifact_byte_accounts(
                           scope_type,scope_id,used_bytes,limit_bytes,updated_at
                       ) VALUES('actor',?,?,?,?)
                       ON CONFLICT(scope_type,scope_id) DO UPDATE SET
                           used_bytes=excluded.used_bytes,
                           limit_bytes=excluded.limit_bytes,
                           updated_at=excluded.updated_at""",
                    (row["actor_id"], used, self.operations_policy.per_actor_artifact_bytes, now),
                )
                actor_count += 1
                domains[row["domain_id"]] = domains.get(row["domain_id"], 0) + used
            for domain_id, used in domains.items():
                connection.execute(
                    """INSERT INTO artifact_byte_accounts(
                           scope_type,scope_id,used_bytes,limit_bytes,updated_at
                       ) VALUES('domain',?,?,?,?)
                       ON CONFLICT(scope_type,scope_id) DO UPDATE SET
                           used_bytes=excluded.used_bytes,
                           limit_bytes=excluded.limit_bytes,
                           updated_at=excluded.updated_at""",
                    (domain_id, used, self.operations_policy.per_domain_artifact_bytes, now),
                )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.quota_reconciled",
                    "active_actor_accounts": actor_count,
                    "active_domain_accounts": len(domains),
                },
            )
        return {"actor_accounts": actor_count, "domain_accounts": len(domains)}

    def reserve(
        self,
        *,
        actor: VerifiedActor,
        idempotency_key: str,
        expected_digest: str,
        expected_size: int,
        media_type: str,
        classification: Classification,
        required_attachment: bool,
        policy_decision_id: str,
        ttl_seconds: int = 3600,
    ) -> dict[str, Any]:
        if self.outage_gate is not None:
            self.outage_gate.require_low_risk_continuity()
        if not SHA256_DIGEST.fullmatch(expected_digest):
            raise ValidationError("artifact expected digest must be a lowercase SHA-256 value")
        if type(expected_size) is not int or not 0 <= expected_size <= MAX_ARTIFACT_BYTES:
            raise ValidationError("artifact expected size is outside the supported boundary")
        if not CANONICAL_MEDIA_TYPE.fullmatch(media_type):
            raise ValidationError("artifact media type must be one canonical lowercase type/subtype")
        if type(ttl_seconds) is not int or not 30 <= ttl_seconds <= 86_400:
            raise ValidationError("artifact reservation lifetime is outside the supported boundary")
        actor_id = actor.positive_authority_id
        if actor_id is None or actor.harness_id is None:
            raise AuthorizationError("artifacts require a verified human or host guest plus harness")
        # Expired reservations in this exact authority scope are reclaimed
        # before evaluating cumulative capacity, so an idle process does not
        # require a restart merely to make its own expired quota reusable.
        self.recover_expired_reservations(
            limit=100,
            domain_id=actor.domain_id,
            actor_id=actor_id,
        )
        request = {
            "actor": actor.audit_view(),
            "classification": classification.value,
            "expected_digest": expected_digest,
            "expected_size": expected_size,
            "media_type": media_type,
            "required_attachment": required_attachment,
        }
        request_digest = canonical_digest(request)
        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        now = self.clock()
        with self.store.transaction() as connection:
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action="artifact.upload.reserve",
                resource_id="artifact:new",
                expected_context=request,
            )
            existing = connection.execute(
                "SELECT * FROM artifact_reservations WHERE domain_id=? AND actor_id=? AND idempotency_key=?",
                (actor.domain_id, actor_id, idempotency_key),
            ).fetchone()
            if existing:
                if existing["request_digest"] != request_digest:
                    raise IdempotencyConflict("artifact idempotency key has a different request digest")
                return dict(existing) | {"duplicate": True}
            reservation_id = str(uuid4())
            object_key = secrets.token_hex(16)
            connection.execute(
                """INSERT INTO artifact_reservations(
                    reservation_id,domain_id,actor_id,actor_json,idempotency_key,request_digest,object_key,
                    expected_digest,expected_size,media_type,classification,required_attachment,state,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    reservation_id,
                    actor.domain_id,
                    actor_id,
                    actor_json,
                    idempotency_key,
                    request_digest,
                    object_key,
                    expected_digest,
                    expected_size,
                    media_type,
                    classification.value,
                    int(required_attachment),
                    "upload_reserved",
                    now + ttl_seconds,
                ),
            )
            self._charge_artifact_bytes_in_transaction(
                connection,
                reservation_id=reservation_id,
                domain_id=actor.domain_id,
                actor_id=actor_id,
                amount=expected_size,
                now=now,
            )
            self.store.append_audit(
                connection,
                {"action": "artifact.reserve", "actor": actor.audit_view(), "request_digest": request_digest, "reservation_id": reservation_id},
            )
        return {
            "reservation_id": reservation_id,
            "object_key": object_key,
            "request_digest": request_digest,
            "state": "upload_reserved",
            "expires_at": now + ttl_seconds,
            "duplicate": False,
            "media_type": media_type,
            "classification": classification.value,
        }

    def abort_reservation(
        self,
        reservation_id: str,
        *,
        actor: VerifiedActor,
        policy_decision_id: str,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        """Abort an unpromoted reservation without releasing quota before bytes."""

        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        now = self.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM artifact_reservations WHERE reservation_id=?",
                (reservation_id,),
            ).fetchone()
            if row is None or row["actor_json"] != actor_json:
                raise AuthorizationError("artifact reservation is not visible to this actor")
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action="artifact.upload.abort",
                resource_id=reservation_id,
                expected_context={"request_digest": row["request_digest"]},
            )
            charge = connection.execute(
                "SELECT * FROM artifact_byte_charges WHERE reservation_id=?",
                (reservation_id,),
            ).fetchone()
            if row["state"] in {"aborted", "expired", "prefilter_denied"}:
                if charge is None or charge["state"] != "released":
                    raise ConflictError("terminal artifact reservation retained a byte charge")
                return {
                    "reservation_id": reservation_id,
                    "state": row["state"],
                    "duplicate": True,
                }
            if row["state"] == "manifest_committed":
                raise ConflictError("promoted artifacts require revision-fenced deletion")
            if row["state"] not in {"upload_reserved", "object_verified", "abort_pending"}:
                raise ConflictError("artifact reservation is not abortable")
            self._begin_artifact_byte_release_in_transaction(
                connection,
                reservation_id=reservation_id,
                reason="aborted",
                now=now,
            )
            connection.execute(
                "UPDATE artifact_reservations SET state='abort_pending' WHERE reservation_id=?",
                (reservation_id,),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.reservation_abort_pending",
                    "actor": actor.audit_view(),
                    "reservation_id": reservation_id,
                    "policy_decision_id": policy_decision_id,
                },
            )
            if phase_hook is not None:
                phase_hook("after_reservation_abort_staged")
        return self.process_reservation_release(reservation_id, phase_hook=phase_hook)

    def _stage_expired_reservation(self, reservation_id: str) -> None:
        now = self.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM artifact_reservations WHERE reservation_id=?",
                (reservation_id,),
            ).fetchone()
            if row is None:
                return
            if row["state"] == "abort_pending":
                return
            if row["state"] not in {"upload_reserved", "object_verified", "prefilter_denied"}:
                return
            if row["state"] != "prefilter_denied" and int(row["expires_at"]) > now:
                return
            reason = "prefilter_denied" if row["state"] == "prefilter_denied" else "expired"
            self._begin_artifact_byte_release_in_transaction(
                connection,
                reservation_id=reservation_id,
                reason=reason,
                now=now,
            )
            connection.execute(
                "UPDATE artifact_reservations SET state='abort_pending' WHERE reservation_id=?",
                (reservation_id,),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.reservation_release_pending",
                    "reason": reason,
                    "reservation_id": reservation_id,
                },
            )

    def process_reservation_release(
        self,
        reservation_id: str,
        *,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        row = self.store.fetch_one(
            """SELECT r.*,c.state AS charge_state,c.release_reason
                 FROM artifact_reservations r
                 JOIN artifact_byte_charges c ON c.reservation_id=r.reservation_id
                WHERE r.reservation_id=?""",
            (reservation_id,),
        )
        if row is None:
            raise ConflictError("artifact reservation release state is absent")
        if row["state"] in {"aborted", "expired", "prefilter_denied"} and row["charge_state"] == "released":
            return {"reservation_id": reservation_id, "state": row["state"], "duplicate": True}
        if row["state"] != "abort_pending" or row["charge_state"] != "release_pending":
            raise ConflictError("artifact reservation release is not current")
        self.objects.delete_object_versions(row["object_key"])
        if phase_hook is not None:
            phase_hook("after_reservation_objects_removed")
        completed_at = self.clock()
        with self.store.transaction() as connection:
            current = connection.execute(
                """SELECT r.*,c.state AS charge_state,c.release_reason
                     FROM artifact_reservations r
                     JOIN artifact_byte_charges c ON c.reservation_id=r.reservation_id
                    WHERE r.reservation_id=?""",
                (reservation_id,),
            ).fetchone()
            if current is None:
                raise ConflictError("artifact reservation release state disappeared")
            if current["state"] in {"aborted", "expired", "prefilter_denied"} and current["charge_state"] == "released":
                return {
                    "reservation_id": reservation_id,
                    "state": current["state"],
                    "duplicate": True,
                }
            manifest = connection.execute(
                "SELECT 1 FROM artifact_manifests WHERE reservation_id=?",
                (reservation_id,),
            ).fetchone()
            if (
                current["state"] != "abort_pending"
                or current["charge_state"] != "release_pending"
                or manifest is not None
            ):
                raise ConflictError("artifact reservation release lost its exact lifecycle fence")
            released = self._finalize_artifact_byte_release_in_transaction(
                connection,
                reservation_id=reservation_id,
                now=completed_at,
            )
            terminal_state = current["release_reason"]
            connection.execute(
                """UPDATE artifact_reservations
                      SET state=?,object_version=NULL
                    WHERE reservation_id=? AND state='abort_pending'""",
                (terminal_state, reservation_id),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "artifact.reservation_released",
                    "reason": terminal_state,
                    "reservation_id": reservation_id,
                },
            )
            if phase_hook is not None:
                phase_hook("before_reservation_release_commit")
        return {
            "reservation_id": reservation_id,
            "state": terminal_state,
            "duplicate": not released,
            "audit_hash": audit_hash,
        }

    def recover_expired_reservations(
        self,
        *,
        limit: int = 100,
        domain_id: str | None = None,
        actor_id: str | None = None,
    ) -> list[dict[str, Any]]:
        if limit < 1 or limit > 1_000:
            raise ValidationError("artifact reservation recovery limit is invalid")
        now = self.clock()
        scope_clause = ""
        parameters: list[Any] = [now]
        if domain_id is not None:
            scope_clause += " AND domain_id=?"
            parameters.append(domain_id)
        if actor_id is not None:
            scope_clause += " AND actor_id=?"
            parameters.append(actor_id)
        parameters.append(limit)
        rows = self.store.fetch_all(
            """SELECT reservation_id FROM artifact_reservations
                WHERE (state='abort_pending'
                   OR state='prefilter_denied'
                   OR (state IN ('upload_reserved','object_verified') AND expires_at<=?))"""
            + scope_clause
            + " ORDER BY expires_at,reservation_id LIMIT ?",
            tuple(parameters),
        )
        recovered: list[dict[str, Any]] = []
        for row in rows:
            self._stage_expired_reservation(row["reservation_id"])
            recovered.append(self.process_reservation_release(row["reservation_id"]))
        return recovered

    def resolve_released_binding(self, artifact_id: str) -> ReleasedArtifactBinding:
        """Resolve and re-verify one exact released object version.

        A database state flag is not sufficient evidence.  Resolution first
        revalidates current scanner policy and then authenticates the released
        ciphertext/decryption, plaintext digest, and size before returning a
        binding suitable for an event envelope.
        """

        state = self.store.fetch_one(
            "SELECT state FROM artifact_manifests WHERE artifact_id=?",
            (artifact_id,),
        )
        if state is None or state["state"] != "released":
            raise AuthorizationError("artifact does not have a completed corporate release")
        try:
            self._require_fresh_scan(artifact_id)
        except Exception as exc:
            self._hold_for_scan_failure(artifact_id, type(exc).__name__)
            raise
        row = self.store.fetch_one(
            """SELECT m.artifact_id,m.domain_id,m.object_key,m.object_version,m.size,
                      m.media_type,m.classification,m.state,m.plaintext_digest_encrypted,
                      m.provenance_json,r.expected_digest,
                      o.intent_id,o.state AS release_state,o.completed_at
                 FROM artifact_manifests m
                 JOIN artifact_reservations r ON r.reservation_id=m.reservation_id
                 JOIN artifact_release_outbox o ON o.artifact_id=m.artifact_id
                WHERE m.artifact_id=?""",
            (artifact_id,),
        )
        if (
            row is None
            or row["state"] != "released"
            or row["release_state"] != "completed"
            or row["completed_at"] is None
        ):
            raise AuthorizationError("artifact does not have a completed corporate release")
        self._require_manifest_provenance(row)
        plaintext = self.objects.read_plaintext(
            row["object_key"],
            row["object_version"],
            released=True,
        )
        if len(plaintext) != int(row["size"]):
            raise ConflictError("released artifact size no longer matches its manifest")
        if not secrets.compare_digest(hashlib.sha256(plaintext).hexdigest(), row["expected_digest"]):
            raise ConflictError("released artifact digest no longer matches its reservation")
        return ReleasedArtifactBinding(
            artifact_id=row["artifact_id"],
            domain_id=row["domain_id"],
            object_version=row["object_version"],
            size=int(row["size"]),
            media_type=row["media_type"],
            classification=Classification(row["classification"]),
            release_intent_id=row["intent_id"],
            released_at=datetime.fromtimestamp(int(row["completed_at"]), UTC),
        )

    def require_released_binding(
        self,
        binding: ReleasedArtifactBinding,
        *,
        domain_id: str,
        event_classification: Classification,
    ) -> ReleasedArtifactBinding:
        """Fail closed unless every supplied field equals current manifest truth."""

        if binding.domain_id != domain_id:
            raise AuthorizationError("released artifact binding crossed the event trust domain")
        rank = {
            Classification.C0_PUBLIC: 0,
            Classification.C1_INTERNAL: 1,
            Classification.C2_RESTRICTED: 2,
            Classification.C3_SEALED: 3,
        }
        if rank[binding.classification] > rank[event_classification]:
            raise AuthorizationError("event classification is lower than its released artifact")
        current = self.resolve_released_binding(binding.artifact_id)
        if not secrets.compare_digest(
            canonical_json(current.model_dump(mode="json")),
            canonical_json(binding.model_dump(mode="json")),
        ):
            raise AuthorizationError("released artifact binding is stale or substituted")
        return current

    def require_event_artifacts(self, event: EventEnvelope) -> tuple[ReleasedArtifactBinding, ...]:
        """Validate all typed artifact bindings before mailbox acceptance."""

        return tuple(
            self.require_released_binding(
                binding,
                domain_id=event.domain_id,
                event_classification=event.classification,
            )
            for binding in event.released_artifacts
        )

    def upload(
        self,
        reservation_id: str,
        content: bytes,
        *,
        actor: VerifiedActor,
        policy_decision_id: str,
    ) -> dict[str, Any]:
        denied_reason: str | None = None
        with self.store.transaction() as connection:
            row = connection.execute("SELECT * FROM artifact_reservations WHERE reservation_id=?", (reservation_id,)).fetchone()
            if row is None or row["actor_json"] != canonical_json(actor.audit_view()).decode("utf-8"):
                raise AuthorizationError("artifact reservation is not visible to this actor")
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action="artifact.upload.bytes",
                resource_id=reservation_id,
                expected_context={"expected_digest": row["expected_digest"], "expected_size": row["expected_size"]},
            )
            if len(content) != row["expected_size"] or hashlib.sha256(content).hexdigest() != row["expected_digest"]:
                raise ValidationError("artifact bytes differ from the exact reservation")
            if row["state"] in {"object_verified", "manifest_committed"}:
                if not row["object_version"]:
                    raise ConflictError("verified artifact reservation lacks an immutable object version")
                verified = self.objects.verify_quarantine(row["object_key"], row["object_version"])
                return {
                    "object_key": row["object_key"],
                    "version": row["object_version"],
                    "size": row["expected_size"],
                    "reservation_id": reservation_id,
                    "state": row["state"],
                    "duplicate": True,
                } | verified
            if row["state"] != "upload_reserved":
                raise ConflictError("artifact reservation is not in an uploadable state")
            if row["expires_at"] <= self.clock():
                raise ConflictError("artifact reservation expired")
            prefilter = self.local_prefilter.scan(
                artifact_id=reservation_id,
                object_version=row["expected_digest"],
                content=content,
                media_type=row["media_type"],
            )
            if prefilter.result == "deny":
                denied_reason = prefilter.reason_code
                self._begin_artifact_byte_release_in_transaction(
                    connection,
                    reservation_id=reservation_id,
                    reason="prefilter_denied",
                    now=self.clock(),
                )
                connection.execute(
                    "UPDATE artifact_reservations SET state='abort_pending' WHERE reservation_id=?",
                    (reservation_id,),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "artifact.prefilter_denied",
                        "reason_code": denied_reason,
                        "reservation_id": reservation_id,
                        "rules_digest": prefilter.rules_digest,
                    },
                )
            else:
                result = self.objects.put_quarantine_bytes(row["object_key"], content, expected_digest=row["expected_digest"])
                verified = self.objects.verify_quarantine(row["object_key"], result["version"])
                connection.execute(
                    "UPDATE artifact_reservations SET state='object_verified',object_version=? WHERE reservation_id=?",
                    (result["version"], reservation_id),
                )
                self.store.append_audit(
                    connection,
                    {"action": "artifact.object_verified", "reservation_id": reservation_id, "version": result["version"]},
                )
        if denied_reason is not None:
            self.process_reservation_release(reservation_id)
            raise AuthorizationError(f"artifact content safety prefilter denied: {denied_reason}")
        return result | verified | {"reservation_id": reservation_id, "state": "object_verified"}

    def promote_manifest(
        self,
        *,
        reservation_id: str,
        object_version: str,
        provenance: object,
        derivation: object | None = None,
        actor: VerifiedActor,
        policy_decision_id: str,
    ) -> dict[str, Any]:
        parsed_provenance = ArtifactProvenanceV1.parse_boundary(provenance)
        parsed_derivation = (
            ArtifactDerivationV1.parse_boundary(derivation) if derivation is not None else None
        )
        if parsed_derivation is not None and (
            actor.harness_id is None
            or any(
                step.executor_harness_id != actor.harness_id
                for step in parsed_derivation.transformations
            )
        ):
            raise AuthorizationError(
                "artifact derivation executor is not the authenticated promoting harness"
            )
        derivation_digest = (
            canonical_digest(parsed_derivation.model_dump(mode="json"))
            if parsed_derivation is not None
            else None
        )
        verified = None
        artifact_id = str(uuid4())
        now = self.clock()
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM artifact_reservations WHERE reservation_id=?",
                (reservation_id,),
            ).fetchone()
            if row is None or row["state"] not in {"object_verified", "manifest_committed"}:
                raise ConflictError("artifact object is not verified")
            if row["state"] != "manifest_committed" and int(row["expires_at"]) <= now:
                raise ConflictError("artifact reservation expired")
            if row["object_version"] != object_version:
                raise ConflictError("manifest does not bind the reservation's immutable object version")
            if row["actor_json"] != canonical_json(actor.audit_view()).decode("utf-8"):
                raise AuthorizationError("artifact reservation is not visible to this actor")
            decision_context = {
                "object_version": object_version,
                "request_digest": row["request_digest"],
            }
            if derivation_digest is not None:
                decision_context["derivation_digest"] = derivation_digest
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action="artifact.manifest.promote",
                resource_id=reservation_id,
                expected_context=decision_context,
            )
            existing = connection.execute(
                "SELECT * FROM artifact_manifests WHERE reservation_id=?",
                (reservation_id,),
            ).fetchone()
            if existing:
                if existing["object_version"] != object_version:
                    raise ConflictError("reservation already promoted with another version")
                stored = self._require_manifest_provenance(existing, connection=connection)
                if stored.client_attribution != parsed_provenance:
                    raise IdempotencyConflict(
                        "reservation provenance attribution changed on exact replay"
                    )
                ledger_row = connection.execute(
                    "SELECT * FROM content_provenance WHERE provenance_digest=?",
                    (stored.ledger_reference.provenance_digest,),
                ).fetchone()
                if ledger_row is None:
                    raise ConflictError("artifact manifest provenance record is unavailable")
                ledger_record = self.provenance._record_from_row(ledger_row)
                expected_parent_digests = (
                    tuple(
                        reference.provenance_digest
                        for reference in parsed_derivation.parent_references
                    )
                    if parsed_derivation is not None
                    else ()
                )
                expected_steps = (
                    parsed_derivation.transformations if parsed_derivation is not None else ()
                )
                if (
                    ledger_record.parent_digests.digests != expected_parent_digests
                    or ledger_record.transformations.steps != expected_steps
                    or (
                        parsed_derivation is None
                        and ledger_record.origin.kind is not OriginKind.ARTIFACT
                    )
                    or (
                        parsed_derivation is not None
                        and ledger_record.origin.kind is not OriginKind.DERIVED
                    )
                ):
                    raise IdempotencyConflict(
                        "reservation artifact derivation changed on exact replay"
                    )
                return dict(existing) | {
                    "duplicate": True,
                    "provenance": stored.ledger_reference.model_dump(mode="json"),
                }
            verified = self.objects.verify_quarantine(row["object_key"], object_version)
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (row["domain_id"],),
            ).fetchone()
            if domain is None:
                raise AuthorizationError("artifact provenance domain is unavailable")
            provenance_time = datetime.fromtimestamp(now, UTC)
            if parsed_derivation is None:
                provenance_record = self.provenance.register_origin_in_transaction(
                    connection,
                    OriginRegistration(
                        object_type=ProvenanceObjectType.ARTIFACT,
                        object_id=artifact_id,
                        domain_id=str(row["domain_id"]),
                        origin=ProvenanceOrigin(
                            kind=OriginKind.ARTIFACT,
                            source_id=f"artifact-reservation:{reservation_id}",
                            source_digest=str(row["expected_digest"]),
                            harness_id=actor.harness_id,
                            observed_at=provenance_time,
                        ),
                        classification=Classification(str(row["classification"])),
                        allowed_sinks=SinkSet(sinks=()),
                        policy_revision=int(domain["policy_revision"]),
                        recorded_at=provenance_time,
                    ),
                    when=provenance_time,
                )
            else:
                parent_digests: list[str] = []
                for reference in parsed_derivation.parent_references:
                    parent = self.provenance.require_reference_in_transaction(
                        connection,
                        reference,
                        expected_domain_id=str(row["domain_id"]),
                        expected_content_digest=reference.content_digest,
                        expected_object_type=reference.object_type,
                        expected_classification=reference.classification,
                        required_sinks=(),
                        expected_policy_revision=int(domain["policy_revision"]),
                    )
                    parent_digests.append(parent.provenance_digest)
                provenance_record = self.provenance.record_tainted_derivation_in_transaction(
                    connection,
                    object_type=ProvenanceObjectType.ARTIFACT,
                    object_id=artifact_id,
                    domain_id=str(row["domain_id"]),
                    expected_previous_version=0,
                    parent_provenance_digests=tuple(parent_digests),
                    transformations=parsed_derivation.transformations,
                    output_digest=str(row["expected_digest"]),
                    classification=Classification(str(row["classification"])),
                    allowed_sinks=(),
                    policy_revision=int(domain["policy_revision"]),
                    recorded_at=provenance_time,
                    when=provenance_time,
                )
            stored_provenance = ArtifactManifestProvenanceV1(
                client_attribution=parsed_provenance,
                ledger_reference=provenance_record.reference(),
            )
            connection.execute(
                """INSERT INTO artifact_manifests(
                    artifact_id,reservation_id,domain_id,object_key,object_version,ciphertext_digest,
                    plaintext_digest_encrypted,size,media_type,classification,state,provenance_json,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    artifact_id,
                    reservation_id,
                    row["domain_id"],
                    row["object_key"],
                    object_version,
                    verified["ciphertext_digest"],
                    self.store.cipher.encrypt_json(row["expected_digest"], purpose=f"artifact-digest:{artifact_id}"),
                    row["expected_size"],
                    row["media_type"],
                    row["classification"],
                    "quarantined",
                    canonical_json(stored_provenance.model_dump(mode="json")).decode("utf-8"),
                    now,
                ),
            )
            connection.execute(
                "UPDATE artifact_reservations SET state='manifest_committed' WHERE reservation_id=?",
                (reservation_id,),
            )
            connection.execute(
                """INSERT INTO artifact_lifecycle(artifact_id,revision,status,updated_at)
                   VALUES(?,1,'active',?)""",
                (artifact_id, now),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "artifact.manifest_committed",
                    "artifact_id": artifact_id,
                    "object_version": object_version,
                    "provenance_digest": provenance_record.provenance_digest,
                    "authority_effect": "none",
                },
            )
        return {
            "artifact_id": artifact_id,
            "state": "quarantined",
            "audit_hash": audit_hash,
            "duplicate": False,
            "provenance": provenance_record.reference().model_dump(mode="json"),
        }

    def record_scan(self, artifact_id: str, attestation: object) -> dict[str, Any]:
        parsed = self._validate_attestation_shape_and_time(attestation)
        exact_attestation = parsed.model_dump(mode="json")
        if parsed.artifact_id != artifact_id:
            raise ValidationError("scan attestation artifact binding is invalid")
        scanner_key = self._scanner_key(parsed.scanner_id, parsed.scanner_key_epoch)
        signed_fields = parsed.signed_fields()
        verify_signature(
            scanner_key,
            "agentnet.artifact.attestation.v1",
            signed_fields,
            parsed.signature,
        )
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT m.*,r.expected_digest
                     FROM artifact_manifests m
                     JOIN artifact_reservations r ON r.reservation_id=m.reservation_id
                    WHERE m.artifact_id=?""",
                (artifact_id,),
            ).fetchone()
            domain = None if row is None else connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (row["domain_id"],),
            ).fetchone()
            if row is not None:
                self._require_manifest_provenance(row, connection=connection)
            expected = None if row is None else {
                "classification": row["classification"],
                "ciphertext_digest": row["ciphertext_digest"],
                "object_key": row["object_key"],
                "object_version": row["object_version"],
                "plaintext_digest": row["expected_digest"],
                "policy_revision": int(domain["policy_revision"]) if domain is not None else None,
            }
            if row is None or any(exact_attestation.get(key) != value for key, value in expected.items()):
                raise ConflictError("scan attestation does not bind the immutable object version")
            verified = self.objects.verify_quarantine(row["object_key"], row["object_version"])
            plaintext = self.objects.read_plaintext(row["object_key"], row["object_version"], released=False)
            if (
                verified["ciphertext_digest"] != parsed.ciphertext_digest
                or hashlib.sha256(plaintext).hexdigest() != parsed.plaintext_digest
            ):
                raise ConflictError("scan attestation does not bind current object bytes")
            if row["scanner_attestation_json"] is not None:
                existing_attestation = self._decode_scan_attestation(
                    artifact_id, row["scanner_attestation_json"]
                )
                if canonical_json(existing_attestation) != canonical_json(exact_attestation):
                    raise ConflictError("artifact scan attestation is immutable")
                return {"artifact_id": artifact_id, "state": row["state"], "duplicate": True}
            if row["state"] != "quarantined":
                raise ConflictError("artifact is not in the scannable quarantine state")
            canonical_attestation = self.store.cipher.encrypt_json(
                exact_attestation, purpose=f"artifact-scan:{artifact_id}"
            )
            state = "scan_passed" if parsed.result == "allow" else "held"
            connection.execute(
                "UPDATE artifact_manifests SET scanner_attestation_json=?,state=? WHERE artifact_id=?",
                (canonical_attestation, state, artifact_id),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "artifact.scan_recorded",
                    "artifact_id": artifact_id,
                    "attestation_digest": canonical_digest(exact_attestation),
                    "state": state,
                },
            )
        return {"artifact_id": artifact_id, "state": state, "audit_hash": audit_hash}

    def release(
        self,
        artifact_id: str,
        *,
        actor: VerifiedActor,
        policy_decision_id: str,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        if self.outage_gate is not None:
            self.outage_gate.require_privileged()
        """Authorize and durably stage release before touching released bytes."""

        try:
            self._require_fresh_scan(artifact_id)
        except Exception as exc:
            self._hold_for_scan_failure(artifact_id, type(exc).__name__)
            raise

        now = int(time.time())
        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        with self.store.transaction() as connection:
            row = connection.execute("SELECT * FROM artifact_manifests WHERE artifact_id=?", (artifact_id,)).fetchone()
            if row is None or row["domain_id"] != actor.domain_id:
                raise AuthorizationError("artifact release requirements are not satisfied")
            self._require_manifest_provenance(row, connection=connection)
            if row["state"] == "released":
                return {"artifact_id": artifact_id, "state": "released", "duplicate": True}
            existing = connection.execute(
                "SELECT * FROM artifact_release_outbox WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if existing is not None:
                if existing["actor_json"] != actor_json or existing["policy_decision_id"] != policy_decision_id:
                    raise AuthorizationError("artifact pending release belongs to different exact authority")
            else:
                if row["state"] != "scan_passed":
                    raise AuthorizationError("artifact release requirements are not satisfied")
                self._require_current_decision(
                    connection,
                    decision_id=policy_decision_id,
                    actor=actor,
                    action="artifact.release",
                    resource_id=artifact_id,
                )
                intent_id = str(uuid4())
                outbox_id = str(uuid4())
                request_digest = canonical_digest(
                    {
                        "action": "artifact.release",
                        "artifact_id": artifact_id,
                        "object_key": row["object_key"],
                        "object_version": row["object_version"],
                        "policy_decision_id": policy_decision_id,
                    }
                )
                connection.execute(
                    """INSERT INTO audit_intents(
                        intent_id,action,resource_id,actor_json,policy_decision_id,request_digest,state,created_at
                    ) VALUES(?,?,?,?,?,?,'pending',?)""",
                    (intent_id, "artifact.release", artifact_id, actor_json, policy_decision_id, request_digest, now),
                )
                connection.execute(
                    """INSERT INTO artifact_release_outbox(
                        outbox_id,artifact_id,intent_id,object_key,object_version,actor_json,
                        policy_decision_id,state,created_at,updated_at
                    ) VALUES(?,?,?,?,?,?,?,'pending',?,?)""",
                    (
                        outbox_id,
                        artifact_id,
                        intent_id,
                        row["object_key"],
                        row["object_version"],
                        actor_json,
                        policy_decision_id,
                        now,
                        now,
                    ),
                )
                connection.execute(
                    "UPDATE artifact_manifests SET state='release_pending' WHERE artifact_id=? AND state='scan_passed'",
                    (artifact_id,),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "artifact.release_intent_committed",
                        "artifact_id": artifact_id,
                        "intent_id": intent_id,
                        "policy_decision_id": policy_decision_id,
                        "request_digest": request_digest,
                    },
                )
                if phase_hook is not None:
                    phase_hook("after_release_intent_inserted")
        if phase_hook is not None:
            phase_hook("after_release_intent_committed")
        return self.process_release_outbox(artifact_id, phase_hook=phase_hook)

    def process_release_outbox(
        self,
        artifact_id: str,
        *,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        """Idempotently promote one authorized release and commit its result."""

        try:
            self._require_fresh_scan(artifact_id)
        except Exception as exc:
            self._hold_for_scan_failure(artifact_id, type(exc).__name__)
            raise

        row = self.store.fetch_one(
            """SELECT o.*,m.state AS manifest_state
                 FROM artifact_release_outbox o
                 JOIN artifact_manifests m ON m.artifact_id=o.artifact_id
                WHERE o.artifact_id=?""",
            (artifact_id,),
        )
        if row is None:
            raise ConflictError("artifact release outbox entry is absent")
        manifest_provenance = self.store.fetch_one(
            "SELECT * FROM artifact_manifests WHERE artifact_id=?",
            (artifact_id,),
        )
        if manifest_provenance is None:
            raise ConflictError("artifact release manifest disappeared")
        self._require_manifest_provenance(manifest_provenance)
        if row["state"] == "completed" and row["manifest_state"] == "released":
            return {"artifact_id": artifact_id, "state": "released", "duplicate": True}

        now = int(time.time())
        with self.store.transaction() as connection:
            connection.execute(
                "UPDATE artifact_release_outbox SET attempts=attempts+1,updated_at=?,last_error=NULL WHERE artifact_id=? AND state='pending'",
                (now, artifact_id),
            )
        try:
            self.objects.promote(row["object_key"], row["object_version"])
        except Exception as exc:
            with self.store.transaction() as connection:
                connection.execute(
                    "UPDATE artifact_release_outbox SET last_error=?,updated_at=? WHERE artifact_id=? AND state='pending'",
                    (type(exc).__name__, int(time.time()), artifact_id),
                )
            raise
        if phase_hook is not None:
            phase_hook("after_release_object_promoted")

        completed_at = int(time.time())
        with self.store.transaction() as connection:
            current = connection.execute(
                "SELECT * FROM artifact_release_outbox WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if current is None:
                raise ConflictError("artifact release outbox disappeared")
            if current["state"] == "completed":
                return {"artifact_id": artifact_id, "state": "released", "duplicate": True}
            manifest = connection.execute(
                "SELECT * FROM artifact_manifests WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if (
                manifest is None
                or manifest["state"] != "release_pending"
                or manifest["object_key"] != current["object_key"]
                or manifest["object_version"] != current["object_version"]
            ):
                raise ConflictError("artifact release state no longer binds the exact immutable object")
            self._require_manifest_provenance(manifest, connection=connection)
            connection.execute(
                "UPDATE artifact_manifests SET state='released' WHERE artifact_id=? AND state='release_pending'",
                (artifact_id,),
            )
            connection.execute(
                """UPDATE artifact_release_outbox
                      SET state='completed',completed_at=?,updated_at=?,last_error=NULL
                    WHERE artifact_id=? AND state='pending'""",
                (completed_at, completed_at, artifact_id),
            )
            connection.execute(
                "UPDATE audit_intents SET state='completed',completed_at=? WHERE intent_id=? AND state='pending'",
                (completed_at, current["intent_id"]),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "artifact.released",
                    "artifact_id": artifact_id,
                    "intent_id": current["intent_id"],
                    "policy_decision_id": current["policy_decision_id"],
                    "object_version": current["object_version"],
                },
            )
            if phase_hook is not None:
                phase_hook("before_release_commit")
        return {"artifact_id": artifact_id, "state": "released", "audit_hash": audit_hash, "duplicate": False}

    def recover_release_outbox(self, *, limit: int = 100) -> list[dict[str, Any]]:
        if limit < 1 or limit > 1_000:
            raise ValidationError("artifact release recovery limit is invalid")
        rows = self.store.fetch_all(
            "SELECT artifact_id FROM artifact_release_outbox WHERE state='pending' ORDER BY created_at LIMIT ?",
            (limit,),
        )
        return [self.process_release_outbox(row["artifact_id"]) for row in rows]

    @staticmethod
    def _validate_lifecycle_reason(reason: str) -> str:
        normalized = reason.strip()
        if not normalized or len(normalized) > 512:
            raise ValidationError("artifact lifecycle reason is required and bounded")
        return normalized

    def _lifecycle_in_transaction(self, connection: Any, artifact_id: str, *, now: int) -> Any:
        connection.execute(
            """INSERT INTO artifact_lifecycle(artifact_id,revision,status,updated_at)
               VALUES(?,1,'active',?) ON CONFLICT(artifact_id) DO NOTHING""",
            (artifact_id, now),
        )
        return connection.execute(
            "SELECT * FROM artifact_lifecycle WHERE artifact_id=?",
            (artifact_id,),
        ).fetchone()

    def lifecycle_status(self, artifact_id: str, *, actor: VerifiedActor) -> dict[str, Any]:
        """Return authorized content-free lifecycle/version metadata."""

        now = int(time.time())
        with self.store.transaction() as connection:
            manifest = connection.execute(
                "SELECT domain_id,object_version,state,created_at FROM artifact_manifests WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if manifest is None or manifest["domain_id"] != actor.domain_id:
                raise AuthorizationError("artifact lifecycle is not visible")
            lifecycle = self._lifecycle_in_transaction(connection, artifact_id, now=now)
            return {
                "artifact_id": artifact_id,
                "lifecycle_revision": int(lifecycle["revision"]),
                "lifecycle_state": lifecycle["status"],
                "legal_hold": lifecycle["legal_hold_at"] is not None,
                "manifest_state": manifest["state"],
                "object_version": manifest["object_version"],
                "created_at": int(manifest["created_at"]),
                "updated_at": int(lifecycle["updated_at"]),
            }

    def set_legal_hold(
        self,
        artifact_id: str,
        *,
        actor: VerifiedActor,
        policy_decision_id: str,
        expected_revision: int,
        reason: str,
        enabled: bool,
    ) -> dict[str, Any]:
        """Set or clear an exact revision-fenced legal hold."""

        if self.outage_gate is not None:
            self.outage_gate.require_privileged()
        reason = self._validate_lifecycle_reason(reason)
        if type(expected_revision) is not int or expected_revision < 1:
            raise ValidationError("artifact lifecycle revision is invalid")
        now = int(time.time())
        action = "artifact.legal_hold.set" if enabled else "artifact.legal_hold.clear"
        exact = {"enabled": enabled, "expected_revision": expected_revision, "reason": reason}
        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        with self.store.transaction() as connection:
            manifest = connection.execute(
                "SELECT domain_id,state FROM artifact_manifests WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if manifest is None or manifest["domain_id"] != actor.domain_id:
                raise AuthorizationError("artifact lifecycle is not visible")
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action=action,
                resource_id=artifact_id,
                expected_context=exact,
            )
            lifecycle = self._lifecycle_in_transaction(connection, artifact_id, now=now)
            if int(lifecycle["revision"]) != expected_revision:
                raise ConflictError("artifact lifecycle revision changed")
            if lifecycle["status"] != "active" or manifest["state"] in {"deletion_pending", "deleted"}:
                raise ConflictError("artifact legal hold cannot mutate after deletion begins")
            if not enabled and lifecycle["legal_hold_at"] is None:
                raise ConflictError("artifact has no active legal hold to clear")
            encrypted_reason = self.store.cipher.encrypt_json(
                reason,
                purpose=f"artifact-legal-hold:{artifact_id}:{expected_revision + 1}",
            )
            if enabled:
                updated = connection.execute(
                    """UPDATE artifact_lifecycle
                          SET revision=revision+1,legal_hold_at=?,legal_hold_reason_encrypted=?,
                              legal_hold_actor_json=?,updated_at=?
                        WHERE artifact_id=? AND revision=? AND status='active'""",
                    (now, encrypted_reason, actor_json, now, artifact_id, expected_revision),
                )
            else:
                updated = connection.execute(
                    """UPDATE artifact_lifecycle
                          SET revision=revision+1,legal_hold_at=NULL,legal_hold_reason_encrypted=NULL,
                              legal_hold_actor_json=NULL,updated_at=?
                        WHERE artifact_id=? AND revision=? AND status='active' AND legal_hold_at IS NOT NULL""",
                    (now, artifact_id, expected_revision),
                )
            if updated.rowcount != 1:
                raise ConflictError("artifact legal hold raced with another lifecycle mutation")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": action,
                    "actor": actor.audit_view(),
                    "artifact_id": artifact_id,
                    "lifecycle_revision": expected_revision + 1,
                    "policy_decision_id": policy_decision_id,
                    "reason_digest": canonical_digest({"reason": reason}),
                },
            )
        return {
            "artifact_id": artifact_id,
            "legal_hold": enabled,
            "lifecycle_revision": expected_revision + 1,
            "state": "active",
            "audit_hash": audit_hash,
        }

    @staticmethod
    def _require_no_retained_event_reference(
        connection: Any,
        *,
        artifact_id: str,
        domain_id: str,
        now: int,
    ) -> None:
        rows = connection.execute(
            """SELECT event_id,envelope_json,retention_delete_at,legal_hold
                 FROM events WHERE domain_id=?""",
            (domain_id,),
        ).fetchall()
        for row in rows:
            try:
                envelope = json.loads(row["envelope_json"])
                if not isinstance(envelope, dict):
                    raise ValueError("event envelope is not an object")
                released = envelope.get("released_artifacts", [])
                if not isinstance(released, list) or any(
                    not isinstance(binding, dict) for binding in released
                ):
                    raise ValueError("event artifact bindings are malformed")
                referenced = any(binding.get("artifact_id") == artifact_id for binding in released)
            except (TypeError, ValueError):
                raise ConflictError("artifact event-reference evidence is malformed") from None
            if not referenced:
                continue
            if bool(row["legal_hold"]):
                raise ConflictError("artifact is referenced by a legally held event")
            retention_delete_at = row["retention_delete_at"]
            if retention_delete_at is None or int(retention_delete_at) > now:
                raise ConflictError("artifact is referenced by retained corporate history")

    def delete(
        self,
        artifact_id: str,
        *,
        actor: VerifiedActor,
        policy_decision_id: str,
        expected_revision: int,
        reason: str,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        """Stage exact deletion before unlinking bytes; legal/retention holds win."""

        if self.outage_gate is not None:
            self.outage_gate.require_privileged()
        reason = self._validate_lifecycle_reason(reason)
        if type(expected_revision) is not int or expected_revision < 1:
            raise ValidationError("artifact lifecycle revision is invalid")
        now = int(time.time())
        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        exact = {"expected_revision": expected_revision, "reason": reason}
        with self.store.transaction() as connection:
            manifest = connection.execute(
                "SELECT * FROM artifact_manifests WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if manifest is None or manifest["domain_id"] != actor.domain_id:
                raise AuthorizationError("artifact deletion requirements are not satisfied")
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action="artifact.delete",
                resource_id=artifact_id,
                expected_context=exact,
            )
            lifecycle = self._lifecycle_in_transaction(connection, artifact_id, now=now)
            if int(lifecycle["revision"]) != expected_revision:
                raise ConflictError("artifact lifecycle revision changed")
            if lifecycle["legal_hold_at"] is not None:
                raise ConflictError("artifact deletion is blocked by legal hold")
            if lifecycle["status"] != "active" or manifest["state"] in {"release_pending", "deletion_pending", "deleted"}:
                raise ConflictError("artifact is not in a deletable lifecycle state")
            self._require_no_retained_event_reference(
                connection,
                artifact_id=artifact_id,
                domain_id=actor.domain_id,
                now=now,
            )
            intent_id = str(uuid4())
            outbox_id = str(uuid4())
            request_digest = canonical_digest(
                {
                    "action": "artifact.delete",
                    "artifact_id": artifact_id,
                    "expected_revision": expected_revision,
                    "object_version": manifest["object_version"],
                    "policy_decision_id": policy_decision_id,
                    "reason": reason,
                }
            )
            connection.execute(
                """INSERT INTO audit_intents(
                       intent_id,action,resource_id,actor_json,policy_decision_id,
                       request_digest,state,created_at
                   ) VALUES(?,?,?,?,?,?,'pending',?)""",
                (
                    intent_id,
                    "artifact.delete",
                    artifact_id,
                    actor_json,
                    policy_decision_id,
                    request_digest,
                    now,
                ),
            )
            connection.execute(
                """INSERT INTO artifact_deletion_outbox(
                       outbox_id,artifact_id,intent_id,object_key,object_version,actor_json,
                       policy_decision_id,expected_revision,state,created_at,updated_at
                   ) VALUES(?,?,?,?,?,?,?,?, 'pending',?,?)""",
                (
                    outbox_id,
                    artifact_id,
                    intent_id,
                    manifest["object_key"],
                    manifest["object_version"],
                    actor_json,
                    policy_decision_id,
                    expected_revision,
                    now,
                    now,
                ),
            )
            encrypted_reason = self.store.cipher.encrypt_json(
                reason,
                purpose=f"artifact-deletion:{artifact_id}:{expected_revision + 1}",
            )
            updated = connection.execute(
                """UPDATE artifact_lifecycle
                      SET revision=revision+1,status='deletion_pending',
                          deletion_reason_encrypted=?,deletion_actor_json=?,updated_at=?
                    WHERE artifact_id=? AND revision=? AND status='active' AND legal_hold_at IS NULL""",
                (encrypted_reason, actor_json, now, artifact_id, expected_revision),
            )
            if updated.rowcount != 1:
                raise ConflictError("artifact deletion raced with another lifecycle mutation")
            connection.execute(
                "UPDATE artifact_manifests SET state='deletion_pending' WHERE artifact_id=?",
                (artifact_id,),
            )
            connection.execute(
                "UPDATE download_capabilities SET consumed_at=COALESCE(consumed_at,?) WHERE artifact_id=?",
                (now, artifact_id),
            )
            self._begin_artifact_byte_release_in_transaction(
                connection,
                reservation_id=manifest["reservation_id"],
                reason="deleted",
                now=now,
            )
            self.store.append_audit(
                connection,
                {
                    "action": "artifact.deletion_intent_committed",
                    "actor": actor.audit_view(),
                    "artifact_id": artifact_id,
                    "intent_id": intent_id,
                    "lifecycle_revision": expected_revision + 1,
                    "policy_decision_id": policy_decision_id,
                    "reason_digest": canonical_digest({"reason": reason}),
                    "request_digest": request_digest,
                },
            )
            if phase_hook is not None:
                phase_hook("after_deletion_intent_inserted")
        if phase_hook is not None:
            phase_hook("after_deletion_intent_committed")
        return self.process_deletion_outbox(artifact_id, phase_hook=phase_hook)

    def process_deletion_outbox(
        self,
        artifact_id: str,
        *,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        row = self.store.fetch_one(
            """SELECT o.*,l.status AS lifecycle_state,l.revision,m.state AS manifest_state
                 FROM artifact_deletion_outbox o
                 JOIN artifact_lifecycle l ON l.artifact_id=o.artifact_id
                 JOIN artifact_manifests m ON m.artifact_id=o.artifact_id
                WHERE o.artifact_id=?""",
            (artifact_id,),
        )
        if row is None:
            raise ConflictError("artifact deletion outbox entry is absent")
        if row["state"] == "completed" and row["lifecycle_state"] == "deleted":
            return {
                "artifact_id": artifact_id,
                "lifecycle_revision": int(row["revision"]),
                "state": "deleted",
                "duplicate": True,
            }
        if (
            row["state"] != "pending"
            or row["lifecycle_state"] != "deletion_pending"
            or row["manifest_state"] != "deletion_pending"
            or int(row["revision"]) != int(row["expected_revision"]) + 1
        ):
            raise ConflictError("artifact deletion outbox is not current")
        now = int(time.time())
        with self.store.transaction() as connection:
            connection.execute(
                """UPDATE artifact_deletion_outbox
                      SET attempts=attempts+1,updated_at=?,last_error=NULL
                    WHERE artifact_id=? AND state='pending'""",
                (now, artifact_id),
            )
        try:
            self.objects.delete_version(row["object_key"], row["object_version"])
        except Exception as exc:
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE artifact_deletion_outbox SET last_error=?,updated_at=?
                         WHERE artifact_id=? AND state='pending'""",
                    (type(exc).__name__, int(time.time()), artifact_id),
                )
            raise
        if phase_hook is not None:
            phase_hook("after_deletion_object_removed")
        completed_at = int(time.time())
        with self.store.transaction() as connection:
            current = connection.execute(
                "SELECT * FROM artifact_deletion_outbox WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            lifecycle = connection.execute(
                "SELECT * FROM artifact_lifecycle WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            manifest = connection.execute(
                "SELECT * FROM artifact_manifests WHERE artifact_id=?",
                (artifact_id,),
            ).fetchone()
            if current is None or lifecycle is None or manifest is None:
                raise ConflictError("artifact deletion state disappeared")
            if current["state"] == "completed" and lifecycle["status"] == "deleted":
                return {
                    "artifact_id": artifact_id,
                    "lifecycle_revision": int(lifecycle["revision"]),
                    "state": "deleted",
                    "duplicate": True,
                }
            if (
                current["state"] != "pending"
                or lifecycle["status"] != "deletion_pending"
                or manifest["state"] != "deletion_pending"
                or int(lifecycle["revision"]) != int(current["expected_revision"]) + 1
            ):
                raise ConflictError("artifact deletion commit lost its exact lifecycle fence")
            connection.execute(
                """UPDATE artifact_manifests
                      SET state='deleted',scanner_attestation_json=NULL
                    WHERE artifact_id=? AND state='deletion_pending'""",
                (artifact_id,),
            )
            connection.execute(
                """UPDATE artifact_lifecycle SET status='deleted',deleted_at=?,updated_at=?
                     WHERE artifact_id=? AND status='deletion_pending'""",
                (completed_at, completed_at, artifact_id),
            )
            connection.execute(
                """UPDATE artifact_deletion_outbox
                      SET state='completed',completed_at=?,updated_at=?,last_error=NULL
                    WHERE artifact_id=? AND state='pending'""",
                (completed_at, completed_at, artifact_id),
            )
            connection.execute(
                "UPDATE audit_intents SET state='completed',completed_at=? WHERE intent_id=? AND state='pending'",
                (completed_at, current["intent_id"]),
            )
            self._finalize_artifact_byte_release_in_transaction(
                connection,
                reservation_id=manifest["reservation_id"],
                now=completed_at,
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "artifact.deleted",
                    "artifact_id": artifact_id,
                    "intent_id": current["intent_id"],
                    "lifecycle_revision": int(lifecycle["revision"]),
                    "object_version": current["object_version"],
                    "policy_decision_id": current["policy_decision_id"],
                },
            )
            if phase_hook is not None:
                phase_hook("before_deletion_commit")
        return {
            "artifact_id": artifact_id,
            "audit_hash": audit_hash,
            "duplicate": False,
            "lifecycle_revision": int(row["expected_revision"]) + 1,
            "state": "deleted",
        }

    def recover_deletion_outbox(self, *, limit: int = 100) -> list[dict[str, Any]]:
        if limit < 1 or limit > 1_000:
            raise ValidationError("artifact deletion recovery limit is invalid")
        rows = self.store.fetch_all(
            """SELECT artifact_id FROM artifact_deletion_outbox
                WHERE state='pending' ORDER BY created_at LIMIT ?""",
            (limit,),
        )
        return [self.process_deletion_outbox(row["artifact_id"]) for row in rows]

    def issue_download_capability(
        self,
        artifact_id: str,
        *,
        actor: VerifiedActor,
        audience_harness_id: str,
        policy_decision_id: str,
        ttl_seconds: int = 60,
    ) -> str:
        if self.outage_gate is not None:
            self.outage_gate.require_privileged()
        visible = self.store.fetch_one(
            "SELECT state,domain_id FROM artifact_manifests WHERE artifact_id=?",
            (artifact_id,),
        )
        if visible is None or visible["state"] != "released" or visible["domain_id"] != actor.domain_id:
            raise AuthorizationError("artifact is not released")
        try:
            self._require_fresh_scan(artifact_id)
        except Exception as exc:
            self._hold_for_scan_failure(artifact_id, type(exc).__name__)
            raise
        token = secrets.token_urlsafe(32)
        token_hash = hashlib.sha256(token.encode("ascii")).hexdigest()
        now = int(time.time())
        with self.store.transaction() as connection:
            row = connection.execute("SELECT state,domain_id FROM artifact_manifests WHERE artifact_id=?", (artifact_id,)).fetchone()
            if row is None or row["state"] != "released" or row["domain_id"] != actor.domain_id:
                raise AuthorizationError("artifact is not released")
            if actor.harness_id != audience_harness_id:
                raise AuthorizationError("download audience must be the verified caller harness")
            self._require_current_decision(
                connection,
                decision_id=policy_decision_id,
                actor=actor,
                action="artifact.download",
                resource_id=artifact_id,
                expected_context={"audience_harness_id": audience_harness_id},
            )
            connection.execute(
                "INSERT INTO download_capabilities(capability_hash,artifact_id,audience_harness_id,expires_at,issued_at) VALUES(?,?,?,?,?)",
                (token_hash, artifact_id, audience_harness_id, now + ttl_seconds, now),
            )
            self.store.append_audit(
                connection,
                {"action": "artifact.download_capability_issued", "artifact_id": artifact_id, "audience_harness_id": audience_harness_id, "policy_decision_id": policy_decision_id},
            )
        return token

    def consume_download(self, token: str, *, actor: VerifiedActor) -> bytes:
        if self.outage_gate is not None:
            self.outage_gate.require_privileged()
        token_hash = hashlib.sha256(token.encode("ascii")).hexdigest()
        now = int(time.time())
        capability = self.store.fetch_one(
            """SELECT c.artifact_id,c.audience_harness_id,c.expires_at,c.consumed_at,
                      m.state,m.domain_id
                 FROM download_capabilities c
                 JOIN artifact_manifests m ON m.artifact_id=c.artifact_id
                WHERE c.capability_hash=?""",
            (token_hash,),
        )
        if (
            capability is None
            or capability["audience_harness_id"] != actor.harness_id
            or capability["domain_id"] != actor.domain_id
            or int(capability["expires_at"]) <= now
            or capability["consumed_at"] is not None
            or capability["state"] != "released"
        ):
            raise AuthorizationError("download capability is invalid")
        if capability is not None:
            try:
                self._require_fresh_scan(capability["artifact_id"])
            except Exception as exc:
                self._hold_for_scan_failure(capability["artifact_id"], type(exc).__name__)
                raise
        with self.store.transaction() as connection:
            row = connection.execute(
                """SELECT c.*,m.object_key,m.object_version,m.state,m.domain_id,m.plaintext_digest_encrypted
                   FROM download_capabilities c JOIN artifact_manifests m ON m.artifact_id=c.artifact_id
                   WHERE c.capability_hash=?""",
                (token_hash,),
            ).fetchone()
            if (
                row is None
                or row["audience_harness_id"] != actor.harness_id
                or row["domain_id"] != actor.domain_id
                or row["expires_at"] <= now
                or row["consumed_at"] is not None
                or row["state"] != "released"
            ):
                raise AuthorizationError("download capability is invalid")
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=connection.execute(
                    "SELECT policy_revision FROM domains WHERE domain_id=?",
                    (actor.domain_id,),
                ).fetchone()["policy_revision"],
                when=datetime.now(UTC),
            )
            if denial is not None:
                raise AuthorizationError("download caller is no longer current")
            connection.execute("UPDATE download_capabilities SET consumed_at=? WHERE capability_hash=?", (now, token_hash))
            self.store.append_audit(
                connection,
                {"action": "artifact.download_authorized", "artifact_id": row["artifact_id"], "audience_harness_id": actor.harness_id, "actor": actor.audit_view()},
            )
            object_key, version, artifact_id = row["object_key"], row["object_version"], row["artifact_id"]
            expected_digest = self.store.cipher.decrypt_json(
                row["plaintext_digest_encrypted"], purpose=f"artifact-digest:{artifact_id}"
            )
        content = self.objects.read_plaintext(object_key, version, released=True)
        if not secrets.compare_digest(hashlib.sha256(content).hexdigest(), expected_digest):
            raise ConflictError("released artifact plaintext digest mismatch")
        return content
