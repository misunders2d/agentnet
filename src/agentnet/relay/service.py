"""Durable store-and-forward between ordinary enrolled server agents.

The relay process is an ordinary human-owned harness.  Its configured
capability, bilateral peer pin, and signatures can only attenuate; an exact
human policy decision is still required to stage outbound custody. Inbound
custody requires both the current target-domain collaboration scope resolved
under the host-local guest and the exact target grant.
"""

from __future__ import annotations

import json
import hashlib
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import NAMESPACE_URL, uuid4, uuid5

from pydantic import BaseModel, ConfigDict, Field, model_validator

from agentnet.authorization.evidence import (
    IssuanceAuthority,
    require_current_authority_decision,
)
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    PolicyEngine,
    validate_actor_state,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import envelope_digest, validate_event_digest
from agentnet.organization.assignment import (
    AssignmentRequest,
    AssignmentService,
    TaskIngressKind,
)
from agentnet.operations.quotas import QuotaService
from agentnet.protocol.models import Classification, DeliveryFact, EventEnvelope, EventType
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json, verify_signature
from agentnet.storage.backend import StoreBackend


RELAY_KEY_ROTATION_PURPOSE = "agentnet.server-relay.key-rotation.v1"
RELAY_KEY_REVOCATION_PURPOSE = "agentnet.server-relay.key-revocation.v1"
MAX_RELAY_KEY_OVERLAP_SECONDS = 3_600
MAX_RELAY_KEY_COMMAND_SECONDS = 300
_MAX_KEY_TIME = (1 << 62) - 1


def relay_peer_key_id(key: bytes) -> str:
    """Return the public identifier for an exact high-entropy peer key."""

    if len(key) != 32:
        raise ValueError("server-agent relay requires an exact 256-bit bilateral software key")
    return hashlib.sha256(key).hexdigest()


@dataclass(frozen=True, slots=True)
class RelayPeerKey:
    """Owner-file key material plus non-secret version/lifecycle metadata."""

    key_id: str
    key_epoch: int
    key: bytes
    provisioned_state: Literal["pending", "active", "overlap"] = "pending"
    not_before: int = 1
    expires_at: int = _MAX_KEY_TIME
    overlap_until: int | None = None

    def __post_init__(self) -> None:
        if self.key_id != relay_peer_key_id(self.key):
            raise ValueError("relay peer key identifier does not match its owner-file bytes")
        if self.key_epoch < 1:
            raise ValueError("relay peer key epoch must be positive")
        if self.not_before < 1 or self.expires_at <= self.not_before:
            raise ValueError("relay peer key validity interval is invalid")
        if self.provisioned_state == "overlap":
            if self.overlap_until is None or not self.not_before < self.overlap_until < self.expires_at:
                raise ValueError("overlap relay keys require a bounded overlap interval")
        elif self.overlap_until is not None:
            raise ValueError("only overlap relay keys may declare overlap_until")

    @property
    def fingerprint(self) -> str:
        return relay_peer_key_id(self.key)


@dataclass(frozen=True, slots=True)
class ServerAgentPeer:
    domain_id: str
    relay_harness_id: str
    signing_key_id: str
    public_key_pem: str
    key_versions: tuple[RelayPeerKey, ...]

    def __post_init__(self) -> None:
        if not self.domain_id or not self.relay_harness_id or not self.signing_key_id:
            raise ValueError("server-agent peer identifiers are required")
        versions = self.key_versions
        if not versions:
            raise ValueError("server-agent peer requires at least one provisioned key version")
        if len({key.key_id for key in versions}) != len(versions):
            raise ValueError("relay peer key identifiers must be unique")
        if len({key.key_epoch for key in versions}) != len(versions):
            raise ValueError("relay peer key epochs must be unique")
        if sum(key.provisioned_state == "active" for key in versions) != 1:
            raise ValueError("relay peer configuration requires exactly one initially active key")

    def key(self, key_id: str, key_epoch: int) -> RelayPeerKey:
        matches = [
            key
            for key in self.key_versions
            if key.key_id == key_id and key.key_epoch == key_epoch
        ]
        if len(matches) != 1:
            raise AuthenticationError("relay packet key version is not provisioned")
        return matches[0]


class RelayPeerKeyRotation(BaseModel):
    """Bilateral exact command activating one pre-provisioned key version."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    profile: Literal["agentnet.server-relay.key-rotation.v1"] = "agentnet.server-relay.key-rotation.v1"
    mutation_id: str = Field(min_length=16, max_length=128)
    domain_a_id: str = Field(min_length=1, max_length=128)
    domain_b_id: str = Field(min_length=1, max_length=128)
    relay_a_harness_id: str = Field(min_length=1, max_length=256)
    relay_b_harness_id: str = Field(min_length=1, max_length=256)
    initiator_domain_id: str = Field(min_length=1, max_length=128)
    initiator_relay_harness_id: str = Field(min_length=1, max_length=256)
    from_key_id: str = Field(pattern=r"^[a-f0-9]{64}$")
    from_key_epoch: int = Field(ge=1)
    to_key_id: str = Field(pattern=r"^[a-f0-9]{64}$")
    to_key_epoch: int = Field(ge=2)
    activate_at: int = Field(ge=1)
    overlap_until: int = Field(ge=1)
    new_key_expires_at: int = Field(ge=1)
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    reason: str = Field(pattern=r"^[a-z][a-z0-9_.-]{0,63}$")
    nonce: str = Field(min_length=24, max_length=256)

    @model_validator(mode="after")
    def coherent_rotation(self) -> "RelayPeerKeyRotation":
        if not self.domain_a_id < self.domain_b_id:
            raise ValueError("relay rotation domains must be distinct and canonically ordered")
        endpoints = {
            self.domain_a_id: self.relay_a_harness_id,
            self.domain_b_id: self.relay_b_harness_id,
        }
        if endpoints.get(self.initiator_domain_id) != self.initiator_relay_harness_id:
            raise ValueError("relay rotation initiator must be an exact endpoint")
        if self.to_key_epoch != self.from_key_epoch + 1 or self.to_key_id == self.from_key_id:
            raise ValueError("relay rotation must advance to one distinct next key epoch")
        if not self.issued_at <= self.activate_at < self.overlap_until < self.new_key_expires_at:
            raise ValueError("relay rotation activation/overlap/key-expiry interval is invalid")
        if self.overlap_until - self.activate_at > MAX_RELAY_KEY_OVERLAP_SECONDS:
            raise ValueError("relay key overlap exceeds the bounded maximum")
        if not self.issued_at < self.expires_at or self.expires_at - self.issued_at > MAX_RELAY_KEY_COMMAND_SECONDS:
            raise ValueError("relay rotation command lifetime is invalid")
        return self

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class RelayPeerKeyRevocation(BaseModel):
    """Unilateral local deny-only containment for a compromised peer key."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    profile: Literal["agentnet.server-relay.key-revocation.v1"] = "agentnet.server-relay.key-revocation.v1"
    mutation_id: str = Field(min_length=16, max_length=128)
    local_domain_id: str = Field(min_length=1, max_length=128)
    peer_domain_id: str = Field(min_length=1, max_length=128)
    local_relay_harness_id: str = Field(min_length=1, max_length=256)
    peer_relay_harness_id: str = Field(min_length=1, max_length=256)
    key_id: str = Field(pattern=r"^[a-f0-9]{64}$")
    key_epoch: int = Field(ge=1)
    reason: Literal["suspected_compromise", "confirmed_compromise", "peer_incident"]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    @model_validator(mode="after")
    def coherent_revocation(self) -> "RelayPeerKeyRevocation":
        if self.local_domain_id == self.peer_domain_id:
            raise ValueError("relay key revocation must name distinct domains")
        if not self.issued_at < self.expires_at or self.expires_at - self.issued_at > MAX_RELAY_KEY_COMMAND_SECONDS:
            raise ValueError("relay key revocation command lifetime is invalid")
        return self

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class RelayPacket(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    profile: Literal["agentnet.server-relay.packet.v2"] = "agentnet.server-relay.packet.v2"
    hop_count: Literal[1] = 1
    max_hops: Literal[1] = 1
    packet_id: str = Field(min_length=16, max_length=128)
    source_domain_id: str = Field(min_length=1)
    source_relay_harness_id: str = Field(min_length=1)
    source_key_id: str = Field(min_length=1)
    peer_key_id: str = Field(pattern=r"^[a-f0-9]{64}$")
    peer_key_epoch: int = Field(ge=1)
    target_domain_id: str = Field(min_length=1)
    target_relay_harness_id: str = Field(min_length=1)
    target_recipient_id: str = Field(min_length=1)
    target_grant_id: str = Field(min_length=1)
    target_collaboration_scope_id: str = Field(min_length=1, max_length=256)
    guest_pairwise_subject: str = Field(min_length=16, max_length=512)
    source_event_id: str = Field(min_length=1)
    source_event_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    ciphertext: str = Field(min_length=32)
    created_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    signature: str = Field(min_length=1)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude={"signature"})

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class ServerRelayReceipt(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    profile: Literal["agentnet.server-relay.receipt.v1"] = "agentnet.server-relay.receipt.v1"
    receipt_id: str = Field(min_length=16, max_length=128)
    packet_id: str = Field(min_length=16, max_length=128)
    packet_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    source_domain_id: str = Field(min_length=1)
    target_domain_id: str = Field(min_length=1)
    target_relay_harness_id: str = Field(min_length=1)
    target_key_id: str = Field(min_length=1)
    fact: Literal["accepted_local", "accepted_durable", "recipient_committed"]
    local_event_id: str | None = None
    created_at: int = Field(ge=1)
    signature: str = Field(min_length=1)

    @model_validator(mode="after")
    def coherent_fact(self) -> "ServerRelayReceipt":
        if self.fact == "recipient_committed" and self.local_event_id is None:
            raise ValueError("recipient_committed receipts require the exact local event")
        return self

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude={"signature"}, exclude_none=True)


class ServerAgentRelayService:
    def __init__(
        self,
        store: StoreBackend,
        *,
        local_actor: VerifiedActor,
        local_signer: P256KeyPair,
        peers: Mapping[str, ServerAgentPeer],
        runtime_capabilities: frozenset[ServerAgentCapability],
        mailbox: MailboxService | None = None,
        policy: PolicyEngine | None = None,
        admission: QuotaService | None = None,
        clock: Callable[[], int] = lambda: int(time.time()),
    ) -> None:
        if local_actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
            raise AuthorizationError("relay capability belongs to an ordinary enrolled human-owned agent")
        self.store = store
        self.local_actor = local_actor
        self.local_signer = local_signer
        self.peers = dict(peers)
        self.runtime_capabilities = frozenset(ServerAgentCapability(value) for value in runtime_capabilities)
        self.mailbox = mailbox
        self.policy = policy
        self.admission = admission
        self.assignments = (
            AssignmentService(
                store,
                collaboration_scopes=mailbox.collaboration_scopes,
                mailbox=mailbox,
                policy=policy,
            )
            if mailbox is not None and policy is not None
            else None
        )
        self.clock = clock
        derived_fact = mailbox.acceptance_fact if mailbox is not None else DeliveryFact.ACCEPTED_LOCAL
        self._custody_fact = derived_fact
        self._require_local_actor(ServerAgentCapability.RELAY)
        self._reconcile_peer_keys()

    @staticmethod
    def _enrolled_capabilities(value: str) -> frozenset[ServerAgentCapability]:
        try:
            decoded = json.loads(value)
        except (TypeError, ValueError) as exc:
            raise AuthenticationError("enrolled server-agent capabilities are malformed") from exc
        if isinstance(decoded, dict) and isinstance(decoded.get("server_agent_capabilities"), list):
            names = decoded["server_agent_capabilities"]
        elif isinstance(decoded, dict):
            names = [name for name, enabled in decoded.items() if enabled is True]
        elif isinstance(decoded, list):
            names = decoded
        else:
            raise AuthenticationError("enrolled server-agent capabilities are malformed")
        try:
            return frozenset(ServerAgentCapability(name) for name in names)
        except (TypeError, ValueError) as exc:
            raise AuthenticationError("enrolled server-agent capability is unknown") from exc

    def _require_local_actor(
        self,
        *required: ServerAgentCapability,
        connection: Any | None = None,
    ) -> None:
        """Revalidate identity and both capability attenuation layers.

        Configuration and enrollment capabilities are intersected.  Neither
        layer grants caller data authority; the policy/grant checks below still
        decide every message operation.
        """

        if connection is None:
            with self.store.transaction(immediate=False) as read_connection:
                self._require_local_actor(*required, connection=read_connection)
            return
        binding = load_credential_binding_from_connection(connection, self.local_actor.credential_id or "")
        binding.require_active(now=self.clock())
        if (
            binding.domain_id != self.local_actor.domain_id
            or binding.principal_id != self.local_actor.principal_id
            or binding.harness_id != self.local_actor.harness_id
            or binding.credential_id != self.local_actor.credential_id
            or binding.credential_epoch != self.local_actor.credential_epoch
            or binding.key_id != self.local_signer.thumbprint
        ):
            raise AuthenticationError("server-agent relay signer does not match its current enrolled credential")
        harness = connection.execute(
            "SELECT capabilities_json FROM harnesses WHERE harness_id=?",
            (self.local_actor.harness_id,),
        ).fetchone()
        if harness is None:
            raise AuthenticationError("server-agent relay harness is unavailable")
        enrolled = self._enrolled_capabilities(harness["capabilities_json"])
        effective = enrolled.intersection(self.runtime_capabilities)
        missing = frozenset(required).difference(effective)
        if missing:
            names = ",".join(sorted(capability.value for capability in missing))
            raise GateBlocked("server_agent_capability", f"ordinary server agent lacks capability attenuation: {names}")

    def _reconcile_peer_keys(self) -> None:
        """Bind owner-file material to durable non-secret lifecycle metadata.

        An empty local store records the configured version set and requires
        exactly one active key. Once metadata exists, additional material may
        only enter as ``pending`` and can become active only through a
        bilateral signed rotation. Missing material for a live durable key
        fails startup.
        """

        now = self.clock()
        with self.store.transaction() as connection:
            for peer in self.peers.values():
                rows = connection.execute(
                    """SELECT * FROM relay_peer_keys
                         WHERE local_domain_id=? AND peer_domain_id=?
                         ORDER BY key_epoch""",
                    (self.local_actor.domain_id, peer.domain_id),
                ).fetchall()
                first_bootstrap = not rows
                known = {str(row["key_id"]): row for row in rows}
                for key in peer.key_versions:
                    row = known.get(key.key_id)
                    if row is None:
                        if not first_bootstrap and key.provisioned_state != "pending":
                            raise AuthenticationError(
                                "new relay peer key material must remain pending until signed rotation"
                            )
                        bootstrap_digest = canonical_digest(
                            {
                                "profile": "agentnet.server-relay.key-bootstrap.v1",
                                "local_domain_id": self.local_actor.domain_id,
                                "peer_domain_id": peer.domain_id,
                                "key_id": key.key_id,
                                "key_epoch": key.key_epoch,
                                "state": key.provisioned_state,
                            }
                        )
                        connection.execute(
                            """INSERT INTO relay_peer_keys(
                                local_domain_id,peer_domain_id,key_id,key_epoch,key_fingerprint,state,
                                not_before,overlap_until,expires_at,revoked_at,rotation_digest,
                                created_at,updated_at
                            ) VALUES(?,?,?,?,?,?,?,?,?,NULL,?,?,?)""",
                            (
                                self.local_actor.domain_id,
                                peer.domain_id,
                                key.key_id,
                                key.key_epoch,
                                key.fingerprint,
                                key.provisioned_state,
                                key.not_before,
                                key.overlap_until,
                                key.expires_at,
                                bootstrap_digest,
                                now,
                                now,
                            ),
                        )
                        continue
                    if (
                        int(row["key_epoch"]) != key.key_epoch
                        or row["key_fingerprint"] != key.fingerprint
                        or int(row["not_before"]) != key.not_before
                        or int(row["expires_at"]) != key.expires_at
                    ):
                        raise AuthenticationError("relay owner-file key metadata conflicts with durable state")
                provisioned_ids = {key.key_id for key in peer.key_versions}
                missing_live = [
                    row
                    for row in rows
                    if row["state"] in {"pending", "active", "overlap"}
                    and int(row["expires_at"]) > now
                    and row["key_id"] not in provisioned_ids
                ]
                if missing_live:
                    raise GateBlocked(
                        "relay_peer_key",
                        "owner-file material is missing for a live durable relay key version",
                    )
                active = connection.execute(
                    """SELECT COUNT(*) AS count FROM relay_peer_keys
                         WHERE local_domain_id=? AND peer_domain_id=? AND state='active'""",
                    (self.local_actor.domain_id, peer.domain_id),
                ).fetchone()
                if first_bootstrap and int(active["count"]) != 1:
                    raise AuthenticationError("relay peer bootstrap must establish one active key")

    @staticmethod
    def _material_for_row(peer: ServerAgentPeer, row: Any) -> RelayPeerKey:
        material = peer.key(str(row["key_id"]), int(row["key_epoch"]))
        if material.fingerprint != row["key_fingerprint"]:
            raise AuthenticationError("relay peer key fingerprint does not match durable metadata")
        return material

    def _active_peer_key(self, connection: Any, peer: ServerAgentPeer, *, now: int) -> RelayPeerKey:
        rows = connection.execute(
            """SELECT * FROM relay_peer_keys
                 WHERE local_domain_id=? AND peer_domain_id=? AND state='active'""",
            (self.local_actor.domain_id, peer.domain_id),
        ).fetchall()
        if len(rows) != 1:
            raise GateBlocked("relay_peer_key", "relay peer has no unique active key version")
        row = rows[0]
        if not int(row["not_before"]) <= now < int(row["expires_at"]):
            raise GateBlocked("relay_peer_key", "relay peer active key is outside its validity interval")
        return self._material_for_row(peer, row)

    def _packet_peer_key(
        self,
        connection: Any,
        peer: ServerAgentPeer,
        packet: RelayPacket,
        *,
        now: int,
    ) -> RelayPeerKey:
        row = connection.execute(
            """SELECT * FROM relay_peer_keys
                 WHERE local_domain_id=? AND peer_domain_id=? AND key_id=? AND key_epoch=?""",
            (
                self.local_actor.domain_id,
                peer.domain_id,
                packet.peer_key_id,
                packet.peer_key_epoch,
            ),
        ).fetchone()
        if row is None:
            raise AuthenticationError("relay packet key version is unknown")
        state = str(row["state"])
        if state not in {"active", "overlap"} or row["revoked_at"] is not None:
            raise AuthenticationError("relay packet key version is not accepted")
        if not int(row["not_before"]) <= packet.created_at < int(row["expires_at"]):
            raise AuthenticationError("relay packet predates or outlives its key version")
        if state == "active":
            if now >= int(row["expires_at"]):
                raise AuthenticationError("relay packet key version has expired")
        else:
            overlap_until = row["overlap_until"]
            if overlap_until is None or now >= int(overlap_until):
                raise AuthenticationError("relay packet key overlap has ended")
            replacement = connection.execute(
                """SELECT not_before FROM relay_peer_keys
                     WHERE local_domain_id=? AND peer_domain_id=? AND state='active'""",
                (self.local_actor.domain_id, peer.domain_id),
            ).fetchone()
            if replacement is None or packet.created_at >= int(replacement["not_before"]):
                raise AuthenticationError("old relay key cannot authenticate post-rotation packets")
        return self._material_for_row(peer, row)

    def _validate_rotation_endpoints(
        self,
        peer: ServerAgentPeer,
        rotation: RelayPeerKeyRotation,
    ) -> None:
        domains = sorted((self.local_actor.domain_id, peer.domain_id))
        expected = {
            domains[0]: (
                self.local_actor.harness_id
                if domains[0] == self.local_actor.domain_id
                else peer.relay_harness_id
            ),
            domains[1]: (
                self.local_actor.harness_id
                if domains[1] == self.local_actor.domain_id
                else peer.relay_harness_id
            ),
        }
        if (
            rotation.domain_a_id != domains[0]
            or rotation.domain_b_id != domains[1]
            or rotation.relay_a_harness_id != expected[domains[0]]
            or rotation.relay_b_harness_id != expected[domains[1]]
        ):
            raise AuthenticationError("relay key rotation endpoint binding failed")

    @staticmethod
    def key_rotation_binding(
        *,
        peer_domain_id: str,
        rotation: RelayPeerKeyRotation,
    ) -> tuple[str, dict[str, Any]]:
        return f"relay-peer:{peer_domain_id}", {
            "schema": "agentnet.server-relay.key-rotation-authority.v1",
            "mutation_id": rotation.mutation_id,
            "manifest_digest": rotation.digest,
            "from_key_id": rotation.from_key_id,
            "from_key_epoch": rotation.from_key_epoch,
            "to_key_id": rotation.to_key_id,
            "to_key_epoch": rotation.to_key_epoch,
            "activate_at": rotation.activate_at,
            "overlap_until": rotation.overlap_until,
        }

    def rotate_peer_key(
        self,
        *,
        peer_domain_id: str,
        rotation: RelayPeerKeyRotation,
        local_signature: str,
        peer_signature: str,
        policy_decision_id: str,
    ) -> dict[str, Any]:
        """Atomically activate a pre-provisioned key after both relays sign.

        The old key authenticates only packets created before activation and
        only until the bounded overlap ends.  Durable queued packets are never
        rewritten or silently re-encrypted.
        """

        peer = self._peer(peer_domain_id)
        self._validate_rotation_endpoints(peer, rotation)
        now = self.clock()
        if (
            not rotation.issued_at <= now < rotation.expires_at
            or rotation.activate_at > now
            or now - rotation.activate_at > MAX_RELAY_KEY_COMMAND_SECONDS
        ):
            raise AuthenticationError("relay key rotation is stale or not yet active")
        verify_signature(
            self.local_signer.public_pem,
            RELAY_KEY_ROTATION_PURPOSE,
            rotation.signed_fields(),
            local_signature,
        )
        verify_signature(
            peer.public_key_pem,
            RELAY_KEY_ROTATION_PURPOSE,
            rotation.signed_fields(),
            peer_signature,
        )
        new_material = peer.key(rotation.to_key_id, rotation.to_key_epoch)
        if (
            new_material.not_before != rotation.activate_at
            or new_material.expires_at != rotation.new_key_expires_at
            or new_material.provisioned_state != "pending"
        ):
            raise AuthenticationError("relay rotation does not bind exact pre-provisioned key metadata")
        with self.store.transaction() as connection:
            self._require_local_actor(ServerAgentCapability.RELAY, connection=connection)
            resource, request = self.key_rotation_binding(
                peer_domain_id=peer_domain_id,
                rotation=rotation,
            )
            require_current_authority_decision(
                connection,
                authority=IssuanceAuthority(
                    actor=self.local_actor,
                    policy_decision_id=policy_decision_id,
                ),
                expected_action="server_agent.relay.key.rotate",
                expected_resource=resource,
                expected_request=request,
                when=datetime.fromtimestamp(now, UTC),
            )
            duplicate = connection.execute(
                """SELECT * FROM relay_peer_key_mutations
                     WHERE mutation_id=? OR manifest_digest=?""",
                (rotation.mutation_id, rotation.digest),
            ).fetchone()
            if duplicate is not None:
                if (
                    duplicate["mutation_id"] == rotation.mutation_id
                    and duplicate["manifest_digest"] == rotation.digest
                    and duplicate["mutation_type"] == "rotate"
                ):
                    return {
                        "peer_domain_id": peer_domain_id,
                        "active_key_id": rotation.to_key_id,
                        "active_key_epoch": rotation.to_key_epoch,
                        "overlap_until": rotation.overlap_until,
                        "duplicate": True,
                    }
                raise ConflictError("relay key rotation identifier or digest was already consumed")
            old = connection.execute(
                """SELECT * FROM relay_peer_keys
                     WHERE local_domain_id=? AND peer_domain_id=? AND key_id=? AND key_epoch=?""",
                (
                    self.local_actor.domain_id,
                    peer_domain_id,
                    rotation.from_key_id,
                    rotation.from_key_epoch,
                ),
            ).fetchone()
            new = connection.execute(
                """SELECT * FROM relay_peer_keys
                     WHERE local_domain_id=? AND peer_domain_id=? AND key_id=? AND key_epoch=?""",
                (
                    self.local_actor.domain_id,
                    peer_domain_id,
                    rotation.to_key_id,
                    rotation.to_key_epoch,
                ),
            ).fetchone()
            if old is None or old["state"] != "active" or old["revoked_at"] is not None:
                raise ConflictError("relay rotation source key is not the exact active epoch")
            if new is None or new["state"] != "pending" or new["revoked_at"] is not None:
                raise ConflictError("relay rotation target key is not exact pending material")
            if (
                int(new["not_before"]) != rotation.activate_at
                or int(new["expires_at"]) != rotation.new_key_expires_at
                or new["key_fingerprint"] != new_material.fingerprint
            ):
                raise AuthenticationError("relay rotation target key metadata drifted")
            old_update = connection.execute(
                """UPDATE relay_peer_keys
                      SET state='overlap',overlap_until=?,updated_at=?
                    WHERE local_domain_id=? AND peer_domain_id=? AND key_id=?
                      AND key_epoch=? AND state='active' AND revoked_at IS NULL""",
                (
                    rotation.overlap_until,
                    now,
                    self.local_actor.domain_id,
                    peer_domain_id,
                    rotation.from_key_id,
                    rotation.from_key_epoch,
                ),
            )
            new_update = connection.execute(
                """UPDATE relay_peer_keys
                      SET state='active',rotation_digest=?,updated_at=?
                    WHERE local_domain_id=? AND peer_domain_id=? AND key_id=?
                      AND key_epoch=? AND state='pending' AND revoked_at IS NULL""",
                (
                    rotation.digest,
                    now,
                    self.local_actor.domain_id,
                    peer_domain_id,
                    rotation.to_key_id,
                    rotation.to_key_epoch,
                ),
            )
            if old_update.rowcount != 1 or new_update.rowcount != 1:
                raise ConflictError("relay key rotation raced with another lifecycle mutation")
            connection.execute(
                """INSERT INTO relay_peer_key_mutations(
                    mutation_id,local_domain_id,peer_domain_id,mutation_type,from_key_id,to_key_id,
                    expected_from_epoch,resulting_epoch,manifest_digest,actor_json,
                    policy_decision_id,state,created_at
                ) VALUES(?,?,?,'rotate',?,?,?,?,?,?,?,'completed',?)""",
                (
                    rotation.mutation_id,
                    self.local_actor.domain_id,
                    peer_domain_id,
                    rotation.from_key_id,
                    rotation.to_key_id,
                    rotation.from_key_epoch,
                    rotation.to_key_epoch,
                    rotation.digest,
                    canonical_json(self.local_actor.audit_view()).decode("utf-8"),
                    policy_decision_id,
                    now,
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "server_agent.relay_peer_key_rotated",
                    "actor": self.local_actor.audit_view(),
                    "from_key_epoch": rotation.from_key_epoch,
                    "from_key_id": rotation.from_key_id,
                    "manifest_digest": rotation.digest,
                    "mutation_id": rotation.mutation_id,
                    "overlap_until": rotation.overlap_until,
                    "peer_domain_id": peer_domain_id,
                    "policy_decision_id": policy_decision_id,
                    "to_key_epoch": rotation.to_key_epoch,
                    "to_key_id": rotation.to_key_id,
                },
            )
        return {
            "peer_domain_id": peer_domain_id,
            "active_key_id": rotation.to_key_id,
            "active_key_epoch": rotation.to_key_epoch,
            "overlap_until": rotation.overlap_until,
            "duplicate": False,
            "audit_hash": audit_hash,
        }

    def _fail_queued_packets_for_key_in_transaction(
        self,
        connection: Any,
        *,
        peer_domain_id: str,
        key_id: str,
        key_epoch: int,
        now: int,
        include_inbox: bool,
        include_remote_accepted: bool,
    ) -> tuple[int, int]:
        failed_outbox = 0
        outbox_states = ("staged", "remote_accepted") if include_remote_accepted else ("staged",)
        placeholders = ",".join("?" for _ in outbox_states)
        outbox = connection.execute(
            f"""SELECT packet_id,packet_json FROM server_agent_relay_outbox
                  WHERE target_domain_id=? AND state IN ({placeholders})""",
            (peer_domain_id, *outbox_states),
        ).fetchall()
        for row in outbox:
            packet = RelayPacket.model_validate_json(row["packet_json"])
            if packet.peer_key_id != key_id or packet.peer_key_epoch != key_epoch:
                continue
            updated = connection.execute(
                f"""UPDATE server_agent_relay_outbox SET state='failed',updated_at=?
                      WHERE packet_id=? AND state IN ({placeholders})""",
                (now, row["packet_id"], *outbox_states),
            )
            if updated.rowcount != 1:
                continue
            failed_outbox += 1
            if self.admission is not None and not self.admission._terminalize_work_in_transaction(
                connection,
                work_kind="relay_outbound",
                source_id=row["packet_id"],
                now=now,
            ):
                raise ConflictError("revoked relay outbox work reservation was not pending")
        failed_inbox = 0
        if include_inbox:
            inbox = connection.execute(
                """SELECT packet_id,packet_json FROM server_agent_relay_inbox
                     WHERE source_domain_id=? AND state='authorized_pending'""",
                (peer_domain_id,),
            ).fetchall()
            for row in inbox:
                packet = RelayPacket.model_validate_json(row["packet_json"])
                if packet.peer_key_id != key_id or packet.peer_key_epoch != key_epoch:
                    continue
                updated = connection.execute(
                    """UPDATE server_agent_relay_inbox SET state='failed',updated_at=?
                         WHERE packet_id=? AND state='authorized_pending'""",
                    (now, row["packet_id"]),
                )
                if updated.rowcount != 1:
                    continue
                failed_inbox += 1
                connection.execute(
                    """UPDATE task_custody_proposals
                          SET state='invalidated',state_reason='relay_key_compromised',
                              revision=revision+1,updated_at=?,decided_at=?
                        WHERE idempotency_key=? AND state='pending'""",
                    (now, now, f"server-relay:{row['packet_id']}"),
                )
                if self.admission is not None and not self.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="relay_inbound",
                    source_id=row["packet_id"],
                    now=now,
                ):
                    raise ConflictError("revoked relay inbox work reservation was not pending")
        return failed_outbox, failed_inbox

    @staticmethod
    def key_revocation_binding(
        *,
        peer_domain_id: str,
        revocation: RelayPeerKeyRevocation,
    ) -> tuple[str, dict[str, Any]]:
        return f"relay-peer:{peer_domain_id}", {
            "schema": "agentnet.server-relay.key-revocation-authority.v1",
            "mutation_id": revocation.mutation_id,
            "manifest_digest": revocation.digest,
            "key_id": revocation.key_id,
            "key_epoch": revocation.key_epoch,
            "reason": revocation.reason,
        }

    def revoke_peer_key(
        self,
        *,
        peer_domain_id: str,
        revocation: RelayPeerKeyRevocation,
        local_signature: str,
        policy_decision_id: str,
    ) -> dict[str, Any]:
        """Immediately revoke one compromised key and quarantine its queues."""

        peer = self._peer(peer_domain_id)
        now = self.clock()
        if (
            revocation.local_domain_id != self.local_actor.domain_id
            or revocation.peer_domain_id != peer_domain_id
            or revocation.local_relay_harness_id != self.local_actor.harness_id
            or revocation.peer_relay_harness_id != peer.relay_harness_id
        ):
            raise AuthenticationError("relay key revocation endpoint binding failed")
        if not revocation.issued_at <= now < revocation.expires_at:
            raise AuthenticationError("relay key revocation is stale or outside its validity interval")
        verify_signature(
            self.local_signer.public_pem,
            RELAY_KEY_REVOCATION_PURPOSE,
            revocation.signed_fields(),
            local_signature,
        )
        peer.key(revocation.key_id, revocation.key_epoch)
        with self.store.transaction() as connection:
            self._require_local_actor(ServerAgentCapability.RELAY, connection=connection)
            resource, request = self.key_revocation_binding(
                peer_domain_id=peer_domain_id,
                revocation=revocation,
            )
            require_current_authority_decision(
                connection,
                authority=IssuanceAuthority(
                    actor=self.local_actor,
                    policy_decision_id=policy_decision_id,
                ),
                expected_action="server_agent.relay.key.revoke",
                expected_resource=resource,
                expected_request=request,
                when=datetime.fromtimestamp(now, UTC),
            )
            duplicate = connection.execute(
                """SELECT * FROM relay_peer_key_mutations
                     WHERE mutation_id=? OR manifest_digest=?""",
                (revocation.mutation_id, revocation.digest),
            ).fetchone()
            if duplicate is not None:
                if (
                    duplicate["mutation_id"] == revocation.mutation_id
                    and duplicate["manifest_digest"] == revocation.digest
                    and duplicate["mutation_type"] == "compromise_revoke"
                ):
                    return {
                        "peer_domain_id": peer_domain_id,
                        "key_id": revocation.key_id,
                        "key_epoch": revocation.key_epoch,
                        "status": "revoked",
                        "duplicate": True,
                    }
                raise ConflictError("relay key revocation identifier or digest was already consumed")
            row = connection.execute(
                """SELECT * FROM relay_peer_keys
                     WHERE local_domain_id=? AND peer_domain_id=? AND key_id=? AND key_epoch=?""",
                (
                    self.local_actor.domain_id,
                    peer_domain_id,
                    revocation.key_id,
                    revocation.key_epoch,
                ),
            ).fetchone()
            if row is None or row["state"] in {"retired", "revoked"}:
                raise ConflictError("relay key revocation is stale or conflicts with lifecycle state")
            updated = connection.execute(
                """UPDATE relay_peer_keys
                      SET state='revoked',revoked_at=?,updated_at=?
                    WHERE local_domain_id=? AND peer_domain_id=? AND key_id=? AND key_epoch=?
                      AND state IN ('pending','active','overlap') AND revoked_at IS NULL""",
                (
                    now,
                    now,
                    self.local_actor.domain_id,
                    peer_domain_id,
                    revocation.key_id,
                    revocation.key_epoch,
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("relay key revocation raced with another lifecycle mutation")
            failed_outbox, failed_inbox = self._fail_queued_packets_for_key_in_transaction(
                connection,
                peer_domain_id=peer_domain_id,
                key_id=revocation.key_id,
                key_epoch=revocation.key_epoch,
                now=now,
                include_inbox=True,
                include_remote_accepted=True,
            )
            connection.execute(
                """INSERT INTO relay_peer_key_mutations(
                    mutation_id,local_domain_id,peer_domain_id,mutation_type,from_key_id,to_key_id,
                    expected_from_epoch,resulting_epoch,manifest_digest,actor_json,
                    policy_decision_id,state,created_at
                ) VALUES(?,?,?,'compromise_revoke',?,NULL,?,?,?,?,?,'completed',?)""",
                (
                    revocation.mutation_id,
                    self.local_actor.domain_id,
                    peer_domain_id,
                    revocation.key_id,
                    revocation.key_epoch,
                    revocation.key_epoch,
                    revocation.digest,
                    canonical_json(self.local_actor.audit_view()).decode("utf-8"),
                    policy_decision_id,
                    now,
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "server_agent.relay_peer_key_compromise_revoked",
                    "actor": self.local_actor.audit_view(),
                    "failed_inbox": failed_inbox,
                    "failed_outbox": failed_outbox,
                    "key_epoch": revocation.key_epoch,
                    "key_id": revocation.key_id,
                    "manifest_digest": revocation.digest,
                    "mutation_id": revocation.mutation_id,
                    "peer_domain_id": peer_domain_id,
                    "policy_decision_id": policy_decision_id,
                    "reason": revocation.reason,
                },
            )
        return {
            "peer_domain_id": peer_domain_id,
            "key_id": revocation.key_id,
            "key_epoch": revocation.key_epoch,
            "status": "revoked",
            "failed_outbox": failed_outbox,
            "failed_inbox": failed_inbox,
            "duplicate": False,
            "audit_hash": audit_hash,
        }

    def expire_peer_key_overlaps(self, *, authoritative_now: int | None = None) -> int:
        """Retire expired overlap keys and fail unsent old-key packets."""

        now = self.clock() if authoritative_now is None else authoritative_now
        retired = 0
        with self.store.transaction() as connection:
            rows = connection.execute(
                """SELECT * FROM relay_peer_keys
                     WHERE local_domain_id=? AND state='overlap' AND overlap_until<=?""",
                (self.local_actor.domain_id, now),
            ).fetchall()
            for row in rows:
                updated = connection.execute(
                    """UPDATE relay_peer_keys SET state='retired',updated_at=?
                         WHERE local_domain_id=? AND peer_domain_id=? AND key_id=?
                           AND key_epoch=? AND state='overlap' AND overlap_until<=?""",
                    (
                        now,
                        self.local_actor.domain_id,
                        row["peer_domain_id"],
                        row["key_id"],
                        row["key_epoch"],
                        now,
                    ),
                )
                if updated.rowcount != 1:
                    continue
                self._fail_queued_packets_for_key_in_transaction(
                    connection,
                    peer_domain_id=str(row["peer_domain_id"]),
                    key_id=str(row["key_id"]),
                    key_epoch=int(row["key_epoch"]),
                    now=now,
                    include_inbox=False,
                    include_remote_accepted=False,
                )
                retired += 1
                self.store.append_audit(
                    connection,
                    {
                        "action": "server_agent.relay_peer_key_overlap_retired",
                        "key_epoch": int(row["key_epoch"]),
                        "key_id": str(row["key_id"]),
                        "peer_domain_id": str(row["peer_domain_id"]),
                    },
                )
        return retired

    @staticmethod
    def new_packet_id() -> str:
        return str(uuid4())

    @staticmethod
    def stage_binding(
        *,
        packet_id: str,
        event_id: str,
        target_domain_id: str,
        target_recipient_id: str,
        guest_pairwise_subject: str,
        target_grant_id: str,
        target_collaboration_scope_id: str,
    ) -> tuple[str, dict[str, str]]:
        request = {
            "event_id": event_id,
            "guest_pairwise_subject": guest_pairwise_subject,
            "packet_id": packet_id,
            "target_grant_id": target_grant_id,
            "target_recipient_id": target_recipient_id,
            "target_collaboration_scope_id": target_collaboration_scope_id,
        }
        return f"server-agent-domain:{target_domain_id}", {"request_digest": canonical_digest(request)}

    def _peer(self, domain_id: str) -> ServerAgentPeer:
        peer = self.peers.get(domain_id)
        if peer is None:
            raise AuthorizationError("server-agent peer is not explicitly configured")
        return peer

    def stage(
        self,
        *,
        packet_id: str,
        event_id: str,
        target_domain_id: str,
        target_recipient_id: str,
        guest_pairwise_subject: str,
        target_grant_id: str,
        target_collaboration_scope_id: str,
        authority: IssuanceAuthority,
        ttl_seconds: int = 300,
        phase_hook: Callable[[str], None] | None = None,
    ) -> RelayPacket:
        if not 30 <= ttl_seconds <= 3_600:
            raise ValueError("relay packet TTL must be between 30 and 3600 seconds")
        if authority.actor.audit_view() != self.local_actor.audit_view():
            raise AuthorizationError("relay authority must be the exact enrolled server agent")
        peer = self._peer(target_domain_id)
        now = self.clock()
        with self.store.transaction() as connection:
            self._require_local_actor(
                ServerAgentCapability.RELAY,
                ServerAgentCapability.STORE_AND_FORWARD,
                connection=connection,
            )
            event = connection.execute("SELECT * FROM events WHERE event_id=?", (event_id,)).fetchone()
            if (
                event is None
                or event["domain_id"] != self.local_actor.domain_id
                or event["acceptance_fact"] not in {
                    DeliveryFact.ACCEPTED_LOCAL.value,
                    DeliveryFact.ACCEPTED_DURABLE.value,
                }
            ):
                raise AuthorizationError("relay source event is unavailable in the local agent domain")
            resource, request = self.stage_binding(
                packet_id=packet_id,
                event_id=event_id,
                target_domain_id=target_domain_id,
                target_recipient_id=target_recipient_id,
                guest_pairwise_subject=guest_pairwise_subject,
                target_grant_id=target_grant_id,
                target_collaboration_scope_id=target_collaboration_scope_id,
            )
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="server_agent.relay.send",
                expected_resource=resource,
                expected_request=request,
                when=datetime.fromtimestamp(now, UTC),
            )
            source, _payload = self.mailbox._validated_event_and_payload(event)
            # This is a separately signed, exact ``server_agent.relay.send``
            # transport operation, not a generic mailbox/conversation read or
            # semantic worker disclosure.  The target still receives task
            # bytes only through AssignmentService, which stamps permanent
            # task-grant-required visibility before mailbox custody.
            if source.actor.kind is ActorKind.HOST_GUEST_HARNESS:
                raise AuthorizationError(
                    "non-transitive federation forbids onward relay of a host-local guest event"
                )
            existing = connection.execute(
                "SELECT * FROM server_agent_relay_outbox WHERE packet_id=?",
                (packet_id,),
            ).fetchone()
            if existing is not None:
                packet = RelayPacket.model_validate_json(existing["packet_json"])
                if (
                    existing["state"] not in {"staged", "remote_accepted", "recipient_committed"}
                    or existing["event_id"] != event_id
                    or packet.target_domain_id != target_domain_id
                    or packet.target_recipient_id != target_recipient_id
                    or packet.guest_pairwise_subject != guest_pairwise_subject
                    or packet.target_grant_id != target_grant_id
                    or packet.target_collaboration_scope_id != target_collaboration_scope_id
                    or packet.source_event_digest != event["envelope_digest"]
                    or existing["packet_digest"] != packet.digest
                ):
                    raise ConflictError("relay packet identifier names different canonical intent")
                if existing["state"] in {"staged", "remote_accepted"}:
                    self._packet_peer_key(connection, peer, packet, now=now)
                return packet
            peer_key = self._active_peer_key(connection, peer, now=now)
            purpose = f"server-relay:{packet_id}:{self.local_actor.domain_id}:{target_domain_id}"
            ciphertext = LocalEnvelopeCipher(peer_key.key).encrypt_json(
                {"event": source.model_dump(mode="json")},
                purpose=purpose,
            )
            fields = {
                "profile": "agentnet.server-relay.packet.v2",
                "hop_count": 1,
                "max_hops": 1,
                "packet_id": packet_id,
                "source_domain_id": self.local_actor.domain_id,
                "source_relay_harness_id": self.local_actor.harness_id,
                "source_key_id": self.local_signer.thumbprint,
                "peer_key_id": peer_key.key_id,
                "peer_key_epoch": peer_key.key_epoch,
                "target_domain_id": target_domain_id,
                "target_relay_harness_id": peer.relay_harness_id,
                "target_recipient_id": target_recipient_id,
                "target_grant_id": target_grant_id,
                "target_collaboration_scope_id": target_collaboration_scope_id,
                "guest_pairwise_subject": guest_pairwise_subject,
                "source_event_id": event_id,
                "source_event_digest": event["envelope_digest"],
                "ciphertext": ciphertext,
                "created_at": now,
                "expires_at": now + ttl_seconds,
            }
            packet = RelayPacket(
                **fields,
                signature=self.local_signer.sign("agentnet.server-relay.packet.v2", fields),
            )
            serialized = canonical_json(packet.model_dump(mode="json")).decode("utf-8")
            if self.admission is not None:
                self.admission._admit_operation_in_transaction(
                    connection,
                    actor_scope=self.local_actor.harness_id or "unattributed",
                    domain_scope=self.local_actor.domain_id,
                    operation="relay_stage",
                    operation_id=packet_id,
                    cost=1,
                    hop_count=packet.hop_count,
                )
                self.admission._reserve_work_in_transaction(
                    connection,
                    work_kind="relay_outbound",
                    source_id=packet_id,
                    domain_id=self.local_actor.domain_id,
                    now=now,
                )
            connection.execute(
                """INSERT INTO server_agent_relay_outbox(
                    packet_id,event_id,target_domain_id,target_recipient_id,packet_json,packet_digest,
                    state,created_at,updated_at
                ) VALUES(?,?,?,?,?,?,'staged',?,?)""",
                (
                    packet_id,
                    event_id,
                    target_domain_id,
                    target_recipient_id,
                    serialized,
                    packet.digest,
                    now,
                    now,
                ),
            )
            if self.admission is not None:
                self.admission._record_success_in_transaction(
                    connection,
                    breaker_key=self.admission._operation_key(
                        "relay_stage", self.local_actor.domain_id
                    ),
                    now=now,
                )
            if phase_hook is not None:
                phase_hook("after_outbox_insert")
            self.store.append_audit(
                connection,
                {
                    "action": "server_agent.relay_staged",
                    "actor": self.local_actor.audit_view(),
                    "packet_digest": packet.digest,
                    "packet_id": packet_id,
                    "source_event_digest": packet.source_event_digest,
                    "source_event_id": packet.source_event_id,
                    "target_collaboration_scope_id": packet.target_collaboration_scope_id,
                    "target_domain_id": target_domain_id,
                },
            )
            if phase_hook is not None:
                phase_hook("before_outbox_commit")
        if phase_hook is not None:
            phase_hook("after_outbox_commit")
        return packet

    def _resolve_guest(self, connection, packet: RelayPacket, *, now: int) -> VerifiedActor:
        guest = connection.execute(
            """SELECT * FROM guests
                 WHERE host_domain_id=? AND home_domain_id=? AND pairwise_subject=? AND status='active'""",
            (packet.target_domain_id, packet.source_domain_id, packet.guest_pairwise_subject),
        ).fetchone()
        if guest is None or int(guest["expires_at"]) <= now:
            raise AuthorizationError("relay packet has no current host-local guest mapping")
        harness = connection.execute(
            "SELECT * FROM harnesses WHERE guest_id=? AND status='active'",
            (guest["guest_id"],),
        ).fetchone()
        if harness is None:
            raise AuthorizationError("relay guest harness is unavailable")
        credential = connection.execute(
            """SELECT * FROM credentials
                 WHERE harness_id=? AND status='active' AND not_before<=? AND expires_at>?
                 ORDER BY epoch DESC LIMIT 1""",
            (harness["harness_id"], now, now),
        ).fetchone()
        if credential is None or int(credential["epoch"]) != int(harness["credential_epoch"]):
            raise AuthorizationError("relay guest credential is unavailable")
        return VerifiedActor(
            kind=ActorKind.HOST_GUEST_HARNESS,
            domain_id=packet.target_domain_id,
            guest_id=guest["guest_id"],
            harness_id=harness["harness_id"],
            credential_id=credential["credential_id"],
            credential_epoch=int(credential["epoch"]),
            binding_assurance=harness["binding_assurance"],
        )

    def _receipt(
        self,
        packet: RelayPacket,
        *,
        fact: Literal["accepted_local", "accepted_durable", "recipient_committed"],
        local_event_id: str | None = None,
        connection: Any | None = None,
    ) -> ServerRelayReceipt:
        self._require_local_actor(
            ServerAgentCapability.RELAY,
            ServerAgentCapability.OFFLINE_CUSTODY,
            connection=connection,
        )
        if fact == DeliveryFact.ACCEPTED_DURABLE.value and self._custody_fact is not DeliveryFact.ACCEPTED_DURABLE:
            raise GateBlocked("relay_durability", "this backend cannot sign accepted_durable custody")
        fields = {
            "profile": "agentnet.server-relay.receipt.v1",
            "receipt_id": str(uuid4()),
            "packet_id": packet.packet_id,
            "packet_digest": packet.digest,
            "source_domain_id": packet.source_domain_id,
            "target_domain_id": packet.target_domain_id,
            "target_relay_harness_id": self.local_actor.harness_id,
            "target_key_id": self.local_signer.thumbprint,
            "fact": fact,
            "local_event_id": local_event_id,
            "created_at": self.clock(),
        }
        signed = {key: value for key, value in fields.items() if value is not None}
        return ServerRelayReceipt(
            **fields,
            signature=self.local_signer.sign("agentnet.server-relay.receipt.v1", signed),
        )

    def accept(
        self,
        packet: RelayPacket,
        *,
        phase_hook: Callable[[str], None] | None = None,
    ) -> ServerRelayReceipt:
        if self.policy is None or self.mailbox is None:
            raise AuthorizationError("relay receive requires local policy and mailbox services")
        peer = self._peer(packet.source_domain_id)
        now = self.clock()
        if (
            packet.target_domain_id != self.local_actor.domain_id
            or packet.target_relay_harness_id != self.local_actor.harness_id
            or packet.source_relay_harness_id != peer.relay_harness_id
            or packet.source_key_id != peer.signing_key_id
            or packet.created_at > now + 60
            or packet.expires_at <= now
            or packet.expires_at - packet.created_at > 3_600
        ):
            raise AuthenticationError("relay packet endpoint or freshness binding failed")
        verify_signature(peer.public_key_pem, "agentnet.server-relay.packet.v2", packet.signed_fields(), packet.signature)
        with self.store.transaction(immediate=False) as replay_connection:
            prior = replay_connection.execute(
                "SELECT packet_digest,state,local_event_id FROM server_agent_relay_inbox WHERE packet_id=?",
                (packet.packet_id,),
            ).fetchone()
            if prior is not None:
                if prior["packet_digest"] != packet.digest:
                    raise ConflictError("relay packet replay contains different exact bytes")
                if prior["state"] == "failed":
                    raise AuthenticationError("relay packet key or custody was revoked after acceptance")
                if prior["state"] in {"authorized_pending", "recipient_committed"}:
                    fact: Literal["accepted_local", "accepted_durable", "recipient_committed"] = (
                        "recipient_committed"
                        if prior["state"] == "recipient_committed"
                        else self._custody_fact.value
                    )
                    return self._receipt(
                        packet,
                        fact=fact,
                        local_event_id=prior["local_event_id"],
                        connection=replay_connection,
                    )
        purpose = f"server-relay:{packet.packet_id}:{packet.source_domain_id}:{packet.target_domain_id}"
        with self.store.transaction(immediate=False) as read_connection:
            peer_key = self._packet_peer_key(
                read_connection,
                peer,
                packet,
                now=now,
            )
        bundle = LocalEnvelopeCipher(peer_key.key).decrypt_json(packet.ciphertext, purpose=purpose)
        source = EventEnvelope.model_validate(bundle["event"])
        validate_event_digest(source)
        if (
            source.event_id != packet.source_event_id
            or source.domain_id != packet.source_domain_id
            or envelope_digest(source) != packet.source_event_digest
        ):
            raise AuthenticationError("relay ciphertext does not bind the signed source event")
        if source.actor.kind is ActorKind.HOST_GUEST_HARNESS:
            raise AuthorizationError(
                "non-transitive federation forbids onward relay of a host-local guest event"
            )
        if source.released_artifacts:
            raise GateBlocked(
                "relay_artifact_import",
                "cross-domain attachments require a target-domain quarantine, scan, and release binding",
            )
        if source.room_id is not None:
            raise GateBlocked(
                "relay_room_import",
                "cross-domain room traffic requires an explicit room-transfer epoch reconciliation",
            )
        local_event_id = str(
            uuid5(NAMESPACE_URL, f"agentnet:server-relay:{packet.packet_id}")
        )
        if source.event_type is EventType.TASK_ASSIGNMENT:
            scope_action = "task.propose"
            scope_resource = f"task:{local_event_id}"
        elif source.event_type is EventType.MESSAGE:
            scope_action = "message.send"
            scope_resource = f"conversation:{source.conversation_id or 'direct'}"
        else:
            raise AuthorizationError("relay source event type is unsupported")

        denied_reason: str | None = None
        local_event: EventEnvelope | None = None
        task_custody: dict[str, Any] | None = None
        duplicate_fact: Literal["accepted_local", "accepted_durable", "recipient_committed"] | None = None
        duplicate_local_event_id: str | None = None
        with self.store.transaction() as connection:
            self._require_local_actor(
                ServerAgentCapability.RELAY,
                ServerAgentCapability.OFFLINE_CUSTODY,
                connection=connection,
            )
            # Close the read/decrypt-to-commit race: compromise revocation or
            # overlap expiry between the initial check and this transaction
            # denies the packet before any grant/custody state is written.
            self._packet_peer_key(connection, peer, packet, now=now)
            duplicate = connection.execute(
                "SELECT * FROM server_agent_relay_inbox WHERE packet_id=?",
                (packet.packet_id,),
            ).fetchone()
            if duplicate is not None:
                if duplicate["packet_digest"] != packet.digest:
                    raise ConflictError("relay packet replay contains different exact bytes")
                duplicate_fact = (
                    "recipient_committed"
                    if duplicate["state"] == "recipient_committed"
                    else self._custody_fact.value
                )
                duplicate_local_event_id = duplicate["local_event_id"]
            if duplicate_fact is not None:
                # The prior acceptance already committed.  Do not consume the
                # target grant a second time.
                pass
            else:
                recipient = connection.execute(
                    "SELECT status,domain_id FROM harnesses WHERE harness_id=?",
                    (packet.target_recipient_id,),
                ).fetchone()
                if recipient is None or recipient["status"] != "active" or recipient["domain_id"] != packet.target_domain_id:
                    raise AuthorizationError("relay target recipient is not a current local harness")
                guest_actor = self._resolve_guest(connection, packet, now=now)
                collaboration_scope = (
                    self.mailbox.collaboration_scopes.require_in_transaction(
                        connection,
                        actor=guest_actor,
                        scope_id=packet.target_collaboration_scope_id,
                        action=scope_action,
                        resource=scope_resource,
                        target_harness_ids=(packet.target_recipient_id,),
                        classification=source.classification,
                        when=datetime.fromtimestamp(now, UTC),
                    )
                )
                target_payload = {
                    key: value
                    for key, value in source.payload.items()
                    if key != "authorization_context"
                }
                target_payload["authorization_context"] = (
                    collaboration_scope.authorization_context()
                )
                grant_use = GrantUse(
                    grant_id=packet.target_grant_id,
                    action="message.send",
                    resource=f"recipient:{packet.target_recipient_id}",
                    input_source="server_agent_relay",
                    output_sink=f"mailbox:{packet.target_recipient_id}",
                    data_class=source.classification,
                )
                domain = connection.execute(
                    "SELECT policy_revision FROM domains WHERE domain_id=?",
                    (packet.target_domain_id,),
                ).fetchone()
                decision = self.policy._decide_in_transaction(
                    connection,
                    AuthorizationRequest(
                        actor=guest_actor,
                        action=grant_use.action,
                        resource=grant_use.resource,
                        operation_class=OperationClass.BUSINESS,
                        policy_revision=int(domain["policy_revision"]),
                        context={"packet_digest": packet.digest, "source_event_digest": packet.source_event_digest},
                        grant_use=grant_use,
                        classification=source.classification,
                    ),
                    when=datetime.fromtimestamp(now, UTC),
                )
                if not decision.allowed:
                    denied_reason = decision.reason
                else:
                    local_event = EventEnvelope(
                        event_id=local_event_id,
                        domain_id=packet.target_domain_id,
                        actor=guest_actor,
                        event_type=source.event_type,
                        classification=source.classification,
                        payload=target_payload,
                        payload_digest=canonical_digest(target_payload),
                        idempotency_key=f"server-relay:{packet.packet_id}",
                        recipients=(packet.target_recipient_id,),
                        conversation_id=source.conversation_id,
                        room_id=source.room_id,
                        thread_id=source.thread_id,
                        task_id=source.task_id,
                        # The source event lives in another trust-domain ledger.
                        # RelayPacket signatures/audit bind it; a local causal
                        # parent requires an explicit signed provenance bridge.
                        causal_parent_ids=(),
                        created_at=datetime.fromtimestamp(packet.created_at, UTC),
                        delivery_expires_at=datetime.fromtimestamp(packet.expires_at, UTC),
                        policy_revision=int(domain["policy_revision"]),
                        credential_epoch=guest_actor.credential_epoch,
                    )
                    encrypted_event = self.store.cipher.encrypt_json(
                        local_event.model_dump(mode="json"),
                        purpose=f"server-relay-local-event:{packet.packet_id}",
                    )
                    if self.admission is not None:
                        self.admission._admit_operation_in_transaction(
                            connection,
                            actor_scope=self.local_actor.harness_id or "unattributed",
                            domain_scope=self.local_actor.domain_id,
                            operation="relay_inbound",
                            operation_id=packet.packet_id,
                            cost=1,
                            hop_count=packet.hop_count,
                        )
                        self.admission._reserve_work_in_transaction(
                            connection,
                            work_kind="relay_inbound",
                            source_id=packet.packet_id,
                            domain_id=self.local_actor.domain_id,
                            now=now,
                        )
                    connection.execute(
                        """INSERT INTO server_agent_relay_inbox(
                            packet_id,source_domain_id,source_event_id,target_recipient_id,guest_actor_json,
                            packet_json,packet_digest,local_event_encrypted,target_grant_id,policy_decision_id,
                            state,created_at,updated_at
                        ) VALUES(?,?,?,?,?,?,?,?,?,?,'authorized_pending',?,?)""",
                        (
                            packet.packet_id,
                            packet.source_domain_id,
                            packet.source_event_id,
                            packet.target_recipient_id,
                            canonical_json(guest_actor.audit_view()).decode("utf-8"),
                            canonical_json(packet.model_dump(mode="json")).decode("utf-8"),
                            packet.digest,
                            encrypted_event,
                            packet.target_grant_id,
                            decision.decision_id,
                            now,
                            now,
                        ),
                    )
                    if local_event.event_type is EventType.TASK_ASSIGNMENT:
                        if self.assignments is None:
                            raise AuthorizationError("relay task receive requires directional task custody")
                        task_custody = self.assignments.submit_event(
                            AssignmentRequest(
                                actor=guest_actor,
                                collaboration_scope_id=packet.target_collaboration_scope_id,
                                recipient_harness_id=packet.target_recipient_id,
                                task_type="relay.task",
                                resources=frozenset({f"recipient:{packet.target_recipient_id}"}),
                                data_classes=frozenset({source.classification}),
                                policy_revision=int(domain["policy_revision"]),
                                context={
                                    "packet_digest": packet.digest,
                                    "source_event_digest": packet.source_event_digest,
                                },
                            ),
                            local_event,
                            ingress=TaskIngressKind.RELAY_TASK,
                            continuation={
                                "kind": "relay_task",
                                "apply_on_initial": True,
                                "packet_id": packet.packet_id,
                                "authorization": {
                                    "action": grant_use.action,
                                    "resource": grant_use.resource,
                                    "grant_id": grant_use.grant_id,
                                    "input_source": grant_use.input_source,
                                    "output_sink": grant_use.output_sink,
                                    "data_class": grant_use.data_class.value,
                                },
                            },
                            proposal_expires_at=datetime.fromtimestamp(packet.expires_at, UTC),
                            when=datetime.fromtimestamp(now, UTC),
                            connection=connection,
                        )
                    if phase_hook is not None:
                        phase_hook("after_inbox_insert")
                    self.store.append_audit(
                        connection,
                        {
                            "action": "server_agent.relay_accepted",
                            "guest_actor": guest_actor.audit_view(),
                            "local_event_digest": envelope_digest(local_event),
                            "local_payload_digest": local_event.payload_digest,
                            "packet_digest": packet.digest,
                            "packet_id": packet.packet_id,
                            "source_event_digest": packet.source_event_digest,
                            "source_event_id": packet.source_event_id,
                            "target_collaboration_scope_id": packet.target_collaboration_scope_id,
                            "policy_decision_id": decision.decision_id,
                            "task_custody_fact": task_custody["fact"] if task_custody else None,
                            "task_proposal_id": task_custody.get("proposal_id") if task_custody else None,
                        },
                    )
                    if self.admission is not None:
                        self.admission._record_success_in_transaction(
                            connection,
                            breaker_key=self.admission._operation_key(
                                "relay_inbound", self.local_actor.domain_id
                            ),
                            now=now,
                        )
                    if phase_hook is not None:
                        phase_hook("before_inbox_commit")
        if phase_hook is not None and duplicate_fact is None and denied_reason is None:
            phase_hook("after_inbox_commit")
        if duplicate_fact is not None:
            return self._receipt(packet, fact=duplicate_fact, local_event_id=duplicate_local_event_id)
        if denied_reason is not None:
            raise AuthorizationError(denied_reason)
        if local_event is None:  # pragma: no cover - defensive invariant
            raise ConflictError("relay acceptance produced no local event")
        return self._receipt(packet, fact=self._custody_fact.value, local_event_id=local_event.event_id)

    def deliver(self, packet_id: str, *, phase_hook: Callable[[str], None] | None = None) -> ServerRelayReceipt:
        if self.mailbox is None:
            raise AuthorizationError("relay delivery requires a local mailbox service")
        self._require_local_actor(
            ServerAgentCapability.RELAY,
            ServerAgentCapability.OFFLINE_CUSTODY,
            ServerAgentCapability.STORE_AND_FORWARD,
        )
        row = self.store.fetch_one("SELECT * FROM server_agent_relay_inbox WHERE packet_id=?", (packet_id,))
        if row is None:
            raise AuthorizationError("relay inbox item is unavailable")
        packet = RelayPacket.model_validate_json(row["packet_json"])
        if row["state"] == "recipient_committed":
            return self._receipt(packet, fact="recipient_committed", local_event_id=row["local_event_id"])
        if row["state"] != "authorized_pending":
            raise ConflictError("relay inbox state is not recoverable for delivery")
        event_value = self.store.cipher.decrypt_json(
            row["local_event_encrypted"],
            purpose=f"server-relay-local-event:{packet_id}",
        )
        event = EventEnvelope.model_validate(event_value)
        guest_actor = VerifiedActor.model_validate(json.loads(row["guest_actor_json"]))
        if packet.expires_at <= self.clock():
            self.expire_inbox()
            raise GateBlocked("relay_expired", "relay inbox work expired before recipient commit")
        if event.event_type is EventType.TASK_ASSIGNMENT:
            if self.assignments is None:
                raise AuthorizationError("relay task delivery requires directional task custody")
            self.assignments.expire_due(
                authoritative_now=datetime.fromtimestamp(self.clock(), UTC)
            )
            proposal = self.store.fetch_one(
                """SELECT state,resumed_event_id FROM task_custody_proposals
                     WHERE domain_id=? AND sender_harness_id=? AND idempotency_key=?""",
                (event.domain_id, guest_actor.harness_id, event.idempotency_key),
            )
            if proposal is None:
                raise ConflictError("relay task has no directional custody proposal")
            if proposal["state"] == "pending":
                return self._receipt(packet, fact=self._custody_fact.value)
            if proposal["state"] == "resumed":
                committed = self.store.fetch_one(
                    "SELECT state,local_event_id FROM server_agent_relay_inbox WHERE packet_id=?",
                    (packet_id,),
                )
                if committed is None or committed["state"] != "recipient_committed":
                    raise ConflictError("relay task approval did not atomically commit recipient custody")
                return self._receipt(
                    packet,
                    fact="recipient_committed",
                    local_event_id=committed["local_event_id"],
                )
            if packet.expires_at <= self.clock():
                self.expire_inbox()
                raise GateBlocked("relay_expired", "relay task expired before recipient commit")
            raise AuthorizationError("relay task proposal is no longer executable")
        expired = False
        accepted: dict[str, Any] | None = None
        with self.store.transaction() as connection:
            now = self.clock()
            if packet.expires_at <= now:
                updated = connection.execute(
                    """UPDATE server_agent_relay_inbox SET state='failed',updated_at=?
                         WHERE packet_id=? AND state='authorized_pending'""",
                    (now, packet_id),
                )
                if updated.rowcount == 1 and self.admission is not None:
                    if not self.admission._terminalize_work_in_transaction(
                        connection,
                        work_kind="relay_inbound",
                        source_id=packet_id,
                        now=now,
                    ):
                        raise ConflictError("expired relay inbox work reservation was not pending")
                expired = True
            else:
                denial, _revision = validate_actor_state(
                    connection,
                    actor=guest_actor,
                    expected_policy_revision=event.policy_revision,
                    when=datetime.fromtimestamp(now, UTC),
                )
                if denial is not None:
                    raise AuthorizationError(f"relay delivery actor is no longer current: {denial}")
                self._require_local_actor(
                    ServerAgentCapability.RELAY,
                    ServerAgentCapability.OFFLINE_CUSTODY,
                    ServerAgentCapability.STORE_AND_FORWARD,
                    connection=connection,
                )
                accepted = self.mailbox._accept_in_transaction(
                    connection,
                    event,
                    now=now,
                    pending_cost=0,
                )
                updated = connection.execute(
                    """UPDATE server_agent_relay_inbox
                          SET state='recipient_committed',local_event_id=?,updated_at=?
                        WHERE packet_id=? AND state='authorized_pending'""",
                    (accepted["event_id"], now, packet_id),
                )
                if updated.rowcount != 1:
                    raise ConflictError("relay inbox terminal transition raced")
                if self.admission is not None:
                    terminalized = self.admission._terminalize_work_in_transaction(
                        connection,
                        work_kind="relay_inbound",
                        source_id=packet_id,
                        now=now,
                    )
                    if not terminalized:
                        raise ConflictError("relay inbox work reservation was not pending")
                self.store.append_audit(
                    connection,
                    {
                        "action": "server_agent.relay_recipient_committed",
                        "local_event_id": accepted["event_id"],
                        "packet_id": packet_id,
                    },
                )
                if phase_hook is not None:
                    phase_hook("before_inbox_commit")
        if expired:
            raise GateBlocked("relay_expired", "relay inbox work expired before recipient commit")
        if accepted is None:
            raise ConflictError("relay inbox delivery produced no committed event")
        if phase_hook is not None:
            phase_hook("after_recipient_mailbox_commit")
            phase_hook("after_inbox_commit")
        return self._receipt(packet, fact="recipient_committed", local_event_id=accepted["event_id"])

    def record_receipt(
        self,
        receipt: ServerRelayReceipt,
        *,
        phase_hook: Callable[[str], None] | None = None,
    ) -> dict[str, Any]:
        peer = self._peer(receipt.target_domain_id)
        if (
            receipt.source_domain_id != self.local_actor.domain_id
            or receipt.target_relay_harness_id != peer.relay_harness_id
            or receipt.target_key_id != peer.signing_key_id
        ):
            raise AuthenticationError("relay receipt endpoint binding failed")
        verify_signature(peer.public_key_pem, "agentnet.server-relay.receipt.v1", receipt.signed_fields(), receipt.signature)
        result: dict[str, Any]
        with self.store.transaction() as connection:
            self._require_local_actor(
                ServerAgentCapability.RELAY,
                ServerAgentCapability.STORE_AND_FORWARD,
                connection=connection,
            )
            row = connection.execute(
                "SELECT * FROM server_agent_relay_outbox WHERE packet_id=?",
                (receipt.packet_id,),
            ).fetchone()
            if (
                row is None
                or row["packet_digest"] != receipt.packet_digest
                or row["target_domain_id"] != receipt.target_domain_id
            ):
                raise AuthenticationError("relay receipt does not bind a staged packet")
            packet = RelayPacket.model_validate_json(row["packet_json"])
            now = self.clock()
            if receipt.created_at < packet.created_at - 60 or receipt.created_at > now + 60:
                raise AuthenticationError("relay receipt freshness binding failed")
            target_state = "recipient_committed" if receipt.fact == "recipient_committed" else "remote_accepted"
            state_rank = {"staged": 0, "remote_accepted": 1, "recipient_committed": 2}
            current_state = row["state"]
            if current_state not in state_rank:
                raise ConflictError("relay outbox contains an unknown state")
            advances = state_rank[target_state] > state_rank[current_state]
            if advances:
                connection.execute(
                    "UPDATE server_agent_relay_outbox SET state=?,receipt_json=?,updated_at=? WHERE packet_id=?",
                    (
                        target_state,
                        canonical_json(receipt.model_dump(mode="json")).decode("utf-8"),
                        now,
                        receipt.packet_id,
                    ),
                )
                resulting_state = target_state
                if target_state == "recipient_committed" and self.admission is not None:
                    terminalized = self.admission._terminalize_work_in_transaction(
                        connection,
                        work_kind="relay_outbound",
                        source_id=receipt.packet_id,
                        now=now,
                    )
                    if not terminalized:
                        raise ConflictError("relay outbox work reservation was not pending")
            else:
                resulting_state = current_state
            self.store.append_audit(
                connection,
                {
                    "action": "server_agent.relay_receipt" if advances else "server_agent.relay_receipt_ignored",
                    "fact": receipt.fact,
                    "packet_id": receipt.packet_id,
                    "state": resulting_state,
                },
            )
            if phase_hook is not None:
                phase_hook("before_receipt_commit")
            result = {
                "packet_id": receipt.packet_id,
                "state": resulting_state,
                "advanced": advances,
            }
        if phase_hook is not None:
            phase_hook("after_receipt_commit")
        return result

    def pending_packets(self, *, limit: int = 100) -> list[RelayPacket]:
        return [item["packet"] for item in self.pending_outbox(limit=limit) if item["state"] == "staged"]

    def expire_outbox(self, *, authoritative_now: int | None = None) -> int:
        now = self.clock() if authoritative_now is None else authoritative_now
        expired = 0
        with self.store.transaction() as connection:
            rows = connection.execute(
                """SELECT packet_id,packet_json FROM server_agent_relay_outbox
                     WHERE state IN ('staged','remote_accepted')"""
            ).fetchall()
            for row in rows:
                packet = RelayPacket.model_validate_json(row["packet_json"])
                if packet.expires_at > now:
                    continue
                updated = connection.execute(
                    """UPDATE server_agent_relay_outbox SET state='failed',updated_at=?
                         WHERE packet_id=? AND state IN ('staged','remote_accepted')""",
                    (now, row["packet_id"]),
                )
                if updated.rowcount != 1:
                    continue
                if self.admission is not None and not self.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="relay_outbound",
                    source_id=row["packet_id"],
                    now=now,
                ):
                    raise ConflictError("expired relay outbox work reservation was not pending")
                expired += 1
        return expired

    def expire_inbox(self, *, authoritative_now: int | None = None) -> int:
        now = self.clock() if authoritative_now is None else authoritative_now
        expired = 0
        with self.store.transaction() as connection:
            rows = connection.execute(
                """SELECT packet_id,packet_json FROM server_agent_relay_inbox
                     WHERE state='authorized_pending'"""
            ).fetchall()
            for row in rows:
                packet = RelayPacket.model_validate_json(row["packet_json"])
                if packet.expires_at > now:
                    continue
                updated = connection.execute(
                    """UPDATE server_agent_relay_inbox SET state='failed',updated_at=?
                         WHERE packet_id=? AND state='authorized_pending'""",
                    (now, row["packet_id"]),
                )
                if updated.rowcount != 1:
                    continue
                if self.admission is not None and not self.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="relay_inbound",
                    source_id=row["packet_id"],
                    now=now,
                ):
                    raise ConflictError("expired relay inbox work reservation was not pending")
                expired += 1
        return expired

    def pending_outbox(self, *, limit: int = 100) -> list[dict[str, Any]]:
        if not 1 <= limit <= 1_000:
            raise ValueError("relay polling limit is invalid")
        self._require_local_actor(ServerAgentCapability.RELAY, ServerAgentCapability.STORE_AND_FORWARD)
        self.expire_peer_key_overlaps()
        self.expire_outbox()
        rows = self.store.fetch_all(
            """SELECT packet_json,state,updated_at FROM server_agent_relay_outbox
                 WHERE state IN ('staged','remote_accepted') ORDER BY created_at LIMIT ?""",
            (limit,),
        )
        return [
            {
                "packet": RelayPacket.model_validate_json(row["packet_json"]),
                "state": row["state"],
                "updated_at": int(row["updated_at"]),
            }
            for row in rows
        ]

    def recover_pending_outbox(self, *, limit: int = 100) -> list[dict[str, Any]]:
        """Re-open durable transport work after process restart.

        This method never transmits on its own.  A bounded transport worker
        receives exact signed packets and remains responsible for HTTPS policy,
        backoff, and recording the signed remote receipt.
        """

        return self.pending_outbox(limit=limit)

    def pending_inbox(self, *, limit: int = 100) -> list[dict[str, Any]]:
        if not 1 <= limit <= 1_000:
            raise ValueError("relay polling limit is invalid")
        self._require_local_actor(
            ServerAgentCapability.RELAY,
            ServerAgentCapability.OFFLINE_CUSTODY,
            ServerAgentCapability.STORE_AND_FORWARD,
        )
        self.expire_inbox()
        rows = self.store.fetch_all(
            """SELECT packet_id,packet_json,state,local_event_id,updated_at
                 FROM server_agent_relay_inbox WHERE state='authorized_pending'
                 ORDER BY created_at LIMIT ?""",
            (limit,),
        )
        return [
            {
                "packet_id": row["packet_id"],
                "packet": RelayPacket.model_validate_json(row["packet_json"]),
                "state": row["state"],
                "local_event_id": row["local_event_id"],
                "updated_at": int(row["updated_at"]),
            }
            for row in rows
        ]

    def recover_pending_inbox(
        self,
        *,
        limit: int = 100,
        phase_hook: Callable[[str], None] | None = None,
    ) -> list[ServerRelayReceipt]:
        return [
            self.deliver(item["packet_id"], phase_hook=phase_hook)
            for item in self.pending_inbox(limit=limit)
        ]

    def receipt_for(self, packet_id: str) -> ServerRelayReceipt:
        """Produce current signed custody evidence for a known inbound packet."""

        row = self.store.fetch_one(
            "SELECT packet_json,state,local_event_id FROM server_agent_relay_inbox WHERE packet_id=?",
            (packet_id,),
        )
        if row is None:
            raise AuthorizationError("relay inbox item is unavailable")
        packet = RelayPacket.model_validate_json(row["packet_json"])
        if row["state"] == "recipient_committed":
            fact: Literal["accepted_local", "accepted_durable", "recipient_committed"] = "recipient_committed"
        elif row["state"] == "authorized_pending":
            fact = self._custody_fact.value
        else:
            raise ConflictError("relay inbox state cannot produce a custody receipt")
        return self._receipt(packet, fact=fact, local_event_id=row["local_event_id"])
