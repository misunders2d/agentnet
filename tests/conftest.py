from __future__ import annotations

import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest

from agentnet.authorization.grants import TaskGrantService
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.workload import RegisteredWorkloadCredential
from agentnet.protocol.models import Classification, TaskGrant
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.sqlite import SQLiteStore


@pytest.fixture
def store(tmp_path: Path) -> SQLiteStore:
    cipher = LocalEnvelopeCipher.from_key_file(tmp_path / "secrets" / "local.key")
    database = SQLiteStore(tmp_path / "core.sqlite3", cipher)
    yield database
    database.close()


@pytest.fixture
def identity_factory(store: SQLiteStore):
    def create(
        *,
        domain: str = "corp.example",
        kind: str = "codex",
        email: str | None = None,
        binding_assurance: str = "lab",
        principal_id: str | None = None,
    ):
        if binding_assurance not in {"lab", "os_bound", "hardware_bound"}:
            raise ValueError("test identity binding assurance is invalid")
        suffix = uuid4().hex[:12]
        principal_id = principal_id or f"principal-{suffix}"
        harness_id = f"harness-{suffix}"
        credential_id = f"credential-{suffix}"
        key = P256KeyPair.generate()
        now = int(time.time())
        with store.transaction() as connection:
            connection.execute(
                "INSERT OR IGNORE INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
                (domain, now),
            )
            principal = connection.execute(
                "SELECT domain_id,status FROM principals WHERE principal_id=?",
                (principal_id,),
            ).fetchone()
            if principal is None:
                connection.execute(
                    """INSERT INTO principals(
                        principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                    ) VALUES(?,?,?,?,?,'active',?)""",
                    (
                        principal_id,
                        domain,
                        "https://idp.example",
                        f"subject-{suffix}",
                        email or f"{suffix}@example.test",
                        now,
                    ),
                )
            elif principal["domain_id"] != domain or principal["status"] != "active":
                raise ValueError("test sibling harness requires an active principal in the same domain")
            connection.execute(
                """INSERT INTO harnesses(
                    harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,capabilities_json,created_at
                ) VALUES(?,?,?,?,?,'active',?,?,?)""",
                (
                    harness_id,
                    domain,
                    principal_id,
                    kind,
                    f"{kind}-{suffix}",
                    binding_assurance,
                    canonical_json({}).decode(),
                    now,
                ),
            )
            connection.execute(
                """INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,'active',1,?,?)""",
                (credential_id, harness_id, key.thumbprint, key.public_pem, now - 1, now + 3600),
            )
        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=domain,
            principal_id=principal_id,
            harness_id=harness_id,
            credential_id=credential_id,
            credential_epoch=1,
            binding_assurance=binding_assurance,
        )
        return actor, key

    return create


@pytest.fixture
def workload_factory(store: SQLiteStore):
    """Insert a cryptographically bound workload registration for focused tests."""

    def create(
        *,
        domain: str,
        role: str,
        workload_id: str | None = None,
        recipient_scope: str = "*",
        parent_event_id: str | None = None,
        task_grant_id: str | None = None,
    ):
        if (parent_event_id is None) != (task_grant_id is None):
            raise ValueError("test workload parent/grant binding must be paired")
        suffix = uuid4().hex
        registration_id = f"workload-registration-{suffix}"
        workload_id = workload_id or f"workload-{suffix}"
        session_id = f"workload-session-{suffix}"
        key = P256KeyPair.generate()
        now = int(time.time())
        domain_row = store.fetch_one(
            "SELECT revocation_epoch FROM domains WHERE domain_id=?", (domain,)
        )
        if domain_row is None:
            raise ValueError("test workload domain must already exist")
        revocation_epoch = int(domain_row["revocation_epoch"])
        with store.transaction() as connection:
            connection.execute(
                """INSERT INTO workload_registrations(
                    registration_id,domain_id,workload_id,workload_role,recipient_scope,
                    process_id,process_start_time,session_id,spiffe_id,certificate_serial,
                    key_id,public_key_pem,credential_epoch,revocation_epoch,parent_event_id,
                    task_grant_id,status,issued_at,expires_at
                ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                (
                    registration_id,
                    domain,
                    workload_id,
                    role,
                    recipient_scope,
                    4242,
                    now - 10,
                    session_id,
                    f"spiffe://{domain}/{role}/{suffix}",
                    f"serial-{suffix}",
                    key.thumbprint,
                    key.public_pem,
                    1,
                    revocation_epoch,
                    parent_event_id,
                    task_grant_id,
                    "active",
                    now - 1,
                    now + 3600,
                ),
            )
        actor = VerifiedActor(
            kind=ActorKind.WORKLOAD,
            domain_id=domain,
            workload_id=workload_id,
            workload_registration_id=registration_id,
            workload_role=role,
            workload_process_id=4242,
            workload_process_start_time=now - 10,
            workload_session_id=session_id,
            workload_revocation_epoch=revocation_epoch,
            parent_event_id=parent_event_id,
            task_grant_id=task_grant_id,
            credential_id=registration_id,
            credential_epoch=1,
            binding_assurance="workload_mtls",
        )
        return actor, key

    return create


@pytest.fixture
def workload_credentials_factory(workload_factory):
    """Build exact event/grant-bound registered credentials for a workflow."""

    def create(
        *,
        domain: str,
        recipient_id: str,
        event_id: str,
        task_grant_id: str,
        roles: tuple[str, ...],
    ) -> dict[str, RegisteredWorkloadCredential]:
        credentials: dict[str, RegisteredWorkloadCredential] = {}
        for role in roles:
            actor, signer = workload_factory(
                domain=domain,
                role=role,
                recipient_scope=recipient_id,
                parent_event_id=event_id,
                task_grant_id=task_grant_id,
            )
            credentials[role] = RegisteredWorkloadCredential(actor=actor, signer=signer)
        return credentials

    return create


@pytest.fixture
def execution_grant_factory(store: SQLiteStore):
    """Issue the recipient-owned, event-scoped grant used by real workers."""

    def create(
        *,
        recipient: VerifiedActor,
        event_id: str,
        actions: frozenset[str] = frozenset(
            {"message.process", "task.process", "effect.execute", "task.cancel"}
        ),
        max_uses: int = 16,
    ) -> TaskGrant:
        grant = TaskGrant(
            domain_id=recipient.domain_id,
            principal_id=recipient.positive_authority_id or "",
            harness_id=recipient.harness_id or "",
            actions=actions,
            resources=frozenset({f"event:{event_id}"}),
            input_sources=frozenset({"mailbox"}),
            output_sinks=frozenset({"receipt"}),
            data_classes=frozenset(
                {
                    Classification.C0_PUBLIC,
                    Classification.C1_INTERNAL,
                    Classification.C2_RESTRICTED,
                }
            ),
            max_uses=max_uses,
            expires_at=datetime.now(UTC) + timedelta(hours=1),
        )
        with store.transaction() as connection:
            return TaskGrantService(store)._insert_in_transaction(
                connection,
                grant=grant,
                when=datetime.now(UTC),
                issuance_evidence={"kind": "focused_recipient_owner_execution_grant"},
            )

    return create
