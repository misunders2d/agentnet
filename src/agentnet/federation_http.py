"""HTTP composition for bilateral, host-local federation.

Local host mutations are authorized from the transport-resolved actor.  The
two pre-enrollment home-domain entry points accept no caller identity fields;
their exact request objects are bounded and authenticated by the pinned home
signature (and, for invitation acceptance, the one-time invitation proof).
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from typing import TYPE_CHECKING, Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import OperationClass
from agentnet.errors import AuthorizationError, ValidationError
from agentnet.federation.service import (
    GuestIdentityAssertion,
    HomeFederationAssertion,
    HomeRevocationSignal,
    HostTrustAcceptance,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification

if TYPE_CHECKING:
    from agentnet.core.app import CommunicationCore


BodyAndActor = Callable[[Request, "CommunicationCore"], Awaitable[tuple[bytes, VerifiedActor]]]


class BilateralTrustAdmissionBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    home_assertion: HomeFederationAssertion
    home_signature: str = Field(min_length=1, max_length=2_048)
    host_acceptance: HostTrustAcceptance
    host_signature: str = Field(min_length=1, max_length=2_048)


class InvitationGrantBody(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    action: str = Field(min_length=1, max_length=256)
    resource_pattern: str = Field(min_length=1, max_length=1_024)
    data_class: Classification
    input_source: str = Field(min_length=1, max_length=256)
    output_sink: str = Field(min_length=1, max_length=1_024)
    max_uses: int = Field(ge=1, le=1_000_000)
    expires_at: int = Field(ge=1)


class InvitationCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    home_domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    pairwise_subject: str = Field(min_length=16, max_length=512)
    guest_public_key_pem: str = Field(min_length=128, max_length=16_384)
    guest_key_id: str = Field(min_length=16, max_length=256)
    grants: tuple[InvitationGrantBody, ...] = Field(min_length=1, max_length=256)
    expires_at: int = Field(ge=1)


class InvitationAcceptBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    secret: str = Field(min_length=32, max_length=512)
    assertion: GuestIdentityAssertion
    home_signature: str = Field(min_length=1, max_length=2_048)


class InvitationReissueBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    expected_invitation_digest: str = Field(pattern=r"^[0-9a-f]{64}$")


class GuestOperationAuthorizationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    grant_use: GrantUse
    classification: Classification


class HomeRevocationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    signal: HomeRevocationSignal
    home_signature: str = Field(min_length=1, max_length=2_048)


class SponsorGuestRevocationBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    reason: str = Field(pattern=r"^[a-z][a-z0-9_.-]{0,63}$")


class SecurityGuestRevocationBody(BaseModel):
    """Exact deny-only domain-security operation; no role field is accepted."""

    model_config = ConfigDict(extra="forbid")

    reason: str = Field(pattern=r"^[a-z][a-z0-9_.-]{0,63}$")


async def _bounded_proof_body(request: Request, core: "CommunicationCore") -> bytes:
    body = await request.body()
    if not body or len(body) > core.config.max_request_bytes:
        raise ValidationError("federation proof body is empty or exceeds the configured limit")
    peer = "unavailable" if request.client is None else request.client.host
    core.quotas.consume(
        scope=f"federation-proof:{peer}",
        metric="federation_proof_attempts",
        amount=1,
        limit=60,
    )
    return body


def _json_result(value: dict[str, Any]) -> dict[str, Any]:
    result = dict(value)
    actor = result.get("actor")
    if isinstance(actor, VerifiedActor):
        result["actor"] = actor.model_dump(mode="json")
    return result


def create_federation_routes(
    core: "CommunicationCore",
    body_and_actor: BodyAndActor,
) -> list[Route]:
    """Compose the existing secure federation service into the ordinary app."""

    core.config.require_feature("federation")
    service = core.federation

    async def admit_trust(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = BilateralTrustAdmissionBody.model_validate_json(body)
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.principal_id is None
            or actor.domain_id != core.config.domain_id
            or parsed.home_assertion.host_domain_id != actor.domain_id
            or parsed.host_acceptance.host_domain_id != actor.domain_id
        ):
            raise AuthorizationError("bilateral trust admission requires exact host-human authority")
        resource = f"federation:{parsed.home_assertion.home_domain_id}"
        core._require(actor=actor, action="federation.trust.admit", resource=resource)
        result = service.admit_bilateral_trust(
            home_assertion=parsed.home_assertion,
            home_signature=parsed.home_signature,
            host_acceptance=parsed.host_acceptance,
            host_signature=parsed.host_signature,
        )
        return JSONResponse(result, status_code=200 if result["duplicate"] else 201)

    async def create_invitation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = InvitationCreateBody.model_validate_json(body)
        resource = f"federation:{parsed.home_domain_id}"
        core._require(actor=actor, action="federation.invitation.create", resource=resource)
        result = service.create_invitation(
            sponsor=actor,
            home_domain_id=parsed.home_domain_id,
            pairwise_subject=parsed.pairwise_subject,
            guest_public_key_pem=parsed.guest_public_key_pem,
            guest_key_id=parsed.guest_key_id,
            grants=tuple(grant.model_dump(mode="json") for grant in parsed.grants),
            expires_at=parsed.expires_at,
        )
        return JSONResponse(result, status_code=201)

    async def accept_invitation(request: Request) -> Response:
        parsed = InvitationAcceptBody.model_validate_json(await _bounded_proof_body(request, core))
        result = service.accept_invitation(
            invitation_id=request.path_params["invitation_id"],
            secret=parsed.secret,
            assertion=parsed.assertion,
            home_signature=parsed.home_signature,
        )
        return JSONResponse(_json_result(result), status_code=201)

    async def reissue_invitation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = InvitationReissueBody.model_validate_json(body)
        invitation_id = request.path_params["invitation_id"]
        core._require(
            actor=actor,
            action="federation.invitation.reissue",
            resource=f"federation-invitation:{invitation_id}",
        )
        return JSONResponse(
            service.reissue_locked_invitation(
                sponsor=actor,
                invitation_id=invitation_id,
                expected_invitation_digest=parsed.expected_invitation_digest,
            ),
            status_code=201,
        )

    async def authorize_guest_operation(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = GuestOperationAuthorizationBody.model_validate_json(body)
        if actor.guest_id is None:
            raise AuthorizationError("federated operation requires a host-local guest actor")
        guest = core.store.fetch_one(
            "SELECT home_domain_id FROM guests WHERE guest_id=? AND host_domain_id=?",
            (actor.guest_id, actor.domain_id),
        )
        if guest is None:
            raise AuthorizationError("federated guest context is not visible")
        return JSONResponse(
            service.authorize_guest_operation(
                actor=actor,
                asserted_host_domain_id=actor.domain_id,
                asserted_home_domain_id=str(guest["home_domain_id"]),
                grant_use=parsed.grant_use,
                classification=parsed.classification,
            )
        )

    async def accept_home_revocation(request: Request) -> Response:
        parsed = HomeRevocationBody.model_validate_json(await _bounded_proof_body(request, core))
        return JSONResponse(
            service.accept_home_revocation(
                signal=parsed.signal,
                home_signature=parsed.home_signature,
            )
        )

    async def revoke_guest(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = SponsorGuestRevocationBody.model_validate_json(body)
        guest_id = request.path_params["guest_id"]
        core._require(actor=actor, action="federation.guest.revoke", resource=f"guest:{guest_id}")
        return JSONResponse(
            service.revoke_guest(host_actor=actor, guest_id=guest_id, reason=parsed.reason)
        )

    async def security_revoke_guest(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = SecurityGuestRevocationBody.model_validate_json(body)
        guest_id = request.path_params["guest_id"]
        resource, exact_request = service.security_guest_revocation_binding(
            guest_id=guest_id,
            reason=parsed.reason,
        )
        decision = core._require(
            actor=actor,
            action="federation.guest.security_revoke",
            resource=resource,
            operation_class=OperationClass.PRIVILEGED,
            context=exact_request,
        )
        return JSONResponse(
            service.security_revoke_guest(
                authority=IssuanceAuthority(
                    actor=actor,
                    policy_decision_id=decision.decision_id,
                ),
                guest_id=guest_id,
                reason=parsed.reason,
            )
        )

    return [
        Route("/v1/federation/trusts", admit_trust, methods=["POST"]),
        Route("/v1/federation/invitations", create_invitation, methods=["POST"]),
        Route(
            "/v1/federation/invitations/{invitation_id}/accept",
            accept_invitation,
            methods=["POST"],
        ),
        Route(
            "/v1/federation/invitations/{invitation_id}/reissue",
            reissue_invitation,
            methods=["POST"],
        ),
        Route(
            "/v1/federation/guest-operations/authorize",
            authorize_guest_operation,
            methods=["POST"],
        ),
        Route("/v1/federation/revocations/home", accept_home_revocation, methods=["POST"]),
        Route("/v1/federation/guests/{guest_id}/revoke", revoke_guest, methods=["POST"]),
        Route(
            "/v1/federation/guests/{guest_id}/security-revoke",
            security_revoke_guest,
            methods=["POST"],
        ),
    ]


__all__ = ["create_federation_routes"]
