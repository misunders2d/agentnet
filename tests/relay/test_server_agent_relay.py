from __future__ import annotations

import json
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from threading import Barrier
from uuid import uuid4

import pytest
import httpx
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.grants import TaskGrantService
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
    OperationClass,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.core.app import CommunicationCore
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    GateBlocked,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.http_api import create_app
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import envelope_digest, new_event
from agentnet.protocol.models import (
    Classification,
    DeliveryFact,
    EventEnvelope,
    EventType,
    ReleasedArtifactBinding,
    TaskGrant,
)
from agentnet.operations.config import (
    ExtensionConfig,
    FeatureFlags,
    RelayPeerKeyConfig,
    RelayPeerConfig,
    RelayServiceConfig,
    RelaySigningIdentityConfig,
)
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.operations.quotas import QuotaService
from agentnet.relay.service import (
    RELAY_KEY_REVOCATION_PURPOSE,
    RELAY_KEY_ROTATION_PURPOSE,
    RelayPacket,
    RelayPeerKey,
    RelayPeerKeyRevocation,
    RelayPeerKeyRotation,
    ServerAgentPeer,
    ServerAgentRelayService,
    relay_peer_key_id,
)
from agentnet.relay.http import ServerAgentRelayClient, create_relay_app
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.sqlite import SQLiteStore


class InjectedRelayCrash(RuntimeError):
    pass


def enrolled_identity(store: SQLiteStore, *, domain: str, label: str):
    now = int(time.time())
    key = P256KeyPair.generate()
    principal_id = f"principal-{label}"
    harness_id = f"harness-{label}"
    credential_id = f"credential-{label}"
    with store.transaction() as connection:
        connection.execute(
            "INSERT OR IGNORE INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
            (domain, now),
        )
        connection.execute(
            """INSERT INTO principals(
                principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
            ) VALUES(?,?,?,?,?,'active',?)""",
            (principal_id, domain, "https://idp.example", label, f"{label}@example.test", now),
        )
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,
                capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,'active','os_bound',?,1,?)""",
            (
                harness_id,
                domain,
                principal_id,
                "server-agent",
                label,
                canonical_json(
                    {
                        "server_agent_capabilities": [
                            ServerAgentCapability.OFFLINE_CUSTODY.value,
                            ServerAgentCapability.RELAY.value,
                            ServerAgentCapability.STORE_AND_FORWARD.value,
                        ]
                    }
                ).decode("utf-8"),
                now,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)""",
            (credential_id, harness_id, key.thumbprint, key.public_pem, now - 1, now + 3_600),
        )
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=domain,
        principal_id=principal_id,
        harness_id=harness_id,
        credential_id=credential_id,
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    return actor, key


def build_pair(
    tmp_path: Path,
    *,
    event_type: EventType = EventType.MESSAGE,
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = (),
    grant_max_uses: int = 1,
):
    source_store = SQLiteStore(tmp_path / "source.sqlite3", LocalEnvelopeCipher(b"s" * 32))
    target_store = SQLiteStore(tmp_path / "target.sqlite3", LocalEnvelopeCipher(b"t" * 32))
    source_actor, source_key = enrolled_identity(source_store, domain="alpha.example", label="alpha-relay")
    target_actor, target_key = enrolled_identity(target_store, domain="beta.example", label="beta-relay")
    recipient, _recipient_key = enrolled_identity(target_store, domain="beta.example", label="beta-recipient")
    guest_key = P256KeyPair.generate()
    now = int(time.time())
    guest_id = "guest-alpha-pairwise"
    guest_harness_id = "guest-alpha-harness"
    guest_credential_id = "guest-alpha-credential"
    pairwise_subject = "pairwise-alpha-subject-0001"
    with target_store.transaction() as connection:
        connection.execute(
            """INSERT INTO guests(
                guest_id,host_domain_id,home_domain_id,pairwise_subject,sponsor_principal_id,status,expires_at
            ) VALUES(?,?,?,?,?,'active',?)""",
            (guest_id, "beta.example", "alpha.example", pairwise_subject, target_actor.principal_id, now + 3_600),
        )
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,guest_id,kind,display_name,status,binding_assurance,
                capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,'federated_guest','alpha guest','active','os_bound',?,1,?)""",
            (guest_harness_id, "beta.example", guest_id, canonical_json({}).decode("utf-8"), now),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)""",
            (guest_credential_id, guest_harness_id, guest_key.thumbprint, guest_key.public_pem, now - 1, now + 3_600),
        )
    guest_actor = VerifiedActor(
        kind=ActorKind.HOST_GUEST_HARNESS,
        domain_id="beta.example",
        guest_id=guest_id,
        harness_id=guest_harness_id,
        credential_id=guest_credential_id,
        credential_epoch=1,
        binding_assurance="os_bound",
    )
    grant = TaskGrant(
        domain_id="beta.example",
        principal_id=guest_id,
        harness_id=guest_harness_id,
        actions=frozenset({"message.send"}),
        resources=frozenset({f"recipient:{recipient.harness_id}"}),
        input_sources=frozenset({"server_agent_relay"}),
        output_sinks=frozenset({f"mailbox:{recipient.harness_id}"}),
        data_classes=frozenset({Classification.C0_PUBLIC}),
        max_uses=grant_max_uses,
        expires_at=datetime.now(UTC) + timedelta(minutes=30),
    )
    with target_store.transaction() as connection:
        TaskGrantService(target_store)._insert_in_transaction(
            connection,
            grant=grant,
            when=datetime.now(UTC),
            issuance_evidence={"kind": "tested_bilateral_guest_admission"},
        )

    bilateral_key = b"r" * 32
    active_key = RelayPeerKey(
        key_id=relay_peer_key_id(bilateral_key),
        key_epoch=1,
        key=bilateral_key,
        provisioned_state="active",
    )
    source_mailbox = MailboxService(source_store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    target_mailbox = MailboxService(target_store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL)
    source_policy = LocalConformancePolicyEngine(source_store)
    target_policy = LocalConformancePolicyEngine(target_store)
    source = ServerAgentRelayService(
        source_store,
        local_actor=source_actor,
        local_signer=source_key,
        peers={
            "beta.example": ServerAgentPeer(
                domain_id="beta.example",
                relay_harness_id=target_actor.harness_id,
                signing_key_id=target_key.thumbprint,
                public_key_pem=target_key.public_pem,
                key_versions=(active_key,),
            )
        },
        runtime_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.RELAY,
                ServerAgentCapability.STORE_AND_FORWARD,
            }
        ),
        mailbox=source_mailbox,
        policy=source_policy,
    )
    target = ServerAgentRelayService(
        target_store,
        local_actor=target_actor,
        local_signer=target_key,
        peers={
            "alpha.example": ServerAgentPeer(
                domain_id="alpha.example",
                relay_harness_id=source_actor.harness_id,
                signing_key_id=source_key.thumbprint,
                public_key_pem=source_key.public_pem,
                key_versions=(active_key,),
            )
        },
        runtime_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.RELAY,
                ServerAgentCapability.STORE_AND_FORWARD,
            }
        ),
        mailbox=target_mailbox,
        policy=target_policy,
    )
    event = new_event(
        domain_id="alpha.example",
        actor=source_actor,
        event_type=event_type,
        classification=Classification.C0_PUBLIC,
        payload={"message": "offline relay synthetic"},
        idempotency_key=f"source-relay-{uuid4()}",
        recipients=(source_actor.harness_id,),
        released_artifacts=released_artifacts,
        task_id=f"relay-task-{uuid4()}" if event_type is EventType.TASK_ASSIGNMENT else None,
    )
    source_mailbox.accept(event)
    packet_id = source.new_packet_id()
    resource, context = source.stage_binding(
        packet_id=packet_id,
        event_id=event.event_id,
        target_domain_id="beta.example",
        target_recipient_id=recipient.harness_id,
        guest_pairwise_subject=pairwise_subject,
        target_grant_id=grant.grant_id,
    )
    source_policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id="alpha.example",
            principal_id=source_actor.principal_id,
            action="server_agent.relay.send",
            resource_pattern=resource,
            revision=1,
        )
    )
    decision = source_policy.require(
        AuthorizationRequest(
            actor=source_actor,
            action="server_agent.relay.send",
            resource=resource,
            policy_revision=1,
            context=context,
            classification=Classification.C0_PUBLIC,
        )
    )
    authority = IssuanceAuthority(actor=source_actor, policy_decision_id=decision.decision_id)
    return {
        "source_store": source_store,
        "target_store": target_store,
        "source": source,
        "target": target,
        "source_actor": source_actor,
        "source_key": source_key,
        "target_actor": target_actor,
        "target_key": target_key,
        "recipient": recipient,
        "guest_actor": guest_actor,
        "event": event,
        "packet_id": packet_id,
        "pairwise_subject": pairwise_subject,
        "grant": grant,
        "authority": authority,
        "bilateral_key": bilateral_key,
    }


def staged(pair):
    return pair["source"].stage(
        packet_id=pair["packet_id"],
        event_id=pair["event"].event_id,
        target_domain_id="beta.example",
        target_recipient_id=pair["recipient"].harness_id,
        guest_pairwise_subject=pair["pairwise_subject"],
        target_grant_id=pair["grant"].grant_id,
        authority=pair["authority"],
    )


def authority_decision_time(pair) -> int:
    row = pair["source_store"].fetch_one(
        "SELECT occurred_at FROM policy_decisions WHERE decision_id=?",
        (pair["authority"].policy_decision_id,),
    )
    assert row is not None
    return int(row["occurred_at"])


def versioned_services(pair, *, now_state: dict[str, int], new_key: bytes):
    old = RelayPeerKey(
        key_id=relay_peer_key_id(pair["bilateral_key"]),
        key_epoch=1,
        key=pair["bilateral_key"],
        provisioned_state="active",
    )
    replacement = RelayPeerKey(
        key_id=relay_peer_key_id(new_key),
        key_epoch=2,
        key=new_key,
        provisioned_state="pending",
        not_before=now_state["value"] + 1,
        expires_at=now_state["value"] + 7_200,
    )
    capabilities = frozenset(
        {
            ServerAgentCapability.OFFLINE_CUSTODY,
            ServerAgentCapability.RELAY,
            ServerAgentCapability.STORE_AND_FORWARD,
        }
    )
    source = ServerAgentRelayService(
        pair["source_store"],
        local_actor=pair["source_actor"],
        local_signer=pair["source_key"],
        peers={
            "beta.example": ServerAgentPeer(
                domain_id="beta.example",
                relay_harness_id=pair["target_actor"].harness_id,
                signing_key_id=pair["target_key"].thumbprint,
                public_key_pem=pair["target_key"].public_pem,
                key_versions=(old, replacement),
            )
        },
        runtime_capabilities=capabilities,
        mailbox=pair["source"].mailbox,
        policy=pair["source"].policy,
        admission=pair["source"].admission,
        clock=lambda: now_state["value"],
    )
    target = ServerAgentRelayService(
        pair["target_store"],
        local_actor=pair["target_actor"],
        local_signer=pair["target_key"],
        peers={
            "alpha.example": ServerAgentPeer(
                domain_id="alpha.example",
                relay_harness_id=pair["source_actor"].harness_id,
                signing_key_id=pair["source_key"].thumbprint,
                public_key_pem=pair["source_key"].public_pem,
                key_versions=(old, replacement),
            )
        },
        runtime_capabilities=capabilities,
        mailbox=pair["target"].mailbox,
        policy=pair["target"].policy,
        admission=pair["target"].admission,
        clock=lambda: now_state["value"],
    )
    return source, target, old, replacement


def rotation_for(pair, *, now_state: dict[str, int], old: RelayPeerKey, new: RelayPeerKey):
    return RelayPeerKeyRotation(
        mutation_id=f"rotation-{uuid4()}",
        domain_a_id="alpha.example",
        domain_b_id="beta.example",
        relay_a_harness_id=pair["source_actor"].harness_id,
        relay_b_harness_id=pair["target_actor"].harness_id,
        initiator_domain_id="alpha.example",
        initiator_relay_harness_id=pair["source_actor"].harness_id,
        from_key_id=old.key_id,
        from_key_epoch=old.key_epoch,
        to_key_id=new.key_id,
        to_key_epoch=new.key_epoch,
        activate_at=new.not_before,
        overlap_until=new.not_before + 60,
        new_key_expires_at=new.expires_at,
        issued_at=now_state["value"],
        expires_at=now_state["value"] + 120,
        reason="routine_rotation",
        nonce=f"relay-key-rotation-{uuid4().hex}",
    )


def relay_key_policy_decision(
    service: ServerAgentRelayService,
    *,
    action: str,
    peer_domain_id: str,
    command: RelayPeerKeyRotation | RelayPeerKeyRevocation,
) -> str:
    assert service.policy is not None
    if isinstance(command, RelayPeerKeyRotation):
        resource, request = service.key_rotation_binding(
            peer_domain_id=peer_domain_id,
            rotation=command,
        )
    else:
        resource, request = service.key_revocation_binding(
            peer_domain_id=peer_domain_id,
            revocation=command,
        )
    service.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=service.local_actor.domain_id,
            principal_id=service.local_actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=1,
        )
    )
    decision = service.policy.require(
        AuthorizationRequest(
            actor=service.local_actor,
            action=action,
            resource=resource,
            operation_class=OperationClass.PRIVILEGED,
            policy_revision=1,
            context=request,
        ),
        when=datetime.fromtimestamp(service.clock(), UTC),
    )
    return decision.decision_id


def stage_additional_message(pair, source: ServerAgentRelayService, *, label: str) -> RelayPacket:
    event = new_event(
        domain_id="alpha.example",
        actor=pair["source_actor"],
        event_type=EventType.MESSAGE,
        classification=Classification.C0_PUBLIC,
        payload={"message": label},
        idempotency_key=f"source-relay-{uuid4()}",
        recipients=(pair["source_actor"].harness_id,),
    )
    source.mailbox.accept(event)
    packet_id = source.new_packet_id()
    resource, context = source.stage_binding(
        packet_id=packet_id,
        event_id=event.event_id,
        target_domain_id="beta.example",
        target_recipient_id=pair["recipient"].harness_id,
        guest_pairwise_subject=pair["pairwise_subject"],
        target_grant_id=pair["grant"].grant_id,
    )
    source.policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id="alpha.example",
            principal_id=pair["source_actor"].principal_id,
            action="server_agent.relay.send",
            resource_pattern=resource,
            revision=1,
        )
    )
    decision = source.policy.require(
        AuthorizationRequest(
            actor=pair["source_actor"],
            action="server_agent.relay.send",
            resource=resource,
            policy_revision=1,
            context=context,
            classification=Classification.C0_PUBLIC,
        )
    )
    return source.stage(
        packet_id=packet_id,
        event_id=event.event_id,
        target_domain_id="beta.example",
        target_recipient_id=pair["recipient"].harness_id,
        guest_pairwise_subject=pair["pairwise_subject"],
        target_grant_id=pair["grant"].grant_id,
        authority=IssuanceAuthority(
            actor=pair["source_actor"],
            policy_decision_id=decision.decision_id,
        ),
    )


def composed_relay_core(
    tmp_path: Path,
    pair,
    *,
    side: str,
    bilateral_key: bytes | None = None,
) -> CommunicationCore:
    if side == "source":
        store = pair["source_store"]
        local_actor = pair["source_actor"]
        local_key = pair["source_key"]
        peer_actor = pair["target_actor"]
        peer_key = pair["target_key"]
    elif side == "target":
        store = pair["target_store"]
        local_actor = pair["target_actor"]
        local_key = pair["target_key"]
        peer_actor = pair["source_actor"]
        peer_key = pair["source_key"]
    else:
        raise ValueError("unknown relay test side")
    data_dir = tmp_path / f"composed-{side}-{uuid4().hex}"
    keys_dir = data_dir / "relay-keys"
    keys_dir.mkdir(parents=True, mode=0o700)
    keys_dir.chmod(0o700)
    signing_path = keys_dir / "signing.pem"
    signing_path.write_bytes(local_key.private_pem)
    signing_path.chmod(0o600)
    bilateral_path = keys_dir / "bilateral.bin"
    bilateral_path.write_bytes(bilateral_key or pair["bilateral_key"])
    bilateral_path.chmod(0o600)
    config = ExtensionConfig(
        domain_id=local_actor.domain_id,
        data_dir=data_dir,
        artifact_dir=data_dir / "artifacts",
        server_agent_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
                ServerAgentCapability.RELAY,
                ServerAgentCapability.STORE_AND_FORWARD,
            }
        ),
        features=FeatureFlags(peer_mesh=True),
        component_evidence={"peer_mesh": "focused-bilateral-relay-composition"},
        relay=RelayServiceConfig(
            signing_identity=RelaySigningIdentityConfig(
                harness_id=local_actor.harness_id,
                credential_id=local_actor.credential_id,
                private_key_path=signing_path,
            ),
            peers=(
                RelayPeerConfig(
                    domain_id=peer_actor.domain_id,
                    relay_harness_id=peer_actor.harness_id,
                    signing_key_id=peer_key.thumbprint,
                    public_key_pem=peer_key.public_pem,
                    key_versions=(
                        RelayPeerKeyConfig(
                            key_id=relay_peer_key_id(
                                bilateral_key
                                if bilateral_key is not None and len(bilateral_key) == 32
                                else pair["bilateral_key"]
                            ),
                            key_epoch=1,
                            bilateral_key_path=bilateral_path,
                            provisioned_state="active",
                            not_before=1,
                            expires_at=(1 << 62) - 1,
                        ),
                    ),
                ),
            ),
        ),
    )
    return CommunicationCore(config, store)


def test_two_ordinary_server_agents_relay_offline_then_reconnect_with_crash_recovery(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)
        assert pair["source"].pending_packets() == [packet]
        custody = pair["target"].accept(packet)
        assert custody.fact == "accepted_local"
        assert pair["source"].record_receipt(custody)["state"] == "remote_accepted"

        def crash(phase: str) -> None:
            if phase == "after_recipient_mailbox_commit":
                raise InjectedRelayCrash(phase)

        with pytest.raises(InjectedRelayCrash):
            pair["target"].deliver(packet.packet_id, phase_hook=crash)
        assert pair["target_store"].fetch_one(
            "SELECT state FROM server_agent_relay_inbox WHERE packet_id=?", (packet.packet_id,)
        )["state"] == "recipient_committed"

        committed = pair["target"].deliver(packet.packet_id)
        assert committed.fact == "recipient_committed"
        assert pair["source"].record_receipt(committed)["state"] == "recipient_committed"
        inbox = pair["target"].mailbox.reconcile(pair["recipient"].harness_id)
        assert len(inbox) == 1
        assert inbox[0]["payload"] == {"message": "offline relay synthetic"}
        assert inbox[0]["event"]["actor"]["kind"] == "host_guest_harness"
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_relay_outbox_route_reserves_pressure_and_terminal_receipt_releases_once(
    tmp_path: Path,
) -> None:
    pair = build_pair(tmp_path)
    try:
        policy = OperationsPolicy().model_copy(
            update={"pending_delivery_backpressure_limit": 2}
        )
        admission = QuotaService(
            pair["source_store"],
            policy=policy,
            safety_reserve_fraction=0,
        )
        pair["source"].admission = admission
        with pair["source_store"].transaction() as connection:
            connection.execute(
                "UPDATE recipients SET current_fact='completed' WHERE event_id=?",
                (pair["event"].event_id,),
            )

        packets = []
        requests = []
        for index in range(3):
            packet_id = pair["source"].new_packet_id()
            recipient_id = f"remote-recipient-{index}"
            resource, context = pair["source"].stage_binding(
                packet_id=packet_id,
                event_id=pair["event"].event_id,
                target_domain_id="beta.example",
                target_recipient_id=recipient_id,
                guest_pairwise_subject=pair["pairwise_subject"],
                target_grant_id=pair["grant"].grant_id,
            )
            decision = pair["source"].policy.require(
                AuthorizationRequest(
                    actor=pair["source_actor"],
                    action="server_agent.relay.send",
                    resource=resource,
                    policy_revision=1,
                    context=context,
                    classification=Classification.C0_PUBLIC,
                )
            )
            requests.append(
                dict(
                    packet_id=packet_id,
                    event_id=pair["event"].event_id,
                    target_domain_id="beta.example",
                    target_recipient_id=recipient_id,
                    guest_pairwise_subject=pair["pairwise_subject"],
                    target_grant_id=pair["grant"].grant_id,
                    authority=IssuanceAuthority(
                        actor=pair["source_actor"],
                        policy_decision_id=decision.decision_id,
                    ),
                )
            )
        packets.extend(pair["source"].stage(**request) for request in requests[:2])
        with pytest.raises(GateBlocked, match="pressure"):
            pair["source"].stage(**requests[2])
        receipt = pair["target"]._receipt(
            packets[0],
            fact="recipient_committed",
            local_event_id="terminal-local-event",
        )
        assert pair["source"].record_receipt(receipt)["state"] == "recipient_committed"
        assert pair["source"].record_receipt(receipt)["advanced"] is False
        pair["source"].stage(**requests[2])
        rows = pair["source_store"].fetch_all(
            """SELECT state FROM operational_work_reservations
                 WHERE work_kind='relay_outbound' ORDER BY source_id"""
        )
        assert sorted(row["state"] for row in rows) == ["pending", "pending", "terminal"]
        pair["source"].clock = lambda: max(packet.expires_at for packet in packets) + 1
        assert pair["source"].pending_outbox() == []
        assert pair["source"].expire_outbox() == 0
        assert pair["source_store"].fetch_one(
            """SELECT COUNT(*) AS count FROM operational_work_reservations
                 WHERE work_kind='relay_outbound' AND state='pending'"""
        )["count"] == 0
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_relay_inbox_expiry_terminalizes_capacity_before_delivery(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        admission = QuotaService(
            pair["target_store"],
            policy=OperationsPolicy(),
            safety_reserve_fraction=0,
        )
        pair["target"].admission = admission
        pair["target"].mailbox.admission = admission
        packet = staged(pair)
        pair["target"].accept(packet)
        pair["target"].clock = lambda: packet.expires_at
        with pytest.raises(GateBlocked, match="expired"):
            pair["target"].deliver(packet.packet_id)
        assert pair["target_store"].fetch_one(
            "SELECT state FROM server_agent_relay_inbox WHERE packet_id=?",
            (packet.packet_id,),
        )["state"] == "failed"
        assert pair["target_store"].fetch_one(
            """SELECT state FROM operational_work_reservations
                 WHERE work_kind='relay_inbound' AND source_id=?""",
            (packet.packet_id,),
        )["state"] == "terminal"
        assert pair["target"].expire_inbox() == 0
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_three_domain_guest_event_cannot_relay_onward_or_back_home_even_with_reused_authority(
    tmp_path: Path,
) -> None:
    """H -> A guest admission is not authority for A -> B or A -> H.

    The final inbound probes are deliberately signed by A and backed by an
    otherwise-valid B-local guest/grant.  They prove that a forged or replayed
    onward packet still fails on the source actor boundary, rather than merely
    because some incidental peer, recipient, or grant record is absent.
    """

    pair = build_pair(tmp_path)
    gamma_store = SQLiteStore(tmp_path / "gamma.sqlite3", LocalEnvelopeCipher(b"g" * 32))
    try:
        original = staged(pair)
        pair["target"].accept(original)
        committed = pair["target"].deliver(original.packet_id)
        assert committed.local_event_id is not None
        assert TaskGrantService(pair["target_store"]).uses_for_local_conformance(pair["grant"].grant_id) == 1

        row = pair["target_store"].fetch_one(
            "SELECT envelope_json,envelope_digest,payload_encrypted FROM events WHERE event_id=?",
            (committed.local_event_id,),
        )
        assert row is not None
        relayed_guest_event = EventEnvelope.model_validate(
            json.loads(row["envelope_json"])
            | {
                "payload": pair["target_store"].decrypted_payload(
                    row["payload_encrypted"], committed.local_event_id
                )
            }
        )
        assert relayed_guest_event.actor.kind is ActorKind.HOST_GUEST_HARNESS

        gamma_actor, gamma_key = enrolled_identity(
            gamma_store, domain="gamma.example", label="gamma-relay"
        )
        gamma_recipient, _ = enrolled_identity(
            gamma_store, domain="gamma.example", label="gamma-recipient"
        )
        gamma_guest_key = P256KeyPair.generate()
        gamma_pairwise_subject = "pairwise-beta-subject-0001"
        gamma_guest_id = "guest-beta-pairwise"
        gamma_guest_harness_id = "guest-beta-harness"
        gamma_guest_credential_id = "guest-beta-credential"
        now = int(time.time())
        with gamma_store.transaction() as connection:
            connection.execute(
                """INSERT INTO guests(
                    guest_id,host_domain_id,home_domain_id,pairwise_subject,sponsor_principal_id,status,expires_at
                ) VALUES(?,?,?,?,?,'active',?)""",
                (
                    gamma_guest_id,
                    "gamma.example",
                    "beta.example",
                    gamma_pairwise_subject,
                    gamma_actor.principal_id,
                    now + 3_600,
                ),
            )
            connection.execute(
                """INSERT INTO harnesses(
                    harness_id,domain_id,guest_id,kind,display_name,status,binding_assurance,
                    capabilities_json,credential_epoch,created_at
                ) VALUES(?,?,?,'federated_guest','beta guest','active','lab',?,1,?)""",
                (
                    gamma_guest_harness_id,
                    "gamma.example",
                    gamma_guest_id,
                    canonical_json({}).decode("utf-8"),
                    now,
                ),
            )
            connection.execute(
                """INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,'active',1,?,?)""",
                (
                    gamma_guest_credential_id,
                    gamma_guest_harness_id,
                    gamma_guest_key.thumbprint,
                    gamma_guest_key.public_pem,
                    now - 1,
                    now + 3_600,
                ),
            )
        gamma_grant = TaskGrant(
            domain_id="gamma.example",
            principal_id=gamma_guest_id,
            harness_id=gamma_guest_harness_id,
            actions=frozenset({"message.send"}),
            resources=frozenset({f"recipient:{gamma_recipient.harness_id}"}),
            input_sources=frozenset({"server_agent_relay"}),
            output_sinks=frozenset({f"mailbox:{gamma_recipient.harness_id}"}),
            data_classes=frozenset({Classification.C0_PUBLIC}),
            max_uses=1,
            expires_at=datetime.now(UTC) + timedelta(minutes=30),
        )
        with gamma_store.transaction() as connection:
            TaskGrantService(gamma_store)._insert_in_transaction(
                connection,
                grant=gamma_grant,
                when=datetime.now(UTC),
                issuance_evidence={"kind": "tested_direct_beta_gamma_admission"},
            )

        beta_gamma_key = b"b" * 32
        alpha_beta_active_key = RelayPeerKey(
            key_id=relay_peer_key_id(pair["bilateral_key"]),
            key_epoch=1,
            key=pair["bilateral_key"],
            provisioned_state="active",
        )
        beta_gamma_active_key = RelayPeerKey(
            key_id=relay_peer_key_id(beta_gamma_key),
            key_epoch=1,
            key=beta_gamma_key,
            provisioned_state="active",
        )
        beta_outbound = ServerAgentRelayService(
            pair["target_store"],
            local_actor=pair["target_actor"],
            local_signer=pair["target_key"],
            peers={
                "alpha.example": ServerAgentPeer(
                    domain_id="alpha.example",
                    relay_harness_id=pair["source_actor"].harness_id,
                    signing_key_id=pair["source_key"].thumbprint,
                    public_key_pem=pair["source_key"].public_pem,
                    key_versions=(alpha_beta_active_key,),
                ),
                "gamma.example": ServerAgentPeer(
                    domain_id="gamma.example",
                    relay_harness_id=gamma_actor.harness_id,
                    signing_key_id=gamma_key.thumbprint,
                    public_key_pem=gamma_key.public_pem,
                    key_versions=(beta_gamma_active_key,),
                ),
            },
            runtime_capabilities=frozenset(
                {
                    ServerAgentCapability.OFFLINE_CUSTODY,
                    ServerAgentCapability.RELAY,
                    ServerAgentCapability.STORE_AND_FORWARD,
                }
            ),
            mailbox=pair["target"].mailbox,
            policy=pair["target"].policy,
        )

        onward_attempts = (
            (
                "gamma.example",
                gamma_actor.harness_id,
                gamma_pairwise_subject,
                gamma_grant.grant_id,
            ),
            (
                "alpha.example",
                pair["source_actor"].harness_id,
                pair["pairwise_subject"],
                pair["grant"].grant_id,
            ),
        )
        attempted_packet_ids: list[str] = []
        for target_domain, recipient_id, pairwise_subject, grant_id in onward_attempts:
            packet_id = beta_outbound.new_packet_id()
            attempted_packet_ids.append(packet_id)
            resource, context = beta_outbound.stage_binding(
                packet_id=packet_id,
                event_id=committed.local_event_id,
                target_domain_id=target_domain,
                target_recipient_id=recipient_id,
                guest_pairwise_subject=pairwise_subject,
                target_grant_id=grant_id,
            )
            beta_outbound.policy.bootstrap_entitlement_for_local_conformance(
                HumanEntitlement(
                    domain_id="beta.example",
                    principal_id=pair["target_actor"].principal_id,
                    action="server_agent.relay.send",
                    resource_pattern=resource,
                    revision=1,
                )
            )
            decision = beta_outbound.policy.require(
                AuthorizationRequest(
                    actor=pair["target_actor"],
                    action="server_agent.relay.send",
                    resource=resource,
                    policy_revision=1,
                    context=context,
                    classification=Classification.C0_PUBLIC,
                )
            )
            with pytest.raises(AuthorizationError, match="non-transitive federation forbids onward relay"):
                beta_outbound.stage(
                    packet_id=packet_id,
                    event_id=committed.local_event_id,
                    target_domain_id=target_domain,
                    target_recipient_id=recipient_id,
                    guest_pairwise_subject=pairwise_subject,
                    target_grant_id=grant_id,
                    authority=IssuanceAuthority(
                        actor=pair["target_actor"], policy_decision_id=decision.decision_id
                    ),
                )
        assert all(
            pair["target_store"].fetch_one(
                "SELECT 1 FROM server_agent_relay_outbox WHERE packet_id=?", (packet_id,)
            )
            is None
            for packet_id in attempted_packet_ids
        )

        gamma = ServerAgentRelayService(
            gamma_store,
            local_actor=gamma_actor,
            local_signer=gamma_key,
            peers={
                "beta.example": ServerAgentPeer(
                    domain_id="beta.example",
                    relay_harness_id=pair["target_actor"].harness_id,
                    signing_key_id=pair["target_key"].thumbprint,
                    public_key_pem=pair["target_key"].public_pem,
                    key_versions=(beta_gamma_active_key,),
                )
            },
            runtime_capabilities=frozenset(
                {
                    ServerAgentCapability.OFFLINE_CUSTODY,
                    ServerAgentCapability.RELAY,
                    ServerAgentCapability.STORE_AND_FORWARD,
                }
            ),
            mailbox=MailboxService(gamma_store, acceptance_fact=DeliveryFact.ACCEPTED_LOCAL),
            policy=LocalConformancePolicyEngine(gamma_store),
        )
        forged_packet_id = gamma.new_packet_id()
        purpose = f"server-relay:{forged_packet_id}:beta.example:gamma.example"
        ciphertext = LocalEnvelopeCipher(beta_gamma_key).encrypt_json(
            {"event": relayed_guest_event.model_dump(mode="json")}, purpose=purpose
        )
        forged_fields = {
            "profile": "agentnet.server-relay.packet.v1",
            "hop_count": 1,
            "max_hops": 1,
            "packet_id": forged_packet_id,
            "source_domain_id": "beta.example",
            "source_relay_harness_id": pair["target_actor"].harness_id,
            "source_key_id": pair["target_key"].thumbprint,
            "peer_key_id": relay_peer_key_id(beta_gamma_key),
            "peer_key_epoch": 1,
            "target_domain_id": "gamma.example",
            "target_relay_harness_id": gamma_actor.harness_id,
            "target_recipient_id": gamma_recipient.harness_id,
            "target_grant_id": gamma_grant.grant_id,
            "guest_pairwise_subject": gamma_pairwise_subject,
            "source_event_id": relayed_guest_event.event_id,
            "source_event_digest": envelope_digest(relayed_guest_event),
            "ciphertext": ciphertext,
            "created_at": now,
            "expires_at": now + 300,
        }
        forged = RelayPacket(
            **forged_fields,
            signature=pair["target_key"].sign("agentnet.server-relay.packet.v1", forged_fields),
        )
        for _ in range(2):
            with pytest.raises(AuthorizationError, match="non-transitive federation forbids onward relay"):
                gamma.accept(forged)
        assert gamma_store.fetch_one(
            "SELECT 1 FROM server_agent_relay_inbox WHERE packet_id=?", (forged.packet_id,)
        ) is None
        assert TaskGrantService(gamma_store).uses_for_local_conformance(gamma_grant.grant_id) == 0
        assert TaskGrantService(pair["target_store"]).uses_for_local_conformance(pair["grant"].grant_id) == 1
    finally:
        pair["source_store"].close()
        pair["target_store"].close()
        gamma_store.close()


def test_relay_task_stays_out_of_recipient_mailbox_until_exact_owner_approval(tmp_path: Path) -> None:
    pair = build_pair(tmp_path, event_type=EventType.TASK_ASSIGNMENT)
    try:
        packet = staged(pair)
        custody = pair["target"].accept(packet)
        assert custody.fact == "accepted_local"
        assert pair["target"].mailbox.reconcile(pair["recipient"].harness_id) == []
        proposals = pair["target"].assignments.pending_for_owner(actor=pair["recipient"])
        assert len(proposals) == 1
        assert "offline relay synthetic" not in str(proposals[0])

        still_pending = pair["target"].deliver(packet.packet_id)
        assert still_pending.fact == "accepted_local"
        assert still_pending.local_event_id is None
        proposal = proposals[0]
        pair["target"].assignments.approve(
            actor=pair["recipient"],
            proposal_id=proposal["proposal_id"],
            expected_request_digest=proposal["request_digest"],
            expected_revision=proposal["revision"],
        )
        committed = pair["target"].deliver(packet.packet_id)
        assert committed.fact == "recipient_committed"
        inbox = pair["target"].mailbox.reconcile(pair["recipient"].harness_id)
        assert len(inbox) == 1
        assert inbox[0]["fact"] == DeliveryFact.ACCEPTED_QUEUED.value
        assert inbox[0]["payload"] is None
        assert inbox[0]["payload_available"] is False
        assert inbox[0]["payload_access"] == "task_grant_required"
        assert "offline relay synthetic" not in json.dumps(inbox[0], sort_keys=True)
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_relay_has_no_special_identity_and_revocation_blocks_pending_delivery(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        assert pair["source_actor"].kind is ActorKind.VERIFIED_HUMAN_HARNESS
        assert pair["target_actor"].kind is ActorKind.VERIFIED_HUMAN_HARNESS
        packet = staged(pair)
        pair["target"].accept(packet)
        with pair["target_store"].transaction() as connection:
            connection.execute("UPDATE guests SET status='revoked' WHERE guest_id=?", (pair["guest_actor"].guest_id,))
            connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (pair["guest_actor"].harness_id,))
            connection.execute("UPDATE credentials SET status='revoked' WHERE credential_id=?", (pair["guest_actor"].credential_id,))
        with pytest.raises(AuthorizationError, match="no longer current"):
            pair["target"].deliver(packet.packet_id)
        assert pair["target"].mailbox.reconcile(pair["recipient"].harness_id) == []
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_relay_capabilities_attenuate_but_do_not_replace_policy_or_grants(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        source = pair["source"]
        attenuated_source = ServerAgentRelayService(
            pair["source_store"],
            local_actor=source.local_actor,
            local_signer=source.local_signer,
            peers=source.peers,
            runtime_capabilities=frozenset({ServerAgentCapability.RELAY}),
            mailbox=source.mailbox,
            policy=source.policy,
        )
        with pytest.raises(GateBlocked, match="store_and_forward"):
            attenuated_source.stage(
                packet_id=pair["packet_id"],
                event_id=pair["event"].event_id,
                target_domain_id="beta.example",
                target_recipient_id=pair["recipient"].harness_id,
                guest_pairwise_subject=pair["pairwise_subject"],
                target_grant_id=pair["grant"].grant_id,
                authority=pair["authority"],
            )

        packet = staged(pair)
        target = pair["target"]
        attenuated_target = ServerAgentRelayService(
            pair["target_store"],
            local_actor=target.local_actor,
            local_signer=target.local_signer,
            peers=target.peers,
            runtime_capabilities=frozenset(
                {ServerAgentCapability.RELAY, ServerAgentCapability.STORE_AND_FORWARD}
            ),
            mailbox=target.mailbox,
            policy=target.policy,
        )
        with pytest.raises(GateBlocked, match="offline_custody"):
            attenuated_target.accept(packet)
        assert TaskGrantService(pair["target_store"]).uses_for_local_conformance(pair["grant"].grant_id) == 0

        # Enabling runtime capabilities is still not positive message authority:
        # revoking the exact target grant fails closed.
        with pair["target_store"].transaction() as connection:
            connection.execute(
                "UPDATE task_grants SET revoked_at=? WHERE grant_id=?",
                (int(time.time()), pair["grant"].grant_id),
            )
        with pytest.raises(AuthorizationError):
            pair["target"].accept(packet)
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_relay_acceptance_crash_duplicate_tamper_and_pending_recovery(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)

        def crash_after_insert(phase: str) -> None:
            if phase == "after_inbox_insert":
                raise InjectedRelayCrash(phase)

        with pytest.raises(InjectedRelayCrash):
            pair["target"].accept(packet, phase_hook=crash_after_insert)
        assert pair["target_store"].fetch_one(
            "SELECT packet_id FROM server_agent_relay_inbox WHERE packet_id=?", (packet.packet_id,)
        ) is None
        assert TaskGrantService(pair["target_store"]).uses_for_local_conformance(pair["grant"].grant_id) == 0

        custody = pair["target"].accept(packet)
        duplicate = pair["target"].accept(packet)
        assert duplicate.fact == custody.fact == "accepted_local"
        assert TaskGrantService(pair["target_store"]).uses_for_local_conformance(pair["grant"].grant_id) == 1
        assert [item["packet_id"] for item in pair["target"].pending_inbox()] == [packet.packet_id]

        tampered = packet.model_copy(update={"target_recipient_id": pair["target_actor"].harness_id})
        with pytest.raises(AuthenticationError, match="signature"):
            pair["target"].accept(tampered)

        pair["target"].recover_pending_inbox()
        assert pair["target"].pending_inbox() == []
        assert pair["target"].mailbox.reconcile(pair["recipient"].harness_id)[0]["payload"] == {
            "message": "offline relay synthetic"
        }
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_relay_stale_or_revoked_local_identity_fails_each_operation(tmp_path: Path) -> None:
    stale_root = tmp_path / "stale"
    stale_root.mkdir(mode=0o700)
    stale_pair = build_pair(stale_root)
    try:
        packet = staged(stale_pair)
        stale_pair["target"].clock = lambda: packet.expires_at + 1
        with pytest.raises(AuthenticationError, match="freshness"):
            stale_pair["target"].accept(packet)
    finally:
        stale_pair["source_store"].close()
        stale_pair["target_store"].close()

    revoked_root = tmp_path / "revoked"
    revoked_root.mkdir(mode=0o700)
    revoked_pair = build_pair(revoked_root)
    try:
        packet = staged(revoked_pair)
        custody = revoked_pair["target"].accept(packet)
        with revoked_pair["source_store"].transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (revoked_pair["source_actor"].credential_id,),
            )
        with pytest.raises(AuthenticationError, match="unavailable"):
            revoked_pair["source"].record_receipt(custody)
        with pytest.raises(AuthenticationError, match="unavailable"):
            revoked_pair["source"].pending_outbox()
    finally:
        revoked_pair["source_store"].close()
        revoked_pair["target_store"].close()


def test_relay_revalidates_local_credential_before_stage_accept_and_receipt_signing(tmp_path: Path) -> None:
    source_root = tmp_path / "source-stage"
    source_root.mkdir(mode=0o700)
    source_pair = build_pair(source_root)
    try:
        with source_pair["source_store"].transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (source_pair["source_actor"].credential_id,),
            )
        with pytest.raises(AuthenticationError, match="unavailable"):
            staged(source_pair)
        assert source_pair["source_store"].fetch_one(
            "SELECT packet_id FROM server_agent_relay_outbox WHERE packet_id=?",
            (source_pair["packet_id"],),
        ) is None
    finally:
        source_pair["source_store"].close()
        source_pair["target_store"].close()

    target_root = tmp_path / "target-accept"
    target_root.mkdir(mode=0o700)
    target_pair = build_pair(target_root)
    try:
        packet = staged(target_pair)
        with target_pair["target_store"].transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (target_pair["target_actor"].credential_id,),
            )
        with pytest.raises(AuthenticationError, match="unavailable"):
            target_pair["target"].accept(packet)
        assert TaskGrantService(target_pair["target_store"]).uses_for_local_conformance(target_pair["grant"].grant_id) == 0
    finally:
        target_pair["source_store"].close()
        target_pair["target_store"].close()

    receipt_root = tmp_path / "target-receipt"
    receipt_root.mkdir(mode=0o700)
    receipt_pair = build_pair(receipt_root)
    try:
        packet = staged(receipt_pair)
        receipt_pair["target"].accept(packet)
        with receipt_pair["target_store"].transaction() as connection:
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=?",
                (receipt_pair["target_actor"].credential_id,),
            )
        with pytest.raises(AuthenticationError, match="unavailable"):
            receipt_pair["target"].receipt_for(packet.packet_id)
        with pytest.raises(AuthenticationError, match="unavailable"):
            receipt_pair["target"].deliver(packet.packet_id)
    finally:
        receipt_pair["source_store"].close()
        receipt_pair["target_store"].close()


def test_relay_receipts_are_monotonic_duplicate_safe_and_crash_recoverable(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)
        custody = pair["target"].accept(packet)

        def crash_before_receipt_commit(phase: str) -> None:
            if phase == "before_receipt_commit":
                raise InjectedRelayCrash(phase)

        with pytest.raises(InjectedRelayCrash):
            pair["source"].record_receipt(custody, phase_hook=crash_before_receipt_commit)
        assert pair["source_store"].fetch_one(
            "SELECT state FROM server_agent_relay_outbox WHERE packet_id=?", (packet.packet_id,)
        )["state"] == "staged"
        assert pair["source"].record_receipt(custody) == {
            "packet_id": packet.packet_id,
            "state": "remote_accepted",
            "advanced": True,
        }

        committed = pair["target"].deliver(packet.packet_id)
        assert pair["source"].record_receipt(committed)["state"] == "recipient_committed"
        duplicate = pair["source"].record_receipt(committed)
        assert duplicate["state"] == "recipient_committed"
        assert duplicate["advanced"] is False
        late_custody = pair["source"].record_receipt(custody)
        assert late_custody["state"] == "recipient_committed"
        assert late_custody["advanced"] is False
        assert pair["source"].pending_outbox() == []
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_versioned_peer_key_rotation_preserves_bounded_old_queue_and_uses_new_key(
    tmp_path: Path,
) -> None:
    pair = build_pair(tmp_path, grant_max_uses=3)
    try:
        now_state = {"value": authority_decision_time(pair)}
        source, target, old, new = versioned_services(
            pair,
            now_state=now_state,
            new_key=b"n" * 32,
        )
        old_packet = source.stage(
            packet_id=pair["packet_id"],
            event_id=pair["event"].event_id,
            target_domain_id="beta.example",
            target_recipient_id=pair["recipient"].harness_id,
            guest_pairwise_subject=pair["pairwise_subject"],
            target_grant_id=pair["grant"].grant_id,
            authority=pair["authority"],
        )
        delayed_old_packet = stage_additional_message(pair, source, label="delayed old key")
        assert old_packet.peer_key_id == delayed_old_packet.peer_key_id == old.key_id
        assert old_packet.peer_key_epoch == 1

        rotation = rotation_for(pair, now_state=now_state, old=old, new=new)
        now_state["value"] = rotation.activate_at
        source_rotation_decision = relay_key_policy_decision(
            source,
            action="server_agent.relay.key.rotate",
            peer_domain_id="beta.example",
            command=rotation,
        )
        target_rotation_decision = relay_key_policy_decision(
            target,
            action="server_agent.relay.key.rotate",
            peer_domain_id="alpha.example",
            command=rotation,
        )
        attacker = P256KeyPair.generate()
        with pytest.raises(AuthenticationError, match="signature"):
            source.rotate_peer_key(
                peer_domain_id="beta.example",
                rotation=rotation,
                local_signature=pair["source_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    rotation.signed_fields(),
                ),
                peer_signature=attacker.sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    rotation.signed_fields(),
                ),
                policy_decision_id=source_rotation_decision,
            )
        wrong_domain = rotation.model_copy(update={"domain_b_id": "gamma.example"})
        with pytest.raises(AuthenticationError, match="endpoint"):
            source.rotate_peer_key(
                peer_domain_id="beta.example",
                rotation=wrong_domain,
                local_signature=pair["source_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    wrong_domain.signed_fields(),
                ),
                peer_signature=pair["target_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    wrong_domain.signed_fields(),
                ),
                policy_decision_id=source_rotation_decision,
            )
        wrong_epoch = rotation.model_copy(
            update={
                "from_key_epoch": 2,
                "to_key_epoch": 3,
                "to_key_id": relay_peer_key_id(b"u" * 32),
            }
        )
        with pytest.raises(AuthenticationError, match="not provisioned"):
            source.rotate_peer_key(
                peer_domain_id="beta.example",
                rotation=wrong_epoch,
                local_signature=pair["source_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    wrong_epoch.signed_fields(),
                ),
                peer_signature=pair["target_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    wrong_epoch.signed_fields(),
                ),
                policy_decision_id=source_rotation_decision,
            )
        with pytest.raises(AuthorizationError, match="decision is unavailable"):
            source.rotate_peer_key(
                peer_domain_id="beta.example",
                rotation=rotation,
                local_signature=pair["source_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    rotation.signed_fields(),
                ),
                peer_signature=pair["target_key"].sign(
                    RELAY_KEY_ROTATION_PURPOSE,
                    rotation.signed_fields(),
                ),
                policy_decision_id="missing-relay-key-authority-decision",
            )
        source_result = source.rotate_peer_key(
            peer_domain_id="beta.example",
            rotation=rotation,
            local_signature=pair["source_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            peer_signature=pair["target_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            policy_decision_id=source_rotation_decision,
        )
        target.rotate_peer_key(
            peer_domain_id="alpha.example",
            rotation=rotation,
            local_signature=pair["target_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            peer_signature=pair["source_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            policy_decision_id=target_rotation_decision,
        )
        assert source_result["active_key_epoch"] == 2
        duplicate = source.rotate_peer_key(
            peer_domain_id="beta.example",
            rotation=rotation,
            local_signature=pair["source_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            peer_signature=pair["target_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            policy_decision_id=source_rotation_decision,
        )
        assert duplicate["duplicate"] is True

        source = ServerAgentRelayService(
            pair["source_store"],
            local_actor=pair["source_actor"],
            local_signer=pair["source_key"],
            peers={
                "beta.example": ServerAgentPeer(
                    domain_id="beta.example",
                    relay_harness_id=pair["target_actor"].harness_id,
                    signing_key_id=pair["target_key"].thumbprint,
                    public_key_pem=pair["target_key"].public_pem,
                    key_versions=(old, new),
                )
            },
            runtime_capabilities=frozenset(
                {
                    ServerAgentCapability.OFFLINE_CUSTODY,
                    ServerAgentCapability.RELAY,
                    ServerAgentCapability.STORE_AND_FORWARD,
                }
            ),
            mailbox=pair["source"].mailbox,
            policy=pair["source"].policy,
            admission=pair["source"].admission,
            clock=lambda: now_state["value"],
        )
        new_packet = stage_additional_message(pair, source, label="new key packet")
        assert new_packet.peer_key_id == new.key_id
        assert new_packet.peer_key_epoch == 2
        old_receipt = target.accept(old_packet)
        new_receipt = target.accept(new_packet)
        assert old_receipt.fact == new_receipt.fact == "accepted_local"
        assert source.record_receipt(old_receipt)["state"] == "remote_accepted"
        assert target.accept(old_packet).packet_digest == old_receipt.packet_digest
        assert TaskGrantService(pair["target_store"]).uses_for_local_conformance(
            pair["grant"].grant_id
        ) == 2

        now_state["value"] = rotation.overlap_until
        with pytest.raises(AuthenticationError, match="overlap"):
            target.accept(delayed_old_packet)
        assert source.expire_peer_key_overlaps() == 1
        delayed_state = pair["source_store"].fetch_one(
            "SELECT state FROM server_agent_relay_outbox WHERE packet_id=?",
            (delayed_old_packet.packet_id,),
        )
        assert delayed_state["state"] == "failed"
        accepted_state = pair["source_store"].fetch_one(
            "SELECT state FROM server_agent_relay_outbox WHERE packet_id=?",
            (old_packet.packet_id,),
        )
        assert accepted_state["state"] == "remote_accepted"
        assert source.pending_packets() == [new_packet]
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_compromise_revocation_quarantines_queued_packets_without_killing_replacement(
    tmp_path: Path,
) -> None:
    pair = build_pair(tmp_path, grant_max_uses=3)
    try:
        now_state = {"value": authority_decision_time(pair)}
        source, target, old, new = versioned_services(
            pair,
            now_state=now_state,
            new_key=b"k" * 32,
        )
        old_packet = source.stage(
            packet_id=pair["packet_id"],
            event_id=pair["event"].event_id,
            target_domain_id="beta.example",
            target_recipient_id=pair["recipient"].harness_id,
            guest_pairwise_subject=pair["pairwise_subject"],
            target_grant_id=pair["grant"].grant_id,
            authority=pair["authority"],
        )
        rotation = rotation_for(pair, now_state=now_state, old=old, new=new)
        now_state["value"] = rotation.activate_at
        source_rotation_decision = relay_key_policy_decision(
            source,
            action="server_agent.relay.key.rotate",
            peer_domain_id="beta.example",
            command=rotation,
        )
        target_rotation_decision = relay_key_policy_decision(
            target,
            action="server_agent.relay.key.rotate",
            peer_domain_id="alpha.example",
            command=rotation,
        )
        source.rotate_peer_key(
            peer_domain_id="beta.example",
            rotation=rotation,
            local_signature=pair["source_key"].sign(RELAY_KEY_ROTATION_PURPOSE, rotation.signed_fields()),
            peer_signature=pair["target_key"].sign(RELAY_KEY_ROTATION_PURPOSE, rotation.signed_fields()),
            policy_decision_id=source_rotation_decision,
        )
        target.rotate_peer_key(
            peer_domain_id="alpha.example",
            rotation=rotation,
            local_signature=pair["target_key"].sign(RELAY_KEY_ROTATION_PURPOSE, rotation.signed_fields()),
            peer_signature=pair["source_key"].sign(RELAY_KEY_ROTATION_PURPOSE, rotation.signed_fields()),
            policy_decision_id=target_rotation_decision,
        )
        target.accept(old_packet)

        source_revocation = RelayPeerKeyRevocation(
            mutation_id=f"revocation-{uuid4()}",
            local_domain_id="alpha.example",
            peer_domain_id="beta.example",
            local_relay_harness_id=pair["source_actor"].harness_id,
            peer_relay_harness_id=pair["target_actor"].harness_id,
            key_id=old.key_id,
            key_epoch=old.key_epoch,
            reason="confirmed_compromise",
            issued_at=now_state["value"],
            expires_at=now_state["value"] + 120,
            nonce=f"source-relay-key-revocation-{uuid4().hex}",
        )
        source_revocation_decision = relay_key_policy_decision(
            source,
            action="server_agent.relay.key.revoke",
            peer_domain_id="beta.example",
            command=source_revocation,
        )
        with pytest.raises(AuthenticationError, match="signature"):
            source.revoke_peer_key(
                peer_domain_id="beta.example",
                revocation=source_revocation,
                local_signature=P256KeyPair.generate().sign(
                    RELAY_KEY_REVOCATION_PURPOSE,
                    source_revocation.signed_fields(),
                ),
                policy_decision_id=source_revocation_decision,
            )
        source_result = source.revoke_peer_key(
            peer_domain_id="beta.example",
            revocation=source_revocation,
            local_signature=pair["source_key"].sign(
                RELAY_KEY_REVOCATION_PURPOSE,
                source_revocation.signed_fields(),
            ),
            policy_decision_id=source_revocation_decision,
        )
        assert source_result["failed_outbox"] == 1
        assert source.revoke_peer_key(
            peer_domain_id="beta.example",
            revocation=source_revocation,
            local_signature=pair["source_key"].sign(
                RELAY_KEY_REVOCATION_PURPOSE,
                source_revocation.signed_fields(),
            ),
            policy_decision_id=source_revocation_decision,
        )["duplicate"] is True
        conflicting = source_revocation.model_copy(update={"reason": "suspected_compromise"})
        conflicting_decision = relay_key_policy_decision(
            source,
            action="server_agent.relay.key.revoke",
            peer_domain_id="beta.example",
            command=conflicting,
        )
        with pytest.raises(ConflictError, match="already consumed"):
            source.revoke_peer_key(
                peer_domain_id="beta.example",
                revocation=conflicting,
                local_signature=pair["source_key"].sign(
                    RELAY_KEY_REVOCATION_PURPOSE,
                    conflicting.signed_fields(),
                ),
                policy_decision_id=conflicting_decision,
            )

        target_revocation = source_revocation.model_copy(
            update={
                "mutation_id": f"revocation-{uuid4()}",
                "local_domain_id": "beta.example",
                "peer_domain_id": "alpha.example",
                "local_relay_harness_id": pair["target_actor"].harness_id,
                "peer_relay_harness_id": pair["source_actor"].harness_id,
                "nonce": f"target-relay-key-revocation-{uuid4().hex}",
            }
        )
        target_revocation_decision = relay_key_policy_decision(
            target,
            action="server_agent.relay.key.revoke",
            peer_domain_id="alpha.example",
            command=target_revocation,
        )
        target_result = target.revoke_peer_key(
            peer_domain_id="alpha.example",
            revocation=target_revocation,
            local_signature=pair["target_key"].sign(
                RELAY_KEY_REVOCATION_PURPOSE,
                target_revocation.signed_fields(),
            ),
            policy_decision_id=target_revocation_decision,
        )
        assert target_result["failed_inbox"] == 1
        with pytest.raises(AuthenticationError, match="revoked"):
            target.accept(old_packet)
        with pytest.raises(ConflictError, match="not recoverable"):
            target.deliver(old_packet.packet_id)

        new_packet = stage_additional_message(pair, source, label="replacement survives compromise")
        assert new_packet.peer_key_id == new.key_id
        assert target.accept(new_packet).fact == "accepted_local"
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


@pytest.mark.anyio
async def test_relay_key_rotation_http_is_dual_signed_strict_and_replay_safe(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        now_state = {"value": authority_decision_time(pair)}
        _source, target, old, new = versioned_services(
            pair,
            now_state=now_state,
            new_key=b"h" * 32,
        )
        rotation = rotation_for(pair, now_state=now_state, old=old, new=new)
        now_state["value"] = rotation.activate_at
        target_rotation_decision = relay_key_policy_decision(
            target,
            action="server_agent.relay.key.rotate",
            peer_domain_id="alpha.example",
            command=rotation,
        )
        path = "/v1/server-agent-relay/peers/alpha.example/key-rotations"
        valid_body = {
            "rotation": rotation.model_dump(mode="json"),
            "local_signature": pair["target_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            "peer_signature": pair["source_key"].sign(
                RELAY_KEY_ROTATION_PURPOSE,
                rotation.signed_fields(),
            ),
            "policy_decision_id": target_rotation_decision,
        }
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=create_relay_app(target), raise_app_exceptions=False),
            base_url="http://127.0.0.1",
        ) as client:
            injected = await client.post(
                path,
                content=canonical_json(valid_body | {"actor_role": "relay-admin"}),
                headers={"Content-Type": "application/json"},
            )
            assert injected.status_code == 422
            wrong_peer = await client.post(
                path,
                content=canonical_json(
                    valid_body
                    | {
                        "peer_signature": P256KeyPair.generate().sign(
                            RELAY_KEY_ROTATION_PURPOSE,
                            rotation.signed_fields(),
                        )
                    }
                ),
                headers={"Content-Type": "application/json"},
            )
            assert wrong_peer.status_code == 401
            created = await client.post(
                path,
                content=canonical_json(valid_body),
                headers={"Content-Type": "application/json"},
            )
            assert created.status_code == 201, created.text
            replay = await client.post(
                path,
                content=canonical_json(valid_body),
                headers={"Content-Type": "application/json"},
            )
            assert replay.status_code == 200
            assert replay.json()["duplicate"] is True

            revocation = RelayPeerKeyRevocation(
                mutation_id=f"revocation-{uuid4()}",
                local_domain_id="beta.example",
                peer_domain_id="alpha.example",
                local_relay_harness_id=pair["target_actor"].harness_id,
                peer_relay_harness_id=pair["source_actor"].harness_id,
                key_id=old.key_id,
                key_epoch=old.key_epoch,
                reason="peer_incident",
                issued_at=now_state["value"],
                expires_at=now_state["value"] + 120,
                nonce=f"http-relay-key-revocation-{uuid4().hex}",
            )
            revocation_decision = relay_key_policy_decision(
                target,
                action="server_agent.relay.key.revoke",
                peer_domain_id="alpha.example",
                command=revocation,
            )
            revocation_path = "/v1/server-agent-relay/peers/alpha.example/key-revocations"
            revocation_body = {
                "revocation": revocation.model_dump(mode="json"),
                "local_signature": pair["target_key"].sign(
                    RELAY_KEY_REVOCATION_PURPOSE,
                    revocation.signed_fields(),
                ),
                "policy_decision_id": revocation_decision,
            }
            revoked = await client.post(
                revocation_path,
                content=canonical_json(revocation_body),
                headers={"Content-Type": "application/json"},
            )
            assert revoked.status_code == 200, revoked.text
            assert revoked.json()["status"] == "revoked"
            revocation_replay = await client.post(
                revocation_path,
                content=canonical_json(revocation_body),
                headers={"Content-Type": "application/json"},
            )
            assert revocation_replay.status_code == 200
            assert revocation_replay.json()["duplicate"] is True
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_rotation_and_compromise_revocation_race_never_resurrects_old_key(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        now_state = {"value": authority_decision_time(pair)}
        source, _target, old, new = versioned_services(
            pair,
            now_state=now_state,
            new_key=b"z" * 32,
        )
        rotation = rotation_for(pair, now_state=now_state, old=old, new=new)
        now_state["value"] = rotation.activate_at
        revocation = RelayPeerKeyRevocation(
            mutation_id=f"revocation-{uuid4()}",
            local_domain_id="alpha.example",
            peer_domain_id="beta.example",
            local_relay_harness_id=pair["source_actor"].harness_id,
            peer_relay_harness_id=pair["target_actor"].harness_id,
            key_id=old.key_id,
            key_epoch=old.key_epoch,
            reason="suspected_compromise",
            issued_at=now_state["value"],
            expires_at=now_state["value"] + 120,
            nonce=f"race-relay-key-revocation-{uuid4().hex}",
        )
        rotation_decision = relay_key_policy_decision(
            source,
            action="server_agent.relay.key.rotate",
            peer_domain_id="beta.example",
            command=rotation,
        )
        revocation_decision = relay_key_policy_decision(
            source,
            action="server_agent.relay.key.revoke",
            peer_domain_id="beta.example",
            command=revocation,
        )
        barrier = Barrier(2)

        def rotate():
            barrier.wait()
            try:
                return source.rotate_peer_key(
                    peer_domain_id="beta.example",
                    rotation=rotation,
                    local_signature=pair["source_key"].sign(
                        RELAY_KEY_ROTATION_PURPOSE,
                        rotation.signed_fields(),
                    ),
                        peer_signature=pair["target_key"].sign(
                            RELAY_KEY_ROTATION_PURPOSE,
                            rotation.signed_fields(),
                        ),
                        policy_decision_id=rotation_decision,
                    )
            except ConflictError as exc:
                return exc

        def revoke():
            barrier.wait()
            try:
                return source.revoke_peer_key(
                    peer_domain_id="beta.example",
                    revocation=revocation,
                    local_signature=pair["source_key"].sign(
                            RELAY_KEY_REVOCATION_PURPOSE,
                            revocation.signed_fields(),
                        ),
                        policy_decision_id=revocation_decision,
                    )
            except ConflictError as exc:
                return exc

        with ThreadPoolExecutor(max_workers=2) as pool:
            results = [pool.submit(rotate), pool.submit(revoke)]
            outcomes = [future.result(timeout=10) for future in results]
        assert any(isinstance(value, dict) for value in outcomes)
        rows = pair["source_store"].fetch_all(
            """SELECT key_id,key_epoch,state FROM relay_peer_keys
                 WHERE local_domain_id='alpha.example' AND peer_domain_id='beta.example'
                 ORDER BY key_epoch"""
        )
        assert rows[0]["state"] == "revoked"
        assert sum(row["state"] == "active" for row in rows) <= 1
        assert all(
            not (row["key_id"] == old.key_id and row["state"] == "active")
            for row in rows
        )
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_sqlite_relay_cannot_be_configured_to_claim_accepted_durable(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "local.sqlite3", LocalEnvelopeCipher(b"l" * 32))
    try:
        with pytest.raises(ValueError, match="verified storage boundary"):
            MailboxService(store, acceptance_fact=DeliveryFact.ACCEPTED_DURABLE)
    finally:
        store.close()


@pytest.mark.anyio
async def test_mounted_two_server_agent_offline_relay_round_trip_and_restart(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)
        assert pair["source"].recover_pending_outbox()[0]["packet"] == packet

        # Reconstructing the ordinary agent's relay component proves that the
        # outbound work comes from durable storage, not process memory.
        original = pair["source"]
        restarted_source = ServerAgentRelayService(
            pair["source_store"],
            local_actor=original.local_actor,
            local_signer=original.local_signer,
            peers=original.peers,
            runtime_capabilities=original.runtime_capabilities,
            mailbox=original.mailbox,
            policy=original.policy,
            admission=QuotaService(
                pair["source_store"],
                policy=OperationsPolicy(),
                safety_reserve_fraction=0,
            ),
        )
        recovered_packet = restarted_source.recover_pending_outbox()[0]["packet"]
        target_admission = QuotaService(
            pair["target_store"],
            policy=OperationsPolicy(),
            safety_reserve_fraction=0,
        )
        pair["target"].admission = target_admission
        pair["target"].mailbox.admission = target_admission

        target_transport = httpx.ASGITransport(app=create_relay_app(pair["target"]), raise_app_exceptions=False)
        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19002",
            service=restarted_source,
            allow_loopback_http_lab=True,
            transport=target_transport,
        ) as target_client:
            custody = await target_client.send_packet(recovered_packet)
        assert restarted_source.record_receipt(custody)["state"] == "remote_accepted"

        committed = pair["target"].recover_pending_inbox()
        assert len(committed) == 1
        source_transport = httpx.ASGITransport(app=create_relay_app(restarted_source), raise_app_exceptions=False)
        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19001",
            service=pair["target"],
            allow_loopback_http_lab=True,
            transport=source_transport,
        ) as source_client:
            result = await source_client.send_receipt(committed[0])
        assert result["state"] == "recipient_committed"
        assert restarted_source.recover_pending_outbox() == []
        assert pair["target"].mailbox.reconcile(pair["recipient"].harness_id)[0]["payload"] == {
            "message": "offline relay synthetic"
        }

        with pytest.raises(ValidationError, match="require HTTPS"):
            ServerAgentRelayClient(
                base_url="http://remote.example:19003",
                allow_loopback_http_lab=True,
            )
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


@pytest.mark.anyio
async def test_relay_transport_failures_open_breaker_and_half_open_success_recovers(
    tmp_path: Path,
) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)
        clock = [140_000]
        policy = OperationsPolicy(circuit_breaker_failure_threshold=2, circuit_breaker_reset_seconds=10)
        admission = QuotaService(
            pair["source_store"],
            policy=policy,
            safety_reserve_fraction=0,
            clock=lambda: clock[0],
        )
        pair["source"].admission = admission

        invalid_receipt = pair["target"]._receipt(packet, fact="accepted_local")

        async def unavailable(_request):
            return httpx.Response(
                200,
                json=invalid_receipt.model_dump(mode="json") | {"signature": "invalid"},
            )

        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19004",
            service=pair["source"],
            allow_loopback_http_lab=True,
            transport=httpx.MockTransport(unavailable),
        ) as client:
            for _ in range(2):
                with pytest.raises(AuthenticationError):
                    await client.send_packet(packet)
            with pytest.raises(GateBlocked, match="open"):
                await client.send_packet(packet)

        clock[0] += 11
        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19005",
            service=pair["source"],
            allow_loopback_http_lab=True,
            transport=httpx.ASGITransport(
                app=create_relay_app(pair["target"]),
                raise_app_exceptions=False,
            ),
        ) as client:
            receipt = await client.send_packet(packet)
        assert receipt.fact == "accepted_local"
        assert admission.content_free_status()["open_breakers"] == 0

        committed = pair["target"].deliver(packet.packet_id)
        target_admission = QuotaService(
            pair["target_store"],
            policy=policy,
            safety_reserve_fraction=0,
            clock=lambda: clock[0],
        )
        pair["target"].admission = target_admission

        async def receipt_unavailable(_request):
            return httpx.Response(503, json={"error": "unavailable"})

        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19006",
            service=pair["target"],
            allow_loopback_http_lab=True,
            transport=httpx.MockTransport(receipt_unavailable),
        ) as client:
            for _ in range(2):
                with pytest.raises(httpx.HTTPStatusError):
                    await client.send_receipt(committed)
            with pytest.raises(GateBlocked, match="open"):
                await client.send_receipt(committed)
        clock[0] += 11
        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19007",
            service=pair["target"],
            allow_loopback_http_lab=True,
            transport=httpx.ASGITransport(
                app=create_relay_app(pair["source"]),
                raise_app_exceptions=False,
            ),
        ) as client:
            assert (await client.send_receipt(committed))["state"] == "recipient_committed"
        assert target_admission.content_free_status()["open_breakers"] == 0
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


@pytest.mark.anyio
@pytest.mark.parametrize(
    ("malformation", "expected_error"),
    (
        ("missing", PydanticValidationError),
        ("wrong_packet", ValidationError),
        ("wrong_state", ValidationError),
        ("advanced_string", PydanticValidationError),
        ("extra", PydanticValidationError),
    ),
)
async def test_receipt_transport_rejects_unbound_acknowledgements_without_closing_breaker(
    tmp_path: Path,
    malformation: str,
    expected_error: type[Exception],
) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)
        pair["target"].accept(packet)
        committed = pair["target"].deliver(packet.packet_id)
        admission = QuotaService(
            pair["target_store"],
            policy=OperationsPolicy(),
            safety_reserve_fraction=0,
        )
        pair["target"].admission = admission
        honest = {
            "packet_id": committed.packet_id,
            "state": "recipient_committed",
            "advanced": True,
        }

        async def malformed_acknowledgement(_request):
            response = dict(honest)
            if malformation == "missing":
                response = {}
            elif malformation == "wrong_packet":
                response["packet_id"] = "different-packet-id-0001"
            elif malformation == "wrong_state":
                response["state"] = "remote_accepted"
            elif malformation == "advanced_string":
                response["advanced"] = "true"
            elif malformation == "extra":
                response["unexpected"] = "not-in-contract"
            return httpx.Response(202, json=response)

        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19008",
            service=pair["target"],
            allow_loopback_http_lab=True,
            transport=httpx.MockTransport(malformed_acknowledgement),
        ) as client:
            with pytest.raises(expected_error):
                await client.send_receipt(committed)

        breaker = pair["target_store"].fetch_one(
            "SELECT state,failure_count FROM circuit_breakers WHERE failure_count>0"
        )
        assert dict(breaker) == {"state": "closed", "failure_count": 1}

        async def honest_acknowledgement(_request):
            return httpx.Response(202, json=honest)

        async with ServerAgentRelayClient(
            base_url="http://127.0.0.1:19009",
            service=pair["target"],
            allow_loopback_http_lab=True,
            transport=httpx.MockTransport(honest_acknowledgement),
        ) as client:
            assert await client.send_receipt(committed) == honest
        assert admission.content_free_status()["open_breakers"] == 0
        breaker = pair["target_store"].fetch_one(
            "SELECT state,failure_count FROM circuit_breakers"
        )
        assert dict(breaker) == {"state": "closed", "failure_count": 0}
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_peer_mesh_relay_config_is_non_inert_exact_and_contains_no_inline_secrets() -> None:
    capabilities = frozenset(
        {
            ServerAgentCapability.OFFLINE_CUSTODY,
            ServerAgentCapability.ARTIFACT_STORAGE,
            ServerAgentCapability.RELAY,
            ServerAgentCapability.STORE_AND_FORWARD,
        }
    )
    with pytest.raises(PydanticValidationError, match="exact relay signing"):
        ExtensionConfig(
            features=FeatureFlags(peer_mesh=True),
            server_agent_capabilities=capabilities,
            component_evidence={"peer_mesh": "passed"},
        )
    key = P256KeyPair.generate()
    wrong = P256KeyPair.generate()
    with pytest.raises(PydanticValidationError, match="identifier must match"):
        RelayPeerConfig(
            domain_id="peer.example",
            relay_harness_id="peer-harness",
            signing_key_id=wrong.thumbprint,
            public_key_pem=key.public_pem,
            key_versions=(
                RelayPeerKeyConfig(
                    key_id=relay_peer_key_id(b"1" * 32),
                    key_epoch=1,
                    bilateral_key_path="relay/peer.key",
                    provisioned_state="active",
                    not_before=1,
                    expires_at=(1 << 62) - 1,
                ),
            ),
        )
    with pytest.raises(PydanticValidationError):
        RelaySigningIdentityConfig(
            harness_id="local-harness",
            credential_id="local-credential",
            private_key_path="-----BEGIN PRIVATE KEY-----",
        )
    now = int(time.time())
    first_key_id = relay_peer_key_id(b"1" * 32)
    second_key_id = relay_peer_key_id(b"2" * 32)
    versioned_peer = RelayPeerConfig(
        domain_id="versioned-peer.example",
        relay_harness_id="versioned-peer-harness",
        signing_key_id=key.thumbprint,
        public_key_pem=key.public_pem,
        key_versions=(
            RelayPeerKeyConfig(
                key_id=first_key_id,
                key_epoch=1,
                bilateral_key_path="relay/peer-v1.key",
                provisioned_state="active",
                not_before=1,
                expires_at=(1 << 62) - 1,
            ),
            RelayPeerKeyConfig(
                key_id=second_key_id,
                key_epoch=2,
                bilateral_key_path="relay/peer-v2.key",
                provisioned_state="pending",
                not_before=now + 60,
                expires_at=now + 7_200,
            ),
        ),
    )
    assert [item.key_epoch for item in versioned_peer.key_versions] == [1, 2]
    legacy_config = versioned_peer.model_dump(mode="python")
    legacy_config["bilateral_key_path"] = "relay/legacy.key"
    with pytest.raises(PydanticValidationError, match="bilateral_key_path|Extra inputs"):
        RelayPeerConfig.model_validate(legacy_config)
    legacy_runtime_peer = {
        "domain_id": "legacy-runtime-peer.example",
        "relay_harness_id": "legacy-runtime-peer-harness",
        "signing_key_id": key.thumbprint,
        "public_key_pem": key.public_pem,
        "key_versions": (
            RelayPeerKey(
                key_id=first_key_id,
                key_epoch=1,
                key=b"1" * 32,
                provisioned_state="active",
            ),
        ),
        "bilateral_encryption_key": b"1" * 32,
    }
    with pytest.raises(TypeError, match="bilateral_encryption_key"):
        ServerAgentPeer(**legacy_runtime_peer)
    with pytest.raises(PydanticValidationError, match="exactly one provisioned active"):
        RelayPeerConfig(
            domain_id="no-active-peer.example",
            relay_harness_id="no-active-peer-harness",
            signing_key_id=key.thumbprint,
            public_key_pem=key.public_pem,
            key_versions=(
                versioned_peer.key_versions[1],
            ),
        )
    relay = RelayServiceConfig(
        signing_identity=RelaySigningIdentityConfig(
            harness_id="local-harness",
            credential_id="local-credential",
            private_key_path="relay/signing.pem",
        ),
        peers=(
            versioned_peer,
        ),
    )
    with pytest.raises(PydanticValidationError, match="inert"):
        ExtensionConfig(relay=relay)
    exported = ExtensionConfig(
        features=FeatureFlags(peer_mesh=True),
        server_agent_capabilities=capabilities,
        component_evidence={"peer_mesh": "passed"},
        relay=relay,
    ).redacted_export()
    assert "bilateral_encryption_key" not in str(exported)
    assert "private_pem" not in str(exported)
    versioned_export = ExtensionConfig(
        features=FeatureFlags(peer_mesh=True),
        server_agent_capabilities=capabilities,
        component_evidence={"peer_mesh": "passed"},
        relay=relay.model_copy(update={"peers": (versioned_peer,)}),
    ).redacted_export()
    assert first_key_id in str(versioned_export)
    assert "11111111111111111111111111111111" not in str(versioned_export)


def test_relay_composition_rejects_non_owner_or_wrong_sized_key_files(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        with pytest.raises(GateBlocked, match="owner-only regular files"):
            composed_relay_core(tmp_path, pair, side="target", bilateral_key=b"x" * 31)
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


def test_cross_domain_attachment_is_held_until_target_quarantine_release(tmp_path: Path) -> None:
    binding = ReleasedArtifactBinding(
        artifact_id=str(uuid4()),
        domain_id="alpha.example",
        object_version="a" * 64,
        size=32,
        media_type="application/octet-stream",
        classification=Classification.C0_PUBLIC,
        release_intent_id=str(uuid4()),
        released_at=datetime.now(UTC),
    )
    pair = build_pair(tmp_path, released_artifacts=(binding,))
    try:
        packet = staged(pair)
        with pytest.raises(GateBlocked, match="target-domain quarantine"):
            pair["target"].accept(packet)
        assert pair["target_store"].fetch_one(
            "SELECT COUNT(*) AS count FROM server_agent_relay_inbox"
        )["count"] == 0
    finally:
        pair["source_store"].close()
        pair["target_store"].close()


@pytest.mark.anyio
async def test_main_extension_mounts_exact_one_hop_relay_packet_and_receipt_routes(tmp_path: Path) -> None:
    pair = build_pair(tmp_path)
    try:
        packet = staged(pair)
        source_core = composed_relay_core(tmp_path, pair, side="source")
        target_core = composed_relay_core(tmp_path, pair, side="target")
        with pytest.raises(AuthenticationError, match="pending|metadata"):
            composed_relay_core(
                tmp_path,
                pair,
                side="target",
                bilateral_key=b"w" * 32,
            )
        assert source_core.relay_service is not None
        assert target_core.relay_service is not None
        assert source_core.mailboxes.admission is source_core.quotas
        assert target_core.mailboxes.admission is target_core.quotas
        assert source_core.relay_service.admission is source_core.quotas
        assert target_core.relay_service.admission is target_core.quotas
        source_app = create_app(source_core)
        target_app = create_app(target_core)
        assert source_app.state.relay_service is source_core.relay_service
        assert target_app.state.relay_service is target_core.relay_service
        assert packet.hop_count == packet.max_hops == 1

        headers = {"Content-Type": "application/json"}
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=target_app, raise_app_exceptions=False),
            base_url="http://127.0.0.1",
        ) as target_client:
            peer_mismatch = packet.model_dump(mode="json") | {
                "source_relay_harness_id": "harness-not-the-pinned-peer"
            }
            denied_peer = await target_client.post(
                "/v1/server-agent-relay/packets",
                content=canonical_json(peer_mismatch),
                headers=headers,
            )
            assert denied_peer.status_code == 401

            stale = packet.model_dump(mode="json") | {"expires_at": packet.created_at}
            denied_ttl = await target_client.post(
                "/v1/server-agent-relay/packets",
                content=canonical_json(stale),
                headers=headers,
            )
            assert denied_ttl.status_code == 401

            excessive_hop = packet.model_dump(mode="json") | {"max_hops": 2}
            denied_hop = await target_client.post(
                "/v1/server-agent-relay/packets",
                content=canonical_json(excessive_hop),
                headers=headers,
            )
            assert denied_hop.status_code == 422

            accepted = await target_client.post(
                "/v1/server-agent-relay/packets",
                content=canonical_json(packet.model_dump(mode="json")),
                headers=headers,
            )
            assert accepted.status_code == 202
            assert accepted.json()["fact"] == "accepted_local"
            replay = await target_client.post(
                "/v1/server-agent-relay/packets",
                content=canonical_json(packet.model_dump(mode="json")),
                headers=headers,
            )
            assert replay.status_code == 202
            assert replay.json()["packet_id"] == accepted.json()["packet_id"]
            assert replay.json()["packet_digest"] == accepted.json()["packet_digest"]
            assert replay.json()["fact"] == accepted.json()["fact"]
            assert pair["target_store"].fetch_one(
                "SELECT COUNT(*) AS count FROM server_agent_relay_inbox WHERE packet_id=?",
                (packet.packet_id,),
            )["count"] == 1

        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=source_app, raise_app_exceptions=False),
            base_url="http://127.0.0.1",
        ) as source_client:
            recorded = await source_client.post(
                "/v1/server-agent-relay/receipts",
                content=canonical_json(accepted.json()),
                headers=headers,
            )
        assert recorded.status_code == 202
        assert recorded.json() == {
            "packet_id": packet.packet_id,
            "state": "remote_accepted",
            "advanced": True,
        }
    finally:
        pair["source_store"].close()
        pair["target_store"].close()
