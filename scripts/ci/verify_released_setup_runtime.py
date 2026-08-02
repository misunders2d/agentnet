#!/usr/bin/env python3
"""Classify and finish exact released-0.1.31 startup proof without mutation."""

from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path
from typing import Any, Callable


class ProbeError(RuntimeError):
    """Sanitized CI-probe failure."""


def _reject_constant(value: str) -> None:
    raise ProbeError(f"non-finite JSON constant is forbidden: {value}")


def _reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ProbeError("duplicate JSON member is forbidden")
        result[key] = value
    return result


def strict_json_object(path: Path) -> dict[str, Any]:
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise ProbeError("setup evidence is unreadable") from exc
    if not raw or len(raw) > 1_048_576:
        raise ProbeError("setup evidence has invalid size")
    try:
        value = json.loads(
            raw.decode("utf-8"),
            object_pairs_hook=_reject_duplicates,
            parse_constant=_reject_constant,
        )
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ProbeError("setup evidence is not one strict JSON value") from exc
    if not isinstance(value, dict):
        raise ProbeError("setup evidence is not a JSON object")
    return value


def classify_transient_refusal(exit_code: int, evidence_path: Path) -> None:
    if exit_code != 1:
        raise ProbeError("released setup refusal did not exit with status 1")
    evidence = strict_json_object(evidence_path)
    required_strings = {
        "schema": "agentnet.server-setup.evidence.v1",
        "status": "blocked",
        "blocker": "service_runtime",
        "message": "managed AgentNet service process does not run the approved hermetic runtime",
    }
    required_false = (
        "authority_granted",
        "identity_enrolled",
        "production_durability_proven",
    )
    if any(evidence.get(key) != expected for key, expected in required_strings.items()) or any(
        evidence.get(key) is not False for key in required_false
    ):
        raise ProbeError("released setup refusal does not match the exact transient boundary")


def _load_exact_released_setup(prefix: Path) -> Any:
    import agentnet
    from agentnet.operations import server_setup

    package_root = (
        prefix
        / "lib"
        / "node_modules"
        / "@misunders2d"
        / "agentnet"
    ).resolve()
    module_path = Path(server_setup.__file__).resolve()
    if agentnet.__version__ != "0.1.31" or not module_path.is_relative_to(package_root):
        raise ProbeError("runtime probe did not load exact released AgentNet 0.1.31")
    return server_setup


def validate_runtime_and_health(setup: Any, prefix: Path) -> None:
    node = (prefix / "bin" / "node").resolve()
    uv = (prefix / "bin" / "uv").resolve()
    launcher = (
        prefix
        / "lib"
        / "node_modules"
        / "@misunders2d"
        / "agentnet"
        / "npm"
        / "bin"
        / "agentnet.mjs"
    ).resolve()
    layout = setup.SetupLayout(Path("/"))
    setup._validate_systemd_service_runtime(
        Path("/usr/bin/systemctl"),
        unit=setup.APPROVAL_UNIT,
        user=setup.APPROVAL_USER,
        data_root=setup.APPROVAL_DATA,
        node_executable=node,
        agentnet_executable=launcher,
        uv_executable=uv,
        expected_argv=(
            str(node),
            str(launcher),
            "approval",
            "serve",
            "--config",
            str(setup.APPROVAL_CONFIG),
            "--host",
            "127.0.0.1",
            "--port",
            str(setup.APPROVAL_PORT),
        ),
        layout=layout,
    )
    setup._validate_systemd_service_runtime(
        Path("/usr/bin/systemctl"),
        unit=setup.CORE_UNIT,
        user=setup.CORE_USER,
        data_root=setup.CORE_DATA,
        node_executable=node,
        agentnet_executable=launcher,
        uv_executable=uv,
        expected_argv=(
            str(node),
            str(launcher),
            "serve",
            "--config",
            str(setup.CORE_CONFIG),
            "--host",
            "127.0.0.1",
            "--port",
            str(setup.CORE_PORT),
        ),
        layout=layout,
    )
    approval_health = {
        "schema": "agentnet.approval.health.v1",
        "service": "agentnet-approval",
        "version": "0.1.31",
        "status": "alive",
        "public_origin": "https://approval.agentnet.test",
        "verifier_id": "approval.agentnet.test",
    }
    core_health = {
        "schema": "agentnet.core.health.v1",
        "service": "agentnet-core",
        "version": "0.1.31",
        "status": "alive",
        "profile": "always_on_server_agent",
        "artifact_mode": "disabled",
        "server_agent_capabilities": ["offline_custody"],
        "domain_id": "agentnet.test",
        "public_origin": "https://core.agentnet.test",
        "service_audience": "urn:agentnet:agentnet.test:corporate-api",
        "runtime_instance_id": "ordinary-server-upgrade-e2e",
    }
    for url, expected in (
        ("http://127.0.0.1:8090/healthz", approval_health),
        ("http://127.0.0.1:8080/healthz", core_health),
        ("https://approval.agentnet.test/healthz", approval_health),
        ("https://core.agentnet.test/healthz", core_health),
    ):
        setup._health(url, expected=expected, attempts=setup._START_HEALTH_ATTEMPTS)


def wait_for_runtime(
    setup: Any,
    prefix: Path,
    *,
    attempts: int = 100,
    interval_seconds: float = 0.1,
    validate: Callable[[Any, Path], None] = validate_runtime_and_health,
    sleep: Callable[[float], None] = time.sleep,
) -> None:
    if attempts < 1:
        raise ProbeError("runtime probe attempts must be positive")
    for attempt in range(attempts):
        try:
            validate(setup, prefix)
            return
        except setup.ServerSetupError as exc:
            if exc.blocker != "service_runtime":
                raise ProbeError("released runtime probe returned an unexpected blocker") from None
            if attempt + 1 == attempts:
                raise ProbeError("released runtime did not converge to exact proof") from None
            sleep(interval_seconds)
    raise AssertionError("unreachable")


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    classify = subparsers.add_parser("classify-transient")
    classify.add_argument("--exit-code", required=True, type=int)
    classify.add_argument("--evidence", required=True, type=Path)
    wait = subparsers.add_parser("wait-runtime")
    wait.add_argument("--prefix", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    arguments = _parser().parse_args(argv)
    try:
        if arguments.command == "classify-transient":
            classify_transient_refusal(arguments.exit_code, arguments.evidence)
        else:
            setup = _load_exact_released_setup(arguments.prefix.resolve())
            wait_for_runtime(setup, arguments.prefix.resolve())
    except ProbeError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print("released 0.1.31 setup runtime proof: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
