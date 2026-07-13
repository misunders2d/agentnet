from __future__ import annotations

from agentnet.adapters.capabilities import ALL


def test_all_four_harness_manifests_are_zero_secret_and_non_foreground() -> None:
    assert set(ALL) == {"claude", "codex", "pi", "antigravity"}
    for harness, capability in ALL.items():
        capability.validate()
        assert capability.harness == harness
        assert capability.holds_credentials is False
        assert capability.foreground_message_methods == ()
        assert capability.background_path
        assert capability.semantic_default in {"clean_worker_required", "deterministic_only"}


def test_pi_stays_deterministic_only_until_direct_binding_is_proven() -> None:
    pi = ALL["pi"]
    assert pi.local_binding == "direct_ipc"
    assert pi.semantic_default == "deterministic_only"
