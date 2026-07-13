from __future__ import annotations

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.artifacts.scanner import (
    ArtifactProvenanceV1,
    ArtifactScanAttestationV1,
)
from agentnet.effects.workflow import (
    VerifiedWorkflowTerminalReceipt,
    WorkflowTerminalReceiptV1,
    WorkflowReceiptVerifier,
    terminal_effect_from_workflow,
    verify_workflow_terminal_receipt,
)
from agentnet.errors import (
    AuthenticationError,
    ConflictError,
    ValidationError,
)
from agentnet.identity.workload import (
    SPIFFEAdapter,
    SPIFFETransportAuthority,
)
from agentnet.interfaces.contracts import (
    ArtifactStoredVersionV1,
    MailboxAcceptanceV1,
    WorkflowStartResultV1,
)


def _scan() -> dict[str, object]:
    return {
        "artifact_id": "artifact-boundary-00000001",
        "classification": "C2",
        "ciphertext_digest": "a" * 64,
        "expires_at": 1_800_000_300,
        "issued_at": 1_800_000_000,
        "object_key": "b" * 32,
        "object_version": "c" * 64,
        "plaintext_digest": "d" * 64,
        "policy_revision": 7,
        "profile_digest": "e" * 64,
        "result": "allow",
        "rules_digest": "f" * 64,
        "scanner_engine": "maintained-scanner",
        "scanner_id": "scanner.example",
        "scanner_key_epoch": 3,
        "scanner_version": "2026.07",
        "signature": "synthetic-signature",
    }


def _spiffe() -> dict[str, object]:
    return {
        "schema_version": "1.0",
        "spiffe_id": "spiffe://corp.example/mailbox/worker-1",
        "trust_domain": "corp.example",
        "workload_role": "mailbox_dispatcher",
        "certificate_serial": "serial-1",
        "process_id": 4121,
        "process_start_time": 1_800_000_000,
        "session_id": "session-boundary-00000001",
    }


def _workflow_receipt() -> dict[str, object]:
    return {
        "schema_version": "1.0",
        "workflow_id": "workflow-1",
        "workflow_run_id": "workflow-run-1",
        "effect_id": "effect-1",
        "attempt_id": "attempt-boundary-00000001",
        "fact": "effect_succeeded",
        "external_receipt_id": "external-receipt-1",
        "external_receipt_digest": "a" * 64,
        "issuer_registration_id": "registration-boundary-00000001",
        "issuer_credential_epoch": 2,
        "issuer_revocation_epoch": 4,
        "observed_at": 1_800_000_000,
        "nonce": "workflow-nonce-boundary-00000001",
        "signature": "synthetic-signature",
    }


class _RecordingWorkflowVerifier(WorkflowReceiptVerifier):
    def __init__(self, seen: list[WorkflowTerminalReceiptV1]) -> None:
        self.seen = seen

    def verify(self, receipt: WorkflowTerminalReceiptV1) -> None:
        self.seen.append(receipt)


class _BooleanWorkflowVerifier(WorkflowReceiptVerifier):
    def verify(self, receipt: WorkflowTerminalReceiptV1) -> None:
        return True  # type: ignore[return-value]


@pytest.mark.parametrize(
    "value",
    [
        {},
        {"origin": 7},
        {"origin": {"name": "payload-claim"}},
        {"origin": "test", "trusted": True},
        {"origin": "bad\norigin"},
    ],
)
def test_artifact_provenance_v1_rejects_missing_extra_and_wrong_nested_types(
    value: object,
) -> None:
    with pytest.raises(ValidationError, match="exact v1 schema"):
        ArtifactProvenanceV1.parse_boundary(value)
    assert ArtifactProvenanceV1.parse_boundary({"origin": "test"}).origin == "test"


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("scanner_key_epoch", True),
        ("policy_revision", "7"),
        ("issued_at", 1.5),
        ("classification", {"value": "C2"}),
        ("result", "passed"),
        ("profile_digest", ["e" * 64]),
    ],
)
def test_scan_attestation_v1_never_coerces_wrong_primitive_or_nested_types(
    field: str,
    value: object,
) -> None:
    raw = _scan()
    raw[field] = value
    with pytest.raises(ValidationError, match="exact v1 schema"):
        ArtifactScanAttestationV1.parse_boundary(raw)


def test_scan_attestation_v1_rejects_unknown_and_missing_fields() -> None:
    unknown = _scan() | {"verified": True}
    missing = _scan()
    del missing["signature"]
    for raw in (unknown, missing):
        with pytest.raises(ValidationError, match="exact v1 schema"):
            ArtifactScanAttestationV1.parse_boundary(raw)


def test_spiffe_adapter_rejects_caller_asserted_verified_flag_and_foreign_authority() -> None:
    adapter = SPIFFEAdapter()
    caller_assertion = _spiffe() | {"verified": "true"}
    with pytest.raises(AuthenticationError, match="authenticated mTLS transport capability"):
        adapter.resolve(caller_assertion)  # type: ignore[arg-type]

    foreign = SPIFFETransportAuthority().bind_verified_peer(_spiffe())
    with pytest.raises(AuthenticationError, match="another authority"):
        adapter.resolve(foreign)

    bound = adapter.transport_authority.bind_verified_peer(_spiffe())
    identity = adapter.resolve(bound)
    assert identity.spiffe_id == _spiffe()["spiffe_id"]


@pytest.mark.parametrize(
    "update",
    [
        {"verified": True},
        {"process_id": True},
        {"process_start_time": "1800000000"},
        {"spiffe_id": "spiffe://other.example/mailbox/worker-1"},
        {"workload_role": {"name": "mailbox_dispatcher"}},
        {"schema_version": "2.0"},
    ],
)
def test_spiffe_transport_authority_strictly_parses_exact_peer_facts(
    update: dict[str, object],
) -> None:
    raw = _spiffe() | update
    with pytest.raises(AuthenticationError, match="transport facts are malformed"):
        SPIFFETransportAuthority().bind_verified_peer(raw)


def test_workflow_terminal_receipt_requires_exact_parse_binding_and_verifier_boundary() -> None:
    raw = _workflow_receipt()
    seen: list[WorkflowTerminalReceiptV1] = []

    verified = verify_workflow_terminal_receipt(
        raw,
        expected_workflow_id="workflow-1",
        expected_effect_id="effect-1",
        verifier=_RecordingWorkflowVerifier(seen),
    )
    assert seen == [verified.receipt]
    assert terminal_effect_from_workflow("completed", verified) == "effect_succeeded"

    with pytest.raises(ValidationError, match="authenticated verifier boundary"):
        terminal_effect_from_workflow("completed", raw)  # type: ignore[arg-type]
    with pytest.raises(ConflictError, match="cannot fabricate"):
        terminal_effect_from_workflow("completed", None)
    with pytest.raises(ConflictError, match="contradicts"):
        terminal_effect_from_workflow("running", verified)
    with pytest.raises(TypeError, match="only be minted"):
        VerifiedWorkflowTerminalReceipt(verified.receipt, _seal=object())


def test_workflow_terminal_receipt_rejects_malformed_unbound_and_boolean_verifier_results() -> None:
    with pytest.raises(ValidationError, match="verifier is required"):
        verify_workflow_terminal_receipt(
            _workflow_receipt(),
            expected_workflow_id="workflow-1",
            expected_effect_id="effect-1",
            verifier=lambda _receipt: None,  # type: ignore[arg-type]
        )
    for changed in (
        _workflow_receipt() | {"verified": True},
        _workflow_receipt() | {"issuer_credential_epoch": "2"},
        _workflow_receipt() | {"fact": {"state": "effect_succeeded"}},
    ):
        with pytest.raises(ValidationError, match="exact v1 schema"):
            verify_workflow_terminal_receipt(
                changed,
                expected_workflow_id="workflow-1",
                expected_effect_id="effect-1",
                verifier=_RecordingWorkflowVerifier([]),
            )
    with pytest.raises(ConflictError, match="another workflow"):
        verify_workflow_terminal_receipt(
            _workflow_receipt(),
            expected_workflow_id="workflow-other",
            expected_effect_id="effect-1",
            verifier=_RecordingWorkflowVerifier([]),
        )
    with pytest.raises(ValidationError, match="success or raise"):
        verify_workflow_terminal_receipt(
            _workflow_receipt(),
            expected_workflow_id="workflow-1",
            expected_effect_id="effect-1",
            verifier=_BooleanWorkflowVerifier(),
        )


def test_replaceable_interface_results_are_strict_versioned_models() -> None:
    stored = {
        "schema_version": "1.0",
        "object_key": "a" * 32,
        "version": "b" * 64,
        "ciphertext_digest": "b" * 64,
        "ciphertext_size": 42,
    }
    assert ArtifactStoredVersionV1.model_validate(stored, strict=True).ciphertext_size == 42
    for malformed in (
        stored | {"ciphertext_size": True},
        stored | {"provider_verified": True},
        {key: value for key, value in stored.items() if key != "version"},
    ):
        with pytest.raises(PydanticValidationError):
            ArtifactStoredVersionV1.model_validate(malformed, strict=True)

    with pytest.raises(PydanticValidationError):
        MailboxAcceptanceV1.model_validate(
            {
                "schema_version": "1.0",
                "event_id": "event-1",
                "fact": "accepted_local",
                "envelope_digest": "c" * 64,
                "duplicate": 1,
            },
            strict=True,
        )
    with pytest.raises(PydanticValidationError):
        WorkflowStartResultV1.model_validate(
            {
                "schema_version": "1.0",
                "workflow_id": "workflow-1",
                "workflow_run_id": "run-1",
                "state": "running",
                "accepted_at": "1800000000",
            },
            strict=True,
        )
