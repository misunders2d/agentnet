"""Public versioned schema catalog for corporate objects."""

from __future__ import annotations

from datetime import datetime
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import Classification


class IdentityRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    domain_id: str
    principal_id: str
    harness_id: str
    credential_id: str
    key_id: str
    binding_assurance: str
    credential_epoch: int = Field(ge=1)
    status: Literal["active", "deterministic_only", "quarantined", "revoked"]


class EnrollmentTransaction(BaseModel):
    model_config = ConfigDict(extra="forbid", populate_by_name=True)
    schema_id: Literal["agentnet.enrollment.challenge.v1"] = Field(alias="schema")
    challenge_id: str
    domain_id: str
    human: dict[str, str]
    harness: dict[str, Any]
    candidate_key: dict[str, str]
    nonce: str
    issued_at: int
    expires_at: int
    purpose: Literal["human_harness_credential_binding"]


class RoomRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    room_id: str
    domain_id: str
    owner_domain_id: str
    owner_epoch: int = Field(ge=1)
    control_sequence: int = Field(ge=1)
    state: Literal["active", "frozen", "tombstoned"]
    classification: Classification
    history_mode: Literal["from_join", "none"]
    policy: dict[str, Any]


class ArtifactManifestRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    artifact_id: str
    reservation_id: str
    domain_id: str
    object_key: str
    object_version: str
    ciphertext_digest: str
    size: int = Field(ge=0)
    media_type: str
    classification: Classification
    state: Literal["quarantined", "scan_passed", "held", "released", "deleted", "tombstoned"]
    provenance: dict[str, Any]


class RevocationRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    domain_id: str
    subject_type: Literal["human", "harness", "credential", "guest", "room", "domain", "grant"]
    subject_id: str
    epoch: int = Field(ge=1)
    reason: str
    effective_at: datetime
    compromised_from: datetime | None = None
    adjudicator_id: str | None = None


class AuditIntent(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    intent_id: str
    action: str
    actor: VerifiedActor
    resource_digest: str
    policy_revision: int = Field(ge=1)
    credential_epoch: int = Field(ge=0)
    data_class: Classification
    committed_at: datetime


class ProtocolError(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    code: str
    message: str
    retryable: bool = False
    opaque_reference: str | None = None


class FederationInvitationRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["1.0"] = "1.0"
    invitation_id: str
    host_domain_id: str
    home_domain_id: str
    sponsor_principal_id: str
    pairwise_subject: str
    grant_digest: str
    expires_at: datetime
    non_transitive: Literal[True] = True
