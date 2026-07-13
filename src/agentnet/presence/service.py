"""Signed/authorized presence lease storage."""

from __future__ import annotations

import time
import json
from datetime import UTC, datetime

from agentnet.authorization.policy import validate_actor_state
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.protocol.models import PresenceLease
from agentnet.security.signatures import canonical_json, verify_signature
from agentnet.storage.sqlite import SQLiteStore


class PresenceService:
    def __init__(self, store: SQLiteStore, *, max_ttl_seconds: int = 120, max_clock_skew_seconds: int = 30) -> None:
        self.store = store
        self.max_ttl_seconds = max_ttl_seconds
        self.max_clock_skew_seconds = max_clock_skew_seconds

    def update(self, lease: PresenceLease, *, actor: VerifiedActor, signature: str) -> None:
        if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            raise AuthorizationError("presence requires a verified harness actor")
        if actor.harness_id != lease.harness_id or actor.domain_id != lease.domain_id:
            raise AuthorizationError("presence lease does not bind the authenticated harness")
        now = int(time.time())
        issued_at = int(lease.issued_at.timestamp())
        expires_at = int(lease.expires_at.timestamp())
        if issued_at > now + self.max_clock_skew_seconds or issued_at < now - self.max_clock_skew_seconds:
            raise ValidationError("presence lease issue time is outside the freshness window")
        if expires_at <= issued_at or expires_at - issued_at > self.max_ttl_seconds:
            raise ValidationError("presence lease expiry exceeds the bounded lifetime")
        signed_lease = lease.model_dump(mode="json")
        with self.store.transaction() as connection:
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (actor.domain_id,),
            ).fetchone()
            if domain is None:
                raise AuthorizationError("presence domain state is absent")
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=datetime.fromtimestamp(now, UTC),
            )
            if denial is not None:
                raise AuthorizationError("presence actor is not current")
            credential = connection.execute(
                "SELECT public_key_pem FROM credentials WHERE credential_id=? AND harness_id=?",
                (actor.credential_id, actor.harness_id),
            ).fetchone()
            if credential is None:
                raise AuthenticationError("presence signing credential is absent")
            verify_signature(
                credential["public_key_pem"],
                "agentnet.presence.lease.v1",
                signed_lease,
                signature,
            )
            existing = connection.execute(
                "SELECT lease_json FROM presence_leases WHERE harness_id=?",
                (lease.harness_id,),
            ).fetchone()
            if existing is not None:
                previous = json.loads(existing["lease_json"])["lease"]
                previous_issued = int(datetime.fromisoformat(previous["issued_at"]).timestamp())
                if issued_at <= previous_issued:
                    raise ConflictError("presence lease sequence did not advance")
            connection.execute(
                """INSERT INTO presence_leases(harness_id,domain_id,lease_json,expires_at) VALUES(?,?,?,?)
                   ON CONFLICT(harness_id) DO UPDATE SET
                   domain_id=excluded.domain_id,lease_json=excluded.lease_json,expires_at=excluded.expires_at""",
                (
                    lease.harness_id,
                    lease.domain_id,
                    canonical_json(
                        {"lease": signed_lease, "actor": actor.audit_view(), "signature": signature}
                    ).decode("utf-8"),
                    expires_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "presence.lease_updated",
                    "harness_id": lease.harness_id,
                    "issued_at": issued_at,
                    "expires_at": expires_at,
                },
            )

    def state(self, harness_id: str, *, recent_window_seconds: int = 300) -> str:
        if recent_window_seconds < 0 or recent_window_seconds > 3600:
            raise ValidationError("presence recent window is outside the bounded range")
        row = self.store.fetch_one("SELECT expires_at FROM presence_leases WHERE harness_id=?", (harness_id,))
        if row is None:
            return "unknown"
        now = int(time.time())
        if row["expires_at"] > now:
            return "live"
        if row["expires_at"] + recent_window_seconds > now:
            return "recent"
        return "stale"

    def state_for(
        self,
        *,
        actor: VerifiedActor,
        harness_id: str,
        recent_window_seconds: int = 300,
    ) -> str:
        """Return presence only for a current same-domain harness.

        The ordinary ``state`` helper is intentionally useful to internal
        schedulers.  Remote callers must use this actor-aware form so an
        ``unknown`` response cannot become a cross-domain or revoked-harness
        enumeration oracle.
        """

        now = int(time.time())
        with self.store.transaction(immediate=False) as connection:
            domain = connection.execute(
                "SELECT policy_revision FROM domains WHERE domain_id=?",
                (actor.domain_id,),
            ).fetchone()
            target = connection.execute(
                "SELECT domain_id,status FROM harnesses WHERE harness_id=?",
                (harness_id,),
            ).fetchone()
            if domain is None or target is None:
                raise AuthorizationError("presence target is not visible")
            denial, _revision = validate_actor_state(
                connection,
                actor=actor,
                expected_policy_revision=int(domain["policy_revision"]),
                when=datetime.fromtimestamp(now, UTC),
            )
            if (
                denial is not None
                or target["domain_id"] != actor.domain_id
                or target["status"] != "active"
            ):
                raise AuthorizationError("presence target is not visible")
        return self.state(harness_id, recent_window_seconds=recent_window_seconds)
