"""Signed, fenced room transfer and threshold tombstone protocol."""

from __future__ import annotations

import json
import time
from typing import Any, Literal, Mapping

from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding
from agentnet.operations.policy_defaults import RoomGovernancePolicy
from agentnet.security.signatures import canonical_digest, canonical_json, verify_signature
from agentnet.storage.sqlite import SQLiteStore


Digest = str


class RoomTransferSnapshot(BaseModel):
    """Immutable state roots and epochs that the destination must reconcile."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    room_id: str = Field(min_length=1)
    cutoff_control_sequence: int = Field(ge=1)
    cutoff_event_sequence: int = Field(ge=0)
    owner_epoch: int = Field(ge=1)
    application_epoch: int = Field(ge=1)
    mls_epoch: int = Field(ge=0)
    file_key_epoch: int = Field(ge=1)
    state_root: str = Field(pattern=r"^[0-9a-f]{64}$")
    history_root: str = Field(pattern=r"^[0-9a-f]{64}$")
    recipient_rows_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    pending_effects_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    artifact_manifests_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    retention_legal_hold_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    guest_roster_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    audit_custody_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    destination_key_id: str = Field(min_length=1)
    reconciliation_closed: Literal[True]

    @property
    def digest(self) -> str:
        return canonical_digest(self.model_dump(mode="json"))


class SourceTransferProposal(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    transfer_id: str = Field(min_length=16, max_length=256)
    room_id: str = Field(min_length=1)
    source_domain_id: str = Field(min_length=1)
    target_domain_id: str = Field(min_length=1)
    source_harness_id: str = Field(min_length=1)
    source_credential_id: str = Field(min_length=1)
    source_owner_epoch: int = Field(ge=1)
    cutoff_control_sequence: int = Field(ge=1)
    cutoff_event_sequence: int = Field(ge=0)
    application_epoch: int = Field(ge=1)
    mls_epoch: int = Field(ge=0)
    file_key_epoch: int = Field(ge=1)
    destination_key_id: str = Field(min_length=1)
    snapshot_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class TargetTransferAcceptance(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    transfer_id: str = Field(min_length=16, max_length=256)
    room_id: str = Field(min_length=1)
    source_domain_id: str = Field(min_length=1)
    target_domain_id: str = Field(min_length=1)
    source_proposal_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    snapshot_digest: str = Field(pattern=r"^[0-9a-f]{64}$")
    source_owner_epoch: int = Field(ge=1)
    cutoff_control_sequence: int = Field(ge=1)
    cutoff_event_sequence: int = Field(ge=0)
    destination_owner_epoch: int = Field(ge=2)
    destination_application_epoch: int = Field(ge=2)
    destination_mls_epoch: int = Field(ge=0)
    destination_file_key_epoch: int = Field(ge=2)
    destination_key_id: str = Field(min_length=1)
    target_harness_id: str = Field(min_length=1)
    target_credential_id: str = Field(min_length=1)
    objects_verified: Literal[True]
    reconciliation_closed: Literal[True]
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")

    @property
    def digest(self) -> str:
        return canonical_digest(self.signed_fields())


class RecoveryTombstoneEvidence(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    schema_version: Literal["1.0"] = "1.0"
    room_id: str = Field(min_length=1)
    owner_domain_id: str = Field(min_length=1)
    owner_epoch: int = Field(ge=1)
    control_sequence: int = Field(ge=1)
    reason: Literal["permanent_owner_loss"]
    successor_room_id: str | None = None
    issued_at: int = Field(ge=1)
    expires_at: int = Field(ge=1)
    nonce: str = Field(min_length=24, max_length=256)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json")


class RoomGovernance:
    def __init__(
        self,
        store: SQLiteStore,
        *,
        policy: RoomGovernancePolicy | None = None,
        clock: Any = time.time,
    ) -> None:
        self.store = store
        self.policy = policy or RoomGovernancePolicy()
        self.clock = clock

    def _verify_credential_signature(
        self,
        *,
        credential_id: str,
        expected_domain_id: str,
        values: Mapping[str, Any],
        signature: str,
        purpose: str,
        now: int,
    ) -> None:
        binding = load_credential_binding(self.store, credential_id)
        binding.require_active(now=now)
        if binding.domain_id != expected_domain_id:
            raise AuthenticationError("room control signer belongs to another trust domain")
        verify_signature(binding.public_key_pem, purpose, values, signature)

    @staticmethod
    def _policy(room: Any) -> dict[str, Any]:
        value = json.loads(room["policy_json"])
        if not isinstance(value, dict):
            raise AuthorizationError("room governance policy is invalid")
        return value

    def propose_transfer(
        self,
        *,
        actor: VerifiedActor,
        proposal: SourceTransferProposal,
        snapshot: RoomTransferSnapshot,
        signature: str,
        additional_signatures: Mapping[str, str] | None = None,
    ) -> dict[str, object]:
        now = int(self.clock())
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS or actor.credential_id is None or actor.harness_id is None:
            raise AuthorizationError("source transfer requires an authenticated host human plus exact harness")
        if not (proposal.issued_at <= now < proposal.expires_at):
            raise AuthenticationError("source transfer proposal is outside its validity interval")
        if (
            proposal.source_harness_id != actor.harness_id
            or proposal.source_credential_id != actor.credential_id
            or proposal.source_domain_id != actor.domain_id
        ):
            raise AuthenticationError("source proposal signer binding mismatch")
        if proposal.source_domain_id == proposal.target_domain_id:
            raise ConflictError("room transfer target must be another owner domain")
        if proposal.snapshot_digest != snapshot.digest or proposal.room_id != snapshot.room_id:
            raise AuthenticationError("source proposal does not bind the exact snapshot")
        bound_snapshot = (
            proposal.cutoff_control_sequence,
            proposal.cutoff_event_sequence,
            proposal.source_owner_epoch,
            proposal.application_epoch,
            proposal.mls_epoch,
            proposal.file_key_epoch,
            proposal.destination_key_id,
        )
        actual_snapshot = (
            snapshot.cutoff_control_sequence,
            snapshot.cutoff_event_sequence,
            snapshot.owner_epoch,
            snapshot.application_epoch,
            snapshot.mls_epoch,
            snapshot.file_key_epoch,
            snapshot.destination_key_id,
        )
        if bound_snapshot != actual_snapshot:
            raise AuthenticationError("source proposal epoch/cutoff fields do not match the snapshot")

        signatures = {actor.credential_id: signature}
        for credential_id, co_signature in (additional_signatures or {}).items():
            if credential_id in signatures:
                raise ConflictError("duplicate source governance signer")
            signatures[credential_id] = co_signature

        with self.store.transaction() as connection:
            room = connection.execute("SELECT * FROM rooms WHERE room_id=?", (proposal.room_id,)).fetchone()
            if room is None or room["owner_domain_id"] != actor.domain_id or room["state"] != "active":
                raise AuthorizationError("source is not the active room owner")
            membership = connection.execute(
                """SELECT role FROM room_members WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL
                   ORDER BY joined_sequence DESC LIMIT 1""",
                (proposal.room_id, actor.harness_id),
            ).fetchone()
            if membership is None or membership["role"] not in {"owner_moderator", "moderator"}:
                raise AuthorizationError("source signer is not a current room moderator")
            room_epochs = (
                int(room["control_sequence"]),
                int(room["owner_epoch"]),
                int(room["application_epoch"]),
                int(room["mls_epoch"]),
                int(room["file_key_epoch"]),
            )
            proposal_epochs = (
                proposal.cutoff_control_sequence,
                proposal.source_owner_epoch,
                proposal.application_epoch,
                proposal.mls_epoch,
                proposal.file_key_epoch,
            )
            if room_epochs != proposal_epochs:
                raise ConflictError("source proposal was signed over a stale room epoch or cutoff")
            policy = self._policy(room)
            allowed_signers = set(policy.get("governance_credential_ids", ()))
            threshold = policy.get("governance_threshold")
            if (
                not isinstance(threshold, int)
                or threshold < self.policy.governance_threshold
                or not allowed_signers
            ):
                raise AuthorizationError("room lacks a valid predeclared governance threshold")
            if len(signatures) < threshold or not set(signatures).issubset(allowed_signers):
                raise AuthorizationError("source proposal lacks the predeclared governance threshold")
            for credential_id, source_signature in signatures.items():
                self._verify_credential_signature(
                    credential_id=credential_id,
                    expected_domain_id=proposal.source_domain_id,
                    values=proposal.signed_fields(),
                    signature=source_signature,
                    purpose="agentnet.room.control.v1",
                    now=now,
                )
            active = connection.execute(
                "SELECT 1 FROM room_transfers WHERE room_id=? AND state NOT IN ('committed','aborted')",
                (proposal.room_id,),
            ).fetchone()
            if active:
                raise ConflictError("room already has an active transfer")
            fenced = connection.execute(
                """UPDATE rooms SET state='frozen' WHERE room_id=? AND state='active'
                   AND owner_domain_id=? AND owner_epoch=? AND control_sequence=?""",
                (
                    proposal.room_id,
                    proposal.source_domain_id,
                    proposal.source_owner_epoch,
                    proposal.cutoff_control_sequence,
                ),
            )
            if fenced.rowcount != 1:
                raise ConflictError("room transfer lost the source fencing race")
            connection.execute(
                """INSERT INTO room_transfers(
                    transfer_id,room_id,source_domain_id,target_domain_id,cutoff_sequence,
                    cutoff_event_sequence,source_owner_epoch,application_epoch,mls_epoch,file_key_epoch,
                    snapshot_digest,source_proposal_json,source_signatures_json,state,created_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'frozen',?)""",
                (
                    proposal.transfer_id,
                    proposal.room_id,
                    proposal.source_domain_id,
                    proposal.target_domain_id,
                    proposal.cutoff_control_sequence,
                    proposal.cutoff_event_sequence,
                    proposal.source_owner_epoch,
                    proposal.application_epoch,
                    proposal.mls_epoch,
                    proposal.file_key_epoch,
                    snapshot.digest,
                    canonical_json(proposal.signed_fields()).decode("utf-8"),
                    canonical_json(dict(sorted(signatures.items()))).decode("utf-8"),
                    now,
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "room.transfer_frozen",
                    "cutoff_control_sequence": proposal.cutoff_control_sequence,
                    "proposal_digest": proposal.digest,
                    "room_id": proposal.room_id,
                    "snapshot_digest": snapshot.digest,
                    "target": proposal.target_domain_id,
                    "transfer_id": proposal.transfer_id,
                },
            )
        return {
            "transfer_id": proposal.transfer_id,
            "state": "frozen",
            "snapshot_digest": snapshot.digest,
            "proposal_digest": proposal.digest,
            "audit_hash": audit_hash,
        }

    def accept_target(
        self,
        *,
        actor: VerifiedActor,
        acceptance: TargetTransferAcceptance,
        signature: str,
    ) -> dict[str, object]:
        now = int(self.clock())
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS or actor.harness_id is None or actor.credential_id is None:
            raise AuthorizationError("destination acceptance requires an authenticated human plus exact harness")
        if not (acceptance.issued_at <= now < acceptance.expires_at):
            raise AuthenticationError("destination acceptance is outside its validity interval")
        if (
            acceptance.target_domain_id != actor.domain_id
            or acceptance.target_harness_id != actor.harness_id
            or acceptance.target_credential_id != actor.credential_id
        ):
            raise AuthenticationError("destination acceptance signer binding mismatch")
        self._verify_credential_signature(
            credential_id=actor.credential_id,
            expected_domain_id=acceptance.target_domain_id,
            values=acceptance.signed_fields(),
            signature=signature,
            purpose="agentnet.room.control.v1",
            now=now,
        )
        with self.store.transaction() as connection:
            transfer = connection.execute(
                "SELECT * FROM room_transfers WHERE transfer_id=?", (acceptance.transfer_id,)
            ).fetchone()
            if transfer is None or transfer["state"] != "frozen":
                raise ConflictError("room transfer is not awaiting target acceptance")
            source_proposal = SourceTransferProposal.model_validate_json(transfer["source_proposal_json"])
            expected = (
                transfer["room_id"],
                transfer["source_domain_id"],
                transfer["target_domain_id"],
                source_proposal.digest,
                transfer["snapshot_digest"],
                int(transfer["source_owner_epoch"]),
                int(transfer["cutoff_sequence"]),
                int(transfer["cutoff_event_sequence"]),
                int(transfer["source_owner_epoch"]) + 1,
                int(transfer["application_epoch"]) + 1,
                int(transfer["mls_epoch"]) + (1 if int(transfer["mls_epoch"]) else 0),
                int(transfer["file_key_epoch"]) + 1,
                source_proposal.destination_key_id,
            )
            presented = (
                acceptance.room_id,
                acceptance.source_domain_id,
                acceptance.target_domain_id,
                acceptance.source_proposal_digest,
                acceptance.snapshot_digest,
                acceptance.source_owner_epoch,
                acceptance.cutoff_control_sequence,
                acceptance.cutoff_event_sequence,
                acceptance.destination_owner_epoch,
                acceptance.destination_application_epoch,
                acceptance.destination_mls_epoch,
                acceptance.destination_file_key_epoch,
                acceptance.destination_key_id,
            )
            if presented != expected:
                raise AuthenticationError("destination acceptance does not bind the exact source snapshot/cutoff/epochs")
            cursor = connection.execute(
                """UPDATE room_transfers SET state='target_accepted',target_acceptance_digest=?,
                   target_acceptance_json=?,target_signature=?,target_credential_id=?
                   WHERE transfer_id=? AND state='frozen'""",
                (
                    acceptance.digest,
                    canonical_json(acceptance.signed_fields()).decode("utf-8"),
                    signature,
                    actor.credential_id,
                    acceptance.transfer_id,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("destination acceptance lost the fencing race")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "acceptance_digest": acceptance.digest,
                    "action": "room.transfer_target_accepted",
                    "target_credential_id": actor.credential_id,
                    "transfer_id": acceptance.transfer_id,
                },
            )
        return {
            "transfer_id": acceptance.transfer_id,
            "state": "target_accepted",
            "acceptance_digest": acceptance.digest,
            "audit_hash": audit_hash,
        }

    def commit(self, transfer_id: str) -> dict[str, object]:
        now = int(self.clock())
        with self.store.transaction() as connection:
            transfer = connection.execute("SELECT * FROM room_transfers WHERE transfer_id=?", (transfer_id,)).fetchone()
            if transfer is None or transfer["state"] != "target_accepted":
                raise ConflictError("room transfer lacks target acceptance")
            proposal = SourceTransferProposal.model_validate_json(transfer["source_proposal_json"])
            acceptance = TargetTransferAcceptance.model_validate_json(transfer["target_acceptance_json"])
            if now >= proposal.expires_at or now >= acceptance.expires_at:
                raise AuthenticationError("room transfer evidence expired before commit")
            signatures = json.loads(transfer["source_signatures_json"])
            for credential_id, signature in signatures.items():
                self._verify_credential_signature(
                    credential_id=credential_id,
                    expected_domain_id=proposal.source_domain_id,
                    values=proposal.signed_fields(),
                    signature=signature,
                    purpose="agentnet.room.control.v1",
                    now=now,
                )
            self._verify_credential_signature(
                credential_id=acceptance.target_credential_id,
                expected_domain_id=acceptance.target_domain_id,
                values=acceptance.signed_fields(),
                signature=transfer["target_signature"],
                purpose="agentnet.room.control.v1",
                now=now,
            )
            room = connection.execute("SELECT * FROM rooms WHERE room_id=?", (transfer["room_id"],)).fetchone()
            if room is None:
                raise ConflictError("room disappeared during transfer")
            room_policy = self._policy(room)
            room_policy.update(
                {
                    "destination_key_id": acceptance.destination_key_id,
                    "previous_owner_domain_id": proposal.source_domain_id,
                    "transfer_id": transfer_id,
                }
            )
            cursor = connection.execute(
                """UPDATE rooms SET owner_domain_id=?,owner_epoch=?,state='active',control_sequence=?,
                   application_epoch=?,mls_epoch=?,file_key_epoch=?,policy_json=?
                   WHERE room_id=? AND state='frozen' AND owner_domain_id=? AND owner_epoch=?
                   AND control_sequence=? AND application_epoch=? AND mls_epoch=? AND file_key_epoch=?""",
                (
                    acceptance.target_domain_id,
                    acceptance.destination_owner_epoch,
                    acceptance.cutoff_control_sequence + 1,
                    acceptance.destination_application_epoch,
                    acceptance.destination_mls_epoch,
                    acceptance.destination_file_key_epoch,
                    canonical_json(room_policy).decode("utf-8"),
                    acceptance.room_id,
                    proposal.source_domain_id,
                    proposal.source_owner_epoch,
                    proposal.cutoff_control_sequence,
                    proposal.application_epoch,
                    proposal.mls_epoch,
                    proposal.file_key_epoch,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("room owner epoch changed during fenced commit")
            committed = connection.execute(
                "UPDATE room_transfers SET state='committed',committed_at=? WHERE transfer_id=? AND state='target_accepted'",
                (now, transfer_id),
            )
            if committed.rowcount != 1:
                raise ConflictError("room transfer commit lost its compare-and-swap fence")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "acceptance_digest": acceptance.digest,
                    "action": "room.transfer_committed",
                    "new_owner": acceptance.target_domain_id,
                    "owner_epoch": acceptance.destination_owner_epoch,
                    "proposal_digest": proposal.digest,
                    "room_id": acceptance.room_id,
                    "transfer_id": transfer_id,
                },
            )
        return {
            "transfer_id": transfer_id,
            "state": "committed",
            "owner_epoch": acceptance.destination_owner_epoch,
            "audit_hash": audit_hash,
        }

    def tombstone_permanent_loss(
        self,
        *,
        evidence: RecoveryTombstoneEvidence,
        signatures: Mapping[str, str],
    ) -> dict[str, object]:
        now = int(self.clock())
        if not (evidence.issued_at <= now < evidence.expires_at):
            raise AuthenticationError("recovery evidence is outside its validity interval")
        if len(signatures) != len(set(signatures)):
            raise ConflictError("duplicate recovery signer")
        with self.store.transaction() as connection:
            room = connection.execute("SELECT * FROM rooms WHERE room_id=?", (evidence.room_id,)).fetchone()
            if room is None:
                raise AuthorizationError("room is unavailable")
            if (
                room["owner_domain_id"] != evidence.owner_domain_id
                or int(room["owner_epoch"]) != evidence.owner_epoch
                or int(room["control_sequence"]) != evidence.control_sequence
                or room["state"] not in {"active", "frozen"}
            ):
                raise ConflictError("recovery evidence was signed over stale room authority")
            policy = self._policy(room)
            allowed_signers = set(policy.get("recovery_credential_ids", ()))
            threshold = policy.get("recovery_threshold")
            if (
                not isinstance(threshold, int)
                or threshold < self.policy.recovery_threshold
                or len(signatures) < threshold
                or not set(signatures).issubset(allowed_signers)
            ):
                raise AuthorizationError("tombstone lacks the predeclared recovery threshold")
            for credential_id, signature in signatures.items():
                self._verify_credential_signature(
                    credential_id=credential_id,
                    expected_domain_id=evidence.owner_domain_id,
                    values=evidence.signed_fields(),
                    signature=signature,
                    purpose="agentnet.room.recovery.v1",
                    now=now,
                )
            cursor = connection.execute(
                """UPDATE rooms SET state='tombstoned',control_sequence=control_sequence+1
                   WHERE room_id=? AND owner_domain_id=? AND owner_epoch=? AND control_sequence=? AND state=?""",
                (
                    evidence.room_id,
                    evidence.owner_domain_id,
                    evidence.owner_epoch,
                    evidence.control_sequence,
                    room["state"],
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("room tombstone lost the compare-and-swap fence")
            evidence_digest = canonical_digest(evidence.signed_fields())
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "room.tombstoned",
                    "evidence_digest": evidence_digest,
                    "recovery_signer_count": len(signatures),
                    "room_id": evidence.room_id,
                    "successor_room_id": evidence.successor_room_id,
                },
            )
        return {
            "room_id": evidence.room_id,
            "state": "tombstoned",
            "evidence_digest": evidence_digest,
            "audit_hash": audit_hash,
        }
