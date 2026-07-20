"""Private controlling-terminal handoff for browser-only URLs."""

from __future__ import annotations

import io
import os
from typing import TextIO
from urllib.parse import urlsplit


class TerminalHandoffError(RuntimeError):
    """A sanitized failure that never contains the private URL."""


_ALLOWED_PURPOSES = {
    "local approval": "LOCAL APPROVAL",
    "owner OIDC enrollment": "OWNER OIDC ENROLLMENT",
}
_MAX_PRIVATE_URL_CHARS = 8192


def _open_controlling_terminal() -> TextIO:
    if os.name != "posix":
        raise OSError("unsupported platform")
    return open(  # noqa: PTH123 - /dev/tty is the required controlling terminal
        "/dev/tty",
        "r+",
        encoding="utf-8",
        errors="strict",
        buffering=1,
    )


def _is_verified_terminal(terminal: TextIO) -> bool:
    try:
        return os.isatty(terminal.fileno())
    except (AttributeError, io.UnsupportedOperation, OSError):
        # Test doubles may not own a real fd. Production /dev/tty always takes the fd path.
        try:
            return bool(terminal.isatty())
        except (AttributeError, OSError):
            return False


def _validate_private_url(url: str) -> str:
    if not isinstance(url, str) or not 1 <= len(url) <= _MAX_PRIVATE_URL_CHARS:
        raise TerminalHandoffError("terminal handoff request is invalid")
    # URLs reaching a terminal must be printable ASCII. This rejects C0/C1/DEL,
    # ANSI/OSC escapes, bidi controls, and ambiguous raw Unicode before any write.
    if any(not 0x21 <= ord(char) <= 0x7E for char in url):
        raise TerminalHandoffError("terminal handoff request is invalid")
    try:
        parsed = urlsplit(url)
        port = parsed.port
    except ValueError:
        raise TerminalHandoffError("terminal handoff request is invalid") from None
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or port is not None
        and not 1 <= port <= 65535
    ):
        raise TerminalHandoffError("terminal handoff request is invalid")
    return url


def _require_verified_terminal(terminal: TextIO) -> None:
    if not _is_verified_terminal(terminal):
        raise TerminalHandoffError("private controlling terminal is unavailable")


def require_private_terminal() -> None:
    """Fail before remote state or capability materialization when no TTY exists."""

    try:
        with _open_controlling_terminal() as terminal:
            _require_verified_terminal(terminal)
    except TerminalHandoffError:
        raise
    except (OSError, UnicodeError, ValueError):
        raise TerminalHandoffError("private controlling terminal is unavailable") from None


def handoff_private_url(
    url: str,
    *,
    purpose: str,
    require_ack: bool,
) -> None:
    """Write one private URL to /dev/tty only, in one flushed framed write."""

    if purpose not in _ALLOWED_PURPOSES:
        raise TerminalHandoffError("terminal handoff request is invalid")
    private_url = _validate_private_url(url)
    acknowledgement = (
        "\nOpen URL in owner-controlled browser, then press Enter here.\n"
        if require_ack
        else "\n"
    )
    frame = (
        "\n========== AGENTNET PRIVATE BROWSER ACTION ==========\n"
        f"Purpose: {_ALLOWED_PURPOSES[purpose]}\n"
        "Do not copy this URL into chat, A2A, logs, or shared terminals.\n"
        f"{private_url}"
        f"{acknowledgement}"
        "=====================================================\n"
    )
    try:
        with _open_controlling_terminal() as terminal:
            _require_verified_terminal(terminal)
            written = terminal.write(frame)
            if written != len(frame):
                raise TerminalHandoffError(
                    "private terminal handoff failed; pending state is retained; rerun command"
                )
            terminal.flush()
            if require_ack and terminal.readline() == "":
                raise TerminalHandoffError(
                    "private terminal acknowledgement failed; pending state is retained; rerun command"
                )
    except TerminalHandoffError:
        raise
    except (OSError, UnicodeError, ValueError):
        raise TerminalHandoffError(
            "private terminal handoff failed; pending state is retained; rerun command"
        ) from None
    finally:
        private_url = ""
        frame = ""
