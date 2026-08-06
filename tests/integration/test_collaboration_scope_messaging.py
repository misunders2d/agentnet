from __future__ import annotations

import time
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    COLLABORATION_SCOPE_REVOKE_ACTION,
    CollaborationScope,
    CollaborationScopeProposal,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    OperationClass,
)
from agentnet.core.app import CommunicationCore
from agentnet.discovery.directory import DirectoryRecord
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.operations.config import ExtensionConfig
from agentnet.organization import AssignmentScope
from agentnet.organization.assignment import AssignmentRequest
from agentnet.organization.relationships import (
    RELATIONSHIP_CONSENT_PURPOSE,
    RelationshipService,
)
from agentnet.protocol.models import Classification, DeliveryFact, Relationship, TaskGrant
from agentnet.security.signatures import P256KeyPair, canonical_json


DOMAIN = "collaboration-messaging.example"
SCOPE_ACTIONS = tuple(
    sorted(
        {
            "message.acknowledge",
            "message.read",
            "message.send",
            "obligation.create",
            "obligation.respond",
            "task.accept",
            "task.propose",
        }
    )
)
SCOPE_RESOURCES = ("conversation:", "task:")


def _core(
    tmp_path: Path,
    store,
    *,
    approval_verifier: IndependentApprovalVerifier | None = None,
) -> CommunicationCore:
    return CommunicationCore(
        ExtensionConfig(
            domain_id=DOMAIN,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
        ),
        store,
        approval_verifier=approval_verifier,
    )


def _grant(core: CommunicationCore, actor, action: str, resource: str = "*") -> None:
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=core.policy.current_policy_revision(actor),
            expires_at=datetime.now(UTC) + timedelta(hours=1),
        )
    )


def _authority(
    core: CommunicationCore,
    actor,
    *,
    action: str,
    resource: str,
    context: dict[str, object],
) -> IssuanceAuthority:
    _grant(core, actor, action, resource)
    decision = core.policy.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=core.policy.current_policy_revision(actor),
            context=context,
        )
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def _issue_scope(
    core: CommunicationCore,
    owner,
    members: tuple[str, ...],
    *,
    scope_id: str,
) -> CollaborationScope:
    domain = core.store.fetch_one(
        "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=scope_id,
        scope_kind="direct" if len(members) == 2 else "shared",
        member_harness_ids=tuple(sorted(members)),
        allowed_actions=SCOPE_ACTIONS,
        allowed_resource_prefixes=SCOPE_RESOURCES,
        allowed_classifications=(Classification.C1_INTERNAL,),
        canonical_references=(),
        policy_revision=int(domain["policy_revision"]),
        domain_revocation_epoch=int(domain["revocation_epoch"]),
        expires_at=int(time.time()) + 3600,
    )
    request = core.collaboration_scopes.issuance_request(actor=owner, proposal=proposal)
    authority = _authority(
        core,
        owner,
        action=COLLABORATION_SCOPE_ISSUE_ACTION,
        resource=f"scope:{scope_id}",
        context=request,
    )
    return core.collaboration_scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=authority,
    )


def _revoke_scope(core: CommunicationCore, owner, scope: CollaborationScope) -> None:
    reason = "integration_revoked"
    request = core.collaboration_scopes.revocation_request(
        scope=scope,
        expected_revision=scope.revision,
        reason=reason,
    )
    authority = _authority(
        core,
        owner,
        action=COLLABORATION_SCOPE_REVOKE_ACTION,
        resource=f"scope:{scope.scope_id}",
        context=request,
    )
    core.collaboration_scopes.revoke(
        actor=owner,
        scope_id=scope.scope_id,
        expected_revision=scope.revision,
        reason=reason,
        authority=authority,
    )


def _grant_direct_message_actions(core: CommunicationCore, sender, recipient) -> None:
    _grant(core, sender, "message.send")
    _grant(core, recipient, "mailbox.read", recipient.harness_id)
    _grant(core, recipient, "mailbox.acknowledge", recipient.harness_id)


def _direct_scope_setup(core: CommunicationCore, identity_factory, *, suffix: str):
    sender, _ = identity_factory(
        domain=DOMAIN,
        kind="codex",
        binding_assurance="os_bound",
    )
    recipient, _ = identity_factory(
        domain=DOMAIN,
        kind="pi",
        binding_assurance="os_bound",
    )
    scope = _issue_scope(
        core,
        sender,
        (sender.harness_id, recipient.harness_id),
        scope_id=f"collaboration-scope-{suffix}",
    )
    _grant_direct_message_actions(core, sender, recipient)
    return sender, recipient, scope


def test_scope_issuance_persists_one_exact_versioned_sqlite_row(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    owner, _ = identity_factory(domain=DOMAIN, kind="codex", binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=DOMAIN, kind="pi", binding_assurance="os_bound")
    issued = _issue_scope(
        core,
        owner,
        (owner.harness_id, recipient.harness_id),
        scope_id="collaboration-scope-sqlite-issue-0001",
    )

    row = store.fetch_one(
        """SELECT scope_id,schema_version,state,revision,created_at,updated_at,expires_at
             FROM collaboration_scopes WHERE scope_id=?""",
        (issued.scope_id,),
    )

    assert dict(row) == {
        "scope_id": issued.scope_id,
        "schema_version": 1,
        "state": "active",
        "revision": 1,
        "created_at": issued.created_at,
        "updated_at": issued.updated_at,
        "expires_at": issued.expires_at,
    }
    assert core.collaboration_scopes.get_for_actor(
        actor=owner,
        scope_id=issued.scope_id,
    ) == issued


def test_message_event_binds_exact_scope_snapshot_and_recipient(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    sender, recipient, scope = _direct_scope_setup(core, identity_factory, suffix="event-0001")

    accepted = core.send_message(
        actor=sender,
        collaboration_scope_id=scope.scope_id,
        recipients=(recipient.harness_id,),
        payload={"text": "Hello"},
        idempotency_key="message-scope-event-0001",
    )
    item = core.mailbox(
        actor=recipient,
        collaboration_scope_id=scope.scope_id,
        after_cursor=0,
        limit=25,
    )[0]

    assert accepted["fact"] == DeliveryFact.ACCEPTED_LOCAL.value
    assert tuple(item["event"]["recipients"]) == (recipient.harness_id,)
    assert item["payload"]["authorization_context"] == scope.authorization_context()


def test_revoked_scope_blocks_queued_read_and_acknowledgement(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    sender, recipient, scope = _direct_scope_setup(core, identity_factory, suffix="revoke-0001")
    accepted = core.send_message(
        actor=sender,
        collaboration_scope_id=scope.scope_id,
        recipients=(recipient.harness_id,),
        payload={"text": "queued before revocation"},
        idempotency_key="message-scope-revoke-0001",
    )

    _revoke_scope(core, sender, scope)

    with pytest.raises(AuthorizationError):
        core.mailbox(actor=recipient, collaboration_scope_id=scope.scope_id)
    with pytest.raises(AuthorizationError):
        core.acknowledge_mailbox(
            actor=recipient,
            collaboration_scope_id=scope.scope_id,
            event_id=accepted["event_id"],
            envelope_digest=accepted["envelope_digest"],
        )


def test_stale_scope_policy_revision_denies_new_send(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    sender, recipient, scope = _direct_scope_setup(core, identity_factory, suffix="stale-0001")
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id=?",
            (DOMAIN,),
        )

    with pytest.raises(AuthorizationError, match="collaboration scope"):
        core.send_message(
            actor=sender,
            collaboration_scope_id=scope.scope_id,
            recipients=(recipient.harness_id,),
            payload={"text": "stale scope must not send"},
            idempotency_key="message-scope-stale-0001",
        )


def test_cross_domain_and_sibling_targets_are_denied_without_scope_expansion(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    sender, recipient, scope = _direct_scope_setup(core, identity_factory, suffix="boundary-0001")
    sibling, _ = identity_factory(
        domain=DOMAIN,
        principal_id=recipient.principal_id,
        kind="claude",
        binding_assurance="os_bound",
    )
    cross_domain, _ = identity_factory(
        domain="other-collaboration.example",
        kind="pi",
        binding_assurance="os_bound",
    )

    for target, key in (
        (sibling, "message-scope-sibling-0001"),
        (cross_domain, "message-scope-cross-domain-0001"),
    ):
        with pytest.raises(AuthorizationError):
            core.send_message(
                actor=sender,
                collaboration_scope_id=scope.scope_id,
                recipients=(target.harness_id,),
                payload={"text": "must stay inside exact scope"},
                idempotency_key=key,
            )


def test_send_retry_is_idempotent_inside_same_frozen_scope(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    sender, recipient, scope = _direct_scope_setup(core, identity_factory, suffix="retry-0001")
    request = {
        "actor": sender,
        "collaboration_scope_id": scope.scope_id,
        "recipients": (recipient.harness_id,),
        "payload": {"text": "same exact retry"},
        "idempotency_key": "message-scope-retry-0001",
    }

    first = core.send_message(**request)
    second = core.send_message(**request)

    assert second["duplicate"] is True
    assert second["event_id"] == first["event_id"]
    assert second["envelope_digest"] == first["envelope_digest"]


def test_recipient_resolution_does_not_disclose_visible_but_out_of_scope_endpoint(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    sender, recipient, _scope = _direct_scope_setup(core, identity_factory, suffix="resolver-0001")
    hidden, _ = identity_factory(
        domain=DOMAIN,
        kind="antigravity",
        binding_assurance="os_bound",
    )
    record = DirectoryRecord(
        record_id="directory-hidden-recipient-0001",
        record_type="agent",
        domain_id=DOMAIN,
        epoch=1,
        attributes={
            "harness_id": hidden.harness_id,
            "approved_aliases": ["Hidden planning agent"],
        },
        visible_to_principal_ids=(sender.principal_id,),
        expires_at=int(time.time()) + 3600,
    )
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO directory_records(
                record_id,record_type,domain_id,epoch,record_json,status,expires_at,updated_at
            ) VALUES(?,?,?,?,?,'active',?,?)""",
            (
                record.record_id,
                record.record_type,
                record.domain_id,
                record.epoch,
                canonical_json(record.model_dump(mode="json")).decode("utf-8"),
                record.expires_at,
                int(time.time()),
            ),
        )

    with pytest.raises(ConflictError) as failure:
        core.recipient_resolver.resolve(actor=sender, query="Hidden planning agent")

    assert str(failure.value) == "recipient could not be resolved"
    assert hidden.harness_id not in str(failure.value)
    assert recipient.harness_id not in str(failure.value)


def _conversation_entitlements(core: CommunicationCore, requester, responsible) -> None:
    for action in (
        "conversation.create",
        "conversation.structured_request.send",
        "conversation.response_obligation.create",
        "conversation.response_obligation.read",
    ):
        _grant(core, requester, action)
    _grant(core, responsible, "conversation.response_obligation.read")


def _create_obligation(
    core: CommunicationCore,
    *,
    requester,
    responsible,
    scope: CollaborationScope,
    conversation_id: str,
    idempotency_key: str,
) -> str:
    core.create_conversation(
        actor=requester,
        collaboration_scope_id=scope.scope_id,
        conversation_id=conversation_id,
        member_harness_ids=(responsible.harness_id,),
    )
    result = core.post_conversation_action(
        actor=requester,
        collaboration_scope_id=scope.scope_id,
        recipients=(responsible.harness_id,),
        conversation_id=conversation_id,
        thread_id=f"thread:{conversation_id}",
        action={
            "kind": "structured_request",
            "request_type": "status",
            "arguments": {"subject": conversation_id},
            "response_obligation": {
                "response_required": True,
                "responsible_harness_id": responsible.harness_id,
            },
        },
        idempotency_key=idempotency_key,
    )
    return result["response_obligation"]["obligation_id"]


def test_obligation_reads_and_counters_do_not_cross_scope(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core = _core(tmp_path, store)
    requester, _ = identity_factory(domain=DOMAIN, kind="codex", binding_assurance="os_bound")
    responsible, _ = identity_factory(domain=DOMAIN, kind="pi", binding_assurance="os_bound")
    _conversation_entitlements(core, requester, responsible)
    first_scope = _issue_scope(
        core,
        requester,
        (requester.harness_id, responsible.harness_id),
        scope_id="collaboration-obligation-scope-0001",
    )
    second_scope = _issue_scope(
        core,
        requester,
        (requester.harness_id, responsible.harness_id),
        scope_id="collaboration-obligation-scope-0002",
    )
    first_id = _create_obligation(
        core,
        requester=requester,
        responsible=responsible,
        scope=first_scope,
        conversation_id="conversation:obligation-one",
        idempotency_key="obligation-scope-create-0001",
    )
    second_id = _create_obligation(
        core,
        requester=requester,
        responsible=responsible,
        scope=second_scope,
        conversation_id="conversation:obligation-two",
        idempotency_key="obligation-scope-create-0002",
    )

    with pytest.raises(AuthorizationError, match="response obligation is unavailable"):
        core.response_obligation(
            actor=responsible,
            collaboration_scope_id=second_scope.scope_id,
            obligation_id=first_id,
        )
    first_rows = core.response_obligation_list(
        actor=responsible,
        collaboration_scope_id=first_scope.scope_id,
    )
    second_rows = core.response_obligation_list(
        actor=responsible,
        collaboration_scope_id=second_scope.scope_id,
    )

    assert [row["obligation_id"] for row in first_rows] == [first_id]
    assert [row["obligation_id"] for row in second_rows] == [second_id]
    assert core.response_obligation_inbox(
        actor=responsible,
        collaboration_scope_id=first_scope.scope_id,
    )["action_required"] == 1
    assert core.response_obligation_inbox(
        actor=responsible,
        collaboration_scope_id=second_scope.scope_id,
    )["action_required"] == 1


def test_downward_assignment_custody_does_not_transfer_manager_data_authority(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    manager, _ = identity_factory(domain=DOMAIN, kind="codex", binding_assurance="os_bound")
    subordinate, _ = identity_factory(domain=DOMAIN, kind="pi", binding_assurance="os_bound")
    signer = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=subordinate.principal_id,
        domain_id=DOMAIN,
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({RELATIONSHIP_CONSENT_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver},
        verifier_id="collaboration-assignment-approval",
    )
    core = _core(tmp_path, store, approval_verifier=verifier)
    scope = _issue_scope(
        core,
        manager,
        (manager.harness_id, subordinate.harness_id),
        scope_id="collaboration-assignment-scope-0001",
    )
    now = datetime.now(UTC)
    assignment_scope = AssignmentScope(
        task_types=frozenset({"research"}),
        resources=frozenset({"catalog:alpha"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        tools=frozenset({"search"}),
        max_budget=100,
        max_duration_seconds=3600,
        max_concurrency=1,
    )
    relationship = Relationship(
        domain_id=DOMAIN,
        administrator_harness_id=manager.harness_id,
        subordinate_harness_id=subordinate.harness_id,
        may_assign=True,
        assignment_scope=assignment_scope.model_dump(mode="json"),
        revision=1,
        expires_at=now + timedelta(hours=2),
    )
    proposal_expires_at = now + timedelta(minutes=10)
    resource, context = RelationshipService.proposal_binding(
        relationship,
        proposal_expires_at=proposal_expires_at,
    )
    proposal = core.relationships.propose(
        relationship,
        authority=_authority(
            core,
            manager,
            action="organization.relationship.propose",
            resource=resource,
            context=context,
        ),
        proposal_expires_at=proposal_expires_at,
        when=now,
    )
    approval = create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=RELATIONSHIP_CONSENT_PURPOSE,
        canonical_transaction=canonical_json(
            proposal.consent_transaction.model_dump(mode="json")
        ),
        issued_at=int(now.timestamp()),
        expires_at=int((now + timedelta(minutes=5)).timestamp()),
    )
    core.relationships.accept(
        relationship.relationship_id,
        actor=manager,
        approval=approval,
        expected_transaction_digest=proposal.transaction_digest,
        expected_relationship_revision=proposal.revision,
        expected_lifecycle_revision=proposal.lifecycle_revision,
        when=now,
    )
    _grant(core, manager, "data.read", "catalog:alpha")
    manager_grant = TaskGrant(
        domain_id=manager.domain_id,
        principal_id=manager.positive_authority_id,
        harness_id=manager.harness_id,
        actions=frozenset({"data.read"}),
        resources=frozenset({"catalog:alpha"}),
        input_sources=frozenset({"manager:catalog-review"}),
        output_sinks=frozenset({"manager:decision"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        max_uses=2,
        expires_at=now + timedelta(hours=1),
    )
    with store.transaction() as connection:
        manager_grant = core.grants._insert_in_transaction(
            connection,
            grant=manager_grant,
            when=now,
            issuance_evidence={"kind": "focused_manager_owned_data_read_grant"},
        )
    manager_grant_use = GrantUse(
        grant_id=manager_grant.grant_id,
        action="data.read",
        resource="catalog:alpha",
        input_source="manager:catalog-review",
        output_sink="manager:decision",
        data_class=Classification.C1_INTERNAL,
    )
    manager_read = core.policy.require(
        AuthorizationRequest(
            actor=manager,
            action="data.read",
            resource="catalog:alpha",
            operation_class=OperationClass.PROTECTED_READ,
            policy_revision=core.policy.current_policy_revision(manager),
            grant_use=manager_grant_use,
        )
    )
    request = AssignmentRequest(
        actor=manager,
        collaboration_scope_id=scope.scope_id,
        recipient_harness_id=subordinate.harness_id,
        task_type="research",
        resources=frozenset({"catalog:alpha"}),
        data_classes=frozenset({Classification.C1_INTERNAL}),
        tools=frozenset({"search"}),
        budget=50,
        concurrency=1,
        policy_revision=core.policy.current_policy_revision(manager),
    )
    custody = core.assign_task(
        request,
        payload={"summary": "research catalog alpha"},
        idempotency_key="assignment-scope-downward-0001",
    )
    _grant(core, subordinate, "data.read", "catalog:alpha")
    subordinate_read = core.policy.decide(
        AuthorizationRequest(
            actor=subordinate,
            action="data.read",
            resource="catalog:alpha",
            operation_class=OperationClass.PROTECTED_READ,
            policy_revision=core.policy.current_policy_revision(subordinate),
            grant_use=manager_grant_use,
        )
    )

    assert manager_read.allowed is True
    assert custody["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
    assert custody["data_access_authorized"] is False
    assert custody["effect_authorized"] is False
    assert subordinate_read.allowed is False
    assert subordinate_read.reason == "task_grant_actor_mismatch"
