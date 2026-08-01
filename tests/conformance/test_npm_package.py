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
    from agentnet import __version__

    assert __version__ == package["version"]
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
        "docs/assets/agentnet-overview.png",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/REVIEW.md",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/compatibility.html",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/junitreport.xml",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/tck_report.html",
        "evidence/local/2026-07-13-final/artifacts/agentnet-0.1.0-py3-none-any.whl",
        "evidence/local/2026-07-13-final/artifacts/agentnet-0.1.0.tar.gz",
        "evidence/local/2026-08-01-v0.1.35/artifacts/RETENTION.md",
        "evidence/local/2026-08-01-v0.1.35/artifacts/agentnet-0.1.35-py3-none-any.whl",
        "evidence/local/2026-08-01-v0.1.35/artifacts/agentnet-0.1.35.tar.gz",
        "skills/**/*.md",
        "tests/fixtures/**/*.json",
    } <= set(package["files"])
    assert "evidence/**/*.whl" not in package["files"]
    assert "evidence/**/*.gz" not in package["files"]
    assert package["scripts"]["check:packed"] == "node npm/scripts/check-packed-package.mjs"
    assert package["scripts"]["check"].endswith("&& npm run check:packed")
    packed_checker = (
        ROOT / "npm/scripts/check-packed-package.mjs"
    ).read_text(encoding="utf-8")
    for required in (
        '"agentnet.server-setup.evidence.v1"',
        '"service_executable_inaccessible"',
        '"unsafe_executable"',
        'evidence.status !== "blocked"',
        "evidence.authority_granted !== false",
        "evidence.identity_enrolled !== false",
        "completed.status !== 1",
        "expectedBlockedSetupBlocker(options.env)",
        "evidence.blocker !== expectedBlocker",
        'resolveCommand("uv", environment.PATH ?? "")',
        "for (const target of [nodeExecutable, uvExecutable])",
        "lineageBlocker(",
        'initName === "systemd" && !isProtectedServicePath(path.resolve(temporary))',
        "expected=${expectedBlocker}",
        'process.platform !== "win32") process.umask(0o022)',
        'entry.path === "npm/bin/agentnet.mjs"',
        "launcherArchiveEntry?.mode !== 0o755",
        "installedLauncherMode !== 0o755",
        '"--umask=0022"',
        "requireSafeInstalledModes(packageRoot)",
        "installed package tree has unsafe mode",
    ):
        assert required in packed_checker
    for rejected_blocker in (
        "missing_package_provenance",
        "missing_executable",
        "missing_host_tool",
        "unsupported_host",
    ):
        assert f'"{rejected_blocker}"' not in packed_checker
    assert package["os"] == ["linux", "darwin", "win32"]
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
        "skills/agentnet-operator/references/fresh-laptop-onboarding.md",
        "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md",
        "skills/agentnet-operator/references/examples/ordinary-server-setup-request.json",
        "skills/agentnet-operator/references/examples/ordinary-server-communication-only-setup-request.json",
        "skills/agentnet-operator/evals/evals.json",
        "src/agentnet/bindings/pi_extension.ts",
        "src/agentnet/adapters/claude.py",
        "src/agentnet/adapters/codex.py",
        "src/agentnet/adapters/pi.py",
        "src/agentnet/adapters/antigravity.py",
    )
    assert all((ROOT / path).is_file() for path in required)
    assert os.access(ROOT / "npm/bin/agentnet.mjs", os.X_OK)


def test_bundled_pi_operator_skill_and_setup_workflow_are_fail_closed() -> None:
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
    assert "Fresh-laptop onboarding is human-mediated" in skill
    assert "references/fresh-laptop-onboarding.md" in skill
    assert "references/examples/fresh-laptop-single-prompt.md" in skill
    assert "AgentNet blank-laptop onboarding — exact public packet" not in skill

    onboarding = (
        ROOT / "skills/agentnet-operator/references/fresh-laptop-onboarding.md"
    ).read_text(encoding="utf-8")
    for required in (
        "The unconnected laptop has no agent inbox",
        "Required bootstrap packet",
        "Canonical public onboarding prompt example",
        "single fresh-laptop onboarding prompt",
        "Any unresolved required placeholder blocks issuance",
        "Public package installation",
        "System-browser Google OIDC",
        "WebAuthn human approval",
        "Fixed C0 round-trip verification",
        "AgentNet `0.1.8` fails this gate",
        "blocked: product component not yet shipped",
        "references/examples/fresh-laptop-single-prompt.md",
    ):
        assert required in onboarding
    assert "AgentNet blank-laptop onboarding — exact public packet" not in onboarding

    example = (
        ROOT
        / "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md"
    ).read_text(encoding="utf-8")
    assert "AgentNet blank-laptop onboarding — exact public packet" in example
    for placeholder in (
        "<ONBOARDING_MODE>",
        "<AUTHORIZED_HUMAN>",
        "<BLANK_LAPTOP_DISPLAY_NAME>",
        "<HARNESS_KIND>",
        "<AGENTNET_DOMAIN>",
        "<CORE_HTTPS_ORIGIN>",
        "<APPROVAL_HTTPS_ORIGIN>",
        "<OIDC_ISSUER>",
        "<OIDC_CALLBACK>",
        "<NPM_PACKAGE>",
        "<AGENTNET_VERSION>",
        "<NPM_INTEGRITY>",
        "<NODE_MIN_VERSION>",
        "<UV_MIN_VERSION>",
        "<RETENTION_ABORT_POLICY>",
        "<HUMAN_REPORT_CHANNEL>",
    ):
        assert placeholder in example
    for forbidden in ("BEGIN PRIVATE KEY", "agcap1.", "Authorization: Bearer"):
        assert forbidden not in example
    assert "first_message_blocked_explicit_authority_required" in example
    assert "atomic ten-entitlement plus guard commit" in example
    assert "never assemble authority with generic entitlement issuance" in example
    assert "No other human setup" in example
    assert "no extra approval host" in example
    assert "No relay channel or second person is required" in example
    assert "Do not report or relay principal/harness IDs" in example
    assert "Infisical or other named secret manager" in example
    assert "per-command setup approvals" in example
    assert "Never ask for another command packet, hostname, URL, callback, hash, identifier, config value" in example
    assert "Before any AgentNet install, parse `node --version`" in example
    assert "Node.js 23 or 25 blocks installation despite satisfying the broad npm engine floor" in example
    for removed_placeholder in (
        "<APPROVER_NAME>",
        "<APPROVAL_CODE_CHANNEL>",
        "<ADMINISTRATOR_NAME>",
        "<PRINCIPAL_ID_REPORTING_APPROVED>",
        "<MESSAGING_TEST_IN_SCOPE>",
    ):
        assert removed_placeholder not in example

    scanner_backed_request = json.loads(
        (
            ROOT
            / "skills/agentnet-operator/references/examples/ordinary-server-setup-request.json"
        ).read_text(encoding="utf-8")
    )
    assert scanner_backed_request["schema"] == "agentnet.server-setup.request.v1"
    assert "artifact_mode" not in scanner_backed_request
    assert isinstance(scanner_backed_request["scanner_trust_file"], str)

    communication_only_request = json.loads(
        (
            ROOT
            / "skills/agentnet-operator/references/examples/ordinary-server-communication-only-setup-request.json"
        ).read_text(encoding="utf-8")
    )
    assert communication_only_request["schema"] == "agentnet.server-setup.request.v2"
    assert communication_only_request["artifact_mode"] == "disabled"
    assert "scanner_trust_file" not in communication_only_request

    evals = json.loads(
        (ROOT / "skills/agentnet-operator/evals/evals.json").read_text(encoding="utf-8")
    )
    assert {item["id"] for item in evals} == {
        "fresh-laptop-human-copy-paste-bootstrap",
        "fresh-agent-receives-bootstrap-packet",
        "v018-fresh-laptop-receipt-gap",
        "v019-guided-enrollment-is-identity-only",
        "hub-generates-public-onboarding-packet",
        "fresh-laptop-canonical-single-prompt-is-mandatory",
        "fresh-laptop-messaging-authority-blocked",
        "fresh-laptop-approval-result-is-automatic",
        "fresh-laptop-default-needs-no-extra-approval-host",
        "fresh-laptop-never-requires-infisical",
        "fresh-laptop-one-consolidated-setup-approval",
        "fresh-laptop-human-never-supplies-technical-metadata",
        "fresh-laptop-rejects-eol-node-25-line",
        "fresh-laptop-rejects-eol-node-line",
        "ordinary-server-uses-product-owned-setup",
        "ordinary-server-remote-manager-never-shells",
        "ordinary-server-missing-route-blocks",
        "ordinary-server-tls-environment-apply-blocks-at-launcher",
        "ordinary-server-tls-environment-blocks-before-mutation",
        "ordinary-server-untrusted-public-route-blocks",
        "ordinary-server-request-v2-requires-explicit-artifact-mode",
        "ordinary-server-resumes-exact-request",
        "ordinary-server-rejects-home-runtime",
        "ordinary-server-invalid-broker-blocks-before-mutation",
        "ordinary-server-postgres-peer-block-and-resume",
        "ordinary-server-postgres-first-apply-safe-partial-state",
        "ordinary-server-configured-not-started-resume",
        "ordinary-server-runtime-drift-invalidates-digest",
        "ordinary-server-marker-never-proves-readiness",
        "ordinary-server-communication-only-explicit-v2",
        "ordinary-server-communication-only-rejects-legacy-evidence",
        "ordinary-server-disabled-mode-rejects-null-scanner-field",
        "ordinary-server-enabled-mode-requires-scanner-before-mutation",
        "ordinary-server-human-ceremony-remains-explicit",
        "headless-server-uses-fixed-browser-only-activation",
        "guided-join-terminal-recovery-is-explicit-and-key-preserving",
        "server-reset-is-destructive-manager-only-recovery",
        "fresh-laptop-rejects-three-grant-c0-fallback",
        "c0-success-requires-approved-seven-fact-sequence",
        "repository-candidate-does-not-unblock-installed-release",
        "c0-binding-invalidation-is-terminal",
        "c0-fixed-commands-and-cleanup-only",
        "identity-only-mode-skips-c0-phase",
        "fresh-laptop-rejects-invalid-onboarding-mode",
        "v0132-c0-responder-is-package-owned-and-isolated",
    }
    assert all(item["prompt"] and item["expected_output"] and item["assertions"] for item in evals)

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
        "agentnet server-agent setup",
        "server-agent reset",
        "--retain-external-prerequisites",
        "--confirm-package-state-removal",
        "ordinary-server-setup.md",
        "agentnet network create",
        "--scanner-trust-config",
        "agentnet bootstrap-server-agent",
        "agentnet approval provision",
        "agentnet join begin",
        "--harness pi",
        "--name server-agent-1",
        "--state .agentnet/join-pending.json",
        "agentnet join complete",
        "agentnet message acknowledge",
        "agentnet artifact upload",
        "OIDC discovery is public-only by default",
        "exact JWK thumbprints",
        "approval service readable or controllable by the enrolling harness",
        "operator may separately install that CA into platform system trust visible to CPython or replace the route certificate",
        "`SSL_CERT_FILE`, `SSL_CERT_DIR`, and `SSLKEYLOGFILE` injection is not a supported remediation",
        "Node.js 23 and 25 are unsupported despite the broad npm engine floor",
        "Minimum-floor compatibility uses Node.js 22.19.0 with npm 10.9.3",
        "Release/publish and the Node.js 24.18.0/26.5.0 lanes stay pinned to npm 12.0.1",
        "deployed Hub compatibility is separately reported as Node.js 22.23.2 with npm 11.18.0",
        "Skill-mediated installation must inspect `node --version` before running `npm install`, `npm exec`, or `pi install`",
        "direct npm installation can warn or proceed according to the caller's npm configuration",
    ):
        assert required in commands
    assert "ordinary always-on server ceremony is defined only" in commands
    assert ".agentnet/guided-join.json" not in commands

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
        "fixed unauthenticated Core `/activate`",
        "server-manager-only package recovery",
    ):
        assert required in scope

    boundaries = (
        ROOT / "skills/agentnet-operator/references/fail-closed-boundaries.md"
    ).read_text(encoding="utf-8")
    for required in (
        "workforce OIDC",
        "WebAuthn user verification/OOB approval authenticated independently",
        "does not require Infisical",
        "PostgreSQL 18.4",
        "accepted_local",
        "measured supervisor-launched child",
        "Protected source builds use one exact recipient-owned supervisor sequence",
        "result upload requires the committed release receipt",
        "include_payload",
        "tool and effect authority false",
        "Public remote activation",
        "activation_wrong_account",
        "Destructive server reset",
    ):
        assert required in boundaries
    assert "ordinary-server-setup.md" in boundaries
    assert ".agentnet/server-agent-identity.json" not in boundaries


def test_ordinary_onboarding_stays_one_server_one_owner_one_prompt() -> None:
    requirements = (ROOT / "docs/requirements.md").read_text(encoding="utf-8")
    specification = (ROOT / "docs/specification.md").read_text(encoding="utf-8")
    decisions = (ROOT / "docs/OWNER_DECISIONS.md").read_text(encoding="utf-8")
    guide = (ROOT / "docs/implementation-guide.md").read_text(encoding="utf-8")
    skill = (ROOT / "skills/agentnet-operator/SKILL.md").read_text(encoding="utf-8")
    prompt = (
        ROOT / "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md"
    ).read_text(encoding="utf-8")

    assert "does not require a separate physical approval host" in requirements
    assert "A separately administered approval host is an optional high-assurance profile" in specification
    assert "One owner may act as enrollee, WebAuthn approver, and messaging administrator" in decisions
    assert "Normal onboarding does not require another computer, another person" in guide
    assert "Do not require\nan extra approval host, extra person" in skill
    assert "This entire packet is the only prompt" in prompt
    assert "independent_boundary_proven=false" in prompt
    assert "No relay channel or second person is required" in prompt
    assert "Never ask for another command packet, hostname, URL, callback, hash, identifier, config value" in prompt
    assert "per-command setup approvals" in prompt
    assert "never assemble authority with generic entitlement issuance, three independent grants" in prompt
    assert "Three separate issuance records are expected" not in prompt
    assert "server-side onboarding orchestrator" not in prompt
    for obsolete in (
        "<APPROVER_NAME>",
        "<APPROVAL_CODE_CHANNEL>",
        "<ADMINISTRATOR_NAME>",
        "<PRINCIPAL_ID_REPORTING_APPROVED>",
        "<MESSAGING_TEST_IN_SCOPE>",
    ):
        assert obsolete not in prompt


def test_fresh_laptop_prompt_uses_the_shipped_cli_surface() -> None:
    from agentnet.cli import build_parser

    example = (
        ROOT
        / "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md"
    ).read_text(encoding="utf-8")
    parser = build_parser()
    guided_arguments = [
        "join",
        "guided",
        "--server",
        "https://agentnet.example",
        "--domain",
        "corp.example",
        "--harness",
        "pi",
        "--name",
        "Fresh laptop",
        "--state",
        ".agentnet/guided-join.json",
        "--identity",
        ".agentnet/identity.json",
        "--timeout",
        "600",
    ]
    parser.parse_args(guided_arguments)
    for arguments in (
        ["bootstrap-plan", "begin", "--identity", ".agentnet/identity.json", "--state", ".agentnet/bootstrap-plan-state.json"],
        ["bootstrap-plan", "status", "--identity", ".agentnet/identity.json", "--state", ".agentnet/bootstrap-plan-state.json"],
        ["bootstrap-plan", "complete", "--identity", ".agentnet/identity.json", "--state", ".agentnet/bootstrap-plan-state.json"],
        ["c0-pilot", "start", "--identity", ".agentnet/identity.json"],
        ["c0-pilot", "status", "--identity", ".agentnet/identity.json"],
        ["c0-pilot", "complete", "--identity", ".agentnet/identity.json"],
    ):
        parser.parse_args(arguments)
    assert "agentnet join guided" in example
    assert "agentnet bootstrap-plan begin" in example
    assert "agentnet c0-pilot complete" in example
    for forbidden_fragment in (
        "agentnet authority inventory",
        "agentnet message send",
        "agentnet message inbox",
        "agentnet message acknowledge",
    ):
        assert forbidden_fragment not in example


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
        "AGENTNET_NODE_EXECUTABLE: nodeExecutable",
        "AGENTNET_SYSTEMCTL: systemctlExecutable",
        "AGENTNET_USERADD: useraddExecutable",
        "PYTHONDONTWRITEBYTECODE",
        "PYTHONPYCACHEPREFIX",
        "privilegedSetupApply",
        "unsupportedTlsEnvironment",
        "Object.hasOwn(process.env, name)",
        'from "../lib/server-setup-preflight.mjs"',
        "privilegedApprovalDigest(userArguments, digestEnvironment)",
        "requireRootOwnedPath(packageRoot, { recursive: true })",
        "info.isDirectory() ? 0o001 : 0o005",
        'const setupRoot = "/var/lib/agentnet-setup"',
        "const inheritedEnvironment = privilegedSetupApply",
        'const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"',
        "PATH: systemPath",
        'createHash("sha256")',
        "realpathSync",
        'shell: false',
        '"3.13.13"',
    ):
        assert required in launcher
    assert launcher.index("unsupportedTlsEnvironment") < launcher.index("const systemPath")
    assert launcher.index("privilegedApprovalDigest(userArguments, digestEnvironment)") < launcher.index(
        'const setupRoot = "/var/lib/agentnet-setup"'
    )
    assert '">=3.13,<3.15"' not in launcher
    assert 'process.platform !== "linux"' not in launcher
    assert "platformStateRoot" in launcher
    assert "supportedPlatform" in launcher
    assert "curl" not in launcher
    assert "postinstall" not in json.loads(
        (ROOT / "package.json").read_text(encoding="utf-8")
    )["scripts"]
    preflight = (ROOT / "npm/lib/server-setup-preflight.mjs").read_text(encoding="utf-8")
    for required in (
        "stableExecutableSha256",
        "setup runtime executable changed during preflight",
        "stablePackageTreeSha256",
        "agentnet.package-tree-content.v1",
        "maximumRecords = 20_000",
        "maximumBytes = 536_870_912n",
        "package_tree_sha256",
        "systemctl_executable",
        "useradd_executable",
    ):
        assert required in preflight


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_npm_launcher_rejects_unsupported_uv_before_runtime_creation(tmp_path: Path) -> None:
    package_root = tmp_path / "package"
    launcher = package_root / "npm/bin/agentnet.mjs"
    platform_helper = package_root / "npm/lib/platform.mjs"
    preflight_helper = package_root / "npm/lib/server-setup-preflight.mjs"
    launcher.parent.mkdir(parents=True)
    platform_helper.parent.mkdir(parents=True)
    shutil.copy2(ROOT / "npm/bin/agentnet.mjs", launcher)
    shutil.copy2(ROOT / "npm/lib/platform.mjs", platform_helper)
    shutil.copy2(ROOT / "npm/lib/server-setup-preflight.mjs", preflight_helper)
    (package_root / "package.json").write_text(
        json.dumps({"version": "0.1.8"}),
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


@pytest.mark.skipif(
    shutil.which("node") is None or sys.platform != "linux",
    reason="setup apply launcher guard requires Linux and Node.js",
)
@pytest.mark.parametrize("variable", ["SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"])
def test_npm_launcher_rejects_tls_environment_before_setup_work(
    tmp_path: Path,
    variable: str,
) -> None:
    state_root = tmp_path / "state"
    private_value = str(tmp_path / "private-tls-state")
    environment = os.environ.copy()
    for name in ("SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"):
        environment.pop(name, None)
    environment.update(
        {
            variable: private_value,
            "AGENTNET_UV": str(tmp_path / "must-not-run-uv"),
            "XDG_STATE_HOME": str(state_root),
        }
    )

    completed = subprocess.run(
        [
            "node",
            str(ROOT / "npm/bin/agentnet.mjs"),
            "server-agent",
            "setup",
            "--request",
            str(tmp_path / "missing-request.json"),
            "--apply",
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
        timeout=30,
    )

    assert completed.returncode == 1
    assert completed.stderr == (
        "AgentNet setup rejects ambient TLS trust and key-log configuration.\n"
    )
    assert private_value not in completed.stdout + completed.stderr
    assert "must-not-run-uv" not in completed.stdout + completed.stderr
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
        "evidence/local/2026-08-01-v0.1.35/artifacts/RETENTION.md",
        "skills/agentnet-operator/SKILL.md",
        "skills/agentnet-operator/references/safe-commands.md",
        "skills/agentnet-operator/references/fail-closed-boundaries.md",
        "skills/agentnet-operator/references/required-communication-scope.md",
        "skills/agentnet-operator/references/fresh-laptop-onboarding.md",
        "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md",
        "tests/fixtures/bootstrap_plan_golden_vector.json",
    } <= filenames
    assert ".gitignore" not in filenames
    assert ".npmignore" not in filenames


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
        platform_helper = package_root / "npm/lib/platform.mjs"
        preflight_helper = package_root / "npm/lib/server-setup-preflight.mjs"
        launcher.parent.mkdir(parents=True)
        platform_helper.parent.mkdir(parents=True)
        shutil.copy2(ROOT / "npm/bin/agentnet.mjs", launcher)
        shutil.copy2(ROOT / "npm/lib/platform.mjs", platform_helper)
        shutil.copy2(ROOT / "npm/lib/server-setup-preflight.mjs", preflight_helper)
        (package_root / "package.json").write_text(
            json.dumps({"version": package_version}),
            encoding="utf-8",
        )
        capture = tmp_path / f"{name}.json"
        environment = os.environ.copy()
        environment.pop("AGENTNET_NPM_RUNTIME_DIR", None)
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
