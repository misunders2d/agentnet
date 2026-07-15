from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

import pytest


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
        "extensions": ["./src/agentnet/bindings/pi_extension.ts"],
        "skills": ["./skills"],
        "image": "https://raw.githubusercontent.com/misunders2d/agentnet/main/docs/assets/agentnet-overview.png",
    }
    assert package["bin"] == {"agentnet": "npm/bin/agentnet.mjs"}
    assert {
        ".gitignore",
        "docs/assets/agentnet-overview.png",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/REVIEW.md",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/compatibility.html",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/junitreport.xml",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/tck_report.html",
        "evidence/local/2026-07-15-v0.1.6/artifacts/RETENTION.md",
        "skills/**/*.md",
    } <= set(package["files"])
    assert package["scripts"]["check:packed"] == "node npm/scripts/check-packed-package.mjs"
    assert package["scripts"]["check"].endswith("&& npm run check:packed")
    assert package["os"] == ["linux"]
    assert re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", package["version"])
    assert package["peerDependenciesMeta"] == {
        "@earendil-works/pi-ai": {"optional": True},
        "@earendil-works/pi-coding-agent": {"optional": True},
    }


def test_npm_package_contains_one_runtime_and_all_harness_adapters() -> None:
    required = (
        "LICENSE",
        "docs/assets/agentnet-overview.png",
        "uv.lock",
        "npm/bin/agentnet.mjs",
        "npm/scripts/check-packed-package.mjs",
        "skills/agentnet-operator/SKILL.md",
        "skills/agentnet-operator/references/safe-commands.md",
        "skills/agentnet-operator/references/fail-closed-boundaries.md",
        "skills/agentnet-operator/references/required-communication-scope.md",
        "src/agentnet/bindings/pi_extension.ts",
        "src/agentnet/adapters/claude.py",
        "src/agentnet/adapters/codex.py",
        "src/agentnet/adapters/pi.py",
        "src/agentnet/adapters/antigravity.py",
    )
    assert all((ROOT / path).is_file() for path in required)
    assert os.access(ROOT / "npm/bin/agentnet.mjs", os.X_OK)


def test_bundled_pi_operator_skill_is_documentation_only_and_fail_closed() -> None:
    skill = (ROOT / "skills/agentnet-operator/SKILL.md").read_text(encoding="utf-8")
    assert skill.startswith("---\n")
    assert re.search(r"^name: agentnet-operator$", skill, re.MULTILINE)
    assert re.search(r"^description: \S.*$", skill, re.MULTILINE)
    assert "installation is code installation only" in skill.lower()
    assert "local_bindings_required=true" in skill
    assert "agentnet-supervisor.json" in skill
    assert "agentnet.json" in skill
    assert "../../docs/requirements.md" in skill
    assert "../../docs/specification.md" in skill
    assert "blocked: product component not yet shipped" in skill
    assert "operator must not have to write integration code" in skill

    commands = (
        ROOT / "skills/agentnet-operator/references/safe-commands.md"
    ).read_text(encoding="utf-8")
    for required in (
        "agentnet demo",
        "agentnet a2a-demo",
        "agentnet init",
        "agentnet status",
        "agentnet serve",
        "agentnet harness-probe",
        "agentnet supervisor-run",
        "agentnet network create",
        "agentnet bootstrap-server-agent",
        "agentnet approval provision",
        "agentnet join begin",
        "--harness pi",
        "--name server-agent-1",
        "--state .agentnet/join-pending.json",
        "agentnet join complete",
        "agentnet server-agent activate",
        "agentnet message acknowledge",
        "agentnet artifact upload",
        "OIDC discovery is public-only by default",
        "exact JWK thumbprints",
        "approval service on the same security boundary",
    ):
        assert required in commands

    scope = (
        ROOT / "skills/agentnet-operator/references/required-communication-scope.md"
    ).read_text(encoding="utf-8")
    for required in (
        "ARC-001..006",
        "COM-001",
        "COM-011",
        "ORG-001..006",
        "FILE-001..006",
        "AVL-001..008",
        "UX-001..006",
        "FED-001..009",
        "There is no separate privileged Hub product",
        "operator must not be required to write missing adapters",
    ):
        assert required in scope

    boundaries = (
        ROOT / "skills/agentnet-operator/references/fail-closed-boundaries.md"
    ).read_text(encoding="utf-8")
    for required in (
        "workforce OIDC",
        "independently controlled WebAuthn",
        "Infisical",
        "PostgreSQL 18.4",
        "accepted_local",
        "measured supervisor-launched child",
        "Protected source builds use one exact recipient-owned supervisor sequence",
        "result upload requires the committed release receipt",
        "include_payload",
        "tool and effect authority false",
    ):
        assert required in boundaries


def test_pi_binding_failure_explains_supervisor_activation() -> None:
    extension = (ROOT / "src/agentnet/bindings/pi_extension.ts").read_text(
        encoding="utf-8"
    )
    assert "package installation alone does not activate it" in extension
    assert "local_bindings_required=true" in extension
    for fail_closed_message in (
        "AgentNet local binding was not activated",
        "AgentNet local binding read failed",
        "AgentNet local binding schema is invalid",
    ):
        assert fail_closed_message in extension


def test_npm_launcher_is_locked_shell_free_and_user_scoped() -> None:
    launcher = (ROOT / "npm/bin/agentnet.mjs").read_text(encoding="utf-8")
    for required in (
        '"--frozen"',
        '"--no-default-groups"',
        "UV_PROJECT_ENVIRONMENT:",
        "AGENTNET_NPM_RUNTIME_DIR",
        "AGENTNET_PACKAGE_ROOT: packageRoot",
        'createHash("sha256")',
        "realpathSync",
        'shell: false',
        '"3.13.13"',
    ):
        assert required in launcher
    assert '">=3.13,<3.15"' not in launcher
    assert "curl" not in launcher
    assert "postinstall" not in json.loads(
        (ROOT / "package.json").read_text(encoding="utf-8")
    )["scripts"]


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_npm_launcher_rejects_unsupported_uv_before_runtime_creation(tmp_path: Path) -> None:
    package_root = tmp_path / "package"
    launcher = package_root / "npm/bin/agentnet.mjs"
    launcher.parent.mkdir(parents=True)
    shutil.copy2(ROOT / "npm/bin/agentnet.mjs", launcher)
    (package_root / "package.json").write_text(
        json.dumps({"version": "0.1.6"}),
        encoding="utf-8",
    )
    fake_uv = tmp_path / "old-uv"
    fake_uv.write_text(
        f"#!{sys.executable}\nprint('uv 0.9.13')\n",
        encoding="utf-8",
    )
    fake_uv.chmod(0o700)
    state_root = tmp_path / "state"
    environment = os.environ.copy()
    environment.update(
        {
            "AGENTNET_UV": str(fake_uv),
            "XDG_STATE_HOME": str(state_root),
        }
    )

    completed = subprocess.run(
        ["node", str(launcher), "--version"],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
        timeout=30,
    )

    assert completed.returncode == 1
    assert completed.stderr == (
        "AgentNet requires uv 0.11.28 or newer; found 0.9.13. "
        "Upgrade uv explicitly, then retry.\n"
    )
    assert not state_root.exists()


@pytest.mark.skipif(shutil.which("npm") is None, reason="npm is unavailable")
def test_npm_dry_run_tarball_contains_release_verifier_inputs() -> None:
    completed = subprocess.run(
        ["npm", "pack", "--dry-run", "--json", "--ignore-scripts"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
        timeout=60,
    )
    packed = json.loads(completed.stdout)
    manifest = next(iter(packed.values())) if isinstance(packed, dict) else packed[0]
    filenames = {entry["path"] for entry in manifest["files"]}
    assert {
        "docs/assets/agentnet-overview.png",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/REVIEW.md",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/compatibility.html",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/junitreport.xml",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/tck_report.html",
        "evidence/local/2026-07-15-v0.1.6/artifacts/RETENTION.md",
        "skills/agentnet-operator/SKILL.md",
        "skills/agentnet-operator/references/safe-commands.md",
        "skills/agentnet-operator/references/fail-closed-boundaries.md",
        "skills/agentnet-operator/references/required-communication-scope.md",
    } <= filenames
    if (ROOT / ".gitignore").is_file():
        assert ".gitignore" in filenames
    else:
        assert (ROOT / ".npmignore").is_file()


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_same_version_npm_installs_use_distinct_runtime_roots(tmp_path: Path) -> None:
    fake_uv = tmp_path / "fake-uv"
    fake_uv.write_text(
        f"#!{sys.executable}\n"
        "import json, os, sys\n"
        "if sys.argv[1:] == ['--version']:\n"
        "    print('uv 0.11.28')\n"
        "    raise SystemExit(0)\n"
        "from pathlib import Path\n"
        "Path(os.environ['AGENTNET_TEST_CAPTURE']).write_text(json.dumps({\n"
        "  'package_root': os.environ['AGENTNET_PACKAGE_ROOT'],\n"
        "  'runtime_root': os.environ['UV_PROJECT_ENVIRONMENT'],\n"
        "}), encoding='utf-8')\n",
        encoding="utf-8",
    )
    fake_uv.chmod(0o700)
    state_root = tmp_path / "state"
    package_version = json.loads(
        (ROOT / "package.json").read_text(encoding="utf-8")
    )["version"]
    captures: list[dict[str, str]] = []

    for name in ("global-copy", "pi-copy"):
        package_root = tmp_path / name
        launcher = package_root / "npm/bin/agentnet.mjs"
        launcher.parent.mkdir(parents=True)
        shutil.copy2(ROOT / "npm/bin/agentnet.mjs", launcher)
        (package_root / "package.json").write_text(
            json.dumps({"version": package_version}),
            encoding="utf-8",
        )
        capture = tmp_path / f"{name}.json"
        environment = os.environ.copy()
        environment.update(
            {
                "AGENTNET_TEST_CAPTURE": str(capture),
                "AGENTNET_UV": str(fake_uv),
                "XDG_STATE_HOME": str(state_root),
            }
        )
        subprocess.run(
            ["node", str(launcher), "--version"],
            check=True,
            env=environment,
            timeout=30,
        )
        captures.append(json.loads(capture.read_text(encoding="utf-8")))

    assert captures[0]["package_root"] != captures[1]["package_root"]
    assert captures[0]["runtime_root"] != captures[1]["runtime_root"]
    assert all(f"{package_version}-" in item["runtime_root"] for item in captures)
