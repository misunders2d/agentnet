from __future__ import annotations

import hashlib
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pytest
from pydantic import ValidationError as PydanticValidationError

from agentnet.artifacts.local_destination import SafeDownloadDestination
from agentnet.bindings.tools import CanonicalToolDispatcher
from agentnet.discovery.directory import DirectoryRecord, DirectoryService
from agentnet.discovery.recipient_resolver import AuthorizedRecipientResolver
from agentnet.protocol.models import Classification
from agentnet.security.signatures import canonical_json
from tests.integration.test_file_send import TransferStack, _make_transfer_stack


_TRANSFER_KEYS = {
    "transfer_id",
    "state",
    "artifact_id",
    "event_id",
    "digest",
    "size",
    "media_type",
}
_DOWNLOAD_KEYS = {
    "artifact_id",
    "state",
    "destination_path",
    "digest",
    "size",
}


class BoundFileCore:
    """Bind production services while leaving public projection to the dispatcher."""

    def __init__(self, transfer, resolver: AuthorizedRecipientResolver) -> None:
        self.transfer = transfer
        self.recipient_resolver = resolver

    @staticmethod
    def _transfer_result(raw: dict[str, Any]) -> dict[str, Any]:
        return raw | {
            "digest": raw["expected_digest"],
            "size": raw["expected_size"],
            "object_key": "must-not-cross-the-tool-boundary",
            "quarantine_path": "/must/not/cross/the/tool/boundary",
            "scanner_key": "must-not-cross-the-tool-boundary",
            "download_token": "must-not-cross-the-tool-boundary",
        }

    def file_send(
        self,
        *,
        actor,
        collaboration_scope_id: str,
        recipients: tuple[str, ...],
        source_path: str,
        media_type: str,
        classification: Classification,
        idempotency_key: str,
    ) -> dict[str, Any]:
        return self._transfer_result(
            self.transfer.send_file(
                collaboration_scope_id=collaboration_scope_id,
                actor=actor,
                recipients=recipients,
                source=Path(source_path),
                media_type=media_type,
                classification=classification,
                idempotency_key=idempotency_key,
            )
        )

    def file_status(
        self,
        *,
        actor,
        collaboration_scope_id: str,
        transfer_id: str,
    ) -> dict[str, Any]:
        return self._transfer_result(
            self.transfer.status(
                actor=actor,
                collaboration_scope_id=collaboration_scope_id,
                transfer_id=transfer_id,
            )
        )

    def file_download(
        self,
        *,
        actor,
        collaboration_scope_id: str,
        artifact_id: str,
        destination_path: str,
        idempotency_key: str,
    ) -> dict[str, Any]:
        raw = self.transfer.download_file(
            collaboration_scope_id=collaboration_scope_id,
            actor=actor,
            artifact_id=artifact_id,
            destination=Path(destination_path),
            idempotency_key=idempotency_key,
        )
        return raw | {
            "destination_path": raw["destination"],
            "digest": raw["plaintext_digest"],
            "download_token": "must-not-cross-the-tool-boundary",
            "object_key": "must-not-cross-the-tool-boundary",
        }


@dataclass
class FileToolStack:
    transfer_stack: TransferStack
    sender: CanonicalToolDispatcher
    recipient: CanonicalToolDispatcher
    download_root: Path


def _publish_recipient_profile(stack: TransferStack) -> AuthorizedRecipientResolver:
    display_name = "The enrolled server"
    alias = "the enrolled server"
    with stack.store.transaction() as connection:
        connection.execute(
            "UPDATE harnesses SET display_name=? WHERE harness_id=?",
            (display_name, stack.recipient.harness_id),
        )
        connection.execute(
            """INSERT INTO endpoint_lifecycle(
                domain_id,harness_id,principal_id,current_credential_id,harness_kind,
                profile_key,state,adapter_generation,mailbox_cursor,capability_root_digest,
                process_measurement,state_reason,revision,created_at,updated_at
            ) VALUES(?,?,?,?,?,?,'connected',1,0,NULL,NULL,'file tool contract',1,?,?)""",
            (
                stack.recipient.domain_id,
                stack.recipient.harness_id,
                stack.recipient.principal_id,
                stack.recipient.credential_id,
                "server",
                f"profile:{stack.recipient.harness_id}",
                stack.now,
                stack.now,
            ),
        )
        record = DirectoryRecord(
            record_id=f"agent:{stack.recipient.harness_id}",
            record_type="agent",
            domain_id=stack.recipient.domain_id,
            epoch=1,
            attributes={
                "harness_id": stack.recipient.harness_id,
                "approved_aliases": [alias],
            },
            visible_to_principal_ids=(stack.sender.principal_id,),
            expires_at=stack.now + 600,
        )
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
                stack.now,
            ),
        )
    return AuthorizedRecipientResolver(
        scopes=stack.scopes,
        directory=DirectoryService(stack.store),
        store=stack.store,
    )


@pytest.fixture
def file_tool_stack(store, identity_factory, tmp_path: Path) -> FileToolStack:
    stack = _make_transfer_stack(store, identity_factory, tmp_path)
    resolver = _publish_recipient_profile(stack)
    download_root = tmp_path / "downloads"
    download_root.mkdir(mode=0o700)
    transfer = stack.service(
        destination=SafeDownloadDestination(download_root),
    )
    core = BoundFileCore(transfer, resolver)
    return FileToolStack(
        transfer_stack=stack,
        sender=CanonicalToolDispatcher(core, lambda: stack.sender),
        recipient=CanonicalToolDispatcher(core, lambda: stack.recipient),
        download_root=download_root,
    )


def test_friendly_resolution_freezes_exact_recipient_before_strict_send(
    file_tool_stack: FileToolStack,
) -> None:
    resolved = file_tool_stack.sender.call(
        "agentnet.recipient.resolve",
        {"query": "  THE   enrolled\tserver "},
    )
    exact_recipients = tuple(endpoint["harness_id"] for endpoint in resolved)

    assert len(resolved) == 1
    assert exact_recipients == (file_tool_stack.transfer_stack.recipient.harness_id,)
    with pytest.raises(PydanticValidationError):
        file_tool_stack.sender.call(
            "agentnet.file.send",
            {
                "collaboration_scope_id": resolved[0]["scope_id"],
                "recipients": exact_recipients,
                "recipient_query": "the enrolled server",
                "source_path": str(file_tool_stack.transfer_stack.source),
                "media_type": "application/octet-stream",
                "classification": "C1",
                "idempotency_key": "file-tool-query-not-frozen-0001",
            },
        )

    sent = file_tool_stack.sender.call(
        "agentnet.file.send",
        {
            "collaboration_scope_id": resolved[0]["scope_id"],
            "recipients": exact_recipients,
            "source_path": str(file_tool_stack.transfer_stack.source),
            "media_type": "application/octet-stream",
            "classification": "C1",
            "idempotency_key": "file-tool-send-0001",
        },
    )

    assert set(sent) == _TRANSFER_KEYS
    assert sent["state"] == "recipient_committed"
    assert sent["digest"] == hashlib.sha256(
        file_tool_stack.transfer_stack.source.read_bytes()
    ).hexdigest()


def test_canonical_send_status_download_is_exact_idempotent_and_byte_identical(
    file_tool_stack: FileToolStack,
) -> None:
    source = file_tool_stack.transfer_stack.source
    content = source.read_bytes()
    digest = hashlib.sha256(content).hexdigest()
    resolved = file_tool_stack.sender.call(
        "agentnet.recipient.resolve",
        {"query": "the enrolled server"},
    )
    recipients = tuple(endpoint["harness_id"] for endpoint in resolved)
    sent = file_tool_stack.sender.call(
        "agentnet.file.send",
        {
            "collaboration_scope_id": resolved[0]["scope_id"],
            "recipients": recipients,
            "source_path": str(source),
            "media_type": "application/octet-stream",
            "classification": "C1",
            "idempotency_key": "file-tool-send-download-0001",
        },
    )
    status = file_tool_stack.sender.call(
        "agentnet.file.status",
        {
            "collaboration_scope_id": resolved[0]["scope_id"],
            "transfer_id": sent["transfer_id"],
        },
    )
    destination = file_tool_stack.download_root / "received.bin"
    request = {
        "collaboration_scope_id": resolved[0]["scope_id"],
        "artifact_id": sent["artifact_id"],
        "destination_path": str(destination),
        "idempotency_key": "file-tool-download-0001",
    }
    downloaded = file_tool_stack.recipient.call(
        "agentnet.file.download",
        request,
    )
    repeated = file_tool_stack.recipient.call(
        "agentnet.file.download",
        request,
    )

    assert set(sent) == _TRANSFER_KEYS
    assert set(status) == _TRANSFER_KEYS
    assert set(downloaded) == _DOWNLOAD_KEYS
    assert status == sent
    assert repeated == downloaded
    assert downloaded == {
        "artifact_id": sent["artifact_id"],
        "state": "materialized",
        "destination_path": str(destination),
        "digest": digest,
        "size": len(content),
    }
    assert destination.read_bytes() == content
    assert list(file_tool_stack.download_root.glob(".received.bin.*.agentnet")) == []
    assert file_tool_stack.transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM download_capabilities"
    )["count"] == 1
    assert file_tool_stack.transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM download_capabilities WHERE consumed_at IS NULL"
    )["count"] == 0
    assert file_tool_stack.transfer_stack.store.fetch_one(
        "SELECT COUNT(*) AS count FROM events"
    )["count"] == 1
