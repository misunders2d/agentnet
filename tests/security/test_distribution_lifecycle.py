from __future__ import annotations

import hashlib
import json
import os
import threading
import time
import sys
from pathlib import Path

import pytest

from agentnet.errors import AuthenticationError, ConflictError, GateBlocked, ValidationError
from agentnet.security.distribution import (
    DistributionInstaller,
    HealthCheckCommand,
    verify_distribution_release,
)
from agentnet.security.signatures import P256KeyPair
from agentnet.security.update import (
    UPDATE_SIGNATURE_PURPOSE,
    UpdateTrustRoot,
    UpdateVerificationState,
)


NOW = 1_900_000_000


def state(**updates: object) -> UpdateVerificationState:
    value: dict[str, object] = {
        "installed_version": "1.0.0",
        "installed_sequence": 1,
        "highest_seen_version": "1.0.0",
        "highest_seen_sequence": 1,
        "highest_seen_manifest_digest": "b" * 64,
        "last_advance_at": NOW - 10,
    }
    value.update(updates)
    return UpdateVerificationState.model_validate(value)


def trust(keys: dict[str, P256KeyPair]) -> UpdateTrustRoot:
    return UpdateTrustRoot.model_validate(
        {
            "schema": "agentnet.update.root.v1",
            "root_version": 7,
            "expires_at": NOW + 86_400,
            "threshold": 2,
            "keys": {name: key.public_pem for name, key in keys.items()},
            "max_manifest_lifetime_seconds": 86_400,
            "max_freeze_seconds": 600,
        }
    )


def manifest(
    content: bytes,
    *,
    version: str = "1.1.0",
    sequence: int = 2,
    installed: str = "1.0.0",
) -> dict[str, object]:
    return {
        "schema": "agentnet.update.manifest.v1",
        "product": "agentnet",
        "channel": "stable",
        "version": version,
        "release_sequence": sequence,
        "root_version": 7,
        "published_at": NOW - 30,
        "expires_at": NOW + 3600,
        "minimum_installed_version": installed,
        "maximum_installed_version": version,
        "artifacts": [
            {
                "platform": "linux",
                "architecture": "x86_64",
                "uri": f"https://updates.example/{version}/agentnet.whl",
                "sha256": hashlib.sha256(content).hexdigest(),
                "size": len(content),
            }
        ],
    }


def verified(
    value: dict[str, object],
    keys: dict[str, P256KeyPair],
    current: UpdateVerificationState,
):
    signatures = [
        {
            "key_id": name,
            "signature": key.sign(UPDATE_SIGNATURE_PURPOSE, value),
        }
        for name, key in keys.items()
    ]
    return verify_distribution_release(
        value,
        signatures,
        trust(keys),
        state=current,
        now=NOW,
    )


def signed(value: dict[str, object], keys: dict[str, P256KeyPair]) -> list[dict[str, str]]:
    return [
        {"key_id": name, "signature": key.sign(UPDATE_SIGNATURE_PURPOSE, value)}
        for name, key in keys.items()
    ]


def installer(
    root: Path,
    keys: dict[str, P256KeyPair],
    current: UpdateVerificationState,
    **kwargs: object,
) -> DistributionInstaller:
    return DistributionInstaller(
        root,
        trusted_update_root=trust(keys),
        bootstrap_state=current,
        architecture="x86_64",
        **kwargs,
    )


def health_command(script: str = "raise SystemExit(0)", *args: str) -> HealthCheckCommand:
    executable = Path(sys.executable).resolve()
    return HealthCheckCommand(
        argv=(sys.executable, "-I", "-c", script, "{bundle}", *args),
        executable_sha256=hashlib.sha256(executable.read_bytes()).hexdigest(),
    )


def test_threshold_verified_release_installs_atomically_and_retries_exactly(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    content = b"synthetic signed wheel bytes"
    source = tmp_path / "candidate.whl"
    source.write_bytes(content)
    value = manifest(content)
    lifecycle = installer(tmp_path / "install", keys, state())

    result = lifecycle.install(
        value,
        signed(value, keys),
        source,
        now=NOW,
        health_check=health_command(),
    )
    assert result["state"] == "active"
    assert result["release_id"] == "2-1.1.0"
    assert result["previous_release"] is None
    assert result["verification_state"]["installed_version"] == "1.1.0"
    bundle = Path(result["bundle"])
    assert bundle.read_bytes() == content
    assert bundle.stat().st_mode & 0o777 == 0o400
    assert (tmp_path / "install").stat().st_mode & 0o777 == 0o700

    duplicate = lifecycle.install(
        value,
        signed(value, keys),
        source,
        now=NOW,
        health_check=health_command("raise SystemExit(1)"),
    )
    assert duplicate["state"] == "active"
    assert duplicate["release_id"] == "2-1.1.0"
    assert duplicate["duplicate"] is True
    assert duplicate["bundle"] == str(bundle)
    assert duplicate["verification_state"]["installed_sequence"] == 2


def test_failed_candidate_keeps_prior_active_and_records_observed_anti_rollback_state(
    tmp_path: Path,
) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    first_bytes = b"first release"
    first_source = tmp_path / "first.whl"
    first_source.write_bytes(first_bytes)
    lifecycle = installer(tmp_path / "install", keys, state())
    first = manifest(first_bytes)
    installed = lifecycle.install(
        first, signed(first, keys), first_source, now=NOW, health_check=health_command()
    )
    installed_state = UpdateVerificationState.model_validate(installed["verification_state"])

    second_bytes = b"unhealthy second release"
    second_source = tmp_path / "second.whl"
    second_source.write_bytes(second_bytes)
    second = manifest(second_bytes, version="1.2.0", sequence=3, installed="1.1.0")
    with pytest.raises(GateBlocked, match="prior release remains active"):
        lifecycle.install(
            second, signed(second, keys), second_source, now=NOW, health_check=health_command("raise SystemExit(1)")
        )

    persisted = json.loads(lifecycle.state_path.read_text())
    assert persisted["active_release"] == "2-1.1.0"
    assert persisted["failed_releases"] == ["3-1.2.0"]
    assert persisted["verification_state"]["installed_version"] == "1.1.0"
    assert persisted["verification_state"]["highest_seen_version"] == "1.2.0"


def test_artifact_symlink_digest_substitution_and_platform_mismatch_fail_closed(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    content = b"exact candidate"
    real = tmp_path / "real.whl"
    real.write_bytes(content)
    symlink = tmp_path / "candidate.whl"
    symlink.symlink_to(real)
    value = manifest(content)

    lifecycle = installer(tmp_path / "install", keys, state())
    with pytest.raises(GateBlocked, match="non-symlink"):
        lifecycle.install(value, signed(value, keys), symlink, now=NOW, health_check=health_command())

    wrong = tmp_path / "wrong.whl"
    wrong.write_bytes(content + b"tamper")
    with pytest.raises(GateBlocked, match="size"):
        lifecycle.install(value, signed(value, keys), wrong, now=NOW, health_check=health_command())

    arm = DistributionInstaller(
        tmp_path / "arm-install",
        trusted_update_root=trust(keys),
        bootstrap_state=state(),
        architecture="aarch64",
    )
    with pytest.raises(GateBlocked, match="no unique artifact"):
        arm.install(value, signed(value, keys), real, now=NOW, health_check=health_command())

    actual_parent = tmp_path / "actual-parent"
    actual_parent.mkdir()
    linked_parent = tmp_path / "linked-parent"
    linked_parent.symlink_to(actual_parent, target_is_directory=True)
    through_symlink = DistributionInstaller(
        linked_parent / "install",
        trusted_update_root=trust(keys),
        bootstrap_state=state(),
        architecture="x86_64",
    )
    with pytest.raises(GateBlocked, match="traverse a symlink"):
        through_symlink.install(
            value, signed(value, keys), real, now=NOW, health_check=health_command()
        )


def test_health_check_cannot_mutate_verified_staged_bytes_before_activation(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    content = b"candidate must remain immutable"
    source = tmp_path / "candidate.whl"
    source.write_bytes(content)
    lifecycle = installer(tmp_path / "install", keys, state())

    with pytest.raises(GateBlocked, match="prior release remains active"):
        value = manifest(content)
        lifecycle.install(
            value,
            signed(value, keys),
            source,
            now=NOW,
            health_check=health_command(
                "import os,sys,pathlib; p=pathlib.Path(sys.argv[1]); os.chmod(p,0o600); p.write_bytes(b'substituted after verification')"
            ),
        )
    persisted = json.loads(lifecycle.state_path.read_text())
    assert persisted["active_release"] is None
    assert persisted["failed_releases"] == ["2-1.1.0"]


def test_uninstall_preflights_allowlist_and_refuses_unexpected_or_symlink_entries(
    tmp_path: Path,
) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    content = b"installed bytes"
    source = tmp_path / "candidate.whl"
    source.write_bytes(content)
    credential = tmp_path / "private" / "credential.key"
    credential.parent.mkdir(mode=0o700)
    credential.write_bytes(b"extension-owned credential")
    os.chmod(credential, 0o600)
    lifecycle = DistributionInstaller(
        tmp_path / "install",
        trusted_update_root=trust(keys),
        bootstrap_state=state(),
        architecture="x86_64",
        cleanup_allowlist=(credential,),
    )
    value = manifest(content)
    lifecycle.install(
        value, signed(value, keys), source, now=NOW, health_check=health_command()
    )

    unexpected = tmp_path / "install" / "operator-data"
    unexpected.write_text("must not delete")
    refused = lifecycle.uninstall(cleanup_paths=(credential,))
    assert refused["state"] == "refused"
    assert refused["deleted"] == []
    assert str(unexpected) in refused["residual"]
    assert credential.exists()
    unexpected.unlink()

    result = lifecycle.uninstall(cleanup_paths=(credential,))
    assert result["state"] == "uninstalled"
    assert result["residual"] == []
    assert result["secure_erase_guaranteed"] is False
    assert not credential.exists()
    assert not (tmp_path / "install").exists()

    unrelated = tmp_path / "unrelated"
    unrelated.write_text("preserve")
    with pytest.raises(ValidationError, match="allowlist"):
        lifecycle.uninstall(cleanup_paths=(unrelated,))
    assert unrelated.read_text() == "preserve"


def test_installer_reverifies_against_persisted_state_and_rejects_stale_release(
    tmp_path: Path,
) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(tmp_path / "install", keys, state())
    first_bytes = b"release two"
    first = manifest(first_bytes)
    first_source = tmp_path / "release-two.whl"
    first_source.write_bytes(first_bytes)
    result = lifecycle.install(
        first, signed(first, keys), first_source, now=NOW, health_check=health_command()
    )
    stale_caller_token = verified(first, keys, state())
    assert stale_caller_token.manifest.release_sequence == 2

    second_bytes = b"release three"
    second = manifest(second_bytes, version="1.2.0", sequence=3, installed="1.1.0")
    second_source = tmp_path / "release-three.whl"
    second_source.write_bytes(second_bytes)
    lifecycle.install(
        second, signed(second, keys), second_source, now=NOW, health_check=health_command()
    )
    with pytest.raises(AuthenticationError, match="compatibility|rollback"):
        lifecycle.install(
            first, signed(first, keys), first_source, now=NOW, health_check=health_command()
        )
    persisted = json.loads(lifecycle.state_path.read_text())
    assert persisted["active_release"] == "3-1.2.0"
    assert persisted["verification_state"]["installed_sequence"] == 3


def test_state_compare_and_swap_rejects_health_callback_state_replacement(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(tmp_path / "install", keys, state())
    first_bytes = b"release two"
    first = manifest(first_bytes)
    first_source = tmp_path / "release-two.whl"
    first_source.write_bytes(first_bytes)
    lifecycle.install(
        first, signed(first, keys), first_source, now=NOW, health_check=health_command()
    )
    second_bytes = b"release three"
    second = manifest(second_bytes, version="1.2.0", sequence=3, installed="1.1.0")
    second_source = tmp_path / "release-three.whl"
    second_source.write_bytes(second_bytes)

    with pytest.raises(ConflictError, match="changed concurrently"):
        lifecycle.install(
            second,
            signed(second, keys),
            second_source,
            now=NOW,
            health_check=health_command(
                "import json,sys,pathlib; p=pathlib.Path(sys.argv[2]); v=json.loads(p.read_text()); v['failed_releases']=['999-9.9.9']; p.write_text(json.dumps(v,sort_keys=True,separators=(',',':'))+'\\n')",
                str(lifecycle.state_path),
            ),
        )
    persisted = json.loads(lifecycle.state_path.read_text())
    assert persisted["active_release"] == "2-1.1.0"


def test_uninstall_rejects_allowlisted_file_reached_through_symlink_ancestor(
    tmp_path: Path,
) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    actual = tmp_path / "actual-private"
    actual.mkdir(mode=0o700)
    credential = actual / "credential.key"
    credential.write_bytes(b"preserve")
    os.chmod(credential, 0o600)
    linked = tmp_path / "linked-private"
    linked.symlink_to(actual, target_is_directory=True)
    linked_credential = linked / "credential.key"
    lifecycle = DistributionInstaller(
        tmp_path / "absent-install",
        trusted_update_root=trust(keys),
        bootstrap_state=state(),
        architecture="x86_64",
        cleanup_allowlist=(linked_credential,),
    )
    result = lifecycle.uninstall(cleanup_paths=(linked_credential,))
    assert result["state"] == "refused"
    assert result["deleted"] == []
    assert str(linked_credential.absolute()) in result["residual"]
    assert credential.read_bytes() == b"preserve"


def test_durable_anti_rollback_state_survives_ordinary_uninstall(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(tmp_path / "install", keys, state())
    content = b"current release"
    value = manifest(content)
    source = tmp_path / "current.whl"
    source.write_bytes(content)
    lifecycle.install(value, signed(value, keys), source, now=NOW, health_check=health_command())
    trust_state = lifecycle.state_path

    result = lifecycle.uninstall()
    assert result["state"] == "uninstalled"
    assert trust_state.is_file()
    persisted = json.loads(trust_state.read_text())
    assert persisted["verification_state"]["installed_sequence"] == 2
    assert persisted["active_release"] is None

    old_content = b"old release"
    old = manifest(old_content, version="1.0.0", sequence=1, installed="1.0.0")
    old["maximum_installed_version"] = "1.1.0"
    old_source = tmp_path / "old.whl"
    old_source.write_bytes(old_content)
    replacement = installer(tmp_path / "install", keys, state())
    with pytest.raises(AuthenticationError, match="rollback"):
        replacement.install(
            old, signed(old, keys), old_source, now=NOW, health_check=health_command()
        )


def test_uninstall_failure_leaves_blocking_root_tombstone_and_honest_residual(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(tmp_path / "install", keys, state())
    content = b"installed release"
    value = manifest(content)
    source = tmp_path / "current.whl"
    source.write_bytes(content)
    lifecycle.install(value, signed(value, keys), source, now=NOW, health_check=health_command())
    original_unlink = os.unlink

    def fail_bundle(path, *args, **kwargs):
        if os.fspath(path) == "bundle.whl":
            raise OSError("injected unlink failure")
        return original_unlink(path, *args, **kwargs)

    monkeypatch.setattr(os, "unlink", fail_bundle)
    result = lifecycle.uninstall()
    assert result["state"] == "residual"
    assert result["secure_erase_guaranteed"] is False
    assert not (tmp_path / "install").exists()
    assert len(result["residual"]) == 1
    assert Path(result["residual"][0]).name.startswith(".agentnet-distribution-uninstall-")
    with pytest.raises(GateBlocked, match="unresolved uninstall tombstone"):
        lifecycle.install(value, signed(value, keys), source, now=NOW, health_check=health_command())
    monkeypatch.setattr(os, "unlink", original_unlink)
    recovered = lifecycle.recover_uninstall()
    assert recovered["state"] == "uninstalled"
    assert recovered["residual"] == []
    reinstall = lifecycle.install(
        value, signed(value, keys), source, now=NOW, health_check=health_command()
    )
    assert reinstall["state"] == "active"


def test_uninstall_waits_for_bounded_health_check_under_same_lock(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(
        tmp_path / "install", keys, state(), health_check_timeout_seconds=5
    )
    content = b"health serialized release"
    value = manifest(content)
    source = tmp_path / "current.whl"
    source.write_bytes(content)
    started = tmp_path / "health-started"
    release = tmp_path / "health-release"

    install_result: list[object] = []
    install_thread = threading.Thread(
        target=lambda: install_result.append(
            lifecycle.install(
                value,
                signed(value, keys),
                source,
                now=NOW,
                health_check=health_command(
                    "import sys,time,pathlib; started=pathlib.Path(sys.argv[2]); release=pathlib.Path(sys.argv[3]); started.write_text('started');\nwhile not release.exists(): time.sleep(0.01)",
                    str(started),
                    str(release),
                ),
            )
        )
    )
    install_thread.start()
    deadline = time.monotonic() + 2
    while not started.exists() and time.monotonic() < deadline:
        time.sleep(0.01)
    assert started.exists()
    uninstall_result: list[object] = []
    uninstall_thread = threading.Thread(target=lambda: uninstall_result.append(lifecycle.uninstall()))
    uninstall_thread.start()
    time.sleep(0.1)
    assert uninstall_thread.is_alive()
    release.write_text("continue")
    install_thread.join(3)
    uninstall_thread.join(3)
    assert install_result and install_result[0]["state"] == "active"
    assert uninstall_result and uninstall_result[0]["state"] == "uninstalled"


def test_stalled_health_process_group_is_killed_with_prior_state_preserved(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(
        tmp_path / "install", keys, state(), health_check_timeout_seconds=1
    )
    content = b"stalled health release"
    value = manifest(content)
    source = tmp_path / "current.whl"
    source.write_bytes(content)

    started = time.monotonic()
    with pytest.raises(GateBlocked, match="prior release remains active"):
        lifecycle.install(
            value,
            signed(value, keys),
            source,
            now=NOW,
            health_check=health_command("import time; time.sleep(30)"),
        )
    assert time.monotonic() - started < 4
    persisted = json.loads(lifecycle.state_path.read_text())
    assert persisted["active_release"] is None
    assert persisted["failed_releases"] == ["2-1.1.0"]


def test_successful_health_check_reaps_spawned_descendant_group(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(tmp_path / "install", keys, state())
    content = b"descendant health release"
    value = manifest(content)
    source = tmp_path / "current.whl"
    source.write_bytes(content)
    pid_file = tmp_path / "health-child.pid"
    command = health_command(
        "import subprocess,sys,pathlib; p=subprocess.Popen(['/bin/sleep','30']); pathlib.Path(sys.argv[2]).write_text(str(p.pid))",
        str(pid_file),
    )
    lifecycle.install(value, signed(value, keys), source, now=NOW, health_check=command)
    child_pid = int(pid_file.read_text())
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        try:
            os.kill(child_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.02)
    else:
        pytest.fail("health-check descendant survived successful checker exit")


def test_health_subreaper_kills_detached_delayed_bundle_mutator(tmp_path: Path) -> None:
    keys = {"release-a": P256KeyPair.generate(), "release-b": P256KeyPair.generate()}
    lifecycle = installer(tmp_path / "install", keys, state())
    content = b"detached descendant health release"
    value = manifest(content)
    source = tmp_path / "current.whl"
    source.write_bytes(content)
    pid_file = tmp_path / "detached-health-child.pid"
    child_script = """
import os
import pathlib
import sys
import time

pathlib.Path(sys.argv[2]).write_text(str(os.getpid()))
time.sleep(0.5)
bundle = pathlib.Path(sys.argv[1])
bundle.chmod(0o600)
bundle.write_bytes(b"late detached mutation")
"""
    checker_script = f"""
import pathlib
import subprocess
import sys
import time

pid_file = pathlib.Path(sys.argv[2])
subprocess.Popen(
    [sys.executable, "-I", "-c", {child_script!r}, sys.argv[1], sys.argv[2]],
    close_fds=True,
    start_new_session=True,
)
deadline = time.monotonic() + 2
while not pid_file.exists() and time.monotonic() < deadline:
    time.sleep(0.01)
raise SystemExit(0 if pid_file.exists() else 2)
"""

    result = lifecycle.install(
        value,
        signed(value, keys),
        source,
        now=NOW,
        health_check=health_command(checker_script, str(pid_file)),
    )
    bundle = Path(result["bundle"])
    detached_pid = int(pid_file.read_text())

    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        try:
            os.kill(detached_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.02)
    else:
        pytest.fail("setsid health-check descendant survived subreaper cleanup")

    time.sleep(0.6)
    assert result["state"] == "active"
    assert bundle.read_bytes() == content
    assert bundle.stat().st_mode & 0o777 == 0o400
