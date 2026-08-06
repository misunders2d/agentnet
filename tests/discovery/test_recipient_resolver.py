from __future__ import annotations

import time
from dataclasses import dataclass
from datetime import datetime
from types import SimpleNamespace

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.discovery.directory import DirectoryRecord, DirectoryService
from agentnet.discovery.recipient_resolver import AuthorizedRecipientResolver, ResolvedEndpoint
from agentnet.errors import ConflictError, ValidationError
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_json


@dataclass
class ScopeVisibility:
    store: object

    def active_recipient_members(
        self,
        *,
        actor,
        candidate_harness_ids: tuple[str, ...],
        action: str = "message.send",
        classification: Classification = Classification.C1_INTERNAL,
        when: datetime | None = None,
    ) -> tuple[SimpleNamespace, ...]:
        assert action == "message.send"
        assert classification is Classification.C1_INTERNAL
        if not candidate_harness_ids:
            return ()
        now = int((when or datetime.now().astimezone()).timestamp())
        placeholders = ",".join("?" for _ in candidate_harness_ids)
        with self.store.transaction(immediate=False) as connection:
            rows = connection.execute(
                f"""SELECT scope.scope_id,scope.revision,scope.policy_revision,
                           target.harness_id
                      FROM collaboration_scopes AS scope
                      JOIN domains AS domain ON domain.domain_id=scope.domain_id
                      JOIN collaboration_scope_members AS caller
                        ON caller.scope_id=scope.scope_id
                       AND caller.authority_kind='principal'
                       AND caller.authority_id=? AND caller.harness_id=?
                       AND caller.state='active'
                      JOIN collaboration_scope_members AS target
                        ON target.scope_id=scope.scope_id AND target.state='active'
                     WHERE scope.domain_id=? AND scope.state='active'
                       AND (scope.expires_at IS NULL OR scope.expires_at>?)
                       AND scope.policy_revision=domain.policy_revision
                       AND scope.domain_revocation_epoch=domain.revocation_epoch
                       AND target.harness_id IN ({placeholders})
                     ORDER BY scope.scope_id,target.harness_id""",
                (
                    actor.principal_id,
                    actor.harness_id,
                    actor.domain_id,
                    now,
                    *candidate_harness_ids,
                ),
            ).fetchall()
        return tuple(
            SimpleNamespace(
                scope_id=str(row["scope_id"]),
                scope_revision=int(row["revision"]),
                scope_policy_revision=int(row["policy_revision"]),
                harness_id=str(row["harness_id"]),
            )
            for row in rows
        )


@pytest.fixture
def resolver(store):
    return AuthorizedRecipientResolver(
        scopes=ScopeVisibility(store),
        directory=DirectoryService(store),
        store=store,
    )


def _make_endpoint(
    store,
    identity_factory,
    *,
    principal_id=None,
    domain="corp.example",
    kind="server",
    name="The enrolled server",
    state="connected",
):
    actor, _key = identity_factory(
        domain=domain,
        principal_id=principal_id,
        kind=kind,
        binding_assurance="os_bound",
    )
    now = int(time.time())
    with store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET display_name=? WHERE harness_id=?",
            (name, actor.harness_id),
        )
        connection.execute(
            """INSERT INTO endpoint_lifecycle(
                domain_id,harness_id,principal_id,current_credential_id,harness_kind,
                profile_key,state,adapter_generation,mailbox_cursor,capability_root_digest,
                process_measurement,state_reason,revision,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,?,1,0,NULL,NULL,'test endpoint state',1,?,?)""",
            (
                actor.domain_id,
                actor.harness_id,
                actor.principal_id,
                actor.credential_id,
                kind,
                f"profile:{actor.harness_id}",
                state,
                now,
                now,
            ),
        )
    return actor


def _add_directory_record(
    store,
    *,
    target,
    visible_to: tuple[str, ...],
    aliases: tuple[str, ...] = (),
    domain_id: str | None = None,
) -> DirectoryRecord:
    now = int(time.time())
    record = DirectoryRecord(
        record_id=f"agent:{target.harness_id}",
        record_type="agent",
        domain_id=domain_id or target.domain_id,
        epoch=1,
        attributes={"harness_id": target.harness_id}
        | ({"approved_aliases": list(aliases)} if aliases else {}),
        visible_to_principal_ids=tuple(sorted(visible_to)),
        expires_at=now + 600,
    )
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO directory_records(
                record_id,record_type,domain_id,epoch,record_json,status,expires_at,updated_at
            ) VALUES(?,?,?,?,?,'active',?,?)""",
            (
                record.record_id,
                record.record_type,
                record.domain_id,
                record.epoch,
                canonical_json(record.model_dump(mode="json")).decode("utf-8"),
                record.expires_at,
                now,
            ),
        )
    return record


def _add_scope(store, *, owner, targets, scope_id="scope-1", state="active", expires_at=None) -> None:
    now = int(time.time())
    revoked_at = now if state == "revoked" else None
    with store.transaction() as connection:
        connection.execute(
            """INSERT INTO collaboration_scopes(
                scope_id,schema_version,domain_id,scope_kind,owner_principal_id,
                owner_harness_id,source_communication_scope_id,state,state_reason,
                allowed_actions_json,allowed_resource_prefixes_json,
                allowed_classifications_json,canonical_references_json,policy_floor,
                policy_revision,domain_revocation_epoch,control_sequence,
                membership_sequence,proposal_digest,scope_digest,audit_record_hash,
                revision,created_at,updated_at,expires_at,revoked_at,archived_at,deleted_at
            ) VALUES(?,1,?,'direct',?,?,NULL,?,'test scope',?,?,?,?,1,1,1,1,1,?,?,?,1,?,?,?,?,NULL,NULL)""",
            (
                scope_id,
                owner.domain_id,
                owner.principal_id,
                owner.harness_id,
                state,
                canonical_json(["message.send"]).decode("utf-8"),
                canonical_json(["conversation:"]).decode("utf-8"),
                canonical_json(["C1"]).decode("utf-8"),
                canonical_json([]).decode("utf-8"),
                "1" * 64,
                f"{abs(hash(scope_id)):064x}"[-64:],
                "3" * 64,
                now - 100,
                now,
                expires_at,
                revoked_at,
            ),
        )
        for index, member in enumerate((owner, *targets), start=1):
            connection.execute(
                """INSERT INTO collaboration_scope_members(
                    scope_id,authority_kind,authority_id,harness_id,role,state,
                    joined_sequence,removed_sequence,member_digest,joined_at,removed_at
                ) VALUES(?,'principal',?,?,?,'active',?,NULL,?,?,NULL)""",
                (
                    scope_id,
                    member.principal_id,
                    member.harness_id,
                    "owner" if member.harness_id == owner.harness_id else "member",
                    index,
                    f"{index:064x}",
                    now - 100,
                ),
            )


def test_resolves_only_visible_exact_endpoint(store, identity_factory, resolver) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(store, identity_factory)
    _add_directory_record(store, target=target, visible_to=(actor.principal_id,))
    _add_scope(store, owner=actor, targets=(target,))

    assert resolver.resolve(actor=actor, query="  THE   enrolled\tserver ") == (
        ResolvedEndpoint(
            harness_id=target.harness_id,
            display_name="The enrolled server",
            harness_kind="server",
            availability="online",
            scope_id="scope-1",
        ),
    )


@pytest.mark.parametrize("query", ["Night queue", "SERVER"])
def test_matches_approved_alias_and_exact_harness_kind(
    store, identity_factory, resolver, query: str
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(store, identity_factory)
    _add_directory_record(
        store,
        target=target,
        visible_to=(actor.principal_id,),
        aliases=("Night queue",),
    )
    _add_scope(store, owner=actor, targets=(target,))

    assert resolver.resolve(actor=actor, query=query)[0].harness_id == target.harness_id


def test_offline_endpoint_remains_an_exact_resolvable_recipient(store, identity_factory, resolver) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(store, identity_factory, state="access_ready")
    _add_directory_record(store, target=target, visible_to=(actor.principal_id,))
    _add_scope(store, owner=actor, targets=(target,))

    assert resolver.resolve(actor=actor, query="The enrolled server")[0].availability == "offline"


@pytest.mark.parametrize(
    "statement",
    [
        "UPDATE endpoint_lifecycle SET state='blocked' WHERE harness_id=?",
        "UPDATE credentials SET status='revoked' WHERE harness_id=?",
    ],
)
def test_blocked_or_credential_stale_endpoint_fails_closed(
    store, identity_factory, resolver, statement: str
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(store, identity_factory)
    _add_directory_record(store, target=target, visible_to=(actor.principal_id,))
    _add_scope(store, owner=actor, targets=(target,))
    with store.transaction() as connection:
        connection.execute(statement, (target.harness_id,))

    with pytest.raises(ConflictError) as failure:
        resolver.resolve(actor=actor, query="The enrolled server")

    assert str(failure.value) == "recipient could not be resolved"
    assert target.harness_id not in str(failure.value)


def test_multiple_sibling_names_fail_without_listing(store, identity_factory, resolver) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    principal_id = f"principal-siblings-{time.time_ns()}"
    first = _make_endpoint(store, identity_factory, principal_id=principal_id, kind="pi", name="Sergey's Pi")
    second = _make_endpoint(store, identity_factory, principal_id=principal_id, kind="pi", name="Sergey's Pi")
    for target in (first, second):
        _add_directory_record(store, target=target, visible_to=(actor.principal_id,))
    _add_scope(store, owner=actor, targets=(first, second))

    with pytest.raises(ConflictError) as failure:
        resolver.resolve(actor=actor, query="Sergey's Pi")

    assert str(failure.value) == "recipient could not be resolved"
    assert first.harness_id not in str(failure.value)
    assert second.harness_id not in str(failure.value)


def test_hidden_endpoint_uses_the_same_non_enumerating_failure(
    store, identity_factory, resolver
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    other_viewer, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(store, identity_factory, name="Hidden payroll agent")
    _add_directory_record(
        store,
        target=target,
        visible_to=(other_viewer.principal_id,),
    )
    _add_scope(store, owner=actor, targets=(target,))

    with pytest.raises(ConflictError) as hidden:
        resolver.resolve(actor=actor, query="Hidden payroll agent")
    with pytest.raises(ConflictError) as absent:
        resolver.resolve(actor=actor, query="No such agent")

    assert str(hidden.value) == str(absent.value) == "recipient could not be resolved"
    assert target.harness_id not in str(hidden.value)
    assert "payroll" not in str(hidden.value).casefold()


def test_cross_domain_directory_row_is_not_a_resolution_candidate(
    store, identity_factory, resolver
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(
        store,
        identity_factory,
        domain="partner.example",
        name="Partner server",
    )
    _add_directory_record(
        store,
        target=target,
        visible_to=(actor.principal_id,),
        domain_id=target.domain_id,
    )

    with pytest.raises(ConflictError) as failure:
        resolver.resolve(actor=actor, query="Partner server")

    assert str(failure.value) == "recipient could not be resolved"
    assert target.harness_id not in str(failure.value)


@pytest.mark.parametrize("scope_state", ["revoked", "expired"])
def test_revoked_or_expired_scope_is_not_resolvable(
    store, identity_factory, resolver, scope_state: str
) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    target = _make_endpoint(store, identity_factory)
    _add_directory_record(store, target=target, visible_to=(actor.principal_id,))
    _add_scope(
        store,
        owner=actor,
        targets=(target,),
        state="revoked" if scope_state == "revoked" else "active",
        expires_at=int(time.time()) - 1 if scope_state == "expired" else None,
    )

    with pytest.raises(ConflictError, match="^recipient could not be resolved$"):
        resolver.resolve(actor=actor, query="The enrolled server")


def test_resolved_endpoint_is_strict_frozen() -> None:
    endpoint = ResolvedEndpoint(
        harness_id="harness-1",
        display_name="Endpoint",
        harness_kind="server",
        availability="unknown",
        scope_id="scope-1",
    )
    with pytest.raises(PydanticValidationError):
        endpoint.display_name = "changed"
    with pytest.raises(PydanticValidationError):
        ResolvedEndpoint.model_validate(endpoint.model_dump() | {"candidate_id": "hidden"})
    with pytest.raises(PydanticValidationError):
        ResolvedEndpoint.model_validate(endpoint.model_dump() | {"harness_kind": 1})


@pytest.mark.parametrize("query", ["", " \t\n ", "x" * 257])
def test_query_bounds_fail_before_directory_resolution(resolver, identity_factory, query: str) -> None:
    actor, _ = identity_factory(binding_assurance="os_bound")
    with pytest.raises(ValidationError, match="recipient query is outside the supported profile"):
        resolver.resolve(actor=actor, query=query)
