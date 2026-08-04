"""Durable owner-approved persistent communication-scope lifecycle."""

from __future__ import annotations

import hashlib
import json
import secrets
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any

from agentnet.approval.service import IndependentApprovalVerifier, consume_independent_approval
from agentnet.authorization.bootstrap_plan_service import ExactBootstrapHarnessResolver, HarnessResolver
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
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.communication_scope_schema import COMMUNICATION_SCOPE_TABLE_DDL


class CommunicationScopeTerminalError(Exception):
    """The exact caller-bound scope reached an irreversible terminal state."""


class _FinalCommitExpired(Exception):
    pass


class ExactCommunicationHarnessResolver(ExactBootstrapHarnessResolver):
    """Reuse the exact current guided same-principal two-harness resolver."""


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
        with self.store.transaction() as connection:
            existing = self._row_for_begin(connection, key_hash)
            if existing is not None:
                self._require_row_actor(existing, actor)
                if existing["state"] in {
                    "rejected",
                    "canceled",
                    "expired",
                    "invalidated",
                }:
                    raise CommunicationScopeTerminalError("communication scope is terminal")
                if existing["state"] != "reserved":
                    return self._stored_begin(existing)
                row = existing
                self._require_current_resolution(
                    connection, row=row, actor=actor, now=now
                )
            else:
                resolved = self.resolver(connection, actor, now)
                if (
                    resolved.get("domain", {}).get("domain_id") != actor.domain_id
                    or resolved.get("principal", {}).get("principal_id")
                    != actor.principal_id
                ):
                    raise AuthorizationError("communication scope denied")
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


__all__ = [
    "COMMUNICATION_SCOPE_TABLE_DDL",
    "CommunicationScopeService",
    "CommunicationScopeTerminalError",
    "ExactCommunicationHarnessResolver",
]
