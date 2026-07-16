from __future__ import annotations

import hashlib
import urllib.parse
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta

import pytest

from agentnet.errors import AuthenticationError, ReplayError, ValidationError
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitation_oidc import InternalInvitationOIDCCoordinator
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_POP_PURPOSE,
    InternalInvitationService,
    InternalInvitationTransaction,
)
from agentnet.identity.oidc import (
    OIDCProvider,
    OIDCProviderConfig,
    OIDCVerificationResult,
)
from agentnet.operations.config import OIDCTokenEndpointAuthMethod
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import (
    P256KeyPair,
    canonical_json,
    verify_signature,
)
from agentnet.storage.sqlite import SQLiteStore


NOW = datetime(2026, 7, 13, 15, 0, tzinfo=UTC)
ISSUER = "https://id.corp.example"


class MutableClock:
    def __init__(self, value: int) -> None:
        self.value = value

    def __call__(self) -> int:
        return self.value


class DeterministicOIDCProvider(OIDCProvider):
    """OIDCProvider test double retaining the coordinator's production type boundary."""

    def __init__(
        self,
        clock: MutableClock,
        *,
        client_id: str = "invitation-client",
        token_endpoint_auth_method: OIDCTokenEndpointAuthMethod = OIDCTokenEndpointAuthMethod.NONE,
        client_secret: str | None = None,
    ) -> None:
        super().__init__(
            OIDCProviderConfig(
                issuer=ISSUER,
                client_id=client_id,
                redirect_uri="https://agentnet.corp.example/oidc/internal-invitation/callback",
                token_endpoint_auth_method=token_endpoint_auth_method,
                client_secret=client_secret,
                allowed_signing_algorithms=("ES256",),
                authorization_ttl_seconds=60,
            ),
            clock=clock,
            resolver=lambda _host, _port: ("8.8.8.8",),
        )
        self._bindings: set[tuple[str, str]] = set()
        self.forced_token_hash: str | None = None
        self.exchange_count = 0

    def authorization_url(self, *, state: str, nonce: str, code_challenge: str) -> str:
        self._bindings.add((hashlib.sha256(nonce.encode("utf-8")).hexdigest(), code_challenge))
        return "https://id.corp.example/authorize?" + urllib.parse.urlencode(
            {"state": state, "nonce": nonce, "code_challenge": code_challenge}
        )

    def exchange_and_verify(
        self,
        *,
        code: str,
        code_verifier: str,
        expected_nonce_hash: str,
    ) -> OIDCVerificationResult:
        challenge = _challenge(code_verifier)
        if (expected_nonce_hash, challenge) not in self._bindings:
            raise AuthenticationError("stored nonce or PKCE binding changed")
        self.exchange_count += 1
        return OIDCVerificationResult(
            identity=VerifiedOIDCIdentity(
                issuer=ISSUER,
                subject="invited-subject",
                verified_email="invited.person@corp.example",
            ),
            id_token_hash=self.forced_token_hash
            or hashlib.sha256(f"id-token:{code}".encode("utf-8")).hexdigest(),
            expires_at=self.clock() + 300,
        )


class DelegatingBackendContract:
    """Exercise coordinator SQL through StoreBackend rather than SQLite type APIs."""

    backend_name = "delegating-contract"

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
        return None


def _challenge(verifier: str) -> str:
    import base64

    return base64.urlsafe_b64encode(hashlib.sha256(verifier.encode("ascii")).digest()).rstrip(
        b"="
    ).decode("ascii")


@pytest.fixture
def invitation_oidc(tmp_path):
    clock = MutableClock(int(NOW.timestamp()))
    store = SQLiteStore(tmp_path / "invitation-oidc.sqlite3", LocalEnvelopeCipher(b"i" * 32))
    sponsor_key = P256KeyPair.generate()
    candidate_key = P256KeyPair.generate()
    transaction = InternalInvitationTransaction(
        invitation_id="internal-invitation-oidc-000000000001",
        domain_id="corp.example",
        sponsor_authority_kind="human",
        sponsor_authority_id="sponsor-principal",
        sponsor_harness_id="sponsor-harness",
        sponsor_credential_id="sponsor-credential",
        sponsor_credential_epoch=1,
        invited_oidc_issuer=ISSUER,
        invited_oidc_subject="invited-subject",
        invited_verified_email="invited.person@corp.example",
        candidate_harness_id="invited-codex-harness",
        candidate_harness_kind="codex",
        candidate_harness_display_name="Invited Codex harness",
        candidate_binding_assurance="os_bound",
        candidate_key_id=candidate_key.thumbprint,
        candidate_public_key_pem=candidate_key.public_pem,
        requested_capabilities=("background_delivery", "messaging"),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=NOW + timedelta(hours=1),
        reason="approved workforce invitation",
    )
    canonical = canonical_json(transaction.model_dump(mode="json"))
    digest = hashlib.sha256(canonical).hexdigest()
    now = clock()
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("corp.example", "active", 1, 1, now - 60),
        )
        connection.execute(
            """
            INSERT INTO principals(
                principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
            ) VALUES(?,?,?,?,?,'active',?)
            """,
            (
                "sponsor-principal",
                "corp.example",
                ISSUER,
                "sponsor-subject",
                "sponsor@corp.example",
                now - 60,
            ),
        )
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,NULL,'codex',?,'active','os_bound','[]',1,?)
            """,
            ("sponsor-harness", "corp.example", "sponsor-principal", "Sponsor", now - 60),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)
            """,
            (
                "sponsor-credential",
                "sponsor-harness",
                sponsor_key.thumbprint,
                sponsor_key.public_pem,
                now - 60,
                now + 7_200,
            ),
        )
        connection.execute(
            """
            INSERT INTO internal_invitations(
                invitation_id,schema_version,domain_id,sponsor_authority_kind,
                sponsor_authority_id,sponsor_harness_id,sponsor_credential_id,
                sponsor_credential_epoch,invited_oidc_issuer,invited_oidc_subject,
                invited_verified_email,candidate_harness_id,candidate_harness_kind,
                candidate_key_id,candidate_public_key_pem,requested_capabilities_json,
                policy_revision,domain_revocation_epoch,max_uses,use_count,state,revision,
                canonical_invitation_json,invitation_digest,expires_at,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,0,'active',1,?,?,?,?,?)
            """,
            (
                transaction.invitation_id,
                "1.0",
                transaction.domain_id,
                transaction.sponsor_authority_kind,
                transaction.sponsor_authority_id,
                transaction.sponsor_harness_id,
                transaction.sponsor_credential_id,
                transaction.sponsor_credential_epoch,
                transaction.invited_oidc_issuer,
                transaction.invited_oidc_subject,
                transaction.invited_verified_email,
                transaction.candidate_harness_id,
                transaction.candidate_harness_kind,
                transaction.candidate_key_id,
                transaction.candidate_public_key_pem,
                canonical_json(list(transaction.requested_capabilities)).decode("utf-8"),
                transaction.policy_revision,
                transaction.domain_revocation_epoch,
                canonical.decode("utf-8"),
                digest,
                int(transaction.expires_at.timestamp()),
                now,
                now,
            ),
        )
    provider = DeterministicOIDCProvider(clock)
    coordinator = InternalInvitationOIDCCoordinator(store, provider)
    yield store, clock, transaction, canonical, candidate_key, provider, coordinator
    store.close()


def _complete(coordinator, canonical, authorization, *, code="authorization-code-0001"):
    return coordinator.complete_authorization(
        canonical_invitation=canonical,
        evidence={"state": authorization.state, "code": code},
    )


def _accept(coordinator, transaction, canonical, challenge, *, token=None, when=NOW):
    return coordinator.verify_invitation_identity(
        canonical_invitation=canonical,
        evidence={
            "transaction_id": challenge.transaction_id,
            "acceptance_token": token or challenge.acceptance_token,
        },
        expected_issuer=transaction.invited_oidc_issuer,
        when=when,
    )


def test_two_step_challenge_enables_exact_candidate_pop_and_one_use_acceptance(
    invitation_oidc,
) -> None:
    store, _clock, transaction, canonical, candidate_key, provider, coordinator = invitation_oidc
    authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
    challenge = _complete(coordinator, canonical, authorization)

    row = store.fetch_one(
        "SELECT * FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )
    assert row["status"] == "verified"
    assert row["invitation_digest"] == hashlib.sha256(canonical).hexdigest()
    assert row["verifier_id"] == coordinator.verifier_id
    assert row["authorization_code_hash"] == hashlib.sha256(
        b"authorization-code-0001"
    ).hexdigest()
    assert row["acceptance_token_hash"] == hashlib.sha256(
        challenge.acceptance_token.encode("utf-8")
    ).hexdigest()
    assert challenge.acceptance_token not in row["verification_result_encrypted"]
    assert provider.exchange_count == 1

    disclosed_result = OIDCVerificationResult(
        identity=challenge.identity,
        id_token_hash=challenge.id_token_hash,
        expires_at=challenge.expires_at,
    )
    possession_fields = InternalInvitationService.candidate_possession_fields(
        transaction,
        disclosed_result,
    )
    signature = candidate_key.sign(INTERNAL_INVITATION_POP_PURPOSE, possession_fields)
    verify_signature(
        transaction.candidate_public_key_pem,
        INTERNAL_INVITATION_POP_PURPOSE,
        possession_fields,
        signature,
    )

    accepted = _accept(coordinator, transaction, canonical, challenge)
    assert accepted == disclosed_result
    assert store.fetch_one(
        "SELECT status FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )["status"] == "consumed"
    assert store.fetch_one("SELECT COUNT(*) AS count FROM replay_nonces")["count"] == 3
    with pytest.raises(ReplayError, match="already consumed"):
        _accept(coordinator, transaction, canonical, challenge)


def test_strict_callback_and_wrong_acceptance_secret_never_burn_valid_state(invitation_oidc) -> None:
    store, _clock, transaction, canonical, _candidate, _provider, coordinator = invitation_oidc
    authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
    wrong = canonical.replace(b"invited-subject", b"attacker-subject", 1)
    with pytest.raises((AuthenticationError, ValidationError)):
        coordinator.complete_authorization(
            canonical_invitation=wrong,
            evidence={"state": authorization.state, "code": "authorization-code-0002"},
        )
    with pytest.raises(AuthenticationError, match="only state and code"):
        coordinator.complete_authorization(
            canonical_invitation=canonical,
            evidence={
                "state": authorization.state,
                "code": "authorization-code-0002",
                "subject": "caller-asserted",
            },
        )
    assert store.fetch_one(
        "SELECT status FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )["status"] == "pending"

    challenge = _complete(
        coordinator,
        canonical,
        authorization,
        code="authorization-code-0002",
    )
    with pytest.raises(AuthenticationError, match="acceptance token"):
        _accept(coordinator, transaction, canonical, challenge, token="x" * 43)
    with pytest.raises(AuthenticationError, match="unavailable"):
        coordinator.verify_invitation_identity(
            canonical_invitation=canonical,
            evidence={
                "transaction_id": "00000000-0000-0000-0000-000000000000",
                "acceptance_token": challenge.acceptance_token,
            },
            expected_issuer=transaction.invited_oidc_issuer,
            when=NOW,
        )
    assert store.fetch_one(
        "SELECT status FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )["status"] == "verified"
    assert _accept(coordinator, transaction, canonical, challenge).identity.subject == "invited-subject"


def test_completion_is_idempotent_after_response_loss_without_reexchange(invitation_oidc) -> None:
    _store, _clock, transaction, canonical, _candidate, provider, coordinator = invitation_oidc
    authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
    first = _complete(coordinator, canonical, authorization, code="authorization-code-idempotent")
    retried = _complete(coordinator, canonical, authorization, code="authorization-code-idempotent")
    assert retried == first
    assert provider.exchange_count == 1


def test_coordinator_uses_backend_neutral_store_contract(invitation_oidc) -> None:
    store, _clock, transaction, canonical, _candidate, provider, _coordinator = invitation_oidc
    coordinator = InternalInvitationOIDCCoordinator(DelegatingBackendContract(store), provider)
    authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
    challenge = _complete(coordinator, canonical, authorization, code="authorization-code-backend")
    assert _accept(coordinator, transaction, canonical, challenge).identity.subject == "invited-subject"


def test_revocation_expiry_and_provider_drift_fail_closed(invitation_oidc) -> None:
    store, clock, transaction, canonical, _candidate, _provider, coordinator = invitation_oidc
    authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
    with store.transaction() as connection:
        connection.execute(
            """
            UPDATE internal_invitations
               SET state='revoked',revision=2,revoked_at=?,updated_at=?
             WHERE invitation_id=?
            """,
            (clock(), clock(), transaction.invitation_id),
        )
    with pytest.raises(AuthenticationError, match="revision changed|not active"):
        _complete(coordinator, canonical, authorization)

    with store.transaction() as connection:
        connection.execute(
            """
            UPDATE internal_invitations
               SET state='active',revision=1,revoked_at=NULL,updated_at=?
             WHERE invitation_id=?
            """,
            (clock(), transaction.invitation_id),
        )
    expiring = coordinator.begin_authorization(transaction.invitation_id, canonical)
    clock.value += 61
    with pytest.raises(AuthenticationError, match="expired"):
        _complete(coordinator, canonical, expiring, code="authorization-code-expired")
    assert store.fetch_one(
        "SELECT status FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (expiring.transaction_id,),
    )["status"] == "failed"

    clock.value = int(NOW.timestamp())
    current = coordinator.begin_authorization(transaction.invitation_id, canonical)
    drifted = InternalInvitationOIDCCoordinator(
        store,
        DeterministicOIDCProvider(clock, client_id="rotated-client"),
    )
    with pytest.raises(AuthenticationError, match="provider binding"):
        _complete(drifted, canonical, current, code="authorization-code-drift")


def test_token_endpoint_auth_method_is_bound_without_secret_material(invitation_oidc) -> None:
    store, clock, transaction, canonical, _candidate, _provider, coordinator = invitation_oidc
    authorization = coordinator.begin_authorization(transaction.invitation_id, canonical)
    confidential = InternalInvitationOIDCCoordinator(
        store,
        DeterministicOIDCProvider(
            clock,
            token_endpoint_auth_method=OIDCTokenEndpointAuthMethod.CLIENT_SECRET_BASIC,
            client_secret="synthetic-runtime-secret",
        ),
    )

    assert confidential.verifier_id != coordinator.verifier_id
    assert "synthetic-runtime-secret" not in confidential.verifier_id
    with pytest.raises(AuthenticationError, match="provider binding"):
        _complete(
            confidential,
            canonical,
            authorization,
            code="authorization-code-auth-method-drift",
        )
    assert store.fetch_one(
        "SELECT status FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (authorization.transaction_id,),
    )["status"] == "pending"


def test_code_token_and_concurrent_acceptance_replays_are_fenced(invitation_oidc) -> None:
    store, _clock, transaction, canonical, _candidate, provider, coordinator = invitation_oidc
    first = coordinator.begin_authorization(transaction.invitation_id, canonical)
    _complete(coordinator, canonical, first, code="authorization-code-replay")

    code_replay = coordinator.begin_authorization(transaction.invitation_id, canonical)
    with pytest.raises(ReplayError, match="code"):
        _complete(coordinator, canonical, code_replay, code="authorization-code-replay")
    assert provider.exchange_count == 1

    token_hash = hashlib.sha256(b"fixed-id-token").hexdigest()
    provider.forced_token_hash = token_hash
    token_first = coordinator.begin_authorization(transaction.invitation_id, canonical)
    _complete(coordinator, canonical, token_first, code="authorization-code-token-a")
    token_replay = coordinator.begin_authorization(transaction.invitation_id, canonical)
    with pytest.raises(ReplayError, match="code or ID token"):
        _complete(coordinator, canonical, token_replay, code="authorization-code-token-b")

    provider.forced_token_hash = None
    racing = coordinator.begin_authorization(transaction.invitation_id, canonical)
    challenge = _complete(coordinator, canonical, racing, code="authorization-code-race")

    def attempt():
        try:
            return _accept(coordinator, transaction, canonical, challenge)
        except Exception as exc:  # retained for exact result accounting
            return exc

    with ThreadPoolExecutor(max_workers=2) as executor:
        outcomes = list(executor.map(lambda _index: attempt(), range(2)))
    assert sum(isinstance(value, OIDCVerificationResult) for value in outcomes) == 1
    assert sum(isinstance(value, ReplayError) for value in outcomes) == 1
    assert store.fetch_one(
        "SELECT status FROM internal_invitation_oidc_transactions WHERE transaction_id=?",
        (racing.transaction_id,),
    )["status"] == "consumed"
