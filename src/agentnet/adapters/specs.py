"""Version-pinned executable specs for the five supported background paths."""

from __future__ import annotations

import json
import os
import re
import shutil
import stat
import subprocess
import sys
from dataclasses import dataclass
from hashlib import sha256
from pathlib import Path
from typing import TYPE_CHECKING
from uuid import NAMESPACE_URL, uuid5

from agentnet.adapters.base import AdapterLaunchSpec, ExecutableProbe, HarnessKind
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES
from agentnet.security.signatures import canonical_json

if TYPE_CHECKING:
    from agentnet.bindings.endpoint import EndpointBinding


PINNED_VERSIONS: dict[HarnessKind, str] = {
    "omp": "17.2.9",
    "claude": "2.1.216",
    "codex": "0.144.6",
    "pi": "0.81.1",
    "antigravity": "1.1.5",
}

EXECUTABLE_NAMES: dict[HarnessKind, str] = {
    "omp": "omp",
    "claude": "claude",
    "codex": "codex",
    "pi": "pi",
    "antigravity": "agy",
}

VERSION_ARGUMENTS: dict[HarnessKind, tuple[str, ...]] = {
    "omp": ("--version",),
    "claude": ("--version",),
    "codex": ("--version",),
    "pi": ("--version",),
    "antigravity": ("--version",),
}

_VERSION = re.compile(r"(?<![0-9])([0-9]+\.[0-9]+\.[0-9]+)(?![0-9])")

@dataclass(frozen=True, slots=True)
class EndpointAdapterLaunchSpec(AdapterLaunchSpec):
    """Launch spec carrying only a locator for one sealed exact-endpoint binding."""

    endpoint_descriptor_path: Path | None = None
    adapter_generation: int | None = None
    credential_epoch: int | None = None
    capability_root_path: Path | None = None
    profile_key: str | None = None
    canonical_tool_names: tuple[str, ...] = ()

    def validate(self) -> None:
        AdapterLaunchSpec.validate(self)
        binding_fields = (
            self.endpoint_descriptor_path,
            self.adapter_generation,
            self.credential_epoch,
            self.capability_root_path,
            self.profile_key,
        )
        if self.local_binding_enabled:
            if any(value is None for value in binding_fields):
                raise ValueError("local binding launch requires one exact endpoint descriptor")
            if self.canonical_tool_names != CANONICAL_TOOL_NAMES:
                raise ValueError("local binding launch must expose the complete canonical tool surface")
        elif any(value is not None for value in binding_fields) or self.canonical_tool_names:
            raise ValueError("probe-only launch cannot carry an AgentNet endpoint binding")


def _endpoint_scope(binding: EndpointBinding) -> str:
    return sha256(
        (
            f"{binding.domain_id}\0{binding.harness_id}\0"
            f"{binding.adapter_generation}"
        ).encode("utf-8")
    ).hexdigest()

def _require_private_capability_root(path: Path, binding: EndpointBinding) -> Path:
    try:
        resolved = path.resolve(strict=True)
        metadata = path.lstat()
        parent = path.parent.lstat()
        if (
            path.name != "capability-root.key"
            or path.parent.name != _endpoint_scope(binding)
        ):
            raise ValueError("endpoint capability root is not generation-specific")
        if os.name == "nt":
            from agentnet.windows_security import require_private_path

            require_private_path(path.parent, directory=True)
            require_private_path(path, directory=False)
            return resolved
    except OSError as exc:
        raise ValueError("endpoint capability root must be an owner-only real path") from exc
    if (
        not path.is_absolute()
        or path.is_symlink()
        or path.parent.is_symlink()
        or resolved != path
        or not stat.S_ISREG(metadata.st_mode)
        or not stat.S_ISDIR(parent.st_mode)
        or metadata.st_uid != os.geteuid()
        or parent.st_uid != os.geteuid()
        or metadata.st_mode & 0o077
        or parent.st_mode & 0o077
        or metadata.st_nlink != 1
    ):
        raise ValueError("endpoint capability root must be an owner-only real path")
    return resolved


def _binding_descriptor(binding: EndpointBinding, capability_root: Path) -> dict[str, object]:
    return {
        "adapter_generation": binding.adapter_generation,
        "capability_root_path": str(capability_root),
        "credential_epoch": binding.credential_epoch,
        "credential_id": binding.credential_id,
        "domain_id": binding.domain_id,
        "harness_id": binding.harness_id,
        "harness_kind": binding.harness_kind,
        "mailbox_cursor": binding.mailbox_cursor,
        "principal_id": binding.principal_id,
        "process_measurement": binding.process_measurement,
        "profile_key": binding.profile_key,
        "refresh_behavior": "restart_required",
        "schema": "agentnet.endpoint-launch-descriptor.v1",
    }


def _atomic_private_descriptor(path: Path, value: dict[str, object]) -> None:
    content = canonical_json(value)
    if os.name == "nt":
        from agentnet.windows_security import read_private_file, write_private_file

        if os.path.lexists(path):
            if read_private_file(path, max_bytes=65_536) != content:
                raise ValueError("stale endpoint descriptor conflicts with the exact binding")
            return
        write_private_file(path, content)
        return
    temporary = path.with_suffix(path.suffix + ".tmp")
    if os.path.lexists(temporary):
        raise ValueError("stale endpoint descriptor temporary path detected")
    if os.path.lexists(path):
        metadata = path.lstat()
        if (
            path.is_symlink()
            or not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or path.read_bytes() != content
        ):
            raise ValueError("stale endpoint descriptor conflicts with the exact binding")
        return
    temporary.write_bytes(content)
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)


def _private_directory(path: Path) -> None:
    if os.name == "nt":
        from agentnet.windows_security import ensure_private_directory

        ensure_private_directory(path)
        return
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink():
        raise ValueError("adapter state directory cannot be a symbolic link")
    os.chmod(path, 0o700)


def _atomic_public_config(path: Path, content: str) -> None:
    if os.name == "nt":
        from agentnet.windows_security import write_private_file

        write_private_file(path, content.encode("utf-8"), force=True)
        return
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(content, encoding="utf-8")
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)


def _session_paths(
    root: Path,
    harness: HarnessKind,
    harness_id: str,
    endpoint_binding: EndpointBinding | None,
) -> tuple[str, tuple[Path, ...]]:
    opaque_harness = sha256(harness_id.encode("utf-8")).hexdigest()[:20]
    session_id = str(uuid5(NAMESPACE_URL, f"agentnet-background:{harness}:{harness_id}"))
    if endpoint_binding is None:
        session_root = (root.resolve() / harness / opaque_harness).resolve()
    else:
        opaque_endpoint = _endpoint_scope(endpoint_binding)
        session_root = (root.resolve() / "endpoints" / opaque_endpoint).resolve()
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
    endpoint_binding: EndpointBinding | None = None,
) -> EndpointAdapterLaunchSpec:
    """Build one isolated exact-endpoint launch or one tool-free executable probe."""

    if harness not in PINNED_VERSIONS:
        raise ValueError("unknown built-in harness; use a conforming adapter provider")
    if not harness_id or len(harness_id) > 256:
        raise ValueError("harness identifier is outside the launch profile")
    if local_bindings and endpoint_binding is None:
        raise ValueError("local binding launch requires an exact endpoint binding")
    if not local_bindings and endpoint_binding is not None:
        raise ValueError("endpoint binding is only valid for a local binding launch")
    capability_root: Path | None = None
    if endpoint_binding is not None:
        if (
            endpoint_binding.harness_kind != harness
            or endpoint_binding.harness_id != harness_id
        ):
            raise ValueError("endpoint binding does not match the exact launch harness")
        if (
            not isinstance(endpoint_binding.domain_id, str)
            or not endpoint_binding.domain_id
            or not isinstance(endpoint_binding.principal_id, str)
            or not endpoint_binding.principal_id
            or not isinstance(endpoint_binding.credential_id, str)
            or not endpoint_binding.credential_id
            or not isinstance(endpoint_binding.profile_key, str)
            or not endpoint_binding.profile_key.strip()
            or len(endpoint_binding.profile_key) > 256
            or not isinstance(endpoint_binding.process_measurement, str)
            or not endpoint_binding.process_measurement
            or type(endpoint_binding.credential_epoch) is not int
            or endpoint_binding.credential_epoch < 1
            or type(endpoint_binding.adapter_generation) is not int
            or endpoint_binding.adapter_generation < 1
            or type(endpoint_binding.mailbox_cursor) is not int
            or endpoint_binding.mailbox_cursor < 0
        ):
            raise ValueError("endpoint binding identity, generation, or cursor is invalid")
        capability_root = _require_private_capability_root(
            endpoint_binding.capability_root_path,
            endpoint_binding,
        )
    session_id, paths = _session_paths(root, harness, harness_id, endpoint_binding)
    session_root, home, work, state, temp = paths
    descriptor_path: Path | None = None
    if endpoint_binding is not None:
        assert capability_root is not None
        descriptor_path = state / "endpoint-binding-descriptor.json"
        _atomic_private_descriptor(
            descriptor_path,
            _binding_descriptor(endpoint_binding, capability_root),
        )
    selected_executable = executable or EXECUTABLE_NAMES[harness]
    proxy_command = (sys.executable, "-m", "agentnet.bindings.mcp_proxy")
    adapter_tool_names = tuple(name.replace(".", "_") for name in CANONICAL_TOOL_NAMES)

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
        claude_tools = (
            ",".join(f"mcp__agentnet__{name}" for name in adapter_tool_names)
            if local_bindings
            else ""
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
            claude_tools,
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
                "mcp_servers.agentnet.enabled_tools="
                + json.dumps(adapter_tool_names, separators=(",", ":")),
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
    elif harness in {"omp", "pi"}:
        sessions = state / "sessions"
        _private_directory(sessions)
        extension = Path(__file__).resolve().parents[1] / "bindings" / "pi_extension.ts"
        binding_arguments = (
            "--extension",
            str(extension),
            *(("--no-builtin-tools",) if harness == "pi" else ()),
            "--tools",
            ",".join(adapter_tool_names),
        ) if local_bindings else ("--no-tools",)
        if harness == "omp":
            assert endpoint_binding is not None or not local_bindings
            omp_profile = (
                ("--profile", endpoint_binding.profile_key)
                if endpoint_binding is not None
                else ()
            )
            arguments = (
                *omp_profile,
                "--mode",
                "rpc",
                "--session-dir",
                str(sessions),
                *binding_arguments,
                "--no-extensions",
                "--no-skills",
                "--no-rules",
                "--no-lsp",
                "--no-pty",
            )
        else:
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
        transport = "omp_rpc_jsonl" if harness == "omp" else "pi_rpc_jsonl"
        persistent = True
    elif harness == "antigravity":
        gemini_config = home / ".gemini"
        _private_directory(gemini_config)
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
            gemini_config / "settings.json",
            json.dumps({"mcpServers": mcp_servers}, separators=(",", ":"), sort_keys=True)
            + "\n",
        )
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

    spec = EndpointAdapterLaunchSpec(
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
        endpoint_descriptor_path=descriptor_path,
        adapter_generation=(
            endpoint_binding.adapter_generation if endpoint_binding is not None else None
        ),
        credential_epoch=(
            endpoint_binding.credential_epoch if endpoint_binding is not None else None
        ),
        capability_root_path=capability_root,
        profile_key=endpoint_binding.profile_key if endpoint_binding is not None else None,
        canonical_tool_names=CANONICAL_TOOL_NAMES if local_bindings else (),
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
    harnesses: tuple[HarnessKind, ...] = ("omp", "pi", "claude", "codex", "antigravity"),
) -> dict[HarnessKind, ExecutableProbe]:
    """Detect the requested local binaries without claiming gate evidence."""

    result: dict[HarnessKind, ExecutableProbe] = {}
    for harness in harnesses:
        spec = build_launch_spec(harness, harness_id=f"probe-{harness}", root=root)
        result[harness] = detect_executable(spec)
    return result
