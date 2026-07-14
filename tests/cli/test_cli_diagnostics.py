from __future__ import annotations

import json
import subprocess
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import __version__
from agentnet import cli


def test_top_level_version_is_available(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as stopped:
        cli.main(["--version"])

    assert stopped.value.code == 0
    assert capsys.readouterr().out == f"agentnet {__version__}\n"


def test_verify_targets_packaged_tests_with_package_cwd(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    (tmp_path / "tests").mkdir()
    monkeypatch.setenv("AGENTNET_PACKAGE_ROOT", str(tmp_path))
    observed: dict[str, object] = {}

    def fake_call(arguments: list[str], *, cwd: Path) -> int:
        observed.update({"arguments": arguments, "cwd": cwd})
        return 7

    monkeypatch.setattr(subprocess, "call", fake_call)

    result = cli.command_verify(SimpleNamespace(pytest_args=["-k", "focused"]))

    assert result == 7
    assert observed == {
        "arguments": [
            cli.sys.executable,
            "-m",
            "pytest",
            "-q",
            str(tmp_path / "tests"),
            f"--ignore={tmp_path / 'tests/adapters/test_installed_live_inference.py'}",
            f"--ignore={tmp_path / 'tests/adapters/test_subprocess_lifecycle.py'}",
            f"--ignore={tmp_path / 'tests/components/test_bakeoff_evidence.py'}",
            "-k",
            "focused",
        ],
        "cwd": tmp_path,
    }


def test_verify_fails_when_packaged_tests_are_absent(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("AGENTNET_PACKAGE_ROOT", str(tmp_path))

    with pytest.raises(SystemExit, match="packaged tests are unavailable"):
        cli.command_verify(SimpleNamespace(pytest_args=[]))


def test_scoped_harness_probe_is_diagnostic_only(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    probe = {
        "harness": "pi",
        "matches_pin": True,
        "resolved_path": "/usr/bin/pi",
    }
    observed: dict[str, object] = {}

    def fake_report(root: Path, *, harnesses: tuple[str, ...]) -> dict[str, dict[str, object]]:
        observed.update({"root": root, "harnesses": harnesses})
        return {"pi": probe}

    monkeypatch.setattr(cli, "installed_probe_report", fake_report)
    monkeypatch.setattr(
        cli,
        "assert_installed_probe_report",
        lambda _report: pytest.fail("scoped diagnostics must not invoke the four-harness gate"),
    )

    result = cli.command_harness_probe(
        SimpleNamespace(data_dir=str(tmp_path), harness="pi")
    )

    assert result == 0
    assert observed == {"root": tmp_path, "harnesses": ("pi",)}
    assert json.loads(capsys.readouterr().out) == {
        "diagnostic_only": True,
        "harness": "pi",
        "probe": probe,
        "ready": True,
        "scope": "single_harness",
    }


def test_full_harness_probe_preserves_four_harness_gate(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    report = {
        harness: {"matches_pin": True, "resolved_path": f"/usr/bin/{harness}"}
        for harness in ("claude", "codex", "pi", "antigravity")
    }
    observed: list[dict[str, dict[str, object]]] = []
    monkeypatch.setattr(cli, "installed_probe_report", lambda root: report)
    monkeypatch.setattr(
        cli,
        "assert_installed_probe_report",
        lambda value: observed.append(value),
    )

    result = cli.command_harness_probe(
        SimpleNamespace(data_dir=str(tmp_path), harness="all")
    )

    assert result == 0
    assert observed == [report]
    assert json.loads(capsys.readouterr().out) == {
        "ready": True,
        "harnesses": report,
    }
