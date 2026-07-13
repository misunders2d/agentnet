"""Explicit authentication inputs for private native harness workers.

Nothing in this module reads the parent process environment or a user's normal
CLI home. Environment credentials live only in the runtime object and child
environment. A preprovisioned bundle is copied into the dedicated private
worker root with owner-only permissions.
"""

from __future__ import annotations

import os
import stat
from abc import ABC, abstractmethod
from pathlib import Path
from types import MappingProxyType
from typing import ClassVar, Literal, Mapping
from urllib.parse import urlsplit

from agentnet.adapters.base import AdapterLaunchSpec, HarnessKind
from agentnet.errors import ValidationError


CredentialScope = Literal["supervisor-model-egress-broker"]


AUTH_ENVIRONMENT_ALLOWLIST: dict[HarnessKind, frozenset[str]] = {
    "claude": frozenset({"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}),
    "codex": frozenset({"OPENAI_API_KEY", "OPENAI_BASE_URL"}),
    "pi": frozenset(
        {
            "ANTHROPIC_API_KEY",
            "ANTHROPIC_BASE_URL",
            "GEMINI_API_KEY",
            "GEMINI_BASE_URL",
            "OPENAI_API_KEY",
            "OPENAI_BASE_URL",
        }
    ),
    # The installed CLI documents private CLI state, not a broker-scoped
    # environment credential. Antigravity therefore uses an explicit private
    # auth bundle until a documented environment path passes its bake-off.
    "antigravity": frozenset(),
}


class HarnessAuthInjection(ABC):
    """In-memory or preprovisioned authentication for exactly one harness."""

    credential_scope: ClassVar[CredentialScope] = "supervisor-model-egress-broker"
    harness: HarnessKind

    @property
    @abstractmethod
    def kind(self) -> str: ...

    @property
    @abstractmethod
    def environment_names(self) -> tuple[str, ...]: ...

    @property
    @abstractmethod
    def broker_origin(self) -> str | None: ...

    @abstractmethod
    def environment_for(self, harness: HarnessKind) -> dict[str, str]: ...

    @abstractmethod
    def materialize(self, spec: AdapterLaunchSpec) -> None: ...


class EphemeralBrokerEnvironment(HarnessAuthInjection):
    """Strictly allowlisted broker credentials that are never written to disk."""

    def __init__(self, harness: HarnessKind, values: Mapping[str, str]) -> None:
        allowed = AUTH_ENVIRONMENT_ALLOWLIST[harness]
        supplied = set(values)
        if not supplied or not supplied <= allowed:
            raise ValidationError("harness authentication environment is not allowlisted")
        copied: dict[str, str] = {}
        for name, value in values.items():
            if (
                not isinstance(name, str)
                or not isinstance(value, str)
                or not value
                or len(value.encode("utf-8")) > 16_384
                or "\x00" in value
                or "\r" in value
                or "\n" in value
            ):
                raise ValidationError("harness authentication value is invalid")
            if name.endswith("_BASE_URL"):
                _validate_broker_url(value)
            copied[name] = value
        if not any(name.endswith(("_API_KEY", "_TOKEN")) for name in supplied):
            raise ValidationError("harness authentication omitted a broker credential")
        origins = [value for name, value in copied.items() if name.endswith("_BASE_URL")]
        if len(origins) > 1:
            raise ValidationError("harness authentication has ambiguous broker origins")
        self.harness = harness
        self._values = MappingProxyType(copied)
        self._broker_origin = origins[0].rstrip("/") if origins else None

    @property
    def kind(self) -> str:
        return "ephemeral_environment"

    @property
    def environment_names(self) -> tuple[str, ...]:
        return tuple(sorted(self._values))

    @property
    def broker_origin(self) -> str | None:
        return self._broker_origin

    def environment_for(self, harness: HarnessKind) -> dict[str, str]:
        if harness != self.harness:
            raise ValidationError("harness authentication crossed its harness binding")
        return dict(self._values)

    def materialize(self, spec: AdapterLaunchSpec) -> None:
        if spec.harness != self.harness:
            raise ValidationError("harness authentication crossed its launch binding")

    def __repr__(self) -> str:
        return (
            f"EphemeralBrokerEnvironment(harness={self.harness!r}, "
            f"environment_names={self.environment_names!r}, values=<redacted>)"
        )


def _validate_broker_url(value: str) -> None:
    parsed = urlsplit(value)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ValidationError("model-egress broker URL is invalid")
    if parsed.scheme == "http" and parsed.hostname not in {"127.0.0.1", "::1", "localhost"}:
        raise ValidationError("plaintext model-egress broker must be loopback-only")


class PreprovisionedPrivateAuth(HarnessAuthInjection):
    """Owner-only auth state prepared specifically for a private worker.

    The source is never inferred from a normal user home. It must be supplied
    explicitly and is copied, without symlinks or special files, into the
    harness's dedicated private auth root.
    """

    _MAX_FILES = 128
    _MAX_TOTAL_BYTES = 4 * 1024 * 1024

    def __init__(
        self,
        harness: HarnessKind,
        source: Path,
        *,
        broker_origin: str | None = None,
    ) -> None:
        if source.is_symlink():
            raise ValidationError("private auth source cannot be a symbolic link")
        source = source.resolve(strict=True)
        source_stat = source.lstat()
        if not source.is_dir() or stat.S_ISLNK(source_stat.st_mode) or source_stat.st_mode & 0o077:
            raise ValidationError("private auth source must be an owner-only real directory")
        self.harness = harness
        self.source = source
        if broker_origin is not None:
            _validate_broker_url(broker_origin)
        self._broker_origin = broker_origin.rstrip("/") if broker_origin else None

    @property
    def kind(self) -> str:
        return "preprovisioned_private_auth"

    @property
    def environment_names(self) -> tuple[str, ...]:
        return ()

    @property
    def broker_origin(self) -> str | None:
        return self._broker_origin

    def environment_for(self, harness: HarnessKind) -> dict[str, str]:
        if harness != self.harness:
            raise ValidationError("private auth crossed its harness binding")
        return {}

    def materialize(self, spec: AdapterLaunchSpec) -> None:
        if spec.harness != self.harness:
            raise ValidationError("private auth crossed its launch binding")
        target = _private_auth_target(spec)
        target.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(target, 0o700)
        file_count = 0
        total_bytes = 0
        pending = [(self.source, target)]
        while pending:
            source_directory, target_directory = pending.pop()
            for entry in source_directory.iterdir():
                source_stat = entry.lstat()
                if stat.S_ISLNK(source_stat.st_mode):
                    raise ValidationError("private auth bundle cannot contain symbolic links")
                destination = target_directory / entry.name
                if stat.S_ISDIR(source_stat.st_mode):
                    if source_stat.st_mode & 0o077:
                        raise ValidationError("private auth directories must be owner-only")
                    destination.mkdir(mode=0o700, exist_ok=True)
                    if destination.is_symlink():
                        raise ValidationError("private auth destination cannot be a symbolic link")
                    os.chmod(destination, 0o700)
                    pending.append((entry, destination))
                    continue
                if (
                    not stat.S_ISREG(source_stat.st_mode)
                    or source_stat.st_mode & 0o077
                    or source_stat.st_mode & 0o111
                ):
                    raise ValidationError("private auth files must be owner-only regular files")
                descriptor = os.open(entry, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
                try:
                    opened_stat = os.fstat(descriptor)
                    if (
                        not stat.S_ISREG(opened_stat.st_mode)
                        or opened_stat.st_dev != source_stat.st_dev
                        or opened_stat.st_ino != source_stat.st_ino
                        or opened_stat.st_mode & 0o077
                        or opened_stat.st_mode & 0o111
                    ):
                        raise ValidationError("private auth file changed during provisioning")
                    file_count += 1
                    total_bytes += opened_stat.st_size
                    if file_count > self._MAX_FILES or total_bytes > self._MAX_TOTAL_BYTES:
                        raise ValidationError("private auth bundle exceeds its bounded profile")
                    data = bytearray()
                    while chunk := os.read(descriptor, 65_536):
                        data.extend(chunk)
                finally:
                    os.close(descriptor)
                try:
                    if destination.exists():
                        if destination.is_symlink() or destination.read_bytes() != bytes(data):
                            raise ValidationError("private auth destination conflicts with existing state")
                        os.chmod(destination, 0o600)
                    else:
                        output = os.open(
                            destination,
                            os.O_WRONLY
                            | os.O_CREAT
                            | os.O_EXCL
                            | getattr(os, "O_NOFOLLOW", 0),
                            0o600,
                        )
                        try:
                            view = memoryview(data)
                            while view:
                                written = os.write(output, view)
                                view = view[written:]
                            os.fsync(output)
                        finally:
                            os.close(output)
                finally:
                    for index in range(len(data)):
                        data[index] = 0
        if file_count == 0:
            raise ValidationError("private auth bundle is empty")

    def __repr__(self) -> str:
        return f"PreprovisionedPrivateAuth(harness={self.harness!r}, source=<redacted>)"


def _private_auth_target(spec: AdapterLaunchSpec) -> Path:
    if spec.harness == "codex":
        return spec.state_dir / "codex"
    if spec.harness == "pi":
        return spec.state_dir / "pi"
    return spec.home_dir
