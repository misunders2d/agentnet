from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.decision import AuthorizationDecision, DecisionRecorder
from agentnet.errors import AuthorizationError
from agentnet.identity.actors import ActorKind, TrustedTransportContext, VerifiedActor
from agentnet.operations.authority_inspection import (
    AuthorityBasis,
    AuthorityBasisRole,
    AuthorityBasisState,
    AuthorityInspectionService,
    DenialCategory,
    DenialExplanationQuery,
)
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_json
from agentnet.storage.sqlite import SQLiteStore


NOW = datetime(2026, 7, 13, 12, 0, tzinfo=UTC)
UNAVAILABLE = "authorization information is unavailable"


def _insert_human(
    store: SQLiteStore,
    *,
    principal_id: str,
    harness_id: str,
    credential_id: str,
    domain_id: str = "corp.example",
) -> VerifiedActor:
    epoch = int(NOW.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """INSERT OR IGNORE INTO principals(
                   principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
               ) VALUES(?,?,?,?,?,'active',?)""",
            (
                principal_id,
                domain_id,
                "https://idp.example",
                f"subject-{principal_id}",
                f"{principal_id}@example.test",
                epoch - 600,
            ),
        )
        connection.execute(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                   binding_assurance,capabilities_json,credential_epoch,created_at
               ) VALUES(?,?,?,NULL,'codex',?,'active','os_bound','{}',1,?)""",
            (harness_id, domain_id, principal_id, harness_id, epoch - 600),
        )
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,'synthetic-public-key','active',1,?,?)""",
            (credential_id, harness_id, f"key-{credential_id}", epoch - 600, epoch + 7200),
        )
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=domain_id,
        principal_id=principal_id,
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="os_bound",
    )


def _insert_guest(store: SQLiteStore) -> VerifiedActor:
    epoch = int(NOW.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO guests(
                   guest_id,host_domain_id,home_domain_id,pairwise_subject,
                   sponsor_principal_id,status,expires_at
               ) VALUES('guest-own','corp.example','partner.example','pairwise-own',
                        'sponsor-not-disclosed','active',?)""",
            (epoch + 7200,),
        )
        connection.execute(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                   binding_assurance,capabilities_json,credential_epoch,created_at
               ) VALUES('guest-harness','corp.example',NULL,'guest-own','federated_guest',
                        'Guest','active','os_bound','{}',1,?)""",
            (epoch - 600,),
        )
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES('guest-credential','guest-harness','guest-key','synthetic-public-key',
                        'active',1,?,?)""",
            (epoch - 600, epoch + 7200),
        )
    return VerifiedActor(
        kind=ActorKind.HOST_GUEST_HARNESS,
        domain_id="corp.example",
        guest_id="guest-own",
        harness_id="guest-harness",
        credential_id="guest-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )


def _transport(actor: VerifiedActor, *, proof_id: str = "proof-current") -> TrustedTransportContext:
    return TrustedTransportContext(
        actor=actor,
        audience="urn:agentnet:test",
        method="GET",
        scheme="https",
        authority="agentnet.example",
        path="/v1/authority/inventory",
        query="",
        body_digest="0" * 64,
        timestamp=int(NOW.timestamp()),
        nonce=f"nonce-{proof_id}",
        proof_id=proof_id,
    )


@pytest.fixture
def authority_store(tmp_path) -> SQLiteStore:
    store = SQLiteStore(tmp_path / "authority-inspection.sqlite3", LocalEnvelopeCipher(b"i" * 32))
    epoch = int(NOW.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at)
               VALUES('corp.example','active',2,1,?)""",
            (epoch - 600,),
        )
    yield store
    store.close()


@pytest.fixture
def actors(authority_store: SQLiteStore) -> dict[str, VerifiedActor]:
    owner = _insert_human(
        authority_store,
        principal_id="owner-human",
        harness_id="owner-harness",
        credential_id="owner-credential",
    )
    sibling = _insert_human(
        authority_store,
        principal_id="owner-human",
        harness_id="owner-sibling-harness",
        credential_id="owner-sibling-credential",
    )
    other = _insert_human(
        authority_store,
        principal_id="other-human",
        harness_id="other-harness",
        credential_id="other-credential",
    )
    return {"owner": owner, "sibling": sibling, "other": other}


def _record_decision(
    store: SQLiteStore,
    *,
    decision_id: str,
    actor: VerifiedActor,
    allowed: bool,
    reason: str,
) -> None:
    decision = AuthorizationDecision(
        decision_id=decision_id,
        occurred_at=NOW,
        actor=actor,
        action="protected.secret.read",
        resource={"id": "secret:project-zephyr", "payload": "protected-resource-bytes"},
        context={
            "request": {"other_actor": "other-human", "secret": "do-not-return"},
            "positive_authority_id": actor.positive_authority_id,
        },
        allowed=allowed,
        reason=reason,
        policy_revision=2,
    )
    with store.transaction() as connection:
        DecisionRecorder(store).record(connection, decision)


def _insert_entitlement(
    store: SQLiteStore,
    *,
    entitlement_id: str,
    principal_id: str,
    revision: int,
    expires_at: datetime | None,
    revoked_at: datetime | None = None,
) -> None:
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO entitlements(
                   entitlement_id,domain_id,principal_id,action,resource_pattern,
                   expires_at,revoked_at,revision
               ) VALUES(?,'corp.example',?,'message.send',?,?,?,?)""",
            (
                entitlement_id,
                principal_id,
                f"resource:{entitlement_id}",
                int(expires_at.timestamp()) if expires_at else None,
                int(revoked_at.timestamp()) if revoked_at else None,
                revision,
            ),
        )


def _insert_grant(
    store: SQLiteStore,
    *,
    grant_id: str,
    actor: VerifiedActor,
    expires_at: datetime,
    policy_revision: int = 2,
    credential_epoch: int = 1,
    uses: int = 0,
    max_uses: int = 3,
    revoked_at: datetime | None = None,
) -> None:
    grant = TaskGrant(
        grant_id=grant_id,
        domain_id=actor.domain_id,
        principal_id=actor.positive_authority_id or "",
        harness_id=actor.harness_id or "",
        actions=frozenset({"message.process"}),
        resources=frozenset({f"event:{grant_id}"}),
        input_sources=frozenset({"mailbox"}),
        output_sinks=frozenset({"receipt"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_uses=max_uses,
        expires_at=expires_at,
    )
    binding = {
        "schema": "agentnet.task-grant.authority-binding.v1",
        "grant_id": grant_id,
        "domain_id": actor.domain_id,
        "principal_id": actor.positive_authority_id,
        "harness_id": actor.harness_id,
        "policy_revision": policy_revision,
        "harness_credential_epoch": credential_epoch,
        "issued_at": int((NOW - timedelta(minutes=5)).timestamp()),
    }
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO task_grants(
                   grant_id,domain_id,principal_id,harness_id,grant_json,
                   max_uses,uses,expires_at,revoked_at
               ) VALUES(?,?,?,?,?,?,?,?,?)""",
            (
                grant_id,
                actor.domain_id,
                actor.positive_authority_id,
                actor.harness_id,
                canonical_json(grant.model_dump(mode="json")).decode("utf-8"),
                max_uses,
                uses,
                int(expires_at.timestamp()),
                int(revoked_at.timestamp()) if revoked_at else None,
            ),
        )
        connection.execute(
            "INSERT INTO metadata(key,value) VALUES(?,?)",
            (
                f"authority-binding:task-grant:{grant_id}",
                canonical_json(binding).decode("utf-8"),
            ),
        )


def test_denial_explanation_is_owner_bound_and_withholds_protected_details(
    authority_store: SQLiteStore,
    actors: dict[str, VerifiedActor],
) -> None:
    _record_decision(
        authority_store,
        decision_id="decision-revoked-grant",
        actor=actors["owner"],
        allowed=False,
        reason="task_grant_revoked",
    )
    service = AuthorityInspectionService(authority_store)

    explanation = service.explain_denial(
        transport=_transport(actors["owner"]),
        query=DenialExplanationQuery(decision_id="decision-revoked-grant"),
        when=NOW,
    )

    assert explanation.category is DenialCategory.GRANT_LIFECYCLE
    assert explanation.reason_code == "task_grant_revoked"
    rendered = json.dumps(explanation.model_dump(mode="json"), sort_keys=True)
    for protected in (
        "secret:project-zephyr",
        "protected-resource-bytes",
        "do-not-return",
        "other-human",
        "owner-human",
        "owner-harness",
    ):
        assert protected not in rendered


def test_denial_explanation_collapses_unknown_wrong_owner_and_allowed_decisions(
    authority_store: SQLiteStore,
    actors: dict[str, VerifiedActor],
) -> None:
    _record_decision(
        authority_store,
        decision_id="decision-other-owner",
        actor=actors["other"],
        allowed=False,
        reason="no_current_positive_entitlement",
    )
    _record_decision(
        authority_store,
        decision_id="decision-owner-allowed",
        actor=actors["owner"],
        allowed=True,
        reason="authorized_by_human_entitlement_and_current_constraints",
    )
    service = AuthorityInspectionService(authority_store)

    messages: list[str] = []
    for decision_id in ("missing-decision", "decision-other-owner", "decision-owner-allowed"):
        with pytest.raises(AuthorizationError) as error:
            service.explain_denial(
                transport=_transport(actors["owner"], proof_id=decision_id),
                query=DenialExplanationQuery(decision_id=decision_id),
                when=NOW,
            )
        messages.append(str(error.value))
    assert messages == [UNAVAILABLE, UNAVAILABLE, UNAVAILABLE]


def test_same_positive_authority_sibling_can_read_but_unknown_reason_is_sanitized(
    authority_store: SQLiteStore,
    actors: dict[str, VerifiedActor],
) -> None:
    raw_reason = "internal_rule:project-zephyr:other-human"
    _record_decision(
        authority_store,
        decision_id="decision-owner-unknown-rule",
        actor=actors["owner"],
        allowed=False,
        reason=raw_reason,
    )

    explanation = AuthorityInspectionService(authority_store).explain_denial(
        transport=_transport(actors["sibling"]),
        query=DenialExplanationQuery(decision_id="decision-owner-unknown-rule"),
        when=NOW,
    )

    assert explanation.reason_code == "policy_denied"
    assert raw_reason not in json.dumps(explanation.model_dump(mode="json"))


def test_human_inventory_reports_only_own_entitlements_and_exact_harness_grants(
    authority_store: SQLiteStore,
    actors: dict[str, VerifiedActor],
) -> None:
    _insert_entitlement(
        authority_store,
        entitlement_id="entitlement-current",
        principal_id="owner-human",
        revision=2,
        expires_at=NOW + timedelta(hours=1),
    )
    _insert_entitlement(
        authority_store,
        entitlement_id="entitlement-stale",
        principal_id="owner-human",
        revision=1,
        expires_at=NOW + timedelta(hours=1),
    )
    _insert_entitlement(
        authority_store,
        entitlement_id="entitlement-expired",
        principal_id="owner-human",
        revision=2,
        expires_at=NOW - timedelta(seconds=1),
    )
    _insert_entitlement(
        authority_store,
        entitlement_id="entitlement-revoked",
        principal_id="owner-human",
        revision=2,
        expires_at=NOW + timedelta(hours=1),
        revoked_at=NOW - timedelta(minutes=1),
    )
    _insert_entitlement(
        authority_store,
        entitlement_id="other-private-entitlement",
        principal_id="other-human",
        revision=2,
        expires_at=NOW + timedelta(hours=1),
    )
    _insert_grant(
        authority_store,
        grant_id="owner-current-grant",
        actor=actors["owner"],
        expires_at=NOW + timedelta(hours=1),
    )
    _insert_grant(
        authority_store,
        grant_id="sibling-private-grant",
        actor=actors["sibling"],
        expires_at=NOW + timedelta(hours=1),
    )

    inventory = AuthorityInspectionService(authority_store).authority_inventory(
        transport=_transport(actors["owner"]),
        when=NOW,
    )

    states = {basis.basis_id: basis.state for basis in inventory.bases}
    assert states == {
        "entitlement-current": AuthorityBasisState.CURRENT,
        "entitlement-expired": AuthorityBasisState.EXPIRED,
        "entitlement-revoked": AuthorityBasisState.REVOKED,
        "entitlement-stale": AuthorityBasisState.STALE_POLICY,
        "owner-current-grant": AuthorityBasisState.CURRENT,
    }
    grant = next(item for item in inventory.bases if item.basis_id == "owner-current-grant")
    assert grant.basis_role is AuthorityBasisRole.ATTENUATION_ONLY
    assert grant.independently_authorizes_operation is False
    assert inventory.descriptive_only is True
    rendered = json.dumps(inventory.model_dump(mode="json"), sort_keys=True)
    assert "other-private-entitlement" not in rendered
    assert "sibling-private-grant" not in rendered
    assert "other-human" not in rendered


def test_guest_inventory_uses_exact_host_task_grant_without_sponsor_disclosure(
    authority_store: SQLiteStore,
) -> None:
    guest = _insert_guest(authority_store)
    _insert_grant(
        authority_store,
        grant_id="guest-current-grant",
        actor=guest,
        expires_at=NOW + timedelta(hours=1),
    )

    inventory = AuthorityInspectionService(authority_store).authority_inventory(
        transport=_transport(guest),
        when=NOW,
    )

    assert inventory.authority_kind == "guest"
    assert len(inventory.bases) == 1
    assert inventory.bases[0].basis_role is AuthorityBasisRole.HOST_GUEST_POSITIVE_GRANT
    assert inventory.bases[0].state is AuthorityBasisState.CURRENT
    rendered = json.dumps(inventory.model_dump(mode="json"), sort_keys=True)
    assert "sponsor-not-disclosed" not in rendered


def test_inventory_surfaces_policy_credential_and_use_lifecycle_without_widening(
    authority_store: SQLiteStore,
    actors: dict[str, VerifiedActor],
) -> None:
    _insert_grant(
        authority_store,
        grant_id="grant-stale-policy",
        actor=actors["owner"],
        expires_at=NOW + timedelta(hours=1),
        policy_revision=1,
    )
    _insert_grant(
        authority_store,
        grant_id="grant-stale-credential",
        actor=actors["owner"],
        expires_at=NOW + timedelta(hours=1),
        credential_epoch=2,
    )
    _insert_grant(
        authority_store,
        grant_id="grant-exhausted",
        actor=actors["owner"],
        expires_at=NOW + timedelta(hours=1),
        uses=3,
    )
    _insert_grant(
        authority_store,
        grant_id="grant-revoked",
        actor=actors["owner"],
        expires_at=NOW + timedelta(hours=1),
        revoked_at=NOW - timedelta(seconds=1),
    )

    inventory = AuthorityInspectionService(authority_store).authority_inventory(
        transport=_transport(actors["owner"]),
        when=NOW,
    )
    states = {item.basis_id: item.state for item in inventory.bases}
    assert states == {
        "grant-exhausted": AuthorityBasisState.EXHAUSTED,
        "grant-revoked": AuthorityBasisState.REVOKED,
        "grant-stale-credential": AuthorityBasisState.STALE_CREDENTIAL,
        "grant-stale-policy": AuthorityBasisState.STALE_POLICY,
    }
    assert all(item.independently_authorizes_operation is False for item in inventory.bases)


def test_inspection_fails_closed_after_actor_revocation_and_on_malformed_owned_state(
    authority_store: SQLiteStore,
    actors: dict[str, VerifiedActor],
) -> None:
    service = AuthorityInspectionService(authority_store)
    with authority_store.transaction() as connection:
        connection.execute(
            """INSERT INTO task_grants(
                   grant_id,domain_id,principal_id,harness_id,grant_json,
                   max_uses,uses,expires_at,revoked_at
               ) VALUES('malformed-owned','corp.example','owner-human','owner-harness',
                        '{"unexpected":true}',1,0,?,NULL)""",
            (int((NOW + timedelta(hours=1)).timestamp()),),
        )
    with pytest.raises(AuthorizationError, match=UNAVAILABLE):
        service.authority_inventory(transport=_transport(actors["owner"]), when=NOW)

    with authority_store.transaction() as connection:
        connection.execute("DELETE FROM task_grants WHERE grant_id='malformed-owned'")
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id='owner-harness'")
    with pytest.raises(AuthorizationError, match=UNAVAILABLE):
        service.authority_inventory(transport=_transport(actors["owner"]), when=NOW)


def test_models_forbid_caller_asserted_identity_and_authority_fields() -> None:
    with pytest.raises(PydanticValidationError):
        DenialExplanationQuery.model_validate(
            {"decision_id": "decision-a", "principal_id": "caller-asserted"}
        )
    with pytest.raises(PydanticValidationError):
        AuthorityBasis.model_validate(
            {
                "basis_type": "human_entitlement",
                "basis_role": "positive_human_authority",
                "basis_id": "entitlement-a",
                "state": "current",
                "actions": ["message.send"],
                "resources": ["*"],
                "principal_id": "other-human",
            }
        )
