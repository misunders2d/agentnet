"""Durable communication approval and collaboration-scope lifecycles."""

from __future__ import annotations

import hashlib
import json
import secrets
import time
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any, Literal

from agentnet.approval.service import IndependentApprovalVerifier, consume_independent_approval
from agentnet.authorization.bootstrap_plan_service import ExactBootstrapHarnessResolver, HarnessResolver
from agentnet.authorization.evidence import IssuanceAuthority, require_current_authority_decision
from agentnet.authorization.policy import validate_actor_state
from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    COMMUNICATION_SCOPE_PROFILE,
    COMMUNICATION_SCOPE_RESTRICTIONS,
    CommunicationScopeBeginRequest,
    CommunicationScopeBeginResult,
    CommunicationScopeCompleteRequest,
    CommunicationScopeCompleteResult,
    CommunicationScopeStatusRequest,
    CommunicationScopeStatusResult,
    build_communication_scope_transaction,
    digest_canonical,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    GateBlocked,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.communication_scope_schema import COMMUNICATION_SCOPE_TABLE_DDL
from agentnet.storage.release_v7_schema import materialize_v6_communication_scope
from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator


class CommunicationScopeTerminalError(Exception):
    """The exact caller-bound scope reached an irreversible terminal state."""


class _FinalCommitExpired(Exception):
    pass


class ExactCommunicationHarnessResolver(ExactBootstrapHarnessResolver):
    """Resolve a fresh peer for the exact authenticated owner server harness."""
    allow_current_credential_rotation = True
    require_fresh_enrollment = False


    def __init__(
        self,
        store: StoreBackend,
        approval_verifier: IndependentApprovalVerifier,
        *,
        owner_harness_id: str | None,
        fresh_max_age_seconds: int = 900,
    ) -> None:
        super().__init__(
            store,
            approval_verifier,
            fresh_max_age_seconds=fresh_max_age_seconds,
            authenticated_role="enrolled_server",
        )
        self.owner_harness_id = owner_harness_id

    def _select_authenticated_rows(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        now: int,
        rows: list[Any],
    ) -> list[Any]:
        pairs = connection.execute(
            """SELECT DISTINCT p.owner_harness_id,p.fresh_harness_id
            FROM c0_pilot_attempts a
            JOIN bootstrap_grant_plans p ON p.plan_id=a.plan_id
            JOIN c0_plan_guards g ON g.guard_id=a.guard_id AND g.plan_id=p.plan_id
            JOIN harnesses owner ON owner.harness_id=p.owner_harness_id
            JOIN credentials owner_credential
              ON owner_credential.harness_id=owner.harness_id
             AND owner_credential.epoch=owner.credential_epoch
            JOIN harnesses fresh ON fresh.harness_id=p.fresh_harness_id
            JOIN credentials fresh_credential
              ON fresh_credential.harness_id=fresh.harness_id
             AND fresh_credential.epoch=fresh.credential_epoch
            WHERE p.domain_id=? AND p.principal_id=? AND p.owner_harness_id=?
              AND p.state='committed'
              AND a.state='communication_revoked'
              AND a.sanitized_result='COMPLETED_C0_ROUND_TRIP'
              AND a.evidence_completed_at IS NOT NULL
              AND a.communication_revoked_at IS NOT NULL
              AND a.terminal_at IS NOT NULL
              AND g.state='revoked'
              AND g.request_remaining_uses=0 AND g.reply_remaining_uses=0
              AND owner.status='active' AND fresh.status='active'
              AND owner.domain_id=p.domain_id AND fresh.domain_id=p.domain_id
              AND owner.principal_id=p.principal_id AND fresh.principal_id=p.principal_id
              AND owner_credential.status='active'
              AND fresh_credential.status='active'
              AND owner_credential.not_before<=? AND owner_credential.expires_at>?
              AND fresh_credential.not_before<=? AND fresh_credential.expires_at>?
            ORDER BY p.owner_harness_id,p.fresh_harness_id""",
            (
                actor.domain_id,
                actor.principal_id,
                actor.harness_id,
                now,
                now,
                now,
                now,
            ),
        ).fetchall()
        if len(pairs) != 1:
            raise ConflictError(
                "communication scope requires exactly one completed C0 harness pair"
            )
        enrollment_by_harness = {
            self._enrollment_harness_id(row): row
            for row in rows
        }
        pair = pairs[0]
        try:
            owner_row = enrollment_by_harness[str(pair["owner_harness_id"])]
            fresh_row = enrollment_by_harness[str(pair["fresh_harness_id"])]
        except KeyError as exc:
            raise ConflictError(
                "completed C0 harness pair lacks guided enrollment evidence"
            ) from exc
        return [owner_row, fresh_row]

    def __call__(
        self,
        connection: Any,
        actor: VerifiedActor,
        now: int,
    ) -> dict[str, Any]:
        if actor.harness_id != self.owner_harness_id:
            raise AuthorizationError("communication scope denied")
        return super().__call__(connection, actor, now)


def _hash_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _actor_binding(actor: VerifiedActor) -> str:
    return canonical_json(actor.model_dump(mode="json")).decode("utf-8")


def _strict_canonical_object(raw: bytes) -> dict[str, Any]:
    def pairs(values: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in values:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result

    value = json.loads(raw.decode("utf-8"), object_pairs_hook=pairs)
    if not isinstance(value, dict) or canonical_json(value) != raw:
        raise ValueError("value is not a canonical object")
    return value


class CommunicationScopeService:
    """Reserve, independently approve, and atomically issue one persistent scope."""

    def __init__(
        self,
        store: StoreBackend,
        approval_client: Any,
        approval_verifier: IndependentApprovalVerifier,
        *,
        resolver: HarnessResolver,
        public_approval_url: str,
        clock: Callable[[], int],
    ) -> None:
        if getattr(approval_verifier, "lab_only", True) or getattr(
            approval_verifier, "assurance", ""
        ) != "independent_webauthn_uv":
            raise GateBlocked(
                "communication_scope",
                "persistent communication scope requires independent WebAuthn approval",
            )
        if not public_approval_url.startswith("https://") or not public_approval_url.endswith("/approval"):
            raise ValueError("public Approval URL must be exact HTTPS /approval")
        self.store = store
        self.approval_client = approval_client
        self.approval_verifier = approval_verifier
        self.resolver = resolver
        self.public_approval_url = public_approval_url
        self.clock = clock

    @staticmethod
    def _require_actor(actor: VerifiedActor) -> None:
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.credential_epoch < 1
            or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        ):
            raise AuthorizationError("communication scope denied")

    @staticmethod
    def _row_for_begin(connection: Any, key_hash: str) -> Any:
        return connection.execute(
            "SELECT * FROM communication_scopes WHERE begin_idempotency_key_sha256=?",
            (key_hash,),
        ).fetchone()

    @staticmethod
    def _require_row_actor(row: Any, actor: VerifiedActor) -> None:
        if row is None or not secrets.compare_digest(str(row["actor_binding_json"]), _actor_binding(actor)):
            raise ConflictError("communication scope idempotency conflict")

    @staticmethod
    def _same_harness_binding(row: Any, actor: VerifiedActor) -> bool:
        try:
            bound = VerifiedActor.model_validate_json(str(row["actor_binding_json"]))
        except Exception:
            return False
        return (
            bound.kind is actor.kind
            and bound.domain_id == actor.domain_id
            and bound.principal_id == actor.principal_id
            and bound.harness_id == actor.harness_id
        )

    @staticmethod
    def _require_current_actor_state(
        connection: Any, *, actor: VerifiedActor, now: int
    ) -> None:
        domain = connection.execute(
            "SELECT status,policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        if domain is None or domain["status"] != "active":
            raise AuthorizationError("communication scope denied")
        denial, _revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(domain["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None:
            raise AuthorizationError("communication scope denied")

    def _begin_storage(self, row: Any) -> tuple[str | None, dict[str, Any] | None]:
        encrypted = row["begin_response_encrypted"]
        if not encrypted:
            return None, None
        value = self.store.cipher.decrypt_json(
            encrypted, purpose=f"communication-scope-begin:{row['scope_id']}"
        )
        if (
            not isinstance(value, dict)
            or set(value) != {"schema", "approval_possession_secret", "result"}
            or value.get("schema") != "agentnet.communication-scope.begin-storage.v1"
        ):
            raise GateBlocked("communication_scope", "communication scope reservation is invalid")
        possession = value.get("approval_possession_secret")
        if (
            not isinstance(possession, str)
            or not 32 <= len(possession) <= 128
            or any(ord(character) < 0x21 or ord(character) > 0x7E for character in possession)
        ):
            raise GateBlocked("communication_scope", "communication scope reservation is invalid")
        result_value = value.get("result")
        result = None if result_value is None else CommunicationScopeBeginResult.model_validate(
            result_value
        ).model_dump(by_alias=True)
        return possession, result

    def _encrypt_begin_storage(
        self, row: Any, *, possession_secret: str, result: dict[str, Any] | None
    ) -> str:
        return self.store.cipher.encrypt_json(
            {
                "schema": "agentnet.communication-scope.begin-storage.v1",
                "approval_possession_secret": possession_secret,
                "result": result,
            },
            purpose=f"communication-scope-begin:{row['scope_id']}",
        )

    def _stored_begin(self, row: Any) -> dict[str, Any]:
        _possession, result = self._begin_storage(row)
        if result is None:
            raise GateBlocked("communication_scope", "communication scope reservation is incomplete")
        return result

    def _stored_complete(self, row: Any, reservation_digest: str) -> dict[str, Any]:
        if not secrets.compare_digest(str(row["completion_request_digest"] or ""), reservation_digest):
            raise ConflictError("communication scope completion conflict")
        encrypted = row["committed_result_encrypted"]
        expected = row["committed_result_digest"]
        if not encrypted or not expected:
            raise GateBlocked("communication_scope", "communication scope result is unavailable")
        value = self.store.cipher.decrypt_json(
            encrypted, purpose=f"communication-scope-result:{row['scope_id']}"
        )
        if not secrets.compare_digest(digest_canonical(value), str(expected)):
            raise AuthenticationError("communication scope stored result denied")
        return CommunicationScopeCompleteResult.model_validate(value).model_dump(by_alias=True)

    @staticmethod
    def _require_stored_transaction(row: Any) -> bytes:
        try:
            preimage = _strict_canonical_object(
                str(row["canonical_scope_preimage_json"]).encode("utf-8")
            )
            stored_bytes = str(row["final_approval_transaction_json"]).encode(
                "utf-8"
            )
            stored = _strict_canonical_object(stored_bytes)
            rebuilt = build_communication_scope_transaction(preimage)
        except Exception as exc:
            raise AuthenticationError("communication scope transaction denied") from exc
        if (
            not secrets.compare_digest(canonical_json(rebuilt), stored_bytes)
            or stored != rebuilt
            or rebuilt["scope_id"] != row["scope_id"]
            or rebuilt["scope_digest"] != row["scope_digest"]
            or not secrets.compare_digest(
                hashlib.sha256(stored_bytes).hexdigest(),
                str(row["transaction_digest"]),
            )
        ):
            raise AuthenticationError("communication scope transaction denied")
        return stored_bytes

    @staticmethod
    def _approval_create_digest(
        *, key: str, principal_id: str, domain_id: str, transaction_digest: str
    ) -> str:
        return digest_canonical(
            {
                "schema": "agentnet.approval.core-request.v1",
                "idempotency_key": key,
                "approver_principal_id": principal_id,
                "domain_id": domain_id,
                "approval_purpose": COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
                "transaction_digest": transaction_digest,
            }
        )

    @staticmethod
    def _stored_resolution(preimage: dict[str, Any]) -> dict[str, Any]:
        return {
            "domain": preimage.get("domain"),
            "principal": preimage.get("principal"),
            "harnesses": preimage.get("harnesses"),
            "enrollment_evidence": preimage.get("enrollment_evidence"),
        }

    def _require_current_resolution(
        self, connection: Any, *, row: Any, actor: VerifiedActor, now: int
    ) -> dict[str, Any]:
        try:
            preimage = _strict_canonical_object(
                str(row["canonical_scope_preimage_json"]).encode("utf-8")
            )
            resolved = self.resolver(connection, actor, now)
        except (AuthorizationError, ConflictError):
            raise
        except Exception as exc:
            raise AuthenticationError("communication scope identity recheck denied") from exc
        if (
            preimage.get("approval_expires_at") != row["approval_expires_at"]
            or preimage.get("authority_expires_at") is not None
            or row["authority_expires_at"] is not None
            or not secrets.compare_digest(
                digest_canonical(preimage), str(row["scope_digest"])
            )
            or not secrets.compare_digest(
                canonical_json(self._stored_resolution(preimage)),
                canonical_json(resolved),
            )
        ):
            raise AuthenticationError("communication scope identity recheck denied")
        return preimage

    def _require_committed_current(
        self, connection: Any, *, row: Any, actor: VerifiedActor, now: int
    ) -> None:
        denied = AuthenticationError("communication scope current authority denied")
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (row["domain_id"],),
        ).fetchone()
        principal = connection.execute(
            "SELECT status FROM principals WHERE domain_id=? AND principal_id=?",
            (row["domain_id"], row["principal_id"]),
        ).fetchone()
        if (
            domain is None
            or principal is None
            or domain["status"] != "active"
            or principal["status"] != "active"
            or int(domain["policy_revision"]) != int(row["policy_revision"])
            or int(domain["revocation_epoch"]) != int(row["domain_revocation_epoch"])
            or row["authority_expires_at"] is not None
        ):
            raise denied

        harness_ids = {
            str(row["owner_harness_id"]),
            str(row["fresh_harness_id"]),
        }
        for harness_id in harness_ids:
            harness = connection.execute(
                """SELECT status,binding_assurance,credential_epoch
                     FROM harnesses
                    WHERE harness_id=? AND domain_id=? AND principal_id=?""",
                (harness_id, row["domain_id"], row["principal_id"]),
            ).fetchone()
            if (
                harness is None
                or harness["status"] != "active"
                or harness["binding_assurance"]
                not in {"os_bound", "hardware_bound"}
                or int(harness["credential_epoch"]) < 1
            ):
                raise denied
        if actor.harness_id not in harness_ids:
            raise denied
        current_actor = connection.execute(
            """SELECT h.status AS harness_status,h.binding_assurance,
                      h.credential_epoch,c.status AS credential_status,
                      c.epoch,c.not_before,c.expires_at
                 FROM harnesses h JOIN credentials c ON c.harness_id=h.harness_id
                WHERE h.harness_id=? AND h.domain_id=? AND h.principal_id=?
                  AND c.credential_id=?""",
            (
                actor.harness_id,
                row["domain_id"],
                row["principal_id"],
                actor.credential_id,
            ),
        ).fetchone()
        if (
            current_actor is None
            or current_actor["harness_status"] != "active"
            or current_actor["credential_status"] != "active"
            or current_actor["binding_assurance"] != actor.binding_assurance
            or int(current_actor["credential_epoch"]) != actor.credential_epoch
            or int(current_actor["epoch"]) != actor.credential_epoch
            or int(current_actor["not_before"]) > now
            or now >= int(current_actor["expires_at"])
        ):
            raise denied

        items = connection.execute(
            """SELECT i.harness_id,i.action AS item_action,
                      i.resource_pattern AS item_resource,
                      i.expires_at AS item_expires_at,
                      e.action AS entitlement_action,
                      e.resource_pattern AS entitlement_resource,
                      e.expires_at AS entitlement_expires_at,
                      e.revoked_at,e.revision,e.domain_id,e.principal_id
                 FROM communication_scope_items i
                 JOIN entitlements e ON e.entitlement_id=i.entitlement_id
                WHERE i.scope_id=?""",
            (row["scope_id"],),
        ).fetchall()
        expected = {
            (harness_id, action)
            for harness_id in harness_ids
            for action in COMMUNICATION_SCOPE_ACTIONS
        }
        seen: set[tuple[str, str]] = set()
        for item in items:
            binding = (str(item["harness_id"]), str(item["item_action"]))
            seen.add(binding)
            if (
                item["item_action"] != item["entitlement_action"]
                or item["item_resource"] != "*"
                or item["entitlement_resource"] != "*"
                or item["item_expires_at"] is not None
                or item["entitlement_expires_at"] is not None
                or item["revoked_at"] is not None
                or int(item["revision"]) != int(row["policy_revision"])
                or item["domain_id"] != row["domain_id"]
                or item["principal_id"] != row["principal_id"]
            ):
                raise denied
        if len(items) != 2 * len(COMMUNICATION_SCOPE_ACTIONS) or seen != expected:
            raise denied

    @staticmethod
    def _active_scope_conflict(
        connection: Any,
        *,
        domain_id: str,
        principal_id: str,
        exclude_scope_id: str | None = None,
    ) -> bool:
        exclusion = "" if exclude_scope_id is None else " AND scope_id<>?"
        params: tuple[Any, ...] = (domain_id, principal_id, COMMUNICATION_SCOPE_PROFILE)
        if exclude_scope_id is not None:
            params += (exclude_scope_id,)
        row = connection.execute(
            """SELECT scope_id FROM communication_scopes
               WHERE domain_id=? AND principal_id=? AND profile=?
                 AND state IN ('reserved','pending_approval','approval_issued',
                               'completion_reserved','committed')"""
            + exclusion
            + " LIMIT 1",
            params,
        ).fetchone()
        return row is not None

    @staticmethod
    def _expire_if_due(connection: Any, *, row: Any, now: int) -> bool:
        if (
            row["state"]
            in {"reserved", "pending_approval", "approval_issued", "completion_reserved"}
            and int(row["approval_expires_at"]) <= now
        ):
            connection.execute(
                "UPDATE communication_scopes SET state='expired',terminal_at=? WHERE scope_id=?",
                (now, row["scope_id"]),
            )
            return True
        return False

    def begin(
        self, *, actor: VerifiedActor, request: CommunicationScopeBeginRequest
    ) -> dict[str, Any]:
        self._require_actor(actor)
        now = int(self.clock())
        key_hash = _hash_text(request.begin_idempotency_key)
        expired_existing = False
        row: Any | None = None
        with self.store.transaction() as connection:
            existing = self._row_for_begin(connection, key_hash)
            if existing is not None:
                if existing["state"] == "committed":
                    try:
                        self._require_row_actor(existing, actor)
                    except ConflictError:
                        if not self._same_harness_binding(existing, actor):
                            raise
                    self._require_committed_current(
                        connection, row=existing, actor=actor, now=now
                    )
                    return self._stored_begin(existing)
                try:
                    self._require_row_actor(existing, actor)
                except ConflictError:
                    if not self._same_harness_binding(existing, actor):
                        raise
                    self._require_current_actor_state(
                        connection, actor=actor, now=now
                    )
                    if existing["state"] in {
                        "rejected",
                        "canceled",
                        "expired",
                        "invalidated",
                    }:
                        expired_existing = True
                    else:
                        changed = connection.execute(
                            """UPDATE communication_scopes
                               SET state='invalidated',terminal_at=?
                               WHERE scope_id=? AND state IN (
                                   'reserved','pending_approval','approval_issued',
                                   'completion_reserved'
                               )""",
                            (now, existing["scope_id"]),
                        )
                        if changed.rowcount != 1:
                            raise ConflictError("communication scope state conflict")
                        expired_existing = True
                if not expired_existing:
                    if self._expire_if_due(connection, row=existing, now=now):
                        expired_existing = True
                    elif existing["state"] in {
                        "rejected",
                        "canceled",
                        "expired",
                        "invalidated",
                    }:
                        raise CommunicationScopeTerminalError(
                            "communication scope is terminal"
                        )
                    elif existing["state"] != "reserved":
                        return self._stored_begin(existing)
                    else:
                        row = existing
                        self._require_current_resolution(
                            connection, row=row, actor=actor, now=now
                        )
            if existing is None:
                resolved = self.resolver(connection, actor, now)
                if (
                    resolved.get("domain", {}).get("domain_id") != actor.domain_id
                    or resolved.get("principal", {}).get("principal_id")
                    != actor.principal_id
                ):
                    raise AuthorizationError("communication scope denied")
                connection.execute(
                    """UPDATE communication_scopes
                       SET state='expired',terminal_at=?
                       WHERE domain_id=? AND principal_id=? AND profile=?
                         AND state IN (
                             'reserved','pending_approval','approval_issued',
                             'completion_reserved'
                         )
                         AND approval_expires_at<=?""",
                    (
                        now,
                        actor.domain_id,
                        actor.principal_id,
                        COMMUNICATION_SCOPE_PROFILE,
                        now,
                    ),
                )
                if self._active_scope_conflict(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                ):
                    raise ConflictError("an active communication scope already exists")
                preimage = {
                    "schema": "agentnet.communication-scope.preimage.v1",
                    "approval_purpose": COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
                    "profile": COMMUNICATION_SCOPE_PROFILE,
                    "profile_version": 1,
                    "independent_boundary_proven": False,
                    "max_uses": 1,
                    "begin_idempotency_key_sha256": key_hash,
                    **resolved,
                    "actions": sorted(COMMUNICATION_SCOPE_ACTIONS),
                    "restrictions": dict(COMMUNICATION_SCOPE_RESTRICTIONS),
                    "issued_at": now,
                    "approval_expires_at": now + 3_600,
                    "authority_expires_at": None,
                }
                transaction = build_communication_scope_transaction(preimage)
                transaction_bytes = canonical_json(transaction)
                transaction_digest = hashlib.sha256(transaction_bytes).hexdigest()
                scope_id = transaction["scope_id"]
                create_key = f"core:communication-scope:create:{scope_id}"
                create_digest = self._approval_create_digest(
                    key=create_key,
                    principal_id=actor.principal_id or "",
                    domain_id=actor.domain_id,
                    transaction_digest=transaction_digest,
                )
                harnesses = resolved["harnesses"]
                domain = resolved["domain"]
                connection.execute(
                    """INSERT INTO communication_scopes(
                        scope_id,profile,profile_version,domain_id,principal_id,
                        owner_harness_id,fresh_harness_id,
                        owner_credential_id,fresh_credential_id,
                        owner_credential_epoch,fresh_credential_epoch,
                        domain_revocation_epoch,policy_revision,actor_binding_json,
                        canonical_scope_preimage_json,final_approval_transaction_json,
                        scope_digest,transaction_digest,begin_idempotency_key_sha256,
                        state,created_at,approval_expires_at,authority_expires_at,
                        approval_create_idempotency_key,approval_create_request_digest
                    ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'reserved',?,?,?,?,?)""",
                    (
                        scope_id,
                        COMMUNICATION_SCOPE_PROFILE,
                        1,
                        actor.domain_id,
                        actor.principal_id,
                        harnesses["owner"]["harness_id"],
                        harnesses["fresh"]["harness_id"],
                        harnesses["owner"]["credential_id"],
                        harnesses["fresh"]["credential_id"],
                        harnesses["owner"]["credential_epoch"],
                        harnesses["fresh"]["credential_epoch"],
                        domain["revocation_epoch"],
                        domain["policy_revision"],
                        _actor_binding(actor),
                        canonical_json(preimage).decode("utf-8"),
                        transaction_bytes.decode("utf-8"),
                        transaction["scope_digest"],
                        transaction_digest,
                        key_hash,
                        now,
                        now + 3_600,
                        None,
                        create_key,
                        create_digest,
                    ),
                )
                row = self._row_for_begin(connection, key_hash)
            if not expired_existing:
                if row is None:
                    raise GateBlocked(
                        "communication_scope",
                        "communication scope reservation is unavailable",
                    )
                possession_secret, _result = self._begin_storage(row)
                if possession_secret is None:
                    possession_secret = secrets.token_urlsafe(32)
                    connection.execute(
                        """UPDATE communication_scopes SET begin_response_encrypted=?
                           WHERE scope_id=? AND state='reserved'""",
                        (
                            self._encrypt_begin_storage(
                                row, possession_secret=possession_secret, result=None
                            ),
                            row["scope_id"],
                        ),
                    )
                    row = self._row_for_begin(connection, key_hash)
        if expired_existing:
            raise CommunicationScopeTerminalError("communication scope is terminal")
        if row is None:
            raise GateBlocked(
                "communication_scope",
                "communication scope reservation is unavailable",
            )

        possession_secret, _result = self._begin_storage(row)
        if possession_secret is None:
            raise GateBlocked(
                "communication_scope", "communication scope reservation is incomplete"
            )
        transaction_bytes = self._require_stored_transaction(row)
        expected_create_digest = self._approval_create_digest(
            key=str(row["approval_create_idempotency_key"]),
            principal_id=str(row["principal_id"]),
            domain_id=str(row["domain_id"]),
            transaction_digest=str(row["transaction_digest"]),
        )
        if not secrets.compare_digest(
            expected_create_digest, str(row["approval_create_request_digest"])
        ):
            raise AuthenticationError("communication scope transaction denied")
        created = self.approval_client.create_request(
            idempotency_key=str(row["approval_create_idempotency_key"]),
            domain_id=str(row["domain_id"]),
            approval_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
            canonical_transaction=transaction_bytes,
            transaction_digest=str(row["transaction_digest"]),
            possession_hash=_hash_text(possession_secret),
            request_expires_at=int(row["approval_expires_at"]),
        )
        if (
            not isinstance(created.get("request_id"), str)
            or created.get("transaction_digest") != row["transaction_digest"]
            or int(created.get("expires_at", 0)) != int(row["approval_expires_at"])
        ):
            raise AuthenticationError("approval service response denied")
        result = CommunicationScopeBeginResult(
            schema="agentnet.communication-scope.begin-result.v1",
            status="approval_pending",
            approval_url=self.public_approval_url,
            expires_at=int(row["approval_expires_at"]),
        ).model_dump(by_alias=True)
        with self.store.transaction() as connection:
            current = self._row_for_begin(connection, key_hash)
            self._require_row_actor(current, actor)
            if current["state"] == "reserved":
                connection.execute(
                    """UPDATE communication_scopes
                       SET approval_request_id=?,state='pending_approval',
                           begin_response_encrypted=?
                       WHERE scope_id=? AND state='reserved'""",
                    (
                        created["request_id"],
                        self._encrypt_begin_storage(
                            current,
                            possession_secret=possession_secret,
                            result=result,
                        ),
                        current["scope_id"],
                    ),
                )
            elif current["state"] != "pending_approval":
                raise ConflictError("communication scope state conflict")
            return self._stored_begin(self._row_for_begin(connection, key_hash))

    def status(
        self, *, actor: VerifiedActor, request: CommunicationScopeStatusRequest
    ) -> dict[str, Any]:
        self._require_actor(actor)
        now = int(self.clock())
        key_hash = _hash_text(request.begin_idempotency_key)
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, key_hash)
            if row is None:
                self._require_row_actor(row, actor)
            if row["state"] == "committed":
                self._require_committed_current(
                    connection, row=row, actor=actor, now=now
                )
                return self._stored_complete(
                    row, str(row["completion_request_digest"] or "")
                )
            self._require_row_actor(row, actor)
            if row["state"] in {
                "rejected",
                "canceled",
                "expired",
                "invalidated",
            }:
                return CommunicationScopeStatusResult(
                    schema="agentnet.communication-scope.status-result.v1",
                    status=row["state"],
                ).model_dump(by_alias=True, exclude_none=True)
            if self._expire_if_due(connection, row=row, now=now):
                return CommunicationScopeStatusResult(
                    schema="agentnet.communication-scope.status-result.v1",
                    status="expired",
                ).model_dump(by_alias=True, exclude_none=True)
            self._require_current_resolution(
                connection, row=row, actor=actor, now=now
            )
            request_id = str(row["approval_request_id"])
            transaction_digest = str(row["transaction_digest"])
            approval_expires_at = int(row["approval_expires_at"])
        remote = self.approval_client.request_status(
            request_id=request_id, transaction_digest=transaction_digest
        )
        if (
            remote.get("request_id") != request_id
            or remote.get("transaction_digest") != transaction_digest
            or remote.get("expires_at") != approval_expires_at
        ):
            raise AuthenticationError("approval service response denied")
        mapping = {
            "pending": ("pending_approval", "approval_pending"),
            "issued": ("approval_issued", "approval_ready"),
            "rejected": ("rejected", "rejected"),
            "canceled": ("canceled", "canceled"),
            "expired": ("expired", "expired"),
        }
        try:
            local_state, public_state = mapping[str(remote["state"])]
        except (KeyError, TypeError) as exc:
            raise AuthenticationError("approval service response denied") from exc
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, key_hash)
            self._require_row_actor(row, actor)
            current_state = str(row["state"])
            if local_state == "pending_approval":
                if current_state != "pending_approval":
                    raise AuthenticationError("approval service state regressed")
            elif local_state == "approval_issued":
                if current_state == "pending_approval":
                    connection.execute(
                        """UPDATE communication_scopes
                           SET state='approval_issued',approval_issued_at=?
                           WHERE scope_id=?""",
                        (int(self.clock()), row["scope_id"]),
                    )
                elif current_state not in {
                    "approval_issued",
                    "completion_reserved",
                }:
                    raise ConflictError("communication scope state conflict")
            elif local_state in {"rejected", "canceled"}:
                if current_state != "pending_approval":
                    raise AuthenticationError("approval service state regressed")
                connection.execute(
                    """UPDATE communication_scopes SET state=?,terminal_at=?
                       WHERE scope_id=?""",
                    (local_state, int(self.clock()), row["scope_id"]),
                )
            elif local_state == "expired":
                if current_state not in {
                    "pending_approval",
                    "approval_issued",
                    "completion_reserved",
                }:
                    raise ConflictError("communication scope state conflict")
                connection.execute(
                    """UPDATE communication_scopes SET state='expired',terminal_at=?
                       WHERE scope_id=?""",
                    (int(self.clock()), row["scope_id"]),
                )
        if public_state in {"rejected", "canceled", "expired"}:
            return CommunicationScopeStatusResult(
                schema="agentnet.communication-scope.status-result.v1",
                status=public_state,
            ).model_dump(by_alias=True, exclude_none=True)
        values: dict[str, Any] = {
            "schema": "agentnet.communication-scope.status-result.v1",
            "status": public_state,
            "approval_url": self.public_approval_url,
            "expires_at": approval_expires_at,
        }
        if public_state == "approval_ready":
            values["next_action"] = "complete_automatically"
        return CommunicationScopeStatusResult.model_validate(values).model_dump(
            by_alias=True, exclude_none=True
        )

    def complete(
        self, *, actor: VerifiedActor, request: CommunicationScopeCompleteRequest
    ) -> dict[str, Any]:
        self._require_actor(actor)
        now = int(self.clock())
        begin_hash = _hash_text(request.begin_idempotency_key)
        completion_hash = _hash_text(request.completion_idempotency_key)
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, begin_hash)
            if row is None:
                self._require_row_actor(row, actor)
            if self._expire_if_due(connection, row=row, now=now):
                raise CommunicationScopeTerminalError(
                    "communication scope is terminal"
                )
            reservation = {
                "schema": "agentnet.communication-scope.completion-reservation.v1",
                "scope_id": row["scope_id"],
                "begin_idempotency_key_sha256": begin_hash,
                "completion_idempotency_key_sha256": completion_hash,
                "approval_request_id": row["approval_request_id"],
                "approval_purpose": COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
                "transaction_digest": row["transaction_digest"],
            }
            reservation_digest = digest_canonical(reservation)
            if row["state"] == "committed":
                self._require_committed_current(
                    connection, row=row, actor=actor, now=now
                )
                materialize_v6_communication_scope(
                    connection,
                    scope_id=str(row["scope_id"]),
                )
                return self._stored_complete(row, reservation_digest)
            self._require_row_actor(row, actor)
            if row["state"] in {
                "rejected",
                "canceled",
                "expired",
                "invalidated",
            }:
                raise CommunicationScopeTerminalError(
                    "communication scope is terminal"
                )
            self._require_current_resolution(
                connection, row=row, actor=actor, now=now
            )
            if (
                row["completion_request_digest"] is not None
                and not secrets.compare_digest(
                    str(row["completion_request_digest"]), reservation_digest
                )
            ):
                raise ConflictError("communication scope completion conflict")
            if row["state"] == "approval_issued":
                connection.execute(
                    """UPDATE communication_scopes
                       SET state='completion_reserved',completion_reserved_at=?,
                           completion_idempotency_key_sha256=?,
                           completion_request_digest=?
                       WHERE scope_id=? AND state='approval_issued'""",
                    (
                        now,
                        completion_hash,
                        reservation_digest,
                        row["scope_id"],
                    ),
                )
            elif row["state"] != "completion_reserved":
                raise ConflictError("communication scope approval is not issued")
            scope_id = str(row["scope_id"])
            request_id = str(row["approval_request_id"])
            transaction_digest = str(row["transaction_digest"])
            transaction_bytes = self._require_stored_transaction(row)
            possession_secret, _begin_result = self._begin_storage(row)
            if possession_secret is None:
                raise GateBlocked(
                    "communication_scope",
                    "communication scope reservation is incomplete",
                )
        receipt_value = self.approval_client.retrieve_receipt(
            request_id=request_id,
            possession_secret=possession_secret,
            domain_id=actor.domain_id,
            approval_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
            transaction_digest=transaction_digest,
            idempotency_key=(
                f"core:communication-scope:retrieve:{scope_id}:"
                f"{reservation_digest}"
            ),
        )
        receipt = self.approval_verifier.verify(
            canonical_transaction=transaction_bytes,
            approval=receipt_value,
            expected_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
            expected_domain_id=actor.domain_id,
            when=datetime.fromtimestamp(now, UTC),
        )
        if receipt.approver_principal_id != actor.principal_id:
            raise AuthorizationError("communication scope owner approval denied")
        result = CommunicationScopeCompleteResult(
            schema="agentnet.communication-scope.complete-result.v1",
            status="communication_active",
            authority_granted=True,
            communication_usable=True,
            **dict(COMMUNICATION_SCOPE_RESTRICTIONS),
        ).model_dump(by_alias=True)
        try:
            with self.store.transaction() as connection:
                row = self._row_for_begin(connection, begin_hash)
                self._require_row_actor(row, actor)
                if row["state"] == "committed":
                    self._require_committed_current(
                        connection, row=row, actor=actor, now=int(self.clock())
                    )
                    materialize_v6_communication_scope(
                        connection,
                        scope_id=str(row["scope_id"]),
                    )
                    return self._stored_complete(row, reservation_digest)
                if (
                    row["state"] != "completion_reserved"
                    or not secrets.compare_digest(
                        str(row["completion_request_digest"]),
                        reservation_digest,
                    )
                ):
                    raise ConflictError("communication scope completion conflict")
                commit_now = int(self.clock())
                if int(row["approval_expires_at"]) <= commit_now:
                    raise _FinalCommitExpired
                if not secrets.compare_digest(
                    str(row["transaction_digest"]), transaction_digest
                ):
                    raise AuthenticationError(
                        "communication scope transaction denied"
                    )
                reloaded_bytes = self._require_stored_transaction(row)
                receipt = self.approval_verifier.verify(
                    canonical_transaction=reloaded_bytes,
                    approval=receipt_value,
                    expected_purpose=COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
                    expected_domain_id=actor.domain_id,
                    when=datetime.fromtimestamp(commit_now, UTC),
                )
                if receipt.approver_principal_id != row["principal_id"]:
                    raise AuthorizationError(
                        "communication scope owner approval denied"
                    )
                preimage = self._require_current_resolution(
                    connection, row=row, actor=actor, now=commit_now
                )
                rebuilt = build_communication_scope_transaction(preimage)
                if (
                    not secrets.compare_digest(
                        canonical_json(rebuilt), reloaded_bytes
                    )
                    or rebuilt["scope_id"] != scope_id
                    or rebuilt["scope_digest"] != row["scope_digest"]
                    or len(rebuilt["items"])
                    != 2 * len(COMMUNICATION_SCOPE_ACTIONS)
                ):
                    raise AuthenticationError(
                        "communication scope transaction denied"
                    )
                if self._active_scope_conflict(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    exclude_scope_id=scope_id,
                ):
                    raise ConflictError(
                        "an active communication scope already exists"
                    )
                consume_independent_approval(connection, receipt=receipt)
                for item in rebuilt["items"]:
                    entitlement = item["entitlement"]
                    if (
                        entitlement["action"] not in COMMUNICATION_SCOPE_ACTIONS
                        or entitlement["expires_at"] is not None
                        or item["harness_id"]
                        not in {
                            row["owner_harness_id"],
                            row["fresh_harness_id"],
                        }
                    ):
                        raise AuthenticationError(
                            "communication scope action denied"
                        )
                    connection.execute(
                        """INSERT INTO entitlements(
                            entitlement_id,domain_id,principal_id,action,
                            resource_pattern,expires_at,revoked_at,revision
                        ) VALUES(?,?,?,?,?,?,?,?)""",
                        (
                            entitlement["entitlement_id"],
                            entitlement["domain_id"],
                            entitlement["principal_id"],
                            entitlement["action"],
                            entitlement["resource_pattern"],
                            None,
                            None,
                            entitlement["revision"],
                        ),
                    )
                    connection.execute(
                        """INSERT INTO communication_scope_items(
                            scope_id,item_ordinal,item_id,entitlement_id,
                            harness_id,action,resource_pattern,item_json,
                            expires_at
                        ) VALUES(?,?,?,?,?,?,?,?,?)""",
                        (
                            scope_id,
                            item["item_ordinal"],
                            item["item_id"],
                            entitlement["entitlement_id"],
                            item["harness_id"],
                            item["action"],
                            item["resource_pattern"],
                            canonical_json(item).decode("utf-8"),
                            None,
                        ),
                    )
                audit_hash = self.store.append_audit(
                    connection,
                    {
                        "schema": "agentnet.audit.communication-scope.v1",
                        "action": "communication_scope.committed",
                        "scope_id": scope_id,
                        "domain_id": row["domain_id"],
                        "principal_id": row["principal_id"],
                        "actor_harness_id": actor.harness_id,
                        "transaction_digest": transaction_digest,
                        "approval_receipt_id": receipt.receipt_id,
                        "entitlement_count": len(rebuilt["items"]),
                        **dict(COMMUNICATION_SCOPE_RESTRICTIONS),
                    },
                )
                result_digest = digest_canonical(result)
                connection.execute(
                    """UPDATE communication_scopes
                       SET state='committed',approval_receipt_id=?,
                           approval_receipt_digest=?,committed_at=?,
                           committed_result_encrypted=?,
                           committed_result_digest=?,audit_record_hash=?
                       WHERE scope_id=?""",
                    (
                        receipt.receipt_id,
                        digest_canonical(receipt_value),
                        commit_now,
                        self.store.cipher.encrypt_json(
                            result,
                            purpose=f"communication-scope-result:{scope_id}",
                        ),
                        result_digest,
                        audit_hash,
                        scope_id,
                    ),
                )
                materialize_v6_communication_scope(
                    connection,
                    scope_id=scope_id,
                )
                return self._stored_complete(
                    self._row_for_begin(connection, begin_hash),
                    reservation_digest,
                )
        except _FinalCommitExpired:
            with self.store.transaction() as connection:
                row = self._row_for_begin(connection, begin_hash)
                self._require_row_actor(row, actor)
                self._expire_if_due(
                    connection, row=row, now=int(self.clock())
                )
            raise CommunicationScopeTerminalError(
                "communication scope is terminal"
            ) from None


COLLABORATION_SCOPE_SCHEMA = "agentnet.collaboration-scope.v1"
COLLABORATION_SCOPE_ISSUE_ACTION = "collaboration.scope.issue"
COLLABORATION_SCOPE_REVOKE_ACTION = "collaboration.scope.revoke"
ALLOWED_COLLABORATION_ACTIONS = frozenset(
    {
        "artifact.download",
        "artifact.send",
        "message.acknowledge",
        "message.read",
        "message.send",
        "obligation.create",
        "obligation.respond",
        "room.create",
        "room.member.add",
        "room.member.remove",
        "room.read",
        "room.send",
        "task.accept",
        "task.cancel",
        "task.handoff",
        "task.propose",
    }
)
_ALLOWED_RESOURCE_ROOTS = frozenset(
    {
        "artifact",
        "catalog",
        "conversation",
        "meeting",
        "obligation",
        "project",
        "repository",
        "resource",
        "room",
        "task",
        "thread",
    }
)
_COLLABORATION_STRICT = ConfigDict(extra="forbid", frozen=True)


def _canonical_nonempty_tuple(
    values: tuple[str, ...],
    *,
    field_name: str,
    maximum: int = 1_000,
) -> tuple[str, ...]:
    if not values or len(values) > maximum:
        raise ValueError(f"{field_name} must be a bounded non-empty tuple")
    if any(
        not value
        or len(value) > 512
        or any(ord(character) < 0x21 for character in value)
        for value in values
    ):
        raise ValueError(f"{field_name} contains an invalid value")
    if values != tuple(sorted(set(values))):
        raise ValueError(f"{field_name} must be sorted and unique")
    return values


class CollaborationScopeProposal(BaseModel):
    """Canonical immutable authority requested by one exact owner harness."""

    model_config = _COLLABORATION_STRICT

    schema_version: Literal["agentnet.collaboration-scope.v1"] = COLLABORATION_SCOPE_SCHEMA
    scope_id: str = Field(min_length=16, max_length=256)
    scope_kind: Literal["personal", "direct", "shared"]
    member_harness_ids: tuple[str, ...] = Field(min_length=1, max_length=1_000)
    allowed_actions: tuple[str, ...] = Field(min_length=1, max_length=64)
    allowed_resource_prefixes: tuple[str, ...] = Field(min_length=1, max_length=256)
    allowed_classifications: tuple[Classification, ...] = Field(min_length=1, max_length=4)
    canonical_references: tuple[str, ...] = Field(default=(), max_length=256)
    policy_revision: int = Field(ge=1)
    domain_revocation_epoch: int = Field(ge=1)
    expires_at: int | None = Field(default=None, ge=1)

    @field_validator("scope_id")
    @classmethod
    def bounded_scope_id(cls, value: str) -> str:
        if any(ord(character) < 0x21 for character in value):
            raise ValueError("collaboration scope identifier is invalid")
        return value

    @field_validator("member_harness_ids")
    @classmethod
    def canonical_members(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        return _canonical_nonempty_tuple(value, field_name="collaboration scope members")

    @field_validator("allowed_actions")
    @classmethod
    def canonical_actions(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        canonical = _canonical_nonempty_tuple(
            value,
            field_name="collaboration scope actions",
            maximum=64,
        )
        if not set(canonical).issubset(ALLOWED_COLLABORATION_ACTIONS):
            raise ValueError("collaboration scope contains an unsupported action")
        return canonical

    @field_validator("allowed_resource_prefixes")
    @classmethod
    def canonical_resources(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        canonical = _canonical_nonempty_tuple(
            value,
            field_name="collaboration scope resources",
            maximum=256,
        )
        if any(
            ":" not in prefix
            or prefix.split(":", 1)[0] not in _ALLOWED_RESOURCE_ROOTS
            or "*" in prefix
            for prefix in canonical
        ):
            raise ValueError("collaboration scope contains an unsupported resource prefix")
        return canonical

    @field_validator("allowed_classifications")
    @classmethod
    def canonical_classifications(
        cls,
        value: tuple[Classification, ...],
    ) -> tuple[Classification, ...]:
        if not value or len(value) != len(set(value)):
            raise ValueError("collaboration scope classifications must be non-empty and unique")
        if tuple(item.value for item in value) != tuple(
            sorted(item.value for item in value)
        ):
            raise ValueError("collaboration scope classifications must be sorted")
        return value

    @field_validator("canonical_references")
    @classmethod
    def canonical_reference_values(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if not value:
            return value
        return _canonical_nonempty_tuple(
            value,
            field_name="collaboration scope references",
            maximum=256,
        )

    @model_validator(mode="after")
    def direct_scope_has_exact_pair(self) -> "CollaborationScopeProposal":
        if self.scope_kind == "personal" and len(self.member_harness_ids) != 1:
            raise ValueError("personal collaboration scope requires one exact harness")
        if self.scope_kind == "direct" and len(self.member_harness_ids) != 2:
            raise ValueError("direct collaboration scope requires two exact harnesses")
        return self


class CollaborationScope(BaseModel):
    """Current immutable collaboration bounds plus their versioned lifecycle."""

    model_config = _COLLABORATION_STRICT

    schema_version: Literal["agentnet.collaboration-scope.v1"] = COLLABORATION_SCOPE_SCHEMA
    scope_id: str
    scope_kind: Literal["personal", "direct", "shared"]
    domain_id: str
    owner_principal_id: str
    owner_harness_id: str
    member_harness_ids: tuple[str, ...]
    allowed_actions: tuple[str, ...]
    allowed_resource_prefixes: tuple[str, ...]
    allowed_classifications: tuple[Classification, ...]
    canonical_references: tuple[str, ...]
    policy_revision: int = Field(ge=1)
    domain_revocation_epoch: int = Field(ge=1)
    control_sequence: int = Field(ge=1)
    membership_sequence: int = Field(ge=1)
    proposal_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    scope_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    revision: int = Field(ge=1)
    state: Literal["active", "revoked", "expired", "archived", "deleted", "blocked"]
    state_reason: str = Field(min_length=1, max_length=128)
    created_at: int = Field(ge=1)
    updated_at: int = Field(ge=1)
    expires_at: int | None = Field(default=None, ge=1)
    revoked_at: int | None = Field(default=None, ge=1)

    def authorization_context(self) -> dict[str, object]:
        """Return the exact non-authoritative snapshot bound into an event."""

        return {
            "collaboration_scope_id": self.scope_id,
            "collaboration_scope_revision": self.revision,
            "collaboration_scope_policy_revision": self.policy_revision,
            "collaboration_scope_domain_revocation_epoch": self.domain_revocation_epoch,
            "collaboration_scope_member_harness_ids": list(self.member_harness_ids),
            "collaboration_scope_digest": self.scope_digest,
        }


class CollaborationScopeMemberVisibility(BaseModel):
    """One non-enumerating exact member/scope intersection for recipient resolution."""

    model_config = _COLLABORATION_STRICT

    scope_id: str
    scope_revision: int = Field(ge=1)
    scope_policy_revision: int = Field(ge=1)
    harness_id: str


class CollaborationScopeService:
    """Issue and enforce principal-owned, exact-harness collaboration bounds."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.store = store
        self.clock = clock

    @staticmethod
    def issuance_request(
        *,
        actor: VerifiedActor,
        proposal: CollaborationScopeProposal,
    ) -> dict[str, object]:
        return {
            "schema_version": proposal.schema_version,
            "owner_principal_id": actor.principal_id,
            "owner_harness_id": actor.harness_id,
            "proposal": proposal.model_dump(mode="json"),
        }

    @staticmethod
    def revocation_request(
        *,
        scope: CollaborationScope,
        expected_revision: int,
        reason: str,
    ) -> dict[str, object]:
        return {
            "schema_version": COLLABORATION_SCOPE_SCHEMA,
            "scope_id": scope.scope_id,
            "expected_revision": expected_revision,
            "reason": reason,
        }

    @staticmethod
    def _when(when: datetime | None, clock: Callable[[], float]) -> tuple[datetime, int]:
        value = when or datetime.fromtimestamp(int(clock()), UTC)
        if value.tzinfo is None:
            raise ValidationError("collaboration scope time must be timezone-aware")
        value = datetime.fromtimestamp(int(value.timestamp()), UTC)
        return value, int(value.timestamp())

    @staticmethod
    def _require_actor(
        connection: Any,
        *,
        actor: VerifiedActor,
        when: datetime,
    ) -> tuple[Literal["principal", "guest"], str, str, int, int]:
        if (
            actor.kind
            not in {
                ActorKind.VERIFIED_HUMAN_HARNESS,
                ActorKind.HOST_GUEST_HARNESS,
            }
            or actor.positive_authority_id is None
            or actor.harness_id is None
            or actor.credential_id is None
        ):
            raise AuthorizationError(
                "collaboration scope requires a verified human or host guest and exact harness"
            )
        domain = connection.execute(
            "SELECT status,policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        if domain is None:
            raise AuthorizationError("collaboration scope is unavailable")
        revision = int(domain["policy_revision"])
        denial, current_revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=revision,
            when=when,
        )
        if denial is not None or domain["status"] != "active":
            raise AuthorizationError("collaboration scope actor is not current")
        authority_kind: Literal["principal", "guest"] = (
            "principal"
            if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS
            else "guest"
        )
        return (
            authority_kind,
            actor.positive_authority_id,
            actor.harness_id,
            current_revision,
            int(domain["revocation_epoch"]),
        )

    @staticmethod
    def _proposal_digest(
        *,
        actor: VerifiedActor,
        proposal: CollaborationScopeProposal,
    ) -> str:
        return canonical_digest(
            CollaborationScopeService.issuance_request(actor=actor, proposal=proposal)
        )

    @staticmethod
    def _member_digest(
        *,
        scope_id: str,
        authority_kind: Literal["principal", "guest"],
        authority_id: str,
        harness_id: str,
        role: str,
        joined_at: int,
    ) -> str:
        return canonical_digest(
            {
                "scope_id": scope_id,
                "authority_kind": authority_kind,
                "authority_id": authority_id,
                "harness_id": harness_id,
                "role": role,
                "state": "active",
                "joined_sequence": 1,
                "joined_at": joined_at,
            }
        )

    @staticmethod
    def _scope_digest(
        *,
        scope_id: str,
        scope_kind: str,
        domain_id: str,
        owner_principal_id: str,
        owner_harness_id: str,
        members: list[dict[str, object]],
        allowed_actions: tuple[str, ...],
        allowed_resource_prefixes: tuple[str, ...],
        allowed_classifications: tuple[Classification, ...],
        canonical_references: tuple[str, ...],
        policy_revision: int,
        domain_revocation_epoch: int,
        control_sequence: int,
        membership_sequence: int,
        proposal_digest: str,
        revision: int,
        state: str,
        state_reason: str,
        created_at: int,
        updated_at: int,
        expires_at: int | None,
        revoked_at: int | None,
    ) -> str:
        return canonical_digest(
            {
                "schema_version": COLLABORATION_SCOPE_SCHEMA,
                "scope_id": scope_id,
                "scope_kind": scope_kind,
                "domain_id": domain_id,
                "owner_principal_id": owner_principal_id,
                "owner_harness_id": owner_harness_id,
                "members": members,
                "allowed_actions": list(allowed_actions),
                "allowed_resource_prefixes": list(allowed_resource_prefixes),
                "allowed_classifications": [
                    value.value for value in allowed_classifications
                ],
                "canonical_references": list(canonical_references),
                "policy_revision": policy_revision,
                "domain_revocation_epoch": domain_revocation_epoch,
                "control_sequence": control_sequence,
                "membership_sequence": membership_sequence,
                "proposal_digest": proposal_digest,
                "revision": revision,
                "state": state,
                "state_reason": state_reason,
                "created_at": created_at,
                "updated_at": updated_at,
                "expires_at": expires_at,
                "revoked_at": revoked_at,
            }
        )

    @staticmethod
    def _load_canonical_tuple(
        raw: object,
        *,
        field_name: str,
        allow_empty: bool = False,
    ) -> tuple[str, ...]:
        try:
            value = json.loads(str(raw))
        except (TypeError, ValueError):
            raise AuthorizationError("collaboration scope is unavailable") from None
        if (
            not isinstance(value, list)
            or any(not isinstance(item, str) for item in value)
            or value != sorted(set(value))
            or (not allow_empty and not value)
            or canonical_json(value).decode("utf-8") != str(raw)
        ):
            raise AuthorizationError(
                f"collaboration scope {field_name} is malformed"
            )
        return tuple(value)

    def _members(
        self,
        connection: Any,
        *,
        row: Any,
    ) -> tuple[tuple[str, ...], list[dict[str, object]]]:
        member_rows = connection.execute(
            """SELECT authority_kind,authority_id,harness_id,role,state,joined_sequence,
                      removed_sequence,member_digest,joined_at,removed_at
                 FROM collaboration_scope_members
                WHERE scope_id=? ORDER BY harness_id""",
            (row["scope_id"],),
        ).fetchall()
        if not member_rows:
            raise AuthorizationError("collaboration scope is unavailable")
        members: list[dict[str, object]] = []
        harness_ids: list[str] = []
        owner_members = 0
        for member in member_rows:
            authority_kind = str(member["authority_kind"])
            authority_id = str(member["authority_id"])
            harness_id = str(member["harness_id"])
            role = str(member["role"])
            if (
                authority_kind not in {"principal", "guest"}
                or not authority_id
                or not harness_id
                or member["state"] != "active"
                or int(member["joined_sequence"]) != 1
                or member["removed_sequence"] is not None
                or member["removed_at"] is not None
                or (authority_kind == "guest") != (role == "guest")
            ):
                raise AuthorizationError("collaboration scope membership is unavailable")
            if role == "owner":
                owner_members += 1
                if (
                    authority_kind != "principal"
                    or authority_id != row["owner_principal_id"]
                    or harness_id != row["owner_harness_id"]
                ):
                    raise AuthorizationError(
                        "collaboration scope membership is unavailable"
                    )
            expected = self._member_digest(
                scope_id=str(row["scope_id"]),
                authority_kind=authority_kind,
                authority_id=authority_id,
                harness_id=harness_id,
                role=role,
                joined_at=int(member["joined_at"]),
            )
            if not secrets.compare_digest(expected, str(member["member_digest"])):
                raise AuthorizationError("collaboration scope membership is unavailable")
            harness_ids.append(harness_id)
            members.append(
                {
                    "authority_kind": authority_kind,
                    "authority_id": authority_id,
                    "harness_id": harness_id,
                    "role": role,
                    "state": "active",
                    "joined_sequence": 1,
                    "joined_at": int(member["joined_at"]),
                }
            )
        if owner_members != 1 or harness_ids != sorted(set(harness_ids)):
            raise AuthorizationError("collaboration scope membership is unavailable")
        return tuple(harness_ids), members

    def _scope_from_row(self, connection: Any, row: Any) -> CollaborationScope:
        actions = self._load_canonical_tuple(
            row["allowed_actions_json"],
            field_name="actions",
        )
        resources = self._load_canonical_tuple(
            row["allowed_resource_prefixes_json"],
            field_name="resources",
        )
        classification_values = self._load_canonical_tuple(
            row["allowed_classifications_json"],
            field_name="classifications",
        )
        references = self._load_canonical_tuple(
            row["canonical_references_json"],
            field_name="references",
            allow_empty=True,
        )
        try:
            classifications = tuple(
                Classification(value) for value in classification_values
            )
        except ValueError:
            raise AuthorizationError("collaboration scope is unavailable") from None
        member_harness_ids, members = self._members(connection, row=row)
        if (
            int(row["policy_floor"]) != int(row["policy_revision"])
            or not set(actions).issubset(ALLOWED_COLLABORATION_ACTIONS)
            or any(
                ":" not in prefix
                or prefix.split(":", 1)[0] not in _ALLOWED_RESOURCE_ROOTS
                or "*" in prefix
                for prefix in resources
            )
            or tuple(value.value for value in classifications)
            != tuple(sorted(value.value for value in classifications))
        ):
            raise AuthorizationError("collaboration scope is unavailable")
        expected_digest = self._scope_digest(
            scope_id=str(row["scope_id"]),
            scope_kind=str(row["scope_kind"]),
            domain_id=str(row["domain_id"]),
            owner_principal_id=str(row["owner_principal_id"]),
            owner_harness_id=str(row["owner_harness_id"]),
            members=members,
            allowed_actions=actions,
            allowed_resource_prefixes=resources,
            allowed_classifications=classifications,
            canonical_references=references,
            policy_revision=int(row["policy_revision"]),
            domain_revocation_epoch=int(row["domain_revocation_epoch"]),
            control_sequence=int(row["control_sequence"]),
            membership_sequence=int(row["membership_sequence"]),
            proposal_digest=str(row["proposal_digest"]),
            revision=int(row["revision"]),
            state=str(row["state"]),
            state_reason=str(row["state_reason"]),
            created_at=int(row["created_at"]),
            updated_at=int(row["updated_at"]),
            expires_at=(
                int(row["expires_at"]) if row["expires_at"] is not None else None
            ),
            revoked_at=(
                int(row["revoked_at"]) if row["revoked_at"] is not None else None
            ),
        )
        if not secrets.compare_digest(expected_digest, str(row["scope_digest"])):
            raise AuthorizationError("collaboration scope is unavailable")
        try:
            return CollaborationScope(
                scope_id=str(row["scope_id"]),
                scope_kind=str(row["scope_kind"]),
                domain_id=str(row["domain_id"]),
                owner_principal_id=str(row["owner_principal_id"]),
                owner_harness_id=str(row["owner_harness_id"]),
                member_harness_ids=member_harness_ids,
                allowed_actions=actions,
                allowed_resource_prefixes=resources,
                allowed_classifications=classifications,
                canonical_references=references,
                policy_revision=int(row["policy_revision"]),
                domain_revocation_epoch=int(row["domain_revocation_epoch"]),
                control_sequence=int(row["control_sequence"]),
                membership_sequence=int(row["membership_sequence"]),
                proposal_digest=str(row["proposal_digest"]),
                scope_digest=str(row["scope_digest"]),
                revision=int(row["revision"]),
                state=str(row["state"]),
                state_reason=str(row["state_reason"]),
                created_at=int(row["created_at"]),
                updated_at=int(row["updated_at"]),
                expires_at=(
                    int(row["expires_at"]) if row["expires_at"] is not None else None
                ),
                revoked_at=(
                    int(row["revoked_at"]) if row["revoked_at"] is not None else None
                ),
            )
        except (TypeError, ValueError):
            raise AuthorizationError("collaboration scope is unavailable") from None

    @staticmethod
    def _scope_row(connection: Any, scope_id: str) -> Any:
        return connection.execute(
            "SELECT * FROM collaboration_scopes WHERE scope_id=?",
            (scope_id,),
        ).fetchone()

    def _visible_scope(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        scope_id: str,
        when: datetime,
        now: int,
    ) -> tuple[CollaborationScope, int, int]:
        authority_kind, authority_id, harness_id, revision, revocation_epoch = (
            self._require_actor(
                connection,
                actor=actor,
                when=when,
            )
        )
        row = connection.execute(
            """SELECT s.* FROM collaboration_scopes s
                 JOIN collaboration_scope_members m ON m.scope_id=s.scope_id
                WHERE s.scope_id=? AND s.domain_id=? AND m.authority_kind=?
                  AND m.authority_id=? AND m.harness_id=? AND m.state='active'""",
            (
                scope_id,
                actor.domain_id,
                authority_kind,
                authority_id,
                harness_id,
            ),
        ).fetchone()
        if row is None:
            raise AuthorizationError("collaboration scope is unavailable")
        if (
            row["state"] == "active"
            and row["expires_at"] is not None
            and int(row["expires_at"]) <= now
        ):
            current = self._scope_from_row(connection, row)
            next_revision = current.revision + 1
            next_control = current.control_sequence + 1
            next_digest = self._scope_digest(
                scope_id=current.scope_id,
                scope_kind=current.scope_kind,
                domain_id=current.domain_id,
                owner_principal_id=current.owner_principal_id,
                owner_harness_id=current.owner_harness_id,
                members=self._members(connection, row=row)[1],
                allowed_actions=current.allowed_actions,
                allowed_resource_prefixes=current.allowed_resource_prefixes,
                allowed_classifications=current.allowed_classifications,
                canonical_references=current.canonical_references,
                policy_revision=current.policy_revision,
                domain_revocation_epoch=current.domain_revocation_epoch,
                control_sequence=next_control,
                membership_sequence=current.membership_sequence,
                proposal_digest=current.proposal_digest,
                revision=next_revision,
                state="expired",
                state_reason="expired",
                created_at=current.created_at,
                updated_at=now,
                expires_at=current.expires_at,
                revoked_at=None,
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "collaboration_scope.expired",
                    "actor": actor.audit_view(),
                    "scope_id": current.scope_id,
                    "scope_digest": next_digest,
                    "revision": next_revision,
                },
            )
            connection.execute(
                """UPDATE collaboration_scopes
                      SET state='expired',state_reason='expired',control_sequence=?,
                          scope_digest=?,audit_record_hash=?,revision=?,updated_at=?
                    WHERE scope_id=? AND revision=? AND state='active'""",
                (
                    next_control,
                    next_digest,
                    audit_hash,
                    next_revision,
                    now,
                    current.scope_id,
                    current.revision,
                ),
            )
            row = self._scope_row(connection, current.scope_id)
        return self._scope_from_row(connection, row), revision, revocation_epoch

    @staticmethod
    def _resource_allowed(scope: CollaborationScope, resource: str) -> bool:
        return any(resource.startswith(prefix) for prefix in scope.allowed_resource_prefixes)

    @staticmethod
    def _current_member_bindings(
        connection: Any,
        *,
        domain_id: str,
        harness_ids: tuple[str, ...],
        now: int,
    ) -> dict[str, tuple[Literal["principal", "guest"], str]]:
        if not harness_ids:
            return {}
        placeholders = ",".join("?" for _ in harness_ids)
        rows = connection.execute(
            f"""SELECT h.harness_id,h.domain_id,h.principal_id,h.guest_id,h.status,
                       p.domain_id AS principal_domain_id,p.status AS principal_status,
                       g.host_domain_id AS guest_host_domain_id,g.status AS guest_status,
                       g.expires_at AS guest_expires_at
                  FROM harnesses h
             LEFT JOIN principals p ON p.principal_id=h.principal_id
             LEFT JOIN guests g ON g.guest_id=h.guest_id
                 WHERE h.harness_id IN ({placeholders})
                   AND EXISTS (
                       SELECT 1 FROM credentials c
                        WHERE c.harness_id=h.harness_id
                          AND c.epoch=h.credential_epoch
                          AND c.status='active'
                          AND c.not_before<=? AND c.expires_at>?
                   )""",
            (*harness_ids, now, now),
        ).fetchall()
        result: dict[str, tuple[Literal["principal", "guest"], str]] = {}
        for member in rows:
            if member["domain_id"] != domain_id or member["status"] != "active":
                continue
            harness_id = str(member["harness_id"])
            if (
                member["principal_id"] is not None
                and member["guest_id"] is None
                and member["principal_domain_id"] == domain_id
                and member["principal_status"] == "active"
            ):
                result[harness_id] = ("principal", str(member["principal_id"]))
            elif (
                member["guest_id"] is not None
                and member["principal_id"] is None
                and member["guest_host_domain_id"] == domain_id
                and member["guest_status"] == "active"
                and member["guest_expires_at"] is not None
                and int(member["guest_expires_at"]) > now
            ):
                result[harness_id] = ("guest", str(member["guest_id"]))
        return result

    @staticmethod
    def _require_targets(
        connection: Any,
        *,
        scope: CollaborationScope,
        target_harness_ids: tuple[str, ...],
        now: int,
    ) -> None:
        if (
            len(target_harness_ids) > 1_000
            or len(target_harness_ids) != len(set(target_harness_ids))
            or any(not target for target in target_harness_ids)
            or not set(target_harness_ids).issubset(scope.member_harness_ids)
        ):
            raise AuthorizationError("collaboration scope does not authorize exact recipients")
        if not target_harness_ids:
            return
        placeholders = ",".join("?" for _ in target_harness_ids)
        rows = connection.execute(
            f"""SELECT harness_id,authority_kind,authority_id
                  FROM collaboration_scope_members
                 WHERE scope_id=? AND state='active'
                   AND harness_id IN ({placeholders})""",
            (scope.scope_id, *target_harness_ids),
        ).fetchall()
        stored = {
            str(row["harness_id"]): (
                str(row["authority_kind"]),
                str(row["authority_id"]),
            )
            for row in rows
        }
        current = CollaborationScopeService._current_member_bindings(
            connection,
            domain_id=scope.domain_id,
            harness_ids=target_harness_ids,
            now=now,
        )
        if (
            set(stored) != set(target_harness_ids)
            or set(current) != set(target_harness_ids)
            or any(current[harness_id] != stored[harness_id] for harness_id in stored)
        ):
            raise AuthorizationError("collaboration scope does not authorize exact recipients")

    def require_in_transaction(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        scope_id: str | None = None,
        action: str,
        resource: str,
        target_harness_ids: tuple[str, ...],
        classification: Classification = Classification.C1_INTERNAL,
        when: datetime | None = None,
    ) -> CollaborationScope:
        when, now = self._when(when, self.clock)
        if action not in ALLOWED_COLLABORATION_ACTIONS or not resource:
            raise AuthorizationError("collaboration scope does not authorize the operation")

        def authorized(candidate: CollaborationScope, revision: int, epoch: int) -> bool:
            return (
                candidate.state == "active"
                and candidate.policy_revision == revision
                and candidate.domain_revocation_epoch == epoch
                and action in candidate.allowed_actions
                and classification in candidate.allowed_classifications
                and self._resource_allowed(candidate, resource)
            )

        if scope_id is not None:
            candidate, revision, epoch = self._visible_scope(
                connection,
                actor=actor,
                scope_id=scope_id,
                when=when,
                now=now,
            )
            if not authorized(candidate, revision, epoch):
                raise AuthorizationError("collaboration scope does not authorize the operation")
            self._require_targets(
                connection,
                scope=candidate,
                target_harness_ids=target_harness_ids,
                now=now,
            )
            return candidate

        authority_kind, authority_id, harness_id, revision, epoch = self._require_actor(
            connection,
            actor=actor,
            when=when,
        )
        rows = connection.execute(
            """SELECT s.* FROM collaboration_scopes s
                 JOIN collaboration_scope_members m ON m.scope_id=s.scope_id
                WHERE s.domain_id=? AND m.authority_kind=? AND m.authority_id=?
                  AND m.harness_id=? AND m.state='active' AND s.state='active'
                ORDER BY s.scope_id""",
            (actor.domain_id, authority_kind, authority_id, harness_id),
        ).fetchall()
        matches: list[CollaborationScope] = []
        for row in rows:
            if row["expires_at"] is not None and int(row["expires_at"]) <= now:
                continue
            candidate = self._scope_from_row(connection, row)
            if not authorized(candidate, revision, epoch):
                continue
            try:
                self._require_targets(
                    connection,
                    scope=candidate,
                    target_harness_ids=target_harness_ids,
                    now=now,
                )
            except AuthorizationError:
                continue
            matches.append(candidate)
        if not matches:
            raise AuthorizationError("collaboration scope does not authorize the operation")
        if len(matches) != 1:
            raise ConflictError("collaboration scope is ambiguous")
        return matches[0]

    def require(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str | None = None,
        action: str,
        resource: str,
        target_harness_ids: tuple[str, ...],
        classification: Classification = Classification.C1_INTERNAL,
        when: datetime | None = None,
    ) -> CollaborationScope:
        with self.store.transaction() as connection:
            return self.require_in_transaction(
                connection,
                actor=actor,
                scope_id=scope_id,
                action=action,
                resource=resource,
                target_harness_ids=target_harness_ids,
                classification=classification,
                when=when,
            )

    def issue(
        self,
        *,
        actor: VerifiedActor,
        proposal: CollaborationScopeProposal,
        authority: IssuanceAuthority,
        when: datetime | None = None,
    ) -> CollaborationScope:
        when, now = self._when(when, self.clock)
        expected_request = self.issuance_request(actor=actor, proposal=proposal)
        proposal_digest = self._proposal_digest(actor=actor, proposal=proposal)
        with self.store.transaction() as connection:
            authority_kind, authority_id, harness_id, policy_revision, revocation_epoch = (
                self._require_actor(
                    connection,
                    actor=actor,
                    when=when,
                )
            )
            if authority_kind != "principal" or actor.principal_id != authority_id:
                raise AuthorizationError(
                    "collaboration scope issuance requires a verified local human owner"
                )
            principal_id = authority_id
            if authority.actor != actor:
                raise AuthorizationError("collaboration scope issuance actor binding mismatch")
            if (
                proposal.policy_revision != policy_revision
                or proposal.domain_revocation_epoch != revocation_epoch
                or harness_id not in proposal.member_harness_ids
                or (proposal.expires_at is not None and proposal.expires_at <= now)
            ):
                raise AuthorizationError("collaboration scope proposal is stale or out of scope")
            authorized_revision = require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=COLLABORATION_SCOPE_ISSUE_ACTION,
                expected_resource=f"scope:{proposal.scope_id}",
                expected_request=expected_request,
                when=when,
            )
            if authorized_revision != policy_revision:
                raise AuthorizationError("collaboration scope policy revision changed")

            existing = self._scope_row(connection, proposal.scope_id)
            if existing is not None:
                stored = self._scope_from_row(connection, existing)
                if (
                    stored.owner_principal_id != principal_id
                    or stored.owner_harness_id != harness_id
                    or not secrets.compare_digest(stored.proposal_digest, proposal_digest)
                ):
                    raise ConflictError("collaboration scope identifier is unavailable")
                return stored

            member_bindings = self._current_member_bindings(
                connection,
                domain_id=actor.domain_id,
                harness_ids=proposal.member_harness_ids,
                now=now,
            )
            if set(member_bindings) != set(proposal.member_harness_ids):
                raise AuthorizationError("collaboration scope member is unavailable")

            member_values: list[dict[str, object]] = []
            member_rows: list[tuple[object, ...]] = []
            for member_harness_id in proposal.member_harness_ids:
                member_authority_kind, member_authority_id = member_bindings[
                    member_harness_id
                ]
                role = (
                    "owner"
                    if member_harness_id == harness_id
                    else "guest"
                    if member_authority_kind == "guest"
                    else "member"
                )
                member_digest = self._member_digest(
                    scope_id=proposal.scope_id,
                    authority_kind=member_authority_kind,
                    authority_id=member_authority_id,
                    harness_id=member_harness_id,
                    role=role,
                    joined_at=now,
                )
                member_values.append(
                    {
                        "authority_kind": member_authority_kind,
                        "authority_id": member_authority_id,
                        "harness_id": member_harness_id,
                        "role": role,
                        "state": "active",
                        "joined_sequence": 1,
                        "joined_at": now,
                    }
                )
                member_rows.append(
                    (
                        proposal.scope_id,
                        member_authority_kind,
                        member_authority_id,
                        member_harness_id,
                        role,
                        member_digest,
                        now,
                    )
                )
            scope_digest = self._scope_digest(
                scope_id=proposal.scope_id,
                scope_kind=proposal.scope_kind,
                domain_id=actor.domain_id,
                owner_principal_id=principal_id,
                owner_harness_id=harness_id,
                members=member_values,
                allowed_actions=proposal.allowed_actions,
                allowed_resource_prefixes=proposal.allowed_resource_prefixes,
                allowed_classifications=proposal.allowed_classifications,
                canonical_references=proposal.canonical_references,
                policy_revision=policy_revision,
                domain_revocation_epoch=revocation_epoch,
                control_sequence=1,
                membership_sequence=1,
                proposal_digest=proposal_digest,
                revision=1,
                state="active",
                state_reason="issued",
                created_at=now,
                updated_at=now,
                expires_at=proposal.expires_at,
                revoked_at=None,
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "collaboration_scope.issued",
                    "actor": actor.audit_view(),
                    "member_harness_ids": list(proposal.member_harness_ids),
                    "proposal_digest": proposal_digest,
                    "scope_digest": scope_digest,
                    "scope_id": proposal.scope_id,
                },
            )
            connection.execute(
                """INSERT INTO collaboration_scopes(
                    scope_id,schema_version,domain_id,scope_kind,owner_principal_id,
                    owner_harness_id,source_communication_scope_id,state,state_reason,
                    allowed_actions_json,allowed_resource_prefixes_json,
                    allowed_classifications_json,canonical_references_json,policy_floor,
                    policy_revision,domain_revocation_epoch,control_sequence,
                    membership_sequence,proposal_digest,scope_digest,audit_record_hash,
                    revision,created_at,updated_at,expires_at,revoked_at,archived_at,deleted_at
                ) VALUES(?,1,?,?,?,?,NULL,'active','issued',?,?,?,?,?,?,?,?,?,?,?,?,
                         1,?,?,?,NULL,NULL,NULL)""",
                (
                    proposal.scope_id,
                    actor.domain_id,
                    proposal.scope_kind,
                    principal_id,
                    harness_id,
                    canonical_json(list(proposal.allowed_actions)).decode("utf-8"),
                    canonical_json(list(proposal.allowed_resource_prefixes)).decode("utf-8"),
                    canonical_json(
                        [value.value for value in proposal.allowed_classifications]
                    ).decode("utf-8"),
                    canonical_json(list(proposal.canonical_references)).decode("utf-8"),
                    policy_revision,
                    policy_revision,
                    revocation_epoch,
                    1,
                    1,
                    proposal_digest,
                    scope_digest,
                    audit_hash,
                    now,
                    now,
                    proposal.expires_at,
                ),
            )
            for values in member_rows:
                connection.execute(
                    """INSERT INTO collaboration_scope_members(
                        scope_id,authority_kind,authority_id,harness_id,role,state,
                        joined_sequence,removed_sequence,member_digest,joined_at,removed_at
                    ) VALUES(?,?,?,?,?,'active',1,NULL,?,?,NULL)""",
                    values,
                )
            return self._scope_from_row(
                connection,
                self._scope_row(connection, proposal.scope_id),
            )

    def get_for_actor(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str,
        when: datetime | None = None,
    ) -> CollaborationScope:
        when, now = self._when(when, self.clock)
        with self.store.transaction() as connection:
            scope, _revision, _epoch = self._visible_scope(
                connection,
                actor=actor,
                scope_id=scope_id,
                when=when,
                now=now,
            )
            return scope

    def revoke(
        self,
        *,
        actor: VerifiedActor,
        scope_id: str,
        expected_revision: int,
        reason: str,
        authority: IssuanceAuthority,
        when: datetime | None = None,
    ) -> CollaborationScope:
        if (
            not reason
            or len(reason) > 128
            or any(ord(character) < 0x21 for character in reason)
        ):
            raise ValidationError("collaboration scope revocation reason is invalid")
        when, now = self._when(when, self.clock)
        with self.store.transaction() as connection:
            scope, _policy_revision, _revocation_epoch = self._visible_scope(
                connection,
                actor=actor,
                scope_id=scope_id,
                when=when,
                now=now,
            )
            if (
                actor.principal_id != scope.owner_principal_id
                or actor.harness_id != scope.owner_harness_id
                or authority.actor != actor
            ):
                raise AuthorizationError("collaboration scope revocation requires its exact owner")
            expected_request = self.revocation_request(
                scope=scope,
                expected_revision=expected_revision,
                reason=reason,
            )
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action=COLLABORATION_SCOPE_REVOKE_ACTION,
                expected_resource=f"scope:{scope.scope_id}",
                expected_request=expected_request,
                when=when,
            )
            if scope.state == "revoked":
                if (
                    scope.revision == expected_revision + 1
                    and scope.state_reason == reason
                ):
                    return scope
                raise ConflictError("collaboration scope revision conflict")
            if scope.state != "active" or scope.revision != expected_revision:
                raise ConflictError("collaboration scope revision conflict")
            next_revision = scope.revision + 1
            next_control = scope.control_sequence + 1
            row = self._scope_row(connection, scope.scope_id)
            next_digest = self._scope_digest(
                scope_id=scope.scope_id,
                scope_kind=scope.scope_kind,
                domain_id=scope.domain_id,
                owner_principal_id=scope.owner_principal_id,
                owner_harness_id=scope.owner_harness_id,
                members=self._members(connection, row=row)[1],
                allowed_actions=scope.allowed_actions,
                allowed_resource_prefixes=scope.allowed_resource_prefixes,
                allowed_classifications=scope.allowed_classifications,
                canonical_references=scope.canonical_references,
                policy_revision=scope.policy_revision,
                domain_revocation_epoch=scope.domain_revocation_epoch,
                control_sequence=next_control,
                membership_sequence=scope.membership_sequence,
                proposal_digest=scope.proposal_digest,
                revision=next_revision,
                state="revoked",
                state_reason=reason,
                created_at=scope.created_at,
                updated_at=now,
                expires_at=scope.expires_at,
                revoked_at=now,
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "collaboration_scope.revoked",
                    "actor": actor.audit_view(),
                    "previous_scope_digest": scope.scope_digest,
                    "reason": reason,
                    "revision": next_revision,
                    "scope_digest": next_digest,
                    "scope_id": scope.scope_id,
                },
            )
            cursor = connection.execute(
                """UPDATE collaboration_scopes
                      SET state='revoked',state_reason=?,control_sequence=?,
                          scope_digest=?,audit_record_hash=?,revision=?,updated_at=?,revoked_at=?
                    WHERE scope_id=? AND revision=? AND state='active'""",
                (
                    reason,
                    next_control,
                    next_digest,
                    audit_hash,
                    next_revision,
                    now,
                    now,
                    scope.scope_id,
                    scope.revision,
                ),
            )
            if cursor.rowcount != 1:
                raise ConflictError("collaboration scope revision conflict")
            return self._scope_from_row(
                connection,
                self._scope_row(connection, scope.scope_id),
            )

    def active_recipient_members(
        self,
        *,
        actor: VerifiedActor,
        candidate_harness_ids: tuple[str, ...],
        action: str = "message.send",
        classification: Classification = Classification.C1_INTERNAL,
        when: datetime | None = None,
    ) -> tuple[CollaborationScopeMemberVisibility, ...]:
        if (
            not candidate_harness_ids
            or len(candidate_harness_ids) > 20
            or len(candidate_harness_ids) != len(set(candidate_harness_ids))
            or any(not candidate for candidate in candidate_harness_ids)
        ):
            raise ValidationError("recipient candidates must be a bounded unique tuple")
        when, now = self._when(when, self.clock)
        with self.store.transaction() as connection:
            authority_kind, authority_id, harness_id, revision, epoch = self._require_actor(
                connection,
                actor=actor,
                when=when,
            )
            rows = connection.execute(
                """SELECT s.* FROM collaboration_scopes s
                     JOIN collaboration_scope_members m ON m.scope_id=s.scope_id
                    WHERE s.domain_id=? AND s.state='active' AND m.authority_kind=?
                      AND m.authority_id=? AND m.harness_id=? AND m.state='active'
                    ORDER BY s.scope_id""",
                (actor.domain_id, authority_kind, authority_id, harness_id),
            ).fetchall()
            result: list[CollaborationScopeMemberVisibility] = []
            for row in rows:
                if row["expires_at"] is not None and int(row["expires_at"]) <= now:
                    continue
                scope = self._scope_from_row(connection, row)
                if (
                    scope.policy_revision != revision
                    or scope.domain_revocation_epoch != epoch
                    or action not in scope.allowed_actions
                    or classification not in scope.allowed_classifications
                ):
                    continue
                visible = tuple(
                    candidate
                    for candidate in candidate_harness_ids
                    if candidate in scope.member_harness_ids
                )
                if not visible:
                    continue
                try:
                    self._require_targets(
                        connection,
                        scope=scope,
                        target_harness_ids=visible,
                        now=now,
                    )
                except AuthorizationError:
                    continue
                result.extend(
                    CollaborationScopeMemberVisibility(
                        scope_id=scope.scope_id,
                        scope_revision=scope.revision,
                        scope_policy_revision=scope.policy_revision,
                        harness_id=candidate,
                    )
                    for candidate in visible
                )
            return tuple(
                sorted(result, key=lambda item: (item.scope_id, item.harness_id))
            )


__all__ = [
    "ALLOWED_COLLABORATION_ACTIONS",
    "COLLABORATION_SCOPE_ISSUE_ACTION",
    "COLLABORATION_SCOPE_REVOKE_ACTION",
    "COLLABORATION_SCOPE_SCHEMA",
    "COMMUNICATION_SCOPE_TABLE_DDL",
    "CollaborationScope",
    "CollaborationScopeMemberVisibility",
    "CollaborationScopeProposal",
    "CollaborationScopeService",
    "CommunicationScopeService",
    "CommunicationScopeTerminalError",
    "ExactCommunicationHarnessResolver",
]
