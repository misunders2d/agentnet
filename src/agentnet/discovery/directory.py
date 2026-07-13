"""Internal directory with bounded disclosure and no global list fallback."""

from __future__ import annotations

import ipaddress
import json
from datetime import UTC, datetime
from typing import Any, Literal
from urllib.parse import urlsplit

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from agentnet.authorization.evidence import IssuanceAuthority, require_current_authority_decision
from agentnet.authorization.policy import validate_actor_state
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend


class DirectoryRecord(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    record_id: str = Field(min_length=1, max_length=256)
    record_type: Literal["agent", "room", "domain", "endpoint"]
    domain_id: str = Field(min_length=1, max_length=128)
    epoch: int = Field(ge=1)
    attributes: dict[str, Any]
    # Positive data authority belongs to the verified human principal.  A
    # harness remains part of every request actor for attribution and
    # revocation, but switching harnesses cannot silently change the human's
    # directory permissions.
    visible_to_principal_ids: tuple[str, ...]
    expires_at: int = Field(ge=1)
    status: Literal["active", "revoked"] = "active"

    @field_validator("visible_to_principal_ids")
    @classmethod
    def exact_visibility(cls, value: tuple[str, ...]) -> tuple[str, ...]:
        if not value or value != tuple(sorted(set(value))):
            raise ValueError("directory visibility must be a nonempty sorted unique principal tuple")
        return value

    @model_validator(mode="after")
    def bounded_attributes(self) -> "DirectoryRecord":
        if any(token in key.casefold() for key in self.attributes for token in ("secret", "password", "token", "private")):
            raise ValueError("directory attributes cannot contain secret-bearing fields")
        if self.record_type == "endpoint":
            url = self.attributes.get("url")
            if not isinstance(url, str):
                raise ValueError("endpoint directory records require a URL")
            parsed = urlsplit(url)
            if (
                parsed.scheme not in {"https", "http"}
                or not parsed.hostname
                or parsed.username is not None
                or parsed.password is not None
                or parsed.fragment
            ):
                raise ValueError("directory endpoint URL is not a canonical credential-free HTTP endpoint")
            if parsed.scheme == "http":
                hostname = parsed.hostname.casefold()
                try:
                    is_loopback = ipaddress.ip_address(hostname).is_loopback
                except ValueError:
                    is_loopback = hostname == "localhost"
                if not is_loopback:
                    raise ValueError("remote directory endpoints require HTTPS")
        return self

    @property
    def digest(self) -> str:
        return canonical_digest(self.model_dump(mode="json"))


class DirectoryService:
    def __init__(self, store: StoreBackend) -> None:
        self.store = store

    @staticmethod
    def publication_binding(record: DirectoryRecord) -> tuple[str, dict[str, str]]:
        return f"directory:{record.record_id}", {"record_digest": record.digest}

    def publish(
        self,
        record: DirectoryRecord,
        *,
        authority: IssuanceAuthority,
        when: datetime | None = None,
    ) -> dict[str, object]:
        when = when or datetime.now(UTC)
        if authority.actor.domain_id != record.domain_id:
            raise AuthorizationError("directory publisher and record domain do not match")
        if record.expires_at <= int(when.timestamp()):
            raise ConflictError("directory record must expire in the future")
        serialized = canonical_json(record.model_dump(mode="json")).decode("utf-8")
        with self.store.transaction() as connection:
            resource, request = self.publication_binding(record)
            require_current_authority_decision(
                connection,
                authority=authority,
                expected_action="directory.publish",
                expected_resource=resource,
                expected_request=request,
                when=when,
            )
            existing = connection.execute(
                "SELECT * FROM directory_records WHERE record_id=?",
                (record.record_id,),
            ).fetchone()
            if existing is not None:
                if existing["record_json"] == serialized:
                    return {"record_id": record.record_id, "epoch": record.epoch, "duplicate": True}
                if record.epoch != int(existing["epoch"]) + 1:
                    raise ConflictError("directory record epoch is not the next coherent epoch")
            elif record.epoch != 1:
                raise ConflictError("new directory record must start at epoch one")
            connection.execute(
                """INSERT INTO directory_records(
                    record_id,record_type,domain_id,epoch,record_json,status,expires_at,updated_at
                ) VALUES(?,?,?,?,?,?,?,?)
                ON CONFLICT(record_id) DO UPDATE SET
                    record_type=excluded.record_type,domain_id=excluded.domain_id,epoch=excluded.epoch,
                    record_json=excluded.record_json,status=excluded.status,
                    expires_at=excluded.expires_at,updated_at=excluded.updated_at""",
                (
                    record.record_id,
                    record.record_type,
                    record.domain_id,
                    record.epoch,
                    serialized,
                    record.status,
                    record.expires_at,
                    int(when.timestamp()),
                ),
            )
            audit_hash = self.store.append_audit(
                connection,
                {
                    "action": "directory.record_published",
                    "actor": authority.actor.audit_view(),
                    "record_digest": record.digest,
                    "record_id": record.record_id,
                    "record_type": record.record_type,
                },
            )
        return {"record_id": record.record_id, "epoch": record.epoch, "duplicate": False, "audit_hash": audit_hash}

    def _require_viewer(self, connection, actor: VerifiedActor, *, now: int) -> None:
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS or actor.principal_id is None:
            raise AuthorizationError("directory visibility requires a verified human principal")
        domain = connection.execute(
            "SELECT policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        if domain is None:
            raise AuthorizationError("directory viewer domain is unavailable")
        denial, _revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=int(domain["policy_revision"]),
            when=datetime.fromtimestamp(now, UTC),
        )
        if denial is not None:
            raise AuthorizationError("directory viewer is not current")

    @staticmethod
    def _is_visible_to_principal(actor: VerifiedActor, record: DirectoryRecord) -> bool:
        # _require_viewer has already proved this field belongs to a current
        # credential-bound human+harness actor.  Never authorize on harness ID.
        return actor.principal_id is not None and actor.principal_id in record.visible_to_principal_ids

    def get_record(self, actor: VerifiedActor, record_id: str, *, now: int | None = None) -> DirectoryRecord:
        now = int(datetime.now(UTC).timestamp()) if now is None else now
        with self.store.transaction(immediate=False) as connection:
            self._require_viewer(connection, actor, now=now)
            row = connection.execute(
                "SELECT * FROM directory_records WHERE record_id=? AND status='active' AND expires_at>?",
                (record_id, now),
            ).fetchone()
            if row is None:
                raise AuthorizationError("directory record is not visible")
            record = DirectoryRecord.model_validate_json(row["record_json"])
            if not self._is_visible_to_principal(actor, record):
                raise AuthorizationError("directory record is not visible")
            return record

    def list_records(
        self,
        actor: VerifiedActor,
        *,
        record_types: frozenset[str] | None = None,
        limit: int = 100,
        now: int | None = None,
    ) -> list[DirectoryRecord]:
        if not 1 <= limit <= 100:
            raise ValueError("directory record limit is outside the bounded range")
        now = int(datetime.now(UTC).timestamp()) if now is None else now
        allowed_types = record_types or frozenset({"agent", "room", "domain", "endpoint"})
        if not allowed_types.issubset({"agent", "room", "domain", "endpoint"}):
            raise ValueError("directory record type filter is invalid")
        with self.store.transaction(immediate=False) as connection:
            self._require_viewer(connection, actor, now=now)
            rows = connection.execute(
                "SELECT record_json FROM directory_records WHERE status='active' AND expires_at>? ORDER BY record_id",
                (now,),
            ).fetchall()
            visible = []
            for row in rows:
                record = DirectoryRecord.model_validate_json(row["record_json"])
                if record.record_type in allowed_types and self._is_visible_to_principal(actor, record):
                    visible.append(record)
                if len(visible) >= limit:
                    break
            return visible
