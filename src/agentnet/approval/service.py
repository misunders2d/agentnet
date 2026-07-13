"""Approval verifier implementations.

``LocalLabApprovalVerifier`` is deliberately self-contained and therefore not
independent of the process using it.  It exists only for local conformance tests;
the enrollment service rejects it under the production profile.
"""

from __future__ import annotations

import hashlib
import secrets
import sqlite3
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError, field_validator

from agentnet.errors import AuthenticationError, ReplayError, ValidationError
from agentnet.security.signatures import P256KeyPair, canonical_json, verify_signature


class LocalLabApprovalVerifier:
    """Signed local approval receipts for synthetic lab identities only."""

    lab_only = True
    assurance = "lab"

    def __init__(
        self,
        key_pair: P256KeyPair,
        *,
        verifier_id: str = "agentnet.local-lab-approval",
        clock: Callable[[], int] | None = None,
    ) -> None:
        if not verifier_id or len(verifier_id) > 128:
            raise ValueError("invalid verifier identifier")
        self.key_pair = key_pair
        self.verifier_id = verifier_id
        self.clock = clock or (lambda: int(time.time()))

    def approve(self, *, canonical_transaction: bytes, ttl_seconds: int = 300) -> dict[str, Any]:
        """Create a synthetic receipt; this is not a human approval ceremony."""

        if ttl_seconds < 1 or ttl_seconds > 600:
            raise ValueError("lab approval TTL must be between one and 600 seconds")
        issued_at = self.clock()
        fields: dict[str, Any] = {
            "approved": True,
            "expires_at": issued_at + ttl_seconds,
            "issued_at": issued_at,
            "lab_only": True,
            "nonce": secrets.token_urlsafe(24),
            "receipt_id": str(uuid4()),
            "schema": "agentnet.approval.receipt.v1",
            "transaction_digest": hashlib.sha256(canonical_transaction).hexdigest(),
            "verifier_id": self.verifier_id,
        }
        return {**fields, "signature": self.key_pair.sign("agentnet.approval.receipt.v1", fields)}

    def verify(
        self,
        *,
        canonical_transaction: bytes,
        approval: Mapping[str, Any],
    ) -> Mapping[str, Any]:
        expected_keys = {
            "approved",
            "expires_at",
            "issued_at",
            "lab_only",
            "nonce",
            "receipt_id",
            "schema",
            "signature",
            "transaction_digest",
            "verifier_id",
        }
        if set(approval) != expected_keys:
            raise ValidationError("approval receipt does not match the exact schema")
        signature = approval.get("signature")
        if not isinstance(signature, str):
            raise ValidationError("approval receipt signature is invalid")
        fields = {key: approval[key] for key in expected_keys if key != "signature"}
        expected_digest = hashlib.sha256(canonical_transaction).hexdigest()
        digest = fields.get("transaction_digest")
        if not isinstance(digest, str) or not secrets.compare_digest(digest, expected_digest):
            raise AuthenticationError("approval transaction binding mismatch")
        if fields.get("schema") != "agentnet.approval.receipt.v1":
            raise AuthenticationError("approval receipt profile mismatch")
        if fields.get("verifier_id") != self.verifier_id or fields.get("lab_only") is not True:
            raise AuthenticationError("approval verifier mismatch")
        if fields.get("approved") is not True:
            raise AuthenticationError("transaction was not approved")
        issued_at = fields.get("issued_at")
        expires_at = fields.get("expires_at")
        nonce = fields.get("nonce")
        if not isinstance(issued_at, int) or not isinstance(expires_at, int) or issued_at > self.clock():
            raise AuthenticationError("approval receipt time is invalid")
        if self.clock() >= expires_at:
            raise AuthenticationError("approval receipt is expired")
        if not isinstance(nonce, str) or len(nonce) < 24 or len(nonce) > 256:
            raise AuthenticationError("approval receipt nonce is invalid")
        verify_signature(self.key_pair.public_pem, "agentnet.approval.receipt.v1", fields, signature)
        return dict(approval)


@dataclass(frozen=True, slots=True)
class TrustedApprover:
    """Approval signer admitted by corporate policy, not by request content."""

    principal_id: str
    domain_id: str
    signer_key_id: str
    public_key_pem: str
    allowed_purposes: frozenset[str]
    authority_kind: Literal["human", "guest"] = "human"


@dataclass(frozen=True, slots=True)
class VerifiedIndependentApproval:
    """Cryptographically verified approval fact created only by the verifier."""

    receipt_id: str
    approver_principal_id: str
    approver_authority_kind: Literal["human", "guest"]
    domain_id: str
    approval_purpose: str
    transaction_digest: str
    nonce: str
    issued_at: int
    authenticated_at: int
    expires_at: int
    verifier_id: str
    signer_key_id: str


INDEPENDENT_APPROVAL_SCHEMA = "agentnet.independent-approval.receipt.v1"
INDEPENDENT_APPROVAL_SIGNATURE_PURPOSE = "agentnet.approval.receipt.v1"


class IndependentApprovalReceipt(BaseModel):
    """Strict wire shape for a purpose-bound independent approval receipt.

    Parsing this model never establishes trust.  Only
    :class:`IndependentApprovalVerifier` may turn it into a verified approval
    fact, using its preconfigured trust registry and the exact canonical
    transaction bytes.
    """

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        populate_by_name=True,
        serialize_by_alias=True,
        strict=True,
    )

    approved: Literal[True]
    approval_purpose: str = Field(min_length=1, max_length=256)
    approver_principal_id: str = Field(min_length=1, max_length=256)
    authenticated_at: int
    authentication_method: Literal["webauthn_uv"]
    domain_id: str = Field(min_length=1, max_length=256)
    expires_at: int
    issued_at: int
    nonce: str = Field(min_length=24, max_length=256)
    receipt_id: str = Field(min_length=16, max_length=128)
    schema_: Literal[INDEPENDENT_APPROVAL_SCHEMA] = Field(alias="schema")
    signature: str = Field(min_length=1, max_length=4096)
    signer_key_id: str = Field(min_length=1, max_length=256)
    transaction_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    verifier_id: str = Field(min_length=1, max_length=128)

    @field_validator("approved", mode="before")
    @classmethod
    def approved_is_the_json_boolean_true(cls, value: Any) -> Any:
        if type(value) is not bool or value is not True:
            raise ValueError("approved must be the JSON boolean true")
        return value


def create_independent_approval_receipt(
    signer: P256KeyPair,
    *,
    approver: TrustedApprover,
    verifier_id: str,
    approval_purpose: str,
    canonical_transaction: bytes,
    issued_at: int,
    expires_at: int,
    authenticated_at: int | None = None,
    nonce: str | None = None,
    receipt_id: str | None = None,
) -> dict[str, Any]:
    """Create receipt bytes for an independent approval-service process.

    The helper owns no key and confers no trust.  A verifier accepts the result
    only when ``signer`` corresponds to its preconfigured trusted public key.
    """

    fields: dict[str, Any] = {
        "approved": True,
        "approval_purpose": approval_purpose,
        "approver_principal_id": approver.principal_id,
        "authenticated_at": issued_at if authenticated_at is None else authenticated_at,
        "authentication_method": "webauthn_uv",
        "domain_id": approver.domain_id,
        "expires_at": expires_at,
        "issued_at": issued_at,
        "nonce": nonce or secrets.token_urlsafe(24),
        "receipt_id": receipt_id or str(uuid4()),
        "schema": INDEPENDENT_APPROVAL_SCHEMA,
        "signer_key_id": approver.signer_key_id,
        "transaction_digest": hashlib.sha256(canonical_transaction).hexdigest(),
        "verifier_id": verifier_id,
    }
    return {
        **fields,
        "signature": signer.sign(INDEPENDENT_APPROVAL_SIGNATURE_PURPOSE, fields),
    }


class IndependentApprovalVerifier:
    """Verify purpose-bound receipts against a static corporate trust registry."""

    lab_only = False
    assurance = "independent_webauthn_uv"

    def __init__(
        self,
        trusted_approvers: Mapping[str, TrustedApprover],
        *,
        verifier_id: str,
        max_receipt_ttl_seconds: int = 600,
        max_authentication_age_seconds: int = 300,
    ) -> None:
        if not verifier_id or len(verifier_id) > 128:
            raise ValueError("invalid independent verifier identifier")
        if not trusted_approvers:
            raise ValueError("at least one trusted independent approver is required")
        if max_receipt_ttl_seconds < 1 or max_receipt_ttl_seconds > 600:
            raise ValueError("approval receipt TTL bound must be between one and 600 seconds")
        if max_authentication_age_seconds < 1 or max_authentication_age_seconds > 600:
            raise ValueError("approval authentication age must be between one and 600 seconds")
        registry: dict[str, TrustedApprover] = {}
        for key_id, approver in trusted_approvers.items():
            if key_id != approver.signer_key_id or not approver.allowed_purposes:
                raise ValueError("trusted approver registry is inconsistent")
            registry[key_id] = approver
        self._trusted_approvers = registry
        self.verifier_id = verifier_id
        self.max_receipt_ttl_seconds = max_receipt_ttl_seconds
        self.max_authentication_age_seconds = max_authentication_age_seconds

    def verify(
        self,
        *,
        canonical_transaction: bytes,
        approval: Mapping[str, Any],
        expected_purpose: str,
        expected_domain_id: str,
        when: datetime,
    ) -> VerifiedIndependentApproval:
        if when.tzinfo is None:
            raise ValidationError("approval verification time must be timezone-aware")
        try:
            parsed = IndependentApprovalReceipt.model_validate(approval)
        except PydanticValidationError as exc:
            raise ValidationError(
                "independent approval receipt does not match the exact schema"
            ) from exc
        exact = parsed.model_dump(mode="python", by_alias=True)
        signature = exact.pop("signature")
        fields = exact
        if fields["schema"] != INDEPENDENT_APPROVAL_SCHEMA or fields["verifier_id"] != self.verifier_id:
            raise AuthenticationError("independent approval verifier profile mismatch")
        if fields["approved"] is not True:
            raise AuthenticationError("independent approval was not granted")
        if fields["approval_purpose"] != expected_purpose or fields["domain_id"] != expected_domain_id:
            raise AuthenticationError("independent approval purpose or domain mismatch")
        if fields["authentication_method"] != "webauthn_uv":
            raise AuthenticationError("independent approval lacks phishing-resistant user verification")

        signer_key_id = fields["signer_key_id"]
        approver_principal_id = fields["approver_principal_id"]
        if not isinstance(signer_key_id, str) or not isinstance(approver_principal_id, str):
            raise ValidationError("independent approval signer identity is invalid")
        trusted = self._trusted_approvers.get(signer_key_id)
        if (
            trusted is None
            or trusted.principal_id != approver_principal_id
            or trusted.domain_id != expected_domain_id
            or expected_purpose not in trusted.allowed_purposes
        ):
            raise AuthenticationError("approval signer is not trusted for this purpose")

        expected_digest = hashlib.sha256(canonical_transaction).hexdigest()
        digest = fields["transaction_digest"]
        if not isinstance(digest, str) or not secrets.compare_digest(digest, expected_digest):
            raise AuthenticationError("independent approval transaction binding mismatch")

        issued_at = fields["issued_at"]
        authenticated_at = fields["authenticated_at"]
        expires_at = fields["expires_at"]
        now = int(when.timestamp())
        if type(issued_at) is not int or type(authenticated_at) is not int or type(expires_at) is not int:
            raise ValidationError("independent approval timestamps are invalid")
        if authenticated_at > issued_at or issued_at > now:
            raise AuthenticationError("independent approval time ordering is invalid")
        if issued_at - authenticated_at > self.max_authentication_age_seconds:
            raise AuthenticationError("independent approval authentication is stale")
        if expires_at <= issued_at or expires_at - issued_at > self.max_receipt_ttl_seconds or now >= expires_at:
            raise AuthenticationError("independent approval receipt is expired or overlong")

        nonce = fields["nonce"]
        receipt_id = fields["receipt_id"]
        if not isinstance(nonce, str) or len(nonce) < 24 or len(nonce) > 256:
            raise ValidationError("independent approval nonce is invalid")
        if not isinstance(receipt_id, str) or len(receipt_id) < 16 or len(receipt_id) > 128:
            raise ValidationError("independent approval receipt identifier is invalid")

        verify_signature(
            trusted.public_key_pem,
            INDEPENDENT_APPROVAL_SIGNATURE_PURPOSE,
            fields,
            signature,
        )
        return VerifiedIndependentApproval(
            receipt_id=receipt_id,
            approver_principal_id=approver_principal_id,
            approver_authority_kind=trusted.authority_kind,
            domain_id=expected_domain_id,
            approval_purpose=expected_purpose,
            transaction_digest=digest,
            nonce=nonce,
            issued_at=issued_at,
            authenticated_at=authenticated_at,
            expires_at=expires_at,
            verifier_id=self.verifier_id,
            signer_key_id=signer_key_id,
        )


def consume_independent_approval(
    connection: sqlite3.Connection,
    *,
    receipt: VerifiedIndependentApproval,
    retain_until: int | None = None,
) -> None:
    """Consume a verified receipt once inside the protected mutation."""

    actor_id, replay_key, expires_at = independent_approval_replay_binding(
        receipt,
        retain_until=retain_until,
    )
    try:
        connection.execute(
            "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
            (actor_id, replay_key, expires_at),
        )
    except Exception as exc:
        if isinstance(exc, sqlite3.IntegrityError) or exc.__class__.__name__ == "UniqueViolation":
            raise ReplayError("independent approval receipt was already consumed") from exc
        raise


def independent_approval_replay_binding(
    receipt: VerifiedIndependentApproval,
    *,
    retain_until: int | None = None,
) -> tuple[str, str, int]:
    """Return the only durable replay-fence key for a verified approval.

    Persisted relationship authority reuses this exact derivation to prove
    that the receipt was consumed by the purpose-bound approval machinery,
    rather than trusting caller-populated receipt columns on an active row.
    """

    replay_key = hashlib.sha256(
        canonical_json(
            {
                "approval_purpose": receipt.approval_purpose,
                "nonce": receipt.nonce,
                "receipt_id": receipt.receipt_id,
                "transaction_digest": receipt.transaction_digest,
                "verifier_id": receipt.verifier_id,
            }
        )
    ).hexdigest()
    durable_until = receipt.expires_at
    if retain_until is not None:
        if type(retain_until) is not int or retain_until < receipt.expires_at:
            raise ValidationError(
                "approval replay retention cannot precede receipt expiry"
            )
        durable_until = retain_until
    return f"approval:{receipt.verifier_id}", replay_key, durable_until
