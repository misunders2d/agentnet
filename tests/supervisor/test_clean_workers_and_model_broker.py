from __future__ import annotations

import asyncio
import json
from datetime import UTC, datetime
from pathlib import Path
from uuid import uuid4

import pytest

from agentnet.errors import AuthorizationError, GateBlocked, ReplayError
from agentnet.protocol.models import Classification
from agentnet.provenance import (
    OriginKind,
    OriginRegistration,
    ProvenanceObjectType,
    ProvenanceOrigin,
    ProvenanceService,
    SinkSet,
)
from agentnet.security.signatures import canonical_digest
from agentnet.supervisor.model_egress import ModelEgressBroker
from agentnet.supervisor.workers import CleanWorkerLauncher, WorkerSpec


def input_provenance(store, identity_factory, frames, *, model="local-test"):
    actor, _ = identity_factory()
    service = ProvenanceService(store)
    now = datetime.now(UTC).replace(microsecond=0)
    record = service.register_origin(
        OriginRegistration(
            object_type=ProvenanceObjectType.EVENT,
            object_id=f"model-prompt:{uuid4()}",
            domain_id=actor.domain_id,
            origin=ProvenanceOrigin(
                kind=OriginKind.INTERNAL_EVENT,
                source_id=f"model-prompt:{uuid4()}",
                source_digest=canonical_digest({"frames": frames}),
                harness_id=actor.harness_id,
                observed_at=now,
            ),
            classification=Classification.C1_INTERNAL,
            allowed_sinks=SinkSet(
                sinks=(f"model:{canonical_digest({'model': model})}",)
            ),
            policy_revision=1,
            recorded_at=now,
        ),
        when=now,
    )
    return service, record.reference()


def fresh_request_nonce() -> str:
    return f"model-broker-request-{uuid4().hex}"


def test_nonsemantic_worker_has_sanitized_environment_and_no_foreground_fallback(tmp_path: Path) -> None:
    launcher = CleanWorkerLauncher(evidence_dir=tmp_path)
    process = launcher.launch(
        WorkerSpec(
            harness_id="synthetic-harness",
            harness_kind="codex",
            executable="/usr/bin/env",
            arguments=(),
            semantic=False,
        )
    )
    stdout, _stderr = process.communicate(timeout=5)
    environment = dict(line.split("=", 1) for line in stdout.decode().splitlines() if "=" in line)
    assert set(environment) == {"HOME", "LANG", "LC_ALL", "PATH", "PYTHONNOUSERSITE", "NO_COLOR"}
    assert environment["HOME"].endswith("/home")


def test_semantic_worker_rejects_unsigned_or_untrusted_evidence(tmp_path: Path, monkeypatch) -> None:
    launcher = CleanWorkerLauncher(evidence_dir=tmp_path)
    evidence = {
        "schema": "agentnet.clean-worker-evidence.v1",
        "harness_kind": "codex",
        "executable_sha256": "0" * 64,
        "bubblewrap_sha256": "0" * 64,
        "sandbox_profile": "networkless-no-ambient-secrets",
        "passed_gates": ["G03", "G05"],
        "tested_at": 1,
        "expires_at": 2,
        "key_id": "untrusted",
        "signature": "invalid",
    }
    (tmp_path / "codex-clean-worker.json").write_text(json.dumps(evidence), encoding="utf-8")
    original_which = __import__("shutil").which
    monkeypatch.setattr(
        "agentnet.supervisor.workers.shutil.which",
        lambda name: "/usr/bin/true" if name in {"/usr/bin/true", "bwrap"} else original_which(name),
    )
    with pytest.raises(GateBlocked):
        launcher.launch(
            WorkerSpec(
                harness_id="synthetic-harness",
                harness_kind="codex",
                executable="/usr/bin/true",
                arguments=(),
                semantic=True,
            )
        )


def test_model_broker_binds_worker_grant_schema_budget_and_output_provenance(
    store, identity_factory
) -> None:
    calls: list[tuple[str, dict[str, object], str]] = []

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        calls.append((model, payload, secret))
        return {"output": "synthetic"}

    frames = [{"role": "user", "content": "synthetic"}]
    provenance, reference = input_provenance(store, identity_factory, frames)
    broker = ModelEgressBroker(
        allowed_models={"local-test"},
        upstream_secret="held-only-by-broker",
        transport=transport,
        provenance=provenance,
    )
    token = broker.issue(
        worker_id="worker-1",
        task_grant_id="grant-1",
        model="local-test",
        max_tokens=5,
        max_requests=1,
        input_provenance=reference,
    )
    with pytest.raises(AuthorizationError):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-2",
                task_grant_id="grant-1",
                prompt_frames=frames,
                max_output_tokens=1,
                request_nonce=fresh_request_nonce(),
            )
        )
    with pytest.raises(AuthorizationError, match="exact content"):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-1",
                task_grant_id="grant-1",
                prompt_frames=[{"role": "user", "content": "substituted prompt"}],
                max_output_tokens=1,
                request_nonce=fresh_request_nonce(),
            )
        )
    result = asyncio.run(
        broker.infer(
            token,
            worker_id="worker-1",
            task_grant_id="grant-1",
            prompt_frames=frames,
            max_output_tokens=5,
            request_nonce=fresh_request_nonce(),
        )
    )
    assert result["response"] == {"output": "synthetic"}
    output = provenance.get_by_digest(result["provenance"]["provenance_digest"])
    assert output.object_type is ProvenanceObjectType.MODEL_OUTPUT
    assert output.parent_digests.digests == (reference.provenance_digest,)
    assert output.content_digest == canonical_digest({"response": {"output": "synthetic"}})
    assert output.tainted is True
    assert output.reference().model_dump(mode="json") == result["provenance"]
    assert calls == [
        (
            "local-test",
            {"model": "local-test", "input": [{"role": "user", "content": "synthetic"}], "max_output_tokens": 5},
            "held-only-by-broker",
        )
    ]
    with pytest.raises(GateBlocked):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-1",
                task_grant_id="grant-1",
                prompt_frames=[{"role": "user", "content": "again"}],
                max_output_tokens=1,
                request_nonce=fresh_request_nonce(),
            )
        )


def test_model_broker_consumes_each_request_nonce_once(store, identity_factory) -> None:
    calls: list[tuple[str, dict[str, object], str]] = []

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        calls.append((model, payload, secret))
        return {"output": "synthetic"}

    frames = [{"role": "user", "content": "replay-fenced"}]
    provenance, reference = input_provenance(store, identity_factory, frames)
    broker = ModelEgressBroker(
        allowed_models={"local-test"},
        upstream_secret="broker-only-secret",
        transport=transport,
        provenance=provenance,
        replay_cache=store,
    )
    token = broker.issue(
        worker_id="worker-replay",
        task_grant_id="grant-replay",
        model="local-test",
        max_tokens=2,
        max_requests=2,
        input_provenance=reference,
    )
    request_nonce = "model-broker-request-nonce-000000000001"
    first = asyncio.run(
        broker.infer(
            token,
            worker_id="worker-replay",
            task_grant_id="grant-replay",
            prompt_frames=frames,
            max_output_tokens=1,
            request_nonce=request_nonce,
        )
    )
    assert first["response"] == {"output": "synthetic"}
    with pytest.raises(ReplayError, match="already consumed"):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-replay",
                task_grant_id="grant-replay",
                prompt_frames=frames,
                max_output_tokens=1,
                request_nonce=request_nonce,
            )
        )
    assert len(calls) == 1
    second = asyncio.run(
        broker.infer(
            token,
            worker_id="worker-replay",
            task_grant_id="grant-replay",
            prompt_frames=frames,
            max_output_tokens=1,
            request_nonce=fresh_request_nonce(),
        )
    )
    assert second["response"] == {"output": "synthetic"}
    assert len(calls) == 2


def test_model_broker_requires_well_formed_request_nonce(store, identity_factory) -> None:
    calls: list[dict[str, object]] = []

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        calls.append(payload)
        return {"output": "synthetic"}

    frames = [{"role": "user", "content": "nonce-required"}]
    provenance, reference = input_provenance(store, identity_factory, frames)
    broker = ModelEgressBroker(
        allowed_models={"local-test"},
        upstream_secret="broker-only-secret",
        transport=transport,
        provenance=provenance,
    )
    token = broker.issue(
        worker_id="worker-nonce",
        task_grant_id="grant-nonce",
        model="local-test",
        max_tokens=1,
        max_requests=1,
        input_provenance=reference,
    )
    for invalid_nonce in (None, "", "short", "a" * 31, " " * 32, "a" * 257, object()):
        with pytest.raises(AuthorizationError, match="request nonce"):
            asyncio.run(
                broker.infer(
                    token,
                    worker_id="worker-nonce",
                    task_grant_id="grant-nonce",
                    prompt_frames=frames,
                    max_output_tokens=1,
                    request_nonce=invalid_nonce,  # type: ignore[arg-type]
                )
            )
    assert calls == []
    result = asyncio.run(
        broker.infer(
            token,
            worker_id="worker-nonce",
            task_grant_id="grant-nonce",
            prompt_frames=frames,
            max_output_tokens=1,
            request_nonce=fresh_request_nonce(),
        )
    )
    assert result["response"] == {"output": "synthetic"}
    assert len(calls) == 1


def test_model_broker_requires_replay_cache(store, identity_factory) -> None:
    frames = [{"role": "user", "content": "replay-cache-required"}]
    provenance, _reference = input_provenance(store, identity_factory, frames)
    provenance.store = object()  # type: ignore[assignment]

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        raise AssertionError("invalid broker configuration must not reach transport")

    with pytest.raises(AuthorizationError, match="configuration"):
        ModelEgressBroker(
            allowed_models={"local-test"},
            upstream_secret="broker-only-secret",
            transport=transport,
            provenance=provenance,
        )


def test_model_broker_concurrent_duplicate_nonce_calls_transport_once(store, identity_factory) -> None:
    calls: list[dict[str, object]] = []

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        calls.append(payload)
        await asyncio.sleep(0)
        return {"output": "synthetic"}

    frames = [{"role": "user", "content": "concurrent-replay"}]
    provenance, reference = input_provenance(store, identity_factory, frames)
    broker = ModelEgressBroker(
        allowed_models={"local-test"},
        upstream_secret="broker-only-secret",
        transport=transport,
        provenance=provenance,
    )
    token = broker.issue(
        worker_id="worker-race",
        task_grant_id="grant-race",
        model="local-test",
        max_tokens=2,
        max_requests=2,
        input_provenance=reference,
    )
    nonce = fresh_request_nonce()

    async def race() -> list[object]:
        return await asyncio.gather(
            broker.infer(
                token,
                worker_id="worker-race",
                task_grant_id="grant-race",
                prompt_frames=frames,
                max_output_tokens=1,
                request_nonce=nonce,
            ),
            broker.infer(
                token,
                worker_id="worker-race",
                task_grant_id="grant-race",
                prompt_frames=frames,
                max_output_tokens=1,
                request_nonce=nonce,
            ),
            return_exceptions=True,
        )

    outcomes = asyncio.run(race())
    assert sum(isinstance(outcome, ReplayError) for outcome in outcomes) == 1
    assert sum(isinstance(outcome, dict) for outcome in outcomes) == 1
    assert len(calls) == 1


def test_model_broker_scopes_request_nonce_to_capability(store, identity_factory) -> None:
    calls: list[dict[str, object]] = []

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        calls.append(payload)
        return {"output": "synthetic"}

    frames = [{"role": "user", "content": "capability-scoped"}]
    provenance, reference = input_provenance(store, identity_factory, frames)
    broker = ModelEgressBroker(
        allowed_models={"local-test"},
        upstream_secret="broker-only-secret",
        transport=transport,
        provenance=provenance,
    )
    tokens = [
        broker.issue(
            worker_id="worker-scope",
            task_grant_id="grant-scope",
            model="local-test",
            max_tokens=1,
            max_requests=1,
            input_provenance=reference,
        )
        for _ in range(2)
    ]
    nonce = fresh_request_nonce()
    for token in tokens:
        result = asyncio.run(
            broker.infer(
                token,
                worker_id="worker-scope",
                task_grant_id="grant-scope",
                prompt_frames=frames,
                max_output_tokens=1,
                request_nonce=nonce,
            )
        )
        assert result["response"] == {"output": "synthetic"}
    assert len(calls) == 2


def test_model_broker_rejects_unbounded_lifetime_malformed_frames_and_cross_grant_use(
    store, identity_factory
) -> None:
    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        raise AssertionError("rejected requests must not reach inference transport")

    frames = [{"role": "user", "content": "bound prompt"}]
    provenance, reference = input_provenance(
        store,
        identity_factory,
        frames,
        model="exact-owner-model",
    )
    broker = ModelEgressBroker(
        allowed_models={"exact-owner-model"},
        upstream_secret="broker-only-secret",
        transport=transport,
        provenance=provenance,
    )
    for ttl in (0, -1, 3601, True):
        with pytest.raises(AuthorizationError):
            broker.issue(
                worker_id="worker-1",
                task_grant_id="grant-1",
                model="exact-owner-model",
                max_tokens=10,
                max_requests=1,
                input_provenance=reference,
                ttl_seconds=ttl,
            )
    token = broker.issue(
        worker_id="worker-1",
        task_grant_id="grant-1",
        model="exact-owner-model",
        max_tokens=10,
        max_requests=1,
        input_provenance=reference,
        ttl_seconds=60,
    )
    with pytest.raises(AuthorizationError):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-1",
                task_grant_id="grant-2",
                prompt_frames=[{"role": "user", "content": "cross grant"}],
                max_output_tokens=1,
                request_nonce=fresh_request_nonce(),
            )
        )
    for frames in (
        [],
        [{"role": "tool", "content": "smuggled"}],
        [{"role": "user", "content": "", "url": "https://proxy.invalid"}],
        [{"role": "user", "content": {"not": "text"}}],
    ):
        with pytest.raises(AuthorizationError, match="frame schema"):
            asyncio.run(
                broker.infer(
                    token,
                    worker_id="worker-1",
                    task_grant_id="grant-1",
                    prompt_frames=frames,
                    max_output_tokens=1,
                    request_nonce=fresh_request_nonce(),
                )
            )
    broker.revoke(token)
    with pytest.raises(AuthorizationError):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-1",
                task_grant_id="grant-1",
                prompt_frames=[{"role": "user", "content": "after revoke"}],
                max_output_tokens=1,
                request_nonce=fresh_request_nonce(),
            )
        )


def test_model_response_is_withheld_when_parent_lineage_disappears_during_transport(
    store, identity_factory
) -> None:
    frames = [{"role": "user", "content": "atomic model input"}]
    provenance, reference = input_provenance(store, identity_factory, frames)

    async def transport(model: str, payload: dict[str, object], secret: str) -> dict[str, object]:
        with store.transaction() as connection:
            connection.execute(
                "DELETE FROM content_provenance WHERE provenance_digest=?",
                (reference.provenance_digest,),
            )
        return {"output": "must not escape"}

    broker = ModelEgressBroker(
        allowed_models={"local-test"},
        upstream_secret="broker-only-secret",
        transport=transport,
        provenance=provenance,
    )
    token = broker.issue(
        worker_id="worker-atomic",
        task_grant_id="grant-atomic",
        model="local-test",
        max_tokens=4,
        max_requests=1,
        input_provenance=reference,
    )
    request_nonce = fresh_request_nonce()
    with pytest.raises(AuthorizationError, match="parent is unavailable"):
        asyncio.run(
            broker.infer(
                token,
                worker_id="worker-atomic",
                task_grant_id="grant-atomic",
                prompt_frames=frames,
                max_output_tokens=4,
                request_nonce=request_nonce,
            )
        )
    assert store.fetch_one("SELECT COUNT(*) AS total FROM replay_nonces")["total"] == 1
    assert store.fetch_one(
        "SELECT COUNT(*) AS total FROM content_provenance WHERE object_type='model_output'"
    )["total"] == 0
