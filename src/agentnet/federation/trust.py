"""Non-transitive trust assertions."""

from __future__ import annotations

from agentnet.errors import AuthorizationError


def require_direct_bilateral(*, host_domain_id: str, admitted_home_domain_id: str, asserted_home_domain_id: str) -> None:
    if (
        not host_domain_id
        or not asserted_home_domain_id
        or host_domain_id == asserted_home_domain_id
        or admitted_home_domain_id != asserted_home_domain_id
    ):
        raise AuthorizationError("transitive or mismatched federation trust is forbidden")
