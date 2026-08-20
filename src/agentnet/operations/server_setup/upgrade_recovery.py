"""Setup upgrade restore and rollback ownership."""

from __future__ import annotations

import base64
from pathlib import Path
from typing import Any, Mapping

from . import systemd as _systemd
from . import upgrade_state as _upgrade_state
from .custody import (
    _read_managed_unit,
    _read_private_managed_file,
    _read_setup_marker,
    _remove_managed_unit_exact,
    _write_managed_unit,
)
from .database import (
    _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA,
    _run_v0145_database_operation_as,
)
from .models import ServerSetupError
from .preflight import _MAX_CONFIG_BYTES
from .systemd import (
    APPROVAL_UNIT,
    C0_RESPONDER_UNIT,
    CORE_UNIT,
    CREDENTIAL_RENEW_TIMER,
    CREDENTIAL_RENEW_UNIT,
    MANAGED_UNITS,
)
def _restore_upgrade_systemd_state(pending: Mapping[str, Any]) -> None:
    if pending.get("service_state_changed") is not True:
        return
    journal = pending["journal"]
    previous = _upgrade_state._validated_upgrade_systemd_snapshot(journal.get("previous_systemd"))
    systemctl_executable = Path(str(pending["systemctl_executable"]))
    _systemd._run_systemctl(
        systemctl_executable,
        ["daemon-reload"],
        failure_message="systemd could not reload restored v0.1.44 units",
    )
    for unit in MANAGED_UNITS:
        state = previous[unit]
        active = state["ActiveState"] == "active"
        unit_file_state = state["UnitFileState"]
        if unit_file_state == "enabled":
            arguments = ["enable", "--now", unit] if active else ["enable", unit]
            _systemd._run_systemctl(
                systemctl_executable,
                arguments,
                failure_message="systemd could not restore v0.1.44 enablement",
            )
            if not active:
                _systemd._run_systemctl(
                    systemctl_executable,
                    ["stop", unit],
                    failure_message="systemd could not restore v0.1.44 inactive state",
                )
        elif unit_file_state == "disabled":
            _systemd._run_systemctl(
                systemctl_executable,
                ["disable", "--now", unit],
                failure_message="systemd could not restore v0.1.44 disablement",
            )
            if active:
                _systemd._run_systemctl(
                    systemctl_executable,
                    ["start", unit],
                    failure_message="systemd could not restore v0.1.44 active state",
                )
        elif unit_file_state == "static":
            _systemd._run_systemctl(
                systemctl_executable,
                ["start" if active else "stop", unit],
                failure_message="systemd could not restore v0.1.44 static unit state",
            )
        else:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "journaled v0.1.44 systemd state cannot be restored exactly",
            )
    for unit in MANAGED_UNITS:
        actual = _systemd._systemd_show(systemctl_executable, unit)
        expected = previous[unit]
        if any(
            actual.get(key) != expected[key]
            for key in ("LoadState", "UnitFileState", "ActiveState")
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "restored v0.1.44 systemd state could not be proven exact",
            )


def _rollback_pending_upgrade(pending: Mapping[str, Any]) -> None:
    """Restore only exact journaled state and retain evidence on uncertainty."""

    journal = pending.get("journal")
    if not isinstance(journal, Mapping):
        return
    previous_configs = _upgrade_state._journaled_config_payloads(journal)
    replacements = pending.get("replacement_configs", {})
    if not isinstance(replacements, Mapping):
        raise ServerSetupError("setup_upgrade_conflict", "upgrade rollback state is invalid")
    core_account = pending["core_account"]
    config_paths = dict(pending["config_paths"])
    current_configs: dict[str, bytes] = {}
    for key, path in config_paths.items():
        current = _read_private_managed_file(
            path,
            core_account,
            blocker="setup_upgrade_conflict",
            max_bytes=_MAX_CONFIG_BYTES,
        )
        replacement = replacements.get(key)
        if current != previous_configs[key] and (
            not isinstance(replacement, bytes) or current != replacement
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "managed Core config changed before upgrade rollback",
            )
        current_configs[key] = current

    previous_units = _upgrade_state._journaled_unit_payloads(journal)
    unit_paths = dict(pending["unit_paths"])
    replacements_units = pending.get("replacement_units", {})
    if set(previous_units) != set(unit_paths) or not isinstance(replacements_units, Mapping):
        raise ServerSetupError("setup_upgrade_conflict", "upgrade rollback state is invalid")
    current_units: dict[str, bytes | None] = {}
    for unit, path in unit_paths.items():
        current = _read_managed_unit(
            path,
            uid=int(pending["uid"]),
            gid=int(pending["gid"]),
            blocker="setup_upgrade_conflict",
        )
        replacement = replacements_units.get(unit)
        if current != previous_units[unit] and (
            not isinstance(replacement, bytes) or current != replacement
        ):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "managed unit changed before upgrade rollback",
            )
        current_units[unit] = current

    lifecycle_upgrade = journal.get("schema") == _LIFECYCLE_UPGRADE_JOURNAL_SCHEMA
    if lifecycle_upgrade:
        marker_payload = _read_setup_marker(
            Path(str(pending["marker_path"])),
            uid=int(pending["uid"]),
            gid=int(pending["gid"]),
        )
        try:
            previous_marker = base64.b64decode(
                str(journal["previous_marker"]),
                validate=True,
            )
        except (ValueError, TypeError) as exc:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "journaled setup marker is invalid",
            ) from exc
        if marker_payload != previous_marker:
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "setup marker changed before upgrade rollback",
            )
        if pending.get("service_state_changed") is True:
            systemctl_executable = Path(str(pending["systemctl_executable"]))
            for arguments in (
                ["disable", "--now", C0_RESPONDER_UNIT],
                ["disable", "--now", CREDENTIAL_RENEW_TIMER],
                ["stop", CREDENTIAL_RENEW_UNIT],
                ["disable", "--now", CORE_UNIT],
                ["disable", "--now", APPROVAL_UNIT],
            ):
                _systemd._run_systemctl(
                    systemctl_executable,
                    arguments,
                    failure_message="v0.1.45 services could not be quiesced for rollback",
                )
        source = _upgrade_state._validated_v0145_database_snapshot(journal.get("previous_database"))
        identity = source["identity"]
        _run_v0145_database_operation_as(
            core_account,
            str(pending["database_url"]),
            operation="rollback",
            source=source,
            domain_id=str(identity["domain_id"]),
            harness_id=str(identity["harness_id"]),
            credential_id=str(identity["credential_id"]),
            profile_key=str(identity["profile_key"]),
        )

    for key, path in config_paths.items():
        current = current_configs[key]
        if current == previous_configs[key]:
            continue
        _upgrade_state._write_journaled_core_config(path,
        previous_configs[key],
        account=core_account,
        previous=current,)
    for unit, path in unit_paths.items():
        current = current_units[unit]
        previous_payload = previous_units[unit]
        if current == previous_payload:
            continue
        if not isinstance(current, bytes):
            raise ServerSetupError(
                "setup_upgrade_conflict",
                "upgrade-created unit disappeared before rollback",
            )
        if previous_payload is None:
            _remove_managed_unit_exact(
                path,
                expected=current,
                uid=int(pending["uid"]),
                gid=int(pending["gid"]),
            )
        else:
            _write_managed_unit(
                path,
                previous_payload,
                uid=int(pending["uid"]),
                gid=int(pending["gid"]),
                previous=current,
            )
    if lifecycle_upgrade:
        _restore_upgrade_systemd_state(pending)
    try:
        _upgrade_state._clear_upgrade_journal(Path(str(pending["journal_path"])))
    except OSError as exc:
        raise ServerSetupError(
            "setup_upgrade_conflict",
            "upgrade rollback completed but its journal evidence could not be cleared",
        ) from exc
