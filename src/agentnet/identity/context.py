"""Resolve verified request proofs into trusted transport contexts.

Caller-supplied identity claims are accepted only so boundary adapters can pass
their raw input through one API.  They are deliberately ignored; every actor
field comes from the persisted credential binding selected by the proof.
"""

from __future__ import annotations

import hashlib
import sqlite3
import time
from collections.abc import Mapping
from typing import Any

from agentnet.errors import AuthenticationError, ReplayError
from agentnet.identity.actors import ActorKind, TrustedTransportContext, VerifiedActor
from agentnet.identity.credentials import (
    CredentialBinding,
    load_credential_binding,
    load_credential_binding_from_connection,
)
from agentnet.security.dpop import RequestProof, verify_request_proof
from agentnet.storage.sqlite import SQLiteStore


class VerifiedContextResolver:
    def __init__(
        self,
        store: SQLiteStore,
        *,
        service_audience: str,
        service_scheme: str,
        service_authority: str,
        proof_max_age: int = 300,
        future_skew: int = 60,
        replay_retention: int = 900,
    ) -> None:
        if proof_max_age <= 0 or future_skew < 0 or replay_retention < proof_max_age + future_skew:
            raise ValueError("replay retention must cover the full proof acceptance window")
        self.store = store
        self.service_audience = service_audience
        self.service_scheme = service_scheme
        self.service_authority = service_authority
        self.proof_max_age = proof_max_age
        self.future_skew = future_skew
        self.replay_retention = replay_retention

    def resolve(
        self,
        proof: RequestProof,
        *,
        expected_method: str,
        expected_scheme: str,
        expected_authority: str,
        expected_path: str,
        expected_query: str,
        body: bytes,
        now: int | None = None,
        caller_claims: Mapping[str, Any] | None = None,
    ) -> TrustedTransportContext:
        """Verify proof, atomically recheck its binding, and consume its nonce."""

        del caller_claims  # Untrusted claims are never an identity input.
        current_time = int(time.time()) if now is None else now

        if expected_scheme != self.service_scheme or expected_authority != self.service_authority:
            raise AuthenticationError("request arrived on an unconfigured service origin")

        initial = load_credential_binding(self.store, proof.credential_id)
        self._require_proof_binding(proof, initial)
        initial.require_active(now=current_time)
        verify_request_proof(
            proof,
            public_key_pem=initial.public_key_pem,
            expected_method=expected_method,
            expected_audience=self.service_audience,
            expected_scheme=expected_scheme,
            expected_authority=expected_authority,
            expected_path=expected_path,
            expected_query=expected_query,
            body=body,
            now=current_time,
            max_age=self.proof_max_age,
            future_skew=self.future_skew,
        )

        nonce_hash = hashlib.sha256(proof.nonce.encode("utf-8")).hexdigest()
        replay_expires_at = max(
            proof.timestamp + self.proof_max_age + self.future_skew,
            current_time + self.replay_retention,
        )
        actor_scope = f"{proof.domain_id}:{proof.harness_id}:{proof.credential_id}"

        with self.store.transaction() as connection:
            current = load_credential_binding_from_connection(connection, proof.credential_id)
            self._require_proof_binding(proof, current)
            current.require_active(now=current_time)
            connection.execute("DELETE FROM replay_nonces WHERE expires_at < ?", (current_time,))
            try:
                connection.execute(
                    "INSERT INTO replay_nonces(actor_id,nonce_hash,expires_at) VALUES(?,?,?)",
                    (actor_scope, nonce_hash, replay_expires_at),
                )
            except sqlite3.IntegrityError as exc:
                raise ReplayError("request proof nonce was already consumed") from exc

        if current.guest_id is None:
            actor = VerifiedActor(
                kind=ActorKind.VERIFIED_HUMAN_HARNESS,
                domain_id=current.domain_id,
                principal_id=current.principal_id,
                harness_id=current.harness_id,
                credential_id=current.credential_id,
                credential_epoch=current.credential_epoch,
                binding_assurance=current.binding_assurance,
            )
        else:
            actor = VerifiedActor(
                kind=ActorKind.HOST_GUEST_HARNESS,
                domain_id=current.domain_id,
                guest_id=current.guest_id,
                harness_id=current.harness_id,
                credential_id=current.credential_id,
                credential_epoch=current.credential_epoch,
                binding_assurance=current.binding_assurance,
            )
        return TrustedTransportContext(
            actor=actor,
            audience=proof.audience,
            method=proof.method,
            scheme=proof.scheme,
            authority=proof.authority,
            path=proof.path,
            query=proof.query,
            body_digest=proof.body_digest,
            timestamp=proof.timestamp,
            nonce=proof.nonce,
            proof_id=proof.proof_id,
        )

    @staticmethod
    def _require_proof_binding(proof: RequestProof, binding: CredentialBinding) -> None:
        presented = (proof.domain_id, proof.harness_id, proof.credential_id, proof.key_id)
        expected = (binding.domain_id, binding.harness_id, binding.credential_id, binding.key_id)
        if presented != expected:
            raise AuthenticationError("request proof credential binding mismatch")
