"""Harness-like parent that launches the configured MCP proxy without FDs."""

from __future__ import annotations

import json
import os
import subprocess
import sys


def _send(process: subprocess.Popen[str], value: dict[str, object]) -> dict[str, object]:
    assert process.stdin is not None and process.stdout is not None
    process.stdin.write(json.dumps(value, separators=(",", ":"), sort_keys=True) + "\n")
    process.stdin.flush()
    response = process.stdout.readline()
    if not response:
        raise RuntimeError("MCP proxy closed before responding")
    return json.loads(response)


def _launch() -> subprocess.Popen[str]:
    return subprocess.Popen(
        [sys.executable, "-m", "agentnet.bindings.mcp_proxy"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )


def _initialize(proxy: subprocess.Popen[str]) -> None:
    initialized = _send(
        proxy,
        {
            "id": 1,
            "jsonrpc": "2.0",
            "method": "initialize",
            "params": {
                "capabilities": {},
                "clientInfo": {"name": "agentnet-process-test", "version": "1"},
                "protocolVersion": "2025-11-25",
            },
        },
    )
    assert initialized.get("id") == 1 and "result" in initialized
    assert proxy.stdin is not None
    proxy.stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
    proxy.stdin.flush()


def _tool_call(proxy: subprocess.Popen[str]) -> dict[str, object]:
    return _send(
        proxy,
        {
            "id": 2,
            "jsonrpc": "2.0",
            "method": "tools/call",
            "params": {
                "arguments": {"after_cursor": 0, "limit": 10},
                "name": "agentnet_inbox",
            },
        },
    )


def main() -> None:
    proxy = _launch()
    try:
        try:
            _initialize(proxy)
        except Exception as exc:
            print(
                json.dumps(
                    {"error": type(exc).__name__, "ready": False},
                    separators=(",", ":"),
                    sort_keys=True,
                ),
                flush=True,
            )
            return
        forbidden_environment = sorted(
            {
                "AGENTNET_LOCAL_BINDING_FD",
                "AGENTNET_LOCAL_BINDING_TOKEN",
                "AGENTNET_HARNESS_ID",
                "AGENTNET_CREDENTIAL_ID",
                "AUTHORIZATION",
            }
            & set(os.environ)
        )
        print(
            json.dumps(
                {"forbidden_environment": forbidden_environment, "ready": True},
                separators=(",", ":"),
                sort_keys=True,
            ),
            flush=True,
        )
        for raw_command in sys.stdin:
            command = raw_command.strip()
            if command == "call":
                print(
                    json.dumps(_tool_call(proxy), separators=(",", ":"), sort_keys=True),
                    flush=True,
                )
            elif command == "restart":
                proxy.terminate()
                proxy.wait(timeout=5)
                proxy = _launch()
                try:
                    _initialize(proxy)
                except Exception as exc:
                    print(
                        json.dumps(
                            {"error": type(exc).__name__, "restart_rejected": True},
                            separators=(",", ":"),
                            sort_keys=True,
                        ),
                        flush=True,
                    )
                else:
                    print('{"restart_rejected":false}', flush=True)
            elif command == "exit":
                break
    finally:
        if proxy.poll() is None:
            proxy.terminate()
            proxy.wait(timeout=5)


if __name__ == "__main__":
    main()
