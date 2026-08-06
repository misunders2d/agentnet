"""Resumable, user-level AgentNet endpoint setup coordination.

This module deliberately owns presentation state only. Enrollment, current
credential authority, and endpoint activation remain with their existing
services. The sole local resume artifact is one owner-private opaque
continuation; an enrolled actor is always re-read from the current identity
profile before endpoint activation.
"""

from __future__ import annotations

import json
import os
import re
import secrets
import stat
from collections.abc import Callable, Sequence
from enum import StrEnum
from pathlib import Path
from typing import Literal, Protocol

from pydantic import (
    AnyHttpUrl,
    BaseModel,
    ConfigDict,
    Field,
    SecretStr,
    field_validator,
    model_validator,
)

from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.endpoint_lifecycle import (
    EndpointActivationState,
    EndpointLifecycleService,
    EndpointLifecycleStatus,
)

_SETUP_STATE_SCHEMA = "agentnet.client-setup-continuation.v1"
_MAX_CONTINUATION_BYTES = 16_384
_PROFILE_KEY_PATTERN = r"^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$"


class ClientSetupError(RuntimeError):
    """Fail-closed client setup error."""


class AmbiguousClientProfile(ClientSetupError):
    """More than one current credential matches the requested local profile."""


class SetupContinuationExpired(ClientSetupError):
    """The enrollment owner proved that an opaque continuation is terminal."""


class SetupNextAction(StrEnum):
    """Complete public presentation vocabulary for client setup."""

    OPEN_BROWSER = "open_browser"
    WAIT_FOR_APPROVAL = "wait_for_approval"
    RESTART_YOUR_AGENT = "restart_your_agent"
    CONNECTED = "connected"
    ADMIN_HELP = "administrator_help"


class ClientSetupResult(BaseModel):
    """Public, secret-free result for one exact endpoint."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    endpoint_id: str = Field(min_length=1, max_length=256)
    state: EndpointActivationState
    next_action: SetupNextAction
    public_url: AnyHttpUrl | None = None
    identity_created: bool

    @property
    def harness_id(self) -> str:
        """Exact enrolled harness identifier bound by ``endpoint_id``."""

        return self.endpoint_id


class ClientSetupState(BaseModel):
    """The only setup-specific local resume state.

    ``SecretStr`` prevents accidental logging or model serialization of the
    continuation. ``ClientSetupContinuationStore`` performs the one intentional
    extraction needed for owner-private persistence and service calls.
    """

    model_config = ConfigDict(
        extra="forbid",
        frozen=True,
        populate_by_name=True,
        serialize_by_alias=True,
    )

    schema_version: Literal["agentnet.client-setup-continuation.v1"] = Field(
        default=_SETUP_STATE_SCHEMA,
        alias="schema",
    )
    continuation: SecretStr

    @field_validator("continuation")
    @classmethod
    def validate_continuation(cls, value: SecretStr) -> SecretStr:
        continuation = value.get_secret_value()
        if not 16 <= len(continuation.encode("utf-8")) <= 8_192 or "\x00" in continuation:
            raise ValueError("setup continuation is invalid")
        return value


class ClientIdentityProfile(BaseModel):
    """One exact local profile whose actor came from its current credential."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    actor: VerifiedActor
    harness_kind: Literal["omp", "pi", "claude", "codex", "antigravity", "server"]
    profile_key: str = Field(pattern=_PROFILE_KEY_PATTERN)

    @model_validator(mode="after")
    def require_exact_owner_harness(self) -> "ClientIdentityProfile":
        if self.actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
            raise ValueError("client setup requires a verified human harness credential")
        if not self.actor.harness_id or not self.actor.credential_id:
            raise ValueError("client setup profile lacks its exact credential binding")
        return self


class EnrollmentProgress(BaseModel):
    """Secret-minimized projection returned by the existing guided join owner."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    endpoint_id: str = Field(min_length=1, max_length=256)
    state: EndpointActivationState
    continuation: SecretStr | None = None
    public_url: AnyHttpUrl | None = None

    @model_validator(mode="after")
    def require_resume_custody_for_pending_state(self) -> "EnrollmentProgress":
        if self.state in {
            EndpointActivationState.READY_TO_CONNECT,
            EndpointActivationState.WAITING_FOR_APPROVAL,
        } and self.continuation is None:
            raise ValueError("pending enrollment progress requires an opaque continuation")
        return self


class GuidedEnrollmentCoordinator(Protocol):
    """Narrow adapter over the existing guided OIDC/passkey coordinator."""

    def begin(
        self,
        *,
        replace_expired_continuation: str | None = None,
    ) -> EnrollmentProgress: ...

    def status(self, *, continuation: str) -> EnrollmentProgress: ...

    def continue_setup(self, *, continuation: str) -> EnrollmentProgress: ...


class ClientSetupContinuationStore:
    """Atomic owner-private custody for one opaque continuation string."""

    def __init__(self, path: Path) -> None:
        self.path = path.absolute()

    def exists(self) -> bool:
        return os.path.lexists(self.path)

    def load(self) -> ClientSetupState:
        self._ensure_private_parent()
        if os.name == "nt":
            from agentnet.windows_security import read_private_file

            try:
                payload = read_private_file(self.path, max_bytes=_MAX_CONTINUATION_BYTES)
            except Exception as exc:
                raise ClientSetupError("setup continuation is unavailable") from exc
            return self._parse(payload)

        descriptor = self._open_for_read()
        try:
            metadata = os.fstat(descriptor)
            payload = bytearray()
            remaining = metadata.st_size
            while remaining:
                chunk = os.read(descriptor, min(remaining, 4_096))
                if not chunk:
                    raise ClientSetupError("setup continuation changed while being read")
                payload.extend(chunk)
                remaining -= len(chunk)
            if os.read(descriptor, 1):
                raise ClientSetupError("setup continuation changed while being read")
        finally:
            os.close(descriptor)
        return self._parse(payload)

    @staticmethod
    def _parse(payload: bytes | bytearray) -> ClientSetupState:
        try:
            value = json.loads(payload)
            if (
                not isinstance(value, dict)
                or set(value) != {"schema", "continuation"}
                or value.get("schema") != _SETUP_STATE_SCHEMA
            ):
                raise ValueError("state does not match the exact schema")
            return ClientSetupState.model_validate(value)
        except (UnicodeError, json.JSONDecodeError, ValueError) as exc:
            raise ClientSetupError("setup continuation state is invalid") from exc

    def save(self, state: ClientSetupState) -> None:
        self._ensure_private_parent()
        payload = json.dumps(
            {
                "schema": state.schema_version,
                "continuation": state.continuation.get_secret_value(),
            },
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8") + b"\n"
        if len(payload) > _MAX_CONTINUATION_BYTES:
            raise ClientSetupError("setup continuation exceeds its custody bound")

        if os.name == "nt":
            from agentnet.windows_security import write_private_file

            try:
                write_private_file(self.path, payload, force=self.path.exists())
            except Exception as exc:
                raise ClientSetupError("could not persist setup continuation safely") from exc
            return

        parent_descriptor = self._open_parent()
        temporary_name = f".{self.path.name}.{secrets.token_hex(16)}.tmp"
        descriptor: int | None = None
        try:
            descriptor = os.open(
                temporary_name,
                os.O_WRONLY
                | os.O_CREAT
                | os.O_EXCL
                | getattr(os, "O_NOFOLLOW", 0)
                | getattr(os, "O_CLOEXEC", 0),
                0o600,
                dir_fd=parent_descriptor,
            )
            offset = 0
            while offset < len(payload):
                written = os.write(descriptor, payload[offset:])
                if written <= 0:
                    raise OSError("setup continuation write made no progress")
                offset += written
            os.fchmod(descriptor, 0o600)
            os.fsync(descriptor)
            os.close(descriptor)
            descriptor = None
            if os.path.lexists(self.path):
                existing = self.path.lstat()
                if not self._private_regular(existing):
                    raise ClientSetupError("existing setup continuation custody is unsafe")
            os.replace(
                temporary_name,
                self.path.name,
                src_dir_fd=parent_descriptor,
                dst_dir_fd=parent_descriptor,
            )
            os.fsync(parent_descriptor)
        except ClientSetupError:
            raise
        except OSError as exc:
            raise ClientSetupError("could not persist setup continuation safely") from exc
        finally:
            if descriptor is not None:
                os.close(descriptor)
            try:
                os.unlink(temporary_name, dir_fd=parent_descriptor)
            except FileNotFoundError:
                pass
            finally:
                os.close(parent_descriptor)

    def clear(self) -> None:
        if not os.path.lexists(self.path):
            return
        self._ensure_private_parent()
        if os.name == "nt":
            from agentnet.windows_security import require_private_path

            try:
                require_private_path(self.path, directory=False)
                self.path.unlink()
            except FileNotFoundError:
                return
            except Exception as exc:
                raise ClientSetupError("could not clear setup continuation safely") from exc
            return

        parent_descriptor = self._open_parent()
        try:
            try:
                metadata = os.stat(
                    self.path.name,
                    dir_fd=parent_descriptor,
                    follow_symlinks=False,
                )
            except FileNotFoundError:
                return
            if not self._private_regular(metadata):
                raise ClientSetupError("setup continuation custody is unsafe")
            os.unlink(self.path.name, dir_fd=parent_descriptor)
            os.fsync(parent_descriptor)
        except ClientSetupError:
            raise
        except OSError as exc:
            raise ClientSetupError("could not clear setup continuation safely") from exc
        finally:
            os.close(parent_descriptor)

    def _ensure_private_parent(self) -> None:
        if os.name == "nt":
            from agentnet.windows_security import ensure_private_directory

            try:
                ensure_private_directory(self.path.parent)
            except Exception as exc:
                raise ClientSetupError("setup continuation directory custody is unsafe") from exc
            return
        try:
            self.path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
            metadata = self.path.parent.lstat()
        except OSError as exc:
            raise ClientSetupError("setup continuation directory is unavailable") from exc
        if (
            stat.S_ISLNK(metadata.st_mode)
            or not stat.S_ISDIR(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or stat.S_IMODE(metadata.st_mode) != 0o700
        ):
            raise ClientSetupError("setup continuation directory must be owner-only")

    def _open_parent(self) -> int:
        try:
            descriptor = os.open(
                self.path.parent,
                os.O_RDONLY
                | getattr(os, "O_DIRECTORY", 0)
                | getattr(os, "O_NOFOLLOW", 0)
                | getattr(os, "O_CLOEXEC", 0),
            )
        except OSError as exc:
            raise ClientSetupError("setup continuation directory custody is unsafe") from exc
        metadata = os.fstat(descriptor)
        if os.name != "nt" and (
            not stat.S_ISDIR(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or stat.S_IMODE(metadata.st_mode) != 0o700
        ):
            os.close(descriptor)
            raise ClientSetupError("setup continuation directory custody is unsafe")
        return descriptor

    def _open_for_read(self) -> int:
        if not self.path.is_absolute():
            raise ClientSetupError("setup continuation path must be absolute")
        try:
            descriptor = os.open(
                self.path,
                os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0),
            )
        except OSError as exc:
            raise ClientSetupError("setup continuation is unavailable") from exc
        metadata = os.fstat(descriptor)
        if not self._private_regular(metadata):
            os.close(descriptor)
            raise ClientSetupError("setup continuation must be an owner-private regular file")
        return descriptor

    @staticmethod
    def _private_regular(metadata: os.stat_result) -> bool:
        return (
            stat.S_ISREG(metadata.st_mode)
            and metadata.st_nlink == 1
            and 0 < metadata.st_size <= _MAX_CONTINUATION_BYTES
            and (os.name == "nt" or metadata.st_uid == os.geteuid())
            and (os.name == "nt" or stat.S_IMODE(metadata.st_mode) == 0o600)
        )


class ClientSetupCoordinator:
    """Compose current identity, guided enrollment, and endpoint lifecycle.

    The coordinator never starts, stops, restarts, or signals a harness. Reaching
    ``restart_required`` returns ``restart_your_agent`` and leaves the explicit
    user restart as the only next step.
    """

    def __init__(
        self,
        *,
        endpoint_lifecycle: EndpointLifecycleService,
        identity_profiles: Callable[[], Sequence[ClientIdentityProfile]],
        enrollment: GuidedEnrollmentCoordinator,
        continuation_store: ClientSetupContinuationStore,
        harness_kind: str,
        profile_key: str,
        close: Callable[[], None] | None = None,
    ) -> None:
        if harness_kind not in {"omp", "pi", "claude", "codex", "antigravity", "server"}:
            raise ValueError("unsupported harness kind")
        if re.fullmatch(_PROFILE_KEY_PATTERN, profile_key) is None:
            raise ValueError("profile key is invalid")
        self.endpoint_lifecycle = endpoint_lifecycle
        self.identity_profiles = identity_profiles
        self.enrollment = enrollment
        self.continuation_store = continuation_store
        self.harness_kind = harness_kind
        self.profile_key = profile_key
        self._close = close
        self._closed = False

    def setup(self) -> ClientSetupResult:
        profile = self._current_profile()
        if profile is not None:
            result = self._activate(profile, identity_created=False)
            self.continuation_store.clear()
            return result
        if self.continuation_store.exists():
            return self.continue_setup()
        return self._consume_enrollment(self.enrollment.begin(), identity_created=False)

    def status(self) -> ClientSetupResult:
        profile = self._current_profile()
        if profile is not None:
            endpoint_id = profile.actor.harness_id or ""
            lifecycle = self.endpoint_lifecycle.reconcile(endpoint_id=endpoint_id)
            return self._lifecycle_result(lifecycle, identity_created=False)
        state = self._required_continuation()
        progress = self.enrollment.status(
            continuation=state.continuation.get_secret_value(),
        )
        return self._consume_enrollment(progress, identity_created=False)

    def continue_setup(self) -> ClientSetupResult:
        profile = self._current_profile()
        if profile is not None:
            result = self._activate(profile, identity_created=False)
            self.continuation_store.clear()
            return result
        state = self._required_continuation()
        continuation = state.continuation.get_secret_value()
        try:
            progress = self.enrollment.continue_setup(continuation=continuation)
        except SetupContinuationExpired:
            replacement = self.enrollment.begin(
                replace_expired_continuation=continuation,
            )
            return self._consume_enrollment(replacement, identity_created=False)
        return self._consume_enrollment(progress, identity_created=True)

    def close(self) -> None:
        if not self._closed and self._close is not None:
            self._close()
        self._closed = True

    def _required_continuation(self) -> ClientSetupState:
        if not self.continuation_store.exists():
            raise ClientSetupError("no resumable AgentNet setup continuation exists")
        return self.continuation_store.load()

    def _current_profile(self) -> ClientIdentityProfile | None:
        try:
            profiles = tuple(self.identity_profiles())
        except ClientSetupError:
            raise
        except Exception as exc:
            raise ClientSetupError("current credential profiles are unavailable") from exc
        candidates = tuple(
            profile
            for profile in profiles
            if profile.harness_kind == self.harness_kind and profile.profile_key == self.profile_key
        )
        if len(candidates) > 1:
            raise AmbiguousClientProfile(
                "current AgentNet identity profile is ambiguous; select one exact profile"
            )
        return candidates[0] if candidates else None

    def _activate(
        self,
        profile: ClientIdentityProfile,
        *,
        identity_created: bool,
    ) -> ClientSetupResult:
        lifecycle = self.endpoint_lifecycle.register_existing(
            actor=profile.actor,
            harness_kind=profile.harness_kind,
            profile_key=profile.profile_key,
        )
        if lifecycle.state is EndpointActivationState.ENROLLED:
            lifecycle = self.endpoint_lifecycle.reconcile(endpoint_id=lifecycle.endpoint_id)
        if lifecycle.state is EndpointActivationState.ACCESS_READY:
            lifecycle = self.endpoint_lifecycle.request_activation(
                actor=profile.actor,
                expected_revision=lifecycle.revision,
            )
        if lifecycle.endpoint_id != profile.actor.harness_id:
            raise ClientSetupError("endpoint lifecycle resolved a different harness identity")
        return self._lifecycle_result(lifecycle, identity_created=identity_created)

    def _consume_enrollment(
        self,
        progress: EnrollmentProgress,
        *,
        identity_created: bool,
    ) -> ClientSetupResult:
        if progress.continuation is not None:
            self.continuation_store.save(
                ClientSetupState(continuation=progress.continuation),
            )
        elif progress.state in {
            EndpointActivationState.READY_TO_CONNECT,
            EndpointActivationState.WAITING_FOR_APPROVAL,
        }:
            raise ClientSetupError("pending setup lost its continuation custody")

        if progress.state in {
            EndpointActivationState.ENROLLED,
            EndpointActivationState.ACCESS_READY,
            EndpointActivationState.RESTART_REQUIRED,
            EndpointActivationState.CONNECTED,
        }:
            profile = self._current_profile()
            if profile is None:
                return ClientSetupResult(
                    endpoint_id=progress.endpoint_id,
                    state=progress.state,
                    next_action=SetupNextAction.ADMIN_HELP,
                    public_url=progress.public_url,
                    identity_created=False,
                )
            if profile.actor.harness_id != progress.endpoint_id:
                raise ClientSetupError("enrollment completed for a different harness identity")
            result = self._activate(profile, identity_created=identity_created)
            self.continuation_store.clear()
            return result

        return ClientSetupResult(
            endpoint_id=progress.endpoint_id,
            state=progress.state,
            next_action=_next_action(progress.state),
            public_url=progress.public_url,
            identity_created=False,
        )

    @staticmethod
    def _lifecycle_result(
        lifecycle: EndpointLifecycleStatus,
        *,
        identity_created: bool,
    ) -> ClientSetupResult:
        return ClientSetupResult(
            endpoint_id=lifecycle.endpoint_id,
            state=lifecycle.state,
            next_action=_next_action(lifecycle.state),
            public_url=None,
            identity_created=identity_created,
        )


def _next_action(state: EndpointActivationState) -> SetupNextAction:
    if state is EndpointActivationState.READY_TO_CONNECT:
        return SetupNextAction.OPEN_BROWSER
    if state in {
        EndpointActivationState.WAITING_FOR_APPROVAL,
        EndpointActivationState.ENROLLED,
    }:
        return SetupNextAction.WAIT_FOR_APPROVAL
    if state in {
        EndpointActivationState.ACCESS_READY,
        EndpointActivationState.RESTART_REQUIRED,
    }:
        return SetupNextAction.RESTART_YOUR_AGENT
    if state is EndpointActivationState.CONNECTED:
        return SetupNextAction.CONNECTED
    return SetupNextAction.ADMIN_HELP


__all__ = [
    "AmbiguousClientProfile",
    "ClientIdentityProfile",
    "ClientSetupContinuationStore",
    "ClientSetupCoordinator",
    "ClientSetupError",
    "ClientSetupResult",
    "ClientSetupState",
    "EnrollmentProgress",
    "GuidedEnrollmentCoordinator",
    "SetupContinuationExpired",
    "SetupNextAction",
]
