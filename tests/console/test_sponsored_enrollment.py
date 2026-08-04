from __future__ import annotations

import hashlib
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.errors import AuthenticationError
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitations import InternalInvitationRecord, InternalInvitationTransaction
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.identity.sponsored_enrollment import (
    SponsoredEnrollmentIntentRequest,
    SponsoredEnrollmentService,
)
from agentnet.security.signatures import P256KeyPair, canonical_json


def _request(**changes):
    values = {
        "intent_id": "intent-id-1234567890",
        "target_kind": "existing_person",
        "target_principal_id": "person-1",
        "invited_verified_email": None,
        "harness_kind": "laptop",
        "harness_display_name": "Field laptop",
        "requested_capabilities": ("message.send",),
        "expires_at": datetime.now(UTC) + timedelta(hours=1),
        "reason": "Add the person's second laptop",
    }
    values.update(changes)
    return SponsoredEnrollmentIntentRequest(**values)


def _identity_for(store, principal_id: str) -> VerifiedOIDCIdentity:
    row = store.fetch_one(
        "SELECT oidc_issuer,oidc_subject,verified_email FROM principals WHERE principal_id=?",
        (principal_id,),
    )
    assert row is not None
    return VerifiedOIDCIdentity(
        issuer=str(row["oidc_issuer"]),
        subject=str(row["oidc_subject"]),
        verified_email=str(row["verified_email"]),
    )


class _Provider:
    def __init__(self, identity: VerifiedOIDCIdentity, now: list[int]) -> None:
        self.identity = identity
        self.now = now

    def authorization_url(self, **_request) -> str:
        return "https://idp.example/authorize"

    def exchange_and_verify(self, **_request) -> OIDCVerificationResult:
        return OIDCVerificationResult(
            identity=self.identity,
            id_token_hash="a" * 64,
            expires_at=self.now[0] + 600,
        )


class _ApprovalClient:
    def __init__(
        self,
        now: list[int],
        *,
        advance_status_to: int | None = None,
        advance_receipt_to: int | None = None,
    ) -> None:
        self.now = now
        self.advance_status_to = advance_status_to
        self.advance_receipt_to = advance_receipt_to
        self.receipt_calls = 0

    def create_request(self, **_request):
        raise AssertionError("approval creation was not expected")

    def request_status(self, **_request):
        if self.advance_status_to is not None:
            self.now[0] = self.advance_status_to
        return {"state": "issued"}

    def retrieve_receipt(self, **_request):
        self.receipt_calls += 1
        if self.advance_receipt_to is not None:
            self.now[0] = self.advance_receipt_to
        return {"receipt": "must not be verified after expiry"}


class _NeverVerifier:
    def verify(self, **_request):
        raise AssertionError("an expired Approval receipt must not be verified")


class _InvitationRows:
    def __init__(self, record: InternalInvitationRecord | None = None) -> None:
        self.record = record

    def _from_row(self, _row):
        assert self.record is not None
        return self.record

    def issue(self, *_args, **_kwargs):
        raise AssertionError("an expired Approval must not issue an invitation")


def _service(
    store,
    identity: VerifiedOIDCIdentity,
    now: list[int],
    *,
    approval_client=None,
    invitations=None,
) -> SponsoredEnrollmentService:
    return SponsoredEnrollmentService(
        store=store,
        provider=_Provider(identity, now),
        invitations=invitations or _InvitationRows(),
        approval_client=approval_client or _ApprovalClient(now),
        approval_verifier=_NeverVerifier(),
        require=lambda **_request: SimpleNamespace(decision_id="decision-1"),
        clock=lambda: now[0],
    )


def _insert_intent(
    store,
    actor,
    *,
    intent_id: str,
    now: int,
    state: str = "waiting_target",
    harness_kind: str = "laptop",
    harness_name: str = "Field laptop",
    candidate_transaction_id: str | None = None,
    approval_expires_at: int | None = None,
) -> None:
    request = {
        "schema": "agentnet.console.enrollment-intent.v1",
        "intent_id": intent_id,
        "target_kind": "existing_person",
        "target": {
            "principal_id": actor.principal_id,
            "oidc_issuer": "https://idp.example",
            "oidc_subject": "bound-by-persisted-principal",
            "verified_email": "bound@example.test",
        },
        "harness_kind": harness_kind,
        "harness_name": harness_name,
        "capabilities": ["message.send"],
        "reason": "Add the person's second laptop",
        "expires_at": now + 3_600,
    }
    approval_transaction = {
        "schema": "agentnet.enrollment.challenge.v1",
        "expires_at": approval_expires_at or now + 600,
    }
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO console_enrollment_intents(
                intent_id,domain_id,sponsor_principal_id,sponsor_harness_id,target_kind,
                target_principal_id,invited_email_alias,request_json,request_digest,state,
                revision,candidate_transaction_id,approval_request_id,approval_transaction_digest,
                approval_transaction_json,possession_secret_encrypted,created_at,updated_at,expires_at
            ) VALUES(?,?,?,?,?, ?,NULL,?,?,?, 1,?,?,?,?,?,?,?,?)""",
            (
                intent_id,
                actor.domain_id,
                actor.principal_id,
                actor.harness_id,
                "existing_person",
                actor.principal_id,
                canonical_json(request).decode(),
                hashlib.sha256(canonical_json(request)).hexdigest(),
                state,
                candidate_transaction_id,
                f"approval-{intent_id}" if state == "waiting_approval" else None,
                "b" * 64 if state == "waiting_approval" else None,
                canonical_json(approval_transaction).decode() if state == "waiting_approval" else None,
                (
                    store.encrypted_payload({"possession_secret": "secret"}, intent_id)
                    if state == "waiting_approval"
                    else None
                ),
                now,
                now,
                now + 3_600,
            ),
        )


def _issued_record(actor, key: P256KeyPair, intent_id: str, now: int) -> InternalInvitationRecord:
    transaction = InternalInvitationTransaction(
        invitation_id=intent_id,
        domain_id=actor.domain_id,
        sponsor_authority_kind="human",
        sponsor_authority_id=str(actor.positive_authority_id),
        sponsor_harness_id=actor.harness_id,
        sponsor_credential_id=actor.credential_id,
        sponsor_credential_epoch=actor.credential_epoch,
        invited_oidc_issuer="https://idp.example",
        invited_oidc_subject="subject",
        invited_verified_email="person@example.test",
        candidate_harness_id="candidate-harness",
        candidate_harness_kind="laptop",
        candidate_harness_display_name="Field laptop",
        candidate_binding_assurance="os_bound",
        candidate_key_id=key.thumbprint,
        candidate_public_key_pem=key.public_pem,
        requested_capabilities=("message.send",),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=datetime.fromtimestamp(now + 3_600, UTC),
        reason="Add the person's second laptop",
    )
    timestamp = datetime.fromtimestamp(now, UTC)
    return InternalInvitationRecord(
        transaction=transaction,
        invitation_digest=transaction.digest,
        state="active",
        revision=1,
        use_count=0,
        created_at=timestamp,
        updated_at=timestamp,
    )


def _insert_invitation_row(store, actor, record: InternalInvitationRecord, now: int) -> None:
    transaction = record.transaction
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO internal_invitations(
                invitation_id,schema_version,domain_id,sponsor_authority_kind,sponsor_authority_id,
                sponsor_harness_id,sponsor_credential_id,sponsor_credential_epoch,
                invited_oidc_issuer,invited_oidc_subject,invited_verified_email,
                candidate_harness_id,candidate_harness_kind,candidate_key_id,candidate_public_key_pem,
                requested_capabilities_json,policy_revision,domain_revocation_epoch,max_uses,use_count,
                state,revision,canonical_invitation_json,invitation_digest,expires_at,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                transaction.invitation_id,
                "1.0",
                transaction.domain_id,
                "human",
                str(actor.positive_authority_id),
                actor.harness_id,
                actor.credential_id,
                actor.credential_epoch,
                transaction.invited_oidc_issuer,
                transaction.invited_oidc_subject,
                transaction.invited_verified_email,
                transaction.candidate_harness_id,
                transaction.candidate_harness_kind,
                transaction.candidate_key_id,
                transaction.candidate_public_key_pem,
                canonical_json(list(transaction.requested_capabilities)).decode(),
                1,
                1,
                1,
                0,
                "active",
                1,
                canonical_json(transaction.model_dump(mode="json")).decode(),
                record.invitation_digest,
                now + 3_600,
                now,
                now,
            ),
        )


def test_sponsored_intent_requires_exactly_one_identity_target() -> None:
    assert _request().target_principal_id == "person-1"
    with pytest.raises(PydanticValidationError):
        _request(invited_verified_email="person@example.test")
    with pytest.raises(PydanticValidationError):
        _request(target_kind="new_person", target_principal_id=None, invited_verified_email=None)


def test_sponsored_intent_rejects_noncanonical_capability_set() -> None:
    with pytest.raises(PydanticValidationError):
        _request(requested_capabilities=("message.send", "message.send"))


def test_candidate_matches_only_the_exact_harness_kind_and_name(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    now = [1_900_000_000]
    identity = _identity_for(store, actor.principal_id)
    service = _service(store, identity, now)
    key = P256KeyPair.generate()
    begin = service.begin_candidate(
        candidate_harness_id="candidate-harness",
        harness_kind="laptop",
        harness_name="Field laptop",
        binding_assurance="os_bound",
        public_key_pem=key.public_pem,
        idempotency_key="candidate-begin-idempotency",
    )
    _insert_intent(
        store,
        actor,
        intent_id="intent-tablet-123456",
        now=now[0],
        harness_kind="tablet",
    )
    _insert_intent(
        store,
        actor,
        intent_id="intent-laptop-12345",
        now=now[0],
        harness_kind="laptop",
    )

    assert service.complete_candidate_oidc(state=begin.state, code="code") == "intent-laptop-12345"
    assert store.fetch_one(
        "SELECT state FROM console_enrollment_intents WHERE intent_id=?",
        ("intent-tablet-123456",),
    )["state"] == "waiting_target"


@pytest.mark.parametrize("external_call", ["status", "receipt"])
def test_delayed_approval_external_call_cannot_issue_after_transaction_expiry(
    store, identity_factory, external_call: str
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    now = [1_900_000_000]
    expires_at = now[0] + 30
    client = _ApprovalClient(
        now,
        advance_status_to=expires_at if external_call == "status" else None,
        advance_receipt_to=expires_at if external_call == "receipt" else None,
    )
    _insert_intent(
        store,
        actor,
        intent_id=f"intent-delay-{external_call}-123",
        now=now[0],
        state="waiting_approval",
        approval_expires_at=expires_at,
    )
    service = _service(
        store,
        _identity_for(store, actor.principal_id),
        now,
        approval_client=client,
    )

    assert service.reconcile(
        actor=actor, intent_id=f"intent-delay-{external_call}-123"
    ) == "failed"
    assert store.fetch_one(
        "SELECT state FROM console_enrollment_intents WHERE intent_id=?",
        (f"intent-delay-{external_call}-123",),
    )["state"] == "failed"
    assert client.receipt_calls == (0 if external_call == "status" else 1)


def test_continuation_expires_and_is_consumed_only_when_invitation_is_released(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    now = [1_900_000_000]
    key = P256KeyPair.generate()
    identity = _identity_for(store, actor.principal_id)
    record = _issued_record(actor, key, "intent-release-123456", now[0])
    service = _service(store, identity, now, invitations=_InvitationRows(record))
    begin = service.begin_candidate(
        candidate_harness_id=record.transaction.candidate_harness_id,
        harness_kind=record.transaction.candidate_harness_kind,
        harness_name=record.transaction.candidate_harness_display_name,
        binding_assurance="os_bound",
        public_key_pem=key.public_pem,
        idempotency_key="candidate-release-idempotency",
    )
    _insert_intent(
        store,
        actor,
        intent_id=record.transaction.invitation_id,
        now=now[0],
        state="candidate_verified",
        candidate_transaction_id=begin.transaction_id,
    )
    with store.transaction() as connection:
        connection.execute(
            """UPDATE console_enrollment_candidates
               SET state='candidate_verified',intent_id=?
               WHERE transaction_id=?""",
            (record.transaction.invitation_id, begin.transaction_id),
        )
    assert service.candidate_status(continuation_token=begin.continuation_token)["state"] == (
        "candidate_verified"
    )
    assert store.fetch_one(
        "SELECT consumed_at FROM console_enrollment_candidates WHERE transaction_id=?",
        (begin.transaction_id,),
    )["consumed_at"] is None

    _insert_invitation_row(store, actor, record, now[0])
    with store.transaction() as connection:
        connection.execute(
            "UPDATE console_enrollment_candidates SET state='invitation_issued' WHERE transaction_id=?",
            (begin.transaction_id,),
        )
    released = service.candidate_status(continuation_token=begin.continuation_token)
    assert released["invitation"]["transaction"]["invitation_id"] == record.transaction.invitation_id
    with pytest.raises(AuthenticationError):
        service.candidate_status(continuation_token=begin.continuation_token)

    expired = service.begin_candidate(
        candidate_harness_id="expired-candidate",
        harness_kind="laptop",
        harness_name="Expired laptop",
        binding_assurance="os_bound",
        public_key_pem=P256KeyPair.generate().public_pem,
        idempotency_key="candidate-expired-idempotency",
    )
    now[0] = expired.expires_at
    with pytest.raises(AuthenticationError):
        service.candidate_status(continuation_token=expired.continuation_token)
