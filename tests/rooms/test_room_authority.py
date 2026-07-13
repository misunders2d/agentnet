from __future__ import annotations

import time
from uuid import uuid4

import pytest

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification
from agentnet.operations.policy_defaults import RoomGovernancePolicy
from agentnet.rooms.governance import (
    RecoveryTombstoneEvidence,
    RoomGovernance,
    RoomTransferSnapshot,
    SourceTransferProposal,
    TargetTransferAcceptance,
)
from agentnet.rooms.mls import (
    MLSAdoptionRecord,
    MLSGroupBinding,
    validate_mls_adoption,
)
from agentnet.rooms.service import RoomService
from agentnet.security.signatures import P256KeyPair, canonical_digest


def _digest(label: str) -> str:
    return canonical_digest({"label": label})


def _snapshot(room: dict[str, object], *, destination_key_id: str = "destination-key-1") -> RoomTransferSnapshot:
    return RoomTransferSnapshot(
        room_id=str(room["room_id"]),
        cutoff_control_sequence=int(room["control_sequence"]),
        cutoff_event_sequence=7,
        owner_epoch=1,
        application_epoch=1,
        mls_epoch=int(room.get("mls_epoch", 0)),
        file_key_epoch=1,
        state_root=_digest("state"),
        history_root=_digest("history"),
        recipient_rows_digest=_digest("recipients"),
        pending_effects_digest=_digest("effects"),
        artifact_manifests_digest=_digest("artifacts"),
        retention_legal_hold_digest=_digest("retention"),
        guest_roster_digest=_digest("guests"),
        audit_custody_digest=_digest("audit"),
        destination_key_id=destination_key_id,
        reconciliation_closed=True,
    )


def _proposal(owner: object, snapshot: RoomTransferSnapshot, now: int) -> SourceTransferProposal:
    return SourceTransferProposal(
        transfer_id=str(uuid4()),
        room_id=snapshot.room_id,
        source_domain_id=owner.domain_id,
        target_domain_id="partner.example",
        source_harness_id=owner.harness_id,
        source_credential_id=owner.credential_id,
        source_owner_epoch=snapshot.owner_epoch,
        cutoff_control_sequence=snapshot.cutoff_control_sequence,
        cutoff_event_sequence=snapshot.cutoff_event_sequence,
        application_epoch=snapshot.application_epoch,
        mls_epoch=snapshot.mls_epoch,
        file_key_epoch=snapshot.file_key_epoch,
        destination_key_id=snapshot.destination_key_id,
        snapshot_digest=snapshot.digest,
        issued_at=now - 1,
        expires_at=now + 300,
        nonce="source-transfer-nonce-with-enough-entropy-001",
    )


def _acceptance(target: object, proposal: SourceTransferProposal, now: int) -> TargetTransferAcceptance:
    return TargetTransferAcceptance(
        transfer_id=proposal.transfer_id,
        room_id=proposal.room_id,
        source_domain_id=proposal.source_domain_id,
        target_domain_id=proposal.target_domain_id,
        source_proposal_digest=proposal.digest,
        snapshot_digest=proposal.snapshot_digest,
        source_owner_epoch=proposal.source_owner_epoch,
        cutoff_control_sequence=proposal.cutoff_control_sequence,
        cutoff_event_sequence=proposal.cutoff_event_sequence,
        destination_owner_epoch=proposal.source_owner_epoch + 1,
        destination_application_epoch=proposal.application_epoch + 1,
        destination_mls_epoch=proposal.mls_epoch + (1 if proposal.mls_epoch else 0),
        destination_file_key_epoch=proposal.file_key_epoch + 1,
        destination_key_id=proposal.destination_key_id,
        target_harness_id=target.harness_id,
        target_credential_id=target.credential_id,
        objects_verified=True,
        reconciliation_closed=True,
        issued_at=now - 1,
        expires_at=now + 300,
        nonce="target-acceptance-nonce-with-enough-entropy-001",
    )


def test_room_from_join_membership_and_signed_fenced_transfer(store, identity_factory) -> None:
    owner, owner_key = identity_factory()
    member, _ = identity_factory()
    target, target_key = identity_factory(domain="partner.example")
    rooms = RoomService(store)
    created = rooms.create(actor=owner, classification=Classification.C1_INTERNAL, persistent=True, expires_at=None)
    creator_view = rooms.describe(actor=owner, room_id=created["room_id"])
    assert creator_view["self_membership"] == {
        "role": "owner_moderator",
        "joined_sequence": 1,
    }
    assert creator_view["member_count"] == 1
    assert rooms.may_read_event(
        room_id=created["room_id"],
        harness_id=owner.harness_id,
        event_control_sequence=1,
    )
    creator_send = rooms.authorize_send(
        actor=owner,
        room_id=created["room_id"],
        recipients=(owner.harness_id,),
        classification=Classification.C1_INTERNAL,
        expected_control_sequence=1,
    )
    assert creator_send["sender_role"] == "owner_moderator"
    assert not rooms.may_read_event(room_id=created["room_id"], harness_id=member.harness_id, event_control_sequence=1)
    joined = rooms.add_member(actor=owner, room_id=created["room_id"], harness_id=member.harness_id)
    assert rooms.may_read_event(
        room_id=created["room_id"], harness_id=member.harness_id, event_control_sequence=joined["control_sequence"]
    )

    snapshot = _snapshot(joined)
    now = int(time.time())
    proposal = _proposal(owner, snapshot, now)
    governance = RoomGovernance(store)
    transfer = governance.propose_transfer(
        actor=owner,
        proposal=proposal,
        snapshot=snapshot,
        signature=owner_key.sign("agentnet.room.control.v1", proposal.signed_fields()),
    )
    acceptance = _acceptance(target, proposal, now)
    governance.accept_target(
        actor=target,
        acceptance=acceptance,
        signature=target_key.sign("agentnet.room.control.v1", acceptance.signed_fields()),
    )
    committed = governance.commit(transfer["transfer_id"])
    assert committed["owner_epoch"] == 2
    described = rooms.describe(actor=owner, room_id=created["room_id"])
    assert described["owner_domain_id"] == "partner.example"
    assert described["application_epoch"] == 2
    assert described["file_key_epoch"] == 2
    with pytest.raises(ConflictError):
        governance.commit(transfer["transfer_id"])


def test_transfer_rejects_bad_source_and_mismatched_destination_signatures(store, identity_factory) -> None:
    owner, owner_key = identity_factory()
    attacker, attacker_key = identity_factory()
    target, target_key = identity_factory(domain="partner.example")
    created = RoomService(store).create(
        actor=owner, classification=Classification.C1_INTERNAL, persistent=True, expires_at=None
    )
    snapshot = _snapshot(created)
    now = int(time.time())
    proposal = _proposal(owner, snapshot, now)
    governance = RoomGovernance(store)
    with pytest.raises(AuthenticationError):
        governance.propose_transfer(
            actor=owner,
            proposal=proposal,
            snapshot=snapshot,
            signature=attacker_key.sign("agentnet.room.control.v1", proposal.signed_fields()),
        )
    assert RoomService(store).describe(actor=owner, room_id=created["room_id"])["state"] == "active"

    governance.propose_transfer(
        actor=owner,
        proposal=proposal,
        snapshot=snapshot,
        signature=owner_key.sign("agentnet.room.control.v1", proposal.signed_fields()),
    )
    acceptance = _acceptance(target, proposal, now)
    with pytest.raises(AuthenticationError):
        governance.accept_target(
            actor=target,
            acceptance=acceptance,
            signature=attacker_key.sign("agentnet.room.control.v1", acceptance.signed_fields()),
        )
    with pytest.raises(AuthenticationError):
        governance.accept_target(
            actor=target,
            acceptance=acceptance.model_copy(update={"destination_key_id": "tampered-key"}),
            signature=target_key.sign("agentnet.room.control.v1", acceptance.signed_fields()),
        )
    assert attacker.domain_id == owner.domain_id


def test_tombstone_requires_predeclared_signed_recovery_threshold(store, identity_factory) -> None:
    owner, owner_key = identity_factory()
    recovery, recovery_key = identity_factory()
    rooms = RoomService(store)
    created = rooms.create(
        actor=owner,
        classification=Classification.C1_INTERNAL,
        persistent=True,
        expires_at=None,
        policy={
            "recovery_threshold": 2,
            "recovery_credential_ids": [owner.credential_id, recovery.credential_id],
        },
    )
    now = int(time.time())
    evidence = RecoveryTombstoneEvidence(
        room_id=created["room_id"],
        owner_domain_id=owner.domain_id,
        owner_epoch=1,
        control_sequence=1,
        reason="permanent_owner_loss",
        issued_at=now - 1,
        expires_at=now + 300,
        nonce="recovery-threshold-nonce-with-enough-entropy-001",
    )
    governance = RoomGovernance(store)
    with pytest.raises(AuthorizationError):
        governance.tombstone_permanent_loss(
            evidence=evidence,
            signatures={owner.credential_id: owner_key.sign("agentnet.room.recovery.v1", evidence.signed_fields())},
        )
    result = governance.tombstone_permanent_loss(
        evidence=evidence,
        signatures={
            owner.credential_id: owner_key.sign("agentnet.room.recovery.v1", evidence.signed_fields()),
            recovery.credential_id: recovery_key.sign("agentnet.room.recovery.v1", evidence.signed_fields()),
        },
    )
    assert result["state"] == "tombstoned"
    with pytest.raises(TypeError):
        governance.tombstone_permanent_loss(  # type: ignore[call-arg]
            room_id=created["room_id"], authority_evidence="looks-valid"
        )


class _FakeMLSProvider:
    provider_id = "openmls-adapter"
    provider_version = "0.8.1"

    def __init__(self, store: object) -> None:
        self.store = store
        self.epochs: dict[str, int] = {}
        self.created_before_room_commit = False

    def healthy(self) -> bool:
        return True

    def _binding(self, room_id: str) -> MLSGroupBinding:
        return MLSGroupBinding(
            provider_id=self.provider_id,
            provider_version=self.provider_version,
            room_id=room_id,
            group_id=f"mls:{room_id}",
            epoch=self.epochs[room_id],
        )

    def create_group(self, room_id: str, members: tuple[str, ...]) -> MLSGroupBinding:
        assert members
        self.created_before_room_commit = self.store.fetch_one("SELECT 1 FROM rooms WHERE room_id=?", (room_id,)) is None
        self.epochs[room_id] = 1
        return self._binding(room_id)

    def add_member(self, room_id: str, member_id: str, key_package: bytes) -> MLSGroupBinding:
        assert member_id and key_package
        self.epochs[room_id] += 1
        return self._binding(room_id)

    def remove_member(self, room_id: str, member_id: str) -> MLSGroupBinding:
        assert member_id
        self.epochs[room_id] += 1
        return self._binding(room_id)


def _validated_adoption(provider: _FakeMLSProvider, now: int) -> object:
    owner_key = P256KeyPair.generate()
    fields = {
        "schema_version": "1.0",
        "provider_id": provider.provider_id,
        "provider_version": provider.provider_version,
        "decision_id": "owner-mls-decision-0001",
        "evidence_digest": _digest("mls-bakeoff"),
        "approved_by": "security-owner",
        "required_gates": ["G12", "G19", "PD-007"],
        "issued_at": now - 1,
        "expires_at": now + 600,
    }
    record = MLSAdoptionRecord(
        **fields,
        signature=owner_key.sign("agentnet.mls.adoption.v1", fields),
    )
    return validate_mls_adoption(record, owner_public_key_pem=owner_key.public_pem, provider=provider, now=now)


def test_sealed_room_requires_validated_adoption_and_live_provider_before_commit(store, identity_factory) -> None:
    owner, _ = identity_factory()
    member, _ = identity_factory()
    with pytest.raises(AuthorizationError):
        RoomService(store).create(
            actor=owner, classification=Classification.C3_SEALED, persistent=True, expires_at=None
        )
    provider = _FakeMLSProvider(store)
    with pytest.raises(AuthorizationError):
        RoomService(store, mls_provider=provider, mls_adoption="passed-the-gate").create(  # type: ignore[arg-type]
            actor=owner, classification=Classification.C3_SEALED, persistent=True, expires_at=None
        )
    now = int(time.time())
    adoption = _validated_adoption(provider, now)
    rooms = RoomService(store, mls_provider=provider, mls_adoption=adoption)
    created = rooms.create(actor=owner, classification=Classification.C3_SEALED, persistent=True, expires_at=None)
    assert provider.created_before_room_commit is True
    assert created["mls_group_id"] == f"mls:{created['room_id']}"
    assert rooms.describe(actor=owner, room_id=created["room_id"])["history_mode"] == "no_prior_history"
    with pytest.raises(AuthorizationError):
        rooms.add_member(actor=owner, room_id=created["room_id"], harness_id=member.harness_id)
    added = rooms.add_member(
        actor=owner,
        room_id=created["room_id"],
        harness_id=member.harness_id,
        mls_key_package=b"maintained-provider-key-package",
    )
    assert rooms.describe(actor=owner, room_id=created["room_id"])["mls_epoch"] == 2
    rooms.remove_member(actor=owner, room_id=created["room_id"], harness_id=member.harness_id)
    assert rooms.describe(actor=owner, room_id=created["room_id"])["mls_epoch"] == 3
    assert added["control_sequence"] == 2


def test_configured_room_policy_enforces_stricter_history_and_recovery_floor(store, identity_factory) -> None:
    owner, owner_key = identity_factory()
    strict_history = RoomGovernancePolicy(history_mode="no_prior_history")
    rooms = RoomService(store, governance_policy=strict_history)
    created = rooms.create(
        actor=owner,
        classification=Classification.C1_INTERNAL,
        persistent=True,
        expires_at=None,
    )
    assert rooms.describe(actor=owner, room_id=created["room_id"])["history_mode"] == "no_prior_history"
    with pytest.raises(ConflictError, match="weakens"):
        rooms.create(
            actor=owner,
            classification=Classification.C1_INTERNAL,
            persistent=True,
            expires_at=None,
            policy={"history_mode": "from_join"},
        )

    legacy = RoomService(store).create(
        actor=owner,
        classification=Classification.C1_INTERNAL,
        persistent=True,
        expires_at=None,
    )
    now = int(time.time())
    evidence = RecoveryTombstoneEvidence(
        room_id=legacy["room_id"],
        owner_domain_id=owner.domain_id,
        owner_epoch=1,
        control_sequence=1,
        reason="permanent_owner_loss",
        issued_at=now - 1,
        expires_at=now + 300,
        nonce="strict-recovery-policy-nonce-with-enough-entropy",
    )
    with pytest.raises(AuthorizationError, match="threshold"):
        RoomGovernance(
            store,
            policy=RoomGovernancePolicy(recovery_threshold=2),
        ).tombstone_permanent_loss(
            evidence=evidence,
            signatures={
                owner.credential_id: owner_key.sign(
                    "agentnet.room.recovery.v1", evidence.signed_fields()
                )
            },
        )


def test_room_delivery_requires_current_speaker_membership_and_exact_epoch_roster(
    store, identity_factory
) -> None:
    owner, _ = identity_factory()
    member, _ = identity_factory()
    never_member, _ = identity_factory()
    sibling, _ = identity_factory()
    sibling_actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id=owner.domain_id,
        principal_id=owner.principal_id,
        harness_id=sibling.harness_id,
        credential_id=sibling.credential_id,
        credential_epoch=sibling.credential_epoch,
        binding_assurance=sibling.binding_assurance,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET principal_id=? WHERE harness_id=?",
            (owner.principal_id, sibling.harness_id),
        )
    rooms = RoomService(store)
    created = rooms.create(
        actor=owner,
        classification=Classification.C2_RESTRICTED,
        persistent=True,
        expires_at=None,
    )
    joined = rooms.add_member(
        actor=owner, room_id=created["room_id"], harness_id=member.harness_id
    )
    recipients = tuple(sorted((owner.harness_id, member.harness_id)))
    snapshot = rooms.authorize_send(
        actor=member,
        room_id=created["room_id"],
        recipients=recipients,
        classification=Classification.C1_INTERNAL,
        expected_control_sequence=joined["control_sequence"],
    )
    assert snapshot["control_sequence"] == joined["control_sequence"]
    assert snapshot["sender_role"] == "member"

    for actor in (never_member, sibling_actor):
        with pytest.raises(AuthorizationError, match="authorization failed"):
            rooms.authorize_send(
                actor=actor,
                room_id=created["room_id"],
                recipients=recipients,
                classification=Classification.C1_INTERNAL,
            )
    for probed in ((owner.harness_id,), (*recipients, never_member.harness_id)):
        with pytest.raises(AuthorizationError, match="authorization failed"):
            rooms.authorize_send(
                actor=owner,
                room_id=created["room_id"],
                recipients=tuple(probed),
                classification=Classification.C1_INTERNAL,
            )
    with pytest.raises(AuthorizationError, match="authorization failed"):
        rooms.authorize_send(
            actor=owner,
            room_id=created["room_id"],
            recipients=recipients,
            classification=Classification.C1_INTERNAL,
            expected_control_sequence=1,
        )

    member_description = rooms.describe(actor=member, room_id=created["room_id"])
    assert member_description["member_count"] == 2
    assert "members" not in member_description
    assert len(rooms.describe(actor=owner, room_id=created["room_id"])["members"]) == 2

    rooms.remove_member(actor=owner, room_id=created["room_id"], harness_id=member.harness_id)
    with pytest.raises(AuthorizationError, match="authorization failed"):
        rooms.authorize_send(
            actor=member,
            room_id=created["room_id"],
            recipients=(owner.harness_id,),
            classification=Classification.C1_INTERNAL,
        )


@pytest.mark.parametrize("terminal_state", ["frozen", "transferring", "tombstoned"])
def test_room_delivery_denies_non_active_state_and_restricted_guest(
    store, identity_factory, terminal_state: str
) -> None:
    owner, _ = identity_factory()
    guest, _ = identity_factory()
    rooms = RoomService(store)
    created = rooms.create(
        actor=owner,
        classification=Classification.C2_RESTRICTED,
        persistent=True,
        expires_at=None,
    )
    rooms.add_member(
        actor=owner,
        room_id=created["room_id"],
        harness_id=guest.harness_id,
        role="guest",
    )
    recipients = tuple(sorted((owner.harness_id, guest.harness_id)))
    with pytest.raises(AuthorizationError, match="authorization failed"):
        rooms.authorize_send(
            actor=guest,
            room_id=created["room_id"],
            recipients=recipients,
            classification=Classification.C2_RESTRICTED,
        )
    rooms.authorize_send(
        actor=guest,
        room_id=created["room_id"],
        recipients=recipients,
        classification=Classification.C1_INTERNAL,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE rooms SET state=? WHERE room_id=?",
            (terminal_state, created["room_id"]),
        )
    with pytest.raises(AuthorizationError, match="authorization failed"):
        rooms.authorize_send(
            actor=owner,
            room_id=created["room_id"],
            recipients=recipients,
            classification=Classification.C1_INTERNAL,
        )


def test_room_delivery_rejects_revoked_current_recipient(store, identity_factory) -> None:
    owner, _ = identity_factory()
    member, _ = identity_factory()
    rooms = RoomService(store)
    created = rooms.create(
        actor=owner,
        classification=Classification.C1_INTERNAL,
        persistent=True,
        expires_at=None,
    )
    rooms.add_member(actor=owner, room_id=created["room_id"], harness_id=member.harness_id)
    with store.transaction() as connection:
        connection.execute("UPDATE harnesses SET status='revoked' WHERE harness_id=?", (member.harness_id,))
    with pytest.raises(AuthorizationError, match="authorization failed"):
        rooms.authorize_send(
            actor=owner,
            room_id=created["room_id"],
            recipients=tuple(sorted((owner.harness_id, member.harness_id))),
            classification=Classification.C1_INTERNAL,
        )
