from __future__ import annotations

import asyncio
import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import time
from dataclasses import replace
from pathlib import Path

import pytest

from agentnet.bindings.ipc import (
    IPCSessionClaims,
    WindowsNamedPipeIPCServer,
    accepted_unix_socket_peer,
    build_ipc_frame,
    mint_inherited_session_capability,
)
from agentnet.bindings.mcp_bootstrap import MCP_BOOTSTRAP_ASSURANCE
from agentnet.bindings.windows_mcp_bootstrap import WindowsMCPBootstrapServer
from agentnet.errors import AuthenticationError, GateBlocked
from agentnet.host import host_platform
from agentnet.host_security import measure_process_identity
from agentnet.operations.policy_defaults import OperationsPolicy
from agentnet.security.envelope import LocalEnvelopeCipher
from agentnet.security.signatures import canonical_json
from agentnet.security.update import UpdateArtifact
from agentnet.storage.sqlite import SQLiteStore


ROOT = Path(__file__).resolve().parents[2]


def test_host_platform_maps_only_the_three_supported_families() -> None:
    assert host_platform("linux") == "linux"
    assert host_platform("darwin") == "macos"
    assert host_platform("win32") == "windows"
    with pytest.raises(Exception, match="unsupported host platform: freebsd"):
        host_platform("freebsd")


def test_live_current_process_identity_uses_canonical_host_account() -> None:
    measured = measure_process_identity(os.getpid())
    assert measured.platform == host_platform()
    assert measured.pid == os.getpid()
    assert measured.parent_pid == os.getppid()
    assert measured.executable_measurement.startswith("sha256:")
    if measured.platform == "windows":
        assert measured.account_id.startswith("sid:S-")
    else:
        assert measured.account_id.startswith("uid:")


def test_process_identity_is_pid_reuse_fenced() -> None:
    class Uids:
        effective = 1000

    class StableProcess:
        pid = os.getpid()

        def create_time(self) -> float:
            return 1_700_000_000.25

        def exe(self) -> str:
            return sys.executable

        def ppid(self) -> int:
            return os.getppid()

        def is_running(self) -> bool:
            return True

        def uids(self) -> Uids:
            return Uids()

    measured = measure_process_identity(
        os.getpid(),
        process_factory=lambda _pid: StableProcess(),
        platform_name="linux",
    )
    assert measured.pid == os.getpid()
    assert measured.account_id == "uid:1000"
    assert measured.start_time == str(round(1_700_000_000.25 * 1_000_000_000))
    assert measured.executable_measurement.startswith("sha256:")

    class ReusedProcess(StableProcess):
        calls = 0

        def create_time(self) -> float:
            self.calls += 1
            return float(self.calls)

    with pytest.raises(AuthenticationError, match="changed during measurement"):
        measure_process_identity(
            os.getpid(),
            process_factory=lambda _pid: ReusedProcess(),
            platform_name="linux",
        )


@pytest.mark.skipif(sys.platform == "win32", reason="Unix peer credentials require Unix")
def test_live_unix_socket_peer_binds_pid_account_and_parent() -> None:
    server, client = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        peer = accepted_unix_socket_peer(server)
    finally:
        server.close()
        client.close()
    assert peer.platform == host_platform()
    assert peer.pid == os.getpid()
    assert peer.account_id.startswith("uid:")
    assert peer.parent_pid == os.getppid()
    assert peer.parent_account_id.startswith("uid:")
    assert peer.process_measurement.startswith("sha256:")
    assert peer.parent_process_measurement.startswith("sha256:")


@pytest.mark.skipif(sys.platform != "darwin", reason="macOS inherited pipe contract")
def test_macos_binding_descriptor_is_read_only_pipe() -> None:
    from agentnet.supervisor.runtime import BackgroundAdapterRuntime

    reader, writer = BackgroundAdapterRuntime._binding_descriptors()
    assert writer is not None
    with pytest.raises(OSError):
        os.write(reader, b"not-writable")
    payload = b'{"safe":true}'
    import threading

    def publish() -> None:
        try:
            BackgroundAdapterRuntime._write_binding_pipe(writer, payload)
        finally:
            os.close(writer)

    thread = threading.Thread(target=publish)
    thread.start()
    received = bytearray()
    while chunk := os.read(reader, 1024):
        received.extend(chunk)
    thread.join(timeout=5)
    os.close(reader)
    assert bytes(received) == payload


@pytest.mark.anyio
@pytest.mark.skipif(sys.platform != "win32", reason="Windows named-pipe contract")
async def test_windows_named_pipe_uses_dacl_client_pid_and_exact_claims(store) -> None:
    import win32con
    import win32file
    import win32pipe

    root = b"windows-pipe-test-capability-root-32-bytes"
    identity = measure_process_identity(os.getpid())
    now = int(time.time())
    claims = IPCSessionClaims(
        schema="agentnet.ipc.session.v1",
        capability_id=secrets.token_urlsafe(32),
        harness_id="windows-pipe-harness",
        credential_id="windows-pipe-credential",
        credential_epoch=1,
        binding="direct_ipc",
        process_binding="exact",
        allowed_methods=("agentnet.inbox",),
        platform="windows",
        account_id=identity.account_id,
        uid=0,
        pid=identity.pid,
        process_start_time=identity.start_time,
        process_measurement=identity.executable_measurement,
        session_id="windows-pipe-session-with-entropy-001",
        issued_at=now,
        expires_at=now + 60,
    )
    capability = mint_inherited_session_capability(root, claims)
    frame = build_ipc_frame(
        capability,
        session_id=claims.session_id,
        nonce="windows-pipe-nonce-with-entropy-001",
        request={"arguments": {}, "method": "agentnet.inbox"},
    )
    endpoint = rf"\\.\pipe\agentnet-{secrets.token_urlsafe(24)}"

    async def handler(_claims, request):
        return {"ok": True, "result": request}

    server = WindowsNamedPipeIPCServer(
        endpoint,
        capability_root=root,
        replay_store=store,
        handler=handler,
    )

    def exchange() -> dict[str, object]:
        win32pipe.WaitNamedPipe(endpoint, 5_000)
        handle = win32file.CreateFile(
            endpoint,
            win32con.GENERIC_READ | win32con.GENERIC_WRITE,
            0,
            None,
            win32con.OPEN_EXISTING,
            0,
            None,
        )
        try:
            body = canonical_json(frame)
            win32file.WriteFile(handle, len(body).to_bytes(4, "big") + body)
            _status, prefix = win32file.ReadFile(handle, 4)
            length = int.from_bytes(prefix, "big")
            _status, response = win32file.ReadFile(handle, length)
            return json.loads(response)
        finally:
            handle.Close()

    await server.start()
    try:
        response = await asyncio.to_thread(exchange)
        assert response == {
            "ok": True,
            "result": {"arguments": {}, "method": "agentnet.inbox"},
        }
        assert await asyncio.to_thread(exchange) == {"error": "replay_rejected"}
    finally:
        await server.close()


@pytest.mark.anyio
@pytest.mark.skipif(sys.platform != "win32", reason="Windows MCP named-pipe contract")
async def test_windows_mcp_bootstrap_binds_peer_and_persists_requests() -> None:
    from agentnet.bindings.mcp_proxy import _WindowsPipeConnection

    endpoint = rf"\\.\pipe\agentnet-mcp-{secrets.token_urlsafe(24)}"
    seen: list[int] = []

    def bind_peer(peer):
        seen.append(peer.pid)
        return peer.pid

    async def handler(bound, peer, request):
        assert bound == peer.pid
        return {"ok": True, "result": request}

    server = WindowsMCPBootstrapServer(
        endpoint,
        bind_peer=bind_peer,
        handler=handler,
        generation="windows-mcp-generation-with-entropy-001",
        assurance=MCP_BOOTSTRAP_ASSURANCE,
    )

    def receive_exact(connection, length: int) -> bytes:
        output = bytearray()
        while len(output) < length:
            chunk = connection.recv(length - len(output))
            if not chunk:
                raise AssertionError("Windows MCP response ended early")
            output.extend(chunk)
        return bytes(output)

    def exchange() -> tuple[dict[str, object], dict[str, object]]:
        connection = _WindowsPipeConnection(endpoint)
        try:
            accepted_length = int.from_bytes(receive_exact(connection, 4), "big")
            accepted = json.loads(receive_exact(connection, accepted_length))
            request = {"arguments": {"limit": 1}, "method": "agentnet.inbox"}
            body = canonical_json(request)
            connection.sendall(len(body).to_bytes(4, "big") + body)
            response_length = int.from_bytes(receive_exact(connection, 4), "big")
            response = json.loads(receive_exact(connection, response_length))
            return accepted, response
        finally:
            connection.close()

    await server.start()
    try:
        accepted, response = await asyncio.to_thread(exchange)
        assert accepted == {
            "assurance": MCP_BOOTSTRAP_ASSURANCE,
            "generation": "windows-mcp-generation-with-entropy-001",
            "ok": True,
            "schema": "agentnet.mcp.bootstrap-accepted.v1",
        }
        assert response == {
            "ok": True,
            "result": {"arguments": {"limit": 1}, "method": "agentnet.inbox"},
        }
        assert seen == [os.getpid()]
    finally:
        await server.close()


@pytest.mark.skipif(sys.platform != "win32", reason="Windows capability delivery contract")
def test_windows_binding_delivery_and_job_object_are_live() -> None:
    import win32con
    import win32file
    import win32pipe

    from agentnet.adapters.native import _spawn_process_tree
    from agentnet.supervisor.windows_binding_delivery import WindowsBindingDelivery

    delivery = WindowsBindingDelivery(timeout_seconds=10)
    delivery.start()
    payload = b'{"binding":"private"}'
    delivery.publish(payload, expected=measure_process_identity(os.getpid()))
    try:
        win32pipe.WaitNamedPipe(delivery.endpoint, 5_000)
        handle = win32file.CreateFile(
            delivery.endpoint,
            win32con.GENERIC_READ,
            0,
            None,
            win32con.OPEN_EXISTING,
            0,
            None,
        )
        try:
            _status, prefix = win32file.ReadFile(handle, 4)
            _status, received = win32file.ReadFile(handle, int.from_bytes(prefix, "big"))
        finally:
            handle.Close()
        assert received == payload
    finally:
        delivery.close()

    process, guard = _spawn_process_tree(
        (sys.executable, "-c", "print('job-ok', flush=True)"),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        close_fds=True,
    )
    stdout, stderr = process.communicate(timeout=10)
    assert process.returncode == 0, stderr
    assert stdout.strip() == "job-ok"
    assert guard is not None
    guard.close()


@pytest.mark.skipif(sys.platform != "win32", reason="Windows DACL contract")
def test_windows_private_state_rejects_broad_dacl_and_reparse_points(tmp_path: Path) -> None:
    import win32security

    from agentnet.windows_security import (
        ensure_private_directory,
        require_private_path,
        write_private_file,
    )

    private_file = (tmp_path / "private" / "secret.bin").absolute()
    write_private_file(private_file, b"secret")
    descriptor = win32security.GetNamedSecurityInfo(
        str(private_file),
        win32security.SE_FILE_OBJECT,
        win32security.DACL_SECURITY_INFORMATION,
    )
    dacl = descriptor.GetSecurityDescriptorDacl()
    assert dacl is not None
    dacl.AddAccessAllowedAceEx(
        win32security.ACL_REVISION_DS,
        0,
        0x00120089,
        win32security.ConvertStringSidToSid("S-1-1-0"),
    )
    win32security.SetNamedSecurityInfo(
        str(private_file),
        win32security.SE_FILE_OBJECT,
        win32security.DACL_SECURITY_INFORMATION
        | win32security.PROTECTED_DACL_SECURITY_INFORMATION,
        None,
        None,
        dacl,
        None,
    )
    with pytest.raises(AuthenticationError, match="broad principal"):
        require_private_path(private_file, directory=False)

    target = (tmp_path / "reparse-target").absolute()
    target.mkdir()
    link = (tmp_path / "reparse-link").absolute()
    os.symlink(target, link, target_is_directory=True)
    with pytest.raises(AuthenticationError, match="reparse point"):
        ensure_private_directory(link / "child")


@pytest.mark.skipif(sys.platform != "win32", reason="Windows capability theft contract")
def test_windows_binding_delivery_rejects_wrong_exact_process_identity() -> None:
    import pywintypes
    import win32con
    import win32file
    import win32pipe

    from agentnet.supervisor.windows_binding_delivery import WindowsBindingDelivery

    actual = measure_process_identity(os.getpid())
    wrong = replace(actual, start_time=str(int(actual.start_time) + 1))
    delivery = WindowsBindingDelivery(timeout_seconds=5)
    delivery.start()
    delivery.publish(b'{"binding":"private"}', expected=wrong)
    try:
        win32pipe.WaitNamedPipe(delivery.endpoint, 5_000)
        handle = win32file.CreateFile(
            delivery.endpoint,
            win32con.GENERIC_READ,
            0,
            None,
            win32con.OPEN_EXISTING,
            0,
            None,
        )
        try:
            with pytest.raises(pywintypes.error):
                win32file.ReadFile(handle, 4)
        finally:
            handle.Close()
    finally:
        delivery.close()


@pytest.mark.skipif(sys.platform != "win32", reason="Windows Job cleanup contract")
def test_windows_job_admission_failure_reaps_suspended_child(monkeypatch) -> None:
    from agentnet.adapters.native import _spawn_process_tree
    from agentnet.adapters.windows_job import WindowsJobGuard

    seen = []

    def reject(_self, process) -> None:
        seen.append(process)
        raise GateBlocked("G05", "synthetic Job admission failure")

    monkeypatch.setattr(WindowsJobGuard, "assign_and_resume", reject)
    with pytest.raises(GateBlocked, match="synthetic Job admission failure"):
        _spawn_process_tree(
            (sys.executable, "-c", "raise SystemExit(0)"),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            close_fds=True,
        )
    assert len(seen) == 1
    assert seen[0].poll() is not None


def test_operations_policy_names_every_supported_host() -> None:
    assert OperationsPolicy().supported_os == ("linux", "macos", "windows")


@pytest.mark.parametrize("platform", ["linux", "macos", "windows"])
def test_update_artifact_accepts_each_supported_host(platform: str) -> None:
    artifact = UpdateArtifact(
        platform=platform,
        architecture="x86_64",
        uri=f"https://updates.example/{platform}/agentnet.tar.gz",
        sha256="0" * 64,
        size=1,
    )
    assert artifact.platform == platform


@pytest.mark.skipif(sys.platform == "win32", reason="portable POSIX path simulation")
def test_portable_sqlite_path_branch_preserves_schema_and_reopen(
    tmp_path: Path,
    monkeypatch,
) -> None:
    import agentnet.storage.sqlite as sqlite_module

    monkeypatch.setattr(sqlite_module, "host_platform", lambda: "macos")
    path = (tmp_path / "portable-store" / "core.sqlite3").absolute()
    cipher = LocalEnvelopeCipher(b"m" * 32)
    first = SQLiteStore(path, cipher)
    try:
        first.consume_once("portable-actor", "portable-nonce", expires_at=int(time.time()) + 60)
    finally:
        first.close()
    second = SQLiteStore(path, cipher)
    try:
        assert second.fetch_one(
            "SELECT nonce_hash FROM replay_nonces WHERE actor_id=?",
            ("portable-actor",),
        ) is not None
    finally:
        second.close()


def test_live_sqlite_store_creates_reopens_and_persists_replay(tmp_path: Path) -> None:
    path = (tmp_path / "private-store" / "core.sqlite3").absolute()
    cipher = LocalEnvelopeCipher(b"p" * 32)
    first = SQLiteStore(path, cipher)
    try:
        first.consume_once("platform-test-actor", "platform-test-nonce", expires_at=int(time.time()) + 60)
        assert first.fetch_one(
            "SELECT nonce_hash FROM replay_nonces WHERE actor_id=?",
            ("platform-test-actor",),
        ) is not None
        if sys.platform == "win32":
            from agentnet.windows_security import require_private_path

            sidecars = tuple(
                candidate
                for candidate in (
                    path.with_name(path.name + "-wal"),
                    path.with_name(path.name + "-shm"),
                )
                if candidate.exists()
            )
            assert sidecars
            for sidecar in sidecars:
                require_private_path(sidecar, directory=False)
    finally:
        first.close()
    second = SQLiteStore(path, cipher)
    try:
        assert second.fetch_one(
            "SELECT nonce_hash FROM replay_nonces WHERE actor_id=?",
            ("platform-test-actor",),
        ) is not None
    finally:
        second.close()
    if sys.platform == "win32":
        from agentnet.windows_security import require_private_path

        require_private_path(path.parent, directory=True)
        require_private_path(path, directory=False)


@pytest.mark.skipif(sys.platform != "darwin", reason="macOS SQLite link contract")
def test_macos_sqlite_state_rejects_symlinked_parent(tmp_path: Path) -> None:
    target = (tmp_path / "real-state").absolute()
    target.mkdir(mode=0o700)
    linked = (tmp_path / "linked-state").absolute()
    linked.symlink_to(target, target_is_directory=True)
    with pytest.raises(GateBlocked, match="owner-only and not a symlink"):
        SQLiteStore(linked / "core.sqlite3", LocalEnvelopeCipher(b"s" * 32))


def test_cli_private_state_round_trip_uses_host_security(tmp_path: Path) -> None:
    from agentnet.cli import _owner_only_file, _write_owner_only

    target = (tmp_path / "private" / "identity.json").absolute()
    _write_owner_only(target, b"private")
    assert _owner_only_file(target, label="test identity") == b"private"
    with pytest.raises(SystemExit, match="refusing to overwrite"):
        _write_owner_only(target, b"replacement")
    _write_owner_only(target, b"replacement", force=True)
    assert _owner_only_file(target, label="test identity") == b"replacement"


def test_cli_import_does_not_require_posix_fcntl() -> None:
    script = r'''
import builtins
original = builtins.__import__
def guarded(name, *args, **kwargs):
    if name == "fcntl":
        raise ModuleNotFoundError("fcntl deliberately unavailable")
    return original(name, *args, **kwargs)
builtins.__import__ = guarded
import agentnet.cli
'''
    completed = subprocess.run(
        [sys.executable, "-I", "-c", script],
        cwd=ROOT,
        env={**os.environ, "PYTHONPATH": str(ROOT / "src")},
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stderr


@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is unavailable")
def test_node_platform_helper_maps_supported_state_roots() -> None:
    module = (ROOT / "npm/lib/platform.mjs").as_uri()
    script = f'''
import {{ platformStateRoot, supportedPlatform }} from {json.dumps(module)};
const home = "/home/tester";
const cases = [
  ["linux", {{ XDG_STATE_HOME: "/state" }}, "/state"],
  ["linux", {{}}, "/home/tester/.local/state"],
  ["darwin", {{}}, "/home/tester/Library/Application Support"],
  ["win32", {{ LOCALAPPDATA: "C:\\\\Users\\\\tester\\\\AppData\\\\Local" }}, "C:\\\\Users\\\\tester\\\\AppData\\\\Local"],
];
for (const [platform, env, expected] of cases) {{
  if (!supportedPlatform(platform)) throw new Error(`unsupported ${{platform}}`);
  if (platformStateRoot(platform, env, home) !== expected) throw new Error(`bad root ${{platform}}`);
}}
if (supportedPlatform("freebsd")) throw new Error("freebsd must fail closed");
let failed = false;
try {{ platformStateRoot("freebsd", {{}}, home); }} catch {{ failed = true; }}
if (!failed) throw new Error("unknown platform did not fail closed");
'''
    completed = subprocess.run(
        ["node", "--input-type=module", "-e", script],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert completed.returncode == 0, completed.stderr
