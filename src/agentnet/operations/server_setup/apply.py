"""Apply the fixed ordinary-server setup orchestration plan."""

from __future__ import annotations

import os
import re
import stat
from pathlib import Path
from typing import Any

from agentnet.operations.config import ExtensionConfig, OIDCEnrollmentConfig

from . import upgrade as _upgrade
from . import upgrade_recovery as _upgrade_recovery
from . import upgrade_state as _upgrade_state
from .activation import (
    C0_RESPONDER_TERMINAL,
    _prepare_c0_responder_activation,
    _start_managed_server_services,
)
from .custody import (
    _account_fact,
    _atomic_write,
    _ensure_account,
    _ensure_private_root,
    _ensure_root_private_directory,
    _private_entry_exists,
    _read_managed_exact,
    _require_communication_only_artifact_absence,
    _require_private_tree,
    _run_as,
)
from .database import _postgres_peer_gate
from .models import ServerSetupError, ServerSetupRequest, SetupLayout
from .preflight import _MAX_CONFIG_BYTES, _planned_setup_evidence, _server_setup_preflight
from .provisioning import (
    APPROVAL_STATE,
    CORE_OIDC_CONFIG,
    _approval_trust,
    _build_core_oidc_config,
    _configure_scanner_worker,
    _ensure_c0_responder_runtime,
    _load_upgrade_compatible_core_config,
    _load_validated_core_config,
    _provision_approval_service,
    _provision_core_service,
    _require_exact_approval_policy,
)
from .systemd import (
    APPROVAL_CONFIG,
    APPROVAL_DATA,
    APPROVAL_ENV,
    APPROVAL_USER,
    C0_RESPONDER_CONFIG,
    C0_RESPONDER_DATA,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_DATA,
    CORE_ENV,
    CORE_USER,
    MANAGED_UNITS,
    SECRET_ROOT,
)
from .upgrade_state import SETUP_ATTEMPT, SETUP_MARKER, SETUP_UPGRADE_JOURNAL


def apply_server_setup(
    request: ServerSetupRequest,
    *,
    start: bool,
    expected_request_digest: str,
    layout: SetupLayout = SetupLayout(),
    _allow_test_layout: bool = False,
) -> dict[str, Any]:
    pending: dict[str, Any] = {}
    verified: dict[str, bool] = {}
    try:
        return _apply_server_setup(
            request,
            start=start,
            expected_request_digest=expected_request_digest,
            layout=layout,
            _allow_test_layout=_allow_test_layout,
            _pending_upgrade=pending,
            _verified=verified,
        )
    except BaseException as exc:
        try:
            _upgrade_recovery._rollback_pending_upgrade(pending)
        except ServerSetupError as rollback_exc:
            if (
                isinstance(exc, ServerSetupError)
                and exc.blocker == "managed_path_conflict"
            ):
                if verified.get("identity_enrolled"):
                    exc.identity_enrolled = True
                raise exc from rollback_exc
            if verified.get("identity_enrolled"):
                rollback_exc.identity_enrolled = True
            raise rollback_exc from exc
        if isinstance(exc, ServerSetupError) and verified.get("identity_enrolled"):
            exc.identity_enrolled = True
        raise


def _apply_server_setup(
    request: ServerSetupRequest,
    *,
    start: bool,
    expected_request_digest: str,
    layout: SetupLayout,
    _allow_test_layout: bool,
    _pending_upgrade: dict[str, Any],
    _verified: dict[str, bool],
) -> dict[str, Any]:
    preflight = _server_setup_preflight(request, layout=layout)
    actual_digest = preflight.request_digest
    if not re.fullmatch(r"[a-f0-9]{64}", expected_request_digest) or actual_digest != expected_request_digest:
        raise ServerSetupError(
            "approval_digest_mismatch",
            "current setup request does not match the frozen human-approved digest",
        )
    approved_digest = expected_request_digest
    if layout.root != Path("/") and not _allow_test_layout:
        raise ServerSetupError("test_layout", "apply requires the real host layout")
    if not _allow_test_layout and os.geteuid() != 0:
        raise ServerSetupError("privilege_required", "server setup apply requires root")
    try:
        import fcntl as posix_fcntl
    except ModuleNotFoundError as exc:
        raise ServerSetupError("unsupported_host", "ordinary server setup requires POSIX file locking") from exc
    root_uid = 0 if layout.root == Path("/") else os.geteuid()
    root_gid = 0 if layout.root == Path("/") else os.getegid()
    _ensure_root_private_directory(
        layout.lock.parent,
        uid=root_uid,
        gid=root_gid,
        label="setup_lock",
    )
    try:
        lock_descriptor = os.open(
            layout.lock,
            os.O_WRONLY
            | os.O_CREAT
            | os.O_NONBLOCK
            | getattr(os, "O_NOFOLLOW", 0)
            | getattr(os, "O_CLOEXEC", 0),
            0o600,
        )
    except OSError as exc:
        raise ServerSetupError("setup_lock", "AgentNet setup lock custody is unsafe") from exc
    lock_metadata = os.fstat(lock_descriptor)
    if (
        not stat.S_ISREG(lock_metadata.st_mode)
        or lock_metadata.st_nlink != 1
        or lock_metadata.st_uid != root_uid
        or lock_metadata.st_gid != root_gid
        or stat.S_IMODE(lock_metadata.st_mode) != 0o600
    ):
        os.close(lock_descriptor)
        raise ServerSetupError("setup_lock", "AgentNet setup lock custody conflicts with fixed profile")
    try:
        try:
            posix_fcntl.flock(lock_descriptor, posix_fcntl.LOCK_EX | posix_fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ServerSetupError("setup_locked", "another AgentNet server setup is active") from exc
        core_data = layout.host(CORE_DATA)
        approval_data = layout.host(APPROVAL_DATA)
        c0_responder_data = layout.host(C0_RESPONDER_DATA)
        c0_responder_config_path = layout.host(C0_RESPONDER_CONFIG)
        c0_responder_terminal_path = layout.host(C0_RESPONDER_TERMINAL)
        core_config_path = layout.host(CORE_CONFIG)
        approval_config_path = layout.host(APPROVAL_CONFIG)
        approval_state = layout.host(APPROVAL_STATE)
        setup_marker = layout.host(SETUP_MARKER)
        setup_attempt = layout.host(SETUP_ATTEMPT)
        journal_path = layout.host(SETUP_UPGRADE_JOURNAL)
        core_env_path = layout.host(CORE_ENV)
        approval_env_path = layout.host(APPROVAL_ENV)

        locked_preflight = _server_setup_preflight(request, layout=layout)
        if locked_preflight.request_digest != approved_digest:
            raise ServerSetupError("request_changed", "setup request, inputs, or runtime changed after approved preflight")
        preflight = locked_preflight
        plan = _planned_setup_evidence(request, preflight)
        node_executable = preflight.runtime.node_executable
        uv_executable = preflight.runtime.uv_executable
        executable = preflight.runtime.agentnet_executable
        systemctl_executable = preflight.runtime.systemctl_executable
        useradd_executable = preflight.runtime.useradd_executable
        input_bundle = preflight.input_bundle
        scanner_setup = preflight.scanner_setup
        oidc_provider = preflight.oidc_provider
        owner_oidc = preflight.owner_oidc
        approvers = preflight.approvers
        scanner_trust = preflight.scanner_trust
        (
            existing_marker_payload,
            existing_marker,
            topology_transition,
            forward_only_upgrade,
            forward_only_transition,
        ) = _upgrade._classify_setup_transition(request=request,
        preflight=preflight,
        layout=layout,
        approved_digest=approved_digest,
        root_uid=root_uid,
        root_gid=root_gid,)
        core_input = input_bundle["core_environment_file"]
        approval_input = input_bundle["approval_environment_file"]
        if topology_transition:
            if (
                _account_fact(CORE_USER, CORE_DATA) != "already_satisfied"
                or _account_fact(APPROVAL_USER, APPROVAL_DATA)
                != "already_satisfied"
                or not core_data.is_dir()
                or core_data.is_symlink()
                or not approval_data.is_dir()
                or approval_data.is_symlink()
                or not layout.host(SECRET_ROOT).is_dir()
                or layout.host(SECRET_ROOT).is_symlink()
                or _read_managed_exact(
                    core_env_path,
                    uid=root_uid,
                    gid=root_gid,
                    mode=0o600,
                    blocker="setup_upgrade_conflict",
                    label="legacy Core environment",
                    max_bytes=_MAX_CONFIG_BYTES,
                )
                != core_input
                or _read_managed_exact(
                    approval_env_path,
                    uid=root_uid,
                    gid=root_gid,
                    mode=0o600,
                    blocker="setup_upgrade_conflict",
                    label="legacy Approval environment",
                    max_bytes=_MAX_CONFIG_BYTES,
                )
                != approval_input
            ):
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "legacy communication-only prerequisite state is incomplete",
                )
        core_account = _ensure_account(
            CORE_USER,
            CORE_DATA,
            useradd_executable=useradd_executable,
        )
        postgres_evidence = _postgres_peer_gate(core_account, request.database_url)
        approval_account = _ensure_account(
            APPROVAL_USER,
            APPROVAL_DATA,
            useradd_executable=useradd_executable,
        )
        c0_responder_account = (
            None
            if topology_transition
            else _ensure_account(
                C0_RESPONDER_USER,
                C0_RESPONDER_DATA,
                useradd_executable=useradd_executable,
            )
        )
        steps: list[dict[str, Any]] = [
            {"id": "preflight", "status": "completed"},
            {"id": "core_identity", "status": "completed"},
            {"id": "postgres_service_identity", "status": postgres_evidence["status"]},
            {"id": "approval_identity", "status": "completed"},
        ]
        if c0_responder_account is not None:
            steps.append({"id": "c0_responder_identity", "status": "completed"})
        steps.append({"id": "core_private_root", "status": _ensure_private_root(core_data, core_account)})
        steps.append({"id": "approval_private_root", "status": _ensure_private_root(approval_data, approval_account)})
        if c0_responder_account is not None:
            steps.append(
                {
                    "id": "c0_responder_private_root",
                    "status": _ensure_private_root(
                        c0_responder_data,
                        c0_responder_account,
                    ),
                }
            )
        approval_preexisting = _private_entry_exists(
            approval_config_path,
            approval_account,
            expected="file",
            blocker="approval_config",
        )
        approval_state_preexisting = _private_entry_exists(
            approval_state,
            approval_account,
            expected="directory",
            blocker="approval_custody",
        )
        core_preexisting = _private_entry_exists(
            core_config_path,
            core_account,
            expected="file",
            blocker="core_custody",
        )
        c0_responder_config_preexisting = (
            False
            if c0_responder_account is None
            else _private_entry_exists(
                c0_responder_config_path,
                c0_responder_account,
                expected="file",
                blocker="c0_responder_custody",
            )
        )
        c0_responder_terminal_preexisting = (
            False
            if c0_responder_account is None
            else _private_entry_exists(
                c0_responder_terminal_path,
                c0_responder_account,
                expected="file",
                blocker="c0_responder_custody",
            )
        )
        core_runtime = core_data / "core"
        core_oidc_path = layout.host(CORE_OIDC_CONFIG)
        if request.effective_artifact_mode == "disabled":
            _require_communication_only_artifact_absence(core_runtime)
        core_runtime_preexisting = _private_entry_exists(
            core_runtime,
            core_account,
            expected="directory",
            blocker="core_custody",
        )
        unit_paths = {unit: layout.unit(unit) for unit in MANAGED_UNITS}
        preexisting_managed_state = any(
            (
                approval_preexisting,
                approval_state_preexisting,
                core_preexisting,
                core_runtime_preexisting,
                c0_responder_config_preexisting,
                c0_responder_terminal_preexisting,
                *(path.exists() or path.is_symlink() for path in unit_paths.values()),
            )
        )
        if (
            existing_marker is None
            and preexisting_managed_state
            and not (setup_attempt.exists() or setup_attempt.is_symlink())
        ):
            raise ServerSetupError(
                "clean_state_required",
                "pre-existing AgentNet state has no current-package setup custody",
            )
        steps.append(
            {
                "id": "setup_marker_root",
                "status": _ensure_root_private_directory(
                    setup_marker.parent,
                    uid=root_uid,
                    gid=root_gid,
                    label="setup_marker",
                ),
            }
        )
        if approval_state_preexisting:
            _require_private_tree(approval_state, approval_account, blocker="approval_custody")
        if core_runtime_preexisting:
            _require_private_tree(core_runtime, core_account, blocker="core_custody")
        prevalidated_oidc: OIDCEnrollmentConfig | None = None
        prevalidated_config: ExtensionConfig | None = None
        legacy_owner_policy = False
        if approval_preexisting and core_preexisting:
            approval_config_before, trusted_before = _approval_trust(
                approval_config_path,
                approval_account,
                approval_state,
            )
            _require_exact_approval_policy(
                approval_config_before,
                request=request,
                owner_oidc=owner_oidc,
                approvers=approvers,
                approval_state=approval_state,
            )
            prevalidated_oidc = _build_core_oidc_config(
                request,
                oidc_provider,
                trusted=trusted_before,
                approvers=approvers,
            )
            prevalidated_config, legacy_owner_policy = _load_upgrade_compatible_core_config(
                core_config_path,
                core_oidc_path,
                core_account,
                request=request,
                core_data=core_data,
                oidc=prevalidated_oidc,
                scanner_trust=scanner_trust,
            )
        upgrade_status = _upgrade._prepare_supported_upgrade(request=request,
        preflight=preflight,
        layout=layout,
        existing_marker=existing_marker,
        existing_marker_payload=existing_marker_payload,
        approved_digest=approved_digest,
        approval_account=approval_account,
        approval_preexisting=approval_preexisting,
        core_account=core_account,
        core_preexisting=core_preexisting,
        prevalidated_config=prevalidated_config,
        uid=root_uid,
        gid=root_gid,
        pending=_pending_upgrade,)
        steps.append({"id": "package_upgrade", "status": upgrade_status})
        attempt_status, attempt_active = _upgrade_state._prepare_setup_attempt(setup_attempt,
        existing_marker=existing_marker,
        preexisting_state=preexisting_managed_state,
        request_digest=approved_digest,
        uid=root_uid,
        gid=root_gid,)
        steps.append({"id": "setup_attempt", "status": attempt_status})
        _configure_scanner_worker(
            scanner_setup=scanner_setup,
            layout=layout,
            core_account=core_account,
            steps=steps,
        )
        def write_setup_units() -> dict[str, bytes]:
            return _upgrade._write_setup_units(preflight=preflight,
            layout=layout,
            pending_upgrade=_pending_upgrade,
            steps=steps,
            root_uid=root_uid,
            root_gid=root_gid,)

        def commit_setup_profile(
            unit_payloads: dict[str, bytes] | None = None,
        ) -> dict[str, bytes]:
            return _upgrade._commit_setup_profile(preflight=preflight,
            layout=layout,
            request=request,
            pending_upgrade=_pending_upgrade,
            steps=steps,
            root_uid=root_uid,
            root_gid=root_gid,
            approval_account=approval_account,
            core_account=core_account,
            existing_marker_payload=existing_marker_payload,
            existing_marker=existing_marker,
            approved_digest=approved_digest,
            forward_only_upgrade=forward_only_upgrade,
            attempt_active=attempt_active,
            unit_payloads=unit_payloads,)


        if legacy_owner_policy:
            if prevalidated_oidc is None:  # pragma: no cover - guarded above
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "legacy owner policy was not prevalidated",
                )
            steps.append(
                {
                    "id": "core_remote_activation_policy_upgrade",
                    "status": _upgrade._migrate_legacy_remote_activation_policy(core_config_path=core_config_path,
                    core_oidc_path=core_oidc_path,
                    core_account=core_account,
                    oidc=prevalidated_oidc,
                    pending=_pending_upgrade,),
                }
            )
        secret_root = layout.host(SECRET_ROOT)
        steps.append(
            {
                "id": "secret_root",
                "status": _ensure_root_private_directory(
                    secret_root,
                    uid=root_uid,
                    gid=root_gid,
                    label="secret",
                ),
            }
        )
        steps.append({"id": "core_environment", "status": _atomic_write(core_env_path, core_input, mode=0o600, uid=root_uid, gid=root_gid)})
        steps.append({"id": "approval_environment", "status": _atomic_write(approval_env_path, approval_input, mode=0o600, uid=root_uid, gid=root_gid)})

        (
            approval_environment,
            approval_config,
            trusted,
            deferred_approval_status,
        ) = _provision_approval_service(
            request=request,
            layout=layout,
            preflight=preflight,
            approval_account=approval_account,
            approval_preexisting=approval_preexisting,
            forward_only_transition=forward_only_transition,
            steps=steps,
        )
        (
            oidc,
            config,
            bootstrap_status,
            core_environment,
        ) = _provision_core_service(
            request=request,
            layout=layout,
            preflight=preflight,
            core_account=core_account,
            core_preexisting=core_preexisting,
            prevalidated_oidc=prevalidated_oidc,
            trusted=trusted,
            steps=steps,
        )
        rollback_capable_upgrade = (
            _pending_upgrade.get("rollback_capable_upgrade") is True
        )
        endpoint_lifecycle_result: dict[str, Any] | None = None
        unit_payloads: dict[str, bytes] | None = None
        if rollback_capable_upgrade:
            unit_payloads = write_setup_units()
        elif forward_only_upgrade:
            unit_payloads = commit_setup_profile()
        c0_runtime_prepared = False
        if forward_only_transition:
            runtime_accounts = [
                (approval_account, approval_data, "approval_runtime_prepare"),
                (core_account, core_data, "core_runtime_prepare"),
            ]
            if c0_responder_account is not None:
                runtime_accounts.append(
                    (
                        c0_responder_account,
                        c0_responder_data,
                        "c0_responder_runtime_prepare",
                    )
                )
                c0_runtime_prepared = True
            _upgrade._prepare_forward_only_transition(rollback_capable_upgrade=rollback_capable_upgrade,
            pending_upgrade=_pending_upgrade,
            systemctl_executable=systemctl_executable,
            runtime_accounts=runtime_accounts,
            node_executable=node_executable,
            agentnet_executable=executable,
            uv_executable=uv_executable,
            steps=steps,)
            if deferred_approval_status:
                _run_as(
                    approval_account,
                    [
                        str(node_executable),
                        str(executable),
                        "approval",
                        "status",
                        "--config",
                        str(approval_config_path),
                    ],
                    environment=approval_environment,
                    stage="approval_status",
                )
        if rollback_capable_upgrade:
            endpoint_lifecycle_result = _upgrade._migrate_rollback_capable_upgrade(pending_upgrade=_pending_upgrade,
            core_account=core_account,
            request=request,
            steps=steps,)
        if core_preexisting and not rollback_capable_upgrade:
            _, bootstrap_status = _upgrade._run_bootstrap_idempotently(core_account,
            [str(node_executable), str(executable), "bootstrap-server-agent", "--config", str(core_config_path)],
            environment=core_environment,
            expected_domain_id=request.domain_id,)
            if bootstrap_status == "completed":
                bootstrap_status = "revalidated"
            _require_private_tree(core_runtime, core_account, blocker="core_custody")
            config = _load_validated_core_config(
                core_config_path,
                core_account,
                request=request,
                core_data=core_data,
                oidc=oidc,
                scanner_trust=scanner_trust,
            )
        elif rollback_capable_upgrade:
            bootstrap_status = "schema_v7_migrated_preserved_identity"
        if forward_only_transition and not rollback_capable_upgrade:
            _upgrade_state._clear_upgrade_journal(journal_path)
        c0_responder_account = _ensure_c0_responder_runtime(
            account=c0_responder_account,
            forward_only_transition=forward_only_transition,
            runtime_prepared=c0_runtime_prepared,
            layout=layout,
            useradd_executable=useradd_executable,
            node_executable=node_executable,
            agentnet_executable=executable,
            uv_executable=uv_executable,
            steps=steps,
        )
        steps.append({"id": "core_bootstrap", "status": bootstrap_status})
        (
            identity_enrolled,
            c0_responder_required,
            responder_payload,
            supersession_evidence,
            responder_config_status,
        ) = _prepare_c0_responder_activation(
            config=config,
            request=request,
            start=start,
            layout=layout,
            c0_responder_account=c0_responder_account,
            core_account=core_account,
            verified=_verified,
        )
        if responder_config_status is not None:
            steps.append(
                {
                    "id": "c0_responder_config",
                    "status": responder_config_status,
                }
            )

        approval_config, trusted_after = _approval_trust(
            approval_config_path,
            approval_account,
            approval_state,
        )
        _require_exact_approval_policy(
            approval_config,
            request=request,
            owner_oidc=owner_oidc,
            approvers=approvers,
            approval_state=approval_state,
        )
        if trusted_after != trusted:
            raise ServerSetupError("approval_conflict", "Approval trust changed during setup")

        if unit_payloads is None:
            unit_payloads = commit_setup_profile()
        assert unit_payloads is not None
        if start:
            status, next_action = _start_managed_server_services(
                request=request,
                layout=layout,
                preflight=preflight,
                oidc=oidc,
                identity_enrolled=identity_enrolled,
                c0_responder_required=c0_responder_required,
                responder_payload=responder_payload,
                c0_responder_account=c0_responder_account,
                supersession_evidence=supersession_evidence,
                steps=steps,
            )
        else:
            steps.append(
                {"id": "service_start", "status": "pending_explicit_start"}
            )
            status = "configured_not_started"
            next_action = (
                "rerun with the same --expected-request-digest plus --apply "
                "--start inside the approved scope"
            )
        if rollback_capable_upgrade:
            commit_setup_profile(unit_payloads)
            _upgrade_state._clear_upgrade_journal(journal_path)
        return {
            **plan,
            "status": status,
            "steps": steps,
            "next": next_action,
            "authority_granted": False,
            "identity_enrolled": identity_enrolled,
            "endpoint_lifecycle": endpoint_lifecycle_result,
            "production_durability_proven": False,
        }
    finally:
        os.close(lock_descriptor)
