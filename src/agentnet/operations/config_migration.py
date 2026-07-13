"""Strict first-release configuration loading and identity-rebinding fences."""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

from agentnet.errors import GateBlocked, ValidationError
from agentnet.operations.config import ExtensionConfig
from agentnet.security.signatures import canonical_digest


CURRENT_CONFIG_SCHEMA = "1.0"
SENSITIVE_KEY_FRAGMENTS = ("password", "private_key", "access_token", "refresh_token")
SECRET_REFERENCE_SUFFIXES = ("_path", "_file", "_env", "_ref")
REBIND_FIELDS = (
    "domain_id",
    "public_base_url",
    "service_audience",
    "database_url_env",
    "enrolled_harness_id",
    "enrolled_credential_id",
    "local_bindings",
    "oidc_enrollment",
)


@dataclass(frozen=True, slots=True)
class ConfigRebindingPlan:
    changed_fields: tuple[str, ...]
    previous_digest: str
    candidate_digest: str
    acknowledgement_digest: str

    @property
    def required(self) -> bool:
        return bool(self.changed_fields)


def _reject_secret_fields(value: Any, *, path: tuple[str, ...] = ()) -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            normalized = str(key).lower()
            names_secret_value = any(
                fragment in normalized for fragment in SENSITIVE_KEY_FRAGMENTS
            )
            is_reference = normalized.endswith(SECRET_REFERENCE_SUFFIXES)
            if names_secret_value and not is_reference:
                raise ValidationError(
                    "configuration migration refuses embedded secret material"
                )
            _reject_secret_fields(child, path=(*path, str(key)))
    elif isinstance(value, list):
        for index, child in enumerate(value):
            _reject_secret_fields(child, path=(*path, str(index)))


def load_config_json(payload: str) -> ExtensionConfig:
    """Load only the exact first-release schema; aliases and N-1 shapes fail."""

    try:
        value = json.loads(payload)
    except json.JSONDecodeError as exc:
        raise ValidationError("configuration is not valid JSON") from exc
    if not isinstance(value, dict):
        raise ValidationError("configuration document must be an object")
    _reject_secret_fields(value)
    if value.get("schema_version") != CURRENT_CONFIG_SCHEMA:
        raise GateBlocked(
            "config_schema",
            "configuration must use the exact first-release schema",
        )
    try:
        return ExtensionConfig.model_validate_json(payload, strict=True)
    except Exception as exc:
        raise ValidationError("configuration failed strict current-schema validation") from exc


def plan_config_rebinding(
    previous: ExtensionConfig,
    candidate: ExtensionConfig,
) -> ConfigRebindingPlan:
    before = previous.redacted_export()
    after = candidate.redacted_export()
    changed = tuple(field for field in REBIND_FIELDS if before.get(field) != after.get(field))
    previous_digest = canonical_digest(before)
    candidate_digest = canonical_digest(after)
    acknowledgement = canonical_digest(
        {
            "changed_fields": list(changed),
            "previous_digest": previous_digest,
            "candidate_digest": candidate_digest,
            "purpose": "agentnet.config.rebinding.v1",
        }
    )
    return ConfigRebindingPlan(
        changed_fields=changed,
        previous_digest=previous_digest,
        candidate_digest=candidate_digest,
        acknowledgement_digest=acknowledgement,
    )


def require_config_rebinding(
    previous: ExtensionConfig,
    candidate: ExtensionConfig,
    *,
    acknowledgement_digest: str | None,
) -> ConfigRebindingPlan:
    plan = plan_config_rebinding(previous, candidate)
    if plan.required and acknowledgement_digest != plan.acknowledgement_digest:
        raise GateBlocked(
            "config_rebind_required",
            "security-bound configuration changed without exact rebinding acknowledgement",
        )
    return plan


__all__ = [
    "CURRENT_CONFIG_SCHEMA",
    "ConfigRebindingPlan",
    "load_config_json",
    "plan_config_rebinding",
    "require_config_rebinding",
]
