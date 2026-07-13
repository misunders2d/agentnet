"""Explicit capabilities for ordinary always-on enrolled server agents.

Capabilities select work a process may attempt.  They never create caller
identity, human authority, data permission, or transitive trust.
"""

from enum import StrEnum


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
