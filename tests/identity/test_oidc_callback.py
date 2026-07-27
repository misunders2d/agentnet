from __future__ import annotations

import pytest

from agentnet.errors import AuthenticationError
from agentnet.identity.oidc_callback import (
    OIDCCallbackError,
    OIDCCallbackSuccess,
    parse_oidc_callback_pairs,
)


STATE = "s" * 43
CODE = "authorization-code"


def test_callback_projects_recognized_fields_and_ignores_unique_extensions() -> None:
    success = parse_oidc_callback_pairs(
        [
            ("state", STATE),
            ("code", CODE),
            ("scope", "openid email"),
            ("authuser", "0"),
            ("prompt", "consent"),
        ]
    )
    assert success == OIDCCallbackSuccess(state=STATE, code=CODE)

    error = parse_oidc_callback_pairs(
        [
            ("state", STATE),
            ("error", "access_denied"),
            ("error_description", "owner canceled"),
            ("error_uri", "https://idp.example/errors/access_denied"),
            ("authuser", "0"),
        ]
    )
    assert error == OIDCCallbackError(
        state=STATE,
        error="access_denied",
        error_description="owner canceled",
        error_uri="https://idp.example/errors/access_denied",
    )


@pytest.mark.parametrize(
    "pairs",
    [
        [("state", STATE), ("state", STATE), ("code", CODE)],
        [("state", STATE), ("code", CODE), ("code", "other-code")],
        [("state", STATE), ("error", "access_denied"), ("error", "server_error")],
        [
            ("state", STATE),
            ("error", "access_denied"),
            ("error_description", "one"),
            ("error_description", "two"),
        ],
        [
            ("state", STATE),
            ("error", "access_denied"),
            ("error_uri", "https://idp.example/one"),
            ("error_uri", "https://idp.example/two"),
        ],
        [("state", STATE), ("code", CODE), ("scope", "openid"), ("scope", "email")],
    ],
)
def test_callback_rejects_every_duplicate_decoded_name(pairs: list[tuple[str, str]]) -> None:
    with pytest.raises(AuthenticationError, match="OIDC callback parameters are invalid"):
        parse_oidc_callback_pairs(pairs)


@pytest.mark.parametrize(
    "pairs",
    [
        [("state", STATE), ("code", CODE), ("error", "access_denied")],
        [("state", STATE), ("code", CODE), ("error_description", "denied")],
        [("state", STATE), ("code", CODE), ("error_uri", "https://idp.example/error")],
        [("state", STATE), ("scope", "openid")],
        [("code", CODE)],
        [("error", "access_denied")],
        [("state", STATE), ("error_description", "denied")],
        [("state", STATE), ("error_uri", "https://idp.example/error")],
        [("state", STATE), ("code", "")],
        [("state", STATE), ("error", "")],
    ],
)
def test_callback_rejects_mixed_or_incomplete_recognized_shapes(
    pairs: list[tuple[str, str]],
) -> None:
    with pytest.raises(AuthenticationError, match="OIDC callback parameters are invalid"):
        parse_oidc_callback_pairs(pairs)
