"""Durable, bounded state owned by the private administration console."""

ADMIN_CONSOLE_SCHEMA_VERSION = 7

ADMIN_CONSOLE_SCHEMA = """
CREATE TABLE IF NOT EXISTS console_session_challenges (
    challenge_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    credential_epoch INTEGER NOT NULL CHECK (credential_epoch >= 1),
    binding_assurance TEXT NOT NULL CHECK (binding_assurance IN ('os_bound','hardware_bound')),
    audience TEXT NOT NULL,
    nonce_hash TEXT NOT NULL CHECK (length(nonce_hash) = 64),
    transaction_digest TEXT NOT NULL CHECK (length(transaction_digest) = 64),
    state TEXT NOT NULL CHECK (state IN ('pending','completed','consumed','expired','failed')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    completed_at INTEGER,
    handoff_hash TEXT UNIQUE CHECK (handoff_hash IS NULL OR length(handoff_hash) = 64),
    handoff_expires_at INTEGER,
    handoff_consumed_at INTEGER,
    consumed_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_challenge_expiry_idx
    ON console_session_challenges(state,expires_at);

CREATE TABLE IF NOT EXISTS console_oidc_transactions (
    transaction_id TEXT PRIMARY KEY,
    challenge_id TEXT NOT NULL UNIQUE REFERENCES console_session_challenges(challenge_id),
    state_hash TEXT NOT NULL UNIQUE CHECK (length(state_hash) = 64),
    nonce_hash TEXT NOT NULL CHECK (length(nonce_hash) = 64),
    code_verifier_encrypted TEXT NOT NULL,
    preauth_hash TEXT NOT NULL UNIQUE CHECK (length(preauth_hash) = 64),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    exchange_started_at INTEGER,
    consumed_at INTEGER
);

CREATE TABLE IF NOT EXISTS console_browser_sessions (
    session_hash TEXT PRIMARY KEY CHECK (length(session_hash) = 64),
    session_id TEXT NOT NULL UNIQUE,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    credential_id TEXT NOT NULL REFERENCES credentials(credential_id),
    credential_epoch INTEGER NOT NULL CHECK (credential_epoch >= 1),
    binding_assurance TEXT NOT NULL CHECK (binding_assurance IN ('os_bound','hardware_bound')),
    csrf_secret_encrypted TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    authenticated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    rotated_from_hash TEXT UNIQUE,
    revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_session_harness_idx
    ON console_browser_sessions(harness_id,expires_at,revoked_at);

CREATE TABLE IF NOT EXISTS console_mutation_authorizations (
    authorization_hash TEXT PRIMARY KEY CHECK (length(authorization_hash) = 64),
    session_hash TEXT NOT NULL REFERENCES console_browser_sessions(session_hash) ON DELETE RESTRICT,
    method TEXT NOT NULL CHECK (method = upper(method)),
    path TEXT NOT NULL CHECK (substr(path,1,1) = '/'),
    body_sha256 TEXT NOT NULL CHECK (length(body_sha256) = 64),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at > created_at),
    consumed_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_mutation_authorization_expiry_idx
    ON console_mutation_authorizations(session_hash,expires_at,consumed_at);

CREATE TABLE IF NOT EXISTS console_server_status (
    harness_id TEXT PRIMARY KEY REFERENCES harnesses(harness_id),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    contribution_json TEXT NOT NULL,
    contribution_digest TEXT NOT NULL CHECK (length(contribution_digest) = 64),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    received_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS console_server_status_expiry_idx
    ON console_server_status(domain_id,expires_at);

CREATE TABLE IF NOT EXISTS console_enrollment_intents (
    intent_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    sponsor_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    sponsor_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('existing_person','new_person')),
    target_principal_id TEXT REFERENCES principals(principal_id),
    invited_email_alias TEXT,
    request_json TEXT NOT NULL,
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    state TEXT NOT NULL CHECK (state IN (
        'waiting_target','candidate_verified','waiting_approval','invitation_issued',
        'waiting_possession','enrolled','expired','canceled','blocked','failed','unknown'
    )),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    invitation_id TEXT UNIQUE,
    approval_request_id TEXT UNIQUE,
    approval_transaction_digest TEXT CHECK (
        approval_transaction_digest IS NULL OR length(approval_transaction_digest) = 64
    ),
    policy_decision_id TEXT REFERENCES policy_decisions(decision_id),
    canonical_invitation_json TEXT,
    candidate_transaction_id TEXT UNIQUE,
    possession_secret_encrypted TEXT,
    approval_transaction_json TEXT,
    result_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    terminal_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_enrollment_state_idx
    ON console_enrollment_intents(domain_id,state,expires_at);

CREATE TABLE IF NOT EXISTS console_enrollment_reviews (
    review_token_hash TEXT PRIMARY KEY CHECK (length(review_token_hash) = 64),
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    sponsor_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    sponsor_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    request_json TEXT NOT NULL,
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    state TEXT NOT NULL CHECK (state IN ('pending','consumed','expired')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_enrollment_review_expiry_idx
    ON console_enrollment_reviews(domain_id,state,expires_at);

CREATE TABLE IF NOT EXISTS console_enrollment_candidates (
    transaction_id TEXT PRIMARY KEY,
    begin_idempotency_hash TEXT NOT NULL UNIQUE CHECK (length(begin_idempotency_hash) = 64),
    begin_request_digest TEXT NOT NULL CHECK (length(begin_request_digest) = 64),
    state_hash TEXT NOT NULL UNIQUE CHECK (length(state_hash) = 64),
    nonce_hash TEXT NOT NULL CHECK (length(nonce_hash) = 64),
    continuation_hash TEXT NOT NULL UNIQUE CHECK (length(continuation_hash) = 64),
    begin_response_encrypted TEXT NOT NULL,
    code_verifier_encrypted TEXT NOT NULL,
    candidate_harness_id TEXT NOT NULL,
    candidate_harness_kind TEXT NOT NULL,
    candidate_harness_name TEXT NOT NULL,
    candidate_binding_assurance TEXT NOT NULL CHECK (
        candidate_binding_assurance IN ('os_bound','hardware_bound')
    ),
    candidate_public_key_pem TEXT NOT NULL,
    candidate_key_id TEXT NOT NULL CHECK (length(candidate_key_id) = 64),
    intent_id TEXT UNIQUE REFERENCES console_enrollment_intents(intent_id),
    oidc_issuer TEXT,
    oidc_subject TEXT,
    verified_email TEXT,
    state TEXT NOT NULL CHECK (state IN (
        'waiting_oidc','candidate_verified','waiting_approval','invitation_issued',
        'waiting_possession','enrolled','expired','failed','unknown'
    )),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_enrollment_candidate_state_idx
    ON console_enrollment_candidates(state,expires_at);

CREATE TABLE IF NOT EXISTS console_mutations (
    mutation_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES domains(domain_id),
    actor_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
    actor_harness_id TEXT NOT NULL REFERENCES harnesses(harness_id),
    mutation_kind TEXT NOT NULL CHECK (mutation_kind IN (
        'harness_revoke','credential_rotation_start','credential_recovery_start',
        'entitlement_issue','entitlement_revoke','incident_set'
    )),
    resource TEXT NOT NULL,
    request_json TEXT NOT NULL,
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    idempotency_key TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL CHECK (state IN (
        'prepared','waiting_approval','completed','rejected','failed','expired','canceled','unknown'
    )),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    policy_decision_id TEXT REFERENCES policy_decisions(decision_id),
    approval_request_id TEXT UNIQUE,
    approval_transaction_digest TEXT CHECK (
        approval_transaction_digest IS NULL OR length(approval_transaction_digest) = 64
    ),
    approval_receipt_digest TEXT CHECK (
        approval_receipt_digest IS NULL OR length(approval_receipt_digest) = 64
    ),
    possession_secret_encrypted TEXT,
    result_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    terminal_at INTEGER
);
CREATE INDEX IF NOT EXISTS console_mutation_state_idx
    ON console_mutations(domain_id,state,expires_at);
"""

__all__ = ["ADMIN_CONSOLE_SCHEMA", "ADMIN_CONSOLE_SCHEMA_VERSION"]
