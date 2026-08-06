"""Explicit capabilities for ordinary always-on enrolled server agents.

Capabilities select work a process may attempt.  They never create caller
identity, human authority, data permission, or transitive trust.
"""

from enum import StrEnum
from hashlib import sha256


ENDPOINT_CAPABILITY_ROOT_BYTES = 32


def endpoint_capability_root_name(
    *, domain_id: str, harness_id: str, adapter_generation: int
) -> str:
    """Return the opaque, exact-generation directory name for one endpoint."""

    if not domain_id or not harness_id:
        raise ValueError("endpoint capability identity must be non-empty")
    if adapter_generation < 1:
        raise ValueError("endpoint adapter generation must be positive")
    material = f"{domain_id}\0{harness_id}\0{adapter_generation}".encode()
    return sha256(material).hexdigest()


class ServerAgentCapability(StrEnum):
    OFFLINE_CUSTODY = "offline_custody"
    STORE_AND_FORWARD = "store_and_forward"
    ARTIFACT_STORAGE = "artifact_storage"
    SENSITIVE_DATA_SERVICE = "sensitive_data_service"
    RELAY = "relay"
    FEDERATION = "federation"
    A2A_GATEWAY = "a2a_gateway"
    LOCAL_BINDING = "local_binding"
    SCANNER = "scanner"
    EFFECT_EXECUTOR = "effect_executor"
    AUDIT_EXPORTER = "audit_exporter"


SEPARATE_CREDENTIAL_CAPABILITIES = frozenset(
    {
        ServerAgentCapability.SCANNER,
        ServerAgentCapability.EFFECT_EXECUTOR,
        ServerAgentCapability.A2A_GATEWAY,
        ServerAgentCapability.FEDERATION,
        ServerAgentCapability.AUDIT_EXPORTER,
    }
)


__all__ = [
    "ENDPOINT_CAPABILITY_ROOT_BYTES",
    "SEPARATE_CREDENTIAL_CAPABILITIES",
    "ServerAgentCapability",
    "endpoint_capability_root_name",
]
