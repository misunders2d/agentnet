from __future__ import annotations

from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.specs import build_launch_spec
from agentnet.bindings.endpoint import EndpointBinding


CAPABILITIES = AdapterCapabilities(
    harness="antigravity",
    background_path="serialized agy --conversation dedicated conversation",
    local_binding="mcp",
    passive_indicator="probe_only",
    semantic_default="deterministic_only",
)


def launch_spec(
    *,
    harness_id: str,
    root: Path,
    executable: str | None = None,
    local_bindings: bool = False,
    endpoint_binding: EndpointBinding | None = None,
):
    return build_launch_spec(
        "antigravity",
        harness_id=harness_id,
        root=root,
        executable=executable,
        local_bindings=local_bindings,
        endpoint_binding=endpoint_binding,
    )
