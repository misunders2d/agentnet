from __future__ import annotations

import json
import re
from hashlib import sha256
from pathlib import Path

from agentnet.adapters import omp, pi
from agentnet.bindings.endpoint import EndpointBinding
from agentnet.bindings.tools import CANONICAL_TOOL_NAMES


ROOT = Path(__file__).resolve().parents[2]
EXTENSION = ROOT / "src" / "agentnet" / "bindings" / "pi_extension.ts"


def _source() -> str:
    return EXTENSION.read_text(encoding="utf-8")


def _parameter_source(source: str, tool_name: str) -> str:
    registration = source.split(f'name: "{tool_name}"', 1)[1]
    schema = registration.split("parameters: Type.Object({", 1)[1]
    return schema.split("}, { additionalProperties: false })", 1)[0]


def test_pi_extension_registers_the_exact_canonical_surface() -> None:
    registered = set(re.findall(r'\bname: "(agentnet_[a-z_]+)"', _source()))
    expected = {name.replace(".", "_") for name in CANONICAL_TOOL_NAMES}

    assert registered == expected


def test_omp_reuses_pi_extension_loader_contract() -> None:
    assert omp.EXTENSION_MANIFEST_KEY == pi.EXTENSION_MANIFEST_KEY == "pi"
    assert omp.EXTENSION_MODULE == pi.EXTENSION_MODULE
    assert omp.CAPABILITIES.harness == "omp"
    assert pi.CAPABILITIES.harness == "pi"
    assert omp.CAPABILITIES.local_binding == pi.CAPABILITIES.local_binding == "direct_ipc"
    manifest = json.loads((ROOT / "package.json").read_text(encoding="utf-8"))
    assert manifest[omp.EXTENSION_MANIFEST_KEY]["extensions"] == [
        f"./{omp.EXTENSION_MODULE}"
    ]


def test_omp_and_pi_launch_with_distinct_exact_endpoint_bindings(tmp_path: Path) -> None:
    def binding(harness_kind: str) -> EndpointBinding:
        harness_id = f"{harness_kind}-harness"
        generation = 7
        endpoint_scope = sha256(
            f"domain-1\0{harness_id}\0{generation}".encode("utf-8")
        ).hexdigest()
        capability_root = (
            tmp_path
            / "capabilities"
            / "endpoints"
            / endpoint_scope
            / "capability-root.key"
        )
        capability_root.parent.mkdir(parents=True, mode=0o700)
        capability_root.parent.chmod(0o700)
        capability_root.write_bytes(bytes(range(32)))
        capability_root.chmod(0o600)
        return EndpointBinding(
            domain_id="domain-1",
            principal_id="principal-1",
            harness_id=harness_id,
            harness_kind=harness_kind,
            credential_id=f"{harness_kind}-credential",
            credential_epoch=3,
            adapter_generation=generation,
            mailbox_cursor=0,
            profile_key=f"{harness_kind}:work",
            capability_root_path=capability_root,
            process_measurement=f"pid:{harness_kind}:start:1",
        )

    omp_binding = binding("omp")
    pi_binding = binding("pi")
    launch_root = tmp_path / "launches"
    omp_spec = omp.launch_spec(
        harness_id=omp_binding.harness_id,
        root=launch_root,
        executable="omp",
        local_bindings=True,
        endpoint_binding=omp_binding,
    )
    pi_spec = pi.launch_spec(
        harness_id=pi_binding.harness_id,
        root=launch_root,
        executable="pi",
        local_bindings=True,
        endpoint_binding=pi_binding,
    )

    assert omp_spec.profile_key == "omp:work"
    assert pi_spec.profile_key == "pi:work"
    omp_profile_index = omp_spec.arguments.index("--profile")
    assert omp_spec.arguments[omp_profile_index + 1] == omp_binding.profile_key
    assert "--profile" not in pi_spec.arguments
    assert omp_spec.capability_root_path != pi_spec.capability_root_path
    assert omp_spec.endpoint_descriptor_path != pi_spec.endpoint_descriptor_path
    assert omp_spec.canonical_tool_names == pi_spec.canonical_tool_names
    assert omp_spec.canonical_tool_names == CANONICAL_TOOL_NAMES
    assert omp_spec.foreground_session_id is pi_spec.foreground_session_id is None
    assert omp_spec.endpoint_descriptor_path is not None
    assert pi_spec.endpoint_descriptor_path is not None
    omp_descriptor = json.loads(omp_spec.endpoint_descriptor_path.read_text(encoding="utf-8"))
    pi_descriptor = json.loads(pi_spec.endpoint_descriptor_path.read_text(encoding="utf-8"))
    assert omp_descriptor["refresh_behavior"] == "restart_required"
    assert pi_descriptor["refresh_behavior"] == "restart_required"
    assert omp_descriptor["harness_id"] == omp_binding.harness_id
    assert pi_descriptor["harness_id"] == pi_binding.harness_id


def test_pi_compatible_extensions_inject_no_foreground_turns() -> None:
    for adapter in (omp, pi):
        assert adapter.CAPABILITIES.foreground_message_methods == ()
    source = _source()
    assert "pi.on(" not in source
    assert "pi.sendMessage(" not in source
    assert "pi.appendEntry(" not in source


def test_extension_uses_only_the_supervisor_sealed_binding_locator() -> None:
    source = _source()

    assert "AGENTNET_LOCAL_BINDING_FD" in source
    assert "AGENTNET_LOCAL_BINDING_ENDPOINT" in source
    assert "identity.json" not in source
    assert "homedir(" not in source
    assert "process.env.HOME" not in source


def test_v0145_tools_do_not_accept_caller_identity_overrides() -> None:
    source = _source()
    expected_parameters = {
        "agentnet_inbox": {"after_cursor", "collaboration_scope_id", "limit"},
        "agentnet_inbox_acknowledge": {
            "collaboration_scope_id",
            "envelope_digest",
            "event_id",
        },
        "agentnet_recipient_resolve": {"query"},
        "agentnet_file_send": {
            "classification",
            "collaboration_scope_id",
            "idempotency_key",
            "media_type",
            "recipients",
            "source_path",
        },
        "agentnet_file_status": {"collaboration_scope_id", "transfer_id"},
        "agentnet_file_download": {
            "artifact_id",
            "collaboration_scope_id",
            "destination_path",
            "idempotency_key",
        },
        "agentnet_conversation_create": {
            "classification",
            "collaboration_scope_id",
            "conversation_id",
            "member_harness_ids",
        },
        "agentnet_conversation_action": {
            "action",
            "collaboration_scope_id",
            "conversation_id",
            "idempotency_key",
            "recipients",
            "thread_id",
        },
        "agentnet_conversation_thread": {
            "collaboration_scope_id",
            "conversation_id",
            "limit",
            "thread_id",
        },
        "agentnet_room_create": {
            "classification",
            "collaboration_scope_id",
            "expires_at",
            "persistent",
            "policy",
        },
        "agentnet_room_member_add": {
            "collaboration_scope_id",
            "harness_id",
            "role",
            "room_id",
        },
        "agentnet_room_get": {"collaboration_scope_id", "room_id"},
        "agentnet_room_send": {
            "classification",
            "collaboration_scope_id",
            "conversation_id",
            "expected_control_sequence",
            "idempotency_key",
            "payload",
            "recipients",
            "room_id",
        },
        "agentnet_obligation_inbox": {"collaboration_scope_id"},
        "agentnet_obligation_list": {
            "collaboration_scope_id",
            "limit",
            "role",
            "states",
        },
        "agentnet_obligation_get": {"collaboration_scope_id", "obligation_id"},
        "agentnet_obligation_transition": {
            "collaboration_scope_id",
            "expected_revision",
            "obligation_id",
            "reason",
            "to_state",
        },
        "agentnet_obligation_cancel": {
            "collaboration_scope_id",
            "expected_revision",
            "obligation_id",
            "reason_code",
        },
        "agentnet_obligation_reconcile": {"collaboration_scope_id", "limit"},
    }
    forbidden = {
        "actor",
        "caller_harness_id",
        "capability",
        "credential",
        "credential_id",
        "domain_id",
        "identity",
        "identity_path",
        "principal_id",
        "profile_key",
    }

    for tool_name, expected in expected_parameters.items():
        parameters = _parameter_source(source, tool_name)
        declared = set(re.findall(r"^\s*([a-z_]+): Type\.", parameters, re.MULTILINE))
        assert declared == expected
        assert declared.isdisjoint(forbidden)
