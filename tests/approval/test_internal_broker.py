from __future__ import annotations

import json

import pytest

from agentnet.approval.internal_broker import (
    INTERNAL_BROKER_ALGORITHM,
    INTERNAL_BROKER_PROOF_HEADER,
    INTERNAL_BROKER_PURPOSE_CREATE,
    build_internal_broker_proof,
    verify_internal_broker_proof,
)
from agentnet.errors import AuthenticationError
from agentnet.security.signatures import b64url_decode


CREDENTIAL = "S" * 43
AUDIENCE = "https://approval.corp.example"
PATH = "/v1/approval/internal/requests"
BODY = b'{"schema":"agentnet.approval.internal-request-create.v1"}'
NOW = 1_800_000_000
NONCE = b"n" * 32


def _build(**overrides: object) -> str:
    arguments: dict[str, object] = {
        "credential": CREDENTIAL,
        "audience": AUDIENCE,
        "method": "POST",
        "path": PATH,
        "purpose": INTERNAL_BROKER_PURPOSE_CREATE,
        "raw_body": BODY,
        "now": NOW,
        "nonce": NONCE,
    }
    arguments.update(overrides)
    return build_internal_broker_proof(**arguments)  # type: ignore[arg-type]


def _verify(proof: str, **overrides: object):
    arguments: dict[str, object] = {
        "credential": CREDENTIAL,
        "header_value": proof,
        "audience": AUDIENCE,
        "method": "POST",
        "path": PATH,
        "purpose": INTERNAL_BROKER_PURPOSE_CREATE,
        "raw_body": BODY,
        "now": NOW,
    }
    arguments.update(overrides)
    return verify_internal_broker_proof(**arguments)  # type: ignore[arg-type]


def test_broker_proof_is_deterministic_fixed_schema_and_verifiable() -> None:
    first = _build()
    second = _build()
    assert first == second
    assert len(first) < 4096

    wire = json.loads(b64url_decode(first))
    assert set(wire) == {
        "alg",
        "audience",
        "body_sha256",
        "expires_at",
        "issued_at",
        "key_id",
        "method",
        "nonce",
        "path",
        "purpose",
        "schema",
        "signature",
    }
    assert wire["alg"] == INTERNAL_BROKER_ALGORITHM
    assert wire["nonce"] == "bm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm4"

    verified = _verify(first)
    assert verified.key_id == wire["key_id"]
    assert verified.nonce == wire["nonce"]
    assert verified.issued_at == NOW
    assert verified.expires_at == NOW + 30
    assert verified.purpose == INTERNAL_BROKER_PURPOSE_CREATE
    assert INTERNAL_BROKER_PROOF_HEADER.lower() == "x-agentnet-approval-broker-proof"


@pytest.mark.parametrize(
    ("field", "value"),
    (
        ("raw_body", BODY + b" "),
        ("audience", "https://other.example"),
        ("method", "GET"),
        ("path", "/v1/approval/internal/requests/status"),
        ("purpose", "agentnet.approval.internal-broker.status.v1"),
        ("credential", "T" * 43),
    ),
)
def test_broker_proof_binds_request_and_credential(field: str, value: object) -> None:
    proof = _build()
    with pytest.raises(AuthenticationError, match="approval request denied"):
        _verify(proof, **{field: value})


def test_broker_proof_rejects_tamper_and_noncanonical_encoding() -> None:
    proof = _build()
    wire = json.loads(b64url_decode(proof))
    wire["signature"] = "A" * 43
    from agentnet.security.signatures import b64url_encode, canonical_json

    tampered = b64url_encode(canonical_json(wire))
    with pytest.raises(AuthenticationError, match="approval request denied"):
        _verify(tampered)

    padded = proof + "="
    with pytest.raises(AuthenticationError, match="approval request denied"):
        _verify(padded)


@pytest.mark.parametrize(
    ("issued_at", "verify_now", "accepted"),
    (
        (NOW + 5, NOW, True),
        (NOW + 6, NOW, False),
        (NOW - 29, NOW, True),
        (NOW - 30, NOW, False),
    ),
)
def test_broker_proof_enforces_exact_clock_boundaries(
    issued_at: int,
    verify_now: int,
    accepted: bool,
) -> None:
    proof = _build(now=issued_at)
    if accepted:
        assert _verify(proof, now=verify_now).issued_at == issued_at
    else:
        with pytest.raises(AuthenticationError, match="approval request denied"):
            _verify(proof, now=verify_now)


def test_broker_proof_rejects_negative_timestamps_and_unknown_fields() -> None:
    proof = _build()
    wire = json.loads(b64url_decode(proof))
    from agentnet.security.signatures import b64url_encode, canonical_json

    for changed in (
        {**wire, "issued_at": -1},
        {**wire, "expires_at": -1},
        {**wire, "unknown_critical_field": "deny"},
    ):
        malformed = b64url_encode(canonical_json(changed))
        with pytest.raises(AuthenticationError, match="approval request denied"):
            _verify(malformed)


def test_broker_proof_rejects_bool_timestamps_and_noncanonical_nonce() -> None:
    proof = _build()
    wire = json.loads(b64url_decode(proof))
    from agentnet.security.signatures import b64url_encode, canonical_json

    for field, value in (("issued_at", True), ("expires_at", False), ("nonce", "A" * 43)):
        changed = dict(wire)
        changed[field] = value
        malformed = b64url_encode(canonical_json(changed))
        with pytest.raises(AuthenticationError, match="approval request denied"):
            _verify(malformed)
