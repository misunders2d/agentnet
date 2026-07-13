#!/usr/bin/env python3
"""Contract-faithful local doubles for installed harness CLI surfaces.

The executable basename selects a native protocol.  This fixture deliberately
does not understand an AgentNet control method: Claude speaks Agent SDK stream JSON,
Codex speaks app-server JSONL, Pi speaks RPC JSONL, and Antigravity is a
serialized print process.
"""

from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path
from typing import Any


VERSIONS = {
    "claude": "2.1.207 (Claude Code)",
    "codex": "codex-cli 0.144.3",
    "pi": "0.80.6",
    "agy": "1.1.1",
}


def executable_name() -> str:
    return os.path.basename(sys.argv[0])


def emit(value: dict[str, Any]) -> None:
    sys.stdout.write(
        json.dumps(value, allow_nan=False, separators=(",", ":"), sort_keys=True) + "\n"
    )
    sys.stdout.flush()


def record(kind: str, value: Any) -> None:
    state_dir = os.environ.get("AGENTNET_STATE_DIR")
    if state_dir is None:
        return
    path = Path(state_dir) / "native-fixture.log"
    entry = {
        "argv": sys.argv[1:],
        "cwd": os.getcwd(),
        "environment_bindings": {
            name: os.environ[name]
            for name in (
                "CODEX_HOME",
                "HOME",
                "PI_CODING_AGENT_DIR",
                "TMPDIR",
                "XDG_CACHE_HOME",
                "XDG_CONFIG_HOME",
                "XDG_DATA_HOME",
            )
            if name in os.environ
        },
        "environment_keys": sorted(os.environ),
        "kind": kind,
        "value": value,
    }
    with path.open("a", encoding="utf-8") as stream:
        stream.write(json.dumps(entry, allow_nan=False, separators=(",", ":"), sort_keys=True))
        stream.write("\n")


def prompt_text_from_claude(message: dict[str, Any]) -> str:
    body = message.get("message")
    if not isinstance(body, dict) or body.get("role") != "user":
        raise ValueError("Claude fixture requires a native user message")
    content = body.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(
            part.get("text", "")
            for part in content
            if isinstance(part, dict)
            and part.get("type") == "text"
            and isinstance(part.get("text"), str)
        )
    raise ValueError("Claude fixture requires native message content")


def serve_claude() -> int:
    record("launch", None)
    turn = 0
    for line in sys.stdin:
        if not line.strip():
            continue
        request = json.loads(line)
        record("claude_input", request)
        if request.get("type") != "user" or request.get("parent_tool_use_id") is not None:
            raise ValueError("invalid Claude Agent SDK input")
        session_id = request.get("session_id")
        if not isinstance(session_id, str) or not session_id:
            raise ValueError("Claude Agent SDK input omitted the session id")
        prompt = prompt_text_from_claude(request)
        turn += 1
        result_text = f"claude:{prompt}"
        emit(
            {
                "type": "assistant",
                "message": {
                    "role": "assistant",
                    "content": [{"type": "text", "text": result_text}],
                },
                "parent_tool_use_id": None,
                "session_id": session_id,
                "uuid": f"fixture-claude-assistant-{turn}",
            }
        )
        emit(
            {
                "type": "result",
                "subtype": "success",
                "is_error": False,
                "result": result_text,
                "session_id": session_id,
                "uuid": f"fixture-claude-result-{turn}",
            }
        )
    return 0


def codex_response(request_id: Any, result: dict[str, Any]) -> None:
    emit({"id": request_id, "result": result})


def codex_error(request_id: Any, message: str) -> None:
    emit({"id": request_id, "error": {"code": -32601, "message": message}})


def codex_prompt(params: dict[str, Any]) -> str:
    input_items = params.get("input")
    if not isinstance(input_items, list):
        raise ValueError("Codex turn/start omitted input")
    return "".join(
        item.get("text", "")
        for item in input_items
        if isinstance(item, dict)
        and item.get("type") == "text"
        and isinstance(item.get("text"), str)
    )


def serve_codex() -> int:
    record("launch", None)
    thread_id = "fixture-codex-thread"
    turn_number = 0
    for line in sys.stdin:
        if not line.strip():
            continue
        request = json.loads(line)
        record("codex_message", request)
        method = request.get("method")
        if method == "initialized" and "id" not in request:
            continue
        request_id = request.get("id")
        params = request.get("params")
        if request_id is None or not isinstance(method, str) or not isinstance(params, dict):
            raise ValueError("invalid Codex app-server request")
        if method == "initialize":
            codex_response(
                request_id,
                {
                    "codexHome": os.environ.get("CODEX_HOME", ""),
                    "platformFamily": "unix",
                    "platformOs": "linux",
                    "userAgent": "agentnet-native-contract-fixture/1",
                },
            )
        elif method in {"thread/start", "thread/resume"}:
            if method == "thread/resume" and params.get("threadId") != thread_id:
                codex_error(request_id, "unknown fixture thread")
                continue
            codex_response(
                request_id,
                {
                    "approvalPolicy": "never",
                    "approvalsReviewer": "user",
                    "cwd": os.getcwd(),
                    "model": "fixture-model",
                    "modelProvider": "fixture",
                    "sandbox": {"type": "readOnly", "networkAccess": False},
                    "thread": {"id": thread_id},
                },
            )
        elif method == "turn/start":
            if params.get("threadId") != thread_id:
                codex_error(request_id, "turn crossed fixture thread")
                continue
            turn_number += 1
            turn_id = f"fixture-codex-turn-{turn_number}"
            output = f"codex:{codex_prompt(params)}"
            codex_response(
                request_id,
                {"turn": {"id": turn_id, "items": [], "status": "inProgress", "error": None}},
            )
            emit(
                {
                    "method": "item/completed",
                    "params": {
                        "threadId": thread_id,
                        "turnId": turn_id,
                        "item": {"id": f"fixture-item-{turn_number}", "type": "agentMessage", "text": output},
                    },
                }
            )
            emit(
                {
                    "method": "turn/completed",
                    "params": {
                        "threadId": thread_id,
                        "turn": {"id": turn_id, "items": [], "status": "completed", "error": None},
                    },
                }
            )
        else:
            codex_error(request_id, f"unsupported fixture method: {method}")
    return 0


def pi_state() -> dict[str, Any]:
    return {
        "model": {"id": "fixture-model", "provider": "fixture"},
        "thinkingLevel": "off",
        "isStreaming": False,
        "isCompacting": False,
        "steeringMode": "one-at-a-time",
        "followUpMode": "one-at-a-time",
        "sessionId": os.environ.get("AGENTNET_BACKGROUND_SESSION_ID", "fixture-pi-session"),
        "sessionFile": None,
        "sessionName": "agentnet-native-contract-fixture",
        "autoCompactionEnabled": False,
        "messageCount": 0,
        "pendingMessageCount": 0,
    }


def serve_pi() -> int:
    record("launch", None)
    last_text = ""
    for line in sys.stdin:
        if not line.strip():
            continue
        request = json.loads(line)
        record("pi_command", request)
        request_id = request.get("id")
        command = request.get("type")
        if not isinstance(request_id, str) or not isinstance(command, str):
            raise ValueError("invalid Pi RPC command")
        if command == "get_state":
            data = pi_state()
        elif command == "prompt":
            message = request.get("message")
            if not isinstance(message, str):
                raise ValueError("Pi prompt command omitted message")
            last_text = f"pi:{message}"
            data = {}
        elif command == "get_last_assistant_text":
            data = {"text": last_text}
        else:
            emit(
                {
                    "type": "response",
                    "id": request_id,
                    "command": command,
                    "success": False,
                    "error": f"unsupported fixture command: {command}",
                }
            )
            continue
        emit(
            {
                "type": "response",
                "id": request_id,
                "command": command,
                "success": True,
                "data": data,
            }
        )
        if command == "prompt":
            emit({"type": "agent_settled"})
    return 0


def serve_antigravity() -> int:
    record("antigravity_print", {"prompt": sys.argv[-1] if len(sys.argv) > 1 else ""})
    prompt = sys.argv[-1] if len(sys.argv) > 1 else ""
    if prompt == "fixture:fail":
        return 41
    if prompt == "fixture:hang":
        time.sleep(30)
        return 42
    sys.stdout.write(f"antigravity:{prompt}\n")
    return 0


def main() -> int:
    executable = executable_name()
    if "--version" in sys.argv[1:]:
        print(VERSIONS[executable])
        return 0
    if executable == "claude":
        return serve_claude()
    if executable == "codex":
        return serve_codex()
    if executable == "pi":
        return serve_pi()
    if executable == "agy":
        return serve_antigravity()
    raise ValueError(f"unsupported fixture executable: {executable}")


if __name__ == "__main__":
    raise SystemExit(main())
