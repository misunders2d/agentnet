"""Direct-construction coverage for retained unmounted legacy bootstrap only."""

from __future__ import annotations

import base64
import hashlib
import json
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
from uuid import NAMESPACE_URL, uuid4, uuid5

import pytest
import httpx
from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Route

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.authority_bootstrap import (
    AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE,
    FirstAuthorityBootstrapService,
    INITIAL_ROOT_ACTION,
    INITIAL_ROOT_RESOURCE,
)
from agentnet.authorization.policy import PolicyEngine
from agentnet.authority_bootstrap_http import create_authority_bootstrap_routes
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.enrollment import ENROLLMENT_APPROVAL_PURPOSE
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.sqlite import SQLiteStore


NOW = datetime(2026, 7, 12, 12, 0, tzinfo=UTC)


def _seed_new_enrollment(
    store: SQLiteStore,
    key: P256KeyPair,
    enrollment_receipt: dict[str, object],
    *,
    suffix: str = "one",
    domain_id: str = "corp.example",
) -> VerifiedActor:
    now = int(NOW.timestamp())
    enrollment_challenge_id = str(uuid4())
    harness_id = str(uuid5(NAMESPACE_URL, f"agentnet:harness:{enrollment_challenge_id}"))
    credential_id = str(uuid5(NAMESPACE_URL, f"agentnet:credential:{enrollment_challenge_id}"))
    principal_id = f"human-{suffix}"
    with store.transaction() as connection:
        domain = connection.execute("SELECT domain_id FROM domains WHERE domain_id=?", (domain_id,)).fetchone()
        if domain is None:
            connection.execute(
                "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
                (domain_id, "active", 1, 1, now - 60),
            )
        connection.execute(
            """INSERT INTO principals(
                   principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
               ) VALUES(?,?,?,?,?,?,?)""",
            (
                principal_id,
                domain_id,
                "https://idp.example",
                f"subject-{suffix}",
                f"{suffix}@example.test",
                "active",
                now - 10,
            ),
        )
        connection.execute(
            """INSERT INTO enrollment_challenges(
                   challenge_id,domain_id,oidc_issuer,oidc_subject,verified_email,
                   harness_kind,harness_name,public_key_pem,key_id,nonce_hash,
                   transaction_digest,expires_at,approved_receipt,consumed_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                enrollment_challenge_id,
                domain_id,
                "https://idp.example",
                f"subject-{suffix}",
                f"{suffix}@example.test",
                "codex",
                f"Codex {suffix}",
                key.public_pem,
                key.thumbprint,
                "a" * 64,
                "b" * 64,
                now + 300,
                canonical_json(enrollment_receipt).decode("utf-8"),
                now - 5,
            ),
        )
        connection.execute(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                   binding_assurance,capabilities_json,credential_epoch,created_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (
                harness_id,
                domain_id,
                principal_id,
                None,
                "codex",
                f"Codex {suffix}",
                "active",
                "os_bound",
                "[]",
                1,
                now - 5,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,?,?,?,?,?)""",
            (credential_id, harness_id, key.thumbprint, key.public_pem, "active", 1, now - 5, now + 3600),
        )
        connection.execute(
            """INSERT INTO oidc_enrollment_transactions(
                   transaction_id,domain_id,issuer,client_id,audience,redirect_uri,state_hash,nonce_hash,
                   code_verifier_encrypted,harness_kind,harness_name,public_key_pem,key_id,
                   binding_assurance,status,created_at,expires_at,claimed_at,consumed_at,
                   authorization_code_hash,id_token_hash,enrollment_challenge_id
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                str(uuid4()),
                domain_id,
                "https://idp.example",
                "client",
                "client",
                "https://agentnet.example/callback",
                hashlib.sha256(f"state-{suffix}".encode()).hexdigest(),
                hashlib.sha256(f"nonce-{suffix}".encode()).hexdigest(),
                "encrypted",
                "codex",
                f"Codex {suffix}",
                key.public_pem,
                key.thumbprint,
                "os_bound",
                "consumed",
                now - 10,
                now + 300,
                now - 8,
                now - 7,
                hashlib.sha256(f"code-{suffix}".encode()).hexdigest(),
                hashlib.sha256(f"token-{suffix}".encode()).hexdigest(),
                enrollment_challenge_id,
            ),
        )
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=domain_id,
        principal_id=principal_id,
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="os_bound",
    )


@pytest.fixture
def stack(tmp_path):
    store = SQLiteStore(tmp_path / "bootstrap.db", LocalEnvelopeCipher(b"z" * 32))
    approver_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="security-approver",
        domain_id="corp.example",
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset(
            {ENROLLMENT_APPROVAL_PURPOSE, AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE}
        ),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver}, verifier_id="approval.corp.example"
    )
    enrollment_receipt = create_independent_approval_receipt(
        approver_key,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=ENROLLMENT_APPROVAL_PURPOSE,
        canonical_transaction=b"enrollment-transaction",
        issued_at=int(NOW.timestamp()) - 6,
        expires_at=int(NOW.timestamp()) + 60,
    )
    actor_key = P256KeyPair.generate()
    actor = _seed_new_enrollment(store, actor_key, enrollment_receipt)
    policy = PolicyEngine(store, runtime_profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT)
    service = FirstAuthorityBootstrapService(
        store,
        policy,
        verifier,
        runtime_profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
    )
    try:
        yield SimpleNamespace(
            store=store,
            actor=actor,
            actor_key=actor_key,
            approver=approver,
            approver_key=approver_key,
            verifier=verifier,
            enrollment_receipt=enrollment_receipt,
            policy=policy,
            service=service,
        )
    finally:
        store.close()


def _approval(stack, challenge, *, transaction: bytes | None = None, signer=None, approver=None, purpose=None):
    return create_independent_approval_receipt(
        signer or stack.approver_key,
        approver=approver or stack.approver,
        verifier_id=stack.verifier.verifier_id,
        approval_purpose=purpose or AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE,
        canonical_transaction=transaction or challenge.canonical_transaction,
        issued_at=int(NOW.timestamp()),
        expires_at=int(NOW.timestamp()) + 60,
    )


def _complete(stack, challenge, **overrides):
    values = {
        "actor": stack.actor,
        "challenge_id": challenge.challenge_id,
        "nonce": challenge.nonce,
        "canonical_transaction": challenge.canonical_transaction,
        "approval": _approval(stack, challenge),
        "when": NOW,
    }
    values.update(overrides)
    return stack.service.complete(**values)


def test_exact_independent_receipt_issues_only_minimal_initial_root_atomically(stack) -> None:
    challenge = stack.service.begin(actor=stack.actor, when=NOW)
    result = _complete(stack, challenge)

    assert result.entitlement.action == INITIAL_ROOT_ACTION
    assert result.entitlement.resource_pattern == INITIAL_ROOT_RESOURCE
    assert result.entitlement.domain_id == stack.actor.domain_id
    assert result.entitlement.principal_id == stack.actor.principal_id
    assert result.entitlement.revision == 1
    assert result.entitlement.expires_at == NOW + timedelta(hours=1)
    assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 1
    assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM authority_bootstrap_slots")["n"] == 1
    consumed = stack.store.fetch_one(
        "SELECT * FROM authority_bootstrap_challenges WHERE challenge_id=?",
        (challenge.challenge_id,),
    )
    assert consumed["consumed_at"] == int(NOW.timestamp())
    assert consumed["approval_receipt_id"] == result.approval_receipt_id
    assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM replay_nonces")["n"] == 1
    actions = [
        json.loads(row["record_json"])["action"]
        for row in stack.store.fetch_all("SELECT record_json FROM audit_log ORDER BY sequence")
    ]
    assert actions[-3:] == [
        "authorization.initial_root.challenge.created",
        "authorization.initial_root.issued",
        "authorization.initial_root.challenge.consumed",
    ]
    assert stack.store.verify_audit_chain()[0]


def test_existing_current_root_blocks_challenge_and_second_challenge_race(stack) -> None:
    first = stack.service.begin(actor=stack.actor, when=NOW)
    second = stack.service.begin(actor=stack.actor, when=NOW)
    _complete(stack, first)
    with pytest.raises(ConflictError):
        _complete(stack, second)
    with pytest.raises(ConflictError):
        stack.service.begin(actor=stack.actor, when=NOW)
    assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 1


def test_same_challenge_and_receipt_have_exactly_one_race_winner(stack) -> None:
    challenge = stack.service.begin(actor=stack.actor, when=NOW)
    approval = _approval(stack, challenge)

    def attempt():
        return stack.service.complete(
            actor=stack.actor,
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            approval=approval,
            when=NOW,
        )

    with ThreadPoolExecutor(max_workers=2) as executor:
        futures = [executor.submit(attempt) for _ in range(2)]
    outcomes = []
    for future in futures:
        try:
            outcomes.append(future.result())
        except (AuthenticationError, ConflictError) as exc:
            outcomes.append(exc)
    assert sum(not isinstance(value, Exception) for value in outcomes) == 1
    assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 1


def test_actor_policy_key_transaction_and_receipt_substitutions_fail_closed(stack) -> None:
    challenge = stack.service.begin(actor=stack.actor, when=NOW)
    sibling_key = P256KeyPair.generate()
    sibling = _seed_new_enrollment(
        stack.store,
        sibling_key,
        stack.enrollment_receipt,
        suffix="sibling",
    )
    with pytest.raises(AuthenticationError, match="actor substitution"):
        _complete(stack, challenge, actor=sibling)

    tampered = canonical_json(
        {**json.loads(challenge.canonical_transaction), "approval_purpose": "other"}
    )
    with pytest.raises(AuthenticationError, match="transaction binding"):
        _complete(
            stack,
            challenge,
            canonical_transaction=tampered,
            approval=_approval(stack, challenge, transaction=tampered),
        )
    with pytest.raises(AuthenticationError, match="transaction binding"):
        _complete(
            stack,
            challenge,
            approval=_approval(stack, challenge, transaction=b"other exact transaction"),
        )

    untrusted = P256KeyPair.generate()
    with pytest.raises(AuthenticationError):
        _complete(stack, challenge, approval=_approval(stack, challenge, signer=untrusted))
    with pytest.raises(AuthenticationError, match="purpose or domain"):
        _complete(
            stack,
            challenge,
            approval=_approval(stack, challenge, purpose="identity.enrollment.approve"),
        )

    with stack.store.transaction() as connection:
        connection.execute("UPDATE domains SET policy_revision=2 WHERE domain_id='corp.example'")
    with pytest.raises(ConflictError, match="policy revision"):
        _complete(stack, challenge)
    assert stack.store.fetch_one("SELECT COUNT(*) AS n FROM entitlements")["n"] == 0


def test_epoch_rotation_stale_enrollment_and_non_oidc_actor_are_denied(stack) -> None:
    challenge = stack.service.begin(actor=stack.actor, when=NOW)
    with stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='retired' WHERE credential_id=?",
            (stack.actor.credential_id,),
        )
        connection.execute(
            "UPDATE harnesses SET credential_epoch=2 WHERE harness_id=?",
            (stack.actor.harness_id,),
        )
    with pytest.raises(AuthorizationError):
        _complete(stack, challenge)

    with stack.store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='active' WHERE credential_id=?",
            (stack.actor.credential_id,),
        )
        connection.execute(
            "UPDATE harnesses SET credential_epoch=1 WHERE harness_id=?",
            (stack.actor.harness_id,),
        )
        connection.execute(
            "UPDATE enrollment_challenges SET consumed_at=?",
            (int(NOW.timestamp()) - 901,),
        )
    with pytest.raises(AuthorizationError, match="newly enrolled"):
        stack.service.begin(actor=stack.actor, when=NOW)


@pytest.mark.anyio
async def test_http_routes_use_authenticated_actor_and_preserve_exact_transaction(stack) -> None:
    current = datetime.now(UTC)
    current_epoch = int(current.timestamp())
    with stack.store.transaction() as connection:
        connection.execute(
            "UPDATE enrollment_challenges SET consumed_at=?",
            (current_epoch - 1,),
        )
        connection.execute(
            "UPDATE oidc_enrollment_transactions SET consumed_at=?",
            (current_epoch - 2,),
        )
        connection.execute(
            "UPDATE credentials SET not_before=?,expires_at=?",
            (current_epoch - 5, current_epoch + 3_600),
        )
    core = SimpleNamespace(store=stack.store)
    authenticated_calls: list[str] = []

    async def body_and_actor(request, _core):
        assert request.headers.get("x-test-auth") == "bound"
        authenticated_calls.append(request.url.path)
        return await request.body(), stack.actor

    app = Starlette(
        routes=create_authority_bootstrap_routes(
            core,
            body_and_actor,
            service=stack.service,
        ),
    )
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, raise_app_exceptions=True),
        base_url="https://agentnet.example",
    ) as client:
        begun = await client.post(
            "/v1/authority-bootstrap/challenges",
            json={},
            headers={"x-test-auth": "bound"},
        )
        assert begun.status_code == 201
        value = begun.json()
        canonical = base64.b64decode(value["canonical_transaction_b64"], validate=True)
        assert canonical == canonical_json(json.loads(canonical))
        approval = create_independent_approval_receipt(
            stack.approver_key,
            approver=stack.approver,
            verifier_id=stack.verifier.verifier_id,
            approval_purpose=AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE,
            canonical_transaction=canonical,
            issued_at=current_epoch,
            expires_at=current_epoch + 60,
        )
        completed = await client.post(
            f"/v1/authority-bootstrap/challenges/{value['challenge_id']}/complete",
            json={
                "nonce": value["nonce"],
                "canonical_transaction_b64": value["canonical_transaction_b64"],
                "independent_approval": approval,
            },
            headers={"x-test-auth": "bound"},
        )
        assert completed.status_code == 201
        assert completed.json()["entitlement"]["action"] == INITIAL_ROOT_ACTION
    assert authenticated_calls == [
        "/v1/authority-bootstrap/challenges",
        f"/v1/authority-bootstrap/challenges/{value['challenge_id']}/complete",
    ]


def test_local_profile_or_local_approval_path_cannot_construct_service(stack) -> None:
    with pytest.raises(GateBlocked, match="server-agent profile"):
        FirstAuthorityBootstrapService(
            stack.store,
            stack.policy,
            stack.verifier,
            runtime_profile=RuntimeProfile.LOCAL_CONFORMANCE,
        )


def test_ordinary_app_never_mounts_legacy_authority_bootstrap(
    stack,
    tmp_path,
) -> None:
    import agentnet.http_api as http_api_module

    core = CommunicationCore(
        ExtensionConfig(
            domain_id="corp.example",
            data_dir=tmp_path / "app-data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        stack.store,
    )
    app = http_api_module.create_app(core)
    paths = {route.path for route in app.routes if hasattr(route, "path")}
    assert all(not path.startswith("/v1/authority-bootstrap") for path in paths)
