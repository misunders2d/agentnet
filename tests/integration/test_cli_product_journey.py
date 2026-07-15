from __future__ import annotations

import json
import socket
import sqlite3
import subprocess
import sys
import time
from datetime import UTC, datetime
from pathlib import Path

import httpx
import pytest

from agentnet.cli import _authority_command, _write_private_config, build_parser
from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.security.signatures import P256KeyPair, verify_signature
from agentnet.storage.migrations import CURRENT_SCHEMA_VERSION


def _free_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def test_zero_state_and_signed_admin_commands_are_exposed_by_one_cli() -> None:
    parser = build_parser()
    assert parser.parse_args(
        [
            "network",
            "create",
            "--public-base-url",
            "https://agents.example",
            "--oidc-config",
            "oidc.json",
        ]
    ).func.__name__ == "command_network_create"
    assert parser.parse_args(
        [
            "server-agent",
            "activate",
            "--config",
            "agentnet.json",
            "--identity",
            "server-agent-identity.json",
        ]
    ).func.__name__ == "command_server_agent_activate"
    assert parser.parse_args(
        [
            "join",
            "begin",
            "--server",
            "https://agents.example",
            "--harness",
            "codex",
            "--name",
            "Laptop",
        ]
    ).func.__name__ == "command_join_begin"
    assert parser.parse_args(
        [
            "admin",
            "entitlement",
            "issue",
            "--beneficiary-identity",
            "second-admin.json",
            "--action",
            "identity.credential.recover.approve",
            "--resource",
            "*",
            "--policy-revision",
            "1",
            "--reason",
            "establish independent recovery administrator",
        ]
    ).func.__name__ == "command_admin_entitlement_issue"
    assert parser.parse_args(
        [
            "admin",
            "harness-revocation",
            "prepare",
            "--harness-id",
            "lost-laptop",
            "--reason",
            "device lost",
        ]
    ).func.__name__ == "command_admin_harness_revoke_prepare"
    assert parser.parse_args(
        [
            "recovery",
            "begin",
            "--server",
            "https://agents.example",
            "--old-harness-id",
            "lost-laptop",
            "--harness",
            "codex",
            "--name",
            "Replacement laptop",
            "--binding-assurance",
            "os_bound",
        ]
    ).func.__name__ == "command_recovery_begin"
    assert parser.parse_args(
        [
            "invitation",
            "prepare",
            "--server",
            "https://agents.example",
            "--domain",
            "corp.example",
            "--issuer",
            "https://idp.example",
            "--subject",
            "candidate-subject",
            "--email",
            "candidate@example.com",
            "--harness",
            "codex",
            "--name",
            "Candidate laptop",
            "--binding-assurance",
            "os_bound",
            "--reason",
            "enroll exact colleague device",
        ]
    ).func.__name__ == "command_invitation_prepare"
    assert parser.parse_args(["invitation", "issue"]).func.__name__ == (
        "command_invitation_issue"
    )
    assert parser.parse_args(["authority", "inventory"]).func.__name__ == (
        "command_authority_inventory"
    )
    assert parser.parse_args(
        [
            "relationship",
            "propose",
            "--relationship",
            "relationship.json",
        ]
    ).func.__name__ == "command_relationship_propose"
    assert parser.parse_args(
        [
            "relationship",
            "accept",
            "--approval",
            "approval.json",
        ]
    ).func.__name__ == "command_relationship_accept"
    assert parser.parse_args(
        [
            "message",
            "send",
            "--recipient",
            "peer-harness",
            "--payload",
            "message.json",
        ]
    ).func.__name__ == "command_message_send"
    assert parser.parse_args(
        [
            "backup",
            "sqlite",
            "--archive",
            "backup.sqlite3",
            "--manifest",
            "backup.json",
            "--seal",
            "seal.json",
            "--backup-id",
            "backup-1",
            "--audit-private-key",
            "/tmp/audit.key",
            "--seal-private-key",
            "/tmp/backup-seal.key",
            "--application-offline",
        ]
    ).func.__name__ == "command_backup_sqlite"
    assert parser.parse_args(
        [
            "restore",
            "sqlite",
            "--archive",
            "backup.sqlite3",
            "--manifest",
            "backup.json",
            "--seal",
            "seal.json",
            "--audit-public-key",
            "/tmp/audit.pub",
            "--target",
            "restored.sqlite3",
            "--application-offline",
        ]
    ).func.__name__ == "command_restore_sqlite"


def test_cli_authority_command_signs_every_revision_and_mutation_field() -> None:
    key = P256KeyPair.generate()
    actor = VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="corp.example",
        principal_id="founder-human",
        harness_id="founder-harness",
        credential_id="founder-credential",
        credential_epoch=1,
        binding_assurance="hardware_bound",
    )
    command = _authority_command(
        actor=actor,
        key=key,
        action="authorization.entitlement.issue",
        resource="entitlement:recovery-admin",
        mutation={"exact": "recovery-admin", "revision": 1},
        expected_policy_revision=7,
        expected_entity_revision=0,
        reason="bounded recovery setup",
    )
    verify_signature(
        key.public_pem,
        "agentnet.authority.command.v1",
        command.signed_fields(),
        command.signature,
    )
    assert command.expected_policy_revision == 7
    assert command.expected_entity_revision == 0
    assert command.expires_at > datetime.now(UTC)


def test_private_config_writer_is_owner_only_atomic_and_rejects_force_through_symlink(
    tmp_path: Path,
) -> None:
    config = tmp_path / "agentnet.json"
    _write_private_config(config, {"schema": "agentnet.test.v1"})
    assert config.stat().st_mode & 0o777 == 0o600
    with pytest.raises(SystemExit, match="refusing to overwrite"):
        _write_private_config(config, {"schema": "agentnet.changed.v1"})

    target = tmp_path / "target.json"
    target.write_text("do not overwrite", encoding="utf-8")
    link = tmp_path / "linked.json"
    link.symlink_to(target)
    with pytest.raises(SystemExit, match="unsafe"):
        _write_private_config(link, {"schema": "agentnet.changed.v1"}, force=True)
    assert target.read_text(encoding="utf-8") == "do not overwrite"


def test_real_cli_executes_sealed_sqlite_backup_restore_and_compromise_plan(
    tmp_path: Path,
) -> None:
    executable = Path(sys.executable).parent / "agentnet"
    config = tmp_path / "agentnet.json"
    data = tmp_path / "state"
    initialized = subprocess.run(
        [
            str(executable),
            "init",
            "--config",
            str(config),
            "--data-dir",
            str(data),
            "--domain",
            "backup-journey.example",
            "--public-base-url",
            "http://127.0.0.1:18089",
        ],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert initialized.returncode == 0, initialized.stderr
    initialized_result = json.loads(initialized.stdout)
    seal_private_key = Path(initialized_result["backup_seal_private_key"])
    audit_key = P256KeyPair.generate()
    private_key = tmp_path / "audit-private.pem"
    public_key = tmp_path / "audit-public.pem"
    private_key.write_bytes(audit_key.private_pem)
    public_key.write_text(audit_key.public_pem, encoding="ascii")
    private_key.chmod(0o600)
    public_key.chmod(0o600)
    custody = tmp_path / "custody"
    seals = tmp_path / "seals"
    restore_dir = tmp_path / "restore"
    plans = tmp_path / "plans"
    for directory in (custody, seals, restore_dir, plans):
        directory.mkdir(mode=0o700)
    archive = custody / "backup.sqlite3"
    manifest = custody / "backup.manifest.json"
    seal = seals / "backup.seal.json"
    target = restore_dir / "restored.sqlite3"

    backup = subprocess.run(
        [
            str(executable),
            "backup",
            "sqlite",
            "--config",
            str(config),
            "--archive",
            str(archive),
            "--manifest",
            str(manifest),
            "--seal",
            str(seal),
            "--backup-id",
            "cli-backup-20260713",
            "--audit-private-key",
            str(private_key),
            "--seal-private-key",
            str(seal_private_key),
            "--application-offline",
        ],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert backup.returncode == 0, backup.stderr
    backup_result = json.loads(backup.stdout)
    assert backup_result["schema_version"] == CURRENT_SCHEMA_VERSION
    assert backup_result["ha_proven"] is False
    assert archive.stat().st_mode & 0o777 == 0o600
    assert manifest.stat().st_mode & 0o777 == 0o600
    assert seal.stat().st_mode & 0o777 == 0o600

    compromise = subprocess.run(
        [
            str(executable),
            "compromise-rebuild",
            "plan",
            "--config",
            str(config),
            "--archive",
            str(archive),
            "--manifest",
            str(manifest),
            "--seal",
            str(seal),
            "--audit-public-key",
            str(public_key),
            "--target",
            str(target),
            "--output",
            str(plans / "compromise.json"),
            "--application-offline",
        ],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert compromise.returncode == 0, compromise.stderr
    compromise_result = json.loads(compromise.stdout)
    assert compromise_result["state"] == "plan_only_not_executed"
    assert compromise_result["service_safe_to_resume"] is False

    restored = subprocess.run(
        [
            str(executable),
            "restore",
            "sqlite",
            "--config",
            str(config),
            "--archive",
            str(archive),
            "--manifest",
            str(manifest),
            "--seal",
            str(seal),
            "--audit-public-key",
            str(public_key),
            "--target",
            str(target),
            "--application-offline",
        ],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert restored.returncode == 0, restored.stderr
    restored_result = json.loads(restored.stdout)
    assert restored_result["restore_completed"] is True
    assert restored_result["signed_manifest_seal_verified"] is True
    assert restored_result["audit_checkpoint_signature_verified"] is True
    assert restored_result["restored_archive_digest_verified"] is True
    assert restored_result["restored_domain_snapshot_matches_manifest"] is True
    assert restored_result["service_safe_to_resume"] is False
    assert target.stat().st_mode & 0o777 == 0o600
    with sqlite3.connect(target) as connection:
        assert connection.execute(
            "SELECT value FROM metadata WHERE key='schema_version'"
        ).fetchone() == (str(CURRENT_SCHEMA_VERSION),)

    replay = subprocess.run(
        restored.args,
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert replay.returncode != 0


def test_real_agentnet_serve_smoke_and_status_distinguish_stopped_from_local_ready(
    tmp_path: Path,
) -> None:
    executable = Path(sys.executable).parent / "agentnet"
    assert executable.is_file(), "installed agentnet console script is required for the smoke test"
    port = _free_loopback_port()
    origin = f"http://127.0.0.1:{port}"
    config = tmp_path / "agentnet.json"
    data = tmp_path / "state"
    initialized = subprocess.run(
        [
            str(executable),
            "init",
            "--config",
            str(config),
            "--data-dir",
            str(data),
            "--domain",
            "cli-smoke.example",
            "--public-base-url",
            origin,
        ],
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    assert initialized.returncode == 0, initialized.stderr
    assert config.stat().st_mode & 0o777 == 0o600

    process = subprocess.Popen(
        [
            str(executable),
            "serve",
            "--config",
            str(config),
            "--host",
            "127.0.0.1",
            "--port",
            str(port),
            "--log-level",
            "warning",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    try:
        deadline = time.monotonic() + 10
        health: httpx.Response | None = None
        while time.monotonic() < deadline:
            if process.poll() is not None:
                break
            try:
                health = httpx.get(f"{origin}/healthz", timeout=0.25)
                if health.status_code == 200:
                    break
            except httpx.HTTPError:
                time.sleep(0.05)
        assert health is not None and health.status_code == 200
        ready = httpx.get(f"{origin}/readyz", timeout=1)
        assert ready.status_code == 200, ready.text
        assert ready.json()["ready"] is True
    finally:
        process.terminate()
        try:
            stdout, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            stdout, stderr = process.communicate(timeout=5)
    assert process.returncode is not None
    assert "Traceback" not in stdout + stderr

    stopped = subprocess.run(
        [
            str(executable),
            "status",
            "--config",
            str(config),
            "--timeout",
            "0.2",
        ],
        capture_output=True,
        text=True,
        timeout=10,
        check=False,
    )
    assert stopped.returncode == 1, stopped.stderr
    stopped_status = json.loads(stopped.stdout)
    assert stopped_status["local_readiness"]["ready"] is True
    assert stopped_status["live_connectivity"]["reachable"] is False
    assert stopped_status["ready"] is False

    local_only = subprocess.run(
        [str(executable), "status", "--config", str(config), "--local-only"],
        capture_output=True,
        text=True,
        timeout=10,
        check=False,
    )
    assert local_only.returncode == 0, local_only.stderr
    assert json.loads(local_only.stdout)["ready"] is True
