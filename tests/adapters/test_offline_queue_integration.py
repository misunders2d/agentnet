from __future__ import annotations

import json
import os
import threading
import time
from pathlib import Path

import pytest

from agentnet.adapters.auth import (
    EphemeralBrokerEnvironment,
    PreprovisionedPrivateAuth,
)
from agentnet.adapters.specs import build_launch_spec
from agentnet.errors import AuthorizationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor.daemon import SupervisorDaemonConfig
from agentnet.supervisor.integration import BackgroundHarnessIntegration
from agentnet.supervisor.queue import LocalQueue
from agentnet.supervisor.runtime import (
    AdapterProcessError,
    BackgroundAdapterRuntime,
    BackgroundTurnAuthorization,
)
from agentnet.supervisor.service import DeviceSupervisor


class FakeCorporateClient:
    def __init__(
        self,
        harness_id: str,
        *,
        deliver_on_watch: bool = False,
        watch_failures_before_wake: int = 0,
    ) -> None:
        self.harness_id = harness_id
        self.acknowledged: list[str] = []
        self.uploaded: list[dict[str, object]] = []
        self.watch_calls = 0
        self.reconcile_calls = 0
        self.obligation_reconcile_calls = 0
        self.obligation_counts = {
            "unread_information": 0,
            "action_required": 0,
            "awaiting_peer": 0,
            "awaiting_human": 0,
            "overdue": 0,
            "failed": 0,
        }
        self.available = not deliver_on_watch
        self.watch_failures_before_wake = watch_failures_before_wake
        self.item = {
            "cursor": 11,
            "envelope_digest": "a" * 64,
            "event": {"event_id": "corporate-task-event", "event_type": "task_assignment"},
            "fact": "accepted_queued",
            "payload": {"task": "run autonomously"},
            "payload_available": True,
        }

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool:
        self.watch_calls += 1
        if self.watch_calls <= self.watch_failures_before_wake:
            raise RuntimeError("synthetic authenticated watch disconnect")
        if after_cursor < 11:
            self.available = True
            return True
        time.sleep(min(wait_seconds, 0.01))
        return False

    def reconcile(self, *, after_cursor: int, limit: int):
        assert limit > 0
        self.reconcile_calls += 1
        return [self.item] if self.available and after_cursor < 11 else []

    def reconcile_obligations(self, *, limit: int):
        assert limit > 0
        self.obligation_reconcile_calls += 1
        return {"recipient_committed": [], "expired": []}

    def obligation_inbox(self):
        return dict(self.obligation_counts)

    def authorize_background(self, item):
        assert item == self.item
        return {
            "decision_id": "background-decision-1",
            "harness_id": self.harness_id,
            "event_id": "corporate-task-event",
            "envelope_digest": "a" * 64,
            "event_type": "task_assignment",
            "classification": "C1",
            "policy_revision": 1,
            "expires_at": int(time.time()) + 60,
            "task_grant_id": "task-grant-1",
        }

    def acknowledge_custody(self, item, authorization, *, local_queue_id: str) -> None:
        assert item == self.item
        assert local_queue_id
        self.acknowledged.append(authorization.event_id)

    def upload_result(self, result) -> None:
        self.uploaded.append(dict(result))


class BlockingCorporateClient(FakeCorporateClient):
    def __init__(self, harness_id: str) -> None:
        super().__init__(harness_id)
        self.entered = threading.Event()
        self.release = threading.Event()

    def reconcile(self, *, after_cursor: int, limit: int):
        self.entered.set()
        if not self.release.wait(timeout=3):
            raise RuntimeError("test did not release the blocked corporate poll")
        return []


class MissedWakeCorporateClient(FakeCorporateClient):
    """Makes data visible without a wake to exercise reconciliation fallback."""

    def __init__(self, harness_id: str) -> None:
        super().__init__(harness_id, deliver_on_watch=True)

    def watch(self, *, after_cursor: int, wait_seconds: float) -> bool:
        self.watch_calls += 1
        self.available = True
        time.sleep(min(wait_seconds, 0.02))
        return False


def wait_until(predicate, *, timeout_seconds: float = 3.0) -> None:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.02)
    raise AssertionError("bounded autonomous daemon condition was not reached")


def test_supervisor_daemon_config_preserves_codex_pin_and_rejects_impossible_watchdog(
    tmp_path: Path,
) -> None:
    evidence_key = P256KeyPair.generate()
    base = {
        "core_base_url": "https://agent.example",
        "audience": "urn:agentnet:corp.example:corporate-api",
        "domain_id": "corp.example",
        "harness_id": "codex-background-harness",
        "credential_id": "codex-background-credential",
        "signing_key_path": tmp_path / "signing.pem",
        "harness": "codex",
        "runtime_root": tmp_path / "runtime",
        "queue_database_path": tmp_path / "queue.sqlite3",
        "queue_key_path": tmp_path / "queue.key",
        "evidence_dir": tmp_path / "evidence",
        "trusted_evidence_keys": {"reviewer": evidence_key.public_pem},
        "auth_environment_names": ("OPENAI_API_KEY",),
    }
    config = SupervisorDaemonConfig(**base)
    assert config.codex_model == "gpt-5.6-sol"
    assert config.codex_reasoning_effort == "ultra"
    assert config.watch_wait_seconds == 5
    assert config.reconciliation_interval_seconds == 30
    with pytest.raises(ValueError, match="greater than or equal to 5"):
        SupervisorDaemonConfig(**base, poll_interval_seconds=1)
    with pytest.raises(ValueError, match="staleness ceiling"):
        SupervisorDaemonConfig(
            **base,
            request_timeout_seconds=30,
            watch_wait_seconds=0.25,
            reconciliation_interval_seconds=5,
            reconnect_initial_seconds=0.05,
            reconnect_max_seconds=0.1,
            heartbeat_interval_seconds=1,
            max_cycle_staleness_seconds=31,
        )


def test_daemon_status_tracks_first_in_flight_cycle_before_any_completion(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    harness_id = "pi-first-cycle-watchdog"
    queue = LocalQueue(
        tmp_path / "watchdog-queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "watchdog-queue.key"),
    )
    client = BlockingCorporateClient(harness_id)
    integration = BackgroundHarnessIntegration(
        DeviceSupervisor(queue),
        core_client=client,
        watch_wait_seconds=0.05,
        reconciliation_interval_seconds=1,
        reconnect_initial_seconds=0.01,
        reconnect_max_seconds=0.05,
    )
    runtime = BackgroundAdapterRuntime(
        build_launch_spec(
            "pi",
            harness_id=harness_id,
            root=tmp_path / "watchdog-runtime",
            executable=fake_harnesses["pi"],
        ),
        request_timeout_seconds=0.5,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    try:
        integration.start_daemon(harness_id)
        assert client.entered.wait(timeout=2)
        status = integration.passive_status(harness_id)["daemon"]
        assert status["running"] is True
        assert status["daemon_started_at"] is not None
        assert status["cycle_started_at"] is not None
        assert status["last_cycle_at"] is None
    finally:
        client.release.set()
        integration.close()
        queue.close()


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("envelope_digest", "A" * 64),
        ("classification", "C4"),
        ("policy_revision", True),
        ("expires_at", 1),
        ("task_grant_id", None),
    ],
)
def test_background_authorization_is_exact_even_when_directly_constructed(
    field: str,
    value,
) -> None:
    authorization = {
        "decision_id": "decision-1",
        "harness_id": "harness-1",
        "event_id": "event-1",
        "envelope_digest": "a" * 64,
        "event_type": "task_assignment",
        "classification": "C1",
        "policy_revision": 1,
        "expires_at": int(time.time()) + 60,
        "task_grant_id": "grant-1",
    }
    authorization[field] = value
    with pytest.raises(AuthorizationError, match="invalid or expired"):
        BackgroundTurnAuthorization(**authorization)


def test_offline_delivery_waits_for_explicit_pull_and_never_pushes_foreground_content(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    harness_id = "pi-offline-queue"
    queue = LocalQueue(
        tmp_path / "queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "queue.key"),
    )
    supervisor = DeviceSupervisor(queue)
    integration = BackgroundHarnessIntegration(supervisor)
    spec = build_launch_spec(
        "pi",
        harness_id=harness_id,
        root=tmp_path / "runtime",
        executable=fake_harnesses["pi"],
    )
    runtime = BackgroundAdapterRuntime(spec, request_timeout_seconds=0.5, heartbeat_interval_seconds=0.05)
    integration.register(runtime)
    event = {"event": {"event_id": "offline-event-1"}, "payload": {"secret": "queued-canary"}}
    try:
        integration.receive_from_core(harness_id=harness_id, event=event, cursor=7)
        status = integration.passive_status(harness_id)
        assert status["activity"] == {"kind": "agentnet_count", "count": 1}
        assert "queued-canary" not in json.dumps(status, sort_keys=True)
        with pytest.raises(AdapterProcessError, match="offline"):
            integration.explicit_pull(harness_id)

        integration.start(harness_id)
        opened = integration.explicit_pull(harness_id)
        assert opened == [
            {
                "queue_id": opened[0]["queue_id"],
                "disposition": "explicit_human_open",
                "payload": event,
            }
        ]
        assert integration.passive_status(harness_id)["activity"]["count"] == 0
        assert queue.cursor(harness_id) == 7
    finally:
        integration.close()
        queue.close()


def test_clean_worker_native_result_is_durable_before_input_ack(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
) -> None:
    harness_id = "codex-native-durable"
    queue = LocalQueue(
        tmp_path / "queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "queue.key"),
    )
    supervisor = DeviceSupervisor(queue)
    integration = BackgroundHarnessIntegration(supervisor)
    production_spec = build_launch_spec(
        "codex",
        harness_id=harness_id,
        root=tmp_path / "runtime",
        executable=fake_harnesses["codex"],
    )
    auth = EphemeralBrokerEnvironment(
        "codex",
        {
            "OPENAI_API_KEY": "fixture-broker-secret",
            "OPENAI_BASE_URL": "http://127.0.0.1:18090",
        },
    )
    runtime = contract_clean_runtime_factory(
        production_spec,
        auth,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    event = {"event": {"event_id": "native-event-1"}, "payload": {"task": "fixture"}}
    try:
        received = integration.receive_from_core(harness_id=harness_id, event=event, cursor=9)
        integration.start(harness_id)
        result = integration.explicit_pull(harness_id)

        assert result == [
            {
                "queue_id": received["queue_id"],
                "disposition": "clean_worker_response",
                "output_queue_id": result[0]["output_queue_id"],
            }
        ]
        assert queue.content_free_counts(harness_id) == {}
        output = queue.claim(harness_id=harness_id, direction="outbox")
        assert output[0]["queue_id"] == result[0]["output_queue_id"]
        assert output[0]["payload"]["source_queue_id"] == received["queue_id"]
        native_result = output[0]["payload"]["native_result"]
        assert native_result["native_session_id"] == "fixture-codex-thread"
        assert native_result["terminal_event"] == "turn/completed"
        assert native_result["output"].startswith("codex:")
        assert integration.explicit_pull(harness_id) == []
        assert queue.cursor(harness_id) == 9
    finally:
        integration.close()
        queue.close()


def test_cursor_does_not_advance_when_local_durable_enqueue_crashes(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    harness_id = "pi-cursor-crash"
    queue = LocalQueue(
        tmp_path / "cursor-crash.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "cursor-crash.key"),
    )
    supervisor = DeviceSupervisor(queue)
    client = FakeCorporateClient(harness_id)
    integration = BackgroundHarnessIntegration(supervisor, core_client=client)
    runtime = BackgroundAdapterRuntime(
        build_launch_spec(
            "pi",
            harness_id=harness_id,
            root=tmp_path / "cursor-crash-runtime",
            executable=fake_harnesses["pi"],
        ),
        request_timeout_seconds=0.5,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    original_receive = supervisor.receive_from_core
    try:
        integration.start(harness_id)

        def crash_before_durable_enqueue(**_kwargs):
            raise RuntimeError("synthetic local custody crash")

        supervisor.receive_from_core = crash_before_durable_enqueue  # type: ignore[method-assign]
        with pytest.raises(RuntimeError, match="custody crash"):
            integration.run_once(harness_id)
        assert queue.cursor(harness_id) == 0
        assert queue.content_free_counts(harness_id) == {}

        supervisor.receive_from_core = original_receive  # type: ignore[method-assign]
        recovered = integration.run_once(harness_id)
        assert recovered == {
            "fetched": 1,
            "enqueued": 1,
            "dispatched": 0,
            "uploaded": 0,
            "obligations_reconciled": 0,
        }
        assert queue.cursor(harness_id) == 11
        assert queue.content_free_counts(harness_id) == {"queued": 1}
    finally:
        integration.close()
        queue.close()


def test_obligation_attention_is_automatically_reconciled_and_survives_restart(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    harness_id = "pi-durable-obligation-attention"
    database = tmp_path / "obligation-attention.sqlite3"
    key_file = tmp_path / "obligation-attention.key"
    queue = LocalQueue(database, LocalEnvelopeCipher.from_key_file(key_file))
    client = FakeCorporateClient(harness_id)
    client.available = False
    client.obligation_counts["action_required"] = 2
    client.obligation_counts["overdue"] = 1
    integration = BackgroundHarnessIntegration(DeviceSupervisor(queue), core_client=client)
    runtime = BackgroundAdapterRuntime(
        build_launch_spec(
            "pi",
            harness_id=harness_id,
            root=tmp_path / "obligation-attention-runtime",
            executable=fake_harnesses["pi"],
        ),
        request_timeout_seconds=0.5,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    try:
        integration.start(harness_id)
        assert integration.run_once(harness_id)["obligations_reconciled"] == 0
        status = integration.passive_status(harness_id)
        assert status["obligations"]["action_required"] == 2
        assert status["obligations"]["overdue"] == 1
        # The overdue item is already represented in action_required.
        assert status["activity"] == {"kind": "agentnet_count", "count": 2}
        assert client.obligation_reconcile_calls == 1
    finally:
        integration.close()
        queue.close()

    reopened = LocalQueue(database, LocalEnvelopeCipher.from_key_file(key_file))
    try:
        assert reopened.obligation_snapshot(harness_id)["action_required"] == 2
    finally:
        reopened.close()


def test_low_frequency_reconciliation_recovers_a_missed_authority_free_wake(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    harness_id = "pi-missed-wake-reconciliation"
    queue = LocalQueue(
        tmp_path / "missed-wake.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "missed-wake.key"),
    )
    client = MissedWakeCorporateClient(harness_id)
    integration = BackgroundHarnessIntegration(
        DeviceSupervisor(queue),
        core_client=client,
        watch_wait_seconds=0.05,
        reconciliation_interval_seconds=0.15,
        reconnect_initial_seconds=0.01,
        reconnect_max_seconds=0.05,
    )
    runtime = BackgroundAdapterRuntime(
        build_launch_spec(
            "pi",
            harness_id=harness_id,
            root=tmp_path / "missed-wake-runtime",
            executable=fake_harnesses["pi"],
        ),
        request_timeout_seconds=0.5,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    try:
        integration.start_daemon(harness_id)
        wait_until(lambda: queue.cursor(harness_id) == 11)
        assert client.watch_calls >= 2
        assert client.reconcile_calls == 2
        assert queue.content_free_counts(harness_id) == {"queued": 1}
    finally:
        integration.close()
        queue.close()


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_autonomous_daemon_reconciles_eligible_custody_dispatches_and_uploads_without_explicit_open(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
    harness: str,
) -> None:
    harness_id = f"{harness}-autonomous-corporate"
    queue = LocalQueue(
        tmp_path / "daemon-queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "daemon-queue.key"),
    )
    client = FakeCorporateClient(harness_id)
    integration = BackgroundHarnessIntegration(DeviceSupervisor(queue), core_client=client)
    production_spec = build_launch_spec(
        harness,
        harness_id=harness_id,
        root=tmp_path / "daemon-runtime",
        executable=fake_harnesses[harness],
    )
    if harness in {"claude", "codex"}:
        key_name = "ANTHROPIC_API_KEY" if harness == "claude" else "OPENAI_API_KEY"
        url_name = "ANTHROPIC_BASE_URL" if harness == "claude" else "OPENAI_BASE_URL"
        auth = EphemeralBrokerEnvironment(
            harness,
            {
                key_name: f"fixture-daemon-broker-secret-{harness}",
                url_name: "http://127.0.0.1:18090",
            },
        )
    else:
        source = tmp_path / f"{harness}-private-auth"
        source.mkdir(mode=0o700)
        auth_file = source / "auth.json"
        auth_file.write_text('{"fixture":"private-broker"}\n', encoding="utf-8")
        os.chmod(auth_file, 0o600)
        auth = PreprovisionedPrivateAuth(
            harness,
            source,
            broker_origin="http://127.0.0.1:18090",
        )
    runtime = contract_clean_runtime_factory(
        production_spec,
        auth,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    try:
        integration.start(harness_id)
        result = integration.run_once(harness_id)
        assert result == {
            "fetched": 1,
            "enqueued": 1,
            "dispatched": 1,
            "uploaded": 1,
            "obligations_reconciled": 0,
        }
        assert client.acknowledged == ["corporate-task-event"]
        assert len(client.uploaded) == 1
        assert client.uploaded[0]["authorization"]["task_grant_id"] == "task-grant-1"
        assert client.uploaded[0]["native_result"]["terminal_event"] == {
            "claude": "result:success",
            "codex": "turn/completed",
            "pi": "agent_settled",
            "antigravity": "process_exit:0",
        }[harness]
        assert queue.cursor(harness_id) == 11
        assert queue.content_free_counts(harness_id) == {}
        assert integration.run_once(harness_id) == {
            "fetched": 0,
            "enqueued": 0,
            "dispatched": 0,
            "uploaded": 0,
            "obligations_reconciled": 0,
        }
        assert "run autonomously" not in json.dumps(
            integration.passive_status(harness_id), sort_keys=True
        )
    finally:
        integration.close()
        queue.close()


def test_started_daemon_runs_and_stops_without_foreground_or_unmonitored_thread(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
) -> None:
    harness_id = "claude-threaded-autonomous"
    queue = LocalQueue(
        tmp_path / "threaded-queue.sqlite3",
        LocalEnvelopeCipher.from_key_file(tmp_path / "threaded-queue.key"),
    )
    client = FakeCorporateClient(
        harness_id,
        deliver_on_watch=True,
        watch_failures_before_wake=2,
    )
    integration = BackgroundHarnessIntegration(
        DeviceSupervisor(queue),
        core_client=client,
        watch_wait_seconds=0.05,
        reconciliation_interval_seconds=1,
        reconnect_initial_seconds=0.01,
        reconnect_max_seconds=0.05,
    )
    runtime = contract_clean_runtime_factory(
        build_launch_spec(
            "claude",
            harness_id=harness_id,
            root=tmp_path / "threaded-runtime",
            executable=fake_harnesses["claude"],
        ),
        EphemeralBrokerEnvironment(
            "claude",
            {
                "ANTHROPIC_API_KEY": "fixture-threaded-broker-secret",
                "ANTHROPIC_BASE_URL": "http://127.0.0.1:18090",
            },
        ),
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.05,
    )
    integration.register(runtime)
    try:
        started = integration.start_daemon(harness_id)
        assert started["daemon"] == "running"
        wait_until(lambda: len(client.uploaded) == 1)
        status = integration.passive_status(harness_id)
        assert status["daemon"]["running"] is True
        assert status["daemon"]["errors"] == 0
        assert status["daemon"]["delivery_mode"] == (
            "authenticated_watch_with_cursor_reconciliation"
        )
        assert client.watch_calls >= 3
        assert client.reconcile_calls == 2
        assert "run autonomously" not in json.dumps(status, sort_keys=True)
        integration.stop(harness_id)
        stopped = integration.passive_status(harness_id)
        assert stopped["daemon"]["running"] is False
        assert stopped["runtime"]["phase"] == "stopped"
    finally:
        integration.close()
        queue.close()
