from __future__ import annotations

import pytest

from a2a.types import (
    AgentCard,
    MutualTlsSecurityScheme,
    SecurityRequirement,
    SecurityScheme,
    StringList,
)

from agentnet.errors import AuthenticationError, ValidationError
from agentnet.gateways.a2a import select_card_security_requirement


def security_scheme() -> SecurityScheme:
    return SecurityScheme(mtls_security_scheme=MutualTlsSecurityScheme())


def card_with_alternatives() -> AgentCard:
    card = AgentCard(name="agent", description="security test", version="1")
    for name in ("api", "oauth", "mtls"):
        card.security_schemes[name].CopyFrom(security_scheme())
    card.security_requirements.extend(
        [
            SecurityRequirement(
                schemes={
                    "api": StringList(),
                    "oauth": StringList(list=["read", "write"]),
                }
            ),
            SecurityRequirement(schemes={"mtls": StringList()}),
        ]
    )
    return card


def test_alternatives_are_or_and_schemes_and_scopes_within_one_are_and() -> None:
    card = card_with_alternatives()
    fallback = select_card_security_requirement(
        card,
        available_scheme_scopes={"oauth": {"read"}, "mtls": set()},
        locally_allowed_schemes={"api", "oauth", "mtls"},
    )
    first = select_card_security_requirement(
        card,
        available_scheme_scopes={
            "api": set(),
            "oauth": {"read", "write", "extra"},
            "mtls": set(),
        },
        locally_allowed_schemes={"api", "oauth", "mtls"},
    )

    assert fallback.alternative_index == 1
    assert fallback.schemes == ("mtls",)
    assert first.alternative_index == 0
    assert set(first.schemes) == {"api", "oauth"}
    assert first.scopes["oauth"] == ("read", "write")


def test_missing_one_scheme_or_scope_fails_the_whole_alternative() -> None:
    card = card_with_alternatives()
    with pytest.raises(AuthenticationError):
        select_card_security_requirement(
            card,
            available_scheme_scopes={"oauth": {"read"}},
            locally_allowed_schemes={"api", "oauth"},
        )


def test_requirement_cannot_reference_undefined_scheme() -> None:
    card = AgentCard(name="agent", description="security test", version="1")
    card.security_requirements.append(
        SecurityRequirement(schemes={"undefined": StringList()})
    )
    with pytest.raises(ValidationError):
        select_card_security_requirement(
            card,
            available_scheme_scopes={"undefined": set()},
            locally_allowed_schemes={"undefined"},
        )


def test_anonymous_requires_explicit_local_permission() -> None:
    card = AgentCard(name="agent", description="security test", version="1")
    with pytest.raises(AuthenticationError):
        select_card_security_requirement(
            card,
            available_scheme_scopes={},
            locally_allowed_schemes=set(),
        )
    selected = select_card_security_requirement(
        card,
        available_scheme_scopes={},
        locally_allowed_schemes=set(),
        allow_anonymous=True,
    )
    assert selected.anonymous is True
    assert selected.alternative_index == -1
