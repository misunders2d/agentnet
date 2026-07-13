from __future__ import annotations

import json
import os
from dataclasses import replace
from pathlib import Path

import pytest

from agentnet.adapters.auth import (
    EphemeralBrokerEnvironment,
    PreprovisionedPrivateAuth,
)
from agentnet.adapters.specs import build_launch_spec
from agentnet.errors import GateBlocked


AMBIENT_KEYS = {
    "AGENT_DECK_TOKEN",
    "ANTHROPIC_API_KEY",
    "AWS_SECRET_ACCESS_KEY",
    "CLAUDE_CONFIG_DIR",
    "GITHUB_TOKEN",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "MCP_CONFIG",
    "OPENAI_API_KEY",
    "SSH_AUTH_SOCK",
}


def _fixture_entries(state_dir: Path) -> list[dict[str, object]]:
    return [
        json.loads(line)
        for line in (state_dir / "native-fixture.log").read_text(encoding="utf-8").splitlines()
    ]


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_clean_worker_inherits_no_user_home_workspace_proxy_plugin_or_secret_environment(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
    monkeypatch,
    harness: str,
) -> None:
    for name in AMBIENT_KEYS:
        monkeypatch.setenv(name, f"ambient-canary-{name.lower()}")
    user_home = tmp_path / "ambient-home"
    user_home.mkdir(mode=0o700)
    (user_home / "AGENTS.md").write_text("hostile inherited instructions", encoding="utf-8")
    monkeypatch.setenv("HOME", str(user_home))

    spec = build_launch_spec(
        harness,
        harness_id=f"clean-boundary-{harness}",
        root=tmp_path / "private-runtime",
        executable=fake_harnesses[harness],
    )
    # A broker-less capability keeps this contract in the networkless sandbox
    # profile while still proving no ambient provider credential is inherited.
    if harness in {"claude", "codex"}:
        allowed_name = "ANTHROPIC_API_KEY" if harness == "claude" else "OPENAI_API_KEY"
        auth = EphemeralBrokerEnvironment(harness, {allowed_name: "explicit-worker-capability"})
    else:
        private_auth = tmp_path / f"{harness}-private-auth"
        private_auth.mkdir(mode=0o700)
        private_file = private_auth / "auth.json"
        private_file.write_text('{"fixture":"private"}\n', encoding="utf-8")
        os.chmod(private_file, 0o600)
        auth = PreprovisionedPrivateAuth(harness, private_auth)
    runtime = contract_clean_runtime_factory(spec, auth, request_timeout_seconds=1)
    try:
        runtime.start()
        runtime.submit("synthetic boundary probe", explicit=True)
        entries = _fixture_entries(runtime.spec.state_dir)
        assert entries
        for entry in entries:
            keys = set(entry["environment_keys"])
            assert not (AMBIENT_KEYS - set(auth.environment_names)) & keys
            assert entry["cwd"] == str(runtime.spec.work_dir)
            bindings = entry["environment_bindings"]
            assert bindings["HOME"] == str(runtime.spec.home_dir)
            assert bindings["CODEX_HOME"] == str(runtime.spec.state_dir / "codex")
            assert bindings["PI_CODING_AGENT_DIR"] == str(runtime.spec.state_dir / "pi")
            assert bindings["TMPDIR"] == str(runtime.spec.temp_dir)
            assert bindings["XDG_CONFIG_HOME"] == str(runtime.spec.state_dir / "xdg-config")
        serialized = json.dumps(entries, sort_keys=True)
        assert "hostile inherited instructions" not in serialized
        assert str(user_home) not in serialized
        wrapper = next(entry for entry in entries if entry["kind"] == "sandbox_wrapper")
        arguments = wrapper["value"]["argv"]
        assert "--unshare-all" in arguments
        assert "--share-net" not in arguments
        assert "--broker-origin" not in arguments
    finally:
        runtime.stop()


@pytest.mark.parametrize("poison", ["AGENTS.md", "CLAUDE.md", ".mcp.json", ".codex-plugin"])
def test_workspace_seed_after_signed_admission_is_rejected_before_process_start(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
    poison: str,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id=f"poison-{poison}",
        root=tmp_path / "runtime",
        executable=fake_harnesses["codex"],
    )
    runtime = contract_clean_runtime_factory(
        spec,
        EphemeralBrokerEnvironment("codex", {"OPENAI_API_KEY": "local-capability"}),
        request_timeout_seconds=1,
    )
    target = runtime.spec.work_dir / poison
    if poison.startswith(".") and "." not in poison[1:]:
        target.mkdir(mode=0o700)
    else:
        target.write_text("ambient instructions", encoding="utf-8")
    with pytest.raises(GateBlocked, match="workspace is not empty"):
        runtime.start()
    assert runtime.status().phase == "offline"
    runtime.stop()


def test_launch_directory_cannot_escape_private_root(tmp_path: Path, fake_harnesses) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="escaped-workspace",
        root=tmp_path / "runtime",
        executable=fake_harnesses["codex"],
    )
    outside = tmp_path / "outside"
    outside.mkdir(mode=0o700)
    with pytest.raises(ValueError, match="inside the private root"):
        replace(spec, work_dir=outside).validate()


def test_broker_origin_and_auth_are_bound_after_admission(
    tmp_path: Path,
    fake_harnesses,
    contract_clean_runtime_factory,
) -> None:
    spec = build_launch_spec(
        "codex",
        harness_id="broker-origin-binding",
        root=tmp_path / "runtime",
        executable=fake_harnesses["codex"],
    )
    runtime = contract_clean_runtime_factory(
        spec,
        EphemeralBrokerEnvironment(
            "codex",
            {
                "OPENAI_API_KEY": "exact-capability",
                "OPENAI_BASE_URL": "http://127.0.0.1:18090",
            },
        ),
    )
    changed = EphemeralBrokerEnvironment(
        "codex",
        {
            "OPENAI_API_KEY": "different-capability",
            "OPENAI_BASE_URL": "http://127.0.0.1:18091",
        },
    )
    admission = runtime._clean_worker_admission
    assert admission is not None
    with pytest.raises(GateBlocked, match="authentication or executable binding changed"):
        admission.validate_runtime(runtime.spec, changed, runtime.spec.executable)
    runtime.stop()


def test_each_native_profile_explicitly_disables_ambient_extension_surfaces(
    tmp_path: Path,
    fake_harnesses,
) -> None:
    specs = {
        harness: build_launch_spec(
            harness,
            harness_id=f"surface-{harness}",
            root=tmp_path / "runtime",
            executable=fake_harnesses[harness],
        )
        for harness in ("claude", "codex", "pi", "antigravity")
    }
    claude = specs["claude"].arguments
    assert {"--strict-mcp-config", "--bare", "--disable-slash-commands", "--no-chrome"} <= set(claude)
    assert claude[claude.index("--tools") + 1] == ""
    codex = specs["codex"].arguments
    assert "--strict-config" in codex
    assert "shell_environment_policy.inherit=none" in codex
    pi = specs["pi"].arguments
    assert {
        "--no-tools",
        "--no-extensions",
        "--no-skills",
        "--no-prompt-templates",
        "--no-context-files",
        "--no-themes",
        "--offline",
    } <= set(pi)
    antigravity = specs["antigravity"].arguments
    assert "--sandbox" in antigravity
    assert "--conversation" in antigravity
