from __future__ import annotations

from pathlib import Path

import pytest

from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor import daemon
from agentnet.supervisor.daemon import SupervisorDaemonConfig, run_c0_pilot_responder_daemon
from agentnet.supervisor.integration import BackgroundHarnessIntegration


class NoWorkerSupervisor:
    def recover(self):
        return 0


class ExactC0Client:
    def __init__(self) -> None:
        self.statuses = ["waiting_owner", "waiting_fresh"]
        self.calls: list[str] = []

    def c0_pilot_status(self):
        self.calls.append("c0_status")
        return {"schema": "agentnet.c0-pilot.result.v1", "status": self.statuses.pop(0)}

    def c0_pilot_respond(self):
        self.calls.append("c0_respond")
        return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_fresh"}

    def __getattr__(self, name):
        raise AssertionError(f"forbidden generic supervisor path invoked: {name}")


def test_c0_responder_daemon_never_constructs_semantic_worker_subsystems(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    calls: list[str] = []

    class FakeClient:
        def __init__(self, **_kwargs) -> None:
            calls.append("signed_client")

        def close(self) -> None:
            calls.append("close")

    class FakeCoreClient:
        def __init__(self, _client) -> None:
            calls.append("core_client")

        def c0_pilot_status(self):
            calls.append("c0_status")
            return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_owner"}

        def c0_pilot_respond(self):
            calls.append("c0_respond")
            return {"schema": "agentnet.c0-pilot.result.v1", "status": "waiting_fresh"}

    def forbidden(*_args, **_kwargs):
        raise AssertionError("semantic worker subsystem was constructed")

    monkeypatch.setattr(daemon, "_owner_private_key", lambda _path: object())
    monkeypatch.setattr(daemon, "AgentNetClient", FakeClient)
    monkeypatch.setattr(daemon, "AgentNetSupervisorCoreClient", FakeCoreClient)
    for name in (
        "CleanWorkerLauncher",
        "LocalQueue",
        "DeviceSupervisor",
        "BackgroundHarnessIntegration",
        "build_launch_spec",
        "PreprovisionedPrivateAuth",
        "EphemeralBrokerEnvironment",
    ):
        monkeypatch.setattr(daemon, name, forbidden)

    evidence_key = P256KeyPair.generate()
    config = SupervisorDaemonConfig.model_validate(
        {
            "schema_version": "1.0",
            "core_base_url": "https://agentnet.example",
            "audience": "https://agentnet.example",
            "domain_id": "corp.example",
            "harness_id": "owner-harness",
            "credential_id": "owner-credential",
            "signing_key_path": str(tmp_path / "signing.pem"),
            "harness": "pi",
            "runtime_root": str(tmp_path / "runtime"),
            "queue_database_path": str(tmp_path / "queue.sqlite3"),
            "queue_key_path": str(tmp_path / "queue.key"),
            "evidence_dir": str(tmp_path / "evidence"),
            "trusted_evidence_keys": {
                evidence_key.thumbprint: evidence_key.public_pem
            },
            "private_auth_source": str(tmp_path / "worker-auth.json"),
        }
    )

    assert run_c0_pilot_responder_daemon(config) == {
        "schema": "agentnet.c0-pilot-responder.exit.v1",
        "status": "waiting_fresh",
        "stopped": True,
    }
    assert calls == [
        "signed_client", "core_client", "c0_status", "c0_respond", "close"
    ]


def test_c0_responder_cycle_calls_only_status_and_fixed_response() -> None:
    client = ExactC0Client()
    integration = BackgroundHarnessIntegration(
        NoWorkerSupervisor(),
        core_client=client,
        watch_wait_seconds=0.05,
        reconciliation_interval_seconds=0.05,
    )

    assert integration.run_c0_pilot_responder_once()["status"] == "waiting_fresh"
    assert client.calls == ["c0_status", "c0_respond"]
    assert integration.run_c0_pilot_responder_once()["status"] == "waiting_fresh"
    assert client.calls == ["c0_status", "c0_respond", "c0_status"]
    assert integration._runtimes == {}
