#!/usr/bin/env python3
"""Contract fixture for the already-signed sandbox-launch boundary.

It intentionally provides no sandbox claim. Tests using it are labeled as
contract-fixture evidence and only exercise command composition/driver I/O.
"""

from __future__ import annotations

import os
import sys
import json
from pathlib import Path


def main() -> int:
    state_dir = os.environ.get("AGENTNET_STATE_DIR")
    if state_dir:
        entry = {
            "argv": sys.argv[1:],
            "cwd": os.getcwd(),
            "environment_bindings": {
                name: os.environ[name]
                for name in (
                    "CODEX_HOME",
                    "HOME",
                    "PI_CODING_AGENT_DIR",
                    "TMPDIR",
                    "XDG_CACHE_HOME",
                    "XDG_CONFIG_HOME",
                    "XDG_DATA_HOME",
                )
                if name in os.environ
            },
            "environment_keys": sorted(os.environ),
            "kind": "sandbox_wrapper",
            "value": {"argv": sys.argv[1:]},
        }
        with (Path(state_dir) / "native-fixture.log").open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(entry, separators=(",", ":"), sort_keys=True) + "\n")
    try:
        separator = sys.argv.index("--")
    except ValueError as exc:
        raise RuntimeError("fixture sandbox command omitted separator") from exc
    command = sys.argv[separator + 1 :]
    if not command:
        raise RuntimeError("fixture sandbox command omitted executable")
    os.execve(command[0], command, dict(os.environ))
    return 127


if __name__ == "__main__":
    raise SystemExit(main())
