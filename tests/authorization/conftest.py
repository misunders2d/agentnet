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
def actor_key() -> P256KeyPair:
    return P256KeyPair.generate()


@pytest.fixture
def store(tmp_path, now: datetime, actor_key: P256KeyPair):
    database = SQLiteStore(tmp_path / "authorization.db", LocalEnvelopeCipher(b"a" * 32))
    epoch = int(now.timestamp())
    with database.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) VALUES(?,?,?,?,?)",
            ("domain-a", "active", 1, 1, epoch - 100),
        )
        connection.execute(
            """
            INSERT INTO principals(
                principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
            ) VALUES(?,?,?,?,?,?,?)
            """,
            ("human-a", "domain-a", "https://idp.example", "subject-a", "a@example.test", "active", epoch - 100),
        )
        for principal_id in ("admin-human", "admin-2"):
            connection.execute(
                """
                INSERT INTO principals(
                    principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                ) VALUES(?,?,?,?,?,?,?)
                """,
                (
                    principal_id,
                    "domain-a",
                    "https://idp.example",
                    principal_id,
                    f"{principal_id}@example.test",
                    "active",
                    epoch - 100,
                ),
            )
        connection.execute(
            """
            INSERT INTO harnesses(
                harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                binding_assurance,capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,?,?,?,?,?,?)
            """,
            ("harness-a", "domain-a", "human-a", None, "codex", "Codex A", "active", "os_bound", "{}", 1, epoch - 100),
        )
        connection.execute(
            """
            INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,?,?,?,?)
            """,
            (
                "credential-a",
                "harness-a",
                actor_key.thumbprint,
                actor_key.public_pem,
                "active",
                1,
                epoch - 100,
                epoch + 86400,
            ),
        )
    try:
        yield database
    finally:
        database.close()


@pytest.fixture
def actor() -> VerifiedActor:
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="domain-a",
        principal_id="human-a",
        harness_id="harness-a",
        credential_id="credential-a",
        credential_epoch=1,
        binding_assurance="os_bound",
    )


@pytest.fixture
def future(now: datetime) -> datetime:
    return now + timedelta(hours=1)


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


@pytest.fixture
def approval_signers():
    return {
        "admin-key-1": P256KeyPair.generate(),
        "admin-key-2": P256KeyPair.generate(),
    }


@pytest.fixture
def trusted_approvers(approval_signers):
    purpose = frozenset({"authorization.elevation.approve"})
    return {
        "admin-key-1": TrustedApprover(
            principal_id="admin-human",
            domain_id="domain-a",
            signer_key_id="admin-key-1",
            public_key_pem=approval_signers["admin-key-1"].public_pem,
            allowed_purposes=purpose,
        ),
        "admin-key-2": TrustedApprover(
            principal_id="admin-2",
            domain_id="domain-a",
            signer_key_id="admin-key-2",
            public_key_pem=approval_signers["admin-key-2"].public_pem,
            allowed_purposes=purpose,
        ),
    }


@pytest.fixture
def approval_verifier(trusted_approvers):
    return IndependentApprovalVerifier(
        trusted_approvers,
        verifier_id="independent-approval.example",
    )
