from __future__ import annotations

import base64
import csv
import hashlib
import io
import json
import shutil
import tarfile
import zipfile

from pathlib import Path
from typing import Any, Callable

import pytest

from scripts.verify_release import ROOT, _expected_sdist_files, verify


SOURCE_FILES = (
    "docs/specification.md",
    "docs/requirements.md",
    "docs/final-verification.md",
)


def _manifest_errors(
    tmp_path: Path,
    mutate: Callable[[dict[str, Any]], None],
) -> list[str]:
    manifest = json.loads((ROOT / "RELEASE_MANIFEST.json").read_text(encoding="utf-8"))
    mutate(manifest)
    path = tmp_path / "manifest.json"
    path.write_text(json.dumps(manifest), encoding="utf-8")
    return verify(root=ROOT, manifest_path=path)


def _copy_release_inputs(tmp_path: Path) -> Path:
    root = tmp_path / "agentnet"
    root.mkdir()
    for filename in ("LICENSE", "RELEASE_MANIFEST.json", "pyproject.toml", "uv.lock"):
        shutil.copy2(ROOT / filename, root / filename)
    shutil.copytree(ROOT / "schemas", root / "schemas")
    shutil.copytree(ROOT / "src", root / "src")
    (root / "scripts").mkdir()
    shutil.copy2(ROOT / "scripts/export_schemas.py", root / "scripts/export_schemas.py")
    (root / "docs").mkdir()
    for relative_path in (
        "README.md",
        "REQUIREMENTS_STATUS.md",
        "deploy/Dockerfile",
        "deploy/compose.production.json",
        "deploy/nginx-agent.conf",
        "deploy/render_and_run.py",
        "docs/GATE_EVIDENCE.md",
        "docs/RELEASE_MANIFEST.md",
        "evidence/gates/G01/2026-07-13-installed-harnesses/manifest.json",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/REVIEW.md",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/compatibility.html",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/compatibility.json",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/junitreport.xml",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/manifest.json",
        "evidence/gates/G04/2026-07-13-alpha2-http-json/tck_report.html",
        "evidence/gates/G09/2026-07-13-postgresql-18.4-local/manifest.json",
        "evidence/local/2026-07-13-final/manifest.json",
        "evidence/local/2026-07-15-v0.1.6/manifest.json",
        "evidence/local/2026-07-15-v0.1.7/manifest.json",
        "evidence/local/2026-07-20-v0.1.18/manifest.json",
        "evidence/local/2026-07-22-v0.1.19/manifest.json",
        "evidence/local/2026-07-29-v0.1.32/manifest.json",
        "scripts/verify_release.py",
    ):
        target = root / relative_path
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(ROOT / relative_path, target)
    for filename in SOURCE_FILES:
        shutil.copy2(ROOT / filename, root / filename)
    shutil.copytree(
        ROOT / "evidence/local/2026-07-13-final/artifacts",
        root / "evidence/local/2026-07-13-final/artifacts",
    )
    shutil.copytree(
        ROOT / "evidence/local/2026-07-29-v0.1.32/artifacts",
        root / "evidence/local/2026-07-29-v0.1.32/artifacts",
    )
    return root


def _refresh_artifact_hash(root: Path, artifact_path: Path) -> None:
    evidence_path = root / "evidence/local/2026-07-29-v0.1.32/manifest.json"
    evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    relative = artifact_path.relative_to(root).as_posix()
    record = next(item for item in evidence["artifacts"] if item["path"] == relative)
    record["sha256"] = hashlib.sha256(artifact_path.read_bytes()).hexdigest()
    evidence_path.write_text(json.dumps(evidence), encoding="utf-8")


def _record_digest(payload: bytes) -> str:
    value = base64.urlsafe_b64encode(hashlib.sha256(payload).digest()).rstrip(b"=")
    return f"sha256={value.decode('ascii')}"


def _rewrite_wheel(
    path: Path,
    mutate: Callable[[dict[str, bytes]], None],
) -> None:
    with zipfile.ZipFile(path) as archive:
        payloads = {name: archive.read(name) for name in archive.namelist()}
    record_name = next(name for name in payloads if name.endswith(".dist-info/RECORD"))
    mutate(payloads)
    record_rows = [
        (name, _record_digest(payload), str(len(payload)))
        for name, payload in sorted(payloads.items())
        if name != record_name
    ]
    record_rows.append((record_name, "", ""))
    output = io.StringIO(newline="")
    csv.writer(output, lineterminator="\n").writerows(record_rows)
    payloads[record_name] = output.getvalue().encode("utf-8")
    with zipfile.ZipFile(path, mode="w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name, payload in sorted(payloads.items()):
            info = zipfile.ZipInfo(name, date_time=(2020, 2, 2, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (0o100644 << 16)
            info.compress_type = zipfile.ZIP_DEFLATED
            archive.writestr(info, payload)


def _rewrite_sdist(
    path: Path,
    mutate: Callable[[list[tuple[tarfile.TarInfo, bytes]]], None],
) -> None:
    with tarfile.open(path, mode="r:gz") as archive:
        entries = []
        for member in archive.getmembers():
            extracted = archive.extractfile(member)
            entries.append((member, extracted.read() if extracted is not None else b""))
    mutate(entries)
    with tarfile.open(path, mode="w:gz", format=tarfile.PAX_FORMAT) as archive:
        for member, payload in entries:
            archive.addfile(member, io.BytesIO(payload) if member.isfile() else None)


def test_release_manifest_matches_current_reproducible_inputs() -> None:
    assert verify() == []


def test_candidate_package_evidence_cannot_remain_pending(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    evidence_path = root / "evidence/local/2026-07-29-v0.1.32/manifest.json"
    evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    evidence["verification_status"] = "PENDING"
    next(
        record for record in evidence["commands"]
        if record["command"] == "npm run check"
    )["result"] = "PENDING"
    evidence_path.write_text(json.dumps(evidence), encoding="utf-8")

    failures = verify(root=root)

    assert "0.1.32 package evidence must record completed PASS verification" in failures
    assert "0.1.32 package evidence cannot retain pending command results" in failures


def test_candidate_evidence_must_cover_every_0132_release_blocker_surface(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    evidence_path = root / "evidence/local/2026-07-29-v0.1.32/manifest.json"
    evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    focused = next(
        record
        for record in evidence["commands"]
        if "tests/supervisor/test_c0_pilot_responder.py" in record["command"]
    )
    focused["command"] = focused["command"].replace(
        " tests/supervisor/test_c0_pilot_responder.py",
        "",
    )
    evidence_path.write_text(json.dumps(evidence), encoding="utf-8")

    failures = verify(root=root)

    assert "0.1.32 focused release-blocker evidence is incomplete" in failures


def test_attacker_consistent_stale_public_readme_is_rejected(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    readme_path = root / "README.md"
    readme_path.write_text(
        readme_path.read_text(encoding="utf-8").replace(
            "latest published package is\n`0.1.31`",
            "latest published package is\n`0.1.29`",
            1,
        ),
        encoding="utf-8",
    )
    manifest_path = root / "RELEASE_MANIFEST.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["release_inputs"]["README.md"]["sha256"] = hashlib.sha256(
        readme_path.read_bytes()
    ).hexdigest()
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    failures = verify(root=root)

    assert any("public README release status is stale or incomplete" in item for item in failures)
    assert any("public README retains stale release status" in item for item in failures)


def test_sdist_contract_does_not_require_installed_root_ignore_files(tmp_path: Path) -> None:
    assert not (tmp_path / ".gitignore").exists()
    assert not (tmp_path / ".npmignore").exists()
    expected = _expected_sdist_files(tmp_path, {})
    assert expected[".gitignore"].endswith(b".coverage\n\n")


def test_release_verifier_requires_candidate_artifact_ignore_policy(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    (root / ".git").mkdir()
    (root / "evidence/local/2026-07-29-v0.1.32/artifacts/.gitignore").unlink(
        missing_ok=True
    )

    failures = verify(root=root, manifest_path=root / "RELEASE_MANIFEST.json")

    assert "final package artifact ignore policy does not retain its archives" in failures


def test_release_verifier_accepts_npm_install_without_git_ignore_metadata(
    tmp_path: Path,
) -> None:
    root = _copy_release_inputs(tmp_path)
    (root / "evidence/local/2026-07-29-v0.1.32/artifacts/.gitignore").unlink(
        missing_ok=True
    )

    failures = verify(root=root, manifest_path=root / "RELEASE_MANIFEST.json")

    assert "final package artifact ignore policy does not retain its archives" not in failures


@pytest.mark.parametrize(
    ("mutate", "expected"),
    [
        (
            lambda manifest: manifest["authoritative_sources"]["concept"].update(
                sha256="0" * 64
            ),
            "wrong authoritative source hash",
        ),
        (
            lambda manifest: manifest["schema_catalog"]["schemas"].pop("actor"),
            "schema catalog is missing",
        ),
        (
            lambda manifest: manifest["release"].update(production_ready=True),
            "false production claim",
        ),
        (
            lambda manifest: manifest["external_release_evidence"]["sbom"].update(
                status="PASSED", passed=True
            ),
            "false external supply-chain claim",
        ),
        (
            lambda manifest: manifest["protocols"]["a2a"].update(wire_version="0.3"),
            "protocol pins drifted",
        ),
    ],
)
def test_manifest_claim_and_catalog_mutations_fail_closed(
    tmp_path: Path,
    mutate: Callable[[dict[str, Any]], None],
    expected: str,
) -> None:
    assert any(expected in failure for failure in _manifest_errors(tmp_path, mutate))


def test_unpinned_direct_dependency_and_project_drift_fail_closed(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    pyproject = root / "pyproject.toml"
    pyproject.write_text(
        pyproject.read_text(encoding="utf-8").replace(
            '"mcp==1.28.1"',
            '"mcp>=1.28.1"',
        ),
        encoding="utf-8",
    )
    failures = verify(root=root)
    assert any("unpinned direct dependency" in failure for failure in failures)
    assert any("pyproject.toml" in failure and "drifted" in failure for failure in failures)


def test_lock_source_and_schema_byte_drift_fail_closed(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    (root / "uv.lock").write_text(
        (root / "uv.lock").read_text(encoding="utf-8") + "\n",
        encoding="utf-8",
    )
    (root / SOURCE_FILES[0]).write_text(
        (root / SOURCE_FILES[0]).read_text(encoding="utf-8") + "\n",
        encoding="utf-8",
    )
    (root / "schemas/v1/actor.json").unlink()
    failures = verify(root=root)
    assert any("uv.lock" in failure and "drifted" in failure for failure in failures)
    assert any("authoritative source bytes drifted" in failure for failure in failures)
    assert any("schemas/v1 directory is missing" in failure for failure in failures)
    assert any("schema file is missing" in failure for failure in failures)


def test_deployment_ledger_and_human_manifest_drift_fail_closed(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    (root / "deploy/Dockerfile").write_text("FROM scratch\n", encoding="utf-8")
    (root / "REQUIREMENTS_STATUS.md").write_text("# missing ledger\n", encoding="utf-8")
    human = root / "docs/RELEASE_MANIFEST.md"
    human.write_text(
        human.read_text(encoding="utf-8")
        .replace(
            "| G04 | `FAILED` | `REVIEWED_PARTIAL` |",
            "| G04 | `PARTIAL` | `REVIEWED_PARTIAL` |",
        )
        .replace(
            "SHA-256 `58245ee0d1147734744fd7a9bef85baa18ecab9680163c6c92bf15299d2e8f85`",
            "SHA-256 `" + "0" * 64 + "`",
            1,
        ),
        encoding="utf-8",
    )

    failures = verify(root=root)

    assert any("release input drifted: deploy/Dockerfile" in failure for failure in failures)
    assert any("requirements ledger does not contain" in failure for failure in failures)
    assert any("human release manifest gate row differs: G04" in failure for failure in failures)
    assert any("human release manifest is missing required marker" in failure for failure in failures)

    summary_parent = tmp_path / "summary"
    summary_parent.mkdir()
    summary_root = _copy_release_inputs(summary_parent)
    requirements_path = summary_root / "REQUIREMENTS_STATUS.md"
    requirements_path.write_text(
        requirements_path.read_text(encoding="utf-8")
        .replace("Snapshot: 2026-07-29.", "Snapshot: 2026-07-25.", 1)
        .replace(
            "Requirement totals: **33 local-tested, 42 partial-external, 10 owner-blocked,\n"
            "0 implementation-gap = 85 unique requirements**.",
            "Requirement totals: **33 local-tested, 40 partial-external, 12 owner-blocked,\n"
            "0 implementation-gap = 85 unique requirements**.",
            1,
        ),
        encoding="utf-8",
    )
    gate_path = summary_root / "docs/GATE_EVIDENCE.md"
    gate_path.write_text(
        gate_path.read_text(encoding="utf-8").replace(
            "Current ledger update: 2026-07-29.",
            "Current ledger update: 2026-07-25.",
            1,
        ),
        encoding="utf-8",
    )
    manifest_path = summary_root / "RELEASE_MANIFEST.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for relative_path in ("REQUIREMENTS_STATUS.md", "docs/GATE_EVIDENCE.md"):
        manifest["release_inputs"][relative_path]["sha256"] = hashlib.sha256(
            (summary_root / relative_path).read_bytes()
        ).hexdigest()
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    summary_failures = verify(root=summary_root)

    assert "requirements ledger human status summary differs from its rows" in summary_failures
    assert "requirements ledger snapshot date differs from the release manifest" in summary_failures
    assert "gate evidence ledger date differs from the release manifest" in summary_failures


def test_build_backend_must_be_in_exact_locked_build_group(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    pyproject = root / "pyproject.toml"
    pyproject.write_text(
        pyproject.read_text(encoding="utf-8").replace(
            '[dependency-groups]\nbuild = [\n  "hatchling==1.28.0",\n  "editables==0.5",\n]\n',
            "",
        ),
        encoding="utf-8",
    )

    failures = verify(root=root)

    assert any("build dependency group must exactly lock" in failure for failure in failures)


def test_artifact_self_hash_cannot_replace_archive_content_validation(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    wheel = root / "evidence/local/2026-07-29-v0.1.32/artifacts/agentnet-0.1.32-py3-none-any.whl"
    wheel.write_bytes(b"not a wheel")
    evidence_path = root / "evidence/local/2026-07-29-v0.1.32/manifest.json"
    evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    for artifact in evidence["artifacts"]:
        if artifact["path"].endswith(".whl"):
            import hashlib

            artifact["sha256"] = hashlib.sha256(wheel.read_bytes()).hexdigest()
    evidence_path.write_text(json.dumps(evidence), encoding="utf-8")

    failures = verify(root=root)

    assert any("wheel is unreadable or malformed" in failure for failure in failures)


def test_full_suite_evidence_cannot_hide_failures_skips_or_xfails(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    evidence_path = root / "evidence/local/2026-07-13-final/manifest.json"
    evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    full_run = next(
        entry
        for entry in evidence["commands"]
        if "AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1" in entry["command"]
        and entry["command"].endswith((
            "uv run pytest -q",
            "uv run pytest -q -p no:cacheprovider",
            ".venv/bin/pytest -q",
            ".venv/bin/pytest -q -p no:cacheprovider",
        ))
    )
    full_run["result"] = "612 passed; 1 failed; 2 skipped; 3 xfailed"
    evidence_path.write_text(json.dumps(evidence), encoding="utf-8")

    failures = verify(root=root)

    assert any("zero failures, skips, and xfails" in failure for failure in failures)


def test_clean_install_evidence_cannot_claim_a_non_release_schema_version(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    evidence_path = root / "evidence/local/2026-07-13-final/manifest.json"
    evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
    status = next(
        entry
        for entry in evidence["commands"]
        if "/agentnet status" in entry["command"]
    )
    status["result"] = "schema version 2; ready true; accepted_local; release_certified false"
    evidence_path.write_text(json.dumps(evidence), encoding="utf-8")

    failures = verify(root=root)

    assert any("storage schema version 1" in failure for failure in failures)


def test_packaging_contract_rejects_a_coarse_sdist_allowlist(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    pyproject = root / "pyproject.toml"
    pyproject.write_text(
        pyproject.read_text(encoding="utf-8").replace(
            '  "scripts/export_schemas.py",\n  "scripts/verify_release.py",',
            '  "scripts",',
        ),
        encoding="utf-8",
    )

    failures = verify(root=root)

    assert any("sdist allowlist differs" in failure for failure in failures)


@pytest.mark.parametrize("mutation", ["extra", "wrong_entrypoint"])
def test_attacker_consistent_wheel_mutation_is_rejected(
    tmp_path: Path,
    mutation: str,
) -> None:
    root = _copy_release_inputs(tmp_path)
    wheel = root / "evidence/local/2026-07-29-v0.1.32/artifacts/agentnet-0.1.32-py3-none-any.whl"

    def mutate(payloads: dict[str, bytes]) -> None:
        if mutation == "extra":
            payloads["agentnet/unreviewed_extra.py"] = b"raise RuntimeError('not reviewed')\n"
        else:
            name = next(item for item in payloads if item.endswith(".dist-info/entry_points.txt"))
            payloads[name] = b"[console_scripts]\nagentnet = agentnet.cli:other\n"

    _rewrite_wheel(wheel, mutate)
    _refresh_artifact_hash(root, wheel)

    failures = verify(root=root)

    expected = "wheel member catalog differs" if mutation == "extra" else "console entry point"
    assert any(expected in failure for failure in failures)


def test_duplicate_wheel_member_is_rejected(tmp_path: Path) -> None:
    root = _copy_release_inputs(tmp_path)
    wheel = root / "evidence/local/2026-07-29-v0.1.32/artifacts/agentnet-0.1.32-py3-none-any.whl"
    with pytest.warns(UserWarning, match="Duplicate name"):
        with zipfile.ZipFile(wheel, mode="a", compression=zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("agentnet/__init__.py", b"duplicate\n")
    _refresh_artifact_hash(root, wheel)

    failures = verify(root=root)

    assert any("duplicate archive member" in failure for failure in failures)


@pytest.mark.parametrize("mutation", ["state", "nested_archive", "traversal", "symlink"])
def test_sdist_unsafe_or_extra_member_is_rejected(tmp_path: Path, mutation: str) -> None:
    root = _copy_release_inputs(tmp_path)
    sdist = root / "evidence/local/2026-07-29-v0.1.32/artifacts/agentnet-0.1.32.tar.gz"

    def mutate(entries: list[tuple[tarfile.TarInfo, bytes]]) -> None:
        if mutation == "symlink":
            index = next(
                i for i, (member, _payload) in enumerate(entries)
                if member.name.endswith("/src/agentnet/__init__.py")
            )
            original, _payload = entries[index]
            link = tarfile.TarInfo(original.name)
            link.type = tarfile.SYMTYPE
            link.linkname = "../../victim"
            link.mode = 0o644
            link.uid = link.gid = 0
            link.mtime = 1_580_601_600
            entries[index] = (link, b"")
            return
        suffix = {
            "state": "state.json",
            "nested_archive": "dist/nested.tar.gz",
            "traversal": "../outside",
        }[mutation]
        member = tarfile.TarInfo(f"agentnet-0.1.32/{suffix}")
        member.size = 2
        member.mode = 0o644
        member.uid = member.gid = 0
        member.mtime = 1_580_601_600
        entries.append((member, b"{}"))

    _rewrite_sdist(sdist, mutate)
    _refresh_artifact_hash(root, sdist)

    failures = verify(root=root)

    expected = "unsafe member" if mutation in {"traversal", "symlink"} else "member catalog differs"
    assert any(expected in failure for failure in failures)
