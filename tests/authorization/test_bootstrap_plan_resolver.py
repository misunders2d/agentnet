from __future__ import annotations

import base64
import hashlib
from dataclasses import dataclass
from uuid import uuid4

import pytest

from agentnet.approval import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.authorization.bootstrap_plan_service import ExactBootstrapHarnessResolver
from agentnet.authorization.communication_scope_service import ExactCommunicationHarnessResolver
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.domains import DomainRegistry
from agentnet.identity.enrollment import (
    ENROLLMENT_APPROVAL_PURPOSE,
    EnrollmentService,
    VerifiedOIDCIdentity,
)
from agentnet.operations.config import RuntimeProfile
from agentnet.security.signatures import P256KeyPair


NOW = 1_800_000_000


@dataclass
class MutableClock:
    value: int

    def __call__(self) -> int:
        return self.value


@pytest.fixture
def resolver_stack(store):
    DomainRegistry(store).register("corp.example", now=NOW - 10_000)
    clock = MutableClock(NOW)
    approver_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id="security-owner",
        domain_id="corp.example",
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({ENROLLMENT_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {approver.signer_key_id: approver}, verifier_id="approval.corp.example"
    )
    enrollment = EnrollmentService(
        store,
        verifier,
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        binding_assurance="os_bound",
        credential_ttl=20_000,
        clock=clock,
    )

    def seed(*, name: str, consumed_at: int, remote_activation: bool = False):
        clock.value = consumed_at
        key = P256KeyPair.generate()
        identity = VerifiedOIDCIdentity(
            issuer="https://idp.example",
            subject="same-owner-subject",
            verified_email="owner@example.test",
        )
        with store.transaction() as connection:
            challenge = enrollment._begin_in_transaction(
                connection,
                domain_id="corp.example",
                identity=identity,
                harness_kind="pi",
                harness_name=name,
                public_key_pem=key.public_pem,
                now=consumed_at,
            )
        approval = create_independent_approval_receipt(
            approver_key,
            approver=approver,
            verifier_id=verifier.verifier_id,
            approval_purpose=ENROLLMENT_APPROVAL_PURPOSE,
            canonical_transaction=challenge.canonical_transaction,
            issued_at=consumed_at,
            authenticated_at=consumed_at,
            expires_at=consumed_at + 60,
        )
        result = enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=key.sign(
                "agentnet.enrollment.pop.v1", challenge.signed_fields()
            ),
            approval=approval,
        )
        transaction_id = str(uuid4())
        challenge_digest = hashlib.sha256(challenge.canonical_transaction).hexdigest()
        challenge_payload = {
            "challenge_id": challenge.challenge_id,
            "nonce": challenge.nonce,
            "canonical_transaction_b64": base64.b64encode(
                challenge.canonical_transaction
            ).decode("ascii"),
        }
        if remote_activation:
            challenge_payload["activation_mode"] = "remote_browser"
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO oidc_enrollment_transactions(
                    transaction_id,domain_id,issuer,client_id,audience,redirect_uri,
                    state_hash,nonce_hash,code_verifier_encrypted,harness_kind,harness_name,
                    public_key_pem,key_id,binding_assurance,status,created_at,expires_at,
                    claimed_at,consumed_at,authorization_code_hash,id_token_hash,
                    enrollment_challenge_id
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'consumed',?,?,?,?,?,?,?)""",
                (
                    transaction_id,
                    "corp.example",
                    identity.issuer,
                    "client",
                    "client",
                    "https://core.example/oidc/callback",
                    hashlib.sha256(f"state:{transaction_id}".encode()).hexdigest(),
                    hashlib.sha256(f"nonce:{transaction_id}".encode()).hexdigest(),
                    "encrypted-verifier",
                    "pi",
                    name,
                    key.public_pem,
                    key.thumbprint,
                    "os_bound",
                    consumed_at - 5,
                    consumed_at + 300,
                    consumed_at - 2,
                    consumed_at,
                    hashlib.sha256(f"code:{transaction_id}".encode()).hexdigest(),
                    hashlib.sha256(f"token:{transaction_id}".encode()).hexdigest(),
                    challenge.challenge_id,
                ),
            )
            connection.execute(
                """INSERT INTO oidc_enrollment_continuations(
                    transaction_id,continuation_hash,status,challenge_encrypted,
                    approval_request_id,approval_transaction_digest,
                    approval_request_expires_at,poll_after_at,poll_interval_seconds,
                    poll_count,created_at,updated_at,expires_at
                ) VALUES(?,?,'enrolled',?,?,?,?,?,2,0,?,?,?)""",
                (
                    transaction_id,
                    hashlib.sha256(f"continuation:{transaction_id}".encode()).hexdigest(),
                    store.cipher.encrypt_json(
                        challenge_payload,
                        purpose=f"oidc-guided-challenge:{transaction_id}",
                    ),
                    f"approval-{transaction_id}",
                    challenge_digest,
                    consumed_at + 60,
                    consumed_at,
                    consumed_at,
                    consumed_at,
                    consumed_at + 300,
                ),
            )
        return result.actor

    return store, verifier, seed


def _resolve(store, verifier, actor):
    resolver = ExactBootstrapHarnessResolver(store, verifier)
    with store.transaction() as connection:
        return resolver(connection, actor, NOW)


def test_exact_two_guided_harnesses_resolve_old_owner_and_fresh_actor(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    owner = seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)

    resolved = _resolve(store, verifier, fresh)

    assert resolved["domain"]["domain_id"] == fresh.domain_id
    assert resolved["principal"]["principal_id"] == fresh.principal_id == owner.principal_id
    assert resolved["harnesses"]["owner"]["harness_id"] == owner.harness_id
    assert resolved["harnesses"]["fresh"]["harness_id"] == fresh.harness_id
    assert resolved["harnesses"]["owner"]["display_name"] == "Owner laptop"
    assert resolved["harnesses"]["fresh"]["display_name"] == "Fresh laptop"
    assert resolved["enrollment_evidence"]["fresh"]["role"] == "fresh"
    assert resolved["enrollment_evidence"]["owner"]["role"] == "owner"
    assert "oidc_subject" not in resolved["enrollment_evidence"]["fresh"]
    assert "verified_email" not in resolved["enrollment_evidence"]["fresh"]


def test_exact_two_remote_guided_harnesses_resolve_for_c0_plan(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner server", consumed_at=NOW - 5_000, remote_activation=True)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60, remote_activation=True)

    resolved = _resolve(store, verifier, fresh)

    assert resolved["harnesses"]["owner"]["display_name"] == "Owner server"
    assert resolved["harnesses"]["fresh"]["display_name"] == "Fresh laptop"


def test_resolver_rejects_non_remote_activation_mode(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner server", consumed_at=NOW - 5_000, remote_activation=True)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60, remote_activation=True)
    with store.transaction() as connection:
        row = connection.execute(
            """SELECT t.transaction_id,g.challenge_encrypted
                 FROM oidc_enrollment_transactions t
                 JOIN oidc_enrollment_continuations g ON g.transaction_id=t.transaction_id
                WHERE t.harness_name='Fresh laptop'"""
        ).fetchone()
        protected = store.cipher.decrypt_json(
            row["challenge_encrypted"],
            purpose=f"oidc-guided-challenge:{row['transaction_id']}",
        )
        protected["activation_mode"] = "local_browser"
        connection.execute(
            """UPDATE oidc_enrollment_continuations SET challenge_encrypted=?
                WHERE transaction_id=?""",
            (
                store.cipher.encrypt_json(
                    protected,
                    purpose=f"oidc-guided-challenge:{row['transaction_id']}",
                ),
                row["transaction_id"],
            ),
        )

    with pytest.raises(AuthorizationError, match="guided enrollment proof"):
        _resolve(store, verifier, fresh)


def test_resolver_rejects_one_or_three_guided_candidates(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)
    with pytest.raises(ConflictError, match="exactly two"):
        _resolve(store, verifier, fresh)

    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    seed(name="Third laptop", consumed_at=NOW - 120)
    with pytest.raises(ConflictError, match="exactly two"):
        _resolve(store, verifier, fresh)


def test_resolver_applies_age_limit_only_to_authenticated_fresh_actor(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    stale_fresh = seed(name="Fresh laptop", consumed_at=NOW - 901)

    with pytest.raises(AuthorizationError, match="fresh enrollment"):
        _resolve(store, verifier, stale_fresh)


def test_duplicate_active_current_epoch_credential_blocks_even_if_future_dated(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)
    with store.transaction() as connection:
        duplicate_key = P256KeyPair.generate()
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?, 'active',?,?,?)""",
            (
                "duplicate-current-epoch-credential",
                fresh.harness_id,
                duplicate_key.thumbprint,
                duplicate_key.public_pem,
                fresh.credential_epoch,
                NOW + 100,
                NOW + 200,
            ),
        )

    with pytest.raises(ConflictError, match="active current-epoch credential"):
        _resolve(store, verifier, fresh)


@pytest.mark.parametrize(
    "mutation",
    [
        "UPDATE oidc_enrollment_transactions SET harness_name='Different laptop' WHERE harness_name='Fresh laptop'",
        "UPDATE oidc_enrollment_transactions SET harness_kind='claude' WHERE harness_name='Fresh laptop'",
        "UPDATE oidc_enrollment_transactions SET binding_assurance='hardware_bound' WHERE harness_name='Fresh laptop'",
        "UPDATE oidc_enrollment_transactions SET key_id='tampered-key' WHERE harness_name='Fresh laptop'",
        "UPDATE harnesses SET display_name='Different laptop' WHERE display_name='Fresh laptop'",
        "UPDATE harnesses SET kind='claude' WHERE display_name='Fresh laptop'",
    ],
)
def test_resolver_rejects_mismatched_guided_identity_metadata(
    resolver_stack, mutation: str
) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)
    with store.transaction() as connection:
        connection.execute(mutation)

    with pytest.raises((AuthorizationError, ConflictError)):
        _resolve(store, verifier, fresh)


@pytest.mark.parametrize(
    "actor_update",
    [
        {"harness_id": "different-harness"},
        {"credential_id": "different-credential"},
        {"credential_epoch": 2},
        {"binding_assurance": "lab"},
    ],
)
def test_resolver_binds_every_authenticated_fresh_actor_field(
    resolver_stack, actor_update: dict[str, object]
) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)

    with pytest.raises(AuthorizationError):
        _resolve(store, verifier, fresh.model_copy(update=actor_update))


def test_resolver_rejects_tampered_historical_receipt(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)
    with store.transaction() as connection:
        connection.execute(
            """UPDATE enrollment_challenges SET approved_receipt='{}'
                WHERE challenge_id=(
                    SELECT enrollment_challenge_id FROM oidc_enrollment_transactions
                    WHERE harness_name='Fresh laptop'
                )"""
        )
    with pytest.raises(AuthorizationError):
        _resolve(store, verifier, fresh)


def test_resolver_rejects_future_fresh_oidc_consumption(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)
    with store.transaction() as connection:
        connection.execute(
            """UPDATE oidc_enrollment_transactions SET consumed_at=?
                WHERE harness_name='Fresh laptop'""",
            (NOW + 1,),
        )

    with pytest.raises(AuthorizationError, match="fresh enrollment"):
        _resolve(store, verifier, fresh)


def test_resolver_rejects_tampered_guided_challenge_ciphertext(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    seed(name="Owner laptop", consumed_at=NOW - 5_000)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60)
    with store.transaction() as connection:
        transaction = connection.execute(
            """SELECT transaction_id FROM oidc_enrollment_transactions
                WHERE enrollment_challenge_id=(
                    SELECT challenge_id FROM enrollment_challenges WHERE key_id=(
                        SELECT key_id FROM credentials WHERE credential_id=?
                    )
                )""",
            (fresh.credential_id,),
        ).fetchone()
        connection.execute(
            "UPDATE oidc_enrollment_continuations SET challenge_encrypted=? WHERE transaction_id=?",
            ("tampered", transaction["transaction_id"]),
        )

    with pytest.raises(AuthorizationError, match="guided enrollment proof"):
        _resolve(store, verifier, fresh)


def _resolve_communication(store, verifier, *, owner, actor):
    resolver = ExactCommunicationHarnessResolver(
        store,
        verifier,
        owner_harness_id=owner.harness_id,
        fresh_max_age_seconds=3_600,
    )
    with store.transaction() as connection:
        return resolver(connection, actor, NOW)


def test_communication_resolver_authenticates_configured_owner_server(
    resolver_stack,
) -> None:
    store, verifier, seed = resolver_stack
    owner = seed(name="Owner server", consumed_at=NOW - 5_000, remote_activation=True)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60, remote_activation=True)

    resolved = _resolve_communication(
        store,
        verifier,
        owner=owner,
        actor=owner,
    )

    assert resolved["harnesses"]["owner"]["harness_id"] == owner.harness_id
    assert resolved["harnesses"]["fresh"]["harness_id"] == fresh.harness_id
    assert resolved["enrollment_evidence"]["owner"]["role"] == "owner"
    assert resolved["enrollment_evidence"]["fresh"]["role"] == "fresh"


def test_communication_resolver_rejects_non_owner_caller(resolver_stack) -> None:
    store, verifier, seed = resolver_stack
    owner = seed(name="Owner server", consumed_at=NOW - 5_000, remote_activation=True)
    fresh = seed(name="Fresh laptop", consumed_at=NOW - 60, remote_activation=True)

    with pytest.raises(AuthorizationError, match="communication scope denied"):
        _resolve_communication(
            store,
            verifier,
            owner=owner,
            actor=fresh,
        )
