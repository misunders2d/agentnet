from __future__ import annotations

from dataclasses import dataclass
from types import SimpleNamespace
from typing import Any

import pytest

from agentnet.artifacts.clamav import ClamAVScanError
from agentnet.supervisor.scanner_worker import ScannerWorker


@dataclass
class FakeObjects:
    content: bytes = b"hello"
    reads: int = 0

    def read_plaintext(self, object_key: str, object_version: str, *, released: bool) -> bytes:
        assert object_key == "b" * 32
        assert object_version == "c" * 64
        assert released is False
        self.reads += 1
        return self.content


class FakeStore:
    def __init__(self, rows: list[dict[str, Any]]) -> None:
        self.rows = rows
        self.limits: list[int] = []

    def fetch_all(self, query: str, parameters: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
        assert "state='quarantined'" in query
        assert "LIMIT ?" in query
        limit = int(parameters[-1])
        self.limits.append(limit)
        pending = [row for row in self.rows if row["state"] == "quarantined"]
        return sorted(pending, key=lambda row: (row["created_at"], row["artifact_id"]))[:limit]


class FakeArtifactService:
    def __init__(self, rows: list[dict[str, Any]]) -> None:
        self.store = FakeStore(rows)
        self.objects = FakeObjects()
        self.recorded: list[Any] = []
        self.release_calls = 0

    def record_scan(self, artifact_id: str, attestation: Any) -> dict[str, Any]:
        row = next(row for row in self.store.rows if row["artifact_id"] == artifact_id)
        if attestation.artifact_id != artifact_id:
            raise AssertionError("worker substituted the artifact binding")
        self.recorded.append(attestation)
        row["state"] = "scan_passed" if attestation.result == "allow" else "held"
        return {"artifact_id": artifact_id, "state": row["state"]}

    def release(self, *_args: Any, **_kwargs: Any) -> None:
        self.release_calls += 1
        raise AssertionError("scanner worker must never release artifacts")


class ScriptedScanner:
    def __init__(self, *outcomes: str | BaseException) -> None:
        self.outcomes = list(outcomes)
        self.calls: list[dict[str, Any]] = []

    def scan(self, **kwargs: Any) -> Any:
        self.calls.append(kwargs)
        outcome = self.outcomes.pop(0)
        if isinstance(outcome, BaseException):
            raise outcome
        return SimpleNamespace(
            artifact_id=kwargs["artifact_id"],
            issued_at=kwargs["issued_at"],
            expires_at=kwargs["expires_at"],
            result=outcome,
        )


def _row(artifact_id: str = "artifact-00000001", *, created_at: int = 1) -> dict[str, Any]:
    return {
        "artifact_id": artifact_id,
        "classification": "C1",
        "ciphertext_digest": "a" * 64,
        "created_at": created_at,
        "expected_digest": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
        "object_key": "b" * 32,
        "object_version": "c" * 64,
        "policy_revision": 4,
        "state": "quarantined",
    }


def test_worker_scans_exact_quarantine_binding_without_releasing() -> None:
    row = _row()
    service = FakeArtifactService([row])
    scanner = ScriptedScanner("allow")
    worker = ScannerWorker(service, scanner, clock=lambda: 100, attestation_ttl_seconds=60)

    assert worker.process_once() == ("artifact-00000001",)

    assert row["state"] == "scan_passed"
    assert scanner.calls == [
        {
            "artifact_id": "artifact-00000001",
            "classification": "C1",
            "ciphertext_digest": "a" * 64,
            "object_key": "b" * 32,
            "object_version": "c" * 64,
            "plaintext_digest": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
            "policy_revision": 4,
            "content": b"hello",
            "issued_at": 100,
            "expires_at": 160,
        }
    ]
    assert service.release_calls == 0


@pytest.mark.parametrize(
    "failure",
    [
        ClamAVScanError("timeout"),
        ClamAVScanError("malformed"),
        ClamAVScanError("unknown"),
        ClamAVScanError("signature database is stale"),
        ClamAVScanError("size boundary"),
    ],
)
def test_scanner_failure_keeps_artifact_quarantined_and_never_releases(
    failure: BaseException,
) -> None:
    row = _row()
    service = FakeArtifactService([row])
    worker = ScannerWorker(service, ScriptedScanner(failure), clock=lambda: 100)

    assert worker.process_once() == ()
    assert row["state"] == "quarantined"
    assert service.recorded == []
    assert service.release_calls == 0


def test_indeterminate_attestation_is_not_recorded_or_released() -> None:
    row = _row()
    service = FakeArtifactService([row])
    worker = ScannerWorker(service, ScriptedScanner("indeterminate"), clock=lambda: 100)

    assert worker.process_once() == ()
    assert row["state"] == "quarantined"
    assert service.recorded == []
    assert service.release_calls == 0


def test_failed_scan_is_idempotently_retried_until_one_attestation_is_recorded() -> None:
    row = _row()
    service = FakeArtifactService([row])
    scanner = ScriptedScanner(ClamAVScanError("timeout"), "allow")
    worker = ScannerWorker(service, scanner, clock=lambda: 100)

    assert worker.process_once() == ()
    assert row["state"] == "quarantined"
    assert worker.process_once() == ("artifact-00000001",)
    assert worker.process_once() == ()
    assert row["state"] == "scan_passed"
    assert len(scanner.calls) == 2
    assert len(service.recorded) == 1


def test_worker_enforces_a_bounded_batch_and_deterministic_order() -> None:
    rows = [_row(f"artifact-{index:08d}", created_at=10 - index) for index in range(30)]
    service = FakeArtifactService(rows)
    scanner = ScriptedScanner(*(["allow"] * 25))
    worker = ScannerWorker(service, scanner, clock=lambda: 100)

    processed = worker.process_once(limit=25)

    assert len(processed) == 25
    assert processed == tuple(row["artifact_id"] for row in sorted(rows, key=lambda row: (row["created_at"], row["artifact_id"]))[:25])
    assert service.store.limits == [25]


@pytest.mark.parametrize("limit", [0, -1, 101, True])
def test_worker_rejects_unbounded_or_invalid_batch_sizes(limit: int) -> None:
    worker = ScannerWorker(FakeArtifactService([]), ScriptedScanner(), clock=lambda: 100)

    with pytest.raises(ValueError, match="batch limit"):
        worker.process_once(limit=limit)
