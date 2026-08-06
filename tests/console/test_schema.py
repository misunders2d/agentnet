from __future__ import annotations

from agentnet.storage.admin_console_schema import ADMIN_CONSOLE_SCHEMA_VERSION
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS


def test_console_schema_is_current_and_additive(store) -> None:
    assert ADMIN_CONSOLE_SCHEMA_VERSION == 6
    assert CURRENT_SCHEMA_VERSION == 7
    assert MIGRATIONS[-1].version == CURRENT_SCHEMA_VERSION
    assert MIGRATIONS[-1].name == "communication_collaboration_release"
    tables = {
        row["name"]
        for row in store.fetch_all("SELECT name FROM sqlite_master WHERE type='table'")
    }
    assert {
        "console_session_challenges",
        "console_oidc_transactions",
        "console_browser_sessions",
        "console_mutation_authorizations",
        "console_server_status",
        "console_enrollment_intents",
        "console_enrollment_reviews",
        "console_enrollment_candidates",
        "console_mutations",
    } <= tables

    mutation_columns = {
        row["name"] for row in store.fetch_all("PRAGMA table_info(console_mutations)")
    }
    status_columns = {
        row["name"] for row in store.fetch_all("PRAGMA table_info(console_server_status)")
    }
    assert "approval_receipt_digest" in mutation_columns
    assert "revision" in status_columns
