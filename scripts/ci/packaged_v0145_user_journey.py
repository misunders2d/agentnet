#!/usr/bin/env python3
"""Hermetic installed-package v0.1.45 user-journey evidence.

The full journey composes the existing separate-process communication and exact
endpoint runners, then exercises the strict invitation, lifecycle, artifact,
and safe-download services in one private disposable store.  Its stdout is the
content-free release report only.  The portable mode intentionally proves only
contracts available on every supported CI host and emits a separately scoped
report that makes no PostgreSQL, ClamAV, systemd, or real-browser claim.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Literal
from urllib.parse import urlsplit

import agentnet
from agentnet.adapters import BUILTIN_ADAPTERS
from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.artifacts.local_destination import SafeDownloadDestination
from agentnet.artifacts.scanner import ArtifactScanAttestationV1, ScannerTrustPolicy
from agentnet.artifacts.service import ArtifactService, FilesystemArtifactStore
from agentnet.artifacts.transfer import ArtifactTransferService
from agentnet.authorization import (
    AuthorizationRequest,
    HumanEntitlement,
    IssuanceAuthority,
    LocalConformancePolicyEngine,
)
from agentnet.authorization.communication_scope_service import (
    COLLABORATION_SCOPE_ISSUE_ACTION,
    CollaborationScopeProposal,
    CollaborationScopeService,
)
from agentnet.bindings.mcp_bootstrap import MCP_BOOTSTRAP_ASSURANCE
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES
from agentnet.errors import AuthenticationError, AuthorizationError, GateBlocked
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.enrollment import VerifiedOIDCIdentity
from agentnet.identity.invitation_links import (
    INVITATION_LINK_ISSUE_ACTION,
    InvitationLinkService,
    InvitationOffer,
)
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_ISSUE_ACTION,
    INTERNAL_INVITATION_POP_PURPOSE,
    INVITATION_REDEMPTION_APPROVAL_PURPOSE,
    InternalInvitationRequest,
    InternalInvitationService,
    InvitationRedemptionEvidence,
    InvitationRedemptionService,
)
from agentnet.identity.oidc import OIDCVerificationResult
from agentnet.mailbox.service import MailboxService
from agentnet.operations.endpoint_lifecycle import EndpointActivationState, EndpointLifecycleService
from agentnet.protocol.models import Classification
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.storage.sqlite import SQLiteStore
from agentnet.supervisor.scanner_worker import ScannerWorker


_SCHEMA = "agentnet.v0145-user-journey.v1"
_PORTABLE_SCHEMA = "agentnet.v0145-portable-contracts.v1"
_DOMAIN_ID = "packaged-v0145.example"
_SCOPE_ID = "scope:packaged-v0145-journey"
_INVITATION_ID = "invitation:packaged-v0145-journey"
_SOURCE_FINGERPRINT = hashlib.sha256(b"packaged-v0145-invitation-source").hexdigest()
_TOOLS = ["omp", "pi", "claude", "codex", "antigravity"]
_REQUIRED_ADAPTERS = frozenset(_TOOLS)
_SCOPE_ACTIONS = ("artifact.download", "artifact.send", "message.read", "message.send")
_FILE_POLICY_ACTIONS = (
    "artifact.download",
    "artifact.manifest.promote",
    "artifact.release",
    "artifact.upload.bytes",
    "artifact.upload.reserve",
)
_EXPECTED_REPORT: dict[str, object] = {
    "schema": _SCHEMA,
    "clean_install": True,
    "upgrade_preserved_state": True,
    "invitation_redeemed": True,
    "explicit_restart_observed": True,
    "tools_available": _TOOLS,
    "message_state": "recipient_committed",
    "response_obligation_state": "completed",
    "file_state": "recipient_custody_recorded",
    "file_digest_match": True,
    "offline_queue_owner_exact": True,
    "sibling_reactions": 0,
    "foreground_turns_injected": 0,
    "residual_processes": 0,
}


def _private_write(path: Path, content: bytes | str | dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if isinstance(content, dict):
        value: bytes | str = json.dumps(content, sort_keys=True) + "\n"
    else:
        value = content
    if isinstance(value, bytes):
        path.write_bytes(value)
    else:
        path.write_text(value, encoding="utf-8")
    if os.name != "nt":
        path.chmod(0o600)


def _assert_installed(package_root: Path) -> None:
    package_root = package_root.resolve()
    module_path = Path(agentnet.__file__).resolve()
    script_path = Path(__file__).resolve()
    if agentnet.__version__ != "0.1.45" or CURRENT_SCHEMA_VERSION != 7:
        raise RuntimeError("the installed release version or schema version is not v0.1.45")
    if not module_path.is_relative_to(package_root):
        raise RuntimeError("AgentNet did not resolve from the selected installed package")
    if script_path != package_root / "scripts" / "ci" / script_path.name:
        raise RuntimeError("v0.1.45 journey did not execute from installed package bytes")
    if "node_modules" not in package_root.parts:
        raise RuntimeError("v0.1.45 journey package root is not an npm installation")
    if set(BUILTIN_ADAPTERS) != _REQUIRED_ADAPTERS:
        raise RuntimeError("the installed adapter registry is not canonical")
    for harness, provider in BUILTIN_ADAPTERS.items():
        if (
            provider.manifest.adapter_id != harness
            or provider.manifest.harness != harness
            or tuple(provider.canonical_tool_names) != tuple(CANONICAL_TOOL_NAMES)
            or provider.capabilities.foreground_message_methods
            or provider.capabilities.holds_credentials is not False
        ):
            raise RuntimeError("an installed adapter registration crossed its strict boundary")


def _assert_portable_contracts(package_root: Path) -> dict[str, object]:
    _assert_installed(package_root)
    if tuple(CANONICAL_TOOL_NAMES) != tuple(dict.fromkeys(CANONICAL_TOOL_NAMES)):
        raise RuntimeError("the installed canonical tool registry contains duplicates")
    if not CANONICAL_TOOL_NAMES or not all(name.startswith("agentnet.") for name in CANONICAL_TOOL_NAMES):
        raise RuntimeError("the installed canonical tool registry is invalid")
    if MCP_BOOTSTRAP_ASSURANCE != "server_derived_account_process_parent_module":
        raise RuntimeError("the installed MCP bootstrap assurance contract changed")
    if EndpointActivationState.RESTART_REQUIRED.value != "restart_required":
        raise RuntimeError("the installed explicit restart state contract changed")
    schema_root = package_root / "schemas" / "v1"
    schema_files = sorted(schema_root.glob("*.json"))
    if not schema_files:
        raise RuntimeError("the installed strict schema catalog is absent")
    for schema_file in schema_files:
        value = json.loads(schema_file.read_text(encoding="utf-8"))
        if not isinstance(value, dict) or value.get("additionalProperties") is not False:
            raise RuntimeError("an installed public schema is not fail-closed")
    return {
        "schema": _PORTABLE_SCHEMA,
        "installed_package": True,
        "package_version": "0.1.45",
        "schema_version": 7,
        "strict_schema_catalog": True,
        "mcp_bootstrap": True,
        "restart_state": "restart_required",
        "tools_available": _TOOLS,
        "external_host_evidence": False,
    }


def _run_json(command: list[str], *, cwd: Path, environment: dict[str, str]) -> dict[str, Any]:
    completed = subprocess.run(
        command,
        cwd=cwd,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=300,
        check=False,
        text=True,
    )
    if completed.returncode != 0:
        raise RuntimeError("a composed installed-package journey failed")
    try:
        value = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError("a composed installed-package journey returned non-JSON output") from exc
    if not isinstance(value, dict):
        raise RuntimeError("a composed installed-package journey returned an invalid report")
    return value


def _run_existing_journeys(
    *, package_root: Path, launcher: Path, root: Path, environment: dict[str, str]
) -> tuple[dict[str, Any], dict[str, Any]]:
    communication_workspace = root / "communication-workspace"
    routing_workspace = root / "routing-workspace"
    communication_workspace.mkdir(mode=0o700)
    routing_workspace.mkdir(mode=0o700)
    communication = _run_json(
        [
            sys.executable,
            "-B",
            "-I",
            str(package_root / "scripts" / "ci" / "packaged_local_communication_e2e.py"),
            "run",
            "--package-root",
            str(package_root),
            "--launcher",
            str(launcher),
            "--workspace",
            str(communication_workspace),
        ],
        cwd=communication_workspace,
        environment=environment,
    )
    routing = _run_json(
        [
            sys.executable,
            "-B",
            "-I",
            str(package_root / "scripts" / "ci" / "exact_endpoint_routing_e2e.py"),
            "run",
            "--package-root",
            str(package_root),
            "--runtime-root",
            str(root / "routing-runtime"),
            "--workspace",
            str(routing_workspace),
        ],
        cwd=routing_workspace,
        environment=environment,
    )
    if any(communication_workspace.iterdir()) or any(routing_workspace.iterdir()):
        raise RuntimeError("a composed installed-package journey left workspace state")
    if (
        communication.get("recipient_fact") != "recipient_committed"
        or communication.get("obligation_state") != "completed"
        or communication.get("idempotency") is not True
        or communication.get("core_restarts") != 3
    ):
        raise RuntimeError("the installed communication journey did not prove its strict contract")
    if (
        routing.get("processing_harness_id") != routing.get("target_harness_id")
        or routing.get("offline_queue_owner") != routing.get("target_harness_id")
        or routing.get("offline_processing_harness_ids") != []
        or routing.get("sibling_reactions") != 0
        or routing.get("endpoint_processes_remaining") != 0
        or routing.get("capability_roots_remaining") != 0
    ):
        raise RuntimeError("the installed exact-endpoint journey did not prove exclusive custody")
    return communication, routing


def _fixture_actor(store: SQLiteStore, *, now: int) -> tuple[VerifiedActor, P256KeyPair]:
    key = P256KeyPair.generate()
    principal_id = "principal:packaged-v0145-owner"
    harness_id = "harness:omp:packaged-v0145-owner"
    credential_id = "credential:packaged-v0145-owner"
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO domains(domain_id,status,created_at) VALUES(?,'active',?)",
            (_DOMAIN_ID, now),
        )
        connection.execute(
            """INSERT INTO principals(
                   principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
               ) VALUES(?,?,?,?,?,'active',?)""",
            (
                principal_id,
                _DOMAIN_ID,
                "https://id.packaged-v0145.example",
                "owner-subject",
                "owner@packaged-v0145.example",
                now,
            ),
        )
        connection.execute(
            """INSERT INTO harnesses(
                   harness_id,domain_id,principal_id,kind,display_name,status,
                   binding_assurance,capabilities_json,created_at
               ) VALUES(?,?,?,?,?,'active','hardware_bound',?,?)""",
            (harness_id, _DOMAIN_ID, principal_id, "omp", "Packaged Journey Owner", "{}", now),
        )
        connection.execute(
            """INSERT INTO credentials(
                   credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
               ) VALUES(?,?,?,?,'active',1,?,?)""",
            (credential_id, harness_id, key.thumbprint, key.public_pem, now - 1, now + 86_400),
        )
    return (
        VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=_DOMAIN_ID,
            principal_id=principal_id,
            harness_id=harness_id,
            credential_id=credential_id,
            credential_epoch=1,
            binding_assurance="hardware_bound",
        ),
        key,
    )


class _Authority:
    def __init__(self, store: SQLiteStore, now: int) -> None:
        self.engine = LocalConformancePolicyEngine(store)
        self.now = now
        self._granted: set[tuple[str, str]] = set()

    def _grant(self, actor: VerifiedActor, action: str) -> None:
        key = (str(actor.principal_id), action)
        if key in self._granted:
            return
        assert actor.principal_id is not None
        self.engine.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern="*",
                revision=1,
                expires_at=datetime.fromtimestamp(self.now, UTC) + timedelta(days=1),
            ),
            when=datetime.fromtimestamp(self.now, UTC),
        )
        self._granted.add(key)

    def decision(
        self,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        context: dict[str, Any] | None = None,
        classification: Classification | None = None,
    ) -> Any:
        self._grant(actor, action)
        return self.engine.require(
            AuthorizationRequest(
                actor=actor,
                action=action,
                resource=resource,
                classification=classification,
                policy_revision=1,
                context=context or {},
            ),
            when=datetime.fromtimestamp(self.now, UTC),
        )

    def issuance(
        self, *, actor: VerifiedActor, action: str, resource: str, context: dict[str, Any]
    ) -> IssuanceAuthority:
        decision = self.decision(
            actor=actor,
            action=action,
            resource=resource,
            context=context,
        )
        return IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id)


@dataclass(frozen=True, slots=True)
class _SyntheticOIDCVerifier:
    result: OIDCVerificationResult
    verifier_id: str = "packaged-v0145-oidc"

    def verify_invitation_identity(
        self,
        *,
        canonical_invitation: object,
        evidence: dict[str, object],
        expected_issuer: str,
        when: datetime,
    ) -> OIDCVerificationResult:
        del canonical_invitation
        if (
            evidence != {"proof": "synthetic-work-account"}
            or expected_issuer != self.result.identity.issuer
            or int(when.timestamp()) >= self.result.expires_at
        ):
            raise AuthenticationError("work account sign-in was not accepted")
        return self.result


@dataclass(frozen=True, slots=True)
class _DeterministicLocalScanner:
    """Issue hermetic signed evidence; never represents external ClamAV proof."""
    key: P256KeyPair
    store: SQLiteStore
    scanner_id: str = "packaged-v0145-deterministic-scanner"
    scanner_engine: str = "packaged-v0145-local-engine"
    rules_digest: str = hashlib.sha256(b"packaged-v0145-local-rules").hexdigest()
    profile_digest: str = hashlib.sha256(b"packaged-v0145-local-profile").hexdigest()

    def scan(
        self,
        *,
        artifact_id: str,
        classification: Literal["C0", "C1", "C2", "C3"],
        ciphertext_digest: str,
        object_key: str,
        object_version: str,
        plaintext_digest: str,
        policy_revision: int,
        content: bytes,
        issued_at: int,
        expires_at: int,
    ) -> ArtifactScanAttestationV1:
        result = "deny" if b"EICAR" in content or b"PRIVATE KEY" in content else "allow"
        fields: dict[str, object] = {
            "artifact_id": artifact_id,
            "classification": classification,
            "ciphertext_digest": ciphertext_digest,
            "expires_at": expires_at,
            "issued_at": issued_at,
            "object_key": object_key,
            "object_version": object_version,
            "plaintext_digest": plaintext_digest,
            "policy_revision": policy_revision,
            "profile_digest": self.profile_digest,
            "result": result,
            "rules_digest": self.rules_digest,
            "scanner_engine": self.scanner_engine,
            "scanner_id": self.scanner_id,
            "scanner_key_epoch": 1,
            "scanner_version": "1",
        }
        return ArtifactScanAttestationV1(
            **fields,
            signature=self.key.sign("agentnet.artifact.attestation.v1", fields),
        )


def _issue_scope(
    *,
    scopes: CollaborationScopeService,
    authority: _Authority,
    owner: VerifiedActor,
) -> CollaborationScopeProposal:
    proposal = CollaborationScopeProposal(
        scope_id=_SCOPE_ID,
        scope_kind="shared",
        member_harness_ids=(str(owner.harness_id),),
        allowed_actions=_SCOPE_ACTIONS,
        allowed_resource_prefixes=("artifact:", "conversation:"),
        allowed_classifications=(Classification.C1_INTERNAL,),
        canonical_references=("project:packaged-v0145",),
        policy_revision=1,
        domain_revocation_epoch=1,
        expires_at=None,
    )
    context = scopes.issuance_request(actor=owner, proposal=proposal)
    issued = scopes.issue(
        actor=owner,
        proposal=proposal,
        authority=authority.issuance(
            actor=owner,
            action=COLLABORATION_SCOPE_ISSUE_ACTION,
            resource=f"scope:{_SCOPE_ID}",
            context=context,
        ),
        when=datetime.fromtimestamp(authority.now, UTC),
    )
    if issued.scope_id != _SCOPE_ID or issued.member_harness_ids != (owner.harness_id,):
        raise RuntimeError("the strict collaboration scope was not issued exactly")
    return proposal


def _redeem_invitation(
    *,
    store: SQLiteStore,
    scopes: CollaborationScopeService,
    lifecycle: EndpointLifecycleService,
    authority: _Authority,
    owner: VerifiedActor,
    proposal: CollaborationScopeProposal,
    now: int,
) -> VerifiedActor:
    links = InvitationLinkService(
        store,
        public_base_url="https://join.packaged-v0145.example/join",
        clock=lambda: now,
    )
    offer = InvitationOffer(
        invitation_id=_INVITATION_ID,
        invited_verified_email="candidate@packaged-v0145.example",
        domain_id=_DOMAIN_ID,
        collaboration_scope_template=proposal,
        permission_actions=_SCOPE_ACTIONS,
        expires_at=now + 86_400,
    )
    resource, context = links.authority_binding(offer, action=INVITATION_LINK_ISSUE_ACTION)
    issued = links.issue(
        actor=owner,
        offer=offer,
        authority=authority.issuance(
            actor=owner,
            action=INVITATION_LINK_ISSUE_ACTION,
            resource=resource,
            context=context,
        ),
    )
    opaque_token = urlsplit(str(issued.public_url)).path.rsplit("/", 1)[-1]
    reservation = links.reserve_redemption(
        opaque_token=opaque_token,
        source_fingerprint=_SOURCE_FINGERPRINT,
    )

    candidate_key = P256KeyPair.generate()
    oidc_result = OIDCVerificationResult(
        identity=VerifiedOIDCIdentity(
            issuer="https://id.packaged-v0145.example",
            subject="candidate-subject",
            verified_email=offer.invited_verified_email,
        ),
        id_token_hash=hashlib.sha256(b"packaged-v0145-work-account").hexdigest(),
        expires_at=now + 1_800,
    )
    oidc = _SyntheticOIDCVerifier(oidc_result)
    internal = InternalInvitationService(store, oidc_verifier=oidc, clock=lambda: now)
    request = InternalInvitationRequest(
        invitation_id=offer.invitation_id,
        domain_id=offer.domain_id,
        invited_oidc_issuer=oidc_result.identity.issuer,
        invited_oidc_subject=oidc_result.identity.subject,
        invited_verified_email=offer.invited_verified_email,
        candidate_harness_id="harness:codex:packaged-v0145-candidate",
        candidate_harness_kind="codex",
        candidate_harness_display_name="Packaged Journey Candidate",
        candidate_binding_assurance="os_bound",
        candidate_key_id=candidate_key.thumbprint,
        candidate_public_key_pem=candidate_key.public_pem,
        requested_capabilities=(),
        expires_at=datetime.fromtimestamp(offer.expires_at, UTC),
        reason="packaged v0.1.45 collaboration invitation",
    )
    internal_resource, internal_context = internal.issuance_binding(request)
    record = internal.issue(
        request,
        authority=authority.issuance(
            actor=owner,
            action=INTERNAL_INVITATION_ISSUE_ACTION,
            resource=internal_resource,
            context=internal_context,
        ),
        when=datetime.fromtimestamp(now, UTC),
    )
    possession = candidate_key.sign(
        INTERNAL_INVITATION_POP_PURPOSE,
        internal.candidate_possession_fields(record.transaction, oidc_result),
    )
    evidence = InvitationRedemptionEvidence(
        reservation=reservation,
        canonical_internal_invitation=canonical_json(
            record.transaction.model_dump(mode="json")
        ).decode(),
        oidc_evidence={"proof": "synthetic-work-account"},
        candidate_possession_signature=possession,
        selected_scope_id=_SCOPE_ID,
        permission_actions=_SCOPE_ACTIONS,
    )
    approver_key = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=str(owner.principal_id),
        domain_id=owner.domain_id,
        signer_key_id=approver_key.thumbprint,
        public_key_pem=approver_key.public_pem,
        allowed_purposes=frozenset({INVITATION_REDEMPTION_APPROVAL_PURPOSE}),
    )
    redemption = InvitationRedemptionService(
        store,
        invitation_links=links,
        internal_invitations=internal,
        approval_verifier=IndependentApprovalVerifier(
            {approver.signer_key_id: approver},
            verifier_id="packaged-v0145-passkey",
        ),
        clock=lambda: now,
    )
    challenge = redemption.prepare(evidence, source_fingerprint=_SOURCE_FINGERPRINT)
    approval = create_independent_approval_receipt(
        approver_key,
        approver=approver,
        verifier_id="packaged-v0145-passkey",
        approval_purpose=INVITATION_REDEMPTION_APPROVAL_PURPOSE,
        canonical_transaction=challenge.canonical_transaction.encode(),
        issued_at=now,
        expires_at=now + 300,
    )
    result = redemption.redeem(
        evidence,
        approval=approval,
        source_fingerprint=_SOURCE_FINGERPRINT,
    )
    if (
        result.endpoint_state != "restart_required"
        or result.restart_required is not True
        or result.scope_id != _SCOPE_ID
        or result.positive_entitlements != _SCOPE_ACTIONS
        or result.unrelated_entitlements_issued != 0
    ):
        raise RuntimeError("invitation redemption did not preserve the strict authority boundary")
    redemption_counts = (
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM collaboration_scope_members WHERE scope_id=? AND harness_id=?",
            (_SCOPE_ID, result.harness_id),
        )["count"],
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM credentials WHERE harness_id=?",
            (result.harness_id,),
        )["count"],
    )
    try:
        redemption.redeem(
            evidence,
            approval=approval,
            source_fingerprint=_SOURCE_FINGERPRINT,
        )
    except AuthenticationError:
        pass
    else:
        raise RuntimeError("a consumed invitation was redeemable twice")
    repeated_counts = (
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM collaboration_scope_members WHERE scope_id=? AND harness_id=?",
            (_SCOPE_ID, result.harness_id),
        )["count"],
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM credentials WHERE harness_id=?",
            (result.harness_id,),
        )["count"],
    )
    if redemption_counts != (1, 1) or repeated_counts != redemption_counts:
        raise RuntimeError("invitation replay changed exact endpoint authority state")

    restarted = lifecycle.record_user_restart(
        actor=result.actor,
        expected_generation=1,
        process_measurement=hashlib.sha256(b"packaged-v0145-explicit-restart").hexdigest(),
    )
    repeated = lifecycle.record_user_restart(
        actor=result.actor,
        expected_generation=1,
        process_measurement=str(restarted.process_measurement),
    )
    if (
        restarted.state is not EndpointActivationState.CONNECTED
        or restarted.adapter_generation != 2
        or repeated != restarted
    ):
        raise RuntimeError("explicit user restart did not converge idempotently")
    if scopes.get_for_actor(actor=result.actor, scope_id=_SCOPE_ID).scope_id != _SCOPE_ID:
        raise RuntimeError("redeemed exact endpoint did not receive only the selected scope")
    return result.actor


def _exercise_files(
    *,
    root: Path,
    store: SQLiteStore,
    scopes: CollaborationScopeService,
    authority: _Authority,
    sender: VerifiedActor,
    recipient: VerifiedActor,
    now: int,
) -> bool:
    scanner_key = P256KeyPair.generate()
    maintained = _DeterministicLocalScanner(scanner_key, store)
    artifacts = ArtifactService(
        store,
        FilesystemArtifactStore(root / "objects", root / "artifact-secrets" / "artifact.key"),
        trusted_scanner_keys={maintained.scanner_id: scanner_key.public_pem},
        scanner_policy=ScannerTrustPolicy(
            required_engine=maintained.scanner_engine,
            required_rules_digest=maintained.rules_digest,
            required_profile_digest=maintained.profile_digest,
        ),
        clock=lambda: now,
    )
    mailbox = MailboxService(store, collaboration_scopes=scopes)
    scanner = ScannerWorker(artifacts, maintained, clock=lambda: now)
    downloads = root / "downloads"
    downloads.mkdir(mode=0o700)
    for actor in (sender, recipient):
        for action in _FILE_POLICY_ACTIONS:
            authority._grant(actor, action)

    def authorize(**request: Any) -> Any:
        return authority.decision(**request)

    transfer = ArtifactTransferService(
        store,
        artifacts,
        scanner,
        scopes,
        SimpleNamespace(store=store, mailbox=mailbox),
        authorize=authorize,
        destination=SafeDownloadDestination(downloads),
        clock=lambda: now,
    )
    source = root / "safe-source.txt"
    content = b"Synthetic packaged v0.1.45 file journey.\n"
    _private_write(source, content)
    expected_digest = hashlib.sha256(content).hexdigest()
    request = {
        "actor": sender,
        "collaboration_scope_id": _SCOPE_ID,
        "recipients": (str(recipient.harness_id),),
        "source": source,
        "media_type": "text/plain",
        "classification": Classification.C1_INTERNAL,
        "idempotency_key": "packaged-v0145-safe-file-0001",
    }
    sent = transfer.send_file(**request)
    repeated_send = transfer.send_file(**request)
    recipient_states = sent.get("recipient_states") or {}
    if (
        sent.get("state") != "recipient_committed"
        or recipient_states != {recipient.harness_id: "recipient_committed"}
        or repeated_send.get("duplicate") is not True
        or repeated_send.get("artifact_id") != sent.get("artifact_id")
        or sent.get("expected_digest") != expected_digest
    ):
        raise RuntimeError("safe file did not reach exact recipient custody idempotently")
    safe_counts = (
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM artifact_transfers WHERE transfer_id=?",
            (sent["transfer_id"],),
        )["count"],
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM artifact_manifests WHERE artifact_id=? AND state='released'",
            (sent["artifact_id"],),
        )["count"],
        store.fetch_one(
            "SELECT COUNT(*) AS count FROM events WHERE event_id=?",
            (sent["event_id"],),
        )["count"],
    )
    if safe_counts != (1, 1, 1):
        raise RuntimeError("safe file retry created duplicate artifact or event state")

    destination = downloads / "received.txt"
    download_request = {
        "actor": recipient,
        "collaboration_scope_id": _SCOPE_ID,
        "artifact_id": str(sent["artifact_id"]),
        "destination": destination,
        "idempotency_key": "packaged-v0145-download-0001",
    }
    downloaded = transfer.download_file(**download_request)
    repeated_download = transfer.download_file(**download_request)
    if (
        downloaded.get("state") != "materialized"
        or downloaded.get("plaintext_digest") != expected_digest
        or repeated_download.get("duplicate") is not True
        or hashlib.sha256(destination.read_bytes()).hexdigest() != expected_digest
    ):
        raise RuntimeError("safe file destination bytes did not match immutable release")
    download_count = store.fetch_one(
        """SELECT COUNT(*) AS count FROM audit_intents
             WHERE action='artifact.download.materialize' AND resource_id=? AND state='completed'""",
        (sent["artifact_id"],),
    )["count"]
    if download_count != 1:
        raise RuntimeError("safe file retry created duplicate download custody")

    released_before = store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_manifests WHERE state='released'"
    )["count"]
    events_before = store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"]
    blocked = (
        (
            "blocked-eicar.txt",
            br"X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*",
            "packaged-v0145-eicar-file-0001",
        ),
        (
            "blocked-secret.txt",
            b"api_key=synthetic-do-not-release-value",
            "packaged-v0145-secret-file-0001",
        ),
    )
    for filename, bytes_, idempotency_key in blocked:
        blocked_path = root / filename
        _private_write(blocked_path, bytes_)
        try:
            transfer.send_file(
                actor=sender,
                collaboration_scope_id=_SCOPE_ID,
                recipients=(str(recipient.harness_id),),
                source=blocked_path,
                media_type="text/plain",
                classification=Classification.C1_INTERNAL,
                idempotency_key=idempotency_key,
            )
        except (AuthorizationError, GateBlocked):
            pass
        else:
            raise RuntimeError("blocked file crossed the release boundary")
    released_after = store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_manifests WHERE state='released'"
    )["count"]
    events_after = store.fetch_one("SELECT COUNT(*) AS count FROM events")["count"]
    denied_reservations = store.fetch_one(
        "SELECT COUNT(*) AS count FROM artifact_reservations WHERE state='prefilter_denied'"
    )["count"]
    if (
        released_after != released_before
        or events_after != events_before
        or denied_reservations != 2
    ):
        raise RuntimeError("blocked file produced recipient-visible or non-rejected state")
    return True


def _assert_upgrade_state(path: Path) -> None:
    value = json.loads(path.read_text(encoding="utf-8"))
    installed_digest = value.get("installed_package_sha256") if isinstance(value, dict) else None
    if (
        not isinstance(value, dict)
        or set(value) != {"schema", "state_digest", "installed_package_sha256"}
        or value["schema"] != "agentnet.v0145-packed-upgrade-state.v1"
        or value["state_digest"]
        != hashlib.sha256(b"packaged-v0145-preserved-state").hexdigest()
        or not isinstance(installed_digest, str)
        or len(installed_digest) != 64
        or any(character not in "0123456789abcdef" for character in installed_digest)
    ):
        raise RuntimeError("the packed installation did not preserve exact prior state")


def _run_full(args: argparse.Namespace) -> dict[str, object]:
    package_root = Path(args.package_root).resolve()
    launcher = Path(args.launcher).resolve()
    workspace = Path(args.workspace).resolve()
    upgrade_state = Path(args.upgrade_state).resolve()
    _assert_installed(package_root)
    if not launcher.is_relative_to(package_root):
        raise RuntimeError("the v0.1.45 journey launcher is not installed package bytes")
    _assert_upgrade_state(upgrade_state)
    if workspace.exists() and (workspace.is_symlink() or any(workspace.iterdir())):
        raise RuntimeError("the v0.1.45 journey workspace is not empty")
    workspace.mkdir(parents=True, exist_ok=True, mode=0o700)
    root = workspace / "packaged-v0145-user-journey"
    root.mkdir(mode=0o700)
    environment = dict(os.environ)
    environment.update(
        {
            "HOME": str(root / "home"),
            "NO_PROXY": "127.0.0.1,localhost",
            "PYTHONDONTWRITEBYTECODE": "1",
            "no_proxy": "127.0.0.1,localhost",
        }
    )
    for name in (
        "ALL_PROXY",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "PYTHONHOME",
        "PYTHONPATH",
        "VIRTUAL_ENV",
        "all_proxy",
        "http_proxy",
        "https_proxy",
    ):
        environment.pop(name, None)
    (root / "home").mkdir(mode=0o700)
    store: SQLiteStore | None = None
    try:
        communication, routing = _run_existing_journeys(
            package_root=package_root,
            launcher=launcher,
            root=root,
            environment=environment,
        )
        now = int(time.time())
        cipher = LocalEnvelopeCipher.from_key_file(root / "store-secrets" / "local.key")
        store = SQLiteStore(root / "journey.sqlite3", cipher)
        owner, _owner_key = _fixture_actor(store, now=now)
        authority = _Authority(store, now)
        scopes = CollaborationScopeService(store, clock=lambda: now)
        lifecycle = EndpointLifecycleService(store, clock=lambda: now)
        proposal = _issue_scope(scopes=scopes, authority=authority, owner=owner)
        recipient = _redeem_invitation(
            store=store,
            scopes=scopes,
            lifecycle=lifecycle,
            authority=authority,
            owner=owner,
            proposal=proposal,
            now=now,
        )
        digest_match = _exercise_files(
            root=root,
            store=store,
            scopes=scopes,
            authority=authority,
            sender=owner,
            recipient=recipient,
            now=now,
        )
        report = {
            "schema": _SCHEMA,
            "clean_install": True,
            "upgrade_preserved_state": True,
            "invitation_redeemed": True,
            "explicit_restart_observed": True,
            "tools_available": _TOOLS,
            "message_state": communication["recipient_fact"],
            "response_obligation_state": communication["obligation_state"],
            "file_state": "recipient_custody_recorded",
            "file_digest_match": digest_match,
            "offline_queue_owner_exact": (
                routing["offline_queue_owner"] == routing["target_harness_id"]
            ),
            "sibling_reactions": routing["sibling_reactions"],
            "foreground_turns_injected": 0,
            "residual_processes": routing["endpoint_processes_remaining"],
        }
        if report != _EXPECTED_REPORT:
            raise RuntimeError("the v0.1.45 journey report did not match its frozen schema")
        return report
    finally:
        try:
            if store is not None:
                store.close()
        finally:
            shutil.rmtree(root, ignore_errors=False)
        if root.exists() or any(workspace.iterdir()):
            raise RuntimeError("the v0.1.45 journey left residual state")


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    portable = subparsers.add_parser("portable")
    portable.add_argument("--package-root", required=True)
    full = subparsers.add_parser("run")
    full.add_argument("--package-root", required=True)
    full.add_argument("--launcher", required=True)
    full.add_argument("--workspace", required=True)
    full.add_argument("--upgrade-state", required=True)
    return parser


def main() -> int:
    args = _parser().parse_args()
    if args.command == "portable":
        report = _assert_portable_contracts(Path(args.package_root))
    else:
        report = _run_full(args)
    print(json.dumps(report, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(
            json.dumps(
                {
                    "schema": "agentnet.v0145-user-journey-diagnostic.v1",
                    "error_type": type(exc).__name__,
                },
                separators=(",", ":"),
            ),
            file=sys.stderr,
        )
        raise SystemExit(1)
