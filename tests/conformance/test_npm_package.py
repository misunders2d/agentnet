from __future__ import annotations

import json
import os
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def test_npm_package_is_scoped_discoverable_and_version_aligned() -> None:
    package = json.loads((ROOT / "package.json").read_text(encoding="utf-8"))
    pyproject = (ROOT / "pyproject.toml").read_text(encoding="utf-8")
    python_version = re.search(r'^version = "([^"]+)"$', pyproject, re.MULTILINE)
    assert package["name"] == "@misunders2d/agentnet"
    assert package["version"] == python_version.group(1)
    assert package["license"] == "Apache-2.0"
    assert package["publishConfig"] == {"access": "public"}
    assert "pi-package" in package["keywords"]
    assert package["pi"] == {
        "extensions": ["./src/agentnet/bindings/pi_extension.ts"]
    }
    assert package["bin"] == {"agentnet": "npm/bin/agentnet.mjs"}
    assert package["os"] == ["linux"]


def test_npm_package_contains_one_runtime_and_all_harness_adapters() -> None:
    required = (
        "LICENSE",
        "uv.lock",
        "npm/bin/agentnet.mjs",
        "src/agentnet/bindings/pi_extension.ts",
        "src/agentnet/adapters/claude.py",
        "src/agentnet/adapters/codex.py",
        "src/agentnet/adapters/pi.py",
        "src/agentnet/adapters/antigravity.py",
    )
    assert all((ROOT / path).is_file() for path in required)
    assert os.access(ROOT / "npm/bin/agentnet.mjs", os.X_OK)


def test_npm_launcher_is_locked_shell_free_and_user_scoped() -> None:
    launcher = (ROOT / "npm/bin/agentnet.mjs").read_text(encoding="utf-8")
    for required in (
        '"--frozen"',
        '"--no-default-groups"',
        "UV_PROJECT_ENVIRONMENT:",
        "AGENTNET_NPM_RUNTIME_DIR",
        'shell: false',
    ):
        assert required in launcher
    assert "curl" not in launcher
    assert "postinstall" not in json.loads(
        (ROOT / "package.json").read_text(encoding="utf-8")
    )["scripts"]
