"""First-release durable contracts for higher-level product lifecycles.

AgentNet starts from this complete schema. Higher-level services create these
records in the same authoritative transaction as the state transition they
govern; there is no prototype-schema retrofit path.
"""

from __future__ import annotations

from typing import Any

from agentnet.errors import GateBlocked


POST_AUDIT_SCHEMA_VERSION = 1

POST_AUDIT_SCHEMA = """
CREATE TABLE IF NOT EXISTS task_execution_intents (
    event_id TEXT PRIMARY KEY REFERENCES events(event_id),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    recipient_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    recipient_authority_id TEXT NOT NULL,
    sender_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    sender_authority_id TEXT NOT NULL,
    authority_basis TEXT NOT NULL CHECK (authority_basis IN (
        'directed_relationship','recipient_owner_approval'
    )),
    relationship_id TEXT,
    relationship_revision INTEGER NOT NULL CHECK (relationship_revision >= 0),
    intent_schema_version TEXT NOT NULL CHECK (intent_schema_version = '1.0'),
    intent_json TEXT NOT NULL,
    intent_digest TEXT NOT NULL,
    continuation_encrypted TEXT NOT NULL,
    continuation_digest TEXT NOT NULL,
    continuation_applied INTEGER NOT NULL CHECK (continuation_applied IN (0,1)),
    state TEXT NOT NULL CHECK (state IN (
        'active','conflict_pending','released','rejected','canceled','expired','completed'
    )),
    state_revision INTEGER NOT NULL CHECK (state_revision >= 1),
    deadline INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (deadline > created_at),
    UNIQUE(domain_id,recipient_harness_id,event_id)
);
CREATE INDEX IF NOT EXISTS idx_task_execution_intents_open
    ON task_execution_intents(domain_id,recipient_harness_id,state,deadline,event_id);
CREATE INDEX IF NOT EXISTS idx_task_execution_intents_digest
    ON task_execution_intents(domain_id,intent_digest);

CREATE TABLE IF NOT EXISTS task_conflicts (
    conflict_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    recipient_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    recipient_authority_id TEXT NOT NULL,
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    recipient_credential_epoch INTEGER NOT NULL CHECK (recipient_credential_epoch >= 1),
    resource_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','resolved')),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    decided_at INTEGER,
    decision_actor_json TEXT,
    decision_digest TEXT,
    reason_code TEXT,
    CHECK (
        (state='pending' AND decided_at IS NULL AND decision_actor_json IS NULL
         AND decision_digest IS NULL AND reason_code IS NULL)
        OR
        (state='resolved' AND decided_at IS NOT NULL AND decision_actor_json IS NOT NULL
         AND decision_digest IS NOT NULL AND reason_code IS NOT NULL)
    ),
    UNIQUE(domain_id,recipient_harness_id,resource_key)
);
CREATE INDEX IF NOT EXISTS idx_task_conflicts_pending
    ON task_conflicts(domain_id,recipient_authority_id,state,created_at,conflict_id);

CREATE TABLE IF NOT EXISTS task_conflict_memberships (
    conflict_id TEXT NOT NULL REFERENCES task_conflicts(conflict_id),
    event_id TEXT NOT NULL REFERENCES task_execution_intents(event_id),
    member_state TEXT NOT NULL CHECK (member_state IN ('pending','released','rejected')),
    joined_at INTEGER NOT NULL,
    decided_at INTEGER,
    PRIMARY KEY(conflict_id,event_id),
    CHECK (
        (member_state='pending' AND decided_at IS NULL)
        OR (member_state IN ('released','rejected') AND decided_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_task_conflict_members_event
    ON task_conflict_memberships(event_id,member_state,conflict_id);

CREATE TABLE IF NOT EXISTS internal_invitations (
    invitation_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    sponsor_authority_kind TEXT NOT NULL CHECK (sponsor_authority_kind IN ('human','guest')),
    sponsor_authority_id TEXT NOT NULL,
    sponsor_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    sponsor_credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    sponsor_credential_epoch INTEGER NOT NULL CHECK (sponsor_credential_epoch >= 1),
    invited_oidc_issuer TEXT NOT NULL,
    invited_oidc_subject TEXT NOT NULL,
    invited_verified_email TEXT NOT NULL,
    candidate_harness_id TEXT NOT NULL,
    candidate_harness_kind TEXT NOT NULL,
    candidate_key_id TEXT NOT NULL,
    candidate_public_key_pem TEXT NOT NULL,
    requested_capabilities_json TEXT NOT NULL,
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    max_uses INTEGER NOT NULL CHECK (max_uses = 1),
    use_count INTEGER NOT NULL CHECK (use_count IN (0,1)),
    state TEXT NOT NULL CHECK (state IN ('active','consumed','revoked','expired')),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    canonical_invitation_json TEXT NOT NULL,
    invitation_digest TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    consumed_at INTEGER,
    revoked_at INTEGER,
    accepted_principal_id TEXT REFERENCES principals(principal_id),
    accepted_harness_id TEXT REFERENCES harnesses(harness_id),
    CHECK (expires_at > created_at),
    CHECK (
        (state='active' AND use_count=0 AND consumed_at IS NULL AND revoked_at IS NULL
         AND accepted_principal_id IS NULL AND accepted_harness_id IS NULL)
        OR
        (state='consumed' AND use_count=1 AND consumed_at IS NOT NULL AND revoked_at IS NULL
         AND accepted_principal_id IS NOT NULL AND accepted_harness_id IS NOT NULL)
        OR
        (state IN ('revoked','expired') AND use_count=0 AND consumed_at IS NULL
         AND accepted_principal_id IS NULL AND accepted_harness_id IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_internal_invitations_candidate
    ON internal_invitations(
        domain_id,invited_oidc_issuer,invited_oidc_subject,candidate_harness_id,state,expires_at
    );
CREATE INDEX IF NOT EXISTS idx_internal_invitations_sponsor
    ON internal_invitations(domain_id,sponsor_authority_id,state,created_at,invitation_id);

CREATE TABLE IF NOT EXISTS internal_invitation_abuse (
    invitation_id TEXT NOT NULL REFERENCES internal_invitations(invitation_id),
    source_fingerprint TEXT NOT NULL,
    window_started_at INTEGER NOT NULL,
    failure_count INTEGER NOT NULL CHECK (failure_count >= 0),
    locked_until INTEGER,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(invitation_id,source_fingerprint)
);
CREATE INDEX IF NOT EXISTS idx_internal_invitation_abuse_lock
    ON internal_invitation_abuse(locked_until,updated_at,invitation_id);

CREATE TABLE IF NOT EXISTS internal_invitation_oidc_transactions (
    transaction_id TEXT PRIMARY KEY,
    invitation_id TEXT NOT NULL REFERENCES internal_invitations(invitation_id),
    invitation_digest TEXT NOT NULL,
    invitation_revision INTEGER NOT NULL CHECK (invitation_revision >= 1),
    verifier_id TEXT NOT NULL,
    issuer TEXT NOT NULL,
    client_id TEXT NOT NULL,
    audience TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    state_hash TEXT NOT NULL UNIQUE,
    nonce_hash TEXT NOT NULL,
    code_verifier_encrypted TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'pending','exchanging','verified','consumed','failed'
    )),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    claimed_at INTEGER,
    consumed_at INTEGER,
    authorization_code_hash TEXT UNIQUE,
    id_token_hash TEXT UNIQUE,
    verification_result_encrypted TEXT,
    acceptance_token_hash TEXT UNIQUE,
    CHECK (expires_at > created_at),
    CHECK (
        (status='pending' AND claimed_at IS NULL AND consumed_at IS NULL
         AND authorization_code_hash IS NULL AND id_token_hash IS NULL
         AND verification_result_encrypted IS NULL AND acceptance_token_hash IS NULL)
        OR
        (status='exchanging' AND claimed_at IS NOT NULL AND consumed_at IS NULL
         AND authorization_code_hash IS NULL AND id_token_hash IS NULL
         AND verification_result_encrypted IS NULL AND acceptance_token_hash IS NULL)
        OR
        (status='verified' AND claimed_at IS NOT NULL AND consumed_at IS NULL
         AND authorization_code_hash IS NOT NULL AND id_token_hash IS NOT NULL
         AND verification_result_encrypted IS NOT NULL AND acceptance_token_hash IS NOT NULL)
        OR
        (status='consumed' AND claimed_at IS NOT NULL AND consumed_at IS NOT NULL
         AND authorization_code_hash IS NOT NULL AND id_token_hash IS NOT NULL
         AND verification_result_encrypted IS NOT NULL AND acceptance_token_hash IS NOT NULL)
        OR
        (status='failed' AND consumed_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_internal_invitation_oidc_pending
    ON internal_invitation_oidc_transactions(status,expires_at,transaction_id);
CREATE INDEX IF NOT EXISTS idx_internal_invitation_oidc_invitation
    ON internal_invitation_oidc_transactions(invitation_id,status,transaction_id);
CREATE INDEX IF NOT EXISTS idx_internal_invitation_oidc_verified
    ON internal_invitation_oidc_transactions(status,expires_at,transaction_id);

CREATE TABLE IF NOT EXISTS domain_incident_controls (
    domain_id TEXT PRIMARY KEY REFERENCES domains(domain_id),
    mode TEXT NOT NULL CHECK (mode IN (
        'normal','freeze_new_authority','freeze_privileged','freeze_all'
    )),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    reason TEXT NOT NULL,
    actor_json TEXT NOT NULL,
    policy_decision_id TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_domain_incident_controls_mode
    ON domain_incident_controls(mode,updated_at,domain_id);

CREATE TABLE IF NOT EXISTS automation_charters (
    charter_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    accountable_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    accountable_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    workload_registration_id TEXT NOT NULL REFERENCES workload_registrations(registration_id),
    workload_id TEXT NOT NULL,
    triggers_json TEXT NOT NULL,
    actions_json TEXT NOT NULL,
    resources_json TEXT NOT NULL,
    sinks_json TEXT NOT NULL,
    data_classes_json TEXT NOT NULL,
    budgets_json TEXT NOT NULL,
    max_fanout INTEGER NOT NULL CHECK (max_fanout >= 1),
    max_spend_micros INTEGER NOT NULL CHECK (max_spend_micros >= 0),
    approval_threshold INTEGER NOT NULL CHECK (approval_threshold >= 1),
    approval_set_digest TEXT NOT NULL,
    proposer_actor_json TEXT NOT NULL,
    reason TEXT NOT NULL,
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    domain_revocation_epoch INTEGER NOT NULL CHECK (domain_revocation_epoch >= 1),
    workload_credential_epoch INTEGER NOT NULL CHECK (workload_credential_epoch >= 1),
    use_limit INTEGER NOT NULL CHECK (use_limit >= 1),
    use_count INTEGER NOT NULL CHECK (use_count >= 0 AND use_count <= use_limit),
    state TEXT NOT NULL CHECK (state IN (
        'proposed','active','revoked','expired','emergency_stopped'
    )),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    canonical_charter_json TEXT NOT NULL,
    charter_digest TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    activated_at INTEGER,
    revoked_at INTEGER,
    emergency_stopped_at INTEGER,
    CHECK (expires_at > created_at),
    CHECK (
        (state='proposed' AND activated_at IS NULL AND revoked_at IS NULL
         AND emergency_stopped_at IS NULL)
        OR
        (state='active' AND activated_at IS NOT NULL AND revoked_at IS NULL
         AND emergency_stopped_at IS NULL)
        OR
        (state='revoked' AND revoked_at IS NOT NULL
         AND emergency_stopped_at IS NULL)
        OR
        (state='expired' AND revoked_at IS NOT NULL
         AND emergency_stopped_at IS NULL)
        OR
        (state='emergency_stopped' AND activated_at IS NOT NULL
         AND revoked_at IS NOT NULL AND emergency_stopped_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_automation_charters_active
    ON automation_charters(domain_id,workload_id,state,expires_at,charter_id);
CREATE INDEX IF NOT EXISTS idx_automation_charters_accountable
    ON automation_charters(domain_id,accountable_principal_id,state,charter_id);

CREATE TABLE IF NOT EXISTS automation_charter_approvals (
    charter_id TEXT NOT NULL REFERENCES automation_charters(charter_id),
    receipt_id TEXT NOT NULL UNIQUE,
    receipt_digest TEXT NOT NULL UNIQUE,
    receipt_json TEXT NOT NULL,
    approver_authority_kind TEXT NOT NULL CHECK (approver_authority_kind IN ('human','guest')),
    approver_authority_id TEXT NOT NULL,
    verifier_id TEXT NOT NULL,
    signer_key_id TEXT NOT NULL,
    receipt_expires_at INTEGER NOT NULL,
    consumed_at INTEGER NOT NULL,
    PRIMARY KEY(charter_id,receipt_id)
);
CREATE INDEX IF NOT EXISTS idx_automation_charter_approvals_charter
    ON automation_charter_approvals(charter_id,approver_authority_id,receipt_id);

CREATE TABLE IF NOT EXISTS automation_charter_uses (
    use_id TEXT PRIMARY KEY,
    charter_id TEXT NOT NULL REFERENCES automation_charters(charter_id),
    invocation_id TEXT NOT NULL,
    intent_digest TEXT NOT NULL,
    intent_json TEXT NOT NULL,
    charter_revision INTEGER NOT NULL CHECK (charter_revision >= 1),
    workload_credential_epoch INTEGER NOT NULL CHECK (workload_credential_epoch >= 1),
    fanout INTEGER NOT NULL CHECK (fanout >= 0),
    spend_micros INTEGER NOT NULL CHECK (spend_micros >= 0),
    state TEXT NOT NULL CHECK (state IN ('reserved','committed','released','failed')),
    result_digest TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER,
    UNIQUE(charter_id,invocation_id),
    UNIQUE(charter_id,intent_digest),
    CHECK (
        (state='reserved' AND result_digest IS NULL AND completed_at IS NULL)
        OR
        (state IN ('committed','released','failed')
         AND result_digest IS NOT NULL AND completed_at IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_automation_charter_uses_state
    ON automation_charter_uses(charter_id,state,created_at,use_id);

CREATE TABLE IF NOT EXISTS relay_peer_keys (
    local_domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    peer_domain_id TEXT NOT NULL,
    key_id TEXT NOT NULL,
    key_epoch INTEGER NOT NULL CHECK (key_epoch >= 1),
    key_fingerprint TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','active','overlap','retired','revoked')),
    not_before INTEGER NOT NULL,
    overlap_until INTEGER,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER,
    rotation_digest TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(local_domain_id,peer_domain_id,key_id),
    UNIQUE(local_domain_id,peer_domain_id,key_epoch),
    CHECK (expires_at > not_before),
    CHECK (overlap_until IS NULL OR overlap_until < expires_at),
    CHECK (revoked_at IS NULL OR state='revoked')
);
CREATE INDEX IF NOT EXISTS idx_relay_peer_keys_state
    ON relay_peer_keys(local_domain_id,peer_domain_id,state,key_epoch);
CREATE INDEX IF NOT EXISTS idx_relay_peer_keys_expiry
    ON relay_peer_keys(expires_at,state,local_domain_id,peer_domain_id);

CREATE TABLE IF NOT EXISTS relay_peer_key_mutations (
    mutation_id TEXT PRIMARY KEY,
    local_domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    peer_domain_id TEXT NOT NULL,
    mutation_type TEXT NOT NULL CHECK (mutation_type IN ('rotate','compromise_revoke')),
    from_key_id TEXT,
    to_key_id TEXT,
    expected_from_epoch INTEGER NOT NULL CHECK (expected_from_epoch >= 0),
    resulting_epoch INTEGER NOT NULL CHECK (resulting_epoch >= 1),
    manifest_digest TEXT NOT NULL UNIQUE,
    actor_json TEXT NOT NULL,
    policy_decision_id TEXT,
    state TEXT NOT NULL CHECK (state = 'completed'),
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_relay_peer_key_mutations_peer
    ON relay_peer_key_mutations(local_domain_id,peer_domain_id,created_at,mutation_id);

CREATE TABLE IF NOT EXISTS content_provenance (
    object_type TEXT NOT NULL CHECK (object_type IN (
        'event','task','artifact','model_output','tool_output','parser_output'
    )),
    object_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    origin_json TEXT NOT NULL,
    transformations_json TEXT NOT NULL,
    parent_digests_json TEXT NOT NULL,
    review_state TEXT NOT NULL CHECK (review_state IN (
        'unreviewed','reviewed','rejected','quarantined'
    )),
    scan_state TEXT NOT NULL CHECK (scan_state IN (
        'not_required','pending','passed','failed','stale'
    )),
    classification TEXT NOT NULL CHECK (classification IN ('C0','C1','C2','C3')),
    allowed_sinks_json TEXT NOT NULL,
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 1),
    tainted INTEGER NOT NULL CHECK (tainted IN (0,1)),
    provenance_digest TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(object_type,object_id,version)
);
CREATE INDEX IF NOT EXISTS idx_content_provenance_object
    ON content_provenance(domain_id,object_type,object_id,version);
CREATE INDEX IF NOT EXISTS idx_content_provenance_taint
    ON content_provenance(domain_id,tainted,review_state,scan_state,classification);

CREATE TABLE IF NOT EXISTS event_provenance (
    event_id TEXT PRIMARY KEY REFERENCES events(event_id),
    provenance_digest TEXT NOT NULL UNIQUE REFERENCES content_provenance(provenance_digest),
    reference_json TEXT NOT NULL,
    object_type TEXT NOT NULL CHECK (object_type IN ('event','task')),
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_event_provenance_object
    ON event_provenance(object_type,event_id,provenance_digest);
"""

POST_AUDIT_REQUIRED_TABLES = frozenset(
    {
        "task_execution_intents",
        "task_conflicts",
        "task_conflict_memberships",
        "internal_invitations",
        "internal_invitation_abuse",
        "internal_invitation_oidc_transactions",
        "domain_incident_controls",
        "automation_charters",
        "automation_charter_approvals",
        "automation_charter_uses",
        "relay_peer_keys",
        "relay_peer_key_mutations",
        "content_provenance",
        "event_provenance",
    }
)
POST_AUDIT_REQUIRED_INDEXES = frozenset(
    {
        "idx_task_execution_intents_open",
        "idx_task_execution_intents_digest",
        "idx_task_conflicts_pending",
        "idx_task_conflict_members_event",
        "idx_internal_invitations_candidate",
        "idx_internal_invitations_sponsor",
        "idx_internal_invitation_abuse_lock",
        "idx_internal_invitation_oidc_pending",
        "idx_internal_invitation_oidc_invitation",
        "idx_internal_invitation_oidc_verified",
        "idx_domain_incident_controls_mode",
        "idx_automation_charters_active",
        "idx_automation_charters_accountable",
        "idx_automation_charter_approvals_charter",
        "idx_automation_charter_uses_state",
        "idx_relay_peer_keys_state",
        "idx_relay_peer_keys_expiry",
        "idx_relay_peer_key_mutations_peer",
        "idx_content_provenance_object",
        "idx_content_provenance_taint",
        "idx_event_provenance_object",
    }
)


def require_post_audit_schema(store: Any) -> None:
    """Fail closed unless the clean first-release schema is complete."""

    backend = getattr(store, "backend_name", "")
    try:
        metadata = store.fetch_one("SELECT value FROM metadata WHERE key='schema_version'")
        if metadata is None or int(metadata["value"]) < POST_AUDIT_SCHEMA_VERSION:
            raise GateBlocked("post_audit_schema", "post-audit lifecycle schema is not current")
        if backend == "sqlite":
            missing_tables = {
                name
                for name in POST_AUDIT_REQUIRED_TABLES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name=?", (name,)
                )
                is None
            }
            missing_indexes = {
                name
                for name in POST_AUDIT_REQUIRED_INDEXES
                if store.fetch_one(
                    "SELECT name FROM sqlite_master WHERE type='index' AND name=?", (name,)
                )
                is None
            }
        elif backend == "postgresql":
            migration = store.fetch_one(
                "SELECT COALESCE(MAX(version),0) AS version FROM schema_migrations"
            )
            if migration is None or int(migration["version"]) < POST_AUDIT_SCHEMA_VERSION:
                raise GateBlocked("post_audit_schema", "post-audit migration is not current")
            missing_tables = {
                name
                for name in POST_AUDIT_REQUIRED_TABLES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
            missing_indexes = {
                name
                for name in POST_AUDIT_REQUIRED_INDEXES
                if not (
                    (row := store.fetch_one("SELECT to_regclass(?) AS relation", (name,)))
                    and row["relation"] is not None
                )
            }
        else:
            raise GateBlocked("post_audit_schema", "post-audit backend is unsupported")
    except GateBlocked:
        raise
    except Exception as exc:
        raise GateBlocked(
            "post_audit_schema", "post-audit lifecycle schema could not be verified"
        ) from exc
    if missing_tables or missing_indexes:
        raise GateBlocked("post_audit_schema", "post-audit lifecycle relations are missing")


__all__ = [
    "POST_AUDIT_REQUIRED_INDEXES",
    "POST_AUDIT_REQUIRED_TABLES",
    "POST_AUDIT_SCHEMA",
    "POST_AUDIT_SCHEMA_VERSION",
    "require_post_audit_schema",
]
