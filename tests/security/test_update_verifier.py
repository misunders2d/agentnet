from __future__ import annotations

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.errors import AuthenticationError, ValidationError
from agentnet.security.signatures import P256KeyPair
from agentnet.security.update import (
    UPDATE_SIGNATURE_PURPOSE,
    UpdateTrustRoot,
    UpdateVerificationState,
    verify_threshold_manifest,
)


NOW = 1_800_000_000


def manifest(**updates: object) -> dict[str, object]:
    value: dict[str, object] = {
        "schema": "agentnet.update.manifest.v1",
        "product": "agentnet",
        "channel": "stable",
        "version": "1.1.0",
        "release_sequence": 2,
        "root_version": 4,
        "published_at": NOW - 60,
        "expires_at": NOW + 3600,
        "minimum_installed_version": "1.0.0",
        "maximum_installed_version": "1.0.0",
        "artifacts": [
            {
                "platform": "linux",
                "architecture": "x86_64",
                "uri": "https://updates.example/agentnet-1.1.0-x86_64.whl",
                "sha256": "a" * 64,
                "size": 12345,
            }
        ],
    }
    value.update(updates)
    return value


def root(keys: dict[str, P256KeyPair], **updates: object) -> UpdateTrustRoot:
    value: dict[str, object] = {
        "schema": "agentnet.update.root.v1",
        "root_version": 4,
        "expires_at": NOW + 86_400,
        "threshold": 2,
        "keys": {key_id: key.public_pem for key_id, key in keys.items()},
        "max_manifest_lifetime_seconds": 86_400,
        "max_freeze_seconds": 600,
    }
    value.update(updates)
    return UpdateTrustRoot.model_validate(value)


def state(**updates: object) -> UpdateVerificationState:
    value: dict[str, object] = {
        "installed_version": "1.0.0",
        "installed_sequence": 1,
        "highest_seen_version": "1.0.0",
        "highest_seen_sequence": 1,
        "highest_seen_manifest_digest": "b" * 64,
        "last_advance_at": NOW - 100,
    }
    value.update(updates)
    return UpdateVerificationState.model_validate(value)


def signatures(value: dict[str, object], keys: dict[str, P256KeyPair], purpose: str = UPDATE_SIGNATURE_PURPOSE):
    return [
        {"key_id": key_id, "signature": key.sign(purpose, value)}
        for key_id, key in keys.items()
    ]


def test_exact_update_manifest_satisfies_dedicated_bounded_threshold() -> None:
    keys = {"update-1": P256KeyPair.generate(), "update-2": P256KeyPair.generate()}
    value = manifest()
    verified = verify_threshold_manifest(value, signatures(value, keys), root(keys), state=state(), now=NOW)
    assert verified.version == "1.1.0"


def test_zero_or_impossible_update_threshold_is_rejected_at_root_parse() -> None:
    keys = {"update-1": P256KeyPair.generate()}
    with pytest.raises(PydanticValidationError):
        root(keys, threshold=0)
    with pytest.raises(PydanticValidationError, match="exceeds"):
        root(keys, threshold=2)


def test_application_event_signatures_do_not_satisfy_update_purpose() -> None:
    keys = {"update-1": P256KeyPair.generate(), "update-2": P256KeyPair.generate()}
    value = manifest()
    wrong = signatures(value, keys, purpose="agentnet.event.origin.v1")
    with pytest.raises(AuthenticationError, match="threshold"):
        verify_threshold_manifest(value, wrong, root(keys), state=state(), now=NOW)


def test_unknown_manifest_or_signature_fields_fail_exact_schema() -> None:
    keys = {"update-1": P256KeyPair.generate(), "update-2": P256KeyPair.generate()}
    value = manifest(install_script="curl | sh")
    with pytest.raises(ValidationError, match="schema"):
        verify_threshold_manifest(value, [], root(keys), state=state(), now=NOW)

    valid = manifest()
    signed = signatures(valid, keys)
    signed[0]["purpose"] = UPDATE_SIGNATURE_PURPOSE
    with pytest.raises(ValidationError, match="signature schema"):
        verify_threshold_manifest(valid, signed, root(keys), state=state(), now=NOW)


@pytest.mark.parametrize(
    ("updates", "reason"),
    [
        ({"expires_at": NOW}, "validity"),
        ({"published_at": NOW + 301}, "validity"),
        ({"root_version": 3}, "root version"),
        ({"expires_at": NOW + 100_000}, "validity window"),
        ({"minimum_installed_version": "1.0.1", "maximum_installed_version": "1.1.0"}, "compatibility"),
        ({"version": "0.9.9", "release_sequence": 3}, "rollback"),
        ({"version": "1.1.0", "release_sequence": 0}, "schema"),
    ],
)
def test_expiry_version_and_rollback_checks(updates: dict[str, object], reason: str) -> None:
    keys = {"update-1": P256KeyPair.generate(), "update-2": P256KeyPair.generate()}
    value = manifest(**updates)
    with pytest.raises((AuthenticationError, ValidationError), match=reason):
        verify_threshold_manifest(value, signatures(value, keys), root(keys), state=state(), now=NOW)


def test_same_metadata_is_rejected_after_bounded_freeze_window() -> None:
    keys = {"update-1": P256KeyPair.generate(), "update-2": P256KeyPair.generate()}
    value = manifest()
    from agentnet.security.signatures import canonical_digest

    frozen_state = state(
        highest_seen_version="1.1.0",
        highest_seen_sequence=2,
        highest_seen_manifest_digest=canonical_digest(value),
        last_advance_at=NOW - 601,
    )
    with pytest.raises(AuthenticationError, match="freeze"):
        verify_threshold_manifest(value, signatures(value, keys), root(keys), state=frozen_state, now=NOW)


def test_same_sequence_manifest_equivocation_is_rejected() -> None:
    keys = {"update-1": P256KeyPair.generate(), "update-2": P256KeyPair.generate()}
    value = manifest()
    equivocation_state = state(
        highest_seen_version="1.1.0",
        highest_seen_sequence=2,
        highest_seen_manifest_digest="c" * 64,
    )
    with pytest.raises(AuthenticationError, match="equivocation"):
        verify_threshold_manifest(value, signatures(value, keys), root(keys), state=equivocation_state, now=NOW)
