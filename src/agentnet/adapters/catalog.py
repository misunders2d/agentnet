"""Built-in adapters expressed through the public adapter ABI v1."""

from __future__ import annotations

from functools import partial

from agentnet.adapters.abi import AdapterManifestV1, StaticAdapterProviderV1
from agentnet.adapters.capabilities import ALL as CAPABILITIES
from agentnet.adapters.native import create_native_driver
from agentnet.adapters.specs import build_launch_spec


_TRANSPORTS = {
    "claude": ("claude_stream_json", True),
    "codex": ("codex_app_server", True),
    "pi": ("pi_rpc_jsonl", True),
    "antigravity": ("antigravity_print", False),
}


BUILTIN_ADAPTERS = {
    harness: StaticAdapterProviderV1(
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


__all__ = ["BUILTIN_ADAPTERS"]
