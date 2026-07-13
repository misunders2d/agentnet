"""Append-only, authority-neutral content provenance and taint tracking.

Provenance records are evidence.  They never create an entitlement, grant,
relationship, task capability, tool permission, or effect authority.  Every
mutation is an append to one object/version stream, and every clean result is
backed by two independently verified, purpose-bound receipts for the exact
canonical clearance transaction.
"""

from __future__ import annotations

import secrets
from collections.abc import Callable
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.approval.service import (
    IndependentApprovalReceipt,
    IndependentApprovalVerifier,
    VerifiedIndependentApproval,
    consume_independent_approval,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.post_audit_schema import require_post_audit_schema


SHA256_PATTERN = r"^[a-f0-9]{64}$"
IDENTIFIER_PATTERN = r"^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$"
REVIEW_CLEARANCE_PURPOSE = "provenance.review.clear-taint"
SCAN_CLEARANCE_PURPOSE = "provenance.scan.clear-taint"


class _StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)


class ProvenanceObjectType(StrEnum):
    EVENT = "event"
    TASK = "task"
    ARTIFACT = "artifact"
    MODEL_OUTPUT = "model_output"
    TOOL_OUTPUT = "tool_output"
    PARSER_OUTPUT = "parser_output"


class OriginKind(StrEnum):
    HUMAN_INPUT = "human_input"
    INTERNAL_EVENT = "internal_event"
    ARTIFACT = "artifact"
    EXTERNAL_INPUT = "external_input"
    SYSTEM = "system"
    DERIVED = "derived"


class TransformationKind(StrEnum):
    MODEL = "model"
    TOOL = "tool"
    PARSER = "parser"


class ReviewState(StrEnum):
    UNREVIEWED = "unreviewed"
    REVIEWED = "reviewed"
    REJECTED = "rejected"
    QUARANTINED = "quarantined"


class ScanState(StrEnum):
    NOT_REQUIRED = "not_required"
    PENDING = "pending"
    PASSED = "passed"
    FAILED = "failed"
    STALE = "stale"


def _require_aware_second(value: datetime, label: str) -> datetime:
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError(f"{label} must be timezone-aware")
    if value.microsecond:
        raise ValueError(f"{label} must have whole-second precision")
    return value.astimezone(UTC)


def _canonical_model(value: BaseModel) -> str:
    return canonical_json(value.model_dump(mode="json", by_alias=True)).decode("utf-8")


class ProvenanceOrigin(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    kind: OriginKind
    source_id: str = Field(pattern=IDENTIFIER_PATTERN)
    source_digest: str = Field(pattern=SHA256_PATTERN)
    principal_id: str | None = Field(default=None, min_length=1, max_length=256)
    harness_id: str | None = Field(default=None, min_length=1, max_length=256)
    observed_at: datetime

    @field_validator("observed_at")
    @classmethod
    def observed_at_is_canonical(cls, value: datetime) -> datetime:
        return _require_aware_second(value, "origin observation time")

    @model_validator(mode="after")
    def source_identity_is_coherent(self) -> "ProvenanceOrigin":
        if self.kind is OriginKind.HUMAN_INPUT and (
            self.principal_id is None or self.harness_id is None
        ):
            raise ValueError("human provenance requires principal and harness identity")
        if self.kind is not OriginKind.HUMAN_INPUT and self.principal_id is not None:
            raise ValueError("only human-input origin may bind a principal")
        if self.kind is OriginKind.DERIVED and self.harness_id is not None:
            raise ValueError("derived origin identity comes only from its parent digests")
        return self


class TransformationStep(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    kind: TransformationKind
    operation_id: str = Field(pattern=IDENTIFIER_PATTERN)
    implementation_id: str = Field(pattern=IDENTIFIER_PATTERN)
    implementation_version: str = Field(min_length=1, max_length=128)
    executor_harness_id: str = Field(min_length=1, max_length=256)
    input_digests: tuple[str, ...] = Field(min_length=1, max_length=256)
    output_digest: str = Field(pattern=SHA256_PATTERN)
    started_at: datetime
    completed_at: datetime

    @field_validator("input_digests")
    @classmethod
    def input_digests_are_a_canonical_set(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if any(len(item) != 64 or any(ch not in "0123456789abcdef" for ch in item) for item in value):
            raise ValueError("transformation input digest is not SHA-256")
        if len(set(value)) != len(value):
            raise ValueError("transformation input digests must be unique")
        return tuple(sorted(value))

    @field_validator("started_at", "completed_at")
    @classmethod
    def times_are_canonical(cls, value: datetime) -> datetime:
        return _require_aware_second(value, "transformation time")

    @model_validator(mode="after")
    def time_order_is_valid(self) -> "TransformationStep":
        if self.completed_at < self.started_at:
            raise ValueError("transformation completion precedes its start")
        return self


class EvidenceBinding(_StrictModel):
    """Audit binding copied from a receipt only after strict schema parsing.

    Trust is still established solely by ``IndependentApprovalVerifier``.
    This model intentionally contains no caller-provided verified flag.
    """

    schema_version: Literal["1.0"] = "1.0"
    purpose: str = Field(min_length=1, max_length=256)
    receipt: IndependentApprovalReceipt
    receipt_id: str = Field(min_length=16, max_length=128)
    receipt_digest: str = Field(pattern=SHA256_PATTERN)
    approver_principal_id: str = Field(min_length=1, max_length=256)
    verifier_id: str = Field(min_length=1, max_length=128)
    signer_key_id: str = Field(min_length=1, max_length=256)
    transaction_digest: str = Field(pattern=SHA256_PATTERN)
    issued_at: int
    expires_at: int


class TransformationChain(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    output_digest: str = Field(pattern=SHA256_PATTERN)
    steps: tuple[TransformationStep, ...] = Field(max_length=256)
    review_evidence: EvidenceBinding | None = None
    scan_evidence: EvidenceBinding | None = None


class ParentDigestSet(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    digests: tuple[str, ...] = Field(max_length=256)

    @field_validator("digests")
    @classmethod
    def canonical_digests(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if any(len(item) != 64 or any(ch not in "0123456789abcdef" for ch in item) for item in value):
            raise ValueError("parent provenance digest is not SHA-256")
        if len(set(value)) != len(value):
            raise ValueError("parent provenance digests must be unique")
        return tuple(sorted(value))


class SinkSet(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    sinks: tuple[str, ...] = Field(max_length=256)

    @field_validator("sinks")
    @classmethod
    def canonical_sinks(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if any(
            item != "*"
            and (
                not item
                or len(item) > 256
                or any(ch not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:/@+-" for ch in item)
            )
            for item in value
        ):
            raise ValueError("allowed sink identifier is invalid")
        if len(set(value)) != len(value):
            raise ValueError("allowed sinks must be unique")
        if "*" in value and len(value) != 1:
            raise ValueError("wildcard sink cannot be combined with explicit sinks")
        return tuple(sorted(value))


class ProvenanceReferenceV1(_StrictModel):
    """Exact authority-neutral reference to one durable provenance version.

    A reference is useful only after :meth:`ProvenanceService.require_reference`
    resolves it against the authoritative ledger.  Every copied constraint is
    compared with that record; none is a caller assertion that can authorize an
    operation.  In particular, this type deliberately has no ``verified``,
    entitlement, grant, approval, or actor field.
    """

    schema_version: Literal["1.0"] = "1.0"
    object_type: ProvenanceObjectType
    object_id: str = Field(pattern=IDENTIFIER_PATTERN)
    version: int = Field(ge=1)
    domain_id: str = Field(min_length=1, max_length=256)
    provenance_digest: str = Field(pattern=SHA256_PATTERN)
    content_digest: str = Field(pattern=SHA256_PATTERN)
    classification: Classification
    allowed_sinks: SinkSet
    policy_revision: int = Field(ge=1)
    review_state: ReviewState
    scan_state: ScanState
    tainted: bool
    authority_effect: Literal["none"] = "none"


class ProvenanceRecord(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    object_type: ProvenanceObjectType
    object_id: str = Field(pattern=IDENTIFIER_PATTERN)
    version: int = Field(ge=1)
    domain_id: str = Field(min_length=1, max_length=256)
    origin: ProvenanceOrigin
    transformations: TransformationChain
    parent_digests: ParentDigestSet
    review_state: ReviewState
    scan_state: ScanState
    classification: Classification
    allowed_sinks: SinkSet
    policy_revision: int = Field(ge=1)
    tainted: bool
    provenance_digest: str = Field(pattern=SHA256_PATTERN)
    created_at: datetime

    @field_validator("created_at")
    @classmethod
    def created_at_is_canonical(cls, value: datetime) -> datetime:
        return _require_aware_second(value, "provenance creation time")

    def digest_fields(self) -> dict[str, Any]:
        return {
            "schema_version": self.schema_version,
            "object_type": self.object_type.value,
            "object_id": self.object_id,
            "version": self.version,
            "domain_id": self.domain_id,
            "origin": self.origin.model_dump(mode="json"),
            "transformations": self.transformations.model_dump(mode="json"),
            "parent_digests": self.parent_digests.model_dump(mode="json"),
            "review_state": self.review_state.value,
            "scan_state": self.scan_state.value,
            "classification": self.classification.value,
            "allowed_sinks": self.allowed_sinks.model_dump(mode="json"),
            "policy_revision": self.policy_revision,
            "tainted": self.tainted,
            "created_at": self.created_at.isoformat(),
        }

    def computed_digest(self) -> str:
        return canonical_digest(self.digest_fields())

    @property
    def content_digest(self) -> str:
        return self.transformations.output_digest

    def reference(self) -> ProvenanceReferenceV1:
        """Copy the exact constraint-bearing fields into a strict reference."""

        return ProvenanceReferenceV1(
            object_type=self.object_type,
            object_id=self.object_id,
            version=self.version,
            domain_id=self.domain_id,
            provenance_digest=self.provenance_digest,
            content_digest=self.content_digest,
            classification=self.classification,
            allowed_sinks=self.allowed_sinks,
            policy_revision=self.policy_revision,
            review_state=self.review_state,
            scan_state=self.scan_state,
            tainted=self.tainted,
        )


class OriginRegistration(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    object_type: ProvenanceObjectType
    object_id: str = Field(pattern=IDENTIFIER_PATTERN)
    domain_id: str = Field(min_length=1, max_length=256)
    origin: ProvenanceOrigin
    classification: Classification
    allowed_sinks: SinkSet
    policy_revision: int = Field(ge=1)
    recorded_at: datetime

    @field_validator("recorded_at")
    @classmethod
    def recorded_at_is_canonical(cls, value: datetime) -> datetime:
        return _require_aware_second(value, "provenance record time")

    @model_validator(mode="after")
    def registration_is_an_actual_origin(self) -> "OriginRegistration":
        if self.origin.kind is OriginKind.DERIVED:
            raise ValueError("derived origin must be created through the derivation API")
        if self.origin.observed_at > self.recorded_at:
            raise ValueError("origin observation follows provenance recording")
        return self


class ProvenanceDerivation(_StrictModel):
    schema_version: Literal["1.0"] = "1.0"
    object_type: ProvenanceObjectType
    object_id: str = Field(pattern=IDENTIFIER_PATTERN)
    domain_id: str = Field(min_length=1, max_length=256)
    expected_previous_version: int = Field(ge=0)
    parent_digests: ParentDigestSet
    transformations: tuple[TransformationStep, ...] = Field(max_length=256)
    output_digest: str = Field(pattern=SHA256_PATTERN)
    classification: Classification
    allowed_sinks: SinkSet
    policy_revision: int = Field(ge=1)
    review_state: ReviewState = ReviewState.UNREVIEWED
    scan_state: ScanState = ScanState.PENDING
    tainted: bool = True
    review_approval: IndependentApprovalReceipt | None = None
    scan_approval: IndependentApprovalReceipt | None = None
    recorded_at: datetime

    @field_validator("recorded_at")
    @classmethod
    def recorded_at_is_canonical(cls, value: datetime) -> datetime:
        return _require_aware_second(value, "provenance record time")

    @model_validator(mode="after")
    def evidence_shape_is_exact(self) -> "ProvenanceDerivation":
        if not self.parent_digests.digests:
            raise ValueError("derivation requires at least one exact parent provenance digest")
        # An unsigned command is a useful exact subject for an independent
        # review/scan ceremony.  ``derive`` still refuses to persist a reviewed
        # or passed state until the corresponding receipt verifies.  A receipt
        # attached to any other state is structurally incoherent.
        if self.review_approval is not None and self.review_state is not ReviewState.REVIEWED:
            raise ValueError("review receipt requires reviewed state")
        if self.scan_approval is not None and self.scan_state is not ScanState.PASSED:
            raise ValueError("scan receipt requires passed state")
        if not self.tainted and (
            self.review_state is not ReviewState.REVIEWED
            or self.scan_state is not ScanState.PASSED
        ):
            raise ValueError("clean derivation requires current reviewed and passed evidence")
        if self.review_state in {ReviewState.REJECTED, ReviewState.QUARANTINED} and not self.tainted:
            raise ValueError("rejected or quarantined provenance remains tainted")
        if self.scan_state in {ScanState.FAILED, ScanState.STALE} and not self.tainted:
            raise ValueError("failed or stale scan provenance remains tainted")
        if (
            self.review_approval is not None
            and self.scan_approval is not None
            and self.review_approval.receipt_id == self.scan_approval.receipt_id
        ):
            raise ValueError("review and scan require distinct purpose-bound receipts")
        return self

    def clearance_transaction(self) -> dict[str, Any]:
        """Exact authority-neutral subject signed by review and scan services."""

        return {
            "schema": "agentnet.provenance-clearance.v1",
            "domain_id": self.domain_id,
            "object_type": self.object_type.value,
            "object_id": self.object_id,
            "version": self.expected_previous_version + 1,
            "parent_digests": list(self.parent_digests.digests),
            "transformations": [step.model_dump(mode="json") for step in self.transformations],
            "output_digest": self.output_digest,
            "classification": self.classification.value,
            "allowed_sinks": list(self.allowed_sinks.sinks),
            "policy_revision": self.policy_revision,
            "review_state": self.review_state.value,
            "scan_state": self.scan_state.value,
            "tainted": self.tainted,
            "recorded_at": self.recorded_at.isoformat(),
            "authority_effect": "none",
        }

    def canonical_clearance_transaction(self) -> bytes:
        return canonical_json(self.clearance_transaction())


def _classification_rank(value: Classification) -> int:
    return {
        Classification.C0_PUBLIC: 0,
        Classification.C1_INTERNAL: 1,
        Classification.C2_RESTRICTED: 2,
        Classification.C3_SEALED: 3,
    }[value]


def _binding_from_receipt(receipt: IndependentApprovalReceipt) -> EvidenceBinding:
    exact = receipt.model_dump(mode="json", by_alias=True)
    return EvidenceBinding(
        purpose=receipt.approval_purpose,
        receipt=receipt,
        receipt_id=receipt.receipt_id,
        receipt_digest=canonical_digest(exact),
        approver_principal_id=receipt.approver_principal_id,
        verifier_id=receipt.verifier_id,
        signer_key_id=receipt.signer_key_id,
        transaction_digest=receipt.transaction_digest,
        issued_at=receipt.issued_at,
        expires_at=receipt.expires_at,
    )


class ProvenanceService:
    """Durable provenance ledger with monotonic derivation semantics."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        evidence_verifier: IndependentApprovalVerifier | None = None,
    ) -> None:
        self.store = store
        self.evidence_verifier = evidence_verifier
        require_post_audit_schema(store)

    @staticmethod
    def _require_domain(
        connection: Any,
        *,
        domain_id: str,
        policy_revision: int,
    ) -> Any:
        row = connection.execute(
            "SELECT status,policy_revision FROM domains WHERE domain_id=?", (domain_id,)
        ).fetchone()
        if row is None or row["status"] != "active":
            raise AuthorizationError("provenance domain is not active")
        if int(row["policy_revision"]) != policy_revision:
            raise ConflictError("provenance policy revision drifted")
        return row

    @staticmethod
    def _new_record(
        *,
        object_type: ProvenanceObjectType,
        object_id: str,
        version: int,
        domain_id: str,
        origin: ProvenanceOrigin,
        transformations: TransformationChain,
        parent_digests: ParentDigestSet,
        review_state: ReviewState,
        scan_state: ScanState,
        classification: Classification,
        allowed_sinks: SinkSet,
        policy_revision: int,
        tainted: bool,
        created_at: datetime,
    ) -> ProvenanceRecord:
        placeholder = "0" * 64
        draft = ProvenanceRecord(
            object_type=object_type,
            object_id=object_id,
            version=version,
            domain_id=domain_id,
            origin=origin,
            transformations=transformations,
            parent_digests=parent_digests,
            review_state=review_state,
            scan_state=scan_state,
            classification=classification,
            allowed_sinks=allowed_sinks,
            policy_revision=policy_revision,
            tainted=tainted,
            provenance_digest=placeholder,
            created_at=created_at,
        )
        return draft.model_copy(update={"provenance_digest": draft.computed_digest()})

    @staticmethod
    def _insert(connection: Any, record: ProvenanceRecord) -> None:
        connection.execute(
            """INSERT INTO content_provenance(
                   object_type,object_id,version,domain_id,origin_json,
                   transformations_json,parent_digests_json,review_state,scan_state,
                   classification,allowed_sinks_json,policy_revision,tainted,
                   provenance_digest,created_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                record.object_type.value,
                record.object_id,
                record.version,
                record.domain_id,
                _canonical_model(record.origin),
                _canonical_model(record.transformations),
                _canonical_model(record.parent_digests),
                record.review_state.value,
                record.scan_state.value,
                record.classification.value,
                _canonical_model(record.allowed_sinks),
                record.policy_revision,
                int(record.tainted),
                record.provenance_digest,
                int(record.created_at.timestamp()),
            ),
        )

    @staticmethod
    def _record_from_row(row: Any) -> ProvenanceRecord:
        try:
            origin = ProvenanceOrigin.model_validate_json(str(row["origin_json"]), strict=True)
            transformations = TransformationChain.model_validate_json(
                str(row["transformations_json"]), strict=True
            )
            parents = ParentDigestSet.model_validate_json(
                str(row["parent_digests_json"]), strict=True
            )
            sinks = SinkSet.model_validate_json(str(row["allowed_sinks_json"]), strict=True)
            record = ProvenanceRecord(
                object_type=ProvenanceObjectType(str(row["object_type"])),
                object_id=str(row["object_id"]),
                version=int(row["version"]),
                domain_id=str(row["domain_id"]),
                origin=origin,
                transformations=transformations,
                parent_digests=parents,
                review_state=ReviewState(str(row["review_state"])),
                scan_state=ScanState(str(row["scan_state"])),
                classification=Classification(str(row["classification"])),
                allowed_sinks=sinks,
                policy_revision=int(row["policy_revision"]),
                tainted=bool(row["tainted"]),
                provenance_digest=str(row["provenance_digest"]),
                created_at=datetime.fromtimestamp(int(row["created_at"]), tz=UTC),
            )
        except Exception as exc:
            raise ConflictError("stored provenance does not match the strict schema") from exc
        if (
            str(row["origin_json"]) != _canonical_model(origin)
            or str(row["transformations_json"]) != _canonical_model(transformations)
            or str(row["parent_digests_json"]) != _canonical_model(parents)
            or str(row["allowed_sinks_json"]) != _canonical_model(sinks)
            or not secrets.compare_digest(record.provenance_digest, record.computed_digest())
        ):
            raise ConflictError("stored provenance integrity check failed")
        return record

    @staticmethod
    def _exact_existing(connection: Any, candidate: ProvenanceRecord) -> ProvenanceRecord | None:
        row = connection.execute(
            """SELECT * FROM content_provenance
                WHERE object_type=? AND object_id=? AND version=?""",
            (candidate.object_type.value, candidate.object_id, candidate.version),
        ).fetchone()
        if row is None:
            return None
        existing = ProvenanceService._record_from_row(row)
        if not secrets.compare_digest(existing.provenance_digest, candidate.provenance_digest):
            raise IdempotencyConflict("provenance version already contains different canonical bytes")
        return existing

    @staticmethod
    def _append_audit(connection: Any, store: StoreBackend, record: ProvenanceRecord) -> None:
        store.append_audit(
            connection,
            {
                "action": "provenance.append",
                "domain_id": record.domain_id,
                "object_type": record.object_type.value,
                "object_id": record.object_id,
                "version": record.version,
                "provenance_digest": record.provenance_digest,
                "classification": record.classification.value,
                "tainted": record.tainted,
                "policy_revision": record.policy_revision,
                "authority_effect": "none",
            },
        )

    def _resolve_insert_race(
        self,
        candidate: ProvenanceRecord,
        error: Exception,
    ) -> ProvenanceRecord:
        """Resolve the only safe outcome after a rolled-back concurrent insert."""

        row = self.store.fetch_one(
            """SELECT * FROM content_provenance
                WHERE object_type=? AND object_id=? AND version=?""",
            (candidate.object_type.value, candidate.object_id, candidate.version),
        )
        if row is None:
            raise error
        existing = self._record_from_row(row)
        if not secrets.compare_digest(existing.provenance_digest, candidate.provenance_digest):
            raise IdempotencyConflict(
                "concurrent provenance version contains different canonical bytes"
            ) from error
        return existing

    def _prepare_origin(
        self,
        registration: OriginRegistration,
        *,
        when: datetime | None,
    ) -> tuple[datetime, ProvenanceRecord]:
        when = _require_aware_second(when or datetime.now(UTC).replace(microsecond=0), "verification time")
        if registration.recorded_at > when:
            raise ValidationError("provenance record time is in the future")
        candidate = self._new_record(
            object_type=registration.object_type,
            object_id=registration.object_id,
            version=1,
            domain_id=registration.domain_id,
            origin=registration.origin,
            transformations=TransformationChain(
                output_digest=registration.origin.source_digest,
                steps=(),
            ),
            parent_digests=ParentDigestSet(digests=()),
            review_state=ReviewState.UNREVIEWED,
            scan_state=ScanState.PENDING,
            classification=registration.classification,
            allowed_sinks=registration.allowed_sinks,
            policy_revision=registration.policy_revision,
            tainted=True,
            created_at=registration.recorded_at,
        )
        return when, candidate

    def _register_origin_candidate_in_transaction(
        self,
        connection: Any,
        registration: OriginRegistration,
        candidate: ProvenanceRecord,
        *,
        mutation_validated: Callable[[], None] | None = None,
    ) -> ProvenanceRecord:
        self._require_domain(
            connection,
            domain_id=registration.domain_id,
            policy_revision=registration.policy_revision,
        )
        existing = self._exact_existing(connection, candidate)
        if existing is not None:
            return existing
        if mutation_validated is not None:
            mutation_validated()
        self._insert(connection, candidate)
        self._append_audit(connection, self.store, candidate)
        return candidate

    def register_origin_in_transaction(
        self,
        connection: Any,
        registration: OriginRegistration,
        *,
        when: datetime | None = None,
    ) -> ProvenanceRecord:
        """Append an origin on the caller's transaction without committing it.

        This is the atomic composition primitive for a content write and its
        provenance.  A database conflict is intentionally left to the caller's
        transaction/retry boundary; the standalone wrapper below additionally
        resolves an identical concurrent insert after rollback.
        """

        _when, candidate = self._prepare_origin(registration, when=when)
        return self._register_origin_candidate_in_transaction(
            connection,
            registration,
            candidate,
        )

    def register_origin(
        self,
        registration: OriginRegistration,
        *,
        when: datetime | None = None,
    ) -> ProvenanceRecord:
        _when, candidate = self._prepare_origin(registration, when=when)
        mutation_validated = False

        def mark_validated() -> None:
            nonlocal mutation_validated
            mutation_validated = True

        try:
            with self.store.transaction() as connection:
                return self._register_origin_candidate_in_transaction(
                    connection,
                    registration,
                    candidate,
                    mutation_validated=mark_validated,
                )
        except Exception as exc:
            if not mutation_validated:
                raise
            return self._resolve_insert_race(candidate, exc)

    @staticmethod
    def _require_sink_restriction(requested: SinkSet, parents: list[ProvenanceRecord]) -> None:
        finite = [set(parent.allowed_sinks.sinks) for parent in parents if "*" not in parent.allowed_sinks.sinks]
        if not finite:
            return
        maximum = set.intersection(*finite)
        if "*" in requested.sinks or not set(requested.sinks).issubset(maximum):
            raise ValidationError("derived provenance widened an allowed sink")

    @staticmethod
    def _require_transformation_chain(
        command: ProvenanceDerivation,
        parents: list[ProvenanceRecord],
        *,
        allow_causal_no_transform: bool = False,
    ) -> None:
        parent_content = tuple(sorted({parent.content_digest for parent in parents}))
        if not command.transformations:
            if allow_causal_no_transform and command.object_type in {
                ProvenanceObjectType.EVENT,
                ProvenanceObjectType.TASK,
            }:
                return
            if len(parent_content) != 1 or command.output_digest != parent_content[0]:
                raise ValidationError("no-op derivation must preserve one exact parent content digest")
            return
        expected_terminal_kind = {
            ProvenanceObjectType.MODEL_OUTPUT: TransformationKind.MODEL,
            ProvenanceObjectType.TOOL_OUTPUT: TransformationKind.TOOL,
            ProvenanceObjectType.PARSER_OUTPUT: TransformationKind.PARSER,
        }.get(command.object_type)
        if (
            expected_terminal_kind is not None
            and command.transformations[-1].kind is not expected_terminal_kind
        ):
            raise ValidationError("output object type does not match its terminal transformation")
        expected_inputs = parent_content
        previous_completed: datetime | None = None
        for step in command.transformations:
            if step.input_digests != expected_inputs:
                raise ValidationError("transformation chain omits or introduces an input digest")
            if previous_completed is not None and step.started_at < previous_completed:
                raise ValidationError("transformation chain time order is invalid")
            if step.completed_at > command.recorded_at:
                raise ValidationError("transformation completed after provenance was recorded")
            expected_inputs = (step.output_digest,)
            previous_completed = step.completed_at
        if command.transformations[-1].output_digest != command.output_digest:
            raise ValidationError("final transformation output digest does not match the record")

    def _verify_evidence(
        self,
        *,
        command: ProvenanceDerivation,
        when: datetime,
    ) -> tuple[VerifiedIndependentApproval | None, VerifiedIndependentApproval | None]:
        if command.review_approval is None and command.scan_approval is None:
            return None, None
        if not isinstance(self.evidence_verifier, IndependentApprovalVerifier):
            raise AuthorizationError("provenance clearance requires an independent evidence verifier")
        transaction = command.canonical_clearance_transaction()
        review: VerifiedIndependentApproval | None = None
        scan: VerifiedIndependentApproval | None = None
        if command.review_approval is not None:
            review = self.evidence_verifier.verify(
                canonical_transaction=transaction,
                approval=command.review_approval.model_dump(mode="python", by_alias=True),
                expected_purpose=REVIEW_CLEARANCE_PURPOSE,
                expected_domain_id=command.domain_id,
                when=when,
            )
        if command.scan_approval is not None:
            scan = self.evidence_verifier.verify(
                canonical_transaction=transaction,
                approval=command.scan_approval.model_dump(mode="python", by_alias=True),
                expected_purpose=SCAN_CLEARANCE_PURPOSE,
                expected_domain_id=command.domain_id,
                when=when,
            )
        return review, scan

    def _prepare_derivation(
        self,
        command: ProvenanceDerivation,
        *,
        when: datetime | None,
    ) -> tuple[datetime, ProvenanceRecord]:
        when = _require_aware_second(when or datetime.now(UTC).replace(microsecond=0), "verification time")
        if command.recorded_at > when:
            raise ValidationError("provenance record time is in the future")
        provisional_review = (
            _binding_from_receipt(command.review_approval)
            if command.review_approval is not None
            else None
        )
        provisional_scan = (
            _binding_from_receipt(command.scan_approval)
            if command.scan_approval is not None
            else None
        )
        parent_set_digest = canonical_digest(
            {
                "schema": "agentnet.provenance-parent-set.v1",
                "parent_digests": list(command.parent_digests.digests),
            }
        )
        derived_origin = ProvenanceOrigin(
            kind=OriginKind.DERIVED,
            source_id=f"derived:{parent_set_digest}",
            source_digest=parent_set_digest,
            observed_at=command.recorded_at,
        )
        candidate = self._new_record(
            object_type=command.object_type,
            object_id=command.object_id,
            version=command.expected_previous_version + 1,
            domain_id=command.domain_id,
            origin=derived_origin,
            transformations=TransformationChain(
                output_digest=command.output_digest,
                steps=command.transformations,
                review_evidence=provisional_review,
                scan_evidence=provisional_scan,
            ),
            parent_digests=command.parent_digests,
            review_state=command.review_state,
            scan_state=command.scan_state,
            classification=command.classification,
            allowed_sinks=command.allowed_sinks,
            policy_revision=command.policy_revision,
            tainted=command.tainted,
            created_at=command.recorded_at,
        )
        return when, candidate

    def _derive_candidate_in_transaction(
        self,
        connection: Any,
        command: ProvenanceDerivation,
        candidate: ProvenanceRecord,
        *,
        when: datetime,
        mutation_validated: Callable[[], None] | None = None,
        allow_causal_no_transform: bool = False,
    ) -> ProvenanceRecord:
        self._require_domain(
            connection,
            domain_id=command.domain_id,
            policy_revision=command.policy_revision,
        )
        existing = self._exact_existing(connection, candidate)
        if existing is not None:
            return existing
        lock_suffix = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
        latest = connection.execute(
            """SELECT * FROM content_provenance
                WHERE object_type=? AND object_id=?
                ORDER BY version DESC LIMIT 1""" + lock_suffix,
            (command.object_type.value, command.object_id),
        ).fetchone()
        actual_previous = 0 if latest is None else int(latest["version"])
        if actual_previous != command.expected_previous_version:
            raise ConflictError("provenance object version changed")
        parent_rows = []
        for provenance_digest in command.parent_digests.digests:
            row = connection.execute(
                "SELECT * FROM content_provenance WHERE provenance_digest=?",
                (provenance_digest,),
            ).fetchone()
            if row is None:
                raise AuthorizationError("provenance parent is unavailable")
            parent_rows.append(self._record_from_row(row))
        if any(parent.domain_id != command.domain_id for parent in parent_rows):
            raise AuthorizationError("provenance parent crossed a trust domain")
        if any(parent.policy_revision > command.policy_revision for parent in parent_rows):
            raise ConflictError("provenance parent has a future policy revision")
        if latest is not None:
            latest_record = self._record_from_row(latest)
            if latest_record.provenance_digest not in command.parent_digests.digests:
                raise ConflictError("next provenance version omitted its exact predecessor")
        maximum_classification = max(
            (_classification_rank(parent.classification) for parent in parent_rows),
            default=0,
        )
        if _classification_rank(command.classification) < maximum_classification:
            raise ValidationError("derived provenance lowered its classification")
        self._require_sink_restriction(command.allowed_sinks, parent_rows)
        self._require_transformation_chain(
            command,
            parent_rows,
            allow_causal_no_transform=allow_causal_no_transform,
        )
        review, scan = self._verify_evidence(command=command, when=when)
        if command.review_state is ReviewState.REVIEWED and review is None:
            raise AuthenticationError("reviewed provenance lacks current signed review evidence")
        if command.scan_state is ScanState.PASSED and scan is None:
            raise AuthenticationError("passed provenance lacks current signed scan evidence")
        if not command.tainted and (review is None or scan is None):
            raise AuthenticationError("taint clearance lacks current review and scan evidence")
        if mutation_validated is not None:
            mutation_validated()
        if review is not None:
            consume_independent_approval(connection, receipt=review)
        if scan is not None:
            consume_independent_approval(connection, receipt=scan)
        self._insert(connection, candidate)
        self._append_audit(connection, self.store, candidate)
        return candidate

    def derive_in_transaction(
        self,
        connection: Any,
        command: ProvenanceDerivation,
        *,
        when: datetime | None = None,
    ) -> ProvenanceRecord:
        """Append a derivation on the caller's transaction without committing."""

        verification_time, candidate = self._prepare_derivation(command, when=when)
        return self._derive_candidate_in_transaction(
            connection,
            command,
            candidate,
            when=verification_time,
        )

    def record_tainted_derivation_in_transaction(
        self,
        connection: Any,
        *,
        object_type: ProvenanceObjectType,
        object_id: str,
        domain_id: str,
        expected_previous_version: int,
        parent_provenance_digests: tuple[str, ...],
        transformations: tuple[TransformationStep, ...],
        output_digest: str,
        classification: Classification,
        allowed_sinks: tuple[str, ...],
        policy_revision: int,
        recorded_at: datetime,
        when: datetime | None = None,
    ) -> ProvenanceRecord:
        """Create one automatic derivation that can only preserve/restrict trust.

        The helper intentionally exposes no review evidence, scan evidence, or
        taint override.  New transformed content therefore starts tainted,
        unreviewed, and pending scan while classification and sink monotonicity
        are still enforced against every exact parent.
        """

        command = ProvenanceDerivation(
            object_type=object_type,
            object_id=object_id,
            domain_id=domain_id,
            expected_previous_version=expected_previous_version,
            parent_digests=ParentDigestSet(digests=parent_provenance_digests),
            transformations=transformations,
            output_digest=output_digest,
            classification=classification,
            allowed_sinks=SinkSet(sinks=allowed_sinks),
            policy_revision=policy_revision,
            review_state=ReviewState.UNREVIEWED,
            scan_state=ScanState.PENDING,
            tainted=True,
            recorded_at=recorded_at,
        )
        return self.derive_in_transaction(connection, command, when=when)

    def record_tainted_causal_derivation_in_transaction(
        self,
        connection: Any,
        *,
        object_type: ProvenanceObjectType,
        object_id: str,
        domain_id: str,
        parent_provenance_digests: tuple[str, ...],
        output_digest: str,
        classification: Classification,
        allowed_sinks: tuple[str, ...],
        policy_revision: int,
        recorded_at: datetime,
        when: datetime | None = None,
    ) -> ProvenanceRecord:
        """Compose one mailbox EVENT/TASK from exact ledger parents.

        This narrow transactional primitive is intentionally distinct from the
        public derivation API: it records causal composition without inventing
        a model, tool, or parser transformation.  It can only create a new,
        tainted version-one event/task and inherits every deny-only parent
        constraint through the ordinary derivation validator.
        """

        if object_type not in {ProvenanceObjectType.EVENT, ProvenanceObjectType.TASK}:
            raise ValidationError("causal mailbox derivation supports event and task objects only")
        command = ProvenanceDerivation(
            object_type=object_type,
            object_id=object_id,
            domain_id=domain_id,
            expected_previous_version=0,
            parent_digests=ParentDigestSet(digests=parent_provenance_digests),
            transformations=(),
            output_digest=output_digest,
            classification=classification,
            allowed_sinks=SinkSet(sinks=allowed_sinks),
            policy_revision=policy_revision,
            review_state=ReviewState.UNREVIEWED,
            scan_state=ScanState.PENDING,
            tainted=True,
            recorded_at=recorded_at,
        )
        verification_time, candidate = self._prepare_derivation(command, when=when)
        return self._derive_candidate_in_transaction(
            connection,
            command,
            candidate,
            when=verification_time,
            allow_causal_no_transform=True,
        )

    def derive(
        self,
        command: ProvenanceDerivation,
        *,
        when: datetime | None = None,
    ) -> ProvenanceRecord:
        verification_time, candidate = self._prepare_derivation(command, when=when)
        mutation_validated = False

        def mark_validated() -> None:
            nonlocal mutation_validated
            mutation_validated = True

        try:
            with self.store.transaction() as connection:
                return self._derive_candidate_in_transaction(
                    connection,
                    command,
                    candidate,
                    when=verification_time,
                    mutation_validated=mark_validated,
                )
        except Exception as exc:
            if not mutation_validated:
                raise
            return self._resolve_insert_race(candidate, exc)

    def require_reference_in_transaction(
        self,
        connection: Any,
        reference: ProvenanceReferenceV1,
        *,
        expected_domain_id: str,
        expected_content_digest: str,
        expected_object_type: ProvenanceObjectType,
        expected_classification: Classification,
        required_sinks: tuple[str, ...],
        expected_policy_revision: int,
    ) -> ProvenanceRecord:
        """Resolve one exact reference and enforce only restrictive constraints.

        Success proves ledger integrity and compatibility with the supplied
        content boundary.  It does *not* authorize that boundary: callers must
        still satisfy their existing identity, policy, grant, relationship,
        approval, and effect checks.  This method performs no mutation and has
        no positive-authority side effect.
        """

        if not isinstance(reference, ProvenanceReferenceV1):
            raise ValidationError("provenance reference does not match the strict v1 schema")
        if (
            len(expected_content_digest) != 64
            or any(character not in "0123456789abcdef" for character in expected_content_digest)
        ):
            raise ValidationError("expected provenance content digest is not SHA-256")
        if not isinstance(expected_object_type, ProvenanceObjectType):
            raise ValidationError("expected provenance object type is invalid")
        if not isinstance(expected_classification, Classification):
            raise ValidationError("expected provenance classification is invalid")
        if type(expected_policy_revision) is not int or expected_policy_revision < 1:
            raise ValidationError("expected provenance policy revision is invalid")
        try:
            required = SinkSet(sinks=required_sinks)
        except Exception as exc:
            raise ValidationError("required provenance sinks are invalid") from exc
        if "*" in required.sinks:
            raise ValidationError("a provenance sink requirement must be explicit")

        self._require_domain(
            connection,
            domain_id=expected_domain_id,
            policy_revision=expected_policy_revision,
        )
        row = connection.execute(
            "SELECT * FROM content_provenance WHERE provenance_digest=?",
            (reference.provenance_digest,),
        ).fetchone()
        if row is None:
            raise AuthorizationError("provenance reference is unavailable")
        record = self._record_from_row(row)
        authoritative_reference = record.reference()
        if not secrets.compare_digest(
            _canonical_model(reference).encode("utf-8"),
            _canonical_model(authoritative_reference).encode("utf-8"),
        ):
            raise AuthorizationError("provenance reference does not match the authoritative record")
        if record.domain_id != expected_domain_id:
            raise AuthorizationError("provenance reference crossed a trust domain")
        if record.object_type is not expected_object_type:
            raise AuthorizationError("provenance reference has the wrong object type")
        if not secrets.compare_digest(record.content_digest, expected_content_digest):
            raise AuthorizationError("provenance reference does not bind the exact content")
        if _classification_rank(expected_classification) < _classification_rank(
            record.classification
        ):
            raise AuthorizationError("provenance reference would lower content classification")
        allowed = set(record.allowed_sinks.sinks)
        if "*" not in allowed and not set(required.sinks).issubset(allowed):
            raise AuthorizationError("provenance reference does not allow the required sink")
        if record.policy_revision != expected_policy_revision:
            raise ConflictError("provenance reference policy revision is stale")
        return record

    def require_reference(
        self,
        reference: ProvenanceReferenceV1,
        *,
        expected_domain_id: str,
        expected_content_digest: str,
        expected_object_type: ProvenanceObjectType,
        expected_classification: Classification,
        required_sinks: tuple[str, ...],
        expected_policy_revision: int,
    ) -> ProvenanceRecord:
        """Validate through non-mutating reads, including inside caller transactions."""

        class _ReadCursor:
            def __init__(self, row: Any) -> None:
                self.row = row

            def fetchone(self) -> Any:
                return self.row

        class _ReadConnection:
            def __init__(self, store: StoreBackend) -> None:
                self.store = store

            def execute(self, query: str, parameters: tuple[Any, ...] = ()) -> _ReadCursor:
                return _ReadCursor(self.store.fetch_one(query, parameters))

        return self.require_reference_in_transaction(
            _ReadConnection(self.store),
            reference,
            expected_domain_id=expected_domain_id,
            expected_content_digest=expected_content_digest,
            expected_object_type=expected_object_type,
            expected_classification=expected_classification,
            required_sinks=required_sinks,
            expected_policy_revision=expected_policy_revision,
        )

    def get_version(
        self,
        *,
        object_type: ProvenanceObjectType,
        object_id: str,
        version: int,
    ) -> ProvenanceRecord:
        row = self.store.fetch_one(
            """SELECT * FROM content_provenance
                WHERE object_type=? AND object_id=? AND version=?""",
            (object_type.value, object_id, version),
        )
        if row is None:
            raise AuthorizationError("provenance record is unavailable")
        return self._record_from_row(row)

    def get_by_digest(self, provenance_digest: str) -> ProvenanceRecord:
        if len(provenance_digest) != 64 or any(ch not in "0123456789abcdef" for ch in provenance_digest):
            raise ValidationError("provenance digest is not SHA-256")
        row = self.store.fetch_one(
            "SELECT * FROM content_provenance WHERE provenance_digest=?", (provenance_digest,)
        )
        if row is None:
            raise AuthorizationError("provenance record is unavailable")
        return self._record_from_row(row)

    def versions(
        self,
        *,
        object_type: ProvenanceObjectType,
        object_id: str,
    ) -> tuple[ProvenanceRecord, ...]:
        rows = self.store.fetch_all(
            """SELECT * FROM content_provenance
                WHERE object_type=? AND object_id=? ORDER BY version""",
            (object_type.value, object_id),
        )
        return tuple(self._record_from_row(row) for row in rows)


__all__ = [
    "EvidenceBinding",
    "OriginKind",
    "OriginRegistration",
    "ParentDigestSet",
    "ProvenanceDerivation",
    "ProvenanceObjectType",
    "ProvenanceOrigin",
    "ProvenanceRecord",
    "ProvenanceReferenceV1",
    "ProvenanceService",
    "REVIEW_CLEARANCE_PURPOSE",
    "ReviewState",
    "SCAN_CLEARANCE_PURPOSE",
    "ScanState",
    "SinkSet",
    "TransformationChain",
    "TransformationKind",
    "TransformationStep",
]
