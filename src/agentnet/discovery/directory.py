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
        approved_aliases = self.attributes.get("approved_aliases")
        if approved_aliases is not None:
            if not isinstance(approved_aliases, list) or not 1 <= len(approved_aliases) <= 32:
                raise ValueError("directory approved aliases must be a bounded canonical list")
            normalized_aliases: list[str] = []
            for alias in approved_aliases:
                if (
                    not isinstance(alias, str)
                    or any(ord(character) < 0x20 or ord(character) == 0x7F for character in alias)
                ):
                    raise ValueError("directory approved aliases contain an invalid value")
                normalized = " ".join(alias.casefold().split())
                if not 1 <= len(normalized) <= 256:
                    raise ValueError("directory approved aliases contain an invalid value")
                normalized_aliases.append(normalized)
            if normalized_aliases != sorted(set(normalized_aliases)):
                raise ValueError("directory approved aliases must be sorted and unique")
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

    def synchronize_agent_record_in_connection(
        self,
        connection: Any,
        *,
        domain_id: str,
        principal_id: str,
        harness_id: str,
        expires_at: int,
        now: int,
    ) -> None:
        """Create or refresh one lifecycle-owned agent projection."""

        record_id = f"agent:{harness_id}"
        row = connection.execute(
            "SELECT * FROM directory_records WHERE record_id=?",
            (record_id,),
        ).fetchone()
        if row is None:
            record = DirectoryRecord(
                record_id=record_id,
                record_type="agent",
                domain_id=domain_id,
                epoch=1,
                attributes={"harness_id": harness_id},
                visible_to_principal_ids=(principal_id,),
                expires_at=expires_at,
            )
            action = "directory.agent.materialized"
        else:
            current = DirectoryRecord.model_validate_json(row["record_json"])
            if (
                current.record_type != "agent"
                or current.domain_id != domain_id
                or current.attributes.get("harness_id") != harness_id
                or current.visible_to_principal_ids != (principal_id,)
                or current.status != "active"
            ):
                raise ConflictError("endpoint directory binding conflicted")
            if current.expires_at == expires_at:
                return
            record = current.model_copy(
                update={"epoch": current.epoch + 1, "expires_at": expires_at}
            )
            action = "directory.agent.refreshed"
        serialized = canonical_json(record.model_dump(mode="json")).decode("utf-8")
        if row is None:
            connection.execute(
                """INSERT INTO directory_records(
                       record_id,record_type,domain_id,epoch,record_json,status,
                       expires_at,updated_at
                   ) VALUES(?,?,?,?,?,'active',?,?)""",
                (
                    record.record_id,
                    record.record_type,
                    record.domain_id,
                    record.epoch,
                    serialized,
                    record.expires_at,
                    now,
                ),
            )
        else:
            updated = connection.execute(
                """UPDATE directory_records
                      SET epoch=?,record_json=?,expires_at=?,updated_at=?
                    WHERE record_id=? AND epoch=?""",
                (
                    record.epoch,
                    serialized,
                    record.expires_at,
                    now,
                    record.record_id,
                    int(row["epoch"]),
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("directory record epoch changed concurrently")
        self.store.append_audit(
            connection,
            {
                "action": action,
                "domain_id": domain_id,
                "principal_id": principal_id,
                "harness_id": harness_id,
                "record_id": record.record_id,
                "record_digest": record.digest,
                "recorded_at": now,
            },
        )

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
        ordered_types = tuple(sorted(allowed_types))
        type_placeholders = ",".join("?" for _ in ordered_types)
        if self.store.backend_name == "sqlite":
            visibility_clause = """EXISTS (
                SELECT 1
                  FROM json_each(
                      directory_records.record_json,
                      '$.visible_to_principal_ids'
                  ) AS visible
                 WHERE visible.value=?
            )"""
        elif self.store.backend_name == "postgresql":
            visibility_clause = """EXISTS (
                SELECT 1
                  FROM jsonb_array_elements_text(
                      directory_records.record_json::jsonb
                      -> 'visible_to_principal_ids'
                  ) AS visible(principal_id)
                 WHERE visible.principal_id=?
            )"""
        else:
            raise AuthorizationError("directory backend cannot enforce bounded visibility")
        with self.store.transaction(immediate=False) as connection:
            self._require_viewer(connection, actor, now=now)
            rows = connection.execute(
                f"""SELECT record_json FROM directory_records
                     WHERE domain_id=? AND status='active' AND expires_at>?
                       AND record_type IN ({type_placeholders})
                       AND {visibility_clause}
                     ORDER BY record_id LIMIT ?""",
                (actor.domain_id, now, *ordered_types, actor.principal_id, limit),
            ).fetchall()
            visible = []
            for row in rows:
                record = DirectoryRecord.model_validate_json(row["record_json"])
                if self._is_visible_to_principal(actor, record):
                    visible.append(record)
                if len(visible) >= limit:
                    break
            return visible

    def list_recipient_records(
        self,
        actor: VerifiedActor,
        *,
        limit: int = 100,
        now: int | None = None,
    ) -> tuple[DirectoryRecord, ...]:
        """Return only bounded actor-visible rows that name an exact harness."""

        records = self.list_records(
            actor,
            record_types=frozenset({"agent", "endpoint"}),
            limit=limit,
            now=now,
        )
        return tuple(
            record
            for record in records
            if isinstance(record.attributes.get("harness_id"), str)
            and 1 <= len(record.attributes["harness_id"]) <= 256
        )
