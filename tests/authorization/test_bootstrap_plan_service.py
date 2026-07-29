from __future__ import annotations

import hashlib
import json
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from datetime import UTC, datetime
from types import SimpleNamespace

import pytest

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.bootstrap_plan import (
    BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
    BootstrapPlanBeginRequest,
    BootstrapPlanCompletionRequest,
    BootstrapPlanStatusRequest,
)
from agentnet.authorization.bootstrap_plan_service import (
    BootstrapPlanService,
    BootstrapPlanTerminalError,
)
from agentnet.authorization.c0_pilot_service import C0PilotService
from agentnet.authorization.policy import AuthorizationRequest, C0GuardedOperation, PolicyEngine
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.protocol.models import Classification, EventType
from agentnet.security.signatures import P256KeyPair, canonical_json


NOW = 1_800_000_000


class FakeApprovalClient:
    def __init__(self, key, approver, verifier):
        self.key = key
        self.approver = approver
        self.verifier = verifier
        self.canonical: bytes | None = None
        self.digest: str | None = None
        self.possession_hash: str | None = None
        self.fail = False
        self.create_failures = 0
        self.retrieve_failures = 0
        self.create_calls = 0
        self.retrieve_calls = 0
        self.status_expires_at = NOW + 300
        self.status_state = "issued"

    def create_request(self, **kwargs):
        self.create_calls += 1
        if self.create_failures:
            self.create_failures -= 1
            raise RuntimeError("approval create unavailable")
        self.canonical = kwargs["canonical_transaction"]
        self.digest = kwargs["transaction_digest"]
        self.possession_hash = kwargs["possession_hash"]
        return {
            "request_id": f"approval-request-bootstrap-{self.create_calls:04d}",
            "transaction_digest": self.digest,
            "expires_at": kwargs["request_expires_at"],
            "state": "pending",
        }

    def request_status(self, **kwargs):
        return {
            "request_id": kwargs["request_id"],
            "transaction_digest": kwargs["transaction_digest"],
            "state": self.status_state,
            "expires_at": self.status_expires_at,
        }

    def retrieve_receipt(self, **kwargs):
        self.retrieve_calls += 1
        assert self.possession_hash is not None
        assert hashlib.sha256(kwargs["possession_secret"].encode("ascii")).hexdigest() == self.possession_hash
        if self.fail:
            raise RuntimeError("approval unavailable")
        if self.retrieve_failures:
            self.retrieve_failures -= 1
            raise AuthenticationError("approval request denied")
        assert self.canonical is not None
        return create_independent_approval_receipt(
            self.key,
            approver=self.approver,
            verifier_id=self.verifier.verifier_id,
            approval_purpose=BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
            canonical_transaction=self.canonical,
            issued_at=NOW,
            expires_at=NOW + 300,
            authenticated_at=NOW,
        )


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


@pytest.fixture
def bootstrap_stack(store, actor):
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            ("owner-harness", actor.domain_id, actor.principal_id, None, "pi", "Owner laptop", "active", "os_bound", "[]", 1, NOW - 100),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)""",
            ("owner-credential", "owner-harness", "owner-key", "owner-public-key", "active", 1, NOW - 100, NOW + 3600),
        )

    def resolver(_connection, current_actor, _now):
        return {
            "domain": {"domain_id": current_actor.domain_id, "policy_revision": 1, "revocation_epoch": 1},
            "principal": {"principal_id": current_actor.principal_id},
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
                    "harness_id": current_actor.harness_id,
                    "credential_id": current_actor.credential_id,
                    "credential_epoch": current_actor.credential_epoch,
                    "binding_assurance": current_actor.binding_assurance,
                    "display_name": "Fresh laptop",
                    "kind": "pi",
                },
            },
            "enrollment_evidence": {"owner": _evidence("owner"), "fresh": _evidence("fresh")},
        }

    key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="security-owner",
        domain_id=actor.domain_id,
        signer_key_id=key.thumbprint,
        public_key_pem=key.public_pem,
        allowed_purposes=frozenset({BOOTSTRAP_PLAN_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {key.thumbprint: approver}, verifier_id="approval.corp.example"
    )
    client = FakeApprovalClient(key, approver, verifier)
    service = BootstrapPlanService(
        store,
        client,
        verifier,
        resolver=resolver,
        public_approval_url="https://approval.corp.example/approval",
        clock=lambda: NOW,
    )
    return SimpleNamespace(service=service, client=client, store=store, actor=actor)


def test_pending_status_body_is_exact_and_omits_ready_action(bootstrap_stack) -> None:
    request = BootstrapPlanBeginRequest.model_validate(
        {
            "schema": "agentnet.bootstrap-plan.begin.v1",
            "begin_idempotency_key": "bootstrap-begin-key-pending-status",
        }
    )
    bootstrap_stack.service.begin(actor=bootstrap_stack.actor, request=request)
    bootstrap_stack.client.status_state = "pending"

    assert bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.status.v1",
                "begin_idempotency_key": request.begin_idempotency_key,
            }
        ),
    ) == {
        "schema": "agentnet.bootstrap-plan.status-result.v1",
        "status": "approval_pending",
        "approval_url": "https://approval.corp.example/approval",
        "expires_at": NOW + 300,
    }


def test_begin_status_complete_commits_exact_pending_plan_atomically(bootstrap_stack) -> None:
    begin = bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": "bootstrap-begin-key-0001"}
        ),
    )
    assert begin["status"] == "approval_pending"

    status = bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": "bootstrap-begin-key-0001"}
        ),
    )
    assert status["status"] == "approval_ready"

    result = bootstrap_stack.service.complete(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanCompletionRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.complete.v2",
                "begin_idempotency_key": "bootstrap-begin-key-0001",
                "completion_idempotency_key": "bootstrap-complete-key-0001",
            }
        ),
    )
    assert result == {
        "schema": "agentnet.bootstrap-plan.complete-result.v1",
        "status": "prepared_unusable",
        "authority_granted": False,
        "communication_usable": False,
    }
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 10
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "pending"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM bootstrap_grant_plan_items")["n"] == 10
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_plan_guard_entitlements")["n"] == 6
    audits = [
        row for row in bootstrap_stack.store.fetch_all("SELECT record_json FROM audit_log")
        if "bootstrap_plan.committed" in row["record_json"]
    ]
    assert len(audits) == 1

    bootstrap_stack.client.fail = True
    duplicate = bootstrap_stack.service.complete(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanCompletionRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.complete.v2",
                "begin_idempotency_key": "bootstrap-begin-key-0001",
                "completion_idempotency_key": "bootstrap-complete-key-0001",
            }
        ),
    )
    assert duplicate == result
    assert bootstrap_stack.client.retrieve_calls == 1

    committed_status = bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": "bootstrap-begin-key-0001"}
        ),
    )
    assert committed_status == result


def test_final_commit_rejects_changed_resolver_evidence_and_rolls_back_receipt(bootstrap_stack) -> None:
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": "bootstrap-begin-key-0002"}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": "bootstrap-begin-key-0002"}
        ),
    )
    original = bootstrap_stack.service.resolver

    def changed(connection, actor, now):
        value = original(connection, actor, now)
        value["harnesses"]["owner"]["display_name"] = "Changed owner laptop"
        return value

    bootstrap_stack.service.resolver = changed
    with pytest.raises(AuthenticationError, match="identity recheck"):
        bootstrap_stack.service.complete(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": "bootstrap-begin-key-0002",
                    "completion_idempotency_key": "bootstrap-complete-key-0002",
                }
            ),
        )
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == 0


def test_begin_retries_exact_reserved_approval_create_after_response_loss(bootstrap_stack) -> None:
    begin_key = "a" * 32
    request = BootstrapPlanBeginRequest.model_validate(
        {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
    )
    bootstrap_stack.client.create_failures = 1
    with pytest.raises(RuntimeError, match="create unavailable"):
        bootstrap_stack.service.begin(actor=bootstrap_stack.actor, request=request)
    row = bootstrap_stack.store.fetch_one("SELECT * FROM bootstrap_grant_plans")
    assert row["state"] == "reserved"
    possession_secret, stored_result = bootstrap_stack.service._begin_storage(row)
    assert possession_secret is not None
    assert len(possession_secret) >= 43
    assert possession_secret != begin_key
    assert stored_result is None
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0

    result = bootstrap_stack.service.begin(actor=bootstrap_stack.actor, request=request)
    assert result["status"] == "approval_pending"
    assert bootstrap_stack.client.create_calls == 2
    assert bootstrap_stack.client.possession_hash == hashlib.sha256(
        possession_secret.encode("ascii")
    ).hexdigest()
    assert bootstrap_stack.client.possession_hash != hashlib.sha256(
        begin_key.encode("ascii")
    ).hexdigest()

    assert bootstrap_stack.service.begin(actor=bootstrap_stack.actor, request=request) == result
    assert bootstrap_stack.client.create_calls == 2
    row = bootstrap_stack.store.fetch_one("SELECT * FROM bootstrap_grant_plans")
    retried_secret, stored_result = bootstrap_stack.service._begin_storage(row)
    assert retried_secret == possession_secret
    assert stored_result == result


def test_legacy_begin_response_uses_idempotency_key_only_for_compatibility(bootstrap_stack) -> None:
    begin_key = "legacy-bootstrap-begin-key-0001"
    request = BootstrapPlanBeginRequest.model_validate(
        {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
    )
    result = bootstrap_stack.service.begin(actor=bootstrap_stack.actor, request=request)
    row = bootstrap_stack.store.fetch_one("SELECT * FROM bootstrap_grant_plans")
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE bootstrap_grant_plans SET begin_response_encrypted=? WHERE plan_id=?",
            (
                bootstrap_stack.store.cipher.encrypt_json(
                    result,
                    purpose=f"bootstrap-plan-begin:{row['plan_id']}",
                ),
                row["plan_id"],
            ),
        )
    bootstrap_stack.client.possession_hash = hashlib.sha256(begin_key.encode("ascii")).hexdigest()

    assert bootstrap_stack.service.begin(actor=bootstrap_stack.actor, request=request) == result
    assert bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )["status"] == "approval_ready"
    completed = bootstrap_stack.service.complete(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanCompletionRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.complete.v2",
                "begin_idempotency_key": begin_key,
                "completion_idempotency_key": "legacy-bootstrap-complete-key-0001",
            }
        ),
    )
    assert completed["status"] == "prepared_unusable"


def test_concurrent_exact_completion_converges_to_one_atomic_commit(bootstrap_stack) -> None:
    begin_key = "bootstrap-concurrent-begin-key"
    completion_key = "bootstrap-concurrent-completion-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    request = BootstrapPlanCompletionRequest.model_validate(
        {
            "schema": "agentnet.bootstrap-plan.complete.v2",
            "begin_idempotency_key": begin_key,
            "completion_idempotency_key": completion_key,
        }
    )
    audit_before = bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM audit_log")["n"]

    with ThreadPoolExecutor(max_workers=2) as executor:
        results = list(
            executor.map(
                lambda _index: bootstrap_stack.service.complete(
                    actor=bootstrap_stack.actor, request=request
                ),
                range(2),
            )
        )

    assert results[0] == results[1]
    assert results[0]["status"] == "prepared_unusable"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 10
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_plan_guards")["n"] == 1
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM audit_log")["n"] == audit_before + 1
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == 1


def test_concurrent_distinct_begin_first_reservation_wins(bootstrap_stack) -> None:
    def begin(key: str):
        try:
            return bootstrap_stack.service.begin(
                actor=bootstrap_stack.actor,
                request=BootstrapPlanBeginRequest.model_validate(
                    {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": key}
                ),
            )
        except ConflictError as exc:
            return exc

    with ThreadPoolExecutor(max_workers=2) as executor:
        results = list(
            executor.map(
                begin,
                ["bootstrap-racing-begin-key-0001", "bootstrap-racing-begin-key-0002"],
            )
        )

    assert sum(isinstance(value, ConflictError) for value in results) == 1
    assert sum(isinstance(value, dict) and value["status"] == "approval_pending" for value in results) == 1
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM bootstrap_grant_plans")["n"] == 1


def test_begin_expires_due_uncommitted_plan_before_new_reservation(bootstrap_stack) -> None:
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": "bootstrap-due-begin-key-0001"}
        ),
    )
    bootstrap_stack.service.clock = lambda: NOW + 301

    result = bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": "bootstrap-due-begin-key-0002"}
        ),
    )

    assert result["status"] == "approval_pending"
    rows = bootstrap_stack.store.fetch_all(
        "SELECT state,terminal_at FROM bootstrap_grant_plans ORDER BY created_at,plan_id"
    )
    assert [row["state"] for row in rows] == ["expired", "pending_approval"]
    assert rows[0]["terminal_at"] == NOW + 301
    with pytest.raises(BootstrapPlanTerminalError):
        bootstrap_stack.service.begin(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanBeginRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.begin.v1",
                    "begin_idempotency_key": "bootstrap-due-begin-key-0001",
                }
            ),
        )


def test_final_commit_rejects_competing_plan_inserted_after_receipt_retrieval(bootstrap_stack) -> None:
    begin_key = "bootstrap-final-race-begin-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    original = bootstrap_stack.service.resolver
    calls = 0

    def insert_competitor(connection, actor, now):
        nonlocal calls
        calls += 1
        resolved = original(connection, actor, now)
        if calls == 1:
            connection.execute(
                """INSERT INTO bootstrap_grant_plans(
                    plan_id,profile,profile_version,domain_id,principal_id,
                    owner_harness_id,fresh_harness_id,owner_credential_id,fresh_credential_id,
                    owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                    policy_revision,actor_binding_json,canonical_plan_preimage_json,
                    final_approval_transaction_json,plan_digest,transaction_digest,
                    begin_idempotency_key_sha256,state,created_at,approval_expires_at,
                    authority_expires_at,approval_create_idempotency_key,
                    approval_create_request_digest
                ) SELECT ?,profile,profile_version,domain_id,principal_id,
                    owner_harness_id,fresh_harness_id,owner_credential_id,fresh_credential_id,
                    owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                    policy_revision,actor_binding_json,canonical_plan_preimage_json,
                    final_approval_transaction_json,?,?,?, 'reserved',?,?,?, ?,?
                  FROM bootstrap_grant_plans WHERE begin_idempotency_key_sha256=?""",
                (
                    "bp1_competing_final_commit_plan",
                    "a" * 64,
                    "b" * 64,
                    "c" * 64,
                    now,
                    now + 300,
                    now + 3600,
                    "core:bootstrap-plan:create:competing",
                    "d" * 64,
                    hashlib.sha256(begin_key.encode()).hexdigest(),
                ),
            )
        return resolved

    bootstrap_stack.service.resolver = insert_competitor
    with pytest.raises(ConflictError, match="active bootstrap plan"):
        bootstrap_stack.service.complete(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": begin_key,
                    "completion_idempotency_key": "bootstrap-final-race-completion-key",
                }
            ),
        )
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM bootstrap_grant_plans")["n"] == 1


@pytest.mark.parametrize(
    ("target", "trigger_sql"),
    [
        (
            "receipt_replay",
            "CREATE TRIGGER bootstrap_fault BEFORE INSERT ON replay_nonces "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
        (
            "guard",
            "CREATE TRIGGER bootstrap_fault BEFORE INSERT ON c0_plan_guards "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
        (
            "entitlement",
            "CREATE TRIGGER bootstrap_fault BEFORE INSERT ON entitlements "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
        (
            "item",
            "CREATE TRIGGER bootstrap_fault BEFORE INSERT ON bootstrap_grant_plan_items "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
        (
            "link",
            "CREATE TRIGGER bootstrap_fault BEFORE INSERT ON c0_plan_guard_entitlements "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
        (
            "audit",
            "CREATE TRIGGER bootstrap_fault BEFORE INSERT ON audit_log "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
        (
            "result",
            "CREATE TRIGGER bootstrap_fault BEFORE UPDATE OF committed_result_encrypted "
            "ON bootstrap_grant_plans WHEN NEW.committed_result_encrypted IS NOT NULL "
            "BEGIN SELECT RAISE(ABORT,'fault'); END",
        ),
    ],
)
def test_final_commit_statement_faults_leave_zero_authority_and_no_replay(
    bootstrap_stack, target: str, trigger_sql: str
) -> None:
    begin_key = f"bootstrap-fault-{target}-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    replay_before = bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM replay_nonces"
    )["n"]
    audit_before = bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM audit_log")["n"]
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(trigger_sql)

    with pytest.raises(Exception):
        bootstrap_stack.service.complete(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": begin_key,
                    "completion_idempotency_key": f"completion-{begin_key}",
                }
            ),
        )

    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_plan_guards")["n"] == 0
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM bootstrap_grant_plan_items"
    )["n"] == 0
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM c0_plan_guard_entitlements"
    )["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == replay_before
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM audit_log")["n"] == audit_before
    row = bootstrap_stack.store.fetch_one(
        "SELECT state,approval_receipt_id,committed_result_encrypted FROM bootstrap_grant_plans"
    )
    assert row["state"] == "completion_reserved"
    assert row["approval_receipt_id"] is None
    assert row["committed_result_encrypted"] is None


def _commit_first_plan(bootstrap_stack, *, begin_key: str) -> None:
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.complete(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanCompletionRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.complete.v2",
                "begin_idempotency_key": begin_key,
                "completion_idempotency_key": f"completion-{begin_key}",
            }
        ),
    )


def test_new_plan_allows_only_inert_exact_revoke_rows_from_fully_revoked_predecessor(
    bootstrap_stack, monkeypatch: pytest.MonkeyPatch
) -> None:
    _commit_first_plan(bootstrap_stack, begin_key="bootstrap-predecessor-key-0001")
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            """UPDATE entitlements SET revoked_at=? WHERE entitlement_id IN (
                   SELECT entitlement_id FROM bootstrap_grant_plan_items
                   WHERE item_kind='communication'
               )""",
            (NOW,),
        )

    client = bootstrap_stack.client

    def create_second(**kwargs):
        client.create_calls += 1
        client.canonical = kwargs["canonical_transaction"]
        client.digest = kwargs["transaction_digest"]
        return {
            "request_id": "approval-request-bootstrap-0002",
            "transaction_digest": client.digest,
            "expires_at": kwargs["request_expires_at"],
            "state": "pending",
        }

    monkeypatch.setattr(client, "create_request", create_second)
    result = bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.begin.v1",
                "begin_idempotency_key": "bootstrap-successor-key-0002",
            }
        ),
    )
    assert result["status"] == "approval_pending"
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM bootstrap_grant_plans"
    )["n"] == 2


def test_new_plan_rejects_predecessor_revoke_row_outside_recorded_ceiling(
    bootstrap_stack,
) -> None:
    _commit_first_plan(bootstrap_stack, begin_key="bootstrap-predecessor-key-0003")
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            """UPDATE entitlements SET revoked_at=? WHERE entitlement_id IN (
                   SELECT entitlement_id FROM bootstrap_grant_plan_items
                   WHERE item_kind='communication'
               )""",
            (NOW,),
        )
        connection.execute(
            """UPDATE entitlements SET expires_at=expires_at+1 WHERE entitlement_id IN (
                   SELECT entitlement_id FROM bootstrap_grant_plan_items
                   WHERE item_kind='exact_revoke'
               )"""
        )
    with pytest.raises(AuthorizationError, match="identity-only"):
        bootstrap_stack.service.begin(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanBeginRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.begin.v1",
                    "begin_idempotency_key": "bootstrap-successor-key-0004",
                }
            ),
        )


@pytest.mark.parametrize("terminal_state", ["rejected", "canceled", "expired", "invalidated"])
def test_complete_rejects_terminal_plan_without_approval_retrieval(
    bootstrap_stack, terminal_state: str
) -> None:
    begin_key = f"bootstrap-terminal-{terminal_state}-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE bootstrap_grant_plans SET state=?,terminal_at=?",
            (terminal_state, NOW),
        )
    status = bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    assert status == {
        "schema": "agentnet.bootstrap-plan.status-result.v1",
        "status": terminal_state,
    }
    with pytest.raises(BootstrapPlanTerminalError):
        bootstrap_stack.service.complete(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": begin_key,
                    "completion_idempotency_key": f"completion-terminal-{terminal_state}-key",
                }
            ),
        )
    assert bootstrap_stack.client.retrieve_calls == 0


def test_status_expires_due_local_plan_without_approval_call(bootstrap_stack) -> None:
    begin_key = "bootstrap-status-local-expiry-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.clock = lambda: NOW + 301
    bootstrap_stack.client.request_status = lambda **_kwargs: (_ for _ in ()).throw(
        AssertionError("Approval status must not be called for a locally due plan")
    )

    assert bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    ) == {"schema": "agentnet.bootstrap-plan.status-result.v1", "status": "expired"}
    row = bootstrap_stack.store.fetch_one("SELECT state,terminal_at FROM bootstrap_grant_plans")
    assert row["state"] == "expired"
    assert row["terminal_at"] == NOW + 301


def test_complete_expires_due_local_plan_without_receipt_retrieval(bootstrap_stack) -> None:
    begin_key = "bootstrap-complete-local-expiry-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.clock = lambda: NOW + 301

    with pytest.raises(BootstrapPlanTerminalError):
        bootstrap_stack.service.complete(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": begin_key,
                    "completion_idempotency_key": "bootstrap-complete-local-expiry-completion-key",
                }
            ),
        )
    assert bootstrap_stack.client.retrieve_calls == 0
    assert bootstrap_stack.store.fetch_one("SELECT state FROM bootstrap_grant_plans")["state"] == "expired"


def test_complete_rechecks_expiry_inside_final_commit_after_receipt_retrieval(
    bootstrap_stack,
) -> None:
    begin_key = "bootstrap-final-commit-expiry-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.begin.v1",
                "begin_idempotency_key": begin_key,
            }
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.status.v1",
                "begin_idempotency_key": begin_key,
            }
        ),
    )
    times = iter((NOW + 299, NOW + 301))
    bootstrap_stack.service.clock = lambda: next(times)

    with pytest.raises(BootstrapPlanTerminalError, match="terminal"):
        bootstrap_stack.service.complete(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanCompletionRequest.model_validate(
                {
                    "schema": "agentnet.bootstrap-plan.complete.v2",
                    "begin_idempotency_key": begin_key,
                    "completion_idempotency_key": "bootstrap-final-commit-expiry-completion-key",
                }
            ),
        )

    assert bootstrap_stack.client.retrieve_calls == 1
    row = bootstrap_stack.store.fetch_one(
        "SELECT state,terminal_at FROM bootstrap_grant_plans"
    )
    assert row["state"] == "expired"
    assert row["terminal_at"] == NOW + 301
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_plan_guards")["n"] == 0


def test_status_rejects_approval_deadline_mismatch(bootstrap_stack) -> None:
    begin_key = "bootstrap-status-expiry-mismatch-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.client.status_expires_at += 1

    with pytest.raises(AuthenticationError, match="approval service response denied"):
        bootstrap_stack.service.status(
            actor=bootstrap_stack.actor,
            request=BootstrapPlanStatusRequest.model_validate(
                {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
            ),
        )


def test_completion_reservation_survives_retrieval_failure_and_conflicting_key(bootstrap_stack) -> None:
    begin_key = "bootstrap-begin-key-0004"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.client.retrieve_failures = 1
    first = BootstrapPlanCompletionRequest.model_validate(
        {
            "schema": "agentnet.bootstrap-plan.complete.v2",
            "begin_idempotency_key": begin_key,
            "completion_idempotency_key": "bootstrap-complete-key-0004",
        }
    )
    with pytest.raises(AuthenticationError, match="approval request denied"):
        bootstrap_stack.service.complete(actor=bootstrap_stack.actor, request=first)
    assert bootstrap_stack.store.fetch_one("SELECT state FROM bootstrap_grant_plans")["state"] == "completion_reserved"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0

    conflicting = first.model_copy(
        update={
            "completion_idempotency_key": "bootstrap-complete-key-other",
        }
    )
    with pytest.raises(ConflictError, match="completion conflict"):
        bootstrap_stack.service.complete(actor=bootstrap_stack.actor, request=conflicting)
    assert bootstrap_stack.client.retrieve_calls == 1

    exact_retry = first.model_copy()
    result = bootstrap_stack.service.complete(
        actor=bootstrap_stack.actor, request=exact_retry
    )
    assert result["status"] == "prepared_unusable"
    assert bootstrap_stack.client.retrieve_calls == 2


def _commit_c0_plan(bootstrap_stack) -> tuple[C0PilotService, VerifiedActor]:
    begin_key = "bootstrap-c0-service-begin-key"
    bootstrap_stack.service.begin(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanBeginRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.begin.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.status(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanStatusRequest.model_validate(
            {"schema": "agentnet.bootstrap-plan.status.v1", "begin_idempotency_key": begin_key}
        ),
    )
    bootstrap_stack.service.complete(
        actor=bootstrap_stack.actor,
        request=BootstrapPlanCompletionRequest.model_validate(
            {
                "schema": "agentnet.bootstrap-plan.complete.v2",
                "begin_idempotency_key": begin_key,
                "completion_idempotency_key": "bootstrap-c0-service-complete-key",
            }
        ),
    )
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id IN (?,?)",
            (NOW + 3600, "credential-a", "owner-credential"),
        )
    owner = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=bootstrap_stack.actor.domain_id,
        principal_id=bootstrap_stack.actor.principal_id,
        harness_id="owner-harness",
        credential_id="owner-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    return (
        C0PilotService(
            bootstrap_stack.store,
            PolicyEngine(bootstrap_stack.store),
            MailboxService(bootstrap_stack.store),
            clock=lambda: NOW + 1,
        ),
        owner,
    )


def test_active_bootstrap_entitlements_never_escape_generic_policy(bootstrap_stack) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute("UPDATE c0_plan_guards SET state='active'")
    policy = service.policy
    generic_send = policy.decide(
        AuthorizationRequest(
            actor=bootstrap_stack.actor,
            action="message.send",
            resource="direct",
            policy_revision=1,
        ),
        when=datetime.fromtimestamp(NOW + 1, UTC),
    )
    revoke_item = bootstrap_stack.store.fetch_one(
        "SELECT resource_pattern FROM bootstrap_grant_plan_items WHERE item_kind='exact_revoke' ORDER BY item_ordinal LIMIT 1"
    )
    generic_revoke = policy.decide(
        AuthorizationRequest(
            actor=bootstrap_stack.actor,
            action="authorization.entitlement.revoke",
            resource=str(revoke_item["resource_pattern"]),
            policy_revision=1,
        ),
        when=datetime.fromtimestamp(NOW + 1, UTC),
    )
    assert generic_send.allowed is False
    assert generic_send.reason == "no_positive_human_entitlement"
    assert generic_revoke.allowed is False
    assert generic_revoke.reason == "no_positive_human_entitlement"


def test_c0_round_trip_is_idempotent_and_revokes_exact_five(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)

    assert service.status(actor=bootstrap_stack.actor)["status"] == "prepared_unusable"
    assert service.start(actor=bootstrap_stack.actor)["status"] == "waiting_owner"
    assert service.start(actor=bootstrap_stack.actor)["status"] == "waiting_owner"
    assert service.respond(actor=owner)["status"] == "waiting_fresh"
    assert service.respond(actor=owner)["status"] == "waiting_fresh"
    assert service.start(actor=bootstrap_stack.actor)["status"] == "waiting_fresh"
    assert service.complete(actor=bootstrap_stack.actor)["status"] == "COMPLETED_C0_ROUND_TRIP"
    assert service.complete(actor=bootstrap_stack.actor)["status"] == "COMPLETED_C0_ROUND_TRIP"
    assert service.start(actor=bootstrap_stack.actor)["status"] == "COMPLETED_C0_ROUND_TRIP"
    assert service.respond(actor=owner)["status"] == "COMPLETED_C0_ROUND_TRIP"

    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 2
    facts = bootstrap_stack.store.fetch_all(
        "SELECT * FROM c0_pilot_facts ORDER BY fact_kind"
    )
    assert len(facts) == 7
    for fact in facts:
        evidence = json.loads(fact["evidence_json"])
        assert evidence == {
            "schema": "agentnet.c0-pilot.fact-evidence.v1",
            "fact_kind": fact["fact_kind"],
            "issuer_kind": fact["issuer_kind"],
            "issuer_harness_id": fact["issuer_harness_id"],
            "event_id": fact["event_id"],
            "receipt_id": fact["receipt_id"],
            "envelope_digest": fact["envelope_digest"],
            "storage_fact": fact["storage_fact"],
        }
        if fact["fact_kind"] in {
            "request_durable_custody", "reply_durable_custody"
        }:
            assert fact["issuer_kind"] == "accepting_core"
            assert fact["issuer_harness_id"] is None
            assert fact["receipt_id"] is not None
            assert fact["storage_fact"] == "accepted_local"
        else:
            assert fact["issuer_kind"] == "harness"
            assert fact["issuer_harness_id"] in {
                owner.harness_id, bootstrap_stack.actor.harness_id
            }
            assert fact["storage_fact"] is None
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements e JOIN bootstrap_grant_plan_items i ON i.entitlement_id=e.entitlement_id WHERE i.item_kind='communication' AND e.revoked_at IS NOT NULL"
    )["n"] == 5
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements e JOIN bootstrap_grant_plan_items i ON i.entitlement_id=e.entitlement_id WHERE i.item_kind='exact_revoke' AND e.revoked_at IS NULL"
    )["n"] == 5
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "revoked"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "communication_revoked"
    for action, resource in (
        ("message.send", "direct"),
        ("mailbox.read", bootstrap_stack.actor.harness_id),
        ("mailbox.acknowledge", bootstrap_stack.actor.harness_id),
    ):
        decision = service.policy.decide(
            AuthorizationRequest(
                actor=bootstrap_stack.actor,
                action=action,
                resource=resource,
                policy_revision=1,
            ),
            when=datetime.fromtimestamp(NOW + 1, UTC),
        )
        assert decision.allowed is False


def test_c0_restart_and_response_loss_converge_across_all_three_phases(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    assert service.start(actor=bootstrap_stack.actor)["status"] == "waiting_owner"

    restarted_owner = C0PilotService(
        bootstrap_stack.store,
        PolicyEngine(bootstrap_stack.store),
        MailboxService(bootstrap_stack.store),
        clock=lambda: NOW + 2,
    )
    assert restarted_owner.start(actor=bootstrap_stack.actor)["status"] == "waiting_owner"
    assert restarted_owner.respond(actor=owner)["status"] == "waiting_fresh"

    restarted_fresh = C0PilotService(
        bootstrap_stack.store,
        PolicyEngine(bootstrap_stack.store),
        MailboxService(bootstrap_stack.store),
        clock=lambda: NOW + 3,
    )
    assert restarted_fresh.respond(actor=owner)["status"] == "waiting_fresh"
    assert restarted_fresh.complete(actor=bootstrap_stack.actor)["status"] == "COMPLETED_C0_ROUND_TRIP"

    final_reader = C0PilotService(
        bootstrap_stack.store,
        PolicyEngine(bootstrap_stack.store),
        MailboxService(bootstrap_stack.store),
        clock=lambda: NOW + 4,
    )
    assert final_reader.status(actor=owner)["status"] == "COMPLETED_C0_ROUND_TRIP"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 2
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 7
    assert bootstrap_stack.store.fetch_one(
        """SELECT COUNT(*) AS n FROM entitlements e
             JOIN bootstrap_grant_plan_items i ON i.entitlement_id=e.entitlement_id
            WHERE i.item_kind='communication' AND e.revoked_at IS NOT NULL"""
    )["n"] == 5


@pytest.mark.parametrize(
    ("field", "value"),
    (
        ("peer_harness_id", "wrong-peer"),
        ("classification", Classification.C1_INTERNAL),
        ("payload_digest", "0" * 64),
        ("event_id", "wrong-event"),
    ),
)
def test_c0_request_pep_rejects_tampered_exact_context_and_rolls_back(
    bootstrap_stack, monkeypatch: pytest.MonkeyPatch, field: str, value: object
) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    original = service.policy._require_c0_operation_in_transaction

    def tampered(connection, *, actor, action, resource, context, when):
        assert isinstance(context, C0GuardedOperation)
        if context.operation_scope == "fresh_to_owner_send":
            context = replace(context, **{field: value})
        return original(
            connection,
            actor=actor,
            action=action,
            resource=resource,
            context=context,
            when=when,
        )

    monkeypatch.setattr(service.policy, "_require_c0_operation_in_transaction", tampered)
    with pytest.raises(AuthorizationError):
        service.start(actor=bootstrap_stack.actor)

    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "pending"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_attempts")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 0


@pytest.mark.parametrize(
    ("field", "value"),
    (
        ("peer_harness_id", "wrong-peer"),
        ("classification", Classification.C1_INTERNAL),
        ("payload_digest", "0" * 64),
        ("event_id", "wrong-event"),
        ("causal_parent_event_id", "wrong-parent"),
    ),
)
def test_c0_reply_pep_rejects_tampered_exact_context_and_rolls_back(
    bootstrap_stack, monkeypatch: pytest.MonkeyPatch, field: str, value: object
) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    original = service.policy._require_c0_operation_in_transaction

    def tampered(connection, *, actor, action, resource, context, when):
        assert isinstance(context, C0GuardedOperation)
        if context.operation_scope == "owner_to_fresh_send":
            context = replace(context, **{field: value})
        return original(
            connection,
            actor=actor,
            action=action,
            resource=resource,
            context=context,
            when=when,
        )

    monkeypatch.setattr(service.policy, "_require_c0_operation_in_transaction", tampered)
    with pytest.raises(AuthorizationError):
        service.respond(actor=owner)

    request_event_id = bootstrap_stack.store.fetch_one(
        "SELECT event_id FROM c0_pilot_facts WHERE fact_kind='request_durable_custody'"
    )["event_id"]
    assert bootstrap_stack.store.fetch_one(
        "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
        (request_event_id, owner.harness_id),
    )["current_fact"] == "accepted_local"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 1
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 1
    guard = bootstrap_stack.store.fetch_one(
        "SELECT request_remaining_uses,reply_remaining_uses FROM c0_plan_guards"
    )
    assert (guard["request_remaining_uses"], guard["reply_remaining_uses"]) == (0, 1)


@pytest.mark.parametrize(
    ("sql", "params"),
    (
        ("UPDATE domains SET policy_revision=2 WHERE domain_id=?", ("domain-a",)),
        ("UPDATE domains SET revocation_epoch=2 WHERE domain_id=?", ("domain-a",)),
        ("UPDATE credentials SET status='revoked' WHERE credential_id=?", ("owner-credential",)),
        ("UPDATE harnesses SET credential_epoch=2 WHERE harness_id=?", ("harness-a",)),
    ),
)
def test_c0_stale_policy_revocation_or_credential_binding_fails_closed(
    bootstrap_stack, sql: str, params: tuple[str, ...]
) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(sql, params)

    assert service.status(actor=bootstrap_stack.actor)["status"] == "invalidated"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "invalidated"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "failed"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 1


def test_c0_added_third_active_harness_invalidates_exact_identity_set(bootstrap_stack) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                   binding_assurance,capabilities_json,credential_epoch,created_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "third-harness", "domain-a", "human-a", None, "pi", "Third laptop",
                "active", "os_bound", "[]", 1, NOW,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                "third-credential", "third-harness", "third-key", "third-public-key",
                "active", 1, NOW, NOW + 3600,
            ),
        )

    assert service.status(actor=bootstrap_stack.actor)["status"] == "invalidated"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "invalidated"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "failed"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 1

    with bootstrap_stack.store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id='third-harness'")
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id='third-credential'"
        )
    assert service.status(actor=bootstrap_stack.actor)["status"] == "invalidated"


def test_c0_added_active_same_epoch_credential_invalidates_exact_identity_set(
    bootstrap_stack,
) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,?,?,?,?,?)""",
            (
                "alternate-fresh-credential", "harness-a", "alternate-key",
                "alternate-public-key", "active", 1, NOW, NOW + 3600,
            ),
        )

    alternate_actor = bootstrap_stack.actor.model_copy(
        update={"credential_id": "alternate-fresh-credential"}
    )
    assert service.status(actor=alternate_actor)["status"] == "invalidated"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "invalidated"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 0

    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id='alternate-fresh-credential'"
        )
    assert service.start(actor=bootstrap_stack.actor)["status"] == "invalidated"


def test_c0_wrong_actor_credential_cannot_invalidate_guard(bootstrap_stack) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    wrong_actor = bootstrap_stack.actor.model_copy(
        update={"credential_id": "caller-selected-credential"}
    )

    with pytest.raises(AuthorizationError, match="actor credential binding"):
        service.status(actor=wrong_actor)
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "pending"


def test_c0_reads_and_acknowledges_only_fact_linked_mailbox_events(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    boundary = datetime.fromtimestamp(NOW + 120, UTC)

    unrelated_owner_event = new_event(
        event_id="unrelated-owner-mailbox-event",
        domain_id=bootstrap_stack.actor.domain_id,
        actor=bootstrap_stack.actor,
        event_type=EventType.MESSAGE,
        classification=Classification.C0_PUBLIC,
        payload={"schema": "unrelated.test.v1", "value": "not-pilot"},
        idempotency_key="unrelated-owner-mailbox-key",
        recipients=(owner.harness_id,),
        delivery_expires_at=boundary,
        retention_delete_at=boundary,
        policy_revision=1,
    )
    with bootstrap_stack.store.transaction() as connection:
        service.mailbox._accept_in_transaction(connection, unrelated_owner_event, now=NOW)
    service.respond(actor=owner)

    unrelated_fresh_event = new_event(
        event_id="unrelated-fresh-mailbox-event",
        domain_id=owner.domain_id,
        actor=owner,
        event_type=EventType.MESSAGE,
        classification=Classification.C0_PUBLIC,
        payload={"schema": "unrelated.test.v1", "value": "not-pilot"},
        idempotency_key="unrelated-fresh-mailbox-key",
        recipients=(bootstrap_stack.actor.harness_id,),
        delivery_expires_at=boundary,
        retention_delete_at=boundary,
        policy_revision=1,
    )
    with bootstrap_stack.store.transaction() as connection:
        service.mailbox._accept_in_transaction(connection, unrelated_fresh_event, now=NOW)
    service.complete(actor=bootstrap_stack.actor)

    for event_id, recipient_id in (
        (unrelated_owner_event.event_id, owner.harness_id),
        (unrelated_fresh_event.event_id, bootstrap_stack.actor.harness_id),
    ):
        recipient = bootstrap_stack.store.fetch_one(
            "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
            (event_id, recipient_id),
        )
        assert recipient["current_fact"] == "accepted_local"
        assert bootstrap_stack.store.fetch_one(
            """SELECT COUNT(*) AS n FROM receipts
                WHERE event_id=? AND recipient_id=? AND fact='recipient_committed'""",
            (event_id, recipient_id),
        )["n"] == 0


def test_c0_expiry_invalidates_active_attempt_without_revoking_or_reactivating(bootstrap_stack) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    expires_at = int(
        bootstrap_stack.store.fetch_one("SELECT expires_at FROM c0_plan_guards")["expires_at"]
    )
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id IN (?,?)",
            (expires_at + 100, "credential-a", "owner-credential"),
        )
    expired_reader = C0PilotService(
        bootstrap_stack.store,
        PolicyEngine(bootstrap_stack.store),
        MailboxService(bootstrap_stack.store),
        clock=lambda: expires_at + 1,
    )

    assert expired_reader.status(actor=bootstrap_stack.actor)["status"] == "expired"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "expired"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "expired"
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements WHERE revoked_at IS NOT NULL"
    )["n"] == 0
    assert expired_reader.start(actor=bootstrap_stack.actor)["status"] == "expired"


def test_c0_committed_terminal_result_survives_guard_ttl_without_restoring_authority(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    service.complete(actor=bootstrap_stack.actor)
    expires_at = int(
        bootstrap_stack.store.fetch_one("SELECT expires_at FROM c0_plan_guards")["expires_at"]
    )
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id IN (?,?)",
            (expires_at + 100, "credential-a", "owner-credential"),
        )
    later = C0PilotService(
        bootstrap_stack.store,
        PolicyEngine(bootstrap_stack.store),
        MailboxService(bootstrap_stack.store),
        clock=lambda: expires_at + 1,
    )

    assert later.status(actor=owner)["status"] == "COMPLETED_C0_ROUND_TRIP"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "revoked"
    assert bootstrap_stack.store.fetch_one(
        """SELECT COUNT(*) AS n FROM entitlements e
             JOIN bootstrap_grant_plan_items i ON i.entitlement_id=e.entitlement_id
            WHERE i.item_kind='communication' AND e.revoked_at IS NOT NULL"""
    )["n"] == 5


def test_c0_stored_success_cannot_bypass_incomplete_terminal_cleanup(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    service.complete(actor=bootstrap_stack.actor)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            """UPDATE entitlements SET revoked_at=NULL WHERE entitlement_id=(
                   SELECT entitlement_id FROM bootstrap_grant_plan_items
                    WHERE item_kind='communication' ORDER BY item_ordinal LIMIT 1
               )"""
        )

    with pytest.raises(ConflictError, match="cleanup"):
        service.status(actor=bootstrap_stack.actor)


@pytest.mark.parametrize("direction", ("request", "reply"))
@pytest.mark.parametrize(
    "mutation",
    ("actor", "recipient", "payload_digest", "envelope_digest", "mailbox_presence"),
)
def test_c0_terminal_replay_revalidates_authoritative_mailbox_event(
    bootstrap_stack, direction: str, mutation: str
) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    service.complete(actor=bootstrap_stack.actor)
    event_id = bootstrap_stack.store.fetch_one(
        "SELECT event_id FROM c0_pilot_facts WHERE fact_kind=?",
        (f"{direction}_durable_custody",),
    )["event_id"]
    other_event_id = bootstrap_stack.store.fetch_one(
        "SELECT event_id FROM c0_pilot_facts WHERE fact_kind=?",
        ("reply_durable_custody" if direction == "request" else "request_durable_custody",),
    )["event_id"]
    expected_recipient = owner.harness_id if direction == "request" else bootstrap_stack.actor.harness_id
    wrong_recipient = bootstrap_stack.actor.harness_id if direction == "request" else owner.harness_id

    with bootstrap_stack.store.transaction() as connection:
        if mutation in {"actor", "payload_digest"}:
            metadata = json.loads(
                connection.execute(
                    "SELECT envelope_json FROM events WHERE event_id=?", (event_id,)
                ).fetchone()["envelope_json"]
            )
            if mutation == "actor":
                other_metadata = json.loads(
                    connection.execute(
                        "SELECT envelope_json FROM events WHERE event_id=?", (other_event_id,)
                    ).fetchone()["envelope_json"]
                )
                metadata["actor"] = other_metadata["actor"]
            else:
                metadata["payload_digest"] = "0" * 64
            connection.execute(
                "UPDATE events SET envelope_json=? WHERE event_id=?",
                (canonical_json(metadata).decode("utf-8"), event_id),
            )
        elif mutation == "recipient":
            connection.execute(
                "DELETE FROM recipient_address_snapshots WHERE event_id=? AND recipient_id=?",
                (event_id, expected_recipient),
            )
            connection.execute(
                "UPDATE recipients SET recipient_id=? WHERE event_id=? AND recipient_id=?",
                (wrong_recipient, event_id, expected_recipient),
            )
        elif mutation == "envelope_digest":
            connection.execute(
                "UPDATE events SET envelope_digest=? WHERE event_id=?", ("0" * 64, event_id)
            )
        else:
            connection.execute(
                "DELETE FROM recipient_address_snapshots WHERE event_id=? AND recipient_id=?",
                (event_id, expected_recipient),
            )
            connection.execute(
                "DELETE FROM recipients WHERE event_id=? AND recipient_id=?",
                (event_id, expected_recipient),
            )

    with pytest.raises((AuthorizationError, ConflictError)):
        service.status(actor=bootstrap_stack.actor)


@pytest.mark.parametrize(
    "fact_kind",
    (
        "request_durable_custody",
        "reply_durable_custody",
        "request_recipient_acknowledged",
        "reply_final_acknowledged",
    ),
)
def test_c0_terminal_replay_revalidates_receipt_owner(
    bootstrap_stack, fact_kind: str
) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    service.complete(actor=bootstrap_stack.actor)
    receipt_id = bootstrap_stack.store.fetch_one(
        "SELECT receipt_id FROM c0_pilot_facts WHERE fact_kind=?", (fact_kind,)
    )["receipt_id"]

    with bootstrap_stack.store.transaction() as connection:
        if fact_kind.endswith("durable_custody"):
            receipt_owner = json.loads(
                connection.execute(
                    "SELECT owner_actor_json FROM receipts WHERE receipt_id=?", (receipt_id,)
                ).fetchone()["owner_actor_json"]
            )
            receipt_owner["domain_id"] = "tampered-domain"
        else:
            receipt_owner = (
                bootstrap_stack.actor.audit_view()
                if fact_kind.startswith("request_")
                else owner.audit_view()
            )
        connection.execute(
            "UPDATE receipts SET owner_actor_json=? WHERE receipt_id=?",
            (canonical_json(receipt_owner).decode("utf-8"), receipt_id),
        )

    with pytest.raises(AuthorizationError, match="receipt"):
        service.status(actor=bootstrap_stack.actor)


def test_c0_request_audit_outage_rolls_back_entire_start_transaction(
    bootstrap_stack, monkeypatch: pytest.MonkeyPatch
) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)
    original = bootstrap_stack.store.append_audit

    def fail_c0_audit(connection, record):
        if record.get("action") == "c0_pilot.request_accepted":
            raise RuntimeError("synthetic audit outage")
        return original(connection, record)

    monkeypatch.setattr(bootstrap_stack.store, "append_audit", fail_c0_audit)
    with pytest.raises(RuntimeError, match="synthetic audit outage"):
        service.start(actor=bootstrap_stack.actor)

    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "pending"
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_attempts")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 0


def test_c0_completion_audit_outage_rolls_back_final_ack_and_cleanup(
    bootstrap_stack, monkeypatch: pytest.MonkeyPatch
) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    original = bootstrap_stack.store.append_audit

    def fail_c0_audit(connection, record):
        if record.get("action") == "c0_pilot.completed":
            raise RuntimeError("synthetic audit outage")
        return original(connection, record)

    monkeypatch.setattr(bootstrap_stack.store, "append_audit", fail_c0_audit)
    with pytest.raises(RuntimeError, match="synthetic audit outage"):
        service.complete(actor=bootstrap_stack.actor)

    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements WHERE revoked_at IS NOT NULL"
    )["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 5
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "active"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "active"


def test_c0_phase_failure_rolls_back_guard_event_fact_and_use(bootstrap_stack) -> None:
    service, _owner = _commit_c0_plan(bootstrap_stack)

    def fail(phase: str) -> None:
        if phase == "after_request_accept":
            raise RuntimeError("synthetic crash")

    service.phase_hook = fail
    with pytest.raises(RuntimeError, match="synthetic crash"):
        service.start(actor=bootstrap_stack.actor)

    guard = bootstrap_stack.store.fetch_one(
        "SELECT state,request_remaining_uses FROM c0_plan_guards"
    )
    assert (guard["state"], guard["request_remaining_uses"]) == ("pending", 1)
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_attempts")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 0


def test_c0_cleanup_crash_rolls_back_final_ack_facts_and_all_revocations(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)

    def fail(phase: str) -> None:
        if phase == "after_cleanup_revoke_3":
            raise RuntimeError("synthetic cleanup crash")

    service.phase_hook = fail
    with pytest.raises(RuntimeError, match="synthetic cleanup crash"):
        service.complete(actor=bootstrap_stack.actor)

    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements e JOIN bootstrap_grant_plan_items i ON i.entitlement_id=e.entitlement_id WHERE i.item_kind='communication' AND e.revoked_at IS NOT NULL"
    )["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 5
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "active"
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_plan_guards")["state"] == "active"

    service.phase_hook = None
    assert service.complete(actor=bootstrap_stack.actor)["status"] == "COMPLETED_C0_ROUND_TRIP"


def test_c0_tampered_fact_evidence_prevents_success_and_cleanup(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "UPDATE c0_pilot_facts SET evidence_json='{}' WHERE fact_kind='reply_sent'"
        )

    with pytest.raises(AuthorizationError, match="fact evidence"):
        service.complete(actor=bootstrap_stack.actor)

    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements WHERE revoked_at IS NOT NULL"
    )["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "active"


def test_c0_missing_machine_fact_prevents_success_and_rolls_back_final_phase(bootstrap_stack) -> None:
    service, owner = _commit_c0_plan(bootstrap_stack)
    service.start(actor=bootstrap_stack.actor)
    service.respond(actor=owner)
    with bootstrap_stack.store.transaction() as connection:
        connection.execute(
            "DELETE FROM c0_pilot_facts WHERE fact_kind='reply_sent'"
        )

    with pytest.raises(AuthorizationError, match="incomplete"):
        service.complete(actor=bootstrap_stack.actor)

    assert bootstrap_stack.store.fetch_one("SELECT COUNT(*) AS n FROM c0_pilot_facts")["n"] == 4
    assert bootstrap_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM entitlements WHERE revoked_at IS NOT NULL"
    )["n"] == 0
    assert bootstrap_stack.store.fetch_one("SELECT state FROM c0_pilot_attempts")["state"] == "active"
