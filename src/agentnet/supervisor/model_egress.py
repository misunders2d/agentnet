"""Credential-holding, non-generic model egress broker."""

from __future__ import annotations

import secrets
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any
from uuid import uuid4

from agentnet.errors import AuthorizationError, GateBlocked, ValidationError
from agentnet.provenance import (
    ProvenanceObjectType,
    ProvenanceReferenceV1,
    ProvenanceService,
    TransformationKind,
    TransformationStep,
)
from agentnet.security.signatures import canonical_digest, canonical_json


@dataclass(slots=True)
class BrokerCapability:
    token: str
    worker_id: str
    task_grant_id: str
    model: str
    remaining_tokens: int
    remaining_requests: int
    expires_at: int
    input_provenance: ProvenanceReferenceV1
    revoked: bool = False


InferenceTransport = Callable[[str, dict[str, Any], str], Awaitable[dict[str, Any]]]


class ModelEgressBroker:
    """Binds inference only; it exposes no URL, connector, or arbitrary headers."""

    def __init__(
        self,
        *,
        allowed_models: set[str],
        upstream_secret: str,
        transport: InferenceTransport,
        provenance: ProvenanceService,
    ) -> None:
        if (
            not allowed_models
            or any(not isinstance(model, str) or not 1 <= len(model) <= 256 for model in allowed_models)
            or not isinstance(upstream_secret, str)
            or not upstream_secret
            or not isinstance(provenance, ProvenanceService)
        ):
            raise AuthorizationError("model broker configuration is invalid")
        self.allowed_models = frozenset(allowed_models)
        self._upstream_secret = upstream_secret
        self._transport = transport
        self.provenance = provenance
        self._capabilities: dict[str, BrokerCapability] = {}

    def _require_input_provenance(
        self,
        reference: ProvenanceReferenceV1,
        *,
        prompt_digest: str | None = None,
        required_sink: str,
    ):
        if not isinstance(reference, ProvenanceReferenceV1):
            raise AuthorizationError("model input requires an exact provenance reference")
        record = self.provenance.get_by_digest(reference.provenance_digest)
        expected_digest = record.content_digest if prompt_digest is None else prompt_digest
        return self.provenance.require_reference(
            reference,
            expected_domain_id=record.domain_id,
            expected_content_digest=expected_digest,
            expected_object_type=record.object_type,
            expected_classification=record.classification,
            required_sinks=(required_sink,),
            expected_policy_revision=record.policy_revision,
        )

    @staticmethod
    def _model_sink(model: str) -> str:
        return f"model:{canonical_digest({'model': model})}"

    def issue(
        self,
        *,
        worker_id: str,
        task_grant_id: str,
        model: str,
        max_tokens: int,
        max_requests: int,
        input_provenance: ProvenanceReferenceV1,
        ttl_seconds: int = 300,
    ) -> str:
        if (
            not isinstance(worker_id, str)
            or not 1 <= len(worker_id) <= 256
            or not isinstance(task_grant_id, str)
            or not 1 <= len(task_grant_id) <= 256
            or model not in self.allowed_models
            or type(max_tokens) is not int
            or not 1 <= max_tokens <= 10_000_000
            or type(max_requests) is not int
            or not 1 <= max_requests <= 10_000
            or type(ttl_seconds) is not int
            or not 1 <= ttl_seconds <= 3_600
        ):
            raise AuthorizationError("model or broker budget is not allowed")
        self._require_input_provenance(
            input_provenance,
            required_sink=self._model_sink(model),
        )
        token = secrets.token_urlsafe(32)
        self._capabilities[token] = BrokerCapability(
            token=token,
            worker_id=worker_id,
            task_grant_id=task_grant_id,
            model=model,
            remaining_tokens=max_tokens,
            remaining_requests=max_requests,
            expires_at=int(time.time()) + ttl_seconds,
            input_provenance=input_provenance,
        )
        return token

    async def infer(
        self,
        token: str,
        *,
        worker_id: str,
        task_grant_id: str,
        prompt_frames: list[dict[str, str]],
        max_output_tokens: int,
    ) -> dict[str, Any]:
        capability = self._capabilities.get(token)
        if (
            capability is None
            or capability.revoked
            or capability.expires_at <= int(time.time())
            or capability.worker_id != worker_id
            or capability.task_grant_id != task_grant_id
        ):
            raise AuthorizationError("model broker capability is invalid")
        if (
            type(max_output_tokens) is not int
            or max_output_tokens <= 0
            or max_output_tokens > capability.remaining_tokens
            or capability.remaining_requests <= 0
        ):
            raise GateBlocked("budget_hold", "model egress budget exhausted")
        allowed_roles = {"system", "user", "assistant"}
        if (
            not isinstance(prompt_frames, list)
            or not 1 <= len(prompt_frames) <= 256
            or any(
                not isinstance(frame, dict)
                or set(frame) != {"role", "content"}
                or frame["role"] not in allowed_roles
                or not isinstance(frame["content"], str)
                or not frame["content"]
                or len(frame["content"].encode("utf-8")) > 1_048_576
                for frame in prompt_frames
            )
            or sum(len(frame["content"].encode("utf-8")) for frame in prompt_frames) > 2_097_152
        ):
            raise AuthorizationError("inference frame schema rejected")
        prompt_digest = canonical_digest({"frames": prompt_frames})
        parent = self._require_input_provenance(
            capability.input_provenance,
            prompt_digest=prompt_digest,
            required_sink=self._model_sink(capability.model),
        )
        capability.remaining_tokens -= max_output_tokens
        capability.remaining_requests -= 1
        payload = {"model": capability.model, "input": prompt_frames, "max_output_tokens": max_output_tokens}
        started_at = datetime.now(UTC).replace(microsecond=0)
        response = await self._transport(capability.model, payload, self._upstream_secret)
        completed_at = datetime.now(UTC).replace(microsecond=0)
        if not isinstance(response, dict):
            raise ValidationError("model transport response does not match the bounded JSON object contract")
        encoded_response = canonical_json(response)
        if len(encoded_response) > 2_097_152:
            raise ValidationError("model transport response exceeds the provenance boundary")
        output_digest = canonical_digest({"response": response})
        output_id = f"model-output:{uuid4()}"
        step = TransformationStep(
            kind=TransformationKind.MODEL,
            operation_id=f"model-inference:{uuid4()}",
            implementation_id=f"model:{canonical_digest({'model': capability.model})}",
            implementation_version=f"sha256:{canonical_digest({'model': capability.model})}",
            executor_harness_id=capability.worker_id,
            input_digests=(parent.content_digest,),
            output_digest=output_digest,
            started_at=started_at,
            completed_at=completed_at,
        )
        with self.provenance.store.transaction() as connection:
            output_provenance = self.provenance.record_tainted_derivation_in_transaction(
                connection,
                object_type=ProvenanceObjectType.MODEL_OUTPUT,
                object_id=output_id,
                domain_id=parent.domain_id,
                expected_previous_version=0,
                parent_provenance_digests=(parent.provenance_digest,),
                transformations=(step,),
                output_digest=output_digest,
                classification=parent.classification,
                allowed_sinks=parent.allowed_sinks.sinks,
                policy_revision=parent.policy_revision,
                recorded_at=completed_at,
                when=completed_at,
            )
        return {
            "response": response,
            "provenance": output_provenance.reference().model_dump(mode="json"),
        }

    def revoke(self, token: str) -> None:
        if token in self._capabilities:
            self._capabilities[token].revoked = True
