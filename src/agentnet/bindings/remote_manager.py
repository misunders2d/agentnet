"""Interactive Pi gateway from one exact local process to signed AgentNet HTTP."""

from __future__ import annotations

import ctypes
import errno
import asyncio
import os
import re
import secrets
import shutil
import signal
import stat
import subprocess
import tempfile
import time
from collections.abc import Mapping, Sequence
from contextlib import suppress
from pathlib import Path
from typing import Any, Literal
from urllib.parse import quote

import psutil

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    ValidationError as PydanticValidationError,
)

from agentnet.bindings.ipc import IPCSessionClaims, UnixIPCServer, mint_inherited_session_capability
from agentnet.bindings.tools import (
    CANONICAL_TOOL_NAMES,
    CanonicalToolRequest,
    ConversationActionArguments,
    ConversationCreateArguments,
    ConversationThreadArguments,
    EmptyArguments,
    InboxAcknowledgeArguments,
    InboxArguments,
    ObligationCancelArguments,
    ObligationGetArguments,
    ObligationListArguments,
    ObligationReconcileArguments,
    ObligationTransitionArguments,
    RoomCreateArguments,
    RoomGetArguments,
    RoomMemberAddArguments,
    RoomSendArguments,
    SendArguments,
)
from agentnet.client import AgentNetClient
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ExtensionError,
    GateBlocked,
    ValidationError,
)
from agentnet.host import host_platform
from agentnet.host_security import HostProcessIdentity, current_account_id, measure_process_identity
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import EventEnvelope
from agentnet.provenance import ProvenanceReferenceV1
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_json
from agentnet.storage.sqlite import SQLiteStore

try:
    import fcntl
except ModuleNotFoundError:  # pragma: no cover - this module rejects non-POSIX runners
    fcntl = None  # type: ignore[assignment]


_MFD_ALLOW_SEALING = 0x0002
_F_ADD_SEALS = 1033
_F_SEAL_SEAL = 0x0001
_F_SEAL_SHRINK = 0x0002
_F_SEAL_GROW = 0x0004
_F_SEAL_WRITE = 0x0008
_MAX_BINDING_BYTES = 65_536
_MAX_UPSTREAM_RESPONSE_BYTES = 1_048_576
_SANDBOX_LAUNCHER = Path("/usr/bin/bwrap")
_SAFE_CHILD_ENVIRONMENT = frozenset(
    {"COLORTERM", "LANG", "LC_ALL", "LC_CTYPE", "NO_COLOR", "TERM"}
)
_CHILD_PATH = "/usr/local/bin:/usr/bin:/bin"
_SHUTDOWN_GRACE_SECONDS = 5.0
_SYS_PIDFD_SEND_SIGNAL = 424
_SYS_PIDFD_OPEN = 434
_REMOTE_ERROR_CODE = re.compile(r"^[a-z][a-z0-9_]{0,127}$")
_FORWARDED_SIGNALS = tuple(
    candidate
    for candidate in (
        getattr(signal, "SIGHUP", None),
        getattr(signal, "SIGINT", None),
        getattr(signal, "SIGQUIT", None),
        getattr(signal, "SIGTERM", None),
    )
    if candidate is not None
)


class RemoteManagerRequestError(ExtensionError):
    """A signed upstream request was rejected with one safe remote error code."""

    code = "remote_request_rejected"

    def __init__(self, *, status_code: int, remote_code: str) -> None:
        super().__init__("signed upstream request was rejected")
        self.status_code = status_code
        self.code = remote_code


class _RemoteResult(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)


class _RoomCreateResult(_RemoteResult):
    room_id: str = Field(min_length=1, max_length=256)
    control_sequence: int = Field(ge=1)
    state: Literal["active"]
    mls_group_id: str | None = Field(max_length=1024)
    mls_epoch: int = Field(ge=0)
    audit_hash: str = Field(pattern=r"^[a-f0-9]{64}$")


class _RoomMemberAddResult(_RemoteResult):
    room_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    control_sequence: int = Field(ge=1)
    audit_hash: str = Field(pattern=r"^[a-f0-9]{64}$")


class _RoomMembershipResult(_RemoteResult):
    role: Literal["owner_moderator", "moderator", "member", "guest"]
    joined_sequence: int = Field(ge=1)


class _RoomMemberResult(_RoomMembershipResult):
    harness_id: str = Field(min_length=1, max_length=256)
    removed_sequence: int | None = Field(ge=1)


class _RoomGetResult(_RemoteResult):
    room_id: str = Field(min_length=1, max_length=256)
    domain_id: str = Field(min_length=1, max_length=253)
    owner_domain_id: str = Field(min_length=1, max_length=253)
    owner_epoch: int = Field(ge=1)
    control_sequence: int = Field(ge=1)
    state: str = Field(min_length=1, max_length=64)
    classification: Literal["C0", "C1", "C2", "C3"]
    history_mode: Literal["from_join", "no_prior_history"]
    expires_at: int | None
    legal_hold: int = Field(ge=0, le=1)
    application_epoch: int = Field(ge=1)
    mls_epoch: int = Field(ge=0)
    file_key_epoch: int = Field(ge=1)
    mls_group_id: str | None = Field(max_length=1024)
    mls_provider_id: str | None = Field(max_length=1024)
    policy_json: str = Field(min_length=2, max_length=1_000_000)
    policy: dict[str, Any]
    member_count: int = Field(ge=1)
    self_membership: _RoomMembershipResult
    members: list[_RoomMemberResult] = Field(default_factory=list)


class _RoomSendResult(_RemoteResult):
    event_id: str = Field(min_length=1, max_length=256)
    fact: str = Field(min_length=1, max_length=128)
    duplicate: bool
    provenance: ProvenanceReferenceV1
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    audit_hash: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")



class _MessageAcceptanceResult(_RemoteResult):
    event_id: str = Field(min_length=1, max_length=256)
    fact: str = Field(min_length=1, max_length=128)
    duplicate: bool
    receipt_id: str | None = Field(default=None, min_length=1, max_length=256)
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    provenance: ProvenanceReferenceV1
    audit_hash: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")


class _CustodyReference(_RemoteResult):
    schema_: Literal["agentnet.custody-payload-reference.v1"] = Field(alias="schema")
    event_id: str = Field(min_length=1, max_length=256)
    payload_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    payload_access: Literal["task_grant_required"]


class _InboxItem(_RemoteResult):
    cursor: int = Field(ge=1)
    fact: str = Field(min_length=1, max_length=128)
    event: EventEnvelope
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    payload: Any
    payload_available: bool
    provenance: ProvenanceReferenceV1
    payload_access: Literal["task_grant_required"] | None = None
    payload_withheld_reason: Literal["exact_task_grant_required"] | None = None
    custody_reference: _CustodyReference | None = None


class _ThreadItem(_RemoteResult):
    event: EventEnvelope
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    payload: Any
    payload_available: bool
    provenance: ProvenanceReferenceV1
    payload_access: Literal["task_grant_required"] | None = None
    payload_withheld_reason: Literal["exact_task_grant_required"] | None = None
    custody_reference: _CustodyReference | None = None


class _MailboxAcknowledgementResult(_RemoteResult):
    schema_: Literal["agentnet.mailbox-acknowledgement.v1"] = Field(alias="schema")
    event_id: str = Field(min_length=1, max_length=256)
    recipient_id: str = Field(min_length=1, max_length=256)
    fact: str = Field(min_length=1, max_length=128)
    current_fact: str = Field(min_length=1, max_length=128)
    duplicate: bool
    receipt_id: str = Field(min_length=1, max_length=256)
    envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    audit_hash: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")


class _ConversationCreateResult(_RemoteResult):
    conversation_id: str = Field(min_length=1, max_length=256)
    duplicate: bool
    policy_decision_id: str = Field(min_length=1, max_length=256)


class _ObligationReference(_RemoteResult):
    obligation_id: str = Field(min_length=1, max_length=256)
    state: str = Field(min_length=1, max_length=64)
    revision: int = Field(ge=1)


class _ConversationActionResult(_MessageAcceptanceResult):
    action_kind: str = Field(min_length=1, max_length=64)
    conversation_id: str = Field(min_length=1, max_length=256)
    policy_decision_id: str | None = Field(default=None, min_length=1, max_length=256)
    response_obligation: _ObligationReference | None = None


class _ObligationRow(_RemoteResult):
    obligation_id: str = Field(min_length=1, max_length=256)
    domain_id: str = Field(min_length=1, max_length=253)
    conversation_id: str = Field(min_length=1, max_length=256)
    thread_id: str = Field(min_length=1, max_length=256)
    request_event_id: str = Field(min_length=1, max_length=256)
    request_payload_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    request_envelope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    requester_authority_id: str = Field(min_length=1, max_length=256)
    requester_harness_id: str = Field(min_length=1, max_length=256)
    responsible_authority_id: str = Field(min_length=1, max_length=256)
    responsible_harness_id: str = Field(min_length=1, max_length=256)
    response_required: bool
    response_schema_id: str | None = Field(default=None, max_length=256)
    response_schema_digest: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")
    state: str = Field(min_length=1, max_length=64)
    state_reason: str = Field(min_length=1, max_length=256)
    revision: int = Field(ge=1)
    deadline_at: int | None = Field(default=None, ge=0)
    policy_revision: int = Field(ge=1)
    response_event_id: str | None = Field(default=None, min_length=1, max_length=256)
    response_payload_digest: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")
    response_outcome: str | None = Field(default=None, min_length=1, max_length=128)
    created_at: int = Field(ge=0)
    updated_at: int = Field(ge=0)
    closed_at: int | None = Field(default=None, ge=0)


class _ObligationTransition(_RemoteResult):
    revision: int = Field(ge=1)
    from_state: str | None = Field(default=None, min_length=1, max_length=64)
    to_state: str = Field(min_length=1, max_length=64)
    detail: dict[str, Any]
    response_event_id: str | None = Field(default=None, min_length=1, max_length=256)
    created_at: int = Field(ge=0)


class _ObligationGetResult(_ObligationRow):
    viewer_role: Literal["requester", "responsible"]
    transitions: list[_ObligationTransition] = Field(max_length=10_000)


class _ObligationInboxResult(_RemoteResult):
    unread_information: int = Field(ge=0)
    action_required: int = Field(ge=0)
    awaiting_peer: int = Field(ge=0)
    awaiting_human: int = Field(ge=0)
    overdue: int = Field(ge=0)
    failed: int = Field(ge=0)


class _ObligationReconcileResult(_RemoteResult):
    recipient_committed: list[str] = Field(max_length=1_000)
    expired: list[str] = Field(max_length=1_000)

class RemoteManagerDispatcher:
    """Map the canonical local surface to the existing signed HTTP endpoints."""

    def __init__(self, client: AgentNetClient, signing_context: VerifiedActor) -> None:
        if (
            signing_context.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or signing_context.positive_authority_id is None
            or signing_context.harness_id is None
            or signing_context.credential_id is None
            or signing_context.credential_epoch < 1
        ):
            raise AuthenticationError("manager gateway requires one exact human harness identity")
        if (
            client.domain_id,
            client.harness_id,
            client.credential_id,
        ) != (
            signing_context.domain_id,
            signing_context.harness_id,
            signing_context.credential_id,
        ):
            raise AuthenticationError("signed client and manager identity context differ")
        self.client = client
        self.signing_context = signing_context

    @staticmethod
    def _arguments(model: type[BaseModel], value: dict[str, Any]) -> BaseModel:
        try:
            return model.model_validate(value)
        except PydanticValidationError as exc:
            raise ValidationError("canonical manager tool arguments are invalid") from exc

    @staticmethod
    def _object(value: Any) -> dict[str, Any]:
        if not isinstance(value, dict):
            raise ValidationError("signed upstream response must be a JSON object")
        return value

    @classmethod
    def _typed_object(cls, model: type[BaseModel], value: Any) -> dict[str, Any]:
        wrapped = cls._object(value)
        try:
            model.model_validate_json(canonical_json(wrapped))
        except PydanticValidationError as exc:
            raise ValidationError("signed upstream response schema is invalid") from exc
        return wrapped
    @classmethod
    def _items(cls, value: Any) -> list[dict[str, Any]]:
        wrapped = cls._object(value)
        if (
            set(wrapped) != {"items"}
            or not isinstance(wrapped["items"], list)
            or any(not isinstance(item, dict) for item in wrapped["items"])
        ):
            raise ValidationError("signed upstream collection response schema is invalid")
        return wrapped["items"]

    @classmethod
    def _typed_items(cls, model: type[BaseModel], value: Any) -> list[dict[str, Any]]:
        items = cls._items(value)
        try:
            [
                model.model_validate_json(canonical_json(item))
                for item in items
            ]
        except PydanticValidationError as exc:
            raise ValidationError("signed upstream collection item schema is invalid") from exc
        return items
    def _request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
    ) -> Any:
        try:
            response = self.client.request(method, path, json_body=json_body)
        except ExtensionError:
            raise
        except Exception as exc:
            raise GateBlocked("remote_manager", "signed upstream request failed") from exc
        declared_length = response.headers.get("content-length")
        if declared_length is not None:
            try:
                parsed_length = int(declared_length)
            except ValueError as exc:
                raise ValidationError("signed upstream response length is invalid") from exc
            if parsed_length < 0 or parsed_length > _MAX_UPSTREAM_RESPONSE_BYTES:
                raise ValidationError("signed upstream response exceeds the bounded profile")
        if len(response.content) > _MAX_UPSTREAM_RESPONSE_BYTES:
            raise ValidationError("signed upstream response exceeds the bounded profile")
        try:
            value = response.json()
        except ValueError as exc:
            raise ValidationError("signed upstream response was not valid JSON") from exc
        if not 200 <= response.status_code < 300:
            if (
                isinstance(value, dict)
                and isinstance(value.get("code"), str)
                and _REMOTE_ERROR_CODE.fullmatch(value["code"])
            ):
                raise RemoteManagerRequestError(
                    status_code=response.status_code,
                    remote_code=value["code"],
                )
            raise ValidationError("signed upstream denial response schema is invalid")
        return value

    def dispatch(self, method: str, arguments: dict[str, Any]) -> Any:
        try:
            request = CanonicalToolRequest.model_validate(
                {"arguments": arguments, "method": method}
            )
        except PydanticValidationError as exc:
            raise ValidationError("canonical manager tool request is invalid") from exc

        if request.method == "agentnet.send":
            parsed = self._arguments(SendArguments, request.arguments)
            assert isinstance(parsed, SendArguments)
            value = self._request(
                "POST",
                "/v1/messages",
                json_body=parsed.model_dump(mode="json"),
            )
            return self._typed_object(_MessageAcceptanceResult, value)
        if request.method == "agentnet.inbox":
            parsed = self._arguments(InboxArguments, request.arguments)
            assert isinstance(parsed, InboxArguments)
            return self._typed_items(
                _InboxItem,
                self._request(
                    "GET",
                    f"/v1/mailbox?after={parsed.after_cursor}&limit={parsed.limit}",
                )
            )
        if request.method == "agentnet.inbox.acknowledge":
            parsed = self._arguments(InboxAcknowledgeArguments, request.arguments)
            assert isinstance(parsed, InboxAcknowledgeArguments)
            event_id = quote(parsed.event_id, safe="")
            return self._typed_object(
                _MailboxAcknowledgementResult,
                self._request(
                    "POST",
                    f"/v1/mailbox/{event_id}/acknowledge",
                    json_body={"envelope_digest": parsed.envelope_digest},
                )
            )
        if request.method == "agentnet.conversation.create":
            parsed = self._arguments(ConversationCreateArguments, request.arguments)
            assert isinstance(parsed, ConversationCreateArguments)
            return self._typed_object(
                _ConversationCreateResult,
                self._request(
                    "POST",
                    "/v1/conversations",
                    json_body=parsed.model_dump(mode="json"),
                )
            )
        if request.method == "agentnet.conversation.action":
            parsed = self._arguments(ConversationActionArguments, request.arguments)
            assert isinstance(parsed, ConversationActionArguments)
            if parsed.action.released_artifacts:
                raise GateBlocked(
                    "artifacts_disabled",
                    "manager communication scope cannot release artifact bindings",
                )
            conversation_id = quote(parsed.conversation_id, safe="")
            return self._typed_object(
                _ConversationActionResult,
                self._request(
                    "POST",
                    f"/v1/conversations/{conversation_id}/actions",
                    json_body={
                        "action": parsed.action.model_dump(mode="json", exclude_none=True),
                        "idempotency_key": parsed.idempotency_key,
                        "recipients": list(parsed.recipients),
                        "thread_id": parsed.thread_id,
                    },
                )
            )
        if request.method == "agentnet.conversation.thread":
            parsed = self._arguments(ConversationThreadArguments, request.arguments)
            assert isinstance(parsed, ConversationThreadArguments)
            conversation_id = quote(parsed.conversation_id, safe="")
            thread_id = quote(parsed.thread_id, safe="")
            return self._typed_items(
                _ThreadItem,
                self._request(
                    "GET",
                    f"/v1/conversations/{conversation_id}/threads/{thread_id}?limit={parsed.limit}",
                )
            )
        if request.method == "agentnet.room.create":
            parsed = self._arguments(RoomCreateArguments, request.arguments)
            assert isinstance(parsed, RoomCreateArguments)
            return self._typed_object(
                _RoomCreateResult,
                self._request(
                    "POST",
                    "/v1/rooms",
                    json_body=parsed.model_dump(mode="json"),
                ),
            )
        if request.method == "agentnet.room.member.add":
            parsed = self._arguments(RoomMemberAddArguments, request.arguments)
            assert isinstance(parsed, RoomMemberAddArguments)
            room_id = quote(parsed.room_id, safe="")
            return self._typed_object(
                _RoomMemberAddResult,
                self._request(
                    "POST",
                    f"/v1/rooms/{room_id}/members",
                    json_body={"harness_id": parsed.harness_id, "role": parsed.role},
                ),
            )
        if request.method == "agentnet.room.get":
            parsed = self._arguments(RoomGetArguments, request.arguments)
            assert isinstance(parsed, RoomGetArguments)
            room_id = quote(parsed.room_id, safe="")
            return self._typed_object(
                _RoomGetResult,
                self._request("GET", f"/v1/rooms/{room_id}"),
            )
        if request.method == "agentnet.room.send":
            parsed = self._arguments(RoomSendArguments, request.arguments)
            assert isinstance(parsed, RoomSendArguments)
            room_id = quote(parsed.room_id, safe="")
            return self._typed_object(
                _RoomSendResult,
                self._request(
                    "POST",
                    f"/v1/rooms/{room_id}/messages",
                    json_body={
                        "classification": parsed.classification.value,
                        "conversation_id": parsed.conversation_id,
                        "expected_control_sequence": parsed.expected_control_sequence,
                        "idempotency_key": parsed.idempotency_key,
                        "payload": parsed.payload,
                        "recipients": list(parsed.recipients),
                        "released_artifacts": [],
                    },
                ),
            )
        if request.method == "agentnet.obligation.inbox":
            self._arguments(EmptyArguments, request.arguments)
            return self._typed_object(
                _ObligationInboxResult,
                self._request("GET", "/v1/response-obligations/inbox"),
            )
        if request.method == "agentnet.obligation.list":
            parsed = self._arguments(ObligationListArguments, request.arguments)
            assert isinstance(parsed, ObligationListArguments)
            query = f"?role={parsed.role}&limit={parsed.limit}"
            query += "".join(f"&state={quote(state, safe='')}" for state in parsed.states)
            return self._typed_items(
                _ObligationRow,
                self._request("GET", f"/v1/response-obligations{query}"),
            )
        if request.method == "agentnet.obligation.get":
            parsed = self._arguments(ObligationGetArguments, request.arguments)
            assert isinstance(parsed, ObligationGetArguments)
            obligation_id = quote(parsed.obligation_id, safe="")
            return self._typed_object(
                _ObligationGetResult,
                self._request("GET", f"/v1/response-obligations/{obligation_id}")
            )
        if request.method == "agentnet.obligation.transition":
            parsed = self._arguments(ObligationTransitionArguments, request.arguments)
            assert isinstance(parsed, ObligationTransitionArguments)
            obligation_id = quote(parsed.obligation_id, safe="")
            body: dict[str, Any] = {
                "reason": parsed.reason,
                "to_state": parsed.to_state,
            }
            if parsed.expected_revision is not None:
                body["expected_revision"] = parsed.expected_revision
            return self._typed_object(
                _ObligationReference,
                self._request(
                    "POST",
                    f"/v1/response-obligations/{obligation_id}/transition",
                    json_body=body,
                )
            )
        if request.method == "agentnet.obligation.cancel":
            parsed = self._arguments(ObligationCancelArguments, request.arguments)
            assert isinstance(parsed, ObligationCancelArguments)
            obligation_id = quote(parsed.obligation_id, safe="")
            body = {"reason_code": parsed.reason_code}
            if parsed.expected_revision is not None:
                body["expected_revision"] = parsed.expected_revision
            return self._typed_object(
                _ObligationReference,
                self._request(
                    "POST",
                    f"/v1/response-obligations/{obligation_id}/cancel",
                    json_body=body,
                )
            )
        parsed = self._arguments(ObligationReconcileArguments, request.arguments)
        assert isinstance(parsed, ObligationReconcileArguments)
        return self._typed_object(
            _ObligationReconcileResult,
            self._request(
                "POST",
                "/v1/response-obligations/reconcile",
                json_body={"limit": parsed.limit},
            )
        )

    async def handle(
        self,
        claims: IPCSessionClaims,
        request: dict[str, Any],
    ) -> dict[str, Any]:
        method = request.get("method") if isinstance(request, dict) else None
        arguments = request.get("arguments") if isinstance(request, dict) else None
        if (
            set(request) != {"arguments", "method"}
            or not isinstance(method, str)
            or method not in claims.allowed_methods
            or not isinstance(arguments, dict)
        ):
            raise AuthorizationError("IPC method is outside the exact manager capability")
        context = self.signing_context
        if (
            claims.binding != "direct_ipc"
            or claims.process_binding != "exact"
            or claims.harness_id != context.harness_id
            or claims.credential_id != context.credential_id
            or claims.credential_epoch != context.credential_epoch
        ):
            raise AuthorizationError("IPC manager identity binding changed")
        result = await asyncio.to_thread(self.dispatch, method, arguments)
        return {"ok": True, "result": result}


def _private_state_root(configured: Path | None) -> Path:
    if configured is None:
        runtime = os.environ.get("XDG_RUNTIME_DIR")
        base = Path(runtime) if runtime else Path(tempfile.gettempdir())
        configured = base / f"agentnet-manager-{os.geteuid()}"
    path = configured.absolute()
    try:
        path.mkdir(parents=True, mode=0o700, exist_ok=True)
        metadata = path.lstat()
    except OSError as exc:
        raise GateBlocked("remote_manager", "manager replay directory is unavailable") from exc
    if (
        path.is_symlink()
        or not stat.S_ISDIR(metadata.st_mode)
        or metadata.st_uid != os.geteuid()
        or metadata.st_mode & 0o077
    ):
        raise AuthenticationError("manager replay directory must be owner-only")
    return path


def _binding_descriptors() -> tuple[int, int | None]:
    platform_name = host_platform()
    if platform_name == "macos" or (
        platform_name == "linux" and not hasattr(os, "memfd_create")
    ):
        reader, writer = os.pipe()
        os.set_inheritable(reader, True)
        os.set_inheritable(writer, False)
        return reader, writer
    if platform_name != "linux":
        raise GateBlocked("remote_manager", "interactive manager binding requires memfd or pipe")
    return (
        os.memfd_create(
            "agentnet-manager-binding",
            flags=getattr(os, "MFD_ALLOW_SEALING", _MFD_ALLOW_SEALING),
        ),
        None,
    )


def _publish_binding(descriptor: int, writer: int | None, payload: bytes) -> None:
    if not 2 <= len(payload) <= _MAX_BINDING_BYTES:
        raise GateBlocked("remote_manager", "manager binding payload size is invalid")
    if writer is not None:
        view = memoryview(payload)
        while view:
            written = os.write(writer, view)
            if written <= 0:
                raise GateBlocked("remote_manager", "manager binding pipe made no progress")
            view = view[written:]
        return
    if fcntl is None:
        raise GateBlocked("remote_manager", "manager binding descriptor cannot be sealed")
    os.pwrite(descriptor, payload, 0)
    os.ftruncate(descriptor, len(payload))
    seals = _F_SEAL_SEAL | _F_SEAL_SHRINK | _F_SEAL_GROW | _F_SEAL_WRITE
    fcntl.fcntl(descriptor, getattr(fcntl, "F_ADD_SEALS", _F_ADD_SEALS), seals)


def _child_environment(
    environment: Mapping[str, str] | None,
    *,
    binding_descriptor: int,
    home: Path,
) -> dict[str, str]:
    source = os.environ if environment is None else environment
    child = {
        name: value
        for name, value in source.items()
        if name in _SAFE_CHILD_ENVIRONMENT
        and isinstance(value, str)
        and "\x00" not in value
    }
    child.update(
        {
            "AGENTNET_LOCAL_BINDING_FD": str(binding_descriptor),
            "HOME": str(home),
            "LANG": child.get("LANG", "C.UTF-8"),
            "LC_ALL": child.get("LC_ALL", "C.UTF-8"),
            "PATH": _CHILD_PATH,
            "PI_TELEMETRY": "0",
            "TMPDIR": str(home.parent / "tmp"),
        }
    )
    return child


def _validate_command(command: Sequence[str]) -> tuple[str, ...]:
    if isinstance(command, (str, bytes)) or not command:
        raise ValidationError("interactive manager command must be a non-empty argument sequence")
    result = tuple(command)
    if any(
        not isinstance(argument, str) or not argument or "\x00" in argument
        for argument in result
    ):
        raise ValidationError("interactive manager command arguments are invalid")
    return result


def _sandbox_parent_directories(path: Path) -> tuple[Path, ...]:
    resolved = path.resolve()
    parents = [parent for parent in reversed(resolved.parents) if parent != Path("/")]
    parents.append(resolved)
    return tuple(dict.fromkeys(parents))


def _sandboxed_command(
    command: tuple[str, ...],
    *,
    session_path: Path,
    source_environment: Mapping[str, str] | None,
    child_environment: Mapping[str, str],
) -> tuple[tuple[str, ...], Path]:
    try:
        launcher = _SANDBOX_LAUNCHER.lstat()
    except OSError as exc:
        raise GateBlocked("remote_manager", "manager filesystem sandbox is unavailable") from exc
    if (
        _SANDBOX_LAUNCHER.is_symlink()
        or not stat.S_ISREG(launcher.st_mode)
        or launcher.st_uid != 0
        or launcher.st_mode & 0o022
        or not os.access(_SANDBOX_LAUNCHER, os.X_OK)
    ):
        raise GateBlocked("remote_manager", "manager filesystem sandbox is not trusted")

    supplied = Path(command[0]).expanduser()
    if supplied.is_absolute() or len(supplied.parts) > 1:
        executable = supplied.absolute().resolve(strict=True)
    else:
        source = os.environ if source_environment is None else source_environment
        resolved = shutil.which(command[0], path=source.get("PATH", _CHILD_PATH))
        if resolved is None:
            raise GateBlocked("remote_manager", "interactive Manager executable is unavailable")
        executable = Path(resolved).resolve(strict=True)
    executable_metadata = executable.lstat()
    if not stat.S_ISREG(executable_metadata.st_mode) or not os.access(executable, os.X_OK):
        raise GateBlocked("remote_manager", "interactive Manager executable is invalid")

    arguments: list[str] = [
        str(_SANDBOX_LAUNCHER),
        "--unshare-user",
        "--unshare-ipc",
        "--unshare-uts",
        "--unshare-cgroup-try",
        "--die-with-parent",
        "--dev",
        "/dev",
        "--tmpfs",
        "/tmp",
    ]
    system_roots = (Path("/usr"), Path("/bin"), Path("/lib"), Path("/lib64"))
    for system_path in system_roots:
        if system_path.exists():
            arguments.extend(("--ro-bind", str(system_path), str(system_path)))
    for safe_system_path in (
        Path("/etc/alternatives"),
        Path("/etc/hosts"),
        Path("/etc/nsswitch.conf"),
        Path("/etc/resolv.conf"),
        Path("/etc/ssl"),
    ):
        if safe_system_path.exists():
            arguments.extend(("--ro-bind", str(safe_system_path), str(safe_system_path)))
    for directory in _sandbox_parent_directories(session_path.parent):
        arguments.extend(("--dir", str(directory)))
    arguments.extend(("--bind", str(session_path), str(session_path)))
    if not any(executable.is_relative_to(root) for root in system_roots):
        for directory in _sandbox_parent_directories(executable.parent):
            arguments.extend(("--dir", str(directory)))
        arguments.extend(("--ro-bind", str(executable), str(executable)))
        runtime_library = executable.parent.parent / "lib"
        if runtime_library.is_dir():
            for directory in _sandbox_parent_directories(runtime_library.parent):
                arguments.extend(("--dir", str(directory)))
            arguments.extend(
                ("--ro-bind", str(runtime_library), str(runtime_library))
            )
    arguments.extend(("--chdir", str(session_path / "home"), "--clearenv"))
    for name, value in sorted(child_environment.items()):
        arguments.extend(("--setenv", name, value))
    arguments.extend(("--", str(executable), *command[1:]))
    return tuple(arguments), executable


def _posix_uid(account_id: str) -> int:
    if not account_id.startswith("uid:"):
        raise AuthenticationError("interactive manager child lacks a POSIX account binding")
    try:
        value = int(account_id.removeprefix("uid:"))
    except ValueError as exc:
        raise AuthenticationError("interactive manager child account is invalid") from exc
    if value < 0:
        raise AuthenticationError("interactive manager child account is invalid")
    return value


def _measure_sandboxed_child(
    supervisor_pid: int,
    *,
    expected_executable: Path,
    timeout_seconds: float = 10.0,
) -> tuple[int, HostProcessIdentity]:
    deadline = time.monotonic() + timeout_seconds
    sandbox = os.path.realpath(_SANDBOX_LAUNCHER)
    target = os.path.realpath(expected_executable)
    try:
        supervisor = psutil.Process(supervisor_pid)
        supervisor_started = supervisor.create_time()
    except psutil.Error as exc:
        raise GateBlocked(
            "remote_manager",
            "manager filesystem sandbox failed before child measurement",
        ) from exc
    while True:
        try:
            if (
                supervisor.create_time() != supervisor_started
                or os.path.realpath(supervisor.exe()) != sandbox
            ):
                raise GateBlocked(
                    "remote_manager",
                    "manager filesystem sandbox identity changed during launch",
                )
            matches: list[tuple[int, HostProcessIdentity]] = []
            for candidate in supervisor.children(recursive=True):
                arguments = {
                    os.path.realpath(item)
                    for item in candidate.cmdline()
                    if os.path.isabs(item)
                }
                executable = os.path.realpath(candidate.exe())
                if executable == target or target in arguments:
                    matches.append(
                        (
                            candidate.pid,
                            measure_process_identity(candidate.pid),
                        )
                    )
            if len(matches) == 1:
                return matches[0]
            if len(matches) > 1:
                raise GateBlocked(
                    "remote_manager",
                    "manager sandbox launched more than one matching child",
                )
        except GateBlocked:
            raise
        except (OSError, psutil.Error):
            pass
        if time.monotonic() >= deadline:
            raise GateBlocked(
                "remote_manager",
                "interactive Manager did not enter its measured sandbox executable",
            )
        time.sleep(0.01)


def _binding_payload(
    *,
    capability_root: bytes,
    signing_context: VerifiedActor,
    socket_path: Path,
    pid: int,
    identity: HostProcessIdentity,
    session_id: str,
    ttl_seconds: int,
) -> tuple[bytes, int]:
    current = measure_process_identity(pid)
    if current != identity:
        raise AuthenticationError("interactive Manager executable changed before capability issuance")
    if identity.account_id != current_account_id():
        raise AuthenticationError("interactive manager child owner differs from the gateway")
    now = int(time.time())
    claims = IPCSessionClaims(
        schema="agentnet.ipc.session.v1",
        capability_id=secrets.token_urlsafe(32),
        harness_id=str(signing_context.harness_id),
        credential_id=str(signing_context.credential_id),
        credential_epoch=signing_context.credential_epoch,
        binding="direct_ipc",
        process_binding="exact",
        child_process_measurement=None,
        allowed_methods=CANONICAL_TOOL_NAMES,
        platform=identity.platform,
        account_id=identity.account_id,
        uid=_posix_uid(identity.account_id),
        pid=pid,
        process_start_time=identity.start_time,
        process_measurement=identity.executable_measurement,
        session_id=session_id,
        issued_at=now,
        expires_at=now + ttl_seconds,
    )
    capability = mint_inherited_session_capability(capability_root, claims)
    return (
        canonical_json(
            {
                "capability": capability,
                "credential_epoch": claims.credential_epoch,
                "credential_id": claims.credential_id,
                "expires_at": claims.expires_at,
                "harness_id": claims.harness_id,
                "schema": "agentnet.ipc.issued-child.v1",
                "session_id": claims.session_id,
                "socket_path": str(socket_path),
            }
        ),
        claims.expires_at,
    )


def _open_process_handle(pid: int) -> int:
    native = getattr(os, "pidfd_open", None)
    if native is not None:
        return int(native(pid, 0))
    libc = ctypes.CDLL(None, use_errno=True)
    descriptor = int(libc.syscall(_SYS_PIDFD_OPEN, pid, 0))
    if descriptor < 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))
    return descriptor


def _signal_process_handle(descriptor: int, received: signal.Signals) -> None:
    native = getattr(signal, "pidfd_send_signal", None)
    if native is not None:
        native(descriptor, received)
        return
    libc = ctypes.CDLL(None, use_errno=True)
    result = int(
        libc.syscall(
            _SYS_PIDFD_SEND_SIGNAL,
            descriptor,
            int(received),
            ctypes.c_void_p(),
            0,
        )
    )
    if result < 0:
        error = ctypes.get_errno()
        if error == errno.ESRCH:
            raise ProcessLookupError(error, os.strerror(error))
        raise OSError(error, os.strerror(error))


async def _wait_for_child(
    process: subprocess.Popen[Any],
    *,
    process_handle: int,
    expires_at: int,
) -> int:
    loop = asyncio.get_running_loop()
    previous: dict[signal.Signals, Any] = {}
    termination_requested = asyncio.Event()

    def forward(received: signal.Signals) -> None:
        termination_requested.set()
        if process.poll() is None:
            with suppress(ProcessLookupError):
                _signal_process_handle(process_handle, received)

    for received in _FORWARDED_SIGNALS:
        try:
            previous[received] = signal.getsignal(received)
            loop.add_signal_handler(received, forward, received)
        except (NotImplementedError, RuntimeError, ValueError):
            previous.pop(received, None)
    wait_task = asyncio.create_task(asyncio.to_thread(process.wait))
    signal_task = asyncio.create_task(termination_requested.wait())
    try:
        remaining = max(0.0, expires_at - time.time())
        done, _pending = await asyncio.wait(
            {wait_task, signal_task},
            timeout=remaining,
            return_when=asyncio.FIRST_COMPLETED,
        )
        if wait_task in done:
            return wait_task.result()

        if signal_task not in done and process.poll() is None:
            with suppress(ProcessLookupError):
                _signal_process_handle(process_handle, signal.SIGTERM)
        try:
            return await asyncio.wait_for(
                asyncio.shield(wait_task),
                timeout=_SHUTDOWN_GRACE_SECONDS,
            )
        except TimeoutError:
            if process.poll() is None:
                with suppress(ProcessLookupError):
                    _signal_process_handle(process_handle, signal.SIGTERM)
            try:
                return await asyncio.wait_for(
                    asyncio.shield(wait_task),
                    timeout=_SHUTDOWN_GRACE_SECONDS,
                )
            except TimeoutError:
                if process.poll() is None:
                    with suppress(ProcessLookupError):
                        _signal_process_handle(process_handle, signal.SIGKILL)
                try:
                    return await asyncio.wait_for(
                        asyncio.shield(wait_task),
                        timeout=_SHUTDOWN_GRACE_SECONDS,
                    )
                except TimeoutError:
                    if process.poll() is None:
                        with suppress(ProcessLookupError):
                            process.kill()
                    return await wait_task
    finally:
        signal_task.cancel()
        with suppress(asyncio.CancelledError):
            await signal_task
        for received, handler in previous.items():
            loop.remove_signal_handler(received)
            signal.signal(received, handler)


def _cleanup_session(path: Path, identity: tuple[int, int]) -> None:
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        return
    if path.is_symlink() or (metadata.st_dev, metadata.st_ino) != identity:
        raise AuthenticationError("manager replay directory identity changed before cleanup")
    shutil.rmtree(path)


async def _run_manager_gateway(
    client: AgentNetClient,
    signing_context: VerifiedActor,
    command: tuple[str, ...],
    *,
    state_dir: Path | None,
    environment: Mapping[str, str] | None,
    capability_ttl_seconds: int,
) -> int:
    if host_platform() != "linux":
        raise GateBlocked(
            "remote_manager",
            "interactive Manager requires the supported Linux filesystem sandbox",
        )
    dispatcher = RemoteManagerDispatcher(client, signing_context)
    root = _private_state_root(state_dir)
    session_path = Path(tempfile.mkdtemp(prefix="s-", dir=root))
    os.chmod(session_path, 0o700)
    session_metadata = session_path.stat()
    session_identity = (session_metadata.st_dev, session_metadata.st_ino)
    home_path = session_path / "home"
    temporary_path = session_path / "tmp"
    home_path.mkdir(mode=0o700)
    temporary_path.mkdir(mode=0o700)
    socket_path = session_path / "m.sock"
    limit = 103 if host_platform() == "macos" else 107
    if len(os.fsencode(socket_path)) > limit:
        _cleanup_session(session_path, session_identity)
        raise GateBlocked("remote_manager", "manager Unix socket path exceeds platform limit")

    replay_store: SQLiteStore | None = None
    server: UnixIPCServer | None = None
    process: subprocess.Popen[Any] | None = None
    process_handle: int | None = None
    descriptor: int | None = None
    writer: int | None = None
    try:
        replay_store = SQLiteStore(
            session_path / "replay.sqlite3",
            LocalEnvelopeCipher(secrets.token_bytes(32)),
        )
        capability_root = secrets.token_bytes(32)
        server = UnixIPCServer(
            socket_path,
            capability_root=capability_root,
            replay_store=replay_store,
            handler=dispatcher.handle,
        )
        await server.start()
        descriptor, writer = _binding_descriptors()
        child_environment = _child_environment(
            environment,
            binding_descriptor=descriptor,
            home=home_path,
        )
        launch_command, expected_executable = _sandboxed_command(
            command,
            session_path=session_path,
            source_environment=environment,
            child_environment=child_environment,
        )
        process = subprocess.Popen(
            launch_command,
            env={},
            close_fds=True,
            pass_fds=(descriptor,),
        )
        child_pid, identity = await asyncio.to_thread(
            _measure_sandboxed_child,
            process.pid,
            expected_executable=expected_executable,
        )
        process_handle = _open_process_handle(child_pid)
        payload, expires_at = _binding_payload(
            capability_root=capability_root,
            signing_context=signing_context,
            socket_path=socket_path,
            pid=child_pid,
            identity=identity,
            session_id=secrets.token_urlsafe(24),
            ttl_seconds=capability_ttl_seconds,
        )
        _publish_binding(descriptor, writer, payload)
        if writer is not None:
            os.close(writer)
            writer = None
        os.close(descriptor)
        descriptor = None
        return_code = await _wait_for_child(
            process,
            process_handle=process_handle,
            expires_at=expires_at,
        )
        return return_code if return_code >= 0 else 128 - return_code
    finally:
        try:
            if writer is not None:
                with suppress(OSError):
                    os.close(writer)
            if descriptor is not None:
                with suppress(OSError):
                    os.close(descriptor)
            if process is not None and process.poll() is None:
                if process_handle is not None:
                    with suppress(ProcessLookupError):
                        _signal_process_handle(process_handle, signal.SIGTERM)
                else:
                    with suppress(ProcessLookupError):
                        process.terminate()
                try:
                    await asyncio.to_thread(process.wait, 5)
                except subprocess.TimeoutExpired:
                    with suppress(ProcessLookupError):
                        process.kill()
                    await asyncio.to_thread(process.wait)
            if process_handle is not None:
                with suppress(OSError):
                    os.close(process_handle)
        finally:
            try:
                if server is not None:
                    await server.close()
            finally:
                try:
                    if replay_store is not None:
                        replay_store.close()
                finally:
                    _cleanup_session(session_path, session_identity)


def run_manager_gateway(
    client: AgentNetClient,
    signing_context: VerifiedActor,
    command: Sequence[str],
    *,
    state_dir: Path | None = None,
    environment: Mapping[str, str] | None = None,
    capability_ttl_seconds: int = 3600,
) -> int:
    """Run an interactive child with only one exact process-bound binding FD.

    Standard input, output, error, terminal process group, and the child's normal
    environment are preserved. AgentNet signing material remains in this parent;
    the child receives only ``AGENTNET_LOCAL_BINDING_FD`` in that namespace.
    A signal exit is returned using the conventional ``128 + signal`` status.
    The caller retains ownership of ``client`` and must close it.
    """

    validated_command = _validate_command(command)
    if not 1 <= capability_ttl_seconds <= 3600:
        raise ValidationError("manager process capability lifetime is outside the bounded profile")
    if host_platform() not in {"linux", "macos"}:
        raise GateBlocked("remote_manager", "interactive manager gateway requires Unix IPC")
    try:
        asyncio.get_running_loop()
    except RuntimeError:
        pass
    else:
        raise ValidationError("synchronous manager runner cannot execute inside an event loop")
    return asyncio.run(
        _run_manager_gateway(
            client,
            signing_context,
            validated_command,
            state_dir=state_dir,
            environment=environment,
            capability_ttl_seconds=capability_ttl_seconds,
        )
    )


__all__ = [
    "RemoteManagerDispatcher",
    "RemoteManagerRequestError",
    "run_manager_gateway",
]
