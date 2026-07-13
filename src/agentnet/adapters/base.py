"""Agent-agnostic adapter capability and executable-launch contracts."""

from __future__ import annotations

from dataclasses import dataclass
import os
from pathlib import Path
from typing import Literal


@dataclass(frozen=True, slots=True)
class AdapterCapabilities:
    # Built-ins use the four documented names.  The ABI deliberately permits a
    # future adapter identifier so adding a harness does not require changing
    # this core dataclass.
    harness: str
    background_path: str
    local_binding: Literal["mcp", "direct_ipc", "none"]
    passive_indicator: Literal["content_free_count", "none", "probe_only"]
    semantic_default: Literal["clean_worker_required", "deterministic_only"]
    foreground_message_methods: tuple[()] = ()
    holds_credentials: bool = False

    def validate(self) -> None:
        if (
            not self.harness
            or len(self.harness) > 64
            or not all(character.islower() or character.isdigit() or character in {"-", "_"} for character in self.harness)
        ):
            raise ValueError("adapter harness identifier is outside the ABI profile")
        if self.foreground_message_methods or self.holds_credentials:
            raise ValueError("adapter violates zero-secret/no-foreground invariant")


# Strings are intentional ABI extension points.  Built-in factories still
# reject unknown values; a future adapter supplies its own conforming factory.
HarnessKind = str
TransportKind = str
SemanticMode = Literal["deterministic_only", "clean_worker"]


@dataclass(frozen=True, slots=True)
class ExecutableProbe:
    harness: HarnessKind
    executable: str
    pinned_version: str
    resolved_path: str | None
    reported_version: str | None
    matches_pin: bool
    exit_code: int | None
    error: Literal["absent", "timeout", "nonzero", "unparseable"] | None
    evidence_scope: Literal["local_detection_only"] = "local_detection_only"
    external_conformance_proven: Literal[False] = False


@dataclass(frozen=True, slots=True)
class AdapterLaunchSpec:
    """Exact command and private state boundary for one background session."""

    harness: HarnessKind
    harness_id: str
    executable: str
    pinned_version: str
    version_arguments: tuple[str, ...]
    arguments: tuple[str, ...]
    transport: TransportKind
    persistent_process: bool
    session_id: str
    root_dir: Path
    home_dir: Path
    work_dir: Path
    state_dir: Path
    temp_dir: Path
    model: str | None = None
    reasoning_effort: str | None = None
    semantic_mode: SemanticMode = "deterministic_only"
    local_binding_enabled: bool = False
    foreground_session_id: None = None

    @property
    def command(self) -> tuple[str, ...]:
        return (self.executable, *self.arguments)

    def validate(self) -> None:
        if not self.harness_id or not self.session_id:
            raise ValueError("background launch binding is incomplete")
        directories = (self.root_dir, self.home_dir, self.work_dir, self.state_dir, self.temp_dir)
        if any(not path.is_absolute() for path in directories) or len(set(directories)) != len(directories):
            raise ValueError("background launch directories must be distinct absolute paths")
        if any(path.is_symlink() or not path.is_dir() or path.stat().st_mode & 0o077 for path in directories):
            raise ValueError("background launch directories must be owner-only real directories")
        if any(path.parent != self.root_dir for path in directories[1:]):
            raise ValueError("background launch directories must remain inside the private root")
        if any(os.path.commonpath((self.root_dir, path)) != str(self.root_dir) for path in directories[1:]):
            raise ValueError("background launch directory escaped its private root")
        if self.foreground_session_id is not None:
            raise ValueError("background adapter cannot bind a foreground session")
        if self.harness == "codex":
            if self.model != "gpt-5.6-sol" or self.reasoning_effort != "ultra":
                raise ValueError("Codex background work must preserve gpt-5.6-sol ultra")
        elif self.model is not None or self.reasoning_effort is not None:
            raise ValueError("non-Codex native profiles cannot inherit Codex model settings")
        forbidden = {
            "--add-dir",
            "--api-key",
            "--chrome",
            "--continue",
            "--dangerously-skip-permissions",
            "--ide",
            "--prompt-interactive",
            "--remote-control",
            "--resume",
        }
        if any(argument in forbidden for argument in self.arguments):
            raise ValueError("background launch requests a foreground or ambient-authority feature")
