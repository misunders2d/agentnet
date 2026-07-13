from __future__ import annotations

import pytest
from pydantic import ValidationError

from agentnet.operations.policy_defaults import ElevationPolicy, SecurePolicyDefaults


def test_pd001_through_pd011_have_executable_conservative_defaults() -> None:
    policy = SecurePolicyDefaults()
    assert len(policy.digest) == 64
    assert policy.identity.principal_key == "oidc_issuer_subject"
    assert policy.enrollment_approval.primary_authentication == "webauthn_uv"
    assert policy.attenuation.harness_session_device_are_deny_only is True
    assert policy.elevation.high_impact_approval_threshold == 2
    assert policy.revocation.enforcement == "next_decision"
    assert policy.rooms.history_mode == "from_join"
    assert policy.confidentiality.c3_enabled_by_default is False
    assert policy.federation.non_transitive is True
    assert policy.outage.privileged_operations == "hold"
    assert policy.operations.accepted_durable_enabled is False
    assert policy.operations.declared_rpo_seconds is None
    assert policy.operations.target_rto_seconds is None
    assert policy.attention.routine_traffic == "silent"


def test_policy_bundle_is_configurable_but_cannot_weaken_high_impact_floor() -> None:
    configured = SecurePolicyDefaults(
        revision=2,
        elevation=ElevationPolicy(
            ordinary_approval_threshold=2,
            high_impact_approval_threshold=3,
            ordinary_ttl_seconds=600,
            high_impact_ttl_seconds=120,
        ),
    )
    assert configured.revision == 2
    with pytest.raises(ValidationError):
        SecurePolicyDefaults(
            elevation=ElevationPolicy(
                ordinary_approval_threshold=2,
                high_impact_approval_threshold=2,
            )
        )
