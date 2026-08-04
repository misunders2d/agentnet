"""Persistent exact-harness communication authority schema."""

from __future__ import annotations


COMMUNICATION_SCOPE_SCHEMA_VERSION = 6
COMMUNICATION_SCOPE_TABLE_DDL = """
CREATE TABLE IF NOT EXISTS communication_scopes (
    scope_id TEXT PRIMARY KEY,
    profile TEXT NOT NULL CHECK (profile='same-principal-full-communication:v1'),
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
    canonical_scope_preimage_json TEXT NOT NULL,
    final_approval_transaction_json TEXT NOT NULL,
    scope_digest TEXT NOT NULL UNIQUE CHECK (length(scope_digest)=64),
    transaction_digest TEXT NOT NULL UNIQUE CHECK (length(transaction_digest)=64),
    begin_idempotency_key_sha256 TEXT NOT NULL UNIQUE CHECK (length(begin_idempotency_key_sha256)=64),
    begin_response_encrypted TEXT,
    state TEXT NOT NULL CHECK (state IN (
        'reserved','pending_approval','approval_issued','completion_reserved','committed',
        'rejected','canceled','expired','invalidated'
    )),
    independent_boundary_proven INTEGER NOT NULL DEFAULT 0 CHECK (independent_boundary_proven=0),
    max_uses INTEGER NOT NULL DEFAULT 1 CHECK (max_uses=1),
    created_at INTEGER NOT NULL,
    approval_expires_at INTEGER NOT NULL CHECK (approval_expires_at > created_at),
    authority_expires_at INTEGER CHECK (authority_expires_at IS NULL),
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
    CHECK (owner_credential_id <> fresh_credential_id),
    CHECK (
        (state='reserved' AND approval_request_id IS NULL AND approval_issued_at IS NULL
            AND completion_reserved_at IS NULL AND committed_at IS NULL)
        OR (state='pending_approval' AND approval_request_id IS NOT NULL
            AND approval_issued_at IS NULL AND completion_reserved_at IS NULL AND committed_at IS NULL)
        OR (state='approval_issued' AND approval_request_id IS NOT NULL
            AND approval_issued_at IS NOT NULL AND completion_reserved_at IS NULL AND committed_at IS NULL)
        OR (state='completion_reserved' AND approval_request_id IS NOT NULL
            AND approval_issued_at IS NOT NULL AND completion_reserved_at IS NOT NULL
            AND completion_idempotency_key_sha256 IS NOT NULL
            AND completion_request_digest IS NOT NULL AND committed_at IS NULL)
        OR (state='committed' AND approval_request_id IS NOT NULL
            AND approval_issued_at IS NOT NULL AND completion_reserved_at IS NOT NULL
            AND completion_idempotency_key_sha256 IS NOT NULL
            AND completion_request_digest IS NOT NULL AND committed_at IS NOT NULL
            AND committed_result_encrypted IS NOT NULL AND committed_result_digest IS NOT NULL
            AND audit_record_hash IS NOT NULL)
        OR (state IN ('rejected','canceled','expired','invalidated') AND terminal_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_communication_scopes_current
    ON communication_scopes(domain_id,principal_id,profile,state);
CREATE TABLE IF NOT EXISTS communication_scope_items (
    scope_id TEXT NOT NULL REFERENCES communication_scopes(scope_id) ON DELETE RESTRICT,
    item_ordinal INTEGER NOT NULL CHECK (item_ordinal BETWEEN 1 AND 38),
    item_id TEXT NOT NULL UNIQUE,
    entitlement_id TEXT NOT NULL UNIQUE REFERENCES entitlements(entitlement_id) ON DELETE RESTRICT,
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action IN (
        'message.send','mailbox.read','mailbox.acknowledge',
        'conversation.create','conversation.message.send','conversation.task.request',
        'conversation.task.handoff','conversation.task.cancel_request','conversation.task.complete',
        'conversation.structured_request.send','conversation.response_obligation.respond',
        'conversation.thread',
        'conversation.response_obligation.create','conversation.response_obligation.read',
        'conversation.response_obligation.transition','conversation.response_obligation.cancel',
        'room.create','room.action','room.read'
    )),
    resource_pattern TEXT NOT NULL CHECK (resource_pattern='*'),
    item_json TEXT NOT NULL,
    expires_at INTEGER CHECK (expires_at IS NULL),
    PRIMARY KEY(scope_id,item_ordinal),
    UNIQUE(scope_id,harness_id,action)
);
CREATE INDEX IF NOT EXISTS idx_communication_scope_items_entitlement_harness
    ON communication_scope_items(entitlement_id,harness_id);
"""


__all__ = ["COMMUNICATION_SCOPE_SCHEMA_VERSION", "COMMUNICATION_SCOPE_TABLE_DDL"]
