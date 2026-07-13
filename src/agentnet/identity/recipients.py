"""Exact enrolled-recipient resolution before any custody claim."""

from __future__ import annotations

from typing import Any

from agentnet.errors import AuthorizationError
from agentnet.identity.actors import ActorKind, VerifiedActor


class RecipientResolver:
    """Resolve address strings into current enrolled harness snapshots.

    Presence is intentionally absent from this decision: an offline enrolled
    harness remains a valid durable recipient.  Revoked, cross-domain,
    workload, unknown, expired-guest, and credential-stale identifiers fail
    before an event can be accepted.
    """

    @staticmethod
    def resolve_in_transaction(
        connection: Any,
        *,
        event_actor: VerifiedActor,
        event_domain_id: str,
        recipient_id: str,
        now: int,
    ) -> dict[str, Any]:
        domain = connection.execute(
            "SELECT status,policy_revision FROM domains WHERE domain_id=?",
            (event_domain_id,),
        ).fetchone()
        harness = connection.execute(
            "SELECT * FROM harnesses WHERE harness_id=? AND domain_id=?",
            (recipient_id, event_domain_id),
        ).fetchone()
        if domain is None or domain["status"] != "active" or harness is None:
            raise AuthorizationError("recipient is not a current enrolled address")
        synthetic_delivery = (
            event_actor.kind is ActorKind.WORKLOAD
            and event_actor.binding_assurance == "synthetic_lab"
        )
        allowed_status = "deterministic_only" if synthetic_delivery else "active"
        if harness["status"] != allowed_status:
            raise AuthorizationError("recipient is not a current enrolled address")

        principal_id = harness["principal_id"]
        guest_id = harness["guest_id"]
        if (principal_id is None) == (guest_id is None):
            raise AuthorizationError("recipient enrollment owner is ambiguous")
        if principal_id is not None:
            owner = connection.execute(
                "SELECT status FROM principals WHERE principal_id=? AND domain_id=?",
                (principal_id, event_domain_id),
            ).fetchone()
            if owner is None or owner["status"] != "active":
                raise AuthorizationError("recipient is not a current enrolled address")
            owner_kind = "human_principal"
            owner_id = principal_id
        else:
            owner = connection.execute(
                """SELECT status,expires_at FROM guests
                     WHERE guest_id=? AND host_domain_id=?""",
                (guest_id, event_domain_id),
            ).fetchone()
            if owner is None or owner["status"] != "active" or int(owner["expires_at"]) <= now:
                raise AuthorizationError("recipient is not a current enrolled address")
            owner_kind = "host_guest"
            owner_id = guest_id

        credential = connection.execute(
            """SELECT credential_id,key_id,epoch,not_before,expires_at,status
                 FROM credentials
                WHERE harness_id=? AND epoch=?
                ORDER BY credential_id LIMIT 1""",
            (recipient_id, int(harness["credential_epoch"])),
        ).fetchone()
        if (
            credential is None
            or credential["status"] != "active"
            or int(credential["not_before"]) > now
            or int(credential["expires_at"]) <= now
        ):
            raise AuthorizationError("recipient is not a current enrolled address")
        return {
            "binding_assurance": harness["binding_assurance"],
            "credential_epoch": int(credential["epoch"]),
            "credential_id": credential["credential_id"],
            "domain_id": event_domain_id,
            "harness_id": recipient_id,
            "harness_kind": harness["kind"],
            "key_id": credential["key_id"],
            "owner_id": owner_id,
            "owner_kind": owner_kind,
            "policy_revision": int(domain["policy_revision"]),
            "resolved_at": now,
        }
