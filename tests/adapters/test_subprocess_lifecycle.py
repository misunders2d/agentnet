from __future__ import annotations

import json
import os
import signal
import socket
import time
from concurrent.futures import CancelledError as FutureCancelledError
from dataclasses import replace
from pathlib import Path
from typing import Any

import pytest

from agentnet.adapters.auth import (
    EphemeralBrokerEnvironment,
    PreprovisionedPrivateAuth,
)
from agentnet.adapters.native import NativeHarnessDriver, NativeTurnResult
from agentnet.adapters.specs import build_launch_spec
from agentnet.errors import AuthenticationError, AuthorizationError, GateBlocked
from agentnet.supervisor import runtime as runtime_module
from agentnet.supervisor.runtime import AdapterProcessError, BackgroundAdapterRuntime


def wait_until(predicate, *, timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.02)
    raise AssertionError("bounded lifecycle condition was not reached")


def fixture_log(state_dir: Path) -> list[dict[str, Any]]:
    path = state_dir / "native-fixture.log"
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


def test_driver_start_cancellation_propagates_and_leaves_runtime_offline(
    tmp_path: Path,
    fake_harnesses,
    monkeypatch,
) -> None:
    spec = build_launch_spec(
        "pi",
        harness_id="cancelled-driver-start",
        root=tmp_path / "runtime",
        executable=fake_harnesses["pi"],
    )

    class CancelledDriver(NativeHarnessDriver):
        stopped = False

        def start(
            self,
            command,
            *,
            environment,
            recover,
            timeout_seconds,
            inherited_fds=(),
            process_started=None,
        ) -> None:
            del command, environment, recover, timeout_seconds, inherited_fds, process_started
            raise FutureCancelledError()

        def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
            raise AssertionError("a cancelled driver never accepts a turn")

        def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]:
            raise AssertionError("a cancelled driver never becomes healthy")

        def stop(self) -> None:
            self.stopped = True

        @property
        def alive(self) -> bool:
            return False

        @property
        def pid(self) -> int | None:
            return None

    driver = CancelledDriver(spec)
    monkeypatch.setattr(runtime_module, "create_native_driver", lambda exact_spec: driver)
    runtime = BackgroundAdapterRuntime(spec, request_timeout_seconds=1)

    with pytest.raises(FutureCancelledError):
        runtime.start()

    assert driver.stopped is True
    assert runtime.status().phase == "offline"
    assert runtime.status().generation == 0


def contract_auth(foreground: Path, harness: str):
    if harness == "claude":
        values = {
            "ANTHROPIC_API_KEY": "fixture-broker-secret-claude",
            "ANTHROPIC_BASE_URL": "http://127.0.0.1:18090",
        }
    elif harness == "codex":
        values = {
            "OPENAI_API_KEY": f"fixture-broker-secret-{harness}",
            "OPENAI_BASE_URL": "http://127.0.0.1:18090",
        }
    elif harness == "pi":
        source = foreground / "pi-private-auth"
        source.mkdir(mode=0o700)
        for name, content in {
            "auth.json": '{"agentnet-broker":{"type":"api_key","key":"fixture-private"}}\n',
            "models.json": '{"providers":{"agentnet-broker":{"baseUrl":"http://127.0.0.1:18090"}}}\n',
        }.items():
            path = source / name
            path.write_text(content, encoding="utf-8")
            os.chmod(path, 0o600)
        return PreprovisionedPrivateAuth(
            "pi",
            source,
            broker_origin="http://127.0.0.1:18090",
        )
    else:
        source = foreground / "antigravity-private-auth"
        source.mkdir(mode=0o700)
        auth_file = source / "fixture-auth.json"
        auth_file.write_text('{"broker":"private-fixture"}\n', encoding="utf-8")
        os.chmod(auth_file, 0o600)
        return PreprovisionedPrivateAuth(
            "antigravity",
            source,
            broker_origin="http://127.0.0.1:18090",
        )
    return EphemeralBrokerEnvironment(harness, values)


def directory_snapshot(path: Path) -> dict[str, tuple[bytes, int]]:
    return {
        str(item.relative_to(path)): (item.read_bytes(), item.stat().st_mode & 0o777)
        for item in path.rglob("*")
        if item.is_file()
    }


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_native_contract_fixture_runs_in_private_sanitized_background_session(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
    monkeypatch,
    harness: str,
) -> None:
    monkeypatch.setenv("SECRET_CANARY", "must-not-enter-worker")
    foreground = tmp_path / "foreground-state"
    foreground.mkdir(mode=0o700)
    sentinel = foreground / "foreground-session.json"
    sentinel.write_text('{"active":"must-not-change"}\n', encoding="utf-8")
    os.chmod(sentinel, 0o600)
    auth = contract_auth(foreground, harness)
    before_foreground = directory_snapshot(foreground)
    production_spec = build_launch_spec(
        harness,
        harness_id=f"native-contract-{harness}",
        root=tmp_path / "runtime",
        executable=fake_harnesses[harness],
    )
    runtime = contract_clean_runtime_factory(
        production_spec,
        auth,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.05,
    )
    spec = runtime.spec
    output_prefix = "agy" if harness == "antigravity" else harness
    try:
        started = runtime.start()
        health = runtime.healthcheck()
        result = runtime.submit("fixture prompt", explicit=True)

        assert started.phase == "ready"
        assert health["ready"] is True
        assert result["output"] == f"{output_prefix}:fixture prompt".replace("agy:", "antigravity:")
        assert runtime.status().semantic_mode == "clean_worker"

        entries = fixture_log(spec.state_dir)
        assert entries
        assert all(entry["cwd"] == str(spec.work_dir) for entry in entries)
        assert all("SECRET_CANARY" not in entry["environment_keys"] for entry in entries)
        sensitive_names = {
            key
            for entry in entries
            for key in entry["environment_keys"]
            if any(marker in key for marker in ("TOKEN", "KEY", "SECRET"))
        }
        assert sensitive_names <= set(auth.environment_names)
        wrapper = next(entry for entry in entries if entry["kind"] == "sandbox_wrapper")
        wrapper_arguments = wrapper["value"]["argv"]
        assert wrapper_arguments[wrapper_arguments.index("--profile") + 1] == "agentnet-clean-worker-broker-v1"
        assert wrapper_arguments[wrapper_arguments.index("--broker-origin") + 1] == auth.broker_origin
        assert "--share-net" not in wrapper_arguments
        if harness == "claude":
            native = next(entry["value"] for entry in entries if entry["kind"] == "claude_input")
            assert native == {
                "message": {"content": "fixture prompt", "role": "user"},
                "parent_tool_use_id": None,
                "session_id": spec.session_id,
                "type": "user",
            }
            assert result["terminal_event"] == "result:success"
        elif harness == "codex":
            messages = [entry["value"] for entry in entries if entry["kind"] == "codex_message"]
            assert [message["method"] for message in messages] == [
                "initialize",
                "initialized",
                "thread/start",
                "turn/start",
            ]
            turn = messages[-1]
            assert turn["params"]["input"] == [{"text": "fixture prompt", "type": "text"}]
            assert turn["params"]["approvalPolicy"] == "never"
            launch = next(entry for entry in entries if entry["kind"] == "launch")
            assert f'openai_base_url="{auth.broker_origin}"' in launch["argv"]
            assert result["native_session_id"] == "fixture-codex-thread"
            assert result["terminal_event"] == "turn/completed"
        elif harness == "pi":
            commands = [entry["value"] for entry in entries if entry["kind"] == "pi_command"]
            assert [command["type"] for command in commands] == [
                "get_state",
                "get_state",
                "prompt",
                "get_last_assistant_text",
                "get_state",
            ]
            assert commands[2]["message"] == "fixture prompt"
            assert all("PI_OFFLINE" not in entry["environment_keys"] for entry in entries)
            launch = next(entry for entry in entries if entry["kind"] == "launch")
            assert "--offline" not in launch["argv"]
            assert (spec.state_dir / "pi" / "auth.json").is_file()
            assert (spec.state_dir / "pi" / "models.json").is_file()
            assert result["native_session_id"] == spec.session_id
            assert result["terminal_event"] == "agent_settled"
        else:
            native = next(entry for entry in entries if entry["kind"] == "antigravity_print")
            assert native["value"] == {"prompt": "fixture prompt"}
            assert native["argv"][-1] == "fixture prompt"
            assert native["argv"][native["argv"].index("--conversation") + 1] == spec.session_id
            assert result["terminal_event"] == "process_exit:0"

        status_text = json.dumps(runtime.content_free_status(), sort_keys=True)
        assert "fixture prompt" not in status_text
        assert "must-not-enter-worker" not in status_text
        assert "fixture-broker-secret" not in status_text
        persisted = (spec.state_dir / "runtime-state.json").read_text(encoding="utf-8")
        assert "fixture-broker-secret" not in persisted
    finally:
        runtime.stop()
    assert runtime.status().phase == "stopped"
    assert directory_snapshot(foreground) == before_foreground


def test_networkless_admission_uses_raw_bubblewrap_contract_without_shared_network(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
) -> None:
    spec = build_launch_spec(
        "claude",
        harness_id="networkless-local-model",
        root=tmp_path / "runtime",
        executable=fake_harnesses["claude"],
    )
    auth = EphemeralBrokerEnvironment(
        "claude",
        {"ANTHROPIC_API_KEY": "fixture-local-model-capability"},
    )
    runtime = contract_clean_runtime_factory(spec, auth, request_timeout_seconds=1)
    try:
        runtime.start()
        wait_until(lambda: (runtime.spec.state_dir / "native-fixture.log").is_file())
        wrapper = next(
            entry
            for entry in fixture_log(runtime.spec.state_dir)
            if entry["kind"] == "sandbox_wrapper"
        )
        arguments = wrapper["value"]["argv"]
        assert "--unshare-all" in arguments
        assert "--share-net" not in arguments
        assert "--broker-origin" not in arguments
    finally:
        runtime.stop()


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_production_launch_spec_cannot_send_semantic_content(
    tmp_path: Path,
    fake_harnesses,
    harness: str,
) -> None:
    spec = build_launch_spec(
        harness,
        harness_id=f"production-gate-{harness}",
        root=tmp_path / "runtime",
        executable=fake_harnesses[harness],
    )
    runtime = BackgroundAdapterRuntime(spec, request_timeout_seconds=1)
    try:
        runtime.start()
        if harness == "pi":
            entries = fixture_log(spec.state_dir)
            assert any("PI_OFFLINE" in entry["environment_keys"] for entry in entries)
            launch = next(entry for entry in entries if entry["kind"] == "launch")
            assert "--offline" in launch["argv"]
        with pytest.raises(AuthorizationError, match="authorized trigger"):
            runtime.submit("denied content")
        with pytest.raises(GateBlocked, match="deterministic-only"):
            runtime.submit("denied content", explicit=True)
    finally:
        runtime.stop()


def test_semantic_mode_cannot_be_forged_without_signed_admission(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    production_spec = build_launch_spec(
        "codex",
        harness_id="forged-clean-worker",
        root=tmp_path / "runtime",
        executable=fake_harnesses["codex"],
    )
    forged_spec = replace(production_spec, semantic_mode="clean_worker")
    with pytest.raises(GateBlocked, match="signed clean-worker admission"):
        BackgroundAdapterRuntime(forged_spec)


@pytest.mark.parametrize("harness", ["claude", "codex", "pi"])
def test_native_persistent_process_recovers_same_private_session_after_sigkill(
    tmp_path: Path,
    fake_harnesses,
    harness: str,
) -> None:
    spec = build_launch_spec(
        harness,
        harness_id=f"restart-{harness}",
        root=tmp_path / "runtime",
        executable=fake_harnesses[harness],
    )
    runtime = BackgroundAdapterRuntime(
        spec,
        request_timeout_seconds=0.5,
        heartbeat_interval_seconds=0.03,
        max_restart_attempts=2,
    )
    try:
        first = runtime.start()
        wait_until(lambda: (spec.state_dir / "native-fixture.log").is_file())
        first_pid = runtime.pid
        assert isinstance(first_pid, int)
        os.kill(first_pid, signal.SIGKILL)
        wait_until(
            lambda: runtime.status().phase == "ready"
            and runtime.status().generation > first.generation
        )

        assert runtime.healthcheck()["ready"] is True
        assert runtime.status().restart_count == 1
        wait_until(
            lambda: len(
                [
                    entry
                    for entry in fixture_log(spec.state_dir)
                    if entry["kind"] == "launch"
                ]
            )
            >= 2
        )
        entries = fixture_log(spec.state_dir)
        launches = [entry for entry in entries if entry["kind"] == "launch"]
        assert len(launches) >= 2
        if harness == "claude":
            assert "--session-id" in launches[0]["argv"]
            assert "--resume" in launches[-1]["argv"]
            assert launches[-1]["argv"][launches[-1]["argv"].index("--resume") + 1] == spec.session_id
        elif harness == "codex":
            messages = [entry["value"] for entry in entries if entry["kind"] == "codex_message"]
            resumes = [message for message in messages if message.get("method") == "thread/resume"]
            assert resumes[-1]["params"]["threadId"] == "fixture-codex-thread"
        else:
            assert all(
                launch["argv"][launch["argv"].index("--session-id") + 1] == spec.session_id
                for launch in launches
            )

        persisted = json.loads((spec.state_dir / "runtime-state.json").read_text(encoding="utf-8"))
        assert persisted["session_id"] == spec.session_id
        assert "payload" not in persisted
    finally:
        runtime.stop()


def test_runtime_registers_mcp_parent_after_spawn_and_again_after_restart_without_fd(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="post-spawn-local-binding-codex",
        root=tmp_path / "runtime",
        executable=fake_harnesses["codex"],
        local_bindings=True,
    )
    issued_pids: list[int] = []
    bootstrap_path = Path("/tmp") / f"agentnet-mcp-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    bootstrap_generation = "runtime-test-bootstrap-generation-001"

    def issue(pid: int, session_id: str) -> dict[str, Any]:
        assert Path(f"/proc/{pid}").is_dir()
        assert session_id == spec.session_id
        issued_pids.append(pid)
        return {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": bootstrap_generation,
            "assurance": "server_derived_account_process_parent_module",
        }

    runtime = BackgroundAdapterRuntime(
        spec,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.03,
        max_restart_attempts=2,
        local_binding_issuer=issue,
    )
    try:
        first = runtime.start()
        first_pid = runtime.pid
        assert issued_pids == [first_pid]
        assert isinstance(first_pid, int)
        locator = json.loads(
            (spec.state_dir / "mcp-bootstrap-locator.json").read_text(encoding="utf-8")
        )
        assert locator == {
            "generation": bootstrap_generation,
            "schema": "agentnet.mcp.bootstrap-locator.v1",
            "socket_path": str(bootstrap_path),
        }
        assert (spec.state_dir / "mcp-bootstrap-locator.json").stat().st_mode & 0o777 == 0o600
        original_socket = bootstrap_path.lstat()
        pinned_descriptor = runtime._local_binding_socket_descriptor
        assert isinstance(pinned_descriptor, int)
        assert os.get_inheritable(pinned_descriptor) is False
        assert (os.fstat(pinned_descriptor).st_dev, os.fstat(pinned_descriptor).st_ino) == (
            original_socket.st_dev,
            original_socket.st_ino,
        )
        bootstrap_socket.close()
        bootstrap_path.unlink()
        bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        bootstrap_socket.bind(str(bootstrap_path))
        os.chmod(bootstrap_path, 0o600)
        replacement_socket = bootstrap_path.lstat()
        assert (replacement_socket.st_dev, replacement_socket.st_ino) != (
            original_socket.st_dev,
            original_socket.st_ino,
        )
        wait_until(lambda: issued_pids.count(first_pid) >= 2)
        assert runtime.status().phase == "ready"

        os.kill(first_pid, signal.SIGKILL)
        wait_until(
            lambda: runtime.status().phase == "ready"
            and runtime.status().generation > first.generation
            and issued_pids[-1] != first_pid
        )
        assert runtime.pid == issued_pids[-1]
        assert all(
            "AGENTNET_LOCAL_BINDING_FD" not in entry["environment_keys"]
            for entry in fixture_log(spec.state_dir)
            if entry["kind"] == "launch"
        )
    finally:
        pinned_descriptor = runtime._local_binding_socket_descriptor
        runtime.stop()
        if pinned_descriptor is not None:
            with pytest.raises(OSError):
                os.fstat(pinned_descriptor)
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_mcp_renewal_failure_degrades_then_recovers_or_restarts_boundedly(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="renewal-failure-local-binding-codex",
        root=tmp_path / "runtime-renewal",
        executable=fake_harnesses["codex"],
        local_bindings=True,
    )
    bootstrap_path = Path("/tmp") / f"agentnet-renew-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    available = True
    registrations: list[int] = []

    def issue(pid: int, session_id: str) -> dict[str, Any]:
        if not available:
            raise RuntimeError("simulated ordinary-extension restart")
        registrations.append(pid)
        return {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": "renewal-test-bootstrap-generation-001",
            "assurance": "server_derived_account_process_parent_module",
        }

    runtime = BackgroundAdapterRuntime(
        spec,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.1,
        max_restart_attempts=2,
        local_binding_issuer=issue,
    )
    try:
        runtime.start()
        first_pid = runtime.pid
        assert isinstance(first_pid, int)
        initial_descriptor = runtime._local_binding_socket_descriptor
        assert isinstance(initial_descriptor, int)
        os.fstat(initial_descriptor)

        bootstrap_socket.close()
        bootstrap_path.unlink()
        bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        bootstrap_socket.bind(str(bootstrap_path))
        os.chmod(bootstrap_path, 0o600)
        available = False
        wait_until(lambda: runtime.status().phase == "degraded")
        with pytest.raises(AdapterProcessError, match="offline"):
            runtime.healthcheck()
        assert runtime.pid == first_pid
        assert runtime._local_binding_socket_descriptor == initial_descriptor
        os.fstat(initial_descriptor)

        available = True
        wait_until(lambda: runtime.status().phase == "ready" and len(registrations) >= 2)
        assert runtime.pid == first_pid
        renewed_descriptor = runtime._local_binding_socket_descriptor
        assert isinstance(renewed_descriptor, int)
        assert renewed_descriptor != initial_descriptor
        with pytest.raises(OSError):
            os.fstat(initial_descriptor)
        os.fstat(renewed_descriptor)

        bootstrap_socket.close()
        bootstrap_path.unlink()
        bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        bootstrap_socket.bind(str(bootstrap_path))
        os.chmod(bootstrap_path, 0o600)
        available = False
        wait_until(lambda: runtime.status().restart_count >= 1)
        assert runtime.status().phase != "ready"
        assert runtime.status().restart_count <= 2
    finally:
        runtime.stop()
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_mcp_detection_failure_invalidates_existing_locator_and_pin(
    tmp_path: Path,
    monkeypatch,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="detection-failure-local-binding-codex",
        root=tmp_path / "runtime-detection-failure",
        executable="/unused/codex",
        local_bindings=True,
    )
    bootstrap_path = Path("/tmp") / f"agentnet-detect-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    runtime = BackgroundAdapterRuntime(spec, local_binding_issuer=lambda _pid, _session: {})
    runtime._publish_mcp_locator(
        {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": spec.session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": "detection-failure-bootstrap-generation-001",
            "assurance": "server_derived_account_process_parent_module",
        }
    )
    pinned_descriptor = runtime._local_binding_socket_descriptor
    assert isinstance(pinned_descriptor, int)
    monkeypatch.setattr(
        runtime_module,
        "detect_executable",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            RuntimeError("simulated executable detection failure")
        ),
    )
    try:
        with pytest.raises(RuntimeError, match="executable detection failure"):
            runtime.start()
        assert runtime.status().last_failure == "native_executable_detection_failed"
        assert not runtime._mcp_locator_path().exists()
        assert runtime._local_binding_socket_descriptor is None
        with pytest.raises(OSError):
            os.fstat(pinned_descriptor)
    finally:
        runtime.stop()
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_mcp_prestart_state_persistence_failure_preserves_error_and_cleans_pin(
    tmp_path: Path,
    fake_harnesses,
    monkeypatch,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="persistence-failure-local-binding-codex",
        root=tmp_path / "runtime-persistence-failure",
        executable=fake_harnesses["codex"],
        local_bindings=True,
    )
    bootstrap_path = Path("/tmp") / f"agentnet-persist-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    runtime = BackgroundAdapterRuntime(spec, local_binding_issuer=lambda _pid, _session: {})
    runtime._publish_mcp_locator(
        {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": spec.session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": "persistence-failure-bootstrap-generation-001",
            "assurance": "server_derived_account_process_parent_module",
        }
    )
    pinned_descriptor = runtime._local_binding_socket_descriptor
    assert isinstance(pinned_descriptor, int)
    monkeypatch.setattr(
        runtime,
        "_persist_content_free_state",
        lambda: (_ for _ in ()).throw(RuntimeError("simulated state persistence failure")),
    )
    try:
        with pytest.raises(AdapterProcessError, match="startup failed") as raised:
            runtime.start()
        assert isinstance(raised.value.__cause__, RuntimeError)
        assert runtime.status().last_failure == "runtime_state_persist_failed"
        assert not runtime._mcp_locator_path().exists()
        assert runtime._local_binding_socket_descriptor is None
        with pytest.raises(OSError):
            os.fstat(pinned_descriptor)
    finally:
        monkeypatch.undo()
        runtime.stop()
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_mcp_terminal_renewal_failure_invalidates_locator_and_stops_retrying(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="terminal-renewal-local-binding-codex",
        root=tmp_path / "runtime-terminal-renewal",
        executable=fake_harnesses["codex"],
        local_bindings=True,
    )
    bootstrap_path = Path("/tmp") / f"agentnet-terminal-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    available = True

    def issue(pid: int, session_id: str) -> dict[str, Any]:
        del pid
        if not available:
            raise RuntimeError("simulated terminal extension outage")
        return {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": "terminal-renewal-bootstrap-generation-001",
            "assurance": "server_derived_account_process_parent_module",
        }

    runtime = BackgroundAdapterRuntime(
        spec,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.05,
        max_restart_attempts=0,
        local_binding_issuer=issue,
    )
    try:
        runtime.start()
        pinned_descriptor = runtime._local_binding_socket_descriptor
        assert isinstance(pinned_descriptor, int)
        assert runtime._mcp_locator_path().is_file()

        bootstrap_socket.close()
        bootstrap_path.unlink()
        bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        bootstrap_socket.bind(str(bootstrap_path))
        os.chmod(bootstrap_path, 0o600)
        available = False

        wait_until(
            lambda: runtime.status().last_failure == "native_restart_budget_exhausted"
        )
        assert runtime.status().phase == "degraded"
        assert runtime._stop_event.is_set()
        assert not runtime._mcp_locator_path().exists()
        assert runtime._local_binding_socket_descriptor is None
        with pytest.raises(OSError):
            os.fstat(pinned_descriptor)
        with pytest.raises(AdapterProcessError, match="offline"):
            runtime.healthcheck()
    finally:
        runtime.stop()
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_mcp_renewal_driver_stop_failure_is_terminal_and_cleans_pin(
    tmp_path: Path,
    fake_harnesses,
    monkeypatch,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="stop-failure-local-binding-codex",
        root=tmp_path / "runtime-stop-failure",
        executable=fake_harnesses["codex"],
        local_bindings=True,
    )
    bootstrap_path = Path("/tmp") / f"agentnet-stop-failure-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    available = True

    def issue(pid: int, session_id: str) -> dict[str, Any]:
        del pid
        if not available:
            raise RuntimeError("simulated renewal outage")
        return {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": "stop-failure-bootstrap-generation-001",
            "assurance": "server_derived_account_process_parent_module",
        }

    runtime = BackgroundAdapterRuntime(
        spec,
        request_timeout_seconds=1,
        heartbeat_interval_seconds=0.05,
        max_restart_attempts=2,
        local_binding_issuer=issue,
    )
    try:
        runtime.start()
        driver = runtime._driver
        assert driver is not None
        pinned_descriptor = runtime._local_binding_socket_descriptor
        assert isinstance(pinned_descriptor, int)

        def fail_stop() -> None:
            raise RuntimeError("simulated native driver stop failure")

        monkeypatch.setattr(driver, "stop", fail_stop)
        bootstrap_socket.close()
        bootstrap_path.unlink()
        bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        bootstrap_socket.bind(str(bootstrap_path))
        os.chmod(bootstrap_path, 0o600)
        available = False

        wait_until(lambda: runtime.status().last_failure == "native_monitor_cleanup_failed")
        assert runtime.status().phase == "degraded"
        assert runtime._stop_event.is_set()
        assert not runtime._mcp_locator_path().exists()
        assert runtime._local_binding_socket_descriptor is None
        with pytest.raises(OSError):
            os.fstat(pinned_descriptor)
    finally:
        monkeypatch.undo()
        runtime.stop()
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_mcp_post_publication_start_failure_closes_pin_and_locator(
    tmp_path: Path,
    fake_harnesses,
    monkeypatch,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="post-publication-failure-codex",
        root=tmp_path / "runtime-post-publication-failure",
        executable=fake_harnesses["codex"],
        local_bindings=True,
    )
    bootstrap_path = Path("/tmp") / f"agentnet-post-publish-{os.getpid()}-{time.time_ns()}.sock"
    bootstrap_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    bootstrap_socket.bind(str(bootstrap_path))
    os.chmod(bootstrap_path, 0o600)
    published_descriptors: list[int] = []

    def issue(pid: int, session_id: str) -> dict[str, Any]:
        del pid
        return {
            "schema": "agentnet.mcp.registered-launch.v1",
            "session_id": session_id,
            "harness_id": spec.harness_id,
            "credential_id": "credential-current-epoch",
            "credential_epoch": 1,
            "expires_at": int(time.time()) + 300,
            "bootstrap_socket_path": str(bootstrap_path),
            "bootstrap_generation": "post-publication-bootstrap-generation-001",
            "assurance": "server_derived_account_process_parent_module",
        }

    startup_error = RuntimeError("simulated failure after binding publication")

    class PublishThenFailDriver(NativeHarnessDriver):
        stopped = False

        def start(
            self,
            command,
            *,
            environment,
            recover,
            timeout_seconds,
            inherited_fds=(),
            process_started=None,
        ) -> None:
            del command, environment, recover, timeout_seconds, inherited_fds
            assert process_started is not None
            process_started(os.getpid())
            raise startup_error

        def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
            raise AssertionError("failed startup never accepts a turn")

        def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]:
            raise AssertionError("failed startup never becomes healthy")

        def stop(self) -> None:
            self.stopped = True

        @property
        def alive(self) -> bool:
            return False

        @property
        def pid(self) -> int | None:
            return os.getpid()

    driver = PublishThenFailDriver(spec)
    runtime = BackgroundAdapterRuntime(
        spec,
        request_timeout_seconds=1,
        local_binding_issuer=issue,
    )

    def publish_binding(*_args: Any) -> None:
        runtime._publish_mcp_locator(issue(os.getpid(), spec.session_id))
        descriptor = runtime._local_binding_socket_descriptor
        assert isinstance(descriptor, int)
        published_descriptors.append(descriptor)

    original_clear = runtime._clear_mcp_locator
    clear_calls = 0

    def fail_cleanup_after_publication() -> None:
        nonlocal clear_calls
        clear_calls += 1
        if clear_calls > 1:
            raise AuthenticationError("simulated locator cleanup authentication failure")
        original_clear()

    monkeypatch.setattr(runtime_module, "create_native_driver", lambda exact_spec: driver)
    monkeypatch.setattr(runtime, "_activate_binding", publish_binding)
    monkeypatch.setattr(runtime, "_clear_mcp_locator", fail_cleanup_after_publication)
    try:
        with pytest.raises(AdapterProcessError, match="startup failed") as raised:
            runtime.start()
        assert raised.value.__cause__ is startup_error
        assert driver.stopped is True
        assert runtime.status().phase == "offline"
        assert runtime.status().last_failure == "native_start_cleanup_failed"
        assert not runtime._mcp_locator_path().exists()
        assert runtime._local_binding_socket_descriptor is None
        assert len(published_descriptors) == 1
        with pytest.raises(OSError):
            os.fstat(published_descriptors[0])
    finally:
        runtime.stop()
        bootstrap_socket.close()
        bootstrap_path.unlink(missing_ok=True)


def test_antigravity_native_print_failure_requires_explicit_lifecycle_restart(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
) -> None:
    foreground = tmp_path / "foreground-auth"
    foreground.mkdir(mode=0o700)
    auth = contract_auth(foreground, "antigravity")
    spec = build_launch_spec(
        "antigravity",
        harness_id="native-failure-antigravity",
        root=tmp_path / "runtime",
        executable=fake_harnesses["antigravity"],
    )
    runtime = contract_clean_runtime_factory(
        spec,
        auth,
        request_timeout_seconds=0.5,
        heartbeat_interval_seconds=0.05,
    )
    try:
        first = runtime.start()
        with pytest.raises(AdapterProcessError, match="turn failed"):
            runtime.submit("fixture:fail", explicit=True)
        assert runtime.status().phase == "offline"
        second = runtime.start()
        assert second.phase == "ready"
        assert second.generation > first.generation
        assert runtime.submit("recovered", explicit=True)["output"] == "antigravity:recovered"
    finally:
        runtime.stop()


def test_antigravity_timeout_terminates_the_one_shot_process_group(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
) -> None:
    foreground = tmp_path / "foreground-auth"
    foreground.mkdir(mode=0o700)
    runtime = contract_clean_runtime_factory(
        build_launch_spec(
            "antigravity",
            harness_id="native-timeout-antigravity",
            root=tmp_path / "runtime",
            executable=fake_harnesses["antigravity"],
        ),
        contract_auth(foreground, "antigravity"),
        request_timeout_seconds=0.1,
        heartbeat_interval_seconds=0.05,
    )
    try:
        runtime.start()
        started_at = time.monotonic()
        with pytest.raises(AdapterProcessError, match="turn failed"):
            runtime.submit("fixture:hang", explicit=True)
        assert time.monotonic() - started_at < 2.0
        assert runtime.status().phase == "offline"
    finally:
        runtime.stop()
