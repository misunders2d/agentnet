from __future__ import annotations

import time

from agentnet import cli
from agentnet.security.signatures import canonical_digest


class _Response:
    def __init__(self, value: dict[str, object]) -> None:
        self.status_code = 201
        self._value = value

    def json(self):
        return self._value


class _Client:
    def __init__(self, actor) -> None:
        self.actor = actor
        self.calls: list[tuple[str, str, dict[str, object] | None]] = []
        self.handoff_token = "opaque-handoff-token-that-must-never-be-printed"
        self.transaction_digest = ""

    def request(self, method: str, path: str, *, json_body=None):
        self.calls.append((method, path, json_body))
        if path == "/v1/console/session-challenges":
            transaction = {
                "schema": "agentnet.console.session-challenge.v1",
                "challenge_id": "console-challenge-0001",
                "audience": "urn:agentnet:corp.example:console",
                "domain_id": self.actor.domain_id,
                "principal_id": self.actor.principal_id,
                "harness_id": self.actor.harness_id,
                "credential_id": self.actor.credential_id,
                "credential_epoch": self.actor.credential_epoch,
                "nonce": "n" * 43,
                "binding_assurance": self.actor.binding_assurance,
                "issued_at": int(time.time()),
                "expires_at": int(time.time()) + 120,
            }
            self.transaction_digest = canonical_digest(transaction)
            return _Response(
                {
                    "schema": "agentnet.console.session-challenge-result.v1",
                    "challenge_id": transaction["challenge_id"],
                    "transaction": transaction,
                    "transaction_digest": self.transaction_digest,
                    "expires_at": transaction["expires_at"],
                    "console_origin": "https://console.example",
                }
            )
        assert path == "/v1/console/session-challenges/console-challenge-0001/complete"
        assert json_body == {"transaction_digest": self.transaction_digest}
        return _Response(
            {
                "schema": "agentnet.console.session-handoff.v1",
                "handoff_token": self.handoff_token,
                "expires_at": int(time.time()) + 60,
            }
        )

    def close(self) -> None:
        return None


def test_console_open_signs_begin_and_complete_without_printing_or_url_disclosing_handoff(
    identity_factory, monkeypatch, capsys
) -> None:
    actor, key = identity_factory(domain="corp.example", binding_assurance="hardware_bound")
    client = _Client(actor)
    opened: list[tuple[str, str]] = []
    monkeypatch.setattr(cli, "_load_identity_client", lambda _path: (client, actor, key))
    monkeypatch.setattr(
        cli,
        "_open_console_handoff_page",
        lambda *, console_origin, handoff_token, timeout_seconds: opened.append(
            (console_origin, handoff_token)
        ),
    )

    result = cli.main(["console", "open", "--identity", "/private/identity.json"])

    assert result == 0
    assert [call[:2] for call in client.calls] == [
        ("POST", "/v1/console/session-challenges"),
        ("POST", "/v1/console/session-challenges/console-challenge-0001/complete"),
    ]
    assert opened == [("https://console.example", client.handoff_token)]
    output = capsys.readouterr()
    assert client.handoff_token not in output.out + output.err
    assert "console-challenge-0001" not in output.out + output.err


def test_loopback_handoff_page_posts_token_in_form_but_never_in_browser_url(monkeypatch) -> None:
    token = "opaque-handoff-token-that-must-not-be-in-a-url"
    browser_urls: list[str] = []
    served_documents: list[str] = []

    monkeypatch.setattr(
        cli,
        "_serve_one_shot_loopback_page",
        lambda *, document, open_browser, timeout_seconds: (
            served_documents.append(document),
            open_browser("http://127.0.0.1:43123/", new=1),
        ),
    )

    cli._open_console_handoff_page(
        console_origin="https://console.example",
        handoff_token=token,
        timeout_seconds=5.0,
        open_browser=lambda url, new=0: browser_urls.append(url) or True,
    )

    assert browser_urls == ["http://127.0.0.1:43123/"]
    assert token not in browser_urls[0]
    assert 'method="post"' in served_documents[0]
    assert 'action="https://console.example/v1/console/open"' in served_documents[0]
    assert f'value="{token}"' in served_documents[0]
    assert "localStorage" not in served_documents[0]
    assert "sessionStorage" not in served_documents[0]
    assert "<script" not in served_documents[0]
