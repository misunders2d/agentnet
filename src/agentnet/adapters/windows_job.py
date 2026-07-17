"""Race-free Windows Job Object process-tree custody for native adapters."""

from __future__ import annotations

import ctypes
import os
import signal
import subprocess
from ctypes import wintypes
from typing import Any

from agentnet.errors import GateBlocked
from agentnet.windows_security import (
    private_security_attributes,
    require_private_kernel_handle,
)


TH32CS_SNAPTHREAD = 0x00000004
THREAD_SUSPEND_RESUME = 0x0002
INVALID_HANDLE_VALUE = ctypes.c_void_p(-1).value


class THREADENTRY32(ctypes.Structure):
    _fields_ = [
        ("dwSize", wintypes.DWORD),
        ("cntUsage", wintypes.DWORD),
        ("th32ThreadID", wintypes.DWORD),
        ("th32OwnerProcessID", wintypes.DWORD),
        ("tpBasePri", wintypes.LONG),
        ("tpDeltaPri", wintypes.LONG),
        ("dwFlags", wintypes.DWORD),
    ]


class WindowsJobGuard:
    """Assign a CREATE_SUSPENDED process before any child code can execute."""

    def __init__(self) -> None:
        if os.name != "nt":
            raise GateBlocked("G05", "Windows Job Object requires Windows")
        try:
            import win32job
        except ImportError as exc:  # pragma: no cover - Windows dependency gate
            raise GateBlocked("G05", "pywin32 Job Object support is unavailable") from exc
        self._win32job = win32job
        # pywin32 requires a string even though Win32 accepts NULL for an
        # unnamed job; an empty string maps to the unnamed-object behavior.
        self._job = win32job.CreateJobObject(private_security_attributes(), "")
        require_private_kernel_handle(self._job, label="AgentNet native adapter job")
        information = win32job.QueryInformationJobObject(
            self._job,
            win32job.JobObjectExtendedLimitInformation,
        )
        basic = dict(information["BasicLimitInformation"])
        basic["LimitFlags"] = int(basic["LimitFlags"]) | win32job.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        information["BasicLimitInformation"] = basic
        win32job.SetInformationJobObject(
            self._job,
            win32job.JobObjectExtendedLimitInformation,
            information,
        )
        self._assigned_pid: int | None = None

    @staticmethod
    def creation_flags() -> int:
        return subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.CREATE_SUSPENDED

    @staticmethod
    def _resume_process_threads(pid: int) -> None:
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.CreateToolhelp32Snapshot.argtypes = [wintypes.DWORD, wintypes.DWORD]
        kernel32.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
        kernel32.Thread32First.argtypes = [wintypes.HANDLE, ctypes.POINTER(THREADENTRY32)]
        kernel32.Thread32First.restype = wintypes.BOOL
        kernel32.Thread32Next.argtypes = [wintypes.HANDLE, ctypes.POINTER(THREADENTRY32)]
        kernel32.Thread32Next.restype = wintypes.BOOL
        kernel32.OpenThread.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel32.OpenThread.restype = wintypes.HANDLE
        kernel32.ResumeThread.argtypes = [wintypes.HANDLE]
        kernel32.ResumeThread.restype = wintypes.DWORD
        kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel32.CloseHandle.restype = wintypes.BOOL

        snapshot = kernel32.CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD, 0)
        if snapshot == INVALID_HANDLE_VALUE:
            raise ctypes.WinError(ctypes.get_last_error())
        resumed = 0
        try:
            entry = THREADENTRY32()
            entry.dwSize = ctypes.sizeof(entry)
            present = bool(kernel32.Thread32First(snapshot, ctypes.byref(entry)))
            while present:
                if int(entry.th32OwnerProcessID) == pid:
                    thread = kernel32.OpenThread(
                        THREAD_SUSPEND_RESUME,
                        False,
                        int(entry.th32ThreadID),
                    )
                    if not thread:
                        raise ctypes.WinError(ctypes.get_last_error())
                    try:
                        previous = int(kernel32.ResumeThread(thread))
                        if previous == 0xFFFFFFFF:
                            raise ctypes.WinError(ctypes.get_last_error())
                        resumed += 1
                    finally:
                        kernel32.CloseHandle(thread)
                present = bool(kernel32.Thread32Next(snapshot, ctypes.byref(entry)))
        finally:
            kernel32.CloseHandle(snapshot)
        if resumed != 1:
            raise GateBlocked("G05", "suspended Windows adapter did not expose one main thread")

    def assign_and_resume(self, process: subprocess.Popen[Any]) -> None:
        if self._assigned_pid is not None or process.poll() is not None:
            raise GateBlocked("G05", "Windows adapter process cannot enter Job Object")
        try:
            self._win32job.AssignProcessToJobObject(self._job, int(process._handle))
            self._resume_process_threads(process.pid)
        except Exception as exc:
            try:
                self._win32job.TerminateJobObject(self._job, 70)
            except Exception:
                pass
            raise GateBlocked("G05", "Windows adapter Job Object admission failed") from exc
        self._assigned_pid = process.pid

    def stop(self, process: subprocess.Popen[Any], *, timeout_seconds: float) -> None:
        if process.poll() is None:
            try:
                process.send_signal(signal.CTRL_BREAK_EVENT)
                process.wait(timeout=timeout_seconds)
            except (OSError, subprocess.TimeoutExpired):
                self._win32job.TerminateJobObject(self._job, 1)
                process.wait(timeout=timeout_seconds)
        self.close()

    def close(self) -> None:
        job, self._job = self._job, None
        if job is not None:
            job.Close()


__all__ = ["WindowsJobGuard"]
