from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "ci" / "exact_endpoint_routing_e2e.py"
SELECTED_PACKAGE_ROOT = Path(
    os.environ.get("AGENTNET_VERIFICATION_INSTALL_ROOT", ROOT)
).resolve()
REQUIRED_REPORT = {
    "event_id",
    "target_harness_id",
    "processing_harness_id",
    "sibling_reactions",
    "offline_queue_owner",
}


def _run_scenario(*, runtime_root: Path, workspace: Path) -> dict[str, object]:
    workspace.mkdir(mode=0o700)
    poison = workspace / "agentnet.py"
    poison.write_text("raise RuntimeError('workspace fallback used')\n", encoding="utf-8")
    environment = os.environ.copy()
    environment.pop("PYTHONPATH", None)
    environment["PYTHONDONTWRITEBYTECODE"] = "1"
    completed = subprocess.run(
        [
            sys.executable,
            "-B",
            "-I",
            str(SCRIPT),
            "run",
            "--package-root",
            str(SELECTED_PACKAGE_ROOT),
            "--runtime-root",
            str(runtime_root),
            "--workspace",
            str(workspace),
        ],
        cwd=workspace,
        env=environment,
        check=False,
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stderr
    assert completed.stderr == ""
    assert poison.read_text(encoding="utf-8") == "raise RuntimeError('workspace fallback used')\n"
    assert {path.name for path in workspace.iterdir()} == {"agentnet.py"}
    return json.loads(completed.stdout)


def test_exact_endpoint_routing_is_deterministic_and_exclusive(tmp_path: Path) -> None:
    first_runtime = tmp_path / "runtime-first"
    second_runtime = tmp_path / "runtime-second"
    first = _run_scenario(runtime_root=first_runtime, workspace=tmp_path / "workspace-first")
    second = _run_scenario(runtime_root=second_runtime, workspace=tmp_path / "workspace-second")

    assert REQUIRED_REPORT <= first.keys()
    assert first == second
    assert first["event_id"] == "event:exact-endpoint-routing:online"
    assert first["target_harness_id"] == "harness:pi:target"
    assert first["processing_harness_id"] == first["target_harness_id"]
    assert first["sibling_reactions"] == 0
    assert first["offline_queue_owner"] == first["target_harness_id"]
    assert first["offline_processing_harness_ids"] == []
    assert first["endpoint_processes_remaining"] == 0
    assert first["capability_roots_remaining"] == 0
    assert first["workspace_fallback_used"] is False
    assert not first_runtime.exists()
    assert not second_runtime.exists()


def test_package_script_exposes_exact_endpoint_routing_smoke() -> None:
    package = json.loads((ROOT / "package.json").read_text(encoding="utf-8"))

    assert package["scripts"]["check:exact-endpoint-routing"] == (
        "UV_CACHE_DIR=/tmp/uv-cache uv run python "
        "scripts/ci/exact_endpoint_routing_e2e.py"
    )
