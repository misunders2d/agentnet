from __future__ import annotations

from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.specs import build_launch_spec
from agentnet.bindings.endpoint import EndpointBinding


CAPABILITIES = AdapterCapabilities(
    harness="codex",
    background_path="dedicated app-server thread over private stdio/socket",
    local_binding="mcp",
    passive_indicator="none",
    semantic_default="clean_worker_required",
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
        "codex",
        harness_id=harness_id,
        root=root,
        executable=executable,
        local_bindings=local_bindings,
        endpoint_binding=endpoint_binding,
    )
