from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
import threading
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import httpx
import pytest

from agentnet.adapters.auth import EphemeralBrokerEnvironment
from agentnet.adapters.specs import build_launch_spec
from agentnet.client import AgentNetClient
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import AuthorizationError
from agentnet.http_api import create_app
from agentnet.messaging.events import new_event
from agentnet.operations.config import (
    ExtensionConfig,
    FeatureFlags,
    LocalBindingConfig,
)
from agentnet.protocol.models import Classification, EventType
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.client import AgentNetSupervisorCoreClient
from agentnet.supervisor.integration import BackgroundHarnessIntegration
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.runtime import (
    BackgroundAdapterRuntime,
    BackgroundTurnAuthorization,
)
from agentnet.supervisor.service import DeviceSupervisor


class SyncASGITransport(httpx.BaseTransport):
    """Small synchronous bridge so the real signed client can call ASGI in-process."""

    def __init__(self, app: Any) -> None:
        self.app = app

    def handle_request(self, request: httpx.Request) -> httpx.Response:
        body = request.read()

        async def invoke() -> tuple[int, list[tuple[bytes, bytes]], bytes, dict[str, Any]]:
            async with httpx.AsyncClient(
                transport=httpx.ASGITransport(app=self.app, raise_app_exceptions=False)
            ) as client:
                response = await client.request(
                    request.method,
                    str(request.url),
                    content=body,
                    headers=request.headers,
                )
                content = await response.aread()
                return response.status_code, response.headers.raw, content, response.extensions

        status, headers, content, extensions = asyncio.run(invoke())
        return httpx.Response(
            status,
            headers=headers,
            content=content,
            extensions=extensions,
            request=request,
        )


class FailFirstResultUpload:
    """Models a process/network break after durable local output exists."""

    def __init__(self, inner: AgentNetSupervisorCoreClient) -> None:
        self.inner = inner
        self.failed = threading.Event()
        self.last_item: dict[str, Any] | None = None
        self.last_authorization = None
        self.last_result: dict[str, Any] | None = None

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool:
        return self.inner.watch(after_cursor=after_cursor, wait_seconds=wait_seconds)

    def reconcile(self, *, after_cursor: int, limit: int):
        items = self.inner.reconcile(after_cursor=after_cursor, limit=limit)
        if items:
            self.last_item = items[0]
        return items

    def reconcile_obligations(self, *, limit: int):
        return self.inner.reconcile_obligations(limit=limit)

    def obligation_inbox(self):
        return self.inner.obligation_inbox()

    def authorize_background(self, item):
        value = self.inner.authorize_background(item)
        self.last_authorization = value
        return value

    def acknowledge_custody(self, item, authorization, *, local_queue_id: str) -> None:
        self.inner.acknowledge_custody(
            item,
            authorization,
            local_queue_id=local_queue_id,
        )

    def upload_result(self, result) -> None:
        self.last_result = dict(result)
        if not self.failed.is_set():
            self.failed.set()
            raise RuntimeError("synthetic connection loss before result upload")
        self.inner.upload_result(result)


def _wait_for(predicate, *, timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.025)
    raise AssertionError("bounded autonomous supervisor wait expired")


def test_signed_supervisor_generic_mailbox_refuses_task_payload_before_authorize(
    tmp_path: Path,
    store,
    identity_factory,
    execution_grant_factory,
) -> None:
    sender, _sender_key = identity_factory(binding_assurance="os_bound")
    recipient, recipient_key = identity_factory(kind="codex", binding_assurance="os_bound")
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=recipient.domain_id,
            data_dir=tmp_path / "core-data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.TASK_ASSIGNMENT,
        classification=Classification.C2_RESTRICTED,
        payload={"task": "autonomous-e2e-secret"},
        idempotency_key="supervisor-e2e-task-0001",
        recipients=(recipient.harness_id,),
        task_id="supervisor-e2e-task",
        retention_delete_at=datetime.now(UTC) + timedelta(hours=1),
        policy_revision=1,
    )
    core.mailboxes.accept(event)
    grant = execution_grant_factory(recipient=recipient, event_id=event.event_id)
    core.grant_local_entitlement(
        recipient,
        action="mailbox.read",
        resource=recipient.harness_id,
    )
    core.grant_local_entitlement(
        recipient,
        action="task.process",
        resource=f"event:{event.event_id}",
    )

    app = create_app(core)
    signed = AgentNetClient(
        base_url="http://127.0.0.1",
        key=recipient_key,
        domain_id=recipient.domain_id,
        harness_id=recipient.harness_id,
        credential_id=recipient.credential_id,
        audience=f"urn:agentnet:{recipient.domain_id}:corporate-api",
        transport=SyncASGITransport(app),
    )
    concrete = AgentNetSupervisorCoreClient(signed)
    try:
        assert concrete.watch(after_cursor=0, wait_seconds=0.05) is True
        item = concrete.reconcile(after_cursor=0, limit=10)[0]
        assert concrete.watch(after_cursor=item["cursor"], wait_seconds=0.05) is False
        assert item["payload"] is None
        assert item["payload_available"] is False
        assert item["payload_access"] == "task_grant_required"
        assert item["payload_withheld_reason"] == "exact_task_grant_required"
        assert "autonomous-e2e-secret" not in json.dumps(item, sort_keys=True)
        with pytest.raises(AuthorizationError, match="payload is unavailable"):
            BackgroundHarnessIntegration._mailbox_item(item)
        assert store.fetch_one(
            "SELECT 1 FROM supervisor_executions WHERE event_id=?",
            (event.event_id,),
        ) is None
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 0
    finally:
        signed.close()


@pytest.mark.parametrize("harness_kind", ["codex", "pi"])
def test_signed_supervisor_registers_only_its_measured_current_epoch_child(
    tmp_path: Path,
    store,
    identity_factory,
    harness_kind: str,
) -> None:
    actor, key = identity_factory(kind=harness_kind, binding_assurance="os_bound")
    secrets_dir = tmp_path / "core-data" / "secrets"
    secrets_dir.mkdir(parents=True, mode=0o700)
    root = secrets_dir / "ipc-root.key"
    root.write_bytes(b"signed-supervisor-local-binding-root")
    os.chmod(root, 0o600)
    config = ExtensionConfig(
        domain_id=actor.domain_id,
        data_dir=tmp_path / "core-data",
        database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
        public_base_url="http://127.0.0.1",
        features=FeatureFlags(local_bindings=True),
        server_agent_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
                ServerAgentCapability.LOCAL_BINDING,
            }
        ),
        local_bindings=LocalBindingConfig(
            socket_path=Path("runtime/agentnet.sock"),
            capability_root_path=Path("secrets/ipc-root.key"),
        ),
    )
    core = CommunicationCore(config, store)
    app = create_app(core)
    child = subprocess.Popen(
        [sys.executable, "-c", "import time; time.sleep(30)"],
        env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")},
    )
    signed = AgentNetClient(
        base_url="http://127.0.0.1",
        key=key,
        domain_id=actor.domain_id,
        harness_id=actor.harness_id,
        credential_id=actor.credential_id,
        audience=f"urn:agentnet:{actor.domain_id}:corporate-api",
        transport=SyncASGITransport(app),
    )
    try:
        issued = AgentNetSupervisorCoreClient(signed).issue_local_binding(
            pid=child.pid,
            session_id=f"signed-supervisor-{harness_kind}-child-001",
        )
        assert issued["harness_id"] == actor.harness_id
        assert issued["credential_id"] == actor.credential_id
        assert issued["credential_epoch"] == actor.credential_epoch
        if harness_kind == "pi":
            assert issued["schema"] == "agentnet.ipc.issued-child.v1"
            assert "capability" in issued
        else:
            assert issued["schema"] == "agentnet.mcp.registered-launch.v1"
            assert "capability" not in issued
            assert issued["assurance"] == "same_uid_peercred_direct_parent_module"

        injected = signed.request(
            "POST",
            "/v1/supervisor/local-binding/children",
            json_body={
                "pid": child.pid,
                "session_id": f"signed-supervisor-{harness_kind}-child-002",
                "harness_id": "caller-must-not-select-identity",
            },
        )
        assert injected.status_code == 422
    finally:
        signed.close()
        child.terminate()
        child.wait(timeout=5)


def test_signed_supervisor_result_provenance_is_atomic_replay_safe_and_fail_closed(
    tmp_path: Path,
    store,
    identity_factory,
    execution_grant_factory,
) -> None:
    sender, _sender_key = identity_factory(binding_assurance="os_bound")
    recipient, recipient_key = identity_factory(kind="codex", binding_assurance="os_bound")
    core = CommunicationCore(
        ExtensionConfig(
            domain_id=recipient.domain_id,
            data_dir=tmp_path / "core-data",
            database_url=f"sqlite:///{tmp_path / 'unused.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    core.grant_local_entitlement(
        recipient,
        action="mailbox.read",
        resource=recipient.harness_id,
    )
    core.grant_local_entitlement(
        recipient,
        action="mailbox.acknowledge",
        resource=recipient.harness_id,
    )
    app = create_app(core)
    signed = AgentNetClient(
        base_url="http://127.0.0.1",
        key=recipient_key,
        domain_id=recipient.domain_id,
        harness_id=recipient.harness_id,
        credential_id=recipient.credential_id,
        audience=f"urn:agentnet:{recipient.domain_id}:corporate-api",
        transport=SyncASGITransport(app),
    )
    concrete = AgentNetSupervisorCoreClient(signed)

    def prepare(sequence: int) -> tuple[Any, dict[str, Any], dict[str, Any]]:
        event = new_event(
            domain_id=sender.domain_id,
            actor=sender,
            event_type=EventType.TASK_ASSIGNMENT,
            classification=Classification.C2_RESTRICTED,
            payload={"task": f"provenance-bound-result-{sequence}"},
            idempotency_key=f"supervisor-result-provenance-{sequence:04d}",
            recipients=(recipient.harness_id,),
            task_id=f"supervisor-result-provenance-{sequence}",
            retention_delete_at=datetime.now(UTC) + timedelta(hours=1),
            policy_revision=1,
        )
        core.mailboxes.accept(event)
        execution_grant_factory(recipient=recipient, event_id=event.event_id)
        core.grant_local_entitlement(
            recipient,
            action="task.process",
            resource=f"event:{event.event_id}",
        )
        items = concrete.reconcile(after_cursor=0, limit=10)
        item = next(value for value in items if value["event"]["event_id"] == event.event_id)
        authorization = concrete.authorize_background(item)
        delivery_receipt_count = store.fetch_one(
            """SELECT COUNT(*) AS count FROM receipts
               WHERE event_id=? AND recipient_id=? AND fact='recipient_committed'""",
            (event.event_id, recipient.harness_id),
        )["count"]
        if sequence == 1:
            acknowledged = signed.acknowledge_mailbox(
                event_id=event.event_id,
                envelope_digest=item["envelope_digest"],
            )
            assert acknowledged.status_code == 200
            assert acknowledged.json()["duplicate"] is False
            delivery_receipt_count += 1
        queue_id = f"local-result-provenance-queue-{sequence:04d}"
        custody = signed.request(
            "POST",
            "/v1/supervisor/executions/custody",
            json_body={
                "authorization": authorization,
                "cursor": item["cursor"],
                "local_queue_id": queue_id,
            },
        )
        assert custody.status_code == 201
        assert store.fetch_one(
            """SELECT COUNT(*) AS count FROM receipts
               WHERE event_id=? AND recipient_id=? AND fact='recipient_committed'""",
            (event.event_id, recipient.harness_id),
        )["count"] == delivery_receipt_count + (0 if sequence == 1 else 1)
        return event, authorization, {
            "authorization": authorization,
            "native_result": {
                "output": f"native-result-{sequence}",
                "terminal_event": "turn/completed",
            },
            "source_queue_id": queue_id,
        }

    try:
        event, _authorization, result_body = prepare(1)
        uploaded = signed.request(
            "POST",
            "/v1/supervisor/executions/result",
            json_body=result_body,
        )
        assert uploaded.status_code == 201
        value = uploaded.json()
        assert value["provenance"]["object_type"] == "parser_output"
        assert value["provenance"]["classification"] == "C2"
        assert value["provenance"]["tainted"] is True
        assert value["provenance"]["authority_effect"] == "none"
        persisted = store.fetch_one(
            "SELECT * FROM supervisor_executions WHERE event_id=?",
            (event.event_id,),
        )
        assert persisted["result_provenance_digest"] == value["provenance"]["provenance_digest"]

        duplicate = signed.request(
            "POST",
            "/v1/supervisor/executions/result",
            json_body=result_body,
        )
        assert duplicate.status_code == 200
        assert duplicate.json()["provenance"] == value["provenance"]

        failed_event, _failed_authorization, failed_body = prepare(2)
        before = store.fetch_one(
            "SELECT COUNT(*) AS count FROM content_provenance WHERE object_type='parser_output'"
        )["count"]
        with store.transaction() as connection:
            connection.execute(
                "DELETE FROM event_provenance WHERE event_id=?",
                (failed_event.event_id,),
            )
        refused = signed.request(
            "POST",
            "/v1/supervisor/executions/result",
            json_body=failed_body,
        )
        assert refused.status_code == 409
        assert store.fetch_one(
            "SELECT state FROM supervisor_executions WHERE event_id=?",
            (failed_event.event_id,),
        )["state"] == "local_custody"
        assert store.fetch_one(
            "SELECT COUNT(*) AS count FROM content_provenance WHERE object_type='parser_output'"
        )["count"] == before

        with store.transaction() as connection:
            connection.execute(
                "UPDATE content_provenance SET transformations_json='{}' WHERE provenance_digest=?",
                (value["provenance"]["provenance_digest"],),
            )
        corrupt_replay = signed.request(
            "POST",
            "/v1/supervisor/executions/result",
            json_body=result_body,
        )
        assert corrupt_replay.status_code == 409
    finally:
        signed.close()
