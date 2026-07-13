"""Capability matrix is routing information, never positive authority."""

from agentnet.adapters.antigravity import CAPABILITIES as ANTIGRAVITY
from agentnet.adapters.claude import CAPABILITIES as CLAUDE
from agentnet.adapters.codex import CAPABILITIES as CODEX
from agentnet.adapters.pi import CAPABILITIES as PI

ALL = {item.harness: item for item in (CLAUDE, CODEX, PI, ANTIGRAVITY)}

for _capability in ALL.values():
    _capability.validate()

