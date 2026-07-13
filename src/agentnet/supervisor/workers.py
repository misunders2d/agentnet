"""Dedicated clean-worker launch policy; no active-session fallback."""

from __future__ import annotations

import os
import hashlib
import json
import shutil
import subprocess
import tempfile
import time
from collections.abc import Mapping
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any, Literal

from agentnet.adapters.auth import HarnessAuthInjection
from agentnet.adapters.base import AdapterLaunchSpec, HarnessKind
from agentnet.errors import GateBlocked
from agentnet.security.signatures import canonical_digest, verify_signature


@dataclass(frozen=True, slots=True)
class WorkerSpec:
    harness_id: str
    harness_kind: Literal["claude", "codex", "pi", "antigravity"]
    executable: str
    arguments: tuple[str, ...]
    semantic: bool = False
    model_endpoint: str | None = None


def adapter_launch_profile_digest(spec: AdapterLaunchSpec) -> str:
    """Stable digest for a versioned native profile without per-session paths."""

    root = str(spec.root_dir)
    normalized_arguments = [
        argument.replace(root, "{PRIVATE_ROOT}")
        .replace(spec.session_id, "{SESSION_ID}")
        .replace(spec.session_id[:8], "{SESSION_PREFIX}")
        for argument in spec.arguments
    ]
    return canonical_digest(
        {
            "harness": spec.harness,
            "pinned_version": spec.pinned_version,
            "transport": spec.transport,
            "persistent_process": spec.persistent_process,
            "model": spec.model,
            "reasoning_effort": spec.reasoning_effort,
            "arguments": normalized_arguments,
        }
    )


def semantic_adapter_spec(
    spec: AdapterLaunchSpec,
    *,
    executable: str | None = None,
    broker_enabled: bool,
    broker_origin: str | None = None,
) -> AdapterLaunchSpec:
    """Derive the exact admitted profile without weakening deterministic mode."""

    arguments = spec.arguments
    if broker_enabled and spec.harness == "pi":
        arguments = tuple(argument for argument in arguments if argument != "--offline")
    elif broker_enabled and spec.harness == "codex":
        if broker_origin is None:
            raise GateBlocked("G03", "Codex broker admission omitted its exact origin")
        arguments = (
            *arguments,
            "-c",
            f"openai_base_url={json.dumps(broker_origin)}",
        )
    return replace(
        spec,
        executable=executable or spec.executable,
        arguments=arguments,
        semantic_mode="clean_worker",
    )


@dataclass(frozen=True, slots=True)
class CleanWorkerAdmission:
    """Signed admission bound to one exact private native launch instance."""

    spec: AdapterLaunchSpec
    executable_path: str
    executable_sha256: str
    sandbox_launcher: str
    sandbox_launcher_sha256: str
    sandbox_launcher_kind: str
    sandbox_profile: str
    broker_origin: str | None
    launch_profile_sha256: str
    auth_kind: str
    auth_environment_names: tuple[str, ...]
    evidence_digest: str

    def validate_runtime(
        self,
        spec: AdapterLaunchSpec,
        auth: HarnessAuthInjection,
        resolved_executable: str,
    ) -> None:
        _require_unseeded_work_directory(spec.work_dir)
        if spec != self.spec or spec.semantic_mode != "clean_worker":
            raise GateBlocked("G03", "clean-worker admission crossed its exact launch binding")
        if (
            auth.harness != spec.harness
            or auth.kind != self.auth_kind
            or auth.environment_names != self.auth_environment_names
            or auth.broker_origin != self.broker_origin
            or Path(resolved_executable).resolve() != Path(self.executable_path).resolve()
        ):
            raise GateBlocked("G03", "clean-worker authentication or executable binding changed")
        if CleanWorkerLauncher._sha256_file(Path(resolved_executable)) != self.executable_sha256:
            raise GateBlocked("G03", "clean-worker executable changed after admission")
        if CleanWorkerLauncher._sha256_file(Path(self.sandbox_launcher)) != self.sandbox_launcher_sha256:
            raise GateBlocked("G03", "clean-worker sandbox launcher changed after admission")
        if adapter_launch_profile_digest(spec) != self.launch_profile_sha256:
            raise GateBlocked("G03", "clean-worker launch profile changed after admission")

    def wrap_command(self, command: tuple[str, ...]) -> tuple[str, ...]:
        root = self.spec.root_dir
        executable = Path(command[0]).resolve()
        if self.sandbox_launcher_kind == "broker_egress_wrapper":
            if self.broker_origin is None:
                raise GateBlocked("G03", "broker-egress wrapper lacks an exact broker origin")
            return (
                self.sandbox_launcher,
                "--profile",
                "agentnet-clean-worker-broker-v1",
                "--private-root",
                str(root),
                "--work-dir",
                str(self.spec.work_dir),
                "--broker-origin",
                self.broker_origin,
                "--",
                *command,
            )
        arguments: list[str] = [
            self.sandbox_launcher,
            "--unshare-all",
        ]
        arguments.extend(
            [
                "--new-session",
                "--die-with-parent",
                "--proc",
                "/proc",
                "--dev",
                "/dev",
                "--tmpfs",
                "/tmp",
            ]
        )
        for system_path in (Path("/usr"), Path("/bin"), Path("/lib"), Path("/lib64")):
            if system_path.exists():
                arguments.extend(("--ro-bind", str(system_path), str(system_path)))
        for directory in _sandbox_parent_directories(root.parent):
            arguments.extend(("--dir", str(directory)))
        arguments.extend(("--bind", str(root), str(root)))
        if not any(executable.is_relative_to(path) for path in (Path("/usr"), Path("/bin"), root)):
            for directory in _sandbox_parent_directories(executable.parent):
                arguments.extend(("--dir", str(directory)))
            arguments.extend(("--ro-bind", str(executable), str(executable)))
        arguments.extend(("--chdir", str(self.spec.work_dir), "--", *command))
        return tuple(arguments)


def _sandbox_parent_directories(path: Path) -> tuple[Path, ...]:
    result: list[Path] = []
    for parent in reversed(path.resolve().parents):
        if parent != Path("/"):
            result.append(parent)
    result.append(path.resolve())
    return tuple(dict.fromkeys(result))


class CleanWorkerLauncher:
    def __init__(self, *, evidence_dir: Path, trusted_evidence_keys: Mapping[str, str] | None = None) -> None:
        self.evidence_dir = evidence_dir
        self.trusted_evidence_keys = dict(trusted_evidence_keys or {})

    def _base_environment(self, home: Path) -> dict[str, str]:
        return {
            "HOME": str(home),
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "PATH": "/usr/local/bin:/usr/bin:/bin",
            "PYTHONNOUSERSITE": "1",
            "NO_COLOR": "1",
        }

    @staticmethod
    def _sha256_file(path: Path) -> str:
        digest = hashlib.sha256()
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        try:
            while chunk := os.read(descriptor, 1_048_576):
                digest.update(chunk)
        finally:
            os.close(descriptor)
        return digest.hexdigest()

    def _validate_semantic_evidence(self, spec: WorkerSpec, executable: str, bubblewrap: str) -> None:
        evidence_path = self.evidence_dir / f"{spec.harness_kind}-clean-worker.json"
        try:
            record = json.loads(evidence_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise GateBlocked("G03", "semantic worker lacks valid signed clean-worker evidence") from exc
        required = {
            "schema",
            "harness_kind",
            "executable_sha256",
            "bubblewrap_sha256",
            "sandbox_profile",
            "passed_gates",
            "tested_at",
            "expires_at",
            "key_id",
            "signature",
        }
        if set(record) != required:
            raise GateBlocked("G03", "clean-worker evidence schema is not exact")
        if (
            record["schema"] != "agentnet.clean-worker-evidence.v1"
            or record["harness_kind"] != spec.harness_kind
            or record["sandbox_profile"] != "networkless-no-ambient-secrets"
            or set(record["passed_gates"]) != {"G03", "G05"}
            or record["executable_sha256"] != self._sha256_file(Path(executable))
            or record["bubblewrap_sha256"] != self._sha256_file(Path(bubblewrap))
        ):
            raise GateBlocked("G03", "clean-worker evidence does not bind the exact runtime")
        now = int(time.time())
        if not isinstance(record["tested_at"], int) or not isinstance(record["expires_at"], int):
            raise GateBlocked("G03", "clean-worker evidence timestamps are invalid")
        if record["tested_at"] > now + 60 or record["expires_at"] <= now or record["expires_at"] - record["tested_at"] > 31_536_000:
            raise GateBlocked("G03", "clean-worker evidence is stale or implausible")
        public_key = self.trusted_evidence_keys.get(str(record["key_id"]))
        if public_key is None:
            raise GateBlocked("G03", "clean-worker evidence signer is not trusted")
        signed = {key: value for key, value in record.items() if key != "signature"}
        try:
            verify_signature(public_key, "agentnet.component.adoption.v1", signed, str(record["signature"]))
        except Exception as exc:
            raise GateBlocked("G03", "clean-worker evidence signature is invalid") from exc

    def admit_adapter(
        self,
        spec: AdapterLaunchSpec,
        auth: HarnessAuthInjection,
        *,
        sandbox_launcher: str = "bwrap",
    ) -> CleanWorkerAdmission:
        """Verify signed v2 evidence and mint an exact semantic launch spec."""

        if spec.semantic_mode != "deterministic_only" or auth.harness != spec.harness:
            raise GateBlocked("G03", "clean-worker admission requires an unmodified native spec")
        _require_unseeded_work_directory(spec.work_dir)
        if any(spec.home_dir.iterdir()):
            raise GateBlocked("G03", "clean-worker admission requires an empty private HOME")
        executable = shutil.which(spec.executable)
        bubblewrap = shutil.which(sandbox_launcher)
        if executable is None or bubblewrap is None:
            raise GateBlocked("G03", "clean-worker executable or sandbox launcher is unavailable")
        record = self._read_adapter_evidence(spec.harness)
        required = {
            "schema",
            "harness_kind",
            "executable_sha256",
            "sandbox_launcher_sha256",
            "sandbox_launcher_kind",
            "launch_profile_sha256",
            "sandbox_profile",
            "broker_origin",
            "credential_scope",
            "auth_kind",
            "auth_environment_names",
            "passed_gates",
            "tested_at",
            "expires_at",
            "key_id",
            "signature",
        }
        if set(record) != required:
            raise GateBlocked("G03", "native clean-worker evidence schema is not exact")
        try:
            environment_names = tuple(record["auth_environment_names"])
            passed_gates = set(record["passed_gates"])
        except (TypeError, ValueError):
            raise GateBlocked("G03", "native clean-worker evidence collections are invalid") from None
        executable_path = str(Path(executable).resolve())
        sandbox_launcher_path = str(Path(bubblewrap).resolve())
        executable_sha256 = self._sha256_file(Path(executable_path))
        sandbox_launcher_sha256 = self._sha256_file(Path(sandbox_launcher_path))
        broker_enabled = auth.broker_origin is not None
        raw_bubblewrap = shutil.which("bwrap")
        if (
            broker_enabled
            and (
                Path(sandbox_launcher_path).name in {"bwrap", "bubblewrap"}
                or (
                    raw_bubblewrap is not None
                    and Path(sandbox_launcher_path).resolve() == Path(raw_bubblewrap).resolve()
                )
            )
        ):
            raise GateBlocked(
                "G03",
                "raw bubblewrap cannot claim broker-only egress; use a separately evidenced wrapper",
            )
        admitted_spec = semantic_adapter_spec(
            spec,
            executable=executable_path,
            broker_enabled=broker_enabled,
            broker_origin=auth.broker_origin,
        )
        profile_sha256 = adapter_launch_profile_digest(admitted_spec)
        expected_launcher_kind = "broker_egress_wrapper" if broker_enabled else "bubblewrap_networkless"
        expected_sandbox_profile = (
            "broker-only-egress-no-ambient-secrets"
            if broker_enabled
            else "networkless-no-ambient-secrets"
        )
        if (
            record["schema"] != "agentnet.clean-worker-evidence.v2"
            or record["harness_kind"] != spec.harness
            or record["sandbox_launcher_kind"] != expected_launcher_kind
            or record["sandbox_profile"] != expected_sandbox_profile
            or record["broker_origin"] != auth.broker_origin
            or record["credential_scope"] != auth.credential_scope
            or record["auth_kind"] != auth.kind
            or environment_names != auth.environment_names
            or list(environment_names) != sorted(set(environment_names))
            or passed_gates != {"G03", "G05"}
            or record["executable_sha256"] != executable_sha256
            or record["sandbox_launcher_sha256"] != sandbox_launcher_sha256
            or record["launch_profile_sha256"] != profile_sha256
        ):
            raise GateBlocked("G03", "native clean-worker evidence does not bind the exact runtime")
        self._validate_evidence_time_and_signature(record)
        signed = {key: value for key, value in record.items() if key != "signature"}
        return CleanWorkerAdmission(
            spec=admitted_spec,
            executable_path=executable_path,
            executable_sha256=executable_sha256,
            sandbox_launcher=sandbox_launcher_path,
            sandbox_launcher_sha256=sandbox_launcher_sha256,
            sandbox_launcher_kind=expected_launcher_kind,
            sandbox_profile=str(record["sandbox_profile"]),
            broker_origin=auth.broker_origin,
            launch_profile_sha256=profile_sha256,
            auth_kind=auth.kind,
            auth_environment_names=environment_names,
            evidence_digest=canonical_digest(signed),
        )

    def _read_adapter_evidence(self, harness: HarnessKind) -> dict[str, Any]:
        evidence_path = self.evidence_dir / f"{harness}-clean-worker.json"
        try:
            record = json.loads(evidence_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise GateBlocked("G03", "native worker lacks valid signed clean-worker evidence") from exc
        if not isinstance(record, dict):
            raise GateBlocked("G03", "native clean-worker evidence must be an object")
        return record

    def _validate_evidence_time_and_signature(self, record: dict[str, Any]) -> None:
        now = int(time.time())
        tested_at = record.get("tested_at")
        expires_at = record.get("expires_at")
        if (
            type(tested_at) is not int
            or type(expires_at) is not int
            or tested_at > now + 60
            or expires_at <= now
            or expires_at - tested_at > 31_536_000
        ):
            raise GateBlocked("G03", "native clean-worker evidence is stale or implausible")
        public_key = self.trusted_evidence_keys.get(str(record.get("key_id")))
        if public_key is None:
            raise GateBlocked("G03", "native clean-worker evidence signer is not trusted")
        signed = {key: value for key, value in record.items() if key != "signature"}
        try:
            verify_signature(
                public_key,
                "agentnet.component.adoption.v1",
                signed,
                str(record.get("signature")),
            )
        except Exception as exc:
            raise GateBlocked("G03", "native clean-worker evidence signature is invalid") from exc

    def create_adapter_runtime(
        self,
        spec: AdapterLaunchSpec,
        auth: HarnessAuthInjection,
        *,
        sandbox_launcher: str = "bwrap",
        **runtime_options: Any,
    ):
        """Create the only runtime path that admits semantic native turns."""

        from agentnet.supervisor.runtime import BackgroundAdapterRuntime

        admission = self.admit_adapter(spec, auth, sandbox_launcher=sandbox_launcher)
        return BackgroundAdapterRuntime(
            admission.spec,
            auth=auth,
            clean_worker_admission=admission,
            **runtime_options,
        )

    def launch(self, spec: WorkerSpec) -> subprocess.Popen[bytes]:
        executable = shutil.which(spec.executable)
        if executable is None:
            raise GateBlocked("G03", f"{spec.harness_kind} executable is unavailable")
        command = [executable, *spec.arguments]
        if spec.semantic:
            bubblewrap = shutil.which("bwrap")
            if bubblewrap is None:
                raise GateBlocked("G03", "semantic worker lacks bwrap and exact-version clean-worker evidence")
            self._validate_semantic_evidence(spec, executable, bubblewrap)
        workspace = Path(tempfile.mkdtemp(prefix=f"agentnet-{spec.harness_kind}-"))
        home = workspace / "home"
        work = workspace / "work"
        home.mkdir(mode=0o700)
        work.mkdir(mode=0o700)
        if spec.semantic:
            command = [
                bubblewrap,
                "--unshare-all",
                "--new-session",
                "--die-with-parent",
                "--clearenv",
                "--ro-bind",
                "/usr",
                "/usr",
                "--ro-bind",
                "/bin",
                "/bin",
                "--proc",
                "/proc",
                "--dev",
                "/dev",
                "--tmpfs",
                "/tmp",
                "--bind",
                str(work),
                "/work",
                "--chdir",
                "/work",
                executable,
                *spec.arguments,
            ]
        return subprocess.Popen(
            command,
            cwd=work,
            env=self._base_environment(home),
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
            close_fds=True,
        )


def _require_unseeded_work_directory(work_dir: Path) -> None:
    """Deny inherited project instructions, hooks, and workspace state.

    The directory is intentionally persistent for one private session, but no
    harness is allowed to seed it with an AGENTS/CLAUDE file, plugin, hook, or
    arbitrary project before admission or between recovery attempts.
    """

    try:
        seeded = next(work_dir.iterdir(), None)
    except OSError as exc:
        raise GateBlocked("G03", "clean-worker private workspace is unavailable") from exc
    if seeded is not None:
        raise GateBlocked("G03", "clean-worker private workspace is not empty")
