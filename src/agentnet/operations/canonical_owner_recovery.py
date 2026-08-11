"""Bounded recovery from the v0.1.50 placeholder Approval owner."""

from __future__ import annotations

import hashlib
import json
import os
import secrets
import stat
from pathlib import Path
from typing import Any, Literal

from cryptography.hazmat.primitives import serialization
from pydantic import BaseModel, ConfigDict, Field, model_validator
from agentnet.approval.config import ApprovalServiceConfig

from agentnet.approval.store import ApprovalStore, approval_user_handle
from agentnet.errors import GateBlocked
from agentnet.security.signatures import (
    P256KeyPair,
    b64url_encode,
    canonical_json,
    load_public_key,
)


_RECOVERY_ID = r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
_MAX_OWNER_APPROVAL_AGE_SECONDS = 31 * 24 * 60 * 60


class CanonicalOwnerAdoptionRequest(BaseModel):
    """Exact owner-approved placeholder-to-canonical adoption input."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.canonical-owner-adoption.v1"] = Field(alias="schema")
    recovery_id: str = Field(pattern=_RECOVERY_ID)
    domain_id: str = Field(min_length=1, max_length=256)
    source_principal_id: str = Field(min_length=1, max_length=256)
    target_principal_id: str = Field(min_length=1, max_length=256)
    oidc_issuer: str = Field(min_length=1, max_length=512)
    oidc_subject: str = Field(min_length=1, max_length=512)
    verified_email: str = Field(min_length=3, max_length=320)
    verifier_id: str = Field(min_length=1, max_length=128)
    approved_at: int = Field(ge=0)

    @model_validator(mode="after")
    def _different_principals(self) -> "CanonicalOwnerAdoptionRequest":
        if self.source_principal_id == self.target_principal_id:
            raise ValueError("source and target principals must differ")
        return self


class CanonicalOwnerAdoptionResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.canonical-owner-adoption-result.v1"] = Field(alias="schema")
    status: Literal["adopted", "already_exact"]
    recovery_id: str = Field(pattern=_RECOVERY_ID)
    migrated_active_credentials: int = Field(ge=0)
    revoked_browser_sessions: int = Field(ge=0)
    canceled_registration_ceremonies: int = Field(ge=0)


class CanonicalOwnerRecoveryReconstruction(BaseModel):
    """Exact observation proving a journal-less terminal repair."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal[
        "agentnet.canonical-owner-recovery-reconstruction.v1"
    ] = Field(alias="schema")
    observed_at: int = Field(ge=0)
    marker_approval_config_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    marker_core_config_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    realized_approval_config_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    realized_core_config_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    source_principal_evidence: Literal["approval_audit_marker_digest"]

class CanonicalOwnerRecoveryJournal(BaseModel):
    """Strict resumable evidence for one owner/signer cutover."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.canonical-owner-recovery-journal.v1"] = Field(
        alias="schema"
    )
    recovery_id: str = Field(pattern=_RECOVERY_ID)
    request_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    config_path: str = Field(min_length=1, max_length=4096)
    signer_path: str = Field(min_length=1, max_length=4096)
    target_signer_path: str = Field(min_length=1, max_length=4096)
    source_config_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    domain_id: str = Field(min_length=1, max_length=256)
    source_principal_id: str = Field(min_length=1, max_length=256)
    target_principal_id: str = Field(min_length=1, max_length=256)
    oidc_issuer: str = Field(min_length=1, max_length=512)
    source_signer_key_id: str = Field(min_length=16, max_length=256)
    source_signer_public_key_pem: str = Field(min_length=64, max_length=8192)
    target_signer_key_id: str = Field(min_length=16, max_length=256)
    target_signer_public_key_pem: str = Field(min_length=64, max_length=8192)
    staged_target_signer_private_key_pem: str | None = Field(
        default=None,
        min_length=64,
        max_length=65_536,
    )
    phase: Literal[
        "prepared",
        "authority_adopted",
        "signer_replaced",
        "config_replacing",
        "config_replaced",
        "complete",
    ]
    prepared_at: int = Field(ge=0)
    completed_at: int | None = Field(default=None, ge=0)
    authority_adoption: dict[str, Any] | None = None
    authority_adoption_digest: str | None = Field(
        default=None, pattern=r"^[0-9a-f]{64}$"
    )
    reconstruction: CanonicalOwnerRecoveryReconstruction | None = None

    @model_validator(mode="after")
    def _phase_shape(self) -> "CanonicalOwnerRecoveryJournal":
        if (
            self.phase != "prepared"
            and self.staged_target_signer_private_key_pem is not None
        ):
            raise ValueError("staged signer secret is retained after preparation")
        adopted = self.phase != "prepared"
        if adopted != (
            self.authority_adoption is not None
            and self.authority_adoption_digest is not None
        ):
            raise ValueError("authority adoption evidence does not match journal phase")
        if (self.phase == "complete") != (self.completed_at is not None):
            raise ValueError("completion timestamp does not match journal phase")
        if self.reconstruction is not None and (
            self.phase != "complete"
            or self.completed_at != self.reconstruction.observed_at
        ):
            raise ValueError("reconstructed evidence does not match terminal phase")
        if (
            self.authority_adoption is not None
            and hashlib.sha256(canonical_json(self.authority_adoption)).hexdigest()
            != self.authority_adoption_digest
        ):
            raise ValueError("authority adoption digest does not match")
        return self



def _request_digest(request: CanonicalOwnerAdoptionRequest) -> str:
    return hashlib.sha256(
        canonical_json(request.model_dump(by_alias=True, mode="json"))
    ).hexdigest()


def _result(
    request: CanonicalOwnerAdoptionRequest,
    *,
    status: Literal["adopted", "already_exact"],
    migrated_active_credentials: int,
    revoked_browser_sessions: int,
    canceled_registration_ceremonies: int,
) -> dict[str, Any]:
    return CanonicalOwnerAdoptionResult(
        schema="agentnet.canonical-owner-adoption-result.v1",
        status=status,
        recovery_id=request.recovery_id,
        migrated_active_credentials=migrated_active_credentials,
        revoked_browser_sessions=revoked_browser_sessions,
        canceled_registration_ceremonies=canceled_registration_ceremonies,
    ).model_dump(by_alias=True)


def _matches_binding(row: Any, request: CanonicalOwnerAdoptionRequest, principal_id: str) -> bool:
    return (
        row["domain_id"] == request.domain_id
        and row["approver_principal_id"] == principal_id
        and row["oidc_issuer"] == request.oidc_issuer
        and row["oidc_subject"] == request.oidc_subject
        and row["verified_email"] == request.verified_email
        and row["status"] == "active"
        and row["revoked_at"] is None
    )


def _active_credentials(connection: Any, principal_id: str, domain_id: str) -> list[Any]:
    return list(
        connection.execute(
            """SELECT * FROM approval_webauthn_credentials
                 WHERE approver_principal_id=? AND domain_id=? AND status='active'
                 ORDER BY credential_id_b64""",
            (principal_id, domain_id),
        ).fetchall()
    )


def _persisted_adoption_counts(
    connection: Any,
    *,
    request: CanonicalOwnerAdoptionRequest,
    request_digest: str,
) -> tuple[int, int, int]:
    rows = connection.execute(
        """SELECT detail_code FROM approval_audit
             WHERE action='owner.canonical_adoption' AND approver_principal_id=?
               AND domain_id=? AND approval_purpose='owner.canonical_adoption'
               AND transaction_digest=? AND outcome='adopted'""",
        (request.target_principal_id, request.domain_id, request_digest),
    ).fetchall()
    if len(rows) != 1:
        raise GateBlocked(
            "canonical_owner_recovery", "target recovery evidence is incomplete"
        )
    parts = str(rows[0]["detail_code"]).split(":")
    try:
        counts = tuple(int(value) for value in parts[2:])
    except ValueError as exc:
        raise GateBlocked(
            "canonical_owner_recovery", "target recovery evidence is invalid"
        ) from exc
    if (
        parts[:2] != ["canonical_owner_adopted", "v1"]
        or len(counts) != 3
        or any(value < 0 for value in counts)
        or parts[2:] != [str(value) for value in counts]
    ):
        raise GateBlocked(
            "canonical_owner_recovery", "target recovery evidence is invalid"
        )
    return counts


def _require_exact_target(
    connection: Any,
    *,
    request: CanonicalOwnerAdoptionRequest,
    binding: Any,
    request_digest: str,
) -> tuple[int, int, int]:
    if not _matches_binding(binding, request, request.target_principal_id):
        raise GateBlocked("canonical_owner_recovery", "target authority already exists")
    if connection.execute(
        """SELECT 1 FROM approval_owner_bindings
             WHERE domain_id=? AND approver_principal_id=? AND status='active'""",
        (request.domain_id, request.source_principal_id),
    ).fetchone() is not None:
        raise GateBlocked("canonical_owner_recovery", "source authority remains active")
    if _active_credentials(connection, request.source_principal_id, request.domain_id):
        raise GateBlocked("canonical_owner_recovery", "source authority remains active")
    credentials = _active_credentials(connection, request.target_principal_id, request.domain_id)
    expected_handle = b64url_encode(
        approval_user_handle(
            verifier_id=request.verifier_id,
            principal_id=request.target_principal_id,
            domain_id=request.domain_id,
        )
    )
    if not credentials or any(row["user_handle_b64"] != expected_handle for row in credentials):
        raise GateBlocked("canonical_owner_recovery", "target authority is incomplete")
    return _persisted_adoption_counts(
        connection,
        request=request,
        request_digest=request_digest,
    )


def validate_canonical_owner_adoption_state(
    connection: Any,
    *,
    request: CanonicalOwnerAdoptionRequest,
) -> tuple[int, int, int]:
    """Require the exact durable authority state produced by owner adoption."""

    active_bindings = list(
        connection.execute(
            """SELECT * FROM approval_owner_bindings
                 WHERE domain_id=? AND status='active'
                 ORDER BY binding_id""",
            (request.domain_id,),
        ).fetchall()
    )
    if len(active_bindings) != 1:
        raise GateBlocked("canonical_owner_recovery", "approval owner state is ambiguous")
    counts = _require_exact_target(
        connection,
        request=request,
        binding=active_bindings[0],
        request_digest=_request_digest(request),
    )
    if list(connection.execute("PRAGMA foreign_key_check").fetchall()):
        raise GateBlocked("canonical_owner_recovery", "approval owner adoption is inconsistent")
    return counts


def adopt_canonical_approval_owner(
    store: ApprovalStore,
    *,
    request: CanonicalOwnerAdoptionRequest,
    now: int,
) -> dict[str, Any]:
    """Atomically move only live Approval authority to the enrolled principal."""

    if (
        isinstance(now, bool)
        or not isinstance(now, int)
        or request.approved_at > now
        or now - request.approved_at > _MAX_OWNER_APPROVAL_AGE_SECONDS
    ):
        raise GateBlocked("canonical_owner_recovery", "owner approval is not current")

    digest = _request_digest(request)
    with store.transaction() as connection:
        active_bindings = list(
            connection.execute(
                """SELECT * FROM approval_owner_bindings
                     WHERE domain_id=? AND status='active'
                     ORDER BY binding_id""",
                (request.domain_id,),
            ).fetchall()
        )
        if len(active_bindings) != 1:
            raise GateBlocked("canonical_owner_recovery", "approval owner state is ambiguous")
        binding = active_bindings[0]

        if binding["approver_principal_id"] == request.target_principal_id:
            (
                migrated_active_credentials,
                revoked_browser_sessions,
                canceled_registration_ceremonies,
            ) = validate_canonical_owner_adoption_state(
                connection,
                request=request,
            )
            return _result(
                request,
                status="already_exact",
                migrated_active_credentials=migrated_active_credentials,
                revoked_browser_sessions=revoked_browser_sessions,
                canceled_registration_ceremonies=canceled_registration_ceremonies,
            )

        if not _matches_binding(binding, request, request.source_principal_id):
            raise GateBlocked("canonical_owner_recovery", "source state does not match")

        target_binding = connection.execute(
            """SELECT 1 FROM approval_owner_bindings
                 WHERE domain_id=? AND approver_principal_id=?""",
            (request.domain_id, request.target_principal_id),
        ).fetchone()
        target_credential = connection.execute(
            """SELECT 1 FROM approval_webauthn_credentials
                 WHERE domain_id=? AND approver_principal_id=?""",
            (request.domain_id, request.target_principal_id),
        ).fetchone()
        if target_binding is not None or target_credential is not None:
            raise GateBlocked("canonical_owner_recovery", "target authority already exists")

        pending_request = connection.execute(
            """SELECT 1 FROM approval_requests
                 WHERE approver_principal_id=? AND domain_id=? AND state='pending' LIMIT 1""",
            (request.source_principal_id, request.domain_id),
        ).fetchone()
        pending_registration = connection.execute(
            """SELECT 1 FROM approval_registration_sessions
                 WHERE approver_principal_id=? AND domain_id=?
                   AND consumed_at IS NULL AND expires_at>? LIMIT 1""",
            (request.source_principal_id, request.domain_id, now),
        ).fetchone()
        pending_oidc = connection.execute(
            """SELECT 1 FROM approval_oidc_login_transactions
                 WHERE state IN ('pending','callback_claimed') LIMIT 1"""
        ).fetchone()
        if pending_request is not None or pending_registration is not None or pending_oidc is not None:
            raise GateBlocked("canonical_owner_recovery", "nonterminal approval state exists")

        credentials = _active_credentials(
            connection, request.source_principal_id, request.domain_id
        )
        source_handle = b64url_encode(
            approval_user_handle(
                verifier_id=request.verifier_id,
                principal_id=request.source_principal_id,
                domain_id=request.domain_id,
            )
        )
        if not credentials or any(row["user_handle_b64"] != source_handle for row in credentials):
            raise GateBlocked("canonical_owner_recovery", "source credential state does not match")

        ceremonies = connection.execute(
            """UPDATE approval_registration_ceremonies
                  SET state='canceled'
                WHERE owner_binding_id=? AND state='pending'""",
            (binding["binding_id"],),
        )
        sessions = connection.execute(
            """UPDATE approval_browser_sessions
                  SET revoked_at=?,revocation_reason='canonical_owner_adoption'
                WHERE owner_binding_id=? AND revoked_at IS NULL AND expires_at>?""",
            (now, binding["binding_id"], now),
        )
        connection.execute(
            """UPDATE approval_owner_bindings
                  SET approver_principal_id=?
                WHERE binding_id=? AND approver_principal_id=? AND status='active'""",
            (
                request.target_principal_id,
                binding["binding_id"],
                request.source_principal_id,
            ),
        )
        target_handle = b64url_encode(
            approval_user_handle(
                verifier_id=request.verifier_id,
                principal_id=request.target_principal_id,
                domain_id=request.domain_id,
            )
        )
        migrated = connection.execute(
            """UPDATE approval_webauthn_credentials
                  SET approver_principal_id=?,user_handle_b64=?
                WHERE approver_principal_id=? AND domain_id=? AND status='active'""",
            (
                request.target_principal_id,
                target_handle,
                request.source_principal_id,
                request.domain_id,
            ),
        )
        if migrated.rowcount != len(credentials):
            raise GateBlocked("canonical_owner_recovery", "approval owner adoption raced")
        adoption_counts = (
            len(credentials),
            int(sessions.rowcount),
            int(ceremonies.rowcount),
        )
        adoption_detail = "canonical_owner_adopted:v1:" + ":".join(
            str(value) for value in adoption_counts
        )
        connection.execute(
            """INSERT INTO approval_audit(
                   action,request_id,approver_principal_id,domain_id,approval_purpose,
                   transaction_digest,occurred_at,outcome,detail_code
               ) VALUES('owner.canonical_adoption',NULL,?,?,
                        'owner.canonical_adoption',?,?,'adopted',?)""",
            (
                request.target_principal_id,
                request.domain_id,
                digest,
                now,
                adoption_detail,
            ),
        )
        persisted_counts = validate_canonical_owner_adoption_state(
            connection,
            request=request,
        )
        if persisted_counts != adoption_counts:
            raise GateBlocked(
                "canonical_owner_recovery", "approval owner adoption evidence drifted"
            )

        return _result(
            request,
            status="adopted",
            migrated_active_credentials=adoption_counts[0],
            revoked_browser_sessions=adoption_counts[1],
            canceled_registration_ceremonies=adoption_counts[2],
        )


class CanonicalOwnerRecoveryResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.canonical-owner-recovery-result.v1"] = Field(alias="schema")
    status: Literal["recovered", "already_exact"]
    recovery_id: str = Field(pattern=_RECOVERY_ID)
    principal_id: str = Field(min_length=1, max_length=256)
    signer_key_id: str = Field(min_length=16, max_length=256)
    historical_signer_key_id: str = Field(min_length=16, max_length=256)
    authority_adoption: dict[str, Any]


def _open_private_parent(path: Path, *, create: bool) -> tuple[Path, int, os.stat_result]:
    path = path.absolute()
    if create:
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    flags = (
        os.O_RDONLY
        | getattr(os, "O_CLOEXEC", 0)
        | getattr(os, "O_DIRECTORY", 0)
        | getattr(os, "O_NOFOLLOW", 0)
    )
    try:
        descriptor = os.open(path.parent, flags)
        metadata = os.fstat(descriptor)
    except OSError as exc:
        raise GateBlocked("canonical_owner_recovery", "recovery path is unsafe") from exc
    if (
        not stat.S_ISDIR(metadata.st_mode)
        or metadata.st_uid != os.geteuid()
        or metadata.st_mode & 0o077
    ):
        os.close(descriptor)
        raise GateBlocked("canonical_owner_recovery", "recovery directory custody is invalid")
    return path, descriptor, metadata


def _parent_matches(path: Path, metadata: os.stat_result) -> bool:
    try:
        current = os.stat(path.parent, follow_symlinks=False)
    except OSError:
        return False
    return (
        stat.S_ISDIR(current.st_mode)
        and current.st_dev == metadata.st_dev
        and current.st_ino == metadata.st_ino
    )


def _private_write(path: Path, payload: bytes) -> None:
    path, directory, parent = _open_private_parent(path, create=True)
    temporary_name = f".{path.name}.{secrets.token_hex(12)}"
    descriptor = -1
    try:
        descriptor = os.open(
            temporary_name,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_EXCL
            | getattr(os, "O_CLOEXEC", 0)
            | getattr(os, "O_NOFOLLOW", 0),
            0o600,
            dir_fd=directory,
        )
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            descriptor = -1
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        try:
            destination = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            destination = None
        if destination is not None and stat.S_ISLNK(destination.st_mode):
            raise GateBlocked("canonical_owner_recovery", "recovery path changed")
        if not _parent_matches(path, parent):
            raise GateBlocked("canonical_owner_recovery", "recovery path changed")
        os.replace(
            temporary_name,
            path.name,
            src_dir_fd=directory,
            dst_dir_fd=directory,
        )
        if not _parent_matches(path, parent):
            raise GateBlocked("canonical_owner_recovery", "recovery path changed")
        os.fsync(directory)
    except BaseException:
        if descriptor >= 0:
            os.close(descriptor)
        try:
            os.unlink(temporary_name, dir_fd=directory)
        except FileNotFoundError:
            pass
        raise
    finally:
        os.close(directory)


def _private_unlink(path: Path) -> None:
    path, directory, parent = _open_private_parent(path, create=False)
    try:
        try:
            metadata = os.stat(path.name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            return
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_mode & 0o077
            or not metadata.st_mode & 0o600
        ):
            raise GateBlocked(
                "canonical_owner_recovery", "retired signer custody is invalid"
            )
        if not _parent_matches(path, parent):
            raise GateBlocked("canonical_owner_recovery", "recovery path changed")
        os.unlink(path.name, dir_fd=directory)
        if not _parent_matches(path, parent):
            raise GateBlocked("canonical_owner_recovery", "recovery path changed")
        os.fsync(directory)
    finally:
        os.close(directory)


def _private_read(path: Path, *, maximum: int) -> bytes:
    path, directory, _parent = _open_private_parent(path, create=False)
    descriptor = -1
    try:
        descriptor = os.open(
            path.name,
            os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0),
            dir_fd=directory,
        )
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_mode & 0o077
            or not metadata.st_mode & 0o600
        ):
            raise GateBlocked("canonical_owner_recovery", "recovery state custody is invalid")
        with os.fdopen(descriptor, "rb", closefd=True) as handle:
            descriptor = -1
            payload = handle.read(maximum + 1)
    except GateBlocked:
        raise
    except OSError as exc:
        raise GateBlocked("canonical_owner_recovery", "recovery state is unavailable") from exc
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        os.close(directory)
    if len(payload) > maximum:
        raise GateBlocked("canonical_owner_recovery", "recovery state custody is invalid")
    return payload


def _journal_write(path: Path, value: dict[str, Any]) -> None:
    _private_write(
        path,
        json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n",
    )


def _configured_owner(
    config: ApprovalServiceConfig,
    request: CanonicalOwnerAdoptionRequest,
) -> tuple[int, Any]:
    matches = [
        (index, item)
        for index, item in enumerate(config.approvers)
        if item.domain_id == request.domain_id
        and item.oidc_issuer == request.oidc_issuer
        and item.oidc_subject == request.oidc_subject
        and item.principal_id
        in {request.source_principal_id, request.target_principal_id}
    ]
    if len(matches) != 1 or len(config.approvers) != 1:
        raise GateBlocked("canonical_owner_recovery", "configured owner state is ambiguous")
    return matches[0]


def _recovery_result(
    request: CanonicalOwnerAdoptionRequest,
    *,
    status: Literal["recovered", "already_exact"],
    journal: dict[str, Any],
    adoption: dict[str, Any],
) -> dict[str, Any]:
    return CanonicalOwnerRecoveryResult(
        schema="agentnet.canonical-owner-recovery-result.v1",
        status=status,
        recovery_id=request.recovery_id,
        principal_id=request.target_principal_id,
        signer_key_id=str(journal["target_signer_key_id"]),
        historical_signer_key_id=str(journal["source_signer_key_id"]),
        authority_adoption=adoption,
    ).model_dump(by_alias=True)
def _validate_recovery_journal(
    value: object,
    *,
    request: CanonicalOwnerAdoptionRequest,
    request_digest: str,
    config_path: Path,
) -> dict[str, Any]:
    try:
        journal = CanonicalOwnerRecoveryJournal.model_validate(value)
        source_public = load_public_key(journal.source_signer_public_key_pem)
        target_public = load_public_key(journal.target_signer_public_key_pem)
        staged_target = (
            P256KeyPair.from_private_pem(
                journal.staged_target_signer_private_key_pem.encode("ascii")
            )
            if journal.staged_target_signer_private_key_pem is not None
            else None
        )
    except Exception as exc:
        raise GateBlocked(
            "canonical_owner_recovery", "recovery journal is invalid"
        ) from exc
    expected = {
        "recovery_id": request.recovery_id,
        "request_digest": request_digest,
        "config_path": str(config_path.absolute()),
        "domain_id": request.domain_id,
        "source_principal_id": request.source_principal_id,
        "target_principal_id": request.target_principal_id,
        "oidc_issuer": request.oidc_issuer,
    }
    if any(
        getattr(journal, key) != expected_value
        for key, expected_value in expected.items()
    ):
        raise GateBlocked(
            "canonical_owner_recovery", "recovery journal conflicts with request"
        )
    source_thumbprint = b64url_encode(
        hashlib.sha256(
            source_public.public_bytes(
                serialization.Encoding.DER,
                serialization.PublicFormat.SubjectPublicKeyInfo,
            )
        ).digest()
    )
    target_thumbprint = b64url_encode(
        hashlib.sha256(
            target_public.public_bytes(
                serialization.Encoding.DER,
                serialization.PublicFormat.SubjectPublicKeyInfo,
            )
        ).digest()
    )
    if (
        source_thumbprint != journal.source_signer_key_id
        or target_thumbprint != journal.target_signer_key_id
        or (
            staged_target is not None
            and staged_target.thumbprint != journal.target_signer_key_id
        )
    ):
        raise GateBlocked(
            "canonical_owner_recovery", "recovery signer evidence is invalid"
        )
    return journal.model_dump(by_alias=True)


def validate_canonical_owner_recovery_journal(
    value: object,
    *,
    request: CanonicalOwnerAdoptionRequest,
    config_path: Path,
) -> dict[str, Any]:
    """Validate one persisted journal against its exact recovery request."""

    return _validate_recovery_journal(
        value,
        request=request,
        request_digest=_request_digest(request),
        config_path=config_path,
    )




def converge_canonical_approval_owner(
    store: ApprovalStore,
    *,
    config_path: Path,
    journal_path: Path,
    request: CanonicalOwnerAdoptionRequest,
    now: int,
    _interrupt_after: Literal[
        "prepared_journal",
        "authority_committed",
        "authority_adopted",
        "signer_replaced",
        "retired_signers_removed",
    ]
    | None = None,
) -> dict[str, Any]:
    """Converge Approval authority, receipt signer, and config after v0.1.50."""

    request_digest = _request_digest(request)
    config = ApprovalServiceConfig.model_validate_json(
        _private_read(config_path.absolute(), maximum=1_048_576)
    )
    index, configured = _configured_owner(config, request)
    journal_exists = journal_path.exists() or journal_path.is_symlink()
    journal: dict[str, Any]
    if journal_exists:
        try:
            raw_journal = json.loads(
                _private_read(journal_path.absolute(), maximum=262_144).decode("utf-8")
            )
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise GateBlocked("canonical_owner_recovery", "recovery journal is invalid") from exc
        journal = _validate_recovery_journal(
            raw_journal,
            request=request,
            request_digest=request_digest,
            config_path=config_path,
        )
        signer_path = Path(str(journal["signer_path"]))
        target_path = Path(str(journal["target_signer_path"]))
    else:
        signer_path = configured.signer_private_key_path
        target_path = signer_path.parent / "canonical-owner-recovery.pem"
        if target_path.exists() or target_path.is_symlink():
            raise GateBlocked(
                "canonical_owner_recovery", "untracked target signer state exists"
            )
    signer_root = config.data_dir / "signers"
    if (
        signer_path.parent != signer_root
        or target_path.parent != signer_root
        or signer_path == target_path
        or configured.signer_private_key_path
        not in {signer_path, target_path}
    ):
        raise GateBlocked("canonical_owner_recovery", "configured signer path is outside custody")

    backup_path = config.data_dir / "canonical-owner-recovery.backup.pem"
    if not journal_exists:
        if configured.principal_id != request.source_principal_id:
            raise GateBlocked("canonical_owner_recovery", "target config lacks recovery journal")
        source_signer = P256KeyPair.from_private_pem(
            _private_read(signer_path, maximum=65_536)
        )
        if source_signer.thumbprint != configured.signer_key_id:
            raise GateBlocked("canonical_owner_recovery", "source signer does not match config")
        target_signer = P256KeyPair.generate()
        _private_write(backup_path, source_signer.private_pem)
        journal = {
            "schema": "agentnet.canonical-owner-recovery-journal.v1",
            "recovery_id": request.recovery_id,
            "request_digest": request_digest,
            "config_path": str(config_path.absolute()),
            "signer_path": str(signer_path),
            "target_signer_path": str(target_path),
            "source_config_sha256": hashlib.sha256(
                _private_read(config_path.absolute(), maximum=1_048_576)
            ).hexdigest(),
            "domain_id": request.domain_id,
            "source_principal_id": request.source_principal_id,
            "target_principal_id": request.target_principal_id,
            "oidc_issuer": request.oidc_issuer,
            "source_signer_key_id": source_signer.thumbprint,
            "source_signer_public_key_pem": source_signer.public_pem,
            "target_signer_key_id": target_signer.thumbprint,
            "target_signer_public_key_pem": target_signer.public_pem,
            "staged_target_signer_private_key_pem": target_signer.private_pem.decode(
                "ascii"
            ),
            "phase": "prepared",
            "prepared_at": now,
            "completed_at": None,
        }
        journal = _validate_recovery_journal(
            journal,
            request=request,
            request_digest=request_digest,
            config_path=config_path,
        )
        _journal_write(journal_path, journal)
        if _interrupt_after == "prepared_journal":
            raise RuntimeError("injected recovery interruption")

    staged_target_pem = journal.get("staged_target_signer_private_key_pem")
    if target_path.exists() or target_path.is_symlink():
        target_signer = P256KeyPair.from_private_pem(
            _private_read(target_path, maximum=65_536)
        )
    elif isinstance(staged_target_pem, str):
        target_signer = P256KeyPair.from_private_pem(staged_target_pem.encode("ascii"))
        _private_write(target_path, target_signer.private_pem)
    else:
        raise GateBlocked(
            "canonical_owner_recovery", "journaled target signer state is unavailable"
        )
    if target_signer.thumbprint != journal["target_signer_key_id"]:
        raise GateBlocked(
            "canonical_owner_recovery", "staged signer does not match journal"
        )
    if staged_target_pem is not None:
        journal.pop("staged_target_signer_private_key_pem", None)
        _journal_write(journal_path, journal)

    was_complete = journal_exists and journal.get("phase") == "complete"
    if journal["phase"] in {"prepared", "authority_adopted", "signer_replaced"}:
        if (
            hashlib.sha256(
                _private_read(config_path.absolute(), maximum=1_048_576)
            ).hexdigest()
            != journal["source_config_sha256"]
        ):
            raise GateBlocked(
                "canonical_owner_recovery", "source config changed during recovery"
            )
        source_signer = P256KeyPair.from_private_pem(
            _private_read(backup_path, maximum=65_536)
        )
        if source_signer.thumbprint != journal["source_signer_key_id"]:
            raise GateBlocked(
                "canonical_owner_recovery", "source signer backup is invalid"
            )
    verified_adoption = adopt_canonical_approval_owner(store, request=request, now=now)
    if _interrupt_after == "authority_committed":
        raise RuntimeError("injected recovery interruption")
    if journal["phase"] != "prepared":
        try:
            recorded_adoption = CanonicalOwnerAdoptionResult.model_validate(
                journal["authority_adoption"]
            )
            observed_adoption = CanonicalOwnerAdoptionResult.model_validate(
                verified_adoption
            )
        except (TypeError, ValueError) as exc:
            raise GateBlocked(
                "canonical_owner_recovery",
                "authority adoption evidence conflicts with recovery",
            ) from exc
        if (
            recorded_adoption.recovery_id != request.recovery_id
            or observed_adoption.recovery_id != recorded_adoption.recovery_id
            or observed_adoption.status != "already_exact"
            or observed_adoption.migrated_active_credentials
            != recorded_adoption.migrated_active_credentials
            or observed_adoption.revoked_browser_sessions
            != recorded_adoption.revoked_browser_sessions
            or observed_adoption.canceled_registration_ceremonies
            != recorded_adoption.canceled_registration_ceremonies
        ):
            raise GateBlocked(
                "canonical_owner_recovery",
                "authority adoption evidence conflicts with recovery",
            )
    if journal["phase"] == "prepared":
        journal["phase"] = "authority_adopted"
        journal["authority_adoption"] = verified_adoption
        journal["authority_adoption_digest"] = hashlib.sha256(
            canonical_json(verified_adoption)
        ).hexdigest()
        _journal_write(journal_path, journal)
    if _interrupt_after == "authority_adopted":
        raise RuntimeError("injected recovery interruption")

    if journal["phase"] == "authority_adopted":
        target_signer = P256KeyPair.from_private_pem(
            _private_read(target_path, maximum=65_536)
        )
        if target_signer.thumbprint != journal["target_signer_key_id"]:
            raise GateBlocked("canonical_owner_recovery", "staged signer does not match journal")
        journal["phase"] = "signer_replaced"
        _journal_write(journal_path, journal)
    if _interrupt_after == "signer_replaced":
        raise RuntimeError("injected recovery interruption")
    if journal["phase"] == "signer_replaced":
        current = ApprovalServiceConfig.model_validate_json(
            _private_read(config_path.absolute(), maximum=1_048_576)
        )
        _index, configured = _configured_owner(current, request)
        if configured.principal_id != request.source_principal_id:
            raise GateBlocked("canonical_owner_recovery", "configured principal changed unexpectedly")
        journal["phase"] = "config_replacing"
        _journal_write(journal_path, journal)

    if journal["phase"] == "config_replacing":
        config = ApprovalServiceConfig.model_validate_json(
            _private_read(config_path.absolute(), maximum=1_048_576)
        )
        index, configured = _configured_owner(config, request)
        if (
            configured.principal_id == request.source_principal_id
            and hashlib.sha256(
                _private_read(config_path.absolute(), maximum=1_048_576)
            ).hexdigest()
            != journal["source_config_sha256"]
        ):
            raise GateBlocked(
                "canonical_owner_recovery", "source config changed during recovery"
            )
        if configured.principal_id == request.source_principal_id:
            replacement = configured.model_copy(
                update={
                    "principal_id": request.target_principal_id,
                    "signer_key_id": str(journal["target_signer_key_id"]),
                    "signer_private_key_path": target_path,
                }
            )
            approvers = list(config.approvers)
            approvers[index] = replacement
            config = config.model_copy(update={"approvers": tuple(approvers)})
            _private_write(
                config_path.absolute(),
                json.dumps(config.model_dump(mode="json"), indent=2, sort_keys=True).encode("utf-8")
                + b"\n",
            )
        elif (
            configured.principal_id != request.target_principal_id
            or configured.signer_key_id != journal["target_signer_key_id"]
            or configured.signer_private_key_path != target_path
        ):
            raise GateBlocked(
                "canonical_owner_recovery", "configured target changed unexpectedly"
            )
        journal["phase"] = "config_replaced"
        _journal_write(journal_path, journal)

    if journal["phase"] == "config_replaced":
        current = ApprovalServiceConfig.model_validate_json(
            _private_read(config_path.absolute(), maximum=1_048_576)
        )
        _index, configured = _configured_owner(current, request)
        signer = P256KeyPair.from_private_pem(
            _private_read(configured.signer_private_key_path, maximum=65_536)
        )
        if (
            configured.principal_id != request.target_principal_id
            or configured.signer_key_id != journal["target_signer_key_id"]
            or signer.thumbprint != journal["target_signer_key_id"]
        ):
            raise GateBlocked("canonical_owner_recovery", "target signer state is incomplete")
        _private_unlink(signer_path)
        _private_unlink(backup_path)
        if _interrupt_after == "retired_signers_removed":
            raise RuntimeError("injected recovery interruption")
        journal["phase"] = "complete"
        journal["completed_at"] = now
        journal = _validate_recovery_journal(
            journal,
            request=request,
            request_digest=request_digest,
            config_path=config_path,
        )
        _journal_write(journal_path, journal)
    _private_unlink(signer_path)
    _private_unlink(backup_path)

    if journal["phase"] != "complete":
        raise GateBlocked("canonical_owner_recovery", "recovery did not converge")
    current = ApprovalServiceConfig.model_validate_json(
        _private_read(config_path.absolute(), maximum=1_048_576)
    )
    _index, configured = _configured_owner(current, request)
    signer = P256KeyPair.from_private_pem(
        _private_read(configured.signer_private_key_path, maximum=65_536)
    )
    if (
        configured.principal_id != request.target_principal_id
        or configured.signer_key_id != journal["target_signer_key_id"]
        or signer.thumbprint != journal["target_signer_key_id"]
    ):
        raise GateBlocked("canonical_owner_recovery", "completed recovery state is invalid")
    return _recovery_result(
        request,
        status="already_exact" if was_complete else "recovered",
        journal=journal,
        adoption=dict(journal["authority_adoption"]),
    )


__all__ = [
    "CanonicalOwnerAdoptionRequest",
    "CanonicalOwnerAdoptionResult",
    "CanonicalOwnerRecoveryReconstruction",
    "CanonicalOwnerRecoveryJournal",
    "CanonicalOwnerRecoveryResult",
    "adopt_canonical_approval_owner",
    "converge_canonical_approval_owner",
    "validate_canonical_owner_adoption_state",
    "validate_canonical_owner_recovery_journal",
]
