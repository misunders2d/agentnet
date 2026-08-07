from __future__ import annotations

import hashlib
import json
from pathlib import Path
from types import SimpleNamespace
from typing import Any, cast

import pytest

from agentnet.core.app import CommunicationCore
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.storage.sqlite import SQLiteStore
from agentnet.errors import GateBlocked
from agentnet.operations.c0_credential_supersession import (
    C0CredentialSupersessionJournal,
    append_supersession,
    canonical_supersession_journal,
    completed_c0_terminal_credential,
    load_audited_supersession_journal,
    load_supersession_journal,
    verify_supersession_audit,
)


DOMAIN_ID = "bezosapp.uk"
PRINCIPAL_ID = "principal-1"
HARNESS_ID = "harness-1"
KEY_ID = "key-thumbprint-1"
REQUEST_ID_1 = "00000000-0000-4000-8000-000000000001"
REQUEST_ID_2 = "00000000-0000-4000-8000-000000000002"


def _terminal(*, credential_id: str = "credential-1") -> bytes:
    return (
        json.dumps(
            {
                "schema": "agentnet.c0-pilot-responder.terminal.v1",
                "status": "COMPLETED_C0_ROUND_TRIP",
                "domain_id": DOMAIN_ID,
                "harness_id": HARNESS_ID,
                "credential_id": credential_id,
            },
            sort_keys=True,
        ).encode("utf-8")
        + b"\n"
    )


def _append(
    *,
    existing: C0CredentialSupersessionJournal | None = None,
    terminal_raw: bytes | None = None,
    request_id: str = REQUEST_ID_1,
    previous_credential_id: str = "credential-1",
    credential_id: str = "credential-2",
    previous_credential_epoch: int = 1,
    terminal_credential_epoch: int = 1,
    credential_epoch: int = 2,
    prior_journal_sha256: str | None = None,
    audit_record_hash: str = "c" * 64,
) -> C0CredentialSupersessionJournal:
    return append_supersession(
        terminal_raw=terminal_raw or _terminal(),
        existing=existing,
        domain_id=DOMAIN_ID,
        principal_id=PRINCIPAL_ID,
        terminal_credential_epoch=terminal_credential_epoch,
        harness_id=HARNESS_ID,
        request_id=request_id,
        transaction_sha256="a" * 64,
        approval_receipt_id=f"receipt-{credential_epoch}",
        approval_receipt_sha256="b" * 64,
        audit_record_hash=audit_record_hash,
        prior_journal_sha256=prior_journal_sha256,
        previous_credential_id=previous_credential_id,
        credential_id=credential_id,
        previous_credential_epoch=previous_credential_epoch,
        credential_epoch=credential_epoch,
        key_id=KEY_ID,
        not_before=100 * credential_epoch,
        expires_at=100 * credential_epoch + 3_600,
    )


def test_first_supersession_preserves_terminal_origin() -> None:
    terminal = _terminal()

    journal = _append(terminal_raw=terminal)

    assert journal.terminal_sha256 == hashlib.sha256(terminal).hexdigest()
    assert journal.terminal_credential_id == "credential-1"
    assert journal.terminal_credential_epoch == 1
    assert journal.current_credential == ("credential-2", 2)
    assert journal.entries[0].previous_entry_sha256 == journal.terminal_sha256
    assert terminal == _terminal()


def test_committed_tail_replays_idempotently_but_conflicts_fail_closed() -> None:
    first = _append()

    assert _append(existing=first) == first
    with pytest.raises(GateBlocked, match="replay conflicts"):
        _append(existing=first, audit_record_hash="d" * 64)


def test_second_supersession_extends_exact_chain() -> None:
    first = _append()
    first_raw = canonical_supersession_journal(first)

    second = _append(
        existing=first,
        request_id=REQUEST_ID_2,
        previous_credential_id="credential-2",
        credential_id="credential-3",
        previous_credential_epoch=2,
        credential_epoch=3,
        prior_journal_sha256=hashlib.sha256(first_raw).hexdigest(),
        audit_record_hash="d" * 64,
    )

    assert len(second.entries) == 2
    assert second.entries[1].previous_entry_sha256 == second.entries[0].entry_sha256
    assert second.current_credential == ("credential-3", 3)
    assert load_supersession_journal(
        canonical_supersession_journal(second),
        terminal_raw=_terminal(),
        domain_id=DOMAIN_ID,
        principal_id=PRINCIPAL_ID,
        harness_id=HARNESS_ID,
    ) == second


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("terminal_sha256", "f" * 64),
        ("terminal_credential_id", "relabeled-credential"),
        ("domain_id", "foreign.example"),
        ("principal_id", "foreign-principal"),
        ("harness_id", "foreign-harness"),
    ],
)
def test_journal_rejects_origin_relabeling(field: str, value: str) -> None:
    value_dict = _append().model_dump(mode="json", by_alias=True)
    value_dict[field] = value

    with pytest.raises(GateBlocked, match="supersession journal"):
        load_supersession_journal(
            json.dumps(value_dict).encode(),
            terminal_raw=_terminal(),
            domain_id=DOMAIN_ID,
            principal_id=PRINCIPAL_ID,
            harness_id=HARNESS_ID,
        )


def test_journal_rejects_unknown_fields() -> None:
    value = _append().model_dump(mode="json", by_alias=True)
    value["authority"] = "granted"

    with pytest.raises(GateBlocked, match="supersession journal"):
        load_supersession_journal(
            json.dumps(value).encode(),
            terminal_raw=_terminal(),
            domain_id=DOMAIN_ID,
            principal_id=PRINCIPAL_ID,
            harness_id=HARNESS_ID,
        )


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("credential_epoch", 4),
        ("key_id", "other-key"),
        ("previous_entry_sha256", "e" * 64),
        ("expires_at", 200),
    ],
)
def test_journal_rejects_broken_transition(field: str, value: object) -> None:
    journal = _append()
    raw = journal.model_dump(mode="json", by_alias=True)
    raw["entries"][0][field] = value
    value = raw

    with pytest.raises(GateBlocked, match="supersession journal"):
        load_supersession_journal(
            json.dumps(value, default=str).encode(),
            terminal_raw=_terminal(),
            domain_id=DOMAIN_ID,
            principal_id=PRINCIPAL_ID,
            harness_id=HARNESS_ID,
        )


def test_first_supersession_rejects_epoch_relabeling() -> None:
    with pytest.raises(GateBlocked, match="terminal credential epoch"):
        _append(
            terminal_credential_epoch=1,
            previous_credential_epoch=99,
            credential_epoch=100,
        )


class _AuditStore:
    def __init__(self, rows: list[dict[str, object]], *, valid: bool = True) -> None:
        self.rows = rows
        self.valid = valid

    def verify_audit_chain(self) -> tuple[bool, int]:
        return self.valid, len(self.rows)

    def fetch_all(self, _query: str, _parameters: tuple[object, ...] = ()) -> list[dict[str, object]]:
        return self.rows

    def fetch_one(
        self,
        _query: str,
        _parameters: tuple[object, ...] = (),
    ) -> dict[str, object] | None:
        return None


def _audit_row(journal: C0CredentialSupersessionJournal) -> dict[str, object]:
    entry = journal.entries[0]
    return {
        "record_hash": entry.audit_record_hash,
        "record_json": json.dumps(
            {
                "action": "credential.managed_server_reauthorized",
                "request_id": entry.request_id,
                "domain_id": journal.domain_id,
                "principal_id": journal.principal_id,
                "harness_id": journal.harness_id,
                "old_credential_id": entry.previous_credential_id,
                "new_credential_id": entry.credential_id,
                "key_id": entry.key_id,
                "previous_credential_epoch": entry.previous_credential_epoch,
                "new_credential_epoch": entry.credential_epoch,
                "not_before": entry.not_before,
                "expires_at": entry.expires_at,
                "approval_receipt_id": entry.approval_receipt_id,
                "approval_receipt_digest": entry.approval_receipt_sha256,
                "transaction_digest": entry.transaction_sha256,
                "c0_terminal_sha256": journal.terminal_sha256,
                "terminal_credential_epoch": journal.terminal_credential_epoch,
                "c0_supersession_sha256": entry.prior_journal_sha256,
            },
            sort_keys=True,
            separators=(",", ":"),
        ),
    }


def test_server_setup_postgres_audit_evidence_validates_chain(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import psycopg
    from agentnet.operations import server_setup

    provisional = _append(audit_record_hash="0" * 64)
    record_json = _audit_row(provisional)["record_json"]
    assert isinstance(record_json, str)
    occurred_at = 250
    record_hash = hashlib.sha256(
        ("0" * 64).encode("ascii")
        + b"\x00"
        + str(occurred_at).encode("ascii")
        + b"\x00"
        + record_json.encode("utf-8")
    ).hexdigest()
    journal = _append(audit_record_hash=record_hash)
    journal_raw = canonical_supersession_journal(journal)
    audit_row = {
        "sequence": 1,
        "occurred_at": occurred_at,
        "record_json": record_json,
        "previous_hash": "0" * 64,
        "record_hash": record_hash,
    }
    connections: list[Any] = []

    class Cursor:
        def __init__(self, rows: list[dict[str, object]]) -> None:
            self.rows = rows

        def fetchall(self) -> list[dict[str, object]]:
            return self.rows

        def fetchone(self) -> dict[str, object] | None:
            return self.rows[0] if self.rows else None

    class Connection:
        def __init__(self) -> None:
            self.closed = False
            self.queries: list[str] = []

        def execute(
            self,
            query: str,
            _parameters: tuple[object, ...] = (),
        ) -> Cursor:
            self.queries.append(query)
            return Cursor([] if query.startswith("SET ") else [dict(audit_row)])

        def close(self) -> None:
            self.closed = True

    def connect(*_args: object, **_kwargs: object) -> Connection:
        connection = Connection()
        connections.append(connection)
        return connection

    monkeypatch.setattr(psycopg, "connect", connect)
    evidence = server_setup._postgres_supersession_audit_evidence(
        "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
        journal_raw=journal_raw,
        terminal_raw=_terminal(),
        domain_id=DOMAIN_ID,
        principal_id=PRINCIPAL_ID,
        harness_id=HARNESS_ID,
    )

    assert evidence == {
        "ready": True,
        "journal_sha256": hashlib.sha256(journal_raw).hexdigest(),
        "transition_count": 1,
        "audit_records_verified": 1,
        "credential_id": "credential-2",
        "credential_epoch": 2,
    }
    assert connections[0].queries[0] == "SET default_transaction_read_only = on"
    assert connections[0].closed is True

    audit_row["record_json"] = "{}"
    with pytest.raises(GateBlocked, match="audit chain is invalid"):
        server_setup._postgres_supersession_audit_evidence(
            "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
            journal_raw=journal_raw,
            terminal_raw=_terminal(),
            domain_id=DOMAIN_ID,
            principal_id=PRINCIPAL_ID,
            harness_id=HARNESS_ID,
        )
    assert connections[1].closed is True


def test_audit_verifier_matches_every_authoritative_transition() -> None:
    journal = _append()

    evidence = verify_supersession_audit(_AuditStore([_audit_row(journal)]), journal)

    assert evidence == {
        "journal_sha256": hashlib.sha256(canonical_supersession_journal(journal)).hexdigest(),
        "transition_count": 1,
        "audit_records_verified": 1,
        "credential_id": "credential-2",
        "credential_epoch": 2,
    }


def test_audit_verifier_rejects_internally_valid_uncommitted_transition() -> None:
    journal = _append()

    with pytest.raises(GateBlocked, match="authoritative audit"):
        verify_supersession_audit(_AuditStore([]), journal)


def test_audit_verifier_rejects_conflicting_authoritative_transition() -> None:
    journal = _append()
    row = _audit_row(journal)
    record = json.loads(str(row["record_json"]))
    record["new_credential_id"] = "other-credential"
    row["record_json"] = json.dumps(record, sort_keys=True, separators=(",", ":"))

    with pytest.raises(GateBlocked, match="authoritative audit"):
        verify_supersession_audit(_AuditStore([row]), journal)


def test_audit_verifier_rejects_invalid_audit_chain() -> None:
    journal = _append()

    with pytest.raises(GateBlocked, match="audit chain"):
        verify_supersession_audit(_AuditStore([_audit_row(journal)], valid=False), journal)


class _TerminalAuditStore(_AuditStore):
    def __init__(
        self,
        rows: list[dict[str, object]],
        *,
        terminal_credential_id: str = "credential-1",
        terminal_credential_epoch: int = 1,
    ) -> None:
        super().__init__(rows)
        self.terminal_credential_id = terminal_credential_id
        self.terminal_credential_epoch = terminal_credential_epoch

    def fetch_one(
        self,
        query: str,
        _parameters: tuple[object, ...] = (),
    ) -> dict[str, object] | None:
        if "FROM c0_pilot_attempts" in query:
            return {
                "owner_harness_id": HARNESS_ID,
                "fresh_harness_id": "fresh-harness",
                "owner_credential_epoch": self.terminal_credential_epoch,
                "fresh_credential_epoch": 1,
            }
        if "FROM credentials" in query:
            return {"credential_id": self.terminal_credential_id}
        raise AssertionError(query)


class _CanonicalCredentialStore(_TerminalAuditStore):
    def __init__(self, store: SQLiteStore) -> None:
        super().__init__([])
        self.store = store

    def fetch_one(
        self,
        query: str,
        parameters: tuple[object, ...] = (),
    ) -> dict[str, object] | None:
        if "FROM c0_pilot_attempts" in query:
            return super().fetch_one(query, parameters)
        return cast(dict[str, object] | None, self.store.fetch_one(query, parameters))


def _core(store: _AuditStore, data_dir: Path) -> Any:
    core = cast(Any, CommunicationCore.__new__(CommunicationCore))
    core.store = store
    core.config = SimpleNamespace(
        data_dir=data_dir,
        domain_id=DOMAIN_ID,
        enrolled_harness_id=HARNESS_ID,
    )
    core._verified_supersession_binding = None
    core._verified_supersession_evidence = None
    return core


def test_completed_c0_terminal_credential_resolves_exact_epoch_binding() -> None:
    store = _TerminalAuditStore([])

    assert completed_c0_terminal_credential(
        store,
        domain_id=DOMAIN_ID,
        principal_id=PRINCIPAL_ID,
        harness_id=HARNESS_ID,
    ) == ("credential-1", 1)


def test_completed_c0_terminal_credential_uses_canonical_identity_schema(tmp_path: Path) -> None:
    sqlite = SQLiteStore(tmp_path / "c0-terminal.sqlite3", LocalEnvelopeCipher(b"c" * 32))
    try:
        with sqlite.transaction() as connection:
            connection.execute(
                "INSERT INTO domains(domain_id,status,policy_revision,revocation_epoch,created_at) "
                "VALUES(?, 'active', 1, 1, 1)",
                (DOMAIN_ID,),
            )
            connection.execute(
                """INSERT INTO principals(
                       principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at)
                   VALUES(?,?, 'https://idp.example', 'subject-1', 'owner@example.test', 'active', 1)""",
                (PRINCIPAL_ID, DOMAIN_ID),
            )
            connection.execute(
                """INSERT INTO harnesses(
                       harness_id,domain_id,principal_id,guest_id,kind,display_name,status,
                       binding_assurance,capabilities_json,credential_epoch,created_at)
                   VALUES(?,?,?,NULL,'pi','Owner','active','test','{}',1,1)""",
                (HARNESS_ID, DOMAIN_ID, PRINCIPAL_ID),
            )
            connection.execute(
                """INSERT INTO credentials(
                       credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at)
                   VALUES('credential-1',?,?,'synthetic-public-key','active',1,1,3600)""",
                (HARNESS_ID, KEY_ID),
            )

        assert completed_c0_terminal_credential(
            _CanonicalCredentialStore(sqlite),
            domain_id=DOMAIN_ID,
            principal_id=PRINCIPAL_ID,
            harness_id=HARNESS_ID,
        ) == ("credential-1", 1)
    finally:
        sqlite.close()


def test_core_allows_unreplaced_terminal_credential_without_journal(tmp_path: Path) -> None:
    core = _core(_TerminalAuditStore([]), tmp_path)

    core._require_managed_credential_supersession(
        principal_id=PRINCIPAL_ID,
        credential_id="credential-1",
        credential_epoch=1,
        key_id=KEY_ID,
    )


def test_core_rejects_replacement_without_journal(tmp_path: Path) -> None:
    core = _core(_TerminalAuditStore([]), tmp_path)

    with pytest.raises(GateBlocked, match="lacks supersession provenance"):
        core._require_managed_credential_supersession(
            principal_id=PRINCIPAL_ID,
            credential_id="credential-2",
            credential_epoch=2,
            key_id=KEY_ID,
        )


def test_core_accepts_current_audited_supersession_journal(tmp_path: Path) -> None:
    journal = _append()
    journal_path = tmp_path / "credential-supersessions.json"
    journal_path.write_bytes(canonical_supersession_journal(journal))
    journal_path.chmod(0o600)
    store = _TerminalAuditStore([_audit_row(journal)])
    core = _core(store, tmp_path)

    core._require_managed_credential_supersession(
        principal_id=PRINCIPAL_ID,
        credential_id="credential-2",
        credential_epoch=2,
        key_id=KEY_ID,
    )
    assert core._verified_supersession_binding == ("credential-2", 2, KEY_ID)


def test_core_rejects_tampered_supersession_journal(tmp_path: Path) -> None:
    journal = _append()
    value = journal.model_dump(mode="json", by_alias=True)
    value["entries"][0]["entry_sha256"] = "f" * 64
    journal_path = tmp_path / "credential-supersessions.json"
    journal_path.write_text(json.dumps(value), encoding="utf-8")
    journal_path.chmod(0o600)
    core = _core(_TerminalAuditStore([_audit_row(journal)]), tmp_path)

    with pytest.raises(GateBlocked, match="supersession journal"):
        core._require_managed_credential_supersession(
            principal_id=PRINCIPAL_ID,
            credential_id="credential-2",
            credential_epoch=2,
            key_id=KEY_ID,
        )


def test_audited_loader_rejects_noncanonical_journal() -> None:
    journal = _append()
    noncanonical = json.dumps(
        journal.model_dump(mode="json", by_alias=True),
        indent=2,
    ).encode()

    with pytest.raises(GateBlocked, match="deployment binding"):
        load_audited_supersession_journal(
            noncanonical,
            _TerminalAuditStore([_audit_row(journal)]),
            domain_id=DOMAIN_ID,
            principal_id=PRINCIPAL_ID,
            harness_id=HARNESS_ID,
        )
