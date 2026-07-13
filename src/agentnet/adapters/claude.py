from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.specs import build_launch_spec

CAPABILITIES = AdapterCapabilities(
    harness="claude",
    background_path="dedicated headless or approved Agent SDK worker",
    local_binding="mcp",
    passive_indicator="content_free_count",
    semantic_default="clean_worker_required",
)


def launch_spec(*, harness_id: str, root: Path, executable: str | None = None):
    return build_launch_spec("claude", harness_id=harness_id, root=root, executable=executable)
