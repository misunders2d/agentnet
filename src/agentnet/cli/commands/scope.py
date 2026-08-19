"""CLI commands and state helpers for bootstrap-plan and communication-scope."""

from __future__ import annotations

import argparse
import json
import os
import secrets
from pathlib import Path
from urllib.parse import urlsplit

from agentnet.authorization.bootstrap_plan import (
    BootstrapPlanBeginResult,
    BootstrapPlanCompleteResult,
    BootstrapPlanStatusResult,
)
from agentnet.authorization.communication_scope import (
    CommunicationScopeBeginRequest,
    CommunicationScopeBeginResult,
    CommunicationScopeCompleteRequest,
    CommunicationScopeCompleteResult,
    CommunicationScopeStatusRequest,
    CommunicationScopeStatusResult,
)
from agentnet.cli import helpers

_BOOTSTRAP_PLAN_CLI_STATE_SCHEMA = "agentnet.bootstrap-plan-cli-state.v1"
_BOOTSTRAP_PLAN_CLI_STATE_KEYS = frozenset(
    {"schema", "begin_idempotency_key", "completion_idempotency_key"}
)
_COMMUNICATION_SCOPE_CLI_STATE_SCHEMA = "agentnet.communication-scope-cli-state.v1"
_COMMUNICATION_SCOPE_CLI_STATE_KEYS = frozenset(
    {"schema", "begin_idempotency_key", "completion_idempotency_key"}
)


def _validate_scoped_cli_state(
    value: dict[str, object],
    *,
    label: str,
    schema: str,
    keys: frozenset[str],
) -> dict[str, str]:
    if set(value) != keys or value.get("schema") != schema:
        raise SystemExit(f"{label} state does not match the exact schema")
    for key in ("begin_idempotency_key", "completion_idempotency_key"):
        item = value.get(key)
        if not isinstance(item, str) or not 16 <= len(item) <= 256:
            raise SystemExit(f"{label} state does not match the exact schema")
    return {key: str(value[key]) for key in keys}


def _load_scoped_cli_state(
    path: Path,
    *,
    label: str,
    schema: str,
    keys: frozenset[str],
) -> dict[str, str]:
    resolved = path.resolve()
    try:
        value = json.loads(helpers._owner_only_file(resolved, label=f"{label} state"))
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise SystemExit(f"{label} state is not readable JSON") from exc
    if not isinstance(value, dict):
        raise SystemExit(f"{label} state does not match the exact schema")
    return _validate_scoped_cli_state(value, label=label, schema=schema, keys=keys)


def _load_or_create_scoped_cli_state(
    path: Path,
    *,
    label: str,
    schema: str,
    keys: frozenset[str],
) -> dict[str, str]:
    resolved = path.resolve()
    if os.path.lexists(resolved):
        return _load_scoped_cli_state(resolved, label=label, schema=schema, keys=keys)
    value: dict[str, object] = {
        "schema": schema,
        "begin_idempotency_key": secrets.token_urlsafe(32),
        "completion_idempotency_key": secrets.token_urlsafe(32),
    }
    helpers._write_owner_json(resolved, value, force=False)
    return _validate_scoped_cli_state(value, label=label, schema=schema, keys=keys)


def _require_scoped_approval_url(value: object, *, label: str) -> None:
    if value is None:
        return
    if not isinstance(value, str):
        raise SystemExit(f"{label} response is invalid")
    try:
        parsed = urlsplit(value)
        _ = parsed.port
    except ValueError as exc:
        raise SystemExit(f"{label} response is invalid") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path != "/approval"
        or parsed.query
        or parsed.fragment
        or value != f"https://{parsed.netloc}/approval"
    ):
        raise SystemExit(f"{label} response is invalid")


def _scoped_request_result(
    response,
    *,
    label: str,
    expected_status: int,
    model,
    exclude_none: bool = False,
    exclude_unset: bool = False,
):
    if response.status_code != expected_status:
        raise SystemExit(
            f"{label} request was rejected with HTTP {response.status_code}"
        )
    try:
        raw = response.json()
    except Exception as exc:
        raise SystemExit(f"{label} response is invalid") from exc
    models = model if isinstance(model, tuple) else (model,)
    result = None
    for candidate in models:
        try:
            result = candidate.model_validate(raw)
            break
        except Exception:
            continue
    if result is None:
        raise SystemExit(f"{label} response is invalid")
    if hasattr(result, "approval_url"):
        _require_scoped_approval_url(result.approval_url, label=label)
    dump_kwargs = {"mode": "json", "by_alias": True}
    if exclude_none:
        dump_kwargs["exclude_none"] = True
    if exclude_unset:
        dump_kwargs["exclude_unset"] = True
    return result.model_dump(**dump_kwargs)


def command_bootstrap_plan_begin(args: argparse.Namespace) -> int:
    state = _load_or_create_scoped_cli_state(
        Path(args.state),
        label="bootstrap plan",
        schema=_BOOTSTRAP_PLAN_CLI_STATE_SCHEMA,
        keys=_BOOTSTRAP_PLAN_CLI_STATE_KEYS,
    )
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/bootstrap-plan/begin",
            json_body={
                "schema": "agentnet.bootstrap-plan.begin.v1",
                "begin_idempotency_key": state["begin_idempotency_key"],
            },
        )
    finally:
        client.close()
    result = _scoped_request_result(
        response,
        label="bootstrap plan",
        expected_status=201,
        model=BootstrapPlanBeginResult,
        exclude_none=True,
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_bootstrap_plan_status(args: argparse.Namespace) -> int:
    state = _load_scoped_cli_state(
        Path(args.state),
        label="bootstrap plan",
        schema=_BOOTSTRAP_PLAN_CLI_STATE_SCHEMA,
        keys=_BOOTSTRAP_PLAN_CLI_STATE_KEYS,
    )
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/bootstrap-plan/status",
            json_body={
                "schema": "agentnet.bootstrap-plan.status.v1",
                "begin_idempotency_key": state["begin_idempotency_key"],
            },
        )
    finally:
        client.close()
    result = _scoped_request_result(
        response,
        label="bootstrap plan",
        expected_status=200,
        model=(BootstrapPlanStatusResult, BootstrapPlanCompleteResult),
        exclude_none=True,
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_bootstrap_plan_complete(args: argparse.Namespace) -> int:
    state = _load_scoped_cli_state(
        Path(args.state),
        label="bootstrap plan",
        schema=_BOOTSTRAP_PLAN_CLI_STATE_SCHEMA,
        keys=_BOOTSTRAP_PLAN_CLI_STATE_KEYS,
    )
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/bootstrap-plan/complete",
            json_body={
                "schema": "agentnet.bootstrap-plan.complete.v2",
                "begin_idempotency_key": state["begin_idempotency_key"],
                "completion_idempotency_key": state["completion_idempotency_key"],
            },
        )
    finally:
        client.close()
    result = _scoped_request_result(
        response,
        label="bootstrap plan",
        expected_status=201,
        model=BootstrapPlanCompleteResult,
        exclude_none=True,
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_communication_scope_begin(args: argparse.Namespace) -> int:
    state_path = Path(args.state).absolute()
    replace_terminal_state = bool(getattr(args, "replace_terminal_state", False))
    with helpers._private_state_lock(state_path):
        expected_state_content: bytes | None = None
        if replace_terminal_state:
            if not os.path.lexists(state_path):
                raise SystemExit(
                    "terminal replacement requires existing communication scope state"
                )
            expected_state_content = helpers._owner_only_file(
                state_path,
                label="communication scope state",
            )
            state = _load_scoped_cli_state(
                state_path,
                label="communication scope",
                schema=_COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
                keys=_COMMUNICATION_SCOPE_CLI_STATE_KEYS,
            )
        else:
            state = _load_or_create_scoped_cli_state(
                state_path,
                label="communication scope",
                schema=_COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
                keys=_COMMUNICATION_SCOPE_CLI_STATE_KEYS,
            )

        body = CommunicationScopeBeginRequest.model_validate(
            {
                "schema": "agentnet.communication-scope.begin.v1",
                "begin_idempotency_key": state["begin_idempotency_key"],
            }
        ).model_dump(mode="json", by_alias=True)
        client, _actor, _key = helpers._load_identity_client(Path(args.identity))
        try:
            response = client.request(
                "POST",
                "/v1/communication-scope/begin",
                json_body=body,
            )
            if replace_terminal_state and response.status_code != 201:
                try:
                    terminal_proof = response.json()
                except (ValueError, json.JSONDecodeError):
                    terminal_proof = None
                if response.status_code != 410 or terminal_proof != {
                    "schema": "agentnet.communication-scope.error.v1",
                    "code": "communication_scope_terminal",
                    "message": "request denied",
                    "retryable": False,
                }:
                    raise SystemExit(
                        "terminal replacement requires exact Core terminal proof"
                    )
                replacement: dict[str, object] = {
                    "schema": _COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
                    "begin_idempotency_key": secrets.token_urlsafe(32),
                    "completion_idempotency_key": secrets.token_urlsafe(32),
                }
                helpers._write_private_config(
                    state_path,
                    replacement,
                    force=True,
                    expected_content=expected_state_content,
                )
                state = _validate_scoped_cli_state(
                    replacement,
                    label="communication scope",
                    schema=_COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
                    keys=_COMMUNICATION_SCOPE_CLI_STATE_KEYS,
                )
                body = CommunicationScopeBeginRequest.model_validate(
                    {
                        "schema": "agentnet.communication-scope.begin.v1",
                        "begin_idempotency_key": state["begin_idempotency_key"],
                    }
                ).model_dump(mode="json", by_alias=True)
                response = client.request(
                    "POST",
                    "/v1/communication-scope/begin",
                    json_body=body,
                )
        finally:
            client.close()
        result = _scoped_request_result(
            response,
            label="communication scope",
            expected_status=201,
            model=CommunicationScopeBeginResult,
            exclude_unset=True,
        )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_communication_scope_status(args: argparse.Namespace) -> int:
    state = _load_scoped_cli_state(
        Path(args.state),
        label="communication scope",
        schema=_COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
        keys=_COMMUNICATION_SCOPE_CLI_STATE_KEYS,
    )
    body = CommunicationScopeStatusRequest.model_validate(
        {
            "schema": "agentnet.communication-scope.status.v1",
            "begin_idempotency_key": state["begin_idempotency_key"],
        }
    ).model_dump(mode="json", by_alias=True)
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/communication-scope/status",
            json_body=body,
        )
    finally:
        client.close()
    result = _scoped_request_result(
        response,
        label="communication scope",
        expected_status=200,
        model=(CommunicationScopeStatusResult, CommunicationScopeCompleteResult),
        exclude_unset=True,
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_communication_scope_complete(args: argparse.Namespace) -> int:
    state = _load_scoped_cli_state(
        Path(args.state),
        label="communication scope",
        schema=_COMMUNICATION_SCOPE_CLI_STATE_SCHEMA,
        keys=_COMMUNICATION_SCOPE_CLI_STATE_KEYS,
    )
    body = CommunicationScopeCompleteRequest.model_validate(
        {
            "schema": "agentnet.communication-scope.complete.v1",
            "begin_idempotency_key": state["begin_idempotency_key"],
            "completion_idempotency_key": state["completion_idempotency_key"],
        }
    ).model_dump(mode="json", by_alias=True)
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/communication-scope/complete",
            json_body=body,
        )
    finally:
        client.close()
    result = _scoped_request_result(
        response,
        label="communication scope",
        expected_status=201,
        model=CommunicationScopeCompleteResult,
        exclude_unset=True,
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0
