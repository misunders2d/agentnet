from __future__ import annotations

from agentnet.storage.admin_console_schema import ADMIN_CONSOLE_SCHEMA_VERSION
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION, MIGRATIONS


def test_console_schema_is_current_and_additive(store) -> None:
    assert ADMIN_CONSOLE_SCHEMA_VERSION == CURRENT_SCHEMA_VERSION
    assert MIGRATIONS[-1].name == "private_administration_console"
    assert MIGRATIONS[-2].name == "persistent_same_principal_communication_scope"
    tables = {
        row["name"]
        for row in store.fetch_all("SELECT name FROM sqlite_master WHERE type='table'")
    }
    assert {
        "console_session_challenges",
        "console_oidc_transactions",
        "console_browser_sessions",
        "console_server_status",
        "console_enrollment_intents",
        "console_enrollment_candidates",
        "console_mutations",
    } <= tables
