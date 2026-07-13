"""Installed lifecycle evidence and strict live-gate contract.

The ordinary suite executes the credential-free installed binary/version and
deterministic lifecycle checks.  Semantic inference is an explicit operator
gate; when requested, missing evidence or credentials is a hard failure and is
never represented as a pytest skip.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from agentnet.errors import GateBlocked
from agentnet.supervisor.demos import (
    content_free_demo_summary,
    run_deterministic_harness_demo,
)
from agentnet.supervisor.live_gate import (
    LIVE_OPT_IN,
    assert_installed_probe_report,
    installed_probe_report,
    require_live_opt_in,
    run_live_harness_gate,
)


pytestmark = pytest.mark.external


def test_all_four_installed_binaries_match_exact_pins_without_inference(tmp_path: Path) -> None:
    report = installed_probe_report(tmp_path / "installed-probes")
    assert_installed_probe_report(report)
    assert set(report) == {"claude", "codex", "pi", "antigravity"}
    assert all(probe["external_conformance_proven"] is False for probe in report.values())
    assert all(probe["evidence_scope"] == "local_detection_only" for probe in report.values())


def test_all_four_installed_deterministic_lifecycles_are_bounded_and_content_free(
    tmp_path: Path,
) -> None:
    result = run_deterministic_harness_demo(
        tmp_path / "installed-lifecycle",
        request_timeout_seconds=5.0,
    )
    summary = content_free_demo_summary(result)
    assert set(summary["harnesses"]) == {"claude", "codex", "pi", "antigravity"}
    for harness in summary["harnesses"].values():
        assert harness["version_match"] is True
        assert harness["phase_at_probe"] == "ready"
        assert harness["clean_worker_admitted"] is False
        assert harness["semantic_content_sent"] is False
    assert "deterministic lifecycle evidence only" in summary["warning"]


@pytest.mark.parametrize("harness", ["claude", "codex", "pi", "antigravity"])
def test_live_inference_not_requested_is_an_explicit_gate_not_a_skip(
    tmp_path: Path,
    harness: str,
) -> None:
    with pytest.raises(GateBlocked, match="not explicitly requested"):
        run_live_harness_gate(harness, root=tmp_path, environment={})


def test_requested_live_gate_fails_closed_on_first_missing_evidence_input(tmp_path: Path) -> None:
    with pytest.raises(GateBlocked, match="AGENTNET_LIVE_CLEAN_EVIDENCE_DIR"):
        run_live_harness_gate(
            "claude",
            root=tmp_path,
            environment={LIVE_OPT_IN: "1"},
        )


def test_live_opt_in_requires_exact_boolean_value() -> None:
    for value in ("", "0", "true", "yes", "2"):
        with pytest.raises(GateBlocked, match="not explicitly requested"):
            require_live_opt_in({LIVE_OPT_IN: value})
    require_live_opt_in({LIVE_OPT_IN: "1"})
