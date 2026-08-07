"""Immutable C0 origin plus approved managed-credential supersession provenance."""

from __future__ import annotations

import hashlib
import json
from typing import Any, Literal, Protocol

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError

from agentnet.errors import GateBlocked
from agentnet.security.signatures import canonical_json


_HEX_DIGEST = r"^[a-f0-9]{64}$"
_BLOCKER = "c0_credential_supersession"


class _C0TerminalMarker(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal["agentnet.c0-pilot-responder.terminal.v1"] = Field(alias="schema")
    status: Literal["COMPLETED_C0_ROUND_TRIP"]
    domain_id: str
    harness_id: str
    credential_id: str


class C0CredentialSupersessionEntry(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    request_id: str = Field(
        pattern=r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
    )
    transaction_sha256: str = Field(pattern=_HEX_DIGEST)
    approval_receipt_id: str = Field(min_length=1, max_length=256)
    approval_receipt_sha256: str = Field(pattern=_HEX_DIGEST)
    audit_record_hash: str = Field(pattern=_HEX_DIGEST)
    prior_journal_sha256: str | None = Field(default=None, pattern=_HEX_DIGEST)
    previous_credential_id: str = Field(min_length=1, max_length=256)
    credential_id: str = Field(min_length=1, max_length=256)
    previous_credential_epoch: int = Field(ge=1)
    credential_epoch: int = Field(ge=2)
    key_id: str = Field(min_length=16, max_length=256)
    not_before: int = Field(ge=0)
    expires_at: int = Field(ge=1)
    previous_entry_sha256: str = Field(pattern=_HEX_DIGEST)
    entry_sha256: str = Field(pattern=_HEX_DIGEST)


class C0CredentialSupersessionJournal(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    schema_version: Literal[
        "agentnet.c0-pilot-responder.credential-supersessions.v1"
    ] = Field(
        default="agentnet.c0-pilot-responder.credential-supersessions.v1",
        alias="schema",
    )
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    principal_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    terminal_sha256: str = Field(pattern=_HEX_DIGEST)
    terminal_credential_id: str = Field(min_length=1, max_length=256)
    terminal_credential_epoch: int = Field(ge=1)
    entries: tuple[C0CredentialSupersessionEntry, ...] = Field(min_length=1)

    @property
    def current_credential(self) -> tuple[str, int]:
        entry = self.entries[-1]
        return entry.credential_id, entry.credential_epoch


class _AuditStore(Protocol):
    def verify_audit_chain(self) -> tuple[bool, int]: ...

    def fetch_all(
        self,
        query: str,
        parameters: tuple[object, ...] = (),
    ) -> list[Any]: ...

    def fetch_one(
        self,
        query: str,
        parameters: tuple[object, ...] = (),
    ) -> Any | None: ...


def _blocked(message: str) -> GateBlocked:
    return GateBlocked(_BLOCKER, message)


def _terminal(raw: bytes) -> _C0TerminalMarker:
    try:
        return _C0TerminalMarker.model_validate_json(raw)
    except PydanticValidationError as exc:
        raise _blocked("C0 terminal evidence is invalid for credential supersession") from exc


def _entry_digest(value: dict[str, object]) -> str:
    digest_value = dict(value)
    digest_value.pop("entry_sha256", None)
    return hashlib.sha256(canonical_json(digest_value)).hexdigest()


def canonical_supersession_journal(journal: C0CredentialSupersessionJournal) -> bytes:
    return canonical_json(journal.model_dump(mode="json", by_alias=True)) + b"\n"


def _validate_internal_chain(journal: C0CredentialSupersessionJournal) -> None:
    previous_id = journal.terminal_credential_id
    previous_epoch = journal.terminal_credential_epoch
    previous_entry_sha256 = journal.terminal_sha256
    previous_key_id: str | None = None
    for index, entry in enumerate(journal.entries):
        if (
            entry.previous_entry_sha256 != previous_entry_sha256
            or entry.previous_credential_id != previous_id
            or entry.credential_id == entry.previous_credential_id
            or entry.credential_epoch != entry.previous_credential_epoch + 1
            or entry.expires_at <= entry.not_before
            or entry.entry_sha256
            != _entry_digest(entry.model_dump(mode="json", by_alias=True))
            or entry.previous_credential_epoch != previous_epoch
        ):
            raise _blocked("C0 credential supersession journal chain is invalid")
        if index == 0:
            if entry.prior_journal_sha256 is not None:
                raise _blocked("C0 credential supersession journal first link is invalid")
        elif previous_key_id != entry.key_id:
            raise _blocked("C0 credential supersession journal continuity is invalid")
        previous_id = entry.credential_id
        previous_epoch = entry.credential_epoch
        previous_key_id = entry.key_id
        previous_entry_sha256 = entry.entry_sha256


def _validate_chain(
    journal: C0CredentialSupersessionJournal,
    *,
    terminal: _C0TerminalMarker,
    terminal_raw: bytes,
    domain_id: str,
    principal_id: str,
    harness_id: str,
) -> None:
    terminal_sha256 = hashlib.sha256(terminal_raw).hexdigest()
    if (
        journal.domain_id != domain_id
        or journal.principal_id != principal_id
        or journal.harness_id != harness_id
        or terminal.domain_id != domain_id
        or terminal.harness_id != harness_id
        or journal.terminal_sha256 != terminal_sha256
        or journal.terminal_credential_id != terminal.credential_id
    ):
        raise _blocked("C0 credential supersession journal origin is invalid")

    _validate_internal_chain(journal)


def load_supersession_journal(
    raw: bytes,
    *,
    terminal_raw: bytes,
    domain_id: str,
    principal_id: str,
    harness_id: str,
) -> C0CredentialSupersessionJournal:
    terminal = _terminal(terminal_raw)
    try:
        journal = C0CredentialSupersessionJournal.model_validate_json(raw)
    except PydanticValidationError as exc:
        raise _blocked("C0 credential supersession journal is invalid") from exc
    _validate_chain(
        journal,
        terminal=terminal,
        terminal_raw=terminal_raw,
        domain_id=domain_id,
        principal_id=principal_id,
        harness_id=harness_id,
    )
    return journal


def load_audited_supersession_journal(
    raw: bytes,
    store: _AuditStore,
    *,
    domain_id: str,
    principal_id: str,
    harness_id: str,
) -> C0CredentialSupersessionJournal:
    """Validate a Core-owned canonical journal against authoritative audit history."""

    try:
        journal = C0CredentialSupersessionJournal.model_validate_json(raw)
    except PydanticValidationError as exc:
        raise _blocked("C0 credential supersession journal is invalid") from exc
    if (
        journal.domain_id != domain_id
        or journal.principal_id != principal_id
        or journal.harness_id != harness_id
        or canonical_supersession_journal(journal) != raw
    ):
        raise _blocked("C0 credential supersession journal deployment binding is invalid")
    _validate_internal_chain(journal)
    verify_supersession_audit(store, journal)
    return journal


def completed_c0_terminal_credential(
    store: _AuditStore,
    *,
    domain_id: str,
    principal_id: str,
    harness_id: str,
) -> tuple[str, int] | None:
    """Resolve the exact credential proven by the latest completed C0 attempt."""

    row = store.fetch_one(
        """SELECT g.owner_harness_id,g.fresh_harness_id,
                  g.owner_credential_epoch,g.fresh_credential_epoch
             FROM c0_pilot_attempts a
             JOIN c0_plan_guards g ON g.guard_id=a.guard_id
            WHERE a.state='communication_revoked'
              AND a.sanitized_result='COMPLETED_C0_ROUND_TRIP'
              AND g.domain_id=? AND g.principal_id=?
              AND (g.owner_harness_id=? OR g.fresh_harness_id=?)
            ORDER BY a.terminal_at DESC,a.attempt_id DESC LIMIT 1""",
        (domain_id, principal_id, harness_id, harness_id),
    )
    if row is None:
        return None
    epoch = int(
        row["owner_credential_epoch"]
        if row["owner_harness_id"] == harness_id
        else row["fresh_credential_epoch"]
    )
    credential = store.fetch_one(
        """SELECT credential_id FROM credentials
            WHERE domain_id=? AND principal_id=? AND harness_id=? AND epoch=?""",
        (domain_id, principal_id, harness_id, epoch),
    )
    if credential is None:
        raise _blocked("completed C0 terminal credential is unavailable")
    return str(credential["credential_id"]), epoch


def verify_recovery_provenance(
    store: _AuditStore,
    *,
    terminal_raw: bytes,
    journal_raw: bytes | None,
    domain_id: str,
    principal_id: str,
    harness_id: str,
    expected_previous_credential_id: str,
    expected_previous_credential_epoch: int,
    c0_terminal_credential_epoch: int,
    c0_terminal_sha256: str,
    prior_journal_sha256: str | None,
) -> dict[str, object]:
    """Fail closed unless a recovery request names the exact authoritative C0 chain."""

    terminal = _terminal(terminal_raw)
    terminal_digest = hashlib.sha256(terminal_raw).hexdigest()
    if (
        terminal.domain_id != domain_id
        or terminal.harness_id != harness_id
        or terminal_digest != c0_terminal_sha256
    ):
        raise _blocked("C0 recovery terminal provenance is invalid")
    if prior_journal_sha256 is None:
        if journal_raw is not None:
            raise _blocked("C0 recovery unexpectedly supplied a supersession journal")
        if (
            terminal.credential_id != expected_previous_credential_id
            or c0_terminal_credential_epoch != expected_previous_credential_epoch
        ):
            raise _blocked("C0 recovery predecessor does not match the terminal origin")
        return {
            "terminal_sha256": terminal_digest,
            "terminal_credential_id": terminal.credential_id,
            "terminal_credential_epoch": c0_terminal_credential_epoch,
            "prior_journal_sha256": None,
            "transition_count": 0,
            "audit_records_verified": 0,
        }
    if journal_raw is None:
        raise _blocked("C0 recovery supersession journal is missing")
    journal = load_supersession_journal(
        journal_raw,
        terminal_raw=terminal_raw,
        domain_id=domain_id,
        principal_id=principal_id,
        harness_id=harness_id,
    )
    if canonical_supersession_journal(journal) != journal_raw:
        raise _blocked("C0 recovery supersession journal is not canonical")
    journal_digest = hashlib.sha256(journal_raw).hexdigest()
    if (
        journal_digest != prior_journal_sha256
        or journal.terminal_credential_epoch != c0_terminal_credential_epoch
        or journal.current_credential
        != (expected_previous_credential_id, expected_previous_credential_epoch)
    ):
        raise _blocked("C0 recovery supersession provenance does not match the request")
    verified = verify_supersession_audit(store, journal)
    return {
        "terminal_sha256": terminal_digest,
        "terminal_credential_id": terminal.credential_id,
        "terminal_credential_epoch": journal.terminal_credential_epoch,
        "prior_journal_sha256": journal_digest,
        "transition_count": verified["transition_count"],
        "audit_records_verified": verified["audit_records_verified"],
    }


def append_supersession(
    *,
    terminal_raw: bytes,
    existing: C0CredentialSupersessionJournal | None,
    domain_id: str,
    principal_id: str,
    terminal_credential_epoch: int,
    harness_id: str,
    request_id: str,
    transaction_sha256: str,
    approval_receipt_id: str,
    approval_receipt_sha256: str,
    audit_record_hash: str,
    prior_journal_sha256: str | None,
    previous_credential_id: str,
    credential_id: str,
    previous_credential_epoch: int,
    credential_epoch: int,
    key_id: str,
    not_before: int,
    expires_at: int,
) -> C0CredentialSupersessionJournal:
    terminal = _terminal(terminal_raw)
    if terminal.domain_id != domain_id or terminal.harness_id != harness_id:
        raise _blocked("C0 terminal evidence crossed the managed actor")

    terminal_sha256 = hashlib.sha256(terminal_raw).hexdigest()
    if existing is None:
        entries: tuple[C0CredentialSupersessionEntry, ...] = ()
        expected_previous_id = terminal.credential_id
        expected_previous_epoch = terminal_credential_epoch
        previous_entry_sha256 = terminal_sha256
        if prior_journal_sha256 is not None:
            raise _blocked("first credential supersession named a prior journal")
    else:
        _validate_chain(
            existing,
            terminal=terminal,
            terminal_raw=terminal_raw,
            domain_id=domain_id,
            principal_id=principal_id,
            harness_id=harness_id,
        )
        if existing.terminal_credential_epoch != terminal_credential_epoch:
            raise _blocked("credential supersession terminal credential epoch changed")
        if existing.entries[-1].request_id == request_id:
            replay_value: dict[str, object] = {
                "request_id": request_id,
                "transaction_sha256": transaction_sha256,
                "approval_receipt_id": approval_receipt_id,
                "approval_receipt_sha256": approval_receipt_sha256,
                "audit_record_hash": audit_record_hash,
                "prior_journal_sha256": prior_journal_sha256,
                "previous_credential_id": previous_credential_id,
                "credential_id": credential_id,
                "previous_credential_epoch": previous_credential_epoch,
                "credential_epoch": credential_epoch,
                "key_id": key_id,
                "not_before": not_before,
                "expires_at": expires_at,
                "previous_entry_sha256": existing.entries[-1].previous_entry_sha256,
            }
            replay_value["entry_sha256"] = _entry_digest(replay_value)
            try:
                replay = C0CredentialSupersessionEntry.model_validate(replay_value)
            except PydanticValidationError as exc:
                raise _blocked("credential supersession replay is invalid") from exc
            if replay != existing.entries[-1]:
                raise _blocked("credential supersession replay conflicts with committed transition")
            return existing
        if any(entry.request_id == request_id for entry in existing.entries):
            raise _blocked("credential supersession request replayed out of order")
        expected_prior = hashlib.sha256(canonical_supersession_journal(existing)).hexdigest()
        if prior_journal_sha256 != expected_prior:
            raise _blocked("credential supersession prior journal changed")
        entries = existing.entries
        expected_previous_id = entries[-1].credential_id
        expected_previous_epoch = entries[-1].credential_epoch
        previous_entry_sha256 = entries[-1].entry_sha256
    if previous_credential_id != expected_previous_id:
        raise _blocked("credential supersession predecessor is invalid")
    if previous_credential_epoch != expected_previous_epoch:
        raise _blocked("credential supersession terminal credential epoch is invalid")

    entry_value: dict[str, object] = {
        "request_id": request_id,
        "transaction_sha256": transaction_sha256,
        "approval_receipt_id": approval_receipt_id,
        "approval_receipt_sha256": approval_receipt_sha256,
        "audit_record_hash": audit_record_hash,
        "prior_journal_sha256": prior_journal_sha256,
        "previous_credential_id": previous_credential_id,
        "credential_id": credential_id,
        "previous_credential_epoch": previous_credential_epoch,
        "credential_epoch": credential_epoch,
        "key_id": key_id,
        "not_before": not_before,
        "expires_at": expires_at,
        "previous_entry_sha256": previous_entry_sha256,
    }
    entry_value["entry_sha256"] = _entry_digest(entry_value)
    try:
        entry = C0CredentialSupersessionEntry.model_validate(entry_value)
        journal = C0CredentialSupersessionJournal.model_validate(
            {
                "schema": "agentnet.c0-pilot-responder.credential-supersessions.v1",
                "domain_id": domain_id,
                "principal_id": principal_id,
                "harness_id": harness_id,
                "terminal_sha256": terminal_sha256,
                "terminal_credential_id": terminal.credential_id,
                "terminal_credential_epoch": terminal_credential_epoch,
                "entries": (*entries, entry),
            }
        )
    except PydanticValidationError as exc:
        raise _blocked("credential supersession transition is invalid") from exc
    _validate_chain(
        journal,
        terminal=terminal,
        terminal_raw=terminal_raw,
        domain_id=domain_id,
        principal_id=principal_id,
        harness_id=harness_id,
    )
    return journal


def _audit_record(entry: C0CredentialSupersessionEntry, journal: C0CredentialSupersessionJournal) -> dict[str, object]:
    return {
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
    }


def verify_supersession_audit(
    store: _AuditStore,
    journal: C0CredentialSupersessionJournal,
) -> dict[str, object]:
    valid, _ = store.verify_audit_chain()
    if not valid:
        raise _blocked("PostgreSQL audit chain is invalid")
    rows = store.fetch_all("SELECT record_json,record_hash FROM audit_log ORDER BY sequence")
    by_hash: dict[str, dict[str, object]] = {}
    for row in rows:
        record_hash = row["record_hash"]
        record_json = row["record_json"]
        if not isinstance(record_hash, str) or not isinstance(record_json, str):
            raise _blocked("authoritative audit record is invalid")
        try:
            record = json.loads(record_json)
        except json.JSONDecodeError as exc:
            raise _blocked("authoritative audit record is invalid") from exc
        if not isinstance(record, dict) or record_hash in by_hash:
            raise _blocked("authoritative audit record is ambiguous")
        by_hash[record_hash] = record

    for entry in journal.entries:
        if by_hash.get(entry.audit_record_hash) != _audit_record(entry, journal):
            raise _blocked("credential supersession lacks its authoritative audit record")

    credential_id, credential_epoch = journal.current_credential
    return {
        "journal_sha256": hashlib.sha256(canonical_supersession_journal(journal)).hexdigest(),
        "transition_count": len(journal.entries),
        "audit_records_verified": len(journal.entries),
        "credential_id": credential_id,
        "credential_epoch": credential_epoch,
    }
