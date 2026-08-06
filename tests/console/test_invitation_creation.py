from __future__ import annotations

import hashlib
import re
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from html.parser import HTMLParser

from starlette.testclient import TestClient

from agentnet.authorization import AuthorizationRequest, HumanEntitlement
from agentnet.authorization.communication_scope_service import CollaborationScopeProposal
from agentnet.authorization.policy import LocalConformancePolicyEngine
from agentnet.console.http import SESSION_COOKIE, create_console_app
from agentnet.console.mutations import ConsoleMutationService
from agentnet.console.read_service import ConsoleReadService
from agentnet.console.session import ConsoleSessionService
from agentnet.errors import AuthorizationError
from agentnet.identity.invitation_links import (
    INVITATION_LINK_ISSUE_ACTION,
    INVITATION_LINK_REVOKE_ACTION,
    InvitationLinkService,
    InvitationOffer,
)
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_digest, canonical_json


_ACTIONS = (
    "artifact.download",
    "artifact.send",
    "message.read",
    "message.send",
)
_SCOPE_ID = "scope-atlas-internal-0001"


class _VisibleText(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.parts: list[str] = []

    def handle_data(self, data: str) -> None:
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
        self.requests: list[dict[str, object]] = []

    @staticmethod
    def _key(actor, action: str, resource: str, context) -> tuple[str, str, str, str]:
        return (
            str(actor.harness_id),
            action,
            resource,
            canonical_digest(context or {}),
        )

    def preauthorize(self, *, actor, action: str, resource: str, context=None) -> None:
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

    def require(self, *, actor, action: str, resource: str, context=None):
        self.requests.append(
            {"actor": actor, "action": action, "resource": resource, "context": context}
        )
        decision = self.decisions.get(self._key(actor, action, resource, context))
        if decision is None:
            raise AuthorizationError("test authority denied")
        return decision


class _UnusedApprovals:
    def create_request(self, **_request):
        raise AssertionError("invitation creation must not create an unrelated approval")

    def request_status(self, **_request):
        raise AssertionError("invitation creation must not query an unrelated approval")

    def retrieve_receipt(self, **_request):
        raise AssertionError("invitation creation must not retrieve an unrelated approval")


class _RecordingInvitationLinks:
    def __init__(self, service: InvitationLinkService) -> None:
        self.service = service
        self.offers = []
        self.clock = service.clock

    def authority_binding(self, *args, **kwargs):
        return self.service.authority_binding(*args, **kwargs)

    def issue(self, *args, **kwargs):
        self.offers.append(kwargs["offer"])
        return self.service.issue(*args, **kwargs)

    def revoke(self, *args, **kwargs):
        return self.service.revoke(*args, **kwargs)


@dataclass
class _ConsoleStack:
    client: TestClient
    sessions: ConsoleSessionService
    session_token: str
    actor: object
    links: _RecordingInvitationLinks
    authority: _Authority
    now: list[int]


def _insert_scope(store, actor, *, role: str = "owner") -> None:
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
    now = int(time.time())
    proposal_digest = canonical_digest(proposal.model_dump(mode="json"))
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
                proposal.scope_id,
                actor.domain_id,
                proposal.scope_kind,
                actor.principal_id,
                actor.harness_id,
                canonical_json(list(proposal.allowed_actions)).decode(),
                canonical_json(list(proposal.allowed_resource_prefixes)).decode(),
                canonical_json([item.value for item in proposal.allowed_classifications]).decode(),
                canonical_json(list(proposal.canonical_references)).decode(),
                proposal_digest,
                hashlib.sha256(b"console-scope").hexdigest(),
                hashlib.sha256(b"console-scope-audit").hexdigest(),
                now,
                now,
            ),
        )
        connection.execute(
            """INSERT INTO collaboration_scope_members(
                scope_id,authority_kind,authority_id,harness_id,role,state,
                joined_sequence,removed_sequence,member_digest,joined_at,removed_at
            ) VALUES(?,'principal',?,?,?,'active',1,NULL,?,?,NULL)""",
            (
                proposal.scope_id,
                actor.principal_id,
                actor.harness_id,
                role,
                hashlib.sha256(f"{role}-member".encode()).hexdigest(),
                now,
            ),
        )


def _expected_offer(store, actor, *, session_id: str, now: int) -> InvitationOffer:
    members = store.fetch_all(
        """SELECT harness_id FROM collaboration_scope_members
            WHERE scope_id=? AND state='active' ORDER BY harness_id""",
        (_SCOPE_ID,),
    )
    proposal = CollaborationScopeProposal(
        scope_id=_SCOPE_ID,
        scope_kind="shared",
        member_harness_ids=tuple(str(row["harness_id"]) for row in members),
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
            "session_id": session_id,
            "actor_principal_id": actor.principal_id,
            "actor_harness_id": actor.harness_id,
            "email": "invitee@corp.example",
            "scope_id": _SCOPE_ID,
            "permissions": list(_ACTIONS),
        }
    )
    return InvitationOffer(
        invitation_id=invitation_id,
        invited_verified_email="invitee@corp.example",
        domain_id=actor.domain_id,
        collaboration_scope_template=proposal,
        permission_actions=_ACTIONS,
        expires_at=now + 86_400,
    )


def _stack(store, actor, *, now: list[int]) -> _ConsoleStack:
    authority = _Authority(store, now)
    authority.preauthorize(
        actor=actor,
        action="console.session.open",
        resource=f"console-domain:{actor.domain_id}",
    )
    sessions = ConsoleSessionService(
        store=store,
        audience="https://console.example",
        ttl_seconds=60,
        require=authority.require,
        clock=lambda: now[0],
    )
    issued_session = sessions.issue_for_verified_actor(actor=actor)
    service = InvitationLinkService(
        store,
        public_base_url="https://onboarding.example/join",
        clock=lambda: now[0],
    )
    links = _RecordingInvitationLinks(service)
    offer = _expected_offer(
        store,
        actor,
        session_id=issued_session.session_id,
        now=now[0],
    )
    for action in (INVITATION_LINK_ISSUE_ACTION, INVITATION_LINK_REVOKE_ACTION):
        resource, context = service.authority_binding(
            offer,
            action=action,
            expected_revision=1,
        )
        authority.preauthorize(
            actor=actor,
            action=action,
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
    app = create_console_app(
        sessions=sessions,
        read_service=ConsoleReadService(store=store, require=authority.require),
        mutation_service=mutations,
        invitation_links=links,
        public_origin="https://console.example",
    )
    client = TestClient(app, base_url="https://console.example")
    client.cookies.set(SESSION_COOKIE, issued_session.session_token, path="/")
    return _ConsoleStack(
        client=client,
        sessions=sessions,
        session_token=issued_session.session_token,
        actor=actor,
        links=links,
        authority=authority,
        now=now,
    )


def _valid_form() -> dict[str, str | list[str]]:
    return {
        "email": "invitee@corp.example",
        "scope_id": _SCOPE_ID,
        "permissions": [
            "message.send",
            "message.read",
            "artifact.send",
            "artifact.download",
        ],
    }


def _create(stack: _ConsoleStack):
    return stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=_valid_form(),
        follow_redirects=False,
    )


def test_administrator_creates_one_exact_email_bound_invitation(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example",
        email="administrator@corp.example",
        binding_assurance="hardware_bound",
    )
    _insert_scope(store, actor)
    now = [int(time.time())]
    stack = _stack(store, actor, now=now)

    form_page = stack.client.get("/invitations/new")
    assert form_page.status_code == 200
    assert "Work email" in form_page.text
    assert "Space" in form_page.text
    assert "Can send messages" in form_page.text
    assert "Can read messages" in form_page.text
    assert "Can send files" in form_page.text
    assert _SCOPE_ID not in _visible_text(form_page.text)
    assert "Can download files" in form_page.text

    created = _create(stack)
    assert created.status_code == 303
    assert len(stack.links.offers) == 1
    offer = stack.links.offers[0]
    assert offer.domain_id == "corp.example"
    assert offer.invited_verified_email == "invitee@corp.example"
    assert offer.collaboration_scope_template.scope_id == _SCOPE_ID
    assert offer.permission_actions == _ACTIONS
    assert offer.expires_at == now[0] + 86_400
    assert offer.max_uses == 1
    assert store.fetch_one("SELECT COUNT(*) AS n FROM invitation_links")["n"] == 1

    detail = stack.client.get(created.headers["location"])
    assert detail.status_code == 200
    assert "Expires in 24 hours" in detail.text
    assert "<svg" in detail.text
    assert "Copy invitation link" in detail.text
    assert "Download QR code" in detail.text
    assert "Copy onboarding instructions" in detail.text
    assert "https://onboarding.example/join/" in detail.text
    assert "http://onboarding.example" not in detail.text
    assert "api.qrserver" not in detail.text
    assert "chart.googleapis" not in detail.text
    visible = _visible_text(detail.text)
    assert _SCOPE_ID not in visible
    assert offer.invitation_id not in visible
    assert "policy_revision" not in visible
    assert "domain_revocation_epoch" not in visible
    assert "project:atlas" not in visible
    for name, expected in {
        "cache-control": "no-store",
        "x-frame-options": "DENY",
        "referrer-policy": "no-referrer",
        "x-content-type-options": "nosniff",
    }.items():
        assert detail.headers[name] == expected
    assert "frame-ancestors 'none'" in detail.headers["content-security-policy"]


def test_non_administrator_and_wrong_scope_fail_closed(store, identity_factory) -> None:
    owner, _ = identity_factory(
        domain="corp.example",
        email="owner@corp.example",
        binding_assurance="hardware_bound",
    )
    _insert_scope(store, owner)
    member, _ = identity_factory(
        domain="corp.example",
        email="member@corp.example",
        binding_assurance="hardware_bound",
    )
    now = int(time.time())
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO collaboration_scope_members(
                scope_id,authority_kind,authority_id,harness_id,role,state,
                joined_sequence,removed_sequence,member_digest,joined_at,removed_at
            ) VALUES(?,'principal',?,?,'member','active',2,NULL,?,?,NULL)""",
            (
                _SCOPE_ID,
                member.principal_id,
                member.harness_id,
                hashlib.sha256(b"ordinary-member").hexdigest(),
                now,
            ),
        )
    member_stack = _stack(store, member, now=[now])

    denied = _create(member_stack)
    assert "The request could not be completed." in denied.text
    assert _SCOPE_ID not in _visible_text(denied.text)
    assert denied.status_code == 403
    assert member_stack.client.get("/invitations/new").status_code == 403

    owner_stack = _stack(store, owner, now=[now])
    wrong_scope_form = {**_valid_form(), "scope_id": "missing-scope"}
    wrong_scope = owner_stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=wrong_scope_form,
        follow_redirects=False,
    )
    assert wrong_scope.status_code == 403
    assert "The request could not be completed." in wrong_scope.text
    assert "missing-scope" not in wrong_scope.text
    assert store.fetch_one("SELECT COUNT(*) AS n FROM invitation_links")["n"] == 0
    assert member_stack.links.offers == []
    assert owner_stack.links.offers == []


def test_creation_requires_current_session_same_origin_and_valid_permissions(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example",
        email="administrator@corp.example",
        binding_assurance="hardware_bound",
    )
    _insert_scope(store, actor)
    now = [int(time.time())]
    stack = _stack(store, actor, now=now)

    missing_origin = stack.client.post("/invitations", data=_valid_form())
    assert missing_origin.status_code == 403
    wrong_origin = stack.client.post(
        "/invitations",
        headers={"Origin": "https://attacker.example"},
        data=_valid_form(),
    )
    assert wrong_origin.status_code == 403

    stack.client.cookies.clear()
    missing_session = stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=_valid_form(),
    )
    assert missing_session.status_code == 401
    stack.client.cookies.set(SESSION_COOKIE, stack.session_token, path="/")

    unexpected = {**_valid_form(), "administrator": "true"}
    unexpected_field = stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=unexpected,
    )
    assert unexpected_field.status_code == 400

    malformed = {**_valid_form(), "permissions": ["room.create"]}
    malformed_permissions = stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=malformed,
    )
    assert malformed_permissions.status_code == 400
    assert "Check the work email, space, and allowed actions" in malformed_permissions.text

    incomplete = {**_valid_form(), "permissions": ["message.send"]}
    incomplete_permissions = stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=incomplete,
    )
    assert incomplete_permissions.status_code == 400
    assert "Choose exactly the message and file actions" in incomplete_permissions.text

    wrong_domain = {**_valid_form(), "email": "invitee@other.example"}
    wrong_email = stack.client.post(
        "/invitations",
        headers={"Origin": "https://console.example"},
        data=wrong_domain,
    )
    assert wrong_email.status_code == 400

    now[0] += 61
    stale_session = _create(stack)
    assert stale_session.status_code == 401
    assert store.fetch_one("SELECT COUNT(*) AS n FROM invitation_links")["n"] == 0


def test_scope_change_after_form_load_is_rechecked(store, identity_factory) -> None:
    actor, _ = identity_factory(
        domain="corp.example",
        email="administrator@corp.example",
        binding_assurance="hardware_bound",
    )
    _insert_scope(store, actor)
    now = [int(time.time())]
    stack = _stack(store, actor, now=now)

    assert stack.client.get("/invitations/new").status_code == 200
    with store.transaction() as connection:
        connection.execute(
            "UPDATE collaboration_scopes SET policy_revision=2,revision=revision+1,updated_at=? WHERE scope_id=?",
            (now[0], _SCOPE_ID),
        )
    denied = _create(stack)
    assert denied.status_code == 403
    assert stack.links.offers == []
    assert store.fetch_one("SELECT COUNT(*) AS n FROM invitation_links")["n"] == 0


def test_creation_replay_and_revocation_replay_create_no_second_invitation(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example",
        email="administrator@corp.example",
        binding_assurance="hardware_bound",
    )
    _insert_scope(store, actor)
    now = [int(time.time())]
    stack = _stack(store, actor, now=now)

    created = _create(stack)
    assert created.status_code == 303
    replayed_creation = _create(stack)
    assert replayed_creation.status_code in {400, 409}
    assert store.fetch_one("SELECT COUNT(*) AS n FROM invitation_links")["n"] == 1

    detail = stack.client.get(created.headers["location"])
    invitation_id = created.headers["location"].rsplit("/", 1)[-1]
    match = re.search(
        rf'action="/invitations/{re.escape(invitation_id)}/revoke".*?'
        r'name="mutation_token" value="([^"]+)"',
        detail.text,
        re.DOTALL,
    )
    assert match is not None
    token = match.group(1)
    missing_revoke_origin = stack.client.post(
        f"/invitations/{invitation_id}/revoke",
        data={"mutation_token": token},
        follow_redirects=False,
    )
    assert missing_revoke_origin.status_code == 403
    revoked = stack.client.post(
        f"/invitations/{invitation_id}/revoke",
        headers={"Origin": "https://console.example"},
        data={"mutation_token": token},
        follow_redirects=False,
    )
    assert revoked.status_code == 303
    row = store.fetch_one(
        "SELECT state,use_count FROM invitation_links WHERE invitation_id=?",
        (invitation_id,),
    )
    assert row["state"] == "revoked"
    assert row["use_count"] == 0

    revoked_detail = stack.client.get(revoked.headers["location"])
    assert "Access removed" in revoked_detail.text
    assert "Copy invitation link" not in revoked_detail.text
    assert "<svg" not in revoked_detail.text
    replayed_revoke = stack.client.post(
        f"/invitations/{invitation_id}/revoke",
        headers={"Origin": "https://console.example"},
        data={"mutation_token": token},
        follow_redirects=False,
    )
    assert replayed_revoke.status_code == 403
    assert store.fetch_one("SELECT COUNT(*) AS n FROM invitation_links")["n"] == 1
