"""Version-pinned executable specs for the four supported background paths."""

from __future__ import annotations

import os
import json
import re
import shutil
import subprocess
import sys
from hashlib import sha256
from pathlib import Path
from uuid import NAMESPACE_URL, uuid5

from agentnet.adapters.base import AdapterLaunchSpec, ExecutableProbe, HarnessKind


PINNED_VERSIONS: dict[HarnessKind, str] = {
    "claude": "2.1.212",
    "codex": "0.144.5",
    "pi": "0.80.10",
    "antigravity": "1.1.3",
}

EXECUTABLE_NAMES: dict[HarnessKind, str] = {
    "claude": "claude",
    "codex": "codex",
    "pi": "pi",
    "antigravity": "agy",
}

VERSION_ARGUMENTS: dict[HarnessKind, tuple[str, ...]] = {
    "claude": ("--version",),
    "codex": ("--version",),
    "pi": ("--version",),
    "antigravity": ("--version",),
}

_VERSION = re.compile(r"(?<![0-9])([0-9]+\.[0-9]+\.[0-9]+)(?![0-9])")


def _private_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink():
        raise ValueError("adapter state directory cannot be a symbolic link")
    os.chmod(path, 0o700)


def _atomic_public_config(path: Path, content: str) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(content, encoding="utf-8")
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)


def _session_paths(root: Path, harness: HarnessKind, harness_id: str) -> tuple[str, tuple[Path, ...]]:
    opaque_harness = sha256(harness_id.encode("utf-8")).hexdigest()[:20]
    session_id = str(uuid5(NAMESPACE_URL, f"agentnet-background:{harness}:{harness_id}"))
    session_root = (root.resolve() / harness / opaque_harness).resolve()
    home = session_root / "home"
    work = session_root / "work"
    state = session_root / "state"
    temp = session_root / "tmp"
    for directory in (session_root, home, work, state, temp):
        _private_directory(directory)
    # Native clients must never fall back to a caller's ambient XDG/Codex/Pi
    # directories merely because an isolated target did not exist yet.  Create
    # every directory referenced by the runtime's allowlisted environment
    # before the subprocess is started.
    for directory in (
        state / "codex",
        state / "pi",
        state / "xdg-cache",
        state / "xdg-config",
        state / "xdg-data",
    ):
        _private_directory(directory)
    return session_id, (session_root, home, work, state, temp)


def build_launch_spec(
    harness: HarnessKind,
    *,
    harness_id: str,
    root: Path,
    executable: str | None = None,
    local_bindings: bool = False,
) -> AdapterLaunchSpec:
    """Build one dedicated session without importing any user configuration.

    Executable detection cannot prove clean semantic isolation.  These native
    specs therefore remain deterministic-only; the separately signed clean
    worker admission path must construct any future semantic worker.
    """

    if harness not in PINNED_VERSIONS:
        raise ValueError("unknown built-in harness; use a conforming adapter provider")
    if not harness_id or len(harness_id) > 256:
        raise ValueError("harness identifier is outside the launch profile")
    session_id, paths = _session_paths(root, harness, harness_id)
    session_root, home, work, state, temp = paths
    selected_executable = executable or EXECUTABLE_NAMES[harness]
    proxy_command = (sys.executable, "-m", "agentnet.bindings.mcp_proxy")

    if harness == "claude":
        mcp_config = state / "mcp.json"
        settings = state / "settings.json"
        mcp_servers = (
            {
                "agentnet": {
                    "command": proxy_command[0],
                    "args": list(proxy_command[1:]),
                }
            }
            if local_bindings
            else {}
        )
        _atomic_public_config(
            mcp_config,
            json.dumps({"mcpServers": mcp_servers}, separators=(",", ":"), sort_keys=True)
            + "\n",
        )
        _atomic_public_config(
            settings,
            '{"enabledPlugins":{},"permissions":{"defaultMode":"dontAsk"}}\n',
        )
        arguments = (
            "--print",
            "--input-format",
            "stream-json",
            "--output-format",
            "stream-json",
            "--session-id",
            session_id,
            "--name",
            f"agentnet-background-{session_id[:8]}",
            "--strict-mcp-config",
            "--mcp-config",
            str(mcp_config),
            "--settings",
            str(settings),
            "--tools",
            "",
            "--permission-mode",
            "dontAsk",
            "--no-chrome",
            "--disable-slash-commands",
            "--bare",
            "--verbose",
        )
        transport = "claude_stream_json"
        persistent = True
    elif harness == "codex":
        codex_binding_arguments = (
            (
                "-c",
                f'mcp_servers.agentnet.command="{proxy_command[0]}"',
                "-c",
                'mcp_servers.agentnet.args=["-m","agentnet.bindings.mcp_proxy"]',
                "-c",
                "mcp_servers.agentnet.required=true",
                "-c",
                'mcp_servers.agentnet.enabled_tools=["agentnet_inbox","agentnet_inbox_acknowledge","agentnet_send"]',
            )
            if local_bindings
            else ()
        )
        arguments = (
            "app-server",
            "--stdio",
            "--strict-config",
            "-c",
            "analytics.enabled=false",
            "-c",
            "shell_environment_policy.inherit=none",
            "-c",
            'model="gpt-5.6-sol"',
            "-c",
            'model_reasoning_effort="ultra"',
            *codex_binding_arguments,
        )
        transport = "codex_app_server"
        persistent = True
    elif harness == "pi":
        sessions = state / "sessions"
        _private_directory(sessions)
        extension = Path(__file__).resolve().parents[1] / "bindings" / "pi_extension.ts"
        binding_arguments = (
            "--extension",
            str(extension),
            "--no-builtin-tools",
            "--tools",
            "agentnet_inbox,agentnet_inbox_acknowledge,agentnet_send",
        ) if local_bindings else ("--no-tools",)
        arguments = (
            "--mode",
            "rpc",
            "--session-dir",
            str(sessions),
            "--session-id",
            session_id,
            "--name",
            f"agentnet-background-{session_id[:8]}",
            *binding_arguments,
            "--no-extensions",
            "--no-skills",
            "--no-prompt-templates",
            "--no-context-files",
            "--no-themes",
            "--offline",
        )
        transport = "pi_rpc_jsonl"
        persistent = True
    elif harness == "antigravity":
        arguments = (
            "--print",
            "--conversation",
            session_id,
            "--sandbox",
            "--print-timeout",
            "30s",
            "--log-file",
            str(state / "antigravity.log"),
        )
        transport = "antigravity_print"
        persistent = False
    else:
        raise ValueError("unknown built-in harness; use a conforming adapter provider")

    spec = AdapterLaunchSpec(
        harness=harness,
        harness_id=harness_id,
        executable=selected_executable,
        pinned_version=PINNED_VERSIONS[harness],
        version_arguments=VERSION_ARGUMENTS[harness],
        arguments=arguments,
        transport=transport,
        persistent_process=persistent,
        session_id=session_id,
        root_dir=session_root,
        home_dir=home,
        work_dir=work,
        state_dir=state,
        temp_dir=temp,
        model="gpt-5.6-sol" if harness == "codex" else None,
        reasoning_effort="ultra" if harness == "codex" else None,
        semantic_mode="deterministic_only",
        local_binding_enabled=local_bindings,
    )
    spec.validate()
    return spec


def detect_executable(spec: AdapterLaunchSpec, *, timeout_seconds: float = 2.0) -> ExecutableProbe:
    """Perform a bounded local version probe, never external certification."""

    if timeout_seconds <= 0 or timeout_seconds > 10:
        raise ValueError("executable probe timeout is outside the bounded profile")
    candidate = spec.executable
    resolved = candidate if os.path.isabs(candidate) and os.access(candidate, os.X_OK) else shutil.which(candidate)
    if resolved is None:
        return ExecutableProbe(
            harness=spec.harness,
            executable=candidate,
            pinned_version=spec.pinned_version,
            resolved_path=None,
            reported_version=None,
            matches_pin=False,
            exit_code=None,
            error="absent",
        )
    environment = {
        "HOME": str(spec.home_dir),
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "NO_COLOR": "1",
        "PATH": "/usr/local/bin:/usr/bin:/bin",
        "PYTHONNOUSERSITE": "1",
        "TMPDIR": str(spec.temp_dir),
    }
    try:
        completed = subprocess.run(
            (resolved, *spec.version_arguments),
            cwd=spec.work_dir,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=timeout_seconds,
            check=False,
            close_fds=True,
        )
    except subprocess.TimeoutExpired:
        return ExecutableProbe(
            harness=spec.harness,
            executable=candidate,
            pinned_version=spec.pinned_version,
            resolved_path=resolved,
            reported_version=None,
            matches_pin=False,
            exit_code=None,
            error="timeout",
        )
    match = _VERSION.search(completed.stdout[:4096])
    reported = match.group(1) if match else None
    error = None
    if completed.returncode != 0:
        error = "nonzero"
    elif reported is None:
        error = "unparseable"
    return ExecutableProbe(
        harness=spec.harness,
        executable=candidate,
        pinned_version=spec.pinned_version,
        resolved_path=resolved,
        reported_version=reported,
        matches_pin=completed.returncode == 0 and reported == spec.pinned_version,
        exit_code=completed.returncode,
        error=error,
    )


def detect_installed_harnesses(
    root: Path,
    *,
    harnesses: tuple[HarnessKind, ...] = ("claude", "codex", "pi", "antigravity"),
) -> dict[HarnessKind, ExecutableProbe]:
    """Detect the requested local binaries without claiming gate evidence."""

    result: dict[HarnessKind, ExecutableProbe] = {}
    for harness in harnesses:
        spec = build_launch_spec(harness, harness_id=f"probe-{harness}", root=root)
        result[harness] = detect_executable(spec)
    return result
