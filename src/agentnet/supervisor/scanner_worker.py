"""Bounded fail-closed processing of immutable quarantined artifacts."""

from __future__ import annotations

import time
from collections.abc import Callable

from agentnet.artifacts.scanner import MaintainedArtifactScanner
from agentnet.artifacts.service import ArtifactService


class ScannerWorker:
    """Scan a bounded quarantine batch without ever performing artifact release."""

    def __init__(
        self,
        artifacts: ArtifactService,
        scanner: MaintainedArtifactScanner,
        *,
        clock: Callable[[], int] | None = None,
        attestation_ttl_seconds: int = 60,
    ) -> None:
        if artifacts.objects is None:
            raise ValueError("scanner worker requires enabled artifact object storage")
        if type(attestation_ttl_seconds) is not int or not 1 <= attestation_ttl_seconds <= 86_400:
            raise ValueError("scanner worker attestation TTL is outside the bounded profile")
        self.artifacts = artifacts
        self.scanner = scanner
        self.clock = clock or (lambda: int(time.time()))
        self.attestation_ttl_seconds = attestation_ttl_seconds

    def process_once(self, *, limit: int = 25) -> tuple[str, ...]:
        if type(limit) is not int or not 1 <= limit <= 100:
            raise ValueError("scanner worker batch limit is outside the bounded profile")
        rows = self.artifacts.store.fetch_all(
            """SELECT m.artifact_id,
                      m.classification,
                      m.ciphertext_digest,
                      m.created_at,
                      m.object_key,
                      m.object_version,
                      r.expected_digest,
                      d.policy_revision
                 FROM artifact_manifests m
                 JOIN artifact_reservations r ON r.reservation_id=m.reservation_id
                 JOIN domains d ON d.domain_id=m.domain_id
                WHERE m.state='quarantined'
                ORDER BY m.created_at,m.artifact_id
                LIMIT ?""",
            (limit,),
        )
        processed: list[str] = []
        for row in rows:
            try:
                artifact_id = str(row["artifact_id"])
                object_key = str(row["object_key"])
                object_version = str(row["object_version"])
                content = self.artifacts.objects.read_plaintext(
                    object_key,
                    object_version,
                    released=False,
                )
                issued_at = self.clock()
                attestation = self.scanner.scan(
                    artifact_id=artifact_id,
                    classification=row["classification"],
                    ciphertext_digest=str(row["ciphertext_digest"]),
                    object_key=object_key,
                    object_version=object_version,
                    plaintext_digest=str(row["expected_digest"]),
                    policy_revision=int(row["policy_revision"]),
                    content=content,
                    issued_at=issued_at,
                    expires_at=issued_at + self.attestation_ttl_seconds,
                )
                if attestation.result not in {"allow", "deny"}:
                    continue
                recorded = self.artifacts.record_scan(artifact_id, attestation)
                if recorded.get("state") in {"scan_passed", "held"}:
                    processed.append(artifact_id)
            except Exception:
                # A timeout, stale database, malformed response, object failure,
                # signature/policy rejection, or concurrent state transition must
                # leave the artifact in its authoritative current state. This
                # worker never advances or releases it on an uncertain outcome.
                continue
        return tuple(processed)


__all__ = ["ScannerWorker"]
