"""Durable activation lifecycle for one exact enrolled harness endpoint."""

from __future__ import annotations

import re
import time
from collections.abc import Callable
from enum import StrEnum
from typing import Any

from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError, ValidationError
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import load_credential_binding_from_connection
from agentnet.identity.domains import validate_domain_id
from agentnet.storage.backend import StoreBackend


_HARNESS_KINDS = frozenset({"omp", "pi", "claude", "codex", "antigravity", "server"})
_PROFILE_KEY = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$")
_PROCESS_MEASUREMENT = re.compile(r"^[0-9a-f]{64}$")
_ADDRESS_PREFIX = "agentnet:"
_UNAVAILABLE = "endpoint lifecycle is unavailable"


class EndpointActivationState(StrEnum):
    READY_TO_CONNECT = "ready_to_connect"
    WAITING_FOR_APPROVAL = "waiting_for_approval"
    ENROLLED = "enrolled"
    ACCESS_READY = "access_ready"
    RESTART_REQUIRED = "restart_required"
    CONNECTED = "connected"
    BLOCKED = "blocked"


_ALLOWED: dict[EndpointActivationState, frozenset[EndpointActivationState]] = {
    EndpointActivationState.READY_TO_CONNECT: frozenset(
        {EndpointActivationState.WAITING_FOR_APPROVAL}
    ),
    EndpointActivationState.WAITING_FOR_APPROVAL: frozenset(
        {EndpointActivationState.ENROLLED, EndpointActivationState.BLOCKED}
    ),
    EndpointActivationState.ENROLLED: frozenset(
        {EndpointActivationState.ACCESS_READY, EndpointActivationState.BLOCKED}
    ),
    EndpointActivationState.ACCESS_READY: frozenset(
        {EndpointActivationState.RESTART_REQUIRED, EndpointActivationState.BLOCKED}
    ),
    EndpointActivationState.RESTART_REQUIRED: frozenset(
        {EndpointActivationState.CONNECTED, EndpointActivationState.BLOCKED}
    ),
    EndpointActivationState.CONNECTED: frozenset(
        {EndpointActivationState.RESTART_REQUIRED, EndpointActivationState.BLOCKED}
    ),
    EndpointActivationState.BLOCKED: frozenset(
        {EndpointActivationState.READY_TO_CONNECT}
    ),
}


class EndpointLifecycleStatus(BaseModel):
    """Content-free lifecycle snapshot for one exact durable endpoint."""

    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    endpoint_id: str = Field(min_length=1, max_length=512)
    endpoint_address: str = Field(min_length=1, max_length=512)
    domain_id: str = Field(min_length=1, max_length=128)
    principal_id: str = Field(min_length=1, max_length=512)
    harness_id: str = Field(min_length=1, max_length=512)
    current_credential_id: str = Field(min_length=1, max_length=512)
    harness_kind: str = Field(min_length=1, max_length=32)
    profile_key: str = Field(min_length=1, max_length=256)
    state: EndpointActivationState
    adapter_generation: int = Field(ge=1)
    mailbox_cursor: int = Field(ge=0)
    capability_root_digest: str | None = Field(default=None, min_length=64, max_length=64)
    process_measurement: str | None = Field(default=None, min_length=64, max_length=64)
    state_reason: str = Field(min_length=1, max_length=128)
    revision: int = Field(ge=1)
    created_at: int = Field(ge=0)
    updated_at: int = Field(ge=0)


class EndpointLifecycleService:
    """Coordinate endpoint presentation state without manufacturing authority."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        clock: Callable[[], int] | None = None,
    ) -> None:
        if not isinstance(store, StoreBackend):
            raise TypeError("endpoint lifecycle requires a StoreBackend")
        self.store = store
        self.clock = clock or (lambda: int(time.time()))

    def register_existing(
        self,
        *,
        actor: VerifiedActor,
        harness_kind: str,
        profile_key: str,
    ) -> EndpointLifecycleStatus:
        """Register the exact current enrolled harness without replacing siblings."""

        kind = _canonical_harness_kind(harness_kind)
        profile = _canonical_profile_key(profile_key)
        now = int(self.clock())
        try:
            with self.store.transaction() as connection:
                self._require_current_actor(connection, actor=actor, now=now)
                harness = connection.execute(
                    "SELECT kind FROM harnesses WHERE domain_id=? AND harness_id=?",
                    (actor.domain_id, actor.harness_id),
                ).fetchone()
                if harness is None or str(harness["kind"]) != kind:
                    raise AuthenticationError("endpoint authority is unavailable")

                current = connection.execute(
                    "SELECT * FROM endpoint_lifecycle WHERE domain_id=? AND harness_id=?",
                    (actor.domain_id, actor.harness_id),
                ).fetchone()
                if current is not None:
                    if not self._same_registered_binding(
                        current,
                        actor=actor,
                        harness_kind=kind,
                        profile_key=profile,
                    ):
                        raise ConflictError("endpoint binding already exists")
                    return _status_from_row(current)

                claimed = connection.execute(
                    """SELECT harness_id FROM endpoint_lifecycle
                         WHERE domain_id=? AND harness_kind=? AND profile_key=?""",
                    (actor.domain_id, kind, profile),
                ).fetchone()
                if claimed is not None:
                    raise ConflictError("endpoint binding already exists")

                connection.execute(
                    """INSERT INTO endpoint_lifecycle(
                           domain_id,harness_id,principal_id,current_credential_id,
                           harness_kind,profile_key,state,adapter_generation,mailbox_cursor,
                           capability_root_digest,process_measurement,state_reason,revision,
                           created_at,updated_at
                       ) VALUES(?,?,?,?,?,?,'access_ready',1,0,NULL,NULL,?,1,?,?)""",
                    (
                        actor.domain_id,
                        actor.harness_id,
                        actor.principal_id,
                        actor.credential_id,
                        kind,
                        profile,
                        "current_enrollment_verified",
                        now,
                        now,
                    ),
                )
                row = self._load_exact_row(
                    connection,
                    domain_id=actor.domain_id,
                    harness_id=actor.harness_id or "",
                )
                self.store.append_audit(
                    connection,
                    {
                        "action": "endpoint.lifecycle.registered",
                        "domain_id": actor.domain_id,
                        "principal_id": actor.principal_id,
                        "harness_id": actor.harness_id,
                        "credential_id": actor.credential_id,
                        "state": EndpointActivationState.ACCESS_READY.value,
                        "revision": 1,
                        "recorded_at": now,
                    },
                )
                return _status_from_row(row)
        except Exception as exc:
            if isinstance(exc, (AuthenticationError, AuthorizationError, ConflictError, ValidationError)):
                raise
            if _is_integrity_constraint_error(exc):
                raise ConflictError("endpoint binding already exists") from exc
            raise

    def status(self, *, endpoint_id: str) -> EndpointLifecycleStatus:
        """Read by globally unique harness ID or canonical exact endpoint address."""

        with self.store.transaction(immediate=False) as connection:
            row = self._resolve_row(connection, endpoint_id=endpoint_id)
            return _status_from_row(row)

    def reconcile(self, *, endpoint_id: str) -> EndpointLifecycleStatus:
        """Narrow stale authority to blocked while never granting new authority."""

        now = int(self.clock())
        with self.store.transaction() as connection:
            row = self._resolve_row(connection, endpoint_id=endpoint_id)
            if self._row_has_current_authority(connection, row=row, now=now):
                return _status_from_row(row)
            state = EndpointActivationState(str(row["state"]))
            if state is EndpointActivationState.BLOCKED:
                return _status_from_row(row)
            if EndpointActivationState.BLOCKED not in _ALLOWED[state]:
                raise ConflictError("endpoint lifecycle transition is unavailable")
            updated = connection.execute(
                """UPDATE endpoint_lifecycle
                      SET state='blocked',state_reason='current_authority_unavailable',
                          adapter_generation=adapter_generation+1,
                          revision=revision+1,updated_at=?
                    WHERE domain_id=? AND harness_id=? AND revision=?""",
                (
                    now,
                    row["domain_id"],
                    row["harness_id"],
                    int(row["revision"]),
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("endpoint lifecycle revision changed concurrently")
            narrowed = self._load_exact_row(
                connection,
                domain_id=str(row["domain_id"]),
                harness_id=str(row["harness_id"]),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "endpoint.lifecycle.blocked",
                    "domain_id": narrowed["domain_id"],
                    "harness_id": narrowed["harness_id"],
                    "state": EndpointActivationState.BLOCKED.value,
                    "revision": int(narrowed["revision"]),
                    "recorded_at": now,
                },
            )
            return _status_from_row(narrowed)

    def request_activation(
        self,
        *,
        actor: VerifiedActor,
        expected_revision: int,
    ) -> EndpointLifecycleStatus:
        """Fence the old adapter generation and require a user-controlled restart."""

        if isinstance(expected_revision, bool) or expected_revision < 1:
            raise ValidationError("endpoint lifecycle revision is invalid")
        now = int(self.clock())
        with self.store.transaction() as connection:
            self._require_current_actor(connection, actor=actor, now=now)
            row = self._load_actor_row(connection, actor=actor)
            self._require_row_actor_binding(row, actor=actor)
            state = EndpointActivationState(str(row["state"]))
            revision = int(row["revision"])

            if state is EndpointActivationState.BLOCKED:
                raise AuthenticationError("endpoint authority is unavailable")
            if state is EndpointActivationState.RESTART_REQUIRED:
                if expected_revision in {revision, revision - 1}:
                    return _status_from_row(row)
                raise ConflictError("endpoint lifecycle revision is stale")
            if revision != expected_revision:
                raise ConflictError("endpoint lifecycle revision is stale")
            if EndpointActivationState.RESTART_REQUIRED not in _ALLOWED[state]:
                raise ConflictError("endpoint activation is not available from its current state")

            updated = connection.execute(
                """UPDATE endpoint_lifecycle
                      SET state='restart_required',state_reason='explicit_user_restart_required',
                          revision=revision+1,updated_at=?
                    WHERE domain_id=? AND harness_id=? AND revision=?""",
                (now, actor.domain_id, actor.harness_id, expected_revision),
            )
            if updated.rowcount != 1:
                raise ConflictError("endpoint lifecycle revision changed concurrently")
            requested = self._load_actor_row(connection, actor=actor)
            self.store.append_audit(
                connection,
                {
                    "action": "endpoint.lifecycle.activation_requested",
                    "domain_id": actor.domain_id,
                    "principal_id": actor.principal_id,
                    "harness_id": actor.harness_id,
                    "credential_id": actor.credential_id,
                    "adapter_generation": int(requested["adapter_generation"]),
                    "revision": int(requested["revision"]),
                    "recorded_at": now,
                },
            )
            return _status_from_row(requested)

    def record_user_restart(
        self,
        *,
        actor: VerifiedActor,
        expected_generation: int,
        process_measurement: str,
    ) -> EndpointLifecycleStatus:
        """Record a newly measured process after the user explicitly restarts it."""

        if isinstance(expected_generation, bool) or expected_generation < 1:
            raise ValidationError("endpoint adapter generation is invalid")
        measurement = _canonical_process_measurement(process_measurement)
        now = int(self.clock())
        with self.store.transaction() as connection:
            self._require_current_actor(connection, actor=actor, now=now)
            row = self._load_actor_row(connection, actor=actor)
            self._require_row_actor_binding(row, actor=actor)
            state = EndpointActivationState(str(row["state"]))
            generation = int(row["adapter_generation"])

            if state is EndpointActivationState.BLOCKED:
                raise AuthenticationError("endpoint authority is unavailable")
            if (
                state is EndpointActivationState.CONNECTED
                and generation == expected_generation + 1
                and row["process_measurement"] == measurement
            ):
                return _status_from_row(row)
            if state is not EndpointActivationState.RESTART_REQUIRED:
                raise ConflictError("explicit user restart is required before endpoint connection")
            if generation != expected_generation:
                raise ConflictError("endpoint adapter generation is stale")
            if row["process_measurement"] == measurement:
                raise ConflictError("explicit user restart requires a newly measured process")

            revision = int(row["revision"])
            updated = connection.execute(
                """UPDATE endpoint_lifecycle
                      SET state='connected',state_reason='explicit_user_restart_recorded',
                          adapter_generation=adapter_generation+1,
                          capability_root_digest=NULL,
                          process_measurement=?,revision=revision+1,updated_at=?
                    WHERE domain_id=? AND harness_id=? AND revision=?
                      AND adapter_generation=? AND state='restart_required'""",
                (
                    measurement,
                    now,
                    actor.domain_id,
                    actor.harness_id,
                    revision,
                    expected_generation,
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("endpoint lifecycle revision changed concurrently")
            connected = self._load_actor_row(connection, actor=actor)
            self.store.append_audit(
                connection,
                {
                    "action": "endpoint.lifecycle.user_restart_recorded",
                    "domain_id": actor.domain_id,
                    "principal_id": actor.principal_id,
                    "harness_id": actor.harness_id,
                    "credential_id": actor.credential_id,
                    "adapter_generation": int(connected["adapter_generation"]),
                    "process_measurement": measurement,
                    "revision": int(connected["revision"]),
                    "recorded_at": now,
                },
            )
            return _status_from_row(connected)

    def record_process_reconnect(
        self,
        *,
        actor: VerifiedActor,
        expected_generation: int,
        process_measurement: str,
    ) -> EndpointLifecycleStatus:
        """Rebind a connected endpoint to the exact process instance now present.

        An ordinary agent restart presents a new process instance without an
        adapter change.  The exact measurement is recorded under the verified
        harness actor and audited; it never rotates the generation, mints new
        authority, or accepts an executable-only measurement as proof.
        """

        if isinstance(expected_generation, bool) or expected_generation < 1:
            raise ValidationError("endpoint adapter generation is invalid")
        measurement = _canonical_process_measurement(process_measurement)
        now = int(self.clock())
        with self.store.transaction() as connection:
            self._require_current_actor(connection, actor=actor, now=now)
            row = self._load_actor_row(connection, actor=actor)
            self._require_row_actor_binding(row, actor=actor)
            state = EndpointActivationState(str(row["state"]))
            generation = int(row["adapter_generation"])

            if state is EndpointActivationState.BLOCKED:
                raise AuthenticationError("endpoint authority is unavailable")
            if state is not EndpointActivationState.CONNECTED:
                raise ConflictError("endpoint process reconnect requires a connected endpoint")
            if generation != expected_generation:
                raise ConflictError("endpoint adapter generation is stale")
            if row["process_measurement"] == measurement:
                return _status_from_row(row)

            revision = int(row["revision"])
            updated = connection.execute(
                """UPDATE endpoint_lifecycle
                      SET state_reason='process_instance_reconnected',
                          process_measurement=?,revision=revision+1,updated_at=?
                    WHERE domain_id=? AND harness_id=? AND revision=?
                      AND adapter_generation=? AND state='connected'""",
                (
                    measurement,
                    now,
                    actor.domain_id,
                    actor.harness_id,
                    revision,
                    expected_generation,
                ),
            )
            if updated.rowcount != 1:
                raise ConflictError("endpoint lifecycle revision changed concurrently")
            reconnected = self._load_actor_row(connection, actor=actor)
            self.store.append_audit(
                connection,
                {
                    "action": "endpoint.lifecycle.process_reconnected",
                    "domain_id": actor.domain_id,
                    "principal_id": actor.principal_id,
                    "harness_id": actor.harness_id,
                    "credential_id": actor.credential_id,
                    "adapter_generation": int(reconnected["adapter_generation"]),
                    "process_measurement": measurement,
                    "revision": int(reconnected["revision"]),
                    "recorded_at": now,
                },
            )
            return _status_from_row(reconnected)

    @staticmethod
    def _same_registered_binding(
        row: Any,
        *,
        actor: VerifiedActor,
        harness_kind: str,
        profile_key: str,
    ) -> bool:
        return (
            row["domain_id"] == actor.domain_id
            and row["principal_id"] == actor.principal_id
            and row["harness_id"] == actor.harness_id
            and row["current_credential_id"] == actor.credential_id
            and row["harness_kind"] == harness_kind
            and row["profile_key"] == profile_key
        )

    @staticmethod
    def _require_current_actor(connection: Any, *, actor: VerifiedActor, now: int) -> None:
        if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
            raise AuthenticationError("endpoint authority is unavailable")
        if actor.principal_id is None or actor.harness_id is None or actor.credential_id is None:
            raise AuthenticationError("endpoint authority is unavailable")
        try:
            binding = load_credential_binding_from_connection(connection, actor.credential_id)
            binding.require_active(now=now)
        except AuthenticationError as exc:
            raise AuthenticationError("endpoint authority is unavailable") from exc
        if (
            binding.domain_id != actor.domain_id
            or binding.principal_id != actor.principal_id
            or binding.guest_id is not None
            or binding.harness_id != actor.harness_id
            or binding.credential_id != actor.credential_id
            or binding.credential_epoch != actor.credential_epoch
            or binding.harness_credential_epoch != actor.credential_epoch
            or binding.binding_assurance != actor.binding_assurance
        ):
            raise AuthenticationError("endpoint authority is unavailable")

    @staticmethod
    def _require_row_actor_binding(row: Any, *, actor: VerifiedActor) -> None:
        if (
            row["domain_id"] != actor.domain_id
            or row["principal_id"] != actor.principal_id
            or row["harness_id"] != actor.harness_id
            or row["current_credential_id"] != actor.credential_id
        ):
            raise AuthenticationError("endpoint authority is unavailable")

    @staticmethod
    def _row_has_current_authority(connection: Any, *, row: Any, now: int) -> bool:
        try:
            binding = load_credential_binding_from_connection(
                connection, str(row["current_credential_id"])
            )
            binding.require_active(now=now)
        except AuthenticationError:
            return False
        if (
            binding.domain_id != row["domain_id"]
            or binding.principal_id != row["principal_id"]
            or binding.guest_id is not None
            or binding.harness_id != row["harness_id"]
            or binding.credential_id != row["current_credential_id"]
        ):
            return False
        harness = connection.execute(
            "SELECT kind FROM harnesses WHERE domain_id=? AND harness_id=?",
            (row["domain_id"], row["harness_id"]),
        ).fetchone()
        return harness is not None and harness["kind"] == row["harness_kind"]

    def _load_actor_row(self, connection: Any, *, actor: VerifiedActor) -> Any:
        if actor.harness_id is None:
            raise AuthenticationError("endpoint authority is unavailable")
        return self._load_exact_row(
            connection,
            domain_id=actor.domain_id,
            harness_id=actor.harness_id,
        )

    @staticmethod
    def _load_exact_row(connection: Any, *, domain_id: str, harness_id: str) -> Any:
        row = connection.execute(
            "SELECT * FROM endpoint_lifecycle WHERE domain_id=? AND harness_id=?",
            (domain_id, harness_id),
        ).fetchone()
        if row is None:
            raise AuthorizationError(_UNAVAILABLE)
        return row

    def _resolve_row(self, connection: Any, *, endpoint_id: str) -> Any:
        if not isinstance(endpoint_id, str) or not endpoint_id or len(endpoint_id) > 512:
            raise AuthorizationError(_UNAVAILABLE)
        if endpoint_id.startswith(_ADDRESS_PREFIX):
            parts = endpoint_id.split(":", 3)
            if len(parts) != 4:
                raise AuthorizationError(_UNAVAILABLE)
            _, domain_id, harness_kind, profile_key = parts
            try:
                validate_domain_id(domain_id)
                kind = _canonical_harness_kind(harness_kind)
                profile = _canonical_profile_key(profile_key)
            except (ValidationError, ValueError) as exc:
                raise AuthorizationError(_UNAVAILABLE) from exc
            row = connection.execute(
                """SELECT * FROM endpoint_lifecycle
                     WHERE domain_id=? AND harness_kind=? AND profile_key=?""",
                (domain_id, kind, profile),
            ).fetchone()
        else:
            row = connection.execute(
                "SELECT * FROM endpoint_lifecycle WHERE harness_id=?",
                (endpoint_id,),
            ).fetchone()
        if row is None:
            raise AuthorizationError(_UNAVAILABLE)
        return row


def _canonical_harness_kind(value: str) -> str:
    if not isinstance(value, str) or value not in _HARNESS_KINDS:
        raise ValidationError("endpoint harness kind is invalid")
    return value


def _canonical_profile_key(value: str) -> str:
    if not isinstance(value, str) or _PROFILE_KEY.fullmatch(value) is None:
        raise ValidationError("endpoint profile key is invalid")
    return value


def _canonical_process_measurement(value: str) -> str:
    if not isinstance(value, str) or _PROCESS_MEASUREMENT.fullmatch(value) is None:
        raise ConflictError("explicit user restart requires a newly measured process")
    return value


def _endpoint_address(*, domain_id: str, harness_kind: str, profile_key: str) -> str:
    return f"{_ADDRESS_PREFIX}{domain_id}:{harness_kind}:{profile_key}"


def _status_from_row(row: Any) -> EndpointLifecycleStatus:
    harness_id = str(row["harness_id"])
    domain_id = str(row["domain_id"])
    harness_kind = str(row["harness_kind"])
    profile_key = str(row["profile_key"])
    return EndpointLifecycleStatus(
        endpoint_id=harness_id,
        endpoint_address=_endpoint_address(
            domain_id=domain_id,
            harness_kind=harness_kind,
            profile_key=profile_key,
        ),
        domain_id=domain_id,
        principal_id=str(row["principal_id"]),
        harness_id=harness_id,
        current_credential_id=str(row["current_credential_id"]),
        harness_kind=harness_kind,
        profile_key=profile_key,
        state=EndpointActivationState(str(row["state"])),
        adapter_generation=int(row["adapter_generation"]),
        mailbox_cursor=int(row["mailbox_cursor"]),
        capability_root_digest=(
            str(row["capability_root_digest"])
            if row["capability_root_digest"] is not None
            else None
        ),
        process_measurement=(
            str(row["process_measurement"])
            if row["process_measurement"] is not None
            else None
        ),
        state_reason=str(row["state_reason"]),
        revision=int(row["revision"]),
        created_at=int(row["created_at"]),
        updated_at=int(row["updated_at"]),
    )


def _is_integrity_constraint_error(exc: Exception) -> bool:
    return exc.__class__.__name__ in {"IntegrityError", "UniqueViolation"}


__all__ = [
    "EndpointActivationState",
    "EndpointLifecycleService",
    "EndpointLifecycleStatus",
]
