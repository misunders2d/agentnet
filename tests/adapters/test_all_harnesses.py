from __future__ import annotations

import json
from importlib import import_module
from pathlib import Path

import pytest

from agentnet.adapters.capabilities import ALL
from agentnet.adapters.specs import EndpointAdapterLaunchSpec
from agentnet.bindings.endpoint import EndpointBinding
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES
from agentnet.core.capabilities import (
    ENDPOINT_CAPABILITY_ROOT_BYTES,
    endpoint_capability_root_name,
)


HARNESSES = ("omp", "pi", "claude", "codex", "antigravity")


def _endpoint_binding(tmp_path: Path, harness: str) -> EndpointBinding:
    domain_id = "domain-installed-contract"
    harness_id = f"{harness}-installed-endpoint"
    adapter_generation = 11
    capability_base = (tmp_path / "capabilities").resolve()
    endpoint_base = capability_base / "endpoints"
    for directory in (capability_base, endpoint_base):
        directory.mkdir(mode=0o700)
        directory.chmod(0o700)
    capability_directory = endpoint_base / endpoint_capability_root_name(
        domain_id=domain_id,
        harness_id=harness_id,
        adapter_generation=adapter_generation,
    )
    capability_directory.mkdir(mode=0o700)
    capability_directory.chmod(0o700)
    capability_root = capability_directory / "capability-root.key"
    capability_root.write_bytes(bytes(range(ENDPOINT_CAPABILITY_ROOT_BYTES)))
    capability_root.chmod(0o600)
    return EndpointBinding(
        domain_id=domain_id,
        principal_id="principal-installed-contract",
        harness_id=harness_id,
        harness_kind=harness,
        credential_id=f"credential-{harness}",
        credential_epoch=7,
        adapter_generation=adapter_generation,
        mailbox_cursor=13,
        profile_key=f"{harness}-ordinary-default",
        capability_root_path=capability_root,
        process_measurement=f"pid:{100 + len(harness)}:start:900",
    )


def test_all_five_harness_manifests_are_zero_secret_and_non_foreground() -> None:
    assert set(ALL) == set(HARNESSES)
    for harness, capability in ALL.items():
        capability.validate()
        assert capability.harness == harness
        assert capability.holds_credentials is False
        assert capability.foreground_message_methods == ()
        assert capability.background_path
        assert capability.semantic_default in {"clean_worker_required", "deterministic_only"}


def test_direct_harnesses_stay_deterministic_only_until_clean_workers_are_proven() -> None:
    for harness in ("omp", "pi"):
        capability = ALL[harness]
        assert capability.local_binding == "direct_ipc"
        assert capability.semantic_default == "deterministic_only"


def test_mcp_harnesses_share_canonical_binding_semantics() -> None:
    for harness in ("claude", "codex", "antigravity"):
        assert ALL[harness].local_binding == "mcp"


def _assert_registered_presentation(
    harness: str,
    spec: EndpointAdapterLaunchSpec,
) -> None:
    arguments = spec.arguments
    presentation_names = tuple(name.replace(".", "_") for name in CANONICAL_TOOL_NAMES)
    if harness == "claude":
        tools_index = arguments.index("--tools")
        assert tuple(arguments[tools_index + 1].split(",")) == tuple(
            f"mcp__agentnet__{name}" for name in presentation_names
        )
        config = json.loads((spec.state_dir / "mcp.json").read_text(encoding="utf-8"))
        assert set(config["mcpServers"]) == {"agentnet"}
    elif harness == "codex":
        enabled = next(
            argument
            for argument in arguments
            if argument.startswith("mcp_servers.agentnet.enabled_tools=")
        )
        assert tuple(json.loads(enabled.split("=", 1)[1])) == presentation_names
        assert "mcp_servers.agentnet.required=true" in arguments
    elif harness in {"omp", "pi"}:
        tools_index = arguments.index("--tools")
        assert tuple(arguments[tools_index + 1].split(",")) == presentation_names
        assert "--extension" in arguments
    else:
        config = json.loads(
            (spec.home_dir / ".gemini" / "settings.json").read_text(encoding="utf-8")
        )
        assert set(config["mcpServers"]) == {"agentnet"}


@pytest.mark.parametrize("harness", HARNESSES)
def test_installed_session_registers_exact_v0145_surface(
    tmp_path: Path,
    harness: str,
) -> None:
    binding = _endpoint_binding(tmp_path, harness)
    adapter = import_module(f"agentnet.adapters.{harness}")

    spec = adapter.launch_spec(
        harness_id=binding.harness_id,
        root=tmp_path / "installed-sessions",
        executable=f"/opt/agentnet-contract/{harness}",
        local_bindings=True,
        endpoint_binding=binding,
    )

    assert spec.canonical_tool_names == CANONICAL_TOOL_NAMES
    _assert_registered_presentation(harness, spec)
    assert spec.foreground_session_id is None
    assert spec.harness_id == binding.harness_id
    assert spec.profile_key == binding.profile_key
    assert spec.adapter_generation == binding.adapter_generation
    assert spec.credential_epoch == binding.credential_epoch
    assert spec.capability_root_path == binding.capability_root_path

    descriptor_path = spec.endpoint_descriptor_path
    assert descriptor_path is not None
    descriptor = json.loads(descriptor_path.read_text(encoding="utf-8"))
    assert descriptor == {
        "adapter_generation": binding.adapter_generation,
        "capability_root_path": str(binding.capability_root_path),
        "credential_epoch": binding.credential_epoch,
        "credential_id": binding.credential_id,
        "domain_id": binding.domain_id,
        "harness_id": binding.harness_id,
        "harness_kind": binding.harness_kind,
        "mailbox_cursor": binding.mailbox_cursor,
        "principal_id": binding.principal_id,
        "process_measurement": binding.process_measurement,
        "profile_key": binding.profile_key,
        "refresh_behavior": "restart_required",
        "schema": "agentnet.endpoint-launch-descriptor.v1",
    }
    assert descriptor_path.stat().st_mode & 0o077 == 0


@pytest.mark.parametrize("harness", HARNESSES)
def test_installed_session_denies_ambient_identity_fallback(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    harness: str,
) -> None:
    ambient_identity = tmp_path / "ambient-home" / ".agentnet" / "identity.json"
    ambient_identity.parent.mkdir(parents=True)
    ambient_identity.write_text(
        '{"domain_id":"forged","harness_id":"global-fallback"}',
        encoding="utf-8",
    )
    monkeypatch.setenv("HOME", str(ambient_identity.parents[1]))
    adapter = import_module(f"agentnet.adapters.{harness}")

    with pytest.raises(ValueError, match="endpoint binding"):
        adapter.launch_spec(
            harness_id=f"{harness}-installed-endpoint",
            root=tmp_path / "missing-binding-sessions",
            executable=f"/opt/agentnet-contract/{harness}",
            local_bindings=True,
        )
