from __future__ import annotations

import os
import shutil
import time
import json
import hashlib
from pathlib import Path

import pytest

from agentnet.bindings.endpoint import EndpointBinding
from agentnet.core.capabilities import (
    ENDPOINT_CAPABILITY_ROOT_BYTES,
    endpoint_capability_root_name,
)
from agentnet.security.signatures import P256KeyPair
from agentnet.supervisor.workers import (
    CleanWorkerLauncher,
    adapter_launch_profile_digest,
    semantic_adapter_spec,
)


@pytest.fixture
def fake_harnesses(tmp_path: Path) -> dict[str, str]:
    source = Path(__file__).with_name("fake_harness.py")
    binaries = tmp_path / "fake-bin"
    binaries.mkdir(mode=0o700)
    result: dict[str, str] = {}
    for harness, executable_name in {
        "claude": "claude",
        "codex": "codex",
        "pi": "pi",
        "antigravity": "agy",
    }.items():
        destination = binaries / executable_name
        shutil.copyfile(source, destination)
        os.chmod(destination, 0o700)
        result[harness] = str(destination)
    return result


@pytest.fixture
def enrolled_endpoint_binding_factory(tmp_path: Path):
    def create(*, harness_id: str, harness_kind: str = "codex") -> EndpointBinding:
        adapter_generation = 1
        domain_id = "domain-subprocess-lifecycle"
        endpoint_scope = endpoint_capability_root_name(
            domain_id=domain_id,
            harness_id=harness_id,
            adapter_generation=adapter_generation,
        )
        capability_directory = (
            tmp_path / "endpoint-capabilities" / "endpoints" / endpoint_scope
        )
        capability_directory.mkdir(parents=True, mode=0o700)
        capability_directory.chmod(0o700)
        capability_root = capability_directory / "capability-root.key"
        capability_root.write_bytes(b"\x01" * ENDPOINT_CAPABILITY_ROOT_BYTES)
        capability_root.chmod(0o600)
        return EndpointBinding(
            domain_id=domain_id,
            principal_id="principal-subprocess-lifecycle",
            harness_id=harness_id,
            harness_kind=harness_kind,
            credential_id="credential-current-epoch",
            credential_epoch=1,
            adapter_generation=adapter_generation,
            mailbox_cursor=0,
            profile_key=f"{harness_kind}:subprocess-lifecycle",
            capability_root_path=capability_root,
            process_measurement="sha256:" + "a" * 64,
        )

    return create


@pytest.fixture
def fake_bwrap(tmp_path: Path) -> str:
    source = Path(__file__).with_name("fake_bwrap.py")
    destination = tmp_path / "fake-bwrap"
    shutil.copyfile(source, destination)
    os.chmod(destination, 0o700)
    return str(destination)


@pytest.fixture
def contract_clean_runtime_factory(tmp_path: Path, fake_bwrap: str):
    """Mint signed test evidence; this is not installed-binary sandbox proof."""

    signer = P256KeyPair.generate()
    evidence_dir = tmp_path / "contract-evidence"
    evidence_dir.mkdir(mode=0o700)

    def sha256_file(path: str) -> str:
        return hashlib.sha256(Path(path).read_bytes()).hexdigest()

    def create(spec, auth, **runtime_options):
        now = int(time.time())
        broker_enabled = auth.broker_origin is not None
        admitted_profile = semantic_adapter_spec(
            spec,
            broker_enabled=broker_enabled,
            broker_origin=auth.broker_origin,
        )
        record = {
            "schema": "agentnet.clean-worker-evidence.v2",
            "harness_kind": spec.harness,
            "executable_sha256": sha256_file(spec.executable),
            "sandbox_launcher_sha256": sha256_file(fake_bwrap),
            "sandbox_launcher_kind": (
                "broker_egress_wrapper" if broker_enabled else "bubblewrap_networkless"
            ),
            "launch_profile_sha256": adapter_launch_profile_digest(admitted_profile),
            "sandbox_profile": (
                "broker-only-egress-no-ambient-secrets"
                if broker_enabled
                else "networkless-no-ambient-secrets"
            ),
            "broker_origin": auth.broker_origin,
            "credential_scope": "supervisor-model-egress-broker",
            "auth_kind": auth.kind,
            "auth_environment_names": list(auth.environment_names),
            "passed_gates": ["G03", "G05"],
            "tested_at": now,
            "expires_at": now + 3600,
            "key_id": "contract-test-signer",
        }
        record["signature"] = signer.sign("agentnet.component.adoption.v1", record)
        evidence_path = evidence_dir / f"{spec.harness}-clean-worker.json"
        evidence_path.write_text(
            json.dumps(record, allow_nan=False, separators=(",", ":"), sort_keys=True),
            encoding="utf-8",
        )
        launcher = CleanWorkerLauncher(
            evidence_dir=evidence_dir,
            trusted_evidence_keys={"contract-test-signer": signer.public_pem},
        )
        return launcher.create_adapter_runtime(
            spec,
            auth,
            sandbox_launcher=fake_bwrap,
            **runtime_options,
        )

    return create
