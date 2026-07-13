#!/usr/bin/env python3
"""Export deterministic JSON Schemas for the versioned protocol catalog."""

from __future__ import annotations

import json
from pathlib import Path

from agentnet.approval import IndependentApprovalReceipt
from agentnet.identity.actors import VerifiedActor
from agentnet.identity.invitations import (
    InternalInvitationAcceptance,
    InternalInvitationRecord,
    InternalInvitationRequest,
    InternalInvitationTransaction,
)
from agentnet.organization import (
    RelationshipConsentTransaction,
    RelationshipGovernanceRecord,
    RelationshipPolicyException,
)
from agentnet.organization.conflicts import (
    TaskConflictAdjudication,
    TaskConflictOutcome,
    TaskExecutionIntent,
)
from agentnet.provenance import ProvenanceReferenceV1
from agentnet.protocol.models import EventEnvelope, PresenceLease, Receipt, TaskGrant
from agentnet.protocol.schema_catalog import (
    ArtifactManifestRecord,
    AuditIntent,
    EnrollmentTransaction,
    FederationInvitationRecord,
    IdentityRecord,
    ProtocolError,
    RevocationRecord,
    RoomRecord,
)


CATALOG = {
    "actor": VerifiedActor,
    "artifact-manifest": ArtifactManifestRecord,
    "audit-intent": AuditIntent,
    "enrollment-transaction": EnrollmentTransaction,
    "event": EventEnvelope,
    "federation-invitation": FederationInvitationRecord,
    "identity": IdentityRecord,
    "independent-approval-receipt": IndependentApprovalReceipt,
    "internal-invitation-acceptance": InternalInvitationAcceptance,
    "internal-invitation-record": InternalInvitationRecord,
    "internal-invitation-request": InternalInvitationRequest,
    "internal-invitation-transaction": InternalInvitationTransaction,
    "presence": PresenceLease,
    "protocol-error": ProtocolError,
    "receipt": Receipt,
    "relationship": RelationshipGovernanceRecord,
    "relationship-consent-transaction": RelationshipConsentTransaction,
    "relationship-policy-exception": RelationshipPolicyException,
    "revocation": RevocationRecord,
    "room": RoomRecord,
    "provenance-reference": ProvenanceReferenceV1,
    "task-conflict-adjudication": TaskConflictAdjudication,
    "task-conflict-outcome": TaskConflictOutcome,
    "task-execution-intent": TaskExecutionIntent,
    "task-grant": TaskGrant,
}


def main() -> None:
    target = Path("schemas/v1")
    target.mkdir(parents=True, exist_ok=True)
    for name, model in CATALOG.items():
        schema = model.model_json_schema(by_alias=True)
        schema["$id"] = f"https://agentnet.invalid/schemas/v1/{name}.json"
        schema["x-agentnet-schema-version"] = "1.0"
        (target / f"{name}.json").write_text(
            json.dumps(schema, indent=2, sort_keys=True, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )


if __name__ == "__main__":
    main()
