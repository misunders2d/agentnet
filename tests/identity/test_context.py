from __future__ import annotations

from dataclasses import replace

import pytest

from agentnet.errors import AuthenticationError, ReplayError
from agentnet.identity.context import VerifiedContextResolver
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.sqlite import SQLiteStore

AUDIENCE = "urn:agentnet:corp.example:corporate-api"
SCHEME = "https"
AUTHORITY = "api.corp.example"


def _resolver(store: SQLiteStore) -> VerifiedContextResolver:
    return VerifiedContextResolver(
        store,
        service_audience=AUDIENCE,
        service_scheme=SCHEME,
        service_authority=AUTHORITY,
    )


def test_resolution_uses_verified_binding_and_ignores_claims(identity_stack: object) -> None:
    key = P256KeyPair.generate()
    enrolled = identity_stack.enroll(key)
    body = b'{"operation":"read-inbox"}'
    proof = create_request_proof(
        key,
        harness_id=enrolled.harness_id,
        credential_id=enrolled.credential_id,
        domain_id="corp.example",
        audience=AUDIENCE,
        method="POST",
        scheme=SCHEME,
        authority=AUTHORITY,
        path="/v1/inbox/read",
        query="",
        body=body,
        timestamp=identity_stack.clock(),
        nonce="fixed-request-nonce-with-enough-entropy-001",
    )
    resolver = _resolver(identity_stack.store)
    context = resolver.resolve(
        proof,
        expected_method="POST",
        expected_scheme=SCHEME,
        expected_authority=AUTHORITY,
        expected_path="/v1/inbox/read",
        expected_query="",
        body=body,
        now=identity_stack.clock(),
        caller_claims={
            "principal_id": "attacker",
            "harness_id": "copied-harness",
            "role": "administrator",
            "email": "attacker@example.test",
        },
    )

    assert context.actor.principal_id == enrolled.principal_id
    assert context.actor.harness_id == enrolled.harness_id
    assert context.actor.credential_id == enrolled.credential_id
    assert context.actor.binding_assurance == "lab"


def test_replay_consumption_persists_across_store_instances(identity_stack: object) -> None:
    key = P256KeyPair.generate()
    enrolled = identity_stack.enroll(key)
    proof = create_request_proof(
        key,
        harness_id=enrolled.harness_id,
        credential_id=enrolled.credential_id,
        domain_id="corp.example",
        audience=AUDIENCE,
        method="GET",
        scheme=SCHEME,
        authority=AUTHORITY,
        path="/v1/status",
        query="",
        body=b"",
        timestamp=identity_stack.clock(),
        nonce="persistent-replay-nonce-with-enough-entropy",
    )
    _resolver(identity_stack.store).resolve(
        proof,
        expected_method="GET",
        expected_scheme=SCHEME,
        expected_authority=AUTHORITY,
        expected_path="/v1/status",
        expected_query="",
        body=b"",
        now=identity_stack.clock(),
    )

    second = SQLiteStore(identity_stack.store.path, identity_stack.store.cipher)
    try:
        with pytest.raises(ReplayError):
            _resolver(second).resolve(
                proof,
                expected_method="GET",
                expected_scheme=SCHEME,
                expected_authority=AUTHORITY,
                expected_path="/v1/status",
                expected_query="",
                body=b"",
                now=identity_stack.clock(),
            )
    finally:
        second.close()


def test_failed_body_or_binding_check_does_not_resolve_actor(identity_stack: object) -> None:
    key = P256KeyPair.generate()
    enrolled = identity_stack.enroll(key)
    proof = create_request_proof(
        key,
        harness_id=enrolled.harness_id,
        credential_id=enrolled.credential_id,
        domain_id="corp.example",
        audience=AUDIENCE,
        method="POST",
        scheme=SCHEME,
        authority=AUTHORITY,
        path="/v1/messages",
        query="",
        body=b"correct",
        timestamp=identity_stack.clock(),
        nonce="body-binding-nonce-with-enough-entropy-001",
    )
    resolver = _resolver(identity_stack.store)
    with pytest.raises(AuthenticationError):
        resolver.resolve(
            proof,
            expected_method="POST",
            expected_scheme=SCHEME,
            expected_authority=AUTHORITY,
            expected_path="/v1/messages",
            expected_query="",
            body=b"tampered",
            now=identity_stack.clock(),
        )

    copied = replace(proof, harness_id="copied-harness")
    with pytest.raises(AuthenticationError):
        resolver.resolve(
            copied,
            expected_method="POST",
            expected_scheme=SCHEME,
            expected_authority=AUTHORITY,
            expected_path="/v1/messages",
            expected_query="",
            body=b"correct",
            now=identity_stack.clock(),
        )

    context = resolver.resolve(
        proof,
        expected_method="POST",
        expected_scheme=SCHEME,
        expected_authority=AUTHORITY,
        expected_path="/v1/messages",
        expected_query="",
        body=b"correct",
        now=identity_stack.clock(),
    )
    assert context.actor.principal_id == enrolled.principal_id
