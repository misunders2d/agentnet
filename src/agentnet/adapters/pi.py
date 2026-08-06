from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.specs import build_launch_spec
from agentnet.bindings.endpoint import EndpointBinding

# OMP consumes Pi's package manifest and TypeScript extension natively.  These
# values describe that shared loader contract; endpoint identity still comes
# only from the sealed per-process binding supplied by the supervisor.
EXTENSION_MANIFEST_KEY = "pi"
EXTENSION_MODULE = "src/agentnet/bindings/pi_extension.ts"

CAPABILITIES = AdapterCapabilities(
    harness="pi",
    background_path="pi --mode rpc with separate session directory",
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
        "pi",
        harness_id=harness_id,
        root=root,
        executable=executable,
        local_bindings=local_bindings,
        endpoint_binding=endpoint_binding,
    )
