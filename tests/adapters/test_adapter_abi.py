from __future__ import annotations

from dataclasses import replace
from pathlib import Path

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.adapters import (
    BUILTIN_ADAPTERS,
    AdapterManifestV1,
    StaticAdapterProviderV1,
    run_adapter_conformance,
)
from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.native import (
    NativeHarnessDriver,
    NativeTurnResult,
    create_native_driver,
    register_native_driver,
)
from agentnet.adapters.specs import build_launch_spec
from agentnet.errors import ValidationError


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_all_builtin_adapters_pass_the_same_versioned_abi_kit(
    tmp_path: Path,
    fake_harnesses,
    harness: str,
) -> None:
    result = run_adapter_conformance(
        BUILTIN_ADAPTERS[harness],
        root=tmp_path / harness,
        executable=fake_harnesses[harness],
    )

    assert result.adapter_id == harness
    assert result.abi_version == "1.0"
    assert result.passed is True
    assert result.external_conformance_proven is False
    assert result.model_dump()["schema"] == "agentnet.adapter-conformance-result.v1"
    assert "schema_" not in result.model_dump()
    assert result.checks == (
        "manifest_strict",
        "capabilities_attenuating",
        "private_launch_spec",
        "driver_exactly_bound",
        "lifecycle_surface",
    )


@pytest.mark.parametrize(
    "field,value",
    [
        ("abi_version", "2.0"),
        ("persistent_process", 1),
        ("foreground_access", True),
        ("holds_credentials", True),
        ("unknown", "ignored"),
    ],
)
def test_adapter_manifest_rejects_version_downgrade_coercion_authority_and_unknowns(
    field: str,
    value: object,
) -> None:
    raw: dict[str, object] = {
        "schema": "agentnet.adapter-manifest.v1",
        "abi_version": "1.0",
        "adapter_id": "future",
        "harness": "future",
        "transport": "future_jsonl",
        "persistent_process": True,
        "local_binding": "none",
        "passive_indicator": "none",
        "semantic_default": "deterministic_only",
        "foreground_access": False,
        "holds_credentials": False,
        "critical_extensions": (),
    }
    raw[field] = value
    with pytest.raises(PydanticValidationError):
        AdapterManifestV1.model_validate(raw)


class _FutureDriver(NativeHarnessDriver):
    def start(self, command, *, environment, recover, timeout_seconds, inherited_fds=(), process_started=None) -> None:
        del command, environment, recover, timeout_seconds, inherited_fds, process_started

    def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
        del prompt, timeout_seconds
        return NativeTurnResult("", self.spec.session_id, None, "future:complete")

    def healthcheck(self, *, timeout_seconds: float) -> dict[str, object]:
        del timeout_seconds
        return {"ready": True, "native_surface": "future_jsonl"}

    def stop(self) -> None:
        return None

    @property
    def alive(self) -> bool:
        return True

    @property
    def pid(self) -> int | None:
        return None


def test_future_adapter_registers_without_a_core_driver_switch_change(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    harness = "future-contract-fixture"
    register_native_driver(harness, _FutureDriver)

    def launch_factory(*, harness_id, root, executable=None, local_bindings=False):
        assert local_bindings is False
        base = build_launch_spec(
            "pi",
            harness_id=harness_id,
            root=root,
            executable=executable,
        )
        return replace(base, harness=harness, transport="future_jsonl")

    provider = StaticAdapterProviderV1(
        manifest=AdapterManifestV1(
            adapter_id=harness,
            harness=harness,
            transport="future_jsonl",
            persistent_process=True,
            local_binding="none",
            passive_indicator="none",
            semantic_default="deterministic_only",
        ),
        capabilities=AdapterCapabilities(
            harness=harness,
            background_path="private ABI fixture",
            local_binding="none",
            passive_indicator="none",
            semantic_default="deterministic_only",
        ),
        launch_factory=launch_factory,
        driver_factory=create_native_driver,
    )

    result = run_adapter_conformance(
        provider,
        root=tmp_path / "future",
        executable=fake_harnesses["pi"],
    )
    assert result.passed is True

    with pytest.raises(ValidationError, match="already registered"):
        register_native_driver(harness, _FutureDriver)


def test_unknown_critical_adapter_extension_fails_closed(tmp_path: Path, fake_harnesses) -> None:
    provider = BUILTIN_ADAPTERS["pi"]
    modified = StaticAdapterProviderV1(
        manifest=provider.manifest.model_copy(
            update={"critical_extensions": ("needs-unreviewed-authority",)}
        ),
        capabilities=provider.capabilities,
        launch_factory=provider.build_launch_spec,
        driver_factory=provider.create_driver,
    )
    with pytest.raises(ValidationError, match="critical"):
        run_adapter_conformance(
            modified,
            root=tmp_path / "critical",
            executable=fake_harnesses["pi"],
        )
