from __future__ import annotations

import base64
import hashlib
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import httpx
import pytest

from agentnet.authorization import (
    AUTHORITY_COMMAND_PURPOSE,
    HumanEntitlement,
    SignedAuthorityCommand,
)
from agentnet.core.app import CommunicationCore
from agentnet.http_api import create_app
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitation_oidc import InternalInvitationOIDCChallenge
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_ISSUE_ACTION,
    INTERNAL_INVITATION_POP_PURPOSE,
    INTERNAL_INVITATION_REVOKE_ACTION,
    InternalInvitationRequest,
    InternalInvitationService,
)
from agentnet.identity.oidc import OIDCAuthorizationRequest, OIDCVerificationResult
from agentnet.operations.config import ExtensionConfig
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.client import proof_headers


@dataclass(frozen=True, slots=True)
class PreparedInvitationVerifier:
    result: OIDCVerificationResult
    transaction_id: str
    acceptance_token: str
    verifier_id: str = "prepared-invitation-http-verifier"

    def verify_invitation_identity(
        self,
        *,
        canonical_invitation,
        evidence,
        expected_issuer,
        when,
    ) -> OIDCVerificationResult:
        assert canonical_invitation
        assert expected_issuer == self.result.identity.issuer
        if evidence != {
            "transaction_id": self.transaction_id,
            "acceptance_token": self.acceptance_token,
        }:
            raise ValueError("wrong prepared invitation evidence")
        return self.result


@dataclass(frozen=True, slots=True)
class PreparedInvitationCoordinator:
    verifier: PreparedInvitationVerifier

    def begin_authorization(self, invitation_id, canonical_invitation):
        assert invitation_id and canonical_invitation
        return OIDCAuthorizationRequest(
            self.verifier.transaction_id,
            "https://id.corp.example/authorize?opaque=1",
            "s" * 43,
            self.verifier.result.expires_at,
        )

    def complete_authorization(self, *, canonical_invitation, evidence):
        assert canonical_invitation
        assert evidence == {"state": "s" * 43, "code": "authorization-code"}
        return InternalInvitationOIDCChallenge(
            transaction_id=self.verifier.transaction_id,
            invitation_digest=hashlib.sha256(canonical_invitation).hexdigest(),
            identity=self.verifier.result.identity,
            id_token_hash=self.verifier.result.id_token_hash,
            expires_at=self.verifier.result.expires_at,
            acceptance_token=self.verifier.acceptance_token,
        )


def _allow(core: CommunicationCore, actor, action: str, resource: str) -> None:
    core.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
            expires_at=datetime.now(UTC) + timedelta(hours=1),
        )
    )


def _signed_headers(key, actor, method: str, path: str, body: bytes) -> dict[str, str]:
    return proof_headers(
        create_request_proof(
            key,
            harness_id=actor.harness_id,
            credential_id=actor.credential_id,
            domain_id=actor.domain_id,
            audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
            method=method,
            scheme="http",
            authority="127.0.0.1",
            path=path,
            query="",
            body=body,
        )
    )


def _command(key, actor, *, action, resource, exact_request, revision, reason):
    now = datetime.now(UTC)
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=action,
        resource=resource,
        request_digest=canonical_digest(exact_request),
        expected_policy_revision=1,
        expected_entity_revision=revision,
        reason=reason,
        issued_at=now,
        expires_at=now + timedelta(minutes=2),
    )
    return SignedAuthorityCommand(
        **fields,
        signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
    )


@pytest.mark.anyio
async def test_internal_invitation_http_is_zero_authority_two_step_and_revocable(
    store,
    identity_factory,
    tmp_path: Path,
) -> None:
    sponsor, sponsor_key = identity_factory(binding_assurance="os_bound")
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=sponsor.domain_id,
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    candidate_key = P256KeyPair.generate()
    now = datetime.now(UTC).replace(microsecond=0)
    result = OIDCVerificationResult(
        identity=VerifiedOIDCIdentity(
            issuer="https://id.corp.example",
            subject="invited-human-subject",
            verified_email="invited@corp.example",
        ),
        id_token_hash=hashlib.sha256(b"invitation-http-id-token").hexdigest(),
        expires_at=int((now + timedelta(minutes=10)).timestamp()),
    )
    verifier = PreparedInvitationVerifier(
        result=result,
        transaction_id="invitation-oidc-http-transaction",
        acceptance_token="acceptance-token-" + "a" * 32,
    )
    core.internal_invitations = InternalInvitationService(
        store,
        oidc_verifier=verifier,
        clock=lambda: int(now.timestamp()),
    )
    core.internal_invitation_oidc = PreparedInvitationCoordinator(verifier)  # type: ignore[assignment]
    invitation = InternalInvitationRequest(
        invitation_id="internal-invitation-http-00000001",
        domain_id=sponsor.domain_id,
        invited_oidc_issuer=result.identity.issuer,
        invited_oidc_subject=result.identity.subject,
        invited_verified_email=result.identity.verified_email,
        candidate_harness_id="invited-http-harness",
        candidate_harness_kind="codex",
        candidate_harness_display_name="Invited HTTP laptop",
        candidate_binding_assurance="os_bound",
        candidate_key_id=candidate_key.thumbprint,
        candidate_public_key_pem=candidate_key.public_pem,
        requested_capabilities=("background_delivery", "messaging"),
        expires_at=now + timedelta(minutes=15),
        reason="sponsor approved exact second laptop",
    )
    resource = f"internal-invitation:{invitation.invitation_id}"
    _allow(core, sponsor, INTERNAL_INVITATION_ISSUE_ACTION, resource)
    _allow(core, sponsor, INTERNAL_INVITATION_REVOKE_ACTION, resource)
    app = create_app(core)
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, client=("127.0.0.1", 41234), raise_app_exceptions=False),
        base_url="http://127.0.0.1",
    ) as client:
        issue_path = "/v1/internal-invitations"
        issue_body = canonical_json({"invitation": invitation.model_dump(mode="json")})
        issued = await client.post(
            issue_path,
            content=issue_body,
            headers={
                "Content-Type": "application/json",
                **_signed_headers(sponsor_key, sponsor, "POST", issue_path, issue_body),
            },
        )
        assert issued.status_code == 201, issued.text
        assert issued.json()["zero_authority_proposal"] is True
        record = issued.json()["invitation"]
        canonical = canonical_json(record["transaction"])
        canonical_b64 = base64.b64encode(canonical).decode("ascii")

        begin = await client.post(
            "/v1/internal-invitations/oidc/begin",
            json={"canonical_invitation_b64": canonical_b64},
        )
        assert begin.status_code == 201, begin.text
        assert begin.json()["authorization"]["state"] == "s" * 43

        completed = await client.post(
            "/v1/internal-invitations/oidc/complete",
            json={
                "canonical_invitation_b64": canonical_b64,
                "state": "s" * 43,
                "code": "authorization-code",
            },
        )
        assert completed.status_code == 200, completed.text
        challenge = completed.json()
        signature = candidate_key.sign(
            INTERNAL_INVITATION_POP_PURPOSE,
            challenge["candidate_possession_fields"],
        )
        accepted = await client.post(
            "/v1/internal-invitations/accept",
            json={
                "canonical_invitation_b64": canonical_b64,
                "oidc_transaction_id": challenge["oidc_transaction_id"],
                "oidc_acceptance_token": challenge["oidc_acceptance_token"],
                "candidate_possession_signature": signature,
            },
        )
        assert accepted.status_code == 201, accepted.text
        acceptance = accepted.json()["acceptance"]
        assert acceptance["positive_entitlements_issued"] == 0
        assert acceptance["harness_id"] == invitation.candidate_harness_id

        replay = await client.post(
            "/v1/internal-invitations/accept",
            json={
                "canonical_invitation_b64": canonical_b64,
                "oidc_transaction_id": challenge["oidc_transaction_id"],
                "oidc_acceptance_token": challenge["oidc_acceptance_token"],
                "candidate_possession_signature": signature,
            },
        )
        assert replay.status_code == 401

        second = invitation.model_copy(
            update={
                "invitation_id": "internal-invitation-http-00000002",
                "candidate_harness_id": "invited-http-harness-revoked",
                "candidate_key_id": P256KeyPair.generate().thumbprint,
            }
        )
        second_key = P256KeyPair.generate()
        second = second.model_copy(
            update={
                "candidate_key_id": second_key.thumbprint,
                "candidate_public_key_pem": second_key.public_pem,
            }
        )
        second_resource = f"internal-invitation:{second.invitation_id}"
        _allow(core, sponsor, INTERNAL_INVITATION_ISSUE_ACTION, second_resource)
        _allow(core, sponsor, INTERNAL_INVITATION_REVOKE_ACTION, second_resource)
        second_body = canonical_json({"invitation": second.model_dump(mode="json")})
        second_issued = await client.post(
            issue_path,
            content=second_body,
            headers={
                "Content-Type": "application/json",
                **_signed_headers(sponsor_key, sponsor, "POST", issue_path, second_body),
            },
        )
        assert second_issued.status_code == 201, second_issued.text
        reason = "sponsor withdrew the unconsumed invitation"
        revoke_resource, revoke_exact = core.internal_invitations.revocation_binding(
            second.invitation_id,
            expected_revision=1,
            reason=reason,
        )
        command = _command(
            sponsor_key,
            sponsor,
            action=INTERNAL_INVITATION_REVOKE_ACTION,
            resource=revoke_resource,
            exact_request=revoke_exact,
            revision=1,
            reason=reason,
        )
        revoke_path = f"/v1/internal-invitations/{second.invitation_id}/revoke"
        revoke_body = canonical_json({"command": command.model_dump(mode="json")})
        revoked = await client.post(
            revoke_path,
            content=revoke_body,
            headers={
                "Content-Type": "application/json",
                **_signed_headers(sponsor_key, sponsor, "POST", revoke_path, revoke_body),
            },
        )
        assert revoked.status_code == 200, revoked.text
        assert revoked.json()["invitation"]["state"] == "revoked"
