#!/usr/bin/env python3
"""Package-installed, separate-process local communication conformance gate.

This proves only synthetic local-conformance behavior. It does not prove
production enrollment, the bounded C0 pilot, approved revocation, PostgreSQL
durability, or ordinary server-agent operation.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import signal
import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path
from typing import Any

import agentnet
from agentnet.adapters.windows_job import WindowsJobGuard
from agentnet.client import AgentNetClient
from agentnet.core.app import CommunicationCore
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.config import ExtensionConfig, RuntimeProfile
from agentnet.security.signatures import P256KeyPair


CONVERSATION_ID = "conversation:packaged-local-conformance"
THREAD_ID = "thread:packaged-local-conformance"
REQUEST_KEY = "packaged-local-obligation-request-0001"
RESPONSE_KEY = "packaged-local-obligation-response-0001"


def _private_write(path: Path, value: bytes | str | dict[str, Any]) -> None:
    if isinstance(value, dict):
        content = json.dumps(value, sort_keys=True) + "\n"
    else:
        content = value
    if isinstance(content, str):
        path.write_text(content, encoding="utf-8")
    else:
        path.write_bytes(content)
    if os.name != "nt":
        path.chmod(0o600)


def _assert_installed(package_root: Path) -> None:
    package_root = package_root.resolve()
    module_path = Path(agentnet.__file__).resolve()
    if not module_path.is_relative_to(package_root):
        raise RuntimeError("AgentNet module did not resolve from the installed package")
    if Path(__file__).resolve() != package_root / "scripts" / "ci" / Path(__file__).name:
        raise RuntimeError("communication gate did not execute from the installed package")


def _load_config(path: Path) -> ExtensionConfig:
    return ExtensionConfig.model_validate_json(path.read_text(encoding="utf-8"), strict=True)


def _identity_client(path: Path) -> tuple[AgentNetClient, VerifiedActor]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if value.get("schema") != "agentnet.identity-profile.v1":
        raise RuntimeError("local identity profile schema is invalid")
    actor = VerifiedActor.model_validate(value["actor"])
    key = P256KeyPair.from_private_pem(Path(value["private_key_path"]).read_bytes())
    return (
        AgentNetClient(
            base_url=value["server_base_url"],
            key=key,
            domain_id=actor.domain_id,
            harness_id=actor.harness_id or "",
            credential_id=actor.credential_id or "",
            audience=value["audience"],
        ),
        actor,
    )


def _client_request(args: argparse.Namespace) -> int:
    client: AgentNetClient | None = None
    try:
        package_root = Path(args.package_root)
        _assert_installed(package_root)
        request = json.loads(Path(args.request).read_text(encoding="utf-8"))
        identity_path = Path(request["identity"])
        client, _actor = _identity_client(identity_path)
        response = client.request(
            request["method"],
            request["path"],
            json_body=request.get("json_body"),
        )
        try:
            body = response.json()
        except Exception as exc:
            raise RuntimeError("signed request returned a non-JSON response") from exc
        _private_write(Path(args.response), {"status": response.status_code, "body": body})
        return 0
    except Exception as exc:
        _private_write(
            Path(args.diagnostic),
            {
                "schema": "agentnet.packaged-local-client-diagnostic.v1",
                "error_type": type(exc).__name__,
            },
        )
        return 1
    finally:
        if client is not None:
            client.close()


def _seed(config_path: Path, origin: str, root: Path) -> dict[str, Any]:
    config = _load_config(config_path)
    if config.profile is not RuntimeProfile.LOCAL_CONFORMANCE:
        raise RuntimeError("local communication seed requires the local-conformance profile")
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()
        actors: list[dict[str, Any]] = []
        for harness_kind, name in (("pi", "laptop"), ("codex", "server-recipient")):
            actor, key = core.bootstrap_synthetic_identity(
                harness_kind=harness_kind,
                display_name=f"packaged-local-{name}",
            )
            if (
                actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
                or actor.binding_assurance != "lab"
            ):
                raise RuntimeError("synthetic identity escaped the local lab boundary")
            key_path = root / f"{name}.key.pem"
            identity_path = root / f"{name}.identity.json"
            _private_write(key_path, key.private_pem)
            _private_write(
                identity_path,
                {
                    "schema": "agentnet.identity-profile.v1",
                    "server_base_url": origin,
                    "audience": config.effective_service_audience,
                    "actor": actor.model_dump(mode="json"),
                    "private_key_path": str(key_path),
                },
            )
            for action in (
                "conversation.create",
                "conversation.message.send",
                "conversation.response_obligation.create",
                "conversation.response_obligation.respond",
                "conversation.response_obligation.read",
                "conversation.response_obligation.transition",
                "mailbox.acknowledge",
                "mailbox.read",
            ):
                core.grant_local_entitlement(actor, action=action, resource="*")
            actors.append(
                {
                    "name": name,
                    "actor": actor.model_dump(mode="json"),
                    "identity": str(identity_path),
                }
            )
        return {"actors": actors, "non_production": True}
    finally:
        core.close()


def _fixture_disable_credential(config_path: Path, actor_value: dict[str, Any]) -> None:
    """Establish a lab refusal fixture; this is not an approved revocation flow."""

    config = _load_config(config_path)
    actor = VerifiedActor.model_validate(actor_value)
    if (
        config.profile is not RuntimeProfile.LOCAL_CONFORMANCE
        or actor.kind is not ActorKind.VERIFIED_HUMAN_HARNESS
        or actor.binding_assurance != "lab"
        or not actor.credential_id
        or not actor.harness_id
    ):
        raise RuntimeError("credential refusal fixture is restricted to an exact lab actor")
    core = CommunicationCore.open(config)
    try:
        with core.store.transaction(immediate=True) as connection:
            row = connection.execute(
                """SELECT c.status AS credential_status,h.status AS harness_status,
                          h.binding_assurance,h.domain_id
                     FROM credentials c JOIN harnesses h ON h.harness_id=c.harness_id
                    WHERE c.credential_id=? AND c.harness_id=?""",
                (actor.credential_id, actor.harness_id),
            ).fetchone()
            if (
                row is None
                or row["credential_status"] != "active"
                or row["harness_status"] != "deterministic_only"
                or row["binding_assurance"] != "lab"
                or row["domain_id"] != actor.domain_id
            ):
                raise RuntimeError("credential refusal fixture actor state is not exact")
            changed = connection.execute(
                "UPDATE credentials SET status='revoked' WHERE credential_id=? AND status='active'",
                (actor.credential_id,),
            )
            if changed.rowcount != 1:
                raise RuntimeError("credential refusal fixture did not change exactly one row")
            core.store.append_audit(
                connection,
                {
                    "action": "local_conformance_credential_refusal_fixture",
                    "credential_id": actor.credential_id,
                    "harness_id": actor.harness_id,
                    "non_production": True,
                    "approved_revocation": False,
                },
            )
    finally:
        core.close()


class _CoreProcess:
    def __init__(self, launcher: Path, config: Path, port: int, root: Path, environment: dict[str, str]):
        self.launcher = launcher
        self.config = config
        self.port = port
        self.root = root
        self.environment = environment
        self.process: subprocess.Popen[bytes] | None = None
        self.windows_job: WindowsJobGuard | None = None
        self.stdout = None
        self.stderr = None

    def start(self) -> int:
        self.stdout = (self.root / "core.stdout").open("ab")
        self.stderr = (self.root / "core.stderr").open("ab")
        kwargs: dict[str, Any] = {}
        if os.name == "nt":
            self.windows_job = WindowsJobGuard()
            kwargs["creationflags"] = self.windows_job.creation_flags()
        else:
            kwargs["start_new_session"] = True
        self.process = subprocess.Popen(
            [
                str(self.launcher),
                "serve",
                "--config",
                str(self.config),
                "--host",
                "127.0.0.1",
                "--port",
                str(self.port),
                "--log-level",
                "warning",
            ],
            cwd=self.root,
            env=self.environment,
            stdin=subprocess.DEVNULL,
            stdout=self.stdout,
            stderr=self.stderr,
            **kwargs,
        )
        if self.windows_job is not None:
            try:
                self.windows_job.assign_and_resume(self.process)
            except Exception:
                self.windows_job.close()
                self.process.wait(timeout=5)
                raise
        deadline = time.monotonic() + 30
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(
                    f"packaged Core stopped before loopback readiness ({self.diagnostic()})"
                )
            try:
                with opener.open(f"http://127.0.0.1:{self.port}/healthz", timeout=0.5) as response:
                    body = json.loads(response.read(65537))
                    if (
                        response.status == 200
                        and body.get("schema") == "agentnet.core.health.v1"
                        and body.get("service") == "agentnet-core"
                        and body.get("profile") == "local_conformance"
                    ):
                        return self.process.pid
            except Exception:
                time.sleep(0.05)
        raise RuntimeError(
            f"packaged Core did not become ready within the bounded interval ({self.diagnostic()})"
        )

    def diagnostic(self) -> str:
        parts = [f"core_exit={self.process.poll() if self.process is not None else 'not_started'}"]
        for stream in (self.stdout, self.stderr):
            if stream is not None and not stream.closed:
                stream.flush()
        for name in ("core.stdout", "core.stderr"):
            target = self.root / name
            data = target.read_bytes() if target.is_file() else b""
            parts.append(f"{name}_bytes={len(data)}")
            parts.append(f"{name}_sha256={hashlib.sha256(data).hexdigest()[:16]}")
        return " ".join(parts)

    def stop(self) -> None:
        process = self.process
        if process is None:
            return
        if os.name == "nt":
            if self.windows_job is None:
                raise RuntimeError("packaged Core Windows Job Object custody is missing")
            self.windows_job.stop(process, timeout_seconds=10)
            self.windows_job = None
        elif process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
        if process.poll() is None:
            raise RuntimeError(
                f"packaged Core process survived cleanup ({self.diagnostic()})"
            )
        self.process = None
        if self.windows_job is not None:
            self.windows_job.close()
            self.windows_job = None
        if self.stdout is not None:
            self.stdout.close()
        if self.stderr is not None:
            self.stderr.close()


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as value:
        value.bind(("127.0.0.1", 0))
        return int(value.getsockname()[1])


def _json_file(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def _run_client(
    *,
    package_root: Path,
    root: Path,
    environment: dict[str, str],
    identity: str,
    method: str,
    path: str,
    json_body: dict[str, Any] | None = None,
) -> dict[str, Any]:
    sequence = time.monotonic_ns()
    request_path = root / f"request-{sequence}.json"
    response_path = root / f"response-{sequence}.json"
    diagnostic_path = root / f"diagnostic-{sequence}.json"
    _private_write(
        request_path,
        {
            "identity": identity,
            "method": method,
            "path": path,
            **({"json_body": json_body} if json_body is not None else {}),
        },
    )
    completed = subprocess.run(
        [
            sys.executable,
            "-B",
            "-I",
            str(Path(__file__).resolve()),
            "client",
            "--package-root",
            str(package_root),
            "--request",
            str(request_path),
            "--response",
            str(response_path),
            "--diagnostic",
            str(diagnostic_path),
        ],
        cwd=root,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=30,
        check=False,
    )
    request_path.unlink(missing_ok=True)
    if completed.returncode != 0 or not response_path.is_file():
        diagnostic: dict[str, Any] = {}
        if diagnostic_path.is_file():
            diagnostic = _json_file(diagnostic_path)
        error_type = diagnostic.get("error_type")
        if (
            diagnostic.get("schema") != "agentnet.packaged-local-client-diagnostic.v1"
            or not isinstance(error_type, str)
            or re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,63}", error_type) is None
        ):
            error_type = "missing_or_invalid_diagnostic"
        response_path.unlink(missing_ok=True)
        diagnostic_path.unlink(missing_ok=True)
        raise RuntimeError(
            f"packaged signed client subprocess failed: exit={completed.returncode} "
            f"error_type={error_type}"
        )
    result = _json_file(response_path)
    response_path.unlink(missing_ok=True)
    diagnostic_path.unlink(missing_ok=True)
    return result


def _expect(result: dict[str, Any], status: int) -> dict[str, Any]:
    if result.get("status") != status or not isinstance(result.get("body"), dict):
        raise RuntimeError("packaged signed request returned an unexpected status")
    return result["body"]


def _run(args: argparse.Namespace) -> int:
    package_root = Path(args.package_root).resolve()
    launcher = Path(args.launcher).resolve()
    root = Path(args.workspace).resolve() / "packaged-local-communication"
    _assert_installed(package_root)
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    environment = dict(os.environ)
    for name in ("PYTHONHOME", "PYTHONPATH", "VIRTUAL_ENV"):
        environment.pop(name, None)
    environment.update(
        {
            "HOME": str(root / "home"),
            "NO_PROXY": "127.0.0.1,localhost",
            "no_proxy": "127.0.0.1,localhost",
            "PYTHONDONTWRITEBYTECODE": "1",
        }
    )
    for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
        environment.pop(name, None)
    (root / "home").mkdir(mode=0o700)
    port = _free_port()
    origin = f"http://127.0.0.1:{port}"
    config_path = root / "agentnet.json"
    data_dir = root / "state"
    core_process = _CoreProcess(launcher, config_path, port, root, environment)
    process_ids: list[int] = []
    try:
        initialized = subprocess.run(
            [
                str(launcher),
                "init",
                "--config",
                str(config_path),
                "--data-dir",
                str(data_dir),
                "--domain",
                "packaged-local.example",
                "--public-base-url",
                origin,
            ],
            cwd=root,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=60,
            check=False,
        )
        if initialized.returncode != 0:
            raise RuntimeError("packaged local initialization failed")
        seed = _seed(config_path, origin, root)
        laptop = seed["actors"][0]
        recipient = seed["actors"][1]
        laptop_actor = VerifiedActor.model_validate(laptop["actor"])
        recipient_actor = VerifiedActor.model_validate(recipient["actor"])

        process_ids.append(core_process.start())
        created = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="POST",
                path="/v1/conversations",
                json_body={
                    "conversation_id": CONVERSATION_ID,
                    "member_harness_ids": [recipient_actor.harness_id],
                    "classification": "C0",
                },
            ),
            201,
        )
        if created.get("duplicate") is not False:
            raise RuntimeError("conversation creation did not create one new local conversation")

        action_path = f"/v1/conversations/{CONVERSATION_ID}/actions"
        request_body = {
            "recipients": [recipient_actor.harness_id],
            "thread_id": THREAD_ID,
            "action": {
                "kind": "post",
                "body": "synthetic packaged local request",
                "response_obligation": {},
            },
            "idempotency_key": REQUEST_KEY,
        }
        requested = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="POST",
                path=action_path,
                json_body=request_body,
            ),
            202,
        )
        retried = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="POST",
                path=action_path,
                json_body=request_body,
            ),
            202,
        )
        obligation = requested.get("response_obligation") or {}
        retry_obligation = retried.get("response_obligation") or {}
        if (
            requested.get("fact") != "accepted_local"
            or requested.get("duplicate") is not False
            or retried.get("duplicate") is not True
            or retried.get("event_id") != requested.get("event_id")
            or retry_obligation.get("obligation_id") != obligation.get("obligation_id")
            or obligation.get("state") != "created"
        ):
            raise RuntimeError("conversation request idempotency or local custody evidence failed")

        first_pid = process_ids[-1]
        core_process.stop()
        process_ids.append(core_process.start())
        if process_ids[-1] == first_pid:
            raise RuntimeError("Core restart did not produce a distinct process")

        mailbox = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="GET",
                path="/v1/mailbox",
            ),
            200,
        )
        request_items = [
            item
            for item in mailbox.get("items", [])
            if item.get("event", {}).get("event_id") == requested.get("event_id")
        ]
        if len(request_items) != 1:
            raise RuntimeError("offline recipient did not recover the exact request after restart")
        if request_items[0].get("event", {}).get("actor") != laptop_actor.model_dump(mode="json"):
            raise RuntimeError("request event lost exact proof-derived sender attribution")

        ack_path = f"/v1/mailbox/{requested['event_id']}/acknowledge"
        ack_body = {"envelope_digest": requested["envelope_digest"]}
        acknowledged = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="POST",
                path=ack_path,
                json_body=ack_body,
            ),
            200,
        )
        acknowledged_retry = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="POST",
                path=ack_path,
                json_body=ack_body,
            ),
            200,
        )
        if (
            acknowledged.get("fact") != "recipient_committed"
            or acknowledged.get("duplicate") is not False
            or acknowledged_retry.get("duplicate") is not True
            or acknowledged_retry.get("receipt_id") != acknowledged.get("receipt_id")
        ):
            raise RuntimeError("exact recipient custody acknowledgement did not converge")

        _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="POST",
                path="/v1/response-obligations/reconcile",
                json_body={"limit": 100},
            ),
            200,
        )
        obligation_id = obligation["obligation_id"]
        obligation_path = f"/v1/response-obligations/{obligation_id}"
        shown = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="GET",
                path=obligation_path,
            ),
            200,
        )
        if shown.get("state") != "recipient_committed":
            raise RuntimeError("response obligation did not mirror exact recipient custody")
        progressed = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="POST",
                path=f"{obligation_path}/transition",
                json_body={"to_state": "acknowledged"},
            ),
            200,
        )
        if progressed.get("state") != "acknowledged":
            raise RuntimeError("responsible harness did not own obligation progress")

        response_body = {
            "recipients": [laptop_actor.harness_id],
            "thread_id": THREAD_ID,
            "action": {
                "kind": "obligation_response",
                "obligation_id": obligation_id,
                "request_event_id": requested["event_id"],
                "request_digest": shown["request_payload_digest"],
                "outcome": "completed",
                "body": "synthetic packaged local response",
            },
            "idempotency_key": RESPONSE_KEY,
        }
        completed = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=recipient["identity"],
                method="POST",
                path=action_path,
                json_body=response_body,
            ),
            202,
        )
        if (completed.get("response_obligation") or {}).get("state") != "completed":
            raise RuntimeError("typed response did not atomically complete the obligation")

        core_process.stop()
        process_ids.append(core_process.start())
        reply_mailbox = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="GET",
                path="/v1/mailbox",
            ),
            200,
        )
        reply_items = [
            item
            for item in reply_mailbox.get("items", [])
            if item.get("event", {}).get("event_id") == completed.get("event_id")
        ]
        if len(reply_items) != 1:
            raise RuntimeError("requester did not recover the exact response after restart")
        if reply_items[0].get("event", {}).get("actor") != recipient_actor.model_dump(mode="json"):
            raise RuntimeError("response event lost exact proof-derived sender attribution")
        reply_ack = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="POST",
                path=f"/v1/mailbox/{completed['event_id']}/acknowledge",
                json_body={"envelope_digest": completed["envelope_digest"]},
            ),
            200,
        )
        terminal = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="GET",
                path=obligation_path,
            ),
            200,
        )
        listed = _expect(
            _run_client(
                package_root=package_root,
                root=root,
                environment=environment,
                identity=laptop["identity"],
                method="GET",
                path="/v1/response-obligations?role=requester&limit=10",
            ),
            200,
        )
        if (
            reply_ack.get("fact") != "recipient_committed"
            or terminal.get("state") != "completed"
            or terminal.get("response_event_id") != completed.get("event_id")
            or len(listed.get("items", [])) != 1
            or listed["items"][0].get("state") != "completed"
        ):
            raise RuntimeError("terminal response custody or obligation linkage failed")

        core_process.stop()
        _fixture_disable_credential(config_path, laptop["actor"])
        process_ids.append(core_process.start())
        refused = _run_client(
            package_root=package_root,
            root=root,
            environment=environment,
            identity=laptop["identity"],
            method="GET",
            path="/v1/mailbox",
        )
        if (
            refused.get("status") != 401
            or (refused.get("body") or {}).get("code") != "authentication_failed"
        ):
            raise RuntimeError("fresh proof from fixture-disabled lab credential was not refused")
        core_process.stop()

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as value:
            value.settimeout(0.5)
            if value.connect_ex(("127.0.0.1", port)) == 0:
                raise RuntimeError("packaged Core listener survived cleanup")
        if len(process_ids) != 4 or len(set(process_ids)) != 4:
            raise RuntimeError("four distinct Core process starts were not observed")
        print(
            json.dumps(
                {
                    "accepted_fact": "accepted_local",
                    "approved_revocation_proven": False,
                    "bounded_c0_pilot_proven": False,
                    "core_process_starts": 4,
                    "core_restarts": 3,
                    "credential_refusal_fixture": True,
                    "exact_attribution": True,
                    "idempotency": True,
                    "non_production": True,
                    "obligation_state": "completed",
                    "production_durability_proven": False,
                    "recipient_fact": "recipient_committed",
                    "release_certified": False,
                    "separate_process_loopback": True,
                },
                sort_keys=True,
            )
        )
        return 0
    finally:
        core_process.stop()
        if root.exists():
            import shutil

            shutil.rmtree(root)
        if root.exists():
            raise RuntimeError("packaged local communication state survived cleanup")


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    run = subparsers.add_parser("run")
    run.add_argument("--package-root", required=True)
    run.add_argument("--launcher", required=True)
    run.add_argument("--workspace", required=True)
    client = subparsers.add_parser("client")
    client.add_argument("--package-root", required=True)
    client.add_argument("--request", required=True)
    client.add_argument("--response", required=True)
    client.add_argument("--diagnostic", required=True)
    return parser


def main() -> int:
    args = _parser().parse_args()
    if args.command == "client":
        return _client_request(args)
    return _run(args)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        message = str(exc)
        message = re.sub(r"(?:[A-Za-z]:)?[/\\][^\s,;()]+", "<path>", message)
        message = re.sub(r"\b[A-Za-z0-9_-]{24,}\b", "<opaque>", message)
        print(
            json.dumps(
                {
                    "error_type": type(exc).__name__,
                    "message": message[:1024],
                    "status": "failed",
                },
                sort_keys=True,
            ),
            file=sys.stderr,
        )
        raise SystemExit(1)
