"""Deterministic no-selector S5 C0 request/reply integration."""

from __future__ import annotations

import hashlib
import json
from copy import copy
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any
from uuid import NAMESPACE_URL, uuid5

from agentnet.authorization.bootstrap_plan import C0_REQUIRED_FACTS
from agentnet.authorization.c0_pilot import C0_PILOT_SUCCESS, c0_result
from agentnet.authorization.policy import C0GuardedOperation, PolicyEngine, validate_actor_state
from agentnet.errors import AuthorizationError, ConflictError, RetryableConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.events import new_event
from agentnet.protocol.models import (
    AcceptingStorageBoundary,
    Classification,
    DeliveryFact,
    EventType,
)
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


PhaseHook = Callable[[str], None]


def _sha256(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _canonical_object(value: str) -> dict[str, Any]:
    parsed = json.loads(value)
    if not isinstance(parsed, dict) or canonical_json(parsed).decode("utf-8") != value:
        raise AuthorizationError("C0 pilot protected state is invalid")
    return parsed

def _bootstrap_c0_authorization_context(row: Any) -> dict[str, Any]:
    members = sorted((str(row["owner_harness_id"]), str(row["fresh_harness_id"])))
    scope_id = f"bootstrap-c0:{row['plan_id']}:{row['guard_id']}"
    revision = 1
    policy_revision = int(row["policy_revision"])
    revocation_epoch = int(row["domain_revocation_epoch"])
    preimage = {
        "schema": "agentnet.bootstrap-c0.authorization-context.v1",
        "plan_id": str(row["plan_id"]),
        "guard_id": str(row["guard_id"]),
        "collaboration_scope_id": scope_id,
        "collaboration_scope_revision": revision,
        "collaboration_scope_policy_revision": policy_revision,
        "collaboration_scope_domain_revocation_epoch": revocation_epoch,
        "collaboration_scope_member_harness_ids": members,
        "guard_expires_at": int(row["guard_expires_at"]),
        "request_payload_digest": str(row["request_payload_digest"]),
        "reply_payload_digest": str(row["reply_payload_digest"]),
    }
    return {
        "collaboration_scope_id": scope_id,
        "collaboration_scope_revision": revision,
        "collaboration_scope_policy_revision": policy_revision,
        "collaboration_scope_domain_revocation_epoch": revocation_epoch,
        "collaboration_scope_member_harness_ids": members,
        "collaboration_scope_digest": canonical_digest(preimage),
    }


def _bootstrap_c0_payload(row: Any, direction: str) -> dict[str, Any]:
    if direction not in {"request", "reply"}:
        raise ValueError("C0 pilot payload direction is invalid")
    payload = _canonical_object(str(row[f"{direction}_payload_json"]))
    if "authorization_context" in payload:
        raise AuthorizationError("C0 pilot payload cannot supply authorization_context")
    payload["authorization_context"] = _bootstrap_c0_authorization_context(row)
    return payload


def _bootstrap_c0_payload_digest(row: Any, direction: str) -> str:
    return canonical_digest(_bootstrap_c0_payload(row, direction))

class _BootstrapC0ScopeSnapshot:
    def __init__(self, context: dict[str, Any]) -> None:
        self._context = context

    def authorization_context(self) -> dict[str, Any]:
        return self._context


class _BootstrapC0AcknowledgementScopes:
    def __init__(self, context: dict[str, Any], actor: VerifiedActor) -> None:
        self._scope = _BootstrapC0ScopeSnapshot(context)
        self._actor = actor

    def require_in_transaction(
        self,
        _connection: Any,
        *,
        actor: VerifiedActor,
        scope_id: str,
        action: str,
        resource: str,
        target_harness_ids: tuple[str, ...],
        classification: Classification,
    ) -> _BootstrapC0ScopeSnapshot:
        context = self._scope.authorization_context()
        if (
            actor != self._actor
            or actor.harness_id is None
            or scope_id != context["collaboration_scope_id"]
            or action != "message.acknowledge"
            or resource != "conversation:direct"
            or target_harness_ids != (actor.harness_id,)
            or classification is not Classification.C0_PUBLIC
        ):
            raise AuthorizationError("C0 pilot acknowledgement scope is invalid")
        return self._scope



class C0PilotService:
    """Compose existing policy and mailbox primitives into one fixed C0 proof."""

    def __init__(
        self,
        store: StoreBackend,
        policy: PolicyEngine,
        mailbox: MailboxService,
        *,
        clock: Callable[[], int],
        phase_hook: PhaseHook | None = None,
    ) -> None:
        if policy.store is not store or mailbox.store is not store:
            raise ValueError("C0 pilot components must share one store")
        self.store = store
        self.policy = policy
        self.mailbox = mailbox
        self.clock = clock
        self.phase_hook = phase_hook

    def _acknowledge_event(
        self,
        connection: Any,
        *,
        row: Any,
        actor: VerifiedActor,
        event_id: str,
        recipient_id: str,
        envelope_digest: str,
        now: int,
    ) -> dict[str, Any]:
        context = _bootstrap_c0_authorization_context(row)
        acknowledgement_mailbox = copy(self.mailbox)
        acknowledgement_mailbox.collaboration_scopes = _BootstrapC0AcknowledgementScopes(
            context, actor
        )
        return acknowledgement_mailbox._acknowledge_in_transaction(
            connection,
            event_id=event_id,
            collaboration_scope_id=str(context["collaboration_scope_id"]),
            recipient_id=recipient_id,
            envelope_digest_value=envelope_digest,
            owner_actor=actor,
            now=now,
        )

    def _phase(self, name: str) -> None:
        if self.phase_hook is not None:
            self.phase_hook(name)

    @staticmethod
    def _require_actor(actor: VerifiedActor) -> None:
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.credential_epoch is None
        ):
            raise AuthorizationError("C0 pilot actor is ineligible")

    @staticmethod
    def _latest_plan(connection: Any, actor: VerifiedActor) -> Any:
        rows = connection.execute(
            """SELECT p.*,g.guard_id,g.state AS guard_state,g.classification,
                      g.request_payload_schema,g.request_payload_schema_digest,
                      g.request_payload_json,g.request_payload_digest,
                      g.reply_payload_schema,g.reply_payload_schema_digest,
                      g.reply_payload_json,g.reply_payload_digest,
                      g.request_remaining_uses,g.reply_remaining_uses,
                      g.created_at AS guard_created_at,g.expires_at AS guard_expires_at,
                      a.attempt_id,a.state AS attempt_state,a.request_idempotency_digest,
                      a.reply_idempotency_digest,a.sanitized_result
                 FROM bootstrap_grant_plans p
                 JOIN c0_plan_guards g ON g.plan_id=p.plan_id
                 LEFT JOIN c0_pilot_attempts a ON a.plan_id=p.plan_id
                WHERE p.state='committed' AND p.domain_id=? AND p.principal_id=?
                  AND (p.owner_harness_id=? OR p.fresh_harness_id=?)
                ORDER BY p.committed_at DESC,p.plan_id DESC LIMIT 2""",
            (actor.domain_id, actor.principal_id, actor.harness_id, actor.harness_id),
        ).fetchall()
        if not rows:
            raise AuthorizationError("C0 pilot is unavailable")
        if len(rows) == 2 and rows[0]["committed_at"] == rows[1]["committed_at"]:
            raise ConflictError("C0 pilot current plan is ambiguous")
        return rows[0]

    def _invalidate_binding_drift(
        self,
        connection: Any,
        row: Any,
        *,
        now: int,
        reason: str,
    ) -> bool:
        if row["guard_state"] == "invalidated" and reason in {
            "active_harness_set_drift",
            "active_credential_set_drift",
        }:
            return False
        if row["guard_state"] not in {"pending", "active"}:
            raise AuthorizationError("C0 pilot identity binding is no longer current")
        guard = connection.execute(
            """UPDATE c0_plan_guards SET state='invalidated',invalidated_at=?
                WHERE guard_id=? AND state IN ('pending','active')""",
            (now, row["guard_id"]),
        )
        if guard.rowcount != 1:
            raise RetryableConflictError("C0 pilot binding invalidation raced")
        if row["attempt_id"] is not None and row["attempt_state"] in {
            "active",
            "evidence_complete",
        }:
            attempt = connection.execute(
                """UPDATE c0_pilot_attempts SET state='failed',terminal_at=?
                    WHERE attempt_id=? AND state IN ('active','evidence_complete')""",
                (now, row["attempt_id"]),
            )
            if attempt.rowcount != 1:
                raise RetryableConflictError("C0 pilot attempt invalidation raced")
        self.store.append_audit(
            connection,
            {
                "action": "c0_pilot.binding_invalidated",
                "plan_id": row["plan_id"],
                "guard_id": row["guard_id"],
                "reason": reason,
            },
        )
        return False

    def _require_current_bindings(
        self,
        connection: Any,
        row: Any,
        actor: VerifiedActor,
        now: int,
    ) -> bool:
        if actor.harness_id == row["owner_harness_id"]:
            actor_role = "owner"
        elif actor.harness_id == row["fresh_harness_id"]:
            actor_role = "fresh"
        else:
            raise AuthorizationError("C0 pilot actor is outside the exact harness-set binding")
        approved_actor_credential = (
            actor.credential_id == row[f"{actor_role}_credential_id"]
            and int(actor.credential_epoch) == int(row[f"{actor_role}_credential_epoch"])
        )

        denial, revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(row["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (row["domain_id"],),
        ).fetchone()
        principal = connection.execute(
            "SELECT status,domain_id FROM principals WHERE principal_id=?",
            (row["principal_id"],),
        ).fetchone()
        if denial is not None and not approved_actor_credential:
            # An unverified caller-selected credential cannot mutate plan state.
            # A current alternate credential passes actor validation, then the
            # authoritative active-set check below persistently invalidates.
            raise AuthorizationError("C0 pilot actor credential binding is not approved")
        if (
            denial is not None
            or revision != int(row["policy_revision"])
            or domain is None
            or domain["status"] != "active"
            or int(domain["policy_revision"]) != int(row["policy_revision"])
            or int(domain["revocation_epoch"]) != int(row["domain_revocation_epoch"])
            or principal is None
            or principal["status"] != "active"
            or principal["domain_id"] != row["domain_id"]
        ):
            return self._invalidate_binding_drift(
                connection, row, now=now, reason="identity_or_policy_binding_drift"
            )

        expected_harnesses = {
            str(row["owner_harness_id"]),
            str(row["fresh_harness_id"]),
        }
        active_harnesses = {
            str(item["harness_id"])
            for item in connection.execute(
                """SELECT harness_id FROM harnesses
                    WHERE domain_id=? AND principal_id=? AND status='active'""",
                (row["domain_id"], row["principal_id"]),
            ).fetchall()
        }
        if active_harnesses != expected_harnesses:
            return self._invalidate_binding_drift(
                connection, row, now=now, reason="active_harness_set_drift"
            )

        expected_credentials = {
            (str(row["owner_harness_id"]), str(row["owner_credential_id"])),
            (str(row["fresh_harness_id"]), str(row["fresh_credential_id"])),
        }
        active_credentials = {
            (str(item["harness_id"]), str(item["credential_id"]))
            for item in connection.execute(
                """SELECT h.harness_id,c.credential_id
                     FROM harnesses h JOIN credentials c ON c.harness_id=h.harness_id
                    WHERE h.domain_id=? AND h.principal_id=? AND h.status='active'
                      AND c.status='active' AND c.not_before<=? AND c.expires_at>?""",
                (row["domain_id"], row["principal_id"], now, now),
            ).fetchall()
        }
        if active_credentials != expected_credentials:
            return self._invalidate_binding_drift(
                connection, row, now=now, reason="active_credential_set_drift"
            )
        if not approved_actor_credential:
            raise AuthorizationError("C0 pilot actor credential binding is not approved")

        for role in ("owner", "fresh"):
            current = connection.execute(
                """SELECT h.status,h.principal_id,h.domain_id,h.credential_epoch,
                          c.status AS credential_status,c.epoch,c.not_before,c.expires_at
                     FROM harnesses h JOIN credentials c ON c.harness_id=h.harness_id
                    WHERE h.harness_id=? AND c.credential_id=?""",
                (row[f"{role}_harness_id"], row[f"{role}_credential_id"]),
            ).fetchall()
            if len(current) != 1:
                return self._invalidate_binding_drift(
                    connection, row, now=now, reason="exact_harness_binding_absent"
                )
            binding = current[0]
            if (
                binding["status"] != "active"
                or binding["credential_status"] != "active"
                or binding["principal_id"] != row["principal_id"]
                or binding["domain_id"] != row["domain_id"]
                or int(binding["credential_epoch"]) != int(row[f"{role}_credential_epoch"])
                or int(binding["epoch"]) != int(row[f"{role}_credential_epoch"])
                or int(binding["not_before"]) > now
                or int(binding["expires_at"]) <= now
            ):
                return self._invalidate_binding_drift(
                    connection, row, now=now, reason="exact_harness_binding_drift"
                )
        return True

    @staticmethod
    def _expire_if_due(connection: Any, row: Any, now: int) -> bool:
        if int(row["guard_expires_at"]) > now:
            return False
        if row["guard_state"] in {"pending", "active"}:
            connection.execute(
                "UPDATE c0_plan_guards SET state='expired',invalidated_at=? WHERE guard_id=? AND state IN ('pending','active')",
                (now, row["guard_id"]),
            )
        if row["attempt_id"] is not None and row["attempt_state"] in {
            "active", "evidence_complete"
        }:
            connection.execute(
                "UPDATE c0_pilot_attempts SET state='expired',terminal_at=? WHERE attempt_id=? AND state IN ('active','evidence_complete')",
                (now, row["attempt_id"]),
            )
        return True

    @staticmethod
    def _attempt_id(row: Any) -> str:
        return str(uuid5(NAMESPACE_URL, f"agentnet:c0-attempt:{row['plan_digest']}:{row['guard_id']}"))

    @staticmethod
    def _event_id(attempt_id: str, direction: str) -> str:
        return str(uuid5(NAMESPACE_URL, f"agentnet:c0-event:{attempt_id}:{direction}"))

    @staticmethod
    def _request_key(row: Any, attempt_id: str) -> str:
        return f"c0-request:{_sha256(f'{row["plan_digest"]}:{attempt_id}:request')}"

    @staticmethod
    def _reply_key(row: Any, attempt_id: str, request_event_id: str, request_digest: str) -> str:
        return f"c0-reply:{_sha256(f'{row["plan_digest"]}:{attempt_id}:{request_event_id}:{request_digest}:reply')}"

    @staticmethod
    def _fact(
        connection: Any,
        *,
        attempt_id: str,
        fact_kind: str,
    ) -> Any | None:
        return connection.execute(
            "SELECT * FROM c0_pilot_facts WHERE attempt_id=? AND fact_kind=?",
            (attempt_id, fact_kind),
        ).fetchone()

    @staticmethod
    def _insert_fact(
        connection: Any,
        *,
        attempt_id: str,
        fact_kind: str,
        issuer_kind: str,
        issuer_harness_id: str | None,
        event_id: str,
        receipt_id: str | None,
        envelope_digest: str,
        storage_fact: str | None,
        observed_at: int,
    ) -> None:
        evidence = {
            "schema": "agentnet.c0-pilot.fact-evidence.v1",
            "fact_kind": fact_kind,
            "issuer_kind": issuer_kind,
            "issuer_harness_id": issuer_harness_id,
            "event_id": event_id,
            "receipt_id": receipt_id,
            "envelope_digest": envelope_digest,
            "storage_fact": storage_fact,
        }
        evidence_json = canonical_json(evidence).decode("utf-8")
        existing = C0PilotService._fact(
            connection, attempt_id=attempt_id, fact_kind=fact_kind
        )
        expected = (
            issuer_kind,
            issuer_harness_id,
            event_id,
            receipt_id,
            envelope_digest,
            storage_fact,
            evidence_json,
        )
        if existing is not None:
            actual = tuple(
                existing[name]
                for name in (
                    "issuer_kind", "issuer_harness_id", "event_id", "receipt_id",
                    "envelope_digest", "storage_fact", "evidence_json",
                )
            )
            if actual != expected:
                raise ConflictError("C0 pilot fact conflicts with authoritative evidence")
            return
        connection.execute(
            """INSERT INTO c0_pilot_facts(
                   attempt_id,fact_kind,issuer_kind,issuer_harness_id,event_id,receipt_id,
                   envelope_digest,storage_fact,evidence_json,observed_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
            (attempt_id, fact_kind, *expected[:-1], evidence_json, observed_at),
        )

    def _request_event(self, row: Any, actor: VerifiedActor, attempt_id: str, now: int) -> Any:
        payload = _bootstrap_c0_payload(row, "request")
        event = new_event(
            event_id=self._event_id(attempt_id, "request"),
            domain_id=actor.domain_id,
            actor=actor,
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload=payload,
            idempotency_key=self._request_key(row, attempt_id),
            recipients=(str(row["owner_harness_id"]),),
            delivery_expires_at=datetime.fromtimestamp(int(row["guard_expires_at"]), UTC),
            retention_delete_at=datetime.fromtimestamp(int(row["guard_expires_at"]), UTC),
            policy_revision=int(row["policy_revision"]),
        )
        return event.model_copy(
            update={"created_at": datetime.fromtimestamp(int(row["guard_created_at"]), UTC)}
        )

    def _reply_event(
        self,
        row: Any,
        actor: VerifiedActor,
        attempt_id: str,
        request_fact: Any,
        now: int,
    ) -> Any:
        payload = _bootstrap_c0_payload(row, "reply")
        event = new_event(
            event_id=self._event_id(attempt_id, "reply"),
            domain_id=actor.domain_id,
            actor=actor,
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload=payload,
            idempotency_key=self._reply_key(
                row, attempt_id, str(request_fact["event_id"]), str(request_fact["envelope_digest"])
            ),
            recipients=(str(row["fresh_harness_id"]),),
            causal_parent_ids=(str(request_fact["event_id"]),),
            delivery_expires_at=datetime.fromtimestamp(int(row["guard_expires_at"]), UTC),
            retention_delete_at=datetime.fromtimestamp(int(row["guard_expires_at"]), UTC),
            policy_revision=int(row["policy_revision"]),
        )
        return event.model_copy(
            update={"created_at": datetime.fromtimestamp(int(row["guard_created_at"]), UTC)}
        )

    @staticmethod
    def _event_row(connection: Any, *, event_id: str, recipient_id: str) -> Any:
        row = connection.execute(
            """SELECT e.*,r.current_fact,r.recipient_id,r.cursor
                 FROM events e JOIN recipients r ON r.event_id=e.event_id
                WHERE e.event_id=? AND r.recipient_id=?""",
            (event_id, recipient_id),
        ).fetchone()
        if row is None:
            raise AuthorizationError("C0 pilot exact mailbox event is unavailable")
        return row

    def _require_custody_event(
        self,
        connection: Any,
        *,
        row: Any,
        attempt_id: str,
        fact_kind: str,
    ) -> Any:
        if fact_kind not in {"request_durable_custody", "reply_durable_custody"}:
            raise ValueError("custody fact kind is invalid")
        direction = "request" if fact_kind.startswith("request_") else "reply"
        sender = row["fresh_harness_id"] if direction == "request" else row["owner_harness_id"]
        recipient = row["owner_harness_id"] if direction == "request" else row["fresh_harness_id"]
        payload_digest = _bootstrap_c0_payload_digest(row, direction)
        fact = self._fact(connection, attempt_id=attempt_id, fact_kind=fact_kind)
        if (
            fact is None
            or fact["issuer_kind"] != "accepting_core"
            or fact["issuer_harness_id"] is not None
            or fact["event_id"] != self._event_id(attempt_id, direction)
            or fact["storage_fact"] != DeliveryFact.ACCEPTED_LOCAL.value
        ):
            raise AuthorizationError("C0 pilot custody fact is invalid")
        receipt = connection.execute(
            """SELECT fact,event_digest,recipient_id,owner_actor_json FROM receipts
                WHERE receipt_id=? AND event_id=?""",
            (fact["receipt_id"], fact["event_id"]),
        ).fetchone()
        try:
            receipt_owner = AcceptingStorageBoundary.model_validate_json(
                receipt["owner_actor_json"] if receipt is not None else ""
            )
        except Exception as exc:
            raise AuthorizationError("C0 pilot custody receipt owner is invalid") from exc
        if (
            receipt is None
            or receipt["fact"] != DeliveryFact.ACCEPTED_LOCAL.value
            or receipt["event_digest"] != fact["envelope_digest"]
            or receipt["recipient_id"] is not None
            or canonical_json(receipt_owner.model_dump(mode="json")).decode("utf-8")
            != receipt["owner_actor_json"]
            or receipt_owner.domain_id != row["domain_id"]
            or receipt_owner.storage_profile != "local_transactional"
            or receipt_owner.acceptance_fact is not DeliveryFact.ACCEPTED_LOCAL
            or receipt_owner.event_digest != fact["envelope_digest"]
        ):
            raise AuthorizationError("C0 pilot custody receipt is invalid")
        event_row = self._event_row(
            connection, event_id=str(fact["event_id"]), recipient_id=str(recipient)
        )
        event, _payload = self.mailbox._validated_event_and_payload(
            event_row, connection=connection
        )
        if (
            event.actor.harness_id != sender
            or event.actor.principal_id != row["principal_id"]
            or event.recipients != (recipient,)
            or event.classification is not Classification.C0_PUBLIC
            or event.payload_digest != payload_digest
            or canonical_digest(event.model_dump(mode="json", exclude_none=True))
            != fact["envelope_digest"]
        ):
            raise AuthorizationError("C0 pilot custody event binding is invalid")
        if direction == "reply":
            request = self._fact(
                connection,
                attempt_id=attempt_id,
                fact_kind="request_durable_custody",
            )
            if request is None or event.causal_parent_ids != (request["event_id"],):
                raise AuthorizationError("C0 pilot reply lineage is invalid")
        return fact

    def start(self, *, actor: VerifiedActor) -> dict[str, str]:
        self._require_actor(actor)
        now = int(self.clock())
        with self.store.transaction(immediate=True) as connection:
            row = self._latest_plan(connection, actor)
            if not self._require_current_bindings(connection, row, actor, now):
                return c0_result("invalidated")
            if row["guard_state"] == "invalidated":
                return c0_result("invalidated")
            if actor.harness_id != row["fresh_harness_id"]:
                raise AuthorizationError("only exact fresh harness may start C0 pilot")
            if row["attempt_state"] == "communication_revoked":
                self._require_terminal_cleanup(connection, row)
                return c0_result(C0_PILOT_SUCCESS)
            if self._expire_if_due(connection, row, now):
                return c0_result("expired")
            attempt_id = self._attempt_id(row)
            request_event = self._request_event(row, actor, attempt_id, now)
            request_digest = canonical_digest(request_event.model_dump(mode="json", exclude_none=True))
            request_key = request_event.idempotency_key
            reply_key = self._reply_key(row, attempt_id, request_event.event_id, request_digest)
            if row["attempt_id"] is not None:
                if (
                    row["attempt_id"] != attempt_id
                    or row["request_idempotency_digest"] != _sha256(request_key)
                    or row["reply_idempotency_digest"] != _sha256(reply_key)
                    or row["attempt_state"] != "active"
                    or row["guard_state"] != "active"
                ):
                    raise ConflictError("C0 pilot attempt binding conflicts")
                if self._fact(connection, attempt_id=attempt_id, fact_kind="reply_durable_custody"):
                    self._require_custody_event(
                        connection,
                        row=row,
                        attempt_id=attempt_id,
                        fact_kind="request_durable_custody",
                    )
                    self._require_custody_event(
                        connection,
                        row=row,
                        attempt_id=attempt_id,
                        fact_kind="reply_durable_custody",
                    )
                    return c0_result("waiting_fresh")
                if self._fact(connection, attempt_id=attempt_id, fact_kind="request_durable_custody"):
                    self._require_custody_event(
                        connection,
                        row=row,
                        attempt_id=attempt_id,
                        fact_kind="request_durable_custody",
                    )
                    return c0_result("waiting_owner")
                raise ConflictError("C0 pilot attempt lacks committed request evidence")
            if row["guard_state"] != "pending":
                raise ConflictError("C0 pilot guard is not pending")
            connection.execute(
                """INSERT INTO c0_pilot_attempts(
                       attempt_id,plan_id,guard_id,request_idempotency_digest,
                       reply_idempotency_digest,state,created_at,expires_at
                   ) VALUES(?,?,?,?,?,'active',?,?)""",
                (
                    attempt_id, row["plan_id"], row["guard_id"], _sha256(request_key),
                    _sha256(reply_key), now, row["guard_expires_at"],
                ),
            )
            updated = connection.execute(
                "UPDATE c0_plan_guards SET state='active' WHERE guard_id=? AND state='pending'",
                (row["guard_id"],),
            )
            if updated.rowcount != 1:
                raise RetryableConflictError("C0 pilot guard activation raced")
            self._phase("after_guard_activation")
            self.policy._require_c0_operation_in_transaction(
                connection,
                actor=actor,
                action="message.send",
                resource="direct",
                context=C0GuardedOperation(
                    attempt_id=attempt_id,
                    operation_scope="fresh_to_owner_send",
                    peer_harness_id=str(row["owner_harness_id"]),
                    classification=Classification.C0_PUBLIC,
                    payload_digest=str(row["request_payload_digest"]),
                    event_id=request_event.event_id,
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            accepted = self.mailbox._accept_in_transaction(connection, request_event, now=now)
            self._phase("after_request_accept")
            use = connection.execute(
                """UPDATE c0_plan_guards SET request_remaining_uses=0
                    WHERE guard_id=? AND state='active' AND request_remaining_uses=1""",
                (row["guard_id"],),
            )
            if use.rowcount != 1:
                raise RetryableConflictError("C0 pilot request use raced")
            self._insert_fact(
                connection,
                attempt_id=attempt_id,
                fact_kind="request_durable_custody",
                issuer_kind="accepting_core",
                issuer_harness_id=None,
                event_id=str(accepted["event_id"]),
                receipt_id=str(accepted["receipt_id"]),
                envelope_digest=str(accepted["envelope_digest"]),
                storage_fact=str(accepted["fact"]),
                observed_at=now,
            )
            self.store.append_audit(
                connection,
                {"action": "c0_pilot.request_accepted", "attempt_id": attempt_id},
            )
            self._phase("before_start_commit")
            return c0_result("waiting_owner")

    def respond(self, *, actor: VerifiedActor) -> dict[str, str]:
        self._require_actor(actor)
        now = int(self.clock())
        with self.store.transaction(immediate=True) as connection:
            row = self._latest_plan(connection, actor)
            if not self._require_current_bindings(connection, row, actor, now):
                return c0_result("invalidated")
            if row["guard_state"] == "invalidated":
                return c0_result("invalidated")
            if actor.harness_id != row["owner_harness_id"]:
                raise AuthorizationError("only exact owner harness may respond to C0 pilot")
            if row["attempt_state"] == "communication_revoked":
                self._require_terminal_cleanup(connection, row)
                return c0_result(C0_PILOT_SUCCESS)
            if self._expire_if_due(connection, row, now):
                return c0_result("expired")
            attempt_id = str(row["attempt_id"] or "")
            if not attempt_id or row["guard_state"] != "active" or row["attempt_state"] != "active":
                raise AuthorizationError("C0 pilot request is not active")
            reply_fact = self._fact(connection, attempt_id=attempt_id, fact_kind="reply_durable_custody")
            if reply_fact is not None:
                self._require_custody_event(
                    connection,
                    row=row,
                    attempt_id=attempt_id,
                    fact_kind="request_durable_custody",
                )
                self._require_custody_event(
                    connection,
                    row=row,
                    attempt_id=attempt_id,
                    fact_kind="reply_durable_custody",
                )
                return c0_result("waiting_fresh")
            request_fact = self._fact(
                connection, attempt_id=attempt_id, fact_kind="request_durable_custody"
            )
            if request_fact is None:
                raise AuthorizationError("C0 pilot request evidence is absent")
            request_row = self._event_row(
                connection,
                event_id=str(request_fact["event_id"]),
                recipient_id=str(row["owner_harness_id"]),
            )
            read_context = C0GuardedOperation(
                attempt_id=attempt_id,
                operation_scope="owner_mailbox_read",
                peer_harness_id=None,
                classification=Classification.C0_PUBLIC,
                event_id=str(request_fact["event_id"]),
                envelope_digest=str(request_fact["envelope_digest"]),
            )
            self.policy._require_c0_operation_in_transaction(
                connection, actor=actor, action="mailbox.read",
                resource=str(row["owner_harness_id"]), context=read_context,
                when=datetime.fromtimestamp(now, UTC),
            )
            request_event, _payload = self.mailbox._validated_event_and_payload(
                request_row, connection=connection
            )
            if (
                request_event.actor.harness_id != row["fresh_harness_id"]
                or request_event.recipients != (row["owner_harness_id"],)
                or request_event.classification is not Classification.C0_PUBLIC
                or request_event.payload_digest != _bootstrap_c0_payload_digest(row, "request")
            ):
                raise AuthorizationError("C0 pilot request event binding is invalid")
            self._insert_fact(
                connection, attempt_id=attempt_id, fact_kind="request_retrieved",
                issuer_kind="harness", issuer_harness_id=actor.harness_id,
                event_id=request_event.event_id, receipt_id=None,
                envelope_digest=str(request_fact["envelope_digest"]), storage_fact=None,
                observed_at=now,
            )
            self.policy._require_c0_operation_in_transaction(
                connection, actor=actor, action="mailbox.acknowledge",
                resource=str(row["owner_harness_id"]),
                context=C0GuardedOperation(
                    attempt_id=attempt_id,
                    operation_scope="owner_mailbox_acknowledge",
                    peer_harness_id=None,
                    classification=Classification.C0_PUBLIC,
                    event_id=request_event.event_id,
                    envelope_digest=str(request_fact["envelope_digest"]),
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            acknowledgement = self._acknowledge_event(
                connection,
                row=row,
                actor=actor,
                event_id=request_event.event_id,
                recipient_id=str(row["owner_harness_id"]),
                envelope_digest=str(request_fact["envelope_digest"]),
                now=now,
            )
            self._insert_fact(
                connection, attempt_id=attempt_id,
                fact_kind="request_recipient_acknowledged", issuer_kind="harness",
                issuer_harness_id=actor.harness_id, event_id=request_event.event_id,
                receipt_id=str(acknowledgement["receipt_id"]),
                envelope_digest=str(request_fact["envelope_digest"]), storage_fact=None,
                observed_at=now,
            )
            self._phase("after_request_ack")
            reply_event = self._reply_event(row, actor, attempt_id, request_fact, now)
            if _sha256(reply_event.idempotency_key) != row["reply_idempotency_digest"]:
                raise ConflictError("C0 pilot reply key binding conflicts")
            self.policy._require_c0_operation_in_transaction(
                connection, actor=actor, action="message.send", resource="direct",
                context=C0GuardedOperation(
                    attempt_id=attempt_id,
                    operation_scope="owner_to_fresh_send",
                    peer_harness_id=str(row["fresh_harness_id"]),
                    classification=Classification.C0_PUBLIC,
                    payload_digest=str(row["reply_payload_digest"]),
                    event_id=reply_event.event_id,
                    causal_parent_event_id=request_event.event_id,
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            accepted = self.mailbox._accept_in_transaction(connection, reply_event, now=now)
            self._phase("after_reply_accept")
            use = connection.execute(
                """UPDATE c0_plan_guards SET reply_remaining_uses=0
                    WHERE guard_id=? AND state='active' AND reply_remaining_uses=1""",
                (row["guard_id"],),
            )
            if use.rowcount != 1:
                raise RetryableConflictError("C0 pilot reply use raced")
            self._insert_fact(
                connection, attempt_id=attempt_id, fact_kind="reply_sent",
                issuer_kind="harness", issuer_harness_id=actor.harness_id,
                event_id=str(accepted["event_id"]), receipt_id=None,
                envelope_digest=str(accepted["envelope_digest"]), storage_fact=None,
                observed_at=now,
            )
            self._insert_fact(
                connection, attempt_id=attempt_id, fact_kind="reply_durable_custody",
                issuer_kind="accepting_core", issuer_harness_id=None,
                event_id=str(accepted["event_id"]), receipt_id=str(accepted["receipt_id"]),
                envelope_digest=str(accepted["envelope_digest"]),
                storage_fact=str(accepted["fact"]), observed_at=now,
            )
            self.store.append_audit(
                connection, {"action": "c0_pilot.reply_accepted", "attempt_id": attempt_id}
            )
            self._phase("before_respond_commit")
            return c0_result("waiting_fresh")

    def _require_exact_evidence(self, connection: Any, row: Any, attempt_id: str) -> None:
        facts = {
            str(fact["fact_kind"]): fact
            for fact in connection.execute(
                "SELECT * FROM c0_pilot_facts WHERE attempt_id=?", (attempt_id,)
            ).fetchall()
        }
        if set(facts) != set(C0_REQUIRED_FACTS):
            raise AuthorizationError("C0 pilot evidence is incomplete")
        for fact in facts.values():
            expected_evidence = {
                "schema": "agentnet.c0-pilot.fact-evidence.v1",
                "fact_kind": fact["fact_kind"],
                "issuer_kind": fact["issuer_kind"],
                "issuer_harness_id": fact["issuer_harness_id"],
                "event_id": fact["event_id"],
                "receipt_id": fact["receipt_id"],
                "envelope_digest": fact["envelope_digest"],
                "storage_fact": fact["storage_fact"],
            }
            if fact["evidence_json"] != canonical_json(expected_evidence).decode("utf-8"):
                raise AuthorizationError("C0 pilot fact evidence is invalid")
        request = self._require_custody_event(
            connection,
            row=row,
            attempt_id=attempt_id,
            fact_kind="request_durable_custody",
        )
        reply = self._require_custody_event(
            connection,
            row=row,
            attempt_id=attempt_id,
            fact_kind="reply_durable_custody",
        )
        request_kinds = (
            "request_retrieved", "request_recipient_acknowledged"
        )
        reply_kinds = ("reply_sent", "reply_retrieved", "reply_final_acknowledged")
        if any(
            facts[kind]["event_id"] != request["event_id"]
            or facts[kind]["envelope_digest"] != request["envelope_digest"]
            or facts[kind]["issuer_harness_id"] != row["owner_harness_id"]
            for kind in request_kinds
        ):
            raise AuthorizationError("C0 pilot request evidence binding is invalid")
        if (
            facts["reply_sent"]["event_id"] != reply["event_id"]
            or facts["reply_sent"]["envelope_digest"] != reply["envelope_digest"]
            or facts["reply_sent"]["issuer_harness_id"] != row["owner_harness_id"]
            or any(
                facts[kind]["event_id"] != reply["event_id"]
                or facts[kind]["envelope_digest"] != reply["envelope_digest"]
                or facts[kind]["issuer_harness_id"] != row["fresh_harness_id"]
                for kind in ("reply_retrieved", "reply_final_acknowledged")
            )
        ):
            raise AuthorizationError("C0 pilot reply evidence binding is invalid")
        request_row = self._event_row(
            connection, event_id=str(request["event_id"]), recipient_id=str(row["owner_harness_id"])
        )
        request_event, _request_payload = self.mailbox._validated_event_and_payload(
            request_row, connection=connection
        )
        reply_row = self._event_row(
            connection, event_id=str(reply["event_id"]), recipient_id=str(row["fresh_harness_id"])
        )
        reply_event, _reply_payload = self.mailbox._validated_event_and_payload(
            reply_row, connection=connection
        )
        for kind in ("request_recipient_acknowledged", "reply_final_acknowledged"):
            fact = facts[kind]
            receipt = connection.execute(
                """SELECT fact,event_digest,recipient_id,owner_actor_json FROM receipts
                    WHERE receipt_id=? AND event_id=?""",
                (fact["receipt_id"], fact["event_id"]),
            ).fetchone()
            expected_recipient = (
                row["owner_harness_id"] if kind.startswith("request_") else row["fresh_harness_id"]
            )
            expected_owner = (
                reply_event.actor if kind.startswith("request_") else request_event.actor
            )
            try:
                receipt_owner = VerifiedActor.model_validate_json(
                    receipt["owner_actor_json"] if receipt is not None else ""
                )
            except Exception as exc:
                raise AuthorizationError("C0 pilot acknowledgement receipt owner is invalid") from exc
            if (
                receipt is None
                or receipt["fact"] != DeliveryFact.RECIPIENT_COMMITTED.value
                or receipt["event_digest"] != fact["envelope_digest"]
                or receipt["recipient_id"] != expected_recipient
                or receipt_owner != expected_owner
                or canonical_json(receipt_owner.audit_view()).decode("utf-8")
                != receipt["owner_actor_json"]
            ):
                raise AuthorizationError("C0 pilot acknowledgement receipt is invalid")
        if reply_event.causal_parent_ids != (request["event_id"],):
            raise AuthorizationError("C0 pilot reply lineage is invalid")

    def _require_terminal_cleanup(self, connection: Any, row: Any) -> None:
        attempt_id = str(row["attempt_id"] or "")
        if (
            not attempt_id
            or row["attempt_state"] != "communication_revoked"
            or row["guard_state"] != "revoked"
            or row["sanitized_result"] != C0_PILOT_SUCCESS
            or int(row["request_remaining_uses"]) != 0
            or int(row["reply_remaining_uses"]) != 0
        ):
            raise ConflictError("C0 pilot terminal evidence is inconsistent")
        self._require_exact_evidence(connection, row, attempt_id)
        counts = connection.execute(
            """SELECT i.item_kind,
                      SUM(CASE WHEN e.revoked_at IS NULL THEN 0 ELSE 1 END) AS revoked_count,
                      COUNT(*) AS item_count
                 FROM bootstrap_grant_plan_items i
                 JOIN entitlements e ON e.entitlement_id=i.entitlement_id
                WHERE i.plan_id=? GROUP BY i.item_kind""",
            (row["plan_id"],),
        ).fetchall()
        values = {
            str(item["item_kind"]): (int(item["revoked_count"]), int(item["item_count"]))
            for item in counts
        }
        if values != {"communication": (5, 5), "exact_revoke": (0, 5)}:
            raise ConflictError("C0 pilot terminal cleanup is inconsistent")

    def complete(self, *, actor: VerifiedActor) -> dict[str, str]:
        self._require_actor(actor)
        now = int(self.clock())
        with self.store.transaction(immediate=True) as connection:
            row = self._latest_plan(connection, actor)
            if not self._require_current_bindings(connection, row, actor, now):
                return c0_result("invalidated")
            if row["guard_state"] == "invalidated":
                return c0_result("invalidated")
            if actor.harness_id != row["fresh_harness_id"]:
                raise AuthorizationError("only exact fresh harness may complete C0 pilot")
            if row["attempt_state"] == "communication_revoked":
                self._require_terminal_cleanup(connection, row)
                return c0_result(C0_PILOT_SUCCESS)
            if self._expire_if_due(connection, row, now):
                return c0_result("expired")
            attempt_id = str(row["attempt_id"] or "")
            if not attempt_id or row["guard_state"] != "active" or row["attempt_state"] != "active":
                raise AuthorizationError("C0 pilot reply is not active")
            reply_fact = self._fact(
                connection, attempt_id=attempt_id, fact_kind="reply_durable_custody"
            )
            if reply_fact is None:
                raise AuthorizationError("C0 pilot reply evidence is absent")
            reply_row = self._event_row(
                connection,
                event_id=str(reply_fact["event_id"]),
                recipient_id=str(row["fresh_harness_id"]),
            )
            self.policy._require_c0_operation_in_transaction(
                connection, actor=actor, action="mailbox.read",
                resource=str(row["fresh_harness_id"]),
                context=C0GuardedOperation(
                    attempt_id=attempt_id, operation_scope="fresh_mailbox_read",
                    peer_harness_id=None, classification=Classification.C0_PUBLIC,
                    event_id=str(reply_fact["event_id"]),
                    envelope_digest=str(reply_fact["envelope_digest"]),
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            reply_event, _payload = self.mailbox._validated_event_and_payload(
                reply_row, connection=connection
            )
            if (
                reply_event.actor.harness_id != row["owner_harness_id"]
                or reply_event.recipients != (row["fresh_harness_id"],)
                or reply_event.classification is not Classification.C0_PUBLIC
                or reply_event.payload_digest != _bootstrap_c0_payload_digest(row, "reply")
            ):
                raise AuthorizationError("C0 pilot reply event binding is invalid")
            self._insert_fact(
                connection, attempt_id=attempt_id, fact_kind="reply_retrieved",
                issuer_kind="harness", issuer_harness_id=actor.harness_id,
                event_id=reply_event.event_id, receipt_id=None,
                envelope_digest=str(reply_fact["envelope_digest"]), storage_fact=None,
                observed_at=now,
            )
            self.policy._require_c0_operation_in_transaction(
                connection, actor=actor, action="mailbox.acknowledge",
                resource=str(row["fresh_harness_id"]),
                context=C0GuardedOperation(
                    attempt_id=attempt_id,
                    operation_scope="fresh_mailbox_acknowledge",
                    peer_harness_id=None, classification=Classification.C0_PUBLIC,
                    event_id=reply_event.event_id,
                    envelope_digest=str(reply_fact["envelope_digest"]),
                ),
                when=datetime.fromtimestamp(now, UTC),
            )
            acknowledgement = self._acknowledge_event(
                connection,
                row=row,
                actor=actor,
                event_id=reply_event.event_id,
                recipient_id=str(row["fresh_harness_id"]),
                envelope_digest=str(reply_fact["envelope_digest"]),
                now=now,
            )
            self._insert_fact(
                connection, attempt_id=attempt_id,
                fact_kind="reply_final_acknowledged", issuer_kind="harness",
                issuer_harness_id=actor.harness_id, event_id=reply_event.event_id,
                receipt_id=str(acknowledgement["receipt_id"]),
                envelope_digest=str(reply_fact["envelope_digest"]), storage_fact=None,
                observed_at=now,
            )
            self._phase("after_final_ack")
            self._require_exact_evidence(connection, row, attempt_id)
            changed = connection.execute(
                """UPDATE c0_pilot_attempts SET state='evidence_complete',evidence_completed_at=?
                    WHERE attempt_id=? AND state='active'""",
                (now, attempt_id),
            )
            if changed.rowcount != 1:
                raise RetryableConflictError("C0 pilot evidence completion raced")
            self._phase("after_evidence_complete")
            targets = self.policy._require_c0_cleanup_in_transaction(
                connection, actor=actor, attempt_id=attempt_id,
                when=datetime.fromtimestamp(now, UTC),
            )
            updated = 0
            for entitlement_id, revision in targets:
                cursor = connection.execute(
                    """UPDATE entitlements SET revoked_at=?
                        WHERE entitlement_id=? AND revision=? AND revoked_at IS NULL""",
                    (now, entitlement_id, revision),
                )
                updated += cursor.rowcount
                self._phase(f"after_cleanup_revoke_{updated}")
            if updated != 5:
                raise ConflictError("C0 pilot cleanup was not exact")
            guard = connection.execute(
                "UPDATE c0_plan_guards SET state='revoked' WHERE guard_id=? AND state='active'",
                (row["guard_id"],),
            )
            attempt = connection.execute(
                """UPDATE c0_pilot_attempts
                      SET state='communication_revoked',communication_revoked_at=?,terminal_at=?,sanitized_result=?
                    WHERE attempt_id=? AND state='evidence_complete'""",
                (now, now, C0_PILOT_SUCCESS, attempt_id),
            )
            if guard.rowcount != 1 or attempt.rowcount != 1:
                raise RetryableConflictError("C0 pilot cleanup terminalization raced")
            self.store.append_audit(
                connection,
                {
                    "action": "c0_pilot.completed",
                    "attempt_id": attempt_id,
                    "communication_entitlements_revoked": 5,
                    "result": C0_PILOT_SUCCESS,
                },
            )
            self._phase("before_complete_commit")
            return c0_result(C0_PILOT_SUCCESS)

    def readiness(self, *, actor: VerifiedActor) -> dict[str, str]:
        """Validate exact current signed actor without requiring plan creation."""

        self._require_actor(actor)
        now = int(self.clock())
        with self.store.transaction() as connection:
            binding = load_credential_binding_from_connection(
                connection, str(actor.credential_id)
            )
            binding.require_active(now=now)
            if (
                binding.domain_id != actor.domain_id
                or binding.principal_id != actor.principal_id
                or binding.harness_id != actor.harness_id
                or binding.credential_epoch != actor.credential_epoch
            ):
                raise AuthorizationError("C0 responder actor binding is not current")
            count = connection.execute(
                """SELECT COUNT(*) AS n FROM bootstrap_grant_plans
                    WHERE state='committed' AND domain_id=? AND principal_id=?
                      AND (owner_harness_id=? OR fresh_harness_id=?)""",
                (actor.domain_id, actor.principal_id, actor.harness_id, actor.harness_id),
            ).fetchone()
        return {
            "schema": "agentnet.c0-pilot.readiness-result.v1",
            "status": "ready" if count is not None and int(count["n"]) > 0 else "waiting_plan",
        }

    def status(self, *, actor: VerifiedActor) -> dict[str, str]:
        self._require_actor(actor)
        now = int(self.clock())
        with self.store.transaction(immediate=True) as connection:
            row = self._latest_plan(connection, actor)
            if not self._require_current_bindings(connection, row, actor, now):
                return c0_result("invalidated")
            if row["guard_state"] == "invalidated":
                return c0_result("invalidated")
            if row["attempt_state"] == "communication_revoked":
                self._require_terminal_cleanup(connection, row)
                return c0_result(C0_PILOT_SUCCESS)
            if self._expire_if_due(connection, row, now):
                return c0_result("expired")
            if row["attempt_id"] is None:
                return c0_result("prepared_unusable")
            if row["attempt_state"] == "failed":
                return c0_result("failed")
            if row["attempt_state"] != "active" or row["guard_state"] != "active":
                raise ConflictError("C0 pilot durable state is inconsistent")
            attempt_id = str(row["attempt_id"])
            if self._fact(connection, attempt_id=attempt_id, fact_kind="reply_durable_custody"):
                return c0_result("waiting_fresh")
            if self._fact(connection, attempt_id=attempt_id, fact_kind="request_durable_custody"):
                return c0_result("waiting_owner")
            raise ConflictError("C0 pilot durable state is inconsistent")


__all__ = ["C0PilotService"]
