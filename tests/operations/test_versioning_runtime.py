from __future__ import annotations

import json
from copy import deepcopy
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from agentnet.errors import ConflictError, GateBlocked, ValidationError
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.operations.config import (
    A2AAgentCardConfig,
    A2AServiceConfig,
    A2ASigningIdentityConfig,
    A2AStandingGrantConfig,
    ExtensionConfig,
    FeatureFlags,
)
from agentnet.operations.config_migration import (
    CURRENT_CONFIG_SCHEMA,
    load_config_json,
    plan_config_rebinding,
    require_config_rebinding,
)
from agentnet.operations.telemetry import Telemetry
from agentnet.operations.versioning import (
    CompatibilityRequirement,
    DigestIdempotentReplayHandler,
    VersioningService,
    VersionWindow,
)
from agentnet.security.signatures import canonical_digest
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION


SCHEMA_HASH = "a" * 64


def _service(store, clock, *, current: str = "1.1", previous: str = "1.0"):
    return VersioningService(
        store,
        protocol_window=VersionWindow(current=current, previous=previous),
        host_domain_id="corp.example",
        schema_profile="agentnet.v1",
        schema_hash=SCHEMA_HASH,
        features=frozenset({"mailbox"}),
        telemetry=Telemetry(store),
        clock=lambda: clock[0],
    )


def _verified_upgrade(store, clock, old: VersioningService) -> VersioningService:
    with store.transaction() as connection:
        connection.execute(
            "INSERT OR IGNORE INTO domains(domain_id,status,created_at) VALUES('corp.example','active',?)",
            (clock[0],),
        )
    rollout = old.begin_rollout(
        host_domain_id="corp.example",
        from_protocol_version="1.1",
        to_protocol_version="1.2",
        from_schema_version=CURRENT_SCHEMA_VERSION,
        to_schema_version=CURRENT_SCHEMA_VERSION,
        compatibility_deadline=clock[0] + 100,
    )
    migrated = old.advance_rollout(
        rollout["rollout_id"],
        expected_phase="expanded",
        target_phase="migrated_backfilled",
    )
    old.advance_rollout(
        rollout["rollout_id"],
        expected_phase="migrated_backfilled",
        target_phase="verified",
        verification_digest=migrated["verification_digest"],
    )
    return _service(store, clock, current="1.2", previous="1.1")


def test_unsupported_event_is_encrypted_deduplicated_and_replayed_after_upgrade(store) -> None:
    clock = [1_000]
    old = _service(store, clock)
    requirement = CompatibilityRequirement(
        event_type="mailbox.future",
        protocol_version="1.2",
        schema_profile="agentnet.v1",
        schema_hash=SCHEMA_HASH,
        required_features=frozenset({"mailbox"}),
    )
    event = {"kind": "future", "payload": {"secret": "never-in-operator-status"}}

    queued = old.queue_if_unsupported(
        peer_namespace="peer.example",
        event=event,
        requirement=requirement,
    )
    duplicate = old.queue_if_unsupported(
        peer_namespace="peer.example",
        event=event,
        requirement=requirement,
    )

    assert queued == {
        "state": "queued",
        "event_digest": canonical_digest(event),
        "queued": True,
        "duplicate": False,
    }
    assert duplicate["duplicate"] is True
    row = store.fetch_one("SELECT * FROM unsupported_event_quarantine")
    assert "never-in-operator-status" not in row["event_encrypted"]
    no_op = DigestIdempotentReplayHandler(lambda *_: None)
    with pytest.raises(GateBlocked, match="verified target runtime"):
        old.replay_supported("peer.example", no_op)

    replayed: list[tuple[dict[str, object], str]] = []
    upgraded = _verified_upgrade(store, clock, old)
    assert upgraded.replay_supported(
        "peer.example",
        DigestIdempotentReplayHandler(
            lambda value, digest: replayed.append((value, digest))
        ),
    ) == {"replayed": 1, "still_queued": 0}
    assert replayed == [(event, canonical_digest(event))]
    assert upgraded.queued_count() == 0
    assert upgraded.replay_supported("peer.example", no_op) == {
        "replayed": 0,
        "still_queued": 0,
    }


def test_replay_is_explicitly_at_least_once_and_recovers_by_digest(store) -> None:
    clock = [1_500]
    old = _service(store, clock)
    requirement = CompatibilityRequirement(
        event_type="mailbox.future",
        protocol_version="1.2",
        schema_profile="agentnet.v1",
        schema_hash=SCHEMA_HASH,
        required_features=frozenset({"mailbox"}),
    )
    event = {"kind": "effect", "value": 7}
    old.queue_if_unsupported(
        peer_namespace="peer.example",
        event=event,
        requirement=requirement,
    )
    upgraded = _verified_upgrade(store, clock, old)
    with pytest.raises(ValidationError, match="replay request"):
        upgraded.replay_supported("peer.example", lambda *_: None)  # type: ignore[arg-type]

    durable_effect_receipts: dict[str, dict[str, object]] = {}
    first_attempt = [True]

    def idempotent_effect(value: dict[str, object], digest: str) -> None:
        durable_effect_receipts.setdefault(digest, value)
        if first_attempt[0]:
            first_attempt[0] = False
            raise RuntimeError("simulated crash after durable effect receipt")

    handler = DigestIdempotentReplayHandler(idempotent_effect)
    with pytest.raises(RuntimeError, match="simulated crash"):
        upgraded.replay_supported("peer.example", handler)
    assert upgraded.queued_count() == 1
    assert upgraded.replay_supported("peer.example", handler) == {
        "replayed": 1,
        "still_queued": 0,
    }
    assert durable_effect_receipts == {canonical_digest(event): event}


def test_rollout_enforces_expand_migrate_verify_contract_and_rollback(store) -> None:
    clock = [2_000]
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
            ("corp.example", clock[0]),
        )
    service = _service(store, clock)
    with pytest.raises(GateBlocked, match="does not extend"):
        service.begin_rollout(
            host_domain_id="corp.example",
            from_protocol_version="1.0",
            to_protocol_version="1.1",
            from_schema_version=CURRENT_SCHEMA_VERSION,
            to_schema_version=CURRENT_SCHEMA_VERSION,
            compatibility_deadline=2_100,
        )
    started = service.begin_rollout(
        host_domain_id="corp.example",
        from_protocol_version="1.1",
        to_protocol_version="1.2",
        from_schema_version=CURRENT_SCHEMA_VERSION,
        to_schema_version=CURRENT_SCHEMA_VERSION,
        compatibility_deadline=2_100,
    )
    rollout_id = started["rollout_id"]
    assert started["phase"] == "expanded"
    target_requirement = CompatibilityRequirement(
        event_type="mailbox.future",
        protocol_version="1.2",
        schema_profile="agentnet.v1",
        schema_hash=SCHEMA_HASH,
        required_features=frozenset({"mailbox"}),
    )
    assert service.supports(target_requirement) is False
    assert _service(store, clock, current="1.2", previous="1.1").supports(target_requirement) is True
    migrated = service.advance_rollout(
        rollout_id,
        expected_phase="expanded",
        target_phase="migrated_backfilled",
    )
    assert migrated["phase"] == "migrated_backfilled"
    verification_digest = migrated["verification_digest"]
    with store.transaction() as connection:
        connection.execute(
            "UPDATE version_rollouts SET compatibility_deadline=compatibility_deadline+1 WHERE rollout_id=?",
            (rollout_id,),
        )
    with pytest.raises(GateBlocked, match="does not match installed verification"):
        service.advance_rollout(
            rollout_id,
            expected_phase="migrated_backfilled",
            target_phase="verified",
            verification_digest=verification_digest,
        )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE version_rollouts SET compatibility_deadline=? WHERE rollout_id=?",
            (2_100, rollout_id),
        )
    with pytest.raises(GateBlocked, match="does not match installed verification"):
        service.advance_rollout(
            rollout_id,
            expected_phase="migrated_backfilled",
            target_phase="verified",
            verification_digest="b" * 64,
        )
    assert service.advance_rollout(
        rollout_id,
        expected_phase="migrated_backfilled",
        target_phase="verified",
        verification_digest=verification_digest,
    )["phase"] == "verified"
    with pytest.raises(GateBlocked, match="deadline"):
        _service(store, clock, current="1.2", previous="1.1").advance_rollout(
            rollout_id,
            expected_phase="verified",
            target_phase="contracted",
            verification_digest=verification_digest,
        )
    clock[0] = 2_101
    upgraded = _service(store, clock, current="1.2", previous="1.1")
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO profile_peer_state(
                   peer_namespace,host_domain_id,actor_encrypted,credential_id,credential_epoch,
                   domain_revocation_epoch,remote_status_epoch,protocol_version,schema_profile,
                   schema_hash,config_schema_version,storage_schema_version,adapter_id,
                   adapter_version,features_json,missing_optional_features_json,offer_digest,negotiated_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "fresh-peer.example", "corp.example", "encrypted", "credential", 1, 1, 1,
                "1.1", "agentnet.v1", SCHEMA_HASH, "1.0", 17, "adapter", "1.0", "[]", "[]",
                "c" * 64, clock[0],
            ),
        )
    with pytest.raises(GateBlocked, match="fresh N-1 peer"):
        upgraded.advance_rollout(
            rollout_id,
            expected_phase="verified",
            target_phase="contracted",
            verification_digest=verification_digest,
        )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE profile_peer_state SET negotiated_at=? WHERE peer_namespace='fresh-peer.example'",
            (clock[0] - 3_601,),
        )
    assert upgraded.advance_rollout(
        rollout_id,
        expected_phase="verified",
        target_phase="contracted",
        verification_digest=verification_digest,
    )["phase"] == "contracted"
    with pytest.raises(GateBlocked, match="cannot roll back"):
        upgraded.rollback_rollout(rollout_id, verification_digest=verification_digest)

    with pytest.raises(ConflictError, match="already belongs"):
        upgraded.begin_rollout(
            host_domain_id="corp.example",
            from_protocol_version="1.2",
            to_protocol_version="1.3",
            from_schema_version=CURRENT_SCHEMA_VERSION,
            to_schema_version=CURRENT_SCHEMA_VERSION,
            compatibility_deadline=2_200,
        )
    assert upgraded.content_free_status() == {
        "active_rollouts": 0,
        "queued_unsupported_events": 0,
        "protocol_current": "1.2",
        "protocol_previous": "1.1",
        "rollout_phase": "contracted",
    }


def test_rollout_fails_closed_on_policy_or_revocation_drift(store) -> None:
    clock = [3_000]
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
            ("corp.example", clock[0]),
        )
    service = _service(store, clock)
    rollout = service.begin_rollout(
        host_domain_id="corp.example",
        from_protocol_version="1.1",
        to_protocol_version="1.2",
        from_schema_version=CURRENT_SCHEMA_VERSION,
        to_schema_version=CURRENT_SCHEMA_VERSION,
        compatibility_deadline=3_100,
    )
    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
            ("corp.example",),
        )
    with pytest.raises(GateBlocked, match="drifted"):
        service.advance_rollout(
            rollout["rollout_id"],
            expected_phase="expanded",
            target_phase="migrated_backfilled",
        )


def test_lowest_minor_rollback_restores_persisted_pre_rollout_window(store) -> None:
    clock = [4_000]
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
            ("corp.example", clock[0]),
        )
    service = _service(store, clock, current="0.1", previous="0.0")
    rollout = service.begin_rollout(
        host_domain_id="corp.example",
        from_protocol_version="0.1",
        to_protocol_version="0.2",
        from_schema_version=CURRENT_SCHEMA_VERSION,
        to_schema_version=CURRENT_SCHEMA_VERSION,
        compatibility_deadline=4_100,
    )
    digest = service.verification_digest(rollout["rollout_id"])
    service.rollback_rollout(rollout["rollout_id"], verification_digest=digest)
    restarted = _service(store, clock, current="0.1", previous="0.0")
    assert restarted.content_free_status() | {} == {
        "active_rollouts": 0,
        "queued_unsupported_events": 0,
        "protocol_current": "0.1",
        "protocol_previous": "0.0",
        "rollout_phase": "rolled_back",
    }


def test_config_accepts_only_exact_first_release_schema_with_rebinding_ack() -> None:
    current = ExtensionConfig(domain_id="corp.example")
    assert CURRENT_CONFIG_SCHEMA == "1.0"
    assert load_config_json(json.dumps(current.redacted_export())) == current

    legacy = deepcopy(current.redacted_export())
    legacy["schema_version"] = "0.9"
    legacy["service_origin"] = legacy.pop("public_base_url")
    legacy["instance_id"] = legacy.pop("runtime_instance_id")
    with pytest.raises(GateBlocked, match="exact first-release"):
        load_config_json(json.dumps(legacy))

    secret_bearing = deepcopy(current.redacted_export())
    secret_bearing["access_token"] = "should-not-be-here"
    with pytest.raises(ValidationError, match="secret material"):
        load_config_json(json.dumps(secret_bearing))

    rebound = ExtensionConfig(domain_id="other.example")
    plan = plan_config_rebinding(current, rebound)
    assert plan.changed_fields == ("domain_id",)
    with pytest.raises(GateBlocked, match="rebinding acknowledgement"):
        require_config_rebinding(current, rebound, acknowledgement_digest=None)
    assert require_config_rebinding(
        current,
        rebound,
        acknowledgement_digest=plan.acknowledgement_digest,
    ) == plan


def test_strict_config_accepts_bounded_a2a_key_path_but_not_key_material() -> None:
    config = ExtensionConfig(
        domain_id="corp.example",
        features=FeatureFlags(public_a2a=True),
        server_agent_capabilities=frozenset(
            {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
                ServerAgentCapability.A2A_GATEWAY,
            }
        ),
        a2a=A2AServiceConfig(
            route_token="a" * 32,
            recipient_harness_id="harness-a2a",
            card=A2AAgentCardConfig(
                name="Agent",
                description="Ordinary extension endpoint",
                version="1",
                streaming=False,
            ),
            standing_grant=A2AStandingGrantConfig(
                grant_id="standing-a2a",
                allowed_actions=frozenset({"a2a.message.send"}),
                allowed_output_sinks=frozenset({"public-response"}),
                expires_at=datetime.now(UTC) + timedelta(hours=1),
            ),
            signing_identity=A2ASigningIdentityConfig(
                harness_id="harness-a2a",
                credential_id="credential-a2a",
                private_key_path=Path("secrets/a2a-signing.pem"),
            ),
        ),
    )
    document = deepcopy(config.redacted_export())
    assert load_config_json(json.dumps(document)) == config

    embedded = deepcopy(document)
    embedded["a2a"]["signing_identity"]["private_key_path"] = (
        "-----BEGIN PRIVATE KEY----- secret"
    )
    with pytest.raises(ValidationError, match="current-schema validation"):
        load_config_json(json.dumps(embedded))
