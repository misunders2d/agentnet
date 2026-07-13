"""Transport-neutral mailbox custody evidence seam."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Literal


@dataclass(frozen=True, slots=True)
class CustodyEvidence:
    custodian_id: str
    event_id: str
    recipient_id: str
    exact_digest: str
    durability: Literal["local", "quorum"]
    receipt_id: str

