from __future__ import annotations

import argparse
import hashlib
import json
from collections.abc import Mapping
from enum import Enum
from pathlib import Path
from typing import Any

from agentnet.cli import build_parser


def _stable_value(value: Any) -> Any:
    if value is argparse.SUPPRESS:
        return "argparse.SUPPRESS"
    if isinstance(value, Enum):
        return value.value
    if isinstance(value, Path):
        return str(value)
    if isinstance(value, Mapping):
        return {
            str(key): _stable_value(item)
            for key, item in sorted(value.items(), key=lambda pair: str(pair[0]))
        }
    if isinstance(value, (list, tuple, set, frozenset)):
        return [_stable_value(item) for item in value]
    if callable(value):
        return f"{value.__module__}.{value.__qualname__}"
    if value is None or isinstance(value, (bool, int, float, str)):
        return value
    return repr(value)


def _parser_contract(parser: argparse.ArgumentParser) -> dict[str, Any]:
    actions: list[dict[str, Any]] = []
    subcommands: dict[str, dict[str, Any]] = {}
    for action in parser._actions:
        if isinstance(action, argparse._HelpAction):
            continue
        if isinstance(action, argparse._SubParsersAction):
            subcommands = {
                name: _parser_contract(child)
                for name, child in sorted(action.choices.items())
            }
            actions.append(
                {
                    "dest": action.dest,
                    "required": action.required,
                    "subcommands": sorted(action.choices),
                }
            )
            continue
        actions.append(
            {
                "choices": _stable_value(action.choices),
                "const": _stable_value(action.const),
                "default": _stable_value(action.default),
                "dest": action.dest,
                "nargs": action.nargs,
                "option_strings": list(action.option_strings),
                "required": action.required,
                "type": _stable_value(action.type),
            }
        )
    return {
        "actions": actions,
        "defaults": _stable_value(parser._defaults),
        "subcommands": subcommands,
    }


def test_complete_parser_contract_is_unchanged() -> None:
    contract = _parser_contract(build_parser())
    encoded = json.dumps(contract, sort_keys=True, separators=(",", ":")).encode()
    assert hashlib.sha256(encoded).hexdigest() == "b9232d2916463514e02592a8964e1418835ad4efdd3bb1e1cd05700ec492a442", contract
