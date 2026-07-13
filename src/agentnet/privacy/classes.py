"""Data-class capabilities and forbidden implicit functions."""

from __future__ import annotations

from agentnet.errors import AuthorizationError
from agentnet.operations.policy_defaults import ConfidentialityPolicy
from agentnet.protocol.models import Classification


CLASS_CAPABILITIES: dict[Classification, frozenset[str]] = {
    Classification.C0_PUBLIC: frozenset({"gateway_read", "abuse_scan"}),
    Classification.C1_INTERNAL: frozenset({"managed_search", "moderation", "dlp", "scanner", "backup"}),
    Classification.C2_RESTRICTED: frozenset({"isolated_scanner", "isolated_tool", "restricted_backup"}),
    Classification.C3_SEALED: frozenset({"member_decrypt"}),
}


def capabilities_for(
    classification: Classification,
    *,
    policy: ConfidentialityPolicy | None = None,
) -> frozenset[str]:
    configured = policy or ConfidentialityPolicy()
    if classification is Classification.C1_INTERNAL and configured.c1_processing == "isolated_service":
        return frozenset({"isolated_scanner", "isolated_tool", "restricted_backup"})
    return CLASS_CAPABILITIES[classification]


def permits(
    classification: Classification,
    capability: str,
    *,
    policy: ConfidentialityPolicy | None = None,
) -> bool:
    return capability in capabilities_for(classification, policy=policy)


class ConfidentialityEnforcer:
    def __init__(self, policy: ConfidentialityPolicy) -> None:
        self.policy = policy

    def processing_profile(self, classification: Classification) -> str:
        return self.policy.processing_profile(classification)

    def permits(self, classification: Classification, capability: str) -> bool:
        return permits(classification, capability, policy=self.policy)

    def require_processing(self, classification: Classification, capability: str) -> str:
        if not self.permits(classification, capability):
            raise AuthorizationError(
                f"{capability} is not an allowed {classification.value} processing capability"
            )
        return self.processing_profile(classification)
