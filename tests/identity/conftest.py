from __future__ import annotations

from dataclasses import dataclass

import pytest

from agentnet.approval.service import LocalLabApprovalVerifier
from agentnet.identity.domains import DomainRegistry
from agentnet.identity.enrollment import (
    EnrollmentChallenge,
    EnrollmentResult,
    EnrollmentService,
    VerifiedOIDCIdentity,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.sqlite import SQLiteStore


class MutableClock:
    def __init__(self, value: int = 2_000_000_000) -> None:
        self.value = value

    def __call__(self) -> int:
        return self.value


@dataclass
class IdentityStack:
    store: SQLiteStore
    clock: MutableClock
    verifier: LocalLabApprovalVerifier
    enrollment: EnrollmentService
    identity: VerifiedOIDCIdentity

    def begin(self, key: P256KeyPair, *, name: str = "Codex workstation") -> EnrollmentChallenge:
        return self.enrollment.begin(
            domain_id="corp.example",
            identity=self.identity,
            harness_kind="codex",
            harness_name=name,
            public_key_pem=key.public_pem,
        )

    def complete(
        self,
        key: P256KeyPair,
        challenge: EnrollmentChallenge,
    ) -> EnrollmentResult:
        approval = self.verifier.approve(canonical_transaction=challenge.canonical_transaction)
        possession = key.sign("agentnet.enrollment.pop.v1", challenge.signed_fields())
        return self.enrollment.complete(
            challenge_id=challenge.challenge_id,
            nonce=challenge.nonce,
            canonical_transaction=challenge.canonical_transaction,
            possession_signature=possession,
            approval=approval,
        )

    def enroll(self, key: P256KeyPair, *, name: str = "Codex workstation") -> EnrollmentResult:
        return self.complete(key, self.begin(key, name=name))


@pytest.fixture
def identity_stack(tmp_path: object) -> IdentityStack:
    path = tmp_path / "identity.sqlite3"  # type: ignore[operator]
    store = SQLiteStore(path, LocalEnvelopeCipher(b"I" * 32))
    clock = MutableClock()
    DomainRegistry(store).register("corp.example", now=clock())
    verifier = LocalLabApprovalVerifier(P256KeyPair.generate(), clock=clock)
    enrollment = EnrollmentService(
        store,
        verifier,
        challenge_ttl=300,
        credential_ttl=3600,
        clock=clock,
    )
    stack = IdentityStack(
        store=store,
        clock=clock,
        verifier=verifier,
        enrollment=enrollment,
        identity=VerifiedOIDCIdentity(
            issuer="https://id.corp.example",
            subject="workforce-user-123",
            verified_email="person@corp.example",
        ),
    )
    try:
        yield stack
    finally:
        store.close()
