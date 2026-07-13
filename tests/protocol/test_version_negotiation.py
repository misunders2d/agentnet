from __future__ import annotations

import pytest

from agentnet.errors import ValidationError
from agentnet.protocol.negotiation import ProfileOffer, negotiate_profile


def offer(*, protocols=("1.1", "1.0"), schemas=("agentnet.v1",), critical=frozenset(), features=frozenset(), epoch=1):
    return ProfileOffer(
        protocol_versions=protocols,
        schema_profiles=schemas,
        critical_extensions=critical,
        features=features,
        status_epoch=epoch,
    )


def test_negotiation_uses_local_preference_and_intersection_only() -> None:
    result = negotiate_profile(
        offer(features=frozenset({"receipts", "rooms"})),
        offer(protocols=("1.0", "1.1"), features=frozenset({"receipts"}), epoch=8),
    )
    assert result == {
        "protocol_version": "1.1",
        "schema_profile": "agentnet.v1",
        "features": ["receipts"],
        "remote_status_epoch": 8,
    }


def test_negotiation_rejects_unknown_or_unsupported_critical_extensions() -> None:
    with pytest.raises(ValidationError):
        negotiate_profile(offer(), offer(critical=frozenset({"future"})))
    with pytest.raises(ValidationError):
        negotiate_profile(offer(critical=frozenset({"sealed"})), offer(), understood_critical={"sealed"})
    with pytest.raises(ValidationError):
        negotiate_profile(offer(protocols=("2.0",)), offer(protocols=("1.0",)))
