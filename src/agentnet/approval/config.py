"""Strict configuration for the dedicated WebAuthn approval service."""

from __future__ import annotations

import json
import os
import stat
from pathlib import Path
from typing import Literal
from urllib.parse import urlsplit

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.errors import GateBlocked, ValidationError
from agentnet.security.signatures import P256KeyPair


def _reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def _reject_nonfinite(_value: str) -> object:
    raise ValueError("non-finite JSON number")


MANDATORY_APPROVAL_PURPOSES = frozenset(
    {
        "authorization.entitlement.bootstrap.approve",
        "authorization.elevation.approve",
        "identity.credential.recover.approve",
        "identity.enrollment.approve",
        "identity.harness.revoke.approve",
        "organization.relationship.accept",
    }
)


class ApprovalServiceApproverConfig(BaseModel):
    """One human/guest approver and its service-local receipt signer reference."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    principal_id: str = Field(min_length=1, max_length=256)
    authority_kind: Literal["human", "guest"] = "human"
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    signer_key_id: str = Field(min_length=16, max_length=256)
    signer_private_key_path: Path
    allowed_purposes: frozenset[str] = Field(min_length=1, max_length=32)

    @model_validator(mode="after")
    def validate_approver(self) -> "ApprovalServiceApproverConfig":
        path = self.signer_private_key_path
        if not path.is_absolute() or ".." in path.parts:
            raise ValueError("approval signer path must be an absolute canonical reference")
        if any(
            not purpose
            or len(purpose) > 256
            or purpose != purpose.strip()
            or any(ord(character) < 0x21 for character in purpose)
            for purpose in self.allowed_purposes
        ):
            raise ValueError("approval purpose is invalid")
        return self


class ApprovalServiceConfig(BaseModel):
    """Independent service configuration; contains paths, never private key bytes."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    public_origin: str = Field(min_length=8, max_length=2048)
    rp_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,253}$")
    rp_name: str = Field(default="AgentNet Approval", min_length=1, max_length=128)
    verifier_id: str = Field(min_length=1, max_length=128)
    data_dir: Path
    database_path: Path
    record_key_path: Path
    request_ttl_seconds: int = Field(default=300, ge=30, le=600)
    challenge_ttl_seconds: int = Field(default=180, ge=30, le=600)
    receipt_ttl_seconds: int = Field(default=300, ge=30, le=600)
    registration_ttl_seconds: int = Field(default=600, ge=60, le=900)
    max_transaction_bytes: int = Field(default=65_536, ge=1024, le=1_048_576)
    max_http_body_bytes: int = Field(default=131_072, ge=4096, le=1_048_576)
    internal_core_credential_env: str | None = Field(
        default=None,
        pattern=r"^[A-Z_][A-Z0-9_]{0,127}$",
    )
    approvers: tuple[ApprovalServiceApproverConfig, ...] = Field(min_length=1, max_length=32)

    @model_validator(mode="after")
    def validate_service(self) -> "ApprovalServiceConfig":
        try:
            parsed = urlsplit(self.public_origin)
            port = parsed.port
        except ValueError as exc:
            raise ValueError("approval public_origin is invalid") from exc
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
        ):
            raise ValueError("approval public_origin must be one exact HTTPS origin")
        hostname = parsed.hostname.lower()
        rendered_host = f"[{hostname}]" if ":" in hostname else hostname
        canonical = f"https://{rendered_host}"
        if port not in {None, 443}:
            canonical += f":{port}"
        if self.public_origin.rstrip("/") != canonical:
            raise ValueError("approval public_origin must use canonical spelling")
        if hostname != self.rp_id:
            raise ValueError("approval RP ID must equal the exact public-origin hostname")
        if self.challenge_ttl_seconds > self.request_ttl_seconds:
            raise ValueError("approval challenge TTL cannot exceed request TTL")

        paths = (self.data_dir, self.database_path, self.record_key_path)
        if any(not path.is_absolute() or ".." in path.parts for path in paths):
            raise ValueError("approval service paths must be absolute canonical references")
        if len(set(paths)) != len(paths):
            raise ValueError("approval service paths must be distinct")
        if self.database_path.parent != self.data_dir:
            raise ValueError("approval database must live directly in the dedicated data directory")
        if self.record_key_path.parent != self.data_dir / "secrets":
            raise ValueError("approval record key must live in the dedicated secrets directory")

        principals = [(item.domain_id, item.principal_id) for item in self.approvers]
        key_ids = [item.signer_key_id for item in self.approvers]
        signer_paths = [item.signer_private_key_path for item in self.approvers]
        if len(set(principals)) != len(principals):
            raise ValueError("approval approver identities must be unique")
        if len(set(key_ids)) != len(key_ids) or len(set(signer_paths)) != len(signer_paths):
            raise ValueError("approval signer keys and paths must be unique")
        if any(
            "identity.enrollment.approve" not in item.allowed_purposes
            for item in self.approvers
        ):
            raise ValueError("every configured approver must cover enrollment")
        configured = frozenset().union(*(item.allowed_purposes for item in self.approvers))
        missing = MANDATORY_APPROVAL_PURPOSES - configured
        if missing:
            raise ValueError(
                "approval service does not cover every mandatory ceremony: "
                + ", ".join(sorted(missing))
            )
        return self

    def approver(self, principal_id: str, domain_id: str | None = None) -> ApprovalServiceApproverConfig:
        matches = [
            item
            for item in self.approvers
            if item.principal_id == principal_id
            and (domain_id is None or item.domain_id == domain_id)
        ]
        if len(matches) != 1:
            raise ValidationError("approval approver is unavailable")
        return matches[0]


def require_owner_only_file(path: Path, *, label: str, max_bytes: int = 1_048_576) -> bytes:
    """Read one bounded owner-only regular file without following a final symlink."""

    if not path.is_absolute() or path.is_symlink() or path.parent.is_symlink():
        raise GateBlocked("approval_custody", f"{label} must be an absolute non-symlink file")
    try:
        parent = path.parent.stat()
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise GateBlocked("approval_custody", f"{label} is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            parent.st_uid != os.geteuid()
            or parent.st_mode & 0o077
            or not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or not 1 <= metadata.st_size <= max_bytes
        ):
            raise GateBlocked("approval_custody", f"{label} must be a bounded owner-only file")
        chunks: list[bytes] = []
        remaining = metadata.st_size
        while remaining:
            chunk = os.read(descriptor, min(remaining, 65_536))
            if not chunk:
                raise GateBlocked("approval_custody", f"{label} changed while being read")
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)
    finally:
        os.close(descriptor)


def load_approval_service_config(path: Path) -> ApprovalServiceConfig:
    raw = require_owner_only_file(path.absolute(), label="approval service configuration")
    try:
        value = json.loads(
            raw.decode("utf-8"),
            object_pairs_hook=_reject_duplicate_keys,
            parse_constant=_reject_nonfinite,
        )
        if not isinstance(value, dict):
            raise ValueError("not an object")
        config = ApprovalServiceConfig.model_validate(value)
    except Exception as exc:
        raise ValidationError("approval service configuration is invalid") from exc
    require_owner_only_file(config.record_key_path, label="approval record key", max_bytes=32)
    for item in config.approvers:
        pem = require_owner_only_file(
            item.signer_private_key_path,
            label=f"approval signer {item.principal_id}",
            max_bytes=16_384,
        )
        signer = P256KeyPair.from_private_pem(pem)
        if signer.thumbprint != item.signer_key_id:
            raise GateBlocked("approval_custody", "approval signer key identifier mismatch")
    return config


__all__ = [
    "ApprovalServiceApproverConfig",
    "ApprovalServiceConfig",
    "MANDATORY_APPROVAL_PURPOSES",
    "load_approval_service_config",
    "require_owner_only_file",
]
