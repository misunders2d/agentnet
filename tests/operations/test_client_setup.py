from __future__ import annotations

import json
import os
import stat
from pathlib import Path
from types import SimpleNamespace

import pytest
from pydantic import SecretStr

from agentnet.identity.actors import ActorKind, VerifiedActor
from agentnet.operations.client_setup import (
    AmbiguousClientProfile,
    ClientIdentityProfile,
    ClientSetupContinuationStore,
    ClientSetupCoordinator,
    EnrollmentProgress,
    SetupContinuationExpired,
    SetupNextAction,
)
from agentnet.operations.endpoint_lifecycle import EndpointActivationState


def _actor(harness_id: str = "harness-v0144") -> VerifiedActor:
    return VerifiedActor(
        kind=ActorKind.VERIFIED_HUMAN_HARNESS,
        domain_id="domain.example",
        principal_id="principal-owner",
        harness_id=harness_id,
        credential_id=f"credential-{harness_id}",
        credential_epoch=3,
        binding_assurance="os_bound",
    )


def _profile(harness_id: str = "harness-v0144", *, profile_key: str = "default") -> ClientIdentityProfile:
    return ClientIdentityProfile(
        actor=_actor(harness_id),
        harness_kind="omp",
        profile_key=profile_key,
    )


class _Lifecycle:
    def __init__(self) -> None:
        self.registered: list[tuple[VerifiedActor, str, str]] = []
        self.requested: list[tuple[VerifiedActor, int]] = []
        self.statuses: dict[str, object] = {}

    @staticmethod
    def _status(endpoint_id: str, state: EndpointActivationState, revision: int) -> object:
        return SimpleNamespace(
            endpoint_id=endpoint_id,
            state=state,
            revision=revision,
            adapter_generation=1,
            public_url=None,
        )

    def register_existing(
        self,
        *,
        actor: VerifiedActor,
        harness_kind: str,
        profile_key: str,
    ) -> object:
        self.registered.append((actor, harness_kind, profile_key))
        endpoint_id = actor.harness_id or ""
        existing = self.statuses.get(endpoint_id)
        if existing is not None:
            return existing
        status = self._status(endpoint_id, EndpointActivationState.ACCESS_READY, 4)
        self.statuses[endpoint_id] = status
        return status

    def status(self, *, endpoint_id: str) -> object:
        return self.statuses[endpoint_id]

    def reconcile(self, *, endpoint_id: str) -> object:
        return self.status(endpoint_id=endpoint_id)

    def request_activation(self, *, actor: VerifiedActor, expected_revision: int) -> object:
        self.requested.append((actor, expected_revision))
        status = self._status(
            actor.harness_id or "",
            EndpointActivationState.RESTART_REQUIRED,
            expected_revision + 1,
        )
        self.statuses[actor.harness_id or ""] = status
        return status


class _Enrollment:
    def __init__(self, profiles: list[ClientIdentityProfile]) -> None:
        self.profiles = profiles
        self.begin_calls: list[str | None] = []
        self.status_calls: list[str] = []
        self.continue_calls: list[str] = []
        self.expire_on_continue = False

    def begin(self, *, replace_expired_continuation: str | None = None) -> EnrollmentProgress:
        self.begin_calls.append(replace_expired_continuation)
        suffix = "replacement" if replace_expired_continuation is not None else "fresh"
        return EnrollmentProgress(
            endpoint_id="harness-new",
            state=EndpointActivationState.READY_TO_CONNECT,
            continuation=SecretStr(f"opaque-{suffix}-continuation"),
            public_url="https://connect.example/start",
        )

    def status(self, *, continuation: str) -> EnrollmentProgress:
        self.status_calls.append(continuation)
        return EnrollmentProgress(
            endpoint_id="harness-new",
            state=EndpointActivationState.WAITING_FOR_APPROVAL,
            continuation=SecretStr(continuation),
            public_url="https://connect.example/approval",
        )

    def continue_setup(self, *, continuation: str) -> EnrollmentProgress:
        self.continue_calls.append(continuation)
        if self.expire_on_continue:
            self.expire_on_continue = False
            raise SetupContinuationExpired("authoritative continuation expired")
        self.profiles.append(_profile("harness-new"))
        return EnrollmentProgress(
            endpoint_id="harness-new",
            state=EndpointActivationState.ENROLLED,
            continuation=None,
            public_url=None,
        )


def _coordinator(
    tmp_path: Path,
    *,
    profiles: list[ClientIdentityProfile],
    lifecycle: _Lifecycle | None = None,
    enrollment: _Enrollment | None = None,
) -> tuple[ClientSetupCoordinator, _Lifecycle, _Enrollment, ClientSetupContinuationStore]:
    lifecycle = lifecycle or _Lifecycle()
    enrollment = enrollment or _Enrollment(profiles)
    continuation_store = ClientSetupContinuationStore(tmp_path / "private" / "continuation.json")
    return (
        ClientSetupCoordinator(
            endpoint_lifecycle=lifecycle,
            identity_profiles=lambda: tuple(profiles),
            enrollment=enrollment,
            continuation_store=continuation_store,
            harness_kind="omp",
            profile_key="default",
        ),
        lifecycle,
        enrollment,
        continuation_store,
    )


def test_setup_resumes_existing_v0144_enrollment_without_new_join(tmp_path: Path) -> None:
    profiles = [_profile()]
    coordinator, lifecycle, enrollment, _state = _coordinator(tmp_path, profiles=profiles)

    result = coordinator.setup()

    assert result.identity_created is False
    assert result.endpoint_id == "harness-v0144"
    assert result.state is EndpointActivationState.RESTART_REQUIRED
    assert result.next_action is SetupNextAction.RESTART_YOUR_AGENT
    assert enrollment.begin_calls == []
    assert lifecycle.registered == [(_actor(), "omp", "default")]
    assert lifecycle.requested == [(_actor(), 4)]


def test_fresh_setup_persists_only_owner_private_opaque_continuation(tmp_path: Path) -> None:
    profiles: list[ClientIdentityProfile] = []
    coordinator, _lifecycle, _enrollment, continuation_store = _coordinator(
        tmp_path,
        profiles=profiles,
    )

    result = coordinator.setup()

    assert result.endpoint_id == "harness-new"
    assert result.next_action is SetupNextAction.OPEN_BROWSER
    assert result.identity_created is False
    assert str(result.public_url) == "https://connect.example/start"
    persisted = json.loads(continuation_store.path.read_text(encoding="utf-8"))
    assert set(persisted) == {"schema", "continuation"}
    assert persisted["schema"] == "agentnet.client-setup-continuation.v1"
    assert persisted["continuation"] == "opaque-fresh-continuation"
    serialized_state = continuation_store.load().model_dump(mode="json")
    assert set(serialized_state) == {"schema", "continuation"}
    assert serialized_state["schema"] == "agentnet.client-setup-continuation.v1"
    assert stat.S_IMODE(continuation_store.path.stat().st_mode) == 0o600
    assert stat.S_IMODE(continuation_store.path.parent.stat().st_mode) == 0o700


def test_status_and_continue_resume_same_enrollment_then_bind_created_identity(tmp_path: Path) -> None:
    profiles: list[ClientIdentityProfile] = []
    coordinator, lifecycle, enrollment, continuation_store = _coordinator(
        tmp_path,
        profiles=profiles,
    )
    coordinator.setup()

    pending = coordinator.status()
    completed = coordinator.continue_setup()

    assert pending.state is EndpointActivationState.WAITING_FOR_APPROVAL
    assert pending.next_action is SetupNextAction.WAIT_FOR_APPROVAL
    assert completed.endpoint_id == "harness-new"
    assert completed.state is EndpointActivationState.RESTART_REQUIRED
    assert completed.next_action is SetupNextAction.RESTART_YOUR_AGENT
    assert completed.identity_created is True
    assert enrollment.status_calls == ["opaque-fresh-continuation"]
    assert enrollment.continue_calls == ["opaque-fresh-continuation"]
    assert lifecycle.registered[-1][0].harness_id == "harness-new"
    assert not continuation_store.path.exists()


def test_ambiguous_profiles_are_denied_without_registering_or_joining(tmp_path: Path) -> None:
    profiles = [_profile("harness-a"), _profile("harness-b")]
    coordinator, lifecycle, enrollment, _state = _coordinator(tmp_path, profiles=profiles)

    with pytest.raises(AmbiguousClientProfile, match="ambiguous"):
        coordinator.setup()

    assert lifecycle.registered == []
    assert enrollment.begin_calls == []


def test_expired_continuation_is_replaced_only_after_authoritative_expiry(tmp_path: Path) -> None:
    profiles: list[ClientIdentityProfile] = []
    enrollment = _Enrollment(profiles)
    enrollment.expire_on_continue = True
    coordinator, _lifecycle, _enrollment, continuation_store = _coordinator(
        tmp_path,
        profiles=profiles,
        enrollment=enrollment,
    )
    coordinator.setup()

    recovered = coordinator.continue_setup()

    assert recovered.next_action is SetupNextAction.OPEN_BROWSER
    assert enrollment.continue_calls == ["opaque-fresh-continuation"]
    assert enrollment.begin_calls == [None, "opaque-fresh-continuation"]
    assert continuation_store.load().continuation.get_secret_value() == "opaque-replacement-continuation"


def test_setup_performs_no_sudo_profile_mutation_or_process_signal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    profile = tmp_path / ".profile"
    profile.write_text("user-owned profile\n", encoding="utf-8")
    before = profile.read_bytes()
    monkeypatch.setattr(
        os,
        "kill",
        lambda *_args: (_ for _ in ()).throw(AssertionError("must not signal a harness")),
    )
    profiles = [_profile()]
    coordinator, _lifecycle, _enrollment, _state = _coordinator(tmp_path, profiles=profiles)

    result = coordinator.setup()

    assert result.next_action is SetupNextAction.RESTART_YOUR_AGENT
    assert profile.read_bytes() == before
