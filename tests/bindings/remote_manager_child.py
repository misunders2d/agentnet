from __future__ import annotations

import base64
import hmac
import json
import os
import signal
import stat
import socket
import subprocess
import sys
import time
from pathlib import Path
from typing import Any


def _canonical(value: Any) -> bytes:
    return json.dumps(
        value,
        allow_nan=False,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")


def _request(binding: dict[str, Any], *, nonce: str) -> dict[str, Any]:
    request = {"arguments": {"after_cursor": 0, "limit": 1}, "method": "agentnet.inbox"}
    authenticated = {
        "nonce": nonce,
        "request": request,
        "session_id": binding["session_id"],
    }
    authenticator = base64.urlsafe_b64encode(
        hmac.digest(
            binding["capability"].encode("ascii"),
            b"AgentNet-IPC-FRAME\x00" + _canonical(authenticated),
            "sha256",
        )
    ).rstrip(b"=").decode("ascii")
    frame = _canonical(
        {
            "authenticator": authenticator,
            "capability": binding["capability"],
            "nonce": nonce,
            "request": request,
            "session_id": binding["session_id"],
        }
    )
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
        peer.connect(binding["socket_path"])
        peer.sendall(len(frame).to_bytes(4, "big") + frame)
        prefix = peer.recv(4)
        length = int.from_bytes(prefix, "big")
        body = bytearray()
        while len(body) < length:
            body.extend(peer.recv(length - len(body)))
    return json.loads(body)


def _binding() -> dict[str, Any]:
    descriptor = int(os.environ["AGENTNET_LOCAL_BINDING_FD"])
    metadata = os.fstat(descriptor)
    if stat.S_ISFIFO(metadata.st_mode):
        chunks: list[bytes] = []
        while chunk := os.read(descriptor, 16_384):
            chunks.append(chunk)
        payload = b"".join(chunks)
        if not 2 <= len(payload) <= 65_536:
            raise RuntimeError("manager binding was not activated")
        return json.loads(payload)
    deadline = time.monotonic() + 5
    while (size := os.fstat(descriptor).st_size) < 2:
        if time.monotonic() >= deadline:
            raise RuntimeError("manager binding was not activated")
        time.sleep(0.005)
    return json.loads(os.pread(descriptor, size, 0))


def _claims(capability: str) -> dict[str, Any]:
    encoded = capability.split(".", 1)[0]
    encoded += "=" * (-len(encoded) % 4)
    return json.loads(base64.urlsafe_b64decode(encoded))


def _sibling() -> int:
    binding = json.loads(sys.stdin.read())
    print(
        json.dumps(
            _request(
                binding,
                nonce="remote-manager-sibling-nonce-000001",
            ),
            separators=(",", ":"),
            sort_keys=True,
        )
    )
    return 0


def _observe() -> dict[str, Any]:
    binding = _binding()
    valid = _request(binding, nonce="remote-manager-child-nonce-0000001")
    if len(sys.argv) < 4:
        raise RuntimeError("manager verifier lacks its isolated inline program")
    source = sys.argv[3]
    sibling = subprocess.run(
        [sys.executable, "-c", source, "--sibling"],
        input=json.dumps(binding, separators=(",", ":"), sort_keys=True),
        text=True,
        capture_output=True,
        check=True,
        env={"LANG": "C.UTF-8", "PATH": os.environ.get("PATH", "/usr/bin:/bin")},
    )
    session_dir = Path(binding["socket_path"]).parent
    return {
        "agentnet_environment": sorted(
            name for name in os.environ if name.startswith("AGENTNET_")
        ),
        "binding": binding,
        "claims": _claims(binding["capability"]),
        "pid": os.getpid(),
        "replay_mode": (session_dir / "replay.sqlite3").stat().st_mode & 0o777,
        "sibling": json.loads(sibling.stdout),
        "socket_mode": Path(binding["socket_path"]).stat().st_mode & 0o777,
        "valid": valid,
    }


def _verify(kind: str, forbidden_path: Path) -> int:
    observed = _observe()
    claims = observed["claims"]
    assert claims["pid"] == observed["pid"]
    assert claims["process_binding"] == "exact"
    assert claims["harness_id"] == "pi-owner-harness-0001"
    assert claims["credential_id"] == "pi-owner-credential-0001"
    assert claims["credential_epoch"] == 7
    assert observed["agentnet_environment"] == ["AGENTNET_LOCAL_BINDING_FD"]
    assert observed["socket_mode"] == 0o600
    assert {
        "A2HUB_TOKEN",
        "AGENTNET_CREDENTIAL_ID",
        "AGENTNET_SIGNING_PRIVATE_KEY",
        "ANTHROPIC_API_KEY",
        "LOCAL_A2A_PRIVATE_KEY",
        "OPENAI_API_KEY",
    }.isdisjoint(os.environ)
    assert observed["replay_mode"] == 0o600
    assert observed["sibling"] == {"error": "authentication_failed"}
    expected = (
        {"error": "not_authorized"}
        if kind == "--verify-denied"
        else {"ok": True, "result": []}
    )
    assert observed["valid"] == expected
    try:
        forbidden_path.read_bytes()
    except FileNotFoundError:
        pass
    else:
        raise AssertionError("isolated child could read the signing identity fixture")
    return 0


def main() -> int:
    if sys.argv[1] == "--sibling":
        return _sibling()
    if sys.argv[1] == "--exit":
        _binding()
        return int(sys.argv[2])
    if sys.argv[1] == "--signal":
        _binding()
        os.kill(os.getpid(), signal.SIGTERM)
        return 99
    if sys.argv[1] in {"--verify-bound", "--verify-denied"}:
        return _verify(sys.argv[1], Path(sys.argv[2]))
    return _manager(Path(sys.argv[1]))


if __name__ == "__main__":
    raise SystemExit(main())
