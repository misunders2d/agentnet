from __future__ import annotations

import pytest

from a2a.types import (
    Artifact,
    Message,
    Part,
    Role,
    SendMessageResponse,
    StreamResponse,
    Task,
    TaskArtifactUpdateEvent,
    TaskState,
    TaskStatus,
    TaskStatusUpdateEvent,
)

from agentnet.protocol.a2a_mapping import (
    A2AMappedKind,
    A2ARecoveryMode,
    external_peer_namespace,
    map_send_message_response,
    map_stream_response,
    namespace_external_id,
)


PEER = "verified-network-peer:key-thumbprint-1"


def message(*, role: int = Role.ROLE_AGENT, url: str | None = None) -> Message:
    part = Part(url=url) if url else Part(text="hello")
    return Message(
        message_id="foreign-message-1",
        context_id="foreign-context-1",
        task_id="foreign-task-1",
        role=role,
        parts=[part],
        reference_task_ids=["foreign-reference-1"],
    )


def task(*, state: int = TaskState.TASK_STATE_WORKING) -> Task:
    return Task(
        id="server-task-1",
        context_id="foreign-context-1",
        status=TaskStatus(state=state),
        artifacts=[Artifact(artifact_id="artifact-1", parts=[Part(text="output")])],
    )


def test_send_message_task_and_direct_message_are_distinct() -> None:
    task_fact = map_send_message_response(SendMessageResponse(task=task()), peer_id=PEER)
    message_fact = map_send_message_response(SendMessageResponse(message=message()), peer_id=PEER)

    assert task_fact.kind is A2AMappedKind.TASK
    assert task_fact.source_variant == "send_message.task"
    assert task_fact.recovery is A2ARecoveryMode.GET_TASK
    assert task_fact.task_id is not None

    assert message_fact.kind is A2AMappedKind.DIRECT_MESSAGE
    assert message_fact.source_variant == "send_message.message"
    assert message_fact.recovery is A2ARecoveryMode.NOT_GET_TASK_RECOVERABLE
    assert message_fact.task_id is not None
    assert message_fact.message_id is not None

    for fact in (task_fact, message_fact):
        assert fact.actor_kind == "external_human_unverified"
        assert fact.authority_eligible is False
        assert fact.credential_disclosure_allowed is False


@pytest.mark.parametrize(
    ("response", "kind", "variant"),
    [
        (StreamResponse(task=task()), A2AMappedKind.TASK, "stream.task"),
        (StreamResponse(message=message()), A2AMappedKind.DIRECT_MESSAGE, "stream.message"),
        (
            StreamResponse(
                status_update=TaskStatusUpdateEvent(
                    task_id="server-task-1",
                    context_id="foreign-context-1",
                    status=TaskStatus(state=TaskState.TASK_STATE_WORKING),
                )
            ),
            A2AMappedKind.TASK_STATUS_UPDATE,
            "stream.status_update",
        ),
        (
            StreamResponse(
                artifact_update=TaskArtifactUpdateEvent(
                    task_id="server-task-1",
                    context_id="foreign-context-1",
                    artifact=Artifact(artifact_id="artifact-1", parts=[Part(text="chunk")]),
                    last_chunk=True,
                )
            ),
            A2AMappedKind.TASK_ARTIFACT_UPDATE,
            "stream.artifact_update",
        ),
    ],
)
def test_every_stream_variant_maps_separately(
    response: StreamResponse,
    kind: A2AMappedKind,
    variant: str,
) -> None:
    fact = map_stream_response(response, peer_id=PEER)
    assert fact.kind is kind
    assert fact.source_variant == variant
    assert fact.authority_eligible is False
    if kind is A2AMappedKind.DIRECT_MESSAGE:
        assert fact.recovery is A2ARecoveryMode.NOT_GET_TASK_RECOVERABLE
    else:
        assert fact.recovery is A2ARecoveryMode.GET_TASK


def test_unspecified_send_or_stream_payload_is_quarantined() -> None:
    send_fact = map_send_message_response(SendMessageResponse(), peer_id=PEER)
    stream_fact = map_stream_response(StreamResponse(), peer_id=PEER)
    assert send_fact.kind is A2AMappedKind.QUARANTINED
    assert stream_fact.kind is A2AMappedKind.QUARANTINED
    assert "unspecified" in (send_fact.quarantined_reason or "").lower()
    assert "unspecified" in (stream_fact.quarantined_reason or "").lower()


def test_unspecified_role_and_state_are_quarantined() -> None:
    role_fact = map_send_message_response(
        SendMessageResponse(message=message(role=Role.ROLE_UNSPECIFIED)),
        peer_id=PEER,
    )
    task_fact = map_send_message_response(
        SendMessageResponse(task=task(state=TaskState.TASK_STATE_UNSPECIFIED)),
        peer_id=PEER,
    )
    status_fact = map_stream_response(
        StreamResponse(
            status_update=TaskStatusUpdateEvent(
                task_id="server-task-1",
                status=TaskStatus(state=TaskState.TASK_STATE_UNSPECIFIED),
            )
        ),
        peer_id=PEER,
    )
    assert role_fact.kind is A2AMappedKind.QUARANTINED
    assert task_fact.kind is A2AMappedKind.QUARANTINED
    assert status_fact.kind is A2AMappedKind.QUARANTINED


@pytest.mark.parametrize(
    "state",
    [TaskState.TASK_STATE_INPUT_REQUIRED, TaskState.TASK_STATE_AUTH_REQUIRED],
)
def test_input_and_auth_required_never_disclose_credentials(state: int) -> None:
    fact = map_send_message_response(SendMessageResponse(task=task(state=state)), peer_id=PEER)
    assert fact.kind is A2AMappedKind.TASK
    assert fact.requires_human_input is True
    assert fact.credential_disclosure_allowed is False
    assert fact.terminal_remote_state is False


def test_foreign_ids_are_stable_origin_namespaced_and_not_raw() -> None:
    first = namespace_external_id(PEER, "task", "same-task")
    repeated = namespace_external_id(PEER, "task", "same-task")
    other_peer = namespace_external_id("another-peer", "task", "same-task")
    other_kind = namespace_external_id(PEER, "message", "same-task")

    assert first == repeated
    assert first != other_peer
    assert first != other_kind
    assert "same-task" not in first
    assert first.startswith(external_peer_namespace(PEER))


def test_url_parts_require_ssrf_validation() -> None:
    response = SendMessageResponse(message=message(url="https://files.example/item"))
    held = map_send_message_response(response, peer_id=PEER)
    released_to_tainted_mapping = map_send_message_response(
        response,
        peer_id=PEER,
        url_validator=lambda url: url,
    )
    assert held.kind is A2AMappedKind.QUARANTINED
    assert "SSRF" in (held.quarantined_reason or "")
    assert released_to_tainted_mapping.kind is A2AMappedKind.DIRECT_MESSAGE
    assert released_to_tainted_mapping.authority_eligible is False
