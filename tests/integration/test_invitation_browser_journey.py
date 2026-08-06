from __future__ import annotations

import hashlib
import re
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from html.parser import HTMLParser
from importlib.metadata import version as package_version
from urllib.parse import urlsplit

import pytest
from starlette.testclient import TestClient

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization import AuthorizationRequest, HumanEntitlement, IssuanceAuthority
from agentnet.authorization.communication_scope_service import CollaborationScopeProposal
from agentnet.authorization.policy import LocalConformancePolicyEngine
from agentnet.console.http import SESSION_COOKIE, create_console_app
from agentnet.console.models import InvitationContinuationResult
from agentnet.console.mutations import ConsoleMutationService
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.session import ConsoleSessionService
from agentnet.errors import AuthenticationError, AuthorizationError
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
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json


_ACTIONS = ("artifact.download", "artifact.send", "message.read", "message.send")
_SCOPE_ID = "scope-atlas-browser-0001"
_UNAVAILABLE = "This invitation is unavailable. Ask the sender for a new link."


class _VisibleText(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.ignored = 0
        self.parts: list[str] = []

    def handle_starttag(self, tag: str, _attrs) -> None:
        if tag in {"script", "style", "title"}:
            self.ignored += 1

    def handle_endtag(self, tag: str) -> None:
        if tag in {"script", "style", "title"}:
            self.ignored -= 1

    def handle_data(self, data: str) -> None:
        if not self.ignored:
            self.parts.append(data)


def _visible_text(document: str) -> str:
    parser = _VisibleText()
    parser.feed(document)
    return " ".join(" ".join(parser.parts).split())


class _Authority:
    def __init__(self, store, now: list[int]) -> None:
        self.store = store
        self.now = now
        self.engine = LocalConformancePolicyEngine(store)
        self.decisions: dict[tuple[str, str, str, str], object] = {}

    @staticmethod
    def _key(actor, action: str, resource: str, context) -> tuple[str, str, str, str]:
        return (
            str(actor.harness_id),
            action,
            resource,
            canonical_digest(context or {}),
        )

    def preauthorize(self, *, actor, action: str, resource: str, context=None):
        assert actor.principal_id is not None
        self.engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
                expires_at=datetime.fromtimestamp(self.now[0] + 172_800, UTC),
            ),
            when=datetime.fromtimestamp(self.now[0], UTC),
        )
        decision = self.engine.require(
            AuthorizationRequest(
                actor=actor,
                action=action,
                resource=resource,
                policy_revision=1,
                context=context or {},
            ),
            when=datetime.fromtimestamp(self.now[0], UTC),
        )
        self.decisions[self._key(actor, action, resource, context)] = decision
        return decision

    def require(self, *, actor, action: str, resource: str, context=None):
        decision = self.decisions.get(self._key(actor, action, resource, context))
        if decision is None:
            raise AuthorizationError("browser journey authority denied")
        return decision

    def issuance(self, *, actor, action: str, resource: str, context) -> IssuanceAuthority:
        decision = self.preauthorize(
            actor=actor,
            action=action,
            resource=resource,
            context=context,
        )
        return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


class _UnusedApprovals:
    def create_request(self, **_request):
        raise AssertionError("browser invitation journey uses its passkey verifier")

    def request_status(self, **_request):
        raise AssertionError("browser invitation journey uses its passkey verifier")

    def retrieve_receipt(self, **_request):
        raise AssertionError("browser invitation journey uses its passkey verifier")


@dataclass(frozen=True, slots=True)
class _SyntheticOIDCVerifier:
    results: dict[str, OIDCVerificationResult]
    verifier_id: str = "browser-journey-oidc"

    def verify_invitation_identity(
        self,
        *,
        canonical_invitation,
        evidence,
        expected_issuer,
        when,
    ) -> OIDCVerificationResult:
        del canonical_invitation
        result = self.results.get(str(evidence.get("proof")))
        if (
            result is None
            or result.identity.issuer != expected_issuer
            or int(when.timestamp()) >= result.expires_at
        ):
            raise AuthenticationError("work account sign-in was not accepted")
        return result


@dataclass(slots=True)
class _BrowserJourney:
    store: object
    actor: object
    candidate_key: P256KeyPair
    approver_key: P256KeyPair
    oidc: _SyntheticOIDCVerifier
    internal: InternalInvitationService
    links: InvitationLinkService
    redemption: InvitationRedemptionService
    authority: _Authority
    client: TestClient
    session_id: str
    now: list[int]
    continuation: _BrowserContinuation

    def proposal(self) -> CollaborationScopeProposal:
        return CollaborationScopeProposal(
            scope_id=_SCOPE_ID,
            scope_kind="shared",
            member_harness_ids=(self.actor.harness_id,),
            allowed_actions=_ACTIONS,
            allowed_resource_prefixes=("room:atlas",),
            allowed_classifications=(Classification.C1_INTERNAL,),
            canonical_references=("project:atlas",),
            policy_revision=1,
            domain_revocation_epoch=1,
            expires_at=None,
        )

    def offer(self, invitation_id: str, *, email: str = "invitee@corp.example") -> InvitationOffer:
        return InvitationOffer(
            invitation_id=invitation_id,
            invited_verified_email=email,
            domain_id="corp.example",
            collaboration_scope_template=self.proposal(),
            permission_actions=_ACTIONS,
            expires_at=self.now[0] + 86_400,
        )

    def issue_direct(self, invitation_id: str):
        offer = self.offer(invitation_id)
        resource, context = self.links.authority_binding(
            offer,
            action=INVITATION_LINK_ISSUE_ACTION,
        )
        issued = self.links.issue(
            actor=self.actor,
            offer=offer,
            authority=self.authority.issuance(
                actor=self.actor,
                action=INVITATION_LINK_ISSUE_ACTION,
                resource=resource,
                context=context,
            ),
        )
        return offer, issued

    @staticmethod
    def token(issued) -> str:
        return urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]

    def evidence(self, offer: InvitationOffer, reservation, *, proof: str = "valid") -> InvitationRedemptionEvidence:
        request = InternalInvitationRequest(
            invitation_id=offer.invitation_id,
            domain_id=offer.domain_id,
            invited_oidc_issuer="https://id.corp.example",
            invited_oidc_subject="invitee-subject",
            invited_verified_email=offer.invited_verified_email,
            candidate_harness_id="candidate-codex-browser",
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
            authority=self.authority.issuance(
                actor=self.actor,
                action=INTERNAL_INVITATION_ISSUE_ACTION,
                resource=resource,
                context=context,
            ),
            when=datetime.fromtimestamp(self.now[0], UTC),
        )
        verification = self.oidc.results[proof]
        possession = self.candidate_key.sign(
            INTERNAL_INVITATION_POP_PURPOSE,
            self.internal.candidate_possession_fields(record.transaction, verification),
        )
        return InvitationRedemptionEvidence(
            reservation=reservation,
            canonical_internal_invitation=canonical_json(
                record.transaction.model_dump(mode="json")
            ).decode(),
            oidc_evidence={"proof": proof},
            candidate_possession_signature=possession,
            selected_scope_id=_SCOPE_ID,
            permission_actions=_ACTIONS,
        )

    def approval(
        self,
        evidence: InvitationRedemptionEvidence,
        *,
        source_fingerprint: str,
    ):
        challenge = self.redemption.prepare(
            evidence,
            source_fingerprint=source_fingerprint,
        )
        approver = TrustedApprover(
            principal_id=self.actor.principal_id,
            domain_id=self.actor.domain_id,
            signer_key_id=self.approver_key.thumbprint,
            public_key_pem=self.approver_key.public_pem,
            allowed_purposes=frozenset({INVITATION_REDEMPTION_APPROVAL_PURPOSE}),
        )
        return create_independent_approval_receipt(
            self.approver_key,
            approver=approver,
            verifier_id="browser-passkey",
            approval_purpose=INVITATION_REDEMPTION_APPROVAL_PURPOSE,
            canonical_transaction=challenge.canonical_transaction.encode(),
            issued_at=self.now[0],
            expires_at=self.now[0] + 300,
        )


class _BrowserContinuation:
    """Hermetic package boundary driven by the real public POST action."""

    def __init__(self) -> None:
        self.journey: _BrowserJourney | None = None
        self.proofs: dict[str, str] = {}
        self.results: dict[str, object] = {}

    def continue_with_work_account(
        self,
        *,
        opaque_token: str,
        source_fingerprint: str,
    ) -> InvitationContinuationResult:
        journey = self.journey
        if journey is None:
            raise AuthenticationError("invitation is unavailable")
        reservation = journey.links.reserve_redemption(
            opaque_token=opaque_token,
            source_fingerprint=source_fingerprint,
        )
        offer = journey.offer(reservation.invitation_id)
        evidence = journey.evidence(
            offer,
            reservation,
            proof=self.proofs.get(reservation.invitation_id, "valid"),
        )
        approval = journey.approval(
            evidence,
            source_fingerprint=source_fingerprint,
        )
        result = journey.redemption.redeem(
            evidence,
            approval=approval,
            source_fingerprint=source_fingerprint,
        )
        self.results[result.invitation_id] = result
        return InvitationContinuationResult(state=result.endpoint_state)


def _insert_scope(store, actor, now: int) -> None:
    proposal = CollaborationScopeProposal(
        scope_id=_SCOPE_ID,
        scope_kind="shared",
        member_harness_ids=(actor.harness_id,),
        allowed_actions=_ACTIONS,
        allowed_resource_prefixes=("room:atlas",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        canonical_references=("project:atlas",),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=None,
    )
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO collaboration_scopes(
                scope_id,schema_version,domain_id,scope_kind,owner_principal_id,owner_harness_id,
                source_communication_scope_id,state,state_reason,allowed_actions_json,
                allowed_resource_prefixes_json,allowed_classifications_json,canonical_references_json,
                policy_floor,policy_revision,domain_revocation_epoch,control_sequence,membership_sequence,
                proposal_digest,scope_digest,audit_record_hash,revision,created_at,updated_at,expires_at
            ) VALUES(?,1,?,?,?, ?,NULL,'active','issued',?,?,?,?,1,1,1,1,1,?,?,?,1,?,?,NULL)""",
            (
                _SCOPE_ID,
                actor.domain_id,
                proposal.scope_kind,
                actor.principal_id,
                actor.harness_id,
                canonical_json(list(_ACTIONS)).decode(),
                canonical_json(["room:atlas"]).decode(),
                canonical_json([Classification.C1_INTERNAL.value]).decode(),
                canonical_json(["project:atlas"]).decode(),
                canonical_digest(proposal.model_dump(mode="json")),
                hashlib.sha256(b"browser-scope").hexdigest(),
                hashlib.sha256(b"browser-scope-audit").hexdigest(),
                now,
                now,
            ),
        )
        connection.execute(
            """INSERT INTO collaboration_scope_members(
                scope_id,authority_kind,authority_id,harness_id,role,state,
                joined_sequence,removed_sequence,member_digest,joined_at,removed_at
            ) VALUES(?,'principal',?,?,'owner','active',1,NULL,?,?,NULL)""",
            (
                _SCOPE_ID,
                actor.principal_id,
                actor.harness_id,
                hashlib.sha256(b"browser-owner").hexdigest(),
                now,
            ),
        )


@pytest.fixture
def browser_journey(store, identity_factory) -> _BrowserJourney:
    now = [int(time.time())]
    actor, _sponsor_key = identity_factory(
        domain="corp.example",
        email="administrator@corp.example",
        binding_assurance="hardware_bound",
    )
    _insert_scope(store, actor, now[0])
    authority = _Authority(store, now)
    authority.preauthorize(
        actor=actor,
        action="console.session.open",
        resource="console-domain:corp.example",
    )
    sessions = ConsoleSessionService(
        store=store,
        audience="https://console.example",
        ttl_seconds=900,
        require=authority.require,
        clock=lambda: now[0],
    )
    session = sessions.issue_for_verified_actor(actor=actor)
    links = InvitationLinkService(
        store,
        public_base_url="https://console.example/join",
        clock=lambda: now[0],
    )
    proposal = CollaborationScopeProposal(
        scope_id=_SCOPE_ID,
        scope_kind="shared",
        member_harness_ids=(actor.harness_id,),
        allowed_actions=_ACTIONS,
        allowed_resource_prefixes=("room:atlas",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        canonical_references=("project:atlas",),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=None,
    )
    invitation_id = "console-" + canonical_digest(
        {
            "schema": "agentnet.console-invitation-submission.v1",
            "session_id": session.session_id,
            "actor_principal_id": actor.principal_id,
            "actor_harness_id": actor.harness_id,
            "email": "invitee@corp.example",
            "scope_id": _SCOPE_ID,
            "permissions": list(_ACTIONS),
        }
    )
    console_offer = InvitationOffer(
        invitation_id=invitation_id,
        invited_verified_email="invitee@corp.example",
        domain_id="corp.example",
        collaboration_scope_template=proposal,
        permission_actions=_ACTIONS,
        expires_at=now[0] + 86_400,
    )
    resource, context = links.authority_binding(
        console_offer,
        action=INVITATION_LINK_ISSUE_ACTION,
        expected_revision=1,
    )
    authority.preauthorize(
        actor=actor,
        action=INVITATION_LINK_ISSUE_ACTION,
        resource=resource,
        context=context,
    )
    mutations = ConsoleMutationService(
        store=store,
        approval_client=_UnusedApprovals(),
        invitation_links=links,
        require=authority.require,
        clock=lambda: now[0],
    )
    continuation = _BrowserContinuation()
    app = create_console_app(
        sessions=sessions,
        read_service=ConsoleReadService(store=store, require=authority.require),
        mutation_service=mutations,
        invitation_links=links,
        public_origin="https://console.example",
        invitation_continuation=continuation,
    )
    client = TestClient(app, base_url="https://console.example")
    client.cookies.set(SESSION_COOKIE, session.session_token, path="/")

    candidate_key = P256KeyPair.generate()
    approver_key = P256KeyPair.generate()
    oidc = _SyntheticOIDCVerifier(
        {
            "valid": OIDCVerificationResult(
                identity=VerifiedOIDCIdentity(
                    issuer="https://id.corp.example",
                    subject="invitee-subject",
                    verified_email="invitee@corp.example",
                ),
                id_token_hash=hashlib.sha256(b"valid-work-account").hexdigest(),
                expires_at=now[0] + 1_800,
            ),
            "wrong-email": OIDCVerificationResult(
                identity=VerifiedOIDCIdentity(
                    issuer="https://id.corp.example",
                    subject="other-subject",
                    verified_email="other@corp.example",
                ),
                id_token_hash=hashlib.sha256(b"wrong-work-account").hexdigest(),
                expires_at=now[0] + 1_800,
            ),
        }
    )
    internal = InternalInvitationService(store, oidc_verifier=oidc, clock=lambda: now[0])
    approver = TrustedApprover(
        principal_id=actor.principal_id,
        domain_id=actor.domain_id,
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({INVITATION_REDEMPTION_APPROVAL_PURPOSE}),
    )
    redemption = InvitationRedemptionService(
        store,
        invitation_links=links,
        internal_invitations=internal,
        approval_verifier=IndependentApprovalVerifier(
            {approver.signer_key_id: approver},
            verifier_id="browser-passkey",
        ),
        clock=lambda: now[0],
    )
    journey = _BrowserJourney(
        store=store,
        actor=actor,
        candidate_key=candidate_key,
        approver_key=approver_key,
        oidc=oidc,
        internal=internal,
        links=links,
        redemption=redemption,
        authority=authority,
        client=client,
        session_id=session.session_id,
        now=now,
        continuation=continuation,
    )
    continuation.journey = journey
    return journey


def _admin_create(browser: _BrowserJourney):
    created = browser.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data={
            "email": "invitee@corp.example",
            "scope_id": _SCOPE_ID,
            "permissions": list(_ACTIONS),
        },
        follow_redirects=False,
    )
    assert created.status_code == 303
    detail = browser.client.get(created.headers["location"])
    assert detail.status_code == 200
    match = re.search(r"https://console\.example/join/[A-Za-z0-9_-]+", detail.text)
    assert match is not None
    return detail, match.group(0)


def _assert_generic_unavailable(response) -> None:
    assert response.status_code == 410
    assert _visible_text(response.text) == _UNAVAILABLE
    assert response.headers["cache-control"] == "no-store"
    assert response.headers["referrer-policy"] == "no-referrer"


def test_admin_to_invitee_browser_journey_is_exact_and_atomic(
    browser_journey: _BrowserJourney,
) -> None:
    detail, public_url = _admin_create(browser_journey)
    token = urlsplit(public_url).path.rsplit("/", 1)[-1]
    assert (
        browser_journey.candidate_key.thumbprint
        != browser_journey.approver_key.thumbprint
    )

    assert public_url.startswith("https://")
    assert "<svg" in detail.text
    assert "api.qrserver" not in detail.text
    assert "chart.googleapis" not in detail.text

    public_page = browser_journey.client.get(public_url)
    assert public_page.status_code == 200
    assert public_page.headers["cache-control"] == "no-store"
    assert public_page.headers["referrer-policy"] == "no-referrer"
    visible = _visible_text(public_page.text)
    assert package_version("agentnet") in visible
    assert "Continue with work account" in visible
    assert "approve the exact invitation with your passkey" in visible.casefold()
    assert f'action="/join/{token}/continue"' in public_page.text
    assert '<button type="submit">Continue with work account</button>' in public_page.text
    assert "You will be asked before your agent restarts" in visible
    assert token not in visible
    for hidden in (
        _SCOPE_ID,
        "message.send",
        "artifact.download",
        "invitation_id",
        "scope_id",
        "secret",
        "/home/",
        "/var/",
        ".agentnet",
    ):
        assert hidden not in visible

    continued = browser_journey.client.post(
        f"/join/{token}/continue",
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Origin": "null",
        },
        content=b"",
    )
    assert continued.status_code == 200
    assert _visible_text(continued.text) == (
        "Skip to main content Restart your agent to enable AgentNet "
        "Access is ready. Nothing restarts automatically; restart the exact agent "
        "yourself when you are ready."
    )
    assert continued.headers["cache-control"] == "no-store"
    assert len(browser_journey.continuation.results) == 1
    result = next(iter(browser_journey.continuation.results.values()))

    assert result.endpoint_state == "restart_required"
    assert result.restart_required is True
    assert result.harness_id == "candidate-codex-browser"
    assert result.scope_id == _SCOPE_ID
    assert result.positive_entitlements == _ACTIONS
    assert result.unrelated_entitlements_issued == 0
    member = browser_journey.store.fetch_one(
        """SELECT authority_kind,authority_id,role,state
             FROM collaboration_scope_members
             WHERE scope_id=? AND harness_id=?""",
        (_SCOPE_ID, result.harness_id),
    )
    credential = browser_journey.store.fetch_one(
        "SELECT harness_id,key_id,status FROM credentials WHERE credential_id=?",
        (result.credential_id,),
    )
    endpoint = browser_journey.store.fetch_one(
        "SELECT state FROM endpoint_lifecycle WHERE harness_id=?",
        (result.harness_id,),
    )
    assert dict(member) == {
        "authority_kind": "principal",
        "authority_id": result.principal_id,
        "role": "member",
        "state": "active",
    }
    assert dict(credential) == {
        "harness_id": result.harness_id,
        "key_id": browser_journey.candidate_key.thumbprint,
        "status": "active",
    }
    assert endpoint["state"] == "restart_required"

    counts_before = (
        browser_journey.store.fetch_one(
            "SELECT COUNT(*) AS n FROM collaboration_scope_members WHERE scope_id=?",
            (_SCOPE_ID,),
        )["n"],
        browser_journey.store.fetch_one(
            "SELECT COUNT(*) AS n FROM credentials WHERE harness_id=?",
            (result.harness_id,),
        )["n"],
    )
    replay = browser_journey.client.post(
        f"/join/{token}/continue",
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Origin": "https://console.example",
        },
        content=b"",
    )
    _assert_generic_unavailable(replay)
    counts_after = (
        browser_journey.store.fetch_one(
            "SELECT COUNT(*) AS n FROM collaboration_scope_members WHERE scope_id=?",
            (_SCOPE_ID,),
        )["n"],
        browser_journey.store.fetch_one(
            "SELECT COUNT(*) AS n FROM credentials WHERE harness_id=?",
            (result.harness_id,),
        )["n"],
    )
    assert counts_after == counts_before
    _assert_generic_unavailable(browser_journey.client.get(public_url))


def test_revoked_expired_and_wrong_email_fail_without_enumeration(
    browser_journey: _BrowserJourney,
) -> None:
    revoked_offer, revoked = browser_journey.issue_direct(
        "browser-revoked-invitation-0001"
    )
    resource, context = browser_journey.links.authority_binding(
        revoked_offer,
        action=INVITATION_LINK_REVOKE_ACTION,
        expected_revision=1,
    )
    browser_journey.links.revoke(
        actor=browser_journey.actor,
        invitation_id=revoked_offer.invitation_id,
        expected_revision=1,
        authority=browser_journey.authority.issuance(
            actor=browser_journey.actor,
            action=INVITATION_LINK_REVOKE_ACTION,
            resource=resource,
            context=context,
        ),
    )
    _assert_generic_unavailable(browser_journey.client.get(str(revoked.public_url)))
    _assert_generic_unavailable(
        browser_journey.client.post(
            f"{urlsplit(str(revoked.public_url)).path}/continue",
            headers={
                "Content-Type": "application/x-www-form-urlencoded",
                "Origin": "https://console.example",
            },
            content=b"",
        )
    )

    wrong_offer, wrong = browser_journey.issue_direct(
        "browser-wrong-email-invitation-0001"
    )
    before = browser_journey.store.fetch_one(
        "SELECT COUNT(*) AS n FROM collaboration_scope_members WHERE scope_id=?",
        (_SCOPE_ID,),
    )["n"]
    browser_journey.continuation.proofs[wrong_offer.invitation_id] = "wrong-email"
    wrong_response = browser_journey.client.post(
        f"{urlsplit(str(wrong.public_url)).path}/continue",
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Origin": "https://console.example",
        },
        content=b"",
    )
    _assert_generic_unavailable(wrong_response)
    after = browser_journey.store.fetch_one(
        "SELECT COUNT(*) AS n FROM collaboration_scope_members WHERE scope_id=?",
        (_SCOPE_ID,),
    )["n"]
    assert after == before
    _assert_generic_unavailable(browser_journey.client.get(str(wrong.public_url)))

    expired_offer, expired = browser_journey.issue_direct(
        "browser-expired-invitation-0001"
    )
    browser_journey.now[0] = expired_offer.expires_at
    _assert_generic_unavailable(browser_journey.client.get(str(expired.public_url)))
    _assert_generic_unavailable(
        browser_journey.client.post(
            f"{urlsplit(str(expired.public_url)).path}/continue",
            headers={
                "Content-Type": "application/x-www-form-urlencoded",
                "Origin": "https://console.example",
            },
            content=b"",
        )
    )
