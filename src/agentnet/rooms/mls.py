"""Validated maintained-MLS adoption and provider boundary.

The application never treats a feature flag or an evidence string as proof
that sealed-room cryptography is available.  A C3 caller must provide both a
currently valid, owner-signed adoption record and a live provider whose exact
identity/version matches that record.
"""

from __future__ import annotations

import time
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field, field_validator

from agentnet.errors import AuthorizationError, GateBlocked
from agentnet.security.signatures import verify_signature


REQUIRED_MLS_ADOPTION_GATES = frozenset({"G12", "G19", "PD-007"})


class MLSGroupBinding(BaseModel):
    """Provider-owned group identity returned after durable group creation."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    provider_id: str = Field(min_length=1, max_length=128)
    provider_version: str = Field(min_length=1, max_length=128)
    room_id: str = Field(min_length=1, max_length=256)
    group_id: str = Field(min_length=1, max_length=512)
    epoch: int = Field(ge=1)


class MLSAdoptionRecord(BaseModel):
    """Owner-signed result of the maintained-component adoption gate."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    provider_id: str = Field(min_length=1, max_length=128)
    provider_version: str = Field(min_length=1, max_length=128)
    decision_id: str = Field(min_length=16, max_length=256)
    evidence_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    approved_by: str = Field(min_length=1, max_length=256)
    required_gates: tuple[str, ...]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    signature: str = Field(min_length=1)

    @field_validator("required_gates")
    @classmethod
    def canonical_gate_set(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if value != tuple(sorted(set(value))):
            raise ValueError("MLS adoption gates must be unique and sorted")
        return value

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude={"signature"})


class MLSProvider(Protocol):
    @property
    def provider_id(self) -> str: ...

    @property
    def provider_version(self) -> str: ...

    def healthy(self) -> bool: ...

    def create_group(self, room_id: str, members: tuple[str, ...]) -> MLSGroupBinding: ...

    def add_member(self, room_id: str, member_id: str, key_package: bytes) -> MLSGroupBinding: ...

    def remove_member(self, room_id: str, member_id: str) -> MLSGroupBinding: ...

    def encrypt(self, room_id: str, plaintext: bytes) -> bytes: ...

    def decrypt(self, room_id: str, ciphertext: bytes) -> bytes: ...


_VALIDATED = object()


class ValidatedMLSAdoption:
    """Opaque capability created only after signature, gate, and provider checks."""

    __slots__ = ("record",)

    def __init__(self, record: MLSAdoptionRecord, token: object) -> None:
        if token is not _VALIDATED:
            raise TypeError("validated MLS adoption must come from validate_mls_adoption")
        self.record = record

    def require_current(self, provider: MLSProvider, *, now: int | None = None) -> None:
        current = int(time.time()) if now is None else now
        if current < self.record.issued_at or current >= self.record.expires_at:
            raise GateBlocked("G12/G19/PD-007", "MLS adoption record is outside its validity interval")
        if provider.provider_id != self.record.provider_id or provider.provider_version != self.record.provider_version:
            raise GateBlocked("G12/G19", "MLS provider identity/version differs from the adopted component")
        if not provider.healthy():
            raise GateBlocked("G12/G19", "adopted MLS provider is unavailable")


def validate_mls_adoption(
    record: MLSAdoptionRecord,
    *,
    owner_public_key_pem: str,
    provider: MLSProvider,
    now: int | None = None,
) -> ValidatedMLSAdoption:
    """Verify exact owner evidence and bind it to the live provider instance."""

    current = int(time.time()) if now is None else now
    if not REQUIRED_MLS_ADOPTION_GATES.issubset(record.required_gates):
        raise AuthorizationError("MLS adoption lacks every mandatory gate")
    if current < record.issued_at or current >= record.expires_at:
        raise AuthorizationError("MLS adoption record is outside its validity interval")
    verify_signature(owner_public_key_pem, "agentnet.mls.adoption.v1", record.signed_fields(), record.signature)
    adoption = ValidatedMLSAdoption(record, _VALIDATED)
    adoption.require_current(provider, now=current)
    return adoption


class UnavailableMLSProvider:
    @property
    def provider_id(self) -> str:
        return "unavailable"

    @property
    def provider_version(self) -> str:
        return "0"

    def healthy(self) -> bool:
        return False

    def __getattr__(self, _name: str):
        raise GateBlocked("G12/G19/PD-007", "sealed rooms require a selected maintained MLS implementation and owner policy")
