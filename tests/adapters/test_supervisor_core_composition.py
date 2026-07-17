from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import asdict
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
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.http_api import create_app
from agentnet.messaging.events import new_event
from agentnet.operations.config import (
    ExtensionConfig,
    FeatureFlags,
    LocalBindingConfig,
)
from agentnet.organization.conflicts import (
    TaskAccessMode,
    TaskExecutionIntent,
    TaskExclusivity,
    TaskResourceIntent,
)
from agentnet.protocol.models import Classification, DeliveryFact, EventType
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.supervisor.client import AgentNetSupervisorCoreClient
from agentnet.supervisor.integration import BackgroundHarnessIntegration
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.runtime import (
    BackgroundAdapterRuntime,
    BackgroundTurnAuthorization,
)
from agentnet.supervisor.service import DeviceSupervisor
from agentnet.supervisor_http import PayloadReleaseBody, SupervisorExecutionService


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

    def release_task_payload(self, item, authorization, *, local_queue_id: str):
        return self.inner.release_task_payload(
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
        assert BackgroundHarnessIntegration._mailbox_item(item) == (
            event.event_id,
            item["cursor"],
            item["envelope_digest"],
        )
        assert store.fetch_one(
            "SELECT 1 FROM supervisor_executions WHERE event_id=?",
            (event.event_id,),
        ) is None
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 0
    finally:
        signed.close()


def test_signed_supervisor_releases_exact_task_payload_once_after_local_custody(
    tmp_path: Path,
    store,
    identity_factory,
    execution_grant_factory,
) -> None:
    sender, _sender_key = identity_factory(binding_assurance="os_bound")
    recipient, recipient_key = identity_factory(kind="codex", binding_assurance="os_bound")
    outsider, outsider_key = identity_factory(kind="pi", binding_assurance="os_bound")
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
    now = datetime.now(UTC).replace(microsecond=0)
    deadline = now + timedelta(minutes=15)
    secret_payload = {"task": "recipient-owned-payload-release", "value": 7}
    event = new_event(
        domain_id=sender.domain_id,
        actor=sender,
        event_type=EventType.TASK_ASSIGNMENT,
        classification=Classification.C2_RESTRICTED,
        payload=secret_payload,
        idempotency_key="supervisor-payload-release-0001",
        recipients=(recipient.harness_id,),
        task_id="supervisor-payload-release-task",
        delivery_expires_at=deadline,
        effect_deadline=deadline,
        retention_delete_at=deadline,
        policy_revision=1,
    ).model_copy(update={"payload_access": "task_grant_required"})
    core.mailboxes.accept(event)
    intent = TaskExecutionIntent(
        resources=(
            TaskResourceIntent(
                resource="dataset:recipient-owned",
                operation="summarize",
                access=TaskAccessMode.READ,
                exclusivity=TaskExclusivity.SHARED,
            ),
        )
    )
    with store.transaction() as connection:
        admission = core.assignments.conflicts.record_accepted_in_transaction(
            connection,
            event_id=event.event_id,
            domain_id=recipient.domain_id,
            recipient_harness_id=recipient.harness_id,
            sender_harness_id=sender.harness_id,
            sender_authority_id=sender.positive_authority_id,
            authority_basis="recipient_owner_approval",
            relationship_id=None,
            relationship_revision=0,
            intent=intent,
            continuation={},
            deadline=deadline,
            when=now,
        )
        assert admission.fact is DeliveryFact.ACCEPTED_QUEUED
        connection.execute(
            "UPDATE recipients SET current_fact=?,updated_at=? WHERE event_id=? AND recipient_id=?",
            (
                DeliveryFact.ACCEPTED_QUEUED.value,
                int(now.timestamp()),
                event.event_id,
                recipient.harness_id,
            ),
        )
    grant = execution_grant_factory(
        recipient=recipient,
        event_id=event.event_id,
        actions=frozenset({"task.process"}),
        max_uses=1,
    )
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
        item = concrete.reconcile(after_cursor=0, limit=10)[0]
        assert item["payload"] is None
        authorization = BackgroundTurnAuthorization.from_mapping(
            concrete.authorize_background(item)
        )
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 1
        queue_id = "local-payload-release-queue-0001"
        with pytest.raises(AuthorizationError):
            concrete.release_task_payload(
                item,
                authorization,
                local_queue_id=queue_id,
            )
        assert store.fetch_one(
            "SELECT 1 FROM task_payload_releases WHERE event_id=?",
            (event.event_id,),
        ) is None
        concrete.acknowledge_custody(
            item,
            authorization,
            local_queue_id=queue_id,
        )
        release_body = PayloadReleaseBody(
            authorization=asdict(authorization),
            cursor=item["cursor"],
            local_queue_id=queue_id,
        )
        injected_idempotency = signed.request(
            "POST",
            "/v1/supervisor/executions/payload-release",
            json_body=release_body.model_dump(mode="json")
            | {"idempotency_key": "caller-selected-release-key"},
        )
        assert injected_idempotency.status_code == 422
        coerced_cursor = release_body.model_dump(mode="json")
        coerced_cursor["cursor"] = str(item["cursor"])
        rejected_coercion = signed.request(
            "POST",
            "/v1/supervisor/executions/payload-release",
            json_body=coerced_cursor,
        )
        assert rejected_coercion.status_code == 422
        outsider_signed = AgentNetClient(
            base_url="http://127.0.0.1",
            key=outsider_key,
            domain_id=outsider.domain_id,
            harness_id=outsider.harness_id,
            credential_id=outsider.credential_id,
            audience=f"urn:agentnet:{outsider.domain_id}:corporate-api",
            transport=SyncASGITransport(app),
        )
        try:
            wrong_recipient = outsider_signed.request(
                "POST",
                "/v1/supervisor/executions/payload-release",
                json_body=release_body.model_dump(mode="json"),
            )
            assert wrong_recipient.status_code == 404
        finally:
            outsider_signed.close()

        def fail_after_release_audit(phase: str) -> None:
            if phase == "after_release_audit":
                raise RuntimeError("synthetic release commit failure")

        with pytest.raises(RuntimeError, match="synthetic release commit failure"):
            SupervisorExecutionService(core).release_task_payload(
                actor=recipient,
                body=release_body,
                phase_hook=fail_after_release_audit,
            )
        assert store.fetch_one(
            "SELECT 1 FROM task_payload_releases WHERE event_id=?",
            (event.event_id,),
        ) is None
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 1
        assert sum(
            '"action":"supervisor.task_payload.released"' in row["record_json"]
            for row in store.fetch_all("SELECT record_json FROM audit_log")
        ) == 0

        released = concrete.release_task_payload(
            item,
            authorization,
            local_queue_id=queue_id,
        )
        assert released["duplicate"] is False
        assert released["payload"] == secret_payload
        assert released["intent"] == intent.model_dump(mode="json")
        assert released["tool_authorized"] is False
        assert released["effect_authorized"] is False
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 1
        persisted = store.fetch_one(
            "SELECT * FROM task_payload_releases WHERE event_id=? AND recipient_harness_id=?",
            (event.event_id, recipient.harness_id),
        )
        assert persisted["release_receipt_id"] == released["release_receipt_id"]

        duplicate = concrete.release_task_payload(
            item,
            authorization,
            local_queue_id=queue_id,
        )
        assert duplicate["duplicate"] is True
        assert duplicate["release_receipt_id"] == released["release_receipt_id"]
        assert duplicate["payload"] == secret_payload
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 1
        with ThreadPoolExecutor(max_workers=2) as pool:
            retries = list(
                pool.map(
                    lambda _index: SupervisorExecutionService(core).release_task_payload(
                        actor=recipient,
                        body=release_body,
                    ),
                    range(2),
                )
            )
        assert [value["duplicate"] for value in retries] == [True, True]
        assert {
            value["release_receipt_id"] for value in retries
        } == {released["release_receipt_id"]}
        assert core.grants.uses_for_local_conformance(grant.grant_id) == 1

        def assert_release_denied() -> None:
            with pytest.raises(AuthorizationError):
                concrete.release_task_payload(
                    item,
                    authorization,
                    local_queue_id=queue_id,
                )

        with pytest.raises(AuthorizationError):
            concrete.release_task_payload(
                item,
                authorization,
                local_queue_id="different-local-queue-0001",
            )

        domain_state = store.fetch_one(
            "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (recipient.domain_id,),
        )
        harness_epoch = int(
            store.fetch_one(
                "SELECT credential_epoch FROM harnesses WHERE harness_id=?",
                (recipient.harness_id,),
            )["credential_epoch"]
        )
        with store.transaction() as connection:
            connection.execute(
                "UPDATE domains SET policy_revision=? WHERE domain_id=?",
                (int(domain_state["policy_revision"]) + 1, recipient.domain_id),
            )
        assert_release_denied()
        with store.transaction() as connection:
            connection.execute(
                "UPDATE domains SET policy_revision=? WHERE domain_id=?",
                (int(domain_state["policy_revision"]), recipient.domain_id),
            )
            connection.execute(
                "UPDATE harnesses SET credential_epoch=? WHERE harness_id=?",
                (harness_epoch + 1, recipient.harness_id),
            )
        assert_release_denied()
        with store.transaction() as connection:
            connection.execute(
                "UPDATE harnesses SET credential_epoch=? WHERE harness_id=?",
                (harness_epoch, recipient.harness_id),
            )
            connection.execute(
                "UPDATE domains SET revocation_epoch=? WHERE domain_id=?",
                (int(domain_state["revocation_epoch"]) + 1, recipient.domain_id),
            )
        assert_release_denied()
        with store.transaction() as connection:
            connection.execute(
                "UPDATE domains SET revocation_epoch=? WHERE domain_id=?",
                (int(domain_state["revocation_epoch"]), recipient.domain_id),
            )
            connection.execute(
                "UPDATE supervisor_executions SET authorization_expires_at=? WHERE event_id=?",
                (int(time.time()), event.event_id),
            )
        assert_release_denied()
        with store.transaction() as connection:
            connection.execute(
                "UPDATE supervisor_executions SET authorization_expires_at=? WHERE event_id=?",
                (authorization.expires_at, event.event_id),
            )

        original_grant_json = str(
            store.fetch_one(
                "SELECT grant_json FROM task_grants WHERE grant_id=?",
                (grant.grant_id,),
            )["grant_json"]
        )
        grant_value = json.loads(original_grant_json)
        for field, wrong_value in (
            ("actions", ["message.process"]),
            ("resources", ["event:not-this-task"]),
            ("input_sources", ["artifact"]),
            ("output_sinks", ["external-effect"]),
            ("data_classes", [Classification.C1_INTERNAL.value]),
        ):
            mutated_grant = dict(grant_value)
            mutated_grant[field] = wrong_value
            with store.transaction() as connection:
                connection.execute(
                    "UPDATE task_grants SET grant_json=? WHERE grant_id=?",
                    (
                        json.dumps(
                            mutated_grant,
                            allow_nan=False,
                            separators=(",", ":"),
                            sort_keys=True,
                        ),
                        grant.grant_id,
                    ),
                )
            try:
                assert_release_denied()
            finally:
                with store.transaction() as connection:
                    connection.execute(
                        "UPDATE task_grants SET grant_json=? WHERE grant_id=?",
                        (original_grant_json, grant.grant_id),
                    )

        with store.transaction() as connection:
            connection.execute(
                "UPDATE task_execution_intents SET state='conflict_pending' WHERE event_id=?",
                (event.event_id,),
            )
            connection.execute(
                "UPDATE recipients SET current_fact=? WHERE event_id=? AND recipient_id=?",
                (
                    DeliveryFact.CONFLICT_PENDING.value,
                    event.event_id,
                    recipient.harness_id,
                ),
            )
        with pytest.raises(AuthorizationError):
            concrete.release_task_payload(
                item,
                authorization,
                local_queue_id=queue_id,
            )
        with store.transaction() as connection:
            connection.execute(
                "UPDATE task_execution_intents SET state='active' WHERE event_id=?",
                (event.event_id,),
            )
            connection.execute(
                "UPDATE recipients SET current_fact=? WHERE event_id=? AND recipient_id=?",
                (
                    DeliveryFact.RECIPIENT_COMMITTED.value,
                    event.event_id,
                    recipient.harness_id,
                ),
            )
            connection.execute(
                "UPDATE events SET retention_delete_at=? WHERE event_id=?",
                (int(time.time()), event.event_id),
            )
        with pytest.raises(AuthorizationError):
            concrete.release_task_payload(
                item,
                authorization,
                local_queue_id=queue_id,
            )
        with store.transaction() as connection:
            connection.execute(
                "UPDATE events SET retention_delete_at=? WHERE event_id=?",
                (int(deadline.timestamp()), event.event_id),
            )
            encrypted_payload = connection.execute(
                "SELECT payload_encrypted FROM events WHERE event_id=?",
                (event.event_id,),
            ).fetchone()["payload_encrypted"]
            connection.execute(
                "UPDATE events SET payload_encrypted='corrupt' WHERE event_id=?",
                (event.event_id,),
            )
        with pytest.raises(ConflictError):
            concrete.release_task_payload(
                item,
                authorization,
                local_queue_id=queue_id,
            )
        with store.transaction() as connection:
            connection.execute(
                "UPDATE events SET payload_encrypted=? WHERE event_id=?",
                (encrypted_payload, event.event_id),
            )

        still_redacted = concrete.reconcile(after_cursor=0, limit=10)[0]
        assert still_redacted["payload"] is None
        assert "recipient-owned-payload-release" not in json.dumps(still_redacted)
        audit = store.fetch_all(
            "SELECT record_json FROM audit_log ORDER BY sequence"
        )
        assert sum(
            '"action":"supervisor.task_payload.released"' in row["record_json"]
            for row in audit
        ) == 1

        with store.transaction() as connection:
            connection.execute(
                "UPDATE task_grants SET revoked_at=? WHERE grant_id=?",
                (int(time.time()), grant.grant_id),
            )
        assert_release_denied()
        with store.transaction() as connection:
            connection.execute(
                "UPDATE task_grants SET revoked_at=NULL WHERE grant_id=?",
                (grant.grant_id,),
            )
            connection.execute(
                "DELETE FROM task_execution_intents WHERE event_id=?",
                (event.event_id,),
            )
        assert_release_denied()
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
            assert issued["assurance"] == "server_derived_account_process_parent_module"

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
        accepted_at = datetime.now(UTC).replace(microsecond=0)
        deadline = accepted_at + timedelta(hours=1)
        event = new_event(
            domain_id=sender.domain_id,
            actor=sender,
            event_type=EventType.TASK_ASSIGNMENT,
            classification=Classification.C2_RESTRICTED,
            payload={"task": f"provenance-bound-result-{sequence}"},
            idempotency_key=f"supervisor-result-provenance-{sequence:04d}",
            recipients=(recipient.harness_id,),
            task_id=f"supervisor-result-provenance-{sequence}",
            retention_delete_at=deadline,
            policy_revision=1,
        ).model_copy(update={"payload_access": "task_grant_required"})
        core.mailboxes.accept(event)
        with store.transaction() as connection:
            admission = core.assignments.conflicts.record_accepted_in_transaction(
                connection,
                event_id=event.event_id,
                domain_id=recipient.domain_id,
                recipient_harness_id=recipient.harness_id,
                sender_harness_id=sender.harness_id,
                sender_authority_id=sender.positive_authority_id,
                authority_basis="recipient_owner_approval",
                relationship_id=None,
                relationship_revision=0,
                intent=TaskExecutionIntent(
                    resources=(
                        TaskResourceIntent(
                            resource=f"dataset:provenance-result-{sequence}",
                            operation="summarize",
                            access=TaskAccessMode.READ,
                            exclusivity=TaskExclusivity.SHARED,
                        ),
                    )
                ),
                continuation={},
                deadline=deadline,
                when=accepted_at,
            )
            assert admission.fact is DeliveryFact.ACCEPTED_QUEUED
            connection.execute(
                "UPDATE recipients SET current_fact=?,updated_at=? WHERE event_id=? AND recipient_id=?",
                (
                    DeliveryFact.ACCEPTED_QUEUED.value,
                    int(accepted_at.timestamp()),
                    event.event_id,
                    recipient.harness_id,
                ),
            )
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
        result_body = {
            "authorization": authorization,
            "native_result": {
                "output": f"native-result-{sequence}",
                "terminal_event": "turn/completed",
            },
            "source_queue_id": queue_id,
        }
        if sequence == 1:
            premature = signed.request(
                "POST",
                "/v1/supervisor/executions/result",
                json_body=result_body,
            )
            assert premature.status_code == 404
        released = concrete.release_task_payload(
            item,
            BackgroundTurnAuthorization.from_mapping(authorization),
            local_queue_id=queue_id,
        )
        assert released["payload_access_authorized"] is True
        return event, authorization, result_body

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

        with store.transaction() as connection:
            connection.execute(
                "DELETE FROM task_payload_releases WHERE event_id=? AND recipient_harness_id=?",
                (event.event_id, recipient.harness_id),
            )
            cursor = int(
                connection.execute(
                    "SELECT cursor FROM recipients WHERE event_id=? AND recipient_id=?",
                    (event.event_id, recipient.harness_id),
                ).fetchone()["cursor"]
            )
        retroactive_release = signed.request(
            "POST",
            "/v1/supervisor/executions/payload-release",
            json_body={
                "authorization": result_body["authorization"],
                "cursor": cursor,
                "local_queue_id": result_body["source_queue_id"],
            },
        )
        assert retroactive_release.status_code == 404
    finally:
        signed.close()
