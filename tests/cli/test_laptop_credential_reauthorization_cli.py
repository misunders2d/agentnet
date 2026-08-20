from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
from uuid import UUID, uuid4

import pytest

import agentnet.cli as cli
from agentnet.cli.commands import auth
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import P256KeyPair


NOW = 1_800_000_000


class Response:
    def __init__(self, status_code: int, value: dict[str, object]) -> None:
        self.status_code = status_code
        self._value = value

    def json(self) -> dict[str, object]:
        return self._value


class Client:
    def __init__(
        self,
        *,
        transaction: dict[str, object],
        progress: list[Response | Exception],
        prepare: Response | Exception | None = None,
    ) -> None:
        self.transaction = transaction
        self.progress_results = list(progress)
        self.prepare_result = prepare
        self.prepare_calls: list[dict[str, str]] = []
        self.progress_calls: list[dict[str, object]] = []
        self.closed = False

    def prepare_expired_current_credential_reauthorization(
        self,
        *,
        request_id: str,
        identity_profile_sha256: str,
    ) -> Response:
        self.prepare_calls.append(
            {
                "request_id": request_id,
                "identity_profile_sha256": identity_profile_sha256,
            }
        )
        if isinstance(self.prepare_result, Exception):
            raise self.prepare_result
        return self.prepare_result or Response(200, self.transaction)

    def progress_expired_current_credential_reauthorization(
        self,
        *,
        transaction: dict[str, object],
        old_key_possession_signature: str,
        possession_secret: str,
    ) -> Response:
        self.progress_calls.append(
            {
                "transaction": transaction,
                "old_key_possession_signature": old_key_possession_signature,
                "possession_secret": possession_secret,
            }
        )
        result = self.progress_results.pop(0)
        if isinstance(result, Exception):
            raise result
        return result

    def close(self) -> None:
        self.closed = True


def _profile(tmp_path: Path) -> tuple[Path, Path, bytes, VerifiedActor, P256KeyPair, bytes]:
    key = P256KeyPair.generate()
    key_path = (tmp_path / "identity.key.pem").resolve()
    key_raw = key.private_pem
    key_path.write_bytes(key_raw)
    key_path.chmod(0o600)
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="principal-owner",
        harness_id="harness-laptop",
        credential_id="credential-expired",
        credential_epoch=7,
        binding_assurance="os_bound",
    )
    identity_path = (tmp_path / "identity.json").resolve()
    identity = {
        "schema": "agentnet.identity-profile.v1",
        "server_base_url": "https://core.example",
        "audience": "https://core.example",
        "actor": actor.model_dump(mode="json"),
        "private_key_path": str(key_path),
    }
    identity_raw = json.dumps(identity, indent=2, sort_keys=True).encode("utf-8") + b"\n"
    identity_path.write_bytes(identity_raw)
    identity_path.chmod(0o600)
    return identity_path, key_path, key_raw, actor, key, identity_raw


def _transaction(
    *,
    actor: VerifiedActor,
    key: P256KeyPair,
    identity_raw: bytes,
    request_id: str,
) -> dict[str, object]:
    return {
        "schema": "agentnet.laptop-credential-reauthorization.v1",
        "approval_purpose": "identity.credential.recover.approve",
        "request_id": request_id,
        "domain_id": actor.domain_id,
        "principal_id": actor.principal_id,
        "harness_id": actor.harness_id,
        "expired_credential_id": actor.credential_id,
        "expected_credential_epoch": actor.credential_epoch,
        "successor_credential_epoch": actor.credential_epoch + 1,
        "expected_expired_at": NOW - 60,
        "expected_key_id": key.thumbprint,
        "expected_public_key_sha256": hashlib.sha256(key.public_pem.encode("utf-8")).hexdigest(),
        "expected_binding_assurance": actor.binding_assurance,
        "identity_profile_sha256": hashlib.sha256(identity_raw).hexdigest(),
        "prepared_at": NOW,
        "expires_at": NOW + 300,
        "maximum_new_credential_ttl_seconds": 86_400,
        "key_binding": "same_laptop_key_with_fresh_possession_proof",
        "old_credential_action": "retire_without_extension",
        "key_preserved": True,
        "authority_granted": False,
    }


def _completed(transaction: dict[str, object], key: P256KeyPair) -> Response:
    return Response(
        200,
        {
            "schema": "agentnet.laptop-credential-reauthorization-result.v1",
            "status": "current",
            "request_id": transaction["request_id"],
            "domain_id": transaction["domain_id"],
            "principal_id": transaction["principal_id"],
            "harness_id": transaction["harness_id"],
            "previous_credential_id": transaction["expired_credential_id"],
            "credential_id": "credential-successor",
            "key_id": key.thumbprint,
            "credential_epoch": transaction["successor_credential_epoch"],
            "not_before": NOW,
            "expires_at": NOW + 86_400,
            "idempotent_repeat": False,
            "key_preserved": True,
            "authority_granted": False,
        },
    )


def _args(identity_path: Path, state_path: Path) -> argparse.Namespace:
    return argparse.Namespace(
        identity=str(identity_path),
        state=str(state_path),
        browser="system",
        timeout=30,
    )


def _install_client(monkeypatch: pytest.MonkeyPatch, client: Client) -> None:
    monkeypatch.setattr(auth, "AgentNetClient", lambda **_kwargs: client)
    monkeypatch.setattr(
        auth,
        "uuid4",
        lambda: UUID(str(client.transaction["request_id"])),
    )
    monkeypatch.setattr(auth.time, "time", lambda: NOW)
    monkeypatch.setattr(auth.time, "monotonic", lambda: 10.0)


def test_parser_exposes_exact_expired_laptop_credential_command() -> None:
    parser = cli.build_parser()
    args = parser.parse_args(["credential", "reauthorize-expired"])
    assert args.func is cli.command_credential_reauthorize_expired
    assert args.identity == ".agentnet/identity.json"
    assert args.state == ".agentnet/credential-reauthorization-state.json"
    assert args.browser == "system"
    assert args.timeout == 300
    assert parser.parse_args(
        ["credential", "reauthorize-expired", "--browser", "manual", "--timeout", "30"]
    ).browser == "manual"
    with pytest.raises(SystemExit):
        parser.parse_args(["credential", "reauthorize-expired", "--browser", "remote"])


def test_response_loss_resume_reuses_every_idempotency_value_and_preserves_identity_key(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    identity_path, key_path, key_raw, actor, key, identity_raw = _profile(tmp_path)
    state_path = (tmp_path / "reauthorization-state.json").resolve()
    request_id = str(uuid4())
    transaction = _transaction(
        actor=actor,
        key=key,
        identity_raw=identity_raw,
        request_id=request_id,
    )
    first = Client(transaction=transaction, progress=[RuntimeError("response lost")])
    _install_client(monkeypatch, first)

    with pytest.raises(RuntimeError, match="response lost"):
        cli.command_credential_reauthorize_expired(_args(identity_path, state_path))

    retained = json.loads(state_path.read_text(encoding="utf-8"))
    assert retained["request_id"] == request_id
    assert retained["transaction"] == transaction
    assert isinstance(retained["old_key_possession_signature"], str)
    assert isinstance(retained["possession_secret"], str)
    first_call = first.progress_calls[0]

    retry = Client(transaction=transaction, progress=[_completed(transaction, key)])
    _install_client(monkeypatch, retry)
    assert cli.command_credential_reauthorize_expired(_args(identity_path, state_path)) == 0

    assert retry.prepare_calls == []
    assert retry.progress_calls == [first_call]
    assert not state_path.exists()
    assert key_path.read_bytes() == key_raw
    updated = json.loads(identity_path.read_text(encoding="utf-8"))
    original = json.loads(identity_raw)
    assert updated | {"actor": original["actor"]} == original
    assert updated["actor"] | {
        "credential_id": actor.credential_id,
        "credential_epoch": actor.credential_epoch,
    } == original["actor"]
    assert updated["actor"]["credential_id"] == "credential-successor"
    assert updated["actor"]["credential_epoch"] == actor.credential_epoch + 1
    assert json.loads(capsys.readouterr().out) == {
        "schema": "agentnet.laptop-credential-reauthorization-cli-result.v1",
        "status": "current",
        "credential_epoch": actor.credential_epoch + 1,
        "identity_saved_locally": True,
        "key_preserved": True,
        "authority_granted": False,
    }


def test_pending_opens_stable_approval_once_then_completes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    identity_path, _key_path, _key_raw, actor, key, identity_raw = _profile(tmp_path)
    state_path = (tmp_path / "reauthorization-state.json").resolve()
    transaction = _transaction(
        actor=actor,
        key=key,
        identity_raw=identity_raw,
        request_id=str(uuid4()),
    )
    client = Client(
        transaction=transaction,
        progress=[
            Response(
                202,
                {
                    "schema": "agentnet.laptop-credential-reauthorization-pending.v1",
                    "status": "approval_pending",
                    "approval_url": "https://approval.example/approval",
                    "expires_at": NOW + 300,
                },
            ),
            _completed(transaction, key),
        ],
    )
    opened: list[tuple[str, int]] = []
    _install_client(monkeypatch, client)
    monkeypatch.setattr(auth.webbrowser, "open", lambda url, new=0: opened.append((url, new)) or True)
    monkeypatch.setattr(auth.time, "sleep", lambda _seconds: None)

    assert cli.command_credential_reauthorize_expired(_args(identity_path, state_path)) == 0
    assert opened == [("https://approval.example/approval", 1)]
    assert len(client.progress_calls) == 2
    assert not state_path.exists()


def test_failed_approval_handoff_remains_retryable(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    identity_path, _key_path, _key_raw, actor, key, identity_raw = _profile(tmp_path)
    state_path = (tmp_path / "reauthorization-state.json").resolve()
    transaction = _transaction(
        actor=actor,
        key=key,
        identity_raw=identity_raw,
        request_id=str(uuid4()),
    )
    pending = Response(
        202,
        {
            "schema": "agentnet.laptop-credential-reauthorization-pending.v1",
            "status": "approval_pending",
            "approval_url": "https://approval.example/approval",
            "expires_at": NOW + 300,
        },
    )
    client = Client(
        transaction=transaction,
        progress=[pending, pending, _completed(transaction, key)],
    )
    opened: list[str] = []
    browser_results = iter((False, True))
    _install_client(monkeypatch, client)
    monkeypatch.setattr(
        auth.webbrowser,
        "open",
        lambda url, new=0: opened.append(url) or next(browser_results),
    )
    monkeypatch.setattr(auth.time, "sleep", lambda _seconds: None)

    with pytest.raises(SystemExit, match="system browser could not be opened"):
        cli.command_credential_reauthorize_expired(_args(identity_path, state_path))
    retained = json.loads(state_path.read_text(encoding="utf-8"))
    assert retained["approval_url_opened"] is False

    assert cli.command_credential_reauthorize_expired(_args(identity_path, state_path)) == 0
    assert opened == [
        "https://approval.example/approval",
        "https://approval.example/approval",
    ]
    assert not state_path.exists()


def test_mismatched_prepare_transaction_is_rejected_before_signing(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    identity_path, _key_path, _key_raw, actor, key, identity_raw = _profile(tmp_path)
    state_path = (tmp_path / "reauthorization-state.json").resolve()
    transaction = _transaction(
        actor=actor,
        key=key,
        identity_raw=identity_raw,
        request_id=str(uuid4()),
    )
    transaction["harness_id"] = "different-harness"
    client = Client(transaction=transaction, progress=[])
    _install_client(monkeypatch, client)

    with pytest.raises(SystemExit, match="transaction binding"):
        cli.command_credential_reauthorize_expired(_args(identity_path, state_path))
    assert client.progress_calls == []
    retained = json.loads(state_path.read_text(encoding="utf-8"))
    assert retained["transaction"] is None
    assert retained["old_key_possession_signature"] is None


def test_blocked_response_output_is_sanitized_and_state_is_retained(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    identity_path, _key_path, _key_raw, actor, key, identity_raw = _profile(tmp_path)
    state_path = (tmp_path / "reauthorization-state.json").resolve()
    transaction = _transaction(
        actor=actor,
        key=key,
        identity_raw=identity_raw,
        request_id=str(uuid4()),
    )
    protected = "private-secret-receipt-and-identifiers"
    client = Client(transaction=transaction, progress=[Response(403, {"protected": protected})])
    _install_client(monkeypatch, client)

    assert cli.command_credential_reauthorize_expired(_args(identity_path, state_path)) == 1
    output = capsys.readouterr().out
    assert protected not in output
    assert transaction["request_id"] not in output
    assert json.loads(output) == {
        "schema": "agentnet.laptop-credential-reauthorization-cli-result.v1",
        "status": "blocked",
    }
    assert state_path.exists()
