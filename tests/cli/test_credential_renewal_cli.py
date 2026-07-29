from __future__ import annotations

import argparse
import json
from pathlib import Path

import pytest

import agentnet.cli as cli


class Response:
    status_code = 200

    def __init__(self, status: str = "current") -> None:
        self.status = status

    def json(self):
        return {
            "schema": "agentnet.credential-renewal-result.v1",
            "status": self.status,
            "expires_at": 123456,
        }


class Client:
    def __init__(self, *, fail: bool = False, status: str = "current") -> None:
        self.fail = fail
        self.status = status
        self.request_ids: list[str] = []
        self.closed = False

    def renew_current_credential(self, *, request_id: str):
        self.request_ids.append(request_id)
        if self.fail:
            raise RuntimeError("response lost")
        return Response(self.status)

    def close(self) -> None:
        self.closed = True


def _args(tmp_path: Path) -> argparse.Namespace:
    return argparse.Namespace(
        identity="identity.json",
        state=str(tmp_path / "renewal-state.json"),
    )


def test_renewal_cli_rotates_request_only_after_exact_response(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    client = Client(status="renewed")
    monkeypatch.setattr(cli, "_load_identity_client", lambda _path: (client, object(), object()))
    args = _args(tmp_path)

    assert cli.command_credential_renew(args) == 0

    output = json.loads(capsys.readouterr().out)
    assert output == {
        "schema": "agentnet.credential-renewal-cli-result.v1",
        "status": "renewed",
    }
    state = json.loads(Path(args.state).read_text(encoding="utf-8"))
    assert state["request_id"] != client.request_ids[0]
    assert client.closed is True


def test_renewal_cli_reuses_durable_request_after_response_loss(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    failing = Client(fail=True)
    monkeypatch.setattr(cli, "_load_identity_client", lambda _path: (failing, object(), object()))
    args = _args(tmp_path)

    with pytest.raises(RuntimeError, match="response lost"):
        cli.command_credential_renew(args)
    first_id = failing.request_ids[0]

    retry = Client()
    monkeypatch.setattr(cli, "_load_identity_client", lambda _path: (retry, object(), object()))
    assert cli.command_credential_renew(args) == 0
    assert retry.request_ids == [first_id]


def test_renewal_cli_blocked_output_contains_no_server_body(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    class BlockedResponse:
        status_code = 403

        def json(self):
            return {"private": "must-not-print"}

    client = Client()
    client.renew_current_credential = lambda **_kwargs: BlockedResponse()  # type: ignore[method-assign]
    monkeypatch.setattr(cli, "_load_identity_client", lambda _path: (client, object(), object()))

    assert cli.command_credential_renew(_args(tmp_path)) == 1
    assert json.loads(capsys.readouterr().out) == {
        "schema": "agentnet.credential-renewal-cli-result.v1",
        "status": "blocked",
    }
