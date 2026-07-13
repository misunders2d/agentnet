from __future__ import annotations

import pytest
from pydantic import ValidationError

from agentnet.identity.actors import ActorKind, VerifiedActor


def human(**updates: object) -> dict[str, object]:
    value: dict[str, object] = {
        "kind": ActorKind.VERIFIED_HUMAN_HARNESS,
        "domain_id": "corp.example",
        "principal_id": "human-1",
        "harness_id": "harness-1",
        "credential_id": "credential-1",
        "credential_epoch": 1,
        "binding_assurance": "os_bound",
    }
    value.update(updates)
    return value


@pytest.mark.parametrize(
    "field",
    ["guest_id", "workload_id", "external_peer_id", "parent_event_id", "task_grant_id"],
)
def test_verified_human_rejects_every_cross_kind_field_even_null(field: str) -> None:
    with pytest.raises(ValidationError, match="cross-kind"):
        VerifiedActor.model_validate(human(**{field: None}))


@pytest.mark.parametrize(
    ("kind", "values", "cross_field"),
    [
        (
            ActorKind.HOST_GUEST_HARNESS,
            {
                "guest_id": "guest-1",
                "harness_id": "guest-harness",
                "credential_id": "guest-credential",
                "credential_epoch": 1,
                "binding_assurance": "hardware_bound",
            },
            "principal_id",
        ),
        (
            ActorKind.WORKLOAD,
            {"workload_id": "worker-1", "binding_assurance": "workload_mtls"},
            "harness_id",
        ),
        (
            ActorKind.EXTERNAL_A2A,
            {"external_peer_id": "peer-1", "binding_assurance": "external"},
            "credential_id",
        ),
    ],
)
def test_other_actor_variants_reject_cross_kind_identity(
    kind: ActorKind,
    values: dict[str, object],
    cross_field: str,
) -> None:
    with pytest.raises(ValidationError, match="cross-kind"):
        VerifiedActor.model_validate(
            {"kind": kind, "domain_id": "corp.example", **values, cross_field: "smuggled"}
        )


@pytest.mark.parametrize(
    ("kind", "values", "assurance"),
    [
        (ActorKind.VERIFIED_HUMAN_HARNESS, human(), "external"),
        (
            ActorKind.HOST_GUEST_HARNESS,
            {
                "kind": ActorKind.HOST_GUEST_HARNESS,
                "domain_id": "corp.example",
                "guest_id": "guest-1",
                "harness_id": "harness-1",
                "credential_id": "credential-1",
                "credential_epoch": 1,
            },
            "workload_mtls",
        ),
        (
            ActorKind.WORKLOAD,
            {"kind": ActorKind.WORKLOAD, "domain_id": "corp.example", "workload_id": "worker-1"},
            "lab",
        ),
        (
            ActorKind.EXTERNAL_A2A,
            {"kind": ActorKind.EXTERNAL_A2A, "domain_id": "corp.example", "external_peer_id": "peer-1"},
            "os_bound",
        ),
    ],
)
def test_actor_variant_rejects_wrong_assurance(kind: ActorKind, values: dict[str, object], assurance: str) -> None:
    del kind
    with pytest.raises(ValidationError, match="assurance"):
        VerifiedActor.model_validate({**values, "binding_assurance": assurance})


def test_model_copy_cannot_bypass_tagged_union_validation() -> None:
    actor = VerifiedActor.model_validate(human())
    with pytest.raises(ValidationError, match="cross-kind"):
        actor.model_copy(update={"workload_id": "smuggled-worker"})


def test_serialized_variant_contains_only_its_exact_tagged_union_fields() -> None:
    workload = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="corp.example",
        workload_id="worker-1",
        workload_registration_id="registration-0001",
        workload_role="mailbox_dispatcher",
        workload_process_id=123,
        workload_process_start_time=456,
        workload_session_id="session-00000001",
        workload_revocation_epoch=1,
        credential_id="registration-0001",
        credential_epoch=1,
        binding_assurance="workload_mtls",
    )
    assert workload.model_dump(mode="json") == {
        "kind": "workload",
        "domain_id": "corp.example",
        "workload_id": "worker-1",
        "workload_registration_id": "registration-0001",
        "workload_role": "mailbox_dispatcher",
        "workload_process_id": 123,
        "workload_process_start_time": 456,
        "workload_session_id": "session-00000001",
        "workload_revocation_epoch": 1,
        "credential_id": "registration-0001",
        "credential_epoch": 1,
        "binding_assurance": "workload_mtls",
    }


def test_synthetic_workload_cannot_claim_mtls_or_internal_process_assurance() -> None:
    for false_assurance in ("workload_mtls", "internal_process"):
        with pytest.raises(ValidationError):
            VerifiedActor(
                kind=ActorKind.WORKLOAD,
                domain_id="synthetic.example",
                workload_id="synthetic-lab-mailbox:actor",
                binding_assurance=false_assurance,
            )
    actor = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="synthetic.example",
        workload_id="synthetic-lab-mailbox:actor",
        binding_assurance="synthetic_lab",
    )
    assert actor.binding_assurance == "synthetic_lab"
