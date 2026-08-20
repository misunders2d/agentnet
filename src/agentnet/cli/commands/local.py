"""AgentNet CLI local commands."""

from __future__ import annotations

import argparse

import json

import os

from datetime import (
    UTC,
    datetime,
)

from pathlib import Path

from uuid import uuid4

from agentnet.core.app import CommunicationCore

from agentnet.core.capabilities import ServerAgentCapability

from agentnet.identity.credentials import public_key_thumbprint

from agentnet.operations.config import (
    BackupSealKeyConfig,
    BackupTrustConfig,
    ExtensionConfig,
    OIDCEnrollmentConfig,
    RuntimeProfile,
    ScannerTrustConfig,
)

from agentnet.cli import helpers

from agentnet.cli.commands.backup import _provision_owner_only_signing_key

from agentnet.cli.commands.server_agent import _provision_owner_only_key


def command_init(args: argparse.Namespace) -> int:
    path = Path(args.config)
    data_dir = Path(args.data_dir)
    helpers._owner_only_directory(data_dir.absolute())
    seal_key_path = (data_dir / "secrets" / "backup-seal.key.pem").absolute()
    seal_key = _provision_owner_only_signing_key(seal_key_path)
    activated_at = int(datetime.now(UTC).replace(microsecond=0).timestamp())
    config = ExtensionConfig(
        domain_id=args.domain,
        data_dir=data_dir,
        database_url=f"sqlite:///{data_dir / 'core.sqlite3'}",
        artifact_dir=data_dir / "artifacts",
        public_base_url=args.public_base_url,
        backup_trust=BackupTrustConfig(
            domain_id=args.domain,
            trust_root_revision=1,
            minimum_key_epoch=1,
            active_signer_key_id=public_key_thumbprint(seal_key.public_pem),
            keys=(
                BackupSealKeyConfig(
                    key_id=public_key_thumbprint(seal_key.public_pem),
                    key_epoch=1,
                    public_key_pem=seal_key.public_pem,
                    not_before=activated_at,
                ),
            ),
        ),
    )
    helpers._write_private_config(path, config.redacted_export(), force=args.force)
    core = CommunicationCore.open(config)
    try:
        core.bootstrap_domain()
    finally:
        core.close()
    print(
        json.dumps(
            {
                "config": str(path),
                "data_dir": str(data_dir),
                "profile": config.profile.value,
                "backup_seal_private_key": str(seal_key_path),
                "backup_seal_key_custody": "local_software_key_not_production_kms",
            }
        )
    )
    return 0


def command_network_create(args: argparse.Namespace) -> int:
    """Create one production server-agent namespace without inventing a founder."""

    path = Path(args.config)
    if args.artifact_mode == "enabled" and not args.scanner_trust_config:
        raise SystemExit("artifact_mode=enabled requires scanner trust configuration")
    if args.artifact_mode == "disabled" and args.scanner_trust_config:
        raise SystemExit("artifact_mode=disabled forbids scanner trust configuration")
    oidc_value = Path(args.oidc_config).read_text(encoding="utf-8")
    try:
        oidc = OIDCEnrollmentConfig.model_validate_json(oidc_value)
    except Exception as exc:
        raise SystemExit("OIDC/independent-approval configuration is invalid") from exc
    scanner_trust = None
    if args.scanner_trust_config:
        try:
            scanner_trust = ScannerTrustConfig.model_validate_json(
                Path(args.scanner_trust_config).read_text(encoding="utf-8")
            )
        except Exception as exc:
            raise SystemExit("scanner trust configuration is invalid") from exc
    domain_id = args.domain or f"network-{uuid4().hex}.agentnet"
    data_dir = Path(args.data_dir)
    helpers._owner_only_directory(data_dir.absolute())
    seal_key_path = (data_dir / "secrets" / "backup-seal.key.pem").absolute()
    seal_key = _provision_owner_only_signing_key(seal_key_path)
    activated_at = int(datetime.now(UTC).replace(microsecond=0).timestamp())
    database_url = args.database_url
    if args.database_url_from_env:
        database_url = os.environ.get(args.database_url_env)
        if not database_url:
            raise SystemExit("network create database URL environment reference is absent")
    config = ExtensionConfig(
        profile=RuntimeProfile.ALWAYS_ON_SERVER_AGENT,
        domain_id=domain_id,
        data_dir=data_dir,
        database_url=database_url,
        database_url_env=args.database_url_env,
        artifact_mode=args.artifact_mode,
        artifact_backend="postgres-manifest",
        artifact_dir=data_dir / "artifacts",
        public_base_url=args.public_base_url,
        runtime_instance_id=args.runtime_instance_id,
        oidc_enrollment=oidc,
        scanner_trust=scanner_trust,
        server_agent_capabilities=(
            {ServerAgentCapability.OFFLINE_CUSTODY}
            if args.artifact_mode == "disabled"
            else {
                ServerAgentCapability.OFFLINE_CUSTODY,
                ServerAgentCapability.ARTIFACT_STORAGE,
            }
        ),
        postgres_recovery_topology=args.postgres_recovery_topology,
        backup_trust=BackupTrustConfig(
            domain_id=domain_id,
            trust_root_revision=1,
            minimum_key_epoch=1,
            active_signer_key_id=public_key_thumbprint(seal_key.public_pem),
            keys=(
                BackupSealKeyConfig(
                    key_id=public_key_thumbprint(seal_key.public_pem),
                    key_epoch=1,
                    public_key_pem=seal_key.public_pem,
                    not_before=activated_at,
                ),
            ),
        ),
    )
    _provision_owner_only_key(data_dir / "secrets" / "records.key")
    if args.artifact_mode == "enabled":
        _provision_owner_only_key(data_dir / "secrets" / "artifact.key")
    helpers._write_private_config(path, config.redacted_export(), force=args.force)
    core = CommunicationCore.open(config, validate_deployment_identity=False)
    try:
        domain = core.bootstrap_domain()
        local_readiness = core.readiness()
    finally:
        core.close()
    print(
        json.dumps(
            {
                "config": str(path),
                "data_dir": str(data_dir),
                "domain": domain,
                "local_readiness": local_readiness,
                "namespace_semantics": (
                    "domain_id is an opaque private AgentNet namespace; it is not proof of DNS ownership"
                ),
                "backup_seal_private_key": str(seal_key_path),
                "backup_seal_key_custody": "local_software_key_not_production_kms",
                "next": [
                    (
                        "run agentnet join guided with --server "
                        + config.public_base_url
                        + ", --domain "
                        + config.domain_id
                        + ", the exact supported --harness, an approved --name, "
                        "--state .agentnet/guided-join.json, and "
                        "--identity .agentnet/server-agent-identity.json"
                    ),
                    "agentnet server-agent activate --config "
                    + str(path)
                    + " --identity .agentnet/server-agent-identity.json",
                    "agentnet serve --config " + str(path),
                    (
                        "after exactly two guided same-principal harnesses enroll, run agentnet "
                        "bootstrap-plan begin --identity <fresh-identity.json>"
                    ),
                    "the fixed C0 service alone activates the pending guard after exact plan approval",
                ],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0 if bool(local_readiness.get("ready")) else 1
