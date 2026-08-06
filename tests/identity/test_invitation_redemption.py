from __future__ import annotations

import hashlib
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from urllib.parse import urlsplit

import pytest

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization import AuthorizationRequest, HumanEntitlement, IssuanceAuthority
from agentnet.authorization.communication_scope_service import (
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.policy import LocalConformancePolicyEngine
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitation_links import (
    INVITATION_LINK_ISSUE_ACTION,
    INVITATION_LINK_REVOKE_ACTION,
    InvitationLinkService,
    InvitationOffer,
)
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_ISSUE_ACTION,
    INTERNAL_INVITATION_POP_PURPOSE,
    INVITATION_REDEMPTION_APPROVAL_PURPOSE,
    InternalInvitationRequest,
    InternalInvitationService,
    InvitationRedemptionEvidence,
    InvitationRedemptionService,
)
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.protocol.models import Classification
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.sqlite import SQLiteStore

NOW = int(datetime(2026, 8, 5, 12, 0, tzinfo=UTC).timestamp())
SOURCE = hashlib.sha256(b"onboarding-source").hexdigest()
ACTIONS = ("artifact.download", "artifact.send", "message.read", "message.send")


@dataclass(frozen=True, slots=True)
class SyntheticOIDCVerifier:
    results: dict[str, OIDCVerificationResult]
    verifier_id: str = "invitation-redemption-test-oidc"

    def verify_invitation_identity(self, *, canonical_invitation, evidence, expected_issuer, when):
        result = self.results.get(str(evidence.get("proof")))
        if result is None or result.identity.issuer != expected_issuer or int(when.timestamp()) >= result.expires_at:
            raise AuthenticationError("OIDC evidence denied")
        return result


@dataclass(slots=True)
class RedemptionStack:
    store: SQLiteStore
    actor: VerifiedActor
    sponsor_key: P256KeyPair
    candidate_key: P256KeyPair
    approver_key: P256KeyPair
    oidc_verifier: SyntheticOIDCVerifier
    internal: InternalInvitationService
    links: InvitationLinkService
    flow: InvitationRedemptionService
    now: list[int]

    def proposal(self) -> CollaborationScopeProposal:
        return CollaborationScopeProposal(
            scope_id="scope-atlas-0001",
            scope_kind="shared",
            member_harness_ids=("sponsor-harness",),
            allowed_actions=ACTIONS,
            allowed_resource_prefixes=("room:atlas",),
            allowed_classifications=(Classification.C1_INTERNAL,),
            canonical_references=("project:atlas",),
            policy_revision=1,
            domain_revocation_epoch=1,
            expires_at=None,
        )

    def offer(self, invitation_id: str = "invite-redemption-000000000001") -> InvitationOffer:
        return InvitationOffer(
            invitation_id=invitation_id,
            invited_verified_email="invitee@corp.example",
            domain_id="corp.example",
            collaboration_scope_template=self.proposal(),
            permission_actions=ACTIONS,
            expires_at=NOW + 86_400,
        )

    def authority(self, *, action: str, resource: str, context: dict) -> IssuanceAuthority:
        engine = LocalConformancePolicyEngine(self.store)
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=self.actor.domain_id,
                principal_id=self.actor.principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
                expires_at=datetime.fromtimestamp(NOW + 172_800, UTC),
            ),
            when=datetime.fromtimestamp(self.now[0], UTC),
        )
        decision = engine.require(
            AuthorizationRequest(
                actor=self.actor,
                action=action,
                resource=resource,
                policy_revision=1,
                context=context,
            ),
            when=datetime.fromtimestamp(self.now[0], UTC),
        )
        return IssuanceAuthority(actor=self.actor, policy_decision_id=decision.decision_id)

    def issue_link(self, offer: InvitationOffer | None = None):
        offer = offer or self.offer()
        resource, context = self.links.authority_binding(offer, action=INVITATION_LINK_ISSUE_ACTION)
        issued = self.links.issue(
            actor=self.actor,
            offer=offer,
            authority=self.authority(action=INVITATION_LINK_ISSUE_ACTION, resource=resource, context=context),
        )
        token = urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]
        reservation = self.links.reserve_redemption(opaque_token=token, source_fingerprint=SOURCE)
        return offer, issued, reservation

    def evidence(self, offer: InvitationOffer, reservation, *, proof: str = "valid") -> InvitationRedemptionEvidence:
        request = InternalInvitationRequest(
            invitation_id=offer.invitation_id,
            domain_id=offer.domain_id,
            invited_oidc_issuer="https://id.corp.example",
            invited_oidc_subject="invitee-subject",
            invited_verified_email=offer.invited_verified_email,
            candidate_harness_id="candidate-codex",
            candidate_harness_kind="codex",
            candidate_harness_display_name="Candidate Codex",
            candidate_binding_assurance="os_bound",
            candidate_key_id=self.candidate_key.thumbprint,
            candidate_public_key_pem=self.candidate_key.public_pem,
            requested_capabilities=(),
            expires_at=datetime.fromtimestamp(offer.expires_at, UTC),
            reason="approved collaboration invitation onboarding",
        )
        resource, context = self.internal.issuance_binding(request)
        record = self.internal.issue(
            request,
            authority=self.authority(action=INTERNAL_INVITATION_ISSUE_ACTION, resource=resource, context=context),
            when=datetime.fromtimestamp(self.now[0], UTC),
        )
        verification = self.oidc_verifier.results[proof]
        signature = self.candidate_key.sign(
            INTERNAL_INVITATION_POP_PURPOSE,
            self.internal.candidate_possession_fields(record.transaction, verification),
        )
        return InvitationRedemptionEvidence(
            reservation=reservation,
            canonical_internal_invitation=canonical_json(record.transaction.model_dump(mode="json")).decode(),
            oidc_evidence={"proof": proof},
            candidate_possession_signature=signature,
            selected_scope_id=offer.collaboration_scope_template.scope_id,
            permission_actions=offer.permission_actions,
        )

    def approval(self, evidence: InvitationRedemptionEvidence):
        challenge = self.flow.prepare(evidence, source_fingerprint=SOURCE)
        approver = TrustedApprover(
            principal_id="sponsor-principal",
            domain_id="corp.example",
            signer_key_id=self.approver_key.thumbprint,
            public_key_pem=self.approver_key.public_pem,
            allowed_purposes=frozenset({INVITATION_REDEMPTION_APPROVAL_PURPOSE}),
        )
        return create_independent_approval_receipt(
            self.approver_key,
            approver=approver,
            verifier_id="invitation-passkey",
            approval_purpose=INVITATION_REDEMPTION_APPROVAL_PURPOSE,
            canonical_transaction=challenge.canonical_transaction.encode(),
            issued_at=self.now[0],
            expires_at=self.now[0] + 300,
        )


@pytest.fixture
def redemption_stack(tmp_path) -> RedemptionStack:
    now = [NOW]
    store = SQLiteStore(tmp_path / "invitation-redemption.sqlite3", LocalEnvelopeCipher(b"r" * 32))
    sponsor_key = P256KeyPair.generate()
    candidate_key = P256KeyPair.generate()
    approver_key = P256KeyPair.generate()
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="sponsor-principal",
        harness_id="sponsor-harness",
        credential_id="sponsor-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    proposal = RedemptionStack.__dict__["proposal"](None)
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
        connection.execute("INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES('corp.example','active',1,1,?)", (NOW - 100,))
        connection.execute("INSERT INTO principals(principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at) VALUES('sponsor-principal','corp.example','https://id.corp.example','sponsor','sponsor@corp.example','active',?)", (NOW - 100,))
        connection.execute("INSERT INTO harnesses(harness_id,domain_id,principal_id,guest_id,kind,display_name,status,binding_assurance,capabilities_json,credential_epoch,created_at) VALUES('sponsor-harness','corp.example','sponsor-principal',NULL,'codex','Sponsor','active','os_bound','[]',1,?)", (NOW - 100,))
        connection.execute("INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at) VALUES('sponsor-credential','sponsor-harness',?,?,'active',1,?,?)", (sponsor_key.thumbprint, sponsor_key.public_pem, NOW - 100, NOW + 200_000))
        connection.execute(
            """INSERT INTO collaboration_scopes(scope_id,schema_version,domain_id,scope_kind,owner_principal_id,owner_harness_id,source_communication_scope_id,state,state_reason,allowed_actions_json,allowed_resource_prefixes_json,allowed_classifications_json,canonical_references_json,policy_floor,policy_revision,domain_revocation_epoch,control_sequence,membership_sequence,proposal_digest,scope_digest,audit_record_hash,revision,created_at,updated_at,expires_at) VALUES('scope-atlas-0001',1,'corp.example','shared','sponsor-principal','sponsor-harness',NULL,'active','issued',?,?,?,?,1,1,1,1,1,?,?,?,1,?,?,NULL)""",
            (
                canonical_json(list(ACTIONS)).decode(),
                canonical_json(["room:atlas"]).decode(),
                canonical_json(["C1"]).decode(),
                canonical_json(["project:atlas"]).decode(),
                proposal_digest,
                scope_digest,
                hashlib.sha256(b"audit").hexdigest(),
                NOW - 50,
                NOW - 50,
            ),
        )
        connection.execute("INSERT INTO collaboration_scope_members(scope_id,authority_kind,authority_id,harness_id,role,state,joined_sequence,removed_sequence,member_digest,joined_at,removed_at) VALUES('scope-atlas-0001','principal','sponsor-principal','sponsor-harness','owner','active',1,NULL,?, ?,NULL)", (owner_digest, NOW - 50))
    identity = VerifiedOIDCIdentity(issuer="https://id.corp.example", subject="invitee-subject", verified_email="invitee@corp.example")
    wrong_email = VerifiedOIDCIdentity(issuer="https://id.corp.example", subject="invitee-subject", verified_email="other@corp.example")
    verifier = SyntheticOIDCVerifier(
        {
            "valid": OIDCVerificationResult(identity=identity, id_token_hash=hashlib.sha256(b"valid-token").hexdigest(), expires_at=NOW + 1_800),
            "wrong-email": OIDCVerificationResult(identity=wrong_email, id_token_hash=hashlib.sha256(b"wrong-token").hexdigest(), expires_at=NOW + 1_800),
        }
    )
    internal = InternalInvitationService(store, oidc_verifier=verifier, clock=lambda: now[0])
    links = InvitationLinkService(store, public_base_url="https://agentnet.corp.example/join", clock=lambda: now[0], maximum_failures_per_source=2, lockout_seconds=60)
    approver = TrustedApprover(
        principal_id="sponsor-principal",
        domain_id="corp.example",
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({INVITATION_REDEMPTION_APPROVAL_PURPOSE}),
    )
    approval_verifier = IndependentApprovalVerifier({approver.signer_key_id: approver}, verifier_id="invitation-passkey")
    flow = InvitationRedemptionService(store, invitation_links=links, internal_invitations=internal, approval_verifier=approval_verifier, clock=lambda: now[0])
    stack = RedemptionStack(store, actor, sponsor_key, candidate_key, approver_key, verifier, internal, links, flow, now)
    try:
        yield stack
    finally:
        store.close()


def test_exact_email_redemption_enrolls_endpoint_and_only_offered_scope(redemption_stack: RedemptionStack) -> None:
    offer, _issued, reservation = redemption_stack.issue_link()
    evidence = redemption_stack.evidence(offer, reservation)
    result = redemption_stack.flow.redeem(
        evidence,
        approval=redemption_stack.approval(evidence),
        source_fingerprint=SOURCE,
    )
    assert result.scope_id == "scope-atlas-0001"
    assert result.endpoint_state == "restart_required"
    assert result.positive_entitlements == ACTIONS
    assert result.unrelated_entitlements_issued == 0
    assert redemption_stack.store.fetch_one("SELECT COUNT(*) AS count FROM entitlements WHERE principal_id=?", (result.principal_id,))["count"] == 0
    assert redemption_stack.store.fetch_one("SELECT role FROM collaboration_scope_members WHERE scope_id=? AND harness_id=?", (result.scope_id, result.harness_id))["role"] == "member"
    scope = CollaborationScopeService(
        redemption_stack.store, clock=lambda: redemption_stack.now[0]
    ).get_for_actor(actor=result.actor, scope_id=result.scope_id)
    assert scope.member_harness_ids == ("candidate-codex", "sponsor-harness")
    link = redemption_stack.store.fetch_one(
        "SELECT state,use_count FROM invitation_links WHERE invitation_id=?",
        (result.invitation_id,),
    )
    assert (link["state"], link["use_count"]) == ("consumed", 1)


def test_replay_and_concurrent_redemption_have_exactly_one_winner(redemption_stack: RedemptionStack) -> None:
    offer, _issued, reservation = redemption_stack.issue_link()
    evidence = redemption_stack.evidence(offer, reservation)
    approval = redemption_stack.approval(evidence)
    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = [
            pool.submit(
                redemption_stack.flow.redeem,
                evidence,
                approval=approval,
                source_fingerprint=SOURCE,
            )
            for _ in range(2)
        ]
    outcomes = []
    for future in futures:
        try:
            outcomes.append(future.result().harness_id)
        except (AuthenticationError, ConflictError):
            outcomes.append("denied")
    assert outcomes.count("candidate-codex") == 1
    assert outcomes.count("denied") == 1
    with pytest.raises(AuthenticationError, match="invitation is unavailable"):
        redemption_stack.flow.redeem(
            evidence, approval=approval, source_fingerprint=SOURCE
        )


@pytest.mark.parametrize("mutation", ["wrong-email", "wrong-domain", "scope-mismatch", "permissions-mismatch", "revoked", "expired", "passkey-denied"])
def test_redemption_mutations_fail_without_identity_scope_or_endpoint(mutation: str, redemption_stack: RedemptionStack) -> None:
    offer, _issued, reservation = redemption_stack.issue_link()
    evidence = redemption_stack.evidence(offer, reservation)
    if mutation == "wrong-email":
        evidence = evidence.model_copy(update={"oidc_evidence": {"proof": "wrong-email"}})
    elif mutation == "wrong-domain":
        canonical = evidence.canonical_internal_invitation.replace('"domain_id":"corp.example"', '"domain_id":"other.example"')
        evidence = evidence.model_copy(update={"canonical_internal_invitation": canonical})
    elif mutation == "scope-mismatch":
        evidence = evidence.model_copy(update={"selected_scope_id": "scope-other"})
    elif mutation == "permissions-mismatch":
        evidence = evidence.model_copy(update={"permission_actions": ("message.read",)})
    elif mutation == "revoked":
        resource, context = redemption_stack.links.authority_binding(offer, action=INVITATION_LINK_REVOKE_ACTION, expected_revision=reservation.revision)
        redemption_stack.links.revoke(actor=redemption_stack.actor, invitation_id=offer.invitation_id, expected_revision=reservation.revision, authority=redemption_stack.authority(action=INVITATION_LINK_REVOKE_ACTION, resource=resource, context=context))
    elif mutation == "expired":
        redemption_stack.now[0] = offer.expires_at
    before = tuple(redemption_stack.store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"] for table in ("principals", "collaboration_scope_members", "endpoint_lifecycle"))
    with pytest.raises((AuthenticationError, AuthorizationError, ConflictError)):
        approval = {} if mutation == "passkey-denied" else redemption_stack.approval(evidence)
        redemption_stack.flow.redeem(
            evidence, approval=approval, source_fingerprint=SOURCE
        )
    after = tuple(redemption_stack.store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"] for table in ("principals", "collaboration_scope_members", "endpoint_lifecycle"))
    assert after == before


def test_repeated_invalid_proof_triggers_durable_abuse_lockout(redemption_stack: RedemptionStack) -> None:
    offer, _issued, reservation = redemption_stack.issue_link()
    evidence = redemption_stack.evidence(offer, reservation)
    invalid = evidence.model_copy(update={"candidate_possession_signature": "invalid-signature"})
    for _ in range(2):
        with pytest.raises(AuthenticationError, match="invitation is unavailable"):
            redemption_stack.flow.redeem(
                invalid, approval={}, source_fingerprint=SOURCE
            )
    with pytest.raises(AuthenticationError, match="invitation is unavailable"):
        redemption_stack.flow.prepare(evidence, source_fingerprint=SOURCE)
    row = redemption_stack.store.fetch_one("SELECT failure_count,locked_until FROM invitation_link_failures WHERE invitation_id=? AND source_fingerprint=?", (offer.invitation_id, SOURCE))
    assert row["failure_count"] == 2
    assert row["locked_until"] == NOW + 60
