from __future__ import annotations

from contextlib import contextmanager
from pathlib import Path

import pytest

from agentnet.bindings.ipc import (
    IPCSessionClaims,
    IPCSessionVerifier,
    build_ipc_frame,
    mint_inherited_session_capability,
)
from agentnet.errors import AuthenticationError, ReplayError, ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.storage.sqlite import SQLiteStore


NOW = 1_800_000_000
ROOT = b"r" * 32
MEASUREMENT = "sha256:" + "a" * 64


def claims(**updates: object) -> IPCSessionClaims:
    value: dict[str, object] = {
        "schema": "agentnet.ipc.session.v1",
        "capability_id": "capability-id-with-enough-entropy-001",
        "harness_id": "harness-bound-to-ipc-session-001",
        "credential_id": "credential-bound-to-ipc-session-001",
        "credential_epoch": 1,
        "allowed_methods": ["agentnet.inbox", "agentnet.send"],
        "uid": 1000,
        "pid": 4242,
        "process_start_time": "987654321",
        "process_measurement": MEASUREMENT,
        "session_id": "session-with-enough-entropy-001",
        "issued_at": NOW - 1,
        "expires_at": NOW + 300,
    }
    value.update(updates)
    return IPCSessionClaims.model_validate(value)


def frame(token: str, *, nonce: str = "nonce-with-enough-entropy-value-001", request=None, session_id=None):
    return build_ipc_frame(
        token,
        session_id=session_id or claims().session_id,
        nonce=nonce,
        request=request or {"method": "mailbox.read"},
    )


def verify(verifier: IPCSessionVerifier, value: dict[str, object], **updates: object):
    inputs: dict[str, object] = {
        "peer_uid": 1000,
        "peer_pid": 4242,
        "process_start_time": "987654321",
        "process_measurement": MEASUREMENT,
    }
    inputs.update(updates)
    return verifier.verify(value, **inputs)  # type: ignore[arg-type]


def test_inherited_capability_is_exact_process_session_bound_and_one_use(store) -> None:
    token = mint_inherited_session_capability(ROOT, claims())
    value = frame(token)
    verifier = IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW)
    assert verify(verifier, value) == {"method": "mailbox.read"}
    with pytest.raises(ReplayError):
        verify(verifier, value)


@pytest.mark.parametrize(
    ("field", "wrong"),
    [
        ("peer_uid", 1001),
        ("peer_pid", 4243),
        ("process_start_time", "987654322"),
        ("process_measurement", "sha256:" + "b" * 64),
    ],
)
def test_copied_capability_rejected_for_wrong_process_binding(field: str, wrong: object, store) -> None:
    token = mint_inherited_session_capability(ROOT, claims())
    with pytest.raises(AuthenticationError, match="binding"):
        verify(IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW), frame(token), **{field: wrong})


def test_session_tamper_and_request_tamper_fail_closed(store) -> None:
    token = mint_inherited_session_capability(ROOT, claims())
    wrong_session = frame(token, session_id="different-session-with-entropy-001")
    with pytest.raises(AuthenticationError, match="binding"):
        verify(IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW), wrong_session)

    tampered = frame(token)
    tampered["request"] = {"method": "effect.execute"}
    with pytest.raises(AuthenticationError, match="authenticator"):
        verify(IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW), tampered)


def test_expired_or_wrong_root_capability_and_short_nonce_are_rejected(store) -> None:
    expired = mint_inherited_session_capability(ROOT, claims(expires_at=NOW))
    with pytest.raises(AuthenticationError, match="validity"):
        verify(IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW), frame(expired))

    token = mint_inherited_session_capability(ROOT, claims())
    with pytest.raises(AuthenticationError):
        verify(IPCSessionVerifier(b"x" * 32, replay_store=store, clock=lambda: NOW), frame(token))
    with pytest.raises(ValidationError, match="nonce"):
        verify(IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW), frame(token, nonce="short"))


def test_exact_frame_schema_rejects_unknown_fields(store) -> None:
    token = mint_inherited_session_capability(ROOT, claims())
    value = frame(token)
    value["claimed_uid"] = 1000
    with pytest.raises(ValidationError, match="schema"):
        verify(IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW), value)


def test_replay_fence_survives_store_and_verifier_restart(tmp_path: Path) -> None:
    database_path = tmp_path / "ipc-replay.sqlite3"
    cipher = LocalEnvelopeCipher(b"i" * 32)
    token = mint_inherited_session_capability(ROOT, claims())
    value = frame(token, nonce="restart-persistent-nonce-with-entropy-001")

    first_store = SQLiteStore(database_path, cipher)
    try:
        first = IPCSessionVerifier(ROOT, replay_store=first_store, clock=lambda: NOW)
        assert verify(first, value) == {"method": "mailbox.read"}
    finally:
        first_store.close()

    second_store = SQLiteStore(database_path, cipher)
    try:
        second = IPCSessionVerifier(ROOT, replay_store=second_store, clock=lambda: NOW)
        with pytest.raises(ReplayError, match="already consumed"):
            verify(second, value)
        persisted = second_store.fetch_one(
            """SELECT capability_id,peer_uid,peer_pid,process_start_time,
                      process_measurement,session_id
                 FROM ipc_replay_fences"""
        )
        assert dict(persisted) == {
            "capability_id": claims().capability_id,
            "peer_uid": claims().uid,
            "peer_pid": claims().pid,
            "process_start_time": claims().process_start_time,
            "process_measurement": claims().process_measurement,
            "session_id": claims().session_id,
        }
    finally:
        second_store.close()


def test_replay_fence_namespace_is_bound_to_session_and_root_key(store) -> None:
    nonce = "context-bound-nonce-with-enough-entropy-001"
    first_claims = claims()
    first_token = mint_inherited_session_capability(ROOT, first_claims)
    first = IPCSessionVerifier(ROOT, replay_store=store, clock=lambda: NOW)
    assert verify(first, frame(first_token, nonce=nonce)) == {"method": "mailbox.read"}

    second_claims = claims(session_id="second-session-with-enough-entropy-001")
    second_token = mint_inherited_session_capability(ROOT, second_claims)
    assert verify(
        first,
        frame(second_token, nonce=nonce, session_id=second_claims.session_id),
    ) == {"method": "mailbox.read"}

    rotated_root = b"k" * 32
    rotated_token = mint_inherited_session_capability(rotated_root, first_claims)
    rotated = IPCSessionVerifier(rotated_root, replay_store=store, clock=lambda: NOW)
    assert verify(rotated, frame(rotated_token, nonce=nonce)) == {"method": "mailbox.read"}

    rows = store.fetch_all(
        "SELECT context_digest,root_key_id,session_id FROM ipc_replay_fences ORDER BY session_id,root_key_id"
    )
    assert len(rows) == 3
    assert len({row["context_digest"] for row in rows}) == 3
    assert len({row["root_key_id"] for row in rows}) == 2
    assert len({row["session_id"] for row in rows}) == 2


def test_postgresql_unique_violation_is_normalized_to_replay_error() -> None:
    class UniqueViolation(Exception):
        pass

    class RejectingConnection:
        def execute(self, query, parameters=()):
            if query.lstrip().startswith("INSERT INTO ipc_replay_fences"):
                raise UniqueViolation("duplicate replay fence")
            return self

    class RejectingStore:
        @contextmanager
        def transaction(self):
            yield RejectingConnection()

    token = mint_inherited_session_capability(ROOT, claims())
    verifier = IPCSessionVerifier(ROOT, replay_store=RejectingStore(), clock=lambda: NOW)
    with pytest.raises(ReplayError, match="already consumed"):
        verify(verifier, frame(token))
