from __future__ import annotations

import hashlib
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from urllib.parse import urlsplit

import pytest

from agentnet.authorization import AuthorizationRequest, HumanEntitlement, IssuanceAuthority
from agentnet.authorization.communication_scope_service import (
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.policy import LocalConformancePolicyEngine
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.invitation_links import (
    INVITATION_LINK_ISSUE_ACTION,
    INVITATION_LINK_REVOKE_ACTION,
    InvitationLinkService,
    InvitationOffer,
    InvitationUnavailable,
)
from agentnet.protocol.models import Classification
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.sqlite import SQLiteStore

NOW = int(datetime(2026, 8, 5, 12, 0, tzinfo=UTC).timestamp())
SOURCE = hashlib.sha256(b"invitation-link-source").hexdigest()


def _proposal() -> CollaborationScopeProposal:
    return CollaborationScopeProposal(
        scope_id="scope-atlas-0001",
        scope_kind="shared",
        member_harness_ids=("sponsor-harness",),
        allowed_actions=("artifact.download", "artifact.send", "message.read", "message.send"),
        allowed_resource_prefixes=("room:atlas",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        canonical_references=("project:atlas",),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=None,
    )


def _offer(*, invitation_id: str = "invite-link-0000000000000001", expires_at: int = NOW + 86_400) -> InvitationOffer:
    return InvitationOffer(
        invitation_id=invitation_id,
        invited_verified_email="invitee@corp.example",
        domain_id="corp.example",
        collaboration_scope_template=_proposal(),
        permission_actions=("artifact.download", "artifact.send", "message.read", "message.send"),
        expires_at=expires_at,
    )


@pytest.fixture
def link_stack(tmp_path):
    now = [NOW]
    store = SQLiteStore(tmp_path / "invitation-links.sqlite3", LocalEnvelopeCipher(b"i" * 32))
    key = P256KeyPair.generate()
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="sponsor-principal",
        harness_id="sponsor-harness",
        credential_id="sponsor-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    proposal = _proposal()
    proposal_digest = CollaborationScopeService._proposal_digest(
        actor=actor, proposal=proposal
    )
    owner_digest = CollaborationScopeService._member_digest(
        scope_id=proposal.scope_id,
        authority_kind="principal",
        authority_id=actor.principal_id,
        harness_id=actor.harness_id,
        role="owner",
        joined_at=NOW - 50,
    )
    scope_digest = CollaborationScopeService._scope_digest(
        scope_id=proposal.scope_id,
        scope_kind=proposal.scope_kind,
        domain_id=actor.domain_id,
        owner_principal_id=actor.principal_id,
        owner_harness_id=actor.harness_id,
        members=[{
            "authority_kind": "principal",
            "authority_id": actor.principal_id,
            "harness_id": actor.harness_id,
            "role": "owner",
            "state": "active",
            "joined_sequence": 1,
            "joined_at": NOW - 50,
        }],
        allowed_actions=proposal.allowed_actions,
        allowed_resource_prefixes=proposal.allowed_resource_prefixes,
        allowed_classifications=proposal.allowed_classifications,
        canonical_references=proposal.canonical_references,
        policy_revision=1,
        domain_revocation_epoch=1,
        control_sequence=1,
        membership_sequence=1,
        proposal_digest=proposal_digest,
        revision=1,
        state="active",
        state_reason="issued",
        created_at=NOW - 50,
        updated_at=NOW - 50,
        expires_at=None,
        revoked_at=None,
    )
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("corp.example", "active", 1, 1, NOW - 100),
        )
        connection.execute(
            "INSERT INTO principals(principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at) VALUES(?,?,?,?,?,'active',?)",
            ("sponsor-principal", "corp.example", "https://id.corp.example", "sponsor", "sponsor@corp.example", NOW - 100),
        )
        connection.execute(
            "INSERT INTO harnesses(harness_id,domain_id,principal_id,guest_id,kind,display_name,status,binding_assurance,capabilities_json,credential_epoch,created_at) VALUES(?,?,?,NULL,'codex',?,'active','os_bound','[]',1,?)",
            ("sponsor-harness", "corp.example", "sponsor-principal", "Sponsor", NOW - 100),
        )
        connection.execute(
            "INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at) VALUES(?,?,?,?,'active',1,?,?)",
            ("sponsor-credential", "sponsor-harness", key.thumbprint, key.public_pem, NOW - 100, NOW + 200_000),
        )
        connection.execute(
            """INSERT INTO collaboration_scopes(
                scope_id,schema_version,domain_id,scope_kind,owner_principal_id,owner_harness_id,
                source_communication_scope_id,state,state_reason,allowed_actions_json,
                allowed_resource_prefixes_json,allowed_classifications_json,canonical_references_json,
                policy_floor,policy_revision,domain_revocation_epoch,control_sequence,membership_sequence,
                proposal_digest,scope_digest,audit_record_hash,revision,created_at,updated_at,expires_at
            ) VALUES(?,1,?,?,?, ?,NULL,'active','issued',?,?,?,?,1,1,1,1,1,?,?,?,1,?,?,NULL)""",
            (
                proposal.scope_id,
                "corp.example",
                proposal.scope_kind,
                actor.principal_id,
                actor.harness_id,
                canonical_json(list(proposal.allowed_actions)).decode(),
                canonical_json(list(proposal.allowed_resource_prefixes)).decode(),
                canonical_json([item.value for item in proposal.allowed_classifications]).decode(),
                canonical_json(list(proposal.canonical_references)).decode(),
                proposal_digest,
                scope_digest,
                hashlib.sha256(b"scope-audit").hexdigest(),
                NOW - 50,
                NOW - 50,
            ),
        )
        connection.execute(
            "INSERT INTO collaboration_scope_members(scope_id,authority_kind,authority_id,harness_id,role,state,joined_sequence,removed_sequence,member_digest,joined_at,removed_at) VALUES(?,'principal',?,?,'owner','active',1,NULL,?, ?,NULL)",
            (proposal.scope_id, actor.principal_id, actor.harness_id, owner_digest, NOW - 50),
        )
    service = InvitationLinkService(
        store,
        public_base_url="https://agentnet.corp.example/join",
        clock=lambda: now[0],
        maximum_failures_per_source=2,
        lockout_seconds=60,
    )

    def authority(offer: InvitationOffer, *, action: str = INVITATION_LINK_ISSUE_ACTION, revision: int = 1):
        resource, context = service.authority_binding(offer, action=action, expected_revision=revision)
        engine = LocalConformancePolicyEngine(store)
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
                expires_at=datetime.fromtimestamp(NOW + 172_800, UTC),
            ),
            when=datetime.fromtimestamp(now[0], UTC),
        )
        decision = engine.require(
            AuthorizationRequest(
                actor=actor,
                action=action,
                resource=resource,
                policy_revision=1,
                context=context,
            ),
            when=datetime.fromtimestamp(now[0], UTC),
        )
        return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)

    try:
        yield store, service, actor, now, authority
    finally:
        store.close()


def test_issue_hashes_token_sets_exact_expiry_and_renders_qr(link_stack) -> None:
    store, service, actor, _now, authority = link_stack
    offer = _offer()
    issued = service.issue(actor=actor, offer=offer, authority=authority(offer))
    token = urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]
    row = store.fetch_one("SELECT * FROM invitation_links WHERE invitation_id=?", (issued.invitation_id,))
    assert row["token_hash"] != token
    assert row["token_hash"] == hashlib.sha256(token.encode("ascii")).hexdigest()
    assert row["expires_at"] == NOW + 86_400
    assert row["max_uses"] == 1
    assert offer.invited_verified_email not in row["encrypted_offer"]
    assert offer.invited_verified_email not in row["invited_email_encrypted"]
    assert row["invited_email_sha256"] == hashlib.sha256(
        offer.invited_verified_email.encode()
    ).hexdigest()
    assert "<svg" in issued.qr_svg
    assert token not in issued.qr_svg
    audit = "\n".join(
        item["record_json"] for item in store.fetch_all("SELECT record_json FROM audit_log")
    )
    assert token not in audit
    assert offer.invited_verified_email not in audit


def test_public_inspection_is_non_enumerating_for_every_unavailable_state(link_stack) -> None:
    store, service, actor, now, authority = link_stack
    tokens = ["missing-token"]
    for suffix, terminal in (("revoked", "revoked"), ("expired", "expired"), ("consumed", "consumed")):
        offer = _offer(invitation_id=f"invite-link-{suffix}-000000000001")
        issued = service.issue(actor=actor, offer=offer, authority=authority(offer))
        token = urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]
        reservation = service.reserve_redemption(opaque_token=token, source_fingerprint=SOURCE)
        if terminal == "revoked":
            service.revoke(
                actor=actor,
                invitation_id=offer.invitation_id,
                expected_revision=reservation.revision,
                authority=authority(offer, action=INVITATION_LINK_REVOKE_ACTION, revision=reservation.revision),
            )
        elif terminal == "expired":
            now[0] = offer.expires_at
            with pytest.raises(InvitationUnavailable):
                service.inspect_public(opaque_token=token)
            assert store.fetch_one(
                "SELECT state FROM invitation_links WHERE invitation_id=?",
                (offer.invitation_id,),
            )["state"] == "expired"
        else:
            with store.transaction() as connection:
                connection.execute(
                    """UPDATE invitation_links SET state='consumed',
                       state_reason='test_consumed',use_count=1,revision=3,
                       updated_at=?,consumed_at=? WHERE invitation_id=?""",
                    (NOW, NOW, offer.invitation_id),
                )
        tokens.append(token)
        now[0] = NOW
    for token in tokens:
        with pytest.raises(InvitationUnavailable, match="invitation is unavailable"):
            service.inspect_public(opaque_token=token)


def test_reservation_is_single_winner_under_concurrency(link_stack) -> None:
    _store, service, actor, _now, authority = link_stack
    offer = _offer()
    issued = service.issue(actor=actor, offer=offer, authority=authority(offer))
    token = urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]
    sources = tuple(hashlib.sha256(f"source-{index}".encode()).hexdigest() for index in range(8))
    with ThreadPoolExecutor(max_workers=8) as pool:
        outcomes = list(pool.map(lambda source: _reserve_result(service, token, source), sources))
    assert outcomes.count("reserved") == 1
    assert outcomes.count("unavailable") == 7


def _reserve_result(service: InvitationLinkService, token: str, source: str) -> str:
    try:
        service.reserve_redemption(opaque_token=token, source_fingerprint=source)
    except InvitationUnavailable:
        return "unavailable"
    return "reserved"


def test_replay_expiry_revocation_wrong_scope_and_abuse_fail_closed(link_stack) -> None:
    store, service, actor, now, authority = link_stack
    offer = _offer()
    issued = service.issue(actor=actor, offer=offer, authority=authority(offer))
    token = urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]
    reservation = service.reserve_redemption(opaque_token=token, source_fingerprint=SOURCE)
    assert service.reserve_redemption(opaque_token=token, source_fingerprint=SOURCE) == reservation
    with pytest.raises(ConflictError):
        service.validate_reserved(
            reservation=reservation.model_copy(update={"destination_scope_id": "scope-other"}),
            source_fingerprint=SOURCE,
        )
    service.note_redemption_failure(reservation=reservation, source_fingerprint=SOURCE)
    service.note_redemption_failure(reservation=reservation, source_fingerprint=SOURCE)
    with pytest.raises(InvitationUnavailable):
        service.validate_reserved(reservation=reservation, source_fingerprint=SOURCE)
    failure = store.fetch_one(
        "SELECT * FROM invitation_link_failures WHERE invitation_id=? AND source_fingerprint=?",
        (offer.invitation_id, SOURCE),
    )
    assert failure["failure_count"] == 2
    assert failure["locked_until"] == NOW + 60
    now[0] += 61
    service.revoke(
        actor=actor,
        invitation_id=offer.invitation_id,
        expected_revision=reservation.revision,
        authority=authority(offer, action=INVITATION_LINK_REVOKE_ACTION, revision=reservation.revision),
    )
    with pytest.raises(InvitationUnavailable):
        service.validate_reserved(reservation=reservation, source_fingerprint=SOURCE)


def test_offer_denies_wrong_domain_unsorted_permissions_and_non_admin(link_stack) -> None:
    _store, service, actor, _now, authority = link_stack
    offer = _offer()
    with pytest.raises(ValueError):
        InvitationOffer(**{**offer.model_dump(), "invited_verified_email": "invitee@other.example"})
    with pytest.raises(ValueError):
        InvitationOffer(**{**offer.model_dump(), "permission_actions": ("message.send", "message.read")})
    with pytest.raises(ValueError):
        InvitationOffer(
            **{
                **offer.model_dump(),
                "permission_actions": ("message.read", "message.send"),
            }
        )
    non_admin = actor.model_copy(update={"principal_id": "other-principal"})
    with pytest.raises(AuthorizationError):
        service.issue(actor=non_admin, offer=offer, authority=authority(offer))
