"""CLI commands for long-running services, daemons, manager gateway, and client setup."""

from __future__ import annotations

import argparse
import html
import ipaddress
import json
import os
import socket
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from urllib.parse import urlsplit

import httpx
import uvicorn

from agentnet.bindings.remote_manager import (
    resolve_packaged_manager_extension,
    run_manager_gateway,
    validate_manager_command,
)
from agentnet.console.http import create_console_app
from agentnet.core.app import CommunicationCore
from agentnet.errors import GateBlocked, ValidationError
from agentnet.http_api import create_app
from agentnet.identity.actors import VerifiedActor
from agentnet.operations.client_setup import (
    ClientIdentityProfile,
    ClientSetupCoordinator,
    ClientSetupContinuationStore,
    ClientSetupError,
    ClientSetupResult,
    EnrollmentProgress,
    SetupNextAction,
)
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.security.signatures import canonical_digest
from agentnet.supervisor.daemon import (
    load_supervisor_config,
    redacted_supervisor_status,
    run_supervisor_daemon,
)
from agentnet.cli import helpers


def _require_safe_serve_binding(config: ExtensionConfig, *, host: str, port: int) -> None:
    """The built-in Uvicorn command is plaintext; expose it only on loopback."""

    try:
        bind_address = ipaddress.ip_address(host)
    except ValueError as exc:
        raise GateBlocked(
            "remote_plaintext_bind",
            "agentnet serve requires an explicit loopback bind behind any remote HTTPS terminator",
        ) from exc
    if not bind_address.is_loopback:
        raise GateBlocked(
            "remote_plaintext_bind",
            "agentnet serve refuses a remotely reachable plaintext bind; use a loopback reverse-proxy upstream",
        )
    if config.service_scheme == "http":
        origin = urlsplit(config.public_base_url)
        try:
            origin_address = ipaddress.ip_address(origin.hostname or "")
        except ValueError as exc:
            raise GateBlocked("loopback_origin", "HTTP service origin must be a literal loopback address") from exc
        origin_port = origin.port or 80
        if bind_address != origin_address or port != origin_port:
            raise GateBlocked(
                "loopback_origin",
                "HTTP loopback bind must exactly match the configured public_base_url authority",
            )


def command_serve(args: argparse.Namespace) -> int:
    config = helpers._load_config(Path(args.config))
    _require_safe_serve_binding(config, host=args.host, port=args.port)
    enrollment_bootstrap = bool(
        config.profile is RuntimeProfile.ALWAYS_ON_SERVER_AGENT
        and config.oidc_enrollment is not None
        and (not config.enrolled_harness_id or not config.enrolled_credential_id)
    )
    core = CommunicationCore.open(
        config,
        validate_deployment_identity=not enrollment_bootstrap,
    )
    try:
        core.bootstrap_domain()
        uvicorn.run(create_app(core), host=args.host, port=args.port, log_level=args.log_level)
    finally:
        core.close()
    return 0


def _console_json_response(response: httpx.Response, *, label: str) -> dict[str, object]:
    return helpers._validate_http_json_response(response, label=label)


def _canonical_console_origin(value: object) -> str:
    if not isinstance(value, str):
        raise SystemExit("console challenge returned an invalid console origin")
    parsed = urlsplit(value)
    if (
        parsed.scheme != "https"
        or not parsed.netloc
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise SystemExit("console challenge returned an invalid console origin")
    return f"https://{parsed.netloc}"


def _serve_one_shot_loopback_page(
    *,
    document: str,
    open_browser=webbrowser.open,
    timeout_seconds: float,
) -> None:
    served = False
    payload = document.encode("utf-8")

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            nonlocal served
            if served or self.path != "/":
                self.send_error(404)
                return
            served = True
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("Pragma", "no-cache")
            self.send_header("Referrer-Policy", "no-referrer")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header(
                "Content-Security-Policy",
                "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action https:",
            )
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, _format: str, *args: object) -> None:
            del args

    try:
        server = HTTPServer(("127.0.0.1", 0), Handler)
    except OSError as exc:
        raise SystemExit("console browser handoff could not bind a loopback listener") from exc
    server.timeout = timeout_seconds
    loopback_url = f"http://127.0.0.1:{server.server_port}/"
    try:
        try:
            opened = open_browser(loopback_url, new=1)
        except Exception as exc:
            raise SystemExit("console browser handoff could not open the system browser") from exc
        if not opened:
            raise SystemExit("console browser handoff could not open the system browser")
        try:
            server.handle_request()
        except (OSError, socket.timeout) as exc:
            raise SystemExit("console browser handoff failed before the page was delivered") from exc
        if not served:
            raise SystemExit("console browser handoff page was not delivered")
    finally:
        server.server_close()


def _open_console_handoff_page(
    *,
    console_origin: str,
    handoff_token: str,
    timeout_seconds: float,
    open_browser=webbrowser.open,
) -> None:
    if (
        not isinstance(handoff_token, str)
        or not 32 <= len(handoff_token) <= 128
        or any(ord(character) > 0x7F for character in handoff_token)
    ):
        raise SystemExit("console handoff response is invalid")
    action = html.escape(f"{console_origin}/v1/console/open", quote=True)
    token = html.escape(handoff_token, quote=True)
    document = (
        '<!doctype html><html lang="en"><head><meta charset="utf-8">'
        '<meta name="viewport" content="width=device-width,initial-scale=1">'
        '<meta name="referrer" content="no-referrer"><title>Open AgentNet console</title></head>'
        '<body><main><h1>Open AgentNet administration</h1>'
        '<p>Continue to the configured private console. This page can be used once.</p>'
        f'<form method="post" action="{action}">'
        f'<input type="hidden" name="handoff_token" value="{token}">'
        '<button type="submit">Continue securely</button></form></main></body></html>'
    )
    _serve_one_shot_loopback_page(
        document=document,
        open_browser=open_browser,
        timeout_seconds=timeout_seconds,
    )


def command_console_open(args: argparse.Namespace) -> int:
    if not 1.0 <= args.handoff_timeout <= 60.0:
        raise SystemExit("console browser handoff timeout must be between 1 and 60 seconds")
    client, actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        begun = _console_json_response(
            client.request(
                "POST",
                "/v1/console/session-challenges",
                json_body={"schema": "agentnet.console.session-challenge-begin.v1"},
            ),
            label="console challenge",
        )
        transaction = begun.get("transaction")
        challenge_id = begun.get("challenge_id")
        transaction_digest = begun.get("transaction_digest")
        expires_at = begun.get("expires_at")
        if (
            set(begun)
            != {
                "schema",
                "challenge_id",
                "transaction",
                "transaction_digest",
                "expires_at",
                "console_origin",
            }
            or begun.get("schema") != "agentnet.console.session-challenge-result.v1"
            or not isinstance(transaction, dict)
            or set(transaction)
            != {
                "schema",
                "challenge_id",
                "audience",
                "domain_id",
                "principal_id",
                "harness_id",
                "credential_id",
                "credential_epoch",
                "binding_assurance",
                "nonce",
                "issued_at",
                "expires_at",
            }
            or transaction.get("schema") != "agentnet.console.session-challenge.v1"
            or not isinstance(challenge_id, str)
            or transaction.get("challenge_id") != challenge_id
            or transaction.get("domain_id") != actor.domain_id
            or transaction.get("principal_id") != actor.principal_id
            or transaction.get("harness_id") != actor.harness_id
            or transaction.get("credential_id") != actor.credential_id
            or transaction.get("credential_epoch") != actor.credential_epoch
            or transaction.get("binding_assurance") != actor.binding_assurance
            or not isinstance(transaction_digest, str)
            or canonical_digest(transaction) != transaction_digest
            or type(expires_at) is not int
            or transaction.get("expires_at") != expires_at
            or expires_at <= int(time.time())
        ):
            raise SystemExit("console challenge response is invalid")
        console_origin = _canonical_console_origin(begun["console_origin"])
        completed = _console_json_response(
            client.request(
                "POST",
                f"/v1/console/session-challenges/{challenge_id}/complete",
                json_body={"transaction_digest": transaction_digest},
            ),
            label="console challenge completion",
        )
    finally:
        client.close()
    if (
        set(completed) != {"schema", "handoff_token", "expires_at"}
        or completed.get("schema") != "agentnet.console.session-handoff.v1"
        or type(completed.get("expires_at")) is not int
        or int(completed["expires_at"]) > expires_at
        or int(completed["expires_at"]) <= int(time.time())
    ):
        raise SystemExit("console handoff response is invalid")
    _open_console_handoff_page(
        console_origin=console_origin,
        handoff_token=str(completed.get("handoff_token")),
        timeout_seconds=args.handoff_timeout,
    )
    print(
        json.dumps(
            {
                "status": "browser_handoff_opened",
                "console_origin": console_origin,
            },
            sort_keys=True,
        )
    )
    return 0


def command_console_serve(args: argparse.Namespace) -> int:
    config = helpers._load_config(Path(args.config))
    config.require_feature("admin_console")
    try:
        bind_address = ipaddress.ip_address(args.host)
    except ValueError as exc:
        raise GateBlocked(
            "remote_plaintext_bind",
            "console serve requires an explicit loopback bind behind its HTTPS origin",
        ) from exc
    if not bind_address.is_loopback:
        raise GateBlocked(
            "remote_plaintext_bind",
            "console serve refuses a remotely reachable plaintext bind",
        )
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()
        uvicorn.run(
            create_console_app(core),
            host=args.host,
            port=args.port,
            log_level=args.log_level,
        )
    finally:
        core.close()
    return 0


def command_supervisor_run(args: argparse.Namespace) -> int:
    """Validate or run one persistent ordinary-harness supervisor."""

    try:
        config = load_supervisor_config(Path(args.config))
    except ValidationError as exc:
        raise SystemExit(str(exc)) from None
    if args.check:
        status = redacted_supervisor_status(config)
        print(json.dumps(status, indent=2, sort_keys=True))
        return 0
    print(json.dumps(run_supervisor_daemon(config), indent=2, sort_keys=True))
    return 0


def command_manager_run(args: argparse.Namespace) -> int:
    try:
        command = validate_manager_command(tuple(args.manager_command))
        manager_extension = resolve_packaged_manager_extension()
    except (GateBlocked, ValidationError) as exc:
        raise SystemExit(str(exc)) from None
    identity_path = Path(args.identity).absolute()
    client, _, _ = helpers._load_identity_client(identity_path)

    def current_signing_context() -> VerifiedActor:
        _profile, actor, _current_key = helpers._load_identity_profile(identity_path)
        return actor

    try:
        return int(
            run_manager_gateway(
                client,
                current_signing_context,
                command,
                state_dir=Path(args.state_dir) if args.state_dir is not None else None,
                manager_extension=manager_extension,
            )
        )
    finally:
        client.close()


class _UnavailableGuidedClientEnrollment:
    """Fail closed when a Core has no configured guided enrollment adapter."""

    @staticmethod
    def _deny() -> EnrollmentProgress:
        raise ClientSetupError(
            "fresh client setup requires the configured guided OIDC/passkey enrollment service"
        )

    def begin(
        self,
        *,
        replace_expired_continuation: str | None = None,
    ) -> EnrollmentProgress:
        del replace_expired_continuation
        return self._deny()

    def status(self, *, continuation: str) -> EnrollmentProgress:
        del continuation
        return self._deny()

    def continue_setup(self, *, continuation: str) -> EnrollmentProgress:
        del continuation
        return self._deny()


def _client_setup_identity_profiles(args: argparse.Namespace) -> tuple[ClientIdentityProfile, ...]:
    profiles: list[ClientIdentityProfile] = []
    identity_paths = args.identity or [str(Path.home() / ".agentnet" / "identity.json")]
    for raw_path in identity_paths:
        path = Path(raw_path).expanduser().absolute()
        if not os.path.lexists(path):
            continue
        _value, actor, _key = helpers._load_identity_profile(path)
        profiles.append(
            ClientIdentityProfile(
                actor=actor,
                harness_kind=args.harness_kind,
                profile_key=args.profile_key,
            )
        )
    return tuple(profiles)


def _build_client_setup_coordinator(args: argparse.Namespace) -> ClientSetupCoordinator:
    """Compose setup against one package-owned Core without starting services."""

    config = helpers._load_config(Path(args.config).expanduser().absolute())
    try:
        core = CommunicationCore.open(config)
    except Exception as exc:
        raise SystemExit(f"AgentNet setup Core is unavailable: {type(exc).__name__}") from exc
    try:
        lifecycle = getattr(core, "endpoint_lifecycle", None)
        if lifecycle is None:
            raise SystemExit("AgentNet endpoint lifecycle is unavailable")
        enrollment = getattr(core, "client_setup_enrollment", None)
        if enrollment is None:
            enrollment = _UnavailableGuidedClientEnrollment()
        return ClientSetupCoordinator(
            endpoint_lifecycle=lifecycle,
            identity_profiles=lambda: _client_setup_identity_profiles(args),
            enrollment=enrollment,
            continuation_store=ClientSetupContinuationStore(
                Path(args.state).expanduser().absolute()
            ),
            harness_kind=args.harness_kind,
            profile_key=args.profile_key,
            close=core.close,
        )
    except BaseException:
        core.close()
        raise


def _print_client_setup_result(result: ClientSetupResult) -> None:
    print(json.dumps(result.model_dump(mode="json"), indent=2, sort_keys=True))
    if result.next_action is SetupNextAction.RESTART_YOUR_AGENT:
        print("Restart your agent to enable AgentNet")


def _run_client_setup(
    args: argparse.Namespace,
    operation: str,
) -> int:
    try:
        coordinator = _build_client_setup_coordinator(args)
    except (ClientSetupError, ValueError) as exc:
        raise SystemExit(str(exc)) from None
    try:
        if operation == "setup":
            result = coordinator.setup()
        elif operation == "status":
            result = coordinator.status()
        elif operation == "continue":
            result = coordinator.continue_setup()
        else:
            raise AssertionError("unknown client setup operation")
    except ClientSetupError as exc:
        raise SystemExit(str(exc)) from None
    finally:
        coordinator.close()
    _print_client_setup_result(result)
    return 0


def command_client_setup(args: argparse.Namespace) -> int:
    """Begin or resume package-owned user-level AgentNet setup."""

    return _run_client_setup(args, "setup")


def command_client_setup_status(args: argparse.Namespace) -> int:
    """Report setup status without restarting or signaling the harness."""

    return _run_client_setup(args, "status")


def command_client_setup_continue(args: argparse.Namespace) -> int:
    """Continue setup while leaving an explicit user restart pending."""

    return _run_client_setup(args, "continue")


def _configure_client_setup_arguments(
    parser: argparse.ArgumentParser,
    *,
    defaults: bool = True,
) -> None:
    private_root = Path.home() / ".agentnet"
    suppressed = argparse.SUPPRESS
    parser.add_argument(
        "--config",
        default=str(private_root / "agentnet.json") if defaults else suppressed,
    )
    parser.add_argument(
        "--identity",
        action="append",
        default=[] if defaults else suppressed,
        help="repeat for exact current identity profiles; ambiguity is denied",
    )
    parser.add_argument(
        "--state",
        default=str(private_root / "setup-continuation.json") if defaults else suppressed,
    )
    parser.add_argument(
        "--harness-kind",
        choices=("omp", "pi", "claude", "codex", "antigravity", "server"),
        default=(
            os.environ.get("AGENTNET_HARNESS_KIND", "omp") if defaults else suppressed
        ),
    )
    parser.add_argument(
        "--profile-key",
        default=(
            os.environ.get("AGENTNET_PROFILE_KEY", "default") if defaults else suppressed
        ),
    )

__all__ = (
    "command_serve",
    "command_console_open",
    "command_console_serve",
    "command_supervisor_run",
    "command_manager_run",
    "command_client_setup",
    "command_client_setup_status",
    "command_client_setup_continue",
)
