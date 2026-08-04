"""Signed status contributions from ordinary enrolled server-agent harnesses."""

from __future__ import annotations

import hashlib
import secrets
import time
from collections.abc import Callable
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import AuthenticationError, ConflictError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.security.signatures import canonical_json
from agentnet.storage.backend import StoreBackend


class ServerStatusContribution(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True, populate_by_name=True)

    schema_id: Literal["agentnet.console.server-status.v1"] = Field(alias="schema")
    domain_id: str
    harness_id: str
    runtime_instance_id: str = Field(min_length=3, max_length=128)
    version: str = Field(min_length=1, max_length=64)
    capability_digest: str = Field(pattern=r"^[a-f0-9]{64}$")
    service_states: tuple[
        Literal["message_delivery", "offline_delivery", "enrollment", "approval", "audit"], ...
    ]
    blocker_codes: tuple[str, ...] = Field(default=(), max_length=32)
    emitted_at: int
    expires_at: int


class ServerStatusService:
    def __init__(
        self,
        *,
        store: StoreBackend,
        ttl_seconds: int = 120,
        clock: Callable[[], int] | None = None,
    ) -> None:
        self.store = store
        self.ttl_seconds = ttl_seconds
        self.clock = clock or (lambda: int(time.time()))

    @staticmethod
    def capability_digest(capabilities_json: str) -> str:
        return hashlib.sha256(capabilities_json.encode("utf-8")).hexdigest()

    def publish(self, *, actor: VerifiedActor, contribution: ServerStatusContribution) -> None:
        now = self.clock()
        if (
            actor.harness_id != contribution.harness_id
            or actor.domain_id != contribution.domain_id
            or actor.credential_id is None
        ):
            raise AuthenticationError("server status contribution denied")
        if (
            contribution.emitted_at > now + 30
            or contribution.emitted_at < now - self.ttl_seconds
            or contribution.expires_at <= contribution.emitted_at
            or contribution.expires_at > contribution.emitted_at + self.ttl_seconds
            or contribution.expires_at <= now
        ):
            raise ValidationError("server status contribution is outside its validity interval")
        raw = canonical_json(contribution.model_dump(mode="json", by_alias=True)).decode("utf-8")
        digest = hashlib.sha256(raw.encode("utf-8")).hexdigest()
        with self.store.transaction() as connection:
            binding = load_credential_binding_from_connection(connection, actor.credential_id)
            binding.require_active(now=now)
            if (
                binding.harness_id != actor.harness_id
                or binding.domain_id != actor.domain_id
                or binding.credential_epoch != actor.credential_epoch
            ):
                raise AuthenticationError("server status contribution denied")
            harness = connection.execute(
                "SELECT kind,capabilities_json FROM harnesses WHERE harness_id=? AND domain_id=?",
                (actor.harness_id, actor.domain_id),
            ).fetchone()
            if harness is None or harness["kind"] != "server-agent":
                raise AuthenticationError("server status contribution denied")
            expected_capabilities = self.capability_digest(str(harness["capabilities_json"]))
            if not secrets.compare_digest(expected_capabilities, contribution.capability_digest):
                raise ConflictError("server status capability configuration does not match enrollment")
            existing = connection.execute(
                "SELECT contribution_json,contribution_digest FROM console_server_status WHERE harness_id=?",
                (actor.harness_id,),
            ).fetchone()
            if existing is not None:
                previous = ServerStatusContribution.model_validate_json(existing["contribution_json"])
                if contribution.emitted_at < previous.emitted_at:
                    raise ConflictError("server status contribution is stale")
                if contribution.emitted_at == previous.emitted_at and not secrets.compare_digest(
                    str(existing["contribution_digest"]), digest
                ):
                    raise ConflictError("server status contribution conflicts at the same revision")
            connection.execute(
                """INSERT INTO console_server_status(
                    harness_id,domain_id,contribution_json,contribution_digest,revision,
                    received_at,expires_at
                ) VALUES(?,?,?,?,1,?,?)
                ON CONFLICT(harness_id) DO UPDATE SET
                    domain_id=excluded.domain_id,
                    contribution_json=excluded.contribution_json,
                    contribution_digest=excluded.contribution_digest,
                    revision=console_server_status.revision+1,
                    received_at=excluded.received_at,
                    expires_at=excluded.expires_at""",
                (
                    actor.harness_id,
                    actor.domain_id,
                    raw,
                    digest,
                    now,
                    contribution.expires_at,
                ),
            )


__all__ = ["ServerStatusContribution", "ServerStatusService"]
