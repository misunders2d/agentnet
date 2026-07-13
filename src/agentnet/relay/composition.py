"""Fail-closed construction of relay services for the ordinary extension."""

from __future__ import annotations

import os
import secrets
import stat
import time
from pathlib import Path

from agentnet.authorization.policy import PolicyEngine
from agentnet.errors import GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding, public_key_thumbprint
from agentnet.mailbox.service import MailboxService
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.operations.quotas import QuotaService
from agentnet.relay.service import (
    RelayPeerKey,
    ServerAgentPeer,
    ServerAgentRelayService,
)
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.backend import StoreBackend


MAX_RELAY_SIGNING_KEY_BYTES = 16 * 1024


def _configured_path(config: ExtensionConfig, configured: Path) -> Path:
    if configured.is_absolute():
        return configured
    base = Path(os.path.abspath(config.data_dir))
    candidate = Path(os.path.abspath(config.data_dir / configured))
    if candidate != base and base not in candidate.parents:
        raise GateBlocked("relay_key_file", "relative relay key references must remain inside data_dir")
    current = base
    for component in candidate.relative_to(base).parts[:-1]:
        current /= component
        if current.is_symlink():
            raise GateBlocked("relay_key_file", "relay key paths cannot traverse symbolic links")
    return candidate


def _read_owner_file(
    config: ExtensionConfig,
    configured: Path,
    *,
    maximum_bytes: int,
    exact_bytes: int | None = None,
) -> bytes:
    path = _configured_path(config, configured)
    try:
        parent = path.parent.stat()
    except OSError as exc:
        raise GateBlocked("relay_key_file", "relay key directory is unavailable") from exc
    if path.parent.is_symlink() or parent.st_uid != os.geteuid() or parent.st_mode & 0o077:
        raise GateBlocked("relay_key_file", "relay key directory must be owner-only")
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as exc:
        raise GateBlocked("relay_key_file", "relay key file is unavailable") from exc
    try:
        metadata = os.fstat(descriptor)
        wrong_size = (
            metadata.st_size != exact_bytes
            if exact_bytes is not None
            else metadata.st_size < 1 or metadata.st_size > maximum_bytes
        )
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_nlink != 1
            or metadata.st_mode & 0o077
            or wrong_size
        ):
            raise GateBlocked("relay_key_file", "relay keys must be bounded owner-only regular files")
        value = os.read(descriptor, maximum_bytes + 1)
        if len(value) != metadata.st_size:
            raise GateBlocked("relay_key_file", "relay key file changed during its bounded read")
        return value
    finally:
        os.close(descriptor)


def create_server_agent_relay_service(
    config: ExtensionConfig,
    store: StoreBackend,
    *,
    mailbox: MailboxService,
    policy: PolicyEngine,
    admission: QuotaService,
) -> ServerAgentRelayService:
    """Load exact configured pins and construct one relay in the core process."""

    configured = config.relay
    if not config.features.peer_mesh or configured is None:
        raise GateBlocked("peer_mesh", "relay composition is disabled or incomplete")
    signing = configured.signing_identity
    try:
        private_pem = _read_owner_file(
            config,
            signing.private_key_path,
            maximum_bytes=MAX_RELAY_SIGNING_KEY_BYTES,
        )
        signer = P256KeyPair.from_private_pem(private_pem)
        binding = load_credential_binding(store, signing.credential_id)
        binding.require_active(now=int(time.time()))
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked("relay_signing_key", "relay signing key binding is unavailable") from exc
    if (
        binding.principal_id is None
        or binding.guest_id is not None
        or binding.domain_id != config.domain_id
        or binding.harness_id != signing.harness_id
        or binding.credential_id != signing.credential_id
        or not secrets.compare_digest(binding.key_id, signer.thumbprint)
        or not secrets.compare_digest(public_key_thumbprint(binding.public_key_pem), signer.thumbprint)
    ):
        raise GateBlocked("relay_signing_key", "relay signer does not match its enrolled human-owned harness")
    if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and binding.binding_assurance == "lab":
        raise GateBlocked("relay_signing_key", "always-on relay cannot use a lab signing binding")
    local_actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=binding.domain_id,
        principal_id=binding.principal_id,
        harness_id=binding.harness_id,
        credential_id=binding.credential_id,
        credential_epoch=binding.credential_epoch,
        binding_assurance=binding.binding_assurance,
    )
    peers: dict[str, ServerAgentPeer] = {}
    for peer in configured.peers:
        key_versions = tuple(
            RelayPeerKey(
                key_id=version.key_id,
                key_epoch=version.key_epoch,
                key=_read_owner_file(
                    config,
                    version.bilateral_key_path,
                    maximum_bytes=32,
                    exact_bytes=32,
                ),
                provisioned_state=version.provisioned_state,
                not_before=version.not_before,
                expires_at=version.expires_at,
                overlap_until=version.overlap_until,
            )
            for version in peer.key_versions
        )
        peers[peer.domain_id] = ServerAgentPeer(
            domain_id=peer.domain_id,
            relay_harness_id=peer.relay_harness_id,
            signing_key_id=peer.signing_key_id,
            public_key_pem=peer.public_key_pem,
            key_versions=key_versions,
        )
    return ServerAgentRelayService(
        store,
        local_actor=local_actor,
        local_signer=signer,
        peers=peers,
        runtime_capabilities=config.server_agent_capabilities,
        mailbox=mailbox,
        policy=policy,
        admission=admission,
    )
