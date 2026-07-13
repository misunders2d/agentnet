"""Local hash-chain audit and signed checkpoint prototype."""

from __future__ import annotations

from typing import Any

from agentnet.security.signatures import P256KeyPair, verify_signature
from agentnet.storage.sqlite import SQLiteStore


class AuditService:
    def __init__(self, store: SQLiteStore) -> None:
        self.store = store

    def verify(self) -> dict[str, int | bool]:
        valid, position = self.store.verify_audit_chain()
        return {"valid": valid, "records_checked": position}

    def checkpoint(self, signer: P256KeyPair) -> dict[str, Any]:
        row = self.store.fetch_one("SELECT sequence,record_hash FROM audit_log ORDER BY sequence DESC LIMIT 1")
        payload = {
            "algorithm": "ES256",
            "last_hash": row["record_hash"] if row else "0" * 64,
            "last_sequence": row["sequence"] if row else 0,
            "profile": "agentnet.audit.checkpoint/1.0",
            "signer_key_id": signer.thumbprint,
        }
        return payload | {"signature": signer.sign("agentnet.audit.checkpoint.v1", payload)}

    @staticmethod
    def verify_checkpoint(public_key_pem: str, checkpoint: dict[str, Any]) -> None:
        payload = dict(checkpoint)
        signature = payload.pop("signature")
        verify_signature(public_key_pem, "agentnet.audit.checkpoint.v1", payload, signature)

