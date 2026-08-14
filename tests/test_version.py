from __future__ import annotations

import json
import tomllib
from pathlib import Path

from agentnet import __version__


ROOT = Path(__file__).resolve().parents[1]
EXPECTED_VERSION = "0.1.51"


def test_release_versions_are_exactly_v0151() -> None:
    pyproject = tomllib.loads((ROOT / "pyproject.toml").read_text(encoding="utf-8"))
    package = json.loads((ROOT / "package.json").read_text(encoding="utf-8"))
    lock_path = ROOT / "package-lock.json"
    package_lock = (
        json.loads(lock_path.read_text(encoding="utf-8")) if lock_path.exists() else None
    )

    assert pyproject["project"]["version"] == EXPECTED_VERSION
    assert package["version"] == EXPECTED_VERSION
    if package_lock is not None:
        assert package_lock["version"] == EXPECTED_VERSION
        assert package_lock["packages"][""]["version"] == EXPECTED_VERSION
    assert __version__ == EXPECTED_VERSION
