"""Signed concrete corporate client used by the autonomous local daemon."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import asdict
from typing import Any
from urllib.parse import quote

import httpx

from agentnet.client import AgentNetClient
from agentnet.errors import AuthorizationError, ConflictError, ValidationError
from agentnet.bindings.ipc import linux_process_probe
from agentnet.messaging.obligation import MailboxResponseObligation
from agentnet.organization.conflicts import TaskExecutionIntent
from agentnet.protocol.models import (
    MailboxWakeHint,
    ObligationInboxCounters,
    ObligationReconciliationResult,
    SupervisorBackgroundAuthorization,
    SupervisorCustodyReceipt,
    SupervisorResultReceipt,
)
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.supervisor.runtime import BackgroundTurnAuthorization


class AgentNetC0PilotCoreClient:
    """Narrow signed client for the isolated C0 pilot responder."""

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
    def _c0_result(value: Any) -> dict[str, str]:
        allowed = {
            "prepared_unusable",
            "waiting_owner",
            "waiting_fresh",
            "expired",
            "invalidated",
            "COMPLETED_C0_ROUND_TRIP",
        }
        if (
            not isinstance(value, dict)
            or set(value) != {"schema", "status"}
            or value.get("schema") != "agentnet.c0-pilot.result.v1"
            or value.get("status") not in allowed
        ):
            raise ValidationError("C0 pilot response schema is invalid")
        return {"schema": str(value["schema"]), "status": str(value["status"])}

    def c0_pilot_readiness(self) -> dict[str, str]:
        value = self._value(self.client.c0_pilot_readiness())
        if (
            not isinstance(value, dict)
            or set(value) != {"schema", "status"}
            or value.get("schema") != "agentnet.c0-pilot.readiness-result.v1"
            or value.get("status") not in {"waiting_plan", "ready"}
        ):
            raise ValidationError("C0 pilot readiness response schema is invalid")
        return {"schema": str(value["schema"]), "status": str(value["status"])}

    def c0_pilot_respond(self) -> dict[str, str]:
        return self._c0_result(self._value(self.client.c0_pilot_respond()))

    def c0_pilot_status(self) -> dict[str, str]:
        return self._c0_result(self._value(self.client.c0_pilot_status()))


class AgentNetSupervisorCoreClient(AgentNetC0PilotCoreClient):
    """Translate the daemon protocol to signed ordinary-extension requests."""

    def __init__(self, client: AgentNetClient, *, collaboration_scope_id: str) -> None:
        if (
            not collaboration_scope_id
            or len(collaboration_scope_id) > 256
            or any(
                character
                not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-"
                for character in collaboration_scope_id
            )
        ):
            raise ValidationError("supervisor collaboration scope is invalid")
        super().__init__(client)
        self.collaboration_scope_id = collaboration_scope_id


    def _scope_query(self) -> str:
        if not self.collaboration_scope_id:
            raise ValidationError("supervisor collaboration scope is unavailable")
        return quote(self.collaboration_scope_id, safe="")


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
    def _response_obligation(item: Mapping[str, Any]) -> MailboxResponseObligation | None:
        if "response_obligation" not in item:
            raise ValidationError("mailbox item response obligation binding is missing")
        value = item["response_obligation"]
        if value is None:
            return None
        try:
            return MailboxResponseObligation.model_validate(value, strict=True)
        except Exception as exc:
            raise ValidationError(
                "mailbox item response obligation binding is invalid"
            ) from exc

    @staticmethod
    def _historical_authorization(value: Any) -> dict[str, Any]:
        try:
            return SupervisorBackgroundAuthorization.model_validate(
                value,
                strict=True,
            ).model_dump(mode="json")
        except Exception as exc:
            raise ValidationError(
                "local supervisor result authorization is invalid"
            ) from exc


    def reconcile(self, *, after_cursor: int, limit: int) -> list[dict[str, Any]]:
        if after_cursor < 0 or not 1 <= limit <= 100:
            raise ValidationError("supervisor mailbox cursor or limit is invalid")
        value = self._value(
            self.client.request(
                "GET",
                f"/v1/mailbox?after={after_cursor}&limit={limit}"
                f"&collaboration_scope_id={self._scope_query()}",
            )
        )
        if not isinstance(value, dict) or set(value) != {"items"} or not isinstance(value["items"], list):
            raise ValidationError("corporate mailbox response schema is invalid")
        items: list[dict[str, Any]] = []
        for item in value["items"]:
            if not isinstance(item, dict):
                raise ValidationError("corporate mailbox item schema is invalid")
            self._item_binding(item)
            reference = self._response_obligation(item)
            items.append(
                dict(item)
                | {
                    "response_obligation": (
                        reference.model_dump(mode="json")
                        if reference is not None
                        else None
                    )
                }
            )
        return items

    def reconcile_obligations(self, *, limit: int) -> dict[str, list[str]]:
        if not 1 <= limit <= 100:
            raise ValidationError("supervisor obligation reconciliation limit is invalid")
        response = self.client.request(
            "POST",
            "/v1/response-obligations/reconcile",
            json_body={
                "collaboration_scope_id": self.collaboration_scope_id,
                "limit": limit,
            },
        )
        self._value(response)
        try:
            parsed = ObligationReconciliationResult.model_validate_json(
                response.content,
                strict=True,
            )
        except Exception as exc:
            raise ValidationError(
                "obligation reconciliation response schema is invalid"
            ) from exc
        return {
            "recipient_committed": list(parsed.recipient_committed),
            "expired": list(parsed.expired),
        }

    def obligation_inbox(self) -> dict[str, int]:
        response = self.client.request(
            "GET",
            f"/v1/response-obligations/inbox"
            f"?collaboration_scope_id={self._scope_query()}",
        )
        self._value(response)
        try:
            parsed = ObligationInboxCounters.model_validate_json(
                response.content,
                strict=True,
            )
        except Exception as exc:
            raise ValidationError("obligation inbox response schema is invalid") from exc
        return parsed.model_dump(mode="json")

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool:
        """Wait for a content-free hint; authoritative bytes come from reconcile()."""

        if after_cursor < 0 or not 0.05 <= wait_seconds <= 30:
            raise ValidationError("supervisor mailbox watch bounds are invalid")
        wait_ms = max(50, min(30_000, int(wait_seconds * 1_000)))
        response = self.client.request(
            "GET",
            f"/v1/mailbox/watch?after={after_cursor}&wait_ms={wait_ms}"
            f"&collaboration_scope_id={self._scope_query()}",
            timeout_seconds=wait_ms / 1_000 + 2,
        )
        self._value(response)
        try:
            parsed = MailboxWakeHint.model_validate_json(
                response.content,
                strict=True,
            )
        except Exception as exc:
            raise ValidationError("corporate mailbox wake schema is invalid") from exc
        if parsed.kind == "wake" and parsed.cursor_hint <= after_cursor:
            raise ValidationError("corporate mailbox wake cursor did not advance")
        if parsed.kind == "idle" and parsed.cursor_hint != after_cursor:
            raise ValidationError("corporate mailbox idle cursor changed")
        return parsed.kind == "wake"

    def authorize_background(self, obligation_id: str) -> dict[str, Any]:
        if not obligation_id or len(obligation_id) > 256:
            raise ValidationError("background obligation identifier is invalid")
        response = self.client.request(
            "POST",
            "/v1/supervisor/executions/authorize",
            json_body={"obligation_id": obligation_id},
        )
        self._value(response)
        try:
            parsed = SupervisorBackgroundAuthorization.model_validate_json(
                response.content,
                strict=True,
            )
        except Exception as exc:
            raise ValidationError(
                "background authorization response schema is invalid"
            ) from exc
        return parsed.model_dump(mode="json")

    def acknowledge_custody(
        self,
        obligation_id: str,
        authorization: BackgroundTurnAuthorization,
        *,
        local_queue_id: str,
    ) -> None:
        if not obligation_id or len(obligation_id) > 256 or not local_queue_id:
            raise AuthorizationError("local custody obligation binding is invalid")
        response = self.client.request(
            "POST",
            "/v1/supervisor/executions/custody",
            json_body={
                "authorization": asdict(authorization),
                "obligation_id": obligation_id,
                "local_queue_id": local_queue_id,
            },
        )
        self._value(response)
        try:
            receipt = SupervisorCustodyReceipt.model_validate_json(
                response.content,
                strict=True,
            )
        except Exception as exc:
            raise ValidationError("corporate custody receipt schema is invalid") from exc
        if receipt.event_id != authorization.event_id:
            raise ValidationError("corporate custody receipt crossed its event binding")

    def release_task_payload(
        self,
        obligation_id: str,
        authorization: BackgroundTurnAuthorization,
        *,
        local_queue_id: str,
    ) -> dict[str, Any]:
        if not obligation_id or len(obligation_id) > 256 or not local_queue_id:
            raise AuthorizationError("task payload release obligation binding is invalid")
        response = self.client.request(
            "POST",
            "/v1/supervisor/executions/payload-release",
            json_body={
                "authorization": asdict(authorization),
                "obligation_id": obligation_id,
                "local_queue_id": local_queue_id,
            },
        )
        if (
            response.headers.get("cache-control") != "no-store"
            or response.headers.get("pragma") != "no-cache"
        ):
            response.close()
            raise ValidationError("task payload release response is cacheable")
        value = self._value(response)
        required = {
            "classification",
            "duplicate",
            "effect_authorized",
            "envelope_digest",
            "event_id",
            "input_source",
            "intent",
            "intent_digest",
            "output_sink",
            "payload",
            "payload_access_authorized",
            "payload_digest",
            "policy_decision_id",
            "provenance",
            "recipient_harness_id",
            "release_expires_at",
            "release_receipt_id",
            "schema",
            "semantic_processing_authorized",
            "task_grant_id",
            "tool_authorized",
        }
        try:
            intent = TaskExecutionIntent.model_validate_json(
                canonical_json(value["intent"]),
                strict=True,
            )
        except Exception as exc:
            raise ValidationError("task payload release intent is invalid") from exc
        if (
            not isinstance(value, dict)
            or set(value) != required
            or value["schema"] != "agentnet.supervisor.task-payload-release.v1"
            or value["event_id"] != authorization.event_id
            or value["recipient_harness_id"] != authorization.harness_id
            or value["envelope_digest"] != authorization.envelope_digest
            or value["classification"] != authorization.classification
            or value["task_grant_id"] != authorization.task_grant_id
            or value["policy_decision_id"] != authorization.decision_id
            or value["release_expires_at"] != authorization.expires_at
            or value["input_source"] != "mailbox"
            or value["output_sink"] != "receipt"
            or value["payload_access_authorized"] is not True
            or value["semantic_processing_authorized"] is not True
            or value["tool_authorized"] is not False
            or value["effect_authorized"] is not False
            or not isinstance(value["duplicate"], bool)
            or not isinstance(value["release_receipt_id"], str)
            or not value["release_receipt_id"]
            or not isinstance(value["payload"], dict)
            or canonical_digest(value["payload"]) != value["payload_digest"]
            or canonical_digest(intent.model_dump(mode="json")) != value["intent_digest"]
            or not isinstance(value["provenance"], dict)
            or value["provenance"].get("content_digest") != value["payload_digest"]
            or value["provenance"].get("authority_effect") != "none"
        ):
            raise ValidationError("task payload release response schema is invalid")
        return value

    def upload_result(self, result: Mapping[str, Any]) -> None:
        if not isinstance(result, dict) or set(result) != {
            "authorization",
            "native_result",
            "source_queue_id",
        }:
            raise ValidationError("local supervisor result schema is invalid")
        authorization_raw = self._historical_authorization(result["authorization"])
        response = self.client.request(
            "POST",
            "/v1/supervisor/executions/result",
            json_body={
                "authorization": authorization_raw,
                "native_result": result["native_result"],
                "source_queue_id": result["source_queue_id"],
            },
        )
        self._value(response)
        try:
            receipt = SupervisorResultReceipt.model_validate_json(
                response.content,
                strict=True,
            )
        except Exception as exc:
            raise ValidationError("corporate result receipt schema is invalid") from exc
        if (
            receipt.event_id != authorization_raw["event_id"]
            or receipt.provenance.get("authority_effect") != "none"
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
            and value["assurance"] == "server_derived_account_process_parent_module"
            and isinstance(value["bootstrap_socket_path"], str)
            and bool(value["bootstrap_socket_path"])
            and isinstance(value["bootstrap_generation"], str)
            and 24 <= len(value["bootstrap_generation"]) <= 128
        )
        if not direct_valid and not mcp_valid:
            raise ValidationError("local binding issuance response is invalid")
        return value


__all__ = ["AgentNetC0PilotCoreClient", "AgentNetSupervisorCoreClient"]
