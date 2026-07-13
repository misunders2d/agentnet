"""Content-free foreground status shaping."""

from __future__ import annotations


def status_indicator(counts: dict[str, int]) -> dict[str, int | str]:
    total = sum(max(0, int(value)) for value in counts.values())
    return {"kind": "agentnet_count", "count": min(total, 9999)}

