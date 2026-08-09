from __future__ import annotations

import json
import os
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import cli
from agentnet._terminal_handoff import TerminalHandoffError
from agentnet.approval import cli_commands as approval_cli
from agentnet.approval.config import MANDATORY_APPROVAL_PURPOSES, load_approval_service_config
from agentnet.errors import ValidationError


def _private_dir(path: Path) -> Path:
    path.mkdir(parents=True, mode=0o700)
    path.chmod(0o700)
    return path


def _approver_spec(path: Path) -> Path:
    value = {
        "approvers": [
            {
                "principal_id": "security-owner",
                "authority_kind": "human",
                "domain_id": "corp.example",
                "allowed_purposes": sorted(MANDATORY_APPROVAL_PURPOSES),
            }
        ]
    }
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return path


def test_approval_provision_outputs_only_public_trust_and_refuses_overwrite(
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    private = _private_dir(tmp_path / "private")
    spec = _approver_spec(private / "approvers.json")
    config_path = private / "config.json"
    data_dir = tmp_path / "approval-data"
    args = [
        "approval",
        "provision",
        "--config",
        str(config_path),
        "--data-dir",
        str(data_dir),
        "--public-origin",
        "https://approval.corp.example",
        "--rp-id",
        "approval.corp.example",
        "--verifier-id",
        "approval.corp.example",
        "--approvers",
        str(spec),
    ]
    assert cli.main(args) == 0
    output_text = capsys.readouterr().out
    output = json.loads(output_text)
    assert output["provisioned"] is True
    assert output["authority_granted"] is False
    assert output["webauthn_registered"] is False
    assert "PRIVATE KEY" not in output_text
    assert "signer_private_key_path" not in output_text
    assert "PUBLIC KEY" in output_text

    config = load_approval_service_config(config_path)
    assert config.verifier_id == "approval.corp.example"
    for path in (
        config_path,
        config.database_path,
        config.record_key_path,
        config.approvers[0].signer_private_key_path,
    ):
        assert stat_mode(path) == 0o600
    assert stat_mode(data_dir) == 0o700
    assert stat_mode(data_dir / "secrets") == 0o700
    assert stat_mode(data_dir / "signers") == 0o700

    with pytest.raises(SystemExit, match="refuses to overwrite"):
        cli.main(args)

    assert cli.main(["approval", "status", "--config", str(config_path)]) == 0
    status_text = capsys.readouterr().out
    status = json.loads(status_text)
    assert status["ready"] is True
    assert status["active_credentials"] == 0
    assert status["independent_boundary_proven"] is False
    for forbidden in ("PRIVATE KEY", "PUBLIC KEY", "receipt", "transaction", "credential_id"):
        assert forbidden not in status_text


def test_provision_wires_reference_only_owner_oidc_and_open_service(
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    private = _private_dir(tmp_path / "private")
    spec = private / "approvers.json"
    spec.write_text(
        json.dumps(
            {
                "approvers": [
                    {
                        "principal_id": "security-owner",
                        "authority_kind": "human",
                        "domain_id": "corp.example",
                        "allowed_purposes": sorted(MANDATORY_APPROVAL_PURPOSES),
                        "oidc_issuer": "https://idp.example",
                        "oidc_subject": "owner-subject",
                    }
                ]
            }
        ),
        encoding="utf-8",
    )
    spec.chmod(0o600)
    owner_oidc = private / "owner-oidc.json"
    owner_oidc.write_text(
        json.dumps(
            {
                "issuer": "https://idp.example",
                "client_id": "agentnet-approval",
                "redirect_uri": (
                    "https://approval.corp.example/v1/approval/owner/oidc/callback"
                ),
                "allowed_endpoint_origins": ["https://idp.example"],
            }
        ),
        encoding="utf-8",
    )
    owner_oidc.chmod(0o600)
    config_path = private / "config.json"
    data_dir = tmp_path / "approval-data"
    assert (
        cli.main(
            [
                "approval",
                "provision",
                "--config",
                str(config_path),
                "--data-dir",
                str(data_dir),
                "--public-origin",
                "https://approval.corp.example",
                "--rp-id",
                "approval.corp.example",
                "--verifier-id",
                "approval.corp.example",
                "--approvers",
                str(spec),
                "--owner-oidc-config",
                str(owner_oidc),
            ]
        )
        == 0
    )
    rendered = capsys.readouterr().out
    assert "owner-subject" not in rendered
    assert "client_secret" not in rendered
    loaded = load_approval_service_config(config_path)
    assert loaded.owner_oidc is not None
    assert loaded.approvers[0].oidc_subject == "owner-subject"
    _config, store, service = approval_cli._open_service(config_path)
    try:
        assert service.owner_sessions is not None
        assert service.owner_sessions.approval_service is service
    finally:
        store.close()


def test_recover_canonical_owner_uses_store_bound_identity(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    config_path = tmp_path / "config.json"
    config = SimpleNamespace(
        verifier_id="approval.corp.example",
        data_dir=tmp_path,
    )
    closed = {"value": False}

    class Store:
        def fetch_one(
            self,
            _sql: str,
            params: tuple[object, ...],
        ) -> dict[str, object] | None:
            if params[1] != "placeholder-owner":
                return None
            return {
                "oidc_issuer": "https://idp.example",
                "oidc_subject": "owner-subject",
                "verified_email": "sergey@corp.example",
                "pinned_at": 1_800_000_000,
            }

        def close(self) -> None:
            closed["value"] = True

    seen: dict[str, object] = {}

    def converge(
        store: object,
        **kwargs: object,
    ) -> dict[str, object]:
        seen.update({"store": store, **kwargs})
        return {
            "schema": "agentnet.canonical-owner-recovery-result.v1",
            "status": "recovered",
        }

    store = Store()
    monkeypatch.setattr(
        approval_cli,
        "_open_service",
        lambda _path: (config, store, object()),
    )
    monkeypatch.setattr(
        approval_cli,
        "converge_canonical_approval_owner",
        converge,
    )
    monkeypatch.setattr(approval_cli.time, "time", lambda: 1_800_000_100)

    assert (
        cli.main(
            [
                "approval",
                "recover-canonical-owner",
                "--config",
                str(config_path),
                "--recovery-id",
                "93756ff6-6337-4ed1-9697-250b63fb68a2",
                "--domain",
                "corp.example",
                "--source-principal",
                "placeholder-owner",
                "--target-principal",
                "canonical-owner",
                "--oidc-issuer",
                "https://idp.example",
            ]
        )
        == 0
    )
    request = seen["request"]
    assert getattr(request, "source_principal_id") == "placeholder-owner"
    assert getattr(request, "target_principal_id") == "canonical-owner"
    assert getattr(request, "oidc_subject") == "owner-subject"
    assert getattr(request, "verified_email") == "sergey@corp.example"
    assert seen["now"] == 1_800_000_100
    assert closed["value"] is True
    assert json.loads(capsys.readouterr().out)["status"] == "recovered"




def test_pending_open_and_watch_keep_approval_capability_local(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    pending = {
        "request_id": "request-1",
        "approver_principal_id": "security-owner",
        "domain_id": "corp.example",
        "approval_purpose": "identity.enrollment.approve",
        "transaction_digest": "a" * 64,
        "delivery_mode": "core_claim_code",
        "openable_locally": True,
        "created_at": 1_800_000_000,
        "expires_at": 1_800_000_300,
    }
    secret_url = "https://approval.corp.example/approval#token=agcap1.SECRET&kind=approval"
    opened: list[str] = []

    class FakeStore:
        def close(self) -> None:
            return None

    class FakeService:
        def pending_requests(self) -> list[dict[str, object]]:
            return [pending]

        def local_approval_url(self, request_id: str) -> str:
            assert request_id == "request-1"
            return secret_url

    monkeypatch.setattr(
        approval_cli,
        "_open_service",
        lambda _path: (
            SimpleNamespace(owner_oidc=None),
            FakeStore(),
            FakeService(),
        ),
    )
    monkeypatch.setattr(
        approval_cli.webbrowser,
        "open",
        lambda url, new=0: opened.append(url) or True,
    )

    assert cli.main(["approval", "pending", "--config", "/private/config.json"]) == 0
    pending_output = capsys.readouterr().out
    assert json.loads(pending_output)["requests"] == [pending]
    assert "agcap1." not in pending_output

    assert (
        cli.main(
            [
                "approval",
                "open",
                "--config",
                "/private/config.json",
                "--request-id",
                "request-1",
            ]
        )
        == 0
    )
    open_output = capsys.readouterr().out
    assert json.loads(open_output) == {
        "schema": "agentnet.approval.local-open.v1",
        "request_id": "request-1",
        "opened": True,
    }
    assert "agcap1." not in open_output

    assert (
        cli.main(
            [
                "approval",
                "watch",
                "--config",
                "/private/config.json",
                "--open",
                "--once",
            ]
        )
        == 0
    )
    watch_output = capsys.readouterr().out
    assert json.loads(watch_output)["opened"] is True
    assert "agcap1." not in watch_output
    assert opened == [secret_url, secret_url]


def test_stable_owner_cli_never_emits_or_opens_request_capabilities(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    protected = {
        "request_id": "request-protected",
        "approver_principal_id": "security-owner",
        "domain_id": "corp.example",
        "approval_purpose": "identity.enrollment.approve",
        "transaction_digest": "d" * 64,
        "openable_locally": True,
    }
    opened: list[str] = []

    class FakeStore:
        def close(self) -> None:
            return None

    class FakeService:
        def pending_requests(self) -> list[dict[str, object]]:
            return [protected]

        def local_approval_url(self, _request_id: str) -> str:
            raise AssertionError("stable profile must not resolve a request capability")

    config = SimpleNamespace(
        owner_oidc=object(),
        public_origin="https://approval.corp.example",
    )
    monkeypatch.setattr(
        approval_cli,
        "_open_service",
        lambda _path: (config, FakeStore(), FakeService()),
    )
    monkeypatch.setattr(
        approval_cli.webbrowser,
        "open",
        lambda url, new=0: opened.append(url) or True,
    )

    assert cli.main(["approval", "pending", "--config", "/private/config.json"]) == 0
    pending_output = capsys.readouterr().out
    assert json.loads(pending_output) == {
        "schema": "agentnet.approval.stable-pending.v1",
        "pending_count": 1,
        "review_at_stable_owner_page": True,
    }

    assert cli.main(
        [
            "approval",
            "open",
            "--config",
            "/private/config.json",
            "--request-id",
            "request-protected",
        ]
    ) == 0
    open_output = capsys.readouterr().out
    assert json.loads(open_output) == {
        "schema": "agentnet.approval.stable-open.v1",
        "opened": True,
    }

    assert cli.main(
        [
            "approval",
            "watch",
            "--config",
            "/private/config.json",
            "--open",
            "--once",
        ]
    ) == 0
    watch_output = capsys.readouterr().out
    assert json.loads(watch_output) == {
        "schema": "agentnet.approval.stable-pending-observed.v1",
        "pending_count": 1,
        "opened": True,
    }
    combined = pending_output + open_output + watch_output
    for forbidden in protected.values():
        if isinstance(forbidden, str):
            assert forbidden not in combined
    assert opened == [
        "https://approval.corp.example/approval",
        "https://approval.corp.example/approval",
    ]

    with pytest.raises(SystemExit, match="signed internal broker"):
        approval_cli.command_approval_request_create(
            SimpleNamespace(config="/private/config.json")
        )


def test_pending_open_and_watch_support_private_terminal_mode(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    pending = {
        "request_id": "request-terminal",
        "approver_principal_id": "security-owner",
        "domain_id": "corp.example",
        "approval_purpose": "identity.enrollment.approve",
        "transaction_digest": "b" * 64,
        "delivery_mode": "core_claim_code",
        "openable_locally": True,
        "created_at": 1_800_000_000,
        "expires_at": 1_800_000_300,
    }
    secret_url = "https://approval.corp.example/approval#token=agcap1.PRIVATE"
    terminal_checks: list[bool] = []
    handoffs: list[tuple[str, str, bool]] = []
    materialized: list[str] = []

    class FakeStore:
        def close(self) -> None:
            return None

    class FakeService:
        def pending_requests(self) -> list[dict[str, object]]:
            return [pending]

        def local_approval_url(self, request_id: str) -> str:
            materialized.append(request_id)
            return secret_url

    monkeypatch.setattr(
        approval_cli,
        "_open_service",
        lambda _path: (
            SimpleNamespace(owner_oidc=None),
            FakeStore(),
            FakeService(),
        ),
    )
    monkeypatch.setattr(
        approval_cli,
        "require_private_terminal",
        lambda: terminal_checks.append(True),
    )
    monkeypatch.setattr(
        approval_cli,
        "handoff_private_url",
        lambda url, *, purpose, require_ack: handoffs.append(
            (url, purpose, require_ack)
        ),
    )
    monkeypatch.setattr(
        approval_cli.webbrowser,
        "open",
        lambda *_args, **_kwargs: pytest.fail("terminal mode must not open system browser"),
    )

    assert (
        cli.main(
            [
                "approval",
                "open",
                "--config",
                "/private/config.json",
                "--request-id",
                "request-terminal",
                "--browser",
                "terminal",
            ]
        )
        == 0
    )
    open_output = capsys.readouterr().out
    assert secret_url not in open_output

    assert (
        cli.main(
            [
                "approval",
                "watch",
                "--config",
                "/private/config.json",
                "--open",
                "--browser",
                "terminal",
                "--once",
            ]
        )
        == 0
    )
    watch_output = capsys.readouterr().out
    assert secret_url not in watch_output
    assert terminal_checks == [True, True]
    assert materialized == ["request-terminal", "request-terminal"]
    assert handoffs == [
        (secret_url, "local approval", True),
        (secret_url, "local approval", False),
    ]


def test_private_terminal_mode_refuses_missing_tty_before_approval_materialization(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def unavailable() -> None:
        raise TerminalHandoffError("private controlling terminal is unavailable")

    monkeypatch.setattr(approval_cli, "require_private_terminal", unavailable)
    monkeypatch.setattr(
        approval_cli,
        "_open_service",
        lambda _path: pytest.fail("no TTY must fail before approval service access"),
    )

    with pytest.raises(SystemExit, match="private controlling terminal is unavailable"):
        cli.main(
            [
                "approval",
                "open",
                "--config",
                "/private/config.json",
                "--request-id",
                "request-terminal",
                "--browser",
                "terminal",
            ]
        )
    with pytest.raises(SystemExit, match="private controlling terminal is unavailable"):
        cli.main(
            [
                "approval",
                "watch",
                "--config",
                "/private/config.json",
                "--open",
                "--browser",
                "terminal",
                "--once",
            ]
        )


def test_approval_config_rejects_duplicate_json_keys_before_parsing(tmp_path: Path) -> None:
    private = _private_dir(tmp_path / "private")
    config = private / "config.json"
    config.write_text('{"schema_version":"1.0","schema_version":"1.0"}', encoding="utf-8")
    config.chmod(0o600)
    with pytest.raises(ValidationError, match="configuration is invalid"):
        load_approval_service_config(config)


def test_register_begin_outputs_only_stable_public_entrypoint(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    owner = SimpleNamespace(oidc_issuer="https://idp.example")
    config = SimpleNamespace(
        owner_oidc=SimpleNamespace(),
        public_origin="https://approval.corp.example",
        approver=lambda principal_id: owner
        if principal_id == "security-owner"
        else pytest.fail("unexpected approver"),
    )
    monkeypatch.setattr(approval_cli, "load_approval_service_config", lambda _path: config)
    assert (
        cli.main(
            [
                "approval",
                "register-begin",
                "--config",
                "/private/config.json",
                "--approver",
                "security-owner",
            ]
        )
        == 0
    )
    rendered = capsys.readouterr().out
    value = json.loads(rendered)
    assert value == {
        "schema": "agentnet.approval.stable-registration-entrypoint.v1",
        "approval_url": "https://approval.corp.example/approval",
        "authority_granted": False,
    }
    assert "agcap1." not in rendered
    assert "#" not in rendered
    assert "approver_principal_id" not in rendered


def test_approval_serve_rejects_non_loopback_before_loading_config() -> None:
    with pytest.raises(SystemExit, match="loopback"):
        cli.main(
            [
                "approval",
                "serve",
                "--config",
                "/does/not/exist",
                "--host",
                "0.0.0.0",
            ]
        )
    with pytest.raises(SystemExit, match="explicit loopback IP"):
        cli.main(
            [
                "approval",
                "serve",
                "--config",
                "/does/not/exist",
                "--host",
                "localhost",
            ]
        )


def stat_mode(path: Path) -> int:
    return os.stat(path).st_mode & 0o777
