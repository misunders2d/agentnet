"""Pure rendering for the fixed ordinary server-agent systemd units."""

from __future__ import annotations

from pathlib import Path

CORE_USER = "agentnet"
APPROVAL_USER = "agentnet-approval"
C0_RESPONDER_USER = "agentnet-c0"
CORE_UNIT = "agentnet-core.service"
APPROVAL_UNIT = "agentnet-approval.service"
C0_RESPONDER_UNIT = "agentnet-c0-responder.service"
CREDENTIAL_RENEW_UNIT = "agentnet-credential-renew.service"
CREDENTIAL_RENEW_TIMER = "agentnet-credential-renew.timer"
MANAGED_UNITS = (
    APPROVAL_UNIT,
    CORE_UNIT,
    C0_RESPONDER_UNIT,
    CREDENTIAL_RENEW_UNIT,
    CREDENTIAL_RENEW_TIMER,
)
LEGACY_COMMUNICATION_ONLY_UNITS = (APPROVAL_UNIT, CORE_UNIT)
CORE_DATA = Path("/var/lib/agentnet")
APPROVAL_DATA = Path("/var/lib/agentnet-approval")
C0_RESPONDER_DATA = Path("/var/lib/agentnet-c0")
C0_RESPONDER_CONFIG = C0_RESPONDER_DATA / "config.json"
SERVER_AGENT_IDENTITY = CORE_DATA / "server-agent-identity.json"
SERVER_AGENT_KEY = CORE_DATA / "guided-join.key.pem"
CREDENTIAL_RENEW_STATE = CORE_DATA / "credential-renewal-state.json"
CORE_CONFIG = CORE_DATA / "agentnet.json"
APPROVAL_CONFIG = APPROVAL_DATA / "config.json"
SECRET_ROOT = Path("/etc/agentnet-secrets")
CORE_ENV = SECRET_ROOT / "core.env"
APPROVAL_ENV = SECRET_ROOT / "approval.env"
CORE_PORT = 8080
APPROVAL_PORT = 8090


class UnitRenderError(ValueError):
    """A fixed systemd argument cannot be rendered safely."""


def _unit_arg(value: str) -> str:
    if any(character in value for character in "\n\r\x00"):
        raise UnitRenderError("systemd unit input contains a forbidden character")
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _managed_service_runtime(data_root: Path, *, package_version: str) -> Path:
    return data_root / "npm-runtimes" / package_version


def render_managed_units(
    node_executable: Path,
    executable: Path,
    uv_executable: Path,
    *,
    package_version: str,
) -> dict[str, bytes]:
    approval_runtime = _managed_service_runtime(
        APPROVAL_DATA, package_version=package_version
    )
    core_runtime = _managed_service_runtime(CORE_DATA, package_version=package_version)
    c0_responder_runtime = _managed_service_runtime(
        C0_RESPONDER_DATA, package_version=package_version
    )
    common = "\n".join(
        (
            "Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "UnsetEnvironment=NODE_OPTIONS NODE_PATH PYTHONPATH PYTHONHOME PYTHONSTARTUP UV_CONFIG_FILE UV_PROJECT UV_WORKING_DIR VIRTUAL_ENV SSL_CERT_FILE SSL_CERT_DIR SSLKEYLOGFILE",
            "NoNewPrivileges=true",
            "PrivateTmp=true",
            "PrivateDevices=true",
            "ProtectSystem=strict",
            "ProtectHome=true",
            "ProtectKernelTunables=true",
            "ProtectKernelModules=true",
            "ProtectControlGroups=true",
            "RestrictSUIDSGID=true",
            "CapabilityBoundingSet=",
            "AmbientCapabilities=",
            "SupplementaryGroups=",
            "UMask=0077",
            "LockPersonality=true",
            "RestrictRealtime=true",
            "SystemCallArchitectures=native",
        )
    )
    approval = f"""[Unit]
Description=AgentNet independent Approval service
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
User={APPROVAL_USER}
Group={APPROVAL_USER}
EnvironmentFile={APPROVAL_ENV}
Environment=HOME={APPROVAL_DATA}
Environment=XDG_STATE_HOME={APPROVAL_DATA}/.local/state
Environment=XDG_CACHE_HOME={APPROVAL_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={approval_runtime}
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} approval serve --config {_unit_arg(str(APPROVAL_CONFIG))} --host 127.0.0.1 --port {APPROVAL_PORT}
SuccessExitStatus=143 SIGTERM
Restart=on-failure
RestartSec=2
{common}
ReadWritePaths={APPROVAL_DATA}

[Install]
WantedBy=multi-user.target
""".encode()
    core = f"""[Unit]
Description=AgentNet ordinary server agent
After=network-online.target {APPROVAL_UNIT}
Wants=network-online.target
Requires={APPROVAL_UNIT}

[Service]
Type=exec
User={CORE_USER}
Group={CORE_USER}
EnvironmentFile={CORE_ENV}
Environment=HOME={CORE_DATA}
Environment=XDG_STATE_HOME={CORE_DATA}/.local/state
Environment=XDG_CACHE_HOME={CORE_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={core_runtime}
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} serve --config {_unit_arg(str(CORE_CONFIG))} --host 127.0.0.1 --port {CORE_PORT}
SuccessExitStatus=143 SIGTERM
Restart=on-failure
RestartSec=2
{common}
ReadWritePaths={CORE_DATA}

[Install]
WantedBy=multi-user.target
""".encode()
    responder = f"""[Unit]
Description=AgentNet package-owned C0 responder
After=network-online.target {CORE_UNIT}
Wants=network-online.target
Requires={CORE_UNIT}
ConditionPathExists={C0_RESPONDER_CONFIG}
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=exec
User={C0_RESPONDER_USER}
Group={C0_RESPONDER_USER}
Environment=HOME={C0_RESPONDER_DATA}
Environment=XDG_STATE_HOME={C0_RESPONDER_DATA}/.local/state
Environment=XDG_CACHE_HOME={C0_RESPONDER_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={c0_responder_runtime}
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
LoadCredential=signing-key.pem:{SERVER_AGENT_KEY}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} c0-pilot responder --run --config {_unit_arg(str(C0_RESPONDER_CONFIG))} --credential %d/signing-key.pem
Restart=on-failure
RestartSec=2
{common}
RestrictAddressFamilies=AF_INET AF_INET6
ReadWritePaths={C0_RESPONDER_DATA}

[Install]
WantedBy=multi-user.target
""".encode()
    renewal = f"""[Unit]
Description=AgentNet current credential renewal
After=network-online.target {CORE_UNIT}
Requires={CORE_UNIT}

[Service]
Type=oneshot
User={CORE_USER}
Group={CORE_USER}
Environment=HOME={CORE_DATA}
Environment=XDG_STATE_HOME={CORE_DATA}/.local/state
Environment=XDG_CACHE_HOME={CORE_DATA}/.cache
Environment=AGENTNET_NPM_RUNTIME_DIR={core_runtime}
Environment={_unit_arg(f"AGENTNET_UV={uv_executable}")}
ExecStart={_unit_arg(str(node_executable))} {_unit_arg(str(executable))} credential renew --identity {_unit_arg(str(SERVER_AGENT_IDENTITY))} --state {_unit_arg(str(CREDENTIAL_RENEW_STATE))}
{common}
RestrictAddressFamilies=AF_INET AF_INET6
ReadWritePaths={CORE_DATA}
""".encode()
    renewal_timer = f"""[Unit]
Description=Hourly AgentNet credential renewal

[Timer]
OnActiveSec=5min
OnUnitInactiveSec=1h
Unit={CREDENTIAL_RENEW_UNIT}

[Install]
WantedBy=timers.target
""".encode()
    return {
        APPROVAL_UNIT: approval,
        CORE_UNIT: core,
        C0_RESPONDER_UNIT: responder,
        CREDENTIAL_RENEW_UNIT: renewal,
        CREDENTIAL_RENEW_TIMER: renewal_timer,
    }
