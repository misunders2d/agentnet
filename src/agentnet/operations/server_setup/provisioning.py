"""Approval, Core, scanner, and service-runtime provisioning."""

from __future__ import annotations

import json
import os
import shutil
import tempfile
from pathlib import Path
from typing import Any, Literal, Mapping, Sequence
from urllib.parse import urlsplit

if os.name == "posix":
    import pwd

from agentnet.approval.config import ApprovalOwnerOIDCConfig, ApprovalServiceConfig
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.operations.config import (
    ApprovalServiceClientConfig,
    ExtensionConfig,
    IndependentApproverConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
    ScannerTrustConfig,
)
from agentnet.operations.config_migration import load_config_json
from agentnet.security.signatures import P256KeyPair

from .custody import (
    _atomic_write,
    _ensure_account,
    _ensure_private_root,
    _prepare_managed_service_runtime,
    _read_private_managed_file,
    _require_communication_only_artifact_absence,
    _require_private_directory,
    _require_private_file,
    _require_private_tree,
    _run_as,
)
from .models import (
    ScannerSetupSpec,
    ServerSetupError,
    ServerSetupPreflight,
    ServerSetupRequest,
    SetupApprover,
    SetupLayout,
    SetupOIDCProvider,
)
from .preflight import (
    SCANNER_SIGNING_KEY,
    _require_scanner_readiness,
    _strict_json_bytes,
)
from .systemd import (
    APPROVAL_CONFIG,
    APPROVAL_DATA,
    C0_RESPONDER_DATA,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_DATA,
)

CORE_OIDC_CONFIG = CORE_DATA / "oidc-enrollment.json"
SCANNER_WORKER_CONFIG = CORE_DATA / "scanner-worker.json"
APPROVAL_STATE = APPROVAL_DATA / "state"



def _approval_trust(
    config_path: Path,
    account: pwd.struct_passwd,
    approval_state: Path,
) -> tuple[ApprovalServiceConfig, list[IndependentApproverConfig]]:
    _require_private_file(config_path, account, blocker="approval_config")
    _require_private_directory(approval_state, account, blocker="approval_custody")
    try:
        config = ApprovalServiceConfig.model_validate(
            json.loads(
                _read_private_managed_file(
                    config_path,
                    account,
                    blocker="approval_config",
                    max_bytes=1_048_576,
                ).decode("utf-8")
            )
        )
    except Exception as exc:
        raise ServerSetupError("approval_config", "Approval configuration is invalid") from exc
    expected_database = approval_state / "approval.sqlite3"
    expected_record_key = approval_state / "secrets" / "records.key"
    if (
        config.data_dir != approval_state
        or config.database_path != expected_database
        or config.record_key_path != expected_record_key
    ):
        raise ServerSetupError("approval_conflict", "existing Approval custody paths conflict with fixed request")
    _require_private_file(expected_record_key, account, blocker="approval_custody")
    _require_private_file(expected_database, account, blocker="approval_custody")
    trusted: list[IndependentApproverConfig] = []
    for index, item in enumerate(config.approvers, start=1):
        expected_signer = approval_state / "signers" / f"approver-{index}.pem"
        if item.signer_private_key_path != expected_signer:
            raise ServerSetupError("approval_custody", "Approval signer path conflicts with fixed profile")
        _require_private_file(expected_signer, account, blocker="approval_custody")
        try:
            signer = P256KeyPair.from_private_pem(
                _read_private_managed_file(
                    expected_signer,
                    account,
                    blocker="approval_custody",
                    max_bytes=65_536,
                )
            )
        except Exception as exc:
            raise ServerSetupError("approval_custody", "Approval signer custody is invalid") from exc
        if signer.thumbprint != item.signer_key_id:
            raise ServerSetupError("approval_custody", "Approval signer key identifier mismatch")
        trusted.append(
            IndependentApproverConfig(
                principal_id=item.principal_id,
                authority_kind=item.authority_kind,
                signer_key_id=item.signer_key_id,
                public_key_pem=signer.public_pem,
                allowed_purposes=item.allowed_purposes,
            )
        )
    return config, trusted


def _require_exact_approval_policy(
    config: ApprovalServiceConfig,
    *,
    request: ServerSetupRequest,
    owner_oidc: ApprovalOwnerOIDCConfig,
    approvers: tuple[SetupApprover, ...],
    approval_state: Path,
) -> None:
    actual_approvers = tuple(
        SetupApprover(
            principal_id=item.principal_id,
            authority_kind=item.authority_kind,
            domain_id=item.domain_id,
            allowed_purposes=item.allowed_purposes,
            oidc_issuer=item.oidc_issuer,
            oidc_subject=item.oidc_subject,
            verified_email_alias=item.verified_email_alias,
        )
        for item in config.approvers
    )
    approval_host = urlsplit(request.approval_public_origin).hostname
    if (
        approval_host is None
        or config.public_origin != request.approval_public_origin
        or config.rp_id != approval_host
        or config.verifier_id != request.approval_verifier_id
        or config.data_dir != approval_state
        or config.database_path != approval_state / "approval.sqlite3"
        or config.record_key_path != approval_state / "secrets" / "records.key"
        or config.internal_core_credential_env != "AGENTNET_APPROVAL_CORE_TOKEN"
        or config.owner_oidc != owner_oidc
        or actual_approvers != approvers
    ):
        raise ServerSetupError("approval_conflict", "existing Approval state conflicts with fixed request")


def _core_create_arguments(
    request: ServerSetupRequest,
    *,
    node_executable: Path,
    executable: Path,
    core_config_path: Path,
    core_data: Path,
    oidc_path: Path,
    scanner_path: Path,
    scanner_trust: ScannerTrustConfig | None,
) -> list[str]:
    arguments = [
        str(node_executable), str(executable), "network", "create",
        "--config", str(core_config_path),
        "--data-dir", str(core_data / "core"),
        "--domain", request.domain_id,
        "--database-url-env", request.database_url_env,
        "--database-url-from-env",
        "--public-base-url", request.core_public_origin,
        "--oidc-config", str(oidc_path),
        "--artifact-mode", request.effective_artifact_mode,
        "--runtime-instance-id", request.runtime_instance_id,
    ]
    if scanner_trust is not None:
        arguments.extend(["--scanner-trust-config", str(scanner_path)])
    return arguments


def _require_core_create_evidence(
    result: Mapping[str, Any],
    core_config_path: Path,
    *,
    artifact_mode: Literal["enabled", "disabled"],
) -> None:
    readiness = result.get("local_readiness")
    artifact_evidence = readiness.get("artifacts") if isinstance(readiness, dict) else None
    scanner_evidence = readiness.get("scanner_trust") if isinstance(readiness, dict) else None
    artifact_ready = (
        isinstance(artifact_evidence, dict)
        and (
            artifact_evidence.get("ready") is True
            if artifact_mode == "enabled"
            else artifact_evidence == {
                "enabled": False,
                "required": False,
                "ready": False,
                "reason": "disabled",
            }
        )
    )
    scanner_ready = (
        isinstance(scanner_evidence, dict)
        and (
            scanner_evidence.get("ready") is True
            if artifact_mode == "enabled"
            else scanner_evidence == {
                "enabled": False,
                "ready": False,
                "required": False,
                "trusted_key_count": 0,
            }
        )
    )
    if (
        result.get("config") != str(core_config_path)
        or not isinstance(readiness, dict)
        or readiness.get("schema") != "agentnet.core.readiness.v1"
        or readiness.get("ready") is not False
        or not isinstance(readiness.get("storage"), dict)
        or readiness["storage"].get("ready") is not True
        or not isinstance(readiness.get("audit"), dict)
        or readiness["audit"].get("valid") is not True
        or readiness.get("artifact_mode") != artifact_mode
        or not artifact_ready
        or not isinstance(readiness.get("deployment_binding"), dict)
        or readiness["deployment_binding"].get("ready") is not False
        or readiness["deployment_binding"].get("required") is not True
        or not isinstance(readiness.get("a2a_schema"), dict)
        or readiness["a2a_schema"].get("ready") is not True
        or not scanner_ready
    ):
        raise ServerSetupError(
            "core_evidence",
            "Core create evidence did not prove exact healthy pre-enrollment state",
        )


def _build_core_oidc_config(
    request: ServerSetupRequest,
    oidc_provider: SetupOIDCProvider,
    *,
    trusted: Sequence[IndependentApproverConfig],
    approvers: Sequence[SetupApprover],
) -> OIDCEnrollmentConfig:
    selected = [
        item for item in trusted
        if item.principal_id == request.approval_approver_principal_id
    ]
    owner_policy = [
        item for item in approvers
        if item.principal_id == request.approval_approver_principal_id
    ]
    if len(selected) != 1 or len(owner_policy) != 1:
        raise ServerSetupError(
            "approval_conflict",
            "selected Approval trust anchor is unavailable",
        )
    return OIDCEnrollmentConfig(
        **oidc_provider.model_dump(mode="python"),
        verifier_id=request.approval_verifier_id,
        trusted_approvers=tuple(trusted),
        approval_service=ApprovalServiceClientConfig(
            origin=request.approval_public_origin,
            public_origin=request.approval_public_origin,
            service_credential_env="AGENTNET_APPROVAL_CORE_TOKEN",
            approver_principal_id=request.approval_approver_principal_id,
            remote_activation_oidc_subject=owner_policy[0].oidc_subject,
            remote_activation_verified_email_alias=owner_policy[0].verified_email_alias,
        ),
    )


def _require_core_config_matches(
    config: ExtensionConfig,
    *,
    request: ServerSetupRequest,
    core_data: Path,
    oidc: OIDCEnrollmentConfig,
    scanner_trust: ScannerTrustConfig | None,
) -> None:
    if (
        config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT
        or config.domain_id != request.domain_id
        or config.data_dir != core_data / "core"
        or config.database_url != request.database_url
        or config.database_url_env != request.database_url_env
        or config.artifact_mode != request.effective_artifact_mode
        or config.artifact_backend != "postgres-manifest"
        or config.artifact_dir != core_data / "core" / "artifacts"
        or config.public_base_url != request.core_public_origin
        or config.effective_service_audience != request.service_audience
        or config.runtime_instance_id != request.runtime_instance_id
        or config.oidc_enrollment != oidc
        or config.scanner_trust != scanner_trust
        or config.server_agent_capabilities
        != (
            {ServerAgentCapability.OFFLINE_CUSTODY, ServerAgentCapability.ARTIFACT_STORAGE}
            if request.effective_artifact_mode == "enabled"
            else {ServerAgentCapability.OFFLINE_CUSTODY}
        )
        or config.a2a is not None
        or config.local_bindings is not None
        or config.relay is not None
        or config.federation_trust is not None
        or config.postgres_recovery_topology
    ):
        raise ServerSetupError("core_conflict", "existing Core state conflicts with fixed request")


def _load_validated_core_config(
    core_config_path: Path,
    core_account: pwd.struct_passwd,
    *,
    request: ServerSetupRequest,
    core_data: Path,
    oidc: OIDCEnrollmentConfig,
    scanner_trust: ScannerTrustConfig | None,
) -> ExtensionConfig:
    config = load_config_json(
        _read_private_managed_file(
            core_config_path,
            core_account,
            blocker="core_custody",
            max_bytes=1_048_576,
        ).decode("utf-8")
    )
    _require_core_config_matches(
        config,
        request=request,
        core_data=core_data,
        oidc=oidc,
        scanner_trust=scanner_trust,
    )
    return config


def _legacy_remote_activation_oidc(
    oidc: OIDCEnrollmentConfig,
) -> OIDCEnrollmentConfig:
    approval = oidc.approval_service
    if approval is None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "Core OIDC config has no Approval client to bind during upgrade",
        )
    return oidc.model_copy(
        update={
            "approval_service": approval.model_copy(
                update={
                    "remote_activation_oidc_subject": None,
                    "remote_activation_verified_email_alias": None,
                }
            )
        }
    )


def _load_upgrade_compatible_core_config(
    core_config_path: Path,
    core_oidc_path: Path,
    core_account: pwd.struct_passwd,
    *,
    request: ServerSetupRequest,
    core_data: Path,
    oidc: OIDCEnrollmentConfig,
    scanner_trust: ScannerTrustConfig | None,
) -> tuple[ExtensionConfig, bool]:
    """Validate exact current semantics, allowing only missing 0.1.30 owner pins."""

    legacy_oidc = _legacy_remote_activation_oidc(oidc)
    config = load_config_json(
        _read_private_managed_file(
            core_config_path,
            core_account,
            blocker="setup_upgrade_conflict",
            max_bytes=1_048_576,
        ).decode("utf-8")
    )
    core_current = config.oidc_enrollment == oidc
    core_legacy = not core_current and config.oidc_enrollment == legacy_oidc
    if not core_current and not core_legacy:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "existing Core OIDC policy differs beyond supported owner binding migration",
        )
    normalized = (
        config.model_copy(update={"oidc_enrollment": oidc})
        if core_legacy and hasattr(config, "model_copy")
        else config
    )
    _require_core_config_matches(
        normalized,
        request=request,
        core_data=core_data,
        oidc=oidc,
        scanner_trust=scanner_trust,
    )

    standalone = _strict_json_bytes(
        _read_private_managed_file(
            core_oidc_path,
            core_account,
            blocker="setup_upgrade_conflict",
            max_bytes=1_048_576,
        ),
        label="Core OIDC config",
    )
    desired_document = oidc.model_dump(mode="json")
    legacy_document = legacy_oidc.model_dump(mode="json")
    standalone_legacy = standalone == legacy_document
    if standalone != desired_document and not standalone_legacy:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "standalone Core OIDC policy differs beyond supported owner binding migration",
        )
    return normalized, core_legacy or standalone_legacy


def _ensure_c0_responder_runtime(
    *,
    account: pwd.struct_passwd | None,
    forward_only_transition: bool,
    runtime_prepared: bool,
    layout: SetupLayout,
    useradd_executable: Path,
    node_executable: Path,
    agentnet_executable: Path,
    uv_executable: Path,
    steps: list[dict[str, Any]],
) -> pwd.struct_passwd:
    """Create and prepare the fixed C0 responder service identity when needed."""

    data_root = layout.host(C0_RESPONDER_DATA)
    if account is None:
        account = _ensure_account(
            C0_RESPONDER_USER,
            C0_RESPONDER_DATA,
            useradd_executable=useradd_executable,
        )
        steps.append({"id": "c0_responder_identity", "status": "completed"})
        steps.append(
            {
                "id": "c0_responder_private_root",
                "status": _ensure_private_root(data_root, account),
            }
        )
    if forward_only_transition and not runtime_prepared:
        _prepare_managed_service_runtime(
            account,
            data_root=data_root,
            node_executable=node_executable,
            agentnet_executable=agentnet_executable,
            uv_executable=uv_executable,
            stage="c0_responder_runtime_prepare",
        )
        steps.append(
            {"id": "c0_responder_runtime_prepare", "status": "completed"}
        )
    return account


def _provision_approval_service(
    *,
    request: ServerSetupRequest,
    layout: SetupLayout,
    preflight: ServerSetupPreflight,
    approval_account: pwd.struct_passwd,
    approval_preexisting: bool,
    forward_only_transition: bool,
    steps: list[dict[str, Any]],
) -> tuple[
    dict[str, str],
    ApprovalServiceConfig,
    list[IndependentApproverConfig],
    bool,
]:
    """Provision or revalidate Approval and return its exact trust state."""

    config_path = layout.host(APPROVAL_CONFIG)
    approval_state = layout.host(APPROVAL_STATE)
    environment = preflight.approval_environment
    if not approval_preexisting:
        staging_root = layout.host(Path("/run"))
        staging_root.mkdir(parents=True, exist_ok=True)
        staging = Path(
            tempfile.mkdtemp(prefix="agentnet-approval-setup-", dir=staging_root)
        )
        os.chown(staging, approval_account.pw_uid, approval_account.pw_gid)
        os.chmod(staging, 0o700)
        try:
            approvers_copy = staging / "approvers.json"
            owner_oidc_copy = staging / "owner-oidc.json"
            _atomic_write(
                approvers_copy,
                preflight.input_bundle["approval_approvers_file"],
                mode=0o600,
                uid=approval_account.pw_uid,
                gid=approval_account.pw_gid,
            )
            _atomic_write(
                owner_oidc_copy,
                preflight.input_bundle["approval_owner_oidc_file"],
                mode=0o600,
                uid=approval_account.pw_uid,
                gid=approval_account.pw_gid,
            )
            result = _run_as(
                approval_account,
                [
                    str(preflight.runtime.node_executable),
                    str(preflight.runtime.agentnet_executable),
                    "approval",
                    "provision",
                    "--config",
                    str(config_path),
                    "--data-dir",
                    str(approval_state),
                    "--public-origin",
                    request.approval_public_origin,
                    "--rp-id",
                    str(urlsplit(request.approval_public_origin).hostname),
                    "--verifier-id",
                    request.approval_verifier_id,
                    "--approvers",
                    str(approvers_copy),
                    "--owner-oidc-config",
                    str(owner_oidc_copy),
                    "--internal-core-credential-env",
                    "AGENTNET_APPROVAL_CORE_TOKEN",
                ],
                environment=environment,
                stage="approval_provision",
            )
            if result.get("schema") != "agentnet.approval.provision-result.v1":
                raise ServerSetupError(
                    "approval_evidence",
                    "Approval provision evidence schema is invalid",
                )
            _require_private_tree(
                approval_state,
                approval_account,
                blocker="approval_custody",
            )
            steps.append({"id": "approval_provision", "status": "completed"})
        finally:
            shutil.rmtree(staging, ignore_errors=True)
    else:
        _require_private_file(
            config_path,
            approval_account,
            blocker="approval_config",
        )
    approval_config, trusted = _approval_trust(
        config_path,
        approval_account,
        approval_state,
    )
    _require_exact_approval_policy(
        approval_config,
        request=request,
        owner_oidc=preflight.owner_oidc,
        approvers=preflight.approvers,
        approval_state=approval_state,
    )
    deferred_status = approval_preexisting and forward_only_transition
    if approval_preexisting:
        if not deferred_status:
            _run_as(
                approval_account,
                [
                    str(preflight.runtime.node_executable),
                    str(preflight.runtime.agentnet_executable),
                    "approval",
                    "status",
                    "--config",
                    str(config_path),
                ],
                environment=environment,
                stage="approval_status",
            )
        steps.append(
            {"id": "approval_provision", "status": "already_satisfied"}
        )
    return environment, approval_config, trusted, deferred_status


def _provision_core_service(
    *,
    request: ServerSetupRequest,
    layout: SetupLayout,
    preflight: ServerSetupPreflight,
    core_account: pwd.struct_passwd,
    core_preexisting: bool,
    prevalidated_oidc: OIDCEnrollmentConfig | None,
    trusted: list[IndependentApproverConfig],
    steps: list[dict[str, Any]],
) -> tuple[OIDCEnrollmentConfig, ExtensionConfig, str, dict[str, str]]:
    """Provision or revalidate Core against the exact Approval trust state."""

    core_data = layout.host(CORE_DATA)
    core_runtime = core_data / "core"
    config_path = layout.host(CORE_CONFIG)
    oidc = _build_core_oidc_config(
        request,
        preflight.oidc_provider,
        trusted=trusted,
        approvers=preflight.approvers,
    )
    if prevalidated_oidc is not None and oidc != prevalidated_oidc:
        raise ServerSetupError(
            "approval_conflict",
            "Approval trust changed during setup",
        )
    oidc_path = layout.host(CORE_OIDC_CONFIG)
    oidc_payload = (
        json.dumps(oidc.model_dump(mode="json"), indent=2, sort_keys=True).encode()
        + b"\n"
    )
    steps.append(
        {
            "id": "core_oidc_config",
            "status": _atomic_write(
                oidc_path,
                oidc_payload,
                mode=0o600,
                uid=core_account.pw_uid,
                gid=core_account.pw_gid,
            ),
        }
    )
    scanner_path = core_data / "scanner-trust.json"
    scanner_trust = preflight.scanner_trust
    if scanner_trust is not None:
        scanner_payload = (
            json.dumps(
                scanner_trust.model_dump(mode="json"),
                indent=2,
                sort_keys=True,
            ).encode()
            + b"\n"
        )
        steps.append(
            {
                "id": "scanner_trust",
                "status": _atomic_write(
                    scanner_path,
                    scanner_payload,
                    mode=0o600,
                    uid=core_account.pw_uid,
                    gid=core_account.pw_gid,
                ),
            }
        )
    else:
        if scanner_path.exists() or scanner_path.is_symlink():
            raise ServerSetupError(
                "core_conflict",
                "communication-only Core state contains forbidden scanner trust",
            )
        _require_communication_only_artifact_absence(core_runtime)
        steps.append(
            {"id": "scanner_trust", "status": "disabled_not_created"}
        )
    environment = preflight.core_environment
    if not core_preexisting:
        result = _run_as(
            core_account,
            _core_create_arguments(
                request,
                node_executable=preflight.runtime.node_executable,
                executable=preflight.runtime.agentnet_executable,
                core_config_path=config_path,
                core_data=core_data,
                oidc_path=oidc_path,
                scanner_path=scanner_path,
                scanner_trust=scanner_trust,
            ),
            environment=environment,
            stage="core_create",
            accepted_returncodes=frozenset({1}),
        )
        _require_private_file(
            config_path,
            core_account,
            blocker="core_custody",
        )
        _require_core_create_evidence(
            result,
            config_path,
            artifact_mode=request.effective_artifact_mode,
        )
        bootstrap_status = "completed"
    else:
        _require_private_file(
            config_path,
            core_account,
            blocker="core_custody",
        )
        bootstrap_status = "revalidated"
    _require_private_tree(core_runtime, core_account, blocker="core_custody")
    config = _load_validated_core_config(
        config_path,
        core_account,
        request=request,
        core_data=core_data,
        oidc=oidc,
        scanner_trust=scanner_trust,
    )
    return oidc, config, bootstrap_status, environment


def _configure_scanner_worker(
    *,
    scanner_setup: ScannerSetupSpec | None,
    layout: SetupLayout,
    core_account: pwd.struct_passwd,
    steps: list[dict[str, Any]],
) -> None:
    """Write and verify scanner-worker state, or record disabled capability."""

    if scanner_setup is None:
        steps.append(
            {
                "id": "scanner_readiness",
                "status": "not_configured_file_capability_disabled",
            }
        )
        return
    config_payload = (
        json.dumps(
            {
                "endpoint": scanner_setup.endpoint.uri,
                "engine_version": scanner_setup.engine_version,
                "key_file": str(SCANNER_SIGNING_KEY),
                "profile_digest": scanner_setup.profile_digest,
                "rules_digest": scanner_setup.rules_digest,
                "scanner_id": scanner_setup.scanner_id,
                "scanner_key_epoch": scanner_setup.scanner_key_epoch,
                "schema": "agentnet.scanner-worker.config.v1",
                "signature_max_age_seconds": (
                    scanner_setup.signature_max_age_seconds
                ),
                "signature_updated_at": scanner_setup.signature_updated_at,
                "signature_version": scanner_setup.signature_version,
            },
            sort_keys=True,
            separators=(",", ":"),
        ).encode()
        + b"\n"
    )
    for step_id, path, payload in (
        (
            "scanner_signing_key_custody",
            layout.host(SCANNER_SIGNING_KEY),
            scanner_setup.key_input,
        ),
        (
            "scanner_worker_config",
            layout.host(SCANNER_WORKER_CONFIG),
            config_payload,
        ),
    ):
        steps.append(
            {
                "id": step_id,
                "status": _atomic_write(
                    path,
                    payload,
                    mode=0o600,
                    uid=core_account.pw_uid,
                    gid=core_account.pw_gid,
                ),
            }
        )
    steps.append(
        {
            "id": "scanner_readiness",
            "status": _require_scanner_readiness(scanner_setup)["status"],
        }
    )
