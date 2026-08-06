from __future__ import annotations

import json
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from threading import Barrier
from uuid import NAMESPACE_URL, uuid5

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization import (
    AuthorizationRequest,
    HumanEntitlement,
    IssuanceAuthority,
    LocalConformancePolicyEngine,
)
from agentnet.errors import (
    AuthorizationError,
    ConflictError,
    IdempotencyConflict,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.organization import (
    RELATIONSHIP_CONSENT_PURPOSE,
    RelationshipService,
)
from agentnet.organization.assignment import (
    AssignmentRequest,
    AssignmentService,
    TaskIngressKind,
    TaskProposalState,
)
from agentnet.organization.conflicts import (
    TaskAccessMode,
    TaskConflictAdjudication,
    TaskExecutionIntent,
    TaskExclusivity,
    TaskResourceIntent,
)
from agentnet.protocol.models import (
    Classification,
    DeliveryFact,
    EventType,
    Relationship,
)
from agentnet.security.signatures import (
    P256KeyPair,
    canonical_digest,
    canonical_json,
)
from agentnet.storage.sqlite import SQLiteStore
from agentnet.supervisor.integration import BackgroundHarnessIntegration


_COLLABORATION_SCOPE_ID = "scope:task-custody-contract"
_COLLABORATION_SCOPE_MEMBERS = (
    "admin-harness",
    "peer-harness",
    "peer-second-harness",
    "sub-harness",
)


@dataclass(frozen=True, slots=True)
class _TaskScopeSnapshot:
    domain_id: str
    scope_id: str = _COLLABORATION_SCOPE_ID
    member_harness_ids: tuple[str, ...] = _COLLABORATION_SCOPE_MEMBERS
    revision: int = 1
    policy_revision: int = 1
    domain_revocation_epoch: int = 1
    state: str = "active"
    allowed_actions: tuple[str, ...] = (
        "message.acknowledge",
        "message.read",
        "task.accept",
        "task.propose",
    )
    allowed_resource_prefixes: tuple[str, ...] = ("task:",)
    allowed_classifications: tuple[Classification, ...] = (
        Classification.C1_INTERNAL,
        Classification.C2_RESTRICTED,
    )
    scope_digest: str = "b" * 64

    def authorization_context(self) -> dict[str, object]:
        return {
            "collaboration_scope_id": self.scope_id,
            "collaboration_scope_revision": self.revision,
            "collaboration_scope_policy_revision": self.policy_revision,
            "collaboration_scope_domain_revocation_epoch": self.domain_revocation_epoch,
            "collaboration_scope_member_harness_ids": list(self.member_harness_ids),
            "collaboration_scope_digest": self.scope_digest,
        }


class _TaskScopes:
    """Exact task-only scope double for custody tests."""

    @staticmethod
    def _snapshot(actor: VerifiedActor) -> _TaskScopeSnapshot:
        return _TaskScopeSnapshot(domain_id=actor.domain_id)

    def get_for_actor(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str,
    ) -> _TaskScopeSnapshot:
        snapshot = self._snapshot(actor)
        if (
            scope_id != snapshot.scope_id
            or actor.domain_id != "domain-a"
            or actor.harness_id not in snapshot.member_harness_ids
        ):
            raise AuthorizationError("collaboration scope authorization failed")
        return snapshot

    def require_in_transaction(
        self,
        _connection: object,
        *,
        actor: VerifiedActor,
        scope_id: str,
        action: str,
        resource: str,
        target_harness_ids: tuple[str, ...],
        classification: Classification,
    ) -> _TaskScopeSnapshot:
        snapshot = self.get_for_actor(actor=actor, scope_id=scope_id)
        if (
            action not in snapshot.allowed_actions
            or not resource.startswith(snapshot.allowed_resource_prefixes)
            or not set(target_harness_ids).issubset(snapshot.member_harness_ids)
            or classification not in snapshot.allowed_classifications
        ):
            raise AuthorizationError("collaboration scope authorization failed")
        return snapshot

    def require(self, **values: object) -> _TaskScopeSnapshot:
        return self.require_in_transaction(None, **values)


def _authorization_context() -> dict[str, object]:
    return _TaskScopeSnapshot(domain_id="domain-a").authorization_context()

@pytest.fixture(autouse=True)
def _mailbox_clock_within_actor_credential_validity(monkeypatch, now: datetime) -> None:
    monkeypatch.setattr("agentnet.mailbox.service.time.time", lambda: now.timestamp())


@dataclass(frozen=True, slots=True)
class _RelationshipApprovalAuthority:
    signers: dict[str, P256KeyPair]
    approvers: dict[str, TrustedApprover]
    verifier: IndependentApprovalVerifier


@pytest.fixture(scope="module")
def relationship_approval_authority() -> _RelationshipApprovalAuthority:
    signers = {
        principal_id: P256KeyPair.generate()
        for principal_id in ("admin-human", "sub-human", "peer-human")
    }
    approvers = {
        principal_id: TrustedApprover(
            principal_id=principal_id,
            domain_id="domain-a",
            signer_key_id=signer.thumbprint,
            public_key_pem=signer.public_pem,
            allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
        )
        for principal_id, signer in signers.items()
    }
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver for approver in approvers.values()},
        verifier_id="task-custody-relationship-approval",
    )
    return _RelationshipApprovalAuthority(
        signers=signers,
        approvers=approvers,
        verifier=verifier,
    )


def _request(
    actor,
    recipient: str = "sub-harness",
    *,
    data_classes: frozenset[Classification] | None = None,
    deadline: datetime | None = None,
) -> AssignmentRequest:
    return AssignmentRequest(
        actor=actor,
        collaboration_scope_id=_COLLABORATION_SCOPE_ID,
        recipient_harness_id=recipient,
        task_type="research",
        resources=frozenset({"catalog:alpha"}),
        data_classes=data_classes or frozenset({Classification.C1_INTERNAL}),
        tools=frozenset({"search"}),
        budget=10,
        concurrency=1,
        deadline=deadline,
        policy_revision=1,
        context={"opaque_context_digest": "d" * 64},
    )


def _event(
    request: AssignmentRequest,
    *,
    key: str,
    secret: str = "sealed work bytes",
    created_at: datetime | None = None,
):
    event = new_event(
        event_id=str(uuid5(NAMESPACE_URL, f"test-task-custody:{key}")),
        domain_id=request.actor.domain_id,
        actor=request.actor,
        event_type=EventType.TASK_ASSIGNMENT,
        classification=max(request.data_classes, key=lambda item: item.value),
        payload={
            "instruction": secret,
            "protected_resource": "catalog:alpha",
            "authorization_context": _authorization_context(),
        },
        idempotency_key=key,
        recipients=(request.recipient_harness_id,),
        task_id=str(uuid5(NAMESPACE_URL, f"test-task-id:{key}")),
        effect_deadline=request.deadline,
        policy_revision=request.policy_revision,
    )
    return event if created_at is None else event.model_copy(update={"created_at": created_at})


def _service(
    store,
    approval_authority: _RelationshipApprovalAuthority,
) -> AssignmentService:
    collaboration_scopes = _TaskScopes()
    mailbox = MailboxService(store, collaboration_scopes=collaboration_scopes)
    return AssignmentService(
        store,
        collaboration_scopes=collaboration_scopes,
        mailbox=mailbox,
        approval_verifier=approval_authority.verifier,
    )


def _reconcile(mailbox: MailboxService, actor: VerifiedActor) -> list[dict[str, object]]:
    return mailbox.reconcile(
        actor=actor,
        collaboration_scope_id=_COLLABORATION_SCOPE_ID,
    )


def _insert_edge(
    store,
    now,
    *,
    approval_authority: _RelationshipApprovalAuthority,
    administrator: str = "peer-harness",
    subordinate: str = "sub-harness",
    expires_in: timedelta = timedelta(hours=2),
    data_classes: tuple[str, ...] = ("C1",),
    resources: tuple[str, ...] = ("catalog:alpha",),
) -> None:
    scope = {
        "task_types": ["research"],
        "resources": list(resources),
        "data_classes": list(data_classes),
        "tools": ["search"],
        "max_budget": 100,
        "max_duration_seconds": 3600,
        "max_concurrency": 1,
        "authority_effect": "custody_only",
    }
    relationship_id = f"edge:{administrator}:{subordinate}:1"
    administrator_row = store.fetch_one(
        "SELECT * FROM harnesses WHERE harness_id=?", (administrator,)
    )
    subordinate_row = store.fetch_one(
        "SELECT * FROM harnesses WHERE harness_id=?", (subordinate,)
    )
    credential = store.fetch_one(
        "SELECT credential_id FROM credentials WHERE harness_id=? AND epoch=? "
        "AND status='active' ORDER BY credential_id LIMIT 1",
        (administrator, administrator_row["credential_epoch"]),
    )
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="domain-a",
        principal_id=administrator_row["principal_id"],
        harness_id=administrator,
        credential_id=credential["credential_id"],
        credential_epoch=int(administrator_row["credential_epoch"]),
        binding_assurance=administrator_row["binding_assurance"],
    )
    relationship = Relationship(
        relationship_id=relationship_id,
        domain_id="domain-a",
        administrator_harness_id=administrator,
        subordinate_harness_id=subordinate,
        may_assign=True,
        assignment_scope=scope,
        revision=1,
        expires_at=now + expires_in,
    )
    proposal_expires_at = now + timedelta(minutes=10)
    resource, context = RelationshipService.proposal_binding(
        relationship,
        proposal_expires_at=proposal_expires_at,
    )
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id="domain-a",
            principal_id=actor.principal_id,
            action="organization.relationship.propose",
            resource_pattern=resource,
            revision=1,
            expires_at=relationship.expires_at,
        )
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action="organization.relationship.propose",
            resource=resource,
            policy_revision=1,
            context=context,
        ),
        when=now,
    )
    owner_id = subordinate_row["principal_id"] or subordinate_row["guest_id"]
    signer = approval_authority.signers[owner_id]
    approver = approval_authority.approvers[owner_id]
    relationships = RelationshipService(
        store,
        approval_verifier=approval_authority.verifier,
    )
    proposal = relationships.propose(
        relationship,
        proposal_expires_at=proposal_expires_at,
        authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        when=now,
    )
    receipt = create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=approval_authority.verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(
            proposal.consent_transaction.model_dump(mode="json")
        ),
        issued_at=int(now.timestamp()),
        expires_at=int((now + timedelta(minutes=5)).timestamp()),
    )
    relationships.accept(
        relationship_id,
        actor=actor,
        approval=receipt,
        expected_transaction_digest=proposal.transaction_digest,
        expected_relationship_revision=proposal.revision,
        expected_lifecycle_revision=proposal.lifecycle_revision,
        when=now,
    )


def _exclusive_intent(*resources: str) -> TaskExecutionIntent:
    return TaskExecutionIntent(
        resources=tuple(
            TaskResourceIntent(
                resource=resource,
                operation="research",
                access=TaskAccessMode.WRITE,
                exclusivity=TaskExclusivity.EXCLUSIVE,
            )
            for resource in sorted(resources)
        )
    )


def _conflict_decision(
    conflict: dict[str, object],
    *,
    release_event_ids: frozenset[str],
    reject_event_ids: frozenset[str],
    reason_code: str,
) -> TaskConflictAdjudication:
    return TaskConflictAdjudication(
        conflict_id=str(conflict["conflict_id"]),
        expected_revision=int(conflict["revision"]),
        expected_policy_revision=int(conflict["policy_revision"]),
        expected_domain_revocation_epoch=int(conflict["domain_revocation_epoch"]),
        expected_recipient_credential_epoch=int(
            conflict["recipient_credential_epoch"]
        ),
        expected_member_event_ids=frozenset(
            str(member["event_id"]) for member in conflict["members"]
        ),
        release_event_ids=release_event_ids,
        reject_event_ids=reject_event_ids,
        reason_code=reason_code,
    )


def test_peer_task_is_invisible_until_exact_owner_approval_and_resumes_once(
    store,
    peer_actor,
    subordinate_actor,
    admin_actor,
    now,
    relationship_approval_authority,
):
    service = _service(store, relationship_approval_authority)
    request = _request(peer_actor)
    event = _event(request, key="peer-proposal-0001")

    pending = service.submit_event(request, event, when=now)
    duplicate = service.submit_event(request, event, when=now)

    assert pending["fact"] == DeliveryFact.PENDING_HUMAN.value
    assert duplicate["duplicate"] is True
    assert duplicate["proposal_id"] == pending["proposal_id"]
    assert pending["state"] == TaskProposalState.PENDING.value
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0
    assert store.fetch_one("SELECT COUNT(*) AS count FROM recipients")["count"] == 0
    proposal = store.fetch_one(
        "SELECT * FROM task_custody_proposals WHERE proposal_id=?", (pending["proposal_id"],)
    )
    assert "sealed work bytes" not in proposal["event_encrypted"]
    assert "catalog:alpha" not in proposal["event_encrypted"]
    assert service.pending_for_owner(
        actor=peer_actor,
        collaboration_scope_id=_COLLABORATION_SCOPE_ID,
        when=now,
    ) == []
    assert service.pending_for_owner(
        actor=admin_actor,
        collaboration_scope_id=_COLLABORATION_SCOPE_ID,
        when=now,
    ) == []
    summaries = service.pending_for_owner(
        actor=subordinate_actor,
        collaboration_scope_id=_COLLABORATION_SCOPE_ID,
        when=now,
    )
    assert len(summaries) == 1
    assert "instruction" not in str(summaries[0])
    assert "catalog:alpha" not in str(summaries[0])
    substituted = event.model_copy(
        update={
            "payload": {
                "instruction": "substituted",
                "protected_resource": "catalog:alpha",
                "authorization_context": event.payload["authorization_context"],
            },
            "payload_digest": "0" * 64,
        }
    )
    # Repairing the payload digest still cannot substitute content under the
    # committed idempotency key/request digest.
    substituted = substituted.model_copy(update={"payload_digest": canonical_digest(substituted.payload)})
    with pytest.raises(IdempotencyConflict):
        service.submit_event(request, substituted, when=now)

    with pytest.raises(AuthorizationError):
        service.approve(
            actor=admin_actor,
            proposal_id=pending["proposal_id"],
            expected_request_digest=pending["request_digest"],
            expected_revision=1,
            when=now,
        )
    with pytest.raises(AuthorizationError):
        service.approve(
            actor=subordinate_actor,
            proposal_id=pending["proposal_id"],
            expected_request_digest="0" * 64,
            expected_revision=1,
            when=now,
        )

    resumed = service.approve(
        actor=subordinate_actor,
        proposal_id=pending["proposal_id"],
        expected_request_digest=pending["request_digest"],
        expected_revision=1,
        when=now,
    )
    assert resumed.state is TaskProposalState.RESUMED
    assert resumed.fact is DeliveryFact.ACCEPTED_QUEUED
    inbox = _reconcile(service.mailbox, subordinate_actor)
    assert len(inbox) == 1
    assert inbox[0]["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert inbox[0]["payload"] is None
    assert inbox[0]["payload_available"] is False
    assert inbox[0]["payload_access"] == "task_grant_required"
    assert inbox[0]["payload_withheld_reason"] == "exact_task_grant_required"
    assert inbox[0]["custody_reference"]["payload_digest"] == event.payload_digest
    with pytest.raises(ConflictError):
        service.approve(
            actor=subordinate_actor,
            proposal_id=pending["proposal_id"],
            expected_request_digest=pending["request_digest"],
            expected_revision=1,
            when=now,
        )
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 1


def test_denial_expiry_and_policy_drift_never_create_executable_mail(
    store, peer_actor, subordinate_actor, now, relationship_approval_authority
):
    service = _service(store, relationship_approval_authority)
    denied = service.submit_event(
        _request(peer_actor), _event(_request(peer_actor), key="peer-denial-0001"), when=now
    )
    outcome = service.deny(
        actor=subordinate_actor,
        proposal_id=denied["proposal_id"],
        expected_request_digest=denied["request_digest"],
        expected_revision=1,
        reason_code="declined_by_owner",
        when=now,
    )
    assert outcome.state is TaskProposalState.DENIED

    expiring_request = _request(peer_actor)
    expiring = service.submit_event(
        expiring_request,
        _event(expiring_request, key="peer-expiry-0001"),
        proposal_expires_at=now + timedelta(seconds=1),
        when=now,
    )
    assert service.expire_due(authoritative_now=now + timedelta(seconds=1)) == 1
    with pytest.raises(ConflictError):
        service.approve(
            actor=subordinate_actor,
            proposal_id=expiring["proposal_id"],
            expected_request_digest=expiring["request_digest"],
            expected_revision=1,
            when=now + timedelta(seconds=1),
        )

    drifting_request = _request(peer_actor)
    drifting = service.submit_event(
        drifting_request, _event(drifting_request, key="peer-drift-00001"), when=now
    )
    with store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='domain-a'")
    invalid = service.approve(
        actor=subordinate_actor,
        proposal_id=drifting["proposal_id"],
        expected_request_digest=drifting["request_digest"],
        expected_revision=1,
        when=now,
    )
    assert invalid.state is TaskProposalState.INVALIDATED
    assert _reconcile(service.mailbox, subordinate_actor) == []
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 0


def test_new_exact_edge_requires_explicit_revision_reauthorization(
    store, peer_actor, subordinate_actor, now, relationship_approval_authority
):
    service = _service(store, relationship_approval_authority)
    request = _request(peer_actor)
    pending = service.submit_event(request, _event(request, key="peer-edge-000001"), when=now)
    _insert_edge(
        store,
        now,
        approval_authority=relationship_approval_authority,
    )

    owner_attempt = service.approve(
        actor=subordinate_actor,
        proposal_id=pending["proposal_id"],
        expected_request_digest=pending["request_digest"],
        expected_revision=1,
        when=now,
    )
    assert owner_attempt.state is TaskProposalState.INVALIDATED
    assert _reconcile(service.mailbox, subordinate_actor) == []

    second_request = _request(peer_actor)
    # Create another proposal first by using a relationship revision expectation
    # that cannot silently bind the newly present edge.
    second_request = second_request.model_copy(update={"expected_relationship_revision": 2})
    second = service.submit_event(
        second_request, _event(second_request, key="peer-edge-000002"), when=now
    )
    resumed = service.reauthorize_with_current_edge(
        actor=peer_actor,
        proposal_id=second["proposal_id"],
        expected_request_digest=second["request_digest"],
        expected_revision=1,
        expected_relationship_revision=1,
        when=now,
    )
    assert resumed.state is TaskProposalState.RESUMED
    assert _reconcile(service.mailbox, subordinate_actor)[0]["fact"] == DeliveryFact.ACCEPTED_QUEUED.value


def test_approval_race_has_one_winner_and_one_event(
    store, peer_actor, subordinate_actor, now, relationship_approval_authority
):
    service = _service(store, relationship_approval_authority)
    request = _request(peer_actor)
    pending = service.submit_event(request, _event(request, key="peer-race-000001"), when=now)

    def approve_once():
        try:
            return service.approve(
                actor=subordinate_actor,
                proposal_id=pending["proposal_id"],
                expected_request_digest=pending["request_digest"],
                expected_revision=1,
                when=now,
            ).state.value
        except ConflictError:
            return "conflict"

    with ThreadPoolExecutor(max_workers=2) as pool:
        outcomes = list(pool.map(lambda _value: approve_once(), range(2)))
    assert sorted(outcomes) == ["conflict", "resumed"]
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 1


def test_current_downward_edge_auto_queues_without_privilege_inheritance(
    store, admin_actor, subordinate_actor, now, relationship_approval_authority
):
    _insert_edge(
        store,
        now,
        approval_authority=relationship_approval_authority,
        administrator="admin-harness",
        subordinate="sub-harness",
    )
    service = _service(store, relationship_approval_authority)
    request = _request(admin_actor)
    result = service.submit_event(
        request,
        _event(request, key="downward-custody-0001"),
        ingress=TaskIngressKind.DIRECT,
        when=now,
    )
    assert result["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert result["data_access_authorized"] is False
    assert result["effect_authorized"] is False
    assert result.get("proposal_id") is None
    assert _reconcile(service.mailbox, subordinate_actor)[0]["fact"] == DeliveryFact.ACCEPTED_QUEUED.value


def test_missing_deadline_is_server_derived_bound_persisted_and_retry_stable(
    store,
    admin_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    _insert_edge(
        store,
        now,
        approval_authority=relationship_approval_authority,
        administrator="admin-harness",
        subordinate="sub-harness",
        expires_in=timedelta(minutes=30),
        data_classes=("C1", "C2"),
    )
    service = _service(store, relationship_approval_authority)
    request = _request(
        admin_actor,
        data_classes=frozenset({Classification.C2_RESTRICTED}),
    )
    event = _event(
        request,
        key="derived-deadline-0001",
        secret="c2 relationship custody canary",
        created_at=now,
    )

    accepted = service.submit_event(request, event, when=now)
    expected = now + timedelta(minutes=30) - timedelta(seconds=1)
    expected_json = expected.isoformat().replace("+00:00", "Z")
    assert accepted["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert accepted["effective_deadline"] == expected_json

    row = store.fetch_one("SELECT * FROM events WHERE event_id=?", (event.event_id,))
    metadata = json.loads(row["envelope_json"])
    assert row["effect_deadline"] == int(expected.timestamp())
    assert row["delivery_expires_at"] == int(expected.timestamp())
    assert metadata["effect_deadline"] == expected_json
    assert metadata["delivery_expires_at"] == expected_json
    assert metadata["payload_access"] == "task_grant_required"

    duplicate = service.submit_event(
        request,
        event,
        when=now + timedelta(minutes=10),
    )
    assert duplicate["duplicate"] is True
    assert duplicate["event_id"] == event.event_id
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 1

    item = _reconcile(service.mailbox, subordinate_actor)[0]
    assert item["payload"] is None
    assert item["payload_available"] is False
    assert item["payload_withheld_reason"] == "exact_task_grant_required"
    assert "c2 relationship custody canary" not in json.dumps(item, sort_keys=True)
    assert BackgroundHarnessIntegration._mailbox_item(item) == (
        event.event_id,
        item["cursor"],
        item["envelope_digest"],
    )


def test_missing_deadline_uses_scope_duration_when_relationship_outlives_scope(
    store,
    admin_actor,
    now,
    relationship_approval_authority,
):
    _insert_edge(
        store,
        now,
        approval_authority=relationship_approval_authority,
        administrator="admin-harness",
        subordinate="sub-harness",
        expires_in=timedelta(hours=2),
    )
    service = _service(store, relationship_approval_authority)
    request = _request(admin_actor)
    event = _event(
        request,
        key="derived-scope-duration-0001",
        created_at=now,
    )

    accepted = service.submit_event(request, event, when=now)
    expected = now + timedelta(hours=1)
    assert accepted["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert accepted["effective_deadline"] == expected.isoformat().replace("+00:00", "Z")
    persisted = store.fetch_one(
        "SELECT effect_deadline,delivery_expires_at FROM events WHERE event_id=?",
        (event.event_id,),
    )
    assert persisted["effect_deadline"] == int(expected.timestamp())
    assert persisted["delivery_expires_at"] == int(expected.timestamp())


def test_explicit_deadline_is_exact_and_past_or_substituted_deadline_is_rejected(
    store,
    admin_actor,
    now,
    relationship_approval_authority,
):
    _insert_edge(
        store,
        now,
        approval_authority=relationship_approval_authority,
        administrator="admin-harness",
        subordinate="sub-harness",
    )
    service = _service(store, relationship_approval_authority)
    exact_deadline = now + timedelta(minutes=20)
    request = _request(admin_actor, deadline=exact_deadline)
    event = _event(
        request,
        key="explicit-deadline-0001",
        created_at=now,
    )

    accepted = service.submit_event(request, event, when=now)
    assert accepted["effective_deadline"] == exact_deadline.isoformat().replace("+00:00", "Z")
    persisted = store.fetch_one(
        "SELECT effect_deadline,delivery_expires_at FROM events WHERE event_id=?",
        (event.event_id,),
    )
    assert persisted["effect_deadline"] == int(exact_deadline.timestamp())
    assert persisted["delivery_expires_at"] == int(exact_deadline.timestamp())

    substituted_request = _request(
        admin_actor,
        deadline=exact_deadline + timedelta(seconds=1),
    )
    with pytest.raises(AuthorizationError, match="effect deadline"):
        service.submit_event(
            substituted_request,
            _event(
                request,
                key="substituted-deadline-0001",
                created_at=now,
            ),
            when=now,
        )

    past = now - timedelta(seconds=1)
    past_request = _request(admin_actor, deadline=past)
    with pytest.raises(ValidationError, match="future"):
        service.submit_event(
            past_request,
            _event(past_request, key="past-deadline-0001", created_at=now),
            when=now,
        )

    with pytest.raises(PydanticValidationError, match="deadline"):
        _request(admin_actor, deadline=datetime(2026, 7, 12, 12, 30))
    with pytest.raises(PydanticValidationError, match="deadline"):
        _request(admin_actor, deadline="2026-07-12T12:30:00Z")  # type: ignore[arg-type]


def test_legacy_task_without_marker_redacts_and_marker_tamper_fails_closed(
    store,
    admin_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
    monkeypatch,
):
    monkeypatch.setattr("agentnet.mailbox.service.time.time", lambda: now.timestamp())
    collaboration_scopes = _TaskScopes()
    legacy_mailbox = MailboxService(
        store,
        collaboration_scopes=collaboration_scopes,
    )
    legacy = new_event(
        domain_id=admin_actor.domain_id,
        actor=admin_actor,
        event_type=EventType.TASK_ASSIGNMENT,
        classification=Classification.C2_RESTRICTED,
        payload={
            "instruction": "legacy c2 canary",
            "authorization_context": _authorization_context(),
        },
        idempotency_key="legacy-task-no-marker-0001",
        recipients=(subordinate_actor.harness_id,),
        task_id="legacy-task-no-marker",
        retention_delete_at=now + timedelta(hours=1),
    ).model_copy(update={"created_at": now})
    legacy_mailbox.accept(legacy)
    legacy_item = _reconcile(legacy_mailbox, subordinate_actor)[0]
    assert legacy_item["event"].get("payload_access") is None
    assert legacy_item["payload"] is None
    assert legacy_item["payload_access"] == "task_grant_required"
    assert "legacy c2 canary" not in json.dumps(legacy_item, sort_keys=True)

    _insert_edge(
        store,
        now,
        approval_authority=relationship_approval_authority,
        administrator="admin-harness",
        subordinate="sub-harness",
    )
    service = _service(store, relationship_approval_authority)
    request = _request(admin_actor)
    protected = _event(
        request,
        key="marker-tamper-0001",
        created_at=now,
    )
    service.submit_event(request, protected, when=now)
    row = store.fetch_one("SELECT envelope_json FROM events WHERE event_id=?", (protected.event_id,))
    metadata = json.loads(row["envelope_json"])
    metadata.pop("payload_access")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE events SET envelope_json=? WHERE event_id=?",
            (canonical_json(metadata).decode("utf-8"), protected.event_id),
        )
    with pytest.raises(ConflictError, match="immutable envelope"):
        _reconcile(service.mailbox, subordinate_actor)


def test_self_approval_and_recipient_key_drift_fail_closed(
    store, peer_actor, subordinate_actor, now, relationship_approval_authority
):
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,
                capabilities_json,credential_epoch,created_at
            ) VALUES('peer-second-harness','domain-a','peer-human','codex','peer second',
                     'active','os_bound','{}',1,?)""",
            (int(now.timestamp()),),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES('peer-second-credential','peer-second-harness','peer-second-key','test',
                     'active',1,?,?)""",
            (int(now.timestamp()) - 1, int(now.timestamp()) + 3600),
        )
    same_owner = peer_actor.model_copy(
        update={
            "harness_id": "peer-second-harness",
            "credential_id": "peer-second-credential",
        }
    )
    service = _service(store, relationship_approval_authority)
    request = _request(peer_actor, recipient="peer-second-harness")
    pending = service.submit_event(
        request, _event(request, key="self-approval-proposal-0001"), when=now
    )
    with pytest.raises(AuthorizationError, match="non-self"):
        service.approve(
            actor=same_owner,
            proposal_id=pending["proposal_id"],
            expected_request_digest=pending["request_digest"],
            expected_revision=1,
            when=now,
        )
    assert _reconcile(service.mailbox, same_owner) == []

    recipient_request = _request(peer_actor)
    recipient_pending = service.submit_event(
        recipient_request,
        _event(recipient_request, key="recipient-key-drift-0001"),
        when=now,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET credential_epoch=2 WHERE harness_id='sub-harness'"
        )
    invalid = service.approve(
        actor=subordinate_actor,
        proposal_id=recipient_pending["proposal_id"],
        expected_request_digest=recipient_pending["request_digest"],
        expected_revision=1,
        when=now,
    )
    assert invalid.state is TaskProposalState.INVALIDATED
    assert _reconcile(service.mailbox, subordinate_actor) == []


@pytest.mark.parametrize("arrival_order", [("admin", "peer"), ("peer", "admin")])
def test_incompatible_downward_intents_atomically_hold_both_and_owner_partitions_exact_members(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
    arrival_order,
):
    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
        )
    actors = {"admin": admin_actor, "peer": peer_actor}
    service = _service(store, relationship_approval_authority)
    accepted: dict[str, dict[str, object]] = {}
    for index, name in enumerate(arrival_order):
        request = _request(actors[name])
        accepted[name] = service.submit_event(
            request,
            _event(request, key=f"intent-conflict-{name}-{index}-0001"),
            when=now,
        )

    assert accepted[arrival_order[0]]["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert accepted[arrival_order[1]]["fact"] == DeliveryFact.CONFLICT_PENDING.value
    inbox = _reconcile(service.mailbox, subordinate_actor)
    assert {item["fact"] for item in inbox} == {DeliveryFact.CONFLICT_PENDING.value}
    pending = service.pending_conflicts_for_owner(actor=subordinate_actor, when=now)
    assert len(pending) == 1
    conflict = pending[0]
    member_ids = frozenset(member["event_id"] for member in conflict["members"])
    assert member_ids == frozenset(str(value["event_id"]) for value in accepted.values())
    release_id = str(accepted["admin"]["event_id"])
    reject_id = str(accepted["peer"]["event_id"])
    outcome = service.adjudicate_conflict(
        actor=subordinate_actor,
        decision=TaskConflictAdjudication(
            conflict_id=conflict["conflict_id"],
            expected_revision=conflict["revision"],
            expected_policy_revision=conflict["policy_revision"],
            expected_domain_revocation_epoch=conflict["domain_revocation_epoch"],
            expected_recipient_credential_epoch=conflict["recipient_credential_epoch"],
            expected_member_event_ids=member_ids,
            release_event_ids=frozenset({release_id}),
            reject_event_ids=frozenset({reject_id}),
            reason_code="subordinate_owner_partition",
        ),
        when=now,
    )

    assert outcome.data_access_authorized is False
    assert outcome.semantic_processing_authorized is False
    assert outcome.tool_authorized is False
    assert outcome.effect_authorized is False
    facts = {
        row["event_id"]: row["current_fact"]
        for row in store.fetch_all(
            "SELECT event_id,current_fact FROM recipients WHERE recipient_id='sub-harness'"
        )
    }
    assert facts[release_id] == DeliveryFact.QUEUED.value
    assert facts[reject_id] == DeliveryFact.REJECTED_BEFORE_ACCEPT.value
    with pytest.raises(ConflictError):
        service.adjudicate_conflict(
            actor=subordinate_actor,
            decision=TaskConflictAdjudication(
                conflict_id=conflict["conflict_id"],
                expected_revision=conflict["revision"],
                expected_policy_revision=conflict["policy_revision"],
                expected_domain_revocation_epoch=conflict["domain_revocation_epoch"],
                expected_recipient_credential_epoch=conflict["recipient_credential_epoch"],
                expected_member_event_ids=member_ids,
                release_event_ids=frozenset({release_id}),
                reject_event_ids=frozenset({reject_id}),
                reason_code="replay_attempt",
            ),
            when=now,
        )


def test_truly_concurrent_incompatible_task_admissions_hold_both_exact_intents(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    """Different authorized administrators cannot win by transaction arrival order."""

    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
        )

    primary = _service(store, relationship_approval_authority)
    parallel_store = SQLiteStore(store.path, store.cipher)
    parallel = _service(parallel_store, relationship_approval_authority)
    barrier = Barrier(2)
    submissions = (
        (
            primary,
            _request(admin_actor),
            "concurrent-conflicting-admin-task-0001",
            "admin requests exclusive catalog rewrite",
        ),
        (
            parallel,
            _request(peer_actor),
            "concurrent-conflicting-peer-task-0001",
            "peer requests incompatible exclusive catalog rewrite",
        ),
    )

    def submit(value):
        service, request, key, instruction = value
        event = _event(request, key=key, secret=instruction)
        barrier.wait(timeout=5)
        return service.submit_event(request, event, when=now)

    try:
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(submit, submissions))
    finally:
        parallel_store.close()

    assert sorted(result["fact"] for result in results) == [
        DeliveryFact.ACCEPTED_QUEUED.value,
        DeliveryFact.CONFLICT_PENDING.value,
    ]
    event_ids = frozenset(str(result["event_id"]) for result in results)
    pending = primary.pending_conflicts_for_owner(actor=subordinate_actor, when=now)
    assert len(pending) == 1
    assert frozenset(member["event_id"] for member in pending[0]["members"]) == event_ids
    assert {
        row["current_fact"]
        for row in store.fetch_all(
            "SELECT current_fact FROM recipients WHERE recipient_id='sub-harness'"
        )
    } == {DeliveryFact.CONFLICT_PENDING.value}
    assert {
        row["state"]
        for row in store.fetch_all(
            "SELECT state FROM task_execution_intents WHERE event_id IN (?,?)",
            tuple(sorted(event_ids)),
        )
    } == {"conflict_pending"}
    assert store.fetch_one("SELECT COUNT(*) AS count FROM task_conflicts")["count"] == 1
    assert store.verify_audit_chain()[0] is True


def test_simultaneous_opposite_conflict_adjudications_have_exactly_one_winner(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
        )
    primary = _service(store, relationship_approval_authority)
    event_ids: dict[str, str] = {}
    for name, actor in (("admin", admin_actor), ("peer", peer_actor)):
        request = _request(actor)
        result = primary.submit_event(
            request,
            _event(request, key=f"opposite-adjudication-{name}-0001"),
            when=now,
        )
        event_ids[name] = str(result["event_id"])
    conflict = primary.pending_conflicts_for_owner(actor=subordinate_actor, when=now)[0]
    decisions = (
        _conflict_decision(
            conflict,
            release_event_ids=frozenset({event_ids["admin"]}),
            reject_event_ids=frozenset({event_ids["peer"]}),
            reason_code="prefer_admin",
        ),
        _conflict_decision(
            conflict,
            release_event_ids=frozenset({event_ids["peer"]}),
            reject_event_ids=frozenset({event_ids["admin"]}),
            reason_code="prefer_peer",
        ),
    )
    parallel_store = SQLiteStore(store.path, store.cipher)
    parallel = _service(parallel_store, relationship_approval_authority)
    barrier = Barrier(2)

    def decide(value):
        service, decision = value
        barrier.wait(timeout=5)
        try:
            outcome = service.adjudicate_conflict(
                actor=subordinate_actor,
                decision=decision,
                when=now,
            )
        except ConflictError:
            return "conflict", ()
        return "resolved", outcome.released_event_ids

    try:
        with ThreadPoolExecutor(max_workers=2) as pool:
            outcomes = list(
                pool.map(decide, ((primary, decisions[0]), (parallel, decisions[1])))
            )
    finally:
        parallel_store.close()

    assert sorted(state for state, _released in outcomes) == ["conflict", "resolved"]
    released = next(released for state, released in outcomes if state == "resolved")
    assert len(released) == 1
    released_id = released[0]
    rejected_id = next(
        event_id for event_id in event_ids.values() if event_id != released_id
    )
    facts = {
        str(row["event_id"]): str(row["current_fact"])
        for row in store.fetch_all(
            "SELECT event_id,current_fact FROM recipients WHERE recipient_id='sub-harness'"
        )
    }
    assert facts == {
        released_id: DeliveryFact.QUEUED.value,
        rejected_id: DeliveryFact.REJECTED_BEFORE_ACCEPT.value,
    }
    persisted = store.fetch_one(
        "SELECT state,revision FROM task_conflicts WHERE conflict_id=?",
        (conflict["conflict_id"],),
    )
    assert persisted["state"] == "resolved"
    assert int(persisted["revision"]) == int(conflict["revision"]) + 1
    assert store.verify_audit_chain()[0] is True


def _prepare_overlapping_conflicts(
    *,
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    resources = ("catalog:alpha", "catalog:beta")
    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
            resources=resources,
        )
    service = _service(store, relationship_approval_authority)
    requests = {
        "shared": _request(admin_actor).model_copy(
            update={
                "resources": frozenset(resources),
                "intent": _exclusive_intent(*resources),
            }
        ),
        "alpha": _request(peer_actor).model_copy(
            update={
                "resources": frozenset({"catalog:alpha"}),
                "intent": _exclusive_intent("catalog:alpha"),
            }
        ),
        "beta": _request(peer_actor).model_copy(
            update={
                "resources": frozenset({"catalog:beta"}),
                "intent": _exclusive_intent("catalog:beta"),
            }
        ),
    }
    event_ids: dict[str, str] = {}
    for name in ("shared", "alpha", "beta"):
        request = requests[name]
        result = service.submit_event(
            request,
            _event(request, key=f"overlapping-conflict-{name}-0001"),
            when=now,
        )
        event_ids[name] = str(result["event_id"])
    conflicts = {
        str(conflict["resource_key"]): conflict
        for conflict in service.pending_conflicts_for_owner(
            actor=subordinate_actor,
            when=now,
        )
    }
    assert set(conflicts) == {"catalog:alpha", "catalog:beta"}
    assert {
        str(member["event_id"]) for member in conflicts["catalog:alpha"]["members"]
    } == {event_ids["shared"], event_ids["alpha"]}
    assert {
        str(member["event_id"]) for member in conflicts["catalog:beta"]["members"]
    } == {event_ids["shared"], event_ids["beta"]}
    return service, event_ids, conflicts


def test_overlapping_conflicts_release_shared_member_only_after_every_partition_resolves(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    service, event_ids, conflicts = _prepare_overlapping_conflicts(
        store=store,
        admin_actor=admin_actor,
        peer_actor=peer_actor,
        subordinate_actor=subordinate_actor,
        now=now,
        relationship_approval_authority=relationship_approval_authority,
    )
    alpha = conflicts["catalog:alpha"]
    service.adjudicate_conflict(
        actor=subordinate_actor,
        decision=_conflict_decision(
            alpha,
            release_event_ids=frozenset({event_ids["shared"]}),
            reject_event_ids=frozenset({event_ids["alpha"]}),
            reason_code="release_shared_alpha",
        ),
        when=now,
    )
    held = store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id='sub-harness'",
        (event_ids["shared"],),
    )
    assert held["current_fact"] == DeliveryFact.CONFLICT_PENDING.value
    assert [
        conflict["resource_key"]
        for conflict in service.pending_conflicts_for_owner(
            actor=subordinate_actor,
            when=now,
        )
    ] == ["catalog:beta"]

    beta = conflicts["catalog:beta"]
    service.adjudicate_conflict(
        actor=subordinate_actor,
        decision=_conflict_decision(
            beta,
            release_event_ids=frozenset({event_ids["shared"]}),
            reject_event_ids=frozenset({event_ids["beta"]}),
            reason_code="release_shared_beta",
        ),
        when=now,
    )
    facts = {
        str(row["event_id"]): str(row["current_fact"])
        for row in store.fetch_all(
            "SELECT event_id,current_fact FROM recipients WHERE recipient_id='sub-harness'"
        )
    }
    assert facts == {
        event_ids["shared"]: DeliveryFact.QUEUED.value,
        event_ids["alpha"]: DeliveryFact.REJECTED_BEFORE_ACCEPT.value,
        event_ids["beta"]: DeliveryFact.REJECTED_BEFORE_ACCEPT.value,
    }
    shared_intent = store.fetch_one(
        "SELECT state,continuation_applied FROM task_execution_intents WHERE event_id=?",
        (event_ids["shared"],),
    )
    assert shared_intent["state"] == "released"
    assert int(shared_intent["continuation_applied"]) == 1
    assert (
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM receipts WHERE event_id=? AND fact='queued'",
            (event_ids["shared"],),
        )["count"]
        == 1
    )
    assert service.pending_conflicts_for_owner(actor=subordinate_actor, when=now) == []
    assert store.verify_audit_chain()[0] is True


def test_terminal_shared_rejection_auto_settles_overlap_and_fences_stale_revision(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    service, event_ids, conflicts = _prepare_overlapping_conflicts(
        store=store,
        admin_actor=admin_actor,
        peer_actor=peer_actor,
        subordinate_actor=subordinate_actor,
        now=now,
        relationship_approval_authority=relationship_approval_authority,
    )
    alpha = conflicts["catalog:alpha"]
    beta = conflicts["catalog:beta"]
    stale_beta = _conflict_decision(
        beta,
        release_event_ids=frozenset({event_ids["beta"]}),
        reject_event_ids=frozenset({event_ids["shared"]}),
        reason_code="stale_beta_partition",
    )
    service.adjudicate_conflict(
        actor=subordinate_actor,
        decision=_conflict_decision(
            alpha,
            release_event_ids=frozenset({event_ids["alpha"]}),
            reject_event_ids=frozenset({event_ids["shared"]}),
            reason_code="reject_shared_terminal",
        ),
        when=now,
    )

    beta_row = store.fetch_one(
        "SELECT state,revision,reason_code FROM task_conflicts WHERE conflict_id=?",
        (beta["conflict_id"],),
    )
    assert beta_row["state"] == "resolved"
    assert int(beta_row["revision"]) == int(beta["revision"]) + 1
    assert beta_row["reason_code"] == "member_terminal"
    memberships = {
        str(row["event_id"]): str(row["member_state"])
        for row in store.fetch_all(
            "SELECT event_id,member_state FROM task_conflict_memberships WHERE conflict_id=?",
            (beta["conflict_id"],),
        )
    }
    assert memberships == {
        event_ids["shared"]: "rejected",
        event_ids["beta"]: "released",
    }
    with pytest.raises(ConflictError, match="no longer pending at the expected revision"):
        service.adjudicate_conflict(
            actor=subordinate_actor,
            decision=stale_beta,
            when=now,
        )
    facts = {
        str(row["event_id"]): str(row["current_fact"])
        for row in store.fetch_all(
            "SELECT event_id,current_fact FROM recipients WHERE recipient_id='sub-harness'"
        )
    }
    assert facts == {
        event_ids["shared"]: DeliveryFact.REJECTED_BEFORE_ACCEPT.value,
        event_ids["alpha"]: DeliveryFact.QUEUED.value,
        event_ids["beta"]: DeliveryFact.QUEUED.value,
    }
    assert service.pending_conflicts_for_owner(actor=subordinate_actor, when=now) == []
    assert sum(
        json.loads(row["record_json"]).get("action") == "task_conflict.auto_resolved"
        for row in store.fetch_all("SELECT record_json FROM audit_log")
    ) == 1
    assert store.verify_audit_chain()[0] is True


def test_conflict_adjudication_racing_third_admission_is_one_complete_serial_order(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
        )
    primary = _service(store, relationship_approval_authority)
    initial_ids: dict[str, str] = {}
    for name, actor in (("admin", admin_actor), ("peer", peer_actor)):
        request = _request(actor)
        result = primary.submit_event(
            request,
            _event(request, key=f"adjudication-admission-race-{name}-0001"),
            when=now,
        )
        initial_ids[name] = str(result["event_id"])
    conflict = primary.pending_conflicts_for_owner(actor=subordinate_actor, when=now)[0]
    decision = _conflict_decision(
        conflict,
        release_event_ids=frozenset({initial_ids["admin"]}),
        reject_event_ids=frozenset({initial_ids["peer"]}),
        reason_code="race_partition",
    )
    third_request = _request(admin_actor)
    third_event = _event(third_request, key="adjudication-admission-race-third-0001")
    parallel_store = SQLiteStore(store.path, store.cipher)
    parallel = _service(parallel_store, relationship_approval_authority)
    barrier = Barrier(2)

    def decide():
        barrier.wait(timeout=5)
        try:
            primary.adjudicate_conflict(
                actor=subordinate_actor,
                decision=decision,
                when=now,
            )
        except ConflictError:
            return "conflict"
        return "resolved"

    def admit():
        barrier.wait(timeout=5)
        return parallel.submit_event(third_request, third_event, when=now)

    try:
        with ThreadPoolExecutor(max_workers=2) as pool:
            decision_future = pool.submit(decide)
            admission_future = pool.submit(admit)
            decision_state = decision_future.result()
            admission = admission_future.result()
    finally:
        parallel_store.close()

    assert admission["fact"] == DeliveryFact.CONFLICT_PENDING.value
    assert store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"] == 3
    pending = primary.pending_conflicts_for_owner(actor=subordinate_actor, when=now)
    assert len(pending) == 1
    pending_ids = {str(member["event_id"]) for member in pending[0]["members"]}
    if decision_state == "resolved":
        assert pending_ids == {initial_ids["admin"], third_event.event_id}
        assert int(pending[0]["revision"]) == int(conflict["revision"]) + 2
    else:
        assert decision_state == "conflict"
        assert pending_ids == set(initial_ids.values()) | {third_event.event_id}
        assert int(pending[0]["revision"]) == int(conflict["revision"]) + 1
    assert store.verify_audit_chain()[0] is True


def test_shared_read_intents_do_not_conflict_and_explicit_intent_must_cover_scope(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
        )
    shared_read = TaskExecutionIntent(
        resources=(
            TaskResourceIntent(
                resource="catalog:alpha",
                operation="research",
                access=TaskAccessMode.READ,
                exclusivity=TaskExclusivity.SHARED,
            ),
        )
    )
    service = _service(store, relationship_approval_authority)
    for index, actor in enumerate((admin_actor, peer_actor)):
        request = _request(actor).model_copy(update={"intent": shared_read})
        result = service.submit_event(
            request,
            _event(request, key=f"shared-read-intent-{index}-0001"),
            when=now,
        )
        assert result["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
        assert result["conflict_ids"] == []
    assert service.pending_conflicts_for_owner(actor=subordinate_actor, when=now) == []
    invalid_values = _request(admin_actor).model_dump()
    invalid_values.update(
        resources=frozenset({"catalog:alpha", "catalog:beta"}),
        intent=shared_read,
    )
    with pytest.raises(PydanticValidationError, match="complete exact assignment resource set"):
        AssignmentRequest(**invalid_values)


def test_conflict_decision_rejects_wrong_owner_stale_epoch_and_incompatible_release_set(
    store,
    admin_actor,
    peer_actor,
    subordinate_actor,
    now,
    relationship_approval_authority,
):
    for administrator in ("admin-harness", "peer-harness"):
        _insert_edge(
            store,
            now,
            approval_authority=relationship_approval_authority,
            administrator=administrator,
            subordinate="sub-harness",
        )
    service = _service(store, relationship_approval_authority)
    event_ids: set[str] = set()
    for index, actor in enumerate((admin_actor, peer_actor)):
        request = _request(actor)
        result = service.submit_event(
            request,
            _event(request, key=f"negative-intent-conflict-{index}-0001"),
            when=now,
        )
        event_ids.add(str(result["event_id"]))
    conflict = service.pending_conflicts_for_owner(actor=subordinate_actor, when=now)[0]
    release_all = TaskConflictAdjudication(
        conflict_id=conflict["conflict_id"],
        expected_revision=conflict["revision"],
        expected_policy_revision=conflict["policy_revision"],
        expected_domain_revocation_epoch=conflict["domain_revocation_epoch"],
        expected_recipient_credential_epoch=conflict["recipient_credential_epoch"],
        expected_member_event_ids=frozenset(event_ids),
        release_event_ids=frozenset(event_ids),
        reason_code="release_all",
    )
    with pytest.raises(AuthorizationError):
        service.adjudicate_conflict(actor=admin_actor, decision=release_all, when=now)
    with pytest.raises(ValidationError, match="mutually incompatible"):
        service.adjudicate_conflict(actor=subordinate_actor, decision=release_all, when=now)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id='domain-a'"
        )
    release_one = release_all.model_copy(
        update={
            "release_event_ids": frozenset({sorted(event_ids)[0]}),
            "reject_event_ids": frozenset({sorted(event_ids)[1]}),
        }
    )
    with pytest.raises(ConflictError, match="epochs drifted"):
        service.adjudicate_conflict(actor=subordinate_actor, decision=release_one, when=now)
