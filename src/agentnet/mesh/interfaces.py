"""Encrypted opportunistic relay contract with no transitive authority."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol


@dataclass(frozen=True, slots=True)
class RelayEnvelope:
    envelope_id: str
    recipient_id: str
    ciphertext_digest: str
    policy_epoch: int
    revocation_epoch: int
    expires_at: int
    origin_signature: str


class OpportunisticRelay(Protocol):
    def store(self, envelope: RelayEnvelope) -> str: ...
    def fetch(self, recipient_id: str, after_receipt: str | None = None) -> list[RelayEnvelope]: ...
    def delete(self, envelope_id: str, authority_receipt: str) -> None: ...

