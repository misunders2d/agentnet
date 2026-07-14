"""Native subprocess drivers for the four documented harness surfaces.

No AgentNet-specific frame is sent to a harness. Claude receives Agent SDK
stream-JSON user messages, Codex receives app-server requests, Pi receives its
documented RPC commands, and Antigravity receives a print-mode prompt.
"""

from __future__ import annotations

import json
import os
import queue
import signal
import subprocess
import threading
import time
from abc import ABC, abstractmethod
from collections import deque
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable
from uuid import uuid4

from agentnet.adapters.base import AdapterLaunchSpec
from agentnet.errors import GateBlocked, ValidationError


class NativeProtocolError(RuntimeError):
    """A native harness emitted an invalid, denied, or incomplete exchange."""


def _remaining(deadline: float) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise NativeProtocolError("native harness exchange exceeded its deadline")
    return remaining


@dataclass(frozen=True, slots=True)
class NativeTurnResult:
    output: str
    native_session_id: str
    native_turn_id: str | None
    terminal_event: str


class _StrictJsonLineProcess:
    """LF-only bounded JSONL subprocess transport."""

    _EOF = object()

    def __init__(
        self,
        command: tuple[str, ...],
        *,
        cwd: Path,
        environment: dict[str, str],
        max_frame_bytes: int = 1_048_576,
        inherited_fds: tuple[int, ...] = (),
        process_started: Callable[[int], None] | None = None,
    ) -> None:
        self.max_frame_bytes = max_frame_bytes
        self.process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
            close_fds=True,
            pass_fds=inherited_fds,
        )
        if process_started is not None:
            try:
                process_started(self.process.pid)
            except Exception:
                self.stop()
                raise
        self._frames: queue.Queue[bytes | BaseException | object] = queue.Queue()
        self._write_lock = threading.Lock()
        self.stderr_tail: deque[str] = deque(maxlen=20)
        threading.Thread(target=self._read_stdout, daemon=True).start()
        threading.Thread(target=self._read_stderr, daemon=True).start()

    def _read_stdout(self) -> None:
        assert self.process.stdout is not None
        buffer = bytearray()
        try:
            while chunk := os.read(self.process.stdout.fileno(), 65_536):
                buffer.extend(chunk)
                if len(buffer) > self.max_frame_bytes and b"\n" not in buffer:
                    raise NativeProtocolError("native JSONL frame exceeds one MiB")
                while True:
                    separator = buffer.find(b"\n")
                    if separator < 0:
                        break
                    frame = bytes(buffer[:separator])
                    del buffer[: separator + 1]
                    if frame.endswith(b"\r"):
                        raise NativeProtocolError("native JSONL transport requires LF framing")
                    if frame:
                        if len(frame) > self.max_frame_bytes:
                            raise NativeProtocolError("native JSONL frame exceeds one MiB")
                        self._frames.put(frame)
            if buffer:
                raise NativeProtocolError("native JSONL stream ended with a partial frame")
        except BaseException as exc:
            self._frames.put(exc)
        finally:
            self._frames.put(self._EOF)

    def _read_stderr(self) -> None:
        assert self.process.stderr is not None
        for raw in iter(self.process.stderr.readline, b""):
            self.stderr_tail.append(raw.decode("utf-8", errors="replace").rstrip("\r\n")[:512])

    def send(self, value: dict[str, Any]) -> None:
        try:
            encoded = json.dumps(
                value,
                allow_nan=False,
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
            ).encode("utf-8")
        except (TypeError, ValueError) as exc:
            raise ValidationError("native harness request is not canonical-JSON compatible") from exc
        if len(encoded) > self.max_frame_bytes:
            raise ValidationError("native harness request exceeds one MiB")
        if self.process.poll() is not None or self.process.stdin is None:
            raise NativeProtocolError("native harness process is offline")
        try:
            with self._write_lock:
                self.process.stdin.write(encoded + b"\n")
                self.process.stdin.flush()
        except (BrokenPipeError, OSError, ValueError) as exc:
            raise NativeProtocolError("native harness stdin write failed") from exc

    def receive(self, timeout_seconds: float) -> dict[str, Any]:
        try:
            frame = self._frames.get(timeout=timeout_seconds)
        except queue.Empty as exc:
            raise NativeProtocolError("native harness response timed out") from exc
        if frame is self._EOF:
            raise NativeProtocolError("native harness process closed stdout")
        if isinstance(frame, BaseException):
            raise NativeProtocolError(str(frame)) from frame
        try:
            value = json.loads(frame)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise NativeProtocolError("native harness emitted invalid JSON") from exc
        if not isinstance(value, dict):
            raise NativeProtocolError("native harness frame must be an object")
        return value

    @property
    def alive(self) -> bool:
        return self.process.poll() is None

    @property
    def pid(self) -> int:
        return self.process.pid

    def stop(self, timeout_seconds: float = 2.0) -> None:
        if self.process.poll() is None:
            try:
                os.killpg(self.process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                self.process.wait(timeout=timeout_seconds)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(self.process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                self.process.wait(timeout=timeout_seconds)


class NativeHarnessDriver(ABC):
    def __init__(self, spec: AdapterLaunchSpec) -> None:
        self.spec = spec

    @abstractmethod
    def start(
        self,
        command: tuple[str, ...],
        *,
        environment: dict[str, str],
        recover: bool,
        timeout_seconds: float,
        inherited_fds: tuple[int, ...] = (),
        process_started: Callable[[int], None] | None = None,
    ) -> None: ...

    @abstractmethod
    def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult: ...

    @abstractmethod
    def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]: ...

    @abstractmethod
    def stop(self) -> None: ...

    @property
    @abstractmethod
    def alive(self) -> bool: ...

    @property
    @abstractmethod
    def pid(self) -> int | None: ...


class ClaudeStreamJsonDriver(NativeHarnessDriver):
    """Claude ``--input-format stream-json --output-format stream-json``."""

    def __init__(self, spec: AdapterLaunchSpec) -> None:
        super().__init__(spec)
        self._transport: _StrictJsonLineProcess | None = None
        self._turn_lock = threading.Lock()

    def start(
        self,
        command,
        *,
        environment,
        recover,
        timeout_seconds,
        inherited_fds=(),
        process_started=None,
    ) -> None:
        del timeout_seconds
        command = list(command)
        if recover:
            try:
                index = command.index("--session-id")
            except ValueError:
                pass
            else:
                command[index : index + 2] = ["--resume", self.spec.session_id]
        self._transport = _StrictJsonLineProcess(
            tuple(command),
            cwd=self.spec.work_dir,
            environment=environment,
            inherited_fds=inherited_fds,
            process_started=process_started,
        )

    def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
        if not prompt:
            raise ValidationError("Claude background prompt cannot be empty")
        transport = self._require_transport()
        with self._turn_lock:
            transport.send(
                {
                    "type": "user",
                    "message": {"role": "user", "content": prompt},
                    "parent_tool_use_id": None,
                    "session_id": self.spec.session_id,
                }
            )
            deadline = time.monotonic() + timeout_seconds
            assistant_text = ""
            while True:
                message = transport.receive(_remaining(deadline))
                message_session = message.get("session_id")
                if message_session != self.spec.session_id:
                    raise NativeProtocolError("Claude stream crossed the dedicated session binding")
                message_type = message.get("type")
                if message_type == "assistant":
                    assistant_text = _claude_text(message)
                elif message_type == "result":
                    if message.get("subtype") != "success" or message.get("is_error") is True:
                        raise NativeProtocolError("Claude background turn failed")
                    result = message.get("result")
                    if not isinstance(result, str):
                        result = assistant_text
                    return NativeTurnResult(
                        output=result,
                        native_session_id=self.spec.session_id,
                        native_turn_id=str(message.get("uuid")) if message.get("uuid") else None,
                        terminal_event="result:success",
                    )

    def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]:
        del timeout_seconds
        transport = self._require_transport()
        if not transport.alive:
            raise NativeProtocolError("Claude stream-json process is offline")
        return {"native_surface": "claude_stream_json", "ready": True}

    def _require_transport(self) -> _StrictJsonLineProcess:
        if self._transport is None:
            raise NativeProtocolError("Claude stream-json driver is not started")
        return self._transport

    @property
    def alive(self) -> bool:
        return self._transport is not None and self._transport.alive

    @property
    def pid(self) -> int | None:
        return self._transport.pid if self._transport is not None else None

    def stop(self) -> None:
        if self._transport is not None:
            self._transport.stop()


def _claude_text(message: dict[str, Any]) -> str:
    body = message.get("message")
    if not isinstance(body, dict):
        return ""
    content = body.get("content")
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    return "".join(
        block.get("text", "")
        for block in content
        if isinstance(block, dict) and block.get("type") == "text" and isinstance(block.get("text"), str)
    )


class CodexAppServerDriver(NativeHarnessDriver):
    """Codex app-server ``initialize``/thread/turn JSONL protocol."""

    def __init__(self, spec: AdapterLaunchSpec) -> None:
        super().__init__(spec)
        self._transport: _StrictJsonLineProcess | None = None
        self._request_number = 0
        self._thread_id: str | None = self._load_thread_id()
        self._notifications: deque[dict[str, Any]] = deque()
        self._turn_lock = threading.Lock()

    def _load_thread_id(self) -> str | None:
        path = self.spec.state_dir / "codex-thread.json"
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return None
        thread_id = value.get("thread_id") if isinstance(value, dict) else None
        return thread_id if isinstance(thread_id, str) and thread_id else None

    def _persist_thread_id(self) -> None:
        if self._thread_id is None:
            return
        path = self.spec.state_dir / "codex-thread.json"
        temporary = path.with_suffix(".tmp")
        temporary.write_text(
            json.dumps({"thread_id": self._thread_id}, separators=(",", ":"), sort_keys=True),
            encoding="utf-8",
        )
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)

    def start(
        self,
        command,
        *,
        environment,
        recover,
        timeout_seconds,
        inherited_fds=(),
        process_started=None,
    ) -> None:
        del recover
        self._transport = _StrictJsonLineProcess(
            command,
            cwd=self.spec.work_dir,
            environment=environment,
            inherited_fds=inherited_fds,
            process_started=process_started,
        )
        self._rpc(
            "initialize",
            {
                "clientInfo": {
                    "name": "agentnet",
                    "title": "Dedicated background adapter",
                    "version": "0.1.5",
                },
                "capabilities": {"experimentalApi": False},
            },
            timeout_seconds,
        )
        self._require_transport().send({"method": "initialized"})
        if self._thread_id is not None:
            method = "thread/resume"
            params = {
                "threadId": self._thread_id,
                "approvalPolicy": "never",
                "approvalsReviewer": "user",
                "cwd": str(self.spec.work_dir),
                "sandbox": "read-only",
            }
        else:
            method = "thread/start"
            params = {
                "approvalPolicy": "never",
                "approvalsReviewer": "user",
                "cwd": str(self.spec.work_dir),
                "ephemeral": False,
                "sandbox": "read-only",
            }
        result = self._rpc(method, params, timeout_seconds)
        thread = result.get("thread")
        thread_id = thread.get("id") if isinstance(thread, dict) else None
        if not isinstance(thread_id, str) or not thread_id:
            raise NativeProtocolError("Codex app-server did not return a thread identifier")
        if self._thread_id is not None and thread_id != self._thread_id:
            raise NativeProtocolError("Codex app-server resumed a different thread")
        self._thread_id = thread_id
        self._persist_thread_id()

    def _rpc(self, method: str, params: dict[str, Any], timeout_seconds: float) -> dict[str, Any]:
        self._request_number += 1
        request_id = self._request_number
        transport = self._require_transport()
        transport.send({"id": request_id, "method": method, "params": params})
        deadline = time.monotonic() + timeout_seconds
        while True:
            message = transport.receive(_remaining(deadline))
            if "id" in message and "method" not in message:
                if message.get("id") != request_id:
                    raise NativeProtocolError("Codex app-server response crossed request binding")
                if "error" in message:
                    raise NativeProtocolError(f"Codex app-server rejected {method}")
                result = message.get("result")
                if not isinstance(result, dict):
                    raise NativeProtocolError("Codex app-server response result is not an object")
                return result
            if "method" in message and "id" not in message:
                self._notifications.append(message)
                continue
            if "method" in message and "id" in message:
                raise NativeProtocolError("Codex app-server requested interactive authority")

    def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
        if not prompt or self._thread_id is None:
            raise ValidationError("Codex background prompt or thread binding is unavailable")
        with self._turn_lock:
            result = self._rpc(
                "turn/start",
                {
                    "threadId": self._thread_id,
                    "input": [{"type": "text", "text": prompt}],
                    "approvalPolicy": "never",
                    "approvalsReviewer": "user",
                },
                timeout_seconds,
            )
            turn = result.get("turn")
            turn_id = turn.get("id") if isinstance(turn, dict) else None
            if not isinstance(turn_id, str) or not turn_id:
                raise NativeProtocolError("Codex turn/start omitted the turn identifier")
            deadline = time.monotonic() + timeout_seconds
            output_parts: list[str] = []
            completed_output: str | None = None
            while True:
                if self._notifications:
                    message = self._notifications.popleft()
                else:
                    message = self._require_transport().receive(_remaining(deadline))
                method = message.get("method")
                params = message.get("params")
                if "id" in message and isinstance(method, str):
                    raise NativeProtocolError("Codex app-server requested interactive authority")
                if not isinstance(params, dict):
                    continue
                if method in {
                    "item/completed",
                    "item/agentMessage/delta",
                    "turn/completed",
                } and params.get("threadId") != self._thread_id:
                    raise NativeProtocolError("Codex notification crossed the dedicated thread binding")
                if method in {"item/completed", "item/agentMessage/delta"} and params.get("turnId") != turn_id:
                    raise NativeProtocolError("Codex notification crossed the active turn binding")
                if method == "item/completed":
                    item = params.get("item")
                    if isinstance(item, dict) and item.get("type") == "agentMessage" and isinstance(item.get("text"), str):
                        completed_output = item["text"]
                elif method == "item/agentMessage/delta":
                    delta = params.get("delta")
                    if isinstance(delta, str):
                        output_parts.append(delta)
                elif method == "turn/completed":
                    completed = params.get("turn")
                    if not isinstance(completed, dict) or completed.get("id") != turn_id:
                        raise NativeProtocolError("Codex completed notification crossed turn binding")
                    status = completed.get("status")
                    completed_status = status == "completed" or (
                        isinstance(status, dict) and status.get("type") == "completed"
                    )
                    if not completed_status:
                        raise NativeProtocolError("Codex background turn did not complete")
                    return NativeTurnResult(
                        output=completed_output if completed_output is not None else "".join(output_parts),
                        native_session_id=self._thread_id,
                        native_turn_id=turn_id,
                        terminal_event="turn/completed",
                    )

    def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]:
        del timeout_seconds
        if not self.alive or self._thread_id is None:
            raise NativeProtocolError("Codex app-server thread is offline")
        return {"native_surface": "codex_app_server", "ready": True, "thread_bound": True}

    def _require_transport(self) -> _StrictJsonLineProcess:
        if self._transport is None:
            raise NativeProtocolError("Codex app-server driver is not started")
        return self._transport

    @property
    def alive(self) -> bool:
        return self._transport is not None and self._transport.alive

    @property
    def pid(self) -> int | None:
        return self._transport.pid if self._transport is not None else None

    def stop(self) -> None:
        if self._transport is not None:
            self._transport.stop()


class PiRpcDriver(NativeHarnessDriver):
    """Pi documented LF-delimited ``--mode rpc`` protocol."""

    def __init__(self, spec: AdapterLaunchSpec) -> None:
        super().__init__(spec)
        self._transport: _StrictJsonLineProcess | None = None
        self._request_number = 0
        self._events: deque[dict[str, Any]] = deque()
        self._turn_lock = threading.Lock()

    def start(
        self,
        command,
        *,
        environment,
        recover,
        timeout_seconds,
        inherited_fds=(),
        process_started=None,
    ) -> None:
        del recover
        self._transport = _StrictJsonLineProcess(
            command,
            cwd=self.spec.work_dir,
            environment=environment,
            inherited_fds=inherited_fds,
            process_started=process_started,
        )
        state = self._command("get_state", {}, timeout_seconds)
        native_session = state.get("sessionId")
        if native_session != self.spec.session_id:
            raise NativeProtocolError("Pi RPC crossed the dedicated session binding")

    def _command(self, command: str, fields: dict[str, Any], timeout_seconds: float) -> dict[str, Any]:
        self._request_number += 1
        request_id = str(self._request_number)
        transport = self._require_transport()
        transport.send({"id": request_id, "type": command, **fields})
        deadline = time.monotonic() + timeout_seconds
        while True:
            message = transport.receive(_remaining(deadline))
            if message.get("type") == "response":
                if message.get("id") != request_id:
                    raise NativeProtocolError("Pi RPC response crossed request binding")
                if message.get("command") != command or message.get("success") is not True:
                    raise NativeProtocolError(f"Pi RPC rejected {command}")
                data = message.get("data", {})
                if not isinstance(data, dict):
                    raise NativeProtocolError("Pi RPC response data is not an object")
                return data
            self._events.append(message)

    def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
        if not prompt:
            raise ValidationError("Pi background prompt cannot be empty")
        with self._turn_lock:
            self._command("prompt", {"message": prompt}, timeout_seconds)
            deadline = time.monotonic() + timeout_seconds
            while True:
                if self._events:
                    event = self._events.popleft()
                else:
                    event = self._require_transport().receive(_remaining(deadline))
                if event.get("type") == "response":
                    raise NativeProtocolError("Pi RPC emitted an unexpected command response")
                if event.get("type") == "agent_settled":
                    break
            data = self._command("get_last_assistant_text", {}, _remaining(deadline))
            output = data.get("text")
            if not isinstance(output, str):
                raise NativeProtocolError("Pi RPC did not return final assistant text")
            state = self._command("get_state", {}, _remaining(deadline))
            native_session = state.get("sessionId")
            if native_session != self.spec.session_id:
                raise NativeProtocolError("Pi RPC session binding changed")
            return NativeTurnResult(
                output=output,
                native_session_id=native_session,
                native_turn_id=None,
                terminal_event="agent_settled",
            )

    def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]:
        state = self._command("get_state", {}, timeout_seconds)
        if state.get("sessionId") != self.spec.session_id or state.get("isStreaming") is not False:
            raise NativeProtocolError("Pi RPC health state is not bound and settled")
        return {
            "native_surface": "pi_rpc",
            "ready": True,
            "session_bound": True,
        }

    def _require_transport(self) -> _StrictJsonLineProcess:
        if self._transport is None:
            raise NativeProtocolError("Pi RPC driver is not started")
        return self._transport

    @property
    def alive(self) -> bool:
        return self._transport is not None and self._transport.alive

    @property
    def pid(self) -> int | None:
        return self._transport.pid if self._transport is not None else None

    def stop(self) -> None:
        if self._transport is not None:
            self._transport.stop()


class AntigravityPrintDriver(NativeHarnessDriver):
    """Serialized ``agy --print --conversation`` invocations."""

    def __init__(self, spec: AdapterLaunchSpec) -> None:
        super().__init__(spec)
        self._command: tuple[str, ...] | None = None
        self._environment: dict[str, str] | None = None
        self._ready = False
        self._turn_lock = threading.Lock()

    def start(
        self,
        command,
        *,
        environment,
        recover,
        timeout_seconds,
        inherited_fds=(),
        process_started=None,
    ) -> None:
        del recover, timeout_seconds
        self._command = command
        self._environment = environment
        if inherited_fds or process_started is not None:
            raise GateBlocked("G05", "one-shot Antigravity cannot retain a process-bound MCP channel")
        self._ready = True

    def submit(self, prompt: str, *, timeout_seconds: float) -> NativeTurnResult:
        if not prompt or self._command is None or self._environment is None:
            raise ValidationError("Antigravity print driver is not ready")
        with self._turn_lock:
            process: subprocess.Popen[str] | None = None
            try:
                process = subprocess.Popen(
                    (*self._command, prompt),
                    cwd=self.spec.work_dir,
                    env=self._environment,
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    encoding="utf-8",
                    errors="strict",
                    close_fds=True,
                    start_new_session=True,
                )
                stdout, _stderr = process.communicate(timeout=timeout_seconds)
            except subprocess.TimeoutExpired as exc:
                self._ready = False
                assert process is not None
                _terminate_process_group(process)
                raise NativeProtocolError("Antigravity print invocation timed out") from exc
            except (OSError, UnicodeError) as exc:
                self._ready = False
                if process is not None:
                    _terminate_process_group(process)
                raise NativeProtocolError("Antigravity print invocation failed") from exc
            if process.returncode != 0:
                self._ready = False
                raise NativeProtocolError("Antigravity print invocation failed")
            return NativeTurnResult(
                output=stdout.rstrip("\r\n"),
                native_session_id=self.spec.session_id,
                native_turn_id=str(uuid4()),
                terminal_event="process_exit:0",
            )

    def healthcheck(self, *, timeout_seconds: float) -> dict[str, Any]:
        del timeout_seconds
        if not self._ready:
            raise NativeProtocolError("Antigravity serialized conversation is offline")
        return {"native_surface": "antigravity_print", "ready": True, "conversation_bound": True}

    @property
    def alive(self) -> bool:
        return self._ready

    @property
    def pid(self) -> int | None:
        return None

    def stop(self) -> None:
        self._ready = False


def _terminate_process_group(process: subprocess.Popen[Any]) -> None:
    """Bounded two-stage shutdown for one-shot native process trees."""

    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=1.0)
        return
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        return
    process.wait(timeout=1.0)


NativeDriverFactory = Callable[[AdapterLaunchSpec], NativeHarnessDriver]


_NATIVE_DRIVER_FACTORIES: dict[str, NativeDriverFactory] = {
    "claude": ClaudeStreamJsonDriver,
    "codex": CodexAppServerDriver,
    "pi": PiRpcDriver,
    "antigravity": AntigravityPrintDriver,
}


def register_native_driver(harness: str, factory: NativeDriverFactory) -> None:
    """Register one ABI-conforming future driver without modifying core code."""

    if (
        not isinstance(harness, str)
        or not harness
        or len(harness) > 64
        or not all(character.islower() or character.isdigit() or character in {"-", "_"} for character in harness)
        or not callable(factory)
    ):
        raise ValidationError("native driver registration is outside the adapter ABI profile")
    if harness in _NATIVE_DRIVER_FACTORIES:
        raise ValidationError("native driver identifier is already registered")
    _NATIVE_DRIVER_FACTORIES[harness] = factory


def create_native_driver(spec: AdapterLaunchSpec) -> NativeHarnessDriver:
    factory = _NATIVE_DRIVER_FACTORIES.get(spec.harness)
    if factory is None:
        raise GateBlocked("G01", "unsupported native harness driver")
    driver = factory(spec)
    if not isinstance(driver, NativeHarnessDriver) or driver.spec is not spec:
        raise GateBlocked("G01", "native harness driver violates adapter ABI v1")
    return driver
