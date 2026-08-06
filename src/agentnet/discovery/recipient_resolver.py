"""Bounded non-enumerating resolution of authorized exact recipients."""

from __future__ import annotations

from collections import defaultdict
from datetime import UTC, datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

from agentnet.authorization.communication_scope_service import CollaborationScopeService
from agentnet.discovery.directory import DirectoryRecord, DirectoryService
from agentnet.errors import ConflictError, ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import Classification
from agentnet.storage.backend import StoreBackend



MAX_DIRECTORY_CANDIDATES = 20
MAX_RESOLVED_ENDPOINTS = 20
_NON_ENUMERATING_FAILURE = "recipient could not be resolved"
_USABLE_OFFLINE_STATES = frozenset(
    {
        "ready_to_connect",
        "waiting_for_approval",
        "enrolled",
        "access_ready",
        "restart_required",
    }
)


class ResolvedEndpoint(BaseModel):
    """One immutable exact endpoint selected inside one current scope."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    harness_id: str = Field(min_length=1, max_length=256)
    display_name: str = Field(min_length=1, max_length=256)
    harness_kind: str = Field(min_length=1, max_length=64)
    availability: Literal["online", "offline", "unknown"]
    scope_id: str = Field(min_length=1, max_length=256)


def normalize_query(value: str) -> str:
    """Casefold and collapse whitespace within the public recipient profile."""

    if not isinstance(value, str) or len(value) > 4_096:
        raise ValidationError("recipient query is outside the supported profile")
    normalized = " ".join(value.casefold().split())
    if not 1 <= len(normalized) <= 256:
        raise ValidationError("recipient query is outside the supported profile")
    return normalized


def _normalized_persisted_alias(value: object) -> str | None:
    if not isinstance(value, str) or len(value) > 4_096:
        return None
    normalized = " ".join(value.casefold().split())
    return normalized if 1 <= len(normalized) <= 256 else None


def _approved_aliases(record: DirectoryRecord) -> tuple[str, ...]:
    aliases = record.attributes.get("approved_aliases", ())
    if not isinstance(aliases, (list, tuple)) or len(aliases) > 32:
        return ()
    normalized = {
        alias
        for value in aliases
        if (alias := _normalized_persisted_alias(value)) is not None
    }
    return tuple(sorted(normalized))


class AuthorizedRecipientResolver:
    """Resolve only directory-visible, scope-authorized, current endpoints."""

    def __init__(
        self,
        *,
        scopes: CollaborationScopeService,
        directory: DirectoryService,
        store: StoreBackend,
    ) -> None:
        self.scopes = scopes
        self.directory = directory
        self.store = store

    @staticmethod
    def _fail() -> None:
        raise ConflictError(_NON_ENUMERATING_FAILURE)

    def _current_endpoints(
        self,
        *,
        actor: VerifiedActor,
        harness_ids: tuple[str, ...],
        now: int,
    ) -> dict[str, dict[str, str]]:
        if not harness_ids:
            return {}
        placeholders = ",".join("?" for _ in harness_ids)
        with self.store.transaction(immediate=False) as connection:
            rows = connection.execute(
                f"""SELECT harness.harness_id,harness.display_name,harness.kind,
                           lifecycle.state
                      FROM harnesses AS harness
                      JOIN principals AS principal
                        ON principal.principal_id=harness.principal_id
                       AND principal.domain_id=harness.domain_id
                       AND principal.status='active'
                      JOIN endpoint_lifecycle AS lifecycle
                        ON lifecycle.domain_id=harness.domain_id
                       AND lifecycle.harness_id=harness.harness_id
                       AND lifecycle.principal_id=harness.principal_id
                       AND lifecycle.harness_kind=harness.kind
                      JOIN credentials AS credential
                        ON credential.credential_id=lifecycle.current_credential_id
                       AND credential.harness_id=harness.harness_id
                       AND credential.epoch=harness.credential_epoch
                       AND credential.status='active'
                       AND credential.not_before<=? AND credential.expires_at>?
                     WHERE harness.domain_id=? AND harness.status='active'
                       AND lifecycle.state<>'blocked'
                       AND harness.harness_id IN ({placeholders})
                     ORDER BY harness.harness_id""",
                (now, now, actor.domain_id, *harness_ids),
            ).fetchall()
        current: dict[str, dict[str, str]] = {}
        for row in rows:
            state = str(row["state"])
            if state == "connected":
                availability = "online"
            elif state in _USABLE_OFFLINE_STATES:
                availability = "offline"
            else:
                continue
            display_name = str(row["display_name"])
            harness_kind = str(row["kind"])
            if (
                not 1 <= len(display_name) <= 256
                or any(ord(character) < 0x20 or ord(character) == 0x7F for character in display_name)
                or not 1 <= len(harness_kind) <= 64
            ):
                continue
            current[str(row["harness_id"])] = {
                "display_name": display_name,
                "harness_kind": harness_kind,
                "availability": availability,
            }
        return current

    def resolve(
        self,
        *,
        actor: VerifiedActor,
        query: str,
    ) -> tuple[ResolvedEndpoint, ...]:
        normalized_query = normalize_query(query)
        when = datetime.now(UTC)
        now = int(when.timestamp())
        records = self.directory.list_recipient_records(
            actor,
            limit=MAX_DIRECTORY_CANDIDATES,
            now=now,
        )

        records_by_harness: dict[str, list[DirectoryRecord]] = defaultdict(list)
        for record in records:
            harness_id = record.attributes.get("harness_id")
            if isinstance(harness_id, str):
                records_by_harness[harness_id].append(record)
        candidate_harness_ids = tuple(sorted(records_by_harness))
        if not candidate_harness_ids:
            self._fail()

        visibility_rows = self.scopes.active_recipient_members(
            actor=actor,
            candidate_harness_ids=candidate_harness_ids,
            action="message.send",
            classification=Classification.C1_INTERNAL,
            when=when,
        )
        visible_pairs: set[tuple[str, str]] = set()
        for visibility in visibility_rows:
            harness_id = getattr(visibility, "harness_id", None)
            scope_id = getattr(visibility, "scope_id", None)
            if (
                isinstance(harness_id, str)
                and harness_id in records_by_harness
                and isinstance(scope_id, str)
                and 1 <= len(scope_id) <= 256
            ):
                visible_pairs.add((harness_id, scope_id))
        if not visible_pairs:
            self._fail()

        authorized_harness_ids = tuple(sorted({harness_id for harness_id, _scope_id in visible_pairs}))
        endpoints = self._current_endpoints(
            actor=actor,
            harness_ids=authorized_harness_ids,
            now=now,
        )

        matches: list[ResolvedEndpoint] = []
        for harness_id, scope_id in sorted(visible_pairs):
            endpoint = endpoints.get(harness_id)
            if endpoint is None:
                continue
            aliases = {
                _normalized_persisted_alias(endpoint["display_name"]),
                _normalized_persisted_alias(endpoint["harness_kind"]),
            }
            for record in records_by_harness[harness_id]:
                aliases.update(_approved_aliases(record))
            aliases.discard(None)
            if normalized_query not in aliases:
                continue
            matches.append(
                ResolvedEndpoint(
                    harness_id=harness_id,
                    display_name=endpoint["display_name"],
                    harness_kind=endpoint["harness_kind"],
                    availability=endpoint["availability"],
                    scope_id=scope_id,
                )
            )
            if len(matches) > MAX_RESOLVED_ENDPOINTS:
                self._fail()

        if len(matches) != 1:
            self._fail()
        return tuple(matches)


__all__ = [
    "AuthorizedRecipientResolver",
    "MAX_RESOLVED_ENDPOINTS",
    "ResolvedEndpoint",
    "normalize_query",
]
