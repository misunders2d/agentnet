"""CLI commands for enrollment, credentials, invitations, recovery, C0, and administration."""

from __future__ import annotations

import argparse
import asyncio
import base64
import getpass
import html
import hashlib
import ipaddress
import json
import socket
import os
import secrets
import signal
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.parse import urlencode, urlsplit
from typing import Any
from uuid import NAMESPACE_URL, UUID, uuid4, uuid5

try:
    import fcntl
except ModuleNotFoundError:  # pragma: no cover - exercised on Windows CI
    fcntl = None  # type: ignore[assignment]

import uvicorn
import httpx
from a2a.types import AgentCapabilities, AgentCard, Message, Part, Role, SendMessageRequest
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette

from agentnet import __version__
from agentnet._terminal_handoff import (
    TerminalHandoffError,
    handoff_private_url,
    require_private_terminal,
)
from agentnet.approval.cli_commands import configure_approval_parser
from agentnet.approval.internal_client import ApprovalServiceClient
from agentnet.approval.service import IndependentApprovalVerifier, TrustedApprover
from agentnet.audit.service import AuditService
from agentnet.authorization import (
    AUTHORITY_COMMAND_PURPOSE,
    HumanEntitlement,
    PolicyEngine,
    SignedAuthorityCommand,
)
from agentnet.authorization.bootstrap_plan import (
    BootstrapPlanBeginResult,
    BootstrapPlanCompleteResult,
    BootstrapPlanStatusResult,
)
from agentnet.authorization.communication_scope import (
    CommunicationScopeBeginRequest,
    CommunicationScopeBeginResult,
    CommunicationScopeCompleteRequest,
    CommunicationScopeCompleteResult,
    CommunicationScopeStatusRequest,
    CommunicationScopeStatusResult,
)
from agentnet.authorization.c0_pilot import C0PilotResult
from agentnet.bindings.remote_manager import (
    resolve_packaged_manager_extension,
    run_manager_gateway,
    validate_manager_command,
)
from agentnet.client import MAX_ARTIFACT_BYTES, AgentNetClient
from agentnet.console.http import create_console_app
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked, ValidationError
from agentnet.http_api import create_app
from agentnet.host import host_platform
from agentnet.gateways.a2a import (
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    create_tainted_proposal_handler,
    generate_opaque_route,
)
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.identity.credentials import (
    LAPTOP_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
    MANAGED_SERVER_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    LaptopCredentialReauthorizationRequest,
    ManagedServerCredentialReauthorizationRequestV2,
    ManagedServerCredentialReauthorizationService,
    load_credential_binding,
    public_key_thumbprint,
)
from agentnet.identity.invitations import (
    INTERNAL_INVITATION_POP_PURPOSE,
    INTERNAL_INVITATION_REVOKE_ACTION,
    InternalInvitationRecord,
    InternalInvitationRequest,
    InternalInvitationService,
)
from agentnet.identity.recovery import CredentialRecoveryRequest
from agentnet.operations.c0_credential_supersession import (
    append_supersession,
    canonical_supersession_journal,
    completed_c0_terminal_credential,
    load_audited_supersession_journal,
)
from agentnet.operations.config import (
    BackupSealKeyConfig,
    BackupTrustConfig,
    ExtensionConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
    ScannerTrustConfig,
)
from agentnet.operations.config_migration import load_config_json
from agentnet.operations.server_reset import ServerSetupResetError, reset_server_setup
from agentnet.operations.server_setup import (
    C0_RESPONDER_TERMINAL,
    C0_RESPONDER_USER,
    CORE_CONFIG,
    CORE_ENV,
    CORE_USER,
    SERVER_AGENT_IDENTITY,
    CREDENTIAL_SUPERSESSION_JOURNAL,
    SERVER_AGENT_KEY,
    SETUP_ROOT,
    ServerSetupError,
    _parse_environment_file,
    apply_server_setup,
    load_server_setup_request,
    plan_server_setup,
)
from agentnet.operations.client_setup import (
    ClientIdentityProfile,
    ClientSetupContinuationStore,
    ClientSetupCoordinator,
    ClientSetupError,
    ClientSetupResult,
    EnrollmentProgress,
    SetupNextAction,
)
from agentnet.operations.incident import (
    DomainIncidentService,
    IncidentMode,
    IncidentModeChange,
)
from agentnet.operations.outage import OutageGate
from agentnet.operations.backup import (
    BackupBinding,
    ManifestSeal,
    PublicationOutcomeUnknown,
    VerifiedBackup,
    build_compromise_rebuild_plan,
    build_sqlite_backup_plan,
    build_sqlite_restore_plan,
    capture_backup_binding,
    discard_unsealed_sqlite_backup,
    discard_failed_sqlite_restore,
    execute_sqlite_backup_plan,
    execute_sqlite_restore_plan,
    inspect_sqlite_restore_target,
    read_manifest_seal,
    verify_backup_for_restore,
    write_manifest_seal,
)
from agentnet.organization.relationships import RelationshipGovernanceRecord
from agentnet.protocol.models import Classification, Relationship
from agentnet.storage.postgres import PostgreSQLReadiness, PostgreSQLStore
from agentnet.storage.sqlite import SQLiteStore
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.dpop import canonical_service_audience
from agentnet.security.signatures import P256KeyPair, canonical_digest, canonical_json
from agentnet.supervisor.demos import (
    content_free_demo_summary,
    run_deterministic_harness_demo,
)
from agentnet.supervisor.live_gate import (
    assert_installed_probe_report,
    installed_probe_report,
    run_live_harness_gate,
)
from agentnet.supervisor.c0_responder import (
    check_c0_responder,
    load_c0_responder_config,
    run_c0_responder,
)
from agentnet.supervisor.daemon import (
    load_supervisor_config,
    redacted_supervisor_status,
    run_supervisor_daemon,
)

from agentnet.cli.helpers import (
    _canonical_server_origin,
    _load_identity_client,
    _load_identity_profile,
    _owner_only_file,
    _owner_only_directory,
    _private_state_lock,
    _public_json_request,
    _read_json_object,
    _remove_private_state,
    _write_owner_json,
    _write_owner_only,
    _write_private_config,
)
from agentnet.cli.commands.server_agent import _setup_progress


def _detect_guided_harness() -> str:
    supported = {"omp", "pi", "claude", "codex", "antigravity"}
    configured = os.environ.get("AGENTNET_HARNESS_KIND")
    if configured is not None:
        if configured not in supported:
            raise SystemExit("AGENTNET_HARNESS_KIND is not a supported laptop harness")
        return configured

    executable_kinds = {
        "omp": "omp",
        "pi": "pi",
        "claude": "claude",
        "codex": "codex",
        "agy": "antigravity",
        "antigravity": "antigravity",
    }
    try:
        import psutil

        process = psutil.Process(os.getppid())
        for _ in range(12):
            name = process.name().casefold()
            if name.endswith(".exe"):
                name = name[:-4]
            detected = executable_kinds.get(name)
            if detected is not None:
                return detected
            parent = process.parent()
            if parent is None:
                break
            process = parent
    except (OSError, ValueError, psutil.Error):
        pass

    environment_kinds = {
        kind
        for variable, kind in (
            ("OMPCODE", "omp"),
            ("CLAUDECODE", "claude"),
            ("CODEX_HOME", "codex"),
            ("ANTIGRAVITY_HOME", "antigravity"),
        )
        if os.environ.get(variable)
    }
    if len(environment_kinds) == 1:
        return environment_kinds.pop()
    raise SystemExit(
        "current AI harness could not be identified unambiguously; use --harness"
    )


def _guided_join_inputs(
    args: argparse.Namespace,
    *,
    server: str,
    retained_state: dict[str, object] | None,
) -> tuple[str, str, str]:
    retained_domain = retained_state.get("domain_id") if retained_state else None
    retained_harness = retained_state.get("harness_kind") if retained_state else None
    retained_name = retained_state.get("harness_name") if retained_state else None

    domain = args.domain if args.domain is not None else retained_domain
    if domain is None:
        discovery = _public_json_request(
            server=server,
            method="GET",
            path="/v1/enrollment/discovery",
            body={},
        )
        if (
            set(discovery) != {"schema", "domain_id", "profile"}
            or discovery.get("schema") != "agentnet.enrollment.discovery.v1"
            or discovery.get("profile") != "guided_oidc_passkey"
        ):
            raise SystemExit("AgentNet enrollment discovery response is invalid")
        domain = discovery.get("domain_id")
    if (
        not isinstance(domain, str)
        or not 3 <= len(domain) <= 128
        or domain[0] not in "abcdefghijklmnopqrstuvwxyz0123456789"
        or any(character not in "abcdefghijklmnopqrstuvwxyz0123456789.-" for character in domain)
    ):
        raise SystemExit("AgentNet enrollment discovery domain is invalid")

    harness = args.harness if args.harness is not None else retained_harness
    if harness is None:
        harness = _detect_guided_harness()
    if (
        not isinstance(harness, str)
        or not 1 <= len(harness) <= 64
        or any(not (character.isascii() and (character.isalnum() or character in "._-")) for character in harness)
    ):
        raise SystemExit("guided join harness is invalid")

    name = args.name if args.name is not None else retained_name
    if name is None:
        name = socket.gethostname()
    if (
        not isinstance(name, str)
        or not 1 <= len(name) <= 128
        or name != name.strip()
        or any(ord(character) < 0x20 or ord(character) == 0x7F for character in name)
    ):
        raise SystemExit("guided join display name is invalid; use --name")
    return domain, harness, name


def _guided_join_state(path: Path) -> dict[str, object]:
    try:
        value = json.loads(_owner_only_file(path, label="guided join state"))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("guided join state is not readable JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit("guided join state must be one JSON object")
    return value


def _validate_guided_authorization(value: object) -> dict[str, object]:
    if not isinstance(value, dict) or set(value) != {
        "transaction_id",
        "authorization_url",
        "state",
        "expires_at",
        "continuation_token",
    }:
        raise SystemExit("guided enrollment authorization response is invalid")
    transaction_id = value.get("transaction_id")
    state = value.get("state")
    continuation = value.get("continuation_token")
    expires_at = value.get("expires_at")
    authorization_url = value.get("authorization_url")
    if (
        not isinstance(transaction_id, str)
        or not 16 <= len(transaction_id) <= 128
        or not isinstance(state, str)
        or not 32 <= len(state) <= 256
        or not isinstance(continuation, str)
        or not 32 <= len(continuation) <= 128
        or type(expires_at) is not int
        or not isinstance(authorization_url, str)
    ):
        raise SystemExit("guided enrollment authorization response is invalid")
    try:
        parsed = urlsplit(authorization_url)
    except ValueError as exc:
        raise SystemExit("guided enrollment authorization URL is invalid") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
    ):
        raise SystemExit("guided enrollment authorization URL is invalid")
    return value


def _validate_stable_approval_url(value: object) -> str:
    if not isinstance(value, str):
        raise SystemExit("guided enrollment approval entrypoint is invalid")
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise SystemExit("guided enrollment approval entrypoint is invalid") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path != "/approval"
        or parsed.query
        or parsed.fragment
        or (port is not None and not 1 <= port <= 65_535)
    ):
        raise SystemExit("guided enrollment approval entrypoint is invalid")
    return value


def _validate_guided_challenge(value: object) -> tuple[dict[str, object], dict[str, object]]:
    if not isinstance(value, dict) or set(value) != {
        "challenge_id",
        "nonce",
        "canonical_transaction_b64",
    }:
        raise SystemExit("guided enrollment challenge is invalid")
    try:
        transaction = base64.b64decode(
            str(value["canonical_transaction_b64"]).encode("ascii"),
            validate=True,
        )
        decoded = json.loads(transaction)
    except (UnicodeError, ValueError, TypeError) as exc:
        raise SystemExit("guided enrollment challenge is invalid") from exc
    if (
        not isinstance(decoded, dict)
        or canonical_json(decoded) != transaction
        or not isinstance(value["challenge_id"], str)
        or not isinstance(value["nonce"], str)
    ):
        raise SystemExit("guided enrollment challenge is invalid")
    return value, decoded


def _guided_identity_result(
    *,
    result: dict[str, object],
    expected_domain_id: str,
    key: P256KeyPair,
) -> VerifiedActor:
    try:
        actor = VerifiedActor.model_validate(result["actor"])
    except Exception as exc:
        raise SystemExit("enrollment response lacks an exact verified actor") from exc
    if (
        actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.domain_id != expected_domain_id
        or result.get("key_id") != key.thumbprint
    ):
        raise SystemExit("enrollment response does not match the requested human/device binding")
    return actor


def _guided_success_output(
    *,
    identity_path: Path,
    actor: VerifiedActor,
    idempotent_repeat: bool,
) -> dict[str, object]:
    _ = identity_path, actor
    return {
        "status": "enrolled_identity_only",
        "idempotent_repeat": idempotent_repeat,
        "identity_saved_locally": True,
        "approval_delivery": "automatic_possession_bound_signed_broker",
        "authority_granted": False,
        "first_message_status": "first_message_blocked_explicit_authority_required",
        "next": "continue only with an explicitly approved bounded authority plan",
    }


def _require_private_terminal_or_exit() -> None:
    try:
        require_private_terminal()
    except TerminalHandoffError as exc:
        raise SystemExit(str(exc)) from None


def _handoff_guided_authorization(
    url: str,
    *,
    browser: str,
    purpose: str = "owner OIDC enrollment",
) -> None:
    if browser == "system":
        if not webbrowser.open(url, new=1):
            raise SystemExit(
                "system browser could not be opened; guided join state is retained"
            )
        return
    if browser == "remote":
        return
    if browser != "terminal":
        raise SystemExit("guided join browser mode is invalid")
    try:
        handoff_private_url(
            url,
            purpose=purpose,
            require_ack=True,
        )
    except TerminalHandoffError as exc:
        raise SystemExit(str(exc)) from None


def _poll_guided_authorization(
    *,
    server: str,
    authorization: dict[str, object],
) -> tuple[dict[str, object], object, int]:
    polled = _public_json_request(
        server=server,
        method="POST",
        path="/v1/enrollment/oidc/poll",
        body={
            "transaction_id": authorization["transaction_id"],
            "continuation_token": authorization["continuation_token"],
        },
    )
    status = polled.get("status")
    interval = polled.get("interval_seconds")
    if type(interval) is not int or not 2 <= interval <= 10:
        raise SystemExit("guided enrollment poll response is invalid")
    return polled, status, interval


def command_join_guided(args: argparse.Namespace) -> int:
    """Run resumable browser OIDC and Core-brokered independent approval."""
    started = time.monotonic()
    deadline = started + float(args.timeout)
    _setup_progress("discover", started, "fetch strict server enrollment metadata")

    state_path = Path(os.path.abspath(args.state))
    identity_path = Path(os.path.abspath(args.identity))
    server = _canonical_server_origin(args.server)
    authorization_url_disclosed = False
    approval_url_disclosed = False
    state_exists = os.path.lexists(state_path)
    pending = _guided_join_state(state_path) if state_exists else None
    args.domain, args.harness, args.name = _guided_join_inputs(
        args,
        server=server,
        retained_state=pending,
    )
    _setup_progress("prepare", started, "validate resumable owner-only identity state")
    replace_terminal_state = False
    if state_exists:
        assert pending is not None
        if pending.get("schema") == "agentnet.guided-join-complete.v1":
            if args.replace_terminal_state:
                raise SystemExit("completed guided join state cannot be replaced")
            expected = {
                "schema",
                "server_base_url",
                "domain_id",
                "harness_kind",
                "harness_name",
                "private_key_path",
                "public_key_pem",
                "identity_path",
                "actor",
            }
            if set(pending) != expected:
                raise SystemExit("completed guided join state does not match the exact schema")
            if (
                pending["server_base_url"] != server
                or pending["domain_id"] != args.domain
                or pending["harness_kind"] != args.harness
                or pending["harness_name"] != args.name
                or pending["identity_path"] != str(identity_path)
            ):
                raise SystemExit("guided join resume arguments do not match completed state")
            try:
                actor = VerifiedActor.model_validate(pending["actor"])
            except Exception as exc:
                raise SystemExit("completed guided join actor is invalid") from exc
            key_path = Path(str(pending["private_key_path"]))
            key = P256KeyPair.from_private_pem(
                _owner_only_file(key_path, label="guided join private key")
            )
            if key.public_pem != pending["public_key_pem"]:
                raise SystemExit("completed guided join key does not match state")
            expected_identity = {
                "schema": "agentnet.identity-profile.v1",
                "server_base_url": server,
                "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
                "actor": actor.model_dump(mode="json"),
                "private_key_path": str(key_path),
            }
            if _guided_join_state(identity_path) != expected_identity:
                raise SystemExit("completed guided join identity file does not match state")
            _setup_progress("enroll", started, "validate retained exact identity binding")
            _setup_progress("verify", started, "confirm identity-only terminal state")
            print(
                json.dumps(
                    _guided_success_output(
                        identity_path=identity_path,
                        actor=actor,
                        idempotent_repeat=True,
                    ),
                    indent=2,
                    sort_keys=True,
                )
            )
            return 0
        expected = {
            "schema",
            "server_base_url",
            "domain_id",
            "harness_kind",
            "harness_name",
            "private_key_path",
            "public_key_pem",
            "authorization",
            "challenge",
            "approval_url",
        }
        pending_schema = pending.get("schema")
        if pending_schema == "agentnet.guided-join.v3":
            expected |= {
                "identity_path",
                "browser_mode",
                "begin_idempotency_key",
                "replaced_authorization",
            }
        elif pending_schema == "agentnet.guided-join.v2":
            expected |= {"identity_path", "browser_mode"}
        elif pending_schema != "agentnet.guided-join.v1":
            raise SystemExit("guided join state does not match the exact schema")
        if set(pending) != expected:
            raise SystemExit("guided join state does not match the exact schema")
        if (
            pending["server_base_url"] != server
            or pending["domain_id"] != args.domain
            or pending["harness_kind"] != args.harness
            or pending["harness_name"] != args.name
            or (
                pending_schema in {"agentnet.guided-join.v2", "agentnet.guided-join.v3"}
                and (
                    pending["identity_path"] != str(identity_path)
                    or pending["browser_mode"] != args.browser
                )
            )
        ):
            raise SystemExit("guided join resume arguments do not match pending state")
        authorization = (
            None
            if pending["authorization"] is None
            else _validate_guided_authorization(pending["authorization"])
        )
        if pending_schema != "agentnet.guided-join.v3" and authorization is None:
            raise SystemExit("guided join authorization is missing")
        if pending_schema == "agentnet.guided-join.v3":
            begin_key = pending["begin_idempotency_key"]
            if (
                not isinstance(begin_key, str)
                or len(begin_key) != 43
                or any(character not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" for character in begin_key)
            ):
                raise SystemExit("guided join begin idempotency state is invalid")
            if authorization is None and (
                pending["challenge"] is not None or pending["approval_url"] is not None
            ):
                raise SystemExit("guided join pre-begin state is inconsistent")
        key_path = Path(str(pending["private_key_path"]))
        key = P256KeyPair.from_private_pem(
            _owner_only_file(key_path, label="guided join private key")
        )
        if key.public_pem != pending["public_key_pem"]:
            raise SystemExit("guided join private key no longer matches pending state")
        if args.replace_terminal_state:
            if pending_schema not in {"agentnet.guided-join.v2", "agentnet.guided-join.v3"}:
                raise SystemExit(
                    "guided join terminal replacement requires argument-bound state"
                )
            if authorization is None:
                raise SystemExit("guided join terminal replacement has no prior authorization")
            _polled, terminal_status, _interval = _poll_guided_authorization(
                server=server,
                authorization=authorization,
            )
            if terminal_status not in {"expired", "failed"}:
                raise SystemExit(
                    "guided join state is not terminal; rerun without --replace-terminal-state"
                )
            replace_terminal_state = True
    elif args.replace_terminal_state:
        raise SystemExit("guided join terminal replacement requires existing pending state")

    begin_required = (
        not state_exists
        or replace_terminal_state
        or (
            state_exists
            and pending.get("schema") == "agentnet.guided-join.v3"
            and pending.get("authorization") is None
        )
    )
    if begin_required:
        if args.browser == "terminal":
            _require_private_terminal_or_exit()
        if not state_exists:
            key_path = (
                Path(os.path.abspath(args.private_key))
                if args.private_key
                else state_path.with_suffix(".key.pem")
            )
            if os.path.lexists(key_path):
                key = P256KeyPair.from_private_pem(
                    _owner_only_file(key_path, label="guided join private key")
                )
            else:
                key = P256KeyPair.generate()
                _write_owner_only(key_path, key.private_pem)
        if not state_exists or replace_terminal_state:
            begin_key = secrets.token_urlsafe(32)
            pending = {
                "schema": "agentnet.guided-join.v3",
                "server_base_url": server,
                "domain_id": args.domain,
                "harness_kind": args.harness,
                "harness_name": args.name,
                "private_key_path": str(key_path),
                "public_key_pem": key.public_pem,
                "identity_path": str(identity_path),
                "browser_mode": args.browser,
                "begin_idempotency_key": begin_key,
                "authorization": None,
                "replaced_authorization": authorization if replace_terminal_state else None,
                "challenge": None,
                "approval_url": None,
            }
            if state_exists:
                _write_private_config(state_path, pending, force=True)
            else:
                _write_owner_json(state_path, pending)
            state_exists = True
        _setup_progress("authenticate", started, "start exact owner OIDC authorization")
        authorization = _validate_guided_authorization(
            _public_json_request(
                server=server,
                method="POST",
                path="/v1/enrollment/oidc/begin",
                body={
                    "idempotency_key": pending["begin_idempotency_key"],
                    "harness_kind": args.harness,
                    "harness_name": args.name,
                    "public_key_pem": key.public_pem,
                    **(
                        {"activation_mode": "remote_browser"}
                        if args.browser == "remote"
                        else {}
                    ),
                },
            )
        )
        pending = {
            **pending,
            "authorization": authorization,
            "replaced_authorization": None,
        }
        _write_private_config(state_path, pending, force=True)
        _setup_progress("approve", started, "request independent owner passkey approval")
        _handoff_guided_authorization(
            str(authorization["authorization_url"]),
            browser=args.browser,
        )
        authorization_url_disclosed = True

    challenge_value = pending.get("challenge")
    while True:
        if time.monotonic() >= deadline:
            raise SystemExit("guided enrollment timed out; pending state is retained")
        polled, status, interval = _poll_guided_authorization(
            server=server,
            authorization=authorization,
        )
        if status in {"approval_pending", "approval_ready"}:
            challenge_value = {
                "challenge_id": polled.get("challenge_id"),
                "nonce": polled.get("nonce"),
                "canonical_transaction_b64": polled.get("canonical_transaction_b64"),
            }
            _validate_guided_challenge(challenge_value)
            pending["challenge"] = challenge_value
            pending["approval_url"] = _validate_stable_approval_url(
                polled.get("approval_url")
            )
            _write_private_config(state_path, pending, force=True)
            if not approval_url_disclosed:
                _handoff_guided_authorization(
                    str(pending["approval_url"]),
                    browser=args.browser,
                    purpose="stable owner approval",
                )
                approval_url_disclosed = True
            if status == "approval_ready":
                break
        elif status not in {"authorization_pending", "slow_down"}:
            raise SystemExit(f"guided enrollment stopped in terminal state: {status}")
        if status == "authorization_pending" and not authorization_url_disclosed:
            if args.browser == "terminal":
                _require_private_terminal_or_exit()
            _handoff_guided_authorization(
                str(authorization["authorization_url"]),
                browser=args.browser,
            )
            authorization_url_disclosed = True
        time.sleep(min(interval, max(0.0, deadline - time.monotonic())))

    _setup_progress("enroll", started, "complete exact human and harness binding")
    _challenge, decoded = _validate_guided_challenge(challenge_value)
    result = _public_json_request(
        server=server,
        method="POST",
        path="/v1/enrollment/oidc/complete",
        body={
            "transaction_id": authorization["transaction_id"],
            "continuation_token": authorization["continuation_token"],
            "possession_signature": key.sign("agentnet.enrollment.pop.v1", decoded),
        },
    )
    actor = _guided_identity_result(
        result=result,
        expected_domain_id=args.domain,
        key=key,
    )
    identity = {
        "schema": "agentnet.identity-profile.v1",
        "server_base_url": server,
        "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
        "actor": actor.model_dump(mode="json"),
        "private_key_path": str(key_path),
    }
    identity_repeat = False
    if os.path.lexists(identity_path):
        existing = _guided_join_state(identity_path)
        if existing != identity:
            raise SystemExit("existing identity file conflicts with completed enrollment")
        identity_repeat = True
    else:
        _write_owner_json(identity_path, identity)
    _setup_progress("verify", started, "confirm identity-only terminal state")
    completed_state = {
        "schema": "agentnet.guided-join-complete.v1",
        "server_base_url": server,
        "domain_id": args.domain,
        "harness_kind": args.harness,
        "harness_name": args.name,
        "private_key_path": str(key_path),
        "public_key_pem": key.public_pem,
        "identity_path": str(identity_path),
        "actor": actor.model_dump(mode="json"),
    }
    _write_private_config(state_path, completed_state, force=True)
    print(
        json.dumps(
            _guided_success_output(
                identity_path=identity_path,
                actor=actor,
                idempotent_repeat=identity_repeat,
            ),
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_join_begin(args: argparse.Namespace) -> int:
    """Generate a device key and start the public OIDC enrollment redirect."""

    state_path = Path(args.state)
    key_path = Path(args.private_key).resolve() if args.private_key else state_path.with_suffix(".key.pem").resolve()
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    response = _public_json_request(
        server=args.server,
        method="POST",
        path="/v1/enrollment/oidc/begin",
        body={
            "idempotency_key": secrets.token_urlsafe(32),
            "harness_kind": args.harness,
            "harness_name": args.name,
            "public_key_pem": key.public_pem,
        },
    )
    pending = {
        "schema": "agentnet.join-pending.v1",
        "server_base_url": _canonical_server_origin(args.server),
        "private_key_path": str(key_path),
        "public_key_pem": key.public_pem,
        "authorization": response,
    }
    _write_owner_json(state_path, pending)
    print(
        json.dumps(
            {
                "state": str(state_path),
                "authorization_url": response.get("authorization_url"),
                "expires_at": response.get("expires_at"),
                "next": (
                    "complete the OIDC redirect, save its JSON response, obtain exact independent approval, "
                    "then run agentnet join complete"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_join_complete(args: argparse.Namespace) -> int:
    """Prove the candidate key and consume one exact independent enrollment approval."""

    state = _read_json_object(Path(args.state), label="join pending state")
    if set(state) != {
        "schema",
        "server_base_url",
        "private_key_path",
        "public_key_pem",
        "authorization",
    } or state.get("schema") != "agentnet.join-pending.v1":
        raise SystemExit("join pending state does not match the exact schema")
    challenge = _read_json_object(Path(args.challenge), label="OIDC callback challenge")
    if set(challenge) != {
        "challenge_id",
        "nonce",
        "expires_at",
        "canonical_transaction_b64",
    }:
        raise SystemExit("OIDC callback challenge does not match the exact schema")
    approval = _read_json_object(Path(args.approval), label="independent enrollment approval")
    try:
        transaction = base64.b64decode(
            str(challenge["canonical_transaction_b64"]).encode("ascii"),
            validate=True,
        )
        decoded = json.loads(transaction)
    except (UnicodeError, ValueError, TypeError) as exc:
        raise SystemExit("OIDC callback transaction is invalid") from exc
    if not isinstance(decoded, dict) or canonical_json(decoded) != transaction:
        raise SystemExit("OIDC callback transaction is not exact canonical JSON")
    key_path = Path(str(state["private_key_path"]))
    key = P256KeyPair.from_private_pem(
        _owner_only_file(key_path, label="join private key")
    )
    if key.public_pem != state["public_key_pem"]:
        raise SystemExit("join private key no longer matches the pending candidate")
    result = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/enrollment/complete",
        body={
            "challenge_id": challenge["challenge_id"],
            "nonce": challenge["nonce"],
            "canonical_transaction_b64": challenge["canonical_transaction_b64"],
            "possession_signature": key.sign("agentnet.enrollment.pop.v1", decoded),
            "independent_approval": approval,
        },
    )
    try:
        actor = VerifiedActor.model_validate(result["actor"])
    except Exception as exc:
        raise SystemExit("enrollment response lacks an exact verified actor") from exc
    if actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS:
        raise SystemExit("enrollment response did not create a human harness")
    identity_path = Path(args.identity)
    _write_owner_json(
        identity_path,
        {
            "schema": "agentnet.identity-profile.v1",
            "server_base_url": state["server_base_url"],
            "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
            "actor": actor.model_dump(mode="json"),
            "private_key_path": str(key_path.resolve()),
        },
        force=args.force,
    )
    print(
        json.dumps(
            {
                "identity": str(identity_path),
                "principal_id": actor.principal_id,
                "harness_id": actor.harness_id,
                "credential_id": actor.credential_id,
                "next": [
                    "ordinary client devices may now reconnect with this exact identity",
                    "for an always-on config, run agentnet server-agent activate --config agentnet.json --identity "
                    + str(identity_path),
                    (
                        "after server-agent activation and exact second guided enrollment, run "
                        "agentnet bootstrap-plan begin from the fresh identity"
                    ),
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def _invitation_record(path: Path) -> InternalInvitationRecord:
    try:
        value = json.loads(
            _owner_only_file(path.absolute(), label="internal invitation")
        )
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("internal invitation is not readable JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit("internal invitation must be one JSON object")
    if set(value) != {"invitation", "zero_authority_proposal"}:
        raise SystemExit("internal invitation file does not match the exact schema")
    if value["zero_authority_proposal"] is not True:
        raise SystemExit("internal invitation file makes an invalid authority claim")
    try:
        return InternalInvitationRecord.model_validate_json(
            json.dumps(value["invitation"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("internal invitation record is invalid") from exc


def _invitation_candidate_state(path: Path) -> tuple[dict[str, object], InternalInvitationRequest, P256KeyPair]:
    try:
        state = json.loads(
            _owner_only_file(path.absolute(), label="invitation candidate state")
        )
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("invitation candidate state is not readable JSON") from exc
    if not isinstance(state, dict):
        raise SystemExit("invitation candidate state must be one JSON object")
    if set(state) != {
        "schema",
        "server_base_url",
        "private_key_path",
        "request",
    } or state.get("schema") != "agentnet.invitation-candidate.v1":
        raise SystemExit("invitation candidate state does not match the exact schema")
    try:
        request = InternalInvitationRequest.model_validate_json(
            json.dumps(state["request"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("invitation candidate request is invalid") from exc
    key_path = Path(str(state["private_key_path"]))
    try:
        key = P256KeyPair.from_private_pem(
            _owner_only_file(key_path, label="invitation candidate key")
        )
    except Exception as exc:
        raise SystemExit("invitation candidate key is invalid") from exc
    if key.thumbprint != request.candidate_key_id or key.public_pem != request.candidate_public_key_pem:
        raise SystemExit("invitation candidate key no longer matches the exact request")
    return state, request, key


def _canonical_invitation_for_candidate(
    record: InternalInvitationRecord,
    request: InternalInvitationRequest,
) -> bytes:
    transaction = record.transaction
    exact_fields = (
        "invitation_id",
        "domain_id",
        "invited_oidc_issuer",
        "invited_oidc_subject",
        "invited_verified_email",
        "candidate_harness_id",
        "candidate_harness_kind",
        "candidate_harness_display_name",
        "candidate_binding_assurance",
        "candidate_key_id",
        "candidate_public_key_pem",
        "requested_capabilities",
        "expires_at",
        "predecessor_invitation_id",
        "reason",
    )
    if any(getattr(transaction, field) != getattr(request, field) for field in exact_fields):
        raise SystemExit("issued invitation does not match the candidate's exact request")
    return canonical_json(transaction.model_dump(mode="json"))


def command_invitation_prepare(args: argparse.Namespace) -> int:
    """Generate candidate-owned key material and a public exact sponsor request."""

    if args.expires_in < 60 or args.expires_in > 2_592_000:
        raise SystemExit("invitation lifetime must be between one minute and thirty days")
    state_path = Path(args.state)
    request_path = Path(args.request)
    key_path = (
        Path(args.private_key).resolve()
        if args.private_key
        else state_path.with_suffix(".key.pem").resolve()
    )
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    try:
        request = InternalInvitationRequest(
            invitation_id=args.invitation_id or str(uuid4()),
            domain_id=args.domain,
            invited_oidc_issuer=args.issuer,
            invited_oidc_subject=args.subject,
            invited_verified_email=args.email,
            candidate_harness_id=args.harness_id or str(uuid4()),
            candidate_harness_kind=args.harness,
            candidate_harness_display_name=args.name,
            candidate_binding_assurance=args.binding_assurance,
            candidate_key_id=key.thumbprint,
            candidate_public_key_pem=key.public_pem,
            requested_capabilities=tuple(sorted(set(args.capability or ()))),
            expires_at=datetime.now(UTC).replace(microsecond=0)
            + timedelta(seconds=args.expires_in),
            reason=args.reason,
        )
    except Exception as exc:
        raise SystemExit("internal invitation request is invalid") from exc
    public_request = request.model_dump(mode="json")
    _write_owner_json(request_path, public_request)
    _write_owner_json(
        state_path,
        {
            "schema": "agentnet.invitation-candidate.v1",
            "server_base_url": _canonical_server_origin(args.server),
            "private_key_path": str(key_path),
            "request": public_request,
        },
    )
    print(
        json.dumps(
            {
                "candidate_state": str(state_path),
                "sponsor_request": str(request_path),
                "invitation_id": request.invitation_id,
                "next": "send only the sponsor request file to a current authorized human sponsor",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_issue(args: argparse.Namespace) -> int:
    request_value = Path(args.request).read_text(encoding="utf-8")
    try:
        invitation = InternalInvitationRequest.model_validate_json(
            request_value,
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("internal invitation request does not match the exact schema") from exc
    client, actor, _key = _load_identity_client(Path(args.identity))
    if invitation.domain_id != actor.domain_id:
        client.close()
        raise SystemExit("internal invitation request crossed the sponsor's domain")
    try:
        response = client.request(
            "POST",
            "/v1/internal-invitations",
            json_body={"invitation": invitation.model_dump(mode="json")},
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"internal invitation issuance was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict):
        raise SystemExit("internal invitation issuance returned invalid JSON")
    _write_owner_json(Path(args.invitation), value, force=args.force)
    record = _invitation_record(Path(args.invitation))
    print(
        json.dumps(
            {
                "invitation": args.invitation,
                "invitation_id": record.transaction.invitation_id,
                "expires_at": record.transaction.expires_at.isoformat(),
                "positive_entitlements_issued": 0,
                "next": "return the invitation file to the exact candidate for OIDC verification",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_join_sponsored(args: argparse.Namespace) -> int:
    """Create candidate-owned proof state, discover one intent, and enter normal acceptance."""
    state_path = Path(args.state).resolve()
    invitation_path = Path(args.invitation).resolve()
    if os.path.lexists(state_path):
        state_bytes = _owner_only_file(state_path, label="sponsored enrollment state")
        try:
            value = json.loads(state_bytes)
        except (UnicodeError, json.JSONDecodeError) as exc:
            raise SystemExit("sponsored enrollment state is not readable JSON") from exc
        if not isinstance(value, dict):
            raise SystemExit("sponsored enrollment state must be one JSON object")
        if value.get("schema") == "agentnet.invitation-candidate.v1":
            if args.callback:
                return command_invitation_complete(
                    argparse.Namespace(
                        state=str(state_path),
                        invitation=str(invitation_path),
                        callback=args.callback,
                        identity=args.identity,
                        force=args.force,
                    )
            )
            return command_invitation_oidc_begin(
                argparse.Namespace(state=str(state_path), invitation=str(invitation_path))
            )
        if (
            set(value)
            != {
                "schema",
                "server_base_url",
                "private_key_path",
                "continuation_token",
            }
            or value.get("schema") != "agentnet.sponsored-enrollment-candidate.v1"
        ):
            raise SystemExit("sponsored enrollment state does not match the exact schema")
        if os.path.lexists(invitation_path):
            raise SystemExit(f"refusing to overwrite {invitation_path}; choose a new invitation path")
        status = _public_json_request(
            server=str(value["server_base_url"]),
            method="POST",
            path="/v1/sponsored-enrollment/candidate/status",
            body={"continuation_token": value["continuation_token"]},
        )
        invitation_value = status.get("invitation")
        if not isinstance(invitation_value, dict):
            print(json.dumps({"state": status.get("state"), "next": "retry after sponsor approval"}, indent=2))
            return 0
        record = InternalInvitationRecord.model_validate(invitation_value)
        transaction = record.transaction
        key_path = Path(str(value["private_key_path"]))
        try:
            candidate_key = P256KeyPair.from_private_pem(
                _owner_only_file(key_path, label="sponsored enrollment candidate key")
            )
        except Exception as exc:
            raise SystemExit("sponsored enrollment candidate key is invalid") from exc
        if (
            candidate_key.thumbprint != transaction.candidate_key_id
            or candidate_key.public_pem != transaction.candidate_public_key_pem
        ):
            raise SystemExit("issued invitation does not match the sponsored candidate key")
        request = InternalInvitationRequest(
            invitation_id=transaction.invitation_id,
            domain_id=transaction.domain_id,
            invited_oidc_issuer=transaction.invited_oidc_issuer,
            invited_oidc_subject=transaction.invited_oidc_subject,
            invited_verified_email=transaction.invited_verified_email,
            candidate_harness_id=transaction.candidate_harness_id,
            candidate_harness_kind=transaction.candidate_harness_kind,
            candidate_harness_display_name=transaction.candidate_harness_display_name,
            candidate_binding_assurance=transaction.candidate_binding_assurance,
            candidate_key_id=transaction.candidate_key_id,
            candidate_public_key_pem=transaction.candidate_public_key_pem,
            requested_capabilities=transaction.requested_capabilities,
            expires_at=transaction.expires_at,
            predecessor_invitation_id=transaction.predecessor_invitation_id,
            reason=transaction.reason,
        )
        invitation_output = {
            "invitation": record.model_dump(mode="json"),
            "zero_authority_proposal": True,
        }
        candidate_state = {
            "schema": "agentnet.invitation-candidate.v1",
            "server_base_url": value["server_base_url"],
            "private_key_path": value["private_key_path"],
            "request": request.model_dump(mode="json"),
        }
        _write_owner_json(invitation_path, invitation_output)
        _owner_only_directory(state_path.parent)
        with _private_state_lock(state_path):
            _write_private_config(
                state_path,
                candidate_state,
                force=True,
                expected_content=state_bytes,
            )
        return command_invitation_oidc_begin(
            argparse.Namespace(state=str(state_path), invitation=str(invitation_path))
        )

    key_path = (
        Path(args.private_key).resolve()
        if args.private_key
        else state_path.with_suffix(".key.pem").resolve()
    )
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    result = _public_json_request(
        server=args.server,
        method="POST",
        path="/v1/sponsored-enrollment/candidate/begin",
        body={
            "candidate_harness_id": args.harness_id or str(uuid4()),
            "harness_kind": args.harness,
            "harness_name": args.name,
            "binding_assurance": args.binding_assurance,
            "public_key_pem": key.public_pem,
            "idempotency_key": secrets.token_urlsafe(24),
        },
    )
    required = {"authorization_url", "continuation_token"}
    if not required <= result.keys() or any(not isinstance(result[key], str) for key in required):
        raise SystemExit("sponsored enrollment begin response is invalid")
    _write_owner_json(
        state_path,
        {
            "schema": "agentnet.sponsored-enrollment-candidate.v1",
            "server_base_url": _canonical_server_origin(args.server),
            "private_key_path": str(key_path),
            "continuation_token": result["continuation_token"],
        },
    )
    print(json.dumps({"authorization_url": result["authorization_url"], "state": str(state_path),
                      "next": "open authorization_url, then rerun this command"}, indent=2))
    return 0


def command_invitation_oidc_begin(args: argparse.Namespace) -> int:
    state, request, _key = _invitation_candidate_state(Path(args.state))
    record = _invitation_record(Path(args.invitation))
    canonical = _canonical_invitation_for_candidate(record, request)
    result = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/internal-invitations/oidc/begin",
        body={"canonical_invitation_b64": base64.b64encode(canonical).decode("ascii")},
    )
    print(
        json.dumps(
            {
                "authorization": result.get("authorization"),
                "next": (
                    "complete the displayed OIDC redirect, save exact state/code JSON, "
                    "then run agentnet invitation complete"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_complete(args: argparse.Namespace) -> int:
    state, request, key = _invitation_candidate_state(Path(args.state))
    record = _invitation_record(Path(args.invitation))
    canonical = _canonical_invitation_for_candidate(record, request)
    callback = _read_json_object(Path(args.callback), label="invitation OIDC callback")
    if set(callback) != {"state", "code"}:
        raise SystemExit("invitation OIDC callback does not match the exact schema")
    encoded = base64.b64encode(canonical).decode("ascii")
    completed = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/internal-invitations/oidc/complete",
        body={
            "canonical_invitation_b64": encoded,
            "state": callback["state"],
            "code": callback["code"],
        },
    )
    if set(completed) != {
        "oidc_transaction_id",
        "oidc_acceptance_token",
        "candidate_possession_fields",
        "expires_at",
    } or not isinstance(completed["candidate_possession_fields"], dict):
        raise SystemExit("invitation OIDC completion does not match the exact schema")
    acceptance = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/internal-invitations/accept",
        body={
            "canonical_invitation_b64": encoded,
            "oidc_transaction_id": completed["oidc_transaction_id"],
            "oidc_acceptance_token": completed["oidc_acceptance_token"],
            "candidate_possession_signature": key.sign(
                INTERNAL_INVITATION_POP_PURPOSE,
                completed["candidate_possession_fields"],
            ),
        },
    )
    if set(acceptance) != {"acceptance"} or not isinstance(acceptance["acceptance"], dict):
        raise SystemExit("invitation acceptance response does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(acceptance["acceptance"]["actor"])
    except Exception as exc:
        raise SystemExit("invitation acceptance lacks an exact verified actor") from exc
    if (
        actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.domain_id != request.domain_id
        or actor.harness_id != request.candidate_harness_id
    ):
        raise SystemExit("invitation acceptance identity does not match the exact candidate")
    _write_owner_json(
        Path(args.identity),
        {
            "schema": "agentnet.identity-profile.v1",
            "server_base_url": state["server_base_url"],
            "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
            "actor": actor.model_dump(mode="json"),
            "private_key_path": state["private_key_path"],
        },
        force=args.force,
    )
    print(
        json.dumps(
            {
                "identity": args.identity,
                "acceptance": acceptance["acceptance"],
                "positive_entitlements_issued": 0,
                "next": "reconnect using the new identity; authority must be granted separately",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_invitation_revoke(args: argparse.Namespace) -> int:
    record = _invitation_record(Path(args.invitation))
    resource, mutation = InternalInvitationService.revocation_binding(
        record.transaction.invitation_id,
        expected_revision=record.revision,
        reason=args.reason,
    )
    client, actor, key = _load_identity_client(Path(args.identity))
    command = _authority_command(
        actor=actor,
        key=key,
        action=INTERNAL_INVITATION_REVOKE_ACTION,
        resource=resource,
        mutation=mutation,
        expected_policy_revision=record.transaction.policy_revision,
        expected_entity_revision=record.revision,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            f"/v1/internal-invitations/{record.transaction.invitation_id}/revoke",
            json_body={"command": command.model_dump(mode="json")},
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"internal invitation revocation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def _c0_pilot_cli_result(response, *, expected_status: int) -> dict[str, str]:
    if response.status_code != expected_status:
        raise SystemExit(f"C0 pilot request was rejected with HTTP {response.status_code}")
    try:
        result = C0PilotResult.model_validate(response.json())
    except Exception as exc:
        raise SystemExit("C0 pilot response is invalid") from exc
    value = result.model_dump(mode="json", by_alias=True)
    if set(value) != {"schema", "status"}:
        raise SystemExit("C0 pilot response is invalid")
    return value


def command_c0_pilot(args: argparse.Namespace) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        if args.c0_pilot_command == "start":
            response = client.c0_pilot_start()
            expected = 201
        elif args.c0_pilot_command == "complete":
            response = client.c0_pilot_complete()
            expected = 200
        elif args.c0_pilot_command == "status":
            response = client.c0_pilot_status()
            expected = 200
        else:
            raise SystemExit("unknown C0 pilot operation")
    finally:
        client.close()
    print(
        json.dumps(
            _c0_pilot_cli_result(response, expected_status=expected),
            indent=2,
            sort_keys=True,
        )
    )
    return 0


_CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA = "agentnet.credential-renewal-cli-state.v1"


def _credential_renewal_cli_state(path: Path) -> dict[str, str]:
    resolved = path.resolve()
    if not os.path.lexists(resolved):
        value = {
            "schema": _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA,
            "request_id": str(uuid4()),
        }
        _write_private_config(resolved, value, force=False)
        return value
    try:
        value = json.loads(_owner_only_file(resolved, label="credential renewal state"))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("credential renewal state is not readable JSON") from exc
    if not isinstance(value, dict) or set(value) != {"schema", "request_id"}:
        raise SystemExit("credential renewal state does not match the exact schema")
    request_id = value.get("request_id")
    try:
        parsed = UUID(str(request_id))
    except (TypeError, ValueError, AttributeError) as exc:
        raise SystemExit("credential renewal state does not match the exact schema") from exc
    if value.get("schema") != _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA or str(parsed) != request_id:
        raise SystemExit("credential renewal state does not match the exact schema")
    return {"schema": _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA, "request_id": str(request_id)}


def command_credential_renew(args: argparse.Namespace) -> int:
    state_path = Path(args.state)
    state = _credential_renewal_cli_state(state_path)
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.renew_current_credential(request_id=state["request_id"])
    finally:
        client.close()
    if response.status_code != 200:
        print(json.dumps({"schema": "agentnet.credential-renewal-cli-result.v1", "status": "blocked"}, indent=2, sort_keys=True))
        return 1
    try:
        result = response.json()
    except Exception as exc:
        raise SystemExit("credential renewal response is invalid") from exc
    if (
        not isinstance(result, dict)
        or set(result) != {"schema", "status", "expires_at"}
        or result.get("schema") != "agentnet.credential-renewal-result.v1"
        or result.get("status") not in {"current", "renewed"}
        or not isinstance(result.get("expires_at"), int)
    ):
        raise SystemExit("credential renewal response is invalid")
    # Rotate only after exact response. If this local write fails, retry retains
    # the old ID and receives the exact stored result without another mutation.
    _write_private_config(
        state_path.resolve(),
        {"schema": _CREDENTIAL_RENEWAL_CLI_STATE_SCHEMA, "request_id": str(uuid4())},
        force=True,
    )
    print(json.dumps({"schema": "agentnet.credential-renewal-cli-result.v1", "status": result["status"]}, indent=2, sort_keys=True))
    return 0


_LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_SCHEMA = (
    "agentnet.laptop-credential-reauthorization-cli-state.v1"
)


_LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_KEYS = {
    "schema",
    "identity_path",
    "identity_profile_sha256",
    "private_key_sha256",
    "request_id",
    "possession_secret",
    "transaction",
    "old_key_possession_signature",
    "approval_url_sha256",
    "approval_url_opened",
    "result",
    "successor_identity_sha256",
}


_LAPTOP_CREDENTIAL_REAUTHORIZATION_RESULT_KEYS = {
    "schema",
    "status",
    "request_id",
    "domain_id",
    "principal_id",
    "harness_id",
    "previous_credential_id",
    "credential_id",
    "key_id",
    "credential_epoch",
    "not_before",
    "expires_at",
    "idempotent_repeat",
    "key_preserved",
    "authority_granted",
}


def _private_json_bytes(value: dict[str, object]) -> bytes:
    return json.dumps(value, indent=2, sort_keys=True).encode("utf-8") + b"\n"


def _load_laptop_reauthorization_identity(
    path: Path,
) -> tuple[
    Path,
    bytes,
    dict[str, object],
    VerifiedActor,
    Path,
    bytes,
    P256KeyPair,
]:
    identity_path = path.resolve()
    identity_raw = _owner_only_file(
        identity_path,
        label="AgentNet identity profile",
    )
    try:
        identity = json.loads(identity_raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit("AgentNet identity profile is not readable JSON") from exc
    if (
        not isinstance(identity, dict)
        or set(identity)
        != {
            "schema",
            "server_base_url",
            "audience",
            "actor",
            "private_key_path",
        }
        or identity.get("schema") != "agentnet.identity-profile.v1"
    ):
        raise SystemExit("AgentNet identity profile does not match the exact schema")
    try:
        actor = VerifiedActor.model_validate(identity["actor"])
    except Exception as exc:
        raise SystemExit("AgentNet identity profile actor is invalid") from exc
    if (
        actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.principal_id is None
        or actor.harness_id is None
        or actor.credential_id is None
        or actor.binding_assurance not in {"os_bound", "hardware_bound"}
    ):
        raise SystemExit(
            "expired credential reauthorization requires an exact bound laptop identity"
        )
    key_path = Path(str(identity["private_key_path"]))
    if not key_path.is_absolute():
        raise SystemExit("AgentNet identity private key path must be absolute")
    key_raw = _owner_only_file(
        key_path,
        label="AgentNet identity private key",
    )
    try:
        key = P256KeyPair.from_private_pem(key_raw)
    except Exception as exc:
        raise SystemExit("AgentNet identity private key is invalid") from exc
    return (
        identity_path,
        identity_raw,
        identity,
        actor,
        key_path,
        key_raw,
        key,
    )


def _laptop_reauthorization_state(
    *,
    path: Path,
    identity_path: Path,
    identity_raw: bytes,
    key_raw: bytes,
) -> tuple[dict[str, object], bytes]:
    identity_digest = hashlib.sha256(identity_raw).hexdigest()
    key_digest = hashlib.sha256(key_raw).hexdigest()
    if not os.path.lexists(path):
        state: dict[str, object] = {
            "schema": _LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_SCHEMA,
            "identity_path": str(identity_path),
            "identity_profile_sha256": identity_digest,
            "private_key_sha256": key_digest,
            "request_id": str(uuid4()),
            "possession_secret": secrets.token_urlsafe(32),
            "transaction": None,
            "old_key_possession_signature": None,
            "approval_url_sha256": None,
            "approval_url_opened": False,
            "result": None,
            "successor_identity_sha256": None,
        }
        raw = _private_json_bytes(state)
        _write_private_config(path, state)
        return state, raw
    try:
        raw = _owner_only_file(
            path,
            label="laptop credential reauthorization state",
        )
        value = json.loads(raw)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit(
            "laptop credential reauthorization state is not readable JSON"
        ) from exc
    if (
        not isinstance(value, dict)
        or set(value) != _LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_KEYS
        or value.get("schema")
        != _LAPTOP_CREDENTIAL_REAUTHORIZATION_STATE_SCHEMA
        or value.get("identity_path") != str(identity_path)
        or value.get("private_key_sha256") != key_digest
        or not isinstance(value.get("identity_profile_sha256"), str)
        or len(str(value.get("identity_profile_sha256"))) != 64
        or any(
            character not in "0123456789abcdef"
            for character in str(value.get("identity_profile_sha256"))
        )
        or not isinstance(value.get("approval_url_opened"), bool)
    ):
        raise SystemExit(
            "laptop credential reauthorization state does not match the exact binding"
        )
    try:
        request_id = UUID(str(value["request_id"]))
    except (TypeError, ValueError, AttributeError) as exc:
        raise SystemExit(
            "laptop credential reauthorization state does not match the exact binding"
        ) from exc
    possession_secret = value.get("possession_secret")
    transaction = value.get("transaction")
    signature = value.get("old_key_possession_signature")
    approval_url_sha256 = value.get("approval_url_sha256")
    result = value.get("result")
    successor_digest = value.get("successor_identity_sha256")
    if (
        str(request_id) != value["request_id"]
        or not isinstance(possession_secret, str)
        or len(possession_secret) != 43
        or (transaction is None) != (signature is None)
        or (
            signature is not None
            and (
                not isinstance(signature, str)
                or not 1 <= len(signature) <= 2_048
            )
        )
        or (
            approval_url_sha256 is not None
            and (
                not isinstance(approval_url_sha256, str)
                or len(approval_url_sha256) != 64
                or any(character not in "0123456789abcdef" for character in approval_url_sha256)
            )
        )
        or (value["approval_url_opened"] and approval_url_sha256 is None)
        or (result is None) != (successor_digest is None)
        or (result is not None and transaction is None)
        or (
            successor_digest is not None
            and (
                not isinstance(successor_digest, str)
                or len(successor_digest) != 64
                or any(character not in "0123456789abcdef" for character in successor_digest)
            )
        )
    ):
        raise SystemExit(
            "laptop credential reauthorization state does not match the exact binding"
        )
    if transaction is not None:
        try:
            parsed = LaptopCredentialReauthorizationRequest.model_validate(transaction)
        except Exception as exc:
            raise SystemExit(
                "laptop credential reauthorization state transaction is invalid"
            ) from exc
        if parsed.request_id != value["request_id"]:
            raise SystemExit(
                "laptop credential reauthorization state transaction binding changed"
            )
    return value, raw


def _replace_laptop_reauthorization_state(
    path: Path,
    state: dict[str, object],
    *,
    expected_raw: bytes,
) -> bytes:
    replacement = _private_json_bytes(state)
    _write_private_config(
        path,
        state,
        force=True,
        expected_content=expected_raw,
    )
    return replacement


def _laptop_reauthorization_transaction(
    value: object,
    *,
    state: dict[str, object],
    actor: VerifiedActor,
    key: P256KeyPair,
    now: int,
    require_active: bool = True,
) -> LaptopCredentialReauthorizationRequest:
    try:
        transaction = LaptopCredentialReauthorizationRequest.model_validate(value)
    except Exception as exc:
        raise SystemExit(
            "laptop credential reauthorization transaction is invalid"
        ) from exc
    public_key_sha256 = hashlib.sha256(key.public_pem.encode("utf-8")).hexdigest()
    if (
        transaction.request_id != state["request_id"]
        or transaction.domain_id != actor.domain_id
        or transaction.principal_id != actor.principal_id
        or transaction.harness_id != actor.harness_id
        or transaction.expired_credential_id != actor.credential_id
        or transaction.expected_credential_epoch != actor.credential_epoch
        or transaction.successor_credential_epoch != actor.credential_epoch + 1
        or transaction.expected_key_id != key.thumbprint
        or transaction.expected_public_key_sha256 != public_key_sha256
        or transaction.expected_binding_assurance != actor.binding_assurance
        or transaction.identity_profile_sha256
        != state["identity_profile_sha256"]
        or (
            require_active
            and (
                transaction.prepared_at > now
                or transaction.expires_at <= now
            )
        )
    ):
        raise SystemExit(
            "laptop credential reauthorization transaction binding changed"
        )
    return transaction


def _laptop_reauthorization_result(
    value: object,
    *,
    transaction: LaptopCredentialReauthorizationRequest,
) -> dict[str, object]:
    if (
        not isinstance(value, dict)
        or set(value) != _LAPTOP_CREDENTIAL_REAUTHORIZATION_RESULT_KEYS
        or value.get("schema")
        != "agentnet.laptop-credential-reauthorization-result.v1"
        or value.get("status") != "current"
        or value.get("request_id") != transaction.request_id
        or value.get("domain_id") != transaction.domain_id
        or value.get("principal_id") != transaction.principal_id
        or value.get("harness_id") != transaction.harness_id
        or value.get("previous_credential_id")
        != transaction.expired_credential_id
        or value.get("key_id") != transaction.expected_key_id
        or value.get("credential_epoch")
        != transaction.successor_credential_epoch
        or not isinstance(value.get("credential_id"), str)
        or not 1 <= len(str(value["credential_id"])) <= 256
        or value.get("credential_id") == transaction.expired_credential_id
        or type(value.get("not_before")) is not int
        or type(value.get("expires_at")) is not int
        or int(value["expires_at"]) <= int(value["not_before"])
        or type(value.get("idempotent_repeat")) is not bool
        or value.get("key_preserved") is not True
        or value.get("authority_granted") is not False
    ):
        raise SystemExit(
            "laptop credential reauthorization completion response is invalid"
        )
    return value


def _laptop_reauthorization_output(
    *,
    status: str,
    credential_epoch: int | None = None,
) -> dict[str, object]:
    output: dict[str, object] = {
        "schema": "agentnet.laptop-credential-reauthorization-cli-result.v1",
        "status": status,
    }
    if status == "current":
        output.update(
            {
                "credential_epoch": credential_epoch,
                "identity_saved_locally": True,
                "key_preserved": True,
                "authority_granted": False,
            }
        )
    return output


def _print_laptop_reauthorization_output(
    *,
    status: str,
    credential_epoch: int | None = None,
) -> None:
    print(
        json.dumps(
            _laptop_reauthorization_output(
                status=status,
                credential_epoch=credential_epoch,
            ),
            indent=2,
            sort_keys=True,
        )
    )


def _remove_laptop_reauthorization_state(path: Path) -> None:
    _remove_private_state(path, label="laptop credential reauthorization state")


def _complete_laptop_identity_reauthorization(
    *,
    identity_path: Path,
    identity_raw: bytes,
    identity: dict[str, object],
    actor: VerifiedActor,
    key_path: Path,
    key_raw: bytes,
    state_path: Path,
    state: dict[str, object],
    state_raw: bytes,
    result: dict[str, object],
) -> int:
    updated_actor = actor.model_copy(
        update={
            "credential_id": result["credential_id"],
            "credential_epoch": result["credential_epoch"],
        }
    )
    updated_identity = {
        **identity,
        "actor": updated_actor.model_dump(mode="json"),
    }
    updated_raw = _private_json_bytes(updated_identity)
    successor_digest = hashlib.sha256(updated_raw).hexdigest()
    state["result"] = result
    state["successor_identity_sha256"] = successor_digest
    _replace_laptop_reauthorization_state(
        state_path,
        state,
        expected_raw=state_raw,
    )
    if not secrets.compare_digest(
        _owner_only_file(key_path, label="AgentNet identity private key"),
        key_raw,
    ):
        raise SystemExit(
            "AgentNet identity private key changed before credential replacement"
        )
    _write_private_config(
        identity_path,
        updated_identity,
        force=True,
        expected_content=identity_raw,
    )
    replaced_identity = _owner_only_file(
        identity_path,
        label="AgentNet identity profile",
    )
    if (
        not secrets.compare_digest(replaced_identity, updated_raw)
        or hashlib.sha256(replaced_identity).hexdigest()
        != state["successor_identity_sha256"]
        or not secrets.compare_digest(
            _owner_only_file(key_path, label="AgentNet identity private key"),
            key_raw,
        )
    ):
        raise SystemExit(
            "AgentNet identity replacement could not be verified exactly"
        )
    _remove_laptop_reauthorization_state(state_path)
    _print_laptop_reauthorization_output(
        status="current",
        credential_epoch=updated_actor.credential_epoch,
    )
    return 0


def command_credential_reauthorize_expired(args: argparse.Namespace) -> int:
    if type(args.timeout) is not int or not 30 <= args.timeout <= 600:
        raise SystemExit(
            "expired credential reauthorization timeout must be between 30 and 600 seconds"
        )
    if args.browser not in {"system", "manual"}:
        raise SystemExit("expired credential reauthorization browser mode is invalid")
    (
        identity_path,
        identity_raw,
        identity,
        actor,
        key_path,
        key_raw,
        key,
    ) = _load_laptop_reauthorization_identity(Path(args.identity))
    state_path = Path(args.state).resolve()
    if state_path in {identity_path, key_path}:
        raise SystemExit(
            "credential reauthorization state must be separate from identity and key files"
        )
    state, state_raw = _laptop_reauthorization_state(
        path=state_path,
        identity_path=identity_path,
        identity_raw=identity_raw,
        key_raw=key_raw,
    )
    identity_digest = hashlib.sha256(identity_raw).hexdigest()
    if identity_digest != state["identity_profile_sha256"]:
        if (
            state["result"] is None
            or identity_digest != state["successor_identity_sha256"]
        ):
            raise SystemExit(
                "AgentNet identity profile changed during credential reauthorization"
            )
        try:
            transaction = LaptopCredentialReauthorizationRequest.model_validate(
                state["transaction"]
            )
        except Exception as exc:
            raise SystemExit(
                "completed credential reauthorization state is invalid"
            ) from exc
        result = _laptop_reauthorization_result(
            state["result"],
            transaction=transaction,
        )
        if (
            actor.domain_id != transaction.domain_id
            or actor.principal_id != transaction.principal_id
            or actor.harness_id != transaction.harness_id
            or actor.binding_assurance != transaction.expected_binding_assurance
            or actor.credential_id != result["credential_id"]
            or actor.credential_epoch != result["credential_epoch"]
            or key.thumbprint != transaction.expected_key_id
        ):
            raise SystemExit(
                "completed credential reauthorization identity binding changed"
            )
        _remove_laptop_reauthorization_state(state_path)
        _print_laptop_reauthorization_output(
            status="current",
            credential_epoch=actor.credential_epoch,
        )
        return 0

    now = int(time.time())
    if state["result"] is not None:
        transaction = _laptop_reauthorization_transaction(
            state["transaction"],
            state=state,
            actor=actor,
            key=key,
            now=now,
            require_active=False,
        )
        result = _laptop_reauthorization_result(
            state["result"],
            transaction=transaction,
        )
        return _complete_laptop_identity_reauthorization(
            identity_path=identity_path,
            identity_raw=identity_raw,
            identity=identity,
            actor=actor,
            key_path=key_path,
            key_raw=key_raw,
            state_path=state_path,
            state=state,
            state_raw=state_raw,
            result=result,
        )
    transaction: LaptopCredentialReauthorizationRequest | None = None
    if state["transaction"] is not None:
        transaction = _laptop_reauthorization_transaction(
            state["transaction"],
            state=state,
            actor=actor,
            key=key,
            now=now,
        )
    client = AgentNetClient(
        base_url=_canonical_server_origin(str(identity["server_base_url"])),
        key=key,
        domain_id=actor.domain_id,
        harness_id=actor.harness_id or "",
        credential_id=actor.credential_id or "",
        audience=str(identity["audience"]),
    )
    deadline = time.monotonic() + float(args.timeout)
    try:
        if transaction is None:
            response = client.prepare_expired_current_credential_reauthorization(
                request_id=str(state["request_id"]),
                identity_profile_sha256=str(
                    state["identity_profile_sha256"]
                ),
            )
            if response.status_code != 200:
                _print_laptop_reauthorization_output(status="blocked")
                return 1
            try:
                prepared_value = response.json()
            except Exception as exc:
                raise SystemExit(
                    "laptop credential reauthorization prepare response is invalid"
                ) from exc
            transaction = _laptop_reauthorization_transaction(
                prepared_value,
                state=state,
                actor=actor,
                key=key,
                now=now,
            )
            transaction_value = transaction.model_dump(
                mode="json",
                by_alias=True,
            )
            signature = key.sign(
                LAPTOP_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
                transaction.possession_fields(),
            )
            state["transaction"] = transaction_value
            state["old_key_possession_signature"] = signature
            state_raw = _replace_laptop_reauthorization_state(
                state_path,
                state,
                expected_raw=state_raw,
            )

        while True:
            response = client.progress_expired_current_credential_reauthorization(
                transaction=transaction.model_dump(
                    mode="json",
                    by_alias=True,
                ),
                old_key_possession_signature=str(
                    state["old_key_possession_signature"]
                ),
                possession_secret=str(state["possession_secret"]),
            )
            if response.status_code == 200:
                try:
                    completed_value = response.json()
                except Exception as exc:
                    raise SystemExit(
                        "laptop credential reauthorization completion response is invalid"
                    ) from exc
                result = _laptop_reauthorization_result(
                    completed_value,
                    transaction=transaction,
                )
                break
            if response.status_code != 202:
                _print_laptop_reauthorization_output(status="blocked")
                return 1
            try:
                pending = response.json()
            except Exception as exc:
                raise SystemExit(
                    "laptop credential reauthorization pending response is invalid"
                ) from exc
            if (
                not isinstance(pending, dict)
                or set(pending)
                != {"schema", "status", "approval_url", "expires_at"}
                or pending.get("schema")
                != "agentnet.laptop-credential-reauthorization-pending.v1"
                or pending.get("status") != "approval_pending"
                or type(pending.get("expires_at")) is not int
                or pending["expires_at"] != transaction.expires_at
            ):
                raise SystemExit(
                    "laptop credential reauthorization pending response is invalid"
                )
            approval_url = _validate_stable_approval_url(
                pending["approval_url"]
            )
            approval_url_sha256 = hashlib.sha256(
                approval_url.encode("utf-8")
            ).hexdigest()
            if (
                state["approval_url_sha256"] is not None
                and state["approval_url_sha256"] != approval_url_sha256
            ):
                raise SystemExit(
                    "laptop credential reauthorization approval entrypoint changed"
                )
            if not state["approval_url_opened"]:
                if args.browser == "manual":
                    _require_private_terminal_or_exit()
                state["approval_url_sha256"] = approval_url_sha256
                state_raw = _replace_laptop_reauthorization_state(
                    state_path,
                    state,
                    expected_raw=state_raw,
                )
                _handoff_guided_authorization(
                    approval_url,
                    browser=(
                        "system"
                        if args.browser == "system"
                        else "terminal"
                    ),
                    purpose="stable owner approval",
                )
                state["approval_url_opened"] = True
                state_raw = _replace_laptop_reauthorization_state(
                    state_path,
                    state,
                    expected_raw=state_raw,
                )
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                _print_laptop_reauthorization_output(
                    status="approval_pending"
                )
                return 2
            time.sleep(min(2.0, remaining))
    finally:
        client.close()

    return _complete_laptop_identity_reauthorization(
        identity_path=identity_path,
        identity_raw=identity_raw,
        identity=identity,
        actor=actor,
        key_path=key_path,
        key_raw=key_raw,
        state_path=state_path,
        state=state,
        state_raw=state_raw,
        result=result,
    )


def command_c0_pilot_responder(args: argparse.Namespace) -> int:
    config_path = Path(args.config)
    credential_path = Path(args.credential)
    config = load_c0_responder_config(config_path)
    if args.check:
        try:
            value = check_c0_responder(config, credential_path)
        except GateBlocked:
            print(
                json.dumps(
                    {
                        "schema": "agentnet.c0-pilot-responder.check.v1",
                        "status": "blocked",
                    },
                    indent=2,
                    sort_keys=True,
                )
            )
            return 1
    else:
        value = run_c0_responder(config, credential_path, config_path)
    print(json.dumps(value, indent=2, sort_keys=True))
    return 0


def _authority_command(
    *,
    actor: VerifiedActor,
    key: P256KeyPair,
    action: str,
    resource: str,
    mutation: dict[str, object],
    expected_policy_revision: int,
    expected_entity_revision: int,
    reason: str,
) -> SignedAuthorityCommand:
    issued_at = datetime.now(UTC).replace(microsecond=0)
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=action,
        resource=resource,
        request_digest=canonical_digest(mutation),
        expected_policy_revision=expected_policy_revision,
        expected_entity_revision=expected_entity_revision,
        reason=reason,
        issued_at=issued_at,
        expires_at=issued_at + timedelta(minutes=5),
    )
    return SignedAuthorityCommand(
        **fields,
        signature=key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
    )


def command_admin_entitlement_issue(args: argparse.Namespace) -> int:
    if args.expires_in < 300 or args.expires_in > 31_536_000:
        raise SystemExit("entitlement lifetime must be between five minutes and one year")
    client, actor, key = _load_identity_client(Path(args.identity))
    if args.beneficiary_identity is not None:
        beneficiary_client, beneficiary, _beneficiary_key = _load_identity_client(
            Path(args.beneficiary_identity)
        )
        beneficiary_client.close()
        if beneficiary.domain_id != actor.domain_id or beneficiary.principal_id is None:
            client.close()
            raise SystemExit("beneficiary identity is not a human in the issuer's exact domain")
        beneficiary_principal_id = beneficiary.principal_id
    else:
        beneficiary_principal_id = args.beneficiary_principal_id
        if (
            not beneficiary_principal_id
            or beneficiary_principal_id.strip() != beneficiary_principal_id
        ):
            client.close()
            raise SystemExit("beneficiary principal id must be a non-empty exact value")
    entitlement = HumanEntitlement(
        entitlement_id=args.entitlement_id or str(uuid4()),
        domain_id=actor.domain_id,
        principal_id=beneficiary_principal_id,
        action=args.action,
        resource_pattern=args.resource,
        revision=args.revision,
        expires_at=datetime.now(UTC).replace(microsecond=0)
        + timedelta(seconds=args.expires_in),
    )
    resource, mutation = PolicyEngine.entitlement_issuance_binding(
        entitlement,
        reason=args.reason,
    )
    command = _authority_command(
        actor=actor,
        key=key,
        action="authorization.entitlement.issue",
        resource=resource,
        mutation=mutation,
        expected_policy_revision=args.policy_revision,
        expected_entity_revision=0,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            "/v1/admin/entitlements",
            json_body={
                "entitlement": entitlement.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"entitlement issuance was rejected with HTTP {response.status_code}")
    print(
        json.dumps(
            {
                "entitlement": response.json(),
                "beneficiary_principal_id": beneficiary_principal_id,
                "authority_is_human_only": True,
                "next": "verify the exact bounded entitlement and retain its audit evidence",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_admin_entitlement_revoke(args: argparse.Namespace) -> int:
    client, actor, key = _load_identity_client(Path(args.identity))
    resource, mutation = PolicyEngine.entitlement_revocation_binding(
        args.entitlement_id,
        expected_entity_revision=args.expected_revision,
        reason=args.reason,
    )
    command = _authority_command(
        actor=actor,
        key=key,
        action="authorization.entitlement.revoke",
        resource=resource,
        mutation=mutation,
        expected_policy_revision=args.policy_revision,
        expected_entity_revision=args.expected_revision,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            f"/v1/admin/entitlements/{args.entitlement_id}/revoke",
            json_body={"command": command.model_dump(mode="json")},
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"entitlement revocation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_admin_harness_revoke_prepare(args: argparse.Namespace) -> int:
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/admin/harness-revocations/prepare",
            json_body={"harness_id": args.harness_id, "reason": args.reason},
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"harness revocation preparation was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict):
        raise SystemExit("harness revocation preparation returned invalid JSON")
    _write_owner_json(Path(args.request), value, force=args.force)
    print(
        json.dumps(
            {
                "request": args.request,
                "transaction": value,
                "next": "obtain exact independent identity.harness.revoke.approve receipt, then commit",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_admin_harness_revoke_commit(args: argparse.Namespace) -> int:
    request_value = _read_json_object(Path(args.request), label="harness revocation request")
    approval = _read_json_object(Path(args.approval), label="independent harness revocation approval")
    client, _actor, _key = _load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/admin/harness-revocations/commit",
            json_body={"request": request_value, "independent_approval": approval},
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"harness revocation was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_recovery_begin(args: argparse.Namespace) -> int:
    state_path = Path(args.state)
    key_path = (
        Path(args.private_key).resolve()
        if args.private_key
        else state_path.with_suffix(".key.pem").resolve()
    )
    key = P256KeyPair.generate()
    _write_owner_only(key_path, key.private_pem)
    response = _public_json_request(
        server=args.server,
        method="POST",
        path="/v1/credential-recovery/oidc/begin",
        body={
            "old_harness_id": args.old_harness_id,
            "new_harness_kind": args.harness,
            "new_harness_name": args.name,
            "new_binding_assurance": args.binding_assurance,
            "new_public_key_pem": key.public_pem,
        },
    )
    _write_owner_json(
        state_path,
        {
            "schema": "agentnet.recovery-pending.v1",
            "server_base_url": _canonical_server_origin(args.server),
            "private_key_path": str(key_path),
            "public_key_pem": key.public_pem,
            "authorization": response,
        },
    )
    print(
        json.dumps(
            {
                "state": str(state_path),
                "authorization_url": response.get("authorization_url"),
                "expires_at": response.get("expires_at"),
                "next": "complete OIDC redirect, save callback JSON, collect recovery approvals, then run recovery complete",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_recovery_complete(args: argparse.Namespace) -> int:
    state = _read_json_object(Path(args.state), label="credential recovery pending state")
    if set(state) != {
        "schema",
        "server_base_url",
        "private_key_path",
        "public_key_pem",
        "authorization",
    } or state.get("schema") != "agentnet.recovery-pending.v1":
        raise SystemExit("credential recovery pending state does not match the exact schema")
    callback = _read_json_object(Path(args.callback), label="credential recovery OIDC callback")
    if set(callback) != {"recovery_request", "recovery_transaction_id"}:
        raise SystemExit("credential recovery callback does not match the exact schema")
    try:
        recovery_request = CredentialRecoveryRequest.model_validate(
            callback["recovery_request"],
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("credential recovery callback request is invalid") from exc
    transaction_id = callback["recovery_transaction_id"]
    if not isinstance(transaction_id, str) or not 16 <= len(transaction_id) <= 128:
        raise SystemExit("credential recovery callback transaction identifier is invalid")
    key_path = Path(str(state["private_key_path"]))
    if key_path.is_symlink() or not key_path.is_file() or key_path.stat().st_mode & 0o077:
        raise SystemExit("credential recovery private key is not an owner-only real file")
    key = P256KeyPair.from_private_pem(key_path.read_bytes())
    if (
        key.public_pem != state["public_key_pem"]
        or recovery_request.new_public_key_pem != key.public_pem
    ):
        raise SystemExit("credential recovery key no longer matches the exact OIDC transaction")
    approvals = tuple(
        _read_json_object(Path(path), label="independent credential recovery approval")
        for path in args.approval
    )
    response = _public_json_request(
        server=str(state["server_base_url"]),
        method="POST",
        path="/v1/credential-recovery/complete",
        body={
            "recovery_transaction_id": transaction_id,
            "possession_signature": key.sign(
                "agentnet.recovery.pop.v1",
                recovery_request.signed_fields(),
            ),
            "independent_approvals": approvals,
        },
    )
    try:
        actor = VerifiedActor(
            kind=ActorKind.VERIFIED_HUMAN_HARNESS,
            domain_id=recovery_request.domain_id,
            principal_id=str(response["principal_id"]),
            harness_id=str(response["harness_id"]),
            credential_id=str(response["credential_id"]),
            credential_epoch=int(response["credential_epoch"]),
            binding_assurance=recovery_request.new_binding_assurance,
        )
    except Exception as exc:
        raise SystemExit("credential recovery response lacks an exact verified actor binding") from exc
    identity_path = Path(args.identity)
    _write_owner_json(
        identity_path,
        {
            "schema": "agentnet.identity-profile.v1",
            "server_base_url": state["server_base_url"],
            "audience": f"urn:agentnet:{actor.domain_id}:corporate-api",
            "actor": actor.model_dump(mode="json"),
            "private_key_path": str(key_path.resolve()),
        },
        force=args.force,
    )
    print(
        json.dumps(
            {
                "identity": str(identity_path),
                "recovery": response,
                "old_harness_revoked_atomically": True,
                "next": "reconnect with the new identity; rotate into a normal-lifetime credential as policy requires",
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0

__all__ = (
    "command_join_guided",
    "command_join_begin",
    "command_join_complete",
    "command_invitation_prepare",
    "command_invitation_issue",
    "command_invitation_join_sponsored",
    "command_invitation_oidc_begin",
    "command_invitation_complete",
    "command_invitation_revoke",
    "command_c0_pilot",
    "command_credential_renew",
    "command_credential_reauthorize_expired",
    "command_c0_pilot_responder",
    "command_admin_entitlement_issue",
    "command_admin_entitlement_revoke",
    "command_admin_harness_revoke_prepare",
    "command_admin_harness_revoke_commit",
    "command_recovery_begin",
    "command_recovery_complete",
)
