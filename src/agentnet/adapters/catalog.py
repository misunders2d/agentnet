"""Built-in adapters expressed through the public adapter ABI v1."""

from __future__ import annotations

from functools import partial
from pathlib import Path
from typing import TYPE_CHECKING

from agentnet.adapters.abi import AdapterManifestV1, StaticAdapterProviderV1
from agentnet.adapters.capabilities import ALL as CAPABILITIES
from agentnet.adapters.native import create_native_driver
from agentnet.adapters.specs import EndpointAdapterLaunchSpec, build_launch_spec
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES

if TYPE_CHECKING:
    from agentnet.bindings.endpoint import EndpointBinding



class _EndpointAdapterProvider(StaticAdapterProviderV1):
    """Built-in provider requiring exact endpoint context before exposing tools."""

    canonical_tool_names = CANONICAL_TOOL_NAMES

    def build_launch_spec(
        self,
        *,
        harness_id: str,
        root: Path,
        executable: str | None = None,
        local_bindings: bool = False,
        endpoint_binding: EndpointBinding | None = None,
    ) -> EndpointAdapterLaunchSpec:
        return self._launch_factory(
            harness_id=harness_id,
            root=root,
            executable=executable,
            local_bindings=local_bindings,
            endpoint_binding=endpoint_binding,
        )


_TRANSPORTS = {
    "omp": ("omp_rpc_jsonl", True),
    "pi": ("pi_rpc_jsonl", True),
    "claude": ("claude_stream_json", True),
    "codex": ("codex_app_server", True),
    "antigravity": ("antigravity_print", False),
}


BUILTIN_ADAPTERS = {
    harness: _EndpointAdapterProvider(
        manifest=AdapterManifestV1(
            adapter_id=harness,
            harness=harness,
            transport=transport,
            persistent_process=persistent,
            local_binding=CAPABILITIES[harness].local_binding,
            passive_indicator=CAPABILITIES[harness].passive_indicator,
            semantic_default=CAPABILITIES[harness].semantic_default,
        ),
        capabilities=CAPABILITIES[harness],
        launch_factory=partial(build_launch_spec, harness),
        driver_factory=create_native_driver,
    )
    for harness, (transport, persistent) in _TRANSPORTS.items()
}


__all__ = ["BUILTIN_ADAPTERS", "CAPABILITIES"]
