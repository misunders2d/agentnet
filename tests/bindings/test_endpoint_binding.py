from __future__ import annotations

import os
import time
from dataclasses import FrozenInstanceError, replace
from hashlib import sha256
from pathlib import Path

import pytest

from agentnet.bindings.endpoint import (
    EndpointBindingRepository,
    endpoint_root,
    read_capability_digest,
    read_capability_root,
)
from agentnet.errors import AuthenticationError


def _measurement(label: str) -> str:
    return sha256(label.encode()).hexdigest()


def _enroll_endpoint(
    store,
    actor,
    *,
    harness_kind: str,
    profile_key: str,
    process_measurement: str,
) -> None:
    now = int(time.time())
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO endpoint_lifecycle(
                   domain_id,harness_id,principal_id,current_credential_id,harness_kind,
                   profile_key,state,adapter_generation,mailbox_cursor,
                   process_measurement,state_reason,revision,created_at,updated_at
               ) VALUES(?,?,?,?,?,?,'connected',1,0,?,'test_connected',1,?,?)""",
            (
                actor.domain_id,
                actor.harness_id,
                actor.principal_id,
                actor.credential_id,
                harness_kind,
                profile_key,
                process_measurement,
                now,
                now,
            ),
        )


def _repository(store, tmp_path: Path) -> EndpointBindingRepository:
    return EndpointBindingRepository(store, tmp_path / "endpoint-capabilities")


def test_endpoint_binding_is_frozen_and_keeps_exact_plan_fields(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="pi-primary",
        process_measurement=_measurement("pi-primary"),
    )
    binding = _repository(store, tmp_path).load_current(
        domain_id=actor.domain_id,
        harness_id=actor.harness_id,
    )

    with pytest.raises(FrozenInstanceError):
        binding.adapter_generation = 2  # type: ignore[misc]


def test_two_pi_profiles_receive_distinct_roots_and_capabilities(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    principal_id = "principal-with-two-pi-profiles"
    actor_a, _ = identity_factory(kind="pi", principal_id=principal_id)
    actor_b, _ = identity_factory(kind="pi", principal_id=principal_id)
    _enroll_endpoint(
        store,
        actor_a,
        harness_kind="pi",
        profile_key="pi-profile-a",
        process_measurement=_measurement("pi-a"),
    )
    _enroll_endpoint(
        store,
        actor_b,
        harness_kind="pi",
        profile_key="pi-profile-b",
        process_measurement=_measurement("pi-b"),
    )
    repository = _repository(store, tmp_path)

    a = repository.load_current(domain_id=actor_a.domain_id, harness_id=actor_a.harness_id)
    b = repository.load_current(domain_id=actor_b.domain_id, harness_id=actor_b.harness_id)

    assert a.capability_root_path != b.capability_root_path
    assert a.capability_root_path.parent.name != actor_a.harness_id
    assert b.capability_root_path.parent.name != actor_b.harness_id
    assert read_capability_root(a) != read_capability_root(b)
    assert read_capability_digest(a) != read_capability_digest(b)
    assert endpoint_root(repository.capability_base, a) == a.capability_root_path.parent
    assert endpoint_root(repository.capability_base, b) == b.capability_root_path.parent


def test_only_capability_digest_is_stored_centrally(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="digest-only",
        process_measurement=_measurement("digest-only"),
    )
    binding = _repository(store, tmp_path).load_current(
        domain_id=actor.domain_id,
        harness_id=actor.harness_id,
    )

    row = store.fetch_one(
        "SELECT capability_root_digest FROM endpoint_lifecycle WHERE domain_id=? AND harness_id=?",
        (actor.domain_id, actor.harness_id),
    )
    assert row["capability_root_digest"] == read_capability_digest(binding)
    assert len(row["capability_root_digest"]) == 64
    assert read_capability_root(binding).hex() != row["capability_root_digest"]


def test_generation_rotation_rejects_stale_descriptor_and_changes_root(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="generation-rotation",
        process_measurement=_measurement("generation-one"),
    )
    repository = _repository(store, tmp_path)
    old = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)
    old_digest = read_capability_digest(old)

    current = repository.rotate_generation(
        actor=actor,
        expected_generation=old.adapter_generation,
        process_measurement="pid:22:start:900",
    )

    assert current.adapter_generation == old.adapter_generation + 1
    assert current.capability_root_path != old.capability_root_path
    assert read_capability_digest(current) != old_digest
    assert current.process_measurement == _measurement("pid:22:start:900")
    with pytest.raises(AuthenticationError, match="generation"):
        repository.verify_current(old)
    with pytest.raises(AuthenticationError, match="generation"):
        repository.rotate_generation(
            actor=actor,
            expected_generation=old.adapter_generation,
            process_measurement="pid:23:start:901",
        )


def test_changed_process_measurement_is_rejected_per_descriptor(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="codex")
    measurement = _measurement("codex-process")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="codex",
        profile_key="codex-primary",
        process_measurement=measurement,
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)

    repository.verify_current(binding, process_measurement=f"sha256:{measurement}")
    with pytest.raises(AuthenticationError, match="process measurement"):
        repository.verify_current(binding, process_measurement="different executable")
    with pytest.raises(AuthenticationError, match="process measurement"):
        repository.verify_current(replace(binding, process_measurement=_measurement("stale")))


def test_owner_private_directory_and_file_are_enforced_on_every_load(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="private-root",
        process_measurement=_measurement("private-root"),
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)

    os.chmod(binding.capability_root_path.parent, 0o750)
    with pytest.raises(AuthenticationError, match="owner-private"):
        repository.verify_current(binding)
    os.chmod(binding.capability_root_path.parent, 0o700)

    os.chmod(binding.capability_root_path, 0o640)
    with pytest.raises(AuthenticationError, match="owner-private"):
        repository.verify_current(binding)
    os.chmod(binding.capability_root_path, 0o600)


def test_symlinked_generation_root_is_rejected(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="symlink-root",
        process_measurement=_measurement("symlink-root"),
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)
    root = binding.capability_root_path.parent
    real_root = root.with_name(f"{root.name}-real")
    root.rename(real_root)
    root.symlink_to(real_root, target_is_directory=True)

    with pytest.raises(AuthenticationError, match="owner-private real directory"):
        repository.verify_current(binding)

    root.unlink()
    real_root.rename(root)


def test_sibling_descriptor_cannot_cross_exact_endpoint(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    principal_id = "principal-sibling-isolation"
    actor_a, _ = identity_factory(kind="pi", principal_id=principal_id)
    actor_b, _ = identity_factory(kind="pi", principal_id=principal_id)
    _enroll_endpoint(
        store,
        actor_a,
        harness_kind="pi",
        profile_key="sibling-a",
        process_measurement=_measurement("sibling-a"),
    )
    _enroll_endpoint(
        store,
        actor_b,
        harness_kind="pi",
        profile_key="sibling-b",
        process_measurement=_measurement("sibling-b"),
    )
    repository = _repository(store, tmp_path)
    a = repository.load_current(domain_id=actor_a.domain_id, harness_id=actor_a.harness_id)
    b = repository.load_current(domain_id=actor_b.domain_id, harness_id=actor_b.harness_id)

    crossed = replace(
        a,
        harness_id=b.harness_id,
        credential_id=b.credential_id,
        capability_root_path=b.capability_root_path,
    )
    with pytest.raises(AuthenticationError, match="identity"):
        repository.verify_current(crossed)


def test_domain_and_current_credential_changes_fail_closed(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="codex")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="codex",
        profile_key="domain-credential-fence",
        process_measurement=_measurement("domain-credential-fence"),
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)

    with pytest.raises(AuthenticationError, match="unavailable"):
        repository.verify_current(replace(binding, domain_id="other.example"))

    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET status='revoked' WHERE credential_id=?",
            (actor.credential_id,),
        )
    with pytest.raises(AuthenticationError, match="credential"):
        repository.verify_current(binding)


def test_current_principal_revocation_blocks_existing_descriptor(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="revocation-fence",
        process_measurement=_measurement("revocation-fence"),
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)

    with store.transaction() as connection:
        connection.execute(
            "UPDATE principals SET status='revoked' WHERE principal_id=?",
            (actor.principal_id,),
        )
    with pytest.raises(AuthenticationError, match="authority"):
        repository.verify_current(binding)


def test_current_domain_revocation_blocks_existing_descriptor(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    actor, _ = identity_factory(kind="codex")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="codex",
        profile_key="domain-revocation-fence",
        process_measurement=_measurement("domain-revocation-fence"),
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(domain_id=actor.domain_id, harness_id=actor.harness_id)

    with store.transaction() as connection:
        connection.execute(
            "UPDATE domains SET status='revoked',revocation_epoch=revocation_epoch+1 WHERE domain_id=?",
            (actor.domain_id,),
        )
    with pytest.raises(AuthenticationError, match="authority"):
        repository.verify_current(binding)


def test_interrupted_capability_materialization_recovers_under_owner_private_root(
    store,
    tmp_path: Path,
    identity_factory,
) -> None:
    """A crash between file creation and commit must not brick the endpoint."""

    actor, _ = identity_factory(kind="pi")
    _enroll_endpoint(
        store,
        actor,
        harness_kind="pi",
        profile_key="interrupted-materialization",
        process_measurement=_measurement("interrupted-materialization"),
    )
    repository = _repository(store, tmp_path)
    binding = repository.load_current(
        domain_id=actor.domain_id,
        harness_id=actor.harness_id,
    )
    orphan = binding.capability_root_path
    assert orphan.exists()
    with store.transaction() as connection:
        connection.execute(
            "UPDATE endpoint_lifecycle SET capability_root_digest=NULL WHERE domain_id=? AND harness_id=?",
            (actor.domain_id, actor.harness_id),
        )

    recovered = repository.load_current(
        domain_id=actor.domain_id,
        harness_id=actor.harness_id,
    )

    assert recovered.capability_root_path == orphan
    assert read_capability_digest(recovered) == store.fetch_one(
        "SELECT capability_root_digest FROM endpoint_lifecycle WHERE domain_id=? AND harness_id=?",
        (actor.domain_id, actor.harness_id),
    )["capability_root_digest"]
    assert repository.load_current(
        domain_id=actor.domain_id,
        harness_id=actor.harness_id,
    ).capability_root_path == orphan
