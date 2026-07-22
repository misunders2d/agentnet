"""Bounded same-principal two-harness C0 bootstrap-plan schema."""

from __future__ import annotations


BOOTSTRAP_PLAN_SCHEMA_VERSION = 4

BOOTSTRAP_PLAN_SCHEMA = """
CREATE TABLE IF NOT EXISTS bootstrap_grant_plans (
    plan_id TEXT PRIMARY KEY,
    profile TEXT NOT NULL CHECK (profile='ordinary-two-harness-c0:v1'),
    profile_version INTEGER NOT NULL CHECK (profile_version=1),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    owner_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    fresh_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    owner_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    fresh_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    owner_credential_epoch INTEGER NOT NULL CHECK (owner_credential_epoch > 0),
    fresh_credential_epoch INTEGER NOT NULL CHECK (fresh_credential_epoch > 0),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch > 0),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    actor_binding_json TEXT NOT NULL,
    canonical_plan_preimage_json TEXT NOT NULL,
    final_approval_transaction_json TEXT NOT NULL,
    plan_digest TEXT NOT NULL UNIQUE CHECK (length(plan_digest)=64),
    transaction_digest TEXT NOT NULL UNIQUE CHECK (length(transaction_digest)=64),
    begin_idempotency_key_sha256 TEXT NOT NULL UNIQUE CHECK (length(begin_idempotency_key_sha256)=64),
    begin_response_encrypted TEXT,
    state TEXT NOT NULL CHECK (state IN (
        'reserved','pending_approval','approval_issued','completion_reserved','committed',
        'rejected','canceled','expired','invalidated'
    )),
    independent_boundary_proven INTEGER NOT NULL DEFAULT 0
        CHECK (independent_boundary_proven=0),
    max_uses INTEGER NOT NULL DEFAULT 1 CHECK (max_uses=1),
    created_at INTEGER NOT NULL,
    approval_expires_at INTEGER NOT NULL CHECK (approval_expires_at > created_at),
    authority_expires_at INTEGER NOT NULL CHECK (authority_expires_at > approval_expires_at),
    approval_create_idempotency_key TEXT NOT NULL,
    approval_create_request_digest TEXT NOT NULL CHECK (length(approval_create_request_digest)=64),
    approval_request_id TEXT UNIQUE,
    approval_issued_at INTEGER,
    completion_reserved_at INTEGER,
    completion_idempotency_key_sha256 TEXT CHECK (
        completion_idempotency_key_sha256 IS NULL OR length(completion_idempotency_key_sha256)=64
    ),
    completion_request_digest TEXT UNIQUE CHECK (
        completion_request_digest IS NULL OR length(completion_request_digest)=64
    ),
    approval_receipt_id TEXT UNIQUE,
    approval_receipt_digest TEXT UNIQUE CHECK (
        approval_receipt_digest IS NULL OR length(approval_receipt_digest)=64
    ),
    committed_at INTEGER,
    committed_result_encrypted TEXT,
    committed_result_digest TEXT CHECK (
        committed_result_digest IS NULL OR length(committed_result_digest)=64
    ),
    audit_record_hash TEXT CHECK (audit_record_hash IS NULL OR length(audit_record_hash)=64),
    terminal_at INTEGER,
    invalidation_reason TEXT,
    CHECK (owner_harness_id <> fresh_harness_id),
    CHECK (
        (state='reserved' AND approval_request_id IS NULL AND approval_issued_at IS NULL
            AND completion_reserved_at IS NULL AND committed_at IS NULL)
        OR
        (state='pending_approval' AND approval_request_id IS NOT NULL
            AND approval_issued_at IS NULL AND completion_reserved_at IS NULL
            AND committed_at IS NULL)
        OR
        (state='approval_issued' AND approval_request_id IS NOT NULL
            AND approval_issued_at IS NOT NULL AND completion_reserved_at IS NULL
            AND committed_at IS NULL)
        OR
        (state='completion_reserved' AND approval_issued_at IS NOT NULL
            AND completion_reserved_at IS NOT NULL
            AND completion_idempotency_key_sha256 IS NOT NULL
            AND completion_request_digest IS NOT NULL AND committed_at IS NULL)
        OR
        (state='committed' AND approval_issued_at IS NOT NULL
            AND completion_reserved_at IS NOT NULL
            AND completion_idempotency_key_sha256 IS NOT NULL
            AND completion_request_digest IS NOT NULL AND committed_at IS NOT NULL
            AND committed_result_encrypted IS NOT NULL
            AND committed_result_digest IS NOT NULL AND audit_record_hash IS NOT NULL)
        OR
        (state IN ('rejected','canceled','expired','invalidated') AND terminal_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_bootstrap_grant_plans_active
    ON bootstrap_grant_plans(domain_id,principal_id,profile,state,authority_expires_at);

CREATE TABLE IF NOT EXISTS c0_plan_guards (
    guard_id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL UNIQUE REFERENCES bootstrap_grant_plans(plan_id) ON DELETE RESTRICT,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    owner_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    fresh_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    owner_credential_epoch INTEGER NOT NULL CHECK (owner_credential_epoch > 0),
    fresh_credential_epoch INTEGER NOT NULL CHECK (fresh_credential_epoch > 0),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch > 0),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    classification TEXT NOT NULL CHECK (classification='C0'),
    request_payload_schema TEXT NOT NULL,
    request_payload_schema_digest TEXT NOT NULL CHECK (length(request_payload_schema_digest)=64),
    request_payload_json TEXT NOT NULL,
    request_payload_digest TEXT NOT NULL CHECK (length(request_payload_digest)=64),
    reply_payload_schema TEXT NOT NULL,
    reply_payload_schema_digest TEXT NOT NULL CHECK (length(reply_payload_schema_digest)=64),
    reply_payload_json TEXT NOT NULL,
    reply_payload_digest TEXT NOT NULL CHECK (length(reply_payload_digest)=64),
    request_remaining_uses INTEGER NOT NULL DEFAULT 1 CHECK (request_remaining_uses BETWEEN 0 AND 1),
    reply_remaining_uses INTEGER NOT NULL DEFAULT 1 CHECK (reply_remaining_uses BETWEEN 0 AND 1),
    state TEXT NOT NULL CHECK (state IN ('pending','active','revoked','expired','invalidated')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at > created_at),
    invalidated_at INTEGER,
    CHECK (owner_harness_id <> fresh_harness_id)
);
CREATE INDEX IF NOT EXISTS idx_c0_plan_guards_active
    ON c0_plan_guards(domain_id,principal_id,state,expires_at);

CREATE TABLE IF NOT EXISTS bootstrap_grant_plan_items (
    plan_id TEXT NOT NULL REFERENCES bootstrap_grant_plans(plan_id) ON DELETE RESTRICT,
    item_ordinal INTEGER NOT NULL CHECK (item_ordinal BETWEEN 1 AND 10),
    item_id TEXT NOT NULL UNIQUE,
    entitlement_id TEXT NOT NULL UNIQUE,
    item_kind TEXT NOT NULL CHECK (item_kind IN ('communication','exact_revoke')),
    action TEXT NOT NULL CHECK (action IN (
        'message.send','mailbox.read','mailbox.acknowledge','authorization.entitlement.revoke'
    )),
    resource_pattern TEXT NOT NULL CHECK (resource_pattern <> '*'),
    guard_id TEXT NOT NULL REFERENCES c0_plan_guards(guard_id) ON DELETE RESTRICT,
    target_entitlement_id TEXT,
    item_json TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(plan_id,item_ordinal),
    UNIQUE(plan_id,item_id),
    CHECK (
        (item_kind='communication' AND action IN ('message.send','mailbox.read','mailbox.acknowledge')
            AND target_entitlement_id IS NULL)
        OR
        (item_kind='exact_revoke' AND action='authorization.entitlement.revoke'
            AND target_entitlement_id IS NOT NULL
            AND resource_pattern='entitlement:' || target_entitlement_id)
    )
);
CREATE INDEX IF NOT EXISTS idx_bootstrap_plan_items_entitlement
    ON bootstrap_grant_plan_items(entitlement_id,plan_id);

CREATE TABLE IF NOT EXISTS c0_plan_guard_entitlements (
    guard_id TEXT NOT NULL REFERENCES c0_plan_guards(guard_id) ON DELETE RESTRICT,
    entitlement_id TEXT NOT NULL REFERENCES entitlements(entitlement_id) ON DELETE RESTRICT,
    operation_scope TEXT NOT NULL CHECK (operation_scope IN (
        'fresh_to_owner_send','owner_to_fresh_send',
        'owner_mailbox_read','owner_mailbox_acknowledge',
        'fresh_mailbox_read','fresh_mailbox_acknowledge'
    )),
    actor_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    peer_harness_id TEXT REFERENCES harnesses(harness_id),
    PRIMARY KEY(guard_id,entitlement_id,operation_scope)
);
CREATE INDEX IF NOT EXISTS idx_c0_guard_entitlements_lookup
    ON c0_plan_guard_entitlements(entitlement_id,guard_id);

CREATE TABLE IF NOT EXISTS c0_pilot_attempts (
    attempt_id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL UNIQUE REFERENCES bootstrap_grant_plans(plan_id) ON DELETE RESTRICT,
    guard_id TEXT NOT NULL UNIQUE REFERENCES c0_plan_guards(guard_id) ON DELETE RESTRICT,
    request_idempotency_digest TEXT NOT NULL UNIQUE CHECK (length(request_idempotency_digest)=64),
    reply_idempotency_digest TEXT NOT NULL UNIQUE CHECK (length(reply_idempotency_digest)=64),
    state TEXT NOT NULL CHECK (state IN (
        'active','evidence_complete','communication_revoked','failed','expired'
    )),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at > created_at),
    evidence_completed_at INTEGER,
    communication_revoked_at INTEGER,
    terminal_at INTEGER,
    sanitized_result TEXT CHECK (
        sanitized_result IS NULL OR sanitized_result='COMPLETED_C0_ROUND_TRIP'
    )
);
CREATE INDEX IF NOT EXISTS idx_c0_pilot_attempts_state
    ON c0_pilot_attempts(state,expires_at);

CREATE TABLE IF NOT EXISTS c0_pilot_facts (
    attempt_id TEXT NOT NULL REFERENCES c0_pilot_attempts(attempt_id) ON DELETE RESTRICT,
    fact_kind TEXT NOT NULL CHECK (fact_kind IN (
        'request_durable_custody','request_retrieved','request_recipient_acknowledged',
        'reply_sent','reply_durable_custody','reply_retrieved','reply_final_acknowledged'
    )),
    issuer_kind TEXT NOT NULL CHECK (issuer_kind IN ('accepting_core','harness')),
    issuer_harness_id TEXT REFERENCES harnesses(harness_id),
    event_id TEXT NOT NULL,
    receipt_id TEXT,
    envelope_digest TEXT NOT NULL CHECK (length(envelope_digest)=64),
    storage_fact TEXT CHECK (storage_fact IS NULL OR storage_fact IN ('accepted_local','accepted_durable')),
    evidence_json TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY(attempt_id,fact_kind),
    CHECK (
        (issuer_kind='accepting_core' AND issuer_harness_id IS NULL
            AND fact_kind IN ('request_durable_custody','reply_durable_custody')
            AND receipt_id IS NOT NULL AND storage_fact IS NOT NULL)
        OR
        (issuer_kind='harness' AND issuer_harness_id IS NOT NULL
            AND fact_kind NOT IN ('request_durable_custody','reply_durable_custody')
            AND storage_fact IS NULL
            AND (
                (fact_kind IN ('request_recipient_acknowledged','reply_final_acknowledged')
                    AND receipt_id IS NOT NULL)
                OR
                (fact_kind IN ('request_retrieved','reply_sent','reply_retrieved')
                    AND receipt_id IS NULL)
            ))
    )
);
CREATE INDEX IF NOT EXISTS idx_c0_pilot_facts_event
    ON c0_pilot_facts(event_id,attempt_id);
"""

BOOTSTRAP_PLAN_REQUIRED_TABLES = frozenset(
    {
        "bootstrap_grant_plans",
        "bootstrap_grant_plan_items",
        "c0_plan_guards",
        "c0_plan_guard_entitlements",
        "c0_pilot_attempts",
        "c0_pilot_facts",
    }
)
BOOTSTRAP_PLAN_REQUIRED_INDEXES = frozenset(
    {
        "idx_bootstrap_grant_plans_active",
        "idx_bootstrap_plan_items_entitlement",
        "idx_c0_plan_guards_active",
        "idx_c0_guard_entitlements_lookup",
        "idx_c0_pilot_attempts_state",
        "idx_c0_pilot_facts_event",
    }
)


__all__ = [
    "BOOTSTRAP_PLAN_REQUIRED_INDEXES",
    "BOOTSTRAP_PLAN_REQUIRED_TABLES",
    "BOOTSTRAP_PLAN_SCHEMA",
    "BOOTSTRAP_PLAN_SCHEMA_VERSION",
]
