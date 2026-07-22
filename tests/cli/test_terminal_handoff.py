from __future__ import annotations

import io

import pytest

from agentnet._terminal_handoff import (
    TerminalHandoffError,
    handoff_private_url,
    require_private_terminal,
)


class _FakeTerminal(io.StringIO):
    def isatty(self) -> bool:
        return True

    def __enter__(self):
        return self

    def __exit__(self, *_args) -> None:
        return None

    def readline(self, *_args) -> str:
        return "\n"


def test_private_url_is_written_only_to_controlling_terminal(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    terminal = _FakeTerminal("\n")
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )
    secret_url = "https://accounts.example/authorize?state=PRIVATE"

    handoff_private_url(
        secret_url,
        purpose="owner OIDC enrollment",
        require_ack=True,
    )

    captured = capsys.readouterr()
    assert secret_url in terminal.getvalue()
    assert secret_url not in captured.out + captured.err


def test_stable_owner_approval_url_uses_private_controlling_terminal(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    terminal = _FakeTerminal("\n")
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )
    approval_url = "https://approval.example/approval"

    handoff_private_url(
        approval_url,
        purpose="stable owner approval",
        require_ack=True,
    )

    captured = capsys.readouterr()
    assert approval_url in terminal.getvalue()
    assert "STABLE OWNER APPROVAL" in terminal.getvalue()
    assert approval_url not in captured.out + captured.err


def test_private_terminal_requirement_fails_closed_without_tty(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _NotATerminal(_FakeTerminal):
        def isatty(self) -> bool:
            return False

    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: _NotATerminal(),
    )

    with pytest.raises(TerminalHandoffError, match="private controlling terminal is unavailable"):
        require_private_terminal()


def test_private_terminal_errors_never_include_url(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    secret_url = "https://approval.example/approval#token=agcap1.PRIVATE"

    def denied():
        raise OSError("device unavailable")

    monkeypatch.setattr("agentnet._terminal_handoff._open_controlling_terminal", denied)

    with pytest.raises(TerminalHandoffError) as raised:
        handoff_private_url(
            secret_url,
            purpose="local approval",
            require_ack=False,
        )
    assert secret_url not in str(raised.value)


def test_private_terminal_rejects_unapproved_purpose() -> None:
    with pytest.raises(TerminalHandoffError, match="terminal handoff request is invalid"):
        handoff_private_url(
            "https://accounts.example/authorize",
            purpose="free-form secret detail",
            require_ack=False,
        )


def test_private_terminal_rejects_control_bytes_before_write(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    terminal = _FakeTerminal()
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )

    with pytest.raises(TerminalHandoffError, match="terminal handoff request is invalid"):
        handoff_private_url(
            "https://accounts.example/authorize?state=ok\x1b]0;owned\x07",
            purpose="owner OIDC enrollment",
            require_ack=False,
        )

    assert terminal.getvalue() == ""


@pytest.mark.parametrize(
    "url",
    [
        "http://accounts.example/authorize",
        "https://user:password@accounts.example/authorize",
    ],
)
def test_private_terminal_rejects_unsafe_url_before_write(
    url: str,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    terminal = _FakeTerminal()
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )

    with pytest.raises(TerminalHandoffError, match="terminal handoff request is invalid"):
        handoff_private_url(
            url,
            purpose="owner OIDC enrollment",
            require_ack=False,
        )

    assert terminal.getvalue() == ""


def test_private_terminal_accepts_https_port_and_fragment(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    terminal = _FakeTerminal()
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )
    url = "https://approval.example:8443/approval#token=agcap1.PRIVATE"

    handoff_private_url(url, purpose="local approval", require_ack=False)

    assert url in terminal.getvalue()


def test_private_terminal_acknowledgement_eof_fails_closed(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _EofTerminal(_FakeTerminal):
        def readline(self, *_args) -> str:
            return ""

    terminal = _EofTerminal()
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )

    with pytest.raises(TerminalHandoffError, match="acknowledgement failed"):
        handoff_private_url(
            "https://accounts.example/authorize?state=PRIVATE",
            purpose="owner OIDC enrollment",
            require_ack=True,
        )


def test_private_terminal_partial_write_is_not_retried(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _ShortWriteTerminal(_FakeTerminal):
        write_calls = 0

        def write(self, value: str) -> int:
            self.write_calls += 1
            super().write(value[:8])
            return 8

    terminal = _ShortWriteTerminal()
    monkeypatch.setattr(
        "agentnet._terminal_handoff._open_controlling_terminal",
        lambda: terminal,
    )
    secret_url = "https://approval.example/approval#token=agcap1.PRIVATE"

    with pytest.raises(TerminalHandoffError) as raised:
        handoff_private_url(
            secret_url,
            purpose="local approval",
            require_ack=False,
        )

    assert terminal.write_calls == 1
    assert secret_url not in str(raised.value)
