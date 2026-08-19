"""CLI commands for diagnostics, verification, harness probing, and status."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import shutil
import subprocess
import sys
import tempfile
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.parse import urlsplit
from uuid import uuid4

import httpx
from a2a.types import AgentCapabilities, AgentCard, Message, Part, Role, SendMessageRequest
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette

from agentnet.authorization import AUTHORITY_COMMAND_PURPOSE
from agentnet.core.app import CommunicationCore
from agentnet.core.capabilities import ServerAgentCapability
from agentnet.errors import GateBlocked
from agentnet.gateways.a2a import (
    SSRFPolicy,
    StandingA2AGrant,
    build_exported_agent_card,
    build_starlette_routes,
    create_tainted_proposal_handler,
    generate_opaque_route,
)
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.operations.incident import (
    DomainIncidentService,
    IncidentMode,
    IncidentModeChange,
)
from agentnet.security.signatures import P256KeyPair
from agentnet.storage.postgres import PostgreSQLReadiness
from agentnet.supervisor.c0_responder import (
    check_c0_responder,
    load_c0_responder_config,
    run_c0_responder,
)
from agentnet.supervisor.demos import (
    content_free_demo_summary,
    run_deterministic_harness_demo,
)
from agentnet.supervisor.live_gate import (
    assert_installed_probe_report,
    installed_probe_report,
    run_live_harness_gate,
)
from agentnet.cli import helpers


def _verification_package_root() -> Path:
    configured = os.environ.get("AGENTNET_PACKAGE_ROOT")
    package_root = (
        Path(configured).expanduser().resolve()
        if configured
        else Path(__file__).resolve().parents[3]
    )
    tests_root = package_root / "tests"
    if not tests_root.is_dir():
        raise SystemExit(
            "AgentNet packaged tests are unavailable; reinstall the complete npm package "
            "or run verification from a source checkout"
        )
    return package_root


def command_verify(args: argparse.Namespace) -> int:
    pytest_arguments = tuple(args.pytest_args)
    if pytest_arguments:
        raise SystemExit("agentnet verify does not permit pytest arguments")
    package_root = _verification_package_root()
    with tempfile.TemporaryDirectory(prefix="agentnet-verify-") as runtime_directory:
        runtime_root = Path(runtime_directory)
        verification_root = runtime_root / "package"
        shutil.copytree(
            package_root,
            verification_root,
            symlinks=True,
            ignore=shutil.ignore_patterns(
                ".agentnet",
                ".git",
                ".hypothesis",
                ".pi",
                ".pytest_cache",
                ".venv",
                "__pycache__",
                "*.pyc",
                "*.pyo",
                "build",
                "dist",
                "node_modules",
            ),
        )
        tests_root = verification_root / "tests"
        host_specific = (
            tests_root / "adapters/test_installed_live_inference.py",
            tests_root / "adapters/test_subprocess_lifecycle.py",
            tests_root / "components/test_bakeoff_evidence.py",
        )
        environment = os.environ.copy()
        environment.update(
            {
                "AGENTNET_PACKAGE_ROOT": str(verification_root),
                "AGENTNET_VERIFICATION_INSTALL_ROOT": str(package_root),
                "HYPOTHESIS_STORAGE_DIRECTORY": str(runtime_root / "hypothesis"),
                "PYTEST_ADDOPTS": "",
                "PYTHONDONTWRITEBYTECODE": "1",
                "PYTHONPATH": os.pathsep.join(
                    (str(verification_root / "src"), str(verification_root))
                ),
                "PYTHONPYCACHEPREFIX": str(runtime_root / "pycache"),
            }
        )
        environment.pop("PYTEST_PLUGINS", None)
        return subprocess.call(
            [
                sys.executable,
                "-m",
                "pytest",
                "-q",
                "-p",
                "no:cacheprovider",
                str(tests_root),
                *(f"--ignore={path}" for path in host_specific),
                *pytest_arguments,
            ],
            cwd=verification_root,
            env=environment,
        )


def command_harness_probe(args: argparse.Namespace) -> int:
    root = Path(args.data_dir)
    if args.harness != "all":
        report = installed_probe_report(root, harnesses=(args.harness,))
        probe = report[args.harness]
        ready = bool(probe.get("matches_pin") and probe.get("resolved_path"))
        result: dict[str, object] = {
            "diagnostic_only": True,
            "harness": args.harness,
            "probe": probe,
            "ready": ready,
            "scope": "single_harness",
        }
        if not ready:
            result["error"] = {
                "code": "installed_harness_mismatch",
                "message": "requested installed harness is absent or version-mismatched",
            }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0 if ready else 1

    report = installed_probe_report(root)
    try:
        assert_installed_probe_report(report)
    except GateBlocked as exc:
        print(
            json.dumps(
                {"ready": False, "error": exc.public_detail(), "harnesses": report},
                indent=2,
                sort_keys=True,
            )
        )
        return 1
    print(json.dumps({"ready": True, "harnesses": report}, indent=2, sort_keys=True))
    return 0


def command_harness_demo(args: argparse.Namespace) -> int:
    result = run_deterministic_harness_demo(
        Path(args.data_dir),
        request_timeout_seconds=args.request_timeout,
    )
    print(json.dumps(content_free_demo_summary(result), indent=2, sort_keys=True))
    return 0


def command_harness_live_gate(args: argparse.Namespace) -> int:
    harnesses = (
        ("claude", "codex", "pi", "antigravity")
        if args.harness == "all"
        else (args.harness,)
    )
    results: dict[str, object] = {}
    for harness in harnesses:
        try:
            results[harness] = run_live_harness_gate(
                harness,
                root=Path(args.data_dir),
                request_timeout_seconds=args.request_timeout,
            )
        except GateBlocked as exc:
            print(
                json.dumps(
                    {
                        "ready": False,
                        "failed_harness": harness,
                        "error": exc.public_detail(),
                        "completed_harnesses": results,
                    },
                    indent=2,
                    sort_keys=True,
                ),
                file=sys.stderr,
            )
            return 1
    print(json.dumps({"ready": True, "harnesses": results}, indent=2, sort_keys=True))
    return 0


def command_a2a_demo(_args: argparse.Namespace) -> int:
    """Exercise the official SDK REST route with an inert external proposal."""

    async def run() -> dict[str, object]:
        route = generate_opaque_route(logical_agent_id="synthetic-public-agent", domain_id="demo.example")
        template = AgentCard(
            name="Synthetic public proposal agent",
            description="Local A2A v1 conformance route",
            version="0.1.8",
            capabilities=AgentCapabilities(streaming=False),
            default_input_modes=["text/plain"],
            default_output_modes=["text/plain"],
        )
        card = build_exported_agent_card(template, route=route, public_base_url="https://agents.example")
        grant = StandingA2AGrant(
            grant_id="synthetic-standing-grant",
            route_token=route.route_token,
            logical_agent_id=route.logical_agent_id,
            allowed_actions=frozenset({"a2a.message.send", "a2a.task.get"}),
            allowed_resources=frozenset({route.logical_agent_id}),
            allowed_output_sinks=frozenset({"inert-proposal"}),
            expires_at=datetime.now(UTC) + timedelta(minutes=5),
        )
        routes = build_starlette_routes(
            request_handler=create_tainted_proposal_handler(card),
            agent_card=card,
            route=route,
            grant_lookup=lambda token: grant if token == route.route_token else None,
            peer_resolver=lambda _request: "synthetic-peer",
            url_policy=SSRFPolicy(allowed_hosts=frozenset({"agents.example"})),
            resolver=lambda _host, _port: ("93.184.216.34",),
            extension_config=ExtensionConfig(
                domain_id="demo.example",
                public_base_url="https://agents.example",
                server_agent_capabilities=frozenset(
                    {
                        ServerAgentCapability.A2A_GATEWAY,
                        ServerAgentCapability.ARTIFACT_STORAGE,
                        ServerAgentCapability.OFFLINE_CUSTODY,
                    }
                ),
            ),
        )
        app = Starlette(routes=routes)
        inbound = SendMessageRequest(
            tenant=route.tenant,
            message=Message(
                message_id="external-message-demo",
                context_id="external-context-demo",
                role=Role.ROLE_USER,
                parts=[Part(text="untrusted content remains tainted")],
            ),
        )
        transport = httpx.ASGITransport(app=app, raise_app_exceptions=False)
        async with httpx.AsyncClient(transport=transport, base_url="https://agents.example") as client:
            response = await client.post(
                f"{route.path_prefix}/message:send",
                json=MessageToDict(inbound),
                headers={"A2A-Version": "1.0"},
            )
        return {
            "status_code": response.status_code,
            "route": route.path_prefix,
            "response": response.json(),
            "warning": "synthetic local A2A proposal; no corporate identity, authority, execution, or durability",
        }

    print(json.dumps(asyncio.run(run()), indent=2, sort_keys=True))
    return 0


def command_status(args: argparse.Namespace) -> int:
    if type(args.timeout) is not float or not 0.1 <= args.timeout <= 10.0:
        raise SystemExit("status --timeout must be between 0.1 and 10 seconds")
    config = helpers._load_config(Path(args.config))
    if config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        local_readiness = PostgreSQLReadiness(config.resolved_database_url()).probe()
        local_readiness.update(
            {
                "profile": config.profile.value,
                "instance_id": config.runtime_instance_id,
                "artifact_root_present": config.artifact_dir.is_dir(),
                "mutating_runtime_lease_acquired": False,
            }
        )
    else:
        core = CommunicationCore.open(config)
        try:
            local_readiness = core.readiness()
        finally:
            core.close()

    live_connectivity: dict[str, object]
    if args.local_only:
        live_connectivity = {
            "checked": False,
            "reachable": False,
            "ready": False,
            "reason": "live probe disabled by --local-only",
        }
    else:
        try:
            with httpx.Client(
                base_url=config.public_base_url,
                timeout=args.timeout,
                follow_redirects=False,
            ) as client:
                health = client.get("/healthz")
                readiness = client.get("/readyz")
            live_connectivity = {
                "checked": True,
                "reachable": True,
                "ready": health.status_code == 200 and readiness.status_code == 200,
                "health_status": health.status_code,
                "readiness_status": readiness.status_code,
            }
        except (httpx.HTTPError, OSError) as exc:
            live_connectivity = {
                "checked": True,
                "reachable": False,
                "ready": False,
                "error_type": type(exc).__name__,
            }

    local_ready = bool(local_readiness.get("ready"))
    ready = local_ready and (
        bool(live_connectivity["ready"]) if not args.local_only else True
    )
    print(
        json.dumps(
            {
                "ready": ready,
                "local_readiness": local_readiness,
                "live_connectivity": live_connectivity,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0 if ready else 1

def _provision_owner_only_key(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.parent.is_symlink() or path.parent.stat().st_mode & 0o077:
        raise SystemExit(f"key directory must be an owner-only real directory: {path.parent}")
    if os.path.lexists(path):
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077 or path.stat().st_size != 32:
            raise SystemExit(f"existing key is not an owner-only 32-byte file: {path}")
        return
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        remaining = memoryview(os.urandom(32))
        while remaining:
            written = os.write(descriptor, remaining)
            if written <= 0:
                raise OSError("key provisioning write made no progress")
            remaining = remaining[written:]
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def _provision_owner_only_signing_key(path: Path) -> P256KeyPair:
    """Create or reload one exact owner-only P-256 software key."""

    path = path.absolute()
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if (
        path.parent.is_symlink()
        or path.parent.stat().st_uid != os.geteuid()
        or path.parent.stat().st_mode & 0o077
    ):
        raise SystemExit(f"signing-key directory must be an owner-only real directory: {path.parent}")
    if os.path.lexists(path):
        return P256KeyPair.from_private_pem(
            helpers._owner_only_file(path, label="existing backup seal private key")
        )
    key = P256KeyPair.generate()
    helpers._write_owner_only(path, key.private_pem)
    return key


def command_bootstrap_server_agent(args: argparse.Namespace) -> int:
    """Provision shared software keys, migrate PostgreSQL, and verify recovery."""

    config = helpers._load_config(Path(args.config))
    if config.profile is not RuntimeProfile.ALWAYS_ON_SERVER_AGENT:
        raise SystemExit("bootstrap-server-agent requires always_on_server_agent profile")
    secrets_dir = config.data_dir / "secrets"
    _provision_owner_only_key(secrets_dir / "records.key")
    if config.artifact_mode == "enabled":
        _provision_owner_only_key(secrets_dir / "artifact.key")
    core = CommunicationCore.open(config, validate_deployment_identity=False)
    try:
        domain = core.bootstrap_domain()
        recovery = core.recovery_status(record_observation=True)
        storage = core.store.readiness()
        audit = core.audit.verify()
        print(
            json.dumps(
                {
                    "domain": domain,
                    "recovery": recovery,
                    "storage": storage,
                    "audit": audit,
                    "deployment_binding": core.server_agent_binding_status(),
                    "warning": "software-key/single-PostgreSQL bootstrap; no HA, mTLS, KMS, or restore claim",
                },
                indent=2,
                sort_keys=True,
            )
        )
        return 0 if recovery["ready"] and storage["ready"] and audit["valid"] else 1
    finally:
        core.close()


def command_demo(args: argparse.Namespace) -> int:
    root = Path(args.data_dir)
    config = ExtensionConfig(
        domain_id="demo.example",
        data_dir=root,
        database_url=f"sqlite:///{root / 'core.sqlite3'}",
        artifact_dir=root / "artifacts",
    )
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()
        sender, _sender_key = core.bootstrap_synthetic_identity(harness_kind="codex", display_name="demo-sender")
        recipient, _recipient_key = core.bootstrap_synthetic_identity(harness_kind="pi", display_name="demo-recipient")
        accepted = core.send_synthetic_message(
            actor=sender,
            recipients=(recipient.harness_id,),
            payload={"synthetic": True, "text": "synthetic local conformance message"},
            idempotency_key=f"demo-message-{uuid4()}",
        )
        inbox = core.reconcile_synthetic_mailbox(actor=recipient)
        print(
            json.dumps(
                {
                    "warning": "synthetic local-conformance identity; not production enrollment or durability",
                    "accepted": accepted,
                    "recipient": recipient.harness_id,
                    "inbox": inbox,
                    "readiness": core.readiness(),
                },
                indent=2,
                sort_keys=True,
            )
        )
    finally:
        core.close()
    return 0


def command_incident_status(args: argparse.Namespace) -> int:
    return helpers._identity_client_json_call(
        Path(args.identity),
        "GET",
        "/v1/operator/incident",
        label="incident status",
    )


def command_incident_set(args: argparse.Namespace) -> int:
    from agentnet.cli import _authority_command

    client, actor, key = helpers._load_identity_client(Path(args.identity))
    change = IncidentModeChange(
        domain_id=actor.domain_id,
        expected_revision=args.expected_revision,
        target_mode=IncidentMode(args.mode),
        reason=args.reason,
    )
    resource, mutation = DomainIncidentService.authority_binding(change)
    command = _authority_command(
        actor=actor,
        key=key,
        action=DomainIncidentService.ACTION,
        resource=resource,
        mutation=mutation,
        expected_policy_revision=args.policy_revision,
        expected_entity_revision=args.expected_revision,
        reason=args.reason,
    )
    try:
        response = client.request(
            "POST",
            "/v1/operator/incident",
            json_body={
                "change": change.model_dump(mode="json"),
                "command": command.model_dump(mode="json"),
            },
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"incident transition was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0
