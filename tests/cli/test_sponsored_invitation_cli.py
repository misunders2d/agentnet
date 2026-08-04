from __future__ import annotations

import argparse
import json
from datetime import UTC, datetime

import pytest

from agentnet import cli
from agentnet.identity.invitations import InternalInvitationRecord, InternalInvitationTransaction
from agentnet.security.signatures import P256KeyPair


def _record(key: P256KeyPair) -> InternalInvitationRecord:
    now = datetime.fromtimestamp(1_900_000_000, UTC)
    transaction = InternalInvitationTransaction(
        invitation_id="sponsored-invitation-123",
        domain_id="corp.example",
        sponsor_authority_kind="human",
        sponsor_authority_id="principal-sponsor",
        sponsor_harness_id="harness-sponsor",
        sponsor_credential_id="credential-sponsor",
        sponsor_credential_epoch=1,
        invited_oidc_issuer="https://idp.example",
        invited_oidc_subject="subject-candidate",
        invited_verified_email="candidate@example.test",
        candidate_harness_id="candidate-harness",
        candidate_harness_kind="laptop",
        candidate_harness_display_name="Finance laptop",
        candidate_binding_assurance="os_bound",
        candidate_key_id=key.thumbprint,
        candidate_public_key_pem=key.public_pem,
        requested_capabilities=("message.send",),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=datetime.fromtimestamp(1_900_003_600, UTC),
        reason="Add the finance laptop",
    )
    return InternalInvitationRecord(
        transaction=transaction,
        invitation_digest=transaction.digest,
        state="active",
        revision=1,
        use_count=0,
        created_at=now,
        updated_at=now,
    )


def _args(state, invitation) -> argparse.Namespace:
    return argparse.Namespace(
        state=str(state),
        invitation=str(invitation),
        callback=None,
        identity="identity.json",
        force=True,
        server="https://core.example",
        harness_id=None,
        harness="laptop",
        name="Finance laptop",
        binding_assurance="os_bound",
        private_key=None,
    )


def _sponsored_state(tmp_path, key: P256KeyPair):
    state_path = (tmp_path / "private" / "sponsored.json").resolve()
    key_path = (tmp_path / "private" / "candidate.pem").resolve()
    cli._write_owner_only(key_path, key.private_pem)
    original = {
        "schema": "agentnet.sponsored-enrollment-candidate.v1",
        "server_base_url": "https://core.example",
        "private_key_path": str(key_path),
        "continuation_token": "continuation-token-that-is-long-enough-123",
    }
    cli._write_owner_json(state_path, original)
    return state_path, original


def test_sponsored_join_atomically_replaces_only_validated_state_and_writes_invitation(
    tmp_path, monkeypatch
) -> None:
    key = P256KeyPair.generate()
    record = _record(key)
    state_path, _original = _sponsored_state(tmp_path, key)
    invitation_path = (tmp_path / "private" / "invitation.json").resolve()
    monkeypatch.setattr(
        cli,
        "_public_json_request",
        lambda **_request: {"state": "invitation_issued", "invitation": record.model_dump(mode="json")},
    )
    resumed: list[tuple[str, str]] = []
    monkeypatch.setattr(
        cli,
        "command_invitation_oidc_begin",
        lambda args: resumed.append((args.state, args.invitation)) or 0,
    )

    assert cli.command_invitation_join_sponsored(_args(state_path, invitation_path)) == 0

    state = json.loads(state_path.read_text(encoding="utf-8"))
    assert state == {
        "schema": "agentnet.invitation-candidate.v1",
        "server_base_url": "https://core.example",
        "private_key_path": state["private_key_path"],
        "request": record.transaction.model_dump(
            mode="json",
            exclude={
                "schema_version",
                "purpose",
                "sponsor_authority_kind",
                "sponsor_authority_id",
                "sponsor_harness_id",
                "sponsor_credential_id",
                "sponsor_credential_epoch",
                "policy_revision",
                "domain_revocation_epoch",
                "max_uses",
                "predecessor_invitation_digest",
                "predecessor_revision",
            },
        ),
    }
    invitation = json.loads(invitation_path.read_text(encoding="utf-8"))
    assert invitation["invitation"]["invitation_digest"] == record.invitation_digest
    assert invitation["zero_authority_proposal"] is True
    assert resumed == [(str(state_path), str(invitation_path))]


def test_sponsored_join_force_flag_cannot_overwrite_invitation_output(
    tmp_path, monkeypatch
) -> None:
    key = P256KeyPair.generate()
    state_path, original = _sponsored_state(tmp_path, key)
    invitation_path = (tmp_path / "private" / "invitation.json").resolve()
    cli._write_owner_json(invitation_path, {"do_not_replace": True})
    monkeypatch.setattr(
        cli,
        "_public_json_request",
        lambda **_request: pytest.fail("existing output must fail before invitation release"),
    )

    with pytest.raises(SystemExit, match="refusing to overwrite"):
        cli.command_invitation_join_sponsored(_args(state_path, invitation_path))

    assert json.loads(invitation_path.read_text(encoding="utf-8")) == {"do_not_replace": True}
    assert json.loads(state_path.read_text(encoding="utf-8")) == original


def test_sponsored_join_rejects_non_owner_only_or_symlinked_state(
    tmp_path, monkeypatch
) -> None:
    key = P256KeyPair.generate()
    state_path, _original = _sponsored_state(tmp_path, key)
    state_path.chmod(0o644)
    monkeypatch.setattr(
        cli,
        "_public_json_request",
        lambda **_request: pytest.fail("unsafe state must fail before polling"),
    )

    with pytest.raises(SystemExit, match="owner-only"):
        cli.command_invitation_join_sponsored(
            _args(state_path, (tmp_path / "private" / "invitation.json").resolve())
        )
