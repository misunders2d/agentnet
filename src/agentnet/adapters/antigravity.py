from pathlib import Path

from agentnet.adapters.base import AdapterCapabilities
from agentnet.adapters.specs import build_launch_spec

CAPABILITIES = AdapterCapabilities(
    harness="antigravity",
    background_path="serialized agy --conversation dedicated conversation",
    local_binding="none",
    passive_indicator="probe_only",
    semantic_default="deterministic_only",
)


def launch_spec(*, harness_id: str, root: Path, executable: str | None = None):
    return build_launch_spec("antigravity", harness_id=harness_id, root=root, executable=executable)
