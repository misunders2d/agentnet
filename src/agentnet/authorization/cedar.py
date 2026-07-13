"""Cedar sidecar contract; no allow fallback when unavailable."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path
from typing import Any

from agentnet.errors import GateBlocked


class CedarCLIAdapter:
    def __init__(self, executable: Path, policy_set: Path, schema: Path) -> None:
        self.executable = executable
        self.policy_set = policy_set
        self.schema = schema

    def authorize(self, request: dict[str, Any], entities: list[dict[str, Any]]) -> dict[str, Any]:
        if not self.executable.is_file() or not self.policy_set.is_file() or not self.schema.is_file():
            raise GateBlocked("G08/G16", "pinned Cedar evaluator or policy evidence is unavailable")
        process = subprocess.run(
            [str(self.executable), "authorize", "--policies", str(self.policy_set), "--schema", str(self.schema)],
            input=json.dumps({"request": request, "entities": entities}),
            text=True,
            capture_output=True,
            timeout=5,
            check=False,
        )
        if process.returncode != 0:
            raise GateBlocked("G08", "Cedar diagnostics or evaluator failure denied the request")
        result = json.loads(process.stdout)
        if result.get("decision") not in {"Allow", "Deny"} or result.get("diagnostics", {}).get("errors"):
            raise GateBlocked("G08", "invalid or diagnostic Cedar result denied the request")
        return result

