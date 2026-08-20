"""Managed identity, responder activation, and service readiness."""

from __future__ import annotations

import hashlib
import json
import os
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Mapping

if os.name == "posix":
    import pwd

from pydantic import ValidationError

from agentnet import __version__
from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.c0_credential_supersession import load_supersession_journal
from agentnet.operations.config import ExtensionConfig, OIDCEnrollmentConfig
from agentnet.security.signatures import P256KeyPair

from . import systemd as _systemd
from .custody import _atomic_write, _read_private_managed_file, _require_private_file
from . import database as _database
from .models import ServerSetupError, ServerSetupPreflight, ServerSetupRequest, SetupLayout
from .preflight import _BROKER_CREDENTIAL_NAME, _MAX_CONFIG_BYTES, _reject_duplicates
from .systemd import (
    APPROVAL_PORT,
    APPROVAL_UNIT,
    C0_RESPONDER_CONFIG,
    C0_RESPONDER_DATA,
    C0_RESPONDER_UNIT,
    CORE_DATA,
    CORE_PORT,
    CORE_UNIT,
    CREDENTIAL_RENEW_TIMER,
    CREDENTIAL_RENEW_UNIT,
    SERVER_AGENT_IDENTITY,
    SERVER_AGENT_KEY,
)

C0_RESPONDER_TERMINAL = C0_RESPONDER_DATA / "terminal.json"
CREDENTIAL_SUPERSESSION_JOURNAL = CORE_DATA / "credential-supersessions.json"

# A first managed start also materializes the service-private uv runtime, and a
# public route can converge after loopback health is exact. Setup gives those
# startup and public-route probes one longer bounded window; ordinary probes keep
# the shorter default so a genuinely broken deployment still fails in bounded time.
_START_HEALTH_ATTEMPTS = 90
_HEALTH_USER_AGENT = f"AgentNet/{__version__}"


def _validated_managed_identity_profile(
    path: Path,
    key_path: Path,
    account: pwd.struct_passwd,
    *,
    config: ExtensionConfig,
    request: ServerSetupRequest,
) -> dict[str, object]:
    try:
        value = json.loads(
            _read_private_managed_file(
                path,
                account,
                blocker="server_agent_identity",
                max_bytes=_MAX_CONFIG_BYTES,
            ),
            object_pairs_hook=_reject_duplicates,
            parse_constant=lambda _value: (_ for _ in ()).throw(
                ValueError("non-finite JSON number")
            ),
        )
    except (UnicodeError, json.JSONDecodeError, ValueError) as exc:
        raise ServerSetupError("server_agent_identity", "managed server-agent identity is invalid") from exc
    if (
        not isinstance(value, dict)
        or set(value) != {"schema", "server_base_url", "audience", "actor", "private_key_path"}
        or value.get("schema") != "agentnet.identity-profile.v1"
        or value.get("server_base_url") != request.core_public_origin
        or value.get("audience") != request.service_audience
        or value.get("private_key_path") != str(key_path)
        or not isinstance(value.get("actor"), dict)
    ):
        raise ServerSetupError("server_agent_identity", "managed server-agent identity mismatches fixed profile")
    try:
        actor = VerifiedActor.model_validate(value["actor"])
    except ValidationError as exc:
        raise ServerSetupError("server_agent_identity", "managed server-agent identity is invalid") from exc
    if (
        actor.domain_id != request.domain_id
        or actor.harness_id != config.enrolled_harness_id
        or actor.credential_id != config.enrolled_credential_id
    ):
        raise ServerSetupError("server_agent_identity", "managed server-agent identity mismatches current binding")
    key_payload = _read_private_managed_file(
        key_path,
        account,
        blocker="server_agent_identity",
        max_bytes=65_536,
    )
    try:
        P256KeyPair.from_private_pem(key_payload)
    except Exception as exc:
        raise ServerSetupError("server_agent_identity", "managed server-agent key is invalid") from exc
    return value


def _validated_c0_terminal_marker(
    path: Path,
    account: pwd.struct_passwd,
    *,
    config: ExtensionConfig,
    principal_id: str,
    credential_epoch: int,
    supersession_path: Path,
    core_account: pwd.struct_passwd,
    database_url: str,
) -> tuple[dict[str, str] | None, dict[str, object]]:
    if not path.exists() and not path.is_symlink():
        return None, {"status": "not_applicable"}
    try:
        terminal_raw = _read_private_managed_file(
            path,
            account,
            blocker="c0_responder_terminal",
            max_bytes=4096,
        )
        value = json.loads(terminal_raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise ServerSetupError(
            "c0_responder_terminal",
            "C0 responder terminal marker is invalid",
        ) from exc
    if (
        not isinstance(value, dict)
        or set(value) != {"schema", "status", "domain_id", "harness_id", "credential_id"}
        or value.get("schema") != "agentnet.c0-pilot-responder.terminal.v1"
        or value.get("domain_id") != config.domain_id
        or value.get("harness_id") != config.enrolled_harness_id
        or value.get("status")
        not in {"COMPLETED_C0_ROUND_TRIP", "expired", "invalidated", "failed"}
    ):
        raise ServerSetupError(
            "c0_responder_terminal",
            "C0 responder terminal marker conflicts with managed identity",
        )
    if value.get("credential_id") == config.enrolled_credential_id:
        return value, {"status": "not_applicable"}
    if value.get("status") != "COMPLETED_C0_ROUND_TRIP":
        raise ServerSetupError(
            "c0_responder_terminal",
            "C0 responder terminal marker conflicts with managed identity",
        )
    try:
        journal_raw = _read_private_managed_file(
            supersession_path,
            core_account,
            blocker="c0_credential_supersession",
            max_bytes=1_048_576,
        )
        journal = load_supersession_journal(
            journal_raw,
            terminal_raw=terminal_raw,
            domain_id=config.domain_id,
            principal_id=principal_id,
            harness_id=config.enrolled_harness_id or "",
        )
    except (OSError, GateBlocked, ServerSetupError) as exc:
        raise ServerSetupError(
            "c0_credential_supersession",
            "C0 terminal credential replacement lacks valid supersession provenance",
        ) from exc
    if journal.current_credential != (
        config.enrolled_credential_id,
        credential_epoch,
    ):
        raise ServerSetupError(
            "c0_credential_supersession",
            "C0 credential supersession journal is stale",
        )
    audit_evidence = _database._run_supersession_audit_as(
        core_account,
        database_url,
        journal_raw=journal_raw,
        terminal_raw=terminal_raw,
        domain_id=config.domain_id,
        principal_id=principal_id,
        harness_id=config.enrolled_harness_id or "",
    )
    expected_audit_evidence = {
        "ready": True,
        "journal_sha256": hashlib.sha256(journal_raw).hexdigest(),
        "transition_count": len(journal.entries),
        "audit_records_verified": len(journal.entries),
        "credential_id": journal.current_credential[0],
        "credential_epoch": journal.current_credential[1],
    }
    if audit_evidence != expected_audit_evidence:
        raise ServerSetupError(
            "c0_credential_supersession",
            "credential supersession audit evidence is invalid",
        )
    return value, {
        "status": "verified",
        "journal_sha256": hashlib.sha256(journal_raw).hexdigest(),
        "transition_count": len(journal.entries),
        "audit_records_verified": len(journal.entries),
        "credential_id": journal.current_credential[0],
        "credential_epoch": journal.current_credential[1],
    }


class _RejectRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        return None


def _health_value_matches(actual: object, expected: object) -> bool:
    if isinstance(expected, tuple):
        return actual in expected
    if isinstance(expected, Mapping):
        return isinstance(actual, Mapping) and all(
            key in actual and _health_value_matches(actual[key], item)
            for key, item in expected.items()
        )
    return actual == expected


def _health(url: str, *, expected: Mapping[str, object], attempts: int = 30) -> None:
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        _RejectRedirects(),
    )
    request = urllib.request.Request(
        url,
        headers={"User-Agent": _HEALTH_USER_AGENT, "Accept": "application/json"},
        method="GET",
    )
    for _ in range(attempts):
        try:
            with opener.open(request, timeout=2) as response:  # noqa: S310 - fixed validated setup URL
                payload = response.read(65_537)
                if response.status != 200 or len(payload) > 65_536:
                    raise ValueError("invalid health response")
                value = json.loads(payload)
                if isinstance(value, dict) and _health_value_matches(value, expected):
                    return
        except (urllib.error.URLError, TimeoutError, ValueError, json.JSONDecodeError):
            pass
        time.sleep(1)
    raise ServerSetupError("service_health", "AgentNet service did not return exact healthy identity evidence")


def _prepare_c0_responder_activation(
    *,
    config: ExtensionConfig,
    request: ServerSetupRequest,
    start: bool,
    layout: SetupLayout,
    c0_responder_account: pwd.struct_passwd,
    core_account: pwd.struct_passwd,
    verified: dict[str, bool],
) -> tuple[bool, bool, bytes | None, dict[str, object], str | None]:
    """Validate activated identity and prepare bounded responder state."""

    identity_enrolled = bool(
        config.enrolled_harness_id and config.enrolled_credential_id
    )
    c0_responder_required = False
    responder_payload: bytes | None = None
    supersession_evidence: dict[str, object] = {"status": "not_applicable"}
    responder_config_status: str | None = None
    config_path = layout.host(C0_RESPONDER_CONFIG)
    terminal_path = layout.host(C0_RESPONDER_TERMINAL)
    if identity_enrolled and start:
        identity_profile = _validated_managed_identity_profile(
            layout.host(SERVER_AGENT_IDENTITY),
            layout.host(SERVER_AGENT_KEY),
            core_account,
            config=config,
            request=request,
        )
        verified["identity_enrolled"] = True
        identity_actor = identity_profile["actor"]
        if not isinstance(identity_actor, dict):
            raise ServerSetupError(
                "server_agent_identity",
                "managed server-agent identity actor is invalid",
            )
        terminal, supersession_evidence = _validated_c0_terminal_marker(
            terminal_path,
            c0_responder_account,
            config=config,
            principal_id=str(identity_actor["principal_id"]),
            credential_epoch=int(identity_actor["credential_epoch"]),
            supersession_path=layout.host(CREDENTIAL_SUPERSESSION_JOURNAL),
            core_account=core_account,
            database_url=request.database_url,
        )
        if terminal is not None:
            if config_path.exists() or config_path.is_symlink():
                _require_private_file(
                    config_path,
                    c0_responder_account,
                    blocker="c0_responder_terminal",
                )
                try:
                    config_path.unlink()
                    directory = os.open(
                        config_path.parent,
                        os.O_RDONLY | getattr(os, "O_DIRECTORY", 0),
                    )
                    try:
                        os.fsync(directory)
                    finally:
                        os.close(directory)
                except OSError as exc:
                    raise ServerSetupError(
                        "c0_responder_terminal",
                        "C0 responder terminal cleanup could not be reconciled",
                    ) from exc
                responder_config_status = "terminal_cleanup_reconciled"
            else:
                responder_config_status = "terminal_not_recreated"
        else:
            c0_responder_required = True
            responder_payload = json.dumps(
                {
                    "schema": "agentnet.c0-pilot-responder.config.v1",
                    "core_base_url": request.core_public_origin,
                    "audience": request.service_audience,
                    "domain_id": request.domain_id,
                    "harness_id": config.enrolled_harness_id,
                    "credential_id": config.enrolled_credential_id,
                    "poll_seconds": 2,
                    "max_consecutive_errors": 5,
                },
                indent=2,
                sort_keys=True,
            ).encode() + b"\n"
    elif not identity_enrolled and any(
        path.exists() or path.is_symlink()
        for path in (config_path, terminal_path)
    ):
        raise ServerSetupError(
            "c0_responder_conflict",
            "C0 responder state exists before exact activated identity",
        )
    return (
        identity_enrolled,
        c0_responder_required,
        responder_payload,
        supersession_evidence,
        responder_config_status,
    )


def _start_managed_server_services(
    *,
    request: ServerSetupRequest,
    layout: SetupLayout,
    preflight: ServerSetupPreflight,
    oidc: OIDCEnrollmentConfig,
    identity_enrolled: bool,
    c0_responder_required: bool,
    responder_payload: bytes | None,
    c0_responder_account: pwd.struct_passwd,
    supersession_evidence: dict[str, object],
    steps: list[dict[str, Any]],
) -> tuple[str, str]:
    """Start, authenticate, and prove the fixed managed-service topology."""

    node_executable = preflight.runtime.node_executable
    agentnet_executable = preflight.runtime.agentnet_executable
    uv_executable = preflight.runtime.uv_executable
    systemctl_executable = preflight.runtime.systemctl_executable
    approval_health = {
        "schema": "agentnet.approval.health.v1",
        "service": "agentnet-approval",
        "version": __version__,
        "status": "alive",
        "public_origin": request.approval_public_origin,
        "verifier_id": request.approval_verifier_id,
    }
    core_health = {
        "schema": "agentnet.core.health.v1",
        "service": "agentnet-core",
        "version": __version__,
        "status": "alive",
        "profile": request.profile,
        "artifact_mode": request.effective_artifact_mode,
        "server_agent_capabilities": sorted(
            capability.value
            for capability in (
                {
                    ServerAgentCapability.OFFLINE_CUSTODY,
                    ServerAgentCapability.ARTIFACT_STORAGE,
                }
                if request.effective_artifact_mode == "enabled"
                else {ServerAgentCapability.OFFLINE_CUSTODY}
            )
        ),
        "domain_id": request.domain_id,
        "public_origin": request.core_public_origin,
        "service_audience": request.service_audience,
        "runtime_instance_id": request.runtime_instance_id,
    }

    def verify_live_service_state(*, auxiliary_ready: bool) -> None:
        _systemd._verify_live_service_state(
            systemctl_executable=systemctl_executable,
            layout=layout,
            node_executable=node_executable,
            agentnet_executable=agentnet_executable,
            uv_executable=uv_executable,
            identity_enrolled=identity_enrolled,
            c0_responder_required=c0_responder_required,
            auxiliary_ready=auxiliary_ready,
        )

    base_systemctl_commands: tuple[list[str], ...] = (
        ["daemon-reload"],
        ["disable", "--now", CREDENTIAL_RENEW_TIMER],
        ["disable", "--now", C0_RESPONDER_UNIT],
        ["stop", CREDENTIAL_RENEW_UNIT],
        ["enable", "--now", APPROVAL_UNIT],
        ["enable", CORE_UNIT],
        ["restart", CORE_UNIT],
    )
    base_start_status = _systemd._run_systemctl_sequence_or_reconcile(
        systemctl_executable,
        base_systemctl_commands,
        reconcile=lambda: verify_live_service_state(auxiliary_ready=False),
    )
    if base_start_status == "completed":
        verify_live_service_state(auxiliary_ready=False)
    _health(
        f"http://127.0.0.1:{APPROVAL_PORT}/healthz",
        expected=approval_health,
        attempts=_START_HEALTH_ATTEMPTS,
    )
    _health(
        f"http://127.0.0.1:{CORE_PORT}/healthz",
        expected=core_health,
        attempts=_START_HEALTH_ATTEMPTS,
    )
    _health(
        f"{request.approval_public_origin}/healthz",
        expected=approval_health,
        attempts=_START_HEALTH_ATTEMPTS,
    )
    _health(
        f"{request.core_public_origin}/healthz",
        expected=core_health,
        attempts=_START_HEALTH_ATTEMPTS,
    )
    if oidc.approval_service is None:  # pragma: no cover - fixed profile invariant
        raise ServerSetupError(
            "approval_broker_auth",
            "Approval broker configuration is unavailable",
        )
    broker_client: ApprovalServiceClient | None = None
    try:
        broker_client = ApprovalServiceClient(
            oidc.approval_service,
            preflight.core_values[_BROKER_CREDENTIAL_NAME],
        )
        broker_client.readiness()
    except GateBlocked as exc:
        blocker = (
            exc.gate
            if exc.gate in {"approval_broker_auth", "approval_broker_unavailable"}
            else "approval_broker_auth"
        )
        raise ServerSetupError(
            blocker,
            "Approval broker readiness failed",
        ) from None
    finally:
        if broker_client is not None:
            broker_client.close()
    steps.append({"id": "approval_broker_readiness", "status": "completed"})
    start_status = base_start_status
    if identity_enrolled:
        readiness = {
            "schema": "agentnet.core.readiness.v1",
            "service": "agentnet-core",
            "version": __version__,
            "ready": True,
            "profile": request.profile,
            "artifact_mode": request.effective_artifact_mode,
            "server_agent_capabilities": sorted(
                capability.value
                for capability in (
                    {
                        ServerAgentCapability.OFFLINE_CUSTODY,
                        ServerAgentCapability.ARTIFACT_STORAGE,
                    }
                    if request.effective_artifact_mode == "enabled"
                    else {ServerAgentCapability.OFFLINE_CUSTODY}
                )
            ),
            "domain_id": request.domain_id,
            "public_origin": request.core_public_origin,
            "service_audience": request.service_audience,
            "runtime_instance_id": request.runtime_instance_id,
            "deployment_binding": {
                "ready": True,
                "required": True,
                "credential_state": ("current", "renewal_needed"),
                "credential_supersession": supersession_evidence,
            },
            "approval_broker": {"ready": True, "required": True},
        }
        _health(f"http://127.0.0.1:{CORE_PORT}/readyz", expected=readiness)
        try:
            _health(
                f"{request.core_public_origin}/readyz",
                expected=readiness,
                attempts=_START_HEALTH_ATTEMPTS,
            )
        except ServerSetupError as exc:
            raise ServerSetupError(
                exc.blocker,
                str(exc),
                identity_enrolled=True,
            ) from exc
        if c0_responder_required:
            if responder_payload is None:  # pragma: no cover - fixed branch invariant
                raise ServerSetupError(
                    "c0_responder_conflict",
                    "C0 responder configuration is unavailable",
                )
            steps.append(
                {
                    "id": "c0_responder_config",
                    "status": _atomic_write(
                        layout.host(C0_RESPONDER_CONFIG),
                        responder_payload,
                        mode=0o600,
                        uid=c0_responder_account.pw_uid,
                        gid=c0_responder_account.pw_gid,
                    ),
                }
            )
        auxiliary_commands = _systemd._credential_renewal_activation_commands(
            c0_responder_required=c0_responder_required,
        )
        auxiliary_status = _systemd._run_systemctl_sequence_or_reconcile(
            systemctl_executable,
            auxiliary_commands,
            reconcile=lambda: verify_live_service_state(auxiliary_ready=True),
        )
        if auxiliary_status == "completed":
            verify_live_service_state(auxiliary_ready=True)
        if auxiliary_status != "completed":
            start_status = auxiliary_status
        steps.append({"id": "operational_readiness", "status": "completed"})
        status = "operational"
        next_action = (
            "enroll additional ordinary laptops with agentnet join guided"
        )
    else:
        status = "waiting_owner_oidc_or_passkey"
        next_action = (
            "complete owner passkey registration and guided identity-only enrollment"
        )
    steps.append({"id": "service_start", "status": start_status})
    steps.append(
        {
            "id": "managed_unit_runtime",
            "status": "validated_hermetic_live_binding",
        }
    )
    steps.append({"id": "public_https_routes", "status": "completed"})
    return status, next_action
