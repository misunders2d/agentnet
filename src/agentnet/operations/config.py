"""Portable, fail-closed runtime configuration."""

from __future__ import annotations

import os
import ipaddress
from datetime import datetime
from enum import StrEnum
from pathlib import Path
from typing import Any, Literal
from urllib.parse import urlsplit

from pydantic import BaseModel, ConfigDict, Field, field_serializer, model_validator

from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.endpoint_policy import (
    canonical_endpoint_address,
    canonical_private_endpoint_network,
)
from agentnet.operations.policy_defaults import SecurePolicyDefaults


class RuntimeProfile(StrEnum):
    LOCAL_CONFORMANCE = "local_conformance"
    ALWAYS_ON_SERVER_AGENT = "always_on_server_agent"


class OIDCTokenEndpointAuthMethod(StrEnum):
    NONE = "none"
    CLIENT_SECRET_POST = "client_secret_post"
    CLIENT_SECRET_BASIC = "client_secret_basic"


class FeatureFlags(BaseModel):
    model_config = ConfigDict(extra="forbid")

    semantic_workers: bool = False
    public_a2a: bool = False
    federation: bool = False
    sealed_rooms: bool = False
    peer_mesh: bool = False
    protected_effects: bool = False
    local_bindings: bool = False


A2A_ACTIONS = frozenset(
    {
        "a2a.task.get",
        "a2a.task.list",
        "a2a.task.cancel",
        "a2a.message.send",
        "a2a.message.stream",
        "a2a.push.create",
        "a2a.push.get",
        "a2a.push.list",
        "a2a.push.delete",
        "a2a.task.subscribe",
        "a2a.card.extended",
    }
)


class A2AAgentCardConfig(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    name: str = Field(min_length=1, max_length=128)
    description: str = Field(min_length=1, max_length=1_024)
    version: str = Field(min_length=1, max_length=64)
    streaming: bool = True
    push_notifications: bool = False


class A2AStandingGrantConfig(BaseModel):
    """Non-secret route exposure grant; corporate data authority remains in the policy store."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    grant_id: str = Field(min_length=1, max_length=256)
    allowed_actions: frozenset[str] = Field(min_length=1)
    allowed_peer_namespaces: frozenset[str] = frozenset()
    allowed_output_sinks: frozenset[str] = Field(min_length=1)
    expires_at: datetime
    revoked_at: datetime | None = None
    revision: int = Field(default=1, ge=1)

    @model_validator(mode="after")
    def validate_scope(self) -> "A2AStandingGrantConfig":
        if not self.allowed_actions <= A2A_ACTIONS:
            raise ValueError("A2A standing grant contains an unsupported action")
        if self.expires_at.tzinfo is None or (
            self.revoked_at is not None and self.revoked_at.tzinfo is None
        ):
            raise ValueError("A2A standing grant times must be timezone-aware")
        return self


def _validate_private_key_reference(path: Path, *, label: str) -> Path:
    rendered = str(path)
    if (
        not rendered
        or len(rendered) > 4_096
        or "PRIVATE KEY" in rendered.upper()
        or any(ord(character) < 0x20 for character in rendered)
    ):
        raise ValueError(f"{label} must be a non-secret filesystem reference")
    return path


class A2ASigningCredentialConfig(BaseModel):
    """One explicit successor in an enrolled harness credential lineage."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    credential_id: str = Field(min_length=1, max_length=256)
    private_key_path: Path

    @model_validator(mode="after")
    def reject_embedded_key_material(self) -> "A2ASigningCredentialConfig":
        _validate_private_key_reference(
            self.private_key_path,
            label="A2A successor private_key_path",
        )
        return self


class A2ASigningIdentityConfig(BaseModel):
    """Anchor and explicit successors for one enrolled harness signing lineage."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    harness_id: str = Field(min_length=1, max_length=256)
    credential_id: str = Field(min_length=1, max_length=256)
    private_key_path: Path
    successors: tuple[A2ASigningCredentialConfig, ...] = Field(default=(), max_length=64)

    @model_validator(mode="after")
    def reject_embedded_key_material(self) -> "A2ASigningIdentityConfig":
        _validate_private_key_reference(self.private_key_path, label="A2A private_key_path")
        credential_ids = (self.credential_id, *(item.credential_id for item in self.successors))
        if len(set(credential_ids)) != len(credential_ids):
            raise ValueError("A2A signing credential lineage contains a duplicate credential")
        return self

    @property
    def credential_lineage(self) -> tuple[A2ASigningCredentialConfig, ...]:
        return (
            A2ASigningCredentialConfig(
                credential_id=self.credential_id,
                private_key_path=self.private_key_path,
            ),
            *self.successors,
        )


class A2AServiceConfig(BaseModel):
    """Explicit native A2A service configuration with no secret material."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    route_token: str = Field(min_length=32, max_length=128, pattern=r"^[A-Za-z0-9_-]+$")
    recipient_harness_id: str = Field(min_length=1, max_length=256)
    card: A2AAgentCardConfig
    standing_grant: A2AStandingGrantConfig
    signing_identity: A2ASigningIdentityConfig
    callback_allowed_hosts: frozenset[str] = frozenset()
    callback_allowed_ports: frozenset[int] = frozenset({443})
    allow_loopback_callback_http_lab: bool = False

    @model_validator(mode="after")
    def validate_callback_profile(self) -> "A2AServiceConfig":
        if any(port < 1 or port > 65_535 for port in self.callback_allowed_ports):
            raise ValueError("A2A callback port is invalid")
        if self.card.push_notifications and not self.callback_allowed_hosts:
            raise ValueError("A2A push notifications require explicit callback_allowed_hosts")
        if not self.card.push_notifications and self.standing_grant.allowed_actions.intersection(
            {"a2a.push.create", "a2a.push.get", "a2a.push.list", "a2a.push.delete"}
        ):
            raise ValueError("A2A standing grant cannot enable push operations when the card disables push")
        if not self.card.streaming and self.standing_grant.allowed_actions.intersection(
            {"a2a.message.stream", "a2a.task.subscribe"}
        ):
            raise ValueError("A2A standing grant cannot enable stream operations when the card disables streaming")
        if self.signing_identity.harness_id != self.recipient_harness_id:
            raise ValueError("A2A signing identity must be the exact exported recipient harness")
        return self


class LocalBindingConfig(BaseModel):
    """Owner-provisioned Unix IPC references; contains no capability bytes."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    socket_path: Path = Path("runtime/agentnet-local.sock")
    # Linux sockaddr_un paths are limited to 107 encoded bytes plus NUL.  Keep
    # the ordinary default deliberately short so valid owner data roots retain
    # useful path budget; final composed paths are still checked fail closed.
    mcp_bootstrap_socket_path: Path = Path("runtime/mcp.sock")
    capability_root_path: Path = Path("secrets/ipc-capability-root.key")
    capability_ttl_seconds: int = Field(default=300, ge=30, le=3600)
    max_frame_bytes: int = Field(default=1_048_576, ge=1024, le=16_777_216)

    @model_validator(mode="after")
    def safe_references(self) -> "LocalBindingConfig":
        for path in (
            self.socket_path,
            self.mcp_bootstrap_socket_path,
            self.capability_root_path,
        ):
            rendered = str(path)
            if (
                not rendered
                or len(rendered) > 4_096
                or "PRIVATE KEY" in rendered.upper()
                or any(ord(character) < 0x20 for character in rendered)
                or (not path.is_absolute() and ".." in path.parts)
            ):
                raise ValueError("local binding paths must be bounded filesystem references")
        if len(
            {
                self.socket_path,
                self.mcp_bootstrap_socket_path,
                self.capability_root_path,
            }
        ) != 3:
            raise ValueError("local binding paths must be distinct")
        return self


class RelaySigningIdentityConfig(BaseModel):
    """Exact enrolled ordinary-agent signer backed by an owner-only key file."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    harness_id: str = Field(min_length=1, max_length=256)
    credential_id: str = Field(min_length=1, max_length=256)
    private_key_path: Path

    @model_validator(mode="after")
    def filesystem_reference_only(self) -> "RelaySigningIdentityConfig":
        rendered = str(self.private_key_path)
        if (
            not rendered
            or len(rendered) > 4_096
            or "PRIVATE KEY" in rendered.upper()
            or any(ord(character) < 0x20 for character in rendered)
        ):
            raise ValueError("relay private_key_path must be a non-secret filesystem reference")
        return self


class RelayPeerConfig(BaseModel):
    """One exact bilateral peer pin with owner-file key versions."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    relay_harness_id: str = Field(min_length=1, max_length=256)
    signing_key_id: str = Field(min_length=16, max_length=256)
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    key_versions: tuple["RelayPeerKeyConfig", ...] = Field(min_length=1)

    @model_validator(mode="after")
    def exact_public_pin_and_file_reference(self) -> "RelayPeerConfig":
        if "PRIVATE KEY" in self.public_key_pem or "PUBLIC KEY" not in self.public_key_pem:
            raise ValueError("relay peer pins must contain a public key only")
        if public_key_thumbprint(self.public_key_pem) != self.signing_key_id:
            raise ValueError("relay peer signing key identifier must match its public-key pin")
        ids = [key.key_id for key in self.key_versions]
        epochs = [key.key_epoch for key in self.key_versions]
        if len(set(ids)) != len(ids) or len(set(epochs)) != len(epochs):
            raise ValueError("relay peer key identifiers and epochs must be unique")
        if sum(key.provisioned_state == "active" for key in self.key_versions) != 1:
            raise ValueError("relay peer key versions require exactly one provisioned active key")
        return self


class RelayPeerKeyConfig(BaseModel):
    """Non-secret lifecycle metadata for one owner-only 256-bit key file."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    key_id: str = Field(pattern=r"^[a-f0-9]{64}$")
    key_epoch: int = Field(ge=1)
    bilateral_key_path: Path
    provisioned_state: Literal["pending", "active", "overlap"]
    not_before: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    overlap_until: int | None = Field(default=None, ge=1)

    @model_validator(mode="after")
    def bounded_lifecycle_and_file_reference(self) -> "RelayPeerKeyConfig":
        rendered = str(self.bilateral_key_path)
        if (
            not rendered
            or len(rendered) > 4_096
            or "PRIVATE KEY" in rendered.upper()
            or any(ord(character) < 0x20 for character in rendered)
        ):
            raise ValueError("relay bilateral key path must be a non-secret filesystem reference")
        if self.expires_at <= self.not_before:
            raise ValueError("relay peer key validity interval is invalid")
        if self.provisioned_state == "overlap":
            if self.overlap_until is None or not self.not_before < self.overlap_until < self.expires_at:
                raise ValueError("relay overlap key requires a bounded overlap interval")
        elif self.overlap_until is not None:
            raise ValueError("only relay overlap keys may declare overlap_until")
        return self


RelayPeerConfig.model_rebuild()


class RelayServiceConfig(BaseModel):
    """Non-inert peer-mesh relay configuration for this ordinary extension."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    signing_identity: RelaySigningIdentityConfig
    peers: tuple[RelayPeerConfig, ...] = Field(min_length=1, max_length=256)

    @model_validator(mode="after")
    def unique_bilateral_peers(self) -> "RelayServiceConfig":
        domains = [peer.domain_id for peer in self.peers]
        harnesses = [peer.relay_harness_id for peer in self.peers]
        key_ids = [peer.signing_key_id for peer in self.peers]
        if len(set(domains)) != len(domains):
            raise ValueError("relay peer domains must be unique")
        if len(set(harnesses)) != len(harnesses):
            raise ValueError("relay peer harness pins must be unique")
        if len(set(key_ids)) != len(key_ids):
            raise ValueError("relay peer signing-key pins must be unique")
        return self


class IndependentApproverConfig(BaseModel):
    """Public trust anchor for a separately operated WebAuthn approval service."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    principal_id: str = Field(min_length=1, max_length=256)
    authority_kind: Literal["human", "guest"] = "human"
    signer_key_id: str = Field(min_length=16, max_length=256)
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    allowed_purposes: frozenset[str] = Field(min_length=1)

    @field_serializer("allowed_purposes")
    def stable_allowed_purposes(self, value: frozenset[str]) -> list[str]:
        return sorted(value)

    @model_validator(mode="after")
    def public_key_only(self) -> "IndependentApproverConfig":
        if "PRIVATE KEY" in self.public_key_pem or "PUBLIC KEY" not in self.public_key_pem:
            raise ValueError("independent approver trust anchor must be a public key")
        if any(not value or len(value) > 256 for value in self.allowed_purposes):
            raise ValueError("independent approver purpose is invalid")
        return self


class ApprovalServiceClientConfig(BaseModel):
    """Non-secret Core client reference for independent approval broker."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    origin: str = Field(min_length=8, max_length=2048)
    service_credential_env: str = Field(pattern=r"^[A-Z_][A-Z0-9_]{0,127}$")
    approver_principal_id: str = Field(min_length=1, max_length=256)
    request_timeout_seconds: float = Field(default=5.0, ge=1.0, le=30.0)
    maximum_response_bytes: int = Field(default=262_144, ge=4096, le=1_048_576)

    @model_validator(mode="after")
    def exact_https_origin(self) -> "ApprovalServiceClientConfig":
        try:
            parsed = urlsplit(self.origin)
            port = parsed.port
        except ValueError as exc:
            raise ValueError("approval service origin is invalid") from exc
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.path not in {"", "/"}
            or parsed.query
            or parsed.fragment
        ):
            raise ValueError("approval service origin must be one exact HTTPS origin")
        hostname = parsed.hostname.lower()
        rendered = f"[{hostname}]" if ":" in hostname else hostname
        canonical = f"https://{rendered}"
        if port not in {None, 443}:
            canonical += f":{port}"
        if self.origin.rstrip("/") != canonical:
            raise ValueError("approval service origin must use canonical spelling")
        return self


class OIDCEnrollmentConfig(BaseModel):
    """Non-secret provider and independent approval trust configuration."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    issuer: str = Field(min_length=8, max_length=512)
    client_id: str = Field(min_length=1, max_length=512)
    redirect_uri: str = Field(min_length=8, max_length=2_048)
    audience: str | None = Field(default=None, min_length=1, max_length=512)
    token_endpoint_auth_method: OIDCTokenEndpointAuthMethod = OIDCTokenEndpointAuthMethod.NONE
    client_secret_env: str | None = Field(
        default=None,
        pattern=r"^[A-Z][A-Z0-9_]{2,127}$",
    )
    allowed_endpoint_origins: tuple[str, ...] = Field(default=(), max_length=32)
    allowed_private_endpoint_cidrs: tuple[str, ...] = Field(default=(), max_length=64)
    pinned_endpoint_addresses: tuple[str, ...] = Field(default=(), max_length=128)
    allowed_signing_algorithms: tuple[Literal["RS256", "ES256"], ...] = ("RS256",)
    pinned_jwk_thumbprints: dict[str, str] = Field(default_factory=dict)
    binding_assurance: Literal["os_bound", "hardware_bound"] = "hardware_bound"
    verifier_id: str = Field(min_length=1, max_length=128)
    trusted_approvers: tuple[IndependentApproverConfig, ...] = Field(min_length=1, max_length=32)
    approval_service: ApprovalServiceClientConfig | None = None

    @model_validator(mode="after")
    def exact_provider_profile(self) -> "OIDCEnrollmentConfig":
        confidential = self.token_endpoint_auth_method is not OIDCTokenEndpointAuthMethod.NONE
        if confidential and self.client_secret_env is None:
            raise ValueError("confidential OIDC authentication requires client_secret_env")
        if not confidential and self.client_secret_env is not None:
            raise ValueError("public OIDC authentication cannot configure client_secret_env")
        if not self.allowed_signing_algorithms or len(set(self.allowed_signing_algorithms)) != len(
            self.allowed_signing_algorithms
        ):
            raise ValueError("OIDC signing algorithms must be a non-empty unique tuple")
        if len(self.pinned_jwk_thumbprints) > 128 or any(
            not key_id
            or len(key_id) > 512
            or len(digest) != 64
            or any(character not in "0123456789abcdef" for character in digest)
            for key_id, digest in self.pinned_jwk_thumbprints.items()
        ):
            raise ValueError("OIDC JWK pins must be lowercase SHA-256 digests")
        if len({item.signer_key_id for item in self.trusted_approvers}) != len(
            self.trusted_approvers
        ):
            raise ValueError("independent approver signer identifiers must be unique")
        origins: list[str] = []
        for value in self.allowed_endpoint_origins:
            try:
                parsed = urlsplit(value)
                port = parsed.port
            except ValueError as exc:
                raise ValueError("OIDC endpoint origin is invalid") from exc
            if (
                parsed.scheme != "https"
                or not parsed.hostname
                or parsed.username is not None
                or parsed.password is not None
                or parsed.path not in {"", "/"}
                or parsed.query
                or parsed.fragment
            ):
                raise ValueError("OIDC endpoint origins must be exact HTTPS origins")
            rendered_host = f"[{parsed.hostname.lower()}]" if ":" in parsed.hostname else parsed.hostname.lower()
            canonical = f"https://{rendered_host}"
            if port not in {None, 443}:
                canonical += f":{port}"
            if value.rstrip("/") != canonical:
                raise ValueError("OIDC endpoint origins must use canonical spelling")
            origins.append(canonical)
        if len(set(origins)) != len(origins):
            raise ValueError("OIDC endpoint origins must be unique")
        private_networks: list[ipaddress.IPv4Network | ipaddress.IPv6Network] = []
        for value in self.allowed_private_endpoint_cidrs:
            canonical = canonical_private_endpoint_network(value)
            private_networks.append(ipaddress.ip_network(canonical, strict=True))
        if len(set(private_networks)) != len(private_networks):
            raise ValueError("OIDC private endpoint CIDR pins must be unique")
        endpoint_addresses: list[ipaddress.IPv4Address | ipaddress.IPv6Address] = []
        for value in self.pinned_endpoint_addresses:
            canonical = canonical_endpoint_address(value)
            endpoint_addresses.append(ipaddress.ip_address(canonical))
        if len(set(endpoint_addresses)) != len(endpoint_addresses):
            raise ValueError("OIDC endpoint address pins must be unique")
        private_addresses = [address for address in endpoint_addresses if not address.is_global]
        if private_networks or private_addresses:
            if not self.allowed_endpoint_origins:
                raise ValueError("private OIDC endpoints require explicit endpoint origins")
            if not self.pinned_jwk_thumbprints:
                raise ValueError("private OIDC endpoints require exact JWK thumbprint pins")
        return self


class ScannerTrustConfig(BaseModel):
    """Pinned maintained-scanner public evidence accepted by artifact release."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    trusted_public_keys: dict[str, str] = Field(min_length=1, max_length=128)
    required_engine: str = Field(min_length=1, max_length=256)
    required_rules_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    required_profile_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    max_attestation_age_seconds: int = Field(default=300, ge=1, le=86_400)
    allowed_future_skew_seconds: int = Field(default=30, ge=0, le=300)
    revoked_key_epochs: frozenset[tuple[str, int]] = frozenset()

    @model_validator(mode="after")
    def public_keys_only(self) -> "ScannerTrustConfig":
        if any(
            not reference
            or len(reference) > 512
            or "PRIVATE KEY" in pem
            or "PUBLIC KEY" not in pem
            for reference, pem in self.trusted_public_keys.items()
        ):
            raise ValueError("scanner trust entries must contain named public keys")
        if any(not scanner_id or epoch < 1 for scanner_id, epoch in self.revoked_key_epochs):
            raise ValueError("revoked scanner key epoch is invalid")
        return self


class FederationPublicKeyPin(BaseModel):
    """One exact, non-secret federation verification-key pin."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    key_id: str = Field(min_length=16, max_length=256)
    public_key_pem: str = Field(min_length=128, max_length=16_384)

    @model_validator(mode="after")
    def public_key_only_and_exact(self) -> "FederationPublicKeyPin":
        if "PRIVATE KEY" in self.public_key_pem or "PUBLIC KEY" not in self.public_key_pem:
            raise ValueError("federation trust pins must contain public keys only")
        if public_key_thumbprint(self.public_key_pem) != self.key_id:
            raise ValueError("federation trust key identifier must match the pinned public key")
        return self


class FederationTrustConfig(BaseModel):
    """Static verification anchors; never a source of imported authority."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    home_domain_keys: tuple[FederationPublicKeyPin, ...] = Field(min_length=1, max_length=256)
    host_policy_keys: tuple[FederationPublicKeyPin, ...] = Field(min_length=1, max_length=32)

    @model_validator(mode="after")
    def unique_exact_pins(self) -> "FederationTrustConfig":
        home = [(pin.domain_id, pin.key_id) for pin in self.home_domain_keys]
        host = [(pin.domain_id, pin.key_id) for pin in self.host_policy_keys]
        if len(set(home)) != len(home) or len(set(host)) != len(host):
            raise ValueError("federation trust pins must be unique by domain and key identifier")
        if set(home).intersection(host):
            raise ValueError("home-domain and host-policy trust roles must use distinct pins")
        return self

    @property
    def trusted_domain_key_map(self) -> dict[tuple[str, str], str]:
        return {(pin.domain_id, pin.key_id): pin.public_key_pem for pin in self.home_domain_keys}

    @property
    def host_policy_key_map(self) -> dict[tuple[str, str], str]:
        return {(pin.domain_id, pin.key_id): pin.public_key_pem for pin in self.host_policy_keys}


class BackupSealKeyConfig(BaseModel):
    """One public-only backup-seal verification pin and lifecycle epoch."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    key_id: str = Field(min_length=16, max_length=128)
    key_epoch: int = Field(ge=1)
    public_key_pem: str = Field(min_length=128, max_length=16_384)
    not_before: int = Field(ge=1)
    retired_at: int | None = Field(default=None, ge=1)
    revoked_at: int | None = Field(default=None, ge=1)

    @model_validator(mode="after")
    def exact_public_pin_and_lifecycle(self) -> "BackupSealKeyConfig":
        if "PRIVATE KEY" in self.public_key_pem or "PUBLIC KEY" not in self.public_key_pem:
            raise ValueError("backup seal pins must contain a public key only")
        if public_key_thumbprint(self.public_key_pem) != self.key_id:
            raise ValueError("backup seal key identifier must match the public-key pin")
        if self.retired_at is not None and self.retired_at <= self.not_before:
            raise ValueError("backup seal retirement must follow key activation")
        if self.revoked_at is not None and self.revoked_at <= self.not_before:
            raise ValueError("backup seal revocation must follow key activation")
        if (
            self.retired_at is not None
            and self.revoked_at is not None
            and self.revoked_at < self.retired_at
        ):
            raise ValueError("backup seal revocation cannot precede retirement")
        return self


class BackupTrustConfig(BaseModel):
    """Monotonic public trust root for independently signed backup manifests."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    trust_root_revision: int = Field(ge=1)
    minimum_key_epoch: int = Field(ge=1)
    active_signer_key_id: str = Field(min_length=16, max_length=128)
    keys: tuple[BackupSealKeyConfig, ...] = Field(min_length=1, max_length=64)

    @model_validator(mode="after")
    def monotonic_unique_trust_root(self) -> "BackupTrustConfig":
        identifiers = [key.key_id for key in self.keys]
        epochs = [key.key_epoch for key in self.keys]
        if len(set(identifiers)) != len(identifiers) or len(set(epochs)) != len(epochs):
            raise ValueError("backup seal key identifiers and epochs must be unique")
        active = next(
            (key for key in self.keys if key.key_id == self.active_signer_key_id),
            None,
        )
        if (
            active is None
            or active.key_epoch < self.minimum_key_epoch
            or active.key_epoch != max(epochs)
            or active.retired_at is not None
            or active.revoked_at is not None
        ):
            raise ValueError(
                "backup trust root requires the unique highest-epoch non-revoked active signer"
            )
        if any(
            key.key_id != self.active_signer_key_id
            and key.retired_at is None
            and key.revoked_at is None
            for key in self.keys
        ):
            raise ValueError("every non-active backup seal key must be explicitly retired or revoked")
        return self

    def key_by_id(self, key_id: str) -> BackupSealKeyConfig | None:
        return next((key for key in self.keys if key.key_id == key_id), None)


class ExtensionConfig(BaseModel):
    """Configuration containing no credentials or private keys.

    Secret values are intentionally absent.  Credential material is rebound at
    enrollment and lives in the selected credential store.
    """

    model_config = ConfigDict(extra="forbid")

    schema_version: Literal["1.0"] = "1.0"
    profile: RuntimeProfile = RuntimeProfile.LOCAL_CONFORMANCE
    domain_id: str = Field(default="local.example", pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    data_dir: Path = Path(".agentnet")
    database_url: str = "sqlite:///.agentnet/core.sqlite3"
    database_url_env: str | None = Field(default=None, pattern=r"^[A-Z][A-Z0-9_]{2,127}$")
    artifact_backend: Literal["filesystem", "postgres-manifest"] = "filesystem"
    artifact_dir: Path = Path(".agentnet/artifacts")
    public_base_url: str = "http://127.0.0.1:8080"
    service_audience: str | None = None
    allowed_clock_skew_seconds: int = Field(default=60, ge=0, le=300)
    proof_max_age_seconds: int = Field(default=300, ge=30, le=900)
    max_request_bytes: int = Field(default=16_777_216, ge=1024, le=16_777_216)
    replay_retention_seconds: int = Field(default=900, ge=300, le=86_400)
    runtime_instance_id: str = Field(default="agent-local", pattern=r"^[a-z0-9][a-z0-9._-]{2,127}$")
    enrolled_harness_id: str | None = Field(default=None, min_length=1, max_length=256)
    enrolled_credential_id: str | None = Field(default=None, min_length=1, max_length=256)
    server_agent_capabilities: frozenset[ServerAgentCapability] = frozenset(
        {ServerAgentCapability.OFFLINE_CUSTODY, ServerAgentCapability.ARTIFACT_STORAGE}
    )
    a2a: A2AServiceConfig | None = None
    local_bindings: LocalBindingConfig | None = None
    relay: RelayServiceConfig | None = None
    oidc_enrollment: OIDCEnrollmentConfig | None = None
    scanner_trust: ScannerTrustConfig | None = None
    federation_trust: FederationTrustConfig | None = None
    backup_trust: BackupTrustConfig | None = None
    postgres_connect_timeout_seconds: int = Field(default=5, ge=1, le=30)
    postgres_statement_timeout_ms: int = Field(default=15_000, ge=1_000, le=120_000)
    postgres_lock_timeout_ms: int = Field(default=5_000, ge=500, le=30_000)
    postgres_lease_ttl_seconds: int = Field(default=30, ge=10, le=300)
    postgres_auto_migrate: bool = True
    postgres_recovery_topology: bool = False
    artifact_recovery_scan_limit: int = Field(default=10_000, ge=1, le=1_000_000)
    features: FeatureFlags = Field(default_factory=FeatureFlags)
    policies: SecurePolicyDefaults = Field(default_factory=SecurePolicyDefaults)
    component_evidence: dict[str, str] = Field(default_factory=dict)

    @model_validator(mode="after")
    def validate_security_floor(self) -> "ExtensionConfig":
        if self.backup_trust is not None and self.backup_trust.domain_id != self.domain_id:
            raise ValueError("backup trust root must bind the exact local domain")
        try:
            origin = urlsplit(self.public_base_url)
            port = origin.port
        except ValueError as exc:
            raise ValueError("public_base_url is not a valid service origin") from exc
        if (
            origin.scheme not in {"http", "https"}
            or not origin.hostname
            or origin.username is not None
            or origin.password is not None
            or origin.path not in {"", "/"}
            or origin.query
            or origin.fragment
        ):
            raise ValueError("public_base_url must be an http(s) origin without credentials, path, query, or fragment")
        default_port = 80 if origin.scheme == "http" else 443
        host = origin.hostname.lower()
        rendered_host = f"[{host}]" if ":" in host else host
        canonical_origin = f"{origin.scheme}://{rendered_host}"
        if port not in {None, default_port}:
            canonical_origin += f":{port}"
        if self.public_base_url.rstrip("/") != canonical_origin:
            raise ValueError("public_base_url must use its canonical origin spelling")
        if origin.scheme == "http":
            try:
                loopback = ipaddress.ip_address(origin.hostname).is_loopback
            except ValueError:
                loopback = False
            if not loopback:
                raise ValueError("http public_base_url is allowed only on an explicit loopback address")
        if self.service_audience is not None:
            if (
                len(self.service_audience) < 3
                or len(self.service_audience) > 512
                or self.service_audience != self.service_audience.strip()
                or any(ord(character) < 0x21 for character in self.service_audience)
            ):
                raise ValueError("service_audience is not canonical")
        if self.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            if not self.database_url.startswith("postgresql://"):
                raise ValueError("always_on_server_agent requires PostgreSQL")
            parsed_database = urlsplit(self.database_url)
            if parsed_database.password is not None:
                raise ValueError("server-agent database credentials must be injected through database_url_env")
            if self.artifact_backend != "postgres-manifest":
                raise ValueError("always_on_server_agent requires PostgreSQL-authoritative artifact manifests")
            if self.postgres_recovery_topology:
                from agentnet.storage.postgres import validate_postgres_recovery_dsn

                try:
                    validate_postgres_recovery_dsn(self.database_url)
                except Exception as exc:
                    raise ValueError(
                        "postgres_recovery_topology requires a password-free multi-host DSN "
                        "with target_session_attrs=read-write"
                    ) from exc
            if (
                not self.enrolled_harness_id or not self.enrolled_credential_id
            ) and self.oidc_enrollment is None:
                raise ValueError("always_on_server_agent requires an externally enrolled harness and credential")
            required_capabilities = {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
            }
            if not required_capabilities <= self.server_agent_capabilities:
                raise ValueError("always_on_server_agent requires mailbox custody and artifact recovery capability limits")
            if self.features.protected_effects and ServerAgentCapability.EFFECT_EXECUTOR not in self.server_agent_capabilities:
                raise ValueError("protected_effects requires the explicit effect_executor capability limit")
        if self.features.public_a2a:
            if ServerAgentCapability.A2A_GATEWAY not in self.server_agent_capabilities:
                raise ValueError("public_a2a requires the explicit a2a_gateway capability limit")
            if self.a2a is None:
                raise ValueError("public_a2a requires explicit route, card, standing grant, and signing-key references")
        elif self.a2a is not None:
            raise ValueError("A2A service configuration is inert unless public_a2a is enabled")
        if self.features.local_bindings:
            if ServerAgentCapability.LOCAL_BINDING not in self.server_agent_capabilities:
                raise ValueError("local_bindings requires the explicit local_binding capability limit")
            if self.local_bindings is None:
                raise ValueError("local_bindings requires explicit socket and capability-root references")
        elif self.local_bindings is not None:
            raise ValueError("local binding configuration is inert unless local_bindings is enabled")
        if self.features.peer_mesh:
            relay_capabilities = {
                ServerAgentCapability.RELAY,
                ServerAgentCapability.STORE_AND_FORWARD,
                ServerAgentCapability.OFFLINE_CUSTODY,
            }
            if not relay_capabilities <= self.server_agent_capabilities:
                raise ValueError(
                    "peer_mesh requires explicit relay, store_and_forward, and offline_custody capability limits"
                )
            if self.relay is None:
                raise ValueError("peer_mesh requires exact relay signing and bilateral peer configuration")
            if any(peer.domain_id == self.domain_id for peer in self.relay.peers):
                raise ValueError("relay peer domains cannot name the local ordinary extension domain")
        elif self.relay is not None:
            raise ValueError("relay configuration is inert unless peer_mesh is enabled")
        if self.features.federation:
            if ServerAgentCapability.FEDERATION not in self.server_agent_capabilities:
                raise ValueError("federation requires the explicit federation capability limit")
            if self.federation_trust is None:
                raise ValueError("federation requires explicit home-domain and host-policy public-key pins")
            if any(pin.domain_id == self.domain_id for pin in self.federation_trust.home_domain_keys):
                raise ValueError("federation home-domain pins cannot name the local host domain")
            if any(pin.domain_id != self.domain_id for pin in self.federation_trust.host_policy_keys):
                raise ValueError("federation host-policy pins must name the exact local host domain")
        elif self.federation_trust is not None:
            raise ValueError("federation trust configuration is inert unless federation is enabled")
        if self.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and self.a2a is not None:
            if (
                self.a2a.recipient_harness_id != self.enrolled_harness_id
                or self.a2a.signing_identity.harness_id != self.enrolled_harness_id
                or self.a2a.signing_identity.credential_id != self.enrolled_credential_id
            ):
                raise ValueError("always-on A2A must use the exact enrolled ordinary server-agent binding")
        if self.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and self.relay is not None:
            if (
                self.relay.signing_identity.harness_id != self.enrolled_harness_id
                or self.relay.signing_identity.credential_id != self.enrolled_credential_id
            ):
                raise ValueError("always-on relay must use the exact enrolled ordinary server-agent binding")
        if self.oidc_enrollment is not None:
            if self.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
                raise ValueError("production OIDC enrollment is available only on the always-on profile")
            expected_redirect = f"{self.public_base_url}/v1/enrollment/oidc/callback"
            if self.oidc_enrollment.redirect_uri != expected_redirect:
                raise ValueError("OIDC redirect URI must exactly match the ordinary extension callback")
            if any(
                "identity.enrollment.approve" not in approver.allowed_purposes
                for approver in self.oidc_enrollment.trusted_approvers
            ):
                raise ValueError("every configured enrollment approver must be trusted for enrollment")
            configured_approval_purposes = frozenset().union(
                *(approver.allowed_purposes for approver in self.oidc_enrollment.trusted_approvers)
            )
            required_ceremony_purposes = {
                "authorization.entitlement.bootstrap.approve",
                "authorization.elevation.approve",
                "identity.credential.recover.approve",
                "identity.harness.revoke.approve",
                "organization.relationship.accept",
            }
            missing_ceremony_purposes = required_ceremony_purposes - configured_approval_purposes
            if missing_ceremony_purposes:
                raise ValueError(
                    "configured OIDC approvers do not cover every mounted high-impact ceremony: "
                    + ", ".join(sorted(missing_ceremony_purposes))
                )
        if self.features.sealed_rooms and "mls" not in self.component_evidence:
            raise ValueError("sealed_rooms requires passed MLS component evidence")
        if self.features.peer_mesh and "peer_mesh" not in self.component_evidence:
            raise ValueError("peer_mesh requires partition/quorum evidence")
        if self.features.semantic_workers and "clean_worker" not in self.component_evidence:
            raise ValueError("semantic_workers requires clean-worker evidence")
        if self.features.federation and "federation" not in self.component_evidence:
            raise ValueError("federation requires bilateral lab evidence")
        return self

    @property
    def effective_service_audience(self) -> str:
        return self.service_audience or f"urn:agentnet:{self.domain_id}:corporate-api"

    @property
    def service_scheme(self) -> str:
        return urlsplit(self.public_base_url).scheme

    @property
    def service_authority(self) -> str:
        origin = urlsplit(self.public_base_url)
        host = origin.hostname or ""
        rendered_host = f"[{host}]" if ":" in host else host
        default_port = 80 if origin.scheme == "http" else 443
        return rendered_host if origin.port in {None, default_port} else f"{rendered_host}:{origin.port}"

    def require_feature(self, feature: str) -> None:
        if not getattr(self.features, feature, False):
            raise GateBlocked("feature_disabled", f"{feature} is disabled until its named gates pass")

    def redacted_export(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    def resolved_database_url(self) -> str:
        """Resolve a runtime DSN without ever serializing the injected value."""

        value = os.environ.get(self.database_url_env, "") if self.database_url_env else self.database_url
        if not value:
            raise GateBlocked("database_secret", "configured PostgreSQL DSN environment variable is absent")
        expected = "postgresql" if self.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT else urlsplit(self.database_url).scheme
        if urlsplit(value).scheme != expected:
            raise GateBlocked("database_secret", "runtime database DSN scheme does not match the configured backend")
        return value
