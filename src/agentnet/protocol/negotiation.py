"""Authenticated profile negotiation with no silent security downgrade."""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass

from agentnet.errors import ValidationError


@dataclass(frozen=True, slots=True)
class ProfileOffer:
    protocol_versions: tuple[str, ...]
    schema_profiles: tuple[str, ...]
    critical_extensions: frozenset[str]
    features: frozenset[str]
    status_epoch: int

    def __post_init__(self) -> None:
        collections = (self.protocol_versions, self.schema_profiles, self.critical_extensions, self.features)
        if self.status_epoch < 0 or any(not value for collection in collections for value in collection):
            raise ValidationError("profile offer contains an invalid epoch or empty identifier")
        if len(set(self.protocol_versions)) != len(self.protocol_versions) or len(set(self.schema_profiles)) != len(self.schema_profiles):
            raise ValidationError("profile offer contains duplicate ordered preferences")


def negotiate_profile(local: ProfileOffer, remote: ProfileOffer, *, understood_critical: Iterable[str] = ()) -> dict[str, object]:
    unknown = remote.critical_extensions - frozenset(understood_critical)
    if unknown:
        raise ValidationError(f"unknown critical extensions: {sorted(unknown)}")
    unsupported_local_critical = local.critical_extensions - remote.features
    if unsupported_local_critical:
        raise ValidationError(f"remote does not support required local extensions: {sorted(unsupported_local_critical)}")
    protocols = [version for version in local.protocol_versions if version in remote.protocol_versions]
    schemas = [profile for profile in local.schema_profiles if profile in remote.schema_profiles]
    if not protocols or not schemas:
        raise ValidationError("no mutually allowed protocol/schema profile")
    return {
        "protocol_version": protocols[0],
        "schema_profile": schemas[0],
        "features": sorted(local.features & remote.features),
        "remote_status_epoch": remote.status_epoch,
    }
