"""Official MCP SDK adapter over the canonical server-bound local tools."""

from __future__ import annotations

from typing import Any

from mcp.server.fastmcp import FastMCP

from agentnet.bindings.tools import CanonicalToolDispatcher


def create_mcp_binding(dispatcher: CanonicalToolDispatcher) -> FastMCP:
    """Create MCP tools without accepting an actor, credential, or bearer."""

    server = FastMCP("AgentNet")

    @server.tool(description="Resolve an authorized exact recipient for this authenticated harness")
    def agentnet_recipient_resolve(query: str) -> list[dict[str, Any]]:
        return dispatcher.call("agentnet.recipient.resolve", {"query": query})

    @server.tool(description="Send one local file to authorized exact recipient harnesses")
    def agentnet_file_send(
        collaboration_scope_id: str,
        recipients: list[str],
        source_path: str,
        media_type: str,
        idempotency_key: str,
        classification: str = "C1",
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.file.send",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "classification": classification,
                "idempotency_key": idempotency_key,
                "media_type": media_type,
                "recipients": recipients,
                "source_path": source_path,
            },
        )

    @server.tool(description="Read one authorized file transfer state")
    def agentnet_file_status(
        collaboration_scope_id: str,
        transfer_id: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.file.status",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "transfer_id": transfer_id,
            },
        )

    @server.tool(description="Download one authorized released artifact to a safe local destination")
    def agentnet_file_download(
        collaboration_scope_id: str,
        artifact_id: str,
        destination_path: str,
        idempotency_key: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.file.download",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "artifact_id": artifact_id,
                "destination_path": destination_path,
                "idempotency_key": idempotency_key,
            },
        )

    @server.tool(
        description=(
            "Send an authorized corporate message to either one friendly exact recipient "
            "or explicit exact harness IDs"
        )
    )
    def agentnet_send(
        payload: dict[str, Any],
        idempotency_key: str,
        recipients: list[str] | None = None,
        recipient_query: str | None = None,
        classification: str = "C1",
    ) -> dict[str, Any]:
        arguments: dict[str, Any] = {
            "classification": classification,
            "idempotency_key": idempotency_key,
            "payload": payload,
        }
        if recipients is not None:
            arguments["recipients"] = recipients
        if recipient_query is not None:
            arguments["recipient_query"] = recipient_query
        return dispatcher.call("agentnet.send", arguments)

    @server.tool(description="Explicitly read this authenticated harness mailbox")
    def agentnet_inbox(
        collaboration_scope_id: str,
        after_cursor: int = 0,
        limit: int = 25,
    ) -> list[dict[str, Any]]:
        return dispatcher.call(
            "agentnet.inbox",
            {
                "after_cursor": after_cursor,
                "collaboration_scope_id": collaboration_scope_id,
                "limit": limit,
            },
        )

    @server.tool(description="Record durable custody for one exact mailbox event")
    def agentnet_inbox_acknowledge(
        collaboration_scope_id: str,
        event_id: str,
        envelope_digest: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.inbox.acknowledge",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "envelope_digest": envelope_digest,
                "event_id": event_id,
            },
        )

    @server.tool(description="Create an authorized corporate conversation for this harness")
    def agentnet_conversation_create(
        collaboration_scope_id: str,
        conversation_id: str,
        member_harness_ids: list[str],
        classification: str = "C1",
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.conversation.create",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "classification": classification,
                "conversation_id": conversation_id,
                "member_harness_ids": member_harness_ids,
            },
        )

    @server.tool(
        description=(
            "Post a strictly typed corporate conversation action, including a request with "
            "response_obligation or a bound obligation_response"
        )
    )
    def agentnet_conversation_action(
        collaboration_scope_id: str,
        recipients: list[str],
        conversation_id: str,
        thread_id: str,
        action: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.conversation.action",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "action": action,
                "conversation_id": conversation_id,
                "idempotency_key": idempotency_key,
                "recipients": recipients,
                "thread_id": thread_id,
            },
        )

    @server.tool(description="Read one authorized corporate conversation thread")
    def agentnet_conversation_thread(
        collaboration_scope_id: str,
        conversation_id: str,
        thread_id: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        return dispatcher.call(
            "agentnet.conversation.thread",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "conversation_id": conversation_id,
                "limit": limit,
                "thread_id": thread_id,
            },
        )

    @server.tool(description="Create an authorized persistent room for this authenticated harness")
    def agentnet_room_create(
        collaboration_scope_id: str,
        classification: str = "C1",
        persistent: bool = True,
        expires_at: str | None = None,
        policy: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.room.create",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "classification": classification,
                "expires_at": expires_at,
                "persistent": persistent,
                "policy": policy,
            },
        )

    @server.tool(description="Add one ordinary member to an authorized room")
    def agentnet_room_member_add(
        collaboration_scope_id: str,
        room_id: str,
        harness_id: str,
        role: str = "member",
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.room.member.add",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "harness_id": harness_id,
                "role": role,
                "room_id": room_id,
            },
        )

    @server.tool(description="Describe one room visible to this authenticated harness")
    def agentnet_room_get(
        collaboration_scope_id: str,
        room_id: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.room.get",
            {"collaboration_scope_id": collaboration_scope_id, "room_id": room_id},
        )

    @server.tool(description="Send an artifact-free message to current members of an authorized room")
    def agentnet_room_send(
        collaboration_scope_id: str,
        room_id: str,
        recipients: list[str],
        payload: dict[str, Any],
        idempotency_key: str,
        expected_control_sequence: int,
        classification: str = "C1",
        conversation_id: str | None = None,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.room.send",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "classification": classification,
                "conversation_id": conversation_id,
                "expected_control_sequence": expected_control_sequence,
                "idempotency_key": idempotency_key,
                "payload": payload,
                "recipients": recipients,
                "room_id": room_id,
            },
        )

    @server.tool(description="Read content-free response-obligation attention counters")
    def agentnet_obligation_inbox(collaboration_scope_id: str) -> dict[str, int]:
        return dispatcher.call(
            "agentnet.obligation.inbox",
            {"collaboration_scope_id": collaboration_scope_id},
        )

    @server.tool(description="List response obligations visible to this exact authenticated harness")
    def agentnet_obligation_list(
        collaboration_scope_id: str,
        role: str = "any",
        states: list[str] | None = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        return dispatcher.call(
            "agentnet.obligation.list",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "limit": limit,
                "role": role,
                "states": states or [],
            },
        )

    @server.tool(description="Fetch one response obligation and its typed transition history")
    def agentnet_obligation_get(
        collaboration_scope_id: str,
        obligation_id: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.obligation.get",
            {
                "collaboration_scope_id": collaboration_scope_id,
                "obligation_id": obligation_id,
            },
        )

    @server.tool(description="Record responsible-recipient progress on a response obligation")
    def agentnet_obligation_transition(
        collaboration_scope_id: str,
        obligation_id: str,
        to_state: str,
        reason: str = "recipient_update",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        arguments: dict[str, Any] = {
            "collaboration_scope_id": collaboration_scope_id,
            "obligation_id": obligation_id,
            "reason": reason,
            "to_state": to_state,
        }
        if expected_revision is not None:
            arguments["expected_revision"] = expected_revision
        return dispatcher.call("agentnet.obligation.transition", arguments)

    @server.tool(description="Cancel an open response obligation as its accountable requester")
    def agentnet_obligation_cancel(
        collaboration_scope_id: str,
        obligation_id: str,
        reason_code: str = "requester_canceled",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        arguments: dict[str, Any] = {
            "collaboration_scope_id": collaboration_scope_id,
            "obligation_id": obligation_id,
            "reason_code": reason_code,
        }
        if expected_revision is not None:
            arguments["expected_revision"] = expected_revision
        return dispatcher.call("agentnet.obligation.cancel", arguments)

    @server.tool(description="Reconcile durable obligation custody and requester-owned deadlines")
    def agentnet_obligation_reconcile(
        collaboration_scope_id: str,
        limit: int = 100,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.obligation.reconcile",
            {"collaboration_scope_id": collaboration_scope_id, "limit": limit},
        )

    @server.resource("agentnet://status")
    def agentnet_status() -> str:
        # No message content and no token is returned.
        return '{"kind":"agentnet","state":"ready"}'

    return server
