"""CLI commands for messaging, response obligations, artifacts, and authority."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.parse import urlencode
from uuid import uuid4

import httpx

from agentnet.client import MAX_ARTIFACT_BYTES
from agentnet.errors import ValidationError
from agentnet.identity.actors import VerifiedActor
from agentnet.organization.relationships import Relationship, RelationshipGovernanceRecord
from agentnet.cli import helpers


def command_authority_inventory(args: argparse.Namespace) -> int:
    """Show authority derived only from the current signed transport identity."""

    return helpers._identity_client_json_call(
        Path(args.identity),
        "GET",
        "/v1/authority",
        label="authority inventory",
    )


def command_authority_explain(args: argparse.Namespace) -> int:
    """Explain one denial visible to the current signed transport identity."""

    return helpers._identity_client_json_call(
        Path(args.identity),
        "GET",
        f"/v1/authority/denials/{args.decision_id}",
        label="denial explanation",
    )


def command_relationship_propose(args: argparse.Namespace) -> int:
    """Submit a zero-authority exact relationship proposal."""

    relationship_value = helpers._read_json_object(
        Path(args.relationship),
        label="relationship proposal terms",
    )
    try:
        relationship = Relationship.model_validate_json(
            json.dumps(relationship_value),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship proposal terms do not match the exact schema") from exc
    if args.proposal_expires_in < 60 or args.proposal_expires_in > 604_800:
        raise SystemExit("relationship proposal lifetime must be between one minute and seven days")
    proposal_expires_at = datetime.now(UTC).replace(microsecond=0) + timedelta(
        seconds=args.proposal_expires_in
    )
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/relationships",
            json_body={
                "relationship": relationship.model_dump(mode="json"),
                "proposal_expires_at": proposal_expires_at.isoformat(),
            },
        )
    finally:
        client.close()
    if response.status_code != 201:
        raise SystemExit(f"relationship proposal was rejected with HTTP {response.status_code}")
    value = response.json()
    if not isinstance(value, dict) or set(value) != {"proposal"}:
        raise SystemExit("relationship proposal response does not match the exact schema")
    try:
        record = RelationshipGovernanceRecord.model_validate_json(
            json.dumps(value["proposal"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship proposal response is invalid") from exc
    helpers._write_owner_json(Path(args.proposal), value, force=args.force)
    print(
        json.dumps(
            {
                "proposal": args.proposal,
                "relationship_id": record.relationship_id,
                "transaction_digest": record.transaction_digest,
                "lifecycle_state": record.lifecycle_state,
                "authority_active": False,
                "next": (
                    "the exact current subordinate human or guest owner independently approves "
                    "the canonical transaction, then runs agentnet relationship accept"
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_relationship_accept(args: argparse.Namespace) -> int:
    """Submit independent subordinate-owner consent for one exact proposal."""

    value = helpers._read_json_object(Path(args.proposal), label="relationship proposal")
    if set(value) != {"proposal"} or not isinstance(value["proposal"], dict):
        raise SystemExit("relationship proposal file does not match the exact schema")
    try:
        proposal = RelationshipGovernanceRecord.model_validate_json(
            json.dumps(value["proposal"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship proposal file is invalid") from exc
    if proposal.lifecycle_state != "proposed" or proposal.activation_basis is not None:
        raise SystemExit("relationship proposal file is not a zero-authority pending proposal")
    approval = helpers._read_json_object(Path(args.approval), label="independent relationship approval")
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            f"/v1/relationships/{proposal.relationship_id}/accept",
            json_body={
                "approval": approval,
                "expected_transaction_digest": proposal.transaction_digest,
                "expected_relationship_revision": proposal.revision,
                "expected_lifecycle_revision": proposal.lifecycle_revision,
            },
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"relationship acceptance was rejected with HTTP {response.status_code}")
    result = response.json()
    if not isinstance(result, dict) or set(result) != {"relationship"}:
        raise SystemExit("relationship acceptance response does not match the exact schema")
    try:
        active = RelationshipGovernanceRecord.model_validate_json(
            json.dumps(result["relationship"]),
            strict=True,
        )
    except Exception as exc:
        raise SystemExit("relationship acceptance response is invalid") from exc
    if active.lifecycle_state != "active":
        raise SystemExit("relationship acceptance did not activate the governance edge")
    print(
        json.dumps(
            {
                "relationship": active.model_dump(mode="json"),
                "authority_effect": "governance_edge_and_custody_only_assignment_scope",
                "data_access_granted": False,
                "semantic_processing_granted": False,
                "tools_granted": False,
                "business_effect_authority_granted": False,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def _artifact_json_response(
    response: httpx.Response,
    *,
    operation: str,
    statuses: frozenset[int],
) -> dict[str, object]:
    return helpers._validate_http_json_response(
        response,
        label=f"artifact {operation}",
        statuses=statuses,
    )


def command_artifact_upload(args: argparse.Namespace) -> int:
    if (
        not 1 <= len(args.origin) <= 256
        or any(ord(character) < 32 or ord(character) == 127 for character in args.origin)
    ):
        raise SystemExit("artifact origin must be 1-256 printable characters")
    _path, content = helpers._read_artifact_file(Path(args.path))
    digest = hashlib.sha256(content).hexdigest()
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        reserved = _artifact_json_response(
            client.reserve_artifact(
                idempotency_key=args.idempotency_key,
                expected_digest=digest,
                expected_size=len(content),
                media_type=args.media_type,
                classification=args.classification,
                required_attachment=not args.optional_attachment,
                ttl_seconds=args.ttl_seconds,
            ),
            operation="reservation",
            statuses=frozenset({200, 201}),
        )
        reservation_id = reserved.get("reservation_id")
        if not isinstance(reservation_id, str):
            raise SystemExit("artifact reservation response lacks an exact reservation_id")
        uploaded = _artifact_json_response(
            client.upload_artifact_bytes(
                reservation_id=reservation_id,
                content=content,
            ),
            operation="byte upload",
            statuses=frozenset({200}),
        )
        object_version = uploaded.get("version")
        if (
            not isinstance(object_version, str)
            or len(object_version) != 64
            or any(character not in "0123456789abcdef" for character in object_version)
        ):
            raise SystemExit("artifact byte upload response lacks an exact object version")
        promoted = _artifact_json_response(
            client.promote_artifact(
                reservation_id=reservation_id,
                object_version=object_version,
                provenance={"origin": args.origin},
            ),
            operation="manifest promotion",
            statuses=frozenset({200, 201}),
        )
    finally:
        client.close()
    artifact_id = promoted.get("artifact_id")
    state = promoted.get("state")
    if not isinstance(artifact_id, str) or not isinstance(state, str):
        raise SystemExit("artifact promotion did not return an exact artifact state")
    scanner_state = {
        "quarantined": "pending",
        "scan_passed": "passed",
        "released": "passed",
        "held": "held",
    }.get(state, "unknown")
    print(
        json.dumps(
            {
                "artifact_id": artifact_id,
                "classification": args.classification,
                "media_type": args.media_type,
                "plaintext_digest": digest,
                "provenance": promoted.get("provenance"),
                "released": state == "released",
                "required_attachment": not args.optional_attachment,
                "reservation_id": reservation_id,
                "scanner_state": scanner_state,
                "size": len(content),
                "state": state,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_artifact_abort(args: argparse.Namespace) -> int:
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        result = _artifact_json_response(
            client.abort_artifact_reservation(reservation_id=args.reservation_id),
            operation="reservation abort",
            statuses=frozenset({200}),
        )
    finally:
        client.close()
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_artifact_lifecycle(args: argparse.Namespace) -> int:
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        result = _artifact_json_response(
            client.artifact_lifecycle(artifact_id=args.artifact_id),
            operation="lifecycle read",
            statuses=frozenset({200}),
        )
    finally:
        client.close()
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


def command_artifact_download(args: argparse.Namespace) -> int:
    output, name, directory = helpers._prepare_artifact_output(Path(args.output))
    try:
        client, _actor, _key = helpers._load_identity_client(Path(args.identity))
        try:
            try:
                response = client.download_artifact(
                    artifact_id=args.artifact_id,
                    ttl_seconds=args.ttl_seconds,
                )
            except ValidationError as exc:
                raise SystemExit("artifact download response was invalid") from exc
        finally:
            client.close()
        if response.status_code != 200:
            raise SystemExit(
                f"artifact download was rejected with HTTP {response.status_code}"
            )
        content_type = response.headers.get("content-type", "").split(";", 1)[0].strip().casefold()
        if content_type != "application/octet-stream":
            raise SystemExit("artifact download returned an invalid content type")
        content = response.content
        if len(content) > MAX_ARTIFACT_BYTES:
            raise SystemExit("artifact download exceeds the 16 MiB limit")
        helpers._write_artifact_output(
            directory=directory,
            name=name,
            content=content,
        )
    finally:
        os.close(directory)
    print(
        json.dumps(
            {
                "artifact_id": args.artifact_id,
                "output": str(output),
                "plaintext_digest": hashlib.sha256(content).hexdigest(),
                "size": len(content),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


def command_message_send(args: argparse.Namespace) -> int:
    payload = helpers._read_json_object(Path(args.payload), label="message payload")
    idempotency_key = args.idempotency_key or f"agentnet-cli-{uuid4()}"
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.request(
            "POST",
            "/v1/messages",
            json_body={
                "collaboration_scope_id": args.collaboration_scope_id,
                "recipients": list(args.recipient),
                "payload": payload,
                "idempotency_key": idempotency_key,
                "classification": args.classification,
            },
        )
    finally:
        client.close()
    if response.status_code != 202:
        raise SystemExit(f"message send was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def _obligation_request(
    args: argparse.Namespace,
    method: str,
    path: str,
    *,
    json_body: dict[str, object] | None = None,
) -> int:
    return helpers._identity_client_json_call(
        Path(args.identity),
        method,
        path,
        label="response obligation call",
        json_body=json_body,
    )


def command_obligation_list(args: argparse.Namespace) -> int:
    if args.limit < 1 or args.limit > 1000:
        raise SystemExit("obligation limit is outside the supported range")
    query = f"?role={args.role}&limit={args.limit}"
    for state in args.state or ():
        query += f"&state={state}"
    return _obligation_request(args, "GET", f"/v1/response-obligations{query}")


def command_obligation_show(args: argparse.Namespace) -> int:
    return _obligation_request(
        args, "GET", f"/v1/response-obligations/{args.obligation_id}"
    )


def command_obligation_inbox(args: argparse.Namespace) -> int:
    return _obligation_request(args, "GET", "/v1/response-obligations/inbox")


def command_obligation_transition(args: argparse.Namespace) -> int:
    body: dict[str, object] = {"to_state": args.to_state, "reason": args.reason}
    if args.expected_revision is not None:
        body["expected_revision"] = args.expected_revision
    return _obligation_request(
        args,
        "POST",
        f"/v1/response-obligations/{args.obligation_id}/transition",
        json_body=body,
    )


def command_obligation_cancel(args: argparse.Namespace) -> int:
    body: dict[str, object] = {"reason_code": args.reason_code}
    if args.expected_revision is not None:
        body["expected_revision"] = args.expected_revision
    return _obligation_request(
        args,
        "POST",
        f"/v1/response-obligations/{args.obligation_id}/cancel",
        json_body=body,
    )


def command_obligation_reconcile(args: argparse.Namespace) -> int:
    if args.limit < 1 or args.limit > 1000:
        raise SystemExit("obligation reconcile limit is outside the supported range")
    return _obligation_request(
        args,
        "POST",
        "/v1/response-obligations/reconcile",
        json_body={"limit": args.limit},
    )


def command_message_inbox(args: argparse.Namespace) -> int:
    if args.after < 0 or args.limit < 1 or args.limit > 1000:
        raise SystemExit("mailbox cursor or limit is outside the supported range")
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        query = urlencode(
            {
                "collaboration_scope_id": args.collaboration_scope_id,
                "after": args.after,
                "limit": args.limit,
            }
        )
        response = client.request("GET", f"/v1/mailbox?{query}")
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(f"mailbox read was rejected with HTTP {response.status_code}")
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0


def command_message_acknowledge(args: argparse.Namespace) -> int:
    client, _actor, _key = helpers._load_identity_client(Path(args.identity))
    try:
        response = client.acknowledge_mailbox(
            collaboration_scope_id=args.collaboration_scope_id,
            event_id=args.event_id,
            envelope_digest=args.envelope_digest,
        )
    finally:
        client.close()
    if response.status_code != 200:
        raise SystemExit(
            f"mailbox acknowledgement was rejected with HTTP {response.status_code}"
        )
    print(json.dumps(response.json(), indent=2, sort_keys=True))
    return 0
