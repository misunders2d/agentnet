from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.pi import EXTENSION_MANIFEST_KEY, EXTENSION_MODULE
from agentnet.adapters.specs import build_launch_spec
from agentnet.bindings.endpoint import EndpointBinding

# OMP natively consumes the Pi package manifest and extension implementation.
# The supervisor still supplies a separate exact EndpointBinding for each OMP
# profile, so none of its endpoint state can be shared with a Pi profile.

CAPABILITIES = AdapterCapabilities(
    harness="omp",
    background_path="omp --mode rpc with an isolated endpoint profile",
    local_binding="direct_ipc",
    passive_indicator="content_free_count",
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
        "omp",
        harness_id=harness_id,
        root=root,
        executable=executable,
        local_bindings=local_bindings,
        endpoint_binding=endpoint_binding,
    )


__all__ = [
    "CAPABILITIES",
    "EXTENSION_MANIFEST_KEY",
    "EXTENSION_MODULE",
    "launch_spec",
]
