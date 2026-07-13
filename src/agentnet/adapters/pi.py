from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.specs import build_launch_spec

CAPABILITIES = AdapterCapabilities(
    harness="pi",
    background_path="pi --mode rpc with separate session directory",
    local_binding="direct_ipc",
    passive_indicator="content_free_count",
    semantic_default="deterministic_only",
)


def launch_spec(*, harness_id: str, root: Path, executable: str | None = None):
    return build_launch_spec("pi", harness_id=harness_id, root=root, executable=executable)
