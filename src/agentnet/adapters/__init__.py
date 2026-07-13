"""Harness adapter declarations, private auth inputs, and executable specs."""

from .auth import EphemeralBrokerEnvironment, HarnessAuthInjection, PreprovisionedPrivateAuth
from .abi import (
    ADAPTER_ABI_VERSION,
    AdapterConformanceResultV1,
    AdapterManifestV1,
    AdapterProviderV1,
    StaticAdapterProviderV1,
    run_adapter_conformance,
)
from .base import AdapterLaunchSpec, ExecutableProbe
from .catalog import BUILTIN_ADAPTERS
from .specs import build_launch_spec, detect_executable, detect_installed_harnesses

__all__ = [
    "AdapterLaunchSpec",
    "ADAPTER_ABI_VERSION",
    "AdapterConformanceResultV1",
    "AdapterManifestV1",
    "AdapterProviderV1",
    "BUILTIN_ADAPTERS",
    "EphemeralBrokerEnvironment",
    "ExecutableProbe",
    "HarnessAuthInjection",
    "PreprovisionedPrivateAuth",
    "StaticAdapterProviderV1",
    "build_launch_spec",
    "detect_executable",
    "detect_installed_harnesses",
    "run_adapter_conformance",
]
