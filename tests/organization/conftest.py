from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest

from agentnet.approval import IndependentApprovalVerifier, TrustedApprover
from agentnet.authorization import AUTHORITY_COMMAND_PURPOSE, SignedAuthorityCommand
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_digest
from agentnet.storage.sqlite import SQLiteStore


@pytest.fixture
def now() -> datetime:
    return datetime(2026, 7, 12, 12, 0, tzinfo=UTC)


@pytest.fixture
def identity_keys() -> dict[str, P256KeyPair]:
    return {
        "admin-credential": P256KeyPair.generate(),
        "sub-credential": P256KeyPair.generate(),
        "peer-credential": P256KeyPair.generate(),
    }


@pytest.fixture
def relationship_approval_keys() -> dict[str, P256KeyPair]:
    return {
        "sub-human": P256KeyPair.generate(),
        "peer-human": P256KeyPair.generate(),
    }


@pytest.fixture
def relationship_approval_verifier(
    relationship_approval_keys: dict[str, P256KeyPair],
) -> IndependentApprovalVerifier:
    purpose = "organization.relationship.accept"
    trusted = {
        key.thumbprint: TrustedApprover(
            principal_id=principal_id,
            domain_id="domain-a",
            signer_key_id=key.thumbprint,
            public_key_pem=key.public_pem,
            allowed_purposes=frozenset({purpose}),
        )
        for principal_id, key in relationship_approval_keys.items()
    }
    return IndependentApprovalVerifier(
        trusted,
        verifier_id="organization-owner-approval.example",
    )


@pytest.fixture
def store(tmp_path, now: datetime, identity_keys: dict[str, P256KeyPair]):
    database = SQLiteStore(tmp_path / "organization.db", LocalEnvelopeCipher(b"o" * 32))
    epoch = int(now.timestamp())
    identities = (
        ("admin-human", "admin-harness", "admin-credential", "admin@example.test"),
        ("sub-human", "sub-harness", "sub-credential", "sub@example.test"),
        ("peer-human", "peer-harness", "peer-credential", "peer@example.test"),
    )
    with database.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("domain-a", "active", 1, 1, epoch - 100),
        )
        for principal_id, harness_id, credential_id, email in identities:
            connection.execute(
                """
                INSERT INTO principals(
                    principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                ) VALUES(?,?,?,?,?,?,?)
                """,
                (principal_id, "domain-a", "https://idp.example", principal_id, email, "active", epoch - 100),
            )
            connection.execute(
                """
                INSERT INTO harnesses(
                    harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                    binding_assurance,capabilities_json,credential_epoch,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
                """,
                (harness_id, "domain-a", principal_id, None, "codex", harness_id, "active", "os_bound", "{}", 1, epoch - 100),
            )
            connection.execute(
                """
                INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,?,?,?,?)
                """,
                (
                    credential_id,
                    harness_id,
                    identity_keys[credential_id].thumbprint,
                    identity_keys[credential_id].public_pem,
                    "active",
                    1,
                    epoch - 100,
                    epoch + 172800,
                ),
            )
    try:
        yield database
    finally:
        database.close()


def make_actor(principal_id: str, harness_id: str, credential_id: str) -> VerifiedActor:
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="domain-a",
        principal_id=principal_id,
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="os_bound",
    )


@pytest.fixture
def admin_actor() -> VerifiedActor:
    return make_actor("admin-human", "admin-harness", "admin-credential")


@pytest.fixture
def subordinate_actor() -> VerifiedActor:
    return make_actor("sub-human", "sub-harness", "sub-credential")


@pytest.fixture
def peer_actor() -> VerifiedActor:
    return make_actor("peer-human", "peer-harness", "peer-credential")


@pytest.fixture
def signed_command():
    def create(
        *,
        key: P256KeyPair,
        actor: VerifiedActor,
        action: str,
        resource: str,
        request: dict[str, object],
        now: datetime,
        entity_revision: int,
        reason: str = "authorized test mutation",
        policy_revision: int = 1,
    ) -> SignedAuthorityCommand:
        fields = SignedAuthorityCommand.signing_fields(
            command_id=str(uuid4()),
            actor=actor,
            action=action,
            resource=resource,
            request_digest=canonical_digest(request),
            expected_policy_revision=policy_revision,
            expected_entity_revision=entity_revision,
            reason=reason,
            issued_at=now,
            expires_at=now + timedelta(minutes=2),
        )
        return SignedAuthorityCommand(
            **fields,
            signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
        )

    return create
