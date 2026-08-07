from __future__ import annotations

import hashlib
import json
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pytest

from agentnet.artifacts.local_destination import SafeDownloadDestination
from agentnet.artifacts.scanner import ArtifactScanAttestationV1
from agentnet.artifacts.service import ArtifactService, FilesystemArtifactStore
from agentnet.artifacts.transfer import ArtifactTransferService
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.authorization.evidence import IssuanceAuthority
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.errors import AuthorizationError, IdempotencyConflict
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.conversation import ConversationService
from agentnet.protocol.models import Classification
from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor.scanner_worker import ScannerWorker


_CRASH_PHASES = (
    "after_reserve",
    "after_upload",
    "after_manifest",
    "after_scan",
    "after_release",
    "after_event",
)
_ARTIFACT_ACTIONS = (
    "artifact.download",
    "artifact.manifest.promote",
    "artifact.release",
    "artifact.upload.bytes",
    "artifact.upload.reserve",
)
_FILE_SCOPE_ID = "scope:file-transfer-contract"


class InjectedTransferCrash(RuntimeError):
    pass


class HermeticMaintainedScanner:
    """Issue valid test attestations without claiming external scanner evidence."""

    scanner_id = "scanner:hermetic-contract"
    scanner_engine = "hermetic-contract-scanner"
    scanner_version = "1"
    scanner_key_epoch = 1
    rules_digest = "a" * 64
    profile_digest = "b" * 64

    def __init__(self, key: P256KeyPair) -> None:
        self.key = key

    def scan(self, **values: Any) -> ArtifactScanAttestationV1:
        fields = {
            "artifact_id": values["artifact_id"],
            "classification": values["classification"],
            "ciphertext_digest": values["ciphertext_digest"],
            "expires_at": values["expires_at"],
            "issued_at": values["issued_at"],
            "object_key": values["object_key"],
            "object_version": values["object_version"],
            "plaintext_digest": values["plaintext_digest"],
            "policy_revision": values["policy_revision"],
            "profile_digest": self.profile_digest,
            "result": "allow",
            "rules_digest": self.rules_digest,
            "scanner_engine": self.scanner_engine,
            "scanner_id": self.scanner_id,
            "scanner_key_epoch": self.scanner_key_epoch,
            "scanner_version": self.scanner_version,
        }
        return ArtifactScanAttestationV1(
            **fields,
            signature=self.key.sign("agentnet.artifact.attestation.v1", fields),
        )


@dataclass
class TransferStack:
    store: Any
    artifacts: ArtifactService
    scanner: ScannerWorker
    scopes: CollaborationScopeService
    conversations: ConversationService
    policy: LocalConformancePolicyEngine
    sender: Any
    recipient: Any
    sibling: Any
    cross_domain: Any
    source: Path
    now: int

    def service(self, phase_hook=None, destination=None) -> ArtifactTransferService:
        return ArtifactTransferService(
            self.store,
            self.artifacts,
            self.scanner,
            self.scopes,
            self.conversations,
            authorize=self.authorize,
            clock=lambda: self.now,
            destination=destination,
            phase_hook=phase_hook,
        )

    def authorize(
        self,
        *,
        actor,
        action: str,
        resource: str,
        classification: Classification,
        context: dict[str, Any],
    ):
        revision = self.policy.current_policy_revision(actor)
        return self.policy.require(
            AuthorizationRequest(
                actor=actor,
                action=action,
                resource=resource,
                policy_revision=revision,
                classification=classification,
                context=context,
            )
        )

    def send(self, service: ArtifactTransferService, *, key: str) -> dict[str, Any]:
        return service.send_file(
            collaboration_scope_id=_FILE_SCOPE_ID,
            actor=self.sender,
            recipients=(self.recipient.harness_id,),
            source=self.source,
            media_type="application/octet-stream",
            classification=Classification.C1_INTERNAL,
            idempotency_key=key,
        )


def _grant_artifact_authority(
    policy: LocalConformancePolicyEngine,
    actors: tuple[Any, ...],
) -> None:
    for actor in actors:
        revision = policy.current_policy_revision(actor)
        for action in _ARTIFACT_ACTIONS:
            policy.bootstrap_entitlement_for_local_conformance(
                HumanEntitlement(
                    domain_id=actor.domain_id,
                    principal_id=actor.principal_id,
                    action=action,
                    resource_pattern="*",
                    revision=revision,
                )
            )


def _issue_exact_scope(
    scopes: CollaborationScopeService,
    policy: LocalConformancePolicyEngine,
    *,
    sender,
    recipient,
    now: int,
) -> None:
    revision = policy.current_policy_revision(sender)
    proposal = CollaborationScopeProposal(
        scope_id=_FILE_SCOPE_ID,
        scope_kind="direct",
        member_harness_ids=tuple(sorted((sender.harness_id, recipient.harness_id))),
        allowed_actions=("artifact.download", "artifact.send", "message.send"),
        allowed_resource_prefixes=("artifact:",),
        allowed_classifications=(Classification.C1_INTERNAL,),
        policy_revision=revision,
        domain_revocation_epoch=1,
    )
    resource = f"scope:{proposal.scope_id}"
    policy.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=sender.domain_id,
            principal_id=sender.principal_id,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource_pattern=resource,
            revision=revision,
        )
    )
    decision = policy.require(
        AuthorizationRequest(
            actor=sender,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=resource,
            policy_revision=revision,
            context=scopes.issuance_request(actor=sender, proposal=proposal),
        ),
        when=datetime.fromtimestamp(now, UTC),
    )
    scopes.issue(
        actor=sender,
        proposal=proposal,
        authority=IssuanceAuthority(
            actor=sender,
            policy_decision_id=decision.decision_id,
        ),
        when=datetime.fromtimestamp(now, UTC),
    )


def _make_transfer_stack(store, identity_factory, tmp_path: Path) -> TransferStack:
    sender, _ = identity_factory(kind="codex", binding_assurance="os_bound")
    recipient, _ = identity_factory(kind="server", binding_assurance="os_bound")
    sibling, _ = identity_factory(
        kind="pi",
        principal_id=sender.principal_id,
        binding_assurance="os_bound",
    )
    cross_domain, _ = identity_factory(
        domain="other.example",
        kind="server",
        binding_assurance="os_bound",
    )
    now = int(time.time())
    policy = LocalConformancePolicyEngine(store)
    _grant_artifact_authority(policy, (sender, recipient))
    scopes = CollaborationScopeService(store, clock=lambda: now)
    _issue_exact_scope(
        scopes,
        policy,
        sender=sender,
        recipient=recipient,
        now=now,
    )
    scanner_key = P256KeyPair.generate()
    artifacts = ArtifactService(
        store,
        FilesystemArtifactStore(
            tmp_path / "quarantine-objects",
            tmp_path / "secrets" / "artifacts.key",
        ),
        trusted_scanner_keys={HermeticMaintainedScanner.scanner_id: scanner_key.public_pem},
    )
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    conversations = ConversationService(
        store,
        policy,
        mailbox,
        collaboration_scopes=scopes,
        artifact_binding_validator=artifacts.require_released_binding,
    )
    scanner = ScannerWorker(
        artifacts,
        HermeticMaintainedScanner(scanner_key),
        clock=lambda: now,
    )
    source = tmp_path / "source.bin"
    source.write_bytes(b"Kiev artifact contract\x00bytes\n")
    return TransferStack(
        store=store,
        artifacts=artifacts,
        scanner=scanner,
        scopes=scopes,
        conversations=conversations,
        policy=policy,
        sender=sender,
        recipient=recipient,
        sibling=sibling,
        cross_domain=cross_domain,
        source=source,
        now=now,
    )


@pytest.fixture
def transfer_stack(store, identity_factory, tmp_path: Path) -> TransferStack:
    return _make_transfer_stack(store, identity_factory, tmp_path)


def test_send_releases_exact_bytes_before_recording_only_recipient_custody(
    transfer_stack: TransferStack,
) -> None:
    result = transfer_stack.send(
        transfer_stack.service(),
        key="file-send-complete-0001",
    )
    content = transfer_stack.source.read_bytes()
    digest = hashlib.sha256(content).hexdigest()

    assert result["state"] == "recipient_committed"
    assert result["expected_digest"] == digest
    assert result["expected_size"] == len(content)
    assert result["recipient_harness_ids"] == (transfer_stack.recipient.harness_id,)
    assert result["recipient_states"] == {
        transfer_stack.recipient.harness_id: "recipient_committed"
    }
    lifecycle = transfer_stack.artifacts.lifecycle_status(
        result["artifact_id"],
        actor=transfer_stack.sender,
    )
    assert lifecycle["manifest_state"] == "released"
    assert transfer_stack.artifacts.resolve_released_binding(result["artifact_id"]) is not None

    recipients = transfer_stack.store.fetch_all(
        "SELECT recipient_id,current_fact FROM recipients WHERE event_id=? ORDER BY recipient_id",
        (result["event_id"],),
    )
    assert [(row["recipient_id"], row["current_fact"]) for row in recipients] == [
        (transfer_stack.recipient.harness_id, "accepted_local")
    ]
    event = transfer_stack.store.fetch_one(
        "SELECT envelope_json FROM events WHERE event_id=?",
        (result["event_id"],),
    )
    released_artifacts = json.loads(event["envelope_json"])["released_artifacts"]
    assert [artifact["artifact_id"] for artifact in released_artifacts] == [
        result["artifact_id"]
    ]


@pytest.mark.parametrize("phase", _CRASH_PHASES)
def test_same_key_reconciliation_after_every_crash_phase_commits_one_event(
    transfer_stack: TransferStack,
    phase: str,
) -> None:
    crashed = False

    def crash_once(current: str) -> None:
        nonlocal crashed
        if current == phase and not crashed:
            crashed = True
            raise InjectedTransferCrash(phase)

    with pytest.raises(InjectedTransferCrash, match=phase):
        transfer_stack.send(
            transfer_stack.service(phase_hook=crash_once),
            key="file-send-crash-reconcile-0001",
        )

    event_before_retry = transfer_stack.store.fetch_one(
        "SELECT event_id FROM events"
    )
    assert (event_before_retry is not None) is (phase == "after_event")

    reconciled = transfer_stack.send(
        transfer_stack.service(),
        key="file-send-crash-reconcile-0001",
    )
    status = transfer_stack.service().status(
        collaboration_scope_id=_FILE_SCOPE_ID,
        actor=transfer_stack.sender,
        transfer_id=reconciled["transfer_id"],
    )

    assert reconciled["state"] == "recipient_committed"
    assert status["state"] == "recipient_committed"
    assert status["event_id"] == reconciled["event_id"]
    if event_before_retry is not None:
        assert event_before_retry["event_id"] == reconciled["event_id"]
    assert transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM events"
    )["count"] == 1
    assert transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM recipients WHERE event_id=?",
        (reconciled["event_id"],),
    )["count"] == 1


def test_same_idempotency_key_rejects_changed_file_bytes(
    transfer_stack: TransferStack,
) -> None:
    service = transfer_stack.service()
    first = transfer_stack.send(service, key="file-send-idempotency-conflict-0001")
    transfer_stack.source.write_bytes(b"Kiev artifact changed bytes\n")

    with pytest.raises(IdempotencyConflict):
        transfer_stack.send(service, key="file-send-idempotency-conflict-0001")

    assert transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM events WHERE event_id=?",
        (first["event_id"],),
    )["count"] == 1


@pytest.mark.parametrize("denied_recipient_name", ("sibling", "cross_domain"))
def test_send_denies_sibling_and_cross_domain_recipient_substitution(
    transfer_stack: TransferStack,
    denied_recipient_name: str,
) -> None:
    denied_recipient = getattr(transfer_stack, denied_recipient_name)

    with pytest.raises(AuthorizationError):
        transfer_stack.service().send_file(
            collaboration_scope_id=_FILE_SCOPE_ID,
            actor=transfer_stack.sender,
            recipients=(denied_recipient.harness_id,),
            source=transfer_stack.source,
            media_type="application/octet-stream",
            classification=Classification.C1_INTERNAL,
            idempotency_key=f"file-send-denied-{denied_recipient_name}-0001",
        )

    assert transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_transfers"
    )["count"] == 0
    assert transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM events"
    )["count"] == 0


@pytest.mark.parametrize("denied_actor_name", ("sibling", "cross_domain"))
def test_released_file_denies_sibling_and_cross_domain_download(
    transfer_stack: TransferStack,
    tmp_path: Path,
    denied_actor_name: str,
) -> None:
    download_root = tmp_path / "denied-downloads"
    download_root.mkdir(mode=0o700)
    service = transfer_stack.service(
        destination=SafeDownloadDestination(download_root)
    )
    sent = transfer_stack.send(service, key="file-send-exact-recipient-0001")
    denied_actor = getattr(transfer_stack, denied_actor_name)
    destination = download_root / f"denied-{denied_actor_name}.bin"

    with pytest.raises(AuthorizationError):
        service.download_file(
            collaboration_scope_id=_FILE_SCOPE_ID,
            actor=denied_actor,
            artifact_id=sent["artifact_id"],
            destination=destination,
            idempotency_key=f"file-download-denied-{denied_actor_name}-0001",
        )

    assert not destination.exists()
    assert transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM download_capabilities WHERE consumed_at IS NULL"
    )["count"] == 0
