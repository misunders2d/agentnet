"""Domain registry operations for the bounded identity lane."""

from __future__ import annotations

import re
import time
from dataclasses import dataclass

from agentnet.errors import AuthenticationError, ConflictError, ValidationError
from agentnet.storage.sqlite import SQLiteStore


_DOMAIN_ID = re.compile(r"^[a-z0-9][a-z0-9.-]{2,127}$")


@dataclass(frozen=True, slots=True)
class DomainRecord:
    domain_id: str
    status: str
    policy_revision: int
    revocation_epoch: int
    created_at: int


def validate_domain_id(domain_id: str) -> str:
    if not _DOMAIN_ID.fullmatch(domain_id):
        raise ValidationError("domain identifier is outside the canonical profile")
    return domain_id


class DomainRegistry:
    def __init__(self, store: SQLiteStore) -> None:
        self.store = store

    def register(self, domain_id: str, *, now: int | None = None) -> DomainRecord:
        """Register a local trust domain without changing an existing domain."""

        domain_id = validate_domain_id(domain_id)
        created_at = int(time.time()) if now is None else now
        with self.store.transaction() as connection:
            row = connection.execute("SELECT * FROM domains WHERE domain_id=?", (domain_id,)).fetchone()
            if row is None:
                connection.execute(
                    "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
                    "VALUES(?,?,?,?,?)",
                    (domain_id, "active", 1, 1, created_at),
                )
                self.store.append_audit(
                    connection,
                    {"action": "domain.registered", "domain_id": domain_id, "status": "active"},
                )
                row = connection.execute("SELECT * FROM domains WHERE domain_id=?", (domain_id,)).fetchone()
            elif row["status"] != "active":
                raise ConflictError("existing domain is not active")
        assert row is not None
        return _record(row)

    def get(self, domain_id: str) -> DomainRecord | None:
        validate_domain_id(domain_id)
        row = self.store.fetch_one("SELECT * FROM domains WHERE domain_id=?", (domain_id,))
        return _record(row) if row is not None else None

    def require_active(self, domain_id: str) -> DomainRecord:
        record = self.get(domain_id)
        if record is None or record.status != "active":
            raise AuthenticationError("trust domain is unavailable")
        return record


def _record(row: object) -> DomainRecord:
    return DomainRecord(
        domain_id=row["domain_id"],  # type: ignore[index]
        status=row["status"],  # type: ignore[index]
        policy_revision=row["policy_revision"],  # type: ignore[index]
        revocation_epoch=row["revocation_epoch"],  # type: ignore[index]
        created_at=row["created_at"],  # type: ignore[index]
    )
