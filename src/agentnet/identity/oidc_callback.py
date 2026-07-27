"""Strict OAuth authorization-response shape parsing.

Recognized fields are projected into strict models. Unique extension parameters
are ignored as required by OAuth 2.0; duplicate names and ambiguous success/error
shapes fail closed before any transaction or token exchange.
"""

from __future__ import annotations

from collections.abc import Iterable

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError

from agentnet.errors import AuthenticationError


class OIDCCallbackSuccess(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    code: str = Field(min_length=1, max_length=4_096)
    state: str = Field(min_length=32, max_length=512)


class OIDCCallbackError(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    error: str = Field(min_length=1, max_length=256)
    state: str = Field(min_length=32, max_length=512)
    error_description: str | None = Field(default=None, min_length=1, max_length=1_024)
    error_uri: str | None = Field(default=None, min_length=1, max_length=2_048)


OIDCCallback = OIDCCallbackSuccess | OIDCCallbackError
_ERROR_METADATA = frozenset({"error_description", "error_uri"})


def parse_oidc_callback_pairs(pairs: Iterable[tuple[str, str]]) -> OIDCCallback:
    """Parse one decoded OAuth callback without trusting extension metadata."""

    materialized = tuple(pairs)
    names = tuple(name for name, _value in materialized)
    if len(names) != len(set(names)):
        raise AuthenticationError("OIDC callback parameters are invalid")
    values = dict(materialized)
    has_code = "code" in values
    has_error = "error" in values
    has_error_metadata = bool(_ERROR_METADATA.intersection(values))

    try:
        if has_code and not has_error and not has_error_metadata:
            return OIDCCallbackSuccess.model_validate(
                {name: values[name] for name in ("code", "state") if name in values}
            )
        if has_error and not has_code:
            return OIDCCallbackError.model_validate(
                {
                    name: values[name]
                    for name in ("error", "state", "error_description", "error_uri")
                    if name in values
                }
            )
    except PydanticValidationError as exc:
        raise AuthenticationError("OIDC callback parameters are invalid") from exc
    raise AuthenticationError("OIDC callback parameters are invalid")


__all__ = [
    "OIDCCallback",
    "OIDCCallbackError",
    "OIDCCallbackSuccess",
    "parse_oidc_callback_pairs",
]
