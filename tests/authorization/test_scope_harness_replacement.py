from __future__ import annotations

import json
import pytest
from datetime import UTC, datetime, timedelta
from pathlib import Path

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import AuthorizationRequest, HumanEntitlement
from agentnet.authorization.scope_harness_replacement import (
    SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE,
    ScopeHarnessReplacementService,
)
from agentnet.errors import AuthenticationError, AuthorizationError
from agentnet.core.app import CommunicationCore
from agentnet.operations.config import ExtensionConfig
from agentnet.protocol.models import Classification
from agentnet.security.signatures import P256KeyPair


DOMAIN = "scope-replacement.example"
NOW = 1_800_000_000


def _core(tmp_path: Path, store) -> CommunicationCore:
    return CommunicationCore(
        ExtensionConfig(
            domain_id=DOMAIN,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
        ),
        store,
    )


def _issue_scope(core: CommunicationCore, owner, old_member) -> object:
    scope_id = "scope-expired-member-replacement-0001"
    proposal = CollaborationScopeProposal(
        scope_id=scope_id,
        scope_kind="direct",
        member_harness_ids=tuple(sorted((owner.harness_id, old_member.harness_id))),
        allowed_actions=("message.acknowledge", "message.read", "message.send"),
        allowed_resource_prefixes=("conversation:",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        canonical_references=(),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=NOW + 3_600,
    )
    request = core.collaboration_scopes.issuance_request(actor=owner, proposal=proposal)
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=owner.domain_id,
            principal_id=owner.principal_id,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource_pattern=f"scope:{scope_id}",
            revision=1,
            expires_at=datetime.fromtimestamp(NOW, UTC) + timedelta(hours=1),
        ),
        when=datetime.fromtimestamp(NOW, UTC),
    )
    decision = core.policy.require(
        AuthorizationRequest(
            actor=owner,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=f"scope:{scope_id}",
            policy_revision=1,
            context=request,
        ),
        when=datetime.fromtimestamp(NOW, UTC),
    )
    return core.collaboration_scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=IssuanceAuthority(actor=owner, policy_decision_id=decision.decision_id),
        when=datetime.fromtimestamp(NOW, UTC),
    )


def test_replacement_atomically_tombstones_old_member_and_activates_new_member(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    owner, _ = identity_factory(
        domain=DOMAIN,
        kind="server-agent",
        binding_assurance="os_bound",
    )
    old_member, _ = identity_factory(
        domain=DOMAIN,
        principal_id=owner.principal_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    replacement, _ = identity_factory(
        domain=DOMAIN,
        principal_id=owner.principal_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    with store.transaction() as connection:
        for actor in (owner, old_member, replacement):
            connection.execute(
                "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
                (NOW - 60, NOW + 3_600, actor.credential_id),
            )
    scope = _issue_scope(_core(tmp_path, store), owner, old_member)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
            (NOW - 3_600, NOW - 1, old_member.credential_id),
        )

    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=owner.principal_id,
        domain_id=DOMAIN,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="scope-replacement-approval.example",
    )
    service = ScopeHarnessReplacementService(store, verifier, clock=lambda: NOW)
    request = service.prepare(
        actor=owner,
        scope_id=scope.scope_id,
        old_harness_id=old_member.harness_id,
        new_harness_id=replacement.harness_id,
        role="member",
        request_id="scope-replacement-request-0001",
        issued_at=NOW,
        expires_at=NOW + 300,
    )
    approval = create_independent_approval_receipt(
        approval_key,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=NOW,
        expires_at=NOW + 300,
        authenticated_at=NOW,
    )

    result = service.replace(actor=owner, request=request, approval=approval)

    old_row = store.fetch_one(
        "SELECT * FROM collaboration_scope_members WHERE scope_id=? AND harness_id=?",
        (scope.scope_id, old_member.harness_id),
    )
    new_row = store.fetch_one(
        "SELECT * FROM collaboration_scope_members WHERE scope_id=? AND harness_id=?",
        (scope.scope_id, replacement.harness_id),
    )
    scope_row = store.fetch_one(
        "SELECT * FROM collaboration_scopes WHERE scope_id=?",
        (scope.scope_id,),
    )
    audit = json.loads(
        store.fetch_one(
            "SELECT record_json FROM audit_log WHERE record_hash=?",
            (result.audit_record_hash,),
        )["record_json"]
    )

    assert result.idempotent_repeat is False
    assert result.membership_sequence == 2
    assert result.scope_revision == 2
    assert result.scope_digest != scope.scope_digest
    assert old_row["state"] == "removed"
    assert old_row["removed_sequence"] == 2
    assert old_row["removed_at"] == NOW
    assert new_row["authority_kind"] == "principal"
    assert new_row["authority_id"] == owner.principal_id
    assert new_row["role"] == "member"
    assert new_row["state"] == "active"
    assert new_row["joined_sequence"] == 2
    assert new_row["removed_sequence"] is None
    assert new_row["joined_at"] == NOW
    assert scope_row["membership_sequence"] == 2
    assert scope_row["revision"] == 2
    assert scope_row["scope_digest"] == result.scope_digest
    assert scope_row["audit_record_hash"] == result.audit_record_hash
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 1
    assert audit["action"] == "collaboration_scope.harness_replaced"
    assert audit["old_harness_id"] == old_member.harness_id
    assert audit["new_harness_id"] == replacement.harness_id


def _replacement_case(store, identity_factory, tmp_path: Path):
    owner, _ = identity_factory(
        domain=DOMAIN,
        kind="server-agent",
        binding_assurance="os_bound",
    )
    old_member, _ = identity_factory(
        domain=DOMAIN,
        principal_id=owner.principal_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    replacement, _ = identity_factory(
        domain=DOMAIN,
        principal_id=owner.principal_id,
        kind="pi",
        binding_assurance="os_bound",
    )
    with store.transaction() as connection:
        for actor in (owner, old_member, replacement):
            connection.execute(
                "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
                (NOW - 60, NOW + 3_600, actor.credential_id),
            )
    core = _core(tmp_path, store)
    scope = _issue_scope(core, owner, old_member)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
            (NOW - 3_600, NOW - 1, old_member.credential_id),
        )
    approval_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=owner.principal_id,
        domain_id=DOMAIN,
        signer_key_id=approval_key.thumbprint,
        public_key_pem=approval_key.public_pem,
        allowed_purposes=frozenset({SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approval_key.thumbprint: approver},
        verifier_id="scope-replacement-approval.example",
    )
    service = ScopeHarnessReplacementService(store, verifier, clock=lambda: NOW)
    request = service.prepare(
        actor=owner,
        scope_id=scope.scope_id,
        old_harness_id=old_member.harness_id,
        new_harness_id=replacement.harness_id,
        role="member",
        request_id="scope-replacement-request-0002",
        issued_at=NOW,
        expires_at=NOW + 300,
    )
    approval = create_independent_approval_receipt(
        approval_key,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=NOW,
        expires_at=NOW + 300,
        authenticated_at=NOW,
    )
    return core, owner, old_member, replacement, service, request, approval


def test_exact_retry_returns_the_committed_result_without_reconsuming_approval(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    _core_instance, owner, _old, _new, service, request, approval = _replacement_case(
        store,
        identity_factory,
        tmp_path,
    )

    first = service.replace(actor=owner, request=request, approval=approval)
    service.clock = lambda: NOW + 301
    repeated = service.replace(actor=owner, request=request, approval=approval)

    assert first.idempotent_repeat is False
    assert repeated.idempotent_repeat is True
    assert repeated.model_dump(exclude={"idempotent_repeat"}) == first.model_dump(
        exclude={"idempotent_repeat"}
    )
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 1


def test_replacement_rejects_trusted_approver_who_is_not_scope_owner(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    _core_instance, owner, _old, _new, _service, request, _approval = _replacement_case(
        store,
        identity_factory,
        tmp_path,
    )
    other_key = P256KeyPair.generate()
    other_approver = TrustedApprover(
        principal_id="other-principal",
        domain_id=DOMAIN,
        signer_key_id=other_key.thumbprint,
        public_key_pem=other_key.public_pem,
        allowed_purposes=frozenset({SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {other_key.thumbprint: other_approver},
        verifier_id="scope-replacement-approval.example",
    )
    service = ScopeHarnessReplacementService(store, verifier, clock=lambda: NOW)
    approval = create_independent_approval_receipt(
        other_key,
        approver=other_approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=SCOPE_HARNESS_REPLACEMENT_APPROVAL_PURPOSE,
        canonical_transaction=request.canonical_transaction,
        issued_at=NOW,
        expires_at=NOW + 300,
        authenticated_at=NOW,
    )

    with pytest.raises(AuthorizationError, match="exact owner approval"):
        service.replace(actor=owner, request=request, approval=approval)


def test_replacement_rolls_back_membership_scope_and_replay_on_audit_failure(
    store,
    identity_factory,
    tmp_path: Path,
    monkeypatch,
) -> None:
    _core_instance, owner, old_member, replacement, service, request, approval = (
        _replacement_case(store, identity_factory, tmp_path)
    )

    def fail_audit(*_args, **_kwargs):
        raise RuntimeError("synthetic audit outage")

    monkeypatch.setattr(store, "append_audit", fail_audit)
    with pytest.raises(RuntimeError, match="synthetic audit outage"):
        service.replace(actor=owner, request=request, approval=approval)

    old_row = store.fetch_one(
        "SELECT state,removed_sequence FROM collaboration_scope_members "
        "WHERE scope_id=? AND harness_id=?",
        (request.scope_id, old_member.harness_id),
    )
    scope_row = store.fetch_one(
        "SELECT revision,membership_sequence,scope_digest FROM collaboration_scopes "
        "WHERE scope_id=?",
        (request.scope_id,),
    )
    assert old_row["state"] == "active"
    assert old_row["removed_sequence"] is None
    assert (
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM collaboration_scope_members "
            "WHERE scope_id=? AND harness_id=?",
            (request.scope_id, replacement.harness_id),
        )["count"]
        == 0
    )
    assert scope_row["revision"] == request.expected_scope_revision
    assert scope_row["membership_sequence"] == request.expected_membership_sequence
    assert scope_row["scope_digest"] == request.expected_scope_digest
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 0


def test_replacement_rejects_a_receipt_for_a_changed_transaction_without_mutation(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    _core_instance, owner, _old, _new, service, request, approval = _replacement_case(
        store,
        identity_factory,
        tmp_path,
    )
    changed = request.model_copy(update={"request_id": "scope-replacement-request-changed"})

    with pytest.raises(AuthenticationError, match="transaction binding mismatch"):
        service.replace(actor=owner, request=changed, approval=approval)

    scope_row = store.fetch_one(
        "SELECT revision,membership_sequence FROM collaboration_scopes WHERE scope_id=?",
        (request.scope_id,),
    )
    assert scope_row["revision"] == request.expected_scope_revision
    assert scope_row["membership_sequence"] == request.expected_membership_sequence
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 0


def test_replacement_requires_the_old_current_credential_to_be_expired(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    _core_instance, owner, old_member, replacement, service, _request, _approval = (
        _replacement_case(store, identity_factory, tmp_path)
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id=?",
            (NOW + 3_600, old_member.credential_id),
        )

    with pytest.raises(AuthorizationError, match="old current credential to be expired"):
        service.prepare(
            actor=owner,
            scope_id="scope-expired-member-replacement-0001",
            old_harness_id=old_member.harness_id,
            new_harness_id=replacement.harness_id,
            role="member",
            request_id="scope-replacement-request-0003",
            issued_at=NOW,
            expires_at=NOW + 300,
        )


def test_removed_harness_stays_denied_after_reactivation_while_replacement_is_authorized(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    core, owner, old_member, replacement, service, request, approval = _replacement_case(
        store,
        identity_factory,
        tmp_path,
    )
    service.replace(actor=owner, request=request, approval=approval)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id=?",
            (NOW + 3_600, old_member.credential_id),
        )

    with pytest.raises(AuthorizationError, match="collaboration scope is unavailable"):
        core.collaboration_scopes.require(
            actor=old_member,
            scope_id=request.scope_id,
            action="message.send",
            resource="conversation:direct",
            target_harness_ids=(owner.harness_id,),
            when=datetime.fromtimestamp(NOW, UTC),
        )
    authorized = core.collaboration_scopes.require(
        actor=replacement,
        scope_id=request.scope_id,
        action="message.send",
        resource="conversation:direct",
        target_harness_ids=(owner.harness_id,),
        when=datetime.fromtimestamp(NOW, UTC),
    )
    assert authorized.member_harness_ids == tuple(
        sorted((owner.harness_id, replacement.harness_id))
    )


def test_replacement_rejects_a_new_harness_owned_by_another_principal(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    _core_instance, owner, old_member, _replacement, service, request, _approval = (
        _replacement_case(store, identity_factory, tmp_path)
    )
    foreign, _ = identity_factory(
        domain=DOMAIN,
        kind="pi",
        binding_assurance="os_bound",
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
            (NOW - 60, NOW + 3_600, foreign.credential_id),
        )

    with pytest.raises(AuthorizationError, match="same-principal harnesses"):
        service.prepare(
            actor=owner,
            scope_id=request.scope_id,
            old_harness_id=old_member.harness_id,
            new_harness_id=foreign.harness_id,
            role="member",
            request_id="scope-replacement-request-foreign",
            issued_at=NOW,
            expires_at=NOW + 300,
        )


def test_replacement_allows_an_active_same_principal_sibling_to_open_owner_ceremony(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    _core_instance, owner, old_member, replacement, service, request, _approval = (
        _replacement_case(store, identity_factory, tmp_path)
    )

    prepared = service.prepare(
        actor=replacement,
        scope_id=request.scope_id,
        old_harness_id=old_member.harness_id,
        new_harness_id=replacement.harness_id,
        role="member",
        request_id="scope-replacement-request-sibling",
        issued_at=NOW,
        expires_at=NOW + 300,
    )

    assert prepared.owner_principal_id == owner.principal_id
    assert prepared.owner_harness_id == owner.harness_id
    assert prepared.new_harness_id == replacement.harness_id
