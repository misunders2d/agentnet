"""Canonical OIDC endpoint pin policy shared by config and runtime validation."""

from __future__ import annotations

import ipaddress


FORBIDDEN_OIDC_ENDPOINT_NETWORKS = tuple(
    ipaddress.ip_network(value)
    for value in (
        "0.0.0.0/8",
        "127.0.0.0/8",
        "169.254.0.0/16",
        "192.0.2.0/24",
        "198.18.0.0/15",
        "198.51.100.0/24",
        "203.0.113.0/24",
        "224.0.0.0/4",
        "240.0.0.0/4",
        "::/128",
        "::1/128",
        "::ffff:0:0/96",
        "64:ff9b::/96",
        "64:ff9b:1::/48",
        "100::/64",
        "2001::/32",
        "2001:db8::/32",
        "2002::/16",
        "fe80::/10",
        "ff00::/8",
    )
)


def canonical_private_endpoint_network(value: str) -> str:
    if not isinstance(value, str) or not value or "%" in value:
        raise ValueError("OIDC private endpoint CIDR pin is invalid")
    try:
        network = ipaddress.ip_network(value, strict=False)
    except ValueError as exc:
        raise ValueError("OIDC private endpoint CIDR pin is invalid") from exc
    if (
        str(network) != value
        or not network.is_private
        or any(
            network.version == forbidden.version and network.overlaps(forbidden)
            for forbidden in FORBIDDEN_OIDC_ENDPOINT_NETWORKS
        )
    ):
        raise ValueError("OIDC private endpoint CIDR pins must be canonical private networks")
    return value


def canonical_endpoint_address(value: str) -> str:
    if not isinstance(value, str) or not value or "%" in value:
        raise ValueError("OIDC endpoint address pin is invalid")
    try:
        address = ipaddress.ip_address(value)
    except ValueError as exc:
        raise ValueError("OIDC endpoint address pin is invalid") from exc
    if (
        str(address) != value
        or address.is_unspecified
        or address.is_multicast
        or address.is_loopback
        or address.is_link_local
        or address.is_reserved
        or any(
            address.version == forbidden.version and address in forbidden
            for forbidden in FORBIDDEN_OIDC_ENDPOINT_NETWORKS
        )
    ):
        raise ValueError("OIDC endpoint address pins must be canonical safe unicast addresses")
    return value


__all__ = [
    "FORBIDDEN_OIDC_ENDPOINT_NETWORKS",
    "canonical_endpoint_address",
    "canonical_private_endpoint_network",
]
