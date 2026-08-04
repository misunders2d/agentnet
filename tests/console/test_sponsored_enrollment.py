from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest
from pydantic import ValidationError

from agentnet.identity.sponsored_enrollment import SponsoredEnrollmentIntentRequest


def _request(**changes):
    values = {
        "intent_id": "intent-id-1234567890",
        "target_kind": "existing_person",
        "target_principal_id": "person-1",
        "invited_verified_email": None,
        "harness_kind": "laptop",
        "harness_display_name": "Field laptop",
        "requested_capabilities": ("message.send",),
        "expires_at": datetime.now(UTC) + timedelta(hours=1),
        "reason": "Add the person's second laptop",
    }
    values.update(changes)
    return SponsoredEnrollmentIntentRequest(**values)


def test_sponsored_intent_requires_exactly_one_identity_target() -> None:
    assert _request().target_principal_id == "person-1"
    with pytest.raises(ValidationError):
        _request(invited_verified_email="person@example.test")
    with pytest.raises(ValidationError):
        _request(target_kind="new_person", target_principal_id=None, invited_verified_email=None)


def test_sponsored_intent_rejects_noncanonical_capability_set() -> None:
    with pytest.raises(ValidationError):
        _request(requested_capabilities=("message.send", "message.send"))
