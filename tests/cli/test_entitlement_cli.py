from __future__ import annotations

import argparse
import json
from pathlib import Path

import pytest

from agentnet import cli
from agentnet.cli.commands import auth
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import P256KeyPair


class _Response:
    status_code = 201

    @staticmethod
    def json() -> dict[str, object]:
        return {"entitlement_id": "entitlement-c0-send"}


class _Client:
    def __init__(self) -> None:
        self.closed = False
        self.requests: list[tuple[str, str, dict[str, object]]] = []

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, object],
    ) -> _Response:
        self.requests.append((method, path, json_body))
        return _Response()

    def close(self) -> None:
        self.closed = True


def _issuer() -> tuple[VerifiedActor, P256KeyPair]:
    key = P256KeyPair.generate()
    return (
        VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id="mellanni.com",
            principal_id="principal-admin",
            harness_id="harness-admin",
            credential_id="credential-admin",
            credential_epoch=1,
            binding_assurance="os_bound",
        ),
        key,
    )


def test_entitlement_issue_by_principal_id_never_loads_beneficiary_private_state(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    client = _Client()
    actor, key = _issuer()
    loaded_paths: list[Path] = []

    def load_identity(path: Path):
        loaded_paths.append(path)
        if path != Path("admin-identity.json"):
            pytest.fail("principal-id issuance must not load beneficiary identity or private key")
        return client, actor, key

    monkeypatch.setattr(auth, "_load_identity_client", load_identity)
    args = argparse.Namespace(
        identity="admin-identity.json",
        beneficiary_identity=None,
        beneficiary_principal_id="principal-fresh-laptop",
        entitlement_id="entitlement-c0-send",
        action="message.send",
        resource="direct",
        revision=1,
        policy_revision=1,
        expires_in=3600,
        reason="authorize isolated C0 direct-message test",
    )

    assert cli.command_admin_entitlement_issue(args) == 0

    assert loaded_paths == [Path("admin-identity.json")]
    assert client.closed is True
    assert len(client.requests) == 1
    method, path, body = client.requests[0]
    assert (method, path) == ("POST", "/v1/admin/entitlements")
    entitlement = body["entitlement"]
    assert isinstance(entitlement, dict)
    assert entitlement["domain_id"] == "mellanni.com"
    assert entitlement["principal_id"] == "principal-fresh-laptop"
    assert entitlement["action"] == "message.send"
    assert entitlement["resource_pattern"] == "direct"

    output = json.loads(capsys.readouterr().out)
    assert output["beneficiary_principal_id"] == "principal-fresh-laptop"
    assert output["authority_is_human_only"] is True


@pytest.mark.parametrize("principal_id", ["", " principal-fresh-laptop "])
def test_entitlement_issue_rejects_non_exact_principal_id_before_request(
    monkeypatch: pytest.MonkeyPatch,
    principal_id: str,
) -> None:
    client = _Client()
    actor, key = _issuer()
    monkeypatch.setattr(
        auth,
        "_load_identity_client",
        lambda path: (client, actor, key)
        if path == Path("admin-identity.json")
        else pytest.fail("invalid principal id must not load beneficiary private state"),
    )
    args = argparse.Namespace(
        identity="admin-identity.json",
        beneficiary_identity=None,
        beneficiary_principal_id=principal_id,
        entitlement_id="entitlement-c0-send",
        action="message.send",
        resource="direct",
        revision=1,
        policy_revision=1,
        expires_in=3600,
        reason="authorize isolated C0 direct-message test",
    )

    with pytest.raises(
        SystemExit,
        match="beneficiary principal id must be a non-empty exact value",
    ):
        cli.command_admin_entitlement_issue(args)

    assert client.closed is True
    assert client.requests == []
