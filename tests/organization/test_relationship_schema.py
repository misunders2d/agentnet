from __future__ import annotations

import os
import subprocess
import sys
from datetime import UTC, datetime, timedelta

import pytest
from pydantic import ValidationError

from agentnet.organization.relationships import RelationshipGovernanceRecord
from agentnet.protocol.models import (
    AssignmentScope,
    Classification,
    EmptyAssignmentScope,
    Relationship,
)


def _scope() -> dict[str, object]:
    return {
        "task_types": ["report"],
        "resources": ["project:alpha"],
        "data_classes": [Classification.C1_INTERNAL],
        "tools": [],
        "max_budget": 10,
        "max_duration_seconds": 300,
        "max_concurrency": 1,
        "authority_effect": "custody_only",
    }


def _relationship(**updates: object) -> Relationship:
    values: dict[str, object] = {
        "relationship_id": "relationship-v1",
        "domain_id": "domain-a",
        "administrator_harness_id": "administrator-harness",
        "subordinate_harness_id": "subordinate-harness",
        "may_assign": True,
        "assignment_scope": _scope(),
        "revision": 1,
        "expires_at": datetime.now(UTC) + timedelta(hours=1),
    }
    values.update(updates)
    return Relationship.model_validate(values)


def test_relationship_schema_has_only_typed_closed_assignment_scope() -> None:
    schema = RelationshipGovernanceRecord.model_json_schema(by_alias=True)
    definitions = schema["$defs"]

    assignment = definitions["AssignmentScope"]
    empty = definitions["EmptyAssignmentScope"]
    assert assignment["additionalProperties"] is False
    assert empty["additionalProperties"] is False
    assert assignment["properties"]["authority_effect"]["const"] == "custody_only"
    assert assignment["properties"]["max_duration_seconds"]["maximum"] == 31_536_000
    assert assignment["properties"]["max_concurrency"]["maximum"] == 65_535

    relationship_properties = schema["properties"]
    assert relationship_properties["relationship_id"]["maxLength"] == 256
    assert relationship_properties["administrator_harness_id"]["maxLength"] == 256
    assert relationship_properties["subordinate_harness_id"]["maxLength"] == 256


@pytest.mark.parametrize(
    "updates",
    [
        {"relationship_id": "x" * 257},
        {"expires_at": datetime.now()},
        {"assignment_scope": {**_scope(), "data_access": True}},
        {"assignment_scope": {}},
        {"may_assign": False, "assignment_scope": _scope()},
    ],
)
def test_relationship_model_rejects_unbounded_or_scope_smuggling(
    updates: dict[str, object],
) -> None:
    with pytest.raises(ValidationError):
        _relationship(**updates)


def test_nonassigning_relationship_has_one_closed_empty_scope_shape() -> None:
    edge = _relationship(may_assign=False, assignment_scope={})
    assert edge.model_dump(mode="json")["assignment_scope"] == {}
    assert isinstance(edge.assignment_scope, EmptyAssignmentScope)

    with pytest.raises(ValidationError):
        Relationship.model_validate(
            {
                **edge.model_dump(mode="json"),
                "assignment_scope": {"unexpected": "authority"},
            }
        )


def test_scope_type_remains_the_runtime_enforcement_model() -> None:
    edge = _relationship()
    assert isinstance(edge.assignment_scope, AssignmentScope)


def test_consent_digest_is_stable_across_python_hash_seeds() -> None:
    program = """
from datetime import UTC, datetime
from agentnet.organization import RelationshipConsentTransaction
from agentnet.protocol.models import AssignmentScope, Classification, Relationship
from agentnet.security.signatures import canonical_digest

scope = AssignmentScope(
    task_types=frozenset({'zeta', 'alpha', 'middle'}),
    resources=frozenset({'resource:z', 'resource:a', 'resource:m'}),
    data_classes=frozenset({Classification.C2_RESTRICTED, Classification.C0_PUBLIC, Classification.C1_INTERNAL}),
    tools=frozenset({'tool:z', 'tool:a', 'tool:m'}),
    max_budget=100,
    max_duration_seconds=600,
    max_concurrency=3,
)
relationship = Relationship(
    relationship_id='relationship-hash-seed',
    domain_id='domain-a',
    administrator_harness_id='administrator-harness',
    subordinate_harness_id='subordinate-harness',
    may_assign=True,
    assignment_scope=scope,
    revision=1,
    expires_at=datetime(2026, 7, 13, 13, 0, tzinfo=UTC),
)
transaction = RelationshipConsentTransaction(
    transaction_id='transaction-hash-seed',
    relationship=relationship,
    proposal_expires_at=datetime(2026, 7, 13, 12, 30, tzinfo=UTC),
    administrator_owner_kind='human',
    administrator_owner_id='administrator-owner',
    subordinate_owner_kind='human',
    subordinate_owner_id='subordinate-owner',
    policy_revision=1,
    domain_revocation_epoch=1,
    administrator_credential_epoch=1,
    subordinate_credential_epoch=1,
    lineage_revocation_epoch=0,
    proposed_at=datetime(2026, 7, 13, 12, 0, tzinfo=UTC),
)
print(canonical_digest(transaction.model_dump(mode='json')))
"""
    digests = set()
    for seed in ("1", "2", "17", "random"):
        environment = os.environ.copy()
        environment["PYTHONHASHSEED"] = seed
        digests.add(
            subprocess.check_output(
                [sys.executable, "-c", program],
                cwd=os.getcwd(),
                env=environment,
                text=True,
            ).strip()
        )
    assert len(digests) == 1
