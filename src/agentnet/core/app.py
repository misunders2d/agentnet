"""Composition root for the self-hosted conformance kernel."""

from __future__ import annotations

import time
from collections.abc import Mapping
from dataclasses import asdict
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
from uuid import NAMESPACE_URL, uuid4, uuid5

from agentnet.approval.service import IndependentApprovalVerifier, TrustedApprover
from agentnet.artifacts.service import ArtifactService, FilesystemArtifactStore
from agentnet.artifacts.scanner import ScannerTrustPolicy
from agentnet.attention.policy import AttentionService
from agentnet.automation import AutomationCharterService
from agentnet.audit.service import AuditService
from agentnet.authorization.elevation import ElevationService
from agentnet.authorization.grants import GrantUse
from agentnet.authorization.evidence import IssuanceAuthority, SignedAuthorityCommand
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
    OperationClass,
    PolicyEngine,
)
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.discovery.directory import DirectoryService
from agentnet.effects.reservations import (
    EffectExecutionEvidence,
    EffectReconciliationEvidence,
    EffectReservations,
    EffectState,
    EffectTerminalEvidence,
    EffectTransitionProof,
    EffectUncertaintyEvidence,
)
from agentnet.errors import (
    AuthenticationError,
    AuthorizationError,
    ExtensionError,
    GateBlocked,
    ValidationError,
)
from agentnet.federation.service import FederationService
from agentnet.identity.actors import ActorKind, TrustedTransportContext, VerifiedActor
from agentnet.identity.context import VerifiedContextResolver
from agentnet.identity.credentials import (
    CredentialRotationRequest,
    CredentialRotationResult,
    CredentialRotationService,
    load_credential_binding,
    public_key_thumbprint,
)
from agentnet.identity.domains import DomainRegistry
from agentnet.identity.enrollment import BindingAssurance, EnrollmentService
from agentnet.identity.invitation_oidc import InternalInvitationOIDCCoordinator
from agentnet.identity.invitations import InternalInvitationService
from agentnet.identity.oidc import (
    OIDCEnrollmentCoordinator,
    OIDCProvider,
    OIDCProviderConfig,
)
from agentnet.identity.recovery import CredentialRecoveryService
from agentnet.identity.workload import WorkloadRegistry
from agentnet.interfaces.contracts import ApprovalVerifier
from agentnet.mailbox.service import MailboxService
from agentnet.messaging.conversation import ConversationService
from agentnet.messaging.events import new_event
from agentnet.messaging.obligation import ResponseObligationService
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.operations.authority_inspection import AuthorityInspectionService
from agentnet.operations.incident import DomainIncidentService
from agentnet.operations.outage import HealthProvider, OutageGate
from agentnet.operations.quotas import QuotaService
from agentnet.operations.telemetry import Telemetry
from agentnet.operations.versioning import (
    CompatibilityRequirement,
    DigestIdempotentReplayHandler,
    VersioningService,
    VersionWindow,
)
from agentnet.organization.assignment import AssignmentRequest, AssignmentService
from agentnet.organization.relationships import (
    RelationshipGovernanceRecord,
    RelationshipPolicyException,
    RelationshipPolicyExceptionRecord,
    RelationshipService,
)
from agentnet.presence.service import PresenceService
from agentnet.privacy.classes import ConfidentialityEnforcer
from agentnet.provenance import ProvenanceService
from agentnet.protocol.models import (
    Classification,
    DeliveryFact,
    EventType,
    EventEnvelope,
    ReleasedArtifactBinding,
    Relationship,
    TaskGrant,
)
from agentnet.relay.composition import create_server_agent_relay_service
from agentnet.relay.service import ServerAgentRelayService
from agentnet.rooms.governance import RoomGovernance
from agentnet.rooms.mls import MLSProvider, ValidatedMLSAdoption
from agentnet.rooms.service import RoomService
from agentnet.security.dpop import RequestProof
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.storage.a2a_schema import require_a2a_schema
from agentnet.storage.backend import StoreBackend
from agentnet.storage.postgres import PostgreSQLStore, is_verified_postgresql_store
from agentnet.storage.recovery import probe_filesystem_artifact_recovery
from agentnet.storage.sqlite import SQLiteStore


class CommunicationCore:
    """Domain-owned application API.

    The local profile is runnable with synthetic data and emits only
    ``accepted_local``. The always-on profile also emits only
    ``accepted_local`` until independently evidenced synchronous replication,
    restore, fencing, and RPO gates exist. Runtime availability is not a
    release gate or HA certification.
    """

    def __init__(
        self,
        config: ExtensionConfig,
        store: StoreBackend,
        *,
        mls_provider: MLSProvider | None = None,
        mls_adoption: ValidatedMLSAdoption | None = None,
        operational_health_provider: HealthProvider | None = None,
        approval_verifier: IndependentApprovalVerifier | None = None,
    ) -> None:
        verified_postgresql = is_verified_postgresql_store(store)
        if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT and not verified_postgresql:
            raise GateBlocked("G09/G16", "always_on_server_agent requires the PostgreSQL durable-commit backend")
        if config.profile is RuntimeProfile.LOCAL_CONFORMANCE and verified_postgresql:
            raise GateBlocked("storage_profile", "local_conformance cannot emit a durable PostgreSQL acceptance fact")
        self.config = config
        self.store = store
        configured_approval_verifier: IndependentApprovalVerifier | None = None
        if config.oidc_enrollment is not None:
            oidc = config.oidc_enrollment
            trusted: dict[str, TrustedApprover] = {}
            for configured in oidc.trusted_approvers:
                if public_key_thumbprint(configured.public_key_pem) != configured.signer_key_id:
                    raise GateBlocked(
                        "approval_trust",
                        "independent approver public key does not match its configured identifier",
                    )
                trusted[configured.signer_key_id] = TrustedApprover(
                    principal_id=configured.principal_id,
                    domain_id=config.domain_id,
                    signer_key_id=configured.signer_key_id,
                    public_key_pem=configured.public_key_pem,
                    allowed_purposes=configured.allowed_purposes,
                    authority_kind=configured.authority_kind,
                )
            configured_approval_verifier = IndependentApprovalVerifier(
                trusted,
                verifier_id=oidc.verifier_id,
            )
        if approval_verifier is not None and configured_approval_verifier is not None:
            raise GateBlocked(
                "approval_trust",
                "configured and injected independent approval verifiers cannot be composed ambiguously",
            )
        self.approval_verifier = approval_verifier or configured_approval_verifier
        self.telemetry = Telemetry(store)
        self.incidents = DomainIncidentService(store)
        outage_kwargs = (
            {"health_provider": operational_health_provider}
            if operational_health_provider is not None
            else {}
        )
        self.outage = OutageGate(
            config.policies.outage,
            telemetry=self.telemetry,
            incident_mode_provider=lambda: self.incidents.current_mode(config.domain_id),
            **outage_kwargs,
        )
        self.versioning = VersioningService(
            store,
            host_domain_id=config.domain_id,
            protocol_window=VersionWindow(current="1.1", previous="1.0"),
            schema_profile="agentnet.v1",
            schema_hash=canonical_digest(
                {"schema_profile": "agentnet.v1", "schema_version": config.schema_version}
            ),
            features=frozenset(
                name for name, enabled in config.features.model_dump().items() if enabled
            ),
            telemetry=self.telemetry,
        )
        self.quotas = QuotaService(
            store,
            policy=config.policies.operations,
            telemetry=self.telemetry,
        )
        self.provenance = ProvenanceService(
            store,
            evidence_verifier=self.approval_verifier,
        )
        self.mailboxes = MailboxService(
            store,
            revocation_policy=config.policies.revocation,
            admission=self.quotas,
            provenance=self.provenance,
        )
        self.workloads = WorkloadRegistry(store)
        self.authority_inspection = AuthorityInspectionService(store)
        self.automation = AutomationCharterService(
            store,
            approval_verifier=self.approval_verifier,
            outage_gate=self.outage,
        )
        if config.profile is RuntimeProfile.LOCAL_CONFORMANCE:
            self.policy = LocalConformancePolicyEngine(
                store,
                attenuation_policy=config.policies.attenuation,
                outage_gate=self.outage,
            )
        else:
            self.policy = PolicyEngine(
                store,
                attenuation_policy=config.policies.attenuation,
                outage_gate=self.outage,
                runtime_profile=config.profile,
            )
        self.grants = self.policy.grants
        self.assignments = AssignmentService(
            store,
            mailbox=self.mailboxes,
            policy=self.policy,
            approval_verifier=self.approval_verifier,
            attenuation_policy=config.policies.attenuation,
            outage_gate=self.outage,
        )
        self.response_obligations = ResponseObligationService(store, self.policy)
        self.conversations = ConversationService(
            store,
            self.policy,
            self.mailboxes,
            assignments=self.assignments,
            obligations=self.response_obligations,
            retention_days=config.policies.operations.retention_days,
        )
        self.relationships = RelationshipService(
            store,
            approval_verifier=self.approval_verifier,
        )
        if config.features.sealed_rooms and (mls_provider is None or mls_adoption is None):
            raise GateBlocked(
                "G12/G19/PD-007",
                "sealed_rooms cannot be enabled by configuration evidence; inject a validated adoption and live MLS provider",
            )
        self.rooms = RoomService(
            store,
            mls_provider=mls_provider,
            mls_adoption=mls_adoption,
            governance_policy=config.policies.rooms,
            confidentiality_policy=config.policies.confidentiality,
            outage_gate=self.outage,
        )
        self.room_governance = RoomGovernance(store, policy=config.policies.rooms)
        self.presence = PresenceService(store)
        self.directory = DirectoryService(store)
        federation_trust = config.federation_trust
        self.federation = FederationService(
            store,
            enabled=config.features.federation,
            runtime_capabilities=config.server_agent_capabilities,
            policy_engine=self.policy,
            trusted_domain_keys=(
                federation_trust.trusted_domain_key_map
                if federation_trust is not None
                else {}
            ),
            host_policy_keys=(
                federation_trust.host_policy_key_map
                if federation_trust is not None
                else {}
            ),
            assurance_policy=config.policies.federation,
            attenuation_policy=config.policies.attenuation,
            outage_gate=self.outage,
            relationships=self.relationships,
        )
        self.relay_service: ServerAgentRelayService | None = None
        if config.features.peer_mesh:
            self.relay_service = create_server_agent_relay_service(
                config,
                store,
                mailbox=self.mailboxes,
                policy=self.policy,
                admission=self.quotas,
            )
        self.effects = EffectReservations(store, admission=self.quotas)
        self.attention = AttentionService(config.policies.attention)
        self.confidentiality = ConfidentialityEnforcer(config.policies.confidentiality)
        self.audit = AuditService(store)
        self.contexts = VerifiedContextResolver(
            store,
            service_audience=config.effective_service_audience,
            service_scheme=config.service_scheme,
            service_authority=config.service_authority,
            proof_max_age=config.proof_max_age_seconds,
            future_skew=config.allowed_clock_skew_seconds,
            replay_retention=config.replay_retention_seconds,
        )
        self.credential_rotation = CredentialRotationService(
            store,
            credential_ttl_seconds=config.policies.identity.credential_ttl_seconds,
            outage_gate=self.outage,
        )
        scanner_policy = None
        scanner_keys: dict[str, str] | None = None
        if config.scanner_trust is not None:
            scanner = config.scanner_trust
            scanner_keys = scanner.trusted_public_keys
            scanner_policy = ScannerTrustPolicy(
                max_attestation_age_seconds=scanner.max_attestation_age_seconds,
                allowed_future_skew_seconds=scanner.allowed_future_skew_seconds,
                required_engine=scanner.required_engine,
                required_rules_digest=scanner.required_rules_digest,
                required_profile_digest=scanner.required_profile_digest,
                revoked_key_epochs=scanner.revoked_key_epochs,
            )
        self.artifacts = ArtifactService(
            store,
            FilesystemArtifactStore(config.artifact_dir, config.data_dir / "secrets" / "artifact.key"),
            trusted_scanner_keys=scanner_keys,
            scanner_policy=scanner_policy,
            operations_policy=config.policies.operations,
            outage_gate=self.outage,
        )
        self.conversations.artifact_binding_validator = self.artifacts.require_released_binding
        self.oidc_enrollment: OIDCEnrollmentCoordinator | None = None
        self.internal_invitation_oidc: InternalInvitationOIDCCoordinator | None = None
        self.internal_invitations: InternalInvitationService | None = None
        if config.oidc_enrollment is not None:
            oidc = config.oidc_enrollment
            if self.approval_verifier is None:  # pragma: no cover - validated composition invariant
                raise GateBlocked("approval_trust", "configured independent approval verifier is absent")
            enrollment = self.create_enrollment_service(
                self.approval_verifier,
                binding_assurance=oidc.binding_assurance,
            )
            provider = OIDCProvider(
                OIDCProviderConfig(
                    issuer=oidc.issuer,
                    client_id=oidc.client_id,
                    redirect_uri=oidc.redirect_uri,
                    audience=oidc.audience,
                    allowed_signing_algorithms=oidc.allowed_signing_algorithms,
                    pinned_jwk_thumbprints=tuple(sorted(oidc.pinned_jwk_thumbprints.items())),
                    allowed_endpoint_origins=oidc.allowed_endpoint_origins,
                    allowed_private_endpoint_cidrs=oidc.allowed_private_endpoint_cidrs,
                    pinned_endpoint_addresses=oidc.pinned_endpoint_addresses,
                )
            )
            self.oidc_enrollment = OIDCEnrollmentCoordinator(store, provider, enrollment)
            self.internal_invitation_oidc = InternalInvitationOIDCCoordinator(store, provider)
            self.internal_invitations = InternalInvitationService(
                store,
                oidc_verifier=self.internal_invitation_oidc,
                credential_ttl_seconds=config.policies.identity.credential_ttl_seconds,
            )

    @classmethod
    def open(
        cls,
        config: ExtensionConfig,
        *,
        validate_deployment_identity: bool = True,
    ) -> "CommunicationCore":
        config.data_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        record_key = config.data_dir / "secrets" / "records.key"
        artifact_key = config.data_dir / "secrets" / "artifact.key"
        if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            missing = [
                str(path)
                for path in (record_key, artifact_key)
                if path.is_symlink() or not path.is_file()
            ]
            if missing:
                raise GateBlocked(
                    "key_preprovision",
                    "always-on software-key files must be preprovisioned owner-only: " + ", ".join(missing),
                )
        cipher = LocalEnvelopeCipher.from_key_file(
            record_key,
            create=config.profile is RuntimeProfile.LOCAL_CONFORMANCE,
        )
        if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            store: StoreBackend = PostgreSQLStore(
                config.resolved_database_url(),
                cipher,
                instance_id=config.runtime_instance_id,
                connect_timeout=config.postgres_connect_timeout_seconds,
                statement_timeout_ms=config.postgres_statement_timeout_ms,
                lock_timeout_ms=config.postgres_lock_timeout_ms,
                lease_ttl_seconds=config.postgres_lease_ttl_seconds,
                run_migrations=config.postgres_auto_migrate,
                require_recovery_topology=config.postgres_recovery_topology,
            )
        else:
            if not config.database_url.startswith("sqlite:///"):
                raise GateBlocked("storage_profile", "local_conformance requires its SQLite backend")
            configured_path = config.database_url.removeprefix("sqlite:///")
            database_path = Path(configured_path)
            if not database_path.is_absolute():
                database_path = config.data_dir.parent / database_path
            store = SQLiteStore(database_path, cipher)
        try:
            core = cls(config, store)
            if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
                if validate_deployment_identity:
                    core._require_enrolled_server_agent_binding()
                    core._require_server_agent_capability(ServerAgentCapability.ARTIFACT_STORAGE)
            recovery_limit = min(config.artifact_recovery_scan_limit, 1_000)
            core.artifacts.reconcile_quota_accounting()
            core.artifacts.recover_expired_reservations(limit=recovery_limit)
            if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
                if validate_deployment_identity:
                    core.artifacts.recover_release_outbox(limit=1_000)
                    core.artifacts.recover_deletion_outbox(limit=1_000)
            return core
        except Exception:
            store.close()
            raise

    def close(self) -> None:
        self.store.close()

    def create_enrollment_service(
        self,
        approval_verifier: ApprovalVerifier,
        *,
        binding_assurance: BindingAssurance,
        credential_ttl: int | None = None,
        clock: Any | None = None,
    ) -> EnrollmentService:
        """Build enrollment from the configured identity, approval, and outage policies."""

        return EnrollmentService(
            self.store,
            approval_verifier,
            profile=self.config.profile,
            binding_assurance=binding_assurance,
            credential_ttl=(
                self.config.policies.identity.credential_ttl_seconds
                if credential_ttl is None
                else credential_ttl
            ),
            identity_policy=self.config.policies.identity,
            approval_policy=self.config.policies.enrollment_approval,
            outage_gate=self.outage,
            clock=clock,
        )

    def create_elevation_service(
        self,
        approval_verifier: IndependentApprovalVerifier,
    ) -> ElevationService:
        """Build elevation from configured thresholds, TTL/use ceilings, and outage holds."""

        return ElevationService(
            self.grants,
            approval_verifier,
            policy=self.config.policies.elevation,
            outage_gate=self.outage,
        )

    def create_recovery_service(
        self,
        approval_verifier: IndependentApprovalVerifier,
    ) -> CredentialRecoveryService:
        """Build key-loss recovery with the configured independent threshold and outage gate."""

        return CredentialRecoveryService(
            self.store,
            approval_verifier,
            policy=self.config.policies.enrollment_approval,
            outage_gate=self.outage,
            relationships=self.relationships,
            task_grants=self.grants,
            recovered_credential_ttl_seconds=(
                self.config.policies.identity.credential_ttl_seconds
            ),
        )

    def require_content_processing(
        self,
        classification: Classification,
        capability: str,
    ) -> str:
        """Authorize a service processing role under the configured C0-C3 profile."""

        return self.confidentiality.require_processing(classification, capability)

    def _require_enrolled_server_agent_binding(self) -> None:
        """Validate deployment labels without granting caller authority.

        Every protected operation still resolves its own verified actor and
        policy decision.  This binding only prevents an always-on process from
        starting under a nonexistent, stale, lab-only, or copied enrollment.
        """

        credential_id = self.config.enrolled_credential_id
        harness_id = self.config.enrolled_harness_id
        if credential_id is None or harness_id is None:
            raise GateBlocked("server_agent_enrollment", "server-agent enrollment configuration is absent")
        try:
            binding = load_credential_binding(self.store, credential_id)
            if (
                binding.domain_id != self.config.domain_id
                or binding.harness_id != harness_id
                or binding.credential_id != credential_id
                or binding.binding_assurance == "lab"
            ):
                raise AuthenticationError("configured server-agent enrollment lineage mismatches")
            # Credential rotation must update the exact deployment binding through
            # an explicit, separately authorized flow. Startup never follows a
            # retired credential label to a different active credential.
            binding.require_active(now=int(time.time()))
        except Exception as exc:
            raise GateBlocked("server_agent_enrollment", "configured server-agent enrollment is not current") from exc
        if (
            binding.domain_id != self.config.domain_id
            or binding.harness_id != harness_id
            or binding.binding_assurance == "lab"
        ):
            raise GateBlocked("server_agent_enrollment", "configured server-agent enrollment binding mismatches")

    def server_agent_binding_status(self) -> dict[str, Any]:
        if self.config.profile is RuntimeProfile.LOCAL_CONFORMANCE:
            return {"ready": True, "required": False}
        try:
            self._require_enrolled_server_agent_binding()
        except Exception as exc:
            return {
                "ready": False,
                "required": True,
                "reason": type(exc).__name__,
            }
        return {
            "ready": True,
            "required": True,
        }

    def _require_server_agent_capability(self, capability: ServerAgentCapability) -> None:
        """Apply a deployment-side upper bound; never create caller authority."""

        if (
            self.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT
            and capability not in self.config.server_agent_capabilities
        ):
            raise GateBlocked(
                "server_agent_capability",
                f"this ordinary enrolled server agent is not configured for {capability.value}",
            )

    def bootstrap_domain(self) -> dict[str, Any]:
        record = DomainRegistry(self.store).register(self.config.domain_id)
        return asdict(record)

    def bootstrap_synthetic_identity(self, *, harness_kind: str, display_name: str) -> tuple[VerifiedActor, P256KeyPair]:
        """Create a local synthetic actor; forbidden in always-on server-agent mode.

        This helper exists for runnable conformance and demos.  It does not
        satisfy OIDC, OOB, platform custody, or owner policy gates.
        """
        if self.config.profile is not RuntimeProfile.LOCAL_CONFORMANCE:
            raise GateBlocked("G06/G17", "synthetic identity is local-conformance only")
        DomainRegistry(self.store).register(self.config.domain_id)
        suffix = uuid4().hex
        principal_id = f"synthetic-principal-{suffix}"
        harness_id = f"synthetic-harness-{suffix}"
        credential_id = f"synthetic-credential-{suffix}"
        key = P256KeyPair.generate()
        now = int(time.time())
        with self.store.transaction() as connection:
            connection.execute(
                """INSERT INTO principals(
                    principal_id,domain_id,oidc_issuer,oidc_subject,verified_email,status,created_at
                ) VALUES(?,?,?,?,?,'active',?)""",
                (principal_id, self.config.domain_id, "https://synthetic.invalid", suffix, f"{suffix}@synthetic.invalid", now),
            )
            connection.execute(
                """INSERT INTO harnesses(
                    harness_id,domain_id,principal_id,kind,display_name,status,binding_assurance,capabilities_json,credential_epoch,created_at
                ) VALUES(?,?,?,?,?,'deterministic_only','lab',?,1,?)""",
                (harness_id, self.config.domain_id, principal_id, harness_kind, display_name, canonical_json({"synthetic": True}).decode(), now),
            )
            connection.execute(
                """INSERT INTO credentials(
                    credential_id,harness_id,key_id,public_key_pem,status,epoch,not_before,expires_at
                ) VALUES(?,?,?,?,'active',1,?,?)""",
                (credential_id, harness_id, key.thumbprint, key.public_pem, now - 1, now + 86_400),
            )
            self.store.append_audit(
                connection,
                {"action": "synthetic_identity.created", "harness_id": harness_id, "principal_id": principal_id, "warning": "not production enrollment"},
            )
        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=self.config.domain_id,
            principal_id=principal_id,
            harness_id=harness_id,
            credential_id=credential_id,
            credential_epoch=1,
            binding_assurance="lab",
        )
        return actor, key

    def send_synthetic_message(
        self,
        *,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        payload: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        """Exercise mailbox mechanics with explicitly synthetic C0 bytes.

        This method cannot carry a corporate entitlement, C1/C2/C3 data, room
        action, task, grant, external sink, or effect. It is intentionally not
        exposed by the HTTP/MCP bindings.
        """
        if self.config.profile is not RuntimeProfile.LOCAL_CONFORMANCE:
            raise GateBlocked("G06/G07", "synthetic message lane is local-conformance only")
        if actor.binding_assurance != "lab" or actor.harness_id is None:
            raise AuthorizationError("synthetic lane requires the lab actor profile")
        actor_row = self.store.fetch_one(
            """
            SELECT h.status,d.status AS domain_status,d.policy_revision
              FROM harnesses AS h
              JOIN domains AS d ON d.domain_id=h.domain_id
             WHERE h.harness_id=? AND h.domain_id=?
            """,
            (actor.harness_id, actor.domain_id),
        )
        if (
            actor_row is None
            or actor_row["status"] != "deterministic_only"
            or actor_row["domain_status"] != "active"
        ):
            raise AuthorizationError("synthetic lane requires deterministic-only harness state")
        if payload.get("synthetic") is not True:
            raise AuthorizationError("synthetic lane requires an explicit synthetic marker")
        recipient_rows = self.store.fetch_all(
            f"SELECT harness_id,status FROM harnesses WHERE harness_id IN ({','.join('?' for _ in recipients)})",
            tuple(recipients),
        ) if recipients else []
        if len(recipient_rows) != len(recipients) or any(row["status"] != "deterministic_only" for row in recipient_rows):
            raise AuthorizationError("synthetic lane recipients must be local deterministic-only harnesses")
        synthetic_workload = VerifiedActor(
            kind=ActorKind.WORKLOAD,
            domain_id=actor.domain_id,
            workload_id=f"synthetic-lab-mailbox:{actor.harness_id}",
            binding_assurance="synthetic_lab",
        )
        event = new_event(
            domain_id=actor.domain_id,
            actor=synthetic_workload,
            event_type=EventType.MESSAGE,
            classification=Classification.C0_PUBLIC,
            payload=payload,
            idempotency_key=idempotency_key,
            recipients=recipients,
            retention_delete_at=datetime.now(UTC) + timedelta(days=1),
            policy_revision=int(actor_row["policy_revision"]),
        )
        return self.mailboxes.accept(event)

    def grant_local_entitlement(self, actor: VerifiedActor, *, action: str, resource: str = "*") -> HumanEntitlement:
        if self.config.profile is not RuntimeProfile.LOCAL_CONFORMANCE or actor.principal_id is None:
            raise GateBlocked("G08/G17", "local entitlement helper is synthetic-only")
        return self.policy.bootstrap_entitlement_for_local_conformance(
            HumanEntitlement(
                domain_id=actor.domain_id,
                principal_id=actor.principal_id,
                action=action,
                resource_pattern=resource,
                revision=1,
                expires_at=datetime.now(UTC) + timedelta(days=1),
            )
        )

    def authenticate(
        self,
        proof: RequestProof,
        *,
        method: str,
        scheme: str,
        authority: str,
        path: str,
        query: str,
        body: bytes,
        caller_claims: dict[str, Any] | None = None,
    ) -> TrustedTransportContext:
        started = time.perf_counter_ns()
        try:
            if self.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
                self._require_enrolled_server_agent_binding()
            context = self.contexts.resolve(
                proof,
                expected_method=method,
                expected_scheme=scheme,
                expected_authority=authority,
                expected_path=path,
                expected_query=query,
                body=body,
                caller_claims=caller_claims,
            )
        except Exception:
            self.telemetry.increment("auth_result", outcome="denied")
            elapsed = min(30_000, max(0, (time.perf_counter_ns() - started) // 1_000_000))
            self.telemetry.observe_latency("auth_latency", int(elapsed), outcome="denied")
            raise
        self.telemetry.increment("auth_result", outcome="ok")
        elapsed = min(30_000, max(0, (time.perf_counter_ns() - started) // 1_000_000))
        self.telemetry.observe_latency("auth_latency", int(elapsed))
        return context

    def _require(
        self,
        *,
        actor: VerifiedActor,
        action: str,
        resource: str,
        operation_class: OperationClass = OperationClass.BUSINESS,
        classification: Classification | None = None,
        grant_use: Any = None,
        context: dict[str, Any] | None = None,
    ) -> Any:
        if self.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            self._require_enrolled_server_agent_binding()
        if operation_class is OperationClass.BUSINESS:
            self.outage.require_low_risk_continuity()
        else:
            self.outage.require_privileged()
        try:
            decision = self.policy.require(
                AuthorizationRequest(
                    actor=actor,
                    action=action,
                    resource=resource,
                    operation_class=operation_class,
                    classification=classification,
                    policy_revision=self.policy.current_policy_revision(actor),
                    grant_use=grant_use,
                    context=context or {},
                )
            )
        except Exception:
            self.telemetry.increment("policy_result", outcome="denied")
            raise
        self.telemetry.increment("policy_result", outcome="ok")
        return decision

    def begin_version_rollout(
        self,
        *,
        actor: VerifiedActor,
        from_protocol_version: str,
        to_protocol_version: str,
        from_schema_version: int,
        to_schema_version: int,
        compatibility_deadline: int,
    ) -> dict[str, Any]:
        self._require(
            actor=actor,
            action="operator.version.rollout",
            resource=f"operator-domain:{self.config.domain_id}",
            operation_class=OperationClass.PRIVILEGED,
            context={
                "from_protocol_version": from_protocol_version,
                "to_protocol_version": to_protocol_version,
                "from_schema_version": from_schema_version,
                "to_schema_version": to_schema_version,
                "compatibility_deadline": compatibility_deadline,
            },
        )
        return self.versioning.begin_rollout(
            host_domain_id=self.config.domain_id,
            from_protocol_version=from_protocol_version,
            to_protocol_version=to_protocol_version,
            from_schema_version=from_schema_version,
            to_schema_version=to_schema_version,
            compatibility_deadline=compatibility_deadline,
        )

    def advance_version_rollout(
        self,
        *,
        actor: VerifiedActor,
        rollout_id: str,
        expected_phase: str,
        target_phase: str,
        verification_digest: str | None,
    ) -> dict[str, Any]:
        self._require(
            actor=actor,
            action="operator.version.rollout",
            resource=f"version-rollout:{rollout_id}",
            operation_class=OperationClass.PRIVILEGED,
            context={
                "expected_phase": expected_phase,
                "target_phase": target_phase,
                "verification_digest": verification_digest,
            },
        )
        return self.versioning.advance_rollout(
            rollout_id,
            expected_phase=expected_phase,
            target_phase=target_phase,
            verification_digest=verification_digest,
        )

    def rollback_version_rollout(
        self,
        *,
        actor: VerifiedActor,
        rollout_id: str,
        verification_digest: str,
    ) -> dict[str, Any]:
        self._require(
            actor=actor,
            action="operator.version.rollback",
            resource=f"version-rollout:{rollout_id}",
            operation_class=OperationClass.PRIVILEGED,
            context={"verification_digest": verification_digest},
        )
        return self.versioning.rollback_rollout(
            rollout_id,
            verification_digest=verification_digest,
        )

    def quarantine_unsupported_event(
        self,
        *,
        peer_namespace: str,
        event: dict[str, Any],
        requirement: CompatibilityRequirement,
    ) -> dict[str, Any]:
        return self.versioning.queue_if_unsupported(
            peer_namespace=peer_namespace,
            event=event,
            requirement=requirement,
        )

    def replay_unsupported_events(
        self,
        *,
        actor: VerifiedActor,
        peer_namespace: str,
        limit: int = 100,
    ) -> dict[str, int]:
        """Replay supported canonical mailbox envelopes through normal custody.

        Generic queued bytes are never marked replayed by the HTTP operator
        route.  Only a fully validated local-domain EventEnvelope can reach the
        canonical mailbox idempotency/admission transaction.
        """

        self._require(
            actor=actor,
            action="operator.version.replay",
            resource=f"version-replay:{peer_namespace}",
            operation_class=OperationClass.PRIVILEGED,
            context={"peer_namespace": peer_namespace, "limit": limit},
        )

        def replay(event: dict[str, Any], _event_digest: str) -> None:
            envelope = EventEnvelope.model_validate(event)
            if envelope.domain_id != self.config.domain_id:
                raise AuthorizationError("unsupported-event replay crossed the local domain")
            self.mailboxes.accept(envelope)

        return self.versioning.replay_supported(
            peer_namespace,
            DigestIdempotentReplayHandler(replay),
            limit=limit,
        )

    def rotate_credential(
        self,
        *,
        actor: VerifiedActor,
        request: CredentialRotationRequest,
    ) -> CredentialRotationResult:
        """Replace only the transport-authenticated harness's current key."""

        if self.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            self._require_enrolled_server_agent_binding()
        return self.credential_rotation.rotate(actor=actor, request=request)

    def send_message(
        self,
        *,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        payload: dict[str, Any],
        idempotency_key: str,
        classification: Classification = Classification.C1_INTERNAL,
        released_artifacts: tuple[ReleasedArtifactBinding, ...] = (),
        conversation_id: str | None = None,
        room_id: str | None = None,
        expected_room_control_sequence: int | None = None,
    ) -> dict[str, Any]:
        started = time.perf_counter_ns()
        operation = "mailbox_accept"
        try:
            if not recipients:
                raise ValidationError("at least one recipient is required")
            self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
            recipient_limit = self.config.policies.operations.per_message_recipient_limit
            if len(recipients) > recipient_limit:
                raise AuthorizationError("recipient fanout exceeds the configured secure policy limit")
            resource = room_id or conversation_id or "direct"
            decision = self._require(
                actor=actor,
                action="message.send",
                resource=resource,
                classification=classification,
            )
            if classification is Classification.C3_SEALED and (
                room_id is None
                or not self.config.features.sealed_rooms
            ):
                raise AuthorizationError("C3 content requires an explicitly enabled validated-MLS room")
            if room_id is None and expected_room_control_sequence is not None:
                raise AuthorizationError("room control sequence cannot be supplied without a room")
            for binding in released_artifacts:
                self.artifacts.require_released_binding(
                    binding,
                    domain_id=actor.domain_id,
                    event_classification=classification,
                )

            def build_event(room_snapshot: dict[str, Any] | None) -> Any:
                return new_event(
                    domain_id=actor.domain_id,
                    actor=actor,
                    event_type=EventType.MESSAGE,
                    classification=classification,
                    payload=payload,
                    idempotency_key=idempotency_key,
                    recipients=recipients,
                    released_artifacts=released_artifacts,
                    conversation_id=conversation_id,
                    room_id=room_id,
                    room_control_sequence=(
                        None if room_snapshot is None else int(room_snapshot["control_sequence"])
                    ),
                    room_application_epoch=(
                        None if room_snapshot is None else int(room_snapshot["application_epoch"])
                    ),
                    room_file_key_epoch=(
                        None if room_snapshot is None else int(room_snapshot["file_key_epoch"])
                    ),
                    room_mls_epoch=(
                        None if room_snapshot is None else int(room_snapshot["mls_epoch"])
                    ),
                    retention_delete_at=datetime.now(UTC)
                    + timedelta(days=self.config.policies.operations.retention_days),
                    policy_revision=decision.policy_revision,
                )

            if room_id is not None:
                if expected_room_control_sequence is None:
                    raise AuthorizationError("room send requires the expected control sequence")
                with self.store.transaction(immediate=True) as connection:
                    room_snapshot = self.rooms.authorize_send_in_transaction(
                        connection,
                        actor=actor,
                        room_id=room_id,
                        recipients=recipients,
                        classification=classification,
                        expected_control_sequence=expected_room_control_sequence,
                    )
                    result = self.mailboxes._accept_in_transaction(
                        connection,
                        build_event(room_snapshot),
                    )
            else:
                result = self.mailboxes.accept(build_event(None))
        except ExtensionError:
            self.telemetry.increment("mailbox_accept", outcome="denied")
            elapsed = min(30_000, max(0, (time.perf_counter_ns() - started) // 1_000_000))
            self.telemetry.observe_latency("mailbox_latency", int(elapsed), outcome="denied")
            raise
        except Exception:
            self.quotas.record_failure(operation=operation, domain_scope=actor.domain_id)
            self.telemetry.increment("mailbox_accept", outcome="error")
            elapsed = min(30_000, max(0, (time.perf_counter_ns() - started) // 1_000_000))
            self.telemetry.observe_latency("mailbox_latency", int(elapsed), outcome="error")
            raise
        self.quotas.record_success(operation=operation, domain_scope=actor.domain_id)
        self.telemetry.increment("mailbox_accept")
        elapsed = min(30_000, max(0, (time.perf_counter_ns() - started) // 1_000_000))
        self.telemetry.observe_latency("mailbox_latency", int(elapsed))
        return result

    def create_conversation(
        self,
        *,
        actor: VerifiedActor,
        conversation_id: str,
        member_harness_ids: tuple[str, ...],
        classification: Classification = Classification.C1_INTERNAL,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        if len(member_harness_ids) + 1 > self.config.policies.operations.per_message_recipient_limit:
            raise AuthorizationError("conversation membership exceeds the configured secure policy limit")
        self.quotas.consume(
            scope=actor.harness_id or "unknown",
            metric="conversation_mutations",
            amount=1,
            limit=self.config.policies.operations.per_actor_requests_per_minute,
        )
        return self.conversations.create(
            actor=actor,
            conversation_id=conversation_id,
            member_harness_ids=member_harness_ids,
            classification=classification,
        )

    def post_conversation_action(
        self,
        *,
        actor: VerifiedActor,
        recipients: tuple[str, ...],
        conversation_id: str,
        thread_id: str,
        action: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        if len(recipients) > self.config.policies.operations.per_message_recipient_limit:
            raise AuthorizationError("conversation fanout exceeds the configured secure policy limit")
        self.quotas.consume(
            scope=actor.harness_id or "unknown",
            metric="conversation_mutations",
            amount=1,
            limit=self.config.policies.operations.per_actor_requests_per_minute,
        )
        return self.conversations.post(
            actor=actor,
            recipients=recipients,
            conversation_id=conversation_id,
            thread_id=thread_id,
            action=action,
            idempotency_key=idempotency_key,
        )

    def conversation_thread(
        self,
        *,
        actor: VerifiedActor,
        conversation_id: str,
        thread_id: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.conversations.thread(
            actor=actor,
            conversation_id=conversation_id,
            thread_id=thread_id,
            limit=limit,
        )

    def _consume_obligation_mutation_quota(self, actor: VerifiedActor) -> None:
        self.quotas.consume(
            scope=actor.harness_id or "unknown",
            metric="conversation_mutations",
            amount=1,
            limit=self.config.policies.operations.per_actor_requests_per_minute,
        )

    def response_obligation_transition(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
        to_state: str,
        reason: str = "recipient_update",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        self._consume_obligation_mutation_quota(actor)
        return self.response_obligations.transition(
            actor=actor,
            obligation_id=obligation_id,
            to_state=to_state,
            reason=reason,
            expected_revision=expected_revision,
        )

    def response_obligation_cancel(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
        reason_code: str = "requester_canceled",
        expected_revision: int | None = None,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        self._consume_obligation_mutation_quota(actor)
        return self.response_obligations.cancel(
            actor=actor,
            obligation_id=obligation_id,
            reason_code=reason_code,
            expected_revision=expected_revision,
        )

    def response_obligation_reconcile(
        self,
        *,
        actor: VerifiedActor,
        limit: int = 100,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        self._consume_obligation_mutation_quota(actor)
        return self.response_obligations.reconcile(actor=actor, limit=limit)

    def response_obligation(
        self,
        *,
        actor: VerifiedActor,
        obligation_id: str,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.response_obligations.get(actor=actor, obligation_id=obligation_id)

    def response_obligation_list(
        self,
        *,
        actor: VerifiedActor,
        role: str = "any",
        states: tuple[str, ...] = (),
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        if role not in {"requester", "responsible", "any"}:
            raise ValidationError("obligation list role is invalid")
        return self.response_obligations.list_for(
            actor=actor,
            role=role,  # type: ignore[arg-type]
            states=states,
            limit=limit,
        )

    def response_obligation_inbox(self, *, actor: VerifiedActor) -> dict[str, int]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.response_obligations.inbox(actor=actor)

    def mailbox(self, *, actor: VerifiedActor, after_cursor: int = 0, limit: int = 100) -> list[dict[str, Any]]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        if actor.harness_id is None:
            raise AuthorizationError("mailbox requires exact harness attribution")
        mailbox_classification = (
            Classification.C0_PUBLIC
            if self.config.profile is RuntimeProfile.LOCAL_CONFORMANCE
            and actor.binding_assurance == "lab"
            else Classification.C1_INTERNAL
        )
        self._require(
            actor=actor,
            action="mailbox.read",
            resource=actor.harness_id,
            classification=mailbox_classification,
        )
        return self.mailboxes.reconcile(actor.harness_id, after_cursor=after_cursor, limit=limit)

    def acknowledge_mailbox(
        self,
        *,
        actor: VerifiedActor,
        event_id: str,
        envelope_digest: str,
    ) -> dict[str, Any]:
        """Assert recipient custody only; never presentation, processing, or effect."""

        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        if actor.harness_id is None:
            raise AuthorizationError("mailbox acknowledgement requires exact harness attribution")
        if (
            not 1 <= len(event_id) <= 256
            or event_id != event_id.strip()
            or any(
                character not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-"
                for character in event_id
            )
        ):
            raise ValidationError("mailbox acknowledgement event_id is invalid")
        if len(envelope_digest) != 64 or any(
            character not in "0123456789abcdef" for character in envelope_digest
        ):
            raise ValidationError("mailbox acknowledgement envelope digest is invalid")
        mailbox_classification = (
            Classification.C0_PUBLIC
            if self.config.profile is RuntimeProfile.LOCAL_CONFORMANCE
            and actor.binding_assurance == "lab"
            else Classification.C1_INTERNAL
        )
        self._require(
            actor=actor,
            action="mailbox.acknowledge",
            resource=actor.harness_id,
            classification=mailbox_classification,
        )
        return self.mailboxes.acknowledge(
            event_id=event_id,
            recipient_id=actor.harness_id,
            envelope_digest_value=envelope_digest,
            owner_actor=actor,
        )

    def assign_task(
        self,
        request: AssignmentRequest,
        *,
        payload: dict[str, Any],
        idempotency_key: str,
        released_artifacts: tuple[ReleasedArtifactBinding, ...] = (),
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        if self.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
            self._require_enrolled_server_agent_binding()
        request = request.model_copy(
            update={"policy_revision": self.policy.current_policy_revision(request.actor)}
        )
        event_id = str(
            uuid5(
                NAMESPACE_URL,
                f"agentnet:task:{request.actor.domain_id}:{request.actor.harness_id}:{idempotency_key}",
            )
        )
        event = new_event(
            event_id=event_id,
            domain_id=request.actor.domain_id,
            actor=request.actor,
            event_type=EventType.TASK_ASSIGNMENT,
            classification=max(request.data_classes, key=lambda item: item.value),
            payload=payload,
            idempotency_key=idempotency_key,
            recipients=(request.recipient_harness_id,),
            released_artifacts=tuple(
                self.artifacts.require_released_binding(
                    binding,
                    domain_id=request.actor.domain_id,
                    event_classification=max(request.data_classes, key=lambda item: item.value),
                )
                for binding in released_artifacts
            ),
            task_id=str(uuid5(NAMESPACE_URL, f"agentnet:task-id:{event_id}")),
            effect_deadline=request.deadline,
            policy_revision=request.policy_revision,
            retention_delete_at=datetime.now(UTC)
            + timedelta(days=self.config.policies.operations.retention_days),
        )
        return self.assignments.submit_event(request, event)

    def task_proposals(self, *, actor: VerifiedActor, limit: int = 100) -> list[dict[str, Any]]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.assignments.pending_for_owner(actor=actor, limit=limit)

    def approve_task_proposal(
        self,
        *,
        actor: VerifiedActor,
        proposal_id: str,
        request_digest: str,
        revision: int,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.assignments.approve(
            actor=actor,
            proposal_id=proposal_id,
            expected_request_digest=request_digest,
            expected_revision=revision,
        ).model_dump(mode="json")

    def deny_task_proposal(
        self,
        *,
        actor: VerifiedActor,
        proposal_id: str,
        request_digest: str,
        revision: int,
        reason_code: str,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.assignments.deny(
            actor=actor,
            proposal_id=proposal_id,
            expected_request_digest=request_digest,
            expected_revision=revision,
            reason_code=reason_code,
        ).model_dump(mode="json")

    def reauthorize_task_proposal(
        self,
        *,
        actor: VerifiedActor,
        proposal_id: str,
        request_digest: str,
        revision: int,
        relationship_revision: int,
    ) -> dict[str, Any]:
        self._require_server_agent_capability(ServerAgentCapability.OFFLINE_CUSTODY)
        return self.assignments.reauthorize_with_current_edge(
            actor=actor,
            proposal_id=proposal_id,
            expected_request_digest=request_digest,
            expected_revision=revision,
            expected_relationship_revision=relationship_revision,
        ).model_dump(mode="json")

    def propose_relationship(
        self,
        *,
        actor: VerifiedActor,
        relationship: Relationship,
        proposal_expires_at: datetime,
    ) -> RelationshipGovernanceRecord:
        self.outage.require_issuance()
        resource, context = self.relationships.proposal_binding(
            relationship,
            proposal_expires_at=proposal_expires_at,
        )
        decision = self._require(
            actor=actor,
            action="organization.relationship.propose",
            resource=resource,
            context=context,
        )
        return self.relationships.propose(
            relationship,
            proposal_expires_at=proposal_expires_at,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        )

    def issue_relationship(
        self,
        *,
        actor: VerifiedActor,
        relationship: Relationship,
        proposal_expires_at: datetime | None = None,
    ) -> RelationshipGovernanceRecord:
        """Compatibility name with proposal-only semantics; it never activates an edge."""

        return self.propose_relationship(
            actor=actor,
            relationship=relationship,
            proposal_expires_at=proposal_expires_at or relationship.expires_at,
        )

    def accept_relationship(
        self,
        *,
        actor: VerifiedActor,
        relationship_id: str,
        approval: Mapping[str, Any],
        expected_transaction_digest: str,
        expected_relationship_revision: int,
        expected_lifecycle_revision: int,
    ) -> RelationshipGovernanceRecord:
        self.outage.require_issuance()
        return self.relationships.accept(
            relationship_id,
            actor=actor,
            approval=approval,
            expected_transaction_digest=expected_transaction_digest,
            expected_relationship_revision=expected_relationship_revision,
            expected_lifecycle_revision=expected_lifecycle_revision,
        )

    def record_relationship_policy_exception(
        self,
        *,
        actor: VerifiedActor,
        exception: RelationshipPolicyException,
        command: SignedAuthorityCommand,
    ) -> RelationshipPolicyExceptionRecord:
        self.outage.require_issuance()
        resource, exact_request = self.relationships.policy_exception_binding(exception)
        if (
            command.action != "organization.relationship.policy_exception.record"
            or command.resource != resource
            or command.request_digest != canonical_digest(exact_request)
        ):
            raise AuthorizationError("relationship policy-exception authority binding mismatch")
        decision = self._require(
            actor=actor,
            action="organization.relationship.policy_exception.record",
            resource=resource,
            operation_class=OperationClass.PRIVILEGED,
            context={"request_digest": command.request_digest},
        )
        return self.relationships.record_policy_exception(
            exception,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        )

    def activate_relationship_policy_exception(
        self,
        *,
        actor: VerifiedActor,
        relationship_id: str,
        policy_exception_id: str,
        expected_transaction_digest: str,
        expected_relationship_revision: int,
        expected_lifecycle_revision: int,
    ) -> RelationshipGovernanceRecord:
        self.outage.require_issuance()
        return self.relationships.activate_with_policy_exception(
            relationship_id,
            policy_exception_id=policy_exception_id,
            actor=actor,
            expected_transaction_digest=expected_transaction_digest,
            expected_relationship_revision=expected_relationship_revision,
            expected_lifecycle_revision=expected_lifecycle_revision,
        )

    def revoke_relationship(
        self,
        *,
        actor: VerifiedActor,
        relationship_id: str,
        command: SignedAuthorityCommand,
    ) -> bool:
        resource = f"relationship:{relationship_id}"
        if command.action not in {
            "organization.relationship.revoke",
            "organization.relationship.admin_revoke",
        } or command.resource != resource:
            raise AuthorizationError("relationship authority binding mismatch")
        operation_class = (
            OperationClass.PRIVILEGED
            if command.action == "organization.relationship.admin_revoke"
            else OperationClass.BUSINESS
        )
        decision = self._require(
            actor=actor,
            action=command.action,
            resource=resource,
            operation_class=operation_class,
            context={"request_digest": command.request_digest},
        )
        return self.relationships.revoke(
            relationship_id,
            command=command,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        )

    def issue_task_grant(self, *, actor: VerifiedActor, grant: TaskGrant) -> TaskGrant:
        self.outage.require_issuance()
        if actor.positive_authority_id != grant.principal_id or actor.harness_id != grant.harness_id:
            raise AuthorizationError("task grant cannot cross the verified beneficiary boundary")
        resource, context = self.grants.issuance_binding(grant)
        decision = self._require(
            actor=actor,
            action="authorization.task_grant.issue",
            resource=resource,
            context=context,
        )
        return self.grants.issue(
            grant,
            authority=IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        )

    def reserve_effect(
        self,
        *,
        actor: VerifiedActor,
        event_id: str,
        grant_use: GrantUse,
        request: dict[str, object],
    ) -> dict[str, object]:
        self.config.require_feature("protected_effects")
        self._require_server_agent_capability(ServerAgentCapability.EFFECT_EXECUTOR)
        self.outage.require_privileged()
        return self.effects.reserve(
            policy=self.policy,
            actor=actor,
            event_id=event_id,
            grant_use=grant_use,
            request=request,
        )

    def start_effect_execution(
        self,
        *,
        actor: VerifiedActor,
        effect_id: str,
        proof: EffectTransitionProof,
        evidence: EffectExecutionEvidence,
    ) -> dict[str, object]:
        self.config.require_feature("protected_effects")
        self._require_server_agent_capability(ServerAgentCapability.EFFECT_EXECUTOR)
        self.outage.require_privileged()
        return self.effects.start_execution(
            effect_id,
            actor=actor,
            proof=proof,
            evidence=evidence,
        )

    def cancel_effect(self, *, actor: VerifiedActor, effect_id: str) -> dict[str, object]:
        self.config.require_feature("protected_effects")
        self._require_server_agent_capability(ServerAgentCapability.EFFECT_EXECUTOR)
        self.outage.require_privileged()
        decision = self._require(actor=actor, action="effect.cancel", resource=effect_id)
        return self.effects.cancel_prepared(
            effect_id,
            actor=actor,
            policy_decision_id=decision.decision_id,
        )

    def mark_effect_unknown(
        self,
        *,
        actor: VerifiedActor,
        effect_id: str,
        proof: EffectTransitionProof,
        evidence: EffectUncertaintyEvidence,
    ) -> dict[str, object]:
        self.config.require_feature("protected_effects")
        self._require_server_agent_capability(ServerAgentCapability.EFFECT_EXECUTOR)
        self.outage.require_privileged()
        return self.effects.mark_unknown(effect_id, actor=actor, proof=proof, evidence=evidence)

    def acknowledge_effect_terminal(
        self,
        *,
        actor: VerifiedActor,
        effect_id: str,
        proof: EffectTransitionProof,
        terminal_state: EffectState,
        evidence: EffectTerminalEvidence,
    ) -> dict[str, object]:
        self.config.require_feature("protected_effects")
        self._require_server_agent_capability(ServerAgentCapability.EFFECT_EXECUTOR)
        self.outage.require_privileged()
        return self.effects.acknowledge_terminal(
            effect_id,
            actor=actor,
            proof=proof,
            terminal_state=terminal_state,
            evidence=evidence,
        )

    def reconcile_effect(
        self,
        *,
        actor: VerifiedActor,
        effect_id: str,
        proof: EffectTransitionProof,
        evidence: EffectReconciliationEvidence,
    ) -> dict[str, object]:
        self.config.require_feature("protected_effects")
        self._require_server_agent_capability(ServerAgentCapability.EFFECT_EXECUTOR)
        self.outage.require_privileged()
        return self.effects.reconcile(effect_id, actor=actor, proof=proof, evidence=evidence)

    def readiness(self) -> dict[str, Any]:
        try:
            storage = self.store.readiness()
            audit = self.audit.verify()
            artifacts = self.recovery_status(record_observation=False)["artifacts"]
            deployment_binding = self.server_agent_binding_status()
        except Exception as exc:
            storage = {"ready": False, "backend": self.store.backend_name, "reason": type(exc).__name__}
            audit = {"valid": False, "reason": type(exc).__name__}
            artifacts = {"ready": False, "reason": type(exc).__name__}
            deployment_binding = {"ready": False, "reason": type(exc).__name__}
        a2a_schema = {"ready": True, "required": self.config.features.public_a2a}
        if self.config.features.public_a2a:
            try:
                require_a2a_schema(self.store)
            except Exception as exc:
                a2a_schema = {"ready": False, "required": True, "reason": type(exc).__name__}
        scanner_trust = {
            "ready": bool(self.config.scanner_trust)
            or self.config.profile is RuntimeProfile.LOCAL_CONFORMANCE,
            "required": self.config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
            "trusted_key_count": len(self.artifacts.trusted_scanner_keys),
        }
        operational = bool(
            storage.get("ready")
            and audit.get("valid")
            and artifacts.get("ready")
            and deployment_binding.get("ready")
            and a2a_schema.get("ready")
            and scanner_trust.get("ready")
        )
        try:
            self.telemetry.set_gauge("storage_ready", int(bool(storage.get("ready"))))
            self.telemetry.set_gauge("artifact_ready", int(bool(artifacts.get("ready"))))
            self.telemetry.set_gauge("audit_valid", int(bool(audit.get("valid"))))
            self.telemetry.increment(
                "audit_check",
                outcome="ok" if audit.get("valid") else "invalid",
            )
        except Exception:
            # Readiness is an observation path. A metrics write failure must not
            # convert an otherwise useful degraded response into a 500.
            pass
        return {
            "ready": operational,
            "profile": self.config.profile.value,
            "acceptance_fact": self.mailboxes.acceptance_fact.value,
            "domain_id": self.config.domain_id,
            "enabled_features": [name for name, value in self.config.features.model_dump().items() if value],
            "server_agent_capabilities": sorted(
                capability.value for capability in self.config.server_agent_capabilities
            ),
            "policy_defaults_digest": self.config.policies.digest,
            "storage": storage,
            "artifacts": artifacts,
            "deployment_binding": deployment_binding,
            "a2a_schema": a2a_schema,
            "scanner_trust": scanner_trust,
            "release_certified": False,
            "unverified_external_gates": [
                "PostgreSQL HA/failover/PITR/restore",
                "artifact backup/host-loss restore",
                "server-agent mTLS and KMS custody",
            ],
            "audit": audit,
        }

    def liveness(self) -> dict[str, Any]:
        """Process-only probe; dependency failures belong to readiness."""

        return {
            "status": "alive",
            "profile": self.config.profile.value,
        }

    def recovery_status(self, *, record_observation: bool = False) -> dict[str, Any]:
        artifacts = probe_filesystem_artifact_recovery(
            self.store,
            self.config.artifact_dir,
            instance_id=self.config.runtime_instance_id,
            scan_limit=self.config.artifact_recovery_scan_limit,
            record_observation=record_observation,
        )
        return {
            "ready": bool(artifacts["ready"]),
            "artifacts": artifacts,
            "restore_tested": False,
            "ha_claimed": False,
        }
