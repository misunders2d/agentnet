"""Public AgentNet CLI entry points."""

from __future__ import annotations

from . import helpers
from .commands.auth import (
    command_join_guided,
    command_join_begin,
    command_join_complete,
    command_invitation_prepare,
    command_invitation_issue,
    command_invitation_join_sponsored,
    command_invitation_oidc_begin,
    command_invitation_complete,
    command_invitation_revoke,
    command_c0_pilot,
    command_credential_renew,
    command_credential_reauthorize_expired,
    command_c0_pilot_responder,
    command_admin_entitlement_issue,
    command_admin_entitlement_revoke,
    command_admin_harness_revoke_prepare,
    command_admin_harness_revoke_commit,
    command_recovery_begin,
    command_recovery_complete,
)
from .commands.diagnostics import (
    command_verify,
    command_harness_probe,
    command_harness_demo,
    command_harness_live_gate,
    command_a2a_demo,
    command_status,
    command_demo,
    command_incident_status,
    command_incident_set,
)
from .commands.messaging import (
    command_authority_inventory,
    command_authority_explain,
    command_relationship_propose,
    command_relationship_accept,
    command_artifact_upload,
    command_artifact_abort,
    command_artifact_lifecycle,
    command_artifact_download,
    command_message_send,
    command_obligation_list,
    command_obligation_show,
    command_obligation_inbox,
    command_obligation_transition,
    command_obligation_cancel,
    command_obligation_reconcile,
    command_message_inbox,
    command_message_acknowledge,
)
from .commands.scope import (
    command_bootstrap_plan_begin,
    command_bootstrap_plan_status,
    command_bootstrap_plan_complete,
    command_communication_scope_begin,
    command_communication_scope_status,
    command_communication_scope_complete,
)
from .commands.services import (
    command_serve,
    command_console_open,
    command_console_serve,
    command_supervisor_run,
    command_manager_run,
    command_client_setup,
    command_client_setup_status,
    command_client_setup_continue,
)
from .commands.backup import (
    command_backup_sqlite,
    command_restore_sqlite,
    command_compromise_rebuild_plan,
)
from .commands.local import command_init, command_network_create
from .commands.server_agent import (
    command_bootstrap_server_agent,
    command_server_agent_setup,
    command_server_agent_reset,
    command_server_agent_reauthorize_expired_credential,
    command_server_agent_activate,
)
from .commands.auth import (
    _authority_command,
    _detect_guided_harness,
)
from .commands.services import (
    _open_console_handoff_page,
    _require_safe_serve_binding,
    _serve_one_shot_loopback_page,
)
from .commands.backup import _provision_owner_only_signing_key
from .commands.server_agent import (
    _cas_managed_private_json,
    _managed_server_reauthorization_lock,
    _open_server_agent_activation_store,
    _open_server_agent_activation_store_as_core_peer,
    _provision_owner_only_key,
    _require_managed_server_reauthorization_topology,
    _server_setup_deadline,
)
from .helpers import (
    _load_config,
    _load_identity_client,
    _owner_only_directory,
    _owner_only_file,
    _prepare_artifact_output,
    _private_state_lock,
    _public_json_request,
    _read_artifact_file,
    _write_artifact_output,
    _write_owner_json,
    _write_owner_only,
    _write_private_config,
)
from .parser import build_parser
from .main import main

__all__ = ["build_parser", "main"]
