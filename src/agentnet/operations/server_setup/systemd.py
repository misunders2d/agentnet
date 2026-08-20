"""Fixed systemd rendering, inspection, validation, and activation."""

from __future__ import annotations

import json
import os
import subprocess
import time
from pathlib import Path
from typing import Callable, Mapping

from .models import ServerSetupError, SetupLayout, SYSTEMD_UNIT_ROOT

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

_SYSTEMD_UNIT_SEARCH_ROOTS = (
    Path("/etc/systemd/system.control"),
    Path("/run/systemd/system.control"),
    Path("/run/systemd/transient"),
    Path("/run/systemd/generator.early"),
    SYSTEMD_UNIT_ROOT,
    Path("/etc/systemd/system.attached"),
    Path("/run/systemd/system"),
    Path("/run/systemd/system.attached"),
    Path("/run/systemd/generator"),
    Path("/usr/local/lib/systemd/system"),
    Path("/usr/local/share/systemd/system"),
    Path("/usr/lib/systemd/system"),
    Path("/usr/share/systemd/system"),
    Path("/lib/systemd/system"),
    Path("/run/systemd/generator.late"),
)
_SYSTEMCTL_TIMEOUT_SECONDS = 30
_SYSTEM_PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"


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


def _require_no_unit_overrides(layout: SetupLayout, unit: str) -> None:
    override_paths = (
        Path(f"/etc/systemd/system/{unit}.d"),
        Path(f"/run/systemd/system/{unit}.d"),
        Path(f"/etc/systemd/system.control/{unit}.d"),
        Path(f"/run/systemd/system.control/{unit}.d"),
        Path(f"/usr/local/lib/systemd/system/{unit}.d"),
        Path(f"/usr/lib/systemd/system/{unit}.d"),
        Path(f"/lib/systemd/system/{unit}.d"),
    )
    if any(
        layout.host(path).exists() or layout.host(path).is_symlink()
        for path in override_paths
    ):
        raise ServerSetupError(
            "unit_override_conflict",
            "managed AgentNet unit has unsupported overrides",
        )


def _require_absent_topology_upgrade_unit(
    layout: SetupLayout,
    systemctl_executable: Path,
    unit: str,
    *,
    journaled: bool,
) -> None:
    """Prove one target-only unit is absent/inert before topology expansion."""

    for root in _SYSTEMD_UNIT_SEARCH_ROOTS:
        candidate = layout.host(root / unit)
        present = candidate.exists() or candidate.is_symlink()
        if present and (not journaled or candidate != layout.unit(unit)):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "target-only managed unit exists before topology upgrade",
            )
    properties = _systemd_show(systemctl_executable, unit)
    service_pid_invalid = (
        unit.endswith(".service") and properties.get("MainPID") not in {"", "0"}
    )
    load_state = properties.get("LoadState")
    unit_file_state = properties.get("UnitFileState")
    fragment_path = properties.get("FragmentPath", "")
    absent = (
        load_state == "not-found"
        and unit_file_state in {"", "disabled"}
        and not fragment_path
    )
    inert_journaled = (
        journaled
        and load_state == "loaded"
        and unit_file_state in {"disabled", "static"}
        and fragment_path == str(layout.unit(unit))
    )
    if (
        not (absent or inert_journaled)
        or properties.get("ActiveState") != "inactive"
        or properties.get("DropInPaths", "").strip()
        or service_pid_invalid
    ):
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "target-only managed unit is loaded, enabled, or active before topology upgrade",
        )


def _run_systemctl(
    executable: Path,
    arguments: list[str],
    *,
    failure_message: str,
) -> None:
    try:
        completed = subprocess.run(
            [str(executable), *arguments],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupError("systemd_start", failure_message) from exc
    if completed.returncode != 0:
        raise ServerSetupError("systemd_start", failure_message)


_SYSTEMD_SHOW_PROPERTIES = (
    "LoadState",
    "UnitFileState",
    "ActiveState",
    "FragmentPath",
    "DropInPaths",
    "User",
    "Group",
    "NoNewPrivileges",
    "PrivateDevices",
    "PrivateTmp",
    "ProtectHome",
    "ProtectSystem",
    "MainPID",
    "Environment",
    "ReadWritePaths",
)


def _systemd_show(executable: Path, unit: str) -> dict[str, str]:
    """Read exact live systemd properties for one managed unit."""

    if any(character in unit for character in "\n\r\x00"):
        raise ServerSetupError(
            "unit_input",
            "systemd unit input contains a forbidden character",
        )
    try:
        completed = subprocess.run(
            [
                str(executable),
                "show",
                unit,
                *(f"--property={name}" for name in _SYSTEMD_SHOW_PROPERTIES),
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit runtime is not inspectable",
        ) from exc
    stdout = completed.stdout or b""
    if completed.returncode != 0 or len(stdout) > 262_144:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit runtime is not inspectable",
        )
    try:
        text = stdout.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit runtime evidence is invalid",
        ) from exc
    properties: dict[str, str] = {}
    for line in text.splitlines():
        name, separator, value = line.partition("=")
        if separator != "=" or name not in _SYSTEMD_SHOW_PROPERTIES:
            continue
        properties[name] = value
    return properties


def _validate_inactive_auxiliary_unit_state(
    *,
    unit: str,
    expected_unit_file_state: str,
    properties: dict[str, str],
) -> None:
    """Validate one inactive auxiliary unit without requiring service-only fields."""

    service_pid_invalid = (
        unit.endswith(".service") and properties.get("MainPID") not in {"", "0"}
    )
    if (
        properties.get("LoadState") != "loaded"
        or properties.get("UnitFileState") != expected_unit_file_state
        or properties.get("ActiveState") != "inactive"
        or service_pid_invalid
    ):
        raise ServerSetupError(
            "service_runtime_binding",
            "inactive AgentNet auxiliary unit does not match fixed state",
        )


def _verify_upgrade_quiescence(systemctl_executable: Path) -> None:
    """Require every managed service to match the fixed inactive upgrade state."""

    expected_states = {
        APPROVAL_UNIT: "disabled",
        CORE_UNIT: "disabled",
        C0_RESPONDER_UNIT: "disabled",
        CREDENTIAL_RENEW_UNIT: "static",
        CREDENTIAL_RENEW_TIMER: "disabled",
    }
    for unit, expected_state in expected_states.items():
        _validate_inactive_auxiliary_unit_state(
            unit=unit,
            expected_unit_file_state=expected_state,
            properties=_systemd_show(systemctl_executable, unit),
        )


def _verify_live_service_state(
    *,
    systemctl_executable: Path,
    layout: SetupLayout,
    node_executable: Path,
    agentnet_executable: Path,
    uv_executable: Path,
    identity_enrolled: bool,
    c0_responder_required: bool,
    auxiliary_ready: bool,
) -> None:
    """Verify exact systemd bindings for the current activation phase."""

    active_units = [APPROVAL_UNIT, CORE_UNIT]
    if auxiliary_ready and identity_enrolled:
        active_units.append(CREDENTIAL_RENEW_TIMER)
    if auxiliary_ready and c0_responder_required:
        active_units.append(C0_RESPONDER_UNIT)
    for unit in active_units:
        _run_systemctl(
            systemctl_executable,
            ["is-active", "--quiet", unit],
            failure_message="AgentNet managed unit is not active",
        )
    _validate_systemd_service_runtime(
        systemctl_executable,
        unit=APPROVAL_UNIT,
        user=APPROVAL_USER,
        data_root=layout.host(APPROVAL_DATA),
        node_executable=node_executable,
        agentnet_executable=agentnet_executable,
        uv_executable=uv_executable,
        expected_argv=(
            str(node_executable),
            str(agentnet_executable),
            "approval",
            "serve",
            "--config",
            str(layout.host(APPROVAL_CONFIG)),
            "--host",
            "127.0.0.1",
            "--port",
            str(APPROVAL_PORT),
        ),
        layout=layout,
    )
    _validate_systemd_service_runtime(
        systemctl_executable,
        unit=CORE_UNIT,
        user=CORE_USER,
        data_root=layout.host(CORE_DATA),
        node_executable=node_executable,
        agentnet_executable=agentnet_executable,
        uv_executable=uv_executable,
        expected_argv=(
            str(node_executable),
            str(agentnet_executable),
            "serve",
            "--config",
            str(layout.host(CORE_CONFIG)),
            "--host",
            "127.0.0.1",
            "--port",
            str(CORE_PORT),
        ),
        layout=layout,
    )
    if auxiliary_ready and c0_responder_required:
        _validate_systemd_service_runtime(
            systemctl_executable,
            unit=C0_RESPONDER_UNIT,
            user=C0_RESPONDER_USER,
            data_root=layout.host(C0_RESPONDER_DATA),
            node_executable=node_executable,
            agentnet_executable=agentnet_executable,
            uv_executable=uv_executable,
            expected_argv=(
                str(node_executable),
                str(agentnet_executable),
                "c0-pilot",
                "responder",
                "--run",
                "--config",
                str(layout.host(C0_RESPONDER_CONFIG)),
                "--credential",
                "/run/credentials/agentnet-c0-responder.service/signing-key.pem",
            ),
            layout=layout,
        )
    expected_states = {CREDENTIAL_RENEW_UNIT: "static"}
    if not auxiliary_ready or not c0_responder_required:
        expected_states[C0_RESPONDER_UNIT] = "disabled"
    if not auxiliary_ready or not identity_enrolled:
        expected_states[CREDENTIAL_RENEW_TIMER] = "disabled"
    for unit, expected_state in expected_states.items():
        _validate_inactive_auxiliary_unit_state(
            unit=unit,
            expected_unit_file_state=expected_state,
            properties=_systemd_show(systemctl_executable, unit),
        )
    if auxiliary_ready and identity_enrolled:
        _validate_active_renewal_timer_state(
            _systemd_show(systemctl_executable, CREDENTIAL_RENEW_TIMER),
            next_run_usec=_systemd_timer_next_run(
                systemctl_executable,
                CREDENTIAL_RENEW_TIMER,
            ),
            now_usec=time.time_ns() // 1_000,
        )


def _systemd_timer_next_run(executable: Path, unit: str) -> int | None:
    """Return the exact next realtime activation in microseconds, if scheduled."""

    if any(character in unit for character in "\n\r\x00"):
        raise ServerSetupError(
            "unit_input",
            "systemd unit input contains a forbidden character",
        )
    try:
        completed = subprocess.run(
            [
                str(executable),
                "list-timers",
                unit,
                "--no-pager",
                "--no-legend",
                "--plain",
                "--output=json",
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env={"PATH": _SYSTEM_PATH, "HOME": "/root", "LANG": "C.UTF-8"},
            timeout=_SYSTEMCTL_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal schedule is not inspectable",
        ) from exc
    stdout = completed.stdout or b""
    if completed.returncode != 0 or len(stdout) > 262_144:
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal schedule is not inspectable",
        )
    try:
        rows = json.loads(stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal schedule evidence is invalid",
        ) from exc
    if (
        not isinstance(rows, list)
        or len(rows) != 1
        or not isinstance(rows[0], dict)
        or rows[0].get("unit") != unit
    ):
        return None
    next_run = rows[0].get("next")
    if isinstance(next_run, bool) or not isinstance(next_run, int):
        return None
    return next_run


def _validate_active_renewal_timer_state(
    properties: Mapping[str, str],
    *,
    next_run_usec: int | None,
    now_usec: int,
) -> None:
    """Require an active timer with one finite future activation."""

    if (
        properties.get("LoadState") != "loaded"
        or properties.get("UnitFileState") != "enabled"
        or properties.get("ActiveState") != "active"
        or next_run_usec is None
        or next_run_usec <= now_usec
    ):
        raise ServerSetupError(
            "credential_renewal_schedule",
            "credential renewal timer has no finite future run",
        )


def _credential_renewal_activation_commands(
    *,
    c0_responder_required: bool,
) -> tuple[list[str], ...]:
    """Return the race-free auxiliary activation sequence."""

    return (
        ["stop", CREDENTIAL_RENEW_UNIT],
        ["reset-failed", CREDENTIAL_RENEW_UNIT],
        (
            ["enable", "--now", C0_RESPONDER_UNIT]
            if c0_responder_required
            else ["disable", "--now", C0_RESPONDER_UNIT]
        ),
        ["enable", "--now", CREDENTIAL_RENEW_TIMER],
    )


def _read_live_process_identity(pid: int) -> tuple[Path, tuple[str, ...]]:
    """Read the exact executable and argv of one live managed service process."""

    try:
        executable = Path(os.readlink(f"/proc/{pid}/exe"))
        with open(f"/proc/{pid}/cmdline", "rb") as handle:
            raw = handle.read(262_145)
    except OSError as exc:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service process is not inspectable",
        ) from exc
    if len(raw) > 262_144:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service process evidence is oversized",
        )
    try:
        arguments = tuple(raw.decode("utf-8").split("\x00"))
    except UnicodeDecodeError as exc:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service process evidence is invalid",
        ) from exc
    while arguments and arguments[-1] == "":
        arguments = arguments[:-1]
    return executable, arguments


def _validate_systemd_service_runtime(
    systemctl_executable: Path,
    *,
    unit: str,
    user: str,
    data_root: Path,
    node_executable: Path,
    agentnet_executable: Path,
    uv_executable: Path,
    expected_argv: tuple[str, ...],
    layout: SetupLayout,
) -> None:
    """Prove the live unit runs exactly the package-owned hermetic runtime.

    Unit files on disk are not evidence: systemd may still be running a stale
    fragment, an operator drop-in may have replaced the sandbox or the command,
    and a PATH-selected runtime would be a different executable from the one the
    approved digest bound. This checks the loaded fragment, the sandbox, and the
    live process itself.
    """

    properties = _systemd_show(systemctl_executable, unit)
    expected_fragment = layout.unit(unit)
    if (
        properties.get("LoadState") != "loaded"
        or properties.get("UnitFileState") != "enabled"
        or properties.get("FragmentPath") != str(expected_fragment)
        or properties.get("DropInPaths", "").strip()
        or properties.get("User") != user
        or properties.get("Group") != user
        or properties.get("NoNewPrivileges") != "yes"
        or properties.get("PrivateDevices") != "yes"
        or properties.get("PrivateTmp") != "yes"
        or properties.get("ProtectHome") != "yes"
        or properties.get("ProtectSystem") != "strict"
    ):
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit is not running the exact managed fragment and sandbox",
        )
    if f"AGENTNET_UV={uv_executable}" not in properties.get(
        "Environment", ""
    ).split():
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit does not bind the package-owned uv runtime",
        )
    if str(data_root) not in properties.get("ReadWritePaths", "").split():
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit does not bind the fixed private state root",
        )
    main_pid = properties.get("MainPID", "0")
    if not main_pid.isdecimal() or int(main_pid) < 1:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet unit has no live main process",
        )
    live_executable, live_argv = _read_live_process_identity(int(main_pid))
    if live_executable != node_executable:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service executable does not match the approved hermetic runtime",
        )
    if live_argv != tuple(expected_argv):
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service argv does not match the approved hermetic runtime",
        )
    if str(agentnet_executable) not in live_argv:
        raise ServerSetupError(
            "service_runtime",
            "managed AgentNet service process does not run the approved package launcher",
        )


def _run_systemctl_sequence_or_reconcile(
    executable: Path,
    sequence: tuple[list[str], ...],
    *,
    reconcile: Callable[[], None],
) -> str:
    """Apply the managed unit sequence, reconciling one lost systemd response.

    A timed-out or failed ``systemctl`` call proves nothing about the effect: the
    unit may already be exactly in the intended end state. On failure the exact
    live end state is verified instead; only that verified evidence, never the
    transport outcome, is allowed to report success.
    """

    first_failure: ServerSetupError | None = None
    for arguments in sequence:
        try:
            _run_systemctl(
                executable,
                arguments,
                failure_message="failed to start AgentNet managed units",
            )
        except ServerSetupError as failure:
            # A transport failure proves neither success nor failure. Continue
            # the bounded idempotent sequence, then accept only its exact final
            # live postcondition. This prevents an early lost response from
            # silently skipping a later Core restart.
            if first_failure is None:
                first_failure = failure
    if first_failure is None:
        return "completed"
    try:
        reconcile()
    except ServerSetupError:
        raise first_failure from None
    return "reconciled_after_response_loss"
