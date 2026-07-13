from __future__ import annotations

import time
from dataclasses import replace

import pytest

from agentnet.errors import AuthenticationError, ReplayError, ValidationError
from agentnet.security.dpop import create_request_proof, verify_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json, verify_signature


def test_canonical_signature_is_purpose_bound() -> None:
    key = P256KeyPair.generate()
    value = {"z": 1, "a": "é"}
    signature = key.sign("agentnet.event.origin.v1", value)
    verify_signature(key.public_pem, "agentnet.event.origin.v1", {"a": "é", "z": 1}, signature)
    with pytest.raises(AuthenticationError):
        verify_signature(key.public_pem, "agentnet.receipt.v1", value, signature)
    with pytest.raises(ValidationError):
        key.sign("arbitrary.bytes", value)


def test_request_proof_binds_method_path_body_and_time() -> None:
    key = P256KeyPair.generate()
    body = canonical_json({"hello": "world"})
    proof = create_request_proof(
        key,
        harness_id="h1",
        credential_id="c1",
        domain_id="corp.example",
        audience="urn:agentnet:corp.example:corporate-api",
        method="POST",
        scheme="https",
        authority="api.corp.example",
        path="/v1/events",
        query="view=full",
        body=body,
    )
    verify_request_proof(
        proof,
        public_key_pem=key.public_pem,
        expected_method="POST",
        expected_audience="urn:agentnet:corp.example:corporate-api",
        expected_scheme="https",
        expected_authority="api.corp.example",
        expected_path="/v1/events",
        expected_query="view=full",
        body=body,
        now=int(time.time()),
        max_age=300,
        future_skew=60,
    )
    with pytest.raises(AuthenticationError):
        verify_request_proof(
            proof,
            public_key_pem=key.public_pem,
            expected_method="POST",
            expected_audience="urn:agentnet:corp.example:corporate-api",
            expected_scheme="https",
            expected_authority="api.corp.example",
            expected_path="/v1/effects",
            expected_query="view=full",
            body=body,
            now=int(time.time()),
            max_age=300,
            future_skew=60,
        )


def test_replay_cache_is_persistent_and_one_use(store) -> None:
    store.consume_once("actor", "a-long-enough-random-nonce", expires_at=int(time.time()) + 300)
    with pytest.raises(ReplayError):
        store.consume_once("actor", "a-long-enough-random-nonce", expires_at=int(time.time()) + 300)


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("audience", "urn:agentnet:other-service"),
        ("scheme", "http"),
        ("authority", "other.example"),
        ("path", "/v1/other"),
        ("query", "view=summary"),
    ],
)
def test_request_proof_rejects_every_wrong_target_dimension(field: str, value: str) -> None:
    key = P256KeyPair.generate()
    body = b"payload"
    proof = create_request_proof(
        key,
        harness_id="h1",
        credential_id="c1",
        domain_id="corp.example",
        audience="urn:agentnet:corp.example:corporate-api",
        method="POST",
        scheme="https",
        authority="api.corp.example",
        path="/v1/events",
        query="view=full",
        body=body,
    )
    with pytest.raises(AuthenticationError):
        verify_request_proof(
            replace(proof, **{field: value}),
            public_key_pem=key.public_pem,
            expected_method="POST",
            expected_audience="urn:agentnet:corp.example:corporate-api",
            expected_scheme="https",
            expected_authority="api.corp.example",
            expected_path="/v1/events",
            expected_query="view=full",
            body=body,
            now=int(time.time()),
            max_age=300,
            future_skew=60,
        )


@pytest.mark.parametrize(
    ("path", "query"),
    [
        ("/v1/../effects", ""),
        ("/v1/%2feffects", ""),
        ("/v1/events", "bad=%zz"),
    ],
)
def test_request_proof_creation_rejects_noncanonical_targets(path: str, query: str) -> None:
    with pytest.raises(ValidationError):
        create_request_proof(
            P256KeyPair.generate(),
            harness_id="h1",
            credential_id="c1",
            domain_id="corp.example",
            audience="urn:agentnet:corp.example:corporate-api",
            method="GET",
            scheme="https",
            authority="api.corp.example",
            path=path,
            query=query,
            body=b"",
        )
