"""Atomic authorization, grant-consumption, and exact-effect reservation."""

from __future__ import annotations

import json
import time
from collections.abc import Callable
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field

from agentnet.authorization.grants import GrantUse
from agentnet.authorization.evidence import (
    IssuanceAuthority,
    require_current_authority_decision,
)
from agentnet.authorization.policy import (
    AuthorizationRequest,
    OperationClass,
    PolicyEngine,
    validate_actor_state,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ReplayError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.workload import WorkloadRegistry
from agentnet.operations.quotas import QuotaService
from agentnet.protocol.models import Classification, EventType, TaskGrant
from agentnet.provenance import (
    ProvenanceObjectType,
    ProvenanceReferenceV1,
    ProvenanceService,
    TransformationKind,
    TransformationStep,
)
from agentnet.security.signatures import (
    P256KeyPair,
    canonical_digest,
    canonical_json,
    verify_signature,
)
from agentnet.storage.backend import StoreBackend


PhaseHook = Callable[[str], None]


class EffectState(StrEnum):
    PREPARED = "effect_prepared"
    EXECUTING = "effect_executing"
    SUCCEEDED = "effect_succeeded"
    FAILED = "effect_failed"
    CANCELLED = "effect_cancelled"
    UNKNOWN = "effect_unknown"


TERMINAL_EFFECT_STATES = frozenset(
    {EffectState.SUCCEEDED, EffectState.FAILED, EffectState.CANCELLED}
)


class EffectExecutionEvidence(BaseModel):
    """Typed dispatch evidence authenticated by the registered executor."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    attempt_id: str = Field(min_length=16, max_length=256)
    executor_instance_id: str = Field(min_length=16, max_length=256)
    request_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    dispatched_at: int = Field(gt=0)


class EffectTerminalEvidence(BaseModel):
    """Typed external-system acknowledgement for a known terminal result."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    attempt_id: str = Field(min_length=16, max_length=256)
    external_receipt_id: str = Field(min_length=8, max_length=512)
    external_receipt_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    observed_at: int = Field(gt=0)


class EffectUncertaintyEvidence(BaseModel):
    """Typed observation that execution may have committed without an acknowledgement."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    attempt_id: str = Field(min_length=16, max_length=256)
    reason: Literal["timeout", "commit_response_lost", "transport_disconnect"]
    observation_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    observed_at: int = Field(gt=0)


class EffectReconciliationEvidence(BaseModel):
    """Typed authoritative read-back used to resolve an unknown commit outcome."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    attempt_id: str = Field(min_length=16, max_length=256)
    authority_system_id: str = Field(min_length=3, max_length=256)
    query_id: str = Field(min_length=8, max_length=512)
    query_response_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    observed_at: int = Field(gt=0)
    terminal_state: Literal[
        EffectState.SUCCEEDED,
        EffectState.FAILED,
        EffectState.CANCELLED,
    ]


class EffectTransitionProof(BaseModel):
    """One exact, fresh, signed workload transition request."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    registration_id: str = Field(min_length=16, max_length=128)
    workload_id: str = Field(min_length=1, max_length=256)
    workload_role: str = Field(min_length=1, max_length=128)
    process_id: int = Field(gt=0)
    process_start_time: int = Field(gt=0)
    session_id: str = Field(min_length=16, max_length=256)
    credential_epoch: int = Field(gt=0)
    revocation_epoch: int = Field(gt=0)
    parent_event_id: str = Field(min_length=1)
    task_grant_id: str = Field(min_length=1)
    effect_id: str = Field(min_length=1)
    fence: int = Field(gt=0)
    from_state: EffectState
    to_state: EffectState
    evidence_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    timestamp: int = Field(gt=0)
    nonce: str = Field(min_length=24, max_length=256)
    signature: str = Field(min_length=1, max_length=2048)

    def signed_fields(self) -> dict[str, Any]:
        return self.model_dump(mode="json", exclude={"signature"})

    @classmethod
    def create(
        cls,
        signer: P256KeyPair,
        *,
        actor: VerifiedActor,
        effect_id: str,
        fence: int,
        from_state: EffectState,
        to_state: EffectState,
        evidence: BaseModel,
        timestamp: int | None = None,
        nonce: str | None = None,
    ) -> "EffectTransitionProof":
        if (
            actor.kind is not ActorKind.WORKLOAD
            or actor.binding_assurance != "workload_mtls"
            or actor.workload_role not in {"effect_authority", "effect_reconciler"}
            or actor.parent_event_id is None
            or actor.task_grant_id is None
        ):
            raise AuthenticationError(
                "effect transition proof requires an event-bound effect authority or reconciler"
            )
        fields = {
            "registration_id": actor.workload_registration_id,
            "workload_id": actor.workload_id,
            "workload_role": actor.workload_role,
            "process_id": actor.workload_process_id,
            "process_start_time": actor.workload_process_start_time,
            "session_id": actor.workload_session_id,
            "credential_epoch": actor.credential_epoch,
            "revocation_epoch": actor.workload_revocation_epoch,
            "parent_event_id": actor.parent_event_id,
            "task_grant_id": actor.task_grant_id,
            "effect_id": effect_id,
            "fence": fence,
            "from_state": from_state.value,
            "to_state": to_state.value,
            "evidence_digest": canonical_digest(evidence.model_dump(mode="json")),
            "timestamp": int(time.time()) if timestamp is None else timestamp,
            "nonce": nonce or f"effect-proof-{uuid4()}-{uuid4()}",
        }
        return cls(**fields, signature=signer.sign("agentnet.effect.transition.v1", fields))


class EffectReservations:
    def __init__(
        self,
        store: StoreBackend,
        *,
        admission: QuotaService | None = None,
        provenance: ProvenanceService | None = None,
    ) -> None:
        self.store = store
        self.admission = admission
        self.provenance = provenance or ProvenanceService(store)
        if self.provenance.store is not store:
            raise ValueError("effect and provenance services must share one transactional store")

    def _parent_event_provenance(self, connection: Any, event_id: str):
        event = connection.execute(
            """SELECT event_id,domain_id,event_type,classification,payload_digest,policy_revision
                 FROM events WHERE event_id=?""",
            (event_id,),
        ).fetchone()
        link = connection.execute(
            "SELECT * FROM event_provenance WHERE event_id=?",
            (event_id,),
        ).fetchone()
        if event is None or link is None:
            raise AuthorizationError("effect parent lacks mandatory event provenance")
        try:
            raw_reference = str(link["reference_json"])
            reference = ProvenanceReferenceV1.model_validate_json(
                raw_reference,
                strict=True,
            )
            expected_type = (
                ProvenanceObjectType.TASK
                if EventType(str(event["event_type"])) is EventType.TASK_ASSIGNMENT
                else ProvenanceObjectType.EVENT
            )
            if (
                str(link["provenance_digest"]) != reference.provenance_digest
                or str(link["object_type"]) != expected_type.value
                or canonical_json(reference.model_dump(mode="json")).decode("utf-8")
                != raw_reference
            ):
                raise ValueError("event provenance link fields disagree")
        except Exception as exc:
            raise ConflictError("effect parent provenance link is invalid") from exc
        return self.provenance.require_reference_in_transaction(
            connection,
            reference,
            expected_domain_id=str(event["domain_id"]),
            expected_content_digest=str(event["payload_digest"]),
            expected_object_type=expected_type,
            expected_classification=Classification(str(event["classification"])),
            required_sinks=(),
            expected_policy_revision=int(event["policy_revision"]),
        )

    def _record_tool_output_provenance(
        self,
        connection: Any,
        *,
        row: Any,
        actor: VerifiedActor,
        output_digest: str,
        completed_at: int,
        recorded_at: int,
        implementation_role: str,
    ):
        parent = self._parent_event_provenance(connection, str(row["event_id"]))
        step = TransformationStep(
            kind=TransformationKind.TOOL,
            operation_id=f"effect:{canonical_digest({'effect_id': row['effect_id'], 'attempt_id': row['attempt_id']})}",
            implementation_id=f"{implementation_role}:{actor.workload_registration_id}",
            implementation_version=f"credential-epoch-{actor.credential_epoch}",
            executor_harness_id=str(actor.workload_id),
            input_digests=(parent.content_digest,),
            output_digest=output_digest,
            started_at=datetime.fromtimestamp(int(row["execution_started_at"]), UTC),
            completed_at=datetime.fromtimestamp(completed_at, UTC),
        )
        return self.provenance.record_tainted_derivation_in_transaction(
            connection,
            object_type=ProvenanceObjectType.TOOL_OUTPUT,
            object_id=f"effect-output:{row['effect_id']}",
            domain_id=parent.domain_id,
            expected_previous_version=0,
            parent_provenance_digests=(parent.provenance_digest,),
            transformations=(step,),
            output_digest=output_digest,
            classification=parent.classification,
            allowed_sinks=(),
            policy_revision=parent.policy_revision,
            recorded_at=datetime.fromtimestamp(recorded_at, UTC),
            when=datetime.fromtimestamp(recorded_at, UTC),
        )

    def _require_tool_output_provenance(
        self,
        connection: Any,
        *,
        row: Any,
        output_digest: str,
    ):
        parent = self._parent_event_provenance(connection, str(row["event_id"]))
        stored = connection.execute(
            """SELECT * FROM content_provenance
                 WHERE object_type='tool_output' AND object_id=? AND version=1""",
            (f"effect-output:{row['effect_id']}",),
        ).fetchone()
        if stored is None:
            raise ConflictError("terminal effect lacks mandatory tool-output provenance")
        record = ProvenanceService._record_from_row(stored)
        if (
            record.parent_digests.digests != (parent.provenance_digest,)
            or not record.transformations.steps
            or record.transformations.steps[-1].kind is not TransformationKind.TOOL
        ):
            raise ConflictError("terminal effect provenance lineage is invalid")
        return self.provenance.require_reference_in_transaction(
            connection,
            record.reference(),
            expected_domain_id=parent.domain_id,
            expected_content_digest=output_digest,
            expected_object_type=ProvenanceObjectType.TOOL_OUTPUT,
            expected_classification=parent.classification,
            required_sinks=(),
            expected_policy_revision=parent.policy_revision,
        )

    @staticmethod
    def _verify_parent_event(
        connection,
        *,
        actor: VerifiedActor,
        event_id: str,
        grant_use: GrantUse,
        now: int,
    ) -> int:
        event = connection.execute(
            """SELECT domain_id,actor_json,classification,envelope_json,effect_deadline,policy_revision
                 FROM events WHERE event_id=?""",
            (event_id,),
        ).fetchone()
        if event is None or event["domain_id"] != actor.domain_id:
            raise AuthorizationError("effect parent event is absent from the actor domain")
        envelope = json.loads(event["envelope_json"])
        actor_json = canonical_json(actor.audit_view()).decode("utf-8")
        actor_is_origin = event["actor_json"] == actor_json
        actor_is_recipient = actor.harness_id is not None and actor.harness_id in envelope.get("recipients", [])
        if not actor_is_origin and not actor_is_recipient:
            raise AuthorizationError("effect actor is not causally bound to the parent event")
        if event["classification"] != grant_use.data_class.value:
            raise AuthorizationError("effect data class does not match the parent event")
        if event["effect_deadline"] is not None and int(event["effect_deadline"]) <= now:
            raise AuthorizationError("effect parent deadline has expired")
        return int(event["policy_revision"])

    @staticmethod
    def _verify_grant_shape(connection, *, actor: VerifiedActor, grant_use: GrantUse, now: int) -> None:
        row = connection.execute("SELECT * FROM task_grants WHERE grant_id=?", (grant_use.grant_id,)).fetchone()
        if row is None or row["revoked_at"] is not None or int(row["expires_at"]) <= now:
            raise AuthorizationError("task grant is absent, revoked, or expired")
        try:
            grant = TaskGrant.model_validate(json.loads(row["grant_json"]))
        except Exception as exc:
            raise AuthorizationError("task grant state is invalid") from exc
        if (
            actor.positive_authority_id != grant.principal_id
            or actor.harness_id != grant.harness_id
            or actor.domain_id != grant.domain_id
            or grant_use.action not in grant.actions
            or grant_use.resource not in grant.resources
            or grant_use.input_source not in grant.input_sources
            or grant_use.output_sink not in grant.output_sinks
            or grant_use.data_class not in grant.data_classes
        ):
            raise AuthorizationError("effect actor or dimension is outside the exact task grant")

    @staticmethod
    def _verify_duplicate(
        connection,
        row,
        *,
        actor: VerifiedActor,
        grant_use: GrantUse,
        now: int,
    ) -> None:
        exact = {
            "actor_json": canonical_json(actor.audit_view()).decode("utf-8"),
            "grant_id": grant_use.grant_id,
            "action": grant_use.action,
            "resource": grant_use.resource,
            "input_source": grant_use.input_source,
            "sink": grant_use.output_sink,
            "data_class": grant_use.data_class.value,
        }
        if any(row[key] != value for key, value in exact.items()):
            raise ConflictError("effect idempotency tuple names different authority or dimensions")
        domain = connection.execute(
            "SELECT policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        if domain is None:
            raise AuthorizationError("effect actor domain is absent")
        denial, _revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(domain["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None:
            raise AuthorizationError("effect duplicate caller is no longer current")

    def reserve(
        self,
        *,
        policy: PolicyEngine,
        actor: VerifiedActor,
        event_id: str,
        grant_use: GrantUse,
        request: dict[str, object],
        when: datetime | None = None,
        phase_hook: PhaseHook | None = None,
    ) -> dict[str, object]:
        """Commit allow decision, one-use grant, audit, and reservation together.

        ``phase_hook`` is a deterministic crash-injection seam.  Raising from
        any phase rolls back every mutation in this method.
        """

        if policy.store is not self.store:
            raise ValueError("effect policy and reservation must share one transactional store")
        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        digest = canonical_digest(request)
        exact_context = {
            "event_id": event_id,
            "output_sink": grant_use.output_sink,
            "input_source": grant_use.input_source,
            "data_class": grant_use.data_class.value,
            "request_digest": digest,
        }
        denied_reason: str | None = None
        result: dict[str, object] | None = None

        with self.store.transaction() as connection:
            existing = connection.execute(
                "SELECT * FROM effect_reservations WHERE event_id=? AND sink=? AND request_digest=?",
                (event_id, grant_use.output_sink, digest),
            ).fetchone()
            if existing is not None:
                self._verify_duplicate(connection, existing, actor=actor, grant_use=grant_use, now=now)
                result = dict(existing) | {"duplicate": True}
            else:
                event_policy_revision = self._verify_parent_event(
                    connection,
                    actor=actor,
                    event_id=event_id,
                    grant_use=grant_use,
                    now=now,
                )
                self._verify_grant_shape(connection, actor=actor, grant_use=grant_use, now=now)
                authorization = AuthorizationRequest(
                    actor=actor,
                    action=grant_use.action,
                    resource=grant_use.resource,
                    operation_class=OperationClass.PROTECTED_EFFECT,
                    policy_revision=event_policy_revision,
                    context=exact_context,
                    grant_use=grant_use,
                )
                decision = policy._decide_in_transaction(
                    connection,
                    authorization,
                    when=when,
                    phase_hook=phase_hook,
                )
                if not decision.allowed:
                    denied_reason = decision.reason
                else:
                    effect_id = str(uuid4())
                    if self.admission is not None:
                        self.admission._admit_operation_in_transaction(
                            connection,
                            actor_scope=actor.harness_id or actor.positive_authority_id or "unattributed",
                            domain_scope=actor.domain_id,
                            operation="effect_reserve",
                            operation_id=f"{event_id}:{grant_use.output_sink}:{digest}",
                            cost=1,
                        )
                        self.admission._reserve_work_in_transaction(
                            connection,
                            work_kind="protected_effect",
                            source_id=effect_id,
                            domain_id=actor.domain_id,
                            now=now,
                        )
                    fence = now * 1_000_000 + (int(effect_id.replace("-", "")[:8], 16) % 1_000_000)
                    actor_json = canonical_json(actor.audit_view()).decode("utf-8")
                    connection.execute(
                        """INSERT INTO effect_reservations(
                            effect_id,event_id,grant_id,actor_json,action,resource,input_source,sink,
                            data_class,request_digest,policy_decision_id,state,fence,created_at,updated_at
                        ) VALUES(?,?,?,?,?,?,?,?,?,?,?,'effect_prepared',?,?,?)""",
                        (
                            effect_id,
                            event_id,
                            grant_use.grant_id,
                            actor_json,
                            grant_use.action,
                            grant_use.resource,
                            grant_use.input_source,
                            grant_use.output_sink,
                            grant_use.data_class.value,
                            digest,
                            decision.decision_id,
                            fence,
                            now,
                            now,
                        ),
                    )
                    if phase_hook is not None:
                        phase_hook("after_reservation_inserted")
                    audit_hash = self.store.append_audit(
                        connection,
                        {
                            "action": "effect.prepared",
                            "actor": actor.audit_view(),
                            "effect_id": effect_id,
                            "event_id": event_id,
                            "grant_id": grant_use.grant_id,
                            "policy_decision_id": decision.decision_id,
                            "request_digest": digest,
                            "resource": grant_use.resource,
                            "sink": grant_use.output_sink,
                        },
                    )
                    if self.admission is not None:
                        self.admission._record_success_in_transaction(
                            connection,
                            breaker_key=self.admission._operation_key(
                                "effect_reserve", actor.domain_id
                            ),
                            now=now,
                        )
                    if phase_hook is not None:
                        phase_hook("before_commit")
                    result = {
                        "effect_id": effect_id,
                        "state": "effect_prepared",
                        "fence": fence,
                        "audit_hash": audit_hash,
                        "policy_decision_id": decision.decision_id,
                        "duplicate": False,
                    }

        if denied_reason is not None:
            raise AuthorizationError(denied_reason)
        if result is None:  # pragma: no cover - defensive invariant
            raise ConflictError("effect reservation produced no durable result")
        return result

    @staticmethod
    def _transition_row(connection: Any, effect_id: str) -> Any:
        return connection.execute(
            """SELECT
                   reservation.effect_id,
                   reservation.event_id,
                   reservation.grant_id,
                   reservation.actor_json AS reservation_actor_json,
                   reservation.request_digest,
                   reservation.state AS reservation_state,
                   reservation.fence,
                   reservation.created_at,
                   lifecycle.attempt_id,
                   lifecycle.executor_registration_id,
                   lifecycle.executor_actor_json,
                   lifecycle.execution_evidence_digest,
                   lifecycle.execution_started_at,
                   lifecycle.current_state,
                   lifecycle.uncertainty_evidence_digest,
                   lifecycle.uncertainty_recorded_at,
                   lifecycle.terminal_evidence_digest,
                   lifecycle.terminal_source,
                   lifecycle.terminal_recorded_at,
                   lifecycle.reconciliation_evidence_digest
                 FROM effect_reservations AS reservation
                 LEFT JOIN effect_lifecycle AS lifecycle ON lifecycle.effect_id=reservation.effect_id
                WHERE reservation.effect_id=?""",
            (effect_id,),
        ).fetchone()

    @staticmethod
    def _require_observation_time(*, observed_at: int, earliest: int, now: int) -> None:
        if observed_at < earliest or observed_at > now + 60:
            raise AuthenticationError("effect evidence observation time is outside its execution window")

    def _verify_executor_proof(
        self,
        connection: Any,
        *,
        row: Any,
        actor: VerifiedActor,
        proof: EffectTransitionProof,
        from_state: EffectState,
        to_state: EffectState,
        evidence_digest: str,
        now: int,
        consume_replay: bool,
        expected_role: str = "effect_authority",
        expected_workload_id: str | None = None,
        forbidden_registration_id: str | None = None,
    ) -> None:
        if actor.kind is not ActorKind.WORKLOAD or actor.binding_assurance != "workload_mtls":
            raise AuthorizationError("effect transition requires an authenticated registered workload")
        if (
            expected_role == "effect_authority"
            and
            row["executor_actor_json"] is not None
            and row["executor_actor_json"] != canonical_json(actor.audit_view()).decode("utf-8")
        ):
            raise ConflictError("effect transition names a different durable executor")
        if (
            forbidden_registration_id is not None
            and actor.workload_registration_id == forbidden_registration_id
        ):
            raise AuthorizationError("effect reconciliation authority must be independent of its executor")
        registration = connection.execute(
            "SELECT * FROM workload_registrations WHERE registration_id=?",
            (actor.workload_registration_id,),
        ).fetchone()
        domain = connection.execute(
            "SELECT status,revocation_epoch FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        grant = connection.execute(
            "SELECT revoked_at,expires_at FROM task_grants WHERE grant_id=?",
            (row["grant_id"],),
        ).fetchone()
        try:
            reservation_actor = VerifiedActor.model_validate(json.loads(row["reservation_actor_json"]))
        except Exception as exc:
            raise AuthorizationError("effect reservation actor binding is invalid") from exc
        expected_actor = None
        if registration is not None:
            expected_actor = WorkloadRegistry._actor_from_request(dict(registration))
        if (
            registration is None
            or expected_actor is None
            or expected_actor.audit_view() != actor.audit_view()
            or registration["status"] != "active"
            or int(registration["issued_at"]) > now
            or int(registration["expires_at"]) <= now
            or registration["workload_role"] != expected_role
            or (
                expected_workload_id is not None
                and registration["workload_id"] != expected_workload_id
            )
            or registration["domain_id"] != reservation_actor.domain_id
            or registration["parent_event_id"] != row["event_id"]
            or registration["task_grant_id"] != row["grant_id"]
            or registration["recipient_scope"] not in {"*", reservation_actor.harness_id}
            or domain is None
            or domain["status"] != "active"
            or int(domain["revocation_epoch"]) != int(registration["revocation_epoch"])
            or grant is None
            or grant["revoked_at"] is not None
            or int(grant["expires_at"]) <= now
        ):
            raise AuthorizationError("effect workload authority is not current for this reservation")
        expected = {
            "registration_id": actor.workload_registration_id,
            "workload_id": actor.workload_id,
            "workload_role": actor.workload_role,
            "process_id": actor.workload_process_id,
            "process_start_time": actor.workload_process_start_time,
            "session_id": actor.workload_session_id,
            "credential_epoch": actor.credential_epoch,
            "revocation_epoch": actor.workload_revocation_epoch,
            "parent_event_id": row["event_id"],
            "task_grant_id": row["grant_id"],
            "effect_id": row["effect_id"],
            "fence": int(row["fence"]),
            "from_state": from_state.value,
            "to_state": to_state.value,
            "evidence_digest": evidence_digest,
        }
        actual = proof.model_dump(
            mode="json",
            exclude={"signature", "timestamp", "nonce"},
        )
        if actual != expected or proof.timestamp > now + 60 or proof.timestamp < now - 300:
            raise AuthenticationError("effect transition proof binding or freshness failed")
        verify_signature(
            registration["public_key_pem"],
            "agentnet.effect.transition.v1",
            proof.signed_fields(),
            proof.signature,
        )
        if not consume_replay:
            return
        replay_key = canonical_digest(
            {
                "nonce": proof.nonce,
                "registration_id": proof.registration_id,
                "effect_id": proof.effect_id,
                "from_state": proof.from_state.value,
                "to_state": proof.to_state.value,
                "evidence_digest": proof.evidence_digest,
            }
        )
        inserted = connection.execute(
            """INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)
               ON CONFLICT(actor_id,nonce_hash) DO NOTHING""",
            (
                f"effect-workload:{proof.registration_id}",
                replay_key,
                max(now + 300, int(registration["expires_at"])),
            ),
        )
        if inserted.rowcount != 1:
            raise ReplayError("effect transition proof was already consumed")

    @staticmethod
    def _transition_result(row: Any, *, duplicate: bool) -> dict[str, object]:
        return {
            "effect_id": row["effect_id"],
            "state": row["current_state"],
            "fence": int(row["fence"]),
            "duplicate": duplicate,
        }

    def start_execution(
        self,
        effect_id: str,
        *,
        actor: VerifiedActor,
        proof: EffectTransitionProof,
        evidence: EffectExecutionEvidence,
        when: datetime | None = None,
    ) -> dict[str, object]:
        """Move one prepared effect behind its exact executor and fence."""

        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        evidence_digest = canonical_digest(evidence.model_dump(mode="json"))
        with self.store.transaction() as connection:
            row = self._transition_row(connection, effect_id)
            if row is None:
                raise AuthorizationError("effect reservation is not visible")
            if row["current_state"] is not None:
                exact_duplicate = (
                    row["current_state"] == EffectState.EXECUTING.value
                    and row["attempt_id"] == evidence.attempt_id
                    and row["executor_actor_json"]
                    == canonical_json(actor.audit_view()).decode("utf-8")
                    and row["execution_evidence_digest"] == evidence_digest
                )
                if not exact_duplicate:
                    raise ConflictError("effect execution already has different durable state")
                self._verify_executor_proof(
                    connection,
                    row=row,
                    actor=actor,
                    proof=proof,
                    from_state=EffectState.PREPARED,
                    to_state=EffectState.EXECUTING,
                    evidence_digest=evidence_digest,
                    now=now,
                    consume_replay=False,
                )
                return self._transition_result(row, duplicate=True)
            if row["reservation_state"] != EffectState.PREPARED.value:
                raise ConflictError("only a prepared effect can begin execution")
            if evidence.request_digest != row["request_digest"]:
                raise AuthenticationError("execution evidence does not bind the reserved request")
            self._require_observation_time(
                observed_at=evidence.dispatched_at,
                earliest=int(row["created_at"]),
                now=now,
            )
            if self.admission is not None:
                self.admission._admit_operation_in_transaction(
                    connection,
                    actor_scope=actor.workload_id or "unattributed",
                    domain_scope=actor.domain_id,
                    operation="effect_execute",
                    operation_id=effect_id,
                    cost=1,
                    pending_cost=0,
                )
            self._verify_executor_proof(
                connection,
                row=row,
                actor=actor,
                proof=proof,
                from_state=EffectState.PREPARED,
                to_state=EffectState.EXECUTING,
                evidence_digest=evidence_digest,
                now=now,
                consume_replay=True,
            )
            actor_json = canonical_json(actor.audit_view()).decode("utf-8")
            updated = connection.execute(
                """UPDATE effect_reservations SET state='effect_executing',updated_at=?
                    WHERE effect_id=? AND state='effect_prepared' AND fence=?""",
                (now, effect_id, int(row["fence"])),
            )
            if updated.rowcount != 1:
                raise ConflictError("effect fence or prepared state changed concurrently")
            connection.execute(
                """INSERT INTO effect_lifecycle(
                       effect_id,event_id,grant_id,fence,attempt_id,executor_registration_id,
                       executor_actor_json,execution_evidence_digest,execution_started_at,
                       current_state,updated_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,'effect_executing',?)""",
                (
                    effect_id,
                    row["event_id"],
                    row["grant_id"],
                    int(row["fence"]),
                    evidence.attempt_id,
                    actor.workload_registration_id,
                    actor_json,
                    evidence_digest,
                    now,
                    now,
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "effect.executing",
                    "actor": actor.audit_view(),
                    "effect_id": effect_id,
                    "event_id": row["event_id"],
                    "fence": int(row["fence"]),
                    "attempt_id": evidence.attempt_id,
                    "evidence_digest": evidence_digest,
                    "proof_digest": canonical_digest(proof.signed_fields()),
                },
            )
            if self.admission is not None:
                self.admission._record_success_in_transaction(
                    connection,
                    breaker_key=self.admission._operation_key(
                        "effect_execute", actor.domain_id
                    ),
                    now=now,
                )
            return {
                "effect_id": effect_id,
                "state": EffectState.EXECUTING.value,
                "fence": int(row["fence"]),
                "audit_hash": audit_hash,
                "duplicate": False,
            }

    def mark_unknown(
        self,
        effect_id: str,
        *,
        actor: VerifiedActor,
        proof: EffectTransitionProof,
        evidence: EffectUncertaintyEvidence,
        when: datetime | None = None,
    ) -> dict[str, object]:
        """Record commit uncertainty; only reconciliation may leave this state."""

        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        evidence_digest = canonical_digest(evidence.model_dump(mode="json"))
        with self.store.transaction() as connection:
            row = self._transition_row(connection, effect_id)
            if row is None:
                raise AuthorizationError("effect reservation is not visible")
            exact_duplicate = (
                row["current_state"] == EffectState.UNKNOWN.value
                and row["attempt_id"] == evidence.attempt_id
                and row["uncertainty_evidence_digest"] == evidence_digest
                and row["executor_actor_json"] == canonical_json(actor.audit_view()).decode("utf-8")
            )
            if exact_duplicate:
                self._verify_executor_proof(
                    connection,
                    row=row,
                    actor=actor,
                    proof=proof,
                    from_state=EffectState.EXECUTING,
                    to_state=EffectState.UNKNOWN,
                    evidence_digest=evidence_digest,
                    now=now,
                    consume_replay=False,
                )
                return self._transition_result(row, duplicate=True)
            if row["current_state"] != EffectState.EXECUTING.value:
                raise ConflictError("only an executing effect can become effect_unknown")
            if evidence.attempt_id != row["attempt_id"]:
                raise ConflictError("uncertainty evidence names a different execution attempt")
            self._require_observation_time(
                observed_at=evidence.observed_at,
                earliest=int(row["execution_started_at"]),
                now=now,
            )
            self._verify_executor_proof(
                connection,
                row=row,
                actor=actor,
                proof=proof,
                from_state=EffectState.EXECUTING,
                to_state=EffectState.UNKNOWN,
                evidence_digest=evidence_digest,
                now=now,
                consume_replay=True,
            )
            connection.execute(
                """UPDATE effect_lifecycle
                      SET current_state='effect_unknown',uncertainty_evidence_digest=?,
                          uncertainty_recorded_at=?,updated_at=?
                    WHERE effect_id=? AND current_state='effect_executing' AND fence=?""",
                (evidence_digest, now, now, effect_id, int(row["fence"])),
            )
            updated = connection.execute(
                """UPDATE effect_reservations SET state='effect_unknown',updated_at=?
                    WHERE effect_id=? AND state='effect_executing' AND fence=?""",
                (now, effect_id, int(row["fence"])),
            )
            if updated.rowcount != 1:
                raise ConflictError("effect execution state changed concurrently")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "effect.unknown",
                    "actor": actor.audit_view(),
                    "effect_id": effect_id,
                    "fence": int(row["fence"]),
                    "attempt_id": evidence.attempt_id,
                    "reason": evidence.reason,
                    "evidence_digest": evidence_digest,
                    "proof_digest": canonical_digest(proof.signed_fields()),
                },
            )
            return {
                "effect_id": effect_id,
                "state": EffectState.UNKNOWN.value,
                "fence": int(row["fence"]),
                "audit_hash": audit_hash,
                "duplicate": False,
            }

    def acknowledge_terminal(
        self,
        effect_id: str,
        *,
        actor: VerifiedActor,
        proof: EffectTransitionProof,
        terminal_state: EffectState,
        evidence: EffectTerminalEvidence,
        when: datetime | None = None,
    ) -> dict[str, object]:
        """Commit an exact known terminal executor acknowledgement idempotently."""

        if terminal_state not in TERMINAL_EFFECT_STATES:
            raise ConflictError("effect terminal acknowledgement names a nonterminal state")
        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        evidence_digest = canonical_digest(evidence.model_dump(mode="json"))
        with self.store.transaction() as connection:
            row = self._transition_row(connection, effect_id)
            if row is None:
                raise AuthorizationError("effect reservation is not visible")
            exact_duplicate = (
                row["current_state"] == terminal_state.value
                and row["terminal_source"] == "executor_ack"
                and row["terminal_evidence_digest"] == evidence_digest
                and row["attempt_id"] == evidence.attempt_id
                and row["executor_actor_json"] == canonical_json(actor.audit_view()).decode("utf-8")
            )
            if exact_duplicate:
                self._verify_executor_proof(
                    connection,
                    row=row,
                    actor=actor,
                    proof=proof,
                    from_state=EffectState.EXECUTING,
                    to_state=terminal_state,
                    evidence_digest=evidence_digest,
                    now=now,
                    consume_replay=False,
                )
                provenance = self._require_tool_output_provenance(
                    connection,
                    row=row,
                    output_digest=evidence.external_receipt_digest,
                )
                return self._transition_result(row, duplicate=True) | {
                    "provenance": provenance.reference().model_dump(mode="json")
                }
            if row["current_state"] == EffectState.UNKNOWN.value:
                raise ConflictError("effect_unknown forbids terminal acknowledgement; reconcile explicitly")
            if row["current_state"] != EffectState.EXECUTING.value:
                raise ConflictError("effect already has different durable terminal or execution state")
            if evidence.attempt_id != row["attempt_id"]:
                raise ConflictError("terminal evidence names a different execution attempt")
            self._require_observation_time(
                observed_at=evidence.observed_at,
                earliest=int(row["execution_started_at"]),
                now=now,
            )
            self._verify_executor_proof(
                connection,
                row=row,
                actor=actor,
                proof=proof,
                from_state=EffectState.EXECUTING,
                to_state=terminal_state,
                evidence_digest=evidence_digest,
                now=now,
                consume_replay=True,
            )
            provenance = self._record_tool_output_provenance(
                connection,
                row=row,
                actor=actor,
                output_digest=evidence.external_receipt_digest,
                completed_at=evidence.observed_at,
                recorded_at=now,
                implementation_role="effect-executor",
            )
            connection.execute(
                """UPDATE effect_lifecycle
                      SET current_state=?,terminal_evidence_digest=?,terminal_source='executor_ack',
                          terminal_recorded_at=?,updated_at=?
                    WHERE effect_id=? AND current_state='effect_executing' AND fence=?""",
                (terminal_state.value, evidence_digest, now, now, effect_id, int(row["fence"])),
            )
            updated = connection.execute(
                """UPDATE effect_reservations SET state=?,updated_at=?
                    WHERE effect_id=? AND state='effect_executing' AND fence=?""",
                (terminal_state.value, now, effect_id, int(row["fence"])),
            )
            if updated.rowcount != 1:
                raise ConflictError("effect execution state changed concurrently")
            if self.admission is not None:
                terminalized = self.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="protected_effect",
                    source_id=effect_id,
                    now=now,
                )
                if not terminalized:
                    raise ConflictError("effect terminal work reservation was not pending")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": f"effect.{terminal_state.value.removeprefix('effect_')}",
                    "actor": actor.audit_view(),
                    "effect_id": effect_id,
                    "fence": int(row["fence"]),
                    "attempt_id": evidence.attempt_id,
                    "terminal_source": "executor_ack",
                    "evidence_digest": evidence_digest,
                    "proof_digest": canonical_digest(proof.signed_fields()),
                    "provenance_digest": provenance.provenance_digest,
                    "authority_effect": "none",
                },
            )
            return {
                "effect_id": effect_id,
                "state": terminal_state.value,
                "fence": int(row["fence"]),
                "audit_hash": audit_hash,
                "duplicate": False,
                "provenance": provenance.reference().model_dump(mode="json"),
            }

    def reconcile(
        self,
        effect_id: str,
        *,
        actor: VerifiedActor,
        proof: EffectTransitionProof,
        evidence: EffectReconciliationEvidence,
        when: datetime | None = None,
    ) -> dict[str, object]:
        """Resolve effect_unknown only from an authenticated authoritative read-back."""

        terminal_state = EffectState(evidence.terminal_state)
        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        evidence_digest = canonical_digest(evidence.model_dump(mode="json"))
        with self.store.transaction() as connection:
            row = self._transition_row(connection, effect_id)
            if row is None:
                raise AuthorizationError("effect reservation is not visible")
            exact_duplicate = (
                row["current_state"] == terminal_state.value
                and row["terminal_source"] == "reconciliation"
                and row["terminal_evidence_digest"] == evidence_digest
                and row["reconciliation_evidence_digest"] == evidence_digest
                and row["attempt_id"] == evidence.attempt_id
            )
            if exact_duplicate:
                self._verify_executor_proof(
                    connection,
                    row=row,
                    actor=actor,
                    proof=proof,
                    from_state=EffectState.UNKNOWN,
                    to_state=terminal_state,
                    evidence_digest=evidence_digest,
                    now=now,
                    consume_replay=False,
                    expected_role="effect_reconciler",
                    expected_workload_id=f"effect-system:{evidence.authority_system_id}",
                    forbidden_registration_id=row["executor_registration_id"],
                )
                provenance = self._require_tool_output_provenance(
                    connection,
                    row=row,
                    output_digest=evidence.query_response_digest,
                )
                return self._transition_result(row, duplicate=True) | {
                    "provenance": provenance.reference().model_dump(mode="json")
                }
            if row["current_state"] != EffectState.UNKNOWN.value:
                raise ConflictError("only effect_unknown may be reconciled")
            if evidence.attempt_id != row["attempt_id"]:
                raise ConflictError("reconciliation evidence names a different execution attempt")
            self._require_observation_time(
                observed_at=evidence.observed_at,
                earliest=int(row["uncertainty_recorded_at"]),
                now=now,
            )
            self._verify_executor_proof(
                connection,
                row=row,
                actor=actor,
                proof=proof,
                from_state=EffectState.UNKNOWN,
                to_state=terminal_state,
                evidence_digest=evidence_digest,
                now=now,
                consume_replay=True,
                expected_role="effect_reconciler",
                expected_workload_id=f"effect-system:{evidence.authority_system_id}",
                forbidden_registration_id=row["executor_registration_id"],
            )
            provenance = self._record_tool_output_provenance(
                connection,
                row=row,
                actor=actor,
                output_digest=evidence.query_response_digest,
                completed_at=evidence.observed_at,
                recorded_at=now,
                implementation_role="effect-reconciler",
            )
            connection.execute(
                """UPDATE effect_lifecycle
                      SET current_state=?,terminal_evidence_digest=?,terminal_source='reconciliation',
                          terminal_recorded_at=?,reconciliation_evidence_digest=?,updated_at=?
                    WHERE effect_id=? AND current_state='effect_unknown' AND fence=?""",
                (
                    terminal_state.value,
                    evidence_digest,
                    now,
                    evidence_digest,
                    now,
                    effect_id,
                    int(row["fence"]),
                ),
            )
            updated = connection.execute(
                """UPDATE effect_reservations SET state=?,updated_at=?
                    WHERE effect_id=? AND state='effect_unknown' AND fence=?""",
                (terminal_state.value, now, effect_id, int(row["fence"])),
            )
            if updated.rowcount != 1:
                raise ConflictError("effect unknown state changed concurrently")
            if self.admission is not None:
                terminalized = self.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="protected_effect",
                    source_id=effect_id,
                    now=now,
                )
                if not terminalized:
                    raise ConflictError("effect reconciled work reservation was not pending")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "effect.reconciled",
                    "actor": actor.audit_view(),
                    "effect_id": effect_id,
                    "fence": int(row["fence"]),
                    "attempt_id": evidence.attempt_id,
                    "authority_system_id": evidence.authority_system_id,
                    "query_id": evidence.query_id,
                    "terminal_state": terminal_state.value,
                    "evidence_digest": evidence_digest,
                    "proof_digest": canonical_digest(proof.signed_fields()),
                    "provenance_digest": provenance.provenance_digest,
                    "authority_effect": "none",
                },
            )
            return {
                "effect_id": effect_id,
                "state": terminal_state.value,
                "fence": int(row["fence"]),
                "audit_hash": audit_hash,
                "duplicate": False,
                "provenance": provenance.reference().model_dump(mode="json"),
            }

    def retry(self, effect_id: str) -> None:
        row = self.store.fetch_one("SELECT state FROM effect_reservations WHERE effect_id=?", (effect_id,))
        if row and row["state"] == "effect_unknown":
            raise ConflictError("effect_unknown must reconcile; blind retry is forbidden")

    def cancel_prepared(
        self,
        effect_id: str,
        *,
        actor: VerifiedActor,
        policy_decision_id: str,
        when: datetime | None = None,
    ) -> dict[str, object]:
        """Cancel only an effect that has never crossed the execution boundary."""

        when = when or datetime.now(UTC)
        now = int(when.timestamp())
        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT effect_id,actor_json,state,fence FROM effect_reservations WHERE effect_id=?",
                (effect_id,),
            ).fetchone()
            if row is None or row["actor_json"] != canonical_json(actor.audit_view()).decode("utf-8"):
                raise AuthorizationError("effect reservation is not visible")
            require_current_authority_decision(
                connection,
                authority=IssuanceAuthority(
                    actor=actor,
                    policy_decision_id=policy_decision_id,
                ),
                expected_action="effect.cancel",
                expected_resource=effect_id,
                expected_request={},
                when=when,
            )
            if row["state"] == EffectState.CANCELLED.value:
                return {
                    "effect_id": effect_id,
                    "state": EffectState.CANCELLED.value,
                    "fence": int(row["fence"]),
                    "duplicate": True,
                }
            if row["state"] != EffectState.PREPARED.value:
                raise ConflictError(
                    "an executing or uncertain effect requires authenticated terminal evidence"
                )
            updated = connection.execute(
                """UPDATE effect_reservations SET state='effect_cancelled',updated_at=?
                     WHERE effect_id=? AND state='effect_prepared' AND fence=?""",
                (now, effect_id, int(row["fence"])),
            )
            if updated.rowcount != 1:
                raise ConflictError("effect cancellation raced with execution")
            if self.admission is not None:
                terminalized = self.admission._terminalize_work_in_transaction(
                    connection,
                    work_kind="protected_effect",
                    source_id=effect_id,
                    now=now,
                )
                if not terminalized:
                    raise ConflictError("effect cancelled work reservation was not pending")
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "effect.cancelled_before_execution",
                    "actor": actor.audit_view(),
                    "effect_id": effect_id,
                    "fence": int(row["fence"]),
                    "policy_decision_id": policy_decision_id,
                },
            )
            return {
                "effect_id": effect_id,
                "state": EffectState.CANCELLED.value,
                "fence": int(row["fence"]),
                "audit_hash": audit_hash,
                "duplicate": False,
            }

    def status(self, effect_id: str, *, actor: VerifiedActor) -> dict[str, object]:
        """Return content-free lifecycle state to the exact current effect actor."""

        now = int(time.time())
        with self.store.transaction(immediate=False) as connection:
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (actor.domain_id,),
            ).fetchone()
            row = connection.execute(
                """SELECT effect_id,actor_json,state,fence,created_at,updated_at
                     FROM effect_reservations WHERE effect_id=?""",
                (effect_id,),
            ).fetchone()
            if domain is None or row is None:
                raise AuthorizationError("effect reservation is not visible")
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=datetime.fromtimestamp(now, UTC),
            )
            if (
                denial is not None
                or row["actor_json"] != canonical_json(actor.audit_view()).decode("utf-8")
            ):
                raise AuthorizationError("effect reservation is not visible")
            return {
                "effect_id": row["effect_id"],
                "state": row["state"],
                "fence": int(row["fence"]),
                "created_at": int(row["created_at"]),
                "updated_at": int(row["updated_at"]),
            }
