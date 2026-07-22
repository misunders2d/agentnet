"""Retained direct-construction coverage for the unmounted legacy founder path.

Ordinary AgentNet applications do not mount this wildcard first-authority
ceremony. The supported zero-state C0 profile uses the fixed bounded
``BootstrapGrantPlan`` path instead. This module remains only for explicit
legacy/lab compatibility and negative migration coverage; importing it does not
grant authority or make it a production composition path.
"""

from __future__ import annotations

import hashlib
import json
import secrets
import sqlite3
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from typing import Any, Mapping
from uuid import NAMESPACE_URL, uuid4, uuid5

from agentnet.approval.service import (
    INDEPENDENT_APPROVAL_SCHEMA,
    IndependentApprovalVerifier,
    consume_independent_approval,
)
from agentnet.authorization.grants import epoch_seconds
from agentnet.authorization.policy import HumanEntitlement, PolicyEngine, validate_actor_state
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ConflictError,
    GateBlocked,
    ReplayError,
    ValidationError,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import (
    load_credential_binding_from_connection,
    public_key_thumbprint,
)
from agentnet.identity.enrollment import ENROLLMENT_APPROVAL_PURPOSE
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.outage import OutageGate
from agentnet.security.signatures import canonical_json
from agentnet.storage.backend import StoreBackend


AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE = "authorization.entitlement.bootstrap.approve"
INITIAL_ROOT_ACTION = "authorization.entitlement.issue"
INITIAL_ROOT_RESOURCE = "*"
AUTHORITY_BOOTSTRAP_TRANSACTION_SCHEMA = "agentnet.authority-bootstrap.challenge.v1"


@dataclass(frozen=True, slots=True)
class AuthorityBootstrapChallenge:
    challenge_id: str
    nonce: str
    expires_at: datetime
    candidate_entitlement: HumanEntitlement
    canonical_transaction: bytes


@dataclass(frozen=True, slots=True)
class AuthorityBootstrapResult:
    challenge_id: str
    entitlement: HumanEntitlement
    approval_receipt_id: str


class FirstAuthorityBootstrapService:
    """Issue the one minimal initial domain root from independent approval."""

    def __init__(
        self,
        store: StoreBackend,
        policy: PolicyEngine,
        approval_verifier: IndependentApprovalVerifier,
        *,
        runtime_profile: RuntimeProfile,
        outage_gate: OutageGate | None = None,
        challenge_ttl_seconds: int = 300,
        root_entitlement_ttl_seconds: int = 3_600,
        newly_enrolled_max_age_seconds: int = 900,
    ) -> None:
        if store is not policy.store:
            raise ValueError("authority bootstrap and policy engine must share one transaction store")
        if RuntimeProfile(runtime_profile) is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            raise GateBlocked(
                "authority_bootstrap",
                "first-positive-authority bootstrap is available only in the server-agent profile",
            )
        if getattr(approval_verifier, "lab_only", True) or getattr(
            approval_verifier, "assurance", ""
        ) != "independent_webauthn_uv":
            raise GateBlocked(
                "authority_bootstrap",
                "first-positive-authority bootstrap requires the production independent WebAuthn verifier",
            )
        if challenge_ttl_seconds < 30 or challenge_ttl_seconds > 600:
            raise ValueError("authority bootstrap challenge TTL must be between 30 and 600 seconds")
        if root_entitlement_ttl_seconds <= challenge_ttl_seconds or root_entitlement_ttl_seconds > 86_400:
            raise ValueError("initial root lifetime must exceed the challenge and cannot exceed one day")
        if newly_enrolled_max_age_seconds < challenge_ttl_seconds or newly_enrolled_max_age_seconds > 3_600:
            raise ValueError("new-enrollment eligibility must cover the challenge and cannot exceed one hour")
        self.store = store
        self.policy = policy
        self.approval_verifier = approval_verifier
        self.outage_gate = outage_gate
        self.challenge_ttl_seconds = challenge_ttl_seconds
        self.root_entitlement_ttl_seconds = root_entitlement_ttl_seconds
        self.newly_enrolled_max_age_seconds = newly_enrolled_max_age_seconds

    @staticmethod
    def _require_time(when: datetime) -> int:
        if when.tzinfo is None:
            raise ValidationError("authority bootstrap time must be timezone-aware")
        return epoch_seconds(when)

    @staticmethod
    def _candidate(
        *,
        challenge_id: str,
        actor: VerifiedActor,
        policy_revision: int,
        expires_at: datetime,
    ) -> HumanEntitlement:
        return HumanEntitlement(
            entitlement_id=str(
                uuid5(
                    NAMESPACE_URL,
                    "agentnet:initial-authority-root:"
                    f"{actor.domain_id}:{actor.principal_id}:{actor.harness_id}:{challenge_id}",
                )
            ),
            domain_id=actor.domain_id,
            principal_id=actor.principal_id or "",
            action=INITIAL_ROOT_ACTION,
            resource_pattern=INITIAL_ROOT_RESOURCE,
            revision=policy_revision,
            expires_at=expires_at,
        )

    @staticmethod
    def _transaction(
        *,
        challenge_id: str,
        nonce: str,
        actor: VerifiedActor,
        credential_key_id: str,
        domain_revocation_epoch: int,
        policy_revision: int,
        candidate: HumanEntitlement,
        issued_at: int,
        expires_at: int,
    ) -> dict[str, Any]:
        return {
            "approval_purpose": AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE,
            "candidate_entitlement": candidate.model_dump(mode="json"),
            "challenge": {
                "challenge_id": challenge_id,
                "expires_at": expires_at,
                "issued_at": issued_at,
                "nonce": nonce,
            },
            "domain": {
                "domain_id": actor.domain_id,
                "policy_revision": policy_revision,
                "revocation_epoch": domain_revocation_epoch,
            },
            "harness": {
                "binding_assurance": actor.binding_assurance,
                "credential_epoch": actor.credential_epoch,
                "credential_id": actor.credential_id,
                "credential_key_id": credential_key_id,
                "harness_id": actor.harness_id,
            },
            "principal": {"principal_id": actor.principal_id},
            "schema": AUTHORITY_BOOTSTRAP_TRANSACTION_SCHEMA,
        }

    def _require_current_new_enrollment(
        self,
        connection: Any,
        *,
        actor: VerifiedActor,
        expected_policy_revision: int | None,
        when: datetime,
    ) -> tuple[int, int, str]:
        """Return current policy/domain epochs and credential key for one OIDC binding."""

        now = self._require_time(when)
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
            raise AuthorizationError("initial authority requires an enrolled human harness")
        if actor.binding_assurance not in {"os_bound", "hardware_bound"}:
            raise AuthorizationError("initial authority refuses non-production harness binding")
        domain = connection.execute(
            "SELECT * FROM domains WHERE domain_id=?", (actor.domain_id,)
        ).fetchone()
        if domain is None or domain["status"] != "active":
            raise AuthorizationError("initial authority domain is unavailable")
        policy_revision = int(domain["policy_revision"])
        if expected_policy_revision is not None and policy_revision != expected_policy_revision:
            raise ConflictError("authority bootstrap policy revision changed")
        denial, current_revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=policy_revision,
            when=when,
        )
        if denial is not None or current_revision != policy_revision:
            raise AuthorizationError(f"authority bootstrap actor is not current: {denial or 'revision_mismatch'}")

        binding = load_credential_binding_from_connection(connection, actor.credential_id or "")
        binding.require_active(now=now)
        if (
            binding.domain_id != actor.domain_id
            or binding.principal_id != actor.principal_id
            or binding.harness_id != actor.harness_id
            or binding.credential_epoch != actor.credential_epoch
            or binding.binding_assurance != actor.binding_assurance
            or binding.key_id != public_key_thumbprint(binding.public_key_pem)
        ):
            raise AuthenticationError("authority bootstrap credential binding mismatch")

        rows = connection.execute(
            """
            SELECT
                ec.challenge_id AS enrollment_challenge_id,
                ec.oidc_issuer AS enrollment_issuer,
                ec.oidc_subject AS enrollment_subject,
                ec.verified_email AS enrollment_email,
                ec.harness_kind AS enrollment_harness_kind,
                ec.harness_name AS enrollment_harness_name,
                ec.public_key_pem AS enrollment_public_key_pem,
                ec.key_id AS enrollment_key_id,
                ec.approved_receipt AS enrollment_approval_receipt,
                ec.consumed_at AS enrollment_consumed_at,
                ot.domain_id AS oidc_domain_id,
                ot.issuer AS oidc_issuer,
                ot.harness_kind AS oidc_harness_kind,
                ot.harness_name AS oidc_harness_name,
                ot.public_key_pem AS oidc_public_key_pem,
                ot.key_id AS oidc_key_id,
                ot.binding_assurance AS oidc_binding_assurance,
                ot.status AS oidc_status,
                ot.consumed_at AS oidc_consumed_at
            FROM enrollment_challenges AS ec
            JOIN oidc_enrollment_transactions AS ot
              ON ot.enrollment_challenge_id=ec.challenge_id
            WHERE ec.domain_id=? AND ec.key_id=? AND ec.consumed_at IS NOT NULL
            """,
            (actor.domain_id, binding.key_id),
        ).fetchall()
        matches: list[Any] = []
        for row in rows:
            enrollment_challenge_id = str(row["enrollment_challenge_id"])
            expected_harness = str(uuid5(NAMESPACE_URL, f"agentnet:harness:{enrollment_challenge_id}"))
            expected_credential = str(uuid5(NAMESPACE_URL, f"agentnet:credential:{enrollment_challenge_id}"))
            if expected_harness == actor.harness_id and expected_credential == actor.credential_id:
                matches.append(row)
        if len(matches) != 1:
            raise AuthorizationError("actor lacks one exact completed OIDC enrollment")
        enrollment = matches[0]

        principal = connection.execute(
            "SELECT * FROM principals WHERE principal_id=?", (actor.principal_id,)
        ).fetchone()
        harness = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=?", (actor.harness_id,)
        ).fetchone()
        consumed_at = enrollment["enrollment_consumed_at"]
        if (
            principal is None
            or harness is None
            or principal["status"] != "active"
            or harness["status"] != "active"
            or harness["principal_id"] != actor.principal_id
            or harness["domain_id"] != actor.domain_id
            or harness["binding_assurance"] != actor.binding_assurance
            or harness["kind"] != enrollment["enrollment_harness_kind"]
            or harness["display_name"] != enrollment["enrollment_harness_name"]
            or int(harness["credential_epoch"]) != actor.credential_epoch
            or principal["oidc_issuer"] != enrollment["enrollment_issuer"]
            or principal["oidc_subject"] != enrollment["enrollment_subject"]
            or principal["verified_email"] != enrollment["enrollment_email"]
            or consumed_at is None
            or int(consumed_at) > now
            or now - int(consumed_at) > self.newly_enrolled_max_age_seconds
        ):
            raise AuthorizationError("actor is not one exact newly enrolled human harness")
        if (
            enrollment["oidc_status"] != "consumed"
            or enrollment["oidc_consumed_at"] is None
            or enrollment["oidc_domain_id"] != actor.domain_id
            or enrollment["oidc_issuer"] != enrollment["enrollment_issuer"]
            or enrollment["oidc_harness_kind"] != harness["kind"]
            or enrollment["oidc_harness_name"] != harness["display_name"]
            or enrollment["oidc_public_key_pem"] != binding.public_key_pem
            or enrollment["enrollment_public_key_pem"] != binding.public_key_pem
            or enrollment["oidc_key_id"] != binding.key_id
            or enrollment["enrollment_key_id"] != binding.key_id
            or enrollment["oidc_binding_assurance"] != actor.binding_assurance
        ):
            raise AuthorizationError("OIDC enrollment evidence does not bind the current credential")
        try:
            enrollment_receipt = json.loads(enrollment["enrollment_approval_receipt"])
        except (TypeError, ValueError):
            raise AuthorizationError("OIDC enrollment lacks independent approval evidence") from None
        if (
            not isinstance(enrollment_receipt, dict)
            or enrollment_receipt.get("schema") != INDEPENDENT_APPROVAL_SCHEMA
            or enrollment_receipt.get("approved") is not True
            or enrollment_receipt.get("approval_purpose") != ENROLLMENT_APPROVAL_PURPOSE
            or enrollment_receipt.get("domain_id") != actor.domain_id
            or enrollment_receipt.get("authentication_method") != "webauthn_uv"
        ):
            raise AuthorizationError("OIDC enrollment approval assurance is invalid")
        return policy_revision, int(domain["revocation_epoch"]), binding.key_id

    @staticmethod
    def _has_current_root(
        connection: Any,
        *,
        domain_id: str,
        revision: int,
        now: int,
    ) -> bool:
        row = connection.execute(
            """
            SELECT entitlement_id FROM entitlements
             WHERE domain_id=? AND action=? AND resource_pattern=?
               AND revision=? AND revoked_at IS NULL
               AND (expires_at IS NULL OR expires_at>?)
             LIMIT 1
            """,
            (domain_id, INITIAL_ROOT_ACTION, INITIAL_ROOT_RESOURCE, revision, now),
        ).fetchone()
        return row is not None

    def begin(
        self,
        *,
        actor: VerifiedActor,
        when: datetime | None = None,
    ) -> AuthorityBootstrapChallenge:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        when = when or datetime.now(UTC)
        now = self._require_time(when)
        challenge_id = str(uuid4())
        nonce = secrets.token_urlsafe(32)
        expires_at = now + self.challenge_ttl_seconds
        with self.store.transaction() as connection:
            policy_revision, domain_revocation_epoch, credential_key_id = (
                self._require_current_new_enrollment(connection, actor=actor, expected_policy_revision=None, when=when)
            )
            if self._has_current_root(
                connection,
                domain_id=actor.domain_id,
                revision=policy_revision,
                now=now,
            ):
                raise ConflictError("a current initial authority root already exists")
            candidate = self._candidate(
                challenge_id=challenge_id,
                actor=actor,
                policy_revision=policy_revision,
                expires_at=when + timedelta(seconds=self.root_entitlement_ttl_seconds),
            )
            transaction = self._transaction(
                challenge_id=challenge_id,
                nonce=nonce,
                actor=actor,
                credential_key_id=credential_key_id,
                domain_revocation_epoch=domain_revocation_epoch,
                policy_revision=policy_revision,
                candidate=candidate,
                issued_at=now,
                expires_at=expires_at,
            )
            canonical = canonical_json(transaction)
            transaction_digest = hashlib.sha256(canonical).hexdigest()
            connection.execute(
                """
                INSERT INTO authority_bootstrap_challenges(
                    challenge_id,domain_id,principal_id,harness_id,credential_id,
                    credential_epoch,credential_key_id,binding_assurance,
                    domain_revocation_epoch,policy_revision,candidate_entitlement_id,
                    candidate_entitlement_json,nonce_hash,transaction_digest,
                    created_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
                """,
                (
                    challenge_id,
                    actor.domain_id,
                    actor.principal_id,
                    actor.harness_id,
                    actor.credential_id,
                    actor.credential_epoch,
                    credential_key_id,
                    actor.binding_assurance,
                    domain_revocation_epoch,
                    policy_revision,
                    candidate.entitlement_id,
                    canonical_json(candidate.model_dump(mode="json")).decode("utf-8"),
                    hashlib.sha256(nonce.encode("utf-8")).hexdigest(),
                    transaction_digest,
                    now,
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "authorization.initial_root.challenge.created",
                    "candidate_entitlement_id": candidate.entitlement_id,
                    "challenge_id": challenge_id,
                    "domain_id": actor.domain_id,
                    "harness_id": actor.harness_id,
                    "policy_revision": policy_revision,
                    "principal_id": actor.principal_id,
                    "transaction_digest": transaction_digest,
                },
            )
        return AuthorityBootstrapChallenge(
            challenge_id=challenge_id,
            nonce=nonce,
            expires_at=datetime.fromtimestamp(expires_at, UTC),
            candidate_entitlement=candidate,
            canonical_transaction=canonical,
        )

    @staticmethod
    def _candidate_from_row(row: Any) -> HumanEntitlement:
        try:
            candidate = HumanEntitlement.model_validate_json(row["candidate_entitlement_json"])
        except Exception as exc:
            raise AuthenticationError("authority bootstrap candidate state is invalid") from exc
        if (
            candidate.entitlement_id != row["candidate_entitlement_id"]
            or candidate.domain_id != row["domain_id"]
            or candidate.principal_id != row["principal_id"]
            or candidate.action != INITIAL_ROOT_ACTION
            or candidate.resource_pattern != INITIAL_ROOT_RESOURCE
            or candidate.revision != int(row["policy_revision"])
            or candidate.expires_at is None
            or candidate.revoked_at is not None
        ):
            raise AuthenticationError("authority bootstrap candidate is not the minimal exact root")
        return candidate

    def _canonical_from_row(self, row: Any, *, nonce: str) -> bytes:
        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=row["domain_id"],
            principal_id=row["principal_id"],
            harness_id=row["harness_id"],
            credential_id=row["credential_id"],
            credential_epoch=int(row["credential_epoch"]),
            binding_assurance=row["binding_assurance"],
        )
        candidate = self._candidate_from_row(row)
        return canonical_json(
            self._transaction(
                challenge_id=row["challenge_id"],
                nonce=nonce,
                actor=actor,
                credential_key_id=row["credential_key_id"],
                domain_revocation_epoch=int(row["domain_revocation_epoch"]),
                policy_revision=int(row["policy_revision"]),
                candidate=candidate,
                issued_at=int(row["created_at"]),
                expires_at=int(row["expires_at"]),
            )
        )

    def _require_exact_submission(
        self,
        row: Any,
        *,
        challenge_id: str,
        nonce: str,
        canonical_transaction: bytes,
        now: int,
    ) -> bytes:
        if row is None or row["challenge_id"] != challenge_id:
            raise AuthenticationError("authority bootstrap challenge is unavailable")
        if row["consumed_at"] is not None:
            raise ReplayError("authority bootstrap challenge was already consumed")
        if now >= int(row["expires_at"]):
            raise AuthenticationError("authority bootstrap challenge is expired")
        if not isinstance(nonce, str) or len(nonce) < 32 or len(nonce) > 256:
            raise AuthenticationError("authority bootstrap nonce is invalid")
        if not secrets.compare_digest(
            hashlib.sha256(nonce.encode("utf-8")).hexdigest(), row["nonce_hash"]
        ):
            raise AuthenticationError("authority bootstrap nonce mismatch")
        try:
            decoded = json.loads(canonical_transaction)
        except (UnicodeDecodeError, ValueError):
            raise ValidationError("authority bootstrap transaction is not canonical JSON") from None
        if not isinstance(decoded, dict) or canonical_json(decoded) != canonical_transaction:
            raise ValidationError("authority bootstrap transaction bytes are not exactly canonical")
        expected = self._canonical_from_row(row, nonce=nonce)
        digest = hashlib.sha256(canonical_transaction).hexdigest()
        if (
            not secrets.compare_digest(expected, canonical_transaction)
            or not secrets.compare_digest(digest, row["transaction_digest"])
        ):
            raise AuthenticationError("authority bootstrap transaction binding mismatch")
        return expected

    def complete(
        self,
        *,
        actor: VerifiedActor,
        challenge_id: str,
        nonce: str,
        canonical_transaction: bytes,
        approval: Mapping[str, Any],
        when: datetime | None = None,
    ) -> AuthorityBootstrapResult:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        when = when or datetime.now(UTC)
        now = self._require_time(when)
        initial = self.store.fetch_one(
            "SELECT * FROM authority_bootstrap_challenges WHERE challenge_id=?",
            (challenge_id,),
        )
        exact_transaction = self._require_exact_submission(
            initial,
            challenge_id=challenge_id,
            nonce=nonce,
            canonical_transaction=canonical_transaction,
            now=now,
        )
        receipt = self.approval_verifier.verify(
            canonical_transaction=exact_transaction,
            approval=approval,
            expected_purpose=AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE,
            expected_domain_id=actor.domain_id,
            when=when,
        )
        if receipt.approver_principal_id == actor.principal_id:
            raise AuthorizationError(
                "initial authority beneficiary cannot approve their own bootstrap"
            )

        with self.store.transaction() as connection:
            row = connection.execute(
                "SELECT * FROM authority_bootstrap_challenges WHERE challenge_id=?",
                (challenge_id,),
            ).fetchone()
            self._require_exact_submission(
                row,
                challenge_id=challenge_id,
                nonce=nonce,
                canonical_transaction=canonical_transaction,
                now=now,
            )
            expected_actor = VerifiedActor(
                kind=ActorKind.VERIFIED_HUMAN_HARNESS,
                domain_id=row["domain_id"],
                principal_id=row["principal_id"],
                harness_id=row["harness_id"],
                credential_id=row["credential_id"],
                credential_epoch=int(row["credential_epoch"]),
                binding_assurance=row["binding_assurance"],
            )
            if not secrets.compare_digest(
                canonical_json(actor.audit_view()), canonical_json(expected_actor.audit_view())
            ):
                raise AuthenticationError("authority bootstrap actor substitution rejected")
            policy_revision, domain_revocation_epoch, credential_key_id = self._require_current_new_enrollment(
                connection,
                actor=actor,
                expected_policy_revision=int(row["policy_revision"]),
                when=when,
            )
            if (
                domain_revocation_epoch != int(row["domain_revocation_epoch"])
                or credential_key_id != row["credential_key_id"]
            ):
                raise ConflictError("authority bootstrap epoch or credential changed")
            if self._has_current_root(
                connection,
                domain_id=actor.domain_id,
                revision=policy_revision,
                now=now,
            ):
                raise ConflictError("a current initial authority root already exists")

            candidate = self._candidate_from_row(row)
            if candidate.expires_at is None or epoch_seconds(candidate.expires_at) <= now:
                raise AuthenticationError("authority bootstrap candidate entitlement is expired")
            consume_independent_approval(connection, receipt=receipt)
            issued = self.policy._insert_entitlement_in_transaction(
                connection,
                candidate,
                when=when,
                audit_record={
                    "action": "authorization.initial_root.issued",
                    "approval_receipt_id": receipt.receipt_id,
                    "approval_signer_key_id": receipt.signer_key_id,
                    "approval_verifier_id": receipt.verifier_id,
                    "challenge_id": challenge_id,
                    "credential_epoch": actor.credential_epoch,
                    "credential_key_id": credential_key_id,
                    "domain_revocation_epoch": domain_revocation_epoch,
                    "entitlement": candidate.model_dump(mode="json"),
                    "harness_id": actor.harness_id,
                    "policy_revision": policy_revision,
                    "principal_id": actor.principal_id,
                    "transaction_digest": row["transaction_digest"],
                },
            )

            slot = connection.execute(
                "SELECT * FROM authority_bootstrap_slots WHERE domain_id=?",
                (actor.domain_id,),
            ).fetchone()
            try:
                if slot is None:
                    connection.execute(
                        """INSERT INTO authority_bootstrap_slots(
                               domain_id,current_entitlement_id,generation,updated_at
                           ) VALUES(?,?,1,?)""",
                        (actor.domain_id, candidate.entitlement_id, now),
                    )
                else:
                    updated = connection.execute(
                        """UPDATE authority_bootstrap_slots
                              SET current_entitlement_id=?,generation=generation+1,updated_at=?
                            WHERE domain_id=? AND current_entitlement_id=?""",
                        (
                            candidate.entitlement_id,
                            now,
                            actor.domain_id,
                            slot["current_entitlement_id"],
                        ),
                    )
                    if updated.rowcount != 1:
                        raise ConflictError("authority bootstrap singleton raced")
            except Exception as exc:
                if isinstance(exc, ConflictError):
                    raise
                if isinstance(exc, sqlite3.IntegrityError) or exc.__class__.__name__ == "UniqueViolation":
                    raise ConflictError("authority bootstrap singleton raced") from exc
                raise

            approval_receipt_digest = hashlib.sha256(canonical_json(dict(approval))).hexdigest()
            updated = connection.execute(
                """UPDATE authority_bootstrap_challenges
                      SET consumed_at=?,approval_receipt_id=?,approval_receipt_digest=?
                    WHERE challenge_id=? AND consumed_at IS NULL""",
                (now, receipt.receipt_id, approval_receipt_digest, challenge_id),
            )
            if updated.rowcount != 1:
                raise ReplayError("authority bootstrap challenge was concurrently consumed")
            self.store.append_audit(
                connection,
                {
                    "action": "authorization.initial_root.challenge.consumed",
                    "approval_receipt_digest": approval_receipt_digest,
                    "approval_receipt_id": receipt.receipt_id,
                    "challenge_id": challenge_id,
                    "entitlement_id": candidate.entitlement_id,
                    "transaction_digest": row["transaction_digest"],
                },
            )
        return AuthorityBootstrapResult(
            challenge_id=challenge_id,
            entitlement=issued,
            approval_receipt_id=receipt.receipt_id,
        )


__all__ = [
    "AUTHORITY_BOOTSTRAP_APPROVAL_PURPOSE",
    "AUTHORITY_BOOTSTRAP_TRANSACTION_SCHEMA",
    "AuthorityBootstrapChallenge",
    "AuthorityBootstrapResult",
    "FirstAuthorityBootstrapService",
    "INITIAL_ROOT_ACTION",
    "INITIAL_ROOT_RESOURCE",
]
