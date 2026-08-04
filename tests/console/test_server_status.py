from __future__ import annotations

import hashlib
import time

import pytest

from agentnet.console.read_service import ConsoleReadService
from agentnet.console.server_status import ServerStatusContribution, ServerStatusService
from agentnet.errors import AuthenticationError
from agentnet.security.signatures import canonical_json


class _Allow:
    def require(self, **_kwargs) -> None:
        return None


def test_independent_server_status_keeps_offline_sibling_visible(store, identity_factory) -> None:
    now = int(time.time())
    alpha, _ = identity_factory(domain="corp.example", kind="server-agent", binding_assurance="os_bound")
    beta, _ = identity_factory(domain="corp.example", kind="server-agent", binding_assurance="os_bound")
    service = ServerStatusService(store=store, ttl_seconds=120, clock=lambda: now)
    rows = store.fetch_all(
        "SELECT harness_id,capabilities_json FROM harnesses WHERE harness_id IN (?,?)",
        (alpha.harness_id, beta.harness_id),
    )
    capabilities = {row["harness_id"]: row["capabilities_json"] for row in rows}
    service.publish(
        actor=alpha,
        contribution=ServerStatusContribution(
            schema="agentnet.console.server-status.v1",
            domain_id=alpha.domain_id,
            harness_id=alpha.harness_id,
            runtime_instance_id="server-alpha",
            version="0.1.43",
            capability_digest=hashlib.sha256(capabilities[alpha.harness_id].encode()).hexdigest(),
            service_states=("message_delivery",),
            emitted_at=now,
            expires_at=now + 120,
        ),
    )
    reader = ConsoleReadService(store=store, require=_Allow().require, clock=lambda: now + 1)
    current = reader.servers(actor=alpha)

    assert {row.harness_id for row in current.servers} == {alpha.harness_id, beta.harness_id}
    assert {row.harness_id: row.state.value for row in current.servers} == {
        alpha.harness_id: "Online",
        beta.harness_id: "Offline",
    }


def test_server_status_body_cannot_substitute_another_harness(store, identity_factory) -> None:
    now = int(time.time())
    alpha, _ = identity_factory(domain="corp.example", kind="server-agent", binding_assurance="os_bound")
    beta, _ = identity_factory(domain="corp.example", kind="server-agent", binding_assurance="os_bound")
    row = store.fetch_one("SELECT capabilities_json FROM harnesses WHERE harness_id=?", (beta.harness_id,))
    service = ServerStatusService(store=store, ttl_seconds=120, clock=lambda: now)
    contribution = ServerStatusContribution(
        schema="agentnet.console.server-status.v1",
        domain_id=beta.domain_id,
        harness_id=beta.harness_id,
        runtime_instance_id="server-beta",
        version="0.1.43",
        capability_digest=hashlib.sha256(row["capabilities_json"].encode()).hexdigest(),
        service_states=("offline_delivery",),
        emitted_at=now,
        expires_at=now + 120,
    )

    with pytest.raises(AuthenticationError):
        service.publish(actor=alpha, contribution=contribution)
