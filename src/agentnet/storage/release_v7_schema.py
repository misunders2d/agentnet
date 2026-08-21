"""Frozen schema-v7 aggregate for the v0.1.45 communication release."""

from __future__ import annotations
import json
from typing import Any

from agentnet.errors import GateBlocked
from agentnet.security.signatures import canonical_digest

from agentnet.storage.artifact_transfer_schema import ARTIFACT_TRANSFER_SCHEMA
from agentnet.storage.collaboration_scope_schema import COLLABORATION_SCOPE_SCHEMA
from agentnet.storage.endpoint_lifecycle_schema import ENDPOINT_LIFECYCLE_SCHEMA
from agentnet.storage.invitation_link_schema import INVITATION_LINK_SCHEMA


RELEASE_V7_SCHEMA_VERSION = 7
RELEASE_V7_SCHEMA = (
    ENDPOINT_LIFECYCLE_SCHEMA
    + COLLABORATION_SCOPE_SCHEMA
    + ARTIFACT_TRANSFER_SCHEMA
    + INVITATION_LINK_SCHEMA
)

_LEGACY_COMMUNICATION_ACTIONS = frozenset(
    {
        "message.send",
        "mailbox.read",
        "mailbox.acknowledge",
        "conversation.create",
        "conversation.message.send",
        "conversation.task.request",
        "conversation.task.handoff",
        "conversation.task.cancel_request",
        "conversation.task.complete",
        "conversation.structured_request.send",
        "conversation.response_obligation.respond",
        "conversation.thread",
        "conversation.response_obligation.create",
        "conversation.response_obligation.read",
        "conversation.response_obligation.transition",
        "conversation.response_obligation.cancel",
        "room.create",
        "room.action",
        "room.read",
    }
)
COMMUNICATION_COLLABORATION_ACTIONS = tuple(
    sorted(
        {
            "message.acknowledge",
            "message.read",
            "message.send",
            "obligation.create",
            "obligation.respond",
            "room.create",
            "room.member.add",
            "room.member.remove",
            "room.read",
            "room.send",
            "task.accept",
            "task.cancel",
            "task.handoff",
            "task.propose",
        }
    )
)
COMMUNICATION_COLLABORATION_RESOURCE_PREFIXES = (
    "conversation:",
    "obligation:",
    "room:",
    "task:",
    "thread:",
)
_COLLABORATION_SCOPE_CONTRACT = "agentnet.collaboration-scope.v1"


def _canonical_text(value: object) -> str:
    return json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    )

def _member_digest(
    *,
    scope_id: str,
    authority_id: str,
    harness_id: str,
    role: str,
    joined_at: int,
) -> str:
    return canonical_digest(
        {
            "scope_id": scope_id,
            "authority_kind": "principal",
            "authority_id": authority_id,
            "harness_id": harness_id,
            "role": role,
            "state": "active",
            "joined_sequence": 1,
            "joined_at": joined_at,
        }
    )


def _scope_digest(
    *,
    scope_id: str,
    domain_id: str,
    principal_id: str,
    owner_harness_id: str,
    members: list[dict[str, object]],
    policy_revision: int,
    domain_revocation_epoch: int,
    proposal_digest: str,
    created_at: int,
) -> str:
    return canonical_digest(
        {
            "schema_version": _COLLABORATION_SCOPE_CONTRACT,
            "scope_id": scope_id,
            "scope_kind": "direct",
            "domain_id": domain_id,
            "owner_principal_id": principal_id,
            "owner_harness_id": owner_harness_id,
            "members": members,
            "allowed_actions": list(COMMUNICATION_COLLABORATION_ACTIONS),
            "allowed_resource_prefixes": list(COMMUNICATION_COLLABORATION_RESOURCE_PREFIXES),
            "allowed_classifications": ["C1"],
            "canonical_references": [f"communication-scope:{scope_id}"],
            "policy_revision": policy_revision,
            "domain_revocation_epoch": domain_revocation_epoch,
            "control_sequence": 1,
            "membership_sequence": 1,
            "proposal_digest": proposal_digest,
            "revision": 1,
            "state": "active",
            "state_reason": "migrated_v6_communication_scope",
            "created_at": created_at,
            "updated_at": created_at,
            "expires_at": None,
            "revoked_at": None,
        }
    )


def migrate_v6_communication_scopes(
    connection: Any,
    *,
    postgres: bool = False,
) -> int:
    """Map each exact current v6 communication scope into schema-v7 authority."""

    def execute(query: str, parameters: tuple[object, ...] = ()) -> Any:
        if postgres:
            query = query.replace("?", "%s")
        return connection.execute(query, parameters)

    rows = execute(
        "SELECT * FROM communication_scopes WHERE state='committed' "
        "ORDER BY domain_id,principal_id,scope_id"
    ).fetchall()
    seen_principals: set[tuple[str, str]] = set()
    migrated = 0
    for row in rows:
        scope_id = str(row["scope_id"])
        domain_id = str(row["domain_id"])
        principal_id = str(row["principal_id"])
        owner_harness_id = str(row["owner_harness_id"])
        fresh_harness_id = str(row["fresh_harness_id"])
        principal_key = (domain_id, principal_id)
        if principal_key in seen_principals:
            raise GateBlocked(
                "schema_v7_scope_migration",
                "multiple committed v6 communication scopes are ambiguous",
            )
        seen_principals.add(principal_key)
        harness_rows = execute(
            "SELECT harness_id,domain_id,principal_id FROM harnesses "
            "WHERE harness_id IN (?,?) ORDER BY harness_id",
            (owner_harness_id, fresh_harness_id),
        ).fetchall()
        if (
            len(harness_rows) != 2
            or owner_harness_id == fresh_harness_id
            or any(
                str(harness["domain_id"]) != domain_id
                or str(harness["principal_id"]) != principal_id
                for harness in harness_rows
            )
        ):
            raise GateBlocked(
                "schema_v7_scope_migration",
                "v6 communication scope exact harness ownership is ambiguous",
            )
        item_rows = execute(
            """SELECT i.harness_id,i.action,i.resource_pattern,i.expires_at,
                      e.action AS entitlement_action,
                      e.resource_pattern AS entitlement_resource_pattern,
                      e.expires_at AS entitlement_expires_at,e.revoked_at,
                      e.revision AS entitlement_revision
                 FROM communication_scope_items AS i
                 JOIN entitlements AS e ON e.entitlement_id=i.entitlement_id
                WHERE i.scope_id=? ORDER BY i.harness_id,i.action""",
            (scope_id,),
        ).fetchall()
        expected_pairs = {
            (harness_id, action)
            for harness_id in (owner_harness_id, fresh_harness_id)
            for action in _LEGACY_COMMUNICATION_ACTIONS
        }
        actual_pairs = {
            (str(item["harness_id"]), str(item["action"])) for item in item_rows
        }
        if (
            len(item_rows) != len(expected_pairs)
            or actual_pairs != expected_pairs
            or any(
                item["resource_pattern"] != "*"
                or item["entitlement_resource_pattern"] != "*"
                or item["action"] != item["entitlement_action"]
                or item["expires_at"] is not None
                or item["entitlement_expires_at"] is not None
                or item["revoked_at"] is not None
                or int(item["entitlement_revision"]) != int(row["policy_revision"])
                for item in item_rows
            )
        ):
            raise GateBlocked(
                "schema_v7_scope_migration",
                "v6 communication scope authority items are incomplete or not current",
            )
        committed_at = int(row["committed_at"])
        audit_record_hash = str(row["audit_record_hash"])
        if len(audit_record_hash) != 64:
            raise GateBlocked(
                "schema_v7_scope_migration",
                "v6 communication scope audit lineage is unavailable",
            )
        proposal_digest = canonical_digest(
            {
                "migration": "v6-communication-scope-to-v7-collaboration-scope",
                "source_communication_scope_id": scope_id,
                "source_scope_digest": str(row["scope_digest"]),
                "source_transaction_digest": str(row["transaction_digest"]),
            }
        )
        member_values = sorted(
            (
                (
                    owner_harness_id,
                    "owner",
                    {
                        "authority_kind": "principal",
                        "authority_id": principal_id,
                        "harness_id": owner_harness_id,
                        "role": "owner",
                        "state": "active",
                        "joined_sequence": 1,
                        "joined_at": committed_at,
                    },
                ),
                (
                    fresh_harness_id,
                    "member",
                    {
                        "authority_kind": "principal",
                        "authority_id": principal_id,
                        "harness_id": fresh_harness_id,
                        "role": "member",
                        "state": "active",
                        "joined_sequence": 1,
                        "joined_at": committed_at,
                    },
                ),
            ),
            key=lambda value: value[0],
        )
        members = [value[2] for value in member_values]
        scope_digest = _scope_digest(
            scope_id=scope_id,
            domain_id=domain_id,
            principal_id=principal_id,
            owner_harness_id=owner_harness_id,
            members=members,
            policy_revision=int(row["policy_revision"]),
            domain_revocation_epoch=int(row["domain_revocation_epoch"]),
            proposal_digest=proposal_digest,
            created_at=committed_at,
        )
        execute(
            """INSERT INTO collaboration_scopes(
                scope_id,schema_version,domain_id,scope_kind,owner_principal_id,
                owner_harness_id,source_communication_scope_id,state,state_reason,
                allowed_actions_json,allowed_resource_prefixes_json,
                allowed_classifications_json,canonical_references_json,policy_floor,
                policy_revision,domain_revocation_epoch,control_sequence,
                membership_sequence,proposal_digest,scope_digest,audit_record_hash,
                revision,created_at,updated_at,expires_at,revoked_at,archived_at,deleted_at
            ) VALUES(?,1,?,?,?,?,?,'active','migrated_v6_communication_scope',
                ?,?,?,?, ?,?,?,1,1,?,?,?,1,?,?,NULL,NULL,NULL,NULL)""",
            (
                scope_id,
                domain_id,
                "direct",
                principal_id,
                owner_harness_id,
                scope_id,
                _canonical_text(list(COMMUNICATION_COLLABORATION_ACTIONS)),
                _canonical_text(list(COMMUNICATION_COLLABORATION_RESOURCE_PREFIXES)),
                _canonical_text(["C1"]),
                _canonical_text([f"communication-scope:{scope_id}"]),
                int(row["policy_revision"]),
                int(row["policy_revision"]),
                int(row["domain_revocation_epoch"]),
                proposal_digest,
                scope_digest,
                audit_record_hash,
                committed_at,
                committed_at,
            ),
        )
        for harness_id, role, member in member_values:
            execute(
                """INSERT INTO collaboration_scope_members(
                    scope_id,authority_kind,authority_id,harness_id,role,state,
                    joined_sequence,removed_sequence,member_digest,joined_at,removed_at
                ) VALUES(?,'principal',?,?,?,'active',1,NULL,?,?,NULL)""",
                (
                    scope_id,
                    principal_id,
                    harness_id,
                    role,
                    _member_digest(
                        scope_id=scope_id,
                        authority_id=principal_id,
                        harness_id=harness_id,
                        role=role,
                        joined_at=int(member["joined_at"]),
                    ),
                    committed_at,
                ),
            )
        migrated += 1
    return migrated


__all__ = [
    "COMMUNICATION_COLLABORATION_ACTIONS",
    "COMMUNICATION_COLLABORATION_RESOURCE_PREFIXES",
    "RELEASE_V7_SCHEMA",
    "RELEASE_V7_SCHEMA_VERSION",
    "migrate_v6_communication_scopes",
]
