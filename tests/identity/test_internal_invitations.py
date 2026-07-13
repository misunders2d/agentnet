from __future__ import annotations

import hashlib
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, replace
from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization import (
    AUTHORITY_COMMAND_PURPOSE,
    AuthorizationRequest,
    HumanEntitlement,
    IssuanceAuthority,
    SignedAuthorityCommand,
)
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import LocalConformancePolicyEngine, PolicyEngine
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_ISSUE_ACTION,
    INTERNAL_INVITATION_POP_PURPOSE,
    INTERNAL_INVITATION_REVOKE_ACTION,
    InternalInvitationRequest,
    InternalInvitationService,
)
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.storage.sqlite import SQLiteStore


NOW = datetime(2026, 7, 13, 12, 0, tzinfo=UTC)
SOURCE = hashlib.sha256(b"trusted-transport-source-1").hexdigest()


@dataclass(frozen=True, slots=True)
class SyntheticIndependentOIDCVerifier:
    """Test seam: identity is configured here, never accepted from evidence."""

    result: OIDCVerificationResult
    verifier_id: str = "synthetic-independent-oidc.example"

    def verify_invitation_identity(
        self,
        *,
        canonical_invitation: bytes,
        evidence,
        expected_issuer: str,
        when: datetime,
    ) -> OIDCVerificationResult:
        if evidence != {"authorization_code_proof": "independently-verified"}:
            raise AuthenticationError("OIDC authorization-code proof is invalid")
        if not canonical_invitation or expected_issuer != self.result.identity.issuer:
            raise AuthenticationError("OIDC invitation state binding mismatch")
        if int(when.timestamp()) >= self.result.expires_at:
            raise AuthenticationError("OIDC proof expired")
        return self.result


class DelegatingBackendContract:
    """StoreBackend implementation that is deliberately not a SQLiteStore."""

    backend_name = "backend-contract"

    def __init__(self, delegate: SQLiteStore) -> None:
        self.delegate = delegate
        self.cipher = delegate.cipher

    def transaction(self, *, immediate: bool = True):
        return self.delegate.transaction(immediate=immediate)

    def fetch_one(self, query, parameters=()):
        return self.delegate.fetch_one(query, parameters)

    def fetch_all(self, query, parameters=()):
        return self.delegate.fetch_all(query, parameters)

    def append_audit(self, connection, record):
        return self.delegate.append_audit(connection, record)

    def verify_audit_chain(self):
        return self.delegate.verify_audit_chain()

    def encrypted_payload(self, payload, event_id):
        return self.delegate.encrypted_payload(payload, event_id)

    def decrypted_payload(self, token, event_id):
        return self.delegate.decrypted_payload(token, event_id)

    def readiness(self):
        return self.delegate.readiness()

    def close(self) -> None:
        # The fixture owns the underlying connection.
        return None


@dataclass(slots=True)
class InvitationStack:
    store: SQLiteStore
    sponsor_key: P256KeyPair
    outsider_key: P256KeyPair
    sponsor: VerifiedActor
    outsider: VerifiedActor
    candidate_key: P256KeyPair
    verifier: SyntheticIndependentOIDCVerifier
    service: InternalInvitationService
    now_epoch: int

    def authority(self, request: InternalInvitationRequest, *, actor: VerifiedActor | None = None) -> IssuanceAuthority:
        actor = actor or self.sponsor
        resource, context = self.service.issuance_binding(request)
        engine = LocalConformancePolicyEngine(self.store)
        engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=INTERNAL_INVITATION_ISSUE_ACTION,
                resource_pattern=resource,
                revision=1,
                expires_at=NOW + timedelta(hours=2),
            ),
            when=NOW,
        )
        decision = engine.require(
            AuthorizationRequest(
                actor=actor,
                action=INTERNAL_INVITATION_ISSUE_ACTION,
                resource=resource,
                policy_revision=1,
                context=context,
            ),
            when=NOW,
        )
        return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)

    def request(
        self,
        *,
        invitation_id: str = "internal-invitation-0000000000000001",
        key: P256KeyPair | None = None,
        predecessor_invitation_id: str | None = None,
        expires_at: datetime | None = None,
        reason: str = "approved new workforce device",
    ) -> InternalInvitationRequest:
        candidate = key or self.candidate_key
        return InternalInvitationRequest(
            invitation_id=invitation_id,
            domain_id="corp.example",
            invited_oidc_issuer="https://id.corp.example",
            invited_oidc_subject="new-workforce-subject",
            invited_verified_email="new.person@corp.example",
            candidate_harness_id="new-person-codex-device",
            candidate_harness_kind="codex",
            candidate_harness_display_name="New person Codex device",
            candidate_binding_assurance="os_bound",
            candidate_key_id=candidate.thumbprint,
            candidate_public_key_pem=candidate.public_pem,
            requested_capabilities=("background_delivery", "messaging"),
            expires_at=expires_at or NOW + timedelta(minutes=15),
            predecessor_invitation_id=predecessor_invitation_id,
            reason=reason,
        )

    def accept(self, record, *, signer: P256KeyPair | None = None, source: str = SOURCE):
        canonical = canonical_json(record.transaction.model_dump(mode="json"))
        fields = self.service.candidate_possession_fields(record.transaction, self.verifier.result)
        signature = (signer or self.candidate_key).sign(INTERNAL_INVITATION_POP_PURPOSE, fields)
        return self.service.accept(
            invitation_id=record.transaction.invitation_id,
            canonical_invitation=canonical,
            oidc_evidence={"authorization_code_proof": "independently-verified"},
            candidate_possession_signature=signature,
            source_fingerprint=source,
            when=datetime.fromtimestamp(self.now_epoch, UTC),
        )


@pytest.fixture
def invitation_stack(tmp_path) -> InvitationStack:
    store = SQLiteStore(tmp_path / "internal-invitations.sqlite3", LocalEnvelopeCipher(b"v" * 32))
    sponsor_key = P256KeyPair.generate()
    outsider_key = P256KeyPair.generate()
    candidate_key = P256KeyPair.generate()
    now_epoch = int(NOW.timestamp())
    identities = (
        ("sponsor-human", "sponsor-harness", "sponsor-credential", sponsor_key, "sponsor@corp.example"),
        ("outsider-human", "outsider-harness", "outsider-credential", outsider_key, "outsider@corp.example"),
    )
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("corp.example", "active", 1, 1, now_epoch - 100),
        )
        for principal_id, harness_id, credential_id, key, email in identities:
            connection.execute(
                """
                INSERT INTO principals(
                    principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                ) VALUES(?,?,?,?,?,'active',?)
                """,
                (principal_id, "corp.example", "https://id.corp.example", principal_id, email, now_epoch - 100),
            )
            connection.execute(
                """
                INSERT INTO harnesses(
                    harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                    binding_assurance,capabilities_json,credential_epoch,created_at
                ) VALUES(?,?,?,NULL,'codex',?,'active','os_bound','[]',1,?)
                """,
                (harness_id, "corp.example", principal_id, harness_id, now_epoch - 100),
            )
            connection.execute(
                """
                INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,'active',1,?,?)
                """,
                (credential_id, harness_id, key.thumbprint, key.public_pem, now_epoch - 100, now_epoch + 7200),
            )
    sponsor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="sponsor-human",
        harness_id="sponsor-harness",
        credential_id="sponsor-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    outsider = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="outsider-human",
        harness_id="outsider-harness",
        credential_id="outsider-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    identity = VerifiedOIDCIdentity(
        issuer="https://id.corp.example",
        subject="new-workforce-subject",
        verified_email="new.person@corp.example",
    )
    verifier = SyntheticIndependentOIDCVerifier(
        OIDCVerificationResult(
            identity=identity,
            id_token_hash=hashlib.sha256(b"independently-verified-id-token").hexdigest(),
            expires_at=now_epoch + 1800,
        )
    )
    service = InternalInvitationService(
        store,
        oidc_verifier=verifier,
        credential_ttl_seconds=3600,
        maximum_failures_per_source=2,
        lockout_seconds=60,
        clock=lambda: now_epoch,
    )
    stack = InvitationStack(
        store=store,
        sponsor_key=sponsor_key,
        outsider_key=outsider_key,
        sponsor=sponsor,
        outsider=outsider,
        candidate_key=candidate_key,
        verifier=verifier,
        service=service,
        now_epoch=now_epoch,
    )
    try:
        yield stack
    finally:
        store.close()


def test_issue_is_zero_authority_then_accept_atomically_enrolls_exact_binding(invitation_stack: InvitationStack) -> None:
    request = invitation_stack.request()
    before = {
        table: invitation_stack.store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"]
        for table in ("principals", "harnesses", "credentials", "entitlements")
    }
    authority = invitation_stack.authority(request)
    after_authority = {
        table: invitation_stack.store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"]
        for table in before
    }
    record = invitation_stack.service.issue(
        request,
        authority=authority,
        when=NOW,
    )
    assert record.state == "active"
    assert record.use_count == 0
    assert record.transaction.sponsor_authority_id == "sponsor-human"
    assert record.transaction.sponsor_harness_id == "sponsor-harness"
    assert record.transaction.sponsor_credential_epoch == 1
    assert record.transaction.policy_revision == 1
    assert record.transaction.domain_revocation_epoch == 1
    after_issue = {
        table: invitation_stack.store.fetch_one(f"SELECT COUNT(*) AS count FROM {table}")["count"]
        for table in before
    }
    assert after_issue == after_authority

    accepted = invitation_stack.accept(record)
    assert accepted.harness_id == request.candidate_harness_id
    assert accepted.key_id == request.candidate_key_id
    assert accepted.actor.principal_id == accepted.principal_id
    assert accepted.actor.harness_id == accepted.harness_id
    assert accepted.positive_entitlements_issued == 0
    row = invitation_stack.store.fetch_one(
        "SELECT * FROM internal_invitations WHERE invitation_id=?", (request.invitation_id,)
    )
    assert row["state"] == "consumed"
    assert row["use_count"] == 1
    assert row["revision"] == 2
    harness = invitation_stack.store.fetch_one(
        "SELECT * FROM harnesses WHERE harness_id=?", (request.candidate_harness_id,)
    )
    assert harness["principal_id"] == accepted.principal_id
    assert harness["credential_epoch"] == 1
    assert harness["capabilities_json"] == '["background_delivery","messaging"]'
    credential = invitation_stack.store.fetch_one(
        "SELECT * FROM credentials WHERE credential_id=?", (accepted.credential_id,)
    )
    assert credential["key_id"] == request.candidate_key_id
    assert invitation_stack.store.fetch_one("SELECT COUNT(*) AS count FROM entitlements")["count"] == after_authority[
        "entitlements"
    ]
    audit = "\n".join(
        row["record_json"] for row in invitation_stack.store.fetch_all("SELECT record_json FROM audit_log")
    )
    assert "new.person@corp.example" not in audit
    assert "BEGIN PUBLIC KEY" not in audit


@pytest.mark.parametrize("drift", ["policy", "domain_epoch", "sponsor_credential", "domain_revoked"])
def test_accept_fails_closed_on_current_authority_drift(
    invitation_stack: InvitationStack,
    drift: str,
) -> None:
    request = invitation_stack.request()
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)
    with invitation_stack.store.transaction() as connection:
        if drift == "policy":
            connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='corp.example'")
        elif drift == "domain_epoch":
            connection.execute("UPDATE domains SET revocation_epoch=2 WHERE domain_id='corp.example'")
        elif drift == "sponsor_credential":
            connection.execute("UPDATE credentials SET status='retired' WHERE credential_id='sponsor-credential'")
            connection.execute("UPDATE harnesses SET credential_epoch=2 WHERE harness_id='sponsor-harness'")
        else:
            connection.execute("UPDATE domains SET status='revoked' WHERE domain_id='corp.example'")

    with pytest.raises(AuthenticationError, match="unavailable or invalid"):
        invitation_stack.accept(record)
    row = invitation_stack.store.fetch_one(
        "SELECT state,use_count FROM internal_invitations WHERE invitation_id=?", (request.invitation_id,)
    )
    assert (row["state"], row["use_count"]) == ("active", 0)
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM harnesses WHERE harness_id=?", (request.candidate_harness_id,)
    )["count"] == 0


def test_wrong_oidc_key_and_canonical_bytes_fail_without_burning_invitation(
    invitation_stack: InvitationStack,
) -> None:
    request = invitation_stack.request()
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)
    canonical = canonical_json(record.transaction.model_dump(mode="json"))
    valid_fields = invitation_stack.service.candidate_possession_fields(
        record.transaction, invitation_stack.verifier.result
    )

    with pytest.raises(AuthenticationError, match="unavailable or invalid"):
        invitation_stack.service.accept(
            invitation_id=request.invitation_id,
            canonical_invitation=canonical,
            oidc_evidence={"identity": request.invited_verified_email},
            candidate_possession_signature=invitation_stack.candidate_key.sign(
                INTERNAL_INVITATION_POP_PURPOSE, valid_fields
            ),
            source_fingerprint=SOURCE,
            when=NOW,
        )
    different_source = hashlib.sha256(b"trusted-transport-source-2").hexdigest()
    with pytest.raises(AuthenticationError, match="unavailable or invalid"):
        invitation_stack.service.accept(
            invitation_id=request.invitation_id,
            canonical_invitation=canonical,
            oidc_evidence={"authorization_code_proof": "independently-verified"},
            candidate_possession_signature=P256KeyPair.generate().sign(
                INTERNAL_INVITATION_POP_PURPOSE, valid_fields
            ),
            source_fingerprint=different_source,
            when=NOW,
        )
    tampered = record.transaction.model_copy(update={"reason": "substituted sponsor intent"})
    tampered_bytes = canonical_json(tampered.model_dump(mode="json"))
    with pytest.raises(AuthenticationError, match="unavailable or invalid"):
        invitation_stack.service.accept(
            invitation_id=request.invitation_id,
            canonical_invitation=tampered_bytes,
            oidc_evidence={"authorization_code_proof": "independently-verified"},
            candidate_possession_signature=invitation_stack.candidate_key.sign(
                INTERNAL_INVITATION_POP_PURPOSE,
                invitation_stack.service.candidate_possession_fields(tampered, invitation_stack.verifier.result),
            ),
            source_fingerprint=hashlib.sha256(b"trusted-transport-source-3").hexdigest(),
            when=NOW,
        )
    row = invitation_stack.store.fetch_one(
        "SELECT state,use_count FROM internal_invitations WHERE invitation_id=?", (request.invitation_id,)
    )
    assert (row["state"], row["use_count"]) == ("active", 0)
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM internal_invitation_abuse WHERE invitation_id=?",
        (request.invitation_id,),
    )["count"] == 3


def test_durable_lockout_does_not_consume_invite_and_valid_accept_works_after_lockout(
    invitation_stack: InvitationStack,
) -> None:
    request = invitation_stack.request()
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)
    for _ in range(2):
        with pytest.raises(AuthenticationError):
            invitation_stack.accept(record, signer=invitation_stack.outsider_key)
    abuse = invitation_stack.store.fetch_one(
        "SELECT * FROM internal_invitation_abuse WHERE invitation_id=? AND source_fingerprint=?",
        (request.invitation_id, SOURCE),
    )
    assert abuse["failure_count"] == 2
    assert abuse["locked_until"] == invitation_stack.now_epoch + 60
    active = invitation_stack.store.fetch_one(
        "SELECT state,use_count FROM internal_invitations WHERE invitation_id=?", (request.invitation_id,)
    )
    assert (active["state"], active["use_count"]) == ("active", 0)
    with pytest.raises(AuthenticationError):
        invitation_stack.accept(record)
    assert invitation_stack.store.fetch_one(
        "SELECT failure_count FROM internal_invitation_abuse WHERE invitation_id=? AND source_fingerprint=?",
        (request.invitation_id, SOURCE),
    )["failure_count"] == 2

    invitation_stack.now_epoch += 61
    accepted = invitation_stack.accept(record)
    assert accepted.harness_id == request.candidate_harness_id


def test_accept_race_and_replay_create_exactly_one_binding(invitation_stack: InvitationStack) -> None:
    request = invitation_stack.request()
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)

    def accept_once():
        try:
            return invitation_stack.accept(record)
        except Exception as exc:
            return exc

    with ThreadPoolExecutor(max_workers=2) as pool:
        outcomes = list(pool.map(lambda _index: accept_once(), range(2)))
    assert sum(not isinstance(outcome, Exception) for outcome in outcomes) == 1
    assert sum(isinstance(outcome, AuthenticationError) for outcome in outcomes) == 1
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM harnesses WHERE harness_id=?", (request.candidate_harness_id,)
    )["count"] == 1
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM credentials WHERE harness_id=?", (request.candidate_harness_id,)
    )["count"] == 1
    with pytest.raises(AuthenticationError, match="unavailable or invalid"):
        invitation_stack.accept(record)


def test_one_verified_oidc_proof_cannot_accept_two_distinct_invitations(
    invitation_stack: InvitationStack,
) -> None:
    first_request = invitation_stack.request()
    first = invitation_stack.service.issue(
        first_request,
        authority=invitation_stack.authority(first_request),
        when=NOW,
    )
    second_key = P256KeyPair.generate()
    second_request = invitation_stack.request(
        invitation_id="internal-invitation-oidc-replay-0002",
        key=second_key,
    ).model_copy(
        update={
            "candidate_harness_id": "new-person-second-device",
            "candidate_harness_display_name": "New person second device",
        }
    )
    second = invitation_stack.service.issue(
        second_request,
        authority=invitation_stack.authority(second_request),
        when=NOW,
    )
    invitation_stack.accept(first)
    canonical = canonical_json(second.transaction.model_dump(mode="json"))
    fields = invitation_stack.service.candidate_possession_fields(
        second.transaction, invitation_stack.verifier.result
    )
    with pytest.raises(AuthenticationError, match="unavailable or invalid"):
        invitation_stack.service.accept(
            invitation_id=second_request.invitation_id,
            canonical_invitation=canonical,
            oidc_evidence={"authorization_code_proof": "independently-verified"},
            candidate_possession_signature=second_key.sign(INTERNAL_INVITATION_POP_PURPOSE, fields),
            source_fingerprint=hashlib.sha256(b"trusted-transport-source-oidc-replay").hexdigest(),
            when=NOW,
        )
    row = invitation_stack.store.fetch_one(
        "SELECT state,use_count FROM internal_invitations WHERE invitation_id=?",
        (second_request.invitation_id,),
    )
    assert (row["state"], row["use_count"]) == ("active", 0)
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM harnesses WHERE harness_id=?",
        (second_request.candidate_harness_id,),
    )["count"] == 0


def _revocation_authority_and_command(
    stack: InvitationStack,
    *,
    invitation_id: str,
    revision: int,
    actor: VerifiedActor,
    key: P256KeyPair,
):
    reason = "sponsor withdrew the invitation"
    resource, context = stack.service.revocation_binding(
        invitation_id,
        expected_revision=revision,
        reason=reason,
    )
    engine = LocalConformancePolicyEngine(stack.store)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=INTERNAL_INVITATION_REVOKE_ACTION,
            resource_pattern=resource,
            revision=1,
            expires_at=NOW + timedelta(hours=1),
        ),
        when=NOW,
    )
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action=INTERNAL_INVITATION_REVOKE_ACTION,
            resource=resource,
            policy_revision=1,
            context={"request_digest": canonical_digest(context)},
        ),
        when=NOW,
    )
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=INTERNAL_INVITATION_REVOKE_ACTION,
        resource=resource,
        request_digest=canonical_digest(context),
        expected_policy_revision=1,
        expected_entity_revision=revision,
        reason=reason,
        issued_at=NOW,
        expires_at=NOW + timedelta(minutes=2),
    )
    return (
        IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        SignedAuthorityCommand(
            **fields,
            signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
        ),
    )


def test_exact_sponsor_revoke_expiry_and_safe_reissue(invitation_stack: InvitationStack) -> None:
    request = invitation_stack.request()
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)
    outsider_authority, outsider_command = _revocation_authority_and_command(
        invitation_stack,
        invitation_id=request.invitation_id,
        revision=record.revision,
        actor=invitation_stack.outsider,
        key=invitation_stack.outsider_key,
    )
    with pytest.raises(AuthorizationError, match="exact current invitation sponsor"):
        invitation_stack.service.revoke(
            request.invitation_id,
            command=outsider_command,
            authority=outsider_authority,
            when=NOW,
        )
    sponsor_authority, sponsor_command = _revocation_authority_and_command(
        invitation_stack,
        invitation_id=request.invitation_id,
        revision=record.revision,
        actor=invitation_stack.sponsor,
        key=invitation_stack.sponsor_key,
    )
    revoked = invitation_stack.service.revoke(
        request.invitation_id,
        command=sponsor_command,
        authority=sponsor_authority,
        when=NOW,
    )
    assert revoked.state == "revoked"
    assert revoked.revision == 2
    with pytest.raises(ConflictError):
        invitation_stack.service.revoke(
            request.invitation_id,
            command=sponsor_command,
            authority=sponsor_authority,
            when=NOW,
        )
    with pytest.raises(AuthenticationError):
        invitation_stack.accept(record)

    replacement_key = P256KeyPair.generate()
    missing_lineage = invitation_stack.request(
        invitation_id="internal-invitation-0000000000000002",
        key=replacement_key,
    )
    with pytest.raises(ConflictError, match="latest predecessor"):
        invitation_stack.service.issue(
            missing_lineage,
            authority=invitation_stack.authority(missing_lineage),
            when=NOW,
        )
    reissue = invitation_stack.request(
        invitation_id="internal-invitation-0000000000000003",
        key=replacement_key,
        predecessor_invitation_id=request.invitation_id,
        reason="fresh sponsor decision after revoked invite",
    )
    reissued = invitation_stack.service.issue(
        reissue,
        authority=invitation_stack.authority(reissue),
        when=NOW,
    )
    assert reissued.state == "active"
    assert reissued.transaction.predecessor_invitation_id == request.invitation_id
    assert reissued.transaction.predecessor_invitation_digest == record.invitation_digest
    assert reissued.transaction.predecessor_revision == revoked.revision

    expiring = replace(invitation_stack, candidate_key=replacement_key).request(
        invitation_id="internal-invitation-0000000000000004",
        predecessor_invitation_id=reissue.invitation_id,
        expires_at=NOW + timedelta(minutes=1),
        reason="successor used to prove automatic expiry",
    )
    # An active predecessor cannot be superseded by another reissue.
    with pytest.raises(ConflictError, match="active invitation"):
        invitation_stack.service.issue(
            expiring,
            authority=invitation_stack.authority(expiring),
            when=NOW,
        )


def test_expiry_marks_terminal_without_abuse_or_enrollment(invitation_stack: InvitationStack) -> None:
    request = invitation_stack.request(expires_at=NOW + timedelta(minutes=1))
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)
    invitation_stack.now_epoch += 60
    with pytest.raises(AuthenticationError):
        invitation_stack.accept(record)
    row = invitation_stack.store.fetch_one(
        "SELECT state,use_count,revision,revoked_at FROM internal_invitations WHERE invitation_id=?",
        (request.invitation_id,),
    )
    assert (row["state"], row["use_count"], row["revision"], row["revoked_at"]) == (
        "expired",
        0,
        2,
        invitation_stack.now_epoch,
    )
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM internal_invitation_abuse WHERE invitation_id=?",
        (request.invitation_id,),
    )["count"] == 0
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM harnesses WHERE harness_id=?", (request.candidate_harness_id,)
    )["count"] == 0


def test_issue_retry_is_byte_idempotent_and_same_id_different_bytes_conflicts(
    invitation_stack: InvitationStack,
) -> None:
    request = invitation_stack.request()
    authority = invitation_stack.authority(request)
    first = invitation_stack.service.issue(request, authority=authority, when=NOW)
    retried = invitation_stack.service.issue(request, authority=authority, when=NOW)
    assert retried == first
    changed = request.model_copy(update={"reason": "different exact sponsor intent"})
    with pytest.raises(ConflictError, match="different canonical bytes"):
        invitation_stack.service.issue(
            changed,
            authority=invitation_stack.authority(changed),
            when=NOW,
        )


def test_unknown_and_replayed_invites_do_not_enumerate_identity_or_create_abuse(
    invitation_stack: InvitationStack,
) -> None:
    request = invitation_stack.request()
    record = invitation_stack.service.issue(request, authority=invitation_stack.authority(request), when=NOW)
    invitation_stack.accept(record)
    canonical = canonical_json(record.transaction.model_dump(mode="json"))
    signature = invitation_stack.candidate_key.sign(
        INTERNAL_INVITATION_POP_PURPOSE,
        invitation_stack.service.candidate_possession_fields(record.transaction, invitation_stack.verifier.result),
    )
    messages = []
    for invitation_id in (request.invitation_id, "unknown-internal-invitation-00000001"):
        with pytest.raises(AuthenticationError) as caught:
            invitation_stack.service.accept(
                invitation_id=invitation_id,
                canonical_invitation=canonical,
                oidc_evidence={"authorization_code_proof": "independently-verified"},
                candidate_possession_signature=signature,
                source_fingerprint=SOURCE,
                when=NOW,
            )
        messages.append(str(caught.value))
    assert messages == ["internal invitation is unavailable or invalid"] * 2
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM internal_invitation_abuse WHERE invitation_id=?",
        ("unknown-internal-invitation-00000001",),
    )["count"] == 0


def test_invitation_models_reject_unknown_fields_unsorted_scope_and_key_substitution(
    invitation_stack: InvitationStack,
) -> None:
    values = invitation_stack.request().model_dump(mode="python")
    with pytest.raises(PydanticValidationError):
        InternalInvitationRequest.model_validate({**values, "claimed_sponsor": "attacker"})
    with pytest.raises(PydanticValidationError, match="canonically sorted"):
        InternalInvitationRequest.model_validate(
            {**values, "requested_capabilities": ("messaging", "background_delivery")}
        )
    with pytest.raises(PydanticValidationError, match="does not match"):
        InternalInvitationRequest.model_validate(
            {**values, "candidate_public_key_pem": P256KeyPair.generate().public_pem}
        )


def test_guest_sponsor_requires_and_consumes_existing_exact_positive_grant(
    invitation_stack: InvitationStack,
) -> None:
    guest_key = P256KeyPair.generate()
    now = invitation_stack.now_epoch
    with invitation_stack.store.transaction() as connection:
        connection.execute(
            """
            INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,
                sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,'active',?)
            """,
            (
                "guest-sponsor",
                "corp.example",
                "partner.example",
                "pairwise-guest-sponsor-subject",
                "sponsor-human",
                now + 3600,
            ),
        )
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,NULL,?,'codex',?,'active','os_bound','[]',1,?)
            """,
            ("guest-sponsor-harness", "corp.example", "guest-sponsor", "Guest sponsor", now - 10),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)
            """,
            (
                "guest-sponsor-credential",
                "guest-sponsor-harness",
                guest_key.thumbprint,
                guest_key.public_pem,
                now - 10,
                now + 3600,
            ),
        )
    guest = VerifiedActor(
        kind=ActorKind.HOST_GUEST_HARNESS,
        domain_id="corp.example",
        guest_id="guest-sponsor",
        harness_id="guest-sponsor-harness",
        credential_id="guest-sponsor-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    request = invitation_stack.request(
        invitation_id="guest-issued-internal-invitation-000001",
    )
    resource, context = invitation_stack.service.issuance_binding(request)
    grant = TaskGrant(
        grant_id="guest-invitation-positive-grant",
        domain_id="corp.example",
        principal_id="guest-sponsor",
        harness_id="guest-sponsor-harness",
        actions=frozenset({INTERNAL_INVITATION_ISSUE_ACTION}),
        resources=frozenset({resource}),
        input_sources=frozenset({"verified_guest_transport"}),
        output_sinks=frozenset({"internal_identity_authority"}),
        data_classes=frozenset({Classification.C0_PUBLIC}),
        max_uses=1,
        expires_at=NOW + timedelta(minutes=30),
    )
    with invitation_stack.store.transaction() as connection:
        connection.execute(
            """
            INSERT INTO task_grants(
                grant_id,domain_id,principal_id,harness_id,grant_json,
                max_uses,uses,expires_at,revoked_at
            ) VALUES(?,?,?,?,?,1,0,?,NULL)
            """,
            (
                grant.grant_id,
                grant.domain_id,
                grant.principal_id,
                grant.harness_id,
                canonical_json(grant.model_dump(mode="json")).decode("utf-8"),
                int(grant.expires_at.timestamp()),
            ),
        )
        connection.execute(
            "INSERT INTO metadata(key,value) VALUES(?,?)",
            (
                f"authority-binding:task-grant:{grant.grant_id}",
                canonical_json(
                    {
                        "schema": "agentnet.task-grant.authority-binding.v1",
                        "grant_id": grant.grant_id,
                        "domain_id": grant.domain_id,
                        "principal_id": grant.principal_id,
                        "harness_id": grant.harness_id,
                        "policy_revision": 1,
                        "harness_credential_epoch": 1,
                        "issued_at": now,
                    }
                ).decode("utf-8"),
            ),
        )
    policy = PolicyEngine(invitation_stack.store)
    with pytest.raises(AuthorizationError, match="exact_task_grant_required"):
        policy.require(
            AuthorizationRequest(
                actor=guest,
                action=INTERNAL_INVITATION_ISSUE_ACTION,
                resource=resource,
                policy_revision=1,
                context=context,
            ),
            when=NOW,
        )
    decision = policy.require(
        AuthorizationRequest(
            actor=guest,
            action=INTERNAL_INVITATION_ISSUE_ACTION,
            resource=resource,
            policy_revision=1,
            context=context,
            grant_use=GrantUse(
                grant_id=grant.grant_id,
                action=INTERNAL_INVITATION_ISSUE_ACTION,
                resource=resource,
                input_source="verified_guest_transport",
                output_sink="internal_identity_authority",
                data_class=Classification.C0_PUBLIC,
            ),
        ),
        when=NOW,
    )
    record = invitation_stack.service.issue(
        request,
        authority=IssuanceAuthority(actor=guest, policy_decision_id=decision.decision_id),
        when=NOW,
    )
    assert record.transaction.sponsor_authority_kind == "guest"
    assert record.transaction.sponsor_authority_id == "guest-sponsor"
    assert invitation_stack.store.fetch_one(
        "SELECT uses FROM task_grants WHERE grant_id=?", (grant.grant_id,)
    )["uses"] == 1
    assert invitation_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM entitlements WHERE principal_id='guest-sponsor'"
    )["count"] == 0


def test_service_runs_through_backend_neutral_store_contract(
    invitation_stack: InvitationStack,
) -> None:
    backend = DelegatingBackendContract(invitation_stack.store)
    service = InternalInvitationService(
        backend,
        oidc_verifier=invitation_stack.verifier,
        credential_ttl_seconds=3600,
        clock=lambda: invitation_stack.now_epoch,
    )
    request = invitation_stack.request(
        invitation_id="backend-neutral-internal-invitation-0001",
    )
    record = service.issue(
        request,
        authority=invitation_stack.authority(request),
        when=NOW,
    )
    canonical = canonical_json(record.transaction.model_dump(mode="json"))
    fields = service.candidate_possession_fields(record.transaction, invitation_stack.verifier.result)
    accepted = service.accept(
        invitation_id=request.invitation_id,
        canonical_invitation=canonical,
        oidc_evidence={"authorization_code_proof": "independently-verified"},
        candidate_possession_signature=invitation_stack.candidate_key.sign(
            INTERNAL_INVITATION_POP_PURPOSE, fields
        ),
        source_fingerprint=SOURCE,
        when=NOW,
    )
    assert backend.backend_name == "backend-contract"
    assert accepted.harness_id == request.candidate_harness_id
