"""Scanner contract and deterministic local prefilter.

The prefilter is not malware certification and cannot satisfy the production
scanner gate.  It only rejects obviously executable/archive-dangerous inputs
before an isolated maintained scanner runs.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from typing import Literal, Protocol

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
    field_validator,
)

from agentnet.errors import ValidationError
from agentnet.provenance import ProvenanceReferenceV1, TransformationStep
from agentnet.security.signatures import canonical_json


_DIGEST = re.compile(r"^[a-f0-9]{64}$")


class ArtifactProvenanceV1(BaseModel):
    """Exact manifest-provenance input supported by the v1 artifact API.

    The existing wire shape contains only ``origin``.  The model name is the
    version discriminator while preserving those bytes; another provenance
    shape needs explicit negotiation instead of an open-ended mapping.  This
    label is attribution metadata only and grants no identity or authority.
    """

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    origin: str = Field(min_length=1, max_length=256, pattern=r"^[^\x00-\x1f\x7f]+$")

    @classmethod
    def parse_boundary(cls, value: object) -> "ArtifactProvenanceV1":
        try:
            return cls.model_validate(value, strict=True)
        except PydanticValidationError as exc:
            raise ValidationError("artifact provenance does not match the exact v1 schema") from exc


class ArtifactDerivationV1(BaseModel):
    """Strict authority-neutral lineage submitted for a derived artifact."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"] = "1.0"
    parent_references: tuple[ProvenanceReferenceV1, ...] = Field(min_length=1, max_length=256)
    transformations: tuple[TransformationStep, ...] = Field(min_length=1, max_length=256)

    @field_validator("parent_references")
    @classmethod
    def parents_are_a_canonical_unique_set(
        cls,
        value: tuple[ProvenanceReferenceV1, ...],
    ) -> tuple[ProvenanceReferenceV1, ...]:
        digests = [reference.provenance_digest for reference in value]
        if len(set(digests)) != len(digests):
            raise ValueError("artifact derivation parent references must be unique")
        return tuple(sorted(value, key=lambda reference: reference.provenance_digest))

    @classmethod
    def parse_boundary(cls, value: object) -> "ArtifactDerivationV1":
        try:
            return cls.model_validate(value, strict=True)
        except PydanticValidationError as exc:
            raise ValidationError("artifact derivation does not match the exact v1 schema") from exc


class ArtifactManifestProvenanceV1(BaseModel):
    """Canonical durable artifact-provenance envelope.

    ``client_attribution`` remains an untrusted display label.  Only the exact
    ledger reference, resolved through ``ProvenanceService``, establishes
    artifact lineage.  The envelope deliberately has no caller-controlled
    verification or authority field.
    """

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["1.0"] = "1.0"
    client_attribution: ArtifactProvenanceV1
    ledger_reference: ProvenanceReferenceV1
    authority_effect: Literal["none"] = "none"

    @classmethod
    def parse_storage(cls, value: object) -> "ArtifactManifestProvenanceV1":
        try:
            encoded = value if isinstance(value, (str, bytes)) else canonical_json(value)
            return cls.model_validate_json(encoded, strict=True)
        except PydanticValidationError as exc:
            raise ValidationError(
                "stored artifact provenance does not match the exact v1 schema"
            ) from exc


class ArtifactScanAttestationV1(BaseModel):
    """Strict signed scanner attestation for ``agentnet.artifact.attestation.v1``.

    Parsing proves only that the wire shape is exact.  Trust still requires a
    configured key, valid signature, current policy/profile/time, and exact
    immutable-byte bindings.
    """

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    artifact_id: str = Field(min_length=16, max_length=128)
    classification: Literal["C0", "C1", "C2", "C3"]
    ciphertext_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    expires_at: int = Field(gt=0)
    issued_at: int = Field(gt=0)
    object_key: str = Field(pattern=r"^[a-f0-9]{32}$")
    object_version: str = Field(pattern=r"^[a-f0-9]{64}$")
    plaintext_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    policy_revision: int = Field(ge=1)
    profile_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    result: Literal["allow", "deny", "indeterminate"]
    rules_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    scanner_engine: str = Field(min_length=1, max_length=256)
    scanner_id: str = Field(min_length=1, max_length=256)
    scanner_key_epoch: int = Field(ge=1)
    scanner_version: str = Field(min_length=1, max_length=128)
    signature: str = Field(min_length=1, max_length=4096)

    @classmethod
    def parse_boundary(cls, value: object) -> "ArtifactScanAttestationV1":
        try:
            return cls.model_validate(value, strict=True)
        except PydanticValidationError as exc:
            raise ValidationError("scan attestation does not match the exact v1 schema") from exc

    def signed_fields(self) -> dict[str, object]:
        return self.model_dump(mode="json", exclude={"signature"})


class LocalPrefilterResultV1(BaseModel):
    """Exact result emitted by the deterministic, non-authoritative prefilter."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    artifact_id: str = Field(min_length=1, max_length=128)
    object_version: str = Field(pattern=r"^[a-f0-9]{64}$")
    scanner_id: Literal["agentnet.local-prefilter"]
    scanner_version: Literal["4"]
    rules_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    reason_code: Literal[
        "none",
        "executable_content",
        "uninspected_container",
        "known_malware_test_signature",
        "secret_pattern",
        "media_type_mismatch",
    ]
    result: Literal["deny", "indeterminate"]


@dataclass(frozen=True, slots=True)
class ScannerTrustPolicy:
    """Current scanner evidence requirements.

    Trusted public keys are supplied separately so removing a key immediately
    revokes every outstanding attestation made by that key.  Rules/profile
    pins are optional only for callers that intentionally accept any
    cryptographically trusted scanner configuration; production composition
    should pin both values.
    """

    max_attestation_age_seconds: int = 300
    allowed_future_skew_seconds: int = 30
    required_engine: str | None = None
    required_rules_digest: str | None = None
    required_profile_digest: str | None = None
    revoked_key_epochs: frozenset[tuple[str, int]] = frozenset()

    def __post_init__(self) -> None:
        if not 1 <= self.max_attestation_age_seconds <= 86_400:
            raise ValueError("scanner attestation age is outside the bounded policy")
        if not 0 <= self.allowed_future_skew_seconds <= 300:
            raise ValueError("scanner future skew is outside the bounded policy")
        for value, label in (
            (self.required_rules_digest, "rules"),
            (self.required_profile_digest, "profile"),
        ):
            if value is not None and not _DIGEST.fullmatch(value):
                raise ValueError(f"scanner {label} digest is invalid")
        if self.required_engine is not None and not self.required_engine:
            raise ValueError("scanner engine pin cannot be empty")

    def require_profile(self, attestation: ArtifactScanAttestationV1) -> None:
        scanner_id = attestation.scanner_id
        key_epoch = attestation.scanner_key_epoch
        if (scanner_id, key_epoch) in self.revoked_key_epochs:
            raise ValidationError("scanner key epoch is revoked")
        if self.required_engine is not None and attestation.scanner_engine != self.required_engine:
            raise ValidationError("scanner engine does not match current policy")
        if self.required_rules_digest is not None and attestation.rules_digest != self.required_rules_digest:
            raise ValidationError("scanner rules do not match current policy")
        if self.required_profile_digest is not None and attestation.profile_digest != self.required_profile_digest:
            raise ValidationError("scanner profile does not match current policy")


class ArtifactScanner(Protocol):
    def scan(
        self,
        *,
        artifact_id: str,
        object_version: str,
        content: bytes,
        media_type: str = "application/octet-stream",
    ) -> LocalPrefilterResultV1: ...


class MaintainedArtifactScanner(Protocol):
    """Scanner capable of issuing release-authoritative artifact evidence."""

    def scan(
        self,
        *,
        artifact_id: str,
        classification: Literal["C0", "C1", "C2", "C3"],
        ciphertext_digest: str,
        object_key: str,
        object_version: str,
        plaintext_digest: str,
        policy_revision: int,
        content: bytes,
        issued_at: int,
        expires_at: int,
    ) -> ArtifactScanAttestationV1: ...


class LocalPrefilter:
    scanner_id = "agentnet.local-prefilter"
    scanner_version = "4"
    blocked_magic = (
        b"MZ",  # PE/COFF
        b"\x7fELF",
        b"\xfe\xed\xfa\xce",  # Mach-O, all byte orders and widths
        b"\xce\xfa\xed\xfe",
        b"\xfe\xed\xfa\xcf",
        b"\xcf\xfa\xed\xfe",
        b"\xca\xfe\xba\xbe",  # Java class / Mach-O universal
        b"\x00asm",  # WebAssembly
        b"#!",
    )
    blocked_container_magic = (
        b"PK\x03\x04",  # ZIP and OOXML containers
        b"PK\x05\x06",
        b"PK\x07\x08",
        b"\x1f\x8b",  # gzip
        b"BZh",
        b"\xfd7zXZ\x00",
        b"7z\xbc\xaf'\x1c",
        b"Rar!\x1a\x07",
        b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1",  # OLE compound documents/macros
    )
    eicar = b"X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"
    secret_patterns = (
        re.compile(br"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----"),
        re.compile(br"\bAKIA[A-Z0-9]{16}\b"),
        re.compile(br"\bgh[pousr]_[A-Za-z0-9]{20,}\b"),
        re.compile(br"(?i)\b(?:api[_-]?key|secret|token|password)\s*[:=]\s*[^\s]{8,}"),
    )

    @staticmethod
    def _media_type_mismatch(media_type: str, content: bytes) -> bool:
        if media_type == "application/json":
            try:
                json.loads(content.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError):
                return True
            return False
        if media_type.startswith("text/"):
            try:
                content.decode("utf-8")
            except UnicodeDecodeError:
                return True
            return b"\x00" in content
        signatures = {
            "image/png": (b"\x89PNG\r\n\x1a\n",),
            "image/jpeg": (b"\xff\xd8\xff",),
            "image/gif": (b"GIF87a", b"GIF89a"),
            "application/pdf": (b"%PDF-",),
        }
        expected = signatures.get(media_type)
        return expected is not None and not content.startswith(expected)

    def scan(
        self,
        *,
        artifact_id: str,
        object_version: str,
        content: bytes,
        media_type: str = "application/octet-stream",
    ) -> LocalPrefilterResultV1:
        reason = "none"
        if content.startswith(self.blocked_magic):
            reason = "executable_content"
        elif content.startswith(self.blocked_container_magic):
            reason = "uninspected_container"
        elif self.eicar in content:
            reason = "known_malware_test_signature"
        elif any(pattern.search(content) for pattern in self.secret_patterns):
            reason = "secret_pattern"
        elif self._media_type_mismatch(media_type, content):
            reason = "media_type_mismatch"
        result = "deny" if reason != "none" else "indeterminate"
        return LocalPrefilterResultV1(
            artifact_id=artifact_id,
            object_version=object_version,
            scanner_id=self.scanner_id,
            scanner_version=self.scanner_version,
            rules_digest=hashlib.sha256(b"agentnet-local-prefilter-v4").hexdigest(),
            reason_code=reason,
            result=result,
        )


__all__ = [
    "ArtifactDerivationV1",
    "ArtifactManifestProvenanceV1",
    "ArtifactProvenanceV1",
    "ArtifactScanAttestationV1",
    "ArtifactScanner",
    "LocalPrefilter",
    "MaintainedArtifactScanner",
    "LocalPrefilterResultV1",
    "ScannerTrustPolicy",
]
