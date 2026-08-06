from __future__ import annotations

import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import NAMESPACE_URL, uuid4, uuid5

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import AuthorizationRequest
from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.messaging.events import envelope_digest, new_event
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.protocol.models import Classification, EventType
from agentnet.security.signatures import (
    P256KeyPair,
    canonical_digest,
    canonical_json,
)


def test_synthetic_identity_is_deterministic_only_and_lane_requires_marked_c0(tmp_path: Path) -> None:
    config = ExtensionConfig(
        domain_id="synthetic.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(harness_kind="codex", display_name="sender")
        recipient, _ = core.bootstrap_synthetic_identity(harness_kind="pi", display_name="recipient")
        row = core.store.fetch_one("SELECT status FROM harnesses WHERE harness_id=?", (sender.harness_id,))
        assert row["status"] == "deterministic_only"
        with pytest.raises(AuthorizationError):
            core.send_synthetic_message(
                actor=sender,
                recipients=(recipient.harness_id,),
                payload={"text": "unmarked"},
                idempotency_key=f"synthetic-{uuid4()}",
            )
        accepted = core.send_synthetic_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"synthetic": True, "text": "marked"},
            idempotency_key=f"synthetic-{uuid4()}",
        )
        assert accepted["fact"] == "accepted_local"
        row = core.store.fetch_one(
            "SELECT * FROM events WHERE event_id=?",
            (accepted["event_id"],),
        )
        assert row is not None
        stored_event, stored_payload = core.mailboxes._validated_event_and_payload(row)
        event = stored_event.model_dump(mode="json")
        authorization_context = stored_payload["authorization_context"]
        assert authorization_context["collaboration_scope_id"].startswith("synthetic-c0:")
        assert authorization_context["collaboration_scope_revision"] == 1
        assert authorization_context["collaboration_scope_member_harness_ids"] == [
            recipient.harness_id
        ]
        assert stored_event.classification is Classification.C0_PUBLIC
        assert stored_event.event_type is EventType.MESSAGE
        assert stored_event.room_id is None
        assert stored_event.task_id is None
        assert stored_event.effect_deadline is None
        assert stored_event.released_artifacts == ()
        assert stored_event.payload_digest == canonical_digest(stored_payload)
        unbound_payload = dict(stored_payload)
        del unbound_payload["authorization_context"]
        assert stored_event.payload_digest != canonical_digest(unbound_payload)
        assert accepted["envelope_digest"] == envelope_digest(stored_event)
        assert (
            stored_event.retention_delete_at - stored_event.created_at
        ).total_seconds() <= 86_400
        assert core.store.fetch_one(
            "SELECT COUNT(*) AS n FROM collaboration_scopes WHERE scope_id=?",
            (authorization_context["collaboration_scope_id"],),
        )["n"] == 0
        synthetic_inbox = core.reconcile_synthetic_mailbox(actor=recipient)
        assert len(synthetic_inbox) == 1
        assert synthetic_inbox[0]["event"]["event_id"] == accepted["event_id"]
        assert synthetic_inbox[0]["payload"] == stored_payload
        with pytest.raises(AuthorizationError):
            core.mailboxes.reconcile(
                actor=recipient,
                collaboration_scope_id=authorization_context[
                    "collaboration_scope_id"
                ],
            )
        assert event["actor"]["kind"] == "workload"
        assert event["actor"]["binding_assurance"] == "synthetic_lab"
        assert event["actor"]["binding_assurance"] != "workload_mtls"
        assert "principal_id" not in event["actor"]
        with pytest.raises(AuthorizationError, match="exact lab recipient actor"):
            core.reconcile_synthetic_mailbox(
                actor=recipient.model_copy(
                    update={"binding_assurance": "os_bound"}
                )
            )
        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id=?",
                (recipient.harness_id,),
            )
        with pytest.raises(AuthorizationError):
            core.reconcile_synthetic_mailbox(actor=recipient)
    finally:
        core.close()


def test_synthetic_lane_rejects_caller_authorization_context_collision(
    tmp_path: Path,
) -> None:
    config = ExtensionConfig(
        domain_id="synthetic-collision.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(
            harness_kind="codex",
            display_name="sender",
        )
        recipient, _ = core.bootstrap_synthetic_identity(
            harness_kind="pi",
            display_name="recipient",
        )
        payload = {
            "synthetic": True,
            "authorization_context": {
                "collaboration_scope_id": "caller-controlled"
            },
        }
        with pytest.raises(
            AuthorizationError,
            match="cannot supply authorization_context",
        ):
            core.send_synthetic_message(
                actor=sender,
                recipients=(recipient.harness_id,),
                payload=payload,
                idempotency_key=f"synthetic-collision-{uuid4()}",
            )
        assert payload["authorization_context"] == {
            "collaboration_scope_id": "caller-controlled"
        }
        assert core.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 0
    finally:
        core.close()


def test_synthetic_reconcile_rejects_non_reserved_authorization_context(
    tmp_path: Path,
) -> None:
    config = ExtensionConfig(
        domain_id="synthetic-reconcile.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(
            harness_kind="codex",
            display_name="sender",
        )
        recipient, _ = core.bootstrap_synthetic_identity(
            harness_kind="pi",
            display_name="recipient",
        )
        synthetic_workload = VerifiedActor(
            kind=ActorKind.WORKLOAD,
            domain_id=sender.domain_id,
            workload_id=f"synthetic-lab-mailbox:{sender.harness_id}",
            binding_assurance="synthetic_lab",
        )
        event = new_event(
            domain_id=sender.domain_id,
            actor=synthetic_workload,
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload={
                "synthetic": True,
                "authorization_context": {
                    "collaboration_scope_id": "scope:ordinary-context",
                    "collaboration_scope_revision": 1,
                    "collaboration_scope_policy_revision": 1,
                    "collaboration_scope_domain_revocation_epoch": 1,
                    "collaboration_scope_member_harness_ids": [
                        recipient.harness_id
                    ],
                    "collaboration_scope_digest": "0" * 64,
                },
            },
            idempotency_key=f"synthetic-reconcile-{uuid4()}",
            recipients=(recipient.harness_id,),
            retention_delete_at=datetime.now(UTC) + timedelta(days=1),
            policy_revision=1,
        )
        core.mailboxes.accept(event)
        with pytest.raises(
            AuthorizationError,
            match="synthetic mailbox entry is not visible",
        ):
            core.reconcile_synthetic_mailbox(actor=recipient)
    finally:
        core.close()


def test_synthetic_lane_requires_current_exact_lab_sender_and_recipients(
    tmp_path: Path,
) -> None:
    config = ExtensionConfig(
        domain_id="synthetic-current.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(
            harness_kind="codex",
            display_name="sender",
        )
        recipient, _ = core.bootstrap_synthetic_identity(
            harness_kind="pi",
            display_name="recipient",
        )

        def send(actor: VerifiedActor = sender) -> None:
            core.send_synthetic_message(
                actor=actor,
                recipients=(recipient.harness_id,),
                payload={"synthetic": True, "text": "exact current state"},
                idempotency_key=f"synthetic-current-{uuid4()}",
            )

        with pytest.raises(AuthorizationError, match="exact lab actor"):
            send(sender.model_copy(update={"binding_assurance": "os_bound"}))

        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE harnesses SET status='active' WHERE harness_id=?",
                (sender.harness_id,),
            )
        with pytest.raises(AuthorizationError, match="current deterministic-only"):
            send()
        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE harnesses SET status='deterministic_only' WHERE harness_id=?",
                (sender.harness_id,),
            )
            connection.execute(
                "UPDATE harnesses SET binding_assurance='os_bound' WHERE harness_id=?",
                (recipient.harness_id,),
            )
        with pytest.raises(AuthorizationError, match="recipients must be current"):
            send()
        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE harnesses SET binding_assurance='lab' WHERE harness_id=?",
                (recipient.harness_id,),
            )
            connection.execute(
                "UPDATE credentials SET status='revoked' WHERE harness_id=?",
                (recipient.harness_id,),
            )
        with pytest.raises(AuthorizationError, match="recipients must be current"):
            send()
        assert core.store.fetch_one("SELECT COUNT(*) AS n FROM events")["n"] == 0
    finally:
        core.close()


def test_synthetic_context_digest_binds_exact_current_server_state(
    tmp_path: Path,
) -> None:
    config = ExtensionConfig(
        domain_id="synthetic-context.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        sender, _ = core.bootstrap_synthetic_identity(
            harness_kind="codex",
            display_name="sender",
        )
        first, _ = core.bootstrap_synthetic_identity(
            harness_kind="pi",
            display_name="first",
        )
        second, _ = core.bootstrap_synthetic_identity(
            harness_kind="pi",
            display_name="second",
        )

        def send_and_context(recipients: tuple[str, ...]) -> tuple[object, dict]:
            accepted = core.send_synthetic_message(
                actor=sender,
                recipients=recipients,
                payload={"synthetic": True, "text": "deterministic context"},
                idempotency_key=f"synthetic-context-{uuid4()}",
            )
            row = core.store.fetch_one(
                "SELECT * FROM events WHERE event_id=?",
                (accepted["event_id"],),
            )
            assert row is not None
            event, stored_payload = core.mailboxes._validated_event_and_payload(row)
            return event, stored_payload["authorization_context"]

        event, context = send_and_context(
            (second.harness_id, first.harness_id)
        )
        repeated_event, repeated = send_and_context(
            (first.harness_id, second.harness_id)
        )
        assert repeated == context
        assert context["collaboration_scope_member_harness_ids"] == sorted(
            (first.harness_id, second.harness_id)
        )
        assert context["collaboration_scope_id"] == (
            "synthetic-c0:"
            + str(
                uuid5(
                    NAMESPACE_URL,
                    canonical_json(
                        {
                            "schema": "agentnet.synthetic-c0.scope-id.v1",
                            "domain_id": sender.domain_id,
                            "sender_harness_id": sender.harness_id,
                        }
                    ).decode("utf-8"),
                )
            )
        )
        assert context["collaboration_scope_digest"] == canonical_digest(
            {
                "schema": "agentnet.synthetic-c0.authorization-context.v1",
                "domain_id": sender.domain_id,
                "sender_harness_id": sender.harness_id,
                "collaboration_scope_id": context["collaboration_scope_id"],
                "collaboration_scope_revision": 1,
                "collaboration_scope_policy_revision": 1,
                "collaboration_scope_domain_revocation_epoch": 1,
                "collaboration_scope_member_harness_ids": sorted(
                    (first.harness_id, second.harness_id)
                ),
                "classification": "C0",
                "lane_marker": "local_conformance_deterministic_only",
                "retention_ceiling_seconds": 86_400,
            }
        )
        assert (
            event.retention_delete_at - event.created_at
        ).total_seconds() <= 86_400
        assert (
            repeated_event.retention_delete_at - repeated_event.created_at
        ).total_seconds() <= 86_400

        _, changed_recipient = send_and_context((first.harness_id,))
        assert (
            changed_recipient["collaboration_scope_id"]
            == context["collaboration_scope_id"]
        )
        assert (
            changed_recipient["collaboration_scope_digest"]
            != context["collaboration_scope_digest"]
        )

        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE domains SET policy_revision=policy_revision+1 WHERE domain_id=?",
                (sender.domain_id,),
            )
        _, changed_policy = send_and_context((first.harness_id,))
        assert changed_policy["collaboration_scope_policy_revision"] == 2
        assert (
            changed_policy["collaboration_scope_digest"]
            != changed_recipient["collaboration_scope_digest"]
        )

        with core.store.transaction(immediate=True) as connection:
            connection.execute(
                "UPDATE domains SET revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
                (sender.domain_id,),
            )
        _, changed_epoch = send_and_context((first.harness_id,))
        assert changed_epoch["collaboration_scope_domain_revocation_epoch"] == 2
        assert (
            changed_epoch["collaboration_scope_digest"]
            != changed_policy["collaboration_scope_digest"]
        )
    finally:
        core.close()


def test_synthetic_lane_is_unavailable_to_the_production_profile(
    tmp_path: Path,
) -> None:
    config = ExtensionConfig(
        domain_id="synthetic-profile.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    local_core = CommunicationCore.open(config)
    try:
        sender, _ = local_core.bootstrap_synthetic_identity(
            harness_kind="codex",
            display_name="sender",
        )
        recipient, _ = local_core.bootstrap_synthetic_identity(
            harness_kind="pi",
            display_name="recipient",
        )
        production_core = object.__new__(CommunicationCore)
        production_core.config = config.model_copy(
            update={"profile": RuntimeProfile.ALWAYS_ON_SERVER_AGENT}
        )
        with pytest.raises(GateBlocked, match="local-conformance only"):
            production_core.send_synthetic_message(
                actor=sender,
                recipients=(recipient.harness_id,),
                payload={"synthetic": True, "text": "not production"},
                idempotency_key=f"synthetic-profile-{uuid4()}",
            )
        with pytest.raises(GateBlocked, match="local-conformance only"):
            production_core.reconcile_synthetic_mailbox(actor=recipient)
    finally:
        local_core.close()


def test_active_harness_uses_signed_c0_with_exact_collaboration_scope(
    tmp_path: Path,
) -> None:
    config = ExtensionConfig(
        domain_id="signed-synthetic.example",
        data_dir=tmp_path / "data",
        database_url=f"sqlite:///{tmp_path / 'core.sqlite3'}",
        artifact_dir=tmp_path / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()

        def active_identity(
            *,
            harness_kind: str,
            display_name: str,
        ) -> VerifiedActor:
            suffix = uuid4().hex
            principal_id = f"principal-{suffix}"
            harness_id = f"harness-{suffix}"
            credential_id = f"credential-{suffix}"
            key = P256KeyPair.generate()
            now = int(time.time())
            with core.store.transaction(immediate=True) as connection:
                connection.execute(
                    """INSERT INTO principals(
                        principal_id,domain_id,oidc_issuer,oidc_subject,
                        verified_email,status,created_at
                    ) VALUES(?,?,?,?,?,'active',?)""",
                    (
                        principal_id,
                        config.domain_id,
                        "https://idp.example",
                        f"subject-{suffix}",
                        f"{suffix}@example.test",
                        now,
                    ),
                )
                connection.execute(
                    """INSERT INTO harnesses(
                        harness_id,domain_id,principal_id,kind,display_name,
                        status,binding_assurance,capabilities_json,
                        credential_epoch,created_at
                    ) VALUES(?,?,?,?,?,'active','os_bound',?,1,?)""",
                    (
                        harness_id,
                        config.domain_id,
                        principal_id,
                        harness_kind,
                        display_name,
                        canonical_json({}).decode("utf-8"),
                        now,
                    ),
                )
                connection.execute(
                    """INSERT INTO credentials(
                        credential_id,harness_id,key_id,public_key_pem,status,
                        epoch,not_before,expires_at
                    ) VALUES(?,?,?,?,'active',1,?,?)""",
                    (
                        credential_id,
                        harness_id,
                        key.thumbprint,
                        key.public_pem,
                        now - 1,
                        now + 3_600,
                    ),
                )
            return VerifiedActor(
                kind=ActorKind.VERIFIED_HUMAN_HARNESS,
                domain_id=config.domain_id,
                principal_id=principal_id,
                harness_id=harness_id,
                credential_id=credential_id,
                credential_epoch=1,
                binding_assurance="os_bound",
            )

        sender = active_identity(
            harness_kind="codex",
            display_name="sender",
        )
        recipient = active_identity(
            harness_kind="pi",
            display_name="recipient",
        )
        scope_id = f"scope:signed-c0:{uuid4()}"
        domain = core.store.fetch_one(
            "SELECT policy_revision,revocation_epoch FROM domains WHERE domain_id=?",
            (config.domain_id,),
        )
        assert domain is not None
        proposal = CollaborationScopeProposal(
            scope_id=scope_id,
            scope_kind="direct",
            member_harness_ids=tuple(
                sorted((sender.harness_id, recipient.harness_id))
            ),
            allowed_actions=(
                "message.acknowledge",
                "message.read",
                "message.send",
            ),
            allowed_resource_prefixes=("conversation:",),
            allowed_classifications=(Classification.C0_PUBLIC,),
            canonical_references=(),
            policy_revision=int(domain["policy_revision"]),
            domain_revocation_epoch=int(domain["revocation_epoch"]),
            expires_at=int(time.time()) + 3_600,
        )
        issuance_request = core.collaboration_scopes.issuance_request(
            actor=sender,
            proposal=proposal,
        )
        core.grant_local_entitlement(
            sender,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=f"scope:{scope_id}",
        )
        issuance_decision = core.policy.require(
            AuthorizationRequest(
                actor=sender,
                action=COLLABORATION_SCOPE_ISSUE_ACTION,
                resource=f"scope:{scope_id}",
                policy_revision=int(domain["policy_revision"]),
                context=issuance_request,
            )
        )
        scope = core.collaboration_scopes.issue(
            actor=sender,
            proposal=proposal,
            authority=IssuanceAuthority(
                actor=sender,
                policy_decision_id=issuance_decision.decision_id,
            ),
        )
        assert scope.scope_id == scope_id
        assert not scope.scope_id.startswith("synthetic-c0:")

        core.grant_local_entitlement(sender, action="message.send")
        core.grant_local_entitlement(
            recipient,
            action="mailbox.read",
            resource=recipient.harness_id,
        )
        core.grant_local_entitlement(
            recipient,
            action="mailbox.acknowledge",
            resource=recipient.harness_id,
        )
        accepted = core.send_message(
            actor=sender,
            collaboration_scope_id=scope.scope_id,
            recipients=(recipient.harness_id,),
            payload={"text": "signed local C0"},
            idempotency_key=f"signed-synthetic-{uuid4()}",
            classification=Classification.C0_PUBLIC,
        )
        assert accepted["fact"] == "accepted_local"
        item = core.mailbox(
            actor=recipient,
            collaboration_scope_id=scope.scope_id,
        )[0]
        assert item["event"]["actor"] == sender.model_dump(
            mode="json",
            exclude_none=True,
        )
        assert (
            item["payload"]["authorization_context"]
            == scope.authorization_context()
        )
        acknowledged = core.acknowledge_mailbox(
            actor=recipient,
            collaboration_scope_id=scope.scope_id,
            event_id=accepted["event_id"],
            envelope_digest=accepted["envelope_digest"],
        )
        assert acknowledged["fact"] == "recipient_committed"
        assert core.store.fetch_one(
            "SELECT status FROM harnesses WHERE harness_id=?",
            (sender.harness_id,),
        )["status"] == "active"

        with pytest.raises(AuthorizationError):
            core.send_message(
                actor=sender,
                collaboration_scope_id=scope.scope_id,
                recipients=(recipient.harness_id,),
                payload={"text": "classification exceeds exact scope"},
                idempotency_key=f"signed-synthetic-{uuid4()}",
                classification=Classification.C1_INTERNAL,
            )
    finally:
        core.close()


def test_workload_event_causal_parent_binding_is_exact_and_synthetic_roots_have_none() -> None:
    synthetic = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="synthetic.example",
        workload_id="synthetic-lab-causal-root",
        binding_assurance="synthetic_lab",
    )
    with pytest.raises(PydanticValidationError, match="synthetic lab root"):
        new_event(
            domain_id=synthetic.domain_id,
            actor=synthetic,
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload={"synthetic": True},
            idempotency_key=f"synthetic-causal-{uuid4()}",
            recipients=("synthetic-recipient",),
            causal_parent_ids=("forged-parent",),
        )

    exact_parent = "event-exact-parent"
    workload = VerifiedActor(
        kind=ActorKind.WORKLOAD,
        domain_id="workload.example",
        workload_id="registered-worker",
        workload_registration_id="registration-1",
        workload_role="mailbox_dispatcher",
        workload_process_id=123,
        workload_process_start_time=456,
        workload_session_id="session-1",
        workload_revocation_epoch=1,
        parent_event_id=exact_parent,
        task_grant_id="grant-1",
        credential_id="registration-1",
        credential_epoch=1,
        binding_assurance="workload_mtls",
    )
    accepted = new_event(
        domain_id=workload.domain_id,
        actor=workload,
        event_type=EventType.MESSAGE,
        classification=Classification.C1_INTERNAL,
        payload={"result": "bounded"},
        idempotency_key=f"workload-causal-{uuid4()}",
        recipients=("workload-recipient",),
        causal_parent_ids=(exact_parent,),
    )
    assert accepted.causal_parent_ids == (exact_parent,)

    for wrong_parents in ((), ("event-other-parent",)):
        with pytest.raises(PydanticValidationError, match="exact transport parent"):
            new_event(
                domain_id=workload.domain_id,
                actor=workload,
                event_type=EventType.MESSAGE,
                classification=Classification.C1_INTERNAL,
                payload={"result": "not-bound"},
                idempotency_key=f"workload-causal-{uuid4()}",
                recipients=("workload-recipient",),
                causal_parent_ids=wrong_parents,
            )
