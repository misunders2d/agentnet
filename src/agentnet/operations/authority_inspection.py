"""Privacy-safe authorization explanations and caller-owned authority inventory.

These read-only services consume a proof-derived :class:`TrustedTransportContext`.
They never accept a principal, guest, harness, or domain identifier from request
payload data.  Every read rechecks the exact actor against current persisted
identity state before returning caller-owned information.

The returned inventory is descriptive only.  A basis marked ``current`` still
has to be intersected with the exact operation, current actor constraints, and
any required task grant by the policy engine.  This module cannot issue, widen,
consume, or revoke authority.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError as PydanticValidationError

from agentnet.authorization.policy import validate_actor_state
from agentnet.errors import AuthorizationError
from agentnet.identity.actors import ActorKind, TrustedTransportContext, VerifiedActor
from agentnet.protocol.models import TaskGrant
from agentnet.storage.backend import StoreBackend


_UNAVAILABLE = "authorization information is unavailable"


class _StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class DenialExplanationQuery(_StrictModel):
    """Untrusted request data for one non-enumerating decision lookup."""

    decision_id: str = Field(min_length=1, max_length=256)


class DenialCategory(StrEnum):
    IDENTITY = "identity"
    POLICY_REVISION = "policy_revision"
    POSITIVE_AUTHORITY = "positive_authority"
    GRANT_LIFECYCLE = "grant_lifecycle"
    SCOPE = "scope"
    ELIGIBILITY = "eligibility"
    CONCURRENT_STATE = "concurrent_state"
    POLICY = "policy"


class DenialRemediation(StrEnum):
    REAUTHENTICATE = "reauthenticate"
    REFRESH_POLICY = "refresh_policy"
    REQUEST_AUTHORITY = "request_authority"
    REPLACE_GRANT = "replace_grant"
    REQUEST_EXACT_SCOPE = "request_exact_scope"
    RESTORE_ELIGIBILITY = "restore_eligibility"
    RETRY_FRESH = "retry_with_fresh_state"
    CONTACT_AUTHORITY_ADMINISTRATOR = "contact_authority_administrator"


class DenialExplanation(_StrictModel):
    """A bounded explanation which contains no protected request material."""

    schema_version: Literal[1] = 1
    decision_id: str = Field(min_length=1, max_length=256)
    occurred_at: datetime
    category: DenialCategory
    reason_code: str = Field(min_length=1, max_length=64, pattern=r"^[a-z0-9_]+$")
    message: str = Field(min_length=1, max_length=256)
    remediation: DenialRemediation
    retryable: bool
    protected_details_withheld: Literal[True] = True


class _SafeDenial(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    category: DenialCategory
    reason_code: str
    message: str
    remediation: DenialRemediation
    retryable: bool


_IDENTITY_REASONS = frozenset(
    {
        "actor_kind_has_no_positive_authority",
        "binding_assurance_mismatch",
        "credential_binding_mismatch",
        "credential_not_active",
        "credential_outside_validity",
        "domain_not_active",
        "guest_binding_mismatch",
        "guest_not_active",
        "harness_domain_mismatch",
        "harness_not_active",
        "missing_credential_state",
        "missing_domain_state",
        "missing_guest_state",
        "missing_harness_state",
        "missing_principal_state",
        "principal_binding_mismatch",
        "principal_not_active",
        "stale_harness_credential_epoch",
        "stale_task_grant_credential_epoch",
        "task_grant_actor_mismatch",
    }
)
_POLICY_REVISION_REASONS = frozenset(
    {
        "stale_policy_revision",
        "stale_positive_entitlement",
        "stale_task_grant_policy_binding",
    }
)
_AUTHORITY_REASONS = frozenset(
    {
        "actor_has_no_positive_authority",
        "exact_task_grant_required",
        "missing_task_grant",
        "missing_task_grant_authority_binding",
        "no_current_positive_entitlement",
        "no_positive_human_entitlement",
    }
)
_SCOPE_REASONS = frozenset(
    {
        "task_grant_action_mismatch",
        "task_grant_class_mismatch",
        "task_grant_request_mismatch",
        "task_grant_resource_mismatch",
        "task_grant_sink_mismatch",
        "task_grant_source_mismatch",
    }
)
_ELIGIBILITY_REASONS = frozenset(
    {
        "binding_assurance_below_policy_floor",
        "capability_ineligible",
        "credential_not_fresh",
        "device_ineligible",
        "harness_ineligible",
        "session_ineligible",
    }
)


def _safe_denial(reason: str) -> _SafeDenial:
    """Map an internal reason to a stable message without echoing raw data."""

    if reason in _IDENTITY_REASONS:
        return _SafeDenial(
            category=DenialCategory.IDENTITY,
            reason_code="identity_not_current",
            message="The authenticated identity or credential was not current for this decision.",
            remediation=DenialRemediation.REAUTHENTICATE,
            retryable=True,
        )
    if reason in _POLICY_REVISION_REASONS:
        return _SafeDenial(
            category=DenialCategory.POLICY_REVISION,
            reason_code="policy_revision_changed",
            message="The decision did not use the currently required authority revision.",
            remediation=DenialRemediation.REFRESH_POLICY,
            retryable=True,
        )
    if reason in _AUTHORITY_REASONS:
        return _SafeDenial(
            category=DenialCategory.POSITIVE_AUTHORITY,
            reason_code="current_authority_required",
            message="A current positive-authority basis required for the operation was unavailable.",
            remediation=DenialRemediation.REQUEST_AUTHORITY,
            retryable=False,
        )
    if reason == "task_grant_revoked":
        return _SafeDenial(
            category=DenialCategory.GRANT_LIFECYCLE,
            reason_code="task_grant_revoked",
            message="The exact task grant had been revoked.",
            remediation=DenialRemediation.REPLACE_GRANT,
            retryable=False,
        )
    if reason == "task_grant_expired":
        return _SafeDenial(
            category=DenialCategory.GRANT_LIFECYCLE,
            reason_code="task_grant_expired",
            message="The exact task grant had expired.",
            remediation=DenialRemediation.REPLACE_GRANT,
            retryable=False,
        )
    if reason == "task_grant_exhausted":
        return _SafeDenial(
            category=DenialCategory.GRANT_LIFECYCLE,
            reason_code="task_grant_exhausted",
            message="The exact task grant had no remaining uses.",
            remediation=DenialRemediation.REPLACE_GRANT,
            retryable=False,
        )
    if reason in _SCOPE_REASONS:
        return _SafeDenial(
            category=DenialCategory.SCOPE,
            reason_code="task_grant_scope_mismatch",
            message="The exact requested operation was outside the supplied task-grant scope.",
            remediation=DenialRemediation.REQUEST_EXACT_SCOPE,
            retryable=False,
        )
    if reason in _ELIGIBILITY_REASONS or reason.startswith("eligibility_denied:"):
        return _SafeDenial(
            category=DenialCategory.ELIGIBILITY,
            reason_code="eligibility_constraint",
            message="A deny-only eligibility constraint blocked the operation.",
            remediation=DenialRemediation.RESTORE_ELIGIBILITY,
            retryable=True,
        )
    if reason in {
        "inconsistent_task_grant_state",
        "invalid_task_grant_state",
        "task_grant_raced_or_exhausted",
    }:
        return _SafeDenial(
            category=DenialCategory.CONCURRENT_STATE,
            reason_code="authority_state_changed",
            message="The authority state changed or could not be validated coherently.",
            remediation=DenialRemediation.RETRY_FRESH,
            retryable=True,
        )
    return _SafeDenial(
        category=DenialCategory.POLICY,
        reason_code="policy_denied",
        message="Current policy denied the operation; protected decision details are withheld.",
        remediation=DenialRemediation.CONTACT_AUTHORITY_ADMINISTRATOR,
        retryable=False,
    )


class AuthorityBasisType(StrEnum):
    HUMAN_ENTITLEMENT = "human_entitlement"
    TASK_GRANT = "task_grant"


class AuthorityBasisRole(StrEnum):
    POSITIVE_HUMAN_AUTHORITY = "positive_human_authority"
    HOST_GUEST_POSITIVE_GRANT = "host_guest_positive_grant"
    ATTENUATION_ONLY = "attenuation_only"


class AuthorityBasisState(StrEnum):
    CURRENT = "current"
    REVOKED = "revoked"
    EXPIRED = "expired"
    EXHAUSTED = "exhausted"
    STALE_POLICY = "stale_policy"
    STALE_CREDENTIAL = "stale_credential"
    INCONSISTENT = "inconsistent"


class AuthorityBasis(_StrictModel):
    """One caller-owned authority record, never an authorization result."""

    basis_type: AuthorityBasisType
    basis_role: AuthorityBasisRole
    basis_id: str = Field(min_length=1, max_length=256)
    state: AuthorityBasisState
    actions: tuple[str, ...]
    resources: tuple[str, ...]
    input_sources: tuple[str, ...] = ()
    output_sinks: tuple[str, ...] = ()
    data_classes: tuple[str, ...] = ()
    policy_revision: int | None = Field(default=None, ge=1)
    credential_epoch: int | None = Field(default=None, ge=1)
    issued_at: datetime | None = None
    expires_at: datetime | None = None
    revoked_at: datetime | None = None
    uses: int | None = Field(default=None, ge=0)
    max_uses: int | None = Field(default=None, ge=1)
    independently_authorizes_operation: Literal[False] = False


class AuthorityInventory(_StrictModel):
    """Current caller identity plus only its own authority bases."""

    schema_version: Literal[1] = 1
    generated_at: datetime
    authority_kind: Literal["human", "guest"]
    domain_id: str = Field(min_length=1, max_length=256)
    authority_id: str = Field(min_length=1, max_length=256)
    authenticated_harness_id: str = Field(min_length=1, max_length=256)
    current_policy_revision: int = Field(ge=1)
    bases: tuple[AuthorityBasis, ...]
    descriptive_only: Literal[True] = True
    grants_no_new_authority: Literal[True] = True


class _TaskGrantAuthorityBinding(_StrictModel):
    """Strict persisted binding written atomically with a task grant."""

    schema_id: Literal["agentnet.task-grant.authority-binding.v1"] = Field(alias="schema")
    grant_id: str = Field(min_length=1)
    domain_id: str = Field(min_length=1)
    principal_id: str = Field(min_length=1)
    harness_id: str = Field(min_length=1)
    policy_revision: int = Field(ge=1)
    harness_credential_epoch: int = Field(ge=1)
    issued_at: int = Field(ge=0)


def _epoch(value: datetime) -> int:
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError("inspection timestamps must be timezone-aware")
    return int(value.timestamp())


def _timestamp(value: Any) -> datetime | None:
    if value is None:
        return None
    return datetime.fromtimestamp(int(value), UTC)


def _same_positive_authority(first: VerifiedActor, second: VerifiedActor) -> bool:
    """Compare the exact authority owner without treating a sibling as a grant."""

    return bool(
        first.kind is second.kind
        and first.domain_id == second.domain_id
        and first.positive_authority_id is not None
        and first.positive_authority_id == second.positive_authority_id
    )


class AuthorityInspectionService:
    """Read-only operational inspection over one backend-neutral store."""

    def __init__(self, store: StoreBackend) -> None:
        self.store = store

    @staticmethod
    def _require_current_actor(
        connection: Any,
        *,
        transport: TrustedTransportContext,
        when: datetime,
    ) -> tuple[VerifiedActor, int]:
        actor = transport.actor
        if actor.kind not in {ActorKind.VERIFIED_HUMAN_HARNESS, ActorKind.HOST_GUEST_HARNESS}:
            raise AuthorizationError(_UNAVAILABLE)
        domain = connection.execute(
            "SELECT policy_revision FROM domains WHERE domain_id=?",
            (actor.domain_id,),
        ).fetchone()
        if domain is None:
            raise AuthorizationError(_UNAVAILABLE)
        revision = int(domain["policy_revision"])
        denial, current_revision = validate_actor_state(
            connection,
            actor=actor,
            expected_policy_revision=revision,
            when=when,
        )
        if denial is not None or current_revision != revision:
            raise AuthorizationError(_UNAVAILABLE)
        return actor, revision

    def explain_denial(
        self,
        *,
        transport: TrustedTransportContext,
        query: DenialExplanationQuery,
        when: datetime | None = None,
    ) -> DenialExplanation:
        """Explain only a denial belonging to the authenticated authority owner.

        Missing IDs, allowed decisions, malformed rows, and decisions owned by a
        different authority all return the same error.  Resource/context bytes,
        the raw internal reason, action, payload, and actor representation are
        never returned.
        """

        when = when or datetime.now(UTC)
        _epoch(when)
        with self.store.transaction(immediate=False) as connection:
            caller, _ = self._require_current_actor(connection, transport=transport, when=when)
            row = connection.execute(
                """SELECT decision_id,occurred_at,actor_json,allowed,reason
                     FROM policy_decisions WHERE decision_id=?""",
                (query.decision_id,),
            ).fetchone()
            if row is None or bool(row["allowed"]):
                raise AuthorizationError(_UNAVAILABLE)
            try:
                owner = VerifiedActor.model_validate(json.loads(str(row["actor_json"])))
                reason = str(row["reason"])
                occurred_at = datetime.fromtimestamp(int(row["occurred_at"]), UTC)
            except (TypeError, ValueError, PydanticValidationError):
                raise AuthorizationError(_UNAVAILABLE) from None
            if not _same_positive_authority(caller, owner):
                raise AuthorizationError(_UNAVAILABLE)
            safe = _safe_denial(reason)
            return DenialExplanation(
                decision_id=str(row["decision_id"]),
                occurred_at=occurred_at,
                category=safe.category,
                reason_code=safe.reason_code,
                message=safe.message,
                remediation=safe.remediation,
                retryable=safe.retryable,
            )

    @staticmethod
    def _entitlement_basis(row: Any, *, current_revision: int, now: int) -> AuthorityBasis:
        expires_at = _timestamp(row["expires_at"])
        revoked_at = _timestamp(row["revoked_at"])
        if revoked_at is not None:
            state = AuthorityBasisState.REVOKED
        elif expires_at is not None and int(row["expires_at"]) <= now:
            state = AuthorityBasisState.EXPIRED
        elif int(row["revision"]) != current_revision:
            state = AuthorityBasisState.STALE_POLICY
        else:
            state = AuthorityBasisState.CURRENT
        return AuthorityBasis(
            basis_type=AuthorityBasisType.HUMAN_ENTITLEMENT,
            basis_role=AuthorityBasisRole.POSITIVE_HUMAN_AUTHORITY,
            basis_id=str(row["entitlement_id"]),
            state=state,
            actions=(str(row["action"]),),
            resources=(str(row["resource_pattern"]),),
            policy_revision=int(row["revision"]),
            expires_at=expires_at,
            revoked_at=revoked_at,
        )

    @staticmethod
    def _task_grant_basis(
        connection: Any,
        row: Any,
        *,
        actor: VerifiedActor,
        current_revision: int,
        now: int,
    ) -> AuthorityBasis:
        try:
            grant = TaskGrant.model_validate(json.loads(str(row["grant_json"])))
        except (TypeError, ValueError, PydanticValidationError):
            raise AuthorizationError(_UNAVAILABLE) from None
        binding_row = connection.execute(
            "SELECT value FROM metadata WHERE key=?",
            (f"authority-binding:task-grant:{row['grant_id']}",),
        ).fetchone()
        binding: _TaskGrantAuthorityBinding | None = None
        if binding_row is not None:
            try:
                binding = _TaskGrantAuthorityBinding.model_validate(json.loads(str(binding_row["value"])))
            except (TypeError, ValueError, PydanticValidationError):
                binding = None

        row_revoked_at = _timestamp(row["revoked_at"])
        grant_revoked_at = grant.revoked_at
        revoked_at = row_revoked_at or grant_revoked_at
        expires_at = datetime.fromtimestamp(int(row["expires_at"]), UTC)
        row_consistent = bool(
            grant.grant_id == row["grant_id"]
            and grant.domain_id == row["domain_id"] == actor.domain_id
            and grant.principal_id == row["principal_id"] == actor.positive_authority_id
            and grant.harness_id == row["harness_id"] == actor.harness_id
            and grant.max_uses == int(row["max_uses"])
            and int(grant.expires_at.timestamp()) == int(row["expires_at"])
        )
        binding_consistent = bool(
            binding is not None
            and binding.grant_id == grant.grant_id
            and binding.domain_id == grant.domain_id
            and binding.principal_id == grant.principal_id
            and binding.harness_id == grant.harness_id
        )
        if not row_consistent or not binding_consistent:
            state = AuthorityBasisState.INCONSISTENT
        elif revoked_at is not None:
            state = AuthorityBasisState.REVOKED
        elif int(row["expires_at"]) <= now:
            state = AuthorityBasisState.EXPIRED
        elif int(row["uses"]) >= int(row["max_uses"]):
            state = AuthorityBasisState.EXHAUSTED
        elif binding is not None and binding.policy_revision != current_revision:
            state = AuthorityBasisState.STALE_POLICY
        elif binding is not None and binding.harness_credential_epoch != actor.credential_epoch:
            state = AuthorityBasisState.STALE_CREDENTIAL
        else:
            state = AuthorityBasisState.CURRENT

        return AuthorityBasis(
            basis_type=AuthorityBasisType.TASK_GRANT,
            basis_role=(
                AuthorityBasisRole.HOST_GUEST_POSITIVE_GRANT
                if actor.kind is ActorKind.HOST_GUEST_HARNESS
                else AuthorityBasisRole.ATTENUATION_ONLY
            ),
            basis_id=grant.grant_id,
            state=state,
            actions=tuple(sorted(grant.actions)),
            resources=tuple(sorted(grant.resources)),
            input_sources=tuple(sorted(grant.input_sources)),
            output_sinks=tuple(sorted(grant.output_sinks)),
            data_classes=tuple(sorted(item.value for item in grant.data_classes)),
            policy_revision=binding.policy_revision if binding is not None else None,
            credential_epoch=binding.harness_credential_epoch if binding is not None else None,
            issued_at=(
                datetime.fromtimestamp(binding.issued_at, UTC)
                if binding is not None
                else None
            ),
            expires_at=expires_at,
            revoked_at=revoked_at,
            uses=int(row["uses"]),
            max_uses=int(row["max_uses"]),
        )

    def authority_inventory(
        self,
        *,
        transport: TrustedTransportContext,
        when: datetime | None = None,
    ) -> AuthorityInventory:
        """Return all and only authority records owned by the current caller."""

        when = when or datetime.now(UTC)
        now = _epoch(when)
        with self.store.transaction(immediate=False) as connection:
            actor, current_revision = self._require_current_actor(
                connection,
                transport=transport,
                when=when,
            )
            authority_id = actor.positive_authority_id
            if authority_id is None or actor.harness_id is None:
                raise AuthorizationError(_UNAVAILABLE)
            bases: list[AuthorityBasis] = []
            if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS:
                entitlement_rows = connection.execute(
                    """SELECT entitlement_id,action,resource_pattern,expires_at,revoked_at,revision
                         FROM entitlements
                        WHERE domain_id=? AND principal_id=?
                        ORDER BY entitlement_id""",
                    (actor.domain_id, authority_id),
                ).fetchall()
                bases.extend(
                    self._entitlement_basis(row, current_revision=current_revision, now=now)
                    for row in entitlement_rows
                )
            grant_rows = connection.execute(
                """SELECT grant_id,domain_id,principal_id,harness_id,grant_json,
                          max_uses,uses,expires_at,revoked_at
                     FROM task_grants
                    WHERE domain_id=? AND principal_id=? AND harness_id=?
                    ORDER BY grant_id""",
                (actor.domain_id, authority_id, actor.harness_id),
            ).fetchall()
            bases.extend(
                self._task_grant_basis(
                    connection,
                    row,
                    actor=actor,
                    current_revision=current_revision,
                    now=now,
                )
                for row in grant_rows
            )
            bases.sort(key=lambda item: (item.basis_type.value, item.basis_id))
            return AuthorityInventory(
                generated_at=when,
                authority_kind=(
                    "human" if actor.kind is ActorKind.VERIFIED_HUMAN_HARNESS else "guest"
                ),
                domain_id=actor.domain_id,
                authority_id=authority_id,
                authenticated_harness_id=actor.harness_id,
                current_policy_revision=current_revision,
                bases=tuple(bases),
            )
