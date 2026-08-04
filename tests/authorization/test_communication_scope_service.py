from __future__ import annotations

import hashlib
from types import SimpleNamespace

import pytest

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    CommunicationScopeBeginRequest,
    CommunicationScopeCompleteRequest,
    CommunicationScopeStatusRequest,
)
from agentnet.authorization.communication_scope_service import CommunicationScopeService
from agentnet.errors import AuthenticationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import P256KeyPair

NOW = 1_800_000_000
BEGIN_KEY = "communication-scope-begin-key-0001"
COMPLETE_KEY = "communication-scope-complete-key-001"


def _evidence(role: str) -> dict[str, object]:
    return {
        "schema": "agentnet.bootstrap-plan.enrollment-evidence.v1",
        "role": role,
        "guided_oidc": True,
        "enrollment_challenge_id": f"challenge-{role}",
        "oidc_transaction_id": f"oidc-{role}",
        "enrollment_consumed_at": NOW - 60,
        "oidc_consumed_at": NOW - 60,
        "oidc_issuer": "https://idp.example",
        "oidc_subject_sha256": hashlib.sha256(b"subject").hexdigest(),
        "verified_email_sha256": hashlib.sha256(b"owner@example.test").hexdigest(),
        "candidate_key_thumbprint": f"thumbprint-{role}",
        "approval_purpose": "identity.enrollment.approve",
        "approval_receipt_id": f"enrollment-receipt-{role}",
        "approval_receipt_digest": hashlib.sha256(role.encode()).hexdigest(),
        "approval_verifier_id": "approval.corp.example",
        "approval_signer_key_id": "enrollment-signer",
        "approval_authenticated_at": NOW - 61,
        "approval_issued_at": NOW - 60,
    }


class MutableResolver:
    def __init__(self, actor: VerifiedActor) -> None:
        self.policy_revision = 1
        self.fresh_credential_epoch = actor.credential_epoch
        self.actor = actor

    def __call__(self, _connection, actor: VerifiedActor, _now: int):
        assert actor == self.actor
        return {
            "domain": {
                "domain_id": actor.domain_id,
                "policy_revision": self.policy_revision,
                "revocation_epoch": 1,
            },
            "principal": {"principal_id": actor.principal_id},
            "harnesses": {
                "owner": {
                    "harness_id": "owner-harness",
                    "credential_id": "owner-credential",
                    "credential_epoch": 1,
                    "binding_assurance": "os_bound",
                    "display_name": "Owner laptop",
                    "kind": "pi",
                },
                "fresh": {
                    "harness_id": actor.harness_id,
                    "credential_id": actor.credential_id,
                    "credential_epoch": self.fresh_credential_epoch,
                    "binding_assurance": actor.binding_assurance,
                    "display_name": "Fresh laptop",
                    "kind": "codex",
                },
            },
            "enrollment_evidence": {
                "owner": _evidence("owner"),
                "fresh": _evidence("fresh"),
            },
        }


class FakeApprovalClient:
    def __init__(self, key, approver, verifier) -> None:
        self.key = key
        self.approver = approver
        self.verifier = verifier
        self.canonical: bytes | None = None
        self.possession_hash: str | None = None
        self.state = "pending"
        self.wrong_status_digest = False
        self.wrong_receipt_transaction = False
        self.create_calls = 0
        self.retrieve_calls = 0

    def create_request(self, **kwargs):
        self.create_calls += 1
        self.canonical = kwargs["canonical_transaction"]
        self.possession_hash = kwargs["possession_hash"]
        return {
            "request_id": "approval-request-communication-scope-0001",
            "transaction_digest": kwargs["transaction_digest"],
            "expires_at": kwargs["request_expires_at"],
            "state": "pending",
        }

    def request_status(self, **kwargs):
        return {
            "request_id": kwargs["request_id"],
            "transaction_digest": (
                "f" * 64 if self.wrong_status_digest else kwargs["transaction_digest"]
            ),
            "state": self.state,
            "expires_at": NOW + 3_600,
        }

    def retrieve_receipt(self, **kwargs):
        self.retrieve_calls += 1
        assert self.possession_hash is not None
        assert hashlib.sha256(kwargs["possession_secret"].encode("ascii")).hexdigest() == self.possession_hash
        assert self.canonical is not None
        canonical = b'{"wrong":"transaction"}' if self.wrong_receipt_transaction else self.canonical
        return create_independent_approval_receipt(
            self.key,
            approver=self.approver,
            verifier_id=self.verifier.verifier_id,
            approval_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
            canonical_transaction=canonical,
            issued_at=NOW,
            expires_at=NOW + 300,
            authenticated_at=NOW,
        )


@pytest.fixture
def communication_stack(store, actor):
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=? WHERE credential_id=?",
            (NOW - 100, NOW + 86_400, actor.credential_id),
        )
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "owner-harness", actor.domain_id, actor.principal_id, None, "pi",
                "Owner laptop", "active", "os_bound", "[]", 1, NOW - 100,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                "owner-credential", "owner-harness", "owner-key", "owner-public-key",
                "active", 1, NOW - 100, NOW + 86_400,
            ),
        )
    resolver = MutableResolver(actor)
    key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=actor.principal_id,
        domain_id=actor.domain_id,
        signer_key_id=key.thumbprint,
        public_key_pem=key.public_pem,
        allowed_purposes=frozenset({COMMUNICATION_SCOPE_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {key.thumbprint: approver}, verifier_id="approval.corp.example"
    )
    client = FakeApprovalClient(key, approver, verifier)
    service = CommunicationScopeService(
        store,
        client,
        verifier,
        resolver=resolver,
        public_approval_url="https://approval.corp.example/approval",
        clock=lambda: NOW,
    )
    return SimpleNamespace(
        service=service, client=client, resolver=resolver, store=store, actor=actor
    )


def _begin(stack):
    return stack.service.begin(
        actor=stack.actor,
        request=CommunicationScopeBeginRequest(
            schema="agentnet.communication-scope.begin.v1",
            begin_idempotency_key=BEGIN_KEY,
        ),
    )


def _status(stack, *, actor=None):
    return stack.service.status(
        actor=actor or stack.actor,
        request=CommunicationScopeStatusRequest(
            schema="agentnet.communication-scope.status.v1",
            begin_idempotency_key=BEGIN_KEY,
        ),
    )


def _complete(stack):
    return stack.service.complete(
        actor=stack.actor,
        request=CommunicationScopeCompleteRequest(
            schema="agentnet.communication-scope.complete.v1",
            begin_idempotency_key=BEGIN_KEY,
            completion_idempotency_key=COMPLETE_KEY,
        ),
    )


def _commit(stack):
    _begin(stack)
    stack.client.state = "issued"
    assert _status(stack)["status"] == "approval_ready"
    return _complete(stack)


def test_pending_approval_creates_no_authority(communication_stack) -> None:
    assert _begin(communication_stack)["expires_at"] == NOW + 3_600
    assert _status(communication_stack)["status"] == "approval_pending"
    with pytest.raises(ConflictError, match="approval is not issued"):
        _complete(communication_stack)
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == 0


def test_approved_completion_commits_exact_persistent_scope(communication_stack) -> None:
    result = _commit(communication_stack)
    assert result == {
        "schema": "agentnet.communication-scope.complete-result.v1",
        "status": "communication_active",
        "authority_granted": True,
        "communication_usable": True,
        "authority_expires_at": None,
        "artifacts_enabled": False,
        "business_effects_enabled": False,
        "federation_enabled": False,
        "public_a2a_enabled": False,
    }
    entitlements = communication_stack.store.fetch_all(
        "SELECT action,resource_pattern,expires_at FROM entitlements ORDER BY action"
    )
    assert {row["action"] for row in entitlements} == COMMUNICATION_SCOPE_ACTIONS
    assert len(entitlements) == 38
    assert all(row["resource_pattern"] == "*" for row in entitlements)
    assert all(row["expires_at"] is None for row in entitlements)
    row = communication_stack.store.fetch_one(
        "SELECT state,authority_expires_at FROM communication_scopes"
    )
    assert row["state"] == "committed"
    assert row["authority_expires_at"] is None


def test_begin_and_complete_are_idempotent(communication_stack) -> None:
    assert _begin(communication_stack) == _begin(communication_stack)
    assert communication_stack.client.create_calls == 1
    communication_stack.client.state = "issued"
    _status(communication_stack)
    assert _complete(communication_stack) == _complete(communication_stack)
    assert communication_stack.client.retrieve_calls == 1
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 38


@pytest.mark.parametrize("stale", ["credential", "policy"])
def test_final_commit_denies_stale_credential_or_policy(communication_stack, stale: str) -> None:
    _begin(communication_stack)
    communication_stack.client.state = "issued"
    _status(communication_stack)
    if stale == "credential":
        communication_stack.resolver.fresh_credential_epoch += 1
    else:
        communication_stack.resolver.policy_revision += 1
    with pytest.raises(AuthenticationError, match="identity recheck denied"):
        _complete(communication_stack)
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == 0


def test_wrong_status_digest_and_wrong_receipt_never_create_authority(communication_stack) -> None:
    _begin(communication_stack)
    communication_stack.client.state = "issued"
    communication_stack.client.wrong_status_digest = True
    with pytest.raises(AuthenticationError, match="approval service response denied"):
        _status(communication_stack)
    communication_stack.client.wrong_status_digest = False
    _status(communication_stack)
    communication_stack.client.wrong_receipt_transaction = True
    with pytest.raises(AuthenticationError, match="transaction binding mismatch"):
        _complete(communication_stack)
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0


def test_committed_scope_survives_issuance_credential_renewal(communication_stack) -> None:
    expected = _commit(communication_stack)
    rotated_key = P256KeyPair.generate()
    with communication_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='retired' WHERE credential_id=?",
            (communication_stack.actor.credential_id,),
        )
        connection.execute(
            "UPDATE harnesses SET credential_epoch=2 WHERE harness_id=?",
            (communication_stack.actor.harness_id,),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                "credential-rotated", communication_stack.actor.harness_id,
                rotated_key.thumbprint, rotated_key.public_pem, "active", 2,
                NOW - 1, NOW + 86_400,
            ),
        )
    rotated_actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=communication_stack.actor.domain_id,
        principal_id=communication_stack.actor.principal_id,
        harness_id=communication_stack.actor.harness_id,
        credential_id="credential-rotated",
        credential_epoch=2,
        binding_assurance=communication_stack.actor.binding_assurance,
    )
    assert _status(communication_stack, actor=rotated_actor) == expected


@pytest.mark.parametrize(
    "mutation",
    ["peer_revoked", "entitlement_revoked", "item_missing", "policy_changed"],
)
def test_committed_scope_denies_stale_or_incomplete_authority(
    communication_stack, mutation: str
) -> None:
    _commit(communication_stack)
    with communication_stack.store.transaction() as connection:
        if mutation == "peer_revoked":
            connection.execute(
                "UPDATE harnesses SET status='revoked' WHERE harness_id='owner-harness'"
            )
        elif mutation == "entitlement_revoked":
            connection.execute(
                """UPDATE entitlements SET revoked_at=? WHERE entitlement_id=(
                    SELECT entitlement_id FROM communication_scope_items LIMIT 1
                )""",
                (NOW,),
            )
        elif mutation == "item_missing":
            connection.execute(
                """DELETE FROM communication_scope_items WHERE item_id=(
                    SELECT item_id FROM communication_scope_items LIMIT 1
                )"""
            )
        else:
            connection.execute(
                "UPDATE domains SET policy_revision=2 WHERE domain_id=?",
                (communication_stack.actor.domain_id,),
            )
    with pytest.raises(AuthenticationError, match="current authority denied"):
        _status(communication_stack)
