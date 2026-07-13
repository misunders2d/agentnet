from __future__ import annotations

import hashlib
import json
import stat
import sys
import time
import socket
import subprocess
from copy import deepcopy
from dataclasses import replace
from pathlib import Path

import pytest

from agentnet.components.bakeoff import (
    EVIDENCE_SCHEMA,
    PLAN_SCHEMA,
    BakeoffPlan,
    ClaimSpec,
    CommandSpec,
    ConfiguredReviewerTrustRoot,
    ConfiguredBakeoffSandbox,
    EvidencePackageError,
    adoption_ready,
    create_evidence_package as _create_evidence_package,
    evidence_package_digest,
    main,
    load_reviewer_trust_root,
    load_bakeoff_sandbox,
    reviewer_attestation_body,
    validate_evidence_package,
)
from agentnet.components.registry import BASELINE_COMPONENTS, ComponentRecord
from agentnet.security.signatures import P256KeyPair
from agentnet.components.bakeoff import REVIEW_ATTESTATION_PURPOSE
from agentnet.components.bakeoff import REVIEW_ROOT_ENDORSEMENT_PURPOSE
from agentnet.security.update import UpdateTrustRoot


SHA = "a" * 64
PRODUCTION_SANDBOX_WRAP = ConfiguredBakeoffSandbox.wrap
SCENARIOS = (
    "failure",
    "revocation",
    "offline",
    "duplicates",
    "upgrade",
    "rollback",
    "replacement",
)
CLAIMS = ("license", "provenance", "self_hosted", "data_egress")


def component(*, decision: str = "accepted_phase0") -> ComponentRecord:
    return ComponentRecord(
        name="synthetic-component",
        version="1.2.3",
        purpose="contract test",
        decision=decision,  # type: ignore[arg-type]
        policy_boundary="cannot define authority",
        evidence="tests/components/test_bakeoff_evidence.py",
    )


def assertions(claim: str, digest: str = SHA) -> list[dict[str, object]]:
    return [
        {
            "assertion_id": __import__("hashlib").sha256(claim.encode()).hexdigest(),
            "passed": False,
            "evidence_sha256": digest,
        }
    ]


def evidence() -> dict[str, object]:
    commands: dict[str, dict[str, object]] = {}
    for scenario in SCENARIOS:
        assertion_id = __import__("hashlib").sha256(
            f"{scenario} behavior passed".encode()
        ).hexdigest()
        body: dict[str, object] = {
            "argv_sha256": SHA,
            "cwd_sha256": SHA,
            "executable_sha256": SHA,
            "exit_code": 0,
            "status": "observed_unreviewed",
            "stdout_sha256": SHA,
            "stderr_sha256": SHA,
            "stdout_bytes": 10,
            "stderr_bytes": 0,
            "redacted_result": {
                "schema": "agentnet.component-bakeoff-scenario-result.v1",
                "scenario": scenario,
                "status": "passed",
                "assertions": [{"assertion_id": assertion_id, "status": "passed"}],
            },
        }
        commands[scenario] = {**body, "result_sha256": evidence_package_digest(body)}
    value: dict[str, object] = {
        "schema": EVIDENCE_SCHEMA,
        "component": "synthetic-component",
        "version": "1.2.3",
        "run_id": "synthetic-run-0001",
        "evidence_package_sha256": "0" * 64,
        "artifact_sha256": SHA,
        "config_sha256": SHA,
        "environment_sha256": SHA,
        "review_status": "observed_unreviewed",
        "reproducibility": {
            "plan_sha256": SHA,
            "dependency_lock_sha256": SHA,
            "executables": {scenario: SHA for scenario in SCENARIOS},
            "runtime": "3.13.0",
            "os": {"system": "Linux", "release": "test", "machine": "x86_64"},
            "sandbox_config_sha256": SHA,
            "sandbox_evidence_sha256": SHA,
            "sandbox_launcher_sha256": SHA,
            "sandbox_profile": "bwrap-unshare-all-networkless-v1",
            "egress_mode": "none",
        },
        "commands": commands,
        "license": {
            "reviewed": False,
            "spdx": "Apache-2.0",
            "evidence_sha256": SHA,
            "assertions": assertions("license reviewed"),
        },
        "provenance": {
            "verified": False,
            "source_sha256": SHA,
            "artifact_sha256": SHA,
            "evidence_sha256": SHA,
            "assertions": assertions("artifact provenance verified"),
        },
        "self_hosted": {
            "verified": False,
            "evidence_sha256": SHA,
            "assertions": assertions("self-hosted deployment verified"),
        },
        "data_egress": {
            "reviewed": False,
            "mode": "none",
            "evidence_sha256": SHA,
            "assertions": assertions("no implicit egress observed"),
        },
        **{
            scenario: {
                "status": "observed_unreviewed",
                "evidence_sha256": commands[scenario]["result_sha256"],
                "assertions": assertions(
                    f"{scenario} behavior passed", str(commands[scenario]["result_sha256"])
                ),
            }
            for scenario in SCENARIOS
        },
    }
    value["evidence_package_sha256"] = evidence_package_digest(value)
    return value


def reviewed(
    value: dict[str, object],
    key: P256KeyPair,
    root: Path,
    *,
    key_id: str = "reviewer-1",
    now: int = 2_000_000_000,
    expires_at: int = 2_000_003_600,
    profile_scope: str | None = None,
    endorsement_root_expires_at: int | None = None,
) -> tuple[dict[str, object], object, int]:
    root.mkdir(parents=True, exist_ok=True)
    root.chmod(0o700)
    co_reviewer = P256KeyPair.generate()
    body = reviewer_attestation_body(value, reviewed_at=now - 10, expires_at=expires_at)
    trust_dir = root / "config"
    trust_dir.mkdir(parents=True, exist_ok=True)
    trust_dir.chmod(0o700)
    trust_path = trust_dir / "component-reviewer-root.json"
    claims = sorted(
        {
            assertion["assertion_id"]
            for name in (*CLAIMS, *SCENARIOS)
            for assertion in value[name]["assertions"]
        }
    )
    root_body = {
        "schema": "agentnet.component-reviewer-root.v1",
        "root_id": "test-review-root",
        "expires_at": now + 7200,
        "threshold": 2,
        "keys": {key_id: key.public_pem, "reviewer-2": co_reviewer.public_pem},
        "components": [value["component"]],
        "versions": [value["version"]],
        "claims": claims,
        "profiles": [profile_scope or value["reproducibility"]["sandbox_profile"]],
    }
    endorsers = {"owner-1": P256KeyPair.generate(), "owner-2": P256KeyPair.generate()}
    endorsement_root = UpdateTrustRoot.model_validate(
        {
            "schema": "agentnet.update.root.v1",
            "root_version": 1,
            "expires_at": endorsement_root_expires_at or now + 7200,
            "threshold": 2,
            "keys": {name: signer.public_pem for name, signer in endorsers.items()},
            "max_manifest_lifetime_seconds": 3600,
            "max_freeze_seconds": 600,
        }
    )
    trust_path.write_text(
        json.dumps(
            {
                **root_body,
                "endorsements": [
                    {
                        "key_id": name,
                        "signature": signer.sign(REVIEW_ROOT_ENDORSEMENT_PURPOSE, root_body),
                    }
                    for name, signer in endorsers.items()
                ],
            },
            sort_keys=True,
            separators=(",", ":"),
        )
        + "\n"
    )
    trust_path.chmod(0o600)
    return (
        {
            **body,
            "signatures": [
                {"key_id": key_id, "signature": key.sign(REVIEW_ATTESTATION_PURPOSE, body)},
                {
                    "key_id": "reviewer-2",
                    "signature": co_reviewer.sign(REVIEW_ATTESTATION_PURPOSE, body),
                },
            ],
        },
        load_reviewer_trust_root(root, endorsement_root=endorsement_root, now=now),
        now,
    )


def write_inputs(tmp_path: Path) -> tuple[Path, Path, Path, Path]:
    artifact = tmp_path / "component.whl"
    config = tmp_path / "component-config.json"
    proof = tmp_path / "review-proof.txt"
    dependency_lock = tmp_path / "dependency.lock"
    artifact.write_bytes(b"immutable synthetic artifact\n")
    config.write_text('{"mode":"self-hosted"}\n', encoding="utf-8")
    proof.write_text("independent synthetic proof\n", encoding="utf-8")
    dependency_lock.write_text("exact-dependency==1.0 --hash=sha256:test\n", encoding="utf-8")
    return artifact, config, proof, dependency_lock


def plan(tmp_path: Path, *, blocked: str | None = None) -> BakeoffPlan:
    artifact, config, proof, dependency_lock = write_inputs(tmp_path)
    claims = {
        name: ClaimSpec(proof, (f"{name} assertion passed",))
        for name in CLAIMS
    }
    commands = {
        scenario: CommandSpec(
            (
                sys.executable,
                "-I",
                "-c",
                (
                    "import json,sys; print(json.dumps("
                    + repr(
                        {
                            "schema": "agentnet.component-bakeoff-scenario-result.v1",
                            "scenario": scenario,
                            "status": "passed",
                            "assertions": [
                                {
                                    "assertion_id": __import__("hashlib").sha256(
                                        f"{scenario} assertion passed".encode()
                                    ).hexdigest(),
                                    "status": "passed",
                                }
                            ],
                        }
                    )
                    + ")); sys.exit(7 if "
                    + repr(scenario)
                    + " == "
                    + repr(str(blocked))
                    + " else 0)"
                ),
            ),
            (f"{scenario} assertion passed",),
            10,
        )
        for scenario in SCENARIOS
    }
    return BakeoffPlan(
        run_id="synthetic-run-0001",
        artifact_path=artifact,
        config_path=config,
        dependency_lock_path=dependency_lock,
        working_directory=tmp_path,
        environment={"AGENTNET_BAKEOFF_TEST": "1"},
        license_spdx="Apache-2.0",
        provenance_source="https://example.test/synthetic-component",
        egress_mode="none",
        claims=claims,
        commands=commands,
    )


def sandbox(root: Path, *, egress_mode: str = "none"):
    if egress_mode != "none":
        raise ValueError("test helper only composes the production networkless profile")
    root.mkdir(parents=True, exist_ok=True)
    root.chmod(0o700)
    launcher = Path("/usr/bin/bwrap")
    digest = __import__("hashlib").sha256(launcher.read_bytes()).hexdigest()
    config_dir = root / "config"
    config_dir.mkdir(exist_ok=True)
    config_dir.chmod(0o700)
    config = config_dir / "bakeoff-sandbox.json"
    config.write_text(
        json.dumps(
            {
                "schema": "agentnet.component-bakeoff-sandbox.v1",
                "bwrap_sha256": digest,
                "evidence_sha256": SHA,
            },
            sort_keys=True,
            separators=(",", ":"),
        )
        + "\n"
    )
    config.chmod(0o600)
    return load_bakeoff_sandbox(root)


@pytest.fixture(autouse=True)
def simulate_pinned_bwrap_execution(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setattr(
        ConfiguredBakeoffSandbox,
        "wrap",
        lambda self, command, *, workdir, environment=None: tuple(command),
    )


def create_evidence_package(component_record, bakeoff_plan, output):
    return _create_evidence_package(
        component_record,
        bakeoff_plan,
        output,
        sandbox=sandbox(output.parent),
    )


def test_exact_digest_bound_component_evidence_is_adoption_ready(tmp_path: Path) -> None:
    value = evidence()
    attestation, trust, now = reviewed(value, P256KeyPair.generate(), tmp_path)
    assert adoption_ready(
        component(),
        value,
        reviewer_attestation=attestation,
        reviewer_trust=trust,
        now=now,
    ) == (True, [])


@pytest.mark.parametrize(
    ("field", "replacement"),
    [
        ("component", "other-component"),
        ("version", "9.9.9"),
        ("evidence_package_sha256", "not-a-digest"),
        ("artifact_sha256", "not-a-digest"),
        ("commands", {}),
        ("failure", {}),
        (
            "upgrade",
            {"status": "blocked", "evidence_sha256": SHA, "assertions": assertions("blocked")},
        ),
        (
            "data_egress",
            {
                "reviewed": False,
                "mode": "none",
                "evidence_sha256": SHA,
                "assertions": assertions("not reviewed"),
            },
        ),
        (
            "self_hosted",
            {"verified": False, "evidence_sha256": SHA, "assertions": []},
        ),
    ],
)
def test_absent_empty_blocked_or_substituted_evidence_never_counts_as_pass(
    field: str,
    replacement: object,
) -> None:
    value = deepcopy(evidence())
    value[field] = replacement
    if field != "evidence_package_sha256":
        value["evidence_package_sha256"] = evidence_package_digest(value)
    ready, reasons = adoption_ready(component(), value)
    assert ready is False
    assert reasons


def test_fixed_schema_rejects_unreviewed_extra_fields() -> None:
    value = evidence()
    value["unreviewed_note"] = "looks fine"
    value["evidence_package_sha256"] = evidence_package_digest(value)
    ready, reasons = adoption_ready(component(), value)
    assert ready is False
    assert "evidence.extra=unreviewed_note" in reasons


def test_unapproved_component_decision_cannot_be_overridden_by_green_evidence() -> None:
    ready, reasons = adoption_ready(component(decision="not_available"), evidence())
    assert ready is False
    assert "decision=not_available" in reasons


def test_runner_builds_read_only_package_and_validator_recomputes_every_binding(
    tmp_path: Path,
) -> None:
    output = tmp_path / "package"
    generated = create_evidence_package(component(), plan(tmp_path), output)

    assert generated["schema"] == EVIDENCE_SCHEMA
    assert generated["evidence_package_sha256"] == evidence_package_digest(generated)
    assert output.is_dir()
    assert output.stat().st_mode & stat.S_IWUSR == 0
    assert all(path.stat().st_mode & stat.S_IWUSR == 0 for path in output.rglob("*") if path.is_file())
    assert generated["review_status"] == "observed_unreviewed"
    assert adoption_ready(component(), generated)[0] is False
    key = P256KeyPair.generate()
    attestation, trust, now = reviewed(generated, key, tmp_path)
    assert validate_evidence_package(
        component(), output,
        reviewer_attestation=attestation,
        reviewer_trust=trust,
        now=now,
    ) == (True, [])
    assert not list((output / "commands").iterdir())


@pytest.mark.parametrize(
    ("field", "expected_reason"),
    [
        ("component", "reproducibility.plan_component_binding"),
        ("version", "reproducibility.plan_version_binding"),
        ("run_id", "reproducibility.plan_run_id_binding"),
        ("environment", "reproducibility.plan_environment_binding"),
        ("claim_evidence", "reproducibility.plan_claim_binding"),
        ("claim_assertion", "reproducibility.plan_claim_binding"),
        ("command_assertion", "reproducibility.plan_command_binding"),
        ("command_executable", "reproducibility.plan_command_binding"),
        ("working_directory", "reproducibility.plan_command_binding"),
        ("license", "reproducibility.plan_license_binding"),
        ("provenance", "reproducibility.plan_provenance_binding"),
        ("egress", "reproducibility.plan_egress_binding"),
        ("timeout", "reproducibility.plan_command_binding"),
        ("repro_executable", "reproducibility.executables.failure_binding"),
    ],
)
def test_valid_reviewer_cannot_bless_internally_inconsistent_plan_bindings(
    tmp_path: Path,
    field: str,
    expected_reason: str,
) -> None:
    inputs = tmp_path / f"inputs-{field}"
    inputs.mkdir()
    output = tmp_path / f"package-{field}"
    generated = create_evidence_package(component(), plan(inputs), output)
    manifest_path = output / "manifest.json"
    plan_path = output / "inputs" / "plan.json"
    manifest = deepcopy(generated)
    plan_snapshot = json.loads(plan_path.read_text(encoding="utf-8"))

    if field in {"component", "version", "run_id"}:
        plan_snapshot[field] = f"tampered-{field}"
    elif field == "environment":
        plan_snapshot["environment"]["AGENTNET_BAKEOFF_TEST"] = "b" * 64
    elif field == "claim_evidence":
        plan_snapshot["claims"]["license"]["evidence_sha256"] = "b" * 64
    elif field == "claim_assertion":
        plan_snapshot["claims"]["license"]["assertion_ids"] = ["b" * 64]
    elif field == "command_assertion":
        plan_snapshot["commands"]["failure"]["assertion_ids"] = ["b" * 64]
    elif field == "command_executable":
        plan_snapshot["commands"]["failure"]["executable_sha256"] = "b" * 64
    elif field == "working_directory":
        plan_snapshot["working_directory_sha256"] = "b" * 64
    elif field == "license":
        plan_snapshot["license_spdx_sha256"] = "b" * 64
    elif field == "provenance":
        plan_snapshot["provenance_source_sha256"] = "b" * 64
    elif field == "egress":
        plan_snapshot["egress_mode"] = "explicit_allowlist"
    elif field == "timeout":
        plan_snapshot["commands"]["failure"]["timeout_seconds"] = 301
    elif field == "repro_executable":
        manifest["reproducibility"]["executables"]["failure"] = "b" * 64
    else:  # pragma: no cover - parameter table is closed
        raise AssertionError(field)

    if field != "repro_executable":
        plan_bytes = (
            json.dumps(plan_snapshot, sort_keys=True, separators=(",", ":")) + "\n"
        ).encode("utf-8")
        plan_path.chmod(0o600)
        plan_path.write_bytes(plan_bytes)
        plan_path.chmod(0o400)
        manifest["reproducibility"]["plan_sha256"] = hashlib.sha256(plan_bytes).hexdigest()

    manifest["evidence_package_sha256"] = evidence_package_digest(manifest)
    manifest_path.chmod(0o600)
    manifest_path.write_text(
        json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )
    manifest_path.chmod(0o400)
    attestation, trust, now = reviewed(
        manifest,
        P256KeyPair.generate(),
        tmp_path / f"review-{field}",
    )
    ready, reasons = validate_evidence_package(
        component(),
        output,
        reviewer_attestation=attestation,
        reviewer_trust=trust,
        now=now,
    )
    assert ready is False
    assert expected_reason in reasons


@pytest.mark.parametrize(
    ("relative_path", "replacement"),
    [
        ("inputs/artifact.bin", b"different artifact\n"),
        ("manifest.json", b"forged result\n"),
        ("inputs/environment.json", b'{"schema":"forged","values":{}}\n'),
    ],
)
def test_tampered_package_file_fails_closed(
    tmp_path: Path,
    relative_path: str,
    replacement: bytes,
) -> None:
    output = tmp_path / "package"
    create_evidence_package(component(), plan(tmp_path), output)
    target = output / relative_path
    target.chmod(0o600)
    target.write_bytes(replacement)

    ready, reasons = validate_evidence_package(component(), output)
    assert ready is False
    assert reasons


def test_tampered_manifest_cannot_rebind_component_or_reseal_itself(tmp_path: Path) -> None:
    output = tmp_path / "package"
    create_evidence_package(component(), plan(tmp_path), output)
    manifest_path = output / "manifest.json"
    manifest_path.chmod(0o600)
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["version"] = "9.9.9"
    manifest_path.write_text(
        json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )

    ready, reasons = validate_evidence_package(component(), output)
    assert ready is False
    assert "version_binding" in reasons
    assert "evidence_package_sha256.binding" in reasons


@pytest.mark.parametrize("decision", ["deferred", "rejected", "not_available"])
def test_only_accepted_phase0_registry_records_can_validate_packages(
    tmp_path: Path,
    decision: str,
) -> None:
    output = tmp_path / "package"
    create_evidence_package(component(), plan(tmp_path), output)

    ready, reasons = validate_evidence_package(component(decision=decision), output)
    assert ready is False
    assert f"decision={decision}" in reasons


def test_runner_does_not_publish_blocked_command_results(tmp_path: Path) -> None:
    output = tmp_path / "package"
    with pytest.raises(EvidencePackageError, match="blocked or failed"):
        create_evidence_package(component(), plan(tmp_path, blocked="offline"), output)
    assert not output.exists()


def test_runner_rejects_nonaccepted_record_before_executing(tmp_path: Path) -> None:
    output = tmp_path / "package"
    with pytest.raises(EvidencePackageError, match="not accepted_phase0"):
        create_evidence_package(component(decision="deferred"), plan(tmp_path), output)
    assert not output.exists()


def test_plan_cannot_self_certify_without_independent_reviewer_signature(tmp_path: Path) -> None:
    output = tmp_path / "package"
    generated = create_evidence_package(component(), plan(tmp_path), output)
    ready, reasons = adoption_ready(component(), generated)
    assert ready is False
    assert reasons == ["reviewer_attestation.missing"]
    assert generated["review_status"] == "observed_unreviewed"
    assert all(generated[name]["status"] == "observed_unreviewed" for name in SCENARIOS)
    assert all(
        assertion["passed"] is False
        for name in SCENARIOS
        for assertion in generated[name]["assertions"]
    )


def test_reviewer_signature_substitution_and_expiry_fail_closed(tmp_path: Path) -> None:
    generated = create_evidence_package(component(), plan(tmp_path), tmp_path / "package")
    trusted = P256KeyPair.generate()
    attacker = P256KeyPair.generate()
    attestation, _attacker_trust, now = reviewed(generated, attacker, tmp_path)
    _trusted_attestation, trust, _ = reviewed(generated, trusted, tmp_path / "trusted", key_id="reviewer-1")
    ready, reasons = adoption_ready(
        component(), generated,
        reviewer_attestation=attestation,
        reviewer_trust=trust,
        now=now,
    )
    assert ready is False
    assert "reviewer_attestation.threshold" in reasons

    expired, trust, _ = reviewed(
        generated,
        trusted,
        tmp_path,
        now=2_000_000_000,
        expires_at=1_999_999_999,
    )
    ready, reasons = adoption_ready(
        component(), generated,
        reviewer_attestation=expired,
        reviewer_trust=trust,
        now=2_000_000_000,
    )
    assert ready is False
    assert "reviewer_attestation.expired" in reasons


def test_expired_reviewer_endorsement_root_fails_before_signature_counting(tmp_path: Path) -> None:
    generated = create_evidence_package(component(), plan(tmp_path), tmp_path / "package")
    now = 2_000_000_000
    with pytest.raises(EvidencePackageError, match="endorsement trust root is expired"):
        reviewed(
            generated,
            P256KeyPair.generate(),
            tmp_path / "review",
            now=now,
            endorsement_root_expires_at=now,
        )


def test_caller_constructed_reviewer_mapping_cannot_self_approve(tmp_path: Path) -> None:
    generated = create_evidence_package(component(), plan(tmp_path), tmp_path / "package")
    attacker = P256KeyPair.generate()
    body = reviewer_attestation_body(
        generated,
        reviewed_at=2_000_000_000,
        expires_at=2_000_003_600,
    )
    forged_root = ConfiguredReviewerTrustRoot(
        root_id="caller-self-approved",
        expires_at=2_000_007_200,
        keys={"attacker": attacker.public_pem},
        threshold=1,
        components=frozenset({"synthetic-component"}),
        versions=frozenset({"1.2.3"}),
        claims=frozenset(),
        profiles=frozenset({"bwrap-unshare-all-networkless-v1"}),
        source_digest=SHA,
        _seal=object(),
    )
    ready, reasons = adoption_ready(
        component(),
        generated,
        reviewer_attestation={
            **body,
            "signatures": [
                {
                    "key_id": "attacker",
                    "signature": attacker.sign(REVIEW_ATTESTATION_PURPOSE, body),
                }
            ],
        },
        reviewer_trust=forged_root,
        now=2_000_000_001,
    )
    assert ready is False
    assert "reviewer_attestation.trust_root" in reasons


def test_reviewer_attestation_binds_reproducible_plan_and_sandbox_claims(tmp_path: Path) -> None:
    generated = create_evidence_package(component(), plan(tmp_path), tmp_path / "package")
    reviewer = P256KeyPair.generate()
    attestation, trust, now = reviewed(generated, reviewer, tmp_path)
    substituted = deepcopy(generated)
    substituted["reproducibility"]["plan_sha256"] = "b" * 64
    substituted["evidence_package_sha256"] = evidence_package_digest(substituted)
    ready, reasons = adoption_ready(
        component(),
        substituted,
        reviewer_attestation=attestation,
        reviewer_trust=trust,
        now=now,
    )
    assert ready is False
    assert "reviewer_attestation.binding" in reasons


def test_reviewer_threshold_and_profile_scope_are_both_mandatory(tmp_path: Path) -> None:
    generated = create_evidence_package(component(), plan(tmp_path), tmp_path / "package")
    attestation, trust, now = reviewed(generated, P256KeyPair.generate(), tmp_path)
    one_signature = {**attestation, "signatures": attestation["signatures"][:1]}
    ready, reasons = adoption_ready(
        component(), generated,
        reviewer_attestation=one_signature,
        reviewer_trust=trust,
        now=now,
    )
    assert ready is False
    assert "reviewer_attestation.threshold" in reasons

    scoped_attestation, wrong_scope, now = reviewed(
        generated,
        P256KeyPair.generate(),
        tmp_path / "wrong-scope",
        profile_scope="different-sandbox-profile",
    )
    ready, reasons = adoption_ready(
        component(), generated,
        reviewer_attestation=scoped_attestation,
        reviewer_trust=wrong_scope,
        now=now,
    )
    assert ready is False
    assert "reviewer_attestation.scope" in reasons


def test_secret_like_environment_names_and_oversize_output_are_rejected(tmp_path: Path) -> None:
    secret_root = tmp_path / "secret"
    secret_root.mkdir()
    secret_plan = replace(plan(secret_root), environment={"API_TOKEN": "must-not-persist"})
    with pytest.raises(EvidencePackageError, match="secret-like environment"):
        create_evidence_package(component(), secret_plan, tmp_path / "secret-package")

    noisy_root = tmp_path / "noisy"
    noisy_root.mkdir()
    noisy = plan(noisy_root)
    commands = dict(noisy.commands)
    commands["failure"] = CommandSpec(
        (sys.executable, "-I", "-c", "import sys; sys.stdout.write('x' * 1100000)"),
        ("untrusted noisy assertion",),
        10,
    )
    with pytest.raises(EvidencePackageError, match="bounded output cap"):
        create_evidence_package(
            component(), replace(noisy, commands=commands), tmp_path / "noisy-package"
        )
    assert not (tmp_path / "noisy-package").exists()


def test_bounded_raw_command_output_is_hashed_but_never_persisted(tmp_path: Path) -> None:
    marker = b"RAW-OUTPUT-SECRET-MUST-NOT-PERSIST"
    observed = plan(tmp_path)
    commands = dict(observed.commands)
    output_program = f"print(bytes({list(marker)!r}).decode())"
    commands["failure"] = CommandSpec(
        (sys.executable, "-I", "-c", output_program),
        ("unreviewed observation",),
        10,
    )
    output = tmp_path / "package"
    with pytest.raises(EvidencePackageError, match="structured JSON"):
        create_evidence_package(component(), replace(observed, commands=commands), output)
    assert not output.exists()


def test_secret_config_is_content_free_digest_only_and_never_packaged(tmp_path: Path) -> None:
    marker = b"CLIENT_SECRET=raw-config-secret"
    observed = plan(tmp_path)
    observed.config_path.write_bytes(marker)
    output = tmp_path / "package"
    generated = create_evidence_package(component(), observed, output)
    assert generated["config_sha256"] == __import__("hashlib").sha256(marker).hexdigest()
    assert not (output / "inputs" / "config.bin").exists()
    assert all(marker not in path.read_bytes() for path in output.rglob("*") if path.is_file())


def test_egress_mode_and_sandbox_launcher_substitution_fail_closed(tmp_path: Path) -> None:
    observed = replace(plan(tmp_path), egress_mode="explicit_allowlist")
    with pytest.raises(EvidencePackageError, match="egress mode is not enforced"):
        _create_evidence_package(
            component(), observed, tmp_path / "mismatch", sandbox=sandbox(tmp_path, egress_mode="none")
        )

    sandbox(tmp_path)
    config = tmp_path / "config" / "bakeoff-sandbox.json"
    value = json.loads(config.read_text())
    value["bwrap_sha256"] = "b" * 64
    config.write_text(json.dumps(value) + "\n")
    with pytest.raises(EvidencePackageError, match="sandbox runtime config is invalid"):
        load_bakeoff_sandbox(tmp_path)


def test_production_bwrap_profile_code_constructs_network_isolation_and_tcp_probe_fails(
    tmp_path: Path,
) -> None:
    boundary = sandbox(tmp_path)
    listener = None
    try:
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)
        host, port = "127.0.0.1", listener.getsockname()[1]
    except PermissionError:
        host, port = "1.1.1.1", 53
    command = (
        sys.executable,
        "-I",
        "-c",
        f"import socket; s=socket.create_connection(({host!r},{port}),1); s.close()",
    )
    wrapped = PRODUCTION_SANDBOX_WRAP(boundary, command, workdir=str(tmp_path))
    assert "--unshare-all" in wrapped
    assert "--unshare-net" in wrapped
    assert "--bind" not in wrapped
    assert ("--ro-bind", str(tmp_path), str(tmp_path)) in tuple(
        wrapped[index : index + 3] for index in range(len(wrapped) - 2)
    )
    completed = subprocess.run(wrapped, capture_output=True, timeout=5)
    if listener is not None:
        listener.close()
    assert completed.returncode != 0


def test_plan_environment_is_applied_only_after_bwrap_starts(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    boundary = sandbox(tmp_path)
    child_environment = {"AGENTNET_BAKEOFF_TEST": "1", "LD_PRELOAD": "/tmp/child-only.so"}
    wrapped = PRODUCTION_SANDBOX_WRAP(
        boundary,
        (sys.executable, "-I", "-c", "pass"),
        workdir=str(tmp_path),
        environment=child_environment,
    )
    triples = tuple(wrapped[index : index + 3] for index in range(len(wrapped) - 2))
    assert ("--setenv", "AGENTNET_BAKEOFF_TEST", "1") in triples
    assert ("--setenv", "LD_PRELOAD", "/tmp/child-only.so") in triples
    assert "--clearenv" in wrapped

    outer_environments: list[dict[str, str]] = []
    original_popen = subprocess.Popen

    def observed_popen(*args, **kwargs):
        outer_environments.append(dict(kwargs["env"]))
        return original_popen(*args, **kwargs)

    monkeypatch.setattr(subprocess, "Popen", observed_popen)
    observed_plan = replace(plan(tmp_path), environment=child_environment)
    create_evidence_package(component(), observed_plan, tmp_path / "environment-package")
    assert outer_environments
    assert all(environment == {
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "PATH": "/usr/bin:/bin",
    } for environment in outer_environments)


def test_stalled_sandbox_process_group_is_killed_at_command_deadline(tmp_path: Path) -> None:
    observed = plan(tmp_path)
    commands = dict(observed.commands)
    commands["failure"] = CommandSpec(
        (sys.executable, "-I", "-c", "import time; time.sleep(30)"),
        ("must never self-certify",),
        1,
    )
    started = time.monotonic()
    with pytest.raises(EvidencePackageError, match="timed out"):
        create_evidence_package(
            component(), replace(observed, commands=commands), tmp_path / "stalled"
        )
    assert time.monotonic() - started < 4
    assert not (tmp_path / "stalled").exists()


def test_structured_scenario_schema_and_assertion_ids_are_exact(tmp_path: Path) -> None:
    observed = plan(tmp_path)
    commands = dict(observed.commands)
    commands["failure"] = CommandSpec(
        (sys.executable, "-I", "-c", "import json; print(json.dumps({'status':'passed'}))"),
        observed.commands["failure"].assertions,
        10,
    )
    with pytest.raises(EvidencePackageError, match="schema/assertion binding"):
        create_evidence_package(
            component(), replace(observed, commands=commands), tmp_path / "malformed"
        )


def test_sensitive_argv_cwd_assertions_and_provenance_are_hash_only(tmp_path: Path) -> None:
    marker = "RAW_PASSWORD_TOKEN_MUST_NOT_PERSIST"
    workdir = tmp_path / marker
    workdir.mkdir()
    observed = plan(workdir)
    commands = {
        name: replace(spec, argv=(*spec.argv, marker))
        for name, spec in observed.commands.items()
    }
    claims = dict(observed.claims)
    claims["license"] = replace(claims["license"], assertions=(marker,))
    observed = replace(
        observed,
        commands=commands,
        claims=claims,
        provenance_source=f"https://example.test/{marker}",
    )
    output = tmp_path / "hashed-only-package"
    create_evidence_package(component(), observed, output)
    marker_bytes = marker.encode()
    assert all(marker_bytes not in path.read_bytes() for path in output.rglob("*") if path.is_file())


def test_registry_facts_are_exact_and_do_not_claim_local_postgres_ha() -> None:
    postgres = next(item for item in BASELINE_COMPONENTS if item.name == "PostgreSQL")
    mcp = next(item for item in BASELINE_COMPONENTS if item.name == "MCP Python SDK")
    assert postgres.version == "18.4 local; non-HA"
    assert postgres.decision == "not_available"
    assert "not HA/PITR evidence" in postgres.policy_boundary
    assert mcp.version == "1.28.1"
    assert mcp.decision == "accepted_phase0"


def test_executable_runner_and_validator_use_fixed_plan_and_registry_record(
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    artifact, config, proof, dependency_lock = write_inputs(tmp_path)
    scenario_commands = {
        scenario: {
            "argv": [
                sys.executable,
                "-I",
                "-c",
                "import json; print(json.dumps(" + repr(
                    {
                        "schema": "agentnet.component-bakeoff-scenario-result.v1",
                        "scenario": scenario,
                        "status": "passed",
                        "assertions": [
                            {
                                "assertion_id": __import__("hashlib").sha256(
                                    f"{scenario} assertion passed".encode()
                                ).hexdigest(),
                                "status": "passed",
                            }
                        ],
                    }
                ) + "))",
            ],
            "assertions": [f"{scenario} assertion passed"],
            "timeout_seconds": 10,
        }
        for scenario in SCENARIOS
    }
    plan_path = tmp_path / "bakeoff-plan.json"
    plan_path.write_text(
        json.dumps(
            {
                "schema": PLAN_SCHEMA,
                "component": "MCP Python SDK",
                "version": "1.28.1",
                "run_id": "mcp-sdk-run-0001",
                "artifact_path": str(artifact),
                "config_path": str(config),
                "dependency_lock_path": str(dependency_lock),
                "working_directory": str(tmp_path),
                "environment": {"AGENTNET_BAKEOFF_TEST": "1"},
                "license": {
                    "spdx": "MIT",
                    "evidence_path": str(proof),
                    "assertions": ["license assertion passed"],
                },
                "provenance": {
                    "source": "https://github.com/modelcontextprotocol/python-sdk",
                    "evidence_path": str(proof),
                    "assertions": ["provenance assertion passed"],
                },
                "self_hosted": {
                    "evidence_path": str(proof),
                    "assertions": ["self-hosted assertion passed"],
                },
                "data_egress": {
                    "mode": "none",
                    "evidence_path": str(proof),
                    "assertions": ["egress assertion passed"],
                },
                "commands": scenario_commands,
            },
            sort_keys=True,
        )
        + "\n",
        encoding="utf-8",
    )
    output = tmp_path / "cli-package"
    sandbox(tmp_path)

    assert main(
        [
            "run",
            "--component",
            "MCP Python SDK",
            "--plan",
            str(plan_path),
            "--output",
            str(output),
            "--install-root",
            str(tmp_path),
        ]
    ) == 0
    assert len(capsys.readouterr().out.strip()) == 64
    assert main(
        [
            "validate",
            "--component",
            "MCP Python SDK",
            "--package",
            str(output),
        ]
    ) == 0
    assert capsys.readouterr().out.strip() == "OBSERVED_UNREVIEWED"
    with pytest.raises(SystemExit):
        main(
            [
                "validate", "--component", "MCP Python SDK", "--package", str(output),
                "--reviewer-trust", str(tmp_path / "caller-root.json"),
            ]
        )
