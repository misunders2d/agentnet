from __future__ import annotations

import hashlib
import time
from typing import Any
from uuid import uuid4

import pytest

from agentnet.approval.service import (
    IndependentApprovalVerifier,
    TrustedApprover,
    create_independent_approval_receipt,
)
from agentnet.errors import AuthenticationError, AuthorizationError, ConflictError
from agentnet.identity.context import ExpiredCredentialContextResolver
from agentnet.identity.credentials import (
    LAPTOP_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
    LAPTOP_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
    LaptopCredentialReauthorizationCoordinator,
    LaptopCredentialReauthorizationPendingResult,
    LaptopCredentialReauthorizationPrepareRequest,
    LaptopCredentialReauthorizationProgressRequest,
    LaptopCredentialReauthorizationRequest,
    LaptopCredentialReauthorizationResult,
    LaptopCredentialReauthorizationService,
)
from agentnet.security.dpop import create_request_proof
from agentnet.security.signatures import P256KeyPair, canonical_json


class FakeApprovalClient:
    def __init__(self) -> None:
        self.state = "pending"
        self.request_id = "approval-request-1"
        self.receipt: dict[str, Any] | None = None
        self.created: list[dict[str, Any]] = []
        self.status_checks = 0
        self.retrievals = 0

    def create_request(self, **values: Any) -> dict[str, Any]:
        self.created.append(values)
        return {
            "schema": "agentnet.approval.internal-request-created.v1",
            "request_id": self.request_id,
            "state": self.state,
            "approval_purpose": values["approval_purpose"],
            "transaction_digest": values["transaction_digest"],
            "expires_at": values["request_expires_at"],
            "duplicate": len(self.created) > 1,
        }

    def request_status(self, *, request_id: str, transaction_digest: str) -> dict[str, Any]:
        self.status_checks += 1
        return {
            "schema": "agentnet.approval.internal-request-status-result.v1",
            "request_id": request_id,
            "state": self.state,
            "transaction_digest": transaction_digest,
            "expires_at": self.created[-1]["request_expires_at"],
        }

    def retrieve_receipt(self, **values: Any) -> dict[str, Any]:
        self.retrievals += 1
        assert values["request_id"] == self.request_id
        assert self.receipt is not None
        return self.receipt


def _approval(
    *, actor: Any, transaction: LaptopCredentialReauthorizationRequest, now: int
) -> tuple[IndependentApprovalVerifier, dict[str, Any]]:
    signer = P256KeyPair.generate()
    approver = TrustedApprover(
        principal_id=actor.principal_id,
        domain_id=actor.domain_id,
        signer_key_id=signer.thumbprint,
        public_key_pem=signer.public_pem,
        allowed_purposes=frozenset({LAPTOP_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE}),
    )
    verifier = IndependentApprovalVerifier(
        {signer.thumbprint: approver}, verifier_id="laptop-reauthorization.example"
    )
    receipt = create_independent_approval_receipt(
        signer,
        approver=approver,
        verifier_id=verifier.verifier_id,
        approval_purpose=LAPTOP_CREDENTIAL_REAUTHORIZATION_APPROVAL_PURPOSE,
        canonical_transaction=transaction.canonical_transaction,
        issued_at=now,
        expires_at=now + 300,
    )
    return verifier, receipt


def _expired(store: Any, credential_id: str, *, now: int) -> int:
    expired_at = now - 1
    with store.transaction() as connection:
        connection.execute(
            "UPDATE credentials SET expires_at=? WHERE credential_id=?",
            (expired_at, credential_id),
        )
    return expired_at


def _progress(
    transaction: LaptopCredentialReauthorizationRequest,
    key: P256KeyPair,
    *,
    possession_secret: str = "p" * 43,
    signer: P256KeyPair | None = None,
) -> LaptopCredentialReauthorizationProgressRequest:
    return LaptopCredentialReauthorizationProgressRequest(
        transaction=transaction,
        old_key_possession_signature=(signer or key).sign(
            LAPTOP_CREDENTIAL_REAUTHORIZATION_POP_PURPOSE,
            transaction.possession_fields(),
        ),
        possession_secret=possession_secret,
    )


def _resolver(store: Any) -> ExpiredCredentialContextResolver:
    return ExpiredCredentialContextResolver(
        store,
        service_audience="agentnet-core",
        service_scheme="https",
        service_authority="core.example",
    )


def _proof(actor: Any, key: P256KeyPair, body: bytes, *, path: str, now: int):
    return create_request_proof(
        key,
        harness_id=actor.harness_id,
        credential_id=actor.credential_id,
        domain_id=actor.domain_id,
        audience="agentnet-core",
        method="POST",
        scheme="https",
        authority="core.example",
        path=path,
        query="",
        body=body,
        timestamp=now,
    )


def test_expired_dpop_boundary_binds_key_body_path_and_never_mints_actor(
    store: Any, identity_factory: Any
) -> None:
    actor, key = identity_factory(binding_assurance="os_bound")
    now = int(time.time())
    _expired(store, actor.credential_id, now=now)
    body = canonical_json(
        {
            "schema": "agentnet.laptop-credential-reauthorization-prepare.v1",
            "request_id": str(uuid4()),
            "identity_profile_sha256": "a" * 64,
        }
    )
    path = "/v1/credentials/current/reauthorize-expired/prepare"
    resolver = _resolver(store)

    with pytest.raises(AuthenticationError, match="body digest"):
        resolver.resolve(
            _proof(actor, key, body, path=path, now=now),
            expected_method="POST", expected_scheme="https", expected_authority="core.example",
            expected_path=path, expected_query="", body=body + b" ", now=now,
            allow_retired_predecessor=False,
        )
    with pytest.raises(AuthenticationError, match="target mismatch"):
        resolver.resolve(
            _proof(actor, key, body, path=path, now=now),
            expected_method="POST", expected_scheme="https", expected_authority="core.example",
            expected_path=path + "/wrong", expected_query="", body=body, now=now,
            allow_retired_predecessor=False,
        )
    with pytest.raises(AuthenticationError, match="binding mismatch"):
        resolver.resolve(
            _proof(actor, P256KeyPair.generate(), body, path=path, now=now),
            expected_method="POST", expected_scheme="https", expected_authority="core.example",
            expected_path=path, expected_query="", body=body, now=now,
            allow_retired_predecessor=False,
        )

    context = resolver.resolve(
        _proof(actor, key, body, path=path, now=now),
        expected_method="POST", expected_scheme="https", expected_authority="core.example",
        expected_path=path, expected_query="", body=body, now=now,
        allow_retired_predecessor=False,
    )
    assert context.binding.credential_id == actor.credential_id
    assert not hasattr(context, "actor")


def test_expired_boundary_rejects_unexpired_revoked_and_unbound_assurance(
    store: Any, identity_factory: Any
) -> None:
    now = int(time.time())
    body = b"{}"
    path = "/v1/credentials/current/reauthorize-expired/prepare"
    resolver = _resolver(store)

    active, active_key = identity_factory(binding_assurance="os_bound")
    with pytest.raises(AuthenticationError, match="not expired"):
        resolver.resolve(
            _proof(active, active_key, body, path=path, now=now),
            expected_method="POST", expected_scheme="https", expected_authority="core.example",
            expected_path=path, expected_query="", body=body, now=now,
            allow_retired_predecessor=False,
        )

    revoked, revoked_key = identity_factory(binding_assurance="os_bound")
    _expired(store, revoked.credential_id, now=now)
    with store.transaction() as connection:
        connection.execute("UPDATE credentials SET status='revoked' WHERE credential_id=?", (revoked.credential_id,))
    with pytest.raises(AuthenticationError, match="not eligible"):
        resolver.resolve(
            _proof(revoked, revoked_key, body, path=path, now=now),
            expected_method="POST", expected_scheme="https", expected_authority="core.example",
            expected_path=path, expected_query="", body=body, now=now,
            allow_retired_predecessor=True,
        )

    lab, lab_key = identity_factory(binding_assurance="lab")
    _expired(store, lab.credential_id, now=now)
    with pytest.raises(AuthenticationError, match="assurance"):
        resolver.resolve(
            _proof(lab, lab_key, body, path=path, now=now),
            expected_method="POST", expected_scheme="https", expected_authority="core.example",
            expected_path=path, expected_query="", body=body, now=now,
            allow_retired_predecessor=False,
        )


def test_valid_flow_preserves_binding_authority_memberships_and_is_idempotent(
    store: Any, identity_factory: Any
) -> None:
    actor, key = identity_factory(binding_assurance="hardware_bound")
    now = int(time.time())
    expired_at = _expired(store, actor.credential_id, now=now)
    with store.transaction() as connection:
        connection.execute(
            "INSERT INTO entitlements(entitlement_id,domain_id,principal_id,action,resource_pattern,expires_at,revoked_at,revision) VALUES(?,?,?,?,?,?,NULL,1)",
            ("entitlement-1", actor.domain_id, actor.principal_id, "message.send", "scope:*", now + 600),
        )
        connection.execute(
            "INSERT INTO rooms(room_id,domain_id,owner_domain_id,owner_epoch,control_sequence,state,classification,history_mode,policy_json) VALUES(?,?,?,1,1,'active','C1','joined','{}')",
            ("room-1", actor.domain_id, actor.domain_id),
        )
        connection.execute(
            "INSERT INTO room_members(room_id,harness_id,role,joined_sequence) VALUES(?,?,?,1)",
            ("room-1", actor.harness_id, "member"),
        )
        connection.execute(
            "INSERT INTO conversations(conversation_id,domain_id,created_by_authority_id,classification,state,created_at,updated_at) VALUES(?,?,?,'C1','active',?,?)",
            ("conversation-1", actor.domain_id, actor.principal_id, now, now),
        )
        connection.execute(
            "INSERT INTO conversation_members(conversation_id,authority_id,harness_id,role,status,joined_at) VALUES(?,?,?,'owner','active',?)",
            ("conversation-1", actor.principal_id, actor.harness_id, now),
        )

    def preserved() -> dict[str, dict[str, Any]]:
        return {
            "domain": dict(store.fetch_one("SELECT * FROM domains WHERE domain_id=?", (actor.domain_id,))),
            "principal": dict(store.fetch_one("SELECT * FROM principals WHERE principal_id=?", (actor.principal_id,))),
            "harness": dict(store.fetch_one("SELECT * FROM harnesses WHERE harness_id=?", (actor.harness_id,))),
            "entitlement": dict(store.fetch_one("SELECT * FROM entitlements WHERE entitlement_id='entitlement-1'")),
            "room_member": dict(store.fetch_one("SELECT * FROM room_members WHERE room_id='room-1'")),
            "conversation_member": dict(store.fetch_one("SELECT * FROM conversation_members WHERE conversation_id='conversation-1'")),
        }

    preserved_before = preserved()
    bootstrap = LaptopCredentialReauthorizationRequest(
        request_id=str(uuid4()), domain_id=actor.domain_id, principal_id=actor.principal_id,
        harness_id=actor.harness_id, expired_credential_id=actor.credential_id,
        expected_credential_epoch=1, successor_credential_epoch=2,
        expected_expired_at=expired_at, expected_key_id=key.thumbprint,
        expected_public_key_sha256=hashlib.sha256(key.public_pem.encode()).hexdigest(),
        expected_binding_assurance="hardware_bound", identity_profile_sha256="a" * 64,
        prepared_at=now, expires_at=now + 300,
        maximum_new_credential_ttl_seconds=3_600,
    )
    verifier, receipt = _approval(actor=actor, transaction=bootstrap, now=now)
    approval_client = FakeApprovalClient()
    approval_client.receipt = receipt
    clock = [now]
    service = LaptopCredentialReauthorizationService(
        store, verifier, credential_ttl_seconds=3_600, approval_ttl_seconds=300, clock=lambda: clock[0]
    )
    coordinator = LaptopCredentialReauthorizationCoordinator(
        service, approval_client, public_approval_url="https://approval.example/approval"
    )
    prepared = coordinator.prepare(
        presented_credential_id=actor.credential_id,
        request=LaptopCredentialReauthorizationPrepareRequest(
            request_id=bootstrap.request_id, identity_profile_sha256="a" * 64
        ),
    )
    assert prepared == bootstrap
    progress = _progress(prepared, key)
    pending = coordinator.progress(presented_credential_id=actor.credential_id, request=progress)
    assert isinstance(pending, LaptopCredentialReauthorizationPendingResult)
    assert pending.status == "approval_pending"

    approval_client.state = "issued"
    completed = coordinator.progress(presented_credential_id=actor.credential_id, request=progress)
    assert isinstance(completed, LaptopCredentialReauthorizationResult)
    assert completed.status == "current"
    assert completed.credential_epoch == 2
    assert completed.key_id == key.thumbprint
    assert completed.key_preserved is True
    assert completed.authority_granted is False
    assert completed.idempotent_repeat is False
    old = dict(store.fetch_one(
        "SELECT status,epoch,key_id,public_key_pem,expires_at FROM credentials WHERE credential_id=?",
        (actor.credential_id,),
    ))
    successor = dict(store.fetch_one(
        "SELECT status,epoch,key_id,public_key_pem FROM credentials WHERE credential_id=?",
        (completed.credential_id,),
    ))
    assert old == {"status": "retired", "epoch": 1, "key_id": key.thumbprint, "public_key_pem": key.public_pem, "expires_at": expired_at}
    assert successor == {"status": "active", "epoch": 2, "key_id": key.thumbprint, "public_key_pem": key.public_pem}

    preserved_after = preserved()
    expected_harness = dict(preserved_before["harness"])
    expected_harness["credential_epoch"] = 2
    assert preserved_after == {**preserved_before, "harness": expected_harness}

    clock[0] = bootstrap.expires_at + 1
    repeated = coordinator.progress(presented_credential_id=actor.credential_id, request=progress)
    assert isinstance(repeated, LaptopCredentialReauthorizationResult)
    assert repeated.credential_id == completed.credential_id
    assert repeated.idempotent_repeat is True
    assert approval_client.retrievals == 1
    assert store.fetch_one("SELECT COUNT(*) AS n FROM credentials WHERE harness_id=?", (actor.harness_id,))["n"] == 2


def test_atomic_service_rejects_epoch_assurance_key_and_unrelated_retired_credential(
    store: Any, identity_factory: Any
) -> None:
    actor, key = identity_factory(binding_assurance="os_bound")
    unrelated, unrelated_key = identity_factory(binding_assurance="os_bound")
    now = int(time.time())
    expired_at = _expired(store, actor.credential_id, now=now)
    _expired(store, unrelated.credential_id, now=now)
    transaction = LaptopCredentialReauthorizationRequest(
        request_id=str(uuid4()), domain_id=actor.domain_id, principal_id=actor.principal_id,
        harness_id=actor.harness_id, expired_credential_id=actor.credential_id,
        expected_credential_epoch=1, successor_credential_epoch=2,
        expected_expired_at=expired_at, expected_key_id=key.thumbprint,
        expected_public_key_sha256=hashlib.sha256(key.public_pem.encode()).hexdigest(),
        expected_binding_assurance="os_bound", identity_profile_sha256="b" * 64,
        prepared_at=now, expires_at=now + 300, maximum_new_credential_ttl_seconds=3_600,
    )
    verifier, receipt = _approval(actor=actor, transaction=transaction, now=now)
    service = LaptopCredentialReauthorizationService(
        store, verifier, credential_ttl_seconds=3_600, approval_ttl_seconds=300, clock=lambda: now
    )
    with pytest.raises(AuthenticationError, match="presented credential"):
        service.reauthorize(
            presented_credential_id=unrelated.credential_id,
            request=_progress(transaction, key), approval=receipt,
        )
    with pytest.raises(AuthenticationError, match="signature verification failed"):
        service.reauthorize(
            presented_credential_id=actor.credential_id,
            request=_progress(transaction, key, signer=unrelated_key), approval=receipt,
        )

    wrong_epoch = transaction.model_copy(update={"expected_credential_epoch": 2, "successor_credential_epoch": 3})
    wrong_epoch_verifier, wrong_epoch_receipt = _approval(actor=actor, transaction=wrong_epoch, now=now)
    service.approval_verifier = wrong_epoch_verifier
    with pytest.raises(ConflictError, match="expired binding changed"):
        service.reauthorize(
            presented_credential_id=actor.credential_id,
            request=_progress(wrong_epoch, key), approval=wrong_epoch_receipt,
        )

    wrong_assurance = transaction.model_copy(update={"expected_binding_assurance": "hardware_bound"})
    wrong_assurance_verifier, wrong_assurance_receipt = _approval(actor=actor, transaction=wrong_assurance, now=now)
    service.approval_verifier = wrong_assurance_verifier
    with pytest.raises(ConflictError, match="expired binding changed"):
        service.reauthorize(
            presented_credential_id=actor.credential_id,
            request=_progress(wrong_assurance, key), approval=wrong_assurance_receipt,
        )

    with store.transaction() as connection:
        connection.execute("UPDATE credentials SET status='revoked' WHERE credential_id=?", (actor.credential_id,))
    service.approval_verifier = verifier
    with pytest.raises(AuthorizationError, match="not eligible"):
        service.reauthorize(
            presented_credential_id=actor.credential_id,
            request=_progress(transaction, key), approval=receipt,
        )
