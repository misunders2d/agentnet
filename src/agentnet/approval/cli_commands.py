"""Owner-operated CLI commands for the independent approval service."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import secrets
import shutil
import stat
import tempfile
import time
import webbrowser
from pathlib import Path
from typing import Any, Literal
from urllib.parse import urlsplit

import uvicorn
from pydantic import BaseModel, ConfigDict, Field

from agentnet.approval.config import (
    ApprovalServiceApproverConfig,
    ApprovalServiceConfig,
    load_approval_service_config,
    require_owner_only_file,
)
from agentnet.approval.http import create_approval_app
from agentnet.approval.store import ApprovalStore
from agentnet.approval.webauthn_uv import WebAuthnApprovalService
from agentnet.errors import ValidationError
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import P256KeyPair, canonical_json


class _ProvisionApprover(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    principal_id: str = Field(min_length=1, max_length=256)
    authority_kind: Literal["human", "guest"] = "human"
    domain_id: str = Field(pattern=r"^[a-z0-9][a-z0-9.-]{2,127}$")
    allowed_purposes: frozenset[str] = Field(min_length=1, max_length=32)


def _reject_duplicates(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _reject_nonfinite(_value: str) -> object:
    raise ValueError("non-finite JSON number")


def _strict_json_object(raw: bytes, *, label: str) -> dict[str, Any]:
    try:
        value = json.loads(
            raw.decode("utf-8"),
            object_pairs_hook=_reject_duplicates,
            parse_constant=_reject_nonfinite,
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise SystemExit(f"{label} is invalid") from exc
    if not isinstance(value, dict):
        raise SystemExit(f"{label} must be one JSON object")
    return value


def _atomic_private_write(path: Path, payload: bytes, *, replace: bool = False) -> None:
    path = path.absolute()
    if path.is_symlink() or path.parent.is_symlink():
        raise SystemExit(f"refusing symlink output: {path}")
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    parent = path.parent.stat()
    if parent.st_uid != os.geteuid() or parent.st_mode & 0o077:
        raise SystemExit(f"private output directory must be owner-only: {path.parent}")
    if path.exists() and not replace:
        raise SystemExit(f"refusing to overwrite existing file: {path}")
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        if path.exists() and path.is_symlink():
            raise SystemExit(f"refusing symlink output: {path}")
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise


def _open_service(config_path: Path) -> tuple[ApprovalServiceConfig, ApprovalStore, WebAuthnApprovalService]:
    config = load_approval_service_config(config_path.absolute())
    cipher = LocalEnvelopeCipher.from_key_file(config.record_key_path, create=False)
    store = ApprovalStore(config.database_path, cipher)
    return config, store, WebAuthnApprovalService(config, store, cipher)


def command_approval_provision(args: argparse.Namespace) -> int:
    config_path = Path(args.config).absolute()
    data_dir = Path(args.data_dir).absolute()
    approvers_path = Path(args.approvers).absolute()
    if config_path.exists():
        raise SystemExit("approval provision refuses to overwrite an existing configuration")
    if data_dir.exists() and any(data_dir.iterdir()):
        raise SystemExit("approval provision requires an absent or empty data directory")
    raw = require_owner_only_file(
        approvers_path,
        label="approval provision approver specification",
        max_bytes=262_144,
    )
    value = _strict_json_object(raw, label="approval approver specification")
    if set(value) != {"approvers"} or not isinstance(value["approvers"], list):
        raise SystemExit("approval approver specification must contain only an approvers array")
    try:
        provision_approvers = tuple(
            _ProvisionApprover.model_validate(item) for item in value["approvers"]
        )
    except Exception as exc:
        raise SystemExit("approval approver specification is invalid") from exc
    if not provision_approvers:
        raise SystemExit("approval provision requires at least one approver")

    parent = data_dir.parent
    parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=f".{data_dir.name}.provision-", dir=parent))
    os.chmod(staging, 0o700)
    moved = False
    try:
        secrets_dir = staging / "secrets"
        signers_dir = staging / "signers"
        secrets_dir.mkdir(mode=0o700)
        signers_dir.mkdir(mode=0o700)
        record_key = secrets.token_bytes(32)
        _atomic_private_write(secrets_dir / "records.key", record_key)
        configured: list[ApprovalServiceApproverConfig] = []
        trust: list[dict[str, Any]] = []
        for index, item in enumerate(provision_approvers):
            signer = P256KeyPair.generate()
            filename = f"approver-{index + 1}.pem"
            _atomic_private_write(signers_dir / filename, signer.private_pem)
            final_signer_path = data_dir / "signers" / filename
            configured.append(
                ApprovalServiceApproverConfig(
                    principal_id=item.principal_id,
                    authority_kind=item.authority_kind,
                    domain_id=item.domain_id,
                    signer_key_id=signer.thumbprint,
                    signer_private_key_path=final_signer_path,
                    allowed_purposes=item.allowed_purposes,
                )
            )
            trust.append(
                {
                    "principal_id": item.principal_id,
                    "authority_kind": item.authority_kind,
                    "signer_key_id": signer.thumbprint,
                    "public_key_pem": signer.public_pem,
                    "allowed_purposes": sorted(item.allowed_purposes),
                }
            )
        config = ApprovalServiceConfig(
            public_origin=args.public_origin,
            rp_id=args.rp_id,
            rp_name=args.rp_name,
            verifier_id=args.verifier_id,
            data_dir=data_dir,
            database_path=data_dir / "approval.sqlite3",
            record_key_path=data_dir / "secrets" / "records.key",
            internal_core_credential_env=args.internal_core_credential_env,
            approvers=tuple(configured),
        )
        database = staging / "approval.sqlite3"
        descriptor = os.open(database, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.close(descriptor)
        cipher = LocalEnvelopeCipher(record_key)
        store = ApprovalStore(database, cipher, initialize=True)
        store.close()
        if data_dir.exists():
            data_dir.rmdir()
        os.replace(staging, data_dir)
        moved = True
        _atomic_private_write(
            config_path,
            json.dumps(config.model_dump(mode="json"), indent=2, sort_keys=True).encode("utf-8") + b"\n",
        )
    except BaseException:
        shutil.rmtree(data_dir if moved else staging, ignore_errors=True)
        raise
    print(
        json.dumps(
            {
                "schema": "agentnet.approval.provision-result.v1",
                "provisioned": True,
                "config": str(config_path),
                "data_dir": str(data_dir),
                "core_trust": {
                    "verifier_id": config.verifier_id,
                    "trusted_approvers": trust,
                },
                "authority_granted": False,
                "webauthn_registered": False,
                "evidence_claim": "single_host_local_only",
            },
            sort_keys=True,
        )
    )
    return 0


def command_approval_serve(args: argparse.Namespace) -> int:
    try:
        address = ipaddress.ip_address(args.host)
    except ValueError as exc:
        raise SystemExit("approval service host must be an explicit loopback IP") from exc
    if not address.is_loopback or not 1 <= args.port <= 65_535:
        raise SystemExit("approval service must bind an explicit loopback address and valid port")
    _config, store, service = _open_service(Path(args.config))
    try:
        uvicorn.run(
            create_approval_app(service),
            host=str(address),
            port=args.port,
            log_level=args.log_level,
            access_log=False,
        )
    finally:
        store.close()
    return 0


def command_approval_register_begin(args: argparse.Namespace) -> int:
    _config, store, service = _open_service(Path(args.config))
    try:
        created = service.begin_registration(args.approver)
    finally:
        store.close()
    print(
        json.dumps(
            {
                "schema": "agentnet.approval.registration-url.v1",
                "approval_url": created.url,
                "approver_principal_id": args.approver,
                "expires_at": created.expires_at,
            },
            sort_keys=True,
        )
    )
    return 0


def command_approval_request_create(args: argparse.Namespace) -> int:
    config, store, service = _open_service(Path(args.config))
    try:
        raw = require_owner_only_file(
            Path(args.transaction).absolute(),
            label="approval canonical transaction",
            max_bytes=config.max_transaction_bytes,
        )
        value = _strict_json_object(raw, label="approval canonical transaction")
        canonical = canonical_json(value)
        created = service.create_request(
            principal_id=args.approver,
            approval_purpose=args.purpose,
            canonical_transaction=canonical,
        )
    finally:
        store.close()
    print(
        json.dumps(
            {
                "schema": "agentnet.approval.request-url.v1",
                "approval_url": created.url,
                "request_id": created.identifier,
                "approver_principal_id": args.approver,
                "approval_purpose": args.purpose,
                "transaction_digest": created.transaction_digest,
                "expires_at": created.expires_at,
            },
            sort_keys=True,
        )
    )
    return 0


def command_approval_pending(args: argparse.Namespace) -> int:
    _config, store, service = _open_service(Path(args.config))
    try:
        requests = service.pending_requests()
    finally:
        store.close()
    print(
        json.dumps(
            {
                "schema": "agentnet.approval.pending-requests.v1",
                "requests": requests,
            },
            sort_keys=True,
        )
    )
    return 0


def command_approval_open(args: argparse.Namespace) -> int:
    _config, store, service = _open_service(Path(args.config))
    try:
        url = service.local_approval_url(args.request_id)
    finally:
        store.close()
    opened = bool(webbrowser.open(url, new=2))
    if not opened:
        raise SystemExit("approval browser could not be opened locally")
    print(
        json.dumps(
            {
                "schema": "agentnet.approval.local-open.v1",
                "request_id": args.request_id,
                "opened": True,
            },
            sort_keys=True,
        )
    )
    return 0


def command_approval_watch(args: argparse.Namespace) -> int:
    if not 0.25 <= args.interval <= 60.0:
        raise SystemExit("approval watch interval must be between 0.25 and 60 seconds")
    _config, store, service = _open_service(Path(args.config))
    observed: set[str] = set()
    try:
        while True:
            for request in service.pending_requests():
                request_id = str(request["request_id"])
                if request_id in observed:
                    continue
                opened = False
                if args.open and bool(request["openable_locally"]):
                    url = service.local_approval_url(request_id)
                    opened = bool(webbrowser.open(url, new=2))
                    if not opened:
                        raise SystemExit("approval browser could not be opened locally")
                print(
                    json.dumps(
                        {
                            "schema": "agentnet.approval.pending-observed.v1",
                            "request": request,
                            "opened": opened,
                        },
                        sort_keys=True,
                    ),
                    flush=True,
                )
                observed.add(request_id)
            if args.once:
                return 0
            time.sleep(args.interval)
    except KeyboardInterrupt:
        return 0
    finally:
        store.close()


def command_approval_credential_revoke(args: argparse.Namespace) -> int:
    _config, store, service = _open_service(Path(args.config))
    try:
        result = service.revoke_credential(
            principal_id=args.approver,
            credential_id=args.credential_id,
            reason=args.reason,
        )
    finally:
        store.close()
    print(json.dumps({"schema": "agentnet.approval.credential-revocation.v1", **result}, sort_keys=True))
    return 0


def command_approval_status(args: argparse.Namespace) -> int:
    config, store, _service = _open_service(Path(args.config))
    try:
        status = store.readiness()
    finally:
        store.close()
    print(
        json.dumps(
            {
                "schema": "agentnet.approval.status.v1",
                "verifier_id": config.verifier_id,
                "public_origin": config.public_origin,
                "rp_id": config.rp_id,
                "approver_count": len(config.approvers),
                "independent_boundary_proven": False,
                **status,
            },
            sort_keys=True,
        )
    )
    return 0


def configure_approval_parser(commands: argparse._SubParsersAction[argparse.ArgumentParser]) -> None:
    approval = commands.add_parser(
        "approval",
        help="operate a separately controlled WebAuthn-UV approval service",
    )
    sub = approval.add_subparsers(dest="approval_command", required=True)

    provision = sub.add_parser("provision", help="create an unregistered independent approval profile")
    provision.add_argument("--config", default=".agentnet-approval/config.json")
    provision.add_argument("--data-dir", required=True)
    provision.add_argument("--public-origin", required=True)
    provision.add_argument("--rp-id", required=True)
    provision.add_argument("--rp-name", default="AgentNet Approval")
    provision.add_argument("--verifier-id", required=True)
    provision.add_argument("--approvers", required=True)
    provision.add_argument("--internal-core-credential-env")
    provision.set_defaults(func=command_approval_provision)

    serve = sub.add_parser("serve", help="serve browser ceremonies behind an independent HTTPS proxy")
    serve.add_argument("--config", default=".agentnet-approval/config.json")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=8090)
    serve.add_argument("--log-level", default="info", choices=("critical", "error", "warning", "info"))
    serve.set_defaults(func=command_approval_serve)

    register = sub.add_parser("register-begin", help="create one host-admin passkey-registration URL")
    register.add_argument("--config", default=".agentnet-approval/config.json")
    register.add_argument("--approver", required=True)
    register.set_defaults(func=command_approval_register_begin)

    request = sub.add_parser("request-create", help="create one exact human approval request URL")
    request.add_argument("--config", default=".agentnet-approval/config.json")
    request.add_argument("--approver", required=True)
    request.add_argument("--purpose", required=True)
    request.add_argument("--transaction", required=True)
    request.set_defaults(func=command_approval_request_create)

    pending = sub.add_parser(
        "pending",
        help="list content-free pending approvals on this approval host",
    )
    pending.add_argument("--config", default=".agentnet-approval/config.json")
    pending.set_defaults(func=command_approval_pending)

    open_request = sub.add_parser(
        "open",
        help="open one Core-created pending approval locally without printing its capability",
    )
    open_request.add_argument("--config", default=".agentnet-approval/config.json")
    open_request.add_argument("--request-id", required=True)
    open_request.set_defaults(func=command_approval_open)

    watch = sub.add_parser(
        "watch",
        help="watch approval-host-local pending requests and optionally open them",
    )
    watch.add_argument("--config", default=".agentnet-approval/config.json")
    watch.add_argument("--interval", type=float, default=2.0)
    watch.add_argument("--open", action="store_true")
    watch.add_argument("--once", action="store_true", help=argparse.SUPPRESS)
    watch.set_defaults(func=command_approval_watch)

    revoke = sub.add_parser("credential-revoke", help="revoke one exact approver WebAuthn credential")
    revoke.add_argument("--config", default=".agentnet-approval/config.json")
    revoke.add_argument("--approver", required=True)
    revoke.add_argument("--credential-id", required=True)
    revoke.add_argument("--reason", required=True)
    revoke.set_defaults(func=command_approval_credential_revoke)

    status = sub.add_parser("status", help="show content-free approval-service readiness")
    status.add_argument("--config", default=".agentnet-approval/config.json")
    status.set_defaults(func=command_approval_status)


__all__ = ["configure_approval_parser"]
