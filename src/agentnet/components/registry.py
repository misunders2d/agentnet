"""Pinned component records cannot redefine corporate semantics."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Literal


@dataclass(frozen=True, slots=True)
class ComponentRecord:
    name: str
    version: str
    purpose: str
    decision: Literal["accepted_phase0", "deferred", "rejected", "not_available"]
    policy_boundary: str
    evidence: str


BASELINE_COMPONENTS = (
    ComponentRecord("a2a-sdk", "1.1.0", "external A2A mechanism", "accepted_phase0", "external identity never becomes corporate authority", "docs/BUILD_VS_REUSE.md"),
    ComponentRecord("PostgreSQL", "18.4 local; non-HA", "production transactional state", "not_available", "state names follow tested commits; local process is not HA/PITR evidence", "evidence/gates/G09/2026-07-13-postgresql-18.4-local/manifest.json"),
    ComponentRecord("Cedar", "unselected", "policy evaluation", "not_available", "one human-positive authority/revision", "docs/BAKEOFF_PLAN.md"),
    ComponentRecord("MCP Python SDK", "1.28.1", "local harness binding", "accepted_phase0", "arguments never establish identity", "docs/BUILD_VS_REUSE.md"),
    ComponentRecord("MLS", "unselected", "sealed room cryptography", "not_available", "room membership/policy remains corporate", "docs/BAKEOFF_PLAN.md"),
)
