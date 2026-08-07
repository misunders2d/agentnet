"""Atomic bounded C0 BootstrapGrantPlan lifecycle."""

from __future__ import annotations

import base64
import hashlib
import json
import secrets
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import NAMESPACE_URL, uuid5

from agentnet.approval.service import IndependentApprovalVerifier, consume_independent_approval
from agentnet.authorization.bootstrap_plan import (
    BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
    BOOTSTRAP_PLAN_PROFILE,
    BootstrapPlanBeginRequest,
    BootstrapPlanBeginResult,
    BootstrapPlanCompleteResult,
    BootstrapPlanCompletionRequest,
    BootstrapPlanStatusRequest,
    BootstrapPlanStatusResult,
    bootstrap_plan_c0_binding,
    build_bootstrap_plan_transaction,
    digest_canonical,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.enrollment import ENROLLMENT_APPROVAL_PURPOSE
from agentnet.security.signatures import canonical_json
from agentnet.storage.backend import StoreBackend


ResolvedHarnesses = dict[str, Any]
HarnessResolver = Callable[[Any, VerifiedActor, int], ResolvedHarnesses]


class BootstrapPlanTerminalError(Exception):
    """Exact caller-bound plan reached an irreversible terminal state."""


class _FinalCommitExpired(Exception):
    """Rollback final authority writes before terminalizing an expired plan."""


def _hash_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


_GUIDED_CHALLENGE_KEYS = frozenset(
    {"challenge_id", "nonce", "canonical_transaction_b64"}
)
_REMOTE_GUIDED_CHALLENGE_KEYS = _GUIDED_CHALLENGE_KEYS | {"activation_mode"}


def _actor_binding(actor: VerifiedActor) -> str:
    return canonical_json(actor.model_dump(mode="json")).decode("utf-8")


def _strict_canonical_object(raw: bytes) -> dict[str, Any]:
    def pairs(values: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in values:
            if key in result:
                raise ValueError("duplicate key")
            result[key] = value
        return result

    value = json.loads(
        raw,
        object_pairs_hook=pairs,
        parse_constant=lambda _value: (_ for _ in ()).throw(ValueError("non-finite value")),
    )
    if not isinstance(value, dict) or canonical_json(value) != raw:
        raise ValueError("noncanonical object")
    return value


class ExactBootstrapHarnessResolver:
    """Resolve exactly two current guided OIDC harnesses for one principal."""

    def __init__(
        self,
        store: StoreBackend,
        approval_verifier: IndependentApprovalVerifier,
        *,
        fresh_max_age_seconds: int = 900,
        authenticated_role: Literal["fresh", "enrolled_server"] = "fresh",
    ) -> None:
        if fresh_max_age_seconds < 300 or fresh_max_age_seconds > 3_600:
            raise ValueError("fresh enrollment age must be between five minutes and one hour")
        if authenticated_role not in {"fresh", "enrolled_server"}:
            raise ValueError("authenticated harness role is invalid")
        self.store = store
        self.approval_verifier = approval_verifier
        self.fresh_max_age_seconds = fresh_max_age_seconds
        self.authenticated_role = authenticated_role

    @staticmethod
    def _denied(message: str = "guided enrollment proof is invalid") -> AuthorizationError:
        return AuthorizationError(message)

    def __call__(self, connection: Any, actor: VerifiedActor, now: int) -> ResolvedHarnesses:
        if (
            actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
            or actor.principal_id is None
            or actor.harness_id is None
            or actor.credential_id is None
            or actor.credential_epoch is None
            or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        ):
            raise self._denied("bootstrap plan actor is ineligible")
        rows = connection.execute(
            """SELECT
                t.transaction_id,t.domain_id,t.issuer,t.harness_kind,t.harness_name,
                t.public_key_pem AS transaction_public_key_pem,
                t.key_id AS transaction_key_id,
                t.binding_assurance AS transaction_binding_assurance,
                t.consumed_at AS oidc_consumed_at,t.enrollment_challenge_id,
                g.challenge_encrypted,g.approval_request_id AS enrollment_approval_request_id,
                g.approval_transaction_digest AS enrollment_approval_transaction_digest,
                ec.domain_id AS challenge_domain_id,ec.oidc_issuer,ec.oidc_subject,
                ec.verified_email,ec.harness_kind AS challenge_harness_kind,
                ec.harness_name AS challenge_harness_name,
                ec.public_key_pem AS challenge_public_key_pem,
                ec.key_id AS challenge_key_id,
                ec.transaction_digest AS challenge_transaction_digest,
                ec.approved_receipt,ec.consumed_at AS enrollment_consumed_at,
                p.principal_id,d.policy_revision,d.revocation_epoch
            FROM oidc_enrollment_transactions t
            JOIN oidc_enrollment_continuations g ON g.transaction_id=t.transaction_id
            JOIN enrollment_challenges ec ON ec.challenge_id=t.enrollment_challenge_id
            JOIN principals p
              ON p.domain_id=t.domain_id
             AND p.oidc_issuer=ec.oidc_issuer
             AND p.oidc_subject=ec.oidc_subject
            JOIN domains d ON d.domain_id=t.domain_id
            WHERE t.domain_id=? AND p.principal_id=?
              AND ec.domain_id=t.domain_id
              AND t.issuer=ec.oidc_issuer
              AND t.status='consumed' AND t.consumed_at IS NOT NULL
              AND g.status='enrolled'
              AND ec.consumed_at IS NOT NULL AND ec.approved_receipt IS NOT NULL
              AND d.status='active' AND p.status='active'
            ORDER BY t.transaction_id""",
            (actor.domain_id, actor.principal_id),
        ).fetchall()
        if len(rows) != 2:
            raise ConflictError("bootstrap plan requires exactly two guided harnesses")

        candidates: list[dict[str, Any]] = []
        for row in rows:
            challenge_id = str(row["enrollment_challenge_id"])
            expected_harness_id = str(uuid5(NAMESPACE_URL, f"agentnet:harness:{challenge_id}"))
            expected_credential_id = str(uuid5(NAMESPACE_URL, f"agentnet:credential:{challenge_id}"))
            current = connection.execute(
                """SELECT
                    h.harness_id,h.domain_id,h.principal_id,h.kind AS stored_harness_kind,
                    h.display_name,h.status AS harness_status,
                    h.binding_assurance AS stored_binding_assurance,
                    h.credential_epoch AS harness_credential_epoch,
                    c.credential_id,c.key_id AS credential_key_id,
                    c.public_key_pem AS credential_public_key_pem,
                    c.status AS credential_status,c.epoch AS credential_epoch,
                    c.not_before AS credential_not_before,c.expires_at AS credential_expires_at
                FROM harnesses h
                JOIN credentials c ON c.harness_id=h.harness_id
                WHERE h.harness_id=? AND h.domain_id=? AND h.principal_id=?
                  AND h.status='active' AND c.status='active'
                  AND c.epoch=h.credential_epoch""",
                (expected_harness_id, actor.domain_id, actor.principal_id),
            ).fetchall()
            if len(current) != 1:
                raise ConflictError(
                    "bootstrap plan harness must have one active current-epoch credential"
                )
            credential = current[0]
            try:
                if (
                    credential["harness_id"] != expected_harness_id
                    or credential["credential_id"] != expected_credential_id
                    or credential["domain_id"] != actor.domain_id
                    or credential["principal_id"] != actor.principal_id
                    or credential["stored_harness_kind"] != row["harness_kind"]
                    or credential["stored_harness_kind"] != row["challenge_harness_kind"]
                    or credential["display_name"] != row["harness_name"]
                    or credential["display_name"] != row["challenge_harness_name"]
                    or credential["stored_binding_assurance"]
                    != row["transaction_binding_assurance"]
                    or credential["stored_binding_assurance"]
                    not in {"os_bound", "hardware_bound"}
                    or int(credential["credential_epoch"])
                    != int(credential["harness_credential_epoch"])
                    or int(credential["credential_not_before"]) > now
                    or now >= int(credential["credential_expires_at"])
                    or row["transaction_public_key_pem"] != row["challenge_public_key_pem"]
                    or row["transaction_public_key_pem"] != credential["credential_public_key_pem"]
                    or row["transaction_key_id"] != row["challenge_key_id"]
                    or row["transaction_key_id"] != credential["credential_key_id"]
                    or public_key_thumbprint(str(credential["credential_public_key_pem"]))
                    != credential["credential_key_id"]
                ):
                    raise self._denied()

                protected = self.store.cipher.decrypt_json(
                    row["challenge_encrypted"],
                    purpose=f"oidc-guided-challenge:{row['transaction_id']}",
                )
                if (
                    not isinstance(protected, dict)
                    or frozenset(protected)
                    not in {_GUIDED_CHALLENGE_KEYS, _REMOTE_GUIDED_CHALLENGE_KEYS}
                    or protected["challenge_id"] != challenge_id
                    or (
                        "activation_mode" in protected
                        and protected["activation_mode"] != "remote_browser"
                    )
                ):
                    raise self._denied()
                canonical_transaction = base64.b64decode(
                    protected["canonical_transaction_b64"], validate=True
                )
                transcript = _strict_canonical_object(canonical_transaction)
                transaction_digest = hashlib.sha256(canonical_transaction).hexdigest()
                if (
                    transaction_digest != row["challenge_transaction_digest"]
                    or transaction_digest != row["enrollment_approval_transaction_digest"]
                    or transcript.get("schema") != "agentnet.enrollment.challenge.v1"
                    or transcript.get("challenge_id") != challenge_id
                    or transcript.get("domain_id") != actor.domain_id
                    or transcript.get("human")
                    != {
                        "oidc_issuer": row["oidc_issuer"],
                        "oidc_subject": row["oidc_subject"],
                        "verified_email": row["verified_email"],
                    }
                    or transcript.get("harness")
                    != {
                        "binding_assurance": row["transaction_binding_assurance"],
                        "display_name": row["harness_name"],
                        "kind": row["harness_kind"],
                        "requested_capabilities": [],
                        "requested_class": "protected_business",
                    }
                    or transcript.get("candidate_key")
                    != {
                        "algorithm": "ES256/P-256",
                        "thumbprint": row["transaction_key_id"],
                    }
                    or transcript.get("nonce") != protected["nonce"]
                    or transcript.get("purpose") != "human_harness_credential_binding"
                ):
                    raise self._denied()
                receipt_bytes = str(row["approved_receipt"]).encode("utf-8")
                receipt_value = _strict_canonical_object(receipt_bytes)
                consumed_at = int(row["enrollment_consumed_at"])
                oidc_consumed_at = int(row["oidc_consumed_at"])
                receipt = self.approval_verifier.verify(
                    canonical_transaction=canonical_transaction,
                    approval=receipt_value,
                    expected_purpose=ENROLLMENT_APPROVAL_PURPOSE,
                    expected_domain_id=actor.domain_id,
                    when=datetime.fromtimestamp(consumed_at, UTC),
                )
                if not receipt.issued_at <= consumed_at < receipt.expires_at:
                    raise self._denied()
            except (AuthorizationError, ConflictError):
                raise
            except Exception as exc:
                raise self._denied() from exc

            candidates.append(
                {
                    "harness": {
                        "harness_id": expected_harness_id,
                        "credential_id": expected_credential_id,
                        "credential_epoch": int(credential["credential_epoch"]),
                        "binding_assurance": credential["stored_binding_assurance"],
                        "display_name": credential["display_name"],
                        "kind": credential["stored_harness_kind"],
                    },
                    "evidence": {
                        "schema": "agentnet.bootstrap-plan.enrollment-evidence.v1",
                        "role": "",
                        "guided_oidc": True,
                        "enrollment_challenge_id": challenge_id,
                        "oidc_transaction_id": row["transaction_id"],
                        "enrollment_consumed_at": consumed_at,
                        "oidc_consumed_at": oidc_consumed_at,
                        "oidc_issuer": row["oidc_issuer"],
                        "oidc_subject_sha256": _hash_text(str(row["oidc_subject"])),
                        "verified_email_sha256": _hash_text(str(row["verified_email"])),
                        "candidate_key_thumbprint": row["transaction_key_id"],
                        "approval_purpose": receipt.approval_purpose,
                        "approval_receipt_id": receipt.receipt_id,
                        "approval_receipt_digest": hashlib.sha256(receipt_bytes).hexdigest(),
                        "approval_verifier_id": receipt.verifier_id,
                        "approval_signer_key_id": receipt.signer_key_id,
                        "approval_authenticated_at": receipt.authenticated_at,
                        "approval_issued_at": receipt.issued_at,
                    },
                }
            )

        authenticated = [
            item
            for item in candidates
            if item["harness"]["harness_id"] == actor.harness_id
            and item["harness"]["credential_id"] == actor.credential_id
            and item["harness"]["credential_epoch"] == actor.credential_epoch
            and item["harness"]["binding_assurance"] == actor.binding_assurance
        ]
        if len(authenticated) != 1:
            raise self._denied("authenticated actor does not match the required harness")
        authenticated_item = authenticated[0]
        peer_item = next(item for item in candidates if item is not authenticated_item)
        if self.authenticated_role == "fresh":
            fresh_item = authenticated_item
            owner_item = peer_item
        else:
            owner_item = authenticated_item
            fresh_item = peer_item
        if (
            now - int(fresh_item["evidence"]["oidc_consumed_at"])
            > self.fresh_max_age_seconds
            or now - int(fresh_item["evidence"]["enrollment_consumed_at"])
            > self.fresh_max_age_seconds
            or int(fresh_item["evidence"]["oidc_consumed_at"]) > now
            or int(fresh_item["evidence"]["enrollment_consumed_at"]) > now
        ):
            raise self._denied("resolved fresh enrollment is stale")
        fresh_item["evidence"]["role"] = "fresh"
        owner_item["evidence"]["role"] = "owner"
        return {
            "domain": {
                "domain_id": actor.domain_id,
                "policy_revision": int(rows[0]["policy_revision"]),
                "revocation_epoch": int(rows[0]["revocation_epoch"]),
            },
            "principal": {"principal_id": actor.principal_id},
            "harnesses": {
                "owner": owner_item["harness"],
                "fresh": fresh_item["harness"],
            },
            "enrollment_evidence": {
                "owner": owner_item["evidence"],
                "fresh": fresh_item["evidence"],
            },
        }


class BootstrapPlanService:
    """Reserve, approve, and atomically commit one unusable-until-S5 C0 plan."""

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
                "bootstrap_plan",
                "bounded bootstrap plan requires independent WebAuthn approval",
            )
        if not public_approval_url.startswith("https://") or not public_approval_url.endswith(
            "/approval"
        ):
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
            or actor.binding_assurance not in {"os_bound", "hardware_bound"}
        ):
            raise AuthorizationError("bootstrap plan denied")

    def _row_for_begin(self, connection: Any, key_hash: str) -> Any:
        return connection.execute(
            "SELECT * FROM bootstrap_grant_plans WHERE begin_idempotency_key_sha256=?",
            (key_hash,),
        ).fetchone()

    @staticmethod
    def _require_row_actor(row: Any, actor: VerifiedActor) -> None:
        if row is None or not secrets.compare_digest(
            str(row["actor_binding_json"]), _actor_binding(actor)
        ):
            raise ConflictError("bootstrap plan idempotency conflict")

    def _begin_storage(self, row: Any) -> tuple[str | None, dict[str, Any] | None]:
        encrypted = row["begin_response_encrypted"]
        if not encrypted:
            return None, None
        value = self.store.cipher.decrypt_json(
            encrypted, purpose=f"bootstrap-plan-begin:{row['plan_id']}"
        )
        if isinstance(value, dict) and value.get("schema") == "agentnet.bootstrap-plan.begin-storage.v1":
            if set(value) != {"schema", "approval_possession_secret", "result"}:
                raise GateBlocked("bootstrap_plan", "bootstrap plan reservation is invalid")
            possession = value.get("approval_possession_secret")
            if (
                not isinstance(possession, str)
                or not 32 <= len(possession) <= 128
                or any(ord(character) < 0x21 or ord(character) > 0x7E for character in possession)
            ):
                raise GateBlocked("bootstrap_plan", "bootstrap plan reservation is invalid")
            result_value = value.get("result")
            result = (
                None
                if result_value is None
                else BootstrapPlanBeginResult.model_validate(result_value).model_dump(by_alias=True)
            )
            return possession, result
        # Compatibility for rows created before purpose-separated possession.
        return None, BootstrapPlanBeginResult.model_validate(value).model_dump(by_alias=True)

    def _encrypt_begin_storage(
        self,
        row: Any,
        *,
        possession_secret: str,
        result: dict[str, Any] | None,
    ) -> str:
        return self.store.cipher.encrypt_json(
            {
                "schema": "agentnet.bootstrap-plan.begin-storage.v1",
                "approval_possession_secret": possession_secret,
                "result": result,
            },
            purpose=f"bootstrap-plan-begin:{row['plan_id']}",
        )

    def _stored_begin(self, row: Any) -> dict[str, Any]:
        _possession, result = self._begin_storage(row)
        if result is None:
            raise GateBlocked("bootstrap_plan", "bootstrap plan reservation is incomplete")
        return result

    def _approval_possession_secret(self, row: Any, *, legacy_fallback: str) -> str:
        possession, _result = self._begin_storage(row)
        return possession if possession is not None else legacy_fallback

    def _stored_complete(self, row: Any, expected_reservation_digest: str) -> dict[str, Any]:
        if not secrets.compare_digest(
            str(row["completion_request_digest"] or ""), expected_reservation_digest
        ):
            raise ConflictError("bootstrap plan completion conflict")
        encrypted = row["committed_result_encrypted"]
        expected_digest = row["committed_result_digest"]
        if not encrypted or not expected_digest:
            raise GateBlocked("bootstrap_plan", "bootstrap plan result is unavailable")
        value = self.store.cipher.decrypt_json(
            encrypted, purpose=f"bootstrap-plan-result:{row['plan_id']}"
        )
        if not secrets.compare_digest(digest_canonical(value), str(expected_digest)):
            raise AuthenticationError("bootstrap plan stored result denied")
        return BootstrapPlanCompleteResult.model_validate(value).model_dump(by_alias=True)

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
                "approval_purpose": BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
                "transaction_digest": transaction_digest,
            }
        )

    def _require_identity_only(self, connection: Any, *, domain_id: str, principal_id: str, now: int) -> None:
        live = connection.execute(
            """SELECT entitlement_id,action,resource_pattern,expires_at FROM entitlements
                WHERE domain_id=? AND principal_id=? AND revoked_at IS NULL
                  AND (expires_at IS NULL OR expires_at>?)""",
            (domain_id, principal_id, now),
        ).fetchall()
        checked_plans: set[str] = set()
        for entitlement in live:
            item = connection.execute(
                """SELECT i.plan_id,i.item_kind,i.action,i.resource_pattern,i.target_entitlement_id,
                          i.expires_at AS item_expires_at,p.profile,p.state,p.created_at,
                          p.domain_id AS plan_domain_id,p.principal_id AS plan_principal_id,
                          p.authority_expires_at,t.item_kind AS target_kind,
                          target.revoked_at AS target_revoked_at
                     FROM bootstrap_grant_plan_items i
                     JOIN bootstrap_grant_plans p ON p.plan_id=i.plan_id
                     LEFT JOIN bootstrap_grant_plan_items t
                       ON t.plan_id=i.plan_id AND t.entitlement_id=i.target_entitlement_id
                     LEFT JOIN entitlements target
                       ON target.entitlement_id=i.target_entitlement_id
                    WHERE i.entitlement_id=?""",
                (entitlement["entitlement_id"],),
            ).fetchone()
            valid = item is not None
            if valid:
                target_id = str(item["target_entitlement_id"] or "")
                valid = (
                    item["item_kind"] == "exact_revoke"
                    and item["action"] == "authorization.entitlement.revoke"
                    and entitlement["action"] == "authorization.entitlement.revoke"
                    and item["target_kind"] == "communication"
                    and item["target_revoked_at"] is not None
                    and item["resource_pattern"] == f"entitlement:{target_id}"
                    and entitlement["resource_pattern"] == f"entitlement:{target_id}"
                    and item["profile"] == BOOTSTRAP_PLAN_PROFILE
                    and item["state"] == "committed"
                    and item["plan_domain_id"] == domain_id
                    and item["plan_principal_id"] == principal_id
                    and int(item["authority_expires_at"]) <= int(item["created_at"]) + 3600
                    and int(item["item_expires_at"]) == int(item["authority_expires_at"])
                    and entitlement["expires_at"] is not None
                    and int(entitlement["expires_at"]) == int(item["authority_expires_at"])
                )
            if not valid:
                raise AuthorizationError("bootstrap plan requires identity-only principal")
            plan_id = str(item["plan_id"])
            if plan_id in checked_plans:
                continue
            counts = connection.execute(
                """SELECT
                       SUM(CASE WHEN i.item_kind='communication' THEN 1 ELSE 0 END) AS communication_count,
                       SUM(CASE WHEN i.item_kind='communication' AND e.revoked_at IS NOT NULL THEN 1 ELSE 0 END)
                           AS revoked_communication_count,
                       SUM(CASE WHEN i.item_kind='exact_revoke' THEN 1 ELSE 0 END) AS revoke_count
                     FROM bootstrap_grant_plan_items i
                     JOIN entitlements e ON e.entitlement_id=i.entitlement_id
                    WHERE i.plan_id=?""",
                (plan_id,),
            ).fetchone()
            if (
                counts is None
                or int(counts["communication_count"] or 0) != 5
                or int(counts["revoked_communication_count"] or 0) != 5
                or int(counts["revoke_count"] or 0) != 5
            ):
                raise AuthorizationError("bootstrap plan requires identity-only principal")
            checked_plans.add(plan_id)

    @staticmethod
    def _expire_due_uncommitted_plans(
        connection: Any,
        *,
        domain_id: str,
        principal_id: str,
        now: int,
        exclude_plan_id: str | None = None,
    ) -> None:
        exclusion = "" if exclude_plan_id is None else " AND plan_id<>?"
        params: tuple[Any, ...] = (
            now,
            domain_id,
            principal_id,
            BOOTSTRAP_PLAN_PROFILE,
            now,
        )
        if exclude_plan_id is not None:
            params += (exclude_plan_id,)
        connection.execute(
            """UPDATE bootstrap_grant_plans
                  SET state='expired',terminal_at=?
                WHERE domain_id=? AND principal_id=? AND profile=?
                  AND state IN ('reserved','pending_approval','approval_issued','completion_reserved')
                  AND approval_expires_at<=?"""
            + exclusion,
            params,
        )

    @staticmethod
    def _expire_current_plan_if_due(connection: Any, *, row: Any, now: int) -> bool:
        if (
            row["state"] in {"reserved", "pending_approval", "approval_issued", "completion_reserved"}
            and int(row["approval_expires_at"]) <= now
        ):
            connection.execute(
                "UPDATE bootstrap_grant_plans SET state='expired',terminal_at=? WHERE plan_id=?",
                (now, row["plan_id"]),
            )
            return True
        return False

    @staticmethod
    def _has_active_plan_conflict(
        connection: Any,
        *,
        domain_id: str,
        principal_id: str,
        now: int,
        exclude_plan_id: str | None = None,
    ) -> bool:
        exclusion = "" if exclude_plan_id is None else " AND plan_id<>?"
        pending_params: tuple[Any, ...] = (
            domain_id,
            principal_id,
            BOOTSTRAP_PLAN_PROFILE,
            now,
        )
        if exclude_plan_id is not None:
            pending_params += (exclude_plan_id,)
        pending = connection.execute(
            """SELECT plan_id FROM bootstrap_grant_plans
                WHERE domain_id=? AND principal_id=? AND profile=?
                  AND state IN ('reserved','pending_approval','approval_issued','completion_reserved')
                  AND approval_expires_at>?"""
            + exclusion
            + " LIMIT 1",
            pending_params,
        ).fetchone()
        if pending is not None:
            return True
        committed = connection.execute(
            """SELECT p.plan_id FROM bootstrap_grant_plans p
                WHERE p.domain_id=? AND p.principal_id=? AND p.profile=? AND p.state='committed'
                  AND EXISTS (
                      SELECT 1 FROM bootstrap_grant_plan_items i
                      JOIN entitlements e ON e.entitlement_id=i.entitlement_id
                      WHERE i.plan_id=p.plan_id AND i.item_kind='communication'
                        AND e.revoked_at IS NULL AND (e.expires_at IS NULL OR e.expires_at>?)
                  )
                LIMIT 1""",
            (domain_id, principal_id, BOOTSTRAP_PLAN_PROFILE, now),
        ).fetchone()
        return committed is not None

    def begin(self, *, actor: VerifiedActor, request: BootstrapPlanBeginRequest) -> dict[str, Any]:
        self._require_actor(actor)
        now = int(self.clock())
        key_hash = _hash_text(request.begin_idempotency_key)
        with self.store.transaction() as connection:
            existing = self._row_for_begin(connection, key_hash)
            if existing is not None:
                self._require_row_actor(existing, actor)
                if existing["state"] in {"rejected", "canceled", "expired", "invalidated"}:
                    raise BootstrapPlanTerminalError("bootstrap plan is terminal")
                if existing["state"] != "reserved":
                    return self._stored_begin(existing)
                row = existing
            else:
                resolved = self.resolver(connection, actor, now)
                domain = resolved["domain"]
                principal = resolved["principal"]
                if domain["domain_id"] != actor.domain_id or principal["principal_id"] != actor.principal_id:
                    raise AuthorizationError("bootstrap plan denied")
                self._require_identity_only(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    now=now,
                )
                self._expire_due_uncommitted_plans(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    now=now,
                )
                if self._has_active_plan_conflict(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    now=now,
                ):
                    raise ConflictError("an active bootstrap plan already exists")
                preimage = {
                    "schema": "agentnet.bootstrap-plan.preimage.v1",
                    "approval_purpose": BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
                    "profile": BOOTSTRAP_PLAN_PROFILE,
                    "profile_version": 1,
                    "begin_idempotency_key_sha256": key_hash,
                    **resolved,
                    "issued_at": now,
                    "approval_expires_at": now + 300,
                    "authority_expires_at": now + 3600,
                    "communication_ttl_seconds": 3600,
                    "max_uses": 1,
                    "independent_boundary_proven": False,
                    "c0": bootstrap_plan_c0_binding(),
                }
                transaction = build_bootstrap_plan_transaction(preimage)
                plan_id = transaction["plan_id"]
                transaction_digest = digest_canonical(transaction)
                create_key = f"core:bootstrap-plan:create:{plan_id}"
                create_digest = self._approval_create_digest(
                    key=create_key,
                    principal_id=actor.principal_id or "",
                    domain_id=actor.domain_id,
                    transaction_digest=transaction_digest,
                )
                harnesses = resolved["harnesses"]
                connection.execute(
                    """INSERT INTO bootstrap_grant_plans(
                        plan_id,profile,profile_version,domain_id,principal_id,
                        owner_harness_id,fresh_harness_id,owner_credential_id,fresh_credential_id,
                        owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                        policy_revision,actor_binding_json,canonical_plan_preimage_json,
                        final_approval_transaction_json,plan_digest,transaction_digest,
                        begin_idempotency_key_sha256,state,created_at,approval_expires_at,
                        authority_expires_at,approval_create_idempotency_key,
                        approval_create_request_digest
                    ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'reserved',?,?,?,?,?)""",
                    (
                        plan_id,
                        BOOTSTRAP_PLAN_PROFILE,
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
                        canonical_json(transaction).decode("utf-8"),
                        transaction["plan_digest"],
                        transaction_digest,
                        key_hash,
                        now,
                        now + 300,
                        now + 3600,
                        create_key,
                        create_digest,
                    ),
                )
                row = self._row_for_begin(connection, key_hash)

            possession_secret, _stored_result = self._begin_storage(row)
            if possession_secret is None:
                possession_secret = secrets.token_urlsafe(32)
                connection.execute(
                    "UPDATE bootstrap_grant_plans SET begin_response_encrypted=? WHERE plan_id=? AND state='reserved'",
                    (
                        self._encrypt_begin_storage(
                            row,
                            possession_secret=possession_secret,
                            result=None,
                        ),
                        row["plan_id"],
                    ),
                )
                row = self._row_for_begin(connection, key_hash)

        possession_secret = self._approval_possession_secret(
            row,
            legacy_fallback=request.begin_idempotency_key,
        )
        transaction_bytes = str(row["final_approval_transaction_json"]).encode("utf-8")
        created = self.approval_client.create_request(
            idempotency_key=str(row["approval_create_idempotency_key"]),
            domain_id=str(row["domain_id"]),
            approval_purpose=BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
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
        result = BootstrapPlanBeginResult(
            schema="agentnet.bootstrap-plan.begin-result.v1",
            status="approval_pending",
            approval_url=self.public_approval_url,
            expires_at=int(row["approval_expires_at"]),
        ).model_dump(by_alias=True)
        with self.store.transaction() as connection:
            current = self._row_for_begin(connection, key_hash)
            self._require_row_actor(current, actor)
            if current["state"] == "reserved":
                connection.execute(
                    """UPDATE bootstrap_grant_plans
                        SET approval_request_id=?,state='pending_approval',begin_response_encrypted=?
                        WHERE plan_id=? AND state='reserved'""",
                    (
                        created["request_id"],
                        self._encrypt_begin_storage(
                            current,
                            possession_secret=self._approval_possession_secret(
                                current,
                                legacy_fallback=request.begin_idempotency_key,
                            ),
                            result=result,
                        ),
                        current["plan_id"],
                    ),
                )
            elif current["state"] != "pending_approval":
                raise ConflictError("bootstrap plan state conflict")
            return self._stored_begin(self._row_for_begin(connection, key_hash))

    def status(self, *, actor: VerifiedActor, request: BootstrapPlanStatusRequest) -> dict[str, Any]:
        self._require_actor(actor)
        now = int(self.clock())
        key_hash = _hash_text(request.begin_idempotency_key)
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, key_hash)
            self._require_row_actor(row, actor)
            if row["state"] == "committed":
                return self._stored_complete(row, str(row["completion_request_digest"] or ""))
            if row["state"] in {"rejected", "canceled", "expired", "invalidated"}:
                return BootstrapPlanStatusResult(
                    schema="agentnet.bootstrap-plan.status-result.v1", status=row["state"]
                ).model_dump(by_alias=True, exclude_none=True)
            if self._expire_current_plan_if_due(connection, row=row, now=now):
                return BootstrapPlanStatusResult(
                    schema="agentnet.bootstrap-plan.status-result.v1", status="expired"
                ).model_dump(by_alias=True, exclude_none=True)
            request_id = str(row["approval_request_id"])
            transaction_digest = str(row["transaction_digest"])
        remote = self.approval_client.request_status(
            request_id=request_id, transaction_digest=transaction_digest
        )
        if (
            remote.get("request_id") != request_id
            or remote.get("transaction_digest") != transaction_digest
            or remote.get("expires_at") != row["approval_expires_at"]
        ):
            raise AuthenticationError("approval service response denied")
        mapping = {
            "pending": ("pending_approval", "approval_pending"),
            "issued": ("approval_issued", "approval_ready"),
            "rejected": ("rejected", "rejected"),
            "expired": ("expired", "expired"),
        }
        try:
            local_state, public_state = mapping[str(remote["state"])]
        except (KeyError, TypeError) as exc:
            raise AuthenticationError("approval service response denied") from exc
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, key_hash)
            self._require_row_actor(row, actor)
            if local_state == "approval_issued" and row["state"] == "pending_approval":
                connection.execute(
                    "UPDATE bootstrap_grant_plans SET state='approval_issued',approval_issued_at=? WHERE plan_id=?",
                    (int(self.clock()), row["plan_id"]),
                )
            elif local_state in {"rejected", "expired"} and row["state"] in {
                "pending_approval",
                "approval_issued",
            }:
                connection.execute(
                    "UPDATE bootstrap_grant_plans SET state=?,terminal_at=? WHERE plan_id=?",
                    (local_state, int(self.clock()), row["plan_id"]),
                )
        if public_state in {"rejected", "expired"}:
            return BootstrapPlanStatusResult(
                schema="agentnet.bootstrap-plan.status-result.v1", status=public_state
            ).model_dump(by_alias=True, exclude_none=True)
        values: dict[str, Any] = {
            "schema": "agentnet.bootstrap-plan.status-result.v1",
            "status": public_state,
            "approval_url": self.public_approval_url,
            "expires_at": int(row["approval_expires_at"]),
        }
        if public_state == "approval_ready":
            values["next_action"] = "complete_automatically"
        return BootstrapPlanStatusResult.model_validate(values).model_dump(
            by_alias=True, exclude_none=True
        )

    def complete(
        self, *, actor: VerifiedActor, request: BootstrapPlanCompletionRequest
    ) -> dict[str, Any]:
        self._require_actor(actor)
        now = int(self.clock())
        begin_hash = _hash_text(request.begin_idempotency_key)
        completion_hash = _hash_text(request.completion_idempotency_key)
        due = False
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, begin_hash)
            self._require_row_actor(row, actor)
            due = self._expire_current_plan_if_due(connection, row=row, now=now)
        if due:
            raise BootstrapPlanTerminalError("bootstrap plan is terminal")
        with self.store.transaction() as connection:
            row = self._row_for_begin(connection, begin_hash)
            self._require_row_actor(row, actor)
            reservation = {
                "schema": "agentnet.bootstrap-plan.completion-reservation.v1",
                "plan_id": row["plan_id"],
                "begin_idempotency_key_sha256": begin_hash,
                "completion_idempotency_key_sha256": completion_hash,
                "approval_request_id": row["approval_request_id"],
                "approval_purpose": BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
                "transaction_digest": row["transaction_digest"],
            }
            reservation_digest = digest_canonical(reservation)
            if row["state"] == "committed":
                return self._stored_complete(row, reservation_digest)
            if row["state"] in {"rejected", "canceled", "expired", "invalidated"}:
                raise BootstrapPlanTerminalError("bootstrap plan is terminal")
            if row["completion_request_digest"] is not None and not secrets.compare_digest(
                str(row["completion_request_digest"]), reservation_digest
            ):
                raise ConflictError("bootstrap plan completion conflict")
            if row["state"] == "approval_issued":
                connection.execute(
                    """UPDATE bootstrap_grant_plans
                        SET state='completion_reserved',completion_reserved_at=?,
                            completion_idempotency_key_sha256=?,completion_request_digest=?
                        WHERE plan_id=? AND state='approval_issued'""",
                    (now, completion_hash, reservation_digest, row["plan_id"]),
                )
            elif row["state"] != "completion_reserved":
                raise ConflictError("bootstrap plan approval is not issued")
            plan_id = str(row["plan_id"])
            request_id = str(row["approval_request_id"])
            transaction_digest = str(row["transaction_digest"])
            transaction_bytes = str(row["final_approval_transaction_json"]).encode("utf-8")
        retrieval_key = f"core:bootstrap-plan:retrieve:{plan_id}:{reservation_digest}"
        receipt_value = self.approval_client.retrieve_receipt(
            request_id=request_id,
            possession_secret=self._approval_possession_secret(
                row,
                legacy_fallback=request.begin_idempotency_key,
            ),
            domain_id=actor.domain_id,
            approval_purpose=BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
            transaction_digest=transaction_digest,
            idempotency_key=retrieval_key,
        )
        receipt = self.approval_verifier.verify(
            canonical_transaction=transaction_bytes,
            approval=receipt_value,
            expected_purpose=BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
            expected_domain_id=actor.domain_id,
            when=datetime.fromtimestamp(now, UTC),
        )
        result = BootstrapPlanCompleteResult(
            schema="agentnet.bootstrap-plan.complete-result.v1",
            status="prepared_unusable",
            authority_granted=False,
            communication_usable=False,
        ).model_dump(by_alias=True)
        commit_expired = False
        try:
            with self.store.transaction() as connection:
                row = self._row_for_begin(connection, begin_hash)
                self._require_row_actor(row, actor)
                if row["state"] == "committed":
                    return self._stored_complete(row, reservation_digest)
                if row["state"] != "completion_reserved" or not secrets.compare_digest(
                    str(row["completion_request_digest"]), reservation_digest
                ):
                    raise ConflictError("bootstrap plan completion conflict")
                commit_now = int(self.clock())
                if int(row["approval_expires_at"]) <= commit_now:
                    raise _FinalCommitExpired
                if not secrets.compare_digest(str(row["transaction_digest"]), transaction_digest):
                    raise AuthenticationError("bootstrap plan transaction denied")
                reloaded_transaction_bytes = str(row["final_approval_transaction_json"]).encode("utf-8")
                receipt = self.approval_verifier.verify(
                    canonical_transaction=reloaded_transaction_bytes,
                    approval=receipt_value,
                    expected_purpose=BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
                    expected_domain_id=actor.domain_id,
                    when=datetime.fromtimestamp(commit_now, UTC),
                )
                resolved = self.resolver(connection, actor, commit_now)
                try:
                    stored_preimage = _strict_canonical_object(
                        str(row["canonical_plan_preimage_json"]).encode("utf-8")
                    )
                except (TypeError, ValueError, json.JSONDecodeError) as exc:
                    raise AuthenticationError("bootstrap plan identity recheck denied") from exc
                stored_resolution = {
                    "domain": stored_preimage.get("domain"),
                    "principal": stored_preimage.get("principal"),
                    "harnesses": stored_preimage.get("harnesses"),
                    "enrollment_evidence": stored_preimage.get("enrollment_evidence"),
                }
                if not secrets.compare_digest(
                    canonical_json(stored_resolution), canonical_json(resolved)
                ):
                    raise AuthenticationError("bootstrap plan identity recheck denied")
                self._require_identity_only(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    now=commit_now,
                )
                self._expire_due_uncommitted_plans(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    now=commit_now,
                    exclude_plan_id=plan_id,
                )
                if self._has_active_plan_conflict(
                    connection,
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id or "",
                    now=commit_now,
                    exclude_plan_id=plan_id,
                ):
                    raise ConflictError("an active bootstrap plan already exists")
                consume_independent_approval(connection, receipt=receipt, retain_until=int(row["authority_expires_at"]))
                try:
                    transaction_value = _strict_canonical_object(
                        str(row["final_approval_transaction_json"]).encode("utf-8")
                    )
                except (TypeError, ValueError, json.JSONDecodeError) as exc:
                    raise AuthenticationError("bootstrap plan transaction denied") from exc
                guard = transaction_value["guard"]
                connection.execute(
                    """INSERT INTO c0_plan_guards(
                        guard_id,plan_id,domain_id,principal_id,owner_harness_id,fresh_harness_id,
                        owner_credential_epoch,fresh_credential_epoch,domain_revocation_epoch,
                        policy_revision,classification,request_payload_schema,
                        request_payload_schema_digest,request_payload_json,request_payload_digest,
                        reply_payload_schema,reply_payload_schema_digest,reply_payload_json,
                        reply_payload_digest,request_remaining_uses,reply_remaining_uses,state,
                        created_at,expires_at
                    ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                    (
                        guard["guard_id"], plan_id, row["domain_id"], row["principal_id"],
                        row["owner_harness_id"], row["fresh_harness_id"], row["owner_credential_epoch"],
                        row["fresh_credential_epoch"], row["domain_revocation_epoch"], row["policy_revision"],
                        "C0", canonical_json(guard["request_payload_schema"]).decode(),
                        guard["request_payload_schema_digest"], canonical_json(guard["request_payload"]).decode(),
                        guard["request_payload_digest"], canonical_json(guard["reply_payload_schema"]).decode(),
                        guard["reply_payload_schema_digest"], canonical_json(guard["reply_payload"]).decode(),
                        guard["reply_payload_digest"], 1, 1, "pending", commit_now, row["authority_expires_at"],
                    ),
                )
                scope_bindings = {
                    "fresh_to_owner_send": (row["fresh_harness_id"], row["owner_harness_id"]),
                    "owner_to_fresh_send": (row["owner_harness_id"], row["fresh_harness_id"]),
                    "owner_mailbox_read": (row["owner_harness_id"], None),
                    "owner_mailbox_acknowledge": (row["owner_harness_id"], None),
                    "fresh_mailbox_read": (row["fresh_harness_id"], None),
                    "fresh_mailbox_acknowledge": (row["fresh_harness_id"], None),
                }
                for item in transaction_value["items"]:
                    entitlement = item["entitlement"]
                    connection.execute(
                        """INSERT INTO entitlements(
                            entitlement_id,domain_id,principal_id,action,resource_pattern,expires_at,revoked_at,revision
                        ) VALUES(?,?,?,?,?,?,?,?)""",
                        (
                            entitlement["entitlement_id"], entitlement["domain_id"], entitlement["principal_id"],
                            entitlement["action"], entitlement["resource_pattern"], entitlement["expires_at"],
                            None, entitlement["revision"],
                        ),
                    )
                    connection.execute(
                        """INSERT INTO bootstrap_grant_plan_items(
                            plan_id,item_ordinal,item_id,entitlement_id,item_kind,action,resource_pattern,
                            guard_id,target_entitlement_id,item_json,expires_at
                        ) VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
                        (
                            plan_id, item["item_ordinal"], item["item_id"], entitlement["entitlement_id"],
                            item["item_kind"], item["action"], item["resource_pattern"], guard["guard_id"],
                            item.get("target_entitlement_id"), canonical_json(item).decode(), item["expires_at"],
                        ),
                    )
                    for scope in item.get("operation_scopes", []):
                        actor_harness, peer_harness = scope_bindings[scope]
                        connection.execute(
                            """INSERT INTO c0_plan_guard_entitlements(
                                guard_id,entitlement_id,operation_scope,actor_harness_id,peer_harness_id
                            ) VALUES(?,?,?,?,?)""",
                            (guard["guard_id"], entitlement["entitlement_id"], scope, actor_harness, peer_harness),
                        )
                audit_hash = self.store.append_audit(
                    connection,
                    {
                        "schema": "agentnet.audit.bootstrap-plan.v1",
                        "action": "bootstrap_plan.committed",
                        "plan_id": plan_id,
                        "domain_id": row["domain_id"],
                        "principal_id": row["principal_id"],
                        "actor_harness_id": actor.harness_id,
                        "transaction_digest": transaction_digest,
                        "approval_receipt_id": receipt.receipt_id,
                        "guard_state": "pending",
                        "entitlement_count": 10,
                    },
                )
                result_digest = digest_canonical(result)
                connection.execute(
                    """UPDATE bootstrap_grant_plans SET state='committed',approval_receipt_id=?,
                        approval_receipt_digest=?,committed_at=?,committed_result_encrypted=?,
                        committed_result_digest=?,audit_record_hash=? WHERE plan_id=?""",
                    (
                        receipt.receipt_id, digest_canonical(receipt_value), commit_now,
                        self.store.cipher.encrypt_json(result, purpose=f"bootstrap-plan-result:{plan_id}"),
                        result_digest, audit_hash, plan_id,
                    ),
                )
                return self._stored_complete(
                    self._row_for_begin(connection, begin_hash), reservation_digest
                )
        except _FinalCommitExpired:
            commit_expired = True
        if commit_expired:
            with self.store.transaction() as connection:
                row = self._row_for_begin(connection, begin_hash)
                self._require_row_actor(row, actor)
                self._expire_current_plan_if_due(connection, row=row, now=commit_now)
            raise BootstrapPlanTerminalError("bootstrap plan is terminal")
        raise AssertionError("bootstrap plan completion reached an invalid state")


__all__ = [
    "BootstrapPlanService",
    "BootstrapPlanTerminalError",
    "ExactBootstrapHarnessResolver",
    "HarnessResolver",
    "ResolvedHarnesses",
]
