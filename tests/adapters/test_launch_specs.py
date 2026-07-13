from __future__ import annotations

import json
from pathlib import Path

import pytest

from agentnet.adapters.auth import EphemeralBrokerEnvironment
from agentnet.adapters.specs import (
    PINNED_VERSIONS,
    build_launch_spec,
    detect_executable,
    detect_installed_harnesses,
)
from agentnet.errors import ValidationError


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_launch_specs_are_pinned_private_and_foreground_free(tmp_path: Path, fake_harnesses, harness: str) -> None:
    spec = build_launch_spec(
        harness,
        harness_id=f"enrolled-{harness}-device",
        root=tmp_path / "runtime",
        executable=fake_harnesses[harness],
    )

    spec.validate()
    assert spec.pinned_version == PINNED_VERSIONS[harness]
    assert spec.semantic_mode == "deterministic_only"
    assert spec.foreground_session_id is None
    assert spec.home_dir != spec.work_dir != spec.state_dir
    assert all(path.is_dir() for path in (spec.home_dir, spec.work_dir, spec.state_dir, spec.temp_dir))
    assert not {
        "--continue",
        "--resume",
        "--dangerously-skip-permissions",
        "--remote-control",
        "--api-key",
    }.intersection(spec.arguments)
    probe = detect_executable(spec)
    assert probe.matches_pin is True
    assert probe.evidence_scope == "local_detection_only"
    assert probe.external_conformance_proven is False


def test_each_native_command_uses_its_dedicated_background_contract(tmp_path: Path, fake_harnesses) -> None:
    specs = {
        harness: build_launch_spec(
            harness,
            harness_id=f"harness-{harness}",
            root=tmp_path / "runtime",
            executable=fake_harnesses[harness],
        )
        for harness in PINNED_VERSIONS
    }

    assert specs["claude"].transport == "claude_stream_json"
    assert ("--input-format", "stream-json") == specs["claude"].arguments[1:3]
    assert "--session-id" in specs["claude"].arguments
    assert specs["codex"].transport == "codex_app_server"
    assert specs["codex"].arguments[:2] == ("app-server", "--stdio")
    assert specs["codex"].model == "gpt-5.6-sol"
    assert specs["codex"].reasoning_effort == "ultra"
    assert 'model="gpt-5.6-sol"' in specs["codex"].arguments
    assert 'model_reasoning_effort="ultra"' in specs["codex"].arguments
    assert specs["pi"].transport == "pi_rpc_jsonl"
    assert specs["pi"].arguments[:2] == ("--mode", "rpc")
    assert "--session-dir" in specs["pi"].arguments
    assert "--offline" in specs["pi"].arguments
    assert specs["antigravity"].transport == "antigravity_print"
    assert specs["antigravity"].persistent_process is False
    assert "--conversation" in specs["antigravity"].arguments


@pytest.mark.parametrize("harness", ["claude", "codex", "pi"])
def test_approved_local_binding_is_exact_and_pi_never_receives_mcp(
    tmp_path: Path,
    fake_harnesses,
    harness: str,
) -> None:
    spec = build_launch_spec(
        harness,
        harness_id=f"local-binding-{harness}",
        root=tmp_path / "runtime",
        executable=fake_harnesses[harness],
        local_bindings=True,
    )

    assert spec.local_binding_enabled is True
    if harness == "claude":
        config_path = Path(spec.arguments[spec.arguments.index("--mcp-config") + 1])
        configured = json.loads(config_path.read_text(encoding="utf-8"))["mcpServers"]
        assert configured["agentnet"]["args"] == [
            "-m",
            "agentnet.bindings.mcp_proxy",
        ]
    elif harness == "codex":
        serialized = "\n".join(spec.arguments)
        assert "mcp_servers.agentnet.command" in serialized
        assert "mcp_servers.agentnet.required=true" in serialized
        assert 'model="gpt-5.6-sol"' in spec.arguments
        assert 'model_reasoning_effort="ultra"' in spec.arguments
    else:
        assert "--extension" in spec.arguments
        assert "--no-builtin-tools" in spec.arguments
        assert "--no-tools" not in spec.arguments
        assert not any("mcp" in argument.casefold() for argument in spec.arguments)


@pytest.mark.external
def test_installed_binary_live_version_probe_is_observation_not_inference_proof(tmp_path: Path) -> None:
    probes = detect_installed_harnesses(tmp_path / "real-probes")

    assert set(probes) == {"claude", "codex", "pi", "antigravity"}
    for probe in probes.values():
        assert probe.evidence_scope == "local_detection_only"
        assert probe.external_conformance_proven is False
        assert isinstance(probe.matches_pin, bool)
        if probe.matches_pin:
            assert probe.reported_version == probe.pinned_version


def test_ephemeral_auth_is_explicit_redacted_and_strictly_per_harness() -> None:
    auth = EphemeralBrokerEnvironment(
        "codex",
        {
            "OPENAI_API_KEY": "broker-secret-canary",
            "OPENAI_BASE_URL": "http://127.0.0.1:18090",
        },
    )
    assert "broker-secret-canary" not in repr(auth)
    assert auth.environment_names == ("OPENAI_API_KEY", "OPENAI_BASE_URL")
    with pytest.raises(ValidationError, match="crossed"):
        auth.environment_for("pi")
    with pytest.raises(ValidationError, match="allowlisted"):
        EphemeralBrokerEnvironment("codex", {"AWS_SECRET_ACCESS_KEY": "ambient"})
    with pytest.raises(ValidationError, match="loopback"):
        EphemeralBrokerEnvironment(
            "codex",
            {"OPENAI_API_KEY": "broker", "OPENAI_BASE_URL": "http://broker.example"},
        )
