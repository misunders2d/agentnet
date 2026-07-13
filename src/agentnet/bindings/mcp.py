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

    @server.resource("agentnet://status")
    def agentnet_status() -> str:
        # No message content and no token is returned.
        return '{"kind":"agentnet","state":"ready"}'

    return server
