from __future__ import annotations

import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest


ROOT = Path(__file__).resolve().parents[2]
PROBE_PATH = ROOT / "scripts/ci/verify_released_setup_runtime.py"
SPEC = importlib.util.spec_from_file_location("released_setup_runtime_probe", PROBE_PATH)
assert SPEC is not None and SPEC.loader is not None
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


def _evidence() -> dict[str, Any]:
    return {
        "schema": "agentnet.server-setup.evidence.v1",
        "status": "blocked",
        "blocker": "service_runtime",
        "message": "managed AgentNet service process does not run the approved hermetic runtime",
        "authority_granted": False,
        "identity_enrolled": False,
        "production_durability_proven": False,
    }


def _write(path: Path, value: object) -> None:
    path.write_text(json.dumps(value), encoding="utf-8")


def test_classifier_accepts_only_exact_exit_one_refusal(tmp_path: Path) -> None:
    evidence = tmp_path / "evidence.json"
    _write(evidence, _evidence())
    probe.classify_transient_refusal(1, evidence)

    with pytest.raises(probe.ProbeError):
        probe.classify_transient_refusal(2, evidence)

    for key, replacement in (
        ("schema", "wrong"),
        ("status", "failed"),
        ("blocker", "systemd_start"),
        ("message", "wrong"),
        ("authority_granted", True),
        ("identity_enrolled", True),
        ("production_durability_proven", True),
    ):
        mutated = _evidence()
        mutated[key] = replacement
        _write(evidence, mutated)
        with pytest.raises(probe.ProbeError):
            probe.classify_transient_refusal(1, evidence)

    for key in (
        "authority_granted",
        "identity_enrolled",
        "production_durability_proven",
    ):
        for false_lookalike in (0, 0.0):
            mutated = _evidence()
            mutated[key] = false_lookalike
            _write(evidence, mutated)
            with pytest.raises(probe.ProbeError):
                probe.classify_transient_refusal(1, evidence)

    for missing in _evidence():
        mutated = _evidence()
        mutated.pop(missing)
        _write(evidence, mutated)
        with pytest.raises(probe.ProbeError):
            probe.classify_transient_refusal(1, evidence)


def test_classifier_rejects_non_strict_json(tmp_path: Path) -> None:
    evidence = tmp_path / "evidence.json"
    invalid_payloads = (
        b'{"schema":"agentnet.server-setup.evidence.v1","schema":"agentnet.server-setup.evidence.v1"}',
        b'{"value":NaN}',
        b'{} {}',
        b'[]',
        b'',
    )
    for payload in invalid_payloads:
        evidence.write_bytes(payload)
        with pytest.raises(probe.ProbeError):
            probe.classify_transient_refusal(1, evidence)


class FakeServerSetupError(RuntimeError):
    def __init__(self, blocker: str) -> None:
        super().__init__(blocker)
        self.blocker = blocker


def _fake_setup() -> SimpleNamespace:
    return SimpleNamespace(ServerSetupError=FakeServerSetupError)


def test_runtime_wait_accepts_only_transient_then_exact_success(tmp_path: Path) -> None:
    calls = 0
    sleeps: list[float] = []

    def validate(_setup: object, _prefix: Path) -> None:
        nonlocal calls
        calls += 1
        if calls == 1:
            raise FakeServerSetupError("service_runtime")

    probe.wait_for_runtime(
        _fake_setup(),
        tmp_path,
        attempts=2,
        interval_seconds=0.25,
        validate=validate,
        sleep=sleeps.append,
    )
    assert calls == 2
    assert sleeps == [0.25]


def test_runtime_wait_fails_on_stable_or_unexpected_failure(tmp_path: Path) -> None:
    def stable(_setup: object, _prefix: Path) -> None:
        raise FakeServerSetupError("service_runtime")

    with pytest.raises(probe.ProbeError, match="did not converge"):
        probe.wait_for_runtime(
            _fake_setup(),
            tmp_path,
            attempts=2,
            validate=stable,
            sleep=lambda _seconds: None,
        )

    calls = 0

    def wrong(_setup: object, _prefix: Path) -> None:
        nonlocal calls
        calls += 1
        raise FakeServerSetupError("systemd_start")

    with pytest.raises(probe.ProbeError, match="unexpected blocker"):
        probe.wait_for_runtime(
            _fake_setup(),
            tmp_path,
            attempts=5,
            validate=wrong,
            sleep=lambda _seconds: None,
        )
    assert calls == 1


def test_exact_validator_constructs_released_approval_core_and_health_proof(
    tmp_path: Path,
) -> None:
    calls: list[dict[str, Any]] = []
    health: list[tuple[str, dict[str, Any], int]] = []
    setup = SimpleNamespace(
        SetupLayout=lambda root: ("layout", root),
        APPROVAL_UNIT="agentnet-approval.service",
        APPROVAL_USER="agentnet-approval",
        APPROVAL_DATA=Path("/var/lib/agentnet-approval"),
        APPROVAL_CONFIG=Path("/var/lib/agentnet-approval/config.json"),
        APPROVAL_PORT=8090,
        CORE_UNIT="agentnet-core.service",
        CORE_USER="agentnet",
        CORE_DATA=Path("/var/lib/agentnet"),
        CORE_CONFIG=Path("/var/lib/agentnet/agentnet.json"),
        CORE_PORT=8080,
        _START_HEALTH_ATTEMPTS=17,
        _validate_systemd_service_runtime=lambda executable, **kwargs: calls.append(
            {"executable": executable, **kwargs}
        ),
        _health=lambda url, *, expected, attempts: health.append((url, expected, attempts)),
    )

    probe.validate_runtime_and_health(setup, tmp_path)

    assert [call["unit"] for call in calls] == [
        "agentnet-approval.service",
        "agentnet-core.service",
    ]
    assert all(call["executable"] == Path("/usr/bin/systemctl") for call in calls)
    assert calls[0]["expected_argv"] == (
        str((tmp_path / "bin/node").resolve()),
        str((tmp_path / "lib/node_modules/@misunders2d/agentnet/npm/bin/agentnet.mjs").resolve()),
        "approval",
        "serve",
        "--config",
        "/var/lib/agentnet-approval/config.json",
        "--host",
        "127.0.0.1",
        "--port",
        "8090",
    )
    assert calls[1]["expected_argv"][2:] == (
        "serve",
        "--config",
        "/var/lib/agentnet/agentnet.json",
        "--host",
        "127.0.0.1",
        "--port",
        "8080",
    )
    assert [url for url, _expected, _attempts in health] == [
        "http://127.0.0.1:8090/healthz",
        "http://127.0.0.1:8080/healthz",
        "https://approval.agentnet.test/healthz",
        "https://core.agentnet.test/healthz",
    ]
    assert all(attempts == 17 for _url, _expected, attempts in health)
    assert health[0][1]["version"] == "0.1.31"
    assert health[1][1]["server_agent_capabilities"] == ["offline_custody"]
