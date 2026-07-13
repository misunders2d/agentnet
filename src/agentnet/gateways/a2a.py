"""Bounded native A2A v1 gateway built on the official Python SDK.

The SDK supplies protobufs, route dispatch, and client transports.  This module
adds the corporate boundary the public protocol does not supply: an exact v1.0
profile, opaque per-agent routes, standing-grant checks, external-only actors,
SSRF controls, and fail-closed security requirement selection.
"""

from __future__ import annotations

import hashlib
import inspect
import ipaddress
import json
import re
import secrets
import socket

from collections.abc import AsyncIterator, Awaitable, Callable, Collection, Iterable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from email.utils import format_datetime, parsedate_to_datetime
from typing import Any, Literal, Protocol, cast
from urllib.parse import SplitResult, urlsplit
from uuid import uuid4

import httpx

from a2a.client import Client, ClientConfig, ClientFactory
from a2a.server.context import ServerCallContext
from a2a.server.request_handlers import RequestHandler
from a2a.server.request_handlers.response_helpers import build_error_response
from a2a.server.routes import (
    DefaultServerCallContextBuilder,
    ServerCallContextBuilder,
    create_agent_card_routes,
    create_jsonrpc_routes,
    create_rest_routes,
)
from a2a.types import (
    AgentCard,
    AgentInterface,
    AgentSkill,
    CancelTaskRequest,
    DeleteTaskPushNotificationConfigRequest,
    GetExtendedAgentCardRequest,
    GetTaskPushNotificationConfigRequest,
    GetTaskRequest,
    ListTaskPushNotificationConfigsRequest,
    ListTaskPushNotificationConfigsResponse,
    ListTasksRequest,
    ListTasksResponse,
    Message,
    Part,
    Role,
    SendMessageRequest,
    SecurityRequirement,
    StreamResponse,
    SubscribeToTaskRequest,
    Task,
    TaskPushNotificationConfig,
    TaskState,
    TaskStatus,
)
from a2a.utils.constants import (
    PROTOCOL_VERSION_1_0,
    VERSION_HEADER,
    TransportProtocol,
)
from a2a.utils.errors import (
    A2AError,
    ContentTypeNotSupportedError,
    ExtendedAgentCardNotConfiguredError,
    InvalidParamsError,
    InvalidRequestError,
    PushNotificationNotSupportedError,
    TaskNotFoundError,
    UnsupportedOperationError,
    VersionNotSupportedError,
)
from a2a.server.tasks import InMemoryTaskStore
from pydantic import BaseModel, ConfigDict, Field, model_validator
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import BaseRoute, Mount, Route

from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ExtensionError,
    GateBlocked,
    UnsupportedMediaTypeError,
    ValidationError,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.a2a_mapping import (
    URLValidator,
    external_peer_namespace,
    validate_message_part_urls,
)
from agentnet.security.signatures import canonical_digest


A2A_WIRE_VERSION = PROTOCOL_VERSION_1_0
HTTP_JSON_BINDING = TransportProtocol.HTTP_JSON.value
JSONRPC_BINDING = TransportProtocol.JSONRPC.value
PREFERRED_BINDINGS = (HTTP_JSON_BINDING, JSONRPC_BINDING)

_OPAQUE_ROUTE_PATTERN = re.compile(r"^[A-Za-z0-9_-]{32,128}$")
_URL_CONTROL_PATTERN = re.compile(r"[\\\x00-\x1f\x7f]")


AddressResolver = Callable[[str, int], Iterable[str]]
PeerResolver = Callable[[Request], str]
GrantLookup = Callable[[str], "StandingA2AGrant | None"]
Clock = Callable[[], datetime]
CorporateRequestAuthenticator = Callable[[Request, bytes], VerifiedActor | None]

MAX_A2A_REQUEST_BYTES = 2 * 1024 * 1024
MAX_A2A_REQUEST_TARGET_BYTES = 8 * 1024
AGENT_CARD_CACHE_MAX_AGE_SECONDS = 300
_AGENT_CARD_CACHE_CONTROL = (
    f"public, max-age={AGENT_CARD_CACHE_MAX_AGE_SECONDS}, must-revalidate, no-transform"
)


class A2AMountCapabilityConfig(Protocol):
    """Small structural view of ExtensionConfig needed at the mount boundary."""

    @property
    def server_agent_capabilities(self) -> Collection[str | ServerAgentCapability]: ...


def require_a2a_gateway_mount_capability(config: A2AMountCapabilityConfig) -> None:
    """Fail closed unless this ordinary server agent may host an A2A gateway.

    This is process attenuation only.  The capability intentionally is not
    passed to request authorization and cannot create actor, grant, task, or
    data authority.
    """

    capabilities = {
        capability.value if isinstance(capability, ServerAgentCapability) else capability
        for capability in config.server_agent_capabilities
    }
    if ServerAgentCapability.A2A_GATEWAY.value not in capabilities:
        raise GateBlocked(
            "a2a_gateway_capability",
            "server agent is not attenuated to host the native A2A gateway",
        )


@dataclass(frozen=True, slots=True)
class SSRFPolicy:
    """Exact outbound URL policy; every redirect must be revalidated."""

    allowed_hosts: frozenset[str] = frozenset()
    allowed_ports: frozenset[int] = frozenset({443})
    allow_private_for_allowlisted_hosts: bool = False
    allow_loopback_http_lab: bool = False
    max_url_length: int = 2_048


@dataclass(frozen=True, slots=True)
class ValidatedURL:
    url: str
    scheme: Literal["https", "http"]
    host: str
    port: int
    addresses: tuple[str, ...]


class OpaqueAgentRoute(BaseModel):
    """An unguessable public route for one exported logical agent."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    route_token: str = Field(min_length=32, max_length=128, pattern=r"^[A-Za-z0-9_-]+$")
    logical_agent_id: str = Field(min_length=1, max_length=256)
    domain_id: str = Field(min_length=1, max_length=256)

    @model_validator(mode="after")
    def require_opaque_route(self) -> "OpaqueAgentRoute":
        if not _OPAQUE_ROUTE_PATTERN.fullmatch(self.route_token):
            raise ValueError("A2A route token is not an opaque base64url token")
        return self

    @property
    def path_prefix(self) -> str:
        return f"/a2a/{self.route_token}"

    @property
    def tenant(self) -> str:
        return self.route_token


class StandingA2AGrant(BaseModel):
    """A scoped standing export grant; it never grants internal data authority."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    grant_id: str = Field(min_length=1, max_length=256)
    route_token: str = Field(min_length=32, max_length=128, pattern=r"^[A-Za-z0-9_-]+$")
    logical_agent_id: str = Field(min_length=1, max_length=256)
    source_class: Literal["external-low-trust"] = "external-low-trust"
    allowed_actions: frozenset[str] = Field(min_length=1)
    allowed_resources: frozenset[str] = Field(min_length=1)
    allowed_output_sinks: frozenset[str] = Field(min_length=1)
    allowed_peer_namespaces: frozenset[str] = frozenset()
    expires_at: datetime
    revoked_at: datetime | None = None
    revision: int = Field(default=1, ge=1)

    @model_validator(mode="after")
    def validate_grant(self) -> "StandingA2AGrant":
        if self.expires_at.tzinfo is None:
            raise ValueError("standing A2A grant expiry must be timezone-aware")
        if self.revoked_at is not None and self.revoked_at.tzinfo is None:
            raise ValueError("standing A2A grant revocation must be timezone-aware")
        return self


class A2ASecuritySelection(BaseModel):
    """Names/scopes selected from a card; credential bytes are never returned."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    alternative_index: int
    anonymous: bool = False
    schemes: tuple[str, ...] = ()
    scopes: dict[str, tuple[str, ...]] = Field(default_factory=dict)


class _SupportsRequest(Protocol):
    headers: Any
    method: str


def generate_opaque_route(*, logical_agent_id: str, domain_id: str) -> OpaqueAgentRoute:
    return OpaqueAgentRoute(
        route_token=secrets.token_urlsafe(32),
        logical_agent_id=logical_agent_id,
        domain_id=domain_id,
    )


def corporate_peer_namespace(actor: VerifiedActor) -> str:
    """Return a non-authoritative route namespace for one verified binding."""

    if (
        actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}
        or actor.positive_authority_id is None
        or actor.harness_id is None
        or actor.credential_id is None
    ):
        raise AuthenticationError("corporate A2A peer requires an exact verified human/harness binding")
    return f"a2a-corporate:{canonical_digest(actor.audit_view())[:40]}"


def _system_resolver(host: str, port: int) -> tuple[str, ...]:
    try:
        results = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
    except OSError as exc:
        raise ValidationError("A2A URL host could not be resolved") from exc
    return tuple(result[4][0] for result in results)


def system_address_resolver(host: str, port: int) -> tuple[str, ...]:
    """Public DNS resolver seam for composition roots and deterministic tests."""

    return _system_resolver(host, port)


def _normalized_hostname(parsed: SplitResult) -> str:
    try:
        hostname = parsed.hostname
    except ValueError as exc:
        raise ValidationError("A2A URL has an invalid host") from exc
    if not hostname or "%" in hostname:
        raise ValidationError("A2A URL requires an unambiguous host")
    try:
        normalized = hostname.rstrip(".").encode("idna").decode("ascii").lower()
    except UnicodeError as exc:
        raise ValidationError("A2A URL host is not valid IDNA") from exc
    if not normalized or normalized == "localhost" or normalized.endswith(".localhost"):
        raise ValidationError("A2A URL host is not public")
    return normalized


def _port(parsed: SplitResult) -> int:
    try:
        return parsed.port or (80 if parsed.scheme.lower() == "http" else 443)
    except ValueError as exc:
        raise ValidationError("A2A URL has an invalid port") from exc


def validate_outbound_url(
    url: str,
    *,
    policy: SSRFPolicy = SSRFPolicy(),
    resolver: AddressResolver = _system_resolver,
) -> ValidatedURL:
    """Validate one fetch/callback hop and pin all resolved addresses.

    Callers must invoke this function again for every redirect and again at
    connection time.  Returning addresses does not authorize an HTTP client to
    follow redirects or re-resolve without comparison.
    """

    if not isinstance(url, str) or not url or len(url) > policy.max_url_length:
        raise ValidationError("A2A URL is empty or exceeds the boundary limit")
    if _URL_CONTROL_PATTERN.search(url):
        raise ValidationError("A2A URL contains ambiguous or control characters")
    parsed = urlsplit(url)
    scheme = parsed.scheme.lower()
    lab_http = scheme == "http" and policy.allow_loopback_http_lab
    if scheme != "https" and not lab_http:
        raise ValidationError("A2A URL must use HTTPS (HTTP is loopback-lab only)")
    if parsed.username is not None or parsed.password is not None:
        raise ValidationError("A2A URL userinfo is forbidden")
    if parsed.fragment:
        raise ValidationError("A2A URL fragments are forbidden")

    host = _normalized_hostname(parsed)
    port = _port(parsed)
    if port not in policy.allowed_ports:
        raise ValidationError("A2A URL port is not allowlisted")

    normalized_allowlist = {
        candidate.rstrip(".").encode("idna").decode("ascii").lower()
        for candidate in policy.allowed_hosts
    }
    if normalized_allowlist and host not in normalized_allowlist:
        raise ValidationError("A2A URL host is not allowlisted")

    try:
        literal = ipaddress.ip_address(host)
    except ValueError:
        if lab_http:
            raise ValidationError("loopback-lab HTTP requires a literal loopback IP address") from None
        raw_addresses = tuple(resolver(host, port))
    else:
        if lab_http and not literal.is_loopback:
            raise ValidationError("HTTP is allowed only for a literal loopback lab endpoint")
        raw_addresses = (str(literal),)
    if not raw_addresses:
        raise ValidationError("A2A URL resolved to no addresses")

    addresses: set[str] = set()
    allow_private = policy.allow_private_for_allowlisted_hosts and host in normalized_allowlist
    for raw_address in raw_addresses:
        try:
            address = ipaddress.ip_address(raw_address.split("%", 1)[0])
        except ValueError as exc:
            raise ValidationError("A2A URL resolver returned an invalid address") from exc
        if lab_http and not address.is_loopback:
            raise ValidationError("loopback-lab HTTP resolved outside loopback")
        if not lab_http and not address.is_global and not allow_private:
            raise ValidationError("A2A URL resolved to a non-public address")
        addresses.add(str(address))
    return ValidatedURL(
        url=url,
        scheme=cast("Literal['https', 'http']", scheme),
        host=host,
        port=port,
        addresses=tuple(sorted(addresses)),
    )


def validate_redirect_chain(
    urls: Iterable[str],
    *,
    policy: SSRFPolicy = SSRFPolicy(),
    resolver: AddressResolver = _system_resolver,
) -> tuple[ValidatedURL, ...]:
    return tuple(validate_outbound_url(url, policy=policy, resolver=resolver) for url in urls)


def _security_scheme_urls(card: AgentCard) -> Iterable[str]:
    for scheme in card.security_schemes.values():
        variant = scheme.WhichOneof("scheme")
        if variant == "open_id_connect_security_scheme":
            url = scheme.open_id_connect_security_scheme.open_id_connect_url
            if url:
                yield url
        elif variant == "oauth2_security_scheme":
            oauth = scheme.oauth2_security_scheme
            if oauth.oauth2_metadata_url:
                yield oauth.oauth2_metadata_url
            for flow_descriptor in oauth.flows.DESCRIPTOR.fields:
                if not oauth.flows.HasField(flow_descriptor.name):
                    continue
                flow = getattr(oauth.flows, flow_descriptor.name)
                for field_descriptor, value in flow.ListFields():
                    if field_descriptor.name.endswith("_url") and value:
                        yield cast("str", value)


def _exact_interfaces(
    card: AgentCard,
    *,
    expected_tenant: str | None,
    policy: SSRFPolicy,
    resolver: AddressResolver,
) -> tuple[AgentInterface, ...]:
    by_binding: dict[str, AgentInterface] = {}
    tenants: set[str] = set()
    for interface in card.supported_interfaces:
        if interface.protocol_version != A2A_WIRE_VERSION:
            continue
        if interface.protocol_binding not in PREFERRED_BINDINGS:
            continue
        if interface.protocol_binding in by_binding:
            raise ValidationError("A2A Agent Card has ambiguous duplicate interfaces")
        validate_outbound_url(interface.url, policy=policy, resolver=resolver)
        if expected_tenant is not None and interface.tenant != expected_tenant:
            raise ValidationError("A2A Agent Card tenant route mismatch")
        if interface.tenant:
            tenants.add(interface.tenant)
        by_binding[interface.protocol_binding] = interface
    if not by_binding:
        raise ValidationError("A2A Agent Card has no exact v1.0 HTTP+JSON or JSONRPC interface")
    if len(tenants) > 1:
        raise ValidationError("A2A Agent Card interfaces disagree on tenant route")
    return tuple(by_binding[binding] for binding in PREFERRED_BINDINGS if binding in by_binding)


def strict_agent_card(
    card: AgentCard,
    *,
    expected_tenant: str | None = None,
    policy: SSRFPolicy = SSRFPolicy(),
    resolver: AddressResolver = _system_resolver,
) -> AgentCard:
    """Return a clone containing only safe exact-v1 interfaces."""

    interfaces = _exact_interfaces(
        card,
        expected_tenant=expected_tenant,
        policy=policy,
        resolver=resolver,
    )
    for url in (card.documentation_url, card.icon_url, *_security_scheme_urls(card)):
        if url:
            validate_outbound_url(url, policy=policy, resolver=resolver)
    clone = AgentCard()
    clone.CopyFrom(card)
    clone.ClearField("supported_interfaces")
    clone.supported_interfaces.extend(interfaces)
    return clone


def select_preferred_interface(
    card: AgentCard,
    *,
    expected_tenant: str | None = None,
    policy: SSRFPolicy = SSRFPolicy(),
    resolver: AddressResolver = _system_resolver,
) -> AgentInterface:
    strict = strict_agent_card(
        card,
        expected_tenant=expected_tenant,
        policy=policy,
        resolver=resolver,
    )
    selected = AgentInterface()
    selected.CopyFrom(strict.supported_interfaces[0])
    return selected


def create_strict_client(
    card: AgentCard,
    *,
    httpx_client: httpx.AsyncClient,
    policy: SSRFPolicy = SSRFPolicy(),
    resolver: AddressResolver = _system_resolver,
) -> Client:
    strict = strict_agent_card(card, policy=policy, resolver=resolver)
    sdk_card = AgentCard()
    sdk_card.CopyFrom(strict)
    # The v1 SDK's HTTP tenant decorator appends ``/{tenant}`` to its base
    # URL.  Published cards expose the final opaque per-agent URL, so adapt a
    # private client-only clone back to the SDK's expected base spelling.
    for interface in sdk_card.supported_interfaces:
        if interface.protocol_binding != HTTP_JSON_BINDING or not interface.tenant:
            continue
        parsed = urlsplit(interface.url)
        suffix = f"/{interface.tenant}"
        if parsed.path.endswith(suffix):
            base_path = parsed.path[: -len(suffix)] or "/"
            interface.url = parsed._replace(path=base_path).geturl().rstrip("/")
    config = ClientConfig(
        httpx_client=httpx_client,
        supported_protocol_bindings=list(PREFERRED_BINDINGS),
        use_client_preference=True,
        streaming=True,
    )
    return ClientFactory(config).create(sdk_card)


def _public_origin(public_base_url: str, *, allow_loopback_http_lab: bool = False) -> str:
    if not isinstance(public_base_url, str) or _URL_CONTROL_PATTERN.search(public_base_url):
        raise ValidationError("public A2A base URL is invalid")
    parsed = urlsplit(public_base_url)
    scheme = parsed.scheme.lower()
    if scheme not in {"https", "http"} or not parsed.hostname:
        raise ValidationError("public A2A base URL must be an HTTPS origin")
    if parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
        raise ValidationError("public A2A base URL must not contain credentials, query, or fragment")
    if parsed.path not in {"", "/"}:
        raise ValidationError("public A2A base URL must be an origin without a path")
    host = _normalized_hostname(parsed)
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        address = None
    if scheme == "http":
        if not allow_loopback_http_lab or address is None or not address.is_loopback:
            raise ValidationError("public A2A HTTP is allowed only for an explicit literal-loopback lab origin")
    port = _port(parsed)
    rendered_host = f"[{host}]" if isinstance(address, ipaddress.IPv6Address) else host
    default_port = 80 if scheme == "http" else 443
    authority = rendered_host if port == default_port else f"{rendered_host}:{port}"
    return f"{scheme}://{authority}"


def build_exported_agent_card(
    template: AgentCard,
    *,
    route: OpaqueAgentRoute,
    public_base_url: str,
    allow_loopback_http_lab: bool = False,
) -> AgentCard:
    """Bind one logical agent card to its opaque fixed route."""

    origin = _public_origin(
        public_base_url,
        allow_loopback_http_lab=allow_loopback_http_lab,
    )
    card = AgentCard()
    card.CopyFrom(template)
    # Proto3 omits empty repeated fields from ProtoJSON, while AgentCard marks
    # these fields required on the wire. Every exported AgentNet agent has a real,
    # deliberately narrow public skill: accepting inert text proposals. This
    # describes containment; it does not advertise corporate execution.
    if not card.default_input_modes:
        card.default_input_modes.append("text/plain")
    if not card.default_output_modes:
        card.default_output_modes.append("text/plain")
    if not card.skills:
        card.skills.append(
            AgentSkill(
                id="agentnet-tainted-proposal-ingress",
                name="Tainted proposal ingress",
                description=(
                    "Accepts text as inert, non-executable public proposal input; "
                    "corporate execution requires separate enrolled authority."
                ),
                tags=["proposal", "non-executable", "text"],
                input_modes=["text/plain"],
                output_modes=["text/plain"],
            )
        )
    card.ClearField("supported_interfaces")
    card.supported_interfaces.extend(
        (
            AgentInterface(
                # The dynamic SDK tenant mount is deliberately removed below,
                # so the advertised HTTP+JSON base must be the fixed opaque
                # per-agent route as well as carrying the tenant echo value.
                url=f"{origin}{route.path_prefix}",
                protocol_binding=HTTP_JSON_BINDING,
                protocol_version=A2A_WIRE_VERSION,
                tenant=route.tenant,
            ),
            AgentInterface(
                url=f"{origin}{route.path_prefix}/rpc",
                protocol_binding=JSONRPC_BINDING,
                protocol_version=A2A_WIRE_VERSION,
                tenant=route.tenant,
            ),
        )
    )
    return card


def select_card_security_requirement(
    card: AgentCard,
    *,
    available_scheme_scopes: Mapping[str, Collection[str]],
    locally_allowed_schemes: Collection[str],
    allow_anonymous: bool = False,
) -> A2ASecuritySelection:
    """Select OR alternatives while requiring AND within one alternative."""

    allowed = frozenset(locally_allowed_schemes)
    requirements = tuple(card.security_requirements)
    for requirement in requirements:
        for scheme_name in requirement.schemes:
            if not scheme_name or scheme_name not in card.security_schemes:
                raise ValidationError("A2A security requirement references an undefined scheme")
    if not requirements:
        if allow_anonymous:
            return A2ASecuritySelection(alternative_index=-1, anonymous=True)
        raise AuthenticationError("A2A Agent Card has no acceptable security requirement")

    for alternative_index, requirement in enumerate(requirements):
        required = {
            name: tuple(value.list)
            for name, value in cast("SecurityRequirement", requirement).schemes.items()
        }
        if not required:
            if allow_anonymous:
                return A2ASecuritySelection(alternative_index=alternative_index, anonymous=True)
            continue
        if any(name not in allowed or name not in available_scheme_scopes for name in required):
            continue
        if any(
            not frozenset(scopes).issubset(frozenset(available_scheme_scopes[name]))
            for name, scopes in required.items()
        ):
            continue
        ordered_names = tuple(required)
        return A2ASecuritySelection(
            alternative_index=alternative_index,
            schemes=ordered_names,
            scopes={name: tuple(required[name]) for name in ordered_names},
        )
    raise AuthenticationError("no A2A security requirement alternative is satisfied")


def require_standing_grant(
    grant: StandingA2AGrant | None,
    *,
    route: OpaqueAgentRoute,
    now: datetime,
    peer_namespace: str | None = None,
    action: str | None = None,
    resource: str | None = None,
) -> StandingA2AGrant:
    """Fail closed on missing, expired, revoked, or out-of-scope export grants."""

    if now.tzinfo is None:
        raise ValidationError("standing grant check requires timezone-aware time")
    if grant is None:
        raise AuthorizationError("missing standing A2A grant")
    if not secrets.compare_digest(grant.route_token, route.route_token):
        raise AuthorizationError("standing A2A grant route mismatch")
    if grant.logical_agent_id != route.logical_agent_id:
        raise AuthorizationError("standing A2A grant agent mismatch")
    if grant.revoked_at is not None or grant.expires_at <= now:
        raise AuthorizationError("standing A2A grant is revoked or expired")
    if peer_namespace is not None and grant.allowed_peer_namespaces:
        if peer_namespace not in grant.allowed_peer_namespaces:
            raise AuthorizationError("standing A2A grant peer mismatch")
    if action is not None and action not in grant.allowed_actions:
        raise AuthorizationError("standing A2A grant action mismatch")
    if resource is not None and resource not in grant.allowed_resources:
        raise AuthorizationError("standing A2A grant resource mismatch")
    return grant


class A2AGatewayContextBuilder(ServerCallContextBuilder):
    """Build an SDK context containing only a low-trust external actor."""

    def __init__(
        self,
        *,
        route: OpaqueAgentRoute,
        grant_lookup: GrantLookup,
        peer_resolver: PeerResolver,
        clock: Clock = lambda: datetime.now(UTC),
        base: ServerCallContextBuilder | None = None,
    ) -> None:
        self.route = route
        self.grant_lookup = grant_lookup
        self.peer_resolver = peer_resolver
        self.clock = clock
        self.base = base or DefaultServerCallContextBuilder()

    def build(self, request: Request) -> ServerCallContext:
        corporate_actor = getattr(request.state, "a2a_corporate_actor", None)
        if corporate_actor is None:
            raw_peer_id = self.peer_resolver(request)
            peer_namespace = external_peer_namespace(raw_peer_id)
            actor = VerifiedActor(
                kind=ActorKind.EXTERNAL_A2A,
                domain_id=self.route.domain_id,
                external_peer_id=peer_namespace,
                binding_assurance="external",
            )
            identity_mode = "external_unverified"
        else:
            if not isinstance(corporate_actor, VerifiedActor):
                raise AuthenticationError("corporate A2A transport actor is invalid")
            actor = corporate_actor
            peer_namespace = corporate_peer_namespace(actor)
            identity_mode = "corporate_verified"
        grant = require_standing_grant(
            self.grant_lookup(self.route.route_token),
            route=self.route,
            now=self.clock(),
            peer_namespace=peer_namespace,
        )
        context = self.base.build(request)
        context.tenant = self.route.tenant
        context.state.update(
            {
                "verified_actor": actor,
                "a2a_peer_namespace": peer_namespace,
                "a2a_identity_mode": identity_mode,
                "a2a_route_token": self.route.route_token,
                "a2a_logical_agent_id": self.route.logical_agent_id,
                "a2a_standing_grant_id": grant.grant_id,
                "a2a_standing_grant_revision": grant.revision,
            }
        )
        if actor.kind is ActorKind.EXTERNAL_A2A:
            context.state["a2a_external_peer_namespace"] = peer_namespace
        return context


class BoundedA2ARequestHandler(RequestHandler):
    """Recheck route, tenant, peer, action, and revocation on every operation."""

    def __init__(
        self,
        delegate: RequestHandler,
        *,
        route: OpaqueAgentRoute,
        grant_lookup: GrantLookup,
        url_validator: URLValidator,
        streaming_enabled: bool = True,
        push_notifications_enabled: bool = True,
        extended_card_enabled: bool = True,
        clock: Clock = lambda: datetime.now(UTC),
    ) -> None:
        self.delegate = delegate
        self.route = route
        self.grant_lookup = grant_lookup
        self.url_validator = url_validator
        self.streaming_enabled = streaming_enabled
        self.push_notifications_enabled = push_notifications_enabled
        self.extended_card_enabled = extended_card_enabled
        self.clock = clock

    def _authorize(self, params: Any, context: ServerCallContext, *, action: str) -> StandingA2AGrant:
        actor = context.state.get("verified_actor")
        if not isinstance(actor, VerifiedActor):
            raise AuthenticationError("A2A request lacks a verified transport actor")
        peer_namespace = context.state.get("a2a_peer_namespace")
        if not isinstance(peer_namespace, str):
            raise AuthenticationError("A2A peer binding is missing")
        if actor.kind is ActorKind.EXTERNAL_A2A:
            if actor.positive_authority_id is not None or peer_namespace != actor.external_peer_id:
                raise AuthenticationError("A2A external peer binding is inconsistent")
        elif actor.kind in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            if actor.positive_authority_id is None or peer_namespace != corporate_peer_namespace(actor):
                raise AuthenticationError("A2A corporate peer binding is inconsistent")
        else:
            raise AuthenticationError("A2A workload identities cannot enter through the human peer gateway")
        if context.state.get("a2a_identity_mode") not in {"external_unverified", "corporate_verified"}:
            raise AuthenticationError("A2A peer binding is missing or inconsistent")
        parameter_tenant = getattr(params, "tenant", "")
        if parameter_tenant and parameter_tenant != self.route.tenant:
            raise AuthorizationError("A2A tenant route mismatch")
        if context.tenant and context.tenant != self.route.tenant:
            raise AuthorizationError("A2A context tenant route mismatch")
        if not parameter_tenant and not context.tenant:
            raise AuthorizationError("A2A tenant route is missing")
        return require_standing_grant(
            self.grant_lookup(self.route.route_token),
            route=self.route,
            now=self.clock(),
            peer_namespace=peer_namespace,
            action=action,
            resource=self.route.logical_agent_id,
        )

    async def on_get_task(self, params: GetTaskRequest, context: ServerCallContext) -> Task | None:
        self._authorize(params, context, action="a2a.task.get")
        return await self.delegate.on_get_task(params, context)

    async def on_list_tasks(self, params: ListTasksRequest, context: ServerCallContext) -> ListTasksResponse:
        self._authorize(params, context, action="a2a.task.list")
        return await self.delegate.on_list_tasks(params, context)

    async def on_cancel_task(self, params: CancelTaskRequest, context: ServerCallContext) -> Task | None:
        self._authorize(params, context, action="a2a.task.cancel")
        return await self.delegate.on_cancel_task(params, context)

    async def on_message_send(self, params: SendMessageRequest, context: ServerCallContext) -> Task | Message:
        self._authorize(params, context, action="a2a.message.send")
        if params.message.role not in {Role.ROLE_USER, Role.ROLE_AGENT}:
            raise ValidationError("A2A inbound Message role is unspecified")
        validate_message_part_urls(params.message, self.url_validator)
        return await self.delegate.on_message_send(params, context)

    async def on_message_send_stream(
        self,
        params: SendMessageRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Any]:
        if not self.streaming_enabled:
            raise UnsupportedOperationError(message="streaming is not enabled by this agent")
        self._authorize(params, context, action="a2a.message.stream")
        if params.message.role not in {Role.ROLE_USER, Role.ROLE_AGENT}:
            raise ValidationError("A2A inbound Message role is unspecified")
        validate_message_part_urls(params.message, self.url_validator)
        async for event in self.delegate.on_message_send_stream(params, context):
            self._authorize(params, context, action="a2a.message.stream")
            yield event

    async def on_create_task_push_notification_config(
        self,
        params: TaskPushNotificationConfig,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        if not self.push_notifications_enabled:
            raise PushNotificationNotSupportedError()
        self._authorize(params, context, action="a2a.push.create")
        self.url_validator(params.url)
        return await self.delegate.on_create_task_push_notification_config(params, context)

    async def on_get_task_push_notification_config(
        self,
        params: GetTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        if not self.push_notifications_enabled:
            raise PushNotificationNotSupportedError()
        self._authorize(params, context, action="a2a.push.get")
        return await self.delegate.on_get_task_push_notification_config(params, context)

    async def on_subscribe_to_task(
        self,
        params: SubscribeToTaskRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Any]:
        if not self.streaming_enabled:
            raise UnsupportedOperationError(message="streaming is not enabled by this agent")
        self._authorize(params, context, action="a2a.task.subscribe")
        async for event in self.delegate.on_subscribe_to_task(params, context):
            self._authorize(params, context, action="a2a.task.subscribe")
            yield event

    async def on_list_task_push_notification_configs(
        self,
        params: ListTaskPushNotificationConfigsRequest,
        context: ServerCallContext,
    ) -> ListTaskPushNotificationConfigsResponse:
        if not self.push_notifications_enabled:
            raise PushNotificationNotSupportedError()
        self._authorize(params, context, action="a2a.push.list")
        return await self.delegate.on_list_task_push_notification_configs(params, context)

    async def on_delete_task_push_notification_config(
        self,
        params: DeleteTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> None:
        if not self.push_notifications_enabled:
            raise PushNotificationNotSupportedError()
        self._authorize(params, context, action="a2a.push.delete")
        await self.delegate.on_delete_task_push_notification_config(params, context)

    async def on_get_extended_agent_card(
        self,
        params: GetExtendedAgentCardRequest,
        context: ServerCallContext,
    ) -> AgentCard:
        if not self.extended_card_enabled:
            raise ExtendedAgentCardNotConfiguredError()
        self._authorize(params, context, action="a2a.card.extended")
        return await self.delegate.on_get_extended_agent_card(params, context)


def _extension_to_a2a_error(error: ExtensionError) -> A2AError:
    """Translate typed internal failures to non-disclosing native A2A errors."""

    if isinstance(error, (AuthorizationError, GateBlocked)):
        return UnsupportedOperationError(message="operation is not available")
    if isinstance(error, UnsupportedMediaTypeError):
        return ContentTypeNotSupportedError(message="message media type is not supported")
    if isinstance(error, ValidationError):
        return InvalidParamsError(message="request parameters are invalid")
    if isinstance(error, AuthenticationError):
        return InvalidRequestError(message="request authentication is invalid")
    return InvalidRequestError(message="request conflicts with existing state")


class _A2AWireContextBuilder(ServerCallContextBuilder):
    """Translate failures raised while the SDK constructs call context."""

    def __init__(self, delegate: ServerCallContextBuilder) -> None:
        self.delegate = delegate

    def build(self, request: Request) -> ServerCallContext:
        try:
            return self.delegate.build(request)
        except ExtensionError as exc:
            raise _extension_to_a2a_error(exc) from exc


class _A2AWireRequestHandler(RequestHandler):
    """Last internal-to-A2A exception boundary before official dispatchers."""

    def __init__(self, delegate: RequestHandler) -> None:
        self.delegate = delegate

    @staticmethod
    async def _unary(operation: Awaitable[Any]) -> Any:
        try:
            return await operation
        except ExtensionError as exc:
            raise _extension_to_a2a_error(exc) from exc

    @staticmethod
    async def _stream(operation: AsyncIterator[Any]) -> AsyncIterator[Any]:
        try:
            async for item in operation:
                yield item
        except ExtensionError as exc:
            raise _extension_to_a2a_error(exc) from exc

    async def on_get_task(self, params: GetTaskRequest, context: ServerCallContext) -> Task | None:
        return cast("Task | None", await self._unary(self.delegate.on_get_task(params, context)))

    async def on_list_tasks(self, params: ListTasksRequest, context: ServerCallContext) -> ListTasksResponse:
        return cast(
            "ListTasksResponse",
            await self._unary(self.delegate.on_list_tasks(params, context)),
        )

    async def on_cancel_task(self, params: CancelTaskRequest, context: ServerCallContext) -> Task | None:
        return cast("Task | None", await self._unary(self.delegate.on_cancel_task(params, context)))

    async def on_message_send(self, params: SendMessageRequest, context: ServerCallContext) -> Task | Message:
        return cast(
            "Task | Message",
            await self._unary(self.delegate.on_message_send(params, context)),
        )

    async def on_message_send_stream(
        self,
        params: SendMessageRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Any]:
        async for item in self._stream(self.delegate.on_message_send_stream(params, context)):
            yield item

    async def on_create_task_push_notification_config(
        self,
        params: TaskPushNotificationConfig,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        return cast(
            "TaskPushNotificationConfig",
            await self._unary(
                self.delegate.on_create_task_push_notification_config(params, context)
            ),
        )

    async def on_get_task_push_notification_config(
        self,
        params: GetTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        return cast(
            "TaskPushNotificationConfig",
            await self._unary(
                self.delegate.on_get_task_push_notification_config(params, context)
            ),
        )

    async def on_subscribe_to_task(
        self,
        params: SubscribeToTaskRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Any]:
        async for item in self._stream(self.delegate.on_subscribe_to_task(params, context)):
            yield item

    async def on_list_task_push_notification_configs(
        self,
        params: ListTaskPushNotificationConfigsRequest,
        context: ServerCallContext,
    ) -> ListTaskPushNotificationConfigsResponse:
        return cast(
            "ListTaskPushNotificationConfigsResponse",
            await self._unary(
                self.delegate.on_list_task_push_notification_configs(params, context)
            ),
        )

    async def on_delete_task_push_notification_config(
        self,
        params: DeleteTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> None:
        await self._unary(
            self.delegate.on_delete_task_push_notification_config(params, context)
        )

    async def on_get_extended_agent_card(
        self,
        params: GetExtendedAgentCardRequest,
        context: ServerCallContext,
    ) -> AgentCard:
        return cast(
            "AgentCard",
            await self._unary(self.delegate.on_get_extended_agent_card(params, context)),
        )


class TaintedProposalHandler(RequestHandler):
    """Minimal runnable SDK handler for inert external proposals.

    It deliberately performs no semantic work, data read, tool call, or effect.
    The SDK task store is process-local and therefore provides no corporate
    durability claim; production handlers must hand the proposal to the
    canonical transactional core before asserting a corporate acceptance fact.
    """

    def __init__(
        self,
        agent_card: AgentCard,
        *,
        response_mode: Literal["task", "message"] = "task",
        task_store: InMemoryTaskStore | None = None,
    ) -> None:
        self.agent_card = AgentCard()
        self.agent_card.CopyFrom(agent_card)
        self.response_mode = response_mode
        self.task_store = task_store or InMemoryTaskStore(
            owner_resolver=self._external_owner,
        )

    @staticmethod
    def _external_owner(context: ServerCallContext) -> str:
        peer = context.state.get("a2a_external_peer_namespace")
        if not isinstance(peer, str) or not peer:
            raise AuthenticationError("A2A proposal lacks external peer custody scope")
        return peer

    @staticmethod
    def _require_external_context(context: ServerCallContext) -> VerifiedActor:
        actor = context.state.get("verified_actor")
        if not isinstance(actor, VerifiedActor) or actor.kind is not ActorKind.EXTERNAL_A2A:
            raise AuthenticationError("A2A proposal must originate from an external actor")
        if actor.positive_authority_id is not None:
            raise AuthenticationError("A2A proposal cannot carry corporate positive authority")
        return actor

    @staticmethod
    def _metadata(context: ServerCallContext) -> dict[str, Any]:
        return {
            "agentnetActorKind": "external_human_unverified",
            "agentnetDisposition": "tainted_non_executable_proposal",
            "agentnetAuthorityEligible": False,
            "agentnetEffectAuthorized": False,
            "agentnetStandingGrantId": str(context.state.get("a2a_standing_grant_id", "")),
        }

    async def on_get_task(self, params: GetTaskRequest, context: ServerCallContext) -> Task | None:
        self._require_external_context(context)
        return await self.task_store.get(params.id, context)

    async def on_list_tasks(self, params: ListTasksRequest, context: ServerCallContext) -> ListTasksResponse:
        self._require_external_context(context)
        return await self.task_store.list(params, context)

    async def on_cancel_task(self, params: CancelTaskRequest, context: ServerCallContext) -> Task | None:
        self._require_external_context(context)
        task = await self.task_store.get(params.id, context)
        if task is None:
            return None
        task.status.CopyFrom(TaskStatus(state=TaskState.TASK_STATE_CANCELED))
        await self.task_store.save(task, context)
        return task

    async def on_message_send(self, params: SendMessageRequest, context: ServerCallContext) -> Task | Message:
        self._require_external_context(context)
        if not params.message.parts or any(
            part.WhichOneof("content") != "text"
            or part.media_type not in {"", "text/plain"}
            for part in params.message.parts
        ):
            raise UnsupportedMediaTypeError(
                "inert proposal ingress accepts only its advertised text/plain input mode"
            )
        context_id = str(uuid4())
        metadata = self._metadata(context)
        if self.response_mode == "message":
            return Message(
                message_id=str(uuid4()),
                context_id=context_id,
                role=Role.ROLE_AGENT,
                parts=[Part(text="Proposal received as tainted, non-executable input.")],
                metadata=metadata,
            )
        task = Task(
            id=str(uuid4()),
            context_id=context_id,
            status=TaskStatus(state=TaskState.TASK_STATE_SUBMITTED),
            history=[params.message],
            metadata=metadata,
        )
        await self.task_store.save(task, context)
        return task

    async def on_message_send_stream(
        self,
        params: SendMessageRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Task | Message]:
        yield await self.on_message_send(params, context)

    async def on_create_task_push_notification_config(
        self,
        params: TaskPushNotificationConfig,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        del params, context
        raise UnsupportedOperationError(message="push notifications are not enabled in the inert proposal handler")

    async def on_get_task_push_notification_config(
        self,
        params: GetTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        del params, context
        raise UnsupportedOperationError(message="push notifications are not enabled in the inert proposal handler")

    async def on_subscribe_to_task(
        self,
        params: SubscribeToTaskRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Task]:
        self._require_external_context(context)
        task = await self.task_store.get(params.id, context)
        if task is None:
            raise TaskNotFoundError(message="task not found")
        yield task

    async def on_list_task_push_notification_configs(
        self,
        params: ListTaskPushNotificationConfigsRequest,
        context: ServerCallContext,
    ) -> ListTaskPushNotificationConfigsResponse:
        del params, context
        raise UnsupportedOperationError(message="push notifications are not enabled in the inert proposal handler")

    async def on_delete_task_push_notification_config(
        self,
        params: DeleteTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> None:
        del params, context
        raise UnsupportedOperationError(message="push notifications are not enabled in the inert proposal handler")

    async def on_get_extended_agent_card(
        self,
        params: GetExtendedAgentCardRequest,
        context: ServerCallContext,
    ) -> AgentCard:
        del params
        self._require_external_context(context)
        card = AgentCard()
        card.CopyFrom(self.agent_card)
        return card


def create_tainted_proposal_handler(
    agent_card: AgentCard,
    *,
    response_mode: Literal["task", "message"] = "task",
) -> TaintedProposalHandler:
    return TaintedProposalHandler(agent_card, response_mode=response_mode)


def _aip193_error(
    *,
    status_code: int,
    status: str,
    reason: str,
    message: str,
) -> JSONResponse:
    return JSONResponse(
        {
            "error": {
                "code": status_code,
                "status": status,
                "message": message,
                "details": [
                    {
                        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                        "reason": reason,
                        "domain": "a2a-protocol.org",
                        "metadata": {},
                    }
                ],
            }
        },
        status_code=status_code,
        media_type="application/json",
    )


def _version_error() -> JSONResponse:
    return _aip193_error(
        status_code=400,
        status="FAILED_PRECONDITION",
        reason="VERSION_NOT_SUPPORTED",
        message="A2A-Version must be exactly 1.0",
    )


def _media_error() -> JSONResponse:
    return _aip193_error(
        status_code=415,
        status="INVALID_ARGUMENT",
        reason="CONTENT_TYPE_NOT_SUPPORTED",
        message="A2A request content type must be application/json",
    )


def _extension_wire_error(error: ExtensionError) -> JSONResponse:
    if isinstance(error, AuthenticationError):
        return _aip193_error(
            status_code=401,
            status="UNAUTHENTICATED",
            reason="INVALID_REQUEST",
            message="request authentication is invalid",
        )
    if isinstance(error, AuthorizationError):
        return _aip193_error(
            status_code=404,
            status="NOT_FOUND",
            reason="TASK_NOT_FOUND",
            message="requested resource is unavailable",
        )
    if isinstance(error, GateBlocked):
        return _aip193_error(
            status_code=503,
            status="UNAVAILABLE",
            reason="UNSUPPORTED_OPERATION",
            message="operation is unavailable",
        )
    if isinstance(error, ValidationError):
        return _aip193_error(
            status_code=400,
            status="INVALID_ARGUMENT",
            reason="INVALID_PARAMS",
            message="request parameters are invalid",
        )
    return _aip193_error(
        status_code=409,
        status="ABORTED",
        reason="INVALID_REQUEST",
        message="request conflicts with existing state",
    )


def _jsonrpc_boundary_error(body: bytes, error: A2AError) -> JSONResponse:
    request_id: str | int | None = None
    try:
        decoded = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError):
        decoded = None
    if isinstance(decoded, dict):
        supplied_id = decoded.get("id")
        if isinstance(supplied_id, str | int) and not isinstance(supplied_id, bool):
            request_id = supplied_id
    return JSONResponse(
        build_error_response(request_id, error),
        status_code=200,
        media_type="application/json",
    )


def _normalize_sdk_rest_error(response: Response) -> Response:
    """Correct one pinned-SDK HTTP mapping without broad error reinterpretation."""

    body = getattr(response, "body", None)
    if response.status_code != 400 or not isinstance(body, bytes):
        return response
    try:
        payload = json.loads(body)
        error = payload["error"]
        details = error["details"]
        reasons = {
            detail.get("reason")
            for detail in details
            if isinstance(detail, dict)
        }
    except (KeyError, TypeError, json.JSONDecodeError, UnicodeDecodeError):
        return response
    if (
        not isinstance(error, dict)
        or error.get("code") != 400
        or error.get("status") != "INVALID_ARGUMENT"
        or reasons != {"CONTENT_TYPE_NOT_SUPPORTED"}
    ):
        return response
    normalized = dict(payload)
    normalized_error = dict(error)
    normalized_error["code"] = 415
    normalized["error"] = normalized_error
    return JSONResponse(normalized, status_code=415, media_type="application/json")


def _strict_wire_endpoint(
    endpoint: Callable[[Request], Any],
    *,
    protocol_binding: str,
    corporate_authenticator: CorporateRequestAuthenticator | None,
    max_request_bytes: int,
) -> Callable[[Request], Any]:
    async def guarded(request: Request) -> Response:
        raw_path = request.scope.get("raw_path", request.url.path.encode("ascii", "ignore"))
        raw_query = request.scope.get("query_string", b"")
        if len(raw_path) + len(raw_query) > MAX_A2A_REQUEST_TARGET_BYTES:
            return _aip193_error(
                status_code=414,
                status="INVALID_ARGUMENT",
                reason="INVALID_REQUEST",
                message="A2A request target is too large",
            )
        is_jsonrpc = protocol_binding == JSONRPC_BINDING
        versions = request.headers.getlist(VERSION_HEADER)
        invalid_version = len(versions) != 1 or versions[0] != A2A_WIRE_VERSION
        if invalid_version and not is_jsonrpc:
            return _version_error()
        invalid_media = False
        if request.method in {"POST", "PUT", "PATCH"}:
            content_types = request.headers.getlist("content-type")
            if len(content_types) != 1:
                invalid_media = True
            else:
                media_type = content_types[0].split(";", 1)[0].strip().lower()
                invalid_media = media_type != "application/json"
            if invalid_media and not is_jsonrpc:
                return _media_error()
        content_lengths = request.headers.getlist("content-length")
        if len(content_lengths) > 1:
            return _aip193_error(
                status_code=400,
                status="INVALID_ARGUMENT",
                reason="INVALID_REQUEST",
                message="A2A Content-Length is ambiguous",
            )
        raw_length = content_lengths[0] if content_lengths else None
        if raw_length is not None:
            try:
                if int(raw_length) > max_request_bytes:
                    return _aip193_error(
                        status_code=413,
                        status="RESOURCE_EXHAUSTED",
                        reason="INVALID_REQUEST",
                        message="A2A payload is too large",
                    )
            except ValueError:
                return _aip193_error(
                    status_code=400,
                    status="INVALID_ARGUMENT",
                    reason="INVALID_REQUEST",
                    message="A2A Content-Length is invalid",
                )
        body = await request.body()
        if len(body) > max_request_bytes:
            return _aip193_error(
                status_code=413,
                status="RESOURCE_EXHAUSTED",
                reason="INVALID_REQUEST",
                message="A2A payload is too large",
            )
        if is_jsonrpc and invalid_version:
            return _jsonrpc_boundary_error(
                body,
                VersionNotSupportedError(message="A2A-Version must be exactly 1.0"),
            )
        if is_jsonrpc and invalid_media:
            return _jsonrpc_boundary_error(
                body,
                ContentTypeNotSupportedError(
                    message="A2A request content type must be application/json"
                ),
            )
        if corporate_authenticator is not None:
            try:
                actor = corporate_authenticator(request, body)
            except ExtensionError as exc:
                return _extension_wire_error(exc)
            if actor is not None:
                request.state.a2a_corporate_actor = actor
        result = endpoint(request)
        if inspect.isawaitable(result):
            response = cast("Response", await result)
        else:
            response = cast("Response", result)
        return (
            _normalize_sdk_rest_error(response)
            if protocol_binding == HTTP_JSON_BINDING
            else response
        )

    return guarded


def _card_endpoint(
    endpoint: Callable[[Request], Any],
    *,
    route: OpaqueAgentRoute,
    grant_lookup: GrantLookup,
    clock: Clock,
    last_modified: datetime,
) -> Callable[[Request], Any]:
    if last_modified.tzinfo is None:
        raise ValidationError("Agent Card last-modified time must be timezone-aware")
    normalized_last_modified = last_modified.astimezone(UTC).replace(microsecond=0)
    last_modified_header = format_datetime(normalized_last_modified, usegmt=True)

    def cache_headers(etag: str) -> dict[str, str]:
        return {
            "Cache-Control": _AGENT_CARD_CACHE_CONTROL,
            "ETag": etag,
            "Last-Modified": last_modified_header,
        }

    def if_none_match_matches(value: str, etag: str) -> bool:
        # If-None-Match uses weak comparison for GET/HEAD.  The generated tag
        # is strong, but accepting its W/ form lets conforming caches revalidate
        # the same representation without weakening the response validator.
        for item in value.split(","):
            candidate = item.strip()
            if candidate == "*":
                return True
            if candidate.startswith("W/"):
                candidate = candidate[2:].lstrip()
            if candidate == etag:
                return True
        return False

    def if_modified_since_matches(value: str) -> bool:
        try:
            parsed = parsedate_to_datetime(value)
        except (TypeError, ValueError, OverflowError):
            return False
        if parsed.tzinfo is None:
            return False
        return normalized_last_modified <= parsed.astimezone(UTC).replace(microsecond=0)

    async def guarded(request: Request) -> Response:
        try:
            require_standing_grant(
                grant_lookup(route.route_token),
                route=route,
                now=clock(),
            )
        except AuthorizationError:
            return JSONResponse(
                {"detail": "not found"},
                status_code=404,
                headers={"Cache-Control": "no-store"},
            )
        result = endpoint(request)
        if inspect.isawaitable(result):
            result = await result
        response = cast("Response", result)
        body = getattr(response, "body", None)
        if response.status_code != 200 or not isinstance(body, bytes):
            raise ValidationError("unexpected SDK Agent Card response")
        etag = f'"{hashlib.sha256(body).hexdigest()}"'
        headers = cache_headers(etag)

        if_none_match = request.headers.get("if-none-match")
        not_modified = (
            if_none_match_matches(if_none_match, etag)
            if if_none_match is not None
            else (
                (if_modified_since := request.headers.get("if-modified-since")) is not None
                and if_modified_since_matches(if_modified_since)
            )
        )
        if not_modified:
            return Response(status_code=304, headers=headers)

        response.headers.update(headers)
        return response

    return guarded


def _clone_route(route: Route, endpoint: Callable[[Request], Any]) -> Route:
    return Route(
        path=route.path,
        endpoint=endpoint,
        methods=sorted(route.methods) if route.methods else None,
        name=route.name,
        include_in_schema=getattr(route, "include_in_schema", True),
    )


def build_starlette_routes(
    *,
    extension_config: A2AMountCapabilityConfig,
    request_handler: RequestHandler,
    agent_card: AgentCard,
    route: OpaqueAgentRoute,
    grant_lookup: GrantLookup,
    peer_resolver: PeerResolver,
    url_policy: SSRFPolicy = SSRFPolicy(),
    resolver: AddressResolver = _system_resolver,
    clock: Clock = lambda: datetime.now(UTC),
    corporate_authenticator: CorporateRequestAuthenticator | None = None,
    max_request_bytes: int = MAX_A2A_REQUEST_BYTES,
) -> list[BaseRoute]:
    """Build fixed Starlette routes using SDK dispatchers with no v0.3 mount."""

    require_a2a_gateway_mount_capability(extension_config)
    if not 1_024 <= max_request_bytes <= 16_777_216:
        raise ValidationError("A2A max_request_bytes is outside the supported boundary")
    safe_card = strict_agent_card(
        agent_card,
        expected_tenant=route.tenant,
        policy=url_policy,
        resolver=resolver,
    )
    url_validator: URLValidator = lambda url: validate_outbound_url(
        url,
        policy=url_policy,
        resolver=resolver,
    )
    context_builder = A2AGatewayContextBuilder(
        route=route,
        grant_lookup=grant_lookup,
        peer_resolver=peer_resolver,
        clock=clock,
    )
    bounded_handler = BoundedA2ARequestHandler(
        request_handler,
        route=route,
        grant_lookup=grant_lookup,
        url_validator=url_validator,
        streaming_enabled=bool(safe_card.capabilities.streaming),
        push_notifications_enabled=bool(safe_card.capabilities.push_notifications),
        extended_card_enabled=bool(safe_card.capabilities.extended_agent_card),
        clock=clock,
    )
    wire_context_builder = _A2AWireContextBuilder(context_builder)
    wire_handler = _A2AWireRequestHandler(bounded_handler)
    card_last_modified = clock()

    card_routes = create_agent_card_routes(
        safe_card,
        card_url=f"{route.path_prefix}/.well-known/agent-card.json",
    )
    rest_routes = create_rest_routes(
        wire_handler,
        context_builder=wire_context_builder,
        enable_v0_3_compat=False,
        path_prefix=route.path_prefix,
    )
    rpc_routes = create_jsonrpc_routes(
        wire_handler,
        rpc_url=f"{route.path_prefix}/rpc",
        context_builder=wire_context_builder,
        enable_v0_3_compat=False,
    )

    result: list[BaseRoute] = []
    for candidate in card_routes:
        if not isinstance(candidate, Route):
            raise ValidationError("unexpected SDK Agent Card route type")
        result.append(
            _clone_route(
                candidate,
                _card_endpoint(
                    candidate.endpoint,
                    route=route,
                    grant_lookup=grant_lookup,
                    clock=clock,
                    last_modified=card_last_modified,
                ),
            )
        )
    for candidate in (*rest_routes, *rpc_routes):
        # The SDK REST helper also emits a dynamic tenant Mount.  This gateway
        # deliberately exposes only the fixed opaque per-agent route.
        if isinstance(candidate, Mount):
            continue
        if not isinstance(candidate, Route):
            raise ValidationError("unexpected SDK A2A route type")
        result.append(
            _clone_route(
                candidate,
                _strict_wire_endpoint(
                    candidate.endpoint,
                    protocol_binding=(
                        JSONRPC_BINDING
                        if candidate.path == f"{route.path_prefix}/rpc"
                        else HTTP_JSON_BINDING
                    ),
                    corporate_authenticator=corporate_authenticator,
                    max_request_bytes=max_request_bytes,
                ),
            )
        )
    return result


__all__ = [
    "A2A_WIRE_VERSION",
    "HTTP_JSON_BINDING",
    "JSONRPC_BINDING",
    "MAX_A2A_REQUEST_BYTES",
    "MAX_A2A_REQUEST_TARGET_BYTES",
    "PREFERRED_BINDINGS",
    "A2AGatewayContextBuilder",
    "A2AMountCapabilityConfig",
    "A2ASecuritySelection",
    "AddressResolver",
    "BoundedA2ARequestHandler",
    "CorporateRequestAuthenticator",
    "GrantLookup",
    "OpaqueAgentRoute",
    "PeerResolver",
    "SSRFPolicy",
    "StandingA2AGrant",
    "TaintedProposalHandler",
    "ValidatedURL",
    "build_exported_agent_card",
    "build_starlette_routes",
    "create_strict_client",
    "create_tainted_proposal_handler",
    "generate_opaque_route",
    "corporate_peer_namespace",
    "require_a2a_gateway_mount_capability",
    "require_standing_grant",
    "select_card_security_requirement",
    "select_preferred_interface",
    "strict_agent_card",
    "system_address_resolver",
    "validate_outbound_url",
    "validate_redirect_chain",
]
