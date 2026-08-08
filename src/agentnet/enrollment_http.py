"""Public, proof-bound enrollment ceremony routes.

These are the only unauthenticated product routes.  They accept no identity
claims: OIDC supplies the human identity, the candidate key proves possession,
and an independently verified WebAuthn receipt authorizes the exact transcript.
"""

from __future__ import annotations

import base64
import binascii
from dataclasses import asdict
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, RedirectResponse, Response
from starlette.routing import Route

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, ValidationError
from agentnet.identity.oidc import (
    OIDCEnrollmentCoordinator,
    RemoteActivationIdentityMismatch,
)
from agentnet.identity.oidc_callback import OIDCCallbackError, parse_oidc_callback_pairs
from agentnet.identity.recovery import OIDCCredentialRecoveryCoordinator


class OIDCBeginBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    idempotency_key: str = Field(min_length=43, max_length=43, pattern=r"^[A-Za-z0-9_-]+$")
    harness_kind: str = Field(min_length=1, max_length=64)
    harness_name: str = Field(min_length=1, max_length=128)
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    activation_mode: Literal["local_browser", "remote_browser"] = "local_browser"


class OIDCPollBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    transaction_id: str = Field(min_length=16, max_length=128)
    continuation_token: str = Field(min_length=32, max_length=128)


class OIDCGuidedCompleteBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    transaction_id: str = Field(min_length=16, max_length=128)
    continuation_token: str = Field(min_length=32, max_length=128)
    possession_signature: str = Field(min_length=16, max_length=2_048)


class EnrollmentCompleteBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    challenge_id: str = Field(min_length=16, max_length=128)
    nonce: str = Field(min_length=32, max_length=256)
    canonical_transaction_b64: str = Field(min_length=32, max_length=131_072)
    possession_signature: str = Field(min_length=1, max_length=2_048)
    independent_approval: dict[str, Any]


def _public_headers() -> dict[str, str]:
    return {
        "Cache-Control": "no-store",
        "Pragma": "no-cache",
        "Referrer-Policy": "no-referrer",
        "X-Content-Type-Options": "nosniff",
    }


async def _bounded_body(request: Request, core: CommunicationCore) -> bytes:
    body = await request.body()
    if len(body) > core.config.max_request_bytes:
        raise ValidationError("request body exceeds configured limit")
    return body


def _rate_limit(
    core: CommunicationCore,
    request: Request,
    *,
    metric: str = "enrollment_attempts",
    limit: int = 20,
) -> None:
    peer = "unavailable" if request.client is None else request.client.host
    core.quotas.consume(
        scope=f"public-enrollment:{peer}",
        metric=metric,
        amount=1,
        limit=limit,
    )


def create_enrollment_routes(
    core: CommunicationCore,
    coordinator: OIDCEnrollmentCoordinator,
    *,
    recovery_coordinator: OIDCCredentialRecoveryCoordinator | None = None,
) -> list[Route]:
    """Mount one OIDC -> challenge -> PoP/OOB completion ceremony."""

    if coordinator.store is not core.store:
        raise ValueError("enrollment HTTP coordinator must share the core store")


    async def discovery(request: Request) -> Response:
        _rate_limit(core, request, metric="enrollment_discovery", limit=120)
        return JSONResponse(
            {
                "schema": "agentnet.enrollment.discovery.v1",
                "domain_id": core.config.domain_id,
                "profile": "guided_oidc_passkey",
            },
            headers=_public_headers(),
        )
    async def begin(request: Request) -> Response:
        _rate_limit(core, request)
        parsed = OIDCBeginBody.model_validate_json(await _bounded_body(request, core))
        authorization = coordinator.begin_authorization(
            domain_id=core.config.domain_id,
            harness_kind=parsed.harness_kind,
            harness_name=parsed.harness_name,
            public_key_pem=parsed.public_key_pem,
            remote_activation=parsed.activation_mode == "remote_browser",
            idempotency_key=parsed.idempotency_key,
        )
        return JSONResponse(asdict(authorization), status_code=201, headers=_public_headers())

    async def activation_page(request: Request) -> Response:
        _rate_limit(core, request, metric="enrollment_activation_pages", limit=60)
        headers = {
            **_public_headers(),
            "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
        }
        return HTMLResponse(
            "<!doctype html><html><head><meta charset=utf-8>"
            "<meta name=viewport content='width=device-width,initial-scale=1'>"
            "<title>Activate AgentNet server</title>"
            "<style>body{font:18px system-ui;max-width:42rem;margin:4rem auto;padding:0 1rem}"
            "a{display:inline-block;padding:.8rem 1rem;background:#155eef;color:white;text-decoration:none;border-radius:.4rem}</style>"
            "</head><body><h1>Activate AgentNet server</h1>"
            "<p>Continue only if you started this AgentNet server activation. "
            "You will sign in with the approved company account, review an identity-only request, and approve with your passkey.</p>"
            "<a href='/v1/enrollment/oidc/activate'>Continue with Google</a></body></html>",
            headers=headers,
        )

    async def activate(request: Request) -> Response:
        _rate_limit(core, request, metric="enrollment_activation_starts", limit=20)
        authorization_url = coordinator.remote_activation_authorization_url()
        return RedirectResponse(
            authorization_url,
            status_code=303,
            headers=_public_headers(),
        )

    async def callback(request: Request) -> Response:
        _rate_limit(core, request)
        query = parse_oidc_callback_pairs(request.query_params.multi_items())
        if isinstance(query, OIDCCallbackError):
            if recovery_coordinator is not None and recovery_coordinator.has_state(query.state):
                recovery_coordinator.fail_authorization(state=query.state)
            else:
                coordinator.fail_authorization(state=query.state)
            raise AuthenticationError("OIDC callback parameters are invalid")
        if recovery_coordinator is not None and recovery_coordinator.has_state(query.state):
            recovery = recovery_coordinator.complete_authorization(
                state=query.state,
                code=query.code,
            )
            return JSONResponse(
                {
                    "recovery_request": recovery.request.model_dump(mode="json"),
                    "recovery_transaction_id": recovery.transaction_id,
                },
                headers=_public_headers(),
            )
        try:
            challenge = coordinator.complete_authorization(state=query.state, code=query.code)
        except RemoteActivationIdentityMismatch:
            headers = {
                **_public_headers(),
                "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'",
            }
            return HTMLResponse(
                "<!doctype html><html><head><meta charset=utf-8>"
                "<meta name=viewport content='width=device-width,initial-scale=1'>"
                "<title>Approved account required</title></head><body>"
                "<h1>Approved company account required</h1>"
                "<p>This account cannot activate this AgentNet server. Sign out, then retry "
                "from the fixed activation page with the approved company account.</p>"
                "<p><a href='/activate'>Return to server activation</a></p></body></html>",
                status_code=403,
                headers=headers,
            )
        if coordinator.remote_activation_for_challenge(challenge.challenge_id):
            approval_config = getattr(coordinator.approval_client, "config", None)
            approval_origin = getattr(approval_config, "public_origin", None)
            if not isinstance(approval_origin, str) or not approval_origin.startswith("https://"):
                raise AuthenticationError("remote activation approval entrypoint is unavailable")
            return RedirectResponse(
                f"{approval_origin.rstrip('/')}/approval",
                status_code=303,
                headers=_public_headers(),
            )
        if "application/json" in request.headers.get("accept", "").lower():
            return JSONResponse(
                {
                    "challenge_id": challenge.challenge_id,
                    "nonce": challenge.nonce,
                    "expires_at": challenge.expires_at,
                    "canonical_transaction_b64": base64.b64encode(
                        challenge.canonical_transaction
                    ).decode("ascii"),
                },
                headers=_public_headers(),
            )
        return HTMLResponse(
            "<!doctype html><meta charset=utf-8><title>AgentNet enrollment</title>"
            "<p>Google sign-in received. Return to the AgentNet onboarding command.</p>",
            headers=_public_headers(),
        )

    async def poll(request: Request) -> Response:
        _rate_limit(core, request, metric="enrollment_polls", limit=120)
        parsed = OIDCPollBody.model_validate_json(await _bounded_body(request, core))
        result = coordinator.poll_continuation(
            transaction_id=parsed.transaction_id,
            continuation_token=parsed.continuation_token,
        )
        return JSONResponse(asdict(result), headers=_public_headers())

    async def complete_guided(request: Request) -> Response:
        _rate_limit(core, request)
        parsed = OIDCGuidedCompleteBody.model_validate_json(
            await _bounded_body(request, core)
        )
        result = coordinator.complete_guided_enrollment(
            transaction_id=parsed.transaction_id,
            continuation_token=parsed.continuation_token,
            possession_signature=parsed.possession_signature,
        )
        value = asdict(result)
        value["actor"] = result.actor.model_dump(mode="json")
        return JSONResponse(value, status_code=201, headers=_public_headers())

    async def complete(request: Request) -> Response:
        _rate_limit(core, request)
        parsed = EnrollmentCompleteBody.model_validate_json(await _bounded_body(request, core))
        try:
            transaction = base64.b64decode(
                parsed.canonical_transaction_b64.encode("ascii"),
                validate=True,
            )
        except (UnicodeEncodeError, binascii.Error, ValueError) as exc:
            raise ValidationError("canonical enrollment transaction is not base64") from exc
        if not transaction or len(transaction) > 98_304:
            raise ValidationError("canonical enrollment transaction is outside the size bound")
        result = coordinator.enrollment.complete(
            challenge_id=parsed.challenge_id,
            nonce=parsed.nonce,
            canonical_transaction=transaction,
            possession_signature=parsed.possession_signature,
            approval=parsed.independent_approval,
        )
        value = asdict(result)
        value["actor"] = result.actor.model_dump(mode="json")
        return JSONResponse(value, status_code=201, headers=_public_headers())

    return [
        Route("/v1/enrollment/discovery", discovery, methods=["GET"]),
        Route("/activate", activation_page, methods=["GET"]),
        Route("/v1/enrollment/oidc/activate", activate, methods=["GET"]),
        Route("/v1/enrollment/oidc/begin", begin, methods=["POST"]),
        Route("/v1/enrollment/oidc/callback", callback, methods=["GET"]),
        Route("/v1/enrollment/oidc/poll", poll, methods=["POST"]),
        Route("/v1/enrollment/oidc/complete", complete_guided, methods=["POST"]),
        Route("/v1/enrollment/complete", complete, methods=["POST"]),
    ]


__all__ = ["create_enrollment_routes"]
