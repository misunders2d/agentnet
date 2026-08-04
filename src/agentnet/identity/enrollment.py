"""Exact-transcript human, harness, and credential enrollment."""

from __future__ import annotations

import hashlib
import json
import re
import secrets
import time
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Literal
from uuid import NAMESPACE_URL, uuid4, uuid5

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    VerifiedIndependentApproval,
    consume_independent_approval,
)
from agentnet.errors import AuthenticationError, ConflictError, GateBlocked, ReplayError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import public_key_thumbprint
from agentnet.identity.domains import DomainRegistry, validate_domain_id
from agentnet.interfaces.contracts import ApprovalVerifier
from agentnet.operations.config import RuntimeProfile
from agentnet.operations.outage import OutageGate
from agentnet.operations.policy_defaults import EnrollmentApprovalPolicy, IdentityPolicy
from agentnet.security.signatures import canonical_json, verify_signature
from agentnet.storage.sqlite import SQLiteStore


BindingAssurance = Literal["lab", "os_bound", "hardware_bound"]
_EMAIL = re.compile(r"^[^\s@]+@[^\s@]+\.[^\s@]+$")
ENROLLMENT_APPROVAL_PURPOSE = "identity.enrollment.approve"


@dataclass(frozen=True, slots=True)
class VerifiedOIDCIdentity:
    """Identity claims already verified by the configured OIDC adapter."""

    issuer: str
    subject: str
    verified_email: str

    def __post_init__(self) -> None:
        if not self.issuer.startswith("https://") or len(self.issuer) > 512:
            raise ValidationError("verified OIDC issuer is outside the enrollment profile")
        if not self.subject or len(self.subject) > 512 or any(ord(character) < 0x20 for character in self.subject):
            raise ValidationError("verified OIDC subject is outside the enrollment profile")
        normalized_email = self.verified_email.strip().casefold()
        if not _EMAIL.fullmatch(normalized_email) or len(normalized_email) > 320:
            raise ValidationError("verified email is outside the enrollment profile")
        object.__setattr__(self, "verified_email", normalized_email)


@dataclass(frozen=True, slots=True)
class EnrollmentChallenge:
    challenge_id: str
    nonce: str
    expires_at: int
    canonical_transaction: bytes

    def signed_fields(self) -> dict[str, Any]:
        value = json.loads(self.canonical_transaction)
        if not isinstance(value, dict):  # pragma: no cover - constructed internally
            raise TypeError("challenge transaction is not an object")
        return value


@dataclass(frozen=True, slots=True)
class EnrollmentResult:
    principal_id: str
    harness_id: str
    credential_id: str
    key_id: str
    credential_epoch: int
    harness_status: str
    actor: VerifiedActor


def canonical_challenge_transcript(
    *,
    challenge_id: str,
    domain_id: str,
    identity: VerifiedOIDCIdentity,
    harness_kind: str,
    harness_name: str,
    key_id: str,
    binding_assurance: BindingAssurance,
    nonce: str,
    issued_at: int,
    expires_at: int,
) -> dict[str, Any]:
    """Return the only accepted enrollment challenge preimage."""

    return {
        "candidate_key": {"algorithm": "ES256/P-256", "thumbprint": key_id},
        "challenge_id": challenge_id,
        "domain_id": domain_id,
        "expires_at": expires_at,
        "harness": {
            "binding_assurance": binding_assurance,
            "display_name": harness_name,
            "kind": harness_kind,
            "requested_capabilities": [],
            "requested_class": "deterministic_control_only" if binding_assurance == "lab" else "protected_business",
        },
        "human": {
            "oidc_issuer": identity.issuer,
            "oidc_subject": identity.subject,
            "verified_email": identity.verified_email,
        },
        "issued_at": issued_at,
        "nonce": nonce,
        "purpose": "human_harness_credential_binding",
        "schema": "agentnet.enrollment.challenge.v1",
    }


class EnrollmentService:
    def __init__(
        self,
        store: SQLiteStore,
        approval_verifier: ApprovalVerifier,
        *,
        profile: RuntimeProfile = RuntimeProfile.LOCAL_CONFORMANCE,
        binding_assurance: BindingAssurance = "lab",
        challenge_ttl: int | None = None,
        credential_ttl: int = 3600,
        identity_policy: IdentityPolicy | None = None,
        approval_policy: EnrollmentApprovalPolicy | None = None,
        outage_gate: OutageGate | None = None,
        clock: Any | None = None,
    ) -> None:
        profile = RuntimeProfile(profile)
        identity_policy = identity_policy or IdentityPolicy()
        approval_policy = approval_policy or EnrollmentApprovalPolicy()
        challenge_ttl = approval_policy.transaction_ttl_seconds if challenge_ttl is None else challenge_ttl
        if binding_assurance not in {"lab", "os_bound", "hardware_bound"}:
            raise ValueError("unknown identity binding assurance")
        if challenge_ttl < 30 or challenge_ttl > 600:
            raise ValueError("enrollment challenge TTL must be between 30 and 600 seconds")
        if challenge_ttl > approval_policy.transaction_ttl_seconds:
            raise ValueError("enrollment challenge TTL exceeds the configured approval-policy ceiling")
        if credential_ttl < 60:
            raise ValueError("credential TTL must be at least 60 seconds")
        if profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and getattr(approval_verifier, "lab_only", False):
            raise GateBlocked("identity_enrollment", "always-on server-agent mode refuses the local lab approval verifier")
        if profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and binding_assurance == "lab":
            raise GateBlocked("identity_enrollment", "always-on server-agent mode refuses lab identity binding")
        if (
            profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT
            and getattr(approval_verifier, "assurance", None) != "independent_webauthn_uv"
        ):
            raise GateBlocked("identity_enrollment", "server-agent enrollment requires independent WebAuthn user verification")
        if profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and not isinstance(
            approval_verifier, IndependentApprovalVerifier
        ):
            raise GateBlocked(
                "identity_enrollment",
                "server-agent enrollment requires the production independent approval verifier",
            )
        if profile is RuntimeProfile.LOCAL_CONFORMANCE and binding_assurance != "lab":
            raise GateBlocked("identity_enrollment", "local conformance cannot claim OS or hardware binding")
        self.store = store
        self.approval_verifier = approval_verifier
        self.profile = profile
        self.binding_assurance = binding_assurance
        self.challenge_ttl = challenge_ttl
        self.credential_ttl = credential_ttl
        self.identity_policy = identity_policy
        self.approval_policy = approval_policy
        self.outage_gate = outage_gate
        self.clock = clock or (lambda: int(time.time()))

    def begin(
        self,
        *,
        domain_id: str,
        identity: VerifiedOIDCIdentity,
        harness_kind: str,
        harness_name: str,
        public_key_pem: str,
    ) -> EnrollmentChallenge:
        if self.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            raise GateBlocked(
                "oidc_verification",
                "server-agent enrollment must begin through the authorization-code verifier",
            )
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        self.validate_begin_request(
            domain_id=domain_id,
            harness_kind=harness_kind,
            harness_name=harness_name,
            public_key_pem=public_key_pem,
        )
        now = self.clock()
        with self.store.transaction() as connection:
            return self._begin_in_transaction(
                connection,
                domain_id=domain_id,
                identity=identity,
                harness_kind=harness_kind,
                harness_name=harness_name,
                public_key_pem=public_key_pem,
                now=now,
            )

    def validate_begin_request(
        self,
        *,
        domain_id: str,
        harness_kind: str,
        harness_name: str,
        public_key_pem: str,
    ) -> str:
        """Validate non-OIDC enrollment inputs and return the candidate key id."""

        validate_domain_id(domain_id)
        DomainRegistry(self.store).require_active(domain_id)
        if not harness_kind or len(harness_kind) > 64 or not harness_name or len(harness_name) > 128:
            raise ValidationError("harness identity is outside the enrollment profile")
        return public_key_thumbprint(public_key_pem)

    def _begin_in_transaction(
        self,
        connection: Any,
        *,
        domain_id: str,
        identity: VerifiedOIDCIdentity,
        harness_kind: str,
        harness_name: str,
        public_key_pem: str,
        now: int,
    ) -> EnrollmentChallenge:
        """Create a challenge in an existing OIDC-consumption transaction."""

        domain = connection.execute("SELECT status FROM domains WHERE domain_id=?", (domain_id,)).fetchone()
        if domain is None or domain["status"] != "active":
            raise AuthenticationError("trust domain is unavailable")
        key_id = public_key_thumbprint(public_key_pem)
        expires_at = now + self.challenge_ttl
        challenge_id = str(uuid4())
        nonce = secrets.token_urlsafe(32)
        transcript = canonical_challenge_transcript(
            challenge_id=challenge_id,
            domain_id=domain_id,
            identity=identity,
            harness_kind=harness_kind,
            harness_name=harness_name,
            key_id=key_id,
            binding_assurance=self.binding_assurance,
            nonce=nonce,
            issued_at=now,
            expires_at=expires_at,
        )
        canonical_transaction = canonical_json(transcript)
        nonce_hash = hashlib.sha256(nonce.encode("utf-8")).hexdigest()
        transaction_digest = hashlib.sha256(canonical_transaction).hexdigest()
        connection.execute(
            "INSERT INTO enrollment_challenges("
            "challenge_id,domain_id,oidc_issuer,oidc_subject,verified_email,harness_kind,harness_name,"
            "public_key_pem,key_id,nonce_hash,transaction_digest,expires_at"
            ") VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
            (
                challenge_id,
                domain_id,
                identity.issuer,
                identity.subject,
                identity.verified_email,
                harness_kind,
                harness_name,
                public_key_pem,
                key_id,
                nonce_hash,
                transaction_digest,
                expires_at,
            ),
        )
        self.store.append_audit(
            connection,
            {
                "action": "enrollment.challenge.created",
                "challenge_id": challenge_id,
                "domain_id": domain_id,
                "expires_at": expires_at,
                "transaction_digest": transaction_digest,
            },
        )
        return EnrollmentChallenge(challenge_id, nonce, expires_at, canonical_transaction)

    def complete(
        self,
        *,
        challenge_id: str,
        nonce: str,
        canonical_transaction: bytes,
        possession_signature: str,
        approval: Mapping[str, Any],
    ) -> EnrollmentResult:
        if self.outage_gate is not None:
            self.outage_gate.require_issuance()
        now = self.clock()
        transcript = _require_exact_canonical_transaction(canonical_transaction)
        row = self.store.fetch_one("SELECT * FROM enrollment_challenges WHERE challenge_id=?", (challenge_id,))
        if row is None:
            raise AuthenticationError("enrollment challenge is unavailable")
        try:
            self._validate_challenge(row, challenge_id, nonce, transcript, canonical_transaction, now)
            verify_signature(row["public_key_pem"], "agentnet.enrollment.pop.v1", transcript, possession_signature)
            independent_receipt: VerifiedIndependentApproval | None = None
            if isinstance(self.approval_verifier, IndependentApprovalVerifier):
                independent_receipt = self.approval_verifier.verify(
                    canonical_transaction=canonical_transaction,
                    approval=approval,
                    expected_purpose=ENROLLMENT_APPROVAL_PURPOSE,
                    expected_domain_id=row["domain_id"],
                    when=datetime.fromtimestamp(now, UTC),
                )
                approval_nonce = independent_receipt.nonce
                approval_digest = independent_receipt.transaction_digest
                approval_receipt_json = canonical_json(dict(approval)).decode("utf-8")
            else:
                approval_receipt = self.approval_verifier.verify(
                    canonical_transaction=canonical_transaction,
                    approval=approval,
                )
                approval_nonce = approval_receipt.get("nonce")
                approval_digest = approval_receipt.get("transaction_digest")
                approval_receipt_json = canonical_json(dict(approval_receipt)).decode("utf-8")
            minimum_characters = (self.approval_policy.out_of_band_min_entropy_bits + 5) // 6
            if not isinstance(approval_nonce, str) or len(approval_nonce) < minimum_characters:
                raise AuthenticationError("approval receipt lacks the configured out-of-band entropy floor")
        except (AuthenticationError, ValidationError):
            self._record_failed_attempt(challenge_id)
            raise
        transaction_digest = hashlib.sha256(canonical_transaction).hexdigest()
        if approval_digest != transaction_digest:
            raise AuthenticationError("approval verifier returned an unbound result")
        if independent_receipt is None:
            if approval_receipt.get("approved") is not True or bool(approval_receipt.get("lab_only")) != (
                self.binding_assurance == "lab"
            ):
                raise AuthenticationError("approval assurance does not match enrollment profile")
        elif self.binding_assurance == "lab":
            raise AuthenticationError("independent production approval cannot authorize a lab binding")

        harness_id = str(uuid5(NAMESPACE_URL, f"agentnet:harness:{challenge_id}"))
        credential_id = str(uuid5(NAMESPACE_URL, f"agentnet:credential:{challenge_id}"))
        with self.store.transaction() as connection:
            current = connection.execute(
                "SELECT * FROM enrollment_challenges WHERE challenge_id=?", (challenge_id,)
            ).fetchone()
            if current is None:
                raise AuthenticationError("enrollment challenge is unavailable")
            self._validate_challenge(current, challenge_id, nonce, transcript, canonical_transaction, now)
            domain = connection.execute("SELECT status FROM domains WHERE domain_id=?", (current["domain_id"],)).fetchone()
            if domain is None or domain["status"] != "active":
                raise AuthenticationError("trust domain is unavailable")
            if independent_receipt is not None:
                consume_independent_approval(connection, receipt=independent_receipt)

            principal = connection.execute(
                "SELECT * FROM principals WHERE domain_id=? AND oidc_issuer=? AND oidc_subject=?",
                (current["domain_id"], current["oidc_issuer"], current["oidc_subject"]),
            ).fetchone()
            email_owners = connection.execute(
                """SELECT DISTINCT p.principal_id,p.oidc_issuer,p.oidc_subject
                   FROM principals p LEFT JOIN principal_aliases a ON a.principal_id=p.principal_id
                   WHERE p.domain_id=? AND (p.verified_email=? OR a.verified_email=?)""",
                (current["domain_id"], current["verified_email"], current["verified_email"]),
            ).fetchall()
            if any(
                owner["oidc_issuer"] != current["oidc_issuer"]
                or owner["oidc_subject"] != current["oidc_subject"]
                for owner in email_owners
            ):
                raise ConflictError("verified email is already bound to a different OIDC subject")

            if principal is None:
                principal_id = str(uuid4())
                connection.execute(
                    "INSERT INTO principals(principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at) "
                    "VALUES(?,?,?,?,?,?,?)",
                    (
                        principal_id,
                        current["domain_id"],
                        current["oidc_issuer"],
                        current["oidc_subject"],
                        current["verified_email"],
                        "active",
                        now,
                    ),
                )
            else:
                if principal["status"] != "active":
                    raise ConflictError("existing principal binding requires explicit recovery")
                principal_id = principal["principal_id"]
                if principal["verified_email"] != current["verified_email"]:
                    connection.execute(
                        "UPDATE principals SET verified_email=? WHERE principal_id=? AND status='active'",
                        (current["verified_email"], principal_id),
                    )
                    self.store.append_audit(
                        connection,
                        {
                            "action": "principal.verified_email.changed",
                            "domain_id": current["domain_id"],
                            "principal_id": principal_id,
                        },
                    )

            connection.execute(
                """INSERT INTO principal_aliases(principal_id,verified_email,first_seen_at,last_seen_at)
                   VALUES(?,?,?,?)
                   ON CONFLICT(principal_id,verified_email) DO UPDATE SET last_seen_at=excluded.last_seen_at""",
                (principal_id, current["verified_email"], now, now),
            )

            harness_status = "deterministic_only" if self.binding_assurance == "lab" else "active"
            connection.execute(
                "INSERT INTO harnesses(harness_id,domain_id,principal_id,guest_id,kind,display_name,status,"
                "binding_assurance,capabilities_json,credential_epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
                (
                    harness_id,
                    current["domain_id"],
                    principal_id,
                    None,
                    current["harness_kind"],
                    current["harness_name"],
                    harness_status,
                    self.binding_assurance,
                    "[]",
                    1,
                    now,
                ),
            )
            connection.execute(
                "INSERT INTO credentials(credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at) "
                "VALUES(?,?,?,?,?,?,?,?)",
                (
                    credential_id,
                    harness_id,
                    current["key_id"],
                    current["public_key_pem"],
                    "active",
                    1,
                    now,
                    now + self.credential_ttl,
                ),
            )
            update = connection.execute(
                "UPDATE enrollment_challenges SET approved_receipt=?,consumed_at=? "
                "WHERE challenge_id=? AND consumed_at IS NULL",
                (approval_receipt_json, now, challenge_id),
            )
            if update.rowcount != 1:
                raise ReplayError("enrollment challenge was already consumed")
            self.store.append_audit(
                connection,
                {
                    "action": "enrollment.binding.created",
                    "challenge_id": challenge_id,
                    "credential_id": credential_id,
                    "domain_id": current["domain_id"],
                    "harness_id": harness_id,
                    "principal_id": principal_id,
                    "transaction_digest": transaction_digest,
                },
            )

        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=row["domain_id"],
            principal_id=principal_id,
            harness_id=harness_id,
            credential_id=credential_id,
            credential_epoch=1,
            binding_assurance=self.binding_assurance,
        )
        return EnrollmentResult(
            principal_id=principal_id,
            harness_id=harness_id,
            credential_id=credential_id,
            key_id=row["key_id"],
            credential_epoch=1,
            harness_status=harness_status,
            actor=actor,
        )

    def _record_failed_attempt(self, challenge_id: str) -> None:
        with self.store.transaction() as connection:
            cursor = connection.execute(
                """UPDATE enrollment_challenges SET failed_attempts=failed_attempts+1
                   WHERE challenge_id=? AND consumed_at IS NULL AND failed_attempts<?""",
                (challenge_id, self.approval_policy.maximum_attempts),
            )
            if cursor.rowcount:
                row = connection.execute(
                    "SELECT failed_attempts FROM enrollment_challenges WHERE challenge_id=?",
                    (challenge_id,),
                ).fetchone()
                self.store.append_audit(
                    connection,
                    {
                        "action": "enrollment.challenge.failed_attempt",
                        "challenge_id": challenge_id,
                        "failed_attempts": int(row["failed_attempts"]),
                    },
                )

    def _validate_challenge(
        self,
        row: Any,
        challenge_id: str,
        nonce: str,
        transcript: Mapping[str, Any],
        canonical_transaction: bytes,
        now: int,
    ) -> None:
        if int(row["failed_attempts"]) >= self.approval_policy.maximum_attempts:
            raise AuthenticationError("enrollment challenge attempt ceiling is exhausted")
        if row["consumed_at"] is not None:
            raise ReplayError("enrollment challenge was already consumed")
        if now >= row["expires_at"]:
            raise AuthenticationError("enrollment challenge is expired")
        if len(nonce) < 32 or len(nonce) > 256:
            raise AuthenticationError("enrollment challenge nonce is invalid")
        nonce_hash = hashlib.sha256(nonce.encode("utf-8")).hexdigest()
        if not secrets.compare_digest(row["nonce_hash"], nonce_hash):
            raise AuthenticationError("enrollment challenge nonce mismatch")
        digest = hashlib.sha256(canonical_transaction).hexdigest()
        if not secrets.compare_digest(row["transaction_digest"], digest):
            raise AuthenticationError("enrollment challenge transcript mismatch")
        human = transcript.get("human")
        harness = transcript.get("harness")
        candidate_key = transcript.get("candidate_key")
        expected = {
            "challenge_id": challenge_id,
            "domain_id": row["domain_id"],
            "expires_at": row["expires_at"],
            "human": {
                "oidc_issuer": row["oidc_issuer"],
                "oidc_subject": row["oidc_subject"],
                "verified_email": row["verified_email"],
            },
            "harness": {
                "binding_assurance": self.binding_assurance,
                "display_name": row["harness_name"],
                "kind": row["harness_kind"],
                "requested_capabilities": [],
                "requested_class": "deterministic_control_only"
                if self.binding_assurance == "lab"
                else "protected_business",
            },
            "candidate_key": {"algorithm": "ES256/P-256", "thumbprint": row["key_id"]},
            "nonce": nonce,
            "purpose": "human_harness_credential_binding",
            "schema": "agentnet.enrollment.challenge.v1",
        }
        for field, value in expected.items():
            if transcript.get(field) != value:
                raise AuthenticationError("enrollment challenge field mismatch")
        issued_at = transcript.get("issued_at")
        if not isinstance(issued_at, int) or issued_at >= row["expires_at"]:
            raise AuthenticationError("enrollment challenge issuance time is invalid")
        if human is None or harness is None or candidate_key is None:  # explicit type narrowing for audits
            raise AuthenticationError("enrollment challenge is incomplete")


def _require_exact_canonical_transaction(value: bytes) -> dict[str, Any]:
    try:
        decoded = json.loads(value)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValidationError("enrollment transaction is not valid canonical JSON") from exc
    if not isinstance(decoded, dict) or canonical_json(decoded) != value:
        raise ValidationError("enrollment transaction bytes are not exactly canonical")
    return decoded
