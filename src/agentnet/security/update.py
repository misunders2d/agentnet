"""Exact, purpose-separated threshold update metadata verification."""

from __future__ import annotations

import re
from collections.abc import Mapping, Sequence
from typing import Any, Literal
from urllib.parse import urlsplit

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError, field_validator, model_validator

from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import canonical_digest, verify_signature


UPDATE_SIGNATURE_PURPOSE = "agentnet.update.manifest.v1"
MAX_UPDATE_KEYS = 16
_VERSION = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")


def _version_tuple(value: str) -> tuple[int, int, int]:
    match = _VERSION.fullmatch(value)
    if match is None:
        raise ValueError("version must be canonical major.minor.patch")
    return tuple(int(part) for part in match.groups())  # type: ignore[return-value]


class UpdateArtifact(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    platform: Literal["linux", "macos", "windows"]
    architecture: Literal["x86_64", "aarch64"]
    uri: str = Field(min_length=1, max_length=2048)
    sha256: str = Field(pattern=r"^[0-9a-f]{64}$")
    size: int = Field(gt=0)

    @field_validator("uri")
    @classmethod
    def canonical_uri(cls, value: str) -> str:
        if value != value.strip() or any(ord(character) < 0x21 for character in value):
            raise ValueError("artifact URI is not canonical")
        try:
            parsed = urlsplit(value)
            port = parsed.port
        except ValueError as exc:
            raise ValueError("artifact URI is invalid") from exc
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.fragment
            or not parsed.path.startswith("/")
        ):
            raise ValueError("artifact URI must be an HTTPS URL without credentials or fragment")
        host = parsed.hostname.lower()
        rendered_host = f"[{host}]" if ":" in host else host
        authority = rendered_host if port in {None, 443} else f"{rendered_host}:{port}"
        canonical = f"https://{authority}{parsed.path}"
        if parsed.query:
            canonical += f"?{parsed.query}"
        if value != canonical:
            raise ValueError("artifact URI is not canonical")
        return value


class UpdateManifest(BaseModel):
    """The only signed update-manifest schema accepted by this profile."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_id: Literal["agentnet.update.manifest.v1"] = Field(alias="schema")
    product: Literal["agentnet"]
    channel: Literal["stable", "canary", "emergency"]
    version: str
    release_sequence: int = Field(gt=0)
    root_version: int = Field(gt=0)
    published_at: int = Field(ge=0)
    expires_at: int = Field(gt=0)
    minimum_installed_version: str
    maximum_installed_version: str
    artifacts: tuple[UpdateArtifact, ...] = Field(min_length=1, max_length=8)

    @field_validator("version", "minimum_installed_version", "maximum_installed_version")
    @classmethod
    def canonical_version(cls, value: str) -> str:
        _version_tuple(value)
        return value

    @model_validator(mode="after")
    def exact_ranges(self) -> "UpdateManifest":
        if self.expires_at <= self.published_at:
            raise ValueError("update manifest expiry must follow publication")
        if _version_tuple(self.minimum_installed_version) > _version_tuple(self.maximum_installed_version):
            raise ValueError("update compatibility range is inverted")
        targets = [(artifact.platform, artifact.architecture) for artifact in self.artifacts]
        if len(set(targets)) != len(targets):
            raise ValueError("update manifest contains duplicate platform targets")
        return self


class UpdateSignature(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    key_id: str = Field(min_length=1, max_length=256)
    signature: str = Field(min_length=1, max_length=4096)


class UpdateTrustRoot(BaseModel):
    """Offline/configured update-only trust root; application keys never enter it."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_id: Literal["agentnet.update.root.v1"] = Field(alias="schema")
    root_version: int = Field(gt=0)
    expires_at: int = Field(gt=0)
    threshold: int = Field(gt=0, le=MAX_UPDATE_KEYS)
    keys: dict[str, str] = Field(min_length=1, max_length=MAX_UPDATE_KEYS)
    max_manifest_lifetime_seconds: int = Field(ge=300, le=2_592_000)
    max_freeze_seconds: int = Field(ge=300, le=2_592_000)

    @model_validator(mode="after")
    def bounded_threshold(self) -> "UpdateTrustRoot":
        if self.threshold > len(self.keys):
            raise ValueError("update threshold exceeds the dedicated root key set")
        if any(not key_id or not public_key for key_id, public_key in self.keys.items()):
            raise ValueError("update root contains an empty key")
        return self


class UpdateVerificationState(BaseModel):
    """Monotonic local state required to reject rollback and freeze attacks."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    installed_version: str
    installed_sequence: int = Field(ge=0)
    highest_seen_version: str
    highest_seen_sequence: int = Field(ge=0)
    highest_seen_manifest_digest: str | None = Field(default=None, pattern=r"^[0-9a-f]{64}$")
    last_advance_at: int = Field(ge=0)

    @field_validator("installed_version", "highest_seen_version")
    @classmethod
    def canonical_version(cls, value: str) -> str:
        _version_tuple(value)
        return value

    @model_validator(mode="after")
    def monotonic_state(self) -> "UpdateVerificationState":
        if self.highest_seen_sequence < self.installed_sequence:
            raise ValueError("highest-seen sequence cannot precede installed sequence")
        if _version_tuple(self.highest_seen_version) < _version_tuple(self.installed_version):
            raise ValueError("highest-seen version cannot precede installed version")
        if self.highest_seen_sequence > 0 and self.highest_seen_manifest_digest is None:
            raise ValueError("highest-seen update state requires an exact manifest digest")
        return self


def _parse_manifest(value: Mapping[str, Any]) -> UpdateManifest:
    try:
        return UpdateManifest.model_validate(dict(value))
    except PydanticValidationError as exc:
        raise ValidationError("update manifest schema rejected") from exc


def _parse_signatures(values: Sequence[Mapping[str, str]]) -> tuple[UpdateSignature, ...]:
    if not 1 <= len(values) <= MAX_UPDATE_KEYS * 2:
        raise ValidationError("update signature count outside profile")
    try:
        return tuple(UpdateSignature.model_validate(dict(value)) for value in values)
    except PydanticValidationError as exc:
        raise ValidationError("update signature schema rejected") from exc


def verify_threshold_manifest(
    manifest: Mapping[str, Any],
    signatures: Sequence[Mapping[str, str]],
    trusted_update_root: UpdateTrustRoot,
    *,
    state: UpdateVerificationState,
    now: int,
    future_skew: int = 60,
) -> UpdateManifest:
    """Verify schema, update-only threshold, freshness, rollback, and freeze.

    ``trusted_update_root`` is deliberately a separate type rather than a
    generic key mapping.  Its positive threshold is bounded by both a small
    profile maximum and the exact update key set.
    """

    if not isinstance(trusted_update_root, UpdateTrustRoot) or not isinstance(state, UpdateVerificationState):
        raise ValidationError("typed update root and verification state are required")
    if not isinstance(now, int) or isinstance(now, bool) or now < 0:
        raise ValidationError("update verification time is invalid")
    if future_skew < 0 or future_skew > 300:
        raise ValidationError("update future skew outside profile")
    if trusted_update_root.expires_at <= now:
        raise AuthenticationError("trusted update root is expired")
    if state.last_advance_at > now + future_skew:
        raise AuthenticationError("update monotonic state is from the future")
    parsed = _parse_manifest(manifest)
    if parsed.root_version != trusted_update_root.root_version:
        raise AuthenticationError("update manifest root version mismatch")
    if parsed.published_at > now + future_skew or parsed.expires_at <= now:
        raise AuthenticationError("update manifest is outside its validity window")
    if parsed.expires_at - parsed.published_at > trusted_update_root.max_manifest_lifetime_seconds:
        raise AuthenticationError("update manifest validity window exceeds the trusted root policy")

    signed_value = parsed.model_dump(mode="json", by_alias=True)
    valid: set[str] = set()
    for item in _parse_signatures(signatures):
        if item.key_id not in trusted_update_root.keys or item.key_id in valid:
            continue
        try:
            verify_signature(
                trusted_update_root.keys[item.key_id],
                UPDATE_SIGNATURE_PURPOSE,
                signed_value,
                item.signature,
            )
        except Exception:
            continue
        valid.add(item.key_id)
    if len(valid) < trusted_update_root.threshold:
        raise AuthenticationError("update threshold signature was not satisfied")

    installed = _version_tuple(state.installed_version)
    candidate = _version_tuple(parsed.version)
    minimum = _version_tuple(parsed.minimum_installed_version)
    maximum = _version_tuple(parsed.maximum_installed_version)
    if not minimum <= installed <= maximum:
        raise AuthenticationError("installed version is outside the signed compatibility range")
    if parsed.release_sequence < state.installed_sequence or candidate < installed:
        raise AuthenticationError("update rollback was rejected")
    if parsed.release_sequence == state.installed_sequence and candidate != installed:
        raise AuthenticationError("update sequence/version mismatch")

    digest = canonical_digest(signed_value)
    highest = _version_tuple(state.highest_seen_version)
    if parsed.release_sequence < state.highest_seen_sequence or candidate < highest:
        raise AuthenticationError("previously observed update metadata cannot be rolled back")
    if parsed.release_sequence == state.highest_seen_sequence:
        if candidate != highest or digest != state.highest_seen_manifest_digest:
            raise AuthenticationError("same-sequence update metadata equivocation detected")
        if now - state.last_advance_at > trusted_update_root.max_freeze_seconds:
            raise AuthenticationError("update metadata freeze window exceeded")
    else:
        if candidate <= highest or candidate <= installed:
            raise AuthenticationError("new update sequence must advance the version")
    return parsed
