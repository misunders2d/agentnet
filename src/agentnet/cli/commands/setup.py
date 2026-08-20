"""Compatibility exports for setup-related CLI command modules."""

from .backup import (
    _provision_owner_only_signing_key,
    _local_sqlite_path,
    _parse_manifest_seal,
    _seal_json,
    _backup_seal_pin,
    command_backup_sqlite,
    _verified_sqlite_backup_from_args,
    command_restore_sqlite,
    command_compromise_rebuild_plan,
)
from .local import (
    command_init,
    command_network_create,
)
from .server_agent import (
    _provision_owner_only_key,
    command_bootstrap_server_agent,
    _open_server_agent_activation_store,
    _open_server_agent_activation_store_as_core_peer,
    _require_server_agent_activation_binding,
    _server_setup_deadline,
    _setup_progress,
    command_server_agent_setup,
    command_server_agent_reset,
    _managed_server_reauthorization_verifier,
    _managed_server_reauthorization_client,
    _require_managed_server_reauthorization_topology,
    _managed_private_file,
    _cas_managed_private_json,
    _replace_managed_private_bytes,
    _managed_server_reauthorization_provenance,
    _managed_server_reauthorization_lock,
    command_server_agent_reauthorize_expired_credential,
    _command_server_agent_reauthorize_expired_credential_locked,
    command_server_agent_activate,
)

__all__ = (
    "command_backup_sqlite",
    "command_restore_sqlite",
    "command_compromise_rebuild_plan",
    "command_init",
    "command_network_create",
    "command_bootstrap_server_agent",
    "command_server_agent_setup",
    "command_server_agent_reset",
    "command_server_agent_reauthorize_expired_credential",
    "command_server_agent_activate",
)
