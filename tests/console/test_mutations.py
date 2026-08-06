from __future__ import annotations

import hashlib
from types import SimpleNamespace

import pytest

from agentnet.console.mutations import ConsoleMutationService
from agentnet.errors import AuthenticationError, AuthorizationError, IdempotencyConflict
from agentnet.identity.revocation import HarnessRevocationRequest
from agentnet.identity.invitation_links import InvitationLinkService
from agentnet.security.signatures import canonical_json


class _Clock:
    def __init__(self, value: int = 1_800_000_000) -> None:
        self.value = value

    def __call__(self) -> int:
        return self.value


class _Authorizer:
    def __init__(self) -> None:
        self.calls: list[dict[str, object]] = []

    def require(self, **request):
        self.calls.append(request)
        return SimpleNamespace(decision_id="decision-console-test")


class _ApprovalClient:
    def __init__(self, clock: _Clock) -> None:
        self.clock = clock
        self.receipt = {
            "approval_purpose": "identity.harness.revoke.approve",
            "receipt_id": "receipt-console-test",
            "transaction_digest": "0" * 64,
        }

    def create_request(self, **request):
        self.clock.value += 11
        self.receipt["transaction_digest"] = request["transaction_digest"]
        return {"request_id": "approval-console-test", "state": "pending"}

    def request_status(self, **_request):
        self.clock.value += 13
        return {"state": "issued"}

    def retrieve_receipt(self, **_request):
        self.clock.value += 17
        return self.receipt


class _HarnessRevocations:
    def __init__(self, store) -> None:
        self.store = store
        self.committed_at: int | None = None

    def prepare(self, *, domain_id: str, harness_id: str, reason: str) -> HarnessRevocationRequest:
        harness = self.store.fetch_one(
            "SELECT credential_epoch FROM harnesses WHERE domain_id=? AND harness_id=?",
            (domain_id, harness_id),
        )
        domain = self.store.fetch_one(
            "SELECT revocation_epoch FROM domains WHERE domain_id=?",
            (domain_id,),
        )
        assert harness is not None and domain is not None
        return HarnessRevocationRequest(
            request_id="prepared-console-test",
            domain_id=domain_id,
            harness_id=harness_id,
            expected_credential_epoch=int(harness["credential_epoch"]),
            expected_domain_revocation_epoch=int(domain["revocation_epoch"]),
            reason=reason,
        )

    def revoke(self, *, request, authority, approval, now: int, commit_callback):
        del authority, approval
        self.committed_at = now
        result = SimpleNamespace(
            domain_id=request.domain_id,
            harness_id=request.harness_id,
            credential_epoch=request.expected_credential_epoch + 1,
            domain_revocation_epoch=request.expected_domain_revocation_epoch + 1,
            revoked_credentials=1,
            already_revoked=False,
        )
        with self.store.transaction() as connection:
            connection.execute(
                "UPDATE harnesses SET status='revoked' WHERE domain_id=? AND harness_id=?",
                (request.domain_id, request.harness_id),
            )
            commit_callback(connection, result)
        return result


def _service(store, *, clock: _Clock | None = None):
    active_clock = clock or _Clock()
    authorizer = _Authorizer()
    approvals = _ApprovalClient(active_clock)
    revocations = _HarnessRevocations(store)
    with store.transaction() as connection:
        connection.execute(
            """INSERT OR IGNORE INTO policy_decisions(
                decision_id,occurred_at,actor_json,action,resource_json,context_json,
                allowed,reason,policy_revision
            ) VALUES(?,?,?,?,?,?,1,?,1)""",
            (
                "decision-console-test",
                active_clock.value,
                "{}",
                "identity.harness.revoke",
                "{}",
                "{}",
                "allowed",
            ),
        )
    invitation_links = InvitationLinkService(
        store,
        public_base_url="https://console.example/join",
        clock=active_clock,
    )
    service = ConsoleMutationService(
        store=store,
        approval_client=approvals,
        invitation_links=invitation_links,
        require=authorizer.require,
        harness_revocations=revocations,
        clock=active_clock,
    )
    return service, authorizer, approvals, revocations, active_clock


def test_enrollment_review_denies_unknown_capability_before_any_persistence(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    service, authorizer, _, _, _ = _service(store)

    with pytest.raises(AuthorizationError, match="service is not allowed"):
        service.prepare_enrollment_review(
            actor=actor,
            target_kind="new_person",
            target_principal_id=None,
            invited_email_alias="person@example.test",
            harness_kind="laptop",
            harness_name="Field laptop",
            capabilities=("message_delivery", "arbitrary.admin"),
            reason="Provide field access",
            idempotency_key="enrollment-review-id-0001",
        )

    assert authorizer.calls == []
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_reviews")["n"] == 0
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_intents")["n"] == 0


def test_review_normalizes_and_authorizes_exact_capabilities_before_intent_commit(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    service, authorizer, _, _, _ = _service(store)

    review = service.prepare_enrollment_review(
        actor=actor,
        target_kind="new_person",
        target_principal_id=None,
        invited_email_alias=" Person@Example.Test ",
        harness_kind="laptop",
        harness_name=" Field laptop ",
        capabilities=("offline_delivery", "message_delivery"),
        reason=" Provide field access ",
        idempotency_key="enrollment-review-id-0002",
    )

    assert review.person == "person@example.test"
    assert review.harness_name == "Field laptop"
    assert review.capabilities == ("message_delivery", "offline_delivery")
    assert store.fetch_one("SELECT COUNT(*) AS n FROM console_enrollment_intents")["n"] == 0
    assert authorizer.calls[-1]["context"] == {
        "target_kind": "new_person",
        "target_identity": {"verified_email": "person@example.test"},
        "harness_kind": "laptop",
        "harness_name": "Field laptop",
        "capabilities": ("message_delivery", "offline_delivery"),
        "expires_at": review.expires_at,
    }

    result = service.create_enrollment_intent(
        actor=actor,
        review_token=review.review_token,
    )

    assert result.intent_id == "enrollment-review-id-0002"
    row = store.fetch_one(
        "SELECT invited_email_alias,request_json FROM console_enrollment_intents WHERE intent_id=?",
        (result.intent_id,),
    )
    assert row["invited_email_alias"] == "person@example.test"
    assert '"harness_kind":"laptop"' in row["request_json"]
    review_row = store.fetch_one(
        "SELECT review_token_hash,state FROM console_enrollment_reviews"
    )
    assert review_row["review_token_hash"] == hashlib.sha256(
        review.review_token.encode("ascii")
    ).hexdigest()
    assert review_row["state"] == "consumed"
    assert review.review_token not in row["request_json"]
    with pytest.raises(AuthenticationError):
        service.create_enrollment_intent(
            actor=actor,
            review_token=review.review_token,
        )


def test_new_person_idempotency_rejects_a_different_canonical_email(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    service, _, _, _, _ = _service(store)
    common = {
        "actor": actor,
        "target_kind": "new_person",
        "target_principal_id": None,
        "harness_kind": "laptop",
        "harness_name": "Field laptop",
        "capabilities": ("message_delivery",),
        "reason": "Provide field access",
        "idempotency_key": "enrollment-review-id-0003",
    }
    first = service.prepare_enrollment_review(
        invited_email_alias="first@example.test", **common
    )
    service.create_enrollment_intent(actor=actor, review_token=first.review_token)
    second = service.prepare_enrollment_review(
        invited_email_alias="second@example.test", **common
    )

    with pytest.raises(IdempotencyConflict):
        service.create_enrollment_intent(actor=actor, review_token=second.review_token)


def test_revocation_refreshes_time_after_external_calls_and_persists_receipt_digest(
    store, identity_factory
) -> None:
    actor, _ = identity_factory(
        domain="corp.example", binding_assurance="hardware_bound"
    )
    target, _ = identity_factory(
        domain="corp.example",
        principal_id=actor.principal_id,
        binding_assurance="hardware_bound",
    )
    clock = _Clock()
    service, _, approvals, revocations, _ = _service(store, clock=clock)
    mutation_id = "harness-revocation-id-0001"

    service.request_harness_revocation(
        actor=actor,
        target_harness_id=target.harness_id,
        reason="Device was lost",
        idempotency_key=mutation_id,
    )
    assert store.fetch_one(
        "SELECT updated_at FROM console_mutations WHERE mutation_id=?", (mutation_id,)
    )["updated_at"] == clock.value

    result = service.reconcile_harness_revocation(actor=actor, mutation_id=mutation_id)

    expected_digest = hashlib.sha256(canonical_json(approvals.receipt)).hexdigest()
    row = store.fetch_one(
        "SELECT state,approval_receipt_digest,updated_at FROM console_mutations WHERE mutation_id=?",
        (mutation_id,),
    )
    assert result.state == "completed"
    assert row["state"] == "completed"
    assert row["approval_receipt_digest"] == expected_digest
    assert row["updated_at"] == clock.value == revocations.committed_at
