"""Durable native A2A request handling with external-taint separation.

The official SDK owns the wire protocol.  This module owns the local durable
projection and corporate policy boundary.  Unsigned public peers can create
only inert proposals. A corporate message or task is accepted into the mailbox
only after an enrolled human+harness request proof, an exact current immutable
collaboration scope, and a separate current use-counted task grant are verified.
"""

from __future__ import annotations

import asyncio
import json
import re
import time

from collections.abc import AsyncIterator, Callable, Mapping, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from typing import Any, Protocol, cast
from uuid import NAMESPACE_URL, uuid4, uuid5

from a2a.server.context import ServerCallContext
from a2a.server.request_handlers import RequestHandler
from a2a.types import (
    AgentCard,
    Artifact,
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
    StreamResponse,
    SubscribeToTaskRequest,
    Task,
    TaskArtifactUpdateEvent,
    TaskPushNotificationConfig,
    TaskState,
    TaskStatus,
    TaskStatusUpdateEvent,
)
from a2a.utils.errors import TaskNotFoundError, UnsupportedOperationError
from google.protobuf.json_format import MessageToDict, ParseDict
from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request

from agentnet.authorization.communication_scope_service import CollaborationScopeService
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    PolicyEngine,
    validate_actor_state,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    IdempotencyConflict,
    UnsupportedMediaTypeError,
    ValidationError,
)
from agentnet.gateways.a2a import corporate_peer_namespace
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.context import VerifiedContextResolver
from agentnet.identity.workload import RegisteredWorkloadCredential
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.organization.assignment import (
    AssignmentRequest,
    AssignmentService,
    TaskIngressKind,
)
from agentnet.protocol.a2a_mapping import URLValidator, validate_message_part_urls
from agentnet.protocol.models import (
    Classification,
    DeliveryFact,
    EventType,
    ReleasedArtifactBinding,
)
from agentnet.security.dpop import proof_from_headers
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.a2a_schema import require_a2a_schema

TERMINAL_TASK_STATES = frozenset(
    {
        TaskState.TASK_STATE_COMPLETED,
        TaskState.TASK_STATE_FAILED,
        TaskState.TASK_STATE_CANCELED,
        TaskState.TASK_STATE_REJECTED,
    }
)

TASK_TRANSITIONS: dict[int, frozenset[int]] = {
    TaskState.TASK_STATE_SUBMITTED: frozenset(
        {
            TaskState.TASK_STATE_WORKING,
            TaskState.TASK_STATE_FAILED,
            TaskState.TASK_STATE_CANCELED,
            TaskState.TASK_STATE_REJECTED,
        }
    ),
    TaskState.TASK_STATE_WORKING: frozenset(
        {
            TaskState.TASK_STATE_COMPLETED,
            TaskState.TASK_STATE_FAILED,
            TaskState.TASK_STATE_CANCELED,
            TaskState.TASK_STATE_INPUT_REQUIRED,
            TaskState.TASK_STATE_AUTH_REQUIRED,
        }
    ),
    TaskState.TASK_STATE_INPUT_REQUIRED: frozenset(
        {TaskState.TASK_STATE_WORKING, TaskState.TASK_STATE_FAILED, TaskState.TASK_STATE_CANCELED}
    ),
    TaskState.TASK_STATE_AUTH_REQUIRED: frozenset(
        {TaskState.TASK_STATE_WORKING, TaskState.TASK_STATE_FAILED, TaskState.TASK_STATE_CANCELED}
    ),
}

_PROOF_HEADERS = frozenset(
    {
        "x-agentnet-harness",
        "x-agentnet-credential",
        "x-agentnet-key",
        "x-agentnet-domain",
        "x-agentnet-audience",
        "x-agentnet-method",
        "x-agentnet-scheme",
        "x-agentnet-authority",
        "x-agentnet-path",
        "x-agentnet-query",
        "x-agentnet-body-digest",
        "x-agentnet-timestamp",
        "x-agentnet-nonce",
        "x-agentnet-signature",
    }
)


class A2ARuntimeLimits(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    stream_window_seconds: float = Field(default=0.25, gt=0, le=30)
    stream_poll_seconds: float = Field(default=0.02, gt=0, le=1)
    max_history_messages: int = Field(default=100, ge=1, le=1000)
    max_parts: int = Field(default=128, ge=1, le=1000)
    max_inline_bytes: int = Field(default=1_048_576, ge=1, le=16_777_216)
    max_artifacts_per_transition: int = Field(default=32, ge=1, le=256)
    max_callbacks_per_task: int = Field(default=8, ge=1, le=32)


class CallbackSender(Protocol):
    async def send(self, config: TaskPushNotificationConfig, event: StreamResponse) -> None: ...


WorkloadCredentialResolver = Callable[
    [str, str, str, str],
    RegisteredWorkloadCredential,
]
ArtifactBindingResolver = Callable[[str], ReleasedArtifactBinding]

_CANONICAL_MEDIA_TYPE = re.compile(
    r"^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$"
)


class SignedCorporateA2AAuthenticator:
    """Resolve detached request proofs; unsigned requests remain public/tainted."""

    def __init__(self, resolver: VerifiedContextResolver) -> None:
        self.resolver = resolver

    def __call__(self, request: Request, body: bytes) -> VerifiedActor | None:
        for header in (*_PROOF_HEADERS, "host"):
            if len(request.headers.getlist(header)) > 1:
                raise AuthenticationError("A2A signed request contains a duplicate security header")
        headers = {key.lower(): value for key, value in request.headers.items()}
        present = _PROOF_HEADERS.intersection(headers)
        if not present:
            return None
        proof = proof_from_headers(headers)
        raw_path = request.scope.get("raw_path", request.url.path.encode("ascii"))
        raw_query = request.scope.get("query_string", b"")
        try:
            path = bytes(raw_path).decode("ascii")
            query = bytes(raw_query).decode("ascii")
        except UnicodeDecodeError as exc:
            raise AuthenticationError("A2A signed request target is not canonical ASCII") from exc
        trusted = self.resolver.resolve(
            proof,
            expected_method=request.method,
            expected_scheme=request.url.scheme,
            expected_authority=request.headers.get("host", ""),
            expected_path=path,
            expected_query=query,
            body=body,
        )
        return trusted.actor


def corporate_input_source(actor: VerifiedActor) -> str:
    return f"a2a:{corporate_peer_namespace(actor)}"


def corporate_output_sink(recipient_id: str) -> str:
    return f"mailbox:{recipient_id}"


def _protobuf_dict(value: Any) -> dict[str, Any]:
    return cast("dict[str, Any]", MessageToDict(value, preserving_proto_field_name=False))


def _struct_dict(value: Any) -> dict[str, Any]:
    if value is None:
        return {}
    rendered = MessageToDict(value, preserving_proto_field_name=False)
    return cast("dict[str, Any]", rendered) if isinstance(rendered, dict) else {}


def _required_string(value: Mapping[str, Any], key: str, *, minimum: int = 1) -> str:
    result = value.get(key)
    if not isinstance(result, str) or len(result) < minimum or len(result) > 512:
        raise ValidationError(f"corporate A2A metadata requires exact {key}")
    return result


def _artifact_references(message: Message, *, max_parts: int, max_inline_bytes: int) -> list[dict[str, Any]]:
    """Inspect non-corporate-reference parts without ever accepting raw files.

    External URL references are retained only for inert/non-fetching proposal
    display.  This helper never turns them into corporate artifact authority.
    """

    if len(message.parts) > max_parts:
        raise ValidationError("A2A message has too many parts")
    inline_bytes = 0
    references: list[dict[str, Any]] = []
    for index, part in enumerate(message.parts):
        variant = part.WhichOneof("content")
        if variant == "raw":
            raise UnsupportedMediaTypeError(
                "A2A raw bytes are forbidden; use staged quarantine, scanning, "
                "and a released artifact binding"
            )
        if variant == "text":
            inline_bytes += len(part.text.encode("utf-8"))
        elif variant == "url":
            if not part.media_type or not _CANONICAL_MEDIA_TYPE.fullmatch(part.media_type):
                raise ValidationError("A2A URL references require a canonical lowercase media type")
            if part.filename and len(part.filename.encode("utf-8")) > 1_024:
                raise ValidationError("A2A URL reference filename exceeds the runtime boundary")
            references.append(
                {
                    "part_index": index,
                    "kind": "external_url_reference",
                    "url_digest": canonical_digest({"url": part.url}),
                    "filename": part.filename,
                    "media_type": part.media_type,
                    "metadata": _struct_dict(part.metadata),
                    "fetch_allowed": False,
                }
            )
        else:
            raise ValidationError("A2A message part requires one explicit content variant")
    if inline_bytes > max_inline_bytes:
        raise ValidationError("A2A inline text exceeds the runtime boundary")
    return references


@dataclass(frozen=True, slots=True)
class _CorporatePartInspection:
    message: Message
    references: tuple[dict[str, Any], ...]
    released_artifacts: tuple[ReleasedArtifactBinding, ...]


def _inspect_corporate_parts(
    message: Message,
    *,
    max_parts: int,
    max_inline_bytes: int,
    domain_id: str,
    classification: Classification,
    resolver: ArtifactBindingResolver | None,
) -> _CorporatePartInspection:
    """Resolve URL parts to exact released versions and remove fetchable URLs."""

    _artifact_references(
        message,
        max_parts=max_parts,
        max_inline_bytes=max_inline_bytes,
    )
    safe_message = Message()
    safe_message.CopyFrom(message)
    references: list[dict[str, Any]] = []
    bindings: list[ReleasedArtifactBinding] = []
    seen: set[str] = set()
    rank = {
        Classification.C0_PUBLIC: 0,
        Classification.C1_INTERNAL: 1,
        Classification.C2_RESTRICTED: 2,
        Classification.C3_SEALED: 3,
    }
    for index, (source, safe) in enumerate(zip(message.parts, safe_message.parts, strict=True)):
        if source.WhichOneof("content") != "url":
            continue
        metadata = _struct_dict(source.metadata)
        raw_binding = metadata.get("agentnetReleasedArtifact")
        if not isinstance(raw_binding, dict):
            raise AuthorizationError(
                "corporate A2A URL input requires an exact released artifact binding"
            )
        try:
            supplied = ReleasedArtifactBinding.model_validate(raw_binding)
        except Exception as exc:
            raise ValidationError("corporate A2A artifact binding is invalid") from exc
        if resolver is None:
            raise AuthorizationError("corporate A2A artifact resolver is unavailable")
        current = resolver(supplied.artifact_id)
        if supplied != current:
            raise AuthorizationError("corporate A2A artifact binding is stale or substituted")
        if supplied.domain_id != domain_id:
            raise AuthorizationError("corporate A2A artifact crossed the request trust domain")
        if rank[supplied.classification] > rank[classification]:
            raise AuthorizationError("corporate A2A request classification is lower than its artifact")
        if source.media_type != supplied.media_type:
            raise ValidationError("A2A part media type does not match the released artifact")
        if supplied.artifact_id in seen:
            raise ValidationError("A2A message repeats a released artifact binding")
        seen.add(supplied.artifact_id)
        bindings.append(supplied)
        references.append(
            {
                "part_index": index,
                "kind": "released_artifact",
                "binding": supplied.model_dump(mode="json"),
                "source_url_digest": canonical_digest({"url": source.url}),
                "fetch_allowed": False,
            }
        )
        safe.ClearField("url")
        safe.ClearField("filename")
        safe.text = f"[AgentNet released artifact {supplied.artifact_id}]"
    return _CorporatePartInspection(
        message=safe_message,
        references=tuple(references),
        released_artifacts=tuple(bindings),
    )


class DurableA2ARuntime(RequestHandler):
    """SDK request handler backed by the canonical store and mailbox."""

    def __init__(
        self,
        *,
        store: StoreBackend,
        mailbox: MailboxService,
        collaboration_scopes: CollaborationScopeService,
        policy: PolicyEngine,
        assignments: AssignmentService | None = None,
        agent_card: AgentCard,
        recipient_id: str,
        url_validator: URLValidator,
        callback_url_validator: URLValidator | None = None,
        callback_sender: CallbackSender | None = None,
        workload_credential_resolver: WorkloadCredentialResolver | None = None,
        artifact_binding_resolver: ArtifactBindingResolver | None = None,
        retention_days: int | None = None,
        limits: A2ARuntimeLimits = A2ARuntimeLimits(),
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
    ) -> None:
        self.store = store
        self.mailbox = mailbox
        if collaboration_scopes is not mailbox.collaboration_scopes:
            raise ValueError("A2A runtime and mailbox must share one collaboration scope service")
        if (
            assignments is not None
            and assignments.collaboration_scopes is not collaboration_scopes
        ):
            raise ValueError("A2A runtime and assignments must share one collaboration scope service")
        self.collaboration_scopes = collaboration_scopes
        self.policy = policy
        self.assignments = assignments or AssignmentService(
            store,
            collaboration_scopes=mailbox.collaboration_scopes,
            mailbox=mailbox,
            policy=policy,
        )
        if mailbox.acceptance_fact is DeliveryFact.ACCEPTED_DURABLE:
            raise ValueError("A2A accepted_durable emission is disabled until replicated RPO evidence is composed")
        self.agent_card = AgentCard()
        self.agent_card.CopyFrom(agent_card)
        self.recipient_id = recipient_id
        self.url_validator = url_validator
        self.callback_url_validator = callback_url_validator or url_validator
        self.callback_sender = callback_sender
        self.workload_credential_resolver = workload_credential_resolver
        self.artifact_binding_resolver = artifact_binding_resolver
        if retention_days is not None and retention_days < 1:
            raise ValueError("A2A retention_days must be positive")
        if mailbox.revocation_policy is not None:
            maximum = mailbox.revocation_policy.accepted_history_max_retention_days
            if retention_days is None or retention_days > maximum:
                raise ValueError("A2A runtime requires retention within the mailbox revocation boundary")
        self.retention_days = retention_days
        self.limits = limits
        self.clock = clock
        require_a2a_schema(self.store)

    @staticmethod
    def _context_actor(context: ServerCallContext) -> tuple[VerifiedActor, str]:
        actor = context.state.get("verified_actor")
        owner_namespace = context.state.get("a2a_peer_namespace")
        if not isinstance(actor, VerifiedActor) or not isinstance(owner_namespace, str):
            raise AuthenticationError("A2A runtime requires a transport-derived actor")
        return actor, owner_namespace

    def _encrypt(self, value: Mapping[str, Any], *, purpose: str) -> str:
        return self.store.cipher.encrypt_json(dict(value), purpose=purpose)

    def _decrypt(self, token: str, *, purpose: str) -> dict[str, Any]:
        value = self.store.cipher.decrypt_json(token, purpose=purpose)
        if not isinstance(value, dict):
            raise ConflictError("durable A2A record is not an object")
        return value

    def _task_from_row(self, row: Any) -> Task:
        payload = self._decrypt(row["task_encrypted"], purpose=f"a2a-task:{row['task_id']}")
        return ParseDict(payload, Task())

    def _message_from_row(self, row: Any) -> Message:
        payload = self._decrypt(
            row["response_encrypted"],
            purpose=f"a2a-message:{row['response_message_id']}",
        )
        return ParseDict(payload, Message())

    def _existing_ingress(
        self,
        *,
        tenant: str,
        owner_namespace: str,
        idempotency_key: str,
        request_digest: str,
    ) -> Task | Message | None:
        row = self.store.fetch_one(
            """SELECT * FROM a2a_ingress_keys
               WHERE tenant=? AND owner_namespace=? AND idempotency_key=?""",
            (tenant, owner_namespace, idempotency_key),
        )
        if row is None:
            return None
        if row["request_digest"] != request_digest:
            raise IdempotencyConflict("A2A idempotency key was reused with different exact content")
        if row["response_kind"] == "task":
            task_row = self.store.fetch_one("SELECT * FROM a2a_tasks WHERE task_id=?", (row["response_id"],))
            if task_row is None:
                raise ConflictError("A2A ingress key points to a missing task")
            return self._task_from_row(task_row)
        message_row = self.store.fetch_one(
            "SELECT * FROM a2a_messages WHERE response_message_id=?",
            (row["response_id"],),
        )
        if message_row is None:
            raise ConflictError("A2A ingress key points to a missing message")
        return self._message_from_row(message_row)

    def _persist_task(
        self,
        *,
        task: Task,
        tenant: str,
        owner_namespace: str,
        actor: VerifiedActor,
        source_message_id: str,
        request_digest: str,
        idempotency_key: str,
        executable: bool,
        recipient_id: str | None,
        task_grant_id: str | None,
        event_id: str | None,
        policy_revision: int,
    ) -> Task:
        now = int(self.clock().timestamp())
        task_payload = _protobuf_dict(task)
        initial_event = _protobuf_dict(StreamResponse(task=task))
        with self.store.transaction() as connection:
            existing = connection.execute(
                """SELECT * FROM a2a_ingress_keys
                   WHERE tenant=? AND owner_namespace=? AND idempotency_key=?""",
                (tenant, owner_namespace, idempotency_key),
            ).fetchone()
            if existing is not None:
                if existing["request_digest"] != request_digest:
                    raise IdempotencyConflict("A2A idempotency key raced with different exact content")
                task_row = connection.execute(
                    "SELECT * FROM a2a_tasks WHERE task_id=?",
                    (existing["response_id"],),
                ).fetchone()
                if task_row is None:
                    raise ConflictError("A2A ingress race produced an incomplete task")
                return self._task_from_row(task_row)
            connection.execute(
                """INSERT INTO a2a_tasks(
                    task_id,context_id,tenant,owner_namespace,actor_json,source_message_id,
                    request_digest,idempotency_key,executable,recipient_id,task_grant_id,
                    corporate_event_id,policy_revision,state,task_encrypted,created_at,updated_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    task.id,
                    task.context_id,
                    tenant,
                    owner_namespace,
                    canonical_json(actor.audit_view()).decode("utf-8"),
                    source_message_id,
                    request_digest,
                    idempotency_key,
                    int(executable),
                    recipient_id,
                    task_grant_id,
                    event_id,
                    policy_revision,
                    int(task.status.state),
                    self._encrypt(task_payload, purpose=f"a2a-task:{task.id}"),
                    now,
                    now,
                ),
            )
            connection.execute(
                "INSERT INTO a2a_task_events(task_id,sequence,event_encrypted,created_at) VALUES(?,?,?,?)",
                (
                    task.id,
                    1,
                    self._encrypt(initial_event, purpose=f"a2a-task-event:{task.id}:1"),
                    now,
                ),
            )
            connection.execute(
                """INSERT INTO a2a_ingress_keys(
                    tenant,owner_namespace,idempotency_key,request_digest,response_kind,response_id,created_at
                ) VALUES(?,?,?,?,?,?,?)""",
                (tenant, owner_namespace, idempotency_key, request_digest, "task", task.id, now),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "a2a.task.persist",
                    "actor": actor.audit_view(),
                    "executable": executable,
                    "request_digest": request_digest,
                    "task_id": task.id,
                },
            )
        return task

    def _persist_message(
        self,
        *,
        response: Message,
        tenant: str,
        owner_namespace: str,
        actor: VerifiedActor,
        source_message_id: str,
        request_digest: str,
        idempotency_key: str,
        event_id: str,
    ) -> Message:
        now = int(self.clock().timestamp())
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO a2a_messages(
                    response_message_id,tenant,owner_namespace,actor_json,source_message_id,
                    request_digest,idempotency_key,corporate_event_id,response_encrypted,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (
                    response.message_id,
                    tenant,
                    owner_namespace,
                    canonical_json(actor.audit_view()).decode("utf-8"),
                    source_message_id,
                    request_digest,
                    idempotency_key,
                    event_id,
                    self._encrypt(_protobuf_dict(response), purpose=f"a2a-message:{response.message_id}"),
                    now,
                ),
            )
            connection.execute(
                """INSERT INTO a2a_ingress_keys(
                    tenant,owner_namespace,idempotency_key,request_digest,response_kind,response_id,created_at
                ) VALUES(?,?,?,?,?,?,?)""",
                (tenant, owner_namespace, idempotency_key, request_digest, "message", response.message_id, now),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "a2a.message.persist",
                    "actor": actor.audit_view(),
                    "event_id": event_id,
                    "request_digest": request_digest,
                    "response_message_id": response.message_id,
                },
            )
        return response

    def _current_policy_revision(self, actor: VerifiedActor) -> int:
        row = self.store.fetch_one("SELECT policy_revision,status FROM domains WHERE domain_id=?", (actor.domain_id,))
        if row is None or row["status"] != "active":
            raise AuthorizationError("corporate A2A domain is unavailable")
        return int(row["policy_revision"])

    def _authorize_corporate(
        self,
        *,
        actor: VerifiedActor,
        request: SendMessageRequest,
        request_digest: str,
    ) -> tuple[str, str, Classification, int, str, str]:
        if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            raise AuthorizationError("corporate A2A submission requires an enrolled human+harness actor")
        metadata = _struct_dict(request.metadata)
        intent = _required_string(metadata, "agentnetIntent")
        if intent not in {"message", "task"}:
            raise ValidationError("agentnetIntent must be exactly message or task")
        idempotency_key = _required_string(metadata, "agentnetIdempotencyKey", minimum=16)
        grant_id = _required_string(metadata, "agentnetTaskGrantId")
        collaboration_scope_id = _required_string(
            metadata,
            "agentnetCollaborationScopeId",
        )
        try:
            data_class = Classification(_required_string(metadata, "agentnetDataClass"))
        except ValueError as exc:
            raise ValidationError("agentnetDataClass is not a supported corporate class") from exc
        action = "a2a.message.submit" if intent == "message" else "a2a.task.submit"
        revision = self._current_policy_revision(actor)
        input_source = corporate_input_source(actor)
        output_sink = corporate_output_sink(self.recipient_id)
        self.policy.require(
            AuthorizationRequest(
                actor=actor,
                action=action,
                resource=self.recipient_id,
                operation_class=OperationClass.BUSINESS,
                policy_revision=revision,
                context={
                    "a2a_intent": intent,
                    "idempotency_key": idempotency_key,
                    "request_digest": request_digest,
                    "collaboration_scope_id": collaboration_scope_id,
                },
                grant_use=GrantUse(
                    grant_id=grant_id,
                    action=action,
                    resource=self.recipient_id,
                    input_source=input_source,
                    output_sink=output_sink,
                    data_class=data_class,
                ),
            ),
            when=self.clock(),
        )
        recipient = self.store.fetch_one(
            "SELECT domain_id,status FROM harnesses WHERE harness_id=?",
            (self.recipient_id,),
        )
        if recipient is None or recipient["domain_id"] != actor.domain_id or recipient["status"] != "active":
            raise AuthorizationError("target server-agent is not an active harness in the actor domain")
        return intent, idempotency_key, data_class, revision, grant_id, collaboration_scope_id

    def _accept_external(
        self,
        *,
        request: SendMessageRequest,
        actor: VerifiedActor,
        owner_namespace: str,
        request_digest: str,
    ) -> Task:
        idempotency_key = f"external:{request.message.message_id}"
        existing = self._existing_ingress(
            tenant=request.tenant,
            owner_namespace=owner_namespace,
            idempotency_key=idempotency_key,
            request_digest=request_digest,
        )
        if existing is not None:
            if not isinstance(existing, Task):
                raise ConflictError("external proposal idempotency key has the wrong response kind")
            return existing
        references = _artifact_references(
            request.message,
            max_parts=self.limits.max_parts,
            max_inline_bytes=self.limits.max_inline_bytes,
        )
        task = Task(
            id=str(uuid4()),
            context_id=request.message.context_id or str(uuid4()),
            status=TaskStatus(state=TaskState.TASK_STATE_SUBMITTED, timestamp=self.clock()),
            history=[request.message],
            metadata={
                "agentnetActorKind": ActorKind.EXTERNAL_A2A.value,
                "agentnetDisposition": "tainted_non_executable_proposal",
                "agentnetAuthorityEligible": False,
                "agentnetEffectAuthorized": False,
                "agentnetExecutable": False,
                "agentnetArtifactReferenceCount": len(references),
                "agentnetRemoteReferencePolicy": "non_fetchable_inert_proposal_only",
            },
        )
        return self._persist_task(
            task=task,
            tenant=request.tenant,
            owner_namespace=owner_namespace,
            actor=actor,
            source_message_id=request.message.message_id,
            request_digest=request_digest,
            idempotency_key=idempotency_key,
            executable=False,
            recipient_id=None,
            task_grant_id=None,
            event_id=None,
            policy_revision=0,
        )

    def _accept_corporate(
        self,
        *,
        request: SendMessageRequest,
        actor: VerifiedActor,
        owner_namespace: str,
        request_digest: str,
    ) -> Task | Message:
        if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            raise AuthorizationError("corporate A2A submission requires an enrolled human+harness actor")
        metadata = _struct_dict(request.metadata)
        intent = _required_string(metadata, "agentnetIntent")
        if intent not in {"message", "task"}:
            raise ValidationError("agentnetIntent must be exactly message or task")
        idempotency_key = _required_string(metadata, "agentnetIdempotencyKey", minimum=16)
        collaboration_scope_id = _required_string(
            metadata,
            "agentnetCollaborationScopeId",
        )
        try:
            declared_classification = Classification(
                _required_string(metadata, "agentnetDataClass")
            )
        except ValueError as exc:
            raise ValidationError("agentnetDataClass is not a supported corporate class") from exc
        task_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:a2a-task:{request.tenant}:{owner_namespace}:{idempotency_key}",
            )
        )
        event_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:a2a-event:{request.tenant}:{owner_namespace}:{idempotency_key}",
            )
        )
        scope_action = "message.send" if intent == "message" else "task.propose"
        scope_resource = (
            f"conversation:{request.message.context_id or 'direct'}"
            if intent == "message"
            else f"task:{event_id}"
        )
        scope = self.collaboration_scopes.require(
            actor=actor,
            scope_id=collaboration_scope_id,
            action=scope_action,
            resource=scope_resource,
            target_harness_ids=(self.recipient_id,),
            classification=declared_classification,
            when=self.clock(),
        )
        existing = self._existing_ingress(
            tenant=request.tenant,
            owner_namespace=owner_namespace,
            idempotency_key=idempotency_key,
            request_digest=request_digest,
        )
        if existing is not None:
            return existing
        inspection = _inspect_corporate_parts(
            request.message,
            max_parts=self.limits.max_parts,
            max_inline_bytes=self.limits.max_inline_bytes,
            domain_id=actor.domain_id,
            classification=declared_classification,
            resolver=self.artifact_binding_resolver,
        )
        (
            authorized_intent,
            authorized_idempotency_key,
            data_class,
            revision,
            grant_id,
            authorized_scope_id,
        ) = self._authorize_corporate(
            actor=actor,
            request=request,
            request_digest=request_digest,
        )
        if (
            authorized_intent != intent
            or authorized_idempotency_key != idempotency_key
            or authorized_scope_id != scope.scope_id
            or revision != scope.policy_revision
        ):
            raise ConflictError("corporate A2A authorization changed during authorization")
        if data_class != declared_classification:
            raise ConflictError("corporate A2A classification changed during authorization")
        references = list(inspection.references)
        payload = {
            "a2a_message": _protobuf_dict(inspection.message),
            "artifact_references": references,
            "authorization_context": scope.authorization_context(),
            "transport": {
                "binding": "A2A-1.0",
                "owner_namespace": owner_namespace,
                "request_digest": request_digest,
            },
        }
        event = new_event(
            event_id=event_id,
            domain_id=actor.domain_id,
            actor=actor,
            event_type=EventType.MESSAGE if intent == "message" else EventType.TASK_ASSIGNMENT,
            classification=data_class,
            payload=payload,
            idempotency_key=idempotency_key,
            recipients=(self.recipient_id,),
            released_artifacts=inspection.released_artifacts,
            conversation_id=request.message.context_id or None,
            task_id=task_id if intent == "task" else None,
            retention_delete_at=(
                datetime.now(UTC) + timedelta(days=self.retention_days)
                if self.retention_days is not None
                else None
            ),
            policy_revision=revision,
        )
        if intent == "message":
            acceptance = self.mailbox.accept(event)
            response = Message(
                message_id=str(uuid4()),
                context_id=request.message.context_id or str(uuid4()),
                role=Role.ROLE_AGENT,
                parts=[Part(text="Corporate message accepted into the server-agent mailbox.")],
                metadata={
                    "agentnetDisposition": "corporate_message_accepted",
                    "agentnetAcceptanceFact": acceptance["fact"],
                    "agentnetEventId": acceptance["event_id"],
                    "agentnetExecutable": False,
                },
            )
            return self._persist_message(
                response=response,
                tenant=request.tenant,
                owner_namespace=owner_namespace,
                actor=actor,
                source_message_id=request.message.message_id,
                request_digest=request_digest,
                idempotency_key=idempotency_key,
                event_id=event.event_id,
            )
        declared_task_type = metadata.get("agentnetTaskType")
        task_type = declared_task_type if isinstance(declared_task_type, str) and declared_task_type else "a2a.task"
        declared_resources = metadata.get("agentnetResources")
        resources = (
            frozenset(declared_resources)
            if isinstance(declared_resources, list)
            and declared_resources
            and all(isinstance(value, str) and value for value in declared_resources)
            else frozenset({self.recipient_id})
        )
        declared_tools = metadata.get("agentnetTools")
        tools = (
            frozenset(declared_tools)
            if isinstance(declared_tools, list)
            and all(isinstance(value, str) and value for value in declared_tools)
            else frozenset()
        )
        declared_budget = metadata.get("agentnetBudget")
        budget = declared_budget if isinstance(declared_budget, int) and declared_budget >= 0 else 0
        declared_concurrency = metadata.get("agentnetConcurrency")
        concurrency = (
            declared_concurrency
            if isinstance(declared_concurrency, int) and declared_concurrency >= 1
            else 1
        )
        assignment = AssignmentRequest(
            actor=actor,
            collaboration_scope_id=scope.scope_id,
            recipient_harness_id=self.recipient_id,
            task_type=task_type,
            resources=resources,
            data_classes=frozenset({data_class}),
            tools=tools,
            budget=budget,
            concurrency=concurrency,
            policy_revision=revision,
            context={
                "a2a_request_digest": request_digest,
                "owner_namespace": owner_namespace,
                "collaboration_scope_id": scope.scope_id,
                "tenant": request.tenant,
            },
        )
        custody = self.assignments.submit_event(
            assignment,
            event,
            ingress=TaskIngressKind.A2A_TASK,
            continuation={
                "kind": "a2a_task",
                "apply_on_initial": False,
                "task_id": task_id,
                "authorization": {
                    "action": "a2a.task.submit",
                    "resource": self.recipient_id,
                    "grant_id": grant_id,
                    "input_source": corporate_input_source(actor),
                    "output_sink": corporate_output_sink(self.recipient_id),
                    "data_class": data_class.value,
                },
            },
            when=self.clock(),
        )
        executable = custody["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
        conflict_pending = custody["fact"] == DeliveryFact.CONFLICT_PENDING.value
        task_metadata: dict[str, Any] = {
            "agentnetDisposition": (
                "corporate_task_queued"
                if executable
                else "conflict_pending"
                if conflict_pending
                else "directional_approval_pending"
            ),
            "agentnetAcceptanceFact": custody["fact"],
            "agentnetExecutable": executable,
            "agentnetArtifactReferenceCount": len(references),
            "agentnetTaskGrantId": grant_id,
            "agentnetAssignmentDigest": custody["request_digest"],
        }
        if executable or conflict_pending:
            task_metadata["agentnetEventId"] = event.event_id
            if conflict_pending:
                task_metadata["agentnetConflictIds"] = list(custody.get("conflict_ids", []))
        else:
            task_metadata["agentnetProposalId"] = custody["proposal_id"]
        task = Task(
            id=task_id,
            context_id=request.message.context_id
            or str(uuid5(NAMESPACE_URL, f"agentnet:a2a-context:{task_id}")),
            status=TaskStatus(state=TaskState.TASK_STATE_SUBMITTED, timestamp=self.clock()),
            history=[inspection.message],
            metadata=task_metadata,
        )
        result = self._persist_task(
            task=task,
            tenant=request.tenant,
            owner_namespace=owner_namespace,
            actor=actor,
            source_message_id=request.message.message_id,
            request_digest=request_digest,
            idempotency_key=idempotency_key,
            executable=executable,
            recipient_id=self.recipient_id,
            task_grant_id=grant_id,
            event_id=event.event_id if executable else None,
            policy_revision=revision,
        )
        if executable and request.configuration.HasField("task_push_notification_config"):
            config = TaskPushNotificationConfig()
            config.CopyFrom(request.configuration.task_push_notification_config)
            config.task_id = result.id
            self._save_callback(config, owner_namespace=owner_namespace)
        return result

    async def on_message_send(self, params: SendMessageRequest, context: ServerCallContext) -> Task | Message:
        actor, owner_namespace = self._context_actor(context)
        if not params.message.message_id:
            raise ValidationError("A2A Message requires message_id")
        if len(params.message.message_id.encode("utf-8")) > 1024:
            raise ValidationError("A2A Message message_id exceeds the runtime boundary")
        if params.message.role != Role.ROLE_USER:
            raise ValidationError("inbound A2A Message must have the user role")
        validate_message_part_urls(params.message, self.url_validator)
        if params.message.task_id:
            # A task reference is an operation on that exact task, never a hint
            # to synthesize a replacement. Resolve it before idempotency or any
            # proposal persistence so inaccessible/nonexistent IDs map to the
            # native TaskNotFound condition without creating durable residue.
            row = self._task_row_for_context(params.message.task_id, context)
            if int(row["state"]) in TERMINAL_TASK_STATES:
                raise UnsupportedOperationError(
                    message="messages cannot be added to a terminal task"
                )
            raise UnsupportedOperationError(
                message="durable proposal task follow-up is not enabled"
            )
        request_digest = canonical_digest(_protobuf_dict(params))
        if actor.kind is ActorKind.EXTERNAL_A2A:
            return self._accept_external(
                request=params,
                actor=actor,
                owner_namespace=owner_namespace,
                request_digest=request_digest,
            )
        return self._accept_corporate(
            request=params,
            actor=actor,
            owner_namespace=owner_namespace,
            request_digest=request_digest,
        )

    def _task_row_for_context(self, task_id: str, context: ServerCallContext) -> Any:
        actor, owner_namespace = self._context_actor(context)
        row = self.store.fetch_one("SELECT * FROM a2a_tasks WHERE task_id=?", (task_id,))
        if row is None or row["owner_namespace"] != owner_namespace or row["tenant"] != context.tenant:
            raise TaskNotFoundError(message="task not found")
        if actor.kind is ActorKind.EXTERNAL_A2A:
            if row["executable"]:
                raise AuthorizationError("external peers cannot access executable corporate tasks")
            return row
        self._require_current_task_authority(row, actor)
        return row

    def _require_current_task_authority(self, row: Any, actor: VerifiedActor) -> None:
        now = self.clock()
        with self.store.transaction(immediate=False) as connection:
            denial, revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(row["policy_revision"]),
                when=now,
            )
            if denial is not None or revision != int(row["policy_revision"]):
                raise AuthorizationError("corporate A2A task actor is no longer current")
            grant_row = connection.execute(
                "SELECT * FROM task_grants WHERE grant_id=?",
                (row["task_grant_id"],),
            ).fetchone()
            if (
                grant_row is None
                or grant_row["domain_id"] != actor.domain_id
                or grant_row["principal_id"] != actor.positive_authority_id
                or grant_row["harness_id"] != actor.harness_id
                or grant_row["revoked_at"] is not None
                or int(grant_row["expires_at"]) <= int(now.timestamp())
            ):
                raise AuthorizationError("corporate A2A task grant is no longer current")

    async def on_get_task(self, params: GetTaskRequest, context: ServerCallContext) -> Task | None:
        return self._task_from_row(self._task_row_for_context(params.id, context))

    async def on_list_tasks(self, params: ListTasksRequest, context: ServerCallContext) -> ListTasksResponse:
        actor, owner_namespace = self._context_actor(context)
        limit = params.page_size or 50
        if limit < 1 or limit > 100:
            raise ValidationError("A2A task page size must be between 1 and 100")
        rows = self.store.fetch_all(
            """SELECT * FROM a2a_tasks
               WHERE tenant=? AND owner_namespace=? AND task_id>?
               ORDER BY task_id LIMIT ?""",
            (context.tenant, owner_namespace, params.page_token, limit + 1),
        )
        tasks: list[Task] = []
        for row in rows[:limit]:
            if actor.kind is not ActorKind.EXTERNAL_A2A:
                self._require_current_task_authority(row, actor)
            if params.context_id and row["context_id"] != params.context_id:
                continue
            if params.status != TaskState.TASK_STATE_UNSPECIFIED and row["state"] != int(params.status):
                continue
            tasks.append(self._task_from_row(row))
        next_page_token = rows[limit - 1]["task_id"] if len(rows) > limit else ""
        return ListTasksResponse(
            tasks=tasks,
            next_page_token=next_page_token,
            page_size=limit,
            total_size=len(tasks),
        )

    def _workload_credential(
        self,
        row: Any,
        role: str,
        supplied: Mapping[str, RegisteredWorkloadCredential],
    ) -> RegisteredWorkloadCredential:
        event_id = str(row["corporate_event_id"])
        recipient_id = str(row["recipient_id"])
        credential = supplied.get(role)
        if credential is None and self.workload_credential_resolver is not None:
            credential = self.workload_credential_resolver(
                role,
                event_id,
                recipient_id,
                str(row["task_grant_id"]),
            )
        expected_domain = json.loads(row["actor_json"])["domain_id"]
        actor = None if credential is None else credential.actor
        if (
            credential is None
            or actor is None
            or actor.kind is not ActorKind.WORKLOAD
            or actor.binding_assurance != "workload_mtls"
            or actor.domain_id != expected_domain
            or actor.workload_role != role
            or actor.parent_event_id != event_id
            or actor.task_grant_id is None
        ):
            raise AuthorizationError(
                f"A2A task transition requires an exact registered {role} credential"
            )
        return credential

    def _mailbox_transition(
        self,
        row: Any,
        *,
        proposed: DeliveryFact,
        credential: RegisteredWorkloadCredential,
        detail: dict[str, Any] | None = None,
    ) -> None:
        event_id = str(row["corporate_event_id"])
        recipient_id = str(row["recipient_id"])
        self.mailbox.transition(
            event_id=event_id,
            recipient_id=recipient_id,
            proposed=proposed,
            owner_actor=credential.actor,
            detail=detail,
            workload_proof=credential.proof(
                event_id=event_id,
                recipient_id=recipient_id,
                proposed_fact=proposed,
                detail=detail,
            ),
        )

    def _require_executor(self, row: Any, actor: VerifiedActor, state: int) -> None:
        if (
            actor.kind is not ActorKind.WORKLOAD
            or actor.domain_id != json.loads(row["actor_json"])["domain_id"]
            or actor.parent_event_id != row["corporate_event_id"]
            or actor.task_grant_id is None
        ):
            raise AuthorizationError("A2A task transition requires the exact event/grant-bound workload")
        expected = {
            TaskState.TASK_STATE_WORKING: "recipient_processor",
            TaskState.TASK_STATE_INPUT_REQUIRED: "recipient_processor",
            TaskState.TASK_STATE_AUTH_REQUIRED: "recipient_processor",
            TaskState.TASK_STATE_REJECTED: "recipient_processor",
            TaskState.TASK_STATE_COMPLETED: "effect_authority",
            TaskState.TASK_STATE_CANCELED: "effect_authority",
        }.get(state)
        if expected is not None and actor.workload_role != expected:
            raise AuthorizationError("A2A task transition workload role mismatch")
        if state == TaskState.TASK_STATE_FAILED and actor.workload_role not in {
            "recipient_processor",
            "effect_authority",
        }:
            raise AuthorizationError("A2A task failure requires an exact processing/effect workload")

    def _mailbox_to_processing(
        self,
        row: Any,
        owner: RegisteredWorkloadCredential,
        supplied: Mapping[str, RegisteredWorkloadCredential],
    ) -> None:
        event_id = row["corporate_event_id"]
        recipient_id = row["recipient_id"]
        current = self.store.fetch_one(
            "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
            (event_id, recipient_id),
        )
        if current is None:
            raise ConflictError("A2A task mailbox record is missing")
        fact = DeliveryFact(current["current_fact"])
        if fact is DeliveryFact.ACCEPTED_QUEUED:
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.QUEUED,
                credential=self._workload_credential(row, "mailbox_dispatcher", supplied),
            )
            fact = DeliveryFact.QUEUED
        if fact is DeliveryFact.QUEUED:
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.DISPATCH_ATTEMPTED,
                credential=self._workload_credential(row, "mailbox_dispatcher", supplied),
            )
            fact = DeliveryFact.DISPATCH_ATTEMPTED
        if fact is DeliveryFact.DISPATCH_ATTEMPTED:
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.RECIPIENT_COMMITTED,
                credential=self._workload_credential(row, "recipient_custodian", supplied),
            )
            fact = DeliveryFact.RECIPIENT_COMMITTED
        if fact is DeliveryFact.RECIPIENT_COMMITTED:
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.PROCESSING,
                credential=owner,
            )

    def _validate_artifact(self, artifact: Artifact) -> Artifact:
        if not artifact.artifact_id:
            raise ValidationError("A2A artifact requires artifact_id")
        if len(artifact.artifact_id.encode("utf-8")) > 1024:
            raise ValidationError("A2A artifact_id exceeds the runtime boundary")
        if len(artifact.parts) > self.limits.max_parts:
            raise ValidationError("A2A artifact has too many parts")
        inline_bytes = 0
        sanitized = Artifact()
        sanitized.CopyFrom(artifact)
        seen_bindings: set[str] = set()
        for part, safe_part in zip(artifact.parts, sanitized.parts, strict=True):
            variant = part.WhichOneof("content")
            if variant == "raw":
                raise ValidationError(
                    "A2A artifact raw bytes are forbidden; release them through corporate quarantine first"
                )
            if variant == "text":
                inline_bytes += len(part.text.encode("utf-8"))
            elif variant == "url":
                self.url_validator(part.url)
                if not part.media_type or not _CANONICAL_MEDIA_TYPE.fullmatch(part.media_type):
                    raise ValidationError("A2A artifact URL requires a canonical lowercase media type")
                raw_binding = _struct_dict(part.metadata).get("agentnetReleasedArtifact")
                if not isinstance(raw_binding, dict):
                    raise AuthorizationError("A2A artifact URL requires an exact released artifact binding")
                try:
                    supplied = ReleasedArtifactBinding.model_validate(raw_binding)
                except Exception as exc:
                    raise ValidationError("A2A artifact released binding is invalid") from exc
                if self.artifact_binding_resolver is None:
                    raise AuthorizationError("A2A artifact resolver is unavailable")
                current = self.artifact_binding_resolver(supplied.artifact_id)
                if supplied != current:
                    raise AuthorizationError("A2A artifact released binding is stale or substituted")
                if supplied.media_type != part.media_type:
                    raise ValidationError("A2A artifact media type differs from its released binding")
                if supplied.artifact_id in seen_bindings:
                    raise ValidationError("A2A artifact repeats one released binding")
                seen_bindings.add(supplied.artifact_id)
                safe_part.ClearField("url")
                safe_part.ClearField("filename")
                safe_part.text = f"[AgentNet released artifact {supplied.artifact_id}]"
            else:
                raise ValidationError("A2A artifact part requires one explicit content variant")
        if inline_bytes > self.limits.max_inline_bytes:
            raise ValidationError("A2A artifact inline text exceeds the runtime boundary")
        return sanitized

    def _mailbox_to_canceled(
        self,
        row: Any,
        owner: RegisteredWorkloadCredential,
        supplied: Mapping[str, RegisteredWorkloadCredential],
    ) -> None:
        current_row = self.store.fetch_one(
            "SELECT current_fact FROM recipients WHERE event_id=? AND recipient_id=?",
            (row["corporate_event_id"], row["recipient_id"]),
        )
        if current_row is None:
            raise ConflictError("A2A task mailbox record is missing")
        current = DeliveryFact(current_row["current_fact"])
        if current is DeliveryFact.CANCELED:
            return
        if current is not DeliveryFact.CANCEL_REQUESTED:
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.CANCEL_REQUESTED,
                credential=self._workload_credential(row, "control_authority", supplied),
            )
        self._mailbox_transition(
            row,
            proposed=DeliveryFact.CANCELED,
            credential=owner,
        )

    async def transition_task(
        self,
        task_id: str,
        *,
        state: int,
        owner_actor: VerifiedActor | None = None,
        workload_credentials: Mapping[str, RegisteredWorkloadCredential] | None = None,
        status_message: Message | None = None,
        artifacts: Sequence[Artifact] = (),
    ) -> Task:
        row = self.store.fetch_one("SELECT * FROM a2a_tasks WHERE task_id=?", (task_id,))
        if row is None:
            raise TaskNotFoundError(message="task not found")
        if not row["executable"]:
            raise AuthorizationError("non-executable task proposals cannot execute")
        self._require_current_task_authority(
            row,
            VerifiedActor.model_validate(json.loads(row["actor_json"])),
        )
        current = int(row["state"])
        if current == state:
            return self._task_from_row(row)
        if state not in TASK_TRANSITIONS.get(current, frozenset()):
            raise ConflictError("illegal A2A task state transition")
        supplied = workload_credentials or {}
        owner_role = {
            TaskState.TASK_STATE_WORKING: "recipient_processor",
            TaskState.TASK_STATE_INPUT_REQUIRED: "recipient_processor",
            TaskState.TASK_STATE_AUTH_REQUIRED: "recipient_processor",
            TaskState.TASK_STATE_REJECTED: "recipient_processor",
            TaskState.TASK_STATE_COMPLETED: "effect_authority",
            TaskState.TASK_STATE_CANCELED: "effect_authority",
        }.get(state)
        if state == TaskState.TASK_STATE_FAILED:
            requested_role = None if owner_actor is None else owner_actor.workload_role
            if requested_role in {"recipient_processor", "effect_authority"}:
                owner_role = requested_role
            elif "recipient_processor" in supplied:
                owner_role = "recipient_processor"
            else:
                owner_role = "effect_authority"
        if owner_role is None:
            raise AuthorizationError("A2A task transition has no workload owner role")
        owner = self._workload_credential(row, owner_role, supplied)
        if owner_actor is not None and owner_actor.audit_view() != owner.actor.audit_view():
            raise AuthorizationError("A2A task transition actor does not match its registered credential")
        owner_actor = owner.actor
        self._require_executor(row, owner_actor, state)
        if (
            current == TaskState.TASK_STATE_SUBMITTED
            and state == TaskState.TASK_STATE_FAILED
            and owner_actor.workload_role != "recipient_processor"
        ):
            raise AuthorizationError("pre-processing A2A task failure requires the exact recipient processor")
        if status_message is not None:
            if status_message.role != Role.ROLE_AGENT:
                raise ValidationError("A2A status messages must have the agent role")
            validate_message_part_urls(status_message, self.url_validator)
            _artifact_references(
                status_message,
                max_parts=self.limits.max_parts,
                max_inline_bytes=self.limits.max_inline_bytes,
            )
        if len(artifacts) > self.limits.max_artifacts_per_transition:
            raise ValidationError("too many artifacts in one A2A transition")
        sanitized_artifacts = tuple(self._validate_artifact(artifact) for artifact in artifacts)

        task = self._task_from_row(row)
        known_artifacts = {artifact.artifact_id for artifact in task.artifacts}
        for artifact in sanitized_artifacts:
            if artifact.artifact_id in known_artifacts:
                raise ConflictError("A2A artifact identifier already exists on the task")
            known_artifacts.add(artifact.artifact_id)

        if state == TaskState.TASK_STATE_WORKING:
            self._mailbox_to_processing(row, owner, supplied)
        elif state == TaskState.TASK_STATE_COMPLETED:
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.COMPLETED,
                credential=owner,
            )
        elif state in {TaskState.TASK_STATE_FAILED, TaskState.TASK_STATE_REJECTED}:
            if current == TaskState.TASK_STATE_SUBMITTED:
                self._mailbox_to_processing(row, owner, supplied)
            self._mailbox_transition(
                row,
                proposed=DeliveryFact.FAILED_TERMINAL,
                credential=owner,
            )
        elif state == TaskState.TASK_STATE_CANCELED:
            self._mailbox_to_canceled(row, owner, supplied)

        status = TaskStatus(state=state, timestamp=self.clock())
        if status_message is not None:
            status.message.CopyFrom(status_message)
        task.status.CopyFrom(status)
        for artifact in sanitized_artifacts:
            task.artifacts.append(artifact)

        emitted: list[StreamResponse] = [
            StreamResponse(
                status_update=TaskStatusUpdateEvent(
                    task_id=task.id,
                    context_id=task.context_id,
                    status=status,
                    metadata={"agentnetOwnerWorkload": owner_actor.workload_id or ""},
                )
            )
        ]
        emitted.extend(
            StreamResponse(
                artifact_update=TaskArtifactUpdateEvent(
                    task_id=task.id,
                    context_id=task.context_id,
                    artifact=artifact,
                    append=False,
                    last_chunk=True,
                )
            )
            for artifact in sanitized_artifacts
        )
        now = int(self.clock().timestamp())
        with self.store.transaction() as connection:
            latest = connection.execute(
                "SELECT COALESCE(MAX(sequence),0) AS sequence FROM a2a_task_events WHERE task_id=?",
                (task.id,),
            ).fetchone()
            sequence = int(latest["sequence"])
            connection.execute(
                "UPDATE a2a_tasks SET state=?,task_encrypted=?,updated_at=? WHERE task_id=?",
                (
                    int(state),
                    self._encrypt(_protobuf_dict(task), purpose=f"a2a-task:{task.id}"),
                    now,
                    task.id,
                ),
            )
            for event in emitted:
                sequence += 1
                connection.execute(
                    "INSERT INTO a2a_task_events(task_id,sequence,event_encrypted,created_at) VALUES(?,?,?,?)",
                    (
                        task.id,
                        sequence,
                        self._encrypt(
                            _protobuf_dict(event),
                            purpose=f"a2a-task-event:{task.id}:{sequence}",
                        ),
                        now,
                    ),
                )
            self.store.append_audit(
                connection,
                {
                    "action": "a2a.task.transition",
                    "from": current,
                    "owner": owner_actor.audit_view(),
                    "task_id": task.id,
                    "to": int(state),
                },
            )
        for event in emitted:
            await self._dispatch_callbacks(task.id, event)
        return task

    async def _cancel_task(
        self,
        row: Any,
        *,
        workload_credentials: Mapping[str, RegisteredWorkloadCredential] | None = None,
    ) -> Task:
        task = self._task_from_row(row)
        if task.status.state in TERMINAL_TASK_STATES:
            return task
        if row["executable"]:
            supplied = workload_credentials or {}
            if not supplied and self.workload_credential_resolver is None:
                task.metadata["agentnetCancellationPending"] = True
                task.metadata["agentnetCancellationDisposition"] = "effect_authority_pending"
                now = int(self.clock().timestamp())
                with self.store.transaction() as connection:
                    cursor = connection.execute(
                        """UPDATE a2a_tasks SET task_encrypted=?,updated_at=?
                             WHERE task_id=? AND state=?""",
                        (
                            self._encrypt(_protobuf_dict(task), purpose=f"a2a-task:{task.id}"),
                            now,
                            task.id,
                            int(task.status.state),
                        ),
                    )
                    if cursor.rowcount != 1:
                        raise ConflictError("A2A cancellation request raced with a task transition")
                    self.store.append_audit(
                        connection,
                        {
                            "action": "a2a.task.cancel_requested",
                            "corporate_event_id": row["corporate_event_id"],
                            "task_id": task.id,
                        },
                    )
                return task
            effect = self._workload_credential(row, "effect_authority", supplied)
            self._mailbox_to_canceled(row, effect, supplied)
            task.metadata.pop("agentnetCancellationPending", None)
            task.metadata["agentnetCancellationDisposition"] = "effect_authority_committed"
        task.status.CopyFrom(TaskStatus(state=TaskState.TASK_STATE_CANCELED, timestamp=self.clock()))
        event = StreamResponse(
            status_update=TaskStatusUpdateEvent(
                task_id=task.id,
                context_id=task.context_id,
                status=task.status,
            )
        )
        now = int(self.clock().timestamp())
        with self.store.transaction() as connection:
            latest = connection.execute(
                "SELECT COALESCE(MAX(sequence),0) AS sequence FROM a2a_task_events WHERE task_id=?",
                (task.id,),
            ).fetchone()
            sequence = int(latest["sequence"]) + 1
            connection.execute(
                "UPDATE a2a_tasks SET state=?,task_encrypted=?,updated_at=? WHERE task_id=?",
                (
                    int(TaskState.TASK_STATE_CANCELED),
                    self._encrypt(_protobuf_dict(task), purpose=f"a2a-task:{task.id}"),
                    now,
                    task.id,
                ),
            )
            connection.execute(
                "INSERT INTO a2a_task_events(task_id,sequence,event_encrypted,created_at) VALUES(?,?,?,?)",
                (
                    task.id,
                    sequence,
                    self._encrypt(_protobuf_dict(event), purpose=f"a2a-task-event:{task.id}:{sequence}"),
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "a2a.task.cancel",
                    "task_id": task.id,
                },
            )
        await self._dispatch_callbacks(task.id, event)
        return task

    async def complete_cancellation(
        self,
        task_id: str,
        *,
        workload_credentials: Mapping[str, RegisteredWorkloadCredential],
    ) -> Task:
        """Commit cancellation only after exact control/effect workload proof."""

        row = self.store.fetch_one("SELECT * FROM a2a_tasks WHERE task_id=?", (task_id,))
        if row is None:
            raise TaskNotFoundError(message="task not found")
        if not row["executable"]:
            raise AuthorizationError("non-executable task proposals have no corporate effect to cancel")
        return await self._cancel_task(row, workload_credentials=workload_credentials)

    async def on_cancel_task(self, params: CancelTaskRequest, context: ServerCallContext) -> Task | None:
        row = self._task_row_for_context(params.id, context)
        return await self._cancel_task(row)

    def _event_from_row(self, row: Any) -> StreamResponse:
        payload = self._decrypt(
            row["event_encrypted"],
            purpose=f"a2a-task-event:{row['task_id']}:{row['sequence']}",
        )
        return ParseDict(payload, StreamResponse())

    @staticmethod
    def _unwrap_stream_event(response: StreamResponse) -> Task | Message | TaskStatusUpdateEvent | TaskArtifactUpdateEvent:
        variant = response.WhichOneof("payload")
        if variant == "task":
            return response.task
        if variant == "message":
            return response.message
        if variant == "status_update":
            return response.status_update
        if variant == "artifact_update":
            return response.artifact_update
        raise ConflictError("durable A2A stream event has no payload")

    async def _bounded_task_updates(
        self,
        task_id: str,
        context: ServerCallContext,
        *,
        after_sequence: int,
    ) -> AsyncIterator[TaskStatusUpdateEvent | TaskArtifactUpdateEvent]:
        loop = asyncio.get_running_loop()
        deadline = loop.time() + self.limits.stream_window_seconds
        cursor = after_sequence
        while loop.time() < deadline:
            self._task_row_for_context(task_id, context)
            rows = self.store.fetch_all(
                """SELECT * FROM a2a_task_events
                   WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT 100""",
                (task_id, cursor),
            )
            if rows:
                for row in rows:
                    cursor = int(row["sequence"])
                    event = self._unwrap_stream_event(self._event_from_row(row))
                    if isinstance(event, TaskStatusUpdateEvent | TaskArtifactUpdateEvent):
                        yield event
                current = self.store.fetch_one("SELECT state FROM a2a_tasks WHERE task_id=?", (task_id,))
                if current is not None and int(current["state"]) in TERMINAL_TASK_STATES:
                    return
                continue
            remaining = deadline - loop.time()
            if remaining <= 0:
                return
            await asyncio.sleep(min(self.limits.stream_poll_seconds, remaining))

    async def on_message_send_stream(
        self,
        params: SendMessageRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Task | Message | TaskStatusUpdateEvent | TaskArtifactUpdateEvent]:
        result = await self.on_message_send(params, context)
        yield result
        if isinstance(result, Task) and result.status.state not in TERMINAL_TASK_STATES:
            async for update in self._bounded_task_updates(result.id, context, after_sequence=1):
                yield update

    async def on_subscribe_to_task(
        self,
        params: SubscribeToTaskRequest,
        context: ServerCallContext,
    ) -> AsyncIterator[Task | TaskStatusUpdateEvent | TaskArtifactUpdateEvent]:
        row = self._task_row_for_context(params.id, context)
        task = self._task_from_row(row)
        yield task
        latest = self.store.fetch_one(
            "SELECT COALESCE(MAX(sequence),0) AS sequence FROM a2a_task_events WHERE task_id=?",
            (params.id,),
        )
        async for update in self._bounded_task_updates(
            params.id,
            context,
            after_sequence=int(latest["sequence"]),
        ):
            yield update

    def _save_callback(self, params: TaskPushNotificationConfig, *, owner_namespace: str) -> TaskPushNotificationConfig:
        if not self.agent_card.capabilities.push_notifications:
            raise AuthorizationError("A2A push notifications are disabled by the exported Agent Card")
        row = self.store.fetch_one(
            "SELECT owner_namespace,executable FROM a2a_tasks WHERE task_id=?",
            (params.task_id,),
        )
        if row is None or row["owner_namespace"] != owner_namespace:
            raise TaskNotFoundError(message="task not found")
        if not row["executable"]:
            raise AuthorizationError("public tainted proposals cannot configure signed callbacks")
        if not params.url:
            raise ValidationError("A2A callback URL is required")
        self.callback_url_validator(params.url)
        if params.authentication.scheme or params.authentication.credentials:
            raise ValidationError("A2A callback credentials cannot be delegated through the gateway")
        config = TaskPushNotificationConfig()
        config.CopyFrom(params)
        if not config.id:
            config.id = str(uuid4())
        if len(config.id.encode("utf-8")) > 256:
            raise ValidationError("A2A callback id exceeds the runtime boundary")
        if config.token and (
            len(config.token.encode("utf-8")) > 512
            or any(ord(character) < 0x20 or ord(character) == 0x7F for character in config.token)
        ):
            raise ValidationError("A2A callback token is not a bounded HTTP header value")
        now = int(self.clock().timestamp())
        encrypted = self._encrypt(
            _protobuf_dict(config),
            purpose=f"a2a-callback:{config.task_id}:{config.id}",
        )
        url_hash = canonical_digest({"url": config.url})
        with self.store.transaction() as connection:
            existing = connection.execute(
                "SELECT * FROM a2a_callbacks WHERE task_id=? AND config_id=?",
                (config.task_id, config.id),
            ).fetchone()
            if existing is not None and existing["url_hash"] != url_hash:
                raise ConflictError("A2A callback identifier already names another URL")
            if existing is None:
                count = connection.execute(
                    "SELECT COUNT(*) AS count FROM a2a_callbacks WHERE task_id=? AND active=1",
                    (config.task_id,),
                ).fetchone()
                if int(count["count"]) >= self.limits.max_callbacks_per_task:
                    raise ValidationError("A2A callback count exceeds the per-task boundary")
            connection.execute(
                """INSERT INTO a2a_callbacks(
                    task_id,config_id,owner_namespace,url_hash,config_encrypted,active,updated_at
                ) VALUES(?,?,?,?,?,1,?)
                ON CONFLICT(task_id,config_id) DO UPDATE SET
                    config_encrypted=excluded.config_encrypted,active=1,last_error=NULL,updated_at=excluded.updated_at""",
                (config.task_id, config.id, owner_namespace, url_hash, encrypted, now),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "a2a.callback.configure",
                    "callback_id": config.id,
                    "task_id": config.task_id,
                    "url_hash": url_hash,
                },
            )
        return config

    def _callback_from_row(self, row: Any) -> TaskPushNotificationConfig:
        payload = self._decrypt(
            row["config_encrypted"],
            purpose=f"a2a-callback:{row['task_id']}:{row['config_id']}",
        )
        config = ParseDict(payload, TaskPushNotificationConfig())
        self.callback_url_validator(config.url)
        return config

    async def on_create_task_push_notification_config(
        self,
        params: TaskPushNotificationConfig,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        _actor, owner_namespace = self._context_actor(context)
        self._task_row_for_context(params.task_id, context)
        return self._save_callback(params, owner_namespace=owner_namespace)

    async def on_get_task_push_notification_config(
        self,
        params: GetTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> TaskPushNotificationConfig:
        _actor, owner_namespace = self._context_actor(context)
        self._task_row_for_context(params.task_id, context)
        row = self.store.fetch_one(
            """SELECT * FROM a2a_callbacks
               WHERE task_id=? AND config_id=? AND owner_namespace=? AND active=1""",
            (params.task_id, params.id, owner_namespace),
        )
        if row is None:
            raise TaskNotFoundError(message="callback not found")
        return self._callback_from_row(row)

    async def on_list_task_push_notification_configs(
        self,
        params: ListTaskPushNotificationConfigsRequest,
        context: ServerCallContext,
    ) -> ListTaskPushNotificationConfigsResponse:
        _actor, owner_namespace = self._context_actor(context)
        self._task_row_for_context(params.task_id, context)
        limit = params.page_size or 50
        if limit < 1 or limit > 100:
            raise ValidationError("A2A callback page size must be between 1 and 100")
        rows = self.store.fetch_all(
            """SELECT * FROM a2a_callbacks
               WHERE task_id=? AND owner_namespace=? AND active=1 AND config_id>?
               ORDER BY config_id LIMIT ?""",
            (params.task_id, owner_namespace, params.page_token, limit + 1),
        )
        configs = [self._callback_from_row(row) for row in rows[:limit]]
        token = rows[limit - 1]["config_id"] if len(rows) > limit else ""
        return ListTaskPushNotificationConfigsResponse(configs=configs, next_page_token=token)

    async def on_delete_task_push_notification_config(
        self,
        params: DeleteTaskPushNotificationConfigRequest,
        context: ServerCallContext,
    ) -> None:
        _actor, owner_namespace = self._context_actor(context)
        self._task_row_for_context(params.task_id, context)
        with self.store.transaction() as connection:
            cursor = connection.execute(
                """UPDATE a2a_callbacks SET active=0,updated_at=?
                   WHERE task_id=? AND config_id=? AND owner_namespace=? AND active=1""",
                (int(self.clock().timestamp()), params.task_id, params.id, owner_namespace),
            )
            if cursor.rowcount != 1:
                raise TaskNotFoundError(message="callback not found")

    async def _dispatch_callbacks(self, task_id: str, event: StreamResponse) -> None:
        if self.callback_sender is None:
            return
        rows = self.store.fetch_all(
            "SELECT * FROM a2a_callbacks WHERE task_id=? AND active=1 ORDER BY config_id",
            (task_id,),
        )
        for row in rows:
            error: str | None = None
            try:
                await self.callback_sender.send(self._callback_from_row(row), event)
            except Exception as exc:  # delivery failure is durable state, not task rollback
                error = type(exc).__name__
            with self.store.transaction() as connection:
                connection.execute(
                    """UPDATE a2a_callbacks
                       SET attempts=attempts+1,last_error=?,updated_at=?
                       WHERE task_id=? AND config_id=?""",
                    (error, int(self.clock().timestamp()), task_id, row["config_id"]),
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "a2a.callback.attempt",
                        "callback_id": row["config_id"],
                        "result": "delivered" if error is None else "failed",
                        "task_id": task_id,
                    },
                )

    async def on_get_extended_agent_card(
        self,
        params: GetExtendedAgentCardRequest,
        context: ServerCallContext,
    ) -> AgentCard:
        del params
        self._context_actor(context)
        card = AgentCard()
        card.CopyFrom(self.agent_card)
        return card


__all__ = [
    "A2ARuntimeLimits",
    "CallbackSender",
    "DurableA2ARuntime",
    "SignedCorporateA2AAuthenticator",
    "TASK_TRANSITIONS",
    "TERMINAL_TASK_STATES",
    "corporate_input_source",
    "corporate_output_sink",
]
