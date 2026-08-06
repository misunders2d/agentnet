"""Composition of the native A2A gateway into the persistent self-hosted app."""

from __future__ import annotations

import os
import secrets
import stat
import time

from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from urllib.parse import urlsplit

from a2a.types import (
    APIKeySecurityScheme,
    AgentCapabilities,
    AgentCard,
    SecurityRequirement,
    SecurityScheme,
    StringList,
)
from starlette.requests import Request
from starlette.routing import BaseRoute

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.gateways.a2a import (
    OpaqueAgentRoute,
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    require_standing_grant,
    system_address_resolver,
    validate_outbound_url,
)
from agentnet.gateways.a2a_client import (
    CorporateA2AClientIdentity,
    PinnedCallbackSender,
    create_pinned_callback_sender,
)
from agentnet.gateways.a2a_runtime import (
    DurableA2ARuntime,
    SignedCorporateA2AAuthenticator,
)
from agentnet.identity.credentials import (
    load_credential_binding,
    public_key_thumbprint,
)
from agentnet.operations.config import RuntimeProfile
from agentnet.security.signatures import P256KeyPair


MAX_A2A_PRIVATE_KEY_BYTES = 16 * 1024


@dataclass(slots=True)
class PersistentA2AService:
    routes: list[BaseRoute]
    runtime: DurableA2ARuntime
    callback_sender: PinnedCallbackSender | None = None

    async def close(self) -> None:
        if self.callback_sender is not None:
            await self.callback_sender.close()


def _key_path(core: CommunicationCore, configured: Path) -> Path:
    return configured if configured.is_absolute() else core.config.data_dir / configured


def _load_owner_signing_key(core: CommunicationCore, configured_path: Path) -> P256KeyPair:
    path = _key_path(core, configured_path)
    try:
        parent_metadata = path.parent.stat()
    except OSError as exc:
        raise GateBlocked("a2a_signing_key", "native A2A signing key directory is unavailable") from exc
    if (
        path.parent.is_symlink()
        or parent_metadata.st_uid != os.geteuid()
        or parent_metadata.st_mode & 0o077
    ):
        raise GateBlocked("a2a_signing_key", "native A2A signing key directory must be owner-only")
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise GateBlocked("a2a_signing_key", "native A2A signing key reference is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or metadata.st_size < 1
            or metadata.st_size > MAX_A2A_PRIVATE_KEY_BYTES
        ):
            raise GateBlocked("a2a_signing_key", "native A2A signing key must be a bounded owner-only file")
        pem = os.read(descriptor, MAX_A2A_PRIVATE_KEY_BYTES + 1)
        if len(pem) != metadata.st_size:
            raise GateBlocked("a2a_signing_key", "native A2A signing key changed during bounded read")
    finally:
        os.close(descriptor)
    try:
        return P256KeyPair.from_private_pem(pem)
    except Exception as exc:
        raise GateBlocked("a2a_signing_key", "native A2A signing key is invalid") from exc


def _load_signing_identity(core: CommunicationCore) -> CorporateA2AClientIdentity:
    service = core.config.a2a
    if service is None:
        raise GateBlocked("a2a_config", "native A2A signing identity configuration is absent")
    configured = service.signing_identity
    try:
        lineage = [
            (entry, load_credential_binding(core.store, entry.credential_id))
            for entry in configured.credential_lineage
        ]
    except Exception as exc:
        raise GateBlocked("a2a_signing_key", "native A2A credential lineage is unavailable") from exc
    if any(
        binding.domain_id != core.config.domain_id
        or binding.harness_id != configured.harness_id
        or binding.credential_id != entry.credential_id
        for entry, binding in lineage
    ):
        raise GateBlocked("a2a_signing_key", "native A2A credential lineage crossed its enrolled harness")
    epochs = [binding.credential_epoch for _entry, binding in lineage]
    if any(current != previous + 1 for previous, current in zip(epochs, epochs[1:])):
        raise GateBlocked("a2a_signing_key", "native A2A credential lineage is not epoch-contiguous")
    if any(binding.credential_status != "retired" for _entry, binding in lineage[:-1]):
        raise GateBlocked("a2a_signing_key", "native A2A credential lineage has a non-retired predecessor")
    current_entry, binding = lineage[-1]
    try:
        binding.require_active(now=int(time.time()))
    except Exception as exc:
        raise GateBlocked("a2a_signing_key", "native A2A signing credential is not current") from exc
    key = _load_owner_signing_key(core, current_entry.private_key_path)
    if (
        not secrets.compare_digest(binding.key_id, key.thumbprint)
        or not secrets.compare_digest(public_key_thumbprint(binding.public_key_pem), key.thumbprint)
    ):
        raise GateBlocked("a2a_signing_key", "native A2A signing key does not match the enrolled credential")
    if core.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and binding.binding_assurance == "lab":
        raise GateBlocked("a2a_signing_key", "always-on native A2A cannot use a lab signing binding")
    return CorporateA2AClientIdentity(
        key=key,
        domain_id=binding.domain_id,
        harness_id=binding.harness_id,
        credential_id=binding.credential_id,
        audience=core.config.effective_service_audience,
    )


def _transport_peer(request: Request) -> str:
    if request.client is None or not request.client.host:
        raise AuthenticationError("unsigned A2A request lacks a transport peer address")
    return request.client.host


def _service_url_policy(core: CommunicationCore) -> SSRFPolicy:
    origin = urlsplit(core.config.public_base_url)
    host = origin.hostname or ""
    default_port = 80 if origin.scheme == "http" else 443
    port = origin.port or default_port
    return SSRFPolicy(
        allowed_hosts=frozenset({host}),
        allowed_ports=frozenset({port}),
        allow_private_for_allowlisted_hosts=True,
        allow_loopback_http_lab=origin.scheme == "http",
    )


def create_persistent_a2a_service(core: CommunicationCore) -> PersistentA2AService:
    """Build one durable native gateway or fail before the HTTP service starts."""

    core.config.require_feature("public_a2a")
    service = core.config.a2a
    if service is None:
        raise GateBlocked("a2a_config", "native A2A route/card/grant/key configuration is absent")
    identity = _load_signing_identity(core)
    recipient = core.store.fetch_one(
        "SELECT domain_id,status FROM harnesses WHERE harness_id=?",
        (service.recipient_harness_id,),
    )
    if (
        recipient is None
        or recipient["domain_id"] != core.config.domain_id
        or recipient["status"] != "active"
    ):
        raise GateBlocked("a2a_recipient", "native A2A recipient is not an active enrolled harness")

    route = OpaqueAgentRoute(
        route_token=service.route_token,
        logical_agent_id=service.recipient_harness_id,
        domain_id=core.config.domain_id,
    )
    template = AgentCard(
        name=service.card.name,
        description=service.card.description,
        version=service.card.version,
        capabilities=AgentCapabilities(
            streaming=service.card.streaming,
            push_notifications=service.card.push_notifications,
        ),
    )
    template.security_schemes["agentNetCorporateProof"].CopyFrom(
        SecurityScheme(
            api_key_security_scheme=APIKeySecurityScheme(
                description=(
                    "AgentNet P-256 exact-request proof. X-AgentNet-Signature is accepted only with the full "
                    "harness, credential, target, digest, timestamp, and nonce header set."
                ),
                location="header",
                name="X-AgentNet-Signature",
            )
        )
    )
    template.security_requirements.extend(
        [
            SecurityRequirement(schemes={"agentNetCorporateProof": StringList()}),
            SecurityRequirement(),  # unsigned public input remains a tainted proposal only
        ]
    )
    card = build_exported_agent_card(
        template,
        route=route,
        public_base_url=core.config.public_base_url,
        allow_loopback_http_lab=core.config.service_scheme == "http",
    )
    grant = StandingA2AGrant(
        grant_id=service.standing_grant.grant_id,
        route_token=service.route_token,
        logical_agent_id=service.recipient_harness_id,
        allowed_actions=service.standing_grant.allowed_actions,
        allowed_resources=frozenset({service.recipient_harness_id}),
        allowed_output_sinks=service.standing_grant.allowed_output_sinks,
        allowed_peer_namespaces=service.standing_grant.allowed_peer_namespaces,
        expires_at=service.standing_grant.expires_at,
        revoked_at=service.standing_grant.revoked_at,
        revision=service.standing_grant.revision,
    )
    require_standing_grant(grant, route=route, now=datetime.now(UTC))

    callback_policy = SSRFPolicy(
        allowed_hosts=service.callback_allowed_hosts,
        allowed_ports=service.callback_allowed_ports,
        allow_private_for_allowlisted_hosts=False,
        allow_loopback_http_lab=service.allow_loopback_callback_http_lab,
    )
    artifact_policy = SSRFPolicy()
    runtime = DurableA2ARuntime(
        store=core.store,
        mailbox=core.mailboxes,
        collaboration_scopes=core.mailboxes.collaboration_scopes,
        policy=core.policy,
        assignments=core.assignments,
        agent_card=card,
        recipient_id=service.recipient_harness_id,
        url_validator=lambda url: validate_outbound_url(
            url,
            policy=artifact_policy,
            resolver=system_address_resolver,
        ),
        callback_url_validator=lambda url: validate_outbound_url(
            url,
            policy=callback_policy,
            resolver=system_address_resolver,
        ),
        callback_sender=None,
        artifact_binding_resolver=core.artifacts.resolve_released_binding,
        retention_days=core.config.policies.operations.retention_days,
    )
    routes = build_starlette_routes(
        extension_config=core.config,
        request_handler=runtime,
        agent_card=card,
        route=route,
        grant_lookup=lambda token: grant if secrets.compare_digest(token, route.route_token) else None,
        peer_resolver=_transport_peer,
        url_policy=_service_url_policy(core),
        resolver=system_address_resolver,
        corporate_authenticator=SignedCorporateA2AAuthenticator(core.contexts),
        max_request_bytes=core.config.max_request_bytes,
    )
    callback_sender: PinnedCallbackSender | None = None
    if service.card.push_notifications:
        callback_sender = create_pinned_callback_sender(
            identity=identity,
            policy=callback_policy,
            resolver=system_address_resolver,
        )
        runtime.callback_sender = callback_sender
    return PersistentA2AService(routes=routes, runtime=runtime, callback_sender=callback_sender)


__all__ = ["PersistentA2AService", "create_persistent_a2a_service"]
