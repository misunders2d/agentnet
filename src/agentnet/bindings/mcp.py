"""Official MCP SDK adapter over the canonical server-bound local tools."""

from __future__ import annotations

from typing import Any

from mcp.server.fastmcp import FastMCP

from agentnet.bindings.tools import CanonicalToolDispatcher


def create_mcp_binding(dispatcher: CanonicalToolDispatcher) -> FastMCP:
    """Create MCP tools without accepting an actor, credential, or bearer."""

    server = FastMCP("AgentNet")

    @server.tool(description="Send an authorized corporate message from this authenticated harness binding")
    def agentnet_send(
        recipients: list[str],
        payload: dict[str, Any],
        idempotency_key: str,
        classification: str = "C1",
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.send",
            {
                "classification": classification,
                "idempotency_key": idempotency_key,
                "payload": payload,
                "recipients": recipients,
            },
        )

    @server.tool(description="Explicitly read this authenticated harness mailbox")
    def agentnet_inbox(after_cursor: int = 0, limit: int = 25) -> list[dict[str, Any]]:
        return dispatcher.call(
            "agentnet.inbox",
            {"after_cursor": after_cursor, "limit": limit},
        )

    @server.tool(description="Record durable custody for one exact mailbox event")
    def agentnet_inbox_acknowledge(
        event_id: str,
        envelope_digest: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.inbox.acknowledge",
            {"envelope_digest": envelope_digest, "event_id": event_id},
        )

    @server.tool(description="Create an authorized corporate conversation for this harness")
    def agentnet_conversation_create(
        conversation_id: str,
        member_harness_ids: list[str],
        classification: str = "C1",
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.conversation.create",
            {
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
        recipients: list[str],
        conversation_id: str,
        thread_id: str,
        action: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        return dispatcher.call(
            "agentnet.conversation.action",
            {
                "action": action,
                "conversation_id": conversation_id,
                "idempotency_key": idempotency_key,
                "recipients": recipients,
                "thread_id": thread_id,
            },
        )

    @server.tool(description="Read one authorized corporate conversation thread")
    def agentnet_conversation_thread(
        conversation_id: str,
        thread_id: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        return dispatcher.call(
            "agentnet.conversation.thread",
            {"conversation_id": conversation_id, "limit": limit, "thread_id": thread_id},
        )

    @server.tool(description="Read content-free response-obligation attention counters")
    def agentnet_obligation_inbox() -> dict[str, int]:
        return dispatcher.call("agentnet.obligation.inbox", {})

    @server.tool(description="List response obligations visible to this exact authenticated harness")
    def agentnet_obligation_list(
        role: str = "any",
        states: list[str] | None = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        return dispatcher.call(
            "agentnet.obligation.list",
            {"limit": limit, "role": role, "states": states or []},
        )

    @server.tool(description="Fetch one response obligation and its typed transition history")
    def agentnet_obligation_get(obligation_id: str) -> dict[str, Any]:
        return dispatcher.call("agentnet.obligation.get", {"obligation_id": obligation_id})

    @server.tool(description="Record responsible-recipient progress on a response obligation")
    def agentnet_obligation_transition(
        obligation_id: str,
        to_state: str,
        reason: str = "recipient_update",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        arguments: dict[str, Any] = {
            "obligation_id": obligation_id,
            "reason": reason,
            "to_state": to_state,
        }
        if expected_revision is not None:
            arguments["expected_revision"] = expected_revision
        return dispatcher.call("agentnet.obligation.transition", arguments)

    @server.tool(description="Cancel an open response obligation as its accountable requester")
    def agentnet_obligation_cancel(
        obligation_id: str,
        reason_code: str = "requester_canceled",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        arguments: dict[str, Any] = {
            "obligation_id": obligation_id,
            "reason_code": reason_code,
        }
        if expected_revision is not None:
            arguments["expected_revision"] = expected_revision
        return dispatcher.call("agentnet.obligation.cancel", arguments)

    @server.tool(description="Reconcile durable obligation custody and requester-owned deadlines")
    def agentnet_obligation_reconcile(limit: int = 100) -> dict[str, Any]:
        return dispatcher.call("agentnet.obligation.reconcile", {"limit": limit})

    @server.resource("agentnet://status")
    def agentnet_status() -> str:
        # No message content and no token is returned.
        return '{"kind":"agentnet","state":"ready"}'

    return server
