"""Executable N/N-1 rollout and unsupported-event quarantine controls."""

from __future__ import annotations

import json
import re
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from typing import Any, Protocol
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field

from agentnet.errors import ConflictError, GateBlocked, ValidationError
from agentnet.security.signatures import canonical_digest, canonical_json
from agentnet.storage.backend import StoreBackend
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS
from agentnet.storage.versioning_schema import require_versioning_schema


VERSION = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SAFE_IDENTIFIER = re.compile(r"^[a-z][a-z0-9_.-]{0,127}$")
SHA256 = re.compile(r"^[a-f0-9]{64}$")


class VersionTelemetry(Protocol):
    def increment(self, metric: str, *, outcome: str = "ok", amount: int = 1) -> None: ...

    def set_gauge(self, metric: str, value: int) -> None: ...


@dataclass(frozen=True, slots=True)
class VersionWindow:
    current: str
    previous: str

    def __post_init__(self) -> None:
        current = VERSION.fullmatch(self.current)
        previous = VERSION.fullmatch(self.previous)
        if current is None or previous is None:
            raise ValidationError("version window requires canonical major.minor versions")
        current_pair = tuple(int(value) for value in current.groups())
        previous_pair = tuple(int(value) for value in previous.groups())
        if current_pair[0] != previous_pair[0] or current_pair[1] != previous_pair[1] + 1:
            raise ValidationError("version window must contain exactly N and N-1")

    def allows(self, version: str) -> bool:
        return version in {self.current, self.previous}


class CompatibilityRequirement(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    event_type: str = Field(pattern=r"^[a-z][a-z0-9_.-]{0,127}$")
    protocol_version: str = Field(pattern=r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
    schema_profile: str = Field(pattern=r"^[a-z][a-z0-9_.-]{0,127}$")
    schema_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    required_features: frozenset[str] = frozenset()


@dataclass(frozen=True, slots=True)
class DigestIdempotentReplayHandler:
    """Explicit at-least-once replay contract keyed by immutable event digest.

    The callback must durably deduplicate the supplied digest before causing an
    external effect. The extension deliberately does not claim effect
    exactly-once: after a crash it calls the callback again with the same key.
    """

    callback: Callable[[dict[str, Any], str], None]
    idempotency_contract: str = field(default="event_digest", init=False)

    def __call__(self, event: dict[str, Any], event_digest: str) -> None:
        self.callback(event, event_digest)


class VersioningService:
    """Own rolling-version state without silently dropping unsupported bytes."""

    def __init__(
        self,
        store: StoreBackend,
        *,
        protocol_window: VersionWindow,
        host_domain_id: str | None = None,
        schema_profile: str,
        schema_hash: str,
        features: frozenset[str] = frozenset(),
        telemetry: VersionTelemetry | None = None,
        clock: Callable[[], int] = lambda: int(time.time()),
        peer_state_max_age_seconds: int = 3_600,
    ) -> None:
        if not SAFE_IDENTIFIER.fullmatch(schema_profile) or not SHA256.fullmatch(schema_hash):
            raise ValidationError("versioning schema profile/hash is invalid")
        if any(not SAFE_IDENTIFIER.fullmatch(feature) for feature in features):
            raise ValidationError("versioning feature identifier is invalid")
        if host_domain_id is not None and not SAFE_IDENTIFIER.fullmatch(host_domain_id):
            raise ValidationError("versioning host domain is invalid")
        if not 60 <= peer_state_max_age_seconds <= 86_400:
            raise ValidationError("versioning peer-state freshness bound is invalid")
        require_versioning_schema(store)
        self.store = store
        self.protocol_window = protocol_window
        self.host_domain_id = host_domain_id
        self.schema_profile = schema_profile
        self.schema_hash = schema_hash
        self.features = frozenset(features)
        self.telemetry = telemetry
        self.clock = clock
        self.peer_state_max_age_seconds = peer_state_max_age_seconds
        self._require_runtime_compatible_with_durable_state()

    def _latest_rollout(self) -> Any | None:
        if self.host_domain_id is None:
            return None
        return self.store.fetch_one(
            """SELECT * FROM version_rollouts WHERE host_domain_id=?
                 ORDER BY created_at DESC,rollout_id DESC LIMIT 1""",
            (self.host_domain_id,),
        )

    @staticmethod
    def _previous_version(version: str) -> str:
        matched = VERSION.fullmatch(version)
        if matched is None:
            raise GateBlocked("version_rollout", "durable rollout contains a malformed version")
        major, minor = (int(value) for value in matched.groups())
        if minor == 0:
            raise GateBlocked("version_rollout", "rolled-back version has no representable N-1 window")
        return f"{major}.{minor - 1}"

    def _effective_protocol_state(self) -> tuple[VersionWindow, str]:
        row = self._latest_rollout()
        if row is None:
            return self.protocol_window, "bootstrap"
        phase = str(row["phase"])
        if phase in {"expanded", "migrated_backfilled", "verified"}:
            self._current_rollout(str(row["rollout_id"]))
        from_version = str(row["from_protocol_version"])
        to_version = str(row["to_protocol_version"])
        if phase == "rolled_back":
            previous_version = row["deprecated_protocol_version"]
            if previous_version is None:
                raise GateBlocked(
                    "version_rollout",
                    "rolled-back rollout lacks its durable pre-rollout window",
                )
            return VersionWindow(
                current=from_version,
                previous=str(previous_version),
            ), phase
        return VersionWindow(current=to_version, previous=from_version), phase

    def _require_runtime_compatible_with_durable_state(self) -> None:
        row = self._latest_rollout()
        if row is None:
            return
        phase = str(row["phase"])
        configured = self.protocol_window.current
        from_version = str(row["from_protocol_version"])
        to_version = str(row["to_protocol_version"])
        allowed = (
            {from_version, to_version}
            if phase in {"expanded", "migrated_backfilled", "verified"}
            else {to_version}
            if phase == "contracted"
            else {from_version}
        )
        if configured not in allowed:
            raise GateBlocked(
                "version_downgrade",
                "runtime protocol is incompatible with the durable rollout phase",
            )

    @property
    def effective_protocol_window(self) -> VersionWindow:
        self._require_runtime_compatible_with_durable_state()
        return self._effective_protocol_state()[0]

    def supports(self, requirement: CompatibilityRequirement) -> bool:
        return bool(
            self.effective_protocol_window.allows(requirement.protocol_version)
            and self.protocol_window.allows(requirement.protocol_version)
            and requirement.schema_profile == self.schema_profile
            and requirement.schema_hash == self.schema_hash
            and requirement.required_features <= self.features
        )

    def queue_if_unsupported(
        self,
        *,
        peer_namespace: str,
        event: Mapping[str, Any],
        requirement: CompatibilityRequirement,
        reason_code: str = "unsupported_profile",
    ) -> dict[str, Any]:
        if not SAFE_IDENTIFIER.fullmatch(peer_namespace) or not SAFE_IDENTIFIER.fullmatch(reason_code):
            raise ValidationError("unsupported-event routing identifier is invalid")
        event_value = dict(event)
        event_digest = canonical_digest(event_value)
        if self.supports(requirement):
            return {"state": "compatible", "event_digest": event_digest, "queued": False}
        quarantine_id = str(uuid4())
        now = self.clock()
        encrypted = self.store.cipher.encrypt_json(
            event_value,
            purpose=f"unsupported-event:{quarantine_id}",
        )
        with self.store.transaction() as connection:
            existing = connection.execute(
                """SELECT quarantine_id,state FROM unsupported_event_quarantine
                     WHERE peer_namespace=? AND event_digest=?""",
                (peer_namespace, event_digest),
            ).fetchone()
            if existing is not None:
                return {
                    "state": existing["state"],
                    "event_digest": event_digest,
                    "queued": existing["state"] == "queued",
                    "duplicate": True,
                }
            connection.execute(
                """INSERT INTO unsupported_event_quarantine(
                       quarantine_id,peer_namespace,event_type,required_protocol_version,
                       required_schema_profile,required_schema_hash,required_features_json,
                       event_digest,event_encrypted,state,reason_code,received_at,updated_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,'queued',?,?,?)""",
                (
                    quarantine_id,
                    peer_namespace,
                    requirement.event_type,
                    requirement.protocol_version,
                    requirement.schema_profile,
                    requirement.schema_hash,
                    canonical_json(sorted(requirement.required_features)).decode("utf-8"),
                    event_digest,
                    encrypted,
                    reason_code,
                    now,
                    now,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "versioning.unsupported_event_queued",
                    "event_digest": event_digest,
                    "peer_namespace": peer_namespace,
                    "reason_code": reason_code,
                },
            )
        if self.telemetry is not None:
            self.telemetry.increment("unsupported_event", outcome="queued")
            self.telemetry.set_gauge(
                "unsupported_event_depth",
                min(self.queued_count(), 1_000_000_000),
            )
        return {
            "state": "queued",
            "event_digest": event_digest,
            "queued": True,
            "duplicate": False,
        }

    @staticmethod
    def _requirement_from_row(row: Any) -> CompatibilityRequirement:
        try:
            features = json.loads(row["required_features_json"])
        except (TypeError, ValueError) as exc:
            raise ConflictError("queued compatibility requirement is malformed") from exc
        return CompatibilityRequirement(
            event_type=row["event_type"],
            protocol_version=row["required_protocol_version"],
            schema_profile=row["required_schema_profile"],
            schema_hash=row["required_schema_hash"],
            required_features=frozenset(features),
        )

    def replay_supported(
        self,
        peer_namespace: str,
        handler: DigestIdempotentReplayHandler,
        *,
        limit: int = 100,
    ) -> dict[str, int]:
        if (
            not SAFE_IDENTIFIER.fullmatch(peer_namespace)
            or not 1 <= limit <= 1_000
            or not isinstance(handler, DigestIdempotentReplayHandler)
            or handler.idempotency_contract != "event_digest"
        ):
            raise ValidationError("unsupported-event replay request is invalid")
        rows = self.store.fetch_all(
            """SELECT * FROM unsupported_event_quarantine
                 WHERE peer_namespace=? AND state='queued'
                 ORDER BY received_at,quarantine_id LIMIT ?""",
            (peer_namespace, limit),
        )
        rollout = self._latest_rollout()
        if (
            rollout is None
            or rollout["phase"] not in {"verified", "contracted"}
            or self.protocol_window.current != rollout["to_protocol_version"]
            or rollout["verification_digest"] != self.verification_digest(rollout["rollout_id"])
        ):
            raise GateBlocked(
                "version_rollout",
                "unsupported-event replay requires the verified target runtime and migration",
            )
        replayed = 0
        still_queued = 0
        for row in rows:
            requirement = self._requirement_from_row(row)
            if requirement.protocol_version != rollout["to_protocol_version"]:
                raise GateBlocked(
                    "version_rollout",
                    "queued event does not target the verified rollout protocol",
                )
            if not self.supports(requirement):
                still_queued += 1
                continue
            value = self.store.cipher.decrypt_json(
                row["event_encrypted"],
                purpose=f"unsupported-event:{row['quarantine_id']}",
            )
            if not isinstance(value, dict) or canonical_digest(value) != row["event_digest"]:
                raise ConflictError("queued unsupported event failed immutable digest verification")
            handler(value, row["event_digest"])
            now = self.clock()
            with self.store.transaction() as connection:
                updated = connection.execute(
                    """UPDATE unsupported_event_quarantine
                          SET state='replayed',updated_at=?,replayed_at=?
                        WHERE quarantine_id=? AND state='queued'""",
                    (now, now, row["quarantine_id"]),
                )
                if updated.rowcount != 1:
                    raise ConflictError("unsupported-event replay raced with another worker")
                self.store.append_audit(
                    connection,
                    {
                        "action": "versioning.unsupported_event_replayed",
                        "event_digest": row["event_digest"],
                        "peer_namespace": peer_namespace,
                    },
                )
            replayed += 1
            if self.telemetry is not None:
                self.telemetry.increment("unsupported_event", outcome="replayed")
        if self.telemetry is not None:
            self.telemetry.set_gauge(
                "unsupported_event_depth",
                min(self.queued_count(), 1_000_000_000),
            )
        return {"replayed": replayed, "still_queued": still_queued}

    def queued_count(self) -> int:
        row = self.store.fetch_one(
            "SELECT COUNT(*) AS count FROM unsupported_event_quarantine WHERE state='queued'"
        )
        return int(row["count"] if row else 0)

    def begin_rollout(
        self,
        *,
        host_domain_id: str,
        from_protocol_version: str,
        to_protocol_version: str,
        from_schema_version: int,
        to_schema_version: int,
        compatibility_deadline: int,
    ) -> dict[str, Any]:
        VersionWindow(current=to_protocol_version, previous=from_protocol_version)
        if (
            not SAFE_IDENTIFIER.fullmatch(host_domain_id)
            or from_schema_version < 1
            or to_schema_version != from_schema_version
            or compatibility_deadline <= self.clock()
        ):
            raise ValidationError(
                "version rollout does not describe a bounded protocol expansion "
                "on the exact installed first-release schema"
            )
        if (
            from_schema_version != CURRENT_SCHEMA_VERSION
            or to_schema_version != CURRENT_SCHEMA_VERSION
        ):
            raise GateBlocked(
                "version_rollout",
                "version rollout must name the exact installed schema",
            )
        self._require_installed_migration(to_schema_version)
        domain = self.store.fetch_one(
            "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=? AND status='active'",
            (host_domain_id,),
        )
        if domain is None:
            raise GateBlocked("version_rollout", "version rollout domain is unavailable")
        if self.host_domain_id is not None and host_domain_id != self.host_domain_id:
            raise GateBlocked("version_rollout", "version rollout crossed the configured host domain")
        effective, _phase = self._effective_protocol_state()
        if from_protocol_version != effective.current:
            raise GateBlocked(
                "version_rollout",
                "version rollout does not extend the durable active compatibility window",
            )
        rollout_id = str(uuid4())
        now = self.clock()
        with self.store.transaction() as connection:
            active = connection.execute(
                """SELECT rollout_id FROM version_rollouts WHERE host_domain_id=?
                     AND phase NOT IN ('contracted','rolled_back') LIMIT 1""",
                (host_domain_id,),
            ).fetchone()
            if active is not None:
                raise ConflictError("another version rollout is already active")
            reused = connection.execute(
                "SELECT rollout_id FROM version_rollouts WHERE to_schema_version=? LIMIT 1",
                (to_schema_version,),
            ).fetchone()
            if reused is not None:
                raise ConflictError("target migration already belongs to a durable rollout")
            sequence = connection.execute(
                """SELECT COALESCE(MAX(created_at),0) AS created_at,
                          COALESCE(MAX(updated_at),0) AS updated_at
                     FROM version_rollouts WHERE host_domain_id=?""",
                (host_domain_id,),
            ).fetchone()
            recorded_at = max(
                now,
                int(sequence["created_at"] if sequence else 0) + 1,
                int(sequence["updated_at"] if sequence else 0) + 1,
            )
            connection.execute(
                """INSERT INTO version_rollouts(
                       rollout_id,host_domain_id,from_protocol_version,to_protocol_version,
                       from_schema_version,to_schema_version,phase,compatibility_deadline,
                       policy_revision_at_start,revocation_epoch_at_start,
                       deprecated_protocol_version,created_at,updated_at
                   ) VALUES(?,?,?,?,?,?,'expanded',?,?,?,?,?,?)""",
                (
                    rollout_id,
                    host_domain_id,
                    from_protocol_version,
                    to_protocol_version,
                    from_schema_version,
                    to_schema_version,
                    compatibility_deadline,
                    int(domain["policy_revision"]),
                    int(domain["revocation_epoch"]),
                    effective.previous,
                    recorded_at,
                    recorded_at,
                ),
            )
            self.store.append_audit(
                connection,
                {
                    "action": "versioning.rollout_started",
                    "rollout_id": rollout_id,
                    "to_protocol_version": to_protocol_version,
                    "to_schema_version": to_schema_version,
                },
            )
        if self.telemetry is not None:
            self.telemetry.increment("version_rollout", outcome="ok")
        return {"rollout_id": rollout_id, "phase": "expanded"}

    @staticmethod
    def _catalog_migration(version: int) -> Any:
        migration = next((item for item in MIGRATIONS if item.version == version), None)
        if migration is None:
            raise GateBlocked("version_rollout", "target migration is absent from the runtime catalog")
        return migration

    def _require_installed_migration(self, version: int) -> Any:
        migration = self._catalog_migration(version)
        metadata = self.store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        if metadata is None or int(metadata["value"]) != CURRENT_SCHEMA_VERSION:
            raise GateBlocked("version_rollout", "installed schema metadata is not current")
        if self.store.backend_name == "postgresql":
            installed = self.store.fetch_one(
                "SELECT name,checksum FROM schema_migrations WHERE version=?",
                (version,),
            )
        elif self.store.backend_name == "sqlite":
            installed = self.store.fetch_one(
                "SELECT name,checksum FROM installed_migration_catalog WHERE version=?",
                (version,),
            )
        else:
            raise GateBlocked("version_rollout", "migration history backend is unsupported")
        if (
            installed is None
            or installed["name"] != migration.name
            or installed["checksum"] != migration.checksum
        ):
            raise GateBlocked("version_rollout", "installed target migration checksum is not exact")
        return migration

    def verification_digest(self, rollout_id: str) -> str:
        """Derive the only acceptable rollout proof from installed state."""

        row = self._current_rollout(rollout_id)
        migration = self._require_installed_migration(int(row["to_schema_version"]))
        mismatch_queries = (
            """SELECT COUNT(*) AS count
                 FROM server_agent_relay_outbox o JOIN events e ON e.event_id=o.event_id
                 LEFT JOIN operational_work_reservations w
                   ON w.work_kind='relay_outbound' AND w.source_id=o.packet_id
                WHERE w.source_id IS NULL OR w.domain_id<>e.domain_id OR w.state<>
                      CASE WHEN o.state IN ('staged','remote_accepted') THEN 'pending' ELSE 'terminal' END""",
            """SELECT COUNT(*) AS count
                 FROM server_agent_relay_inbox i JOIN harnesses h ON h.harness_id=i.target_recipient_id
                 LEFT JOIN operational_work_reservations w
                   ON w.work_kind='relay_inbound' AND w.source_id=i.packet_id
                WHERE w.source_id IS NULL OR w.domain_id<>h.domain_id OR w.state<>
                      CASE WHEN i.state='authorized_pending' THEN 'pending' ELSE 'terminal' END""",
            """SELECT COUNT(*) AS count
                 FROM effect_reservations x JOIN events e ON e.event_id=x.event_id
                 LEFT JOIN operational_work_reservations w
                   ON w.work_kind='protected_effect' AND w.source_id=x.effect_id
                WHERE w.source_id IS NULL OR w.domain_id<>e.domain_id OR w.state<>
                      CASE WHEN x.state IN ('effect_prepared','effect_executing','effect_unknown')
                           THEN 'pending' ELSE 'terminal' END""",
        )
        mismatches = tuple(
            int((self.store.fetch_one(query) or {"count": 0})["count"])
            for query in mismatch_queries
        )
        if any(mismatches):
            raise GateBlocked("version_rollout", "target migration backfill invariants are not satisfied")
        return canonical_digest(
            {
                "profile": "agentnet.version-rollout-verification.v1",
                "host_domain_id": row["host_domain_id"],
                "rollout_id": row["rollout_id"],
                "compatibility_deadline": int(row["compatibility_deadline"]),
                "policy_revision_at_start": int(row["policy_revision_at_start"]),
                "revocation_epoch_at_start": int(row["revocation_epoch_at_start"]),
                "created_at": int(row["created_at"]),
                "from_protocol_version": row["from_protocol_version"],
                "to_protocol_version": row["to_protocol_version"],
                "pre_rollout_previous_protocol_version": row["deprecated_protocol_version"],
                "from_schema_version": int(row["from_schema_version"]),
                "to_schema_version": int(row["to_schema_version"]),
                "migration_name": migration.name,
                "migration_checksum": migration.checksum,
                "schema_profile": self.schema_profile,
                "schema_hash": self.schema_hash,
                "backfill_mismatches": mismatches,
            }
        )

    def _current_rollout(self, rollout_id: str) -> Any:
        row = self.store.fetch_one("SELECT * FROM version_rollouts WHERE rollout_id=?", (rollout_id,))
        if row is None:
            raise ConflictError("version rollout is unavailable")
        domain = self.store.fetch_one(
            "SELECT policy_revision,revocation_epoch,status FROM domains WHERE domain_id=?",
            (row["host_domain_id"],),
        )
        if (
            domain is None
            or domain["status"] != "active"
            or int(domain["policy_revision"]) != int(row["policy_revision_at_start"])
            or int(domain["revocation_epoch"]) != int(row["revocation_epoch_at_start"])
        ):
            raise GateBlocked("version_rollout", "policy or revocation state drifted during rollout")
        return row

    def advance_rollout(
        self,
        rollout_id: str,
        *,
        expected_phase: str,
        target_phase: str,
        verification_digest: str | None = None,
    ) -> dict[str, Any]:
        transitions = {
            "expanded": "migrated_backfilled",
            "migrated_backfilled": "verified",
            "verified": "contracted",
        }
        if transitions.get(expected_phase) != target_phase:
            raise ValidationError("version rollout transition is not allowed")
        row = self._current_rollout(rollout_id)
        if row["phase"] != expected_phase:
            raise ConflictError("version rollout phase changed")
        if target_phase == "verified" and (
            verification_digest is None or not SHA256.fullmatch(verification_digest)
        ):
            raise ValidationError("verified rollout phase requires an exact verification digest")
        server_digest = self.verification_digest(rollout_id)
        if target_phase == "migrated_backfilled" and verification_digest is not None:
            raise ValidationError("migration phase proof is derived by the server")
        if target_phase in {"verified", "contracted"} and verification_digest != server_digest:
            raise GateBlocked("version_rollout", "caller rollout digest does not match installed verification")
        if target_phase == "contracted" and row["verification_digest"] != server_digest:
            raise GateBlocked("version_rollout", "verified rollout digest no longer matches installed state")
        if target_phase == "contracted":
            if self.protocol_window.current != row["to_protocol_version"]:
                raise GateBlocked(
                    "version_rollout",
                    "target runtime protocol is not active for contraction",
                )
            if self.clock() < int(row["compatibility_deadline"]):
                raise GateBlocked("version_rollout", "N-1 compatibility deadline has not elapsed")
            queued = self.store.fetch_one(
                """SELECT COUNT(*) AS count FROM unsupported_event_quarantine
                     WHERE state='queued' AND required_protocol_version=?""",
                (row["from_protocol_version"],),
            )
            if queued is not None and int(queued["count"]) > 0:
                raise GateBlocked("version_rollout", "N-1 queued events remain unreplayed")
            active_n_minus_one = self.store.fetch_one(
                """SELECT COUNT(*) AS count FROM profile_peer_state
                     WHERE host_domain_id=? AND protocol_version=? AND negotiated_at>?""",
                (
                    row["host_domain_id"],
                    row["from_protocol_version"],
                    self.clock() - self.peer_state_max_age_seconds,
                ),
            )
            if active_n_minus_one is not None and int(active_n_minus_one["count"]) > 0:
                raise GateBlocked(
                    "version_rollout",
                    "fresh N-1 peer compatibility state still requires the expansion window",
                )
        now = self.clock()
        with self.store.transaction() as connection:
            updated = connection.execute(
                """UPDATE version_rollouts SET phase=?,verification_digest=COALESCE(?,verification_digest),updated_at=?
                     WHERE rollout_id=? AND phase=?""",
                (target_phase, server_digest, now, rollout_id, expected_phase),
            )
            if updated.rowcount != 1:
                raise ConflictError("version rollout transition raced")
            self.store.append_audit(
                connection,
                {
                    "action": "versioning.rollout_advanced",
                    "rollout_id": rollout_id,
                    "phase": target_phase,
                    "verification_digest": server_digest,
                },
            )
        if self.telemetry is not None:
            self.telemetry.increment("version_rollout", outcome="ok")
        return {
            "rollout_id": rollout_id,
            "phase": target_phase,
            "verification_digest": server_digest,
        }

    def rollback_rollout(self, rollout_id: str, *, verification_digest: str) -> dict[str, Any]:
        if not SHA256.fullmatch(verification_digest):
            raise ValidationError("rollout rollback requires an exact verification digest")
        row = self._current_rollout(rollout_id)
        if row["phase"] in {"contracted", "rolled_back"}:
            raise GateBlocked("version_rollout", "contracted or terminal rollout cannot roll back")
        if verification_digest != self.verification_digest(rollout_id):
            raise GateBlocked("version_rollout", "rollback digest does not match installed verification")
        now = self.clock()
        with self.store.transaction() as connection:
            updated = connection.execute(
                """UPDATE version_rollouts SET phase='rolled_back',verification_digest=?,updated_at=?
                     WHERE rollout_id=? AND phase=?""",
                (verification_digest, now, rollout_id, row["phase"]),
            )
            if updated.rowcount != 1:
                raise ConflictError("version rollout rollback raced")
            self.store.append_audit(
                connection,
                {
                    "action": "versioning.rollout_rolled_back",
                    "rollout_id": rollout_id,
                    "verification_digest": verification_digest,
                },
            )
        if self.telemetry is not None:
            self.telemetry.increment("version_rollout", outcome="rejected")
        return {"rollout_id": rollout_id, "phase": "rolled_back"}

    def content_free_status(self) -> dict[str, int | str]:
        active = self.store.fetch_one(
            """SELECT COUNT(*) AS count FROM version_rollouts
                 WHERE phase NOT IN ('contracted','rolled_back')"""
        )
        window, phase = self._effective_protocol_state()
        return {
            "active_rollouts": int(active["count"] if active else 0),
            "queued_unsupported_events": self.queued_count(),
            "protocol_current": window.current,
            "protocol_previous": window.previous,
            "rollout_phase": phase,
        }


__all__ = [
    "CompatibilityRequirement",
    "DigestIdempotentReplayHandler",
    "VersionWindow",
    "VersioningService",
]
