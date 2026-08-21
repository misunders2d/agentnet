from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import httpx
import pytest
from starlette.applications import Starlette

from agentnet.core.app import CommunicationCore
from agentnet.identity.recovery import CredentialRecoveryResult
from agentnet.identity.recovery_http import create_credential_recovery_routes
from agentnet.operations.config import ExtensionConfig
from agentnet.security.signatures import P256KeyPair, canonical_json


@dataclass(frozen=True)
class _Authorization:
    transaction_id: str
    authorization_url: str
    expires_at: int


class _RecoveryCoordinator:
    def __init__(self) -> None:
        self.begin_kwargs = None
        self.complete_kwargs = None

    def begin_authorization(self, **kwargs):
        self.begin_kwargs = kwargs
        return _Authorization(
            transaction_id="recovery-transaction-0001",
            authorization_url="https://idp.example/authorize",
            expires_at=2_000_000_300,
        )

    def complete_recovery(self, **kwargs):
        self.complete_kwargs = kwargs
        return CredentialRecoveryResult(
            principal_id="principal-1",
            revoked_harness_id="old-harness-1",
            harness_id="new-harness-1",
            credential_id="new-credential-1",
            approval_receipt_ids=("receipt-1",),
        )


@pytest.mark.anyio
async def test_recovery_http_mount_executes_begin_and_complete_contracts(
    store,
    tmp_path: Path,
) -> None:
    core = CommunicationCore(
        ExtensionConfig(
            domain_id="corp.example",
            data_dir=tmp_path / "data",
            database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
            artifact_dir=tmp_path / "artifacts",
            public_base_url="http://127.0.0.1",
        ),
        store,
    )
    coordinator = _RecoveryCoordinator()
    headers = {"Cache-Control": "no-store", "Pragma": "no-cache"}
    app = Starlette(
        routes=create_credential_recovery_routes(core, coordinator, headers)  # type: ignore[arg-type]
    )
    key = P256KeyPair.generate()

    begin_body = canonical_json(
        {
            "old_harness_id": "old-harness-1",
            "new_harness_kind": "pi",
            "new_harness_name": "replacement laptop",
            "new_binding_assurance": "os_bound",
            "new_public_key_pem": key.public_pem,
        }
    )
    complete_body = canonical_json(
        {
            "recovery_transaction_id": "recovery-transaction-0001",
            "possession_signature": "proof-of-possession",
            "independent_approvals": [{"receipt_id": "receipt-1"}],
        }
    )

    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app),
        base_url="http://127.0.0.1",
    ) as client:
        begun = await client.post(
            "/v1/credential-recovery/oidc/begin",
            content=begin_body,
            headers={"Content-Type": "application/json"},
        )
        completed = await client.post(
            "/v1/credential-recovery/complete",
            content=complete_body,
            headers={"Content-Type": "application/json"},
        )

    assert begun.status_code == 201
    assert begun.json()["transaction_id"] == "recovery-transaction-0001"
    assert begun.headers["cache-control"] == "no-store"
    assert coordinator.begin_kwargs == {
        "domain_id": "corp.example",
        "old_harness_id": "old-harness-1",
        "new_harness_kind": "pi",
        "new_harness_name": "replacement laptop",
        "new_binding_assurance": "os_bound",
        "new_public_key_pem": key.public_pem,
    }
    assert completed.status_code == 201
    assert completed.json()["credential_id"] == "new-credential-1"
    assert completed.headers["pragma"] == "no-cache"
    assert coordinator.complete_kwargs == {
        "transaction_id": "recovery-transaction-0001",
        "possession_signature": "proof-of-possession",
        "approvals": ({"receipt_id": "receipt-1"},),
    }
