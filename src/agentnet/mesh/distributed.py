"""Fully distributed authority remains fail-closed until quorum gates pass."""

from agentnet.errors import GateBlocked


class DisabledDistributedAuthority:
    def __getattr__(self, _name: str):
        raise GateBlocked("G09/G11/G12/G16/G19", "distributed authority lacks approved quorum/partition/revocation evidence")

