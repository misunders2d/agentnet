from __future__ import annotations

from pathlib import Path

import pytest

from agentnet.approval.store import ApprovalStore, approval_user_handle
from agentnet.errors import GateBlocked
from agentnet.operations.canonical_owner_recovery import (
    CanonicalOwnerAdoptionRequest,
    adopt_canonical_approval_owner,
)
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import b64url_encode


NOW = 1_800_000_000
DOMAIN = "corp.example"
SOURCE = "sergey-owner"
VERIFIER_ID = "approval.corp.example"
TARGET = "6fac7b4c-de08-4192-9f6a-ef29b5ae6b0"
ISSUER = "https://accounts.example/oidc"
SUBJECT = "oidc-sergey-subject"
EMAIL = "sergey@corp.example"
RP_ID = "approval.corp.example"


def _store(tmp_path: Path) -> ApprovalStore:
    root = tmp_path / "approval"
    root.mkdir(mode=0o700)
    root.chmod(0o700)
    path = root / "approval.sqlite3"
    path.touch(mode=0o600)
    path.chmod(0o600)
    store = ApprovalStore(path, LocalEnvelopeCipher(b"r" * 32), initialize=True)
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO approval_owner_bindings(
                   binding_id,domain_id,approver_principal_id,oidc_issuer,oidc_subject,
                   verified_email,pin_source,status,pinned_at
               ) VALUES(?,?,?,?,?,?,'exact_subject','active',?)""",
            ("binding-owner", DOMAIN, SOURCE, ISSUER, SUBJECT, EMAIL, NOW - 10_000),
        )
        connection.execute(
            """INSERT INTO approval_webauthn_credentials(
                   credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                   credential_public_key_b64,sign_count,device_type,backed_up,status,created_at
               ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
            (
                "active-credential",
                SOURCE,
                DOMAIN,
                b64url_encode(
                    approval_user_handle(
                        verifier_id=VERIFIER_ID,
                        principal_id=SOURCE,
                        domain_id=DOMAIN,
                    )
                ),
                "active-public-key",
                17,
                "single_device",
                1,
                "active",
                NOW - 9_000,
            ),
        )
        connection.execute(
            """INSERT INTO approval_webauthn_credentials(
                   credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                   credential_public_key_b64,sign_count,device_type,backed_up,status,
                   created_at,revoked_at,revocation_reason
               ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                "historical-credential",
                SOURCE,
                DOMAIN,
                b64url_encode(b"historical-user-handle"),
                "historical-public-key",
                3,
                "single_device",
                0,
                "revoked",
                NOW - 20_000,
                NOW - 15_000,
                "rotated",
            ),
        )
        connection.execute(
            """INSERT INTO approval_requests(
                   request_id,approver_principal_id,domain_id,approval_purpose,capability_hash,
                   canonical_transaction_encrypted,transaction_digest,state,active_fingerprint,
                   created_at,expires_at,delivery_mode
               ) VALUES(?,?,?,?,?,?,?,'issued',NULL,?,?,'direct_receipt')""",
            (
                "historical-request",
                SOURCE,
                DOMAIN,
                "core.enrollment",
                "1" * 64,
                "encrypted-transaction",
                "2" * 64,
                NOW - 8_000,
                NOW - 7_000,
            ),
        )
        connection.execute(
            """INSERT INTO approval_issued_receipts(
                   request_id,credential_id_b64,authenticated_at,issued_at,
                   receipt_expires_at,receipt_encrypted,receipt_digest
               ) VALUES(?,?,?,?,?,?,?)""",
            (
                "historical-request",
                "historical-credential",
                NOW - 7_500,
                NOW - 7_499,
                NOW - 7_000,
                "encrypted-receipt",
                "3" * 64,
            ),
        )
        connection.execute(
            """INSERT INTO approval_audit(
                   action,request_id,approver_principal_id,domain_id,approval_purpose,
                   transaction_digest,occurred_at,outcome,detail_code
               ) VALUES(?,?,?,?,?,?,?,?,?)""",
            (
                "approval.issued",
                "historical-request",
                SOURCE,
                DOMAIN,
                "core.enrollment",
                "2" * 64,
                NOW - 7_499,
                "issued",
                "webauthn_verified",
            ),
        )
    return store


def _request(**updates: object) -> CanonicalOwnerAdoptionRequest:
    values: dict[str, object] = {
        "schema": "agentnet.canonical-owner-adoption.v1",
        "recovery_id": "93756ff6-6337-4ed1-9697-250b63fb68a2",
        "domain_id": DOMAIN,
        "source_principal_id": SOURCE,
        "target_principal_id": TARGET,
        "oidc_issuer": ISSUER,
        "oidc_subject": SUBJECT,
        "verified_email": EMAIL,
        "verifier_id": VERIFIER_ID,
        "approved_at": NOW - 100,
    }
    values.update(updates)
    return CanonicalOwnerAdoptionRequest.model_validate(values)

def test_adoption_moves_only_current_owner_authority_and_is_idempotent(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        before_active = dict(
            store.fetch_one(
                "SELECT * FROM approval_webauthn_credentials WHERE credential_id_b64=?",
                ("active-credential",),
            )
        )
        historical_request = dict(
            store.fetch_one("SELECT * FROM approval_requests WHERE request_id='historical-request'")
        )
        historical_audit = dict(
            store.fetch_one("SELECT * FROM approval_audit WHERE action='approval.issued'")
        )

        result = adopt_canonical_approval_owner(store, request=_request(), now=NOW)
        replay = adopt_canonical_approval_owner(store, request=_request(), now=NOW + 1)

        assert result == {
            "schema": "agentnet.canonical-owner-adoption-result.v1",
            "status": "adopted",
            "recovery_id": "93756ff6-6337-4ed1-9697-250b63fb68a2",
            "migrated_active_credentials": 1,
            "revoked_browser_sessions": 0,
            "canceled_registration_ceremonies": 0,
        }
        assert replay == {**result, "status": "already_exact", "migrated_active_credentials": 0}
        binding = store.fetch_one("SELECT * FROM approval_owner_bindings WHERE binding_id='binding-owner'")
        assert binding is not None
        assert binding["approver_principal_id"] == TARGET
        assert binding["oidc_issuer"] == ISSUER
        assert binding["oidc_subject"] == SUBJECT
        assert binding["verified_email"] == EMAIL

        active = dict(
            store.fetch_one(
                "SELECT * FROM approval_webauthn_credentials WHERE credential_id_b64=?",
                ("active-credential",),
            )
        )
        assert active == {
            **before_active,
            "approver_principal_id": TARGET,
            "user_handle_b64": b64url_encode(
                approval_user_handle(
                    verifier_id=VERIFIER_ID,
                    principal_id=TARGET,
                    domain_id=DOMAIN,
                )
            ),
        }
        historical_credential = store.fetch_one(
            "SELECT * FROM approval_webauthn_credentials WHERE credential_id_b64=?",
            ("historical-credential",),
        )
        assert historical_credential is not None
        assert historical_credential["approver_principal_id"] == SOURCE
        assert dict(
            store.fetch_one("SELECT * FROM approval_requests WHERE request_id='historical-request'")
        ) == historical_request
        assert dict(
            store.fetch_one("SELECT * FROM approval_audit WHERE action='approval.issued'")
        ) == historical_audit
        adoption_audits = store.fetch_all(
            "SELECT * FROM approval_audit WHERE action='owner.canonical_adoption'"
        )
        assert len(adoption_audits) == 1
        assert adoption_audits[0]["approver_principal_id"] == TARGET
        assert adoption_audits[0]["detail_code"] == "canonical_owner_adopted"
    finally:
        store.close()


def test_adoption_rejects_oidc_mismatch_without_mutation(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with pytest.raises(GateBlocked, match="source state does not match"):
            adopt_canonical_approval_owner(
                store,
                request=_request(oidc_subject="different-subject"),
                now=NOW,
            )
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings WHERE binding_id='binding-owner'"
        )[0] == SOURCE
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_webauthn_credentials WHERE credential_id_b64='active-credential'"
        )[0] == SOURCE
    finally:
        store.close()


def test_adoption_rejects_nonterminal_approval_request(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_requests(
                       request_id,approver_principal_id,domain_id,approval_purpose,capability_hash,
                       canonical_transaction_encrypted,transaction_digest,state,active_fingerprint,
                       created_at,expires_at,delivery_mode
                   ) VALUES(?,?,?,?,?,?,?,'pending',?,?,?,'direct_receipt')""",
                (
                    "pending-request",
                    SOURCE,
                    DOMAIN,
                    "communication.scope",
                    "4" * 64,
                    "encrypted-pending",
                    "5" * 64,
                    "6" * 64,
                    NOW - 10,
                    NOW + 300,
                ),
            )
        with pytest.raises(GateBlocked, match="nonterminal approval state"):
            adopt_canonical_approval_owner(store, request=_request(), now=NOW)
        assert store.fetch_one(
            "SELECT approver_principal_id FROM approval_owner_bindings WHERE binding_id='binding-owner'"
        )[0] == SOURCE
    finally:
        store.close()


def test_adoption_revokes_owner_sessions_and_cancels_pending_registration(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_browser_sessions(
                       session_hash,owner_binding_id,csrf_secret_encrypted,rp_id,public_origin,
                       verifier_id,created_at,authenticated_at,expires_at
                   ) VALUES(?,?,?,?,?,?,?,?,?)""",
                (
                    "7" * 64,
                    "binding-owner",
                    "encrypted-csrf",
                    RP_ID,
                    "https://approval.corp.example",
                    "approval.corp.example",
                    NOW - 50,
                    NOW - 50,
                    NOW + 300,
                ),
            )
            connection.execute(
                """INSERT INTO approval_registration_ceremonies(
                       ceremony_id,owner_binding_id,session_hash,challenge_encrypted,
                       challenge_hash,state,created_at,expires_at
                   ) VALUES(?,?,?,?,?,'pending',?,?)""",
                (
                    "pending-ceremony",
                    "binding-owner",
                    "7" * 64,
                    "encrypted-challenge",
                    "8" * 64,
                    NOW - 20,
                    NOW + 100,
                ),
            )
        result = adopt_canonical_approval_owner(store, request=_request(), now=NOW)
        assert result["revoked_browser_sessions"] == 1
        assert result["canceled_registration_ceremonies"] == 1
        session = store.fetch_one(
            "SELECT revoked_at,revocation_reason FROM approval_browser_sessions WHERE session_hash=?",
            ("7" * 64,),
        )
        assert tuple(session) == (NOW, "canonical_owner_adoption")
        assert store.fetch_one(
            "SELECT state FROM approval_registration_ceremonies WHERE ceremony_id='pending-ceremony'"
        )[0] == "canceled"
    finally:
        store.close()


def test_adoption_rejects_preexisting_target_authority(tmp_path: Path) -> None:
    store = _store(tmp_path)
    try:
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO approval_webauthn_credentials(
                       credential_id_b64,approver_principal_id,domain_id,user_handle_b64,
                       credential_public_key_b64,sign_count,device_type,backed_up,status,created_at
                   ) VALUES(?,?,?,?,?,?,?,?,?,?)""",
                (
                    "target-credential",
                    TARGET,
                    DOMAIN,
                    b64url_encode(b"target-user-handle"),
                    "target-public-key",
                    0,
                    "single_device",
                    0,
                    "active",
                    NOW - 1,
                ),
            )
        with pytest.raises(GateBlocked, match="target authority already exists"):
            adopt_canonical_approval_owner(store, request=_request(), now=NOW)
    finally:
        store.close()
