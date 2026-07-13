"""Single-owner room model with from-join history and explicit membership."""

from __future__ import annotations

import json
import time
from datetime import UTC, datetime
from typing import Any
from uuid import uuid4

from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.credentials import (
    load_credential_binding,
    load_credential_binding_from_connection,
)
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import (
    ConfidentialityPolicy,
    RoomGovernancePolicy,
)
from agentnet.protocol.models import Classification
from agentnet.rooms.mls import MLSGroupBinding, MLSProvider, ValidatedMLSAdoption
from agentnet.security.signatures import canonical_json
from agentnet.storage.sqlite import SQLiteStore


class RoomService:
    def __init__(
        self,
        store: SQLiteStore,
        *,
        mls_provider: MLSProvider | None = None,
        mls_adoption: ValidatedMLSAdoption | None = None,
        governance_policy: RoomGovernancePolicy | None = None,
        confidentiality_policy: ConfidentialityPolicy | None = None,
        outage_gate: OutageGate | None = None,
    ) -> None:
        self.store = store
        self.mls_provider = mls_provider
        self.mls_adoption = mls_adoption
        self.governance_policy = governance_policy or RoomGovernancePolicy()
        self.confidentiality_policy = confidentiality_policy or ConfidentialityPolicy()
        self.outage_gate = outage_gate

    def _require_authenticated_actor(self, actor: VerifiedActor, *, connection: Any | None = None) -> None:
        if actor.credential_id is None or actor.harness_id is None:
            raise AuthorizationError("room control requires a credential-bound exact harness")
        binding = (
            load_credential_binding(self.store, actor.credential_id)
            if connection is None
            else load_credential_binding_from_connection(connection, actor.credential_id)
        )
        binding.require_active(now=int(time.time()))
        expected = (binding.domain_id, binding.harness_id, binding.credential_id, binding.credential_epoch)
        presented = (actor.domain_id, actor.harness_id, actor.credential_id, actor.credential_epoch)
        if presented != expected:
            raise AuthorizationError("room actor does not match the authenticated credential binding")

    def _require_sealed_provider(self) -> tuple[MLSProvider, ValidatedMLSAdoption]:
        if (
            self.mls_provider is None
            or self.mls_adoption is None
            or not isinstance(self.mls_adoption, ValidatedMLSAdoption)
        ):
            raise AuthorizationError("sealed rooms require a validated MLS adoption and live provider")
        self.mls_adoption.require_current(self.mls_provider)
        return self.mls_provider, self.mls_adoption

    @staticmethod
    def _require_group_binding(
        binding: MLSGroupBinding,
        *,
        provider: MLSProvider,
        room_id: str,
        expected_epoch: int | None = None,
    ) -> None:
        if (
            binding.provider_id != provider.provider_id
            or binding.provider_version != provider.provider_version
            or binding.room_id != room_id
            or (expected_epoch is not None and binding.epoch != expected_epoch)
        ):
            raise AuthorizationError("MLS provider returned a mismatched group binding")

    def create(
        self,
        *,
        actor: VerifiedActor,
        classification: Classification,
        persistent: bool,
        expires_at: datetime | None,
        policy: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        self._require_authenticated_actor(actor)
        if actor.positive_authority_id is None or actor.harness_id is None:
            raise AuthorizationError("room creation requires a verified human/guest plus harness")
        if not persistent and expires_at is None:
            raise ConflictError("temporary rooms require an expiry")
        room_id = str(uuid4())
        configured_history = self.governance_policy.history_mode
        room_policy = {
            "history_mode": "no_prior_history" if classification is Classification.C3_SEALED else configured_history,
            "persistent": persistent,
            "guest_prior_history": self.governance_policy.guest_prior_history,
            "fanout_budget": 100,
            "governance_threshold": self.governance_policy.governance_threshold,
            "governance_credential_ids": [actor.credential_id],
            "recovery_threshold": self.governance_policy.recovery_threshold,
            "recovery_credential_ids": [actor.credential_id],
        }
        if policy:
            unknown = set(policy) - set(room_policy)
            if unknown:
                raise ConflictError(f"unknown room policy fields: {sorted(unknown)}")
            room_policy.update(policy)
        if room_policy["guest_prior_history"] is not False:
            raise ConflictError("room policy cannot expose prior history to guests")
        if room_policy["history_mode"] not in {"from_join", "no_prior_history"}:
            raise ConflictError("room history mode is outside the configured profile")
        if configured_history == "no_prior_history" and room_policy["history_mode"] != "no_prior_history":
            raise ConflictError("room history mode weakens the configured policy")
        if classification is Classification.C3_SEALED and room_policy["history_mode"] != "no_prior_history":
            raise ConflictError("sealed rooms cannot expose prior history")
        for threshold_field, credentials_field in (
            ("governance_threshold", "governance_credential_ids"),
            ("recovery_threshold", "recovery_credential_ids"),
        ):
            credentials = room_policy[credentials_field]
            threshold = room_policy[threshold_field]
            if (
                not isinstance(credentials, list)
                or not credentials
                or len(credentials) != len(set(credentials))
                or not isinstance(threshold, int)
                or isinstance(threshold, bool)
                or threshold < 1
                or threshold > len(credentials)
            ):
                raise ConflictError(f"{threshold_field} must be satisfiable by unique predeclared credentials")
            configured_floor = (
                self.governance_policy.governance_threshold
                if threshold_field == "governance_threshold"
                else self.governance_policy.recovery_threshold
            )
            if threshold < configured_floor:
                raise ConflictError(f"{threshold_field} weakens the configured policy")

        mls_binding: MLSGroupBinding | None = None
        if classification is Classification.C3_SEALED:
            provider, adoption = self._require_sealed_provider()
            adoption.require_current(provider)
            mls_binding = provider.create_group(room_id, (actor.harness_id,))
            self._require_group_binding(mls_binding, provider=provider, room_id=room_id)
            room_policy.update(
                {
                    "mls_adoption_decision_id": adoption.record.decision_id,
                    "mls_evidence_digest": adoption.record.evidence_digest,
                    "mls_group_id": mls_binding.group_id,
                    "mls_provider_id": mls_binding.provider_id,
                    "mls_provider_version": mls_binding.provider_version,
                }
            )
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO rooms(
                    room_id,domain_id,owner_domain_id,owner_epoch,control_sequence,state,
                    classification,history_mode,expires_at,policy_json,application_epoch,
                    mls_epoch,file_key_epoch,mls_group_id,mls_provider_id
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    room_id,
                    actor.domain_id,
                    actor.domain_id,
                    1,
                    1,
                    "active",
                    classification.value,
                    room_policy["history_mode"],
                    int(expires_at.timestamp()) if expires_at else None,
                    canonical_json(room_policy).decode("utf-8"),
                    1,
                    mls_binding.epoch if mls_binding else 0,
                    1,
                    mls_binding.group_id if mls_binding else None,
                    mls_binding.provider_id if mls_binding else None,
                ),
            )
            connection.execute(
                "INSERT INTO room_members(room_id,harness_id,role,joined_sequence) VALUES(?,?,?,?)",
                (room_id, actor.harness_id, "owner_moderator", 1),
            )
            audit_hash = self.store.append_audit(
                connection,
                {"action": "room.create", "actor": actor.audit_view(), "classification": classification.value, "room_id": room_id},
            )
        return {
            "room_id": room_id,
            "control_sequence": 1,
            "state": "active",
            "mls_group_id": mls_binding.group_id if mls_binding else None,
            "mls_epoch": mls_binding.epoch if mls_binding else 0,
            "audit_hash": audit_hash,
        }

    def _require_active_moderator(self, connection: Any, room_id: str, harness_id: str) -> Any:
        room = connection.execute("SELECT * FROM rooms WHERE room_id=?", (room_id,)).fetchone()
        if room is None or room["state"] != "active":
            raise AuthorizationError("room is unavailable")
        membership = connection.execute(
            """SELECT * FROM room_members WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL
               ORDER BY joined_sequence DESC LIMIT 1""",
            (room_id, harness_id),
        ).fetchone()
        if membership is None or membership["role"] not in {"owner_moderator", "moderator"}:
            raise AuthorizationError("room control requires current moderator authority")
        return room

    def add_member(
        self,
        *,
        actor: VerifiedActor,
        room_id: str,
        harness_id: str,
        role: str = "member",
        mls_key_package: bytes | None = None,
    ) -> dict[str, Any]:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        self._require_authenticated_actor(actor)
        if actor.harness_id is None:
            raise AuthorizationError("room mutation requires an exact harness")
        if role not in {"member", "guest", "moderator"}:
            raise ConflictError("invalid room membership role")
        with self.store.transaction() as connection:
            room = self._require_active_moderator(connection, room_id, actor.harness_id)
            existing = connection.execute(
                "SELECT 1 FROM room_members WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL",
                (room_id, harness_id),
            ).fetchone()
            if existing:
                raise ConflictError("harness is already a current room member")
            next_mls_epoch = int(room["mls_epoch"])
            if room["classification"] == Classification.C3_SEALED.value:
                if not mls_key_package:
                    raise AuthorizationError("sealed-room membership requires an exact MLS KeyPackage")
                provider, _adoption = self._require_sealed_provider()
                binding = provider.add_member(room_id, harness_id, mls_key_package)
                next_mls_epoch += 1
                self._require_group_binding(
                    binding,
                    provider=provider,
                    room_id=room_id,
                    expected_epoch=next_mls_epoch,
                )
            sequence = room["control_sequence"] + 1
            cursor = connection.execute(
                "UPDATE rooms SET control_sequence=?,mls_epoch=? WHERE room_id=? AND control_sequence=? AND mls_epoch=?",
                (sequence, next_mls_epoch, room_id, room["control_sequence"], room["mls_epoch"]),
            )
            if cursor.rowcount != 1:
                raise ConflictError("room membership epoch changed concurrently")
            connection.execute(
                "INSERT INTO room_members(room_id,harness_id,role,joined_sequence) VALUES(?,?,?,?)",
                (room_id, harness_id, role, sequence),
            )
            audit_hash = self.store.append_audit(
                connection,
                {"action": "room.member_add", "actor": actor.audit_view(), "harness_id": harness_id, "role": role, "room_id": room_id, "sequence": sequence},
            )
        return {"room_id": room_id, "harness_id": harness_id, "control_sequence": sequence, "audit_hash": audit_hash}

    def remove_member(self, *, actor: VerifiedActor, room_id: str, harness_id: str) -> dict[str, Any]:
        self._require_authenticated_actor(actor)
        if actor.harness_id is None:
            raise AuthorizationError("room mutation requires an exact harness")
        with self.store.transaction() as connection:
            room = self._require_active_moderator(connection, room_id, actor.harness_id)
            membership = connection.execute(
                "SELECT * FROM room_members WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL",
                (room_id, harness_id),
            ).fetchone()
            if membership is None:
                raise AuthorizationError("room member is not visible")
            next_mls_epoch = int(room["mls_epoch"])
            if room["classification"] == Classification.C3_SEALED.value:
                provider, _adoption = self._require_sealed_provider()
                binding = provider.remove_member(room_id, harness_id)
                next_mls_epoch += 1
                self._require_group_binding(
                    binding,
                    provider=provider,
                    room_id=room_id,
                    expected_epoch=next_mls_epoch,
                )
            sequence = room["control_sequence"] + 1
            cursor = connection.execute(
                "UPDATE rooms SET control_sequence=?,mls_epoch=? WHERE room_id=? AND control_sequence=? AND mls_epoch=?",
                (sequence, next_mls_epoch, room_id, room["control_sequence"], room["mls_epoch"]),
            )
            if cursor.rowcount != 1:
                raise ConflictError("room membership epoch changed concurrently")
            connection.execute(
                "UPDATE room_members SET removed_sequence=? WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL",
                (sequence, room_id, harness_id),
            )
            audit_hash = self.store.append_audit(
                connection,
                {"action": "room.member_remove", "actor": actor.audit_view(), "harness_id": harness_id, "room_id": room_id, "sequence": sequence},
            )
        return {"room_id": room_id, "harness_id": harness_id, "control_sequence": sequence, "audit_hash": audit_hash}

    def authorize_send(
        self,
        *,
        actor: VerifiedActor,
        room_id: str,
        recipients: tuple[str, ...],
        classification: Classification,
        expected_control_sequence: int | None = None,
    ) -> dict[str, Any]:
        """Authorize one exact room fanout and return its epoch snapshot.

        The roster comparison and sender membership check share one database
        transaction, so callers cannot probe membership or race a removal by
        supplying a guessed recipient set.
        """

        with self.store.transaction(immediate=True) as connection:
            return self.authorize_send_in_transaction(
                connection,
                actor=actor,
                room_id=room_id,
                recipients=recipients,
                classification=classification,
                expected_control_sequence=expected_control_sequence,
            )

    def authorize_send_in_transaction(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        room_id: str,
        recipients: tuple[str, ...],
        classification: Classification,
        expected_control_sequence: int | None = None,
    ) -> dict[str, Any]:
        """Authorize under the transaction that also accepts the event."""

        if not recipients or len(set(recipients)) != len(recipients):
            raise AuthorizationError("room delivery authorization failed")
        self._require_authenticated_actor(actor, connection=connection)
        lock = " FOR UPDATE" if self.store.backend_name == "postgresql" else ""
        room = connection.execute(
            "SELECT * FROM rooms WHERE room_id=? AND domain_id=?" + lock,
            (room_id, actor.domain_id),
        ).fetchone()
        now = int(time.time())
        if (
            room is None
            or room["state"] != "active"
            or (room["expires_at"] is not None and int(room["expires_at"]) <= now)
            or (
                expected_control_sequence is not None
                and int(room["control_sequence"]) != expected_control_sequence
            )
        ):
            raise AuthorizationError("room delivery authorization failed")
        sender = connection.execute(
            """SELECT role,joined_sequence FROM room_members
                 WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL
                 ORDER BY joined_sequence DESC LIMIT 1""",
            (room_id, actor.harness_id),
        ).fetchone()
        if sender is None or sender["role"] not in {"owner_moderator", "moderator", "member", "guest"}:
            raise AuthorizationError("room delivery authorization failed")
        classification_rank = {
            Classification.C0_PUBLIC.value: 0,
            Classification.C1_INTERNAL.value: 1,
            Classification.C2_RESTRICTED.value: 2,
            Classification.C3_SEALED.value: 3,
        }
        if (
            classification_rank[classification.value] > classification_rank[room["classification"]]
            or (sender["role"] == "guest" and classification_rank[classification.value] > 1)
            or (
                classification is Classification.C3_SEALED
                and (int(room["mls_epoch"]) < 1 or room["mls_group_id"] is None)
            )
        ):
            raise AuthorizationError("room delivery authorization failed")
        member_rows = connection.execute(
            """SELECT rm.harness_id,h.domain_id,h.status,h.credential_epoch
                 FROM room_members rm
                 JOIN harnesses h ON h.harness_id=rm.harness_id
                WHERE rm.room_id=? AND rm.removed_sequence IS NULL
                ORDER BY rm.harness_id""",
            (room_id,),
        ).fetchall()
        current_recipients = tuple(row["harness_id"] for row in member_rows)
        if (
            tuple(sorted(recipients)) != current_recipients
            or any(row["domain_id"] != actor.domain_id or row["status"] != "active" for row in member_rows)
        ):
            raise AuthorizationError("room delivery authorization failed")
        return {
            "application_epoch": int(room["application_epoch"]),
            "classification": room["classification"],
            "control_sequence": int(room["control_sequence"]),
            "file_key_epoch": int(room["file_key_epoch"]),
            "mls_epoch": int(room["mls_epoch"]),
            "recipients": current_recipients,
            "sender_joined_sequence": int(sender["joined_sequence"]),
            "sender_role": sender["role"],
        }

    def recipients(self, *, actor: VerifiedActor, room_id: str) -> tuple[str, ...]:
        """Return the current roster only to a current room moderator."""

        with self.store.transaction(immediate=False) as connection:
            self._require_authenticated_actor(actor, connection=connection)
            self._require_active_moderator(connection, room_id, actor.harness_id or "")
            rows = connection.execute(
                "SELECT harness_id FROM room_members WHERE room_id=? AND removed_sequence IS NULL ORDER BY harness_id",
                (room_id,),
            ).fetchall()
            if not rows:
                raise AuthorizationError("room is unavailable")
            return tuple(row["harness_id"] for row in rows)

    def may_read_event(self, *, room_id: str, harness_id: str, event_control_sequence: int) -> bool:
        row = self.store.fetch_one(
            """SELECT * FROM room_members WHERE room_id=? AND harness_id=?
               AND joined_sequence<=? AND (removed_sequence IS NULL OR removed_sequence>?)
               ORDER BY joined_sequence DESC LIMIT 1""",
            (room_id, harness_id, event_control_sequence, event_control_sequence),
        )
        return row is not None

    def describe(self, *, actor: VerifiedActor, room_id: str) -> dict[str, Any]:
        """Describe a room without making it or hidden membership enumerable."""

        with self.store.transaction(immediate=False) as connection:
            self._require_authenticated_actor(actor, connection=connection)
            room = connection.execute(
                "SELECT * FROM rooms WHERE room_id=? AND domain_id=?",
                (room_id, actor.domain_id),
            ).fetchone()
            membership = connection.execute(
                """SELECT role,joined_sequence FROM room_members
                     WHERE room_id=? AND harness_id=? AND removed_sequence IS NULL
                     ORDER BY joined_sequence DESC LIMIT 1""",
                (room_id, actor.harness_id),
            ).fetchone()
            if room is None or membership is None:
                raise AuthorizationError("room is unavailable")
            members = connection.execute(
                "SELECT harness_id,role,joined_sequence,removed_sequence FROM room_members WHERE room_id=? ORDER BY joined_sequence",
                (room_id,),
            ).fetchall()
            result = dict(room) | {
                "policy": json.loads(room["policy_json"]),
                "member_count": sum(1 for member in members if member["removed_sequence"] is None),
                "self_membership": dict(membership),
            }
            if membership["role"] in {"owner_moderator", "moderator"}:
                result["members"] = [dict(member) for member in members]
            return result
