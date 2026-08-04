from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import httpx
import pytest

from agentnet.bindings.remote_manager import (
    RemoteManagerDispatcher,
    RemoteManagerRequestError,
    run_manager_gateway,
)
from agentnet.errors import GateBlocked, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor


@dataclass
class RecordingClient:
    payload: Any
    status_code: int = 200
    raw_content: bytes | None = None
    domain_id: str = "owner.example"
    harness_id: str = "pi-owner-harness-0001"
    credential_id: str = "pi-owner-credential-0001"

    def __post_init__(self) -> None:
        self.calls: list[dict[str, Any]] = []

    def request(
        self,
        method: str,
        path: str,
        *,
        json_body: dict[str, Any] | None = None,
        timeout_seconds: float | None = None,
    ) -> httpx.Response:
        self.calls.append(
            {
                "json_body": json_body,
                "method": method,
                "path": path,
                "timeout_seconds": timeout_seconds,
            }
        )
        if self.raw_content is not None:
            return httpx.Response(self.status_code, content=self.raw_content)
        return httpx.Response(self.status_code, json=self.payload)


def _actor() -> VerifiedActor:
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="owner.example",
        principal_id="principal-owner-0001",
        harness_id="pi-owner-harness-0001",
        credential_id="pi-owner-credential-0001",
        credential_epoch=7,
        binding_assurance="os_bound",
    )


_PROVENANCE = {
    "allowed_sinks": {
        "schema_version": "1.0",
        "sinks": ["peer-harness-0001"],
    },
    "authority_effect": "none",
    "classification": "C1",
    "content_digest": "d" * 64,
    "domain_id": "owner.example",
    "object_id": "event-1",
    "object_type": "event",
    "policy_revision": 1,
    "provenance_digest": "e" * 64,
    "review_state": "unreviewed",
    "scan_state": "not_required",
    "schema_version": "1.0",
    "tainted": False,
    "version": 1,
}
_ACCEPTED = {
    "duplicate": True,
    "envelope_digest": "c" * 64,
    "event_id": "event-1",
    "fact": "accepted_local",
    "provenance": _PROVENANCE,
}
_OBLIGATION_ROW = {
    "closed_at": None,
    "conversation_id": "conversation:1",
    "created_at": 1,
    "deadline_at": None,
    "domain_id": "owner.example",
    "obligation_id": "obligation:1",
    "policy_revision": 1,
    "request_envelope_digest": "1" * 64,
    "request_event_id": "event-1",
    "request_payload_digest": "2" * 64,
    "requester_authority_id": "authority-owner-1",
    "requester_harness_id": "pi-owner-harness-0001",
    "response_event_id": None,
    "response_outcome": None,
    "response_payload_digest": None,
    "response_required": True,
    "response_schema_digest": None,
    "response_schema_id": None,
    "responsible_authority_id": "authority-peer-1",
    "responsible_harness_id": "peer-harness-0001",
    "revision": 1,
    "state": "pending",
    "state_reason": "created",
    "thread_id": "thread:1",
    "updated_at": 1,
}
_OBLIGATION_COUNTS = {
    "action_required": 1,
    "awaiting_human": 0,
    "awaiting_peer": 0,
    "failed": 0,
    "overdue": 0,
    "unread_information": 0,
}


_ROUTE_CASES = (
    (
        "agentnet.send",
        {
            "recipients": ["peer-harness-0001"],
            "payload": {"body": "hello"},
            "idempotency_key": "message-idempotency-0001",
            "classification": "C1",
        },
        "POST",
        "/v1/messages",
        {
            "classification": "C1",
            "idempotency_key": "message-idempotency-0001",
            "payload": {"body": "hello"},
            "recipients": ["peer-harness-0001"],
        },
        _ACCEPTED,
        _ACCEPTED,
    ),
    (
        "agentnet.inbox",
        {"after_cursor": 4, "limit": 9},
        "GET",
        "/v1/mailbox?after=4&limit=9",
        None,
        {"items": []},
        [],
    ),
    (
        "agentnet.inbox.acknowledge",
        {"event_id": "event:1", "envelope_digest": "a" * 64},
        "POST",
        "/v1/mailbox/event%3A1/acknowledge",
        {"envelope_digest": "a" * 64},
        {
            "current_fact": "recipient_committed",
            "duplicate": False,
            "envelope_digest": "a" * 64,
            "event_id": "event:1",
            "fact": "recipient_committed",
            "receipt_id": "receipt-1",
            "recipient_id": "pi-owner-harness-0001",
            "schema": "agentnet.mailbox-acknowledgement.v1",
        },
        {
            "current_fact": "recipient_committed",
            "duplicate": False,
            "envelope_digest": "a" * 64,
            "event_id": "event:1",
            "fact": "recipient_committed",
            "receipt_id": "receipt-1",
            "recipient_id": "pi-owner-harness-0001",
            "schema": "agentnet.mailbox-acknowledgement.v1",
        },
    ),
    (
        "agentnet.conversation.create",
        {
            "conversation_id": "conversation:1",
            "member_harness_ids": ["peer-harness-0001"],
            "classification": "C1",
        },
        "POST",
        "/v1/conversations",
        {
            "classification": "C1",
            "conversation_id": "conversation:1",
            "member_harness_ids": ["peer-harness-0001"],
        },
        {
            "conversation_id": "conversation:1",
            "duplicate": False,
            "policy_decision_id": "decision-1",
        },
        {
            "conversation_id": "conversation:1",
            "duplicate": False,
            "policy_decision_id": "decision-1",
        },
    ),
    (
        "agentnet.conversation.action",
        {
            "recipients": ["peer-harness-0001"],
            "conversation_id": "conversation:1",
            "thread_id": "thread:1",
            "action": {"kind": "post", "body": "hello"},
            "idempotency_key": "conversation-action-0001",
        },
        "POST",
        "/v1/conversations/conversation%3A1/actions",
        {
            "action": {
                "body": "hello",
                "kind": "post",
                "mentions": [],
                "released_artifacts": [],
            },
            "idempotency_key": "conversation-action-0001",
            "recipients": ["peer-harness-0001"],
            "thread_id": "thread:1",
        },
        _ACCEPTED
        | {
            "action_kind": "post",
            "conversation_id": "conversation:1",
            "policy_decision_id": "decision-1",
        },
        _ACCEPTED
        | {
            "action_kind": "post",
            "conversation_id": "conversation:1",
            "policy_decision_id": "decision-1",
        },
    ),
    (
        "agentnet.conversation.thread",
        {"conversation_id": "conversation:1", "thread_id": "thread:1", "limit": 11},
        "GET",
        "/v1/conversations/conversation%3A1/threads/thread%3A1?limit=11",
        None,
        {"items": []},
        [],
    ),
    (
        "agentnet.room.create",
        {},
        "POST",
        "/v1/rooms",
        {
            "classification": "C1",
            "expires_at": None,
            "persistent": True,
            "policy": None,
        },
        {
            "audit_hash": "a" * 64,
            "control_sequence": 1,
            "mls_epoch": 0,
            "mls_group_id": None,
            "room_id": "room-1",
            "state": "active",
        },
        {
            "audit_hash": "a" * 64,
            "control_sequence": 1,
            "mls_epoch": 0,
            "mls_group_id": None,
            "room_id": "room-1",
            "state": "active",
        },
    ),
    (
        "agentnet.room.member.add",
        {
            "harness_id": "peer-harness-0001",
            "role": "guest",
            "room_id": "room:/owner",
        },
        "POST",
        "/v1/rooms/room%3A%2Fowner/members",
        {"harness_id": "peer-harness-0001", "role": "guest"},
        {
            "audit_hash": "b" * 64,
            "control_sequence": 2,
            "harness_id": "peer-harness-0001",
            "room_id": "room:/owner",
        },
        {
            "audit_hash": "b" * 64,
            "control_sequence": 2,
            "harness_id": "peer-harness-0001",
            "room_id": "room:/owner",
        },
    ),
    (
        "agentnet.room.get",
        {"room_id": "room:/owner"},
        "GET",
        "/v1/rooms/room%3A%2Fowner",
        None,
        {
            "application_epoch": 1,
            "classification": "C1",
            "control_sequence": 2,
            "domain_id": "owner.example",
            "expires_at": None,
            "file_key_epoch": 1,
            "history_mode": "from_join",
            "legal_hold": 0,
            "member_count": 2,
            "members": [
                {
                    "harness_id": "pi-owner-harness-0001",
                    "joined_sequence": 1,
                    "removed_sequence": None,
                    "role": "owner_moderator",
                }
            ],
            "mls_epoch": 0,
            "mls_group_id": None,
            "mls_provider_id": None,
            "owner_domain_id": "owner.example",
            "owner_epoch": 1,
            "policy": {"history_mode": "from_join"},
            "policy_json": "{\"history_mode\":\"from_join\"}",
            "room_id": "room:/owner",
            "self_membership": {"joined_sequence": 1, "role": "owner_moderator"},
            "state": "active",
        },
        {
            "application_epoch": 1,
            "classification": "C1",
            "control_sequence": 2,
            "domain_id": "owner.example",
            "expires_at": None,
            "file_key_epoch": 1,
            "history_mode": "from_join",
            "legal_hold": 0,
            "member_count": 2,
            "members": [
                {
                    "harness_id": "pi-owner-harness-0001",
                    "joined_sequence": 1,
                    "removed_sequence": None,
                    "role": "owner_moderator",
                }
            ],
            "mls_epoch": 0,
            "mls_group_id": None,
            "mls_provider_id": None,
            "owner_domain_id": "owner.example",
            "owner_epoch": 1,
            "policy": {"history_mode": "from_join"},
            "policy_json": "{\"history_mode\":\"from_join\"}",
            "room_id": "room:/owner",
            "self_membership": {"joined_sequence": 1, "role": "owner_moderator"},
            "state": "active",
        },
    ),
    (
        "agentnet.room.send",
        {
            "expected_control_sequence": 2,
            "idempotency_key": "room-message-0001",
            "payload": {"body": "hello room"},
            "recipients": ["peer-harness-0001"],
            "room_id": "room:/owner",
        },
        "POST",
        "/v1/rooms/room%3A%2Fowner/messages",
        {
            "classification": "C1",
            "conversation_id": None,
            "expected_control_sequence": 2,
            "idempotency_key": "room-message-0001",
            "payload": {"body": "hello room"},
            "recipients": ["peer-harness-0001"],
            "released_artifacts": [],
        },
        {
            "duplicate": True,
            "envelope_digest": "c" * 64,
            "event_id": "event-room-1",
            "fact": "accepted_local",
            "provenance": _PROVENANCE,
        },
        {
            "duplicate": True,
            "envelope_digest": "c" * 64,
            "event_id": "event-room-1",
            "fact": "accepted_local",
            "provenance": _PROVENANCE,
        },
    ),
    (
        "agentnet.obligation.inbox",
        {},
        "GET",
        "/v1/response-obligations/inbox",
        None,
        _OBLIGATION_COUNTS,
        _OBLIGATION_COUNTS,
    ),
    (
        "agentnet.obligation.list",
        {"role": "responsible", "states": ["in_progress", "blocked"], "limit": 12},
        "GET",
        "/v1/response-obligations?role=responsible&limit=12&state=in_progress&state=blocked",
        None,
        {"items": [_OBLIGATION_ROW]},
        [_OBLIGATION_ROW],
    ),
    (
        "agentnet.obligation.get",
        {"obligation_id": "obligation:1"},
        "GET",
        "/v1/response-obligations/obligation%3A1",
        None,
        _OBLIGATION_ROW
        | {
            "transitions": [],
            "viewer_role": "requester",
        },
        _OBLIGATION_ROW
        | {
            "transitions": [],
            "viewer_role": "requester",
        },
    ),
    (
        "agentnet.obligation.transition",
        {
            "obligation_id": "obligation:1",
            "to_state": "in_progress",
            "reason": "recipient_update",
        },
        "POST",
        "/v1/response-obligations/obligation%3A1/transition",
        {"reason": "recipient_update", "to_state": "in_progress"},
        {"obligation_id": "obligation:1", "revision": 2, "state": "in_progress"},
        {"obligation_id": "obligation:1", "revision": 2, "state": "in_progress"},
    ),
    (
        "agentnet.obligation.cancel",
        {
            "obligation_id": "obligation:1",
            "reason_code": "requester_canceled",
            "expected_revision": 3,
        },
        "POST",
        "/v1/response-obligations/obligation%3A1/cancel",
        {"expected_revision": 3, "reason_code": "requester_canceled"},
        {"obligation_id": "obligation:1", "revision": 4, "state": "canceled"},
        {"obligation_id": "obligation:1", "revision": 4, "state": "canceled"},
    ),
    (
        "agentnet.obligation.reconcile",
        {"limit": 13},
        "POST",
        "/v1/response-obligations/reconcile",
        {"limit": 13},
        {"expired": [], "recipient_committed": []},
        {"expired": [], "recipient_committed": []},
    ),
)


@pytest.mark.parametrize(
    ("canonical_method", "arguments", "http_method", "path", "body", "payload", "expected"),
    _ROUTE_CASES,
)
def test_dispatcher_maps_every_canonical_method_to_the_existing_signed_http_contract(
    canonical_method: str,
    arguments: dict[str, Any],
    http_method: str,
    path: str,
    body: dict[str, Any] | None,
    payload: Any,
    expected: Any,
) -> None:
    client = RecordingClient(payload)
    dispatcher = RemoteManagerDispatcher(client, _actor())

    result = dispatcher.dispatch(canonical_method, arguments)

    assert result == expected
    assert client.calls == [
        {
            "json_body": body,
            "method": http_method,
            "path": path,
            "timeout_seconds": None,
        }
    ]



def test_room_gateway_rejects_unsafe_arguments_and_non_exact_success_shapes() -> None:
    unsafe_client = RecordingClient({"event_id": "must-not-be-used"})
    unsafe = RemoteManagerDispatcher(unsafe_client, _actor())
    with pytest.raises(ValidationError, match="arguments"):
        unsafe.dispatch(
            "agentnet.room.send",
            {
                "expected_control_sequence": 1,
                "idempotency_key": "room-message-unsafe-0001",
                "payload": {"body": "unsafe"},
                "recipients": ["peer-harness-0001"],
                "released_artifacts": [{"artifact_id": "artifact-1"}],
                "room_id": "room-1",
            },
        )
    assert unsafe_client.calls == []

    with pytest.raises(GateBlocked, match="artifact"):
        unsafe.dispatch(
            "agentnet.conversation.action",
            {
                "action": {
                    "body": "unsafe",
                    "kind": "post",
                    "released_artifacts": [
                        {
                            "artifact_id": "00000000-0000-4000-8000-000000000001",
                            "classification": "C1",
                            "domain_id": "owner.example",
                            "media_type": "text/plain",
                            "object_version": "f" * 64,
                            "release_intent_id": "00000000-0000-4000-8000-000000000002",
                            "released_at": "2026-08-04T00:00:00Z",
                            "schema_version": "1.0",
                            "size": 1,
                        }
                    ],
                },
                "conversation_id": "conversation:1",
                "idempotency_key": "conversation-action-unsafe-0001",
                "recipients": ["peer-harness-0001"],
                "thread_id": "thread:1",
            },
        )
    assert unsafe_client.calls == []

    malformed = RemoteManagerDispatcher(
        RecordingClient(
            {
                "audit_hash": "a" * 64,
                "control_sequence": 1,
                "mls_epoch": 0,
                "mls_group_id": None,
                "room_id": "room-1",
                "state": "active",
                "unexpected": True,
            }
        ),
        _actor(),
    )
    with pytest.raises(ValidationError, match="response schema"):
        malformed.dispatch("agentnet.room.create", {})


def test_dispatcher_propagates_remote_denial_code_and_rejects_malformed_success_json() -> None:
    denied_client = RecordingClient(
        {"code": "not_authorized", "message": "request could not be processed"},
        status_code=404,
    )
    denied = RemoteManagerDispatcher(denied_client, _actor())

    with pytest.raises(RemoteManagerRequestError) as rejected:
        denied.dispatch("agentnet.obligation.inbox", {})

    assert rejected.value.code == "not_authorized"
    assert rejected.value.status_code == 404

    malformed = RemoteManagerDispatcher(
        RecordingClient(None, status_code=200, raw_content=b"not-json"),
        _actor(),
    )
    with pytest.raises(ValidationError, match="valid JSON"):
        malformed.dispatch("agentnet.obligation.inbox", {})


def test_runner_propagates_remote_denial_over_the_local_socket(tmp_path: Path) -> None:
    state_dir = tmp_path / "s"
    state_dir.mkdir(mode=0o700)
    identity = tmp_path / "signed-identity.json"
    identity.write_text("must-not-reach-child", encoding="utf-8")
    identity.chmod(0o600)
    helper = Path(__file__).with_name("remote_manager_child.py")
    source = helper.read_text(encoding="utf-8")
    client = RecordingClient(
        {"code": "not_authorized", "message": "request could not be processed"},
        status_code=404,
    )

    status = run_manager_gateway(
        client,
        _actor(),
        (sys.executable, "-c", source, "--verify-denied", str(identity), source),
        state_dir=state_dir,
        environment={"LANG": "C.UTF-8", "PATH": os.environ.get("PATH", "/usr/bin:/bin")},
    )

    assert status == 0
    assert list(state_dir.iterdir()) == []


def test_runner_binds_exact_child_without_exposing_signing_material_and_cleans_local_state(
    tmp_path: Path,
) -> None:
    state_dir = tmp_path / "s"
    state_dir.mkdir(mode=0o700)
    identity = tmp_path / "signed-identity.json"
    identity.write_text("must-not-reach-child", encoding="utf-8")
    identity.chmod(0o600)
    helper = Path(__file__).with_name("remote_manager_child.py")
    source = helper.read_text(encoding="utf-8")
    client = RecordingClient({"items": []})

    status = run_manager_gateway(
        client,
        _actor(),
        (sys.executable, "-c", source, "--verify-bound", str(identity), source),
        state_dir=state_dir,
        environment={
            "A2HUB_TOKEN": "must-not-reach-child",
            "AGENTNET_CREDENTIAL_ID": "must-not-reach-child",
            "AGENTNET_SIGNING_PRIVATE_KEY": "must-not-reach-child",
            "ANTHROPIC_API_KEY": "must-not-reach-child",
            "LANG": "C.UTF-8",
            "LOCAL_A2A_PRIVATE_KEY": "must-not-reach-child",
            "OPENAI_API_KEY": "must-not-reach-child",
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        },
    )

    assert status == 0
    assert client.calls == [
        {
            "json_body": None,
            "method": "GET",
            "path": "/v1/mailbox?after=0&limit=1",
            "timeout_seconds": None,
        }
    ]
    assert list(state_dir.iterdir()) == []


@pytest.mark.parametrize(
    ("child_arguments", "expected_status"),
    ((("--exit", "23"), 23), (("--signal",), 143)),
)
def test_runner_propagates_child_exit_and_signal_status_and_still_cleans_state(
    tmp_path: Path,
    child_arguments: tuple[str, ...],
    expected_status: int,
) -> None:
    state_dir = tmp_path / "s"
    state_dir.mkdir(mode=0o700)
    helper = Path(__file__).with_name("remote_manager_child.py")
    command = (
        sys.executable,
        "-c",
        helper.read_text(encoding="utf-8"),
        *child_arguments,
    )

    status = run_manager_gateway(
        RecordingClient({"items": []}),
        _actor(),
        command,
        state_dir=state_dir,
        environment={"LANG": "C.UTF-8", "PATH": os.environ.get("PATH", "/usr/bin:/bin")},
    )

    assert status == expected_status
    assert list(state_dir.iterdir()) == []
