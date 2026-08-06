from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    COLLABORATION_SCOPE_REVOKE_ACTION,
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification


def authority_for(store, *, actor, action, resource, request, when):
    policy = LocalConformancePolicyEngine(store)
    revision = policy.current_policy_revision(actor, when=when)
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=revision,
            expires_at=when + timedelta(minutes=30),
        ),
        when=when,
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=revision,
            context=request,
        ),
        when=when,
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def proposal_for(
    store,
    *,
    owner,
    members,
    scope_id,
    actions=("message.read", "message.send"),
    resources=("conversation:",),
    expires_at=None,
):
    domain = store.fetch_one(
        "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
        (owner.domain_id,),
    )
    return CollaborationScopeProposal(
        scope_id=scope_id,
        scope_kind="personal" if len(members) == 1 else "direct" if len(members) == 2 else "shared",
        member_harness_ids=tuple(sorted(member.harness_id for member in members)),
        allowed_actions=tuple(sorted(actions)),
        allowed_resource_prefixes=tuple(sorted(resources)),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=int(domain["policy_revision"]),
        domain_revocation_epoch=int(domain["revocation_epoch"]),
        expires_at=expires_at,
    )


def issue_scope(service, store, *, owner, proposal, when):
    authority = authority_for(
        store,
        actor=owner,
        action=COLLABORATION_SCOPE_ISSUE_ACTION,
        resource=f"scope:{proposal.scope_id}",
        request=service.issuance_request(actor=owner, proposal=proposal),
        when=when,
    )
    return service.issue(actor=owner, proposal=proposal, authority=authority, when=when), authority


def guest_identity(
    store,
    *,
    sponsor: VerifiedActor,
    when: datetime,
    guest_id: str = "guest-collaboration-member",
    harness_id: str = "guest-collaboration-harness",
    credential_id: str = "guest-collaboration-credential",
) -> VerifiedActor:
    epoch = int(when.timestamp())
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,
                sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,'active',?)""",
            (
                guest_id,
                sponsor.domain_id,
                "guest-home.example",
                f"pairwise-{guest_id}",
                sponsor.principal_id,
                epoch + 3_600,
            ),
        )
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,NULL,?,'federated_guest',?,'active','os_bound','{}',1,?)""",
            (harness_id, sponsor.domain_id, guest_id, harness_id, epoch - 60),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)""",
            (
                credential_id,
                harness_id,
                f"key-{credential_id}",
                "synthetic-public-key",
                epoch - 60,
                epoch + 3_600,
            ),
        )
    return VerifiedActor(
        kind=ActorKind.HOST_GUEST_HARNESS,
        domain_id=sponsor.domain_id,
        guest_id=guest_id,
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="os_bound",
    )


def test_scope_guest_member_binds_host_guest_authority_and_exact_harness(
    store,
    identity_factory,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    guest = guest_identity(store, sponsor=owner, when=now)
    service = CollaborationScopeService(store)
    proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, guest),
        scope_id="scope-guest-member-current",
    )

    scope, _authority = issue_scope(
        service,
        store,
        owner=owner,
        proposal=proposal,
        when=now,
    )

    member = store.fetch_one(
        """SELECT authority_kind,authority_id,harness_id,role,member_digest
             FROM collaboration_scope_members
            WHERE scope_id=? AND harness_id=?""",
        (scope.scope_id, guest.harness_id),
    )
    assert (
        member["authority_kind"],
        member["authority_id"],
        member["harness_id"],
        member["role"],
    ) == ("guest", guest.guest_id, guest.harness_id, "guest")
    assert member["member_digest"] == service._member_digest(
        scope_id=scope.scope_id,
        authority_kind="guest",
        authority_id=guest.guest_id,
        harness_id=guest.harness_id,
        role="guest",
        joined_at=int(now.timestamp()),
    )
    assert service.get_for_actor(actor=guest, scope_id=scope.scope_id, when=now) == scope
    assert service.require(
        actor=guest,
        scope_id=scope.scope_id,
        action="message.send",
        resource="conversation:guest-current",
        target_harness_ids=(owner.harness_id,),
        when=now,
    ) == scope
    assert [
        visibility.harness_id
        for visibility in service.active_recipient_members(
            actor=guest,
            candidate_harness_ids=(owner.harness_id,),
            when=now,
        )
    ] == [owner.harness_id]
    with pytest.raises(AuthorizationError):
        service.issue(
            actor=guest,
            proposal=proposal,
            authority=_authority,
            when=now,
        )


@pytest.mark.parametrize(
    "mutation",
    (
        "guest_revoked",
        "guest_expired",
        "harness_revoked",
        "credential_revoked",
        "credential_expired",
        "wrong_guest",
        "wrong_harness",
        "wrong_credential",
        "wrong_domain",
    ),
)
def test_scope_guest_member_requires_current_exact_host_binding(
    store,
    identity_factory,
    mutation: str,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    epoch = int(now.timestamp())
    owner, _ = identity_factory(binding_assurance="os_bound")
    guest = guest_identity(store, sponsor=owner, when=now)
    service = CollaborationScopeService(store)
    scope, _authority = issue_scope(
        service,
        store,
        owner=owner,
        proposal=proposal_for(
            store,
            owner=owner,
            members=(owner, guest),
            scope_id=f"scope-guest-current-{mutation}",
        ),
        when=now,
    )
    attempted = guest
    with store.transaction() as connection:
        if mutation == "guest_revoked":
            connection.execute(
                "UPDATE guests SET status='revoked' WHERE guest_id=?",
                (guest.guest_id,),
            )
        elif mutation == "guest_expired":
            connection.execute(
                "UPDATE guests SET expires_at=? WHERE guest_id=?",
                (epoch, guest.guest_id),
            )
        elif mutation == "harness_revoked":
            connection.execute(
                "UPDATE harnesses SET status='revoked' WHERE harness_id=?",
                (guest.harness_id,),
            )
        elif mutation == "credential_revoked":
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (guest.credential_id,),
            )
        elif mutation == "credential_expired":
            connection.execute(
                "UPDATE credentials SET expires_at=? WHERE credential_id=?",
                (epoch, guest.credential_id),
            )
        elif mutation == "wrong_guest":
            attempted = guest.model_copy(update={"guest_id": "guest-not-the-member"})
        elif mutation == "wrong_harness":
            attempted = guest.model_copy(update={"harness_id": owner.harness_id})
        elif mutation == "wrong_credential":
            attempted = guest.model_copy(update={"credential_id": owner.credential_id})
        elif mutation == "wrong_domain":
            attempted = guest.model_copy(update={"domain_id": "wrong-host.example"})

    with pytest.raises(AuthorizationError):
        service.require(
            actor=attempted,
            scope_id=scope.scope_id,
            action="message.send",
            resource="conversation:guest-current",
            target_harness_ids=(owner.harness_id,),
            when=now,
        )


@pytest.mark.parametrize(
    "mutation",
    (
        "guest_revoked",
        "guest_expired",
        "harness_revoked",
        "credential_revoked",
        "credential_expired",
        "wrong_guest_binding",
        "wrong_host_domain",
    ),
)
def test_scope_target_validation_rechecks_current_guest_authority(
    store,
    identity_factory,
    mutation: str,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    epoch = int(now.timestamp())
    owner, _ = identity_factory(binding_assurance="os_bound")
    guest = guest_identity(store, sponsor=owner, when=now)
    service = CollaborationScopeService(store)
    scope, _authority = issue_scope(
        service,
        store,
        owner=owner,
        proposal=proposal_for(
            store,
            owner=owner,
            members=(owner, guest),
            scope_id=f"scope-guest-target-{mutation}",
        ),
        when=now,
    )
    with store.transaction() as connection:
        if mutation == "guest_revoked":
            connection.execute(
                "UPDATE guests SET status='revoked' WHERE guest_id=?",
                (guest.guest_id,),
            )
        elif mutation == "guest_expired":
            connection.execute(
                "UPDATE guests SET expires_at=? WHERE guest_id=?",
                (epoch, guest.guest_id),
            )
        elif mutation == "harness_revoked":
            connection.execute(
                "UPDATE harnesses SET status='revoked' WHERE harness_id=?",
                (guest.harness_id,),
            )
        elif mutation == "credential_revoked":
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (guest.credential_id,),
            )
        elif mutation == "credential_expired":
            connection.execute(
                "UPDATE credentials SET expires_at=? WHERE credential_id=?",
                (epoch, guest.credential_id),
            )
        elif mutation == "wrong_guest_binding":
            connection.execute(
                "UPDATE harnesses SET guest_id='guest-not-the-member' WHERE harness_id=?",
                (guest.harness_id,),
            )
        elif mutation == "wrong_host_domain":
            connection.execute(
                "UPDATE guests SET host_domain_id='wrong-host.example' WHERE guest_id=?",
                (guest.guest_id,),
            )

    with pytest.raises(AuthorizationError):
        service.require(
            actor=owner,
            scope_id=scope.scope_id,
            action="message.send",
            resource="conversation:guest-target",
            target_harness_ids=(guest.harness_id,),
            when=now,
        )


@pytest.mark.parametrize(
    ("column", "value"),
    (
        ("authority_kind", "principal"),
        ("authority_id", "spoofed-authority"),
    ),
)
def test_scope_member_digest_rejects_spoofed_authority_binding(
    store,
    identity_factory,
    column: str,
    value: str,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    guest = guest_identity(store, sponsor=owner, when=now)
    service = CollaborationScopeService(store)
    scope, _authority = issue_scope(
        service,
        store,
        owner=owner,
        proposal=proposal_for(
            store,
            owner=owner,
            members=(owner, guest),
            scope_id=f"scope-member-spoof-{column}",
        ),
        when=now,
    )
    with store.transaction() as connection:
        connection.execute(
            f"""UPDATE collaboration_scope_members SET {column}=?
                  WHERE scope_id=? AND harness_id=?""",
            (value, scope.scope_id, guest.harness_id),
        )

    with pytest.raises(AuthorizationError):
        service.get_for_actor(actor=owner, scope_id=scope.scope_id, when=now)


def test_scope_keeps_colliding_local_principal_and_guest_authorities_distinct(
    store,
    identity_factory,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    guest = guest_identity(
        store,
        sponsor=owner,
        when=now,
        guest_id="colliding-local-authority",
    )
    local_human, _ = identity_factory(
        domain=owner.domain_id,
        binding_assurance="os_bound",
        principal_id=guest.guest_id,
    )
    service = CollaborationScopeService(store)
    scope, _authority = issue_scope(
        service,
        store,
        owner=owner,
        proposal=proposal_for(
            store,
            owner=owner,
            members=(owner, guest, local_human),
            scope_id="scope-colliding-member-authorities",
        ),
        when=now,
    )

    colliding_rows = store.fetch_all(
        """SELECT authority_kind,authority_id,harness_id
             FROM collaboration_scope_members
            WHERE scope_id=? AND authority_id=?
            ORDER BY authority_kind""",
        (scope.scope_id, guest.guest_id),
    )
    assert [
        (row["authority_kind"], row["authority_id"], row["harness_id"])
        for row in colliding_rows
    ] == [
        ("guest", guest.guest_id, guest.harness_id),
        ("principal", local_human.principal_id, local_human.harness_id),
    ]
    assert service.get_for_actor(actor=guest, scope_id=scope.scope_id, when=now) == scope
    assert service.get_for_actor(actor=local_human, scope_id=scope.scope_id, when=now) == scope
    with pytest.raises(AuthorizationError):
        service.get_for_actor(
            actor=guest.model_copy(
                update={
                    "harness_id": local_human.harness_id,
                    "credential_id": local_human.credential_id,
                }
            ),
            scope_id=scope.scope_id,
            when=now,
        )
    with pytest.raises(AuthorizationError):
        service.get_for_actor(
            actor=local_human.model_copy(
                update={
                    "harness_id": guest.harness_id,
                    "credential_id": guest.credential_id,
                }
            ),
            scope_id=scope.scope_id,
            when=now,
        )


def test_scope_proposal_requires_canonical_exact_bounds(store, identity_factory) -> None:
    owner, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    valid = proposal_for(
        store,
        owner=owner,
        members=(owner, recipient),
        scope_id="scope-canonical-0001",
    )

    with pytest.raises(PydanticValidationError, match="sorted and unique"):
        CollaborationScopeProposal.model_validate(
            valid.model_dump() | {"member_harness_ids": (recipient.harness_id, owner.harness_id, owner.harness_id)}
        )
    with pytest.raises(PydanticValidationError, match="unsupported action"):
        CollaborationScopeProposal.model_validate(
            valid.model_dump() | {"allowed_actions": ("data.read",)}
        )
    with pytest.raises(PydanticValidationError, match="unsupported resource"):
        CollaborationScopeProposal.model_validate(
            valid.model_dump() | {"allowed_resource_prefixes": ("principal:manager/private",)}
        )


def test_scope_issue_get_require_and_replay_preserve_exact_harnesses(
    store,
    identity_factory,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    sibling, _ = identity_factory(
        domain=owner.domain_id,
        principal_id=recipient.principal_id,
        binding_assurance="os_bound",
    )
    service = CollaborationScopeService(store, clock=lambda: now.timestamp())
    proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, recipient),
        scope_id="scope-positive-0001",
    )
    issued, authority = issue_scope(service, store, owner=owner, proposal=proposal, when=now)

    replay = service.issue(actor=owner, proposal=proposal, authority=authority, when=now)
    assert replay == issued
    assert service.get_for_actor(actor=recipient, scope_id=issued.scope_id, when=now) == issued
    assert service.require(
        actor=owner,
        scope_id=issued.scope_id,
        action="message.send",
        resource="conversation:quarterly-plan",
        target_harness_ids=(recipient.harness_id,),
        classification=Classification.C1_INTERNAL,
        when=now,
    ) == issued
    assert service.active_recipient_members(
        actor=owner,
        candidate_harness_ids=(recipient.harness_id,),
        when=now,
    )[0].model_dump() == {
        "scope_id": issued.scope_id,
        "scope_revision": issued.revision,
        "scope_policy_revision": issued.policy_revision,
        "harness_id": recipient.harness_id,
    }
    with pytest.raises(AuthorizationError, match="unavailable"):
        service.get_for_actor(actor=sibling, scope_id=issued.scope_id, when=now)
    with pytest.raises(AuthorizationError, match="exact recipients"):
        service.require(
            actor=owner,
            scope_id=issued.scope_id,
            action="message.send",
            resource="conversation:quarterly-plan",
            target_harness_ids=(sibling.harness_id,),
            classification=Classification.C1_INTERNAL,
            when=now,
        )


def test_scope_never_transfers_manager_data_authority(store, identity_factory) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    manager, _ = identity_factory(binding_assurance="os_bound")
    subordinate, _ = identity_factory(domain=manager.domain_id, binding_assurance="os_bound")
    service = CollaborationScopeService(store, clock=lambda: now.timestamp())
    proposal = proposal_for(
        store,
        owner=manager,
        members=(manager, subordinate),
        scope_id="scope-no-transfer-0001",
    )
    scope, _ = issue_scope(service, store, owner=manager, proposal=proposal, when=now)

    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=subordinate,
            scope_id=scope.scope_id,
            action="data.read",
            resource="principal:manager/private",
            target_harness_ids=(),
            classification=Classification.C1_INTERNAL,
            when=now,
        )
    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=manager,
            scope_id=scope.scope_id,
            action="message.send",
            resource="conversation:restricted",
            target_harness_ids=(subordinate.harness_id,),
            classification=Classification.C2_RESTRICTED,
            when=now,
        )
    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=manager,
            scope_id=scope.scope_id,
            action="message.send",
            resource="task:manager-private-task",
            target_harness_ids=(subordinate.harness_id,),
            classification=Classification.C1_INTERNAL,
            when=now,
        )


def test_scope_issuance_rejects_cross_domain_exact_member(store, identity_factory) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    external, _ = identity_factory(domain="other.example", binding_assurance="os_bound")
    service = CollaborationScopeService(store, clock=lambda: now.timestamp())
    proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, external),
        scope_id="scope-cross-domain-0001",
    )
    authority = authority_for(
        store,
        actor=owner,
        action=COLLABORATION_SCOPE_ISSUE_ACTION,
        resource=f"scope:{proposal.scope_id}",
        request=service.issuance_request(actor=owner, proposal=proposal),
        when=now,
    )
    with pytest.raises(AuthorizationError, match="member is unavailable"):
        service.issue(
            actor=owner,
            proposal=proposal,
            authority=authority,
            when=now,
        )


def test_scope_ambiguity_denies_without_selecting_a_sibling_scope(store, identity_factory) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    service = CollaborationScopeService(store, clock=lambda: now.timestamp())
    for scope_id in ("scope-ambiguous-0001", "scope-ambiguous-0002"):
        proposal = proposal_for(
            store,
            owner=owner,
            members=(owner, recipient),
            scope_id=scope_id,
        )
        issue_scope(service, store, owner=owner, proposal=proposal, when=now)

    with pytest.raises(ConflictError, match="ambiguous") as failure:
        service.require(
            actor=owner,
            scope_id=None,
            action="message.send",
            resource="conversation:ambiguous",
            target_harness_ids=(recipient.harness_id,),
            classification=Classification.C1_INTERNAL,
            when=now,
        )
    assert recipient.harness_id not in str(failure.value)


def test_scope_rejects_stale_policy_and_revocation_is_terminal_and_idempotent(
    store,
    identity_factory,
) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    service = CollaborationScopeService(store, clock=lambda: now.timestamp())
    proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, recipient),
        scope_id="scope-revoked-0001",
    )
    scope, _ = issue_scope(service, store, owner=owner, proposal=proposal, when=now)
    request = service.revocation_request(scope=scope, expected_revision=1, reason="owner.revoked")
    revoke_authority = authority_for(
        store,
        actor=owner,
        action=COLLABORATION_SCOPE_REVOKE_ACTION,
        resource=f"scope:{scope.scope_id}",
        request=request,
        when=now,
    )

    revoked = service.revoke(
        actor=owner,
        scope_id=scope.scope_id,
        expected_revision=1,
        reason="owner.revoked",
        authority=revoke_authority,
        when=now,
    )
    assert revoked.state == "revoked"
    assert revoked.revision == 2
    assert service.revoke(
        actor=owner,
        scope_id=scope.scope_id,
        expected_revision=1,
        reason="owner.revoked",
        authority=revoke_authority,
        when=now,
    ) == revoked
    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=recipient,
            scope_id=scope.scope_id,
            action="message.read",
            resource="conversation:revoked",
            target_harness_ids=(),
            classification=Classification.C1_INTERNAL,
            when=now,
        )

    fresh_proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, recipient),
        scope_id="scope-stale-policy-0001",
    )
    fresh, _ = issue_scope(service, store, owner=owner, proposal=fresh_proposal, when=now)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id=?",
            (owner.domain_id,),
        )
    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=owner,
            scope_id=fresh.scope_id,
            action="message.send",
            resource="conversation:stale",
            target_harness_ids=(recipient.harness_id,),
            classification=Classification.C1_INTERNAL,
            when=now,
        )


def test_scope_rejects_stale_domain_revocation_epoch(store, identity_factory) -> None:
    now = datetime.now(UTC).replace(microsecond=0)
    owner, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    service = CollaborationScopeService(store, clock=lambda: now.timestamp())
    proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, recipient),
        scope_id="scope-stale-domain-0001",
    )
    scope, _ = issue_scope(service, store, owner=owner, proposal=proposal, when=now)
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
            (owner.domain_id,),
        )

    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=owner,
            scope_id=scope.scope_id,
            action="message.send",
            resource="conversation:stale-domain",
            target_harness_ids=(recipient.harness_id,),
            classification=Classification.C1_INTERNAL,
            when=now,
        )


def test_scope_expiry_versions_state_without_reauthorizing_use(store, identity_factory) -> None:
    epoch = int(datetime.now(UTC).timestamp())
    clock = {"value": epoch}
    now = datetime.fromtimestamp(epoch, UTC)
    owner, _ = identity_factory(binding_assurance="os_bound")
    recipient, _ = identity_factory(domain=owner.domain_id, binding_assurance="os_bound")
    service = CollaborationScopeService(store, clock=lambda: clock["value"])
    proposal = proposal_for(
        store,
        owner=owner,
        members=(owner, recipient),
        scope_id="scope-expiring-0001",
        expires_at=epoch + 5,
    )
    scope, _ = issue_scope(service, store, owner=owner, proposal=proposal, when=now)
    clock["value"] = epoch + 6

    expired = service.get_for_actor(actor=owner, scope_id=scope.scope_id)
    assert expired.state == "expired"
    assert expired.revision == scope.revision + 1
    with pytest.raises(AuthorizationError, match="does not authorize"):
        service.require(
            actor=owner,
            scope_id=scope.scope_id,
            action="message.send",
            resource="conversation:expired",
            target_harness_ids=(recipient.harness_id,),
            classification=Classification.C1_INTERNAL,
        )
