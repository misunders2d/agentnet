"""Stable versioned adapter ABI and executable local conformance kit.

The ABI owns launch/driver integration, not corporate authority.  A conforming
adapter can only narrow a private background session; it cannot grant identity,
credentials, foreground access, data access, or semantic execution authority.
"""

from __future__ import annotations

from collections.abc import Callable
from pathlib import Path
from typing import Any, Literal, Protocol, runtime_checkable

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.adapters.base import AdapterCapabilities, AdapterLaunchSpec
from agentnet.errors import ValidationError


ADAPTER_ABI_VERSION = "1.0"
ADAPTER_MANIFEST_SCHEMA = "agentnet.adapter-manifest.v1"


class AdapterManifestV1(BaseModel):
    """Strict discovery record for one adapter implementation."""

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        strict=True,
        populate_by_name=True,
        serialize_by_alias=True,
    )

    schema_: Literal[ADAPTER_MANIFEST_SCHEMA] = Field(
        default=ADAPTER_MANIFEST_SCHEMA,
        alias="schema",
    )
    abi_version: Literal[ADAPTER_ABI_VERSION] = ADAPTER_ABI_VERSION
    adapter_id: str = Field(pattern=r"^[a-z][a-z0-9_-]{0,63}$")
    harness: str = Field(pattern=r"^[a-z][a-z0-9_-]{0,63}$")
    transport: str = Field(pattern=r"^[a-z][a-z0-9_-]{0,63}$")
    persistent_process: bool
    local_binding: Literal["mcp", "direct_ipc", "none"]
    passive_indicator: Literal["content_free_count", "none", "probe_only"]
    semantic_default: Literal["clean_worker_required", "deterministic_only"]
    foreground_access: Literal[False] = False
    holds_credentials: Literal[False] = False
    critical_extensions: tuple[str, ...] = ()

    @field_validator("persistent_process", "foreground_access", "holds_credentials", mode="before")
    @classmethod
    def exact_booleans(cls, value: Any) -> Any:
        if type(value) is not bool:
            raise ValueError("adapter ABI booleans must be JSON booleans")
        return value

    @field_validator("critical_extensions")
    @classmethod
    def canonical_extensions(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if value != tuple(sorted(set(value))):
            raise ValueError("critical adapter extensions must be canonical")
        return value


@runtime_checkable
class AdapterProviderV1(Protocol):
    """Only extension point consumed by the adapter conformance kit."""

    manifest: AdapterManifestV1
    capabilities: AdapterCapabilities

    def build_launch_spec(
        self,
        *,
        harness_id: str,
        root: Path,
        executable: str | None = None,
        local_bindings: bool = False,
    ) -> AdapterLaunchSpec: ...

    def create_driver(self, spec: AdapterLaunchSpec) -> object: ...


class AdapterConformanceResultV1(BaseModel):
    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        strict=True,
        populate_by_name=True,
        serialize_by_alias=True,
    )

    schema_: Literal["agentnet.adapter-conformance-result.v1"] = Field(
        default="agentnet.adapter-conformance-result.v1",
        alias="schema",
    )
    adapter_id: str
    abi_version: Literal[ADAPTER_ABI_VERSION] = ADAPTER_ABI_VERSION
    passed: Literal[True] = True
    checks: tuple[str, ...]
    external_conformance_proven: Literal[False] = False


class StaticAdapterProviderV1:
    """Small provider implementation used by built-ins and third parties."""

    def __init__(
        self,
        *,
        manifest: AdapterManifestV1,
        capabilities: AdapterCapabilities,
        launch_factory: Callable[..., AdapterLaunchSpec],
        driver_factory: Callable[[AdapterLaunchSpec], object],
    ) -> None:
        self.manifest = manifest
        self.capabilities = capabilities
        self._launch_factory = launch_factory
        self._driver_factory = driver_factory

    def build_launch_spec(
        self,
        *,
        harness_id: str,
        root: Path,
        executable: str | None = None,
        local_bindings: bool = False,
    ) -> AdapterLaunchSpec:
        return self._launch_factory(
            harness_id=harness_id,
            root=root,
            executable=executable,
            local_bindings=local_bindings,
        )

    def create_driver(self, spec: AdapterLaunchSpec) -> object:
        return self._driver_factory(spec)


def run_adapter_conformance(
    provider: AdapterProviderV1,
    *,
    root: Path,
    executable: str,
    supported_critical_extensions: frozenset[str] = frozenset(),
) -> AdapterConformanceResultV1:
    """Validate the stable local ABI without claiming live external evidence."""

    try:
        supplied_manifest = provider.manifest
        capabilities = provider.capabilities
        build = provider.build_launch_spec
        create_driver = provider.create_driver
    except AttributeError as exc:
        raise ValidationError("adapter provider does not implement ABI v1") from exc
    raw_manifest = (
        supplied_manifest.model_dump(mode="python", by_alias=True)
        if isinstance(supplied_manifest, BaseModel)
        else supplied_manifest
    )
    manifest = AdapterManifestV1.model_validate(raw_manifest)
    unsupported = set(manifest.critical_extensions) - supported_critical_extensions
    if unsupported:
        raise ValidationError("adapter requests unsupported critical ABI extensions")
    if not callable(build) or not callable(create_driver) or not isinstance(capabilities, AdapterCapabilities):
        raise ValidationError("adapter provider does not implement ABI v1")
    capabilities.validate()
    if (
        capabilities.harness != manifest.harness
        or capabilities.local_binding != manifest.local_binding
        or capabilities.passive_indicator != manifest.passive_indicator
        or capabilities.semantic_default != manifest.semantic_default
        or capabilities.holds_credentials is not False
        or capabilities.foreground_message_methods
    ):
        raise ValidationError("adapter capabilities do not match its signed ABI surface")
    spec = build(
        harness_id=f"abi-conformance-{manifest.adapter_id}",
        root=root,
        executable=executable,
        local_bindings=False,
    )
    if not isinstance(spec, AdapterLaunchSpec):
        raise ValidationError("adapter did not return an AdapterLaunchSpec")
    spec.validate()
    if (
        spec.harness != manifest.harness
        or spec.transport != manifest.transport
        or spec.persistent_process is not manifest.persistent_process
        or spec.foreground_session_id is not None
        or spec.local_binding_enabled is not False
    ):
        raise ValidationError("adapter launch spec does not match its ABI manifest")
    driver = create_driver(spec)
    if getattr(driver, "spec", None) is not spec:
        raise ValidationError("adapter driver is not bound to the exact launch spec")
    for method in ("start", "submit", "healthcheck", "stop"):
        if not callable(getattr(driver, method, None)):
            raise ValidationError("adapter driver is missing an ABI lifecycle method")
    return AdapterConformanceResultV1(
        adapter_id=manifest.adapter_id,
        checks=(
            "manifest_strict",
            "capabilities_attenuating",
            "private_launch_spec",
            "driver_exactly_bound",
            "lifecycle_surface",
        ),
    )


__all__ = [
    "ADAPTER_ABI_VERSION",
    "ADAPTER_MANIFEST_SCHEMA",
    "AdapterConformanceResultV1",
    "AdapterManifestV1",
    "AdapterProviderV1",
    "StaticAdapterProviderV1",
    "run_adapter_conformance",
]
