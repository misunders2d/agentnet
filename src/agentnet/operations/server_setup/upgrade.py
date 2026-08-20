"""Setup marker, journal, upgrade, recovery, and rollback ownership."""

from __future__ import annotations

import base64
import hashlib
import json
import os
import re
import tempfile
from pathlib import Path
from typing import Any, Literal, Mapping

if os.name == "posix":
    import pwd

from agentnet import __version__
from agentnet.operations.config import ExtensionConfig, OIDCEnrollmentConfig
from agentnet.storage.migrations import MIGRATIONS

from . import systemd as _systemd
from . import upgrade_state as _upgrade_state
from .custody import (
    _MAX_UNIT_BYTES,
    _account_fact,
    _atomic_replace_exact,
    _atomic_write,
    _managed_config_digest,
    _prepare_managed_service_runtime,
    _read_managed_exact,
    _read_managed_unit,
    _read_private_managed_file,
    _read_setup_marker,
    _remove_managed_unit_exact,
    _run_as,
    _write_managed_unit,
)
from .database import (
    _LIFECYCLE_PRESERVED_TABLES,
    _LIFECYCLE_SETUP_UPGRADE,
    _LIFECYCLE_SOURCE_SCHEMA,
    _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA,
    _run_v0145_database_operation_as,
)
from .models import (
    SETUP_ROOT,
    ServerSetupError,
    ServerSetupPreflight,
    ServerSetupRequest,
    SetupLayout,
)
from .preflight import _MAX_CONFIG_BYTES, _strict_json_bytes
from .provisioning import CORE_OIDC_CONFIG, _legacy_remote_activation_oidc
from .upgrade_state import (
    SETUP_ATTEMPT,
    SETUP_MARKER,
    SETUP_UPGRADE_JOURNAL,
    _FORWARD_ONLY_SETUP_UPGRADES,
    _RESPONSE_LOSS_BLOCKERS,
    _UPGRADE_JOURNAL_SCHEMA,
)
from .systemd import (
    APPROVAL_CONFIG,
    APPROVAL_UNIT,
    C0_RESPONDER_DATA,
    C0_RESPONDER_UNIT,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_UNIT,
    CREDENTIAL_RENEW_STATE,
    CREDENTIAL_RENEW_TIMER,
    CREDENTIAL_RENEW_UNIT,
    LEGACY_COMMUNICATION_ONLY_UNITS,
    MANAGED_UNITS,
    UnitRenderError,
    _managed_service_runtime as _rendered_service_runtime,
    render_managed_units,
)
def _managed_service_runtime(data_root: Path) -> Path:
    """Return the package-generation runtime root retained across rollback."""

    return _rendered_service_runtime(data_root, package_version=__version__)


def render_units(
    node_executable: Path,
    executable: Path,
    uv_executable: Path,
) -> dict[str, bytes]:
    try:
        return render_managed_units(
            node_executable,
            executable,
            uv_executable,
            package_version=__version__,
        )
    except UnitRenderError as exc:
        raise ServerSetupError("unit_input", str(exc)) from exc


def _prepare_forward_only_transition(
    *,
    rollback_capable_upgrade: bool,
    pending_upgrade: dict[str, Any],
    systemctl_executable: Path,
    runtime_accounts: list[tuple[pwd.struct_passwd, Path, str]],
    node_executable: Path,
    agentnet_executable: Path,
    uv_executable: Path,
    steps: list[dict[str, Any]],
) -> None:
    """Quiesce managed units and prepare exact package runtimes for upgrade."""

    if rollback_capable_upgrade:
        pending_upgrade["service_state_changed"] = True
    quiesce_status = _systemd._run_systemctl_sequence_or_reconcile(
        systemctl_executable,
        (
            ["daemon-reload"],
            ["disable", "--now", C0_RESPONDER_UNIT],
            ["reset-failed", C0_RESPONDER_UNIT],
            ["disable", "--now", CREDENTIAL_RENEW_TIMER],
            ["reset-failed", CREDENTIAL_RENEW_TIMER],
            ["stop", CREDENTIAL_RENEW_UNIT],
            ["reset-failed", CREDENTIAL_RENEW_UNIT],
            ["disable", "--now", CORE_UNIT],
            ["reset-failed", CORE_UNIT],
            ["disable", "--now", APPROVAL_UNIT],
            ["reset-failed", APPROVAL_UNIT],
        ),
        reconcile=lambda: _systemd._verify_upgrade_quiescence(systemctl_executable),
    )
    if quiesce_status == "completed":
        _systemd._verify_upgrade_quiescence(systemctl_executable)
    steps.append(
        {
            "id": "package_upgrade_service_quiescence",
            "status": quiesce_status,
        }
    )

    for account, data_root, runtime_stage in runtime_accounts:
        _prepare_managed_service_runtime(
            account,
            data_root=data_root,
            node_executable=node_executable,
            agentnet_executable=agentnet_executable,
            uv_executable=uv_executable,
            stage=runtime_stage,
        )
        steps.append({"id": runtime_stage, "status": "completed"})
    return None



def _require_core_bootstrap_evidence(
    evidence: Mapping[str, Any],
    *,
    expected_domain_id: str,
) -> None:
    domain = evidence.get("domain")
    recovery = evidence.get("recovery")
    storage = evidence.get("storage")
    audit = evidence.get("audit")
    binding = evidence.get("deployment_binding")
    if (
        not isinstance(domain, dict)
        or domain.get("domain_id") != expected_domain_id
        or not isinstance(recovery, dict)
        or recovery.get("ready") is not True
        or not isinstance(storage, dict)
        or storage.get("ready") is not True
        or not isinstance(audit, dict)
        or audit.get("valid") is not True
        or not isinstance(binding, dict)
        or (
            binding.get("ready") is True
            and (
                not isinstance(binding.get("credential_supersession"), dict)
                or binding["credential_supersession"].get("status")
                not in {"not_applicable", "verified"}
            )
        )
    ):
        raise ServerSetupError(
            "core_bootstrap_evidence",
            "Core bootstrap evidence did not prove exact healthy durable state",
        )


def _run_bootstrap_idempotently(
    account: pwd.struct_passwd,
    argv: list[str],
    *,
    environment: Mapping[str, str],
    expected_domain_id: str,
) -> tuple[dict[str, Any], str]:
    """Run the idempotent Core bootstrap, retrying exactly one lost response.

    ``bootstrap-server-agent`` is idempotent, so a lost or truncated response is
    reconciled by rerunning it and requiring fresh exact evidence.  A refused or
    unhealthy bootstrap is never retried and never reported as reconciled.
    """

    attempts = 2
    for attempt in range(1, attempts + 1):
        try:
            evidence = _run_as(
                account,
                argv,
                environment=environment,
                stage="core_bootstrap",
            )
        except ServerSetupError as exc:
            if attempt >= attempts or exc.blocker not in _RESPONSE_LOSS_BLOCKERS:
                raise
            continue
        _require_core_bootstrap_evidence(evidence, expected_domain_id=expected_domain_id)
        return evidence, "completed" if attempt == 1 else "reconciled_after_response_loss"
    raise ServerSetupError("core_bootstrap_evidence", "Core bootstrap did not produce exact evidence")


def _migrate_legacy_remote_activation_policy(
    *,
    core_config_path: Path,
    core_oidc_path: Path,
    core_account: pwd.struct_passwd,
    oidc: OIDCEnrollmentConfig,
    pending: dict[str, Any],
) -> str:
    journal = pending.get("journal")
    if not isinstance(journal, Mapping):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "legacy owner binding migration requires an active upgrade journal",
        )
    previous = _upgrade_state._journaled_config_payloads(journal)
    legacy_document = _legacy_remote_activation_oidc(oidc).model_dump(mode="json")
    desired_document = oidc.model_dump(mode="json")

    previous_oidc_document = _strict_json_bytes(
        previous["core_oidc_config"],
        label="journaled Core OIDC config",
    )
    if previous_oidc_document not in (legacy_document, desired_document):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "journaled Core OIDC policy is not an exact supported upgrade source",
        )
    oidc_payload = json.dumps(desired_document, indent=2, sort_keys=True).encode() + b"\n"

    previous_core_document = _strict_json_bytes(
        previous["core_config"],
        label="journaled Core config",
    )
    if previous_core_document.get("oidc_enrollment") not in (
        legacy_document,
        desired_document,
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "journaled Core config is not an exact supported owner binding source",
        )
    replacement_core_document = dict(previous_core_document)
    replacement_core_document["oidc_enrollment"] = desired_document
    core_payload = (
        json.dumps(replacement_core_document, indent=2, sort_keys=True).encode() + b"\n"
    )
    replacements = {
        "core_config": core_payload,
        "core_oidc_config": oidc_payload,
    }
    pending["replacement_configs"] = replacements
    core_status = _upgrade_state._write_journaled_core_config(core_config_path,
    core_payload,
    account=core_account,
    previous=previous["core_config"],)
    oidc_status = _upgrade_state._write_journaled_core_config(core_oidc_path,
    oidc_payload,
    account=core_account,
    previous=previous["core_oidc_config"],)
    return (
        "updated_package_upgrade"
        if "updated_package_upgrade" in {core_status, oidc_status}
        else "already_satisfied"
    )


def _resume_supported_upgrade(
    *,
    request: ServerSetupRequest,
    preflight: ServerSetupPreflight,
    layout: SetupLayout,
    existing_marker: dict[str, Any] | None,
    existing_marker_payload: bytes | None,
    approved_digest: str,
    approval_account: pwd.struct_passwd,
    approval_preexisting: bool,
    core_account: pwd.struct_passwd,
    core_preexisting: bool,
    uid: int,
    gid: int,
    pending: dict[str, Any],
) -> str | None:
    """Resume or reconcile one exact journaled setup upgrade."""

    journal_path = layout.host(SETUP_UPGRADE_JOURNAL)
    journal = _upgrade_state._read_upgrade_journal(journal_path, uid=uid, gid=gid)
    if journal is None:
        return None
    approval_config_path = layout.host(APPROVAL_CONFIG)
    core_config_path = layout.host(CORE_CONFIG)
    unit_paths = {unit: layout.unit(unit) for unit in MANAGED_UNITS}
    superseded_committed_target = (
        existing_marker is not None
        and existing_marker_payload is not None
        and existing_marker.get("request_digest") == journal["to_request_digest"]
        and existing_marker.get("package_version") == journal["to_package_version"]
        and existing_marker.get("previous_marker_digest")
        == journal["from_marker_sha256"]
        and journal["to_package_version"] != __version__
        and _upgrade_state._forward_only_setup_upgrade(journal["from_package_version"],
        journal["to_package_version"],)
        and _upgrade_state._supported_marker_upgrade(existing_marker)
    )
    if superseded_committed_target:
        assert existing_marker is not None
        if not approval_preexisting or not core_preexisting:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "committed setup marker has no realized Core and Approval state",
            )
        _upgrade_state._require_marker_realized_state(existing_marker,
        approval_config_digest=_managed_config_digest(
            approval_config_path,
            approval_account,
            blocker="approval_config",
        ),
        core_config_digest=_managed_config_digest(
            core_config_path,
            core_account,
            blocker="core_custody",
            exclude_top_level=frozenset(
                {"enrolled_harness_id", "enrolled_credential_id"}
            ),
        ),
        unit_paths=unit_paths,
        uid=uid,
        gid=gid,)
        # The prior target marker is already the no-rollback boundary. Keep its
        # journal until the next-edge journal atomically replaces it.
        return None
    committed_target = (
        existing_marker is not None
        and existing_marker_payload is not None
        and existing_marker.get("request_digest") == journal["to_request_digest"]
        and existing_marker.get("package_version") == journal["to_package_version"]
        and existing_marker.get("previous_marker_digest")
        == journal["from_marker_sha256"]
        and journal["to_request_digest"] == approved_digest
        and journal["to_package_version"] == __version__
    )
    if committed_target:
        assert existing_marker is not None
        if not approval_preexisting or not core_preexisting:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "committed setup marker has no realized Core and Approval state",
            )
        _upgrade_state._require_marker_realized_state(existing_marker,
        approval_config_digest=_managed_config_digest(
            approval_config_path,
            approval_account,
            blocker="approval_config",
        ),
        core_config_digest=_managed_config_digest(
            core_config_path,
            core_account,
            blocker="core_custody",
            exclude_top_level=frozenset(
                {"enrolled_harness_id", "enrolled_credential_id"}
            ),
        ),
        unit_paths=unit_paths,
        uid=uid,
        gid=gid,)
        if journal.get("schema") == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA:
            _upgrade_state._clear_upgrade_journal(journal_path)
            return "cleared_committed_lifecycle_upgrade"
        if _upgrade_state._forward_only_setup_upgrade(journal["from_package_version"],
        journal["to_package_version"],):
            pending.update(
                forward_only_upgrade=True,
                journal_path=journal_path,
            )
            return "resumed_committed_forward_only_upgrade"
        _upgrade_state._clear_upgrade_journal(journal_path)
        return "cleared_committed_upgrade"
    if (
        existing_marker_payload is None
        or existing_marker is None
        or journal["from_marker_sha256"]
        != hashlib.sha256(existing_marker_payload).hexdigest()
        or journal["from_package_version"] != existing_marker.get("package_version")
        or journal["from_request_digest"] != existing_marker.get("request_digest")
        or journal["to_request_digest"] != approved_digest
        or journal["to_package_version"] != __version__
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "an unrelated interrupted AgentNet setup upgrade is journaled on this host",
        )
    lifecycle_upgrade = (
        journal.get("schema") == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
    )
    pending.update(
        journal=journal,
        journal_path=journal_path,
        marker_path=layout.host(SETUP_MARKER),
        unit_paths=unit_paths,
        config_paths={
            "core_config": core_config_path,
            "core_oidc_config": layout.host(CORE_OIDC_CONFIG),
        },
        core_account=core_account,
        database_url=request.database_url,
        systemctl_executable=preflight.runtime.systemctl_executable,
        rollback_capable_upgrade=lifecycle_upgrade,
        uid=uid,
        gid=gid,
    )
    return (
        "resumed_journaled_lifecycle_upgrade"
        if lifecycle_upgrade
        else "resumed_journaled_upgrade"
    )


def _prepare_supported_upgrade(
    *,
    request: ServerSetupRequest,
    preflight: ServerSetupPreflight,
    layout: SetupLayout,
    existing_marker: dict[str, Any] | None,
    existing_marker_payload: bytes | None,
    approved_digest: str,
    approval_account: pwd.struct_passwd,
    approval_preexisting: bool,
    core_account: pwd.struct_passwd,
    core_preexisting: bool,
    prevalidated_config: ExtensionConfig | None,
    uid: int,
    gid: int,
    pending: dict[str, Any],
) -> str:
    """Gate one supported package upgrade before any managed host write.

    The marker validator already refused every digest drift except a released
    upgrade source.  This proves the recorded pre-upgrade state is exactly what
    is realized, then journals the exact previous units so a failed or
    interrupted upgrade rolls back to that same state instead of leaving a
    deployment that matches neither marker.
    """

    approval_config_path = layout.host(APPROVAL_CONFIG)
    core_config_path = layout.host(CORE_CONFIG)
    core_oidc_path = layout.host(CORE_OIDC_CONFIG)
    database_url = request.database_url
    domain_id = request.domain_id
    enrolled_harness_id = (
        prevalidated_config.enrolled_harness_id
        if prevalidated_config is not None
        else None
    )
    enrolled_credential_id = (
        prevalidated_config.enrolled_credential_id
        if prevalidated_config is not None
        else None
    )
    profile_key = request.runtime_instance_id
    systemctl_executable = preflight.runtime.systemctl_executable
    unit_paths = {unit: layout.unit(unit) for unit in MANAGED_UNITS}
    marker_path = layout.host(SETUP_MARKER)
    journal_path = layout.host(SETUP_UPGRADE_JOURNAL)

    resumed = _resume_supported_upgrade(
        request=request,
        preflight=preflight,
        layout=layout,
        existing_marker=existing_marker,
        existing_marker_payload=existing_marker_payload,
        approved_digest=approved_digest,
        approval_account=approval_account,
        approval_preexisting=approval_preexisting,
        core_account=core_account,
        core_preexisting=core_preexisting,
        uid=uid,
        gid=gid,
        pending=pending,
    )
    if resumed is not None:
        return resumed
    upgrading = (
        existing_marker is not None
        and existing_marker.get("request_digest") != approved_digest
    )
    if not upgrading:
        return "not_required"
    assert existing_marker is not None and existing_marker_payload is not None
    source_profile = _upgrade_state._marker_upgrade_unit_profile(existing_marker)
    if source_profile is None:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "recorded setup marker is not an exact supported upgrade source",
        )
    source_unit_paths = {unit: unit_paths[unit] for unit in source_profile}
    if not approval_preexisting or not core_preexisting:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "recorded setup marker has no realized Core and Approval state to upgrade",
        )
    _upgrade_state._require_marker_realized_state(existing_marker,
    approval_config_digest=_managed_config_digest(
        approval_config_path,
        approval_account,
        blocker="approval_config",
    ),
    core_config_digest=_managed_config_digest(
        core_config_path,
        core_account,
        blocker="core_custody",
        exclude_top_level=frozenset({"enrolled_harness_id", "enrolled_credential_id"}),
    ),
    unit_paths=source_unit_paths,
    uid=uid,
    gid=gid,)
    previous_units: dict[str, str | None] = {}
    for unit, path in unit_paths.items():
        if unit not in source_profile:
            if path.exists() or path.is_symlink():
                raise ServerSetupError(
                    "setup_upgrade_conflict",
                    "target-only managed unit exists before topology upgrade",
                )
            previous_units[unit] = None
            continue
        payload = _read_managed_unit(path, uid=uid, gid=gid, blocker="setup_upgrade_conflict")
        if payload is None:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "realized managed unit disappeared during upgrade preparation",
            )
        previous_units[unit] = base64.b64encode(payload).decode("ascii")
    previous_configs = {
        "core_config": base64.b64encode(
            _read_private_managed_file(
                core_config_path,
                core_account,
                blocker="setup_upgrade_conflict",
                max_bytes=_MAX_CONFIG_BYTES,
            )
        ).decode("ascii"),
        "core_oidc_config": base64.b64encode(
            _read_private_managed_file(
                core_oidc_path,
                core_account,
                blocker="setup_upgrade_conflict",
                max_bytes=_MAX_CONFIG_BYTES,
            )
        ).decode("ascii"),
    }
    lifecycle_upgrade = (
        str(existing_marker["package_version"]),
        __version__,
    ) == _LIFECYCLE_SETUP_UPGRADE
    journal: dict[str, Any] = {
        "schema": (
            _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
            if lifecycle_upgrade
            else _UPGRADE_JOURNAL_SCHEMA
        ),
        "from_marker_sha256": hashlib.sha256(existing_marker_payload).hexdigest(),
        "from_package_version": str(existing_marker["package_version"]),
        "from_request_digest": str(existing_marker["request_digest"]),
        "to_package_version": __version__,
        "to_request_digest": approved_digest,
        "previous_units": previous_units,
        "previous_configs": previous_configs,
    }
    if lifecycle_upgrade:
        if not enrolled_harness_id or not enrolled_credential_id:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "v0.1.44 source has no exact enrolled server identity",
            )
        database_evidence = _run_v0145_database_operation_as(
            core_account,
            database_url,
            operation="snapshot",
            source=None,
            domain_id=domain_id,
            harness_id=enrolled_harness_id,
            credential_id=enrolled_credential_id,
            profile_key=profile_key,
        )
        previous_database = _upgrade_state._validated_v0145_database_snapshot(database_evidence.get("source"))
        previous_systemd: dict[str, dict[str, str]] = {}
        for unit in MANAGED_UNITS:
            properties = _systemd._systemd_show(systemctl_executable, unit)
            previous_systemd[unit] = {
                key: properties[key]
                for key in ("LoadState", "UnitFileState", "ActiveState")
            }
        _upgrade_state._validated_upgrade_systemd_snapshot(previous_systemd)
        journal.update(
            previous_marker=base64.b64encode(existing_marker_payload).decode("ascii"),
            previous_database=previous_database,
            previous_systemd=previous_systemd,
        )
    _upgrade_state._write_upgrade_journal(journal_path, journal, uid=uid, gid=gid)
    pending.update(
        journal=journal,
        journal_path=journal_path,
        marker_path=marker_path,
        unit_paths=dict(unit_paths),
        config_paths={
            "core_config": core_config_path,
            "core_oidc_config": core_oidc_path,
        },
        core_account=core_account,
        database_url=database_url,
        systemctl_executable=systemctl_executable,
        rollback_capable_upgrade=lifecycle_upgrade,
        uid=uid,
        gid=gid,
    )
    return "validated_pre_upgrade_realized_state"


def _migrate_rollback_capable_upgrade(
    *,
    pending_upgrade: Mapping[str, Any],
    core_account: pwd.struct_passwd,
    request: ServerSetupRequest,
    steps: list[dict[str, Any]],
) -> dict[str, object]:
    """Migrate the retained v0.1.45 database snapshot and prove endpoint state."""

    journal = pending_upgrade.get("journal")
    if not isinstance(journal, Mapping):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 migration requires its exact upgrade journal",
        )
    source = _upgrade_state._validated_v0145_database_snapshot(journal.get("previous_database"))
    identity = source["identity"]
    migration_evidence = _run_v0145_database_operation_as(
        core_account,
        request.database_url,
        operation="migrate",
        source=source,
        domain_id=str(identity["domain_id"]),
        harness_id=str(identity["harness_id"]),
        credential_id=str(identity["credential_id"]),
        profile_key=str(identity["profile_key"]),
    )
    endpoint_row = migration_evidence.get("endpoint_lifecycle")
    if (
        not isinstance(endpoint_row, dict)
        or endpoint_row.get("harness_id") != identity["harness_id"]
        or endpoint_row.get("state") != "restart_required"
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "v0.1.45 migration did not prove the exact endpoint lifecycle",
        )
    steps.append(
        {
            "id": "schema_v7_endpoint_lifecycle",
            "status": "restart_required",
        }
    )
    return {
        "endpoint_id": str(identity["harness_id"]),
        "state": "restart_required",
        "public_url": request.core_public_origin,
        "identity_created": False,
    }


def _classify_setup_transition(
    *,
    request: ServerSetupRequest,
    preflight: ServerSetupPreflight,
    layout: SetupLayout,
    approved_digest: str,
    root_uid: int,
    root_gid: int,
) -> tuple[
    bytes | None,
    dict[str, Any] | None,
    bool,
    bool,
    bool,
]:
    """Validate existing marker/topology state and classify this apply edge."""

    marker_path = layout.host(SETUP_MARKER)
    journal_path = layout.host(SETUP_UPGRADE_JOURNAL)
    existing_payload = _read_setup_marker(
        marker_path,
        uid=root_uid,
        gid=root_gid,
    )
    existing_marker = _upgrade_state._validated_setup_marker(existing_payload,
    request_digest=approved_digest,
    legacy_request_digest=preflight.legacy_request_digest,
    artifact_mode=(
        request.effective_artifact_mode
        if request.schema_version == "agentnet.server-setup.request.v2"
        else None
    ),)
    upgrade_profile = (
        _upgrade_state._marker_upgrade_unit_profile(existing_marker)
        if existing_marker is not None
        else None
    )
    topology_expansion = upgrade_profile == LEGACY_COMMUNICATION_ONLY_UNITS
    forward_only_upgrade = (
        existing_marker is not None
        and _upgrade_state._forward_only_setup_upgrade(existing_marker.get("package_version"),
        __version__,)
    )
    journal_present = journal_path.exists() or journal_path.is_symlink()
    journal_preview = (
        _upgrade_state._read_upgrade_journal(journal_path, uid=root_uid, gid=root_gid)
        if journal_present
        else None
    )
    journaled_topology = (
        journal_preview is not None
        and any(
            payload is None
            for payload in _upgrade_state._journaled_unit_payloads(journal_preview).values()
        )
    )
    topology_transition = topology_expansion or journaled_topology
    journaled_forward_only_upgrade = (
        journal_preview is not None
        and _upgrade_state._forward_only_setup_upgrade(journal_preview.get("from_package_version"),
        journal_preview.get("to_package_version"),)
    )
    forward_only_transition = (
        forward_only_upgrade or journaled_forward_only_upgrade
    )
    if topology_transition:
        c0_data = layout.host(C0_RESPONDER_DATA)
        renewal_state = layout.host(CREDENTIAL_RENEW_STATE)
        if (
            c0_data.exists()
            or c0_data.is_symlink()
            or renewal_state.exists()
            or renewal_state.is_symlink()
            or _account_fact(C0_RESPONDER_USER, C0_RESPONDER_DATA) != "create"
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "legacy communication-only state has unexpected target-only state",
            )
        for unit in set(MANAGED_UNITS) - set(
            LEGACY_COMMUNICATION_ONLY_UNITS
        ):
            _systemd._require_absent_topology_upgrade_unit(
                layout,
                preflight.runtime.systemctl_executable,
                unit,
                journaled=journal_present,
            )
    for unit in MANAGED_UNITS:
        _systemd._require_no_unit_overrides(layout, unit)
    return (
        existing_payload,
        existing_marker,
        topology_transition,
        forward_only_upgrade,
        forward_only_transition,
    )


def _write_setup_units(
    *,
    preflight: ServerSetupPreflight,
    layout: SetupLayout,
    pending_upgrade: dict[str, Any],
    steps: list[dict[str, Any]],
    root_uid: int,
    root_gid: int,
) -> dict[str, bytes]:
    """Write exact managed units while retaining rollback source bytes."""

    unit_payloads = render_units(
        preflight.runtime.node_executable,
        preflight.runtime.agentnet_executable,
        preflight.runtime.uv_executable,
    )
    journal = pending_upgrade.get("journal")
    journaled_units = (
        _upgrade_state._journaled_unit_payloads(journal) if journal is not None else {}
    )
    if journal is not None:
        pending_upgrade["replacement_units"] = dict(unit_payloads)
    unit_paths = {unit: layout.unit(unit) for unit in MANAGED_UNITS}
    for unit, payload in unit_payloads.items():
        steps.append(
            {
                "id": f"unit:{unit}",
                "status": _write_managed_unit(
                    unit_paths[unit],
                    payload,
                    uid=root_uid,
                    gid=root_gid,
                    previous=journaled_units.get(unit),
                ),
            }
        )
    return unit_payloads


def _commit_setup_profile(
    *,
    preflight: ServerSetupPreflight,
    layout: SetupLayout,
    request: ServerSetupRequest,
    pending_upgrade: dict[str, Any],
    steps: list[dict[str, Any]],
    root_uid: int,
    root_gid: int,
    approval_account: pwd.struct_passwd,
    core_account: pwd.struct_passwd,
    existing_marker_payload: bytes | None,
    existing_marker: dict[str, Any] | None,
    approved_digest: str,
    forward_only_upgrade: bool,
    attempt_active: bool,
    unit_payloads: dict[str, bytes] | None = None,
) -> dict[str, bytes]:
    """Commit managed units and marker, then cross the no-rollback boundary."""

    unit_paths = {unit: layout.unit(unit) for unit in MANAGED_UNITS}
    if unit_payloads is None:
        unit_payloads = _write_setup_units(
            preflight=preflight,
            layout=layout,
            pending_upgrade=pending_upgrade,
            steps=steps,
            root_uid=root_uid,
            root_gid=root_gid,
        )
    approval_config_digest = _managed_config_digest(
        layout.host(APPROVAL_CONFIG),
        approval_account,
        blocker="approval_config",
    )
    core_config_digest = _managed_config_digest(
        layout.host(CORE_CONFIG),
        core_account,
        blocker="core_custody",
        exclude_top_level=frozenset(
            {"enrolled_harness_id", "enrolled_credential_id"}
        ),
    )
    artifact_mode = (
        request.effective_artifact_mode
        if request.schema_version == "agentnet.server-setup.request.v2"
        else None
    )
    setup_marker = layout.host(SETUP_MARKER)
    try:
        marker_status = _upgrade_state._commit_setup_marker(setup_marker,
        existing_payload=existing_marker_payload,
        existing_marker=existing_marker,
        request_digest=approved_digest,
        approval_config_digest=approval_config_digest,
        core_config_digest=core_config_digest,
        unit_payloads=unit_payloads,
        artifact_mode=artifact_mode,
        uid=root_uid,
        gid=root_gid,)
    except BaseException:
        # A directory-fsync or response failure may occur after the
        # compare-and-swap became observable. Never restore source files over
        # an exact target marker: retain the journal and recover forward.
        if forward_only_upgrade and existing_marker_payload is not None:
            try:
                observed_payload = _read_setup_marker(
                    setup_marker,
                    uid=root_uid,
                    gid=root_gid,
                )
                observed_marker = _upgrade_state._validated_setup_marker(observed_payload,
                request_digest=approved_digest,
                legacy_request_digest=preflight.legacy_request_digest,
                artifact_mode=artifact_mode,)
                if (
                    existing_marker is None
                    or observed_marker is None
                    or observed_marker.get("package_version") != __version__
                    or observed_marker.get("revision")
                    != int(existing_marker.get("revision", 0)) + 1
                    or observed_marker.get("previous_marker_digest")
                    != hashlib.sha256(existing_marker_payload).hexdigest()
                ):
                    raise ServerSetupError(
                        "setup_upgrade_conflict",
                        "setup marker commit outcome is not the exact upgrade target",
                    )
                _upgrade_state._require_marker_realized_state(observed_marker,
                approval_config_digest=approval_config_digest,
                core_config_digest=core_config_digest,
                unit_paths=unit_paths,
                uid=root_uid,
                gid=root_gid,)
            except Exception:
                pass
            else:
                pending_upgrade.clear()
        raise
    pending_upgrade.clear()
    steps.append({"id": "setup_marker", "status": marker_status})
    if not forward_only_upgrade:
        _upgrade_state._clear_upgrade_journal(layout.host(SETUP_UPGRADE_JOURNAL))
    if attempt_active:
        _upgrade_state._clear_upgrade_journal(layout.host(SETUP_ATTEMPT))
    return unit_payloads


