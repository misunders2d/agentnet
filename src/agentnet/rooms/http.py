"""Authenticated room, meeting, membership, and transfer HTTP routes."""

from __future__ import annotations

import hashlib
from collections.abc import Awaitable, Callable
from datetime import datetime
from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from agentnet.core.app import CommunicationCore
from agentnet.errors import AuthorizationError
from agentnet.identity.actors import VerifiedActor
from agentnet.protocol.models import (
    Classification,
    ReleasedArtifactBinding,
)
from agentnet.rooms.governance import (
    RoomTransferSnapshot,
    SourceTransferProposal,
    TargetTransferAcceptance,
)
from agentnet.security.signatures import canonical_digest


BodyAndActor = Callable[
    [Request, CommunicationCore],
    Awaitable[tuple[bytes, VerifiedActor]],
]
DecodeBase64 = Callable[..., bytes]


class RoomCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    persistent: bool = True
    expires_at: datetime | None = None
    policy: dict[str, Any] | None = None


class MeetingCreateBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    expires_at: datetime
    policy: dict[str, Any] | None = None


class RoomMemberBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)
    role: str = Field(default="member", pattern=r"^(member|guest|moderator)$")
    mls_key_package_b64: str | None = Field(default=None, max_length=1_000_000)


class RoomMemberRemoveBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)
    harness_id: str = Field(min_length=1, max_length=256)


class RoomSendBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)
    recipients: tuple[str, ...] = Field(min_length=1, max_length=1000)
    payload: dict[str, Any]
    idempotency_key: str = Field(min_length=16, max_length=256)
    classification: Classification = Classification.C1_INTERNAL
    released_artifacts: tuple[ReleasedArtifactBinding, ...] = ()
    expected_control_sequence: int = Field(ge=1)
    conversation_id: str | None = None


class RoomDescribeBody(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    collaboration_scope_id: str = Field(min_length=1, max_length=256)


class TransferProposalBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    proposal: SourceTransferProposal
    snapshot: RoomTransferSnapshot
    signature: str = Field(min_length=1, max_length=2048)
    additional_signatures: dict[str, str] = Field(default_factory=dict)


class TransferAcceptanceBody(BaseModel):
    model_config = ConfigDict(extra="forbid")

    acceptance: TargetTransferAcceptance
    signature: str = Field(min_length=1, max_length=2048)


def create_room_routes(
    core: CommunicationCore,
    body_and_actor: BodyAndActor,
    decode_base64: DecodeBase64,
) -> list[Route]:
    """Mount only room, meeting, membership, messaging, and transfer routes."""

    async def create_room(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomCreateBody.model_validate_json(body)
        core._require(
            actor=actor,
            action="room.create",
            resource="room:new",
            classification=parsed.classification,
            context={
                "classification": parsed.classification.value,
                "persistent": parsed.persistent,
                "expires_at": (
                    parsed.expires_at.isoformat() if parsed.expires_at else None
                ),
                "policy_digest": canonical_digest(parsed.policy or {}),
            },
        )
        result = core.rooms.create(
            actor=actor,
            collaboration_scope_id=parsed.collaboration_scope_id,
            classification=parsed.classification,
            persistent=parsed.persistent,
            expires_at=parsed.expires_at,
            policy=parsed.policy,
        )
        return JSONResponse(result, status_code=201)

    async def create_meeting(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = MeetingCreateBody.model_validate_json(body)
        core._require(
            actor=actor,
            action="room.create",
            resource="room:new",
            classification=parsed.classification,
            context={
                "classification": parsed.classification.value,
                "persistent": False,
                "expires_at": parsed.expires_at.isoformat(),
                "policy_digest": canonical_digest(parsed.policy or {}),
            },
        )
        result = core.rooms.create(
            actor=actor,
            collaboration_scope_id=parsed.collaboration_scope_id,
            classification=parsed.classification,
            persistent=False,
            expires_at=parsed.expires_at,
            policy=parsed.policy,
        )
        return JSONResponse(result, status_code=201)

    async def add_room_member(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomMemberBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        mls_key_package = (
            decode_base64(
                parsed.mls_key_package_b64,
                field="mls_key_package_b64",
            )
            if parsed.mls_key_package_b64 is not None
            else None
        )
        core._require(
            actor=actor,
            action="room.action",
            resource=room_id,
            context={
                "operation": "member.add",
                "harness_id": parsed.harness_id,
                "role": parsed.role,
                "mls_key_package_digest": (
                    hashlib.sha256(mls_key_package).hexdigest()
                    if mls_key_package is not None
                    else None
                ),
            },
        )
        result = core.rooms.add_member(
            actor=actor,
            collaboration_scope_id=parsed.collaboration_scope_id,
            room_id=room_id,
            harness_id=parsed.harness_id,
            role=parsed.role,
            mls_key_package=mls_key_package,
        )
        return JSONResponse(result, status_code=201)

    async def remove_room_member(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomMemberRemoveBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        core._require(
            actor=actor,
            action="room.action",
            resource=room_id,
            context={"operation": "member.remove", "harness_id": parsed.harness_id},
        )
        return JSONResponse(
            core.rooms.remove_member(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                room_id=room_id,
                harness_id=parsed.harness_id,
            )
        )

    async def describe_room(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomDescribeBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        core._require(actor=actor, action="room.read", resource=room_id)
        return JSONResponse(
            core.rooms.describe(
                actor=actor,
                collaboration_scope_id=parsed.collaboration_scope_id,
                room_id=room_id,
            )
        )

    async def send_room_message(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = RoomSendBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        core._require(
            actor=actor,
            action="room.action",
            resource=room_id,
            classification=parsed.classification,
            context={
                "operation": "message.send",
                "recipient_harness_ids": sorted(parsed.recipients),
                "payload_digest": canonical_digest(parsed.payload),
                "expected_control_sequence": parsed.expected_control_sequence,
            },
        )
        result = core.send_message(
            actor=actor,
            collaboration_scope_id=parsed.collaboration_scope_id,
            recipients=parsed.recipients,
            payload=parsed.payload,
            idempotency_key=parsed.idempotency_key,
            classification=parsed.classification,
            released_artifacts=parsed.released_artifacts,
            conversation_id=parsed.conversation_id,
            room_id=room_id,
            expected_room_control_sequence=parsed.expected_control_sequence,
        )
        return JSONResponse(result, status_code=202)

    async def propose_room_transfer(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TransferProposalBody.model_validate_json(body)
        room_id = request.path_params["room_id"]
        if parsed.proposal.room_id != room_id or parsed.snapshot.room_id != room_id:
            raise AuthorizationError("room transfer path binding mismatch")
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="room.transfer.propose",
            resource=room_id,
            context={
                "proposal_digest": parsed.proposal.digest,
                "snapshot_digest": parsed.snapshot.digest,
            },
        )
        return JSONResponse(
            core.room_governance.propose_transfer(
                actor=actor,
                proposal=parsed.proposal,
                snapshot=parsed.snapshot,
                signature=parsed.signature,
                additional_signatures=parsed.additional_signatures,
            ),
            status_code=202,
        )

    async def accept_room_transfer(request: Request) -> Response:
        body, actor = await body_and_actor(request, core)
        parsed = TransferAcceptanceBody.model_validate_json(body)
        transfer_id = request.path_params["transfer_id"]
        if parsed.acceptance.transfer_id != transfer_id:
            raise AuthorizationError("room transfer path binding mismatch")
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="room.transfer.accept",
            resource=f"room-transfer:{transfer_id}",
            context={"acceptance_digest": parsed.acceptance.digest},
        )
        return JSONResponse(
            core.room_governance.accept_target(
                actor=actor,
                acceptance=parsed.acceptance,
                signature=parsed.signature,
            )
        )

    async def commit_room_transfer(request: Request) -> Response:
        _body, actor = await body_and_actor(request, core)
        transfer_id = request.path_params["transfer_id"]
        with core.store.transaction(immediate=False) as connection:
            transfer = connection.execute(
                "SELECT target_domain_id,target_credential_id,state "
                "FROM room_transfers WHERE transfer_id=?",
                (transfer_id,),
            ).fetchone()
            if (
                transfer is None
                or transfer["state"] != "target_accepted"
                or transfer["target_domain_id"] != actor.domain_id
                or transfer["target_credential_id"] != actor.credential_id
            ):
                raise AuthorizationError("room transfer is not visible")
        core.outage.require_privileged()
        core._require(
            actor=actor,
            action="room.transfer.commit",
            resource=f"room-transfer:{transfer_id}",
        )
        return JSONResponse(core.room_governance.commit(transfer_id))

    return [
        Route("/v1/rooms", create_room, methods=["POST"]),
        Route("/v1/meetings", create_meeting, methods=["POST"]),
        Route("/v1/rooms/{room_id}", describe_room, methods=["POST"]),
        Route("/v1/rooms/{room_id}/members", add_room_member, methods=["POST"]),
        Route(
            "/v1/rooms/{room_id}/members/remove",
            remove_room_member,
            methods=["POST"],
        ),
        Route("/v1/rooms/{room_id}/messages", send_room_message, methods=["POST"]),
        Route(
            "/v1/rooms/{room_id}/transfers",
            propose_room_transfer,
            methods=["POST"],
        ),
        Route(
            "/v1/room-transfers/{transfer_id}/accept",
            accept_room_transfer,
            methods=["POST"],
        ),
        Route(
            "/v1/room-transfers/{transfer_id}/commit",
            commit_room_transfer,
            methods=["POST"],
        ),
    ]


__all__ = ["create_room_routes"]
