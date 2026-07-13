from __future__ import annotations

import hashlib
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.approval.service import (
    IndependentApprovalReceipt,
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.protocol.models import Classification
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ParentDigestSet,
    ProvenanceDerivation,
    ProvenanceObjectType,
    ProvenanceOrigin,
    ProvenanceReferenceV1,
    ProvenanceService,
    REVIEW_CLEARANCE_PURPOSE,
    ReviewState,
    SCAN_CLEARANCE_PURPOSE,
    ScanState,
    SinkSet,
    TransformationKind,
    TransformationStep,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.sqlite import SQLiteStore


NOW = datetime(2026, 7, 13, 10, 0, tzinfo=UTC)


def digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


@pytest.fixture
def store(tmp_path):
    database = SQLiteStore(tmp_path / "provenance.db", LocalEnvelopeCipher(b"p" * 32))
    with database.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("domain-a", "active", 1, 1, int((NOW - timedelta(days=1)).timestamp())),
        )
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("domain-b", "active", 1, 1, int((NOW - timedelta(days=1)).timestamp())),
        )
    try:
        yield database
    finally:
        database.close()


@pytest.fixture
def evidence_profile():
    review_key = P256KeyPair.generate()
    scan_key = P256KeyPair.generate()
    review_trust = TrustedApprover(
        principal_id="review-human",
        domain_id="domain-a",
        signer_key_id=review_key.thumbprint,
        public_key_pem=review_key.public_pem,
        allowed_purposes=frozenset({REVIEW_CLEARANCE_PURPOSE}),
    )
    scan_trust = TrustedApprover(
        principal_id="scan-human",
        domain_id="domain-a",
        signer_key_id=scan_key.thumbprint,
        public_key_pem=scan_key.public_pem,
        allowed_purposes=frozenset({SCAN_CLEARANCE_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {
            review_key.thumbprint: review_trust,
            scan_key.thumbprint: scan_trust,
        },
        verifier_id="provenance-evidence.example",
    )
    return verifier, review_key, review_trust, scan_key, scan_trust


def origin_registration(
    *,
    object_id: str = "source-1",
    domain_id: str = "domain-a",
    classification: Classification = Classification.C1_INTERNAL,
    sinks: tuple[str, ...] = ("sink:archive", "sink:conversation"),
    source_digest: str | None = None,
    when: datetime = NOW,
) -> OriginRegistration:
    return OriginRegistration(
        object_type=ProvenanceObjectType.ARTIFACT,
        object_id=object_id,
        domain_id=domain_id,
        origin=ProvenanceOrigin(
            kind=OriginKind.EXTERNAL_INPUT,
            source_id=f"external:{object_id}",
            source_digest=source_digest or digest(f"content:{object_id}"),
            observed_at=when,
        ),
        classification=classification,
        allowed_sinks=SinkSet(sinks=sinks),
        policy_revision=1,
        recorded_at=when,
    )


def register_origin(
    service: ProvenanceService,
    *,
    object_id: str = "source-1",
    domain_id: str = "domain-a",
    classification: Classification = Classification.C1_INTERNAL,
    sinks: tuple[str, ...] = ("sink:archive", "sink:conversation"),
    source_digest: str | None = None,
    when: datetime = NOW,
):
    command = origin_registration(
        object_id=object_id,
        domain_id=domain_id,
        classification=classification,
        sinks=sinks,
        source_digest=source_digest,
        when=when,
    )
    return service.register_origin(command, when=when)


def derivation(
    parent,
    *,
    object_id: str = "derived-1",
    object_type: ProvenanceObjectType = ProvenanceObjectType.MODEL_OUTPUT,
    expected_previous_version: int = 0,
    classification: Classification | None = None,
    sinks: tuple[str, ...] = ("sink:archive",),
    output_digest: str | None = None,
    review_state: ReviewState = ReviewState.UNREVIEWED,
    scan_state: ScanState = ScanState.PENDING,
    tainted: bool = True,
    recorded_at: datetime = NOW + timedelta(seconds=3),
):
    output = output_digest or digest(f"output:{object_id}:{expected_previous_version + 1}")
    step = TransformationStep(
        kind=TransformationKind.MODEL,
        operation_id=f"operation:{object_id}:{expected_previous_version + 1}",
        implementation_id="model:gpt",
        implementation_version="locked-test-version",
        executor_harness_id="executor-harness",
        input_digests=(parent.content_digest,),
        output_digest=output,
        started_at=recorded_at - timedelta(seconds=2),
        completed_at=recorded_at - timedelta(seconds=1),
    )
    return ProvenanceDerivation(
        object_type=object_type,
        object_id=object_id,
        domain_id=parent.domain_id,
        expected_previous_version=expected_previous_version,
        parent_digests=ParentDigestSet(digests=(parent.provenance_digest,)),
        transformations=(step,),
        output_digest=output,
        classification=classification or parent.classification,
        allowed_sinks=SinkSet(sinks=sinks),
        policy_revision=1,
        review_state=review_state,
        scan_state=scan_state,
        tainted=tainted,
        recorded_at=recorded_at,
    )


def attach_clearance(command: ProvenanceDerivation, profile):
    _, review_key, review_trust, scan_key, scan_trust = profile
    transaction = command.canonical_clearance_transaction()
    issued_at = int(command.recorded_at.timestamp())
    review = IndependentApprovalReceipt.model_validate(
        create_independent_approval_receipt(
            review_key,
            approver=review_trust,
            verifier_id="provenance-evidence.example",
            approval_purpose=REVIEW_CLEARANCE_PURPOSE,
            canonical_transaction=transaction,
            issued_at=issued_at,
            expires_at=issued_at + 300,
            nonce="review-clearance-nonce-0000000001",
            receipt_id="review-receipt-0000000001",
        )
    )
    scan = IndependentApprovalReceipt.model_validate(
        create_independent_approval_receipt(
            scan_key,
            approver=scan_trust,
            verifier_id="provenance-evidence.example",
            approval_purpose=SCAN_CLEARANCE_PURPOSE,
            canonical_transaction=transaction,
            issued_at=issued_at,
            expires_at=issued_at + 300,
            nonce="scan-clearance-nonce-00000000001",
            receipt_id="scan-receipt-00000000001",
        )
    )
    return command.model_copy(update={"review_approval": review, "scan_approval": scan})


def test_origin_is_append_only_tainted_and_authority_neutral(store):
    service = ProvenanceService(store)
    record = register_origin(service)

    assert record.version == 1
    assert record.tainted is True
    assert record.review_state is ReviewState.UNREVIEWED
    assert record.scan_state is ScanState.PENDING
    assert record.parent_digests.digests == ()
    assert record.content_digest == digest("content:source-1")
    assert service.get_by_digest(record.provenance_digest) == record
    audit = store.fetch_one("SELECT record_json FROM audit_log ORDER BY sequence DESC LIMIT 1")
    assert '"authority_effect":"none"' in audit["record_json"]

    replay = register_origin(service)
    assert replay == record
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 1


def test_origin_version_replay_with_different_bytes_is_rejected(store):
    service = ProvenanceService(store)
    register_origin(service)
    with pytest.raises(IdempotencyConflict):
        register_origin(service, source_digest=digest("different bytes"))


def test_concurrent_identical_origin_replays_converge_on_one_version(store):
    service = ProvenanceService(store)
    with ThreadPoolExecutor(max_workers=8) as pool:
        records = list(pool.map(lambda _: register_origin(service), range(24)))
    assert len({record.provenance_digest for record in records}) == 1
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 1


def test_transaction_origin_append_is_replay_safe_and_rolls_back_with_caller(store):
    service = ProvenanceService(store)
    command = origin_registration(object_id="transaction-origin")

    with pytest.raises(RuntimeError, match="rollback caller"):
        with store.transaction() as connection:
            first = service.register_origin_in_transaction(connection, command, when=NOW)
            replay = service.register_origin_in_transaction(connection, command, when=NOW)
            assert replay == first
            assert connection.execute(
                "SELECT COUNT(*) AS total FROM content_provenance"
            ).fetchone()["total"] == 1
            raise RuntimeError("rollback caller")

    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 0
    with store.transaction() as connection:
        committed = service.register_origin_in_transaction(connection, command, when=NOW)
    with store.transaction() as connection:
        replay = service.register_origin_in_transaction(connection, command, when=NOW)
    assert replay == committed
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 1


def test_strict_models_reject_caller_asserted_authority_and_verified_flags():
    with pytest.raises(PydanticValidationError):
        ProvenanceOrigin.model_validate(
            {
                "kind": "external_input",
                "source_id": "external:x",
                "source_digest": digest("x"),
                "observed_at": NOW.isoformat(),
                "granted_authority": "tools:*",
            }
        )
    with pytest.raises(PydanticValidationError):
        SinkSet.model_validate({"sinks": ["sink:a"], "verified": True})


def test_reference_is_strict_complete_and_cannot_assert_authority(store):
    record = register_origin(ProvenanceService(store))
    reference = record.reference()

    assert reference.provenance_digest == record.provenance_digest
    assert reference.content_digest == record.content_digest
    assert reference.authority_effect == "none"
    with pytest.raises(PydanticValidationError):
        ProvenanceReferenceV1.model_validate(
            reference.model_dump(mode="json") | {"authorized": True},
            strict=True,
        )
    with pytest.raises(PydanticValidationError):
        ProvenanceReferenceV1.model_validate(
            reference.model_dump(mode="json") | {"verified": True},
            strict=True,
        )


def test_derivation_cannot_lower_classification_or_widen_sinks(store):
    service = ProvenanceService(store)
    parent = register_origin(
        service,
        classification=Classification.C2_RESTRICTED,
        sinks=("sink:archive", "sink:conversation"),
    )
    with pytest.raises(ValidationError, match="lowered"):
        service.derive(
            derivation(parent, classification=Classification.C1_INTERNAL),
            when=NOW + timedelta(seconds=3),
        )
    with pytest.raises(ValidationError, match="widened"):
        service.derive(
            derivation(parent, sinks=("sink:external",)),
            when=NOW + timedelta(seconds=3),
        )


def test_reference_validation_is_an_exact_and_deny_only_constraint_gate(store):
    service = ProvenanceService(store)
    record = register_origin(
        service,
        classification=Classification.C2_RESTRICTED,
        sinks=("sink:archive", "sink:conversation"),
    )
    reference = record.reference()
    common = {
        "expected_domain_id": "domain-a",
        "expected_content_digest": record.content_digest,
        "expected_object_type": ProvenanceObjectType.ARTIFACT,
        "expected_classification": Classification.C2_RESTRICTED,
        "required_sinks": ("sink:archive",),
        "expected_policy_revision": 1,
    }

    assert service.require_reference(reference, **common) == record
    assert service.require_reference(
        reference,
        **(common | {"expected_classification": Classification.C3_SEALED}),
    ) == record
    with pytest.raises(AuthorizationError, match="trust domain"):
        service.require_reference(
            reference,
            **(common | {"expected_domain_id": "domain-b"}),
        )
    with pytest.raises(AuthorizationError, match="exact content"):
        service.require_reference(
            reference,
            **(common | {"expected_content_digest": digest("substituted")}),
        )
    with pytest.raises(AuthorizationError, match="wrong object type"):
        service.require_reference(
            reference,
            **(common | {"expected_object_type": ProvenanceObjectType.EVENT}),
        )
    with pytest.raises(AuthorizationError, match="lower content classification"):
        service.require_reference(
            reference,
            **(common | {"expected_classification": Classification.C1_INTERNAL}),
        )
    with pytest.raises(AuthorizationError, match="required sink"):
        service.require_reference(
            reference,
            **(common | {"required_sinks": ("sink:external",)}),
        )

    tampered = reference.model_copy(update={"classification": Classification.C0_PUBLIC})
    with pytest.raises(AuthorizationError, match="authoritative record"):
        service.require_reference(tampered, **common)


def test_reference_policy_epoch_must_be_current_and_exact(store):
    service = ProvenanceService(store)
    record = register_origin(service)
    reference = record.reference()
    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='domain-a'")

    with pytest.raises(ConflictError, match="policy revision is stale"):
        service.require_reference(
            reference,
            expected_domain_id="domain-a",
            expected_content_digest=record.content_digest,
            expected_object_type=ProvenanceObjectType.ARTIFACT,
            expected_classification=Classification.C1_INTERNAL,
            required_sinks=("sink:archive",),
            expected_policy_revision=2,
        )
    with pytest.raises(ConflictError, match="policy revision drifted"):
        service.require_reference(
            reference,
            expected_domain_id="domain-a",
            expected_content_digest=record.content_digest,
            expected_object_type=ProvenanceObjectType.ARTIFACT,
            expected_classification=Classification.C1_INTERNAL,
            required_sinks=("sink:archive",),
            expected_policy_revision=1,
        )


def test_reference_validation_never_creates_positive_authority(store):
    service = ProvenanceService(store)
    record = register_origin(service)
    before = {
        table: store.fetch_one(f"SELECT COUNT(*) AS total FROM {table}")["total"]
        for table in (
            "task_grants",
            "relationship_governance_transactions",
            "policy_decisions",
        )
    }

    with store.transaction() as connection:
        required = service.require_reference_in_transaction(
            connection,
            record.reference(),
            expected_domain_id="domain-a",
            expected_content_digest=record.content_digest,
            expected_object_type=ProvenanceObjectType.ARTIFACT,
            expected_classification=Classification.C1_INTERNAL,
            required_sinks=("sink:archive",),
            expected_policy_revision=1,
        )
    assert required == record
    assert required.tainted is True
    assert {
        table: store.fetch_one(f"SELECT COUNT(*) AS total FROM {table}")["total"]
        for table in (
            "task_grants",
            "relationship_governance_transactions",
            "policy_decisions",
        )
    } == before


def test_transformation_chain_must_bind_all_inputs_and_exact_output(store):
    service = ProvenanceService(store)
    parent = register_origin(service)
    command = derivation(parent)
    hidden_input = command.transformations[0].model_copy(
        update={"input_digests": (digest("hidden"),)}
    )
    with pytest.raises(ValidationError, match="omits or introduces"):
        service.derive(
            command.model_copy(update={"transformations": (hidden_input,)}),
            when=command.recorded_at,
        )
    wrong_output = command.model_copy(update={"output_digest": digest("wrong-output")})
    with pytest.raises(ValidationError, match="final transformation"):
        service.derive(wrong_output, when=wrong_output.recorded_at)


@pytest.mark.parametrize(
    ("object_type", "kind", "object_id"),
    [
        (ProvenanceObjectType.MODEL_OUTPUT, TransformationKind.MODEL, "typed-model"),
        (ProvenanceObjectType.TOOL_OUTPUT, TransformationKind.TOOL, "typed-tool"),
        (ProvenanceObjectType.PARSER_OUTPUT, TransformationKind.PARSER, "typed-parser"),
    ],
)
def test_model_tool_and_parser_outputs_bind_their_terminal_transformation(
    store, object_type, kind, object_id
):
    service = ProvenanceService(store)
    parent = register_origin(service)
    command = derivation(parent, object_id=object_id, object_type=object_type)
    command = command.model_copy(
        update={
            "transformations": (
                command.transformations[0].model_copy(update={"kind": kind}),
            )
        }
    )
    record = service.derive(command, when=command.recorded_at)
    assert record.transformations.steps[-1].kind is kind

    mismatch = derivation(
        parent,
        object_id=f"{object_id}-mismatch",
        object_type=object_type,
    )
    wrong_kind = next(value for value in TransformationKind if value is not kind)
    mismatch = mismatch.model_copy(
        update={
            "transformations": (
                mismatch.transformations[0].model_copy(update={"kind": wrong_kind}),
            )
        }
    )
    with pytest.raises(ValidationError, match="terminal transformation"):
        service.derive(mismatch, when=mismatch.recorded_at)


def test_transaction_derivation_rolls_back_and_automatic_helper_is_replay_safe(store):
    service = ProvenanceService(store)
    parent = register_origin(service)
    command = derivation(parent, object_id="transaction-derived")

    with pytest.raises(RuntimeError, match="rollback caller"):
        with store.transaction() as connection:
            record = service.derive_in_transaction(
                connection,
                command,
                when=command.recorded_at,
            )
            assert record.tainted is True
            assert connection.execute(
                "SELECT COUNT(*) AS total FROM content_provenance"
            ).fetchone()["total"] == 2
            raise RuntimeError("rollback caller")
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 1

    with store.transaction() as connection:
        automatic = service.record_tainted_derivation_in_transaction(
            connection,
            object_type=command.object_type,
            object_id=command.object_id,
            domain_id=command.domain_id,
            expected_previous_version=command.expected_previous_version,
            parent_provenance_digests=command.parent_digests.digests,
            transformations=command.transformations,
            output_digest=command.output_digest,
            classification=command.classification,
            allowed_sinks=command.allowed_sinks.sinks,
            policy_revision=command.policy_revision,
            recorded_at=command.recorded_at,
            when=command.recorded_at,
        )
    assert automatic.tainted is True
    assert automatic.review_state is ReviewState.UNREVIEWED
    assert automatic.scan_state is ScanState.PENDING
    assert automatic.transformations.review_evidence is None
    assert automatic.transformations.scan_evidence is None

    with store.transaction() as connection:
        replay = service.record_tainted_derivation_in_transaction(
            connection,
            object_type=command.object_type,
            object_id=command.object_id,
            domain_id=command.domain_id,
            expected_previous_version=command.expected_previous_version,
            parent_provenance_digests=command.parent_digests.digests,
            transformations=command.transformations,
            output_digest=command.output_digest,
            classification=command.classification,
            allowed_sinks=command.allowed_sinks.sinks,
            policy_revision=command.policy_revision,
            recorded_at=command.recorded_at,
            when=command.recorded_at,
        )
    assert replay == automatic
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 2


def test_concurrent_identical_derivations_converge_on_one_version(store):
    service = ProvenanceService(store)
    parent = register_origin(service)
    command = derivation(parent, object_id="concurrent-derived")

    with ThreadPoolExecutor(max_workers=8) as pool:
        records = list(
            pool.map(
                lambda _: service.derive(command, when=command.recorded_at),
                range(24),
            )
        )
    assert len({record.provenance_digest for record in records}) == 1
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 2


def test_clean_derivation_fails_closed_without_verified_evidence(store):
    service = ProvenanceService(store)
    parent = register_origin(service)
    unsigned = derivation(
        parent,
        review_state=ReviewState.REVIEWED,
        scan_state=ScanState.PASSED,
        tainted=False,
    )
    with pytest.raises(AuthenticationError, match="reviewed provenance"):
        service.derive(unsigned, when=unsigned.recorded_at)


def test_exact_current_review_and_scan_evidence_clear_taint_replay_safely(
    store, evidence_profile
):
    verifier = evidence_profile[0]
    service = ProvenanceService(store, evidence_verifier=verifier)
    parent = register_origin(service)
    unsigned = derivation(
        parent,
        review_state=ReviewState.REVIEWED,
        scan_state=ScanState.PASSED,
        tainted=False,
    )
    command = attach_clearance(unsigned, evidence_profile)

    clean = service.derive(command, when=command.recorded_at)
    assert clean.tainted is False
    assert clean.transformations.review_evidence is not None
    assert clean.transformations.scan_evidence is not None
    assert clean.transformations.review_evidence.purpose == REVIEW_CLEARANCE_PURPOSE
    assert clean.transformations.scan_evidence.purpose == SCAN_CLEARANCE_PURPOSE
    assert clean.transformations.review_evidence.receipt.signature
    assert clean.transformations.scan_evidence.receipt.signature
    assert clean.allowed_sinks.sinks == ("sink:archive",)

    replay = service.derive(command, when=command.recorded_at)
    assert replay == clean
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 2
    assert store.fetch_one("SELECT COUNT(*) AS total FROM replay_nonces")["total"] == 2

    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='domain-a'")
    with pytest.raises(ConflictError, match="policy revision drifted"):
        service.derive(command, when=command.recorded_at)


def test_clearance_receipts_cannot_be_replayed_for_another_output(store, evidence_profile):
    service = ProvenanceService(store, evidence_verifier=evidence_profile[0])
    parent = register_origin(service)
    first = attach_clearance(
        derivation(
            parent,
            object_id="clean-1",
            review_state=ReviewState.REVIEWED,
            scan_state=ScanState.PASSED,
            tainted=False,
        ),
        evidence_profile,
    )
    service.derive(first, when=first.recorded_at)
    second_unsigned = derivation(
        parent,
        object_id="clean-2",
        review_state=ReviewState.REVIEWED,
        scan_state=ScanState.PASSED,
        tainted=False,
    )
    replayed = second_unsigned.model_copy(
        update={
            "review_approval": first.review_approval,
            "scan_approval": first.scan_approval,
        }
    )
    with pytest.raises(AuthenticationError, match="transaction binding"):
        service.derive(replayed, when=replayed.recorded_at)


def test_policy_drift_fails_before_append_and_receipts_bind_revision(store, evidence_profile):
    service = ProvenanceService(store, evidence_verifier=evidence_profile[0])
    parent = register_origin(service)
    stale = attach_clearance(
        derivation(
            parent,
            review_state=ReviewState.REVIEWED,
            scan_state=ScanState.PASSED,
            tainted=False,
        ),
        evidence_profile,
    )
    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='domain-a'")
    with pytest.raises(ConflictError, match="policy revision drifted"):
        service.derive(stale, when=stale.recorded_at)
    assert store.fetch_one("SELECT COUNT(*) AS total FROM content_provenance")["total"] == 1


def test_version_stream_requires_exact_predecessor_and_lists_verified_lineage(store):
    service = ProvenanceService(store)
    first = register_origin(service, object_id="versioned")
    second_command = derivation(
        first,
        object_id="versioned",
        object_type=ProvenanceObjectType.ARTIFACT,
        expected_previous_version=1,
    )
    second = service.derive(second_command, when=second_command.recorded_at)
    assert second.version == 2
    assert service.versions(
        object_type=ProvenanceObjectType.ARTIFACT, object_id="versioned"
    ) == (first, second)

    other = register_origin(service, object_id="other", when=NOW + timedelta(seconds=4))
    omitted = derivation(
        other,
        object_id="versioned",
        object_type=ProvenanceObjectType.ARTIFACT,
        expected_previous_version=2,
        recorded_at=NOW + timedelta(seconds=7),
    )
    with pytest.raises(ConflictError, match="omitted"):
        service.derive(omitted, when=omitted.recorded_at)


def test_cross_domain_parent_and_stored_tamper_fail_closed(store):
    service = ProvenanceService(store)
    parent = register_origin(service)
    cross_domain = register_origin(
        service,
        object_id="domain-b-source",
        domain_id="domain-b",
    )
    command = derivation(parent)
    command = command.model_copy(
        update={"parent_digests": ParentDigestSet(digests=(cross_domain.provenance_digest,))}
    )
    with pytest.raises(AuthorizationError, match="crossed a trust domain"):
        service.derive(command, when=command.recorded_at)

    with store.transaction() as connection:
        connection.execute(
            """UPDATE content_provenance SET allowed_sinks_json=?
                WHERE provenance_digest=?""",
            ('{"schema_version":"1.0","sinks":["sink:tampered"]}', parent.provenance_digest),
        )
    with pytest.raises(ConflictError, match="integrity"):
        service.get_by_digest(parent.provenance_digest)


def test_record_survives_store_reopen_with_digest_integrity(tmp_path):
    path = tmp_path / "durable-provenance.db"
    cipher = LocalEnvelopeCipher(b"d" * 32)
    store = SQLiteStore(path, cipher)
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("domain-a", "active", 1, 1, int((NOW - timedelta(days=1)).timestamp())),
        )
    record = register_origin(ProvenanceService(store))
    store.close()

    reopened = SQLiteStore(path, cipher)
    try:
        loaded = ProvenanceService(reopened).get_by_digest(record.provenance_digest)
        assert loaded == record
        assert loaded.computed_digest() == record.provenance_digest
    finally:
        reopened.close()
