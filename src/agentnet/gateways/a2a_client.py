"""Signed, SSRF-pinned native A2A client, callbacks, and outbound journal."""

from __future__ import annotations

import asyncio
import ipaddress
import json
import time

from collections.abc import AsyncIterator, Callable, Mapping
from dataclasses import dataclass
from typing import Any
from urllib.parse import urlsplit, urlunsplit
from uuid import uuid4

import httpx

from a2a.client import Client, ClientCallContext
from a2a.types import (
    AgentCard,
    GetTaskRequest,
    Message,
    Role,
    SendMessageRequest,
    StreamResponse,
    Task,
    TaskPushNotificationConfig,
    TaskState,
)
from google.protobuf.json_format import MessageToDict, ParseDict

from agentnet.client import proof_headers
from agentnet.errors import ConflictError, IdempotencyConflict, ValidationError
from agentnet.gateways.a2a import (
    A2A_WIRE_VERSION,
    AddressResolver,
    SSRFPolicy,
    create_strict_client,
    validate_outbound_url,
)
from agentnet.protocol.a2a_mapping import (
    A2AMappedKind,
    MappedA2AFact,
    external_peer_namespace,
    map_stream_response,
    map_task,
    validate_message_part_urls,
)
from agentnet.security.dpop import canonical_request_target, create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.storage.a2a_schema import require_a2a_schema
from agentnet.storage.backend import StoreBackend

@dataclass(frozen=True, slots=True)
class CorporateA2AClientIdentity:
    key: P256KeyPair
    domain_id: str
    harness_id: str
    credential_id: str
    audience: str


class SignedA2AAuth(httpx.Auth):
    """Sign the exact HTTPX wire body and original canonical URL."""

    requires_request_body = True

    def __init__(self, identity: CorporateA2AClientIdentity) -> None:
        self.identity = identity

    def auth_flow(self, request: httpx.Request):
        raw_path = request.url.raw_path
        path_bytes, separator, query_bytes = raw_path.partition(b"?")
        del separator
        try:
            path = path_bytes.decode("ascii")
            query = query_bytes.decode("ascii")
        except UnicodeDecodeError as exc:
            raise ValidationError("A2A client URL is not canonical ASCII") from exc
        host = request.url.host
        rendered_host = f"[{host}]" if ":" in host else host
        default_port = 443 if request.url.scheme == "https" else 80
        authority = rendered_host if request.url.port in {None, default_port} else f"{rendered_host}:{request.url.port}"
        target = canonical_request_target(
            scheme=request.url.scheme,
            authority=authority,
            path=path,
            query=query,
        )
        proof = create_request_proof(
            self.identity.key,
            harness_id=self.identity.harness_id,
            credential_id=self.identity.credential_id,
            domain_id=self.identity.domain_id,
            audience=self.identity.audience,
            method=request.method,
            scheme=target.scheme,
            authority=target.authority,
            path=target.path,
            query=target.query,
            body=request.content,
        )
        request.headers.update(proof_headers(proof))
        yield request


class PinnedA2AHTTPTransport(httpx.AsyncBaseTransport):
    """Resolve once, connect to the validated IP, and retain original TLS SNI."""

    def __init__(
        self,
        *,
        policy: SSRFPolicy,
        resolver: AddressResolver,
        inner: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self.policy = policy
        self.resolver = resolver
        self.inner = inner or httpx.AsyncHTTPTransport(retries=0, trust_env=False)

    async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
        validated = validate_outbound_url(str(request.url), policy=self.policy, resolver=self.resolver)
        body = await request.aread()
        address = validated.addresses[0]
        rendered_ip = f"[{address}]" if isinstance(ipaddress.ip_address(address), ipaddress.IPv6Address) else address
        parsed = urlsplit(str(request.url))
        default_port = 80 if validated.scheme == "http" else 443
        netloc = rendered_ip if validated.port == default_port else f"{rendered_ip}:{validated.port}"
        pinned_url = urlunsplit((parsed.scheme, netloc, parsed.path, parsed.query, ""))
        headers = httpx.Headers(request.headers)
        rendered_host = f"[{validated.host}]" if ":" in validated.host else validated.host
        headers["Host"] = rendered_host if validated.port == default_port else f"{rendered_host}:{validated.port}"
        extensions = dict(request.extensions)
        if validated.scheme == "https":
            extensions["sni_hostname"] = validated.host
        pinned = httpx.Request(
            request.method,
            pinned_url,
            headers=headers,
            content=body,
            extensions=extensions,
        )
        response = await self.inner.handle_async_request(pinned)
        if 300 <= response.status_code < 400:
            await response.aclose()
            raise ValidationError("A2A endpoint redirects are rejected; publish the final validated URL")
        return response

    async def aclose(self) -> None:
        await self.inner.aclose()


class PinnedCallbackSender:
    """Deliver A2A callbacks with IP pinning, signed identity, and a hard timeout."""

    def __init__(self, client: httpx.AsyncClient, *, timeout_seconds: float = 2.0) -> None:
        if timeout_seconds <= 0 or timeout_seconds > 30:
            raise ValueError("callback timeout must be in (0, 30]")
        self.client = client
        self.timeout_seconds = timeout_seconds

    async def send(self, config: TaskPushNotificationConfig, event: StreamResponse) -> None:
        body = canonical_json(MessageToDict(event))
        headers = {
            "A2A-Version": A2A_WIRE_VERSION,
            "Content-Type": "application/json",
        }
        if config.token:
            headers["X-A2A-Notification-Token"] = config.token
        async with asyncio.timeout(self.timeout_seconds):
            response = await self.client.post(config.url, content=body, headers=headers)
            response.raise_for_status()

    async def close(self) -> None:
        await self.client.aclose()


class OutboundA2AJournal:
    def __init__(self, store: StoreBackend) -> None:
        self.store = store
        require_a2a_schema(self.store)

    @staticmethod
    def _idempotency_key(request: SendMessageRequest) -> str:
        metadata = MessageToDict(request.metadata)
        key = metadata.get("agentnetIdempotencyKey") if isinstance(metadata, dict) else None
        if not isinstance(key, str) or len(key) < 16:
            key = f"a2a-outbound:{request.message.message_id}"
        return key

    def begin(self, *, peer_id: str, request: SendMessageRequest) -> tuple[str, bool]:
        peer_namespace = external_peer_namespace(peer_id)
        request_payload = MessageToDict(request)
        digest = canonical_digest(request_payload)
        idempotency_key = self._idempotency_key(request)
        existing = self.store.fetch_one(
            """SELECT * FROM a2a_outbound_exchanges
               WHERE peer_namespace=? AND tenant=? AND idempotency_key=?""",
            (peer_namespace, request.tenant, idempotency_key),
        )
        if existing is not None:
            if existing["request_digest"] != digest:
                raise IdempotencyConflict("outbound A2A idempotency key has different exact bytes")
            return str(existing["exchange_id"]), True
        exchange_id = str(uuid4())
        now = int(time.time())
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO a2a_outbound_exchanges(
                    exchange_id,peer_namespace,tenant,idempotency_key,request_digest,
                    request_encrypted,state,created_at,updated_at
                ) VALUES(?,?,?,?,?,?,?, ?,?)""",
                (
                    exchange_id,
                    peer_namespace,
                    request.tenant,
                    idempotency_key,
                    digest,
                    self.store.cipher.encrypt_json(
                        request_payload,
                        purpose=f"a2a-outbound-request:{exchange_id}",
                    ),
                    "submitted",
                    now,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "a2a.outbound.submitted",
                    "exchange_id": exchange_id,
                    "peer_namespace": peer_namespace,
                    "request_digest": digest,
                },
            )
        return exchange_id, False

    def record(self, exchange_id: str, response: StreamResponse, mapped: MappedA2AFact) -> None:
        payload = MessageToDict(response)
        remote_task_id: str | None = None
        if response.WhichOneof("payload") == "task":
            remote_task_id = response.task.id
        elif response.WhichOneof("payload") in {"status_update", "artifact_update"}:
            variant = response.WhichOneof("payload")
            remote_task_id = getattr(response, variant).task_id
        if mapped.kind is A2AMappedKind.DIRECT_MESSAGE or mapped.terminal_remote_state:
            state = "terminal_remote_fact"
        elif mapped.kind is A2AMappedKind.QUARANTINED:
            state = "quarantined"
        else:
            state = "poll_pending" if remote_task_id else "response_recorded"
        now = int(time.time())
        with self.store.transaction() as connection:
            latest = connection.execute(
                """SELECT COALESCE(MAX(sequence),0) AS sequence
                   FROM a2a_outbound_events WHERE exchange_id=?""",
                (exchange_id,),
            ).fetchone()
            sequence = int(latest["sequence"]) + 1
            connection.execute(
                """INSERT INTO a2a_outbound_events(
                    exchange_id,sequence,response_encrypted,created_at
                ) VALUES(?,?,?,?)""",
                (
                    exchange_id,
                    sequence,
                    self.store.cipher.encrypt_json(
                        payload,
                        purpose=f"a2a-outbound-response:{exchange_id}:{sequence}",
                    ),
                    now,
                ),
            )
            connection.execute(
                """UPDATE a2a_outbound_exchanges
                   SET state=?,remote_task_id=COALESCE(?,remote_task_id),last_variant=?,
                       attempts=attempts+1,updated_at=? WHERE exchange_id=?""",
                (state, remote_task_id, mapped.source_variant, now, exchange_id),
            )

    def mark_unknown(self, exchange_id: str) -> None:
        with self.store.transaction() as connection:
            connection.execute(
                """UPDATE a2a_outbound_exchanges
                   SET state='remote_response_unknown',attempts=attempts+1,updated_at=?
                   WHERE exchange_id=?""",
                (int(time.time()), exchange_id),
            )

    def exchange(self, exchange_id: str) -> dict[str, Any]:
        row = self.store.fetch_one(
            "SELECT * FROM a2a_outbound_exchanges WHERE exchange_id=?",
            (exchange_id,),
        )
        if row is None:
            raise ConflictError("outbound A2A exchange disappeared from the durable journal")
        return dict(row)

    def replay_facts(
        self,
        exchange_id: str,
        *,
        peer_id: str,
        url_validator: Callable[[str], object],
    ) -> list[MappedA2AFact]:
        """Revalidate and replay already-durable remote facts without resending."""

        rows = self.store.fetch_all(
            """SELECT * FROM a2a_outbound_events
               WHERE exchange_id=? ORDER BY sequence""",
            (exchange_id,),
        )
        result: list[MappedA2AFact] = []
        for row in rows:
            payload = self.store.cipher.decrypt_json(
                row["response_encrypted"],
                purpose=f"a2a-outbound-response:{exchange_id}:{row['sequence']}",
            )
            if not isinstance(payload, dict):
                raise ConflictError("durable outbound A2A response is not an object")
            response = ParseDict(payload, StreamResponse())
            result.append(
                map_stream_response(
                    response,
                    peer_id=peer_id,
                    url_validator=url_validator,
                )
            )
        return result

    def pending(self) -> list[dict[str, Any]]:
        return [
            dict(row)
            for row in self.store.fetch_all(
                """SELECT * FROM a2a_outbound_exchanges
                   WHERE state IN ('submitted','poll_pending','remote_response_unknown')
                   ORDER BY created_at,exchange_id"""
            )
        ]


class NativeA2AClient:
    """Bounded official-SDK client with durable responses and polling fallback."""

    def __init__(
        self,
        *,
        sdk_client: Client,
        store: StoreBackend,
        peer_id: str,
        tenant: str,
        url_validator: Callable[[str], object],
        call_timeout_seconds: float = 2.0,
        total_timeout_seconds: float = 10.0,
        poll_interval_seconds: float = 0.05,
    ) -> None:
        if not (0 < call_timeout_seconds <= total_timeout_seconds <= 60):
            raise ValueError("A2A client timeouts must be positive, ordered, and at most 60 seconds")
        if not (0 < poll_interval_seconds <= 5):
            raise ValueError("A2A poll interval must be in (0, 5]")
        self.sdk_client = sdk_client
        self.journal = OutboundA2AJournal(store)
        self.peer_id = peer_id
        self.tenant = tenant
        self.url_validator = url_validator
        self.call_timeout_seconds = call_timeout_seconds
        self.total_timeout_seconds = total_timeout_seconds
        self.poll_interval_seconds = poll_interval_seconds

    async def _poll(
        self,
        *,
        exchange_id: str,
        remote_task_id: str,
        deadline: float,
    ) -> list[MappedA2AFact]:
        facts: list[MappedA2AFact] = []
        loop = asyncio.get_running_loop()
        while loop.time() < deadline:
            remaining = deadline - loop.time()
            if remaining <= 0:
                break
            task = await asyncio.wait_for(
                self.sdk_client.get_task(
                    GetTaskRequest(tenant=self.tenant, id=remote_task_id),
                    context=ClientCallContext(timeout=min(self.call_timeout_seconds, remaining)),
                ),
                timeout=min(self.call_timeout_seconds, remaining),
            )
            mapped = map_task(
                task,
                peer_id=self.peer_id,
                source_variant="poll.task",
                url_validator=self.url_validator,
            )
            response = StreamResponse(task=task)
            self.journal.record(exchange_id, response, mapped)
            facts.append(mapped)
            if mapped.terminal_remote_state or mapped.kind is A2AMappedKind.QUARANTINED:
                return facts
            await asyncio.sleep(min(self.poll_interval_seconds, max(0, deadline - loop.time())))
        raise TimeoutError("A2A polling fallback exceeded the bounded total timeout")

    async def send(self, request: SendMessageRequest, *, wait_for_terminal: bool = True) -> list[MappedA2AFact]:
        if request.tenant and request.tenant != self.tenant:
            raise ValidationError("outbound A2A tenant differs from the configured opaque route")
        request.tenant = self.tenant
        if request.message.role != Role.ROLE_USER or not request.message.message_id:
            raise ValidationError("outbound A2A request requires a user Message with message_id")
        validate_message_part_urls(request.message, self.url_validator)
        exchange_id, duplicate = self.journal.begin(peer_id=self.peer_id, request=request)
        if duplicate:
            row = self.journal.exchange(exchange_id)
            durable_facts = self.journal.replay_facts(
                exchange_id,
                peer_id=self.peer_id,
                url_validator=self.url_validator,
            )
            if row["state"] in {"terminal_remote_fact", "quarantined", "response_recorded"}:
                return durable_facts
            if not wait_for_terminal and durable_facts:
                return durable_facts
            if row.get("remote_task_id") and wait_for_terminal:
                deadline = asyncio.get_running_loop().time() + self.total_timeout_seconds
                return durable_facts + await self._poll(
                    exchange_id=exchange_id,
                    remote_task_id=str(row["remote_task_id"]),
                    deadline=deadline,
                )
            raise ConflictError("duplicate outbound A2A request has no safely recoverable task")

        facts: list[MappedA2AFact] = []
        remote_task_id: str | None = None
        loop = asyncio.get_running_loop()
        deadline = loop.time() + self.total_timeout_seconds
        try:
            iterator = self.sdk_client.send_message(
                request,
                context=ClientCallContext(timeout=self.call_timeout_seconds),
            )
            while True:
                remaining = deadline - loop.time()
                if remaining <= 0:
                    raise TimeoutError("A2A send exceeded the bounded total timeout")
                try:
                    response = await asyncio.wait_for(
                        anext(iterator),
                        timeout=min(self.call_timeout_seconds, remaining),
                    )
                except StopAsyncIteration:
                    break
                mapped = map_stream_response(
                    response,
                    peer_id=self.peer_id,
                    url_validator=self.url_validator,
                )
                self.journal.record(exchange_id, response, mapped)
                facts.append(mapped)
                variant = response.WhichOneof("payload")
                if variant == "task":
                    remote_task_id = response.task.id
                elif variant in {"status_update", "artifact_update"}:
                    remote_task_id = getattr(response, variant).task_id
                if mapped.kind is A2AMappedKind.DIRECT_MESSAGE:
                    return facts
                if mapped.terminal_remote_state or mapped.kind is A2AMappedKind.QUARANTINED:
                    return facts
        except Exception:
            if remote_task_id is None:
                self.journal.mark_unknown(exchange_id)
                raise
        if remote_task_id is not None and wait_for_terminal:
            facts.extend(
                await self._poll(
                    exchange_id=exchange_id,
                    remote_task_id=remote_task_id,
                    deadline=deadline,
                )
            )
        return facts

    async def resume_pending(self) -> dict[str, list[MappedA2AFact]]:
        results: dict[str, list[MappedA2AFact]] = {}
        for row in self.journal.pending():
            task_id = row.get("remote_task_id")
            if not task_id:
                continue
            deadline = asyncio.get_running_loop().time() + self.total_timeout_seconds
            results[row["exchange_id"]] = await self._poll(
                exchange_id=row["exchange_id"],
                remote_task_id=task_id,
                deadline=deadline,
            )
        return results

    async def close(self) -> None:
        await self.sdk_client.close()


def create_native_a2a_client(
    card: AgentCard,
    *,
    store: StoreBackend,
    identity: CorporateA2AClientIdentity,
    peer_id: str,
    tenant: str,
    policy: SSRFPolicy,
    resolver: AddressResolver,
    inner_transport: httpx.AsyncBaseTransport | None = None,
    call_timeout_seconds: float = 2.0,
    total_timeout_seconds: float = 10.0,
    poll_interval_seconds: float = 0.05,
) -> NativeA2AClient:
    transport = PinnedA2AHTTPTransport(policy=policy, resolver=resolver, inner=inner_transport)
    http_client = httpx.AsyncClient(
        transport=transport,
        auth=SignedA2AAuth(identity),
        timeout=httpx.Timeout(call_timeout_seconds),
        follow_redirects=False,
    )
    sdk_client = create_strict_client(
        card,
        httpx_client=http_client,
        policy=policy,
        resolver=resolver,
    )
    url_validator = lambda url: validate_outbound_url(url, policy=policy, resolver=resolver)
    return NativeA2AClient(
        sdk_client=sdk_client,
        store=store,
        peer_id=peer_id,
        tenant=tenant,
        url_validator=url_validator,
        call_timeout_seconds=call_timeout_seconds,
        total_timeout_seconds=total_timeout_seconds,
        poll_interval_seconds=poll_interval_seconds,
    )


def create_pinned_callback_sender(
    *,
    identity: CorporateA2AClientIdentity,
    policy: SSRFPolicy,
    resolver: AddressResolver,
    inner_transport: httpx.AsyncBaseTransport | None = None,
    timeout_seconds: float = 2.0,
) -> PinnedCallbackSender:
    client = httpx.AsyncClient(
        transport=PinnedA2AHTTPTransport(policy=policy, resolver=resolver, inner=inner_transport),
        auth=SignedA2AAuth(identity),
        timeout=httpx.Timeout(timeout_seconds),
        follow_redirects=False,
    )
    return PinnedCallbackSender(client, timeout_seconds=timeout_seconds)


__all__ = [
    "CorporateA2AClientIdentity",
    "NativeA2AClient",
    "OutboundA2AJournal",
    "PinnedA2AHTTPTransport",
    "PinnedCallbackSender",
    "SignedA2AAuth",
    "create_native_a2a_client",
    "create_pinned_callback_sender",
]
