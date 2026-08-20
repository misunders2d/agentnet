from __future__ import annotations

import json
import os
import subprocess
import ssl
from pathlib import Path
from types import SimpleNamespace

import pytest

from agentnet import __version__
from agentnet import cli
from agentnet.cli.commands import diagnostics
from agentnet.cli import helpers


def test_top_level_version_is_available(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as stopped:
        cli.main(["--version"])

    assert stopped.value.code == 0
    assert capsys.readouterr().out == f"agentnet {__version__}\n"

def test_public_cli_requests_use_system_tls_context(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    observed: dict[str, object] = {}

    def request(method: str, url: str, **kwargs: object) -> SimpleNamespace:
        observed.update({"method": method, "url": url, **kwargs})
        return SimpleNamespace(status_code=200, json=lambda: {"status": "alive"})

    monkeypatch.setattr(helpers.httpx, "request", request)

    result = helpers._public_json_request(
        server="https://core.example",
        method="GET",
        path="/healthz",
        body={},
    )

    context = observed["verify"]
    assert isinstance(context, ssl.SSLContext)
    assert context.check_hostname is True
    assert context.verify_mode == ssl.CERT_REQUIRED
    assert result == {"status": "alive"}

def test_verify_finds_source_root_after_module_moves(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("AGENTNET_PACKAGE_ROOT", raising=False)
    assert diagnostics._verification_package_root() == Path(__file__).resolve().parents[2]



def test_verify_targets_packaged_tests_with_package_cwd(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    (tmp_path / "tests").mkdir()
    monkeypatch.setenv("AGENTNET_PACKAGE_ROOT", str(tmp_path))
    observed: dict[str, object] = {}

    def fake_call(arguments: list[str], *, cwd: Path, env: dict[str, str]) -> int:
        observed.update(
            {
                "arguments": arguments,
                "cwd": cwd,
                "agentnet_package_root": env["AGENTNET_PACKAGE_ROOT"],
                "agentnet_verification_install_root": env[
                    "AGENTNET_VERIFICATION_INSTALL_ROOT"
                ],
                "hypothesis_storage": env["HYPOTHESIS_STORAGE_DIRECTORY"],
                "pytest_addopts": env["PYTEST_ADDOPTS"],
                "pytest_plugins_present": "PYTEST_PLUGINS" in env,
                "python_dont_write_bytecode": env["PYTHONDONTWRITEBYTECODE"],
                "python_pycache_prefix": env["PYTHONPYCACHEPREFIX"],
                "pythonpath": env["PYTHONPATH"],
            }
        )
        return 7

    monkeypatch.setattr(subprocess, "call", fake_call)

    result = cli.command_verify(SimpleNamespace(pytest_args=[]))

    assert result == 7
    verification_root = Path(str(observed["cwd"]))
    assert Path(str(observed["agentnet_package_root"])) == verification_root
    assert Path(str(observed["agentnet_verification_install_root"])) == tmp_path
    assert observed["arguments"] == [
        diagnostics.sys.executable,
        "-m",
        "pytest",
        "-q",
        "-p",
        "no:cacheprovider",
        str(verification_root / "tests"),
        f"--ignore={verification_root / 'tests/adapters/test_installed_live_inference.py'}",
        f"--ignore={verification_root / 'tests/adapters/test_subprocess_lifecycle.py'}",
        f"--ignore={verification_root / 'tests/components/test_bakeoff_evidence.py'}",
    ]
    assert observed["pytest_addopts"] == ""
    assert observed["pytest_plugins_present"] is False
    assert observed["python_dont_write_bytecode"] == "1"
    assert observed["pythonpath"] == os.pathsep.join(
        (str(verification_root / "src"), str(verification_root))
    )
    hypothesis_storage = Path(str(observed["hypothesis_storage"]))
    assert hypothesis_storage.name == "hypothesis"
    assert hypothesis_storage.parent.name.startswith("agentnet-verify-")
    pycache_prefix = Path(str(observed["python_pycache_prefix"]))
    assert pycache_prefix == hypothesis_storage.parent / "pycache"
    assert not hypothesis_storage.parent.exists()


@pytest.mark.parametrize(
    "arguments",
    [
        ["-p", "cacheprovider"],
        ["-pcacheprovider"],
        ["-k", "focused"],
        ["--basetemp=/tmp/unsafe"],
        ["--junitxml=/tmp/unsafe.xml"],
    ],
)
def test_verify_rejects_all_pytest_arguments(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    arguments: list[str],
) -> None:
    (tmp_path / "tests").mkdir()
    monkeypatch.setenv("AGENTNET_PACKAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("PYTEST_ADDOPTS", "-p cacheprovider")
    monkeypatch.setenv("PYTEST_PLUGINS", "_pytest.cacheprovider")
    monkeypatch.setattr(
        subprocess,
        "call",
        lambda *_args, **_kwargs: pytest.fail("unsafe pytest argument reached pytest"),
    )

    with pytest.raises(SystemExit, match="does not permit pytest arguments"):
        cli.command_verify(SimpleNamespace(pytest_args=arguments))


def test_verify_does_not_write_runtime_cache_into_package_tree(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    tests_root = tmp_path / "tests"
    tests_root.mkdir()
    (tests_root / "test_property.py").write_text(
        "from hypothesis import given, strategies as st\n"
        "@given(st.integers())\n"
        "def test_property(value):\n"
        "    assert isinstance(value, int)\n",
        encoding="utf-8",
    )
    monkeypatch.setenv("AGENTNET_PACKAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("PYTEST_ADDOPTS", "-p cacheprovider")
    monkeypatch.setenv("PYTEST_PLUGINS", "_pytest.cacheprovider")

    assert cli.command_verify(SimpleNamespace(pytest_args=[])) == 0

    assert not (tmp_path / ".hypothesis").exists()
    assert not (tmp_path / ".pytest_cache").exists()
    assert not list(tmp_path.rglob("__pycache__"))
    assert not list(tmp_path.rglob("*.pyc"))


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

    monkeypatch.setattr(
        "agentnet.cli.commands.diagnostics.installed_probe_report",
        fake_report,
    )
    monkeypatch.setattr(
        "agentnet.cli.commands.diagnostics.assert_installed_probe_report",
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
    monkeypatch.setattr(
        "agentnet.cli.commands.diagnostics.installed_probe_report",
        lambda root: report,
    )
    monkeypatch.setattr(
        "agentnet.cli.commands.diagnostics.assert_installed_probe_report",
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
