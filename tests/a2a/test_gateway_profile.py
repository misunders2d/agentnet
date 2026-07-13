from __future__ import annotations

import pytest

from a2a.types import AgentCapabilities, AgentCard, AgentInterface

from agentnet.errors import ValidationError
from agentnet.gateways.a2a import (
    A2A_WIRE_VERSION,
    HTTP_JSON_BINDING,
    JSONRPC_BINDING,
    OpaqueAgentRoute,
    SSRFPolicy,
    build_exported_agent_card,
    generate_opaque_route,
    select_preferred_interface,
    strict_agent_card,
    validate_outbound_url,
    validate_redirect_chain,
)


GLOBAL_ADDRESS = "93.184.216.34"
ROUTE_TOKEN = "A" * 43


def resolver(host: str, port: int) -> tuple[str, ...]:
    assert port == 443
    return (GLOBAL_ADDRESS,)


def route() -> OpaqueAgentRoute:
    return OpaqueAgentRoute(
        route_token=ROUTE_TOKEN,
        logical_agent_id="logical-agent-1",
        domain_id="corp.example",
    )


def template_card() -> AgentCard:
    return AgentCard(
        name="Public logical agent",
        description="Bounded A2A endpoint",
        version="2026.07",
        capabilities=AgentCapabilities(streaming=True),
        default_input_modes=["text/plain"],
        default_output_modes=["text/plain"],
    )


def policy() -> SSRFPolicy:
    return SSRFPolicy(allowed_hosts=frozenset({"agents.example"}))


def test_exported_card_has_exact_preferred_bindings_and_opaque_tenant() -> None:
    exported = build_exported_agent_card(
        template_card(),
        route=route(),
        public_base_url="https://agents.example",
    )
    interfaces = list(exported.supported_interfaces)
    assert [item.protocol_binding for item in interfaces] == [HTTP_JSON_BINDING, JSONRPC_BINDING]
    assert [item.protocol_version for item in interfaces] == [A2A_WIRE_VERSION, A2A_WIRE_VERSION]
    assert [item.tenant for item in interfaces] == [ROUTE_TOKEN, ROUTE_TOKEN]
    assert interfaces[0].url == f"https://agents.example/a2a/{ROUTE_TOKEN}"
    assert interfaces[1].url == f"https://agents.example/a2a/{ROUTE_TOKEN}/rpc"
    assert "logical-agent-1" not in interfaces[0].url
    assert "logical-agent-1" not in interfaces[1].url


def test_http_json_is_preferred_even_if_card_lists_jsonrpc_first() -> None:
    exported = build_exported_agent_card(
        template_card(),
        route=route(),
        public_base_url="https://agents.example",
    )
    reversed_card = AgentCard()
    reversed_card.CopyFrom(exported)
    reversed_card.ClearField("supported_interfaces")
    reversed_card.supported_interfaces.extend(reversed(list(exported.supported_interfaces)))

    selected = select_preferred_interface(
        reversed_card,
        expected_tenant=ROUTE_TOKEN,
        policy=policy(),
        resolver=resolver,
    )
    assert selected.protocol_binding == HTTP_JSON_BINDING
    assert selected.protocol_version == "1.0"


@pytest.mark.parametrize("version", ["", "0.3", "1.1"])
def test_missing_legacy_or_non_exact_version_never_downgrades(version: str) -> None:
    card = template_card()
    card.supported_interfaces.append(
        AgentInterface(
            url="https://agents.example/rpc",
            protocol_binding=JSONRPC_BINDING,
            protocol_version=version,
            tenant=ROUTE_TOKEN,
        )
    )
    with pytest.raises(ValidationError):
        strict_agent_card(card, policy=policy(), resolver=resolver)


def test_unofficial_binding_literal_is_not_accepted() -> None:
    card = template_card()
    card.supported_interfaces.append(
        AgentInterface(
            url="https://agents.example/rpc",
            protocol_binding="HTTP_JSON",
            protocol_version="1.0",
            tenant=ROUTE_TOKEN,
        )
    )
    with pytest.raises(ValidationError):
        strict_agent_card(card, policy=policy(), resolver=resolver)


def test_duplicate_binding_is_rejected_as_ambiguous() -> None:
    card = template_card()
    card.supported_interfaces.extend(
        [
            AgentInterface(
                url="https://agents.example/one",
                protocol_binding=HTTP_JSON_BINDING,
                protocol_version="1.0",
                tenant=ROUTE_TOKEN,
            ),
            AgentInterface(
                url="https://agents.example/two",
                protocol_binding=HTTP_JSON_BINDING,
                protocol_version="1.0",
                tenant=ROUTE_TOKEN,
            ),
        ]
    )
    with pytest.raises(ValidationError):
        strict_agent_card(card, policy=policy(), resolver=resolver)


def test_tenant_mismatch_is_rejected() -> None:
    exported = build_exported_agent_card(
        template_card(),
        route=route(),
        public_base_url="https://agents.example",
    )
    with pytest.raises(ValidationError):
        strict_agent_card(
            exported,
            expected_tenant="B" * 43,
            policy=policy(),
            resolver=resolver,
        )


@pytest.mark.parametrize(
    "url",
    [
        "http://agents.example/path",
        "https://user:pass@agents.example/path",
        "https://agents.example/path#fragment",
        "https://localhost/path",
        "https://agents.example:8443/path",
        "https://evil.example/path",
    ],
)
def test_ssrf_policy_rejects_unsafe_url_shapes(url: str) -> None:
    with pytest.raises(ValidationError):
        validate_outbound_url(url, policy=policy(), resolver=resolver)


def test_ssrf_policy_rejects_any_private_dns_answer() -> None:
    with pytest.raises(ValidationError):
        validate_outbound_url(
            "https://agents.example/path",
            policy=policy(),
            resolver=lambda host, port: (GLOBAL_ADDRESS, "127.0.0.1"),
        )


def test_ssrf_policy_returns_pinned_addresses_and_revalidates_redirects() -> None:
    validated = validate_outbound_url(
        "https://agents.example/path?opaque=1",
        policy=policy(),
        resolver=resolver,
    )
    chain = validate_redirect_chain(
        ["https://agents.example/one", "https://agents.example/two"],
        policy=policy(),
        resolver=resolver,
    )
    assert validated.addresses == (GLOBAL_ADDRESS,)
    assert validated.host == "agents.example"
    assert len(chain) == 2


def test_generated_route_is_opaque_and_unrelated_to_logical_agent() -> None:
    generated = generate_opaque_route(logical_agent_id="sensitive-agent-name", domain_id="corp.example")
    assert _route_token_is_opaque(generated.route_token)
    assert "sensitive" not in generated.route_token


def _route_token_is_opaque(value: str) -> bool:
    return len(value) >= 32 and all(character.isalnum() or character in "_-" for character in value)
