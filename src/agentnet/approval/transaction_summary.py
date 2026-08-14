"""Purpose-specific Approval transaction validation and sanitized browser summaries."""

from __future__ import annotations

import base64
import hashlib
import json
import re
from typing import Any

from agentnet.authorization.bootstrap_plan import (
    BOOTSTRAP_PLAN_APPROVAL_PURPOSE,
    BOOTSTRAP_PLAN_GUARD_STATE_AT_COMMIT,
    BOOTSTRAP_PLAN_PROFILE,
)
from agentnet.errors import AuthenticationError
from agentnet.authorization.communication_scope import (
    COMMUNICATION_SCOPE_ACTIONS,
    COMMUNICATION_SCOPE_APPROVAL_PURPOSE,
    build_communication_scope_transaction,
)
from agentnet.security.signatures import canonical_json


_REQUEST_MESSAGE = "AgentNet C0 pilot request: fixed harmless transport check."
_REPLY_MESSAGE = "AgentNet C0 pilot reply: fixed harmless transport check."
_MANAGED_SERVER_REAUTHORIZATION_PURPOSE = "identity.credential.recover.approve"
_LAPTOP_CREDENTIAL_REAUTHORIZATION_PURPOSE = "identity.credential.recover.approve"
_SAFE_CAPABILITY = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$")


def _denied() -> AuthenticationError:
    return AuthenticationError("approval request denied")


def _strict_object(raw: bytes) -> dict[str, Any]:
    def pairs(values: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in values:
            if key in result:
                raise ValueError("duplicate key")
            result[key] = value
        return result

    try:
        value = json.loads(raw, object_pairs_hook=pairs, parse_constant=lambda _value: (_ for _ in ()).throw(ValueError()))
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise _denied() from exc
    if not isinstance(value, dict) or canonical_json(value) != raw:
        raise _denied()
    return value


def _keys(value: Any, expected: set[str]) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != expected:
        raise _denied()
    return value


def _digest(value: dict[str, Any]) -> str:
    return hashlib.sha256(canonical_json(value)).hexdigest()


def _string(value: Any) -> bool:
    return isinstance(value, str) and 1 <= len(value) <= 2048


def _positive_int(value: Any) -> bool:
    return type(value) is int and value > 0


def _sha256(value: Any) -> bool:
    return (
        isinstance(value, str)
        and len(value) == 64
        and all(character in "0123456789abcdef" for character in value)
    )


def _derived_id(plan_digest: str, kind: str, ordinal: int | None = None) -> str:
    preimage: dict[str, Any] = {
        "schema": "agentnet.bootstrap-plan.derived-id.v1",
        "plan_digest": plan_digest,
        "kind": kind,
    }
    if ordinal is not None:
        preimage["ordinal"] = ordinal
    prefixes = {"plan": "bp1", "guard": "c0g1", "item": "bpi1", "entitlement": "ent1"}
    try:
        prefix = prefixes[kind]
    except KeyError as exc:
        raise _denied() from exc
    token = base64.urlsafe_b64encode(hashlib.sha256(canonical_json(preimage)).digest()).rstrip(b"=")
    return f"{prefix}_{token.decode('ascii')}"


def _payload_schema(role: str, message: str) -> dict[str, Any]:
    schema = f"agentnet.c0-pilot.{role}-payload.v1"
    return {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": schema,
        "type": "object",
        "additionalProperties": False,
        "required": ["schema", "profile", "role", "message"],
        "properties": {
            "schema": {"const": schema},
            "profile": {"const": BOOTSTRAP_PLAN_PROFILE},
            "role": {"const": role},
            "message": {"const": message},
        },
    }


def _payload(role: str, message: str) -> dict[str, Any]:
    return {
        "schema": f"agentnet.c0-pilot.{role}-payload.v1",
        "profile": BOOTSTRAP_PLAN_PROFILE,
        "role": role,
        "message": message,
    }


def _validate_enrollment(transaction: dict[str, Any], digest: str) -> dict[str, Any]:
    _keys(
        transaction,
        {
            "candidate_key",
            "challenge_id",
            "domain_id",
            "expires_at",
            "harness",
            "human",
            "issued_at",
            "nonce",
            "purpose",
            "schema",
        },
    )
    candidate_key = _keys(transaction["candidate_key"], {"algorithm", "thumbprint"})
    harness = _keys(
        transaction["harness"],
        {
            "binding_assurance",
            "display_name",
            "kind",
            "requested_capabilities",
            "requested_class",
        },
    )
    human = _keys(
        transaction["human"],
        {"oidc_issuer", "oidc_subject", "verified_email"},
    )
    requested_capabilities = harness["requested_capabilities"]
    issued_at = transaction["issued_at"]
    expires_at = transaction["expires_at"]
    required_strings = (
        transaction["challenge_id"],
        transaction["domain_id"],
        transaction["nonce"],
        candidate_key["thumbprint"],
        harness["display_name"],
        harness["kind"],
        human["oidc_issuer"],
        human["oidc_subject"],
        human["verified_email"],
    )
    if (
        transaction["schema"] != "agentnet.enrollment.challenge.v1"
        or transaction["purpose"] != "human_harness_credential_binding"
        or candidate_key["algorithm"] != "ES256/P-256"
        or harness["binding_assurance"] not in {"os_bound", "hardware_bound"}
        or harness["requested_class"] != "protected_business"
        or not isinstance(requested_capabilities, list)
        or len(requested_capabilities) > 64
        or any(
            not isinstance(capability, str)
            or len(capability) > 128
            or _SAFE_CAPABILITY.fullmatch(capability) is None
            for capability in requested_capabilities
        )
        or requested_capabilities != sorted(set(requested_capabilities))
        or any(not isinstance(value, str) or not value for value in required_strings)
        or type(issued_at) is not int
        or type(expires_at) is not int
        or not 30 <= expires_at - issued_at <= 600
    ):
        raise _denied()
    capabilities = (
        ", ".join(requested_capabilities) if requested_capabilities else "none"
    )
    return {
        "title": "Enroll a laptop identity",
        "statements": [
            f"Laptop or agent: {harness['display_name']} ({harness['kind']})",
            f"Verified person: {human['verified_email']}",
            f"Corporate domain: {transaction['domain_id']}",
            f"Requested capabilities (not granted by enrollment): {capabilities}",
            "Authority granted by enrollment: none",
        ],
        "advanced_digest": digest,
    }


def _validate_harness_revocation(transaction: dict[str, Any], digest: str) -> dict[str, Any]:
    _keys(
        transaction,
        {
            "type",
            "request_id",
            "domain_id",
            "harness_id",
            "expected_credential_epoch",
            "expected_domain_revocation_epoch",
            "reason",
        },
    )
    if (
        transaction["type"] != "harness_revocation"
        or any(
            not _string(transaction[field])
            for field in ("request_id", "domain_id", "harness_id", "reason")
        )
        or not _positive_int(transaction["expected_credential_epoch"])
        or not _positive_int(transaction["expected_domain_revocation_epoch"])
    ):
        raise _denied()
    return {
        "title": "Remove one laptop or agent",
        "statements": [
            f"Exact device: {transaction['harness_id']}",
            f"Network: {transaction['domain_id']}",
            f"Reason: {transaction['reason']}",
            "Other devices and the person remain active",
        ],
        "advanced_digest": digest,
    }


def _validate_managed_server_credential_reauthorization(
    transaction: dict[str, Any],
    digest: str,
) -> dict[str, Any]:
    schema = transaction.get("schema")
    v2 = schema == "agentnet.managed-server-credential-reauthorization.v2"
    keys = {
        "schema",
        "approval_purpose",
        "request_id",
        "domain_id",
        "principal_id",
        "harness_id",
        "expired_credential_id",
        "expected_credential_epoch",
        "expected_expired_at",
        "expected_key_id",
        "expected_binding_assurance",
        "managed_config_sha256",
        "managed_identity_sha256",
        "maximum_new_credential_ttl_seconds",
        "managed_profile",
        "key_binding",
        "old_credential_action",
        "authority_granted",
    }
    if v2:
        keys.update(
            {
                "c0_terminal_credential_epoch",
                "c0_terminal_sha256",
                "prior_supersession_journal_sha256",
            }
        )
    _keys(transaction, keys)
    if (
        schema
        not in {
            "agentnet.managed-server-credential-reauthorization.v1",
            "agentnet.managed-server-credential-reauthorization.v2",
        }
        or transaction["approval_purpose"] != _MANAGED_SERVER_REAUTHORIZATION_PURPOSE
        or not _string(transaction["request_id"])
        or not _string(transaction["domain_id"])
        or not _string(transaction["principal_id"])
        or not _string(transaction["harness_id"])
        or not _string(transaction["expired_credential_id"])
        or not _positive_int(transaction["expected_credential_epoch"])
        or not _positive_int(transaction["expected_expired_at"])
        or not _string(transaction["expected_key_id"])
        or transaction["expected_binding_assurance"] not in {"os_bound", "hardware_bound"}
        or not _sha256(transaction["managed_config_sha256"])
        or not _sha256(transaction["managed_identity_sha256"])
        or type(transaction["maximum_new_credential_ttl_seconds"]) is not int
        or not 3_600 <= transaction["maximum_new_credential_ttl_seconds"] <= 604_800
        or transaction["managed_profile"] != "always_on_server_agent"
        or transaction["key_binding"] != "same_managed_key_with_fresh_possession_proof"
        or transaction["old_credential_action"] != "retire_without_extension"
        or transaction["authority_granted"] is not False
        or (
            v2
            and (
                not _positive_int(transaction["c0_terminal_credential_epoch"])
                or transaction["c0_terminal_credential_epoch"]
                > transaction["expected_credential_epoch"]
                or not _sha256(transaction["c0_terminal_sha256"])
                or (
                    transaction["prior_supersession_journal_sha256"] is not None
                    and not _sha256(transaction["prior_supersession_journal_sha256"])
                )
                or (
                    transaction["c0_terminal_credential_epoch"]
                    == transaction["expected_credential_epoch"]
                )
                != (transaction["prior_supersession_journal_sha256"] is None)
            )
        )
    ):
        raise _denied()
    hours = transaction["maximum_new_credential_ttl_seconds"] // 3_600
    statements = [
        "Server identity: existing managed server agent",
        "Key custody: the existing managed key must prove fresh possession",
        "Old credential: remains expired and is retired, never extended",
        f"New credential lifetime: no more than {hours} hour{'s' if hours != 1 else ''}",
        "Scope: same corporate owner, domain, harness, key, config, and identity",
    ]
    if v2:
        statements.extend(
            [
                "C0 origin: exact immutable terminal evidence is bound",
                (
                    "Recovery history: exact prior supersession journal is bound"
                    if transaction["prior_supersession_journal_sha256"] is not None
                    else "Recovery history: first post-C0 supersession"
                ),
            ]
        )
    statements.append("Authority granted: none")
    return {
        "title": "Reauthorize an expired server-agent credential",
        "statements": statements,
        "advanced_digest": digest,
    }


def _validate_laptop_credential_reauthorization(
    transaction: dict[str, Any],
    digest: str,
) -> dict[str, Any]:
    _keys(
        transaction,
        {
            "schema",
            "approval_purpose",
            "request_id",
            "domain_id",
            "principal_id",
            "harness_id",
            "expired_credential_id",
            "expected_credential_epoch",
            "successor_credential_epoch",
            "expected_expired_at",
            "expected_key_id",
            "expected_public_key_sha256",
            "expected_binding_assurance",
            "identity_profile_sha256",
            "prepared_at",
            "expires_at",
            "maximum_new_credential_ttl_seconds",
            "key_binding",
            "old_credential_action",
            "key_preserved",
            "authority_granted",
        },
    )
    if (
        transaction["schema"]
        != "agentnet.laptop-credential-reauthorization.v1"
        or transaction["approval_purpose"]
        != _LAPTOP_CREDENTIAL_REAUTHORIZATION_PURPOSE
        or not _string(transaction["request_id"])
        or not _string(transaction["domain_id"])
        or not _string(transaction["principal_id"])
        or not _string(transaction["harness_id"])
        or not _string(transaction["expired_credential_id"])
        or not _positive_int(transaction["expected_credential_epoch"])
        or transaction["successor_credential_epoch"]
        != transaction["expected_credential_epoch"] + 1
        or not _positive_int(transaction["expected_expired_at"])
        or not _string(transaction["expected_key_id"])
        or not _sha256(transaction["expected_public_key_sha256"])
        or transaction["expected_binding_assurance"]
        not in {"os_bound", "hardware_bound"}
        or not _sha256(transaction["identity_profile_sha256"])
        or not _positive_int(transaction["prepared_at"])
        or not _positive_int(transaction["expires_at"])
        or transaction["expected_expired_at"] > transaction["prepared_at"]
        or transaction["expires_at"] <= transaction["prepared_at"]
        or transaction["expires_at"] - transaction["prepared_at"] > 600
        or type(transaction["maximum_new_credential_ttl_seconds"]) is not int
        or not 3_600
        <= transaction["maximum_new_credential_ttl_seconds"]
        <= 604_800
        or transaction["key_binding"]
        != "same_laptop_key_with_fresh_possession_proof"
        or transaction["old_credential_action"] != "retire_without_extension"
        or transaction["key_preserved"] is not True
        or transaction["authority_granted"] is not False
    ):
        raise _denied()
    hours = transaction["maximum_new_credential_ttl_seconds"] // 3_600
    return {
        "title": "Reauthorize an expired laptop credential",
        "statements": [
            "Laptop identity: the existing person and exact laptop remain unchanged",
            "Key custody: the existing P-256 key must prove fresh possession",
            "Old credential: remains expired and is retired, never extended",
            "New credential: exact next epoch on the same public key",
            f"New credential lifetime: no more than {hours} hour{'s' if hours != 1 else ''}",
            "Capabilities, memberships, communication scopes, and authority: unchanged",
            "Authority granted: none",
        ],
        "advanced_digest": digest,
    }


def _validate_bootstrap(transaction: dict[str, Any]) -> dict[str, Any]:
    _keys(
        transaction,
        {
            "schema",
            "approval_purpose",
            "plan_digest",
            "canonical_plan_preimage",
            "plan_id",
            "guard",
            "items",
            "item_count",
            "entitlement_count",
        },
    )
    if (
        transaction["schema"] != "agentnet.bootstrap-plan.transaction.v1"
        or transaction["approval_purpose"] != BOOTSTRAP_PLAN_APPROVAL_PURPOSE
        or not _sha256(transaction["plan_digest"])
        or type(transaction["item_count"]) is not int
        or transaction["item_count"] != 10
        or type(transaction["entitlement_count"]) is not int
        or transaction["entitlement_count"] != 10
    ):
        raise _denied()

    preimage = _keys(
        transaction["canonical_plan_preimage"],
        {
            "schema",
            "approval_purpose",
            "profile",
            "profile_version",
            "begin_idempotency_key_sha256",
            "domain",
            "principal",
            "harnesses",
            "enrollment_evidence",
            "issued_at",
            "approval_expires_at",
            "authority_expires_at",
            "communication_ttl_seconds",
            "max_uses",
            "independent_boundary_proven",
            "c0",
        },
    )
    if (
        preimage["schema"] != "agentnet.bootstrap-plan.preimage.v1"
        or preimage["approval_purpose"] != BOOTSTRAP_PLAN_APPROVAL_PURPOSE
        or preimage["profile"] != BOOTSTRAP_PLAN_PROFILE
        or type(preimage["profile_version"]) is not int
        or preimage["profile_version"] != 1
        or type(preimage["communication_ttl_seconds"]) is not int
        or preimage["communication_ttl_seconds"] != 3600
        or type(preimage["max_uses"]) is not int
        or preimage["max_uses"] != 1
        or preimage["independent_boundary_proven"] is not False
    ):
        raise _denied()
    issued_at = preimage["issued_at"]
    approval_expires_at = preimage["approval_expires_at"]
    authority_expires_at = preimage["authority_expires_at"]
    if (
        not _positive_int(issued_at)
        or not _positive_int(approval_expires_at)
        or not _positive_int(authority_expires_at)
        or not issued_at < approval_expires_at <= issued_at + 300
        or not approval_expires_at < authority_expires_at <= issued_at + 3600
    ):
        raise _denied()

    domain = _keys(preimage["domain"], {"domain_id", "policy_revision", "revocation_epoch"})
    principal = _keys(preimage["principal"], {"principal_id"})
    harnesses = _keys(preimage["harnesses"], {"owner", "fresh"})
    evidence = _keys(preimage["enrollment_evidence"], {"owner", "fresh"})
    harness_keys = {
        "harness_id",
        "credential_id",
        "credential_epoch",
        "binding_assurance",
        "display_name",
        "kind",
    }
    evidence_keys = {
        "schema",
        "role",
        "guided_oidc",
        "enrollment_challenge_id",
        "oidc_transaction_id",
        "enrollment_consumed_at",
        "oidc_consumed_at",
        "oidc_issuer",
        "oidc_subject_sha256",
        "verified_email_sha256",
        "candidate_key_thumbprint",
        "approval_purpose",
        "approval_receipt_id",
        "approval_receipt_digest",
        "approval_verifier_id",
        "approval_signer_key_id",
        "approval_authenticated_at",
        "approval_issued_at",
    }
    owner = _keys(harnesses["owner"], harness_keys)
    fresh = _keys(harnesses["fresh"], harness_keys)
    if (
        not _string(preimage["begin_idempotency_key_sha256"])
        or not _sha256(preimage["begin_idempotency_key_sha256"])
        or not _string(domain["domain_id"])
        or not _positive_int(domain["policy_revision"])
        or not _positive_int(domain["revocation_epoch"])
        or not _string(principal["principal_id"])
        or owner["harness_id"] == fresh["harness_id"]
        or owner["credential_id"] == fresh["credential_id"]
    ):
        raise _denied()
    for harness in (owner, fresh):
        if (
            not _string(harness["harness_id"])
            or not _string(harness["credential_id"])
            or not _positive_int(harness["credential_epoch"])
            or harness["binding_assurance"] not in {"os_bound", "hardware_bound"}
            or not _string(harness["display_name"])
            or not _string(harness["kind"])
        ):
            raise _denied()
    for role in ("owner", "fresh"):
        item = _keys(evidence[role], evidence_keys)
        string_fields = (
            "enrollment_challenge_id",
            "oidc_transaction_id",
            "oidc_issuer",
            "candidate_key_thumbprint",
            "approval_receipt_id",
            "approval_verifier_id",
            "approval_signer_key_id",
        )
        timestamp_fields = (
            "enrollment_consumed_at",
            "oidc_consumed_at",
            "approval_authenticated_at",
            "approval_issued_at",
        )
        if (
            item["schema"] != "agentnet.bootstrap-plan.enrollment-evidence.v1"
            or item["role"] != role
            or item["guided_oidc"] is not True
            or item["approval_purpose"] != "identity.enrollment.approve"
            or any(not _string(item[field]) for field in string_fields)
            or not _sha256(item["oidc_subject_sha256"])
            or not _sha256(item["verified_email_sha256"])
            or not _sha256(item["approval_receipt_digest"])
            or any(not _positive_int(item[field]) for field in timestamp_fields)
            or item["approval_authenticated_at"] > item["approval_issued_at"]
            or item["approval_issued_at"] > item["enrollment_consumed_at"]
            or item["enrollment_consumed_at"] > issued_at
            or item["oidc_consumed_at"] > issued_at
        ):
            raise _denied()

    plan_digest = _digest(preimage)
    if transaction["plan_digest"] != plan_digest or transaction["plan_id"] != _derived_id(plan_digest, "plan"):
        raise _denied()
    guard_id = _derived_id(plan_digest, "guard")
    guard = _keys(
        transaction["guard"],
        {
            "schema",
            "guard_id",
            "classification",
            "owner_harness_id",
            "fresh_harness_id",
            "request_remaining_uses",
            "reply_remaining_uses",
            "request_payload_schema",
            "request_payload_schema_digest",
            "request_payload",
            "request_payload_digest",
            "reply_payload_schema",
            "reply_payload_schema_digest",
            "reply_payload",
            "reply_payload_digest",
            "state_at_commit",
        },
    )
    request_schema = _payload_schema("request", _REQUEST_MESSAGE)
    reply_schema = _payload_schema("reply", _REPLY_MESSAGE)
    request_payload = _payload("request", _REQUEST_MESSAGE)
    reply_payload = _payload("reply", _REPLY_MESSAGE)
    if (
        guard["schema"] != "agentnet.c0-plan-guard.v1"
        or guard["guard_id"] != guard_id
        or guard["classification"] != "C0"
        or guard["owner_harness_id"] != owner["harness_id"]
        or guard["fresh_harness_id"] != fresh["harness_id"]
        or type(guard["request_remaining_uses"]) is not int
        or guard["request_remaining_uses"] != 1
        or type(guard["reply_remaining_uses"]) is not int
        or guard["reply_remaining_uses"] != 1
        or guard["state_at_commit"] != BOOTSTRAP_PLAN_GUARD_STATE_AT_COMMIT
        or guard["request_payload_schema"] != request_schema
        or guard["reply_payload_schema"] != reply_schema
        or guard["request_payload"] != request_payload
        or guard["reply_payload"] != reply_payload
        or guard["request_payload_schema_digest"] != _digest(request_schema)
        or guard["reply_payload_schema_digest"] != _digest(reply_schema)
        or guard["request_payload_digest"] != _digest(request_payload)
        or guard["reply_payload_digest"] != _digest(reply_payload)
    ):
        raise _denied()
    c0 = _keys(
        preimage["c0"],
        {
            "classification",
            "request_payload_schema_digest",
            "request_payload_digest",
            "reply_payload_schema_digest",
            "reply_payload_digest",
        },
    )
    if c0 != {
        "classification": "C0",
        "request_payload_schema_digest": _digest(request_schema),
        "request_payload_digest": _digest(request_payload),
        "reply_payload_schema_digest": _digest(reply_schema),
        "reply_payload_digest": _digest(reply_payload),
    }:
        raise _denied()

    items = transaction["items"]
    if not isinstance(items, list) or len(items) != 10:
        raise _denied()
    communication_specs = (
        (1, "message.send", "direct", ["fresh_to_owner_send", "owner_to_fresh_send"]),
        (2, "mailbox.read", owner["harness_id"], ["owner_mailbox_read"]),
        (3, "mailbox.acknowledge", owner["harness_id"], ["owner_mailbox_acknowledge"]),
        (4, "mailbox.read", fresh["harness_id"], ["fresh_mailbox_read"]),
        (5, "mailbox.acknowledge", fresh["harness_id"], ["fresh_mailbox_acknowledge"]),
    )
    entitlement_ids = {ordinal: _derived_id(plan_digest, "entitlement", ordinal) for ordinal in range(1, 11)}
    common_item_keys = {
        "item_ordinal",
        "item_id",
        "plan_id",
        "domain_id",
        "principal_id",
        "revision",
        "item_kind",
        "action",
        "resource_pattern",
        "guard_id",
        "expires_at",
        "entitlement",
    }
    entitlement_keys = {
        "entitlement_id",
        "domain_id",
        "principal_id",
        "action",
        "resource_pattern",
        "revision",
        "expires_at",
        "revoked_at",
    }
    for ordinal, item in enumerate(items, 1):
        extra = {"operation_scopes"} if ordinal <= 5 else {"target_communication_ordinal", "target_entitlement_id"}
        current = _keys(item, common_item_keys | extra)
        if (
            type(current["item_ordinal"]) is not int
            or current["item_ordinal"] != ordinal
            or current["item_id"] != _derived_id(plan_digest, "item", ordinal)
            or current["plan_id"] != transaction["plan_id"]
            or current["domain_id"] != domain["domain_id"]
            or current["principal_id"] != principal["principal_id"]
            or not _positive_int(current["revision"])
            or current["revision"] != domain["policy_revision"]
            or current["guard_id"] != guard_id
            or not _positive_int(current["expires_at"])
            or current["expires_at"] != authority_expires_at
        ):
            raise _denied()
        entitlement = _keys(current["entitlement"], entitlement_keys)
        if (
            entitlement["entitlement_id"] != entitlement_ids[ordinal]
            or entitlement["domain_id"] != domain["domain_id"]
            or entitlement["principal_id"] != principal["principal_id"]
            or not _positive_int(entitlement["revision"])
            or entitlement["revision"] != domain["policy_revision"]
            or not _positive_int(entitlement["expires_at"])
            or entitlement["expires_at"] != authority_expires_at
            or entitlement["revoked_at"] is not None
        ):
            raise _denied()
        if ordinal <= 5:
            _, action, resource, scopes = communication_specs[ordinal - 1]
            if (
                current["item_kind"] != "communication"
                or current["action"] != action
                or current["resource_pattern"] != resource
                or current["operation_scopes"] != scopes
            ):
                raise _denied()
        else:
            target_ordinal = ordinal - 5
            target = entitlement_ids[target_ordinal]
            resource = f"entitlement:{target}"
            if (
                current["item_kind"] != "exact_revoke"
                or current["action"] != "authorization.entitlement.revoke"
                or current["resource_pattern"] != resource
                or type(current["target_communication_ordinal"]) is not int
                or current["target_communication_ordinal"] != target_ordinal
                or current["target_entitlement_id"] != target
            ):
                raise _denied()
        if entitlement["action"] != current["action"] or entitlement["resource_pattern"] != current["resource_pattern"]:
            raise _denied()

    return {
        "title": "Approve a bounded C0 laptop communication plan",
        "statements": [
            "Same verified person: two enrolled laptop harnesses",
            f"Owner laptop: {owner['display_name']} ({owner['kind']})",
            f"Fresh laptop: {fresh['display_name']} ({fresh['kind']})",
            "Classification: C0",
            "Communication: one fixed harmless request and one fixed harmless reply",
            "Authority: five communication permissions and five exact cleanup permissions",
            "Expiry: communication and cleanup authority ends within one hour",
            "Safety: communication remains unusable until the exact C0 guard ships",
            "Independent approval boundary: not proven",
        ],
    }


def _validate_communication_scope(transaction: dict[str, Any]) -> dict[str, Any]:
    preimage = transaction.get("canonical_scope_preimage")
    if not isinstance(preimage, dict):
        raise _denied()
    try:
        expected = build_communication_scope_transaction(preimage)
    except (KeyError, TypeError, ValueError) as exc:
        raise _denied() from exc
    if transaction != expected:
        raise _denied()
    harnesses = preimage["harnesses"]
    owner = harnesses["owner"]
    fresh = harnesses["fresh"]
    for harness in (owner, fresh):
        for key in ("display_name", "kind"):
            value = harness[key]
            if (
                not isinstance(value, str)
                or not 1 <= len(value) <= 128
                or any(ord(character) < 0x20 or ord(character) == 0x7F for character in value)
            ):
                raise _denied()
    if transaction["item_count"] != len(COMMUNICATION_SCOPE_ACTIONS) * 2:
        raise _denied()
    return {
        "title": "Approve persistent communication",
        "statements": [
            "Same verified person: one owner OMP laptop and one ordinary server agent",
            f"Owner laptop: {owner['display_name']} ({owner['kind']})",
            f"Server agent: {fresh['display_name']} ({fresh['kind']})",
            (
                "Communication: messages, mailbox delivery, conversations, rooms, "
                "and response obligations"
            ),
            (
                f"Authority: {len(COMMUNICATION_SCOPE_ACTIONS)} communication permissions "
                "for each exact enrolled harness"
            ),
            "Expiry: this communication authority does not expire automatically",
            "Revocation: either exact harness or any individual permission can be revoked",
            "Excluded: files, artifacts, tools, business effects, federation, and public A2A",
            "Independent approval boundary: not proven",
        ],
    }


def validate_and_summarize_approval_transaction(
    purpose: str,
    canonical_transaction: bytes,
    transaction_digest: str,
) -> dict[str, Any]:
    """Validate exact purpose bytes before challenge persistence and return safe browser text."""

    if (
        not isinstance(purpose, str)
        or not isinstance(canonical_transaction, bytes)
        or len(transaction_digest) != 64
        or hashlib.sha256(canonical_transaction).hexdigest() != transaction_digest
    ):
        raise _denied()
    transaction = _strict_object(canonical_transaction)
    if purpose == "identity.enrollment.approve":
        return _validate_enrollment(transaction, transaction_digest)
    if purpose == "identity.harness.revoke.approve":
        return _validate_harness_revocation(transaction, transaction_digest)
    if (
        purpose == _MANAGED_SERVER_REAUTHORIZATION_PURPOSE
        and transaction.get("schema")
        in {
            "agentnet.managed-server-credential-reauthorization.v1",
            "agentnet.managed-server-credential-reauthorization.v2",
        }
    ):
        return _validate_managed_server_credential_reauthorization(
            transaction,
            transaction_digest,
        )
    if (
        purpose == _LAPTOP_CREDENTIAL_REAUTHORIZATION_PURPOSE
        and transaction.get("schema")
        == "agentnet.laptop-credential-reauthorization.v1"
    ):
        return _validate_laptop_credential_reauthorization(
            transaction,
            transaction_digest,
        )
    if purpose == BOOTSTRAP_PLAN_APPROVAL_PURPOSE:
        return _validate_bootstrap(transaction)
    if purpose == COMMUNICATION_SCOPE_APPROVAL_PURPOSE:
        return _validate_communication_scope(transaction)
    raise _denied()


__all__ = ["validate_and_summarize_approval_transaction"]
