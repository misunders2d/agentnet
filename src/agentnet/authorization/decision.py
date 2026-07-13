"""Durable authorization decisions.

An allow is not observable until its decision and audit record commit.  Callers
therefore pass the transaction that also contains any one-use grant update.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import UTC, datetime
from typing import Any
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field

from agentnet.identity.actors import VerifiedActor
from agentnet.security.signatures import canonical_json
from agentnet.storage.sqlite import SQLiteStore


class AuthorizationDecision(BaseModel):
    """The exact result persisted for one policy evaluation."""

    model_config = ConfigDict(extra="forbid", frozen=True)

    decision_id: str = Field(default_factory=lambda: str(uuid4()))
    occurred_at: datetime = Field(default_factory=lambda: datetime.now(UTC))
    actor: VerifiedActor
    action: str = Field(min_length=1)
    resource: dict[str, Any]
    context: dict[str, Any]
    allowed: bool
    reason: str = Field(min_length=1)
    policy_revision: int = Field(ge=0)


class DecisionRecorder:
    """Persists a decision and its hash-chained audit record in one transaction."""

    def __init__(self, store: SQLiteStore) -> None:
        self.store = store

    def record(self, connection: sqlite3.Connection, decision: AuthorizationDecision) -> AuthorizationDecision:
        # The authoritative local schema stores epoch seconds.  Normalize the
        # returned/audited object to the same boundary so a reload is exact.
        decision = decision.model_copy(
            update={"occurred_at": datetime.fromtimestamp(int(decision.occurred_at.timestamp()), UTC)}
        )
        actor_json = canonical_json(decision.actor.audit_view()).decode("utf-8")
        resource_json = canonical_json(decision.resource).decode("utf-8")
        context_json = canonical_json(decision.context).decode("utf-8")
        connection.execute(
            """
            INSERT INTO policy_decisions(
                decision_id, occurred_at, actor_json, action, resource_json,
                context_json, allowed, reason, policy_revision
            ) VALUES(?,?,?,?,?,?,?,?,?)
            """,
            (
                decision.decision_id,
                int(decision.occurred_at.timestamp()),
                actor_json,
                decision.action,
                resource_json,
                context_json,
                int(decision.allowed),
                decision.reason,
                decision.policy_revision,
            ),
        )
        self.store.append_audit(
            connection,
            {
                "type": "authorization_decision",
                "decision": decision.model_dump(mode="json"),
            },
        )
        return decision

    def get(self, decision_id: str) -> AuthorizationDecision | None:
        row = self.store.fetch_one("SELECT * FROM policy_decisions WHERE decision_id=?", (decision_id,))
        if row is None:
            return None
        return AuthorizationDecision(
            decision_id=row["decision_id"],
            occurred_at=datetime.fromtimestamp(row["occurred_at"], UTC),
            actor=VerifiedActor.model_validate(json.loads(row["actor_json"])),
            action=row["action"],
            resource=json.loads(row["resource_json"]),
            context=json.loads(row["context_json"]),
            allowed=bool(row["allowed"]),
            reason=row["reason"],
            policy_revision=row["policy_revision"],
        )
