from __future__ import annotations

import hashlib
from concurrent.futures import ThreadPoolExecutor
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
    digest_canonical,
)
from agentnet.authorization.communication_scope_service import (
    CommunicationScopeService,
    CommunicationScopeTerminalError,
)
from agentnet.discovery.directory import DirectoryRecord
from agentnet.errors import AuthenticationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.endpoint_lifecycle import EndpointLifecycleService
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
        self.fail_create = False
        self.retrieve_calls = 0

    def create_request(self, **kwargs):
        self.create_calls += 1
        if self.fail_create:
            raise RuntimeError("approval unavailable")
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
    endpoint_lifecycle = EndpointLifecycleService(store, clock=lambda: NOW)
    actor_kind = str(
        store.fetch_one(
            "SELECT kind FROM harnesses WHERE harness_id=?",
            (actor.harness_id,),
        )["kind"]
    )
    endpoint_lifecycle.register_existing(
        actor=actor,
        harness_kind=actor_kind,
        profile_key="member-profile",
    )
    owner_actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=actor.domain_id,
        principal_id=actor.principal_id,
        harness_id="owner-harness",
        credential_id="owner-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    endpoint_lifecycle.register_existing(
        actor=owner_actor,
        harness_kind="pi",
        profile_key="owner-profile",
    )
    resolver = MutableResolver(actor)
    key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="approval-owner",
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
        approver_principal_id=approver.principal_id,
        endpoint_lifecycle=endpoint_lifecycle,
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


def _complete(stack, *, actor=None):
    return stack.service.complete(
        actor=actor or stack.actor,
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
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM endpoint_lifecycle")["n"] == 2
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM directory_records")["n"] == 2


def test_approved_completion_commits_exact_persistent_scope(communication_stack) -> None:
    result = _commit(communication_stack)
    assert result["schema"] == "agentnet.communication-scope.complete-result.v2"
    assert result["status"] == "communication_active"
    assert result["authority_granted"] is True
    assert result["communication_usable"] is True
    assert result["authority_expires_at"] is None
    assert result["artifacts_enabled"] is False
    assert result["business_effects_enabled"] is False
    assert result["federation_enabled"] is False
    assert result["public_a2a_enabled"] is False
    assert result["collaboration_scope_id"]
    collaboration = communication_stack.store.fetch_one(
        """SELECT scope_id,source_communication_scope_id,state,owner_harness_id
             FROM collaboration_scopes"""
    )
    assert dict(collaboration) == {
        "scope_id": result["collaboration_scope_id"],
        "source_communication_scope_id": result["collaboration_scope_id"],
        "state": "active",
        "owner_harness_id": "owner-harness",
    }
    members = communication_stack.store.fetch_all(
        """SELECT harness_id,role FROM collaboration_scope_members
            ORDER BY harness_id"""
    )
    assert [dict(row) for row in members] == [
        {
            "harness_id": communication_stack.actor.harness_id,
            "role": "member",
        },
        {"harness_id": "owner-harness", "role": "owner"},
    ]
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
    endpoints = communication_stack.store.fetch_all(
        """SELECT harness_id,state FROM endpoint_lifecycle ORDER BY harness_id"""
    )
    assert [dict(endpoint) for endpoint in endpoints] == [
        {"harness_id": communication_stack.actor.harness_id, "state": "access_ready"},
        {"harness_id": "owner-harness", "state": "access_ready"},
    ]
    records = communication_stack.store.fetch_all(
        "SELECT record_json FROM directory_records ORDER BY record_id"
    )
    parsed = [DirectoryRecord.model_validate_json(record["record_json"]) for record in records]
    assert [record.attributes["harness_id"] for record in parsed] == [
        communication_stack.actor.harness_id,
        "owner-harness",
    ]
    assert all(
        record.visible_to_principal_ids == (communication_stack.actor.principal_id,)
        for record in parsed
    )


def test_committed_v1_replay_materializes_missing_scope_and_directory(
    communication_stack,
) -> None:
    completed = _commit(communication_stack)
    legacy = completed | {"schema": "agentnet.communication-scope.complete-result.v1"}
    legacy.pop("collaboration_scope_id")
    source = communication_stack.store.fetch_one(
        "SELECT scope_id FROM communication_scopes WHERE state='committed'"
    )
    with communication_stack.store.transaction() as connection:
        connection.execute(
            "DELETE FROM collaboration_scope_members WHERE scope_id=?",
            (source["scope_id"],),
        )
        connection.execute(
            "DELETE FROM collaboration_scopes WHERE scope_id=?",
            (source["scope_id"],),
        )
        connection.execute("DELETE FROM directory_records")
        connection.execute(
            """UPDATE communication_scopes
                  SET committed_result_encrypted=?,committed_result_digest=?
                WHERE scope_id=?""",
            (
                communication_stack.store.cipher.encrypt_json(
                    legacy,
                    purpose=f"communication-scope-result:{source['scope_id']}",
                ),
                digest_canonical(legacy),
                source["scope_id"],
            ),
        )

    replayed = _complete(communication_stack)

    assert replayed["schema"] == "agentnet.communication-scope.complete-result.v2"
    assert replayed["collaboration_scope_id"] == source["scope_id"]
    assert communication_stack.store.fetch_one(
        """SELECT scope_id FROM collaboration_scopes
            WHERE source_communication_scope_id=? AND state='active'""",
        (source["scope_id"],),
    )["scope_id"] == source["scope_id"]
    assert communication_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM endpoint_lifecycle"
    )["n"] == 2
    assert communication_stack.store.fetch_one(
        "SELECT COUNT(*) AS n FROM directory_records"
    )["n"] == 2


def test_committed_replay_requires_setup_owned_endpoint(communication_stack) -> None:
    _commit(communication_stack)
    with communication_stack.store.transaction() as connection:
        connection.execute(
            "DELETE FROM endpoint_lifecycle WHERE harness_id=?",
            (communication_stack.actor.harness_id,),
        )

    with pytest.raises(ConflictError, match="endpoint setup is required"):
        _complete(communication_stack)


def test_begin_and_complete_are_idempotent(communication_stack) -> None:
    assert _begin(communication_stack) == _begin(communication_stack)
    assert communication_stack.client.create_calls == 1
    communication_stack.client.state = "issued"
    _status(communication_stack)
    assert _complete(communication_stack) == _complete(communication_stack)
    assert communication_stack.client.retrieve_calls == 1
    assert communication_stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 38


def test_committed_begin_replay_remains_bound_to_original_harness(
    communication_stack,
) -> None:
    _commit(communication_stack)
    peer = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=communication_stack.actor.domain_id,
        principal_id=communication_stack.actor.principal_id,
        harness_id="owner-harness",
        credential_id="owner-credential",
        credential_epoch=1,
        binding_assurance="os_bound",
    )

    with pytest.raises(ConflictError, match="idempotency conflict"):
        communication_stack.service.begin(
            actor=peer,
            request=CommunicationScopeBeginRequest(
                schema="agentnet.communication-scope.begin.v1",
                begin_idempotency_key=BEGIN_KEY,
            ),
        )


def test_new_begin_expires_due_orphaned_reservation(communication_stack) -> None:
    communication_stack.client.fail_create = True
    with pytest.raises(RuntimeError, match="approval unavailable"):
        _begin(communication_stack)

    communication_stack.client.fail_create = False
    replacement = CommunicationScopeBeginRequest(
        schema="agentnet.communication-scope.begin.v1",
        begin_idempotency_key="communication-scope-replacement-key-0001",
    )
    with pytest.raises(ConflictError, match="active communication scope"):
        communication_stack.service.begin(
            actor=communication_stack.actor,
            request=replacement,
        )

    communication_stack.service.clock = lambda: NOW + 3_600
    result = communication_stack.service.begin(
        actor=communication_stack.actor,
        request=replacement,
    )

    assert result["status"] == "approval_pending"
    assert [
        row["state"]
        for row in communication_stack.store.fetch_all(
            "SELECT state FROM communication_scopes ORDER BY created_at,scope_id"
        )
    ] == ["expired", "pending_approval"]


def test_concurrent_replacements_after_due_orphan_have_one_winner(
    communication_stack,
) -> None:
    communication_stack.client.fail_create = True
    with pytest.raises(RuntimeError, match="approval unavailable"):
        _begin(communication_stack)

    communication_stack.client.fail_create = False
    communication_stack.service.clock = lambda: NOW + 3_600

    def begin(key: str) -> str:
        try:
            result = communication_stack.service.begin(
                actor=communication_stack.actor,
                request=CommunicationScopeBeginRequest(
                    schema="agentnet.communication-scope.begin.v1",
                    begin_idempotency_key=key,
                ),
            )
        except ConflictError:
            return "conflict"
        return str(result["status"])

    with ThreadPoolExecutor(max_workers=2) as executor:
        outcomes = list(
            executor.map(
                begin,
                (
                    "communication-scope-racing-key-0001",
                    "communication-scope-racing-key-0002",
                ),
            )
        )

    assert sorted(outcomes) == ["approval_pending", "conflict"]
    assert sorted(
        row["state"]
        for row in communication_stack.store.fetch_all(
            "SELECT state FROM communication_scopes"
        )
    ) == ["expired", "pending_approval"]


def test_same_key_retry_expires_due_orphan_without_recalling_approval(
    communication_stack,
) -> None:
    communication_stack.client.fail_create = True
    with pytest.raises(RuntimeError, match="approval unavailable"):
        _begin(communication_stack)
    assert communication_stack.client.create_calls == 1

    communication_stack.client.fail_create = False
    communication_stack.service.clock = lambda: NOW + 3_600
    with pytest.raises(CommunicationScopeTerminalError, match="terminal"):
        _begin(communication_stack)

    assert communication_stack.client.create_calls == 1
    assert communication_stack.store.fetch_one(
        "SELECT state FROM communication_scopes"
    )["state"] == "expired"


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
    assert _complete(communication_stack, actor=rotated_actor) == expected
    assert dict(
        communication_stack.store.fetch_one(
            """SELECT current_credential_id,profile_key
                 FROM endpoint_lifecycle WHERE harness_id=?""",
            (rotated_actor.harness_id,),
        )
    ) == {
        "current_credential_id": rotated_actor.credential_id,
        "profile_key": "member-profile",
    }
    assert communication_stack.store.fetch_one(
        "SELECT expires_at FROM directory_records WHERE record_id=?",
        (f"agent:{rotated_actor.harness_id}",),
    )["expires_at"] == NOW + 86_400


@pytest.mark.parametrize("approval_issued", [False, True])
def test_rotated_current_credential_terminalizes_precommit_scope(
    communication_stack, approval_issued: bool
) -> None:
    _begin(communication_stack)
    if approval_issued:
        communication_stack.client.state = "issued"
        assert _status(communication_stack)["status"] == "approval_ready"
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
                "credential-rotated-precommit",
                communication_stack.actor.harness_id,
                rotated_key.thumbprint,
                rotated_key.public_pem,
                "active",
                2,
                NOW - 1,
                NOW + 86_400,
            ),
        )
    rotated_actor = communication_stack.actor.model_copy(
        update={
            "credential_id": "credential-rotated-precommit",
            "credential_epoch": 2,
        }
    )
    communication_stack.resolver.actor = rotated_actor
    communication_stack.resolver.fresh_credential_epoch = 2

    with pytest.raises(CommunicationScopeTerminalError, match="terminal"):
        communication_stack.service.begin(
            actor=rotated_actor,
            request=CommunicationScopeBeginRequest(
                schema="agentnet.communication-scope.begin.v1",
                begin_idempotency_key=BEGIN_KEY,
            ),
        )
    with pytest.raises(CommunicationScopeTerminalError, match="terminal"):
        communication_stack.service.begin(
            actor=rotated_actor,
            request=CommunicationScopeBeginRequest(
                schema="agentnet.communication-scope.begin.v1",
                begin_idempotency_key=BEGIN_KEY,
            ),
        )

    assert communication_stack.store.fetch_one(
        "SELECT state FROM communication_scopes"
    )["state"] == "invalidated"


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
