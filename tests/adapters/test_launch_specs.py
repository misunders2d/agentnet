from __future__ import annotations

from dataclasses import replace
import json
from pathlib import Path

import pytest

from agentnet.adapters.auth import EphemeralBrokerEnvironment
from agentnet.adapters.specs import (
    PINNED_VERSIONS,
    EndpointAdapterLaunchSpec,
    build_launch_spec,
    detect_executable,
    detect_installed_harnesses,
)
from agentnet.adapters.catalog import BUILTIN_ADAPTERS
from agentnet.bindings.endpoint import EndpointBinding
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES
from agentnet.core.capabilities import (
    ENDPOINT_CAPABILITY_ROOT_BYTES,
    endpoint_capability_root_name,
)
from agentnet.errors import ValidationError
from agentnet.bindings.mcp_bootstrap import UnixMCPBootstrapServer
from agentnet.bindings.windows_mcp_bootstrap import WindowsMCPBootstrapServer

def _fake_executable(tmp_path: Path, fake_harnesses: dict[str, str], harness: str) -> str:
    if harness != "omp":
        return fake_harnesses[harness]
    executable = tmp_path / "fake-bin" / "omp"
    executable.write_text('#!/usr/bin/env python3\nprint("omp/17.2.9")\n', encoding="utf-8")
    executable.chmod(0o700)
    return str(executable)


def _endpoint_binding(
    tmp_path: Path,
    harness: str,
    *,
    harness_id: str | None = None,
    generation: int = 7,
) -> EndpointBinding:
    exact_harness_id = harness_id or f"endpoint-{harness}"
    endpoint_scope = endpoint_capability_root_name(
        domain_id="domain-exact-launch",
        harness_id=exact_harness_id,
        adapter_generation=generation,
    )
    capability_base = tmp_path / "capabilities"
    endpoints = capability_base / "endpoints"
    capability_directory = endpoints / endpoint_scope
    for directory in (capability_base, endpoints, capability_directory):
        directory.mkdir(exist_ok=True, mode=0o700)
        directory.chmod(0o700)
    capability_root = capability_directory / "capability-root.key"
    capability_root.write_bytes(bytes([generation]) * ENDPOINT_CAPABILITY_ROOT_BYTES)
    capability_root.chmod(0o600)
    return EndpointBinding(
        domain_id="domain-exact-launch",
        principal_id="principal-exact-launch",
        harness_id=exact_harness_id,
        harness_kind=harness,
        credential_id=f"credential-{harness}",
        credential_epoch=3,
        adapter_generation=generation,
        mailbox_cursor=11,
        profile_key=f"{harness}:profile",
        capability_root_path=capability_root,
        process_measurement="sha256:" + "a" * 64,
    )


@pytest.mark.parametrize("harness", ["omp", "pi", "claude", "codex", "antigravity"])
def test_launch_specs_are_pinned_private_and_foreground_free(tmp_path: Path, fake_harnesses, harness: str) -> None:
    spec = build_launch_spec(
        harness,
        harness_id=f"enrolled-{harness}-device",
        root=tmp_path / "runtime",
        executable=_fake_executable(tmp_path, fake_harnesses, harness),
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
            executable=_fake_executable(tmp_path, fake_harnesses, harness),
        )
        for harness in PINNED_VERSIONS
    }

    assert specs["omp"].transport == "omp_rpc_jsonl"
    assert specs["omp"].arguments[:2] == ("--mode", "rpc")
    assert "--session-dir" in specs["omp"].arguments
    assert "--no-tools" in specs["omp"].arguments

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


@pytest.mark.parametrize("harness", ["omp", "pi", "claude", "codex", "antigravity"])
def test_approved_local_binding_is_exact_and_exposes_complete_surface(
    tmp_path: Path,
    fake_harnesses,
    harness: str,
) -> None:
    harness_id = f"local-binding-{harness}"
    binding = _endpoint_binding(tmp_path, harness, harness_id=harness_id)
    spec = build_launch_spec(
        harness,
        harness_id=harness_id,
        root=tmp_path / "runtime",
        executable=_fake_executable(tmp_path, fake_harnesses, harness),
        local_bindings=True,
        endpoint_binding=binding,
    )

    assert isinstance(spec, EndpointAdapterLaunchSpec)
    assert spec.local_binding_enabled is True
    assert spec.canonical_tool_names == CANONICAL_TOOL_NAMES
    assert spec.adapter_generation == binding.adapter_generation
    assert spec.credential_epoch == binding.credential_epoch
    assert spec.capability_root_path == binding.capability_root_path
    assert spec.endpoint_descriptor_path is not None
    assert spec.endpoint_descriptor_path.stat().st_mode & 0o777 == 0o600
    descriptor = json.loads(spec.endpoint_descriptor_path.read_text(encoding="utf-8"))
    assert descriptor == {
        "adapter_generation": 7,
        "capability_root_path": str(binding.capability_root_path),
        "credential_epoch": 3,
        "credential_id": f"credential-{harness}",
        "domain_id": "domain-exact-launch",
        "harness_id": harness_id,
        "harness_kind": harness,
        "mailbox_cursor": 11,
        "principal_id": "principal-exact-launch",
        "process_measurement": "sha256:" + "a" * 64,
        "profile_key": f"{harness}:profile",
        "refresh_behavior": "restart_required",
        "schema": "agentnet.endpoint-launch-descriptor.v1",
    }
    serialized_tools = ",".join(name.replace(".", "_") for name in CANONICAL_TOOL_NAMES)
    if harness == "omp":
        assert spec.arguments[:2] == ("--profile", "omp:profile")
        assert "--extension" in spec.arguments
        assert "--no-builtin-tools" not in spec.arguments
        assert spec.arguments[spec.arguments.index("--tools") + 1] == serialized_tools
        assert not any("mcp" in argument.casefold() for argument in spec.arguments)
    elif harness == "pi":
        assert "--extension" in spec.arguments
        assert "--no-builtin-tools" in spec.arguments
        assert "--no-tools" not in spec.arguments
        assert spec.arguments[spec.arguments.index("--tools") + 1] == serialized_tools
        assert not any("mcp" in argument.casefold() for argument in spec.arguments)
    elif harness == "claude":
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
        assert all(name.replace(".", "_") in serialized for name in CANONICAL_TOOL_NAMES)
        assert 'model="gpt-5.6-sol"' in spec.arguments
        assert 'model_reasoning_effort="ultra"' in spec.arguments
    else:
        configured = json.loads(
            (spec.home_dir / ".gemini" / "settings.json").read_text(encoding="utf-8")
        )["mcpServers"]
        assert configured["agentnet"]["args"] == [
            "-m",
            "agentnet.bindings.mcp_proxy",
        ]


def test_local_binding_rejects_missing_mismatched_or_shared_endpoint_profile(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    with pytest.raises(ValueError, match="exact endpoint binding"):
        build_launch_spec(
            "pi",
            harness_id="pi-exact",
            root=tmp_path / "missing",
            executable=fake_harnesses["pi"],
            local_bindings=True,
        )

    pi_binding = _endpoint_binding(tmp_path, "pi", harness_id="pi-exact")
    with pytest.raises(ValueError, match="does not match"):
        build_launch_spec(
            "omp",
            harness_id="omp-exact",
            root=tmp_path / "mismatch",
            executable=_fake_executable(tmp_path, fake_harnesses, "omp"),
            local_bindings=True,
            endpoint_binding=pi_binding,
        )

    omp_binding = _endpoint_binding(tmp_path, "omp", harness_id="omp-exact")
    pi_spec = build_launch_spec(
        "pi",
        harness_id="pi-exact",
        root=tmp_path / "isolated",
        executable=fake_harnesses["pi"],
        local_bindings=True,
        endpoint_binding=pi_binding,
    )
    omp_spec = build_launch_spec(
        "omp",
        harness_id="omp-exact",
        root=tmp_path / "isolated",
        executable=_fake_executable(tmp_path, fake_harnesses, "omp"),
        local_bindings=True,
        endpoint_binding=omp_binding,
    )
    assert pi_spec.root_dir != omp_spec.root_dir
    assert pi_spec.capability_root_path != omp_spec.capability_root_path

    shared_root_binding = replace(omp_binding, harness_id="omp-sibling")
    with pytest.raises(ValueError, match="generation-specific"):
        build_launch_spec(
            "omp",
            harness_id="omp-sibling",
            root=tmp_path / "shared-root-rejected",
            executable=_fake_executable(tmp_path, fake_harnesses, "omp"),
            local_bindings=True,
            endpoint_binding=shared_root_binding,
        )
    assert pi_spec.endpoint_descriptor_path != omp_spec.endpoint_descriptor_path


@pytest.mark.external
def test_installed_binary_live_version_probe_is_observation_not_inference_proof(tmp_path: Path) -> None:
    probes = detect_installed_harnesses(tmp_path / "real-probes")

    assert set(probes) == {"omp", "pi", "claude", "codex", "antigravity"}
    for probe in probes.values():
        assert probe.evidence_scope == "local_detection_only"
        assert probe.external_conformance_proven is False
        assert isinstance(probe.matches_pin, bool)
        if probe.matches_pin:
            assert probe.reported_version == probe.pinned_version


def test_catalog_and_bootstraps_share_canonical_surface_without_live_refresh(
    tmp_path: Path,
) -> None:
    assert set(BUILTIN_ADAPTERS) == {"omp", "pi", "claude", "codex", "antigravity"}
    assert all(
        provider.canonical_tool_names == CANONICAL_TOOL_NAMES
        for provider in BUILTIN_ADAPTERS.values()
    )

    async def handler(_bound, _peer, _request):
        return {"ok": True, "result": {}}

    server = UnixMCPBootstrapServer(
        tmp_path / "bootstrap" / "agentnet.sock",
        bind_peer=lambda peer: peer,
        handler=handler,
        generation="endpoint-generation-with-entropy-001",
    )
    assert server.canonical_tool_names == CANONICAL_TOOL_NAMES
    assert server.refresh_status() == {"state": "restart_required"}
    assert WindowsMCPBootstrapServer.refresh_status() == {"state": "restart_required"}


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
