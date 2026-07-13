"""Signed concrete corporate client used by the autonomous local daemon."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import asdict
from typing import Any

import httpx

from agentnet.client import AgentNetClient
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.bindings.ipc import linux_process_probe
from agentnet.supervisor.runtime import BackgroundTurnAuthorization


class AgentNetSupervisorCoreClient:
    """Translate the daemon protocol to signed ordinary-extension requests."""

    def __init__(self, client: AgentNetClient) -> None:
        self.client = client

    @staticmethod
    def _value(response: httpx.Response) -> Any:
        if response.status_code in {401, 403, 404}:
            raise AuthorizationError("corporate supervisor request was not authorized")
        if response.status_code == 409:
            raise ConflictError("corporate supervisor execution changed concurrently")
        if not 200 <= response.status_code < 300:
            raise ValidationError("corporate supervisor response was unsuccessful")
        try:
            return response.json()
        except ValueError as exc:
            raise ValidationError("corporate supervisor response was not JSON") from exc

    @staticmethod
    def _item_binding(item: Mapping[str, Any]) -> tuple[str, int, str]:
        event = item.get("event")
        cursor = item.get("cursor")
        digest = item.get("envelope_digest")
        if (
            not isinstance(event, dict)
            or not isinstance(event.get("event_id"), str)
            or type(cursor) is not int
            or cursor < 1
            or not isinstance(digest, str)
            or len(digest) != 64
        ):
            raise ValidationError("mailbox item lacks its immutable supervisor binding")
        return event["event_id"], cursor, digest

    @staticmethod
    def _historical_authorization(value: Any) -> dict[str, Any]:
        required = {
            "decision_id",
            "harness_id",
            "event_id",
            "envelope_digest",
            "event_type",
            "classification",
            "policy_revision",
            "expires_at",
            "task_grant_id",
        }
        if not isinstance(value, dict) or set(value) != required:
            raise ValidationError("local supervisor result authorization schema is invalid")
        identifiers = (
            value["decision_id"],
            value["harness_id"],
            value["event_id"],
            value["event_type"],
            value["task_grant_id"],
        )
        if (
            any(not isinstance(item, str) or not 1 <= len(item) <= 256 for item in identifiers)
            or not isinstance(value["envelope_digest"], str)
            or len(value["envelope_digest"]) != 64
            or any(character not in "0123456789abcdef" for character in value["envelope_digest"])
            or value["classification"] not in {"C0", "C1", "C2", "C3"}
            or type(value["policy_revision"]) is not int
            or value["policy_revision"] < 1
            or type(value["expires_at"]) is not int
            or value["expires_at"] < 1
        ):
            raise ValidationError("local supervisor result authorization is invalid")
        return value

    def reconcile(self, *, after_cursor: int, limit: int) -> list[dict[str, Any]]:
        if after_cursor < 0 or not 1 <= limit <= 100:
            raise ValidationError("supervisor mailbox cursor or limit is invalid")
        value = self._value(
            self.client.request("GET", f"/v1/mailbox?after={after_cursor}&limit={limit}")
        )
        if not isinstance(value, dict) or set(value) != {"items"} or not isinstance(value["items"], list):
            raise ValidationError("corporate mailbox response schema is invalid")
        if any(not isinstance(item, dict) for item in value["items"]):
            raise ValidationError("corporate mailbox item schema is invalid")
        return value["items"]

    def reconcile_obligations(self, *, limit: int) -> dict[str, list[str]]:
        if not 1 <= limit <= 100:
            raise ValidationError("supervisor obligation reconciliation limit is invalid")
        value = self._value(
            self.client.request(
                "POST",
                "/v1/response-obligations/reconcile",
                json_body={"limit": limit},
            )
        )
        if (
            not isinstance(value, dict)
            or set(value) != {"recipient_committed", "expired"}
            or any(
                not isinstance(items, list)
                or any(not isinstance(item, str) or not item for item in items)
                for items in value.values()
            )
        ):
            raise ValidationError("obligation reconciliation response schema is invalid")
        return value

    def obligation_inbox(self) -> dict[str, int]:
        required = {
            "unread_information",
            "action_required",
            "awaiting_peer",
            "awaiting_human",
            "overdue",
            "failed",
        }
        value = self._value(self.client.request("GET", "/v1/response-obligations/inbox"))
        if (
            not isinstance(value, dict)
            or set(value) != required
            or any(type(item) is not int or item < 0 for item in value.values())
        ):
            raise ValidationError("obligation inbox response schema is invalid")
        return value

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool:
        """Wait for a content-free hint; authoritative bytes come from reconcile()."""

        if after_cursor < 0 or not 0.05 <= wait_seconds <= 30:
            raise ValidationError("supervisor mailbox watch bounds are invalid")
        wait_ms = max(50, min(30_000, int(wait_seconds * 1_000)))
        value = self._value(
            self.client.request(
                "GET",
                f"/v1/mailbox/watch?after={after_cursor}&wait_ms={wait_ms}",
                timeout_seconds=wait_ms / 1_000 + 2,
            )
        )
        if (
            not isinstance(value, dict)
            or set(value) != {"schema", "kind", "cursor_hint"}
            or value["schema"] != "agentnet.mailbox-wake.v1"
            or value["kind"] not in {"wake", "idle"}
            or type(value["cursor_hint"]) is not int
        ):
            raise ValidationError("corporate mailbox wake schema is invalid")
        if value["kind"] == "wake" and value["cursor_hint"] <= after_cursor:
            raise ValidationError("corporate mailbox wake cursor did not advance")
        if value["kind"] == "idle" and value["cursor_hint"] != after_cursor:
            raise ValidationError("corporate mailbox idle cursor changed")
        return value["kind"] == "wake"

    def authorize_background(self, item: Mapping[str, Any]) -> dict[str, Any]:
        event_id, cursor, envelope_digest = self._item_binding(item)
        value = self._value(
            self.client.request(
                "POST",
                "/v1/supervisor/executions/authorize",
                json_body={
                    "cursor": cursor,
                    "envelope_digest": envelope_digest,
                    "event_id": event_id,
                },
            )
        )
        if not isinstance(value, dict):
            raise ValidationError("background authorization response schema is invalid")
        return asdict(BackgroundTurnAuthorization.from_mapping(value))

    def acknowledge_custody(
        self,
        item: Mapping[str, Any],
        authorization: BackgroundTurnAuthorization,
        *,
        local_queue_id: str,
    ) -> None:
        event_id, cursor, envelope_digest = self._item_binding(item)
        if (
            event_id != authorization.event_id
            or envelope_digest != authorization.envelope_digest
            or not local_queue_id
        ):
            raise AuthorizationError("local custody does not bind the authorized mailbox item")
        value = self._value(
            self.client.request(
                "POST",
                "/v1/supervisor/executions/custody",
                json_body={
                    "authorization": asdict(authorization),
                    "cursor": cursor,
                    "local_queue_id": local_queue_id,
                },
            )
        )
        if (
            not isinstance(value, dict)
            or set(value) != {"custody_receipt_id", "duplicate", "event_id", "state"}
            or value["event_id"] != event_id
            or value["state"] not in {"local_custody", "result_uploaded"}
        ):
            raise ValidationError("corporate custody receipt schema is invalid")

    def upload_result(self, result: Mapping[str, Any]) -> None:
        if not isinstance(result, dict) or set(result) != {
            "authorization",
            "native_result",
            "source_queue_id",
        }:
            raise ValidationError("local supervisor result schema is invalid")
        authorization_raw = self._historical_authorization(result["authorization"])
        value = self._value(
            self.client.request(
                "POST",
                "/v1/supervisor/executions/result",
                json_body={
                    "authorization": authorization_raw,
                    "native_result": result["native_result"],
                    "source_queue_id": result["source_queue_id"],
                },
            )
        )
        if (
            not isinstance(value, dict)
            or set(value)
            != {
                "duplicate",
                "event_id",
                "provenance",
                "result_digest",
                "result_receipt_id",
                "state",
            }
            or value["event_id"] != authorization_raw["event_id"]
            or value["state"] != "result_uploaded"
            or not isinstance(value["provenance"], dict)
            or value["provenance"].get("authority_effect") != "none"
        ):
            raise ValidationError("corporate result receipt schema is invalid")

    def execution_status(self, event_id: str) -> dict[str, Any]:
        if not event_id or "/" in event_id:
            raise ValidationError("supervisor event identifier is invalid")
        value = self._value(
            self.client.request("GET", f"/v1/supervisor/executions/{event_id}/status")
        )
        if not isinstance(value, dict) or value.get("schema") != "agentnet.supervisor.execution-status.v1":
            raise ValidationError("corporate supervisor status schema is invalid")
        return value

    def issue_local_binding(self, *, pid: int, session_id: str) -> dict[str, Any]:
        """Register an MCP parent or request one Pi direct-IPC capability."""

        if pid <= 0 or not session_id:
            raise ValidationError("local binding child identity is invalid")
        process_start_time, process_measurement = linux_process_probe(pid)
        value = self._value(
            self.client.request(
                "POST",
                "/v1/supervisor/local-binding/children",
                json_body={
                    "pid": pid,
                    "session_id": session_id,
                    "process_start_time": process_start_time,
                    "process_measurement": process_measurement,
                },
            )
        )
        direct_required = {
            "schema",
            "capability",
            "session_id",
            "harness_id",
            "credential_id",
            "credential_epoch",
            "expires_at",
            "socket_path",
        }
        mcp_required = {
            "schema",
            "session_id",
            "harness_id",
            "credential_id",
            "credential_epoch",
            "expires_at",
            "bootstrap_socket_path",
            "bootstrap_generation",
            "assurance",
        }
        direct_valid = (
            isinstance(value, dict)
            and set(value) == direct_required
            and value["schema"] == "agentnet.ipc.issued-child.v1"
            and value["session_id"] == session_id
            and isinstance(value["capability"], str)
            and len(value["capability"]) >= 64
        )
        mcp_valid = (
            isinstance(value, dict)
            and set(value) == mcp_required
            and value["schema"] == "agentnet.mcp.registered-launch.v1"
            and value["session_id"] == session_id
            and value["assurance"] == "same_uid_peercred_direct_parent_module"
            and isinstance(value["bootstrap_socket_path"], str)
            and bool(value["bootstrap_socket_path"])
            and isinstance(value["bootstrap_generation"], str)
            and 24 <= len(value["bootstrap_generation"]) <= 128
        )
        if not direct_valid and not mcp_valid:
            raise ValidationError("local binding issuance response is invalid")
        return value


__all__ = ["AgentNetSupervisorCoreClient"]
