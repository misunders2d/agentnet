from __future__ import annotations

import hashlib
import json

import pytest

from agentnet.errors import GateBlocked
from agentnet.operations.c0_credential_supersession import (
    C0CredentialSupersessionJournal,
    append_supersession,
    canonical_supersession_journal,
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
