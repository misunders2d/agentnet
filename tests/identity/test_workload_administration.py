from __future__ import annotations

import time
from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest

from agentnet.authorization.evidence import (
    AUTHORITY_COMMAND_PURPOSE,
    IssuanceAuthority,
    SignedAuthorityCommand,
)
from agentnet.authorization.policy import (
    AuthorizationRequest,
    HumanEntitlement,
    LocalConformancePolicyEngine,
)
from agentnet.errors import AuthenticationError, ConflictError
from agentnet.identity.workload import WorkloadIdentity, WorkloadRegistry
from agentnet.security.signatures import P256KeyPair, canonical_digest


def _authority(
    store,
    *,
    actor,
    actor_key,
    action: str,
    resource: str,
    mutation: dict[str, object],
    entity_revision: int,
    reason: str,
) -> tuple[IssuanceAuthority, SignedAuthorityCommand]:
    now = datetime.now(UTC)
    engine = LocalConformancePolicyEngine(store)
    engine.bootstrap_entitlement_for_local_conformance(
        HumanEntitlement(
            domain_id=actor.domain_id,
            principal_id=actor.principal_id,
            action=action,
            resource_pattern=resource,
            revision=engine.current_policy_revision(actor, when=now),
            expires_at=now + timedelta(minutes=20),
        ),
        when=now,
    )
    request_digest = canonical_digest(mutation)
    decision = engine.require(
        AuthorizationRequest(
            actor=actor,
            action=action,
            resource=resource,
            policy_revision=engine.current_policy_revision(actor, when=now),
            context={"request_digest": request_digest},
        ),
        when=now,
    )
    fields = SignedAuthorityCommand.signing_fields(
        command_id=str(uuid4()),
        actor=actor,
        action=action,
        resource=resource,
        request_digest=request_digest,
        expected_policy_revision=1,
        expected_entity_revision=entity_revision,
        reason=reason,
        issued_at=now,
        expires_at=now + timedelta(minutes=2),
    )
    return (
        IssuanceAuthority(actor=actor, policy_decision_id=decision.decision_id),
        SignedAuthorityCommand(
            **fields,
            signature=actor_key.sign(AUTHORITY_COMMAND_PURPOSE, fields),
        ),
    )


def test_workload_register_renew_revoke_is_pop_bound_revision_fenced_and_transport_current(
    store,
    identity_factory,
) -> None:
    administrator, administrator_key = identity_factory(binding_assurance="os_bound")
    registry = WorkloadRegistry(store)
    now = int(time.time())
    domain_epoch = int(
        store.fetch_one(
            "SELECT revocation_epoch FROM domains WHERE domain_id=?",
            (administrator.domain_id,),
        )["revocation_epoch"]
    )
    registration_id = f"workload-registration-{uuid4().hex}"
    session_id = f"workload-session-{uuid4().hex}"
    workload_key = P256KeyPair.generate()
    identity = WorkloadIdentity(
        spiffe_id=f"spiffe://{administrator.domain_id}/mailbox/worker-1",
        trust_domain=administrator.domain_id,
        workload_role="mailbox_dispatcher",
        certificate_serial="serial-initial",
    )
    registration = registry.registration_request(
        registration_id=registration_id,
        domain_id=administrator.domain_id,
        workload_id="mailbox.worker-1",
        workload_role=identity.workload_role,
        recipient_scope="*",
        process_id=4201,
        process_start_time=now - 10,
        session_id=session_id,
        identity=identity,
        public_key_pem=workload_key.public_pem,
        key_id=workload_key.thumbprint,
        credential_epoch=1,
        revocation_epoch=domain_epoch,
        parent_event_id=None,
        task_grant_id=None,
        issued_at=now,
        expires_at=now + 600,
    )
    authority, command = _authority(
        store,
        actor=administrator,
        actor_key=administrator_key,
        action="identity.workload.register",
        resource=f"workload:{registration_id}",
        mutation=registration,
        entity_revision=0,
        reason="register exact mailbox dispatcher",
    )
    with pytest.raises(AuthenticationError):
        registry.register(
            authority=authority,
            command=command,
            registration_id=registration_id,
            domain_id=administrator.domain_id,
            workload_id="mailbox.worker-1",
            workload_role=identity.workload_role,
            recipient_scope="*",
            process_id=4201,
            process_start_time=now - 10,
            session_id=session_id,
            identity=identity,
            public_key_pem=workload_key.public_pem,
            key_id=workload_key.thumbprint,
            credential_epoch=1,
            revocation_epoch=domain_epoch,
            parent_event_id=None,
            task_grant_id=None,
            issued_at=now,
            expires_at=now + 600,
            possession_signature="not-a-signature",
        )
    actor = registry.register(
        authority=authority,
        command=command,
        registration_id=registration_id,
        domain_id=administrator.domain_id,
        workload_id="mailbox.worker-1",
        workload_role=identity.workload_role,
        recipient_scope="*",
        process_id=4201,
        process_start_time=now - 10,
        session_id=session_id,
        identity=identity,
        public_key_pem=workload_key.public_pem,
        key_id=workload_key.thumbprint,
        credential_epoch=1,
        revocation_epoch=domain_epoch,
        parent_event_id=None,
        task_grant_id=None,
        issued_at=now,
        expires_at=now + 600,
        possession_signature=workload_key.sign("agentnet.workload.registration.pop.v1", registration),
    )
    transport = registry.spiffe.transport_authority.bind_verified_peer(
        {
            "schema_version": "1.0",
            "spiffe_id": identity.spiffe_id,
            "trust_domain": identity.trust_domain,
            "workload_role": identity.workload_role,
            "certificate_serial": identity.certificate_serial,
            "process_id": 4201,
            "process_start_time": now - 10,
            "session_id": session_id,
        }
    )
    assert registry.resolve(
        transport=transport,
        registration_id=registration_id,
        process_id=4201,
        process_start_time=now - 10,
        session_id=session_id,
    ).audit_view() == actor.audit_view()

    renewed_key = P256KeyPair.generate()
    renewed_identity = WorkloadIdentity(
        spiffe_id=identity.spiffe_id,
        trust_domain=identity.trust_domain,
        workload_role=identity.workload_role,
        certificate_serial="serial-renewed",
    )
    renewed_session = f"workload-session-{uuid4().hex}"
    renewal = registry.renewal_request(
        registration_id=registration_id,
        expected_credential_epoch=1,
        credential_epoch=2,
        revocation_epoch=domain_epoch,
        process_id=4202,
        process_start_time=now,
        session_id=renewed_session,
        identity=renewed_identity,
        public_key_pem=renewed_key.public_pem,
        key_id=renewed_key.thumbprint,
        issued_at=now,
        expires_at=now + 900,
    )
    renewal_authority, renewal_command = _authority(
        store,
        actor=administrator,
        actor_key=administrator_key,
        action="identity.workload.renew",
        resource=f"workload:{registration_id}",
        mutation=renewal,
        entity_revision=1,
        reason="renew exact worker process",
    )
    renewed = registry.renew(
        authority=renewal_authority,
        command=renewal_command,
        registration_id=registration_id,
        expected_credential_epoch=1,
        process_id=4202,
        process_start_time=now,
        session_id=renewed_session,
        identity=renewed_identity,
        public_key_pem=renewed_key.public_pem,
        key_id=renewed_key.thumbprint,
        issued_at=now,
        expires_at=now + 900,
        possession_signature=renewed_key.sign("agentnet.workload.renewal.pop.v1", renewal),
    )
    assert renewed.credential_epoch == 2
    with pytest.raises(AuthenticationError):
        registry.resolve(
            transport=transport,
            registration_id=registration_id,
            process_id=4201,
            process_start_time=now - 10,
            session_id=session_id,
        )
    with pytest.raises(ConflictError, match="state changed"):
        registry.renew(
            authority=renewal_authority,
            command=renewal_command,
            registration_id=registration_id,
            expected_credential_epoch=1,
            process_id=4202,
            process_start_time=now,
            session_id=renewed_session,
            identity=renewed_identity,
            public_key_pem=renewed_key.public_pem,
            key_id=renewed_key.thumbprint,
            issued_at=now,
            expires_at=now + 900,
            possession_signature=renewed_key.sign("agentnet.workload.renewal.pop.v1", renewal),
        )

    revocation = registry.revocation_request(
        registration_id=registration_id,
        expected_credential_epoch=2,
        expected_revocation_epoch=domain_epoch,
        reason="retire worker",
    )
    revocation_authority, revocation_command = _authority(
        store,
        actor=administrator,
        actor_key=administrator_key,
        action="identity.workload.revoke",
        resource=f"workload:{registration_id}",
        mutation=revocation,
        entity_revision=2,
        reason="retire worker",
    )
    result = registry.revoke(
        authority=revocation_authority,
        command=revocation_command,
        registration_id=registration_id,
        expected_credential_epoch=2,
        expected_revocation_epoch=domain_epoch,
        reason="retire worker",
    )
    assert result["status"] == "revoked"
    renewed_transport = registry.spiffe.transport_authority.bind_verified_peer(
        {
            "schema_version": "1.0",
            "spiffe_id": renewed_identity.spiffe_id,
            "trust_domain": renewed_identity.trust_domain,
            "workload_role": renewed_identity.workload_role,
            "certificate_serial": renewed_identity.certificate_serial,
            "process_id": 4202,
            "process_start_time": now,
            "session_id": renewed_session,
        }
    )
    with pytest.raises(AuthenticationError):
        registry.resolve(
            transport=renewed_transport,
            registration_id=registration_id,
            process_id=4202,
            process_start_time=now,
            session_id=renewed_session,
        )
    with pytest.raises(ConflictError, match="state changed"):
        registry.revoke(
            authority=revocation_authority,
            command=revocation_command,
            registration_id=registration_id,
            expected_credential_epoch=2,
            expected_revocation_epoch=domain_epoch,
            reason="retire worker",
        )
