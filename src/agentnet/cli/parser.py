"""Argument parser for the AgentNet CLI."""

from __future__ import annotations

import argparse
from agentnet import __version__
from agentnet.approval.cli_commands import configure_approval_parser
from agentnet.operations.server_setup import (
    CORE_CONFIG,
    SERVER_AGENT_IDENTITY,
)
from agentnet.operations.incident import IncidentMode
from agentnet.protocol.models import Classification
from agentnet.cli.commands.messaging import (
    command_artifact_abort,
    command_artifact_download,
    command_artifact_lifecycle,
    command_artifact_upload,
    command_authority_explain,
    command_authority_inventory,
    command_message_acknowledge,
    command_message_inbox,
    command_message_send,
    command_obligation_cancel,
    command_obligation_inbox,
    command_obligation_list,
    command_obligation_reconcile,
    command_obligation_show,
    command_obligation_transition,
    command_relationship_accept,
    command_relationship_propose,
)
from agentnet.cli.commands.diagnostics import (
    command_a2a_demo,
    command_demo,
    command_harness_demo,
    command_harness_live_gate,
    command_harness_probe,
    command_incident_set,
    command_incident_status,
    command_status,
    command_verify,
)
from agentnet.cli.commands.services import (
    _configure_client_setup_arguments,
    command_client_setup,
    command_client_setup_continue,
    command_client_setup_status,
    command_console_open,
    command_console_serve,
    command_manager_run,
    command_serve,
    command_supervisor_run,
)
from agentnet.cli.commands.setup import (
    command_bootstrap_server_agent,
    command_backup_sqlite,
    command_compromise_rebuild_plan,
    command_init,
    command_network_create,
    command_restore_sqlite,
    command_server_agent_activate,
    command_server_agent_reauthorize_expired_credential,
    command_server_agent_reset,
    command_server_agent_setup,
)
from agentnet.cli.commands.auth import (
    command_admin_entitlement_issue,
    command_admin_entitlement_revoke,
    command_admin_harness_revoke_commit,
    command_admin_harness_revoke_prepare,
    command_c0_pilot,
    command_c0_pilot_responder,
    command_credential_reauthorize_expired,
    command_credential_renew,
    command_invitation_complete,
    command_invitation_issue,
    command_invitation_join_sponsored,
    command_invitation_oidc_begin,
    command_invitation_prepare,
    command_invitation_revoke,
    command_join_begin,
    command_join_complete,
    command_join_guided,
    command_recovery_begin,
    command_recovery_complete,
)
from agentnet.cli.commands.scope import (
    command_bootstrap_plan_begin,
    command_bootstrap_plan_complete,
    command_bootstrap_plan_status,
    command_communication_scope_begin,
    command_communication_scope_complete,
    command_communication_scope_status,
)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="agentnet", description="AgentNet")
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    commands = parser.add_subparsers(dest="command", required=True)
    configure_approval_parser(commands)
    setup = commands.add_parser(
        "setup",
        help="begin or resume package-owned user-level AgentNet setup",
    )
    _configure_client_setup_arguments(setup)
    setup.set_defaults(func=command_client_setup)
    setup_commands = setup.add_subparsers(dest="setup_command", required=False)
    setup_status = setup_commands.add_parser(
        "status",
        help="show the exact resumable setup state",
    )
    _configure_client_setup_arguments(setup_status, defaults=False)
    setup_status.set_defaults(func=command_client_setup_status)
    setup_continue = setup_commands.add_parser(
        "continue",
        help="continue enrollment or activation without restarting the harness",
    )
    _configure_client_setup_arguments(setup_continue, defaults=False)
    setup_continue.set_defaults(func=command_client_setup_continue)


    network = commands.add_parser("network", help="create and operate one AgentNet namespace")
    network_commands = network.add_subparsers(dest="network_command", required=True)
    network_create = network_commands.add_parser(
        "create",
        help="create a production server-agent domain and migrate its PostgreSQL store",
    )
    network_create.add_argument("--config", default="agentnet.json")
    network_create.add_argument("--data-dir", default=".agentnet/server")
    network_create.add_argument("--domain")
    network_create.add_argument(
        "--database-url",
        default="postgresql://agentnet@127.0.0.1/agentnet",
        help="password-free PostgreSQL DSN; credentials come from --database-url-env",
    )
    network_create.add_argument("--database-url-env", default="AGENTNET_DATABASE_URL")
    network_create.add_argument(
        "--database-url-from-env",
        action="store_true",
        help="resolve the DSN only from --database-url-env so it never appears in process arguments",
    )
    network_create.add_argument("--public-base-url", required=True)
    network_create.add_argument("--oidc-config", required=True)
    network_create.add_argument(
        "--artifact-mode",
        choices=("enabled", "disabled"),
        default="enabled",
        help="enabled keeps scanner-backed artifacts; disabled permits communication only",
    )
    network_create.add_argument(
        "--scanner-trust-config",
        help="public maintained-scanner trust configuration required when artifacts are enabled",
    )
    network_create.add_argument("--runtime-instance-id", default="agentnet-server-1")
    network_create.add_argument("--postgres-recovery-topology", action="store_true")
    network_create.add_argument("--force", action="store_true")
    network_create.set_defaults(func=command_network_create)

    server_agent = commands.add_parser(
        "server-agent",
        help="activate and operate one ordinary enrolled always-on AgentNet process",
    )
    server_agent_commands = server_agent.add_subparsers(
        dest="server_agent_command",
        required=True,
    )
    server_agent_setup = server_agent_commands.add_parser(
        "setup",
        help="plan or apply the fixed product-owned ordinary Linux server profile",
    )
    server_agent_setup.add_argument("--request")
    server_agent_setup.add_argument(
        "--apply",
        action="store_true",
        help="apply the frozen setup request; omitted means no-managed-host-write plan",
    )
    server_agent_setup.add_argument(
        "--start",
        action="store_true",
        help="start and health-check only the managed AgentNet units after apply",
    )
    server_agent_setup.add_argument(
        "--expected-request-digest",
        help="exact digest from the human-approved no-managed-host-write plan; required with --apply",
    )
    server_agent_setup.set_defaults(func=command_server_agent_setup)
    server_agent_reset = server_agent_commands.add_parser(
        "reset",
        help="remove only package-owned server state while retaining every external prerequisite",
    )
    server_agent_reset.add_argument(
        "--retain-external-prerequisites",
        action="store_true",
        required=True,
        help="required acknowledgment that PostgreSQL, Node.js, uv, proxy, TLS, and operator config are retained",
    )
    server_agent_reset.add_argument(
        "--confirm-package-state-removal",
        action="store_true",
        required=True,
        help="required explicit confirmation to stop managed units and remove package-owned AgentNet state",
    )
    server_agent_reset.set_defaults(func=command_server_agent_reset)
    server_agent_activate = server_agent_commands.add_parser(
        "activate",
        help="bind an offline server config to one exact enrolled identity without granting authority",
    )
    server_agent_activate.add_argument("--config", default="agentnet.json")
    server_agent_activate.add_argument("--identity", default=".agentnet/identity.json")
    server_agent_activate.set_defaults(func=command_server_agent_activate)
    server_agent_reauthorize = server_agent_commands.add_parser(
        "reauthorize-expired-credential",
        help="reauthorize one exact expired managed-server credential through Approval",
    )
    server_agent_reauthorize.add_argument("--config", default=str(CORE_CONFIG))
    server_agent_reauthorize.add_argument("--identity", default=str(SERVER_AGENT_IDENTITY))
    server_agent_reauthorize.add_argument(
        "--state",
        default="/var/lib/agentnet-setup/credential-reauthorization.json",
    )
    server_agent_reauthorize.add_argument(
        "--replace-terminal-state",
        action="store_true",
        help="replace only a broker-proven rejected or expired pending ceremony",
    )
    server_agent_reauthorize.set_defaults(
        func=command_server_agent_reauthorize_expired_credential
    )

    join = commands.add_parser("join", help="enroll this person and device into an AgentNet")
    join_commands = join.add_subparsers(dest="join_command", required=True)
    join_guided = join_commands.add_parser(
        "guided",
        help="run resumable browser OIDC and Core-brokered independent approval",
    )
    join_guided.add_argument("--server", required=True)
    join_guided.add_argument("--domain")
    join_guided.add_argument("--harness")
    join_guided.add_argument("--name")
    join_guided.add_argument("--state", default=".agentnet/guided-join.json")
    join_guided.add_argument("--private-key")
    join_guided.add_argument("--identity", default=".agentnet/identity.json")
    join_guided.add_argument(
        "--browser",
        choices=("system", "terminal", "remote"),
        default="system",
        help="open locally, use fixed Core /activate remotely, or disclose through a private terminal",
    )
    join_guided.add_argument("--timeout", type=int, choices=range(30, 601), default=300)
    join_guided.add_argument(
        "--replace-terminal-state",
        action="store_true",
        help=(
            "replace one exact pending local state only after Core proves its "
            "continuation expired or failed; the candidate key is reused"
        ),
    )
    join_guided.set_defaults(func=command_join_guided)
    join_begin = join_commands.add_parser("begin", help="start exact OIDC/device enrollment")
    join_begin.add_argument("--server", required=True)
    join_begin.add_argument("--harness", required=True)
    join_begin.add_argument("--name", required=True)
    join_begin.add_argument("--state", default=".agentnet/join-pending.json")
    join_begin.add_argument("--private-key")
    join_begin.set_defaults(func=command_join_begin)
    join_complete = join_commands.add_parser(
        "complete",
        help="complete OIDC enrollment with exact key possession and independent approval",
    )
    join_complete.add_argument("--state", default=".agentnet/join-pending.json")
    join_complete.add_argument("--challenge", required=True)
    join_complete.add_argument("--approval", required=True)
    join_complete.add_argument("--identity", default=".agentnet/identity.json")
    join_complete.add_argument("--force", action="store_true")
    join_complete.set_defaults(func=command_join_complete)

    invitation = commands.add_parser(
        "invitation",
        help="prepare, sponsor, verify, accept, or revoke one exact internal invitation",
    )
    invitation_commands = invitation.add_subparsers(
        dest="invitation_command",
        required=True,
    )
    invitation_prepare = invitation_commands.add_parser("prepare")
    invitation_prepare.add_argument("--server", required=True)
    invitation_prepare.add_argument("--domain", required=True)
    invitation_prepare.add_argument("--issuer", required=True)
    invitation_prepare.add_argument("--subject", required=True)
    invitation_prepare.add_argument("--email", required=True)
    invitation_prepare.add_argument("--harness", required=True)
    invitation_prepare.add_argument("--harness-id")
    invitation_prepare.add_argument("--name", required=True)
    invitation_prepare.add_argument(
        "--binding-assurance",
        choices=("os_bound", "hardware_bound"),
        required=True,
    )
    invitation_prepare.add_argument("--capability", action="append")
    invitation_prepare.add_argument("--reason", required=True)
    invitation_prepare.add_argument("--expires-in", type=int, default=86_400)
    invitation_prepare.add_argument("--invitation-id")
    invitation_prepare.add_argument("--request", default=".agentnet/invitation-request.json")
    invitation_prepare.add_argument("--state", default=".agentnet/invitation-candidate.json")
    invitation_prepare.add_argument("--private-key")
    invitation_prepare.set_defaults(func=command_invitation_prepare)
    invitation_issue = invitation_commands.add_parser("issue")
    invitation_issue.add_argument("--identity", default=".agentnet/identity.json")
    invitation_issue.add_argument("--request", default=".agentnet/invitation-request.json")
    invitation_issue.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_issue.add_argument("--force", action="store_true")
    invitation_issue.set_defaults(func=command_invitation_issue)
    invitation_begin = invitation_commands.add_parser("oidc-begin")
    invitation_begin.add_argument("--state", default=".agentnet/invitation-candidate.json")
    invitation_begin.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_begin.set_defaults(func=command_invitation_oidc_begin)
    invitation_complete = invitation_commands.add_parser("complete")
    invitation_complete.add_argument("--state", default=".agentnet/invitation-candidate.json")
    invitation_complete.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_complete.add_argument("--callback", required=True)
    invitation_complete.add_argument("--identity", default=".agentnet/identity.json")
    invitation_complete.add_argument("--force", action="store_true")
    invitation_complete.set_defaults(func=command_invitation_complete)
    invitation_sponsored = invitation_commands.add_parser("join-sponsored")
    invitation_sponsored.add_argument("--server", required=True)
    invitation_sponsored.add_argument("--harness-id")
    invitation_sponsored.add_argument("--harness", default="laptop")
    invitation_sponsored.add_argument("--name", required=True)
    invitation_sponsored.add_argument(
        "--binding-assurance",
        choices=("os_bound", "hardware_bound"),
        default="os_bound",
    )
    invitation_sponsored.add_argument("--state", default=".agentnet/sponsored-enrollment.json")
    invitation_sponsored.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_sponsored.add_argument("--private-key")
    invitation_sponsored.add_argument("--callback")
    invitation_sponsored.add_argument("--identity", default=".agentnet/identity.json")
    invitation_sponsored.add_argument("--force", action="store_true")
    invitation_sponsored.set_defaults(func=command_invitation_join_sponsored)
    invitation_revoke = invitation_commands.add_parser("revoke")
    invitation_revoke.add_argument("--identity", default=".agentnet/identity.json")
    invitation_revoke.add_argument("--invitation", default=".agentnet/invitation.json")
    invitation_revoke.add_argument("--reason", required=True)
    invitation_revoke.set_defaults(func=command_invitation_revoke)

    bootstrap_plan = commands.add_parser(
        "bootstrap-plan",
        help="prepare the bounded same-principal two-harness C0 plan",
    )
    bootstrap_plan_commands = bootstrap_plan.add_subparsers(
        dest="bootstrap_plan_command",
        required=True,
    )
    for name, function in (
        ("begin", command_bootstrap_plan_begin),
        ("status", command_bootstrap_plan_status),
        ("complete", command_bootstrap_plan_complete),
    ):
        operation = bootstrap_plan_commands.add_parser(name)
        operation.add_argument("--identity", default=".agentnet/identity.json")
        operation.add_argument("--state", default=".agentnet/bootstrap-plan-state.json")
        operation.set_defaults(func=function)

    communication_scope = commands.add_parser(
        "communication-scope",
        help="approve or inspect the persistent same-principal communication scope",
    )
    communication_scope_commands = communication_scope.add_subparsers(
        dest="communication_scope_command",
        required=True,
    )
    for name, function in (
        ("begin", command_communication_scope_begin),
        ("status", command_communication_scope_status),
        ("complete", command_communication_scope_complete),
    ):
        operation = communication_scope_commands.add_parser(name)
        operation.add_argument("--identity", default=".agentnet/identity.json")
        operation.add_argument(
            "--state",
            default=".agentnet/communication-scope-state.json",
        )
        if name == "begin":
            operation.add_argument(
                "--replace-terminal-state",
                action="store_true",
                help="replace retry keys only after Core proves the old scope terminal",
            )
        operation.set_defaults(func=function)

    credential = commands.add_parser(
        "credential",
        help="operate the exact current signed credential",
    )
    credential_commands = credential.add_subparsers(dest="credential_command", required=True)
    credential_renew = credential_commands.add_parser(
        "renew",
        help="renew the exact configured always-on credential within policy window",
    )
    credential_renew.add_argument("--identity", default=".agentnet/identity.json")
    credential_renew.add_argument("--state", default=".agentnet/credential-renewal-state.json")
    credential_renew.set_defaults(func=command_credential_renew)
    credential_reauthorize = credential_commands.add_parser(
        "reauthorize-expired",
        help="reauthorize the exact expired laptop credential without replacing its key or identity",
    )
    credential_reauthorize.add_argument(
        "--identity",
        default=".agentnet/identity.json",
    )
    credential_reauthorize.add_argument(
        "--state",
        default=".agentnet/credential-reauthorization-state.json",
    )
    credential_reauthorize.add_argument(
        "--browser",
        choices=("system", "manual"),
        default="system",
        help="open the stable Approval entrypoint or disclose it through a private terminal",
    )
    credential_reauthorize.add_argument(
        "--timeout",
        type=int,
        choices=range(30, 601),
        default=300,
    )
    credential_reauthorize.set_defaults(
        func=command_credential_reauthorize_expired
    )

    c0_pilot = commands.add_parser(
        "c0-pilot",
        help="run or inspect the fixed same-principal two-harness C0 proof",
    )
    c0_pilot_commands = c0_pilot.add_subparsers(
        dest="c0_pilot_command", required=True
    )
    for name in ("start", "status", "complete"):
        operation = c0_pilot_commands.add_parser(name)
        operation.add_argument("--identity", default=".agentnet/identity.json")
        operation.set_defaults(func=command_c0_pilot)
    c0_responder = c0_pilot_commands.add_parser(
        "responder",
        help="check or run dedicated package-owned C0 responder",
    )
    c0_responder.add_argument("--config", required=True)
    c0_responder.add_argument("--credential", required=True)
    c0_responder_mode = c0_responder.add_mutually_exclusive_group(required=True)
    c0_responder_mode.add_argument("--check", action="store_true")
    c0_responder_mode.add_argument("--run", action="store_true")
    c0_responder.set_defaults(func=command_c0_pilot_responder)

    authority = commands.add_parser(
        "authority",
        help="inspect authority bound to the current authenticated identity",
    )
    authority_commands = authority.add_subparsers(dest="authority_command", required=True)
    authority_inventory = authority_commands.add_parser("inventory")
    authority_inventory.add_argument("--identity", default=".agentnet/identity.json")
    authority_inventory.set_defaults(func=command_authority_inventory)
    authority_explain = authority_commands.add_parser("explain")
    authority_explain.add_argument("--identity", default=".agentnet/identity.json")
    authority_explain.add_argument("--decision-id", required=True)
    authority_explain.set_defaults(func=command_authority_explain)

    relationship = commands.add_parser(
        "relationship",
        help="propose and independently accept exact bilateral governance",
    )
    relationship_commands = relationship.add_subparsers(
        dest="relationship_command",
        required=True,
    )
    relationship_propose = relationship_commands.add_parser("propose")
    relationship_propose.add_argument("--identity", default=".agentnet/identity.json")
    relationship_propose.add_argument("--relationship", required=True)
    relationship_propose.add_argument("--proposal", default=".agentnet/relationship-proposal.json")
    relationship_propose.add_argument("--proposal-expires-in", type=int, default=86_400)
    relationship_propose.add_argument("--force", action="store_true")
    relationship_propose.set_defaults(func=command_relationship_propose)
    relationship_accept = relationship_commands.add_parser("accept")
    relationship_accept.add_argument("--identity", default=".agentnet/identity.json")
    relationship_accept.add_argument("--proposal", default=".agentnet/relationship-proposal.json")
    relationship_accept.add_argument("--approval", required=True)
    relationship_accept.set_defaults(func=command_relationship_accept)

    artifact = commands.add_parser(
        "artifact",
        help="upload quarantined artifacts and download released bytes",
    )
    artifact_commands = artifact.add_subparsers(dest="artifact_command", required=True)
    artifact_upload = artifact_commands.add_parser(
        "upload",
        help="reserve, upload, and promote one exact file into quarantine",
    )
    artifact_upload.add_argument("path")
    artifact_upload.add_argument("--identity", default=".agentnet/identity.json")
    artifact_upload.add_argument("--idempotency-key", required=True)
    artifact_upload.add_argument("--media-type", required=True)
    artifact_upload.add_argument("--origin", required=True)
    artifact_upload.add_argument(
        "--classification",
        choices=tuple(item.value for item in Classification),
        default=Classification.C1_INTERNAL.value,
    )
    artifact_upload.add_argument("--ttl-seconds", type=int, default=3600)
    artifact_upload.add_argument("--optional-attachment", action="store_true")
    artifact_upload.set_defaults(func=command_artifact_upload)
    artifact_abort = artifact_commands.add_parser(
        "abort",
        help="abort one caller-owned unpromoted reservation",
    )
    artifact_abort.add_argument("reservation_id")
    artifact_abort.add_argument("--identity", default=".agentnet/identity.json")
    artifact_abort.set_defaults(func=command_artifact_abort)
    artifact_lifecycle = artifact_commands.add_parser(
        "lifecycle",
        help="read content-free lifecycle state for one artifact",
    )
    artifact_lifecycle.add_argument("artifact_id")
    artifact_lifecycle.add_argument("--identity", default=".agentnet/identity.json")
    artifact_lifecycle.set_defaults(func=command_artifact_lifecycle)
    artifact_download = artifact_commands.add_parser(
        "download",
        help="consume a current single-use capability into a new private file",
    )
    artifact_download.add_argument("artifact_id")
    artifact_download.add_argument("--output", required=True)
    artifact_download.add_argument("--identity", default=".agentnet/identity.json")
    artifact_download.add_argument("--ttl-seconds", type=int, default=60)
    artifact_download.set_defaults(func=command_artifact_download)

    message = commands.add_parser("message", help="send and receive authenticated messages")
    message_commands = message.add_subparsers(dest="message_command", required=True)
    message_send = message_commands.add_parser("send")
    message_send.add_argument("--identity", default=".agentnet/identity.json")
    message_send.add_argument("--collaboration-scope-id", required=True)
    message_send.add_argument("--recipient", action="append", required=True)
    message_send.add_argument("--payload", required=True)
    message_send.add_argument("--idempotency-key")
    message_send.add_argument(
        "--classification",
        choices=tuple(item.value for item in Classification),
        default=Classification.C1_INTERNAL.value,
    )
    message_send.set_defaults(func=command_message_send)
    message_inbox = message_commands.add_parser("inbox")
    message_inbox.add_argument("--identity", default=".agentnet/identity.json")
    message_inbox.add_argument("--collaboration-scope-id", required=True)
    message_inbox.add_argument("--after", type=int, default=0)
    message_inbox.add_argument("--limit", type=int, default=100)
    message_inbox.set_defaults(func=command_message_inbox)
    message_acknowledge = message_commands.add_parser(
        "acknowledge",
        help="record durable custody for one exact mailbox event",
    )
    message_acknowledge.add_argument("event_id")
    message_acknowledge.add_argument("--envelope-digest", required=True)
    message_acknowledge.add_argument("--identity", default=".agentnet/identity.json")
    message_acknowledge.add_argument("--collaboration-scope-id", required=True)
    message_acknowledge.set_defaults(func=command_message_acknowledge)

    obligation = commands.add_parser(
        "obligation",
        help="inspect and operate durable response obligations",
    )
    obligation_commands = obligation.add_subparsers(dest="obligation_command", required=True)
    obligation_list = obligation_commands.add_parser("list")
    obligation_list.add_argument("--identity", default=".agentnet/identity.json")
    obligation_list.add_argument(
        "--role", choices=("requester", "responsible", "any"), default="any"
    )
    obligation_list.add_argument("--state", action="append")
    obligation_list.add_argument("--limit", type=int, default=100)
    obligation_list.set_defaults(func=command_obligation_list)
    obligation_show = obligation_commands.add_parser("show")
    obligation_show.add_argument("obligation_id")
    obligation_show.add_argument("--identity", default=".agentnet/identity.json")
    obligation_show.set_defaults(func=command_obligation_show)
    obligation_inbox = obligation_commands.add_parser("inbox")
    obligation_inbox.add_argument("--identity", default=".agentnet/identity.json")
    obligation_inbox.set_defaults(func=command_obligation_inbox)
    obligation_transition = obligation_commands.add_parser("transition")
    obligation_transition.add_argument("obligation_id")
    obligation_transition.add_argument(
        "to_state",
        choices=(
            "recipient_committed",
            "acknowledged",
            "in_progress",
            "pending_human",
            "blocked",
        ),
    )
    obligation_transition.add_argument("--identity", default=".agentnet/identity.json")
    obligation_transition.add_argument("--reason", default="recipient_update")
    obligation_transition.add_argument("--expected-revision", type=int)
    obligation_transition.set_defaults(func=command_obligation_transition)
    obligation_cancel = obligation_commands.add_parser("cancel")
    obligation_cancel.add_argument("obligation_id")
    obligation_cancel.add_argument("--identity", default=".agentnet/identity.json")
    obligation_cancel.add_argument("--reason-code", default="requester_canceled")
    obligation_cancel.add_argument("--expected-revision", type=int)
    obligation_cancel.set_defaults(func=command_obligation_cancel)
    obligation_reconcile = obligation_commands.add_parser("reconcile")
    obligation_reconcile.add_argument("--identity", default=".agentnet/identity.json")
    obligation_reconcile.add_argument("--limit", type=int, default=100)
    obligation_reconcile.set_defaults(func=command_obligation_reconcile)

    admin = commands.add_parser("admin", help="perform signed, revision-fenced human administration")
    admin_commands = admin.add_subparsers(dest="admin_command", required=True)
    entitlement = admin_commands.add_parser("entitlement", help="issue or revoke human authority")
    entitlement_commands = entitlement.add_subparsers(
        dest="entitlement_command",
        required=True,
    )
    entitlement_issue = entitlement_commands.add_parser("issue")
    entitlement_issue.add_argument("--identity", default=".agentnet/identity.json")
    beneficiary = entitlement_issue.add_mutually_exclusive_group(required=True)
    beneficiary.add_argument("--beneficiary-identity")
    beneficiary.add_argument("--beneficiary-principal-id")
    entitlement_issue.add_argument("--entitlement-id")
    entitlement_issue.add_argument("--action", required=True)
    entitlement_issue.add_argument("--resource", required=True)
    entitlement_issue.add_argument("--revision", type=int, default=1)
    entitlement_issue.add_argument("--policy-revision", type=int, required=True)
    entitlement_issue.add_argument("--expires-in", type=int, default=86_400)
    entitlement_issue.add_argument("--reason", required=True)
    entitlement_issue.set_defaults(func=command_admin_entitlement_issue)
    entitlement_revoke = entitlement_commands.add_parser("revoke")
    entitlement_revoke.add_argument("--identity", default=".agentnet/identity.json")
    entitlement_revoke.add_argument("--entitlement-id", required=True)
    entitlement_revoke.add_argument("--expected-revision", type=int, required=True)
    entitlement_revoke.add_argument("--policy-revision", type=int, required=True)
    entitlement_revoke.add_argument("--reason", required=True)
    entitlement_revoke.set_defaults(func=command_admin_entitlement_revoke)

    harness_revocation = admin_commands.add_parser(
        "harness-revocation",
        help="prepare or commit an independently approved lost-device revocation",
    )
    harness_revocation_commands = harness_revocation.add_subparsers(
        dest="harness_revocation_command",
        required=True,
    )
    harness_prepare = harness_revocation_commands.add_parser("prepare")
    harness_prepare.add_argument("--identity", default=".agentnet/identity.json")
    harness_prepare.add_argument("--harness-id", required=True)
    harness_prepare.add_argument("--reason", required=True)
    harness_prepare.add_argument("--request", default=".agentnet/harness-revocation.json")
    harness_prepare.add_argument("--force", action="store_true")
    harness_prepare.set_defaults(func=command_admin_harness_revoke_prepare)
    harness_commit = harness_revocation_commands.add_parser("commit")
    harness_commit.add_argument("--identity", default=".agentnet/identity.json")
    harness_commit.add_argument("--request", default=".agentnet/harness-revocation.json")
    harness_commit.add_argument("--approval", required=True)
    harness_commit.set_defaults(func=command_admin_harness_revoke_commit)

    recovery = commands.add_parser("recovery", help="recover a lost device through exact OIDC and independent approval")
    recovery_commands = recovery.add_subparsers(dest="recovery_command", required=True)
    recovery_begin = recovery_commands.add_parser("begin")
    recovery_begin.add_argument("--server", required=True)
    recovery_begin.add_argument("--old-harness-id", required=True)
    recovery_begin.add_argument("--harness", required=True)
    recovery_begin.add_argument("--name", required=True)
    recovery_begin.add_argument(
        "--binding-assurance",
        choices=("os_bound", "hardware_bound"),
        required=True,
    )
    recovery_begin.add_argument("--state", default=".agentnet/recovery-pending.json")
    recovery_begin.add_argument("--private-key")
    recovery_begin.set_defaults(func=command_recovery_begin)
    recovery_complete = recovery_commands.add_parser("complete")
    recovery_complete.add_argument("--state", default=".agentnet/recovery-pending.json")
    recovery_complete.add_argument("--callback", required=True)
    recovery_complete.add_argument("--approval", action="append", required=True)
    recovery_complete.add_argument("--identity", default=".agentnet/identity.json")
    recovery_complete.add_argument("--force", action="store_true")
    recovery_complete.set_defaults(func=command_recovery_complete)

    incident = commands.add_parser("incident", help="inspect or change durable domain incident mode")
    incident_commands = incident.add_subparsers(dest="incident_command", required=True)
    incident_status = incident_commands.add_parser("status")
    incident_status.add_argument("--identity", default=".agentnet/identity.json")
    incident_status.set_defaults(func=command_incident_status)
    incident_set = incident_commands.add_parser("set")
    incident_set.add_argument("--identity", default=".agentnet/identity.json")
    incident_set.add_argument("--mode", choices=tuple(mode.value for mode in IncidentMode), required=True)
    incident_set.add_argument("--expected-revision", type=int, required=True)
    incident_set.add_argument("--policy-revision", type=int, required=True)
    incident_set.add_argument("--reason", required=True)
    incident_set.set_defaults(func=command_incident_set)

    backup = commands.add_parser(
        "backup",
        help="create a sealed offline backup without making HA or PITR claims",
    )
    backup_commands = backup.add_subparsers(dest="backup_command", required=True)
    backup_sqlite = backup_commands.add_parser(
        "sqlite",
        help="execute an owner-only offline local-profile backup",
    )
    backup_sqlite.add_argument("--config", default="agentnet.json")
    backup_sqlite.add_argument("--archive", required=True)
    backup_sqlite.add_argument("--manifest", required=True)
    backup_sqlite.add_argument("--seal", required=True)
    backup_sqlite.add_argument("--backup-id", required=True)
    backup_sqlite.add_argument("--audit-private-key", required=True)
    backup_sqlite.add_argument("--seal-private-key", required=True)
    backup_sqlite.add_argument("--application-offline", action="store_true", required=True)
    backup_sqlite.set_defaults(func=command_backup_sqlite)

    restore = commands.add_parser(
        "restore",
        help="restore a separately sealed backup to an absent offline target",
    )
    restore_commands = restore.add_subparsers(dest="restore_command", required=True)
    restore_sqlite = restore_commands.add_parser(
        "sqlite",
        help="execute an exact local-profile restore and verify its authority/audit binding",
    )
    restore_sqlite.add_argument("--config", default="agentnet.json")
    restore_sqlite.add_argument("--archive", required=True)
    restore_sqlite.add_argument("--manifest", required=True)
    restore_sqlite.add_argument("--seal", required=True)
    restore_sqlite.add_argument("--audit-public-key", required=True)
    restore_sqlite.add_argument("--target", required=True)
    restore_sqlite.add_argument("--application-offline", action="store_true", required=True)
    restore_sqlite.set_defaults(func=command_restore_sqlite)

    compromise = commands.add_parser(
        "compromise-rebuild",
        help="plan a fail-closed rebuild; never resumes service or claims rotations complete",
    )
    compromise_commands = compromise.add_subparsers(
        dest="compromise_rebuild_command",
        required=True,
    )
    compromise_plan = compromise_commands.add_parser("plan")
    compromise_plan.add_argument("--config", default="agentnet.json")
    compromise_plan.add_argument("--archive", required=True)
    compromise_plan.add_argument("--manifest", required=True)
    compromise_plan.add_argument("--seal", required=True)
    compromise_plan.add_argument("--audit-public-key", required=True)
    compromise_plan.add_argument("--target", required=True)
    compromise_plan.add_argument("--output")
    compromise_plan.add_argument("--application-offline", action="store_true", required=True)
    compromise_plan.set_defaults(func=command_compromise_rebuild_plan)

    init = commands.add_parser("init", help="initialize a local self-hosted conformance profile")
    init.add_argument("--config", default="agentnet.json")
    init.add_argument("--data-dir", default=".agentnet")
    init.add_argument("--domain", default="local.example")
    init.add_argument("--public-base-url", default="http://127.0.0.1:8080")
    init.add_argument("--force", action="store_true")
    init.set_defaults(func=command_init)

    serve = commands.add_parser("serve", help="run the self-hosted HTTP service")
    serve.add_argument("--config", default="agentnet.json")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=8080)
    serve.add_argument("--log-level", default="info")
    serve.set_defaults(func=command_serve)
    console = commands.add_parser(
        "console",
        help="operate the private AgentNet administration console",
    )
    console_commands = console.add_subparsers(dest="console_command", required=True)
    console_serve = console_commands.add_parser(
        "serve",
        help="serve only the private administration console on loopback",
    )
    console_serve.add_argument("--config", default="agentnet.json")
    console_serve.add_argument("--host", default="127.0.0.1")
    console_serve.add_argument("--port", type=int, default=8090)
    console_serve.add_argument("--log-level", default="info")
    console_serve.set_defaults(func=command_console_serve)
    console_open = console_commands.add_parser(
        "open",
        help="open a signed private console session without disclosing credentials in a URL",
    )
    console_open.add_argument("--identity", default=".agentnet/identity.json")
    console_open.add_argument("--handoff-timeout", type=float, default=10.0)
    console_open.set_defaults(func=command_console_open)


    supervisor_run = commands.add_parser(
        "supervisor-run",
        help="run an enrolled laptop/server harness supervisor until terminated",
    )
    supervisor_run.add_argument("--config", default="agentnet-supervisor.json")
    supervisor_run.add_argument(
        "--check",
        action="store_true",
        help="validate the owner-only configuration and print only non-secret fields",
    )
    supervisor_run.set_defaults(func=command_supervisor_run)

    manager_run = commands.add_parser(
        "manager-run",
        help="run interactive Pi or OMP with the packaged local signed Manager extension",
    )
    manager_run.add_argument("--identity", required=True)
    manager_run.add_argument(
        "--state-dir",
        help="owner-only local Manager gateway state directory",
    )
    manager_run.add_argument(
        "manager_command",
        nargs="+",
        metavar="COMMAND",
        help="required Pi or OMP command and arguments after --; AgentNet owns extension/tool flags",
    )
    manager_run.set_defaults(func=command_manager_run)

    status = commands.add_parser("status", help="show operational readiness without acquiring a runtime lease")
    status.add_argument("--config", default="agentnet.json")
    status.add_argument(
        "--local-only",
        action="store_true",
        help="inspect configuration/storage readiness without claiming the HTTP process is live",
    )
    status.add_argument("--timeout", type=float, default=2.0)
    status.set_defaults(func=command_status)

    bootstrap = commands.add_parser(
        "bootstrap-server-agent",
        help="provision and verify an always-on server-agent runtime without granting agent authority",
    )
    bootstrap.add_argument("--config", default="agentnet.json")
    bootstrap.set_defaults(func=command_bootstrap_server_agent)

    demo = commands.add_parser("demo", help="run an end-to-end synthetic local flow")
    demo.add_argument("--data-dir", default="/tmp/agentnet-demo")
    demo.set_defaults(func=command_demo)

    verify = commands.add_parser("verify", help="run the hermetic conformance tests")
    verify.add_argument("pytest_args", nargs="*")
    verify.set_defaults(func=command_verify)

    harness_probe = commands.add_parser(
        "harness-probe",
        help="probe exact installed harness versions without inference",
    )
    harness_probe.add_argument(
        "--harness",
        choices=("all", "claude", "codex", "pi", "antigravity"),
        default="all",
        help="use one diagnostic probe or the default four-harness G01 gate",
    )
    harness_probe.add_argument("--data-dir", default="/tmp/agentnet-harness-probes")
    harness_probe.set_defaults(func=command_harness_probe)

    harness_demo = commands.add_parser(
        "harness-demo",
        help="run the four installed deterministic background lifecycles without inference",
    )
    harness_demo.add_argument("--data-dir", default="/tmp/agentnet-harness-demo")
    harness_demo.add_argument("--request-timeout", type=float, default=5.0)
    harness_demo.set_defaults(func=command_harness_demo)

    harness_live = commands.add_parser(
        "harness-live-gate",
        help="run explicitly credentialed signed clean-worker inference evidence",
    )
    harness_live.add_argument(
        "--harness",
        choices=("all", "claude", "codex", "pi", "antigravity"),
        default="all",
    )
    harness_live.add_argument("--data-dir", default="/tmp/agentnet-harness-live")
    harness_live.add_argument("--request-timeout", type=float, default=60.0)
    harness_live.set_defaults(func=command_harness_live_gate)

    a2a_demo = commands.add_parser("a2a-demo", help="exercise the strict official-SDK A2A proposal route")
    a2a_demo.set_defaults(func=command_a2a_demo)
    return parser

