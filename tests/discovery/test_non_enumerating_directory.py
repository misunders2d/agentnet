from __future__ import annotations

import pytest
import time
from uuid import uuid4

from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
    PolicyEngine,
)
from agentnet.discovery.directory import DirectoryRecord, DirectoryService
from agentnet.errors import AuthorizationError, ConflictError
from agentnet.security.signatures import P256KeyPair, canonical_json


def publish_authority(policy: PolicyEngine, actor, record: DirectoryRecord, *, add: bool = True) -> IssuanceAuthority:
    resource, context = DirectoryService.publication_binding(record)
    if add:
        policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action="directory.publish",
                resource_pattern=resource,
                revision=1,
            )
        )
    decision = policy.require(
        AuthorizationRequest(
            actor=actor,
            action="directory.publish",
            resource=resource,
            policy_revision=1,
            context=context,
        )
    )
    return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


def second_harness_for_same_principal(store, actor):
    suffix = uuid4().hex[:12]
    harness_id = f"harness-second-{suffix}"
    credential_id = f"credential-second-{suffix}"
    key = P256KeyPair.generate()
    now = int(time.time())
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO harnesses(
                harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,
                capabilities_json,credential_epoch,created_at
            ) VALUES(?,?,?,?,?,'active','lab',?,1,?)""",
            (
                harness_id,
                actor.domain_id,
                actor.principal_id,
                "pi",
                "same-human-second-harness",
                canonical_json({}).decode("utf-8"),
                now,
            ),
        )
        connection.execute(
            """INSERT INTO credentials(
                credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
            ) VALUES(?,?,?,?,'active',1,?,?)""",
            (credential_id, harness_id, key.thumbprint, key.public_pem, now - 1, now + 3_600),
        )
    return actor.model_copy(update={"harness_id": harness_id, "credential_id": credential_id})


def test_directory_agents_rooms_domains_endpoints_are_epoch_rotated_and_policy_visible(store, identity_factory) -> None:
    publisher, _ = identity_factory()
    viewer, _ = identity_factory()
    viewer_other_harness = second_harness_for_same_principal(store, viewer)
    hidden, _ = identity_factory()
    service = DirectoryService(store)
    policy = LocalConformancePolicyEngine(store)
    expires = int(time.time()) + 600
    records = (
        DirectoryRecord(
            record_id="agent:server-a",
            record_type="agent",
            domain_id=publisher.domain_id,
            epoch=1,
            attributes={"harness_id": publisher.harness_id, "capabilities": ["offline_custody"]},
            visible_to_principal_ids=tuple(sorted((publisher.principal_id, viewer.principal_id))),
            expires_at=expires,
        ),
        DirectoryRecord(
            record_id="room:planning",
            record_type="room",
            domain_id=publisher.domain_id,
            epoch=1,
            attributes={"classification": "C1"},
            visible_to_principal_ids=(viewer.principal_id,),
            expires_at=expires,
        ),
        DirectoryRecord(
            record_id="domain:corp",
            record_type="domain",
            domain_id=publisher.domain_id,
            epoch=1,
            attributes={"domain_id": publisher.domain_id},
            visible_to_principal_ids=(viewer.principal_id,),
            expires_at=expires,
        ),
        DirectoryRecord(
            record_id="endpoint:server-a",
            record_type="endpoint",
            domain_id=publisher.domain_id,
            epoch=1,
            attributes={"url": "https://server-a.example/a2a", "protocol": "a2a-1.0"},
            visible_to_principal_ids=(viewer.principal_id,),
            expires_at=expires,
        ),
    )
    for record in records:
        service.publish(record, authority=publish_authority(policy, publisher, record))
    assert {record.record_type for record in service.list_records(viewer)} == {"agent", "room", "domain", "endpoint"}
    assert {record.record_type for record in service.list_records(viewer_other_harness)} == {
        "agent",
        "room",
        "domain",
        "endpoint",
    }
    assert service.get_record(viewer, "endpoint:server-a").epoch == 1
    with pytest.raises(AuthorizationError):
        service.get_record(hidden, "endpoint:server-a")

    stale = records[-1].model_copy(update={"attributes": {"url": "https://server-b.example/a2a"}})
    with pytest.raises(ConflictError):
        service.publish(stale, authority=publish_authority(policy, publisher, stale, add=False))
    rotated = stale.model_copy(update={"epoch": 2})
    service.publish(rotated, authority=publish_authority(policy, publisher, rotated, add=False))
    assert service.get_record(viewer, "endpoint:server-a").attributes["url"] == "https://server-b.example/a2a"


def test_directory_rejects_harness_scoped_visibility_and_remote_plain_http(store, identity_factory) -> None:
    actor, _ = identity_factory()
    common = {
        "record_id": "endpoint:security-negative",
        "record_type": "endpoint",
        "domain_id": actor.domain_id,
        "epoch": 1,
        "expires_at": int(time.time()) + 600,
    }
    with pytest.raises(ValueError, match="visible_to_principal_ids"):
        DirectoryRecord.model_validate(
            common
            | {
                "attributes": {"url": "https://safe.example/a2a"},
                "visible_to_harness_ids": (actor.harness_id,),
            }
        )
    with pytest.raises(ValueError, match="require HTTPS"):
        DirectoryRecord(
            **common,
            attributes={"url": "http://remote.example/a2a"},
            visible_to_principal_ids=(actor.principal_id,),
        )
    loopback = DirectoryRecord(
        **common,
        attributes={"url": "http://127.0.0.1:8080/a2a"},
        visible_to_principal_ids=(actor.principal_id,),
    )
    assert loopback.attributes["url"].startswith("http://127.0.0.1")
