"""Executable, content-addressed component bake-off evidence packages.

The runner captures exact inputs and command results into a fixed-layout,
read-only directory.  The validator recomputes every file and package digest
before applying the component adoption gate.  A successful command is only
evidence for a registry record already marked ``accepted_phase0``; this module
does not turn candidate presence into an adoption decision.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import selectors
import signal
import shutil
import stat
import subprocess
import tempfile
import time
import platform
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from types import MappingProxyType

from agentnet.components.registry import BASELINE_COMPONENTS, ComponentRecord
from agentnet.security.signatures import verify_signature
from agentnet.security.update import UpdateTrustRoot


EVIDENCE_SCHEMA = "agentnet.component-bakeoff.v3"
PLAN_SCHEMA = "agentnet.component-bakeoff-plan.v1"
ENVIRONMENT_SCHEMA = "agentnet.component-environment-hashes.v1"
REVIEW_ATTESTATION_SCHEMA = "agentnet.component-bakeoff-review.v1"
REVIEW_ATTESTATION_PURPOSE = "agentnet.component.bakeoff.review.v1"
REVIEW_ROOT_ENDORSEMENT_PURPOSE = "agentnet.component.reviewer.root.v1"
MANIFEST_NAME = "manifest.json"
MAX_COMMAND_OUTPUT_BYTES = 1_048_576

_SCENARIOS = (
    "failure",
    "revocation",
    "offline",
    "duplicates",
    "upgrade",
    "rollback",
    "replacement",
)
_CLAIMS = ("license", "provenance", "self_hosted", "data_egress")
_SHA256 = re.compile(r"^[a-f0-9]{64}$")
_RUN_ID = re.compile(r"^[a-z0-9][a-z0-9._-]{7,127}$")
_ENV_NAME = re.compile(r"^[A-Za-z_][A-Za-z0-9_]{0,127}$")
_SECRET_ENV_NAME = re.compile(
    r"(?:SECRET|TOKEN|PASSWORD|PASSWD|API_?KEY|PRIVATE_?KEY|CREDENTIAL|AUTH|COOKIE|BEARER|ACCESS_?KEY|CLIENT_?SECRET)",
    re.IGNORECASE,
)
_SPDX_EXPRESSION = re.compile(r"^[A-Za-z0-9.+()-]+(?: (?:AND|OR|WITH) [A-Za-z0-9.+()-]+)*$")
_SECRET_CONTENT = re.compile(
    rb"(?i)(?:password|secret|token|api[_-]?key|authorization|credential)\s*[:=]|://[^/@\s:]+:[^/@\s]+@"
)

REQUIRED_EVIDENCE_FIELDS = frozenset(
    {
        "schema",
        "component",
        "version",
        "run_id",
        "evidence_package_sha256",
        "artifact_sha256",
        "config_sha256",
        "environment_sha256",
        "review_status",
        "reproducibility",
        "commands",
        "license",
        "provenance",
        "self_hosted",
        "data_egress",
        *_SCENARIOS,
    }
)
_PLAN_FIELDS = frozenset(
    {
        "schema",
        "component",
        "version",
        "run_id",
        "artifact_path",
        "config_path",
        "working_directory",
        "dependency_lock_path",
        "environment",
        "license",
        "provenance",
        "self_hosted",
        "data_egress",
        "commands",
    }
)
_COMMAND_FIELDS = frozenset(
    {
        "argv_sha256",
        "cwd_sha256",
        "executable_sha256",
        "exit_code",
        "status",
        "stdout_sha256",
        "stderr_sha256",
        "stdout_bytes",
        "stderr_bytes",
        "redacted_result",
        "result_sha256",
    }
)
_ASSERTION_FIELDS = frozenset({"assertion_id", "passed", "evidence_sha256"})
_SCENARIO_FIELDS = frozenset({"status", "evidence_sha256", "assertions"})
_LICENSE_FIELDS = frozenset({"reviewed", "spdx", "evidence_sha256", "assertions"})
_PROVENANCE_FIELDS = frozenset(
    {"verified", "source_sha256", "artifact_sha256", "evidence_sha256", "assertions"}
)
_SELF_HOSTED_FIELDS = frozenset({"verified", "evidence_sha256", "assertions"})
_EGRESS_FIELDS = frozenset({"reviewed", "mode", "evidence_sha256", "assertions"})
_REPRODUCIBILITY_FIELDS = frozenset(
    {
        "plan_sha256",
        "dependency_lock_sha256",
        "executables",
        "runtime",
        "os",
        "sandbox_config_sha256",
        "sandbox_evidence_sha256",
        "sandbox_launcher_sha256",
        "sandbox_profile",
        "egress_mode",
    }
)
_REVIEW_FIELDS = frozenset(
    {
        "schema",
        "component",
        "version",
        "run_id",
        "evidence_package_sha256",
        "component_claims_sha256",
        "reviewed_at",
        "expires_at",
        "decision",
        "signatures",
    }
)
_REVIEW_TRUST_SEAL = object()
_SANDBOX_SEAL = object()
_SANDBOX_LAUNCH_ENVIRONMENT = MappingProxyType(
    {
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "PATH": "/usr/bin:/bin",
    }
)


def _reject_symlink_ancestors(path: Path, label: str) -> None:
    current = path.absolute()
    for candidate in (current, *current.parents):
        if os.path.lexists(candidate) and stat.S_ISLNK(candidate.lstat().st_mode):
            raise EvidencePackageError(f"{label} traverses a symlink ancestor")


@dataclass(frozen=True, slots=True)
class ConfiguredReviewerTrustRoot:
    root_id: str
    expires_at: int
    keys: Mapping[str, str]
    threshold: int
    components: frozenset[str]
    versions: frozenset[str]
    claims: frozenset[str]
    profiles: frozenset[str]
    source_digest: str
    _seal: object


@dataclass(frozen=True, slots=True)
class ConfiguredBakeoffSandbox:
    launcher: Path
    launcher_sha256: str
    profile: str
    egress_mode: str
    evidence_sha256: str
    config_digest: str
    _seal: object

    def wrap(
        self,
        command: Sequence[str],
        *,
        workdir: str,
        environment: Mapping[str, str] | None = None,
    ) -> tuple[str, ...]:
        if self._seal is not _SANDBOX_SEAL:
            raise EvidencePackageError("configured sandbox boundary is required")
        if _sha256_file(self.launcher) != self.launcher_sha256:
            raise EvidencePackageError("sandbox launcher changed after protected composition")
        arguments = [
            str(self.launcher), "--unshare-all", "--unshare-net", "--new-session",
            "--die-with-parent", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
        ]
        for system_path in ("/usr", "/bin", "/lib", "/lib64"):
            if Path(system_path).exists():
                arguments.extend(("--ro-bind", system_path, system_path))
        rendered_command = tuple(command)
        if rendered_command[0].startswith("/proc/self/fd/"):
            pinned_target = "/run/agentnet-bakeoff-executable"
            arguments.extend(("--dir", "/run", "--ro-bind", rendered_command[0], pinned_target))
            rendered_command = (pinned_target, *rendered_command[1:])
        arguments.extend(("--ro-bind", workdir, workdir, "--chdir", workdir, "--clearenv"))
        for name, value in sorted((environment or {}).items()):
            arguments.extend(("--setenv", name, value))
        arguments.append("--")
        arguments.extend(rendered_command)
        return tuple(arguments)


def load_bakeoff_sandbox(install_root: Path) -> ConfiguredBakeoffSandbox:
    source = install_root.absolute() / "config" / "bakeoff-sandbox.json"
    _reject_symlink_ancestors(source, "sandbox runtime config")
    try:
        parent_info = source.parent.lstat()
        source_info = source.lstat()
    except FileNotFoundError as exc:
        raise EvidencePackageError("sandbox runtime config is absent") from exc
    if (
        source.parent.is_symlink()
        or not stat.S_ISDIR(parent_info.st_mode)
        or parent_info.st_uid != os.geteuid()
        or parent_info.st_mode & 0o077
        or source.is_symlink()
        or not stat.S_ISREG(source_info.st_mode)
        or source_info.st_uid != os.geteuid()
        or source_info.st_mode & 0o077
    ):
        raise EvidencePackageError("sandbox runtime config is not owner-protected")
    raw = _load_json_no_duplicates(source)
    fields = {"schema", "bwrap_sha256", "evidence_sha256"}
    if not isinstance(raw, dict) or set(raw) != fields:
        raise EvidencePackageError("sandbox runtime config schema is not exact")
    launcher = Path("/usr/bin/bwrap")
    if (
        raw.get("schema") != "agentnet.component-bakeoff-sandbox.v1"
        or launcher.is_symlink()
        or not launcher.is_file()
        or raw.get("bwrap_sha256") != _sha256_file(launcher)
        or not _valid_sha(raw.get("evidence_sha256"))
    ):
        raise EvidencePackageError("sandbox runtime config is invalid")
    return ConfiguredBakeoffSandbox(
        launcher=launcher,
        launcher_sha256=raw["bwrap_sha256"],
        profile="bwrap-unshare-all-networkless-v1",
        egress_mode="none",
        evidence_sha256=raw["evidence_sha256"],
        config_digest=_sha256_file(source),
        _seal=_SANDBOX_SEAL,
    )


def load_reviewer_trust_root(
    install_root: Path,
    *,
    endorsement_root: UpdateTrustRoot,
    now: int,
) -> ConfiguredReviewerTrustRoot:
    """Load reviewer-only trust from an owner-protected runtime config."""

    if endorsement_root.expires_at <= now:
        raise EvidencePackageError("reviewer endorsement trust root is expired")

    source = install_root.absolute() / "config" / "component-reviewer-root.json"
    _reject_symlink_ancestors(source, "reviewer trust config")
    parent = source.parent
    try:
        parent_info = parent.lstat()
        source_info = source.lstat()
    except FileNotFoundError as exc:
        raise EvidencePackageError("reviewer trust config is absent") from exc
    if (
        parent.is_symlink()
        or not stat.S_ISDIR(parent_info.st_mode)
        or parent_info.st_uid != os.geteuid()
        or parent_info.st_mode & 0o077
        or source.is_symlink()
        or not stat.S_ISREG(source_info.st_mode)
        or source_info.st_uid != os.geteuid()
        or source_info.st_mode & 0o077
    ):
        raise EvidencePackageError("reviewer trust config is not owner-protected")
    raw = _load_json_no_duplicates(source)
    fields = {
        "schema", "root_id", "expires_at", "threshold", "keys", "components",
        "versions", "claims", "profiles", "endorsements",
    }
    if not isinstance(raw, dict) or frozenset(raw) != fields:
        raise EvidencePackageError("reviewer trust config schema is not exact")
    keys = raw.get("keys")
    if (
        raw.get("schema") != "agentnet.component-reviewer-root.v1"
        or not isinstance(raw.get("root_id"), str)
        or not raw["root_id"]
        or not isinstance(raw.get("expires_at"), int)
        or isinstance(raw.get("expires_at"), bool)
        or raw["expires_at"] <= now
        or not isinstance(keys, dict)
        or not 1 <= len(keys) <= 16
        or not isinstance(raw.get("threshold"), int)
        or isinstance(raw.get("threshold"), bool)
        or not 1 <= raw["threshold"] <= len(keys)
        or any(
            not isinstance(raw.get(field), list)
            or not raw[field]
            or any(not isinstance(item, str) or not item for item in raw[field])
            for field in ("components", "versions", "claims", "profiles")
        )
        or any(not isinstance(key, str) or not key or not isinstance(value, str) or not value for key, value in keys.items())
    ):
        raise EvidencePackageError("reviewer trust config is invalid or expired")
    endorsements = raw["endorsements"]
    if not isinstance(endorsements, list) or not endorsements:
        raise EvidencePackageError("reviewer trust root lacks independent endorsements")
    root_body = {key: value for key, value in raw.items() if key != "endorsements"}
    valid_endorsers: set[str] = set()
    for endorsement in endorsements:
        if not isinstance(endorsement, dict) or set(endorsement) != {"key_id", "signature"}:
            continue
        key_id = endorsement.get("key_id")
        if not isinstance(key_id, str) or key_id in valid_endorsers or key_id not in endorsement_root.keys:
            continue
        try:
            verify_signature(
                endorsement_root.keys[key_id],
                REVIEW_ROOT_ENDORSEMENT_PURPOSE,
                root_body,
                endorsement["signature"],
            )
        except Exception:
            continue
        valid_endorsers.add(key_id)
    if len(valid_endorsers) < endorsement_root.threshold:
        raise EvidencePackageError("reviewer trust root endorsement threshold was not satisfied")
    return ConfiguredReviewerTrustRoot(
        root_id=raw["root_id"],
        expires_at=raw["expires_at"],
        keys=MappingProxyType(dict(keys)),
        threshold=raw["threshold"],
        components=frozenset(raw["components"]),
        versions=frozenset(raw["versions"]),
        claims=frozenset(raw["claims"]),
        profiles=frozenset(raw["profiles"]),
        source_digest=_sha256_file(source),
        _seal=_REVIEW_TRUST_SEAL,
    )


class EvidencePackageError(ValueError):
    """A plan, run, or immutable evidence package failed closed."""


@dataclass(frozen=True, slots=True)
class CommandSpec:
    """One exact, bounded command and the claims its zero exit establishes."""

    argv: tuple[str, ...]
    assertions: tuple[str, ...]
    timeout_seconds: int = 60
    pass_fds: tuple[int, ...] = ()


@dataclass(frozen=True, slots=True)
class ClaimSpec:
    """A non-empty evidence file plus explicit review assertions."""

    evidence_path: Path
    assertions: tuple[str, ...]


@dataclass(frozen=True, slots=True)
class BakeoffPlan:
    """Inputs needed to create one self-contained evidence package."""

    run_id: str
    artifact_path: Path
    config_path: Path
    dependency_lock_path: Path
    working_directory: Path
    environment: Mapping[str, str]
    license_spdx: str
    provenance_source: str
    egress_mode: str
    claims: Mapping[str, ClaimSpec]
    commands: Mapping[str, CommandSpec]


def _canonical_json(value: object) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False) + "\n").encode(
        "utf-8"
    )


def _sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while chunk := handle.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def _sha256_descriptor(descriptor: int) -> str:
    os.lseek(descriptor, 0, os.SEEK_SET)
    digest = hashlib.sha256()
    while chunk := os.read(descriptor, 1024 * 1024):
        digest.update(chunk)
    os.lseek(descriptor, 0, os.SEEK_SET)
    return digest.hexdigest()


def _package_digest(evidence: Mapping[str, object]) -> str:
    body = dict(evidence)
    body.pop("evidence_package_sha256", None)
    return _sha256_bytes(_canonical_json(body))


def evidence_package_digest(evidence: Mapping[str, object]) -> str:
    """Return the content address for a manifest, excluding its digest slot."""

    return _package_digest(evidence)


def _exact_keys(value: object, fields: frozenset[str], label: str, reasons: list[str]) -> bool:
    if not isinstance(value, dict):
        reasons.append(label)
        return False
    actual = frozenset(value)
    if actual != fields:
        missing = sorted(fields - actual)
        extra = sorted(actual - fields)
        if missing:
            reasons.append(f"{label}.missing={','.join(missing)}")
        if extra:
            reasons.append(f"{label}.extra={','.join(extra)}")
        return False
    return True


def _valid_sha(value: object) -> bool:
    return isinstance(value, str) and _SHA256.fullmatch(value) is not None


def _assertion_ids(value: object) -> list[object] | None:
    if not isinstance(value, dict) or not isinstance(value.get("assertions"), list):
        return None
    assertions = value["assertions"]
    if any(not isinstance(assertion, dict) for assertion in assertions):
        return None
    return [assertion.get("assertion_id") for assertion in assertions]


def _validate_assertions(
    value: object,
    label: str,
    reasons: list[str],
    *,
    expected_evidence_sha256: object,
    expected_passed: bool = False,
) -> None:
    if not isinstance(value, list) or not value:
        reasons.append(f"{label}.assertions")
        return
    claims: set[str] = set()
    for index, assertion in enumerate(value):
        item_label = f"{label}.assertions[{index}]"
        if not _exact_keys(assertion, _ASSERTION_FIELDS, item_label, reasons):
            continue
        claim = assertion["assertion_id"]
        if not _valid_sha(claim) or claim in claims:
            reasons.append(f"{item_label}.assertion_id")
        else:
            claims.add(claim)
        if assertion["passed"] is not expected_passed:
            reasons.append(f"{item_label}.passed")
        if not _valid_sha(assertion["evidence_sha256"]):
            reasons.append(f"{item_label}.evidence_sha256")
        elif assertion["evidence_sha256"] != expected_evidence_sha256:
            reasons.append(f"{item_label}.evidence_binding")


def _observed_record(value: object, label: str, reasons: list[str]) -> None:
    if not _exact_keys(value, _SCENARIO_FIELDS, label, reasons):
        return
    if value["status"] != "observed_unreviewed":
        reasons.append(f"{label}.status")
    if not _valid_sha(value["evidence_sha256"]):
        reasons.append(f"{label}.evidence_sha256")
    _validate_assertions(
        value["assertions"],
        label,
        reasons,
        expected_evidence_sha256=value["evidence_sha256"],
    )


def _review_claims_digest(evidence: Mapping[str, object]) -> str:
    """Bind the exact component and scenario claim set reviewed out of band."""

    claims: dict[str, object] = {}
    for name in (*_CLAIMS, *_SCENARIOS):
        value = evidence.get(name)
        if isinstance(value, dict):
            claims[name] = {
                key: value.get(key)
                for key in ("evidence_sha256", "assertions")
            }
        else:
            claims[name] = None
    return _sha256_bytes(_canonical_json(claims))


def reviewer_attestation_body(
    evidence: Mapping[str, object],
    *,
    reviewed_at: int,
    expires_at: int,
) -> dict[str, object]:
    """Create the exact body an independent reviewer must sign."""

    if expires_at <= reviewed_at:
        raise EvidencePackageError("reviewer attestation metadata is invalid")
    return {
        "schema": REVIEW_ATTESTATION_SCHEMA,
        "component": evidence.get("component"),
        "version": evidence.get("version"),
        "run_id": evidence.get("run_id"),
        "evidence_package_sha256": evidence.get("evidence_package_sha256"),
        "component_claims_sha256": _review_claims_digest(evidence),
        "reviewed_at": reviewed_at,
        "expires_at": expires_at,
        "decision": "adoption_ready",
    }


def _verify_reviewer_attestation(
    evidence: Mapping[str, object],
    attestation: Mapping[str, object] | None,
    reviewer_trust: ConfiguredReviewerTrustRoot | None,
    *,
    now: int | None,
    reasons: list[str],
) -> None:
    if attestation is None:
        reasons.append("reviewer_attestation.missing")
        return
    if not isinstance(attestation, Mapping) or frozenset(attestation) != _REVIEW_FIELDS:
        reasons.append("reviewer_attestation.schema")
        return
    body = dict(attestation)
    signatures = body.pop("signatures")
    expected = reviewer_attestation_body(
        evidence,
        reviewed_at=body.get("reviewed_at") if isinstance(body.get("reviewed_at"), int) else -1,
        expires_at=body.get("expires_at") if isinstance(body.get("expires_at"), int) else -1,
    ) if (
        isinstance(body.get("reviewed_at"), int)
        and not isinstance(body.get("reviewed_at"), bool)
        and isinstance(body.get("expires_at"), int)
        and not isinstance(body.get("expires_at"), bool)
        and body["expires_at"] > body["reviewed_at"]
    ) else None
    if expected is None or body != expected:
        reasons.append("reviewer_attestation.binding")
        return
    if now is None or not isinstance(now, int) or isinstance(now, bool):
        reasons.append("reviewer_attestation.time")
        return
    if body["reviewed_at"] > now or body["expires_at"] <= now:
        reasons.append("reviewer_attestation.expired")
        return
    if (
        not isinstance(reviewer_trust, ConfiguredReviewerTrustRoot)
        or reviewer_trust._seal is not _REVIEW_TRUST_SEAL
        or reviewer_trust.expires_at <= now
    ):
        reasons.append("reviewer_attestation.trust_root")
        return
    reproducibility = evidence.get("reproducibility")
    profile = reproducibility.get("sandbox_profile") if isinstance(reproducibility, dict) else None
    claim_ids = {
        assertion.get("assertion_id")
        for name in (*_CLAIMS, *_SCENARIOS)
        for assertion in (
            evidence.get(name, {}).get("assertions", [])
            if isinstance(evidence.get(name), dict)
            else []
        )
        if isinstance(assertion, dict) and isinstance(assertion.get("assertion_id"), str)
    }
    if (
        evidence.get("component") not in reviewer_trust.components
        or evidence.get("version") not in reviewer_trust.versions
        or profile not in reviewer_trust.profiles
        or not claim_ids <= reviewer_trust.claims
    ):
        reasons.append("reviewer_attestation.scope")
        return
    if not isinstance(signatures, list) or not signatures:
        reasons.append("reviewer_attestation.signatures")
        return
    valid_reviewers: set[str] = set()
    for signature in signatures:
        if not isinstance(signature, dict) or set(signature) != {"key_id", "signature"}:
            continue
        key_id = signature.get("key_id")
        if not isinstance(key_id, str) or key_id in valid_reviewers:
            continue
        public_key = reviewer_trust.keys.get(key_id)
        if not public_key:
            continue
        try:
            verify_signature(public_key, REVIEW_ATTESTATION_PURPOSE, body, signature["signature"])
        except Exception:
            continue
        valid_reviewers.add(key_id)
    if len(valid_reviewers) < reviewer_trust.threshold:
        reasons.append("reviewer_attestation.threshold")


def adoption_ready(
    component: ComponentRecord,
    evidence: dict[str, object],
    *,
    reviewer_attestation: Mapping[str, object] | None = None,
    reviewer_trust: ConfiguredReviewerTrustRoot | None = None,
    now: int | None = None,
) -> tuple[bool, list[str]]:
    """Require a separate configured reviewer signature before adoption."""

    reasons: list[str] = []
    _exact_keys(evidence, REQUIRED_EVIDENCE_FIELDS, "evidence", reasons)
    if component.decision != "accepted_phase0":
        reasons.append(f"decision={component.decision}")
    if evidence.get("schema") != EVIDENCE_SCHEMA:
        reasons.append(f"schema={EVIDENCE_SCHEMA}")
    if evidence.get("component") != component.name:
        reasons.append("component_binding")
    if evidence.get("version") != component.version:
        reasons.append("version_binding")
    if evidence.get("review_status") != "observed_unreviewed":
        reasons.append("review_status")
    reproducibility = evidence.get("reproducibility")
    if _exact_keys(reproducibility, _REPRODUCIBILITY_FIELDS, "reproducibility", reasons):
        for field in (
            "plan_sha256",
            "dependency_lock_sha256",
            "sandbox_config_sha256",
            "sandbox_evidence_sha256",
            "sandbox_launcher_sha256",
        ):
            if not _valid_sha(reproducibility[field]):
                reasons.append(f"reproducibility.{field}")
        egress_record = evidence.get("data_egress")
        if reproducibility["egress_mode"] != (
            egress_record.get("mode") if isinstance(egress_record, dict) else None
        ):
            reasons.append("reproducibility.egress_binding")
        if not isinstance(reproducibility["executables"], dict) or set(reproducibility["executables"]) != set(_SCENARIOS) or any(
            not _valid_sha(value) for value in reproducibility["executables"].values()
        ):
            reasons.append("reproducibility.executables")
        os_record = reproducibility["os"]
        if not (
            isinstance(reproducibility["runtime"], str)
            and re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", reproducibility["runtime"])
            and isinstance(os_record, dict)
            and set(os_record) == {"system", "release", "machine"}
            and all(isinstance(value, str) and value for value in os_record.values())
            and reproducibility["sandbox_profile"] == "bwrap-unshare-all-networkless-v1"
            and reproducibility["egress_mode"] == "none"
        ):
            reasons.append("reproducibility.runtime_profile")
    run_id = evidence.get("run_id")
    if not isinstance(run_id, str) or _RUN_ID.fullmatch(run_id) is None:
        reasons.append("run_id")
    for field in (
        "evidence_package_sha256",
        "artifact_sha256",
        "config_sha256",
        "environment_sha256",
    ):
        if not _valid_sha(evidence.get(field)):
            reasons.append(field)

    license_evidence = evidence.get("license")
    if _exact_keys(license_evidence, _LICENSE_FIELDS, "license", reasons):
        if license_evidence["reviewed"] is not False:
            reasons.append("license.reviewed")
        if not isinstance(license_evidence["spdx"], str) or not license_evidence["spdx"].strip():
            reasons.append("license.spdx")
        if not _valid_sha(license_evidence["evidence_sha256"]):
            reasons.append("license.evidence_sha256")
        _validate_assertions(
            license_evidence["assertions"],
            "license",
            reasons,
            expected_evidence_sha256=license_evidence["evidence_sha256"],
        )

    provenance = evidence.get("provenance")
    if _exact_keys(provenance, _PROVENANCE_FIELDS, "provenance", reasons):
        if provenance["verified"] is not False:
            reasons.append("provenance.verified")
        if not _valid_sha(provenance["source_sha256"]):
            reasons.append("provenance.source_sha256")
        if provenance["artifact_sha256"] != evidence.get("artifact_sha256"):
            reasons.append("provenance.artifact_sha256")
        if not _valid_sha(provenance["evidence_sha256"]):
            reasons.append("provenance.evidence_sha256")
        _validate_assertions(
            provenance["assertions"],
            "provenance",
            reasons,
            expected_evidence_sha256=provenance["evidence_sha256"],
        )

    self_hosted = evidence.get("self_hosted")
    if _exact_keys(self_hosted, _SELF_HOSTED_FIELDS, "self_hosted", reasons):
        if self_hosted["verified"] is not False:
            reasons.append("self_hosted.verified")
        if not _valid_sha(self_hosted["evidence_sha256"]):
            reasons.append("self_hosted.evidence_sha256")
        _validate_assertions(
            self_hosted["assertions"],
            "self_hosted",
            reasons,
            expected_evidence_sha256=self_hosted["evidence_sha256"],
        )

    data_egress = evidence.get("data_egress")
    if _exact_keys(data_egress, _EGRESS_FIELDS, "data_egress", reasons):
        if data_egress["reviewed"] is not False:
            reasons.append("data_egress.reviewed")
        if not isinstance(data_egress["mode"], str) or data_egress["mode"] not in {
            "none",
            "explicit_allowlist",
        }:
            reasons.append("data_egress.mode")
        if not _valid_sha(data_egress["evidence_sha256"]):
            reasons.append("data_egress.evidence_sha256")
        _validate_assertions(
            data_egress["assertions"],
            "data_egress",
            reasons,
            expected_evidence_sha256=data_egress["evidence_sha256"],
        )

    commands = evidence.get("commands")
    if _exact_keys(commands, frozenset(_SCENARIOS), "commands", reasons):
        for scenario in _SCENARIOS:
            command = commands[scenario]
            if not _exact_keys(command, _COMMAND_FIELDS, f"commands.{scenario}", reasons):
                continue
            for field in ("argv_sha256", "cwd_sha256", "executable_sha256"):
                if not _valid_sha(command[field]):
                    reasons.append(f"commands.{scenario}.{field}")
            if command["exit_code"] != 0:
                reasons.append(f"commands.{scenario}.exit_code")
            if command["status"] != "observed_unreviewed":
                reasons.append(f"commands.{scenario}.status")
            for field in ("stdout_sha256", "stderr_sha256", "result_sha256"):
                if not _valid_sha(command[field]):
                    reasons.append(f"commands.{scenario}.{field}")
            for field in ("stdout_bytes", "stderr_bytes"):
                if (
                    not isinstance(command[field], int)
                    or isinstance(command[field], bool)
                    or not 0 <= command[field] <= MAX_COMMAND_OUTPUT_BYTES
                ):
                    reasons.append(f"commands.{scenario}.{field}")
            redacted = command["redacted_result"]
            if not (
                isinstance(redacted, dict)
                and set(redacted) == {"schema", "scenario", "status", "assertions"}
                and redacted.get("schema") == "agentnet.component-bakeoff-scenario-result.v1"
                and redacted.get("scenario") == scenario
                and redacted.get("status") == "passed"
                and isinstance(redacted.get("assertions"), list)
                and all(
                    isinstance(item, dict)
                    and set(item) == {"assertion_id", "status"}
                    and _valid_sha(item.get("assertion_id"))
                    and item.get("status") == "passed"
                    for item in redacted["assertions"]
                )
            ):
                reasons.append(f"commands.{scenario}.redacted_result")
            command_body = {
                field: command[field]
                for field in (
                    "argv_sha256",
                    "cwd_sha256",
                    "executable_sha256",
                    "exit_code",
                    "status",
                    "stdout_sha256",
                    "stderr_sha256",
                    "stdout_bytes",
                    "stderr_bytes",
                    "redacted_result",
                )
            }
            if _valid_sha(command["result_sha256"]) and command["result_sha256"] != _sha256_bytes(
                _canonical_json(command_body)
            ):
                reasons.append(f"commands.{scenario}.result_binding")
            scenario_evidence = evidence.get(scenario)
            if isinstance(scenario_evidence, dict) and (
                scenario_evidence.get("evidence_sha256") != command["result_sha256"]
            ):
                reasons.append(f"{scenario}.command_binding")
            if isinstance(scenario_evidence, dict) and isinstance(redacted, dict):
                envelope_ids = [
                    item.get("assertion_id")
                    for item in scenario_evidence.get("assertions", [])
                    if isinstance(item, dict)
                ]
                result_ids = [
                    item.get("assertion_id")
                    for item in redacted.get("assertions", [])
                    if isinstance(item, dict)
                ]
                if envelope_ids != result_ids:
                    reasons.append(f"{scenario}.structured_assertion_binding")
            if (
                isinstance(reproducibility, dict)
                and isinstance(reproducibility.get("executables"), dict)
                and reproducibility["executables"].get(scenario) != command["executable_sha256"]
            ):
                reasons.append(f"reproducibility.executables.{scenario}_binding")

    for scenario in _SCENARIOS:
        _observed_record(evidence.get(scenario), scenario, reasons)

    if _valid_sha(evidence.get("evidence_package_sha256")) and (
        evidence["evidence_package_sha256"] != _package_digest(evidence)
    ):
        reasons.append("evidence_package_sha256.binding")
    _verify_reviewer_attestation(
        evidence,
        reviewer_attestation,
        reviewer_trust,
        now=now,
        reasons=reasons,
    )
    return not reasons, sorted(set(reasons))


def _require_nonempty_file(path: Path, label: str) -> Path:
    expanded = path.expanduser()
    if expanded.is_symlink():
        raise EvidencePackageError(f"{label} must be a non-empty regular file")
    resolved = expanded.resolve()
    if not resolved.is_file() or resolved.stat().st_size == 0:
        raise EvidencePackageError(f"{label} must be a non-empty regular file")
    return resolved


def _validate_plan(component: ComponentRecord, plan: BakeoffPlan) -> None:
    if component.decision != "accepted_phase0":
        raise EvidencePackageError(f"component decision is {component.decision}, not accepted_phase0")
    if not isinstance(plan.run_id, str) or _RUN_ID.fullmatch(plan.run_id) is None:
        raise EvidencePackageError("run_id is empty or invalid")
    _require_nonempty_file(plan.artifact_path, "artifact_path")
    _require_nonempty_file(plan.config_path, "config_path")
    _require_nonempty_file(plan.dependency_lock_path, "dependency_lock_path")
    workdir_input = plan.working_directory.expanduser()
    workdir = workdir_input.resolve()
    if workdir_input.is_symlink() or not workdir.is_dir():
        raise EvidencePackageError("working_directory must be a regular directory")
    if not plan.environment:
        raise EvidencePackageError("environment must contain the exact command environment")
    for key, value in plan.environment.items():
        if not isinstance(key, str) or _ENV_NAME.fullmatch(key) is None:
            raise EvidencePackageError(f"invalid environment name: {key!r}")
        if _SECRET_ENV_NAME.search(key):
            raise EvidencePackageError(f"secret-like environment name is forbidden: {key}")
        if not isinstance(value, str) or not value or "\x00" in value:
            raise EvidencePackageError(f"environment value is empty: {key}")
    if not isinstance(plan.license_spdx, str) or _SPDX_EXPRESSION.fullmatch(plan.license_spdx) is None:
        raise EvidencePackageError("license SPDX expression is unsafe or invalid")
    if not isinstance(plan.provenance_source, str) or not plan.provenance_source.startswith("https://"):
        raise EvidencePackageError("provenance source must use https")
    if not isinstance(plan.egress_mode, str) or plan.egress_mode not in {
        "none",
        "explicit_allowlist",
    }:
        raise EvidencePackageError("data egress mode is not reviewed")
    if frozenset(plan.claims) != frozenset(_CLAIMS):
        raise EvidencePackageError("claim evidence set must be exact")
    for name in _CLAIMS:
        claim = plan.claims[name]
        _require_nonempty_file(claim.evidence_path, f"{name}.evidence_path")
        if not claim.assertions or any(
            not isinstance(item, str) or not item.strip() for item in claim.assertions
        ):
            raise EvidencePackageError(f"{name}.assertions must be non-empty")
        if len(set(claim.assertions)) != len(claim.assertions):
            raise EvidencePackageError(f"{name}.assertions must be unique")
    if frozenset(plan.commands) != frozenset(_SCENARIOS):
        raise EvidencePackageError("commands must contain the exact scenario set")
    for name in _SCENARIOS:
        command = plan.commands[name]
        if not command.argv or any(not isinstance(item, str) or not item for item in command.argv):
            raise EvidencePackageError(f"{name}.argv must be non-empty")
        if not Path(command.argv[0]).is_absolute():
            raise EvidencePackageError(f"{name}.argv[0] must be absolute")
        if not command.assertions or any(
            not isinstance(item, str) or not item.strip() for item in command.assertions
        ):
            raise EvidencePackageError(f"{name}.assertions must be non-empty")
        if len(set(command.assertions)) != len(command.assertions):
            raise EvidencePackageError(f"{name}.assertions must be unique")
        if not isinstance(command.timeout_seconds, int) or isinstance(command.timeout_seconds, bool):
            raise EvidencePackageError(f"{name}.timeout_seconds must be an integer")
        if not 1 <= command.timeout_seconds <= 300:
            raise EvidencePackageError(f"{name}.timeout_seconds must be between 1 and 300")
        if command.pass_fds:
            raise EvidencePackageError(f"{name}.pass_fds is reserved for the pinned runner")


def _assertion_records(assertions: Sequence[str], evidence_sha256: str) -> list[dict[str, object]]:
    return [
        {
            "assertion_id": _sha256_bytes(claim.encode("utf-8")),
            "passed": False,
            "evidence_sha256": evidence_sha256,
        }
        for claim in assertions
    ]


def _argv_digest(argv: Sequence[str]) -> str:
    return _sha256_bytes(_canonical_json({"argv": list(argv)}))


def _plan_snapshot(component: ComponentRecord, plan: BakeoffPlan) -> dict[str, object]:
    return {
        "component": component.name,
        "version": component.version,
        "run_id": plan.run_id,
        "artifact_sha256": _sha256_file(plan.artifact_path),
        "config_sha256": _sha256_file(plan.config_path),
        "dependency_lock_sha256": _sha256_file(plan.dependency_lock_path),
        "working_directory_sha256": _sha256_bytes(
            str(plan.working_directory.resolve()).encode("utf-8")
        ),
        "environment": {
            key: _sha256_bytes(value.encode("utf-8")) for key, value in sorted(plan.environment.items())
        },
        "license_spdx_sha256": _sha256_bytes(plan.license_spdx.encode("utf-8")),
        "provenance_source_sha256": _sha256_bytes(plan.provenance_source.encode("utf-8")),
        "egress_mode": plan.egress_mode,
        "claims": {
            name: {
                "evidence_sha256": _sha256_file(plan.claims[name].evidence_path),
                "assertion_ids": [
                    _sha256_bytes(assertion.encode("utf-8"))
                    for assertion in plan.claims[name].assertions
                ],
            }
            for name in _CLAIMS
        },
        "commands": {
            name: {
                "argv_sha256": _argv_digest(plan.commands[name].argv),
                "executable_sha256": _sha256_file(Path(plan.commands[name].argv[0]).resolve()),
                "assertion_ids": [
                    _sha256_bytes(assertion.encode("utf-8"))
                    for assertion in plan.commands[name].assertions
                ],
                "timeout_seconds": plan.commands[name].timeout_seconds,
            }
            for name in _SCENARIOS
        },
    }


def _plan_digest(component: ComponentRecord, plan: BakeoffPlan) -> str:
    return _sha256_bytes(_canonical_json(_plan_snapshot(component, plan)))


def _run_bounded_command(
    spec: CommandSpec,
    *,
    workdir: str,
) -> dict[str, object]:
    """Hash subprocess output in memory-bounded chunks and never persist it."""

    process = subprocess.Popen(
        spec.argv,
        cwd=workdir,
        env=dict(_SANDBOX_LAUNCH_ENVIRONMENT),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        stdin=subprocess.DEVNULL,
        start_new_session=True,
        pass_fds=spec.pass_fds,
    )
    selector = selectors.DefaultSelector()
    streams = {
        "stdout": (process.stdout, hashlib.sha256(), 0),
        "stderr": (process.stderr, hashlib.sha256(), 0),
    }
    captured_stdout = bytearray()
    for name, (stream, _digest, _count) in streams.items():
        assert stream is not None
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, selectors.EVENT_READ, name)
    deadline = time.monotonic() + spec.timeout_seconds
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                raise EvidencePackageError("command timed out")
            events = selector.select(min(remaining, 0.25))
            if not events and process.poll() is not None:
                events = [(key, selectors.EVENT_READ) for key in selector.get_map().values()]
            for key, _mask in events:
                name = key.data
                stream, digest, count = streams[name]
                chunk = os.read(stream.fileno(), 65_536)
                if not chunk:
                    selector.unregister(stream)
                    stream.close()
                    continue
                count += len(chunk)
                if count > MAX_COMMAND_OUTPUT_BYTES:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                    raise EvidencePackageError(f"{name} exceeded the bounded output cap")
                digest.update(chunk)
                if name == "stdout":
                    captured_stdout.extend(chunk)
                streams[name] = (stream, digest, count)
        return_code = process.wait(timeout=max(0.0, deadline - time.monotonic()))
    except BaseException:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
        raise
    finally:
        selector.close()
    stdout = streams["stdout"]
    stderr = streams["stderr"]
    return {
        "exit_code": return_code,
        "stdout_sha256": stdout[1].hexdigest(),
        "stderr_sha256": stderr[1].hexdigest(),
        "stdout_bytes": stdout[2],
        "stderr_bytes": stderr[2],
        "stdout_payload": bytes(captured_stdout),
    }


def _structured_scenario_result(
    payload: bytes,
    *,
    scenario: str,
    assertions: Sequence[str],
) -> dict[str, object]:
    try:
        value = json.loads(payload.decode("utf-8"))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise EvidencePackageError(f"{scenario} result is not exact structured JSON") from exc
    expected_ids = [_sha256_bytes(assertion.encode("utf-8")) for assertion in assertions]
    if (
        not isinstance(value, dict)
        or set(value) != {"schema", "scenario", "status", "assertions"}
        or value.get("schema") != "agentnet.component-bakeoff-scenario-result.v1"
        or value.get("scenario") != scenario
        or value.get("status") != "passed"
        or not isinstance(value.get("assertions"), list)
        or value["assertions"]
        != [{"assertion_id": assertion_id, "status": "passed"} for assertion_id in expected_ids]
    ):
        raise EvidencePackageError(f"{scenario} result schema/assertion binding failed")
    return value


def create_evidence_package(
    component: ComponentRecord,
    plan: BakeoffPlan,
    output_directory: Path,
    *,
    sandbox: ConfiguredBakeoffSandbox,
) -> dict[str, object]:
    """Run the fixed bake-off and atomically publish a read-only package."""

    _validate_plan(component, plan)
    if not isinstance(sandbox, ConfiguredBakeoffSandbox) or sandbox._seal is not _SANDBOX_SEAL:
        raise EvidencePackageError("configured sandbox boundary is required")
    if sandbox.egress_mode != plan.egress_mode:
        raise EvidencePackageError("plan egress mode is not enforced by the configured sandbox")
    output = output_directory.expanduser().resolve()
    if output.exists() or output.is_symlink():
        raise EvidencePackageError("output directory already exists; evidence packages are immutable")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix=f".{output.name}.", dir=output.parent))
    try:
        (temporary / "inputs").mkdir()
        (temporary / "commands").mkdir()
        artifact_target = temporary / "inputs" / "artifact.bin"
        shutil.copyfile(_require_nonempty_file(plan.artifact_path, "artifact_path"), artifact_target)
        config_sha256 = _sha256_file(_require_nonempty_file(plan.config_path, "config_path"))

        environment_snapshot = {
            "schema": ENVIRONMENT_SCHEMA,
            "values": {
                name: _sha256_bytes(value.encode("utf-8"))
                for name, value in sorted(plan.environment.items())
            },
        }
        environment_target = temporary / "inputs" / "environment.json"
        environment_target.write_bytes(_canonical_json(environment_snapshot))
        plan_target = temporary / "inputs" / "plan.json"
        plan_target.write_bytes(_canonical_json(_plan_snapshot(component, plan)))
        lock_source = _require_nonempty_file(plan.dependency_lock_path, "dependency_lock_path")
        lock_bytes = lock_source.read_bytes()
        if _SECRET_CONTENT.search(lock_bytes):
            raise EvidencePackageError("dependency lock contains secret-like content")
        lock_target = temporary / "inputs" / "dependency.lock"
        lock_target.write_bytes(lock_bytes)
        (temporary / "inputs" / "config.sha256").write_text(config_sha256 + "\n")

        claim_digests: dict[str, str] = {}
        for name in _CLAIMS:
            claim_digests[name] = _sha256_file(
                _require_nonempty_file(plan.claims[name].evidence_path, name)
            )

        commands: dict[str, dict[str, object]] = {}
        scenarios: dict[str, dict[str, object]] = {}
        workdir = str(plan.working_directory.expanduser().resolve())
        for name in _SCENARIOS:
            spec = plan.commands[name]
            executable = Path(spec.argv[0]).resolve()
            executable_fd = os.open(executable, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
            executable_info = os.fstat(executable_fd)
            if not stat.S_ISREG(executable_info.st_mode):
                os.close(executable_fd)
                raise EvidencePackageError(f"{name} executable is not regular")
            executable_sha256 = _sha256_descriptor(executable_fd)
            pinned_argv = (f"/proc/self/fd/{executable_fd}", *spec.argv[1:])
            try:
                wrapped = CommandSpec(
                    sandbox.wrap(
                        pinned_argv,
                        workdir=workdir,
                        environment=plan.environment,
                    ),
                    spec.assertions,
                    spec.timeout_seconds,
                    (executable_fd,),
                )
                observed = _run_bounded_command(
                    wrapped,
                    workdir=workdir,
                )
            except (OSError, subprocess.TimeoutExpired) as exc:
                raise EvidencePackageError(f"{name} command could not complete: {exc}") from exc
            finally:
                pinned_info = os.fstat(executable_fd)
                pinned_digest = _sha256_descriptor(executable_fd)
                os.close(executable_fd)
            if observed["exit_code"] != 0:
                raise EvidencePackageError(
                    f"{name} command blocked or failed with exit {observed['exit_code']}"
                )
            if observed["stdout_bytes"] == 0:
                raise EvidencePackageError(f"{name} command produced empty result evidence")
            current_info = executable.stat()
            if (
                (current_info.st_dev, current_info.st_ino, current_info.st_size)
                != (executable_info.st_dev, executable_info.st_ino, executable_info.st_size)
                or (pinned_info.st_dev, pinned_info.st_ino, pinned_info.st_size)
                != (executable_info.st_dev, executable_info.st_ino, executable_info.st_size)
                or pinned_digest != executable_sha256
                or _sha256_file(executable) != executable_sha256
            ):
                raise EvidencePackageError(f"{name} executable changed during the sandbox run")
            structured_result = _structured_scenario_result(
                observed.pop("stdout_payload"),
                scenario=name,
                assertions=spec.assertions,
            )
            result_body = {
                "argv_sha256": _argv_digest(spec.argv),
                "cwd_sha256": _sha256_bytes(workdir.encode("utf-8")),
                "executable_sha256": executable_sha256,
                "exit_code": observed["exit_code"],
                "status": "observed_unreviewed",
                "stdout_sha256": observed["stdout_sha256"],
                "stderr_sha256": observed["stderr_sha256"],
                "stdout_bytes": observed["stdout_bytes"],
                "stderr_bytes": observed["stderr_bytes"],
                "redacted_result": structured_result,
            }
            result_sha256 = _sha256_bytes(_canonical_json(result_body))
            commands[name] = {**result_body, "result_sha256": result_sha256}
            scenarios[name] = {
                "status": "observed_unreviewed",
                "evidence_sha256": result_sha256,
                "assertions": _assertion_records(spec.assertions, result_sha256),
            }

        artifact_sha256 = _sha256_file(artifact_target)
        executable_digests: dict[str, str] = {}
        for name, spec in plan.commands.items():
            executable = Path(spec.argv[0]).resolve()
            if not executable.is_file():
                raise EvidencePackageError(f"{name} executable does not resolve to a regular file")
            executable_digests[name] = _sha256_file(executable)
        evidence: dict[str, object] = {
            "schema": EVIDENCE_SCHEMA,
            "component": component.name,
            "version": component.version,
            "run_id": plan.run_id,
            "evidence_package_sha256": "0" * 64,
            "artifact_sha256": artifact_sha256,
            "config_sha256": config_sha256,
            "environment_sha256": _sha256_file(environment_target),
            "review_status": "observed_unreviewed",
            "reproducibility": {
                "plan_sha256": _plan_digest(component, plan),
                "dependency_lock_sha256": _sha256_file(plan.dependency_lock_path),
                "executables": executable_digests,
                "runtime": platform.python_version(),
                "os": {
                    "system": platform.system(),
                    "release": platform.release(),
                    "machine": platform.machine(),
                },
                "sandbox_config_sha256": sandbox.config_digest,
                "sandbox_evidence_sha256": sandbox.evidence_sha256,
                "sandbox_launcher_sha256": sandbox.launcher_sha256,
                "sandbox_profile": sandbox.profile,
                "egress_mode": sandbox.egress_mode,
            },
            "commands": commands,
            "license": {
                "reviewed": False,
                "spdx": plan.license_spdx,
                "evidence_sha256": claim_digests["license"],
                "assertions": _assertion_records(
                    plan.claims["license"].assertions, claim_digests["license"]
                ),
            },
            "provenance": {
                "verified": False,
                "source_sha256": _sha256_bytes(plan.provenance_source.encode("utf-8")),
                "artifact_sha256": artifact_sha256,
                "evidence_sha256": claim_digests["provenance"],
                "assertions": _assertion_records(
                    plan.claims["provenance"].assertions, claim_digests["provenance"]
                ),
            },
            "self_hosted": {
                "verified": False,
                "evidence_sha256": claim_digests["self_hosted"],
                "assertions": _assertion_records(
                    plan.claims["self_hosted"].assertions, claim_digests["self_hosted"]
                ),
            },
            "data_egress": {
                "reviewed": False,
                "mode": plan.egress_mode,
                "evidence_sha256": claim_digests["data_egress"],
                "assertions": _assertion_records(
                    plan.claims["data_egress"].assertions, claim_digests["data_egress"]
                ),
            },
            **scenarios,
        }
        evidence["evidence_package_sha256"] = _package_digest(evidence)
        ready, reasons = adoption_ready(component, evidence)
        if ready or reasons != ["reviewer_attestation.missing"]:
            raise EvidencePackageError(
                "generated observation failed its unreviewed gate: " + ", ".join(reasons)
            )
        (temporary / MANIFEST_NAME).write_bytes(_canonical_json(evidence))

        for path in sorted(temporary.rglob("*"), reverse=True):
            path.chmod(stat.S_IRUSR if path.is_file() else 0o500)
        temporary.chmod(0o500)
        os.replace(temporary, output)
        return evidence
    except BaseException:
        if temporary.exists():
            for path in temporary.rglob("*"):
                try:
                    path.chmod(0o700 if path.is_dir() else 0o600)
                except OSError:
                    pass
            temporary.chmod(0o700)
            shutil.rmtree(temporary)
        raise


def _load_json_no_duplicates(path: Path) -> object:
    def reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in pairs:
            if key in result:
                raise EvidencePackageError(f"duplicate JSON key: {key}")
            result[key] = value
        return result

    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicates)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise EvidencePackageError(f"invalid JSON at {path}: {exc}") from exc


def _expected_package_files() -> frozenset[str]:
    return frozenset(
        {
            MANIFEST_NAME,
            "inputs/artifact.bin",
            "inputs/environment.json",
            "inputs/plan.json",
            "inputs/dependency.lock",
            "inputs/config.sha256",
        }
    )


def validate_evidence_package(
    component: ComponentRecord,
    package_directory: Path,
    *,
    reviewer_attestation: Mapping[str, object] | None = None,
    reviewer_trust: ConfiguredReviewerTrustRoot | None = None,
    now: int | None = None,
) -> tuple[bool, list[str]]:
    """Recompute every package binding and apply :func:`adoption_ready`."""

    package_input = package_directory.expanduser()
    if package_input.is_symlink():
        return False, ["package_directory.symlink"]
    root = package_input.resolve()
    reasons: list[str] = []
    if not root.is_dir() or root.is_symlink():
        return False, ["package_directory"]
    entries = list(root.rglob("*"))
    if any(path.is_symlink() for path in entries):
        reasons.append("package.symlink")
    actual_files = frozenset(str(path.relative_to(root)) for path in entries if path.is_file())
    expected_files = _expected_package_files()
    if actual_files != expected_files:
        missing = sorted(expected_files - actual_files)
        extra = sorted(actual_files - expected_files)
        if missing:
            reasons.append(f"package.missing={','.join(missing)}")
        if extra:
            reasons.append(f"package.extra={','.join(extra)}")
    actual_directories = frozenset(
        str(path.relative_to(root)) for path in entries if path.is_dir() and not path.is_symlink()
    )
    if actual_directories != {"inputs", "commands"}:
        reasons.append("package.directories")
    if root.stat().st_mode & 0o222 or any(path.stat().st_mode & 0o222 for path in entries):
        reasons.append("package.mutable")
    manifest_path = root / MANIFEST_NAME
    if not manifest_path.is_file() or manifest_path.is_symlink():
        return False, sorted(set([*reasons, "manifest.json"]))
    try:
        loaded = _load_json_no_duplicates(manifest_path)
    except EvidencePackageError as exc:
        return False, sorted(set([*reasons, str(exc)]))
    if not isinstance(loaded, dict):
        return False, sorted(set([*reasons, "manifest.object"]))
    evidence: dict[str, object] = loaded
    if manifest_path.read_bytes() != _canonical_json(evidence):
        reasons.append("manifest.canonical_json")

    bindings = {
        "artifact_sha256": root / "inputs" / "artifact.bin",
        "environment_sha256": root / "inputs" / "environment.json",
    }
    for field, path in bindings.items():
        if path.is_file() and not path.is_symlink():
            if path.stat().st_size == 0 or evidence.get(field) != _sha256_file(path):
                reasons.append(f"{field}.binding")
    environment_path = root / "inputs" / "environment.json"
    snapshot: object = None
    if environment_path.is_file():
        try:
            snapshot = _load_json_no_duplicates(environment_path)
        except EvidencePackageError:
            reasons.append("environment.json")
        else:
            if not (
                isinstance(snapshot, dict)
                and frozenset(snapshot) == {"schema", "values"}
                and snapshot.get("schema") == ENVIRONMENT_SCHEMA
                and isinstance(snapshot.get("values"), dict)
                and bool(snapshot["values"])
                and all(
                    isinstance(key, str)
                    and _ENV_NAME.fullmatch(key) is not None
                    and _SECRET_ENV_NAME.search(key) is None
                    and _valid_sha(value)
                    for key, value in snapshot["values"].items()
                )
                and environment_path.read_bytes() == _canonical_json(snapshot)
            ):
                reasons.append("environment.snapshot")
    config_digest_path = root / "inputs" / "config.sha256"
    if config_digest_path.is_file() and not config_digest_path.is_symlink():
        try:
            config_digest = config_digest_path.read_text(encoding="ascii").removesuffix("\n")
        except (OSError, UnicodeError):
            reasons.append("config.digest_file")
        else:
            if not _valid_sha(config_digest) or evidence.get("config_sha256") != config_digest:
                reasons.append("config.digest_binding")
    reproducibility = evidence.get("reproducibility")
    if isinstance(reproducibility, dict):
        plan_path = root / "inputs" / "plan.json"
        lock_path = root / "inputs" / "dependency.lock"
        if plan_path.is_file() and reproducibility.get("plan_sha256") != _sha256_file(plan_path):
            reasons.append("reproducibility.plan_file_binding")
        if lock_path.is_file() and reproducibility.get("dependency_lock_sha256") != _sha256_file(lock_path):
            reasons.append("reproducibility.lock_file_binding")
        if lock_path.is_file() and _SECRET_CONTENT.search(lock_path.read_bytes()):
            reasons.append("reproducibility.lock_secret")
        if plan_path.is_file():
            try:
                plan_snapshot = _load_json_no_duplicates(plan_path)
            except EvidencePackageError:
                reasons.append("reproducibility.plan_json")
            else:
                if plan_path.read_bytes() != _canonical_json(plan_snapshot):
                    reasons.append("reproducibility.plan_canonical")
                if not isinstance(plan_snapshot, dict) or set(plan_snapshot) != {
                    "component", "version", "run_id", "artifact_sha256", "config_sha256",
                    "dependency_lock_sha256", "working_directory_sha256", "environment",
                    "license_spdx_sha256", "provenance_source_sha256", "egress_mode",
                    "claims", "commands",
                }:
                    reasons.append("reproducibility.plan_schema")
                else:
                    plan_identity = {
                        "component": evidence.get("component"),
                        "version": evidence.get("version"),
                        "run_id": evidence.get("run_id"),
                    }
                    for field, expected in plan_identity.items():
                        if plan_snapshot[field] != expected:
                            reasons.append(f"reproducibility.plan_{field}_binding")
                    for plan_field, manifest_field in (
                        ("artifact_sha256", "artifact_sha256"),
                        ("config_sha256", "config_sha256"),
                    ):
                        if plan_snapshot[plan_field] != evidence.get(manifest_field):
                            reasons.append(f"reproducibility.plan_{plan_field}_binding")
                    if plan_snapshot["dependency_lock_sha256"] != reproducibility.get(
                        "dependency_lock_sha256"
                    ):
                        reasons.append("reproducibility.plan_lock_binding")
                    if (
                        isinstance(snapshot, dict)
                        and plan_snapshot["environment"] != snapshot.get("values")
                    ):
                        reasons.append("reproducibility.plan_environment_binding")
                    if not _valid_sha(plan_snapshot["working_directory_sha256"]):
                        reasons.append("reproducibility.plan_working_directory")
                    if plan_snapshot["egress_mode"] != reproducibility.get("egress_mode"):
                        reasons.append("reproducibility.plan_egress_binding")
                    license_record = evidence.get("license")
                    if not (
                        isinstance(license_record, dict)
                        and plan_snapshot["license_spdx_sha256"]
                        == _sha256_bytes(str(license_record.get("spdx", "")).encode("utf-8"))
                    ):
                        reasons.append("reproducibility.plan_license_binding")
                    provenance_record = evidence.get("provenance")
                    if not (
                        isinstance(provenance_record, dict)
                        and plan_snapshot["provenance_source_sha256"]
                        == provenance_record.get("source_sha256")
                    ):
                        reasons.append("reproducibility.plan_provenance_binding")
                    plan_claims = plan_snapshot.get("claims")
                    claim_records = {
                        "license": evidence.get("license"),
                        "provenance": evidence.get("provenance"),
                        "self_hosted": evidence.get("self_hosted"),
                        "data_egress": evidence.get("data_egress"),
                    }
                    if not (
                        isinstance(plan_claims, dict)
                        and set(plan_claims) == set(_CLAIMS)
                        and all(
                            isinstance(plan_claims[name], dict)
                            and set(plan_claims[name]) == {"evidence_sha256", "assertion_ids"}
                            and isinstance(claim_records[name], dict)
                            and plan_claims[name]["evidence_sha256"]
                            == claim_records[name].get("evidence_sha256")
                            and plan_claims[name]["assertion_ids"]
                            == _assertion_ids(claim_records[name])
                            for name in _CLAIMS
                        )
                    ):
                        reasons.append("reproducibility.plan_claim_binding")
                    plan_commands = plan_snapshot.get("commands")
                    manifest_commands = evidence.get("commands")
                    executable_records = reproducibility.get("executables")
                    if not (
                        isinstance(plan_commands, dict)
                        and set(plan_commands) == set(_SCENARIOS)
                        and isinstance(manifest_commands, dict)
                        and isinstance(executable_records, dict)
                        and set(executable_records) == set(_SCENARIOS)
                        and all(
                            isinstance(plan_commands[name], dict)
                            and set(plan_commands[name])
                            == {"argv_sha256", "executable_sha256", "assertion_ids", "timeout_seconds"}
                            and isinstance(manifest_commands.get(name), dict)
                            and plan_commands[name].get("argv_sha256")
                            == manifest_commands[name].get("argv_sha256")
                            and plan_commands[name].get("executable_sha256")
                            == manifest_commands[name].get("executable_sha256")
                            == executable_records.get(name)
                            and isinstance(evidence.get(name), dict)
                            and plan_commands[name].get("assertion_ids")
                            == _assertion_ids(evidence[name])
                            and isinstance(plan_commands[name].get("timeout_seconds"), int)
                            and not isinstance(plan_commands[name].get("timeout_seconds"), bool)
                            and 1 <= plan_commands[name]["timeout_seconds"] <= 300
                            and manifest_commands[name].get("cwd_sha256")
                            == plan_snapshot["working_directory_sha256"]
                            for name in _SCENARIOS
                        )
                    ):
                        reasons.append("reproducibility.plan_command_binding")

    commands = evidence.get("commands")
    if isinstance(commands, dict):
        for name in _SCENARIOS:
            command = commands.get(name)
            if not isinstance(command, dict):
                continue
            result_body = {
                key: command.get(key)
                for key in (
                    "argv_sha256",
                    "cwd_sha256",
                    "executable_sha256",
                    "exit_code",
                    "status",
                    "stdout_sha256",
                    "stderr_sha256",
                    "stdout_bytes",
                    "stderr_bytes",
                    "redacted_result",
                )
            }
            if command.get("result_sha256") != _sha256_bytes(_canonical_json(result_body)):
                reasons.append(f"commands.{name}.result_binding")

    ready, gate_reasons = adoption_ready(
        component,
        evidence,
        reviewer_attestation=reviewer_attestation,
        reviewer_trust=reviewer_trust,
        now=now,
    )
    reasons.extend(gate_reasons)
    return not reasons, sorted(set(reasons))


def _require_exact_object(value: object, fields: frozenset[str], label: str) -> dict[str, Any]:
    if not isinstance(value, dict) or frozenset(value) != fields:
        raise EvidencePackageError(f"{label} does not match the fixed plan schema")
    return value


def _resolve_plan_path(base: Path, value: object, label: str) -> Path:
    if not isinstance(value, str) or not value:
        raise EvidencePackageError(f"{label} path is empty")
    path = Path(value).expanduser()
    return (base / path).resolve() if not path.is_absolute() else path.resolve()


def load_plan(component: ComponentRecord, plan_path: Path) -> BakeoffPlan:
    """Load the strict JSON plan used by the executable runner."""

    resolved = plan_path.expanduser().resolve()
    raw = _require_exact_object(_load_json_no_duplicates(resolved), _PLAN_FIELDS, "plan")
    if raw["schema"] != PLAN_SCHEMA:
        raise EvidencePackageError(f"plan schema must be {PLAN_SCHEMA}")
    if raw["component"] != component.name or raw["version"] != component.version:
        raise EvidencePackageError("plan component/version binding failed")
    base = resolved.parent
    environment = raw["environment"]
    if not isinstance(environment, dict) or not environment:
        raise EvidencePackageError("environment must be a non-empty object")

    license_raw = _require_exact_object(
        raw["license"], frozenset({"spdx", "evidence_path", "assertions"}), "license"
    )
    provenance_raw = _require_exact_object(
        raw["provenance"],
        frozenset({"source", "evidence_path", "assertions"}),
        "provenance",
    )
    egress_raw = _require_exact_object(
        raw["data_egress"],
        frozenset({"mode", "evidence_path", "assertions"}),
        "data_egress",
    )
    self_hosted_raw = _require_exact_object(
        raw["self_hosted"], frozenset({"evidence_path", "assertions"}), "self_hosted"
    )
    for name, claim_raw in {
        "license": license_raw,
        "provenance": provenance_raw,
        "self_hosted": self_hosted_raw,
        "data_egress": egress_raw,
    }.items():
        assertions = claim_raw["assertions"]
        if not isinstance(assertions, list) or not all(
            isinstance(item, str) for item in assertions
        ):
            raise EvidencePackageError(f"{name}.assertions must be strings")
    claims: dict[str, ClaimSpec] = {}
    claims["license"] = ClaimSpec(
        _resolve_plan_path(base, license_raw["evidence_path"], "license"),
        tuple(license_raw["assertions"]),
    )
    claims["provenance"] = ClaimSpec(
        _resolve_plan_path(base, provenance_raw["evidence_path"], "provenance"),
        tuple(provenance_raw["assertions"]),
    )
    claims["self_hosted"] = ClaimSpec(
        _resolve_plan_path(base, self_hosted_raw["evidence_path"], "self_hosted"),
        tuple(self_hosted_raw["assertions"]),
    )
    claims["data_egress"] = ClaimSpec(
        _resolve_plan_path(base, egress_raw["evidence_path"], "data_egress"),
        tuple(egress_raw["assertions"]),
    )

    command_raw = _require_exact_object(raw["commands"], frozenset(_SCENARIOS), "commands")
    commands: dict[str, CommandSpec] = {}
    for name in _SCENARIOS:
        item = _require_exact_object(
            command_raw[name], frozenset({"argv", "assertions", "timeout_seconds"}), name
        )
        argv = item["argv"]
        assertions = item["assertions"]
        if not isinstance(argv, list) or not all(isinstance(value, str) for value in argv):
            raise EvidencePackageError(f"{name}.argv must be strings")
        if not isinstance(assertions, list) or not all(
            isinstance(value, str) for value in assertions
        ):
            raise EvidencePackageError(f"{name}.assertions must be strings")
        commands[name] = CommandSpec(tuple(argv), tuple(assertions), item["timeout_seconds"])

    plan = BakeoffPlan(
        run_id=raw["run_id"],
        artifact_path=_resolve_plan_path(base, raw["artifact_path"], "artifact_path"),
        config_path=_resolve_plan_path(base, raw["config_path"], "config_path"),
        dependency_lock_path=_resolve_plan_path(
            base, raw["dependency_lock_path"], "dependency_lock_path"
        ),
        working_directory=_resolve_plan_path(base, raw["working_directory"], "working_directory"),
        environment=environment,
        license_spdx=license_raw["spdx"],
        provenance_source=provenance_raw["source"],
        egress_mode=egress_raw["mode"],
        claims=claims,
        commands=commands,
    )
    _validate_plan(component, plan)
    return plan


def _registry_component(name: str) -> ComponentRecord:
    matches = [component for component in BASELINE_COMPONENTS if component.name == name]
    if len(matches) != 1:
        raise EvidencePackageError(f"unknown or ambiguous registry component: {name}")
    return matches[0]


def main(argv: Sequence[str] | None = None) -> int:
    """Run or validate immutable evidence from ``python -m ...components.bakeoff``."""

    parser = argparse.ArgumentParser(prog="agentnet-component-bakeoff")
    subparsers = parser.add_subparsers(dest="action", required=True)
    run_parser = subparsers.add_parser("run")
    run_parser.add_argument("--component", required=True)
    run_parser.add_argument("--plan", type=Path, required=True)
    run_parser.add_argument("--output", type=Path, required=True)
    run_parser.add_argument("--install-root", type=Path, required=True)
    validate_parser = subparsers.add_parser("validate")
    validate_parser.add_argument("--component", required=True)
    validate_parser.add_argument("--package", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        component = _registry_component(args.component)
        if args.action == "run":
            evidence = create_evidence_package(
                component,
                load_plan(component, args.plan),
                args.output,
                sandbox=load_bakeoff_sandbox(args.install_root),
            )
            print(evidence["evidence_package_sha256"])
            return 0
        ready, reasons = validate_evidence_package(component, args.package)
        if ready:
            raise EvidencePackageError("structural CLI cannot issue an adoption PASS")
        if reasons == ["reviewer_attestation.missing"]:
            print("OBSERVED_UNREVIEWED")
            return 0
        print("FAIL: " + ", ".join(reasons))
        return 1
    except EvidencePackageError as exc:
        print(f"FAIL: {exc}")
        return 2


if __name__ == "__main__":  # pragma: no cover - exercised as an executable module
    raise SystemExit(main())
